package cli

// bot.go — `hanzo bot`: launch a computer-using agent (a "bot").
//
// A bot is the COMPUTER-USING flavor of an agent: it quick-boots a terminal or
// desktop sandbox on the operative stack (Hanzo's computer-use runtime — noVNC
// desktop, shell, browser, tools), provisioned by visor, and drives that machine
// to do the task. The headless flavor (no computer) is `hanzo agent` (agent.go).
//
//	hanzo bot run "<task>"            → boot a desktop bot, run the task
//	hanzo bot run --terminal "<task>" → boot a terminal-only bot
//
// Thin client over the IAM token + env.CloudURL (reuses cloudCall, run.go). The
// operative runtime + visor-provisioned machine do the work; the CLI only starts
// the run and reports the live session URL. See docs/architecture/compute-ladder.md.

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// BotRunReq is the boot-a-computer-using-agent body. Surface selects the sandbox
// the operative runtime drives: "desktop" (noVNC GUI) or "terminal" (shell only).
type BotRunReq struct {
	Task    string `json:"task"`
	Surface string `json:"surface"` // desktop | terminal
	GPU     bool   `json:"gpu,omitempty"`
	Timeout string `json:"timeout,omitempty"`
}

// BotRunResult carries the live session: a URL to watch/attach (noVNC / terminal)
// plus the run id.
type BotRunResult struct {
	RunID      string `json:"runId"`
	Status     string `json:"status"`
	SessionURL string `json:"sessionUrl"`
}

func newBotCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bot",
		Short: "Launch a computer-using agent (booted desktop or terminal)",
		Long: "Quick-boot a terminal or desktop sandbox on the operative computer-use\n" +
			"runtime (visor-provisioned) and have an agent drive it to do a task —\n" +
			"metered like any run. For a headless task agent, use `hanzo agent`.",
	}
	cmd.AddCommand(newBotRunCmd(envOf))
	return cmd
}

func newBotRunCmd(envOf func() *Env) *cobra.Command {
	var req BotRunReq
	var terminal bool
	c := &cobra.Command{
		Use:   "run <task>",
		Short: "Boot a computer-using bot and run the task",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env := envOf()
			req.Task = strings.Join(args, " ")
			req.Surface = "desktop"
			if terminal {
				req.Surface = "terminal"
			}
			out := &BotRunResult{}
			if err := cloudCall(cmd.Context(), env, http.MethodPost, "/v1/bots/run", &req, out); err != nil {
				return err
			}
			return env.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "%s\t%s\t%s\n", out.RunID, out.Status, out.SessionURL)
			})
		},
	}
	c.Flags().BoolVar(&terminal, "terminal", false, "boot a terminal-only sandbox (default: desktop)")
	c.Flags().BoolVar(&req.GPU, "gpu", false, "boot on a GPU machine")
	c.Flags().StringVar(&req.Timeout, "timeout", "", "max wall-clock (e.g. 30m)")
	return c
}
