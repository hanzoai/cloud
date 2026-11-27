package coding

import (
	"testing"

	"github.com/hanzoai/cloud/client"
)

// A RUN STARTED ON A PROJECT says so on its session, so the project's own list
// of runs (GET /v1/agent/sessions?project=) holds the run that was asked for in
// it; a run that names no project tags none.
func TestStart_ARunOnAProjectTagsItsSession(t *testing.T) {
	for _, tc := range []struct{ project, want string }{{"site", "site"}, {"Site", "site"}, {"", ""}} {
		r := newRig()
		r.mirror = false
		planPod(r)
		r.serve(t)
		r.startMode(t, client.CodingStartIn{Repo: "site", Prompt: "fix the header", Mode: "plan", Project: tc.project})
		r.mu.Lock()
		got := r.sessions.tagged().Project
		r.mu.Unlock()
		if got != tc.want {
			t.Fatalf("project %q: the session says project %q, want %q", tc.project, got, tc.want)
		}
	}
}
