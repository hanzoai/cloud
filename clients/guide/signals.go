package guide

import (
	"context"
	"strconv"
	"strings"
)

// signals.go is the Guide's real-time growth-OBSERVE layer: the cross-subsystem
// read seams the growth detectors need, the observed SignalSet, and the pure
// growth-stage classifier. It PRODUCES signals + a stage; it decides NOTHING about
// recommendations (that is a later surface engine) — a pure reader/classifier, so
// the observe layer stays decomplected from whatever consumes it.
//
// DECOMPLECT: guide imports NONE of the sibling subsystems it observes. Each
// cross-subsystem read is an injected func field on Signals, bound once at the
// composition root (apps/wire_seams.go) to a provably org-scoped, nil-safe read
// (framework.ModuleInstalled, integrations.Connected, …) — exactly the seam
// pattern clients/coding uses for git's CloneURL/VerifyRef. A nil field
// honest-degrades its signal to "not present": the vocabulary is the contract, a
// read fills in the day its seam is bound, so a subsystem that is down or not yet
// wired can NEVER spuriously satisfy a signal.

// Signals are the org-scoped, cross-subsystem reads the growth observe layer
// composes. Every func takes the caller's VALIDATED org and reads ONLY that org's
// state; a signal is a boolean / a threshold / the org's OWN number — never a
// secret and never another org's data.
type Signals struct {
	// ModuleInstalled reports whether org has installed the named framework module
	// (framework.ModuleInstalled) — e.g. "cms", "erp".
	ModuleInstalled func(ctx context.Context, org, module string) bool
	// ConnectorPresent reports whether org has connected the named integration
	// provider — a BOOLEAN "is connected" (integrations.Connected), NEVER the token.
	ConnectorPresent func(ctx context.Context, org, provider string) bool
	// HasDeployment reports whether org has a live deployment.
	HasDeployment func(ctx context.Context, org string) (bool, error)
	// RevenueCents is org's OWN recorded revenue-of-record in minor units. The
	// number stays in the org's own profile; the signal it satisfies is revenue > 0.
	RevenueCents func(ctx context.Context, org string) (int64, error)
	// RecordCount is a count of org's OWN business records — the data-volume signal.
	RecordCount func(ctx context.Context, org string) (int64, error)
}

// boundSignals is the process-wide seam set, installed once at the composition
// root via BindSignals before cloud.Serve mounts guide. The zero value (all nil)
// honest-degrades every growth signal to "not present" — the state guide's own
// tests and a minimally-wired deployment both observe.
var boundSignals Signals

// BindSignals installs the cross-subsystem reads. The composition root
// (apps/wire_seams.go) calls it once at init, before guide.Mount. Last write wins;
// guide never mutates it after mount.
func BindSignals(s Signals) { boundSignals = s }

// Growth signal names — the observed vocabulary. The fixed (no-parameter) signals
// are constants; the parameterized families (module:<name>, connected:<provider>,
// funnel:<stage>, customers:>=<N>) are a KIND plus a param. These strings are the
// ONE contract the profile emits, the classifier reads, and a curriculum step may
// name as its done-signal.
const (
	SignalAnalytics      = "analytics"       // the analytics warehouse is emitting for the org
	SignalDeployed       = "deployed"        // the org has a live deployment
	SignalRevenue        = "revenue"         // the org has recorded revenue > 0
	SignalCustomers      = "customers"       // the org's own record count crossed the data-volume threshold
	SignalFunnelVisitors = "funnel:visitors" // the funnel has visitors
	SignalFunnelSignups  = "funnel:signups"  // the funnel has signups
	SignalFunnelOrders   = "funnel:orders"   // the funnel has orders
)

// Signal KINDS — the prefix of a parameterized growth signal. reconcile resolves a
// step's "<kind>:<param>" signal to the kind's detector (lookupDetector), which
// reads its param from the signal string, so ONE registered detector serves every
// param. customers is BOTH a bare signal and a kind (customers:>=<N>).
const (
	kindModule    = "module"
	kindConnected = "connected"
	kindFunnel    = "funnel"
	kindCustomers = "customers"
)

// customersThreshold is the default data-volume bar the customers signal crosses —
// the org has a real book of records, not a handful of test rows. A curriculum step
// may override it with "customers:>=<N>".
const customersThreshold = 25

// standardModules / standardConnectors are the curated sets the profile probes for
// its SignalSet. An unregistered name honest-degrades to false through the seams, so
// the vocabulary is safe to carry ahead of a lane/provider landing. The connector set
// includes the providers the strategies corpus joins against (facebook/google-ads/
// mailchimp/sms — the Guide's connect:<provider> steps), so a `has:<capability>` tag
// resolves to a real observed signal (strategies.go hasSignal); an org that never
// connected a provider reads false, never a spurious true.
var (
	standardModules    = []string{"cms", "erp", "kb", "help"}
	standardConnectors = []string{"stripe", "shopify", "facebook", "google-ads", "mailchimp", "sms"}
)

// SignalSet is the observed growth facts about ONE org: signal name → present. It is
// a set of BOOLEANS / thresholds — never a secret, never another org's data. The
// profile emits it and classifyStage folds it into a Stage.
type SignalSet map[string]bool

// Stage is an org's observed growth stage — a first-principles ladder from
// nothing-observed to money-flowing. It is a pure function of the SignalSet.
type Stage string

const (
	StageFormed    Stage = "formed"    // the org exists; nothing else is observed yet
	StageLaunched  Stage = "launched"  // a live presence: a deployment is up or analytics is emitting
	StageActivated Stage = "activated" // real engagement: people are signing up or a book of records exists
	StageScaling   Stage = "scaling"   // money of record: revenue is recorded or orders are completing
)

// stageRung is one rung of the growth ladder: a stage and the predicate over the
// observed signals that ADMITS it. The ladder is DATA, not branching — each rung is
// an OUTCOME (presence, engagement, money), never a setup step, so the stage tracks
// what the org has ACHIEVED, not what it has merely configured (installed modules /
// connected providers are observed context, not stage drivers).
type stageRung struct {
	Stage Stage
	Admit func(SignalSet) bool
}

// stageLadder is ordered low→high. classifyStage returns the HIGHEST rung whose
// predicate holds (money-of-record reads as scaling even if an earlier rung's signal
// was not observed — the honest floor is the strongest evidence present),
// defaulting to formed when nothing is observed.
var stageLadder = []stageRung{
	{StageLaunched, func(s SignalSet) bool { return s[SignalDeployed] || s[SignalAnalytics] }},
	{StageActivated, func(s SignalSet) bool { return s[SignalFunnelSignups] || s[SignalCustomers] }},
	{StageScaling, func(s SignalSet) bool { return s[SignalRevenue] || s[SignalFunnelOrders] }},
}

// classifyStage folds an observed SignalSet into the org's growth stage. Pure and
// total: no I/O, every SignalSet maps to exactly one Stage, so it is exhaustively
// unit-testable and can never run an effect.
func classifyStage(s SignalSet) Stage {
	stage := StageFormed
	for _, rung := range stageLadder {
		if rung.Admit(s) {
			stage = rung.Stage
		}
	}
	return stage
}

// profileMetrics are the org's OWN growth numbers — never another org's. Funnel is
// the analytics read (visitors/signups/orders/revenue); RevenueCents is the
// money-of-record; Records is the org's business-record count; LaunchProgress is the
// checklist completion. The observe layer fills the growth numbers; the profile
// handler fills LaunchProgress from the reconciled snapshot.
type profileMetrics struct {
	Funnel         Funnel       `json:"funnel"`
	RevenueCents   int64        `json:"revenueCents"`
	Records        int64        `json:"records"`
	LaunchProgress progressView `json:"launchProgress"`
}

// observe runs the growth signal probes against the org's real, CURRENT state and
// returns the observed SignalSet plus the org's own key metrics. Every probe is
// org-scoped (each seam reads only `org`) and honest-degrading: a nil seam or a
// probe error yields an ABSENT (false) signal, never a spurious true and never a
// failed profile — the profile degrades signal-by-signal. Pure I/O orchestration;
// the fold into a Stage is classifyStage (pure).
func observe(ctx context.Context, org string, sig Signals, funnel Funnel) (SignalSet, profileMetrics) {
	set := SignalSet{}
	m := profileMetrics{Funnel: funnel}

	// Analytics + funnel stages from the single org-scoped funnel read.
	set[SignalAnalytics] = funnel.Available
	set[SignalFunnelVisitors] = funnelStagePresent(funnel, "visitors")
	set[SignalFunnelSignups] = funnelStagePresent(funnel, "signups")
	set[SignalFunnelOrders] = funnelStagePresent(funnel, "orders")

	// Live deployment (boolean seam).
	set[SignalDeployed] = probeBool(ctx, org, sig.HasDeployment)

	// Revenue-of-record: read the org's OWN number once, derive the boolean. The
	// signal is always emitted (false when the seam is unbound or errors) so the
	// profile has a stable shape — honest-degrading is a false signal, not a missing key.
	set[SignalRevenue] = false
	if sig.RevenueCents != nil {
		if cents, err := sig.RevenueCents(ctx, org); err == nil {
			m.RevenueCents = cents
			set[SignalRevenue] = cents > 0
		}
	}
	// Data volume: the org's OWN record count, thresholded (same stable-shape rule).
	set[SignalCustomers] = false
	if sig.RecordCount != nil {
		if n, err := sig.RecordCount(ctx, org); err == nil {
			m.Records = n
			set[SignalCustomers] = n >= customersThreshold
		}
	}
	// Installed modules + connected providers (boolean seams).
	for _, mod := range standardModules {
		set[kindModule+":"+mod] = sig.ModuleInstalled != nil && sig.ModuleInstalled(ctx, org, mod)
	}
	for _, p := range standardConnectors {
		set[kindConnected+":"+p] = sig.ConnectorPresent != nil && sig.ConnectorPresent(ctx, org, p)
	}
	return set, m
}

// probeBool runs an honest-degrading boolean seam: a nil seam or an error yields
// false (not present), never a spurious true.
func probeBool(ctx context.Context, org string, fn func(context.Context, string) (bool, error)) bool {
	if fn == nil {
		return false
	}
	v, err := fn(ctx, org)
	return err == nil && v
}

// funnelStagePresent is the ONE predicate for "does the funnel show <stage>", shared
// by observe and the funnel detector (DRY) so both read a funnel stage identically.
// An unavailable funnel (unreachable/empty warehouse) is not-present for every
// stage — honest-degrading, never a fabricated true.
func funnelStagePresent(f Funnel, stage string) bool {
	if !f.Available {
		return false
	}
	switch stage {
	case "visitors":
		return f.Visitors > 0
	case "signups":
		return f.Signups > 0
	case "orders":
		return f.Orders > 0
	case "revenue":
		return f.Revenue > 0
	default:
		return false
	}
}

// parseThreshold reads the ">=N" tail of a "customers:>=N" signal, returning def when
// the signal carries no explicit threshold. e.g. "customers" → def,
// "customers:>=250" → 250. A malformed or negative threshold falls back to def.
func parseThreshold(signal string, def int64) int64 {
	_, param, ok := strings.Cut(signal, ":")
	if !ok {
		return def
	}
	param = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(param), ">="))
	n, err := strconv.ParseInt(param, 10, 64)
	if err != nil || n < 0 {
		return def
	}
	return n
}
