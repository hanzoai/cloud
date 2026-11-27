package iam

import (
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/internal/authztest"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
)

// TestAuthorizationContract holds iam's routes and tools to the shared
// authorization contract (internal/authztest), over a store that can sign.
func TestAuthorizationContract(t *testing.T) {
	authztest.Run(t, authztest.Contract{
		Plugin: cloud.Plugin{Name: "iam", Price: cloud.Free, Use: Use, Prefixes: manifest.PrefixesFor("iam")},
		// The protocol IAM serves to callers who have no credential yet. IAM
		// decides public by the group a route is registered on (hanzoai/iam
		// internal/routes), and its document does not yet mark them
		// `security: []`; until it does, they are named here by prefix.
		Floors: map[string]authztest.Floor{
			"/.well-known":        authztest.Open,
			"/v1/iam/.well-known": authztest.Open,
			"/v1/iam/oauth":       authztest.Open,
			"/v1/iam/webauthn":    authztest.Open,
			"/v1/iam/web3":        authztest.Open,
			"/v1/iam/auth":        authztest.Open,
			"/v1/iam/registry":    authztest.Open,
			"/v1/iam/stats":       authztest.Open, // public aggregate user/org counts, zero PII
			"/v1/iam/accounts":    authztest.Open, // this browser's signed-in accounts, from its cookie
		},
		Data: func(t *testing.T) string {
			dir := storeWithSigningCert(t, "cert-contract")
			mountSigningKey(t, "cert-contract")
			t.Setenv("initDataFile", filepath.Join(dir, "absent.json"))
			t.Setenv("IAM_ISSUER", "https://hanzo.id")
			return dir
		},
	})
}
