package bot

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestMain hands cek a throwaway master key. The store opens through cek,
// which refuses to open a data plane unencrypted, so these tests run the REAL
// encrypted path — the same code a deployment runs, not a way around it.
func TestMain(m *testing.M) {
	_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	os.Exit(m.Run())
}

// Four methods stand in for the families that will be written on this spine:
// one that reads, one that writes, one that only an admin may call, and one
// whose parameters are closed. They exercise the registry the way a real
// family will, so the tests below drive registration rather than describing it.
func init() {
	Register("probe.read", Read, func(c *Call) (any, error) {
		var p struct {
			Key string `json:"key"`
		}
		if err := c.Bind(&p); err != nil {
			return nil, err
		}
		st, err := c.Store()
		if err != nil {
			return nil, err
		}
		var value string
		if err := st.Get(c.Context(), "probe", p.Key, &value); err != nil && !errors.Is(err, ErrNoDoc) {
			return nil, err
		}
		return map[string]any{"value": value, "org": c.Org(), "bot": c.Bot()}, nil
	})

	Register("probe.write", Write, func(c *Call) (any, error) {
		var p struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := c.Bind(&p); err != nil {
			return nil, err
		}
		st, err := c.Store()
		if err != nil {
			return nil, err
		}
		if err := st.Put(c.Context(), "probe", p.Key, p.Value); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})

	Register("probe.admin", Admin, func(*Call) (any, error) {
		return map[string]any{"ok": true}, nil
	})

	// A family that answers with its own declared type, which is the idiom
	// every real family here uses and the shape a reader that assumed
	// map[string]any silently fails on.
	Register("commands.list", Read, func(*Call) (any, error) {
		return map[string]any{"commands": probeCommands}, nil
	})

	Register("probe.compose", Read, func(*Call) (any, error) {
		// What a method gets back when it composes another subsystem. The
		// spine must turn it into the protocol's own shape, not leak an HTTP
		// status the client has no branch for.
		return nil, zip.ErrNotFound("session not found")
	})
}

// probeCommand is one slash command as a family that owns them would declare
// it: a struct, not a map.
type probeCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

var probeCommands = []probeCommand{
	{Name: "compact", Description: "shorten the conversation"},
	{Name: "clear", Description: "start again"},
}

// mount builds the subsystem over a fresh directory exactly as a deployment
// does, so what the tests below drive is the mounted surface rather than the
// handlers called out of band. A test that needs a model hands the deps one,
// the same field a deployment fills.
func mount(t *testing.T, shape ...func(*cloud.Deps)) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "bot-test", DisableStartupMessage: true})
	deps := cloud.Deps{Logger: luxlog.NewWriter(io.Discard), DataDir: t.TempDir(), Version: "test"}
	for _, fill := range shape {
		fill(&deps)
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// announced puts one bot in the registry through the same door a real bot loop
// announces itself at. A protocol call binds to a bot only once it is there.
func announced(t *testing.T, app *zip.App, org, id string) {
	t.Helper()
	if code, body := as(t, app, org, http.MethodPost, "/v1/bot/runs",
		`{"runId":"`+id+`","where":"cloud"}`); code != http.StatusCreated {
		t.Fatalf("announce %s: %d %s", id, code, body)
	}
}

// as issues one request carrying a validated caller in org. Both headers are
// set: an org with no user is the anonymous forge and scopes to nothing, so a
// test stating only the org would prove nothing about orgs.
func as(t *testing.T, app *zip.App, org, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u@"+org)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close() //nolint:errcheck
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return res.StatusCode, string(b)
}

func field(t *testing.T, body, name string) any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("response is not a JSON object: %v (%s)", err, body)
	}
	return m[name]
}

// who names a caller: an org, and whether that caller is an admin of it. Both
// identity headers are always set — an org with no validated user is the
// anonymous forge and holds nothing, so a test stating only the org would
// prove nothing about orgs.
type who struct {
	org   string
	admin bool
	bot   string
}

// asking builds one request frame as an HTTP request: the frame, the door it
// goes through, and the identity headers the boundary would have set.
func asking(w who, id, method, params string) *http.Request {
	frame := `{"type":"req","id":"` + id + `","method":"` + method + `"`
	if params != "" {
		frame += `,"params":` + params
	}
	frame += `}`

	path := "/v1/bot"
	if w.bot != "" {
		path += "?bot=" + w.bot
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(frame))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", w.org)
	req.Header.Set("X-User-Id", "u@"+w.org)
	if w.admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	return req
}

// answered reads one response frame off a served request.
func answered(res *http.Response) (int, map[string]any, error) {
	defer res.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return res.StatusCode, nil, err
	}
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return res.StatusCode, nil, fmt.Errorf("response is not a frame: %w (%s)", err, body)
	}
	return res.StatusCode, out, nil
}

// ask sends one request frame through the mounted surface and returns the HTTP
// status and the response frame.
func ask(t *testing.T, app *zip.App, w who, id, method, params string) (int, map[string]any) {
	t.Helper()
	res, err := app.Fiber().Test(asking(w, id, method, params))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	code, frame, err := answered(res)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return code, frame
}

// atOnce sends one request frame per parameter set, all in flight together, and
// returns the answers in the order the parameters were given. It is how a test
// puts two writers on one document at the same instant — two browser tabs, a
// slash command beside the composer — which is what a read-modify-write has to
// survive. Nothing here fails from a goroutine that is not the test's: an error
// travels back and is raised where it can be reported.
func atOnce(t *testing.T, app *zip.App, w who, method string, params []string) []map[string]any {
	t.Helper()
	out := make([]map[string]any, len(params))
	failed := make([]error, len(params))
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for i, p := range params {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := asking(w, fmt.Sprintf("c:%d", i), method, p)
			<-ready
			res, err := app.Fiber().Test(req)
			if err != nil {
				failed[i] = err
				return
			}
			_, out[i], failed[i] = answered(res)
		}()
	}
	close(ready)
	wg.Wait()
	for i, err := range failed {
		if err != nil {
			t.Fatalf("%s[%d]: %v", method, i, err)
		}
	}
	return out
}

func payload(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	p, ok := frame["payload"].(map[string]any)
	if !ok {
		t.Fatalf("frame carries no payload object: %v", frame)
	}
	return p
}

func wrong(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	e, ok := frame["error"].(map[string]any)
	if !ok {
		t.Fatalf("frame carries no error object: %v", frame)
	}
	return e
}

// The envelope: a request names a method and carries an id, and the answer
// comes back as a response frame carrying that same id. Nothing else
// correlates the two.
func TestEnvelopeRoundTrips(t *testing.T) {
	app := mount(t)

	code, frame := ask(t, app, who{org: "acme"}, "1:abc", "connect",
		`{"minProtocol":4,"maxProtocol":4,"client":{"id":"openclaw-control","version":"1.0.0","platform":"web","mode":"control"}}`)
	if code != http.StatusOK {
		t.Fatalf("connect: %d", code)
	}
	if frame["type"] != "res" {
		t.Errorf("frame type is %v, want res", frame["type"])
	}
	if frame["id"] != "1:abc" {
		t.Errorf("answer carries id %v, want the 1:abc it was asked with", frame["id"])
	}
	if frame["ok"] != true {
		t.Fatalf("connect refused: %v", frame)
	}

	hello := payload(t, frame)
	if hello["type"] != "hello-ok" {
		t.Errorf("handshake payload is %v, want hello-ok", hello["type"])
	}
	if hello["protocol"] != float64(protocol) {
		t.Errorf("protocol is %v, want %d", hello["protocol"], protocol)
	}
	features, _ := hello["features"].(map[string]any)
	methods, _ := features["methods"].([]any)
	var sawConnect bool
	for _, m := range methods {
		if m == "connect" {
			sawConnect = true
		}
	}
	if !sawConnect {
		t.Errorf("the handshake does not advertise the method that answered it: %v", methods)
	}
	if _, ok := hello["policy"].(map[string]any)["tickIntervalMs"]; !ok {
		t.Errorf("the handshake states no tick interval, and the client times its silence from it: %v", hello)
	}
}

// Two requests in flight are told apart by their ids and nothing else, so an
// answer must never borrow the id of another.
func TestAnswerCarriesTheIdItWasAskedWith(t *testing.T) {
	app := mount(t)
	for _, id := range []string{"1:aaa", "2:bbb", "3:ccc"} {
		_, frame := ask(t, app, who{org: "acme"}, id, "probe.read", `{"key":"k"}`)
		if frame["id"] != id {
			t.Errorf("answer to %s carries id %v", id, frame["id"])
		}
	}
}

// A caller with no validated principal is refused at every door of this
// surface: before a frame is read on the protocol's two, and before anything is
// read or written on the registry's. A request carrying nothing is not a
// tenant, and neither is one carrying an org header alone — that is the
// off-gateway forge.
func TestUnvalidatedCallerIsRefused(t *testing.T) {
	app := mount(t)
	for _, probe := range []struct {
		what    string
		method  string
		path    string
		body    string
		org     bool
		upgrade bool
	}{
		{"the roster", http.MethodGet, "/v1/bot/runs", "", false, false},
		{"beginning a run", http.MethodPost, "/v1/bot/runs", `{"where":"cloud"}`, false, false},
		{"the roster, from a forged org", http.MethodGet, "/v1/bot/runs", "", true, false},
		{"the socket", http.MethodGet, "/v1/bot", "", true, true},
		{"one frame", http.MethodPost, "/v1/bot", `{"type":"req","id":"1:a","method":"connect"}`, true, false},
	} {
		req := httptest.NewRequest(probe.method, probe.path, strings.NewReader(probe.body))
		if probe.org {
			req.Header.Set("X-Org-Id", "acme") // an org, and nothing that validated it
		}
		if probe.upgrade {
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			req.Header.Set("Sec-WebSocket-Version", "13")
			req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		}
		res, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s (%s %s): %v", probe.what, probe.method, probe.path, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s reached an unvalidated caller: %d, want 403", probe.what, res.StatusCode)
		}
	}
}

// A method costs a capability. A caller who lacks it is told which one, in the
// shape the client reads to offer an upgrade — not in prose it would have to
// parse.
func TestMissingScopeNamesItself(t *testing.T) {
	app := mount(t)

	_, frame := ask(t, app, who{org: "acme"}, "1:a", "probe.admin", "")
	if frame["ok"] != false {
		t.Fatalf("a member of the org ran an admin method: %v", frame)
	}
	e := wrong(t, frame)
	if e["code"] != "FORBIDDEN" {
		t.Errorf("refusal code is %v, want FORBIDDEN", e["code"])
	}
	details, _ := e["details"].(map[string]any)
	if details["code"] != "MISSING_SCOPE" {
		t.Errorf("refusal details are %v, want a MISSING_SCOPE discriminant", details)
	}
	if details["missingScope"] != string(Admin) {
		t.Errorf("refusal names %v as missing, want %s", details["missingScope"], Admin)
	}

	if _, frame = ask(t, app, who{org: "acme", admin: true}, "2:a", "probe.admin", ""); frame["ok"] != true {
		t.Errorf("an admin of the org was refused its own admin method: %v", frame)
	}
}

// The capabilities a caller holds come from IAM and from nothing else, so a
// connect that asks for more than IAM granted gets the overlap rather than a
// refusal — the UI asks for all six every time.
func TestConnectGrantsWhatIamAllowsAndNoMore(t *testing.T) {
	app := mount(t)
	all := `{"minProtocol":4,"maxProtocol":4,"scopes":["operator.admin","operator.read","operator.write","operator.approvals","operator.questions","operator.pairing"]}`

	held := func(w who) map[string]bool {
		_, frame := ask(t, app, w, "1:a", "connect", all)
		auth, _ := payload(t, frame)["auth"].(map[string]any)
		out := map[string]bool{}
		for _, s := range auth["scopes"].([]any) {
			out[s.(string)] = true
		}
		return out
	}

	member := held(who{org: "acme"})
	for _, want := range []Scope{Read, Write, Approvals, Questions} {
		if !member[string(want)] {
			t.Errorf("a member of the org was not granted %s: %v", want, member)
		}
	}
	for _, deny := range []Scope{Admin, Pairing} {
		if member[string(deny)] {
			t.Errorf("a member of the org was granted %s by asking for it: %v", deny, member)
		}
	}

	admin := held(who{org: "acme", admin: true})
	for _, want := range []Scope{Admin, Pairing} {
		if !admin[string(want)] {
			t.Errorf("an admin of the org was not granted %s: %v", want, admin)
		}
	}
}

// The implications are the protocol's: admin stands for everything, read is
// answered by write, and nothing else is implied.
func TestScopeImplications(t *testing.T) {
	for _, tc := range []struct {
		held Grant
		need Scope
		want bool
	}{
		{Grant{Admin}, Read, true},
		{Grant{Admin}, Pairing, true},
		{Grant{Write}, Read, true},
		{Grant{Read}, Write, false},
		{Grant{Read}, Admin, false},
		{Grant{Write}, Admin, false},
		{Grant{Write}, Approvals, false},
		{Grant{Approvals}, Approvals, true},
		{nil, Read, false},
		{nil, Open, true},
	} {
		if got := tc.held.Allows(tc.need); got != tc.want {
			t.Errorf("%v allows %s = %v, want %v", tc.held, tc.need, got, tc.want)
		}
	}
}

// A name nobody registered is a request the caller can correct, not a failure
// of the server.
func TestUnknownMethodIsInvalid(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, who{org: "acme"}, "1:a", "sessions.nothing", "")
	if frame["ok"] != false {
		t.Fatalf("an unregistered method answered: %v", frame)
	}
	if code := wrong(t, frame)["code"]; code != "INVALID_REQUEST" {
		t.Errorf("unknown method answered %v, want INVALID_REQUEST", code)
	}
}

// Parameters are closed. A field the method does not declare is a caller that
// has misunderstood it, and answering anyway is how a typo becomes silence.
func TestBindRefusesAFieldTheMethodDoesNotDeclare(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, who{org: "acme"}, "1:a", "probe.read", `{"key":"k","keyy":"typo"}`)
	if frame["ok"] != false {
		t.Fatalf("a misspelled parameter was accepted: %v", frame)
	}
	if code := wrong(t, frame)["code"]; code != "INVALID_REQUEST" {
		t.Errorf("a bad parameter answered %v, want INVALID_REQUEST", code)
	}
}

// A method may compose the subsystems that already exist and hand back their
// error. The spine turns it into a code the client has a branch for, rather
// than an HTTP status the protocol never defined.
func TestASubsystemErrorBecomesAProtocolCode(t *testing.T) {
	app := mount(t)
	code, frame := ask(t, app, who{org: "acme"}, "1:a", "probe.compose", "")
	if code != http.StatusOK {
		t.Fatalf("a method-level failure changed the transport status: %d", code)
	}
	e := wrong(t, frame)
	if e["code"] != "INVALID_REQUEST" {
		t.Errorf("a 404 from another subsystem became %v", e["code"])
	}
	if e["message"] != "session not found" {
		t.Errorf("the reason was lost: %v", e["message"])
	}
}

// State that belongs to a bot lives in that bot's own file. Two bots of one
// org share nothing, and neither shares the org's own file.
func TestABotsStateIsItsOwn(t *testing.T) {
	app := mount(t)
	announced(t, app, "acme", "bot_aaaaaaaa")
	announced(t, app, "acme", "bot_bbbbbbbb")
	first := who{org: "acme", bot: "bot_aaaaaaaa"}
	second := who{org: "acme", bot: "bot_bbbbbbbb"}

	if _, frame := ask(t, app, first, "1:a", "probe.write", `{"key":"k","value":"mine"}`); frame["ok"] != true {
		t.Fatalf("write: %v", frame)
	}

	_, frame := ask(t, app, first, "2:a", "probe.read", `{"key":"k"}`)
	if got := payload(t, frame)["value"]; got != "mine" {
		t.Errorf("a bot cannot read back what it wrote: %v", got)
	}
	_, frame = ask(t, app, second, "3:a", "probe.read", `{"key":"k"}`)
	if got := payload(t, frame)["value"]; got != "" {
		t.Errorf("another bot of the same org read it: %v", got)
	}
	_, frame = ask(t, app, who{org: "acme"}, "4:a", "probe.read", `{"key":"k"}`)
	if got := payload(t, frame)["value"]; got != "" {
		t.Errorf("the org's own store carries a bot's state: %v", got)
	}
}

// The org is the boundary, and it is the file. One org cannot read another's
// state by asking for the same key.
func TestOrgsShareNothing(t *testing.T) {
	app := mount(t)
	if _, frame := ask(t, app, who{org: "acme"}, "1:a", "probe.write", `{"key":"k","value":"secret"}`); frame["ok"] != true {
		t.Fatalf("write: %v", frame)
	}
	_, frame := ask(t, app, who{org: "other"}, "2:a", "probe.read", `{"key":"k"}`)
	if got := payload(t, frame)["value"]; got != "" {
		t.Errorf("another org read it: %v", got)
	}
}

// A bot id chooses a file, so a shape that is not an id is refused before it
// can become a path.
func TestABadBotIdIsRefused(t *testing.T) {
	app := mount(t)
	code, _ := ask(t, app, who{org: "acme", bot: "../../etc"}, "1:a", "probe.read", `{"key":"k"}`)
	if code != http.StatusBadRequest {
		t.Errorf("a traversal in the bot id got %d, want 400", code)
	}
}

// The shape is only half the question. A well-formed id nobody owns must bind
// to nothing, because each binding opens a file and holds its descriptors for
// the life of the process: an org that could name bots freely would mint
// partitions until the process ran out of handles, and the tenant that runs out
// is whichever one asks next. So the id is looked up in the registry that owns
// bots, and only a bot this org announced chooses a file.
func TestAnInventedBotBindsToNothing(t *testing.T) {
	dir := t.TempDir()
	app := mount(t, func(d *cloud.Deps) { d.DataDir = dir })
	announced(t, app, "acme", "bot_realone")

	for _, w := range []who{{org: "acme"}, {org: "acme", bot: "bot_realone"}} {
		if _, frame := ask(t, app, w, "1:a", "probe.write", `{"key":"k","value":"v"}`); frame["ok"] != true {
			t.Fatalf("%s could not write: %v", w.org+"/"+w.bot, frame)
		}
	}
	for i := range 40 {
		id := fmt.Sprintf("botfill%06d", i)
		code, frame := ask(t, app, who{org: "acme", bot: id}, "2:a",
			"probe.write", `{"key":"k","value":"v"}`)
		if code != http.StatusForbidden {
			t.Fatalf("an invented bot id answered %d %v", code, frame)
		}
	}

	files := 0
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "bot.db" {
			files++
		}
		return err
	}); err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	// The org's own file, and the one bot it announced.
	if files != 2 {
		t.Errorf("40 invented bot ids left %d bot.db files, want 2: a query string minted partitions", files)
	}
}

// A body that is not a request frame has no id, so there is nothing to answer
// it with; the transport says so instead.
func TestABodyThatIsNotAFrameIsRefused(t *testing.T) {
	app := mount(t)
	for _, body := range []string{`{`, `{"type":"res","id":"1:a"}`, `{"type":"req","method":"connect"}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/bot", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "u@acme")
		res, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("post %q: %v", body, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%q got %d, want 400", body, res.StatusCode)
		}
	}
}

// Publishing before a mount and after a shutdown reaches nobody, and must not
// take the process with it.
func TestPublishOutsideTheSurfaceIsQuiet(t *testing.T) {
	app := mount(t)
	Publish("acme", "", "", "sessions.changed", map[string]any{"key": "main"})
	if err := Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	Publish("acme", "", "", "sessions.changed", map[string]any{"key": "main"})
	_ = app
}

func TestShutdownIsIdempotent(t *testing.T) {
	mount(t)
	for range 3 {
		if err := Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}
}

// A doc comment on an exported declaration begins with the name it documents.
// The convention is not decoration: `go doc` renders the comment beside the
// name, so one that opens with a different identifier sends a reader looking
// for a symbol that is not there — and a rename that misses the prose leaves
// the two disagreeing with nothing to notice. go vet does not check this.
func TestExportedDocsNameWhatTheyDocument(t *testing.T) {
	pkgs, err := parser.ParseDir(gotoken.NewFileSet(), ".", func(f os.FileInfo) bool {
		return !strings.HasSuffix(f.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	named := func(doc *ast.CommentGroup, name string) {
		t.Helper()
		if doc == nil || !ast.IsExported(name) {
			return
		}
		first, _, _ := strings.Cut(strings.TrimPrefix(doc.Text(), "// "), " ")
		if first != name {
			t.Errorf("the doc on exported %s opens with %q", name, first)
		}
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					named(d.Doc, d.Name.Name)
				case *ast.GenDecl:
					// A doc on a group covers the group; a doc on the one spec
					// inside it covers that spec, and is the case here.
					if len(d.Specs) != 1 {
						continue
					}
					switch spec := d.Specs[0].(type) {
					case *ast.TypeSpec:
						named(d.Doc, spec.Name.Name)
					case *ast.ValueSpec:
						if len(spec.Names) == 1 {
							named(d.Doc, spec.Names[0].Name)
						}
					}
				}
			}
		}
	}
}
