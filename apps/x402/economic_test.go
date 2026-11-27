package x402

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// quiet stops the background drainer, so a test runs every pass itself and counts
// exactly what each one did.
func (h *harness) quiet() { live.State.drain.close() }

// announced runs one drain pass, as the drainer would, and fails the test on error.
func (h *harness) announced() int {
	h.t.Helper()
	n, err := Announce(context.Background())
	if err != nil {
		h.t.Fatalf("announce: %v", err)
	}
	return n
}

// owedNow is how many settled payments still owe their statement.
func owedNow(t *testing.T) int {
	t.Helper()
	var n int
	if err := live.State.store.db.QueryRow(
		`SELECT count(*) FROM settlements WHERE settled = 1 AND stated = 0 AND payer_org != payee_org`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// One settlement is one economic event, stated for the payer, derived from the
// row; the client retrying the paid request states nothing more.
func TestSettlementStatesOneEconomicEvent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		h.quiet()
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})
		req := h.challenge("/paid/tool", h.payerOrg)
		now := nowUnix()
		payment := h.pay(req, randNonce(), now-60, now+300)
		code, b, hdr := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, "")
		if code != http.StatusOK {
			t.Fatalf("pay = %d %s", code, b)
		}
		id := h.settlement(hdr).Transaction
		if code, b, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, ""); code != http.StatusOK {
			t.Fatalf("retry = %d %s", code, b)
		}
		h.tie(money.FromCents(100), "after a retry")
		h.announced()
		h.announced()

		got := h.plane.stated()
		if len(got) != 1 {
			t.Fatalf("%d economic events stated, want exactly one for one settlement: %+v", len(got), got)
		}
		e := got[0]
		want := client.EconomicEvent{
			Buyer: h.payerOrg, Seller: h.payeeOrg, Agent: "u_" + h.payerOrg, Service: "/paid/tool",
			Currency: "USD", Gross: "1", FMV: "1", Rail: client.RailX402, Proof: id, At: e.At,
		}
		if e.At < now-60 || e.At > nowUnix() || !equal(e, want) {
			t.Fatalf("stated %+v, want %+v", e, want)
		}
		if actor := h.plane.actors[0]; actor != h.payerOrg {
			t.Fatalf("stated acting for %q, want the payer", actor)
		}
		if n := owedNow(t); n != 0 {
			t.Fatalf("%d statements still owed after the plane landed them", n)
		}
	})
}

func equal(a, b client.EconomicEvent) bool {
	return a.Buyer == b.Buyer && a.Seller == b.Seller && a.Agent == b.Agent && a.Service == b.Service &&
		a.Currency == b.Currency && a.Gross == b.Gross && a.FMV == b.FMV && a.Rail == b.Rail &&
		a.Proof == b.Proof && a.At == b.At && a.Chain == b.Chain && a.Tx == b.Tx
}

// The event plane being down never stops a settlement whose money moved: the payer
// is served, the settlement is settled, and its statement is owed in the same row.
// When the plane is back the drainer states it — once — and owes nothing more.
func TestAPlaneOutageDelaysTheStatementNotThePayment(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		h.quiet()
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(250), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})
		req := h.challenge("/paid/tool", h.payerOrg)
		h.plane.set(true, false)
		code, b, hdr := h.payFor("/paid/tool", h.payerOrg, req, randNonce())
		if code != http.StatusOK {
			t.Fatalf("pay with the plane down = %d %s, want 200: the money moved", code, b)
		}
		h.tie(money.FromCents(250), "served with the plane down")
		id := h.settlement(hdr).Transaction
		st, _, _ := live.State.store.get(context.Background(), id)
		if !st.Settled {
			t.Fatal("a settlement whose money moved is not settled while the plane is down")
		}
		h.announced()
		if n := owedNow(t); n != 1 {
			t.Fatalf("%d statements owed with the plane down, want the one settlement", n)
		}
		if pending, _ := live.State.store.pending(context.Background(), nowUnix()+1); len(pending) != 0 {
			t.Fatalf("%d settlements left unsettled by a plane outage", len(pending))
		}

		h.plane.set(false, false)
		dueNow(t)
		if n := h.announced(); n != 1 {
			t.Fatalf("landed %d when the plane came back, want 1", n)
		}
		if got := h.plane.stated(); len(got) != 1 || got[0].Proof != id {
			t.Fatalf("stated %+v, want the settlement once", got)
		}
		if n := h.announced(); n != 0 || owedNow(t) != 0 {
			t.Fatalf("a second pass landed %d; %d still owed", n, owedNow(t))
		}
		h.tie(money.FromCents(250), "the drain moved no money")
	})
}

// dueNow makes every owed statement due, standing in for its backoff elapsing.
func dueNow(t *testing.T) {
	t.Helper()
	if _, err := live.State.store.db.Exec(`UPDATE settlements SET due = 0`); err != nil {
		t.Fatal(err)
	}
}

// A statement the bus took but the warehouse has not landed is still owed: the
// rail keeps it until the plane says both rows landed, backing off in between, so
// an outage longer than the stream keeps a message loses nothing.
func TestAStatementIsOwedUntilItLands(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		h.quiet()
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})
		req := h.challenge("/paid/tool", h.payerOrg)
		h.plane.set(false, true)
		if code, b, _ := h.payFor("/paid/tool", h.payerOrg, req, randNonce()); code != http.StatusOK {
			t.Fatalf("pay = %d %s", code, b)
		}
		h.announced()
		if owedNow(t) != 1 {
			t.Fatal("a statement the warehouse has not landed was cleared")
		}
		var tries, due int64
		if err := live.State.store.db.QueryRow(`SELECT tries, due FROM settlements`).Scan(&tries, &due); err != nil {
			t.Fatal(err)
		}
		if tries < 1 || due <= time.Now().Unix() {
			t.Fatalf("tries %d due %d: an unlanded statement is not backed off", tries, due)
		}
		calls := h.plane.count()
		h.announced()
		if h.plane.count() != calls {
			t.Fatal("a statement that is not yet due was stated again")
		}
		h.plane.set(false, false)
		dueNow(t)
		if n := h.announced(); n != 1 || owedNow(t) != 0 {
			t.Fatalf("landed %d, %d still owed", n, owedNow(t))
		}
		if got := h.plane.stated(); len(got) != 1 {
			t.Fatalf("stated %d events, want one however often it was sent", len(got))
		}
	})
}

// A backlog — every settlement a release before the outbox left unstated — drains
// batch after batch without waiting a tick between them.
func TestABacklogDrainsBatchAfterBatch(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		h.quiet()
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})
		req := h.challenge("/paid/tool", h.payerOrg)
		if code, b, _ := h.payFor("/paid/tool", h.payerOrg, req, randNonce()); code != http.StatusOK {
			t.Fatalf("pay = %d %s", code, b)
		}
		// Copy the one settlement into a backlog bigger than a batch.
		db := live.State.store.db
		for i := range drainBatch + 5 {
			if _, err := db.Exec(`INSERT INTO settlements
			SELECT 'x402_backlog_'||CAST($1 AS TEXT), payer_org, from_addr, nonce||CAST($1 AS TEXT), resource, payee, payee_org, payee_subj,
			       agent, amount, network, settled_via, tx_hash, 1, created_at, 0, 0, 0, category, payer_subj
			FROM settlements LIMIT 1`, strconv.Itoa(i)); err != nil {
				t.Fatal(err)
			}
		}
		d := drain(live)
		defer d.close()
		deadline := time.Now().Add(5 * time.Second)
		for owedNow(t) != 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := owedNow(t); n != 0 {
			t.Fatalf("%d still owed: the drainer waited a tick between full batches", n)
		}
		if got := len(h.plane.stated()); got != drainBatch+6 {
			t.Fatalf("stated %d, want every settlement once", got)
		}
	})
}

// Backoff doubles from its first wait and never passes its most.
func TestBackoffIsBounded(t *testing.T) {
	if backoff(0) != backoffFirst || backoff(1) != 2*backoffFirst {
		t.Fatalf("backoff(0)=%v backoff(1)=%v", backoff(0), backoff(1))
	}
	for _, n := range []int64{10, 20, 63, 1 << 40} {
		if b := backoff(n); b != backoffMost {
			t.Fatalf("backoff(%d) = %v, want the most, %v", n, b, backoffMost)
		}
	}
}

// A settlement settled before the outbox existed was never stated. The migration
// makes it owed like any other, so a tax year read off the plane misses nothing
// this rail ever settled.
func TestASettlementFromBeforeTheOutboxIsStated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		h.quiet()
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})
		req := h.challenge("/paid/tool", h.payerOrg)
		h.plane.set(true, false)
		if code, b, _ := h.payFor("/paid/tool", h.payerOrg, req, randNonce()); code != http.StatusOK {
			t.Fatalf("pay = %d %s", code, b)
		}
		// The table as the last release left it: settled rows, no outbox columns.
		db := live.State.store.db
		for _, stmt := range []string{
			`DROP INDEX ix_settlements_owed`,
			`ALTER TABLE settlements DROP COLUMN stated`,
			`ALTER TABLE settlements DROP COLUMN tries`,
			`ALTER TABLE settlements DROP COLUMN due`,
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		if err := live.State.store.remigrate(); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		h.plane.set(false, false)
		if n := h.announced(); n != 1 {
			t.Fatalf("stated %d settlements from before the outbox, want 1", n)
		}
	})
}

// An org buying from itself moved money between its own books: nothing is owed and
// nothing is stated, and the rail refuses to describe it as a payment.
func TestASelfPaymentStatesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		h.quiet()
		own := h.createRecipient(h.payerOrg)
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payerOrg, RecipientWalletID: own},
		})
		req := h.challenge("/paid/tool", h.payerOrg)
		code, b, hdr := h.payFor("/paid/tool", h.payerOrg, req, randNonce())
		if code != http.StatusOK {
			t.Fatalf("pay = %d %s", code, b)
		}
		h.announced()
		if got := h.plane.stated(); len(got) != 0 || owedNow(t) != 0 {
			t.Fatalf("a payment to itself was stated: %+v", got)
		}
		notice := &client.EconomicNotice{Rail: client.RailX402, Proof: h.settlement(hdr).Transaction}
		if _, err := (rail{live}).planeEconomic(cloud.For(context.Background(), h.payerOrg), notice); statusOf(err) != http.StatusNotFound {
			t.Fatalf("the rail described a self-payment as a payment: %v", err)
		}
	})
}

// The rail answers what a settlement records to the org that paid it — the org it
// states every settlement for — and to no one else, and only once its money has
// moved: the event plane takes the payment's content from here and nowhere else.
func TestTheRailSaysWhatItSettledToItsPayerOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		h.quiet()
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient},
		})
		req := h.challenge("/paid/tool", h.payerOrg)
		code, b, hdr := h.payFor("/paid/tool", h.payerOrg, req, randNonce())
		if code != http.StatusOK {
			t.Fatalf("pay = %d %s", code, b)
		}
		id := h.settlement(hdr).Transaction
		ask := func(org, rl, proof string) (*client.EconomicEvent, error) {
			r := rail{live}
			return r.planeEconomic(cloud.For(context.Background(), org), &client.EconomicNotice{Rail: rl, Proof: proof})
		}
		e, err := ask(h.payerOrg, client.RailX402, id)
		if err != nil || e.Buyer != h.payerOrg || e.Seller != h.payeeOrg || e.Gross != "1" || e.Proof != id ||
			e.Agent != "u_"+h.payerOrg {
			t.Fatalf("the payer asked: %+v %v", e, err)
		}
		for _, org := range []string{h.payeeOrg, "initech"} {
			if _, err := ask(org, client.RailX402, id); statusOf(err) != http.StatusNotFound {
				t.Fatalf("%s read what the payer's settlement records: %v", org, err)
			}
		}
		if _, err := ask(h.payerOrg, client.RailX402, "x402_nope"); statusOf(err) != http.StatusNotFound {
			t.Fatalf("an unknown proof: %v", err)
		}
		if _, err := ask(h.payerOrg, client.RailWire, id); statusOf(err) != http.StatusBadRequest {
			t.Fatalf("another rail's proof: %v", err)
		}
		if _, err := live.State.store.db.Exec(`UPDATE settlements SET settled = 0 WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := ask(h.payerOrg, client.RailX402, id); statusOf(err) != http.StatusConflict {
			t.Fatalf("a settlement whose money has not moved was described as a payment: %v", err)
		}
		r := rail{live}
		if _, err := r.planeEconomic(context.Background(), &client.EconomicNotice{Rail: client.RailX402, Proof: id}); statusOf(err) != http.StatusForbidden {
			t.Fatalf("a call acting for no org: %v", err)
		}
	})
}

func statusOf(err error) int {
	if he, ok := err.(*zip.HTTPError); ok {
		return he.Status
	}
	return 0
}

// Shutdown stops the drainer before it closes the store, so no pass runs against
// a closed database.
func TestShutdownStopsTheDrainer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newHarness(t)
		d := live.State.drain
		if err := Shutdown(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-waitDone(d):
		case <-time.After(5 * time.Second):
			t.Fatal("the drainer is still running after Shutdown")
		}
	})
}

func waitDone(d *drainer) <-chan struct{} {
	c := make(chan struct{})
	go func() { d.done.Wait(); close(c) }()
	return c
}
