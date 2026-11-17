# Quickstart

Yu turns a set of plain Go functions into a concurrent HTTP pipeline. This
guide builds a small server, explains what the engine did at startup, and
shows how to inspect the result.

## Requirements

- Go 1.26 or newer

## Install

```bash
go get github.com/Okysu/Yu
```

## A first server

```go
package main

import (
	"log"
	"time"

	yu "github.com/Okysu/Yu"
)

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Stats struct {
	Orders int `json:"orders"`
}

func loadUser(req yu.Request) (User, error) {
	return User{ID: 1, Name: "user-" + req.Param("id")}, nil
}

func loadStats(req yu.Request) (Stats, error) {
	return Stats{Orders: 3}, nil
}

func render(user User, stats Stats) yu.Response {
	return yu.JSON(200, map[string]any{"user": user, "stats": stats})
}

func main() {
	engine := yu.New()

	engine.GET("/ping", func(req yu.Request) yu.Response {
		return yu.Text(200, "pong")
	})

	engine.GET("/users/:id",
		yu.WithTimeout(2*time.Second),
		loadUser,
		loadStats,
		render,
	)

	engine.EnableDebugDAG("/debug/dag")

	log.Println("listening on :8080")
	if err := engine.Run(":8080"); err != nil {
		log.Fatal(err)
	}
}
```

Run it:

```bash
go run .
```

```bash
curl http://localhost:8080/ping
# pong

curl http://localhost:8080/users/42
# {"stats":{"orders":3},"user":{"id":1,"name":"user-42"}}
```

## What the engine did

At startup the engine reflected over the three skills and produced:

| wave | skill | why |
|---|---|---|
| 0 | `loadUser` | needs only `yu.Request` |
| 0 | `loadStats` | needs only `yu.Request`, so it runs **in parallel** with `loadUser` |
| 1 | `render` | needs `User` and `Stats`, both produced by wave 0 |

No dependency declarations, goroutines, or locks were written. Open
`http://localhost:8080/debug/dag` to see the same topology rendered, or
`/debug/dag?format=json` for the machine-readable form.

## Reading the request

`yu.Request` is frozen before the pipeline runs and exposes read-only
accessors:

| Accessor | Meaning |
|---|---|
| `req.Param("id")` | Path parameter, including `*filepath` catch-alls |
| `req.QueryDefault("q", "")` | First query value, or the default |
| `req.Get("X-Request-ID")` | First header value, case-insensitive |
| `req.Body()` | Raw body bytes |
| `req.BindJSON(&dst)` | Decode the body as JSON |
| `req.Context` | Context carrying timeout/cancellation |

## Returning a response

Any skill can terminate the route by returning a non-zero `yu.Response`:

```go
func rejectBadInput(req yu.Request) yu.Response {
	if req.QueryDefault("token", "") == "" {
		return yu.JSON(401, map[string]string{"error": "missing token"})
	}
	return yu.Response{} // zero response: continue the pipeline
}
```

A zero `Response` (`Status == 0` and empty body) is ignored and the pipeline
continues.

## Where to go next

- [Advanced usage](advanced.md): early exit, groups, partitioning,
  cancellation, testing.
- [API reference](api.md).
- [Benchmarks](benchmarks.md).
- Runnable examples: [`examples/quickstart`](../examples/quickstart) and
  [`examples/advanced`](../examples/advanced).
