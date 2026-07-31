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
// apps.go is the single source of truth for both facts (Wire() for the set and
// its order, the `eager` map for the rest), and apps.go is where a change is
// made. Apps in apps.go is generated from it — see plugin/gen-app-cmds.
package manifest

import (
	"os"
	"path/filepath"
	"strings"

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

	// Open means this app also serves tools that depend on WHO is asking, so its
	// build-time catalogue (plugin/<name>/mcp.json) is incomplete BY CONSTRUCTION
	// and the host asks it per caller — see zip.Plugin.Open. Exactly one app in
	// the fleet may be open, because a tool name no catalogue claims has to
	// resolve somewhere and two candidates would make it ambiguous.
	Open bool

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
func (a App) Plugin() zip.Plugin {
	p := a.resolve()
	// Open is a property of the APP — what it serves — and not of where its binary
	// came from, so it is stamped once here rather than in each of resolve's four
	// rungs, which is four places for it to be forgotten.
	p.Open = a.Open
	return p
}

// resolve is the ladder: an operator's address, an operator's path, the binary on
// disk beside the host, a published release, and finally the on-disk path again so
// the failure names the file a developer expected to have built.
func (a App) resolve() zip.Plugin {
	env := "CLOUD_" + strings.ToUpper(strings.NewReplacer("-", "_").Replace(a.Name))
	if addr := strings.TrimSpace(os.Getenv(env + "_ADDR")); addr != "" {
		return zip.Plugin{Name: a.Name, Addr: addr}
	}
	// An explicit path is honoured as given: the operator named this file, so
	// silently running something else instead would be a lie.
	if path := strings.TrimSpace(os.Getenv(env + "_BIN")); path != "" {
		return zip.Plugin{Name: a.Name, Path: path, Lazy: !a.Eager}
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
	return zip.Plugin{Name: a.Name, Path: filepath.Join(dir, a.Name), Lazy: !a.Eager}
}
