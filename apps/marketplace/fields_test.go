package marketplace

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/internal/kmstest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
)

// raw is a column as the engine holds it.
func raw(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// sealedNotPlain holds a stored column to ciphertext that does not contain what it hides.
func sealedNotPlain(t *testing.T, where, stored, plain string) {
	t.Helper()
	if !strings.HasPrefix(stored, "seal:") || strings.Contains(stored, plain) {
		t.Errorf("%s rests as %.30q: not sealed", where, stored)
	}
}

func openJob(t *testing.T, s *Store, id string) {
	t.Helper()
	j := jobRow{Job: Job{ID: id, Title: "t", BuyerOrg: "buyer", SellerOrg: "seller", Status: StatusOpen, CreatedAt: 1, UpdatedAt: 1,
		Brief: "Draft the contract for Ada Lovelace's estate"}, payment: "0xsignedauthorization-" + id, payer: "0xAda" + id, review: 10}
	if err := s.putJob(context.Background(), j, "buyer", func() error { return nil }); err != nil {
		t.Fatalf("put job %s: %v", id, err)
	}
}

// freshStore is a store over its own KMS, which a test reads what the store keeps from.
func freshStore(t *testing.T) (*Store, *kmstest.Mem) {
	t.Helper()
	k := kmstest.New()
	s, err := Open(t.TempDir(), testMaster, k)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, k
}

// What the buyer asked and the address that signed, and what the seller said and the
// reason of a dispute, rest as ciphertext on both engines; the buyer's signed payment and
// the delivery link, which are credentials, are KMS values in the org that wrote them, and
// the row holds their paths. However the row is written, the insert or a move that names the
// column, every read gives them back, and what a predicate reads stays plain; the signed
// payment is read by the reads that act on a job and not by a listing.
func TestAJobsWordsRestSealedAndItsCredentialsLiveInKMS(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, k := freshStore(t)
		openJob(t, s, "job_1")
		sealedNotPlain(t, "jobs.brief", raw(t, s, `SELECT brief FROM jobs WHERE id = 'job_1'`), "Lovelace")
		sealedNotPlain(t, "jobs.payer", raw(t, s, `SELECT payer FROM jobs WHERE id = 'job_1'`), "0xAda")
		const payRef = "orgs/buyer/marketplace/job_1/payment@default"
		if v := raw(t, s, `SELECT payment FROM jobs WHERE id = 'job_1'`); v != "kms:"+payRef {
			t.Fatalf("jobs.payment holds %q, want its KMS path in the buyer's org", v)
		}
		if k.Held()[payRef] != "0xsignedauthorization-job_1" {
			t.Fatalf("KMS holds %v, want the signed payment at its path", k.Held())
		}
		if v := raw(t, s, `SELECT delivery_note FROM jobs WHERE id = 'job_1'`); v != "" {
			t.Errorf("an empty delivery note rests as %q", v)
		}

		const note, url, reason = "Summary for Ada Lovelace attached", "https://files.example.com/q3.pdf?token=secret", "Ada says it never arrived"
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			moved, err := s.move(ctx, tx, "job_1", []string{StatusOpen}, "", StatusDelivered, "seller",
				map[string]any{"delivery_note": note, "delivery_url": url, "delivery_hash": "0xhash", "delivered_at": int64(5)})
			if err != nil || !moved {
				t.Fatalf("deliver: moved=%v err=%v", moved, err)
			}
			moved, err = s.move(ctx, tx, "job_1", []string{StatusDelivered}, "", StatusDisputed, "buyer",
				map[string]any{"dispute_reason": reason, "disputed_by": "buyer", "disputed_at": int64(6)})
			if err != nil || !moved {
				t.Fatalf("dispute: moved=%v err=%v", moved, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		sealedNotPlain(t, "jobs.delivery_note", raw(t, s, `SELECT delivery_note FROM jobs WHERE id = 'job_1'`), "Lovelace")
		const urlRef = "orgs/seller/marketplace/job_1/delivery_url@default"
		if v := raw(t, s, `SELECT delivery_url FROM jobs WHERE id = 'job_1'`); v != "kms:"+urlRef || k.Held()[urlRef] != url {
			t.Fatalf("jobs.delivery_url holds %q and KMS %q, want its path in the seller's org and the link", v, k.Held()[urlRef])
		}
		sealedNotPlain(t, "jobs.dispute_reason", raw(t, s, `SELECT dispute_reason FROM jobs WHERE id = 'job_1'`), "arrived")
		if h := raw(t, s, `SELECT delivery_hash FROM jobs WHERE id = 'job_1'`); h != "0xhash" {
			t.Errorf("the delivery hash rests as %q, want it plain", h)
		}

		j, ok, err := s.job(ctx, "job_1")
		if err != nil || !ok {
			t.Fatalf("job: %v %v", ok, err)
		}
		if j.Brief != "Draft the contract for Ada Lovelace's estate" || j.payment != "0xsignedauthorization-job_1" || j.payer != "0xAdajob_1" ||
			j.Delivery == nil || j.Delivery.Note != note || j.Delivery.URL != url || j.Delivery.Hash != "0xhash" ||
			j.Dispute == nil || j.Dispute.Reason != reason {
			t.Fatalf("job read back as %+v", j)
		}
		reads := k.Reads()
		if mine, err := s.jobsOf(ctx, false, "buyer", "", 10); err != nil || len(mine) != 1 || mine[0].Delivery.Note != note || mine[0].Delivery.URL != url || mine[0].payment != "" {
			t.Fatalf("jobsOf = %+v, %v; a listing carries the link and not the signed payment", mine, err)
		}
		if k.Reads() != reads+1 {
			t.Fatalf("a listing of one job read KMS %d times, want the delivery link only", k.Reads()-reads)
		}
		// A job removed with its quote forgets the payment it held.
		openJob(t, s, "job_q")
		if _, err := s.db.Exec(`UPDATE jobs SET status = $1 WHERE id = 'job_q'`, StatusQuoted); err != nil {
			t.Fatal(err)
		}
		if err := s.dropQuote(ctx, "job_q"); err != nil {
			t.Fatal(err)
		}
		if _, ok := k.Held()["orgs/buyer/marketplace/job_q/payment@default"]; ok {
			t.Fatal("a dropped quote left its payment in KMS")
		}
	})
}

// A ciphertext is bound to its job and its column: carried to another job, or planted in
// the clear, it is refused.
func TestAJobsSealedFieldIsBoundToItsRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, _ := freshStore(t)
		openJob(t, s, "job_1")
		openJob(t, s, "job_2")
		if _, err := s.db.Exec(`UPDATE jobs SET brief = $1 WHERE id = 'job_2'`, raw(t, s, `SELECT brief FROM jobs WHERE id = 'job_1'`)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.job(ctx, "job_2"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("one job's brief opened on another: %v", err)
		}
		if _, err := s.db.Exec(`UPDATE jobs SET brief = $1, payer = $2 WHERE id = 'job_2'`, raw(t, s, `SELECT payer FROM jobs WHERE id = 'job_2'`), "0xplanted"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.job(ctx, "job_2"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("a payer sealed for a column opened as the brief: %v", err)
		}
		if _, err := s.db.Exec(`UPDATE jobs SET brief = 'planted' WHERE id = 'job_2'`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.job(ctx, "job_2"); !errors.Is(err, tenure.ErrFieldPlain) {
			t.Fatalf("a planted plaintext read as the store's: %v", err)
		}
	})
}

// A payment path names its own job's authorization: a column pointed at another org's path
// is refused by KMS, and one that names nothing KMS holds is a dangling reference.
func TestAPaymentReferenceReadsOnlyItsOwnValue(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, _ := freshStore(t)
		openJob(t, s, "job_1")
		other := jobRow{Job: Job{ID: "job_o", Title: "t", BuyerOrg: "globex", SellerOrg: "seller", Status: StatusOpen, CreatedAt: 1, UpdatedAt: 1, Brief: "b"},
			payment: "0xglobex-authorization", review: 10}
		if err := s.putJob(ctx, other, "globex", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE jobs SET payment = $1 WHERE id = 'job_1'`, raw(t, s, `SELECT payment FROM jobs WHERE id = 'job_o'`)); err != nil {
			t.Fatal(err)
		}
		if j, _, err := s.job(ctx, "job_1"); err == nil || j.payment != "" {
			t.Fatalf("the buyer's job read globex's payment: %q, %v", j.payment, err)
		}
		if _, err := s.db.Exec(`UPDATE jobs SET payment = 'kms:orgs/buyer/marketplace/job_1/nothing@default' WHERE id = 'job_1'`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.job(ctx, "job_1"); !errors.Is(err, escrow.ErrGone) {
			t.Fatalf("a dangling reference read as %v, want ErrGone", err)
		}
	})
}

// With no KMS a payment is neither kept nor read, and the job is not written.
func TestNoKMSKeepsNoPayment(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := Open(t.TempDir(), testMaster, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		j := jobRow{Job: Job{ID: "job_1", Title: "t", BuyerOrg: "buyer", SellerOrg: "seller", Status: StatusOpen, CreatedAt: 1, UpdatedAt: 1, Brief: "b"}, payment: "0xauth", review: 10}
		if err := s.putJob(context.Background(), j, "buyer", func() error { return nil }); !errors.Is(err, escrow.ErrNoKMS) {
			t.Fatalf("putJob with a payment and no KMS = %v, want ErrNoKMS", err)
		}
		// A quote carries none, and needs no KMS.
		j.payment = ""
		if err := s.putJob(context.Background(), j, "buyer", func() error { return nil }); err != nil {
			t.Fatalf("a quote with no payment: %v", err)
		}
	})
}

// The addresses a seller proves a payout wallet with, and a party's comment, are sealed.
func TestPayoutAddressesAndCommentsRestSealed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, err := Open(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		const address = "0x1111111111111111111111111111111111111111"
		if err := s.challenge(ctx, "seller", "wal_1", address, "sign this", 99); err != nil {
			t.Fatal(err)
		}
		sealedNotPlain(t, "challenges.address", raw(t, s, `SELECT address FROM challenges WHERE org = 'seller'`), address)
		wallet, got, msg, expires, found, err := s.pending(ctx, "seller")
		if err != nil || !found || wallet != "wal_1" || got != address || msg != "sign this" || expires != 99 {
			t.Fatalf("pending = %q %q %q %d %v %v", wallet, got, msg, expires, found, err)
		}
		held, err := s.seal("payouts", "address", address, "seller")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO payouts (org, wallet, address, bound_at) VALUES ('seller', 'wal_1', $1, 5)`, held); err != nil {
			t.Fatal(err)
		}
		p, ok, err := s.payout(ctx, "seller")
		if err != nil || !ok || p.Address != address {
			t.Fatalf("payout = %+v %v %v", p, ok, err)
		}
		comment, err := s.seal("feedback", "comment", "Ada Lovelace was a delight", "job_1", "buyer")
		if err != nil {
			t.Fatal(err)
		}
		sealedNotPlain(t, "feedback.comment", comment, "delight")
		// A payout address lifted into another org's row is refused.
		if _, err := s.db.Exec(`INSERT INTO payouts (org, wallet, address, bound_at) VALUES ('thief', 'wal_2', $1, 5)`, held); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.payout(ctx, "thief"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("another org's payout address opened: %v", err)
		}
	})
}

func TestTheStoreDoesNotOpenWithoutAMaster(t *testing.T) {
	for _, master := range [][]byte{nil, testMaster[:31]} {
		if s, err := Open(t.TempDir(), master, testKMS); err == nil {
			_ = s.Close()
			t.Fatalf("opened with a %d-byte master", len(master))
		}
	}
}

// A file written before the columns were sealed holds plaintext in them, and one written
// before KMS holds the signed payment and the delivery link plain or sealed as a field. The
// open seals the first and blinds the brief, writes the others to KMS and replaces the
// column with the path, once. SQLite only, by nature: hanzo-sql has no file from before.
func TestALegacyFileIsSealedAndMovedAndStillReads(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	ctx := context.Background()
	k := kmstest.New()
	s, err := Open(dir, testMaster, k)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO jobs (id, title, buyer_org, seller_org, amount, category, rail, status, brief, review, recipient, clearance, payment, payer,
			delivery_note, delivery_url, delivery_hash, delivered_at, dispute_reason, disputed_by, disputed_at, created_at, updated_at)
		 VALUES ('old', 't', 'buyer', 'seller', '1', 'c', 'x402', 'disputed', 'old brief', 10, 'w', 'c', 'old payment', '0xold', 'old note', 'https://old', '0xh', 5, 'old reason', 'buyer', 6, 1, 1)`,
		`INSERT INTO feedback (job, from_org, about, rating, comment, at) VALUES ('old', 'buyer', 'seller', 5, 'old comment', 1)`,
		`INSERT INTO payouts (org, wallet, address, bound_at) VALUES ('seller', 'w', '0xoldpayout', 1)`,
		`INSERT INTO challenges (org, wallet, address, message, expires) VALUES ('seller', 'w', '0xoldchallenge', 'm', 1)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ { // the second open finds nothing to do
		held, reads := k.Held(), k.Reads()
		s, err = Open(dir, testMaster, k)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if pass == 2 && (len(k.Held()) != len(held) || k.Reads() != reads) {
			t.Fatalf("a second open touched KMS: %d values, then %d; reads %d then %d", len(held), len(k.Held()), reads, k.Reads())
		}
		j, ok, err := s.job(ctx, "old")
		if err != nil || !ok || j.Brief != "old brief" || j.payment != "old payment" || j.payer != "0xold" ||
			j.Delivery.Note != "old note" || j.Delivery.URL != "https://old" || j.Dispute.Reason != "old reason" {
			t.Fatalf("pass %d: a legacy job = %+v (%v %v)", pass, j, ok, err)
		}
		if got, want := raw(t, s, `SELECT brief_blind FROM jobs WHERE id = 'old'`), s.fields.Blind("jobs", "brief", "old brief"); got != want || got == "" {
			t.Fatalf("pass %d: a legacy brief is blinded as %q, want %q", pass, got, want)
		}
		if p, ok, err := s.payout(ctx, "seller"); err != nil || !ok || p.Address != "0xoldpayout" {
			t.Fatalf("pass %d: a legacy payout = %+v (%v %v)", pass, p, ok, err)
		}
		if _, addr, _, _, found, err := s.pending(ctx, "seller"); err != nil || !found || addr != "0xoldchallenge" {
			t.Fatalf("pass %d: a legacy challenge = %q (%v %v)", pass, addr, found, err)
		}
		for _, c := range []string{
			`SELECT brief FROM jobs`, `SELECT payer FROM jobs`, `SELECT delivery_note FROM jobs`,
			`SELECT dispute_reason FROM jobs`, `SELECT comment FROM feedback`,
			`SELECT address FROM payouts`, `SELECT address FROM challenges`,
		} {
			if v := raw(t, s, c); !strings.HasPrefix(v, "seal:") {
				t.Errorf("pass %d: %s is still %.20q after the upgrade", pass, c, v)
			}
		}
		if v := raw(t, s, `SELECT payment FROM jobs`); v != "kms:orgs/buyer/marketplace/old/payment@default" {
			t.Errorf("pass %d: payment holds %.60q, want its KMS path", pass, v)
		}
		if v := raw(t, s, `SELECT delivery_url FROM jobs`); v != "kms:orgs/seller/marketplace/old/delivery_url@default" {
			t.Errorf("pass %d: delivery_url holds %.60q, want its KMS path", pass, v)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// A quote is found again by its brief, which rests sealed: the equality is over the blind
// index, so the same words find it and different words do not.
func TestAQuoteIsFoundBySealedBrief(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, _ := freshStore(t)
		quote := jobRow{Job: Job{ID: "job_q", Title: "t", BuyerOrg: "buyer", SellerOrg: "seller", Status: StatusQuoted, Category: "c",
			Brief: "Translate the lease for Ada Lovelace", CreatedAt: 10, UpdatedAt: 10, Escrow: Escrow{Rail: RailX402}}, recipient: "w", review: 10}
		if err := s.putJob(ctx, quote, "buyer", func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		got, ok, err := s.quoted(ctx, quote, 0)
		if err != nil || !ok || got.ID != "job_q" || got.Brief != quote.Brief {
			t.Fatalf("the same terms found %+v (%v %v)", got.ID, ok, err)
		}
		quote.Brief = "Translate the lease for someone else"
		if got, ok, err := s.quoted(ctx, quote, 0); err != nil || ok {
			t.Fatalf("other words found %+v (%v %v)", got.ID, ok, err)
		}
	})
}
