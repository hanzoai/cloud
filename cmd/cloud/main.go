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
// cloud.Serve middleware it would serve standalone. Identity, billing and
// telemetry run in the app's own process, where they already ran.
//
// The host is the FRONT DOOR, so it owns three things no plugin can: it serves
// the white-labelled console at "/" (webui, mounted last so every app prefix
// wins); it threads the deployment's operator flags to the children as CLOUD_*
// env (run→forward); and it SCOPES CREDENTIALS — it scrubs the KMS root key from
// its own environment so no child inherits it, and hands it to the kms broker
// child alone (run→childEnv), the boundary credz was built for.
//
// Deployment is one directory: the host plus its plugins, which is what the image
// already ships. Point CLOUD_<NAME>_ADDR at an instance running elsewhere, or
// CLOUD_<NAME>_BIN at a specific build, to override one of them.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hanzoai/cloud/credz/launch"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plugin"
	"github.com/hanzoai/cloud/webui"
	"github.com/zap-proto/zip"
)

func main() {
	listen := flag.String("listen", getenv("CLOUD_LISTEN", ":8080"), "HTTP listen address")
	zapAddr := flag.String("zap", getenv("CLOUD_ZAP_LISTEN", ":9653"), "ZAP-RPC listen address")
	enable := flag.String("enable", os.Getenv("CLOUD_ENABLE"), "comma-separated subsystems to mount; empty mounts all")

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

	if err := run(*listen, *zapAddr, *enable); err != nil {
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

func run(addr, zapAddr, enable string) error {
	// THE FLEET'S ONE AGENT DOOR, at POST /v1/mcp. zip serves it: initialize, ping,
	// tools/list and tools/call are its handleMCP, and the tool list is the union
	// of every mounted plugin's build-time catalogue (mount below), rendered once
	// as bytes. So tools/list — the method an MCP client calls constantly — is a
	// memcpy and starts NO child; only a tools/call wakes one, the single plugin
	// that owns the named tool, over ZAP on its private socket.
	//
	// The host is the only process that can own it. MCPTools() is in-process, so a
	// plugin cannot enumerate a lazy sibling, and a plugin-hosted door would cost
	// its own wake on the very first list.
	app := zip.New(zip.Config{AppName: "cloud", MCP: zip.MCPConfig{Path: "/v1/mcp"}})

	// Mint this host's child-signing secret and take the KMS root key OUT of the
	// host's own environment — both BEFORE the first Load spawns an eager child.
	secret, rootKey := stampAndScrub()

	on := enabled(enable)
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
	// It is the enable list applied, coresident apps included: an app that mounts
	// as middleware on a sibling's router still SERVES its routes, so it belongs in
	// the document even though the host claims no prefix for it.
	composed := make([]string, 0, len(manifest.Apps))
	for _, a := range manifest.Apps {
		if on == nil || on[a.Name] {
			composed = append(composed, a.Name)
		}
	}

	// The broker is a precondition, not a selection. Every child this host spawns
	// carries a CREDZ_TOKEN, and credz refuses to fall back to a dev key once a
	// token is present — so a child that cannot reach the broker resolves Unkeyed
	// and fails closed at its FIRST store open, in every build. An allowlist that
	// omits it therefore does not produce a smaller deployment; it produces one
	// where no child can open a store, and the symptom is every eager child exiting
	// 1 with "CLOUD_KMS_MASTER_KEY_REF is required" while the loop below silently
	// mounts no broker at all. Refused for the same reason a name the manifest does
	// not list is refused: the alternative is a deployment that cannot work and does
	// not say so.
	if on != nil && !on[launch.Broker] {
		return fmt.Errorf("--enable omits %q, the credential broker every other child pulls its data-plane key from; add it or drop --enable", launch.Broker)
	}

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
		if a.Name != launch.Broker || (on != nil && !on[a.Name]) {
			continue
		}
		if err := mount(app, a, true, secret, rootKey, absent); err != nil {
			return err
		}
		delete(on, a.Name)
	}

	for _, a := range manifest.Apps {
		if a.Name == launch.Broker || (on != nil && !on[a.Name]) {
			continue
		}
		if err := mount(app, a, a.Eager, secret, rootKey, absent); err != nil {
			return err
		}
		delete(on, a.Name)
	}
	// A name that matched nothing is a typo, and the symptom of tolerating one is
	// a subsystem that is simply absent from a deployment with no error anywhere.
	if len(on) > 0 {
		return fmt.Errorf("--enable names %v, which the manifest does not list — run `make generate` if the app is new, else fix the name", keys(on))
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

	// The console at "/" is the HOST's, because the host is the front door: every
	// SPA route (/, /signin, /dashboard, …) is under no app prefix, so it reaches
	// the host's catch-all rather than a plugin. Registered LAST — after every app
	// prefix — so a real /v1 route always wins and only unmatched paths fall
	// through to the white-labelled shell. The embed is the light webui leaf
	// (stdlib + the brand registry), so owning "/" costs the host the console
	// bytes, not the fleet's package graph.
	if err := webui.Mount(app); err != nil {
		return fmt.Errorf("console: %w", err)
	}

	// The start door, AFTER the mount loops so the plugin table it starts from is
	// the finished one, and before anything listens so no child can ask before it
	// is there. Without it every internal call to a lazy app dials a socket that
	// no request has ever caused to exist (wake.go).
	defer func() { _ = serveWake(app)() }()

	// SIGTERM must reach the children. zip drains its shutdown hooks LIFO, and
	// every Load registered one that stops its process, so this is what keeps a
	// rollout from leaving orphans behind. (On Linux each child also carries
	// Pdeathsig, which covers a host that dies without getting here.)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		_ = app.Shutdown()
	}()

	// Both transports, same router — the pair cloud.Serve listens on. A bare
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
func mount(app *zip.App, a manifest.App, eager bool, secret, rootKey string, absent map[string]string) error {
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
	// This app's MCP tools, from the artifact its own binary wrote when it was
	// built. Given them, zip serves this app's tools on the host's door and
	// forwards a tools/call to this app alone — without ever running it to ask.
	p.Tools = plugin.Tools(a.Name)
	// Per-plugin, on the plugin's OWN Env, which zip appends to that ONE child's
	// environment: a scoped token for every child, and — for the broker alone —
	// the launch secret and the root key. A token or key placed in the host's
	// os.Environ() would reach every child alike and prove nothing about any of
	// them (#51).
	p.Env = append(p.Env, childEnv(a.Name, secret, rootKey)...)
	p.Start = startTimeout()

	err := app.Add(zip.Load(p, a.Prefixes...))
	if err == nil {
		return nil
	}
	if a.Required {
		return fmt.Errorf("%s is required here and would not start: %w", a.Name, err)
	}
	// A remote mount (CLOUD_<NAME>_ADDR) starts no process — zip.Mount only builds
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
	if err := app.Add(zip.Load(p, a.Prefixes...)); err != nil {
		return fmt.Errorf("%s: mounting it absent failed too: %w", a.Name, err)
	}
	return nil
}

// health is the host's liveness route AND the one place a probe can read which
// subsystems are absent.
//
// The status code and "status" field are about THIS PROCESS and never move: the
// host is a router, it is up, and 112 cold plugins are not a reason to take it
// out of rotation. Failing liveness or readiness for an optional plugin would
// recreate the outage this fix exists to prevent, one layer up — K8s would
// restart a pod that is serving every other subsystem correctly.
//
// "absent" is about the FLEET, and it carries the REASON. That distinction is
// what "staged" vs "failed" needs (degraded.go): both answer 503 on the wire, so
// without the reason an operator cannot tell a subsystem this deployment never
// ran from one that died, and a release gate tolerates both.
func health(app *zip.App, absent map[string]string) {
	app.Get("/healthz", func(c *zip.Ctx) error {
		out := map[string]any{"status": "ok"}
		if a := stillAbsent(app, absent); len(a) > 0 {
			out["absent"] = a
		}
		return c.JSON(200, out)
	})
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
//     mk/fleet.mk surface-check regenerates those files from source and fails on
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

// stampAndScrub mints this launcher's child-signing secret and takes the KMS root
// key OUT of the host's OWN environment, returning it for re-injection into the
// broker child alone (childEnv). It is one function so the boot order is one
// fact: zip builds every child's environment as append(os.Environ(), Plugin.Env
// …), and Go keeps the FIRST occurrence of a duplicated key — so a root key left
// in the host's environment reaches EVERY child and CANNOT be scrubbed by a later
// Env entry. Unset here, it reaches only the child whose Plugin.Env carries it.
// The host needs the key for nothing of its own: it opens no store and decrypts
// nothing. Split out from run so a test can prove the host's environment no
// longer carries the key after it runs.
func stampAndScrub() (secret, rootKey string) {
	secret = launch.Secret()
	rootKey = os.Getenv(launch.RootEnv)
	_ = os.Unsetenv(launch.RootEnv)
	return secret, rootKey
}

// childEnv is the environment the host stamps onto ONE plugin child's
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

// zip.Plugin.Env: a scoped launch token (credz/launch.Env) for EVERY child,
// naming the app it was started as; and — for the broker child ALONE — the launch
// secret it verifies those tokens with and the KMS root key it needs to be Root
// and unseal the store. Every OTHER child therefore comes up with a per-app token
// and NO root key, and must ask the broker for its scoped bundle: the credz
// boundary, now the default entrypoint. rootKey is "" in a keyless dev run, and a
// broker handed no key stays unkeyed rather than being handed an empty one.
func childEnv(app, secret, rootKey string) []string {
	env := []string{launch.Env(secret, app)}
	if app == launch.Broker {
		env = append(env, launch.SecretEnv+"="+secret)
		if rootKey != "" {
			env = append(env, launch.RootEnv+"="+rootKey)
		}
	}
	return env
}

// enabled parses the subsystem allowlist. nil means every app, which is the
// default and the shape a full deployment runs.
func enabled(list string) map[string]bool {
	list = strings.TrimSpace(list)
	if list == "" {
		return nil
	}
	on := map[string]bool{}
	for _, n := range strings.Split(list, ",") {
		if n = strings.TrimSpace(n); n != "" {
			on[n] = true
		}
	}
	return on
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
