package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/kms"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fixture is the command list the resolver is read against: a handful of commands
// shaped exactly as zip.CommandsFromSpec produces them, spanning the one
// distinction the rules turn on — GET against everything else.
//
// A fixture rather than the fleet's own projection, because these tests are
// about the RULES. Composing 119 subsets to ask whether an abbreviation resolves
// would pin the rules to whatever the fleet happens to publish today, so a route
// added in another app could turn a prefix ambiguous and redden a test that has
// nothing to do with it.
func fixture() []zip.Command {
	return []zip.Command{
		{Service: "platform", Name: "apps-list", Method: http.MethodGet, Path: "/v1/platform/apps"},
		{Service: "platform", Name: "apps-get", Method: http.MethodGet, Path: "/v1/platform/apps/:app",
			Args: []zip.Arg{{Name: "app"}}},
		{Service: "platform", Name: "apps-rollback", Method: http.MethodPost, Path: "/v1/platform/apps/:app/rollback",
			Args: []zip.Arg{{Name: "app"}}},
		{Service: "platform", Name: "apps-delete", Method: http.MethodDelete, Path: "/v1/platform/apps/:app",
			Args: []zip.Arg{{Name: "app"}}},
		{Service: "deploy", Name: "sites-create", Method: http.MethodPost, Path: "/v1/deploy/sites",
			Flags: []zip.Flag{{Name: "name", Field: "name", Type: "string", Required: true}}},
		{Service: "o11y", Name: "logs-list", Method: http.MethodGet, Path: "/v1/o11y/logs",
			Flags: []zip.Flag{{Name: "limit", Field: "limit", Type: "integer"}}},
	}
}

// The parse table. Every row is a slash body a person could type, and what the
// registry makes of it: the argv the runner takes, or nothing — which is the
// signal to answer in prose.
func TestResolve(t *testing.T) {
	cases := []struct {
		name string
		text string
		argv []string
	}{
		// EXACT. Any method, named in full.
		{"exact get", "platform apps-list", []string{"platform", "apps-list"}},
		{"exact get with an argument", "platform apps-get web", []string{"platform", "apps-get", "web"}},
		{"exact mutation", "platform apps-rollback web", []string{"platform", "apps-rollback", "web"}},
		{"exact delete", "platform apps-delete web", []string{"platform", "apps-delete", "web"}},
		{"exact with flags", "deploy sites-create --name site", []string{"deploy", "sites-create", "--name", "site"}},

		// FUZZY, GET only, and only when one command answers to it. The
		// abbreviation still has to be given the arguments the command takes —
		// naming it loosely does not excuse invoking it wrongly.
		{"unambiguous get prefix", "platform apps-g web", []string{"platform", "apps-get", "web"}},
		{"an abbreviation missing its argument", "platform apps-g", nil},
		{"unambiguous get prefix in another service", "o11y logs", []string{"o11y", "logs-list"}},
		{"whole name is a prefix of itself", "o11y logs-list", []string{"o11y", "logs-list"}},

		// A MUTATION IS NEVER REACHED BY AN ABBREVIATION.
		{"prefix of a POST", "platform apps-rollb web", nil},
		{"prefix of a DELETE", "platform apps-del web", nil},
		{"prefix of a POST in another service", "deploy sites-cr --name site", nil},

		// AMBIGUOUS is not resolved: two GETs answer to "apps".
		{"ambiguous get prefix", "platform apps", nil},

		// PROSE falls through untouched.
		{"a question", "what's the weather in tokyo", nil},
		{"one word", "platform", nil},
		{"empty", "", nil},
		{"whitespace", "   ", nil},
		{"unknown service", "ghost apps-list", nil},
		{"a quoted empty service", `"" apps-list`, nil},
		{"a quoted empty operation", `platform ""`, nil},
		{"unknown operation", "platform nope", nil},
		{"a sentence that starts with a service name", "platform is down again right?", nil},
		{"an apostrophe after a service name", "platform's apps are down", nil},

		// NAMING IS NOT INVOKING. These open with two tokens that DO resolve, and
		// then hand the command words it has nowhere to put. They are sentences.
		{"prose past a resolved GET", "o11y logs for last week please", nil},
		{"prose past a command taking one argument", "platform apps-get web and also the other one", nil},
		{"a flag the command never declared", "o11y logs-list --since yesterday", nil},
		{"a required flag left out", "deploy sites-create", nil},
		{"a flag with no value", "o11y logs-list --limit", nil},
		{"a flag whose value is the wrong type", "o11y logs-list --limit soon", nil},
		// …but a help request IS a command: the runner answers it without invoking.
		{"asking what it does", "platform apps-get --help", []string{"platform", "apps-get", "--help"}},

		// SLACK'S OWN WIRE. A quoted run is one argument; the three entities
		// Slack escapes come back as themselves.
		{"quoted value", `deploy sites-create --name "my site"`, []string{"deploy", "sites-create", "--name", "my site"}},
		{"escaped ampersand", `deploy sites-create --name "a &amp; b"`, []string{"deploy", "sites-create", "--name", "a & b"}},
	}
	cmds := fixture()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv, ok := resolve(cmds, c.text)
			if ok != (c.argv != nil) {
				t.Fatalf("resolve(%q) ok=%v, want %v (argv %v)", c.text, ok, c.argv != nil, argv)
			}
			if ok && strings.Join(argv, "\x00") != strings.Join(c.argv, "\x00") {
				t.Fatalf("resolve(%q) = %q, want %q", c.text, argv, c.argv)
			}
		})
	}
}

// The asymmetry, stated over the whole fixture rather than over the rows that
// happen to be listed above: no proper prefix of a non-GET's name NAMES it, and
// every non-GET names itself in full.
//
// Asked of `names` rather than `resolve`, because these are two questions and
// mixing them would test the wrong one — `resolve` also refuses a command whose
// arguments are missing, so a bare mutation name would come back false for a
// reason that has nothing to do with the rule under test.
func TestNames_MutationsMustBeNamed(t *testing.T) {
	cmds := fixture()
	for _, c := range cmds {
		if c.Method == http.MethodGet {
			continue
		}
		for i := 1; i < len(c.Name); i++ {
			argv := []string{c.Service, c.Name[:i]}
			if names(cmds, argv) && argv[1] == c.Name {
				t.Errorf("%s %s: the prefix %q named a %s", c.Service, c.Name, c.Name[:i], c.Method)
			}
		}
		if argv := []string{c.Service, c.Name}; !names(cmds, argv) {
			t.Errorf("%s %s: naming it in full did not resolve", c.Service, c.Name)
		}
	}
}

// A registry that could not be built resolves nothing, so every slash body still
// reaches the agent. The failure is soft by construction, not by a flag.
func TestResolve_NoRegistryIsProse(t *testing.T) {
	if _, ok := resolve(nil, "platform apps-list"); ok {
		t.Fatal("an empty registry resolved a command")
	}
}

// THE SAFETY PROPERTY, over the REAL registry rather than the fixture: no
// abbreviation of any command's name resolves to something a non-GET answers to,
// unless that abbreviation IS the non-GET's whole name.
//
// The fixture states the rule; this states that the rule survives contact with
// 2,300 route names nobody wrote with it in mind. It is worth the second telling
// because that is exactly how it failed: `commandName` reduces the internal
// `/_/` plane's first segment to an empty service, and a quoted empty token then
// addressed POST /_/commerce/tenants — a shape no hand-written fixture contains.
//
// It skips rather than fails when the document will not compose. That failure is
// real and it is openapi/compose_test.go's to report; repeating it here would say
// nothing new and would redden this package for another app's duplicate id.
func TestNames_TheFleetHasNoBackDoor(t *testing.T) {
	cmds, err := commandRegistry()
	if err != nil {
		t.Skipf("the fleet document does not compose on this tree (openapi/compose_test.go owns that): %v", err)
	}
	if len(cmds) < 100 {
		t.Fatalf("the fleet projected %d commands; that is not the fleet", len(cmds))
	}
	claims := map[string][]zip.Command{}
	for _, c := range cmds {
		claims[c.Service+" "+c.Name] = append(claims[c.Service+" "+c.Name], c)
	}
	for _, c := range cmds {
		for i := 1; i <= len(c.Name); i++ {
			typed := c.Name[:i]
			argv := []string{c.Service, typed}
			if c.Service == "" || !names(cmds, argv) {
				continue
			}
			for _, hit := range claims[argv[0]+" "+argv[1]] {
				if hit.Method == http.MethodGet || typed == hit.Name {
					continue
				}
				t.Fatalf("%q named %s %s (%s %s) without spelling it out",
					c.Service+" "+typed, hit.Service, hit.Name, hit.Method, hit.Path)
			}
		}
	}
	// And the untypeable ones stay untypeable: resolve refuses an empty service,
	// which is how the internal `/_/` plane is addressed and nothing else is.
	for _, c := range cmds {
		if c.Service != "" {
			continue
		}
		if _, ok := resolve(cmds, `"" `+c.Name); ok {
			t.Fatalf("an empty service token reached %s %s", c.Method, c.Path)
		}
	}
}

func TestWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a b c", []string{"a", "b", "c"}},
		{"  a   b  ", []string{"a", "b"}},
		{`a "b c" d`, []string{"a", "b c", "d"}},
		{`a 'b c'`, []string{"a", "b c"}},
		{`--json '{"a":1}'`, []string{"--json", `{"a":1}`}},
		{`a ""`, []string{"a", ""}}, // an explicitly empty argument survives
		{"a\tb\nc", []string{"a", "b", "c"}},
		{"", nil},
	}
	for _, c := range cases {
		got := words(c.in)
		if strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
			t.Errorf("words(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The entities Slack escapes come back as themselves, and a literal "&lt;" a
// person typed stays a literal — which is what the reverse order buys.
func TestUnescapeSlack(t *testing.T) {
	cases := [][2]string{
		{"a &amp; b", "a & b"},
		{"&lt;tag&gt;", "<tag>"},
		{"&amp;lt;", "&lt;"},
		{"nothing to do", "nothing to do"},
	}
	for _, c := range cases {
		if got := unescapeSlack(c[0]); got != c[1] {
			t.Errorf("unescapeSlack(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

// ── the answer ──────────────────────────────────────────────────────────────

// A scalar is printed, a container is named by its size, and a record inside a
// list is spelled out as its own scalars — one rule, applied at the two depths a
// chat message has room for.
func TestBrief(t *testing.T) {
	object := plain(json.RawMessage(`{"name":"web","running":true,"replicas":3,"env":{"A":"1"},"tags":["a","b"]}`))
	got := strings.Join(brief(object), "\n")
	want := "env: {1}\nname: web\nreplicas: 3\nrunning: true\ntags: [2]"
	if got != want {
		t.Fatalf("object brief =\n%s\nwant\n%s", got, want)
	}

	list := plain(json.RawMessage(`[{"id":"a","up":true},{"id":"b","up":false}]`))
	got = strings.Join(brief(list), "\n")
	want = "2 items\nid=a up=true\nid=b up=false"
	if got != want {
		t.Fatalf("list brief =\n%s\nwant\n%s", got, want)
	}

	if got := strings.Join(brief(plain(json.RawMessage(`"ok"`))), ""); got != "ok" {
		t.Fatalf("scalar brief = %q", got)
	}
}

func TestCommandReply(t *testing.T) {
	argv := []string{"platform", "apps-get", "web"}
	const console = "https://console.hanzo.ai"

	// SUCCESS: the command line, then the payload.
	got := commandReply(argv, json.RawMessage(`{"name":"web","status":"running"}`), "", nil, console)
	if !strings.HasPrefix(got, "✓ `"+slashName+" platform apps-get web`") {
		t.Fatalf("success does not echo the command: %q", got)
	}
	if !strings.Contains(got, "name: web") || !strings.Contains(got, "status: running") {
		t.Fatalf("success dropped the payload: %q", got)
	}
	if strings.Contains(got, console) {
		t.Fatalf("a payload that fits must not offer the console: %q", got)
	}

	// A REFUSAL IS NOT AN OUTAGE: the service's own status and sentence survive.
	got = commandReply(argv, nil, "", errTest("GET /v1/platform/apps/web: 403 not a member of this org"), console)
	if !strings.HasPrefix(got, "✗ ") || !strings.Contains(got, "403") || !strings.Contains(got, "not a member of this org") {
		t.Fatalf("the refusal was not passed through: %q", got)
	}
	if strings.Contains(got, "try again") || strings.Contains(got, "unavailable") {
		t.Fatalf("a refusal must not read as an outage: %q", got)
	}

	// A LARGE PAYLOAD truncates and says where to read the rest.
	big := make([]map[string]string, 40)
	for i := range big {
		big[i] = map[string]string{"id": strings.Repeat("x", 60)}
	}
	raw, err := json.Marshal(big)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = commandReply(argv, json.RawMessage(raw), "", nil, console)
	if !strings.Contains(got, console) {
		t.Fatalf("a truncated payload must link the console: %q", got)
	}
	if len(got) > 4*briefBytes {
		t.Fatalf("truncation let %d bytes through", len(got))
	}

	// NOTHING CAME BACK: the status alone, no invented payload.
	if got = commandReply(argv, nil, "", nil, console); got != "✓ `"+slashName+" platform apps-get web`" {
		t.Fatalf("an empty result should be the status alone: %q", got)
	}

	// --help: the runner printed the answer and never invoked anything.
	got = commandReply(argv, nil, "platform apps-get <app>\n\nGET /v1/platform/apps/:app\n", nil, console)
	if !strings.Contains(got, "GET /v1/platform/apps/:app") {
		t.Fatalf("help was dropped: %q", got)
	}
}

// A payload posted into a chat has left Hanzo's custody, so a credential-named
// field never reaches it — whether it sits at the top level, inside a record in
// a list, or under a nested object the renderer only counts.
// The shapes here are the ones the renderer actually PRINTS — a root scalar and
// a record in a top-level list. Nesting them would prove nothing: the depth rule
// reduces a nested object to a count, so a test built that way passes whether or
// not anything is redacted at all.
func TestCommandReply_RedactsCredentials(t *testing.T) {
	root := json.RawMessage(`{"name":"acme","api_key":"hk-live-000","client_secret":"shhh"}`)
	got := commandReply([]string{"integrations", "connections-get"}, root, "", nil, defaultConsoleURL)
	for _, secret := range []string{"hk-live-000", "shhh"} {
		if strings.Contains(got, secret) {
			t.Fatalf("a root credential reached the reply (%q): %q", secret, got)
		}
	}
	if !strings.Contains(got, "name: acme") {
		t.Fatalf("redaction ate the payload: %q", got)
	}
	if !strings.Contains(got, "api_key: [REDACTED]") {
		t.Fatalf("the redacted field is not visible as redacted: %q", got)
	}

	// A list of records is spelled out field by field at depth 0, so a secret on
	// a row is printed unless it is redacted.
	rows := json.RawMessage(`[{"id":"a1","access_token":"tok-abc"},{"id":"a2","password":"hunter2"}]`)
	got = commandReply([]string{"integrations", "connections-list"}, rows, "", nil, defaultConsoleURL)
	for _, secret := range []string{"tok-abc", "hunter2"} {
		if strings.Contains(got, secret) {
			t.Fatalf("a credential on a row reached the reply (%q): %q", secret, got)
		}
	}
	if !strings.Contains(got, "id=a1") || !strings.Contains(got, "access_token=[REDACTED]") {
		t.Fatalf("the row did not render as expected: %q", got)
	}
}

// A REFUSAL CARRIES A BODY, and a body is a payload like any other. The status
// and the reason survive — collapsing them is what sends someone to wait for a
// healthy service — but a credential inside the body does not.
func TestSaid(t *testing.T) {
	got := said(errTest(`POST /v1/x: 403 {"error":"forbidden","api_key":"hk-live-000"}`))
	if strings.Contains(got, "hk-live-000") {
		t.Fatalf("a credential survived a refusal: %q", got)
	}
	if !strings.Contains(got, "403") || !strings.Contains(got, "forbidden") {
		t.Fatalf("the status or the reason was lost: %q", got)
	}
	// No body, nothing to redact, nothing lost.
	if got := said(errTest("GET /v1/x: 502 Bad Gateway")); got != "GET /v1/x: 502 Bad Gateway" {
		t.Fatalf("a bodyless error was altered: %q", got)
	}
}

// A code fence is closed by three backticks, so a value carrying them would let
// everything after it render as markup.
func TestCommandReply_CannotEscapeTheFence(t *testing.T) {
	got := commandReply([]string{"platform", "apps-list"},
		json.RawMessage("{\"name\":\"a```\\n<!channel>\"}"), "", nil, defaultConsoleURL)
	if strings.Contains(strings.TrimPrefix(got, "✓ "), "```\n<!channel>") {
		t.Fatalf("a payload closed the fence: %q", got)
	}
	if strings.Count(got, "```") != 2 {
		t.Fatalf("the reply does not have exactly one code block: %q", got)
	}
}

// No value reaching a channel may pass for markup: an unescaped <!channel> in a
// payload would broadcast to the whole workspace.
func TestCommandReply_EscapesInjection(t *testing.T) {
	hostile := json.RawMessage(`{"name":"<!channel> <@U123> <https://evil|click>"}`)
	got := commandReply([]string{"platform", "apps-list"}, hostile, "", nil, "https://console.hanzo.ai")
	if strings.ContainsAny(strings.TrimPrefix(got, "✓ "), "<>") {
		t.Fatalf("a raw angle bracket survived: %q", got)
	}
	got = commandReply([]string{"platform", "apps-list"}, nil, "", errTest("boom <!channel>"), "https://console.hanzo.ai")
	if strings.ContainsAny(strings.TrimPrefix(got, "✗ "), "<>") {
		t.Fatalf("a raw angle bracket survived an error: %q", got)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

// ── execution ───────────────────────────────────────────────────────────────

// run is the turn's tail, minus the identity it is handed: resolve, parse,
// send, render. It is spelled here exactly as slackCommandTurn spells it, so
// what these tests exercise is what a slash command runs.
func run(t *testing.T, base, bearer, text string) string {
	t.Helper()
	cmds := fixture()
	argv, ok := resolve(cmds, text)
	if !ok {
		t.Fatalf("%q resolved to no command", text)
	}
	var result any
	out := &strings.Builder{}
	send := invoker(base, bearer, "acme")
	cli := &zip.CLI{
		Name: slashName, Commands: cmds, Out: out,
		Invoke: func(ctx context.Context, c zip.Command, path map[string]string, body []byte) (any, error) {
			v, err := send(ctx, c, path, body)
			result = v
			return v, err
		},
	}
	err := cli.Run(context.Background(), argv)
	return commandReply(argv, result, out.String(), err, defaultConsoleURL)
}

// A POSITIONAL ARGUMENT ADDRESSES A RESOURCE, AND NOTHING ELSE. zip percent-
// encodes it, but fasthttp's SetRequestURI decodes %2F and resolves "..", so
// without a guard `platform apps-get ../../../v1/iam/users` arrives at the front
// door as GET /v1/iam/users — the command that runs is not the command that was
// named, and the exact-naming rule that keeps mutations behind their own names
// is void.
//
// Driven through the same map Command.parse produces, because that is the value
// the substitution actually reads.
func TestCommandRun_AnArgumentCannotRetargetThePath(t *testing.T) {
	var seen []string
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer front.Close()

	get := fixture()[1] // platform apps-get, GET /v1/platform/apps/:app
	if len(get.Args) != 1 {
		t.Fatalf("fixture drifted: %+v", get)
	}
	send := invoker(front.URL, "tok-1", "acme")
	for _, arg := range []string{
		"../../../v1/iam/users", // the demonstrated retarget
		"a/b",                   // a bare separator
		"a%2Fb",                 // pre-encoded, decoded once by the transport
		"..",
		".",
		"",
		"a\x00b", // a control rune
		"a?x=1",  // a query the route never declared
		"a#frag",
		`a\b`,
	} {
		_, err := send(context.Background(), get, map[string]string{"app": arg}, nil)
		if err == nil {
			t.Errorf("%q was accepted as a path segment", arg)
		}
	}
	if len(seen) != 0 {
		t.Fatalf("a refused argument still reached the front door: %v", seen)
	}

	// An ordinary id still goes through, at the address the registry named.
	if _, err := send(context.Background(), get, map[string]string{"app": "web-1.prod"}, nil); err != nil {
		t.Fatalf("an ordinary id was refused: %v", err)
	}
	if len(seen) != 1 || seen[0] != "/v1/platform/apps/web-1.prod" {
		t.Fatalf("front door saw %v", seen)
	}
}

// THE ORG IS THE WORKSPACE'S, NOT THE CALLER'S HOME. The front door reads a
// selection from X-Org-Id and validates it against the token's signed membership
// set; sending nothing means the caller's home org answers, so a person linked in
// org A's workspace would run mutations in org B.
func TestCommandRun_CarriesTheWorkspaceOrg(t *testing.T) {
	var got string
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Org-Id")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer front.Close()

	send := invoker(front.URL, "tok-1", "acme")
	if _, err := send(context.Background(), fixture()[0], nil, nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got != "acme" {
		t.Fatalf("X-Org-Id = %q, want acme", got)
	}
}

// bearerFor builds an unsigned token carrying a membership set — the shape IAM's
// token endpoint hands back, which is the only part `acting` reads.
func bearerFor(t *testing.T, orgs ...string) string {
	t.Helper()
	var claims authz.Claims
	for _, o := range orgs {
		claims.Orgs = append(claims.Orgs, authz.Membership{Org: o})
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(body) + "." + enc([]byte("sig"))
}

// A caller acts in the workspace's org only if the token says they belong to it.
// The front door DISCARDS a selection it cannot find rather than refusing, so a
// non-member would otherwise run the command in their own org, silently.
func TestActing(t *testing.T) {
	cases := []struct {
		name   string
		bearer string
		org    string
		want   bool
	}{
		{"home org", bearerFor(t, "acme"), "acme", true},
		{"a second membership", bearerFor(t, "widgets", "acme"), "acme", true},
		{"not a member", bearerFor(t, "widgets"), "acme", false},
		{"no memberships at all", bearerFor(t), "acme", false},
		{"not a token", "nonsense", "acme", false},
		{"empty org", bearerFor(t, "acme"), "", false},
	}
	for _, c := range cases {
		if got := acting(c.bearer, c.org); got != c.want {
			t.Errorf("%s: acting = %v, want %v", c.name, got, c.want)
		}
	}
}

// The command reaches the front door as the caller: the method and path the
// registry names, the positional argument substituted into the path, and the
// LINKED PERSON's bearer — never a service identity.
func TestCommandRun(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"web","status":"running"}`))
	}))
	defer front.Close()

	reply := run(t, front.URL, "tok-1", "platform apps-get web")
	if gotMethod != http.MethodGet || gotPath != "/v1/platform/apps/web" {
		t.Fatalf("front door saw %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("the caller's bearer did not reach the front door: %q", gotAuth)
	}
	if !strings.HasPrefix(reply, "✓ ") || !strings.Contains(reply, "name: web") {
		t.Fatalf("reply = %q", reply)
	}
}

// A bodyless method's flags ride the query string, which is where the route's
// own decoder looks for them.
func TestCommandRun_FlagsOnAGet(t *testing.T) {
	var gotQuery string
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	defer front.Close()

	run(t, front.URL, "tok-1", "o11y logs-list --limit 5")
	if gotQuery != "limit=5" {
		t.Fatalf("query = %q, want limit=5", gotQuery)
	}
}

// A mutation carries its flags as a JSON body.
func TestCommandRun_BodyOnAPost(t *testing.T) {
	var gotBody, gotMethod string
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotMethod = string(b), r.Method
		_, _ = w.Write([]byte(`{"id":"site_1"}`))
	}))
	defer front.Close()

	reply := run(t, front.URL, "tok-1", `deploy sites-create --name "my site"`)
	if gotMethod != http.MethodPost || gotBody != `{"name":"my site"}` {
		t.Fatalf("front door saw %s %s", gotMethod, gotBody)
	}
	if !strings.Contains(reply, "id: site_1") {
		t.Fatalf("reply = %q", reply)
	}
}

// The service's REFUSAL is what the reader gets — the status and the sentence it
// came with. A revoked credential must never read as an outage.
func TestCommandRun_RefusalIsReportedNotRewritten(t *testing.T) {
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"platform grants are required for this org"}`))
	}))
	defer front.Close()

	reply := run(t, front.URL, "tok-1", "platform apps-rollback web")
	if !strings.HasPrefix(reply, "✗ ") || !strings.Contains(reply, "403") {
		t.Fatalf("reply = %q", reply)
	}
	if !strings.Contains(reply, "platform grants are required") {
		t.Fatalf("the service's own sentence was dropped: %q", reply)
	}
	if strings.Contains(reply, "try again") || strings.Contains(reply, "unavailable") {
		t.Fatalf("a refusal must not read as an outage: %q", reply)
	}
}

// Asking what a command does answers from the command's own declaration, and
// invokes nothing.
func TestCommandRun_HelpInvokesNothing(t *testing.T) {
	sent := false
	front := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { sent = true }))
	defer front.Close()

	reply := run(t, front.URL, "tok-1", "platform apps-get --help")
	if sent {
		t.Fatal("a help request reached the front door")
	}
	if !strings.Contains(reply, "GET /v1/platform/apps/:app") {
		t.Fatalf("the reply does not carry the command's own address: %q", reply)
	}
}

// The turn's deadline is honoured where the pool slot is held: zip.Remote reads
// the context only before dialling, so a front door that never answers must not
// pin a bridge slot for the life of the socket.
func TestCommandRun_HonoursTheDeadline(t *testing.T) {
	block := make(chan struct{})
	front := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer front.Close()
	defer close(block)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := invoker(front.URL, "tok-1", "acme")(ctx, fixture()[0], nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// ── identity ────────────────────────────────────────────────────────────────

// emptyKMS is a KMS that holds nothing: every read is a miss, which is exactly
// what an unlinked user looks like.
type emptyKMS struct{ puts int }

func (k *emptyKMS) GetSecret(context.Context, string) ([]byte, error) {
	return nil, kms.ErrSecretNotFound
}
func (k *emptyKMS) PutSecret(context.Context, string, []byte) error { k.puts++; return nil }
func (k *emptyKMS) DeleteSecret(context.Context, string) error      { return nil }
func (k *emptyKMS) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, kms.ErrSecretNotFound
}

func linkService(store cloud.KMSClient) *cloud.Service[state] {
	key := make([]byte, minStateKeyLen)
	for i := range key {
		key[i] = byte(i + 1)
	}
	s := &cloud.Service[state]{State: state{stateKey: key, kms: store, consoleURL: defaultConsoleURL}}
	s.Domain = "api.hanzo.ai"
	s.Log = luxlog.NewNoOpLogger()
	return s
}

// An UNLINKED person gets the link prompt the channel already writes — and nothing
// runs. A command is the linked person's, so there is no identity to fall back
// to: not the workspace's bot token, not the service's own. (The caller delivers
// every answer on this branch ephemerally; slackSlashTurn states that once.)
func TestCommandTurn_UnlinkedIsThePrompt(t *testing.T) {
	store := &emptyKMS{}
	s := linkService(store)
	in := Inbound{Provider: "slack", ExternalID: "TACME", User: "U1", Channel: "C1", Text: "platform apps-list"}

	text := slackCommandTurn(s, context.Background(), "acme", in, fixture(), []string{"platform", "apps-list"})
	if !strings.Contains(text, "Connect your Hanzo account") {
		t.Fatalf("want the link prompt, got %q", text)
	}
	if !strings.Contains(text, "https://api.hanzo.ai/v1/integrations/slack/link?state=") {
		t.Fatalf("the prompt must carry the link URL: %q", text)
	}
	if store.puts != 0 {
		t.Fatal("an unlinked turn wrote to the secret store")
	}
}

// A link with no refresh token mints no bearer, so the command never leaves the
// process — the failure is refused at the credential, not at the call.
func TestUserBearer_NeedsTheSealedToken(t *testing.T) {
	s := linkService(&emptyKMS{})
	if _, err := userBearer(s, context.Background(), "acme", "U1", userLink{Subject: "sub-1"}); err == nil {
		t.Fatal("a link with no refresh token minted a bearer")
	}
}

// A REFUSED CREDENTIAL AND AN UNREACHABLE IAM NEED OPPOSITE ADVICE. IAM saying
// no to the sealed grant is permanent — waiting for it is a loop with no exit —
// so it says the link expired and carries the way to fix it. A 5xx or a dead
// socket is worth retrying, and says so.
func TestUserBearer_TellsARefusalFromAnOutage(t *testing.T) {
	var status int
	var body string
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer iam.Close()
	t.Setenv("IAM_ENDPOINT", iam.URL)

	s := linkService(&emptyKMS{})
	in := Inbound{Provider: "slack", ExternalID: "TACME", User: "U1"}
	link := userLink{Subject: "sub-1", Refresh: "sealed"}

	for _, c := range []struct {
		name     string
		status   int
		body     string
		rejected bool
	}{
		{"the grant was revoked", http.StatusBadRequest, `{"error":"invalid_grant"}`, true},
		{"an OAuth error on a 200", http.StatusOK, `{"error":"invalid_grant"}`, true},
		{"IAM is down", http.StatusBadGateway, `nope`, false},
	} {
		status, body = c.status, c.body
		_, err := userBearer(s, context.Background(), "acme", "U1", link)
		if err == nil {
			t.Fatalf("%s: minted a bearer anyway", c.name)
		}
		if errors.Is(err, errLinkRejected) != c.rejected {
			t.Fatalf("%s: errLinkRejected=%v, want %v (%v)", c.name, !c.rejected, c.rejected, err)
		}
	}

	// The sentence a refused grant produces names the fix and carries the URL.
	say := relink(s, in)
	if !strings.Contains(say, "expired") || !strings.Contains(say, "/v1/integrations/slack/link?state=") {
		t.Fatalf("relink must say what broke and how to fix it: %q", say)
	}
	if strings.Contains(say, "try again") {
		t.Fatalf("a permanent failure must not read as a transient one: %q", say)
	}
}
