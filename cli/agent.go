package cli

// agent.go — `hanzo agent`: invoke a managed Hanzo agent with a task.
//
// An agent is a managed capability (its own /v1/agents subsystem, self-metered),
// NOT your artifact — so it is a peer verb of `hanzo run`, not a run kind. This is
// the HEADLESS flavor: a task/tool/code agent, no computer. The computer-using
// flavor (a booted desktop/terminal) is `hanzo bot` (see bot.go).
//
//	hanzo agent run <ref> "<task>"  → POST /v1/agents/:ref/run
//
// Thin client: one authenticated call over the IAM token + env.CloudURL, reusing
// cloudCall (run.go). See docs/architecture/compute-ladder.md.

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// AgentRunReq is the POST /v1/agents/:ref/run body — a task for a managed agent.
type AgentRunReq struct {
	Task    string `json:"task"`
	GPU     bool   `json:"gpu,omitempty"`
	Timeout string `json:"timeout,omitempty"`
	Repo    string `json:"repo,omitempty"`
}

// AgentRunResult is the agent-run acceptance.
type AgentRunResult struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	URL    string `json:"url"`
}

func newAgentCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Invoke a managed Hanzo agent to run a task (headless)",
		Long: "Invoke a managed agent by ref (e.g. a coding or tool agent) to run a task on\n" +
			"Hanzo compute — metered per run. For a computer-using agent (booted desktop\n" +
			"or terminal), use `hanzo bot`.",
	}
	cmd.AddCommand(newAgentRunCmd(envOf))
	return cmd
}

func newAgentRunCmd(envOf func() *Env) *cobra.Command {
	var req AgentRunReq
	c := &cobra.Command{
		Use:   "run <ref> <task>",
		Short: "Run a task with the named agent (POST /v1/agents/:ref/run)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			env := envOf()
			ref := args[0]
			req.Task = strings.Join(args[1:], " ")
			out := &AgentRunResult{}
			if err := cloudCall(cmd.Context(), env, http.MethodPost, "/v1/agents/"+ref+"/run", &req, out); err != nil {
				return err
			}
			return env.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "%s\t%s\t%s\n", out.RunID, out.Status, out.URL)
			})
		},
	}
	c.Flags().BoolVar(&req.GPU, "gpu", false, "run the agent on a GPU node")
	c.Flags().StringVar(&req.Timeout, "timeout", "", "max wall-clock (e.g. 30m)")
	c.Flags().StringVar(&req.Repo, "repo", "", "a repo/checkout the agent operates on")
	return c
}
