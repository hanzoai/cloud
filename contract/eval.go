package contract

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// A contract can be WRITTEN as code — hanzo.js, hanzo.ts, hanzo.go — and code is a
// generator, never the contract. It runs exactly once, here, at the edge where the
// project's own toolchain already is, and what it prints is the document.
// Everything downstream — CI, platform.hanzo.ai, the operator, and a chain that one
// day witnesses a release — reads that document and nothing else.
//
// The reason is what those readers have to agree on. A document has a hash, so two
// of them can show they read the same declaration, and a signature over it still
// means something a year later. A program has no such thing: agreeing on "run this
// JavaScript and see what deploys" would need a build box, an operator pod and a
// validator each to run it and each to get the same answer, which none of them can
// promise the others. So the code runs in one place, and what travels is data.
//
// That is also why the evaluator is a separate door from Load. A reader holding
// only Load cannot execute a repository's file, whatever the repository names it.
//
// WHERE EVAL MAY BE CALLED. A generator is repository-supplied code, so running one
// carries exactly the trust a Dockerfile `RUN` from the same repository already
// carries: it belongs where that belongs — a developer's own checkout, or a build
// container holding no credential — and nowhere else. It inherits the caller's
// environment, because `go run` and `node` need theirs. The serving process holds
// Load and never reaches here.

// toolchains names what each generator spelling is written for. A generator runs on
// the toolchain the project already needs in order to build, so there is nothing
// extra to install and no interpreter embedded here.
var toolchains = map[string][]string{
	".js": {"node"},
	".ts": {"tsx"},
	".go": {"go", "run"},
}

func toolchain(name string) []string { return toolchains[path.Ext(name)] }

// wait bounds one generator. A declaration is printed, not computed.
const wait = 2 * time.Minute

// Eval resolves the contract in dir and returns it in canonical form, evaluating a
// generator spelling on the project's own toolchain. It is the ONE place a
// contract written as code is ever run.
func Eval(ctx context.Context, dir string) (Doc, error) {
	name, body, err := find(func(n string) ([]byte, error) { return open(dir, n) })
	if err != nil {
		return Doc{}, err
	}
	argv := toolchain(name)
	if argv == nil {
		return Parse(name, body)
	}
	out, err := emit(ctx, dir, name, argv)
	if err != nil {
		return Doc{}, err
	}
	return Parse(name, out)
}

// open takes one file from a checkout, bounded. A directory carrying the name is
// absent rather than unreadable — a directory cannot be a document and no author
// ever meant one as one — which is also what reading a tree at a revision answers.
func open(dir, name string) ([]byte, error) {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return nil, err
	} else if info.IsDir() {
		return nil, fs.ErrNotExist
	}
	b, err := io.ReadAll(io.LimitReader(f, Max+1))
	if err != nil {
		return nil, err
	}
	if len(b) > Max {
		return nil, fmt.Errorf("larger than %d bytes", Max)
	}
	return b, nil
}

// emit runs one generator and returns what it printed. Stdout is the document;
// stderr is the generator's own diagnostics, quoted back on failure so a broken
// generator names itself instead of arriving later as a parse error.
func emit(ctx context.Context, dir, name string, argv []string) ([]byte, error) {
	ctx, stop := context.WithTimeout(ctx, wait)
	defer stop()
	cmd := exec.CommandContext(ctx, argv[0], append(append([]string{}, argv[1:]...), name)...)
	cmd.Dir = dir
	out, bad := &clip{max: Max}, &clip{max: 2 << 10}
	cmd.Stdout, cmd.Stderr = out, bad
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %s: %w%s", name, strings.Join(argv, " "), err, said(bad.b.String()))
	}
	return out.b.Bytes(), nil
}

// said quotes a generator's diagnostics into the error, when it wrote any.
func said(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return ""
	}
	return ": " + s
}

// clip collects at most max bytes and refuses the rest, so a generator that never
// stops printing cannot fill memory: refusing the write ends the copy, the child
// sees a closed pipe, and Run reports it.
type clip struct {
	b   bytes.Buffer
	max int
}

func (c *clip) Write(p []byte) (int, error) {
	if c.b.Len()+len(p) > c.max {
		return 0, fmt.Errorf("printed more than %d bytes", c.max)
	}
	return c.b.Write(p)
}
