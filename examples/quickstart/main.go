// Command quickstart runs a minimal Yu server.
//
// Two independent skills load a user and user stats in parallel; a third skill
// renders both values as JSON. Run it and try:
//
//	curl http://localhost:8080/ping
//	curl http://localhost:8080/users/42
//	open http://localhost:8080/debug/dag
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

// loadUser and loadStats both depend only on yu.Request, so they are placed in
// the same wave and run concurrently.
func loadUser(req yu.Request) (User, error) {
	return User{ID: 1, Name: "user-" + req.Param("id")}, nil
}

func loadStats(req yu.Request) (Stats, error) {
	return Stats{Orders: 3}, nil
}

// render runs after both values are available.
func render(user User, stats Stats) yu.Response {
	return yu.JSON(200, map[string]any{
		"user":  user,
		"stats": stats,
	})
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

	log.Println("quickstart listening on :8080")
	if err := engine.Run(":8080"); err != nil {
		log.Fatal(err)
	}
}
