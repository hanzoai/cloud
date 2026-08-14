package forge_test

// live_test.go exercises this client against a REAL forge.
//
// It is SKIPPED unless FORGE_LIVE_TOKEN and FORGE_LIVE_ORG are set, so it never
// runs in CI and never needs a credential to be available there. It exists
// because the stub in forge_test.go encodes behaviours — Sudo drops privilege,
// an unknown actor is 404, issues-search answers labels and milestones inline —
// that are ASSERTIONS ABOUT AN UPSTREAM WE DO NOT CONTROL. A stub can only ever
// confirm we implemented what we believed; this confirms what we believed is
// true, and it is the thing to re-run when Forgejo is upgraded.
//
//	FORGE_LIVE_TOKEN=… FORGE_LIVE_ORG=hanzoai FORGE_LIVE_ACTOR=z \
//	  go test -run Live -v ./forge/
//
// The token is read from the ENVIRONMENT and never from a file in the repo. In
// production the same credential comes from KMS (apps/todo/source.go).

import (
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/forge"
)

func liveClient(t *testing.T) (*forge.Client, string) {
	t.Helper()
	token := strings.TrimSpace(os.Getenv("FORGE_LIVE_TOKEN"))
	org := strings.TrimSpace(os.Getenv("FORGE_LIVE_ORG"))
	if token == "" || org == "" {
		t.Skip("set FORGE_LIVE_TOKEN and FORGE_LIVE_ORG to run the live forge checks")
	}
	host := strings.TrimSpace(os.Getenv("FORGE_LIVE_HOST"))
	if host == "" {
		host = "git.hanzo.ai"
	}
	actor := strings.TrimSpace(os.Getenv("FORGE_LIVE_ACTOR"))
	if actor == "" {
		t.Skip("set FORGE_LIVE_ACTOR to the forge login to act as")
	}
	c, err := forge.New(host, token)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c.As(actor), org
}

// The issues search really does answer labels, milestone and the owning
// repository inline — which is what lets a board render from one request.
func TestLive_IssuesSearchCarriesTheBoardInline(t *testing.T) {
	c, org := liveClient(t)

	issues, err := c.Issues(t.Context(), org, forge.IssueFilter{State: "all", Limit: 20})
	if err != nil {
		t.Fatalf("Issues(%s): %v", org, err)
	}
	t.Logf("live: %d issues returned for %s", len(issues), org)
	if len(issues) == 0 {
		t.Skip("no issues on this org to inspect")
	}
	withRepo := 0
	for _, is := range issues {
		if is.Repository != nil && is.Repository.Name != "" {
			withRepo++
		}
	}
	if withRepo != len(issues) {
		t.Errorf("%d/%d issues named their repository; the board cannot address the rest",
			withRepo, len(issues))
	}
	is := issues[0]
	t.Logf("  sample: %s#%d %q state=%s labels=%d assignees=%d",
		func() string {
			if is.Repository != nil {
				return is.Repository.Name
			}
			return "?"
		}(), is.Number, is.Title, is.State, len(is.Labels), len(is.Assignees))
}

// THE SAFETY PROPERTY, against the real forge: Sudo DROPS PRIVILEGE. Reading a
// repository the actor cannot see must fail even though the machine token can
// see it. Requires FORGE_LIVE_PRIVATE (an "org/repo" the actor may NOT read).
func TestLive_SudoDropsPrivilege(t *testing.T) {
	c, _ := liveClient(t)
	target := strings.TrimSpace(os.Getenv("FORGE_LIVE_PRIVATE"))
	if target == "" {
		t.Skip("set FORGE_LIVE_PRIVATE=org/repo (one the actor may NOT read) to check privilege drop")
	}
	parts := strings.SplitN(target, "/", 2)
	if len(parts) != 2 {
		t.Fatalf("FORGE_LIVE_PRIVATE = %q, want org/repo", target)
	}
	got, err := c.Repo(t.Context(), parts[0], parts[1])
	if err == nil && got.FullName != "" {
		t.Fatalf("the sudoed actor read %s — Sudo did not drop privilege, "+
			"and the machine credential is therefore the only thing standing between "+
			"one tenant and another", got.FullName)
	}
	t.Logf("live: sudoed actor correctly cannot read %s (err=%v)", target, err)
}
