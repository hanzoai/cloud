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
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/treasury/formance"
	"github.com/hanzoai/cloud/clients/treasury/ledger"
	"github.com/hanzoai/cloud/clients/treasury/ledger/sqlstore"
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
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("treasury.Mount: nil zip.App")
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

	// ONE scope-aware /v1/finance/* engine, three tenancy surfaces (HIP finance):
	// per-org reads derive the tenant from the validated IAM identity and see ONLY
	// their own accounts; the reserve fund + revenue-share + house mutations are
	// locked to SuperAdmin under /v1/admin/treasury* (the console admin-proxy
	// convention, enveloped).
	app.Get("/v1/finance/treasury", cloud.Handle(s, myTreasury))                 // per-org: reserve transparency + policy
	app.Get("/v1/finance/accounts", cloud.Handle(s, myAccounts))                 // per-org: own ledger accounts (admin: ?org=/?scope=house)
	app.Get("/v1/admin/treasury", cloud.Handle(s, adminReport))                  // SuperAdmin: report + journal + anchor
	app.Post("/v1/admin/treasury/policy", cloud.Handle(s, adminSetPolicy))       // SuperAdmin: set revenue-share %
	app.Post("/v1/admin/treasury/sweep", cloud.Handle(s, adminSweep))            // SuperAdmin: accrue revenue-share
	app.Post("/v1/admin/treasury/seed", cloud.Handle(s, adminSeed))              // SuperAdmin: inject reserve capital
	app.Post("/v1/admin/treasury/anchor", cloud.Handle(s, adminAnchor))          // SuperAdmin: anchor ledger root on Hanzo L1
	app.Post("/v1/admin/treasury/bind-anchor", cloud.Handle(s, adminBindAnchor)) // SuperAdmin: bind the reserve MPC wallet as the anchor signer

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

// ── customer surface ─────────────────────────────────────────────────────────

// myTreasury answers GET /v1/finance/treasury for any validated caller: the
// reserve-fund health + the revenue-share policy. This is a TRANSPARENCY view — a
// partner/author can see the pool that backs their payouts is solvent — not per-org
// money (that is the customer's commerce balance at /v1/billing/balance). Policy is
// read-only here; only SuperAdmin sets it.
func myTreasury(s *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return zip.ErrForbidden("sign in to view the treasury")
	}
	rep, err := s.State.record.Snapshot(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "treasury snapshot: %v", err)
	}
	return c.JSON(http.StatusOK, rep)
}

// accountView is one row of the scope-aware accounts read.
type accountView struct {
	Address      string `json:"address"`
	BalanceCents int64  `json:"balanceCents"`
}

// myAccounts answers GET /v1/finance/accounts — the scope-aware ledger-account read
// that the console (surface #2) and finance.hanzo.ai (surface #3) consume. It is
// tenant-isolated SERVER-SIDE: a per-org caller sees ONLY accounts under its own
// "org:<tenant>:" prefix, never house or another tenant's accounts. SuperAdmin may
// widen with ?scope=house (the reserve/revenue/payout house accounts) or ?org=<t> (a
// specific tenant) — the ONLY way to cross the tenant boundary, and only for admins.
// Honest empty until a tenant has ledger postings (the commerce→ledger projection is
// the rebrand/datastore agents' concurrent work; this contract is stable for them).
func myAccounts(s *cloud.Service[state], c *zip.Ctx) error {
	tenant, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("sign in to view accounts")
	}
	prefix := "org:" + tenant + ":"
	scope := "org"
	if c.IsAdmin() {
		switch strings.TrimSpace(c.Query("scope")) {
		case "house":
			prefix, scope = "", "house" // all accounts; the report separates house from tenant
		default:
			if org := strings.TrimSpace(c.Query("org")); org != "" {
				prefix, scope, tenant = "org:"+org+":", "org", org
			}
		}
	}
	balances, err := s.State.record.AccountsWithPrefix(c.Context(), prefix)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "accounts: %v", err)
	}
	accounts := make([]accountView, 0, len(balances))
	for addr, bal := range balances {
		// house scope: exclude per-tenant accounts so the admin house view stays house-only.
		if scope == "house" && strings.HasPrefix(addr, "org:") {
			continue
		}
		accounts = append(accounts, accountView{Address: addr, BalanceCents: bal})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"scope":    scope,
		"tenant":   tenant,
		"accounts": accounts,
	})
}

// ── admin surface (SuperAdmin, fail-closed) ────────────────────────────────

// adminReport answers GET /v1/admin/treasury — the full fund report, the recent
// journal (double-entry postings), and the Hanzo L1 anchor status. SuperAdmin only.
func adminReport(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	ctx := c.Context()
	rep, err := s.State.record.Snapshot(ctx)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "treasury snapshot: %v", err)
	}
	entries, err := s.State.record.Entries(ctx, journalLimitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "journal: %v", err)
	}
	return adminOK(c, map[string]any{
		"report":  rep,
		"journal": entries,
		"anchor":  s.State.anchor.status(ctx, s.State.record),
	})
}

// policyRequest is the POST /v1/admin/treasury/policy body.
type policyRequest struct {
	RevenueShareBps int64 `json:"revenueShareBps"`
}

// adminSetPolicy sets the revenue-share basis points (0–10000). SuperAdmin only.
func adminSetPolicy(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	var body policyRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	pol, err := s.State.record.SetPolicy(c.Context(), body.RevenueShareBps, time.Now().Unix())
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	emitAudit(s, c.Context(), "treasury.policy", "", "", map[string]any{"revenueShareBps": pol.RevenueShareBps})
	return adminOK(c, map[string]any{"policy": pol})
}

// sweepRequest is the POST /v1/admin/treasury/sweep body. RevenueCents is the net
// platform revenue MEASURED for the period (the caller — a cron or an operator —
// supplies it from the revenue view; treasury does the accounting, not the metering,
// keeping the concerns orthogonal). Period defaults to the current UTC month.
type sweepRequest struct {
	Period       string `json:"period"`
	RevenueCents int64  `json:"revenueCents"`
}

// adminSweep posts the revenue-share accrual for a period (revenue → fund),
// idempotent per period. SuperAdmin only.
func adminSweep(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	var body sweepRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	period := strings.TrimSpace(body.Period)
	if period == "" {
		period = time.Now().UTC().Format("2006-01")
	}
	if body.RevenueCents < 0 {
		return zip.ErrBadRequest("revenueCents must be >= 0")
	}
	entry, created, err := s.State.record.Accrue(c.Context(), period, body.RevenueCents, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "sweep: %v", err)
	}
	if created {
		emitAudit(s, c.Context(), "treasury.sweep", "", entry.ID, map[string]any{
			"period": period, "revenueCents": body.RevenueCents, "accruedCents": entry.Amount.Cents(),
		})
	}
	reserve, _ := s.State.record.ReserveCents(c.Context())
	return adminOK(c, map[string]any{
		"period":       period,
		"revenueCents": body.RevenueCents,
		"accruedCents": entry.Amount.Cents(),
		"created":      created,
		"reserveCents": reserve,
	})
}

// seedRequest is the POST /v1/admin/treasury/seed body — a bootstrap capital
// injection into the reserve fund. Ref (optional) is an idempotency key; without one
// each seed is a distinct injection.
type seedRequest struct {
	AmountCents int64  `json:"amountCents"`
	Memo        string `json:"memo"`
	Ref         string `json:"ref"`
}

// adminSeed injects bootstrap capital into the reserve fund so backed payouts can
// begin before the first revenue-share sweep. SuperAdmin only.
func adminSeed(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("SuperAdmin required")
	}
	var body seedRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.AmountCents <= 0 {
		return zip.ErrBadRequest("amountCents must be > 0")
	}
	ref := strings.TrimSpace(body.Ref)
	if ref == "" {
		ref = "seed:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	memo := strings.TrimSpace(body.Memo)
	if memo == "" {
		memo = "reserve capital injection"
	}
	entry, created, err := s.State.record.Seed(c.Context(), ref, memo, body.AmountCents, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "seed: %v", err)
	}
	if created {
		emitAudit(s, c.Context(), "treasury.seed", "", entry.ID, map[string]any{
			"amountCents": body.AmountCents, "ref": ref, "entryId": entry.ID,
		})
	}
	reserve, _ := s.State.record.ReserveCents(c.Context())
	return adminOK(c, map[string]any{"entry": entry, "created": created, "reserveCents": reserve})
}

// ── helpers ──────────────────────────────────────────────────────────────────

// adminOK writes the { status:"ok", msg, data } envelope the console's admin
// surface (originGet/originPost via app/admin/aggregate) unwraps — identical to
// clients/admin and clients/referrals. The customer /v1/finance surface stays bare
// JSON (read via the /cloud proxy + restGet).
func adminOK(c *zip.Ctx, data any) error {
	return c.JSON(http.StatusOK, map[string]any{"status": "ok", "msg": "", "data": data})
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

func journalLimitOf(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
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
