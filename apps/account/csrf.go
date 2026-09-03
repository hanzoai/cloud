package account

// The console's half of the anti-forgery control: the address that HANDS a browser
// its token, and the three shapes in which this app's own surfaces ask for one.
//
// THE CONTROL ITSELF IS NOT HERE. It is cloud.Intended (intent.go) — one decision,
// asked at op-invoke for every operation the program serves, which is the only seam
// MCP, the call plane, the graph and the CLI all pass through. The token is
// internal/attest. This file holds what belongs to the ACCOUNT surface: a caller
// obtains their first token from their own account, so the issuing route is the
// account's, and the boot verdicts below are this app's because this app is the
// minter.
//
// WHY THE TOKEN WORKS. A browser write is authenticated from its httpOnly session
// COOKIE, which is AMBIENT — a page the caller never visited sends it too. So a
// change gets a POSITIVE control: a value the caller can obtain ONLY by reading a
// same-origin response (the Same-Origin Policy stops a cross-site page from reading
// GET /v1/account/csrf) and MUST echo in a CUSTOM header (a simple cross-site
// request cannot set X-CSRF-Token without a preflight the server never grants).
//
// THE KEY IS NO LONGER SHARED, AND NOTHING VERIFIES ACROSS PROCESSES.
//
// This route still mints a token, and a client that echoes one is not broken. But
// cloud.Intended does not read it: it reads Sec-Fetch-Site, a fact the browser
// states on the request and script cannot write. So a process that mints a key of
// its own checks only tokens it wrote itself, which is fine, because nothing else
// checks them at all.
//
// What that removed was an agreement problem. One value had to reach every child of
// the fleet, and when it did not — blank, unset, or wrong — the surfaces that asked
// for it refused every caller with a 503 that no probe could explain.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/attest"
	"github.com/zap-proto/zip"
)

// KeyEnv names the shared anti-forgery MAC key. It is attest's name, said here so
// an operator provisioning it and an error telling them say the same word.
const KeyEnv = attest.KeyEnv

// NO MINTER KEY CHECK EITHER. own() refused to mount a DEPLOYED account process
// holding a key of its own, because a per-replica key meant a token minted by one
// replica failed on the next.
//
// Nothing mints a token that another process verifies any more, so there is no key
// to disagree about. This check was also the last thing keeping /v1/account/* at
// 503 for every caller after the fleet-wide ones came out: account is the surface
// that serves the avatar, the appearance and the API keys, and it was refusing all
// of them over a value none of them read.

// requireCSRF is the same control as MIDDLEWARE, which is the shape a route chain
// takes: it wraps the leaf handler at registration rather than standing in front of
// it. Both are one line over [cloud.Intended] — one decision, three shapes, never
// three decisions.
func requireCSRF() zip.Middleware {
	return func(next zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			if err := cloud.Intended(c); err != nil {
				return err
			}
			return next(c)
		}
	}
}

// csrfResp is the anti-forgery token a browser echoes on every change.
type csrfResp struct {
	// Token is the value to send back in the X-CSRF-Token header. It is bound to the
	// caller's identity, so it authorizes changes as them and as nobody else.
	Token string `json:"csrfToken"`
	// ExpiresIn is the token's lifetime in seconds. Fetch a new one when it lapses;
	// a change with an expired token is refused.
	ExpiresIn int64 `json:"expiresIn"`
}

// IssueCSRFToken mints the anti-forgery token a browser echoes as X-CSRF-Token on
// every change it asks for. The token is bound to the caller's validated identity
// and expires, so one minted for one identity cannot authorize a change as another.
//
// It is answered no-store, so it is never cached by a shared proxy. This is the
// same-origin endpoint the embedded console reads — the Same-Origin Policy is what
// stops a cross-site page from reading the response and forging a change.
func (o ops) issueCSRFToken(ctx context.Context, _ *cloud.Unit) (*csrfResp, error) {
	// reads: this is the endpoint that ISSUES the token, so requiring one here
	// would leave a browser no way to obtain its first. unscoped: a zero-org
	// (first-run) user still needs one to onboard.
	cr, c, err := o.requestCaller(ctx, reads, unscoped, "obtain a CSRF token")
	if err != nil {
		return nil, err
	}
	token, ttl := attest.Process().Mint(cr.name, cr.owner)
	c.Fiber().Set("Cache-Control", "no-store")
	return &csrfResp{Token: token, ExpiresIn: ttl}, nil
}
