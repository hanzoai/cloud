package ae

import (
	"context"
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/datastore/query"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

// NewContext creates a new test context with in-memory SQLite backend.
// This replaces the App Engine aetest functionality with a lightweight
// in-memory database suitable for unit testing.
func NewContext(args ...Options) Context {
	mu.Lock()
	defer mu.Unlock()

	// Share Context if possible
	Counter++

	if SharedContext != nil {
		return SharedContext
	}

	var opts Options

	// Parse options
	switch len(args) {
	case 0:
		opts = defaults()
	case 1:
		opts = defaults(args[0])
	default:
		log.Panic("At most one ae.Options argument may be supplied.")
	}

	// Create temporary directory for test data
	tempDir, err := os.MkdirTemp("", "commerce-test-*")
	if err != nil {
		log.Panic("Failed to create temp directory: %v", err)
	}

	// Configure the database manager with in-memory/temp SQLite
	cfg := db.DefaultConfig()
	cfg.DataDir = tempDir
	cfg.UserDataDir = filepath.Join(tempDir, "users")
	cfg.OrgDataDir = filepath.Join(tempDir, "orgs")
	cfg.EnableDatastore = false // Use SQLite only for tests
	cfg.EnableVectorSearch = false
	cfg.IsDev = opts.Debug

	// Create database manager
	manager, err := db.NewManager(cfg)
	if err != nil {
		os.RemoveAll(tempDir)
		log.Panic("Failed to create database manager: %v", err)
	}

	// Get an organization database for the test app
	database, err := manager.Org(opts.AppID)
	if err != nil {
		manager.Close()
		os.RemoveAll(tempDir)
		log.Panic("Failed to create test database: %v", err)
	}

	// Create a context with the test database embedded
	ctx := context.Background()
	ctx = context.WithValue(ctx, "testDB", database)
	ctx = context.WithValue(ctx, "testAppID", opts.AppID)
	ctx = context.WithValue(ctx, "testTempDir", tempDir)

	// Set global defaults so datastore.New() and query.New() work
	datastore.SetDefaultDB(database)
	query.SetDefaultDB(database)

	// Return context with database embedded
	SharedContext = &testContext{
		Context:  ctx,
		database: database,
		manager:  manager,
		tempDir:  tempDir,
	}
	return SharedContext
}

// testContext wraps a standard context with test infrastructure
type testContext struct {
	context.Context
	database db.DB
	manager  *db.Manager
	tempDir  string
}

// DB returns the underlying database for direct access in tests
func (c *testContext) DB() db.DB {
	return c.database
}

// Close cleans up the test context
func (c *testContext) Close() {
	mu.Lock()
	defer mu.Unlock()

	Counter--

	if Counter > 0 {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			log.Warn("Recovered from panic in context.Close()")
		}
	}()

	if c.manager != nil {
		c.manager.Close()
		c.manager = nil
	}

	// Clean up temp directory
	if c.tempDir != "" {
		os.RemoveAll(c.tempDir)
		c.tempDir = ""
	}

	SharedContext = nil
}
