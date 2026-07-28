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
//	neither           — a file named <name> beside the running host.
//
// The default is the shipped container layout — host and plugins in one
// directory, still one artifact — so a deployment needs no configuration at all.
// Resolving from os.Executable rather than $PATH means a host always loads the
// binaries it was built and shipped with, not whichever ones a PATH finds.
func (a App) Plugin() zip.Plugin {
	env := "CLOUD_" + strings.ToUpper(strings.NewReplacer("-", "_").Replace(a.Name))
	if addr := strings.TrimSpace(os.Getenv(env + "_ADDR")); addr != "" {
		return zip.Plugin{Name: a.Name, Addr: addr}
	}
	path := strings.TrimSpace(os.Getenv(env + "_BIN"))
	if path == "" {
		path = a.Name
		if self, err := os.Executable(); err == nil {
			path = filepath.Join(filepath.Dir(self), a.Name)
		}
	}
	return zip.Plugin{Name: a.Name, Path: path, Lazy: !a.Eager}
}
