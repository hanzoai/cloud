package main

// The plugin wire's response deadline.
//
// THE BUG. zip dials every plugin with zaphttp's DEFAULT transport, whose
// readTimeout is 30 seconds (zap-proto/http client.go: `readTimeout: 30 *
// time.Second`), and nothing overrode it. That deadline is armed ONCE, just
// before the response head is read — and for a STREAMED response it is never
// re-armed: the head arrives, `resp.SetBodyStream(...)` returns, and the whole
// body must then arrive within what is left of the original 30 seconds.
//
// So it is not an idle timeout. It is a hard cap on the TOTAL DURATION of a
// response, applied to a plane whose responses are model completions.
//
// WHAT IT DID. `ai` owns the /v1 remainder — /v1/chat/completions and the rest
// of the OpenAI-compatible surface — and runs as a plugin behind this wire. So
// every completion longer than 30 seconds was cut:
//
//   - streamed: the body stops mid-token at ~30.1s with NO finish_reason, which
//     downstream reads as a complete answer. Measured against the live gateway:
//     a long page request to claude-opus-4.8 returned 7056 bytes, no </html>,
//     total 30.1s — and the identical request straight to the upstream ran 389
//     seconds and finished properly at 15661 bytes.
//   - unstreamed: 502 `zaphttp: read response: read unix …: i/o timeout`.
//
// It is why hanzo.app's builder could produce a small landing page and not a
// real app: the app is simply a longer generation, and the truncation was
// silent. It also made a slow-to-first-token model look broken rather than
// slow — enso spends ~19s thinking, so most of its budget was gone before it
// emitted anything.
//
// WHY A NUMBER AND NOT ZERO. Zero disables the deadline, and then a wedged
// plugin holds the connection forever. The honest shape here is an idle
// timeout — time BETWEEN frames — which the transport does not offer, so this
// stays a total-duration cap and is set to a duration no legitimate completion
// reaches. A frontier model streaming a large document takes single-digit
// minutes at worst; fifteen leaves room and still bounds a hang.
//
// This is registered before any plugin is mounted, because the transport is
// resolved at dial time from a process-global registry — a later registration
// would leave every already-dialed plugin on the 30s default.

import (
	"strings"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
	"github.com/zap-proto/zip"
)

// pluginResponseTimeout bounds a whole plugin response, streamed or not.
const pluginResponseTimeout = 15 * time.Minute

// pluginNetwork mirrors zip's own unexported networkOf: an address that names a
// path (or an abstract socket) is unix, everything else is tcp. It must agree
// with zip's, or a unix plugin socket would be dialed as tcp.
func pluginNetwork(addr string) string {
	if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "./") || strings.HasPrefix(addr, "@") {
		return "unix"
	}
	return "tcp"
}

// longDeadlineTransport is the ZAP wire with a response deadline a model
// completion can finish inside.
//
// BOTH halves are supplied, and that is not incidental: a zip.Transport may
// leave either nil, so replacing the scheme with a Dial-only value would
// silently remove the fleet's ability to SERVE zap — the host would build fine
// and fail to listen on its own default scheme.
func longDeadlineTransport() zip.Transport {
	return zip.Transport{
		Serve: func(addr string, h fasthttp.RequestHandler) zip.Server {
			return &zaphttp.Server{Network: pluginNetwork(addr), Addr: addr, Handler: h}
		},
		Dial: func(addr string) zip.Client {
			t := zaphttp.Dial(pluginNetwork(addr), addr)
			t.SetReadTimeout(pluginResponseTimeout)
			return t
		},
	}
}

// useLongPluginDeadline re-registers the ZAP scheme — the default transport for
// a bare address, which is what every plugin mount uses.
func useLongPluginDeadline() {
	zip.RegisterTransport("zap", longDeadlineTransport())
}
