package coding

import (
	"cmp"
	"encoding/json"
	"github.com/hanzoai/cloud/apps/agents"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// An Enso id is the one a run can always be served: when the org that pays has
// no balance the gateway answers it on the tier's no-cost rungs instead of
// refusing it with a 402, so a run of an org with no credit still finishes,
// where a paid-only id ends the run at its first model call. A run names ONE
// gateway id for the whole session; the gateway decides what serves it.
// Which one is a flag, not a build: setting coding_model moves the next run to
// another model, with no deploy. The 402 retry is not that flag. It runs once,
// on codingFallback, and only when the model that was asked refused for money.
const (
	codingModelKey = "coding_model"
	codingModel    = "enso-pro"
	codingFallback = "enso-auto"
)

// argvFor is the harness's command line, as argued above; the task is not in it
// (turn writes it to stdin). ask is the run's ask
// server (key.go), or "" for a run that has none; model is the agent's model, ""
// for codingModel; effort is its reasoning effort, "" for the model's own. dev
// and codex take both in one spelling (-m, and model_reasoning_effort as a -c
// override); codex is given a model only when one was asked for, because its
// own default is its own. Each agent harness is handed it as an MCP server named `run` whose one tool is ask_user,
// in its own spelling — dev and codex as `-c` overrides, claude as --mcp-config —
// and in every spelling the ticket is NAMED, never written: the header's value is
// the HANZO_RUN variable keyed puts in the environment. The wait allowed is the
// server's own plus a minute, so the harness never gives up on an answer the
// server is still waiting for. An interpreter has no MCP client and is given none.
//
// servers are the org's own MCP servers (kit.go), handed over beside `run` in
// the same spellings and on the same rule — each is the run's own address for
// that server (relayURL), and the header sent to it is the run's ticket, NAMED
// and never written. A server's own address and credential are never here: the
// tool app adds the credential and dials the server. Each carries exactly the
// tools of it an admin of the org activated, and the harness is given those and
// no others: claude may call only them unasked (--allowedTools names each), and
// dev and codex register only them (enabled_tools) — the dev harness runs every
// tool it registers without asking anyone.
func argvFor(tool, ask, model, effort string, servers ...Server) []string {
	thinking := thought(effort)
	switch strings.TrimSpace(tool) {
	case "claude":
		argv := []string{"claude", "-p"}
		if ask != "" {
			argv = append([]string{"env", "MCP_TOOL_TIMEOUT=" + strconv.Itoa(int(askTimeout/time.Millisecond))}, argv...)
		}
		if config, allowed := mcpConfig(ask, servers); config != "" {
			argv = append(argv, "--mcp-config", config, "--allowedTools", allowed)
		}
		return argv
	case "codex":
		argv := []string{"codex", "exec", "--json", "--output-last-message", answerPath}
		if model != "" {
			argv = append(argv, "-m", model)
		}
		return append(slices.Concat(argv, thinking, served(ask, servers)), "--", "-")
	case "python":
		return []string{"python3", "-"}
	case "node":
		return []string{"node", "-"}
	default:
		return append(devExec(ask, model, effort, servers), "--", "-")
	}
}

// thought is a harness's reasoning effort as dev and codex take it, nothing for
// the model's own.
func thought(effort string) []string {
	if effort == "" {
		return nil
	}
	return []string{"-c", `model_reasoning_effort="` + effort + `"`}
}

// devContextWindow unlocks the full 1M token context window for custom and
// gateway model ids (enso-pro, enso-ultra, enso-auto), matching hanzoai/gen
// (src/dev.rs) so codex-rs does not clamp them to its 272k fallback window.
const devContextWindow = "model_context_window=1000000"

// devExec is dev's non-interactive command up to the turn it is given: a new
// one (argvFor, for both dev and gen) or one more in a conversation it kept (resumeFor).
//
// No plan flag. dev 0.6 registered update_plan only behind
// --include-plan-tool while its instructions promised the tool, so a model
// called it, was answered "unsupported call" and looped (sess_a3ca6bb3…,
// 2026-10-26). dev 0.7 (sandbox 1.3.9) has no such flag and refuses it
// (`unexpected argument '--include-plan-tool'`, exit 2), so every turn passing
// it would end at argv parsing.
func devExec(ask, model, effort string, servers []Server) []string {
	argv := []string{"dev", "exec", "-m", cmp.Or(model, codingModel), "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check",
		"--json", "--output-last-message", answerPath, "-c", devContextWindow}
	return slices.Concat(argv, thought(effort), served(ask, servers))
}

// resumeFor is one more dev turn in the conversation thread, which reads what it
// is told on stdin (`-`), as a first turn does. thread is a UUID the caller
// checked (threadRE).
func resumeFor(thread, ask, model, effort string, servers ...Server) []string {
	return append(devExec(ask, model, effort, servers), "resume", "--", thread, "-")
}

// cleanDeveloperInstructions normalizes line endings and strips control runes
// TOML basic strings and argv reject, while keeping newlines and tabs intact.
func cleanDeveloperInstructions(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return truncateUTF8(strings.TrimSpace(s), maxDeveloperInstructionsLen)
}

// withDeveloperInstructions injects per-run developer instructions into argv
// outside the user prompt so turn-1 messages stay clean and prompt caches hold.
func withDeveloperInstructions(tool string, argv []string, devInst string) []string {
	clean := cleanDeveloperInstructions(devInst)
	if clean == "" {
		return argv
	}
	switch strings.TrimSpace(tool) {
	case "claude":
		return append(slices.Clone(argv), "--append-system-prompt", clean)
	case "python", "node":
		return argv
	default:
		flag := []string{"-c", "developer_instructions=" + quoted(clean)}
		at := slices.Index(argv, "resume")
		if at < 0 {
			at = slices.Index(argv, "--")
		}
		if at < 0 {
			return append(slices.Clone(argv), flag...)
		}
		out := make([]string, 0, len(argv)+len(flag))
		out = append(out, argv[:at]...)
		out = append(out, flag...)
		out = append(out, argv[at:]...)
		return out
	}
}

// askTimeout is how long a harness lets ask_user take: the server's own wait and
// a minute more, so the answer, or the server's word that none came, arrives.
const askTimeout = agents.AskWait + time.Minute

// served is the run's MCP servers as dev and codex are told about them: the ask
// server, then the org's, each a `-c` override under mcp_servers.<name> whose
// env_http_headers sends the run's ticket from HANZO_RUN.
func served(ask string, servers []Server) []string {
	var out []string
	if ask != "" {
		out = append(out,
			"-c", `mcp_servers.run.url="`+ask+`"`,
			"-c", `mcp_servers.run.env_http_headers={"`+TicketHeader+`"="HANZO_RUN"}`,
			"-c", "mcp_servers.run.tool_timeout_sec="+strconv.Itoa(int(askTimeout/time.Second)))
	}
	for _, s := range servers {
		out = append(out,
			"-c", "mcp_servers."+s.Name+".url="+quoted(relayURL(s.ID)),
			"-c", "mcp_servers."+s.Name+`.env_http_headers={"`+TicketHeader+`"="HANZO_RUN"}`)
		tools := make([]string, 0, len(s.Tools))
		for _, t := range s.Tools {
			tools = append(tools, quoted(t))
		}
		out = append(out, "-c", "mcp_servers."+s.Name+".enabled_tools=["+strings.Join(tools, ",")+"]")
	}
	return out
}

// mcpServer is one entry of claude's --mcp-config.
type mcpServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// mcpConfig is the run's MCP servers as claude is told about them, and the tools
// it may call without a person to approve them: ask_user, and each tool of each
// server an admin of the org activated, one by one — never a whole server. Every
// server is the run's own address, sent the ticket as `${HANZO_RUN}`, which
// claude expands from its environment. Empty when there is no server at all.
func mcpConfig(ask string, servers []Server) (string, string) {
	all := map[string]mcpServer{}
	var allowed []string
	if ask != "" {
		all[askServer] = mcpServer{Type: "http", URL: ask, Headers: map[string]string{TicketHeader: "${HANZO_RUN}"}}
		allowed = append(allowed, "mcp__"+askServer+"__"+agents.AskTool.Name)
	}
	for _, s := range servers {
		all[s.Name] = mcpServer{Type: "http", URL: relayURL(s.ID), Headers: map[string]string{TicketHeader: "${HANZO_RUN}"}}
		for _, t := range s.Tools {
			allowed = append(allowed, "mcp__"+s.Name+"__"+t)
		}
	}
	if len(all) == 0 {
		return "", ""
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": all})
	return string(b), strings.Join(allowed, ",")
}

// quoted is s as a TOML basic string, which JSON's string escapes also spell.
// Every string that reaches it has been checked for control characters — a
// server id and a tool name by their grammars (kit.go) — which TOML would refuse.
func quoted(s string) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// askLine is what an agent harness is told about ask_user, ahead of its task.
const askLine = "When a decision needs the person you are working for, call ask_user with the options " +
	"rather than guessing or stopping.\n\n"

// shellLine is what dev's model is told about its shell tool, which takes an
// argument list and runs it without a shell. Measured: told nothing, enso-pro
// wrote files as ["cat", ">", "path", "<<", "'EOF'", …] — cat was handed ">" as
// a file name — and repeated it until the turn was stopped (sess_2ef3fb9d…, and
// again in a fresh run on 2026-10-26); told this, it wrote every file through
// bash -lc on the first try.
const shellLine = "Your shell tool runs `command` as an argument list, without a shell: redirection, pipes, heredocs and && " +
	`reach the program as literal arguments. To use them, pass ["bash", "-lc", "<script>"].` + "\n"

// deliverLine is what an agent of a run that pushes is told about its work
// leaving the sandbox. Told nothing, it tries: a measured run spent its last
// turns on `git push` (no credential is in its environment, by design) and `gh`
// (not installed), then answered that its push had failed — while the platform
// pushed its commit and opened the pull request a second later.
const deliverLine = "Commit your work locally in small, atomic, well-described git commits (`git add <files> && git commit -m \"<type>(<scope>): <summary>\"`) " +
	"as each logical step is verified, so the pull request reads as a clean, reviewable series that is easy to understand and cherry-pick. " +
	"Keep source files modular and under 1,000 lines — split growing files along clear boundaries rather than building monoliths. " +
	"Do not run `git push` or `gh` yourself: when you finish, the platform pushes this branch and opens or updates the pull request.\n\n"

// directLine is prepended when the task explicitly tells the agent not to search
// or explore, or names the exact file paths to edit. Measured in production
// : given exact file paths and "do not
// search", the model still ran grep repeatedly until the duplicate-call guard
// stopped the turn.
const directLine = "The task names the exact files to change or asks you not to search. " +
	"Read and edit the named files directly — do not run grep, rg or find across the repository, " +
	"and never repeat a search command.\n\n"

// resumedLine tells an agent resuming after a platform restart to inspect the
// checkout before redoing work an earlier turn already wrote.
const resumedLine = "The platform restarted while an earlier turn of this run was working in this checkout. " +
	"Check git status and git diff to see what is already done in the tree, and finish the task:\n\n"

// continuingLine tells an agent continuing after a usage limit stopped its run that its
// earlier work is on this branch, in a checkout that holds nothing else of the run.
const continuingLine = "This run is continuing after a usage limit stopped it. Its earlier work is on this branch: " +
	"read git log and git diff to see what is done, and finish the task:\n\n"

// shallowLine is what an agent working in a clone is told about it: cloneArgv
// takes one commit, and a `git fetch` without --depth then negotiates the whole
// history of what it names. On a large repository that is minutes of a run that
// looks hung, then a loop of waits on it (loop.go waitHint).
const shallowLine = "This checkout is a shallow clone (depth 1) with no git or gh credential. Give every git fetch a depth " +
	"(`git fetch --depth=50 origin <ref>`); one without it walks past the shallow boundary and downloads the whole history. " +
	"Public GitHub pull requests, diffs (`.diff` / `.patch`), issues and comments can be read directly from https://github.com and https://api.github.com without a token.\n\n"

// partialLine is what an agent working in a treeless clone is told: it holds every commit and fetches
// a directory or a file when git first needs it, and git has a credential that can read this
// repository and cannot push, so a branch can be fetched and merged. The platform pushes the run's
// commits itself.
const partialLine = "This checkout is a partial clone with every commit and no file you have not read yet, and git here can read " +
	"this GitHub repository: `git fetch origin main` and `git merge origin/main` work. It cannot push: your commits are pushed for you when the run ends. " +
	"There is no gh credential. Public GitHub pull requests, diffs (`.diff` / `.patch`), issues and comments can be read directly from https://github.com and https://api.github.com without a token.\n\n"

var namedFileRE = regexp.MustCompile(`(?:^|[\s` + "`" + `"'(])([a-zA-Z0-9_.-]+/[a-zA-Z0-9_./-]+\.(?:go|ts|tsx|js|jsx|py|rs|md|sql|json|yaml|yml))\b`)

func forbidsExploration(prompt string) bool {
	low := strings.ToLower(prompt)
	for _, phrase := range []string{
		"don't explore", "do not explore",
		"don't search", "do not search",
		"don't grep", "do not grep",
		"no grep", "without searching",
	} {
		if strings.Contains(low, phrase) {
			return true
		}
	}
	return namedFileRE.MatchString(prompt)
}
