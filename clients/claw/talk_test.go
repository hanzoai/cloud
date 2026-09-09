package claw

import "testing"

// talkMethods is every method this family serves, and what each costs. A client
// reads the first list out of the handshake to decide which controls to offer.
var talkMethods = map[string]Scope{
	"talk.catalog": Read,
	"talk.config":  Read,
}

// talkGone is every method that addresses a live audio path. Each names a
// session, a mark, a tool result or an agent run, and there is none here, so
// each could only refuse. They stay off the registry and off the handshake: a
// composer that sees them offers a microphone that fails when it is pressed.
var talkGone = []string{
	"talk.client.create", "talk.client.toolCall", "talk.client.transcript",
	"talk.client.close", "talk.client.steer",
	"talk.session.create", "talk.session.appendAudio", "talk.session.cancelOutput",
	"talk.session.acknowledgeMark", "talk.session.submitToolResult",
	"talk.session.steer", "talk.session.close",
}

// The microphone is offered or withheld on this answer: the UI sets the
// control to "unavailable" when realtime.ready is not true
// (ui/src/pages/chat/composer-microphone-picker.ts:169). Each provider list
// must be a list, empty — a null there is what a client maps over and dies on.
func TestTalkCatalogIsEmptyAndComplete(t *testing.T) {
	app := mount(t)

	_, frame := ask(t, app, who{org: "acme"}, "1:a", "talk.catalog", `{}`)
	if frame["ok"] != true {
		t.Fatalf("talk.catalog refused: %v", frame)
	}
	shelf := payload(t, frame)

	for _, name := range []string{"modes", "transports", "brains"} {
		list, ok := shelf[name].([]any)
		if !ok {
			t.Errorf("%s is %v, want a list", name, shelf[name])
			continue
		}
		if len(list) != 0 {
			t.Errorf("%s claims %v, and this gateway serves none", name, list)
		}
	}

	for _, name := range []string{"speech", "transcription", "realtime"} {
		family, ok := shelf[name].(map[string]any)
		if !ok {
			t.Fatalf("the catalog has no %s: %v", name, shelf)
		}
		if family["ready"] == true {
			t.Errorf("%s says it is ready, and nothing here can serve a call", name)
		}
		providers, ok := family["providers"].([]any)
		if !ok {
			t.Fatalf("%s.providers is %v, want a list — a client maps over it", name, family["providers"])
		}
		if len(providers) != 0 {
			t.Errorf("%s offers %v, and no provider is configured", name, providers)
		}
	}
}

// The configuration must not name a transport this gateway cannot hold: the UI
// reads config.talk.realtime.transport to choose how to retry after a session
// fails (ui/src/pages/chat/realtime-talk.ts:346), and a named one sends it back
// for a second refusal.
func TestTalkConfigNamesNoTransport(t *testing.T) {
	app := mount(t)

	_, frame := ask(t, app, who{org: "acme"}, "1:a", "talk.config", `{}`)
	if frame["ok"] != true {
		t.Fatalf("talk.config refused: %v", frame)
	}
	config, ok := payload(t, frame)["config"].(map[string]any)
	if !ok {
		t.Fatalf("talk.config answered no config object: %v", frame)
	}
	if talk, exists := config["talk"]; exists {
		t.Errorf("the answer carries a Talk configuration (%v), and there is none", talk)
	}
	hints, _ := config["clientHints"].(map[string]any)
	realtime, _ := hints["realtime"].(map[string]any)
	if realtime["gatewayRelaySupported"] != false {
		t.Errorf("the relay hint is %v, want false: this gateway relays nothing",
			realtime["gatewayRelaySupported"])
	}
}

// Reading the configuration with its secrets asked for is answered the same
// way, because this gateway holds no Talk credential for the flag to reveal.
func TestTalkConfigWithSecretsCarriesNone(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, who{org: "acme"}, "1:a", "talk.config", `{"includeSecrets":true}`)
	if frame["ok"] != true {
		t.Fatalf("talk.config refused: %v", frame)
	}
	config, _ := payload(t, frame)["config"].(map[string]any)
	if _, exists := config["talk"]; exists {
		t.Errorf("asking for secrets produced a Talk configuration: %v", config)
	}
}

// The handshake lists what this surface answers, and a client offers only the
// controls it finds there.
func TestTalkFamilyIsAdvertised(t *testing.T) {
	app := mount(t)

	_, frame := ask(t, app, who{org: "acme"}, "1:a", "connect", `{"minProtocol":4,"maxProtocol":4}`)
	features, _ := payload(t, frame)["features"].(map[string]any)
	listed := map[string]bool{}
	for _, m := range features["methods"].([]any) {
		listed[m.(string)] = true
	}
	for method := range talkMethods {
		if !listed[method] {
			t.Errorf("%s is served but not advertised, so a client will not offer it", method)
		}
	}
	for _, method := range talkGone {
		if listed[method] {
			t.Errorf("%s is advertised and cannot be answered", method)
		}
	}
	// talk.event carries a live call's audio and transcript. With no call
	// there is none to carry, so it is not announced either.
	for _, e := range features["events"].([]any) {
		if e == "talk.event" {
			t.Error("talk.event is announced, and nothing here emits it")
		}
	}
}

// What each method costs is the protocol's own price. A member of the org holds
// read and write both, so no request can tell these apart: the registration is
// what has to be right.
func TestTalkCosts(t *testing.T) {
	surface.mu.RLock()
	defer surface.mu.RUnlock()
	for method, need := range talkMethods {
		m, ok := surface.methods[method]
		if !ok {
			t.Errorf("%s is not registered", method)
			continue
		}
		if m.need != need {
			t.Errorf("%s costs %s, want %s", method, m.need, need)
		}
	}
	for _, method := range talkGone {
		if _, ok := surface.methods[method]; ok {
			t.Errorf("%s is registered, and nothing here can answer it", method)
		}
	}
}
