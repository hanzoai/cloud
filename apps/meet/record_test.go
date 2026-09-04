// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package meet

// record_test.go — who may record a room, and what is actually sent when they do.
//
// The fixture stands up BOTH peers a recording needs, because a test with neither
// can only ever watch a refusal that nothing earned: a stand-in media server that
// answers LiveKit's Twirp API and remembers every call, and a stand-in object store
// that answers the bucket probe. With them in place, everything still refused was
// refused by a RULE — and the media server's own call log is what proves it, since
// a rule that ran too late would leave a start behind it.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/internal/planetest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── the two peers ────────────────────────────────────────────────────────────

// media stands in for the LiveKit server's Egress API. It answers Twirp JSON and
// records what it was asked, which is the half of every refusal test that says the
// refusal came from a rule rather than from an unreachable peer.
type media struct {
	*httptest.Server
	mu      sync.Mutex
	calls   []string         // Twirp method names, in order
	starts  []map[string]any // the decoded StartRoomCompositeEgress bodies
	stopped []string         // the egress ids StopEgress was asked to end
	bearers []string         // the Authorization value of every call
	held    []*shot          // every recording ListEgress reports, running or finished
	refuse  *refused         // when set, every call is answered with this Twirp error
	slow    time.Duration    // how long ListEgress takes — the window two replicas race in
	lies    bool             // ignore the roomName filter, as a peer that does not honour it
	flat    map[string]any   // when set, StartRoomCompositeEgress answers 200 with exactly this
	lists   []map[string]any // the decoded ListEgress bodies
	camel   bool             // answer in lowerCamelCase rather than the proto names
}

// firstStart is when the stand-in says its first recording began. NANOSECONDS,
// which is the magnitude LiveKit's egress service reports — a value this large is
// also what catches a reader that took proto3's JSON string as a number and
// reported 0.
const firstStart int64 = 1700000000000000000

// shot is one recording the stand-in is willing to report.
type shot struct {
	id      string
	room    string
	status  string
	object  string
	started int64
}

func newMedia(t *testing.T) *media {
	t.Helper()
	m := &media{}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Close)
	return m
}

func (m *media) serve(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, twirp)
	var in map[string]any
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &in)

	m.mu.Lock()
	m.calls = append(m.calls, method)
	slow, lies, flat := m.slow, m.lies, m.flat
	m.bearers = append(m.bearers, r.Header.Get("Authorization"))
	refuse, camel := m.refuse, m.camel
	if method == "StartRoomCompositeEgress" {
		m.starts = append(m.starts, in)
	}
	if method == "ListEgress" {
		m.lists = append(m.lists, in)
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if refuse != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(refuse)
		return
	}

	switch method {
	case "StartRoomCompositeEgress":
		if flat != nil {
			_ = json.NewEncoder(w).Encode(flat)
			return
		}
		room, _ := in["roomName"].(string)
		key := ""
		if outs, ok := in["fileOutputs"].([]any); ok && len(outs) > 0 {
			if f, ok := outs[0].(map[string]any); ok {
				key, _ = f["filepath"].(string)
			}
		}
		m.mu.Lock()
		n := len(m.held)
		one := &shot{id: "EG_" + strconv.Itoa(n+1), room: room, status: "EGRESS_STARTING", object: key, started: firstStart + int64(n)}
		m.held = append(m.held, one)
		got := *one
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(m.render(got, camel))
	case "ListEgress":
		room, _ := in["roomName"].(string)
		if slow > 0 {
			time.Sleep(slow)
		}
		m.mu.Lock()
		items := []any{}
		for _, one := range m.held {
			// An entry with NO room name is returned whatever was asked for — that is
			// the peer this models, and filtering it out here would hide the very
			// thing the test is about.
			if lies || room == "" || one.room == room || one.room == "" {
				items = append(items, m.render(*one, camel))
			}
		}
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case "StopEgress":
		id, _ := in["egressId"].(string)
		m.mu.Lock()
		m.stopped = append(m.stopped, id)
		got := shot{id: id, status: "EGRESS_COMPLETE"}
		for _, one := range m.held {
			if one.id == id {
				one.status = "EGRESS_COMPLETE"
				got = *one
			}
		}
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(m.render(got, camel))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// render writes one EgressInfo under whichever of the two names Twirp is
// configured to emit — a deployment picks one and never tells its clients.
func (m *media) render(s shot, camel bool) map[string]any {
	name := func(proto, json string) string {
		if camel {
			return json
		}
		return proto
	}
	out := map[string]any{
		name("egress_id", "egressId"):       s.id,
		name("room_name", "roomName"):       s.room,
		name("started_at", "startedAt"):     strconv.FormatInt(s.started, 10),
		name("file_results", "fileResults"): []any{map[string]any{"filename": s.object}},
	}
	// An EMPTY status is OMITTED, not sent as "". proto3's JSON mapping drops a
	// field at its default value unless the encoder sets EmitUnpopulated, and
	// EgressStatus's default is EGRESS_STARTING — so this is exactly the wire a
	// peer produces for a recording that has just started.
	if s.status != "" {
		out["status"] = s.status
	}
	return out
}

func (m *media) asked() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

func (m *media) startedWith() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]map[string]any(nil), m.starts...)
}

// storeKey and storeSecret are the object store credential this deployment holds.
// The tests assert they reach the media server, because what the egress worker is
// handed is the whole store.
const (
	storeKey    = "s3-access-key"
	storeSecret = "s3-secret-key"
)

// store stands in for the object store, answering the bucket probe the start makes
// before it hands a caller a recording that would have nowhere to go.
func newStore(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(s.Close)
	return s
}

// host strips the scheme off an httptest URL, since both the LiveKit address and
// the S3 endpoint are configured as bare hosts.
func host(u string) string { return strings.TrimPrefix(u, "http://") }

// recordEnv points this process's recording plane at the two stand-ins. It must
// run BEFORE load(), which is why every fixture below calls it first.
func recordEnv(t *testing.T, m *media, s *httptest.Server) {
	t.Helper()
	if m != nil {
		t.Setenv(wsEnv, "ws://"+host(m.URL))
	}
	if s != nil {
		t.Setenv("S3_ADMIN_ENDPOINT", host(s.URL))
		t.Setenv("S3_ADMIN_ACCESS_KEY", storeKey)
		t.Setenv("S3_ADMIN_SECRET_KEY", storeSecret)
		t.Setenv("S3_SECURE", "false")
		t.Setenv("S3_REGION", "us-east-1")
	}
}

// recordUse is the whole deployment: the real identity boundary, a space
// authority that answers, and both peers a recording needs.
func recordUse(t *testing.T, rows roster) (*zip.App, *media) {
	t.Helper()
	m := newMedia(t)
	recordEnv(t, m, newStore(t))
	return mountWith(t, keyFileWith(t, keyBody(apiKey, apiSecret)), rows), m
}

// billedRecord is recordUse plus a ledger: the same identity boundary, the same
// space authority and the same two peers, with money behind the meter.
func billedRecord(t *testing.T, l *planetest.Ledger) (*zip.App, *media) {
	t.Helper()
	m := newMedia(t)
	recordEnv(t, m, newStore(t))
	iamIssuer(t)
	t.Setenv(keyFileEnv, keyFileWith(t, keyBody(apiKey, apiSecret)))
	st := load()
	st.authority = holds(map[string]string{spaceA: token.RoleMember})
	sharedKey(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.IdentityMiddleware(&cloud.Config{IAMIssuer: iamtest.Issuer, JWKSURL: jwksURL}))
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_ENV", "mainnet")
	if err := serve(app, cloud.Deps{Metering: l.Client(t)}, st); err != nil {
		t.Fatalf("serve: %v", err)
	}
	return app, m
}

// rec makes one call on /v1/meet/record. The room rides the BODY on the POST and
// the QUERY on the two bodyless methods, which is the wire zip publishes for each.
func rec(t *testing.T, app *zip.App, method, room, bearer string) (int, recording, string) {
	t.Helper()
	path, body := "/v1/meet/record", io.Reader(nil)
	if method == http.MethodPost {
		raw, _ := json.Marshal(recordIn{Room: room})
		body = bytes.NewReader(raw)
	} else {
		path += "?room=" + url.QueryEscape(room)
	}
	req := httptest.NewRequest(method, path, body)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out recording
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("body is not the recording shape: %v\n%s", err, raw)
		}
	}
	return resp.StatusCode, out, string(raw)
}

// ── the endpoint ─────────────────────────────────────────────────────────────────

// TestRecordRefusesAnyoneTheRoomWouldNot is the fail-closed table, and it is the
// test this whole file exists for.
//
// A recording is a durable artifact of other people's conversation, so the rule is
// that only somebody the room would ADMIT may start, stop or read one — the same
// decision /v1/meet/getToken makes. Every row below breaks exactly one thing that
// decision turns on, and each is run against all three methods, because three entry
// points onto one rule is three chances to leave one open.
//
// THE MEDIA SERVER IS ASSERTED UNTOUCHED. Without that, a surface that authorized
// AFTER starting a recording would pass every row here: the caller would still get
// a 401 and the meeting would still be on disk. "Refused" and "refused before
// anything happened" are different results and only the second one is safe.
func TestRecordRefusesAnyoneTheRoomWouldNot(t *testing.T) {
	member := func(t *testing.T) string { return access(t, ada) }
	cases := []struct {
		name   string
		room   string
		rows   roster // nil ⇒ the ordinary deployment: ada is a member of space A
		bearer func(t *testing.T) string
	}{
		{"no bearer at all", roomIn(spaceA), nil, func(*testing.T) string { return "" }},
		{"not a token", roomIn(spaceA), nil, func(*testing.T) string { return "not-a-jwt" }},
		// Signed by an issuer this deployment publishes no keys for. IAM's signature
		// is the only thing that can produce a principal here, so this is the whole
		// forgery surface.
		{"forged: signed by another issuer", roomIn(spaceA), anyone(), func(t *testing.T) string {
			return iamtest.New(t).Sign(t, iamtest.Claims{Sub: ada, Owner: org, Orgs: homeOrg})
		}},
		{"expired session", roomIn(spaceA), anyone(), func(t *testing.T) string {
			return issuer.Sign(t, iamtest.Claims{Sub: ada, Owner: org, Orgs: homeOrg, Exp: time.Now().Add(-time.Hour)})
		}},
		// A MACHINE credential: an org and a user, and no `sub`. "Which humans are in
		// this room" is not a question an API key gets to answer, and recording one is
		// not a thing a key gets to do. The authority says yes to everything, so only
		// the rule refuses.
		{"machine credential carries no subject", roomIn(spaceA), anyone(), func(t *testing.T) string {
			return issuer.Sign(t, iamtest.Claims{Owner: org, Orgs: homeOrg, PreferredUsername: "sk-key-user"})
		}},
		// THE tenant boundary: the rows put ada in space A and the room names B.
		// Room names are client-chosen, so this is the only thing stopping one
		// space from recording another's meeting.
		{"member of another space", roomIn(spaceB), nil, member},
		{"room with an empty space segment", "_standup_1", nil, member},
		{"separator-less room", "lobby", nil, member},
		// A guest is a reduced principal. Sitting in a colleague's meeting is not a
		// guest privilege, so neither is recording one.
		{"guest role", roomIn(spaceA), holds(map[string]string{spaceA: token.RoleGuest}), member},
		{"no role on the row", roomIn(spaceA), holds(map[string]string{spaceA: ""}), member},
		{"unknown future role", roomIn(spaceA), holds(map[string]string{spaceA: "observer"}), member},
		{"not a member of anything", roomIn(spaceA), holds(nil), member},
		// The authority answered with a row that seats nobody. getToken refuses it
		// because LiveKit cannot seat an empty identity; this refuses it because the
		// two entry points onto one room must not disagree about who is in it.
		{"the row names no account", roomIn(spaceA), seats(""), member},
	}
	for _, c := range cases {
		for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodGet} {
			t.Run(c.name+"/"+method, func(t *testing.T) {
				rows := c.rows
				if rows == nil {
					rows = holds(map[string]string{spaceA: token.RoleMember})
				}
				app, m := recordUse(t, rows)
				code, _, body := rec(t, app, method, c.room, c.bearer(t))
				if code != http.StatusUnauthorized {
					t.Fatalf("got %d %q, want 401", code, body)
				}
				if asked := m.asked(); len(asked) != 0 {
					t.Fatalf("the media server was asked %v for a caller this room refuses — "+
						"authorization must precede the peer, or a refusal still leaves a recording behind", asked)
				}
			})
		}
	}
	// The positive control. Without it every row above passes on a deployment that
	// admits nobody at all.
	t.Run("control: the ordinary member IS admitted", func(t *testing.T) {
		app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
		code, got, body := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada))
		if code != http.StatusOK {
			t.Fatalf("got %d %q, want 200 — the refusals above prove nothing if nobody may ever record", code, body)
		}
		if got.ID == "" {
			t.Errorf("no recording id in %q", body)
		}
		if asked := m.asked(); len(asked) == 0 {
			t.Fatal("an admitted caller reached no media server, so nothing was recorded")
		}
	})
}

// TestTheSecondBearerAuthorityIsClosedForRecording. meet used to accept apps/team's
// HS256 space session as authorization; that lane is gone from the mint and it
// must never reappear on a surface that records people.
func TestTheSecondBearerAuthorityIsClosedForRecording(t *testing.T) {
	app, m := recordUse(t, anyone())
	tok, err := token.Generate(account, spaceA, map[string]any{"role": token.RoleOwner}, time.Now().Add(time.Hour).Unix(), "a-real-team-secret")
	if err != nil {
		t.Fatalf("token.Generate: %v", err)
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodGet} {
		if code, _, body := rec(t, app, method, roomIn(spaceA), tok); code != http.StatusUnauthorized {
			t.Fatalf("SECURITY: an HS256 space session reached %s /v1/meet/record: %d %q", method, code, body)
		}
	}
	if asked := m.asked(); len(asked) != 0 {
		t.Fatalf("the media server was asked %v under a space session", asked)
	}
}

// ── one recording per room ───────────────────────────────────────────────────

// TestASecondStartReturnsTheRunningRecording pins the choice this surface made:
// there is at most one recording per room, and asking for a second one hands back
// the first rather than refusing. The property that matters is not which of the two
// answers it is — it is that no SECOND egress is started, because two recorders on
// one room is two files, two bills and two things to stop.
func TestASecondStartReturnsTheRunningRecording(t *testing.T) {
	app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
	room := roomIn(spaceA)

	_, first, body := rec(t, app, http.MethodPost, room, access(t, ada))
	if first.ID == "" {
		t.Fatalf("no recording started: %s", body)
	}
	code, second, body := rec(t, app, http.MethodPost, room, access(t, ada))
	if code != http.StatusOK {
		t.Fatalf("a second start = %d %q, want 200 with the running recording", code, body)
	}
	if second.ID != first.ID {
		t.Errorf("second start = %q, want the running recording %q", second.ID, first.ID)
	}
	if n := len(m.startedWith()); n != 1 {
		t.Fatalf("StartRoomCompositeEgress was called %d times; a room must never have two recorders", n)
	}
}

// TestStopEndsTheRoomsOwnRecording: the id stopped is the one the MEDIA SERVER
// reports for this room, never one the caller could name. An egress id names a
// recording of some room, and a caller who could pass one directly would stop a
// recording in a room it was never admitted to.
func TestStopEndsTheRoomsOwnRecording(t *testing.T) {
	app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
	room := roomIn(spaceA)

	_, started, _ := rec(t, app, http.MethodPost, room, access(t, ada))
	code, stopped, body := rec(t, app, http.MethodDelete, room, access(t, ada))
	if code != http.StatusOK {
		t.Fatalf("stop = %d %q, want 200", code, body)
	}
	if stopped.ID != started.ID {
		t.Errorf("stopped %q, want the recording that was running, %q", stopped.ID, started.ID)
	}
	m.mu.Lock()
	ended := append([]string(nil), m.stopped...)
	m.mu.Unlock()
	if len(ended) != 1 || ended[0] != started.ID {
		t.Fatalf("StopEgress was asked to end %v, want exactly [%s]", ended, started.ID)
	}
	// And the read still says where it went — which is the point of stopping.
	_, after, body := rec(t, app, http.MethodGet, room, access(t, ada))
	if after.ID != started.ID {
		t.Errorf("after stopping, the read reports %q, want the recording that was made, %q: %s", after.ID, started.ID, body)
	}
	if after.Status != "EGRESS_COMPLETE" {
		t.Errorf("status after stopping = %q, want the media server's own terminal state", after.Status)
	}
	if after.Object != started.Object {
		t.Errorf("the read reports object %q, the start reported %q", after.Object, started.Object)
	}
	// And a stopped room takes a new recording: a terminal state must not block one.
	if code, again, body := rec(t, app, http.MethodPost, room, access(t, ada)); code != http.StatusOK || again.ID == started.ID {
		t.Errorf("re-recording a stopped room = %d %q, want a NEW recording", code, body)
	}
}

// TestStoppingAnUnrecordedRoomIsNotAnError: nobody is recording, so the answer is
// the room with no recording on it. A 404 would make a client that stops on leaving
// a call treat the ordinary case as a failure.
func TestStoppingAnUnrecordedRoomIsNotAnError(t *testing.T) {
	app, _ := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
	code, got, body := rec(t, app, http.MethodDelete, roomIn(spaceA), access(t, ada))
	if code != http.StatusOK {
		t.Fatalf("stop with nothing running = %d %q, want 200", code, body)
	}
	if got.ID != "" || got.Room != roomIn(spaceA) {
		t.Errorf("got %+v, want the room named and no recording", got)
	}
}

// ── what is actually sent ────────────────────────────────────────────────────

// TestTheEgressTokenRecordsAndCannotJoin is the least-privilege assertion on the
// credential this binary presents to the media server.
//
// It is a DIFFERENT grant from the one a browser gets, and the separation has to
// hold in both directions: a token that could also join would, if it leaked, put
// its holder in the call; a join token that could record would let any participant
// record from the browser without passing this surface's check at all.
func TestTheEgressTokenRecordsAndCannotJoin(t *testing.T) {
	app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
	room := roomIn(spaceA)
	before := time.Now()
	if code, _, body := rec(t, app, http.MethodPost, room, access(t, ada)); code != http.StatusOK {
		t.Fatalf("start = %d %q", code, body)
	}
	m.mu.Lock()
	bearers := append([]string(nil), m.bearers...)
	m.mu.Unlock()
	if len(bearers) == 0 {
		t.Fatal("the media server was called with no Authorization at all")
	}
	for _, b := range bearers {
		tok := strings.TrimPrefix(b, "Bearer ")
		if tok == b || tok == "" {
			t.Fatalf("Authorization %q is not a bearer token", b)
		}
		claims := verify(t, tok, apiSecret) // the media server's own check, re-implemented
		grant, ok := claims["video"].(map[string]any)
		if !ok {
			t.Fatalf("no video grant: %v", claims)
		}
		if grant["roomRecord"] != true {
			t.Errorf("video.roomRecord = %v, want true — the media server refuses an egress without it", grant["roomRecord"])
		}
		if _, present := grant["roomJoin"]; present {
			t.Error("SECURITY: the egress token also grants roomJoin — a leaked API token would seat its holder in the call")
		}
		for _, priv := range []string{"roomAdmin", "roomCreate", "roomList", "ingressAdmin", "agent", "hidden", "recorder"} {
			if _, present := grant[priv]; present {
				t.Errorf("grant names %q; an egress token must not carry it", priv)
			}
		}
		if grant["room"] != room {
			t.Errorf("video.room = %v, want %q", grant["room"], room)
		}
		// Attributed to the person who asked, not to the deployment.
		if claims["sub"] != account {
			t.Errorf("sub = %v, want the admitted account %q", claims["sub"], account)
		}
		// One call, immediately: minutes of life would be a standing key to the
		// whole deployment's recorder if the token ever escaped a log.
		exp, _ := claims["exp"].(float64)
		if d := time.Unix(int64(exp), 0).Sub(before); d > 2*time.Minute {
			t.Errorf("the egress token lives %s; it is used once, at once", d)
		}
		if strings.Contains(tok, apiSecret) {
			t.Fatal("the signing key leaked into the token")
		}
	}
	// And a join token minted by the same binary must still not be able to record.
	_, join := ask(t, app, room, "person-42", access(t, ada))
	joinGrant := verify(t, join, apiSecret)["video"].(map[string]any)
	if _, present := joinGrant["roomRecord"]; present {
		t.Error("SECURITY: the browser's join token carries roomRecord — a participant could record without passing this check")
	}
}

// TestTheRecordingIsWrittenToThisDeploymentsStore: the egress worker uploads the
// file itself, so what crosses to it is this deployment's own object-store
// configuration — the one apps/s3admin reads — and never a second copy of it.
func TestTheRecordingIsWrittenToThisDeploymentsStore(t *testing.T) {
	app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
	if code, _, body := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada)); code != http.StatusOK {
		t.Fatalf("start = %d %q", code, body)
	}
	starts := m.startedWith()
	if len(starts) != 1 {
		t.Fatalf("StartRoomCompositeEgress called %d times, want 1", len(starts))
	}
	outs, ok := starts[0]["fileOutputs"].([]any)
	if !ok || len(outs) != 1 {
		t.Fatalf("want exactly one file output, got %v", starts[0]["fileOutputs"])
	}
	file := outs[0].(map[string]any)
	if file["fileType"] != "MP4" {
		t.Errorf("fileType = %v, want MP4", file["fileType"])
	}
	s3, ok := file["s3"].(map[string]any)
	if !ok {
		t.Fatalf("no s3 destination: %v", file)
	}
	for field, want := range map[string]any{
		"accessKey":      storeKey,
		"secret":         storeSecret,
		"bucket":         bucket,
		"region":         "us-east-1",
		"forcePathStyle": true,
	} {
		if s3[field] != want {
			t.Errorf("s3.%s = %v, want %v", field, s3[field], want)
		}
	}
	// The endpoint carries a scheme, which is what an S3 client that is not
	// hanzos3/go needs; a bare host would be a silent upload failure after the
	// meeting had already been recorded.
	if e, _ := s3["endpoint"].(string); !strings.HasPrefix(e, "http://") {
		t.Errorf("s3.endpoint = %q, want a URL with a scheme", e)
	}
	// No sidecar manifest: the bucket's contents are exactly what this surface
	// reports.
	if file["disableManifest"] != true {
		t.Errorf("disableManifest = %v, want true", file["disableManifest"])
	}
}

// TestTheObjectStaysInsideItsTenant. Past its leading space segment a room name
// is arbitrary CLIENT text — only the segment before the first underscore is ever
// checked — so a name carrying separators or dot-dot would otherwise compose a key
// outside the org prefix it was given, and write one tenant's meeting into another's
// space.
func TestTheObjectStaysInsideItsTenant(t *testing.T) {
	at := time.Unix(1700000000, 0)
	for _, name := range []string{
		spaceA + "_../../elsewhere",
		spaceA + "_a/b/c",
		spaceA + "_..",
		spaceA + "_" + strings.Repeat("x", 500),
		"",
	} {
		key, err := object("acme", name, at)
		if err != nil {
			t.Fatalf("object(%q): %v", name, err)
		}
		if !strings.HasPrefix(key, "acme/") {
			t.Errorf("object(%q) = %q, which is not under its tenant", name, key)
		}
		if n := strings.Count(key, "/"); n != 2 {
			t.Errorf("object(%q) = %q has %d separators, want exactly 2 (<org>/<room>/<file>)", name, key, n)
		}
		for _, bad := range []string{"..", "//"} {
			if strings.Contains(key, bad) {
				t.Errorf("object(%q) = %q contains %q", name, key, bad)
			}
		}
	}
	// A tenant cannot be written into a sibling's prefix by naming one either.
	if key, _ := object("acme/../evil", roomIn(spaceA), at); !strings.HasPrefix(key, "acme-") {
		t.Errorf("object with a hostile org = %q, want the org folded to one segment", key)
	}
}

// TestTheAnswerNamesWhereTheRecordingWent: a caller is told the object key, and it
// is the one this deployment actually asked the media server to write.
func TestTheAnswerNamesWhereTheRecordingWent(t *testing.T) {
	app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
	room := roomIn(spaceA)
	_, got, body := rec(t, app, http.MethodPost, room, access(t, ada))
	if got.Object == "" {
		t.Fatalf("the answer says nothing about where the recording went: %s", body)
	}
	if !strings.HasPrefix(got.Object, org+"/") {
		t.Errorf("object %q is not under the caller's own org %q", got.Object, org)
	}
	if !strings.HasSuffix(got.Object, ".mp4") {
		t.Errorf("object %q does not name a file", got.Object)
	}
	// A key with no bucket is half an address.
	if got.Bucket != bucket {
		t.Errorf("bucket = %q, want %q — a key alone does not say where the recording is", got.Bucket, bucket)
	}
	file := m.startedWith()[0]["fileOutputs"].([]any)[0].(map[string]any)
	if file["filepath"] != got.Object {
		t.Errorf("the caller was told %q and the media server was told %q", got.Object, file["filepath"])
	}
	// The read beside it answers the same fact for a caller that did not start it.
	if _, read, body := rec(t, app, http.MethodGet, room, access(t, ada)); read.Object != got.Object {
		t.Errorf("the read reports %q, the start reported %q: %s", read.Object, got.Object, body)
	}
}

// TestEitherTwirpSpellingIsUnderstood. Twirp emits the PROTO field names by default
// and the JSON ones when the server was built with camelCase; a deployment picks
// one without telling its clients. A reader that knows only one of them reports an
// empty id and an empty status for every recording, which reads as "not recording"
// — so a second start would begin a second recorder on a room already being
// recorded.
func TestEitherTwirpSpellingIsUnderstood(t *testing.T) {
	for _, camel := range []bool{false, true} {
		t.Run(map[bool]string{false: "proto names", true: "camelCase"}[camel], func(t *testing.T) {
			app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
			m.mu.Lock()
			m.camel = camel
			m.mu.Unlock()
			_, got, body := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada))
			if got.ID == "" {
				t.Fatalf("no egress id read back: %s", body)
			}
			if got.Status != "EGRESS_STARTING" {
				t.Errorf("status = %q, want EGRESS_STARTING", got.Status)
			}
			// proto3 carries a 64-bit integer as a JSON STRING; a reader that took it
			// as a number would report 0 here, and one that lost precision would
			// report a neighbour of this.
			if got.Started != firstStart {
				t.Errorf("started = %d, want %d verbatim", got.Started, firstStart)
			}
		})
	}
}

// ── unconfigured, and refused upstream ───────────────────────────────────────

// TestRecordSaysWhyItCannot. A deployment that cannot record must say so, naming
// what is missing, rather than answering 200 over a recording that is not
// happening. The reason is for a member of the room and reaches nobody else — which
// is why authorization runs first, and why the media server is never touched.
func TestRecordSaysWhyItCannot(t *testing.T) {
	cases := []struct {
		name  string
		peers func(t *testing.T) *media // what this deployment is given
		names string                    // the fact the refusal has to name
	}{
		{"no media server address", func(t *testing.T) *media {
			recordEnv(t, nil, newStore(t))
			t.Setenv(wsEnv, "")
			return nil
		}, wsEnv},
		{"a media address that is not a URL", func(t *testing.T) *media {
			recordEnv(t, nil, newStore(t))
			t.Setenv(wsEnv, "live.hanzo.bot:7880")
			return nil
		}, wsEnv},
		{"no object store", func(t *testing.T) *media {
			m := newMedia(t)
			recordEnv(t, m, nil)
			t.Setenv("S3_ADMIN_ACCESS_KEY", "")
			t.Setenv("S3_ADMIN_SECRET_KEY", "")
			return m
		}, "S3_ADMIN_ACCESS_KEY"},
	}
	for _, c := range cases {
		for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodGet} {
			t.Run(c.name+"/"+method, func(t *testing.T) {
				m := c.peers(t)
				app := mountWith(t, keyFileWith(t, keyBody(apiKey, apiSecret)),
					holds(map[string]string{spaceA: token.RoleMember}))
				code, _, body := rec(t, app, method, roomIn(spaceA), access(t, ada))
				if code != http.StatusServiceUnavailable {
					t.Fatalf("got %d %q, want 503 — a deployment that cannot record must never answer as though it did", code, body)
				}
				if !strings.Contains(body, c.names) {
					t.Errorf("the refusal %q does not name %s, so nobody knows what to fix", body, c.names)
				}
				if m != nil {
					if asked := m.asked(); len(asked) != 0 {
						t.Errorf("the media server was asked %v on a deployment that cannot record", asked)
					}
				}
			})
		}
	}
	// The same table proves nothing unless a configured deployment DOES record.
	t.Run("control: a configured deployment records", func(t *testing.T) {
		app, _ := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
		if code, _, body := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada)); code != http.StatusOK {
			t.Fatalf("got %d %q, want 200", code, body)
		}
	})
}

// TestNoRecordingIsClaimedWhenTheStoreIsUnreachable. The store answers the
// credential check from configuration alone, so the only way to find out the bucket
// is not there is to ask — and a start that skipped the asking would hand back a
// recording that fails to upload after the meeting is over, with nothing kept.
func TestNoRecordingIsClaimedWhenTheStoreIsUnreachable(t *testing.T) {
	m := newMedia(t)
	dead := newStore(t)
	recordEnv(t, m, dead)
	dead.Close() // configured, and not answering
	app := mountWith(t, keyFileWith(t, keyBody(apiKey, apiSecret)),
		holds(map[string]string{spaceA: token.RoleMember}))

	code, got, body := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %q, want 503", code, body)
	}
	if got.ID != "" {
		t.Errorf("a recording id was handed out for a store that cannot be reached: %q", got.ID)
	}
	for _, called := range m.asked() {
		if called == "StartRoomCompositeEgress" {
			t.Fatal("a recording was started into a store that does not answer")
		}
	}
}

// TestTheMediaServersRefusalIsNamedButNotQuoted. The commonest shape of "recording
// is not configured" is a media server with no egress worker registered, and the
// caller has to be able to tell that apart from a fault. Its CODE says which — a
// bounded vocabulary, so it can be repeated safely — while its prose stays in the
// log, because the request this surface sends carries the object store's credential
// and a peer that echoes its input is a peer writing into our answer.
func TestTheMediaServersRefusalIsNamedButNotQuoted(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{"unavailable", http.StatusServiceUnavailable},
		{"internal", http.StatusBadGateway},
		{"permission_denied", http.StatusBadGateway},
	}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
			m.mu.Lock()
			m.refuse = &refused{Code: c.code, Msg: "no available egress instances"}
			m.mu.Unlock()
			code, _, body := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada))
			if code != c.want {
				t.Fatalf("got %d %q, want %d", code, body, c.want)
			}
			if !strings.Contains(body, c.code) {
				t.Errorf("the answer %q does not name which kind of refusal this was", body)
			}
			if strings.Contains(body, "no available egress instances") {
				t.Errorf("the answer %q quotes the peer's prose; it belongs in the log", body)
			}
		})
	}
}

// ── the gate and the meter ───────────────────────────────────────────────────

// TestRecordingWritesNeedCSRF. Starting or stopping a recording is a write reachable
// from an ambient session COOKIE, and the deployment's CORS policy reflects
// *.hanzo.ai with credentials — a wildcard covering hosts that serve arbitrary user
// content. Without the gate, a page on one of them could start recording a
// colleague's call in a signed-in tab.
//
// The gate is on the GROUP, so this also pins that the new routes actually sit
// behind it rather than beside it.
func TestRecordingWritesNeedCSRF(t *testing.T) {
	app, _ := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
	browser := func(t *testing.T, method, path string, body io.Reader) int {
		t.Helper()
		rq := httptest.NewRequest(method, path, body)
		if body != nil {
			rq.Header.Set("Content-Type", "application/json")
		}
		rq.Header.Set("Cookie", "hanzo_iam_token=session-value")
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_acme")
		resp, err := app.Test(rq, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	body := func() io.Reader { return strings.NewReader(`{"room":"` + roomIn(spaceA) + `"}`) }

	if got := browser(t, http.MethodPost, "/v1/meet/record", body()); got != http.StatusForbidden {
		t.Errorf("POST /v1/meet/record from a signed-in tab with no CSRF token = %d, want 403 — "+
			"a cross-site page can start recording a call it was never in", got)
	}
	if got := browser(t, http.MethodDelete, "/v1/meet/record?room=x", nil); got != http.StatusForbidden {
		t.Errorf("DELETE /v1/meet/record with no CSRF token = %d, want 403", got)
	}
	// The read changes nothing, and requiring a token to poll would mean fetching
	// one before the page that fetches one.
	if got := browser(t, http.MethodGet, "/v1/meet/record?room=x", nil); got == http.StatusForbidden {
		t.Errorf("GET /v1/meet/record = 403 — reads change nothing")
	}
	// A header-authenticated caller is not CSRF-able: a cross-site page cannot set
	// Authorization. It is refused on its merits, never by the gate.
	rq := httptest.NewRequest(http.MethodPost, "/v1/meet/record", body())
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("Authorization", "Bearer some-iam-token")
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("bearer start: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusForbidden {
		t.Error("a bearer-authenticated start = 403 — the gate is running on the header lane")
	}
}

// TestAnUnfundedCallerRecordsNothing. Starting a recording stands up a dedicated
// worker and keeps an object, so it is authorized against the caller's balance
// BEFORE the media server is asked — a recording already running is a cost already
// being incurred, whatever the ledger says afterwards.
func TestAnUnfundedCallerRecordsNothing(t *testing.T) {
	l := planetest.Money(t, 0)
	app, m := billedRecord(t, l)

	code, got, body := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada))
	if code == http.StatusOK {
		t.Fatalf("an unfunded caller started recording %q: %s — the gate must refuse first", got.ID, body)
	}
	for _, called := range m.asked() {
		if called == "StartRoomCompositeEgress" {
			t.Fatal("a recording was started for a caller who cannot pay for it")
		}
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a refused start, want 0", n)
	}
	// Stopping is never billed: a caller made to pay to stop being recorded would
	// be paying for the wrong thing.
	if code, _, body := rec(t, app, http.MethodDelete, roomIn(spaceA), access(t, ada)); code != http.StatusOK {
		t.Fatalf("an unfunded caller could not stop a recording: %d %q", code, body)
	}
}

// TestStartingARecordingBills: the act is metered at the platform's provision fee,
// because it provisions a worker and keeps an object.
func TestStartingARecordingBills(t *testing.T) {
	l := planetest.Money(t, 100000)
	app, _ := billedRecord(t, l)

	if code, _, body := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada)); code != http.StatusOK {
		t.Fatalf("start = %d %q", code, body)
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — a started recording must bill", l.Count())
	}
	_, cents, model, _ := l.Charged()
	if model != record {
		t.Errorf("debit unit = %q, want %q", model, record)
	}
	if cents != cloud.DefaultResourceFeeCents {
		t.Errorf("debit = %dc, want the provision fee %dc", cents, cloud.DefaultResourceFeeCents)
	}
	// A second start begins no recording, so it bills nothing.
	if code, _, body := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada)); code != http.StatusOK {
		t.Fatalf("second start = %d %q", code, body)
	}
	if n := l.Count(); n != 1 {
		t.Errorf("debits = %d after a second start that began nothing, want 1", n)
	}
}

// ── the media address ────────────────────────────────────────────────────────

// TestTheApiOriginIsTheSocketsOwn. The Egress API and the WebSocket are one server
// on one port, so the address is derived rather than configured twice: a second
// variable could name a different server, and a token minted for one is refused by
// the other.
func TestTheApiOriginIsTheSocketsOwn(t *testing.T) {
	ok := map[string]string{
		"wss://live.hanzo.bot":       "https://live.hanzo.bot",
		"ws://127.0.0.1:7880":        "http://127.0.0.1:7880",
		"wss://live.hanzo.bot:443":   "https://live.hanzo.bot:443",
		"https://live.hanzo.bot":     "https://live.hanzo.bot",
		"wss://live.hanzo.bot/rtc":   "https://live.hanzo.bot",
		"wss://live.hanzo.bot?x=1":   "https://live.hanzo.bot",
		"ws://live.hanzo.bot:7880/x": "http://live.hanzo.bot:7880",
	}
	for in, want := range ok {
		got, err := origin(in)
		if err != nil {
			t.Errorf("origin(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("origin(%q) = %q, want %q", in, got, want)
		}
	}
	// A plaintext downgrade must be the operator's own word, never something an
	// address is quietly folded into.
	for _, bad := range []string{"live.hanzo.bot:7880", "", "ftp://live.hanzo.bot", "://x", "wss://"} {
		if got, err := origin(bad); err == nil {
			t.Errorf("origin(%q) = %q, want a refusal naming %s", bad, got, wsEnv)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// seats is a space authority that admits with a row naming the given account —
// including the empty one, which is a member the media server cannot seat.
func seats(id string) *answers {
	a := &answers{list: client.Spaces{Account: id}}
	a.row = func(string, string) client.Member {
		return client.Member{Member: true, Role: token.RoleMember, Account: id}
	}
	a.list.Items = append(a.list.Items, client.Space{UUID: spaceA, Role: token.RoleMember})
	return a
}

// ── the endpoints a route check does not cover ────────────────────────────────────

// mcp calls one tool over the MCP server the way a browser can: JSON-RPC in the body,
// whatever headers the caller chooses. It returns the raw envelope.
func mcp(t *testing.T, app *zip.App, method string, params map[string]any, head map[string]string) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	rq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(raw))
	rq.Header.Set("Content-Type", "application/json")
	for k, v := range head {
		rq.Header.Set(k, v)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestTheMCPEndpointIsNotAWayPastTheGate.
//
// A typed op is reachable by TWO entry points and only one is a route. zip records
// the route's handler and the op as two fields of one entry, and wraps only the
// handler (typed.go, addRoute); the MCP server calls op.invoke directly (mcp.go). So
// NO Use/Group/With reaches a tools/call — the identity middleware runs because it
// is installed at depth 0, which leaves the caller authenticated with every
// prefix-scoped gate skipped.
//
// That is the whole attack: a signed-in tab's cookie is ambient, a cross-origin
// POST with a CORS-simple content type needs no preflight, and the anti-CSRF token
// a route would have demanded is never asked for. The gate therefore cannot live on
// the group. It lives in ops.ready, which both entry points go through.
func TestTheMCPEndpointIsNotAWayPastTheGate(t *testing.T) {
	app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))

	// The tool is really there — otherwise this test passes by naming nothing.
	list := mcp(t, app, "tools/list", nil, nil)
	if !strings.Contains(list, "meetRecordStart") {
		t.Fatalf("meetRecordStart is not on the MCP server, so this test asserts nothing:\n%s", list)
	}

	body := mcp(t, app, "tools/call", map[string]any{
		"name":      "meetRecordStart",
		"arguments": map[string]any{"room": roomIn(spaceA)},
	}, map[string]string{
		// A signed-in tab: the cookie is ambient, so any page that can reach us
		// sends it. No CSRF token, and a content type that needs no preflight.
		"Cookie":       "hanzo_iam_token=" + access(t, ada),
		"Content-Type": "text/plain;charset=UTF-8",
		"Origin":       "https://evil.example",
	})
	for _, called := range m.asked() {
		if called == "StartRoomCompositeEgress" {
			t.Fatalf("SECURITY: a cross-site page started a recording through the MCP server with no CSRF token.\n%s", body)
		}
	}
}

// TestEveryWriteIsGatedOnEveryEndpointAndEveryHeader.
//
// The anti-CSRF gate has to hold across THREE independent axes, and a test that
// fixes two of them measures almost nothing:
//
//   - the ENTRY POINT. A typed op is not one. zip wraps only the route's
//     handler and calls the op directly over MCP, the call plane, GraphQL, the CLI
//     and Here — so the gate lives in the op's own preamble, and every surface has to
//     be shown to reach it.
//   - the OPERATION. start and stop both CHANGE something and read does not. Stop
//     is the consent-critical direction: a cross-site page that can end a recording
//     can kill a compliance record, and a suite that only exercised start would not
//     notice stop being marked a read.
//   - the HEADER. The gate steps aside for a caller holding an explicit credential,
//     and "explicit" has to mean exactly what the identity boundary reads. It is a
//     CROSS-HEADER precedence — bearer(Authorization), then bearer(X-Authorization),
//     then basic(Authorization) — so a value that is a credential under one header
//     and not the other is precisely where the gate and the boundary come apart.
//
// Every row is a signed-in tab: a real session cookie, which is ambient, and no
// CSRF token. The writes must be refused before the media server hears anything.
func TestEveryWriteIsGatedOnEveryEndpointAndEveryHeader(t *testing.T) {
	// basic64 is `user:password` — a WELL-FORMED Basic credential. Under
	// Authorization the boundary reads it and the caller is explicit; under
	// X-Authorization the boundary never tries Basic at all and falls through to
	// the cookie, so a gate that read it as explicit excused a cookie-authenticated
	// write.
	const basic64 = "Basic dXNlcjpwYXNzd29yZA=="
	creds := map[string]string{
		"no credential header": "",
		"junk":                 "x",
		"bare Bearer":          "Bearer",
		"another scheme":       "Token abc",
		"bare Basic":           "Basic",
		"the string null":      "null",
		"padded Bearer":        "  Bearer  ",
		"well-formed Basic":    basic64,
	}
	for name, value := range creds {
		for _, header := range []string{"Authorization", "X-Authorization"} {
			if value == "" && header == "X-Authorization" {
				continue // the same row as "no credential header" on the other name
			}
			t.Run(name+"/"+header, func(t *testing.T) {
				// A well-formed Basic under Authorization IS a credential the boundary
				// reads, so the gate correctly steps aside — and the request is then
				// refused on its merits by the endpoint instead. That row is the control
				// that keeps the rest from passing because nothing is ever explicit.
				explicit := value == basic64 && header == "Authorization"

				app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
				cookie := "hanzo_iam_token=" + access(t, ada)
				for _, surface := range []string{"rest", "mcp"} {
					m.clear()
					for _, op := range []struct {
						name    string
						method  string
						tool    string
						changes bool
						needs   bool // seed a live recording, or this op never reaches the peer
					}{
						{"start", http.MethodPost, "meetRecordStart", true, false},
						{"stop", http.MethodDelete, "meetRecordStop", true, true},
						{"read", http.MethodGet, "meetRecordRead", false, false},
					} {
						m.clear()
						if op.needs {
							// Without something to stop, an UNGATED stop answers "not
							// being recorded" and never calls the peer — so the peer log
							// would stay clean and prove nothing.
							m.seed(shot{id: "EG_live", room: roomIn(spaceA), status: "EGRESS_ACTIVE",
								object: "acme/x/y.mp4", started: firstStart})
						}
						before := len(m.asked())
						head := map[string]string{"Cookie": cookie}
						if value != "" {
							head[header] = value
						}
						var code int
						var body string
						if surface == "rest" {
							code, body = ambient(t, app, op.method, roomIn(spaceA), head)
						} else {
							// A cross-origin POST with a CORS-simple content type: no
							// preflight, so nothing stops a browser sending it.
							head["Content-Type"] = "text/plain;charset=UTF-8"
							head["Origin"] = "https://evil.example"
							body = mcp(t, app, "tools/call", map[string]any{
								"name": op.tool, "arguments": map[string]any{"room": roomIn(spaceA)},
							}, head)
						}
						if op.changes && !explicit {
							// The REFUSAL ITSELF, on whichever surface. MCP answers a handler
							// error as isError content rather than a status, so the surface
							// that has no status is asserted on the words — and on the
							// words of THIS gate, so a refusal for some other reason
							// cannot stand in for one that never happened.
							switch surface {
							case "rest":
								if code != http.StatusForbidden {
									t.Errorf("rest %s with %s: %q and a session cookie = %d %q, want 403",
										op.name, header, value, code, body)
								}
							default:
								if !strings.Contains(body, "CSRF") {
									t.Errorf("mcp %s with %s: %q and a session cookie answered %q, "+
										"want the anti-CSRF refusal", op.name, header, value, body)
								}
							}
							for _, called := range m.asked()[before:] {
								if called == "StartRoomCompositeEgress" || called == "StopEgress" {
									t.Fatalf("SECURITY: %s %s with %s: %q reached the media server (%s) "+
										"from a cross-site page with no CSRF token", surface, op.name, header, value, called)
								}
							}
						}
						if !op.changes && surface == "rest" && code == http.StatusForbidden {
							t.Errorf("%s read with %s: %q = 403 — a read changes nothing, and requiring a "+
								"token to poll would mean fetching one before the page that fetches one",
								surface, header, value)
						}
					}
				}
			})
		}
	}
}

// ambient makes one call the way a signed-in tab does: whatever headers are given,
// and no bearer of its own.
func ambient(t *testing.T, app *zip.App, method, room string, head map[string]string) (int, string) {
	t.Helper()
	path, body := "/v1/meet/record", io.Reader(nil)
	if method == http.MethodPost {
		raw, _ := json.Marshal(recordIn{Room: room})
		body = bytes.NewReader(raw)
	} else {
		path += "?room=" + url.QueryEscape(room)
	}
	rq := httptest.NewRequest(method, path, body)
	if method == http.MethodPost {
		rq.Header.Set("Content-Type", "application/json")
	}
	for k, v := range head {
		rq.Header.Set(k, v)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// TestStopEndsEveryRecordingOfTheRoom.
//
// "At most one recording per room" is an invariant this surface WANTS, not one it
// can impose: reading the media server's list and then starting are two calls, and
// two replicas racing through that window both start. The list is authoritative,
// so when it comes back holding two, two is the truth.
//
// Ending only the first and answering 200 is the worst failure this surface has.
// The person who pressed stop is withdrawing consent, and a 200 tells them the
// recording is over while a second worker keeps writing. Stop ends EVERY live one
// and answers 200 only when it did.
func TestStopEndsEveryRecordingOfTheRoom(t *testing.T) {
	app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
	room := roomIn(spaceA)

	// The two-replica shape: both reads answer "nothing running", so both start.
	m.mu.Lock()
	m.slow = 150 * time.Millisecond
	m.mu.Unlock()
	// The two starts go out on their own goroutines and report NOTHING back: a
	// t.Fatalf off the test's own goroutine is undefined, and what this measures is
	// the state the media server is left in, not either caller's answer.
	bearer := access(t, ada)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := json.Marshal(recordIn{Room: room})
			rq := httptest.NewRequest(http.MethodPost, "/v1/meet/record", bytes.NewReader(raw))
			rq.Header.Set("Content-Type", "application/json")
			rq.Header.Set("Authorization", "Bearer "+bearer)
			if resp, err := app.Test(rq, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}); err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	m.mu.Lock()
	m.slow = 0
	live := 0
	for _, one := range m.held {
		if one.status == "EGRESS_STARTING" {
			live++
		}
	}
	m.mu.Unlock()
	if live != 2 {
		t.Skipf("the race did not land (%d live) — this test only means something with two", live)
	}

	code, _, body := rec(t, app, http.MethodDelete, room, access(t, ada))
	if code != http.StatusOK {
		t.Fatalf("stop = %d %q, want 200", code, body)
	}
	m.mu.Lock()
	stillLive := []string{}
	for _, one := range m.held {
		if one.status == "EGRESS_STARTING" {
			stillLive = append(stillLive, one.id)
		}
	}
	m.mu.Unlock()
	if len(stillLive) != 0 {
		t.Fatalf("SECURITY: stop answered 200 while %v kept recording — the person withdrawing "+
			"consent was told it had stopped, and it had not", stillLive)
	}
}

// TestARoomsAnswerIsAboutThatRoom. The room binding is ONE argument to a remote
// peer, and nothing here checked what came back. Against a peer that does not
// honour the filter — a proxy, a version change, a compromised one — a read
// returned another tenant's recording and a stop ended it.
func TestARoomsAnswerIsAboutThatRoom(t *testing.T) {
	app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember, spaceB: token.RoleMember}))
	other := roomIn(spaceB)

	if code, _, body := rec(t, app, http.MethodPost, other, access(t, ada)); code != http.StatusOK {
		t.Fatalf("seed the other room: %d %q", code, body)
	}
	// From here the peer ignores roomName and answers with everything it holds.
	m.mu.Lock()
	m.lies = true
	m.mu.Unlock()

	mine := roomIn(spaceA)
	if _, got, body := rec(t, app, http.MethodGet, mine, access(t, ada)); got.ID != "" {
		t.Errorf("SECURITY: reading %s returned a recording of another room: %s", mine, body)
	}
	if code, _, body := rec(t, app, http.MethodDelete, mine, access(t, ada)); code != http.StatusOK {
		t.Fatalf("stop = %d %q", code, body)
	}
	m.mu.Lock()
	ended := append([]string(nil), m.stopped...)
	m.mu.Unlock()
	if len(ended) != 0 {
		t.Fatalf("SECURITY: stopping %s ended %v, which belongs to another room", mine, ended)
	}
}

// TestAFailureInsideA200IsNotASuccess. A 200 whose BODY says the recording failed —
// or says nothing at all — was read as a started recording: the caller got an
// object path naming a file nobody would ever write, and the ledger got a debit.
func TestAFailureInsideA200IsNotASuccess(t *testing.T) {
	cases := map[string]map[string]any{
		"the body says it failed": {
			"egress_id": "EG_x", "room_name": "r", "status": "EGRESS_FAILED",
			"error": "no available egress instances",
		},
		"the body says nothing": {},
		"the body has no id":    {"status": "EGRESS_ACTIVE"},
	}
	for name, flat := range cases {
		t.Run(name, func(t *testing.T) {
			l := planetest.Money(t, 100000)
			app, m := billedRecord(t, l)
			m.mu.Lock()
			m.flat = flat
			m.mu.Unlock()
			code, got, body := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada))
			if code == http.StatusOK {
				t.Errorf("a start the media server did not make answered %d %q (id %q)", code, body, got.ID)
			}
			if n := l.Count(); n != 0 {
				t.Errorf("debits = %d for a recording that was never started, want 0", n)
			}
		})
	}
}

// TestTwoRecordingsOfARoomDoNotShareAKey. One-second resolution rested on "at most
// one recording per room", which the race above disproves — and two workers handed
// the same key is last-write-wins: one meeting gone, both callers billed. Distinct
// room names that FOLD to the same label collide the same way.
func TestTwoRecordingsOfARoomDoNotShareAKey(t *testing.T) {
	at := time.Unix(1700000000, 0)
	seen := map[string]string{}
	for _, name := range []string{
		roomIn(spaceA), roomIn(spaceA), // the same room, the same second
		spaceA + "_a/b", spaceA + "_a-b", // two names, one label
		spaceA + "_" + strings.Repeat("x", 200) + "one",
		spaceA + "_" + strings.Repeat("x", 200) + "two", // both truncated
	} {
		key, err := object("acme", name, at)
		if err != nil {
			t.Fatalf("object(%q): %v", name, err)
		}
		if prev, dup := seen[key]; dup {
			t.Fatalf("SECURITY: %q and %q are both written to %q — last write wins and one "+
				"meeting is lost, with both callers billed", prev, name, key)
		}
		seen[key] = name
	}
}

// TestThePeerCannotSpeakThroughUs. The request this surface sends CONTAINS the
// object store's access key and endpoint, and a peer that echoes its input back in
// an error would reflect them to whoever asked. The transport error was already
// dropped for naming the internal host; the peer's own words are the same hazard
// from the same direction.
func TestThePeerCannotSpeakThroughUs(t *testing.T) {
	app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
	leak := "invalid request: fileOutputs[0].s3{endpoint=http://s3.hanzo.svc:9000 accessKey=" +
		storeKey + " secret=" + storeSecret + "} " + strings.Repeat("detail ", 600)
	m.mu.Lock()
	m.refuse = &refused{Code: "invalid_argument", Msg: leak}
	m.mu.Unlock()

	_, _, body := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada))
	for _, secret := range []string{storeKey, storeSecret, "s3.hanzo.svc", "accessKey"} {
		if strings.Contains(body, secret) {
			t.Errorf("SECURITY: the answer reflects %q from the peer: %s", secret, body)
		}
	}
	if len(body) > 512 {
		t.Errorf("the peer wrote %d bytes into our answer; its words belong in the log", len(body))
	}
	// The peer's CODE is a bounded vocabulary and does reach the caller — it is the
	// part that says which kind of failure this was.
	if !strings.Contains(body, "invalid_argument") {
		t.Errorf("the answer names no reason at all: %s", body)
	}
}

// clear empties the stand-in of recordings, so one mount can serve several rows
// without what one row started standing in the way of the next.
func (m *media) clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held, m.stopped = nil, nil
}

// seed puts one recording into the stand-in directly, so a test can name a state
// the stand-in would never reach on its own.
func (m *media) seed(one shot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held = append(m.held, &one)
}

// ── the four properties nothing held ─────────────────────────────────────────

// TestAStrangerLearnsNothingAboutTheDeployment.
//
// The 503 that says a deployment cannot record NAMES the variable and the Secret an
// operator has to fix, which is only safe because a caller reads it after being
// admitted to the room. Checking configuration first would hand that sentence to
// anyone who can reach the surface — and this endpoint is on the public API host.
//
// The order is the property. Nothing else in the suite fails if it is reversed.
func TestAStrangerLearnsNothingAboutTheDeployment(t *testing.T) {
	recordEnv(t, nil, newStore(t))
	t.Setenv(wsEnv, "")
	app := mountWith(t, keyFileWith(t, keyBody(apiKey, apiSecret)),
		holds(map[string]string{spaceA: token.RoleMember}))

	for _, bearer := range []string{"", "not-a-jwt"} {
		for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodGet} {
			code, _, body := rec(t, app, method, roomIn(spaceA), bearer)
			if code != http.StatusUnauthorized {
				t.Errorf("%s with bearer %q on an unconfigured deployment = %d, want 401", method, bearer, code)
			}
			for _, secret := range []string{wsEnv, "S3_ADMIN", "livekit", "media server"} {
				if strings.Contains(body, secret) {
					t.Errorf("SECURITY: an unauthenticated %s reads %q about this deployment: %s", method, secret, body)
				}
			}
		}
	}
}

// TestAnUnknownStateCountsAsRunning.
//
// live() is a DENYLIST of the media server's terminal states, so a state LiveKit
// adds tomorrow reads as running. That direction is the safe one and it is a
// choice: as an allowlist, an unrecognised state would read as finished and a
// second recorder would be started over a live recording. The worst case here is
// refusing a second recording of a room that has already stopped, which is a
// message; the other is two workers on one live conversation.
func TestAnUnknownStateCountsAsRunning(t *testing.T) {
	app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
	room := roomIn(spaceA)
	m.seed(shot{id: "EG_future", room: room, status: "EGRESS_PAUSED", object: "acme/x/y.mp4", started: firstStart})

	code, got, body := rec(t, app, http.MethodPost, room, access(t, ada))
	if code != http.StatusOK {
		t.Fatalf("start = %d %q", code, body)
	}
	if got.ID != "EG_future" {
		t.Errorf("start returned %q over a recording in an unrecognised state; want the running one, EG_future", got.ID)
	}
	for _, called := range m.asked() {
		if called == "StartRoomCompositeEgress" {
			t.Fatal("a second recorder was started over a recording whose state this build does not recognise")
		}
	}
}

// TestARefusedStartBillsNothing. The debit is taken AFTER the media server says it
// started, and moving it earlier is invisible to every other test here: the caller
// still gets their 5xx, and only the ledger knows they were charged for a recording
// that does not exist.
func TestARefusedStartBillsNothing(t *testing.T) {
	l := planetest.Money(t, 100000)
	app, m := billedRecord(t, l)
	m.mu.Lock()
	m.refuse = &refused{Code: "unavailable", Msg: "no available egress instances"}
	m.mu.Unlock()

	if code, _, _ := rec(t, app, http.MethodPost, roomIn(spaceA), access(t, ada)); code == http.StatusOK {
		t.Fatal("a refused start answered 200")
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a recording the media server refused to make, want 0", n)
	}
}

// TestTheRoomIsNamedOnTheWayOut as well as checked on the way back. The check is
// what makes the answer safe; the filter is what keeps this from pulling every
// recording in the deployment over the wire on every poll.
func TestTheRoomIsNamedOnTheWayOut(t *testing.T) {
	app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
	room := roomIn(spaceA)
	if code, _, body := rec(t, app, http.MethodPost, room, access(t, ada)); code != http.StatusOK {
		t.Fatalf("start = %d %q", code, body)
	}
	m.mu.Lock()
	lists := append([]map[string]any(nil), m.lists...)
	m.mu.Unlock()
	if len(lists) == 0 {
		t.Fatal("the media server was never listed, so this asserts nothing")
	}
	for i, one := range lists {
		if one["roomName"] != room {
			t.Errorf("ListEgress[%d] asked for roomName %v, want %q — an unfiltered list is every "+
				"recording in the deployment, fetched on every poll", i, one["roomName"], room)
		}
	}
}

// TestAnUnreadableEntryIsNotACleanRoom.
//
// A peer that omits room_name on a live recording used to have that entry silently
// discarded, and the room then read as free: stop answered 200 "not being recorded"
// while it kept writing, and the next start put a second recorder beside it. That is
// the two-recorder failure again, arriving through a peer-shaped surface instead of a
// race — and the 200 is the same false assurance to the same person.
//
// LiveKit populates room_name for a room-composite egress, so this is not a live
// exploit; it is the class staying closed when something between us and the media
// server does not.
func TestAnUnreadableEntryIsNotACleanRoom(t *testing.T) {
	for _, missing := range []struct {
		name string
		one  shot
	}{
		{"no room name", shot{id: "EG_1", room: "", status: "EGRESS_ACTIVE", started: firstStart}},
		{"no id", shot{id: "", room: roomIn(spaceA), status: "EGRESS_ACTIVE", started: firstStart}},
	} {
		t.Run(missing.name, func(t *testing.T) {
			app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
			m.seed(missing.one)
			room := roomIn(spaceA)

			// STOP is the one that matters: 200 here tells somebody withdrawing
			// consent that the recording is over.
			code, got, body := rec(t, app, http.MethodDelete, room, access(t, ada))
			if code == http.StatusOK {
				t.Errorf("stop answered %d %q over an entry this build could not read — "+
					"a room that cannot be shown to be free must not be reported as free", code, body)
			}
			if got.ID != "" {
				t.Errorf("stop named a recording it did not end: %q", got.ID)
			}
			// And a start must not add a second recorder to a room it cannot see.
			before := len(m.asked())
			if code, _, body := rec(t, app, http.MethodPost, room, access(t, ada)); code == http.StatusOK {
				t.Errorf("start answered %d %q over an unreadable entry: %s", code, body, "a second recorder")
			}
			for _, called := range m.asked()[before:] {
				if called == "StartRoomCompositeEgress" {
					t.Fatal("a second recorder was started on a room this build could not see the state of")
				}
			}
			// The read says so too, rather than reporting an empty room.
			if code, _, _ := rec(t, app, http.MethodGet, room, access(t, ada)); code == http.StatusOK {
				t.Errorf("read answered %d over an unreadable entry, which reads as an empty room", code)
			}
		})
	}
	// AN ABSENT STATUS IS NOT MISSING INFORMATION — it is EGRESS_STARTING.
	//
	// proto3's JSON mapping omits a field at its default value, and EgressStatus's
	// default is the FIRST state a recording is in. So a peer whose marshaler does
	// not set EmitUnpopulated sends a live, just-started recording with no status
	// field at all — on every start. Read as unjudgeable, that is this review's own
	// worst case arriving through the encoder instead of an attacker: the start is
	// refused while a worker runs, the room is then permanently unattributable, and
	// the recording can never be stopped.
	//
	// It is the same axis as the int64-carried-as-a-string one surface away. The
	// encoder's promise is not something this side can check, so both are read here.
	t.Run("a peer that omits status is sending a STARTING recording", func(t *testing.T) {
		app, m := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
		room := roomIn(spaceA)
		m.seed(shot{id: "EG_live", room: room, status: "", object: "acme/x/y.mp4", started: firstStart})

		// It is RUNNING, so a start hands it back rather than beginning a second.
		before := len(m.asked())
		code, got, body := rec(t, app, http.MethodPost, room, access(t, ada))
		if code != http.StatusOK {
			t.Fatalf("start = %d %q, want 200 with the running recording", code, body)
		}
		if got.ID != "EG_live" {
			t.Errorf("start returned %q, want the recording already running, EG_live", got.ID)
		}
		if got.Status != "EGRESS_STARTING" {
			t.Errorf("status = %q, want EGRESS_STARTING — an absent proto3 enum MEANS its zero value", got.Status)
		}
		for _, called := range m.asked()[before:] {
			if called == "StartRoomCompositeEgress" {
				t.Fatal("a second recorder was started beside a live one whose status field was omitted")
			}
		}
		// And it can be STOPPED, which is the failure that mattered: unjudgeable
		// would have made this room impossible to end.
		if code, stopped, body := rec(t, app, http.MethodDelete, room, access(t, ada)); code != http.StatusOK || stopped.ID != "EG_live" {
			t.Fatalf("stop = %d %q (id %q), want 200 ending EG_live", code, body, stopped.ID)
		}
		m.mu.Lock()
		ended := append([]string(nil), m.stopped...)
		m.mu.Unlock()
		if len(ended) != 1 || ended[0] != "EG_live" {
			t.Fatalf("StopEgress was asked to end %v, want [EG_live]", ended)
		}
	})

	// The control: a legible answer with nothing in it IS a clean room.
	t.Run("control: an empty room is still an empty room", func(t *testing.T) {
		app, _ := recordUse(t, holds(map[string]string{spaceA: token.RoleMember}))
		if code, got, body := rec(t, app, http.MethodDelete, roomIn(spaceA), access(t, ada)); code != http.StatusOK || got.ID != "" {
			t.Fatalf("stopping a genuinely empty room = %d %q, want 200 and no recording", code, body)
		}
	})
}
