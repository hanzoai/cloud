package cloud

// Where a sibling process reaches the model API.
//
// `ai` is a plugin of this same binary running as its own process, and the
// question this file answers is the narrow one: from ANOTHER process of the same
// fleet, what address serves /v1/chat/completions?
//
// # What the plane socket is, and what it is not
//
// This used to dial the peer's canonical socket — zip.SocketPath("ai"),
// /var/lib/cloud/run/ai.sock — and speak ordinary HTTP to it, on the belief that
// "an app's routes ride its unix socket exactly as they ride a public listener".
// That belief is wrong twice, and each half is independently fatal.
//
//	THE WIRE IS NOT HTTP. That socket is served by zaphttp.Server (zip
//	transport.go: the "zap" scheme, the default for a bare address) — a framed
//	binary protocol with its own codec. A cleartext HTTP request is not slower
//	there, it is unintelligible: the peer reads a malformed frame and closes, so
//	the caller gets `Post "http://ai/v1/chat/completions": EOF` on every request,
//	any method, any path, first connection, peer perfectly healthy.
//
//	THE SURFACE IS NOT THE APP'S. What binds there is the app's PLANE — the
//	typed-op door at /.well-known/zip/op/<name>, which is what plane.Ask uses
//	(plane/ask.go: "ServePlane binds before the app's own listener"). The app's
//	own HTTP routes are on a listener the plane socket knows nothing about, so
//	/v1/chat/completions is a 404 there even when the wire is spoken correctly.
//	Measured on a healthy pod: over ZAP, ai.sock answers 404 for /v1/models and
//	/v1/chat/completions alike, while ai's own listener answers 200 and 401.
//
// Together they are why @hanzo in Slack answered "the agent hit an error handling
// that": the model call EOF'd, agents recorded an honest error-status run, and the
// bridge turned that into its generic reply. `ai` never logged the request because
// the request never arrived.
//
// # The address that does serve it
//
// The fleet ROUTER's own HTTP listener — the one CLOUD_LISTEN names and
// api.hanzo.ai is merely the public face of. It owns the route table that sends
// /v1/* to `ai`, and it owns starting a cold app, so reaching the model API
// through it is not a special case: it is the same door every external caller
// uses, entered from inside.
//
// On LOOPBACK, which is the whole point. The router runs in this pod, so
// 127.0.0.1 never leaves the network namespace: no DNS, no Service hop, and
// above all no trip out through Cloudflare and back to the pod's own public
// address, which is what the configured base URL (https://api.hanzo.ai/v1) does
// and what made a completion depend on the edge being willing to loop. The
// credential is unchanged — same static key or same M2M identity, chosen the
// same way by the pickers in build.go — because who may ask is a different
// question from where the peer is.
//
// There is deliberately NO second mechanism here. A raw route reached
// process-to-process is not something this fleet offers; ops are (plane.Ask), and
// inventing a parallel path for the one surface that is not an op is what broke
// it. One door, entered from inside.

import (
	"net"
	"net/http"
	"strings"
)

// aiApp is the app name this decision is about. One spelling.
const aiApp = "ai"

// aiLoopbackPort is the port assumed when the listener address names none. It
// matches config.go's own default for CLOUD_LISTEN, so the two cannot drift into
// disagreeing about where this binary listens.
const aiLoopbackPort = "8080"

// aiRoute answers the two questions a caller has about reaching `ai`: over what
// transport, and under what address. It is ONE decision, shared by the
// completions and the embeddings pickers so they cannot drift into disagreeing
// about where the peer is.
//
// !Enabled(ai) means this process does not carry the app, which is exactly when
// `ai` is a SIBLING and the router's loopback listener is the honest address. The
// process that IS `ai` keeps the configured one — routing inference back through
// the picker there would be the process calling itself.
//
// The transport is nil in both branches: an ordinary HTTP address is reached with
// the ordinary transport, and the pickers' "socket" log field reads false because
// no socket is involved. Neither branch needs a custom RoundTripper — waking a
// cold app is the router's job, and doing it again here would be a second
// mechanism for something that already has one.
func aiRoute(cfg *Config) (http.RoundTripper, string) {
	if cfg.Enabled(aiApp) {
		return nil, cfg.AIBaseURL
	}
	return nil, aiLoopbackURL(cfg)
}

// aiLoopbackURL is the router's HTTP listener as seen from inside its own pod.
//
// Only the PORT is taken from the configured listener: the host half is whatever
// the process binds (":8000", "0.0.0.0:8000"), and neither is an address a client
// may dial. 127.0.0.1 is, and it is the one that cannot leave the pod.
func aiLoopbackURL(cfg *Config) string {
	port := aiLoopbackPort
	if addr := strings.TrimSpace(cfg.ListenAddr); addr != "" {
		if _, p, err := net.SplitHostPort(addr); err == nil && p != "" {
			port = p
		}
	}
	return "http://127.0.0.1:" + port + "/v1"
}
