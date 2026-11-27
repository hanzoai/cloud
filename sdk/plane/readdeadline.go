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
// ReadCeiling must be LONGER than the longest legitimate caller budget, so
// that the caller's context is what expires. Today that is the chat bridge's
// 110s. Set to 15 minutes to match the host's own plugin-start budget — the
// deadline that already governs how long the fleet is willing to wait for a
// child — so there is one answer to "how long may an in-flight plane call take"
// rather than two.
//
// # A caller with a deadline is answered on that deadline
//
// The ceiling applies to a call that states no deadline. A call that states one
// waits exactly that long, longer or shorter than the ceiling. zaphttp alone
// reads min(ceiling, deadline), which cuts a long command — a sandbox_run on a
// run's hour budget — at the ceiling, and when the callee's own bound equals the
// ceiling the caller's read deadline fires first and reports an i/o timeout in
// place of the callee's answer. A command's wait is sized by the command.
//
// This is a CEILING, never a floor. Every caller still bounds itself with a
// context; a caller that gives itself ten seconds still gets ten seconds. What
// changes is that a caller can no longer be cut off BELOW its own budget by a
// default it never saw.

import (
	"context"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
	"github.com/zap-proto/zip"
)

// ReadCeiling bounds how long a plane call that states no deadline waits for its
// response: four hours, so an unattended call is bounded by its caller. A call that states one
// is answered on that, whatever it is, and every call a coding run makes does: its context
// carries the run's own deadline, at most the longest a session may live.
const ReadCeiling = 4 * time.Hour

func init() { widenPlaneReadDeadline() }

// widenPlaneReadDeadline registers Transport as the zap scheme.
//
// It runs at init because a transport must be registered before the first Dial,
// and plane is imported by every process that makes a plane call. Registering
// the same scheme twice is defined: RegisterTransport "adds (or replaces)".
func widenPlaneReadDeadline() { zip.RegisterTransport("zap", Transport()) }

// Transport is the zap scheme every process here dials and serves with.
//
// The stock Serve, verbatim (zip transport.go): re-registering a scheme replaces
// BOTH halves, so the serve side is restated or every plugin stops listening.
// The Dial half is sized: see sized.
func Transport() zip.Transport {
	return zip.Transport{
		Serve: func(addr string, h fasthttp.RequestHandler) zip.Server {
			return &zaphttp.Server{Network: Network(addr), Addr: addr, Handler: h}
		},
		Dial: func(addr string) zip.Client { return dial(addr, ReadCeiling) },
	}
}

// sized is a plane client whose response wait is the call's own deadline when
// it has one, and ceiling when it has none. zaphttp holds its read timeout per
// transport, so the two waits are two transports to the same address.
type sized struct {
	bounded *zaphttp.Transport // no read timeout: the context's deadline bounds the conn
	open    *zaphttp.Transport // ceiling: for a call with no deadline
}

func dial(addr string, ceiling time.Duration) *sized {
	b := zaphttp.Dial(Network(addr), addr)
	b.SetReadTimeout(0)
	o := zaphttp.Dial(Network(addr), addr)
	o.SetReadTimeout(ceiling)
	return &sized{bounded: b, open: o}
}

func (s *sized) Do(req *fasthttp.Request, resp *fasthttp.Response) error {
	return s.open.Do(req, resp)
}

func (s *sized) DoContext(ctx context.Context, req *fasthttp.Request, resp *fasthttp.Response) error {
	if _, ok := ctx.Deadline(); ok {
		return s.bounded.DoContext(ctx, req, resp)
	}
	return s.open.DoContext(ctx, req, resp)
}

// CloseIdleConnections releases both pools; zip calls it on Conn.Close.
func (s *sized) CloseIdleConnections() {
	s.bounded.CloseIdleConnections()
	s.open.CloseIdleConnections()
}

// Network mirrors zip's own rule (transport.go:125): a path is a unix socket,
// anything else is tcp. Copied rather than imported because zip keeps it
// unexported — and it is three lines, so the alternative is a fork.
func Network(addr string) string {
	if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "./") || strings.HasPrefix(addr, "@") {
		return "unix"
	}
	return "tcp"
}
