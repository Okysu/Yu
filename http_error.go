package yu

import (
	"fmt"
	"net/http"
)

// HTTPError lets a skill fail a request with a specific HTTP status code.
//
// The wrapped error is logged server-side; the client receives only the status
// text. Status codes outside 400-599 fall back to 500. Return it as the last
// (error) return value of a skill:
//
//	func validate(req yu.Request) (Input, error) {
//		if req.QueryDefault("token", "") == "" {
//			return Input{}, &yu.HTTPError{Status: 401, Err: errMissingToken}
//		}
//		...
//	}
//
// HTTPError only maps the status. It does not prevent sibling skills in the
// same wave from running; use a typed dependency if ordering matters.
type HTTPError struct {
	Status int
	Err    error
}

// Error implements error.
func (e *HTTPError) Error() string {
	text := http.StatusText(e.Status)
	if text == "" {
		text = "unknown status"
	}
	if e.Err == nil {
		return fmt.Sprintf("http %d %s", e.Status, text)
	}
	return fmt.Sprintf("http %d %s: %v", e.Status, text, e.Err)
}

// Unwrap exposes the wrapped error to errors.Is/As.
func (e *HTTPError) Unwrap() error {
	return e.Err
}
