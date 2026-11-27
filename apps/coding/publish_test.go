package coding

// publish_test.go — a project's site built in a sandbox: what may be uploaded,
// the curl config that uploads it, the step a run takes after it pushes, and a
// publish on its own, end to end against stand-ins for every peer it reaches.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/forge"
	"github.com/zap-proto/zip"
)

// theUpload is a grant as the project app hands it over.
var theUpload = client.SiteUpload{
	URL:    "https://s3.test/hanzo-sites",
	Prefix: "acme/site",
	Fields: client.Fields(map[string]string{"policy": "eyJQT0xJQ1kiOiJ5ZXMifQ==", "x-amz-signature": "SIGNATURE0123456789abcdef",
		"x-amz-algorithm": "AWS4-HMAC-SHA256", "key": "acme/site/", "bucket": "hanzo-sites"}),
}

// A SITE IS WHAT A BUILD LEFT, BOUNDED: an index at its root, no hidden path, no
// name a curl form would read as syntax, and no more than a site may hold.
func TestAManifestIsBoundedAndExact(t *testing.T) {
	got, err := parseManifest("12\tindex.html\n4\tassets/app-1a2b.js\n9\t.well-known/security.txt\n")
	if err != nil || len(got) != 3 || got[1] != (shipFile{rel: "assets/app-1a2b.js", size: 4}) {
		t.Fatalf("a plain site = %+v, %v", got, err)
	}
	for name, out := range map[string]string{
		"no index":         "4\tapp.js\n",
		"a hidden file":    "1\tindex.html\n3\t.env\n",
		"a hidden dir":     "1\tindex.html\n3\t.git/config\n",
		"a climb":          "1\tindex.html\n3\ta/../../etc/passwd\n",
		"a form separator": "1\tindex.html\n3\ta;type=text/html\n",
		"a list separator": "1\tindex.html\n3\ta,b\n",
		"a quote":          "1\tindex.html\n3\ta\"b\n",
		"a space":          "1\tindex.html\n3\ta b.html\n",
		"no size":          "index.html\n",
		"a negative size":  "-1\tindex.html\n",
		"too large":        fmt.Sprintf("1\tindex.html\n%d\tbig.bin\n", siteBytes),
	} {
		if _, err := parseManifest(out); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	var many strings.Builder
	many.WriteString("1\tindex.html\n")
	for i := range siteFiles {
		fmt.Fprintf(&many, "1\tf%d.html\n", i)
	}
	if _, err := parseManifest(many.String()); err == nil {
		t.Error("a site of more than the most files was accepted")
	}
}

// EACH FILE IS ONE POST TO THE GRANT, under the grant's prefix, the grant's
// fields carried verbatim, and the file itself last, as the store requires.
func TestTheUploadNamesEachFileUnderTheGrantsPrefix(t *testing.T) {
	cfg, err := uploadConfig(theUpload, []shipFile{{rel: "index.html"}, {rel: "assets/app.js"}})
	if err != nil {
		t.Fatal(err)
	}
	blocks := strings.Split(cfg, "next\n")
	if len(blocks) != 2 {
		t.Fatalf("%d transfers:\n%s", len(blocks), cfg)
	}
	lines := strings.Split(strings.TrimSpace(blocks[1]), "\n")
	if lines[0] != `url = "https://s3.test/hanzo-sites"` || lines[len(lines)-1] != `form = "file=@assets/app.js"` {
		t.Fatalf("the second transfer is:\n%s", blocks[1])
	}
	for _, want := range []string{
		`form-string = "key=acme/site/assets/app.js"`,
		`form-string = "Content-Type=text/javascript; charset=utf-8"`,
		`form-string = "policy=eyJQT0xJQ1kiOiJ5ZXMifQ=="`,
		`form-string = "x-amz-signature=SIGNATURE0123456789abcdef"`,
		`write-out = "%{http_code}\n"`,
	} {
		if !slices.Contains(lines, want) {
			t.Fatalf("the second transfer lacks %s:\n%s", want, blocks[1])
		}
	}
	if n := strings.Count(blocks[1], "key="); n != 1 {
		t.Fatalf("the second transfer names %d keys:\n%s", n, blocks[1])
	}
	if strings.Contains(cfg, "location") || strings.Contains(cfg, "proxy") {
		t.Fatalf("the config follows or relays:\n%s", cfg)
	}
	if got := curlQuote(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("curlQuote = %s", got)
	}
	for _, up := range []client.SiteUpload{{URL: "http://s3.test/b", Prefix: "acme/site"}, {URL: "https://s3.test/b"}} {
		if _, err := uploadConfig(up, []shipFile{{rel: "index.html"}}); err == nil {
			t.Errorf("a grant %+v was used", up)
		}
	}
}

// AN UPLOAD IS DONE ONLY WHEN EVERY FILE WAS ACCEPTED.
func TestTheUploadIsAcceptedOnlyWhenEveryFileIs(t *testing.T) {
	two := []shipFile{{rel: "index.html"}, {rel: "a.css"}}
	if err := accepted(two, "204\n201\n", ""); err != nil {
		t.Fatalf("two 2xx: %v", err)
	}
	for _, out := range []string{"204\n", "204\n403\n", "204\n000\n", ""} {
		if err := accepted(two, out, "curl: (7) Failed to connect"); err == nil {
			t.Errorf("%q was accepted", out)
		}
	}
}

// ── the stand-ins ────────────────────────────────────────────────────────────

// siteDesk is the project app's half of a publish: the deployments it opened,
// how each ended, and what it answers.
type siteDesk struct {
	mu     sync.Mutex
	opened []client.SiteOpenIn
	closed []client.SiteCloseIn
	orgs   []string
	// owner and repo are the project's repository, as the project app answers it.
	owner, repo string
	refuse      error
	closedCh    chan client.SiteCloseIn
}

func (d *siteDesk) serve(app *zip.App) {
	app.Post("/projects/site/open", func(ctx context.Context, in *client.SiteOpenIn) (*client.SiteOpened, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.opened = append(d.opened, *in)
		d.orgs = append(d.orgs, zip.CallerOf(ctx).Org)
		if d.refuse != nil {
			return nil, d.refuse
		}
		return &client.SiteOpened{ID: "dep_1", Owner: d.owner, Repo: d.repo, Branch: "main", Upload: theUpload}, nil
	}, zip.WithOperationID(client.ProjectsSiteOpen))
	app.Post("/projects/site/close", func(ctx context.Context, in *client.SiteCloseIn) (*client.SiteClosed, error) {
		d.mu.Lock()
		d.closed = append(d.closed, *in)
		d.orgs = append(d.orgs, zip.CallerOf(ctx).Org)
		d.mu.Unlock()
		if d.closedCh != nil {
			d.closedCh <- *in
		}
		if !in.Live {
			return &client.SiteClosed{Status: "error", Message: in.Message}, nil
		}
		return &client.SiteClosed{Status: "live", LiveURL: "https://site.hanzo.app"}, nil
	}, zip.WithOperationID(client.ProjectsSiteClose))
}

// siteAnswers is a pod that builds into dist, lists index.html and a.css there,
// and accepts every upload.
func siteAnswers(p *pod, tip *string) func(argv []string) client.Ran {
	return func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: *tip + "\n"}
		case slices.Contains(argv, "commit"):
			*tip = newSHA
		case slices.Contains(argv, "--stat"):
			return client.Ran{Stdout: " index.html | 1 +\n"}
		case len(argv) == 3 && argv[2] == buildScript:
			return client.Ran{Stdout: "vite v6 building for production...\n"}
		case len(argv) == 3 && argv[2] == findScript:
			return client.Ran{Stdout: "dist"}
		case len(argv) == 5 && argv[2] == listScript:
			return client.Ran{Stdout: "12\tindex.html\n4\ta.css\n"}
		case len(argv) > 2 && strings.Contains(argv[2], "curl -q --config"):
			return client.Ran{Stdout: "204\n204\n"}
		}
		return client.Ran{}
	}
}

// serveDesk stands the project app's site ops up beside servePod's peers.
func serveDesk(t *testing.T, d *siteDesk) {
	t.Helper()
	app := zip.New(zip.Config{AppName: projectpeer.App, DisableStartupMessage: true})
	d.serve(app)
	go func() { _ = app.Listen(zip.SocketPath(projectpeer.App)) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitListening(t, projectpeer.App)
}

// A RUN ON A PROJECT PUBLISHES WHAT IT PUSHED: after the push, the checkout is
// built, listed and uploaded through the grant, the deployment ends live with
// the keys uploaded, and the run says where it is — with the grant blinded out
// of every stream and never on an argv.
func TestARunPublishesWhatItPushed(t *testing.T) {
	tip := theSHA
	p := &pod{}
	p.answer = siteAnswers(p, &tip)
	servePod(t, p)
	d := &siteDesk{owner: "acme", repo: "site"}
	serveDesk(t, d)

	req := aRun()
	req.Site = Site{Project: "site", Owner: "acme", Repo: "site"}
	var said []string
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, steps(&said))
	if err != nil || !res.OK || !res.Changed {
		t.Fatalf("run = %+v, %v", res, err)
	}
	if res.Live != "https://site.hanzo.app" || res.Unpublished != "" {
		t.Fatalf("the run says it is live at %q (unpublished %q)", res.Live, res.Unpublished)
	}
	if len(d.opened) != 1 || d.opened[0] != (client.SiteOpenIn{Slug: "site", Owner: "acme", Repo: "site"}) || d.orgs[0] != "acme" {
		t.Fatalf("opened %+v as %v", d.opened, d.orgs)
	}
	if len(d.closed) != 1 {
		t.Fatalf("closed %+v", d.closed)
	}
	c := d.closed[0]
	if !c.Live || c.Commit != newSHA || c.Files != 2 || c.Bytes != 16 || !slices.Equal(c.Keys, []string{"index.html", "a.css"}) {
		t.Fatalf("the deployment ended %+v", c)
	}

	// Order: the push, then the build, then the upload.
	lines := p.lines()
	push := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, " push ") })
	build := slices.IndexFunc(lines, func(l string) bool { return strings.HasSuffix(l, buildScript) })
	upload := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "curl -q --config") })
	if push < 0 || build < push || upload < build {
		t.Fatalf("push %d, build %d, upload %d:\n%s", push, build, upload, strings.Join(lines, "\n"))
	}
	// The grant crossed on stdin, compressed, into a file — never on an argv.
	for i, l := range lines {
		for _, f := range theUpload.Fields {
			if strings.Contains(l, f.Value) {
				t.Fatalf("command %d names a grant value on its argv: %s", i, l)
			}
		}
	}
	p.mu.Lock()
	var carried string
	for i, argv := range p.ran {
		if slices.Contains(argv, uploadPath) && strings.Contains(argv[2], "base64 -d") {
			carried = p.fed[i]
		}
	}
	curlBlind := p.blind[upload]
	p.mu.Unlock()
	cfg := gunzip(t, carried)
	if !strings.Contains(cfg, `form-string = "key=acme/site/a.css"`) || !strings.Contains(cfg, client.Named(theUpload.Fields)["x-amz-signature"]) {
		t.Fatalf("the upload config carried is:\n%s", cfg)
	}
	for _, f := range theUpload.Fields {
		if !slices.Contains(curlBlind, f.Value) {
			t.Fatalf("the upload does not blind %q: %v", f.Value, curlBlind)
		}
	}
}

// A SITE THAT CANNOT BE PUBLISHED IS SAID, AND THE RUN STANDS: the work is
// pushed either way.
func TestARunWhoseSiteIsRefusedStillDelivers(t *testing.T) {
	tip := theSHA
	p := &pod{}
	p.answer = siteAnswers(p, &tip)
	servePod(t, p)
	d := &siteDesk{owner: "acme", repo: "site", refuse: zip.ErrConflict("projects: this organization has no code workspace yet")}
	serveDesk(t, d)

	req := aRun()
	req.Site = Site{Project: "site", Owner: "acme", Repo: "site"}
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
	if err != nil || !res.OK || !res.Changed || res.Live != "" || !strings.Contains(res.Unpublished, "no code workspace") {
		t.Fatalf("run = %+v, %v", res, err)
	}
	if _, built := p.did(buildScript); built {
		t.Fatal("a refused site was built")
	}
}

// THE PROJECT'S REPOSITORY IS THE RUN'S, or nothing is built and the deployment
// ends failed.
func TestARunPublishesOnlyItsOwnRepository(t *testing.T) {
	tip := theSHA
	p := &pod{}
	p.answer = siteAnswers(p, &tip)
	servePod(t, p)
	d := &siteDesk{owner: "acme", repo: "other"}
	serveDesk(t, d)

	req := aRun()
	req.Site = Site{Project: "site", Owner: "acme", Repo: "site"}
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
	if err != nil || res.Live != "" || res.Unpublished == "" {
		t.Fatalf("run = %+v, %v", res, err)
	}
	if _, built := p.did(buildScript); built {
		t.Fatal("another repository's project was built from this run")
	}
	if len(d.closed) != 1 || d.closed[0].Live {
		t.Fatalf("the deployment ended %+v", d.closed)
	}
}

// A RUN THAT CHANGED NOTHING PUBLISHES NOTHING.
func TestARunThatChangedNothingPublishesNothing(t *testing.T) {
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	d := &siteDesk{owner: "acme", repo: "site"}
	serveDesk(t, d)
	req := aRun()
	req.Site = Site{Project: "site", Owner: "acme", Repo: "site"}
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", req, nil); err != nil {
		t.Fatal(err)
	}
	if len(d.opened) != 0 {
		t.Fatalf("a run with no change opened %+v", d.opened)
	}
}

func gunzip(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	r, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	out, _ := io.ReadAll(r)
	return string(out)
}

// ── a publish on its own ─────────────────────────────────────────────────────

// publishWorld stands up every peer a publish reaches: the forge (world's), KMS
// holding its credential and pinned host key, the project app's workspace
// record and site ops, the sandbox and commerce's funds check.
func publishWorld(t *testing.T) (*world, *siteDesk, *pod, *[]map[string]any) {
	t.Helper()
	w := &world{
		orgs: map[string]string{"hanzoai": "public", "acme": "private"}, repos: map[string]*wrepo{"acme/site": {private: true}},
		projects: map[string]client.ProjectIn{}, ended: make(chan string, 16), book: &shelf{m: map[string]string{"acme": "acme"}},
	}
	var keys []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/keys") {
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			w.mu.Lock()
			keys = append(keys, body)
			w.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		w.forge(rw, r)
	}))
	t.Cleanup(srv.Close)
	w.host = strings.TrimPrefix(srv.URL, "http://")
	t.Setenv("CLOUD_FORGE_HOST", srv.URL)
	forge.Invalidate()
	t.Cleanup(forge.Invalidate)

	tip := theSHA
	p := &pod{}
	p.answer = siteAnswers(p, &tip)
	servePod(t, p)

	kms := zip.New(zip.Config{AppName: "kms", DisableStartupMessage: true})
	kms.Post("/kms/get", func(_ context.Context, in *client.SecretIn) (*client.Secret, error) {
		switch in.Ref {
		case forge.TokenRef:
			return &client.Secret{Value: []byte("machine")}, nil
		case forge.HostKeyRef:
			return &client.Secret{Value: []byte("git.test ssh-ed25519 AAAAPINNED")}, nil
		}
		return nil, zip.ErrNotFound("no such secret")
	}, zip.WithOperationID(client.KMSGet))
	d := &siteDesk{owner: "acme", repo: "site", closedCh: make(chan client.SiteCloseIn, 4)}
	project := zip.New(zip.Config{AppName: projectpeer.App, DisableStartupMessage: true})
	d.serve(project)
	project.Post("/projects/workspace", func(ctx context.Context, _ *client.WorkspaceIn) (*client.Workspace, error) {
		ws, err := forge.Find(ctx, w.book, zip.CallerOf(ctx).Org)
		if err != nil {
			return nil, err
		}
		return &client.Workspace{Owner: ws.Owner, Made: ws.Made}, nil
	}, zip.WithOperationID(client.ProjectsWorkspace))
	for name, app := range map[string]*zip.App{"kms": kms, projectpeer.App: project} {
		go func(path string) { _ = app.Listen(path) }(zip.SocketPath(name))
		t.Cleanup(func() { _ = app.Shutdown() })
		waitListening(t, name)
	}
	return w, d, p, &keys
}

// A PUBLISH ON ITS OWN builds the project's repository at the ref in a sandbox
// of its own: a key that can only READ the one repository, minted and withdrawn
// by the machine; a clone at the ref; the build and the upload; and the
// deployment ended live with what was uploaded.
func TestAPublishBuildsTheProjectsOwnRepository(t *testing.T) {
	w, d, p, keys := publishWorld(t)
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	got, err := Publish(ctx, "acme", client.PublishIn{Project: "site", Ref: "agent/abc123def456"}, nil)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if *got != (client.Publishing{Deployment: "dep_1", Project: "site", Ref: "agent/abc123def456", Status: "building"}) {
		t.Fatalf("admitted as %+v", got)
	}
	var c client.SiteCloseIn
	select {
	case c = <-d.closedCh:
	case <-time.After(10 * time.Second):
		t.Fatal("the publish never ended its deployment")
	}
	if !c.Live || c.Commit != theSHA || !slices.Equal(c.Keys, []string{"index.html", "a.css"}) {
		t.Fatalf("the deployment ended %+v", c)
	}
	select {
	case key := <-w.ended:
		if key != "acme/site" {
			t.Fatalf("a key was withdrawn from %s", key)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the read key was never withdrawn")
	}
	w.mu.Lock()
	if len(*keys) != 1 || (*keys)[0]["read_only"] != true || w.keys[0] != "acme/site" {
		t.Fatalf("keys minted: %v on %v", *keys, w.keys)
	}
	for _, s := range w.sudo {
		if !strings.HasSuffix(s, " as ") {
			t.Fatalf("a publish in a made workspace acted as a person: %s", s)
		}
	}
	w.mu.Unlock()
	if clone, ok := p.did(" clone "); !ok || !strings.Contains(clone, "-b agent/abc123def456") {
		t.Fatalf("the clone was %q", clone)
	}
	p.mu.Lock()
	leased := p.leased
	p.mu.Unlock()
	if len(leased) != 1 || leased[0].Class != "exec" || leased[0].Project != "" {
		t.Fatalf("leased %+v", leased)
	}
}

// A PUBLISH'S SANDBOX IS THE PERSON'S WHO ASKED FOR IT: its lease and every
// command in it state them, their name and the account their request named, as a
// coding run's do. The sandbox app pays a lease from the stated caller's wallet,
// and a publish that stated only the org named no wallet, so every lease was
// refused ("this lease names no wallet") and no template ever went live. The read
// key stays the organization's: nothing on the forge acts as the person.
func TestAPublishLeasesItsSandboxAsThePersonWhoAsked(t *testing.T) {
	w, d, p, _ := publishWorld(t)
	who := zip.Caller{Org: "acme", User: "u_1", Name: "dave", Account: "acme"}
	if _, err := Publish(zip.WithCaller(context.Background(), who), "acme", client.PublishIn{Project: "site", Ref: "main"}, nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case <-d.closedCh:
	case <-time.After(10 * time.Second):
		t.Fatal("the publish never ended its deployment")
	}
	p.mu.Lock()
	lessees, runners := append([]zip.Caller(nil), p.lessees...), append([]zip.Caller(nil), p.runners...)
	p.mu.Unlock()
	if len(lessees) != 1 {
		t.Fatalf("leased %d times", len(lessees))
	}
	stated := func(c zip.Caller) bool {
		return c.Org == "acme" && c.User == "u_1" && c.Name == "dave" && c.Account == "acme"
	}
	if !stated(lessees[0]) {
		t.Fatalf("the sandbox was leased as %+v, not as the person who asked", lessees[0])
	}
	if len(runners) == 0 {
		t.Fatal("nothing ran in the sandbox")
	}
	for _, c := range runners {
		if !stated(c) {
			t.Fatalf("a command ran in the sandbox as %+v, not as its lessee", c)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.sudo {
		if !strings.HasSuffix(s, " as ") {
			t.Fatalf("a publish acted on the forge as a person: %s", s)
		}
	}
}

// THE ESTATE'S PROJECTS BUILD IN THEIR OWN CI: a publish is refused before the
// project app is asked anything.
func TestAPublishIsForAMadeWorkspaceOnly(t *testing.T) {
	_, d, _, _ := publishWorld(t)
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo"})
	if _, err := Publish(ctx, "hanzo", client.PublishIn{Project: "site"}, nil); status(err) != http.StatusForbidden {
		t.Fatalf("the estate's publish answered %v", err)
	}
	// An org with no workspace is refused the same way.
	ctx = zip.WithCaller(context.Background(), zip.Caller{Org: "initech"})
	if _, err := Publish(ctx, "initech", client.PublishIn{Project: "site"}, nil); status(err) != http.StatusForbidden {
		t.Fatalf("an org with no workspace answered %v", err)
	}
	for _, in := range []client.PublishIn{{Project: "../x"}, {Project: "site", Ref: "-x"}, {Project: "site", Owner: "a/b"}} {
		if _, err := Publish(ctx, "acme", in, nil); status(err) != http.StatusBadRequest {
			t.Errorf("%+v answered %v", in, err)
		}
	}
	if len(d.opened) != 0 {
		t.Fatalf("the project app was asked %+v", d.opened)
	}
}

// A PROJECT APP THAT ANSWERS ANOTHER NAMESPACE IS NOT BELIEVED: nothing is
// leased, and the deployment it opened ends failed.
func TestAPublishChecksTheAnswerItIsGiven(t *testing.T) {
	_, d, p, _ := publishWorld(t)
	d.owner = "hanzoai"
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	if _, err := Publish(ctx, "acme", client.PublishIn{Project: "site"}, nil); status(err) != http.StatusForbidden {
		t.Fatalf("a foreign answer was built: %v", err)
	}
	if len(d.closed) != 1 || d.closed[0].Live {
		t.Fatalf("the deployment ended %+v", d.closed)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.leased) != 0 {
		t.Fatalf("a sandbox was leased: %+v", p.leased)
	}
}

// A RUN PUBLISHES ONLY A BUILD ON A PROJECT IN A MADE WORKSPACE.
func TestOnlyABuildInAMadeWorkspacePublishes(t *testing.T) {
	made := forge.Workspace{Owner: "acme", Made: true}
	if got := siteOf(made, false, modeBuild, "site", "site"); got != (Site{Project: "site", Owner: "acme", Repo: "site"}) {
		t.Fatalf("a build in a made workspace publishes to %+v", got)
	}
	for name, got := range map[string]Site{
		"the estate": siteOf(forge.Workspace{Owner: "hanzoai"}, false, modeBuild, "site", "site"),
		"GitHub":     siteOf(made, true, modeBuild, "site", "site"),
		"a plan":     siteOf(made, false, modePlan, "site", "site"),
		"a setup":    siteOf(made, false, modeSetup, "site", "site"),
		"no project": siteOf(made, false, modeBuild, "", "site"),
	} {
		if got != (Site{}) {
			t.Errorf("%s publishes to %+v", name, got)
		}
	}
}

// THE SITE IS WHERE THE BUILD LEFT IT, and the root only of a repository with
// no package.json: the root of one with a package.json is its source.
func TestTheSiteIsWhereTheBuildLeftIt(t *testing.T) {
	for name, c := range map[string]struct {
		files []string
		want  string
	}{
		"a vite build":            {[]string{"package.json", "index.html", "dist/index.html"}, "dist"},
		"a next export":           {[]string{"package.json", "out/index.html"}, "out"},
		"a plain static site":     {[]string{"index.html", "style.css"}, "."},
		"a public directory":      {[]string{"package.json", "public/index.html"}, "public"},
		"source with no build":    {[]string{"package.json", "index.html"}, ""},
		"nothing a browser opens": {[]string{"README.md"}, ""},
	} {
		dir := t.TempDir()
		for _, f := range c.files {
			p := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("sh", "-c", findScript)
		cmd.Dir = dir
		out, err := cmd.Output()
		if got := string(out); got != c.want || (c.want == "") != (err != nil) {
			t.Errorf("%s: found %q (%v), want %q", name, got, err, c.want)
		}
	}
}

// A NEXT.JS APP IS BUILT AS ITS STATIC EXPORT, the only form a site is served
// in: its own config is read through one that asks for `output: 'export'` and
// unoptimized images, whatever shape that config has, and the checkout is left
// as it was found, built or not.
func TestANextAppBuildsItsStaticExport(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; the Next.js export is unverified in this environment")
	}
	// npm stands in for the package manager: `npm run build` reads the config
	// Next.js would read, as Next.js reads it, and writes what it resolved to.
	bin := t.TempDir()
	npm := "#!/bin/sh\nexec " + node + ` --input-type=module -e '
const m = await import(process.cwd() + "/next.config.mjs")
const c = typeof m.default === "function" ? await m.default("phase-production-build", {}) : m.default
console.log(JSON.stringify(c))
if (process.env.FAIL) process.exit(1)' > resolved.json
`
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(npm), 0o755); err != nil {
		t.Fatal(err)
	}
	pkg := `{"scripts":{"build":"next build"},"dependencies":{"next":"14.2.35"}}`
	for name, c := range map[string]struct {
		config map[string]string
		fail   bool
	}{
		"an object in next.config.mjs": {config: map[string]string{"next.config.mjs": "export default { reactStrictMode: true, images: { domains: ['a.test'] } }\n"}},
		"a function in next.config.js": {config: map[string]string{"next.config.js": "module.exports = (phase) => ({ reactStrictMode: phase === 'phase-production-build' })\n"}},
		"no config":                    {},
		"a promise in next.config.mjs": {config: map[string]string{"next.config.mjs": "export default Promise.resolve({ reactStrictMode: true })\n"}},
		"a .ts beside next.config.js":  {config: map[string]string{"next.config.js": "module.exports = { reactStrictMode: true }\n", "next.config.ts": "export default {}\n"}},
		"a build that fails":           {config: map[string]string{"next.config.mjs": "export default { reactStrictMode: true }\n"}, fail: true},
	} {
		dir := t.TempDir()
		files := map[string]string{"package.json": pkg, "node_modules/.keep": ""}
		for f, body := range c.config {
			files[f] = body
		}
		for f, body := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("sh", "-c", buildScript)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		if c.fail {
			cmd.Env = append(cmd.Env, "FAIL=1")
		}
		out, err := cmd.CombinedOutput()
		if (err != nil) != c.fail {
			t.Fatalf("%s: the build answered %v: %s", name, err, out)
		}
		raw, _ := os.ReadFile(filepath.Join(dir, "resolved.json"))
		var got struct {
			Output          string `json:"output"`
			ReactStrictMode bool   `json:"reactStrictMode"`
			Images          struct {
				Unoptimized bool     `json:"unoptimized"`
				Domains     []string `json:"domains"`
			} `json:"images"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: the build resolved %q: %v (%s)", name, raw, err, out)
		}
		if got.Output != "export" || !got.Images.Unoptimized {
			t.Errorf("%s: the build read %s", name, raw)
		}
		if len(c.config) > 0 && !got.ReactStrictMode {
			t.Errorf("%s: the app's own config was not kept: %s", name, raw)
		}
		if _, ok := c.config["next.config.mjs"]; ok && (len(got.Images.Domains) == 0) != (name != "an object in next.config.mjs") {
			t.Errorf("%s: the app's own images config was not kept: %s", name, raw)
		}
		left, _ := os.ReadDir(dir)
		var names []string
		for _, e := range left {
			names = append(names, e.Name())
		}
		for f, body := range c.config {
			if b, _ := os.ReadFile(filepath.Join(dir, f)); string(b) != body {
				t.Errorf("%s: %s reads %q after the build", name, f, b)
			}
		}
		want := len(files) + 1 // what was written, and resolved.json
		if len(names) != want {
			t.Errorf("%s: the checkout holds %v after the build", name, names)
		}
	}
}

// A BUILD THAT IS STOPPED PUTS THE APP'S CONFIG BACK, and one that was killed
// before it could is put back by the next build or before the next commit: the
// wrapper is known by its first line, the app's own config comes back from where
// the build kept it or, when that is gone, from the commit, and nothing of the
// build's is ever staged. A config the app itself deleted stays deleted.
func TestAStoppedOrKilledNextBuildPutsTheConfigBack(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; the Next.js export is unverified in this environment")
	}
	own := "export default { reactStrictMode: true }\n"
	repo := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		for f, body := range map[string]string{
			"package.json":       `{"scripts":{"build":"next build"},"dependencies":{"next":"14.2.35"}}`,
			"next.config.mjs":    own,
			"node_modules/.keep": "",
			".gitignore":         "node_modules\n",
		} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, argv := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "app"}} {
			cmd := exec.Command("git", argv...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", argv, err, out)
			}
		}
		return dir
	}
	sh := func(dir, script string, env ...string) (string, error) {
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	clean := func(t *testing.T, dir, name string) {
		t.Helper()
		if b, _ := os.ReadFile(filepath.Join(dir, "next.config.mjs")); string(b) != own {
			t.Errorf("%s: next.config.mjs reads %q", name, b)
		}
		if st, _ := sh(dir, "git status --porcelain"); st != "" {
			t.Errorf("%s: the tree is not as committed:\n%s", name, st)
		}
		if st, _ := sh(dir, "git add -A && git status --porcelain"); st != "" {
			t.Errorf("%s: staging picks up:\n%s", name, st)
		}
	}

	// Stopped mid-build: the build's own npm signals the shell running it.
	bin := t.TempDir()
	stop := "#!/bin/sh\nkill -TERM $PPID\nsleep 1\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(stop), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := repo(t)
	if _, err := sh(dir, buildScript, "PATH="+bin+":"+os.Getenv("PATH")); err == nil {
		t.Fatal("a stopped build answered success")
	}
	clean(t, dir, "stopped")

	// Killed mid-build: what it left is the wrapper, with the app's config kept
	// beside it, or with that lost too.
	wrapper := func(t *testing.T, dir string) {
		t.Helper()
		bin := t.TempDir()
		wedge := "#!/bin/sh\ncp next.config.mjs wrapper.mjs; cp .hanzo.next.config.mjs kept.mjs 2>/dev/null; kill -KILL $PPID\n"
		if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(wedge), 0o755); err != nil {
			t.Fatal(err)
		}
		_, _ = sh(dir, buildScript, "PATH="+bin+":"+os.Getenv("PATH"))
		for _, f := range []string{"wrapper.mjs", "kept.mjs"} {
			_ = os.Remove(filepath.Join(dir, f))
		}
	}
	for name, lose := range map[string]bool{"killed": false, "killed, its keep lost": true} {
		for _, after := range []string{"the next build", "the next commit"} {
			dir := repo(t)
			wrapper(t, dir)
			if lose {
				_ = os.Remove(filepath.Join(dir, ".hanzo.next.config.mjs"))
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "next.config.mjs")); string(b) == own {
				t.Fatalf("%s: the kill left the app's config in place; the case tests nothing", name)
			}
			if after == "the next build" {
				npm := "#!/bin/sh\nexec " + node + ` --input-type=module -e 'const m = await import(process.cwd() + "/next.config.mjs"); const c = await m.default("phase-production-build", {}); if (c.output !== "export" || !c.reactStrictMode) process.exit(9)'` + "\n"
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(npm), 0o755); err != nil {
					t.Fatal(err)
				}
				if out, err := sh(dir, buildScript, "PATH="+bin+":"+os.Getenv("PATH")); err != nil {
					t.Fatalf("%s, then %s: %v %s", name, after, err, out)
				}
			} else if out, err := sh(dir, tidyNext); err != nil {
				t.Fatalf("%s, then %s: %v %s", name, after, err, out)
			}
			clean(t, dir, name+", then "+after)
		}
	}

	// The wrapper's first shape carried no mark: its import of the kept config
	// is what names it.
	dir = repo(t)
	if err := os.Rename(filepath.Join(dir, "next.config.mjs"), filepath.Join(dir, ".hanzo.next.config.mjs")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "next.config.mjs"), []byte("import own from './.hanzo.next.config.mjs'\nexport default own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := sh(dir, tidyNext); err != nil {
		t.Fatalf("tidy: %v %s", err, out)
	}
	clean(t, dir, "an unmarked wrapper")

	// A config the app deleted is not the build's to bring back.
	dir = repo(t)
	_ = os.Remove(filepath.Join(dir, "next.config.mjs"))
	if out, err := sh(dir, tidyNext); err != nil {
		t.Fatalf("tidy: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "next.config.mjs")); err == nil {
		t.Error("tidying brought back a config the app deleted")
	}
}

// EVERY SCRIPT A PUBLISH RUNS IS SHELL THE SANDBOX CAN PARSE.
func TestThePublishScriptsParse(t *testing.T) {
	for name, script := range map[string]string{"build": buildScript, "find": findScript, "list": listScript} {
		if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
			t.Errorf("the %s script does not parse: %v %s", name, err, out)
		}
	}
}

func TestBuildScriptRewritesLegacyHanzoTsProcessEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"aerva","scripts":{"build":"true"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy := "const apiKey = opts.apiKey || (typeof process !== 'undefined' ? process.env.HANZO_API_KEY : undefined) || '';\n"
	if err := os.WriteFile(filepath.Join(dir, "src", "lib", "hanzo.ts"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", buildScript)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("buildScript failed: %v %s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "lib", "hanzo.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "globalThis") || strings.Contains(string(got), "typeof process !== 'undefined'") {
		t.Fatalf("expected globalThis rewrite in src/lib/hanzo.ts, got: %s", got)
	}
}

// THE UPLOAD CONFIG DRIVES A REAL CURL: each file is one POST carrying the
// grant's fields once, its own key and type, and itself last — against a store
// that refuses a field sent twice, as S3 does.
func TestTheUploadConfigDrivesARealCurl(t *testing.T) {
	type post struct {
		fields map[string]string
		order  []string
		body   string
	}
	var (
		mu    sync.Mutex
		posts []post
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p := post{fields: map[string]string{}}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			b, _ := io.ReadAll(part)
			name := part.FormName()
			if _, twice := p.fields[name]; twice {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			p.fields[name], p.order = string(b), append(p.order, name)
		}
		if len(p.order) == 0 || p.order[len(p.order)-1] != "file" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.body = p.fields["file"]
		mu.Lock()
		posts = append(posts, p)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(dir, "dist")
	for rel, body := range map[string]string{"index.html": "<h1>hi</h1>", "assets/a.css": "b{}"} {
		p := filepath.Join(site, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	up := theUpload
	up.URL = srv.URL + "/hanzo-sites"
	files := []shipFile{{rel: "index.html", size: 11}, {rel: "assets/a.css", size: 3}}
	cfg, err := uploadConfig(up, files)
	if err != nil {
		t.Fatal(err)
	}
	// The test's own CA, per transfer: `next` resets every option but the
	// global ones, which is why each transfer states its own.
	cfg = strings.ReplaceAll(cfg, "silent\n", "silent\ncacert = "+curlQuote(ca)+"\n")
	cfgPath := filepath.Join(dir, "upload")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("curl", "-q", "--config", cfgPath)
	cmd.Dir = site
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, _ := cmd.Output()
	if err := accepted(files, string(out), stderr.String()); err != nil {
		t.Fatalf("curl: %v (stdout %q)", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(posts) != 2 {
		t.Fatalf("%d posts", len(posts))
	}
	got := map[string]post{}
	for _, p := range posts {
		got[p.fields["key"]] = p
	}
	css := got["acme/site/assets/a.css"]
	if css.body != "b{}" || !strings.HasPrefix(css.fields["Content-Type"], "text/css") ||
		css.fields["policy"] != client.Named(up.Fields)["policy"] || css.fields["bucket"] != "hanzo-sites" {
		t.Fatalf("the css post was %+v", css)
	}
	if got["acme/site/index.html"].body != "<h1>hi</h1>" {
		t.Fatalf("posts: %+v", posts)
	}
}
