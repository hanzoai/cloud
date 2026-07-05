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
//     with the code → we record referrer↔referee at status signed_up. Self-referral
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
//	GET  /v1/referrals               (org)          my code, link, referrals, credits earned
//	POST /v1/referrals/claim         (org=referee)  record a referral from a ?ref code
//	GET  /v1/admin/referrals         (global-admin) every referral + a summary
//	POST /v1/admin/referrals/sweep   (global-admin) qualify-check every pending referral
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
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

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

type svc struct {
	store      *Store
	commerce   commerce
	log        luxlog.Logger
	linkBase   string          // https://hanzo.ai (brand host) — the ?ref link prefix
	auditStore *audit.Recorder // best-effort grant audit; nil disables it
}

var mounted *svc

// Mount wires the referrals surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("referrals.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("referrals.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "referrals")
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
	s := &svc{
		store:      store,
		commerce:   newCommerceClient(os.Getenv("CLOUD_COMMERCE_HTTP_URL"), os.Getenv("COMMERCE_SERVICE_TOKEN")),
		log:        log,
		linkBase:   linkBase(deps),
		auditStore: deps.Audit,
	}
	mounted = s

	app.Get("/v1/referrals", s.myReferrals)
	app.Post("/v1/referrals/claim", s.claim)
	app.Get("/v1/admin/referrals", s.adminList)
	app.Post("/v1/admin/referrals/sweep", s.adminSweep)

	log.Info("referrals mounted", "brand", deps.Brand, "linkBase", s.linkBase, "commerce", s.commerce.configured())
	return nil
}

func init() {
	// Order 149: a free slot just before the AI /v1/* catch-all (150). No ordering
	// dependency (it owns its own store + fans out to commerce over HTTP); its
	// routes are all specific (/v1/referrals*, /v1/admin/referrals*) so they bind
	// ahead of the catch-all regardless.
	cloud.Register("referrals", 149, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("referrals.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// ── customer surface ─────────────────────────────────────────────────────────

// myReferrals answers GET /v1/referrals for the validated caller: their stable
// code + link, the referrals they've made (with status + credit earned), and the
// total credit earned. It ALSO opportunistically runs the qualify check for the
// caller's own pending referees, so the referrer's page is self-updating.
func (s *svc) myReferrals(c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("sign in to view referrals")
	}
	ctx := c.Context()

	code, err := s.store.EnsureCode(ctx, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "referral code: %v", err)
	}

	// Lazy qualify sweep for MY referees (bounded, best-effort — a commerce hiccup
	// never fails the page; the referral simply stays pending for the next check).
	if pending, perr := s.store.ListPending(ctx, org, sweepLimit); perr == nil {
		for _, r := range pending {
			if _, gerr := s.qualifyAndGrant(ctx, r); gerr != nil {
				s.log.Warn("referrals: lazy qualify check failed", "id", r.ID, "err", gerr)
			}
		}
	}

	rows, err := s.store.ListByReferrer(ctx, org, listLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list referrals: %v", err)
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

	return c.JSON(http.StatusOK, map[string]any{
		"code":               code,
		"link":               s.linkBase + "/?ref=" + code,
		"referrerBonusCents": referrerBonusCents,
		"refereeBonusCents":  refereeBonusCents,
		"creditsEarnedCents": earned,
		"counts":             counts,
		"referrals":          views,
	})
}

// claimRequest is the POST /v1/referrals/claim body: the referrer's code the
// referee arrived with (from a ?ref= link, stashed at signup).
type claimRequest struct {
	Code string `json:"code"`
}

// claim records a referral. The REFEREE is the validated caller (never client-
// supplied); the referrer is resolved from the code. Idempotent (one per referee,
// first-touch wins), self-referral blocked, unknown code rejected.
func (s *svc) claim(c *zip.Ctx) error {
	refereeOrg, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("sign in to claim a referral")
	}
	var body claimRequest
	if err := c.Bind(&body); err != nil {
		return err
	}
	code := normalizeCode(body.Code)
	if code == "" {
		return zip.ErrBadRequest("code is required")
	}
	ctx := c.Context()

	referrerOrg, err := s.store.OrgForCode(ctx, code)
	if err != nil {
		if err == errUnknownCode {
			return zip.ErrNotFound("unknown referral code")
		}
		return zip.Errorf(http.StatusInternalServerError, "resolve code: %v", err)
	}
	if referrerOrg == refereeOrg {
		return zip.ErrBadRequest("cannot refer yourself")
	}

	id, err := genID("ref")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	ref, created, err := s.store.Claim(ctx, id, referrerOrg, refereeOrg, code)
	if err != nil {
		switch err {
		case errSelfReferral:
			return zip.ErrBadRequest("cannot refer yourself")
		default:
			return zip.Errorf(http.StatusInternalServerError, "claim: %v", err)
		}
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return c.JSON(status, map[string]any{
		"id":        ref.ID,
		"code":      ref.Code,
		"status":    ref.Status,
		"created":   created,
		"createdAt": ref.CreatedAt,
	})
}

// ── admin surface (global-admin, fail-closed) ────────────────────────────────

// adminList answers GET /v1/admin/referrals — every referral (both orgs exposed)
// + a fleet summary. Global-admin only.
func (s *svc) adminList(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	rows, err := s.store.ListAll(c.Context(), adminLimitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list referrals: %v", err)
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
	return adminOK(c, map[string]any{"referrals": views, "summary": sum})
}

// adminSweep answers POST /v1/admin/referrals/sweep — the periodic qualify path
// (a cron/o11y hits it, or an operator on demand). It qualify-checks every
// pending referral and grants the ones that now qualify. Global-admin only.
func (s *svc) adminSweep(c *zip.Ctx) error {
	if !c.IsAdmin() {
		return zip.ErrForbidden("global admin required")
	}
	ctx := c.Context()
	pending, err := s.store.ListPending(ctx, "", sweepLimit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list pending: %v", err)
	}
	swept, credited := 0, 0
	for _, r := range pending {
		swept++
		after, gerr := s.qualifyAndGrant(ctx, r)
		if gerr != nil {
			s.log.Warn("referrals: sweep qualify failed", "id", r.ID, "err", gerr)
			continue
		}
		if after.Status == StatusCredited && r.Status != StatusCredited {
			credited++
		}
	}
	return adminOK(c, map[string]any{"swept": swept, "credited": credited})
}

// adminOK writes the { status:"ok", msg, data } envelope the console's admin
// surface (originGet/originPost via app/admin/aggregate) unwraps — identical to
// clients/admin's ok(). The customer /v1/referrals surface stays bare JSON (read
// via the /cloud proxy + restGet), matching clients/crm.
func adminOK(c *zip.Ctx, data any) error {
	return c.JSON(http.StatusOK, map[string]any{"status": "ok", "msg": "", "data": data})
}

// ── qualify → grant core (the ONE credit path, shared by sweep + lazy read) ───

// qualifyAndGrant is the loop's heart: if the referral is still pending and the
// referee has now made metered spend (the qualify signal), LATCH the one-time
// grant and deposit BOTH bonuses. Idempotent by the credited_at latch — a re-run,
// a concurrent read, and the sweep can never double-pay. A commerce read error
// leaves the referral pending (retried next check); a deposit error after the
// latch is logged loud (at-most-once is the safety priority for credit).
func (s *svc) qualifyAndGrant(ctx context.Context, ref Referral) (Referral, error) {
	if ref.Status == StatusCredited || ref.CreditedAt != 0 {
		return ref, nil
	}
	spent, err := s.commerce.spendCents(ctx, ref.RefereeOrg, orgSubject(ref.RefereeOrg))
	if err != nil {
		return ref, err // commerce hiccup — try again next check, stays pending
	}
	if spent <= 0 {
		return ref, nil // not qualified yet — the referee hasn't used the product
	}

	won, err := s.store.LatchCredit(ctx, ref.ID, referrerBonusCents, refereeBonusCents, time.Now().Unix())
	if err != nil {
		return ref, err
	}
	if !won {
		// A concurrent sweep/read already claimed + granted it — never double-pay.
		return s.store.Get(ctx, ref.ID)
	}

	referrerTxn, rerr := s.grant(ctx, ref.ReferrerOrg, referrerBonusCents,
		fmt.Sprintf("Referral bonus: %s qualified (code %s)", ref.RefereeOrg, ref.Code))
	refereeTxn, ferr := s.grant(ctx, ref.RefereeOrg, refereeBonusCents,
		fmt.Sprintf("Referral welcome bonus (code %s)", ref.Code))
	if err := s.store.SetTxns(ctx, ref.ID, referrerTxn, refereeTxn); err != nil {
		s.log.Error("referrals: record txns failed", "id", ref.ID, "err", err)
	}
	if rerr != nil || ferr != nil {
		// The latch already fired, so this bonus is NOT retried (at-most-once). Loud,
		// never silent — an operator can reconcile from this + the audit row.
		s.log.Error("referrals: bonus deposit failed (latched at-most-once; not retried)",
			"id", ref.ID, "referrerErr", rerr, "refereeErr", ferr)
	}
	s.emitGrantAudit(ctx, ref, referrerTxn, refereeTxn)
	return s.store.Get(ctx, ref.ID)
}

// grant deposits a promo credit into org's wallet (Credit/trial bucket) and
// returns the ledger transaction id. Subject == the bare org slug, exactly the
// wallet the balance panel reads (symmetric with admin.grantCredit).
func (s *svc) grant(ctx context.Context, org string, cents int64, note string) (string, error) {
	return s.commerce.deposit(ctx, org, orgSubject(org), cents, grantCurrency, note, grantTag)
}

// emitGrantAudit records a referral bonus in cloud's tamper-evident trail (action
// referral.credit, distinct from admin.customer.credit). Best-effort; a nil store
// is a no-op. The actor is the referral engine (a system grant, not a user).
func (s *svc) emitGrantAudit(ctx context.Context, ref Referral, referrerTxn, refereeTxn string) {
	if s.auditStore == nil {
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
	if _, err := s.auditStore.Append(ctx, rec); err != nil {
		s.log.Error("referrals: audit emit failed", "id", ref.ID, "err", err)
	}
}

// ── view models + helpers ─────────────────────────────────────────────────────

// myReferralView is one row in the referrer's own list (their side of the edge).
type myReferralView struct {
	ID           string `json:"id"`
	Referee      string `json:"referee"` // the org that signed up via my code
	Status       string `json:"status"`
	CreditsCents int64  `json:"creditsCents"` // what I earned from this referral
	CreatedAt    int64  `json:"createdAt"`
	QualifiedAt  int64  `json:"qualifiedAt"`
	CreditedAt   int64  `json:"creditedAt"`
}

// adminReferralView is one row in the global-admin directory (both orgs exposed).
type adminReferralView struct {
	ID                 string `json:"id"`
	ReferrerOrg        string `json:"referrerOrg"`
	RefereeOrg         string `json:"refereeOrg"`
	Code               string `json:"code"`
	Status             string `json:"status"`
	ReferrerGrantCents int64  `json:"referrerGrantCents"`
	RefereeGrantCents  int64  `json:"refereeGrantCents"`
	ReferrerTxn        string `json:"referrerTxn,omitempty"`
	RefereeTxn         string `json:"refereeTxn,omitempty"`
	CreatedAt          int64  `json:"createdAt"`
	QualifiedAt        int64  `json:"qualifiedAt"`
	CreditedAt         int64  `json:"creditedAt"`
}

// statusCounts is the customer-view tally of a referrer's referrals by status.
type statusCounts struct {
	Total     int `json:"total"`
	SignedUp  int `json:"signedUp"`
	Qualified int `json:"qualified"`
	Credited  int `json:"credited"`
}

func (s *statusCounts) add(status string) {
	s.Total++
	switch status {
	case StatusSignedUp:
		s.SignedUp++
	case StatusQualified:
		s.Qualified++
	case StatusCredited:
		s.Credited++
	}
}

// adminSummary is the fleet tally for the admin directory, including the total
// promo credit granted across both sides.
type adminSummary struct {
	Total        int   `json:"total"`
	SignedUp     int   `json:"signedUp"`
	Qualified    int   `json:"qualified"`
	Credited     int   `json:"credited"`
	GrantedCents int64 `json:"grantedCents"`
}

func (a *adminSummary) add(r Referral) {
	a.Total++
	switch r.Status {
	case StatusSignedUp:
		a.SignedUp++
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

func adminLimitOf(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
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
	if mounted == nil || mounted.store == nil {
		return nil
	}
	err := mounted.store.Close()
	mounted = nil
	return err
}
