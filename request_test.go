package yu

import (
	"net/http"
	"testing"
)

func TestRequestAccessors(t *testing.T) {
	req := Request{
		header: http.Header{"X-Test": []string{"a", "b"}},
		query:  map[string][]string{"q": {"x", "y"}},
		params: map[string]string{"id": "42"},
		body:   []byte(`{"msg":"hello"}`),
	}

	if got := req.Param("id"); got != "42" {
		t.Fatalf("Param(id) = %q", got)
	}
	if got := req.Param("missing"); got != "" {
		t.Fatalf("Param(missing) = %q", got)
	}

	if got := req.QueryDefault("q", "def"); got != "x" {
		t.Fatalf("QueryDefault(q) = %q", got)
	}
	if got := req.QueryDefault("missing", "def"); got != "def" {
		t.Fatalf("QueryDefault(missing) = %q", got)
	}

	// http.Header.Get is canonicalizing, so lookups are case-insensitive.
	if got := req.Get("X-Test"); got != "a" {
		t.Fatalf("Get(X-Test) = %q", got)
	}
	if got := req.Get("x-test"); got != "a" {
		t.Fatalf("Get(x-test) = %q", got)
	}
	if got := req.Get("Missing"); got != "" {
		t.Fatalf("Get(Missing) = %q", got)
	}

	if got := string(req.Body()); got != `{"msg":"hello"}` {
		t.Fatalf("Body() = %q", got)
	}

	var dst map[string]string
	if err := req.BindJSON(&dst); err != nil {
		t.Fatalf("BindJSON: %v", err)
	}
	if dst["msg"] != "hello" {
		t.Fatalf("BindJSON = %v", dst)
	}
}

func TestRequestZeroValueAccessors(t *testing.T) {
	var req Request

	if got := req.Param("id"); got != "" {
		t.Fatalf("Param = %q", got)
	}
	if got := req.QueryDefault("q", "def"); got != "def" {
		t.Fatalf("QueryDefault = %q", got)
	}
	if got := req.Get("X-Test"); got != "" {
		t.Fatalf("Get = %q", got)
	}
	if got := req.Body(); got != nil {
		t.Fatalf("Body = %v", got)
	}
	if err := req.BindJSON(&struct{}{}); err == nil {
		t.Fatal("BindJSON on empty body should fail")
	}
}
