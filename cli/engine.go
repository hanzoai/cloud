package cli

// engine.go — `hanzo engine install|serve|status`: manage a local hanzo-engine
// (the `hanzoai` OpenAI + Anthropic model server) on THIS machine.
//
// One source of truth for install logic: `install` runs the SAME install.sh /
// install.ps1 the `curl … | sh` one-liner uses (it downloads the prebuilt,
// cosign-signed binary from the latest github.com/hanzoai/engine release), so the
// CLI never re-implements platform detection or verification. `serve` launches the
// installed binary (`hanzoai --port P run -m MODEL`); `status` probes it, reusing
// the same /v1/models probe `hanzo gpu connect --serve-engine` advertises with.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"
)

const (
	installScriptSh = "https://raw.githubusercontent.com/hanzoai/engine/main/install.sh"
	installScriptPS = "https://raw.githubusercontent.com/hanzoai/engine/main/install.ps1"
	engineBinName   = "hanzoai"
)

func newEngineCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "engine",
		Short: "Run a local hanzo-engine (OpenAI + Anthropic model server)",
		Long: "Install, serve, and inspect a local hanzo-engine on this machine.\n" +
			"`install` downloads the prebuilt, signed `hanzoai` binary; `serve` runs it on\n" +
			":1234; `status` shows whether it is up and which models it serves.",
	}

	// install ----------------------------------------------------------------
	var version, dir string
	install := &cobra.Command{
		Use:   "install",
		Short: "Download + install the prebuilt hanzoai binary (runs the canonical install script)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEngineInstall(cmd, version, dir)
		},
	}
	install.Flags().StringVar(&version, "engine-version", "", "install a specific release tag (default: latest)")
	install.Flags().StringVar(&dir, "dir", "", "install directory (default: /usr/local/bin or ~/.local/bin)")

	// serve ------------------------------------------------------------------
	var model string
	var port int
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve a model with the local hanzoai binary (hanzoai --port P run -m MODEL)",
		Long: "Launch the installed hanzoai server. Any args after `--` are passed through to\n" +
			"the underlying `hanzoai … run` invocation (e.g. `-- --max-seqs 32`).",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, extra []string) error {
			return runEngineServe(cmd, model, port, extra)
		},
	}
	serve.Flags().StringVarP(&model, "model", "m", "Qwen/Qwen3-4B", "model to serve (HF repo id or local path)")
	serve.Flags().IntVarP(&port, "port", "p", 1234, "port to serve the OpenAI + Anthropic API on")

	// status -----------------------------------------------------------------
	var url string
	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether a local hanzo-engine is up and what it serves",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEngineStatus(cmd, envOf(), firstNonEmpty(url, defaultEngineURL))
		},
	}
	status.Flags().StringVar(&url, "url", defaultEngineURL, "local engine URL to probe (GET /v1/models)")

	cmd.AddCommand(install, serve, status)
	return cmd
}

// runEngineInstall shells out to the canonical install script so there is exactly
// one implementation of platform detection + signature verification.
func runEngineInstall(cmd *cobra.Command, version, dir string) error {
	env := os.Environ()
	if version != "" {
		env = append(env, "HANZOAI_VERSION="+version)
	}
	if dir != "" {
		env = append(env, "HANZOAI_INSTALL_DIR="+dir)
	}

	var sh *exec.Cmd
	if runtime.GOOS == "windows" {
		sh = exec.CommandContext(cmd.Context(), "powershell", "-NoProfile", "-Command",
			"irm "+installScriptPS+" | iex")
	} else {
		// curl … | sh — the exact one-liner documented in the README.
		sh = exec.CommandContext(cmd.Context(), "sh", "-c",
			"curl -fsSL "+installScriptSh+" | sh")
	}
	sh.Env = env
	sh.Stdin, sh.Stdout, sh.Stderr = os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := sh.Run(); err != nil {
		return fmt.Errorf("install script failed: %w", err)
	}
	return nil
}

// runEngineServe execs the installed hanzoai binary. On Unix it replaces this
// process (syscall.Exec) so signals + exit code flow straight through; on Windows
// it runs as a child with inherited stdio.
func runEngineServe(cmd *cobra.Command, model string, port int, extra []string) error {
	bin, err := findEngineBinary()
	if err != nil {
		return err
	}
	args := []string{bin, "--port", fmt.Sprintf("%d", port), "run", "-m", model}
	args = append(args, extra...)
	fmt.Fprintf(cmd.ErrOrStderr(), "→ %s\n", exec.Command(bin, args[1:]...).String())
	return execEngine(bin, args)
}

// runEngineStatus probes the local engine and prints (or JSON-emits) its state,
// reusing the same /v1/models probe the fleet advertisement uses.
func runEngineStatus(cmd *cobra.Command, env *Env, url string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 6*time.Second)
	defer cancel()

	adv := &engineAdvertisement{URL: url, APIs: []string{"openai", "anthropic"}}
	if models, perr := probeEngine(ctx, url); perr != nil {
		adv.Status = "unreachable"
	} else {
		adv.Status = "ready"
		adv.Models = models
	}
	bin, _ := findEngineBinary()

	return env.emit(map[string]any{"url": url, "status": adv.Status, "models": adv.Models, "binary": bin},
		func(out io.Writer) {
			fmt.Fprintf(out, "engine   %s — %s\n", adv.URL, describeEngine(adv))
			if len(adv.Models) > 0 {
				for _, m := range adv.Models {
					fmt.Fprintf(out, "  model  %s\n", m)
				}
			}
			if bin != "" {
				fmt.Fprintf(out, "binary   %s\n", bin)
			} else {
				fmt.Fprintln(out, "binary   not installed — run `hanzo engine install`")
			}
			if adv.Status != "ready" {
				fmt.Fprintf(out, "hint     start it with `hanzo engine serve -m Qwen/Qwen3-4B --port %s`\n",
					portOf(url))
			}
		})
}

// findEngineBinary locates the installed hanzoai binary: PATH first, then the
// directories install.sh / install.ps1 write to.
func findEngineBinary() (string, error) {
	name := engineBinName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	var dirs []string
	if runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			dirs = append(dirs, filepath.Join(la, "Hanzo", "bin"))
		}
	} else {
		dirs = append(dirs, "/usr/local/bin", filepath.Join(home, ".local", "bin"), filepath.Join(home, ".hanzo", "bin"))
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("hanzoai not found on PATH or in the default install dirs — run `hanzo engine install`")
}

func portOf(url string) string {
	// best-effort: pull the ":<port>" tail for the hint
	for i := len(url) - 1; i >= 0; i-- {
		if url[i] == ':' {
			return url[i+1:]
		}
	}
	return "1234"
}
