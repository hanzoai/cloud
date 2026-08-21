package cloud

// Two facts about the COMMITTED build artifacts that no other test owns.
//
// Each app's own ui/embed_test.go already pins that app's mount contract — that
// its index.html was built for /<app>/, that the chunks call the right API
// prefix, that the handler serves and refuses the right things. This file does
// not repeat any of that. It asserts the two properties that are about the
// artifacts being COMMITTED SOURCE rather than about any one app's mount, and
// that are therefore homeless in a per-app test:
//
//  1. No dist file carries a git conflict marker. A merge once wrote <<<<<<<
//     into a committed index.html and it SHIPPED, past every gate, because
//     nothing in CI renders an SPA — a conflicted document is still 200 and
//     still text/html.
//  2. Every hashed asset index.html names is actually present. Vite content-
//     addresses its output, so a dist synced with a fresh build but a stale
//     index — or the reverse — names chunks that are not there. The existing
//     tests cannot see this: they check that a FABRICATED asset path 404s, and
//     a real-but-missing chunk 404s exactly the same way, into a <script> tag,
//     surfacing as "Unexpected token '<'" long after the deploy.
//
// Both are properties of the bytes in git, so they are checked against the bytes
// in git rather than through a handler.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// assetRefRE finds src="…" / href="…" in a built index.html.
var assetRefRE = regexp.MustCompile(`(?:src|href)="([^"]+)"`)

// conflictMarkers are the leaves a failed merge leaves behind. "=======" is
// deliberately absent — it is ordinary content in CSS and markdown.
var conflictMarkers = []string{"<<<<<<< ", ">>>>>>> "}

func distDirs(t *testing.T) []string {
	t.Helper()
	dists, err := filepath.Glob(filepath.Join("apps", "*", "ui", "dist"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dists) == 0 {
		t.Fatal("no embedded SPA dist found — this gate scanned nothing")
	}
	return dists
}

// TestNoConflictMarkersInShippedBundles reads every committed build artifact and
// refuses a merge leaf. This is the one that already got through.
func TestNoConflictMarkersInShippedBundles(t *testing.T) {
	scanned := 0
	for _, dist := range distDirs(t) {
		err := filepath.WalkDir(dist, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable subtree is not this test's business
			}
			switch filepath.Ext(p) {
			case ".html", ".js", ".mjs", ".css", ".json", ".svg":
			default:
				return nil
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			scanned++
			for _, marker := range conflictMarkers {
				if strings.Contains(string(raw), marker) {
					t.Errorf("%s contains a git conflict marker %q — a merge artifact was committed and would ship to users", p, strings.TrimSpace(marker))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dist, err)
		}
	}
	if scanned == 0 {
		t.Fatal("no build artifact scanned — the gate is not reaching the bundles")
	}
}

// TestShippedIndexNamesAssetsThatExist proves each index.html is in sync with
// the dist it ships beside: every hashed chunk it names is present.
//
// Content-addressed filenames make this a pure set comparison, so it needs to
// know nothing about where the app is mounted — which is exactly why it does not
// tread on the per-app base assertions in ui/embed_test.go.
func TestShippedIndexNamesAssetsThatExist(t *testing.T) {
	checked := 0
	for _, dist := range distDirs(t) {
		index := filepath.Join(dist, "index.html")
		raw, err := os.ReadFile(index)
		if err != nil {
			continue // an app may render its index rather than build one
		}
		for _, m := range assetRefRE.FindAllStringSubmatch(string(raw), -1) {
			ref := m[1]
			// Only same-origin hashed build output. External origins, data:
			// URIs and server-side template holes are not this app's chunks.
			if !strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "//") {
				continue
			}
			if i := strings.IndexAny(ref, "?#"); i >= 0 {
				ref = ref[:i]
			}
			if !strings.Contains(ref, "/assets/") {
				continue
			}
			name := filepath.Base(ref)
			if _, err := os.Stat(filepath.Join(dist, "assets", name)); err != nil {
				t.Errorf("%s names asset %q, which is not in %s/assets — index.html and its dist were synced from different builds", index, name, dist)
			}
			checked++
		}
	}
	if checked == 0 {
		// Every remaining bundle is self-contained — inline styles/scripts and no
		// hashed assets/ dir (apps/research ships a single-file ops board). That is
		// a real bundle the gate reached, not a gate scanning nothing, so it fails
		// only if some dist DOES ship a hashed assets/ dir whose references we never
		// checked — the actual index.html-vs-dist skew this gate exists to catch.
		for _, dist := range distDirs(t) {
			if _, err := os.Stat(filepath.Join(dist, "assets")); err == nil {
				t.Fatalf("%s ships an assets/ dir but its index.html named no hashed asset — index.html and its dist were synced from different builds", dist)
			}
		}
	}
}
