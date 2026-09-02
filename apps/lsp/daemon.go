package lsp

// daemon.go is the client for hanzoai/lsp — the jailed language-server daemon.
//
// # Why the language servers are not here
//
// Answering "where is this defined" for a symbol that lands in a DEPENDENCY means
// fetching that dependency and type-checking both, which means running a
// third-party toolchain over untrusted bytes. That belongs in a pod with gVisor,
// no egress but a module proxy, and no credential — not in a fleet app that holds
// a principal and a ledger. So this package resolves the tenant, prices the work
// and forwards the position; the daemon runs the compiler.
//
// # Two calls, and the daemon can never make the first one
//
// A root is an immutable (org, repo, commit) tree the daemon HOLDS. It has no git
// credential and no way to get one — a daemon that could fetch a repository would
// need a credential that could reach every repository — so it cannot go and get a
// tree it lacks. It says so instead: /ask answers 409 {"need":"tree"} and the
// caller, which already has the repository, POSTs /root and asks again. Two calls
// on a cold root, one on a warm one, and no path by which the sandbox reaches
// anything.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/internal/environ"
	"github.com/zap-proto/zip"
)

// upstream is the daemon's in-cluster address. WHERE it runs is infra wiring, so
// it is env with a constant default — never a request field.
const (
	upstreamEnv     = "LSP_UPSTREAM"
	upstreamDefault = "http://lsp.hanzo.svc.cluster.local:8000"
)

// keyEnv names the shared service key this proxy presents. The value is a KMS
// secret the Deployment injects; nothing here ever writes it to a log or a reply.
const keyEnv = "LSP_KEY"

// The two budgets, which are two different kinds of work.
//
// A PREPARE is a tree write, a dependency fetch and a language server's first
// index: minutes, legitimately. A QUERY is a JSON-RPC round trip to a process
// that already holds the index: milliseconds. One deadline for both would either
// cut an honest cold start in half or let a wedged server hold a hover request
// for five minutes.
const (
	prepareWait = 5 * time.Minute
	askWait     = 10 * time.Second
)

// bodyMost bounds a reply. The daemon caps what it answers; this caps what a
// compromised or confused one could make this process allocate.
const bodyMost = 32 << 20

// errNeedTree is the daemon's 409: it holds no root for that revision, and only
// the caller can supply one.
var errNeedTree = errors.New("lsp: the daemon holds no root for this revision")

// daemon is the upstream: one address, one key, one client. Package-level and
// shared, because a connection pool that is rebuilt per request is not a pool.
type daemon struct {
	url  string
	key  string
	http *http.Client
}

// newDaemon reads the deployment's wiring once, at Mount.
//
// The client's own Timeout is the PREPARE ceiling — the longest legitimate call —
// and each call narrows it further with a context deadline. One ceiling, one
// per-call bound, and no request that can outlive both.
func newDaemon() *daemon {
	return &daemon{
		url:  environ.Or(upstreamEnv, upstreamDefault),
		key:  environ.Or(keyEnv, ""),
		http: &http.Client{Timeout: prepareWait},
	}
}

// tree is the body of /root: a whole working tree, which only the caller can
// produce.
type tree struct {
	Org   string `json:"org"`
	Repo  string `json:"repo"`
	Rev   string `json:"rev"`
	Files []file `json:"files"`
}

// file is one file of that tree, tree-relative path and whole content.
type file struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ready is what /root answers. Cold reports that THIS call paid for the build —
// the fetch and the first index — which is the event that carries a fee.
type ready struct {
	Ready bool     `json:"ready"`
	Cold  bool     `json:"cold"`
	Langs []string `json:"langs"`
}

// question is the body of /ask: one op at one position in one root.
type question struct {
	Org       string `json:"org"`
	Repo      string `json:"repo"`
	Rev       string `json:"rev"`
	Op        string `json:"op"`
	Relation  string `json:"relation,omitempty"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

// ask puts one question to a root the daemon already holds, decoding the reply
// into out. A daemon that holds no such root answers errNeedTree.
func (d *daemon) ask(ctx context.Context, in *question, out *Answer) error {
	ctx, cancel := context.WithTimeout(ctx, askWait)
	defer cancel()
	return d.post(ctx, "/ask", in, out)
}

// root hands the daemon the tree it said it needed.
func (d *daemon) root(ctx context.Context, in *tree) (*ready, error) {
	ctx, cancel := context.WithTimeout(ctx, prepareWait)
	defer cancel()
	out := &ready{}
	if err := d.post(ctx, "/root", in, out); err != nil {
		return nil, err
	}
	return out, nil
}

// post is the ONE way this package speaks to the daemon: encode, present the
// key, decode, and translate a status into an error a caller can act on.
//
// The daemon's own error text names its paths and can echo tenant source, so it
// is never returned to a client — the status decides what this says, and the
// detail goes to the log at the call site.
func (d *daemon) post(ctx context.Context, path string, in, out any) error {
	if d.key == "" {
		// Fail CLOSED and say so as an outage, not as a refusal of the caller: a
		// proxy with no credential is a misconfigured deployment, and the daemon
		// would 503 this same request anyway.
		return zip.Errorf(http.StatusServiceUnavailable, "language server unavailable")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", d.key)

	res, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("call %s: %w", path, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, res.Body); _ = res.Body.Close() }()

	reply, err := io.ReadAll(io.LimitReader(res.Body, bodyMost))
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	switch {
	case res.StatusCode == http.StatusOK:
		if err := json.Unmarshal(reply, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
		return nil
	case res.StatusCode == http.StatusConflict && needsTree(reply):
		return errNeedTree
	case res.StatusCode == http.StatusBadRequest:
		// The daemon narrows the same inputs this package does — op, relation,
		// path, position, key shape. Reaching here means the two disagree, which
		// is the caller's request being wrong in a way worth telling them.
		return zip.ErrBadRequest("the language server refused this request")
	default:
		return fmt.Errorf("%s: upstream status %d", path, res.StatusCode)
	}
}

// needsTree reads the one discriminator on a 409. The daemon has two: "tree" (no
// root for this revision) and "held" (a warm request for a repository with no
// live root). Only the first is one this proxy can answer by sending a tree.
func needsTree(reply []byte) bool {
	var body struct {
		Need string `json:"need"`
	}
	return json.Unmarshal(reply, &body) == nil && body.Need == "tree"
}
