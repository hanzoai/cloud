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
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Program identifiers for backed payouts — the ONE place the growth loops name
// themselves to the treasury, so a payout sink account id is never re-spelled.
const (
	ProgramReferral  = "referral"
	ProgramAffiliate = "affiliate"
	ProgramAuthor    = "author"
)

const (
	// journalLimit / maxJournalLimit bound the admin journal read.
	journalLimit    = 200
	maxJournalLimit = 1000
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
	// The reserve, published on the internal plane (native ZAP over the unix
	// socket, cloud/rpc.go). admin's money board reads it here — as the
	// SuperAdmin who asked, re-checked on THIS side — instead of importing this
	// package, which in admin's own binary could only ever return zero.
	cloud.Expose("treasury.reserve", func(ctx context.Context, who cloud.Ident, _ []byte) ([]byte, error) {
		if !who.Admin {
			return nil, cloud.Fault(403, "SuperAdmin required")
		}
		cents, ok := ReserveCents(ctx)
		if !ok {
			return nil, cloud.Fault(503, "treasury store not open")
		}
		return cloud.PutI64(cents), nil
	})

	if err := routes(app, s); err != nil {
		return err
	}

	log.Info("treasury mounted", "brand", deps.Brand, "ledgerOfRecord", record.Name(), "anchor", s.State.anchor.configured())
	return nil
}

// ops binds the ledger of record to the typed handlers: a TypedHandler takes only
// (context, *In), so the service arrives as a RECEIVER.
type ops struct{ s *cloud.Service[state] }

// routes registers the treasury surface. ONE scope-aware /v1/finance/* engine, three
// tenancy surfaces (HIP finance): per-org reads derive the tenant from the validated
// IAM identity and see ONLY their own accounts; the reserve fund + revenue-share +
// house mutations are locked to SuperAdmin under /v1/admin/treasury* (the console
// admin-proxy convention, enveloped).
func routes(app cloud.Router, s *cloud.Service[state]) error {
	z := cloud.ZipApp(app)
	if z == nil {
		return fmt.Errorf("treasury.routes: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	o := ops{s: s}
	// The bridge FIRST — a typed op is handed only a context, so the request its
	// SuperAdmin predicate and validated org are read from is parked there. Bounded to
	// treasury's declared prefixes; Serve installs one app-wide too, harmlessly.
	app.Use(cloud.Bridge())
	zip.Get(z, "/v1/finance/treasury", o.treasury, opID("financeTreasury"))                      // per-org: reserve transparency + policy
	zip.Get(z, "/v1/finance/accounts", o.accounts, opID("financeAccounts"))                      // per-org: own ledger accounts (admin: ?org=/?scope=house)
	zip.Get(z, "/v1/admin/treasury", o.report, opID("adminTreasury"))                            // SuperAdmin: report + journal + anchor
	zip.Post(z, "/v1/admin/treasury/policy", o.setPolicy, opID("adminTreasuryPolicy"))           // SuperAdmin: set revenue-share %
	zip.Post(z, "/v1/admin/treasury/sweep", o.sweep, opID("adminTreasurySweep"))                 // SuperAdmin: accrue revenue-share
	zip.Post(z, "/v1/admin/treasury/seed", o.seed, opID("adminTreasurySeed"))                    // SuperAdmin: inject reserve capital
	zip.Post(z, "/v1/admin/treasury/anchor", o.anchor, opID("adminTreasuryAnchor"))              // SuperAdmin: anchor ledger root on Hanzo L1
	zip.Post(z, "/v1/admin/treasury/bind-anchor", o.bindAnchor, opID("adminTreasuryBindAnchor")) // SuperAdmin: bind the reserve MPC wallet as the anchor signer
	return nil
}

// opID is the per-route stable operation id — the name the OpenAPI document, the MCP
// tool and the CLI command all take. The summary is NOT set here: cmd/zipdoc lifts it
// from each handler's own doc comment.
func opID(id string) zip.OpOption { return zip.WithOperationID(id) }

// admit is the SuperAdmin gate for a typed op: the request a typed handler cannot see
// is parked on its context by cloud.Bridge. Fails CLOSED off the HTTP path, where
// there is no validated identity to admit.
func admit(ctx context.Context) (*zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok || !c.IsAdmin() {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	return c, nil
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

// ── customer surface ─────────────────────────────────────────────────────────

// treasury reports the reserve fund's health and the revenue-share policy. Any
// validated caller may read it: this is a TRANSPARENCY view — a partner or author can
// see the pool that backs their payouts is solvent — not per-org money, which is the
// customer's commerce balance at /v1/billing/balance. Policy is read-only here; only
// SuperAdmin sets it.
//
// Response: {"reserveCents": 1250000, "accruedCents": 4000000, "paidCents": 2750000, "byProgramCents": {"referral": 2750000}, "policy": {"revenueShareBps": 2000, "updatedAt": 1780000000}, "solventForPayout": true}
func (o ops) treasury(ctx context.Context, _ *struct{}) (*ledger.Report, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view the treasury")
	}
	if _, ok := principal.Org(c); !ok {
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
	// Address is the ledger account address, e.g. "fund:reserve" or "org:acme:wallet".
	Address string `json:"address"`
	// BalanceCents is that account's balance in minor units.
	BalanceCents int64 `json:"balanceCents"`
}

// AccountsQuery widens the accounts read. Both fields are honoured for SuperAdmin
// ONLY; a per-org caller's scope is fixed to its own tenant whatever it sends.
type AccountsQuery struct {
	// Scope is "house" to read the reserve/revenue/payout house accounts.
	// SuperAdmin only.
	Scope string `json:"scope"`
	// Org reads one specific tenant's accounts. SuperAdmin only.
	Org string `json:"org"`
}

// AccountsView is the scope-aware ledger-account read.
type AccountsView struct {
	// Scope is the scope that was served: "org" or "house".
	Scope string `json:"scope"`
	// Tenant is the org the accounts belong to; empty under house scope.
	Tenant string `json:"tenant"`
	// Accounts is one row per ledger account in scope.
	Accounts []accountView `json:"accounts"`
}

// accounts lists the caller's ledger accounts and their balances. Tenant isolation is
// enforced SERVER-SIDE: a per-org caller sees ONLY accounts under its own
// "org:<tenant>:" prefix, never house accounts and never another tenant's. SuperAdmin
// may widen with scope=house or org=<tenant> — the ONLY way to cross the tenant
// boundary. Honest empty until a tenant has ledger postings.
//
// Example: {"scope": "house"}
// Response: {"scope": "house", "tenant": "hanzo", "accounts": [{"address": "fund:reserve", "balanceCents": 1250000}]}
func (o ops) accounts(ctx context.Context, in *AccountsQuery) (*AccountsView, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view accounts")
	}
	tenant, ok := principal.Org(c)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view accounts")
	}
	prefix := "org:" + tenant + ":"
	scope := "org"
	if c.IsAdmin() {
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
	return &AccountsView{Scope: scope, Tenant: tenant, Accounts: accounts}, nil
}

// ── admin surface (SuperAdmin, fail-closed) ────────────────────────────────

// JournalQuery bounds how much of the journal a report carries.
type JournalQuery struct {
	// Limit caps the journal entries returned. 0 or below means the default;
	// anything above the maximum is clamped to it.
	Limit int `json:"limit"`
}

// TreasuryReport is the SuperAdmin fund view.
type TreasuryReport struct {
	// Report is the reserve-fund health snapshot.
	Report ledger.Report `json:"report"`
	// Journal is the most recent double-entry postings, newest first.
	Journal []ledger.Entry `json:"journal"`
	// Anchor is the Hanzo L1 anchor status for the current ledger root.
	Anchor AnchorStatus `json:"anchor"`
}

// TreasuryReportOut is the admin envelope the console's admin proxy unwraps.
type TreasuryReportOut struct {
	Status string          `json:"status"`
	Msg    string          `json:"msg"`
	Data   *TreasuryReport `json:"data"`
}

// report returns the fund, its journal and its anchor status in one read. The journal
// is the recent double-entry postings; the anchor is the Hanzo L1 commit state of the
// current ledger root. SuperAdmin only.
//
// Example: {"limit": 50}
func (o ops) report(ctx context.Context, in *JournalQuery) (*TreasuryReportOut, error) {
	if _, err := admit(ctx); err != nil {
		return nil, err
	}
	rep, err := o.s.State.record.Snapshot(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "treasury snapshot: %v", err)
	}
	entries, err := o.s.State.record.Entries(ctx, journalLimitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "journal: %v", err)
	}
	return &TreasuryReportOut{Status: "ok", Data: &TreasuryReport{
		Report:  rep,
		Journal: entries,
		Anchor:  o.s.State.anchor.status(ctx, o.s.State.record),
	}}, nil
}

// policyRequest is the POST /v1/admin/treasury/policy body.
type policyRequest struct {
	// RevenueShareBps is the share of net platform revenue a sweep accrues into
	// the reserve fund, in basis points (0–10000).
	RevenueShareBps int64 `json:"revenueShareBps"`
}

// PolicyOut is the admin envelope carrying the policy as it stands after the write.
type PolicyOut struct {
	Status string      `json:"status"`
	Msg    string      `json:"msg"`
	Data   *PolicyData `json:"data"`
}

// PolicyData wraps the stored revenue-share policy.
type PolicyData struct {
	// Policy is the revenue-share policy now in force.
	Policy ledger.Policy `json:"policy"`
}

// setPolicy sets the revenue-share basis points a sweep accrues. The value is 0–10000
// and the answer carries the policy as it now stands. SuperAdmin only; the write is
// audited.
//
// Example: {"revenueShareBps": 2000}
func (o ops) setPolicy(ctx context.Context, in *policyRequest) (*PolicyOut, error) {
	if _, err := admit(ctx); err != nil {
		return nil, err
	}
	pol, err := o.s.State.record.SetPolicy(ctx, in.RevenueShareBps, time.Now().Unix())
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	emitAudit(o.s, ctx, "treasury.policy", "", "", map[string]any{"revenueShareBps": pol.RevenueShareBps})
	return &PolicyOut{Status: "ok", Data: &PolicyData{Policy: pol}}, nil
}

// sweepRequest is the POST /v1/admin/treasury/sweep body. RevenueCents is the net
// platform revenue MEASURED for the period (the caller — a cron or an operator —
// supplies it from the revenue view; treasury does the accounting, not the metering,
// keeping the concerns orthogonal). Period defaults to the current UTC month.
type sweepRequest struct {
	// Period is the accounting period, "YYYY-MM"; empty means the current UTC month.
	Period string `json:"period"`
	// RevenueCents is the net platform revenue measured for that period, in minor
	// units. Must be >= 0.
	RevenueCents int64 `json:"revenueCents"`
}

// SweepData is one period's accrual result.
type SweepData struct {
	// Period is the period that was accrued.
	Period string `json:"period"`
	// RevenueCents echoes the revenue the accrual was computed from.
	RevenueCents int64 `json:"revenueCents"`
	// AccruedCents is the amount posted into the reserve fund.
	AccruedCents int64 `json:"accruedCents"`
	// Created is false when this period was already swept (idempotent no-op).
	Created bool `json:"created"`
	// ReserveCents is the fund balance after the accrual.
	ReserveCents int64 `json:"reserveCents"`
}

// SweepOut is the admin envelope around a sweep result.
type SweepOut struct {
	Status string     `json:"status"`
	Msg    string     `json:"msg"`
	Data   *SweepData `json:"data"`
}

// sweep posts one period's revenue-share accrual into the fund. It reports the fund
// balance after it. Idempotent per period: a repeat returns created=false and posts
// nothing. SuperAdmin only; the accrual is audited. Treasury does the accounting, not
// the metering — the caller supplies the measured revenue.
//
// Example: {"period": "2026-07", "revenueCents": 4000000}
func (o ops) sweep(ctx context.Context, in *sweepRequest) (*SweepOut, error) {
	if _, err := admit(ctx); err != nil {
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
	return &SweepOut{Status: "ok", Data: &SweepData{
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
	// Memo describes the injection; empty defaults to "reserve capital injection".
	Memo string `json:"memo"`
	// Ref is an idempotency key. Without one each seed is a distinct injection.
	Ref string `json:"ref"`
}

// SeedData is one capital injection's result.
type SeedData struct {
	// Entry is the journal entry the injection posted.
	Entry ledger.Entry `json:"entry"`
	// Created is false when this ref was already seeded (idempotent no-op).
	Created bool `json:"created"`
	// ReserveCents is the fund balance after the injection.
	ReserveCents int64 `json:"reserveCents"`
}

// SeedOut is the admin envelope around a capital injection.
type SeedOut struct {
	Status string    `json:"status"`
	Msg    string    `json:"msg"`
	Data   *SeedData `json:"data"`
}

// seed injects bootstrap capital into the reserve fund. It is how backed payouts
// begin before the first revenue-share sweep. Idempotent by ref: a repeat with the
// same ref returns created=false and posts nothing. SuperAdmin only; the injection is
// audited.
//
// Example: {"amountCents": 1000000, "memo": "seed round", "ref": "seed-2026-07"}
func (o ops) seed(ctx context.Context, in *seedRequest) (*SeedOut, error) {
	if _, err := admit(ctx); err != nil {
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
	return &SeedOut{Status: "ok", Data: &SeedData{Entry: entry, Created: created, ReserveCents: reserve}}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

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

func journalLimitOf(n int) int {
	if n <= 0 {
		return journalLimit
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
