package cloud

import (
	"errors"

	"github.com/zap-proto/zip"
)

// The three entry points a program is built from. Their bodies are the host's: the
// whole middleware chain in its one order, the process that serves several
// subsystems, the handles every subsystem is given. The host registers them
// (SetHooks) when it is linked, and every plugin main and cmd/cloud links it, so a
// program that reaches one of these has the host's body. Reaching one without a host
// is a program that was assembled wrong, said once and loudly.

// ErrNoHost is returned by [Listen] when no host is linked into the process.
var ErrNoHost = errors.New("cloud: no host is linked: import github.com/hanzoai/cloud/internal/host")

// App returns an app carrying everything a Hanzo program must carry, in the one
// order those parts are correct in. It is the only way to obtain one: a program
// mounts its subsystem on what it gets back and never builds a zip.App itself.
//
// name is what the program calls itself in a diagnostic. tools is the per-caller
// half of this program's agent MCP server — the tools that exist because of WHO is
// asking; everyone else passes nil and offers none (see [CallerTools]).
func App(name string, cfg *Config, deps Deps, tools zip.Source) *zip.App {
	f := hooks().App
	if f == nil {
		panic(ErrNoHost)
	}
	return f(name, cfg, deps, tools)
}

// Listen serves plugins as one process: it builds the deps, mounts every enabled
// subsystem, serves the plane socket and the public port, and drains on a signal.
// enable, when non-nil, names the subsystems to run.
func Listen(plugins []Plugin, enable []string) error {
	f := hooks().Listen
	if f == nil {
		return ErrNoHost
	}
	return f(plugins, enable)
}

// BuildDeps constructs the process-wide handles every subsystem is given.
func BuildDeps(cfg *Config) Deps {
	f := hooks().BuildDeps
	if f == nil {
		panic(ErrNoHost)
	}
	return f(cfg)
}
