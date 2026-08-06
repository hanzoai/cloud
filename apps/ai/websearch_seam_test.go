// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"context"
	"os"
	"strings"
	"testing"

	webtools "github.com/hanzoai/ai/agent/builtin_tool/web"
	"github.com/hanzoai/cloud/apps/websearch"
)

// THE SEAM IS A LINE OF CODE NOTHING ELSE DEPENDS ON, WHICH IS EXACTLY THE KIND
// THAT GETS DELETED.
//
// ai's builtin registry DECLARES web_search unconditionally and holds no backend
// for it; this host installs one. Remove the install and nothing fails to compile,
// no route 404s, and no test goes red — the tool simply starts answering "not
// available in this deployment" to every agent, forever. That is the same shape as
// every other "wired but never consulted" defect in this tree.
func TestInstallWebSearchClosesTheSeam(t *testing.T) {
	t.Cleanup(func() { webtools.SetSearch(nil) })

	webtools.SetSearch(nil)
	if webtools.Search() != nil {
		t.Fatal("precondition: the seam should start empty")
	}

	installWebSearch(func(context.Context, string, string) []websearch.Result { return nil })
	if webtools.Search() == nil {
		t.Fatal("installWebSearch left the seam empty — every agent's web_search would " +
			"report the capability as unavailable")
	}
}

// The field mapping is the whole adapter, and it is a RENAME waiting to happen:
// websearch.Result calls the body Content, the tool contract calls it Snippet. Lose
// that and every agent gets results with empty snippets — which a model reads as
// "the web had nothing to say about this", not as a bug.
func TestInstallWebSearchMapsEveryFieldTheToolPublishes(t *testing.T) {
	t.Cleanup(func() { webtools.SetSearch(nil) })

	installWebSearch(func(context.Context, string, string) []websearch.Result {
		return []websearch.Result{{
			URL:     "https://example.com/a",
			Title:   "A title",
			Content: "the snippet body",
			Engine:  "duckduckgo",
		}}
	})

	got, err := webtools.Search()(context.Background(), "q", 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	if got[0].Title != "A title" {
		t.Errorf("Title = %q", got[0].Title)
	}
	if got[0].URL != "https://example.com/a" {
		t.Errorf("URL = %q", got[0].URL)
	}
	if got[0].Snippet != "the snippet body" {
		t.Errorf("Snippet = %q, want the Content field — a model reads an empty snippet "+
			"as the web having nothing to say", got[0].Snippet)
	}
}

// The limit is HONOURED. The tool clamps its own maximum before it reaches here, so
// this only has to not ignore it — but ignoring it would hand an agent ten results
// when it asked for two, and context windows are the budget being spent.
func TestInstallWebSearchHonoursTheLimit(t *testing.T) {
	t.Cleanup(func() { webtools.SetSearch(nil) })

	many := make([]websearch.Result, 10)
	for i := range many {
		many[i] = websearch.Result{URL: "https://e.com/", Title: "t"}
	}
	installWebSearch(func(context.Context, string, string) []websearch.Result { return many })

	for _, tc := range []struct{ limit, want int }{
		{2, 2},
		{0, 10},  // 0 means "unset" — do not silently truncate to nothing
		{-1, 10}, // nor does a negative
		{99, 10}, // asking for more than exists yields what exists, not an error
	} {
		got, err := webtools.Search()(context.Background(), "q", tc.limit)
		if err != nil {
			t.Fatalf("limit %d: %v", tc.limit, err)
		}
		if len(got) != tc.want {
			t.Errorf("limit %d returned %d results, want %d", tc.limit, len(got), tc.want)
		}
	}
}

// An empty upstream result is an empty SLICE and not an error. The distinction is
// the one the tool layer is built on: an error means the capability is unavailable
// and a model should say so, while an empty list is a genuine finding.
func TestInstallWebSearchReturnsEmptyNotError(t *testing.T) {
	t.Cleanup(func() { webtools.SetSearch(nil) })

	installWebSearch(func(context.Context, string, string) []websearch.Result { return nil })
	got, err := webtools.Search()(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("an empty result set surfaced as an error: %v — an agent would report the "+
			"capability broken rather than the search empty", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d results from an empty upstream", len(got))
	}
}

// And Mount must still CALL it. This half is a source assertion and cannot be
// anything better here: Mount needs a real zip.App and cloud.Deps (it runs ai's
// Bootstrap), so no test in this package constructs one. It pins the call site
// against deletion; the behaviour above is what pins the adapter.
func TestMountInstallsTheWebSearchSeam(t *testing.T) {
	src, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}
	if !strings.Contains(string(src), "installWebSearch(websearch.Search)") {
		t.Error("Mount no longer installs the web-search backend — ai declares web_search " +
			"either way, so every agent would be told the capability is unavailable and " +
			"nothing else would report it")
	}
}
