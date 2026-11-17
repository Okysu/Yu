package yu_test

import (
	"fmt"
	"net/http/httptest"

	yu "github.com/Okysu/Yu"
)

type greetingUser struct{ Name string }

func Example() {
	engine := yu.New()

	engine.GET("/hello/:name",
		func(req yu.Request) greetingUser {
			return greetingUser{Name: req.Param("name")}
		},
		func(u greetingUser) yu.Response {
			return yu.Text(200, "hello, "+u.Name)
		},
	)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("GET", "/hello/yu", nil))
	fmt.Println(w.Body.String())

	// Output:
	// hello, yu
}
