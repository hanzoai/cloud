package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// The kit is what an ADMIN put in place: the skills an admin activated — the
// brand's document where the brand carries the name, the org's own only where an
// admin wrote it — and the servers an admin registered, each by its id with
// exactly the tools of it an admin activated, and never its address or its
// credential. What a member did before the gate, and what a push
// wrote, is not carried. Another org's kit holds none of it.
func TestKitCarriesOnlyWhatAnAdminPutInPlace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		act, store := openSkillDocStores(t)
		servers, err := OpenMCPServerStore(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = servers.Close() })
		prev := brandDocs
		t.Cleanup(func() { brandDocs = prev })
		brandDocs = brandHas(map[string]string{"git_repos": "# repos"})

		for _, sk := range []Skill{
			{Org: "acme", Name: "triage", Content: "# triage", Admitted: true},
			{Org: "acme", Name: "idle", Content: "# idle", Admitted: true},
			{Org: "acme", Name: "planted", Content: "# a member wrote this before the gate"},
		} {
			if _, err := store.Put(ctx, sk); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.Replace(ctx, "acme", "site", []Skill{{Name: "pushed", Content: "# a push wrote this"}}); err != nil {
			t.Fatal(err)
		}
		for name, admitted := range map[string]bool{
			"skill_triage": true, "skill_git_repos": true, "skill_planted": true, "skill_pushed": true,
			"skill_idle":        false, // a member switched it on before the gate
			"stripe-com_charge": true, "stripe-com_refund": false, "stripe-com_bad.name": true,
			"docs_search": true, "legacy_drop": true,
		} {
			if err := act.Activate(ctx, "acme", "", name, SourceSkill, "u", admitted); err != nil {
				t.Fatal(err)
			}
		}
		for _, srv := range []MCPServer{
			{ID: "stripe-com", Org: "acme", Name: "Stripe", URL: "https://mcp.stripe.com/", AuthHeader: "Authorization", HasSecret: true, Admitted: true},
			{ID: "docs", Org: "acme", Name: "Docs", URL: "https://docs.example.com/mcp", HasSecret: true, Admitted: true},
			{ID: "quiet", Org: "acme", Name: "Quiet", URL: "https://quiet.example.com/mcp", Admitted: true},
			{ID: "legacy", Org: "acme", Name: "Legacy", URL: "https://legacy.example.com/mcp"},
		} {
			if _, err := servers.Write(ctx, srv, true); err != nil {
				t.Fatal(err)
			}
		}

		got, err := kit(ctx, act, store, servers, "acme", "")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Skills, []client.SkillDoc{{Name: "git_repos", Content: "# repos"}, {Name: "triage", Content: "# triage"}}) {
			t.Fatalf("skills: %+v", got.Skills)
		}
		want := []client.Remote{
			{Name: "docs", Tools: []string{"search"}},
			{Name: "stripe-com", Tools: []string{"charge"}},
		}
		if !reflect.DeepEqual(got.Servers, want) || len(got.Omitted) != 0 {
			t.Fatalf("servers: %+v omitted %v", got.Servers, got.Omitted)
		}

		other, err := kit(ctx, act, store, servers, "other", "")
		if err != nil || len(other.Skills) != 0 || len(other.Servers) != 0 {
			t.Fatalf("another org's kit: %v %+v", err, other)
		}

		// Built-in core skills (code-review, dev, ui) are carried out of the box
		// whenever the brand catalogue serves them, and can still be muted per person.
		brandDocs = brandHas(map[string]string{
			"code-review": "# code-review",
			"dev":         "# dev",
			"ui":          "# ui",
		})
		fresh, err := kit(ctx, act, store, servers, "unconfigured-org", "u-1")
		if err != nil || !slices.Equal(skillNames(*fresh), []string{"code-review", "dev", "ui"}) {
			t.Fatalf("fresh org kit out of the box: %v %+v", err, fresh)
		}
	})
}

// A run carries only so many skills: past the count or the bytes, a skill is
// named in Omitted rather than silently dropped.
func TestKitIsBounded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		act, store := openSkillDocStores(t)
		servers, err := OpenMCPServerStore(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = servers.Close() })
		prev := brandDocs
		t.Cleanup(func() { brandDocs = prev })
		brandDocs = brandHas(nil)

		big := strings.Repeat("x", maxKitBytes/2-1000)
		for i := range maxSkillDocs + 2 {
			name := fmt.Sprintf("s%02d", i)
			content := "# " + name
			if i < 3 {
				content = big
			}
			if _, err := store.Put(ctx, Skill{Org: "acme", Name: name, Content: content, Admitted: true}); err != nil {
				t.Fatal(err)
			}
			if err := act.Activate(ctx, "acme", "", "skill_"+name, SourceSkill, "u", true); err != nil {
				t.Fatal(err)
			}
		}
		got, err := kit(ctx, act, store, servers, "acme", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Skills) != maxSkillDocs-1 || !slices.Equal(got.Omitted, []string{"s16", "s17", "s02"}) {
			t.Fatalf("carried %d, omitted %v", len(got.Skills), got.Omitted)
		}
	})
}

// A run carries at most client.KitServers servers: past that, a server is named in
// Excess rather than silently dropped.
func TestKitCarriesSoManyServers(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		act, store := openSkillDocStores(t)
		servers, err := OpenMCPServerStore(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = servers.Close() })
		prev := brandDocs
		t.Cleanup(func() { brandDocs = prev })
		brandDocs = brandHas(nil)
		for i := range client.KitServers + 2 {
			id := fmt.Sprintf("s%02d", i)
			if _, err := servers.Write(ctx, MCPServer{ID: id, Org: "acme", Name: id, URL: "https://" + id + ".example.com/mcp", Admitted: true}, true); err != nil {
				t.Fatal(err)
			}
			if err := act.Activate(ctx, "acme", "", id+"_search", SourceMCP, "u", true); err != nil {
				t.Fatal(err)
			}
		}
		got, err := kit(ctx, act, store, servers, "acme", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Servers) != client.KitServers || got.Servers[client.KitServers-1].Name != "s15" || !slices.Equal(got.Excess, []string{"s16", "s17"}) {
			t.Fatalf("carried %d (last %+v), excess %v", len(got.Servers), got.Servers[len(got.Servers)-1], got.Excess)
		}
	})
}

// kitAs reads or writes the kit at /v1/tool/kit as person, a member of org.
func kitAs(t *testing.T, app *zip.App, method, org, person string, body any) (int, client.Kit) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, "/v1/tool/kit", r)
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", person)
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var k client.Kit
	_ = json.NewDecoder(resp.Body).Decode(&k)
	return resp.StatusCode, k
}

func skillNames(k client.Kit) []string {
	out := []string{}
	for _, s := range k.Skills {
		out = append(out, s.Name)
	}
	return out
}

// A PERSON'S LAYER ONLY NARROWS. GET /v1/tool/kit answers a member what their
// runs carry — the plane's own read, for the same person — and PUT mutes a skill,
// a server's tool or a whole server for that person alone. A name the org's kit
// does not carry is refused and nothing is muted; unmuting gives back only what an
// admin put in place; an admin's switch-off wins over a person's unmute.
func TestAPersonsLayerOnlyNarrowsTheKit(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t, nil)
		prev := brandDocs
		t.Cleanup(func() { brandDocs = prev })
		brandDocs = brandHas(nil)
		ctx := context.Background()
		st := mounted.State
		for _, name := range []string{"triage", "deploy"} {
			if _, err := st.skills.Put(ctx, Skill{Org: "acme", Name: name, Content: "# " + name, Admitted: true}); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{"stripe-com", "docs"} {
			if _, err := st.servers.Write(ctx, MCPServer{ID: id, Org: "acme", Name: id, URL: "https://" + id + ".example.com/mcp", Admitted: true}, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"skill_triage", "skill_deploy", "stripe-com_charge", "stripe-com_refund", "docs_search"} {
			if err := st.activation.Activate(ctx, "acme", "", name, "", "admin", true); err != nil {
				t.Fatal(err)
			}
		}

		code, whole := kitAs(t, app, http.MethodGet, "acme", "u-zoe", nil)
		if code != http.StatusOK || !slices.Equal(skillNames(whole), []string{"deploy", "triage"}) || len(whole.Servers) != 2 || len(whole.Muted) != 0 {
			t.Fatalf("zoe's kit before muting: %d %+v", code, whole)
		}

		for _, bad := range []string{"skill_absent", "evil", "../x"} {
			if code, _ := kitAs(t, app, http.MethodPut, "acme", "u-zoe", map[string]any{"mute": []string{"skill_triage", bad}}); code != http.StatusBadRequest {
				t.Fatalf("muting %q: %d, want 400", bad, code)
			}
		}
		if muted, _ := st.activation.Muted(ctx, "acme", "u-zoe"); len(muted) != 0 {
			t.Fatalf("a refused request muted %v", muted)
		}

		code, mine := kitAs(t, app, http.MethodPut, "acme", "u-zoe", map[string]any{"mute": []string{"skill_deploy", "docs", "stripe-com_refund"}})
		want := []client.Remote{{Name: "stripe-com", Tools: []string{"charge"}}}
		if code != http.StatusOK || !slices.Equal(skillNames(mine), []string{"triage"}) || !reflect.DeepEqual(mine.Servers, want) ||
			!slices.Equal(mine.Muted, []string{"docs", "skill_deploy", "stripe-com_refund"}) {
			t.Fatalf("zoe's kit after muting: %d %+v", code, mine)
		}
		plane, err := planeKit(zip.WithCaller(ctx, zip.Caller{Org: "acme", User: "u-zoe"}), &client.KitIn{})
		if err != nil || !reflect.DeepEqual(*plane, mine) {
			t.Fatalf("zoe's run carries %+v (%v), not what she is shown %+v", plane, err, mine)
		}
		if _, bob := kitAs(t, app, http.MethodGet, "acme", "u-bob", nil); !reflect.DeepEqual(bob, whole) {
			t.Fatalf("zoe's layer reached bob: %+v", bob)
		}

		// Unmute gives back what the org carries and nothing more; an admin's
		// switch-off wins over it.
		if err := st.activation.Deactivate(ctx, "acme", "", "stripe-com_refund"); err != nil {
			t.Fatal(err)
		}
		code, mine = kitAs(t, app, http.MethodPut, "acme", "u-zoe", map[string]any{"unmute": []string{"docs", "stripe-com_refund", "skill_absent"}})
		want = []client.Remote{{Name: "docs", Tools: []string{"search"}}, {Name: "stripe-com", Tools: []string{"charge"}}}
		if code != http.StatusOK || !reflect.DeepEqual(mine.Servers, want) || !slices.Equal(mine.Muted, []string{"skill_deploy"}) {
			t.Fatalf("zoe's kit after unmuting: %d %+v", code, mine)
		}
	})
}
