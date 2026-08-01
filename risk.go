package cloud

// The scorer seam — the ONE door from anywhere in cloud to /v1/risk.
//
// /v1/risk is the platform's scoring and decision plane: it judges an entity at
// a lifecycle moment and answers with an action. Everything that DEFENDS a
// lifecycle moment — the abuse gate on the API plane here, the signup gate in
// hanzoai/iam over the wire — asks that one scorer. None of them scores. A
// second scorer would be a second answer to one question, and the two would
// disagree silently, which is the failure mode a risk product cannot have.
//
// The seam is installed the way the observability plane installs its claim on
// the event door (obsevents.go): the app that owns /v1/risk hands its scoring
// function to the core at mount, and the core calls it through a nil-safe
// accessor. That inverts the import — package cloud cannot import an app, since
// every app imports cloud — and it means the gate below is complete and testable
// before the scorer exists, and stays correct when it is absent at run time.
//
// THE FAIL POLICY LIVES HERE, IN ONE FUNCTION, AND NOWHERE ELSE.
//
//	ordinary path   — a scorer that is absent, erroring, timing out or silent
//	                  ALLOWS. A risk plane that is down must not be able to take
//	                  the product down with it. Login keeps working.
//	privileged grant — the same conditions BLOCK. A grant that hands out standing
//	                  authority (a new tenant, a credential, an elevation) is not
//	                  a request to be waved through because the judge is out; the
//	                  caller can retry in a minute.
//
// Both answers carry a Refusal naming why, so an unscored allow is never
// mistaken for a clean result. Silence must not read as innocence — the same
// doctrine the anomaly engine applies to its own refusals.

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// The lifecycle stages a decision can be asked at. A stage selects the feature
// window and the rule set on the scorer's side; it never selects a different
// tenant gate.
const (
	// StageSignup is registration: is this account real, and is it one account?
	StageSignup = "signup"
	// StageUsage is the API/usage plane: is this traffic the customer's, and is
	// it being used the way a customer uses it?
	StageUsage = "usage"
	// StagePayment is authorization time on a transaction.
	StagePayment = "payment"
)

// The actions a scorer can return, most permissive first. They are the whole
// vocabulary: a caller that receives anything else treats it as unrecognised and
// applies the fail policy, rather than guessing.
const (
	// ActionAllow proceeds.
	ActionAllow = "allow"
	// ActionReview proceeds and summons a person. A statistical judgement may
	// reach here and no further on its own.
	ActionReview = "review"
	// ActionChallenge proceeds only after the caller proves something more.
	ActionChallenge = "challenge"
	// ActionRestrict proceeds at a reduced ceiling.
	ActionRestrict = "restrict"
	// ActionBlock does not proceed.
	ActionBlock = "block"
)

// The reasons an answer is not a scored one. A caller that logs, audits or
// reports an outcome reports this beside it, so "allowed" and "allowed because
// nobody was listening" are never the same row.
const (
	// RefusalAbsent — no scorer is installed in this process.
	RefusalAbsent = "scorer-absent"
	// RefusalError — the scorer returned an error.
	RefusalError = "scorer-error"
	// RefusalTimeout — the scorer did not answer inside the budget.
	RefusalTimeout = "scorer-timeout"
	// RefusalSilent — the scorer answered with no action.
	RefusalSilent = "scorer-silent"
	// RefusalUnknown — the scorer answered with an action outside the vocabulary.
	RefusalUnknown = "scorer-unknown"
	// RefusalBusy — the scorer was already answering as many questions at once as
	// it is allowed to. The question was not asked.
	RefusalBusy = "scorer-busy"
)

// RiskBudget bounds how long a decision may take. The gate sits on the request
// path, so a scorer that hangs must not hang the product: past the budget the
// answer is the fail policy's, not the scorer's. 150ms is generous for an
// in-process half-space-tree score and tight enough to be invisible next to any
// real handler.
const RiskBudget = 150 * time.Millisecond

// RiskSubject names WHAT is being judged. Kind is the entity class — account,
// transaction, session, agent, merchant, payout — and ID is its identity within
// the tenant. The tenant itself is never in here: it is the org argument, and it
// comes from the validated principal.
type RiskSubject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// RiskQuery is one question for the scorer.
type RiskQuery struct {
	// Stage is the lifecycle moment.
	Stage string `json:"stage"`
	// Subject is the entity being judged.
	Subject RiskSubject `json:"subject"`
	// Agency is the caller's lane as the asking gate classed it — agent, human,
	// bot or unknown. It is a SIGNAL, not an assertion: the scorer may overrule it
	// on facts the gate does not hold, and its answer is the one that counts.
	Agency string `json:"agency,omitempty"`
	// Signals are the facts the gate observed: ip, path spread, failure count,
	// credential class, and whatever else the stage cares about. Free-form because
	// the vocabulary belongs to the scorer's feature inventory, not to the gate.
	Signals map[string]string `json:"signals,omitempty"`
	// Privileged marks a grant of standing authority — a new tenant, a credential,
	// an elevation. It selects the FAIL-CLOSED branch: silence denies. It is set by
	// the gate from the request it is judging, never by the caller being judged.
	Privileged bool `json:"-"`
}

// RiskVerdict is the answer.
type RiskVerdict struct {
	// ID is the scorer's decision id, the handle a decision record is fetched by.
	ID string `json:"id,omitempty"`
	// Action is what to do, from the vocabulary above.
	Action string `json:"action"`
	// Score is the weight of evidence in [0,1].
	Score float64 `json:"score,omitempty"`
	// Agency is the lane the scorer settled on — the authoritative one.
	Agency string `json:"agency,omitempty"`
	// Cause is the scorer's short reason, for the audit record.
	Cause string `json:"cause,omitempty"`
	// Refusal names why this is not a scored answer, and is empty when it is one.
	Refusal string `json:"refusal,omitempty"`
}

// Scored reports whether a verdict came from the scorer rather than the fail
// policy. Every recorder asks this before treating an allow as evidence.
func (v RiskVerdict) Scored() bool { return v.Refusal == "" }

// Allowed reports whether the verdict lets the request proceed unchanged.
// Review proceeds too — it summons a person, it does not stop traffic.
func (v RiskVerdict) Allowed() bool { return v.Action == ActionAllow || v.Action == ActionReview }

// RiskScorer is the scoring function /v1/risk installs. org is the SERVER-resolved
// tenant; a scorer never re-derives identity, and never answers for a tenant it
// was not asked about.
type RiskScorer func(ctx context.Context, org string, q RiskQuery) (RiskVerdict, error)

// riskScorer holds the installed scorer. atomic.Value rather than a bare var
// because Mount runs on one goroutine and the request path reads on all of them.
var riskScorer atomic.Value // RiskScorer

// SetRiskScorer installs the ONE scorer. Called by the app that owns /v1/risk
// when its model and stores are ready. Installing nil uninstalls it, which is how
// a degraded scorer withdraws rather than answering badly.
func SetRiskScorer(fn RiskScorer) {
	if fn == nil {
		riskScorer.Store(RiskScorer(nil))
		return
	}
	riskScorer.Store(fn)
}

// RiskScorerInstalled reports whether a scorer is available — for a health probe
// or a report, never as a gate. The gate is Decide, which handles absence itself.
func RiskScorerInstalled() bool { return loadRiskScorer() != nil }

func loadRiskScorer() RiskScorer {
	fn, _ := riskScorer.Load().(RiskScorer)
	return fn
}

// Decide asks the scorer and applies the fail policy. It is the ONLY way cloud
// code reaches /v1/risk, so the policy is stated once and cannot drift between
// the gates that depend on it.
//
// It never returns an error. A gate needs an action, and "I could not tell you"
// IS an action — stated by q.Privileged and carried in Refusal.
func Decide(ctx context.Context, org string, q RiskQuery) RiskVerdict {
	return decide(scorerSlots, ctx, org, q)
}

// decide is Decide with its ceiling passed in — the whole of the logic, over the
// one piece of process state it holds. Decide supplies the process ceiling; a
// test supplies its own, so the bound is asserted without mutating a global that
// another test's parked goroutine is still reading.
func decide(sem semaphore, ctx context.Context, org string, q RiskQuery) RiskVerdict {
	fn := loadRiskScorer()
	if fn == nil {
		return riskUnavailable(q, RefusalAbsent)
	}

	// BOUNDED, always. Every ask costs a goroutine that lives until the scorer
	// returns — which, for a scorer stuck on a lock, a model load or a stalled
	// socket, is longer than the budget below. Without a ceiling those goroutines
	// accumulate one per screened request for as long as the stall lasts, so a
	// slow scorer becomes an out-of-memory in the process it was installed to
	// protect. Past the ceiling the question is not asked at all and the fail
	// policy answers, which is the same answer a timeout gives and reaches it
	// without allocating anything.
	if !sem.take() {
		return riskUnavailable(q, RefusalBusy)
	}

	// The budget is the gate's, not the scorer's: a scorer that ignores its
	// context must still not hold the request. The answer is taken from whichever
	// arrives first, and a late one is discarded by a buffered channel with no
	// goroutine left blocked on it.
	ctx, cancel := context.WithTimeout(ctx, RiskBudget)
	defer cancel()
	type answer struct {
		v   RiskVerdict
		err error
	}
	ch := make(chan answer, 1)
	go func() {
		// The slot is released by the goroutine that holds it, when the scorer
		// actually returns — NOT when the budget expires. Releasing it at the
		// timeout would let the ceiling be exceeded without bound by exactly the
		// scorer it exists to contain.
		defer sem.give()
		defer func() {
			// A panicking scorer is a scorer that did not answer. Contained here
			// so a model bug cannot take down the request path.
			if r := recover(); r != nil {
				ch <- answer{err: errScorerPanic}
			}
		}()
		v, err := fn(ctx, org, q)
		ch <- answer{v, err}
	}()

	select {
	case <-ctx.Done():
		return riskUnavailable(q, RefusalTimeout)
	case a := <-ch:
		switch {
		case a.err != nil:
			return riskUnavailable(q, RefusalError)
		case a.v.Action == "":
			return riskUnavailable(q, RefusalSilent)
		case !riskActionKnown(a.v.Action):
			return riskUnavailable(q, RefusalUnknown)
		}
		return a.v
	}
}

// MaxScorerCalls is how many questions may be in flight at once, process-wide.
// It is a bound on the COST of asking, not a rate limit: a healthy in-process
// score returns in microseconds, so this ceiling is never reached by real load —
// it is reached only when the scorer has stopped answering, which is exactly when
// asking it again is worthless.
const MaxScorerCalls = 256

// scorerSlots is the ceiling. A counting semaphore over a buffered channel: take
// never blocks (a full channel means "busy", and busy is an answer), give always
// succeeds because only a taker gives.
var scorerSlots = slots(MaxScorerCalls)

type semaphore chan struct{}

func slots(n int) semaphore { return make(semaphore, n) }

func (s semaphore) take() bool {
	select {
	case s <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s semaphore) give() { <-s }

// errScorerPanic is the error a contained panic reports as. Unexported and
// never returned to a caller — Decide converts it to a refusal like any other.
var errScorerPanic = errScorer("the scorer panicked")

type errScorer string

func (e errScorer) Error() string { return string(e) }

// riskUnavailable is THE fail policy. Two lines, one place: a privileged grant
// denies, everything else proceeds, and both say why.
func riskUnavailable(q RiskQuery, why string) RiskVerdict {
	if q.Privileged {
		return RiskVerdict{Action: ActionBlock, Agency: q.Agency, Refusal: why}
	}
	return RiskVerdict{Action: ActionAllow, Agency: q.Agency, Refusal: why}
}

func riskActionKnown(a string) bool {
	switch a {
	case ActionAllow, ActionReview, ActionChallenge, ActionRestrict, ActionBlock:
		return true
	}
	return false
}

// grantPaths are the surfaces that hand out STANDING AUTHORITY — a credential, a
// tenant, an elevation, or the contents of the key store. A request to one of
// these takes the FAIL-CLOSED branch: when the scorer cannot answer, it is
// refused rather than waved through.
//
// Two lists rather than one predicate, because the two have different reasons:
//
//	grantPaths     — every method. Reading a secret with a stolen key IS the
//	                 attack, so a GET here is as much a grant as a POST.
//	grantMutations — mutations only. A SuperAdmin READING an admin surface is
//	                 audited and access-controlled already; it is a WRITE that
//	                 changes who can do what.
//
// Data rather than logic, so the list of things that fail closed can be read and
// reviewed in one place instead of inferred from handlers.
var (
	grantPaths = []string{
		"/v1/iam/issue-user-token",
		"/v1/iam/mint-user-keys",
		"/v1/iam/revoke-user-keys",
		"/v1/iam/admin/provision",
		"/v1/iam/signup",
		"/v1/iam/onboard",
		"/v1/kms/",
	}
	grantMutations = []string{
		"/v1/admin/",
		"/v1/iam/",
		"/v1/orgs/",
	}
)

// RoutePath is a request path in the form THE ROUTER MATCHES IT, and it is the
// only form a security comparison may use.
//
// fiber resolves a route against its "detection path": the request path
// lower-cased (CaseSensitive is off) with trailing slashes stripped
// (StrictRouting is off). c.Path() is the raw spelling the client sent. So
// `/V1/KMS/secret` and `/v1/kms/` reach exactly the handlers `/v1/kms/...` and
// `/v1/kms` do, while a prefix test over the raw path matches neither — one
// capital letter turned a fail-CLOSED grant surface into a fail-OPEN one.
//
// The rule here is the ROUTER'S rule, not an approximation of it: any other
// normalization would be a second opinion about what a path means, and the
// router's is the one that decides which handler runs. Percent-encoding is
// deliberately NOT decoded, for the same reason — fiber does not decode it
// either (UnescapePath is off), so `/v1/%6bms` routes nowhere and is not a
// bypass; decoding it here would make this function match a route that does not
// exist.
func RoutePath(path string) string {
	path = strings.ToLower(path)
	for len(path) > 1 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	return path
}

// underPrefix reports whether a NORMALIZED path is at or below prefix, on
// SEGMENT boundaries. Both sides are compared slash-terminated, so "/v1/kms"
// matches the "/v1/kms/" subtree (the router treats the two as one route) while
// "/v1/kmsx" does not match either — a prefix test on the bare strings would
// have said yes to the second, which is a rule about spelling rather than about
// routes.
func underPrefix(path, prefix string) bool {
	prefix = strings.TrimSuffix(prefix, "/")
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// Probe reports whether a request is a liveness/readiness check. A probe is
// never a grant and is never screened — a health endpoint a risk decision can
// fail is not a health endpoint, and a kubelet is not a customer.
//
// ONE predicate, read by both the gate's exemption and Privileged below, so the
// two can never disagree about what a probe is. Read-only methods only: a POST
// to something ending in "/health" is not a probe, so an attacker cannot name a
// mutating route into the exemption.
//
// path is normalized here rather than by the caller, so a caller cannot forget.
func Probe(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	path = RoutePath(path)
	switch path {
	case "/health", "/healthz", "/readyz", "/livez", "/metrics":
		return true
	}
	return strings.HasSuffix(path, "/health") || strings.HasSuffix(path, "/healthz")
}

// Privileged reports whether a request grants standing authority, and therefore
// whether the scorer's silence must deny it.
//
// It normalizes the path FIRST and compares nothing before it has. That order is
// the whole fix: the grant lists below describe ROUTES, and a route is what the
// router says it is.
func Privileged(method, path string) bool {
	if Probe(method, path) {
		return false
	}
	path = RoutePath(path)
	for _, p := range grantPaths {
		if underPrefix(path, p) {
			return true
		}
	}
	if !mutating(method) {
		return false
	}
	for _, p := range grantMutations {
		if underPrefix(path, p) {
			return true
		}
	}
	return false
}

func mutating(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}
