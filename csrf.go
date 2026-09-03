// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"

	"github.com/zap-proto/zip"
)

// The anti-forgery control, where the thing it reads lives.
//
// These four were exported from apps/account, and fifteen files across nine apps
// imported that app to reach them — the largest single app-to-app import edge in
// the estate. None of them touches account: each is a couple of lines over
// [Intended], [Consumes] and [Request], all of which are declared in this package.
//
// It is NOT a call to another subsystem and must not become one. What the control
// reads is the CURRENT REQUEST — a token the caller already handed us — so asking
// another process about it would put a network round trip in front of every
// mutating request in the fleet to re-derive something held in hand. The import
// was the defect; the hop would be a worse one.

// Unattested is what the off-the-HTTP-path refusal SAYS.
const Unattested = "a change is made by an attested caller"

// CSRF refuses a request that is not attested, from a context.
//
// A typed op holds a context rather than a *zip.Ctx, and a context with no request
// behind it is a call that arrived by some other transport — the op plane, an MCP
// tool — where an ambient browser cookie cannot exist and this control has nothing
// to check. That is a refusal rather than a pass: the surfaces this guards move
// money, and admitting an unattested caller because it came in a door with no
// cookies is the failure the control exists to prevent.
func CSRF(ctx context.Context) error {
	c, ok := Request(ctx)
	if !ok {
		return zip.ErrForbidden(Unattested)
	}
	return Intended(c)
}

// RequireCSRF is the same refusal as route middleware.
func RequireCSRF() zip.Handler {
	return func(c *zip.Ctx) error {
		if err := Intended(c); err != nil {
			return err
		}
		return c.Next()
	}
}

// RequireCSRFOnSpend asks it only where the request SPENDS.
//
// A read costs nothing and a page the caller never visited cannot make one cost
// something, so gating reads would buy nothing and break every ordinary GET a
// browser makes. [Consumes] is the one place that decides what spends.
func RequireCSRFOnSpend() zip.Handler {
	gate := RequireCSRF()
	return func(c *zip.Ctx) error {
		if !Consumes(c.Method(), c.Path()) {
			return c.Next()
		}
		return gate(c)
	}
}
