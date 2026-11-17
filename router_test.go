package yu

import (
	"testing"
	"time"
)

func TestParsePattern(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"/", nil},
		{"", nil},
		{"/user/:id", []string{"user", ":id"}},
		{"/static/*filepath", []string{"static", "*filepath"}},
		{"/a/b/c/", []string{"a", "b", "c"}},
	}

	for _, c := range cases {
		got := parsePattern(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("parsePattern(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Fatalf("parsePattern(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestRouterStaticBeatsWildcard(t *testing.T) {
	orders := [][]string{
		{"/user/:id", "/user/new"},
		{"/user/new", "/user/:id"},
	}

	for _, order := range orders {
		r := newRouter()
		wildEntry := &routeEntry{timeout: time.Millisecond}
		staticEntry := &routeEntry{timeout: 2 * time.Millisecond}

		for _, pattern := range order {
			if pattern == "/user/new" {
				r.addRoute("GET", pattern, staticEntry)
			} else {
				r.addRoute("GET", pattern, wildEntry)
			}
		}

		entry, params := r.getRoute("GET", "/user/new")
		if entry != staticEntry {
			t.Fatalf("order %v: /user/new resolved to the wildcard entry", order)
		}
		if len(params) != 0 {
			t.Fatalf("order %v: /user/new got params %v", order, params)
		}

		entry, params = r.getRoute("GET", "/user/42")
		if entry != wildEntry {
			t.Fatalf("order %v: /user/42 did not resolve to the wildcard entry", order)
		}
		if params["id"] != "42" {
			t.Fatalf("order %v: /user/42 params = %v", order, params)
		}
	}
}

func TestRouterStaticAndWildcardSubtrees(t *testing.T) {
	r := newRouter()
	wild := &routeEntry{timeout: time.Millisecond}
	static := &routeEntry{timeout: 2 * time.Millisecond}

	r.addRoute("GET", "/files/:bucket/:key", wild)
	r.addRoute("GET", "/files/static/logo.png", static)

	entry, params := r.getRoute("GET", "/files/static/logo.png")
	if entry != static {
		t.Fatal("static subtree did not win over wildcard subtree")
	}
	if len(params) != 0 {
		t.Fatalf("static route got params %v", params)
	}

	entry, params = r.getRoute("GET", "/files/other/logo.png")
	if entry != wild || params["bucket"] != "other" || params["key"] != "logo.png" {
		t.Fatalf("wildcard subtree mismatch: entry=%p params=%v", entry, params)
	}

	// The static branch fails deeper, so search must backtrack to the wildcard.
	entry, params = r.getRoute("GET", "/files/static/other.png")
	if entry != wild || params["bucket"] != "static" || params["key"] != "other.png" {
		t.Fatalf("backtracking failed: entry=%p params=%v", entry, params)
	}
}

func TestRouterParams(t *testing.T) {
	r := newRouter()
	entry := &routeEntry{}
	r.addRoute("GET", "/api/:version/users/:id", entry)

	got, params := r.getRoute("GET", "/api/v1/users/42")
	if got != entry {
		t.Fatal("route not found")
	}
	if params["version"] != "v1" || params["id"] != "42" {
		t.Fatalf("params = %v", params)
	}
}

func TestRouterCatchAll(t *testing.T) {
	r := newRouter()
	entry := &routeEntry{}
	r.addRoute("GET", "/static/*filepath", entry)

	got, params := r.getRoute("GET", "/static/css/app.css")
	if got != entry {
		t.Fatal("catch-all route not found")
	}
	if params["filepath"] != "css/app.css" {
		t.Fatalf("filepath = %q", params["filepath"])
	}

	if got, _ := r.getRoute("GET", "/static"); got != nil {
		t.Fatal("catch-all route must require at least one segment")
	}
}

func TestRouterMethodIsolation(t *testing.T) {
	r := newRouter()
	entry := &routeEntry{}
	r.addRoute("POST", "/x", entry)

	if got, _ := r.getRoute("GET", "/x"); got != nil {
		t.Fatal("GET must not resolve a POST-only route")
	}
	if got, _ := r.getRoute("POST", "/x"); got != entry {
		t.Fatal("POST route not found")
	}
}

func TestRouterNoMatch(t *testing.T) {
	r := newRouter()
	r.addRoute("GET", "/a/:b", &routeEntry{})

	if got, _ := r.getRoute("GET", "/a/b/c"); got != nil {
		t.Fatal("path with extra segments must not match")
	}
	if got, _ := r.getRoute("GET", "/nope"); got != nil {
		t.Fatal("unknown path must not match")
	}
}

func TestRouterTrailingSlash(t *testing.T) {
	r := newRouter()
	entry := &routeEntry{}
	r.addRoute("GET", "/a/b", entry)

	if got, _ := r.getRoute("GET", "/a/b/"); got != entry {
		t.Fatal("trailing slash should match")
	}
}

func TestRouterStaticRouteAllocatesNoParams(t *testing.T) {
	r := newRouter()
	entry := &routeEntry{}
	r.addRoute("GET", "/static/path", entry)

	got, params := r.getRoute("GET", "/static/path")
	if got != entry {
		t.Fatal("static route not found")
	}
	if params != nil {
		t.Fatalf("static route allocated params: %v", params)
	}
}

func TestRouterWildcardRouteAllocatesParams(t *testing.T) {
	r := newRouter()
	entry := &routeEntry{}
	r.addRoute("GET", "/users/:id", entry)

	got, params := r.getRoute("GET", "/users/42")
	if got != entry {
		t.Fatal("wildcard route not found")
	}
	if params["id"] != "42" {
		t.Fatalf("params = %v", params)
	}
}

func TestRouterWildcardConflictPanics(t *testing.T) {
	r := newRouter()
	r.addRoute("GET", "/user/:id", &routeEntry{})

	expectPanic(t, func() {
		r.addRoute("GET", "/user/:name", &routeEntry{})
	})
}

func TestRouterEmptyPatternPanics(t *testing.T) {
	expectPanic(t, func() {
		newRouter().addRoute("GET", "", &routeEntry{})
	})
}
