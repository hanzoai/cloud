// Package books is the revenue BOOKS spine: a native double-entry ledger that records
// hanzo.ai's real prepaid-credit revenue on per-org Base/SQLite, exposed at /v1/books.
//
// WHY THIS EXISTS. commerce holds the money (a prepaid wallet: deposits + withdraws);
// finance.go PROJECTS that wallet for the customer UI. Neither keeps BOOKS — a
// double-entry general ledger with a chart of accounts, revenue recognition, and a trial
// balance that proves the books balance. This domain ports ERPNext's Accounts SEMANTICS
// (process_gl_map: merge → toggle → round-off → the debit==credit invariant) to Go, with
// ZERO of its Python/Postgres, and books commerce's transactions into it.
//
// THE ONE POSTING SOURCE. commerce GET /v1/billing/transactions is the SOLE source
// (ingest.go). This domain is READ-ONLY against commerce — it never mints a deposit,
// credit, or payout. It only READS money that already moved and writes the accounting
// twin. So the books can restate but never create money.
//
// TENANT ISOLATION. Every read resolves the caller's OWN org from the validated
// principal (principal.Org — the gateway-minted X-Org-Id, HIP-0026), and each org's
// books live in a physically separate {DataDir}/orgs/{slug}/books.db (sandbox →
// books-sandbox.db). One org can never read another's ledger.
package books

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// state is the subsystem's own data: the per-org book stores (live + sandbox, physically
// separate so a X-Hanzo-Test row can never pollute real revenue), the commerce posting
// source, and the logger.
type state struct {
	live    *cloud.OrgStore[*store]
	sandbox *cloud.OrgStore[*store]
	source  txnSource
	cost    costSource
	// ai + model are the NARRATION-ONLY seam for the AI Ask brain (ask.go): a chat client
	// that rephrases a deterministic, already-computed answer more naturally. It NEVER
	// writes to the books and never sources a figure — the numbers come from metrics.go.
	// nil ai ⇒ the Ask brain returns its templated (already-correct) answer.
	ai    cloud.AIClient
	model string
	// kms is the ONLY home for bank credentials: Plaid/Teller access_tokens are
	// stored + fetched through it (bank engine), NEVER persisted in books.db. nil on
	// a deployment without KMS wired — every credentialed bank op then fails closed.
	kms cloud.KMSClient
	log luxlog.Logger
}

// mounted is the process-wide handle the in-process ingestion seam reaches the stores
// through — the same pattern experiments/flags expose.
var mounted *state

// Mount opens the per-org book stores, wires the commerce posting source, and registers
// the /v1/books surface.
func Mount(app *zip.App, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("books.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("books.Mount: empty deps.DataDir")
	}
	b := cloud.NewBase(deps, "books")
	src := newCommerceReader(commerceinproc.BaseURL(os.Getenv("CLOUD_COMMERCE_HTTP_URL")), os.Getenv("COMMERCE_SERVICE_TOKEN"))
	mounted = &state{
		live:    cloud.NewOrgStore[*store](deps.DataDir, "books", openStore, cloud.WithDurable(b.Durable), cloud.WithStoreLogger(b.Log)),
		sandbox: cloud.NewOrgStore[*store](deps.DataDir, "books-sandbox", openStore, cloud.WithDurable(b.Durable), cloud.WithStoreLogger(b.Log)),
		source:  src,
		cost:    noCost{}, // revenue-only until the cloud_usage cost projection is wired (see costSource)
		ai:      deps.AI,
		model:   strings.TrimSpace(deps.AIDefaultModel),
		kms:     deps.KMS,
		log:     b.Log,
	}
	svc := &cloud.Service[*state]{Base: b, State: mounted}
	routes(app, svc)
	b.Log.Info("books mounted", "prefix", "/v1/books", "commerce", src.configured())
	return nil
}

// Shutdown closes every open per-org book store (live + sandbox).
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	var first error
	if mounted.live != nil {
		if err := mounted.live.CloseAll(); err != nil {
			first = err
		}
	}
	if mounted.sandbox != nil {
		if err := mounted.sandbox.CloseAll(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func routes(app *zip.App, s *cloud.Service[*state]) {
	app.Get("/v1/books/accounts", cloud.Handle(s, accountsHandler))
	app.Get("/v1/books/gl", cloud.Handle(s, glHandler))
	app.Get("/v1/books/trial-balance", cloud.Handle(s, trialBalanceHandler))
	app.Get("/v1/books/metrics", cloud.Handle(s, metricsHandler))
	app.Get("/v1/books/pnl", cloud.Handle(s, pnlHandler))
	app.Get("/v1/books/balance-sheet", cloud.Handle(s, balanceSheetHandler))
	app.Get("/v1/books/export", cloud.Handle(s, exportHandler))
	// The AI Ask brain: a plain-language question answered with REAL figures computed from
	// the books (ask.go), and the clarifying-questions detector over unusual transactions
	// (anomalies.go). Both are strictly read-only over the ledger.
	app.Post("/v1/books/ask", cloud.Handle(s, askHandler))
	app.Get("/v1/books/questions", cloud.Handle(s, questionsHandler))
	// The customer-triggered ingestion of the caller's OWN org: reads commerce's
	// transactions and posts the accounting twin. Idempotent, so a repeat is safe.
	app.Post("/v1/books/sync", cloud.Handle(s, syncHandler))
	// The shared BANK engine surface (bank_api.go): OFX/CSV import, connector sync,
	// transaction + unreconciled reads, and the Plaid/Teller link plumbing stubs.
	bankRoutes(app, s)
	// The SCANNER suite (scan.go): receipt/invoice extraction → a reviewed draft → the
	// one Post(); the inbox queue, vendor + rule auto-categorization, and the unified
	// filterable transactions read over the booked ledger.
	scannerRoutes(app, s)
}

// storeFor resolves the caller's OWN book store for the requested ledger (live/sandbox).
func (s *state) storeFor(org string, sandbox bool) (*store, error) {
	if sandbox {
		return s.sandbox.For(org, "")
	}
	return s.live.For(org, "")
}

// syncLedger ingests one org's commerce transactions into one ledger, then ships the
// store to its durable object (best-effort) so the postings survive a rolling deploy.
func (s *state) syncLedger(ctx context.Context, org string, sandbox bool) (int, error) {
	st, err := s.storeFor(org, sandbox)
	if err != nil {
		return 0, err
	}
	posted, err := ingestOrg(ctx, s.source, s.cost, st, org, sandbox)
	if err != nil {
		return posted, err
	}
	if posted > 0 {
		store := s.live
		if sandbox {
			store = s.sandbox
		}
		// acked==false means this pod was deposed mid-request and the fence REFUSED the
		// ship: the postings are committed only to a local file a successor's hydrate will
		// overwrite. Report it — this ingest is idempotent, so a re-run recovers, but it
		// must never look like a clean success. (Since the durable plane became the default
		// this is a live path, not a no-op.)
		if acked, serr := store.Sync(org, ""); serr != nil {
			s.log.Warn("books durable sync degraded", "org", org, "sandbox", sandbox, "err", serr)
		} else if !acked {
			s.log.Error("books postings NOT durably shipped — this pod is not the org's elected writer; the postings will be dropped on the next hydrate, re-run the sync",
				"org", org, "sandbox", sandbox, "posted", posted)
		}
	}
	return posted, nil
}

// ── commerce posting source (S2S read of /v1/billing/transactions) ──

// commerceReader reads commerce's per-org transaction ledger with the admin-scoped
// COMMERCE_SERVICE_TOKEN, scoping every read to ONE org via X-Org-Id (the S2S selector
// commerce's EdgeAuth honors after it verifies the service token) and routing sandbox
// reads to commerce's TEST ledger with X-Hanzo-Test:true. It is the SAME S2S machinery
// billing's commerceProxy uses — kept read-only (transactions only, never a mint).
type commerceReader struct {
	base  string
	token string
	http  *http.Client
}

func newCommerceReader(base, token string) *commerceReader {
	return &commerceReader{
		base:  strings.TrimRight(strings.TrimSpace(base), "/"),
		token: strings.TrimSpace(token),
		http:  commerceinproc.Client(15 * time.Second),
	}
}

func (r *commerceReader) configured() bool { return r != nil && r.base != "" && r.token != "" }

// transactions reads the org's commerce ledger (live or the X-Hanzo-Test sandbox),
// tolerating both the wrapped {transactions:[…]} shape and a bare array. An unconfigured
// reader returns no rows (an unconfigured deployment ingests nothing rather than erroring).
func (r *commerceReader) transactions(ctx context.Context, org string, sandbox bool) ([]commerceTxn, error) {
	if !r.configured() {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/v1/billing/transactions?limit=2000", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("X-Org-Id", org)
	if sandbox {
		req.Header.Set("X-Hanzo-Test", "true")
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("commerce unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("commerce transactions status %d", resp.StatusCode)
	}
	var wrap struct {
		Transactions []commerceTxn `json:"transactions"`
	}
	if json.Unmarshal(body, &wrap) == nil && wrap.Transactions != nil {
		return wrap.Transactions, nil
	}
	var rows []commerceTxn
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("commerce transactions decode: %w", err)
	}
	return rows, nil
}

// sandboxQuery reads the ?sandbox=true toggle a read handler uses to select the ledger.
func sandboxQuery(c *zip.Ctx) bool {
	return strings.EqualFold(strings.TrimSpace(c.Query("sandbox")), "true")
}
