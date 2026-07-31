package projects

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/plane"
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
	// Public is the default: the project appears in the community
	// catalogue and its source is mirrored to hanzo-community on git.hanzo.ai.
	Public = "public"
	// Private hides a project from the catalogue at the publisher's own
	// request. Paid: see resolve.
	Private = "private"

	// privateKind is the metering unit for keeping a project private. It shares
	// the ONE cloud.ResourceMeter every other paid surface uses (hosting, agents,
	// functions, s3), so "must have a paid account" is the existing funded-org
	// gate rather than a second notion of entitlement that could disagree with
	// billing. Fee 0 (operator-configured) makes private free and un-gated.
	privateKind = "private"
)

// resolve resolves the visibility a create/update request asks for, and
// enforces the ONE rule: public is free, private requires a funded org.
//
// An empty request means public — a caller that says nothing gets the default
// the platform wants, not an error. An unfunded org asking for private is
// REFUSED (402), never silently published as public: quietly making somebody's
// private project public is the one failure mode here that cannot be undone.
func resolve(s *cloud.Service[state], c *zip.Ctx, want string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(want)) {
	case "", Public:
		return Public, nil
	case Private:
		fee := cloud.ResourceFeeCents(deployFeeEnvPrefix, privateKind)
		project, validated := principal.ValidatedProject(c)
		if err := s.State.bill.Gate(c.Context(), principal.Ledger(c), project, validated, privateKind, fee); err != nil {
			return "", err
		}
		return Private, nil
	default:
		return "", zip.Errorf(http.StatusBadRequest,
			"visibility must be %q or %q", Public, Private)
	}
}

// listed reports whether a project belongs in the public community catalogue:
// the publisher chose public AND moderation has not removed it. Both halves are
// plain values on the row, so this is the whole rule and there is nowhere else
// for a second copy of it to drift.
func (p Project) listed() bool {
	return p.Visibility == Public && !p.Hidden
}

// publish pushes a project's resolved visibility to the canonical git
// plane, which gives it a repo at git.hanzo.ai/<org>/<slug>, world-readable
// exactly when the project is.
//
// Fired on every create and every update rather than only on a transition: the
// subscriber is idempotent, and a transition this side failed to notice would
// leave a private project's source world-readable. It is also BEST-EFFORT —
// logged, never returned — because the project row is the source of truth and a
// git plane that is down (or simply not co-resident in this binary) must not
// fail a publish. The next update reconciles it.
func share(s *cloud.Service[state], ctx context.Context, p Project) {
	ev := plane.Visibility{
		Slug: p.Slug, Name: p.Name, Description: p.Description,
		Listed: p.listed(),
	}
	if _, err := cloud.Ask[plane.Visibility, struct{}](cloud.For(ctx, p.Org), "git",
		plane.GitPublish, &ev); err != nil {
		s.Log.Warn("publish visibility", "org", p.Org, "slug", p.Slug,
			"listed", ev.Listed, "err", err)
	}
}
