package event

// economic_test.go holds the economic event to what economic.go promises: only the
// rail's owner says what a payment was; one row per party at the rail's time, the
// seller's naming no one inside the buyer; one event per (rail, proof), the first
// statement standing; a correction that restates only its own rail's payment; a
// dispute that amends nothing; a read that binds the caller's org first and costs
// a page; and no door from the public wire to the signal.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	planeops "github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// paid is one payment from acme to globex on the x402 rail, as the rail records it.
func paid() planeops.EconomicEvent {
	return planeops.EconomicEvent{
		Buyer: "acme", Seller: "globex", Agent: "u_acme", Parent: "agent_7", Service: "/v1/tool/search",
		Currency: "USD", Gross: "15.00", FMV: "15", Rail: planeops.RailX402,
		Proof: "x402_" + strings.Repeat("ab", 32), At: time.Now().Add(-time.Hour).Unix(),
	}
}

// book is the economic plane as a test drives it: the rails' records, what each
// owner was asked, and a warehouse that lands what is published (or, with slow
// set, lands nothing yet).
type book struct {
	w       *warehouse
	records map[string]planeops.EconomicEvent // rail+"\x00"+proof → what the rail answers
	asked   []string                          // the org each ask acted for
	slow    bool
}

// economicBook stands the plane up: every rail in rails gets an owner that answers
// from records, and the warehouse's reads are answered from the facts published.
func economicBook(t *testing.T, rails ...string) *book {
	t.Helper()
	b := &book{w: fakeWarehouse(t), records: map[string]planeops.EconomicEvent{}}
	prevOwners, prevQuery := owners, warehouseQuery
	owners = map[string]func(context.Context, *planeops.EconomicNotice) (*planeops.EconomicEvent, error){}
	for _, r := range append([]string{planeops.RailX402}, rails...) {
		owners[r] = b.answer
	}
	warehouseQuery = b.query(t)
	t.Cleanup(func() { owners, warehouseQuery = prevOwners, prevQuery })
	return b
}

// record makes a payment part of its rail's records, and answers its notice.
func (b *book) record(e planeops.EconomicEvent) *planeops.EconomicNotice {
	b.records[e.Rail+"\x00"+e.Proof] = e
	return &planeops.EconomicNotice{Rail: e.Rail, Proof: e.Proof}
}

func (b *book) answer(ctx context.Context, in *planeops.EconomicNotice) (*planeops.EconomicEvent, error) {
	org, _ := cloud.Tenant(ctx)
	b.asked = append(b.asked, org)
	e, ok := b.records[in.Rail+"\x00"+in.Proof]
	if !ok {
		return nil, zip.ErrNotFound("no such payment")
	}
	return &e, nil
}

// query answers the three point reads the statement and the dispute make, from the
// facts published so far; any other read is a test that should not reach one.
func (b *book) query(t *testing.T) func(context.Context, string, ...any) ([]map[string]any, error) {
	landedQ, _ := landedSQL([]fact{{}, {}})
	restatesQ, _ := restatesSQL(planeops.EconomicEvent{})
	eventQ, _ := eventSQL("", "")
	isEventName := func(f fact) bool { return f.name == economicPayment || f.name == economicCorrection }
	return func(_ context.Context, q string, args ...any) ([]map[string]any, error) {
		var out []map[string]any
		if b.slow {
			return out, nil
		}
		for _, f := range b.w.facts {
			switch q {
			case landedQ:
				if (f.org == args[0] || f.org == args[1]) && f.time.Unix() == args[3] && isEventName(f) && f.attributes["event"] == args[4] {
					out = append(out, map[string]any{"org": f.org, "id": f.id})
				}
			case restatesQ:
				if f.org == args[0] && f.time.Unix() == args[2] && isEventName(f) && f.attributes["event"] == args[3] {
					return []map[string]any{row(f)}, nil
				}
			case eventQ:
				if f.org == args[0] && isEventName(f) && f.attributes["event"] == args[2] {
					return []map[string]any{row(f)}, nil
				}
			default:
				t.Fatalf("a read this test does not answer:\n%s", q)
			}
		}
		return out, nil
	}
}

// row is what the warehouse answers for a fact.
func row(f fact) map[string]any {
	attrs := map[string]string{}
	for k, v := range f.attributes {
		attrs[k] = v
	}
	return map[string]any{"event": attrs["event"], "rail": f.kind, "attrs": attrs, "name": f.name}
}

func status(err error) int {
	if he, ok := err.(*zip.HTTPError); ok {
		return he.Status
	}
	return 0
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A caller names a payment, and the payment is what the RAIL answers for it: the
// orgs, the amount and the time on the rows are the rail's, whatever the caller is.
// A rail no app owns, a proof the rail does not know, and an answer about another
// payment are all refused with nothing landed.
func TestOnlyTheRailSaysWhatAPaymentWas(t *testing.T) {
	b := economicBook(t)
	n := b.record(paid())

	out, err := planeEconomic(asPeer("acme"), n)
	if err != nil {
		t.Fatalf("planeEconomic: %v", err)
	}
	if want := planeops.EconomicID(planeops.RailX402, paid().Proof); out.ID != want || out.Landed {
		t.Fatalf("stated %+v, want id %q not yet landed — the id is the rail's and the proof's", out, want)
	}
	if !slices.Equal(b.asked, []string{"acme"}) {
		t.Fatalf("the rail was asked acting for %v, want the caller's org", b.asked)
	}
	for _, f := range b.w.facts {
		if f.attributes["gross"] != "15" || f.attributes["buyer"] != "acme" || f.attributes["seller"] != "globex" {
			t.Fatalf("row %+v is not what the rail recorded", f.attributes)
		}
	}

	refused := func(what string, ctx context.Context, n *planeops.EconomicNotice, want int) {
		t.Helper()
		before := len(b.w.facts)
		if _, err := planeEconomic(ctx, n); status(err) != want {
			t.Errorf("%s: %v, want %d", what, err, want)
		}
		if len(b.w.facts) != before {
			t.Errorf("%s: %d rows landed", what, len(b.w.facts)-before)
		}
	}
	refused("a proof the rail never recorded", asPeer("acme"),
		&planeops.EconomicNotice{Rail: planeops.RailX402, Proof: "never-settled"}, http.StatusNotFound)
	refused("a rail no app owns", asPeer("acme"),
		&planeops.EconomicNotice{Rail: planeops.RailWire, Proof: "FED-1"}, http.StatusForbidden)
	refused("a rail that is no rail", asPeer("acme"),
		&planeops.EconomicNotice{Rail: "paypal", Proof: "p"}, http.StatusForbidden)
	refused("a third org", asPeer("initech"), n, http.StatusForbidden)
	refused("no org at all", context.Background(), n, http.StatusForbidden)
	refused("a control character", asPeer("acme"),
		&planeops.EconomicNotice{Rail: planeops.RailX402, Proof: "a\nb"}, http.StatusBadRequest)

	other := paid()
	other.Proof = "x402_other"
	b.records[planeops.RailX402+"\x00"+"x402_asked"] = other
	refused("an answer about another payment", asPeer("acme"),
		&planeops.EconomicNotice{Rail: planeops.RailX402, Proof: "x402_asked"}, http.StatusBadGateway)
}

// A payment is two rows — the buyer's copy and the seller's — at the time the rail
// recorded it. The seller's copy names no one inside the buyer, and its id is the
// digest of the seller's own copy, so nothing about the buyer's principal can be
// recovered from it either. A third org has no row.
func TestOneRowPerPartyAndTheSellersCopyNamesNoOne(t *testing.T) {
	b := economicBook(t)
	e := paid()
	e.At = time.Now().Add(-48 * time.Hour).Unix()
	if _, err := planeEconomic(asPeer("globex"), b.record(e)); err != nil {
		t.Fatalf("the seller could not state its own payment: %v", err)
	}
	if len(b.w.facts) != 2 {
		t.Fatalf("%d facts, want one per party", len(b.w.facts))
	}
	byOrg := map[string]fact{}
	for _, f := range b.w.facts {
		byOrg[f.org] = f
		if f.signal != signalEconomic || f.kind != planeops.RailX402 || f.name != economicPayment {
			t.Fatalf("row %+v", f)
		}
		if f.time.Unix() != e.At || f.attributes["at"] != strconv.FormatInt(e.At, 10) {
			t.Fatalf("row at %v, want the rail's time %d: no server clock orders a payment", f.time, e.At)
		}
	}
	buyer, seller := byOrg["acme"], byOrg["globex"]
	if buyer.attributes["agent"] != "u_acme" || buyer.attributes["parent"] != "agent_7" {
		t.Fatalf("the buyer's copy lost its own principal: %v", buyer.attributes)
	}
	if seller.attributes["agent"] != "" || seller.attributes["parent"] != "" {
		t.Fatalf("the seller's copy names the buyer's principal: %v", seller.attributes)
	}
	want, _ := canonical(e)
	sellers := copyFor(want, "globex")
	if seller.id != digest(sellers) || buyer.id != digest(want) || seller.id == buyer.id {
		t.Fatal("a row's id is not the digest of that party's own copy")
	}
	for _, f := range b.w.facts {
		back, err := economicRows([]map[string]any{row(f)})
		if err != nil || len(back) != 1 {
			t.Fatalf("read back: %v %v", back, err)
		}
		if got, want := mustJSON(t, back[0]), mustJSON(t, copyFor(want, f.org)); string(got) != string(want) {
			t.Fatalf("read back %s, want %s", got, want)
		}
	}
}

// The same rail and proof are one event and one row per party. Stated again before
// it lands, it is published again under the same key and collapses; once landed,
// the plane answers landed and publishes nothing. A statement that says something
// different about a payment already landed is refused: the first one stands.
func TestTheFirstStatementStands(t *testing.T) {
	b := economicBook(t)
	n := b.record(paid())
	b.slow = true
	if _, err := planeEconomic(asPeer("acme"), n); err != nil {
		t.Fatal(err)
	}
	if _, err := planeEconomic(asPeer("acme"), n); err != nil {
		t.Fatal(err)
	}
	if len(b.w.facts) != 4 || b.w.facts[0].id != b.w.facts[2].id || !b.w.facts[0].time.Equal(b.w.facts[2].time) {
		t.Fatal("a restatement before landing is not the same row")
	}
	b.slow = false
	out, err := planeEconomic(asPeer("globex"), n)
	if err != nil || !out.Landed {
		t.Fatalf("landed payment stated again: %+v %v, want landed", out, err)
	}
	if len(b.w.facts) != 4 {
		t.Fatal("a landed payment was published again")
	}
	changed := paid()
	changed.Gross, changed.FMV = "1500", "1500"
	b.record(changed)
	if _, err := planeEconomic(asPeer("acme"), n); status(err) != http.StatusConflict {
		t.Fatalf("a different statement of a landed payment: %v, want 409", err)
	}
	if len(b.w.facts) != 4 {
		t.Fatal("a different statement of a landed payment was published")
	}
}

// With no warehouse to ask, a payment still goes on the stream and the rail is told
// it has not landed, so it asks again. A correction is refused: it is stated only
// against the event it names.
func TestNoWarehouseLeavesTheStatementOwed(t *testing.T) {
	b := economicBook(t)
	prevWait, prevGrace := warehouseWait, warehouseGrace
	warehouseReady = func() bool { return false }
	warehouseWait = func(context.Context) error { return context.DeadlineExceeded }
	warehouseGrace = time.Millisecond
	t.Cleanup(func() { warehouseWait, warehouseGrace = prevWait, prevGrace })
	out, err := planeEconomic(asPeer("acme"), b.record(paid()))
	if err != nil || out.Landed || len(b.w.facts) != 2 {
		t.Fatalf("stated %+v %v with %d rows, want published and not landed", out, err, len(b.w.facts))
	}
	fix := paid()
	fix.Proof, fix.Corrects, fix.Gross, fix.FMV = "x402_refund", out.ID, "5", "5"
	if _, err := planeEconomic(asPeer("acme"), b.record(fix)); status(err) != http.StatusServiceUnavailable {
		t.Fatalf("a correction with no warehouse: %v, want 503", err)
	}
}

// A correction is the rail's, like every statement, and it restates only a payment
// that has landed on the SAME rail, between the SAME orgs, at the SAME time. A
// correction naming anything else — another rail's payment, other orgs, another
// time, or a payment not yet landed — is refused, so no correction moves a payment
// into another year or onto another rail. A read marks the original superseded.
func TestACorrectionRestatesOnlyItsOwnRailsPayment(t *testing.T) {
	b := economicBook(t, planeops.RailWire)
	orig := paid()
	o, err := planeEconomic(asPeer("acme"), b.record(orig))
	if err != nil {
		t.Fatal(err)
	}
	correction := func(mut func(*planeops.EconomicEvent)) planeops.EconomicEvent {
		c := paid()
		c.Proof, c.Corrects, c.Gross, c.FMV = "x402_refund_"+strconv.Itoa(len(b.records)), o.ID, "5", "5"
		mut(&c)
		return c
	}
	for name, mut := range map[string]func(*planeops.EconomicEvent){
		"another rail":    func(c *planeops.EconomicEvent) { c.Rail = planeops.RailWire },
		"other orgs":      func(c *planeops.EconomicEvent) { c.Seller = "initech" },
		"another time":    func(c *planeops.EconomicEvent) { c.At -= 366 * 24 * 3600 },
		"no such payment": func(c *planeops.EconomicEvent) { c.Corrects = planeops.EconomicID(planeops.RailX402, "x") },
	} {
		before := len(b.w.facts)
		if _, err := planeEconomic(asPeer("acme"), b.record(correction(mut))); status(err) != http.StatusConflict {
			t.Errorf("a correction of %s: %v, want 409", name, err)
		}
		if len(b.w.facts) != before {
			t.Errorf("a correction of %s landed", name)
		}
	}

	c := correction(func(*planeops.EconomicEvent) {})
	b.slow = true
	if _, err := planeEconomic(asPeer("acme"), b.record(c)); status(err) != http.StatusConflict {
		t.Fatalf("a correction whose payment has not landed: %v, want 409 until it has", err)
	}
	b.slow = false
	fixed, err := planeEconomic(asPeer("globex"), b.record(c))
	if err != nil {
		t.Fatalf("the rail's correction: %v", err)
	}
	if fixed.ID == o.ID {
		t.Fatal("a correction took the id of the event it corrects")
	}
	fixes := b.w.facts[len(b.w.facts)-2:]
	if fixes[0].name != economicCorrection || fixes[0].attributes["corrects"] != o.ID || !fixes[0].time.Equal(b.w.facts[0].time) {
		t.Fatalf("correction rows %+v", fixes)
	}
	if b.w.facts[0].attributes["gross"] != "15" {
		t.Fatal("the corrected event's row changed")
	}
	rows, err := economicRows([]map[string]any{row(b.w.facts[0]), row(fixes[0])})
	if err != nil {
		t.Fatal(err)
	}
	if got := superseded(rows, rows[1:]); !slices.Equal(got, []string{o.ID}) {
		t.Fatalf("superseded %v, want the corrected event", got)
	}
	for name, mut := range map[string]func(*planeops.EconomicEvent){
		"other orgs":   func(c *planeops.EconomicEvent) { c.Seller = "initech" },
		"another rail": func(c *planeops.EconomicEvent) { c.Rail = planeops.RailWire },
		"another time": func(c *planeops.EconomicEvent) { c.At++ },
	} {
		stranger := rows[1]
		mut(&stranger)
		if got := superseded(rows, []planeops.EconomicEvent{stranger}); len(got) != 0 {
			t.Errorf("a correction on %s superseded a payment it is not about", name)
		}
	}
}

// What a rail may answer, and what it may not: anything that is not an event is
// the rail's fault, a 502, and lands nothing.
func TestARailAnswerThatIsNotAPaymentLandsNothing(t *testing.T) {
	b := economicBook(t, planeops.RailChain)
	for name, mut := range map[string]func(*planeops.EconomicEvent){
		"one org":          func(e *planeops.EconomicEvent) { e.Seller = "acme" },
		"no seller":        func(e *planeops.EconomicEvent) { e.Seller = "" },
		"uuid org":         func(e *planeops.EconomicEvent) { e.Seller = "cd35a51b-f7e7-5412-b67f-58b8703e5219" },
		"another id":       func(e *planeops.EconomicEvent) { e.ID = planeops.EconomicID("wire", "x") },
		"newer version":    func(e *planeops.EconomicEvent) { e.V = planeops.EconomicVersion + 1 },
		"bad currency":     func(e *planeops.EconomicEvent) { e.Currency = "usd" },
		"negative gross":   func(e *planeops.EconomicEvent) { e.Gross = "-1" },
		"zero payment":     func(e *planeops.EconomicEvent) { e.Gross, e.FMV = "0", "0" },
		"usd fmv":          func(e *planeops.EconomicEvent) { e.FMV = "14" },
		"fee over gross":   func(e *planeops.EconomicEvent) { e.Fee = "10"; e.Tax = "6" },
		"tx no chain":      func(e *planeops.EconomicEvent) { e.Tx = "0xabc" },
		"bad chain":        func(e *planeops.EconomicEvent) { e.Chain, e.Tx = "Base", "0xabc" },
		"bad place":        func(e *planeops.EconomicEvent) { e.Jurisdictions = []string{"usa"} },
		"future":           func(e *planeops.EconomicEvent) { e.At = time.Now().Add(time.Hour).Unix() },
		"milliseconds":     func(e *planeops.EconomicEvent) { e.At = time.Now().UnixMilli() },
		"before rails":     func(e *planeops.EconomicEvent) { e.At = 1 },
		"bad corrects":     func(e *planeops.EconomicEvent) { e.Corrects = "x402_1" },
		"corrects itself":  func(e *planeops.EconomicEvent) { e.Corrects = planeops.EconomicID(e.Rail, e.Proof) },
		"long service":     func(e *planeops.EconomicEvent) { e.Service = strings.Repeat("s", maxEconomicText+1) },
		"control in agent": func(e *planeops.EconomicEvent) { e.Agent = "a\x00b" },
	} {
		e := paid()
		mut(&e)
		if _, err := planeEconomic(asPeer("acme"), b.record(e)); status(err) != http.StatusBadGateway {
			t.Errorf("%s: stated (%v)", name, err)
		}
	}
	chainless := paid()
	chainless.Rail = planeops.RailChain
	if _, err := planeEconomic(asPeer("acme"), b.record(chainless)); status(err) != http.StatusBadGateway {
		t.Errorf("a chain payment with no transaction: %v", err)
	}
	if len(b.w.facts) != 0 {
		t.Fatalf("%d facts landed from refused statements", len(b.w.facts))
	}
	chain := paid()
	chain.Rail, chain.Currency, chain.Gross, chain.FMV = planeops.RailChain, "USDC", "1000.000000", "999.5"
	chain.Chain, chain.Tx, chain.Jurisdictions = "eip155:8453", "0x"+strings.Repeat("c", 64), []string{"US-CA", "US", "US"}
	if _, err := planeEconomic(asPeer("acme"), b.record(chain)); err != nil {
		t.Fatalf("a chain payment at its fair market value: %v", err)
	}
	if a := b.w.facts[0].attributes; a["gross"] != "1000" || a["fmv"] != "999.5" || a["jurisdictions"] != "US,US-CA" {
		t.Fatalf("not canonical: %v", a)
	}
}

// No taxpayer number, address or credential rides in the free text an economic row
// carries, and nothing else in the text is lost.
func TestEconomicFreeTextCarriesNoTIN(t *testing.T) {
	for in, want := range map[string]string{
		"SSN 123-45-6789 on file":            "SSN [redacted] on file",
		"EIN 12-3456789.":                    "EIN [redacted].",
		"tin 123456789 given":                "tin [redacted] given",
		"mail a@b.example":                   "mail [redacted]",
		"/v1/tool?api_key=sk-abcdefgh12345":  "/v1/tool[redacted]",
		"order 1234567890 and 12345678 stay": "order 1234567890 and 12345678 stay",
	} {
		if got := scrubEconomic(in); got != want {
			t.Errorf("scrubEconomic(%q) = %q, want %q", in, got, want)
		}
	}
	e := paid()
	e.Service = "/v1/w9/123-45-6789"
	c, err := canonical(e)
	if err != nil || strings.Contains(c.Service, "6789") {
		t.Fatalf("a TIN in what was bought reached the row: %q %v", c.Service, err)
	}
}

// A payment says what it was for in the rail's closed words, and the word
// travels with it: a category outside them is refused, not stored, and one inside
// them is on the row and read back.
func TestACategoryIsOneOfTheRailsWords(t *testing.T) {
	e := paid()
	e.Category = "bribe"
	if _, err := canonical(e); err == nil {
		t.Fatal("a category outside the closed set was accepted")
	}
	e.Category = planeops.CategoryRoyalty
	c, err := canonical(e)
	if err != nil {
		t.Fatalf("a royalty was refused: %v", err)
	}
	back, err := economicOf(planeops.RailX402, economicAttrs(c))
	if err != nil || back.Category != planeops.CategoryRoyalty {
		t.Fatalf("read back %+v %v, want the royalty it was stated as", back, err)
	}
	e.Category = ""
	if c, err := canonical(e); err != nil || c.Category != "" {
		t.Fatalf("an event stated before categories = %+v %v, want it kept as it was", c, err)
	}
}

// Every read binds the caller's org first and the signal second, both as
// parameters; a counterparty is matched against the OTHER side only; and only
// events are listed, never a dispute.
func TestEconomicReadBindsTheCallersOrg(t *testing.T) {
	in := planeops.EconomicsIn{From: 1767225600, To: 1798761600, Counterparty: "globex", Rail: "x402"}
	q, args := economicSQL("initech", in, cursor{}, 10)
	if !strings.Contains(q, "WHERE org = ? AND signal = ? AND "+isEvent) || args[0] != "initech" || args[1] != string(signalEconomic) {
		t.Fatalf("read does not lead with the caller's org: %s %v", q, args)
	}
	if strings.Contains(q, "initech") || strings.Contains(q, "globex") {
		t.Fatal("an org was interpolated into the statement")
	}
	want := []any{"initech", "economic", int64(1767225600), int64(1798761600), "x402",
		"globex", "initech", "globex", "initech", 11}
	if !slices.Equal(args, want) {
		t.Fatalf("args %v, want %v", args, want)
	}
	page := []planeops.EconomicEvent{{ID: "econ_a", At: 10}, {ID: "econ_b", At: 20}}
	bq, bargs := besideSQL("initech", page)
	if !strings.Contains(bq, "WHERE org = ? AND signal = ?") || bargs[1] != "initech" || bargs[2] != string(signalEconomic) {
		t.Fatalf("what stands beside a page is not read in the caller's org: %s %v", bq, bargs)
	}
	if !slices.Equal(bargs[0].([]string), []string{"econ_a", "econ_b"}) || bargs[3] != int64(10) || bargs[4] != int64(20) {
		t.Fatalf("corrections are not read over the page's own range: %v", bargs)
	}
	if strings.Count(bq, "?") != len(bargs) || strings.Count(bq, "has(page,") != 2 {
		t.Fatalf("the page's ids are not bound once: %s", bq)
	}
	for _, sql := range []func() (string, []any){
		func() (string, []any) { return eventSQL("initech", "econ_a") },
		func() (string, []any) { return restatesSQL(planeops.EconomicEvent{Buyer: "initech"}) },
	} {
		q, args := sql()
		if !strings.Contains(q, "WHERE org = ? AND signal = ?") || args[0] != "initech" {
			t.Fatalf("a point read does not lead with the org: %s %v", q, args)
		}
	}
	if _, err := economics(context.Background(), "acme", planeops.EconomicsIn{From: 1, To: 2, Counterparty: "acme"}); status(err) != http.StatusBadRequest {
		t.Fatalf("an org read itself as its own counterparty: %v", err)
	}
}

// A page costs a page: the window and the cursor are ranges of the sort key's time
// in the WHERE — never a HAVING over every row the org has — and the read takes
// each event's least row and stops at the limit.
func TestAPageIsARangeOfTheKey(t *testing.T) {
	e := planeops.EconomicEvent{ID: planeops.EconomicID("x402", "p"), At: 1780000000}
	c, err := decodeCursor(encodeCursor(e))
	if err != nil || c.at != e.At || c.event != e.ID {
		t.Fatalf("cursor round trip: %+v %v", c, err)
	}
	for _, bad := range []string{"x", "1.econ_zz", "abc." + e.ID} {
		if _, err := decodeCursor(bad); status(err) != http.StatusBadRequest {
			t.Errorf("cursor %q accepted", bad)
		}
	}
	q, args := economicSQL("acme", planeops.EconomicsIn{From: 1, To: 2}, c, 5)
	where, tail, ok := strings.Cut(q, "ORDER BY")
	if !ok || strings.Contains(q, "GROUP BY") || strings.Contains(q, "HAVING") {
		t.Fatalf("a page groups every row the org has:\n%s", q)
	}
	for _, clause := range []string{
		"time >= toDateTime64(?, 9, 'UTC') AND time < toDateTime64(?, 9, 'UTC')",
		"(time > toDateTime64(?, 9, 'UTC') OR (time = toDateTime64(?, 9, 'UTC') AND attributes['event'] > ?))",
	} {
		if !strings.Contains(where, clause) {
			t.Fatalf("the WHERE does not bound the key by %s:\n%s", clause, q)
		}
	}
	if !strings.Contains(tail, "time, event, id") || !strings.Contains(tail, "LIMIT 1 BY event") {
		t.Fatalf("a page does not take each event's least row in key order:\n%s", q)
	}
	if args[len(args)-2] != e.ID || args[len(args)-1] != 6 {
		t.Fatalf("page after %v: %v", c, args)
	}
}

// No wire type reaches the economic signal: /v1/event cannot mint a payment.
func TestNoWireTypeRoutesToTheEconomicSignal(t *testing.T) {
	for _, typ := range []string{"", "economic", "econ", "payment", "event", "track", "ECONOMIC", "dispute"} {
		if r := routeOf(CaptureEvent{Type: typ, Event: "x"}); r.signal == signalEconomic {
			t.Fatalf("wire type %q routes to the economic signal", typ)
		}
	}
}

// The sink never gives an economic fact up; every other signal keeps its bound.
func TestEconomicFactsAreKept(t *testing.T) {
	for _, w := range writers {
		want := maxDeliver
		if w.signal == signalEconomic {
			want = -1
		}
		if got := w.deliveries(); got != want {
			t.Errorf("%s delivered %d times at most, want %d", w.signal, got, want)
		}
	}
	if !landableSignals[signalEconomic] {
		t.Fatal("no writer lands the economic signal")
	}
}

// A dispute is one party's record beside a payment. Both parties' partitions get
// it, it names the payment and the disputing org, and it amends nothing: no event
// row is added or changed, and a read lists only events. A caller can dispute only
// a payment its own org is party to.
func TestADisputeAmendsNothing(t *testing.T) {
	b := economicBook(t)
	o, err := planeEconomic(asPeer("acme"), b.record(paid()))
	if err != nil {
		t.Fatal(err)
	}
	events := slices.Clone(b.w.facts)
	app := mountApp(t)
	post := func(hdr map[string]string, body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/event/economic/dispute", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(out)
	}
	sellerAdmin := map[string]string{"X-User-Id": "u_g", "X-Org-Id": "globex", "X-User-IsOrgAdmin": "true"}
	body := `{"event":"` + o.ID + `","reason":"never delivered; ask ops@globex.example, EIN 12-3456789"}`
	if code, out := post(map[string]string{"X-User-Id": "u_g", "X-Org-Id": "globex"}, body); code != http.StatusForbidden {
		t.Fatalf("a member disputed: %d %s", code, out)
	}
	if code, out := post(map[string]string{"X-User-Id": "u_i", "X-Org-Id": "initech", "X-User-IsOrgAdmin": "true"}, body); code != http.StatusNotFound {
		t.Fatalf("a third org disputed a payment it is not party to: %d %s", code, out)
	}
	if code, out := post(sellerAdmin, `{"event":"`+o.ID+`","reason":""}`); code != http.StatusBadRequest {
		t.Fatalf("an empty reason: %d %s", code, out)
	}
	code, out := post(sellerAdmin, body)
	if code != http.StatusOK {
		t.Fatalf("the seller's admin disputed its payment: %d %s", code, out)
	}
	var d planeops.EconomicDispute
	if err := json.Unmarshal([]byte(out), &d); err != nil || d.Event != o.ID || d.By != "globex" ||
		strings.Contains(d.Reason, "@") || strings.Contains(d.Reason, "3456789") {
		t.Fatalf("dispute %+v (%v): want the event, the disputing org, and a scrubbed reason", d, err)
	}
	added := b.w.facts[len(events):]
	orgs := []string{}
	for _, f := range added {
		if f.name != economicDispute || f.attributes["disputes"] != o.ID || f.attributes["event"] != "" {
			t.Fatalf("a dispute row %+v is an event, or names another", f)
		}
		orgs = append(orgs, f.org)
	}
	slices.Sort(orgs)
	if !slices.Equal(orgs, []string{"acme", "globex"}) {
		t.Fatalf("a dispute landed under %v, want both parties", orgs)
	}
	for i, f := range events {
		if b.w.facts[i].id != f.id || b.w.facts[i].attributes["gross"] != f.attributes["gross"] {
			t.Fatal("a dispute changed the event it disputes")
		}
	}
}

// The org's own read and its dispute are its admins'.
func TestEconomicReadIsForTheOrgsAdmins(t *testing.T) {
	app := mountApp(t)
	call := func(method, path string, hdr map[string]string) int {
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"event":"` + planeops.EconomicID("x402", "p") + `","reason":"r"}`)
		}
		req := httptest.NewRequest(method, path, body)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	member := map[string]string{"X-User-Id": "u", "X-Org-Id": "acme"}
	orgAdmin := map[string]string{"X-User-Id": "u", "X-Org-Id": "acme", "X-User-IsOrgAdmin": "true"}
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/v1/event/economic?year=2026"},
		{http.MethodPost, "/v1/event/economic/dispute"},
	} {
		if code := call(r.method, r.path, nil); code != http.StatusUnauthorized {
			t.Errorf("%s %s with no principal = %d, want 401", r.method, r.path, code)
		}
		if code := call(r.method, r.path, member); code != http.StatusForbidden {
			t.Errorf("%s %s by a member = %d, want 403", r.method, r.path, code)
		}
	}
	if code := call(http.MethodGet, "/v1/event/economic?year=1999", orgAdmin); code != http.StatusBadRequest {
		t.Errorf("a year before any rail = %d, want 400", code)
	}
	if code := call(http.MethodGet, "/v1/event/economic?year=2026", orgAdmin); code != http.StatusServiceUnavailable {
		t.Errorf("an admin = %d, want the warehouse gate's 503 in a test process", code)
	}
}
