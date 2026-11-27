package coding

// mode.go is what a run may do to its repository, and what its agent thinks with.
//
// A BUILD edits, commits and pushes its branch. A PLAN reads the repository and
// answers with a plan: it is handed a credential that can only read, no commit or
// push step runs, and its answer is the run's final status. The line is drawn by
// the credential and the runner, not by the prompt — a harness told to plan can
// still write to its checkout, and nothing it writes can leave.
//
// A SETUP is a plan put to one question — how is this codebase installed, started
// and checked — with the same credential and the same nothing-leaves rule. Its agent
// may install and run whatever it needs inside the sandbox, and its answer ends with
// the environment it found, which is kept as the codebase's proposal
// (environment.go).
//
// The model and the effort are the agent's: the dev and codex harnesses take a
// model (-m) and a reasoning effort (model_reasoning_effort), in one spelling.
// The others either think with a model of their own choosing or with none, so
// naming one for them is refused rather than dropped — a caller who asked for a
// model and was quietly given another would read the answer as that model's.

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	modeBuild = "build"
	modePlan  = "plan"
	modeSetup = "setup"
)

// answers reports whether a mode's result is its agent's answer rather than a
// pushed branch: a read-only credential, no commit, no push.
func answers(mode string) bool { return mode == modePlan || mode == modeSetup }

// efforts is the closed set of reasoning efforts a run may ask for.
var efforts = map[string]bool{"low": true, "medium": true, "high": true}

// modelRE is a model id as GET /v1/models spells one: alnum-led, so it can never
// read as a flag in the argv position it lands in.
var modelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// thinks reports whether a harness takes the model and the effort we name.
func thinks(tool string) bool {
	switch strings.TrimSpace(tool) {
	case "", "dev", "gen", "codex":
		return true
	}
	return false
}

// takes reports whether a run takes the model and the effort we name: a claimed
// machine runs its own harness with them, and here the dev, gen and codex harnesses do.
func takes(tool, target string) bool { return strings.TrimSpace(target) != "" || thinks(tool) }

// modeOf is the run's mode, the default spelled out.
func modeOf(mode string) string {
	if m := strings.TrimSpace(mode); m != "" {
		return m
	}
	return modeBuild
}

// checkAsk refuses a mode, model or effort the run cannot honour, and answers the
// three as the run will carry them.
func checkAsk(tool, target, mode, model, effort string) (string, string, string, error) {
	mode, model, effort = modeOf(mode), strings.TrimSpace(model), strings.TrimSpace(effort)
	switch mode {
	case modeBuild:
	case modePlan, modeSetup:
		if strings.TrimSpace(target) != "" {
			return "", "", "", fmt.Errorf("coding: a %s runs in our sandbox, where the credential it is handed can only read; "+
				"a claimed machine clones and pushes with its own, so a %s cannot be routed to one", mode, mode)
		}
		if !agentic(tool) {
			return "", "", "", fmt.Errorf("coding: a %s is an agent's answer, and %s runs the task as a program", mode, harness(tool))
		}
	default:
		return "", "", "", fmt.Errorf("coding: %q is not a mode — build, plan or setup", mode)
	}
	if model != "" && !modelRE.MatchString(model) {
		return "", "", "", fmt.Errorf("coding: %q is not a model id", model)
	}
	if effort != "" && !efforts[effort] {
		return "", "", "", fmt.Errorf("coding: %q is not an effort — low, medium or high", effort)
	}
	if (model != "" || effort != "") && !takes(tool, target) {
		return "", "", "", fmt.Errorf("coding: the %s harness does not take a model or an effort we name; the dev, gen and codex harnesses do", harness(tool))
	}
	return mode, model, effort, nil
}

// planLine is what a plan run's agent is told, ahead of its task.
const planLine = "Plan only. Read the repository and answer with a plan for the task below: what you would " +
	"change, where, and how you would check it. Do not edit files, commit or push — this run's " +
	"credential can only read, and nothing written here leaves the sandbox.\n\n"

// setupLine is what a setup run's agent is told, ahead of its task. The fenced
// block it ends with is what proposalOf reads; everything before it is the note.
const setupLine = "Set up this repository's development environment. Explore it, install what it needs, " +
	"start it if it is an app, and check that it works: run its tests or its build. Do not commit or push — " +
	"this run's credential can only read, and nothing written here leaves the sandbox. If it needs a secret, " +
	"name the environment variable it reads; never ask for, guess or print a secret's value. The install " +
	"commands you settle on run in every later fresh checkout before an agent starts, from the repository " +
	"root, so they must be non-interactive and safe to run again. End your answer with what you found and " +
	"checked, then exactly one fenced block tagged environment holding JSON, and nothing after it:\n\n" +
	"```environment\n{\"install\": \"<install commands>\", \"start\": \"<one long-running start command, or empty>\", " +
	"\"secrets\": [\"<ENV_VAR_NAME>\"]}\n```\n\n"

// planCap bounds a plan as the status event that carries it will ENCODE it: the
// session refuses an event over its payload limit, and a refused terminal event
// would close the run done with its plan — the whole product of a plan run —
// nowhere. JSON escaping makes a byte cost up to six, so the bound is on the
// encoded size, with room left for the event's other fields.
const planCap = 24 << 10

// planOf is a plan run's answer: what the harness printed to stdout, where the
// dev and codex harnesses put their final message, keeping the END when it is
// long, because the answer is the last thing an agent says. It is scrubbed by the
// caller before it is cut, so a credential split by the cut cannot survive it.
func planOf(stdout string) string {
	s := strings.ToValidUTF8(strings.TrimSpace(stdout), "\uFFFD")
	cost, i := 0, len(s)
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		if cost+encoded(r) > planCap-len(`"…"`) {
			return "…" + s[i:]
		}
		cost += encoded(r)
		i -= size
	}
	return s
}

// encoded is how many bytes encoding/json writes for r inside a string.
func encoded(r rune) int {
	switch {
	case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t':
		return 2
	case r < 0x20 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029':
		return 6
	}
	return utf8.RuneLen(r)
}
