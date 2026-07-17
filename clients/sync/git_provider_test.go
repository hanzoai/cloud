package sync

import "testing"

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
	return Event{Provider: provGitHub, Org: "acme", Locator: widgetsURL, Repo: repo, Branch: "main", After: "aaa", Actor: "octocat"}
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
