package cloud

import (
	"context"
	"errors"
	"testing"
)

// TestOnServiceReleaseNoop proves the dispatch seam is a safe no-op when no
// releaser is registered (a binary without the paas control plane co-resident) —
// mirroring OnGitPush's contract.
func TestOnServiceReleaseNoop(t *testing.T) {
	RegisterServiceReleaser(nil)
	if ServiceReleaserRegistered() {
		t.Fatal("ServiceReleaserRegistered() = true with no releaser registered")
	}
	if err := OnServiceRelease(context.Background(), ServiceReleaseEvent{Service: "cloud", Image: "ghcr.io/hanzoai/cloud:v1.0.0"}); err != nil {
		t.Fatalf("OnServiceRelease with no releaser = %v, want nil no-op", err)
	}
}

// TestOnServiceReleaseDispatch proves a registered releaser receives the exact
// event and its error propagates — the one inversion point paas installs.
func TestOnServiceReleaseDispatch(t *testing.T) {
	var got ServiceReleaseEvent
	sentinel := errors.New("boom")
	RegisterServiceReleaser(func(_ context.Context, ev ServiceReleaseEvent) error {
		got = ev
		return sentinel
	})
	t.Cleanup(func() { RegisterServiceReleaser(nil) })

	if !ServiceReleaserRegistered() {
		t.Fatal("ServiceReleaserRegistered() = false after registration")
	}
	want := ServiceReleaseEvent{Service: "hanzo-app", Image: "ghcr.io/hanzoai/hanzo-app:v1.42.15", SHA: "abc1234"}
	if err := OnServiceRelease(context.Background(), want); !errors.Is(err, sentinel) {
		t.Fatalf("OnServiceRelease error = %v, want sentinel", err)
	}
	if got != want {
		t.Fatalf("releaser received %+v, want %+v", got, want)
	}
}
