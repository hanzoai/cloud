package coding

// RED 731 L2: a python or node run makes no model call and needs no credential, but key() still mints one
// for it and bearer.dies() holds the run to that token's four hours: a ten-hour data job the caller asked
// for is ended at ~3h55m with "ran out of time".

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
)

func TestRed731AnInterpreterRunIsNotHeldToACredentialItNeverSpends(t *testing.T) {
	for _, tool := range []string{"python", "node"} {
		t.Run(tool, func(t *testing.T) {
			iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+string(rune('0'+n))) }, life: 4 * 3600}
			iam.serve(t)
			newSessionsApp(t)
			holdClock(t)
			p := &pod{answer: func(argv []string) client.Ran {
				if slices.Contains(argv, "rev-parse") {
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}}
			servePod(t, p)
			ctx, cancel := context.WithTimeout(t.Context(), 24*time.Hour)
			defer cancel()
			run := aRun()
			run.Tool = tool
			run.RunTimeoutSeconds = 10 * 3600
			if _, err := (sandboxRunner{}).Run(ctx, "acme", "alice", run, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			p.mu.Lock()
			most := -1
			for i, argv := range p.ran {
				if slices.Contains(argv, "python3") || slices.Contains(argv, "node") {
					most = p.timeouts[i]
				}
			}
			p.mu.Unlock()
			if most < 9*3600 {
				t.Fatalf("the %s program was given %ds of the 10h it asked for: it was held to a model credential it never spends", tool, most)
			}
		})
	}
}

func TestInterpreterRunDoesNotPrependEnvironmentBriefToSource(t *testing.T) {
	for _, tool := range []string{"python", "node"} {
		t.Run(tool, func(t *testing.T) {
			newSessionsApp(t)
			holdClock(t)
			var gotStdin string
			p := &pod{answer: func(argv []string) client.Ran {
				if slices.Contains(argv, "rev-parse") {
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}}
			servePod(t, p)
			run := aRun()
			run.Tool = tool
			run.Prompt = "print('ok')\n"
			run.Prepared = Prepared{Missing: []string{"API_SECRET"}}
			if _, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", run, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			p.mu.Lock()
			for i, argv := range p.ran {
				if slices.Contains(argv, "python3") || slices.Contains(argv, "node") {
					gotStdin = p.fed[i]
				}
			}
			p.mu.Unlock()
			wantSuffix := "\n" + run.Prompt
			if !strings.HasSuffix(gotStdin, wantSuffix) {
				t.Fatalf("interpreter stdin = %q, want prompt verbatim without prose brief", gotStdin)
			}
			if strings.Contains(gotStdin, "environment variables are named") || strings.Contains(gotStdin, "install script") {
				t.Fatalf("environment prose brief leaked into %s source stdin: %q", tool, gotStdin)
			}
		})
	}
}
