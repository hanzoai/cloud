package plane

// readdeadline.go widens the plane's RESPONSE-READ deadline so a call that is
// legitimately slow is not cut off by a transport default.
//
// # The bug this fixes
//
// Every @hanzo turn in Slack answered "the agent hit an error handling that",
// and the bridge logged:
//
//	zip: call agents_run_on_behalf at /var/lib/cloud/run/agents.sock:
//	zaphttp: read response: i/o timeout
//
// zap-proto/http@v0.3.1 sets `readTimeout: 30 * time.Second` (client.go:72) on
// every dialled transport. An agent turn runs a real model completion, and enso
// spends 40s+ on one — measured in production at 41,153ms server-side, answered
// 200 by both `ai` and `agents`. So the work SUCCEEDED and the caller had already
// hung up on it. The reply was written to a socket nobody was reading.
//
// That is the worst shape a timeout can have: the expensive work is done and
// paid for, the callee logs success, and only the caller reports failure — so
// every log you would naturally check says the system is healthy.
//
// # Why the context budget did not save it
//
// The bridge already bounds a turn at bridgeAgentTimeout (110s) on a detached
// context. That governs the CALL; it does not reach the transport's own
// SetReadDeadline, which is wall-clock on the connection and shorter. Two
// deadlines existed for one operation and the smaller one — the one nobody
// chose — won.
//
// # The fix, and why it is a registration rather than a patch
//
// zip resolves a scheme to a Transport through a registry (zip.RegisterTransport,
// transport.go:109) and its default `zap` Dial is one line:
//
//	Dial: func(addr string) Client { return zaphttp.Dial(networkOf(addr), addr) }
//
// So the client already exists: re-register the same scheme with the same dialler
// and one call to the knob zap-proto/http exports for exactly this
// (SetReadTimeout, client.go:81). Nothing is forked and no behaviour changes
// except the number.
//
// # Why this number
//
// planeReadTimeout must be LONGER than the longest legitimate caller budget, so
// that the caller's context is what expires. Today that is the chat bridge's
// 110s. Set to 15 minutes to match the host's own plugin-start budget — the
// deadline that already governs how long the fleet is willing to wait for a
// child — so there is one answer to "how long may an in-flight plane call take"
// rather than two.
//
// This is a CEILING, never a floor. Every caller still bounds itself with a
// context; a caller that gives itself ten seconds still gets ten seconds. What
// changes is that a caller can no longer be cut off BELOW its own budget by a
// default it never saw.

import (
	"strings"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
	"github.com/zap-proto/zip"
)

// planeReadTimeout bounds how long a plane call waits for its response. See the
// file comment for why it is 15 minutes and why it is a ceiling.
const planeReadTimeout = 15 * time.Minute

func init() { widenPlaneReadDeadline() }

// widenPlaneReadDeadline re-registers the zap scheme with the stock dialler and
// a response-read deadline that does not undercut the caller.
//
// It runs at init because a transport must be registered before the first Dial,
// and plane is imported by every process that makes a plane call. Registering
// the same scheme twice is defined: RegisterTransport "adds (or replaces)".
func widenPlaneReadDeadline() {
	zip.RegisterTransport("zap", zip.Transport{
		// The stock Serve, verbatim (zip transport.go:66-68). Only Dial changes;
		// re-registering a scheme replaces BOTH halves, so the serve side has to
		// be restated or every plugin stops listening.
		Serve: func(addr string, h fasthttp.RequestHandler) zip.Server {
			return &zaphttp.Server{Network: networkOf(addr), Addr: addr, Handler: h}
		},
		Dial: func(addr string) zip.Client {
			t := zaphttp.Dial(networkOf(addr), addr)
			t.SetReadTimeout(planeReadTimeout)
			return t
		},
	})
}

// networkOf mirrors zip's own rule (transport.go:125): a path is a unix socket,
// anything else is tcp. Copied rather than imported because zip keeps it
// unexported — and it is three lines, so the alternative is a fork.
func networkOf(addr string) string {
	if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "./") || strings.HasPrefix(addr, "@") {
		return "unix"
	}
	return "tcp"
}
