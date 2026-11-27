package billing

import (
	"testing"

	"github.com/hanzoai/cloud/internal/authztest"
	"github.com/hanzoai/cloud/sdk"
)

// TestAuthorizationContract holds billing's routes and tools to the shared
// authorization contract (internal/authztest).
func TestAuthorizationContract(t *testing.T) {
	authztest.Run(t, authztest.Contract{
		Plugin: cloud.Plugin{Name: "billing", Price: cloud.Free, Use: Use},
	})
}
