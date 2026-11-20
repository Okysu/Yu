package yu

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type engineGroupUser struct{ Name string }
type engineSvc struct{ Name string }
type enginePairA struct{}
type enginePairB struct{}
type engineIDValue struct{ ID string }

type engineRawStatus struct{ status int }

func (h engineRawStatus) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(h.status)
}

func serve(e *Engine, method, target string, body io.Reader) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(method, target, body))
	return w
}

func TestEngineBasicRoutes(t *testing.T) {
	e := New()

	e.GET("/ping", func(req Request) Response {
		return Text(http.StatusOK, "pong")
	})
	e.GET("/users/:id", func(req Request) Response {
		return JSON(http.StatusOK, map[string]string{
			"id": req.Param("id"),
			"q":  req.QueryDefault("q", "none"),
			"ua": req.Get("X-Test"),
		})
	})
	e.POST("/echo", func(req Request) Response {
		var body map[string]string
		if err := req.BindJSON(&body); err != nil {
			return Text(http.StatusBadRequest, "bad json")
		}
		return Text(http.StatusOK, body["msg"])
	})
	e.GET("/body", func(req Request) Response {
		return Text(http.StatusOK, string(req.Body()))
	})

	w := serve(e, "GET", "/ping", nil)
	if w.Code != http.StatusOK || w.Body.String() != "pong" {
		t.Fatalf("GET /ping = %d %q", w.Code, w.Body.String())
	}

	req := httptest.NewRequest("GET", "/users/42?q=hi", nil)
	req.Header.Set("X-Test", "yes")
	uw := httptest.NewRecorder()
	e.ServeHTTP(uw, req)

	var got map[string]string
	if err := json.Unmarshal(uw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["id"] != "42" || got["q"] != "hi" || got["ua"] != "yes" {
		t.Fatalf("GET /users/42 = %v", got)
	}

	w = serve(e, "POST", "/echo", strings.NewReader(`{"msg":"hello"}`))
	if w.Code != http.StatusOK || w.Body.String() != "hello" {
		t.Fatalf("POST /echo = %d %q", w.Code, w.Body.String())
	}

	w = serve(e, "POST", "/echo", strings.NewReader("not-json"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON = %d, want 400", w.Code)
	}

	w = serve(e, "GET", "/body", strings.NewReader("raw-bytes"))
	if w.Body.String() != "raw-bytes" {
		t.Fatalf("GET /body = %q", w.Body.String())
	}
}

func TestEngineNotFound(t *testing.T) {
	e := New()
	e.GET("/ping", func(req Request) Response { return Text(200, "pong") })

	if w := serve(e, "GET", "/missing", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing path = %d, want 404", w.Code)
	}
}

func TestEngineMethodNotAllowed(t *testing.T) {
	e := New()
	e.GET("/ping", func(req Request) Response { return Text(200, "pong") })
	e.POST("/ping", func(req Request) Response { return Text(200, "pong") })

	w := serve(e, "DELETE", "/ping", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method = %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow != "GET, POST" {
		t.Fatalf("Allow = %q, want %q", allow, "GET, POST")
	}
}

func TestEngineTimeout(t *testing.T) {
	e := New()
	e.GET("/slow", WithTimeout(20*time.Millisecond), func(req Request) Response {
		select {
		case <-time.After(time.Second):
			return Text(200, "late")
		case <-req.Context.Done():
			return Response{}
		}
	})

	w := serve(e, "GET", "/slow", nil)
	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout = %d, want 504 (body %q)", w.Code, w.Body.String())
	}
}

func TestEngineTimeoutPreemptsSingleNode(t *testing.T) {
	e := New()
	e.GET("/slow-sync", WithTimeout(20*time.Millisecond), func(req Request) Response {
		time.Sleep(300 * time.Millisecond)
		return Text(200, "late")
	})

	start := time.Now()
	w := serve(e, "GET", "/slow-sync", nil)
	elapsed := time.Since(start)

	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout = %d, want 504", w.Code)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("timed single-node wave was not preempted: %v", elapsed)
	}
}

func TestEngineHTTPError(t *testing.T) {
	e := New()
	e.GET("/bad",
		func(req Request) (engineSvc, error) {
			return engineSvc{}, &HTTPError{Status: http.StatusBadRequest, Err: errors.New("bad input")}
		},
		func(engineSvc) Response { return Text(200, "ok") },
	)

	w := serve(e, "GET", "/bad", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != "Bad Request" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestEngineClose(t *testing.T) {
	e := New()
	e.GET("/ping", func(req Request) Response { return Text(200, "pong") })
	e.GET("/items",
		PartitionBy(func(req Request) string { return "k" }, 2),
		func(req Request) Response { return Text(200, "item") },
	)

	e.Close()
	e.Close()

	if w := serve(e, "GET", "/ping", nil); w.Code != http.StatusOK {
		t.Fatalf("non-partitioned route after Close = %d", w.Code)
	}
	if w := serve(e, "GET", "/items", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("partitioned route after Close = %d, want 503", w.Code)
	}
}

func TestEnginePanicIsolation(t *testing.T) {
	e := New()
	e.GET("/panic", func(req Request) Response {
		panic("boom")
	})

	w := serve(e, "GET", "/panic", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("panic = %d, want 500", w.Code)
	}
	if got := w.Body.String(); got != "500 Internal Server Error\n" {
		t.Fatalf("internal details leaked to the client: %q", got)
	}
}

func TestEngineJSONMarshalFailure(t *testing.T) {
	e := New()
	e.GET("/bad-json", func(req Request) Response {
		return JSON(200, make(chan int))
	})

	w := serve(e, "GET", "/bad-json", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("marshal failure = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "failed to marshal") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestEngineProvideSingleton(t *testing.T) {
	e := New()
	e.Provide(&engineSvc{Name: "yu"})
	e.GET("/svc", func(s *engineSvc) Response {
		return Text(200, s.Name)
	})

	w := serve(e, "GET", "/svc", nil)
	if w.Code != http.StatusOK || w.Body.String() != "yu" {
		t.Fatalf("GET /svc = %d %q", w.Code, w.Body.String())
	}
}

func TestEnginePartitionedRoute(t *testing.T) {
	e := New()
	e.GET("/items",
		PartitionBy(func(req Request) string { return req.QueryDefault("id", "") }, 4),
		func(req Request) Response {
			return Text(200, "item")
		},
	)

	w := serve(e, "GET", "/items?id=7", nil)
	if w.Code != http.StatusOK || w.Body.String() != "item" {
		t.Fatalf("GET /items = %d %q", w.Code, w.Body.String())
	}
}

func TestEngineGroupNodesJoinCausalDAG(t *testing.T) {
	e := New()
	groupNode := func(req Request) engineGroupUser {
		return engineGroupUser{Name: "yu"}
	}

	g := e.Group("/api", groupNode)
	g.GET("/me", func(u engineGroupUser) Response {
		return Text(200, u.Name)
	})

	w := serve(e, "GET", "/api/me", nil)
	if w.Code != http.StatusOK || w.Body.String() != "yu" {
		t.Fatalf("GET /api/me = %d %q", w.Code, w.Body.String())
	}

	entry := e.router.entries["GET-/api/me"]
	if entry == nil {
		t.Fatal("route not registered")
	}
	if len(entry.dag.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (group node + route node)", len(entry.dag.Nodes))
	}
	if len(entry.dag.Waves) != 2 {
		t.Fatalf("waves = %d, want 2 (type-wired, not registration-ordered)", len(entry.dag.Waves))
	}
}

func TestEngineNestedGroupsAndUse(t *testing.T) {
	e := New()
	api := e.Group("/api")
	v1 := api.Group("/v1")
	v1.Use(func(req Request) engineGroupUser { return engineGroupUser{Name: "v1"} })
	v1.GET("/who", func(u engineGroupUser) Response { return Text(200, u.Name) })

	if w := serve(e, "GET", "/api/v1/who", nil); w.Body.String() != "v1" {
		t.Fatalf("GET /api/v1/who = %d %q", w.Code, w.Body.String())
	}
	if w := serve(e, "GET", "/v1/who", nil); w.Code != http.StatusNotFound {
		t.Fatalf("prefix leak: %d", w.Code)
	}
}

func TestEngineDebugEndpoint(t *testing.T) {
	e := New()
	e.GET("/ping", func(req Request) Response { return Text(200, "pong") })
	e.Raw("/raw", func(w http.ResponseWriter, req *http.Request) {})
	e.EnableDebugDAG("/_yu/topology")

	w := serve(e, "GET", "/_yu/topology?format=json", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("debug json = %d", w.Code)
	}

	var tops []RouteTopologyDTO
	if err := json.Unmarshal(w.Body.Bytes(), &tops); err != nil {
		t.Fatalf("decode topologies: %v", err)
	}

	var foundPing bool
	for _, topo := range tops {
		if topo.RouteKey == "GET-/_yu/topology" {
			t.Fatalf("internal route must not list itself")
		}
		if topo.RouteKey == "GET-/raw" {
			t.Fatalf("raw route must not appear in DAG topologies")
		}
		if topo.RouteKey == "GET-/ping" {
			foundPing = true
			if topo.TotalWaves != 1 || len(topo.Waves) != 1 || len(topo.Waves[0]) != 1 {
				t.Fatalf("GET-/ping topology = %+v", topo)
			}
		}
	}
	if !foundPing {
		t.Fatal("GET-/ping missing from debug topologies")
	}

	w = serve(e, "GET", "/_yu/topology", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("debug html = %d", w.Code)
	}
	html := w.Body.String()
	if !strings.Contains(html, "Deterministic Concurrency Topology") {
		t.Fatal("debug HTML missing title")
	}
	if !strings.Contains(html, `id="route-search"`) {
		t.Fatal("debug HTML missing search input")
	}
	if !strings.Contains(html, `id="theme-toggle"`) || !strings.Contains(html, `data-theme="light"`) {
		t.Fatal("debug HTML missing light/dark theme support")
	}
}

func TestEngineBodyLimit(t *testing.T) {
	e := New()
	e.POST("/upload", WithBodyLimit(8), func(req Request) Response {
		return Text(200, string(req.Body()))
	})

	if w := serve(e, "POST", "/upload", strings.NewReader("12345")); w.Code != http.StatusOK {
		t.Fatalf("small body = %d, want 200", w.Code)
	}

	w := serve(e, "POST", "/upload", strings.NewReader("1234567890"))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413", w.Code)
	}
}

func TestEngineProvideRejectsDuplicate(t *testing.T) {
	e := New()
	e.Provide(&engineSvc{Name: "a"})

	expectPanic(t, func() {
		e.Provide(&engineSvc{Name: "b"})
	})
}

func TestEngineProvideRejectsTypedNil(t *testing.T) {
	e := New()
	var svc *engineSvc

	expectPanic(t, func() {
		e.Provide(svc)
	})
}

// trackActive records the peak number of concurrently running skills.
func trackActive(active, peak *int32) {
	n := atomic.AddInt32(active, 1)
	for {
		p := atomic.LoadInt32(peak)
		if n <= p || atomic.CompareAndSwapInt32(peak, p, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	atomic.AddInt32(active, -1)
}

func TestEngineInlineWaveLimit(t *testing.T) {
	t.Run("parallel by default", func(t *testing.T) {
		var active, peak int32
		e := New()
		e.GET("/pair",
			func(req Request) enginePairA { trackActive(&active, &peak); return enginePairA{} },
			func(req Request) enginePairB { trackActive(&active, &peak); return enginePairB{} },
			func(enginePairA, enginePairB) Response { return Text(200, "ok") },
		)

		if w := serve(e, "GET", "/pair", nil); w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if peak != 2 {
			t.Fatalf("peak concurrency = %d, want 2", peak)
		}
	})

	t.Run("inlined wave", func(t *testing.T) {
		var active, peak int32
		e := New()
		e.GET("/pair", WithInlineWaveLimit(2),
			func(req Request) enginePairA { trackActive(&active, &peak); return enginePairA{} },
			func(req Request) enginePairB { trackActive(&active, &peak); return enginePairB{} },
			func(enginePairA, enginePairB) Response { return Text(200, "ok") },
		)

		if w := serve(e, "GET", "/pair", nil); w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		if peak != 1 {
			t.Fatalf("peak concurrency = %d, want 1", peak)
		}

		entry := e.router.entries["GET-/pair"]
		if entry == nil || entry.dag.inlineWaveLimit != 2 {
			t.Fatalf("inlineWaveLimit not applied")
		}
	})
}

func TestEngineRawHandler(t *testing.T) {
	e := New()
	var gotID string

	e.Raw("/raw/:id", func(w http.ResponseWriter, req *http.Request) {
		gotID = req.PathValue("id")
		w.Header().Set("X-Raw", "1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("raw"))
	})

	w := serve(e, "POST", "/raw/42", nil)
	if w.Code != http.StatusCreated || w.Body.String() != "raw" {
		t.Fatalf("raw = %d %q", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Raw") != "1" {
		t.Fatal("raw handler header missing")
	}
	if gotID != "42" {
		t.Fatalf("PathValue = %q", gotID)
	}

	// Raw registers every method.
	if w := serve(e, "DELETE", "/raw/7", nil); w.Code != http.StatusCreated {
		t.Fatalf("raw route registered for only one method: %d", w.Code)
	}
}

func TestEngineHandleAdapter(t *testing.T) {
	e := New()
	e.Handle("/third-party", engineRawStatus{status: http.StatusTeapot})

	if w := serve(e, "GET", "/third-party", nil); w.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", w.Code)
	}
}

func TestEngineRawBypassesDAG(t *testing.T) {
	e := New()
	g := e.Group("/g", func(req Request) Response {
		panic("group node must not run for raw routes")
	})
	g.Raw("/raw", func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	if w := serve(e, "GET", "/g/raw", nil); w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("raw group route = %d %q", w.Code, w.Body.String())
	}
}

func TestEngineConcurrentRequestsAreIsolated(t *testing.T) {
	e := New()
	e.GET("/id/:id",
		func(req Request) engineIDValue { return engineIDValue{ID: req.Param("id")} },
		func(v engineIDValue) Response { return Text(200, v.ID) },
	)

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest("GET", "/id/"+id, nil))
			if w.Code != http.StatusOK || w.Body.String() != id {
				t.Errorf("id %s got %d %q", id, w.Code, w.Body.String())
			}
		}(strconv.Itoa(i))
	}
	wg.Wait()
}
