package main

import (
	"net/http"
	"os"

	a "github.com/hanzoai/cloud/clients/commerce/api/api"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func main() {
	api := router.New("api")
	a.Route(api)

	// Get port from environment or default to 8080
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Start standard HTTP server
	http.ListenAndServe(":"+port, nil)
}
