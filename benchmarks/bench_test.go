// Package benchmarks compares Yu with several popular Go HTTP frameworks on
// the same business routes.
//
// Every implementation serves:
//
//	GET  /ping            plain text
//	GET  /users/:id       path parameter, JSON response
//	POST /users           JSON body in, JSON body out
//	GET  /compute         three independent CPU tasks merged into one response
//
// Yu places the three CPU tasks in one wave and runs them concurrently; the
// other frameworks call them sequentially. This is the same business logic
// expressed in each framework's natural style.
package benchmarks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-chi/chi/v5"
	"github.com/gofiber/fiber/v2"
	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"

	yu "github.com/Okysu/Yu"
)

const (
	manyRoutes    = 500
	computeRounds = 200000
	ioDelay       = 2 * time.Millisecond
)

type userResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type userRequest struct {
	Name string `json:"name"`
}

func digest(seed uint64) uint64 {
	h := seed
	for i := 0; i < computeRounds; i++ {
		h ^= h << 13
		h ^= h >> 7
		h ^= h << 17
	}
	return h
}

// Distinct output types let Yu run the three tasks in one wave.
type (
	digestA struct{ V uint64 }
	digestB struct{ V uint64 }
	digestC struct{ V uint64 }
)

func taskA(req yu.Request) digestA { return digestA{V: digest(11)} }
func taskB(req yu.Request) digestB { return digestB{V: digest(22)} }
func taskC(req yu.Request) digestC { return digestC{V: digest(33)} }

func mergeDigests(a digestA, b digestB, c digestC) yu.Response {
	return yu.JSON(http.StatusOK, map[string]uint64{"sum": a.V + b.V + c.V})
}

// Simulated downstream I/O: three independent calls, each latency-bound.
type (
	ioA struct{ V int }
	ioB struct{ V int }
	ioC struct{ V int }
)

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func ioTaskA(req yu.Request) (ioA, error) {
	if err := sleepCtx(req.Context, ioDelay); err != nil {
		return ioA{}, err
	}
	return ioA{V: 1}, nil
}

func ioTaskB(req yu.Request) (ioB, error) {
	if err := sleepCtx(req.Context, ioDelay); err != nil {
		return ioB{}, err
	}
	return ioB{V: 2}, nil
}

func ioTaskC(req yu.Request) (ioC, error) {
	if err := sleepCtx(req.Context, ioDelay); err != nil {
		return ioC{}, err
	}
	return ioC{V: 3}, nil
}

func mergeIO(a ioA, b ioB, c ioC) yu.Response {
	return yu.JSON(http.StatusOK, map[string]int{"sum": a.V + b.V + c.V})
}

// Tiny skills used to measure wave scheduling overhead.
type (
	tinyA struct{ V int }
	tinyB struct{ V int }
)

func tinyTaskA(req yu.Request) tinyA { return tinyA{V: 1} }
func tinyTaskB(req yu.Request) tinyB { return tinyB{V: 2} }

func tinyMerge(a tinyA, b tinyB) yu.Response {
	return yu.Text(http.StatusOK, "ok")
}

func mergeIO2(a ioA, b ioB) yu.Response {
	return yu.JSON(http.StatusOK, map[string]int{"sum": a.V + b.V})
}

type framework struct {
	name  string
	build func(extraRoutes int) (baseURL string, stop func(), err error)
}

func frameworks() []framework {
	return []framework{
		{name: "stdlib", build: buildStdlib},
		{name: "gin", build: buildGin},
		{name: "gin-errgroup", build: buildGinErrgroup},
		{name: "echo", build: buildEcho},
		{name: "chi", build: buildChi},
		{name: "fiber", build: buildFiber},
		{name: "yu", build: buildYu},
	}
}

func startHTTP(handler http.Handler) (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), func() { _ = srv.Close() }, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func buildStdlib(extraRoutes int) (string, func(), error) {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("pong"))
	})
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, req *http.Request) {
		id := req.PathValue("id")
		writeJSON(w, userResponse{ID: id, Name: "user-" + id})
	})
	mux.HandleFunc("POST /users", func(w http.ResponseWriter, req *http.Request) {
		var in userRequest
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		writeJSON(w, userResponse{ID: "1", Name: in.Name})
	})
	mux.HandleFunc("GET /compute", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, map[string]uint64{"sum": digest(11) + digest(22) + digest(33)})
	})
	mux.HandleFunc("GET /io", func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(3 * ioDelay)
		writeJSON(w, map[string]int{"sum": 1 + 2 + 3})
	})
	for i := 0; i < extraRoutes; i++ {
		mux.HandleFunc("GET "+fmt.Sprintf("/r%d", i), func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("ok"))
		})
	}

	return startHTTP(mux)
}

func buildGin(extraRoutes int) (string, func(), error) {
	return buildGinVariant(extraRoutes, false)
}

func buildGinErrgroup(extraRoutes int) (string, func(), error) {
	return buildGinVariant(extraRoutes, true)
}

func buildGinVariant(extraRoutes int, concurrent bool) (string, func(), error) {
	r := gin.New()

	r.GET("/ping", func(c *gin.Context) {
		c.String(http.StatusOK, "pong")
	})
	r.GET("/users/:id", func(c *gin.Context) {
		id := c.Param("id")
		c.JSON(http.StatusOK, userResponse{ID: id, Name: "user-" + id})
	})
	r.POST("/users", func(c *gin.Context) {
		var in userRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			c.String(http.StatusBadRequest, "bad json")
			return
		}
		c.JSON(http.StatusOK, userResponse{ID: "1", Name: in.Name})
	})

	if concurrent {
		r.GET("/compute", func(c *gin.Context) {
			var eg errgroup.Group
			var a, b, cc uint64
			eg.Go(func() error { a = digest(11); return nil })
			eg.Go(func() error { b = digest(22); return nil })
			eg.Go(func() error { cc = digest(33); return nil })
			if err := eg.Wait(); err != nil {
				_ = c.Error(err)
				return
			}
			c.JSON(http.StatusOK, map[string]uint64{"sum": a + b + cc})
		})
		r.GET("/io", func(c *gin.Context) {
			var eg errgroup.Group
			eg.Go(func() error { time.Sleep(ioDelay); return nil })
			eg.Go(func() error { time.Sleep(ioDelay); return nil })
			eg.Go(func() error { time.Sleep(ioDelay); return nil })
			if err := eg.Wait(); err != nil {
				_ = c.Error(err)
				return
			}
			c.JSON(http.StatusOK, map[string]int{"sum": 1 + 2 + 3})
		})
	} else {
		r.GET("/compute", func(c *gin.Context) {
			c.JSON(http.StatusOK, map[string]uint64{"sum": digest(11) + digest(22) + digest(33)})
		})
		r.GET("/io", func(c *gin.Context) {
			time.Sleep(3 * ioDelay)
			c.JSON(http.StatusOK, map[string]int{"sum": 1 + 2 + 3})
		})
	}

	for i := 0; i < extraRoutes; i++ {
		r.GET(fmt.Sprintf("/r%d", i), func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})
	}

	return startHTTP(r)
}

func buildEcho(extraRoutes int) (string, func(), error) {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.GET("/ping", func(c echo.Context) error {
		return c.String(http.StatusOK, "pong")
	})
	e.GET("/users/:id", func(c echo.Context) error {
		id := c.Param("id")
		return c.JSON(http.StatusOK, userResponse{ID: id, Name: "user-" + id})
	})
	e.POST("/users", func(c echo.Context) error {
		var in userRequest
		if err := c.Bind(&in); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "bad json")
		}
		return c.JSON(http.StatusOK, userResponse{ID: "1", Name: in.Name})
	})
	e.GET("/compute", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]uint64{"sum": digest(11) + digest(22) + digest(33)})
	})
	e.GET("/io", func(c echo.Context) error {
		time.Sleep(3 * ioDelay)
		return c.JSON(http.StatusOK, map[string]int{"sum": 1 + 2 + 3})
	})
	for i := 0; i < extraRoutes; i++ {
		e.GET(fmt.Sprintf("/r%d", i), func(c echo.Context) error {
			return c.String(http.StatusOK, "ok")
		})
	}

	return startHTTP(e)
}

func buildChi(extraRoutes int) (string, func(), error) {
	r := chi.NewRouter()

	r.Get("/ping", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("pong"))
	})
	r.Get("/users/{id}", func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "id")
		writeJSON(w, userResponse{ID: id, Name: "user-" + id})
	})
	r.Post("/users", func(w http.ResponseWriter, req *http.Request) {
		var in userRequest
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		writeJSON(w, userResponse{ID: "1", Name: in.Name})
	})
	r.Get("/compute", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, map[string]uint64{"sum": digest(11) + digest(22) + digest(33)})
	})
	r.Get("/io", func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(3 * ioDelay)
		writeJSON(w, map[string]int{"sum": 1 + 2 + 3})
	})
	for i := 0; i < extraRoutes; i++ {
		r.Get(fmt.Sprintf("/r%d", i), func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("ok"))
		})
	}

	return startHTTP(r)
}

func buildFiber(extraRoutes int) (string, func(), error) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})

	app.Get("/ping", func(c *fiber.Ctx) error {
		return c.SendString("pong")
	})
	app.Get("/users/:id", func(c *fiber.Ctx) error {
		id := c.Params("id")
		return c.JSON(userResponse{ID: id, Name: "user-" + id})
	})
	app.Post("/users", func(c *fiber.Ctx) error {
		var in userRequest
		if err := c.BodyParser(&in); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "bad json")
		}
		return c.JSON(userResponse{ID: "1", Name: in.Name})
	})
	app.Get("/compute", func(c *fiber.Ctx) error {
		return c.JSON(map[string]uint64{"sum": digest(11) + digest(22) + digest(33)})
	})
	app.Get("/io", func(c *fiber.Ctx) error {
		time.Sleep(3 * ioDelay)
		return c.JSON(map[string]int{"sum": 1 + 2 + 3})
	})
	for i := 0; i < extraRoutes; i++ {
		app.Get(fmt.Sprintf("/r%d", i), func(c *fiber.Ctx) error {
			return c.SendString("ok")
		})
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	go func() { _ = app.Listener(ln) }()
	return "http://" + ln.Addr().String(), func() { _ = app.Shutdown() }, nil
}

func buildYu(extraRoutes int) (string, func(), error) {
	engine := yu.New()

	engine.GET("/ping", func(req yu.Request) yu.Response {
		return yu.Text(200, "pong")
	})
	engine.GET("/users/:id", func(req yu.Request) yu.Response {
		id := req.Param("id")
		return yu.JSON(200, userResponse{ID: id, Name: "user-" + id})
	})
	engine.POST("/users", func(req yu.Request) yu.Response {
		var in userRequest
		if err := req.BindJSON(&in); err != nil {
			return yu.Text(400, "bad json")
		}
		return yu.JSON(200, userResponse{ID: "1", Name: in.Name})
	})
	engine.GET("/compute", taskA, taskB, taskC, mergeDigests)
	engine.GET("/io", ioTaskA, ioTaskB, ioTaskC, mergeIO)
	for i := 0; i < extraRoutes; i++ {
		engine.GET(fmt.Sprintf("/r%d", i), func(req yu.Request) yu.Response {
			return yu.Text(200, "ok")
		})
	}

	return startHTTP(engine)
}

func newClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			MaxIdleConnsPerHost: 4096,
			DisableCompression:  true,
			ForceAttemptHTTP2:   false,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 30 * time.Second,
	}
}

func doRequest(client *http.Client, method, url string, body []byte) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: status %d", method, url, resp.StatusCode)
	}
	return nil
}

func benchmarkAll(b *testing.B, method, path string, body []byte, extraRoutes int) {
	for _, fw := range frameworks() {
		fw := fw
		b.Run(fw.name, func(b *testing.B) {
			baseURL, stop, err := fw.build(extraRoutes)
			if err != nil {
				b.Fatal(err)
			}
			defer stop()

			client := newClient()
			defer client.CloseIdleConnections()
			url := baseURL + path

			// Warm up connections and the server before measuring.
			for i := 0; i < 20; i++ {
				if err := doRequest(client, method, url, body); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			b.ResetTimer()

			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if err := doRequest(client, method, url, body); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}

func benchmarkAllSequential(b *testing.B, method, path string, body []byte, extraRoutes int) {
	for _, fw := range frameworks() {
		fw := fw
		b.Run(fw.name, func(b *testing.B) {
			baseURL, stop, err := fw.build(extraRoutes)
			if err != nil {
				b.Fatal(err)
			}
			defer stop()

			client := newClient()
			defer client.CloseIdleConnections()
			url := baseURL + path

			for i := 0; i < 20; i++ {
				if err := doRequest(client, method, url, body); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				if err := doRequest(client, method, url, body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPing(b *testing.B) {
	benchmarkAll(b, http.MethodGet, "/ping", nil, 0)
}

func BenchmarkPingLatency(b *testing.B) {
	benchmarkAllSequential(b, http.MethodGet, "/ping", nil, 0)
}

func BenchmarkGetUser(b *testing.B) {
	benchmarkAll(b, http.MethodGet, "/users/42", nil, 0)
}

func BenchmarkCreateUser(b *testing.B) {
	benchmarkAll(b, http.MethodPost, "/users", []byte(`{"name":"alice"}`), 0)
}

func BenchmarkFanout(b *testing.B) {
	benchmarkAll(b, http.MethodGet, "/compute", nil, 0)
}

// BenchmarkFanoutLatency measures single-request latency. With most cores
// idle, Yu's intra-request parallelism can overlap the three CPU tasks.
func BenchmarkFanoutLatency(b *testing.B) {
	benchmarkAllSequential(b, http.MethodGet, "/compute", nil, 0)
}

func BenchmarkIOFanout(b *testing.B) {
	benchmarkAll(b, http.MethodGet, "/io", nil, 0)
}

// BenchmarkIOFanoutLatency measures an I/O-bound fan-out with one client and
// reports p50/p99. Sequential implementations sleep three times; errgroup,
// hand-written Gin concurrency, and Yu overlap the three downstream calls.
func BenchmarkIOFanoutLatency(b *testing.B) {
	benchmarkLatencyDistribution(b, http.MethodGet, "/io")
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func benchmarkLatencyDistribution(b *testing.B, method, path string) {
	for _, fw := range frameworks() {
		fw := fw
		b.Run(fw.name, func(b *testing.B) {
			baseURL, stop, err := fw.build(0)
			if err != nil {
				b.Fatal(err)
			}
			defer stop()

			client := newClient()
			defer client.CloseIdleConnections()
			url := baseURL + path

			for i := 0; i < 10; i++ {
				if err := doRequest(client, method, url, nil); err != nil {
					b.Fatal(err)
				}
			}

			durations := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				start := time.Now()
				if err := doRequest(client, method, url, nil); err != nil {
					b.Fatal(err)
				}
				durations = append(durations, time.Since(start))
			}
			b.StopTimer()

			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			b.ReportMetric(float64(percentile(durations, 50))/float64(time.Microsecond), "p50-us")
			b.ReportMetric(float64(percentile(durations, 99))/float64(time.Microsecond), "p99-us")
		})
	}
}

func BenchmarkManyRoutes(b *testing.B) {
	benchmarkAll(b, http.MethodGet, fmt.Sprintf("/r%d", manyRoutes-1), nil, manyRoutes)
}

// BenchmarkYuCompile measures startup compilation for a single-route DAG.
func BenchmarkYuCompile(b *testing.B) {
	skill := func(req yu.Request) yu.Response { return yu.Text(200, "ok") }

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = yu.CompileDAG("GET-/bench", nil, skill)
	}
}

// benchSink keeps results observable so the compiler cannot eliminate them.
var (
	benchSink uint64
	benchResp yu.Response
)

// BenchmarkYuDAGFanout isolates the engine: three independent skills in one
// wave, then a merge skill.
func BenchmarkYuDAGFanout(b *testing.B) {
	dag := yu.CompileDAG("GET-/compute", nil, taskA, taskB, taskC, mergeDigests)

	var last yu.Response
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := dag.Run(context.Background(), nil, yu.Request{})
		if err != nil {
			b.Fatal(err)
		}
		last = resp
	}
	benchResp = last
}

// BenchmarkYuSequentialFanout is the same computation without the engine.
func BenchmarkYuSequentialFanout(b *testing.B) {
	var acc uint64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		acc = digest(11) + digest(22) + digest(33)
	}
	benchSink = acc
}

func BenchmarkDigest(b *testing.B) {
	var acc uint64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		acc = digest(11)
	}
	benchSink = acc
}

// BenchmarkYuTinyWave compares parallel and inlined execution for a wave of
// two CPU-trivial skills.
func BenchmarkYuTinyWave(b *testing.B) {
	for _, mode := range []struct {
		name  string
		limit int
	}{
		{name: "parallel", limit: 1},
		{name: "inline", limit: 2},
	} {
		mode := mode
		b.Run(mode.name, func(b *testing.B) {
			dag := yu.CompileDAG("GET-/tiny", nil, tinyTaskA, tinyTaskB, tinyMerge)
			dag.SetInlineWaveLimit(mode.limit)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := dag.Run(context.Background(), nil, yu.Request{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkYuIOWave shows the same trade-off for a latency-bound wave: inlining
// serializes two 2 ms calls, while parallel execution overlaps them.
func BenchmarkYuIOWave(b *testing.B) {
	for _, mode := range []struct {
		name  string
		limit int
	}{
		{name: "parallel", limit: 1},
		{name: "inline", limit: 2},
	} {
		mode := mode
		b.Run(mode.name, func(b *testing.B) {
			dag := yu.CompileDAG("GET-/io2", nil, ioTaskA, ioTaskB, mergeIO2)
			dag.SetInlineWaveLimit(mode.limit)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := dag.Run(context.Background(), nil, yu.Request{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestMain(m *testing.M) {
	gin.SetMode(gin.ReleaseMode)
	log.SetOutput(io.Discard) // silence Yu's startup compiler log
	os.Exit(m.Run())
}
