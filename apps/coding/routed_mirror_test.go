package coding

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// A routed run on a repository that mirrors GitHub is refused with the way out: the
// message names where the repository lives and says to name that as the repository.
// It reads as neither "no org on the call" nor a bare statement of fact.
//
// MUTATION: put back the old refusal and the message names no github.com address.
func TestGrantRouted_AMirrorOfGitHubSaysWhatToNameInstead(t *testing.T) {
	r := newRig()
	r.serve(t)

	proj := zip.New(zip.Config{AppName: "project", DisableStartupMessage: true})
	proj.Post("/projects/workspace", func(context.Context, *client.WorkspaceIn) (*client.Workspace, error) {
		return &client.Workspace{Owner: "acme", Made: true}, nil
	}, zip.WithOperationID(client.ProjectsWorkspace))
	plane.Bind()
	go func() { _ = proj.Listen(zip.SocketPath("project")) }()
	t.Cleanup(func() { _ = proj.Shutdown() })
	waitListening(t, "project")

	_, _, err := Dispatcher{}.grantRouted(context.Background(), agents.RoutedRun{Org: "acme", Repo: "site", SessionID: "sess_m", Made: true})
	if err == nil {
		t.Fatal("a run on a mirror of GitHub was granted a key on the forge")
	}
	want := "acme/site mirrors github.com/hanzo-apps/site: to run it on a machine, name github.com/hanzo-apps/site as the repository"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("refusal %q, want it to contain %q", err, want)
	}
	if strings.Contains(err.Error(), "no org on the call") {
		t.Fatalf("a mirror was refused for the wrong reason: %v", err)
	}
}
