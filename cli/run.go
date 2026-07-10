package cli

// run.go — `hanzo run`: launch a workload on Hanzo compute through one verb.
//
// A "run" is a workload value = {artifact, shape}, dispatched by kind onto the
// one API host (api.hanzo.ai/v1) and the one binary (cloud):
//
//	hanzo run <image>              → POST /v1/run   container service (autoscaled)
//	hanzo run fn <src>             → POST /v1/fn    source function   (scale-to-zero)
//
// `run` launches YOUR artifact (container/source/site) on Hanzo compute. Invoking
// a managed agent with a task is a peer verb, `hanzo agent` (see agent.go), not a
// run kind. It is a THIN client: kind-dispatch + one authenticated HTTP call each,
// over the IAM access token and env.CloudURL, exactly like the fleet worker
// (gpu.go) — it invents no parallel API. See docs/architecture/compute-ladder.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// cloudCall performs one authenticated JSON request against env.CloudURL
// (api.hanzo.ai), decoding a 2xx body into out. It is the api.hanzo.ai analog of
// Platform.do (which targets platform.hanzo.ai); the two converge as the platform
// control plane folds into cloud. Mirrors the fleet worker's call idiom.
func cloudCall(ctx context.Context, env *Env, method, path string, body, out any) error {
	tok, err := env.ensureToken(ctx)
	if err != nil {
		return err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, env.CloudURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hanzo-cli/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("cloud %s %s: HTTP %d: %s", method, path, resp.StatusCode, serverMessage(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("cloud %s: decode response: %w", path, err)
		}
	}
	return nil
}

// RunSpec is the POST /v1/run (container) / /v1/fn (source) body. Shape selects
// the execution model: "service" (autoscaled, long-lived), "function"
// (per-request, scale-to-zero), or "task" (run-to-completion).
type RunSpec struct {
	Name    string            `json:"name,omitempty"`
	Image   string            `json:"image,omitempty"`  // container artifact (run)
	Source  string            `json:"source,omitempty"` // source ref/path (fn)
	Runtime string            `json:"runtime,omitempty"`
	Port    int               `json:"port,omitempty"`
	Shape   string            `json:"shape"` // service | function | task
	Min     int               `json:"minScale"`
	Max     int               `json:"maxScale"`
	GPU     bool              `json:"gpu,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// RunResult is the enqueue/creation acceptance.
type RunResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status string `json:"status"`
	Shape  string `json:"shape"`
}

func newRunCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Launch a workload on Hanzo compute (container, function, or agent task)",
		Long: "One verb, dispatched by kind, all on api.hanzo.ai/v1 — k8s is abstracted away:\n" +
			"  hanzo run <image>    a container service (autoscaled)\n" +
			"  hanzo run fn <src>   a source function (scale-to-zero)\n" +
			"To invoke a managed agent use `hanzo agent`; a computer-using one, `hanzo bot`.\n" +
			"For a declared, long-running or stateful service use `hanzo deploy`; for raw\n" +
			"cluster access use `hanzo k8s`.",
	}
	cmd.AddCommand(newRunContainerCmd(envOf), newRunFnCmd(envOf))
	return cmd
}

// newRunContainerCmd is `hanzo run <image>` (default) AND `hanzo run container <image>`.
func newRunContainerCmd(envOf func() *Env) *cobra.Command {
	var spec RunSpec
	var envKV []string
	c := &cobra.Command{
		Use:   "container <image>",
		Short: "Run a container as an autoscaled service (POST /v1/run)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env := envOf()
			spec.Image = args[0]
			spec.Shape = firstNonEmpty(spec.Shape, "service")
			spec.Env = parseEnvKV(envKV)
			out := &RunResult{}
			if err := cloudCall(cmd.Context(), env, http.MethodPost, "/v1/run", &spec, out); err != nil {
				return err
			}
			return env.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "%s\t%s\t%s\n", out.Name, out.Status, out.URL)
			})
		},
	}
	c.Flags().StringVar(&spec.Name, "name", "", "workload name (default: derived from image)")
	c.Flags().IntVar(&spec.Port, "port", 8080, "container port to route traffic to")
	c.Flags().StringVar(&spec.Shape, "shape", "service", "service | task (run-to-completion)")
	c.Flags().IntVar(&spec.Min, "min", 0, "min replicas (0 = scale-to-zero when supported)")
	c.Flags().IntVar(&spec.Max, "max", 20, "max replicas (autoscale ceiling)")
	c.Flags().BoolVar(&spec.GPU, "gpu", false, "schedule on a GPU node")
	c.Flags().StringArrayVarP(&envKV, "env", "e", nil, "environment variable KEY=VALUE (repeatable)")
	return c
}

func newRunFnCmd(envOf func() *Env) *cobra.Command {
	var spec RunSpec
	var envKV []string
	c := &cobra.Command{
		Use:   "fn <source>",
		Short: "Run a source function, scale-to-zero (POST /v1/functions)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env := envOf()
			spec.Source = args[0]
			spec.Shape = "function"
			spec.Env = parseEnvKV(envKV)
			out := &RunResult{}
			if err := cloudCall(cmd.Context(), env, http.MethodPost, "/v1/functions", &spec, out); err != nil {
				return err
			}
			return env.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "%s\t%s\t%s\n", out.Name, out.Status, out.URL)
			})
		},
	}
	c.Flags().StringVar(&spec.Name, "name", "", "function name")
	c.Flags().StringVar(&spec.Runtime, "runtime", "", "runtime env (e.g. python312, node20, rust)")
	c.Flags().BoolVar(&spec.GPU, "gpu", false, "use a GPU function environment")
	c.Flags().StringArrayVarP(&envKV, "env", "e", nil, "environment variable KEY=VALUE (repeatable)")
	return c
}

// parseEnvKV turns []{"K=V"} into a map, ignoring malformed entries.
func parseEnvKV(kv []string) map[string]string {
	if len(kv) == 0 {
		return nil
	}
	m := make(map[string]string, len(kv))
	for _, e := range kv {
		if i := strings.IndexByte(e, '='); i > 0 {
			m[e[:i]] = e[i+1:]
		}
	}
	return m
}
