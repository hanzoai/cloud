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

	"github.com/hanzoai/cloud/cli"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// Restore the real stdout (cli.init redirected it to stderr so dependency
	// startup chatter cannot corrupt machine-readable output).
	cli.RestoreStdout()

	// Share the build version with the CLI (User-Agent, `hanzo version`).
	cli.Version = version

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
