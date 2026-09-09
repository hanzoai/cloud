package bot

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fasthttp/websocket"
)

// A person's notification defaults follow them to every browser and are stored
// in the org's own file: the bot a connection happens to be bound to says
// nothing about where they live (Call.OrgStore). So a change made in one browser
// is news to every browser that person has open, whichever partition each one
// bound to — and a change nobody hears about is a screen showing settings that
// are no longer stored anywhere.

// pushBrowser registers one browser for push and starts it hearing about the
// person's defaults, which is what reading the preferences does.
func pushBrowser(t *testing.T, ws *websocket.Conn, id, endpoint string) {
	t.Helper()
	// A real P-256 key, because the subscription is what a notification is
	// encrypted to and one this cloud cannot encrypt to is refused at once.
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("browser key: %v", err)
	}
	p256dh := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	auth := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	frame := reply(t, ws, id+":sub", "push.web.subscribe",
		`{"endpoint":"`+endpoint+`","keys":{"p256dh":"`+p256dh+`","auth":"`+auth+`"}}`)
	if frame["ok"] != true {
		t.Fatalf("push.web.subscribe refused: %v", frame)
	}
	if frame = reply(t, ws, id+":get", "push.web.preferences.get",
		`{"endpoint":"`+endpoint+`"}`); frame["ok"] != true {
		t.Fatalf("push.web.preferences.get refused: %v", frame)
	}
}

func TestPrefsChangedReachesEveryBrowserOfThePerson(t *testing.T) {
	url := serve(t)
	announceAt(t, url, "acme", "bot_aaaaaaaa")
	plain := dialAs(t, url, "acme", "op@acme", "", false)
	bound := dialAs(t, url, "acme", "op@acme", "bot_aaaaaaaa", false)

	pushBrowser(t, plain, "1", "https://push.example.com/plain")
	pushBrowser(t, bound, "2", "https://push.example.com/bound")

	seen := listen(bound)
	frame := reply(t, plain, "3:set", "push.web.preferences.set",
		`{"endpoint":"https://push.example.com/plain","scope":"user","preferences":{`+
			`"categories":{"approvalRequested":true,"agentFinished":true,"agentQuestion":true,`+
			`"humanMentioned":true,"scheduledTaskFailed":true,"backgroundTaskFailed":true},`+
			`"detailLevel":"private",`+
			`"quietHours":{"enabled":false,"startMinute":0,"endMinute":0,"timeZone":"UTC"},`+
			`"agentIds":[]}}`)
	if frame["ok"] != true {
		t.Fatalf("push.web.preferences.set refused: %v", frame)
	}

	// A marker every connection of the org hears, raised after the change.
	PublishOrg("acme", "", "probe.moved", map[string]any{"key": "marker"})
	if got := until(t, seen, "probe.moved"); !slices.Contains(got, "users.prefs.changed") {
		t.Errorf("the person's other browser was not told their defaults changed: %v", got)
	}
}

// ---- delivery ----

// browserSub mints a subscription the test holds the browser key for, so what
// pushDeliver sends can be read the way the browser that registered it would.
func browserSub(t *testing.T, endpoint string) (pushSub, *ecdh.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("browser key: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("auth secret: %v", err)
	}
	return pushSub{
		ID:       "sub_" + pushID(endpoint)[:8],
		User:     "op@acme",
		Endpoint: endpoint,
		P256dh:   pushB64(key.PublicKey().Bytes()),
		Auth:     pushB64(auth),
	}, key, auth
}

// browserOpen is the receiving half of RFC 8291 over the aes128gcm content
// encoding of RFC 8188: it derives the same key from the record's own header
// and returns the bytes a service worker would be handed.
func browserOpen(t *testing.T, browser *ecdh.PrivateKey, auth, body []byte) []byte {
	t.Helper()
	if len(body) < 22 {
		t.Fatalf("record is %d bytes, too short to carry a header", len(body))
	}
	salt, rs, idLen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != pushRecord {
		t.Errorf("record size %d, want %d", rs, pushRecord)
	}
	server, err := ecdh.P256().NewPublicKey(body[21 : 21+idLen])
	if err != nil {
		t.Fatalf("the record does not carry a P-256 key: %v", err)
	}
	shared, err := browser.ECDH(server)
	if err != nil {
		t.Fatalf("ecdh: %v", err)
	}
	prk, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	info := append([]byte("WebPush: info\x00"), browser.PublicKey().Bytes()...)
	info = append(info, server.Bytes()...)
	ikm, err := hkdf.Expand(sha256.New, prk, string(info), 32)
	if err != nil {
		t.Fatalf("expand ikm: %v", err)
	}
	if prk, err = hkdf.Extract(sha256.New, ikm, salt); err != nil {
		t.Fatalf("extract salted: %v", err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatalf("expand cek: %v", err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatalf("expand nonce: %v", err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	plain, err := gcm.Open(nil, nonce, body[21+idLen:], nil)
	if err != nil {
		t.Fatalf("the browser could not decrypt what was sent it: %v", err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatalf("the record does not end in the last-record delimiter")
	}
	return plain[:len(plain)-1]
}

// A notification leaves as ciphertext only the browser that registered can
// read, under an assertion signed by the key that browser bound itself to.
// A push service checks both before it will carry anything, so a delivery that
// never reaches the encryption is a notification nobody gets.
func TestPushDeliverEncryptsToTheBrowser(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gateway key: %v", err)
	}

	var seen *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	sub, browser, auth := browserSub(t, srv.URL)
	payload := []byte(`{"title":"Hanzo","body":"Web push test notification"}`)
	got := pushDeliver(t.Context(), key, "https://cloud.hanzo.ai", sub, payload)

	if !got.OK || got.Status != http.StatusCreated || got.Error != "" {
		t.Fatalf("delivery reported %+v, want ok at 201", got)
	}
	if got.ID != sub.ID {
		t.Errorf("result names %q, want the subscription %q", got.ID, sub.ID)
	}
	if seen == nil {
		t.Fatal("the push service was never called")
	}
	if seen.Method != http.MethodPost {
		t.Errorf("method %s, want POST", seen.Method)
	}
	for h, want := range map[string]string{
		"Content-Encoding": "aes128gcm",
		"Content-Type":     "application/octet-stream",
		"TTL":              strconv.Itoa(pushTTL),
	} {
		if v := seen.Header.Get(h); v != want {
			t.Errorf("%s is %q, want %q", h, v, want)
		}
	}

	// The assertion names the gateway by the same public key vapidPublicKey
	// answers with; a browser refuses one minted under any other.
	pub, err := key.PublicKey.ECDH()
	if err != nil {
		t.Fatalf("gateway public key: %v", err)
	}
	assertion := seen.Header.Get("Authorization")
	if !strings.HasPrefix(assertion, "vapid t=") || !strings.Contains(assertion, ", k="+pushB64(pub.Bytes())) {
		t.Errorf("Authorization %q is not a VAPID assertion under this gateway's key", assertion)
	}

	if out := browserOpen(t, browser, auth, body); !bytes.Equal(out, payload) {
		t.Errorf("the browser reads %q, but %q was sent", out, payload)
	}
}

// A fan-out over several browsers is several independent outcomes. One
// unreachable browser must not swallow the others: it is reported against its
// own endpoint, and every later endpoint is still tried.
func TestPushDeliverReportsEachEndpointSeparately(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gateway key: %v", err)
	}

	// A browser that took it, one whose push service is not answering at all,
	// one the push service says is gone, and one behind both failures.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer ok.Close()
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte("push subscription expired"))
	}))
	defer gone.Close()
	var reachedLast atomic.Bool
	tail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reachedLast.Store(true)
		w.WriteHeader(http.StatusCreated)
	}))
	defer tail.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()

	subs := make([]pushSub, 0, 4)
	for _, e := range []string{ok.URL, dead.URL, gone.URL, tail.URL} {
		s, _, _ := browserSub(t, e)
		subs = append(subs, s)
	}

	// The fan-out pushTest performs: every subscription is attempted and its
	// outcome kept, whatever the one before it did.
	results := make([]pushResult, 0, len(subs))
	for _, s := range subs {
		results = append(results, pushDeliver(t.Context(), key, "https://cloud.hanzo.ai", s, []byte(`{"title":"t","body":"b"}`)))
	}

	if len(results) != len(subs) {
		t.Fatalf("%d results for %d subscriptions", len(results), len(subs))
	}
	if !reachedLast.Load() {
		t.Error("the endpoint after the failures was never tried")
	}
	for i, want := range []struct {
		ok     bool
		status int
	}{{true, http.StatusCreated}, {false, 0}, {false, http.StatusGone}, {true, http.StatusCreated}} {
		got := results[i]
		if got.OK != want.ok || got.Status != want.status {
			t.Errorf("subscription %d reported %+v, want ok=%v status=%d", i, got, want.ok, want.status)
		}
		if got.ID != subs[i].ID {
			t.Errorf("result %d names %q, want %q", i, got.ID, subs[i].ID)
		}
		if !want.ok && got.Error == "" {
			t.Errorf("subscription %d failed without saying why", i)
		}
	}

	// 410 is how a push service reports a subscription the browser abandoned.
	// pushTest drops that row, so the status has to survive as a status.
	if results[2].Status != http.StatusGone {
		t.Errorf("an abandoned subscription reported %d, want 410 so its row is dropped", results[2].Status)
	}
}

// A person's own news is addressed to the person. Nothing a caller can name in
// a request reaches it: a subscription key is a string any member of the org
// may write down, and the keyspace is shared by every family that watches one.
func TestPrefsChangedIsNotReachableByNamingAKey(t *testing.T) {
	url := serve(t)
	announceAt(t, url, "acme", "bot_shared1")
	alice := dialAs(t, url, "acme", "alice@acme", "", false)
	mallory := dialAs(t, url, "acme", "mallory@acme", "bot_shared1", false)

	// Reading a board is how a connection says it is watching one, and the key
	// it hands over is its own string. Here it is the one a person's defaults
	// were addressed by.
	if frame := reply(t, mallory, "1:m", "board.get",
		`{"sessionKey":"user:alice@acme"}`); frame["ok"] != true {
		t.Fatalf("board.get refused: %v", frame)
	}

	pushBrowser(t, alice, "1", "https://push.example.com/alice")
	seen := listen(mallory)
	frame := reply(t, alice, "2:a", "push.web.preferences.set",
		`{"endpoint":"https://push.example.com/alice","scope":"user","preferences":{`+
			`"categories":{"approvalRequested":true,"agentFinished":true,"agentQuestion":true,`+
			`"humanMentioned":true,"scheduledTaskFailed":true,"backgroundTaskFailed":true},`+
			`"detailLevel":"private",`+
			`"quietHours":{"enabled":false,"startMinute":0,"endMinute":0,"timeZone":"UTC"},`+
			`"agentIds":[]}}`)
	if frame["ok"] != true {
		t.Fatalf("push.web.preferences.set refused: %v", frame)
	}

	// A marker every connection of the org hears, raised after the change, so
	// what did not arrive before it was not sent rather than not yet sent.
	PublishOrg("acme", "", "probe.moved", map[string]any{"key": "marker"})
	if got := until(t, seen, "probe.moved"); slices.Contains(got, "users.prefs.changed") {
		t.Errorf("another member read one person's news by naming a key: %v", got)
	}
}

// A browser names itself. The name is `label` in WebPushDevicePreferencesSchema
// (packages/gateway-protocol/src/schema/push.ts:79), so the arm is written
// against that spelling — and because the arm is closed, a spelling the schema
// does not declare refuses the whole write rather than dropping the name.
func TestDevicePreferencesRoundTripTheNameABrowserSends(t *testing.T) {
	url := serve(t)
	ws := dialAs(t, url, "acme", "op@acme", "", false)
	pushBrowser(t, ws, "1", "https://push.example.com/mac")

	set := func(id, prefs string) map[string]any {
		return reply(t, ws, id, "push.web.preferences.set",
			`{"endpoint":"https://push.example.com/mac","scope":"device","preferences":`+prefs+`}`)
	}

	if frame := set("2:set", `{"enabled":true,"label":"MacBook"}`); frame["ok"] != true {
		t.Fatalf("the schema's own device payload was refused: %v", frame)
	}
	if frame := set("3:set", `{"enabled":true,"pushLabel":"MacBook"}`); frame["ok"] != false {
		t.Errorf("a field no schema declares was accepted: %v", frame)
	}

	frame := reply(t, ws, "4:get", "push.web.preferences.get",
		`{"endpoint":"https://push.example.com/mac"}`)
	if frame["ok"] != true {
		t.Fatalf("push.web.preferences.get refused: %v", frame)
	}
	got, _ := frame["payload"].(map[string]any)
	for _, layer := range []string{"device", "effective"} {
		l, _ := got[layer].(map[string]any)
		if l["label"] != "MacBook" {
			t.Errorf("%s reads back %v; the UI binds preferences.label", layer, l)
		}
	}
}

// ---- wake ----

// A wake is for the conversation. A subagent lane is a step inside one, opened
// by an agent for its own work, and an operator's line of text does not belong
// in the middle of it — whether the key says so at the front or after the agent
// that owns the lane.
func TestWakeRefusesASubagentLane(t *testing.T) {
	app := mount(t)
	me := who{org: "acme"}
	for i, key := range []string{
		"subagent:task-abc",
		"SUBAGENT:task-abc",
		"agent:main:subagent:demo",
		"agent:main:SubAgent:demo",
	} {
		id := strconv.Itoa(i) + ":a"
		_, frame := ask(t, app, me, id, "wake",
			`{"mode":"now","text":"hello","sessionKey":"`+key+`"}`)
		if frame["ok"] != false {
			t.Errorf("wake on %q was accepted: %v", key, frame)
			continue
		}
		if code := wrong(t, frame)["code"]; code != "INVALID_REQUEST" {
			t.Errorf("wake on %q refused with %v, want INVALID_REQUEST", key, code)
		}
	}
	// A lane that merely begins with those letters is a lane of its own. The
	// guard reads a prefix, so the boundary is what keeps it from over-reaching.
	if _, frame := ask(t, app, me, "9:a", "wake",
		`{"mode":"now","text":"hello","sessionKey":"agent:main:subagentry"}`); frame["ok"] != true {
		t.Errorf("wake on an ordinary lane was refused: %v", frame)
	}
}
