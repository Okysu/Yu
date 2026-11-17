// Command middleware shows how to combine Yu with standard net/http
// middleware. Cross-cutting concerns live outside the engine: *yu.Engine is an
// http.Handler, so CORS, access logs, compression or metrics compose normally.
//
// Try:
//
//	curl -i http://localhost:8080/ping
//	curl -i -X OPTIONS http://localhost:8080/ping
package main

import (
	"log"
	"net/http"
	"time"

	yu "github.com/Okysu/Yu"
)

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start))
	})
}

// statusRecorder captures the status code. Streaming handlers would also need
// to forward Flush and Hijack; simple JSON/text routes do not.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func main() {
	engine := yu.New()

	engine.GET("/ping", func(req yu.Request) yu.Response {
		return yu.Text(200, "pong")
	})

	handler := withAccessLog(withCORS(engine))

	log.Println("middleware example listening on :8080")
	if err := http.ListenAndServe(":8080", handler); err != nil {
		log.Fatal(err)
	}
}
