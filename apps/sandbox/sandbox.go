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
//	POST   /v1/sandboxes/:id/terminal/ticket a single-use ticket for one terminal
//	GET    /v1/sandboxes/:id/terminal        ?ticket=&arg=  the terminal, as a page
//	GET    /v1/sandboxes/:id/terminal/ws     ?ticket=&arg=  the terminal, as a socket
//
// A RUN IS WATCHABLE AND IT IS STOPPABLE. Name a session on a run and the
// command's output is appended to that session's live log as it is produced, so
// a surface reading GET /v1/agents/sessions/stream watches the work happen
// instead of a blank pause; `stop_run` interrupts what a sandbox is running and
// leaves the sandbox leased, because a run that went wrong is one somebody still
// wants to look at. See work.go — both are the same fact, that a command in
// flight is addressable.
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
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
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
	// tickets are the thirty-second, single-use credentials a browser presents
	// to open a terminal. Per service and in memory — see terminal.go for why
	// the one credential a WebSocket can carry is minted rather than borrowed.
	tickets *tickets
	// work is every command in flight, by the sandbox running it, so a caller can
	// stop one. In memory for the same reason the tickets are: it holds a live
	// goroutine's cancel, which exists nowhere but here. See work.go.
	work *work
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
		stores:  cloud.NewOrgStore(b, "sandbox", openStore),
		rt:      newRuntime(),
		tickets: newTickets(),
		work:    newWork(),
	}}
	Routes(app, s)
	// The peer half. Registered beside the routes because they are two adapters
	// over one domain, and an app that mounted only one of them would be an app
	// whose answer depends on who asked.
	mounted.Store(s)
	expose()
	// Start the reaper HERE, not from a route and not from a caller. Nothing
	// else ends a lease: every field it needs was already written on every
	// create and every call, and for want of this one line a sandbox once
	// created ran forever — a proof pod was found still Running 64 minutes
	// after its test had finished.
	go reap(context.Background(), s)
	// A runtime that cannot serve says so ONCE AND IN FULL, at startup. `cluster:
	// false` alone is a symptom with the cause stripped off, and the two causes
	// read nothing alike: a cluster we cannot reach is an outage to page on, a
	// fleet runtime the table has never heard of is a typo to fix. Both
	// fail every lease closed; only one of them is anybody's fault.
	if err := s.State.rt.ready(); err != nil {
		s.Log.Error("sandbox cannot serve", "why", err)
	}
	s.Log.Info("sandbox mounted",
		"namespace", s.State.rt.ns, "image", s.State.rt.image,
		"bare", s.State.rt.bare,
		"cluster", s.State.rt.ready() == nil,
		"reapEvery", reapEvery, "idleAfter", idleAfter)
	return nil
}

// Routes registers everything this package serves.
//
// The collection and member routes used to be left out, on the reasoning that
// they were "shared with the compute surface" — true while this served
// /v1/machines, which visor owns and where a second registration of one
// resource is a conflict. It is /v1/sandboxes now, owned outright, and leaving
// them out meant Create, List, Get and Delete existed as exported functions
// that no request could ever reach: a caller could exec in a sandbox it had no
// way to create. The handlers were there, the routes were not, and nothing said
// so — the same shape as the policy that selected no pod and the installer that
// installed nothing.
func Routes(app cloud.Router, s *cloud.Service[state]) {
	app.Get("/v1/sandboxes", cloud.Handle(s, list))
	app.Post("/v1/sandboxes", cloud.Handle(s, create))

	g := app.Group("/v1/sandboxes")
	g.Get("/:id", cloud.Handle(s, get))
	g.Delete("/:id", cloud.Handle(s, del))
	g.Post("/:id/exec", cloud.Handle(s, execIn))
	g.Get("/:id/fs", cloud.Handle(s, fsRead))
	g.Post("/:id/fs", cloud.Handle(s, fsWrite))

	terminal(g, s)
	screen(g, s)

	// THE AGENT'S DOOR. Everything above is a RAW route, and a raw route is
	// invisible to every projection zip derives from its typed registry — REST is
	// the only one it reaches. So an agent asking the fleet door what it can do
	// was told nothing about sandboxes, while the child answered tools/list
	// happily with an empty array: absent from the tool list AND absent from the
	// outage list. Silent absence, which is the shape that cost the most today.
	//
	// The typed ops are registered here rather than written fresh, because they
	// already exist one file over — expose() puts these exact five on
	// cloud.Plane() (apps/sandbox/plane.go), which is a DIFFERENT zip.App on a
	// DIFFERENT socket that the door never asks. Same handlers, same types, now
	// also on the server the door does ask. Nothing new is invented and there is
	// no second implementation to drift.
	//
	// This is what stands between "@hanzo can run code" and "@hanzo can lease a
	// computer": the run path was built and reachable, and no agent could name it.
	if reg := cloud.ZipApp(app); reg != nil {
		zip.Post[plane.LeaseIn, plane.Leased](reg, "/v1/sandboxes/lease", planeLease,
			zip.WithOperationID("lease_sandbox"),
			zip.WithSummary("Lease a sandbox — a real computer — or resume one you hold"))
		zip.Post[plane.RunIn, plane.Ran](reg, "/v1/sandboxes/run", planeRun,
			zip.WithOperationID("run_in_sandbox"),
			zip.WithSummary("Run a command in a sandbox you hold and read its output"))
		zip.Post[plane.PathIn, plane.Blob](reg, "/v1/sandboxes/read", planeRead,
			zip.WithOperationID("read_sandbox_file"),
			zip.WithSummary("Read a file from a sandbox you hold"))
		zip.Post[plane.WriteIn, plane.Wrote](reg, "/v1/sandboxes/write", planeWrite,
			zip.WithOperationID("write_sandbox_file"),
			zip.WithSummary("Write a file into a sandbox you hold"))
		// STOP ENDS THE WORK; END ENDS THE RESOURCE. They are two verbs because a
		// run that has gone wrong is one somebody still wants to look at, and an
		// agent told to "stop" that deleted the pod would take the checkout, the
		// logs and the half-written file with it.
		zip.Post[plane.StopIn, plane.Stopped](reg, "/v1/sandboxes/stop", planeStop,
			zip.WithOperationID("stop_run"),
			zip.WithSummary("Stop what a sandbox is running, and keep the sandbox"))
		zip.Post[plane.EndIn, struct{}](reg, "/v1/sandboxes/end", planeEnd,
			zip.WithOperationID("end_sandbox"),
			zip.WithSummary("End a sandbox and release it"))
	}
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
		stores:  cloud.NewOrgStore(b, "sandbox", openStore),
		rt:      newRuntime(),
		tickets: newTickets(),
		work:    newWork(),
	}}, nil
}

// The ADAPTERS. Each is bind, call, JSON — and nothing else. Every decision they
// used to make (which class is legal, which org owns the row, what a non-zero exit
// means) moved to api.go, where a peer app can reach it too. What is left is the
// part that is genuinely about HTTP: where a value comes from on the wire, and
// which status carries it back.

// createBody is what a request may state about a sandbox. Named rather than
// anonymous so the fields it does NOT carry are checkable: no org and no
// runtime, which are the two facts runtimeFor derives from and the two a caller
// must never be able to hand in. See trust_test.go.
type createBody struct {
	Kind    string `json:"kind"`
	Class   string `json:"class"`
	Project string `json:"project"`
	Image   string `json:"image"`
	// Runtime is the isolation boundary the caller would LIKE. The server still
	// decides — runtimeFor refuses a choice it cannot honour instead of quietly
	// substituting one — so this field may be asked for by anyone and obtained
	// by no one the policy would turn away. The sandbox that comes back carries
	// the runtime it GOT, which is the field to read.
	Runtime string `json:"runtime"`
	TTLSec  int    `json:"ttlSec"`
}

func create(s *Service, c *zip.Ctx) error {
	o, ok := orgOf(c)
	if !ok {
		return principal.Refused(c)
	}
	var body createBody
	if err := c.Bind(&body); err != nil {
		return err
	}
	// principal.IsSuperAdmin is THE predicate — membership of the reserved `admin`
	// org, attested by the identity middleware. It is read here and nowhere else in
	// this package: what it decides is which image a `dev` sandbox runs (imageFor),
	// and a second caller of it would be a second answer to drift from.
	m, err := Lease(s, c.Context(), o, principal.IsSuperAdmin(c), Spec{
		Class: body.Class, Project: body.Project, Image: body.Image,
		Runtime: body.Runtime, TTLSec: body.TTLSec})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, m)
}

func list(s *Service, c *zip.Ctx) error {
	o, ok := orgOf(c)
	if !ok {
		return principal.Refused(c)
	}
	out, err := List(s, c.Context(), o, c.Query("project"), c.Query("status"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"sandboxes": out})
}

func get(s *Service, c *zip.Ctx) error {
	o, ok := orgOf(c)
	if !ok {
		return principal.Refused(c)
	}
	m, err := Get(s, c.Context(), o, idParam(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, m)
}

func del(s *Service, c *zip.Ctx) error {
	o, ok := orgOf(c)
	if !ok {
		return principal.Refused(c)
	}
	if err := End(s, c.Context(), o, idParam(c), c.Query("purge") == "1"); err != nil {
		return err
	}
	c.Status(http.StatusNoContent)
	return nil
}

func execIn(s *Service, c *zip.Ctx) error {
	o, ok := orgOf(c)
	if !ok {
		return principal.Refused(c)
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
	r, err := Run(s, c.Context(), o, idParam(c), Cmd{Argv: body.Argv, Command: body.Command,
		Stdin: body.Stdin, Dir: body.Dir, TimeoutSec: body.TimeoutSec})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, r)
}

// fsRead answers text, because this address always has: a file as its bytes, a
// directory as one entry per line. The typed Entry the core returns is what the
// plane carries; here it is rendered back to the one shape this route has served.
func fsRead(s *Service, c *zip.Ctx) error {
	o, ok := orgOf(c)
	if !ok {
		return principal.Refused(c)
	}
	e, err := Read(s, c.Context(), o, idParam(c), c.Query("path"))
	if err != nil {
		return err
	}
	c.SetHeader("Content-Type", "text/plain; charset=utf-8")
	if e.Dir {
		return c.String(http.StatusOK, strings.Join(e.Entries, "\n")+"\n")
	}
	return c.String(http.StatusOK, string(e.Data))
}

func fsWrite(s *Service, c *zip.Ctx) error {
	o, ok := orgOf(c)
	if !ok {
		return principal.Refused(c)
	}
	path, n, err := Write(s, c.Context(), o, idParam(c), c.Query("path"), c.Body())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"path": path, "bytes": n})
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

// workdirFor is where a sandbox of this class keeps its files, and it is TWO
// values because two contracts name two directories.
//
//	dev, desktop  /work     the project volume's mount point
//	exec          /mnt/data the code interpreter's artifact directory
//
// /mnt/data is not ours to choose. It is what the code tool TELLS THE MODEL to
// write to ("Persist handoff artifacts in `/mnt/data`", @hanzochat/agents
// CodeExecutor), so a run's plots and CSVs land there whatever this package would
// have preferred. A sandbox that collected /work would have listed an empty
// directory after every successful run and reported no files at all — the failure
// would have looked like "the model did not write anything", which is the kind of
// wrong answer nobody debugs.
func workdirFor(class string) string {
	if class == "exec" {
		return execdir
	}
	return workdir
}

// confine resolves a caller path under the sandbox's own root. The sandbox mounts
// its files at that root and nothing above it is addressable — a path that climbs
// out is refused here rather than being sanitized into something else, because
// silently rewriting a path is how a caller ends up reading a file it did not ask
// for and never learns.
func confine(class, p string) (string, error) {
	root := workdirFor(class)
	p = strings.TrimSpace(p)
	if p == "" {
		return root, nil
	}
	if strings.Contains(p, "..") {
		return "", zip.ErrBadRequest("path must not contain ..")
	}
	if strings.HasPrefix(p, "/") {
		if p != root && !strings.HasPrefix(p, root+"/") {
			return "", zip.ErrBadRequest("path must be under " + root)
		}
		return p, nil
	}
	return root + "/" + p, nil
}

// shellQuote makes one argument literal for `sh -c`. Single quotes with the
// close-escape-reopen trick: inside single quotes nothing is special, so the
// only case to handle is a single quote itself.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
	openapi.Describe("/v1/sandboxes", http.MethodGet,
		"The sandboxes this org holds",
		"Lists the caller org's sandboxes, newest first. `project` and `status` narrow it, "+
			"and both are read from the QUERY STRING.\n\n"+
			"It answers from the org's own store rather than from the cluster, so a sandbox "+
			"whose pod has since died still appears, carrying the status it was last known to "+
			"have. That is deliberate: a lease you are being charged for should not vanish "+
			"from the list because the thing behind it fell over.")
	openapi.Describe("/v1/sandboxes", http.MethodPost,
		"Lease a sandbox",
		"Creates a sandbox and returns it. `class` is one of `exec`, `dev` or `desktop`; "+
			"`dev` and `desktop` are attached to a `project`, which is required for them and "+
			"names the volume the work persists on. `ttlSec` bounds the lease, and `image` "+
			"overrides the class default.\n\n"+
			"This is the ONLY path that creates cluster objects. The isolation boundary is the "+
			"pod's runtime class, one field, so what a sandbox is confined by is a deployment "+
			"decision rather than anything this operation negotiates.")
	openapi.Describe("/v1/sandboxes/:id", http.MethodGet,
		"One sandbox",
		"Returns one of the caller org's sandboxes. An id belonging to another org answers "+
			"404 and not 403 — a 403 would confirm the id exists, and whether a given sandbox "+
			"exists is itself a cross-tenant fact.")
	openapi.Describe("/v1/sandboxes/:id", http.MethodDelete,
		"End a sandbox",
		"Stops the sandbox's pod and drops the lease. The VOLUME survives by default, so a "+
			"`dev` or `desktop` sandbox can be leased again over the same project and find its "+
			"checkout where it left it.\n\n"+
			"`purge=1` deletes the volume too. It is opt-in because it is the one part of this "+
			"that cannot be undone.")
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
	openapi.Describe("/v1/sandboxes/:id/terminal/ticket", http.MethodPost,
		"Open a terminal",
		"Mints a SINGLE-USE ticket for one interactive terminal in this sandbox and returns "+
			"`{ticket, expiresIn, url}`, where url is the terminal PAGE with the ticket already "+
			"on it.\n\n"+
			"It exists because a browser carries no Authorization header into a WebSocket or an "+
			"iframe, so a terminal cannot be authenticated the way every other route here is. "+
			"The ticket is a credential MINTED for that one terminal: bound to this org and this "+
			"sandbox, valid for thirty seconds, and gone the first time it is presented. A "+
			"long-lived bearer in a query string would instead be written into every access log "+
			"on the path.\n\n"+
			"Mint one per terminal, and mint a fresh one to reconnect.")
	openapi.Describe("/v1/sandboxes/:id/terminal", http.MethodGet,
		"The terminal, as a page",
		"A complete, self-contained terminal — xterm inline, no other origin — that opens its "+
			"own socket and runs a shell in this sandbox. Embed it in an iframe and there is "+
			"nothing else to build.\n\n"+
			"`ticket` is the credential from the POST above and `arg` names the session (see "+
			"the socket below); both are simply carried through to the socket. The page is NOT "+
			"gated — it is inert markup and does not redeem the ticket, because a ticket is spent "+
			"once and a page that spent it would hold a credential that no longer opens anything.\n\n"+
			"When the terminal is up it posts `{source:\"hanzo-term\", ready:true}` to its parent "+
			"frame, so a host can tell a live terminal from a page that failed into something "+
			"else. `frame-ancestors` admits our own brands' hosts and nothing further.")
	openapi.Describe("/v1/sandboxes/:id/terminal/ws", http.MethodGet,
		"The terminal, as a socket",
		"Upgrades to a WebSocket carrying a login shell on a pseudo-terminal inside the "+
			"sandbox — for a host that brings its own emulator. Requires `ticket`; a missing, "+
			"expired or already-spent one answers 401 without upgrading.\n\n"+
			"THE WIRE. A text frame is stdin, unless it is the one control object "+
			"`{\"resize\":{\"cols\":N,\"rows\":M}}`; a binary frame is always stdin. Output comes "+
			"back as BINARY frames, because a pty emits arbitrary bytes cut at arbitrary offsets "+
			"and a text frame carrying half a rune is one the browser closes the connection over.\n\n"+
			"`arg` names a SESSION: the shell runs under `tmux new -A -s <arg>`, which attaches "+
			"to that session if it exists and creates it if it does not — so one sandbox holds as "+
			"many terminals as a caller has names for. It is 1-64 characters of letters, digits, "+
			"`-` or `_` and may not begin with `-`; anything else is 400. Without `arg` the shell "+
			"is unnamed and unmultiplexed.\n\n"+
			"The shell is `zsh -l`, falling back to `bash -l` and then to `sh -l`, and to the "+
			"plain shell again when the image has no tmux. Every step is a preference and none "+
			"is a requirement: whatever else the image carries — the hanzo CLI included — is a "+
			"command to type, never a condition for getting a prompt.")
}
