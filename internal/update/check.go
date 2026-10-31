package update

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CheckInterval is how often the passive update notice asks GitHub for the latest release.
const CheckInterval = 24 * time.Hour

const checkTimeout = 2 * time.Second

type checkState struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest,omitempty"`
}

// Notice returns a one-line "update available" message, or "" when current is up to date or
// not a release build. It asks GitHub at most once per CheckInterval, remembering the answer in
// statePath; a failed check is remembered too, so an offline machine doesn't retry every run.
// If ctx is canceled mid-check nothing is recorded and the next run tries again.
func (c *Client) Notice(ctx context.Context, current, statePath string, now time.Time) string {
	if !IsRelease(current) {
		return ""
	}
	var st checkState
	if data, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	if now.Sub(st.CheckedAt) >= CheckInterval || now.Before(st.CheckedAt) {
		cctx, cancel := context.WithTimeout(ctx, checkTimeout)
		rel, err := c.Latest(cctx)
		cancel()
		if ctx.Err() != nil {
			return ""
		}
		st.CheckedAt = now
		if err == nil {
			st.Latest = rel.Tag
		}
		saveState(statePath, st)
	}
	if !Newer(current, st.Latest) {
		return ""
	}
	return fmt.Sprintf("holectl %s is available (you have %s): run 'holectl update'",
		strings.TrimPrefix(st.Latest, "v"), strings.TrimPrefix(current, "v"))
}

func saveState(path string, st checkState) {
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
