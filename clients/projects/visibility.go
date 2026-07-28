package projects

import (
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// visibility.go — who can SEE a project, and who decides.
//
// There is exactly one axis, and it belongs to the publisher: public or
// private. Public is the default and is ungated, because a community nobody can
// enter without approval is a directory, and a directory does not grow. Private
// is the paid feature.
//
// That is the inverse of the arrangement this replaced, which gated the way IN
// (an admin-only `official` badge) and left the way out free. Gating entry
// suppresses exactly the thing the platform wants more of, and it failed on its
// own terms: the badge was unreachable by the script that published the
// platform's own catalogue, so 74 Hanzo apps were filed as somebody else's.
//
// AUTHORSHIP IS NOT STORED. Who made a project is its org — the account that
// pays for it — which the tenancy boundary already enforces and no request can
// forge. A tenant cannot publish into `hanzo` because it cannot fund `hanzo`.
// So there is nothing left for a badge to say that the org column does not
// already say, unforgeably.
//
// MODERATION IS SUBTRACTIVE. Project.Hidden is set from admin.hanzo.ai only and
// only ever removes. It is safe to be the one admin-gated field precisely
// because it cannot be used to promote anything, and because it leaves the
// publisher's own Visibility untouched — lifting a moderation restores exactly
// what they asked for, with no second write to get wrong.

const (
	// VisibilityPublic is the default: the project appears in the community
	// catalogue and its source is mirrored to hanzo-community on git.hanzo.ai.
	VisibilityPublic = "public"
	// VisibilityPrivate hides a project from the catalogue at the publisher's own
	// request. Paid: see visibilityFor.
	VisibilityPrivate = "private"

	// privateKind is the metering unit for keeping a project private. It shares
	// the ONE cloud.ResourceMeter every other paid surface uses (hosting, agents,
	// functions, s3), so "must have a paid account" is the existing funded-org
	// gate rather than a second notion of entitlement that could disagree with
	// billing. Fee 0 (operator-configured) makes private free and un-gated.
	privateKind = "private"
)

// visibilityFor resolves the visibility a create/update request asks for, and
// enforces the ONE rule: public is free, private requires a funded org.
//
// An empty request means public — a caller that says nothing gets the default
// the platform wants, not an error. An unfunded org asking for private is
// REFUSED (402), never silently published as public: quietly making somebody's
// private project public is the one failure mode here that cannot be undone.
func visibilityFor(s *cloud.Service[state], c *zip.Ctx, want string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(want)) {
	case "", VisibilityPublic:
		return VisibilityPublic, nil
	case VisibilityPrivate:
		fee := cloud.ResourceFeeCents(deployFeeEnvPrefix, privateKind)
		project, validated := principal.ValidatedProject(c)
		if err := s.State.bill.Gate(c.Context(), principal.Ledger(c), project, validated, privateKind, fee); err != nil {
			return "", err
		}
		return VisibilityPrivate, nil
	default:
		return "", zip.Errorf(http.StatusBadRequest,
			"visibility must be %q or %q", VisibilityPublic, VisibilityPrivate)
	}
}

// listed reports whether a project belongs in the public community catalogue:
// the publisher chose public AND moderation has not removed it. Both halves are
// plain values on the row, so this is the whole rule and there is nowhere else
// for a second copy of it to drift.
func (p Project) listed() bool {
	return p.Visibility == VisibilityPublic && !p.Hidden
}
