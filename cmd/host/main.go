// Command host is the Hanzo Cloud router: one binary that serves the whole API
// by mounting every subsystem as its own process, started on the first request
// that reaches it.
//
// It knows only where each app lives and what path it answers — never what the
// app does — so it links zip and the generated manifest and nothing else. That
// is the entire difference from cmd/cloud, which imports apps and therefore
// links all 103 subsystem graphs into one 3105-package binary that must be
// relinked whenever any of them changes. Here a subsystem changing rebuilds
// itself; the host is untouched.
//
// Lazy is what makes 69 services affordable. Mounting them eagerly costs 69
// processes, 69 resident sets and 69 startup times at boot for a set that is
// mostly idle. Mounted lazily, an app nobody calls costs a route entry and a
// struct; the cost moves to the first request that needs it. The subsystems that
// cannot wait for a request — the ones that own a listener or a background
// loop — say so in apps.go's `eager` map and start with the host.
//
// The apps themselves are unchanged and unaware: each is the same cmd/<name>
// binary that already exists, serving the same routes through the same
// cloud.Serve middleware it would serve standalone. Identity, billing and
// telemetry run in the app's own process, where they already ran.
//
// Deployment is one directory: the host plus its plugins, which is what the
// image already ships. Point CLOUD_<NAME>_ADDR at an instance running elsewhere,
// or CLOUD_<NAME>_BIN at a specific build, to override one of them.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/hanzoai/cloud/credz/launch"
	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

func main() {
	addr := flag.String("addr", getenv("CLOUD_LISTEN", ":8080"), "HTTP listen address")
	zap := flag.String("zap", getenv("CLOUD_ZAP_LISTEN", ":9653"), "ZAP-RPC listen address")
	enable := flag.String("enable", os.Getenv("CLOUD_ENABLE"), "comma-separated subsystems to mount; empty mounts all")
	flag.Parse()

	if err := run(*addr, *zap, *enable); err != nil {
		fmt.Fprintln(os.Stderr, "host:", err)
		os.Exit(1)
	}
}

func run(addr, zapAddr, enable string) error {
	app := zip.New(zip.Config{AppName: "cloud"})

	// Liveness belongs to the HOST, not to any app: it must answer while every
	// plugin is still cold, or a lazy fleet fails its readiness probe before the
	// first real request ever arrives and gets restarted forever.
	app.Get("/healthz", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"status": "ok"})
	})

	// One secret for this host's whole child set, minted here and held only here.
	// The host is the only process that knows which app it started as which
	// child, so it is the only process that can say so — the credz broker will
	// not take a child's word for it (#51: a child chooses its own argv, and one
	// exec'd as `billing` used to be handed billing's KMS scope).
	//
	// credz/launch is stdlib-only for exactly this call site. Importing credz
	// itself would pull cek → modernc/sqlite + sqlcipher into a build whose whole
	// reason to exist is being ~395 packages instead of 3105.
	secret := launch.Secret()

	on := enabled(enable)
	for _, a := range manifest.Apps {
		if on != nil && !on[a.Name] {
			continue
		}
		p := a.Plugin()
		// Per-plugin, on the plugin's own Env, which zip appends to that ONE
		// child's environment. A token in the host's os.Environ() would reach
		// every child alike and prove nothing about any of them.
		p.Env = append(p.Env, launch.Env(secret, a.Name))
		// The secret itself crosses exactly one edge: to the child that runs the
		// broker, because it owns the sealed store and is therefore the process
		// that has to verify what the others present. Anything holding this can
		// mint any app's identity, so it goes nowhere else — and credz.Boot in
		// that child scrubs it from its environment on arrival.
		if a.Name == launch.Broker {
			p.Env = append(p.Env, launch.SecretEnv+"="+secret)
		}
		if err := app.Add(zip.Load(p, a.Prefixes...)); err != nil {
			return err
		}
		delete(on, a.Name)
	}
	// A name that matched nothing is a typo, and the symptom of tolerating one is
	// a subsystem that is simply absent from a deployment with no error anywhere.
	if len(on) > 0 {
		return fmt.Errorf("--enable names %v, which the manifest does not list — run `make generate` if the app is new, else fix the name", keys(on))
	}

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
