package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/contract"
	luxlog "github.com/luxfi/log"
)

// build_on_push_test.go proves the PURE core of the native CI/CD orchestrator: the
// image parse off a repo's contract, the deterministic enqueue body/tag (which must
// match the ci mode:delegate shape byte-for-byte), and the brand→GitHub-owner map.
// The reactor's IO (tree read + HTTP enqueue) is exercised end-to-end when armed;
// these lock the contract that makes the two build entry points converge instead of
// fork.

func TestPipelineParse(t *testing.T) {
	const cfg = `
images:
  - name: api
    repo: ghcr.io/hanzoai/api
    context: ./api
  - name: web
    repo: ghcr.io/hanzoai/web
    tag-suffix: frontend
deploy:
  on: [main]
`
	doc, err := contract.Parse("hanzo.yml", []byte(cfg))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var p pipeline
	if err := doc.Into(&p); err != nil {
		t.Fatalf("into: %v", err)
	}
	if len(p.Images) != 2 {
		t.Fatalf("want 2 images, got %d", len(p.Images))
	}
	if p.Images[0].Name != "api" || p.Images[0].Repo != "ghcr.io/hanzoai/api" || p.Images[0].Context != "./api" {
		t.Fatalf("image[0] parsed wrong: %+v", p.Images[0])
	}
	if p.Images[1].TagSuffix != "frontend" {
		t.Fatalf("image[1] tag-suffix want frontend, got %q", p.Images[1].TagSuffix)
	}
}

// tree is a repository at one revision: enough of Repository for the reactor's
// read, and nothing else. Reading a path that is not in it answers ErrNoPath,
// which is what the resolver reads as absence.
type tree map[string]string

func (tree) Refs(context.Context) ([]Ref, []Ref, error) { return nil, nil, nil }
func (tree) Resolve(_ context.Context, ref string) (Revision, string, error) {
	return Revision(ref), ref, nil
}
func (tree) DefaultBranch(context.Context) (string, error) { return "main", nil }
func (tree) Log(context.Context, Revision, string, int) ([]Change, error) {
	return nil, nil
}
func (tree) WalkText(context.Context, Revision, int64, func(string, string) error) error {
	return nil
}

func (t tree) Tree(_ context.Context, _ Revision, dir string) ([]Entry, error) {
	var out []Entry
	for path := range t {
		if d, name := filepath.Split(path); strings.TrimSuffix(d, "/") == dir {
			out = append(out, Entry{Name: name, Path: path})
		}
	}
	if len(out) == 0 {
		return nil, ErrNoPath
	}
	return out, nil
}

func (t tree) Blob(_ context.Context, _ Revision, path string, max int64) (Blob, error) {
	body, ok := t[path]
	if !ok {
		return Blob{}, ErrNoPath
	}
	if size := int64(len(body)); max > 0 && size > max {
		return Blob{Path: path, Size: size, Truncated: true}, nil
	}
	return Blob{Path: path, Size: int64(len(body)), Content: []byte(body)}, nil
}

// THE ORCHESTRATOR READS A CONTRACT, NOT A FILENAME. A repo declaring its images
// in any data spelling builds the same, and the log names the file it read.
func TestReadPipelineSpellings(t *testing.T) {
	const images = "images:\n  - name: api\n    repo: ghcr.io/hanzoai/api\n"
	const asJSON = `{"images": [{"name": "api", "repo": "ghcr.io/hanzoai/api"}]}`
	for _, c := range []struct{ name, body string }{
		{"hanzo.yml", images},
		{"hanzo.yaml", images},
		{"hanzo.json", asJSON},
	} {
		pl, from, err := readPipeline(context.Background(), tree{c.name: c.body}, "abc")
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if from != c.name {
			t.Errorf("%s: read from %q", c.name, from)
		}
		if pl == nil || len(pl.Images) != 1 || pl.Images[0].Repo != "ghcr.io/hanzoai/api" {
			t.Errorf("%s: %+v", c.name, pl)
		}
	}

	// A repo declaring nothing is not an error — most repos declare nothing.
	pl, from, err := readPipeline(context.Background(), tree{"README.md": "hi"}, "abc")
	if pl != nil || from != "" || err != nil {
		t.Errorf("no contract: %+v %q %v", pl, from, err)
	}

	// .hanzo/workflows still comes first, and merges.
	pl, from, err = readPipeline(context.Background(), tree{
		".hanzo/workflows/cicd.yml": images,
		"hanzo.yml":                 "images:\n  - name: other\n    repo: ghcr.io/hanzoai/other\n",
	}, "abc")
	if err != nil || pl == nil || len(pl.Images) != 1 || pl.Images[0].Name != "api" {
		t.Errorf("workflows-first: %+v %q %v", pl, from, err)
	}

	// TWO CONTRACTS IS REFUSED, loudly, rather than one of them quietly winning.
	if _, _, err := readPipeline(context.Background(), tree{"hanzo.yml": images, "hanzo.json": asJSON}, "abc"); !errors.Is(err, contract.ErrMany) {
		t.Errorf("two contracts: %v", err)
	}

	// A generator is not run here. The reactor reads what a repo declares.
	_, _, err = readPipeline(context.Background(), tree{"hanzo.config.ts": "export default {}"}, "abc")
	if err == nil || !strings.Contains(err.Error(), "hanzo.config.ts") {
		t.Errorf("generator: %v", err)
	}

	// A repo's PUBLISHED BUNDLE is not its contract. hanzo-js ships hanzo.js at its
	// root; a resolver naming that would have run it. It declares nothing here.
	pl, from, err = readPipeline(context.Background(), tree{"hanzo.js": "(function(){})();"}, "abc")
	if pl != nil || from != "" || err != nil {
		t.Errorf("a bundle named hanzo.js: %+v %q %v", pl, from, err)
	}

	// A contract too big to read is a refusal, not an absence. hanzoai/openapi
	// carries a 2 MB hanzo.yaml that is its aggregated API document and not a
	// declaration at all; reading past it would drop the hanzo.yml beside it and
	// stop that repo building with nothing said.
	_, _, err = readPipeline(context.Background(), tree{
		"hanzo.yml":  images,
		"hanzo.yaml": strings.Repeat("x", contract.Max+1),
	}, "abc")
	if err == nil || !strings.Contains(err.Error(), "hanzo.yaml") {
		t.Errorf("oversized file: %v", err)
	}
}

// TWO CONTRACTS IS LOUDER THAN A BAD MOMENT. The reactor is best-effort, so this
// line is the whole account of a repository that has stopped building — and a
// misconfiguration nobody will notice on its own must not read like a connection
// that dropped once and will be fine next push.
func TestTwoContractsIsLouder(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: hanzo.yml hanzo.json", contract.ErrMany), `"level":"error"`},
		{errors.New("dial tcp: connection refused"), `"level":"warn"`},
		{contract.ErrNone, `"level":"warn"`},
	} {
		var buf bytes.Buffer
		s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test").Output(&buf)}}
		level(s, c.err)("native ci/cd: read pipeline", "err", c.err)
		if got := buf.String(); !strings.Contains(got, c.want) {
			t.Errorf("%v: want %s, logged %s", c.err, c.want, strings.TrimSpace(got))
		}
	}
}

// THE FILES ALREADY IN THE ESTATE. This repository's own hanzo.yml declares no
// images, and must go on meaning exactly that: no build, no error, no new name
// required to be present.
func TestReadPipelineOwnContract(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "hanzo.yml"))
	if err != nil {
		t.Fatalf("read the repo's own contract: %v", err)
	}
	pl, from, err := readPipeline(context.Background(), tree{"hanzo.yml": string(body)}, "abc")
	if err != nil {
		t.Fatalf("the repo's own contract does not read: %v", err)
	}
	if pl != nil || from != "" {
		t.Fatalf("a contract with no images: must enqueue nothing, got %+v from %q", pl, from)
	}
}

func TestEnqueueBody(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	// tag-suffix defaults to name; context defaults to "."; dockerfile to <ctx>/Dockerfile.
	b := enqueueBody(pipelineImage{Name: "api", Repo: "ghcr.io/hanzoai/api"}, "hanzoai/api", "main", sha)
	if b == nil {
		t.Fatal("nil body for a valid image")
	}
	// The tag MUST match the ci mode:delegate shape sha-<short7>-amd64[-suffix].
	if b.Image != "ghcr.io/hanzoai/api:sha-abcdef1-amd64-api" {
		t.Fatalf("image ref: %q", b.Image)
	}
	if b.Repo != "hanzoai/api" || b.SHA != sha || b.Branch != "main" || b.Ref != "refs/heads/main" {
		t.Fatalf("body coords wrong: %+v", b)
	}
	if b.Context != "." || b.Dockerfile != "./Dockerfile" || b.OS != "linux" || b.Arch != "amd64" {
		t.Fatalf("body defaults wrong: %+v", b)
	}

	// Explicit context + dockerfile + tag-suffix are honored.
	b2 := enqueueBody(pipelineImage{Name: "web", Repo: "ghcr.io/hanzoai/web", Context: "svc", Dockerfile: "svc/Prod.Dockerfile", TagSuffix: "frontend"}, "hanzoai/web", "main", sha)
	if b2.Image != "ghcr.io/hanzoai/web:sha-abcdef1-amd64-frontend" {
		t.Fatalf("explicit-suffix image ref: %q", b2.Image)
	}
	if b2.Context != "svc" || b2.Dockerfile != "svc/Prod.Dockerfile" {
		t.Fatalf("explicit ctx/dockerfile not honored: %+v", b2)
	}

	// An image with no repo yields nil (nothing to push to) — the reactor counts it
	// failed and moves on, never panicking.
	if enqueueBody(pipelineImage{Name: "x"}, "hanzoai/x", "main", sha) != nil {
		t.Fatal("expected nil body for an image with no repo")
	}
}

func TestGithubOwnerFor(t *testing.T) {
	cases := map[string]string{
		"hanzo": "hanzoai", "HANZO": "hanzoai", " lux ": "luxfi", "zoo": "zooai",
		"acme": "acme", // an unmapped org is its own owner — forward-safe default
	}
	for in, want := range cases {
		if got := githubOwnerFor(in); got != want {
			t.Fatalf("githubOwnerFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNativeCICDEnabled(t *testing.T) {
	for _, v := range []string{"", "0", "off", "false", "no"} {
		t.Setenv(nativeCICDEnabledEnv, v)
		if nativeCICDEnabled() {
			t.Fatalf("enable=%q should be dormant", v)
		}
	}
	// The flag is the whole switch. It used to also require a shared build token,
	// because the enqueue was an HTTP POST to our own edge; it is a plane call now,
	// and a plane call carries its caller.
	t.Setenv(nativeCICDEnabledEnv, "true")
	if !nativeCICDEnabled() {
		t.Fatal("enable=true should be armed")
	}
}

// TestPipelineMergeShape proves the .hanzo/workflows/ multi-file merge semantics
// through the reactor's own read: two workflow files' images concatenate into one
// pipeline, in the order the directory lists them.
func TestPipelineMergeShape(t *testing.T) {
	pl, from, err := readPipeline(context.Background(), tree{
		".hanzo/workflows/build.yml": "images:\n  - { name: api, repo: ghcr.io/hanzoai/api }\n",
		".hanzo/workflows/extra.yml": "images:\n  - { name: worker, repo: ghcr.io/hanzoai/worker }\n",
	}, "abc")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if pl == nil || len(pl.Images) != 2 {
		t.Fatalf("merge shape wrong: %+v", pl)
	}
	names := []string{}
	for _, i := range pl.Images {
		names = append(names, i.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "api,worker" {
		t.Fatalf("merge lost an image: %v (from %s)", names, from)
	}
}

// A LINK IS NOT A CONTRACT, and this reader answers that the way a checkout does.
//
// git stores a link as a blob of mode 120000 whose bytes are the target's PATH, so
// this reader never sees what a link points at while a checkout follows it. Read
// one as a declaration and the two disagree about the repository they both hold:
// worst case the link's own TEXT parses, and CI queues a build for an image the
// repository declares nowhere — off a tree carrying no such document, which no
// developer can reproduce locally. Absent, on both sides, is what keeps them
// together.
//
// Driven through the real backend, because the claim is about the mode git
// records and only git records it.
func TestLinkedContractIsAbsent(t *testing.T) {
	for _, c := range []struct{ what, target string }{
		{"the link's text is itself a document", "images: [{name: link/text}]"},
		{"the link points inside the repository", "config/hanzo.yml"},
		{"the link points out of the repository", "../../elsewhere/hanzo.yml"},
	} {
		t.Run(c.what, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "config"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, "config"), "hanzo.yml", "images: [{name: elsewhere, repo: ghcr.io/x/y}]\n")
			if err := os.Symlink(c.target, filepath.Join(dir, "hanzo.yml")); err != nil {
				t.Fatal(err)
			}
			repo := committed(t, dir)

			// The mode is the whole claim, so assert it before the answer built on it.
			b, err := repo.Blob(context.Background(), headOf(t, repo), "hanzo.yml", contract.Max)
			if err != nil {
				t.Fatalf("blob: %v", err)
			}
			if !b.Link {
				t.Fatalf("git stored a link and the read model did not say so: %+v", b)
			}

			pl, from, err := readPipeline(context.Background(), repo, "")
			if err != nil {
				t.Fatalf("a linked contract was read: %v", err)
			}
			if pl != nil {
				t.Errorf("a link declared a pipeline: %+v (from %q)", pl, from)
			}
		})
	}
}

// The same for a workflow, which is the sharper shape: .hanzo/workflows/ is read
// straight off the tree, so a repository holding ONE link and no file at all
// would enqueue a build for whatever image the link's target spells. Nothing in
// the tree carries that declaration, and nothing ever would.
func TestLinkedWorkflowIsSkipped(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".hanzo", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("images: [{name: pwn, repo: ghcr.io/attacker/x}]",
		filepath.Join(dir, ".hanzo", "workflows", "build.yml")); err != nil {
		t.Fatal(err)
	}
	// A real workflow beside it still merges, so this skips the link and not the
	// directory.
	writeFile(t, filepath.Join(dir, ".hanzo", "workflows"), "real.yml",
		"images: [{name: api, repo: ghcr.io/hanzoai/api}]\n")
	repo := committed(t, dir)

	pl, from, err := readPipeline(context.Background(), repo, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if pl == nil {
		t.Fatal("the real workflow was lost")
	}
	for _, img := range pl.Images {
		if img.Name == "pwn" {
			t.Errorf("a link's text became a build: %+v (from %q)", img, from)
		}
	}
	if len(pl.Images) != 1 || pl.Images[0].Name != "api" {
		t.Errorf("images %+v (from %q)", pl.Images, from)
	}
}

// A link does not shadow the contract a repository actually wrote.
func TestLinkBesideAContract(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "hanzo.yaml", "images: [{name: api, repo: ghcr.io/hanzoai/api}]\n")
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "hanzo.yml")); err != nil {
		t.Fatal(err)
	}
	repo := committed(t, dir)
	pl, from, err := readPipeline(context.Background(), repo, "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if pl == nil || len(pl.Images) != 1 || pl.Images[0].Name != "api" {
		t.Fatalf("a repository's own contract did not resolve beside a link: %+v (from %q)", pl, from)
	}
}

// committed makes dir a repository holding one commit of everything in it, and
// answers it as the read model — the type the reactor is handed in production.
func committed(t *testing.T, dir string) Repository {
	t.Helper()
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "contract")
	r, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &gitRepository{repo: r}
}

func headOf(t *testing.T, repo Repository) Revision {
	t.Helper()
	rev, _, err := repo.Resolve(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return rev
}
