// Command advanced demonstrates the less obvious parts of Yu:
//
//   - a group node that joins the DAG (there is no middleware concept)
//   - a cache lookup that exits early on a hit and falls through on a miss
//   - distinct types for each refined value, so causality stays unambiguous
//   - per-key serialization with PartitionBy
//   - route timeouts and request body limits
//
// Try:
//
//	curl -i http://localhost:8080/api/items/7 \
//	  -H 'X-Request-ID: demo' \
//	  -H 'Content-Type: application/json' \
//	  -d '{"id":"7","value":9}'
package main

import (
	"log"
	"time"

	yu "github.com/Okysu/Yu"
)

type Item struct {
	ID    string `json:"id"`
	Value int    `json:"value"`
}

// Each refinement of the item gets its own type. A type has exactly one
// producer, which keeps the causal graph unambiguous.
type SanitizedItem Item
type StoredItem Item

// CacheMiss lets parseItem depend on the lookup result. On a hit, cacheLookup
// returns a non-zero yu.Response and the rest of the pipeline never runs.
type CacheMiss struct {
	Miss bool
}

// RequestID is produced by a node registered on the group and consumed by the
// final response skill.
type RequestID string

func requestID(req yu.Request) RequestID {
	id := req.Get("X-Request-ID")
	if id == "" {
		id = "anonymous"
	}
	return RequestID(id)
}

func cacheLookup(req yu.Request) (yu.Response, CacheMiss) {
	// A real implementation would return a cached yu.JSON response here.
	return yu.Response{}, CacheMiss{Miss: true}
}

func parseItem(miss CacheMiss, req yu.Request) (Item, error) {
	if !miss.Miss {
		return Item{}, nil // unreachable: a cache hit already terminated the route
	}

	var item Item
	if err := req.BindJSON(&item); err != nil {
		return Item{}, err
	}
	return item, nil
}

func sanitize(item Item) SanitizedItem {
	if item.Value < 0 {
		item.Value = 0
	}
	return SanitizedItem(item)
}

func store(item SanitizedItem) (StoredItem, error) {
	// A real implementation would write to a database here.
	return StoredItem(item), nil
}

func reply(id RequestID, item StoredItem) yu.Response {
	return yu.JSON(200, map[string]any{
		"request_id": string(id),
		"item":       item,
	})
}

func main() {
	engine := yu.New()

	api := engine.Group("/api", requestID)

	api.POST("/items/:id",
		yu.WithTimeout(2*time.Second),
		yu.WithBodyLimit(1<<20),
		// Requests with the same :id are pinned to the same lane and run
		// serially; different ids run in parallel.
		yu.PartitionBy(func(req yu.Request) string {
			return req.Param("id")
		}),
		cacheLookup,
		parseItem,
		sanitize,
		store,
		reply,
	)

	engine.EnableDebugDAG("/debug/dag")

	log.Println("advanced example listening on :8080")
	if err := engine.Run(":8080"); err != nil {
		log.Fatal(err)
	}
}
