// Package books is double-entry accounting: chart of accounts, ledger, bank
// reconciliation, and the reports that prove the books balance.
//
// It serves /v1/books: a fixed chart of accounts, an append-only general ledger,
// bank feeds with reconciliation, receipt scanning, and the trial / P&L /
// position reports.
//
// WHY THIS EXISTS. finance holds the money (a prepaid wallet: deposits + usage debits);
// billing PROJECTS that wallet for the customer UI. Neither keeps BOOKS — a general
// ledger with a chart of accounts, revenue recognition, and a trial balance. This domain
// ports ERPNext's Accounts SEMANTICS (process_gl_map: merge → toggle → round-off → the
// debit==credit invariant) to Go, with ZERO of its Python/Postgres.
//
// IT RECORDS MONEY, IT NEVER MOVES IT. Three sources post: commerce
// GET /v1/billing/transactions (ingest.go), a read-only bank connector (bank.go), and a
// reviewed receipt scan (scan.go). Every one of them lands through the SAME post() choke
// point, and none of them can mint a deposit, credit, or payout — this domain only READS
// money that already moved and writes the accounting twin, so the books can restate but
// never create money.
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
	"github.com/hanzoai/cloud/apps/commerce/transport"
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
func Mount(app cloud.Router, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("books.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("books.Mount: empty deps.DataDir")
	}
	b := cloud.NewBase(deps, "books")
	src := newCommerceReader(transport.BaseURL(os.Getenv("CLOUD_COMMERCE_HTTP_URL")), os.Getenv("COMMERCE_SERVICE_TOKEN"))
	mounted = &state{
		live:    cloud.NewOrgStore[*store](b, "books", openStore),
		sandbox: cloud.NewOrgStore[*store](b, "books-sandbox", openStore),
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

func routes(app cloud.Router, s *cloud.Service[*state]) {
	g := app.Group("/v1/books")
	// noStore carries the Cache-Control header every books answer has always sent.
	// It precedes the leaves because fiber runs middleware in registration order.
	// cloud.Bridge is not installed here: the composer owns it — the fused host
	// installs it once at its root, and the plugin constructor does the same for a
	// plugin program — and typed ops read the validated org it parks on the
	// context. See typed.go.
	g.Use(noStore())

	// TYPED ops, declared on the group: the prefix is part of each op's path and
	// therefore of every projection — the document, the MCP tool, the CLI command,
	// the SDK method — so this one registration is the whole contract.
	o := booksOps{s: s}
	zip.Get(g, "/accounts", o.listAccounts)
	zip.Get(g, "/gl", o.listGL)
	zip.Get(g, "/trial", o.trialBalance)
	zip.Get(g, "/pnl", o.profitAndLoss)
	zip.Get(g, "/position", o.balanceSheet)
	zip.Get(g, "/export", o.exportPackage)
	zip.Get(g, "/questions", o.listQuestions)
	// The metrics read went typed the day its Out stopped embedding Metrics:
	// MetricsResponse now spells the snapshot fields out flat (metrics.go), because
	// zip's schema walk publishes an embedded struct as a NESTED property while
	// encoding/json flattens it — the copy is pinned complete by
	// TestMetricsResponseCarriesEveryMetricsField, so it cannot silently drift.
	zip.Get(g, "/metrics", o.metrics)
	// The customer-triggered ingestion of the caller's OWN org: reads commerce's
	// transactions and posts the accounting twin. Idempotent, so a repeat is safe.
	zip.Post(g, "/sync", o.sync)
	// The AI Ask brain: a plain-language question answered from the org's real
	// figures. Its body IS its In; the ?sandbox selector stays on the URL (query,
	// typed.go), so typing described the route without moving it.
	zip.Post(g, "/ask", o.ask)

	// The shared BANK engine surface (bank_api.go): OFX/CSV import, connector sync,
	// transaction + unreconciled reads, and the Plaid/Teller link plumbing stubs.
	bankRoutes(app, s)
	// The SCANNER suite (scan.go): receipt/invoice extraction → a reviewed draft → the
	// one Post(); the inbox queue, vendor + rule auto-categorization, and the unified
	// filterable transactions read over the booked ledger.
	scannerRoutes(app, s)
}

// ledger picks the registry for the requested book (live or sandbox). The two
// are separate SUBSYSTEMS — separate files under the same org — so the choice is
// which registry to ask, never a different name to ask it for.
func (s *state) ledger(sandbox bool) *cloud.OrgStore[*store] {
	if sandbox {
		return s.sandbox
	}
	return s.live
}

// storeFor is the ONE way this package reaches a book store: it names the
// database through cloud.OrgNamespace — the single door a validated org walks
// through — and asks the chosen registry for that name.
func (s *state) storeFor(org string, sandbox bool) (*store, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.ledger(sandbox).For(ns)
}

// shipLedger is the ship-before-ack step: it names the same database the write
// went to and ships THAT one, so a write and its ship can never address
// different files.
func (s *state) shipLedger(org string, sandbox bool) (acked bool, err error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return false, err
	}
	return s.ledger(sandbox).Sync(ns)
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
		if _, serr := s.shipLedger(org, sandbox); serr != nil {
			s.log.Warn("books durable sync degraded", "org", org, "sandbox", sandbox, "err", serr)
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
		http:  transport.Client(15 * time.Second),
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

// sandboxQuery reads the ?sandbox=true toggle a handler still untyped uses to select the
// ledger. It is sandboxOf (typed.go) read off a request — one rule, two readers, so the
// typed ops and the raw handlers beside them can never disagree about which books answer.
func sandboxQuery(c *zip.Ctx) bool { return sandboxOf(c.Query("sandbox")) }
