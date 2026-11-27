package patrol

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	psembed "github.com/hanzoai/pubsub/embed"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// feed_test.go reads the live feed the way a browser reads it: one connection
// held open over a real bus, frames arriving as the writes that made them land.
// Nothing here is a stub — the frames cross a broker and come back as Server-Sent
// Events records, which is the only arrangement in which "the reader is told" and
// "the reader is told only their own" are separable questions.

// riding mounts patrol over a real broker and serves it on a socket, and answers
// a client that speaks to it. The broker is ephemeral so a run never contends
// with a developer's own bus, and the cached connection is dropped either side so
// one run never reads the previous run's dead pipe.
func riding(t *testing.T) (*zip.App, *http.Client) {
	t.Helper()
	srv, err := psembed.Open(psembed.Options{
		Host: "127.0.0.1", Port: -1, StoreDir: t.TempDir(), ServerName: "patrol-feed-test",
	})
	if err != nil {
		t.Fatalf("embedded broker: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	bus.Disconnect()
	t.Cleanup(bus.Disconnect)
	t.Setenv("CLOUD_PUBSUB_URL", srv.ClientURL())
	// The app is left serving when the test ends. A stream outlives the request
	// that opened it by design and ends at its next write, so asking the app to
	// stop would wait out a heartbeat; the reader hangs up and the loop reclaims
	// itself.
	app := mount(t)

	dir, err := os.MkdirTemp("", "ptl")
	if err != nil {
		t.Fatalf("socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	go func() { _ = app.Listen("http://" + sock) }()

	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, "unix", sock)
		if err != nil {
			return nil, err
		}
		return &patient{Conn: c}, nil
	}
	for range 400 {
		if c, err := dial(context.Background(), "", ""); err == nil {
			_ = c.Close()
			return app, &http.Client{Transport: &http.Transport{DialContext: dial}}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("patrol never began serving on %s", sock)
	return nil, nil
}

// patient bounds every read, so a record that never arrives ends the read rather
// than the test binary's ten minutes.
type patient struct{ net.Conn }

func (p *patient) Read(b []byte) (int, error) {
	_ = p.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	return p.Conn.Read(b)
}

// watch opens the feed as one user and answers a reader over the records it
// carries.
func watch(t *testing.T, cl *http.Client, query, in, user string) (*bufio.Reader, *http.Response) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://patrol/v1/patrol/feed"+query, nil)
	if err != nil {
		t.Fatalf("feed request: %v", err)
	}
	req.Header.Set("X-Org-Id", in)
	req.Header.Set("X-User-Id", user)
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("open the feed: %v", err)
	}
	// Registered before the close, so it runs after it: the loop learns its
	// reader is gone at the next frame it writes, and until it does it holds a
	// charge against the reader and the tenant that the next test would inherit.
	t.Cleanup(func() { hangup(in) })
	t.Cleanup(func() { _ = resp.Body.Close() })
	return bufio.NewReader(resp.Body), resp
}

// hangup writes to every stream open on one org's subject, so a reader that has
// closed is discovered rather than waited for. One frame names no site, which is
// what a reader of the whole operation carries, and one names each site the org
// holds, which is what a customer carries.
func hangup(in string) {
	frames := []frame{{Kind: "fix", Unit: "bye"}}
	names, _ := framework.Names(context.Background(), in, DTSite, nil, maxSites)
	for _, site := range names {
		frames = append(frames, frame{Kind: "event", Event: &patrolEvent{Name: "bye", Site: site}})
	}
	// Twice: the first write to a socket whose peer has closed may still be
	// taken, and it is the write that fails that ends the loop.
	for range 2 {
		for _, f := range frames {
			b, err := json.Marshal(f)
			if err != nil {
				continue
			}
			_ = bus.Publish(bus.Phys(in, Module), b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// alive reads the comment the stream is established by, which is also how a
// test knows the subscription behind it is live.
func alive(t *testing.T, r *bufio.Reader) {
	t.Helper()
	if got := record(t, r); got != ": stream open" {
		t.Fatalf("the feed opens with %q, want the established comment", got)
	}
}

// record reads one SSE record: its lines up to the blank line that terminates it.
func record(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read a feed record: %v (so far %q)", err, strings.Join(lines, "\n"))
		}
		if line = strings.TrimRight(line, "\r\n"); line == "" {
			return strings.Join(lines, "\n")
		}
		lines = append(lines, line)
	}
}

// framed decodes a `data:` record into the frame it carries.
func framed(t *testing.T, rec string) frame {
	t.Helper()
	body, ok := strings.CutPrefix(rec, "data: ")
	if !ok {
		t.Fatalf("a feed record is not a data line: %q", rec)
	}
	var f frame
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		t.Fatalf("decode a frame: %v (%q)", err, body)
	}
	return f
}

// TestFeedCarriesTheWriteThatMadeIt proves the whole path: a write publishes a
// frame, and a reader watching that org's feed is handed it as one SSE record.
func TestFeedCarriesTheWriteThatMadeIt(t *testing.T) {
	app, cl := riding(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	r, _ := watch(t, cl, "", org, controller)
	alive(t, r)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
		map[string]any{"level": LevelAmber, "title": "Gate left open", "site": "depot"}); code != http.StatusCreated {
		t.Fatalf("raise an event want 201, got %d (%s)", code, raw)
	}
	f := framed(t, record(t, r))
	if f.Kind != "event" || f.Event == nil {
		t.Fatalf("the feed carried %+v, want the event the write raised", f)
	}
	if f.Event.Title != "Gate left open" || f.Event.Site != "depot" {
		t.Fatalf("the frame carries %+v, want the row as written", *f.Event)
	}
}

// TestFeedCarriesOnlyTheReadersOwnEstate proves the narrowing holds on the live
// path as well as on the list: a customer watching the feed is handed the rows
// about their own sites, and the row about another customer's is not among them.
func TestFeedCarriesOnlyTheReadersOwnEstate(t *testing.T) {
	app, cl := riding(t)
	twoCustomers(t, app)

	r, _ := watch(t, cl, "", org, buyer)
	alive(t, r)

	// The rival's row first, then theirs. Both cross the same subject in this
	// order, so the first record the customer is handed answers whether the one
	// before it was withheld.
	for _, site := range []string{"rivals", "theirs"} {
		if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
			map[string]any{"level": LevelRed, "title": "Intruder at " + site, "site": site}); code != http.StatusCreated {
			t.Fatalf("raise the %s row want 201, got %d (%s)", site, code, raw)
		}
	}
	f := framed(t, record(t, r))
	if f.Event == nil || f.Event.Site != "theirs" {
		t.Fatalf("a customer was handed %+v: the row before it was another customer's", f)
	}
}

// TestFeedCarriesNoOtherTenantsFrames proves the subject is the org's own: a
// write in another tenant reaches nobody watching this one.
func TestFeedCarriesNoOtherTenantsFrames(t *testing.T) {
	app, cl := riding(t)
	install(t, app, org)
	install(t, app, "victim")
	seedSite(t, app, org, "depot", "1001", "")
	seedSite(t, app, "victim", "secret-depot", "9001", "")

	r, _ := watch(t, cl, "", org, controller)
	alive(t, r)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", "victim", boss,
		map[string]any{"level": LevelRed, "title": "Break-in at secret-depot", "site": "secret-depot"}); code != http.StatusCreated {
		t.Fatalf("raise the other tenant's row want 201, got %d (%s)", code, raw)
	}
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
		map[string]any{"level": LevelCyan, "title": "ours", "site": "depot"}); code != http.StatusCreated {
		t.Fatalf("raise our own row want 201, got %d (%s)", code, raw)
	}
	f := framed(t, record(t, r))
	if f.Event == nil || f.Event.Title != "ours" {
		t.Fatalf("a reader in %s was handed %+v, which is another tenant's", org, f)
	}
}

// TestFeedReplaysWhatAReconnectingReaderMissed proves ?since=: the rows written
// at or after the moment named arrive before the stream goes live, and a row
// written before it does not.
func TestFeedReplaysWhatAReconnectingReaderMissed(t *testing.T) {
	app, cl := riding(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	// Stamped by hand, an hour either side of the mark, so what the replay keeps
	// is the moment on the row and not how long the test took to write it.
	create(t, app, org, DTEvent, map[string]any{
		"level": LevelCyan, "title": "before the mark", "site": "depot", "at": "2026-01-02 09:00:00"})
	create(t, app, org, DTEvent, map[string]any{
		"level": LevelCyan, "title": "after the mark", "site": "depot", "at": "2026-01-02 11:00:00"})
	const mark = "2026-01-02 10:00:00"

	r, _ := watch(t, cl, "?since="+strings.ReplaceAll(mark, " ", "%20"), org, controller)
	alive(t, r)
	f := framed(t, record(t, r))
	if f.Event == nil || f.Event.Title != "after the mark" {
		t.Fatalf("the replay opened with %+v, want the row written at or after the mark", f)
	}
}

// TestFeedReplaysNothingForAnUnreadableSince proves the two values that replay
// nothing: no ?since= at all, and one that is not a stored moment. Either way the
// stream goes straight live, which the next frame published proves.
func TestFeedReplaysNothingForAnUnreadableSince(t *testing.T) {
	app, cl := riding(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
		map[string]any{"level": LevelCyan, "title": "already written", "site": "depot"}); code != http.StatusCreated {
		t.Fatalf("seed a row: %d (%s)", code, raw)
	}

	for _, since := range []string{"", "?since=yesterday"} {
		r, _ := watch(t, cl, since, org, controller)
		alive(t, r)
		if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
			map[string]any{"level": LevelCyan, "title": "live" + since, "site": "depot"}); code != http.StatusCreated {
			t.Fatalf("raise a live row: %d (%s)", code, raw)
		}
		if f := framed(t, record(t, r)); f.Event == nil || f.Event.Title != "live"+since {
			t.Fatalf("with since=%q the first record was %+v, want the live row and no replay", since, f)
		}
	}
}

// TestCarriesReadsTheSiteEachFrameNames proves what decides whether a published
// frame is a caller's to read: the site it names. A frame about no site is the
// operation's, and one that is not a frame at all is nobody's.
func TestCarriesReadsTheSiteEachFrameNames(t *testing.T) {
	mine := reach{client: "northside", sites: map[string]bool{"theirs": true}}
	all := reach{all: true}
	inc := patrolIncident{Name: "inc-00001", Site: "theirs"}
	other := patrolIncident{Name: "inc-00002", Site: "rivals"}
	ev := patrolEvent{Name: "e1", Site: "theirs"}
	pt := patrolCheckpoint{Name: "p1", Site: "rivals"}

	for _, tc := range []struct {
		name string
		r    reach
		f    frame
		want bool
	}{
		{"an incident at their own site", mine, frame{Kind: "incident", Incident: &inc}, true},
		{"an incident at another customer's", mine, frame{Kind: "incident", Incident: &other}, false},
		{"a feed row about their own site", mine, frame{Kind: "event", Event: &ev}, true},
		{"a checkpoint at another customer's", mine, frame{Kind: "point", Point: &pt}, false},
		{"a unit's position, which names no site", mine, frame{Kind: "fix", Unit: "gf-01"}, false},
		{"the same position read by the operation", all, frame{Kind: "fix", Unit: "gf-01"}, true},
	} {
		b, err := json.Marshal(tc.f)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		if got := tc.r.carries(b); got != tc.want {
			t.Errorf("%s: carries = %v, want %v", tc.name, got, tc.want)
		}
	}
	if all.carries([]byte("{not json")) != true {
		t.Error("the operation reads every frame, whatever it holds")
	}
	if mine.carries([]byte("{not json")) {
		t.Error("a customer was handed bytes that are not a frame at all")
	}
}

// TestAStreamIsChargedToItsReaderAndItsTenant proves the two counts an open
// stream is held against, and that letting it go leaves neither row behind.
func TestAStreamIsChargedToItsReaderAndItsTenant(t *testing.T) {
	const key = "one-reader"
	let, held := count(key, 2)
	if !held {
		t.Fatal("the first stream on a fresh key was refused")
	}
	again, held := count(key, 2)
	if !held {
		t.Fatal("the second stream was refused below the bound")
	}
	if _, held := count(key, 2); held {
		t.Error("a third stream was charged against a bound of two")
	}
	let()
	again()
	streams.Lock()
	_, remains := streams.held[key]
	streams.Unlock()
	if remains {
		t.Error("a key whose streams were all let go still holds a row")
	}
}

// TestWritingToAReaderThatHasGoneSaysSo proves how the loop learns its reader
// left: every write to a connection that has hung up reports it, which is what
// ends the stream and gives its two charges back.
func TestWritingToAReaderThatHasGoneSaysSo(t *testing.T) {
	if open(gone()) {
		t.Error("opening a stream to a reader that has gone reported it live")
	}
	if ping(gone()) {
		t.Error("a heartbeat to a reader that has gone reported it live")
	}
	if raw(gone(), []byte(`{"kind":"fix"}`)) {
		t.Error("a frame written to a reader that has gone reported it live")
	}
	if send(gone(), frame{Kind: "fix", Unit: "gf-01"}) {
		t.Error("an SSE record written to a reader that has gone reported it live")
	}
	// A frame that cannot be rendered is not the reader leaving, so the stream
	// stays open and the next frame still goes out.
	var buf strings.Builder
	w := bufio.NewWriter(&buf)
	if !send(w, frame{Kind: "fix", Unit: "gf-01", Lat: math.NaN()}) {
		t.Error("a frame that cannot be rendered was reported as the reader leaving")
	}
	if buf.Len() != 0 {
		t.Errorf("a frame that cannot be rendered wrote %q", buf.String())
	}
}

// gone is a writer over a connection whose reader has hung up: it has already
// failed, so every write on it reports the reader gone.
func gone() *bufio.Writer {
	w := bufio.NewWriterSize(broken{}, 16)
	_, _ = w.WriteString(strings.Repeat("x", 64))
	return w
}

// broken is a connection that refuses every byte.
type broken struct{}

func (broken) Write([]byte) (int, error) { return 0, os.ErrClosed }

// charged is what one key is holding right now.
func charged(key string) int {
	streams.Lock()
	defer streams.Unlock()
	return streams.held[key]
}

// charge holds streams against one key until it will take no more, and gives
// them all back when the test ends.
func charge(t *testing.T, key string, most int) {
	t.Helper()
	var lets []func()
	t.Cleanup(func() {
		for _, let := range lets {
			let()
		}
	})
	for {
		let, held := count(key, most)
		if !held {
			return
		}
		lets = append(lets, let)
	}
}

// TestAStreamIsHeldAgainstItsReaderAndItsTenant proves what an open stream is
// charged to, and that a reader who hangs up gives both charges back: a stream
// costs a goroutine, a subscription and a socket for as long as it is held, and
// the loop learns the reader left at the next frame it writes.
func TestAStreamIsHeldAgainstItsReaderAndItsTenant(t *testing.T) {
	app, cl := riding(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	// The reader's own key is the attribution key every record carries: the org
	// they acted in and their subject.
	const reader = org + "/" + controller
	before, mine := charged(org), charged(reader)

	r, resp := watch(t, cl, "", org, controller)
	alive(t, r)
	if charged(org) != before+1 || charged(reader) != mine+1 {
		t.Fatalf("an open stream is charged %d to the tenant and %d to its reader, want one more of each",
			charged(org)-before, charged(reader)-mine)
	}

	_ = resp.Body.Close()
	for range 200 {
		if charged(org) == before && charged(reader) == mine {
			return
		}
		if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
			map[string]any{"level": LevelCyan, "title": "still there?"}); code != http.StatusCreated {
			t.Fatalf("raise a row: %d (%s)", code, first(raw, 160))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("a stream whose reader hung up still holds %d against the tenant and %d against the reader",
		charged(org)-before, charged(reader)-mine)
}

// TestAReaderAtTheirOwnCeilingIsRefused proves the bound a reader is held to,
// and that the refusal costs the tenant nothing: the two counts exist so that
// one member opening tabs cannot close the control room's wall.
func TestAReaderAtTheirOwnCeilingIsRefused(t *testing.T) {
	app, cl := riding(t)
	install(t, app, org)
	charge(t, org+"/"+controller, maxStreams)
	tenant := charged(org)

	if code := waits(t, app, http.MethodGet, "/v1/patrol/feed", org, controller, nil); code != http.StatusTooManyRequests {
		t.Fatalf("a reader already holding %d streams want 429, got %d", maxStreams, code)
	}
	if charged(org) != tenant {
		t.Errorf("the refusal charged the tenant %d", charged(org)-tenant)
	}
	// Another member of the same org still opens one.
	r, _ := watch(t, cl, "", org, supervisor)
	alive(t, r)
}

// TestATenantAtItsCeilingGivesTheReaderBack proves the ceiling above the
// per-reader bound: it refuses, and the reader is charged nothing for a stream
// they never got.
func TestATenantAtItsCeilingGivesTheReaderBack(t *testing.T) {
	const crowded = "crowded-feed"
	app, _ := riding(t)
	install(t, app, crowded)
	charge(t, crowded, maxTenant)
	const reader = crowded + "/" + controller
	mine := charged(reader)

	if code := waits(t, app, http.MethodGet, "/v1/patrol/feed", crowded, controller, nil); code != http.StatusTooManyRequests {
		t.Fatalf("a tenant already holding %d streams want 429, got %d", maxTenant, code)
	}
	if charged(reader) != mine {
		t.Errorf("a reader refused by the tenant's ceiling was left holding %d", charged(reader)-mine)
	}
}

// TestTheFeedRefusesWithNoBusToSubscribeTo proves the direction the feed fails
// in: it holds a connection open to carry frames, so with nothing to subscribe
// to it refuses rather than holding one open that carries none.
func TestTheFeedRefusesWithNoBusToSubscribeTo(t *testing.T) {
	bus.Disconnect()
	t.Cleanup(bus.Disconnect)
	t.Setenv("CLOUD_PUBSUB_URL", "nats://127.0.0.1:1")
	app := mount(t)
	install(t, app, org)

	if code := waits(t, app, http.MethodGet, "/v1/patrol/feed", org, controller, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("the feed with no bus want 503, got %d", code)
	}
}

// TestAFrameTheBusRefusesLeavesTheWriteStanding proves the publish is best
// effort at the far end too: a frame the broker will not carry costs the screens
// one refresh and never the write that produced it.
func TestAFrameTheBusRefusesLeavesTheWriteStanding(t *testing.T) {
	srv, err := psembed.Open(psembed.Options{
		Host: "127.0.0.1", Port: -1, StoreDir: t.TempDir(), ServerName: "patrol-small-test",
		MaxPayload: 1024,
	})
	if err != nil {
		t.Fatalf("embedded broker: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	bus.Disconnect()
	t.Cleanup(bus.Disconnect)
	t.Setenv("CLOUD_PUBSUB_URL", srv.ClientURL())
	app := mount(t)
	install(t, app, org)

	o := ops{s: &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}}
	o.publish(context.Background(), org, frame{Kind: "event", Event: &patrolEvent{
		Name: "e1", Level: LevelRed, Title: "Intruder", Detail: strings.Repeat("x", 4000),
	}})

	// The write behind such a frame still lands.
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller, map[string]any{
		"level": LevelRed, "title": "Intruder", "detail": strings.Repeat("x", 4000),
	}); code != http.StatusCreated {
		t.Fatalf("a row whose frame the bus refuses want 201, got %d (%s)", code, first(raw, 160))
	}
}
