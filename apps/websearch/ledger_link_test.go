package websearch

// This package's tests run against the deployment's app table and the production
// ledger, the way every plugin binary does.
import (
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
)
