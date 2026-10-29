package uploadsession

import (
	"fmt"
	"net/http"
)

// Error is returned by Manager methods and carries the HTTP status the API
// layer should answer with plus a stable machine-readable code.
type Error struct {
	Status     int
	Code       string
	Message    string
	RetryAfter int // seconds; sent as Retry-After when > 0
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func newErr(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

func busyErr(code string) *Error {
	retry := 1
	if code == "staging_window_full" {
		// Waiting for earlier parts to reach Telegram, which takes much longer
		// than a transient slot shortage.
		retry = 5
	}
	return &Error{Status: http.StatusTooManyRequests, Code: code, RetryAfter: retry}
}

var (
	errNotFound    = newErr(http.StatusNotFound, "session_not_found", "")
	errNotOpen     = newErr(http.StatusConflict, "session_not_open", "")
	errDisabled    = newErr(http.StatusServiceUnavailable, "chunked_upload_disabled", "")
	errBadChecksum = newErr(http.StatusBadRequest, "invalid_checksum", "checksum must be 64 lowercase hex characters (SHA-256)")
)
