// Package sandbox is the ONE compute primitive: a sandbox is a gVisor pod that
// runs somebody else's code, and every lifetime is the same object.
//
//	a function invoke      = a sandbox with a seconds-long lease
//	a code-exec call       = a sandbox with a session lease
//	an agentic coding run  = a sandbox with a project volume and a long lease
//
// Not three subsystems, not three schedulers. One record, one pod spec, one way
// in. What differs between them is `ttlSec` and whether a volume is attached.
//
//	POST   /v1/sandboxes             {kind:"sandbox", class, project?, ttlSec?} -> Sandbox
//	GET    /v1/sandboxes             ?kind=&project=&status=
//	GET    /v1/sandboxes/:id
//	DELETE /v1/sandboxes/:id         ?purge=1 drops the volume too
//	POST   /v1/sandboxes/:id/exec    {argv|command, stdin?, timeoutSec?} -> {exitCode,stdout,stderr}
//	GET    /v1/sandboxes/:id/fs      ?path=  read a file, or list a directory
//	POST   /v1/sandboxes/:id/fs      ?path=  write a file
//
// THERE IS EXACTLY ONE WAY INTO A SANDBOX, and it is the Kubernetes exec
// subresource. fs read/list/write are not a second channel — they are `cat`,
// `ls` and `tee` over that one channel, which is also how `kubectl cp` has
// always worked. The predecessor shipped an in-pod HTTP daemon with eleven
// endpoints, a shared pool-wide API key and a pod-IP address book; all three are
// gone, because Kubernetes already had the channel and we were re-implementing
// it badly.
//
// A SANDBOX IS ADDRESSED BY POD NAME THROUGH THE APISERVER, NEVER BY IP. That is
// not a style preference, it is the fix for a real cross-tenant read: a pod that
// dies on its own — evicted, OOM-killed, drained — never runs a release path, so
// a row keeps a Host the CNI has since handed to another tenant's replacement
// pod, and a call to that address is served by a stranger with every credential
// checking out. A pod NAME is minted per sandbox and never reused, so the same
// mistake cannot be spelled. The header-stamping protocol that was invented to
// defend the IP scheme is deleted along with the scheme.
//
// NOTHING RUNS IN THIS PACKAGE. There is no os/exec here and there must never
// be. It creates Kubernetes objects and streams bytes to the apiserver; the work
// happens inside the pod, under runsc, on the far side of a runtime boundary.
//
// THERE IS NO POOL. A sandbox is created for a lease and deleted at its end.
// Claiming from a warm pool is what forced the recycle, the pod-IP address book
// and the label-patch race arbitration; it also grows, inevitably, into a
// scheduler beside Kubernetes' own. If a warm pool is ever wanted, it is a
// Deployment an operator sizes — not a manager in this process.
//
// AUTH is the ordinary one: IAM terminates identity at the edge and this package
// reads principal.Org. It never mints a credential, and no caller credential
// reaches a sandbox — the pod carries no service-account token at all.
package sandbox

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// classes is the CLOSED set of sandbox shapes. Each is one image tag and one
// resource envelope; they are not independent, which is why this is one field
// and not three.
//
//	exec    — a chat or function code run. No volume, seconds to minutes.
//	dev     — a coding sandbox. Project volume, a toolchain, hours.
//	desktop — dev plus a virtual display for computer use.
var classes = map[string]bool{"exec": true, "dev": true, "desktop": true}

// KindSandbox is the sandbox this package provisions: a gVisor pod in our own
// cluster. It is a VALUE on the shared /v1/sandboxes resource, beside the kinds
// the compute control plane provisions (droplets, GPUs, bot sandbox), because
// "a sandbox the org has" is one noun and splitting it by who provisions it
// would publish two.
const KindSandbox = "sandbox"

// IDPrefix is how a sandbox of ours is told apart from one the compute control
// plane owns, on a resource both answer for. It is a prefix on the id rather
// than a lookup, so dispatch costs nothing and cannot go stale.
const IDPrefix = "m_"

// Ours reports whether an id names a sandbox this package provisions.
func Ours(id string) bool { return strings.HasPrefix(strings.TrimSpace(id), IDPrefix) }

type state struct {
	stores *cloud.OrgStore[*Store]
	rt     *runtime
}

// storeFor is the ONE way this package reaches a store, through
// cloud.OrgNamespace — the single door a VALIDATED org walks through. org must
// already come from principal.Org, never from a body field.
func storeFor(s *cloud.Service[state], org string) (*Store, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// Mount registers the sandbox-sandbox half of /v1/sandboxes.
//
// It is composed INTO the app that already owns the /v1/sandboxes prefix rather
// than claiming a manifest row of its own: zip refuses two owners for one
// prefix, the compute surface has held that prefix in production for months,
// and a second `sandbox` noun is exactly the duplication this package exists to
// remove. See apps/visor's mount, which is the only caller.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("sandbox.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("sandbox.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("sandbox.Mount: empty DataDir")
	}
	b := cloud.NewBase(deps, "sandbox")
	s := &cloud.Service[state]{Base: b, State: state{
		stores: cloud.NewOrgStore(b, "sandbox", openStore),
		rt:     newRuntime(),
	}}
	Routes(app, s)
	s.Log.Info("sandbox mounted",
		"namespace", s.State.rt.ns, "image", s.State.rt.image,
		"runtimeClass", s.State.rt.runtimeClass, "cluster", s.State.rt.ready() == nil)
	return nil
}

// Routes registers everything this package serves.
//
// The collection and member routes used to be left out, on the reasoning that
// they were "shared with the compute surface" — true while this served
// /v1/sandbox, which visor owns and where a second registration of one
// resource is a conflict. It is /v1/sandboxes now, owned outright, and leaving
// them out meant Create, List, Get and Delete existed as exported functions
// that no request could ever reach: a caller could exec in a sandbox it had no
// way to create. The handlers were there, the routes were not, and nothing said
// so — the same shape as the policy that selected no pod and the installer that
// installed nothing.
func Routes(app cloud.Router, s *cloud.Service[state]) {
	app.Get("/v1/sandboxes", cloud.Handle(s, func(s *Service, c *zip.Ctx) error {
		out, err := List(s, c)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]any{"sandbox": out})
	}))
	app.Post("/v1/sandboxes", cloud.Handle(s, Create))

	g := app.Group("/v1/sandboxes")
	g.Get("/:id", cloud.Handle(s, Get))
	g.Delete("/:id", cloud.Handle(s, Delete))
	g.Post("/:id/exec", cloud.Handle(s, ExecIn))
	g.Get("/:id/fs", cloud.Handle(s, FsRead))
	g.Post("/:id/fs", cloud.Handle(s, FsWrite))
}

func orgOf(c *zip.Ctx) (string, bool) { return principal.Org(c) }
func idParam(c *zip.Ctx) string       { return strings.TrimSpace(c.Param("id")) }

// Service is the mounted subsystem, handed back to the app that owns the
// /v1/sandboxes prefix so its collection handlers can dispatch into this one.
type Service = cloud.Service[state]

// New builds the subsystem without registering anything, for a host that wants
// to dispatch the shared collection routes itself.
func New(deps cloud.Deps) (*Service, error) {
	if deps.Logger == nil {
		return nil, fmt.Errorf("sandbox.New: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return nil, fmt.Errorf("sandbox.New: empty DataDir")
	}
	b := cloud.NewBase(deps, "sandbox")
	return &cloud.Service[state]{Base: b, State: state{
		stores: cloud.NewOrgStore(b, "sandbox", openStore),
		rt:     newRuntime(),
	}}, nil
}

// Create leases a sandbox. It is the only path that creates cluster objects.
func Create(s *Service, c *zip.Ctx) error {
	o, ok := orgOf(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body struct {
		Kind    string `json:"kind"`
		Class   string `json:"class"`
		Project string `json:"project"`
		Image   string `json:"image"`
		TTLSec  int    `json:"ttlSec"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	class := strings.ToLower(strings.TrimSpace(body.Class))
	if class == "" {
		class = "exec"
	}
	if !classes[class] {
		return zip.ErrBadRequest("class must be one of exec, dev, desktop")
	}
	// A project is what makes a sandbox RESUMABLE — it names the volume. An exec
	// sandbox has no project and no volume, which is why it can be created and
	// destroyed freely and why two of them never contend.
	project := slug(body.Project)
	if class != "exec" && project == "" {
		return zip.ErrBadRequest("project required for class " + class)
	}
	store, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}

	// One live sandbox per (org, project), and the refusal is deliberate: the
	// project volume is single-attach, so a second concurrent sandbox would
	// either fail to attach or silently get a cold empty disk. "Silently cold" is
	// the worse of the two — the user sees a sandbox that works and reinstalls
	// everything on every call — so it is refused in the open, naming the sandbox
	// that already holds the volume.
	if project != "" {
		if live, err := store.Live(c.Context(), o, project); err == nil && live.ID != "" {
			return zip.Errorf(http.StatusConflict,
				"project %q already has a live sandbox (%s); delete it first", project, live.ID)
		}
	}

	id, err := genID()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "id: %v", err)
	}
	now := time.Now().Unix()
	m := Sandbox{
		ID: id, Org: o, Kind: KindSandbox, Class: class, Project: project,
		Image: firstNonEmpty(body.Image, s.State.rt.imageFor(class)),
		Pod:   podName(id), Status: "pending",
		CreatedAt: now, LastUsedAt: now,
	}
	if project != "" {
		m.Volume = volumeName(o, project)
	}
	// The lease. Unbounded is not an option for a sandbox running submitted code
	// on our nodes, so an unset ttl takes the class default rather than forever.
	ttl := body.TTLSec
	if ttl <= 0 {
		ttl = defaultTTL[class]
	}
	if ttl > maxTTL {
		ttl = maxTTL
	}
	m.ExpiresAt = now + int64(ttl)

	if err := store.Put(c.Context(), m); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "put: %v", err)
	}
	// A failure to start is RECORDED on the row and answered 503 — the row stays
	// so an operator can see what was asked for and why it did not happen, rather
	// than the request vanishing with the evidence.
	if err := s.State.rt.start(c.Context(), m); err != nil {
		m.Status, m.Error = "error", err.Error()
		_ = store.Put(c.Context(), m)
		return zip.Errorf(http.StatusServiceUnavailable, "start sandbox: %v", err)
	}
	m.Status = "running"
	if err := store.Put(c.Context(), m); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "put: %v", err)
	}
	return c.JSON(http.StatusCreated, m)
}

// List answers the caller org's sandbox. Returns the slice so a host that also
// has upstream sources can fold them into one answer.
func List(s *Service, c *zip.Ctx) ([]Sandbox, error) {
	o, ok := orgOf(c)
	if !ok {
		return nil, zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, o)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	ms, err := store.List(c.Context(), o, slug(c.Query("project")), strings.TrimSpace(c.Query("status")))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return ms, nil
}

// Get answers one sandbox THIS org owns.
func Get(s *Service, c *zip.Ctx) error {
	m, _, err := load(s, c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, m)
}

// Delete ends the lease: the pod goes, the volume stays unless purge=1.
func Delete(s *Service, c *zip.Ctx) error {
	m, store, err := load(s, c)
	if err != nil {
		return err
	}
	if serr := s.State.rt.stop(c.Context(), m); serr != nil {
		s.Log.Warn("stop sandbox", "id", m.ID, "err", serr)
	}
	// purge=1 drops the VOLUME as well, and it is opt-in because the volume holds
	// the only copy of the checkout and the caches. Ending a lease is cheap and
	// reversible; deleting someone's uncommitted work is neither.
	if c.Query("purge") == "1" && m.Volume != "" {
		if perr := s.State.rt.purge(c.Context(), m); perr != nil {
			s.Log.Warn("purge volume", "volume", m.Volume, "err", perr)
		}
	}
	if err := store.Delete(c.Context(), m.Org, m.ID); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	c.Status(http.StatusNoContent)
	return nil
}

// ExecIn runs argv inside the sandbox and answers its exit code and output.
//
// A non-zero exit is a SUCCESSFUL call carrying a failed program: the HTTP
// status stays 200, because "the tests failed" and "the sandbox is broken" are
// different facts and a caller has to be able to tell them apart.
func ExecIn(s *Service, c *zip.Ctx) error {
	m, store, err := load(s, c)
	if err != nil {
		return err
	}
	var body struct {
		Argv       []string `json:"argv"`
		Command    string   `json:"command"`
		Stdin      string   `json:"stdin"`
		Dir        string   `json:"dir"`
		TimeoutSec int      `json:"timeoutSec"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	argv, err := argvOf(body.Argv, body.Command)
	if err != nil {
		return err
	}
	if body.Dir != "" {
		argv = append([]string{"sh", "-c", "cd " + shellQuote(body.Dir) + " && exec \"$@\"", "sh"}, argv...)
	}
	r, err := s.State.rt.exec(c.Context(), m, argv, strings.NewReader(body.Stdin), body.TimeoutSec)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "exec: %v", err)
	}
	touch(c, store, m)
	return c.JSON(http.StatusOK, r)
}

// FsRead reads one file, or lists a directory when the path names one. Both go
// through the same exec channel — there is no second protocol and no daemon in
// the pod to speak one.
func FsRead(s *Service, c *zip.Ctx) error {
	m, store, err := load(s, c)
	if err != nil {
		return err
	}
	path, err := confine(c.Query("path"))
	if err != nil {
		return err
	}
	// One call, not two: `cat` a file, and fall back to a listing when the path is
	// a directory. A stat round-trip first would double the latency of the common
	// case to save a shell test that costs nothing.
	argv := []string{"sh", "-c",
		"if [ -d " + shellQuote(path) + " ]; then ls -1A -- " + shellQuote(path) +
			"; else cat -- " + shellQuote(path) + "; fi"}
	r, err := s.State.rt.exec(c.Context(), m, argv, nil, 0)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "fs read: %v", err)
	}
	touch(c, store, m)
	if r.ExitCode != 0 {
		return zip.ErrNotFound(strings.TrimSpace(firstNonEmpty(r.Stderr, "no such path")))
	}
	c.SetHeader("Content-Type", "text/plain; charset=utf-8")
	return c.String(http.StatusOK, r.Stdout)
}

// FsWrite writes the request body to one file, creating parents.
func FsWrite(s *Service, c *zip.Ctx) error {
	m, store, err := load(s, c)
	if err != nil {
		return err
	}
	path, err := confine(c.Query("path"))
	if err != nil {
		return err
	}
	argv := []string{"sh", "-c",
		"mkdir -p -- \"$(dirname -- " + shellQuote(path) + ")\" && cat > " + shellQuote(path)}
	r, err := s.State.rt.exec(c.Context(), m, argv, strings.NewReader(string(c.Body())), 0)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "fs write: %v", err)
	}
	touch(c, store, m)
	if r.ExitCode != 0 {
		return zip.Errorf(http.StatusBadRequest, "write %s: %s", path, strings.TrimSpace(r.Stderr))
	}
	return c.JSON(http.StatusOK, map[string]any{"path": path, "bytes": len(c.Body())})
}

// load resolves :id to a sandbox THIS org owns. The org comes from the validated
// principal and the query is scoped by it, so a caller cannot address another
// org's sandbox by guessing an id — a miss is 404 and not 403, because "that
// sandbox belongs to someone else" is itself a cross-tenant fact.
func load(s *Service, c *zip.Ctx) (Sandbox, *Store, error) {
	o, ok := orgOf(c)
	if !ok {
		return Sandbox{}, nil, zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, o)
	if err != nil {
		return Sandbox{}, nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	m, err := store.Get(c.Context(), o, idParam(c))
	if err == errNotFound {
		return Sandbox{}, nil, zip.ErrNotFound("sandbox not found")
	}
	if err != nil {
		return Sandbox{}, nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if m.Status != "running" {
		// Only the routes that reach INTO the sandbox care; get and delete are
		// happy with a stopped row, and they do not come through here for that
		// check — this returns the row and the caller decides.
		return m, store, nil
	}
	return m, store, nil
}

// touch records use, so an idle reaper can tell an actively-worked sandbox from
// one whose owner walked away.
func touch(c *zip.Ctx, store *Store, m Sandbox) {
	m.LastUsedAt = time.Now().Unix()
	_ = store.Put(c.Context(), m)
}

// argvOf takes the one form or the other. `command` is a convenience for a
// caller holding a shell line; `argv` is the honest form and the one that cannot
// be word-split by accident. Both end as an argv — nothing here builds a shell
// string out of caller input except where the caller asked for a shell.
func argvOf(argv []string, command string) ([]string, error) {
	if len(argv) > 0 {
		return argv, nil
	}
	if strings.TrimSpace(command) != "" {
		return []string{"sh", "-c", command}, nil
	}
	return nil, zip.ErrBadRequest("argv or command required")
}

// confine resolves a caller path under the project root. The sandbox mounts the
// project at workdir and nothing above it is addressable — a path that climbs
// out is refused here rather than being sanitized into something else, because
// silently rewriting a path is how a caller ends up reading a file it did not
// ask for and never learns.
func confine(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return workdir, nil
	}
	if strings.Contains(p, "..") {
		return "", zip.ErrBadRequest("path must not contain ..")
	}
	if strings.HasPrefix(p, "/") {
		if p != workdir && !strings.HasPrefix(p, workdir+"/") {
			return "", zip.ErrBadRequest("path must be under " + workdir)
		}
		return p, nil
	}
	return workdir + "/" + p, nil
}

// shellQuote makes one argument literal for `sh -c`. Single quotes with the
// close-escape-reopen trick: inside single quotes nothing is special, so the
// only case to handle is a single quote itself.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && v > 0 {
		return v
	}
	return def
}

// Prose for the surface, declared beside the routes. These handlers bind through
// zip.Ctx rather than being typed ops, so zipdoc has no comment to lift and the
// document would otherwise publish operationIds and nothing else.
func init() {
	openapi.Describe("/v1/sandboxes/:id/exec", http.MethodPost,
		"Run a command in a sandbox",
		"Runs a command inside the sandbox and returns its exit code, stdout and stderr. "+
			"A non-zero exit is a SUCCESSFUL call carrying a failed program — the HTTP status "+
			"stays 200, because \"the tests failed\" and \"the sandbox is broken\" are "+
			"different facts.\n\n"+
			"NOTHING RUNS IN cloud. The command is streamed to the Kubernetes exec "+
			"subresource of the sandbox's pod, which runs under the gVisor runtime class. "+
			"The sandbox is addressed by pod NAME through the apiserver, never by address.")
	openapi.Describe("/v1/sandboxes/:id/fs", http.MethodGet,
		"Read a file, or list a directory",
		"Reads one file from the sandbox's project directory as text, or lists the entries "+
			"when the path names a directory. Paths resolve under the project root and a path "+
			"that climbs out is refused rather than rewritten.")
	openapi.Describe("/v1/sandboxes/:id/fs", http.MethodPost,
		"Write a file",
		"Writes the request body to one file in the sandbox's project directory, creating "+
			"parent directories. Same confinement as the read above.")
}
