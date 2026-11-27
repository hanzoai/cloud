package kms

import (
	"testing"

	"github.com/hanzoai/cloud/internal/authztest"
	"github.com/hanzoai/cloud/sdk"
)

// TestAuthorizationContract holds kms's routes and tools to the shared
// authorization contract (internal/authztest).
func TestAuthorizationContract(t *testing.T) {
	authztest.Run(t, authztest.Contract{
		Plugin: cloud.Plugin{Name: "kms", Price: cloud.Free, Use: Use, OwnsHealth: true},
	})
}
