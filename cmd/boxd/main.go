// boxd is the agent that runs INSIDE a sandbox box.
//
// It is the thing every control plane in this org was already pointing at and
// nobody had built: hanzo.chat's execute_code tool, /v1/functions invoke,
// apps/coding's Runner and hanzo.app's ProjectFs all terminate here.
//
// It is deliberately small. It has no store, no IAM, no OpenAPI document, no
// billing and no knowledge of orgs — it is one process in one container that
// holds one project directory. Everything that needs to know WHOSE box this is
// lives in apps/sandbox, on the other side of the cluster network. boxd's whole
// job is: run this, read that, write this, clone, push, and stream an agent.
//
// WHY IT LIVES IN hanzoai/cloud AND SHIPS IN hanzoai/bot'S IMAGE: its
// request/response types are the same Go declarations apps/sandbox marshals
// (apps/sandbox/wire). Two repos holding two copies of a wire contract drift on
// the first rename and fail only in production. One module, one declaration.
// The image consumes it as a published artifact — the same direction cloud's
// own Dockerfile already runs when it pulls console-embed.
//
// WHAT IT IS NOT: an isolation boundary. boxd runs arbitrary submitted code on
// purpose — that is its function. The boundary is the pod (caps dropped,
// read-only root, no service-account token) and the tainted node pool
// underneath it. boxd must NEVER be reachable from outside the cluster.
//
// NETWORK containment is real but is NOT boxd's: universe declares one policy
// over `hanzo.ai/box-class` in namespace hanzo-boxes, so it governs every box
// whether the pool scheduled it or a Deployment holds it. Egress is a whitelist
// — DNS, then 80/443 to public address space only — so box→box on this port,
// box→datastore and box→apiserver are denied by omission. That is what makes
// the shared key useless from inside a box; boxd's uid separation is what keeps
// it from being read in the first place. Two controls, deliberately, because
// this one previously selected `app: code-exec` in namespace `hanzo` and
// therefore matched no pod at all — containment that is declared and not in
// force is worse than none, since nothing looks wrong.
//
// AUTH is one shared service key on X-API-Key, compared in constant time —
// the same KMS-sourced CODE_EXEC_API_KEY the rest of this path already carries,
// because a box holding a second credential would be a second auth system. An
// unset key fails CLOSED (503): a box that answers anyone is worse than a box
// that answers no one.
//
// THE KEY IS SHARED ACROSS THE POOL, which is why BOX_EXEC_UID is not optional
// when it is set: submitted code running as boxd's own uid can read that key
// out of /proc/<pid>/environ and use it against every other tenant's box. boxd
// refuses to start in that configuration. See confine() in pgroup_unix.go for
// why the two obvious cheaper defenses do not work.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanzoai/cloud/apps/sandbox/wire"
	"github.com/zap-proto/zip"
)

// box is the process's whole state: where the project is, what the box is
// called, and the sessions the LibreChat family accumulates.
type box struct {
	workdir string // absolute project root; every path is resolved under it
	image   string
	project string
	ref     string
	boot    int64
	key     string

	// id is who this box is, learned from cloud at claim time over
	// [wire.PathBind]. The guard compares it against the caller's X-Box-Id so a
	// call that arrived at a RECYCLED ADDRESS is refused rather than served — see
	// guard(). It is not an env var and cannot be: this pod was started by the
	// warm pool's Deployment before the tenant it will serve existed.
	//
	// Guarded by mu because bind runs on a request goroutine and the guard reads
	// it on every other one.
	mu sync.RWMutex
	id string

	// The uid/gid submitted code runs as. Zero means "the same uid as boxd",
	// which is the configuration in which the child can read boxd's
	// credentials out of /proc — see confine() in pgroup_unix.go.
	execUID int
	execGID int

	sessions *sessions // the LibreChat family's session store (librechat.go)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "boxd:", err)
		os.Exit(1)
	}
}

func run() error {
	b := &box{
		workdir:  envOr("BOX_WORKDIR", "/work"),
		image:    os.Getenv("BOX_IMAGE"),
		project:  os.Getenv("BOX_PROJECT"),
		ref:      os.Getenv("BOX_REF"),
		boot:     time.Now().Unix(),
		key:      strings.TrimSpace(os.Getenv("CODE_EXEC_API_KEY")),
		execUID:  envInt("BOX_EXEC_UID", 0),
		execGID:  envInt("BOX_EXEC_GID", 0),
		sessions: newSessions(),
	}

	if err := b.checkExecIsolation(); err != nil {
		return err
	}

	abs, err := filepath.Abs(b.workdir)
	if err != nil {
		return fmt.Errorf("workdir %q: %w", b.workdir, err)
	}
	b.workdir = abs
	if err := os.MkdirAll(b.workdir, dirMode); err != nil {
		return fmt.Errorf("workdir %q: %w", b.workdir, err)
	}
	if err := shareWith(b.workdir, b.execUID, b.execGID); err != nil {
		return fmt.Errorf("workdir %q: share with uid %d: %w", b.workdir, b.execUID, err)
	}

	// zip is the ONE web framework in the org, standalone binaries included.
	// A box serves the same request tier every other Hanzo service does — one
	// middleware seam, one error shape, one place a route is declared — so it
	// gets there the same way rather than assembling its own from net/http.
	//
	// BodyLimit is stated rather than defaulted. zip's default is 4 MiB, which is
	// right for an API taking JSON documents and wrong for a box: this one takes
	// SOURCE — a file write, an upload, a patch — and the ceiling it has always
	// had is 32. Leaving it to the default would have cut the limit by 8x while
	// every test that writes a small file went on passing.
	app := zip.New(zip.Config{AppName: "boxd", BodyLimit: maxBody})
	app.Use(zip.H(b.guard))
	b.routes(app)

	addr := ":" + envOr("BOX_PORT", "8000")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = app.Shutdown()
	}()

	// exec-uid is in the startup line because "who does submitted code run as"
	// is the single fact that decides whether this process is a sandbox, and it
	// should be readable from a pod log without an exec.
	log.Printf("boxd listening on %s workdir=%s keyed=%t exec-uid=%d", addr, b.workdir, b.key != "", b.execUID)
	return app.Listen(addr)
}

// checkExecIsolation refuses the one configuration that hands the pool's
// credential to the code this box was built to distrust.
//
// The key arrives in the environment, and Linux serves /proc/<pid>/environ to
// anything running as the same uid — so with no uid separation, the first thing
// submitted code can do is read CODE_EXEC_API_KEY out of boxd and use it as a
// valid X-API-Key against every OTHER box in the pool, because it is one shared
// service key. That is not a sandbox, so boxd does not pretend to be one: it
// fails at startup, in the open, the same way it already refuses to serve with
// no key at all.
//
// An UNKEYED box is exempt: it has no credential to leak, and it already fails
// closed at the guard with 503. That is what keeps `go run ./cmd/boxd` working
// on a laptop without root.
func (b *box) checkExecIsolation() error {
	if b.key == "" || b.execUID > 0 {
		return nil
	}
	return errors.New(
		"CODE_EXEC_API_KEY is set but BOX_EXEC_UID is not: submitted code would run as boxd's own uid " +
			"and could read the key from /proc/<pid>/environ, which opens every box in the pool. " +
			"Set BOX_EXEC_UID/BOX_EXEC_GID to an unprivileged uid that is NOT boxd's")
}

// routes states the whole surface once. The METHOD is declared here rather than
// checked inside each handler: a mux matched a path and left every handler to
// re-derive its own verb, which is how PathFsDelete came to share a handler with
// the fs root and why three of them opened with the same six-line method switch.
func (b *box) routes(app *zip.App) {
	// Native surface — what apps/sandbox and hanzo.app's ProjectFs speak.
	app.Post(wire.PathBind, b.bind)
	app.Get(wire.PathHealth, b.health)
	app.Post(wire.PathExec, b.exec)
	app.Get(wire.PathFsList, b.fsList)
	app.Get(wire.PathFsRead, b.fsRead)
	app.Post(wire.PathFsWrite, b.fsWrite)
	app.Get(wire.PathFsSearch, b.fsSearch)
	app.Delete(wire.PathFsDelete, b.fsDelete)
	app.Post(wire.PathGitClone, b.gitClone)
	app.Post(wire.PathGitPush, b.gitPush)
	app.Post(wire.PathAgentRun, b.agentRun)

	// The frozen LibreChat family. Named where it is served, not in wire.go,
	// because these paths are not ours to rename (librechat.go).
	b.librechatRoutes(app)
}

// guard is the one credential check, in front of everything except nothing —
// health included, because "is there a box here" is itself information about the
// cluster and this surface has no anonymous reader.
func (b *box) guard(c *zip.Ctx) error {
	if b.key == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "box not configured: CODE_EXEC_API_KEY unset")
	}
	got := strings.TrimSpace(c.Header(wire.KeyHeader))
	if subtle.ConstantTimeCompare([]byte(got), []byte(b.key)) != 1 {
		return zip.Errorf(http.StatusUnauthorized, "invalid api key")
	}
	// AM I THE BOX THE CALLER MEANT? The key cannot answer that — it is one
	// shared key for the whole pool, so it proves the caller is cloud and
	// says nothing about which box it wanted. Neither can the address: a pod
	// that dies on its own never runs release(), so cloud's row keeps a Host
	// the CNI has since reassigned to a replacement pod belonging to someone
	// else, and the misdirected call arrives looking perfectly valid.
	//
	// Only the destination can catch that, which is why the check is here
	// and not in the proxy — cloud believing its own row is the thing that
	// is wrong in that scenario. 409 rather than 401: the credential was
	// fine, the addressee is not, and cloud can tell those apart and go
	// reconcile the row.
	//
	// An UNBOUND box refuses a named call too. That is the recycled-address
	// case exactly: the replacement pod is warm and has been bound to nobody,
	// so serving it would hand a stranger's request to a fresh checkout. The
	// bind endpoint itself is exempt, because that is the call that answers
	// the question.
	if want := strings.TrimSpace(c.Header(wire.BoxHeader)); want != "" && c.Path() != wire.PathBind {
		b.mu.RLock()
		have := b.id
		b.mu.RUnlock()
		if want != have {
			return zip.Errorf(http.StatusConflict, "wrong box: this is %s, caller wanted %s", quoted(have), want)
		}
	}
	return c.Next()
}

// bind tells this box which box it is. Cloud calls it once, immediately after
// claiming the pod out of the warm pool and before handing the address to
// anyone, so every later call can be checked against an id the box holds itself.
//
// Binding TWICE to the same id is fine and answers 200 — cloud may retry, and a
// retry that fails would strand a pod that is already correct. Binding to a
// DIFFERENT id is refused: release() deletes a pod rather than recycling it, so
// there is no legitimate second tenant, and the request that asks for one is
// either a stale claim or the confusion this whole check exists to catch.
func (b *box) bind(c *zip.Ctx) error {
	var in wire.Bind
	if err := c.Bind(&in); err != nil {
		return zip.Errorf(http.StatusBadRequest, "bind: %v", err)
	}
	if in.ID = strings.TrimSpace(in.ID); in.ID == "" {
		return zip.Errorf(http.StatusBadRequest, "bind: id required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.id != "" && b.id != in.ID {
		return zip.Errorf(http.StatusConflict, "already bound to %s", b.id)
	}
	b.id = in.ID
	return c.JSON(http.StatusOK, wire.Bind{ID: b.id})
}

// quoted renders an id for an error message, so an UNBOUND box says so instead
// of reading as though it were bound to the empty string.
func quoted(id string) string {
	if id == "" {
		return "unbound"
	}
	return id
}

func (b *box) health(c *zip.Ctx) error {
	return c.JSON(http.StatusOK, wire.Health{
		OK: true, Boot: b.boot, Image: b.image,
		Project: b.project, Ref: b.ref, Workdir: b.workdir,
	})
}

// ── small shared helpers ────────────────────────────────────────────────────

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(k))); err == nil && v > 0 {
		return v
	}
	return def
}

// maxBody is the ceiling on a request body. A box takes source code, so it is
// generous; unbounded it is a memory bomb from the one caller whose whole job is
// running untrusted input. It is handed to zip at construction, which enforces
// it for every route at once — the three helpers that used to live here
// (writeJSON, writeErr, and a hand-rolled bind that re-applied this ceiling per
// call) are all zip's now: c.JSON, zip.Errorf, c.Bind.
const maxBody = 32 << 20 // 32 MiB
