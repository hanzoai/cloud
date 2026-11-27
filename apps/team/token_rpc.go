package team

// token_rpc.go — the verdict on a presented team token, published on the plane.
//
// The HS256 key that signs a team token is team's: it mints them (account.go) and
// verifies them (identity.who). A peer that must decide on one — event, admitting
// an ingest write under the SPA's session — used to read the same key out of KMS
// and decode the token itself, which is a second copy of the key and a second
// reading of "verified". Now it asks, and only the verdict comes back.
//
// A deployment with no usable key answers Verified=false for every token, the same
// fail-closed posture Mount takes (resolveSecret): no key, no team credential.

import (
	"context"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// exposeToken publishes the verdict. Mount calls it with the configured key.
func exposeToken(secret string) {
	cloud.Plane().Post("/team/token",
		func(ctx context.Context, in *client.TokenIn) (*client.Token, error) {
			return verdict(secret, in), nil
		},
		zip.WithOperationID(client.TeamToken),
		zip.WithSummary("Whether a bearer is a team session token, and what it names"))
}

// verdict is the wire form of verdictOf.
func verdict(secret string, in *client.TokenIn) *client.Token {
	v := verdictOf(secret, in.Bearer)
	return &client.Token{Verified: v.verified, Org: v.org, Account: v.account, Privileged: v.privileged}
}
