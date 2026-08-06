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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hanzoai/cloud/apps/sandbox/wire"
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

	mux := http.NewServeMux()
	b.routes(mux)

	addr := ":" + envOr("BOX_PORT", "8000")
	srv := &http.Server{
		Addr:    addr,
		Handler: b.guard(mux),
		// A build or a test suite is legitimately slow, so there is no read or
		// write timeout on the whole request; the per-call timeoutSec bounds the
		// work instead. The header timeout is what stops a stalled connection
		// from holding a goroutine forever.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	// exec-uid is in the startup line because "who does submitted code run as"
	// is the single fact that decides whether this process is a sandbox, and it
	// should be readable from a pod log without an exec.
	log.Printf("boxd listening on %s workdir=%s keyed=%t exec-uid=%d", addr, b.workdir, b.key != "", b.execUID)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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

func (b *box) routes(mux *http.ServeMux) {
	// Native surface — what apps/sandbox and hanzo.app's ProjectFs speak.
	mux.HandleFunc(wire.PathHealth, b.health)
	mux.HandleFunc(wire.PathExec, b.exec)
	mux.HandleFunc(wire.PathFsList, b.fsList)
	mux.HandleFunc(wire.PathFsRead, b.fsRead)
	mux.HandleFunc(wire.PathFsWrite, b.fsWrite)
	mux.HandleFunc(wire.PathFsSearch, b.fsSearch)
	mux.HandleFunc(wire.PathFsDelete, b.fsRoot) // DELETE /v1/box/fs?path=
	mux.HandleFunc(wire.PathGitClone, b.gitClone)
	mux.HandleFunc(wire.PathGitPush, b.gitPush)
	mux.HandleFunc(wire.PathAgentRun, b.agentRun)

	// The frozen LibreChat family. Named where it is served, not in wire.go,
	// because these paths are not ours to rename (librechat.go).
	b.librechatRoutes(mux)
}

// guard is the one credential check, in front of everything except nothing —
// health included, because "is there a box here" is itself information about the
// cluster and this surface has no anonymous reader.
func (b *box) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.key == "" {
			writeErr(w, http.StatusServiceUnavailable, "box not configured: CODE_EXEC_API_KEY unset")
			return
		}
		got := strings.TrimSpace(r.Header.Get(wire.KeyHeader))
		if subtle.ConstantTimeCompare([]byte(got), []byte(b.key)) != 1 {
			writeErr(w, http.StatusUnauthorized, "invalid api key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (b *box) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	writeJSON(w, http.StatusOK, wire.Health{
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"status": status, "error": msg})
}

// bind reads a JSON body with a hard size ceiling. A box takes source code, so
// the ceiling is generous; unbounded it is a memory bomb from the one caller
// whose whole job is running untrusted input.
func bind(r *http.Request, v any) error {
	defer func() { _ = r.Body.Close() }()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody))
	return dec.Decode(v)
}

const maxBody = 32 << 20 // 32 MiB
