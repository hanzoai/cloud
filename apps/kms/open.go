package kms

import (
	"net/http"

	"github.com/hanzoai/cloud/sdk/spec"
)

// The routes a caller reaches with no credential: the probe, the console's runtime config, and the login that exchanges a client credential for a token.
func init() {
	spec.Open("/v1/kms/health", http.MethodGet)
	spec.Open("/v1/kms/config", http.MethodGet)
	spec.Open("/v1/kms/auth/login", http.MethodPost)
}
