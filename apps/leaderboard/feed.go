// feed.go — THE LIVE FEED: what is happening on the board, as it happens.
//
//	GET /v1/leaderboard/feed     public; Server-Sent Events, no resume, no poll
//
// A builder joined, joined through a friend's link, tried a model for the first
// time, crossed a token milestone, was awarded a bounty, reached a level, listed as an
// expert, and how many tokens the last hour carried. One line each, the way the
// standing names a row: masked in the middle, whole where the org listed itself, none
// where it withdrew (handleOf). A line carries a kind, a time, a handle and one
// detail, and never an email, a real name or an org id.
//
// WHERE IT COMES FROM. Cloud's own bus: the pulses its apps announce (bus.Announce)
// when an org is created, a referral is claimed, a bounty is awarded and an expert is
// listed. The rest cannot be announced by whoever does it, because it is not one act
// but many: usage. So while someone is watching, the hub reads the line and the new
// (org, model) pairs once a minute and says what CHANGED between two reads, one line
// per org at most, the highest milestone or level it crossed. Nothing is said per
// request, and an hourly total is one line. A read the hub has not made before only
// sets the baseline.
//
// THE CAP. At most feedRate lines a minute leave the hub; the rest are dropped, since a
// platform busy enough to exceed it is not short of lines. A new reader is replayed
// the last feedKeep, so the feed is not empty on a real platform: after a restart the
// hub is filled from what is durable (the newest signups and awards) before it says
// anything live.
//
// A stream is not a typed op (zip's typed path writes one JSON out), so this is a raw
// route and is named in typed_wire_test.go's ledger. There is no Last-Event-ID resume:
// a reader that reconnects is replayed the last feedKeep.

package leaderboard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/spec"
)

const (
	// feedKeep is how many lines a new reader is replayed.
	feedKeep = 20
	// feedRate is how many lines a minute leave the hub.
	feedRate = 30
	// feedBacklog is how many lines may wait for a slow reader before it is cut off.
	feedBacklog = 64
	// feedBeat is how often an idle stream writes a comment: a failing write is how a
	// vanished reader is noticed.
	feedBeat = 25 * time.Second
	// feedEvery is how often the hub reads what changed while someone watches.
	feedEvery = time.Minute
)

// The kinds of line.
const (
	kindJoined    = "joined"    // an org was created
	kindReferred  = "referred"  // an org joined through a friend's link
	kindTried     = "tried"     // an org used a model for the first time
	kindMilestone = "milestone" // an org crossed a token milestone
	kindHour      = "hour"      // the tokens one hour carried
	kindBounty    = "bounty"    // an award
	kindLevel     = "level"     // an org reached a level
	kindExpert    = "expert"    // an expert was listed
)

// milestones are the token totals an org is said to cross.
var milestones = []int64{1_000_000, 10_000_000, 100_000_000, 1_000_000_000, 10_000_000_000, 100_000_000_000}

// levels are the names of the rungs of revealSteps from the second: the bands of
// levels, Wave to Singularity, the first of which (Photon) is where everyone starts.
var levels = func() []string {
	names := make([]string, 0, len(bands)-1)
	for _, band := range bands[1:] {
		names = append(names, band.Name)
	}
	return names
}()

func init() {
	spec.Register("/v1/leaderboard/feed", http.MethodGet, nil, spec.Bytes{Type: "text/event-stream"})
	spec.Describe("/v1/leaderboard/feed", http.MethodGet,
		"Stream the board's live lines to anyone as Server-Sent Events",
		"Holds one text/event-stream open and pushes a `message` frame for each line as it happens, after "+
			"replaying the last 20. `data` is `{id, kind, at, handle, detail}`, where kind is joined, referred, "+
			"tried, milestone, hour, bounty, level or expert. `handle` is masked in the middle, whole for an org "+
			"that listed itself and empty for one that withdrew. At most 30 lines a minute. A `: ping` comment is "+
			"written every 25 seconds. There is no resume: a reader that reconnects is replayed the last 20. "+
			"No credential is needed to read it.")
}

// FeedLine is one line of the feed.
type FeedLine struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	At     string `json:"at"`
	Handle string `json:"handle"`
	Detail string `json:"detail"`
}

// hub holds the last lines and the readers they are pushed to.
type hub struct {
	st  *optinStore
	log reader
	mu  sync.Mutex
	// ring is the last feedKeep lines, oldest first.
	ring []FeedLine
	next int64
	subs map[chan FeedLine]struct{}
	// sent is when each line that left in the last minute did.
	sent []time.Time

	// filled is whether the durable lines have been read in.
	filled bool

	// reading makes one read of what changed at a time.
	reading sync.Mutex

	// What the last read saw, so the next says what changed.
	seen    bool
	tokens  map[string]int64
	points  map[string]int
	tried   map[string]bool
	hourMax time.Time

	stop func()
}

// theHub is the process's hub, made on the first reader.
var (
	theHub  *hub
	hubOnce sync.Mutex
)

// hubFor is the hub, started on the first call.
func hubFor(st *optinStore, log reader) *hub {
	hubOnce.Lock()
	defer hubOnce.Unlock()
	if theHub == nil {
		theHub = newHub(st, log)
		theHub.run()
	}
	return theHub
}

// stopHub ends the hub. Shutdown calls it.
func stopHub() {
	hubOnce.Lock()
	defer hubOnce.Unlock()
	if theHub != nil && theHub.stop != nil {
		theHub.stop()
	}
	theHub = nil
}

func newHub(st *optinStore, log reader) *hub {
	return &hub{st: st, log: log, subs: map[chan FeedLine]struct{}{}, tokens: map[string]int64{}, points: map[string]int{}, tried: map[string]bool{}}
}

// say adds a line about org, named the way the standing names it, and pushes it to
// every reader. It is dropped when the cap is spent. at is when it happened.
func (h *hub) say(kind, org, detail string, at time.Time) {
	handle := ""
	if org != "" {
		choices, err := h.st.Choices(context.Background())
		if err != nil {
			return
		}
		handle = handleOf(org, choices)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := nowFn()
	h.sent = slices.DeleteFunc(h.sent, func(t time.Time) bool { return now.Sub(t) >= time.Minute })
	if len(h.sent) >= feedRate {
		return
	}
	h.sent = append(h.sent, now)
	h.next++
	line := FeedLine{ID: h.next, Kind: kind, At: at.UTC().Format(time.RFC3339), Handle: handle, Detail: detail}
	// Oldest first by when it happened: a line the hub learns of late, as one filled from
	// what is durable is, takes its place and not the end.
	h.ring = append(h.ring, line)
	slices.SortStableFunc(h.ring, func(a, b FeedLine) int { return strings.Compare(a.At, b.At) })
	if len(h.ring) > feedKeep {
		h.ring = h.ring[len(h.ring)-feedKeep:]
	}
	for ch := range h.subs {
		select {
		case ch <- line:
		default:
			// A reader that cannot keep up is cut off rather than allowed to hold the hub.
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// pulse says a line for a pulse an app announced on the bus; others it ignores.
func (h *hub) pulse(p bus.Pulse) {
	at := nowFn()
	if t, err := time.Parse(time.RFC3339, p.At); err == nil {
		at = t
	}
	switch p.Kind {
	case "org":
		h.say(kindJoined, p.Org, "", at)
	case "referral":
		h.say(kindReferred, p.Org, "", at)
	case "bounty":
		h.say(kindBounty, p.Org, "+"+p.Detail+" pts", at)
	case "expert":
		h.say(kindExpert, p.Org, "", at)
	}
}

// observe says what changed since the last read: the highest token milestone and the
// highest level each org crossed, and each (org, model) pair that is new. The first
// read only sets the baseline.
func (h *hub) observe(l *line, tried map[string]bool, at time.Time) {
	h.mu.Lock()
	seen, prevTokens, prevPoints, prevTried := h.seen, h.tokens, h.points, h.tried
	h.mu.Unlock()

	tokens, points := make(map[string]int64, len(l.ranked)), make(map[string]int, len(l.ranked))
	type crossed struct{ org, detail string }
	var marks, rungs []crossed
	for _, e := range l.ranked {
		tokens[e.org], points[e.org] = e.src.Tokens, e.points
		if !seen {
			continue
		}
		if m := crossing(prevTokens[e.org], e.src.Tokens, milestones); m >= 0 {
			marks = append(marks, crossed{e.org, shortTokens(milestones[m]) + " tokens"})
		}
		if r := crossing(int64(prevPoints[e.org]), int64(e.points), rungAt()); r >= 0 {
			rungs = append(rungs, crossed{e.org, levels[r]})
		}
	}
	h.mu.Lock()
	h.seen, h.tokens, h.points, h.tried = true, tokens, points, tried
	h.mu.Unlock()
	if !seen {
		return
	}

	for _, c := range marks {
		h.say(kindMilestone, c.org, c.detail, at)
	}
	for _, c := range rungs {
		h.say(kindLevel, c.org, c.detail, at)
	}
	for _, pair := range sortedKeys(tried) {
		if prevTried[pair] {
			continue
		}
		org, model, _ := strings.Cut(pair, "\x00")
		if !bases.MatchString(strings.ToLower(model)) {
			h.say(kindTried, org, model, at)
		}
	}
}

// rungAt is the points each band from the second begins at.
func rungAt() []int64 {
	out := make([]int64, 0, len(revealSteps)-1)
	for _, s := range revealSteps[1:] {
		out = append(out, int64(s.At))
	}
	return out
}

// crossing is the index of the highest mark `to` has reached and `from` had not, or
// -1 when it crossed none.
func crossing(from, to int64, marks []int64) int {
	hit := -1
	for i, m := range marks {
		if from < m && to >= m {
			hit = i
		}
	}
	return hit
}

// shortTokens is a milestone as a reader says it: 10M, 1B.
func shortTokens(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return strconv.FormatInt(n/1_000_000_000, 10) + "B"
	default:
		return strconv.FormatInt(n/1_000_000, 10) + "M"
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// hourly says the tokens the last whole hour carried, once.
func (h *hub) hourly(p *PulseView) {
	if p == nil || p.Tokens == nil || len(p.Tokens.Hours) < 2 {
		return
	}
	i := len(p.Tokens.Hours) - 2
	start, err := time.Parse(time.RFC3339, p.Hours[i])
	n := p.Tokens.Hours[i]
	h.mu.Lock()
	done := err != nil || !start.After(h.hourMax) || n <= 0
	if !done {
		h.hourMax = start
	}
	h.mu.Unlock()
	if !done {
		h.say(kindHour, "", fmt.Sprintf("%s tokens", compact(n)), start.Add(time.Hour))
	}
}

// compact writes a count the way a reader reads one: 1.2M, 840K.
func compact(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + "B"
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 0, 64) + "K"
	}
	return strconv.FormatInt(n, 10)
}

// triedPairs reads the (org, model) pairs that began recently.
func triedPairs(ctx context.Context) (map[string]bool, error) {
	sqlStr, args := feedTriesSQL(nowFn().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1))
	rows, err := queryDatastore(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[aString(r["organization"])+"\x00"+aString(r["model"])] = true
	}
	return out, nil
}

// read makes one round of what changed.
func (h *hub) read(ctx context.Context) {
	h.reading.Lock()
	defer h.reading.Unlock()
	l, err := current(ctx, h.st)
	if err != nil {
		return
	}
	tried, err := triedPairs(ctx)
	if err != nil {
		tried = h.triedNow()
	}
	h.observe(l, tried, nowFn())
	if p, err := pulses.get(pulseTTL, func() (*PulseView, error) { return readPulse(ctx, h.log) }); err == nil {
		h.hourly(p)
	}
}

func (h *hub) triedNow() map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return maps0(h.tried)
}

func maps0(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// fill puts what is durable into the hub, once it can be read, so a reader of a platform
// that has just restarted still has lines: the newest orgs created and awards given.
func (h *hub) fill(ctx context.Context) {
	h.mu.Lock()
	done := h.filled
	h.mu.Unlock()
	if done {
		return
	}
	type old struct {
		kind, org, detail string
		at                time.Time
	}
	var past []old
	l, err := current(ctx, h.st)
	if err != nil {
		return
	}
	// The newest orgs IAM dates. An org it does not date has no moment to be said at, and
	// is left out before the newest are taken, or it would crowd every dated one out.
	byNew := slices.DeleteFunc(slices.Clone(l.ranked), func(e entry) bool { return e.since.Year() >= 9999 })
	slices.SortFunc(byNew, func(a, b entry) int { return b.since.Compare(a.since) })
	for _, e := range byNew[:min(len(byNew), feedKeep)] {
		past = append(past, old{kindJoined, e.org, "", e.since})
	}
	if awards, err := h.st.ListBountyAwards(ctx, feedKeep); err == nil {
		for _, a := range awards {
			past = append(past, old{kindBounty, a.Org, "+" + strconv.Itoa(a.Points) + " pts", time.Unix(a.CreatedAt, 0)})
		}
	}
	h.mu.Lock()
	h.filled = true
	h.mu.Unlock()
	slices.SortFunc(past, func(a, b old) int { return a.at.Compare(b.at) })
	for _, p := range past[max(0, len(past)-feedKeep):] {
		h.say(p.kind, p.org, p.detail, p.at)
	}
}

// subscribe adds a reader and gives it the last lines.
func (h *hub) subscribe() (replay []FeedLine, live chan FeedLine, leave func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	live = make(chan FeedLine, feedBacklog)
	h.subs[live] = struct{}{}
	return slices.Clone(h.ring), live, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[live]; ok {
			delete(h.subs, live)
			close(live)
		}
	}
}

// watching is how many readers are on the stream.
func (h *hub) watching() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// run starts the hub: the bus subscription for what apps announce, and the minute's
// read of what changed, which runs only while someone is watching.
func (h *hub) run() {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	h.stop = func() { cancel(); wg.Wait() }

	wg.Add(1)
	go func() {
		defer wg.Done()
		var msgs chan bus.Message
		var sub bus.Subscription
		defer func() {
			if sub != nil {
				_ = sub.Unsubscribe()
			}
		}()
		retry := time.NewTimer(0)
		defer retry.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-retry.C:
				// The bus may not be up yet when the first reader comes; ask again until it is.
				if sub == nil {
					msgs = make(chan bus.Message, feedBacklog)
					if s, err := bus.Subscribe(bus.PulseSubject(), msgs); err == nil {
						sub = s
					}
					if sub == nil {
						retry.Reset(30 * time.Second)
					}
				}
			case m := <-msgs:
				var p bus.Pulse
				if json.Unmarshal(m.Data, &p) == nil {
					h.pulse(p)
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(feedEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if h.watching() > 0 {
					h.read(ctx)
				}
			}
		}
	}()
}

// feed streams the lines to anyone: the last feedKeep first, then each as it happens.
func feed(s *cloud.Service[state], c *zip.Ctx) error {
	h := hubFor(s.State.store, s.Log)
	ctx := context.WithoutCancel(c.Context())
	h.fill(ctx)
	h.read(ctx)
	replay, live, leave := h.subscribe()
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("X-Accel-Buffering", "no")
	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer leave()
		stream(ctx, w, replay, live, feedBeat)
	})
}

// stream writes the replay, then each line until the context ends, the hub cuts the
// reader off or a write fails. It is the whole stream minus the HTTP.
func stream(ctx context.Context, w *bufio.Writer, replay []FeedLine, live <-chan FeedLine, beat time.Duration) {
	if !send(w, "retry: 5000\n\n") {
		return
	}
	for _, l := range replay {
		if !send(w, frame(l)) {
			return
		}
	}
	hb := time.NewTicker(beat)
	defer hb.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			if !send(w, ": ping\n\n") {
				return
			}
		case l, open := <-live:
			if !open || !send(w, frame(l)) {
				return
			}
		}
	}
}

// frame is a line as an event: its id, and its fields as one JSON object.
func frame(l FeedLine) string {
	body, _ := json.Marshal(l)
	return fmt.Sprintf("id: %d\ndata: %s\n\n", l.ID, body)
}

// send writes raw stream bytes and flushes; false means the reader is gone.
func send(w *bufio.Writer, s string) bool {
	if _, err := w.WriteString(s); err != nil {
		return false
	}
	return w.Flush() == nil
}
