// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"net/http"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// AccountFromPrincipal decomplects the console's IDENTITY onto the ONE truth every
// /v1/admin/* call already authorizes on — the validated cloud principal — instead of
// the embedded casibase account model.
//
// THE COMPLECTION IT REMOVES. The operator SPA authenticates via /v1/signin (a cloud
// PKCE session → the X-User-* principal the middleware mints) but read its IDENTITY
// from the account read, which the embedded IAM (casibase) answers from ITS OWN session
// cookie. A PKCE session is not a casibase session, so that read returned
// owner:"hanzo" (anonymous) or "Unauthorized operation" — and the SPA's SuperAdmin
// gate (owner == "admin" && isAdmin), reading that, bounced the operator UI to login
// even though the SAME session got 200 from every /v1/admin/* route. Two session
// models for one surface; the identity source and the authorization source disagreed.
//
// THE DECOMPLECTION. Identity is now the principal: when a VALIDATED principal is
// present (X-User-Id is set ONLY by IdentityMiddleware from a real credential, never a
// raw client header — see middleware_identity.go), /v1/ai/account reflects it. owner
// is the HOME org (principal.Owner) so a SuperAdmin org-switched into a tenant stays a
// SuperAdmin; isAdmin is the validated bit. With NO principal it falls through
// (c.Next()) to the casibase account surface unchanged — the anonymous sign-in page
// and any legacy casibase-session caller are untouched. One truth, additive, fail-open
// to the old path. MUST be registered AFTER IdentityMiddleware (needs the minted
// headers) and BEFORE MountAll (so it precedes the casibase account handler).
// accountPath is the account read this middleware fronts. It is a named constant
// because the interception is a PATH MATCH: when the /v1 surface was namespaced
// (/v1/get-account → /v1/ai/account) a literal left un-updated here would not
// error — the middleware would simply stop firing, fall through to the casibase
// account surface, and hand the SPA the anonymous owner again. That is precisely
// the bug this file exists to fix, silently restored.
const accountPath = "/v1/ai/account"

func AccountFromPrincipal() zip.Handler {
	return func(c *zip.Ctx) error {
		if c.Method() != http.MethodGet || c.Path() != accountPath {
			return c.Next()
		}
		user := c.User() // X-User-Id — minted only from a validated credential
		if user == "" {
			return c.Next() // no principal → the casibase account surface (unchanged)
		}
		owner := principal.Owner(c) // HOME org (a SuperAdmin stays one when org-switched)
		if owner == "" {
			owner = c.Org()
		}
		name := c.Header("X-User-Name")
		if name == "" {
			name = user
		}
		return c.JSON(http.StatusOK, map[string]any{
			"status": "ok",
			"msg":    "",
			"data": map[string]any{
				"owner":       owner,
				"name":        name,
				"displayName": name,
				"email":       c.Header("X-User-Email"),
				"isAdmin":     c.IsAdmin(),
				"type":        "normal-user",
			},
		})
	}
}
