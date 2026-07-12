package cli

// code.go — `hanzo code <agent> [model]`: launch a coding agent on the Hanzo AI
// cloud. ONE way to start ANY agent on ANY Hanzo model: the endpoint, the
// credential and the model are injected for you; nothing to export, nothing to
// remember.
//
//	hanzo code claude              # Claude Code on the default model
//	hanzo code codex deepseek-v4-pro
//	hanzo code dev glm5.2          # ids are resolved fuzzily: glm5.2 -> glm-5.2
//	hanzo code ls                  # what can I run?
//
// Two wire protocols cover all three agents: Claude Code speaks Anthropic,
// Codex and @hanzo/dev (a Codex fork) speak OpenAI — so an agent is just a
// binary + a wire + the flag that turns approvals off. Agents run full-auto by
// default (--safe restores prompting).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
)

// defaultCodeModel is the model `hanzo code <agent>` runs when no model is
// named: the virtual `best` model, which auto-routes to the best-available
// coding model by quality (glm-5.2 → deepseek-v4-pro → kimi-k2.6 → …) and
// cascades on the next rank when one is rate-limited, out of credit, or down
// (server-side, controllers/failover.go). Override per-invocation with an
// explicit id: `hanzo code claude glm5.2`.
const defaultCodeModel = "best"

// wire builds the env that points an agent's SDK at the Hanzo cloud.
type wire func(base, token, model string) map[string]string

func anthropicWire(base, token, model string) map[string]string {
	return map[string]string{
		"ANTHROPIC_BASE_URL":   base,
		"ANTHROPIC_AUTH_TOKEN": token,
		"ANTHROPIC_MODEL":      model,
	}
}

func openaiWire(base, token, _ string) map[string]string {
	return map[string]string{
		"OPENAI_BASE_URL": strings.TrimSuffix(base, "/") + "/v1",
		"OPENAI_API_KEY":  token,
	}
}

type codeAgent struct {
	bin      string   // executable to exec
	wire     wire     // how it finds the cloud
	fullAuto []string // flags that bypass approval prompts
	modelArg []string // how the model is passed on argv (empty: via env)
	provider func(base string) []string // agents that need the endpoint declared, not just env'd
	clear    []string // env that would shadow the wire (a stale key in the shell)
	install  string   // hint when the binary is missing
}

// codex and @hanzo/dev share a lineage (dev is a Codex fork), hence a wire.
// They also ignore OPENAI_BASE_URL and talk to chatgpt.com unless a provider is
// declared, so declare Hanzo as the provider and select it.
func codexLike(bin, install string) codeAgent {
	return codeAgent{
		bin:      bin,
		wire:     openaiWire,
		fullAuto: []string{"--dangerously-bypass-approvals-and-sandbox"},
		modelArg: []string{"-m"},
		provider: func(base string) []string {
			return []string{
				"-c", "model_provider=hanzo",
				"-c", `model_providers.hanzo.name="Hanzo"`,
				"-c", fmt.Sprintf(`model_providers.hanzo.base_url="%s/v1"`, strings.TrimSuffix(base, "/")),
				"-c", `model_providers.hanzo.env_key="OPENAI_API_KEY"`,
				"-c", `model_providers.hanzo.wire_api="responses"`,
			}
		},
		install: install,
	}
}

var codeAgents = map[string]codeAgent{
	"claude": {
		bin:      "claude",
		wire:     anthropicWire,
		fullAuto: []string{"--allow-dangerously-skip-permissions"},
		clear:    []string{"ANTHROPIC_API_KEY"}, // outranks AUTH_TOKEN: a stale one silently wins
		install:  "npm i -g @anthropic-ai/claude-code",
	},
	"codex": codexLike("codex", "npm i -g @openai/codex"),
	"dev":   codexLike("dev", "npm i -g @hanzo/dev"),
}

func newCodeCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "code <agent> [model] [-- args]",
		Short: "Launch a coding agent (claude, codex, dev) on a Hanzo cloud model",
		Long: "Run Claude Code, Codex, or @hanzo/dev against api.hanzo.ai with the endpoint,\n" +
			"credential and model injected — no env vars to remember. Model ids are resolved\n" +
			"fuzzily (glm5.2 -> glm-5.2) and agents run full-auto unless you pass --safe.",
		Example: "  hanzo code claude\n" +
			"  hanzo code codex deepseek-v4-pro\n" +
			"  hanzo code dev glm5.2 -- --resume\n" +
			"  hanzo code ls",
	}

	names := make([]string, 0, len(codeAgents))
	for name := range codeAgents {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		agent := codeAgents[name]
		cmd.AddCommand(&cobra.Command{
			Use:                name + " [model] [-- args]",
			Short:              "Launch " + name + " on a Hanzo model (default: " + defaultCodeModel + ")",
			DisableFlagParsing: true, // agent flags (--resume, -c …) pass straight through
			RunE: func(c *cobra.Command, args []string) error {
				return runCode(envOf(), agent, args)
			},
		})
	}

	cmd.AddCommand(&cobra.Command{
		Use:     "ls",
		Aliases: []string{"models", "list"},
		Short:   "List the models this account can run",
		Args:    cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			env := envOf()
			models, err := catalog(env)
			if err != nil {
				return err
			}
			for _, m := range models {
				fmt.Fprintln(env.out, m)
			}
			return nil
		},
	})
	return cmd
}

func runCode(env *Env, agent codeAgent, args []string) error {
	bin, err := exec.LookPath(agent.bin)
	if err != nil {
		return fmt.Errorf("%s is not installed — try: %s", agent.bin, agent.install)
	}
	token := codeToken(env)
	if token == "" {
		return fmt.Errorf("no Hanzo credential — run `hanzo login`, or set HANZO_API_KEY")
	}
	base := strings.TrimSuffix(firstNonEmpty(env.CloudURL, "https://api.hanzo.ai"), "/")

	// First non-flag arg is the model; --safe is ours; the rest is the agent's.
	model, safe, rest := "", false, make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case a == "--": // the separator is ours; the agent must not see it
		case a == "--safe" || a == "--ask":
			safe = true
		case model == "" && !strings.HasPrefix(a, "-") && len(rest) == 0:
			model = a
		default:
			rest = append(rest, a)
		}
	}
	if model == "" {
		model = defaultCodeModel
	}
	if model, err = resolveModel(env, model); err != nil {
		return err
	}

	argv := []string{agent.bin}
	if !safe {
		argv = append(argv, agent.fullAuto...)
	}
	if agent.provider != nil {
		argv = append(argv, agent.provider(base)...)
	}
	if len(agent.modelArg) > 0 { // claude takes the model via env, codex/dev on argv
		argv = append(argv, agent.modelArg...)
		argv = append(argv, model)
	}
	argv = append(argv, rest...)

	for _, k := range agent.clear {
		if err := os.Unsetenv(k); err != nil {
			return err
		}
	}
	for k, v := range agent.wire(base, token, model) {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	fmt.Fprintf(env.out, "%s → %s on %s\n", agent.bin, model, base)
	return execEngine(bin, argv) // exec: signals + exit code flow straight through
}

// codeToken resolves the credential the agents authenticate with: an explicit
// API key wins, then the stored key, then the key the rest of the Hanzo
// toolchain already keeps in ~/.hanzo/config.json, then the `hanzo login` token.
func codeToken(env *Env) string {
	return firstNonEmpty(os.Getenv("HANZO_API_KEY"), env.cfg.APIKey, storedAPIKey(), env.accessToken())
}

// storedAPIKey reads (never writes) the hk- key that hanzo-mcp and the rest of
// the toolchain share, so one `hanzo login`/key serves every tool.
func storedAPIKey() string {
	dir, err := hanzoDir()
	if err != nil {
		return ""
	}
	var cfg struct {
		APIKey string `json:"apiKey"`
	}
	if err := loadJSON(filepath.Join(dir, "config.json"), &cfg); err != nil {
		return ""
	}
	return cfg.APIKey
}

// resolveModel turns what you typed into an id the cloud serves. Exact ids pass
// through; otherwise ids are compared with punctuation and case removed, so
// "glm5.2" finds "glm-5.2". An unknown id lists the near misses rather than
// failing deep inside the agent.
func resolveModel(env *Env, want string) (string, error) {
	models, err := catalog(env)
	if err != nil {
		// Never hand an agent a model we could not confirm: it would fail deep
		// inside the session with an opaque error. Say so here instead.
		return "", fmt.Errorf("cannot read the model catalog: %w", err)
	}
	fold := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, s)
	}
	target := fold(want)
	var near []string
	for _, m := range models {
		switch f := fold(m); {
		case m == want, f == target:
			return m, nil
		case strings.Contains(f, target), strings.Contains(target, f):
			near = append(near, m)
		}
	}
	if len(near) == 1 {
		return near[0], nil
	}
	if len(near) > 1 {
		return "", fmt.Errorf("%q is ambiguous — did you mean: %s", want, strings.Join(near, ", "))
	}
	return "", fmt.Errorf("no model %q — run `hanzo code ls` to see what you can run", want)
}

func catalog(env *Env) ([]string, error) {
	base := strings.TrimSuffix(firstNonEmpty(env.CloudURL, "https://api.hanzo.ai"), "/")
	req, err := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+codeToken(env))
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s/v1/models: %s", base, resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}
