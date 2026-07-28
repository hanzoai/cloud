package content

// lifecycle.go is the ONE marketing-content state machine — a pure value, defined
// once, read by two separated concerns:
//
//   - the framework before_save HOOK (hooks.go) enforces edge legality on EVERY
//     write to a publishable DocType, including a raw PUT /v1/framework/:doctype —
//     so an illegal status jump is impossible at the storage boundary; and
//   - the /v1/content transition ENDPOINT (content.go) composes the same table with
//     the orchestration side effects (fan-out to distribution on queued/published).
//
// Decomplected (values, not places): the RULE lives here as data; ENFORCEMENT (the
// hook) and ORCHESTRATION (the endpoint) are distinct consumers that both read it.
// Neither owns the rule; changing an edge is a one-line change in one map.
//
// The states subsume the bespoke karma lifecycle (catalog draft→approved→published,
// marketing draft→queued→published) and add the missing editorial gate `in_review`:
//
//	draft ──▶ in_review ──▶ approved ──▶ queued ──▶ published
//	  ▲          │             │           │            │
//	  └──────────┴─────────────┘           │            ▼
//	                (send back)            └────────▶ archived ──▶ draft (reopen)
const (
	StatusDraft     = "draft"     // authored / generated, not yet submitted
	StatusInReview  = "in_review" // submitted for editorial/agent review
	StatusApproved  = "approved"  // signed off, ready to schedule or publish
	StatusQueued    = "queued"    // handed to distribution (scheduled / in-flight)
	StatusPublished = "published" // live on site + confirmed on channels
	StatusArchived  = "archived"  // retired / unpublished
)

// StatusField is the DocField name every publishable content DocType carries. It is
// the SINGLE lifecycle column; framework's own docstatus (0/1/2) is deliberately
// left unused (IsSubmittable=false) so there is exactly one lifecycle, here.
const StatusField = "status"

// statuses is the canonical ordering (board columns, UI). The zero value of a fresh
// document is StatusDraft (the Select field's Default).
var statuses = []string{
	StatusDraft, StatusInReview, StatusApproved, StatusQueued, StatusPublished, StatusArchived,
}

// transitions is the legal directed graph: transitions[from][to] == true iff a
// document may move from `from` to `to`. It is the whole rule — nothing else decides
// legality. A missing edge is illegal (fail closed).
var transitions = map[string]map[string]bool{
	StatusDraft:     {StatusInReview: true, StatusArchived: true},
	StatusInReview:  {StatusApproved: true, StatusDraft: true, StatusArchived: true},
	StatusApproved:  {StatusQueued: true, StatusPublished: true, StatusDraft: true, StatusArchived: true},
	StatusQueued:    {StatusPublished: true, StatusApproved: true, StatusArchived: true},
	StatusPublished: {StatusArchived: true, StatusDraft: true},
	StatusArchived:  {StatusDraft: true},
}

// IsStatus reports whether s is a defined lifecycle state.
func IsStatus(s string) bool {
	_, ok := transitions[s]
	return ok
}

// CanTransition reports whether from→to is a legal edge. A no-op (from==to) is
// legal (an idempotent write that does not change status). An unknown from/to is
// illegal.
func CanTransition(from, to string) bool {
	if from == to {
		return true
	}
	return transitions[from][to]
}

// entersDistribution reports whether moving to `to` should trigger the channel
// fan-out. Distribution happens on EXACTLY ONE edge — the move to `published` — so a
// legal walk (approved → queued → published) can never fan the same item out twice.
//
// The two states are decomplected:
//   - `queued` = approved + scheduled/ready, NOT yet posted. The move to queued records
//     `scheduled_at` (the intended go-live), a pure CMS staging column; it makes NO
//     external call. A scheduler/automation promotes a queued item to `published` when
//     its `scheduled_at` arrives (mirroring the site's PULL model: the platform owns
//     timing, not the channel edge).
//   - `published` = live/distributed. This is the ONE edge that fans out — `published_at`
//     is stamped now and the item is posted to its channels now (a caller may still pass
//     an explicit scheduleAt to hand a future time to the channel's own scheduler).
//
// The transition endpoint fires the Distributor only on this edge; the hook never does
// (side effects are orchestration, never engine-internal). Publish is ALSO idempotent
// per-channel (publish.go), so even a re-transition or retry cannot double-post.
func entersDistribution(to string) bool {
	return to == StatusPublished
}

// IsLive reports whether a document in state s is publicly readable (the site reads
// only live content). Exactly StatusPublished today; kept a predicate so a future
// "live" state is a one-line change, not a scattered string compare.
func IsLive(s string) bool { return s == StatusPublished }

// Transitions returns the legal successors of `from` (nil for an unknown state), a
// copy so a caller cannot mutate the rule. The console renders per-item action
// buttons from this.
func Transitions(from string) []string {
	edges := transitions[from]
	out := make([]string, 0, len(edges))
	for _, s := range statuses { // canonical order, not map order
		if edges[s] {
			out = append(out, s)
		}
	}
	return out
}

// stateGraph is the JSON shape served at GET /v1/content/lifecycle: the ordered
// states and, per state, its legal successors. The console builds the board columns
// and the allowed per-item actions from this ONE source, so UI and enforcement can
// never drift.
type stateGraph struct {
	States      []string            `json:"states"`
	Initial     string              `json:"initial"`
	Live        string              `json:"live"`
	Transitions map[string][]string `json:"transitions"`
}

func lifecycleGraph() stateGraph {
	tx := make(map[string][]string, len(statuses))
	for _, s := range statuses {
		tx[s] = Transitions(s)
	}
	return stateGraph{States: statuses, Initial: StatusDraft, Live: StatusPublished, Transitions: tx}
}
