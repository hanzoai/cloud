package marketplace

import (
	"context"
	"encoding/hex"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/x402"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/luxfi/crypto"
)

// Where a seller stands, in one read, from the owners of each fact — and the one
// fact it proves here, its payout wallet, bound only by that wallet's signature
// over a challenge that binds once.
func TestSellerOnboarding(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		year := time.Now().UTC().Year()
		e.peers.set(func(s *stand) {
			s.standing[seller] = client.Standing{Identity: "verified", Entity: "company", Sanctions: "clear", Reason: "clear of every list",
				Statements: []client.Statement{{ID: "st_1", Agent: "agent_1", Org: seller, Kind: client.StatementTax, Form: "w9", Status: "live"}}}
			s.forms[seller] = client.TaxForm{Form: "w9", Certified: true, Valid: true}
			s.inbox[seller] = []client.Received{{ID: "f1099_1", Payer: buyer, Kind: "1099-NEC", Year: year, Furnished: time.Now().Unix()},
				{ID: "f1099_0", Payer: buyer, Kind: "1099-NEC", Year: year - 1}}
		})
		read := func() Onboarding {
			t.Helper()
			var o Onboarding
			e.must(200, http.MethodGet, "/v1/marketplace/seller", admin(seller), nil, &o)
			return o
		}
		o := read()
		if o.Ready || !slices.Equal(o.Missing, []string{"payout"}) || o.Identity != "verified" || o.Entity != "company" ||
			o.Sanctions != "clear" || o.Tax == nil || o.Tax.Form != "w9" || len(o.Credentials) != 1 || len(o.Received) != 1 ||
			o.Received[0].ID != "f1099_1" || o.Earnings.Payments != 0 || o.Earnings.Year != year {
			t.Fatalf("onboarding before a payout wallet = %+v", o)
		}
		for _, s := range o.Sources {
			if s.Status != "ok" {
				t.Fatalf("source %s answered %s", s.App, s.Status)
			}
		}
		if code, _, _ := e.req(http.MethodGet, "/v1/marketplace/seller", member(seller), "", nil); code != http.StatusForbidden {
			t.Fatalf("a member who is not an admin read the org's standing: %d", code)
		}
		// Another org's standing is its own: an org with nothing on file owes all of it.
		var other Onboarding
		e.must(200, http.MethodGet, "/v1/marketplace/seller", admin(buyer), nil, &other)
		if other.Ready || !slices.Equal(other.Missing, []string{"identity", "sanctions", "tax_form", "payout"}) || len(other.Received) != 0 {
			t.Fatalf("an org with nothing on file = %+v", other)
		}

		// A direct offer needs a wallet to be paid into.
		offer := map[string]any{"seller": seller, "title": "Custom model", "brief": "Fine-tune on our data", "amount": "75.00"}
		if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", offer); code != http.StatusConflict {
			t.Fatalf("a direct offer to a seller with no payout wallet = %d (%s), want 409", code, b)
		}

		wallet := e.createWallet(seller)
		// A wallet that is not the seller's is not offered a challenge.
		if code, _, _ := e.req(http.MethodPost, "/v1/marketplace/seller/payout", admin(seller), "",
			map[string]any{"wallet": e.createWallet(buyer)}); code != http.StatusNotFound {
			t.Fatalf("a challenge for another org's wallet = %d, want 404", code)
		}
		var ch PayoutChallenge
		e.must(200, http.MethodPost, "/v1/marketplace/seller/payout", admin(seller), map[string]any{"wallet": wallet}, &ch)
		// Signed by anyone else, it binds nothing.
		stranger, _ := crypto.GenerateKey()
		digest := personal(ch.Message)
		forged, _ := crypto.Sign(digest[:], stranger)
		if code, _, _ := e.req(http.MethodPost, "/v1/marketplace/seller/payout/verify", admin(seller), "",
			map[string]any{"signature": "0x" + hex.EncodeToString(forged)}); code != http.StatusForbidden {
			t.Fatalf("a stranger's signature = %d, want 403", code)
		}
		var sig struct {
			Signature string `json:"signature"`
		}
		e.must(200, http.MethodPost, "/v1/wallet/"+wallet+"/sign", admin(seller), map[string]any{"digest": ch.Digest}, &sig)
		var p Payout
		e.must(200, http.MethodPost, "/v1/marketplace/seller/payout/verify", admin(seller), map[string]any{"signature": sig.Signature}, &p)
		sealedNotPlain(t, "payouts.address", raw(t, mounted.State.store, `SELECT address FROM payouts WHERE org = $1`, seller), p.Address)
		if !p.Bound || p.Wallet != wallet || p.Address != ch.Address {
			t.Fatalf("bound %+v", p)
		}
		// A challenge binds once.
		if code, _, _ := e.req(http.MethodPost, "/v1/marketplace/seller/payout/verify", admin(seller), "",
			map[string]any{"signature": sig.Signature}); code != http.StatusConflict {
			t.Fatalf("a spent challenge = %d, want 409", code)
		}
		// One that expired binds nothing.
		e.must(200, http.MethodPost, "/v1/marketplace/seller/payout", admin(seller), map[string]any{"wallet": wallet}, &ch)
		if _, err := mounted.State.store.db.Exec(`UPDATE challenges SET expires = $1 WHERE org = $2`, time.Now().Add(-time.Minute).Unix(), seller); err != nil {
			t.Fatal(err)
		}
		e.must(200, http.MethodPost, "/v1/wallet/"+wallet+"/sign", admin(seller), map[string]any{"digest": ch.Digest}, &sig)
		if code, _, _ := e.req(http.MethodPost, "/v1/marketplace/seller/payout/verify", admin(seller), "",
			map[string]any{"signature": sig.Signature}); code != http.StatusConflict {
			t.Fatalf("an expired challenge = %d, want 409", code)
		}
		if o = read(); !o.Ready || len(o.Missing) != 0 || o.Payout.Wallet != wallet {
			t.Fatalf("onboarding with a bound wallet = %+v", o)
		}
		if acts := e.actions(seller, "marketplace.payout."); !slices.Equal(acts, []string{"marketplace.payout.bound"}) {
			t.Fatalf("payout trail = %v", acts)
		}

		// The direct offer now pays into the bound wallet, and the earnings say so.
		e.fund(buyer, money.FromCents(10_000))
		j := e.hire(buyer, offer, 30*24*time.Hour)
		if j.Listing != "" || j.Title != "Custom model" || j.SellerOrg != seller {
			t.Fatalf("direct offer = %+v", j)
		}
		e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
		e.act(j.ID, "release", member(buyer), nil, http.StatusOK)
		if got := e.balance(seller, wallet); got.Cmp(money.FromCents(7500)) != 0 {
			t.Fatalf("the bound wallet holds %s, want 75", got)
		}
		if _, err := x402.Announce(context.Background()); err != nil {
			t.Fatal(err)
		}
		o = read()
		if o.Earnings.Payments != 1 || o.Earnings.Gross.String() != "75" || o.Earnings.ByRail[client.RailX402].String() != "75" {
			t.Fatalf("earnings = %+v, want the one 75 payment over x402", o.Earnings)
		}
	})
}
