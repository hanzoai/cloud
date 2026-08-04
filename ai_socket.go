package cloud

// Inference reached over the peer's own socket.
//
// `ai` is a plugin of this same binary running as its own process. Its routes
// ride its unix socket exactly as they ride a public listener — zip's plane is
// "an ordinary route on the app … ZAP over a unix socket is simply the address
// the caller dialed" — so a sibling speaks the ordinary OpenAI-compatible wire
// to it WITHOUT leaving the host.
//
// What that deletes is the whole reason the old path existed:
//
//	base_url  https://api.hanzo.ai/v1   the pod's OWN public address
//	token_url http://iam.hanzo.svc/…    a token minted to authenticate to itself
//
// Both were consequences of addressing a peer by URL. There is no address to
// configure here: the socket is derived from the app NAME, the same mapping the
// meter and the ledger already use.

import (
	"context"
	"net"
	"net/http"

	"github.com/zap-proto/zip"
)

// aiApp is the app name the socket is derived from. One spelling.
const aiApp = "ai"

// aiPeerURL is the base a socket-dialed call carries. The HOST is inert — the
// transport dials a named peer, not this address — so it names the peer for logs
// and error text and nothing more. The /v1 prefix is real: it is the peer's own
// route prefix.
const aiPeerURL = "http://ai/v1"

// aiRoute answers the two questions a caller has about reaching `ai`: over what
// transport, and under what address. It is ONE decision, shared by the
// completions and the embeddings pickers so they cannot drift into disagreeing
// about where the peer is.
//
// !Enabled(ai) means this process does not carry the app, which is exactly when
// `ai` is a SIBLING and its socket is the honest address. The process that IS
// `ai` keeps the configured one — routing inference back through the picker
// there would be the process calling itself.
func aiRoute(cfg *Config) (http.RoundTripper, string) {
	if cfg.Enabled(aiApp) {
		return nil, cfg.AIBaseURL
	}
	return newSocketTransport(aiApp), aiPeerURL
}

// socketRoundTripper speaks HTTP to one app over its canonical unix socket.
//
// It WAKES the peer before dialing, through the same reach() every plane call
// uses: an app is lazy by default, so a sibling that dialed a cold socket would
// read "not deployed here" from what is really "not started yet". reach asks the
// router, which owns the manifest, so absence and outage stay distinguishable.
type socketRoundTripper struct {
	app  string
	next http.RoundTripper
}

func newSocketTransport(app string) http.RoundTripper {
	srt := &socketRoundTripper{app: app}
	srt.next = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// network and address are DISCARDED: the peer is named, not addressed.
			// Whatever host the base URL carries is inert here, which is why the
			// deployment no longer states one.
			return (&net.Dialer{}).DialContext(ctx, "unix", zip.SocketPath(srt.app))
		},
	}
	return srt
}

func (s *socketRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	bindRuntimeDir()
	if err := reach(r.Context(), s.app); err != nil {
		return nil, err
	}
	return s.next.RoundTrip(r)
}
