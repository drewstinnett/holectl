// Package config loads and saves holectl's JSON config file.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config is the on-disk config file (~/.config/holectl/config.json).
type Config struct {
	URL             string `json:"url,omitempty"`
	Password        string `json:"password,omitempty"`
	PasswordCommand string `json:"password_command,omitempty"`
	Insecure        bool   `json:"insecure,omitempty"`
	Timeout         string `json:"timeout,omitempty"`
	Output          string `json:"output,omitempty"`
}

// Keys lists settable keys with a description, in display order.
var Keys = []struct{ Name, Desc string }{
	{"url", "Pi-hole base URL, e.g. https://pi.hole or http://192.168.1.2"},
	{"password", "web interface password or app password (stored in plain text, file is 0600)"},
	{"password_command", "shell command whose stdout is the password, e.g. a keychain lookup"},
	{"insecure", "true to skip TLS certificate verification (Pi-hole's default cert is self-signed)"},
	{"timeout", "per-request timeout as a Go duration, e.g. 30s"},
	{"output", "default output format: auto, json or table"},
}

// Env vars that override the file.
const (
	EnvConfig   = "HOLECTL_CONFIG"
	EnvURL      = "HOLECTL_URL"
	EnvPassword = "HOLECTL_PASSWORD"
	EnvInsecure = "HOLECTL_INSECURE"
	EnvOutput   = "HOLECTL_OUTPUT"
)

// DefaultPath is $HOLECTL_CONFIG, else $XDG_CONFIG_HOME/holectl/config.json, else ~/.config/holectl/config.json.
func DefaultPath() string {
	if p := os.Getenv(EnvConfig); p != "" {
		return p
	}
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), "holectl", "config.json")
}

// CacheDir is $XDG_CACHE_HOME/holectl, else ~/.cache/holectl.
func CacheDir() string {
	return filepath.Join(xdgDir("XDG_CACHE_HOME", ".cache"), "holectl")
}

// SessionCachePath is where cached API sessions live.
func SessionCachePath() string {
	return filepath.Join(CacheDir(), "sessions.json")
}

func xdgDir(env, fallback string) string {
	if d := os.Getenv(env); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback
	}
	return filepath.Join(home, fallback)
}

// Load reads path. A missing file yields an empty config, not an error.
func Load(path string) (*Config, error) {
	c := &Config{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return c, c.Validate()
}

// Save writes the config with 0600 permissions, since it may hold a password.
func (c *Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Validate checks field formats.
func (c *Config) Validate() error {
	if c.Timeout != "" {
		if _, err := time.ParseDuration(c.Timeout); err != nil {
			return fmt.Errorf("invalid timeout %q: %w", c.Timeout, err)
		}
	}
	switch c.Output {
	case "", "auto", "json", "table":
	default:
		return fmt.Errorf("invalid output %q: want auto, json or table", c.Output)
	}
	return nil
}

// Set assigns a key by name.
func (c *Config) Set(key, value string) error {
	switch key {
	case "url":
		c.URL = value
	case "password":
		c.Password = value
	case "password_command":
		c.PasswordCommand = value
	case "insecure":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("insecure: %w", err)
		}
		c.Insecure = b
	case "timeout":
		c.Timeout = value
	case "output":
		c.Output = value
	default:
		return fmt.Errorf("unknown config key %q (known: %s)", key, strings.Join(KeyNames(), ", "))
	}
	return c.Validate()
}

// KeyNames returns the settable keys, sorted.
func KeyNames() []string {
	out := make([]string, 0, len(Keys))
	for _, k := range Keys {
		out = append(out, k.Name)
	}
	sort.Strings(out)
	return out
}

// ApplyEnv overlays HOLECTL_* environment variables.
func (c *Config) ApplyEnv() error {
	if v := os.Getenv(EnvURL); v != "" {
		c.URL = v
	}
	if v := os.Getenv(EnvPassword); v != "" {
		c.Password, c.PasswordCommand = v, ""
	}
	if v := os.Getenv(EnvInsecure); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s: %w", EnvInsecure, err)
		}
		c.Insecure = b
	}
	if v := os.Getenv(EnvOutput); v != "" {
		c.Output = v
	}
	return c.Validate()
}

// ResolvePassword returns Password, or runs PasswordCommand when Password is empty.
func (c *Config) ResolvePassword() (string, error) {
	if c.Password != "" || c.PasswordCommand == "" {
		return c.Password, nil
	}
	cmd := exec.Command("sh", "-c", c.PasswordCommand) //nolint:gosec // user-configured
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("password_command failed: %w", err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// TimeoutDuration parses Timeout, returning 0 when unset.
func (c *Config) TimeoutDuration() time.Duration {
	d, _ := time.ParseDuration(c.Timeout)
	return d
}

// Redacted returns a copy safe to print.
func (c *Config) Redacted() Config {
	r := *c
	if r.Password != "" {
		r.Password = "********"
	}
	return r
}
