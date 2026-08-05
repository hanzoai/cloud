package lsp

// workspace.go owns the working tree: check a repo out at a revision, fetch its
// dependencies under the scripts-off policy, and keep the resulting live server
// warm in a bounded pool.
//
// # On not reusing apps/code's checkout
//
// There is nothing to reuse. apps/code holds a static INDEX, not a tree — its
// door is POST /v1/code/index {repo,files:[{path,content}]}, files PUSHED to it
// by a client, and its own doc comment says so ("get_repo_structure over the
// org's own index, with no git checkout involved"). It never clones anything.
//
// The repo does have ONE git working-tree checkout already: apps/deploy's
// unexported gitSource.render — shallow clone, rev-parse, hardened env. It is not
// importable: apps/deploy pulls in the Kubernetes machinery (unstructured, the
// sync engine) that would then be linked into the lsp binary, and each app here
// is its own binary precisely so that does not happen.
//
// So this is the second working-tree checkout in the tree, and that is a real
// duplicate, not a resolved one. It follows deploy's invocation and its hardened
// environment exactly so the two cannot drift in BEHAVIOUR while they wait for
// the fix, which is to hoist the primitive into the root cloud package and have
// both call it. That is a change to a live deployment path and is not made here.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
)

// fetchable is the ENTIRE scripts-off policy: one predicate, one place, read by
// the one caller that runs a dependency fetch.
//
// A fetch that runs dependency-authored code (langs.go: Executes) is remote code
// execution triggered by whatever the caller asked us to check out, and it buys
// nothing a language server needs — definitions and references come from source.
// So it does not run, and no configuration turns it on: the phase-2 sandbox is
// what would earn that, and until it exists an env var here would only be a way
// to lose the argument at 3am.
//
// THE DEPLOYED WORKER MUST STILL BE SANDBOXED. Even scripts-off, this process
// runs third-party language servers over untrusted source. The cloud-lsp worker's
// pod (phase 2) must additionally run non-root with a read-only root filesystem
// and all capabilities dropped, restrict egress to the module mirrors alone, cap
// CPU/memory/PIDs by cgroup, and mount no KMS material, no cloud metadata
// endpoint and no other tenant's volume. Scripts-off narrows the blast radius; it
// is not the boundary.
func fetchable(l Lang) bool { return len(l.Fetch) > 0 && !l.Executes }

// Bounds. A workspace is a checkout plus a language server process, so the cap is
// on memory and file descriptors, not on rows.
const (
	warmMax  = 8                // live workspaces held at once
	warmTTL  = 20 * time.Minute // idle before eviction
	fetchMax = 10 * time.Minute // dependency fetch budget
	cloneMax = 5 * time.Minute  // checkout budget
)

// key names one workspace. org is FIRST and is always the validated principal's
// org: two tenants naming the same repo at the same revision get two keys, two
// directories and two servers, so a warm workspace can never be handed across the
// tenant boundary.
type key struct{ org, repo, rev string }

// Tree is one checked-out revision with its language server attached.
//
// ready is closed when it is usable. It is inserted into the pool BEFORE the slow
// work starts, so a second request for the same key waits for the first checkout
// instead of starting a competing one into the same directory.
type Tree struct {
	key   key
	dir   string
	lang  Lang
	conn  *Conn
	used  time.Time
	ready chan struct{}
	err   error
}

func (t *Tree) close() {
	if t == nil {
		return
	}
	if t.conn != nil {
		t.conn.Close()
	}
	if t.dir != "" {
		_ = os.RemoveAll(t.dir)
	}
}

// pool is the bounded set of warm workspaces, LRU by last use with an idle TTL.
//
// open and now are FIELDS rather than calls so the eviction policy can be tested
// as the arithmetic it is, with no git, no toolchain and no wall clock.
//
// The path is an argument to open rather than part of key on purpose: a workspace
// is a CHECKOUT, and which file you are asking about is not part of which
// checkout you want. Building one still needs it — the path picks the language
// and roots the server — so it is passed, not keyed.
type pool struct {
	mu   sync.Mutex
	warm map[key]*Tree
	max  int
	ttl  time.Duration
	open func(ctx context.Context, k key, path string) (*Tree, error)
	now  func() time.Time
}

func newPool(open func(ctx context.Context, k key, path string) (*Tree, error)) *pool {
	return &pool{
		warm: make(map[key]*Tree),
		max:  warmMax,
		ttl:  warmTTL,
		open: open,
		now:  time.Now,
	}
}

// get returns the workspace for k, building it if it is not warm. The bool
// reports a COLD start — the checkout, the fetch and the server handshake — which
// is the event meter.go charges for.
func (p *pool) get(ctx context.Context, k key, path string) (*Tree, bool, error) {
	p.mu.Lock()
	p.sweep()
	if t, ok := p.warm[k]; ok {
		t.used = p.now()
		p.mu.Unlock()
		select {
		case <-t.ready:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
		if t.err != nil {
			return nil, false, t.err
		}
		return t, false, nil
	}

	t := &Tree{key: k, used: p.now(), ready: make(chan struct{})}
	p.warm[k] = t
	p.evict()
	p.mu.Unlock()

	built, err := p.open(ctx, k, path)
	if err != nil {
		t.err = err
		close(t.ready)
		p.mu.Lock()
		if p.warm[k] == t {
			delete(p.warm, k)
		}
		p.mu.Unlock()
		return nil, true, err
	}
	t.dir, t.lang, t.conn = built.dir, built.lang, built.conn
	close(t.ready)
	return t, true, nil
}

// sweep drops workspaces idle past the TTL. Caller holds mu.
func (p *pool) sweep() {
	cut := p.now().Add(-p.ttl)
	for k, t := range p.warm {
		if t.settled() && t.used.Before(cut) {
			delete(p.warm, k)
			go t.close()
		}
	}
}

// evict enforces max by dropping least-recently-used entries. Caller holds mu.
//
// An entry still being built is never chosen: it has no server to close and a
// request is already waiting on it. That does mean a burst of cold starts can
// briefly exceed max, which is the right trade — the alternative is evicting the
// checkout somebody is blocked on.
func (p *pool) evict() {
	for len(p.warm) > p.max {
		var oldest key
		var found bool
		for k, t := range p.warm {
			if !t.settled() {
				continue
			}
			if !found || t.used.Before(p.warm[oldest].used) {
				oldest, found = k, true
			}
		}
		if !found {
			return
		}
		t := p.warm[oldest]
		delete(p.warm, oldest)
		go t.close()
	}
}

func (t *Tree) settled() bool {
	select {
	case <-t.ready:
		return true
	default:
		return false
	}
}

// drop discards every warm revision of one repo for one org.
//
// A push or a re-index makes a checkout stale. Mostly that resolves itself — keys
// are revision-pinned, so new content is a new key — but a caller that tracks a
// BRANCH keeps asking for the same key while the branch moves, and this is what
// releases it.
//
// Cross-process invalidation is phase 2 and is stated here rather than implied:
// code and lsp are separate binaries, so apps/code cannot call this in-process.
// The push signal has to arrive over the bus.
func (p *pool) drop(org, repo string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, t := range p.warm {
		if k.org == org && k.repo == repo && t.settled() {
			delete(p.warm, k)
			go t.close()
		}
	}
}

func (p *pool) closeAll() {
	p.mu.Lock()
	warm := p.warm
	p.warm = make(map[key]*Tree)
	p.mu.Unlock()
	for _, t := range warm {
		if t.settled() {
			t.close()
		}
	}
}

// ── building one workspace ───────────────────────────────────────────────────

// build is the cold path: check out, fetch dependencies, start the server.
func (s *state) build(ctx context.Context, k key, path string) (*Tree, error) {
	l, ok := langFor(path)
	if !ok {
		return nil, fmt.Errorf("no language server for %q", filepath.Ext(path))
	}
	dir, err := s.dirFor(k)
	if err != nil {
		return nil, err
	}
	if err := checkout(ctx, dir, k, s.forge); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	root := rootFor(dir, path, l)
	fetch(ctx, l, root)

	conn, err := Start(ctx, l, root)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &Tree{key: k, dir: dir, lang: l, conn: conn}, nil
}

// dirFor is where a workspace lives: inside the ORG's own partition, under the
// same {DataDir}/orgs/{slug} convention every per-org store in this binary uses
// (orgdb.go). The org segment is rendered by cloud.OrgNamespace from the
// validated principal — the one door that turns a principal into a name — so the
// isolation is a property of the path, not of a check somebody has to remember.
func (s *state) dirFor(k key) (string, error) {
	ns, err := cloud.OrgNamespace(k.org, "")
	if err != nil {
		return "", err
	}
	rev := k.rev
	if rev == "" {
		rev = "_default"
	}
	dir := filepath.Join(s.DataDir, "orgs", ns.ID(), "lsp", k.repo, rev)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("workspace dir: %w", err)
	}
	return dir, nil
}

// checkout materializes repo@rev into dir.
//
// init + fetch, not clone, because it is ONE path for both a branch name and a
// bare sha — `clone --branch` cannot take a sha, and having two checkout paths
// would mean the answer depends on which kind of revision you named.
//
// The URL is BUILT, never accepted: base is the deployment's forge and the owner
// segment is k.org, the validated principal's org. A caller supplies only the
// repo slug, already narrowed to [a-zA-Z0-9._-] by [slug]. There is therefore no
// input from which a request could name another tenant's repo, an internal
// address, or a host of its own choosing — the class of bug is absent rather than
// defended against.
func checkout(ctx context.Context, dir string, k key, base string) error {
	ctx, cancel := context.WithTimeout(ctx, cloneMax)
	defer cancel()

	url := strings.TrimSuffix(base, "/") + "/" + k.org + "/" + k.repo + ".git"
	rev := k.rev
	if rev == "" {
		rev = "HEAD"
	}
	steps := [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", url},
		{"fetch", "--depth", "1", "--no-tags", "origin", rev},
		{"checkout", "-q", "--detach", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if err := git(ctx, dir, args...); err != nil {
			return err
		}
	}
	return nil
}

// git runs one git subprocess in dir under the hardened environment apps/deploy
// and apps/git both use: no terminal prompt, no system or global config (so no
// inherited credential helper, insteadOf rewrite or proxy), protocols restricted
// to http/https, and redirects refused so a moved ref cannot bounce the fetch to
// another host. Arguments are a SLICE, never a shell string.
func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append([]string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_ALLOW_PROTOCOL=http:https",
		"HOME=" + os.TempDir(),
		"PATH=" + os.Getenv("PATH"),
	}, gitConfig("http.followRedirects=false")...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		// git's stderr can echo a URL; it never carries our credential, which
		// rides env-injected config and not argv. Bounded so a chatty failure
		// cannot balloon an error string.
		return fmt.Errorf("git %s: %w: %s", args[0], err, trim(string(out), 512))
	}
	return nil
}

// gitConfig renders git config as env (GIT_CONFIG_COUNT/KEY/VALUE), the form
// apps/git/mirror.go uses — config that never appears on argv or in a log.
func gitConfig(kv ...string) []string {
	env := []string{fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(kv))}
	for i, pair := range kv {
		k, v, _ := strings.Cut(pair, "=")
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, k),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, v))
	}
	return env
}

// fetch populates the dependency tree when the policy allows it.
//
// Failure is NOT fatal and is not reported to the caller: a language server still
// answers about the checkout's own source with no dependencies resolved, and a
// private module the worker cannot reach is a degraded answer, not a 500.
func fetch(ctx context.Context, l Lang, root string) {
	if !fetchable(l) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, fetchMax)
	defer cancel()

	cmd := exec.CommandContext(ctx, l.Fetch[0], l.Fetch[1:]...)
	cmd.Dir = root
	cmd.Env = env(l)
	_ = cmd.Run()
}

// ── input narrowing ──────────────────────────────────────────────────────────

// slug is a repository name: the shape apps/git gives a repo under an owner. No
// slash, so it cannot name another owner's repository; no leading dot, so it
// cannot climb out of the org's data directory.
var slug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)

// revision is a branch, tag or sha. The leading character is constrained to
// alphanumeric, which is what stops a revision from being read by git as a FLAG —
// `--upload-pack=…` in the rev position is command execution on the fetch.
var revision = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,199}$`)

// clean narrows a repo-relative path to a location proven to be inside dir.
//
// The check is done on the RESOLVED path, and that is the part that matters.
// Rejecting ".." is not sufficient: a checkout is TENANT-CONTROLLED content, and a
// repo may contain a symlink named `src` pointing at /etc, at the KMS mount, or at
// another org's directory one level up. Only resolving symlinks and then proving
// containment refuses that, so that is what happens — lexical checks first (they
// refuse the cheap attacks without a syscall), then EvalSymlinks, then a
// containment proof against the resolved root.
func clean(dir, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be repo-relative")
	}
	rel := filepath.Clean(filepath.FromSlash(path))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the repository")
	}

	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	abs, err := filepath.EvalSymlinks(filepath.Join(root, rel))
	if err != nil {
		return "", fmt.Errorf("no such file in the repository")
	}
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the repository")
	}
	if info, err := os.Stat(abs); err != nil || info.IsDir() {
		return "", fmt.Errorf("no such file in the repository")
	}
	return abs, nil
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// methods is every LSP request this door forwards. A CLOSED set: the door names
// what it serves, so a method the table does not carry is a 400 here rather than
// an arbitrary string handed to a language server.
var methods = []string{
	"hover", "definition", "references", "typeDefinition",
	"implementation", "documentSymbol", "completion", "diagnostics",
}

func known(m string) bool { return slices.Contains(methods, m) }
