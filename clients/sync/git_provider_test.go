package sync

import (
	"context"
	"testing"
)

// git_provider_test.go proves the git provider's resolve decision in isolation (no
// git object plane, no network): a source push drives an inbound advance only when
// the direction pulls and the repo matches; a manual run reconciles; anything else
// is a no-op. The I/O side of Reconcile is exercised by the git package's own tests
// + the webhook path — here we pin the DECISION.

func gitSync(direction string) Sync {
	return Sync{
		Kind: "git", Direction: direction, Actor: "hanzo-sync",
		Source: Endpoint{Provider: provGitHub, Locator: widgetsURL},
		Target: Endpoint{Provider: provNative, Locator: "widgets"},
	}
}

func srcPush(repo string) Event {
	return Event{Provider: provGitHub, Org: "acme", Locator: widgetsURL, Repo: repo, Ref: "refs/heads/main", After: "aaa", Actor: "octocat"}
}

func TestGitResolve(t *testing.T) {
	// both + matching source push → inbound advance.
	if a := resolve(gitSync(dirBoth), srcPush("widgets")); !a.do || !a.inbound {
		t.Fatalf("both+push want inbound, got %+v", a)
	}
	// pull also advances inbound.
	if a := resolve(gitSync(dirPull), srcPush("widgets")); !a.do || !a.inbound {
		t.Fatalf("pull+push want inbound, got %+v", a)
	}
	// push-only → inbound disabled (outbound is mirror_out's job).
	if a := resolve(gitSync(dirPush), srcPush("widgets")); a.do {
		t.Fatalf("push-only must not inbound-advance, got %+v", a)
	}
	// off → no-op.
	if a := resolve(gitSync(dirOff), srcPush("widgets")); a.do {
		t.Fatalf("off must not act, got %+v", a)
	}
	// non-matching repo → no-op.
	if a := resolve(gitSync(dirBoth), srcPush("other")); a.do {
		t.Fatalf("non-matching repo must not act, got %+v", a)
	}
	// event from a non-source provider (the native side) → no-op.
	ev := srcPush("widgets")
	ev.Provider = provNative
	if a := resolve(gitSync(dirBoth), ev); a.do {
		t.Fatalf("native-side event must not inbound-advance, got %+v", a)
	}
	// no branch tip → no-op.
	ev = srcPush("widgets")
	ev.After = ""
	if a := resolve(gitSync(dirBoth), ev); a.do {
		t.Fatalf("empty tip must not act, got %+v", a)
	}
	// manual run → reconcile (not inbound — a full converge toward direction).
	if a := resolve(gitSync(dirBoth), Event{Manual: true, Provider: provGitHub}); !a.do || a.inbound {
		t.Fatalf("manual want reconcile, got %+v", a)
	}
	// manual on an off sync → no-op.
	if a := resolve(gitSync(dirOff), Event{Manual: true}); a.do {
		t.Fatalf("manual on off must not act, got %+v", a)
	}
}

// TestGitTokenFallback pins the credential preference order: the event token wins;
// for github with no App connected (integrations unmounted here → InstallationToken
// errors) it falls back to the shared GIT_MIRROR_TOKEN, then to anonymous — NEVER a
// hard error (a public repo needs no credential, so the whole reconcile must not fail
// just because the App is absent). A non-github provider gets no minted token.
func TestGitTokenFallback(t *testing.T) {
	ctx := context.Background()

	// The event's own token always wins, regardless of provider/env.
	t.Setenv(gitMirrorTokenEnv, "mirror-tok")
	if tok, err := gitToken(ctx, provGitHub, "acme", "event-tok"); err != nil || tok != "event-tok" {
		t.Fatalf("event token must win, got (%q,%v)", tok, err)
	}

	// github + App not connected + GIT_MIRROR_TOKEN set → the shared mirror token.
	if tok, err := gitToken(ctx, provGitHub, "acme", ""); err != nil || tok != "mirror-tok" {
		t.Fatalf("github fallback want mirror-tok, got (%q,%v)", tok, err)
	}

	// github + App not connected + GIT_MIRROR_TOKEN unset → anonymous, NO error.
	t.Setenv(gitMirrorTokenEnv, "")
	if tok, err := gitToken(ctx, provGitHub, "acme", ""); err != nil || tok != "" {
		t.Fatalf("github no-cred want anonymous (\"\",nil), got (%q,%v)", tok, err)
	}

	// A non-github provider mints nothing here (event-carried creds only).
	t.Setenv(gitMirrorTokenEnv, "mirror-tok")
	if tok, err := gitToken(ctx, provGitLab, "acme", ""); err != nil || tok != "" {
		t.Fatalf("gitlab want no minted token, got (%q,%v)", tok, err)
	}
}
