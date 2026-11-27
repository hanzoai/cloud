package host

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/writerlease"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plugin"
	"github.com/hanzoai/cloud/preview"
	"github.com/hanzoai/cloud/sdk/edge"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/inflight"
	"github.com/hanzoai/cloud/sdk/object"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/role"
	"github.com/hanzoai/cloud/secret"
	"github.com/hanzoai/cloud/webui"
	"github.com/hanzoai/cloud/webui/release"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Main runs the Hanzo Cloud light-host CLI entrypoint.
func Main() {
	// `cloud secret fetch` reads this service's own secrets and exits — the same
	// secret.Boot a Go service calls at startup, delivered as files for a
	// workload that is not Go. It serves nothing and mounts nothing, so it is
	// answered here, ahead of every listener the server below sets up.
	if len(os.Args) > 1 && os.Args[1] == "secret" {
		if err := secret.Run(context.Background(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "cloud:", err)
			os.Exit(1)
		}
		return
	}

	listen := flag.String("listen", env.Or("CLOUD_LISTEN", ":8080"), "HTTP listen address")
	// LOOPBACK, for the reason config.go states at ZAPListenAddr: this listener
	// serves the identical route surface as HTTP over plaintext TCP, including
	// arbitrary process execution, and a bare ":9653" is reachable from another
	// host on the same subnet with no credential. config.go already chose the
	// safe default and this flag did not, so the two disagreed and this one is
	// what runs. Clusters set CLOUD_ZAP_LISTEN and the environment still wins, so
	// only the unattended developer case moves, which is the case a default is
	// for.
	zapAddr := flag.String("zap", env.Or("CLOUD_ZAP_LISTEN", "127.0.0.1:9653"), "ZAP-RPC listen address")
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

	// WHICH BUILD THIS IS, published the same way and for the same reason: the
	// linker stamps the tag and the commit into THIS binary alone, and the
	// children — which serve /v1/health, where a rollout check reads them — carry
	// no stamp of their own. A per-commit value linked into every per-app binary
	// is a fleet of objects that differ on every commit, so the image could never
	// ship less than all of them (sdk/env, and the Dockerfile).
	//
	// It overwrites rather than defers to the environment, so what a child reports
	// is what this image was built from and never what a pod spec says it was.
	env.ExportBuild()

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

// endpointConfig is the entry point's transport posture. The entry point installs no
// middleware -- that is the program's job behind it -- but it still TERMINATES
// public HTTP, so the transport ceilings are its to set. They were not set: this
// app was built with the framework defaults while cloud.App() configured the
// program behind it correctly, and the entry point refuses a body before the
// program ever sees it. GATEWAY_BODY_LIMIT read 100 MiB in the pod's environment
// and 4,194,305 bytes still answered 400, because 4 MiB is the fasthttp default
// and nothing here had ever asked.
//
// The numbers come from cloud, not from literals here. A literal is what made
// the two disagree in the first place.
func endpointConfig() zip.Config {
	return zip.Config{
		AppName: "cloud",
		MCP:     zip.MCPConfig{Disabled: true},
		// The ceiling on plugin PROCESSES, stated here because zip enforces it
		// where a plugin starts rather than on the reaper's ticker. It has to be:
		// the MCP server below asks every subsystem at once, which starts children
		// far faster than any sweep runs, and a bound restored a minute later is
		// not a bound — that is how this pod came to hold every child it had, stop
		// answering its own liveness probe, and get killed.
		Warm:           manifest.Warm(),
		ReadBufferSize: edge.ReadBufferSize(),
		BodyLimit:      edge.BodyLimit(),
		// Which hops are ours, so the framework's ONE answer at the client is the
		// caller and not the ingress. zip honours a forwarded header only where an
		// app names its own proxies; unnamed, every request through the bar reports
		// the same in-cluster address, and anything keyed on the caller — the free
		// lane's per-visitor ceiling, an audit line — collapses to a single bucket.
		//
		// The list comes from sdk/geo so there is one trust set and one knob
		// (CLOUD_TRUSTED_PROXIES), never a second spelling here to drift from it.
		TrustProxy:     true,
		TrustedProxies: geo.Proxies(),
		ProxyHeader:    "X-Forwarded-For",
	}
}

// Run starts the Hanzo Cloud light-host router on addr (HTTP) and zapAddr (ZAP).
func Run(addr, zapAddr string) (runErr error) {
	return run(addr, zapAddr)
}

func run(addr, zapAddr string) (runErr error) {
	// The shipped cmd/cloud router is not cloud.Listen. Resolve the role here,
	// BEFORE the runtime directory, pod writer lease or child processes: a
	// reader must never mount any of the writer's per-tenant stores.
	haRole, err := role.FromEnv()
	if err != nil {
		return err
	}
	if haRole.IsReader() {
		return serveReaderEdge(addr)
	}
	// THE SHARED RUNTIME DIR, before anything dials a child. The host reaches its
	// children on their plane sockets — the console read and the site edge below,
	// the start endpoint (wake.go) — and each resolves zip.SocketPath when it dials.
	// Bound only when the start endpoint was served, the console's FIRST read went
	// to /run/zip/project.sock, missed the socket project had bound under the data
	// dir, and the console waited a whole poll for a release that was there.
	plane.BindRuntimeDir()

	// THE FLEET'S ONE AGENT MCP SERVER is served BY THIS HOST, at POST /v1/mcp,
	// and zip's is switched off so that exactly one handler holds the address.
	//
	// zip's MCP server answers out of an app's own typed-op registry plus the
	// build-time catalogues a host hands it. This host has neither: it registers
	// no op, and the catalogues are deleted. What it has is CHILDREN, and the
	// honest content of the surface's MCP server is what they serve RIGHT NOW — so
	// the host asks them (client.Use, below, after the mount loops have built the
	// plugin table).
	//
	// The host is still the only process that can own it: a plugin's MCPTools() is
	// in-process, so no subsystem can enumerate a lazy sibling.
	//
	// The address comes from manifest, not from a literal here: the console's
	// terminal handler has to know it too (to refuse to answer an MCP request with
	// the SPA shell, and to send an agent that guessed zip's default to the real
	// one), and when those two were written down separately the second one was
	// simply missing — GET /mcp answered 200 text/html for as long as that lasted.
	app := zip.New(endpointConfig())

	// A lazy child that has served once stays resident forever unless something
	// stops it, so the pod's process count follows the CATALOG rather than the
	// traffic — and this surface's catalog is a hundred subsystems under one
	// memory cap. The sweep gives that back: an app nobody has called for
	// manifest.Idle() is stopped, and the next request through its prefix starts
	// it again by the path that already exists.
	//
	// The stop function is bound HERE and deferred, not called through a
	// deferred call of Reap itself — that form evaluates at defer-run time
	// and would start the sweep during shutdown, which is the mistake serveWake
	// records one file over.
	//
	// The second argument is an LRU CEILING on resident subsystems, and it is what
	// keeps this pod inside the memory it was PROMISED rather than the memory it is
	// permitted. Those are different numbers and only one of them decides whether
	// the pod survives a busy node.
	//
	// Measured in production, 2026-08-15: 125 processes, 11,115 MB — the whole
	// 123-app manifest resident at once, against requests 6Gi / limits 11Gi. The
	// kubelet's own words when it killed it: "was using 11406568Ki, request is
	// 6Gi, has larger consumption of memory." Under node pressure a pod is
	// reclaimed on how far it has run past its REQUEST, so the 11Gi limit was never
	// the line that mattered — and at replicas:1 with Recreate, each reclaim is a
	// full API outage. There were three.
	//
	// 48 x ~89 MB (the measured mean) + the host is ~4.7 GB, comfortably under the
	// 6Gi request, which takes this pod out of the first rank of eviction
	// candidates. Hot subsystems stay warm because the bound is least-recently-
	// used; a cold one pays a start on its next request, single-flighted through
	// the same path a first request already takes.
	//
	// The idle bound still runs first and still does most of the work: this only
	// decides what happens when more than 48 are genuinely in use. It is not read
	// from the cgroup, because the cgroup carries the LIMIT and the number worth
	// sizing against is the REQUEST, which a process cannot see.
	// The ceiling itself is in endpointConfig, enforced at every start; this only puts
	// the AGE bound on a ticker. One reaper: there were two on this app, both a
	// minute apart with the same number written twice, and a second sweep buys
	// nothing a single one does not already do.
	stopReaping := app.Reap(time.Minute)
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
	// its own liveness probe and restarted into the same deadlock.
	//
	// HERE, before the mount loops, because the very next
	// thing this function does is spawn children — and the whole point is that they
	// are born into a pod whose volume is already claimed. Hold stamps the
	// environment they inherit, so each one knows the answer instead of racing for
	// it. Released by the defer after halt (below) has stopped every child: the
	// lock is withdrawn only once nothing in this pod can still write.
	//
	// Unset CLOUD_WRITER_LEASE ⇒ Hold does nothing at all, which is correct under
	// strategy: Recreate and is what production runs today.
	releaseLease, err := writerlease.Hold(env.DataDir(), writerlease.DefaultWait, app.Logger().Info)
	if err != nil {
		return err
	}
	defer func() { _ = releaseLease() }()

	// THE POD'S PLACE IN THE DEPLOYMENT, taken here for the lease's reason: the
	// live set says "this pod is alive at this address", which is the root's to
	// say, before any child starts and until the last one has stopped
	// (joinDeployment).
	co, peers, err := joinDeployment(app.Logger())
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		co.Close(ctx)
	}()

	// hostCtx bounds every background loop owned by this host (boot, console
	// priming, coordinator watching, and signal waiting). halt cancels it first
	// so teardown never races with new child starts or leaks background tickers.
	hostCtx, stopHost := context.WithCancel(context.Background())
	defer stopHost()

	// halt stops the host and every child, once, and every caller waits for it.
	// Deferred after the lease so it runs before the lease is released, and on
	// every return: a boot that fails below has already started the Eager
	// children. Listen returns as soon as Shutdown has closed the listeners,
	// which is BEFORE its hooks have stopped the children, so a host that
	// returned there exited under them — and on macOS, which has no Pdeathsig,
	// every child outlived it holding its ports.
	halt := haltOnce(app.Logger(), func(string, error) {
		stopHost()
		// Flip readiness BEFORE tearing anything down, so /readyz tells the
		// truth for whatever window remains. The host cannot call
		// readiness.SetDraining(): that flag belongs to the root package, which
		// this binary deliberately does not link (it is zip + manifest + the
		// webui leaf and nothing else), and it is a different PROCESS's state
		// anyway. Same contract, owned by the process K8s actually signals.
		draining.Store(true)
		// Out of the live set first, so peers stop electing this pod for orgs
		// and loops at their next read; the loops its children run stay
		// fenced by their leases until their passes end.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		co.Leave(ctx)
		cancel()
		shutdown(app, shutdownGrace)
	})
	returnReason := "run_return"
	defer func() { halt(returnReason, runErr) }()
	go watchDeployment(hostCtx, co, app.Logger(), halt)

	// composed is what THIS deployment put together, in manifest order — the app
	// set spec() describes. Taken here rather than from the loops below because
	// they consume `on` as they go (delete, so a leftover name is a typo), and
	// because the broker-first split would put the surface's document in an order
	// that is not the surface's.
	//
	// Coresident apps included: an app that mounts as middleware on a sibling's
	// router still SERVES its routes, so it belongs in the document even though
	// the host claims no prefix for it.
	composed := make([]string, 0, len(manifest.Apps))
	for _, a := range manifest.Apps {
		composed = append(composed, a.Name)
	}

	// THE MANIFEST IS THE APP SET. The host mounts what it was built with; there
	// is no second list to disagree with it.

	// WHO IS CALLING, before any child is included.
	//
	// Every subsystem runs as its own process, reached over a unix socket, so the
	// request a child handles has the SOCKET as its peer — measured empty, and the
	// same for every caller either way. Anything a child derives from it is one
	// value for the whole internet, which is how ai's per-visitor ceiling became one
	// bucket for everyone. Only this host can see the connection, so it answers here
	// and the children read the answer.
	//
	// BEFORE the mount loop, and that is the whole of it: zip visits an included App
	// with the middleware stack as it stood at the inclusion site, so a Use written
	// after these mounts would reach none of them.
	//
	// sdk/geo, not cloud: this host links no subsystem code (host-is-light), and
	// reaching through the root package for one function pulls eight of them in.
	app.Use(zip.H(geo.Stamp))

	// WHAT IS IN FLIGHT, counted ahead of every mount, so shutdown waits for the
	// handlers that are running and not for the streams clients hold open.
	app.Use(zip.H(inflight.Count))

	// THE CANONICAL SPELLING, before anything reads the path.
	//
	// A capability has one name, and English gives its name two spellings. The
	// surface publishes one of them and accepts both: the plural of a singular
	// capability, and the singular of a plural one, are rewritten HERE to the name
	// manifest.Apps carries (manifest.Normalize, HIP-0139 §2.2).
	//
	// It is a rewrite and not a route. Registering the second spelling would put
	// two addresses in the table for one thing, and every table it flows into
	// after — the index, the composed document, each generated SDK, the tool list —
	// would carry both and a reader would have to be told which is real. One
	// string comparison at the edge instead, and everything downstream sees the
	// one name it already knows.
	//
	// AT THE EDGE, ahead of the mounts, for the same reason sdk/geo is: zip
	// visits an included App with the stack as it stood at inclusion, and a child
	// must never be handed a spelling it does not serve. Fiber recomputes its
	// route bucket on the override, so the scan that follows matches the new path.
	app.Use(zip.H(func(c *zip.Ctx) error {
		if p := c.Path(); p != "" {
			if canonical := manifest.Normalize(p); canonical != p {
				c.Fiber().Path(canonical)
				p = canonical
			}
			if dst := manifest.ProviderWebhookPath(c.Method(), p); dst != p {
				c.Fiber().Path(dst)
			}
		}
		return c.Next()
	}))

	// A PORT IN A SANDBOX IS AN ORIGIN OF ITS OWN (apps/sandbox/preview): a request
	// for a preview host is rewritten onto the sandbox app's route, here, ahead of
	// the mounts for the reason the spelling above is.
	app.Use(preview.Middleware())

	// THE API'S OWN INDEX, and the links every /v1 answer carries — here, for the
	// same one reason sdk/geo is: middleware reaches only what is composed after
	// it, and both halves are about requests the subsystems below would otherwise
	// answer. It costs the boot nothing; the index is composed on the first request
	// that needs one (openapi/index.go). The agent's door answers from the same
	// reading, further down.
	ix := index(app, composed)

	// WHAT STARTS WITH THE HOST depends on the deployment, on purpose: the turn
	// path is pinned where the reservation is production's and started on first
	// use below it (manifest.TurnPinned). Said once, here, so a first turn that
	// waits on a cold start on a laptop reads as the trade it is.
	//
	// The loop REGISTERS and starts nothing (mount). The pinned children start in
	// boot, once the port is bound, so the listener never waits on a child.
	app.Logger().Info("turn path", "pinned", manifest.TurnPinned(), "ceiling", manifest.Warm())
	for _, a := range manifest.Apps {
		if err := mount(app, a, a.Pinned()); err != nil {
			return err
		}
	}

	// Liveness belongs to the HOST, not to any app: it must answer while every
	// plugin is still cold, or a lazy surface fails its readiness probe before the
	// first real request ever arrives and gets restarted forever. Registered after
	// the mount loops so it closes over the finished absence set — nothing is
	// listening until app.Listen either way, so registration order costs no
	// availability, only route specificity, and /healthz collides with no prefix.
	health(app)

	// The surface's own description, at /v1/openapi.json. Same reasoning as
	// /healthz, and the same layer: it is the HOST's, because it is about the
	// whole surface and no plugin can see past itself.
	serveIndex(app, composed)

	// The same description as a QUERY LANGUAGE, at /v1/graphql. The host's for the
	// same reason, and by claiming the address rather than leaving it to the app
	// that holds the /v1 remainder — which answered it with a schema of its own
	// registry, one field wide, about the wrong thing.
	graphql(app, composed)

	// THE AGENT MCP SERVER, at POST /v1/mcp — composed by ASKING, at the moment of
	// asking. Registered after the mount loops so the plugin table it starts from
	// is the finished one, and before anything listens.
	//
	// It is the composed set minus the CORESIDENT apps: a coresident app is
	// middleware on a sibling's router (zen on ai's), so it is not a child this
	// host can start and its ops are already in the sibling's registry — asking
	// for it by name would report a permanent outage for an app that is serving.
	//
	// The MCP server is KEPT, because the surface's own subsystems need it as much
	// as an external client does — an agent run inside `agents` has to resolve
	// its tool names against the same aggregated surface. serveWake publishes
	// this same object on the host's internal socket, so there is one gather, one
	// routing table and one curation rule for both directions.
	mcp := client.Use(app, manifest.MCPPath, routed(composed), locate(app))

	// LISTING WHAT THE FLEET SERVES MUST NOT START THE FLEET. Discovery asks a
	// subsystem, and asking a lazy one starts it — so one tools/list started every
	// subsystem this host composes, and the pod's resident cost became the size of
	// the catalog rather than of the work. The MCP server asks the ones that are
	// already running and reads the rest from what they published
	// (client/catalog.go); this is the half only the host can answer, because the
	// plugin table is its.

	// The bare /mcp needs no route here. webui's terminal handler answers it from
	// manifest.MCPPath (webui/mcp.go) — one rule, in the one place that can tell
	// an MCP request from a client-side console route. A route registered here
	// would be a SECOND implementation of that redirect, and one that is not
	// self-scoping: app.All("/mcp") claims the path unconditionally, so the same
	// call in cloud.Serve would hijack a plugin's OWN zip MCP server at that
	// address.
	//
	// The console at "/" is the HOST's, because the host is the entry point: every
	// SPA route (/, /signin, /dashboard, …) is under no app prefix, so it reaches
	// the host's catch-all rather than a plugin. Registered LAST — after every app
	// prefix — so a real /v1 route always wins and only unmatched paths fall
	// through to the white-labelled shell. webui is a light leaf (stdlib + the
	// brand registry), so owning "/" costs the host a handler, not the surface's
	// package graph.
	// The published-site edge goes BEFORE the console: <slug>.hanzo.app must serve
	// the customer's site, and webui owns "/" for every path no app prefix claims,
	// so mounting it after would let the console answer first — which is exactly
	// the defect. See sites.go. It also installs the resolver the console reads its
	// own release through, so this order is load-bearing twice over.
	// THE OBJECT STORE'S OWN LINKS. A store on local disk signs the URLs a browser
	// reads and writes, and this port is where the browser arrives: any process on the
	// volume may have signed one, and this is the one that answers. A deployment on a
	// registered object store signs links to that store and nothing here matches.
	app.Use(object.Middleware())

	mountSites(app)

	// THE AGENT'S DOOR — the API host's home page, robots.txt, sitemap, auth.md,
	// API catalog and MCP card, /llms.txt and /skill.md on every host a brand
	// claims, and the resource metadata the MCP server's challenge names: after
	// the site edge, so a customer's site answers its own, and before the console,
	// whose catch-all would answer each with its shell. See agent.go.
	useAgent(app, ix)

	// The console's BYTES are a published site release, not an embed, read through
	// `project`, which owns the release pointer. Nothing here waits on it: the
	// Source mounts empty and the first read mounts the release (webui answers 503
	// until one is in), and boot primes it once project runs. A console that cannot
	// be read is loud in the log and never takes the API down with it.
	consoleSrc := release.New(release.ConfigFromEnv(), app.Logger())
	if err := webui.Use(app, release.FS(consoleSrc), servesConsole); err != nil {
		return fmt.Errorf("console: %w", err)
	}

	// The start endpoint, AFTER the mount loops so the plugin table it starts
	// from is the finished one, and before anything listens so no child can ask
	// before it is there. Without it every internal call to a lazy app dials a
	// socket that no request has ever caused to exist (wake.go). It closes with
	// the app, so there is nothing here to defer and nothing to forget to.
	serveWake(app, mcp)

	// The wire ports, for the same reason and in the same place: after the mount
	// loops, because a connection starts an app from the finished plugin table, and
	// before anything listens, so a client that dials early waits for its app
	// rather than finding the port closed (wire.go).
	serveWire(app)

	// SIGTERM must reach the children. zip drains its shutdown hooks LIFO, and
	// every Load registered one that stops its process, so this is what keeps a
	// rollout from leaving orphans behind. (On Linux each child also carries
	// Pdeathsig, which covers a host that dies without getting here.)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	go func() {
		select {
		case sig := <-stop:
			halt("signal:"+sig.String(), nil)
		case <-hostCtx.Done():
		}
	}()

	// THE PORT FIRST, THEN THE CHILDREN. Listen binds below; boot waits for the
	// bind and then starts the pinned children, the vital ones first and all at
	// once, then the rest all at once. /readyz turns 200 when the vital ones run
	// (health). A boot that cannot go on — a Required app would not start — stops
	// the host and run returns why.
	booted := make(chan error, 1)
	go func() {
		err := boot(hostCtx, app, addr, manifest.Apps, consoleSrc)
		booted <- err
		if err != nil && hostCtx.Err() == nil {
			app.Logger().Error("boot failed; stopping the host", "err", err)
			halt("boot_failure", err)
		}
	}()

	// Both transports, same router — the pair cloud.Listen listens on. A bare
	// address is ZAP (zip's default scheme); HTTP has to be spelled out, and
	// omitting it is why a curl against the host answers with a frame-size error
	// instead of JSON.
	// The peer listener rides beside them when this pod has peers to reach it.
	if err := app.Listen(append([]string{zapAddr, "http://" + addr}, peers...)...); err != nil {
		returnReason = "listener_error"
		return err
	}
	returnReason = "listener_return"
	select {
	case err := <-booted:
		if hostCtx.Err() != nil && errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	default:
		return nil
	}
}

// haltOnce returns an idempotent shutdown trigger that logs the FIRST
// initiating reason and error before executing stop. Every path that can stop
// the host — OS signals, fatal boot failures, deployment coordinator fencing,
// or listener exit — calls it with an explicit reason so teardown is never
// anonymous in logs.
func haltOnce(log luxlog.Logger, stop func(reason string, cause error)) func(reason string, cause error) {
	var once sync.Once
	return func(reason string, cause error) {
		once.Do(func() {
			if reason == "" {
				reason = "unspecified"
			}
			if cause != nil {
				log.Error("host shutdown initiated", "reason", reason, "err", cause, "pid", os.Getpid())
			} else {
				log.Info("host shutdown initiated", "reason", reason, "pid", os.Getpid())
			}
			stop(reason, cause)
		})
	}
}

// boot starts the pinned children once the host is listening: the vital ones
// first, all at once, then the rest, all at once. A child a request reaches
// before boot gets to it starts on that request instead, single-flighted with
// boot's own start, so nothing waits on order.
//
// A start that fails leaves the app absent — zip keeps the reason, census
// reports it, and a vital one makes /readyz unfit — unless the app is Required,
// which fails the boot. A `project` failure degrades the console to 503 and
// never takes the API host down with it.
func boot(ctx context.Context, app *zip.App, addr string, apps []manifest.App, console *release.Source) error {
	if err := waitBound(ctx, addr); err != nil {
		return err
	}
	var vital, rest []manifest.App
	for _, a := range apps {
		switch {
		case a.Coresident || !a.Pinned():
		case a.Vital:
			vital = append(vital, a)
		default:
			rest = append(rest, a)
		}
	}
	start := time.Now()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := startAll(app, vital); err != nil {
		return err
	}
	app.Logger().Info("vital children started", "apps", len(vital), "took", time.Since(start).Round(time.Millisecond))
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := startAll(app, rest); err != nil {
		return err
	}
	app.Logger().Info("pinned children started", "apps", len(vital)+len(rest), "took", time.Since(start).Round(time.Millisecond))
	if console == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := app.Start(projectApp); err != nil {
		// Degrade the console non-fatally: webui answers 503 until project comes
		// up on demand, while the API and every healthy sibling keep serving.
		app.Logger().Error("console: project would not start — serving the API without it",
			"app", projectApp, "err", err)
		return nil
	}
	if err := console.Prime(ctx); err != nil && ctx.Err() == nil {
		app.Logger().Error("console: no release in use — serving the API without it", "err", err)
	}
	return nil
}

// startAll starts apps in parallel and waits for every one.
func startAll(app *zip.App, apps []manifest.App) error {
	errs := make([]error, len(apps))
	var wg sync.WaitGroup
	for i, a := range apps {
		wg.Go(func() {
			if _, err := app.Start(a.Name); err != nil {
				errs[i] = err
				app.Logger().Error("subsystem ABSENT: it would not start, and the host is serving without it",
					"name", a.Name, "prefixes", a.Prefixes, "err", err)
			}
		})
	}
	wg.Wait()
	for i, a := range apps {
		if errs[i] != nil && a.Required {
			return fmt.Errorf("%s is required here and would not start: %w", a.Name, errs[i])
		}
	}
	return nil
}

// waitBound returns once the host's HTTP listener accepts: a path is a unix
// socket, anything else a TCP address, dialled on loopback when it names none.
func waitBound(ctx context.Context, addr string) error {
	network, target := "unix", addr
	if !strings.HasPrefix(addr, "/") {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("listen address %q: %w", addr, err)
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		network, target = "tcp", net.JoinHostPort(host, port)
	}
	for {
		if c, err := net.DialTimeout(network, target, time.Second); err == nil {
			_ = c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// shutdownGrace bounds how long shutdown waits for the requests in flight. The
// listeners close at once, the drain ends when the last handler returns, and a
// stream a client holds open is cut rather than waited on: under Recreate every
// second of teardown is a second no pod serves.
const shutdownGrace = 5 * time.Second

// shutdown stops app and every child it started. The drain ends when no handler
// is running, or at grace; then every child is told to stop at once — SIGTERM, so
// each drains, closes its stores with a checkpoint and ships them — and waited
// for: zip kills one only once its 20s are spent. grace plus that stays inside
// the pod's 45s termination grace.
func shutdown(app *zip.App, grace time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	drained, done := inflight.Drain(ctx)
	defer done()
	start := time.Now()
	if err := app.ShutdownWithContext(drained); err != nil && !errors.Is(err, context.Canceled) {
		app.Logger().Warn("shutdown", "err", err)
	}
	app.Logger().Info("shutdown: host and children stopped", "took", time.Since(start).Round(time.Millisecond))
}

// mount composes one app onto the host and starts nothing: every child is
// Loaded lazily, so the mount loop costs a route entry per app and the port binds
// before any child exists. A pinned app is started by boot once the host listens,
// and is never stopped — IdleAfter 0, which zip reads as not evictable, idle or
// for room.
//
// A lazy Load spawns no process, so its only failures are configuration this
// deployment got wrong — no binary resolves for the app, an unusable address, a
// route conflict — and those abort the host by name. A child that will not START
// is boot's to report: its mount stands and answers 503 ("no instance running"),
// zip keeps the reason, and the next request to the prefix retries the start,
// which heals the common cause, a dependency that was not up yet.
//
// Keeping the mount is not cosmetic. An unregistered prefix falls through to the
// console at "/", and webui only refuses the namespaces in its apiPrefixes list —
// so an absent /v1 app 404s (honest, but indistinguishable from "this deployment
// does not run that subsystem") while the seven prefixes outside that list answer
// 200 text/html. Among them is iam's /login/oauth: an OAuth client would receive
// the console shell instead of a redirect.
func mount(app *zip.App, a manifest.App, pinned bool) error {
	// A co-resident app routes no prefix of its own: it is middleware on another
	// app's router and decides per request whether to serve or Next. There is
	// nothing for the host to claim or spawn, so there is nothing to mount.
	if a.Coresident {
		return nil
	}
	p := a.Plugin()
	p.Lazy = true
	if pinned {
		p.IdleAfter = 0
	}
	// NO BUILD-TIME TOOL CATALOGUE. zip.Plugin.Tools took the array this app's
	// binary projected when it was BUILT (plugin/<app>/mcp.json) so the host could
	// answer tools/list without running anything. That artifact was a second
	// source for a fact the child already knows, and it was wrong: o11y's held 12
	// tools while the o11y binary at the same commit served 365. The MCP server
	// asks the child now (client.Use), so there is nothing to hand over here.
	p.Start = startTimeout()

	// zip v1.23 removed (*App).Add: Use is the ONE composition verb, and zip.Load
	// already returns the leaf *App — which IS a zip.Component — so the child is
	// included by reference rather than through a second registration call.
	leaf, err := zip.Load(p, a.Prefixes...)
	if err != nil {
		if p.Addr != "" {
			return fmt.Errorf("%s in use at %s: %w", a.Name, p.Addr, err)
		}
		return fmt.Errorf("%s: %w", a.Name, err)
	}
	app.Use(leaf)
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

// locate is how the host reaches ONE app: the child this host started, or the
// instance an operator pointed CLOUD_<NAME>_ADDR at.
//
// Both are needed because they are reached differently and only the composition
// root knows which is which. zip.App.Start covers a spawned child and is the
// right call for it — idempotent, and the same single-flighted path a request to
// the app's prefix takes, so a burst of askers still produces one process. A
// remotely mounted app is never started, so Start has nothing to report about it
// and would name it unavailable forever.

func locate(app *zip.App) client.At {
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

// inside is how an endpoint reached from INSIDE the surface reaches one app: the
// app's own plane socket, where its agent MCP server answers with no edge in
// front of it (cloud.UseMCP). Same start, different address.
//
// A REMOTELY mounted app (CLOUD_<NAME>_ADDR) keeps the edge address, because its
// plane socket is on its own host and no path here reaches it. So an internal
// caller's identity survives into every app this host RUNS, and into a remote one
// only as far as that app's own boundary lets it — which is the honest answer,
// and the same one it has always given.
func inside(app *zip.App) client.At {
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
// liveness for a broken plugin recreates that failure one layer up: K8s
// would kill a pod that is serving every other subsystem correctly, and the
// replacement would fail identically because the cause is in the image or the
// config, not in the process. `absent` rides in the BODY with its reason —
// "staged" vs "failed" (degraded.go) is unanswerable without it.
//
// /readyz — READINESS. 503 when the host is draining, while a VITAL subsystem
// (manifest.App.Vital) is still starting, or when one is absent. The port binds
// before any child starts, so readiness is what holds traffic off until the
// product API can answer it — and only the vital children hold it: the rest
// start beside the first requests, and a request that reaches one first starts
// it. Absence matters because it is what let a total failure read as healthy:
// `ai` — the greedy /v1 catch-all, i.e. the entire product API — degraded to
// absent, the reason sat in the `absent` FIELD, and a probe reading the STATUS
// CODE saw 200. The pod stayed Ready with 0 restarts while /v1/models 503d.
//
// A 503 here is not an outage, it is the outage becoming VISIBLE, and the timing
// is what makes it cheap: a rollout whose new image cannot start `ai` never gets
// a Ready pod, so the Deployment stalls and the OLD pods keep serving. The bad
// config stops at the first replica instead of reaching all of them. When it is
// already surface-wide the endpoints empty and the product is down — but it was
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
func health(app *zip.App) {
	alive := func(context.Context, *probeIn) (*probeOut, error) {
		absent, _ := census(app)
		return &probeOut{Status: "ok", Absent: absent}, nil
	}
	app.Get("/healthz", alive, zip.WithStatus(200),
		zip.WithSummary("Report whether this host process is alive"))

	// /health IS a liveness probe, and until this line it was a console page.
	// The name is the one every monitor reaches for first, and nothing claimed
	// it here, so the SPA catch-all did: api.hanzo.ai/health served 241 KB of
	// console index.html. That is a wrong answer in both directions — 200 while
	// the API is unreachable, and 503 when only a static bundle is, which is a
	// sentence about a asset that reads as a sentence about the product.
	//
	// One handler at two addresses, not two handlers: a second body is how two
	// spellings of one question come to give different answers.
	app.Get("/health", alive, zip.WithStatus(200),
		zip.WithSummary("Report whether this host process is alive"))

	app.Get("/readyz", func(context.Context, *probeIn) (*probeOut, error) {
		// Draining first: a pod on its way out is not ready regardless of what
		// it is still able to serve. drain.go describes this contract for the
		// root package's ops listener; the HOST is a different process with its
		// own lifecycle, and it is the one K8s signals and probes.
		if draining.Load() {
			return &probeOut{Status: "draining"}, nil
		}
		a, starting := census(app)
		if u := unfit(a); len(u) > 0 {
			return &probeOut{Status: "unfit", Absent: u}, nil
		}
		if len(starting) > 0 {
			return &probeOut{Status: "starting", Starting: starting}, nil
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
	// Starting names the vital subsystems this host has not finished starting.
	// Only /readyz reports it, and only with status "starting".
	Starting []string `json:"starting,omitempty"`
	// Status is "ok", "starting" (a vital subsystem is not running yet),
	// "draining" (this pod is shutting down) or "unfit" (a vital subsystem is
	// absent).
	Status string `json:"status"`
}

// StatusCode makes the answer carry its own status: starting, draining and
// unfit are 503, everything else 200. The liveness op declares 200 alone, so a
// healthz answer can never smuggle a 503 and flap the kubelet into a restart loop.
func (p *probeOut) StatusCode() int {
	switch p.Status {
	case "starting", "draining", "unfit":
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

// serveIndex is the surface's published document, and the HOST is the only process that
// can answer for it.
//
// WITHOUT this registration the path is not unclaimed — it is claimed by the
// wrong thing, silently. /v1/openapi.json matches no host route, falls to the
// only prefix that covers it (ai's "/v1", manifest/apps.go), and is proxied to
// the ai child, which answers with openapi.Use reading ITS OWN router. That
// router's entire AI surface is one greedy All("/v1/*") (apps/ai/ai.go), so
// api.hanzo.ai/v1/openapi.json served a 3.7 KB document of EIGHT paths — the
// child's own health, iam edge, zap, console catch-all and wildcard — while the
// surface serves 1039. Every SDK generator, every serveIndex-derived CLI and every third
// party reading the published serveIndex read that instead. 200 OK the whole time.
//
// What it answers with is the CUSTOMER contract (openapi.UseFleet): the
// endpoint takes no credential, and the same readers that meet it here are the
// ones the audience rule is written for.
//
// Two properties make the fix the honest one rather than merely a fix:
//
//   - It costs no subsystem. The document is composed from the subsets the plugins
//     projected when they were BUILT (plugin.Spec — bytes in this binary), so
//     answering it starts nothing. A host that had to mount 113 subsystems to
//     describe them would have given back exactly what laziness buys.
//   - It is not a second source of truth. openapi.Surface and openapi.Publish are
//     the same composition and projection that WRITE openapi.yaml, over the same
//     committed files, so the served bytes and the committed artifact are one
//     document by construction — and
//     mk/fleet.mk check regenerates those files from source and fails on
//     any diff, so a drifted serveIndex goes red in CI instead of shipping.
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
func serveIndex(app *zip.App, composed []string) {
	openapi.UseFleet(app, subsets(composed))
}

// index is the surface's HYPERMEDIA layer: GET /v1 lists what this deployment
// answers, GET /v1/<capability> lists one capability's operations, and every /v1
// answer carries the RFC 8288 links back to both plus the document.
//
// The host's, for the reason [spec] is the host's: the answer is about the whole
// surface and no plugin can see past itself. It reads the SAME subsets the document
// is composed from, so the two endpoints describe one API by construction.
//
// It is composed BEFORE the mount loops — see the call site — because the
// addresses it answers are inside subtrees the mounts claim. That is also why it
// is middleware rather than routes: ai's row is the "/v1" REMAINDER and zip mounts
// a prefix as All(prefix) too, so a host route at /v1 is two definitions claiming
// one address and the composition is refused outright.
func index(app *zip.App, composed []string) *openapi.Reading {
	r := openapi.Read(subsets(composed))
	openapi.UseIndex(app, r)
	return r
}

// graphql is the surface's QUERY LANGUAGE, at /v1/graphql: GET renders the schema
// this deployment publishes, POST runs a request against it, one hop per root
// field to the app that owns the operation.
//
// It reads the SAME subsets the document and the index do, so all three describe
// one API by construction — and it dispatches through locate, the resolver the
// agent MCP server already uses, so a field reaches its app the way every other
// surface question does.
//
// /v1/graphql, never /v1/graph: the second is the knowledge graph's own address,
// assertions and neighbours, and the two mean different things by the word.
func graphql(app *zip.App, composed []string) {
	client.UseGraph(app, openapi.GraphPath, subsets(composed), locate(app))
}

// subsets is what this deployment publishes: each app's own document, read from
// the bytes its binary projected when it was built.
//
// Stated once because two endpoints read it — the surface document and the
// index — and they are mounted at different points in run(), so the reading
// would otherwise be written twice and be free to disagree about which apps this
// deployment runs.
func subsets(composed []string) func() ([]openapi.Part, error) {
	return func() ([]openapi.Part, error) {
		return openapi.Subsets(composed, plugin.Spec, manifest.StageOf)
	}
}

// census reads zip's plugin table once and answers two questions about it:
// which subsystems failed to start, with the reason, and which VITAL ones this
// host has not finished starting.
//
// Absence is read off the failure, never off Running: almost every app is lazy
// and cold, so Running=false is the NORMAL state for ~110 of 112, and deriving
// absence from it would report the whole surface as broken. zip.Status.Error is
// the fact — it is empty for a plugin nobody has asked for yet, set by a start
// that failed (boot's, or the first request's long after boot), and cleared when
// an instance comes up. Nine api.hanzo.ai surfaces once sat 503 while /healthz
// answered {"status":"ok"}, for want of exactly this.
//
// A vital app is starting while it is neither running nor failed. A remote one
// (CLOUD_<NAME>_ADDR) has no process here to wait for.
func census(app *zip.App) (absent map[string]string, starting []string) {
	for _, s := range app.Plugins() {
		switch {
		case s.Running || s.Source == "remote":
		case s.Error != "":
			if absent == nil {
				absent = map[string]string{}
			}
			absent[s.Name] = s.Error
		case vital(s.Name):
			starting = append(starting, s.Name)
		}
	}
	return absent, starting
}

// vital reports whether the manifest marks name Vital.
func vital(name string) bool {
	a, ok := manifest.Named(name)
	return ok && a.Vital
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

// servesConsole answers which request Hosts this entry point serves the console
// for. Every other name that reaches the terminal catch-all gets a 404.
//
// It is deliberately NARROWER than apps/sites.IsSelfHost. That predicate answers
// "is this one of our registrable domains", which is true of oci.hanzo.ai,
// pkg.hanzo.ai and ci.hanzo.ai — the three hosts that were serving a byte-
// identical copy of the console to every caller, so `helm pull` parsed an HTML
// page as a manifest and every status-code check reported them healthy. Using it
// here would have kept exactly the defect this closes.
//
// A console is served at the console name, at the cloud name (which has always
// answered there and is what the operator types), and on a loopback or .local
// name so a laptop needs no configuration. Bound site hosts never arrive here —
// the sites edge serves them first.
func servesConsole(host string) bool {
	if host == "" {
		return false
	}
	if host == "localhost" || host == "127.0.0.1" || host == "[::1]" ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") {
		return true
	}
	label, _, ok := strings.Cut(host, ".")
	return ok && (label == "console" || label == "cloud" || label == "platform" || label == "platform2" || label == "console2")
}
