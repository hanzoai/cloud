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
// named. It must be a catalog-served id with working tool calls: coding agents
// cannot operate on a text-only model even when its SSE transport is healthy.
// zen5 is the flagship GLM-5.2-class capability alias (1M ctx, tool-capable) —
// the frontier coding tier. Do not use the virtual `best`: Claude Code treats
// it (like `opus`/`sonnet`/`haiku`) as a reserved alias and rewrites it to a
// claude-* id that api.hanzo.ai does not serve. Override per invocation with an
// explicit id: `hanzo code claude zen5-pro`.
const defaultCodeModel = "zen5"

// wire builds the env that points an agent's SDK at the Hanzo cloud.
type wire func(base, token, model string) map[string]string

// anthropicWire builds the env that points Claude Code at the Hanzo cloud AND
// pins every CC model slot to a zen5 alias — the stable Hanzo capability
// contract — so CC never routes to a raw claude-* model. api.hanzo.ai does not
// serve the Anthropic ids CC defaults to (claude-haiku-*, claude-opus-*, …);
// a request for one 403s, which kills the permission classifier ("auto mode
// cannot determine safety"), every subagent, and /compact.
//
// zen5-* is the STABLE API contract. Each alias is a capability tier, not a
// model name — the backend maps each to the best upstream it serves (today, on
// DigitalOcean GenAI; tomorrow, whatever supersedes it). Clients never see the
// upstream: swap GLM for Qwen 3.6 or a future frontier and every SDK / CLI /
// Claude Code integration keeps working unchanged. The CC tier → zen5 alias
// map is fixed here; the zen5 → upstream map lives in models.yaml.
//
//	CC tier        zen5 alias    capability
//	──────────    ──────────    ─────────────────────────────
//	Haiku         zen5-flash    fast / cheap (classifier, quick tasks)
//	Sonnet        zen5           default frontier (GLM-5.2 class, 1M ctx)
//	Opus          zen5-pro      heavy reasoning (DeepSeek-V4 Pro class)
//	Fable         zen5-pro      premium frontier (ultra disabled; falls through to zen5)
//	main          <model>       the resolved id, default zen5-pro (see defaultCodeModel)
//
// The main slot takes a served, tool-capable id (default zen5-pro), never the
// virtual `best`: CC rewrites the reserved word `best` to a claude-* id that
// 403s. ANTHROPIC_MODEL alone is not enough — a persisted /model selection
// overrides it — so the claude agent also passes --model on argv (see
// codeAgents) to force the session model. The env vars below pin the four CC
// tier slots so the classifier, subagents, and /compact hit served zen5 SKUs.
func anthropicWire(base, token, model string) map[string]string {
	return map[string]string{
		"ANTHROPIC_BASE_URL":   base,
		"ANTHROPIC_AUTH_TOKEN": token,
		"ANTHROPIC_MODEL":      model,
		// The four CC tier slots are FIXED zen5 aliases — the stable contract.
		// Without these, CC falls back to its built-in claude-* ids and 403s
		// on every non-main call (subagents, the classifier, compaction).
		"ANTHROPIC_SMALL_FAST_MODEL":     "zen5-flash",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "zen5-flash",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "zen5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "zen5-pro",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "zen5-pro",
	}
}

func openaiWire(base, token, _ string) map[string]string {
	return map[string]string{
		"OPENAI_BASE_URL": strings.TrimSuffix(base, "/") + "/v1",
		"OPENAI_API_KEY":  token,
	}
}

type codeAgent struct {
	bin        string                     // executable to exec
	wire       wire                       // how it finds the cloud
	fullAuto   []string                   // flags that bypass approval prompts
	modelArg   []string                   // how the model is passed on argv (empty: via env)
	provider   func(base string) []string // agents that need the endpoint declared, not just env'd
	clear      []string                   // env that would shadow the wire (a stale key in the shell)
	configHome string                     // env var that relocates the agent's config dir to ~/.hanzo ("" = share the user's own install)
	seed       func(dir string) error     // one-time defaults for the isolated config dir
	install    string                     // hint when the binary is missing
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
		fullAuto: []string{"--dangerously-skip-permissions"},
		// --model forces the session model on argv. Claude Code persists the
		// user's last /model selection (e.g. the reserved word "best"), and that
		// persisted choice OVERRIDES ANTHROPIC_MODEL — so the env var alone cannot
		// pin the model. --model is the per-session override that beats it: CC
		// sends it as the request's model field verbatim, and api.hanzo.ai serves
		// the zen5 id. This is what makes `hanzo code claude` always run zen5
		// regardless of what the user last picked in /model.
		modelArg: []string{"--model"},
		clear:    []string{"ANTHROPIC_API_KEY"}, // outranks AUTH_TOKEN: a stale one silently wins
		// Its own config home under ~/.hanzo, not the user's ~/.claude. Claude
		// Code and `hanzo code claude` are independent products: sharing one
		// mutable config braids them — the user's saved /model (e.g. "fable")
		// leaks in as the session identity, and hanzo's zen5 picks leak back out.
		// A separate home decomplects them; the injected zen5 slots then show
		// cleanly in /model instead of a stale saved value.
		configHome: "CLAUDE_CONFIG_DIR",
		seed:       seedClaudeConfig,
		install:    "npm i -g @anthropic-ai/claude-code",
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

	argv := codeArgv(agent, base, model, safe, rest)

	for _, k := range agent.clear {
		if err := os.Unsetenv(k); err != nil {
			return err
		}
	}
	if agent.configHome != "" {
		dir, err := hanzoDir()
		if err != nil {
			return err
		}
		if agent.seed != nil {
			if err := agent.seed(dir); err != nil {
				return err
			}
		}
		if err := os.Setenv(agent.configHome, dir); err != nil {
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

// codeArgv builds the final agent command line. Permission bypass is the
// launcher default for every agent; --safe is the single explicit opt-out.
func codeArgv(agent codeAgent, base, model string, safe bool, rest []string) []string {
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
	return argv
}

// seedClaudeConfig writes first-run defaults into the isolated config dir so
// `hanzo code claude` starts clean: auto-approve, high effort, onboarding done,
// and NO pinned model (the zen5 slots come from the injected env, not saved
// state). It never overwrites — the user's own later edits in this dir persist.
func seedClaudeConfig(dir string) error {
	if err := writeIfAbsent(filepath.Join(dir, "settings.json"), claudeSettingsSeed); err != nil {
		return err
	}
	return writeIfAbsent(filepath.Join(dir, ".claude.json"), "{\"hasCompletedOnboarding\":true}\n")
}

// claudeSettingsSeed is the initial settings.json for `hanzo code claude`:
// sensible agent defaults and no model, so the env-injected zen5 slots win.
// effortLevel is max — the deepest reasoning tier — matching the operator's
// /effort max session setting; zen folds it into the upstream's reasoning
// budget (anthropicThinkingBudget → normalizeReasoning) so it reaches the model.
const claudeSettingsSeed = `{
  "includeCoAuthoredBy": false,
  "permissions": { "defaultMode": "auto" },
  "skipAutoPermissionPrompt": true,
  "skipDangerousModePermissionPrompt": true,
  "effortLevel": "max",
  "theme": "dark",
  "enableWorkflows": true
}
`

// writeIfAbsent creates path with content only when it does not already exist,
// so seeding a config dir never clobbers a user's later edits.
func writeIfAbsent(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o600)
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
