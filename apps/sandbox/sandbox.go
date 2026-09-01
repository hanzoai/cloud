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
//	POST   /v1/sandbox             {kind:"sandbox", class, project?, ttlSec?} -> Sandbox
//	GET    /v1/sandbox             ?kind=&project=&status=
//	GET    /v1/sandbox/:id
//	DELETE /v1/sandbox/:id         ?purge=1 drops the volume too
//	POST   /v1/sandbox/:id/exec    {argv|command, stdin?, timeoutSec?} -> {exitCode,stdout,stderr}
//	GET    /v1/sandbox/:id/fs      ?path=  read a file, or list a directory
//	POST   /v1/sandbox/:id/fs      ?path=  write a file
//	POST   /v1/sandbox/:id/terminal/ticket a single-use ticket for one terminal
//	GET    /v1/sandbox/:id/terminal        ?ticket=&arg=  the terminal, as a page
//	GET    /v1/sandbox/:id/terminal/ws     ?ticket=&arg=  the terminal, as a socket
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
	"sort"
	"strconv"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/fleet"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// class is what a sandbox class IS, in one row: how long its lease runs, how
// much of a node it may hold, whether its image has a program of its own, and
// whether it needs a CPU it can virtualise.
//
//	exec    — a chat or function code run. No volume, seconds to minutes.
//	dev     — a coding sandbox. Project volume, a toolchain, hours.
//	desktop — dev plus a virtual display for computer use.
//	android — desktop plus an emulator drawing a phone on that display.
//
// THIS USED TO BE THREE TABLES IN TWO FILES and each of the three was a place a
// new class could be half-added, silently. A class missing from the TTL map got
// `ExpiresAt = now + 0` and was reaped before its caller finished reading the
// reply. A class missing from the `!= "desktop"` test had its image's CMD
// replaced by `sleep infinity`, so the one thing it exists to run never ran and
// the pod looked perfectly healthy. Neither failure says anything at the point
// it happens, which is the whole argument for one row: adding a class is filling
// in a struct, and the compiler asks for every field.
//
// The comment this replaced already CLAIMED this shape — "each is one image tag
// and one resource envelope" — while the envelope half had never been built.
type class struct {
	// ttl is the lease in seconds when the caller names none. Unbounded is not an
	// option for a pod running submitted code on our nodes.
	ttl int
	// cpu, mem and disk are what the pod REQUESTS. Empty takes the fleet default,
	// so a class says only what it needs differently — an android pod holds an
	// entire emulated phone and cannot live inside `exec`'s 512Mi.
	cpu, mem, disk string
	// screen means the image brings up a display and its own program, so cloud
	// must NOT state a command: doing so replaces the image's CMD outright.
	screen bool
	// kvm means the pod needs /dev/kvm. It is a SCHEDULING fact — the device
	// reaches a pod as an extended resource, so a node without the plugin simply
	// never receives this class rather than running it a thousand times slower.
	kvm bool
	// micros is what one hour of this class costs, in micro-USD, when the platform
	// has published no price for it. It sits in the SAME row as the envelope
	// because it is a fact about the same thing: a class is one image tag, one
	// resource envelope, and what an hour of that envelope costs. Held apart, a
	// class that doubled its memory would keep the price of the one it replaced.
	micros int64
}

// classes is the CLOSED set of sandbox shapes.
// The DEFAULT ENVELOPE is 250m/512Mi/2Gi (runtime.go), and it is what a class
// that names no resources gets. Three of the four take it, so three of the four
// are the same size and cost the same hour — the price follows the reservation,
// not the name, which is why `dev` is not more expensive than `exec` for being
// longer-lived. A lease is charged for the hours it is HELD either way.
//
// `android` is the one that differs: 2 CPU against 250m is eight times the cores,
// and 6Gi against 512Mi is twelve times the memory. Twelve is the multiplier
// because memory is what bounds a node — the fleet packs sixteen 512Mi pods onto
// one, and an android pod displaces twelve of them. Pricing it at the default
// rate would sell three quarters of a node for the price of a sixteenth.
var classes = map[string]class{
	"exec":    {ttl: 900, micros: cloud.RuntimeHourMicros},
	"dev":     {ttl: 14400, micros: cloud.RuntimeHourMicros},
	"desktop": {ttl: 14400, screen: true, micros: cloud.RuntimeHourMicros},
	// An emulator is a whole guest machine: 4Gi for the phone's own RAM plus the
	// SDK, the emulator process and the X stack around it. Requesting the fleet
	// default and using this much is the eviction bug written down elsewhere in
	// this file — a pod that asks for less than it takes is permanently first in
	// line when the node runs short.
	"android": {ttl: 14400, screen: true, kvm: true, cpu: "2", mem: "6Gi", disk: "12Gi", micros: 12 * cloud.RuntimeHourMicros},
}

// classNames lists the classes, sorted, for the messages that have to enumerate
// them. Derived rather than written, so a refusal cannot name a set the code
// does not serve — which it did: the 400 said "one of exec, dev, desktop" for as
// long as the table held three, and would have gone on saying it.
func classNames() []string {
	out := make([]string, 0, len(classes))
	for c := range classes {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// KindSandbox is the sandbox this package provisions: a gVisor pod in our own
// cluster. It is a VALUE on the shared /v1/sandbox resource, beside the kinds
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
	// The lifetime policy, read ONCE here so a sweep cannot see two policies
	// halfway through a pass. See lifecycle.go.
	clk clocks
	// debit is WHERE this package's runtime money goes, resolved once at mount for
	// the same reason the policy above is: it is a fact about the deployment, not a
	// decision a sweep makes per row.
	//
	// It is a value rather than a reach through s.Bill because the sink is the one
	// thing a money test has to be able to WATCH, and the alternative watches the
	// wrong thing. Record posts to commerce on a background goroutine, so a
	// test that observed it there would be timing a network client while trying to
	// measure whether a span was billed once — and would go green on a debit that
	// was emitted twice and lost once in flight.
	//
	// The payer is a STRING because cloud.RuntimeSweep hands it one: the sweep's only
	// caller is that function, and what it emits is cloud.Running.Payer, a column read
	// off the stored lease row. So the seam's shape is the sweep's, not a choice made
	// here; it says account.Account the day Running.Payer does.
	debit func(payer string, u metering.Usage)
}

// debit is the deployment's money sink: the runtime meter's debits, on the org's
// commerce ledger, under this package's own name.
//
// This is where the lease row's stored payer becomes an address. It is the last
// point this package holds the value — the string comes off Running.Payer, which
// the store wrote when the lease was taken — so parsing it here is one site rather
// than one per RuntimeSweep call. PayerOf is upstream's own parse: a bare slug is
// that org's account, an "<org>/<name>" key the member's, which is exactly what
// principal.Ledger recorded on the row.
func debit(b cloud.Base) func(string, metering.Usage) {
	return func(payer string, u metering.Usage) {
		b.Bill.Record(account.PayerOf("", payer), "sandbox", u)
	}
}

// storeFor is the ONE way this package reaches a store, through
// cloud.OrgNamespace — the single path a VALIDATED org takes. org must
// already come from principal.Org, never from a body field.
func storeFor(s *cloud.Service[state], org string) (*Store, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// Mount registers the sandbox-sandbox half of /v1/sandbox.
//
// It is composed INTO the app that already owns the /v1/sandbox prefix rather
// than claiming a manifest row of its own: zip refuses two owners for one
// prefix, the compute surface has held that prefix in production for months,
// and a second `sandbox` noun is exactly the duplication this package exists to
// remove. See apps/visor's mount, which is the only caller.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("sandbox.Use:  nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("sandbox.Use:  empty DataDir")
	}
	b := cloud.NewBase(deps, "sandbox")
	rt := newRuntime()
	// The fleet registry is how a lease that NAMES a cluster reaches it: the
	// org's sealed kubeconfig, unsealed from KMS behind the one interface a
	// test can stand in for. See runtime.at.
	rt.attached = fleet.New(deps.Brand, b.Log)
	s := &cloud.Service[state]{Base: b, State: state{
		stores:  cloud.NewOrgStore(b, "sandbox", openStore),
		rt:      rt,
		tickets: newTickets(),
		work:    newWork(),
		clk:     newClocks(),
		debit:   debit(b),
	}}
	Routes(app, s)
	// The peer half. Registered beside the routes because they are two adapters
	// over one domain, and an app that mounted only one of them would be an app
	// whose answer depends on who asked.
	mounted.Store(s)
	// The drain. Without it the org stores never close on a rolling restart, so
	// whatever a pod wrote since its last ship — every lease it ended, every
	// watermark it advanced — is gone with the volume, and the successor hydrates a
	// snapshot in which those leases are still running. See [retire]: the ship after
	// a delete is what makes one ending durable, and this is what makes the last of
	// them durable when the process is asked to stop rather than told to.
	shutdownStores = s.State.stores.CloseAll
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
		"reapEvery", reapEvery, "idleConnected", s.State.clk.connected,
		"idleDisconnected", s.State.clk.disconnected, "maxLife", s.State.clk.absolute)
	return nil
}

// Routes registers everything this package serves.
//
// The collection and member routes used to be left out, on the reasoning that
// they were "shared with the compute surface" — true while this served
// /v1/visor/machines, which visor owns and where a second registration of one
// resource is a conflict. It is /v1/sandbox now, owned outright, and leaving
// them out meant Create, List, Get and Delete existed as exported functions
// that no request could ever reach: a caller could exec in a sandbox it had no
// way to create. The handlers were there, the routes were not, and nothing said
// so — the same shape as the policy that selected no pod and the installer that
// installed nothing.
func Routes(app cloud.Router, s *cloud.Service[state]) {
	// The RESOURCE surface. It is typed now: every one of these was a raw route,
	// which is invisible to every projection zip derives from its typed registry —
	// so a caller could read seven addresses in the document and reach none of them
	// as a tool, a CLI command or an SDK method. The two that stay raw are named in
	// untypedByDesign (typed_wire_test.go) with the wire that keeps them there.
	o := ops{s: s}
	zapp := cloud.ZipApp(app)
	zip.Get(zapp, "/v1/sandbox", o.list)
	zip.Post(zapp, "/v1/sandbox", o.create, zip.WithStatus(http.StatusCreated))

	g := app.Group("/v1/sandbox")
	gz := zapp.Group("/v1/sandbox")
	zip.Get(gz, "/:id", o.get)
	zip.Delete(gz, "/:id", o.del)
	zip.Post(gz, "/:id/exec", o.exec)
	// UNTYPED BY DESIGN — fsRead answers text/plain (a file as its bytes, a
	// directory as one entry per line) and fsWrite takes the file's RAW BYTES as
	// its body. A typed op always marshals JSON and always decodes its body as
	// JSON, so typing either would move the wire rather than describe it.
	g.Get("/:id/fs", cloud.Handle(s, fsRead))
	g.Post("/:id/fs", cloud.Handle(s, fsWrite))

	// The two interactive TICKETS are typed and registered HERE rather than beside
	// their routes, because cmd/zipdoc resolves a router it can READ in the file: a
	// group passed as a parameter is not one, and prose it cannot place is prose
	// silently dropped from the document and the MCP tool. terminal() and screen()
	// keep the three routes that stay raw.
	zip.Post(gz, "/:id/terminal/ticket", o.terminalTicket, zip.WithStatus(http.StatusCreated))
	zip.Post(gz, "/:id/screen/ticket", o.screenTicket, zip.WithStatus(http.StatusCreated))

	terminal(g, s)
	screen(g, s)

	// THE AGENT'S MCP SERVER. Everything above is a RAW route, and a raw route is
	// invisible to every projection zip derives from its typed registry — REST is
	// the only one it reaches. So an agent asking the fleet MCP server what it can
	// do was told nothing about sandboxes, while the child answered tools/list
	// happily with an empty array: absent from the tool list AND absent from the
	// outage list. Silent absence, which is the shape that cost the most today.
	//
	// The typed ops are registered here rather than written fresh, because they
	// already exist one file over — expose() puts these exact five on
	// cloud.Plane() (apps/sandbox/plane.go), which is a DIFFERENT zip.App on a
	// DIFFERENT socket that the fleet never asks. Same handlers, same types, now
	// also on the server the fleet does ask. Nothing new is invented and there is
	// no second implementation to drift.
	//
	// This is what stands between "@hanzo can run code" and "@hanzo can lease a
	// computer": the run path was built and reachable, and no agent could name it.
	if reg := cloud.ZipApp(app); reg != nil {
		zip.Post[plane.LeaseIn, plane.Leased](reg, "/v1/sandbox/lease", planeLease,
			zip.WithOperationID("lease_sandbox"),
			zip.WithSummary("Lease a sandbox — a real computer — or resume one you hold"))
		zip.Post[plane.RunIn, plane.Ran](reg, "/v1/sandbox/run", planeRun,
			zip.WithOperationID("run_in_sandbox"),
			zip.WithSummary("Run a command in a sandbox you hold and read its output"))
		zip.Post[plane.PathIn, plane.Blob](reg, "/v1/sandbox/read", planeRead,
			zip.WithOperationID("read_sandbox_file"),
			zip.WithSummary("Read a file from a sandbox you hold"))
		zip.Post[plane.WriteIn, plane.Wrote](reg, "/v1/sandbox/write", planeWrite,
			zip.WithOperationID("write_sandbox_file"),
			zip.WithSummary("Write a file into a sandbox you hold"))
		// STOP ENDS THE WORK; END ENDS THE RESOURCE. They are two verbs because a
		// run that has gone wrong is one somebody still wants to look at, and an
		// agent told to "stop" that deleted the pod would take the checkout, the
		// logs and the half-written file with it.
		zip.Post[plane.StopIn, plane.Stopped](reg, "/v1/sandbox/stop", planeStop,
			zip.WithOperationID("stop_run"),
			zip.WithSummary("Stop what a sandbox is running, and keep the sandbox"))
		zip.Post[plane.EndIn, struct{}](reg, "/v1/sandbox/end", planeEnd,
			zip.WithOperationID("end_sandbox"),
			zip.WithSummary("End a sandbox and release it"))
	}
}

func orgOf(c *zip.Ctx) (string, bool) { return principal.Org(c) }
func idParam(c *zip.Ctx) string       { return strings.TrimSpace(c.Param("id")) }

// Service is the mounted subsystem, handed back to the app that owns the
// /v1/sandbox prefix so its collection handlers can dispatch into this one.
type Service = cloud.Service[state]

// New builds the subsystem without registering anything, for a host that wants
// to dispatch the shared collection routes itself.
func New(deps cloud.Deps) (*Service, error) {
	if luxlog.Default() == nil {
		return nil, fmt.Errorf("sandbox.New: nil luxlog.Default()")
	}
	if deps.DataDir == "" {
		return nil, fmt.Errorf("sandbox.New: empty DataDir")
	}
	b := cloud.NewBase(deps, "sandbox")
	rt := newRuntime()
	rt.attached = fleet.New(deps.Brand, b.Log)
	return &cloud.Service[state]{Base: b, State: state{
		stores:  cloud.NewOrgStore(b, "sandbox", openStore),
		rt:      rt,
		tickets: newTickets(),
		work:    newWork(),
		clk:     newClocks(),
		debit:   debit(b),
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
	openapi.Describe("/v1/sandbox", http.MethodGet,
		"The sandboxes this org holds",
		"Lists the caller org's sandboxes, newest first. `project` and `status` narrow it, "+
			"and both are read from the QUERY STRING.\n\n"+
			"It answers from the org's own store rather than from the cluster, so a sandbox "+
			"whose pod has since died still appears, carrying the status it was last known to "+
			"have. That is deliberate: a lease you are being charged for should not vanish "+
			"from the list because the thing behind it fell over.")
	openapi.Describe("/v1/sandbox", http.MethodPost,
		"Lease a sandbox",
		"Creates a sandbox and returns it. `class` is one of `exec`, `dev`, `desktop` or `android`; "+
			"`dev` and `desktop` are attached to a `project`, which is required for them and "+
			"names the volume the work persists on. `ttlSec` bounds the lease, and `image` "+
			"overrides the class default.\n\n"+
			"This is the ONLY path that creates cluster objects. The isolation boundary is the "+
			"pod's runtime class, one field, so what a sandbox is confined by is a deployment "+
			"decision rather than anything this operation negotiates.")
	openapi.Describe("/v1/sandbox/:id", http.MethodGet,
		"One sandbox",
		"Returns one of the caller org's sandboxes. An id belonging to another org answers "+
			"404 and not 403 — a 403 would confirm the id exists, and whether a given sandbox "+
			"exists is itself a cross-tenant fact.")
	openapi.Describe("/v1/sandbox/:id", http.MethodDelete,
		"End a sandbox",
		"Stops the sandbox's pod and drops the lease. The VOLUME survives by default, so a "+
			"`dev` or `desktop` sandbox can be leased again over the same project and find its "+
			"checkout where it left it.\n\n"+
			"`purge=1` deletes the volume too. It is opt-in because it is the one part of this "+
			"that cannot be undone.")
	openapi.Describe("/v1/sandbox/:id/exec", http.MethodPost,
		"Run a command in a sandbox",
		"Runs a command inside the sandbox and returns its exit code, stdout and stderr. "+
			"A non-zero exit is a SUCCESSFUL call carrying a failed program — the HTTP status "+
			"stays 200, because \"the tests failed\" and \"the sandbox is broken\" are "+
			"different facts.\n\n"+
			"NOTHING RUNS IN cloud. The command is streamed to the Kubernetes exec "+
			"subresource of the sandbox's pod, which runs under the gVisor runtime class. "+
			"The sandbox is addressed by pod NAME through the apiserver, never by address.")
	openapi.Describe("/v1/sandbox/:id/fs", http.MethodGet,
		"Read a file, or list a directory",
		"Reads one file from the sandbox's project directory as text, or lists the entries "+
			"when the path names a directory. Paths resolve under the project root and a path "+
			"that climbs out is refused rather than rewritten.")
	openapi.Describe("/v1/sandbox/:id/fs", http.MethodPost,
		"Write a file",
		"Writes the request body to one file in the sandbox's project directory, creating "+
			"parent directories. Same confinement as the read above.")
	openapi.Describe("/v1/sandbox/:id/terminal/ticket", http.MethodPost,
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
	openapi.Describe("/v1/sandbox/:id/terminal", http.MethodGet,
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
	openapi.Describe("/v1/sandbox/:id/terminal/ws", http.MethodGet,
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

	// The screen's three, beside the terminal's three. They were published with no
	// prose at all — the sentence lives on each handler's doc comment, but those
	// routes are registered on the GROUP (`/:id/screen/ws`) while this document
	// addresses them as the fleet routes them, so the lift landed under a key
	// nothing asks for. Stated here, at the address, exactly as the terminal's are.
	openapi.Describe("/v1/sandbox/:id/screen/ticket", http.MethodPost,
		"Open a screen",
		"Mints a SINGLE-USE ticket for this sandbox's DISPLAY and returns "+
			"`{ticket, expiresIn, url}`, where url is the desktop PAGE with the ticket already "+
			"on it. The same ticket as the terminal's, minted for a different endpoint.\n\n"+
			"A ticket says which org and which sandbox, and the terminal and the screen are two "+
			"views of one machine: a caller who may type in a sandbox may look at it. What the "+
			"endpoint decides is the URL handed back, which is the only part that differs.\n\n"+
			"Mint one per screen, and mint a fresh one to reconnect.")
	openapi.Describe("/v1/sandbox/:id/screen", http.MethodGet,
		"The screen, as a page",
		"A complete, self-contained desktop — noVNC inline, no other origin — that opens its "+
			"own socket and draws this sandbox's display. Embed it in an iframe and there is "+
			"nothing else to build.\n\n"+
			"`ticket` is the credential from the POST above, carried through to the socket. The "+
			"page is NOT gated: it is inert markup and does not redeem the ticket, because a "+
			"ticket is spent once and a page that spent it would hold a credential that no "+
			"longer opens anything. `frame-ancestors` admits our own brands' hosts and nothing "+
			"further.\n\n"+
			"It is served for every class, not only for `desktop`. The class is a fact about the "+
			"image, and a sandbox with no VNC server already fails exactly — the connection is "+
			"refused and the page says so — where a check here would be a second opinion about "+
			"what is running inside a pod, formed from a label rather than from the pod.")
	openapi.Describe("/v1/sandbox/:id/screen/ws", http.MethodGet,
		"The screen, as a socket",
		"Upgrades to a WebSocket carrying RFB — the VNC wire protocol — from the sandbox's "+
			"display, for a host that brings its own client. Requires `ticket`; a missing, "+
			"expired or already-spent one answers 401 without upgrading.\n\n"+
			"THE WIRE IS RFB, in BINARY frames both ways, and it is not interpreted here: this "+
			"is a pipe between the caller's client and the server inside the pod.\n\n"+
			"THE PIXELS COME OUT THROUGH THE EXEC CHANNEL. The display binds 127.0.0.1 only and "+
			"deliberately nothing else, so there is no address to dial — `socat` joins the "+
			"stream to that loopback port over the same Kubernetes exec subresource every other "+
			"call into a sandbox uses. One way in, one thing to authorize, nothing further "+
			"exposed.\n\n"+
			"The window size is ignored. A browser pane is not the X server's geometry, and the "+
			"client scales what it is given rather than asking a server with no RandR to resize "+
			"itself.")
}
