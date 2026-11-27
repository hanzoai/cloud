// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"cmp"
	"context"
	"fmt"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/zap-proto/zip"
)

// The caller as one person, published on the internal plane.
//
// A person reaches cloud with two kinds of credential, and each presents its own
// subject: a sign-in token its `sub`, the id of their user row, and an API key
// the row's `<owner>/<name>`. A record one person holds, keyed on whichever
// credential made the call, is two records for one person. The row is what both
// name, so this answers it.

// exposeSelf publishes the person read. Mount calls it.
func exposeSelf() {
	cloud.Plane().Post("/iam/self",
		self,
		zip.WithOperationID(iam.IAMSelf),
		zip.WithSummary("The caller as one person, whichever credential they called with"))
}

// self answers the CALLER's own user row: its id and its `<owner>/<name>`.
//
// The subject is the caller's and never an argument, as email states. A subject
// no row holds — an application acting as itself, a person since deleted — is a
// refusal, never somebody's row.
func self(ctx context.Context, _ *cloud.Unit) (*iam.Self, error) {
	who := cloud.Who(ctx)
	if who.User == "" {
		return nil, zip.ErrUnauthorized("self: no subject on the call")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("self: identity store not open in the process that owns it")
	}
	u, err := iamstore.GetUserBySubject(ctx, db, who.User)
	if err != nil {
		return nil, fmt.Errorf("self: %w", err)
	}
	if u == nil || u.IsDeleted {
		return nil, zip.ErrUnauthorized("self: no such subject")
	}
	row := u.Owner + "/" + u.Name
	return &iam.Self{ID: cmp.Or(u.Id, row), Row: row}, nil
}
