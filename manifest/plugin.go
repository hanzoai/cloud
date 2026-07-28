// Package manifest is what the light host knows about the fleet: for every
// subsystem that ships as its own binary, its name, the absolute paths it
// answers, and whether it must already be running when the first request
// arrives.
//
// It deliberately imports NOTHING but zip. That is the whole point — cmd/host
// links this package and zip and stops, so adding the 70th subsystem does not
// grow the host's build by one package. What an app DOES belongs to the app's
// own binary; where it lives and what it answers is all the router needs.
//
// apps.go is the single source of truth for both facts (Wire() for the set and
// its order, the `eager` map for the rest), and apps.go is where a change is
// made. Apps in apps.go is generated from it — see cmd/gen-app-cmds.
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

	// Eager starts the child WITH the host instead of on the first request
	// reaching one of its prefixes. It is for a subsystem whose work is not
	// request-driven — one that owns a listener or a background loop, where
	// deferring the start means it silently does nothing and the symptom is an
	// empty dashboard rather than an error.
	Eager bool
}

// MultiCall is the unified binary's name. Serving ONE app is not a second mode
// of it — `cloud --enable=<name>` is the mode `cloud.Serve` has always had, and
// a child started that way is byte-for-byte the same process a dedicated
// cmd/<name> binary would be: same Serve, same middleware, same ZIP_ADDR
// contract. The host cannot tell them apart and does not try.
const MultiCall = "cloud"

// Plugin says where this app's binary is, without naming it twice:
//
//	CLOUD_<NAME>_ADDR — already listening there; start nothing, just mount it.
//	CLOUD_<NAME>_BIN  — the binary's path on disk.
//	neither           — a file named <name> beside the running host,
//	                    else the multi-call binary beside it, serving that app,
//	                    else the release index at CLOUD_PLUGINS (see release.go),
//	                    which is how a host with NO plugins in its image runs.
//
// The default is the shipped container layout — still one directory, still no
// configuration. Resolving from os.Executable rather than $PATH means a host
// always loads the binaries it was built and shipped with, not whichever ones a
// PATH finds.
//
// The last rung is what makes shipping all 108 affordable. A dedicated plugin is
// ~40MB of which ~35MB is the core every other plugin also links, so 108 of them
// weigh 4.5GB — 108 copies of the same code. The multi-call binary is that core
// ONCE (212MB) plus the 19MB host, and it serves every app. Both rungs stay,
// because they are the two link modes of one contract: a developer builds the
// single lean plugin they are editing and the host prefers it (1.3s rebuild), a
// release ships the unified binary and the host falls through to it. Preference
// order is deliberate — a dedicated binary present on disk is someone's explicit
// intent, so it wins.
func (a App) Plugin() zip.Plugin {
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
	path := filepath.Join(dir, a.Name)
	if _, err := os.Stat(path); err == nil {
		return zip.Plugin{Name: a.Name, Path: path, Lazy: !a.Eager}
	}
	multi := filepath.Join(dir, MultiCall)
	if _, err := os.Stat(multi); err == nil {
		return zip.Plugin{Name: a.Name, Path: multi, Args: []string{"--enable=" + a.Name}, Lazy: !a.Eager}
	}
	// Neither shipped. Name the dedicated path in the failure, because that is
	// the one a developer is expecting to have built.
	return zip.Plugin{Name: a.Name, Path: path, Lazy: !a.Eager}
}
