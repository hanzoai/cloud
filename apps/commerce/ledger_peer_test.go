// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// ledger_peer_test.go drives the REAL peer path a customer's finance pages take when
// the ledger lives in another process: a real per-org finance ledger, the real
// /finance/txns op published on a real socket, and the real /v1/finance/* reader in
// apps/billing on the other end of it.
//
// It exists because the reader classified entries on strings the writer never emits —
// the ledger writes `finance.deposit` / `finance.usage`, the reader matched commerce's
// `deposit` / `withdraw` — so credits rendered EMPTY, usage totalled 0, and a deposit
// signed NEGATIVE on a customer's own balance page. The existing finance_test.go stays
// green through all of it because it only ever answers from the S2S HTTP mock, which
// speaks the vocabulary the reader expected. A test that cannot see the wire it ships
// on is not a weaker test, it is the reason a broken wire ships.
//
// So this one holds no strings of its own. It writes through the ledger's own API,
// reads through the customer's own route, and asserts on MONEY.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/billing"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// servePeerLedger wires the production peer half over a temp data dir: a real finance
// ledger published as the process-wide client, and the real ledger-read ops served on
// the canonical "commerce" socket — the exact pair Mount publishes at boot.
func servePeerLedger(t *testing.T) finance.Client {
	t.Helper()
	// A unix socket address is capped near a hundred bytes, and t.TempDir embeds
	// this test's own long name — on darwin the bind failed on the discarded
	// goroutine error and every dial below refused, so the suite was red on any
	// Mac while green in CI. An anonymous short-named dir keeps the address
	// inside the cap on every platform.
	sockDir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	t.Setenv("ZIP_RUNTIME_DIR", sockDir)

	fin := finance.New(t.TempDir())
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })

	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	exposeTxns() // the REAL op, the one Mount publishes

	app := cloud.Plane()
	go func() { _ = app.Listen(zip.SocketPath("commerce")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for range 200 {
		if c, err := net.Dial("unix", zip.SocketPath("commerce")); err == nil {
			_ = c.Close()
			return fin
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("commerce plane socket never began listening")
	return nil
}

// mountReader mounts the REAL customer finance surface. Its commerce link is pointed
// at a server that FAILS the test if it is ever called: the peer path must serve, and
// a silent fall-through to the S2S mock is exactly the blindness this file exists to
// remove.
func mountReader(t *testing.T) *zip.App {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the S2S commerce path was taken for %s — the peer ledger must serve", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("CLOUD_COMMERCE_HTTP_URL", srv.URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-token")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// The composer's install, once at the root, ahead of every route it serves:
	// cloud.Bridge parks the validated org on the context for billing's typed ops.
	app.Use(cloud.Bridge())
	if err := billing.Mount(app, cloud.Deps{Brand: "hanzo"}); err != nil {
		t.Fatalf("billing.Mount: %v", err)
	}
	return app
}

// readAs drives one customer read with a VALIDATED principal for org.
func readAs(t *testing.T, app *zip.App, path, org string) []byte {
	t.Helper()
	return readAsUser(t, app, path, org, "dave")
}

// readAsUser is readAs for a NAMED member, which is the only way to tell two
// billing subjects of one org apart: in the shared signup org the name is what
// account.Payer resolves a person's wallet from, so "who is asking" and "which
// wallet" are the same question.
func readAsUser(t *testing.T, app *zip.App, path, org, user string) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-User-Id", org+"/"+user)
	req.Header.Set("X-Org-Id", org)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: want 200, got %d (%s)", path, resp.StatusCode, b)
	}
	return b
}

// seedBooks posts one grant and one metered debit through the ledger's OWN API — the
// same two calls the admin grant and the edge meter make.
func seedBooks(t *testing.T, fin finance.Client, org string) {
	t.Helper()
	ctx := context.Background()
	if _, err := fin.Deposit(ctx, types.DepositInput{
		Org: org, Subject: org, Amount: money.FromCents(50_000), Currency: "usd",
		Notes: "Startup grant", Ref: "grant-1",
	}); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	if err := fin.RecordUsage(ctx, types.UsageInput{
		Org: org, Subject: org, Amount: money.FromCents(1_200), Currency: "usd",
		Model: "zen", Ref: "use-1",
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}
}

// TestPeerLedger_CreditsRenderTheGrant — money PUT IN must appear on the credits page.
// Over the peer path it rendered an empty array: every row was skipped because the
// reader was looking for a kind the ledger does not write.
func TestPeerLedger_CreditsRenderTheGrant(t *testing.T) {
	fin := servePeerLedger(t)
	seedBooks(t, fin, "acme")
	app := mountReader(t)

	var credits []financeCreditView
	if err := json.Unmarshal(readAs(t, app, "/v1/finance/credits", "acme"), &credits); err != nil {
		t.Fatalf("decode credits: %v", err)
	}
	if len(credits) != 1 {
		t.Fatalf("want the one grant, got %d rows: %+v", len(credits), credits)
	}
	if credits[0].Cents != 50_000 {
		t.Errorf("grant cents: want 50000, got %d", credits[0].Cents)
	}
}

// TestPeerLedger_UsageTotalsTheDebit — metered spend must total the debit, not 0.
func TestPeerLedger_UsageTotalsTheDebit(t *testing.T) {
	fin := servePeerLedger(t)
	seedBooks(t, fin, "acme")
	app := mountReader(t)

	var usage struct {
		TotalCents int64 `json:"totalCents"`
	}
	if err := json.Unmarshal(readAs(t, app, "/v1/finance/usage", "acme"), &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	if usage.TotalCents != 1_200 {
		t.Errorf("usage total: want 1200, got %d", usage.TotalCents)
	}
}

// TestPeerLedger_DepositSignsPositive — the sharpest of the three. A deposit CREDITS
// the wallet, so it renders positive; every other posting debits it. Over the peer
// path the grant signed NEGATIVE, so a customer read their own top-up as a charge.
func TestPeerLedger_DepositSignsPositive(t *testing.T) {
	fin := servePeerLedger(t)
	seedBooks(t, fin, "acme")
	app := mountReader(t)

	var entries []financeLedgerView
	if err := json.Unmarshal(readAs(t, app, "/v1/finance/ledger", "acme"), &entries); err != nil {
		t.Fatalf("decode ledger: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want the grant and the debit, got %d: %+v", len(entries), entries)
	}
	var deposit, usage int64
	for _, e := range entries {
		if e.Cents > 0 {
			deposit += e.Cents
		} else {
			usage += e.Cents
		}
	}
	if deposit != 50_000 {
		t.Errorf("the grant must render POSITIVE: got %d (entries %+v)", deposit, entries)
	}
	if usage != -1_200 {
		t.Errorf("the debit must render NEGATIVE: got %d (entries %+v)", usage, entries)
	}
}

// seedWallet posts one grant and one metered debit against ONE wallet inside org —
// seedBooks addressed to a named subject instead of the org pool.
func seedWallet(t *testing.T, fin finance.Client, org, subject string, grant, spend int64, model string) {
	t.Helper()
	ctx := context.Background()
	if _, err := fin.Deposit(ctx, types.DepositInput{
		Org: org, Subject: subject, Amount: money.FromCents(grant), Currency: "usd",
		Notes: "grant " + subject, Ref: "grant-" + subject,
	}); err != nil {
		t.Fatalf("deposit %s: %v", subject, err)
	}
	if err := fin.RecordUsage(ctx, types.UsageInput{
		Org: org, Subject: subject, Amount: money.FromCents(spend), Currency: "usd",
		Model: model, Ref: "use-" + subject,
	}); err != nil {
		t.Fatalf("record usage %s: %v", subject, err)
	}
}

// TestPeerLedger_EachSubjectReadsItsOwnBooks — the adversarial case, and the one the
// pooled orgs above cannot show.
//
// A tenant org has one wallet, so its ledger file and its wallet are the same set of
// rows and any read of either answers correctly. The SHARED SIGNUP ORG is the shape
// that separates them: every self-serve customer is a distinct billing subject
// (account.Payer resolves them to <org>/<name>) inside ONE org's file, so alice and
// bob deposit, spend and are gated on wallets of their own while their entries sit
// side by side in the same ledger.
//
// The read must answer for the wallet, not the file. It is the same address the
// balance already answers for — /v1/finance/balance totals walletAcct(subject) — so a
// page of movements drawn from the file instead described a total the customer does
// not have, made of somebody else's money.
//
// Both directions are asserted: alice must SEE her own two rows (a filter that
// answered nothing would pass a test that only checked for absence) and must not see
// bob's, by amount and by memo.
func TestPeerLedger_EachSubjectReadsItsOwnBooks(t *testing.T) {
	fin := servePeerLedger(t)
	const org = "hanzo" // the shared signup org: strangers, one file, a wallet each
	seedWallet(t, fin, org, org+"/alice", 50_000, 1_200, "zen")
	seedWallet(t, fin, org, org+"/bob", 900_000, 5_000, "gpu")
	app := mountReader(t)

	for _, who := range []struct {
		user    string
		grant   int64
		spend   int64
		theirs  string
		others  string
		notMine int64
	}{
		{user: "alice", grant: 50_000, spend: 1_200, theirs: "grant hanzo/alice", others: "grant hanzo/bob", notMine: 900_000},
		{user: "bob", grant: 900_000, spend: 5_000, theirs: "grant hanzo/bob", others: "grant hanzo/alice", notMine: 50_000},
	} {
		var entries []financeLedgerView
		if err := json.Unmarshal(readAsUser(t, app, "/v1/finance/ledger", org, who.user), &entries); err != nil {
			t.Fatalf("%s decode ledger: %v", who.user, err)
		}
		if len(entries) != 2 {
			t.Fatalf("%s reads %d entries, want their own grant and debit: %+v",
				who.user, len(entries), entries)
		}
		var in, out int64
		for _, e := range entries {
			if e.Description == who.others {
				t.Errorf("%s reads %q — the ledger answered from the FILE, not the wallet",
					who.user, e.Description)
			}
			if e.Cents == who.notMine || e.Cents == -who.notMine {
				t.Errorf("%s reads %d¢, which is not their money", who.user, e.Cents)
			}
			if e.Cents > 0 {
				in += e.Cents
			} else {
				out += e.Cents
			}
		}
		if in != who.grant {
			t.Errorf("%s's grant reads %d¢, want %d¢", who.user, in, who.grant)
		}
		if out != -who.spend {
			t.Errorf("%s's spend reads %d¢, want %d¢", who.user, out, -who.spend)
		}
		if len(entries) > 0 && entries[0].Description != who.theirs && entries[1].Description != who.theirs {
			t.Errorf("%s cannot find their own grant in %+v", who.user, entries)
		}
	}
}

// TestPeerLedger_CreditsAndUsageFollowTheLedger — credits, usage and the ledger page
// are three projections of ONE list, so they scope together or they contradict each
// other. alice's credits page must show her grant alone and her usage total must be
// her spend alone, off the same read the case above asserts.
func TestPeerLedger_CreditsAndUsageFollowTheLedger(t *testing.T) {
	fin := servePeerLedger(t)
	const org = "hanzo"
	seedWallet(t, fin, org, org+"/alice", 50_000, 1_200, "zen")
	seedWallet(t, fin, org, org+"/bob", 900_000, 5_000, "gpu")
	app := mountReader(t)

	var credits []financeCreditView
	if err := json.Unmarshal(readAsUser(t, app, "/v1/finance/credits", org, "alice"), &credits); err != nil {
		t.Fatalf("decode credits: %v", err)
	}
	if len(credits) != 1 || credits[0].Cents != 50_000 {
		t.Errorf("alice's credits page: want her one 50000¢ grant, got %+v", credits)
	}

	var usage struct {
		TotalCents int64 `json:"totalCents"`
	}
	if err := json.Unmarshal(readAsUser(t, app, "/v1/finance/usage", org, "alice"), &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	if usage.TotalCents != 1_200 {
		t.Errorf("alice's usage total: want her own 1200¢, got %d", usage.TotalCents)
	}
}

// The two response shapes this file reads. They mirror the customer contract in
// apps/billing (financeCredit / financeLedgerEntry), which is unexported there; only
// the fields asserted on are named.
type financeCreditView struct {
	ID    string `json:"id"`
	Cents int64  `json:"cents"`
}

type financeLedgerView struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	Description string `json:"description"`
	Cents       int64  `json:"cents"`
}
