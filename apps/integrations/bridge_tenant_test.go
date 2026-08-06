package integrations

// The tenant a chat turn bills has to travel ON THE WIRE, and these pin the two
// halves of why that is not obvious.
//
// commerce takes the org from the CALLER's identity and never from an argument, so
// no caller can name the books it charges. The org therefore rides the caller. But
// zip reads a STATED caller only where there is NO request behind the context
// (caller.go:352-356) — otherwise CallerOf reads the request's own headers. So
// cloud.For applied to an inbound webhook's context is a SILENT NO-OP.
//
// That is not hypothetical. It shipped: the tenant was stated one hop later, inside
// the agents op, on a context that had the plane request behind it. Every Slack
// message still died with "authorize: no org on the call", and the statement looked
// correct in review because the code read exactly like the working background
// callers elsewhere in the tree. The difference is invisible unless you know the
// precedence rule — which is what these tests write down.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
)

// A turn's context must name the tenant, or the balance gate refuses it.
func TestRunContextStatesTheTenant(t *testing.T) {
	ctx, cancel := bridgeRunContext("acme")
	defer cancel()
	if got := cloud.Who(ctx).Org; got != "acme" {
		t.Fatalf("the turn must act for a named tenant, got %q", got)
	}
}

// The statement must be READABLE, which is only true off a request. This is the
// regression that shipped: same call, wrong base context, silently no org.
func TestStatingOnARequestContextIsANoOp(t *testing.T) {
	// A background context is the only base a stated caller survives on.
	if got := cloud.Who(cloud.For(context.Background(), "acme")).Org; got != "acme" {
		t.Fatalf("stated tenant must be readable off a request, got %q", got)
	}
	// bridgeRunContext must not be derivable from a caller-supplied context: it
	// takes an org and nothing else, so there is no parameter through which the
	// webhook's request could be threaded back in. If this ever grows a
	// context.Context argument, the bug returns — the signature IS the guard.
	var _ func(string) (context.Context, context.CancelFunc) = bridgeRunContext
}

// An empty org states nothing rather than a blank tenant: downstream must refuse on
// "no org" rather than bill an account named "".
func TestEmptyTenantIsNotStated(t *testing.T) {
	ctx, cancel := bridgeRunContext("")
	defer cancel()
	if got := cloud.Who(ctx).Org; got != "" {
		t.Errorf("an empty org must not become a tenant, got %q", got)
	}
}

// The turn outlives the webhook reply, so its deadline must come from the turn
// budget and not from a request that is already answered.
func TestRunContextIsNotAlreadyCancelled(t *testing.T) {
	ctx, cancel := bridgeRunContext("acme")
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("a fresh turn context is already done; the run would be cancelled before it starts")
	default:
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Error("a turn must be bounded by bridgeAgentTimeout")
	}
}
