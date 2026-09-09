package bot

// The talk family: a spoken call between a person and an agent.
//
// A Talk session is a live audio path, not a record. The browser opens the
// microphone and sends what it captures 4096 samples at a time, while the
// gateway holds a socket to a speech provider and pushes what comes back —
// audio, transcript, marks, tool calls — onto the connection the request
// arrived on.
//
// This cloud has no speech provider. Models and inference are hanzoai/ai
// behind api.hanzo.ai, which answers chat completions rather than holding a
// realtime audio socket. So the two methods that report what this gateway
// knows about itself are the whole family:
//
//	talk.catalog   what could serve a call: nothing, and ready is false
//	talk.config    what is configured for Talk: nothing
//
// The catalog is what decides whether the UI offers voice at all: the
// microphone control reads realtime.ready and transcription.ready and shows
// "unavailable" when neither is true, in
// ui/src/pages/chat/composer-microphone-picker.ts:169. An empty catalog is
// therefore what keeps a call from being started that cannot be held.
//
// The thirteen methods that address a live audio path are not registered. Each
// names a session id, a mark, a tool result or an agent run, and there is
// nothing here for any of them to name, so each could only refuse. With the
// catalog empty the microphone is not offered and no client reaches them; a
// gateway that advertised them would put a control on the composer that fails
// every time it is pressed.
//
// What a real one needs is a provider: a socket held per call, its audio
// pushed onto the connection as talk.event, and a credential minted for the
// browser to open its own. The catalog is where it becomes visible — a ready
// family there is what turns the microphone back on.

// The protocol costs the two reads operator.read, as it does upstream.
// talk.config's includeSecrets — which upstream prices at a second scope —
// selects nothing here, because this gateway holds no Talk credential.
//
// No event is announced. talk.event carries a session's audio and transcript
// back to the browser; with no session there is none to carry.
func init() {
	Register("talk.catalog", Read, talkCatalog)
	Register("talk.config", Read, talkConfig)
}

// ---- what this gateway knows about itself ----

// talkCatalog reports what could serve a call. Every provider list is empty
// and every family is not ready, which is the state of this cloud: the answer
// is a true empty rather than a candidate a client would then fail against.
func talkCatalog(c *Call) (any, error) {
	if err := c.Bind(&struct{}{}); err != nil {
		return nil, err
	}
	return talkCatalogView{
		Modes:         []string{},
		Transports:    []string{},
		Brains:        []string{},
		Speech:        talkCapability{Providers: []any{}},
		Transcription: talkCapability{Providers: []any{}},
		Realtime:      talkCapability{Providers: []any{}},
	}, nil
}

// talkCatalogView is TalkCatalogResultSchema (channels.ts:376). All six fields
// are required, so each is present and each is empty.
type talkCatalogView struct {
	Modes         []string       `json:"modes"`
	Transports    []string       `json:"transports"`
	Brains        []string       `json:"brains"`
	Speech        talkCapability `json:"speech"`
	Transcription talkCapability `json:"transcription"`
	Realtime      talkCapability `json:"realtime"`
}

// talkCapability is one thing Talk can do and who could do it. Providers is
// always a list, never null: a client maps over it without asking whether it
// is there.
type talkCapability struct {
	Ready     bool  `json:"ready"`
	Providers []any `json:"providers"`
}

// talkConfig reports the Talk configuration. There is none, so the answer
// carries only the hint that says as much: a client reading
// clientHints.realtime.gatewayRelaySupported learns not to fall back to a
// gateway-relayed call, which is the fallback the UI takes when a client-owned
// session cannot be created (ui/src/pages/chat/realtime-talk.ts:346).
//
// includeSecrets selects nothing. Upstream prices it at a second scope because
// the answer can carry provider credentials; this gateway holds none, so there
// is nothing for the flag to reveal or for a scope to protect.
func talkConfig(c *Call) (any, error) {
	var p struct {
		IncludeSecrets *bool `json:"includeSecrets"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	return talkConfigView{Config: talkConfigBody{
		Hints: talkHints{Realtime: talkRelayHint{Supported: false}},
	}}, nil
}

// talkConfigView is TalkConfigResultSchema (channels.ts:562). config.talk is
// absent rather than empty: there is no Talk configuration, which is not the
// same as one whose fields are unset.
type talkConfigView struct {
	Config talkConfigBody `json:"config"`
}

type talkConfigBody struct {
	Hints talkHints `json:"clientHints"`
}

type talkHints struct {
	Realtime talkRelayHint `json:"realtime"`
}

type talkRelayHint struct {
	Supported bool `json:"gatewayRelaySupported"`
}
