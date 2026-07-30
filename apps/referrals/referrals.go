// Package referrals mounts the Hanzo Cloud /v1/referrals/* viral-loop surface: a
// native-Go, per-org referral program on Base/SQLite that grants promo cloud
// credit through the SAME commerce ledger path as clients/admin.grantCredit (the
// trial/Credit bucket, tag grant:referral). It mirrors clients/crm's structure
// exactly — one SQLite store, server-side tenant isolation, one Mount, HIP-0106.
//
// The loop, end to end:
//
//  1. Every org has a STABLE referral code (deriveCode: deterministic base32 of a
//     hash of the org id) and a link https://<brand>/?ref=<code>.
//  2. A new org signs up via a link → the console posts POST /v1/referrals/claim
//     with the code → we record referrer↔referee at status signup. Self-referral
//     is blocked; one referral per referee ever (idempotent).
//  3. When the referee QUALIFIES (the honest signal: they've made metered spend —
//     actually USED the product, not just claimed a welcome grant) we grant BOTH
//     sides trial credit: referrer +$10, referee +$5. The grant is LATCHED
//     at-most-once (credited_at) so no sweep and no concurrent read can double-pay.
//     The qualify check runs lazily when the referrer loads GET /v1/referrals AND
//     via the admin sweep (POST /v1/admin/referrals/sweep, the cron path).
//
// Surface:
//
//	GET  /v1/referrals                 (org)          my code, link, referrals, credits earned
//	POST /v1/referrals/claim           (org=referee)  record a referral from a ?ref code
//	GET  /v1/admin/referrals/bonuses   (SuperAdmin) every one-time bonus referral + a summary
//	POST /v1/admin/referrals/sweep     (SuperAdmin) qualify-check every pending referral
//
// The cross-tenant referral ANALYTICS board (top referrers, conversion, multi-level
// accrual liability) is GET /v1/admin/referrals, owned by clients/affiliates over the
// shared attribution spine; this package owns the one-time-bonus ledger at
// /v1/admin/referrals/bonuses so the two admin surfaces compose without colliding.
//
// serve.go auto-registers GET /v1/referrals/health.
package referrals

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/commerce/transport"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/treasury"
	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// The referral economy — ONE place, so the bonus amounts and the ledger bucket
// are never re-defined. Amounts are USD minor units (cents); the grant lands in
// the commerce Credit/trial bucket (grant:* → Credit per DepositKind) — promo
// credit, NEVER refundable cash and never paid out, exactly like an admin comp.
const (
	// referrerBonusCents is granted to the REFERRER when a referee qualifies.
	referrerBonusCents int64 = 1000 // $10
	// refereeBonusCents is granted to the REFEREE on qualification (on top of the
	// $5 welcome grant they already got at signup).
	refereeBonusCents int64 = 500 // $5
	// grantCurrency is the ledger currency for referral bonuses.
	grantCurrency = "usd"
	// grantTag classifies the deposit as a non-cash Credit (trial) in commerce's
	// DepositKind (grant:* → Credit), distinct from admin's grant:admin so the
	// ledger/audit can tell a referral bonus from a staff comp.
	grantTag = "grant:referral"
)

const (
	// sweepLimit bounds one qualify sweep (admin sweep + lazy-on-read), so an
	// unbounded pending backlog can't wedge a single request.
	sweepLimit = 500
	// listLimit / maxAdminLimit bound the read responses.
	listLimit     = 500
	maxAdminLimit = 1000
)

// state is referrals's own data; shared deps live in the embedded cloud.Base.
type state struct {
	store      *Store
	commerce   commerce
	linkBase   string          // https://hanzo.ai (brand host) — the ?ref link prefix
	auditStore *audit.Recorder // best-effort grant audit; nil disables it
}

var mounted *cloud.Service[state]

// Mount wires the referrals surface onto app per HIP-0106. Complex flavour: it
// holds a package-global (mounted) so Shutdown can release the store, so it
// constructs the Service value directly rather than via cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("referrals.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("referrals.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("referrals.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("referrals.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "referrals.db"))
	if err != nil {
		return fmt.Errorf("referrals.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "referrals"), State: state{
		store:      store,
		commerce:   newCommerceClient(transport.BaseURL(os.Getenv("CLOUD_COMMERCE_HTTP_URL")), os.Getenv("COMMERCE_SERVICE_TOKEN")),
		linkBase:   linkBase(deps),
		auditStore: deps.Audit,
	}}
	mounted = s
	routes(app, s)
	s.Log.Info("referrals mounted", "brand", s.Brand, "linkBase", s.State.linkBase, "commerce", s.State.commerce.configured())
	return nil
}

// routes registers the referrals surface.
//
// Each middleware install is bounded by the EXACT path it gates, never by a
// subtree: /v1/admin/referrals is clients/affiliates' cross-tenant analytics
// board, so a group at that prefix would gate a neighbour's route. The two admin
// leaves get their own groups; the customer surface gets one at /v1/referrals,
// whose only sibling is the auto-registered GET /v1/referrals/health (a GET, which
// requireOrgOnWrite lets through so probes keep working).
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := referralOps{s: s}
	zapp := cloud.ZipApp(app)

	// Bridge FIRST, before the leaves it serves: a typed op receives only a
	// context, so the validated org reaches it by being parked there — never as an
	// In field, which is caller-supplied and would be a cross-tenant read the
	// caller asserted for itself. fiber runs middleware in registration order, so
	// one installed after its leaves never runs.
	rg := app.Group("/v1/referrals")
	rg.Use(cloud.Bridge(), requireOrgOnWrite())
	zip.Get(zapp, "/v1/referrals", o.mine)
	zip.Post(zapp, "/v1/referrals/claim", o.claim)

	// The one-time-bonus ledger board. The cross-tenant analytics board at
	// GET /v1/admin/referrals is owned by clients/affiliates (shared spine).
	bg := app.Group("/v1/admin/referrals/bonuses")
	bg.Use(cloud.Bridge(), requireAdmin())
	zip.Get(zapp, "/v1/admin/referrals/bonuses", o.adminList)

	sg := app.Group("/v1/admin/referrals/sweep")
	sg.Use(cloud.Bridge(), requireAdmin())
	zip.Post(zapp, "/v1/admin/referrals/sweep", o.adminSweep)
}

// requireOrgOnWrite refuses a WRITE with no validated principal before zip decodes
// its body.
//
// A typed op runs after the decode, so moving the identity check into the op would
// answer 400 to an unauthenticated caller whose body is also malformed, where this
// surface has always answered 403. The check therefore lives where the untyped
// handler's ran: ahead of the body. It is scoped to writes because the read under
// this prefix answers 403 from inside its own handler (no body to decode first),
// and because the auto-registered GET /v1/referrals/health must stay probe-able.
func requireOrgOnWrite() zip.Handler {
	return func(c *zip.Ctx) error {
		if c.Method() != http.MethodPost {
			return c.Continue()
		}
		if _, ok := principal.Org(c); !ok {
			return zip.ErrForbidden("sign in to claim a referral")
		}
		return c.Continue()
	}
}

// requireAdmin is the SuperAdmin gate on the two /v1/admin leaves. SuperAdmin-ness
// is a HEADER, which a typed op cannot see, so the check runs here — and running it
// here also keeps the 403 ahead of the body decode, exactly where the untyped
// handlers had it.
func requireAdmin() zip.Handler {
	return func(c *zip.Ctx) error {
		if !c.IsAdmin() {
			return zip.ErrForbidden("SuperAdmin required")
		}
		return c.Continue()
	}
}

// referralOps binds the service to the typed referral ops. A TypedHandler takes no
// service parameter, so the service arrives as a RECEIVER and every op is a method
// value — also the only bound form cmd/zipdoc can lift prose from.
type referralOps struct{ s *cloud.Service[state] }

// ── customer surface ─────────────────────────────────────────────────────────

// noIn is the input of an op that takes nothing: no body, no path parameter, no
// query.
type noIn struct{}

// myReferrals is the caller's own referral dashboard. Field order is the
// alphabetical key order the map it replaced marshalled in, so the bytes on the
// wire did not move when this route became a typed op.
type myReferrals struct {
	// Code is the org's STABLE referral code — a deterministic function of the org
	// id, so it never changes and never has to be stored to be reproduced.
	Code string `json:"code"`
	// Counts tallies this org's referrals by status.
	Counts statusCounts `json:"counts"`
	// CreditsEarnedCents is the total promo credit this org has earned as the
	// REFERRER, in USD cents.
	CreditsEarnedCents int64 `json:"creditsEarnedCents"`
	// Link is the shareable signup link carrying the code, on the brand's own host.
	Link string `json:"link"`
	// RefereeBonusCents is what a referee is granted on qualification, in USD cents.
	RefereeBonusCents int64 `json:"refereeBonusCents"`
	// Referrals is one row per org that signed up with this code.
	Referrals []myReferralView `json:"referrals"`
	// ReferrerBonusCents is what the referrer is granted when a referee qualifies,
	// in USD cents.
	ReferrerBonusCents int64 `json:"referrerBonusCents"`
}

// mine returns the caller's referral code, share link and the referrals they have made.
//
// The code is a stable, deterministic function of the org, so the link in this
// response is the same one every time. Each row carries the referee, its status and
// the credit this org earned from it; creditsEarnedCents is their sum.
//
// The read is self-updating: before listing, it runs the qualify check over this
// org's still-pending referees, so a referee who has since made metered spend is
// credited by the act of the referrer loading their page. That check is
// best-effort and bounded — a commerce hiccup leaves the referral pending for the
// next check rather than failing the page — and the grant is latched at-most-once,
// so this path and the admin sweep can never double-pay.
func (o referralOps) mine(ctx context.Context, _ *noIn) (*myReferrals, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view referrals")
	}

	code, err := s.State.store.EnsureCode(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "referral code: %v", err)
	}

	// Lazy qualify sweep for MY referees (bounded, best-effort — a commerce hiccup
	// never fails the page; the referral simply stays pending for the next check).
	if pending, perr := s.State.store.ListPending(ctx, org, sweepLimit); perr == nil {
		for _, r := range pending {
			if _, gerr := qualifyAndGrant(s, ctx, r); gerr != nil {
				s.Log.Warn("referrals: lazy qualify check failed", "id", r.ID, "err", gerr)
			}
		}
	}

	rows, err := s.State.store.ListByReferrer(ctx, org, listLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list referrals: %v", err)
	}

	views := make([]myReferralView, 0, len(rows))
	var earned int64
	counts := statusCounts{}
	for _, r := range rows {
		counts.add(r.Status)
		earned += r.ReferrerGrantCents
		views = append(views, myReferralView{
			ID: r.ID, Referee: r.RefereeOrg, Status: r.Status,
			CreditsCents: r.ReferrerGrantCents, CreatedAt: r.CreatedAt,
			QualifiedAt: r.QualifiedAt, CreditedAt: r.CreditedAt,
		})
	}

	return &myReferrals{
		Code:               code,
		Counts:             counts,
		CreditsEarnedCents: earned,
		Link:               s.State.linkBase + "/?ref=" + code,
		RefereeBonusCents:  refereeBonusCents,
		Referrals:          views,
		ReferrerBonusCents: referrerBonusCents,
	}, nil
}

// claimRequest is the POST /v1/referrals/claim body: the referrer's code the
// referee arrived with (from a ?ref= link, stashed at signup).
//
// Code is `url:"-"`. zip binds query over a decoded body, and this route has never
// read the query — a `?code=` that outranked the body would be a new way to
// address the write.
type claimRequest struct {
	// Code is the referrer's referral code, as it appeared in their ?ref= link.
	// Case and surrounding whitespace do not matter.
	Code string `json:"code" url:"-"`
}

// claimView is the receipt for a recorded referral. Field order is the
// alphabetical key order the map it replaced marshalled in, so the bytes on the
// wire did not move when this route became a typed op.
type claimView struct {
	// Code is the referral code the referral was recorded against.
	Code string `json:"code"`
	// Created is true when this call recorded the referral and false when it found
	// one already recorded for this referee — the idempotent replay.
	Created bool `json:"created"`
	// CreatedAt is when the referral was first recorded, as a Unix timestamp.
	CreatedAt int64 `json:"createdAt"`
	// ID is the referral's handle.
	ID string `json:"id"`
	// Status is the referral's lifecycle state: "signup" until the referee
	// makes metered spend, then "qualified", then "credited".
	Status string `json:"status"`
}

// claim records that the caller's org signed up through a referral code.
//
// The REFEREE is the validated caller, never a client field, and the referrer is
// resolved from the code — so a caller can only ever attach THEMSELVES to someone
// else's code. Referring yourself is 400 and an unknown code is 404.
//
// It is idempotent and first-touch: an org can be referred once, ever. A repeat
// call returns the referral already on file with created=false and 200, where the
// first call answers 201.
//
// Recording a referral grants nothing. Both bonuses are granted later, when the
// referee actually makes metered spend — see GET /v1/referrals and
// POST /v1/admin/referrals/sweep.
//
// Example: {"code": "H4NZ0ABC"}
func (o referralOps) claim(ctx context.Context, body *claimRequest) (*claimView, error) {
	s := o.s
	refereeOrg, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to claim a referral")
	}
	code := normalizeCode(body.Code)
	if code == "" {
		return nil, zip.ErrBadRequest("code is required")
	}

	referrerOrg, err := s.State.store.OrgForCode(ctx, code)
	if err != nil {
		if err == errUnknownCode {
			return nil, zip.ErrNotFound("unknown referral code")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve code: %v", err)
	}
	if referrerOrg == refereeOrg {
		return nil, zip.ErrBadRequest("cannot refer yourself")
	}

	id, err := genID("ref")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	ref, created, err := s.State.store.Claim(ctx, id, referrerOrg, refereeOrg, code)
	if err != nil {
		switch err {
		case errSelfReferral:
			return nil, zip.ErrBadRequest("cannot refer yourself")
		default:
			return nil, zip.Errorf(http.StatusInternalServerError, "claim: %v", err)
		}
	}
	// A FIRST claim answers 201, an idempotent replay 200. zip.WithStatus declares
	// ONE unconditional status and cannot express the pair, so this op stays
	// typed-but-shimmed: cloud.Created marks the create branch only, exactly as the
	// untyped handler did. It converts when zip can declare multi-status responses.
	if created {
		cloud.Created(ctx)
	}
	return &claimView{
		Code:      ref.Code,
		Created:   created,
		CreatedAt: ref.CreatedAt,
		ID:        ref.ID,
		Status:    ref.Status,
	}, nil
}

// ── admin surface (SuperAdmin, fail-closed) ────────────────────────────────

// adminListIn bounds the SuperAdmin bonus directory.
type adminListIn struct {
	// Limit is how many referrals to return, as a decimal string in the `?limit=`
	// query. Absent, unparseable or non-positive means 500; over 1000 is clamped to
	// 1000. It is a string rather than a number because the parse that has always
	// served this route trims surrounding whitespace, and one parse rule is better
	// than two.
	Limit string `json:"limit"`
}

// adminBonusDirectory is the SuperAdmin view of the one-time-bonus ledger.
type adminBonusDirectory struct {
	// Referrals is every referral in the ledger, both orgs exposed.
	Referrals []adminReferralView `json:"referrals"`
	// Summary is the fleet tally across those referrals.
	Summary adminSummary `json:"summary"`
}

// adminBonusesEnvelope is the { status, msg, data } wrapper the console's admin
// aggregate proxy unwraps. Field order is the alphabetical key order the map it
// replaced marshalled in, so the bytes on the wire did not move.
type adminBonusesEnvelope struct {
	// Data is the directory itself.
	Data adminBonusDirectory `json:"data"`
	// Msg is empty on success; the console surfaces it when status is not "ok".
	Msg string `json:"msg"`
	// Status is "ok" on success.
	Status string `json:"status"`
}

// adminList returns every one-time referral bonus in the ledger with a fleet summary.
//
// SuperAdmin only, fail-closed. This is the ONE-TIME BONUS ledger — who referred
// whom, what each side was granted and which ledger transactions carried it. The
// cross-tenant referral ANALYTICS board (top referrers, conversion, multi-level
// accrual liability) is a different surface, GET /v1/admin/referrals, owned by the
// affiliates subsystem over the shared attribution spine.
func (o referralOps) adminList(ctx context.Context, in *adminListIn) (*adminBonusesEnvelope, error) {
	s := o.s
	rows, err := s.State.store.ListAll(ctx, adminLimitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list referrals: %v", err)
	}
	views := make([]adminReferralView, 0, len(rows))
	sum := adminSummary{}
	for _, r := range rows {
		sum.add(r)
		views = append(views, adminReferralView{
			ID: r.ID, ReferrerOrg: r.ReferrerOrg, RefereeOrg: r.RefereeOrg, Code: r.Code,
			Status: r.Status, ReferrerGrantCents: r.ReferrerGrantCents, RefereeGrantCents: r.RefereeGrantCents,
			ReferrerTxn: r.ReferrerTxn, RefereeTxn: r.RefereeTxn,
			CreatedAt: r.CreatedAt, QualifiedAt: r.QualifiedAt, CreditedAt: r.CreditedAt,
		})
	}
	// Envelope { status, msg, data } — the /v1/admin/* convention the console's
	// admin-aggregate proxy + originGet read (same as clients/admin).
	return &adminBonusesEnvelope{Data: adminBonusDirectory{Referrals: views, Summary: sum}, Status: "ok"}, nil
}

// sweepResult counts what one qualify sweep did.
type sweepResult struct {
	// Credited is how many of those referrals qualified on this pass and were
	// granted their bonuses.
	Credited int `json:"credited"`
	// Swept is how many pending referrals were checked.
	Swept int `json:"swept"`
}

// sweepEnvelope is the { status, msg, data } wrapper the console's admin aggregate
// proxy unwraps. Field order is the alphabetical key order the map it replaced
// marshalled in, so the bytes on the wire did not move.
type sweepEnvelope struct {
	// Data is the sweep's counters.
	Data sweepResult `json:"data"`
	// Msg is empty on success; the console surfaces it when status is not "ok".
	Msg string `json:"msg"`
	// Status is "ok" on success.
	Status string `json:"status"`
}

// adminSweep qualify-checks every pending referral and grants the ones that now qualify.
//
// SuperAdmin only, fail-closed. This is the cron path: a referee QUALIFIES once
// they have made metered spend — the honest signal that they actually used the
// product rather than merely claiming a welcome grant — and qualifying grants the
// referrer and the referee their bonuses in one latched step.
//
// The grant is backed against the platform reserve fund before it is latched, so
// an empty fund leaves the referral honestly pending rather than minting unbacked
// credit, and the latch makes it at-most-once: this sweep, a concurrent sweep and
// the lazy check on GET /v1/referrals can never double-pay. One pass is bounded,
// so a large backlog drains over several runs instead of wedging one request.
//
// It reads nothing from the caller — the counters it returns are the whole result.
func (o referralOps) adminSweep(ctx context.Context, _ *noIn) (*sweepEnvelope, error) {
	s := o.s
	pending, err := s.State.store.ListPending(ctx, "", sweepLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list pending: %v", err)
	}
	swept, credited := 0, 0
	for _, r := range pending {
		swept++
		after, gerr := qualifyAndGrant(s, ctx, r)
		if gerr != nil {
			s.Log.Warn("referrals: sweep qualify failed", "id", r.ID, "err", gerr)
			continue
		}
		if after.Status == StatusCredited && r.Status != StatusCredited {
			credited++
		}
	}
	return &sweepEnvelope{Data: sweepResult{Credited: credited, Swept: swept}, Status: "ok"}, nil
}

// ── qualify → grant core (the ONE credit path, shared by sweep + lazy read) ───

// qualifyAndGrant is the loop's heart: if the referral is still pending and the
// referee has now made metered spend (the qualify signal), LATCH the one-time
// grant and deposit BOTH bonuses. Idempotent by the credited_at latch — a re-run,
// a concurrent read, and the sweep can never double-pay. A commerce read error
// leaves the referral pending (retried next check); a deposit error after the
// latch is logged loud (at-most-once is the safety priority for credit).
func qualifyAndGrant(s *cloud.Service[state], ctx context.Context, ref Referral) (Referral, error) {
	if ref.Status == StatusCredited || ref.CreditedAt != 0 {
		return ref, nil
	}
	spent, err := s.State.commerce.spendCents(ctx, ref.RefereeOrg, orgSubject(ref.RefereeOrg))
	if err != nil {
		return ref, err // commerce hiccup — try again next check, stays pending
	}
	if spent <= 0 {
		return ref, nil // not qualified yet — the referee hasn't used the product
	}

	// BACK the bonus against the platform reserve fund BEFORE latching: the combined
	// bonus ($15) debits fund:reserve (double-entry fund→payout:referral), idempotent
	// by the referral id. Not backed → the fund is empty; leave the referral pending
	// (honest, retried on the next sweep/qualify check) rather than mint unbacked
	// credit. Idempotent by ref, so a concurrent qualify + the latch below can never
	// double-charge the fund. Unmounted treasury → passthrough (backed=true).
	backed, entryID, berr := treasury.Reserve(ctx, treasury.ProgramReferral, "referral:"+ref.ID,
		fmt.Sprintf("Referral bonus: %s qualified (code %s)", ref.RefereeOrg, ref.Code),
		referrerBonusCents+refereeBonusCents)
	if berr != nil {
		return ref, fmt.Errorf("reserve referral bonus: %w", berr) // stays pending, retried
	}
	if !backed {
		s.Log.Warn("referrals: bonus deferred — treasury reserve insufficient",
			"id", ref.ID, "neededCents", referrerBonusCents+refereeBonusCents)
		return ref, nil // honestly pending until the fund is replenished
	}
	_ = entryID // the fund debit is linked to this referral by its ref (referral:<id>)

	won, err := s.State.store.LatchCredit(ctx, ref.ID, referrerBonusCents, refereeBonusCents, time.Now().Unix())
	if err != nil {
		return ref, err
	}
	if !won {
		// A concurrent sweep/read already claimed + granted it — never double-pay.
		return s.State.store.Get(ctx, ref.ID)
	}

	referrerTxn, rerr := grant(s, ctx, ref.ReferrerOrg, referrerBonusCents,
		fmt.Sprintf("Referral bonus: %s qualified (code %s)", ref.RefereeOrg, ref.Code))
	refereeTxn, ferr := grant(s, ctx, ref.RefereeOrg, refereeBonusCents,
		fmt.Sprintf("Referral welcome bonus (code %s)", ref.Code))
	if err := s.State.store.SetTxns(ctx, ref.ID, referrerTxn, refereeTxn); err != nil {
		s.Log.Error("referrals: record txns failed", "id", ref.ID, "err", err)
	}
	if rerr != nil || ferr != nil {
		// The latch already fired, so this bonus is NOT retried (at-most-once). Loud,
		// never silent — an operator can reconcile from this + the audit row.
		s.Log.Error("referrals: bonus deposit failed (latched at-most-once; not retried)",
			"id", ref.ID, "referrerErr", rerr, "refereeErr", ferr)
	}
	emitGrantAudit(s, ctx, ref, referrerTxn, refereeTxn)
	return s.State.store.Get(ctx, ref.ID)
}

// grant deposits a promo credit into org's wallet (Credit/trial bucket) and
// returns the ledger transaction id. Subject == the bare org slug, exactly the
// wallet the balance panel reads (symmetric with admin.grantCredit).
func grant(s *cloud.Service[state], ctx context.Context, org string, cents int64, note string) (string, error) {
	return s.State.commerce.deposit(ctx, org, orgSubject(org), cents, grantCurrency, note, grantTag)
}

// emitGrantAudit records a referral bonus in cloud's tamper-evident trail (action
// referral.credit, distinct from admin.customer.credit). Best-effort; a nil store
// is a no-op. The actor is the referral engine (a system grant, not a user).
func emitGrantAudit(s *cloud.Service[state], ctx context.Context, ref Referral, referrerTxn, refereeTxn string) {
	if s.State.auditStore == nil {
		return
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: ref.ReferrerOrg, Sub: "referrals"},
		Action:   "referral.credit",
		Resource: audit.Resource{Type: "credit", ID: ref.ID},
		Auth:     audit.AuthContext{Method: "service"},
		Outcome:  audit.Outcome{Result: "success", Status: 200},
		After: audit.Redact(mustJSON(map[string]any{
			"referrerOrg": ref.ReferrerOrg, "refereeOrg": ref.RefereeOrg, "code": ref.Code,
			"referrerGrantCents": referrerBonusCents, "refereeGrantCents": refereeBonusCents,
			"referrerTxn": referrerTxn, "refereeTxn": refereeTxn,
		})),
	}
	if _, err := s.State.auditStore.Append(ctx, rec); err != nil {
		s.Log.Error("referrals: audit emit failed", "id", ref.ID, "err", err)
	}
}

// ── view models + helpers ─────────────────────────────────────────────────────

// myReferralView is one row in the referrer's own list (their side of the edge).
type myReferralView struct {
	// ID is the referral's handle.
	ID string `json:"id"`
	// Referee is the org that signed up with my code.
	Referee string `json:"referee"`
	// Status is the referral's lifecycle state: "signup" until the referee
	// makes metered spend, then "qualified", then "credited".
	Status string `json:"status"`
	// CreditsCents is what I earned from this referral, in USD cents. It is 0
	// until the referee qualifies.
	CreditsCents int64 `json:"creditsCents"`
	// CreatedAt is when the referral was recorded, as a Unix timestamp.
	CreatedAt int64 `json:"createdAt"`
	// QualifiedAt is when the referee first made metered spend, as a Unix
	// timestamp; 0 while the referral is still pending.
	QualifiedAt int64 `json:"qualifiedAt"`
	// CreditedAt is when the bonuses were latched and granted, as a Unix
	// timestamp; 0 until they are. It is the at-most-once latch.
	CreditedAt int64 `json:"creditedAt"`
}

// adminReferralView is one row in the SuperAdmin directory (both orgs exposed).
type adminReferralView struct {
	// ID is the referral's handle.
	ID string `json:"id"`
	// ReferrerOrg is the org whose code was used.
	ReferrerOrg string `json:"referrerOrg"`
	// RefereeOrg is the org that signed up with it.
	RefereeOrg string `json:"refereeOrg"`
	// Code is the referral code the referral was recorded against.
	Code string `json:"code"`
	// Status is the referral's lifecycle state: "signup", "qualified" or
	// "credited".
	Status string `json:"status"`
	// ReferrerGrantCents is what the referrer was granted, in USD cents; 0 until
	// the referral is credited.
	ReferrerGrantCents int64 `json:"referrerGrantCents"`
	// RefereeGrantCents is what the referee was granted, in USD cents; 0 until the
	// referral is credited.
	RefereeGrantCents int64 `json:"refereeGrantCents"`
	// ReferrerTxn is the commerce ledger transaction that carried the referrer's
	// grant, omitted until one exists.
	ReferrerTxn string `json:"referrerTxn,omitempty"`
	// RefereeTxn is the commerce ledger transaction that carried the referee's
	// grant, omitted until one exists.
	RefereeTxn string `json:"refereeTxn,omitempty"`
	// CreatedAt is when the referral was recorded, as a Unix timestamp.
	CreatedAt int64 `json:"createdAt"`
	// QualifiedAt is when the referee first made metered spend, as a Unix
	// timestamp; 0 while still pending.
	QualifiedAt int64 `json:"qualifiedAt"`
	// CreditedAt is when the bonuses were latched and granted, as a Unix
	// timestamp; 0 until they are.
	CreditedAt int64 `json:"creditedAt"`
}

// statusCounts is the customer-view tally of a referrer's referrals by status.
type statusCounts struct {
	// Total is every referral this org has made.
	Total int `json:"total"`
	// Signup is how many referees have signed up but not yet spent.
	Signup int `json:"signup"`
	// Qualified is how many referees have spent but are not yet credited.
	Qualified int `json:"qualified"`
	// Credited is how many referrals have paid both bonuses.
	Credited int `json:"credited"`
}

func (s *statusCounts) add(status string) {
	s.Total++
	switch status {
	case StatusSignup:
		s.Signup++
	case StatusQualified:
		s.Qualified++
	case StatusCredited:
		s.Credited++
	}
}

// adminSummary is the fleet tally for the admin directory, including the total
// promo credit granted across both sides.
type adminSummary struct {
	// Total is every referral in the ledger.
	Total int `json:"total"`
	// Signup is how many are recorded but not yet qualified.
	Signup int `json:"signup"`
	// Qualified is how many have qualified but are not yet credited.
	Qualified int `json:"qualified"`
	// Credited is how many have paid both bonuses.
	Credited int `json:"credited"`
	// GrantedCents is the promo credit granted across BOTH sides of every
	// referral, in USD cents — the program's total liability to date.
	GrantedCents int64 `json:"grantedCents"`
}

func (a *adminSummary) add(r Referral) {
	a.Total++
	switch r.Status {
	case StatusSignup:
		a.Signup++
	case StatusQualified:
		a.Qualified++
	case StatusCredited:
		a.Credited++
	}
	a.GrantedCents += r.ReferrerGrantCents + r.RefereeGrantCents
}

// orgSubject is the billing subject commerce keys an org's wallet on — the bare
// org slug, exactly like clients/admin.orgSubject. Kept as a named function so
// the "subject == org" contract lives in one place.
func orgSubject(org string) string { return org }

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// adminLimitOf is the ONE `?limit=` rule for the admin board: an absent,
// unparseable or non-positive value is the default, and anything above the ceiling
// is clamped.
func adminLimitOf(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return listLimit
	}
	if n > maxAdminLimit {
		return maxAdminLimit
	}
	return n
}

// mustJSON marshals v for the audit After payload, returning an empty object on
// the (unexpected) marshal error rather than crashing a money action.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// linkBase resolves the ?ref link prefix. REFERRAL_LINK_BASE wins (an explicit
// override); else the brand's public host; else hanzo.ai. White-label by brand so
// a Lux/Zoo deployment mints its OWN link, never hanzo.ai.
func linkBase(deps cloud.Deps) string {
	if v := strings.TrimSpace(os.Getenv("REFERRAL_LINK_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	switch strings.ToLower(strings.TrimSpace(deps.Brand)) {
	case "lux":
		return "https://lux.network"
	case "zoo":
		return "https://zoo.ngo"
	case "pars":
		return "https://pars.ai"
	default:
		return "https://hanzo.ai"
	}
}

// Shutdown closes the referrals store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
