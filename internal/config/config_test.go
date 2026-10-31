package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || *c != (Config{}) {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestSaveLoadRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	in := &Config{URL: "https://pi.hole", Password: "pw", Insecure: true, Timeout: "5s", Output: "json"}
	if err := in.Save(path); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, %v", st.Mode(), err)
	}
	out, err := Load(path)
	if err != nil || *out != *in {
		t.Fatalf("got %+v, %v", out, err)
	}
}

func TestLoadRejectsUnknownKeysAndBadValues(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"typo.json":    `{"ulr":"x"}`,
		"timeout.json": `{"timeout":"soon"}`,
		"output.json":  `{"output":"xml"}`,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestEnvOverridesFile(t *testing.T) {
	t.Setenv(EnvURL, "http://env")
	t.Setenv(EnvPassword, "envpw")
	t.Setenv(EnvInsecure, "true")
	t.Setenv(EnvOutput, "")
	c := &Config{URL: "http://file", PasswordCommand: "echo filepw"}
	if err := c.ApplyEnv(); err != nil {
		t.Fatal(err)
	}
	pw, _ := c.ResolvePassword()
	if c.URL != "http://env" || pw != "envpw" || !c.Insecure {
		t.Fatalf("got %+v (password %q)", c, pw)
	}
}

func TestPasswordCommand(t *testing.T) {
	c := &Config{PasswordCommand: "printf 's3cret\\n'"}
	pw, err := c.ResolvePassword()
	if err != nil || pw != "s3cret" {
		t.Fatalf("got %q, %v", pw, err)
	}
	c.PasswordCommand = "exit 3"
	if _, err := c.ResolvePassword(); err == nil {
		t.Fatal("expected failure")
	}
}

func TestSetAndRedact(t *testing.T) {
	c := &Config{}
	if err := c.Set("insecure", "yes"); err == nil {
		t.Fatal("expected bool parse error")
	}
	if err := c.Set("bogus", "1"); err == nil {
		t.Fatal("expected unknown key error")
	}
	if err := c.Set("password", "pw"); err != nil {
		t.Fatal(err)
	}
	if r := c.Redacted(); r.Password != "********" || c.Password != "pw" {
		t.Fatalf("redacted %+v, original %+v", r, c)
	}
}
