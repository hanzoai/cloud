package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The two guards on the manual admin grant that must hold BEFORE anything is
// reachable: a grant is POSITIVE-ONLY, and it is CAPPED.
//
// They are driven through a real request, and the Service is deliberately NIL.
// That is the assertion, not a shortcut: ApplyGrant dereferences the service to
// resolve the org, the ledger and the audit store, so a nil service that does
// not panic proves both refusals are decided from the REQUEST alone, ahead of
// every dependency — which is what "before any money moves" has to mean. Move
// either check below FindOrg and this test panics instead of failing politely.
//
// The cap is a fat-finger guard, not a policy limit: $100,000 per grant. A
// funding decision larger than that is deliberately more than one action.
func TestGrantRefusalsPrecedeEveryDependency(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cents  int64
		reason string
	}{
		{"zero is not a grant", 0, "must be positive"},
		{"a negative grant is a debit in disguise", -50_000, "must be positive"},
		{"one cent over the cap is refused", maxGrantCents + 1, "cap"},
		{"an absurd amount is refused", 1 << 62, "cap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := driveGrant(t, tc.cents)
			if out == nil {
				t.Fatal("ApplyGrant returned no envelope")
			}
			if out.Status != Err {
				t.Fatalf("status = %q, want %q — the grant was NOT refused", out.Status, Err)
			}
			if !strings.Contains(out.Msg, tc.reason) {
				t.Fatalf("msg = %q, want it to mention %q", out.Msg, tc.reason)
			}
			if out.Data != nil {
				t.Fatalf("a refused grant returned a receipt: %+v", out.Data)
			}
		})
	}

}

// driveGrant calls ApplyGrant with a NIL service through a real zip request.
func driveGrant(t *testing.T, cents int64) *GrantOut {
	t.Helper()
	var out *GrantOut
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Post("/grant", func(c *zip.Ctx) error {
		o, err := ApplyGrant(nil, c, "maxpower", CreditRequest{
			AmountCents: cents, Currency: "usd", Reason: "guard test",
		})
		if err != nil {
			return err
		}
		out = o
		return c.JSON(200, o)
	})
	if _, err := app.Test(httptest.NewRequest(http.MethodPost, "/grant", nil)); err != nil {
		t.Fatalf("drive: %v", err)
	}
	return out
}

// The cap is a MONEY constant. Restating its value here means a change to it is a
// deliberate two-line edit that a reviewer sees as a money decision, rather than
// a one-character edit inside an expression.
func TestGrantCapIsOneHundredThousandDollars(t *testing.T) {
	const wantCents int64 = 100_000 * 100
	if maxGrantCents != wantCents {
		t.Fatalf("maxGrantCents = %d cents, want %d ($100,000) — changing the per-grant cap is a money decision",
			maxGrantCents, wantCents)
	}
}
