package lsp

// lsp_test.go drives the REAL routes against a real daemon on loopback.
//
// The daemon here is an httptest server speaking the actual wire contract — POST
// /root, POST /ask, 409 {"need":"tree"}, X-API-Key — so these tests need no
// gVisor, no gopls and no network, and still exercise the same client the binary
// ships. The forge is substituted at its two variables (resolveRev, readTree),
// which is what lets one process stand in for two.
//
// Four properties, and they are the four this rewrite has to hold:
//
//	THE OPS REACH THE RIGHT DOOR. Five ops, one daemon URL each, with locate's
//	relation carried and defaulted.
//	A COLD REVISION IS ONE ROUND TRIP MORE, NOT A LOOP. 409 → /root → ask again,
//	exactly once.
//	THE ORG IS THE PRINCIPAL'S. A body that names an org is answered for the
//	principal's org anyway, because the body cannot carry one at all.
//	THE KEY IS PRESENTED. Every call to the daemon carries LSP_KEY, and a proxy
//	without one refuses instead of calling out.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	testKey = "test-service-key"
	testSHA = "0123456789abcdef0123456789abcdef01234567"
)

// call is one request the daemon received: which door, what it said, and which
// key it presented.
type call struct {
	path string
	key  string
	ask  question
	tree tree
}

// fleet is one test's world: a daemon on loopback, a git peer answering from
// memory, and the real routes over both.
type fleet struct {
	app *zip.App

	// held, once true, is a daemon that holds a root for the revision it is
	// asked about. It starts false — the cold state — and /root sets it, unless
	// stuck says this daemon never keeps one.
	held  bool
	stuck bool

	// files is what the forge answers a whole-tree read with.
	files []forge.File

	calls []call   // every daemon request, in order
	orgs  []string // the org each forge read was made FOR
	subs  []string // the principal each forge read RODE, which is who it acts as
	refs  []string // the ref each whole-tree read was pinned to
}

// newFleet stands the world up. key is what the proxy is configured with, so a
// test can also describe a deployment that never got one.
func newFleet(t *testing.T, key string, files []forge.File) *fleet {
	t.Helper()
	f := &fleet{files: files}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /ask", func(w http.ResponseWriter, r *http.Request) {
		var in question
		f.take(t, r, "/ask", &in, func(c *call) { c.ask = in })
		if !f.held {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"need": "tree", "org": in.Org, "repo": in.Repo, "rev": in.Rev,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"op": in.Op, "lang": "go", "hover": "func Hello()",
			"locations": []map[string]any{{"path": "a.go"}},
		})
	})
	mux.HandleFunc("POST /root", func(w http.ResponseWriter, r *http.Request) {
		var in tree
		f.take(t, r, "/root", &in, func(c *call) { c.tree = in })
		f.held = !f.stuck
		_ = json.NewEncoder(w).Encode(ready{Ready: true, Cold: true, Langs: []string{"go"}})
	})
	up := httptest.NewServer(mux)
	t.Cleanup(up.Close)

	prevRev, prevTree := resolveRev, readTree
	resolveRev = func(ctx context.Context, org, repo, ref string) (string, error) {
		f.saw(ctx, org)
		return testSHA, nil
	}
	readTree = func(ctx context.Context, org, repo, sha string) (forge.Tree, error) {
		f.saw(ctx, org)
		f.refs = append(f.refs, sha)
		return forge.Tree{Rev: testSHA, Files: f.files}, nil
	}
	t.Cleanup(func() { resolveRev, readTree = prevRev, prevTree })

	s := &state{
		Base:   cloud.Base{Log: luxlog.New("test")},
		daemon: &daemon{url: up.URL, key: key, http: &http.Client{Timeout: prepareWait}},
	}
	f.app = zip.New(zip.Config{Logger: luxlog.New("test")})
	// A subsystem never installs cloud.Bridge — the composer does, once, after
	// the identity check that mints the validated org. In a test the test IS the
	// composer, so it owes the same thing.
	f.app.Use(cloud.Bridge())
	// The REAL registration, not a reconstruction of it: routes() is what Mount
	// calls, so every typed op is exercised here exactly as the binary serves it.
	if err := routes(f.app, s); err != nil {
		t.Fatalf("routes: %v", err)
	}
	return f
}

// warm is a fleet whose daemon already holds the root, which is the steady state.
func warm(t *testing.T) *fleet {
	t.Helper()
	f := newFleet(t, testKey, source())
	f.held = true
	return f
}

func (f *fleet) take(t *testing.T, r *http.Request, path string, in any, set func(*call)) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read %s body: %v", path, err)
	}
	if err := json.Unmarshal(body, in); err != nil {
		t.Fatalf("decode %s body %s: %v", path, body, err)
	}
	c := call{path: path, key: r.Header.Get("X-API-Key")}
	set(&c)
	f.calls = append(f.calls, c)
}

// paths is the door sequence the daemon saw.
func (f *fleet) paths() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.path)
	}
	return out
}

// sent is the tree the daemon was given, if it was given one.
func (f *fleet) sent() tree {
	for _, c := range f.calls {
		if c.path == "/root" {
			return c.tree
		}
	}
	return tree{}
}

func (f *fleet) post(t *testing.T, op, org string, body any) (int, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/code/lsp/"+op, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		// A VALIDATED principal, as SanitizeIdentity mints one from a verified
		// token — which is the only thing that satisfies the org gate.
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	res, err := f.app.Test(req)
	if err != nil {
		t.Fatalf("Test %s: %v", op, err)
	}
	defer func() { _ = res.Body.Close() }()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

func source() []forge.File {
	return []forge.File{{Path: "a.go", Data: []byte("package p\n")}}
}

// saw records one forge read: the tenant it names, and the principal it rides.
// Both matter and neither substitutes for the other — the tenant picks the
// namespace, the principal is who the forge is asked AS.
func (f *fleet) saw(ctx context.Context, org string) {
	f.orgs = append(f.orgs, org)
	f.subs = append(f.subs, cloud.Who(ctx).User)
}

// TestEachOpReachesAskUnderItsOwnName proves the door→op mapping: every route
// posts /ask, and the op it names is its own. A mapping that drifts here answers
// a hover as a completion, which no status code would reveal.
func TestEachOpReachesAskUnderItsOwnName(t *testing.T) {
	for _, tc := range []struct {
		op       string
		relation string
		in       Query
	}{
		{"hover", "", Query{Repo: "cloud", Path: "a.go", Line: 3, Character: 7}},
		{"symbols", "", Query{Repo: "cloud", Path: "a.go"}},
		{"diagnostics", "", Query{Repo: "cloud", Path: "a.go"}},
		{"complete", "", Query{Repo: "cloud", Path: "a.go", Line: 1, Character: 4}},
		// locate defaults its relation to definition — the question an editor's
		// go-to means — and carries an explicit one through untouched.
		{"locate", "definition", Query{Repo: "cloud", Path: "a.go"}},
		{"locate", "reference", Query{Repo: "cloud", Path: "a.go", Relation: "reference"}},
		{"locate", "type", Query{Repo: "cloud", Path: "a.go", Relation: "type"}},
		{"locate", "implementation", Query{Repo: "cloud", Path: "a.go", Relation: "implementation"}},
	} {
		t.Run(tc.op+"/"+tc.relation, func(t *testing.T) {
			f := warm(t)
			code, body := f.post(t, tc.op, "acme", tc.in)
			if code != http.StatusOK {
				t.Fatalf("status=%d body=%s", code, body)
			}
			if got := f.paths(); !slices.Equal(got, []string{"/ask"}) {
				t.Fatalf("daemon saw %v, want one /ask", got)
			}
			got := f.calls[0].ask
			if got.Op != tc.op {
				t.Errorf("op = %q, want %q", got.Op, tc.op)
			}
			if got.Relation != tc.relation {
				t.Errorf("relation = %q, want %q", got.Relation, tc.relation)
			}
			// The position is the LSP's and passes through untouched.
			if got.Line != tc.in.Line || got.Character != tc.in.Character {
				t.Errorf("position = %d:%d, want %d:%d",
					got.Line, got.Character, tc.in.Line, tc.in.Character)
			}
			// The rev on the wire is the RESOLVED commit, never what was named.
			if got.Rev != testSHA {
				t.Errorf("rev = %q, want the resolved sha %q", got.Rev, testSHA)
			}
		})
	}
}

// TestAnUnknownRelationIsRefused proves locate's relation is a CLOSED set, so an
// arbitrary string never reaches a language server.
func TestAnUnknownRelationIsRefused(t *testing.T) {
	f := warm(t)
	code, _ := f.post(t, "locate", "acme", Query{Repo: "cloud", Path: "a.go", Relation: "everything"})
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", code)
	}
	if len(f.calls) != 0 {
		t.Fatalf("daemon saw %v; a refused relation must cost no round trip", f.paths())
	}
}

// TestAColdRevisionSendsTheTreeAndAsksAgainOnce proves the 409 contract: the
// daemon says it holds no root, this side supplies one, and asks EXACTLY once
// more.
func TestAColdRevisionSendsTheTreeAndAsksAgainOnce(t *testing.T) {
	f := newFleet(t, testKey, source())

	code, body := f.post(t, "hover", "acme", Query{Repo: "cloud", Path: "a.go"})
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	if got := f.paths(); !slices.Equal(got, []string{"/ask", "/root", "/ask"}) {
		t.Fatalf("daemon saw %v, want [/ask /root /ask]", got)
	}
	// The tree is keyed by the caller's org and the RESOLVED commit, and carries
	// the repository's text.
	sent := f.sent()
	if sent.Org != "acme" || sent.Repo != "cloud" || sent.Rev != testSHA {
		t.Errorf("tree keyed (%q,%q,%q), want (acme,cloud,%s)", sent.Org, sent.Repo, sent.Rev, testSHA)
	}
	if len(sent.Files) != 1 || sent.Files[0].Path != "a.go" || sent.Files[0].Content != "package p\n" {
		t.Errorf("tree files = %+v, want a.go with its content", sent.Files)
	}
	// Preparing a revision is the BILLED event, and the answer says so.
	var out Answer
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode answer %s: %v", body, err)
	}
	if !out.Cold {
		t.Error("cold = false; the request that prepared the revision must report it")
	}
	if out.Rev != testSHA || out.Repo != "cloud" || out.Path != "a.go" {
		t.Errorf("answer echoed (%q,%q,%q)", out.Repo, out.Rev, out.Path)
	}
}

// TestTheRetryIsOnceAndNotALoop bounds the retry. A daemon that refuses again
// after being given a tree — one evicting roots as fast as they arrive — gets one
// tree and one more ask, never a third, and the caller gets an outage.
func TestTheRetryIsOnceAndNotALoop(t *testing.T) {
	f := newFleet(t, testKey, source())
	f.stuck = true

	code, _ := f.post(t, "hover", "acme", Query{Repo: "cloud", Path: "a.go"})
	if code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 — a daemon that never holds a root is an outage", code)
	}
	if got := f.paths(); !slices.Equal(got, []string{"/ask", "/root", "/ask"}) {
		t.Fatalf("daemon saw %v, want exactly [/ask /root /ask] — the retry is once", got)
	}
}

// TestTheOrgIsThePrincipalsAndNeverTheBodys is the tenant boundary. A body that
// names another org is answered for the principal's org, because Query has no org
// field for one to land in — and the git plane is asked for that same org, so a
// repository slug can only ever resolve under the caller.
func TestTheOrgIsThePrincipalsAndNeverTheBodys(t *testing.T) {
	f := newFleet(t, testKey, source())

	body := map[string]any{
		"repo": "cloud", "path": "a.go",
		"org": "victim", "Org": "victim", "owner": "victim", "tenant": "victim",
	}
	code, out := f.post(t, "hover", "acme", body)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, out)
	}
	for _, c := range f.calls {
		if c.path == "/ask" && c.ask.Org != "acme" {
			t.Errorf("/ask carried org %q, want acme", c.ask.Org)
		}
		if c.path == "/root" && c.tree.Org != "acme" {
			t.Errorf("/root carried org %q, want acme", c.tree.Org)
		}
	}
	if len(f.orgs) == 0 {
		t.Fatal("the forge was never read")
	}
	for _, org := range f.orgs {
		if org != "acme" {
			t.Errorf("forge read for org %q, want acme", org)
		}
	}
	// The principal has to RIDE the read, not merely precede it: the forge login
	// this acts as is resolved from that context, so a read made without it has
	// nobody to act as and would fall back to nothing.
	for _, sub := range f.subs {
		if sub != "u_acme" {
			t.Errorf("forge read rode principal %q, want the caller's own", sub)
		}
	}
}

// TestNoPrincipalIsRefusedBeforeAnythingIsReached is the fail-closed spine: an
// anonymous request reaches neither the forge nor the daemon.
func TestNoPrincipalIsRefusedBeforeAnythingIsReached(t *testing.T) {
	f := warm(t)
	code, _ := f.post(t, "hover", "", Query{Repo: "cloud", Path: "a.go"})
	if code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", code)
	}
	if len(f.calls) != 0 || len(f.orgs) != 0 {
		t.Fatalf("an anonymous request reached daemon=%v forge=%v", f.paths(), f.orgs)
	}
}

// TestEveryDaemonCallPresentsTheKey proves the shared service key is sent on both
// doors. The daemon compares it in constant time and 401s without it, so a
// forgotten header is a fleet that cannot answer at all.
func TestEveryDaemonCallPresentsTheKey(t *testing.T) {
	f := newFleet(t, testKey, source())

	if code, body := f.post(t, "locate", "acme", Query{Repo: "cloud", Path: "a.go"}); code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	if len(f.calls) == 0 {
		t.Fatal("daemon saw nothing")
	}
	for _, c := range f.calls {
		if c.key != testKey {
			t.Errorf("%s presented key %q, want the configured one", c.path, c.key)
		}
	}
}

// TestAnUnkeyedProxyRefusesRatherThanCallsOut is the other half of the same fact:
// a deployment that never got LSP_KEY fails CLOSED here rather than making a call
// the daemon would refuse anyway.
func TestAnUnkeyedProxyRefusesRatherThanCallsOut(t *testing.T) {
	f := newFleet(t, "", source())
	f.held = true

	code, _ := f.post(t, "hover", "acme", Query{Repo: "cloud", Path: "a.go"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", code)
	}
	if len(f.calls) != 0 {
		t.Fatalf("daemon saw %v; an unkeyed proxy must not call out", f.paths())
	}
}

// TestNewDaemonReadsItsWiringFromTheEnvironment proves the two facts a Deployment
// supplies — where the daemon is and which key reaches it — are read from the
// environment and nowhere else, and that the address has a working default.
func TestNewDaemonReadsItsWiringFromTheEnvironment(t *testing.T) {
	t.Setenv(upstreamEnv, "")
	t.Setenv(keyEnv, "")
	if d := newDaemon(); d.url != upstreamDefault || d.key != "" {
		t.Fatalf("unconfigured daemon = (%q,%q), want (%q,\"\")", d.url, d.key, upstreamDefault)
	}
	t.Setenv(upstreamEnv, "http://elsewhere:9000")
	t.Setenv(keyEnv, "  k  ")
	if d := newDaemon(); d.url != "http://elsewhere:9000" || d.key != "k" {
		t.Fatalf("configured daemon = (%q,%q)", d.url, d.key)
	}
}

// TestOnlySourceIsSentToTheDaemon proves the tree read is filtered: a binary blob
// and a truncated one are dropped. A binary spends the daemon's tree budget on
// bytes no parser reads; a truncated file is a HALF file, and type-checking one
// invents errors that are not in the repository.
func TestOnlySourceIsSentToTheDaemon(t *testing.T) {
	f := newFleet(t, testKey, []forge.File{
		{Path: "a.go", Data: []byte("package p\n")},
		{Path: "logo.png", Data: []byte{0x89, 'P', 'N', 'G', 0x00, 0x1a}},
		{Path: "huge.go", Truncated: true},
	})

	if code, body := f.post(t, "hover", "acme", Query{Repo: "cloud", Path: "a.go"}); code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	sent := f.sent()
	if len(sent.Files) != 1 || sent.Files[0].Path != "a.go" {
		t.Fatalf("tree carried %+v, want only a.go", sent.Files)
	}
}

// TestTheTreeIsReadAtTheResolvedCommit proves the forge read is pinned to the
// commit the daemon was told about, not to the ref the caller named — so the tree
// and the root key can never come from two sides of a push.
func TestTheTreeIsReadAtTheResolvedCommit(t *testing.T) {
	f := newFleet(t, testKey, source())

	if code, body := f.post(t, "hover", "acme",
		Query{Repo: "cloud", Rev: "main", Path: "a.go"}); code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	if !slices.Equal(f.refs, []string{testSHA}) {
		t.Errorf("tree read at %v, want the resolved commit [%s]", f.refs, testSHA)
	}
}

// TestAMalformedRequestCostsNoCall proves the narrowing happens HERE: a slug or a
// ref that cannot name anything is refused before the forge or the daemon is
// reached.
func TestAMalformedRequestCostsNoCall(t *testing.T) {
	f := warm(t)

	for _, in := range []Query{
		{Repo: "", Path: "a.go"},
		{Repo: "../other", Path: "a.go"},
		{Repo: "a/b", Path: "a.go"},
		{Repo: "-flag", Path: "a.go"},
		{Repo: "cloud", Path: ""},
		{Repo: "cloud", Rev: "--upload-pack=x", Path: "a.go"},
		{Repo: "cloud", Path: "a.go", Line: -1},
		{Repo: "cloud", Path: "a.go", Character: -1},
	} {
		if code, _ := f.post(t, "hover", "acme", in); code != http.StatusBadRequest {
			t.Errorf("%+v: status=%d, want 400", in, code)
		}
	}
	if len(f.calls) != 0 || len(f.orgs) != 0 {
		t.Fatalf("a malformed request reached daemon=%v forge=%v", f.paths(), f.orgs)
	}
}

// TestReaderRefusesAnUnmappedTenant pins the first of the two tenancy controls
// on the repository read. An IAM org reaches a forge namespace ONLY through
// forge.Owner's closed table — never by being spelled like one — and it is
// refused before a credential is spent on it. The second control is the sudo
// actor, which forge/tree_test.go pins at the wire.
func TestReaderRefusesAnUnmappedTenant(t *testing.T) {
	for _, org := range []string{"hanzoai", "acme", "admin", ""} {
		if _, _, err := reader(context.Background(), org); !errors.Is(err, forge.ErrNoOwner) {
			t.Fatalf("org %q was refused as %v, want ErrNoOwner", org, err)
		}
	}
}
