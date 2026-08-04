package cloud

import "testing"

// A SIBLING REACHES `ai` OVER ITS SOCKET, NOT THROUGH THE INTERNET.
//
// `ai` is a plugin of this same binary running as its own process. Addressing it
// by its public URL sent a completion out through Cloudflare and back, and made
// the pod mint an OAuth token to authenticate to its own deployment. Which
// transport a process gets is decided by WHAT IT IS, never by configuration.
func TestSiblingReachesAIOverItsSocket(t *testing.T) {
	sibling := &Config{Enable: []string{"agents"}, AIBaseURL: "https://api.hanzo.ai/v1"}

	via, base := aiRoute(sibling)
	if via == nil {
		t.Error("a sibling took the default transport — it would leave the host to reach a peer")
	}
	if base == "https://api.hanzo.ai/v1" {
		t.Error("a sibling addressed `ai` by the pod's OWN public URL")
	}
	if base != aiPeerURL {
		t.Errorf("sibling base = %q, want the named peer %q", base, aiPeerURL)
	}
	srt, ok := via.(*socketRoundTripper)
	if !ok {
		t.Fatalf("transport is %T, want the socket one", via)
	}
	if srt.app != aiApp {
		t.Errorf("socket targets %q, want %q — the peer is NAMED, never addressed", srt.app, aiApp)
	}
}

// The process that IS `ai` keeps the configured address: routing inference back
// through the picker there would be the process calling itself.
func TestTheAIProcessDoesNotDialItself(t *testing.T) {
	self := &Config{Enable: []string{"ai"}, AIBaseURL: "https://api.hanzo.ai/v1"}
	via, base := aiRoute(self)
	if via != nil {
		t.Error("the ai process resolved itself to its own socket — it would call itself")
	}
	if base != "https://api.hanzo.ai/v1" {
		t.Errorf("ai process base = %q, want its configured address", base)
	}
}

// The host carries every app, so it is not a sibling either.
func TestTheHostIsNotASibling(t *testing.T) {
	host := &Config{AIBaseURL: "https://api.hanzo.ai/v1"} // empty Enable = carries all
	if via, _ := aiRoute(host); via != nil {
		t.Error("the host took the sibling path while carrying `ai` itself")
	}
}
