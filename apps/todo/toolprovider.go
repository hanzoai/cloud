package todo

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/internal/mint"
)

// todoToolProvider makes the board callable BY AN AGENT, so a conversation about
// work can also record it.
//
// The whole point is that an agent doing work across a project can open, find and
// close items on the SAME board a person reads, rather than keeping a private
// list that nobody else can see. So this is a thin binding onto the store the
// HTTP surface already uses — no second table, no second notion of what an issue
// is.
//
// SCOPE IS NOT AN ARGUMENT. `tools.Scope`/`tools.Principal` already carry the
// validated (org, project), and `storeFor` takes exactly those, so an agent
// cannot name a tenant: the org it writes to is the one its credential resolved
// to, decided upstream in SanitizeIdentity. A `project` argument below is the
// board's KEY — a row inside that tenant — never the tenant itself. Conflating
// the two is how a tool plane becomes a cross-tenant write.
//
// Source is ZAPService because that is what this is: a first-party cloud service
// route exposed as a tool. It ranks above agents, skills and an org's external
// MCP servers, so nothing can shadow `todo_*` with its own.
type todoToolProvider struct{}

func (todoToolProvider) Source() tools.Source { return tools.SourceZAPService }

// The four verbs an agent needs to work a board: see what boards exist, read
// items, open one, and move one. Deliberately no delete — an agent that can
// erase a person's work item is a worse trade than one that has to say so.
const (
	toolBoards = "todo_boards"
	toolList   = "todo_list"
	toolCreate = "todo_create"
	toolUpdate = "todo_update"
)

// boardArg is the same optional parameter on every item tool: which board inside
// the caller's project. Omitted, it resolves to the only board there is — which
// is the common case and saves an agent a round trip — and refuses when there
// are several, because guessing is how work lands on the wrong board.
const boardArg = `"board":{"type":"string","description":"Board key (e.g. ENG). Optional when the project has exactly one board."}`

// enum renders a closed set as JSON-Schema text, and it is the ONLY place a
// legal value is spelled for a caller.
//
// The set is the app's (`statuses`, `priorities` in todo.go) and stays the app's.
// Restating one here as a string literal is a second copy of a value with an
// owner, and that copy already cost something: with the enum written out by hand
// the schema LOOKED authoritative, so priority went unvalidated while status was
// checked. A copy does not merely risk drift — it disguises the absence of the
// thing it copies.
//
// SORTED, and that is load-bearing rather than tidy: a Go map has no order, the
// published document is a committed artifact the drift gate compares byte for
// byte, and an unsorted enum would turn a green gate red at random.
func enum(set map[string]bool) string {
	vals := make([]string, 0, len(set))
	for v := range set {
		vals = append(vals, `"`+v+`"`)
	}
	sort.Strings(vals)
	return "[" + strings.Join(vals, ",") + "]"
}

// oneOf is the ONE membership check, so a value the schema advertises and a value
// the handler accepts cannot disagree. An empty string means "not asked".
func oneOf(set map[string]bool, v, what string) error {
	if v == "" || set[v] {
		return nil
	}
	return fmt.Errorf("unknown %s %q", what, v)
}

// The schemas are BUILT from the sets, never written beside them. Adding a status
// to the app's map moves the document, the MCP tool, the CLI and the validator at
// once, because all four read that one map.
var (
	statusEnum   = enum(statuses)
	priorityEnum = enum(priorities)

	boardsSchema = []byte(`{"type":"object","properties":{},"additionalProperties":false}`)

	listSchema = []byte(`{"type":"object","properties":{` + boardArg + `,
"status":{"type":"string","enum":` + statusEnum + `,"description":"Only items in this column."},
"assignee":{"type":"string","description":"Only items held by this person."},
"source":{"type":"string","enum":["team","git","crm","helpdesk","cms","agent"],"description":"Where the item came from. Use \"agent\" to see what agents opened for themselves."},
"text":{"type":"string","description":"Case-insensitive match on title or description."},
"limit":{"type":"integer","description":"Most items to return (default 50, max 200)."}},
"additionalProperties":false}`)

	createSchema = []byte(`{"type":"object","properties":{` + boardArg + `,
"title":{"type":"string","description":"One line naming the work."},
"description":{"type":"string","description":"What needs doing, and why."},
"status":{"type":"string","enum":` + statusEnum + `,"description":"Defaults to backlog."},
"priority":{"type":"string","enum":` + priorityEnum + `},
"assignee":{"type":"string","description":"Who holds it."},
"labels":{"type":"string","description":"Comma-separated labels."}},
"required":["title"],"additionalProperties":false}`)

	updateSchema = []byte(`{"type":"object","properties":{` + boardArg + `,
"number":{"type":"integer","description":"The item's number on its board."},
"title":{"type":"string"},"description":{"type":"string"},
"status":{"type":"string","enum":` + statusEnum + `},
"priority":{"type":"string","enum":` + priorityEnum + `},
"assignee":{"type":"string"},"labels":{"type":"string"}},
"required":["number"],"additionalProperties":false}`)
)

func (todoToolProvider) List(ctx context.Context, scope tools.Scope) ([]tools.Tool, error) {
	// No org means no tenant to resolve a store for, so there is nothing to
	// offer — not an error, just an empty listing.
	if mounted == nil || scope.Org == "" {
		return nil, nil
	}
	t := func(name, desc string, schema []byte) tools.Tool {
		return tools.Tool{
			Name: name, Source: tools.SourceZAPService,
			Description: desc, Schema: schema, Dispatchable: true,
		}
	}
	return []tools.Tool{
		t(toolBoards, "List the work boards in this project.", boardsSchema),
		t(toolList, "Read work items on a board — filter by column, assignee, source or text. Use source=agent to see what agents opened for themselves.", listSchema),
		t(toolCreate, "Open a work item on a board, so the work is recorded where people can see it.", createSchema),
		t(toolUpdate, "Move or edit a work item — change its column, priority, assignee or text.", updateSchema),
	}, nil
}

func (todoToolProvider) Dispatch(ctx context.Context, p tools.Principal, name string, args map[string]any) (any, error) {
	if mounted == nil {
		return nil, tools.ErrUnknownTool
	}
	// The tenant is the principal's, never the arguments'.
	store, err := storeFor(mounted, p.Org, p.Project)
	if err != nil {
		return nil, err
	}

	switch name {
	case toolBoards:
		return listBoards(ctx, store, p.Org)
	case toolList:
		return listItems(ctx, store, p, args)
	case toolCreate:
		return createItem(ctx, store, p, args)
	case toolUpdate:
		return updateItem(ctx, store, p, args)
	}
	return nil, tools.ErrUnknownTool
}

func listBoards(ctx context.Context, store *Store, org string) (any, error) {
	ps, err := store.ListProjects(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(ps))
	for _, b := range ps {
		out = append(out, map[string]any{"key": b.Key, "name": b.Name, "description": b.Description})
	}
	return map[string]any{"boards": out}, nil
}

// board resolves which board a call means: the key it named, or the only one
// there is. With several and no key it REFUSES and names them, because an agent
// filing into an arbitrary board is worse than one that asks.
func board(ctx context.Context, store *Store, org string, args map[string]any) (Project, error) {
	if k := str(args, "board"); k != "" {
		b, err := store.GetProject(ctx, org, strings.ToUpper(k))
		if err != nil {
			return Project{}, fmt.Errorf("no board %q in this project", k)
		}
		return b, nil
	}
	ps, err := store.ListProjects(ctx, org)
	if err != nil {
		return Project{}, err
	}
	switch len(ps) {
	case 0:
		return Project{}, fmt.Errorf("this project has no boards yet — create one before filing work")
	case 1:
		return ps[0], nil
	}
	keys := make([]string, 0, len(ps))
	for _, b := range ps {
		keys = append(keys, b.Key)
	}
	sort.Strings(keys)
	return Project{}, fmt.Errorf("this project has several boards (%s) — name one with \"board\"", strings.Join(keys, ", "))
}

func listItems(ctx context.Context, store *Store, p tools.Principal, args map[string]any) (any, error) {
	b, err := board(ctx, store, p.Org, args)
	if err != nil {
		return nil, err
	}
	f := IssueFilter{
		Status:   str(args, "status"),
		Source:   str(args, "source"),
		Assignee: str(args, "assignee"),
		Text:     str(args, "text"),
	}
	if err := oneOf(statuses, f.Status, "status"); err != nil {
		return nil, err
	}
	items, err := store.ListIssues(ctx, p.Org, b.ID, f)
	if err != nil {
		return nil, err
	}
	// A model pays for every row it reads, so the listing is bounded and says
	// when it truncated rather than quietly returning a prefix.
	limit := 50
	if n := num(args, "limit"); n > 0 {
		limit = n
	}
	if limit > 200 {
		limit = 200
	}
	total := len(items)
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]map[string]any, 0, len(items))
	for _, i := range items {
		out = append(out, brief(b.Key, i))
	}
	res := map[string]any{"board": b.Key, "items": out, "total": total}
	if total > len(items) {
		res["truncated"] = true
	}
	return res, nil
}

func createItem(ctx context.Context, store *Store, p tools.Principal, args map[string]any) (any, error) {
	b, err := board(ctx, store, p.Org, args)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(str(args, "title"))
	if title == "" {
		return nil, fmt.Errorf("a work item needs a title")
	}
	status := str(args, "status")
	if status == "" {
		status = "backlog"
	}
	if err := oneOf(statuses, status, "status"); err != nil {
		return nil, err
	}
	// Checked, not merely advertised: the schema's enum and this call read the
	// same set, so a value the document offers is a value the handler takes.
	if err := oneOf(priorities, str(args, "priority"), "priority"); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	i := Issue{
		// The row's identity is the caller's to supply — the store assigns the
		// board NUMBER and nothing else, so an omitted id makes every insert
		// collide on the primary key.
		ID: mint.ID("issue"), CreatedAt: now, UpdatedAt: now,
		Org: p.Org, ProjectID: b.ID, Kind: "issue",
		// Stamped, not taken from arguments: this row was opened by an agent and
		// the board must be able to say so — that is what makes "show me what the
		// agents are doing" answerable, and it is not an agent's to claim
		// otherwise.
		Source:      "agent",
		Title:       title,
		Description: str(args, "description"),
		Status:      status,
		Priority:    str(args, "priority"),
		Assignee:    str(args, "assignee"),
		Labels:      str(args, "labels"),
	}
	created, err := store.CreateIssue(ctx, i)
	if err != nil {
		return nil, err
	}
	return brief(b.Key, created), nil
}

func updateItem(ctx context.Context, store *Store, p tools.Principal, args map[string]any) (any, error) {
	b, err := board(ctx, store, p.Org, args)
	if err != nil {
		return nil, err
	}
	n := num(args, "number")
	if n <= 0 {
		return nil, fmt.Errorf("which item? pass its number")
	}
	cur, err := store.GetIssue(ctx, p.Org, b.ID, n)
	if err != nil {
		return nil, fmt.Errorf("no item %s-%d", b.Key, n)
	}
	// A field the caller did not name keeps its value — an update is a patch,
	// so omitting `description` must not erase it.
	if v, ok := args["title"]; ok {
		cur.Title = fmt.Sprint(v)
	}
	if v, ok := args["description"]; ok {
		cur.Description = fmt.Sprint(v)
	}
	if v, ok := args["assignee"]; ok {
		cur.Assignee = fmt.Sprint(v)
	}
	if v, ok := args["labels"]; ok {
		cur.Labels = fmt.Sprint(v)
	}
	if v, ok := args["priority"]; ok {
		cur.Priority = fmt.Sprint(v)
	}
	if err := oneOf(statuses, str(args, "status"), "status"); err != nil {
		return nil, err
	}
	if err := oneOf(priorities, str(args, "priority"), "priority"); err != nil {
		return nil, err
	}
	if s := str(args, "status"); s != "" {
		cur.Status = s
	}
	cur.UpdatedAt = time.Now().Unix()
	if err := store.UpdateIssue(ctx, cur); err != nil {
		return nil, err
	}
	return brief(b.Key, cur), nil
}

// brief is what a model reads back: enough to reason and to refer to the item
// again, and not the whole row. `ref` is the human name for it (ENG-12), which
// is also what a person will search for.
func brief(key string, i Issue) map[string]any {
	m := map[string]any{
		"ref": fmt.Sprintf("%s-%d", key, i.Number), "number": i.Number,
		"title": i.Title, "status": i.Status, "source": i.Source,
	}
	if i.Priority != "" {
		m["priority"] = i.Priority
	}
	if i.Assignee != "" {
		m["assignee"] = i.Assignee
	}
	if i.Labels != "" {
		m["labels"] = i.Labels
	}
	return m
}

func str(args map[string]any, k string) string {
	if v, ok := args[k]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// num reads an integer that arrived as JSON, which decodes every number as a
// float64 — so an int-typed read alone silently answers 0 for every value.
func num(args map[string]any, k string) int {
	switch v := args[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
