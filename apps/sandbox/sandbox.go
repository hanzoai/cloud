package sandbox

import (
	"context"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/apps/dev"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/fleet"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// class is what a sandbox class IS, in one row: how long its lease runs, how
// much of a node it may hold, whether its image has a program of its own, and
// whether it needs a CPU it can virtualise.
//
//	exec    — a chat or function code run. No volume, seconds to minutes.
//	dev     — a coding sandbox. Project volume, a toolchain, hours.
//	desktop — dev plus a virtual display for computer use.
//	browser — desktop with Chromium open full screen and DevTools on loopback.
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
	// burst is what the pod may use past its request when its node has it spare:
	// its LIMITS. Empty is the request, or the fleet's limit for a class that
	// requests the default. It is not a reservation, so it is not priced: the
	// hour follows the request (micros).
	burst envelope
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

// envelope is a pod's resources: CPU, memory and ephemeral disk, as Kubernetes
// quantities.
type envelope struct{ cpu, mem, disk string }

// builder is the burst of a class that runs an agent's build (dev, desktop,
// browser), sized from measured runs (hanzo-inc/cloud#553). In the fleet's 2 CPU
// / 4 GiB / 8 GiB, hanzoai/iam's `make test` ran its race tests pinned at the
// 2-core ceiling (1.5 cores on average), two of its packages ran past go test's
// 10-minute bound, and ~52 minutes in its pod was killed with its memory past
// 3 GiB and climbing. A pod pushed past 4 GiB on purpose dies the same way: the
// exec answers exit 128, `urpc method "containerManager.WaitPID" failed: EOF`,
// and the cluster says OOMKilled — which is exactly how the #464 follow-up's
// `make test` ended. Its writable disk reached ~3 GiB (build cache 1.3, modules
// 0.7, /tmp 0.9) at two-way parallelism, and a pod over its 8 GiB is evicted. A
// pool node is 8 vCPU / 32 GiB / 180 GiB: a build may use its cores when they
// are idle, half its memory, and three times the disk it was evicted at.
var builder = envelope{cpu: "8", mem: "16Gi", disk: "24Gi"}

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
	"dev":     {ttl: 14400, burst: builder, micros: cloud.RuntimeHourMicros},
	"desktop": {ttl: 14400, screen: true, burst: builder, micros: cloud.RuntimeHourMicros},
	// A browser is the desktop with one more layer: its image's own program opens
	// Chromium full screen on the display and listens for DevTools on loopback, so
	// the screen shows what a person watches and the same page is what an agent
	// drives. It holds what a desktop holds and costs what a desktop costs.
	"browser": {ttl: 14400, screen: true, burst: builder, micros: cloud.RuntimeHourMicros},
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
// scratch reports whether a class may run with no project: it asks for no
// device and no more than the exec box.
func (c class) scratch() bool { return !c.kvm && c.cpu == "" && c.mem == "" && c.disk == "" }

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
	// passes are the opened previews and web carries their requests into the
	// sandboxes (web.go).
	passes *passes
	web    *relay
	// The lifetime policy, read ONCE here so a sweep cannot see two policies
	// halfway through a pass. See lifecycle.go.
	clk clocks
	// pulse is how often a command in flight stamps its sandbox as used (api.go).
	pulse time.Duration
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
	debit func(payer string, u meter.Usage)
	// audit is this process's tamper-evident trail. An admin acting on a sandbox
	// somebody else leased is written to it (crossed): that sandbox holds the
	// lessee's session, and a coding run's holds a write key and the codebase's
	// secrets. Nil on a deployment with no trail, where the act is logged instead.
	audit *audit.Recorder
	// ai is the model gateway the sandbox agent works through (agent.go). Nil where
	// the deployment mounts none, and then the route says so rather than failing.
	ai infer.Client
	// quotas are the last quotas commerce answered, per account (quota.go).
	quotas *quotas
}

// debit is the deployment's money sink: the runtime meter's debits, on the org's
// commerce ledger, under this package's own name.
//
// This is where the lease row's stored payer becomes an address. It is the last
// point this package holds the value — the string comes off Running.Payer, which
// the store wrote when the lease was taken — so parsing it here is one site rather
// than one per RuntimeSweep call. The row holds the subject of the account the
// lease was gated on, and PayerOf is upstream's own parse of a subject: a bare slug
// is that org's account, an "<org>/<name>" key the member's, so the runtime is
// billed to exactly the wallet the lease fee was.
func debit(b cloud.Base) func(string, meter.Usage) {
	return func(payer string, u meter.Usage) {
		b.Bill.Record(account.PayerOf("", payer), "sandbox", u)
	}
}

// storeFor is the ONE way this package reaches a store, through
// orgns.OrgNamespace — the single path a VALIDATED org takes. org must
// already come from principal.Org, never from a body field.
func storeFor(s *cloud.Service[state], org string) (*Store, error) {
	ns, err := principal.OrgNamespace(org, "")
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
		return errors.New("sandbox: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("sandbox.Use:  empty DataDir")
	}
	b := cloud.NewBase(deps, "sandbox")
	rt := newRuntime()
	// The fleet registry is how a lease that NAMES a cluster reaches it: the
	// org's sealed kubeconfig, unsealed from KMS behind the one interface a
	// test can stand in for. See runtime.at.
	rt.attached = fleet.New(deps.KMS, b.Log)
	s := &cloud.Service[state]{Base: b, State: state{
		stores:  cloud.NewOrgStore(b, "sandbox", openStore),
		rt:      rt,
		tickets: newTickets(),
		work:    newWork(),
		passes:  newPasses(),
		web:     newRelay(rt),
		clk:     newClocks(),
		pulse:   pulseEvery,
		debit:   debit(b),
		audit:   deps.Audit,
		ai:      deps.AI,
		quotas:  &quotas{},
	}}
	Routes(app, s)
	if err := dev.Routes(app); err != nil {
		return err
	}
	// Dev's browser listener. A deployment without a certificate has none, and says
	// so, rather than stopping the sandbox.
	if rt, err := dev.NewRuntime(b, deps.AI, cloud.EmbeddedTasks); err != nil {
		b.Log.Error("dev: no wasm runtime", "error", err)
	} else {
		rt.Serve()
	}
	if _, err := dev.Listen(); err != nil {
		b.Log.Error("dev: no browser listener", "error", err)
	}
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
		"bare", s.State.rt.bare, "pools", slices.Sorted(maps.Keys(s.State.rt.pools)),
		"cluster", s.State.rt.ready() == nil,
		"reapEvery", reapEvery, "idleConnected", s.State.clk.connected,
		"idleDisconnected", s.State.clk.disconnected, "maxLife", s.State.clk.absolute)
	return nil
}

// Routes registers everything this package serves.
//
// The collection and member routes used to be left out, on the reasoning that
// they were "shared with the compute surface" — true while this served
// /v1/compute/machines, which visor owns and where a second registration of one
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
	g := app.Group("/v1/sandbox")
	// The collection is the group's own address, declared as the empty leaf.
	g.Get("", o.list)
	g.Post("", o.create, zip.WithStatus(http.StatusCreated))
	// The account's quota, a static leaf, so it is never read as a sandbox id. Staff
	// set it in admin.hanzo.ai; the account reads it.
	g.Get("/quota", o.quota)

	g.Get("/:id", o.get)
	g.Delete("/:id", o.del)
	g.Post("/:id/exec", o.exec)
	// Parking and resuming are typed, so an agent asking the MCP server what it
	// can do is told a sandbox can be kept without being paid for.
	g.Post("/:id/pause", o.park)
	g.Post("/:id/resume", o.resume)
	// UNTYPED BY DESIGN — fsRead answers text/plain (a file as its bytes, a
	// directory as one entry per line) and fsWrite takes the file's RAW BYTES as
	// its body. A typed op always marshals JSON and always decodes its body as
	// JSON, so typing either would move the wire rather than describe it.
	g.Raw(http.MethodGet, "/:id/fs", cloud.Handle(s, fsRead))
	g.Raw(http.MethodPost, "/:id/fs", cloud.Handle(s, fsWrite))

	// A terminal or a screen is a credential, a page and a socket, registered
	// together so none can be missing. The page and the socket are two addresses
	// because the ticket is spent once; see terminal.go and screen.go.
	g.Post("/:id/terminal/ticket", o.terminalTicket, zip.WithStatus(http.StatusCreated))
	g.Raw(http.MethodGet, "/:id/terminal", cloud.Handle(s, serve(document)))
	g.Raw(http.MethodGet, "/:id/terminal/ws", cloud.Handle(s, pty))
	g.Post("/:id/screen/ticket", o.screenTicket, zip.WithStatus(http.StatusCreated))
	g.Raw(http.MethodGet, "/:id/screen", cloud.Handle(s, serve(desktop)))
	g.Raw(http.MethodGet, "/:id/screen/ws", cloud.Handle(s, watch))
	// A model working the sandbox while its person watches (agent.go). Raw: the
	// answer is an event stream that outlives the handler.
	g.Raw(http.MethodPost, "/:id/agent", cloud.Handle(s, agentRun))

	// A port in a browser (web.go): the ticket, then every request on the
	// preview's own origin, which the router rewrites onto this address.
	g.Get("/:id/ports", o.ports)
	g.Post("/:id/preview", o.preview, zip.WithStatus(http.StatusCreated))
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		g.Raw(method, "/preview/:id/:port/*", cloud.Handle(s, web))
	}

	// THE AGENT'S MCP SERVER. Everything above is a RAW route, and a raw route is
	// invisible to every projection zip derives from its typed registry — REST is
	// the only one it reaches. So an agent asking the fleet MCP server what it can
	// do was told nothing about sandboxes, while the child answered tools/list
	// happily with an empty array: absent from the tool list AND absent from the
	// outage list. Silent absence, which is the shape that cost the most today.
	//
	// The typed ops are registered here rather than written fresh, because they
	// already exist one file over — expose() puts these exact five on
	// cloud.Plane() (apps/sandbox/peer.go), which is a DIFFERENT zip.App on a
	// DIFFERENT socket that the fleet never asks. Same handlers, same types, now
	// also on the server the fleet does ask. Nothing new is invented and there is
	// no second implementation to drift.
	//
	// This is what stands between "@hanzo can run code" and "@hanzo can lease a
	// computer": the run path was built and reachable, and no agent could name it.
	g.Post("/lease", planeLease,
		zip.WithOperationID("lease_sandbox"),
		zip.WithSummary("Lease a sandbox — a real computer — or resume one you hold"))
	g.Post("/run", planeRun,
		zip.WithOperationID("run_in_sandbox"),
		zip.WithSummary("Run a command in a sandbox you hold and read its output"))
	g.Post("/read", planeRead,
		zip.WithOperationID("read_sandbox_file"),
		zip.WithSummary("Read a file from a sandbox you hold"))
	g.Post("/write", planeWrite,
		zip.WithOperationID("write_sandbox_file"),
		zip.WithSummary("Write a file into a sandbox you hold"))
	// STOP ENDS THE WORK; END ENDS THE RESOURCE. They are two verbs because a
	// run that has gone wrong is one somebody still wants to look at, and an
	// agent told to "stop" that deleted the pod would take the checkout, the
	// logs and the half-written file with it.
	g.Post("/stop", planeStop,
		zip.WithOperationID("stop_run"),
		zip.WithSummary("Stop what a sandbox is running, and keep the sandbox"))
	g.Post("/end", planeEnd,
		zip.WithOperationID("end_sandbox"),
		zip.WithSummary("End a sandbox and release it"))
	g.Post("/computer", planeComputer,
		zip.WithOperationID("computer_in_sandbox"),
		zip.WithSummary("Use a desktop sandbox's screen, mouse, keyboard and browser"))
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
	if cloud.DataDir() == "" {
		return nil, fmt.Errorf("sandbox.New: empty DataDir")
	}
	b := cloud.NewBase(deps, "sandbox")
	rt := newRuntime()
	rt.attached = fleet.New(deps.KMS, b.Log)
	return &cloud.Service[state]{Base: b, State: state{
		stores:  cloud.NewOrgStore(b, "sandbox", openStore),
		rt:      rt,
		tickets: newTickets(),
		work:    newWork(),
		passes:  newPasses(),
		web:     newRelay(rt),
		clk:     newClocks(),
		pulse:   pulseEvery,
		debit:   debit(b),
		audit:   deps.Audit,
		ai:      deps.AI,
		quotas:  &quotas{},
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
	// c.Forward() and not c.Context(): an untyped handler's own context does not
	// carry the caller, and who is asking decides whether the sandbox is theirs.
	e, err := Read(s, c.Forward(), o, idParam(c), c.Query("path"))
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
	path, n, err := Write(s, c.Forward(), o, idParam(c), c.Query("path"), c.Body())
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
	spec.Describe("/v1/sandbox", http.MethodGet,
		"The sandboxes this org holds",
		"Lists the caller org's sandboxes, newest first. `project` and `status` narrow it, "+
			"and both are read from the QUERY STRING.\n\n"+
			"It answers from the org's own store rather than from the cluster, so a sandbox "+
			"whose pod has since died still appears, carrying the status it was last known to "+
			"have. That is deliberate: a lease you are being charged for should not vanish "+
			"from the list because the thing behind it fell over.")
	spec.Describe("/v1/sandbox", http.MethodPost,
		"Lease a sandbox",
		"Creates a sandbox and returns it. `class` is one of `exec`, `dev`, `desktop`, `browser` or `android`; "+
			"`dev`, `desktop` and `browser` are attached to a `project`, which is required for them and "+
			"names the volume the work persists on. `ttlSec` bounds the lease, and `image` "+
			"overrides the class default.\n\n"+
			"This is the ONLY path that creates cluster objects. The isolation boundary is the "+
			"pod's runtime class, one field, so what a sandbox is confined by is a deployment "+
			"decision rather than anything this operation negotiates.")
	spec.Describe("/v1/sandbox/:id", http.MethodGet,
		"One sandbox",
		"Returns one of the caller org's sandboxes. An id belonging to another org answers "+
			"404 and not 403 — a 403 would confirm the id exists, and whether a given sandbox "+
			"exists is itself a cross-tenant fact.")
	spec.Describe("/v1/sandbox/:id", http.MethodDelete,
		"End a sandbox",
		"Stops the sandbox's pod and drops the lease. The VOLUME survives by default, so a "+
			"`dev` or `desktop` sandbox can be leased again over the same project and find its "+
			"checkout where it left it.\n\n"+
			"`purge=1` deletes the volume too. It is opt-in because it is the one part of this "+
			"that cannot be undone.")
	spec.Describe("/v1/sandbox/:id/exec", http.MethodPost,
		"Run a command in a sandbox",
		"Runs a command inside the sandbox and returns its exit code, stdout and stderr. "+
			"A non-zero exit is a SUCCESSFUL call carrying a failed program — the HTTP status "+
			"stays 200, because \"the tests failed\" and \"the sandbox is broken\" are "+
			"different facts.\n\n"+
			"NOTHING RUNS IN cloud. The command is streamed to the Kubernetes exec "+
			"subresource of the sandbox's pod, which runs under the gVisor runtime class. "+
			"The sandbox is addressed by pod NAME through the apiserver, never by address.")
	spec.Describe("/v1/sandbox/:id/fs", http.MethodGet,
		"Read a file, or list a directory",
		"Reads one file from the sandbox's project directory as text, or lists the entries "+
			"when the path names a directory. Paths resolve under the project root and a path "+
			"that climbs out is refused rather than rewritten.")
	spec.Describe("/v1/sandbox/:id/fs", http.MethodPost,
		"Write a file",
		"Writes the request body to one file in the sandbox's project directory, creating "+
			"parent directories. Same confinement as the read above.")
	spec.Describe("/v1/sandbox/:id/terminal/ticket", http.MethodPost,
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
	spec.Describe("/v1/sandbox/:id/terminal", http.MethodGet,
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
	spec.Describe("/v1/sandbox/:id/terminal/ws", http.MethodGet,
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
	spec.Describe("/v1/sandbox/:id/screen/ticket", http.MethodPost,
		"Open a screen",
		"Mints a SINGLE-USE ticket for this sandbox's DISPLAY and returns "+
			"`{ticket, expiresIn, url}`, where url is the desktop PAGE with the ticket already "+
			"on it. The same ticket as the terminal's, minted for a different endpoint.\n\n"+
			"A ticket says which org and which sandbox, and the terminal and the screen are two "+
			"views of one machine: a caller who may type in a sandbox may look at it. What the "+
			"endpoint decides is the URL handed back, which is the only part that differs.\n\n"+
			"Mint one per screen, and mint a fresh one to reconnect.")
	spec.Describe("/v1/sandbox/:id/screen", http.MethodGet,
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
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		spec.Describe("/v1/sandbox/preview/:id/:port/*", method,
			"A preview's own request",
			"What a browser asks of a preview host, https://sandbox-<id>-preview-<port>.<apex>/, "+
				"rewritten here by the router and carried to the port inside the sandbox: pages, assets, "+
				"APIs and WebSockets, answered in the app's own shape. Served only under that host, to a "+
				"browser holding the cookie POST /v1/sandbox/{id}/preview's URL sets. It is not called "+
				"directly.")
	}
	spec.Describe("/v1/sandbox/:id/screen/ws", http.MethodGet,
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
