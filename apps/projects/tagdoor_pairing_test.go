package projects

import (
	"os"
	"regexp"
	"testing"
)

// Every platform this door advertises must have an injector in the hosted tag, or
// the tag fetches a pixel id and silently drops it — the site is told it is tracking
// and is not. The two halves live in different packages (the config here, the
// injector in apps/event/tag.js) precisely because one is data and one is code,
// so nothing but a test can hold them together.
func TestEveryAdvertisedTagHasAnInjector(t *testing.T) {
	src, err := os.ReadFile("../event/tag.js")
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
	for platform, typ := range browserTags {
		if !injectors[typ] {
			t.Errorf("browserTags advertises %q as type %q, and tag.js has no such injector", platform, typ)
		}
	}
}
