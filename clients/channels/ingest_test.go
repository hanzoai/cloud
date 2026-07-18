package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ingest_test.go owns the shared package harness (mounted app, identity
// requests, door spies, seam fixtures) plus the ingress-gate behavior tests.
// ingest is driven DIRECTLY: the goroutine hop lives in
// integrations.emitIngress and is proven in clients/integrations/
// ingress_test.go, so every test here is deterministic.

// ── harness ──────────────────────────────────────────────────────────────────

// syncBuf is a mutex-guarded log sink so tests can assert never-log
// invariants (pairing codes, sender ids) without a data race.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testEnv is one mounted channels app: the zip app, captured subsystem logs,
// and the DataDir holding channels.db.
type testEnv struct {
	app     *zip.App
	logs    *syncBuf
	dataDir string
}

// newApp mounts channels exactly as apps.go does. Integrations stays
// unmounted on purpose: LinkedSubject fails soft (empty UserID),
// OrgForExternalID / ConnectionFor answer not-found — the fail-closed side
// every gate must survive.
func newApp(t *testing.T) *testEnv {
	t.Helper()
	logs := &syncBuf{}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	dataDir := t.TempDir()
	deps := cloud.Deps{Logger: luxlog.NewWriter(logs), DataDir: dataDir, Domain: "api.hanzo.ai"}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return &testEnv{app: app, logs: logs, dataDir: dataDir}
}

func (e *testEnv) store(t *testing.T) *store {
	t.Helper()
	s := mounted.Load()
	if s == nil {
		t.Fatal("channels not mounted")
	}
	return s.State.store
}

type httpResult struct {
	Code int
	Body []byte
}

// doReq issues one request with the gateway identity headers the middleware
// would mint (the integrations_test.go req idiom). org == "" sends NO
// identity — the anonymous-forge path the 403 tests need. admin adds the
// org-admin bit (X-User-IsOrgAdmin, principal.IsOrgAdmin).
func doReq(t *testing.T, e *testEnv, method, path, org string, admin bool, body any) httpResult {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	if admin {
		rq.Header.Set("X-User-IsOrgAdmin", "true")
	}
	resp, err := e.app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Body: b}
}

func req(t *testing.T, e *testEnv, method, path, org string, body any) httpResult {
	t.Helper()
	return doReq(t, e, method, path, org, false, body)
}

func reqAdmin(t *testing.T, e *testEnv, method, path, org string, body any) httpResult {
	t.Helper()
	return doReq(t, e, method, path, org, true, body)
}

func decodeJSON(t *testing.T, body []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
}

func putAllowlist(t *testing.T, e *testEnv, org string, body map[string]any) {
	t.Helper()
	res := reqAdmin(t, e, http.MethodPut, "/v1/channels/allowlist", org, body)
	if res.Code != http.StatusOK {
		t.Fatalf("PUT allowlist: %d (%s)", res.Code, res.Body)
	}
}

// ── door spies ───────────────────────────────────────────────────────────────

// doorCall is one recorded transport-door invocation.
type doorCall struct {
	org     string
	root    string
	room    string
	replyTo string
	text    string
}

// doorRec records door invocations. Mutex-guarded so recorders stay
// race-clean if a caller ever drives them from a goroutine.
type doorRec struct {
	mu    sync.Mutex
	id    string
	fail  error
	calls []doorCall
}

func (r *doorRec) hit(c doorCall) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
	return r.id, r.fail
}

func (r *doorRec) setFail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = err
}

func (r *doorRec) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *doorRec) call(t *testing.T, i int) doorCall {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.calls) {
		t.Fatalf("door call %d not recorded (have %d)", i, len(r.calls))
	}
	return r.calls[i]
}

// The four spy installers swap the package door vars for recorders and
// restore them on cleanup. ALL FOUR doors are spies in this package —
// Discord's real HTTP path is proven in clients/integrations/ingress_test.go
// (C2-4), symmetric with the other transports' existing send-path tests.

func spyTelegram(t *testing.T) *doorRec {
	t.Helper()
	rec := &doorRec{}
	saved := telegramDoor
	telegramDoor = func(_ context.Context, chatID, replyTo int64, text string) error {
		_, err := rec.hit(doorCall{room: strconv.FormatInt(chatID, 10), replyTo: strconv.FormatInt(replyTo, 10), text: text})
		return err
	}
	t.Cleanup(func() { telegramDoor = saved })
	return rec
}

func spySlack(t *testing.T) *doorRec {
	t.Helper()
	rec := &doorRec{}
	saved := slackDoor
	slackDoor = func(_ context.Context, org, channel, threadTS, text string) error {
		_, err := rec.hit(doorCall{org: org, room: channel, replyTo: threadTS, text: text})
		return err
	}
	t.Cleanup(func() { slackDoor = saved })
	return rec
}

func spyTeams(t *testing.T) *doorRec {
	t.Helper()
	rec := &doorRec{}
	saved := teamsDoor
	teamsDoor = func(_ context.Context, serviceURL, conversationID, text string) error {
		_, err := rec.hit(doorCall{root: serviceURL, room: conversationID, text: text})
		return err
	}
	t.Cleanup(func() { teamsDoor = saved })
	return rec
}

func spyDiscord(t *testing.T) *doorRec {
	t.Helper()
	rec := &doorRec{id: "m-1"}
	saved := discordDoor
	discordDoor = func(_ context.Context, channelID, replyTo, text string) (string, error) {
		return rec.hit(doorCall{room: channelID, replyTo: replyTo, text: text})
	}
	t.Cleanup(func() { discordDoor = saved })
	return rec
}

// ingressEv builds one seam event; realistic per-transport values live at the
// call sites.
func ingressEv(org, provider, externalID, user, channel, thread, text, key, replyRoot string) integrations.IngressEvent {
	return integrations.IngressEvent{
		Org:       org,
		In:        integrations.Inbound{Provider: provider, ExternalID: externalID, User: user, Channel: channel, ThreadID: thread, Text: text, DedupeKey: key},
		ReplyRoot: replyRoot,
	}
}

// ── ingest behavior ──────────────────────────────────────────────────────────

func TestIngestPairingDefault(t *testing.T) {
	e := newApp(t)
	tg := spyTelegram(t)
	sl := spySlack(t)
	ctx := context.Background()
	const org = "acme-pair"
	st := e.store(t)

	// Telegram DM from an unknown sender: pairing request minted, message dropped.
	ingest(ctx, ingressEv(org, "telegram", "hanzobot", "42", "777", "", "hi", "k1", ""))
	rows, err := listPairing(ctx, st, org, time.Now().Unix())
	if err != nil {
		t.Fatalf("listPairing: %v", err)
	}
	if len(rows) != 1 || rows[0].Channel != "telegram" || rows[0].Sender != "42" {
		t.Fatalf("pending = %+v, want one telegram/42 request", rows)
	}
	tgCode := rows[0].Code
	if len(tgCode) != pairCodeLen || tgCode != strings.ToUpper(tgCode) {
		t.Fatalf("code %q: want %d uppercase chars", tgCode, pairCodeLen)
	}
	if inbox, _ := st.listInbox(ctx, org, 0, 0); len(inbox) != 0 {
		t.Fatalf("inbox = %d rows; a pairing-gated message is never stored", len(inbox))
	}
	// Integrations is unmounted here, so the telegram chat has NO org bind and
	// C1-F1 gates even the pairing reply. In prod the bind exists by
	// construction — the adapter resolved the org from this very chat.
	if tg.count() != 0 {
		t.Fatal("telegram door must not fire for an unbound chat")
	}

	// Same sender again: the request is refreshed — same code, one row.
	ingest(ctx, ingressEv(org, "telegram", "hanzobot", "42", "777", "", "hi again", "k2", ""))
	if rows2, _ := listPairing(ctx, st, org, time.Now().Unix()); len(rows2) != 1 || rows2[0].Code != tgCode {
		t.Fatalf("refresh must keep the pending code: %+v", rows2)
	}

	// Slack DM: the pairing reply rides the door and carries the code.
	ingest(ctx, ingressEv(org, "slack", "T024ABC", "u1", "D024BE91L", "", "hello", "s1", ""))
	var slCode string
	rows3, _ := listPairing(ctx, st, org, time.Now().Unix())
	for _, r := range rows3 {
		if r.Channel == "slack" && r.Sender == "u1" {
			slCode = r.Code
		}
	}
	if slCode == "" {
		t.Fatalf("slack pairing request missing: %+v", rows3)
	}
	if sl.count() != 1 {
		t.Fatalf("slack pairing reply: %d door calls, want 1", sl.count())
	}
	reply := sl.call(t, 0)
	if reply.org != org || reply.room != "D024BE91L" || !strings.Contains(reply.text, slCode) {
		t.Fatalf("pairing reply = %+v, want the code delivered to the DM", reply)
	}

	// Reply throttle: a refresh (created=false) sends nothing.
	ingest(ctx, ingressEv(org, "slack", "T024ABC", "u1", "D024BE91L", "", "again", "s2", ""))
	if sl.count() != 1 {
		t.Fatal("refreshed pairing must not re-send the code")
	}
	if inbox, _ := st.listInbox(ctx, org, 0, 0); len(inbox) != 0 {
		t.Fatal("pairing-gated messages never reach the inbox")
	}

	// Codes are bearer capabilities: admin surface only, never logs.
	logs := e.logs.String()
	for _, c := range []string{tgCode, slCode} {
		if strings.Contains(logs, c) {
			t.Fatalf("pairing code %q leaked into logs", c)
		}
	}
}

func TestIngestApproveRoundtrip(t *testing.T) {
	e := newApp(t)
	_ = spyTelegram(t) // absorb the (bind-gated) pairing reply attempt
	ctx := context.Background()
	const org = "acme-approve"

	ingest(ctx, ingressEv(org, "telegram", "hanzobot", "42", "777", "", "hi", "k1", ""))

	res := reqAdmin(t, e, http.MethodGet, "/v1/channels/pairing", org, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("pairing list: %d (%s)", res.Code, res.Body)
	}
	var pending struct {
		Pending []struct {
			Channel string `json:"channel"`
			Sender  string `json:"sender"`
			Code    string `json:"code"`
		} `json:"pending"`
	}
	decodeJSON(t, res.Body, &pending)
	if len(pending.Pending) != 1 || pending.Pending[0].Sender != "42" {
		t.Fatalf("pending = %s", res.Body)
	}
	code := pending.Pending[0].Code

	// Plain members may read; anonymous callers may not.
	if r := req(t, e, http.MethodGet, "/v1/channels/pairing", org, nil); r.Code != http.StatusOK {
		t.Fatalf("member pairing read: %d", r.Code)
	}
	if r := req(t, e, http.MethodGet, "/v1/channels/pairing", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("anonymous pairing read: %d, want 403", r.Code)
	}

	// Approval is admin-gated.
	approveBody := map[string]any{"channel": "telegram", "code": code}
	if r := req(t, e, http.MethodPost, "/v1/channels/pairing/approve", org, approveBody); r.Code != http.StatusForbidden {
		t.Fatalf("non-admin approve: %d, want 403", r.Code)
	}
	res = reqAdmin(t, e, http.MethodPost, "/v1/channels/pairing/approve", org, approveBody)
	if res.Code != http.StatusOK {
		t.Fatalf("approve: %d (%s)", res.Code, res.Body)
	}
	var approved struct {
		Sender            string `json:"sender"`
		OwnerBootstrapped bool   `json:"ownerBootstrapped"`
	}
	decodeJSON(t, res.Body, &approved)
	if approved.Sender != "42" || !approved.OwnerBootstrapped {
		t.Fatalf("approve = %+v, want sender 42 + first-approval owner bootstrap", approved)
	}
	// A consumed code is gone.
	if r := reqAdmin(t, e, http.MethodPost, "/v1/channels/pairing/approve", org, approveBody); r.Code != http.StatusNotFound {
		t.Fatalf("re-approve: %d, want 404", r.Code)
	}

	// The paired sender's next message lands in the inbox.
	ingest(ctx, ingressEv(org, "telegram", "hanzobot", "42", "777", "", "hello", "k2", ""))
	res = req(t, e, http.MethodGet, "/v1/channels/inbox", org, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("inbox: %d (%s)", res.Code, res.Body)
	}
	var inbox struct {
		Messages []struct {
			ID         int64  `json:"id"`
			Channel    string `json:"channel"`
			RoomID     string `json:"roomId"`
			RoomKind   string `json:"roomKind"`
			Sender     string `json:"sender"`
			SenderUser string `json:"senderUser"`
			Text       string `json:"text"`
		} `json:"messages"`
		Cursor int64 `json:"cursor"`
	}
	decodeJSON(t, res.Body, &inbox)
	if len(inbox.Messages) != 1 {
		t.Fatalf("inbox = %s", res.Body)
	}
	m := inbox.Messages[0]
	if m.Channel != "telegram" || m.RoomID != "777" || m.RoomKind != "dm" || m.Sender != "42" || m.Text != "hello" {
		t.Fatalf("inbox row = %+v", m)
	}
	if m.SenderUser != "" {
		t.Fatalf("senderUser = %q; unlinked identity (integrations unmounted) must stay empty", m.SenderUser)
	}
	if inbox.Cursor != m.ID {
		t.Fatalf("cursor = %d, want the last row id %d", inbox.Cursor, m.ID)
	}

	// The cursor excludes what was read.
	res = req(t, e, http.MethodGet, "/v1/channels/inbox?since="+strconv.FormatInt(inbox.Cursor, 10), org, nil)
	var page2 struct {
		Messages []json.RawMessage `json:"messages"`
		Cursor   int64             `json:"cursor"`
	}
	decodeJSON(t, res.Body, &page2)
	if len(page2.Messages) != 0 || page2.Cursor != inbox.Cursor {
		t.Fatalf("since-cursor page = %s", res.Body)
	}
}

func TestIngestAllowlistMode(t *testing.T) {
	e := newApp(t)
	_ = spyTelegram(t)
	ctx := context.Background()
	const org = "acme-allow"
	st := e.store(t)

	putAllowlist(t, e, org, map[string]any{"channel": "telegram", "dmPolicy": "allowlist", "dm": []string{"42"}})

	ingest(ctx, ingressEv(org, "telegram", "hanzobot", "42", "700", "", "yo", "a1", ""))
	if rows, _ := st.listInbox(ctx, org, 0, 0); len(rows) != 1 {
		t.Fatalf("allowlisted sender: %d inbox rows, want 1", len(rows))
	}

	// A stranger is dropped silently: no inbox row AND no pairing request —
	// pairing-source grants exist only under the pairing policy.
	ingest(ctx, ingressEv(org, "telegram", "hanzobot", "u-sneak", "701", "", "let me in", "a2", ""))
	if rows, _ := st.listInbox(ctx, org, 0, 0); len(rows) != 1 {
		t.Fatal("blocked sender reached the inbox")
	}
	if pend, _ := listPairing(ctx, st, org, time.Now().Unix()); len(pend) != 0 {
		t.Fatal("allowlist policy must not mint pairing requests")
	}
	// Blocked drops log the closed reason code only — never sender ids.
	logs := e.logs.String()
	if !strings.Contains(logs, string(dmNotAllowlisted)) {
		t.Fatal("blocked drop must log its reason code")
	}
	if strings.Contains(logs, "u-sneak") {
		t.Fatal("sender ids must never appear in logs")
	}
}

func TestIngestOpenMode(t *testing.T) {
	e := newApp(t)
	_ = spyTelegram(t)
	ctx := context.Background()
	const org = "acme-open"
	st := e.store(t)

	// Open without `*` (or an explicit entry) admits nobody — and never pairs.
	putAllowlist(t, e, org, map[string]any{"channel": "telegram", "dmPolicy": "open"})
	ingest(ctx, ingressEv(org, "telegram", "hanzobot", "50", "500", "", "x", "o1", ""))
	if rows, _ := st.listInbox(ctx, org, 0, 0); len(rows) != 0 {
		t.Fatal("open without a wildcard must block")
	}
	if pend, _ := listPairing(ctx, st, org, time.Now().Unix()); len(pend) != 0 {
		t.Fatal("open policy must not mint pairing requests")
	}

	putAllowlist(t, e, org, map[string]any{"channel": "telegram", "dmPolicy": "open", "dm": []string{"*"}})
	ingest(ctx, ingressEv(org, "telegram", "hanzobot", "50", "500", "", "x", "o2", ""))
	if rows, _ := st.listInbox(ctx, org, 0, 0); len(rows) != 1 {
		t.Fatal("open + wildcard must admit")
	}
}

func TestIngestGroupPolicy(t *testing.T) {
	e := newApp(t)
	sl := spySlack(t)
	ctx := context.Background()
	const org = "acme-group"
	st := e.store(t)
	group := func(key, text string) integrations.IngressEvent {
		return ingressEv(org, "slack", "T024ABC", "u9", "C024BE91L", "", text, key, "")
	}

	// Default groupPolicy=open: a group message is stored.
	ingest(ctx, group("g1", "hi"))
	rows, _ := st.listInbox(ctx, org, 0, 0)
	if len(rows) != 1 || rows[0].RoomKind != RoomGroup {
		t.Fatalf("rows = %+v, want one group row", rows)
	}

	putAllowlist(t, e, org, map[string]any{"channel": "slack", "groupPolicy": "disabled"})
	ingest(ctx, group("g2", "anyone?"))
	if rows, _ := st.listInbox(ctx, org, 0, 0); len(rows) != 1 {
		t.Fatal("disabled group surface must drop")
	}

	putAllowlist(t, e, org, map[string]any{"channel": "slack", "groupPolicy": "allowlist"})
	ingest(ctx, group("g3", "empty allowlist"))
	if rows, _ := st.listInbox(ctx, org, 0, 0); len(rows) != 1 {
		t.Fatal("an empty group allowlist admits nobody")
	}

	// Pair + approve the same sender over DM…
	ingest(ctx, ingressEv(org, "slack", "T024ABC", "u9", "D9", "", "pair me", "d1", ""))
	if sl.count() != 1 {
		t.Fatalf("pairing reply calls = %d, want 1", sl.count())
	}
	pend, _ := listPairing(ctx, st, org, time.Now().Unix())
	if len(pend) != 1 {
		t.Fatalf("pending = %+v", pend)
	}
	res := reqAdmin(t, e, http.MethodPost, "/v1/channels/pairing/approve", org,
		map[string]any{"channel": "slack", "code": pend[0].Code})
	if res.Code != http.StatusOK {
		t.Fatalf("approve: %d (%s)", res.Code, res.Body)
	}
	ingest(ctx, ingressEv(org, "slack", "T024ABC", "u9", "D9", "", "dm ok", "d2", ""))
	if rows, _ := st.listInbox(ctx, org, 0, 0); len(rows) != 2 {
		t.Fatal("approved sender's DM must land in the inbox")
	}
	// …and the DM approval STILL does not open the group allowlist.
	ingest(ctx, group("g4", "group again"))
	if rows, _ := st.listInbox(ctx, org, 0, 0); len(rows) != 2 {
		t.Fatal("a DM pairing approval must never grant group access")
	}
}

func TestIngestRouteAfterGate(t *testing.T) {
	e := newApp(t)
	tm := spyTeams(t)
	ctx := context.Background()
	const org = "acme-teams"
	st := e.store(t)
	const conv = "19:abc@thread.tacv2"
	const root = "https://smba.example/amer/"

	// C1-F4: a blocked sender mints NO route — route presence is a send
	// capability.
	putAllowlist(t, e, org, map[string]any{"channel": "teams", "groupPolicy": "disabled"})
	ingest(ctx, ingressEv(org, "teams", "tenant-1", "u1", conv, "", "hi", "t1", root))
	if _, ok, _ := st.routeFor(ctx, org, "teams", conv); ok {
		t.Fatal("blocked inbound must not mint a route")
	}
	if tm.count() != 0 {
		t.Fatal("no door traffic for a blocked event")
	}

	// Allowed inbound stores the JWT-verified reply root; egress rides it.
	putAllowlist(t, e, org, map[string]any{"channel": "teams", "groupPolicy": "open"})
	ingest(ctx, ingressEv(org, "teams", "tenant-1", "u1", conv, "", "hi again", "t2", root))
	got, ok, err := st.routeFor(ctx, org, "teams", conv)
	if err != nil || !ok || got != root {
		t.Fatalf("route = %q ok=%v err=%v, want the stored reply root", got, ok, err)
	}
	res := req(t, e, http.MethodPost, "/v1/channels/teams/send", org,
		map[string]any{"room": map[string]any{"id": conv}, "text": "reply"})
	if res.Code != http.StatusOK {
		t.Fatalf("send: %d (%s)", res.Code, res.Body)
	}
	sent := tm.call(t, 0)
	if sent.root != root || sent.room != conv {
		t.Fatalf("send rode %+v, want the learned root", sent)
	}

	// The PAIR branch mints the route too — the pairing reply must be able to
	// ride the teams door.
	const dmConv = "a:1a2b"
	const root2 = "https://smba.example/emea/"
	ingest(ctx, ingressEv(org, "teams", "tenant-1", "u7", dmConv, "", "hello", "t3", root2))
	got2, ok2, _ := st.routeFor(ctx, org, "teams", dmConv)
	if !ok2 || got2 != root2 {
		t.Fatalf("pair-branch route = %q ok=%v, want %q", got2, ok2, root2)
	}
	if tm.count() != 2 {
		t.Fatalf("door calls = %d, want the pairing reply as the 2nd", tm.count())
	}
	reply := tm.call(t, 1)
	if reply.root != root2 || reply.room != dmConv || !strings.Contains(reply.text, "Pairing code: ") {
		t.Fatalf("pairing reply = %+v", reply)
	}
}

func TestIngestEventKeyIdempotent(t *testing.T) {
	e := newApp(t)
	_ = spyTelegram(t)
	ctx := context.Background()
	const org = "acme-dup"
	st := e.store(t)

	putAllowlist(t, e, org, map[string]any{"channel": "telegram", "dmPolicy": "open", "dm": []string{"*"}})
	ev := ingressEv(org, "telegram", "hanzobot", "9", "900", "", "same", "dup-1", "")
	ingest(ctx, ev)
	ingest(ctx, ev)
	if rows, _ := st.listInbox(ctx, org, 0, 0); len(rows) != 1 {
		t.Fatalf("redelivered event key stored %d rows, want 1", len(rows))
	}
}

func TestIngestOrgIsolation(t *testing.T) {
	e := newApp(t)
	_ = spyTelegram(t)
	_ = spySlack(t)
	ctx := context.Background()
	const orgA = "acme-iso-a"
	const orgB = "acme-iso-b"
	st := e.store(t)

	putAllowlist(t, e, orgA, map[string]any{"channel": "telegram", "dmPolicy": "open", "dm": []string{"*"}})
	ingest(ctx, ingressEv(orgA, "telegram", "hanzobot", "1", "100", "", "secret-a", "i1", ""))
	ingest(ctx, ingressEv(orgA, "slack", "T1", "u1", "D1", "", "pair", "i2", ""))

	// Org B sees neither A's inbox nor A's pending pairings.
	res := req(t, e, http.MethodGet, "/v1/channels/inbox", orgB, nil)
	var inbox struct {
		Messages []json.RawMessage `json:"messages"`
	}
	decodeJSON(t, res.Body, &inbox)
	if len(inbox.Messages) != 0 {
		t.Fatalf("org-b inbox = %s, want empty", res.Body)
	}
	res = reqAdmin(t, e, http.MethodGet, "/v1/channels/pairing", orgB, nil)
	var pending struct {
		Pending []json.RawMessage `json:"pending"`
	}
	decodeJSON(t, res.Body, &pending)
	if len(pending.Pending) != 0 {
		t.Fatalf("org-b pending = %s, want empty", res.Body)
	}
	// And A's open policy does not leak into B: the same sender pairs there.
	ingest(ctx, ingressEv(orgB, "telegram", "hanzobot", "1", "100", "", "hi", "i3", ""))
	if rows, _ := st.listInbox(ctx, orgB, 0, 0); len(rows) != 0 {
		t.Fatal("org-b must keep the strict pairing default")
	}
	if pend, _ := listPairing(ctx, st, orgB, time.Now().Unix()); len(pend) != 1 {
		t.Fatalf("org-b pending = %+v, want its own pairing request", pend)
	}
}

func TestIngestDiscordRoute(t *testing.T) {
	e := newApp(t)
	dc := spyDiscord(t)
	ctx := context.Background()
	const org = "acme-disc"
	st := e.store(t)

	// Allowed guild inbound (groupPolicy default open) stores the message AND
	// mints the discord route — presence with reply_root '' IS the send
	// capability (C1-F1).
	ingest(ctx, ingressEv(org, "discord", "GUILD9", "u5", "c-99", "", "hi", "dk1", ""))
	rows, _ := st.listInbox(ctx, org, 0, 0)
	if len(rows) != 1 || rows[0].RoomKind != RoomGroup || rows[0].Account != "guild9" {
		t.Fatalf("rows = %+v", rows)
	}
	root, ok, err := st.routeFor(ctx, org, "discord", "c-99")
	if err != nil || !ok || root != "" {
		t.Fatalf("route = %q ok=%v err=%v, want present with empty root", root, ok, err)
	}
	res := req(t, e, http.MethodPost, "/v1/channels/discord/send", org,
		map[string]any{"room": map[string]any{"id": "c-99"}, "text": "pong"})
	if res.Code != http.StatusOK {
		t.Fatalf("send: %d (%s)", res.Code, res.Body)
	}
	var d Delivery
	decodeJSON(t, res.Body, &d)
	if d.MessageID != "m-1" {
		t.Fatalf("delivery = %+v, want the door's message id", d)
	}
	if dc.count() != 1 {
		t.Fatalf("door calls = %d, want 1", dc.count())
	}
	if got := dc.call(t, 0); got.room != "c-99" {
		t.Fatalf("door call = %+v, want room c-99", got)
	}
}
