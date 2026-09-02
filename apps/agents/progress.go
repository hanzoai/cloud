package agents

import (
	"github.com/hanzoai/cloud/internal/stamp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// progress.go answers the one question a fleet board asks and no column can:
// HOW FAR ALONG IS THIS RUN.
//
// A session's Status is what the SURFACE reported — running, paused, done,
// error — and "running" is true of a run three turns in and of a run that has
// been waiting forty minutes for an approval nobody will give. Both render the
// same on a board, so a human scanning fifty runs cannot tell which one needs
// them. The difference between those two is not in the row; it is in the
// transcript, which is prose, which is what a model reads.
//
// So it is ESTIMATED — by the cheap tier, from the run's own goal and the tail
// of its log — and it is MARKED as an estimate wherever it is published. That
// mark is the whole design, not a caveat on it: a fabricated 90% that reads as a
// measurement is worse than no number at all, because it tells a human to leave
// a stuck run alone. Three rules follow, and each is a test:
//
//   - A run with nothing to estimate from reports UNKNOWN, never 0. An empty bar
//     says "this run has done nothing", which is a claim, and we do not have it.
//   - A TERMINAL run reports the row's own status and is marked NOT estimated. A
//     finished run's completeness is a fact; nothing guesses at it.
//   - An estimate is only ever replaced by another ESTIMATE. A model outage or an
//     unreadable reply advances the clock and keeps the last good answer, whose
//     age is published beside it so a reader can discount it themselves.
//
// It runs on cloud.DefaultModel — the flash tier, which model.go already names
// as what unnamed one-shot text work runs on. This is one-shot text work: a
// number and a line, over a few kilobytes, across every run somebody is looking
// at. There is no model knob here, because a second place to decide that is only
// somewhere for the two to disagree.

// The phases an estimate may report.
//
// blocked is the value that earns this field's existence: it is the one thing
// the surface running the agent will never report about itself, because a run
// that is stuck does not know it is stuck. done overlaps StatusDone by design —
// a live run the model judges FINISHED is a run somebody forgot to close, which
// is the second most useful thing on a board — and `estimated` is what tells the
// two apart. A terminal session reports its own status instead (done | error),
// so error is never a phase a model may choose.
const (
	phaseRunning = "running"
	phaseBlocked = "blocked"
	phaseDone    = "done"
	phaseUnknown = "unknown"
)

// validPhase is the closed set, and BOTH producers go through it — the model's
// reply and the run's own report. One vocabulary, so a board branches on three
// words whoever said them, and a self-report cannot introduce a fourth that a
// model may not use.
func validPhase(p string) bool {
	switch p {
	case phaseRunning, phaseBlocked, phaseDone:
		return true
	}
	return false
}

// pctUnknown is the stored pct of a run whose progress is INDETERMINATE, and it
// is -1 rather than 0 because those are different facts. It never reaches the
// wire: progressOf omits the key entirely rather than publishing a sentinel a
// client could render.
const pctUnknown = -1

// progressInterval is the floor between two estimates of ONE run. It is a cost
// bound rather than a freshness target: a board polls, and a poll that becomes a
// completion is a bill that scales with how long somebody leaves a tab open. A
// run's progress does not move meaningfully faster than this.
const progressInterval = 30 * time.Second

// progressBusy caps how many estimates are in flight across the process. A board
// listing a hundred running sessions asks for a hundred refreshes; this is what
// turns that into eight completions and a retry on the next poll, rather than a
// hundred concurrent calls to the gateway.
const progressBusy = 8

// progressTimeout bounds one estimate. It is generous for a flash-tier reply of
// under a hundred tokens and short enough that a wedged gateway cannot pin a
// slot for the life of the process.
const progressTimeout = 20 * time.Second

// progressReply caps the completion. The answer is one small JSON object; buying
// more than that buys prose nothing parses.
const progressReply = 256

// The bounds on what a model is shown and what it may say back. progressTail is
// how many turns of the log ride the brief, progressCut how much of one turn's
// payload, and progressLine how long the activity may be — a card renders it on
// one line, so a paragraph would be silently clipped by whoever draws it.
const (
	progressTail = 20
	progressCut  = 320
	progressLine = 120
)

// Progress is the last estimate made of one run, as the session row holds it.
// Pct is pctUnknown when indeterminate; At is zero when no estimate has ever
// been made.
type Progress struct {
	Pct      int
	Phase    string
	Activity string
	At       int64
	// Estimated says a MODEL produced this rather than the run itself. It is the
	// wire field spelled once — the column, the value and the published key are
	// one fact, so no projection can disagree about whether a number was guessed.
	Estimated bool
}

// sessionProgress is how far along a run is, as every read of it publishes it.
//
// It is a VALUE on sessionView rather than an optional object, so a board always
// has something to bind and never branches on presence: phase always says
// something, even if what it says is that we do not know.
type sessionProgress struct {
	// Pct is how much of the run is done, 0 to 100. THE KEY IS ABSENT when
	// progress is indeterminate — a run nobody can estimate is not a run that has
	// done nothing, and rendering the second for the first is the mistake this
	// omission exists to make impossible. Read `phase` before reaching for it.
	Pct *int `json:"pct,omitempty"`
	// Phase is what shape the run is in: running, blocked, done, error, or
	// unknown when nothing has estimated it yet. blocked means the transcript
	// shows the run waiting on something — an approval, a credential, an answer —
	// which is the one state the running surface cannot report about itself.
	// error only ever comes from the session's own terminal status.
	Phase string `json:"phase"`
	// Activity is the one line saying what the run is doing right now ("running
	// the reaper's tests"), up to 120 characters, in the model's words. Empty
	// when nothing has estimated it yet.
	Activity string `json:"activity,omitempty"`
	// At is when this was determined, RFC 3339 in UTC to the second. Read it as
	// the estimate's AGE: an estimate is not refreshed while nothing has
	// happened, and a stale one beside a run that is still moving is itself worth
	// seeing. Empty when nothing has estimated it yet.
	At string `json:"at,omitempty"`
	// Estimated says a MODEL produced this, from the run's transcript, and it may
	// be wrong. False means the session's own row said it: a finished run is 100%
	// because it finished, not because anything guessed. Never treat a true here
	// as a measurement — it is the reason to look, not the answer.
	Estimated bool `json:"estimated"`
}

// progressOf renders a session's progress. Ground truth outranks the estimate:
// a session that reached a terminal status reports what the row says and is
// marked NOT estimated, because how finished a finished run is is not a
// question anybody needs a model for.
//
// An errored run carries NO pct on purpose. "How far along" has no answer for a
// run that stopped — it got where it got — and publishing the last estimate
// under a phase the row supplied would mix a guess and a fact in one object.
func progressOf(x Session) sessionProgress {
	switch x.Status {
	case StatusDone:
		full := 100
		return sessionProgress{Pct: &full, Phase: phaseDone, At: stamp.Unix(x.EndedAt)}
	case StatusError:
		return sessionProgress{Phase: StatusError, At: stamp.Unix(x.EndedAt)}
	}
	if x.ProgressAt == 0 || x.ProgressPhase == "" {
		return sessionProgress{Phase: phaseUnknown}
	}
	v := sessionProgress{
		Phase:     x.ProgressPhase,
		Activity:  x.ProgressActivity,
		At:        stamp.Unix(x.ProgressAt),
		Estimated: x.ProgressEstimated,
	}
	if x.ProgressPct >= 0 {
		pct := x.ProgressPct
		v.Pct = &pct
	}
	return v
}

// estimator holds what an estimate needs that a request does not carry: the AI
// plane, and the two bounds that keep a board from becoming a bill.
//
// The in-flight set is the whole of its mutable state and it is bounded BY
// CONSTRUCTION rather than by a sweep: claim refuses past progressBusy and every
// claim is released by the goroutine that took it, so the map never holds more
// than eight entries and never needs reclaiming.
type estimator struct {
	ai types.AIClient

	mu       sync.Mutex
	inflight map[string]bool
}

func newEstimator(ai types.AIClient) *estimator {
	return &estimator{ai: ai, inflight: map[string]bool{}}
}

// enabled reports whether an estimate can be made at all. A deployment with no
// AI plane leaves every run reading unknown, which is the honest answer and the
// same degrade deps.AI gets everywhere else in this package.
func (e *estimator) enabled() bool { return e != nil && e.ai != nil }

// stale is the ONE staleness rule, and it has two terms because either alone is
// wrong. The clock term bounds the cost. The transcript term bounds the WASTE:
// re-reading an unchanged log produces the same answer from the same input, so a
// run that has said nothing since its last estimate is not asked about again —
// which is what makes a fleet of mostly-idle runs nearly free.
func (e *estimator) stale(x Session, now int64) bool {
	if !e.enabled() || isTerminalStatus(x.Status) {
		return false
	}
	return x.UpdatedAt > x.ProgressAt && now-x.ProgressAt >= int64(progressInterval/time.Second)
}

// refresh brings one session's estimate up to date WITHOUT making the caller
// wait for it. A read answers from the row and kicks the model off behind it, so
// a board stays fast and gains the new estimate on its next poll.
//
// org must already be the validated tenant — it names the file the write lands
// in, and the goroutine detaches from the request, so there is nothing left to
// re-derive it from.
func (e *estimator) refresh(sto *Store, org string, x Session) {
	if !e.stale(x, time.Now().Unix()) || !e.claim(org, x.ID) {
		return
	}
	go func() {
		defer e.release(org, x.ID)
		// A background context, not the request's: the request is answered the
		// instant this goroutine starts, and a completion cancelled by its own
		// caller returning would never land.
		ctx, cancel := context.WithTimeout(context.Background(), progressTimeout)
		defer cancel()
		_, _ = e.measure(ctx, sto, org, x)
	}()
}

// measure makes one estimate and persists it, and ALWAYS advances the clock.
//
// The clock moves even when the estimate fails, and that is the point of doing
// it here rather than at the call site: a failure that left progress_at alone
// would make every subsequent poll retry immediately, which is the cost blowup
// the interval exists to prevent, arriving exactly when the gateway is already
// unwell. What does NOT move is the estimate itself — an estimate is only ever
// replaced by another estimate, so a model outage costs freshness, published as
// the growing age of `at`, and never a good answer.
func (e *estimator) measure(ctx context.Context, sto *Store, org string, x Session) (Progress, error) {
	now := time.Now().Unix()
	// Carry the previous estimate forward as the floor: every path below either
	// replaces it wholesale or leaves it standing with a newer stamp.
	kept := Progress{Pct: x.ProgressPct, Phase: x.ProgressPhase, Activity: x.ProgressActivity,
		At: now, Estimated: x.ProgressEstimated}
	if kept.Phase == "" {
		kept.Pct = pctUnknown
	}
	if !e.enabled() {
		return kept, fmt.Errorf("agents: no AI plane, progress cannot be estimated")
	}

	tail, err := sto.TailEvents(ctx, org, x.ID, progressTail)
	if err != nil {
		return kept, fmt.Errorf("progress tail: %w", err)
	}
	// The opening turn is the run's ASK — what it was sent to do — and a percentage
	// is meaningless without it, because a percentage needs a denominator. On any
	// run longer than the tail it has already scrolled off, so it costs its own
	// read; on a short one it is already in the tail and costs nothing.
	var opening []Event
	if len(tail) > 0 && tail[0].Seq > 1 {
		opening, _ = sto.ListEvents(ctx, org, x.ID, 0, 1)
	}

	res, err := e.ai.ChatCompletion(ctx, &types.ChatRequest{
		Model:  cloud.DefaultModel,
		Prompt: brief(x, opening, tail, now),
		Org:    org,
		// The wallet the session already named. An unattributed completion is
		// EXEMPT from the meter, so an empty billing scope here would give these
		// tokens away — and a board makes a lot of them.
		BillingOrg: payerOrOrg(x),
		Project:    x.Project,
		Actor:      x.Actor,
		MaxTokens:  progressReply,
	})
	if err != nil {
		_, _ = sto.SetEstimate(ctx, org, x.ID, kept, x.ProgressAt)
		return kept, fmt.Errorf("progress estimate: %w", err)
	}
	p, ok := parseEstimate(resContent(res))
	if !ok {
		// The model answered and we could not read it. Same treatment as an
		// outage, for the same reason: an unusable reply is not an estimate, and
		// it must not displace one.
		_, _ = sto.SetEstimate(ctx, org, x.ID, kept, x.ProgressAt)
		return kept, fmt.Errorf("progress estimate: unreadable reply")
	}
	p.At = now
	// A compare-and-set, because the model thought for seconds and the run may
	// have REPORTED in the meantime. A guess formed before the run spoke must not
	// land on top of what the run said about itself; losing the race costs this
	// estimate and nothing else, and the newer word is already the better one.
	if wrote, err := sto.SetEstimate(ctx, org, x.ID, p, x.ProgressAt); err != nil {
		return kept, fmt.Errorf("progress persist: %w", err)
	} else if !wrote {
		return kept, nil
	}
	return p, nil
}

// claim takes the single-flight slot for one run, refusing when that run is
// already being estimated or the process is at its concurrency ceiling. A
// refusal is not an error: the next poll asks again, one interval later at worst.
func (e *estimator) claim(org, id string) bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inflight == nil {
		e.inflight = map[string]bool{}
	}
	key := org + "/" + id
	if e.inflight[key] || len(e.inflight) >= progressBusy {
		return false
	}
	e.inflight[key] = true
	return true
}

func (e *estimator) release(org, id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.inflight, org+"/"+id)
}

// payerOrOrg is whose books an estimate is charged to: the wallet the session
// recorded when it opened, falling back to the org's own pool for a session that
// predates that column. It is meter.go's sessionRunning rule, applied to the one
// other thing a session spends.
func payerOrOrg(x Session) string {
	if x.Payer != "" {
		return x.Payer
	}
	return x.Org
}

func resContent(res *types.ChatResponse) string {
	if res == nil {
		return ""
	}
	return res.Content
}

// brief is everything the model is shown, and it is deliberately small: this
// runs across every run on a board, so a brief that grows with the transcript is
// a bill that grows with it.
//
// There is no todo PARSER here, and there must not be one. A run that keeps a
// checklist writes it as a tool call whose payload shape belongs to whatever
// wrote it — this surface has never interpreted an event payload and does not
// start now. The list is handed over as the turns it appears in, and reading a
// checklist out of prose is the one thing the model is unambiguously better at
// than a regexp.
//
// Payloads are safe to forward: guardEvent refuses a credential at the append,
// so a transcript that reached the store has already been through the one
// scrubber this package has.
func brief(x Session, opening, tail []Event, now int64) string {
	var b strings.Builder
	b.WriteString("Estimate how far along an agent run is. Reply with ONE JSON object and nothing else:\n")
	b.WriteString(`{"pct":<0-100>,"phase":"running|blocked|done","activity":"<what it is doing, <=12 words>"}` + "\n\n")
	b.WriteString("Rules. pct is the share of the run's stated goal that is DONE. " +
		"If the goal or the remaining work cannot be told from the log, set pct to -1 — do not guess, " +
		"and never answer 0 to mean unsure. " +
		"phase is blocked when the log shows the run waiting on somebody: an approval, a credential, " +
		"an answer, a review. phase is done when the goal reads as finished even though the run is " +
		"still open. Otherwise running.\n\n")

	fmt.Fprintf(&b, "agent: %s\n", x.Agent)
	if x.Title != "" {
		fmt.Fprintf(&b, "goal: %s\n", preview(x.Title, progressCut))
	}
	if x.Repo != "" {
		fmt.Fprintf(&b, "repo: %s\n", preview(x.Repo, progressCut))
	}
	if x.StartedAt > 0 && now > x.StartedAt {
		fmt.Fprintf(&b, "elapsed: %s\n", (time.Duration(now-x.StartedAt) * time.Second).Round(time.Second))
	}
	if len(tail) > 0 {
		fmt.Fprintf(&b, "turns: %d\n", tail[len(tail)-1].Seq)
	}
	if len(opening) > 0 {
		b.WriteString("\nopening turn:\n")
		writeTurn(&b, opening[0])
	}
	b.WriteString("\nlatest turns:\n")
	if len(tail) == 0 {
		b.WriteString("(none — the run has logged nothing)\n")
	}
	for _, e := range tail {
		writeTurn(&b, e)
	}
	return b.String()
}

func writeTurn(b *strings.Builder, e Event) {
	fmt.Fprintf(b, "%d %s %s: %s\n", e.Seq, e.Kind, e.Actor, preview(collapse(e.Payload), progressCut))
}

// collapse folds a payload's whitespace onto one line so one turn is one line in
// the brief — a JSON blob pretty-printed across forty lines would spend the
// whole budget on indentation. Length is fleet.go's clip, which is already the
// package's one way to cut a string somebody will read.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// parseEstimate reads the model's reply. It is STRICT about the phase and
// forgiving about everything around the object, because those two failures are
// not alike: a model wrapping its JSON in a sentence has still answered, while a
// model naming a phase outside the closed four has not, and treating an unknown
// word as "running" would publish a state nobody chose.
//
// ok=false means there is no usable estimate here, which the caller treats
// exactly as it treats an outage.
func parseEstimate(s string) (Progress, bool) {
	open := strings.Index(s, "{")
	shut := strings.LastIndex(s, "}")
	if open < 0 || shut < open {
		return Progress{}, false
	}
	// Pct arrives as RAW BYTES rather than a number, so a model that answers it in
	// prose ("most of it") costs its own field and not the whole reply. Losing a
	// phase of "blocked" over an unusable percentage would throw away the more
	// valuable half of the answer.
	var raw struct {
		Pct      json.RawMessage `json:"pct"`
		Phase    string          `json:"phase"`
		Activity string          `json:"activity"`
	}
	if err := json.Unmarshal([]byte(s[open:shut+1]), &raw); err != nil {
		return Progress{}, false
	}
	p := Progress{Pct: pctUnknown, Activity: preview(collapse(raw.Activity), progressLine), Estimated: true}
	if p.Phase = strings.ToLower(strings.TrimSpace(raw.Phase)); !validPhase(p.Phase) {
		return Progress{}, false
	}
	// A pct outside 0..100 — including the -1 the prompt asks for when the run
	// cannot be told, and including a model that answered with prose — is
	// INDETERMINATE, not zero. Parsed as a float so 45 and "45" and 45.0 all read
	// the same, and so a decimal is rounded rather than silently truncated toward
	// something the model did not say.
	if n, err := strconv.ParseFloat(strings.Trim(string(raw.Pct), `"`), 64); err == nil && n >= 0 && n <= 100 {
		p.Pct = int(n + 0.5)
	}
	// A phase of done with no number is still 100% done — the model said the goal
	// is met, and dropping the bar to unknown would contradict the word beside it.
	if p.Phase == phaseDone && p.Pct == pctUnknown {
		p.Pct = 100
	}
	return p, true
}

// ---- what the run says about itself ----

// A run REPORTS its own progress by appending a `progress` turn to its
// transcript — the same route, the same guard, the same bound, the same stream
// as every other turn:
//
//	POST /v1/agents/sessions/{id}/events
//	{"kind":"progress","payload":{"pct":60,"phase":"running","activity":"…"}}
//
// It is an event kind rather than a route of its own because progress IS
// something that happened in the run, and the transcript is where what happened
// is recorded. That buys the whole feature for free: the append is already
// typed, already org-scoped, already scanned for credentials, already bounded,
// already sequenced, and already fanned out to every live subscriber — so a
// self-report reaches a board the instant the run emits it, with no poll and no
// second write path to keep in step. It also becomes DURABLE history: how a run's
// own sense of its progress moved is replayable, which an estimate overwritten in
// place could never be.
//
// This is the ONE payload this surface reads. Every other kind's body belongs to
// whoever wrote it and is stored opaque — the rule the estimator's brief also
// obeys. A `progress` payload is different in kind: the shape is OURS, published
// in the append op's own prose, so interpreting it is reading our own contract
// rather than guessing at somebody else's.

// parseReport reads a run's own progress payload. It is STRICT where the model
// parser is forgiving, and the asymmetry is the point: a model is a text
// generator whose output we salvage, while a client is a caller holding a
// published contract — telling it 400 is how it learns, and silently keeping the
// half we understood would let a run believe it reported something it did not.
func parseReport(payload []byte) (Progress, error) {
	if len(payload) == 0 {
		return Progress{}, zip.ErrBadRequest("a progress turn needs a payload: {pct, phase, activity}")
	}
	var raw struct {
		Pct      *int   `json:"pct"`
		Phase    string `json:"phase"`
		Activity string `json:"activity"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Progress{}, zip.ErrBadRequest("progress payload: " + err.Error())
	}
	p := Progress{Pct: pctUnknown, Phase: strings.ToLower(strings.TrimSpace(raw.Phase))}
	if !validPhase(p.Phase) {
		return Progress{}, zip.ErrBadRequest("progress phase must be running|blocked|done")
	}
	// pct is OPTIONAL, so a run that knows it is blocked and does not know how far
	// along it is can say the half it knows. Absent leaves it indeterminate; out of
	// range is refused rather than clamped, because clamping accepts a report whose
	// author was computing something else.
	if raw.Pct != nil {
		if *raw.Pct < 0 || *raw.Pct > 100 {
			return Progress{}, zip.ErrBadRequest("progress pct must be 0-100")
		}
		p.Pct = *raw.Pct
	}
	if p.Activity = collapse(raw.Activity); len(p.Activity) > progressLine {
		return Progress{}, zip.Errorf(http.StatusBadRequest,
			"progress activity is %d bytes; the limit is %d", len(p.Activity), progressLine)
	}
	return p, nil
}
