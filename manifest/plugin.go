// Package manifest is what the light host knows about the fleet: for every
// subsystem that ships as its own binary, its name, the absolute paths it
// answers, and whether it must already be running when the first request
// arrives.
//
// It deliberately imports NOTHING but zip. That is the whole point — cmd/cloud
// links this package and zip and stops, so adding the 70th subsystem does not
// grow the host's build by one package. What an app DOES belongs to the app's
// own binary; where it lives and what it answers is all the router needs.
//
// apps.go is the single source of truth, and it is HAND-AUTHORED — see its own
// header. Apps is the set and its load order; the per-app flags sit on the row.
// A change is made there. plugin/gen-app-cmds reads it the other way, validating
// that every row has a plugin/<name> and every plugin/<name> has a row.
package manifest

import (
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
	// reaching one of its prefixes. It is for a subsystem whose work is not
	// request-driven — one that owns a listener or a background loop, where
	// deferring the start means it silently does nothing and the symptom is an
	// empty dashboard rather than an error.
	Eager bool

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
	// installed at Serve answers 404 on its prefixes for an org that does not hold
	// the flag named for it (cloud.Stage). Promotion is one edit here.
	//
	// The zero value is ga, which is the direction that fails safe in the one way
	// that matters: a row nobody thought about is PUBLISHED and reachable, which is
	// visible the day it lands, rather than hidden and reachable by nobody, which
	// is not visible at all.
	Stage string

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
//	                    else the release index at CLOUD_PLUGINS (see release.go),
//	                    which is how a host with NO plugins in its image runs.
//
// The default is the shipped container layout — one directory, the host plus its
// per-app plugins, no configuration. Resolving from os.Executable rather than
// $PATH means a host always loads the binaries it was built and shipped with, not
// whichever ones a PATH finds.
//
// There is ONE way a name resolves to a binary: its own. A subsystem is its own
// plugin/<name> binary, on disk beside the host or fetched by digest from the
// release index — the two link modes of one contract (a developer builds the
// single lean plugin they are editing; a release ships every per-app binary and
// the host falls through to the index). A dedicated binary present on disk is
// someone's explicit intent, so it wins over the index.
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
// every other call goes through — declares Eager instead, which makes it
// non-lazy and therefore never a candidate.
const idleAfter = 15 * time.Minute

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
// It is a COUNT because the cost is the count. Measured in the running pod, the
// per-child distribution is flat: 33 children, mean 167MiB, largest 251MiB. No
// single subsystem dominates, so how many are up IS the bill.
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
	// perChildMiB is the measured mean resident size of one plugin child: 33
	// children at 167MiB, flat, no subsystem dominating.
	perChildMiB = 167
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
func warmFor(requestMiB string) int {
	mib := defaultRequestMiB
	if n, err := strconv.Atoi(strings.TrimSpace(requestMiB)); err == nil && n > 0 {
		mib = n
	}
	n := (mib - hostMiB) / perChildMiB
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

func (a App) resolve() zip.Plugin {
	env := "CLOUD_" + strings.ToUpper(strings.NewReplacer("-", "_").Replace(a.Name))
	if addr := strings.TrimSpace(os.Getenv(env + "_ADDR")); addr != "" {
		return zip.Plugin{Name: a.Name, Addr: addr}
	}
	// An explicit path is honoured as given: the operator named this file, so
	// silently running something else instead would be a lie.
	if path := strings.TrimSpace(os.Getenv(env + "_BIN")); path != "" {
		return zip.Plugin{Name: a.Name, Path: path, Lazy: !a.Eager, IdleAfter: idleAfter}
	}
	dir := ""
	if self, err := os.Executable(); err == nil {
		dir = filepath.Dir(self)
	}
	// On-disk wins: it is what this host was built with. The index is the rung
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
	return zip.Plugin{Name: a.Name, Path: filepath.Join(dir, a.Name), Lazy: !a.Eager, IdleAfter: idleAfter}
}
