package cli

// runner.go — `hanzo runner`: turn THIS machine into a JIT CI runner for your
// org's GitHub Actions. The host role of the arc daemon, migrated from
// arc-runner/arc (cmd/arcd) into cloud/runner: a GitHub App polls each configured
// org for queued workflow jobs and spawns ephemeral, auto-exiting actions-runner
// subprocesses tagged with this box's labels (GPU / vulkan aware). Outbound only —
// nothing listens for inbound. Completes the trifecta: `engine` serves models,
// `link` shares compute, `runner` claims CI — one binary, one org login.

import (
	"os/signal"
	"syscall"

	"github.com/hanzoai/cloud/runner"
	"github.com/spf13/cobra"
)

func newRunnerCmd(_ func() *Env, _ *globalFlags) *cobra.Command {
	runner.Version = Version // propagate the cloud build version into the daemon
	var configPath string
	cmd := &cobra.Command{
		Use:   "runner",
		Short: "Run this machine as a JIT CI runner for your org (GitHub Actions)",
		Long: "Turn this box into an ephemeral, GPU-aware GitHub Actions runner for your\n" +
			"org(s). A GitHub App polls for queued jobs and spawns auto-exiting runner\n" +
			"subprocesses labelled with this machine's tags. Outbound-only; nothing\n" +
			"listens for inbound. Config defaults to ~/.arcd/config.yaml (app_id, orgs,\n" +
			"labels, runner_dir).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := runner.LoadConfig(configPath)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return runner.RunHost(ctx, cfg)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "runner config.yaml (default: ~/.arcd/config.yaml)")
	return cmd
}
