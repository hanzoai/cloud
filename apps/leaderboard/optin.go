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
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// handleRE bounds a public display handle / org display name: a printable label,
// 1..40 chars, starting alphanumeric. It becomes a value shown to other users, so it
// is shape-guarded (no control chars, no injection surface — it is never a SQL/label
// key, only display text, but bounding it keeps the board tidy and abuse-resistant).
var handleRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._'-]{0,39}$`)

// userOptinView is one user's public-listing state.
type userOptinView struct {
	// Listed is whether this user is shown by name to other people on the board.
	Listed bool `json:"listed"`
	// Handle is the display name shown when listed.
	Handle string `json:"handle"`
	// CanSet is false when the caller's identity cannot be resolved, so the preference is unwritable.
	CanSet bool `json:"canSet"`
}

// orgOptinView is one org's public-board listing state.
type orgOptinView struct {
	// Listed is whether this org appears on the cross-org public board.
	Listed bool `json:"listed"`
	// Display is the org name shown when listed.
	Display string `json:"display"`
	// CanManage is whether this caller may change the org's listing.
	CanManage bool `json:"canManage"`
}

// optinView is the caller's own listing state beside their org's.
type optinView struct {
	// User is the caller's own public-listing state.
	User userOptinView `json:"user"`
	// Org is the caller's org's public-board listing state.
	Org orgOptinView `json:"org"`
}

// getOptin returns the caller's own public-listing preference and their org's. canSet
// and canManage say whether this caller may change each one, so a console renders the
// controls it is actually allowed to use.
//
// Response: {"user": {"listed": true, "handle": "sam", "canSet": true}, "org": {"listed": false, "display": "", "canManage": false}}
func (o ops) getOptin(ctx context.Context, _ *struct{}) (*optinView, error) {
	s := o.s
	c, hasReq := o.request(ctx)
	if !hasReq {
		return nil, zip.ErrUnauthorized("sign in to manage leaderboard visibility")
	}
	org, ok := tenant(c)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to manage leaderboard visibility")
	}
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
	return &optinView{User: uv, Org: ov}, nil
}

// userOptinReq sets the caller's own listing.
type userOptinReq struct {
	// Listed is whether to show this user by name to other people on the board.
	Listed bool `json:"listed"`
	// Handle is the display name to show; empty while listing defaults to your username.
	Handle string `json:"handle"`
}

// putUserOptin sets the caller's own public-listing preference. Self only — the key is
// the caller's validated ledger id, never a body or query value — and opting in with
// no handle defaults to the caller's username so an opted-in row never reads
// "Anonymous".
//
// Example: {"listed": true, "handle": "sam"}
func (o ops) putUserOptin(ctx context.Context, body *userOptinReq) (*userOptinView, error) {
	s := o.s
	c, hasReq := o.request(ctx)
	if !hasReq {
		return nil, zip.ErrUnauthorized("sign in to manage leaderboard visibility")
	}
	org, ok := tenant(c)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to manage leaderboard visibility")
	}
	selfID := selfLedgerID(c, org)
	if selfID == "" {
		return nil, zip.ErrBadRequest("cannot resolve your identity (missing user name)")
	}
	handle := strings.TrimSpace(body.Handle)
	if handle != "" && !handleRE.MatchString(handle) {
		return nil, zip.ErrBadRequest("handle must be 1-40 chars of letters, digits, space, . _ ' -")
	}
	// A listed user always has a non-empty handle so they never render as "Anonymous"
	// on their own opted-in row — default to their username.
	if body.Listed && handle == "" {
		handle = nameOf(selfID)
	}
	now := time.Now().Unix()
	if err := s.State.store.PutUser(ctx, userOptin{
		UserID: selfID, Org: org, Handle: handle, Listed: body.Listed,
	}, now); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "save opt-in: %v", err)
	}
	c.SetHeader("Cache-Control", "no-store")
	return &userOptinView{Listed: body.Listed, Handle: handle, CanSet: true}, nil
}

// orgOptinReq sets the org's listing on the public board.
type orgOptinReq struct {
	// Listed is whether the org appears on the cross-org public board.
	Listed bool `json:"listed"`
	// Display is the org name to show; empty while listing defaults to the org id.
	Display string `json:"display"`
}

// putOrgOptin sets the org's listing on the cross-org public board. Only an admin OF
// the caller's org (org admin or SuperAdmin) may set it, and the org key is the
// caller's validated org, never a body value.
//
// Example: {"listed": true, "display": "Acme"}
func (o ops) putOrgOptin(ctx context.Context, body *orgOptinReq) (*orgOptinView, error) {
	s := o.s
	c, hasReq := o.request(ctx)
	if !hasReq {
		return nil, zip.ErrUnauthorized("sign in to manage leaderboard visibility")
	}
	org, ok := tenant(c)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to manage leaderboard visibility")
	}
	if !(principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c)) {
		return nil, zip.ErrForbidden("only an org admin can change the org's leaderboard visibility")
	}
	display := strings.TrimSpace(body.Display)
	if display != "" && !handleRE.MatchString(display) {
		return nil, zip.ErrBadRequest("display must be 1-40 chars of letters, digits, space, . _ ' -")
	}
	if body.Listed && display == "" {
		display = org
	}
	now := time.Now().Unix()
	if err := s.State.store.PutOrg(ctx, orgOptin{
		Org: org, Display: display, Listed: body.Listed,
	}, now); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "save org opt-in: %v", err)
	}
	c.SetHeader("Cache-Control", "no-store")
	return &orgOptinView{Listed: body.Listed, Display: display, CanManage: true}, nil
}
