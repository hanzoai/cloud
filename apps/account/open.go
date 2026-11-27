package account

import (
	"net/http"

	"github.com/hanzoai/cloud/sdk/spec"
)

// An avatar is an <img src> from any origin, addressed by its content digest, so it needs no credential.
func init() {
	spec.Open("/v1/account/avatar/:org/:user/:digest", http.MethodGet)
}
