package tools

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// status is the HTTP status a plane op refused with, or 0.
func status(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// ownRows lists each org its own rows, as functions, agents and an org's servers
// do: the same name in two orgs is two tools.
type ownRows struct{ byOrg map[string][]string }

func (ownRows) Source() Source { return SourceFunction }
func (p ownRows) List(_ context.Context, s Scope) ([]Tool, error) {
	var out []Tool
	for _, n := range p.byOrg[s.Org] {
		out = append(out, tool(n, SourceFunction))
	}
	return out, nil
}
func (ownRows) Dispatch(context.Context, Principal, string, map[string]any) (any, error) {
	return nil, nil
}

// Ownership is answered as the org on the call, from the stores that know: a
// skill it authored, a server it registered by hand, a tool one of its own rows
// resolves — and another org's thing reads exactly as a thing that does not exist.
// Resolving is not owning: a connector action every org reaches is the platform's,
// for every org alike, and a server the public shelf lists is nobody's, whether it
// was enabled off the shelf or its URL typed in by hand, under any spelling of its
// host a resolver reads the same — while an org's own server on a host the shelf
// lists others' servers from is its own.
func TestOwnedIsTheCallersOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t, nil)
		std.Register(&fakeProvider{src: SourceConnector, tools: []Tool{tool("slack_send", SourceConnector)}})
		std.Register(ownRows{byOrg: map[string][]string{"acme": {"function_summarize"}, "globex": {"function_summarize"}}})
		if r := do(t, app, http.MethodPost, "/v1/tool/skills", "acme", map[string]any{"name": "triage", "content": "# Triage"}); r.Code != 201 {
			t.Fatalf("author skill = %d %s", r.Code, r.Body)
		}
		// The public shelf lists two hosted servers.
		tx, err := mounted.State.catalog.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range []MCPListing{
			{ID: "io.github.github-mcp-server", Name: "github-mcp-server", Vendor: "github", Remotes: []MCPRemote{{Transport: "streamable-http", URL: "https://api.githubcopilot.com/mcp/"}}},
			{ID: "com.stripe.mcp", Name: "stripe", Vendor: "stripe", Remotes: []MCPRemote{{Transport: "streamable-http", URL: "https://mcp.stripe.com"}}},
			{ID: "ai.smithery.weather", Name: "weather", Vendor: "someone", Remotes: []MCPRemote{{Transport: "streamable-http", URL: "https://server.smithery.ai/@someone/weather/mcp"}}},
			{ID: "com.example.tenant", Name: "tenant", Vendor: "example", Remotes: []MCPRemote{{Transport: "streamable-http", URL: "https://mcp.tenant.example/{tenant_id}/mcp"}}},
			{ID: "org.example.region", Name: "region", Vendor: "example", Remotes: []MCPRemote{{Transport: "streamable-http", URL: "https://{region}.api.example.org/mcp"}}},
			{ID: "com.snowflake.account", Name: "account", Vendor: "snowflake", Remotes: []MCPRemote{{Transport: "streamable-http", URL: "https://{org}-{account}.snowflakecomputing.com/api/v2/mcp"}}},
			// A template that names no domain of its own claims nobody's server.
			{ID: "io.github.someone.selfhost", Name: "selfhost", Vendor: "someone", Remotes: []MCPRemote{
				{Transport: "streamable-http", URL: "https://mcp.{company}.com/mcp"}, {Transport: "streamable-http", URL: "https://{a}.{b}/mcp"}}},
		} {
			if err := mounted.State.catalog.put(context.Background(), tx, l); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		for _, srv := range []MCPServer{
			{ID: "srv_search", Org: "acme", Name: "search", URL: "https://mcp.example.com/rpc"},
			{ID: "srv_github", Org: "acme", Name: "github", URL: "https://api.githubcopilot.com/mcp/", Listing: "io.github.github-mcp-server"},
			// The shelf's servers, their URLs typed in by hand.
			{ID: "srv_stripe", Org: "acme", Name: "stripe", URL: "https://MCP.Stripe.com/"},
			{ID: "srv_copilot", Org: "acme", Name: "copilot", URL: "https://api.githubcopilot.com:443/other"},
			{ID: "srv_dot", Org: "acme", Name: "dot", URL: "https://mcp.stripe.com./"},
			{ID: "srv_dotport", Org: "acme", Name: "dotport", URL: "https://MCP.STRIPE.COM.:443/mcp"},
			{ID: "srv_ideo", Org: "acme", Name: "ideo", URL: "https://mcp。stripe。com/"},
			{ID: "srv_weather", Org: "acme", Name: "weather", URL: "https://Server.Smithery.ai/@SomeOne/weather/mcp"},
			{ID: "srv_climb", Org: "acme", Name: "climb", URL: "https://server.smithery.ai/@acme/../@someone/weather/mcp"},
			// Instances of the shelf's templated remotes.
			{ID: "srv_tenant", Org: "acme", Name: "tenant", URL: "https://mcp.tenant.example/acme/mcp"},
			{ID: "srv_region", Org: "acme", Name: "region", URL: "https://US.api.example.org/mcp"},
			{ID: "srv_account", Org: "acme", Name: "account", URL: "https://acme-prod.snowflakecomputing.com/api/v2/mcp"},
			// Its own servers, of the shapes a domainless template spells.
			{ID: "srv_company", Org: "acme", Name: "company", URL: "https://mcp.acme.com/mcp"},
			{ID: "srv_dev", Org: "acme", Name: "dev", URL: "https://acme.dev/tools/mcp"},
			// A host the shelf does not list, though a listed one ends with it.
			{ID: "srv_mine", Org: "acme", Name: "mine", URL: "https://stripe.com.acme.dev/mcp"},
			// Its own server on a host the shelf lists another's from.
			{ID: "srv_ledger", Org: "acme", Name: "ledger", URL: "https://server.smithery.ai/@acme/ledger/mcp"},
		} {
			if _, err := mounted.State.servers.Write(context.Background(), srv, true); err != nil {
				t.Fatal(err)
			}
		}
		ask := func(org, kind, ref string) *client.Owned {
			t.Helper()
			out, err := planeOwned(cloud.For(context.Background(), org), &client.OwnedIn{Kind: kind, Ref: ref})
			if err != nil {
				t.Fatalf("owned %s %s as %s: %v", kind, ref, org, err)
			}
			return out
		}
		for _, tc := range []struct {
			org, kind, ref string
			mine, platform bool
			id             string
		}{
			{"acme", client.OwnedSkill, "triage", true, false, "triage"},
			{"globex", client.OwnedSkill, "triage", false, false, ""},
			{"acme", client.OwnedMCP, "search", true, false, "srv_search"},
			{"acme", client.OwnedMCP, "srv_search", true, false, "srv_search"},
			{"globex", client.OwnedMCP, "srv_search", false, false, ""},
			{"acme", client.OwnedMCP, "github", false, false, ""},
			{"acme", client.OwnedMCP, "stripe", false, false, ""},
			{"acme", client.OwnedMCP, "srv_copilot", false, false, ""},
			{"acme", client.OwnedMCP, "dot", false, false, ""},
			{"acme", client.OwnedMCP, "dotport", false, false, ""},
			{"acme", client.OwnedMCP, "ideo", false, false, ""},
			{"acme", client.OwnedMCP, "weather", false, false, ""},
			{"acme", client.OwnedMCP, "climb", false, false, ""},
			{"acme", client.OwnedMCP, "tenant", false, false, ""},
			{"acme", client.OwnedMCP, "region", false, false, ""},
			{"acme", client.OwnedMCP, "account", false, false, ""},
			{"acme", client.OwnedMCP, "company", true, false, "srv_company"},
			{"acme", client.OwnedMCP, "dev", true, false, "srv_dev"},
			{"acme", client.OwnedMCP, "mine", true, false, "srv_mine"},
			{"acme", client.OwnedMCP, "ledger", true, false, "srv_ledger"},
			{"acme", client.OwnedTool, "function_summarize", true, false, "function_summarize"},
			{"globex", client.OwnedTool, "function_summarize", true, false, "function_summarize"},
			{"acme", client.OwnedTool, "slack_send", false, true, "slack_send"},
			{"globex", client.OwnedTool, "slack_send", false, true, "slack_send"},
			{"acme", client.OwnedTool, "acme_ghost", false, false, ""},
		} {
			if got := ask(tc.org, tc.kind, tc.ref); got.Mine != tc.mine || got.Platform != tc.platform || got.ID != tc.id {
				t.Errorf("%s owns %s %s = %+v, want mine=%v platform=%v id=%q", tc.org, tc.kind, tc.ref, got, tc.mine, tc.platform, tc.id)
			}
		}
		if got := ask("acme", client.OwnedTool, "slack_send"); got.Source != string(SourceConnector) {
			t.Errorf("a connector action answers source %q", got.Source)
		}
		if _, err := planeOwned(cloud.For(context.Background(), "acme"), &client.OwnedIn{Kind: "planet", Ref: "x"}); status(err) != http.StatusBadRequest {
			t.Fatalf("an unknown kind = %v, want 400", err)
		}
		if _, err := planeOwned(cloud.For(context.Background(), ""), &client.OwnedIn{Kind: client.OwnedSkill, Ref: "triage"}); status(err) != http.StatusForbidden {
			t.Fatalf("no org on the call = %v, want 403", err)
		}
	})
}

// Listing and activation over the plane are GET /v1/tool and the activation
// write, for the org on the call and in the project it names.
func TestListAndActivateOverThePlane(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newApp(t, nil)
		std.Register(&fakeProvider{src: SourceConnector, tools: []Tool{tool("acme_hello", SourceConnector)}})
		acme := cloud.For(context.Background(), "acme")
		on := func(org context.Context, project string) []string {
			t.Helper()
			set, err := planeList(org, &client.ToolsIn{Project: project})
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, b := range set.Tools {
				if b.Activated {
					names = append(names, b.Name)
				}
			}
			return names
		}
		if _, err := planeActivate(acme, &client.ActivateIn{Tool: "acme_ghost", On: true}); status(err) != http.StatusUnprocessableEntity {
			t.Fatalf("activating an unknown tool = %v, want 422", err)
		}
		out, err := planeActivate(acme, &client.ActivateIn{Tool: "acme_hello", On: true})
		if err != nil || !out.Installed {
			t.Fatalf("activate = %+v %v", out, err)
		}
		if got := on(acme, ""); !slices.Equal(got, []string{"acme_hello"}) {
			t.Fatalf("acme's activated tools = %v", got)
		}
		if got := on(acme, "research"); len(got) != 0 {
			t.Fatalf("another project inherited the activation: %v", got)
		}
		if got := on(cloud.For(context.Background(), "globex"), ""); len(got) != 0 {
			t.Fatalf("another org inherited the activation: %v", got)
		}
		if out, err := planeActivate(acme, &client.ActivateIn{Tool: "acme_hello"}); err != nil || out.Installed {
			t.Fatalf("deactivate = %+v %v", out, err)
		}
		if got := on(acme, ""); len(got) != 0 {
			t.Fatalf("deactivated tools still on: %v", got)
		}
	})
}

// A pull request opened on a repository switches the code-review and dev skills
// on for the org on the call, in its default scope and the repository's, and for
// no other org. A call that names no org switches nothing.
func TestReviewOverThePlaneSwitchesTheOrgsSkillsOn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newApp(t, nil)
		acme := cloud.For(context.Background(), "acme")
		out, err := planeReview(acme, &client.ReviewIn{Project: "app"})
		if err != nil || !slices.Equal(out.Activated, []string{"skill_code-review", "skill_dev"}) {
			t.Fatalf("review = %+v %v", out, err)
		}
		act := mounted.State.activation
		for _, project := range []string{"", "app"} {
			for _, name := range out.Activated {
				if !act.IsActivated(acme, "acme", project, name) {
					t.Fatalf("%s is not on for acme in project %q", name, project)
				}
			}
		}
		if act.IsActivated(acme, "globex", "", "skill_dev") {
			t.Fatal("another org inherited the skills")
		}
		if _, err := planeReview(cloud.For(context.Background(), ""), &client.ReviewIn{}); status(err) != http.StatusForbidden {
			t.Fatalf("a call with no org = %v, want 403", err)
		}
	})
}
