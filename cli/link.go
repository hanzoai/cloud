package cli

// link.go — `hanzo link | unlink | status`: bring THIS machine into the Hanzo
// cloud fleet AS A NODE, take it back out, and view the fleet.
//
// A node is two orthogonal memberships, composed under one verb:
//
//  1. The FABRIC — hanzod on hanzo.network. `link` starts it by invoking the
//     canonical fabric verb `hanzo node up` (the Rust node CLI: resolve a hanzod
//     binary, spawn it detached, record its pid). link does NOT reimplement hanzod
//     supervision — there is exactly one way to start hanzod, and this composes it.
//     Best-effort: a node with no hanzod still joins the compute fleet (CPU-only
//     boxes and dev machines link fine); `--no-fabric` skips it outright.
//
//  2. The COMPUTE fleet — this machine's inventory (CPU cores + model, memory, and
//     each GPU as its own resource) registered as a heartbeating presence in the
//     org's `fleet` namespace, running the outbound worker loop that claims jobs
//     from `gpu-jobs`. That machinery lives in gpu.go (runConnect); `link` runs it.
//
// `unlink` reverses both: it deregisters the worker (drops the fleet row), then
// stops the fabric (`hanzo node stop`). `status` is the fleet view — every machine
// with each of its GPUs shown distinctly, this box highlighted.
//
// One identity: the IAM token `hanzo login` mints authorizes every cloud call; the
// server derives the tenant from the token. No secrets on the box.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// link / unlink / status — the node-level command surface.
// ---------------------------------------------------------------------------

func newLinkCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	var opts connectOpts
	var daemon bool
	var noFabric bool

	cmd := &cobra.Command{
		Use:   "link",
		Short: "Bring this machine into the Hanzo cloud fleet as a node",
		Long: "Link this machine into the Hanzo cloud as a node: join the fabric (start\n" +
			"hanzod on hanzo.network) and register as a compute worker — advertising this\n" +
			"host's CPU (cores + model), memory, and each GPU as its own resource, then\n" +
			"heartbeating and claiming jobs from your org's queue. The node shows up in the\n" +
			"console (Machines + GPUs) and on `hanzo status`. Works on a CPU-only box.\n" +
			"Authentication reuses the `hanzo login` token; the org is taken from its claims.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if daemon {
				return installDaemon(cmd, opts)
			}
			// 1. Fabric: start hanzod via the canonical `hanzo node up` (best-effort).
			startFabric(cmd, noFabric)
			// 2. Compute fleet: register inventory, heartbeat, claim jobs (foreground).
			return runConnect(cmd, envOf(), opts)
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.jobsNS, "jobs-namespace", defaultJobsNS, "tasks namespace to claim jobs from")
	f.BoolVar(&noFabric, "no-fabric", false, "join the compute fleet only; do not start hanzod")
	f.BoolVar(&daemon, "daemon", false, "install a systemd --user unit (Restart=always) instead of running in the foreground")
	f.BoolVar(&opts.serveEngine, "serve-engine", false, "also advertise a hanzo-engine model server (OpenAI + Anthropic) running on this node")
	f.StringVar(&opts.engineURL, "engine-url", defaultEngineURL, "local URL where hanzo-engine is probed (GET /v1/models)")
	f.StringVar(&opts.engineEndpoint, "engine-endpoint", "", "public URL to advertise for gateway routing (defaults to --engine-url; a node behind NAT needs a reachable URL/tunnel)")
	f.BoolVar(&opts.registerProvider, "register-provider", false, "auto-register the engine endpoint as an org model provider (POST /v1/add-provider)")
	f.StringVar(&opts.studioDir, "studio-dir", os.Getenv("HANZO_STUDIO_DIR"), "local Hanzo Studio checkout; when set, link launches and supervises the render backend on 127.0.0.1:8188")
	f.StringVar(&opts.studioURL, "studio-url", firstNonEmpty(os.Getenv("HANZO_STUDIO_UPLOAD_URL"), defaultStudioUploadURL), "studio base URL the render mirror uploads finished images to (POST /v1/library/upload)")
	return cmd
}

func newUnlinkCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Take this machine out of the fleet (deregister + stop hanzod)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Reverse of link, both best-effort: drop the compute-fleet row, then
			// ALWAYS stop the fabric so a deregister error never leaves hanzod
			// running. The deregister error is reported, not short-circuited.
			derr := runDisconnect(cmd, envOf())
			stopFabric(cmd)
			return derr
		},
	}
}

func newStatusCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the org's fleet — every machine with each of its GPUs (this box highlighted)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runFleetStatus(cmd, envOf()) },
	}
}

// ---------------------------------------------------------------------------
// Fabric — compose the canonical `hanzo node up` / `node stop`.
// ---------------------------------------------------------------------------

// fabricCLI resolves the Hanzo node CLI that owns hanzod supervision — the Rust
// fabric/dev CLI with `node up/stop`. In the canonical layout the Go unified binary
// takes the `hanzo` name and the Rust CLI is installed alongside it as `hanzo-node`,
// so `link` composes `node up` without shelling into itself (this binary has no
// `node`). Resolution order: HANZO_FABRIC_CLI, then `hanzo-node`, then a
// self-guarded `hanzo` (for boxes where the Rust CLI still holds the `hanzo` name).
// Returns "" when none is resolvable.
func fabricCLI() string {
	if p := os.Getenv("HANZO_FABRIC_CLI"); p != "" {
		return p
	}
	if p, err := exec.LookPath("hanzo-node"); err == nil {
		return p
	}
	p, err := exec.LookPath("hanzo")
	if err != nil {
		return ""
	}
	if self, err := os.Executable(); err == nil {
		if sp, _ := os.Readlink(p); sp == self || p == self {
			return ""
		}
	}
	return p
}

// Passthrough delegates a verb this binary does not own — node, dev, wallet,
// network, … — to the Rust fabric/dev CLI (resolved by fabricCLI, installed as
// `hanzo-node`), so the single `hanzo` name is a SUPERSET: Go verbs served natively,
// everything else handed through unchanged. `hanzo node up` (the fabric `link`
// itself composes) works for users too. It runs the delegate to completion with
// inherited stdio and exits with its code; it returns false only when no fabric CLI
// is resolvable, so the caller can report unknown-subcommand.
func Passthrough(args []string) bool {
	bin := fabricCLI()
	if bin == "" {
		return false
	}
	c := exec.Command(bin, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "hanzo: delegating %v to %s: %v\n", args, bin, err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}

// startFabric starts hanzod by invoking `hanzo node up` — the one canonical way to
// join the fabric. Best-effort by design: a missing CLI or a missing hanzod prints
// a clear note and the node still joins the compute fleet. `--no-fabric` skips it.
func startFabric(cmd *cobra.Command, skip bool) {
	out := cmd.OutOrStdout()
	if skip {
		fmt.Fprintln(out, "fabric: skipped (--no-fabric); joining the compute fleet only")
		return
	}
	bin := fabricCLI()
	if bin == "" {
		fmt.Fprintln(out, "fabric: hanzo node CLI not found — joining the compute fleet only")
		fmt.Fprintln(out, "        (install the `hanzo` node CLI or set HANZO_FABRIC_CLI to start hanzod)")
		return
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, bin, "node", "up")
	c.Stdout, c.Stderr = out, cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		fmt.Fprintf(out, "fabric: `%s node up` did not start hanzod (%v) — joining the compute fleet only\n", bin, err)
	}
}

// stopFabric stops the hanzod this box started, via the canonical `hanzo node stop`.
// Best-effort — nothing to stop is not an error.
func stopFabric(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	bin := fabricCLI()
	if bin == "" {
		return
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, bin, "node", "stop")
	c.Stdout, c.Stderr = out, cmd.ErrOrStderr()
	_ = c.Run()
}
