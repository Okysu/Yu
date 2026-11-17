package yu

import (
	"errors"
	"strings"
	"testing"
)

func TestHTTPErrorStringAndUnwrap(t *testing.T) {
	base := errors.New("bad input")
	httpErr := &HTTPError{Status: 400, Err: base}

	if !errors.Is(httpErr, base) {
		t.Fatal("HTTPError does not unwrap its cause")
	}
	msg := httpErr.Error()
	if !strings.Contains(msg, "400") || !strings.Contains(msg, "bad input") {
		t.Fatalf("Error() = %q", msg)
	}

	unknown := &HTTPError{Status: 700}
	if !strings.Contains(unknown.Error(), "unknown status") {
		t.Fatalf("Error() = %q", unknown.Error())
	}
}
