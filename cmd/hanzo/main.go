// Command hanzo is the Hanzo control CLI: `hanzo login`, `hanzo apps`,
// `hanzo deploy`, `hanzo gpu connect`, and every other verb the cli package
// serves. Anything it does not serve is handed to the Rust fabric CLI, so the
// single `hanzo` name stays a superset of both.
//
// CLIENT ONLY. This binary used to be both halves — a control CLI and, via
// apps.Wire(), a server that could mount any subsystem or the whole fused
// surface. That is what made it a 3102-package link, and it is why the file was
// deleted with apps.Wire and cmd/cloud when the mega build was killed
// (22f4fc64e). Server mode did not need saving: the host in cmd/cloud starts
// each app as its own plugin process, which is a better answer to the same
// question.
//
// The CLIENT half did need saving, and nothing was left to build it. `hanzo gpu
// connect` is the fleet worker — the process that claims render jobs on every
// BYO GPU — so with no main importing cli, the worker running the fleet could
// not be rebuilt from source in this repo or any other. A fix to it (the
// readiness gate, cli/gpu.go) had nowhere to ship.
//
// So this main links `cli` and nothing else. It cannot mount a subsystem and
// cannot reach apps.Wire; the fleet stays unlinked and the mega build stays
// dead. Serving is cmd/cloud's job, and asking is this one's.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/hanzoai/cloud/cli"
)

// version is stamped at link time by the Makefile's `hanzo` target,
// -ldflags "-X main.version=$(VERSION)". It is only the FIRST rung of
// resolveVersion, never the only one.
var version = "dev"

// readBuildInfo is the toolchain's own account of this binary. It is a var so
// the ladder below can be tested at every rung: which rung answers depends on
// the toolchain that built you (Go ≥1.24 fills Main.Version in from VCS itself;
// before that it is "(devel)" and only the vcs.revision rung saves you), and a
// rung nobody can reach is a rung nobody has checked.
var readBuildInfo = debug.ReadBuildInfo

// resolveVersion answers what this binary IS, and does it without depending on
// anyone remembering a build flag. That dependency is exactly what shipped
// broken: the ldflag was documented here and wired nowhere, so every build ever
// made — including the installed one — said "dev".
//
// The ladder, most authoritative first: the stamped release tag; the module
// version the toolchain records on its own (`go install ...@v1.2.3`, or a
// pseudo-version it derives from the checkout); the raw VCS stamp `go build`
// embeds. Only a binary whose toolchain knew nothing at all may still say "dev".
func resolveVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	info, ok := readBuildInfo()
	if !ok {
		return "dev"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if rev = s.Value; len(rev) > 12 {
				rev = rev[:12]
			}
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev == "" {
		return "dev"
	}
	// Unmistakably a source build, and it names the exact source: 0.0.0 sorts
	// below every release, and the commit is the whole point of asking.
	return "0.0.0-dev+" + rev + dirty
}

func main() {
	// Restore the real stdout (cli.init redirected it to stderr so dependency
	// startup chatter cannot corrupt machine-readable output).
	cli.RestoreStdout()

	// Share the build version with the CLI (User-Agent, `hanzo version`).
	// Resolved ONCE, here: cli holds the answer, it does not re-derive it.
	cli.Version = resolveVersion()

	if len(os.Args) < 2 {
		if err := cli.Execute([]string{"--help"}); err != nil {
			os.Exit(1)
		}
		return
	}

	// `version`, `--version` and `-v` are three spellings of ONE command, so
	// they normalise onto it rather than each printing their own line. A second
	// implementation here is how the version command's delegate reporting came
	// to be dead code: this short-circuit ran first and nobody saw the rest.
	switch os.Args[1] {
	case "version", "--version", "-v":
		if err := cli.Execute([]string{"version"}); err != nil {
			os.Exit(1)
		}
		return
	}

	// A control verb routes to the cobra CLI with the FULL args (including the
	// verb) so cobra parses its own subcommands and flags. cobra prints its own
	// errors, so this only translates failure to a non-zero exit.
	if os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" || cli.IsControlVerb(os.Args[1]) {
		if err := cli.Execute(os.Args[1:]); err != nil {
			os.Exit(1)
		}
		return
	}

	// Everything else belongs to the Rust fabric CLI (node, dev, wallet,
	// network, …), installed as `hanzo-node`. Passthrough execs and never
	// returns on success; it returns only when no fabric CLI is resolvable.
	cli.Passthrough(os.Args[1:])
	_ = cli.Execute([]string{"--help"})
	fmt.Fprintf(os.Stderr, "\nunknown subcommand %q (no `hanzo-node` fabric CLI to delegate to)\n", os.Args[1])
	os.Exit(1)
}
