package coding

// RED 731 M1: there is no runtime guard for a sandbox whose dev cannot take the credential override.
// Measured: dev 0.6.94 (sandbox <= 1.3.8; hanzo-devnet pins 1.3.4) answers
// `Error: missing field `name`` and exits 1 on ANY `-c model_providers.hanzo.auth.*` (the override
// replaces the provider table instead of merging into it, as 0.7.x does), so every dev turn of a run
// in such a box dies before its first model call, with no fallback to the long-lived token it used
// to be given. This asserts what a guard would do: a box whose dev is older than the override is
// started without it and minted the long life.

import (
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
)

func TestRed731ABoxWhoseDevIsTooOldIsNotGivenTheOverride(t *testing.T) {
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+string(rune('0'+n))) }, life: 4 * 3600}
	iam.serve(t)
	newSessionsApp(t)
	holdClock(t)
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--version"):
			return client.Ran{Stdout: "dev 0.6.94\n"} // the sandbox image 1.3.4 .. 1.3.8
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	run := aRun()
	if _, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", run, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	argvs, _ := agentExecs(p)
	if len(argvs) == 0 {
		t.Fatal("the harness never ran")
	}
	if strings.Contains(strings.Join(argvs[0], " "), "model_providers.hanzo.auth") {
		t.Fatalf("dev 0.6.94 is handed -c model_providers.hanzo.auth.*, which it answers `missing field name` and exits 1:\n%s", strings.Join(argvs[0], " "))
	}
}
