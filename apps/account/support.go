package account

import (
	"context"
	"errors"
	"net/http"

	"github.com/hanzoai/cloud/sdk/principal"
)

// errSupport is a write this service would make to IAM for a support session.
var errSupport = errors.New("a support session cannot change the IAM account: the person does that signed in as themselves")

// supported is the floor under cloud's address table (impersonation.go): this
// client writes IAM with cloud's OWN credential — keys, organizations,
// memberships, the profile — so IAM's refusal of an impersonated bearer never
// sees the call. Any write reached from a support session stops here, whatever
// address reached it. A read passes.
func supported(ctx context.Context, method string) error {
	if method != http.MethodGet && principal.ActFrom(ctx).Sub != "" {
		return errSupport
	}
	return nil
}
