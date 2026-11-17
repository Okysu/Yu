// Command sse streams server-sent events.
//
// The DAG path produces a fully buffered yu.Response, so streaming belongs in a
// raw net/http handler. Raw handlers receive the untouched request and
// http.ResponseWriter, including http.Flusher.
//
// Try:
//
//	curl -N http://localhost:8080/events
package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	yu "github.com/Okysu/Yu"
)

func main() {
	engine := yu.New()

	engine.Raw("/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for i := 1; i <= 10; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				_, _ = fmt.Fprintf(w, "id: %d\ndata: tick %d\n\n", i, i)
				flusher.Flush()
			}
		}
	})

	log.Println("sse example listening on :8080")
	if err := engine.Run(":8080"); err != nil {
		log.Fatal(err)
	}
}
