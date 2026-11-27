package manifest

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// App is one mountable subsystem.
type App struct {
	// Name identifies the app in logs, names its socket, and is the stem of
	// both its env overrides and its sibling binary.
	Name string

	// Grace is how long the process has from SIGTERM to exit: to hand off what it
	// drives, close its stores and ship them. Zero is zip's DefaultGrace. A deployment's
	// terminationGracePeriodSeconds must cover the longest of them.
	Grace time.Duration

	// Pkg is the directory under apps/ holding the app's code, when that is not
	// the Name. Empty — nearly every app — means apps/<Name>.
	//
	// The two are separable because a capability's ADDRESS and its package are
	// different facts: `project` answers /v1/project and its code lives in
	// apps/projects. Every generator that needs one from the other reads THIS
	// field, so the pairing is stated once. It was stated nowhere, and the plane
	// generator inferred an app's name from its directory instead — which is a
	// second source for a name this file already owns, and it disagreed the day
	// the two diverged: seven plane clients addressed a socket nothing bound.
	Pkg string

	// Prefixes are the absolute paths it answers. zip mounts each path AND its
	// subtree, and the router takes the first match — so a shallower prefix
	// registered earlier wins, exactly as it does when everything is linked into
	// one binary.
	Prefixes []string

	// Gates are the subtrees whose MIDDLEWARE this app may install, when that is
	// not the same list as Prefixes. Empty — every app but one — means Prefixes,
	// because you gate what you serve.
	//
	// It exists for the co-resident case, where the two genuinely differ: zen
	// answers no path and wraps ai's "/v1". Saying that with Prefixes would be a
	// routing claim on a path it does not serve — the duplicate this field's
	// neighbour Coresident was introduced to remove — and saying it nowhere is
	// what actually happened: the grant went silently to nil and zen's mount was
	// refused. See [GrantFor], which is the only reader.
	Gates []string

	// Coresident means the app is NOT prefix-routed: it mounts as middleware on
	// another app's router and decides per request whether to serve or call Next.
	// The light host must not Load it, because Load's whole job is to claim a
	// prefix and hand matching requests to a process — a contract a middleware
	// cannot express, since a proxied request never falls through to the next
	// candidate.
	//
	// The row still exists, because every plugin/<name> binary needs one (the
	// gen-app-cmds bijection). What it must not do is state a Prefix it does not
	// route: zen said "/v1", the same prefix ai serves, so the manifest carried a
	// duplicate claim that only worked because nothing checked. zip now refuses
	// two owners for one prefix at compose time, which is how this surfaced.
	// Naming the property is the fix; tolerating the duplicate would have been a
	// second way to say one thing.
	Coresident bool

	// Eager starts the child WITH the host instead of on the first request
	// reaching one of its prefixes, and it is never stopped — not for being idle,
	// not to make room under Warm.
	//
	// It is for a subsystem whose work is not request-driven — one that owns a
	// listener or a background loop, or that the host or an eager sibling needs
	// while it boots — where deferring the start means it silently does nothing.
	// Every deployment takes it, whatever its size.
	//
	// The price is the process: resident for the life of the host, and started
	// inside the host's serial boot.
	Eager bool

	// Wire is the TCP port this app answers in a protocol of its own, not HTTP
	// or ZAP: Kafka's, for kafka. Zero, every other app, means none.
	//
	// The HOST binds it and keeps it bound, so the app can stay lazy: a wire
	// client never calls the app's HTTP prefix, and an app that bound the port
	// itself would have to start with the host to be reachable at all. The first
	// connection starts the app the way a first request does, and the host relays
	// every connection to the app's own listener at its zip socket + WireSuffix.
	// While a connection is open the host keeps the app from going idle; when the
	// last one closes, the app is stopped after IdleAfter like any lazy app.
	//
	// CLOUD_<NAME>_PORT overrides it (Port), and the app advertises that same
	// value to its clients, so the two cannot name different ports.
	Wire int

	// Turn marks an app a turn waits on: a Slack question or a coding run cannot
	// finish without it. Where the deployment pins the turn path the app is
	// PINNED — started with the host and never stopped, exactly as Eager — and
	// elsewhere it starts on first use like any lazy app. See [TurnPinned].
	//
	// Pinning is for latency, and it is needed even when traffic is steady. The
	// host sees only what it routes: a plane call dials the child's socket
	// directly, so an app reached that way is stopped fifteen minutes after it
	// started however busy it is, and any burst that needs the room stops it
	// sooner. On api.hanzo.ai (2026-09-19, three hours) a wake took 1.6s at the
	// median, 4.2s at p90 and 11.3s at worst; project was stopped fifteen times
	// while answering the site edge every second, and one Slack turn paid a cold
	// start for allowance, flag, websearch and crawl, one after another.
	// TestTheTurnPathIsPinned names that path.
	//
	// It is a separate fact from Eager because it is a trade and Eager is not. A
	// listener that is not bound does nothing at all; a turn-path app that is not
	// pinned costs its first caller a cold start. A laptop makes that trade the
	// other way: a resident process per turn-path app is more than one developer's
	// cold starts are worth.
	Turn bool

	// Required means the HOST must not serve without this app. A required app
	// that will not start aborts the process; every other app degrades to being
	// absent — its prefixes answer 503 and the rest of the fleet serves.
	//
	// It is deliberately a property of the app rather than of start order,
	// because start order is where it lived by accident and that cost a 25-minute
	// outage of the whole API: pubsub is Apps[0] and Eager, so when its child
	// could not open a store, the single `return err` in the host's mount loop
	// took down the API, IAM validation, billing and the team backend with it.
	// Being first in a list is not a claim on everyone else's availability.
	//
	// The default is false, and NOTHING in Apps sets it — see
	// manifest/required_test.go for the argument and for what would justify an
	// entry. The host is a router: it opens no store, validates no token, and
	// holds no state whose absence corrupts anything. Every child enforces its
	// own auth in its own process, so one child's absence cannot silently weaken
	// another's plane — the planes ARE processes. Against that, aborting buys
	// exactly one thing (a pod that never goes Ready) and destroys the console,
	// the health surface, the log stream an operator needs, and every healthy
	// sibling. CrashLoopBackOff is the state in which a process cannot tell you
	// why it is unhappy.
	Required bool

	// Vital means the host is not fit to RECEIVE TRAFFIC without this app: its
	// absence is reported on /readyz as a 503, so Kubernetes takes the pod out of
	// the Service and a rollout that breaks it stalls against the old pods instead
	// of replacing them.
	//
	// It is the other half of Required, and the two are deliberately separate
	// because they answer different questions. Required asks "may this process
	// run at all", and the answer is argued above: aborting destroys the console,
	// the health surface, the log stream and every healthy sibling, so nothing
	// sets it. Vital asks "should this process be sent requests", and the pod that
	// answers no is still up, still serving its siblings, and still able to say
	// why. Required's own doc names "a pod that never goes Ready" as the one thing
	// aborting buys; Vital buys exactly that and nothing else.
	//
	// The bar is NOT "serving without it is unsafe" — that is Required's bar. It
	// is "serving without it is pointless": traffic that arrives will not be
	// answered, so routing it here helps nobody. That is a strictly narrower claim
	// than Required's, and `ai` is the app that meets it.
	//
	// Written against 2026-08-01, ~30 minutes of api.hanzo.ai/v1/models and
	// /v1/chat/completions answering 503 {"error":"mount /v1: no instance
	// running"} while the pod stayed Ready with 0 restarts. o11y v1.5.41 seized
	// :4317-:4319 from the `ai` child, the child's listen failed, and mount()
	// correctly degraded it to absent — but absence went into a map that only
	// /healthz reported, in a FIELD, and the probe reads the STATUS CODE. Every
	// specifically-mounted prefix (/v1/o11y, /v1/commerce/catalog,
	// /v1/admin/*) kept answering from its own subsystem, so only a path falling
	// THROUGH to `ai` showed it. A health check that returns 200 while the entire
	// product API is absent is not a health check.
	Vital bool

	// Stage is whether a customer is shown this capability: [Beta], [Alpha], or
	// empty for ga (HIP-0139 §8). It is a fact about the PRODUCT, not about the
	// specification — a HIP's status says whether the text is settled.
	//
	// It is here, in the row, because every reader of it is downstream of the row
	// and none of them may decide for themselves: the compose stamps x-stage from it
	// and the public rule drops anything that is not ga, so a beta capability is in
	// no generated client, no tool list and no public page; and the refusal
	// installed at Serve answers 404 on its prefixes to a caller not let in
	// (cloud.Stage): for an alpha row, an org that does not hold the flag named for
	// it; for a beta row, a caller the platform's `beta` switch is off for.
	// Promotion is one edit here.
	//
	// The zero value is ga, which is the direction that fails safe in the one way
	// that matters: a row nobody thought about is PUBLISHED and reachable, which is
	// visible the day it lands, rather than hidden and reachable by nobody, which
	// is not visible at all.
	Stage string

	// Open are the GET addresses of a staged row that a caller with no org of its
	// own may read, for the org its ?org= names: the refusal asks about THAT org,
	// and the app answers only what the org granted public. Every other address of
	// the row still answers such a caller 404. See cloud.Stage, the one reader.
	Open []string
}

// The stages a row may declare. There is no `ga` constant because ga is the
// ABSENCE of one: a third spelling of the default is a second way to say the
// same thing, and every reader would then have to accept both.
const (
	Beta  = "beta"
	Alpha = "alpha"
)

// GA reports whether a customer is shown this capability without a flag.
func (a App) GA() bool { return a.Stage == "" }

// Names is every app, in mount order — which is the fleet's routing order and
// therefore the order its document is composed in, so a conflict is reported as the
// router would meet it.
//
// It exists so that "the fleet, as a list of names" is written once. Both callers
// are about the published document (the host that serves it, the gate that writes
// it) and a second loop over Apps in either would be a second answer to a
// question with one.
func Names() []string {
	out := make([]string, len(Apps))
	for i, a := range Apps {
		out[i] = a.Name
	}
	return out
}

// Plugin says where this app's binary is, without naming it twice:
//
//	CLOUD_<NAME>_ADDR — already listening there; start nothing, just mount it.
//	CLOUD_<NAME>_BIN  — the binary's path on disk.
//	neither           — a file named <name> beside the running host,
//	                    else the stanza plugins.lock pins for it (see release.go),
//	                    which is how a host with NO plugins in its image runs.
//
// The default is the shipped container layout — one directory, the host plus its
// per-app plugins, no configuration. Resolving from os.Executable rather than
// $PATH means a host always loads the binaries it was built and shipped with, not
// whichever ones a PATH finds.
//
// There is ONE way a name resolves to a binary: its own. A subsystem is its own
// plugin/<name> binary, on disk beside the host or fetched by digest from the
// pinned release — the two link modes of one contract (a developer builds the
// single lean plugin they are editing; a release ships every per-app binary and
// the host falls through to the lock). A dedicated binary present on disk is
// someone's explicit intent, so it wins over the lock.
func (a App) Plugin() zip.Plugin { return a.resolve() }

// resolve is the ladder: an operator's address, an operator's path, the binary on
// disk beside the host, a published release, and finally the on-disk path again so
// the failure names the file a developer expected to have built.
// IdleAfter is how long a lazy subsystem may go unused before its process is
// stopped, to be started again by the next request that needs it.
//
// Every subsystem here is its own process, and until now none of them ever
// stopped: resident memory tracked the SIZE OF THE CATALOG rather than the
// traffic. Measured on the fleet — 24 children, ~150MiB of live heap each,
// ~4.4GiB, while the three that do little sat at 12-20MiB. That is what caps
// how many subsystems this host can carry, and it is why one of them being
// evicted for node memory took the whole API down.
//
// Fifteen minutes is chosen to be longer than any human's think-time between
// two calls to the same surface, so an interactive session never pays a cold
// start twice. A subsystem that must NEVER pay one — identity, config, anything
// every other call goes through — declares Eager or Turn instead, which pins it
// and therefore makes it never a candidate.
//
// The clock counts what the HOST routes and nothing else. A call over the
// internal plane dials the child's socket directly, so an app reached only that
// way is stopped on this schedule however busy it is — which is a reason for
// that app to declare Turn, never a reason to lengthen this.
const idleAfter = 15 * time.Minute

// Idle is how long a lazy subsystem may go unused before its process is
// stopped (idleAfter). The host reads it to keep an app that serves a Wire
// connection, which zip does not route and so cannot see, from going idle.
func Idle() time.Duration { return idleAfter }

// Warm is how many plugin processes this host may hold at once. Age bounds the
// steady state; this bounds the BURST, which age cannot reach — IdleAfter only
// reclaims a plugin quiet for its whole window, so in the minutes after a cold
// start, when every prefix that gets a request starts a child and none is old
// enough to be idle, it reclaims nothing. zip enforces this at the START path,
// not on the reaper's ticker: the fleet's MCP server asks every subsystem at
// once and starts children far faster than a sweep runs, and a bound restored a
// minute later is not a bound — that is how this pod came to hold every child it
// had, stop answering its own liveness probe, and get killed.
//
// It is a COUNT because the cost is the count: the mean child times the ceiling
// reproduces the pod's measured resident total.
//
// THE BINDING NUMBER IS THE REQUEST, NOT THE LIMIT, and it is now READ rather
// than assumed. The kubelet scores eviction candidacy against the REQUEST, so a
// pod comfortably inside its limit is still first in line once it exceeds what it
// reserved — which is exactly how this one was evicted for node memory and took
// the API down with it. A process cannot see its own request (the cgroup carries
// the limit), so the Deployment projects it through the downward API and this
// reads it: one number, stated where it is decided, with the ceiling derived from
// it. It was a hand-sized 36 against an assumed 6Gi, which meant raising the
// reservation bought capacity the host would not use and lowering it left the host
// budgeting against memory the pod no longer had, silently, in the direction that
// gets it evicted.
//
// The floor is the observed working set, because a ceiling below it does not save
// memory — it thrashes, evicting a child about to be asked for again — and the cap
// is the app count, past which there is nothing left to hold.
func Warm() int {
	return warmFor(os.Getenv(memoryRequestEnv))
}

const (
	// memoryRequestEnv carries the container's memory REQUEST in MiB, projected by
	// the Deployment (resourceFieldRef requests.memory, divisor 1Mi). Unset is the
	// unmanaged case — a developer's box, a test — and yields the reservation the
	// count was hand-sized against, so behaviour off a cluster is what it was.
	memoryRequestEnv = "CLOUD_MEMORY_REQUEST_MIB"
	// perChildMiB is the measured mean resident size of one plugin child: 630
	// readings of 35 children in production, mean 70.5MiB, median 53MiB, p90
	// 118MiB. 35 x 70.5 + 200 reproduces the measured pod total of 2.6-2.9GiB.
	perChildMiB = 71
	// hostMiB is what the host itself holds, off the children's budget.
	hostMiB = 200
	// defaultRequestMiB is the reservation in the deployment this was measured in,
	// and therefore what an unmanaged process assumes.
	defaultRequestMiB = 6 * 1024
)

// warmFor is Warm over a value the caller already holds, so the derivation is
// testable without an environment. A malformed or absent value falls back to the
// measured reservation rather than to zero: a ceiling of zero would hold no
// children at all, which is an outage, and this must never be the thing that
// causes one.
func warmFor(request string) int {
	n := (requestMiB(request) - hostMiB) / perChildMiB
	if n < 1 {
		// A host that may hold no child at all serves nothing. One is the floor,
		// and a reservation this small is the operator's to fix.
		return 1
	}
	if n > len(Apps) {
		return len(Apps)
	}
	return n
}

// requestMiB is the reservation a deployment states, or the measured one when
// it states none or one that does not parse.
func requestMiB(request string) int {
	if n, err := strconv.Atoi(strings.TrimSpace(request)); err == nil && n > 0 {
		return n
	}
	return defaultRequestMiB
}

// Pinned reports whether the host starts this app with itself and never stops
// it: every Eager app, every Vital app, and every Turn app where this deployment
// pins the turn path. A Vital app is pinned because /readyz reads its absence
// from the boot, and an app the boot never started is never absent.
func (a App) Pinned() bool { return a.Eager || a.Vital || (a.Turn && TurnPinned()) }

// TurnPinned reports whether this deployment pins the turn path. It does where the
// reservation is at least defaultRequestMiB, and where none is stated: that is the
// reservation the latency pinning buys was measured on, and the one it was sized
// against, because a pinned app is never evicted yet counts against Warm.
// Production states none today, so a Deployment that starts projecting a smaller
// request unpins the turn path with it.
//
// It is read off the reservation because that is the one fact about its own size
// a deployment already states. Below it, the turn path starts on first use and
// only the Eager and Vital apps start with the host. `make dev` states 4720 MiB, a
// ceiling of 63, which holds the pinned apps and a whole turn at once, so a turn
// pays each cold start once and evicts nothing.
func TurnPinned() bool { return requestMiB(os.Getenv(memoryRequestEnv)) >= defaultRequestMiB }

// WireSuffix names an app's own listener for its Wire protocol: the app's zip
// socket with this suffix. It sits beside zip's <sock>.http and is the same
// length, so the socket-length check zip makes before it starts a child covers
// it too.
const WireSuffix = ".wire"

// Port is the TCP port the host binds for this app's Wire protocol: Wire, or
// CLOUD_<NAME>_PORT when that is set to a port. Zero means the app has none.
func (a App) Port() int {
	if a.Wire == 0 {
		return 0
	}
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(a.env() + "_PORT"))); err == nil && v > 0 && v < 1<<16 {
		return v
	}
	return a.Wire
}

// Listen opens this app's own listener for its Wire protocol: the unix socket
// the host relays to (zip socket + WireSuffix) when the process is a host's
// child, else the TCP port itself. It answers the listener and where it is.
//
// The socket is opened inside the app's Use, before its zip socket accepts, so
// it is there by the time the host's Start returns. A file left at the path by
// an earlier instance is removed first; the host runs one of each app at a time.
func (a App) Listen() (net.Listener, string, error) {
	if sock := zip.Addr(""); sock != "" {
		path := sock + WireSuffix
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, "", fmt.Errorf("clear %s: %w", path, err)
		}
		ln, err := net.Listen("unix", path)
		if err != nil {
			return nil, "", fmt.Errorf("listen %s: %w", path, err)
		}
		return ln, path, nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", a.Port()))
	if err != nil {
		return nil, "", fmt.Errorf("listen :%d: %w", a.Port(), err)
	}
	return ln, ln.Addr().String(), nil
}

// Named is the row for name, if the manifest has one.
func Named(name string) (App, bool) {
	for _, a := range Apps {
		if a.Name == name {
			return a, true
		}
	}
	return App{}, false
}

// env is the stem of this app's overrides: CLOUD_<NAME>.
func (a App) env() string {
	return "CLOUD_" + strings.ToUpper(strings.NewReplacer("-", "_").Replace(a.Name))
}

func (a App) resolve() zip.Plugin {
	p := a.locate()
	if p.Addr == "" { // one already running is not started, so it has no environment to give
		p.Env = append(p.Env, a.sqlEnv()...)
	}
	return p
}

func (a App) locate() zip.Plugin {
	env := a.env()
	if addr := strings.TrimSpace(os.Getenv(env + "_ADDR")); addr != "" {
		return zip.Plugin{Name: a.Name, Addr: addr}
	}
	// An explicit path is honoured as given: the operator named this file, so
	// silently running something else instead would be a lie.
	if path := strings.TrimSpace(os.Getenv(env + "_BIN")); path != "" {
		return zip.Plugin{Name: a.Name, Path: path, Lazy: !a.Pinned(), IdleAfter: a.idleAfter()}
	}
	dir := ""
	if self, err := os.Executable(); err == nil {
		dir = filepath.Dir(self)
	}
	// On-disk wins: it is what this host was built with. The lock is the rung
	// below, so an image carrying plugins is unchanged and one carrying none
	// now works.
	if p := a.pluginIn(dir); found(p.Path) {
		return p
	}
	if p, ok := a.remote(); ok {
		return p
	}
	return a.pluginIn(dir)
}

// DefaultGrace is zip's own: the time a process has to exit after SIGTERM when its app
// states none.
const DefaultGrace = 20 * time.Second

// GraceOf is the time the named app's process has from SIGTERM to exit.
func GraceOf(name string) time.Duration {
	for _, a := range Apps {
		if a.Name == name && a.Grace > 0 {
			return a.Grace
		}
	}
	return DefaultGrace
}

func (a App) idleAfter() time.Duration {
	if a.Pinned() {
		return 0
	}
	return idleAfter
}

func found(path string) bool { _, err := os.Stat(path); return err == nil }

// pluginIn is the sibling-directory half of the ladder, split out because the
// choice it makes depends on what is ON DISK next to the host — and a test whose
// answer comes from os.Executable() can only ever see the test binary's own
// directory.
func (a App) pluginIn(dir string) zip.Plugin {
	// The dedicated per-app binary, beside the host — and nothing else. Every
	// subsystem ships as its own binary now (plugin/<name>), so a name resolves to
	// <dir>/<name> or it does not resolve on disk at all. A missing one is named
	// in the failure, because that is the binary a developer expects to have
	// built (or the release ladder below fills in over the network).
	return zip.Plugin{Name: a.Name, Path: filepath.Join(dir, a.Name), Lazy: !a.Pinned(), IdleAfter: a.idleAfter(), Grace: a.Grace}
}

// NameFor is the app whose code lives in apps/<pkg> — the inverse of App.Pkg,
// and the answer generators need when they are walking the tree rather than the
// manifest. Returns "" for a directory no app claims.
//
// A name is a fact about the app, not about where its files sit, so it is read
// from here rather than inferred from a path. Inferring it is what let seven
// generated plane clients keep addressing a socket the fleet stopped binding.
func NameFor(pkg string) string {
	for _, a := range Apps {
		if a.Pkg == pkg || (a.Pkg == "" && a.Name == pkg) {
			return a.Name
		}
	}
	return ""
}
