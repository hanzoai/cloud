package claw

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
)

// The identity family answers a question about live connections, so these
// tests ask it over real sockets: the roster it returns is the set of clients
// actually attached to the mounted surface, never a fixture.

// dialAs opens the protocol socket for one named client. socket_test.go's dial
// speaks for a single user; this family is about telling clients apart, so
// these tests vary the user and the bot a connection binds to.
func dialAs(t *testing.T, url, org, user, bot string, admin bool) *websocket.Conn {
	t.Helper()
	if bot != "" {
		url += "?bot=" + bot
	}
	h := http.Header{}
	h.Set("X-Org-Id", org)
	h.Set("X-User-Id", user)
	if admin {
		h.Set("X-User-IsOrgAdmin", "true")
	}
	ws, res, err := websocket.DefaultDialer.Dial(url, h)
	if err != nil {
		t.Fatalf("dial %s: %v", user, err)
	}
	_ = res.Body.Close()
	t.Cleanup(func() { _ = ws.Close() })
	// The handshake is answered from the read loop, which the connection joins
	// the hub before entering — so a reply proves this client is on the roster.
	if frame := reply(t, ws, "hello:"+user, "connect", ""); frame["ok"] != true {
		t.Fatalf("connect %s: %v", user, frame)
	}
	return ws
}

// reply sends one request and reads until its answer arrives. A method that
// publishes reaches the connection that called it too, so the answer is not
// always the next frame; the real client correlates by id, and so does this.
func reply(t *testing.T, ws *websocket.Conn, id, method, params string) map[string]any {
	t.Helper()
	frame := say(t, ws, id, method, params)
	for frame["type"] != "res" {
		frame = next(t, ws)
	}
	if frame["id"] != id {
		t.Fatalf("%s answered id %v, want %v", method, frame["id"], id)
	}
	return frame
}

// roster reads the device list and returns its rows by device id.
func roster(t *testing.T, ws *websocket.Conn, req string) map[string]map[string]any {
	t.Helper()
	frame := reply(t, ws, req, "device.pair.list", `{}`)
	if frame["ok"] != true {
		t.Fatalf("device.pair.list refused: %v", frame)
	}
	list, _ := frame["payload"].(map[string]any)
	pending, ok := list["pending"].([]any)
	if !ok {
		t.Fatalf("pending is %v, want an array — the UI reads its length", list["pending"])
	}
	if len(pending) != 0 {
		t.Errorf("pending carries %d requests; a client is admitted by IAM before it gets here", len(pending))
	}
	paired, ok := list["paired"].([]any)
	if !ok {
		t.Fatalf("paired is %v, want an array", list["paired"])
	}
	out := map[string]map[string]any{}
	for _, r := range paired {
		row, _ := r.(map[string]any)
		key, _ := row["deviceId"].(string)
		if key == "" {
			t.Fatalf("a row carries no deviceId: %v", row)
		}
		out[key] = row
	}
	if len(out) != len(paired) {
		t.Fatalf("the roster repeats a device id: %v", paired)
	}
	return out
}

func scopesOf(t *testing.T, row map[string]any) []string {
	t.Helper()
	raw, ok := row["scopes"].([]any)
	if !ok {
		t.Fatalf("row carries no scopes: %v", row)
	}
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		v, _ := s.(string)
		out = append(out, v)
	}
	return out
}

// gone asserts the socket ended rather than went quiet.
func gone(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	if err := ws.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
	}
}

// The roster is who is connected, and what IAM lets each of them do. An
// operator's own capabilities are not everyone's: the member on the same
// roster holds fewer, because the grant is read per connection.
func TestDeviceRosterIsWhoIsConnected(t *testing.T) {
	url := serve(t)
	op := dialAs(t, url, "acme", "op@acme", "", true)
	dialAs(t, url, "acme", "sam@acme", "", false)

	rows := roster(t, op, "1:list")
	if len(rows) != 2 {
		t.Fatalf("roster has %d devices, want the 2 that are connected: %v", len(rows), rows)
	}
	for id, row := range rows {
		if row["connected"] != true {
			t.Errorf("%s is on the roster but not connected: %v", id, row)
		}
		if row["role"] != role {
			t.Errorf("%s holds role %v, want %s", id, row["role"], role)
		}
		if _, has := row["tokens"]; has {
			t.Errorf("%s carries a token summary; there is no stored credential to summarise, "+
				"and a token row draws a rotate and a revoke control this surface does not offer", id)
		}
	}
	if got := rows["op@acme"]["displayName"]; got != "op@acme" {
		t.Errorf("displayName is %v, want the validated user", got)
	}
	if !slices.Contains(scopesOf(t, rows["op@acme"]), string(Pairing)) {
		t.Errorf("an admin of the org does not hold %s: %v", Pairing, rows["op@acme"])
	}
	if slices.Contains(scopesOf(t, rows["sam@acme"]), string(Pairing)) {
		t.Errorf("a plain member holds %s: %v — the row reports IAM's answer, not a constant",
			Pairing, rows["sam@acme"])
	}
}

// A device is an identity, not a socket: a person with two windows open is one
// device, and the same person driving a bot is another.
func TestDeviceRosterCountsAnIdentityOnce(t *testing.T) {
	url := serve(t)
	announceAt(t, url, "acme", "worker-01")
	op := dialAs(t, url, "acme", "op@acme", "", true)
	dialAs(t, url, "acme", "op@acme", "", true)
	dialAs(t, url, "acme", "op@acme", "worker-01", true)

	rows := roster(t, op, "1:list")
	if len(rows) != 2 {
		t.Fatalf("three sockets of one person made %d devices, want 2: %v", len(rows), rows)
	}
	if _, ok := rows["op@acme"]; !ok {
		t.Errorf("the person's own client is missing: %v", rows)
	}
	if _, ok := rows["op@acme/worker-01"]; !ok {
		t.Errorf("the bot-bound client is missing: %v", rows)
	}
}

// An org sees its own clients and no others.
func TestDeviceRosterDoesNotCrossOrgs(t *testing.T) {
	url := serve(t)
	op := dialAs(t, url, "acme", "op@acme", "", true)
	dialAs(t, url, "other", "op@other", "", true)

	rows := roster(t, op, "1:list")
	if _, leaked := rows["op@other"]; leaked {
		t.Fatalf("another org's client is on this roster: %v", rows)
	}
	if len(rows) != 1 {
		t.Fatalf("roster has %d devices, want 1: %v", len(rows), rows)
	}
}

// Reading the roster is a pairing capability, and a member of the org does not
// hold it.
func TestDeviceRosterNeedsPairing(t *testing.T) {
	sam := dialAs(t, serve(t), "acme", "sam@acme", "", false)

	frame := reply(t, sam, "1:list", "device.pair.list", `{}`)
	if frame["ok"] != false {
		t.Fatalf("a plain member read the roster: %v", frame)
	}
	e, _ := frame["error"].(map[string]any)
	if e["code"] != "FORBIDDEN" {
		t.Fatalf("refusal code is %v, want FORBIDDEN", e["code"])
	}
	details, _ := e["details"].(map[string]any)
	if details["missingScope"] != string(Pairing) {
		t.Errorf("refusal names %v, want %s", details["missingScope"], Pairing)
	}
}

// A name is given to a device, not to the socket it arrived on, so it is still
// there when the client comes back on a new one.
func TestDeviceNameOutlivesTheSocket(t *testing.T) {
	url := serve(t)
	op := dialAs(t, url, "acme", "op@acme", "", true)
	sam := dialAs(t, url, "acme", "sam@acme", "", false)

	frame := reply(t, op, "1:name", "device.pair.rename", `{"deviceId":"sam@acme","label":"Sam's laptop"}`)
	if frame["ok"] != true {
		t.Fatalf("rename: %v", frame)
	}
	named, _ := frame["payload"].(map[string]any)
	if named["deviceId"] != "sam@acme" || named["label"] != "Sam's laptop" {
		t.Errorf("rename answered %v", named)
	}
	if got := roster(t, op, "2:list")["sam@acme"]["operatorLabel"]; got != "Sam's laptop" {
		t.Fatalf("the roster shows label %v, want the one just given", got)
	}

	_ = sam.Close()
	dialAs(t, url, "acme", "sam@acme", "", false)
	if got := roster(t, op, "3:list")["sam@acme"]["operatorLabel"]; got != "Sam's laptop" {
		t.Errorf("after reconnecting the label is %v; it is kept under the identity, not the socket", got)
	}
}

// A name can only be given to a device that is there, and only a name that
// fits.
func TestDeviceRenameRefusesWhatItCannotName(t *testing.T) {
	op := dialAs(t, serve(t), "acme", "op@acme", "", true)

	for _, tc := range []struct{ name, params, want string }{
		{"a device nobody is on", `{"deviceId":"ghost@acme","label":"nobody"}`, "unknown deviceId"},
		{"no name at all", `{"deviceId":"op@acme","label":"   "}`, "label required"},
		{"a name that does not fit", `{"deviceId":"op@acme","label":"` + strings.Repeat("n", maxDeviceLabel+1) + `"}`, "label exceeds"},
	} {
		frame := reply(t, op, "1:"+tc.name, "device.pair.rename", tc.params)
		if frame["ok"] != false {
			t.Fatalf("%s was accepted: %v", tc.name, frame)
		}
		e, _ := frame["error"].(map[string]any)
		if e["code"] != "INVALID_REQUEST" {
			t.Errorf("%s refused with %v, want INVALID_REQUEST", tc.name, e["code"])
		}
		if msg, _ := e["message"].(string); !strings.Contains(msg, tc.want) {
			t.Errorf("%s refused with %q, want it to say %q", tc.name, msg, tc.want)
		}
	}
}

// Parameters are closed, so a field this method does not know is a caller that
// has misunderstood it.
func TestDeviceRenameRefusesAFieldItDoesNotKnow(t *testing.T) {
	op := dialAs(t, serve(t), "acme", "op@acme", "", true)

	frame := reply(t, op, "1:name", "device.pair.rename",
		`{"deviceId":"op@acme","label":"mine","operatorLabel":"mine"}`)
	if frame["ok"] != false {
		t.Fatalf("an unknown parameter was accepted: %v", frame)
	}
}

// Removing a device ends its sessions and takes it off the roster.
func TestDeviceRemoveEndsTheSession(t *testing.T) {
	url := serve(t)
	op := dialAs(t, url, "acme", "op@acme", "", true)
	sam := dialAs(t, url, "acme", "sam@acme", "", false)

	frame := reply(t, op, "1:remove", "device.pair.remove", `{"deviceId":"sam@acme"}`)
	if frame["ok"] != true {
		t.Fatalf("remove: %v", frame)
	}
	removed, _ := frame["payload"].(map[string]any)
	if removed["deviceId"] != "sam@acme" {
		t.Errorf("remove answered for %v", removed["deviceId"])
	}
	if removed["connected"] != false {
		t.Errorf("the removed device is still reported connected: %v", removed)
	}
	gone(t, sam)
	if rows := roster(t, op, "2:list"); len(rows) != 1 {
		t.Fatalf("the removed device is still on the roster: %v", rows)
	}
}

// The session that asks is left running, so that it hears the answer, and the
// answer says the device is still connected because it is.
func TestDeviceRemoveKeepsTheAskingSession(t *testing.T) {
	op := dialAs(t, serve(t), "acme", "op@acme", "", true)

	frame := reply(t, op, "1:remove", "device.pair.remove", `{"deviceId":"op@acme"}`)
	if frame["ok"] != true {
		t.Fatalf("remove: %v", frame)
	}
	removed, _ := frame["payload"].(map[string]any)
	if removed["connected"] != true {
		t.Errorf("the asking session was reported gone: %v", removed)
	}
	if rows := roster(t, op, "2:list"); len(rows) != 1 {
		t.Fatalf("the asking session was ended: %v", rows)
	}
}

// Removing a device forgets its name, which is the whole of what this surface
// held about it.
func TestDeviceRemoveForgetsTheName(t *testing.T) {
	url := serve(t)
	op := dialAs(t, url, "acme", "op@acme", "", true)
	sam := dialAs(t, url, "acme", "sam@acme", "", false)

	if frame := reply(t, op, "1:name", "device.pair.rename",
		`{"deviceId":"sam@acme","label":"Sam's laptop"}`); frame["ok"] != true {
		t.Fatalf("rename: %v", frame)
	}
	if frame := reply(t, op, "2:remove", "device.pair.remove", `{"deviceId":"sam@acme"}`); frame["ok"] != true {
		t.Fatalf("remove: %v", frame)
	}
	gone(t, sam)

	dialAs(t, url, "acme", "sam@acme", "", false)
	if got := roster(t, op, "3:list")["sam@acme"]["operatorLabel"]; got != nil {
		t.Errorf("the removed device came back named %v", got)
	}
}

// Removing something nobody is on is a request the caller can correct.
func TestDeviceRemoveRefusesAnUnknownDevice(t *testing.T) {
	op := dialAs(t, serve(t), "acme", "op@acme", "", true)

	frame := reply(t, op, "1:remove", "device.pair.remove", `{"deviceId":"ghost@acme"}`)
	if frame["ok"] != false {
		t.Fatalf("removing a device nobody is on succeeded: %v", frame)
	}
	e, _ := frame["error"].(map[string]any)
	if e["code"] != "INVALID_REQUEST" {
		t.Errorf("refusal code is %v, want INVALID_REQUEST", e["code"])
	}
}

// A change to the roster reaches the other operators watching it, so their view
// refetches instead of going stale.
func TestDeviceChangeReachesTheOtherOperators(t *testing.T) {
	url := serve(t)
	one := dialAs(t, url, "acme", "one@acme", "", true)
	two := dialAs(t, url, "acme", "two@acme", "", true)

	if frame := reply(t, one, "1:name", "device.pair.rename",
		`{"deviceId":"two@acme","label":"the other desk"}`); frame["ok"] != true {
		t.Fatalf("rename: %v", frame)
	}
	frame := next(t, two)
	if frame["type"] != "event" || frame["event"] != deviceChanged {
		t.Fatalf("the second operator was told %v, want a %s event", frame, deviceChanged)
	}
}

// The methods that would mint, admit or revoke a credential are not offered.
// Identity is IAM's, and hello.features.methods is how this protocol says what
// a gateway does: a control the UI cannot see is better than one that is always
// refused.
func TestCredentialMethodsAreNotOffered(t *testing.T) {
	app := mount(t)

	code, frame := ask(t, app, who{org: "acme", admin: true}, "1:hi", "connect", "")
	if code != http.StatusOK || frame["ok"] != true {
		t.Fatalf("connect: %d %v", code, frame)
	}
	features, _ := payload(t, frame)["features"].(map[string]any)
	raw, _ := features["methods"].([]any)
	offered := map[string]bool{}
	for _, m := range raw {
		name, _ := m.(string)
		offered[name] = true
	}

	for _, want := range []string{"device.pair.list", "device.pair.rename", "device.pair.remove"} {
		if !offered[want] {
			t.Errorf("%s is not advertised, so the UI will hide it", want)
		}
	}
	for _, absent := range []string{
		"device.pair.approve", "device.pair.reject",
		"device.token.rotate", "device.token.revoke",
	} {
		if offered[absent] {
			t.Errorf("%s is advertised; this surface admits and revokes nothing — that is IAM's", absent)
		}
	}
}

// The roster's labels are the org's, in the org's own file: every connection
// the org has open reads that one file, whichever bot it happened to bind to.
// So a rename is news to all of them. Addressed to a partition instead, the
// operator who renamed a device is the only one whose screen is right, and the
// rest keep showing a name that is no longer stored anywhere.
func TestADeviceRenameReachesTheOrgAndNotOnePartition(t *testing.T) {
	url := serve(t)
	announceAt(t, url, "acme", "bot_aaaaaaaa")
	watching := dialAs(t, url, "acme", "op@acme", "", true)
	bound := dialAs(t, url, "acme", "op@acme", "bot_aaaaaaaa", true)

	seen := listen(watching)
	frame := reply(t, bound, "1:r", "device.pair.rename",
		`{"deviceId":"op@acme/bot_aaaaaaaa","label":"the sandbox"}`)
	if frame["ok"] != true {
		t.Fatalf("device.pair.rename refused: %v", frame)
	}
	Publish("acme", "", "", "probe.moved", map[string]any{"key": "marker"})
	if got := until(t, seen, "probe.moved"); !slices.Contains(got, deviceChanged) {
		t.Errorf("a rename made from a bot-bound socket did not reach the rest of the org: %v", got)
	}
}
