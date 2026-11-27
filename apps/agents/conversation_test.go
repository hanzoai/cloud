package agents

import (
	"context"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	hz "github.com/hanzoai/agent"
	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// A member pins, archives and deletes their own conversation through the mounted
// surface; another org's caller reaches none of it; and each change is written to
// the audit trail on the conversation it changed, not on a share.
func TestAConversationChangeIsAuditedOnTheConversation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		rec, err := audit.Open(t.TempDir(), "audit", nil)
		if err != nil {
			t.Fatalf("audit.Open: %v", err)
		}
		t.Cleanup(func() { _ = rec.Close() })
		iss := iamtest.New(t)
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		cloud.Identify(app, &cloud.Config{IAMIssuer: iamtest.Issuer, Discovery: oidctest.Discovery(iamtest.Issuer), DiscoveryClient: oidctest.Client(iamtest.Issuer, iss.URL)})
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
		if err := Use(app, cloud.Deps{AI: &fakeAI{content: "x"}, Audit: rec}); err != nil {
			t.Fatalf("Use: %v", err)
		}
		t.Cleanup(func() { _ = Shutdown(context.Background()) })
		person := func(sub, name, org string) string {
			return iss.Sign(t, iamtest.Claims{Sub: sub, PreferredUsername: name, Owner: org, Orgs: []map[string]any{{"org": org, "role": "owner"}}})
		}
		acme := person("7a1e0000-0000-4000-8000-0000000000b1", "ann", "acme")
		globex := person("7a1e0000-0000-4000-8000-0000000000b2", "gus", "globex")

		code, made := sendAs(t, app, http.MethodPost, chat+"/conversations", acme, map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "how do tides work"}},
		})
		id, _ := made["conversationId"].(string)
		if code != http.StatusOK || id == "" {
			t.Fatalf("record: %d %v", code, made)
		}
		one := chat + "/conversations/" + id

		if code, out := sendAs(t, app, http.MethodPatch, one, globex, map[string]any{"pinned": true}); code != http.StatusNotFound {
			t.Fatalf("another org pins it: %d %v", code, out)
		}
		if code, out := sendAs(t, app, http.MethodPatch, one, acme, map[string]any{"pinned": true}); code != http.StatusOK {
			t.Fatalf("pin: %d %v", code, out)
		}
		_, list := sendAs(t, app, http.MethodGet, chat+"/conversations", acme, nil)
		convs, _ := list["conversations"].([]any)
		if len(convs) != 1 || convs[0].(map[string]any)["id"] != id || convs[0].(map[string]any)["pinned"] != true {
			t.Fatalf("list after the pin: %v", list)
		}
		if code, out := sendAs(t, app, http.MethodDelete, one, globex, nil); code != http.StatusNotFound {
			t.Fatalf("another org deletes it: %d %v", code, out)
		}
		if code, out := sendAs(t, app, http.MethodDelete, one, acme, nil); code != http.StatusOK {
			t.Fatalf("delete: %d %v", code, out)
		}
		if code, _ := sendAs(t, app, http.MethodGet, one, acme, nil); code != http.StatusNotFound {
			t.Fatalf("read after delete: %d", code)
		}

		rows, _, err := rec.Query(context.Background(), audit.Filter{Org: "acme"})
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		want := map[string]bool{"agent.conversation.update": false, "agent.conversation.delete": false}
		for _, r := range rows {
			if _, ok := want[r.Action]; !ok {
				continue
			}
			if r.Resource != (audit.Resource{Type: "conversation", ID: id}) {
				t.Fatalf("%s recorded on %+v", r.Action, r.Resource)
			}
			want[r.Action] = true
		}
		for action, seen := range want {
			if !seen {
				t.Fatalf("no %s in the trail: %+v", action, rows)
			}
		}
		if others, _, _ := rec.Query(context.Background(), audit.Filter{Org: "globex"}); len(others) != 0 {
			t.Fatalf("a refused change was audited: %+v", others)
		}

	})
}

// A caller the identity boundary did not attest — headers alone, as a request
// that never crossed the boundary carries — keys nothing and is refused.
func TestAnUnattestedCallerKeysNoConversation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		if code, raw := do(t, app, http.MethodPost, chat+"/conversations", "acme", map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		}); code != http.StatusForbidden {
			t.Fatalf("record without an attested subject: %d %s", code, raw)
		}
		if code, raw := do(t, app, http.MethodGet, chat+"/conversations", "acme", nil); code != http.StatusForbidden {
			t.Fatalf("list without an attested subject: %d %s", code, raw)
		}

	})
}

// Every self-serve customer is a member of org hanzo, so a conversation is the
// person's who opened it, never the org's. Through the identity boundary, a
// second customer of hanzo lists none of the first's, opens it as an id that is
// not there, and a turn sent under it opens a thread of their own; an admin of
// hanzo is one more stranger, to the conversation and to its links.
// (Regression: GET /v1/agents/chat/conversations listed 67 strangers' chats to a
// brand-new customer.)
func TestACustomerSeesNoOtherCustomersConversation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		iss := iamtest.New(t)
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		cloud.Identify(app, &cloud.Config{IAMIssuer: iamtest.Issuer, Discovery: oidctest.Discovery(iamtest.Issuer), DiscoveryClient: oidctest.Client(iamtest.Issuer, iss.URL)})
		dir := t.TempDir()
		t.Setenv("CLOUD_DATA_DIR", dir)
		old := memberless(t, dir, "hanzo")
		if err := Use(app, cloud.Deps{AI: &fakeAI{content: "x"}}); err != nil {
			t.Fatalf("Use: %v", err)
		}
		t.Cleanup(func() { _ = Shutdown(t.Context()) })
		person := func(sub, name, role string) string {
			return iss.Sign(t, iamtest.Claims{Sub: sub, PreferredUsername: name, Owner: "hanzo", Orgs: []map[string]any{{"org": "hanzo", "role": role}}})
		}
		// Platform staff can enter this org but still cannot read one another's
		// conversations. Plain signup members now have no tenancy in this org.
		first := person("7a1e0000-0000-4000-8000-0000000000a1", "first", "admin")
		second := person("7a1e0000-0000-4000-8000-0000000000a2", "second", "admin")
		staff := person("7a1e0000-0000-4000-8000-0000000000a3", "staff", "admin")

		status, rec := sendAs(t, app, http.MethodPost, chat+"/conversations", first, map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "my private question"}, {"role": "assistant", "content": "my private answer"}},
		})
		conv, _ := rec["conversationId"].(string)
		if status != http.StatusOK || conv == "" {
			t.Fatalf("record: %d %v", status, rec)
		}
		if status, made := sendAs(t, app, http.MethodPost, chat+"/conversations/"+conv+"/shares", first, nil); status != http.StatusCreated {
			t.Fatalf("share: %d %v", status, made)
		}
		listOf := func(bearer string) []any {
			t.Helper()
			status, out := sendAs(t, app, http.MethodGet, chat+"/conversations", bearer, nil)
			if status != http.StatusOK {
				t.Fatalf("list: %d %v", status, out)
			}
			l, _ := out["conversations"].([]any)
			return l
		}
		for name, bearer := range map[string]string{"the first customer": first, "another customer": second, "an admin of hanzo": staff} {
			for _, c := range listOf(bearer) {
				if id, _ := c.(map[string]any)["id"].(string); id == old {
					t.Fatalf("%s lists the conversation recorded with no member", name)
				}
			}
			if status, _ := sendAs(t, app, http.MethodGet, chat+"/conversations/"+old, bearer, nil); status != http.StatusNotFound {
				t.Fatalf("%s opens the conversation recorded with no member: %d", name, status)
			}
			status, out := sendAs(t, app, http.MethodPost, chat+"/conversations", bearer, map[string]any{
				"conversationId": old, "messages": []map[string]string{{"role": "user", "content": "written by " + name}},
			})
			if got, _ := out["conversationId"].(string); status != http.StatusOK || got == old {
				t.Fatalf("%s wrote into the conversation recorded with no member: %d", name, status)
			}
		}
		for name, bearer := range map[string]string{"another customer": second, "an admin of hanzo": staff} {
			if l := listOf(bearer); len(l) != 1 {
				t.Fatalf("%s lists %d conversations; want only the one they just opened", name, len(l))
			}
			foreign, a := sendAs(t, app, http.MethodGet, chat+"/conversations/"+conv, bearer, nil)
			absent, b := sendAs(t, app, http.MethodGet, chat+"/conversations/0000000000000000000-000000", bearer, nil)
			if foreign != http.StatusNotFound || absent != http.StatusNotFound || a["detail"] != b["detail"] {
				t.Fatalf("%s opens it: %d %v; an absent id: %d %v", name, foreign, a, absent, b)
			}
			status, out := sendAs(t, app, http.MethodPost, chat+"/conversations", bearer, map[string]any{
				"conversationId": conv, "messages": []map[string]string{{"role": "user", "content": "written by " + name}},
			})
			if got, _ := out["conversationId"].(string); status != http.StatusOK || got == conv || got == "" {
				t.Fatalf("%s's turn landed in %q (the first's is %q): %d", name, got, conv, status)
			}
			for _, m := range []string{http.MethodPatch, http.MethodDelete} {
				if status, _ := sendAs(t, app, m, chat+"/conversations/"+conv, bearer, map[string]any{"title": "taken"}); status != http.StatusNotFound {
					t.Fatalf("%s %s it: %d", name, m, status)
				}
			}
			if status, _ := sendAs(t, app, http.MethodGet, chat+"/shares", bearer, nil); status != http.StatusForbidden {
				t.Fatalf("%s lists hanzo's links: %d", name, status)
			}
		}
		status, out := sendAs(t, app, http.MethodGet, chat+"/conversations/"+conv, first, nil)
		if msgs, _ := out["messages"].([]any); status != http.StatusOK || len(msgs) != 2 {
			t.Fatalf("the first customer's own thread moved: %d %v", status, out)
		}
		if l := listOf(first); len(l) != 2 {
			t.Fatalf("the first customer lists %d; want their two", len(l))
		}

	})
}

// memberless writes into org's history a conversation as the store kept one
// before members were (2026-09-07): one turn, and no member named.
func memberless(t *testing.T, dir, org string) string {
	t.Helper()
	db, err := orm.OpenSQLite(&ormdb.SQLiteDBConfig{Path: filepath.Join(dir, "orgs", org, "agent.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	cv := orm.New[hz.Conversation](db)
	cv.SetId("1788756056956243915-000012")
	cv.Org, cv.Title = org, "With Antje"
	if err := cv.CreateCtx(t.Context()); err != nil {
		t.Fatal(err)
	}
	m := orm.New[hz.Message](db)
	m.SetId("1788756056956243916-000013")
	m.ConversationId, m.Org, m.Role, m.Content = cv.Id(), org, "user", "a stranger's words"
	if err := m.CreateCtx(t.Context()); err != nil {
		t.Fatal(err)
	}
	return cv.Id()
}
