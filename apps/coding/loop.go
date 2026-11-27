package coding

// loop.go — an agent that repeats itself is told so, then a stronger model is
// told so, then the run stops and names what it repeated.
//
// The dev harness (dev 0.7.9, in sandbox 1.3.9) ends a turn the moment its
// model makes the same tool calls twice in a row and gets the same results both
// times (hanzoai/codex codex-rs/core/src/session/cycle.rs, `MARK`), failing it
// with loopMark and the calls. Stopping the turn is right; ending the run there is not. Three
// runs it ended on 2026-10-26 were each one correctable mistake from working:
// update_plan with its list sent as a string, a heredoc handed to `shell` as
// argv, one `ls` repeated on a large tree.
//
// So the run carries on in the same conversation (`dev exec resume <thread>`),
// told the call it repeated and what is wrong with its shape — never what the
// call printed, which is the repository's text and not ours to put in a turn of
// the person's:
//
//	first loop    the same model is told, once
//	second loop   the next tier (tiers) is told, once — only when the request
//	              named no model; one it named is the person's choice
//	third loop    the run fails, naming the call
//
// Each resume must come back in the conversation it named: dev 0.6.94 starts a
// new one, silently, for an id it does not know, and that turn would answer a
// nudge with no task. Every turn runs inside the run's own budget, so the
// breaker cannot hold a run open past its deadline.

import (
	"cmp"
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strings"
)

// loopMark is what dev's guard says when it stops a turn, ahead of the call the
// model made twice in a row with the same result.
const loopMark = "repeated identical tool-use cycle detected; refusing to continue after duplicate tool calls:"

// tiers is the model a looping run moves to: the next Enso tier up. A model not
// named here has no next tier — codingFallback (enso-auto) least of all: a run the
// gateway refused for money thinks with it from then on, loops included.
var tiers = map[string]string{
	"enso":       "enso-pro",
	"enso-flash": "enso-pro",
	"enso-pro":   "enso-ultra",
}

// threadRE is the shape of a dev conversation id, which a resume puts on argv.
// It is the harness's own value read back from its events, and it is checked
// anyway: an argument that is not a UUID is never handed to a resume.
var threadRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// nudgeClip bounds each quoted piece of a nudge: the call and what it returned.
const nudgeClip = 800

// repeat is the run's verdict on a turn its harness stopped for repeating
// itself: the model to tell next, and false when the run has been told enough.
// told is how many times this run has already been told; named is a model the
// request chose, which is never traded for another.
func repeat(model string, told int, named bool) (string, bool) {
	switch {
	case told == 0:
		return model, true
	case told == 1:
		if named {
			return model, true
		}
		next, ok := tiers[model]
		return next, ok
	}
	return "", false
}

// call is the first tool call in a repeat: its name and its arguments as the
// model sent them. dev names the call as JSON — {"type":"function_call","name":
// …,"arguments":"<json>"} — and a local shell call carries an "action" instead.
func call(repeated string) (name, args string) {
	var c struct {
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     string          `json:"input"`
		Action    json.RawMessage `json:"action"`
	}
	if json.NewDecoder(strings.NewReader(repeated)).Decode(&c) != nil {
		return "", repeated
	}
	switch {
	case c.Arguments != "":
		return c.Name, c.Arguments
	case c.Input != "":
		return c.Name, c.Input
	case len(c.Action) > 0:
		return cmp.Or(c.Name, "shell"), string(c.Action)
	}
	return c.Name, ""
}

// shown is a repeated call as a person reads it in a failure: its name and its
// arguments, scrubbed and then clipped — in that order, so a clip can never cut a
// secret into a piece the scrub no longer recognizes.
func shown(repeated string, scrub func(string) string) string {
	name, args := call(repeated)
	return lead(scrub(strings.TrimSpace(name+" "+args)), nudgeClip)
}

// nudge is what a looping model is told: the call it repeated (its own words,
// scrubbed then clipped), that repeating it changes nothing, and what is wrong
// with the call when its shape says so (hints). What the call printed is left
// out: it is the repository's text, and a nudge is a turn spoken as the person.
func nudge(repeated string, scrub func(string) string, forced ...bool) string {
	name, args := call(repeated)
	var b strings.Builder
	b.WriteString("You made the same tool call twice in a row and got the same result both times, so the harness stopped you. The call:\n\n")
	b.WriteString(shown(repeated, scrub))
	b.WriteString("\n\nMaking it again will get the same result. Do something different: another command, other arguments, " +
		"or another approach. If you cannot go on, stop and say plainly what is blocking you.")
	if len(forced) > 0 && forced[0] {
		b.WriteString("\n\nYou were already told this and repeated the call again. You MUST change course: a different command or different arguments, no exceptions. Repeating it again will end this run.")
	}
	for _, h := range hints(name, args) {
		b.WriteString("\n\n" + h)
	}
	return b.String()
}

// The shapes of call measured looping in production, each with what the
// model was missing.
const (
	shellHint = shellLine
	listHint  = "An argument that takes a list or an object is sent as a JSON array or object, never as a string holding one: " +
		`update_plan takes {"plan": [{"step": "…", "status": "in_progress"}]}.`
	searchHint = "Stop searching with grep, rg or find: you already have the search output from the earlier call. " +
		"Read and edit the target files directly instead of repeating the search."
	readHint = "Stop re-reading the same file or lines with sed, head, tail, nl or git: their contents are already in the conversation above. " +
		"Edit the target file directly now (or move on to the next file) instead of reading the same slice again."
	// waitHint: polling a background command that is still running answers the
	// same thing every time, so the harness stops the turn. Measured 2026-11-04:
	// an unbounded `git fetch` of one branch into a depth-1 clone of ClickHouse
	// ran for minutes; the run waited on it twice, was stopped, was resumed,
	// waited on the same call again and failed.
	waitHint = "Never call `wait` on that `call_id` again: the harness ended the previous turn, so that background handle is gone and " +
		"waiting on it will fail the same way every time. If a child process (such as `git fetch`) is still running in the sandbox, kill it " +
		"and take a faster route. Name the one ref in a `git fetch`; in a shallow clone (depth 1) a fetch without `--depth` downloads the whole history, " +
		"so pass `--depth=50` (for example `git fetch --depth=50 origin <ref>`), or read the pull request or diff over https://api.github.com instead."
)

// shellSyntax are the argv elements that only a shell understands.
var shellSyntax = []string{">", ">>", "<", "<<", "<<<", "|", "||", "&&", ";", "2>&1", "&"}

// hints are what a call's own shape says is wrong with it: a shell script passed
// to an argv tool, a repeated search command, or a list sent as a string.
func hints(name, args string) []string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &fields) != nil {
		return nil
	}
	var out []string
	if name == "wait" {
		out = append(out, waitHint)
	}
	var argv []string
	if name == "shell" && json.Unmarshal(fields["command"], &argv) == nil && len(argv) > 0 {
		if !shelled(argv) {
			for _, a := range argv[1:] {
				if slices.Contains(shellSyntax, a) || strings.HasPrefix(a, "<<") || strings.HasPrefix(a, ">") {
					out = append(out, shellHint)
					break
				}
			}
		}
		if isSearchCommand(argv) {
			out = append(out, searchHint)
		}
		if isReadCommand(argv) {
			out = append(out, readHint)
		}
	}
	for _, k := range sortedKeys(fields) {
		var s string
		if json.Unmarshal(fields[k], &s) != nil {
			continue
		}
		s = strings.TrimSpace(s)
		if (strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{")) && json.Valid([]byte(s)) {
			out = append(out, listHint)
			break
		}
	}
	return out
}

// isSearchCommand reports whether argv invokes grep, rg, ripgrep or find.
func isSearchCommand(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch path.Base(argv[0]) {
	case "grep", "rg", "ripgrep", "find":
		return true
	case "git":
		return len(argv) > 1 && argv[1] == "grep"
	case "bash", "sh", "zsh":
		for _, a := range argv[1:] {
			for tok := range strings.FieldsSeq(a) {
				switch path.Base(tok) {
				case "grep", "rg", "ripgrep", "find":
					return true
				}
			}
		}
	}
	return false
}

// isReadCommand reports whether argv is a read-only inspection (sed -n, head,
// tail, nl, or git diff/show/log/status/blame) with no redirection or in-place
// edit flag.
func isReadCommand(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, ">") || strings.Contains(joined, "tee ") {
		return false
	}
	check := func(tokens []string) bool {
		for len(tokens) > 0 && strings.HasPrefix(tokens[0], "-") {
			tokens = tokens[1:]
		}
		if len(tokens) == 0 {
			return false
		}
		switch path.Base(tokens[0]) {
		case "head", "tail", "nl":
			return true
		case "cat":
			return !slices.ContainsFunc(tokens[1:], func(t string) bool {
				return strings.HasPrefix(t, ">") || strings.Contains(t, "<<")
			})
		case "sed":
			quiet := slices.Contains(tokens[1:], "-n") || slices.Contains(tokens[1:], "--quiet") || slices.Contains(tokens[1:], "--silent")
			return quiet && !slices.ContainsFunc(tokens[1:], func(t string) bool {
				return t == "-i" || strings.HasPrefix(t, "-i")
			})
		case "git":
			return len(tokens) > 1 && slices.Contains([]string{"diff", "show", "log", "status", "blame"}, tokens[1])
		}
		return false
	}
	if check(argv) {
		return true
	}
	if shelled(argv) {
		for _, a := range argv[1:] {
			if check(strings.Fields(a)) {
				return true
			}
		}
	}
	return false
}

// shelled reports whether argv already hands its script to a shell.
func shelled(argv []string) bool {
	switch path.Base(argv[0]) {
	case "bash", "sh", "zsh":
		return true
	}
	return false
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// lead keeps the start of s, which is where a call's name and a command's first
// complaint are.
func lead(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
