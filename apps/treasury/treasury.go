// Package treasury mounts the Hanzo Cloud /v1/finance/* surface: the platform's
// OWN fund/reserve accounting, one layer ABOVE the per-org commerce credit ledger.
// Where commerce tracks what each CUSTOMER holds and spends, treasury tracks the
// PLATFORM's books — a real, backed reserve fund that stands behind the growth-loop
// payouts (referrals, affiliates, OSS authors) so a payout is a debit against funded
// capital, never unbounded minting.
//
// It is the cloud-facing adapter around the ledger-of-record PORT (ledger.Backend):
// this file owns HTTP, tenant scoping, audit and the KMS-signed L1 anchor + the Hanzo
// policy/fund/payout logic; the backend owns the double-entry. Two backends satisfy
// the port — the native Base/SQLite engine (clients/treasury/ledger, offline/default)
// and the Formance adapter (clients/treasury/formance, the Postgres-backed ledger of
// record when FORMANCE_LEDGER_URL is wired). Selecting one is a config flip.
//
// Storage tiers (OLTP → OLAP): the authoritative double-entry is the ledger-of-record
// backend (single-writer, overdraw-guarded — reserve/revenue/house on the house
// tenant). Cross-tenant GLOBAL analytics is the datastore OLAP projection in the
// shared hanzoai/datastore, fed by the SAME event stream o11y already emits — treasury
// money-actions mirror there via cloud's audit datastore mirror, so there is NO second
// metering pipeline. datastore is NEVER the ledger of record; single-tenant drill-down
// reads the authoritative ledger, cross-tenant aggregates read the projection.
//
// ONE scope-aware /v1/finance/* engine, three tenancy surfaces — the tenant is derived
// from the validated IAM identity, house/reserve is locked to SuperAdmin, and a
// per-org caller only ever sees its own tenant:
//
//	GET  /v1/finance/treasury          (org)          reserve health + policy (the pool backing MY payouts)
//	GET  /v1/finance/accounts          (org)          MY ledger accounts (admin: ?scope=house | ?org=<t>)
//	GET  /v1/admin/treasury            (SuperAdmin) full report + journal + anchor status
//	POST /v1/admin/treasury/policy     (SuperAdmin) set the revenue-share %
//	POST /v1/admin/treasury/sweep      (SuperAdmin) accrue the revenue-share into the fund for a period
//	POST /v1/admin/treasury/seed       (SuperAdmin) inject bootstrap capital into the fund
//	POST /v1/admin/treasury/anchor     (SuperAdmin) anchor the ledger root on Hanzo L1
//
// The three surfaces (admin.hanzo.ai SuperAdmin, console.hanzo.ai per-org customer,
// finance.hanzo.ai per-org operator) are the SAME engine projected by IAM scope. A
// separate frontend agent builds the console + finance surfaces against this contract.
// The reserve-fund admin board is the `treasury` admin head (distinct from the
// existing /v1/admin/finance COGS/margin god-view in clients/admin — they compose,
// never collide).
//
// serve.go auto-registers GET /v1/finance/health.
package treasury

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/treasury/formance"
	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/apps/treasury/ledger/sqlstore"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/money"
	"github.com/zap-proto/zip"
)

// Program identifiers for backed payouts — the ONE place the growth loops name
// themselves to the treasury, so a payout sink account id is never re-spelled.
const (
	ProgramReferral  = "referral"
	ProgramAffiliate = "affiliate"
	ProgramAuthor    = "author"
)

const (
	// defaultJournalLimit / maxJournalLimit bound the admin journal read.
	defaultJournalLimit = 200
	maxJournalLimit     = 1000
)

// state is treasury's own data; shared deps live in the embedded cloud.Base.
type state struct {
	store      *sqlstore.Store // native store: policy config always, journal when native backend
	record     ledger.Backend  // the ledger of record — native (default) or Formance
	auditStore *audit.Recorder // best-effort debit/policy audit; nil disables it
	anchor     *anchorer       // Hanzo L1 anchor (Phase 2); nil-safe
}

// mounted is the process singleton the Reserve helper resolves. Set at Mount; nil
// when the subsystem is not linked/enabled, which makes Reserve a passthrough.
var mounted *cloud.Service[state]

// Mount wires the treasury surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("treasury.Mount: nil app")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("treasury.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "treasury")
	if deps.DataDir == "" {
		return fmt.Errorf("treasury.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("treasury.Mount: data dir: %w", err)
	}
	store, err := sqlstore.Open(filepath.Join(deps.DataDir, "treasury.db"))
	if err != nil {
		return fmt.Errorf("treasury.Mount: open store: %w", err)
	}
	// Select the ledger of RECORD. Formance (Postgres-backed, the production ledger)
	// when FORMANCE_LEDGER_URL is wired; else the native Base/SQLite engine (the
	// offline/default, so the reserve fund works today). The Hanzo revenue-share
	// policy is held in the native store either way (it is config, not accounting).
	var record ledger.Backend
	if base := strings.TrimSpace(os.Getenv("FORMANCE_LEDGER_URL")); base != "" {
		record = formance.New(base, os.Getenv("FORMANCE_LEDGER_NAME"), os.Getenv("FORMANCE_LEDGER_TOKEN"), store)
		log.Info("treasury ledger of record: formance", "url", base)
	} else {
		record = ledger.New(store)
		log.Info("treasury ledger of record: native (Base/SQLite) — set FORMANCE_LEDGER_URL for Formance")
	}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(deps, "treasury"),
		State: state{
			store:      store,
			record:     record,
			auditStore: deps.Audit,
			anchor:     newAnchorer(deps, log),
		},
	}
	mounted = s
	// The reserve, published on the internal plane as a typed op (plane.go).
	// admin's money board reads it here — as the SuperAdmin who asked, re-checked
	// on THIS side — instead of importing this package, which in admin's own
	// binary could only ever return zero.
	zip.Post(cloud.Plane(), "/treasury/reserve", planeReserve,
		zip.WithOperationID(plane.TreasuryReserve),
		zip.WithSummary("Reserve fund balance"))

	// cloud.Bridge carries into a typed op the request its signature drops — this
	// surface reads the SuperAdmin bit, the caller's org and two query parameters
	// off it. On the scoped Router it installs once per DECLARED prefix
	// (/v1/admin/treasury, /v1/finance/accounts, /v1/finance/treasury) and nowhere
	// else, and it must precede the leaves below: fiber runs middleware in
	// registration order. Serve installs one app-wide too; nesting is harmless.
	app.Use(cloud.Bridge())
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		log.Error("treasury: router exposes no op registry; the money surface would serve routes no projection knows")
		return nil
	}
	o := ops{s: s}
	// ONE scope-aware /v1/finance/* engine, three tenancy surfaces (HIP finance):
	// per-org reads derive the tenant from the validated IAM identity and see ONLY
	// their own accounts; the reserve fund + revenue-share + house mutations are
	// locked to SuperAdmin under /v1/admin/treasury* (the console admin-proxy
	// convention, enveloped).
	//
	// Every route is a TYPED op: ONE registry entry that is at once the REST
	// route, the OpenAPI operation with its schemas, the MCP tool, the CLI command
	// and the generated SDK method. Declared on the App with WHOLE paths, because
	// this subsystem owns three unrelated nouns and no single prefix.
	zip.Get(zapp, "/v1/finance/treasury", o.myTreasury)              // per-org: reserve transparency + policy
	zip.Get(zapp, "/v1/finance/accounts", o.myAccounts)              // per-org: own ledger accounts (admin: ?org=/?scope=house)
	zip.Get(zapp, "/v1/admin/treasury", o.adminReport)               // SuperAdmin: report + journal + anchor
	zip.Post(zapp, "/v1/admin/treasury/policy", o.adminSetPolicy)    // SuperAdmin: set revenue-share %
	zip.Post(zapp, "/v1/admin/treasury/sweep", o.adminSweep)         // SuperAdmin: accrue revenue-share
	zip.Post(zapp, "/v1/admin/treasury/seed", o.adminSeed)           // SuperAdmin: inject reserve capital
	zip.Post(zapp, "/v1/admin/treasury/anchor", o.adminAnchor)       // SuperAdmin: anchor ledger root on Hanzo L1
	zip.Post(zapp, "/v1/admin/treasury/bind-anchor", o.adminBindAnchor) // SuperAdmin: bind the reserve MPC wallet as the anchor signer

	log.Info("treasury mounted", "brand", deps.Brand, "ledgerOfRecord", record.Name(), "anchor", s.State.anchor.configured())
	return nil
}

// ── the backed-payout seam (the ONE helper the 3 growth loops call) ──────────

// Reserve backs a payout of amountCents (minor units) for `program` against the
// platform reserve fund: it posts the double-entry fund→payout:<program> journal
// entry, idempotently keyed by ref, and reports whether the fund could cover it.
//
//   - backed=true  → posted (or already posted for this ref); the caller MUST now
//     credit the recipient wallet. Fund down, wallet up — reconciled.
//   - backed=false → INSUFFICIENT RESERVE; the caller MUST NOT credit. The payout is
//     honestly pending/blocked until a sweep or seed replenishes the fund.
//
// When the treasury subsystem is NOT mounted (a partial deploy, or a growth-loop
// unit test that does not wire treasury) Reserve is a PASSTHROUGH returning
// backed=true — behaviour identical to before treasury existed, the same
// degrade-gracefully contract the commerce seam uses. In production the subsystem is
// always mounted, so the reserve is enforced. entryID is the journal entry id (empty
// on passthrough), for the caller to record alongside its own payout row.
func Reserve(ctx context.Context, program, ref, memo string, amountCents int64) (backed bool, entryID string, err error) {
	s := mounted
	if s == nil {
		return true, "", nil // unmounted → passthrough (backward-safe)
	}
	entry, backed, created, err := s.State.record.DebitReserve(ctx, program, ref, memo, amountCents, time.Now().Unix())
	if err != nil {
		return false, "", err
	}
	if backed && created {
		emitAudit(s, ctx, "treasury.debit", program, entry.ID, map[string]any{
			"program": program, "ref": ref, "amountCents": amountCents, "entryId": entry.ID,
		})
	}
	return backed, entry.ID, nil
}

// ReserveCents reports the reserve fund's currently available balance (minor units)
// and whether the treasury subsystem is mounted. The growth loops use it only to
// render an honest "X cents available" message when a payout is blocked for lack of
// reserve — never as an authority (the atomic guard in DebitReserve is the
// authority). Unmounted → (0, false).
func ReserveCents(ctx context.Context) (int64, bool) {
	s := mounted
	if s == nil {
		return 0, false
	}
	bal, err := s.State.record.ReserveCents(ctx)
	if err != nil {
		return 0, true
	}
	return bal, true
}

// Credit is the INBOUND mirror of Reserve: it credits amountCents into the platform
// reserve fund (revenue:platform → fund:reserve), idempotently keyed by ref. It is
// the "pay ourselves" seam — when a growth loop's royalty is owed to HANZO itself
// (a Hanzo-maintained OSS template deployed by another org), the creator share is
// realized into the treasury reserve instead of paid out to an external wallet.
//
// It reuses the ledger-of-record's Seed primitive (a fixed-amount reserve credit,
// distinct KindSeed, idempotent by ref) — NOT a new ledger. Idempotency by ref makes
// a retry a no-op: the reserve is credited AT MOST ONCE per ref, the mirror of the
// loops' at-most-once accrual latch.
//
//   - credited=true  → posted (or already posted for this ref). entryID is the
//     journal entry id (empty on passthrough), for the caller to record.
//   - credited=false → only on an unexpected ledger error (err set); the caller
//     leaves its own reservation intact and reconciles.
//
// When treasury is NOT mounted (a partial deploy or a growth-loop unit test that does
// not wire treasury) Credit is a PASSTHROUGH returning credited=true — the same
// degrade-gracefully contract Reserve uses.
func Credit(ctx context.Context, program, ref, memo string, amountCents int64) (credited bool, entryID string, err error) {
	s := mounted
	if s == nil {
		return true, "", nil // unmounted → passthrough (backward-safe)
	}
	entry, created, err := s.State.record.Seed(ctx, ref, memo, amountCents, time.Now().Unix())
	if err != nil {
		return false, "", err
	}
	if created {
		emitAudit(s, ctx, "treasury.credit", program, entry.ID, map[string]any{
			"program": program, "ref": ref, "amountCents": amountCents, "entryId": entry.ID,
		})
	}
	return true, entry.ID, nil
}

// ── typed ops ────────────────────────────────────────────────────────────────

// zipdoc lifts the doc comment off each typed op — and off each field of its In
// and Out — into zipdoc_gen.go, which hands them to zip.Describe at init. Go
// drops comments at compile time, so this build-time pass is the ONLY way that
// prose reaches the published document, the MCP tool list and the generated SDKs.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops carries the subsystem's state onto every typed op. A typed handler takes a
// context and its decoded In and nothing else, so the state rides on the receiver.
type ops struct{ s *cloud.Service[state] }

// TreasuryReserve returns the reserve fund's available balance — the pool that
// backs every referral, affiliate and author payout. SuperAdmin only, re-checked
// on this side against the principal that asked.
//
// (A named function, not the closure it replaced: zipdoc harvests the doc comment
// of the HANDLER, and a function literal has none, so this op used to register
// with an empty description on every projection that reads one.)
func planeReserve(ctx context.Context, _ *struct{}) (*plane.Reserved, error) {
	if !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	cents, ok := ReserveCents(ctx)
	if !ok {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "treasury store not open")
	}
	return &plane.Reserved{Amount: plane.Amount(money.FromUSD(cents))}, nil
}

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire. ONE of these for the whole package.
type noInput struct{}

// admin is the request the SuperAdmin surface runs on. A typed op receives a
// context and nothing else, so the platform-sudo bit and the caller's org — both
// REQUEST facts, never In fields — cross on the request cloud.Bridge parked. Off
// the HTTP path there is no request, and the honest answer is a refusal rather
// than an invented identity.
func admin(ctx context.Context) (*zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok || !c.IsAdmin() {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	return c, nil
}

// ── customer surface ─────────────────────────────────────────────────────────

// GetTreasury returns the reserve fund's health and the current revenue-share
// policy for any validated caller. It is a TRANSPARENCY view — a partner or
// author can see that the pool backing their payouts is solvent — and NOT per-org
// money, which is the customer's own commerce balance at /v1/billing/balance. The
// policy is read-only here; only a SuperAdmin sets it.
func (o ops) myTreasury(ctx context.Context, _ *noInput) (*ledger.TreasuryReport, error) {
	if _, ok := principal.OrgFrom(ctx); !ok {
		return nil, zip.ErrForbidden("sign in to view the treasury")
	}
	rep, err := o.s.State.record.Snapshot(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "treasury snapshot: %v", err)
	}
	return &rep, nil
}

// accountView is one row of the scope-aware accounts read.
type accountView struct {
	// Address is the ledger account address ("org:acme:wallet", "fund:reserve", …).
	Address string `json:"address"`
	// BalanceCents is that account's signed balance in minor units.
	BalanceCents int64 `json:"balanceCents"`
}

// accountsIn is the scope selection for the accounts read. Both fields are query
// parameters and BOTH are honoured only for a SuperAdmin — for anyone else the
// tenant comes from the validated principal and these are ignored, which is what
// makes an In field safe here: it can only ever widen a scope the request has
// already proved it may widen.
type accountsIn struct {
	// Scope is "house" to read the reserve/revenue/payout house accounts. SuperAdmin only.
	Scope string `json:"scope"`
	// Org names another tenant to read. SuperAdmin only; ignored when scope=house.
	Org string `json:"org"`
}

// accountsOut is the scope-aware accounts answer.
type accountsOut struct {
	// Scope is the scope actually served: "org" or "house".
	Scope string `json:"scope"`
	// Tenant is the org whose accounts these are (empty for the house scope's own rows).
	Tenant string `json:"tenant"`
	// Accounts are the ledger accounts in scope with their balances.
	Accounts []accountView `json:"accounts"`
}

// ListFinanceAccounts returns the ledger accounts the caller may see, with their
// balances. It is tenant-isolated SERVER-SIDE: an ordinary caller sees ONLY
// accounts under its own "org:<tenant>:" prefix, never house accounts and never
// another tenant's. A SuperAdmin may widen with ?scope=house (the reserve,
// revenue and payout house accounts) or ?org=<tenant> — the only way to cross the
// tenant boundary, and only for platform sudo. The answer is honestly empty until
// a tenant has ledger postings.
func (o ops) myAccounts(ctx context.Context, in *accountsIn) (*accountsOut, error) {
	c, hasReq := cloud.Request(ctx)
	tenant, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view accounts")
	}
	prefix := "org:" + tenant + ":"
	scope := "org"
	if hasReq && c.IsAdmin() {
		switch strings.TrimSpace(in.Scope) {
		case "house":
			prefix, scope = "", "house" // all accounts; the report separates house from tenant
		default:
			if org := strings.TrimSpace(in.Org); org != "" {
				prefix, scope, tenant = "org:"+org+":", "org", org
			}
		}
	}
	balances, err := o.s.State.record.AccountsWithPrefix(ctx, prefix)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "accounts: %v", err)
	}
	accounts := make([]accountView, 0, len(balances))
	for addr, bal := range balances {
		// house scope: exclude per-tenant accounts so the admin house view stays house-only.
		if scope == "house" && strings.HasPrefix(addr, "org:") {
			continue
		}
		accounts = append(accounts, accountView{Address: addr, BalanceCents: bal})
	}
	return &accountsOut{Scope: scope, Tenant: tenant, Accounts: accounts}, nil
}

// ── admin surface (SuperAdmin, fail-closed) ────────────────────────────────
//
// Every admin op answers the { status, msg, data } envelope the console's admin
// proxy unwraps (cloud.OK — identical to clients/admin and clients/referrals), so
// each Out spells that envelope out around its own data. The customer
// /v1/finance surface stays bare JSON, read through the /cloud proxy.

// journalIn bounds one page of the double-entry journal.
type journalIn struct {
	// Limit caps the journal entries returned. Out of range or unparseable takes the default.
	Limit int `json:"limit"`
}

// adminReportData is the admin treasury board: fund, journal and anchor together.
type adminReportData struct {
	// Report is the reserve-fund snapshot — available, accrued, paid, per-program, policy.
	Report ledger.TreasuryReport `json:"report"`
	// Journal is the recent double-entry entries, newest first.
	Journal []ledger.JournalEntry `json:"journal"`
	// Anchor is the Hanzo L1 anchoring status of the ledger root.
	Anchor anchorStatus `json:"anchor"`
}

// adminReportOut is adminReportData in the admin envelope.
type adminReportOut struct {
	// Status is "ok" on success; the transport maps a non-ok envelope to an error.
	Status string `json:"status"`
	// Msg carries an operator-facing note; empty on success.
	Msg string `json:"msg"`
	// Data is the treasury board.
	Data adminReportData `json:"data"`
}

// GetAdminTreasury returns the whole treasury board for a SuperAdmin: the reserve
// fund report, the recent double-entry journal, and the Hanzo L1 anchor status of
// the ledger root. ?limit= bounds the journal page.
func (o ops) adminReport(ctx context.Context, in *journalIn) (*adminReportOut, error) {
	if _, err := admin(ctx); err != nil {
		return nil, err
	}
	rep, err := o.s.State.record.Snapshot(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "treasury snapshot: %v", err)
	}
	entries, err := o.s.State.record.Entries(ctx, journalLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "journal: %v", err)
	}
	return &adminReportOut{Status: "ok", Data: adminReportData{
		Report:  rep,
		Journal: entries,
		Anchor:  o.s.State.anchor.status(ctx, o.s.State.record),
	}}, nil
}

// policyRequest is the POST /v1/admin/treasury/policy body.
type policyRequest struct {
	// RevenueShareBps is the share of net platform revenue a sweep accrues into the
	// reserve fund, in basis points. 0–10000; 2000 (20%) is the platform default.
	RevenueShareBps int64 `json:"revenueShareBps"`
}

// policyData carries the stored policy.
type policyData struct {
	// Policy is the revenue-share configuration as stored.
	Policy ledger.SharePolicy `json:"policy"`
}

// policyOut is policyData in the admin envelope.
type policyOut struct {
	// Status is "ok" on success.
	Status string `json:"status"`
	// Msg carries an operator-facing note; empty on success.
	Msg string `json:"msg"`
	// Data is the stored policy.
	Data policyData `json:"data"`
}

// SetTreasuryPolicy sets the revenue-share basis points a sweep accrues into the
// reserve fund and returns the stored policy. 0–10000; the change is audited.
// SuperAdmin only.
//
// Example: {"revenueShareBps": 2000}
func (o ops) adminSetPolicy(ctx context.Context, in *policyRequest) (*policyOut, error) {
	if _, err := admin(ctx); err != nil {
		return nil, err
	}
	pol, err := o.s.State.record.SetPolicy(ctx, in.RevenueShareBps, time.Now().Unix())
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	emitAudit(o.s, ctx, "treasury.policy", "", "", map[string]any{"revenueShareBps": pol.RevenueShareBps})
	return &policyOut{Status: "ok", Data: policyData{Policy: pol}}, nil
}

// sweepRequest is the POST /v1/admin/treasury/sweep body. RevenueCents is the net
// platform revenue MEASURED for the period (the caller — a cron or an operator —
// supplies it from the revenue view; treasury does the accounting, not the metering,
// keeping the concerns orthogonal). Period defaults to the current UTC month.
type sweepRequest struct {
	// Period is the accrual period as YYYY-MM. Empty takes the current UTC month.
	Period string `json:"period"`
	// RevenueCents is the net platform revenue measured for the period, in minor units. Must be >= 0.
	RevenueCents int64 `json:"revenueCents"`
}

// sweepData is what one accrual did.
type sweepData struct {
	// Period is the period actually accrued.
	Period string `json:"period"`
	// RevenueCents is the revenue the share was computed from.
	RevenueCents int64 `json:"revenueCents"`
	// AccruedCents is the amount moved into the reserve fund.
	AccruedCents int64 `json:"accruedCents"`
	// Created is false when this period had already been swept — the accrual is idempotent.
	Created bool `json:"created"`
	// ReserveCents is the fund balance after the accrual.
	ReserveCents int64 `json:"reserveCents"`
}

// sweepOut is sweepData in the admin envelope.
type sweepOut struct {
	// Status is "ok" on success.
	Status string `json:"status"`
	// Msg carries an operator-facing note; empty on success.
	Msg string `json:"msg"`
	// Data is the accrual result.
	Data sweepData `json:"data"`
}

// SweepTreasury posts the revenue-share accrual for one period — revenue into the
// reserve fund, at the current policy's basis points — and returns what it moved.
// It is idempotent per period: a re-run of a period already swept accrues nothing
// and reports created=false. SuperAdmin only.
//
// Example: {"period": "2026-07", "revenueCents": 100000}
func (o ops) adminSweep(ctx context.Context, in *sweepRequest) (*sweepOut, error) {
	if _, err := admin(ctx); err != nil {
		return nil, err
	}
	period := strings.TrimSpace(in.Period)
	if period == "" {
		period = time.Now().UTC().Format("2006-01")
	}
	if in.RevenueCents < 0 {
		return nil, zip.ErrBadRequest("revenueCents must be >= 0")
	}
	entry, created, err := o.s.State.record.Accrue(ctx, period, in.RevenueCents, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "sweep: %v", err)
	}
	if created {
		emitAudit(o.s, ctx, "treasury.sweep", "", entry.ID, map[string]any{
			"period": period, "revenueCents": in.RevenueCents, "accruedCents": entry.Amount.Cents(),
		})
	}
	reserve, _ := o.s.State.record.ReserveCents(ctx)
	return &sweepOut{Status: "ok", Data: sweepData{
		Period:       period,
		RevenueCents: in.RevenueCents,
		AccruedCents: entry.Amount.Cents(),
		Created:      created,
		ReserveCents: reserve,
	}}, nil
}

// seedRequest is the POST /v1/admin/treasury/seed body — a bootstrap capital
// injection into the reserve fund. Ref (optional) is an idempotency key; without one
// each seed is a distinct injection.
type seedRequest struct {
	// AmountCents is the capital to inject, in minor units. Must be > 0.
	AmountCents int64 `json:"amountCents"`
	// Memo is the operator's note on the entry. Empty takes "reserve capital injection".
	Memo string `json:"memo"`
	// Ref is an idempotency key. Without one each seed is a distinct injection.
	Ref string `json:"ref"`
}

// seedData is what one capital injection did.
type seedData struct {
	// Entry is the journal entry the injection wrote.
	Entry ledger.JournalEntry `json:"entry"`
	// Created is false when this ref had already been seeded — the injection is at-most-once.
	Created bool `json:"created"`
	// ReserveCents is the fund balance after the injection.
	ReserveCents int64 `json:"reserveCents"`
}

// seedOut is seedData in the admin envelope.
type seedOut struct {
	// Status is "ok" on success.
	Status string `json:"status"`
	// Msg carries an operator-facing note; empty on success.
	Msg string `json:"msg"`
	// Data is the injection result.
	Data seedData `json:"data"`
}

// SeedTreasury injects bootstrap capital into the reserve fund so backed payouts
// can begin before the first revenue-share sweep, and returns the journal entry
// it wrote. A repeat of the same ref is at-most-once and reports created=false.
// SuperAdmin only.
//
// Example: {"amountCents": 500000, "memo": "founding capital", "ref": "seed:2026-q3"}
func (o ops) adminSeed(ctx context.Context, in *seedRequest) (*seedOut, error) {
	if _, err := admin(ctx); err != nil {
		return nil, err
	}
	if in.AmountCents <= 0 {
		return nil, zip.ErrBadRequest("amountCents must be > 0")
	}
	ref := strings.TrimSpace(in.Ref)
	if ref == "" {
		ref = "seed:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	memo := strings.TrimSpace(in.Memo)
	if memo == "" {
		memo = "reserve capital injection"
	}
	entry, created, err := o.s.State.record.Seed(ctx, ref, memo, in.AmountCents, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "seed: %v", err)
	}
	if created {
		emitAudit(o.s, ctx, "treasury.seed", "", entry.ID, map[string]any{
			"amountCents": in.AmountCents, "ref": ref, "entryId": entry.ID,
		})
	}
	reserve, _ := o.s.State.record.ReserveCents(ctx)
	return &seedOut{Status: "ok", Data: seedData{Entry: entry, Created: created, ReserveCents: reserve}}, nil
}

// emitAudit records a treasury money action in cloud's tamper-evident trail.
// Best-effort; a nil store is a no-op. The actor is the treasury engine (a system
// action, not a user).
func emitAudit(s *cloud.Service[state], ctx context.Context, action, program, resourceID string, after map[string]any) {
	if s.State.auditStore == nil {
		return
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: program, Sub: "treasury"},
		Action:   action,
		Resource: audit.Resource{Type: "treasury", ID: resourceID},
		Auth:     audit.AuthContext{Method: "service"},
		Outcome:  audit.Outcome{Result: "success", Status: 200},
		After:    audit.Redact(mustJSON(after)),
	}
	if _, err := s.State.auditStore.Append(ctx, rec); err != nil {
		s.Log.Error("treasury: audit emit failed", "action", action, "err", err)
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// journalLimit bounds a requested journal page. A missing, unparseable or
// non-positive value takes the default; anything above the ceiling takes the
// ceiling. (An unparseable ?limit= arrives here as 0, because zip's URL binder
// leaves a field it cannot convert at its zero value — the same answer
// strconv.Atoi's error gave.)
func journalLimit(n int) int {
	if n <= 0 {
		return defaultJournalLimit
	}
	if n > maxJournalLimit {
		return maxJournalLimit
	}
	return n
}

// Shutdown closes the treasury store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
