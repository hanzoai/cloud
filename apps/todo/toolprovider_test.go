package todo

import (
	"context"
	"strings"
	"testing"

	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/internal/mint"
	luxlog "github.com/luxfi/log"
)

func mountTools(t *testing.T) {
	t.Helper()
	prev := mounted
	mounted = &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.New("test")},
		State: state{stores: cloud.NewOrgStore(cloud.Base{DataDir: t.TempDir()}, "todo", openStore)},
	}
	t.Cleanup(func() { mounted = prev })
}

func seedBoard(t *testing.T, org, project, key string) {
	t.Helper()
	st, err := storeFor(mounted, org, project)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	now := time.Now().Unix()
	if err := st.CreateProject(context.Background(), Project{
		ID: mint.ID("prj"), Org: org, Key: key, Name: key + " board",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
}

func call(t *testing.T, p tools.Principal, name string, args map[string]any) (any, error) {
	t.Helper()
	return todoToolProvider{}.Dispatch(context.Background(), p, name, args)
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("want map, got %T", v)
	}
	return m
}

// The tenant is the PRINCIPAL's. An agent naming another org's board must reach
// its own store and find nothing — never the other tenant's rows. This is the
// property that makes the whole tool safe to hand to a model.
func TestDispatchIsScopedToThePrincipal(t *testing.T) {
	mountTools(t)
	seedBoard(t, "acme", "default", "ENG")
	seedBoard(t, "other", "default", "ENG")

	acme := tools.Principal{Org: "acme", Project: "default"}
	if _, err := call(t, acme, toolCreate, map[string]any{"title": "acme work"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The other tenant ATTEMPTS the read, naming acme every way an argument can:
	// the board key it shares, and the tenant words a future field might use. A
	// test that passes no such argument proves only that the happy path works —
	// it cannot fail on a handler that honours one, which is the bug worth
	// catching (verified: an argument-honouring build passes without these).
	other := tools.Principal{Org: "other", Project: "default"}
	for _, attempt := range []map[string]any{
		{"board": "ENG"},
		{"board": "ENG", "org": "acme"},
		{"board": "ENG", "owner": "acme"},
		{"board": "ENG", "tenant": "acme"},
		{"board": "ENG", "project": "acme"},
	} {
		res, err := call(t, other, toolList, attempt)
		if err != nil {
			t.Fatalf("list %v: %v", attempt, err)
		}
		if n := asMap(t, res)["total"].(int); n != 0 {
			t.Fatalf("cross-tenant read via %v: other org saw %d of acme's items", attempt, n)
		}
	}

	// And a WRITE cannot cross either — the row must land in other's own store.
	if _, err := call(t, other, toolCreate, map[string]any{"board": "ENG", "org": "acme", "title": "other work"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	res, err := call(t, acme, toolList, map[string]any{"board": "ENG"})
	if err != nil {
		t.Fatalf("list acme: %v", err)
	}
	if n := asMap(t, res)["total"].(int); n != 1 {
		t.Fatalf("cross-tenant WRITE: acme's board holds %d items, want its own 1", n)
	}
}

// An item an agent opens must SAY an agent opened it, so "show me what the
// agents are doing" is answerable — and an agent must not be able to claim it
// came from a person.
func TestCreateStampsTheAgentSource(t *testing.T) {
	mountTools(t)
	seedBoard(t, "acme", "default", "ENG")
	p := tools.Principal{Org: "acme", Project: "default"}

	res, err := call(t, p, toolCreate, map[string]any{
		"title": "ship the thing", "source": "team",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := asMap(t, res)["source"]; got != "agent" {
		t.Fatalf("source = %v, want agent (an argument must not override it)", got)
	}
	if ref := asMap(t, res)["ref"]; ref != "ENG-1" {
		t.Fatalf("ref = %v, want ENG-1", ref)
	}
}

// Omitting the board is only safe when there is one. With several, guessing
// files work onto the wrong board, so it refuses and names them.
func TestBoardIsInferredOnlyWhenUnambiguous(t *testing.T) {
	mountTools(t)
	seedBoard(t, "acme", "default", "ENG")
	p := tools.Principal{Org: "acme", Project: "default"}

	if _, err := call(t, p, toolCreate, map[string]any{"title": "one board"}); err != nil {
		t.Fatalf("single board should infer: %v", err)
	}

	seedBoard(t, "acme", "default", "OPS")
	_, err := call(t, p, toolCreate, map[string]any{"title": "two boards"})
	if err == nil {
		t.Fatal("want a refusal when several boards exist")
	}
	for _, want := range []string{"ENG", "OPS", "board"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must name the boards; got %q", err)
		}
	}
}

// An update is a PATCH: a field the caller did not name keeps its value.
func TestUpdateLeavesUnnamedFieldsAlone(t *testing.T) {
	mountTools(t)
	seedBoard(t, "acme", "default", "ENG")
	p := tools.Principal{Org: "acme", Project: "default"}

	if _, err := call(t, p, toolCreate, map[string]any{
		"title": "keep me", "description": "the why", "assignee": "dave",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// JSON numbers arrive as float64 — an int-typed read alone answers 0 here.
	res, err := call(t, p, toolUpdate, map[string]any{
		"number": float64(1), "status": "in_progress",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	m := asMap(t, res)
	if m["status"] != "in_progress" {
		t.Fatalf("status = %v", m["status"])
	}
	if m["assignee"] != "dave" {
		t.Fatalf("assignee was erased by an update that did not name it: %v", m["assignee"])
	}

	st, _ := storeFor(mounted, "acme", "default")
	b, _ := st.GetProject(context.Background(), "acme", "ENG")
	got, err := st.GetIssue(context.Background(), "acme", b.ID, 1)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Description != "the why" {
		t.Fatalf("description erased: %q", got.Description)
	}
}

func TestUnknownStatusIsRefused(t *testing.T) {
	mountTools(t)
	seedBoard(t, "acme", "default", "ENG")
	p := tools.Principal{Org: "acme", Project: "default"}

	if _, err := call(t, p, toolCreate, map[string]any{"title": "x", "status": "shipped"}); err == nil {
		t.Fatal("want a refusal for a status outside the board's columns")
	}
}

// The listing is bounded, and says when it truncated rather than quietly
// returning a prefix a model would read as the whole board.
func TestListIsBoundedAndSaysSo(t *testing.T) {
	mountTools(t)
	seedBoard(t, "acme", "default", "ENG")
	p := tools.Principal{Org: "acme", Project: "default"}
	for i := 0; i < 5; i++ {
		if _, err := call(t, p, toolCreate, map[string]any{"title": "item"}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	res, err := call(t, p, toolList, map[string]any{"limit": float64(2)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	m := asMap(t, res)
	if got := len(m["items"].([]map[string]any)); got != 2 {
		t.Fatalf("returned %d items, want 2", got)
	}
	if m["total"].(int) != 5 || m["truncated"] != true {
		t.Fatalf("must report the real total and flag truncation: %v", m)
	}
}

// The provider offers nothing without a tenant, and offers a listing with one.
func TestListToolsNeedsAnOrg(t *testing.T) {
	mountTools(t)
	got, err := todoToolProvider{}.List(context.Background(), tools.Scope{})
	if err != nil || len(got) != 0 {
		t.Fatalf("no org must offer nothing: %v %v", got, err)
	}
	got, err = todoToolProvider{}.List(context.Background(), tools.Scope{Org: "acme"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("want the four verbs, got %d", len(got))
	}
	for _, tool := range got {
		if !tool.Dispatchable {
			t.Fatalf("%s must be callable", tool.Name)
		}
		if tool.Source != tools.SourceZAPService {
			t.Fatalf("%s source = %s", tool.Name, tool.Source)
		}
	}
}
