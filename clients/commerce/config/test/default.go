package main

import (
	"log"
	"net/http"
	"os"

	"github.com/hanzoai/cloud/clients/commerce/util/default_"

	// Imported for side-effect, ensures tasks are registered
	_ "github.com/hanzoai/cloud/clients/commerce/test-integration/datastore/tasks"
	_ "github.com/hanzoai/cloud/clients/commerce/test-integration/util/task/tasks"
)

func init() {
	default_.Init()
}

func main() {
	// Get port from environment or default to 8080
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Starting server on :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
