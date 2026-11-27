package share

// members.go — whether a person holds a seat in an org, asked of IAM over the plane.
// It is its own file because it names the generated IAM peer (client/iam), whose
// package name is the one sdk/iam also takes, and an import name is per file.

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/sdk"
)

func memberOfOrg(parent context.Context, org string, subjects []string) bool {
	for _, u := range subjects {
		pctx, cancel := planeCtx(parent, org)
		out, err := iam.IAMMembers(pctx, &client.Scope{User: u, Any: true})
		cancel()
		if errors.Is(err, cloud.ErrNoPeer) {
			return false
		}
		if err == nil && out != nil && len(out.Memberships) > 0 {
			return true
		}
	}
	return false
}
