package cloud

// The scorer client — the ONE entry point from anywhere in cloud to /v1/risk.
//
// /v1/risk is the platform's scoring and decision plane: it judges an entity at
// a lifecycle moment and answers with an action. Everything that DEFENDS a
// lifecycle moment — the abuse gate on the API plane here, the signup gate in
// hanzoai/iam over the wire — asks that one scorer. None of them scores. A
// second scorer would be a second answer to one question, and the two would
// disagree silently, which is the failure mode a risk product cannot have.
//
// The client inverts the import — package cloud cannot import an app, since every
// app imports cloud — so the app that owns /v1/risk hands its scoring function to
// the core at mount and the core calls it through a nil-safe accessor. That is
// what makes the gate below complete and testable before a scorer exists, and
// correct when it is absent at run time.
//
// IT IS AN IN-PROCESS HANDOFF, and which apps share a process is a per-plugin
// CHOICE rather than a law. Each plugin/<name> is a composition root that links
// whatever it imports: plugin/risk lists risk alone and plugin/gateway lists gateway
// alone, so as composed today no binary holds both, and a scorer installed here
// would arm the risk process while apps/gateway's RiskScorerInstalled — running in
// the gateway binary — stayed false. Sibling apps DO co-reside wherever a root asks
// them to: plugin/campaigns, plugin/integrations and plugin/guide each link three or
// four apps/* and wire process-global clients across them (clients.go in each), which is
// this client's shape exactly, under no build tag. One import in one composition root
// is the whole distance between the two arrangements, so read "one process per app"
// as the current composition and never as an impossibility.
//
// WHAT HOLDS WHATEVER THE TOPOLOGY IS SIMPLER: apps/risk exports Mount and Shutdown,
// and nothing else. There is no scoring function to install, so SetRiskScorer has no
// producer because none can be spelled — not because a boundary forbids one. Giving
// it one means EXPORTING a scorer from apps/risk and then deciding what it answers
// for: this global answers for its own process, while arming asks whether the risk
// plane can answer for the FLEET. That is a cross-process ask. The observability
// plane's event endpoint was the same shape and learned it the expensive way
// (cloud.SetObsErrorIngest, in a file named obsevents.go, read nil in the process
// that needed it and answered 503 to every Sentry SDK until apps/o11y/obs_rpc.go
// replaced it with a plane op; obsevents.go is gone).
//
// So a caller in another binary — the abuse gate that serve.go installs fleet-wide,
// the signup gate in hanzoai/iam — reaches /v1/risk over the wire or over the plane,
// never through this global. Note what that means for the tests below: they link
// cloud and the app into ONE binary, where the handoff always works, so no test here
// can observe the composition that decides whether it works in production.
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

// Severity ranks the vocabulary above, and it exists so that TWO judgements about
// one event compose into one answer: the severest of them stands.
//
// It is here rather than at the one gate that fuses today because the ordering IS
// a property of the vocabulary — the constants are declared "most permissive
// first" and this makes that sentence executable, in the same file, so the prose
// and the code cannot drift into two orderings.
//
// AN UNRECOGNISED ACTION RANKS BELOW ALLOW. It is not a milder verdict; it is a
// string this vocabulary does not contain, and letting one place anywhere in the
// order would let a typo win a fusion and become the outcome. Ranking it lowest
// means the judgement that IS recognised decides, and the fail policy — which is
// [Decide]'s, not this function's — is what answers for the unrecognised one.
func Severity(action string) int {
	switch action {
	case ActionAllow:
		return 0
	case ActionReview:
		return 1
	case ActionChallenge:
		return 2
	case ActionRestrict:
		return 3
	case ActionBlock:
		return 4
	}
	return -1
}

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
	// RefusalStuck — the scorer holds every slot and has not returned from ANY
	// call for longer than a stall. It is installed and it is not answering, which
	// is a different fact from busy: busy clears in microseconds, this does not
	// clear at all.
	RefusalStuck = "scorer-stuck"
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
	// Shape is the model SPACE the verdict was reached in, as `<family>:<digest>`.
	// It is what pins an adverse decision to a model — a score is only meaningful
	// against the space that produced it, and "the model that was running" is not
	// an answer to which model decided this. Empty on an unscored answer, which has
	// no space behind it.
	Shape string `json:"shape,omitempty"`
	// Policy is the version of the organisation's decision regime this verdict was
	// reached under. The threshold is derived from the appetite that version
	// states, so it is the record that makes the decision reconstructible after the
	// appetite is restated. Zero means no regime was ever stated and the default
	// posture — shadow — was in force.
	Policy int `json:"policy,omitempty"`
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
	return decide(scorerCalls, ctx, org, q)
}

// decide is Decide with its ceiling passed in — the whole of the logic, over the
// one piece of process state it holds. Decide supplies the process ceiling; a
// test supplies its own, so the bound is asserted without mutating a global that
// another test's parked goroutine is still reading.
func decide(sem *calls, ctx context.Context, org string, q RiskQuery) RiskVerdict {
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
		// FULL is not the same fact as STOPPED. A healthy scorer returns in
		// microseconds, so every slot being held means either a burst (which clears
		// before the caller could retry) or a scorer that has stopped returning at
		// all — and the second one never clears, because a slot is released by the
		// goroutine that holds it. Told apart by when a call last came back.
		if sem.stalled(time.Now()) {
			return riskUnavailable(q, RefusalStuck)
		}
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
		v, err := func() (v RiskVerdict, err error) {
			// A panicking scorer is a scorer that did not answer. Contained here
			// so a model bug cannot take down the request path.
			defer func() {
				if r := recover(); r != nil {
					err = errScorerPanic
				}
			}()
			return fn(ctx, org, q)
		}()

		// The slot is released by the goroutine that holds it, when the scorer
		// actually returns — NOT when the budget expires. Releasing it at the
		// timeout would let the ceiling be exceeded without bound by exactly the
		// scorer it exists to contain. It goes back BEFORE the answer is handed
		// over, so holding an answer means holding a released slot: the ceiling
		// then counts calls that are still out, and never one that already came
		// back. The return is also the health signal: a scorer that keeps
		// returning is busy, one that never returns is stuck.
		sem.give()
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

// ScorerStall is how long every slot may be held with nothing coming back before
// the scorer is called stuck rather than busy. Twenty budgets: far past any burst
// a healthy scorer produces, and reached in seconds by one that has deadlocked.
const ScorerStall = 20 * RiskBudget

// calls is the ceiling on asking, and the ONE record of whether asking still
// works: how many questions are in flight, and when an answer last came back.
// The two facts live together because neither one alone can tell a burst from a
// deadlock, and that difference decides whether a privileged grant waits or
// proceeds.
type calls struct {
	// slots is a counting semaphore over a buffered channel: take never blocks (a
	// full channel means "no room", which is an answer), give always succeeds
	// because only a taker gives.
	slots chan struct{}
	// last is the unix-nano time a scorer call last RETURNED. Stamped when a
	// scorer is installed, so a scorer that saturates immediately is busy rather
	// than born stuck.
	last atomic.Int64
}

func newCalls(n int) *calls {
	c := &calls{slots: make(chan struct{}, n)}
	c.answered(time.Now())
	return c
}

// scorerCalls is the process ceiling.
var scorerCalls = newCalls(MaxScorerCalls)

func (c *calls) take() bool {
	select {
	case c.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (c *calls) give() {
	c.answered(time.Now())
	<-c.slots
}

func (c *calls) answered(at time.Time) { c.last.Store(at.UnixNano()) }

// stalled reports that nothing has come back for longer than ScorerStall. Only
// asked when every slot is held: a quiet deployment has an old timestamp and is
// not stuck, it is unasked.
func (c *calls) stalled(now time.Time) bool {
	return now.UnixNano()-c.last.Load() > int64(ScorerStall)
}

// errScorerPanic is the error a contained panic reports as. Unexported and
// never returned to a caller — Decide converts it to a refusal like any other.
var errScorerPanic = errScorer("the scorer panicked")

type errScorer string

func (e errScorer) Error() string { return string(e) }

// riskUnavailable is THE fail policy, in one place — and it turns on TWO facts,
// not one. The second is what keeps it a defense rather than an outage.
//
//	privileged — the request grants standing authority, so silence must deny.
//	answering  — there IS a scorer here and it is returning answers. Every
//	             refusal except two means exactly that: it exists and it did not
//	             answer THIS question, which is when a grant must wait.
//
// The two exemptions are the deployments where there is no judge at all:
//
//	absent — no scorer was ever installed in this process, or one withdrew (which
//	         is how a degraded scorer is meant to step down). Refusing to mint a
//	         credential or read a secret because a component is not deployed is
//	         not security, it is a product that cannot be operated.
//	stuck  — every slot is held and nothing has come back for a stall. A slot is
//	         released by the goroutine holding it, so a deadlocked scorer holds
//	         them forever: without this, one hung goroutine would 403 every armed
//	         org's key store until someone restarted the pod, and no operator
//	         could tell that apart from the control working as designed.
//
// This is the same rule hanzoai/iam applies at its own signup gate with a
// different arming signal. Two mechanisms, one semantic: fail closed once there
// is something to fail closed on, allow before.
//
// Neither exemption is silent. Every verdict carries the Refusal that produced
// it, the gate logs it, and Traffic.Screen counts the unanswered screens on the
// org's own report — so "allowed" and "allowed because nobody was listening" are
// never the same row.
func riskUnavailable(q RiskQuery, why string) RiskVerdict {
	if q.Privileged && answering(why) {
		return RiskVerdict{Action: ActionBlock, Agency: q.Agency, Refusal: why}
	}
	return RiskVerdict{Action: ActionAllow, Agency: q.Agency, Refusal: why}
}

// RiskUnavailable is that same policy, for a scorer that has to state one fact
// [Decide] cannot observe from here.
//
// A scorer reached over the plane learns something the client does not: whether the
// app it asks is PART OF THIS DEPLOYMENT. Over a socket, "not deployed" arrives as
// a failed call like any other — and read as an error it would take the
// fail-CLOSED branch, so a fleet that simply does not run the risk app would
// refuse every privileged grant in it. That is the outage the absent exemption
// exists to prevent, and only the caller of the plane can tell it apart from a
// peer that is here and silent.
//
// So the scorer states the refusal and this applies the ONE policy to it, rather
// than a second copy of the rule appearing at the one client that needs it most.
// Every other refusal stays [Decide]'s to determine.
func RiskUnavailable(q RiskQuery, why string) RiskVerdict { return riskUnavailable(q, why) }

// answering reports whether a refusal came from a scorer that is present and
// returning — the fact that separates "the judge is out today" from "the judge
// did not answer this one".
func answering(why string) bool { return why != RefusalAbsent && why != RefusalStuck }

// Facts drops the empty values from a signal map, because a fact we do not have
// must be ABSENT rather than empty. An empty string is a VALUE: a scorer keying
// velocity on "ip" would group every request whose address never arrived — which,
// behind a load balancer that does not pass the peer, is all of them — into one
// very busy caller and refuse the lot. "We do not know" and "it is the empty
// string" are different answers and only one of them is true.
//
// Stated once, at the client every question passes through, so no gate has to
// remember it.
func Facts(m map[string]string) map[string]string {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	return m
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
		"/v1/iam/tokens/issue",
		"/v1/iam/keys/mint",
		"/v1/iam/keys/revoke",
		"/v1/iam/admin/provision",
		"/v1/iam/signup",
		"/v1/iam/onboard",
		"/v1/kms/",
	}
	grantMutations = []string{
		"/v1/admin/",
		"/v1/iam/",
		"/v1/account/orgs",
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
