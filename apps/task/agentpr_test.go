package task

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
)

// mountAgentPR points the package singleton at a throwaway per-org OrgStore so
// CreateAgentPR exercises the real store path (get-or-create project + numbered
// insert), exactly as the HTTP createIssue handler does.
func mountAgentPR(t *testing.T) {
	t.Helper()
	prev := mounted
	mounted = &cloud.Service[state]{
		Log:   luxlog.New("test"),
		State: state{stores: cloud.NewOrgStore(cloud.Base{DataDir: t.TempDir()}, store, openStore)},
	}
	t.Cleanup(func() { mounted = prev })
}

func TestCreateAgentPR_KeyNumberingAndDiscriminators(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountAgentPR(t)
		ctx := context.Background()

		pr1, err := CreateAgentPR(ctx, AgentPRInput{
			Org: "acme", Repo: "api", Base: "main", Head: "agent/x1",
			Title: "fix null deref", Body: "body", Assignee: "hanzo",
		})
		if err != nil {
			t.Fatalf("create1: %v", err)
		}
		if pr1.Identifier != "API-1" || pr1.ProjectKey != "API" || pr1.Number != 1 {
			t.Fatalf("want API-1, got %+v", pr1)
		}
		pr2, err := CreateAgentPR(ctx, AgentPRInput{Org: "acme", Repo: "api", Head: "agent/x2", Title: "second"})
		if err != nil {
			t.Fatalf("create2: %v", err)
		}
		if pr2.Identifier != "API-2" {
			t.Fatalf("want API-2 (monotonic per project), got %q", pr2.Identifier)
		}

		// The rows carry the right immutable discriminators (Kind:pr, Source:agent).
		store, _ := mounted.State.stores.For(principal.MustOrgNamespace("acme", ""))
		rows, err := store.ListIssues(ctx, "acme", pr1ProjectID(t, store, ctx), IssueFilter{Repo: "api", Kind: "pr"})
		if err != nil || len(rows) != 2 {
			t.Fatalf("want 2 pr rows, got %d (%v)", len(rows), err)
		}
		for _, r := range rows {
			if r.Kind != "pr" || r.Source != "agent" || r.Repo != "api" {
				t.Fatalf("bad discriminators: %+v", r)
			}
		}
	})
}

// pr1ProjectID resolves the API project id for the assertion above.
func pr1ProjectID(t *testing.T, store *Store, ctx context.Context) string {
	t.Helper()
	p, err := store.GetProject(ctx, "acme", "API")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	return p.ID
}

func TestCreateAgentPR_TenantIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountAgentPR(t)
		ctx := context.Background()
		if _, err := CreateAgentPR(ctx, AgentPRInput{Org: "acme", Repo: "api", Head: "b1"}); err != nil {
			t.Fatalf("acme1: %v", err)
		}
		if _, err := CreateAgentPR(ctx, AgentPRInput{Org: "acme", Repo: "api", Head: "b2"}); err != nil {
			t.Fatalf("acme2: %v", err)
		}
		if _, err := CreateAgentPR(ctx, AgentPRInput{Org: "beta", Repo: "api", Head: "b1"}); err != nil {
			t.Fatalf("beta1: %v", err)
		}
		// acme sees only its own two rows; beta sees only its one — separate org DBs.
		acme, _ := mounted.State.stores.For(principal.MustOrgNamespace("acme", ""))
		ap, _ := acme.GetProject(ctx, "acme", "API")
		arows, _ := acme.ListIssues(ctx, "acme", ap.ID, IssueFilter{Source: "agent"})
		if len(arows) != 2 {
			t.Fatalf("acme should see 2 agent PRs, got %d", len(arows))
		}
		beta, _ := mounted.State.stores.For(principal.MustOrgNamespace("beta", ""))
		bp, _ := beta.GetProject(ctx, "beta", "API")
		brows, _ := beta.ListIssues(ctx, "beta", bp.ID, IssueFilter{Source: "agent"})
		if len(brows) != 1 {
			t.Fatalf("beta should see 1 agent PR, got %d", len(brows))
		}
	})
}

func TestCreateAgentPR_InvalidKeyFallsBackToAgent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountAgentPR(t)
		// repo "a" derives a 1-char key that fails keyRE -> falls back to "AGENT".
		pr, err := CreateAgentPR(context.Background(), AgentPRInput{Org: "acme", Repo: "a", Head: "x"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if pr.ProjectKey != "AGENT" || pr.Identifier != "AGENT-1" {
			t.Fatalf("want AGENT-1 fallback, got %+v", pr)
		}
	})
}

func TestCreateAgentPR_FailClosed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountAgentPR(t)
		if _, err := CreateAgentPR(context.Background(), AgentPRInput{Org: "", Repo: "api"}); err == nil {
			t.Fatal("empty org must fail")
		}
		if _, err := CreateAgentPR(context.Background(), AgentPRInput{Org: "acme", Repo: ""}); err == nil {
			t.Fatal("empty repo must fail")
		}
		// unmounted
		prev := mounted
		mounted = nil
		t.Cleanup(func() { mounted = prev })
		if _, err := CreateAgentPR(context.Background(), AgentPRInput{Org: "acme", Repo: "api"}); err == nil {
			t.Fatal("unmounted must fail closed")
		}
	})
}

// A work item filed for a request is filed once: a retry names the same request and is
// answered the row it already filed, in the same project; another request, or none, files a
// new one.
//
// MUTATION: insert without looking for the request and the replay files a second row.
func TestCreateAgentPR_OncePerRequest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountAgentPR(t)
		ctx := context.Background()
		in := AgentPRInput{Org: "acme", Repo: "api", Head: "agent/x1", Title: "fix", Request: "sub\x1fpull/sess_1\x1fp1"}
		first, err := CreateAgentPR(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		again, err := CreateAgentPR(ctx, in)
		if err != nil || again != first {
			t.Fatalf("the replay answered (%+v, %v), want the row it filed: %+v", again, err, first)
		}
		in.Request = "sub\x1fpull/sess_1\x1fp2"
		other, err := CreateAgentPR(ctx, in)
		if err != nil || other.Number != first.Number+1 {
			t.Fatalf("another request answered (%+v, %v), want a new row after %d", other, err, first.Number)
		}
		in.Request = ""
		a, _ := CreateAgentPR(ctx, in)
		b, _ := CreateAgentPR(ctx, in)
		if a.Number == b.Number || a.Number != other.Number+1 {
			t.Fatalf("no request filed rows %d and %d, want two new ones", a.Number, b.Number)
		}
		store, _ := mounted.State.stores.For(principal.MustOrgNamespace("acme", ""))
		rows, err := store.ListIssues(ctx, "acme", pr1ProjectID(t, store, ctx), IssueFilter{Repo: "api", Kind: "pr"})
		if err != nil || len(rows) != 4 {
			t.Fatalf("the project holds %d rows (%v), want 4", len(rows), err)
		}
	})
}
