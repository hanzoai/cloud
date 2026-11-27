package manifest

import "testing"

// TestCrawlOwnsTheBotKeyDirectory: the Web Bot Auth key directory reaches crawl,
// the app that signs agent fetches and serves the keys (hanzo-inc/cloud#423). A
// well-known address no row names is answered by the console's catch-all as a
// 404, which a verifier caches as "this bot publishes no keys".
func TestCrawlOwnsTheBotKeyDirectory(t *testing.T) {
	const path = "/.well-known/http-message-signatures-directory"
	if got := OwnerOf(path); got != "crawl" {
		t.Fatalf("%s routes to %q, want crawl", path, got)
	}
}
