package coding

// outcome.go — what a harness said it did, read from the events it wrote.
//
// The dev and codex harnesses run with --json: every event of their turn is a
// JSON line on stdout, which the sandbox narrates into the run's session as an
// `event` apiece (apps/sandbox work.go), so a surface renders the turn — its
// messages, its commands and their output, its edits, its usage — from the
// harness's own words. Their raw output is on stderr, and narrated as the log.
//
// The run's own result reads two facts from the same lines: the tokens its
// turns used, and the error it ended on when it failed. Its last message is
// written to a file (answerPath) as well, since a long turn's events can run
// past what a command's collected stdout keeps.

import (
	"bufio"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
)

// answerPath is where the harness writes its last message.
const answerPath = "/tmp/hanzo-run/answer"

// events reports whether a harness writes its turn as JSON lines.
func events(tool string) bool {
	switch strings.TrimSpace(tool) {
	case "", "dev", "gen", "codex":
		return true
	}
	return false
}

// outcome is what a harness's events say: its last message, the tokens its
// turns used, and the last error it reported. plain is output with no event in
// it at all, a harness that wrote none.
//
// thread is the conversation the turn began in — the first one it names — which
// a later turn resumes (loop.go); repeated is the tool call its model made twice
// in a row with the same result when the harness stopped the turn for that, ""
// when it stopped for any other reason.
type outcome struct {
	answer   string
	usage    Usage
	failed   string
	plain    bool
	items    int
	worked   bool
	thread   string
	repeated string
}

// empty reports a turn that came back with nothing: events written, and no item,
// no output token and no error among them. It is the model's empty answer, not a
// verdict that the task needs no change.
func (o outcome) empty() bool {
	return !o.plain && o.items == 0 && o.usage.Output == 0 && o.failed == ""
}

// settle is what a build that changed nothing is asked in its own conversation
// (sandboxrunner.go): to make the change it may only have announced, or to say in
// words settled reads that none is needed.
const settle = "Your turn ended and the repository is unchanged: no file was written. If the task needs a change, " +
	"make it now with your tools and end your turn only when it is in the working tree. If it needs none, answer with " +
	"one line that begins \"No change needed:\" and says why."

// noChangeRetryPrompt is sent on a fresh thread when a build turn changed nothing
// and cannot resume its prior conversation (e.g. a non-dev harness or a gone/missing
// thread): it tells the agent to actually make the edits, run tests, and leave the
// final changes in the working tree, or state why none is needed.
const noChangeRetryPrompt = "Your previous turn ended and the repository is unchanged: no file was written. " +
	"Check git status and git diff, actually make the required edits with your tools, run the relevant tests, " +
	"and leave your final changes in the working tree. If no change is needed, answer with one line that begins " +
	"\"No change needed:\" and says why.\n\n"

// threadRecoveryLine tells a fresh thread started after a harness thread/rollout
// crash to inspect the existing checkout so uncommitted edits are verified and kept
// rather than redone or lost.
const threadRecoveryLine = "An earlier turn in this checkout hit a harness thread/rollout error. " +
	"Check git status and git diff to see any uncommitted edits already in the working tree, " +
	"verify and complete the task, and leave all final changes in the working tree:\n\n"

// settled reports an answer that says the task needs no change, as settle asks:
// "No change needed: …", or "No changes are needed …", markdown and all.
// Leading markdown/quotes are stripped. The check is a PREFIX match on the
// normalized/lowercased answer. Additionally, a leading "I'll leave it as is:"
// style clause is accepted only when the remainder starts with "no change".
func settled(answer string) bool {
	a := strings.ToLower(strings.TrimLeft(strings.TrimSpace(normalizeApostrophes(answer)), "*_`#> "))
	if strings.HasPrefix(a, "i'll leave it as is:") {
		a = strings.TrimSpace(strings.TrimPrefix(a, "i'll leave it as is:"))
	}
	if !strings.HasPrefix(a, "no change") && !strings.HasPrefix(a, "nothing to change") {
		return false
	}
	first := a
	if i := strings.IndexAny(first, ".\n"); i >= 0 {
		first = first[:i]
	}
	return !strings.Contains(first, ", but")
}

// outOfBudget reports the harness's wrap-up for a conversation whose goal has spent
// its token budget. It is no answer to the task.
func outOfBudget(answer string) bool {
	a := strings.ToLower(answer)
	at := strings.Index(a, "token budget")
	return at >= 0 && strings.Contains(a[:at], "goal")
}

// finishTurn is what a build turn that stopped right after announcing its next
// step is told in its own conversation (hanzo-inc/cloud#525).
const finishTurn = "Your turn ended right after announcing your next step, without calling a tool. " +
	"Make the tool call you just announced now, and end your turn only when all edits and checks are complete."

// narrating reports an answer that announces the next step instead of finishing
// the task (hanzo-inc/cloud#525): a line ending with a colon ("Now let me run
// the tests:"), a first-person future announcement ("Now I'll…", "Let me…",
// "Next I will…"), or an imperative step ("Fix it to match.", "Run gofmt…").
func narrating(answer string) bool {
	s := strings.TrimSpace(answer)
	if s == "" || settled(s) {
		return false
	}
	if strings.HasSuffix(s, ":") {
		return true
	}
	// Normalize typographic apostrophes/quotes before matching so "I'll"/"I'm"
	// with U+2019 match the ASCII patterns.
	s = normalizeApostrophes(s)

	// Check for "; now i'm " pattern anywhere in the string (semicolon continuation)
	low := strings.ToLower(s)
	for _, p := range []string{
		"; now i'm ",
		"; now i will ",
	} {
		if strings.Contains(low, p) {
			return true
		}
	}

	// The last line, and the last sentence of it: "Go is being installed. Let me
	// wait for it." announces in its second sentence, on a line that begins with
	// a report.
	line := lastLine(s)
	for _, l := range []string{line, lastSentence(line)} {
		last := strings.ReplaceAll(strings.ToLower(strings.TrimLeft(l, "-*•0123456789.) `_>#")), "`", "")
		if strings.HasPrefix(last, "let me know") {
			continue
		}
		if reportsCompletedWork(last) {
			continue
		}
		for _, p := range []string{
			"let me ", "let's ", "now let me", "now let's",
			"now i'll ", "now i will ", "next i'll ", "next i will ", "next, i'll ", "next, i will ",
			"i'll now ", "i will now ",
			"fix it ", "run gofmt", "run go test", "run the test", "commit and push",
			// present-progressive and sequencing announcements from production failures
			"i'm ", "now i'm ", "i am now ", "i'm going to ",
			"then i'll ", ", then i'll ", ", then i will ",
			"before patching", "before editing", "now i'm patching", "i'm patching", "i'm reading",
			"i'll ", "i will ", "next, ",
		} {
			if strings.HasPrefix(last, p) {
				return true
			}
		}
	}
	lines := strings.Split(s, "\n")
	if len(lines) >= 2 {
		allSteps := true
		for _, ln := range lines {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			isBullet := strings.HasPrefix(ln, "- ") || strings.HasPrefix(ln, "* ") ||
				(len(ln) >= 3 && ln[0] >= '1' && ln[0] <= '9' && (ln[1] == '.' || ln[1] == ')') && ln[2] == ' ')
			body := strings.ReplaceAll(strings.ToLower(strings.TrimLeft(ln, "-*•0123456789.) `_>#")), "`", "")
			isAction := false
			for _, v := range []string{"check ", "add ", "update ", "run ", "fix ", "edit ", "create ", "write ", "remove ", "commit "} {
				if strings.HasPrefix(body, v) {
					isAction = true
					break
				}
			}
			if !isBullet || !isAction {
				allSteps = false
				break
			}
		}
		if allSteps {
			return true
		}
	}
	for _, p := range []string{
		"; next i will ", "; next i'll ",
		" and will now ", " and i'll now ",
		"once that is done, i'll ", "once that is done, i will ",
	} {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// reportsCompletedWork returns true if the last sentence indicates completed work,
// not a future announcement. Used to avoid false positives in narrating().
func reportsCompletedWork(sentence string) bool {
	low := strings.ToLower(sentence)
	for _, m := range []string{
		"tests pass", "test pass", "tests passed", "test passed",
		"is complete", "are complete", "is done", "are done",
		"all pass", "build pass", "build passed",
	} {
		if strings.Contains(low, m) {
			return true
		}
	}
	if strings.HasPrefix(low, "i've ") || strings.HasPrefix(low, "i have ") {
		for _, pp := range []string{"updated ", "fixed ", "completed ", "finished ", "resolved ", "verified "} {
			if strings.Contains(low, pp) {
				return true
			}
		}
	}
	return false
}

// paymentRefused reports a turn the model API refused because the payer could
// not pay: 402, in the harness's own words. The run asks the free model once
// and then stops; any other failure is the task's.
func paymentRefused(ran *client.Ran) bool {
	if ran == nil {
		return false
	}
	msg := outcomeOf(ran.Stdout).failed + "\n" + ran.Stderr
	low := strings.ToLower(msg)
	return strings.Contains(msg, "402") || strings.Contains(msg, "Payment Required") ||
		strings.Contains(low, "insufficient balance")
}

// delegatedAuthRefused reports a turn whose model call was rejected with 401
// specifically because its delegated token was revoked or expired at the gateway
// (sdk/confined.go confinedRefusal). It matches only the gateway's own delegated-
// token refusal phrases so an unrelated 401 in command stderr is never misread.
func delegatedAuthRefused(ran *client.Ran) bool {
	if ran == nil {
		return false
	}
	low := strings.ToLower(outcomeOf(ran.Stdout).failed + "\n" + ran.Stderr)
	return strings.Contains(low, "this delegated token was revoked") ||
		strings.Contains(low, "this delegated token has expired") ||
		strings.Contains(low, "the run it was minted for is over")
}

// paymentWaits reports a turn the payer refused in the API's own words, which is what a
// run waits for credits on. The bare "402" paymentRefused reads is enough to ask the free
// model once; it is not enough to park a run for, since a number in a failing command's
// stderr (`file.go:402`) is not a refusal.
func paymentWaits(ran *client.Ran) bool {
	if ran == nil {
		return false
	}
	msg := outcomeOf(ran.Stdout).failed + "\n" + ran.Stderr
	return strings.Contains(msg, "Payment Required") || strings.Contains(strings.ToLower(msg), "insufficient balance")
}

// limitRefused reports a turn the model API refused for a usage or rate limit — 429,
// or a quota/limit code the gateway or harness names — in the harness's own words.
// On the Free plan it is the free AI calls, and on paid plans it may be a per-minute
// rate limit, concurrency cap, or session/plan usage cap.
func limitRefused(ran *client.Ran) bool {
	if ran == nil {
		return false
	}
	msg := outcomeOf(ran.Stdout).failed + "\n" + ran.Stderr
	low := strings.ToLower(msg)
	return strings.Contains(msg, "429 Too Many Requests") || bareLimitRE.MatchString(msg) ||
		strings.Contains(low, "quota exceeded") || strings.Contains(low, "insufficient_quota") ||
		strings.Contains(low, "quota_error") || strings.Contains(low, "usage limit reached") ||
		strings.Contains(low, "allowance_spent") || strings.Contains(low, "rate_limit_exceeded") ||
		strings.Contains(low, "paid_lane_full") || strings.Contains(low, "usage_cap_exceeded") ||
		strings.Contains(low, "plan_allowance_used") || strings.Contains(low, "free_plan_cap") ||
		strings.Contains(low, "model_cap")
}

// modelCapRefused reports a 429 tied to a single model's cap, where switching to
// another Enso model can recover immediately.
func modelCapRefused(ran *client.Ran) bool {
	if ran == nil {
		return false
	}
	low := strings.ToLower(outcomeOf(ran.Stdout).failed + "\n" + ran.Stderr)
	return strings.Contains(low, "model_cap")
}

// capTransientWait bounds a transient 429 retry wait to transientRetryCap (60s)
// so a long Retry-After never stalls a warm sandbox for hours.
func capTransientWait(wait time.Duration) time.Duration {
	switch {
	case wait <= 0:
		return transientRetryDefault
	case wait > transientRetryCap:
		return transientRetryCap
	default:
		return wait
	}
}

// limitFallback selects another Enso model to try after a 429 rate-limit refusal
// so a throttled lane can hand off instead of stalling. A caller-pinned non-Enso
// model is respected (returns "").
func limitFallback(model string, pinned bool) string {
	switch model {
	case "enso-ultra":
		return "enso-pro"
	case "enso-pro", "enso", "enso-flash":
		return codingFallback
	case codingFallback:
		return "enso-pro"
	default:
		if !pinned && model != codingFallback {
			return codingFallback
		}
		return ""
	}
}

// transientLimit reports a retry-after duration the harness's stdout/stderr carries
// from a 429 response, and whether one was found. Only rate_limit_exceeded and
// paid_lane_full are treated as transient; payer refusals (402, etc.) are not.
func transientLimit(ran *client.Ran) (time.Duration, bool) {
	if ran == nil {
		return 0, false
	}
	msg := outcomeOf(ran.Stdout).failed + "\n" + ran.Stderr
	low := strings.ToLower(msg)
	if !strings.Contains(low, "rate_limit_exceeded") && !strings.Contains(low, "paid_lane_full") {
		return 0, false
	}
	if m := retryAfterRE.FindStringSubmatch(msg); m != nil {
		if secs, err := strconv.Atoi(m[1]); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second, true
		}
	}
	return 0, true
}

// limitResetsAt parses an RFC3339 "resets_at" timestamp carried in a gateway 429
// payload (usage_cap_exceeded, plan_allowance_used, free_plan_cap, model_cap), returning
// the zero time when absent, unparseable, or already past.
func limitResetsAt(ran *client.Ran) time.Time {
	if ran == nil {
		return time.Time{}
	}
	msg := outcomeOf(ran.Stdout).failed + "\n" + ran.Stderr
	m := resetsAtRE.FindStringSubmatch(msg)
	if m == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, m[1])
	if err != nil || !t.After(time.Now()) {
		return time.Time{}
	}
	return t
}

var (
	bareLimitRE     = regexp.MustCompile(`(?i)(?:last status:|unexpected status)\s*429\b`)
	retryAfterRE    = regexp.MustCompile(`(?i)\bretry after\s+(\d+)\s*s\b`)
	resetsAtRE      = regexp.MustCompile(`"resets_at"\s*:\s*"([^"]+)"`)
	threadGoneRE    = regexp.MustCompile(`(?i)(?:\bthread\b[^\n]*\b(?:not found|is gone|gone)\b|\bno rollout found\b|\brollout\b[^\n]*\bnot found\b|\bsession\b[^\n]*\b(?:not found|is gone|gone)\b|\b(?:failed|unable|cannot) to resume\b)`)
	rolloutFailedRE = regexp.MustCompile(`(?i)(?:\bfailed to record rollout\b|\bcould(?:n't| not) record(?: the)? rollout\b|\bstate db missing rollout\b|\bmissing rollout path\b|\brollout path is missing\b|\brollout recorder\b|\brollout\b[^\n]*\b(?:failed to (?:record|write|flush|persist)|channel closed|disconnected|corrupt)\b)`)
)

// threadGone reports a turn whose harness failed because a resumed thread or session
// no longer exists on disk or in-process.
func threadGone(ran *client.Ran) bool {
	if ran == nil {
		return false
	}
	msg := outcomeOf(ran.Stdout).failed + "\n" + ran.Stderr
	return threadGoneRE.MatchString(msg)
}

// rolloutFailed reports a turn whose harness reported a rollout recording or
// persistence error (such as "failed to record rollout").
func rolloutFailed(ran *client.Ran) bool {
	if ran == nil {
		return false
	}
	msg := outcomeOf(ran.Stdout).failed + "\n" + ran.Stderr
	return rolloutFailedRE.MatchString(msg)
}

// harnessThreadCrash reports a turn that failed because the harness lost its thread
// or rollout rather than because of the task itself.
func harnessThreadCrash(ran *client.Ran) bool {
	return threadGone(ran) || rolloutFailed(ran)
}

// rolloutTeardownOnly reports a non-zero exit whose only complaint is a rollout
// persistence error on stderr after the turn itself completed work (no turn.failed
// or error event in stdout, and the harness produced work or a message).
func rolloutTeardownOnly(ran *client.Ran) bool {
	if ran == nil || ran.ExitCode == 0 || killedBy(ran.ExitCode) != "" {
		return false
	}
	said := outcomeOf(ran.Stdout)
	if said.failed != "" || said.repeated != "" {
		return false
	}
	return rolloutFailed(ran) && (said.worked || said.answer != "" || said.items > 0)
}

// faultOf names the server error (500, 502, 503, 504) a turn ended with after the harness's own retries
// ("exceeded retry limit, last status: 503 Service Unavailable"), "" for one that is not: its status and
// phrase lower-cased, the request id left off ("503 service unavailable"), so one failure is one name.
func faultOf(ran *client.Ran) string {
	if ran == nil {
		return ""
	}
	m := fault.FindStringSubmatch(outcomeOf(ran.Stdout).failed + "\n" + ran.Stderr)
	if m == nil {
		return ""
	}
	return strings.Join(strings.Fields(strings.ToLower(m[1])), " ")
}

var fault = regexp.MustCompile(`(?i)exceeded retry limit, last status: ((?:500|502|503|504)\b[^,\n"\\]*)`)

// refusedFor is why the model API ended a turn that ended the run: the reason the run
// waits on (agents.ParkLimit, agents.ParkPayment, agents.ParkFault), or "" for a failure that
// is the task's. A usage limit is read first: a limit names a time, and a payer refusal does
// not; a fault is read last.
func refusedFor(ran *client.Ran) string {
	switch {
	case limitRefused(ran):
		return agents.ParkLimit
	case paymentWaits(ran):
		return agents.ParkPayment
	case faultOf(ran) != "":
		return agents.ParkFault
	}
	return ""
}

// refusedSaid is what a run the model API refused for a usage limit tells the person who
// asked when it could not wait for the limit to clear: its work is in its sandbox alone,
// not on a branch, so it ended where a run with a branch would have parked (park.go). A
// payer's refusal says what the API said, and what pays, as it always has.
const refusedSaid = "the run's AI calls were refused for a usage limit, and its work is in its sandbox alone, so it could " +
	"not wait for that to clear; add credits, or start a run with `after` set to this session once the limit has"

// event is the part of one harness event this reads.
type event struct {
	Type   string `json:"type"`
	Thread string `json:"thread_id"`
	Item   struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Usage *struct {
		Input  int64 `json:"input_tokens"`
		Cached int64 `json:"cached_input_tokens"`
		Output int64 `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

// outcomeOf reads a harness's stdout.
func outcomeOf(stdout string) outcome {
	o := outcome{plain: true}
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type == "" {
			continue
		}
		o.plain = false
		switch e.Type {
		case "thread.started":
			if o.thread == "" {
				o.thread = e.Thread
			}
		case "item.completed":
			o.items++
			if e.Item.Type == "agent_message" {
				o.answer = e.Item.Text
			} else if e.Item.Type != "" {
				o.worked = true
			}
		case "turn.completed":
			if e.Usage != nil {
				o.usage.Input += e.Usage.Input
				o.usage.Cached += e.Usage.Cached
				o.usage.Output += e.Usage.Output
			}
		case "turn.failed":
			if e.Error != nil && e.Error.Message != "" {
				o.failed = e.Error.Message
			}
		case "error":
			if e.Message != "" {
				o.failed = e.Message
			}
		}
	}
	if _, call, ok := strings.Cut(o.failed, loopMark); ok {
		o.repeated = strings.TrimSpace(call)
	}
	return o
}

// lastSentence is the last sentence of a line: what follows its final ". ", "! "
// or "? ", or the whole line when it has one sentence.
func lastSentence(line string) string {
	s := strings.TrimRight(strings.TrimSpace(line), ".!?:")
	i := max(strings.LastIndex(s, ". "), strings.LastIndex(s, "! "), strings.LastIndex(s, "? "))
	return strings.TrimSpace(s[i+1:])
}
