package sandbox

// screen.go — a sandbox that HAS a display, seen.
//
// Three addresses, the terminal's three exactly:
//
//	POST  /:id/screen/ticket   the credential
//	GET   /:id/screen          the desktop, as a page
//	GET   /:id/screen/ws       the desktop, as a socket
//
// and almost nothing here is new, which is the point. The ticket is the same
// ticket, the socket is the same bridge, the page is served the same way and
// framed by the same brands. What differs is two things: the bytes on the wire
// are RFB rather than a pty's, and the client is noVNC rather than xterm.
//
// THE PIXELS COME OUT THROUGH THE EXEC CHANNEL. The desktop image serves RFB on
// 127.0.0.1:5900 and deliberately binds nothing else — a screen reachable from
// the pod network is a screen whose only defence is a NetworkPolicy — so there
// is no address for cloud to dial. What there is, and the only thing there is,
// is the same Kubernetes exec subresource every other call into a sandbox uses:
// `socat` joins stdin and stdout to that loopback port and the stream becomes
// the transport. One way in, one thing to authorize, nothing new exposed.
//
// The pod also runs websockify on 6080 with noVNC's own copy of this client, and
// that is NOT what this uses. It listens on loopback too, so reaching it would
// need the same socat and gain a second web server, a second copy of noVNC and a
// second version to keep in step with this one — to arrive at the same pixels.

import (
	"context"
	_ "embed"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// rfb is where a desktop's VNC server listens inside its own pod, and it is
// written once because two places read it: the pod spec declares the port, and
// the stream dials it. The image's own default (SANDBOX_VNC_PORT) is the same
// number for the same reason — it is the registered RFB port.
const rfb = 5900

//go:embed screen/page.html
var screenHTML string

//go:embed screen/rfb.js
var rfbJS string

// desktop is the assembled page, built once: noVNC substituted into its marker,
// the same way xterm is (page.go). Inline, for the same reason — a desktop that
// fetches its client from somewhere else is a desktop that stops working when
// the somewhere else does.
var desktop = sync.OnceValue(func() string {
	return strings.Replace(screenHTML, "__RFB__", rfbJS, 1)
})

// watch serves one screen: RFB from the sandbox's display, as long as somebody
// is looking. The window is ignored — a browser pane's size is not the X
// server's, and the page scales what it is given rather than asking a server
// with no RandR to resize itself.
func watch(s *Service, c *zip.Ctx) error {
	return attach(s, c, func(ctx context.Context, m Sandbox, in *pipe, out *frames, _ *window) error {
		return s.State.rt.screen(ctx, m, in, out)
	})
}

// screen registers the three routes, beside the terminal's three. One function
// and not three lines in Routes, so that what a screen needs — a credential, a
// page and a socket — cannot be half registered.
//
// It is registered for EVERY class and not only for `desktop`, because the
// class is a fact about the image and the failure is already exact: a sandbox
// with no VNC server refuses the socat connection and the page says the
// connection failed. A check here would be a second opinion about what is
// running inside a pod, formed from a label rather than from the pod.
func screen(g zip.Router, s *Service) {
	g.Get("/:id/screen", cloud.Handle(s, serve(desktop)))
	g.Get("/:id/screen/ws", cloud.Handle(s, watch))
}
