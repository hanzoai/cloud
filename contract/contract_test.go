package contract

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tree is a repository root held in memory: the shape Read resolves through, with
// absence spelled the one way find recognises.
type tree map[string][]byte

func (t tree) read(name string) ([]byte, error) {
	b, ok := t[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return b, nil
}

// A repository that declares nothing is not a failure — most declare nothing —
// and the answer says so precisely enough for a caller to carry on.
func TestNone(t *testing.T) {
	if _, err := Load(tree{"README.md": []byte("hi")}.read); !errors.Is(err, ErrNone) {
		t.Fatalf("no contract: want ErrNone, got %v", err)
	}
}

// THE PRECEDENCE. One spelling resolves, whichever it is, and the document names
// the file it came from.
func TestOne(t *testing.T) {
	for _, name := range []string{"hanzo.yml", "hanzo.yaml", "hanzo.json"} {
		doc, err := Load(tree{name: []byte(`{"bucket": "plugins"}`)}.read)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if doc.Name != name {
			t.Errorf("%s: read as %q", name, doc.Name)
		}
	}
}

// TWO FILES IS REFUSED, and this is the whole reason a scan probes every name
// instead of stopping at the first hit. Two files each claiming to be the contract
// is genuinely ambiguous; a reader that picked one would disagree with the reader
// that picked the other about what the repository declares.
func TestMany(t *testing.T) {
	_, err := Load(tree{
		"hanzo.yml":  []byte("bucket: plugins\n"),
		"hanzo.json": []byte(`{"bucket": "other"}`),
	}.read)
	if !errors.Is(err, ErrMany) {
		t.Fatalf("two contracts: want ErrMany, got %v", err)
	}
	// It names both, so the fix is obvious without a second look at the repo.
	for _, want := range []string{"hanzo.yml", "hanzo.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
	// A data spelling beside a generator is the same ambiguity.
	if _, err := Load(tree{"hanzo.yml": []byte("a: 1\n"), "hanzo.config.ts": []byte("x")}.read); !errors.Is(err, ErrMany) {
		t.Errorf("yml + config.ts: want ErrMany, got %v", err)
	}
}

// THE REGRESSION THAT MOVED THE GENERATOR NAMES. hanzo-js publishes a browser
// bundle called hanzo.js at its repo root, and hanzo.js was once a generator
// spelling — so resolving that repository meant `node hanzo.js`, the resolver
// running a repository's own published artifact because of what it is called. A
// root hanzo.go was worse: Go would have compiled it into the project as well.
//
// None of those three is a contract now. A repository carrying all of them, and
// nothing else, declares NOTHING — not a document, not a refusal to run one.
func TestOrdinarySourceIsNotAContract(t *testing.T) {
	bundles := tree{
		"hanzo.js": []byte("(function(){/* 300kB of published bundle */})();"),
		"hanzo.ts": []byte("export const version = '1.0.0'\n"),
		"hanzo.go": []byte("package hanzo\n\nfunc Version() string { return \"1.0.0\" }\n"),
	}
	if _, err := Load(bundles.read); !errors.Is(err, ErrNone) {
		t.Fatalf("a repo of ordinary source: want ErrNone, got %v", err)
	}
	// And it does not become ambiguous beside a real contract: the repository
	// declares the one document it wrote, and the bundle stays a bundle.
	with := tree{"hanzo.yml": []byte("bucket: plugins\n")}
	for name, body := range bundles {
		with[name] = body
	}
	doc, err := Load(with.read)
	if err != nil {
		t.Fatalf("contract beside a bundle: %v", err)
	}
	if doc.Name != "hanzo.yml" {
		t.Errorf("resolved %q", doc.Name)
	}
	// The names that ARE generators cannot be ordinary source: nothing is bundled
	// to hanzo.config.js, and .hanzo/contract.go is out of the repo's own package.
	for _, name := range Names {
		if toolchain(name) == nil {
			continue
		}
		if _, taken := bundles[name]; taken {
			t.Errorf("%s is a generator spelling AND a name ordinary source has", name)
		}
	}
}

// THE SCAN ORDER DECIDES NOTHING, which is the claim Names makes about itself.
// Shuffle it and every answer is the same, because a tie is refused rather than
// broken — so no reader's answer can depend on where its scan started.
func TestReorderIsNotATieBreak(t *testing.T) {
	one := tree{"hanzo.json": []byte(`{"bucket": "plugins"}`)}
	two := tree{"hanzo.yml": []byte("a: 1\n"), ".hanzo/contract.go": []byte("package main")}

	was := Names
	t.Cleanup(func() { Names = was })
	first, err := Load(one.read)
	if err != nil {
		t.Fatal(err)
	}

	// Reversed from Names ITSELF, not from a second list written out here: a copy
	// would drift, and then this would be reversing something Names no longer says.
	Names = append([]string{}, was...)
	slices.Reverse(Names)
	again, err := Load(one.read)
	if err != nil {
		t.Fatalf("reversed scan: %v", err)
	}
	if again.Name != first.Name || again.Digest() != first.Digest() {
		t.Errorf("the scan order changed the answer: %s/%s then %s/%s",
			first.Name, first.Digest(), again.Name, again.Digest())
	}
	if _, err := Load(two.read); !errors.Is(err, ErrMany) {
		t.Errorf("reversed scan, two contracts: %v", err)
	}
}

// A file that is present but unreadable must never pass for an absent one, or a
// repository that declares a build silently stops building.
func TestUnreadable(t *testing.T) {
	boom := errors.New("disk said no")
	_, err := Load(func(name string) ([]byte, error) {
		if name == "hanzo.yml" {
			return nil, boom
		}
		return nil, fs.ErrNotExist
	})
	if !errors.Is(err, boom) {
		t.Fatalf("want the read error, got %v", err)
	}
	if errors.Is(err, ErrNone) {
		t.Error("an unreadable contract was reported as none")
	}
}

// THE DIGEST PROPERTY, and the reason any of this is safe: one contract written
// three ways is one document with one hash. A receipt — or a chain — can name what
// a repository declared without holding the declaration, and cannot be made to
// disagree by re-spelling the file.
func TestSameDigest(t *testing.T) {
	spellings := map[string]string{
		"hanzo.yml": `
bucket: plugins
binaries:
  - name: demo
    main: ./cmd/demo
    platforms: [linux/amd64, darwin/arm64]
retries: 3
`,
		"hanzo.yaml": `
retries: 3
binaries:
- platforms:
  - linux/amd64
  - darwin/arm64
  main: ./cmd/demo
  name: demo
bucket: plugins
`,
		"hanzo.json": `{
	"retries": 3,
	"bucket": "plugins",
	"binaries": [
		{"name": "demo", "main": "./cmd/demo", "platforms": ["linux/amd64", "darwin/arm64"]}
	]
}`,
	}
	var first Doc
	for name, body := range spellings {
		doc, err := Load(tree{name: []byte(body)}.read)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if first.Name == "" {
			first = doc
			continue
		}
		if doc.Digest() != first.Digest() {
			t.Fatalf("%s digest %s != %s digest %s\n  %s\n  %s",
				name, doc.Digest(), first.Name, first.Digest(), doc.Data, first.Data)
		}
	}
	// The canonical form is JSON with names in sorted order — the property that
	// makes the digests above agree, stated as bytes.
	const want = `{"binaries":[{"main":"./cmd/demo","name":"demo","platforms":["linux/amd64","darwin/arm64"]}],"bucket":"plugins","retries":3}`
	if string(first.Data) != want {
		t.Errorf("canonical form:\n got %s\nwant %s", first.Data, want)
	}
}

// A different declaration is a different digest. Without this the sameness above
// could be a hash of nothing.
func TestDigestMoves(t *testing.T) {
	a, err := Load(tree{"hanzo.yml": []byte("bucket: plugins\n")}.read)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(tree{"hanzo.yml": []byte("bucket: other\n")}.read)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest() == b.Digest() {
		t.Fatal("two declarations, one digest")
	}
	if len(a.Digest()) != 64 {
		t.Errorf("digest is not a sha256 in hex: %q", a.Digest())
	}
}

// Values that the two spellings write differently still land on one document: a
// number keeps its literal where JSON can spell it the same way, a date is text
// because JSON has no other way to hold one, and comments and key order are not
// part of what a contract says.
func TestValues(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"a: 3\n", `{"a":3}`},
		{"a: 10000000000000000001\n", `{"a":10000000000000000001}`}, // every digit survives
		{"a: 0o755\n", `{"a":493}`},                                 // a YAML-only spelling means its value
		{"a: 1_000\n", `{"a":1000}`},
		{"a: true\nb: yes\n", `{"a":true,"b":"yes"}`}, // YAML 1.2: only true/false are bool
		{"a: 2026-08-28\n", `{"a":"2026-08-28"}`},     // a date is text, as JSON must spell it
		{"a: ~\n", `{"a":null}`},
		{"# comment\nb: 1\na: 2\n", `{"a":2,"b":1}`},
		{"a: \"x && y\"\n", `{"a":"x && y"}`}, // not escaped into &
	} {
		doc, err := Load(tree{"hanzo.yml": []byte(c.in)}.read)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if string(doc.Data) != c.want {
			t.Errorf("%q\n got %s\nwant %s", c.in, doc.Data, c.want)
		}
	}
}

// Anchors and merges are how a YAML author says a thing once. They are spelling,
// so they are resolved before the document is hashed — a `<<` left in the
// canonical form would be a name no reader knows, with a different hash in JSON.
func TestAnchors(t *testing.T) {
	doc, err := Load(tree{"hanzo.yml": []byte(`
base: &base
  context: .
  dockerfile: Dockerfile
images:
  - <<: *base
    name: api
    dockerfile: api/Dockerfile
`)}.read)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"base":{"context":".","dockerfile":"Dockerfile"},"images":[{"context":".","dockerfile":"api/Dockerfile","name":"api"}]}`
	if string(doc.Data) != want {
		t.Fatalf("merge:\n got %s\nwant %s", doc.Data, want)
	}
}

// A declaration is data, and reading data is bounded work. An anchor referenced
// from inside its own value is a cycle; an anchor referenced repeatedly over
// several levels expands exponentially. Neither shows in the file's size.
func TestBounded(t *testing.T) {
	var b strings.Builder
	fmt.Fprintln(&b, "a0: &a0 [x, x, x, x, x, x, x, x, x]")
	for level := 1; level < 9; level++ {
		fmt.Fprintf(&b, "a%d: &a%d [", level, level)
		for ref := 0; ref < 9; ref++ {
			if ref > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "*a%d", level-1)
		}
		b.WriteString("]\n")
	}
	if _, err := Load(tree{"hanzo.yml": []byte(b.String())}.read); err == nil {
		t.Fatal("a document that expands to 9^9 values was accepted")
	}
}

// A name is declared once. Taking the last of two would let half a declaration
// disappear without a word — an `images:` written twice is a build that never
// happens for the images in the block that lost.
func TestTwice(t *testing.T) {
	_, err := Load(tree{"hanzo.yml": []byte("images: [{name: a}]\nimages: [{name: b}]\n")}.read)
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate name: %v", err)
	}
	_, err = Load(tree{"hanzo.json": []byte(`{"images": [], "images": []}`)}.read)
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate name in json: %v", err)
	}
}

// A contract declares names. A list or a bare word at the top is not a contract,
// and saying so is what stops a generator that prints its own error message from
// being read as a valid, empty declaration.
func TestShape(t *testing.T) {
	for _, body := range []string{"- a\n- b\n", "hello\n", "42\n"} {
		if _, err := Load(tree{"hanzo.yml": []byte(body)}.read); err == nil {
			t.Errorf("%q was accepted as a contract", body)
		}
	}
	// A file that declares nothing — empty, or nothing but comments, which is how
	// a contract gets parked — is a contract declaring nothing, and that is not the
	// same as no contract at all.
	for _, body := range []string{"", "# nothing here yet\n", "---\n"} {
		doc, err := Load(tree{"hanzo.yml": []byte(body)}.read)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if string(doc.Data) != "{}" {
			t.Errorf("%q: %s", body, doc.Data)
		}
	}
}

// LOAD READS AND DOES NOT RUN. Every downstream reader — CI, platform, the
// operator — holds this endpoint and no other, so a repository cannot get code
// executed by naming its declaration hanzo.config.ts.
func TestLoadRefusesCode(t *testing.T) {
	for _, name := range []string{"hanzo.config.js", "hanzo.config.ts", ".hanzo/contract.go"} {
		_, err := Load(tree{name: []byte("print('nope')")}.read)
		if err == nil {
			t.Errorf("%s: Load accepted a generator", name)
			continue
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s: error does not name the file: %v", name, err)
		}
	}
}

// Eval is the one endpoint that runs a generator, and what it produces is an ordinary
// document: the same canonical form, the same digest as the data spelling of the
// same declaration.
func TestEvalGenerator(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".hanzo/contract.go", `package main

import "fmt"

func main() { fmt.Println(`+"`"+`{"bucket": "plugins", "retries": 3}`+"`"+`) }
`)
	// It is out of the project's own package, which is the whole reason it lives
	// under .hanzo/, and that claim is asked of the toolchain rather than believed:
	// `go build ./...` here compiles the project and never the generator, because
	// the go tool skips a dot-directory when it walks packages.
	// The go directive stays BELOW the toolchain running this test, so the build is
	// local and nothing is fetched.
	write(t, dir, "go.mod", "module contract.test\n\ngo 1.24\n")
	write(t, dir, "lib.go", "package lib\n")
	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("a generator under .hanzo/ was compiled into the project: %v: %s", err, out)
	}

	doc, err := Eval(context.Background(), dir)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if doc.Name != ".hanzo/contract.go" {
		t.Errorf("name %q", doc.Name)
	}
	same, err := Load(tree{"hanzo.yml": []byte("bucket: plugins\nretries: 3\n")}.read)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Digest() != same.Digest() {
		t.Fatalf("generated %s (%s) != declared %s (%s)", doc.Data, doc.Digest(), same.Data, same.Digest())
	}
}

// A generator that fails names itself and says what it said, so a broken
// declaration is not reported as a parse error a hundred lines away.
func TestEvalRefuses(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".hanzo/contract.go", `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "the registry is not configured")
	os.Exit(1)
}
`)
	_, err := Eval(context.Background(), dir)
	if err == nil {
		t.Fatal("a generator that exited 1 was accepted")
	}
	for _, want := range []string{".hanzo/contract.go", "the registry is not configured"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not carry %q: %v", want, err)
		}
	}

	// Output that is not a document is refused the same way, naming the file.
	other := t.TempDir()
	write(t, other, ".hanzo/contract.go", `package main

import "fmt"

func main() { fmt.Println("built ok") }
`)
	if _, err := Eval(context.Background(), other); err == nil || !strings.Contains(err.Error(), ".hanzo/contract.go") {
		t.Errorf("non-document output: %v", err)
	}
}

// Eval reads a data spelling from disk without running anything.
func TestEvalReadsData(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "hanzo.yml", "bucket: plugins\n")
	doc, err := Eval(context.Background(), dir)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if string(doc.Data) != `{"bucket":"plugins"}` {
		t.Errorf("%s", doc.Data)
	}
	if _, err := Eval(context.Background(), t.TempDir()); !errors.Is(err, ErrNone) {
		t.Errorf("empty dir: %v", err)
	}

	// A PATH THAT CANNOT BE HOLDING A DOCUMENT IS ABSENT, not unreadable, and
	// reading a tree at a revision already answers that way — so the checkout reader
	// and the git reader cannot disagree about the same repository. Two shapes reach
	// it: a directory carrying one of the names, and a plain file where .hanzo/ has
	// to be a directory for the name under it to exist at all.
	for _, odd := range []func(t *testing.T, dir string){
		func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, ".hanzo", "contract.go"), 0o700); err != nil {
				t.Fatal(err)
			}
		},
		func(t *testing.T, dir string) { write(t, dir, ".hanzo", "not a directory\n") },
	} {
		both := t.TempDir()
		odd(t, both)
		write(t, both, "hanzo.yml", "bucket: plugins\n")
		doc, err = Eval(context.Background(), both)
		if err != nil {
			t.Fatalf("resolution stopped on a path that cannot be a document: %v", err)
		}
		if doc.Name != "hanzo.yml" {
			t.Errorf("resolved %q", doc.Name)
		}
	}
}

// A LINK IS NOT A CONTRACT — the third shape of "a path that cannot be holding a
// document", and the one with teeth.
//
// A contract is a file a repository CONTAINS, never a pointer to one it does not.
// A tree stores a link as a link, mode 120000, whose bytes are the target's PATH:
// a reader at a revision never sees what the link points at, and cannot descend
// one at all. So following one here is not a smaller reading of the same
// repository, it is a DIFFERENT repository — and for `.hanzo` it is a different
// repository's CODE, because a generator is `go run` and one link is enough to
// aim that at any file on the box. The link's own text is not a way in either: it
// is a path, and a path is not a declaration.
//
// Every shape below answers the same as an empty directory does, which is the
// answer a tree at a revision gives for all of them.
func TestLinkIsNotAContract(t *testing.T) {
	for _, c := range []struct {
		what string
		link func(t *testing.T, repo, outside string)
	}{
		// .hanzo is a link: the whole repository is one entry, and resolving it
		// would run a generator the repository does not carry.
		{".hanzo -> outside", func(t *testing.T, repo, outside string) {
			link(t, outside, filepath.Join(repo, ".hanzo"))
		}},
		// The same aimed by a relative path, which survives being cloned anywhere
		// the shape above it repeats.
		{".hanzo -> ../outside", func(t *testing.T, repo, outside string) {
			link(t, "../outside", filepath.Join(repo, ".hanzo"))
		}},
		// .hanzo/ is real; the generator inside it is the link.
		{".hanzo/contract.go -> outside", func(t *testing.T, repo, outside string) {
			write(t, repo, ".hanzo/keep", "")
			link(t, filepath.Join(outside, "contract.go"), filepath.Join(repo, ".hanzo", "contract.go"))
		}},
		// A data spelling is only read, never run — and it is still a different
		// repository's document, carrying whatever that one declares.
		{"hanzo.yml -> outside", func(t *testing.T, repo, outside string) {
			link(t, filepath.Join(outside, "hanzo.yml"), filepath.Join(repo, "hanzo.yml"))
		}},
		// A link needs no target to be a problem: the text alone parses.
		{`hanzo.yml -> "images: [{name: link/text}]"`, func(t *testing.T, repo, _ string) {
			link(t, "images: [{name: link/text}]", filepath.Join(repo, "hanzo.yml"))
		}},
	} {
		t.Run(c.what, func(t *testing.T) {
			base := t.TempDir()
			repo, outside := filepath.Join(base, "repo"), filepath.Join(base, "outside")
			if err := os.MkdirAll(repo, 0o700); err != nil {
				t.Fatal(err)
			}
			// What the link would reach: a generator that reports having run, and
			// a document declaring an image nobody in the repository asked for.
			ran := filepath.Join(base, "ran")
			write(t, outside, "contract.go", `package main

import (
	"fmt"
	"os"
)

func main() {
	os.WriteFile(`+fmt.Sprintf("%q", ran)+`, nil, 0o600)
	fmt.Println(`+"`"+`{"images":[{"name":"outside"}]}`+"`"+`)
}
`)
			write(t, outside, "hanzo.yml", "images: [{name: outside}]\n")
			c.link(t, repo, outside)

			if _, err := Eval(t.Context(), repo); !errors.Is(err, ErrNone) {
				t.Errorf("a linked contract resolved: %v", err)
			}
			if _, err := os.Stat(ran); err == nil {
				t.Error("the generator ran: a link made Eval run a file the repository does not contain")
			}
		})
	}
}

// And a link does not shadow the contract a repository actually wrote, or make it
// ambiguous: it is absent, so the real one resolves alone.
func TestLinkBesideAContract(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "hanzo.yaml", "bucket: plugins\n")
	link(t, "/etc/passwd", filepath.Join(dir, "hanzo.yml"))
	doc, err := Eval(t.Context(), dir)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if doc.Name != "hanzo.yaml" || string(doc.Data) != `{"bucket":"plugins"}` {
		t.Errorf("resolved %q = %s", doc.Name, doc.Data)
	}
}

// THE CONTRACTS ALREADY COMMITTED. This repository's own hanzo.yml is a real
// one — mostly comments, a `test:` block, no `images:` and no `binaries:` —
// and it must read exactly as it always has: a document, with no image to build,
// and no new name required to be present.
func TestOwnContract(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "hanzo.yml"))
	if err != nil {
		t.Fatalf("read the repo's own contract: %v", err)
	}
	doc, err := Load(tree{"hanzo.yml": body}.read)
	if err != nil {
		t.Fatalf("the repo's own contract does not read: %v", err)
	}
	var declared struct {
		Images   []map[string]any `json:"images"`
		Binaries []map[string]any `json:"binaries"`
		Test     []map[string]any `json:"test"`
	}
	if err := doc.Into(&declared); err != nil {
		t.Fatalf("into: %v", err)
	}
	if len(declared.Images) != 0 || len(declared.Binaries) != 0 {
		t.Errorf("this repo declares no images and no binaries; got %d/%d", len(declared.Images), len(declared.Binaries))
	}
	if len(declared.Test) == 0 {
		t.Error("the test block did not survive the read")
	}
	if doc.Digest() == "" {
		t.Error("no digest")
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	at := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(at), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(at, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// link points at, which needs no target to exist — a link is its text.
func link(t *testing.T, target, at string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(at), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}
}
