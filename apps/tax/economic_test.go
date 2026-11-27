package tax

// economic_test.go drives a tax year read off the event plane across every kind
// of rail: a real x402 settlement the rail itself stated, and a wire, a card
// charge and a stablecoin transfer stated as their rails would state them — one
// of them corrected, one stated twice.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	eventpeer "github.com/hanzoai/cloud/client/event"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

func TestPreparedFromEventsAcrossRails(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		e.profile("acme", CCorp, EIN, acmeEIN, true)
		e.profile("globex", Individual, SSN, globexSSN, true)
		e.certify("globex")
		e.payTo("acme", "globex", 150000) // x402: $1,500, settled and stated by the rail

		at := time.Now().Add(-time.Hour).Unix()
		usd := func(rail, proof, amount string) client.EconomicEvent {
			return client.EconomicEvent{Buyer: "acme", Seller: "globex", Currency: "USD", Gross: amount,
				FMV: amount, Rail: rail, Proof: proof, At: at}
		}
		wire := e.state(usd(client.RailWire, "FED-0001", "400"))
		e.state(usd(client.RailCard, "sq_pay_1", "5000")) // 1099-K: the card network's to report
		tx := "0x" + strings.Repeat("c", 64)
		chain := client.EconomicEvent{Buyer: "acme", Seller: "globex", Currency: "USDC", Gross: "1000",
			FMV: "999.50", Rail: client.RailChain, Chain: "eip155:8453", Tx: tx, Proof: tx, At: at}
		e.state(chain)
		e.state(chain) // stated twice: one event
		fix := usd(client.RailWire, "FED-0001/returned-100", "300")
		fix.Corrects = wire
		e.state(fix) // $100 of the wire came back: the wire is read as $300

		e.share("acme", "globex", Services)
		var l Ledger
		e.must(200, http.MethodGet, "/v1/tax/payments?year=2026", admin("acme"), nil, &l)
		if len(l.Payees) != 1 || l.Payees[0].Payee != "globex" {
			t.Fatalf("payees = %+v", l.Payees)
		}
		g := l.Payees[0]
		byRail := map[string][]Line{}
		var reportable int64
		for _, ln := range g.Lines {
			byRail[ln.Rail] = append(byRail[ln.Rail], ln)
			if ln.Verdict.Reportable {
				reportable += ln.AmountCents
			}
			if ln.ID == wire {
				t.Fatalf("the corrected wire was read beside its correction: %+v", ln)
			}
		}
		if len(g.Lines) != 4 {
			t.Fatalf("%d lines, want x402, card, chain and the corrected wire: %+v", len(g.Lines), g.Lines)
		}
		if c := byRail[client.RailCard]; len(c) != 1 || c[0].Verdict.Reportable || codes(c[0].Verdict) != "card" {
			t.Fatalf("card line = %+v, want excluded as 1099-K", c)
		}
		if c := byRail[client.RailChain]; len(c) != 1 || c[0].AmountCents != 99950 || codes(c[0].Verdict) != "digital_asset,reportable" {
			t.Fatalf("chain line = %+v, want its fair market value, $999.50", c)
		}
		if w := byRail[client.RailWire]; len(w) != 1 || w[0].AmountCents != 30000 || w[0].Proof != "FED-0001/returned-100" {
			t.Fatalf("wire line = %+v, want the correction's $300", w)
		}
		if x := byRail[client.RailX402]; len(x) != 1 || x[0].AmountCents != 150000 {
			t.Fatalf("x402 line = %+v", x)
		}
		if want := int64(150000 + 99950 + 30000); reportable != want || len(g.Totals) != 1 || g.Totals[0].AmountCents != want {
			t.Fatalf("reportable %d¢, totals %+v, want %d¢", reportable, g.Totals, want)
		}

		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		if len(p.Forms) != 1 || box(p.Forms[0].Boxes, "1a") != reportable {
			t.Fatalf("prepared %+v, want one 1099-NEC with box 1a = the reportable events, %d¢", p.Forms, reportable)
		}
		if len(p.Forms[0].Payments) != 3 {
			t.Fatalf("form sums %v, want the three reportable events", p.Forms[0].Payments)
		}
	})
}

// A third org is party to none of it: the plane answers it nothing, and its year
// has no payees.
func TestAThirdOrgSeesNoPayment(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		e.payTo("acme", "globex", 150000)
		out, err := eventpeer.EventEconomics(cloud.For(context.Background(), "initech"),
			&client.EconomicsIn{From: 0, To: time.Now().Add(time.Hour).Unix()})
		if err != nil || len(out.Rows) != 0 {
			t.Fatalf("a third org read %+v (%v)", out, err)
		}
		var l Ledger
		e.must(200, http.MethodGet, "/v1/tax/payments?year=2026", admin("initech"), nil, &l)
		if len(l.Payees) != 0 {
			t.Fatalf("a third org's year = %+v", l.Payees)
		}
		for _, org := range []string{"acme", "globex"} {
			out, err := eventpeer.EventEconomics(cloud.For(context.Background(), org),
				&client.EconomicsIn{From: 0, To: time.Now().Add(time.Hour).Unix()})
			if err != nil || len(out.Rows) != 1 || out.Rows[0].Buyer != "acme" || out.Rows[0].Seller != "globex" {
				t.Fatalf("%s reads %+v (%v), want the one payment", org, out, err)
			}
		}
	})
}
