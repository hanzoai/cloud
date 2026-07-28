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
// cmd/<name> binary, on disk beside the host or fetched by digest from the
// release index — the two link modes of one contract (a developer builds the
// single lean plugin they are editing; a release ships every per-app binary and
// the host falls through to the index). A dedicated binary present on disk is
// someone's explicit intent, so it wins over the index.
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
	// The dedicated per-app binary, beside the host — and nothing else. Every
	// subsystem ships as its own binary now (cmd/<name>), so a name resolves to
	// <dir>/<name> or it does not resolve on disk at all. A missing one is named
	// in the failure, because that is the binary a developer expects to have
	// built (or the release ladder below fills in over the network).
	return zip.Plugin{Name: a.Name, Path: filepath.Join(dir, a.Name), Lazy: !a.Eager}
}
