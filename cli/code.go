package cli

// code.go — `hanzo code <agent> [model]`: launch a coding agent on the Hanzo AI
// cloud. ONE way to start ANY agent on ANY Hanzo model: the endpoint, the
// credential and the model are injected for you; nothing to export, nothing to
// remember.
//
//	hanzo code claude              # Claude Code on the default model
//	hanzo code codex enso-pro
//	hanzo code dev zen5            # ids are resolved fuzzily: zen5pro -> zen5-pro
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

// A zenTier bridges a model id Claude Code recognizes (the carrier) to the zen
// alias api.hanzo.ai serves. CC sizes a model's context window and features only
// from ids it knows; an unknown id like "zen5-pro" gets a 128K budget and is
// rejected client-side above it — the "max context 131072" the server never
// actually imposes. Nothing lets us declare a custom model's window, so the
// carrier (a known 1M id, [1m] where needed) unlocks the budget and modelOverrides
// rewrites it back to zen before the request leaves the client — the carrier never
// reaches the server. Requires Claude Code v2.1.200+.
type zenTier struct {
	zen     string // served zen alias — the wire model api.hanzo.ai receives
	carrier string // recognized id CC budgets from; "" pins zen directly (fast tiers, never >128K)
	env     string // ANTHROPIC_DEFAULT_*_MODEL slot ("" = selectable, no slot)
	name    string // /model picker label (via _NAME, gateway-effective)
	desc    string // /model picker description (via _DESCRIPTION)
}

// slotID is the tier's wire value: its carrier, or the zen id when direct.
func (t zenTier) slotID() string {
	if t.carrier != "" {
		return t.carrier
	}
	return t.zen
}

// zenTiers is the ONE source of truth for the CC-tier ⇆ zen mapping — the wire
// env, picker branding, and modelOverrides all derive from it. Every zen id must
// be one api.hanzo.ai serves (TestZenTiersServeReal); zen5-mini/max/ultra are
// listed but not served, so fable maps to zen5-pro until zen5-max returns.
var zenTiers = []zenTier{
	{zen: "zen5-flash", env: "ANTHROPIC_DEFAULT_HAIKU_MODEL", name: "Zen5 Flash", desc: "Hanzo Zen5 Flash — fast, cheap tier"},
	{zen: "zen5", carrier: "claude-sonnet-4-6[1m]", env: "ANTHROPIC_DEFAULT_SONNET_MODEL", name: "Zen5", desc: "Hanzo Zen5 — frontier tier (1M context)"},
	{zen: "zen5-pro", carrier: "claude-opus-4-8[1m]", env: "ANTHROPIC_DEFAULT_OPUS_MODEL", name: "Zen5 Pro", desc: "Hanzo Zen5 Pro — deep reasoning (1M context)"},
	{zen: "zen5-pro", carrier: "claude-fable-5[1m]", env: "ANTHROPIC_DEFAULT_FABLE_MODEL", name: "Zen5 Pro (max effort)", desc: "Hanzo Zen5 Pro, top tier (1M context)"},
	{zen: "zen5-coder", carrier: "claude-sonnet-5", name: "Zen5 Coder", desc: "Hanzo Zen5 Coder — code-specialized (1M context)"},
}

// zenCarrier maps a resolved zen alias to the carrier CC budgets from; an unknown
// id passes through with CC's default budget.
func zenCarrier(model string) string {
	for _, t := range zenTiers {
		if t.zen == model {
			return t.slotID()
		}
	}
	return model
}

// stripModelSuffix drops the "[1m]" suffix so a carrier matches its override key.
func stripModelSuffix(id string) string {
	if i := strings.IndexByte(id, '['); i >= 0 {
		return id[:i]
	}
	return id
}

// claudeModelOverrides is the carrier→zen map for settings.json: CC budgets from
// the carrier key and sends the zen value on the wire. Direct tiers need no entry.
func claudeModelOverrides() map[string]string {
	m := make(map[string]string, len(zenTiers))
	for _, t := range zenTiers {
		if t.carrier != "" {
			m[stripModelSuffix(t.carrier)] = t.zen
		}
	}
	return m
}

// wire builds the env that points an agent's SDK at the Hanzo cloud.
type wire func(base, token, model string) map[string]string

// anthropicWire points Claude Code at the Hanzo cloud and pins each CC tier slot
// to its carrier (see zenTier), so subagents, the classifier, and /compact get
// the right context budget while modelOverrides rewrites carriers back to zen ids
// on the wire. `model` arrives already mapped to its carrier (runCode).
func anthropicWire(base, token, model string) map[string]string {
	env := map[string]string{
		"ANTHROPIC_BASE_URL":   base,
		"ANTHROPIC_AUTH_TOKEN": token,
		"ANTHROPIC_MODEL":      model,
	}
	for _, t := range zenTiers {
		if t.env == "" {
			continue
		}
		env[t.env] = t.slotID()
		env[t.env+"_NAME"] = t.name
		env[t.env+"_DESCRIPTION"] = t.desc
		// SMALL_FAST_MODEL is deprecated AND not rewritten by modelOverrides, so
		// it must hold a served zen id directly — mirror the (direct) haiku slot.
		if t.env == "ANTHROPIC_DEFAULT_HAIKU_MODEL" {
			env["ANTHROPIC_SMALL_FAST_MODEL"] = t.slotID()
		}
	}
	return env
}

func openaiWire(base, token, _ string) map[string]string {
	return map[string]string{
		"OPENAI_BASE_URL": strings.TrimSuffix(base, "/") + "/v1",
		"OPENAI_API_KEY":  token,
	}
}

type codeAgent struct {
	bin          string                            // executable to exec
	wire         wire                              // how it finds the cloud
	fullAuto     []string                          // flags that bypass approval prompts
	continueArgs []string                          // harness-native form of Hanzo -c/--continue
	modelArg     []string                          // how the model is passed on argv (empty: via env)
	carrier      func(model string) string         // maps the resolved model to a client-recognized id (claude: zen→carrier); nil = pass through
	provider     func(base, model string) []string // agents that need the endpoint declared, not just env'd
	clear        []string                          // env that would shadow the wire (a stale key in the shell)
	configHome   string                            // env var that relocates the agent's config dir to ~/.hanzo ("" = share the user's own install)
	seed         func(dir string) error            // one-time defaults for the isolated config dir
	appendSystem []string                          // --append-system-prompt + text; ALWAYS applied (identity, not a permission bypass — present in --safe too)
	mcp          bool                              // auto-wire the Hanzo MCP server (code/vector/web/vision tools) as an stdio server scoped to the cwd
	install      string                            // hint when the binary is missing
}

// codeContextWindow is the input context (tokens) the served coding model
// budgets from. The enso and zen5 flagship tiers serve 1M; the flash tiers
// serve 131072. Codex is told this so it sizes context to the real window
// instead of a flat 256K cap — the cause of "maximum context exceeded" at
// 262144 even on a 1M-capable model. This wrapper only launches Hanzo coding
// models (default zen5), so non-flash defaults to the 1M flagship window.
func codeContextWindow(model string) int {
	if strings.Contains(strings.ToLower(model), "flash") {
		return 131072
	}
	return 1000000
}

// codex and @hanzo/dev share a lineage (dev is a Codex fork), hence a wire.
// They also ignore OPENAI_BASE_URL and talk to chatgpt.com unless a provider is
// declared, so declare Hanzo as the provider and select it.
func codexLike(bin, install string) codeAgent {
	return codeAgent{
		bin:          bin,
		wire:         openaiWire,
		fullAuto:     []string{"--dangerously-bypass-approvals-and-sandbox"},
		continueArgs: []string{"resume", "--last"},
		modelArg:     []string{"-m"},
		provider: func(base, model string) []string {
			// api.hanzo.ai exposes the standard OpenAI /v1/models shape, not
			// Codex's private remote model-catalog schema, so skip that refresh
			// and supply the model's window here — sized to the SERVED model
			// (enso / zen5 flagship = 1M, flash tiers = 131072) so Codex budgets
			// the real context instead of a flat 256K cap (the "maximum context
			// exceeded at 262144" bug). Auto-compact at 90% leaves headroom.
			win := codeContextWindow(model)
			return []string{
				"-c", "model_provider=hanzo",
				"-c", `model_providers.hanzo.name="Hanzo"`,
				"-c", fmt.Sprintf(`model_providers.hanzo.base_url="%s/v1"`, strings.TrimSuffix(base, "/")),
				"-c", `model_providers.hanzo.env_key="OPENAI_API_KEY"`,
				"-c", `model_providers.hanzo.wire_api="responses"`,
				"-c", `features.remote_models=false`,
				"-c", fmt.Sprintf("model_context_window=%d", win),
				"-c", fmt.Sprintf("model_auto_compact_token_limit=%d", win*9/10),
			}
		},
		install: install,
	}
}

// zenIdentityPrompt is appended to Claude Code's base system prompt so a model
// served through the Hanzo cloud self-identifies as a Hanzo Zen model. It is an
// APPEND, not a replace: Claude Code keeps its base prompt (tool-use, safety,
// coding conventions); only the model's identity is Hanzo Zen. The model is
// served as a zen5 alias via api.hanzo.ai. Passed as --append-system-prompt,
// present in --safe too (identity is not a permission bypass).
const zenIdentityPrompt = "You are running through the Hanzo AI cloud as a Hanzo Zen model (the `zen5` capability tier, served via api.hanzo.ai). When asked what model or assistant you are, identify as a Hanzo Zen model. You are operating inside the Claude Code harness; keep its tool-use, safety, and coding conventions — only your identity is Hanzo Zen."

var codeAgents = map[string]codeAgent{
	"claude": {
		bin:          "claude",
		wire:         anthropicWire,
		fullAuto:     []string{"--dangerously-skip-permissions"},
		continueArgs: []string{"--continue"},
		// --model forces the session model on argv. Claude Code persists the
		// user's last /model selection (e.g. the reserved word "best"), and that
		// persisted choice OVERRIDES ANTHROPIC_MODEL — so the env var alone cannot
		// pin the model. --model is the per-session override that beats it. CC
		// budgets the context window from this (carrier) id, then rewrites it to
		// the zen alias via modelOverrides before the request leaves the client —
		// so api.hanzo.ai serves the zen id regardless of what /model last held.
		modelArg: []string{"--model"},
		// zen→carrier: hand CC a model it recognizes so it grants the full (1M)
		// context budget; claudeSettings' modelOverrides maps it back to zen.
		carrier: zenCarrier,
		// Stamp the identity: append the Hanzo Zen identity to CC's base prompt so
		// the served model says it is a Hanzo Zen model when asked. An append (not
		// --system-prompt) keeps CC's harness prompt intact; applied in --safe too.
		appendSystem: []string{"--append-system-prompt", zenIdentityPrompt},
		clear:        []string{"ANTHROPIC_API_KEY"}, // outranks AUTH_TOKEN: a stale one silently wins
		// Its own config home under ~/.hanzo, not the user's ~/.claude. Claude
		// Code and `hanzo code claude` are independent products: sharing one
		// mutable config braids them — the user's saved /model (e.g. "fable")
		// leaks in as the session identity, and hanzo's zen5 picks leak back out.
		// A separate home decomplects them; the injected zen5 slots then show
		// cleanly in /model instead of a stale saved value.
		configHome: "CLAUDE_CONFIG_DIR",
		seed:       seedClaudeConfig,
		// Auto-wire the Hanzo MCP server so `hanzo code claude` starts with the
		// Hanzo tool lattice (code search over the cloud index, web search, vision,
		// fs/exec/git) instead of a bare model. Resolved + injected in runCode.
		mcp:     true,
		install: "npm i -g @anthropic-ai/claude-code",
	},
	"codex": codexLike("codex", "npm i -g @openai/codex"),
	"dev":   codexLike("dev", "npm i -g @hanzo/dev"),
}

// defaultAgent is the agent `hanzo code` (no agent named) and bare `hanzo` launch:
// HANZO_CODE_TOOL, else the config `code_tool`, else dev — the Hanzo agent. An
// unknown value falls back to dev rather than failing, so a stale preference never
// blocks the default flow.
func defaultAgent(env *Env) codeAgent {
	name := firstNonEmpty(os.Getenv("HANZO_CODE_TOOL"), env.cfg.CodeTool)
	if a, ok := codeAgents[name]; ok {
		return a
	}
	return codeAgents["dev"]
}

func newCodeCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "code [agent] [model] [-- args]",
		Short: "Launch a coding agent (dev, claude, codex) on a Hanzo cloud model",
		Long: "Run @hanzo/dev, Claude Code, or Codex against api.hanzo.ai with the endpoint,\n" +
			"credential and model injected — no env vars to remember. `hanzo code` alone runs\n" +
			"dev (the Hanzo agent); name an agent to pick another. Model ids resolve fuzzily\n" +
			"(zen5pro -> zen5-pro), -c resumes either harness, and agents run full-auto unless\n" +
			"you pass --safe. Unknown options pass through; -- forces verbatim passthrough.",
		Example: "  hanzo code                 # dev, the default agent\n" +
			"  hanzo code claude\n" +
			"  hanzo code claude -c\n" +
			"  hanzo code codex -c\n" +
			"  hanzo code codex enso-pro\n" +
			"  hanzo code dev zen5 -- --resume\n" +
			"  hanzo code ls",
		// Bare `hanzo code` (or `hanzo code <model>/-- args` with no agent name) runs
		// the configured default agent (code_tool, else dev). A recognized agent name
		// (or `ls`) dispatches to its subcommand; anything else is the agent's args, so
		// `hanzo code glm5.2` works too.
		DisableFlagParsing: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) > 0 {
				if _, isAgent := codeAgents[args[0]]; isAgent || args[0] == "ls" {
					sub, _, err := c.Find(args)
					if err == nil && sub != c {
						sub.SetArgs(args[1:])
						return sub.Execute()
					}
				}
			}
			return runCode(envOf(), defaultAgent(envOf()), args)
		},
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

	// First non-flag arg before -- is the model; --safe and --continue are ours.
	// Unknown options pass through unchanged. Everything after -- belongs to the
	// agent, including positional subcommands and raw Codex -c config overrides.
	model, safe, continueLast, rest := splitCodeArgs(args)
	if model == "" {
		model = defaultCodeModel
	}
	if model, err = resolveModel(env, model); err != nil {
		return err
	}
	// Resolve the served zen id first (above), THEN hand the agent its carrier:
	// the id its client recognizes for context/feature budgeting. The zen id is
	// what api.hanzo.ai serves; the carrier is a client-side concern only.
	served := model
	if agent.carrier != nil {
		model = agent.carrier(model)
	}

	for _, k := range agent.clear {
		if err := os.Unsetenv(k); err != nil {
			return err
		}
	}
	// The isolated config dir is resolved BEFORE argv so the MCP config file can be
	// written into it (and cleaned with the rest of the session state).
	var configDir string
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
		configDir = dir
	}
	// Auto-wire the Hanzo MCP server (code search over the cloud index, web search,
	// vision, fs/exec/git) so the agent starts with the tool lattice, not a bare
	// model. Appended to the agent's own args; a missing server warns, never blocks.
	if agent.mcp && configDir != "" {
		cwd, _ := os.Getwd()
		if flags, warn := mcpArgs(configDir, cwd); warn != "" {
			fmt.Fprintf(env.out, "hanzo mcp: %s\n", warn)
		} else {
			rest = append(rest, flags...)
		}
	}

	argv := codeArgv(agent, base, model, safe, codeAgentRest(agent, continueLast, rest))

	for k, v := range agent.wire(base, token, model) {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	if served != model {
		fmt.Fprintf(env.out, "%s → %s (as %s) on %s\n", agent.bin, served, model, base)
	} else {
		fmt.Fprintf(env.out, "%s → %s on %s\n", agent.bin, model, base)
	}
	return execEngine(bin, argv) // exec: signals + exit code flow straight through
}

// splitCodeArgs pulls the launcher-owned tokens (the model, --safe, -c/--continue)
// out of the raw args; everything else is the agent's. The `--` separator is ours
// and switches on verbatim passthrough — every token after it goes to the agent
// untouched, including positional subcommands (`codex exec`) and raw Codex -c
// config overrides that would otherwise look like our --continue.
func splitCodeArgs(args []string) (model string, safe, continueLast bool, rest []string) {
	rest = make([]string, 0, len(args))
	passthrough := false
	for _, a := range args {
		switch {
		case passthrough:
			rest = append(rest, a)
		case a == "--": // the separator is ours; the agent must not see it
			passthrough = true
		case a == "--safe" || a == "--ask":
			safe = true
		case a == "-c" || a == "--continue":
			continueLast = true
		case model == "" && !strings.HasPrefix(a, "-") && len(rest) == 0:
			model = a
		default:
			rest = append(rest, a)
		}
	}
	return model, safe, continueLast, rest
}

// codeAgentRest prepends the agent's harness-native resume tokens when -c/--continue
// was given, so one Hanzo flag resumes the last session on either harness (`--continue`
// for Claude Code, `resume --last` for Codex/dev).
func codeAgentRest(agent codeAgent, continueLast bool, rest []string) []string {
	if !continueLast {
		return rest
	}
	args := make([]string, 0, len(agent.continueArgs)+len(rest))
	args = append(args, agent.continueArgs...)
	return append(args, rest...)
}

// codeArgv builds the final agent command line. Permission bypass is the
// launcher default for every agent; --safe is the single explicit opt-out.
func codeArgv(agent codeAgent, base, model string, safe bool, rest []string) []string {
	argv := []string{agent.bin}
	if !safe {
		argv = append(argv, agent.fullAuto...)
	}
	if agent.provider != nil {
		argv = append(argv, agent.provider(base, model)...)
	}
	if len(agent.modelArg) > 0 { // claude takes the model via env, codex/dev on argv
		argv = append(argv, agent.modelArg...)
		argv = append(argv, model)
	}
	argv = append(argv, agent.appendSystem...) // identity — applies in safe AND full-auto
	argv = append(argv, rest...)
	return argv
}

// mcpArgs resolves the Hanzo MCP server and returns the flags that attach it to a
// claude session, scoped to cwd. It ports the Rust CLI's resolve_mcp: prefer an
// installed `hanzo-mcp` on PATH, else `uvx hanzo-mcp` (ephemeral), else nothing —
// MCP is an enhancement, so a missing server never blocks the session. The Hanzo
// server is layered via --mcp-config, and --strict-mcp-config makes it the SOLE
// MCP source: a repo's own .mcp.json is NOT loaded (it can carry a hostile stdio
// server that would inherit the session's bearer). The config file is written into
// the isolated config dir so it is cleaned with the rest of the hanzo session state.
//
// Returns the argv flags to append (possibly empty) and a warning to surface when
// no server could be resolved, so the caller can tell the user tools are absent.
func mcpArgs(configDir, cwd string) (flags []string, warn string) {
	prog, args := resolveHanzoMCP(cwd)
	if prog == "" {
		return nil, "hanzo-mcp not found (install: `uv tool install hanzo-mcp`); launching without Hanzo tools"
	}
	cfg := mcpConfigJSON(prog, args)
	path := filepath.Join(configDir, "mcp.json")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		return nil, "could not write MCP config; launching without Hanzo tools"
	}
	// --strict-mcp-config: the Hanzo server is the ONLY MCP source (a repo
	// .mcp.json is ignored — it could ship a bearer-exfiltrating stdio server).
	return []string{"--mcp-config", path, "--strict-mcp-config"}, ""
}

// resolveHanzoMCP finds how to launch hanzo-mcp as an stdio server scoped to cwd:
// an installed console script first, else uv's ephemeral runner. Empty program =
// neither is on PATH.
func resolveHanzoMCP(cwd string) (prog string, args []string) {
	if p, err := exec.LookPath("hanzo-mcp"); err == nil {
		return p, []string{"--project-dir", cwd}
	}
	if p, err := exec.LookPath("uvx"); err == nil {
		return p, []string{"hanzo-mcp", "--project-dir", cwd}
	}
	return "", nil
}

// mcpConfigJSON is the --mcp-config document adding Hanzo's stdio server. Claude
// requires an explicit "type". Marshaled (not fmt'd) so the cwd/program are
// correctly escaped.
func mcpConfigJSON(prog string, args []string) string {
	doc := map[string]any{
		"mcpServers": map[string]any{
			"hanzo": map[string]any{
				"type":    "stdio",
				"command": prog,
				"args":    args,
				"env":     map[string]string{},
			},
		},
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// seedClaudeConfig writes first-run defaults into the isolated config dir so
// `hanzo code claude` starts clean: auto-approve, high effort, onboarding done,
// and NO pinned model (the zen5 slots come from the injected env, not saved
// state). It never overwrites — the user's own later edits in this dir persist.
func seedClaudeConfig(dir string) error {
	if err := upsertClaudeSettings(filepath.Join(dir, "settings.json")); err != nil {
		return err
	}
	return writeIfAbsent(filepath.Join(dir, ".claude.json"), "{\"hasCompletedOnboarding\":true}\n")
}

// claudeSettingsBase is the first-run settings.json for `hanzo code claude`:
// sensible agent defaults and no pinned model, so the env-injected carrier slots
// win. effortLevel is max — the deepest reasoning tier — matching the operator's
// /effort max session setting; zen folds it into the upstream's reasoning budget
// (anthropicThinkingBudget → normalizeReasoning) so it reaches the model. Applied
// only when settings.json does not yet exist — later user edits to these persist.
var claudeSettingsBase = map[string]any{
	"includeCoAuthoredBy":               false,
	"permissions":                       map[string]any{"defaultMode": "auto"},
	"skipAutoPermissionPrompt":          true,
	"skipDangerousModePermissionPrompt": true,
	"effortLevel":                       "max",
	"theme":                             "dark",
	"enableWorkflows":                   true,
}

// upsertClaudeSettings writes settings.json, (re)applying the operator-owned
// modelOverrides (carrier→zen) on EVERY launch while preserving the user's own
// edits to every other key. modelOverrides is policy, not preference: it must
// track the current zenTiers so Claude Code's context-budgeting carriers keep
// mapping to the served zen ids — so, unlike the base defaults, it is not
// write-once. A first run (or an unreadable/corrupt file) starts from the base.
func upsertClaudeSettings(path string) error {
	settings := map[string]any{}
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &settings) != nil || len(settings) == 0 {
		settings = map[string]any{}
		for k, v := range claudeSettingsBase {
			settings[k] = v
		}
	}
	overrides := make(map[string]any, len(zenTiers))
	for carrier, zen := range claudeModelOverrides() {
		overrides[carrier] = zen
	}
	settings["modelOverrides"] = overrides
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// writeIfAbsent creates path with content only when it does not already exist,
// so seeding a config dir never clobbers a user's later edits.
func writeIfAbsent(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// codeToken resolves the credential the agents authenticate with. Precedence:
// an explicit HANZO_API_KEY always wins (the operator's deliberate override),
// then the live `hanzo login` JWT, then the hk- API key chain.
//
// Why the JWT beats the hk- key: the JWT carries the caller's owner/project/sub
// claims verbatim, so the identity boundary mints a billing principal on EVERY
// deployment. The hk- key only mints a principal when the server can resolve it
// (iamKeys.resolve, which is a no-op without IAM_MINT_CLIENT_ID/SECRET) — on a
// deployment lacking that credential an hk- request arrives anonymous and zen's
// billing gate 402s ("a billable tenant is required"). Preferring a FRESH JWT
// keeps `hanzo code` working everywhere; the hk- key stays the fallback for
// mint-credentialed servers and the explicit-override case. freshAccessToken
// skips an expired token so it can't 401 a session a valid hk- key would serve.
func codeToken(env *Env) string {
	return firstNonEmpty(os.Getenv("HANZO_API_KEY"), env.freshAccessToken(), env.cfg.APIKey, storedAPIKey())
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
