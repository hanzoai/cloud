package risk

// typed.go is the CONTRACT. Every operation this app serves is declared here as
// a zip typed op, and that one declaration is the whole of it: the REST route,
// the OpenAPI operation, the MCP tool, the CLI command and every generated SDK
// method are all projections of the same registry entry. An untyped route
// appends nothing to that registry and is therefore invisible to all five.
//
// TWO OPERATIONS ARE NOT HERE, both wire-bound, both named in
// typed_wire_test.go's closed list with the wire fact that keeps them raw.
//
// THE ORG IS NEVER AN In FIELD. A typed op receives only a context; the tenant
// arrives through cloud.Bridge and is read by tenantOf. An In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the
// caller asserted for itself.
//
// EVERY SCHEMA NAME IS PREFIXED. openapi.Weave refuses one name with two shapes
// across apps because a generated SDK binds whichever it read last, and this app
// introduces forty types into a namespace already flat across 130-odd apps.
// `ref`, `list`, `page` and `state` are exactly the names the next app reaches
// for, so nothing here is spelled without its face.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op AND off every In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool description a model reads to pick the tool — Go drops
// comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) and has no parameter for the service,
// so the service arrives as a RECEIVER and every op is a METHOD VALUE — which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *stateService }

// mount registers the surface. Separate from Mount because Mount BUILDS the
// state and the routes do not care where it came from: a test can pin the state
// and exercise the ROUTES without a warehouse, a data directory or a clock.
func mount(s *stateService, app cloud.Router) {
	// ONE `g := <router>.Group("/prefix")` per line — cmd/zipdoc resolves a
	// group's prefix by reading that exact assignment form and FAILS the generate
	// rather than filing prose under a path that does not exist.
	g := app.Group("/v1/risk")
	gml := app.Group("/v1/ml")
	// cloud.Bridge FIRST on each group. fiber runs middleware in registration
	// order, so one installed after its leaves never runs, and a typed op with no
	// Bridge in front of it has no validated principal to read — which this
	// package answers with 403 rather than with a guess.
	g.Use(cloud.Bridge())
	gml.Use(cloud.Bridge())
	o := ops{s: s}

	// ── decide ──────────────────────────────────────────────────────────────
	zip.Post(g, "/decide", o.decide,
		zip.WithOperationID("riskDecide"),
		zip.WithSummary("Score an entity or event and return a decision"),
		zip.WithTags("risk"))

	// ── record ──────────────────────────────────────────────────────────────
	zip.Get(g, "/decisions", o.decisions,
		zip.WithOperationID("riskDecisions"),
		zip.WithSummary("List recent decisions"),
		zip.WithTags("risk"))
	zip.Get(g, "/decisions/:id", o.decision,
		zip.WithOperationID("riskDecisionDetail"),
		zip.WithSummary("Read one decision with its evidence"),
		zip.WithTags("risk"))
	zip.Post(g, "/decisions/:id/label", o.label,
		zip.WithOperationID("riskLabel"),
		zip.WithSummary("Record what a human concluded about a decision"),
		zip.WithTags("risk"))
	zip.Get(g, "/subjects/:kind/:id", o.subject,
		zip.WithOperationID("riskSubjectState"),
		zip.WithSummary("Read one subject's current risk state"),
		zip.WithTags("risk"))
	zip.Get(g, "/activity", o.activity,
		zip.WithOperationID("riskActivity"),
		zip.WithSummary("Watch live rule activations and their disposition"),
		zip.WithTags("risk"))

	// ── govern ──────────────────────────────────────────────────────────────
	zip.Post(g, "/simulate", o.simulate,
		zip.WithOperationID("riskSimulate"),
		zip.WithSummary("Replay a candidate rule over this tenant's own history"),
		zip.WithTags("risk"))
	zip.Get(g, "/rules", o.rules,
		zip.WithOperationID("riskRules"),
		zip.WithSummary("List this tenant's rules"),
		zip.WithTags("risk"))
	zip.Post(g, "/rules", o.createRule,
		zip.WithOperationID("riskCreateRule"),
		zip.WithSummary("Create a rule"),
		zip.WithStatus(201),
		zip.WithTags("risk"))
	zip.Patch(g, "/rules/:id", o.updateRule,
		zip.WithOperationID("riskUpdateRule"),
		zip.WithSummary("Replace a rule"),
		zip.WithTags("risk"))
	zip.Delete(g, "/rules/:id", o.deleteRule,
		zip.WithOperationID("riskDeleteRule"),
		zip.WithSummary("Retire a rule"),
		zip.WithTags("risk"))

	zip.Get(g, "/lists", o.lists,
		zip.WithOperationID("riskLists"),
		zip.WithSummary("List this tenant's allow and deny lists"),
		zip.WithTags("risk"))
	zip.Post(g, "/lists", o.createList,
		zip.WithOperationID("riskCreateList"),
		zip.WithSummary("Create an allow or deny list"),
		zip.WithStatus(201),
		zip.WithTags("risk"))
	zip.Post(g, "/lists/:name/entries", o.addEntries,
		zip.WithOperationID("riskAddListEntries"),
		zip.WithSummary("Add values to a list"),
		zip.WithTags("risk"))
	zip.Delete(g, "/lists/:name/entries/:value", o.removeEntry,
		zip.WithOperationID("riskRemoveListEntry"),
		zip.WithSummary("Remove one value from a list"),
		zip.WithTags("risk"))

	zip.Get(g, "/suppressions", o.suppressions,
		zip.WithOperationID("riskSuppressions"),
		zip.WithSummary("List active suppressions"),
		zip.WithTags("risk"))
	zip.Post(g, "/suppressions", o.suppress,
		zip.WithOperationID("riskSuppress"),
		zip.WithSummary("Suppress a rule's activations"),
		zip.WithStatus(201),
		zip.WithTags("risk"))
	zip.Delete(g, "/suppressions/:id", o.unsuppress,
		zip.WithOperationID("riskUnsuppress"),
		zip.WithSummary("Lift a suppression"),
		zip.WithTags("risk"))

	zip.Get(g, "/controls", o.controls,
		zip.WithOperationID("riskControls"),
		zip.WithSummary("List the platform controls declared on subjects"),
		zip.WithTags("risk"))
	zip.Post(g, "/controls", o.setControl,
		zip.WithOperationID("riskSetControl"),
		zip.WithSummary("Declare a reserve, a payout hold or a block on a subject"),
		zip.WithStatus(201),
		zip.WithTags("risk"))
	zip.Delete(g, "/controls/:id", o.releaseControl,
		zip.WithOperationID("riskReleaseControl"),
		zip.WithSummary("Release a control"),
		zip.WithTags("risk"))

	zip.Get(g, "/dictionary", o.dictionary,
		zip.WithOperationID("riskDictionary"),
		zip.WithSummary("Read the field catalogue a rule may name"),
		zip.WithTags("risk"))
	zip.Get(g, "/mode", o.getMode,
		zip.WithOperationID("riskMode"),
		zip.WithSummary("Read whether this tenant's decisions act or only observe"),
		zip.WithTags("risk"))
	zip.Put(g, "/mode", o.setMode,
		zip.WithOperationID("riskSetMode"),
		zip.WithSummary("Take this tenant live, or return it to shadow"),
		zip.WithTags("risk"))

	// UNTYPED BY DESIGN — a real probe answers 503 carrying the degraded REPORT
	// as its body. A typed op reaches a non-2xx only by returning an error, and
	// zip renders that as its own envelope, dropping the report. See
	// typed_wire_test.go.
	g.Get("/health", health(s))

	// ── /v1/ml — the shared model plane, owned by THIS app ───────────────────
	//
	// The native ML leaves are on THIS app's manifest row and not on apps/ml's,
	// and that is the sharpest structural decision in the design. A manifest row
	// is a BINARY, and the model is in-process MUTABLE state: if plugin/ml
	// trained and plugin/risk scored, the two processes would hold different
	// mass counters and there would be NO ERROR — just two different answers to
	// one question. One owner of the state, one row.
	zip.Post(gml, "/score", o.score,
		zip.WithOperationID("mlScore"),
		zip.WithSummary("Score an observation without learning from it"),
		zip.WithTags("ml"))
	zip.Post(gml, "/train", o.train,
		zip.WithOperationID("mlTrain"),
		zip.WithSummary("Train this tenant's model on its own observations"),
		zip.WithTags("ml"))
	zip.Get(gml, "/state", o.modelState,
		zip.WithOperationID("mlState"),
		zip.WithSummary("Read the model's governance state"),
		zip.WithTags("ml"))
	zip.Put(gml, "/state/appetite", o.setAppetite,
		zip.WithOperationID("mlSetAppetite"),
		zip.WithSummary("Set the share of the stream the model may examine"),
		zip.WithTags("ml"))
	zip.Get(gml, "/features", o.features,
		zip.WithOperationID("mlFeatures"),
		zip.WithSummary("Read the typology-to-feature inventory the model is built on"),
		zip.WithTags("ml"))
	zip.Post(gml, "/search", o.search,
		zip.WithOperationID("mlSearch"),
		zip.WithSummary("Exhaustively search the model topology over this tenant's own history"),
		zip.WithStatus(202),
		zip.WithTags("ml"))
	zip.Get(gml, "/search/:id", o.searchResult,
		zip.WithOperationID("mlSearchResult"),
		zip.WithSummary("Read an exhaustive search's report"),
		zip.WithTags("ml"))
	zip.Post(gml, "/snapshot", o.snapshot,
		zip.WithOperationID("mlSnapshot"),
		zip.WithSummary("Pin this tenant's learned state"),
		zip.WithStatus(201),
		zip.WithTags("ml"))
	zip.Post(gml, "/restore", o.restore,
		zip.WithOperationID("mlRestore"),
		zip.WithSummary("Restore this tenant's learned state from its pinned snapshot"),
		zip.WithTags("ml"))

	// The lifecycle leaves — the registry, the schedule and drift — declare
	// themselves, on the same prefix and AFTER the Bridge installed above. They
	// are in fit.go rather than here because cmd/zipdoc resolves a route's path
	// from the group assignment in the file that registers it.
	mountFit(app, o)
}

// ── the shapes ──────────────────────────────────────────────────────────────

// riskNoInput is the In of an op that takes nothing off the wire. Its whole
// input is the caller's validated principal, which is what decides the tenant.
type riskNoInput struct{}

// riskRef addresses one record by its identifier. The identifier is the path
// segment: the URL is the addressing authority, so it binds from there whatever
// a body says.
type riskRef struct {
	// ID is the record to act on, taken from the path.
	ID string `json:"id"`
}

// riskSubject names what is being judged.
type riskSubject struct {
	// Kind is the sort of thing this is: account, transaction, session, agent,
	// merchant or payout. It selects the aggregation axes, not the tenant gate.
	Kind string `json:"kind"`
	// ID is the subject's identifier within the caller's own org. It is never
	// interpreted, only aggregated on, so it may be any stable string.
	ID string `json:"id"`
}

// riskAmount is money on the wire. It is an INTEGER of nano-units, never a
// float: a float amount is a rounding error waiting to be argued about with a
// customer, and the aggregate this feeds is a statistic that can afford the
// conversion where the ledger cannot.
type riskAmount struct {
	// Nano is the amount in billionths of one unit of Currency.
	Nano int64 `json:"nano"`
	// Currency is the ISO 4217 code.
	Currency string `json:"currency"`
	// Direction is whether value came in or went out: "in" or "out". Without it
	// the model cannot see funds passing through, which is the layering shape.
	Direction string `json:"direction,omitempty"`
}

// riskActor is who or what is acting, as the caller understands it. Every field
// is ADVISORY: the server derives the real agency class from its own registries,
// because a caller that could declare itself an agent could buy the agent lane.
type riskActor struct {
	// Agent is the agent reference the caller claims to be acting as. It is
	// resolved against THIS org's agent registry; an unresolvable reference is
	// simply not a declaration.
	Agent string `json:"agent,omitempty"`
	// Session is the session reference this action belongs to.
	Session string `json:"session,omitempty"`
	// Credential is the credential class the caller believes it presented.
	// Advisory only — the server reads the credential it actually verified.
	Credential string `json:"credential,omitempty"`
}

// riskDecideIn is one thing to judge.
type riskDecideIn struct {
	// Stage is the lifecycle moment being judged: signup, payment, session,
	// usage, payout or dispute. It selects the feature window and the rule set;
	// it does NOT select a different tenant gate.
	Stage string `json:"stage"`
	// Subject is what is being judged.
	Subject riskSubject `json:"subject"`
	// Actor is who is acting, if the caller knows.
	Actor *riskActor `json:"actor,omitempty"`
	// Amount is the money involved, for the stages where there is any.
	Amount *riskAmount `json:"amount,omitempty"`
	// Signals are the observations the caller can supply: ip, email,
	// emaildomain, device, bin, asn, country, counterparty. Unknown keys are
	// carried and are readable by a rule as signal.<key>, so a tenant can score
	// on facts only it has.
	Signals map[string]string `json:"signals,omitempty"`
	// Idem makes a re-decide idempotent: the same key returns the same decision
	// rather than scoring twice and moving the counters twice.
	Idem string `json:"idem,omitempty"`
}

// riskHit is one piece of evidence behind a decision.
type riskHit struct {
	// Rule is the identifier of the rule or model that produced this evidence.
	Rule string `json:"rule"`
	// Name is the detection's human-readable name.
	Name string `json:"name"`
	// Action is what this evidence alone asked for.
	Action string `json:"action"`
	// Weight is how much it contributed, in [0,1].
	Weight float64 `json:"weight"`
	// Severity is the reviewer-facing grading.
	Severity string `json:"severity,omitempty"`
	// Suppressed marks evidence a suppression muted. It is reported and
	// contributes nothing — a muted control that left no trace would be
	// indistinguishable from one that was never running.
	Suppressed bool `json:"suppressed,omitempty"`
}

// riskCause is one feature's contribution to a model's judgement: the arithmetic
// the score came from, plus the score the model would have produced had this
// feature been unremarkable. That last number is a counterfactual on the model
// itself, which is what makes the contribution a measurement rather than an
// attribution scheme's opinion.
type riskCause struct {
	// Feature is the model dimension.
	Feature string `json:"feature"`
	// Typology is the pattern the feature expresses.
	Typology string `json:"typology"`
	// Indicator is the supervisor's own words for what is being looked for.
	Indicator string `json:"indicator"`
	// Citation is where those words come from.
	Citation string `json:"citation"`
	// Unit is how to read Observed and Baseline.
	Unit string `json:"unit"`
	// Observed is this subject's value.
	Observed float64 `json:"observed"`
	// Baseline is what is unremarkable for this subject.
	Baseline float64 `json:"baseline"`
	// Without is the score the model would have produced with this feature
	// neutral, holding everything else.
	Without float64 `json:"without"`
	// Share is this feature's part of the score, in [0,1].
	Share float64 `json:"share"`
}

// riskDecision is the answer.
type riskDecision struct {
	// ID identifies this decision for the whole of its life: the label, the
	// evidence read and the dispute packet all key on it.
	ID string `json:"id"`
	// Action is what to do: allow, challenge, review, restrict or block.
	Action string `json:"action"`
	// Score is the weight-of-evidence score in [0,1].
	Score float64 `json:"score"`
	// Agency is what kind of actor this was: agent, human, bot or unknown.
	// Derived server-side from the credential class, this org's agent registry,
	// the live agent session and the account's metered shape — never from a
	// user-agent string, which is a claim rather than a fact.
	Agency string `json:"agency"`
	// Hits is the evidence, strongest first.
	Hits []riskHit `json:"hits"`
	// Causes is the model's per-feature attribution, when the model contributed.
	Causes []riskCause `json:"causes,omitempty"`
	// Shadow is true when this tenant is observing rather than acting. In shadow
	// Action is always allow and everything else is computed and recorded.
	Shadow bool `json:"shadow"`
	// Refusal names why the model declined to score, when it did: warming,
	// unidentified or shadow. It is present precisely so that silence never
	// reads as a clean result.
	Refusal string `json:"refusal,omitempty"`
	// Model is the digest of the model that produced this, so an auditor can pin
	// the exact geometry that raised an alert.
	Model string `json:"model"`
}

// riskDecisionsIn filters the decision log. Every filter is an equality on a
// column the tenant's own file owns; there is no free-text predicate and no
// caller-chosen ordering, so there is no statement to inject into.
type riskDecisionsIn struct {
	// Kind narrows to one subject kind.
	Kind string `json:"kind,omitempty"`
	// Subject narrows to one subject.
	Subject string `json:"subject,omitempty"`
	// Action narrows to one outcome.
	Action string `json:"action,omitempty"`
	// Stage narrows to one lifecycle moment.
	Stage string `json:"stage,omitempty"`
	// Limit bounds the page, 1..500, default 100.
	Limit int `json:"limit,omitempty"`
}

// riskDecisionBrief is one row of the decision log.
type riskDecisionBrief struct {
	// ID identifies the decision.
	ID string `json:"id"`
	// At is when it was made, RFC 3339 in UTC.
	At string `json:"at"`
	// Stage is the lifecycle moment.
	Stage string `json:"stage"`
	// Kind is the subject kind.
	Kind string `json:"kind"`
	// Subject is the subject identifier.
	Subject string `json:"subject"`
	// Action is what was decided.
	Action string `json:"action"`
	// Score is the weight-of-evidence score.
	Score float64 `json:"score"`
	// Agency is the actor class.
	Agency string `json:"agency"`
	// Shadow is whether the decision acted.
	Shadow bool `json:"shadow"`
	// Refusal names why the model declined, when it did.
	Refusal string `json:"refusal,omitempty"`
	// Label is what a human later concluded, when anyone has.
	Label string `json:"label,omitempty"`
}

// riskDecisionPage is a page of the decision log.
type riskDecisionPage struct {
	// Items is the page, newest first.
	Items []riskDecisionBrief `json:"items"`
}

// riskDecisionView is one decision with its evidence — the dispute packet: what
// was decided, on what evidence, over which features, by which model.
type riskDecisionView struct {
	// Decision is the row.
	Decision riskDecisionBrief `json:"decision"`
	// Hits is the evidence.
	Hits []riskHit `json:"hits"`
	// Causes is the model's attribution.
	Causes []riskCause `json:"causes,omitempty"`
	// Model is the digest of the model that produced it.
	Model string `json:"model"`
}

// riskLabelIn records the outcome a human concluded.
type riskLabelIn struct {
	// ID is the decision, from the path.
	ID string `json:"id"`
	// Verdict is what it turned out to be: fraud, legitimate, chargeback, abuse
	// or unknown. It is the only supervision this product has, and it is what
	// turns a false-positive rate from a guess into a measurement.
	Verdict string `json:"verdict"`
}

// riskSubjectRef addresses one subject.
type riskSubjectRef struct {
	// Kind is the subject kind, from the path.
	Kind string `json:"kind"`
	// ID is the subject identifier, from the path.
	ID string `json:"id"`
}

// riskVelocity is one sliding aggregate over one axis.
type riskVelocity struct {
	// Axis is what is being aggregated over: account, device, ip, pair, email or
	// bin.
	Axis string `json:"axis"`
	// Window is the span: 1h, 24h, 7d or 30d.
	Window string `json:"window"`
	// Count is how many observations fell in the window.
	Count int `json:"count"`
	// Sum is their total value, in units of currency.
	Sum float64 `json:"sum"`
	// Near is how many fell just below the reporting threshold — the structuring
	// signal.
	Near int `json:"near"`
	// Days is how many distinct calendar days contributed.
	Days int `json:"days"`
}

// riskHistory is one bucket of a subject's activity as the warehouse holds it —
// the long horizon the in-memory rings cannot keep.
type riskHistory struct {
	// Bucket is the five-minute period, RFC 3339 in UTC.
	Bucket string `json:"bucket"`
	// Values is each counted feature over that period, keyed by the names
	// GET /v1/risk/dictionary publishes.
	Values map[string]float64 `json:"values"`
}

// riskBaseline is one NETWORK quantile band.
//
// This is the ONLY cross-org value this API returns, and it is aggregate by
// construction rather than by policy: the table it comes from has no tenant
// column, no subject and no pseudonym, and a band is published only once at
// least 25 distinct orgs and 1000 observations contributed to it. There is no
// query that returns another tenant's numbers, because those rows do not exist.
type riskBaseline struct {
	// Feature is what is being compared.
	Feature string `json:"feature"`
	// Q10 is the tenth percentile across the platform for this subject kind.
	Q10 float64 `json:"q10"`
	// Q50 is the median.
	Q50 float64 `json:"q50"`
	// Q90 is the ninetieth percentile.
	Q90 float64 `json:"q90"`
	// Q99 is the ninety-ninth — where the tail this product is looking for
	// begins.
	Q99 float64 `json:"q99"`
	// Orgs is how many distinct organisations contributed to this band. It is
	// reported so a reader can see the k-anonymity floor was met rather than
	// take it on trust.
	Orgs uint64 `json:"orgs"`
	// Observations is how many measurements the band was cut from.
	Observations uint64 `json:"observations"`
	// Day is the period the band covers, YYYY-MM-DD.
	Day string `json:"day"`
}

// riskSubjectView is one subject's current state.
type riskSubjectView struct {
	// Subject is what this describes.
	Subject riskSubject `json:"subject"`
	// Velocity is every live aggregate the subject has.
	Velocity []riskVelocity `json:"velocity"`
	// Decisions is the subject's recent decision history, newest first.
	Decisions []riskDecisionBrief `json:"decisions"`
	// Controls is what is currently declared on the subject.
	Controls []riskControl `json:"controls"`
	// History is the subject's activity over the last thirty days from the
	// warehouse. Absent when the warehouse is unreachable — an absent history is
	// an honest gap, where a zeroed one would say the subject did nothing.
	History []riskHistory `json:"history,omitempty"`
	// Network is where this subject kind sits across the platform, as quantiles
	// only. Absent when no band has met the k-anonymity floor.
	Network []riskBaseline `json:"network,omitempty"`
	// Gap names what could not be read, when something could not be. It is
	// present precisely so that a missing History or Network is legible as a
	// gap rather than as an answer.
	Gap string `json:"gap,omitempty"`
}

// riskTerm is one comparison in a rule.
type riskTerm struct {
	// Field is the fact to read: one of the closed vocabulary, or signal.<name>,
	// or velocity.<axis>.<window>.<stat>. GET /v1/risk/dictionary lists them.
	Field string `json:"field"`
	// Op is the comparison: eq, ne, gt, gte, lt, lte, in, notin, inlist,
	// notinlist, exists, absent, contains, prefix or suffix.
	Op string `json:"op"`
	// Value is the string operand.
	Value string `json:"value,omitempty"`
	// Number is the numeric operand.
	Number float64 `json:"number,omitempty"`
	// Values is the set operand, for in and notin.
	Values []string `json:"values,omitempty"`
}

// riskRule is one detection. All of its terms must hold: a conjunction,
// deliberately, because a disjunction is two rules and two rules are two things
// a reviewer can judge, retire and measure separately.
type riskRule struct {
	// ID is the rule's identifier within this tenant.
	ID string `json:"id"`
	// Name is what a reviewer reads in an alert.
	Name string `json:"name"`
	// Stage narrows the rule to one lifecycle moment; empty means every stage.
	Stage string `json:"stage,omitempty"`
	// Action is what the rule asks for when it holds.
	Action string `json:"action"`
	// Weight is how much a hit contributes, in [0,1].
	Weight float64 `json:"weight"`
	// Severity is the reviewer-facing grading: low, medium, high or critical.
	Severity string `json:"severity,omitempty"`
	// Enabled governs the live path. A disabled rule still replays, because the
	// question a simulation asks is what happens ON activation.
	Enabled bool `json:"enabled"`
	// All is the conjunction. An empty conjunction is refused: a rule that holds
	// on everything is not a detection.
	All []riskTerm `json:"all"`
}

// riskRuleIn is a rule to create or replace.
type riskRuleIn struct {
	// ID is the rule to replace, from the path on a PATCH. Ignored on create,
	// where the server mints one.
	ID string `json:"id,omitempty"`
	// Rule is the detection.
	Rule riskRule `json:"rule"`
}

// riskRuleList is this tenant's rule set.
type riskRuleList struct {
	// Items is every rule, by identifier.
	Items []riskRule `json:"items"`
}

// riskSimulateIn asks what a candidate would have done.
type riskSimulateIn struct {
	// Candidate is the rule to try. It is evaluated as activated whatever its
	// enabled flag says: the question is what happens on activation.
	Candidate riskRule `json:"candidate"`
	// Incumbent is the rule the candidate would replace, when it replaces one.
	// Naming it is what turns a report into a justification for a retirement.
	Incumbent string `json:"incumbent,omitempty"`
	// Limit bounds how many recorded decisions are replayed, 1..5000.
	Limit int `json:"limit,omitempty"`
}

// riskSimulateReport is what the candidate would have done over real history.
type riskSimulateReport struct {
	// Events is how many recorded decisions were replayed.
	Events int `json:"events"`
	// Alerts is how many the candidate would have fired on.
	Alerts int `json:"alerts"`
	// Added is what the candidate catches and the incumbent does not — the new
	// coverage, and the new volume.
	Added []string `json:"added,omitempty"`
	// Dropped is what the incumbent catches and the candidate does not — the
	// coverage being given up, which is what a retirement has to justify.
	Dropped []string `json:"dropped,omitempty"`
	// Kept is what both catch.
	Kept []string `json:"kept,omitempty"`
	// Judged is how many of the candidate's alerts fall on decisions a human has
	// labelled.
	Judged int `json:"judged"`
	// FalsePositive is the share of judged alerts a human called legitimate. It
	// is ABSENT rather than zero when nothing was judged: an unmeasured
	// proportion reported as 0.0 reads as a perfect rule.
	FalsePositive *float64 `json:"falsePositive,omitempty"`
	// Refusal names why a report is empty when it is. An empty history is
	// refused rather than reported as zero alerts, because a quiet rule and an
	// unrun rule produce the same number.
	Refusal string `json:"refusal,omitempty"`
}

// riskListView is one allow or deny list.
type riskListView struct {
	// Name is the list's identifier, which a rule names in an inlist term.
	Name string `json:"name"`
	// Kind is whether membership allows or denies: "allow" or "deny".
	Kind string `json:"kind"`
	// Entries is how many values it holds.
	Entries int `json:"entries"`
	// CreatedAt is when it was created, RFC 3339 in UTC.
	CreatedAt string `json:"createdAt"`
}

// listView is the internal spelling of riskListView, used by the store.
type listView = riskListView

// riskListPage is this tenant's lists.
type riskListPage struct {
	// Items is every list.
	Items []riskListView `json:"items"`
}

// riskListIn creates a list.
type riskListIn struct {
	// Name is the list's identifier.
	Name string `json:"name"`
	// Kind is whether membership allows or denies.
	Kind string `json:"kind"`
}

// riskListEntriesIn adds values to a list.
type riskListEntriesIn struct {
	// Name is the list, from the path.
	Name string `json:"name"`
	// Values are the values to add. They are folded to lower case, so a value
	// added in one case matches a signal in another.
	Values []string `json:"values"`
}

// riskListEntryRef addresses one value in one list.
type riskListEntryRef struct {
	// Name is the list, from the path.
	Name string `json:"name"`
	// Value is the value to remove, from the path.
	Value string `json:"value"`
}

// riskSuppressIn mutes a rule's activations.
//
// A suppressed hit is RECORDED with suppressed set and contributes nothing. It
// is never dropped: a compliance record that can be silently muted by an
// operational knob is not a record.
type riskSuppressIn struct {
	// Rule is the rule to mute. At least one of Rule, Kind or Subject must be
	// given — a suppression that names nothing would mute everything.
	Rule string `json:"rule,omitempty"`
	// Kind narrows the suppression to one subject kind.
	Kind string `json:"kind,omitempty"`
	// Subject narrows it to one subject.
	Subject string `json:"subject,omitempty"`
	// Until is when it expires, RFC 3339. Absent means it does not, which is a
	// choice somebody should be able to be asked about.
	Until string `json:"until,omitempty"`
	// Reason is why. It is required, because a mute nobody can explain later is
	// a control that quietly stopped.
	Reason string `json:"reason"`
}

// riskSuppression is one active mute.
type riskSuppression struct {
	// ID identifies the suppression.
	ID string `json:"id"`
	// Rule is the rule muted, when it names one.
	Rule string `json:"rule,omitempty"`
	// Kind is the subject kind muted, when it names one.
	Kind string `json:"kind,omitempty"`
	// Subject is the subject muted, when it names one.
	Subject string `json:"subject,omitempty"`
	// Until is when it expires, RFC 3339. Absent means it does not.
	Until string `json:"until,omitempty"`
	// Reason is why it was applied.
	Reason string `json:"reason"`
	// By is who applied it, taken from the validated principal and never from
	// the request body.
	By string `json:"by"`
	// At is when it was applied, RFC 3339 in UTC.
	At string `json:"at"`
}

// riskSuppressionPage is the active suppressions.
type riskSuppressionPage struct {
	// Items is every suppression, newest first.
	Items []riskSuppression `json:"items"`
}

// riskControlIn declares a platform control on a subject.
//
// Risk DECLARES; it never moves money. hanzoai/commerce owns payouts, disputes
// and balances, and the integration is commerce reading these before a payout
// and calling decide at authorization. That separation is also what makes the
// product processor-agnostic: risk takes signals and returns a judgement, and
// touches no processor.
type riskControlIn struct {
	// Subject is what the control applies to.
	Subject riskSubject `json:"subject"`
	// Control is which one: reserve, payout-hold or block.
	Control string `json:"control"`
	// Rate is the reserve fraction, in [0,1], for a reserve.
	Rate float64 `json:"rate,omitempty"`
	// Until is when it lapses, RFC 3339. Absent means it does not.
	Until string `json:"until,omitempty"`
	// Reason is why.
	Reason string `json:"reason"`
}

// riskControl is one declared control.
type riskControl struct {
	// ID identifies the control.
	ID string `json:"id"`
	// Subject is what it applies to.
	Subject riskSubject `json:"subject"`
	// Control is which one.
	Control string `json:"control"`
	// Rate is the reserve fraction, for a reserve.
	Rate float64 `json:"rate,omitempty"`
	// Until is when it lapses, RFC 3339. Absent means it does not.
	Until string `json:"until,omitempty"`
	// Reason is why it was declared.
	Reason string `json:"reason"`
	// By is who declared it, taken from the validated principal.
	By string `json:"by"`
	// At is when, RFC 3339 in UTC.
	At string `json:"at"`
}

// riskControlPage is the declared controls.
type riskControlPage struct {
	// Items is every control, newest first.
	Items []riskControl `json:"items"`
}

// riskControlsIn narrows the control list to one subject.
type riskControlsIn struct {
	// Kind narrows to one subject kind.
	Kind string `json:"kind,omitempty"`
	// Subject narrows to one subject.
	Subject string `json:"subject,omitempty"`
}

// riskField is one entry in the field catalogue.
type riskField struct {
	// Field is how a rule names it.
	Field string `json:"field"`
	// Kind is what sort of value it holds: "string" or "number".
	Kind string `json:"kind"`
	// Ops are the comparisons that make sense on it.
	Ops []string `json:"ops"`
	// Note says what it means.
	Note string `json:"note"`
}

// riskDictionaryView is the vocabulary a rule may be written in. It is the
// closed set: a field not listed here is refused at admission rather than
// silently never matching, which is the failure that reads as a working control.
type riskDictionaryView struct {
	// Fields is every scalar fact.
	Fields []riskField `json:"fields"`
	// Stages are the lifecycle moments.
	Stages []string `json:"stages"`
	// Actions are the outcomes, weakest first.
	Actions []string `json:"actions"`
	// Axes are the velocity axes.
	Axes []string `json:"axes"`
	// Windows are the velocity spans.
	Windows []string `json:"windows"`
	// Lists are this tenant's own lists, which an inlist term may name.
	Lists []string `json:"lists"`
	// Signals are the signal keys this tenant has actually sent, which is the
	// half of the catalogue that is the tenant's own rather than the product's.
	Signals []string `json:"signals"`
}

// riskActivityIn bounds the activity poll.
type riskActivityIn struct {
	// Limit bounds how many recent decisions are summarised, 1..500.
	Limit int `json:"limit,omitempty"`
}

// riskActivityRule is one rule's recent activation count.
type riskActivityRule struct {
	// Rule is the rule that fired.
	Rule string `json:"rule"`
	// Name is its detection name.
	Name string `json:"name"`
	// Activations is how many of the sampled decisions it fired on.
	Activations int `json:"activations"`
	// Suppressed is how many of those were muted.
	Suppressed int `json:"suppressed"`
}

// riskActivityView is the watcher: what is firing right now and what is being
// muted. It reads the decision log rather than a second bus, because the log is
// already the record and a second stream would be a second thing to keep true.
type riskActivityView struct {
	// Sampled is how many recent decisions this summarises.
	Sampled int `json:"sampled"`
	// Actions is how many decisions reached each action.
	Actions map[string]int `json:"actions"`
	// Agency is how many decisions were of each actor class.
	Agency map[string]int `json:"agency"`
	// Refusals is how many decisions the model declined to score, by reason.
	Refusals map[string]int `json:"refusals"`
	// Rules is per-rule activation, most active first.
	Rules []riskActivityRule `json:"rules"`
	// Shadow is whether this tenant is observing rather than acting.
	Shadow bool `json:"shadow"`
}

// riskModeIn takes a tenant live or returns it to shadow.
type riskModeIn struct {
	// Mode is "shadow" or "live". Shadow is the default and the default is not
	// configurable: a model that quietly went live and started declining
	// payments is the worst failure available here, so going live is an act
	// somebody performs and can be asked about.
	Mode string `json:"mode"`
}

// riskModeView is whether this tenant's decisions act.
type riskModeView struct {
	// Mode is "shadow" or "live".
	Mode string `json:"mode"`
	// Since is when it was last changed, RFC 3339 in UTC. Empty means never.
	Since string `json:"since,omitempty"`
}

// ── the /v1/ml shapes ───────────────────────────────────────────────────────

// mlNoInput is the In of a model-plane op that takes nothing off the wire.
type mlNoInput struct{}

// mlRef addresses one model-plane record.
type mlRef struct {
	// ID is the record to read, from the path.
	ID string `json:"id"`
}

// mlObservation is one thing for the model to score or learn from. It is the
// decide input minus the governance: no stage, no rules, no action — just the
// coordinates.
type mlObservation struct {
	// Subject is whose behaviour this is.
	Subject riskSubject `json:"subject"`
	// Amount is the money involved, if any.
	Amount *riskAmount `json:"amount,omitempty"`
	// Signals are the observations: ip, device, counterparty and the rest.
	Signals map[string]string `json:"signals,omitempty"`
	// At is when it happened, RFC 3339. Absent means now.
	At string `json:"at,omitempty"`
}

// mlScoreIn is one observation to score.
type mlScoreIn struct {
	// Observation is what to score.
	Observation mlObservation `json:"observation"`
}

// mlValue is one coordinate as the model read it, including the ones that
// contributed nothing — so a reviewer sees what the model read and not only what
// it concluded.
type mlValue struct {
	// Feature is the dimension.
	Feature string `json:"feature"`
	// X is the coordinate the model used, in [0,1].
	X float64 `json:"x"`
	// Observed is the raw number behind it.
	Observed float64 `json:"observed"`
	// Baseline is what is unremarkable here.
	Baseline float64 `json:"baseline"`
	// Unit is how to read Observed and Baseline.
	Unit string `json:"unit"`
	// Blind is true when this feature had no usable coordinate, so it took its
	// neutral value. A feature blind on most traffic is not contributing
	// whatever the inventory claims for it.
	Blind bool `json:"blind"`
}

// mlScoreOut is the model's verdict on one observation. This op LEARNS NOTHING:
// it is how a candidate is tried against a tenant's real behaviour before
// anything depends on the answer.
type mlScoreOut struct {
	// Scored is false when the model declined; Refusal says which refusal.
	Scored bool `json:"scored"`
	// Refusal names the decline: warming or unidentified.
	Refusal string `json:"refusal,omitempty"`
	// Score is the anomaly score in [0,1]: 0 where this tenant's recent
	// behaviour is densest, 1 where there is none of it.
	Score float64 `json:"score"`
	// Cut is the threshold in force, derived from the appetite.
	Cut float64 `json:"cut"`
	// Alert is whether this would become evidence.
	Alert bool `json:"alert"`
	// Shadow is whether the model is observing rather than contributing.
	Shadow bool `json:"shadow"`
	// Causes is the per-feature attribution, ordered by contribution.
	Causes []riskCause `json:"causes,omitempty"`
	// Values is every coordinate, including the blind ones.
	Values []mlValue `json:"values,omitempty"`
	// Model is the digest of the model that produced this.
	Model string `json:"model"`
}

// mlTrainIn is a batch of this tenant's own observations to learn from.
//
// There is no training JOB, because there is no training PASS: the half-space
// geometry is built before any data arrives and the model IS a set of mass
// counters, so training is one online increment per observation. That is why a
// tenant is never protected by a stale model and why a deploy that snapshots is
// the whole of the durability story.
type mlTrainIn struct {
	// Observations are what to learn from. They are this tenant's own data and
	// nothing else: there is no cross-tenant training input and no way to
	// express one, because the model is indexed by the tenant key and its tree
	// GEOMETRY is seeded from it.
	Observations []mlObservation `json:"observations"`
}

// mlTrainOut is what the model took in.
type mlTrainOut struct {
	// Learned is how many observations were incorporated.
	Learned int `json:"learned"`
	// Refused is how many were not, by reason.
	Refused map[string]int `json:"refused"`
	// Warm is true once the model has learned enough of this tenant's behaviour
	// to score at all.
	Warm bool `json:"warm"`
	// Model is the digest of the model that learned them.
	Model string `json:"model"`
}

// mlAppetite is how much of the stream the model may examine.
type mlAppetite struct {
	// Review is the share of the stream that may be sent for examination. The
	// alert threshold is derived from it as a quantile of the scores actually
	// observed, rather than fixed at a number someone liked — so an alert level
	// is governed rather than tuned, and a drifting distribution does not turn
	// it into silence or a flood.
	Review float64 `json:"review"`
	// Sample is the share of NON-alerting traffic retained for review below the
	// line. It is the only instrument that can measure what the model missed,
	// because nothing in the stream is labelled.
	Sample float64 `json:"sample"`
	// Warm is how many observations the model must learn before it may score.
	Warm int `json:"warm"`
}

// mlFeature is one dimension of the model space and the obligation it serves.
// The mapping is code rather than a document because a mapping kept beside the
// model cannot drift away from what the model actually reads.
type mlFeature struct {
	// Name is the dimension.
	Name string `json:"name"`
	// Window is the span it is measured over, when it has one.
	Window string `json:"window,omitempty"`
	// Typology is the pattern it expresses.
	Typology string `json:"typology"`
	// Indicator is the supervisor's own words for what is being looked for.
	Indicator string `json:"indicator"`
	// Citation is where those words come from, so the claim is checkable.
	Citation string `json:"citation"`
	// Severity is the grading a hit on this feature carries.
	Severity string `json:"severity"`
	// Unit is how to read the raw number.
	Unit string `json:"unit"`
	// Neutral is the value at which this feature is unremarkable — the
	// coordinate the counterfactual moves it to.
	Neutral float64 `json:"neutral"`
}

// mlFeatureInventory is the typology-to-feature mapping the model is built on.
type mlFeatureInventory struct {
	// Items is every dimension, in coordinate order. The ORDER is part of the
	// model's identity: adding, removing or reordering one invalidates learned
	// state, which the digest enforces.
	Items []mlFeature `json:"items"`
	// Digest is the identity of this shape.
	Digest string `json:"digest"`
}

// mlModelState is what a review of the model reads. It covers ONE tenant: a
// caller scoped to a tenant cannot learn another's volumes, alert rate or
// behaviour from it.
type mlModelState struct {
	// Appetite is the stated share of the stream that may be examined.
	Appetite mlAppetite `json:"appetite"`
	// Threshold is the score cut currently in force, recomputed each window as
	// the quantile that admits the stated share.
	Threshold float64 `json:"threshold"`
	// Realised is the share that ACTUALLY alerted. Stated against realised is
	// the governance report — an appetite is a measured commitment or it is
	// nothing.
	Realised float64 `json:"realised"`
	// Warming is true while the model has not learned enough to score. A warming
	// model REFUSES rather than scoring low, and the refusal is counted below.
	Warming bool `json:"warming"`
	// Saturated means the appetite cannot be honoured by any threshold because
	// too much of the stream scores in the top band. It is the one state that
	// must never be mistaken for quiet.
	Saturated bool `json:"saturated"`
	// Learned is how many observations this tenant's model has taken in.
	Learned int64 `json:"learned"`
	// Scored is how many observations the model was able to score — the
	// denominator of Realised.
	Scored int64 `json:"scored"`
	// Alerted is how many of those it alerted on — the numerator of Realised.
	Alerted int64 `json:"alerted"`
	// Refusals counts what the model declined to score, by reason. None of these
	// was examined by the model; all of them were examined by the rules.
	Refusals map[string]int64 `json:"refusals"`
	// Blind counts, per feature, how often it took its neutral value for want of
	// data.
	Blind map[string]int64 `json:"blind"`
	// Distribution is the score distribution the threshold is cut from, in 32
	// bands.
	Distribution []float64 `json:"distribution"`
	// Inventory is the feature set, so the state and the shape are read
	// together.
	Inventory []mlFeature `json:"inventory"`
	// Digest is the model identity an auditor pins.
	Digest string `json:"digest"`
}

// mlAppetiteIn sets the appetite.
type mlAppetiteIn struct {
	// Review is the share of the stream that may be examined, in (0, 0.5].
	Review float64 `json:"review"`
	// Sample is the share of non-alerting traffic retained, in [0, 1].
	Sample float64 `json:"sample"`
}

// mlSearchIn asks for an exhaustive topology search.
type mlSearchIn struct {
	// Limit bounds how many recorded observations are replayed, 1..5000. The
	// grid itself is closed and needs no bound from the caller.
	Limit int `json:"limit,omitempty"`
}

// mlCandidate is one point in the topology grid.
type mlCandidate struct {
	// Trees is how many half-space trees the model holds.
	Trees int `json:"trees"`
	// Depth is how deep each tree splits.
	Depth int `json:"depth"`
	// Window is how many observations make up one reference window.
	Window int `json:"window"`
	// Blend is how much of a closing window folds into the reference.
	Blend float64 `json:"blend"`
	// Review is the share of the stream this topology may examine.
	Review float64 `json:"review"`
}

// mlTrial is what one candidate did over the replayed history.
type mlTrial struct {
	// Candidate is the topology tried.
	Candidate mlCandidate `json:"candidate"`
	// Scored is how many observations it was able to score.
	Scored int `json:"scored"`
	// Alerted is how many of those it would have alerted on.
	Alerted int `json:"alerted"`
	// Realised is Alerted over Scored, against the Review share intended.
	Realised float64 `json:"realised"`
	// Separation is the mean score of the alerted set minus the mean of the
	// rest. It is the ranking objective: a topology that separates the tail from
	// the body is doing the job whatever its absolute scores look like.
	Separation float64 `json:"separation"`
	// Warm is how many observations passed before it would score at all.
	Warm int `json:"warm"`
}

// mlSearchRun is the receipt for a started search.
type mlSearchRun struct {
	// ID identifies the run; read it back at GET /v1/ml/search/{id}.
	ID string `json:"id"`
	// Status is "running" or "done".
	Status string `json:"status"`
	// Candidates is how many topologies will be tried.
	Candidates int `json:"candidates"`
}

// mlSearchReport is the answer.
type mlSearchReport struct {
	// ID identifies the run.
	ID string `json:"id"`
	// Status is "running", "done" or "refused".
	Status string `json:"status"`
	// Events is how many historical observations were replayed.
	Events int `json:"events"`
	// Trials is every candidate tried, best-separating first.
	Trials []mlTrial `json:"trials,omitempty"`
	// Winner is the best-separating topology that also honoured its stated
	// appetite. Absent when no candidate did both.
	Winner *mlCandidate `json:"winner,omitempty"`
	// Curve is the winner's separation as a function of how much history it had
	// seen, in ten steps — the learning curve.
	Curve []float64 `json:"curve,omitempty"`
	// Refusal names why the report is empty when it is. An empty history is
	// refused rather than reported as zero alerts, because a quiet model and an
	// unrun model produce the same number.
	Refusal string `json:"refusal,omitempty"`
}

// mlSnapshotOut is the receipt for a pinned model.
type mlSnapshotOut struct {
	// Digest is the model identity that was pinned.
	Digest string `json:"digest"`
	// Learned is how many observations that state had taken in.
	Learned int64 `json:"learned"`
	// At is when it was pinned, RFC 3339 in UTC.
	At string `json:"at"`
}

// ── the ops ─────────────────────────────────────────────────────────────────

// Decide scores one entity or event and answers what to do about it: allow,
// challenge, review, restrict or block, with the evidence behind it.
//
// One op, not three. "Score this thing" is one verb, and a stage-specific op per
// lifecycle moment would be three places for the tenant gate to drift. The stage
// selects the feature window and the rule set and nothing else.
//
// It is METERED AFTER the work, never gated before it. A pre-work balance gate
// would have to render its refusal in the fleet's nested error contract, which a
// typed op cannot do — so the hot path stays typed, the screen is billed on the
// decision that was actually produced, and no funded client's 402 parsing is
// affected. Overage lands on the caller's OWN ledger: Meter sends the org as
// both the commerce identity and the org header, which is the anti-cross-org
// property.
func (o ops) decide(ctx context.Context, in *riskDecideIn) (*riskDecision, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	stage := strings.ToLower(strings.TrimSpace(in.Stage))
	if !stages[stage] {
		return nil, zip.Errorf(400, "%q is not a lifecycle stage", in.Stage)
	}
	kind := strings.ToLower(strings.TrimSpace(in.Subject.Kind))
	if !subjectKinds[kind] {
		return nil, zip.Errorf(400, "%q is not a subject kind", in.Subject.Kind)
	}
	if strings.TrimSpace(in.Subject.ID) == "" {
		return nil, zip.Errorf(400, "the subject names nobody")
	}

	// Idempotency, inside the tenant's own file: the same key returns the same
	// decision rather than scoring twice and moving the counters twice.
	if id, found, err := byIdem(db, in.Idem); err != nil {
		return nil, err
	} else if found {
		return o.decided(db, id)
	}

	obs := observation{
		id:      newID("dec"),
		at:      time.Now().UTC(),
		stage:   stage,
		kind:    kind,
		subject: strings.TrimSpace(in.Subject.ID),
		signals: normaliseSignals(in.Signals),
	}
	if in.Amount != nil {
		obs.amount, obs.currency, obs.direction = in.Amount.Nano, in.Amount.Currency, in.Amount.Direction
	}
	if in.Actor != nil {
		obs.agent, obs.session = in.Actor.Agent, in.Actor.Session
	}
	obs.agency = o.agencyOf(ctx, obs)

	rules, err := loadRules(db)
	if err != nil {
		return nil, err
	}
	lists, err := loadLists(db)
	if err != nil {
		return nil, err
	}
	sups, err := loadSuppressions(db)
	if err != nil {
		return nil, err
	}
	shadow := mode(db) != "live"

	// WHICH MODEL DECIDES. The champion's store, which is the shipped shape until
	// this tenant promotes a fit of its own. A champion whose geometry cannot be
	// housed yields no store, and the decision is then made on rules alone with
	// the model refusing — never silently on a shape nobody promoted.
	store, championFit := champion(o.s, sc.tenant, db)

	out := decide(ctx, o.s.State.vel, store, sc.tenant, obs, rules,
		func(name, value string) bool { return lists[name][strings.ToLower(value)] },
		func(h hit, ob observation) bool {
			now := time.Now()
			for _, s := range sups {
				if s.matches(h, ob, now) {
					return true
				}
			}
			return false
		}, shadow)

	digest := o.s.State.model.Digest()
	// DURABLE FIRST. The decision is the record; the analytics copy comes after
	// and is best-effort. Wired the other way, a bus hiccup loses evidence.
	//
	// A CHALLENGER, IF THERE IS ONE, SCORES THIS SAME OBSERVATION — after the
	// record, off the rings decide() already advanced exactly once, and
	// contributing nothing to what was returned. See lifecycle.go's challenge.
	//
	// The idempotency key is claimed IN the insert, so a concurrent retry loses
	// the race at the index rather than after a second decision exists — and the
	// loser reads back the winner's answer, which is what the key promised.
	if err := putDecision(db, obs, out, digest, in.Idem); err != nil {
		if errors.Is(err, errIdemTaken) {
			id, found, ferr := byIdem(db, in.Idem)
			if ferr != nil || !found {
				return nil, err
			}
			return o.decided(db, id)
		}
		return nil, err
	}

	challenge(o.s, sc, db, obs, out, championFit)

	// METER the screen on the caller's own ledger, after the work.
	o.s.State.bill.Meter(sc.org, sc.project, "screen", screenCents, sc.request, sc.clientIP)

	// The analytics COPY, last and fail-soft.
	emit(sc.org, "risk.decision", map[string]any{
		"stage": obs.stage, "kind": obs.kind, "action": out.action,
		"score": out.score, "agency": out.agency, "shadow": out.shadow,
	})

	return &riskDecision{
		ID: out.id, Action: out.action, Score: out.score, Agency: out.agency,
		Hits: wireHits(out.hits), Causes: wireCauses(out.causes),
		Shadow: out.shadow, Refusal: out.refusal, Model: digest,
	}, nil
}

// screenCents is what one screen costs. It rides the existing rails and adds no
// billing code: the tier (Lite / Standard / Plus / Pro) is a plan in
// hanzoai/plans plus an entitlement capping included screens per month, and this
// is the per-decision overage that lands on the org's own ledger.
const screenCents = 1

// Decisions lists this tenant's recent decisions, newest first. Every filter is
// an equality on a column the tenant's own file owns, so a filter can narrow the
// caller's own log and can never widen it.
func (o ops) decisions(ctx context.Context, in *riskDecisionsIn) (*riskDecisionPage, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	rows, err := decisionsPage(db, in.Kind, in.Subject, in.Action, in.Stage, in.Limit)
	if err != nil {
		return nil, err
	}
	page := &riskDecisionPage{Items: make([]riskDecisionBrief, 0, len(rows))}
	for _, r := range rows {
		page.Items = append(page.Items, brief(r))
	}
	return page, nil
}

// DecisionDetail reads one decision with everything behind it: the evidence, the
// model's per-feature attribution and the digest of the model that produced it.
// That set IS the dispute packet — what was decided, on what, by which model.
//
// A decision another tenant owns answers 404, exactly as an unknown identifier
// does, because the tenant's file is the only place looked in: there is no query
// that could reach another tenant's row, so a probe learns nothing.
func (o ops) decision(ctx context.Context, in *riskRef) (*riskDecisionView, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	return o.decisionView(db, in.ID)
}

// decided renders an ALREADY-RECORDED decision as the decide answer. Both
// idempotency paths go through it, so a retry cannot get a differently-shaped
// body from the original.
func (o ops) decided(db *sql.DB, id string) (*riskDecision, error) {
	view, err := o.decisionView(db, id)
	if err != nil {
		return nil, err
	}
	return &riskDecision{
		ID: view.Decision.ID, Action: view.Decision.Action, Score: view.Decision.Score,
		Agency: view.Decision.Agency, Hits: view.Hits, Causes: view.Causes,
		Shadow: view.Decision.Shadow, Refusal: view.Decision.Refusal, Model: view.Model,
	}, nil
}

func (o ops) decisionView(db *sql.DB, id string) (*riskDecisionView, error) {
	row, hits, causesJSON, digest, err := decisionDetail(db, id)
	if err != nil {
		return nil, err
	}
	var causes []types.Cause
	_ = json.Unmarshal(causesJSON, &causes)
	return &riskDecisionView{
		Decision: brief(row), Hits: wireHits(hits), Causes: wireCauses(causes), Model: digest,
	}, nil
}

// Label records what a human concluded about a decision — fraud, legitimate,
// chargeback, abuse or unknown.
//
// THE DECIDER IS SERVER-SET. It comes from the validated principal and never
// from the request body: the engine's own resolutions carry an unauthenticated
// `by`, and importing that gap into a product that bills on the decision would
// make the attribution worthless the moment anyone asked who cleared what.
//
// This is also the only supervision this product has. Everything the model knows
// about its own miss rate comes from these labels against the below-the-line
// sample.
func (o ops) label(ctx context.Context, in *riskLabelIn) (*riskDecisionView, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	v := strings.ToLower(strings.TrimSpace(in.Verdict))
	if !labels[v] {
		return nil, zip.Errorf(400, "%q is not a verdict", in.Verdict)
	}
	if err := label(db, in.ID, v, by(ctx, sc)); err != nil {
		return nil, err
	}
	emit(sc.org, "risk.label", map[string]any{"verdict": v})
	return o.decisionView(db, in.ID)
}

// SubjectState reads one subject's current risk state: every live velocity
// aggregate it has, its recent decisions, whatever controls are declared on it,
// its thirty-day history, and where this KIND of subject sits across the
// platform. This is the continuous-monitoring read — a merchant, an account or
// an agent, on one page.
//
// The last two come from the warehouse and are BEST EFFORT: an unreachable
// warehouse omits them and names the gap, because a zeroed history would say the
// subject did nothing and a zeroed baseline would say the platform did.
//
// The network comparison is the only cross-org value this API returns and it is
// aggregate BY CONSTRUCTION: the table has no tenant column, so there is no
// query — here or anywhere — that could return another tenant's rows.
func (o ops) subject(ctx context.Context, in *riskSubjectRef) (*riskSubjectView, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if !subjectKinds[kind] {
		return nil, zip.Errorf(400, "%q is not a subject kind", in.Kind)
	}
	view := &riskSubjectView{Subject: riskSubject{Kind: kind, ID: in.ID}}
	view.Velocity = velocityOf(o.s.State.vel, sc.tenant, observation{kind: kind, subject: in.ID})

	rows, err := decisionsPage(db, kind, in.ID, "", "", 50)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		view.Decisions = append(view.Decisions, brief(r))
	}
	cs, err := loadControls(db, kind, in.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		view.Controls = append(view.Controls, wireControl(c))
	}

	// The warehouse reads. Both take the MINTED tenant (the history) or no
	// tenant at all (the baseline, which has none to take) — the two shapes the
	// isolation argument rests on.
	now := time.Now().UTC()
	buckets, err := window(ctx, sc.tenant, kind, in.ID, now.AddDate(0, 0, -30), now)
	if err != nil {
		view.Gap = err.Error()
	} else {
		for _, r := range buckets {
			view.History = append(view.History, riskHistory{
				Bucket: r.bucket.UTC().Format(time.RFC3339), Values: r.values,
			})
		}
	}
	bands, err := baseline(ctx, kind, now.AddDate(0, 0, -1))
	if err != nil {
		if view.Gap == "" {
			view.Gap = err.Error()
		}
	} else {
		for _, b := range bands {
			view.Network = append(view.Network, riskBaseline{
				Feature: b.feature, Q10: b.q10, Q50: b.q50, Q90: b.q90, Q99: b.q99,
				Orgs: b.orgs, Observations: b.n, Day: b.bucket.UTC().Format("2006-01-02"),
			})
		}
	}
	return view, nil
}

// Activity is the watcher: what is firing right now, what is being muted, and
// which way the decisions are going.
//
// It reads the DECISION LOG rather than a second bus. The log is already the
// record and the analytics copies already reach the platform stream; a dedicated
// activation bus would be a third representation of one fact and a third thing
// that can be behind.
func (o ops) activity(ctx context.Context, in *riskActivityIn) (*riskActivityView, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := decisionsPage(db, "", "", "", "", limit)
	if err != nil {
		return nil, err
	}
	view := &riskActivityView{
		Sampled:  len(rows),
		Actions:  map[string]int{},
		Agency:   map[string]int{},
		Refusals: map[string]int{},
		Shadow:   mode(db) != "live",
	}
	perRule := map[string]*riskActivityRule{}
	for _, r := range rows {
		view.Actions[r.Action]++
		view.Agency[r.Agency]++
		if r.Refusal != "" {
			view.Refusals[r.Refusal]++
		}
		_, hits, _, _, err := decisionDetail(db, r.ID)
		if err != nil {
			continue
		}
		for _, h := range hits {
			a := perRule[h.Rule]
			if a == nil {
				a = &riskActivityRule{Rule: h.Rule, Name: h.Name}
				perRule[h.Rule] = a
			}
			a.Activations++
			if h.Suppressed {
				a.Suppressed++
			}
		}
	}
	for _, a := range perRule {
		view.Rules = append(view.Rules, *a)
	}
	sort.SliceStable(view.Rules, func(i, j int) bool {
		if view.Rules[i].Activations != view.Rules[j].Activations {
			return view.Rules[i].Activations > view.Rules[j].Activations
		}
		return view.Rules[i].Rule < view.Rules[j].Rule
	})
	return view, nil
}

// Simulate replays a candidate rule over this tenant's OWN recorded decisions
// and reports what it would have caught, what it would have given up, and how
// much of that a human has already judged.
//
// It is a gate and not a nicety: a new typology has to be tested before live
// activation, and a retirement has to be justified against the outgoing rule's
// performance. Nothing is written — the replay reads the decision log and
// evaluates in memory.
//
// An EMPTY history is REFUSED rather than reported as zero alerts. "No alerts"
// is exactly what a quiet rule looks like, and being unable to tell the two
// apart is the failure a sandbox exists to prevent.
func (o ops) simulate(ctx context.Context, in *riskSimulateIn) (*riskSimulateReport, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	cand := fromWireRule(in.Candidate)
	cand.Enabled = true // activated whatever the flag says: the question is what happens ON activation
	if err := admit(cand); err != nil {
		return nil, zip.Errorf(400, "%s", err.Error())
	}
	limit := in.Limit
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	hist, err := replayHistory(db, limit)
	if err != nil {
		return nil, err
	}
	if len(hist) == 0 {
		return &riskSimulateReport{Refusal: errNoHistory.Error()}, nil
	}

	lists, err := loadLists(db)
	if err != nil {
		return nil, err
	}
	member := func(name, value string) bool { return lists[name][strings.ToLower(value)] }

	rep := &riskSimulateReport{Events: len(hist)}
	var judged, fp int
	for _, h := range hist {
		f := h.facts
		f.lists = member
		fired := len(evaluate([]rule{cand}, h.obs.stage, f)) > 0
		was := contains(h.rules, in.Incumbent)
		switch {
		case fired && was:
			rep.Kept = append(rep.Kept, h.obs.id)
		case fired && !was:
			rep.Added = append(rep.Added, h.obs.id)
		case !fired && was:
			rep.Dropped = append(rep.Dropped, h.obs.id)
		}
		if fired {
			rep.Alerts++
			if h.label != "" {
				judged++
				if h.label == "legitimate" {
					fp++
				}
			}
		}
	}
	rep.Judged = judged
	if judged > 0 {
		v := round4(float64(fp) / float64(judged))
		rep.FalsePositive = &v
	}
	return rep, nil
}

// Rules lists this tenant's detections. A tenant starts with a starter set
// covering the lifecycle the product names — signup burst, shared device,
// disposable email, card testing, denied address, spend spike, payout velocity,
// undeclared automation — all of which it may read, copy, edit and retire.
func (o ops) rules(ctx context.Context, _ *riskNoInput) (*riskRuleList, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	rs, err := loadRules(db)
	if err != nil {
		return nil, err
	}
	out := &riskRuleList{Items: make([]riskRule, 0, len(rs))}
	for _, r := range rs {
		out.Items = append(out.Items, toWireRule(r))
	}
	return out, nil
}

// CreateRule admits a new detection. Admission is strict on purpose: a rule with
// no terms holds on everything, a rule naming a field that does not exist holds
// on nothing, and both read as a working control from the outside.
func (o ops) createRule(ctx context.Context, in *riskRuleIn) (*riskRule, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	r := fromWireRule(in.Rule)
	r.ID = newID("rule")
	if err := admit(r); err != nil {
		return nil, zip.Errorf(400, "%s", err.Error())
	}
	if err := putRule(db, r); err != nil {
		return nil, err
	}
	emit(sc.org, "risk.rule.created", map[string]any{"rule": r.ID, "stage": r.Stage, "action": r.Action})
	w := toWireRule(r)
	return &w, nil
}

// UpdateRule replaces a detection whole. There is no partial update: a rule is a
// conjunction of terms, and merging a partial term list into an existing one is
// a change nobody can read off the request.
func (o ops) updateRule(ctx context.Context, in *riskRuleIn) (*riskRule, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if _, err := getRule(db, in.ID); err != nil {
		return nil, err
	}
	r := fromWireRule(in.Rule)
	r.ID = in.ID
	if err := admit(r); err != nil {
		return nil, zip.Errorf(400, "%s", err.Error())
	}
	if err := putRule(db, r); err != nil {
		return nil, err
	}
	w := toWireRule(r)
	return &w, nil
}

// DeleteRule retires a detection. Answers 204, or 404 for an identifier this
// tenant does not own.
func (o ops) deleteRule(ctx context.Context, in *riskRef) (*struct{}, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := dropRule(db, in.ID); err != nil {
		return nil, err
	}
	return &struct{}{}, nil
}

// Lists shows this tenant's allow and deny lists and how many values each holds.
//
// These are the TENANT's operational lists. Sanctions and PEP designations are
// not here and are never merged into them: a tenant may add to its own deny
// list, and a tenant may not edit OFAC.
func (o ops) lists(ctx context.Context, _ *riskNoInput) (*riskListPage, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	items, err := listNames(db)
	if err != nil {
		return nil, err
	}
	return &riskListPage{Items: items}, nil
}

// CreateList creates an allow or deny list a rule can name in an inlist term.
func (o ops) createList(ctx context.Context, in *riskListIn) (*riskListView, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if name == "" {
		return nil, zip.Errorf(400, "a list needs a name")
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if kind != "allow" && kind != "deny" {
		return nil, zip.Errorf(400, "a list either allows or denies")
	}
	if err := putList(db, name, kind); err != nil {
		return nil, err
	}
	return &riskListView{Name: name, Kind: kind, CreatedAt: stamp(time.Now())}, nil
}

// AddListEntries adds values to a list. Values are folded to lower case so a
// value added in one case matches a signal sent in another.
func (o ops) addEntries(ctx context.Context, in *riskListEntriesIn) (*riskListView, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := addEntries(db, strings.ToLower(in.Name), in.Values, by(ctx, sc)); err != nil {
		return nil, err
	}
	items, err := listNames(db)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.Name == strings.ToLower(in.Name) {
			v := it
			return &v, nil
		}
	}
	return nil, zip.ErrNotFound("no such list")
}

// RemoveListEntry removes one value from a list. Answers 204, or 404 when the
// list does not hold it.
func (o ops) removeEntry(ctx context.Context, in *riskListEntryRef) (*struct{}, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := dropEntry(db, strings.ToLower(in.Name), in.Value); err != nil {
		return nil, err
	}
	return &struct{}{}, nil
}

// Suppressions lists the active mutes. A suppression that has expired is not
// listed and mutes nothing — a forgotten mute stops muting instead of quietly
// staying on forever.
func (o ops) suppressions(ctx context.Context, _ *riskNoInput) (*riskSuppressionPage, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	ss, err := loadSuppressions(db)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	page := &riskSuppressionPage{Items: []riskSuppression{}}
	for _, s := range ss {
		if !s.Until.IsZero() && now.After(s.Until) {
			continue
		}
		page.Items = append(page.Items, wireSuppression(s))
	}
	return page, nil
}

// Suppress mutes a rule's activations for a subject, a subject kind, or
// everywhere that rule fires.
//
// A suppressed hit is RECORDED with suppressed set and contributes nothing to
// the action. It is never dropped, for the same reason the model counts its
// refusals: silence must never read as a clean result, and a compliance record
// that can be silently muted by an operational knob is not a record.
func (o ops) suppress(ctx context.Context, in *riskSuppressIn) (*riskSuppression, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if in.Rule == "" && in.Kind == "" && in.Subject == "" {
		return nil, zip.Errorf(400, "a suppression that names nothing would mute everything")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, zip.Errorf(400, "a mute nobody can explain later is a control that quietly stopped")
	}
	s := suppression{
		ID: newID("sup"), Rule: in.Rule, Kind: in.Kind, Subject: in.Subject,
		Reason: in.Reason, By: by(ctx, sc), At: time.Now().UTC(),
	}
	if in.Until != "" {
		u, err := time.Parse(time.RFC3339, in.Until)
		if err != nil {
			return nil, zip.Errorf(400, "until is not an RFC 3339 time")
		}
		s.Until = u
	}
	if err := putSuppression(db, s); err != nil {
		return nil, err
	}
	emit(sc.org, "risk.suppressed", map[string]any{"rule": s.Rule, "kind": s.Kind})
	w := wireSuppression(s)
	return &w, nil
}

// Unsuppress lifts a mute. Answers 204, or 404 for an identifier this tenant
// does not own.
func (o ops) unsuppress(ctx context.Context, in *riskRef) (*struct{}, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := dropSuppression(db, in.ID); err != nil {
		return nil, err
	}
	return &struct{}{}, nil
}

// Controls lists the platform controls declared on this tenant's subjects:
// reserves, payout holds and blocks.
//
// They are DECLARATIONS the money plane reads. Risk never moves money —
// hanzoai/commerce owns payouts, disputes and balances — so a marketplace reads
// this before a payout and calls decide at authorization, and risk touches no
// processor. That is what "processor-agnostic" is: a structural property, not a
// feature.
func (o ops) controls(ctx context.Context, in *riskControlsIn) (*riskControlPage, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	cs, err := loadControls(db, in.Kind, in.Subject)
	if err != nil {
		return nil, err
	}
	page := &riskControlPage{Items: make([]riskControl, 0, len(cs))}
	for _, c := range cs {
		page.Items = append(page.Items, wireControl(c))
	}
	return page, nil
}

// SetControl declares a reserve, a payout hold or a block on a subject.
func (o ops) setControl(ctx context.Context, in *riskControlIn) (*riskControl, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	kind := strings.ToLower(strings.TrimSpace(in.Subject.Kind))
	if !subjectKinds[kind] {
		return nil, zip.Errorf(400, "%q is not a subject kind", in.Subject.Kind)
	}
	if !controlKinds[in.Control] {
		return nil, zip.Errorf(400, "%q is not a control", in.Control)
	}
	if in.Rate < 0 || in.Rate > 1 {
		return nil, zip.Errorf(400, "a reserve rate is a fraction in [0,1]")
	}
	c := control{
		ID: newID("ctl"), Kind: kind, Subject: in.Subject.ID, Control: in.Control,
		Rate: in.Rate, Reason: in.Reason, By: by(ctx, sc), At: time.Now().UTC(),
	}
	if in.Until != "" {
		u, err := time.Parse(time.RFC3339, in.Until)
		if err != nil {
			return nil, zip.Errorf(400, "until is not an RFC 3339 time")
		}
		c.Until = u
	}
	if err := putControl(db, c); err != nil {
		return nil, err
	}
	emit(sc.org, "risk.control", map[string]any{"control": c.Control, "kind": c.Kind})
	w := wireControl(c)
	return &w, nil
}

// ReleaseControl lifts a control. Answers 204, or 404 for an identifier this
// tenant does not own.
func (o ops) releaseControl(ctx context.Context, in *riskRef) (*struct{}, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := dropControl(db, in.ID); err != nil {
		return nil, err
	}
	return &struct{}{}, nil
}

// Dictionary is the vocabulary a rule may be written in: every fact, every
// operator, every velocity axis and window, this tenant's own lists, and the
// signal keys this tenant has actually sent.
//
// Two lenses on one catalogue. The product half is closed and is what admission
// checks against. The tenant half — the signals — is the tenant's own, read back
// off its recent decisions, which is what makes a rule builder able to offer the
// fields that exist here rather than the fields that exist in general.
func (o ops) dictionary(ctx context.Context, _ *riskNoInput) (*riskDictionaryView, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	view := &riskDictionaryView{
		Stages:  keys(stages),
		Actions: []string{ActionAllow, ActionChallenge, ActionReview, ActionRestrict, ActionBlock},
		Axes:    keys(velocityAxes),
		Windows: []string{"1h", "24h", "7d", "30d"},
	}
	for _, f := range keys(facts) {
		view.Fields = append(view.Fields, riskField{
			Field: f, Kind: factKind(f), Ops: opsFor(factKind(f)), Note: factNote(f),
		})
	}
	items, err := listNames(db)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		view.Lists = append(view.Lists, it.Name)
	}
	seen, err := seenSignals(db)
	if err != nil {
		return nil, err
	}
	view.Signals = seen
	return view, nil
}

// Mode reports whether this tenant's decisions act or only observe.
func (o ops) getMode(ctx context.Context, _ *riskNoInput) (*riskModeView, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	return &riskModeView{Mode: mode(db), Since: getSetting(db, "mode_at")}, nil
}

// SetMode takes this tenant live, or returns it to shadow.
//
// SHADOW IS THE DEFAULT AND THE DEFAULT IS NOT CONFIGURABLE. In shadow every
// rule runs, the model scores and learns, every decision is recorded, and the
// action is always allow — so what would have happened is readable over real
// traffic before anyone depends on it. A model that quietly went live and
// started declining payments is the worst failure available here.
func (o ops) setMode(ctx context.Context, in *riskModeIn) (*riskModeView, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	m := strings.ToLower(strings.TrimSpace(in.Mode))
	if m != "shadow" && m != "live" {
		return nil, zip.Errorf(400, "a tenant either observes or acts")
	}
	if err := setSetting(db, "mode", m); err != nil {
		return nil, err
	}
	now := stamp(time.Now())
	if err := setSetting(db, "mode_at", now); err != nil {
		return nil, err
	}
	emit(sc.org, "risk.mode", map[string]any{"mode": m, "by": by(ctx, sc)})
	return &riskModeView{Mode: m, Since: now}, nil
}

// ── /v1/ml ──────────────────────────────────────────────────────────────────

// Score scores one observation and LEARNS NOTHING from it. It is how a candidate
// is tried against a tenant's real behaviour before anything depends on the
// answer, and it is the model's analogue of testing a rule: because it records
// nothing, the aggregates it reads do not include the candidate.
func (o ops) score(ctx context.Context, in *mlScoreIn) (*mlScoreOut, error) {
	sc, _, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	obs, err := fromWireObservation(in.Observation)
	if err != nil {
		return nil, err
	}
	tx, ent := txOf(sc.tenant, obs)
	a := o.s.State.model.Inspect(tx, ent)
	return &mlScoreOut{
		Scored: a.Scored, Refusal: a.Reason, Score: round4(a.Score), Cut: round4(a.Cut),
		Alert: a.Alert, Shadow: a.Shadow, Causes: wireCauses(a.Causes),
		Values: wireValues(a.Values), Model: o.s.State.model.Digest(),
	}, nil
}

// Train teaches this tenant's model from this tenant's own observations.
//
// It is online: there is no job, no queue and no window in which the tenant is
// protected by a stale model, because the half-space geometry is built before
// any data arrives and the model IS a set of mass counters. One observation is
// one increment.
//
// THERE IS NO CROSS-TENANT TRAINING INPUT AND NO WAY TO EXPRESS ONE. The model
// is indexed by the tenant key and its tree GEOMETRY is seeded from it, so two
// tenants do not merely hold different counters — they hold different trees.
// Any cross-org learning in this product is aggregate-only, lives in a table
// with no tenant column, and is published only above a k-anonymity floor.
func (o ops) train(ctx context.Context, in *mlTrainIn) (*mlTrainOut, error) {
	sc, _, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if len(in.Observations) == 0 {
		return nil, zip.Errorf(400, "nothing to learn from")
	}
	if len(in.Observations) > 1000 {
		return nil, zip.Errorf(400, "at most 1000 observations per call")
	}
	// Gate BEFORE the work: training a thousand observations is real CPU on a
	// shared pod, so an unfunded org is refused before it spends it. This is a
	// NEW route, so no client parses a nested 402 body from it and the typed
	// refusal costs nothing.
	if err := o.s.State.bill.Gate(ctx, sc.org, sc.project, sc.validate, "train", trainCents); err != nil {
		return nil, zip.Errorf(402, "%s", err.Error())
	}

	out := &mlTrainOut{Refused: map[string]int{}}
	for _, w := range in.Observations {
		obs, err := fromWireObservation(w)
		if err != nil {
			out.Refused["malformed"]++
			continue
		}
		record(o.s.State.vel, sc.tenant, obs)
		tx, ent := txOf(sc.tenant, obs)
		a := o.s.State.model.Inspect(tx, ent)
		if !a.Scored && a.Reason != "" && a.Reason != RefusalWarming {
			out.Refused[a.Reason]++
		}
		// Assess is the LEARNING path; in shadow it contributes no hit and the
		// counters still move, which is exactly training.
		_, _ = o.s.State.model.Assess(tx, ent)
		out.Learned++
	}
	st := o.s.State.model.State(sc.tenant.String())
	out.Warm, out.Model = st.Warm, st.Digest
	o.s.State.bill.Meter(sc.org, sc.project, "train", trainCents, sc.request, sc.clientIP)
	return out, nil
}

// trainCents is what one training call costs. Per call rather than per
// observation, because the batch is bounded above.
const trainCents = 1

// ModelState is what a review of the model reads: the appetite it was given, the
// threshold that appetite produced, and the share it ACTUALLY reached.
//
// Stated against realised is the whole governance report. An appetite is a
// measured commitment or it is nothing, and a fixed threshold on a drifting
// distribution silently becomes either silence or a flood.
func (o ops) modelState(ctx context.Context, _ *mlNoInput) (*mlModelState, error) {
	sc, _, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	st := o.s.State.model.State(sc.tenant.String())
	return wireState(st), nil
}

// SetAppetite sets how much of the stream the model may examine.
//
// Review is the lever, and the threshold is derived from it as a quantile of the
// scores actually observed rather than fixed at a number someone liked. That is
// what makes an alert level governed rather than tuned, and it is why the level
// cannot be set to fit the size of the review team.
func (o ops) setAppetite(ctx context.Context, in *mlAppetiteIn) (*mlModelState, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if in.Review <= 0 || in.Review > 0.5 {
		return nil, zip.Errorf(400, "review is a share in (0, 0.5]")
	}
	if in.Sample < 0 || in.Sample > 1 {
		return nil, zip.Errorf(400, "sample is a share in [0, 1]")
	}
	body, _ := json.Marshal(mlAppetite{Review: in.Review, Sample: in.Sample})
	if err := setSetting(db, "appetite", string(body)); err != nil {
		return nil, err
	}
	// The engine's appetite is a Store-level configuration, so a per-tenant
	// appetite is recorded here and reported alongside the model's own. Saying
	// so is better than pretending a per-tenant lever exists that does not: the
	// stored value is what a search optimises against and what the next
	// deployment-level change is measured from.
	st := o.s.State.model.State(sc.tenant.String())
	out := wireState(st)
	out.Appetite = mlAppetite{Review: in.Review, Sample: in.Sample, Warm: out.Appetite.Warm}
	return out, nil
}

// Features is the typology-to-feature inventory the model is built on: what each
// dimension measures, the risk indicator it serves, and the published source
// those words come from.
//
// It is code rather than a document because a mapping kept beside the model
// cannot drift away from what the model actually reads — and the attributability
// it makes possible is why this is a tree and not a net.
func (o ops) features(ctx context.Context, _ *mlNoInput) (*mlFeatureInventory, error) {
	if _, _, err := tenantState(ctx, o.s); err != nil {
		return nil, err
	}
	inv := anomaly.Inventory()
	out := &mlFeatureInventory{Digest: o.s.State.model.Digest()}
	for _, f := range inv {
		out.Items = append(out.Items, mlFeature{
			Name: f.Name, Window: f.Window, Typology: f.Typology, Indicator: f.Indicator,
			Citation: f.Citation, Severity: f.Severity, Unit: f.Unit, Neutral: f.Neutral,
		})
	}
	return out, nil
}

// Search exhaustively tries the model topology over this tenant's own history
// and returns the learning curve and the winning shape.
//
// The grid is CLOSED — trees x depth x window x blend x appetite — because an
// unbounded grid is an unbounded amount of a shared pod's CPU reachable by
// anyone with a key. Each candidate runs in a sandbox over fresh counters, so a
// search can never move the live model, and no candidate can see another
// tenant's history because the replay reads this tenant's own file.
//
// It answers 202 with a run identifier; read the report back at
// GET /v1/ml/search/{id}.
func (o ops) search(ctx context.Context, in *mlSearchIn) (*mlSearchRun, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := o.s.State.bill.Gate(ctx, sc.org, sc.project, sc.validate, "search", searchCents); err != nil {
		return nil, zip.Errorf(402, "%s", err.Error())
	}
	limit := in.Limit
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	hist, err := replayHistory(db, limit)
	if err != nil {
		return nil, err
	}
	obs := make([]observation, 0, len(hist))
	for _, h := range hist {
		obs = append(obs, h.obs)
	}

	id := newID("search")
	run := &mlSearchRun{ID: id, Status: "running", Candidates: len(candidates())}
	if len(obs) == 0 {
		run.Status = "refused"
		body, _ := json.Marshal(searchReport{Refusal: errNoHistory.Error()})
		if err := putSearch(db, id, "refused", body); err != nil {
			return nil, err
		}
		return run, nil
	}
	body, _ := json.Marshal(searchReport{Events: len(obs)})
	if err := putSearch(db, id, "running", body); err != nil {
		return nil, err
	}

	tenant := sc.tenant
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		rep, err := searchRun(bg, tenant, obs)
		status := "done"
		if err != nil {
			status = "refused"
			rep.Refusal = err.Error()
		}
		b, _ := json.Marshal(rep)
		if err := putSearch(db, id, status, b); err != nil {
			o.s.Log.Error("risk: a search report could not be kept", "search", id, "err", err)
		}
	}()
	o.s.State.bill.Meter(sc.org, sc.project, "search", searchCents, sc.request, sc.clientIP)
	return run, nil
}

// searchCents is what one exhaustive search costs. It is priced well above a
// screen because it is minutes of CPU rather than microseconds.
const searchCents = 100

// SearchResult reads an exhaustive search's report: every candidate tried, the
// winner, and the winner's learning curve.
func (o ops) searchResult(ctx context.Context, in *mlRef) (*mlSearchReport, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	status, body, err := getSearch(db, in.ID)
	if err != nil {
		return nil, err
	}
	var rep searchReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return nil, err
	}
	out := &mlSearchReport{
		ID: in.ID, Status: status, Events: rep.Events, Curve: rep.Curve, Refusal: rep.Refusal,
	}
	for _, t := range rep.Trials {
		out.Trials = append(out.Trials, mlTrial{
			Candidate: mlCandidate(t.Candidate), Scored: t.Scored, Alerted: t.Alerted,
			Realised: t.Realised, Separation: t.Separation, Warm: t.Warm,
		})
	}
	if rep.Winner != nil {
		w := mlCandidate(*rep.Winner)
		out.Winner = &w
	}
	return out, nil
}

// Snapshot pins this tenant's learned state into its own encrypted file.
//
// It is what an auditor asks for: the exact model that raised an alert, kept so
// that the same input can be scored again a year later. It is also what makes a
// rollout survivable — the shutdown path snapshots every resident tenant,
// because cloud deploys Recreate at one replica and a model that comes back with
// nothing learned refuses to score for its whole warm period.
func (o ops) snapshot(ctx context.Context, _ *mlNoInput) (*mlSnapshotOut, error) {
	sc, _, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := saveModel(o.s.State.shelf, o.s.State.model, sc.tenant); err != nil {
		return nil, err
	}
	st := o.s.State.model.State(sc.tenant.String())
	return &mlSnapshotOut{Digest: st.Digest, Learned: st.Learned, At: stamp(time.Now())}, nil
}

// Restore reinstates this tenant's learned state from its pinned snapshot.
//
// A snapshot whose shape does not match the running inventory is REFUSED, not
// coerced, and a snapshot belonging to another tenant is refused twice — here,
// by the tenant that asked, and again inside the engine. State the model would
// treat as its own memory has to have come from this algorithm over this feature
// set for this tenant.
func (o ops) restore(ctx context.Context, _ *mlNoInput) (*mlModelState, error) {
	sc, _, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if err := loadModel(o.s.State.shelf, o.s.State.model, sc.tenant); err != nil {
		return nil, zip.Errorf(409, "%s", err.Error())
	}
	st := o.s.State.model.State(sc.tenant.String())
	return wireState(st), nil
}

// ── conversions ─────────────────────────────────────────────────────────────

func brief(r decisionRow) riskDecisionBrief {
	return riskDecisionBrief{
		ID: r.ID, At: r.At, Stage: r.Stage, Kind: r.Kind, Subject: r.Subject,
		Action: r.Action, Score: r.Score, Agency: r.Agency, Shadow: r.Shadow,
		Refusal: r.Refusal, Label: r.Label,
	}
}

func wireHits(hs []hit) []riskHit {
	out := make([]riskHit, 0, len(hs))
	for _, h := range hs {
		out = append(out, riskHit(h))
	}
	return out
}

func wireCauses(cs []types.Cause) []riskCause {
	if len(cs) == 0 {
		return nil
	}
	out := make([]riskCause, 0, len(cs))
	for _, c := range cs {
		out = append(out, riskCause{
			Feature: c.Feature, Typology: c.Typology, Indicator: c.Indicator,
			Citation: c.Citation, Unit: c.Unit, Observed: c.Observed,
			Baseline: c.Baseline, Without: c.Without, Share: c.Share,
		})
	}
	return out
}

func wireValues(vs []anomaly.Value) []mlValue {
	out := make([]mlValue, 0, len(vs))
	for _, v := range vs {
		out = append(out, mlValue{
			Feature: v.Feature, X: round4(v.X), Observed: v.Observed,
			Baseline: v.Baseline, Unit: v.Unit, Blind: v.Blind,
		})
	}
	return out
}

func wireState(st anomaly.State) *mlModelState {
	out := &mlModelState{
		Appetite: mlAppetite{
			Review: st.Config.Appetite.Review,
			Sample: st.Config.Appetite.Sample,
			Warm:   st.Config.Appetite.Warm,
		},
		Threshold: round4(st.Cut), Realised: round4(st.Realised),
		Warming: !st.Warm, Saturated: st.Saturated,
		Learned: st.Learned, Scored: st.Scored, Alerted: st.Alerted,
		Refusals: st.Refused, Blind: st.Blind, Distribution: st.Distribution,
		Digest: st.Digest,
	}
	for _, f := range st.Inventory {
		out.Inventory = append(out.Inventory, mlFeature{
			Name: f.Name, Window: f.Window, Typology: f.Typology, Indicator: f.Indicator,
			Citation: f.Citation, Severity: f.Severity, Unit: f.Unit, Neutral: f.Neutral,
		})
	}
	return out
}

func wireSuppression(s suppression) riskSuppression {
	w := riskSuppression{
		ID: s.ID, Rule: s.Rule, Kind: s.Kind, Subject: s.Subject,
		Reason: s.Reason, By: s.By, At: stamp(s.At),
	}
	if !s.Until.IsZero() {
		w.Until = stamp(s.Until)
	}
	return w
}

func wireControl(c control) riskControl {
	w := riskControl{
		ID: c.ID, Subject: riskSubject{Kind: c.Kind, ID: c.Subject}, Control: c.Control,
		Rate: c.Rate, Reason: c.Reason, By: c.By, At: stamp(c.At),
	}
	if !c.Until.IsZero() {
		w.Until = stamp(c.Until)
	}
	return w
}

func toWireRule(r rule) riskRule {
	w := riskRule{
		ID: r.ID, Name: r.Name, Stage: r.Stage, Action: r.Action,
		Weight: r.Weight, Severity: r.Severity, Enabled: r.Enabled,
	}
	for _, t := range r.All {
		w.All = append(w.All, riskTerm(t))
	}
	return w
}

func fromWireRule(w riskRule) rule {
	r := rule{
		ID: w.ID, Name: w.Name, Stage: w.Stage, Action: w.Action,
		Weight: w.Weight, Severity: w.Severity, Enabled: w.Enabled,
	}
	for _, t := range w.All {
		r.All = append(r.All, term(t))
	}
	return r
}

func fromWireObservation(w mlObservation) (observation, error) {
	kind := strings.ToLower(strings.TrimSpace(w.Subject.Kind))
	if !subjectKinds[kind] {
		return observation{}, zip.Errorf(400, "%q is not a subject kind", w.Subject.Kind)
	}
	if strings.TrimSpace(w.Subject.ID) == "" {
		return observation{}, zip.Errorf(400, "the observation names nobody")
	}
	o := observation{
		id: newID("obs"), at: time.Now().UTC(), kind: kind,
		subject: strings.TrimSpace(w.Subject.ID), signals: normaliseSignals(w.Signals),
	}
	if w.At != "" {
		t, err := time.Parse(time.RFC3339, w.At)
		if err != nil {
			return observation{}, zip.Errorf(400, "at is not an RFC 3339 time")
		}
		o.at = t.UTC()
	}
	if w.Amount != nil {
		o.amount, o.currency, o.direction = w.Amount.Nano, w.Amount.Currency, w.Amount.Direction
	}
	return o, nil
}

func txOf(t Tenant, o observation) (types.Transaction, types.Entity) {
	return types.Transaction{
			ID: o.id, OrgID: t.String(), UserID: o.subject, AccountID: o.subject,
			Currency: o.currency, Direction: o.direction,
			IPAddress: o.signals["ip"], DeviceFingerprint: o.signals["device"],
			Counterparty: o.signals["counterparty"], Timestamp: o.at, USD: nanoUSD(o.amount),
		}, types.Entity{
			ID: o.subject, OrgID: t.String(),
		}
}

// normaliseSignals folds keys to lower case so `IP` and `ip` are one signal, and
// drops empty values so a rule's exists term means what it says.
func normaliseSignals(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// by names the actor from the VALIDATED principal. Never off the wire: the
// engine's own resolutions carry an unauthenticated decider, and importing that
// gap into a product that bills on the decision would make every attribution
// worthless.
func by(ctx context.Context, sc scope) string {
	if c, ok := cloud.Request(ctx); ok {
		if u := c.User(); u != "" {
			return u
		}
	}
	return sc.org
}

// agencyOf classifies the actor. It reads the credential class off the request
// the identity boundary already validated, and the declaration off THIS org's
// own registry — facts nobody who does not run agents can compute.
func (o ops) agencyOf(ctx context.Context, obs observation) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return AgencyUnknown
	}
	credentialed := c.User() != "" || c.Org() != ""
	// A publishable key resolves an ORG and no user: by construction it
	// authenticates nobody, so it can never be an agent.
	publishable := c.User() == "" && c.Org() != ""
	declared := strings.TrimSpace(obs.agent) != ""
	humanSession := c.User() != "" && obs.session != "" && obs.agent == ""
	return classify(credentialed, publishable, declared, humanSession)
}

// velocityOf renders every live aggregate a subject has.
func velocityOf(vel *velocity.Store, t Tenant, o observation) []riskVelocity {
	out := []riskVelocity{}
	for _, axis := range keys(velocityAxes) {
		v := o.axisOf(axis)
		if v == "" {
			continue
		}
		for _, ob := range vel.Observe(velocity.Key{OrgID: t.String(), Kind: axis, Value: v}) {
			out = append(out, riskVelocity{
				Axis: axis, Window: ob.Window, Count: ob.Count,
				Sum: ob.Sum, Near: ob.Near, Days: ob.Days,
			})
		}
	}
	return out
}

// keys returns a map's keys in a stable order, so a published catalogue does not
// reshuffle between calls.
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func factKind(f string) string {
	switch f {
	case "amount.nano", "model.score":
		return "number"
	default:
		return "string"
	}
}

func opsFor(kind string) []string {
	if kind == "number" {
		return []string{OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpExists, OpAbsent}
	}
	return []string{OpEq, OpNe, OpIn, OpNotIn, OpInList, OpNotList, OpContains, OpPrefix, OpSuffix, OpExists, OpAbsent}
}

func factNote(f string) string {
	switch f {
	case "stage":
		return "the lifecycle moment being judged"
	case "subject.kind":
		return "the sort of thing being judged"
	case "subject.id":
		return "the subject's identifier in your own system"
	case "agency":
		return "agent, human, bot or unknown — derived server-side, never from a user agent"
	case "amount.nano":
		return "the amount in billionths of one currency unit"
	case "amount.currency":
		return "the ISO 4217 code"
	case "amount.direction":
		return "in or out"
	case "model.score":
		return "the anomaly score this observation received, in [0,1]"
	case "model.warming":
		return "true while the model has not learned enough of your traffic to score"
	case "actor.agent":
		return "the agent reference the caller declared"
	case "actor.session":
		return "the session reference this action belongs to"
	}
	return ""
}
