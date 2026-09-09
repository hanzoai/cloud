package claw

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"slices"
	"testing"

	"github.com/fasthttp/websocket"
)

// A person's notification defaults follow them to every browser and are stored
// in the org's own file: the bot a connection happens to be bound to says
// nothing about where they live (notifyStore). So a change made in one browser
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
