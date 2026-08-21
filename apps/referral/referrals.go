// Package referrals is referral ATTRIBUTION: who referred whom, and whether that
// referee ever became a real customer.
//
// Every org has a stable code and share link, a new org claims it at signup, and
// the edge advances signup → qualified once the referee actually makes metered
// spend. That attribution record IS this package's product.
//
// IT MOVES NO MONEY. There is no bonus, no grant, no deposit and no ledger write
// here. Credit enters an org by exactly two doors — a manual per-org admin grant
// against the auditable ledger, and an invite that carries credit to a new org —
// and this package is neither. A referral REWARD is an affiliate PAYABLE: it is
// tracked in hanzoai/commerce and settled by wire or to a connected wallet, never
// minted as platform credit.
//
// The loop, end to end:
//
//  1. Every org has a STABLE referral code (deriveCode: deterministic base32 of a
//     hash of the org id) and a link https://<brand>/?ref=<code>.
//  2. A new org signs up via a link → the console posts POST /v1/referral/claim
//     with the code → we record referrer↔referee at status signup. Self-referral
//     is blocked; one referral per referee ever (idempotent).
//  3. The referee QUALIFIES once they have made metered spend — the honest signal
//     that they used the product rather than merely signed up. Qualification is a
//     WRITE, so it happens on the admin sweep (POST /v1/admin/referral/sweep, the
//     cron path) and NOWHERE else. GET /v1/referral is a pure read: it reports
//     the attribution, it never advances it.
//
// Surface:
//
//	GET  /v1/referral                 (org)          my code, link, my referrals
//	POST /v1/referral/claim           (org=referee)  record a referral from a ?ref code
//	GET  /v1/admin/referral/bonuses   (SuperAdmin) every referral edge + a summary
//	POST /v1/admin/referral/sweep     (SuperAdmin) qualify-check every pending referral
//
// ONE OWNER FOR THE ADMIN PREFIX. The cross-tenant referral ANALYTICS board (top
// referrers, conversion) reads apps/affiliates' OWN tables, so it answers at
// GET /v1/admin/affiliate/referrals — the second segment names the capability
// that serves it (HIP-0139 §3.2). What is left under /v1/admin/referral is this
// package's alone: the edge directory and the qualify sweep.
//
// serve.go auto-registers GET /v1/referral/health.
package referral

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

const (
	// sweepLimit bounds one qualify sweep, so an unbounded pending backlog can't
	// wedge a single request.
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
	auditStore *audit.Recorder // best-effort qualification audit; nil disables it
}

var mounted *cloud.Service[state]

// Mount wires the referrals surface onto app per HIP-0106. Complex flavour: it
// holds a package-global (mounted) so Shutdown can release the store, so it
// constructs the Service value directly rather than via cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("referral.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("referral.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("referral.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "referral"), State: state{
		store:      store,
		commerce:   newCommerceClient(),
		linkBase:   linkBase(deps),
		auditStore: deps.Audit,
	}}
	mounted = s
	routes(app, s)
	s.Log.Info("referrals mounted", "brand", s.Brand, "linkBase", s.State.linkBase)
	return nil
}

// routes registers the referrals surface.
//
// Each middleware install is bounded by the EXACT path it gates, never by a
// subtree, so a group can never reach a neighbour's route. The two admin
// leaves get their own groups; the customer surface gets one at /v1/referral,
// whose only sibling is the auto-registered GET /v1/referral/health (a GET, which
// requireOrgOnWrite lets through so probes keep working).
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := referralOps{s: s}
	zapp := cloud.ZipApp(app)

	// THE TWO GATES ARE BOUND BY PATH, one subtree each, and never by a group.
	//
	// They used to ride on three app.Group(prefix).Use(...) installs while every
	// leaf was registered on the App with its whole path — the roots ARE these
	// prefixes, and declaring them on their own group with an empty leaf would name
	// "/v1/referral/", an address this API has never served. Middleware on a group
	// wraps what is composed BENEATH it, so all three groups were empty: the write
	// gate never ran on POST /v1/referral/claim and requireAdmin never ran on
	// EITHER admin board. zip refuses to compose that program now, which is how a
	// gate that had silently stopped running became visible.
	//
	// Bound is the honest word for what a group was standing in for. under() is the
	// same shape apps/commerce already uses for its error envelope
	// (commerceErrorScope) and that scope.Use uses for a subsystem's own prefixes.
	app.Use(zip.H(under("/v1/referral", requireOrgOnWrite())))
	zip.Get(zapp, "/v1/referral", o.mine)
	zip.Post(zapp, "/v1/referral/claim", o.claim)

	// The one-time-bonus ledger board. The cross-tenant analytics board is
	// affiliates' own, at GET /v1/admin/affiliate/referrals (shared spine).
	app.Use(zip.H(under("/v1/admin/referral/bonuses", requireAdmin())))
	zip.Get(zapp, "/v1/admin/referral/bonuses", o.adminList)

	app.Use(zip.H(under("/v1/admin/referral/sweep", requireAdmin())))
	zip.Post(zapp, "/v1/admin/referral/sweep", o.adminSweep)
}

// under is h, run only for prefix and what lives inside it, passed over for
// everything else. It is the bound a group prefix used to imply, stated as the
// predicate it always was — so a gate covers one subtree whether or not that
// subtree's routes were composed through a group object.
func under(prefix string, h zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		p := c.Path()
		if p != prefix && !strings.HasPrefix(p, prefix+"/") {
			return c.Continue()
		}
		return h(c)
	}
}

// requireOrgOnWrite refuses a WRITE with no validated principal before zip decodes
// its body.
//
// A typed op runs after the decode, so moving the identity check into the op would
// answer 400 to an unauthenticated caller whose body is also malformed, where this
// surface has always answered 403. The check therefore lives where the untyped
// handler's ran: ahead of the body.
//
// It gates by SAFE METHOD, not by naming one verb. The predecessor let everything
// that was not POST through, which made the gate's correctness depend on nobody
// ever adding a PUT/PATCH/DELETE — and, worse, taught the surface that "not POST"
// means "harmless", which is exactly the reasoning that let a GET reach a deposit.
// A method is waved through here only when it is READ-ONLY by definition (GET and
// HEAD, so the auto-registered GET /v1/referral/health stays probe-able); every
// other verb, including ones this package does not serve today, must carry an org.
// Being read-only is then enforced for real by the handlers: no GET in this package
// writes.
func requireOrgOnWrite() zip.Handler {
	return func(c *zip.Ctx) error {
		if isSafeMethod(c.Method()) {
			return c.Continue()
		}
		if _, ok := principal.Org(c); !ok {
			return zip.ErrForbidden("sign in to claim a referral")
		}
		return c.Continue()
	}
}

// isSafeMethod reports whether a method is read-only by definition (RFC 9110
// "safe"). OPTIONS is deliberately absent: it is safe, but it is not served here,
// and a gate that enumerates what it lets through fails closed on the verb nobody
// thought about.
func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead
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
	// Link is the shareable signup link carrying the code, on the brand's own host.
	Link string `json:"link"`
	// Referrals is one row per org that signed up with this code.
	Referrals []myReferralView `json:"referrals"`
}

// mine returns the caller's referral code, share link and the referrals they have made.
//
// The code is a stable, deterministic function of the org, so the link in this
// response is the same one every time. Each row carries the referee and the status
// of that attribution.
//
// IT IS A PURE READ. It advances no referral, grants nothing and deposits nothing
// — a GET reports state, it never changes it. Qualification is the admin sweep's
// job (POST /v1/admin/referral/sweep). The one row this handler can write is the
// caller's OWN code-directory entry (EnsureCode), which materialises a value
// deriveCode already computes deterministically from the org id so the code has an
// O(1) reverse lookup; it carries no money, no referral state and no other tenant.
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

	rows, err := s.State.store.ListByReferrer(ctx, org, listLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list referrals: %v", err)
	}

	views := make([]myReferralView, 0, len(rows))
	counts := statusCounts{}
	for _, r := range rows {
		counts.add(r.Status)
		views = append(views, myReferralView{
			ID: r.ID, Referee: r.RefereeOrg, Status: r.Status,
			CreatedAt: r.CreatedAt, QualifiedAt: r.QualifiedAt,
		})
	}

	return &myReferrals{
		Code:      code,
		Counts:    counts,
		Link:      s.State.linkBase + "/?ref=" + code,
		Referrals: views,
	}, nil
}

// claimRequest is the POST /v1/referral/claim body: the referrer's code the
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
// Recording a referral grants nothing, and neither does anything downstream of it:
// the edge later advances to qualified when the referee makes metered spend
// (POST /v1/admin/referral/sweep), and that is the end of it. No credit is ever
// issued from this package.
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

	id := mint.ID("ref")
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

// adminBonusDirectory is the SuperAdmin view of the referral edge directory.
type adminBonusDirectory struct {
	// Referrals is every referral in the directory, both orgs exposed.
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

// adminList returns every referral edge in the directory with a fleet summary.
//
// SuperAdmin only, fail-closed. This is the ATTRIBUTION directory — who referred
// whom and whether that referee became a customer. It carries no amounts because
// this package issues none. The cross-tenant referral ANALYTICS board (top
// referrers, conversion) is a different surface, GET /v1/admin/affiliate/referrals,
// owned by the affiliates subsystem over the shared attribution spine.
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
			Status: r.Status, CreatedAt: r.CreatedAt, QualifiedAt: r.QualifiedAt,
		})
	}
	// Envelope { status, msg, data } — the /v1/admin/* convention the console's
	// admin-aggregate proxy + originGet read (same as clients/admin).
	return &adminBonusesEnvelope{Data: adminBonusDirectory{Referrals: views, Summary: sum}, Status: "ok"}, nil
}

// sweepResult counts what one qualify sweep did.
type sweepResult struct {
	// Qualified is how many of those referrals qualified on this pass.
	Qualified int `json:"qualified"`
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

// adminSweep qualify-checks every pending referral and advances the ones that now qualify.
//
// SuperAdmin only, fail-closed. This is the cron path, and the ONLY path that
// advances a referral: a referee QUALIFIES once they have made metered spend — the
// honest signal that they actually used the product rather than merely signing up.
//
// Qualifying moves NO money. It records that an attribution became a real customer;
// what is owed for that is an affiliate payable in commerce, settled by wire or to a
// connected wallet. One pass is bounded, so a large backlog drains over several runs
// instead of wedging one request, and the latch makes the transition at-most-once
// under a concurrent sweep.
//
// It reads nothing from the caller — the counters it returns are the whole result.
func (o referralOps) adminSweep(ctx context.Context, _ *noIn) (*sweepEnvelope, error) {
	s := o.s
	pending, err := s.State.store.ListPending(ctx, "", sweepLimit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list pending: %v", err)
	}
	swept, qualified := 0, 0
	for _, r := range pending {
		swept++
		after, gerr := qualify(s, ctx, r)
		if gerr != nil {
			s.Log.Warn("referrals: sweep qualify failed", "id", r.ID, "err", gerr)
			continue
		}
		if after.Status == StatusQualified && r.Status != StatusQualified {
			qualified++
		}
	}
	return &sweepEnvelope{Data: sweepResult{Qualified: qualified, Swept: swept}, Status: "ok"}, nil
}

// ── qualify (attribution only — there is no credit path) ─────────────────────

// qualify advances a pending referral to qualified when the referee has made
// metered spend. That is the whole operation: it records that an attribution
// became a real customer, and it moves NO money.
//
// It is idempotent by the qualified_at latch, so a concurrent sweep observes one
// transition rather than two. A commerce read error leaves the referral pending,
// retried on the next sweep.
//
// This is deliberately NOT reachable from a GET. The reward owed for a qualified
// referral is an affiliate payable in hanzoai/commerce, settled by wire or to a
// connected wallet; it is never issued here as platform credit.
func qualify(s *cloud.Service[state], ctx context.Context, ref Referral) (Referral, error) {
	if ref.Status != StatusSignup {
		return ref, nil
	}
	spent, err := s.State.commerce.spendCents(ctx, ref.RefereeOrg)
	if err != nil {
		return ref, err // commerce hiccup — try again next sweep, stays pending
	}
	if spent <= 0 {
		return ref, nil // not qualified yet — the referee hasn't used the product
	}

	won, err := s.State.store.LatchQualified(ctx, ref.ID, time.Now().Unix())
	if err != nil {
		return ref, err
	}
	if won {
		emitQualifyAudit(s, ctx, ref)
	}
	return s.State.store.Get(ctx, ref.ID)
}

// emitQualifyAudit records the attribution transition in cloud's tamper-evident
// trail (action referral.qualified). Best-effort; a nil store is a no-op. There is
// no money in this record because there is no money in this package — it attests
// that a referee became a customer, nothing more.
func emitQualifyAudit(s *cloud.Service[state], ctx context.Context, ref Referral) {
	if s.State.auditStore == nil {
		return
	}
	rec := audit.Record{
		Actor:    audit.Actor{Org: ref.ReferrerOrg, Sub: "referrals"},
		Action:   "referral.qualified",
		Resource: audit.Resource{Type: "referral", ID: ref.ID},
		Auth:     audit.AuthContext{Method: "service"},
		Outcome:  audit.Outcome{Result: "success", Status: 200},
		After: audit.Redact(mustJSON(map[string]any{
			"referrerOrg": ref.ReferrerOrg, "refereeOrg": ref.RefereeOrg, "code": ref.Code,
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
	// Status is the referral's lifecycle state: "signup" until the referee makes
	// metered spend, then "qualified".
	Status string `json:"status"`
	// CreatedAt is when the referral was recorded, as a Unix timestamp.
	CreatedAt int64 `json:"createdAt"`
	// QualifiedAt is when the referee first made metered spend, as a Unix
	// timestamp; 0 while the referral is still pending.
	QualifiedAt int64 `json:"qualifiedAt"`
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
	// Status is the referral's lifecycle state: "signup" or "qualified".
	Status string `json:"status"`
	// CreatedAt is when the referral was recorded, as a Unix timestamp.
	CreatedAt int64 `json:"createdAt"`
	// QualifiedAt is when the referee first made metered spend, as a Unix
	// timestamp; 0 while still pending.
	QualifiedAt int64 `json:"qualifiedAt"`
}

// statusCounts is the customer-view tally of a referrer's referrals by status.
type statusCounts struct {
	// Total is every referral this org has made.
	Total int `json:"total"`
	// Signup is how many referees have signed up but not yet spent.
	Signup int `json:"signup"`
	// Qualified is how many referees have made metered spend.
	Qualified int `json:"qualified"`
}

func (s *statusCounts) add(status string) {
	s.Total++
	switch status {
	case StatusSignup:
		s.Signup++
	case StatusQualified:
		s.Qualified++
	}
}

// adminSummary is the fleet tally for the admin directory. It carries no amount:
// this package issues no credit, so there is no liability to total here.
type adminSummary struct {
	// Total is every referral in the directory.
	Total int `json:"total"`
	// Signup is how many are recorded but not yet qualified.
	Signup int `json:"signup"`
	// Qualified is how many referees have made metered spend.
	Qualified int `json:"qualified"`
}

func (a *adminSummary) add(r Referral) {
	a.Total++
	switch r.Status {
	case StatusSignup:
		a.Signup++
	case StatusQualified:
		a.Qualified++
	}
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
