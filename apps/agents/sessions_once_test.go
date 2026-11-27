package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// sessions_once_test.go pins the append's once-per-requestId rule: a client that names
// a turn and retries it (after a 5xx, or a timeout that did apply) must find the
// transcript holding the turn once, whichever pod the retry reached.

// named appends a turn that names itself.
func named(t *testing.T, app *zip.App, org, session, request string, n int) (int, []byte) {
	t.Helper()
	return do(t, app, http.MethodPost, "/v1/agent/sessions/"+session+"/events", org,
		map[string]any{"kind": KindMessage, "payload": map[string]any{"n": n}, "requestId": request})
}

func appended(t *testing.T, app *zip.App, org, session, request string, n int) eventView {
	t.Helper()
	code, b := named(t, app, org, session, request, n)
	if code != http.StatusCreated {
		t.Fatalf("append %q = %d %s, want 201", request, code, b)
	}
	var v eventView
	mustJSON(t, b, &v)
	return v
}

// turns is what the store holds for a session, oldest first.
func turns(t *testing.T, org, session string) []Event {
	t.Helper()
	evs, err := storeOf(t, &mounted.State, org).ListEvents(context.Background(), org, session, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// heard drains the event frames the bus carried while the test ran.
func heard(ch <-chan streamUpdate) (events int) {
	for {
		select {
		case u := <-ch:
			if u.Type == "event" {
				events++
			}
		case <-time.After(150 * time.Millisecond):
			return events
		}
	}
}

func TestARetriedEventIsStoredOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		root := register(t, app, "acme", map[string]any{"agent": "dev"})
		ch, cancel := mounted.State.bus.subscribe("acme")
		defer cancel()

		code1, body1 := named(t, app, "acme", root.ID, "turn-1", 1)
		code2, body2 := named(t, app, "acme", root.ID, "turn-1", 1)
		if code1 != http.StatusCreated || code2 != http.StatusCreated {
			t.Fatalf("both answers must be 201, got %d and %d (%s)", code1, code2, body2)
		}
		if string(body1) != string(body2) {
			t.Fatalf("the retry must answer with the stored event as it was first answered:\n%s\n%s", body1, body2)
		}
		if evs := turns(t, "acme", root.ID); len(evs) != 1 {
			t.Fatalf("a retried turn is stored once, got %d rows", len(evs))
		}
		if n := heard(ch); n != 1 {
			t.Fatalf("the stream sees one publish, got %d", n)
		}
	})
}

func TestTurnsThatAreNotTheSameRequestAreEachStored(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		root := register(t, app, "acme", map[string]any{"agent": "dev"})
		other := register(t, app, "acme", map[string]any{"agent": "dev"})

		a := appended(t, app, "acme", root.ID, "turn-a", 1)
		b := appended(t, app, "acme", root.ID, "turn-b", 2)
		if a.ID == b.ID || a.Seq == b.Seq {
			t.Fatalf("two request ids are two events: %+v %+v", a, b)
		}
		// no request id, twice: today's behaviour kept
		for range 2 {
			if code, body := do(t, app, http.MethodPost, "/v1/agent/sessions/"+root.ID+"/events", "acme",
				map[string]any{"kind": KindMessage, "payload": map[string]any{"n": 3}}); code != http.StatusCreated {
				t.Fatalf("unnamed append = %d %s", code, body)
			}
		}
		if n := len(turns(t, "acme", root.ID)); n != 4 {
			t.Fatalf("2 named + 2 unnamed = 4 rows, got %d", n)
		}
		// the same id on another session is another request
		c := appended(t, app, "acme", other.ID, "turn-a", 1)
		if c.SessionID != other.ID || c.Seq != 1 {
			t.Fatalf("the same id on another session is its own first turn: %+v", c)
		}
		if n := len(turns(t, "acme", other.ID)); n != 1 {
			t.Fatalf("the other session holds one turn, got %d", n)
		}
	})
}

func TestARequestIDIsTrimmed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		root := register(t, app, "acme", map[string]any{"agent": "dev"})
		a := appended(t, app, "acme", root.ID, "turn-1", 1)
		b := appended(t, app, "acme", root.ID, "  turn-1\t", 1)
		if a.ID != b.ID || len(turns(t, "acme", root.ID)) != 1 {
			t.Fatalf("an id with stray spaces is the same id: %+v %+v", a, b)
		}
	})
}

func TestARacingPairOfOneRequestEndsWithOneRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		root := register(t, app, "acme", map[string]any{"agent": "dev"})

		const racers = 8
		var wg sync.WaitGroup
		got := make([]eventView, racers)
		for i := range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code, b := named(t, app, "acme", root.ID, "race", 1)
				if code != http.StatusCreated {
					t.Errorf("racer %d = %d %s", i, code, b)
					return
				}
				mustJSON(t, b, &got[i])
			}()
		}
		wg.Wait()
		if n := len(turns(t, "acme", root.ID)); n != 1 {
			t.Fatalf("racing appends of one id leave one row, got %d", n)
		}
		for i := range got {
			if got[i].ID != got[0].ID || got[i].Seq != got[0].Seq {
				t.Fatalf("every racer answers with the one event: %+v vs %+v", got[i], got[0])
			}
		}
	})
}

// The store's own guarantee, below the handler: two appends of one id from two
// callers end with one row and the same answer.
func TestTheStoreAppendsOneRequestOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := &state{stores: testStores(t)}
		sto := storeOf(t, st, "acme")
		ctx := context.Background()
		if err := sto.CreateSession(ctx, mkSession("acme", "sess_o", "", "sess_o")); err != nil {
			t.Fatal(err)
		}
		const racers = 8
		var wg sync.WaitGroup
		got := make([]Event, racers)
		for i := range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				e, err := sto.AppendEvent(ctx, Event{
					ID: "evt_" + string(rune('a'+i)), SessionID: "sess_o", Org: "acme",
					Kind: KindLog, Actor: "acme/u1", Payload: `{"n":1}`, CreatedAt: 1, RequestID: "r1",
				})
				if err != nil {
					t.Errorf("racer %d: %v", i, err)
				}
				got[i] = e
			}()
		}
		wg.Wait()
		evs, err := sto.ListEvents(ctx, "acme", "sess_o", 0, 100)
		if err != nil || len(evs) != 1 {
			t.Fatalf("one row, got %d, %v", len(evs), err)
		}
		fresh := 0
		for _, e := range got {
			if e.ID != evs[0].ID || e.Seq != 1 {
				t.Fatalf("every racer answers with the stored event %s: %+v", evs[0].ID, e)
			}
			if !e.Replay {
				fresh++
			}
		}
		if fresh != 1 {
			t.Fatalf("exactly one append is the first, got %d", fresh)
		}
	})
}

func TestARetriedProgressTurnMovesProgressOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		id := openSession(t, app, "acme", "ship the reaper")
		send := func(request, payload string) int {
			body := map[string]any{"kind": KindProgress, "payload": json.RawMessage(payload)}
			if request != "" {
				body["requestId"] = request
			}
			code, b := do(t, app, http.MethodPost, "/v1/agent/sessions/"+id+"/events", "acme", body)
			if code != http.StatusCreated {
				t.Fatalf("report = %d %s", code, b)
			}
			return code
		}
		send("p1", `{"pct":60,"phase":"running"}`)
		send("", `{"pct":80,"phase":"running"}`) // the run moved on
		send("p1", `{"pct":60,"phase":"running"}`) // the first report's retry arrives late
		p, _ := progressInList(t, app, "acme", id)
		if p.Pct == nil || *p.Pct != 80 {
			t.Fatalf("a retried report must not move progress back, got %+v", p)
		}
		if n := len(turns(t, "acme", id)); n != 2 {
			t.Fatalf("two reports stored, got %d", n)
		}
	})
}

// A reused id that carries another turn is another request, refused 422 as
// apps/coding refuses one (idempotency.go): answering with the first turn would tell
// the writer its second turn was kept when it was not.
func TestAReusedRequestIDWithAnotherTurnIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		root := register(t, app, "acme", map[string]any{"agent": "dev"})
		appended(t, app, "acme", root.ID, "turn-1", 1)

		code, b := named(t, app, "acme", root.ID, "turn-1", 2)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("same id, another payload: want 422, got %d %s", code, b)
		}
		code, b = do(t, app, http.MethodPost, "/v1/agent/sessions/"+root.ID+"/events", "acme",
			map[string]any{"kind": KindLog, "payload": map[string]any{"n": 1}, "requestId": "turn-1"})
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("same id, another kind: want 422, got %d %s", code, b)
		}
		if n := len(turns(t, "acme", root.ID)); n != 1 {
			t.Fatalf("a refused reuse stores nothing, got %d rows", n)
		}
	})
}

func TestARequestIDThatCannotBeKeptIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		root := register(t, app, "acme", map[string]any{"agent": "dev"})
		for name, rid := range map[string]string{
			"too long":    strings.Repeat("a", 257),
			"newline":     "a\nb",
			"nul":         "a\x00b",
			"escape":      "a\x1bb",
			"delete char": "a\x7fb",
		} {
			if code, b := named(t, app, "acme", root.ID, rid, 1); code != http.StatusBadRequest {
				t.Fatalf("%s: want 400, got %d %s", name, code, b)
			}
		}
		if code, b := named(t, app, "acme", root.ID, strings.Repeat("a", 256), 1); code != http.StatusCreated {
			t.Fatalf("an id at the bound is kept: %d %s", code, b)
		}
		if n := len(turns(t, "acme", root.ID)); n != 1 {
			t.Fatalf("a refused id stores nothing, got %d rows", n)
		}
	})
}
