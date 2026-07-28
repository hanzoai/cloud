package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// send_test.go proves the egress fan-out through the real HTTP surface using
// the ingest_test.go harness. Zero live network: all four doors are spies —
// Discord's real HTTP path is proven in clients/integrations/ingress_test.go
// (C2-4) — so what is under test here is the route surface, the C1-F1 target
// bindings, and the idempotency ledger.

func TestSendSlack(t *testing.T) {
	e := newApp(t)
	sl := spySlack(t)

	res := req(t, e, http.MethodPost, "/v1/channels/slack/send", "acme",
		map[string]any{"room": map[string]any{"id": "C1"}, "replyTo": "171.2", "text": "hi"})
	if res.Code != http.StatusOK {
		t.Fatalf("send: %d (%s)", res.Code, res.Body)
	}
	var d Delivery
	decodeJSON(t, res.Body, &d)
	if d.Timestamp <= 0 {
		t.Fatalf("delivery = %+v, want a send timestamp", d)
	}
	if sl.count() != 1 {
		t.Fatalf("door calls = %d, want 1", sl.count())
	}
	call := sl.call(t, 0)
	// The caller's org rides to the door — SendSlack's per-org TokenFor IS the
	// slack tenancy gate.
	if call.org != "acme" || call.room != "C1" || call.replyTo != "171.2" || call.text != "hi" {
		t.Fatalf("door call = %+v", call)
	}
}

func TestSendDiscordRouteCapability(t *testing.T) {
	e := newApp(t)
	dc := spyDiscord(t)
	ctx := context.Background()
	st := e.store(t)
	body := map[string]any{"room": map[string]any{"id": "999"}, "text": "x"}

	// No inbound-learned route ⇒ 409, and the door is never consulted.
	if r := req(t, e, http.MethodPost, "/v1/channels/discord/send", "acme", body); r.Code != http.StatusConflict {
		t.Fatalf("routeless send: %d, want 409", r.Code)
	}
	if dc.count() != 0 {
		t.Fatal("binding gate must precede the door")
	}

	if err := st.upsertRoute(ctx, "acme", "discord", "999", "", time.Now().Unix()); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	res := req(t, e, http.MethodPost, "/v1/channels/discord/send", "acme", body)
	if res.Code != http.StatusOK {
		t.Fatalf("send: %d (%s)", res.Code, res.Body)
	}
	var d Delivery
	decodeJSON(t, res.Body, &d)
	if d.MessageID != "m-1" || dc.count() != 1 {
		t.Fatalf("delivery = %+v after %d door calls", d, dc.count())
	}

	// C1-F1 tenancy: another org holds no route for the same room.
	if r := req(t, e, http.MethodPost, "/v1/channels/discord/send", "beta", body); r.Code != http.StatusConflict {
		t.Fatalf("cross-org send: %d, want 409", r.Code)
	}
	if dc.count() != 1 {
		t.Fatal("a foreign org must never reach the door")
	}
}

func TestSendTeamsRouteCapability(t *testing.T) {
	e := newApp(t)
	tm := spyTeams(t)
	ctx := context.Background()
	st := e.store(t)
	const conv = "19:x@thread.tacv2"
	const root = "https://smba.example/amer/"
	body := map[string]any{"room": map[string]any{"id": conv}, "text": "x"}

	if r := req(t, e, http.MethodPost, "/v1/channels/teams/send", "acme", body); r.Code != http.StatusConflict {
		t.Fatalf("routeless send: %d, want 409", r.Code)
	}
	if tm.count() != 0 {
		t.Fatal("binding gate must precede the door")
	}

	if err := st.upsertRoute(ctx, "acme", "teams", conv, root, time.Now().Unix()); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	if r := req(t, e, http.MethodPost, "/v1/channels/teams/send", "acme", body); r.Code != http.StatusOK {
		t.Fatalf("send: %d (%s)", r.Code, r.Body)
	}
	call := tm.call(t, 0)
	if call.root != root || call.room != conv {
		t.Fatalf("door call = %+v, want the learned serviceURL", call)
	}

	if r := req(t, e, http.MethodPost, "/v1/channels/teams/send", "beta", body); r.Code != http.StatusConflict {
		t.Fatalf("cross-org send: %d, want 409", r.Code)
	}
	if tm.count() != 1 {
		t.Fatal("a foreign org must never reach the door")
	}
}

func TestSendTelegramBinding(t *testing.T) {
	e := newApp(t)
	tg := spyTelegram(t)

	// The telegram bind lives in integrations (OrgForExternalID) and cannot be
	// seeded from this package — unbound is exactly what an org that never
	// onboarded telegram looks like, and it must 403 with the door untouched.
	res := req(t, e, http.MethodPost, "/v1/channels/telegram/send", "acme",
		map[string]any{"room": map[string]any{"id": "777"}, "text": "x"})
	if res.Code != http.StatusForbidden {
		t.Fatalf("unbound send: %d, want 403", res.Code)
	}
	if tg.count() != 0 {
		t.Fatal("binding gate must precede the door")
	}

	// Unit-level: the typed refusal, and the gate ordering, are explicit.
	s := mounted.Load()
	if s == nil {
		t.Fatal("channels not mounted")
	}
	_, err := telegramEgress(context.Background(), s, "acme", Message{Channel: "telegram", Room: Room{ID: "777"}, Text: "x"})
	if !errors.Is(err, errRoomNotBound) {
		t.Fatalf("err = %v, want errRoomNotBound", err)
	}
	if tg.count() != 0 {
		t.Fatal("errRoomNotBound must fire before the door")
	}
}

func TestSendAuthValidation(t *testing.T) {
	e := newApp(t)
	sl := spySlack(t)
	ok := map[string]any{"room": map[string]any{"id": "C1"}, "text": "x"}

	if r := req(t, e, http.MethodPost, "/v1/channels/slack/send", "", ok); r.Code != http.StatusForbidden {
		t.Fatalf("anonymous send: %d, want 403", r.Code)
	}
	if r := req(t, e, http.MethodPost, "/v1/channels/bogus/send", "acme", ok); r.Code != http.StatusNotFound {
		t.Fatalf("unknown channel: %d, want 404", r.Code)
	}
	bad := []map[string]any{
		{"room": map[string]any{"id": ""}, "text": "x"},                                                  // room required
		{"room": map[string]any{"id": "C1"}},                                                             // content required
		{"room": map[string]any{"id": "C1"}, "text": "x", "actions": []map[string]any{{"kind": "menu"}}}, // closed action set
		{"room": map[string]any{"id": "C1", "kind": "castle"}, "text": "x"},                              // closed room kinds
		// C2-6: identity fields are not decodable on the egress body — they are
		// rejected loudly, never silently dropped.
		{"room": map[string]any{"id": "C1"}, "text": "x", "sender": map[string]any{"externalId": "evil"}},
		{"room": map[string]any{"id": "C1"}, "text": "x", "account": "spoof"},
		{"room": map[string]any{"id": "C1"}, "text": "x", "channel": "slack"},
	}
	for i, b := range bad {
		if r := req(t, e, http.MethodPost, "/v1/channels/slack/send", "acme", b); r.Code != http.StatusBadRequest {
			t.Fatalf("bad body %d: %d, want 400 (%s)", i, r.Code, r.Body)
		}
	}
	if sl.count() != 0 {
		t.Fatalf("no rejected request may reach a door (%d calls)", sl.count())
	}
}

func TestChannelsList(t *testing.T) {
	e := newApp(t)

	res := req(t, e, http.MethodGet, "/v1/channels", "acme", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", res.Code, res.Body)
	}
	var typed struct {
		Channels []struct {
			ID          string `json:"id"`
			Connected   bool   `json:"connected"`
			DMPolicy    string `json:"dmPolicy"`
			GroupPolicy string `json:"groupPolicy"`
		} `json:"channels"`
	}
	decodeJSON(t, res.Body, &typed)
	want := []string{"discord", "slack", "teams", "telegram"}
	if len(typed.Channels) != len(want) {
		t.Fatalf("channels = %s, want the closed registry", res.Body)
	}
	for i, ch := range typed.Channels {
		if ch.ID != want[i] {
			t.Fatalf("channel[%d] = %q, want %q (fixed alphabetical order)", i, ch.ID, want[i])
		}
		if ch.Connected {
			t.Fatalf("%s: connected must be false with integrations unmounted", ch.ID)
		}
		if ch.DMPolicy != string(DMPairing) || ch.GroupPolicy != string(GroupOpen) {
			t.Fatalf("%s policy = %s/%s, want the pairing/open defaults", ch.ID, ch.DMPolicy, ch.GroupPolicy)
		}
	}
	// C2-7: account (id-shaped fact) and accountLabel (human label) are both
	// present on every entry — one meaning per field, on every surface.
	var raw struct {
		Channels []map[string]json.RawMessage `json:"channels"`
	}
	decodeJSON(t, res.Body, &raw)
	for i, ch := range raw.Channels {
		for _, key := range []string{"account", "accountLabel", "capabilities", "pendingPairing"} {
			if _, ok := ch[key]; !ok {
				t.Fatalf("channel[%d] missing %q", i, key)
			}
		}
	}
	if r := req(t, e, http.MethodGet, "/v1/channels", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("anonymous list: %d, want 403", r.Code)
	}
}

func TestSendIdempotency(t *testing.T) {
	e := newApp(t)
	dc := spyDiscord(t)
	ctx := context.Background()
	st := e.store(t)
	if err := st.upsertRoute(ctx, "acme", "discord", "999", "", time.Now().Unix()); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	body := map[string]any{"room": map[string]any{"id": "999"}, "text": "x", "idempotency": "idem-1"}

	res := req(t, e, http.MethodPost, "/v1/channels/discord/send", "acme", body)
	if res.Code != http.StatusOK {
		t.Fatalf("send: %d (%s)", res.Code, res.Body)
	}
	var first Delivery
	decodeJSON(t, res.Body, &first)
	if first.MessageID != "m-1" || dc.count() != 1 {
		t.Fatalf("first = %+v after %d calls", first, dc.count())
	}

	// Same key replays the stored receipt without a second transport send.
	res = req(t, e, http.MethodPost, "/v1/channels/discord/send", "acme", body)
	if res.Code != http.StatusOK {
		t.Fatalf("replay: %d (%s)", res.Code, res.Body)
	}
	var replay Delivery
	decodeJSON(t, res.Body, &replay)
	if replay.MessageID != "m-1" || replay.Timestamp <= 0 {
		t.Fatalf("replay = %+v, want the stored receipt", replay)
	}
	if dc.count() != 1 {
		t.Fatalf("door calls = %d; a replay must not re-send", dc.count())
	}

	// A different key is a different send.
	body["idempotency"] = "idem-2"
	if r := req(t, e, http.MethodPost, "/v1/channels/discord/send", "acme", body); r.Code != http.StatusOK {
		t.Fatalf("second key: %d", r.Code)
	}
	if dc.count() != 2 {
		t.Fatalf("door calls = %d, want 2", dc.count())
	}
}

func TestSendRetryAfterFailure(t *testing.T) {
	e := newApp(t)
	dc := spyDiscord(t)
	ctx := context.Background()
	st := e.store(t)
	if err := st.upsertRoute(ctx, "acme", "discord", "999", "", time.Now().Unix()); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	body := map[string]any{"room": map[string]any{"id": "999"}, "text": "x", "idempotency": "k-r"}

	// C2-1: a transport failure releases the claimed key in the same error
	// path — a failed send must not poison the retention window.
	dc.setFail(errors.New("gateway sad"))
	if r := req(t, e, http.MethodPost, "/v1/channels/discord/send", "acme", body); r.Code != http.StatusBadGateway {
		t.Fatalf("failed send: %d, want 502 (%s)", r.Code, r.Body)
	}
	if dc.count() != 1 {
		t.Fatalf("door calls = %d", dc.count())
	}
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_send
  WHERE org='acme' AND channel='discord' AND idempotency='k-r'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("claimed key rows = %d err=%v, want released", n, err)
	}

	// The same key re-attempts and succeeds…
	dc.setFail(nil)
	if r := req(t, e, http.MethodPost, "/v1/channels/discord/send", "acme", body); r.Code != http.StatusOK {
		t.Fatalf("retry: %d", r.Code)
	}
	if dc.count() != 2 {
		t.Fatalf("door calls = %d, want the retry to re-send", dc.count())
	}
	// …and only the COMPLETED send replays.
	if r := req(t, e, http.MethodPost, "/v1/channels/discord/send", "acme", body); r.Code != http.StatusOK {
		t.Fatalf("replay: %d", r.Code)
	}
	if dc.count() != 2 {
		t.Fatalf("door calls = %d; the completed send must replay", dc.count())
	}
}

func TestSendNoSecretsAtRest(t *testing.T) {
	// Plant a token in the transport env: channels must never read, store, or
	// log it — custody stays in integrations, spies own the doors here.
	const planted = "tok-discord-secret"
	t.Setenv("DISCORD_BOT_TOKEN", planted)
	e := newApp(t)
	dc := spyDiscord(t)
	sl := spySlack(t)
	ctx := context.Background()
	st := e.store(t)
	if err := st.upsertRoute(ctx, "acme", "discord", "999", "", time.Now().Unix()); err != nil {
		t.Fatalf("seed route: %v", err)
	}

	// Exercise the surfaces that persist state: a plain send, a failed
	// idempotent send, its retry, and a replay.
	if r := req(t, e, http.MethodPost, "/v1/channels/slack/send", "acme",
		map[string]any{"room": map[string]any{"id": "C1"}, "text": "hi"}); r.Code != http.StatusOK {
		t.Fatalf("slack send: %d", r.Code)
	}
	body := map[string]any{"room": map[string]any{"id": "999"}, "text": "x", "idempotency": "k-s"}
	dc.setFail(errors.New("boom"))
	if r := req(t, e, http.MethodPost, "/v1/channels/discord/send", "acme", body); r.Code != http.StatusBadGateway {
		t.Fatalf("failed send: %d", r.Code)
	}
	dc.setFail(nil)
	for range 2 {
		if r := req(t, e, http.MethodPost, "/v1/channels/discord/send", "acme", body); r.Code != http.StatusOK {
			t.Fatalf("send: %d", r.Code)
		}
	}
	// Discord door: the failed attempt plus the retry; the final POST replays.
	if sl.count() != 1 || dc.count() != 2 {
		t.Fatalf("door calls slack=%d discord=%d, want 1/2", sl.count(), dc.count())
	}

	// The token bytes appear nowhere: not in any store file (channels.db plus
	// its WAL sidecars), not in a log line.
	entries, err := os.ReadDir(e.dataDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(e.dataDir, entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", entry.Name(), err)
		}
		if bytes.Contains(data, []byte(planted)) {
			t.Fatalf("token bytes found in %s", entry.Name())
		}
	}
	if strings.Contains(e.logs.String(), planted) {
		t.Fatal("token bytes found in logs")
	}
}
