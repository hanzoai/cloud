package cloud

// Resource billing — the ONE per-org gate+meter every non-LLM "create a
// resource" handler uses, so provisioned infrastructure (databases, buckets,
// caches, vector collections, ML models, training jobs) is paid for, in EVERY
// environment, by the resource's OWN org.
//
// It is the in-handler analogue of BillingGate (the request-edge LLM gate): both
// wrap the SAME canonical metering client (github.com/hanzoai/cloud/apps/metering,
// Deps.Metering) and the SAME commerce ledger. The edge gate prices by PATH and
// runs as middleware; resource billing runs INSIDE a create handler, where the
// caller's org has already been resolved to the exact slug that namespaces the
// backend resource — so the balance check and the debit are guaranteed to target
// that one org, never a default, never another org.
//
// Multitenancy contract (the whole point):
//   - org is the caller's resolved org slug (the value that namespaces the
//     resource). It is sent to commerce as BOTH the user identity AND the
//     X-IAM-Org-Id org header, OVERRIDING the metering client's default org —
//     so an unfunded org can never provision on another org's balance, and
//     a funded org's spend lands only on its own ledger.
//   - Fail-closed: a non-positive balance is 402; a commerce that cannot be
//     reached is 503 (unless the client was built fail-open). No free
//     provisioning on a billing outage for a priced resource.
//
// Env awareness: the real-money-vs-sandbox decision is structural — each
// deployment (mainnet/testnet/devnet, the 3-env split) points at its OWN
// commerce/Square, so test and dev STILL meter and bill, just against that env's
// sandbox ledger. This gate fires identically in every env; it never branches on
// env to bypass billing. Env is recorded for attribution only.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/apps/metering"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// DefaultResourceFeeCents is the fallback flat provision/create fee (in cents)
// when no operator override is set — $1.00, so "every service costs money" holds
// out of the box. It is a configurable POLICY default, not a fabricated market
// price: ops sets the real number per deployment/kind via ResourceFeeCents's env
// knobs. Set a kind to 0 to make it free (and therefore un-gated, mirroring the
// edge gate's price==0 pass-through).
const DefaultResourceFeeCents int64 = 100

// ledgerCallTimeout bounds the debit's wait on the money plane.
//
// A debit OWNS the commitment it settles (see [Charge.Debit]), so an unbounded
// call holds that commitment for as long as the ledger is unwell — and a
// commitment outstanding is money the wallet cannot spend. Bounded, the worst case
// is a debit that is logged and not written: the charge is lost, the customer keeps
// their money, and the gate goes on working.
//
// A var, not a const, so a test can drive the timeout path in milliseconds rather
// than making the suite wait out a real one.
var ledgerCallTimeout = 10 * time.Second

// Meter gates and meters per-org spend for non-LLM resource creation, reusing
// Deps.Metering (the single commerce billing client). Build it with NewMeter.
//
// It owns WHO pays and WHAT IS HELD: the payer's address is forced onto every
// debit, so a surface cannot bill one caller and attribute the row to another, and
// a reservation weighs its cost against this pod's other uncommitted spend. HOW the
// ledger is reached — co-resident, or a socket away — belongs to
// [metering.Client] and is not a question any caller here asks. A nil meter is
// inert, so an unconstructed deployment is never blocked.
type Meter struct {
	// inflight is what this pod's authorized-but-unsettled calls have COMMITTED,
	// so a second caller weighs the balance against the first one's commitment
	// rather than against money that is already being spent. See Reserve.
	inflight commitments

	m        *metering.Client
	provider string // commerce "provider" label for attribution (e.g. "provisioning", "compute").
	env      string // deployment env (mainnet|testnet|devnet); attribution only — never a billing bypass.
	log      luxlog.Logger
}

// NewMeter builds a Meter from the shared deps. provider labels
// the recorded usage so spend is attributable to the surface that metered it.
func NewMeter(deps Deps, provider string) *Meter {
	return &Meter{
		m:        deps.Metering,
		provider: provider,
		env:      Env(),
		log:      luxlog.Default(),
	}
}

// Authorize is the pre-create balance gate. It returns:
//
//	nil                              -> allow (balance positive, OR not priced,
//	                                    OR no meter constructed).
//	metering.ErrInsufficientBalance  -> deny, out of funds (render 402).
//	other error                      -> balance unknown; fail-closed denies
//	                                    (render 503). Fail-open returns nil.
//
// costCents<=0 means the kind is free → no gate (mirrors BillingGate's price==0
// short-circuit). The balance check honors ctx (a client disconnect/timeout
// cancels it).
//
// payer IS THE ADDRESS, both halves of it. A debit needs two things — which
// ledger file holds the money, and which account inside it — and this meter used
// to be handed ONE string and parse the second out of the first. That parse
// asserted "the wallet IS the org", which is true of a pooled tenant and false in
// the shared signup org, where every self-serve stranger lives and account.Payer
// resolves a person to <org>/<username>. So whether a surface billed the person
// or the platform's own pool came down to which helper its author happened to
// reach for, and the two were indistinguishable at the call site because both are
// strings. An [account.Account] answers both questions itself, so the question
// cannot be answered two ways: Org() names the books, Subject() names the wallet.
// A surface that genuinely means the org pool says so — account.Org(slug).
//
// costCents is forwarded as AuthInput.AmountCents so the gate enforces
// available >= costCents, not merely available > 0 — otherwise a 1-cent balance
// would authorize an arbitrarily expensive charge (the debit still lands, taking
// the ledger negative). This mirrors what a prepaid gate must do: refuse a
// request the balance cannot cover BEFORE the work runs.
// project + projectValidated are the caller's org SUB-SCOPE and whether it is
// bound to a VALIDATED identity claim — principal.ValidatedProject(c), the SAME
// signal the edge BillingGate threads. When validated, a project-scoped spend cap
// (issue #70) on (project, provider) HARD-enforces (402) on resource creation
// exactly as on the request edge; when not, it DEGRADES to soft (org- and
// service-scoped caps stay hard), so a forgeable X-Project-Id can neither
// hard-stop nor be evaded. service is intrinsically this meter's provider. Pass
// ("", false) on a background/no-principal path (org- and service-scoped caps only).
func (rm *Meter) Authorize(ctx context.Context, payer account.Account, project string, projectValidated bool, kind string, costCents int64) error {
	if costCents <= 0 {
		return nil
	}
	// An UNATTRIBUTABLE payer is an IDENTITY refusal, so it is answered here rather
	// than by the money plane, which can only answer in the vocabulary of money — a
	// 503 "Billing temporarily unavailable" for a request whose problem is that
	// nobody is asking.
	//
	// It sits ABOVE the ledger call so a fail-OPEN money policy cannot make an
	// unidentified caller free: fail-open decides what to do when the ledger is
	// unreachable, never who the caller is. See [ErrNoLedger] and [denial].
	if payer.Zero() {
		return ErrNoLedger
	}
	// NEVER CONSTRUCTED, and nothing else. Serve always builds a meter with a
	// client, so this is a construction defect rather than a deployment shape; where
	// the ledger lives is the client's question and it answers it for both
	// topologies. Allow, and above all never panic.
	if rm == nil || rm.m == nil {
		return nil
	}
	return rm.m.Authorize(ctx, metering.AuthInput{
		User: payer.Subject(), Org: payer.Org(), AmountCents: costCents,
		// Service (=provider) is server-set → always validated. Project hardens iff
		// it is claim-bound (projectValidated) — the anti project-spoof gate.
		Project: project, ProjectValidated: projectValidated, Service: rm.provider,
	})
}

// Record is the per-org debit: it writes the caller-built usage event after
// forcing the per-org billing invariants that make the debit land on the
// CALLER's ledger and never another org's:
//
//   - u.User and u.Org are OVERWRITTEN from payer — Subject() is the wallet the
//     debit lands in, Org() the books that hold it — so a caller can never bill
//     someone else, and a surface cannot accidentally leave them unset (which
//     would debit the client-default org).
//   - Provider defaults to the meter's provider; Status defaults to "success";
//     Currency defaults to "usd".
//
// It carries BOTH the one-time create fee and any recurring footprint charge
// (storage GB-month, GPU-hour) — the caller supplies the amount, so a usage-derived
// charge is the same call. Everything else it supplies (AmountCents, Model, Actor,
// RequestID, token counts, ClientIP) flows through so a metered surface can
// attribute spend richly. It is fire-and-forget on a background context and a
// no-op on a nil meter or a non-positive amount. kind is for the failure log.
//
// This is also the entry point for a surface that already HOLDS the act's
// server-assigned name (a domain registration ref, a company formation ref): it sets
// [metering.Usage.Ref] and the debit is exactly-once on it. Left unset, the meter mints
// a fresh name and the debit stands alone — which is what every per-request meter
// wants, since two calls are two acts.
func (rm *Meter) Record(payer account.Account, kind string, u metering.Usage) {
	rm.record(payer, kind, u, nil)
}

// record is Record with the one thing a RESERVATION needs and a fire-and-forget
// caller does not: posted, run once the debit has reached the ledger (or failed
// to).
//
// A hold cannot be released when the call returns — the debit is still in flight
// then, so the next gate would read a balance that still contains money already
// spent, which is the very window the reservation exists to close. The only
// moment that is true is inside the recording goroutine, so that is where the
// release is handed. posted runs on EVERY exit, including the ones that record
// nothing: a hold released late is a customer locked out of their own balance.
func (rm *Meter) record(payer account.Account, kind string, u metering.Usage, posted func()) {
	if posted == nil {
		posted = func() {}
	}
	// Same defensive line as Authorize: never-constructed records nothing and never panics.
	if rm == nil || rm.m == nil {
		posted()
		return
	}
	// Ask the VALUE what it is worth, never the wire fields. Usage carries three
	// amount sources with a documented precedence — typed Amount, then micro-USD,
	// then cents — and Usage.Money is the one function that resolves them, so this
	// guard and [metering.Client.Record]'s cannot disagree about what money is.
	if amt := u.Money(); amt.IsZero() || amt.IsNeg() {
		posted()
		return
	}
	// OWN THE STRINGS BEFORE THEY OUTLIVE THE REQUEST. This value is handed to a
	// background goroutine, and a Usage built in a handler carries zero-copy views
	// into the server's reused request arena (c.User(), c.RequestID(), the forwarded
	// IP). Without the clone the debit eventually marshals the NEXT request's bytes
	// onto this caller's row — and connections are reused across tenants, so the row
	// it corrupts belongs to someone else. One clone here, rather than every caller
	// of a fire-and-forget meter having to remember. See [metering.Usage.Clone].
	u = u.Clone()
	// NAME THE ACT BEFORE BILLING IT. The ledger dedups on this ref, so the name is
	// fixed here rather than left to the recording goroutine. Seal is idempotent, so a
	// caller that already holds the act's own name (a formation ref, a registration
	// ref) keeps it, and [metering.Client.Record] — which seals what it is given — is
	// unchanged by having been sealed one step earlier.
	u = u.Seal()
	u.User = payer.Subject() // the WALLET the debit lands in.
	u.Org = payer.Org()      // WHICH BOOKS hold it (X-Org-Id; overrides the client default).
	if u.Provider == "" {
		u.Provider = rm.provider
	}
	if u.Service == "" {
		u.Service = rm.provider // scope service axis = this meter's provider.
	}
	if u.Status == "" {
		u.Status = "success"
	}
	if u.Currency == "" {
		u.Currency = "usd"
	}
	m, log, env := rm.m, rm.log, rm.env
	settle(posted, log, payer.Subject(), kind, func() {
		ctx, cancel := context.WithTimeout(context.Background(), ledgerCallTimeout)
		defer cancel()
		if _, err := m.Record(ctx, u); err != nil && log != nil {
			log.Error("resource debit failed (resource created, not billed)",
				"payer", payer.Subject(), "books", payer.Org(), "kind", kind, "provider", u.Provider,
				"cents", u.AmountCents, "env", env, "err", err)
		}
	})
}

// settle runs a debit and gives the commitment back on whichever happens first:
// the ledger answering, or the bound expiring.
//
// A TIMER, NOT A CONTEXT, and that is the whole point of this function. The
// crossing to the money plane does not honour a context: zip.Call checks ctx.Err
// once before it starts and then issues a plain client Do with no deadline, so a
// ledger that ACCEPTS and answers slowly — apps/finance is per-org SQLite with one
// writer, so lock contention looks exactly like that — is uninterruptible from
// here. Passing a shorter context does not bound it; it only looks like it does.
//
// That mattered the moment the debit took ownership of the hold. An uninterruptible
// call holding a commitment strands it: reserve.go adds it to every later weigh-in
// for that wallet, so the customer is refused with a balance they can see and
// cannot spend, until the pod restarts. Bounding it here trades that for a small
// window in which a debit is still in flight while its commitment has already been
// returned — the pre-existing behaviour, and the right way round. Losing a charge
// is recoverable; taking a customer's balance away is not.
//
// posted is [hold.release], which is once-only, so the late arrival is harmless.
func settle(posted func(), log luxlog.Logger, org, kind string, record func()) {
	go func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			record()
		}()
		select {
		case <-done:
		case <-time.After(ledgerCallTimeout):
			if log != nil {
				log.Warn("ledger slow: commitment released before the debit landed",
					"org", org, "kind", kind, "after", ledgerCallTimeout.String())
			}
		}
		posted() // the hold ends where the money lands, or where we stop waiting for it.
	}()
}

// denial is the ONE decision behind a refused Gate: which status the money wire
// answers with, and the code+message it carries. Both renderings below read it,
// so an untyped handler and a typed op can never describe the same refusal two
// different ways.

// ErrNoLedger is a priced act with no ledger to charge: there is nobody to bill
// because there is nobody. It is the ONE value [Meter.Authorize] answers with
// when it is handed an empty org, so all of its callers refuse identically
// without any of them re-deciding it — [denial] renders it as the tenant gate's
// own 403 rather than as a fault of the biller.
var ErrNoLedger = errors.New("no validated principal")

func denial(err error) (int, string, string) {
	// Identity before money: this refusal is about WHO is asking, so it never
	// takes a money status. Checked first because it is the more specific fact.
	if errors.Is(err, ErrNoLedger) {
		return http.StatusForbidden, "forbidden", ErrNoLedger.Error()
	}
	// The ONE classifier (errmap.go), so the money wire and the app's error
	// renderer answer one refusal identically. It keeps the DISTINCT 402s — a
	// per-scope cap (issue #70) is never the out-of-funds shape, which would be
	// the wrong code plus a retry storm against a ceiling that will not clear
	// until the period rolls over — and it keeps an UPSTREAM'S status, so a 402
	// commerce already decided is not re-decided into 503 here.
	if he, ok := refused(err); ok {
		return he.Status, he.Code, he.Msg
	}
	// The gate could not decide. Unknown is not free, and it is not a refusal the
	// caller can act on either.
	return http.StatusServiceUnavailable, "balance_unavailable", "Billing temporarily unavailable"
}

// denyBody is the money wire's body: the NESTED {"error":{"code","message"}} the
// edge gate emits, so every Hanzo surface refuses in one contract.
func denyBody(code, msg string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": msg}}
}

// DenyResource renders the Gate denial outcomes as the SAME JSON the edge gate
// returns (denyBilling), so every Hanzo surface emits one error contract:
// 402 insufficient_balance / 503 balance_unavailable.
//
// It WRITES the response, which is what an untyped handler wants and what a
// typed op cannot use: zip stamps the op's own status over a hand-written one
// after the handler returns, so a typed op refuses with Denied instead.
func DenyResource(c *zip.Ctx, err error) error {
	status, code, msg := denial(err)
	return c.JSON(status, denyBody(code, msg))
}

// deniedErr carries a refused Gate as an ERROR, which is the only refusal
// channel a typed op has. It holds the money wire's own status and body, and
// DenyEnvelope writes them back untouched — so a typed op refuses with exactly
// the bytes DenyResource writes beside it, rather than reshaping a 402 the whole
// fleet reads by `error.code` into zip's RFC 9457 problem members.
//
// It is not an escape from typing. The op still declares its In and its Out, so
// the document, the MCP tool, the CLI command and the SDK method all exist; what
// it declines to do is invent a SECOND vocabulary for a refusal the platform
// already has words for.
type deniedErr struct {
	status int
	code   string
	msg    string
}

func (e *deniedErr) Error() string { return e.msg }

// Unwrap gives the refusal a status and a message OFF the HTTP path, where there
// is no response to write bytes into: an MCP tools/call and an in-process CLI
// invoke run the op without passing through DenyEnvelope, so zip's own error
// handler renders this instead — the same status and sentence in zip's envelope,
// rather than a blanket 500 that loses both.
func (e *deniedErr) Unwrap() error {
	return &zip.HTTPError{Status: e.status, Code: e.code, Msg: e.msg}
}

// Denied turns a refused Gate into the error a TYPED op returns. Pair it with
// DenyEnvelope on the routes that can refuse; without the envelope the refusal
// still carries the right status and sentence (Unwrap), just in zip's shape.
func Denied(err error) error {
	status, code, msg := denial(err)
	return &deniedErr{status: status, code: code, msg: msg}
}

// DenyEnvelope writes a Denied refusal back as the money wire's own bytes.
// Install it on the group whose typed ops gate on balance — and BEFORE those
// routes, since fiber runs middleware in registration order — so the REST
// projection answers the 402/503 contract it always has. Anything else passes
// through untouched.
func DenyEnvelope() zip.Handler {
	return func(c *zip.Ctx) error {
		err := c.Continue()
		if d, ok := errors.AsType[*deniedErr](err); ok {
			return c.JSON(d.status, denyBody(d.code, d.msg))
		}
		return err
	}
}

// FeeCents resolves the flat fee (in cents) for kind from operator config, most
// specific first:
//
//	<envPrefix>_<KIND>   e.g. CLOUD_PROVISION_FEE_CENTS_SQL=500
//	<envPrefix>          e.g. CLOUD_PROVISION_FEE_CENTS=100  (all kinds)
//	def                  the surface's own default
//
// A clearly-named, configurable policy knob — never a fabricated price. A value
// of 0 makes the kind free (and un-gated). Negative/invalid values are ignored
// (fall through), so a typo can never make a paid resource free by accident.
//
// THE DEFAULT IS A PARAMETER because the resolution ORDER is fleet-wide and the
// number is not. $1.00 is right for provisioning a database and absurd for one
// web search, so two surfaces had already copied this three-line rule to change
// only its last line — sandbox.ResourceFee and answer.feeCents, both carrying a
// comment explaining that they exist to escape DefaultResourceFeeCents. A rule
// worth stating once with a value worth stating per surface is a function with
// an argument, not a third copy.
func FeeCents(envPrefix, kind string, def int64) int64 {
	if v, ok := parseNonNegCents(os.Getenv(envPrefix + "_" + strings.ToUpper(kind))); ok {
		return v
	}
	if v, ok := parseNonNegCents(os.Getenv(envPrefix)); ok {
		return v
	}
	return def
}

// ResourceFeeCents is FeeCents at the platform's provision-fee default
// (DefaultResourceFeeCents, $1.00) — the fee for creating a resource, which is
// what the great majority of metered surfaces charge for.
func ResourceFeeCents(envPrefix, kind string) int64 {
	return FeeCents(envPrefix, kind, DefaultResourceFeeCents)
}

func parseNonNegCents(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
