package pihole

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// APIError is a non-2xx response from the Pi-hole API. Pi-hole v6 reports errors as
// {"error": {"key": "...", "message": "...", "hint": ...}}; Key is the machine-readable part.
type APIError struct {
	Status  int    `json:"status"`
	Key     string `json:"key"`
	Message string `json:"message"`
	Hint    any    `json:"hint,omitempty"`
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Key != "" {
		msg = fmt.Sprintf("%s (%s, HTTP %d)", msg, e.Key, e.Status)
	} else {
		msg = fmt.Sprintf("%s (HTTP %d)", msg, e.Status)
	}
	if h, ok := e.Hint.(string); ok && h != "" {
		msg += ": " + h
	}
	return msg
}

// ErrDryRun is returned instead of sending a mutating request when the client is in dry-run mode.
var ErrDryRun = errors.New("dry run: request not sent")

// AuthError means the server rejected our credentials, or we have none and it wants some.
type AuthError struct {
	Reason string
	Err    error
}

func (e *AuthError) Error() string {
	if e.Err != nil {
		return e.Reason + ": " + e.Err.Error()
	}
	return e.Reason
}

func (e *AuthError) Unwrap() error { return e.Err }

// NetworkError wraps a transport-level failure (DNS, refused connection, TLS, timeout).
type NetworkError struct{ Err error }

func (e *NetworkError) Error() string { return "network error: " + e.Err.Error() }
func (e *NetworkError) Unwrap() error { return e.Err }

func parseAPIError(status int, body []byte) *APIError {
	e := &APIError{Status: status}
	var wrapper struct {
		Error struct {
			Key     string `json:"key"`
			Message string `json:"message"`
			Hint    any    `json:"hint"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &wrapper) == nil && (wrapper.Error.Key != "" || wrapper.Error.Message != "") {
		e.Key = wrapper.Error.Key
		e.Message = wrapper.Error.Message
		e.Hint = wrapper.Error.Hint
		return e
	}
	if len(body) > 0 && len(body) < 512 {
		e.Message = string(body)
	}
	return e
}
