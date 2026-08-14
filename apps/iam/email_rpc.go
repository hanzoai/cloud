// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/zap-proto/zip"
)

// The caller's address and whether they proved it, published on the internal
// plane.
//
// It exists because an address a person merely TYPED is not evidence, and
// something downstream was treating one as an identity. The coding path derives
// a forge login from the local part of an address, which maps many addresses
// onto one login — so on a shared signup org, naming an address beginning with
// a colleague's local part was enough to act as that colleague. Requiring the
// address to be CONFIRMED is what turns it back into a fact about a person.
//
// The proof lives here and nowhere else reachable: the token carries `email` and
// no `email_verified` (internal/oidc/jwt.go), while the account record carries
// EmailVerified and a direct password signup writes it FALSE until the address is
// confirmed. Asking the store answers for every token that already exists, where
// a new claim would answer only for tokens minted after it shipped.

// exposeEmail publishes the address read. Mount calls it.
func exposeEmail() {
	zip.Post[struct{}, plane.Email](cloud.Plane(), "/iam/email",
		email,
		zip.WithOperationID(plane.IAMEmail),
		zip.WithSummary("The caller's address, and whether they have proved it"))
}

// email answers with the CALLER'S OWN address and verification state.
//
// The subject is the caller's and can never be an argument, exactly as approval
// states: a caller able to name a subject could read another person's address,
// which is a leak on its own and an account oracle in bulk.
//
// A subject with no user row is a REFUSAL rather than an unverified answer. The
// two would gate the same way today, but they are different facts — "this person
// has not confirmed their address" is about a person, and "this principal is not
// a person" is about the credential — and a caller that ever wants to tell them
// apart should not have to guess which one it got.
func email(ctx context.Context, _ *struct{}) (*plane.Email, error) {
	who := cloud.Who(ctx)
	if who.User == "" {
		return nil, zip.ErrUnauthorized("email: no subject on the call")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("email: identity store not open in the process that owns it")
	}
	u, err := iamstore.GetUserBySubject(ctx, db, who.User)
	if err != nil {
		return nil, fmt.Errorf("email: %w", err)
	}
	if u == nil {
		return nil, zip.ErrUnauthorized("email: no such subject")
	}
	return &plane.Email{Address: u.Email, Verified: u.EmailVerified}, nil
}
