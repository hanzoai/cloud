package ae

import (
	ctx "context"
	"sync"

	"github.com/hanzoai/cloud/clients/commerce/db"
)

var (
	SharedContext *testContext
	Counter       int
	mu            sync.Mutex
)

// Context interface provides a test context with database access
type Context interface {
	ctx.Context
	Close()
	DB() db.DB
}
