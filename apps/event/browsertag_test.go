package event

import (
	"os"
	"regexp"
	"testing"
)

// A platform in BrowserTags with no injector in tag.js is a site told it is tracking
// and is not: the endpoint advertises the pixel, the tag fetches its id, and nothing
// fires.
// The map and the injectors sit in one package so this reads both from one place.
func TestEveryBrowserTagHasAnInjector(t *testing.T) {
	src, err := os.ReadFile("tag.js")
	if err != nil {
		t.Fatalf("read tag.js: %v", err)
	}
	injectors := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s{4}([a-z]+):\s*\{`).FindAllSubmatch(src, -1) {
		injectors[string(m[1])] = true
	}
	if len(injectors) == 0 {
		t.Fatal("parsed no injectors from tag.js — the pattern moved, and this gate is now blind")
	}
	for platform, typ := range BrowserTags {
		if !injectors[typ] {
			t.Errorf("BrowserTags advertises %q as type %q, and tag.js has no such injector", platform, typ)
		}
	}
}
