package principal

// The MCP server carries a caller and runs no middleware, so the two typed-op
// readers have to resolve identity from the caller as well as from the slot the
// route middleware fills. These pin that they do — and, more importantly, that
// the fallback did not widen the trust rule while it widened the surface.
//
// WHAT IT CAUGHT: every op gating on ValidatedFrom was unreachable as an MCP
// tool. websearch refused `sign in to search the web` for a request that reached
// the plugin WITH org=hanzo and user=hanzo/z@hanzo.ai on it, because tools/call
// invokes an op directly (zip here.go) — no route, so no middleware, so nothing
// parked the slot. The gate had been moved into the handler so that "every door
// reaches it", and it was unsatisfiable on the one endpoint that motivated the move.

import (
	"context"
	"testing"

	"github.com/zap-proto/zip"
)

// TestTypedReadersResolveTheMCPCaller is the fix: a caller and no middleware
// still resolves, which is what a tools/call looks like from inside an op.
func TestTypedReadersResolveTheMCPCaller(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-1"})

	if !ValidatedFrom(ctx) {
		t.Error("ValidatedFrom = false for a caller with a validated user; " +
			"an op gating on it is unreachable as an MCP tool")
	}
	org, ok := OrgFrom(ctx)
	if !ok || org != "acme" {
		t.Errorf("OrgFrom = (%q, %v), want (\"acme\", true)", org, ok)
	}
}

// TestTypedReadersRefuseAnUnvalidatedOrg is the half that must NOT move. An org
// riding along without a user is the exact shape OrgOf exists to refuse — a
// caller naming its own tenant — and reading the caller instead of the slot must
// not turn that refusal into a grant.
func TestTypedReadersRefuseAnUnvalidatedOrg(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "victim-corp"})

	if ValidatedFrom(ctx) {
		t.Error("ValidatedFrom = true with an empty user — an unauthenticated " +
			"caller would pass every gate that reads it")
	}
	if org, ok := OrgFrom(ctx); ok {
		t.Errorf("OrgFrom = (%q, true) with no validated user; the org that rode "+
			"along is untrusted and must not become a tenant key", org)
	}
}

// TestTypedReadersRefuseNothing keeps the empty context answering honestly: a
// context with no caller at all is not a caller with no name.
func TestTypedReadersRefuseNothing(t *testing.T) {
	ctx := context.Background()

	if ValidatedFrom(ctx) {
		t.Error("ValidatedFrom = true on a bare context")
	}
	if org, ok := OrgFrom(ctx); ok {
		t.Errorf("OrgFrom = (%q, true) on a bare context", org)
	}
}

// TestSlotStillWins pins that the route path is unchanged: what the middleware
// parked is answered first, so this is a fallback and not a replacement.
func TestSlotStillWins(t *testing.T) {
	ctx := context.WithValue(context.Background(), orgKey{}, "parked")
	ctx = zip.WithCaller(ctx, zip.Caller{Org: "from-caller", User: "u-1"})

	if org, _ := OrgFrom(ctx); org != "parked" {
		t.Errorf("OrgFrom = %q, want the parked org to win", org)
	}
}
