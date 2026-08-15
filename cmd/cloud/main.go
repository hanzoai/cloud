// Command cloud is the Hanzo Cloud router: one binary that serves the whole API
// by mounting every subsystem as its own process, started on the first request
// that reaches it.
//
// It IS the shipped binary now — the fused monolith that imported apps and linked
// all 112 subsystem graphs into one ~3105-package link is gone. This host knows
// only where each app lives and what path it answers — never what the app does —
// so it links zip, the generated manifest, and the light webui console embed, and
// nothing else. A subsystem changing rebuilds itself; the host is untouched.
//
// Lazy is what makes 112 services affordable. Mounting them eagerly costs 112
// processes, 112 resident sets and 112 startup times at boot for a set that is
// mostly idle. Mounted lazily, an app nobody calls costs a route entry and a
// struct; the cost moves to the first request that needs it. The subsystems that
// cannot wait for a request — the ones that own a listener or a background
// loop — say so in apps.go's `eager` map and start with the host.
//
// The apps themselves are unchanged and unaware: each is the same plugin/<name>
// binary that already exists, serving the same routes through the same
// cloud.Listen middleware it would serve standalone. Identity, billing and
// telemetry run in the app's own process, where they already ran.
//
// The host is the FRONT DOOR, so it owns three things no plugin can: it serves
// the white-labelled console at "/" (webui, mounted last so every app prefix
// wins); it threads the deployment's operator flags to the children as CLOUD_*
// env (run→forward). The data-plane key is NOT one of those: it arrives in the
// environment from KMS and every child inherits it, which is how each one opens
// the encrypted files they all share.
//
// Deployment is one directory: the host plus its plugins, which is what the image
// already ships. Point CLOUD_<NAME>_ADDR at an instance running elsewhere, or
// CLOUD_<NAME>_BIN at a specific build, to override one of them.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanzoai/cloud/fleet"
	"github.com/hanzoai/cloud/internal/datadir"
	"github.com/hanzoai/cloud/internal/edge"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/hanzoai/cloud/internal/writerlease"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plugin"
	"github.com/hanzoai/cloud/webui"
	"github.com/hanzoai/cloud/webui/release"
	"github.com/zap-proto/zip"
)

func main() {
	// BEFORE any plugin is mounted: the wire to a plugin resolves its transport
	// from a process-global registry at dial time, and the default one caps a
	// whole response at 30 seconds — which silently truncated every model
	// completion longer than that (transport.go).
	useLongPluginDeadline()

	listen := flag.String("listen", environ.Or("CLOUD_LISTEN", ":8080"), "HTTP listen address")
	zapAddr := flag.String("zap", environ.Or("CLOUD_ZAP_LISTEN", ":9653"), "ZAP-RPC listen address")
	// The operator flags helm and universe pass to the ENTRYPOINT. The host
	// consumes none of them itself — it is a router; it opens no store and
	// validates no token — but the per-app CHILDREN read them from the environment
	// (cloud.LoadConfig), so the host re-publishes each non-empty one as its
	// CLOUD_* variable before it spawns anything, and the children inherit it
	// through os.Environ(). A flag the host silently DROPPED would be a silent
	// misconfig — a lux deployment left validating tokens against hanzo.id.
	brand := flag.String("brand", "", "white-label brand → CLOUD_BRAND")
	domain := flag.String("domain", "", "primary public domain → CLOUD_DOMAIN")
	dataDir := flag.String("data-dir", "", "on-disk data root → CLOUD_DATA_DIR")
	iamIssuer := flag.String("iam-issuer", "", "OIDC issuer for JWT validation → CLOUD_IAM_ISSUER")
	flag.Parse()

	forward(map[string]string{
		"CLOUD_BRAND":      *brand,
		"CLOUD_DOMAIN":     *domain,
		"CLOUD_DATA_DIR":   *dataDir,
		"CLOUD_IAM_ISSUER": *iamIssuer,
	})

	if err := run(*listen, *zapAddr); err != nil {
		fmt.Fprintln(os.Stderr, "cloud:", err)
		os.Exit(1)
	}
}

// forward re-publishes the operator flags as the CLOUD_* environment the per-app
// children read, so a value set once on the entrypoint reaches every subsystem
// through zip's append(os.Environ(), …) spawn. An EMPTY value is skipped, never
// written: a helm template that renders `--iam-issuer=` (the value unset) must
// not CLOBBER a CLOUD_IAM_ISSUER already in the environment with an empty string.
func forward(kv map[string]string) {
	for k, v := range kv {
		if v != "" {
			_ = os.Setenv(k, v)
		}
	}
}

// doorConfig is the front door's transport posture. The door installs no
// middleware -- that is the program's job behind it -- but it still TERMINATES
// public HTTP, so the transport ceilings are its to set. They were not set: this
// app was built with the framework defaults while cloud.App() configured the
// program behind it correctly, and the door refuses a body before the program
// ever sees it. GATEWAY_BODY_LIMIT read 100 MiB in the pod's environment and
// 4,194,305 bytes still answered 400, because 4 MiB is the fasthttp default and
// nothing here had ever asked.
//
// The numbers come from cloud, not from literals here. A literal is what made
// the two disagree in the first place.
func doorConfig() zip.Config {
	return zip.Config{
		AppName:        "cloud",
		MCP:            zip.MCPConfig{Disabled: true},
		ReadBufferSize: edge.ReadBufferSize(),
		BodyLimit:      edge.BodyLimit(),
	}
}

func run(addr, zapAddr string) error {
	// THE FLEET'S ONE AGENT DOOR is served BY THIS HOST, at POST /v1/mcp, and
	// zip's is switched off so that exactly one handler holds the address.
	//
	// zip's door answers out of an app's own typed-op registry plus the build-time
	// catalogues a host hands it. This host has neither: it registers no op, and
	// the catalogues are deleted. What it has is CHILDREN, and the honest content
	// of the fleet's door is what they serve RIGHT NOW — so the host asks them
	// (fleet.Mount, below, after the mount loops have built the plugin table).
	//
	// The host is still the only process that can own it: a plugin's MCPTools() is
	// in-process, so no subsystem can enumerate a lazy sibling.
	//
	// The address comes from manifest, not from a literal here: the console's
	// terminal handler has to know it too (to refuse to answer a machine door with
	// the SPA shell, and to send an agent that guessed zip's default to the real
	// one), and when those two were written down separately the second one was
	// simply missing — GET /mcp answered 200 text/html for as long as that lasted.
	app := zip.New(doorConfig())

	// A lazy child that has served once stays resident forever unless something
	// stops it, so the pod's process count follows the CATALOG rather than the
	// traffic — and this fleet's catalog is a hundred subsystems under one
	// memory cap. The sweep gives that back: an app nobody has called for
	// manifest.Idle() is stopped, and the next request through its prefix starts
	// it again by the path that already exists.
	//
	// The stop function is bound HERE and deferred, not called through a
	// deferred call of Reap itself — that form evaluates at defer-run time
	// and would start the sweep during shutdown, which is the mistake serveWake
	// records one door over.
	//
	// warm=0 applies the IDLE bound only: an app nobody has called for its
	// IdleAfter is stopped, and nothing is evicted merely for being one process
	// too many. zip also offers an LRU ceiling as the second argument — the bound
	// that would stop a single fleet-wide tools/list from holding every subsystem
	// resident at once — but the number it takes is this pod's memory budget,
	// which is a deployment fact and a decision of its own. Choosing one here,
	// inside a build fix, is how a compile error becomes a change in what the pod
	// does at runtime.
	stopReaping := app.Reap(time.Minute, 0)
	defer stopReaping()

	// THE POD'S WRITER LEASE, and this is the only process that may take it.
	//
	// The lease says "this pod owns this volume", so it belongs to the ROOT of the
	// pod — the process that exists before any store is open and outlives every
	// process that opens one. That is this host. It used to be taken in
	// cloud.Listen instead, which is the body each PLUGIN runs and the one thing
	// this binary never calls, so switching CLOUD_WRITER_LEASE on pointed a
	// single-holder lock at the siblings: kms took it, pubsub and kafka waited for
	// a handoff that could not come, nothing bound :8080, and the pod was killed by
	// its own liveness probe and restarted into the same deadlock (2026-08-04,
	// api.hanzo.ai, four minutes of 503).
	//
	// HERE, before the mount loops, because the very next
	// thing this function does is spawn children — and the whole point is that they
	// are born into a pod whose volume is already claimed. Hold stamps the
	// environment they inherit, so each one knows the answer instead of racing for
	// it. Released by the defer AFTER app.Listen returns, which is after zip has
	// drained its shutdown hooks and stopped every child: the lock is withdrawn
	// only once nothing in this pod can still write.
	//
	// Unset CLOUD_WRITER_LEASE ⇒ Hold does nothing at all, which is correct under
	// strategy: Recreate and is what production runs today.
	releaseLease, err := writerlease.Hold(datadir.Resolve(), writerlease.DefaultWait, app.Logger().Info)
	if err != nil {
		return err
	}
	defer func() { _ = releaseLease() }()

	// absent is what this host tried to mount and could not: name → why. Written
	// only by the loops below, read only by the health route registered after
	// them, so it is frozen before anything serves and needs no lock.
	absent := map[string]string{}

	// composed is what THIS deployment put together, in manifest order — the app
	// set spec() describes. Taken here rather than from the loops below because
	// they consume `on` as they go (delete, so a leftover name is a typo), and
	// because the broker-first split would put the fleet's document in an order
	// that is not the fleet's.
	//
	// Coresident apps included: an app that mounts as middleware on a sibling's
	// router still SERVES its routes, so it belongs in the document even though
	// the host claims no prefix for it.
	composed := make([]string, 0, len(manifest.Apps))
	for _, a := range manifest.Apps {
		composed = append(composed, a.Name)
	}

	// THE MANIFEST IS THE APP SET. The host mounts what it was built with; there
	// is no second list to disagree with it. The broker below is ordered first
	// because every other child pulls its data-plane key from it, not because it
	// was selected — a deployment that omitted it produced children that all
	// failed at their first store open, which is why naming the set twice was
	// never a smaller deployment, only a broken one.

	// THE BROKER FIRST, and eagerly. Every other app pulls its data-plane key and
	// its scoped credentials from it, so an app that starts before it has nothing
	// to ask — and the broker is itself an app, so left in manifest order it comes
	// up whenever its turn arrives. It is also lazy by default, which means it does
	// not come up at all until a request reaches /v1/kms: the fleet then waits on a
	// process nothing has asked for. Starting it here makes the dependency explicit
	// instead of a property of list order.
	//
	// The two loops differ ONLY in which app they select and how eager it is; what
	// a mount MEANS is one function (mount), so the failure policy cannot drift
	// between them.
	for _, a := range manifest.Apps {
		if err := mount(app, a, a.Eager, absent); err != nil {
			return err
		}
	}

	// Liveness belongs to the HOST, not to any app: it must answer while every
	// plugin is still cold, or a lazy fleet fails its readiness probe before the
	// first real request ever arrives and gets restarted forever. Registered after
	// the mount loops so it closes over the finished absence set — nothing is
	// listening until app.Listen either way, so registration order costs no
	// availability, only route specificity, and /healthz collides with no prefix.
	health(app, absent)

	// The fleet's own description, at /v1/openapi.json. Same reasoning as
	// /healthz, and the same layer: it is the HOST's, because it is about the
	// whole fleet and no plugin can see past itself.
	spec(app, composed)

	// THE AGENT DOOR, at POST /v1/mcp — composed by ASKING, at the moment of
	// asking. Registered after the mount loops so the plugin table it starts from
	// is the finished one, and before anything listens.
	//
	// It is the composed set minus the CORESIDENT apps: a coresident app is
	// middleware on a sibling's router (zen on ai's), so it is not a child this
	// host can start and its ops are already in the sibling's registry — asking
	// for it by name would report a permanent outage for an app that is serving.
	//
	// The Door is KEPT, because the fleet's own subsystems need it as much as an
	// external client does — an agent run inside `agents` has to resolve its tool
	// names against the same aggregated surface. serveWake publishes this same
	// object on the host's internal socket, so there is one gather, one routing
	// table and one curation rule for both directions.
	mcp := fleet.Mount(app, manifest.MCPPath, routed(composed), locate(app))

	// LISTING WHAT THE FLEET SERVES MUST NOT START THE FLEET. Discovery asks a
	// subsystem, and asking a lazy one starts it — so one tools/list started every
	// subsystem this host composes, and the pod's resident cost became the size of
	// the catalog rather than of the work. The door asks the ones that are already
	// running and reads the rest from what they published (fleet/catalog.go); this
	// is the half only the host can answer, because the plugin table is its.
	mcp.Warm = warm(app)

	// The bare /mcp needs no route here. webui's terminal handler answers it from
	// manifest.MCPPath (webui/mcp.go) — one rule, in the one place that can tell a
	// machine door from a client-side console route. A route registered here would
	// be a SECOND implementation of that redirect, and one that is not
	// self-scoping: app.All("/mcp") claims the path unconditionally, so the same
	// call in cloud.Serve would hijack a plugin's OWN zip door at that address.
	//
	// The console at "/" is the HOST's, because the host is the front door: every
	// SPA route (/, /signin, /dashboard, …) is under no app prefix, so it reaches
	// the host's catch-all rather than a plugin. Registered LAST — after every app
	// prefix — so a real /v1 route always wins and only unmatched paths fall
	// through to the white-labelled shell. webui is a light leaf (stdlib + the
	// brand registry), so owning "/" costs the host a handler, not the fleet's
	// package graph.
	// The published-site edge goes BEFORE the console: <slug>.hanzo.app must serve
	// the customer's site, and webui owns "/" for every path no app prefix claims,
	// so mounting it after would let the console answer first — which is exactly
	// the defect. See sites.go. It also installs the resolver the console reads its
	// own release through, so this order is load-bearing twice over.
	mountSites(app)

	// The console's BYTES are a published site release now, not an embed, so the
	// app that owns the release pointer has to be running before the front door can
	// read one. `projects` is lazy — its trigger is a request reaching /v1/projects,
	// and none has arrived — and the resolver the edge just installed dials its
	// socket directly rather than waking it. Without this, the host's first act
	// after mounting is to ask a process that does not exist. Start is idempotent
	// and goes through the SAME single-flighted path a prefix request takes, so this
	// is the ordinary start, made explicit rather than left to whoever happens to
	// call first. Same argument as the broker above: a dependency is cheaper stated
	// than discovered.
	//
	// This moves BOOT ORDER, so both properties that makes it safe are stated here
	// rather than assumed, and both are read off zip v1.25.1 load.go:
	//
	//   - IDEMPOTENT. Start goes through target(), which returns the running
	//     instance when p.cur is already set — so if anything started projects
	//     first this returns its address and spawns no second child. The on-demand
	//     path is single-flighted under p.mu and re-checks p.cur inside the lock,
	//     so a burst of first callers still produces exactly one process.
	//   - IT REFUSES, IT DOES NOT HANG. waitListening is bounded by spec.Start
	//     (p.Start = startTimeout(), 90s, CLOUD_PLUGIN_START) and ALSO returns the
	//     moment the child's process exits, so a broken projects fails in the time
	//     it takes to die rather than in the timeout. An unknown app name fails
	//     immediately ("no plugin named"). A boot that blocks forever on a socket
	//     is worse than one that refuses with a reason; this one refuses.
	if _, err := app.Start(projectsApp); err != nil {
		return fmt.Errorf("console: %s owns the console release and would not start: %w", projectsApp, err)
	}

	// REQUIRED here, unlike in a per-app child (cloud.Listen explains why a child
	// cannot bootstrap this). console.hanzo.ai is this process; a front door that
	// came up with no console would serve the API perfectly while every human who
	// opened the product got a blank page, and it would do it silently. Failing
	// with the reason is the difference between an alert and a mystery.
	//
	// The watcher's lifetime is this function's: run returns only after app.Listen
	// has drained, so cancelling on the way out stops the poll with the process.
	consoleCtx, stopConsole := context.WithCancel(context.Background())
	defer stopConsole()
	consoleSrc, consoleErr := release.Load(consoleCtx, release.ConfigFromEnv(), app.Logger())
	if consoleErr != nil {
		// Loud, and ALIVE. This used to return the error, and the difference is
		// what an unreadable 404.html cost: the object store dropped one read, the
		// front door refused to boot, and api.hanzo.ai answered 503 to every
		// caller — including everyone who never opens a browser. Exiting does not
		// save the console, because a dead process serves a blank page too; it
		// only adds the API to what is lost. There is no state of the world where
		// it leaves a user better off, so it is not the stricter choice, just the
		// more expensive one.
		//
		// The alert this was reaching for is the log line, not the exit. A running
		// process ships it; a CrashLoop takes the telemetry front door down with
		// it and ships nothing.
		app.Logger().Error("console: no release mounted — serving the API without it",
			"err", consoleErr)
	} else {
		// A publish reaches users through this loop, in one poll interval — the
		// whole point of taking the console out of the binary. Stopped when run
		// returns.
		go consoleSrc.Watch(consoleCtx)
	}

	// nil is webui's stated "this process serves no console": the catch-all still
	// keeps the API namespaces honest and still answers the agent door, and a
	// console path gets a 503 saying so. Mounting an empty release ERRORS, so this
	// is conditional for the same reason cloud.Listen's is — doing it
	// unconditionally turns "serves none" back into "starts none".
	if consoleErr == nil {
		if err := webui.Mount(app, release.FS(consoleSrc)); err != nil {
			return fmt.Errorf("console: %w", err)
		}
	} else if err := webui.Mount(app, nil); err != nil {
		return fmt.Errorf("console: %w", err)
	}

	// The start door, AFTER the mount loops so the plugin table it starts from is
	// the finished one, and before anything listens so no child can ask before it
	// is there. Without it every internal call to a lazy app dials a socket that
	// no request has ever caused to exist (wake.go). It closes with the app, so
	// there is nothing here to defer and nothing to forget to.
	serveWake(app, mcp)

	// SIGTERM must reach the children. zip drains its shutdown hooks LIFO, and
	// every Load registered one that stops its process, so this is what keeps a
	// rollout from leaving orphans behind. (On Linux each child also carries
	// Pdeathsig, which covers a host that dies without getting here.)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		// Flip readiness BEFORE tearing anything down, so /readyz tells the truth
		// for whatever window remains. The host cannot call cloud.SetDraining():
		// that flag belongs to the root package, which this binary deliberately
		// does not link (it is zip + manifest + the webui leaf and nothing else),
		// and it is a different PROCESS's state anyway. Same contract, owned by
		// the process K8s actually signals.
		draining.Store(true)
		_ = app.Shutdown()
	}()

	// Stop subsystems nothing is asking for. Every one is its own process, and
	// until this they only ever accumulated: resident memory tracked the size of
	// the catalog rather than the traffic, which is what evicted this pod for
	// node memory and took the API down with it. A stopped plugin costs a cold
	// start on its next request and nothing in between; an Eager one is never a
	// candidate, so identity and config keep their process.
	//
	// The sweep is cheap (a timestamp compare per plugin) so a minute is often
	// enough to be precise without being noisy. Stopped before Shutdown runs,
	// because a sweep in flight reads state Shutdown writes.
	stopReaper := app.Reap(time.Minute, manifest.Warm)
	defer stopReaper()

	// Both transports, same router — the pair cloud.Listen listens on. A bare
	// address is ZAP (zip's default scheme); HTTP has to be spelled out, and
	// omitting it is why a curl against the host answers with a frame-size error
	// instead of JSON.
	return app.Listen(zapAddr, "http://"+addr)
}

// mount composes one app onto the host, and decides what a FAILED mount means.
// That decision is the whole of this file's contribution to availability, so it
// lives in one function that both loops call rather than in a `return err` copied
// twice.
//
// zip reports a start failure by returning an error from the Service (zip
// load.go: `if in, err = start(spec); err != nil { return … }`, surfaced by
// service.go as "zip: Add service N"). Returning an error is a library telling
// its caller the truth; escalating that error to os.Exit is a POLICY, and it was
// this host's. On 2026-07-29 the policy cost 25 minutes of api.hanzo.ai and
// cloud.hanzo.ai because one child could not open one SQLite file.
//
// So: a required app that will not start aborts, by name. Every other app
// degrades to ABSENT — the mount stands with no process behind it, which is a
// state zip already has and already answers 503 for (load.go mountVia: "no
// instance running").
//
// Keeping the mount is not cosmetic. An unregistered prefix falls through to the
// console at "/", and webui only refuses the namespaces in its apiPrefixes list —
// so an absent /v1 app 404s (honest, but indistinguishable from "this deployment
// does not run that subsystem") while the seven prefixes outside that list answer
// 200 text/html. Among them is iam's /login/oauth: an OAuth client would receive
// the console shell instead of a redirect.
//
// Absence is LOUD in three places, because a silently missing subsystem is the
// failure mode this fleet keeps getting bitten by: an error log here, the reason
// on the host's health route, and Running=false in zip's own plugin table.
func mount(app *zip.App, a manifest.App, eager bool, absent map[string]string) error {
	// A co-resident app routes no prefix of its own: it is middleware on another
	// app's router and decides per request whether to serve or Next. There is
	// nothing for the host to claim or spawn, so there is nothing to mount. This
	// is not new behaviour — zen's row named ai's own "/v1", so ai matched first
	// and zen's child never saw a request. The difference is that the fleet now
	// says so instead of relying on registration order to mean it.
	if a.Coresident {
		return nil
	}
	p := a.Plugin()
	p.Lazy = !eager
	// NO BUILD-TIME TOOL CATALOGUE. zip.Plugin.Tools took the array this app's
	// binary projected when it was BUILT (plugin/<app>/mcp.json) so the host could
	// answer tools/list without running anything. That artifact was a second
	// source for a fact the child already knows, and it was wrong: o11y's held 12
	// tools while the o11y binary at the same commit served 365. The door asks the
	// child now (fleet.Mount), so there is nothing to hand over here.
	p.Start = startTimeout()

	// zip v1.23 removed (*App).Add: Use is the ONE composition verb, and zip.Load
	// already returns the leaf *App — which IS a zip.Component — so the child is
	// included by reference rather than through a second registration call.
	leaf, err := zip.Load(p, a.Prefixes...)
	if err == nil {
		app.Use(leaf)
		return nil
	}
	if a.Required {
		return fmt.Errorf("%s is required here and would not start: %w", a.Name, err)
	}
	// A remote mount (CLOUD_<NAME>_ADDR) starts no process — zip.Proxy only builds
	// a client — so its only failures are an unusable address and a route conflict.
	// Both are configuration this deployment got wrong, there is no process to
	// degrade, and a second Load would re-register the same prefixes.
	if p.Addr != "" {
		return fmt.Errorf("%s mounted at %s: %w", a.Name, p.Addr, err)
	}

	app.Logger().Error("subsystem ABSENT: it would not start, and the host is serving without it",
		"name", a.Name, "prefixes", a.Prefixes, "err", err)
	absent[a.Name] = err.Error()

	// Mount it with no process behind it. A failed eager Load registers NOTHING —
	// zip returns before it records the plugin or mounts a prefix — so this is the
	// first registration, not a second one, and it is the same Load with the same
	// spec at the lazy rung of the same ladder a.Plugin() already climbs for the
	// binary. The next request to the prefix retries the start and 503s if it
	// fails again, which is zip's existing answer for a cold plugin and heals the
	// common cause of a boot failure: a dependency that was not up yet.
	p.Lazy = true
	lazy, lerr := zip.Load(p, a.Prefixes...)
	if lerr != nil {
		return fmt.Errorf("%s: mounting it absent failed too: %w", a.Name, lerr)
	}
	app.Use(lazy)
	return nil
}

// routed drops the CORESIDENT apps from a composed set: the ones that mount as
// middleware on a sibling's router and are therefore not children this host can
// reach by name. Their ops are registered on the sibling's app, so the sibling
// already answers for them; asking for one by name would report a permanent
// outage for a subsystem that is serving perfectly.
func routed(composed []string) []string {
	out := make([]string, 0, len(composed))
	for _, name := range composed {
		if !manifest.Coresident(name) {
			out = append(out, name)
		}
	}
	return out
}

// locate is how the door reaches ONE app: the child this host started, or the
// instance an operator pointed CLOUD_<NAME>_ADDR at.
//
// Both are needed because they are reached differently and only the composition
// root knows which is which. zip.App.Start covers a spawned child and is the
// right door for it — idempotent, and the same single-flighted path a request to
// the app's prefix takes, so a burst of askers still produces one process. A
// remotely mounted app is never started, so Start has nothing to report about it
// and would name it unavailable forever.
// warm reports whether a subsystem is running right now, WITHOUT starting it.
//
// A subsystem mounted at an address this host did not start (CLOUD_<NAME>_ADDR)
// is warm by definition: it is somebody else's process, already up, and asking
// it costs this host nothing.
func warm(app *zip.App) func(string) bool {
	remote := map[string]bool{}
	for _, a := range manifest.Apps {
		if a.Plugin().Addr != "" {
			remote[a.Name] = true
		}
	}
	return func(name string) bool {
		if remote[name] {
			return true
		}
		for _, p := range app.Plugins() {
			if p.Name == name {
				return p.Running
			}
		}
		return false
	}
}

func locate(app *zip.App) fleet.At {
	remote := map[string]string{}
	for _, a := range manifest.Apps {
		if addr := a.Plugin().Addr; addr != "" {
			remote[a.Name] = addr
		}
	}
	return func(name string) (addr, path string, err error) {
		if addr := remote[name]; addr != "" {
			return addr, manifest.FrameworkMCPPath, nil
		}
		addr, err = app.Start(name)
		return addr, manifest.FrameworkMCPPath, err
	}
}

// inside is how a door reached from INSIDE the fleet reaches one app: the app's
// own plane socket, where its agent door answers with no edge in front of it
// (cloud.Door). Same start, different door.
//
// A REMOTELY mounted app (CLOUD_<NAME>_ADDR) keeps the edge door, because its
// plane socket is on its own host and no path here reaches it. So an internal
// caller's identity survives into every app this host RUNS, and into a remote one
// only as far as that app's own boundary lets it — which is the honest answer,
// and the same one it has always given.
func inside(app *zip.App) fleet.At {
	edge := locate(app)
	remote := map[string]bool{}
	for _, a := range manifest.Apps {
		if a.Plugin().Addr != "" {
			remote[a.Name] = true
		}
	}
	return func(name string) (addr, path string, err error) {
		if remote[name] {
			return edge(name)
		}
		if _, err := app.Start(name); err != nil {
			return "", "", err
		}
		return zip.SocketPath(name), manifest.MCPPath, nil
	}
}

// draining flips true when this host is shutting down, and is read lock-free by
// /readyz. It is the HOST's copy of the contract drain.go states for the root
// package: the host is the process K8s signals and probes, and it does not link
// the root package to borrow its flag.
var draining atomic.Bool

// health is the host's TWO probe routes, and the split between them is the whole
// point: /healthz answers "is this process alive", /readyz answers "should it be
// sent requests". They are different questions and they had one answer.
//
// /healthz — LIVENESS. 200 while the process routes, always. The host is a
// router, it is up, and 112 cold plugins are not a reason to restart it. Failing
// liveness for a broken plugin recreates the 2026-07-29 outage one layer up: K8s
// would kill a pod that is serving every other subsystem correctly, and the
// replacement would fail identically because the cause is in the image or the
// config, not in the process. `absent` rides in the BODY with its reason —
// "staged" vs "failed" (degraded.go) is unanswerable without it.
//
// /readyz — READINESS. 503 when the host is draining, or when a VITAL subsystem
// is absent (manifest.App.Vital). This is the route that was missing, and its
// absence is what let 2026-08-01 happen: `ai` — the greedy /v1 catch-all, i.e.
// the entire product API — degraded to absent, main.go recorded the reason in
// the `absent` FIELD, and the probe read the STATUS CODE, which was 200. The pod
// stayed Ready with 0 restarts for ~30 minutes while /v1/models 503d.
//
// A 503 here is not an outage, it is the outage becoming VISIBLE, and the timing
// is what makes it cheap: a rollout whose new image cannot start `ai` never gets
// a Ready pod, so the Deployment stalls and the OLD pods keep serving. The bad
// config stops at the first replica instead of reaching all of them. When it is
// already fleet-wide the endpoints empty and the product is down — but it was
// ALREADY down, silently, and now `kubectl get pods` says so.
//
// A non-vital absence stays READY and is still reported. Taking a pod out of
// rotation because one minor subsystem died would turn a partial failure into a
// total one, which is the same mistake as aborting, just later.
//
// Both probes are TYPED ops. They read plain JSON out and nothing else — no
// stream, no bytes, no redirect, no foreign signature — so they carry their
// shape in the registry like any other op, and the same answer is reachable
// over native ZAP typed Call on :9653 as over the kubelet's GET.
func health(app *zip.App, absent map[string]string) {
	zip.Get(app, "/healthz", func(context.Context, *probeIn) (*probeOut, error) {
		return &probeOut{Status: "ok", Absent: stillAbsent(app, absent)}, nil
	}, zip.WithStatus(200),
		zip.WithSummary("Report whether this host process is alive"))

	zip.Get(app, "/readyz", func(context.Context, *probeIn) (*probeOut, error) {
		// Draining first: a pod on its way out is not ready regardless of what
		// it is still able to serve. drain.go describes this contract for the
		// root package's ops listener; the HOST is a different process with its
		// own lifecycle, and it is the one K8s signals and probes.
		if draining.Load() {
			return &probeOut{Status: "draining"}, nil
		}
		a := stillAbsent(app, absent)
		if u := unfit(a); len(u) > 0 {
			return &probeOut{Status: "unfit", Absent: u}, nil
		}
		return &probeOut{Status: "ok", Absent: a}, nil
	}, zip.WithStatus(200, 503),
		zip.WithSummary("Report whether this host should be sent requests"))
}

// probeIn is empty on purpose: both probes read nothing from the caller.
type probeIn struct{}

// probeOut is the one answer both probes give. Absent is declared ahead of
// Status because the raw handlers marshalled a map and encoding/json writes map
// keys sorted, so this order keeps the exact bytes every probe script already
// parses.
type probeOut struct {
	// Absent names each subsystem that failed to start, with its reason.
	// Empty on a healthy host, and omitted then.
	Absent map[string]string `json:"absent,omitempty"`
	// Status is "ok", "draining" (this pod is shutting down) or "unfit" (a
	// vital subsystem is absent).
	Status string `json:"status"`
}

// StatusCode makes the answer carry its own status: draining and unfit are
// 503, everything else 200. The liveness op declares 200 alone, so a healthz
// answer can never smuggle a 503 and flap the kubelet into a restart loop.
func (p *probeOut) StatusCode() int {
	if p.Status == "draining" || p.Status == "unfit" {
		return 503
	}
	return 200
}

// unfit narrows the absence set to the subsystems this deployment has declared
// it should not take traffic without — the intersection of "did not start" and
// "Vital". Reading vitality from manifest.Apps rather than from a list here
// keeps ONE source: the row that declares what an app serves is the row that
// declares whether serving without it is worth doing.
func unfit(absent map[string]string) map[string]string {
	if len(absent) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, a := range manifest.Apps {
		if !a.Vital {
			continue
		}
		if why, ok := absent[a.Name]; ok {
			out[a.Name] = why
		}
	}
	return out
}

// spec is the fleet's published document, and the HOST is the only process that
// can answer for it.
//
// WITHOUT this registration the path is not unclaimed — it is claimed by the
// wrong thing, silently. /v1/openapi.json matches no host route, falls to the
// only prefix that covers it (ai's "/v1", manifest/apps.go), and is proxied to
// the ai child, which answers with openapi.Mount reading ITS OWN router. That
// router's entire AI surface is one greedy All("/v1/*") (apps/ai/ai.go), so
// api.hanzo.ai/v1/openapi.json served a 3.7 KB document of EIGHT paths — the
// child's own health, iam edge, zap, console catch-all and wildcard — while the
// fleet serves 1039. Every SDK generator, every spec-derived CLI and every third
// party reading the published spec read that instead. 200 OK the whole time.
//
// Two properties make the fix the honest one rather than merely a fix:
//
//   - It costs no subsystem. The document is woven from the subsets the plugins
//     projected when they were BUILT (plugin.Spec — bytes in this binary), so
//     answering it starts nothing. A host that had to mount 113 subsystems to
//     describe them would have given back exactly what laziness buys.
//   - It is not a second source of truth. openapi.Fleet is the same composition
//     that WRITES openapi.yaml, over the same committed files, so the served
//     bytes and the committed artifact are one document by construction — and
//     mk/fleet.mk check regenerates those files from source and fails on
//     any diff, so a drifted spec goes red in CI instead of shipping.
//
// Precedence is by SPECIFICITY, not registration order: a static path beats the
// wildcard that contains it whatever order they arrive in (zip's fiber fork,
// ServeMux-1.22 semantics). What would defeat it is an app row claiming this path
// BYTE-IDENTICALLY — fiber merges identical patterns into one chained route and
// the host's handler would sit behind the proxy. manifest/openapi_test.go refuses
// that; cmd/cloud/openapi_test.go pins the resolution end to end.
//
// composed, not the whole manifest, so ENABLEMENT still scopes the document the
// way it did when one binary held everything: a deployment that does not run a
// subsystem must not publish its routes. Production sets no allowlist, so there
// the two are the same list — which is why the artifact comparison holds.
func spec(app *zip.App, composed []string) {
	openapi.MountFleet(app, func() ([]openapi.Part, error) {
		return openapi.Subsets(composed, plugin.Spec)
	})
}

// stillAbsent is the boot failures minus whatever has since come up on its own —
// a plugin that failed eagerly and then started on a later request is not absent,
// and reporting it would train an operator to ignore the field.
//
// It reads the failure set rather than deriving absence from zip's Running flag,
// which is the trap here: almost every app is lazy and cold, so Running=false is
// the NORMAL state for ~110 of 112 and deriving absence from it would report the
// whole fleet as broken.
func stillAbsent(app *zip.App, absent map[string]string) map[string]string {
	if len(absent) == 0 {
		return nil
	}
	up := map[string]bool{}
	for _, s := range app.Plugins() {
		if s.Running {
			up[s.Name] = true
		}
	}
	out := make(map[string]string, len(absent))
	for name, why := range absent {
		if !up[name] {
			out[name] = why
		}
	}
	return out
}

// startTimeout bounds how long a plugin may take to listen. zip's default is 10s,
// which an app that opens stores, runs migrations and seeds a catalog does not
// meet on a cold volume — commerce misses it, the host reports "did not listen
// within 10s", and its prefix answers 502 for an app that was merely still
// booting. The cost of waiting is paid only by a slow start; the cost of not
// waiting is a subsystem that never comes up.
//
// CLOUD_PLUGIN_START overrides it for a deployment on slower storage.
func startTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("CLOUD_PLUGIN_START")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 90 * time.Second
}

// enabled parses the subsystem allowlist. nil means every app, which is the
// default and the shape a full deployment runs.
