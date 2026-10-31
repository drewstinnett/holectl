package cli

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// listFlag is a repeatable flag that also splits on commas: --group a --group b,c.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Type() string   { return "list" }
func (l *listFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

// optBool is a bool flag that remembers whether it was given, for partial updates.
type optBool struct {
	set bool
	val bool
}

func (b *optBool) String() string   { return strconv.FormatBool(b.val) }
func (b *optBool) IsBoolFlag() bool { return true }
func (b *optBool) Type() string     { return "bool" }
func (b *optBool) Set(v string) error {
	p, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	b.set, b.val = true, p
	return nil
}

// choice is a string flag restricted to fixed values.
type choice struct {
	val     string
	allowed []string
}

func newChoice(allowed ...string) *choice { return &choice{allowed: allowed} }
func (c *choice) String() string {
	if c == nil {
		return ""
	}
	return c.val
}
func (c *choice) Type() string { return strings.Join(c.allowed, "|") }
func (c *choice) Set(v string) error {
	for _, a := range c.allowed {
		if v == a {
			c.val = v
			return nil
		}
	}
	return fmt.Errorf("must be one of %s", strings.Join(c.allowed, ", "))
}

// timeNow is swappable in tests.
var timeNow = time.Now

// parseWhen accepts unix seconds, RFC 3339, "2006-01-02[ 15:04[:05]]" (local time), or a
// duration ago such as 24h, 90m or 7d. It returns unix seconds.
func parseWhen(s string, now time.Time) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "now" {
		return now.Unix(), nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix(), nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Unix(), nil
		}
	}
	d := strings.TrimPrefix(s, "-")
	if days, ok := strings.CutSuffix(d, "d"); ok {
		if n, err := strconv.ParseFloat(days, 64); err == nil {
			return now.Add(-time.Duration(n * 24 * float64(time.Hour))).Unix(), nil
		}
	}
	if dur, err := time.ParseDuration(d); err == nil {
		return now.Add(-dur).Unix(), nil
	}
	return 0, fmt.Errorf("can't parse time %q: use unix seconds, RFC 3339, YYYY-MM-DD[ HH:MM], or a duration ago like 24h or 7d", s)
}

// timeRange adds from/until to q. until defaults to now when only from is given.
func timeRange(q url.Values, from, until string) error {
	now := timeNow()
	if from == "" && until == "" {
		return nil
	}
	if from == "" {
		return fmt.Errorf("--until requires --from")
	}
	f, err := parseWhen(from, now)
	if err != nil {
		return err
	}
	u := now.Unix()
	if until != "" {
		if u, err = parseWhen(until, now); err != nil {
			return err
		}
	}
	q.Set("from", strconv.FormatInt(f, 10))
	q.Set("until", strconv.FormatInt(u, 10))
	return nil
}
