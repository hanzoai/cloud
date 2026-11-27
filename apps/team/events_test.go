package team

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// frameSeen is one decoded event frame.
type frameSeen struct {
	name string
	data map[string]any
}

// readFrames decodes an SSE byte stream into frames until it ends.
func readFrames(r io.Reader, out chan<- frameSeen) {
	defer close(out)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var name string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var d map[string]any
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d)
			out <- frameSeen{name: name, data: d}
		}
	}
}

// streamFor runs the stream core for one member of the cast's space and
// answers its frames, and a stop.
func streamFor(t *testing.T, c *cast, account, role string) (<-chan frameSeen, func()) {
	t.Helper()
	return streamWith(t, c, account, role, streamAuth{}, pace{beat: time.Hour, readmit: time.Hour})
}

// streamWith is streamFor with the credential and pacing stated, for the tests
// about expiry, re-checks and admission.
func streamWith(t *testing.T, c *cast, account, role string, auth streamAuth, p pace) (<-chan frameSeen, func()) {
	t.Helper()
	b := &channelBridge{trans: mounted.State.trans, accounts: mounted.State.accounts, ident: mounted.State.trans.ident}
	s := seat{caller: caller{account: account, org: c.org}, ws: c.ws, role: role}
	tp, stop, err := mounted.State.trans.hub.listen(account, []string{c.ws})
	if err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer func() { _ = pw.Close() }()
		b.pump(ctx, bufio.NewWriter(pw), []seat{s}, tp, auth, p)
	}()
	frames := make(chan frameSeen, 256)
	go readFrames(pr, frames)
	return frames, func() { cancel(); stop() }
}

// ended reports whether a stream's frames channel closed within a deadline.
func ended(frames <-chan frameSeen, within time.Duration) bool {
	deadline := time.After(within)
	for {
		select {
		case _, ok := <-frames:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// TestStreamEndsWithItsCredential: a stream stops at its credential's expiry,
// and at the first event after the credential stops verifying.
func TestStreamEndsWithItsCredential(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	frames, stop := streamWith(t, c, c.bobID, roleMember,
		streamAuth{exp: time.Now().Add(200 * time.Millisecond)}, pace{beat: time.Hour, readmit: time.Hour})
	defer stop()
	if !ended(frames, 2*time.Second) {
		t.Fatal("the stream outlived its credential's expiry")
	}

	revoked := func(context.Context) error { return errNoSpace }
	frames2, stop2 := streamWith(t, c, c.bobID, roleMember, streamAuth{recheck: revoked}, pace{beat: time.Hour, readmit: 0})
	defer stop2()
	c.say(t, c.ada, general.ID, "anyone?")
	if !ended(frames2, 2*time.Second) {
		t.Fatal("the stream kept going after its credential stopped verifying")
	}
}

// TestStreamRefreshesTheRoleItFiltersBy: a stream opened with a stale role
// (member) for somebody IAM holds as a GUEST re-reads the role on touch, and
// from then on filters by it — a guest does not receive a public channel.
func TestStreamRefreshesTheRoleItFiltersBy(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false)
	frames, stop := streamWith(t, c, c.gusID, roleMember, streamAuth{}, pace{beat: time.Hour, readmit: 0})
	defer stop()
	c.say(t, c.ada, general.ID, "public news")
	d := c.doc(t, c.ada, map[string]any{"title": "Marker"})
	_ = d
	deadline := time.After(time.Second)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return
			}
			if f.name == evMessageCreated {
				t.Fatalf("a guest's stream carried a public channel message: %v", f.data)
			}
		case <-deadline:
			return
		}
	}
}

// TestStreamsPerAccountAreCapped: an account holds at most streamsMax open
// streams; the next is refused (429 on the route), and a closed one frees a
// slot.
func TestStreamsPerAccountAreCapped(t *testing.T) {
	c := newCast(t)
	hub := mounted.State.trans.hub
	var stops []func()
	for range streamsMax {
		_, stop, err := hub.listen(c.bobID, []string{c.ws})
		if err != nil {
			t.Fatalf("stream within the cap refused: %v", err)
		}
		stops = append(stops, stop)
	}
	if _, _, err := hub.listen(c.bobID, []string{c.ws}); err == nil {
		t.Fatal("a stream past the cap was opened")
	}
	if code, _ := call(t, c.app, http.MethodGet, c.q("/v1/team/events"), c.bob, nil); code != http.StatusTooManyRequests {
		t.Fatalf("the route past the cap = %d, want 429", code)
	}
	stops[0]()
	stops[0]() // idempotent: a second stop frees nothing twice
	_, again, err := hub.listen(c.bobID, []string{c.ws})
	if err != nil {
		t.Fatalf("a freed slot was not reusable: %v", err)
	}
	if _, _, err := hub.listen(c.bobID, []string{c.ws}); err == nil {
		t.Fatal("a double stop freed two slots")
	}
	again()
	for _, s := range stops[1:] {
		s()
	}
}

// until collects frames until one matches, failing after a deadline.
func until(t *testing.T, frames <-chan frameSeen, match func(frameSeen) bool) []frameSeen {
	t.Helper()
	var got []frameSeen
	deadline := time.After(3 * time.Second)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("stream ended before the frame arrived; saw %v", frameNames(got))
			}
			got = append(got, f)
			if match(f) {
				return got
			}
		case <-deadline:
			t.Fatalf("no matching frame; saw %v", frameNames(got))
		}
	}
}

func frameNames(fs []frameSeen) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.name)
	}
	return out
}

// TestEventStreamCarriesWhatTheCallerMaySee drives every event kind through
// Bob's stream and checks what he must NOT receive: a message in a private channel
// he is not in, and anybody else's inbox.
func TestEventStreamCarriesWhatTheCallerMaySee(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	secret := c.channel(t, "secret", true)
	frames, stop := streamFor(t, c, c.bobID, roleMember)
	defer stop()

	// Each step waits for its event before the next write: the stream re-reads
	// the store when it projects, so a message created and deleted before the
	// stream got to it arrives as its deletion alone — the state, not the
	// history. Pacing the writes is what lets every kind be observed.
	var got []frameSeen
	step := func(want string, write func()) {
		t.Helper()
		write()
		got = append(got, until(t, frames, func(f frameSeen) bool { return f.name == want })...)
	}
	var m teamMessage
	var d teamDoc
	step(evMessageCreated, func() {
		c.say(t, c.ada, secret.ID, "the secret plan")
		m = c.say(t, c.ada, general.ID, "<@"+c.bobID+"> look")
	})
	step(evReaction, func() {
		c.must(t, http.StatusOK, http.MethodPut, "/v1/team/messages/"+m.ID+"/reactions/eyes", c.ada,
			map[string]any{"space": c.ws}, nil)
	})
	step(evMessageUpdated, func() {
		c.must(t, http.StatusOK, http.MethodPatch, "/v1/team/messages/"+m.ID, c.ada,
			map[string]any{"space": c.ws, "text": "<@" + c.bobID + "> look again"}, nil)
	})
	step(evChannel, func() {
		c.must(t, http.StatusOK, http.MethodPatch, "/v1/team/channels/"+general.ID, c.ada,
			map[string]any{"space": c.ws, "topic": "all hands"}, nil)
	})
	step(evDoc, func() { d = c.doc(t, c.ada, map[string]any{"title": "Notes"}) })
	step(evMessageDeleted, func() {
		c.must(t, http.StatusNoContent, http.MethodDelete, c.q("/v1/team/messages/"+m.ID), c.ada, nil, nil)
	})

	seen := map[string]bool{}
	for _, f := range got {
		seen[f.name] = true
		if f.data["space"] != c.ws {
			t.Errorf("%s carries space %v", f.name, f.data["space"])
		}
		raw, _ := json.Marshal(f.data)
		if strings.Contains(string(raw), secret.ID) || strings.Contains(string(raw), "secret plan") {
			t.Fatalf("bob's stream carried the private channel: %s %s", f.name, raw)
		}
		if f.name == evInboxCreated {
			item, _ := f.data["item"].(map[string]any)
			if item == nil || item["channel"] != general.ID {
				t.Errorf("inbox.created = %v, want bob's mention in general", f.data)
			}
		}
		if f.name == evDoc {
			doc, _ := f.data["doc"].(map[string]any)
			if doc == nil || doc["id"] != d.ID {
				t.Errorf("doc.changed = %v, want the new page", f.data)
			}
		}
		if f.name == evMessageDeleted && (f.data["id"] != m.ID || f.data["channel"] != general.ID) {
			t.Errorf("message.deleted = %v, want %s in %s", f.data, m.ID, general.ID)
		}
	}
	if !seen[evInboxCreated] {
		t.Errorf("never saw %s; saw %v", evInboxCreated, frameNames(got))
	}

	// Ada's stream never carries Bob's inbox.
	adaFrames, stopAda := streamFor(t, c, c.adaID, roleOwner)
	defer stopAda()
	c.say(t, c.ada, general.ID, "<@"+c.bobID+"> once more")
	for _, f := range until(t, adaFrames, func(f frameSeen) bool { return f.name == evMessageCreated }) {
		if f.name == evInboxCreated {
			t.Fatalf("ada's stream carried bob's notification: %v", f.data)
		}
	}
}

// TestASlowStreamIsCutNotWaitedOn: a tap nobody reads is closed once it falls
// tapDepth broadcasts behind, and the writer broadcasting never blocks on it.
func TestASlowStreamIsCutNotWaitedOn(t *testing.T) {
	h := newHub(nil)
	tp, stop, err := h.listen("u", []string{"ws"})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	done := make(chan struct{})
	go func() {
		for range tapDepth + 5 {
			h.broadcast("ws", []json.RawMessage{json.RawMessage(`{}`)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on a reader that never reads")
	}
	n := 0
	for range tp.ch {
		n++
	}
	if n != tapDepth {
		t.Fatalf("the cut tap held %d batches, want %d then closed", n, tapDepth)
	}
	if len(h.present(context.Background(), "ws")) != 0 {
		t.Fatal("a cut tap still counts as presence")
	}
}

// TestEventStreamOverHTTP is the route itself: no credential is 401, a space
// the caller is not in is 404, and a member gets text/event-stream opening with
// a comment and carrying the event pushed while it is open.
func TestEventStreamOverHTTP(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	if code, _ := call(t, c.app, http.MethodGet, "/v1/team/events", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("no credential = %d, want 401", code)
	}
	for _, s := range strangers {
		if code, _ := call(t, c.app, http.MethodGet, c.q("/v1/team/events"), s.who(c), nil); code != s.want {
			t.Errorf("%s streaming the space = %d, want %d", s.name, code, s.want)
		}
	}
	body := streamOnce(t, c, c.bob, c.bobID, func() { c.say(t, c.ada, general.ID, "live") })
	if !strings.HasPrefix(body, ": open\n\n") || !strings.Contains(body, "event: message.created\n") ||
		!strings.Contains(body, `"text":"live"`) {
		t.Fatalf("stream body = %q", body)
	}
}

// streamOnce opens GET /events as `who`, runs push while it is open, then cuts
// the account's taps so the stream ends and the whole body can be read. It
// checks the stream's media type on the way.
func streamOnce(t *testing.T, c *cast, who map[string]string, account string, push func()) string {
	t.Helper()
	go func() {
		time.Sleep(300 * time.Millisecond)
		push()
		time.Sleep(200 * time.Millisecond)
		hub := mounted.State.trans.hub
		hub.mu.Lock()
		for tp := range hub.taps[c.ws] {
			if tp.account == account {
				hub.cut(tp)
			}
		}
		hub.mu.Unlock()
	}()
	req := httptest.NewRequest(http.MethodGet, c.q("/v1/team/events"), nil)
	for k, v := range who {
		req.Header.Set(k, v)
	}
	resp, err := c.app.Test(req, zip.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
