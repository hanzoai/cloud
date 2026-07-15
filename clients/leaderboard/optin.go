// The opt-in surface — PUBLIC-LISTING IS OPT-IN, PRIVATE BY DEFAULT.
//
//	GET /v1/usage/leaderboard/optin        the caller's own opt-in + their org's opt-in
//	PUT /v1/usage/leaderboard/optin         set the caller's OWN listing (self only)
//	PUT /v1/usage/leaderboard/optin/org     set the ORG's public-board listing (org admin)
//
// A user writes ONLY their own preference (keyed by their validated ledger id); an
// org preference is writable only by an admin OF that org. Nothing here is secret.
package leaderboard

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// handleRE bounds a public display handle / org display name: a printable label,
// 1..40 chars, starting alphanumeric. It becomes a value shown to other users, so it
// is shape-guarded (no control chars, no injection surface — it is never a SQL/label
// key, only display text, but bounding it keeps the board tidy and abuse-resistant).
var handleRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._'-]{0,39}$`)

type userOptinView struct {
	Listed bool   `json:"listed"`
	Handle string `json:"handle"`
	CanSet bool   `json:"canSet"` // false when the caller's identity can't be resolved
}

type orgOptinView struct {
	Listed    bool   `json:"listed"`
	Display   string `json:"display"`
	CanManage bool   `json:"canManage"` // may the caller edit the org opt-in
}

type optinView struct {
	User userOptinView `json:"user"`
	Org  orgOptinView  `json:"org"`
}

// getOptin returns the caller's own listing preference + their org's.
func getOptin(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to manage leaderboard visibility")
	}
	ctx := c.Context()
	selfID := selfLedgerID(c, org)

	uv := userOptinView{CanSet: selfID != ""}
	if selfID != "" {
		if u, err := s.State.store.GetUser(ctx, selfID); err == nil {
			uv.Listed, uv.Handle = u.Listed, u.Handle
		} else if err != errNotFound {
			s.Log.Debug("get user optin failed", "err", err)
		}
	}

	canManage := principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c)
	ov := orgOptinView{CanManage: canManage}
	if o, err := s.State.store.GetOrg(ctx, org); err == nil {
		ov.Listed, ov.Display = o.Listed, o.Display
	} else if err != errNotFound {
		s.Log.Debug("get org optin failed", "err", err)
	}

	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, optinView{User: uv, Org: ov})
}

type userOptinReq struct {
	Listed bool   `json:"listed"`
	Handle string `json:"handle"`
}

// putUserOptin sets the CALLER's own public-listing preference. Self only — the key
// is the caller's validated ledger id, never a body/query value.
func putUserOptin(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to manage leaderboard visibility")
	}
	selfID := selfLedgerID(c, org)
	if selfID == "" {
		return zip.ErrBadRequest("cannot resolve your identity (missing user name)")
	}
	var body userOptinReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	handle := strings.TrimSpace(body.Handle)
	if handle != "" && !handleRE.MatchString(handle) {
		return zip.ErrBadRequest("handle must be 1-40 chars of letters, digits, space, . _ ' -")
	}
	// A listed user always has a non-empty handle so they never render as "Anonymous"
	// on their own opted-in row — default to their username.
	if body.Listed && handle == "" {
		handle = nameOf(selfID)
	}
	now := time.Now().Unix()
	if err := s.State.store.PutUser(c.Context(), userOptin{
		UserID: selfID, Org: org, Handle: handle, Listed: body.Listed,
	}, now); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "save opt-in: %v", err)
	}
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, userOptinView{Listed: body.Listed, Handle: handle, CanSet: true})
}

type orgOptinReq struct {
	Listed  bool   `json:"listed"`
	Display string `json:"display"`
}

// putOrgOptin sets the ORG's public-board listing. Only an admin OF the caller's org
// (org admin or SuperAdmin) may set it; the org key is the caller's validated org.
func putOrgOptin(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to manage leaderboard visibility")
	}
	if !(principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c)) {
		return zip.ErrForbidden("only an org admin can change the org's leaderboard visibility")
	}
	var body orgOptinReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	display := strings.TrimSpace(body.Display)
	if display != "" && !handleRE.MatchString(display) {
		return zip.ErrBadRequest("display must be 1-40 chars of letters, digits, space, . _ ' -")
	}
	if body.Listed && display == "" {
		display = org
	}
	now := time.Now().Unix()
	if err := s.State.store.PutOrg(c.Context(), orgOptin{
		Org: org, Display: display, Listed: body.Listed,
	}, now); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "save org opt-in: %v", err)
	}
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, orgOptinView{Listed: body.Listed, Display: display, CanManage: true})
}
