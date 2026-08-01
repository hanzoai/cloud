package cloud

// AbuseGate — the API plane's lifecycle defense. It watches every authenticated
// and anonymous request, classes its caller into a lane, and — when the shape of
// the traffic warrants it — asks /v1/risk what to do. It does not score. It
// senses, asks, and enforces.
//
// WHAT IT ADDS THAT THE TWO EXISTING LIMITERS DO NOT. EdgeRateLimit caps a
// client IP before identity; ScopeRateLimit caps an authenticated
// (org, project, service). Neither can see a CREDENTIAL. So a key lifted out of
// a CI log, used from a residential address, inside its org's normal ceiling, is
// invisible to both — and that is precisely the shape of token theft, credential
// stuffing, scraping and pay-as-you-go abuse. This gate keys on the credential,
// which is the thing that was stolen.
//
// WHERE IT SITS, and why exactly there (serve.go):
//
//	SanitizeIdentity → AuditTrail → ScopeRateLimit → AbuseGate → StarterGrant → BillingGate
//
//   - AFTER SanitizeIdentity, so the org it scopes to is the validated one and
//     the credential class it reads cannot be forged.
//   - INSIDE AuditTrail, so a refusal is a 401/403 that the tamper-evident trail
//     already records — with the actor, the resource and the outcome. There is no
//     second audit write here, because there must not be two records of one event.
//   - AFTER ScopeRateLimit, so ordinary over-rate traffic is already 429'd and
//     never reaches the scorer: rate limiting is a limiter's job, and asking a
//     model what to do about a request that is simply too fast would be paying
//     for an answer we already have.
//   - BEFORE BillingGate, so an abusive request is refused before it can consume
//     a balance.
//
// SHADOW BY DEFAULT, PER ORG. An org's Mode (edge.Policy.Mode) is shadow unless
// an operator sets it live. In shadow the gate observes, asks and RECORDS, and
// then lets the request through whatever the answer was. A statistical judgement
// that quietly started refusing an org's payments or logins because a feature
// shipped is the worst failure available here, so it cannot happen by default —
// only by decision, and the decision is visible at GET /v1/gateway/traffic.
//
// FAIL POLICY. The gate never decides what to do when the scorer is unavailable;
// cloud.Decide does, in one place, for every caller (see risk.go). An ordinary
// request proceeds; a PRIVILEGED GRANT — minting a credential, provisioning an
// identity, reading the key store — does not. That asymmetry is the whole
// posture: an outage in the risk plane must not be able to lock people out of
// the product, and must not be able to hand out standing authority either.

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// The shapes that make the gate ask. They are constants rather than per-org
// configuration on purpose: a threshold a tenant can widen is a threshold an
// attacker's target can be talked into widening, and these are properties of the
// protocol rather than preferences of a customer.
//
// They are deliberately narrower than the bot thresholds in agency.go: this is
// "worth a question", that is "already answered".
const (
	// screenFailures — 401/403 in the window from one credential. A client with a
	// stale token retries a handful of times; ten is a pattern.
	screenFailures = 10
	// screenPeers — distinct credentials presented from one address. Two is a
	// laptop with two projects open; four is a list being worked through.
	screenPeers = 4
	// screenPaths — distinct paths one credential touched. A real integration
	// walks a few endpoints; twenty-four is a survey.
	screenPaths = 24
	// screenFirst — a credential's first request in a window is always screened,
	// so a freshly stolen key is judged on its first use rather than after it has
	// done enough damage to trip a counter. This bounds the cost of screening to
	// one per credential per minute, not one per request.
	screenFirst = 1
)

// holdFor is how long a non-allow verdict is enforced before the scorer is asked
// again. Long enough that an attack costs one screen rather than one per
// request; short enough that a false positive clears itself in a minute without
// anyone being paged.
const holdFor = time.Minute

// screenCentsEnv names the operator knob for what one screen costs. Unset means
// ZERO: screens are COUNTED on the org's own usage ledger from day one, and
// priced when the SKU is priced. A default price here would be a fabricated one.
const screenCentsEnv = "CLOUD_RISK_SCREEN_CENTS"

// exemptPrefixes are the subtrees the gate does not watch. Two kinds, and each
// is a correctness requirement rather than a performance concession:
//
//   - THE APPEAL SURFACE. /v1/risk is where a refused caller reads the decision
//     that refused it and where an operator releases a hold. Putting the appeal
//     behind the thing that refuses makes a false positive unappealable, which is
//     not an acceptable property for a plane that refuses payments and logins.
//   - the internal money plane. ScopeRateLimit exempts these for the same reason
//     (its own config lives behind them), and a gate that could refuse them could
//     refuse its own metering.
//
// Deliberately NOT here: /v1/ml and /v1/aml. They are ordinary API surfaces
// served by ordinary apps, and exempting a whole prefix family because the risk
// product happens to own part of it would leave the compliance face and the model
// plane unwatched. The gate calls the scorer as a Go function, not as a route
// (see cloud.Decide), so watching them cannot re-enter anything.
var exemptPrefixes = []string{
	"/v1/risk/",
	"/v1/billing/",
	"/v1/commerce/",
	"/_/",
}

// AbuseGate returns the lifecycle-defense middleware. t is the shared edge
// sensor; deps carries the policy store, the logger and the metering client.
//
// It is a no-op passthrough when the sensor is absent, mirroring every other
// gate on this path: an unwired deployment is never blocked.
func AbuseGate(deps Deps, t *edge.Traffic) zip.Handler {
	if t == nil {
		return func(c *zip.Ctx) error { return c.Next() }
	}
	g := &abuseGate{
		traffic: t,
		policy:  deps.GatewayPolicy,
		meter:   NewResourceMeter(deps, "risk"),
		log:     deps.Logger,
		cents:   screenCents(),
	}
	return g.handle
}

func screenCents() int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(screenCentsEnv)), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

type abuseGate struct {
	traffic *edge.Traffic
	policy  *edge.Store
	meter   *ResourceMeter
	log     interface {
		Info(msg string, args ...any)
		Warn(msg string, args ...any)
	}
	cents int64
}

func (g *abuseGate) handle(c *zip.Ctx) error {
	path := c.Path()
	if Probe(c.Method(), path) {
		return c.Next()
	}
	for _, p := range exemptPrefixes {
		if strings.HasPrefix(path, p) {
			return c.Next()
		}
	}

	org, _ := principal.Org(c)
	now := time.Now()
	sig := edge.Signal{
		Org:  org,
		Cred: credentialOf(c),
		IP:   ClientIP(c),
		Path: routeFamily(path),
	}

	// Sense first, once. Every request is counted and lands in its lane whatever
	// happens next — including one refused by a held verdict, because a caller
	// that keeps knocking after being refused is exactly the caller a report needs
	// to show.
	class := credentialClass(c)
	p := g.traffic.Observe(sig, now)
	sig.Agency = agency(class, p)

	// A verdict already in force short-circuits the question. The sensor holds it
	// for at most a minute, so this is enforcement WITH a recent judgement behind
	// it, never enforcement that outlives its reason.
	if h, ok := g.traffic.Held(sig, now); ok {
		return g.enforce(c, sig, RiskVerdict{Action: h.Action, ID: h.Decision, Cause: h.Reason})
	}

	privileged := Privileged(c.Method(), path)
	if !g.screen(p, privileged) {
		return g.watch(c, sig)
	}

	// LIVE-ONLY QUESTION. In shadow the gate still senses and still reports, but
	// it does not ask — because asking has two costs an unarmed org must not pay:
	// a metered screen on its ledger, and the fail-CLOSED branch. A privileged
	// grant denied by a scorer that was never installed would be an outage, not a
	// defense, so arming is a decision an operator makes (and PUT /v1/gateway/config
	// refuses to arm an org while no scorer is installed).
	if g.mode(org) != edge.ModeLive {
		return g.watch(c, sig)
	}

	v := Decide(c.Context(), org, RiskQuery{
		Stage:      StageUsage,
		Subject:    RiskSubject{Kind: "session", ID: sig.Cred},
		Agency:     sig.Agency,
		Privileged: privileged,
		Signals: map[string]string{
			"credential": class,
			"ip":         sig.IP,
			"path":       sig.Path,
			"method":     c.Method(),
			"requests":   strconv.Itoa(p.Requests),
			"failures":   strconv.Itoa(p.Failures),
			"paths":      strconv.Itoa(p.Paths),
			"peers":      strconv.Itoa(p.Peers),
		},
	})
	if v.Agency != "" {
		sig.Agency = v.Agency // the scorer's lane is the authoritative one.
	}

	// The screen is surfaced on BOTH rails, because they answer different
	// questions and only one of them is live yet:
	//
	//	o11y     — Traffic.Screen counts it for the org from the first request,
	//	           whatever a screen costs, and GET /v1/gateway/traffic reports it.
	//	billing  — ResourceMeter puts it on the org's OWN usage ledger, the same
	//	           rail every other metered resource rides, so a plan's included
	//	           allowance and its overage are one ledger and not a second
	//	           billing path.
	//
	// Meter is a NO-OP while the screen is unpriced (AmountCents<=0), which is the
	// default: a price invented here would be a fabricated one, and the number
	// belongs to the pricing catalog. That is exactly why the count above does not
	// go through the ledger — an unpriced product must still be measurable.
	// Both only in live mode: a shadow screen is not a product the org bought.
	g.traffic.Screen(org, now)
	g.meter.Meter(org, principal.Project(c), "screen", g.cents, c.RequestID(), sig.IP)

	if !v.Allowed() {
		g.traffic.Hold(sig, edge.Hold{Action: v.Action, Reason: v.Cause, Decision: v.ID}, holdFor, now)
	}
	return g.enforce(c, sig, v)
}

// screen reports whether this request's pattern is worth a question. A privileged
// grant always is: the fail-closed branch only protects what it is asked about.
func (g *abuseGate) screen(p edge.Pattern, privileged bool) bool {
	return privileged ||
		p.Requests <= screenFirst ||
		p.Failures >= screenFailures ||
		p.Peers >= screenPeers ||
		p.Paths >= screenPaths
}

func (g *abuseGate) mode(org string) string {
	if g.policy == nil || org == "" {
		return edge.ModeShadow
	}
	return g.policy.Mode(org)
}

// watch runs the request unjudged, and still learns from its outcome. A 401/403
// is the signal that separates a client with a stale token from one guessing
// them, and it only exists after the handler has run.
func (g *abuseGate) watch(c *zip.Ctx, sig edge.Signal) error {
	err := c.Next()
	g.observeOutcome(c, sig, err)
	return err
}

// enforce applies a verdict. In shadow it applies NOTHING: it records what it
// would have done and lets the request through, which is what makes the mode
// switch a real one rather than a label.
func (g *abuseGate) enforce(c *zip.Ctx, sig edge.Signal, v RiskVerdict) error {
	if v.Allowed() {
		if v.Action == ActionReview {
			g.record(c, sig, v, "review")
		}
		return g.watch(c, sig)
	}
	if g.mode(sig.Org) != edge.ModeLive {
		g.record(c, sig, v, "shadow")
		return g.watch(c, sig)
	}

	g.record(c, sig, v, "enforced")
	g.traffic.Deny(sig.Org, time.Now())

	// The refusal is written in the fleet's own nested error contract, the same
	// bytes every other Hanzo surface refuses with, so a client has ONE error
	// shape to parse. The body names the decision id and never the score, the
	// features or the rule: an attacker must not be able to use the refusal as a
	// readout of the model that produced it.
	switch v.Action {
	case ActionChallenge:
		c.SetHeader("WWW-Authenticate", `Bearer error="step_up_required"`)
		return c.JSON(401, denyBody("step_up_required",
			"Additional verification is required for this request. "+ref(v)))
	case ActionRestrict:
		c.SetHeader("Retry-After", strconv.Itoa(int(holdFor.Seconds())))
		return c.JSON(429, denyBody("restricted",
			"This credential is temporarily restricted. "+ref(v)))
	default:
		return c.JSON(403, denyBody("refused",
			"This request was refused. "+ref(v)))
	}
}

// ref names the decision a refusal came from, so a customer can quote one id to
// support and support can fetch the whole judgement — its rules, its features
// and the model digest — from GET /v1/risk/decisions/{id}. Without an id the
// refusal is unappealable, which is not an acceptable property for a product
// that refuses payments and logins.
func ref(v RiskVerdict) string {
	if v.ID == "" {
		return "Contact support if this is unexpected."
	}
	return "Reference " + v.ID + "."
}

// observeOutcome feeds the response status back into the sensor. Only 401/403
// count: those are the outcomes that mean a credential did not work, which is the
// signature of stuffing and of a replayed stolen token.
func (g *abuseGate) observeOutcome(c *zip.Ctx, sig edge.Signal, err error) {
	if s := effectiveStatus(c.Fiber().Response().StatusCode(), err); s == 401 || s == 403 {
		g.traffic.Fail(sig, time.Now())
	}
}

// record surfaces a non-allow verdict to o11y. It is a LOG, not a second audit
// write: an enforced refusal is a 401/403 and AuditTrail already puts that in the
// tamper-evident trail, with the actor and the resource. Recording it twice would
// give one event two records that can disagree.
//
// A shadow verdict is the interesting one — it is the only evidence of what the
// gate WOULD do, and the only way stated-versus-realised can be measured before
// an org is armed.
func (g *abuseGate) record(c *zip.Ctx, sig edge.Signal, v RiskVerdict, outcome string) {
	if g.log == nil {
		return
	}
	g.log.Info("risk decision",
		"stage", StageUsage,
		"outcome", outcome,
		"action", v.Action,
		"org", sig.Org,
		"cred", sig.Cred,
		"agency", sig.Agency,
		"path", sig.Path,
		"method", c.Method(),
		"score", v.Score,
		"decision", v.ID,
		"cause", v.Cause,
		// refusal is empty on a scored verdict and names the failure otherwise, so
		// an allow that happened because nobody was listening is never read as a
		// clean result.
		"refusal", v.Refusal,
		"request", c.RequestID(),
	)
}
