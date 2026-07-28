package platform

import (
	"testing"

	"github.com/hanzoai/cloud"
)

// TestIsReleasePush pins which pushes publish a version. This predicate is the
// whole trigger: too loose and a feature branch or an unrelated repo cuts a
// release of the image the entire fleet runs.
func TestIsReleasePush(t *testing.T) {
	const sha = "65e8c5bd75ee0e1db5385caf5ea6541810bcfafa"
	for _, tc := range []struct {
		name string
		ev   cloud.GitPushEvent
		want bool
	}{
		{"merge to cloud main", cloud.GitPushEvent{
			Ref: "refs/heads/main", Commit: sha, CloneURL: "https://github.com/hanzoai/cloud"}, true},
		{"clone url with .git suffix", cloud.GitPushEvent{
			Ref: "refs/heads/main", Commit: sha, CloneURL: "https://github.com/hanzoai/cloud.git"}, true},
		{"clone url cased differently", cloud.GitPushEvent{
			Ref: "refs/heads/main", Commit: sha, CloneURL: "https://github.com/HanzoAI/Cloud.git"}, true},

		{"feature branch never releases", cloud.GitPushEvent{
			Ref: "refs/heads/rename/sign-to-esign", Commit: sha, CloneURL: "https://github.com/hanzoai/cloud"}, false},
		{"another repo's main never releases", cloud.GitPushEvent{
			Ref: "refs/heads/main", Commit: sha, CloneURL: "https://github.com/hanzoai/universe"}, false},
		{"a repo whose name merely contains cloud", cloud.GitPushEvent{
			Ref: "refs/heads/main", Commit: sha, CloneURL: "https://github.com/hanzoai/cloud-docs"}, false},
		{"an impostor host", cloud.GitPushEvent{
			Ref: "refs/heads/main", Commit: sha, CloneURL: "https://evil.example.com/hanzoai/cloud"}, false},
		{"no commit pinned", cloud.GitPushEvent{
			Ref: "refs/heads/main", Commit: "", CloneURL: "https://github.com/hanzoai/cloud"}, false},
		{"a tag is never a release push, even one named main", cloud.GitPushEvent{
			Ref: "refs/tags/main", Commit: sha, CloneURL: "https://github.com/hanzoai/cloud"}, false},
		{"empty event", cloud.GitPushEvent{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReleasePush(tc.ev); got != tc.want {
				t.Errorf("isReleasePush(ref=%q repo=%q) = %v, want %v",
					tc.ev.Ref, tc.ev.CloneURL, got, tc.want)
			}
		})
	}
}

// TestReleaseIsSingleFlight proves a second release cannot start while one is in
// flight. The version is computed from the tags that already exist, so two
// overlapping runs would compute the SAME version and race to publish it —
// exactly the phantom-tag failure the pipeline's ordering exists to prevent.
func TestReleaseIsSingleFlight(t *testing.T) {
	if releasing.Load() {
		t.Fatal("the guard is already set before the test starts")
	}
	if !releasing.CompareAndSwap(false, true) {
		t.Fatal("could not take the release guard")
	}
	t.Cleanup(func() { releasing.Store(false) })

	// launchRelease must refuse BEFORE doing any work — no commit resolution, no
	// version compute, no build job — so a concurrent trigger is cheap and safe.
	_, _, err := launchRelease(nil, t.Context(), "main", "", "")
	if err == nil {
		t.Fatal("a second release started while one was in flight")
	}

	// And the guard is still held: refusing must not clear another run's flag.
	if !releasing.Load() {
		t.Error("the refused call cleared the in-flight guard belonging to the running release")
	}
}

// TestReleaseGuardClearsWhenStartFails proves a failure to start releases the
// guard. Without this a single failed trigger would wedge releases permanently:
// every later push would be told one is already in flight, forever.
func TestReleaseGuardClearsWhenStartFails(t *testing.T) {
	if releasing.Load() {
		t.Fatal("the guard is already set before the test starts")
	}
	// A nil service cannot resolve a commit, so this exercises the early-exit path.
	if _, _, err := launchRelease(nil, t.Context(), "main", "", ""); err == nil {
		t.Fatal("expected the start to fail with no service")
	}
	if releasing.Load() {
		t.Error("the guard stayed set after a failed start; releases would be wedged forever")
	}
}
