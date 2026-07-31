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

// GetOptin returns the caller's own public-listing preference and their org's,
// each with whether the caller may change it. Public listing is opt-in and private
// by default, so a fresh caller reads listed=false for both.
func (o boardOps) getOptin(ctx context.Context, _ *noInput) (*optinView, error) {
	org, err := tenantOf(ctx, "sign in to manage leaderboard visibility")
	if err != nil {
		return nil, err
	}
	s := o.s
	selfID := selfIDOf(ctx, org)

	uv := userOptinView{CanSet: selfID != ""}
	if selfID != "" {
		if u, err := s.State.store.GetUser(ctx, selfID); err == nil {
			uv.Listed, uv.Handle = u.Listed, u.Handle
		} else if err != errNotFound {
			s.Log.Debug("get user optin failed", "err", err)
		}
	}

	ov := orgOptinView{CanManage: adminOf(ctx)}
	if row, err := s.State.store.GetOrg(ctx, org); err == nil {
		ov.Listed, ov.Display = row.Listed, row.Display
	} else if err != errNotFound {
		s.Log.Debug("get org optin failed", "err", err)
	}

	noStore(ctx)
	return &optinView{User: uv, Org: ov}, nil
}

// userOptinReq sets the caller's own public-listing preference.
type userOptinReq struct {
	// Listed publishes the caller's row to other viewers of the board when true, and
	// anonymizes it when false.
	Listed bool `json:"listed"`
	// Handle is the display name shown on a listed row: 1-40 characters of letters,
	// digits, space, dot, underscore, apostrophe or hyphen. Left empty on a listing
	// opt-in it defaults to the caller's username.
	Handle string `json:"handle"`
}

// PutUserOptin sets the CALLER's own public-listing preference on the leaderboard.
// Self only: the row written is keyed by the caller's validated ledger identity, so
// this can never edit another member's visibility whatever the request says. A
// caller opting in with no handle is given their username, so a listed row never
// renders as "Anonymous" to its own owner.
//
// Example: {"listed": true, "handle": "ada"}
func (o boardOps) putUserOptin(ctx context.Context, in *userOptinReq) (*userOptinView, error) {
	s := o.s
	org, err := tenantOf(ctx, "sign in to manage leaderboard visibility")
	if err != nil {
		return nil, err
	}
	selfID := selfIDOf(ctx, org)
	if selfID == "" {
		return nil, zip.ErrBadRequest("cannot resolve your identity (missing user name)")
	}
	body := *in
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
	noStore(ctx)
	return &userOptinView{Listed: body.Listed, Handle: handle, CanSet: true}, nil
}

// orgOptinReq sets the org's public-board listing.
type orgOptinReq struct {
	// Listed publishes the org on the cross-org global board when true, and withdraws
	// it when false.
	Listed bool `json:"listed"`
	// Display is the name shown for the org on that board: 1-40 characters of
	// letters, digits, space, dot, underscore, apostrophe or hyphen. Left empty on a
	// listing opt-in it defaults to the org id.
	Display string `json:"display"`
}

// PutOrgOptin sets the ORG's listing on the cross-org global board. Only an admin of
// the caller's own org — an org admin or a platform SuperAdmin — may change it, and
// the org written is the caller's validated tenant, never a value from the request.
// Listing consents to publishing the org's usage VOLUME; cross-org spend stays
// restricted to platform admins regardless.
//
// Example: {"listed": true, "display": "Acme"}
func (o boardOps) putOrgOptin(ctx context.Context, in *orgOptinReq) (*orgOptinView, error) {
	s := o.s
	org, err := tenantOf(ctx, "sign in to manage leaderboard visibility")
	if err != nil {
		return nil, err
	}
	if !adminOf(ctx) {
		return nil, zip.ErrForbidden("only an org admin can change the org's leaderboard visibility")
	}
	body := *in
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
	noStore(ctx)
	return &orgOptinView{Listed: body.Listed, Display: display, CanManage: true}, nil
}
