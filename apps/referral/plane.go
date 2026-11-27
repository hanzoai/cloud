// Copyright © 2026 Hanzo AI. MIT License.

package referral

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// exposeCode publishes an org's referral code on the internal plane. Marketing
// asks it for the member a beta invite is from, so the invite's link carries
// their code; the org is an argument because the caller is a peer acting for the
// member, never the member over the edge.
func exposeCode(s *cloud.Service[state]) {
	cloud.Plane().Post("/referral/code",
		func(ctx context.Context, in *client.ReferralOrg) (*client.ReferralLink, error) {
			return codeOf(ctx, s, in)
		},
		zip.WithOperationID(client.ReferralCode),
		zip.WithSummary("One org's referral code and share link"))
}

// codeOf answers one org's referral code and share link: the same stable code
// GET /v1/referral answers that org, materialised on first ask. An empty org is
// refused rather than defaulted, because a code is attribution: defaulting it
// would credit someone else's signups to whoever the default names.
func codeOf(ctx context.Context, s *cloud.Service[state], in *client.ReferralOrg) (*client.ReferralLink, error) {
	org := ""
	if in != nil {
		org = strings.TrimSpace(in.Org)
	}
	if org == "" {
		return nil, zip.ErrBadRequest("referral: org is required")
	}
	code, err := s.State.store.EnsureCode(ctx, org)
	if err != nil {
		return nil, zip.Errorf(500, "referral code: %v", err)
	}
	return &client.ReferralLink{Code: code, Link: s.State.linkBase + "/?ref=" + code}, nil
}
