// Runner subprocess management. Each match (queued job whose labels fit
// this host) gets one runnerLauncher.Run() call: mint JIT config, exec
// the actions-runner binary with --jitconfig, wait for exit. Runner
// picks the one job, exits, auto-deregisters server-side.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type runnerLauncher struct {
	cfg    *Config
	gh     *ghClient
	logger *slog.Logger
	state  *daemonState
}

func newRunnerLauncher(cfg *Config, gh *ghClient, logger *slog.Logger, state *daemonState) *runnerLauncher {
	return &runnerLauncher{cfg: cfg, gh: gh, logger: logger, state: state}
}

// Run mints a JIT config for the given org and spawns ONE actions-runner
// subprocess with --jitconfig. Blocks until the runner exits.
func (r *runnerLauncher) Run(ctx context.Context, org string, repo Repo, job WorkflowJob) error {
	name := fmt.Sprintf("%s-%s-%s", r.cfg.HostName, org, randID(8))
	jit, err := r.gh.MintJITConfig(ctx, org, name, r.cfg.Labels, 1)
	if err != nil {
		return fmt.Errorf("mint jit: %w", err)
	}
	binary := r.runnerBinary()
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("runner binary %q: %w", binary, err)
	}
	if err := os.MkdirAll(r.cfg.WorkDir, 0o755); err != nil {
		return fmt.Errorf("mkdir work: %w", err)
	}

	r.state.StartJob(JobRecord{
		Org:          org,
		Repo:         repo.Name,
		WorkflowName: job.WorkflowName,
		JobName:      job.Name,
		JobID:        job.ID,
		RunnerName:   name,
		HTMLURL:      job.HTMLURL,
	})

	args := []string{"--jitconfig", jit.EncodedJITConfig}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = r.cfg.RunnerDir
	cmd.Env = append(sanitizedEnv(),
		"RUNNER_ALLOW_RUNASROOT=0",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_NOLOGO=1",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	r.logger.Info("runner.spawn",
		"org", org,
		"job", job.Name,
		"job_url", job.HTMLURL,
		"runner_name", name,
		"runner_id", jit.Runner.ID,
		"binary", binary,
	)
	if err := cmd.Start(); err != nil {
		r.state.EndJob(job.ID, true)
		return fmt.Errorf("start runner: %w", err)
	}
	err = cmd.Wait()
	if err != nil {
		// Non-zero exit is informational only — runner sometimes exits
		// non-zero when the job fails. Job failure is GitHub's concern.
		r.logger.Warn("runner.exit_nonzero", "org", org, "runner_name", name, "err", err.Error())
		r.state.EndJob(job.ID, true)
		return nil
	}
	r.logger.Info("runner.exit_ok", "org", org, "runner_name", name)
	r.state.EndJob(job.ID, false)
	return nil
}

func (r *runnerLauncher) runnerBinary() string {
	if r.cfg.RunnerBinary != "" {
		return r.cfg.RunnerBinary
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(r.cfg.RunnerDir, "run.cmd")
	default:
		return filepath.Join(r.cfg.RunnerDir, "run.sh")
	}
}

// sanitizedEnv is os.Environ() with secret-looking variables removed. The child
// actions-runner executes untrusted workflow code (especially with allow_forks),
// so the daemon must not hand it the operator's secrets via the environment.
// PATH/HOME and toolchain vars are preserved so builds still work — per-job
// secrets belong in GitHub Actions secrets, injected by the runner, not the
// daemon env.
func sanitizedEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		i := strings.IndexByte(kv, '=')
		if i >= 0 && secretEnvKey(kv[:i]) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// secretEnvKey reports whether an env var NAME looks like it holds a secret.
func secretEnvKey(key string) bool {
	k := strings.ToUpper(key)
	for _, sub := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "_KEY", "APIKEY", "API_KEY", "PRIVATE"} {
		if strings.Contains(k, sub) {
			return true
		}
	}
	for _, pre := range []string{"HANZO_", "GH_", "GITHUB_", "AWS_", "KMS_", "ARC_", "AZURE_", "GCP_", "GOOGLE_"} {
		if strings.HasPrefix(k, pre) {
			return true
		}
	}
	return false
}

func randID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "xxxxxxxx"
	}
	return hex.EncodeToString(b)
}
