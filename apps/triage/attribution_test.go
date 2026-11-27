package triage

import (
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/event"
)

// A FAULT'S CRASH SITE IS READ OFF ITS STACK: the platform's own frame nearest the
// crash, never a vendor or runtime frame, with the path made repository-relative.
func TestTheCrashSiteIsTheOwnFrameNearestTheCrash(t *testing.T) {
	site := attribute(event.Fault{
		Frames: []event.FaultFrame{
			{Function: "main", File: "webpack://_N_E/./src/app/page.tsx", Line: 3, Own: true},
			{Function: "render", File: "webpack://_N_E/./src/components/CheckoutButton.tsx?v=9", Line: 41, Column: 7, Own: true},
			{Function: "invoke", File: "webpack://_N_E/./node_modules/react-dom/index.js", Line: 900},
		},
		Attributes: map[string]string{"component_path": "main[app] > section[checkout] > button[CheckoutButton]"},
	})
	if site.File != "src/components/CheckoutButton.tsx" || site.Line != 41 || site.Column != 7 || site.Function != "render" {
		t.Fatalf("site = %+v", site)
	}
	if site.Component != "CheckoutButton" || site.at() != "src/components/CheckoutButton.tsx:41" {
		t.Fatalf("component %q at %q", site.Component, site.at())
	}
	if !strings.HasPrefix(site.Stack, "  invoke (src/../node_modules") && !strings.Contains(site.Stack, "* render (src/components/CheckoutButton.tsx:41)") {
		t.Fatalf("the stack does not mark the platform's own frames, crash site first: %q", site.Stack)
	}
}

// Go frames lose their module path and build root; a frame with no file is named by
// its function; no frames and a host for an origin say nothing of where.
func TestAGoFrameIsRepositoryRelative(t *testing.T) {
	site := attribute(event.Fault{Frames: []event.FaultFrame{
		{Function: "github.com/hanzoai/cloud/apps/agents.(*Ops).read", File: "/workspace/github.com/hanzoai/cloud/apps/agents/sessions.go", Line: 41, Own: true},
	}})
	if site.File != "apps/agents/sessions.go" || site.Line != 41 || site.Component != "sessions" {
		t.Fatalf("site = %+v", site)
	}
	if none := attribute(event.Fault{Origin: "app.hanzo.ai"}); none.File != "" || none.at() != "" {
		t.Fatalf("a host is no source location: %+v", none)
	}
	if origin := attribute(event.Fault{Origin: "apps/o11y/errorsink.go:175:8"}); origin.File != "apps/o11y/errorsink.go" || origin.Line != 175 || origin.Column != 8 {
		t.Fatalf("an origin that is a source location: %+v", origin)
	}
}

func TestAnOriginIsParsedWithoutSplittingItsScheme(t *testing.T) {
	for origin, want := range map[string][3]any{
		"https://app.hanzo.ai/src/a.tsx:12:3": {"https://app.hanzo.ai/src/a.tsx", uint32(12), uint32(3)},
		"src/a.ts:42":                         {"src/a.ts", uint32(42), uint32(0)},
		"https://app.hanzo.ai":                {"https://app.hanzo.ai", uint32(0), uint32(0)},
		"a.ts:x:1":                            {"a.ts:x", uint32(1), uint32(0)},
	} {
		file, line, col := parseOrigin(origin)
		if file != want[0] || line != want[1] || col != want[2] {
			t.Errorf("parseOrigin(%q) = %q %d %d, want %v", origin, file, line, col, want)
		}
	}
}

// THE ISSUE SAYS WHERE IT CRASHED, in the body and never in a way that closes the
// fence its stack sits in.
func TestTheIssueNamesTheCrashSite(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(false, false))
		f := cloudFault("a1a1a1a1a1a1a1a1", "/v1/x", time.Now())
		f.Frames = []event.FaultFrame{{Function: "render", File: "src/CheckoutButton.tsx", Line: 41, Own: true}, {Function: "x```y", File: "src/a.ts", Line: 1, Own: true}}
		for range 3 {
			if err := l.observe(t.Context(), f); err != nil {
				t.Fatal(err)
			}
		}
		body := l.w.issues[41].Body
		for _, want := range []string{"crash site `src/a.ts:1`", "(component `a`)", "* render (src/CheckoutButton.tsx:41)"} {
			if !strings.Contains(body, want) {
				t.Errorf("the issue does not say %q:\n%s", want, body)
			}
		}
		if strings.Count(body, "```") != 2 {
			t.Fatalf("a stack frame closed its own fence:\n%s", body)
		}
	})
}
