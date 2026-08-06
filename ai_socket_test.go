package cloud

import (
	"strings"
	"testing"
)

// A SIBLING REACHES `ai` THROUGH THE ROUTER ON LOOPBACK.
//
// Two addresses are wrong here and this pins both.
//
// The pod's own PUBLIC url sends a completion out through Cloudflare and back,
// and makes the pod mint an OAuth token to authenticate to its own deployment.
//
// The peer's PLANE SOCKET (zip.SocketPath("ai")) cannot serve it at all: that
// socket speaks ZAP, not HTTP, and carries the typed-op door rather than the
// app's routes — so a raw /v1 request there is first unintelligible and then, if
// framed correctly, a 404. Reaching it that way is what made @hanzo answer "the
// agent hit an error handling that" for every Slack turn.
//
// What is left is the router's own listener, entered on 127.0.0.1 so it never
// leaves the pod. Which address a process gets is decided by WHAT IT IS, never
// by configuration.
func TestSiblingReachesAIThroughTheRouterOnLoopback(t *testing.T) {
	sibling := &Config{Enable: []string{"agents"}, AIBaseURL: "https://api.hanzo.ai/v1", ListenAddr: ":8000"}

	via, base := aiRoute(sibling)
	if via != nil {
		t.Errorf("a sibling got a custom transport (%T) — an ordinary address is reached with the ordinary transport", via)
	}
	if base == "https://api.hanzo.ai/v1" {
		t.Error("a sibling addressed `ai` by the pod's OWN public URL — that leaves the host and comes back through the edge")
	}
	if want := "http://127.0.0.1:8000/v1"; base != want {
		t.Errorf("sibling base = %q, want the router on loopback %q", base, want)
	}
}

// The port is READ from the configured listener rather than assumed, or a
// deployment that moves its listener would send every completion to a closed port.
func TestSiblingFollowsTheConfiguredListenerPort(t *testing.T) {
	for _, listen := range []string{":9100", "0.0.0.0:9100", "127.0.0.1:9100"} {
		_, base := aiRoute(&Config{Enable: []string{"agents"}, ListenAddr: listen})
		if want := "http://127.0.0.1:9100/v1"; base != want {
			t.Errorf("ListenAddr %q → %q, want %q", listen, base, want)
		}
	}
}

// A sibling never dials the peer's plane socket. That door is zip's typed-op
// plane (plane.Ask), it does not speak HTTP, and the app's own routes are not on
// it — so naming it here can only ever produce an EOF or a 404.
func TestSiblingNeverDialsThePlaneSocket(t *testing.T) {
	_, base := aiRoute(&Config{Enable: []string{"agents"}, ListenAddr: ":8000"})
	if strings.Contains(base, ".sock") || strings.HasPrefix(base, "http://ai") {
		t.Errorf("sibling base = %q — that is the plane socket, which serves ops and not /v1", base)
	}
}

// The process that IS `ai` keeps the configured address: routing inference back
// through the picker there would be the process calling itself.
func TestTheAIProcessDoesNotDialItself(t *testing.T) {
	self := &Config{Enable: []string{"ai"}, AIBaseURL: "https://api.hanzo.ai/v1", ListenAddr: ":8000"}
	via, base := aiRoute(self)
	if via != nil {
		t.Error("the ai process got a custom transport — it would call itself")
	}
	if base != "https://api.hanzo.ai/v1" {
		t.Errorf("ai process base = %q, want its configured address", base)
	}
}

// The host carries every app, so it is not a sibling either.
func TestTheHostIsNotASibling(t *testing.T) {
	host := &Config{AIBaseURL: "https://api.hanzo.ai/v1", ListenAddr: ":8000"} // empty Enable = carries all
	_, base := aiRoute(host)
	if base != "https://api.hanzo.ai/v1" {
		t.Errorf("host base = %q, want its configured address", base)
	}
}
