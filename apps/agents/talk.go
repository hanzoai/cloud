package agents

// talk.go — the app's conversation: one session, every turn.
//
// Dev in the app used to send every message straight to the coding plane: the
// first one leased a sandbox before anything was decided, and every later one was
// a new run with a session, an address and a transcript of its own. A
// conversation is what a person has with Chat, and Dev is that conversation with
// hands: the agent answers, and decides for itself when a turn needs a computer
// (a sandbox call, leased lazily on the first one — box.go) or a build (a coding
// run, which leases its own). A turn that only talks touches neither.
//
// ONE SESSION HOLDS EVERY TURN. The first thing said opens it (channel `app`);
// everything said after resumes it under a new run, the way a stopped turn is
// resumed (resumeTurn). Between turns it RESTS paused rather than done: done is
// terminal and monotonic (sessions.go Status), and a conversation that has
// answered is waiting for the person, not over. Resting writes its run's end and
// seals it (Store.Rest), so the sweeps that look for live sessions leave it alone,
// and what is said next is the next turn. A turn whose process dies, or whose run
// panics, rests the same way with the reason on its record (reap.go endOrphan,
// turn.go). A turn session is never billed for its wall clock (Store.Unbilled
// reads run='' only) — what a turn costs is its run (runAgent: the fee and the
// tokens), paid by the person asking.
//
// IT IS ITS PERSON'S, as a Chat conversation is (reader.sees), and so is every
// coding run in its tree (private): nobody else in the org lists, watches, steers or
// continues any of it, a SuperAdmin reads it by its id and is audited for it
// (overheard, which refuses the read when it cannot be recorded), and nobody
// writes into it but its turns and the coding plane (appendEvent refuses). What its
// next turn is handed as the conversation is therefore what its person and its
// turns said.
//
// IT HAS NO HANDS OF ITS OWN. Code is worked on by a coding run, and a coding run is
// `dev exec` in a sandbox: the CLI itself, with the CLI's own tools — so what Dev
// can do to code is what `dev` can, by construction, and a turn is not offered a
// second, different set (agents.go, box.go boxCall). The first build leases the
// conversation's sandbox; every build after carries on in it (`after`), parked by
// apps/coding between them.
//
// ITS CODING RUNS ARE ITS OWN. What the person chose for the code — the
// repository, the branch, the machine, the mode — is kept on the conversation's
// session and written into every coding call its turns make (furnish), with the
// run it carries on (`after`: the conversation's latest), so every build after the
// first works in the first one's sandbox and on its branch (apps/coding start.go).
// One still working is refused rather than started beside it, whoever named it. In
// Manual the person is asked before each run starts, and before any other call
// that is not a read (ask_user, answered on the conversation's page). The run is
// BORN in the conversation's tree (OpenSessionOn), so the conversation is one row
// in a list, and each run in it is its person's alone, as the conversation is.
//
// A TURN ENDS WHEN ITS ANSWER DOES. A build takes many minutes, and a conversation
// that waited on it could not be spoken in: a turn rests once the model has
// answered, and the runs it started are followed after it (follow), each one's
// outcome said into the conversation when it ends. Words said meanwhile are the
// next turn; a build is steered only from its own page.
//
// The bridges are untouched by construction: everything here keys on the
// session's channel being the app's, which no bridge's is (channelOf).

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// talkChannel is the channel of the app's conversation. A bridge's is
// provider:channel[/thread] (channelOf), so the two never meet.
const talkChannel = "app"

// talks reports whether x is the app's conversation itself: a root on the app's
// channel.
func talks(x Session) bool { return x.Channel == talkChannel && x.ParentID == "" }

// private reports whether x is in an app conversation: the conversation, or a coding
// run in its tree, which is born there (OpenSessionOn). The whole tree is its
// person's alone, as the conversation is (reader.sees, actable, appendEvent).
func private(x Session) bool { return x.Channel == talkChannel }

// ConversationOf is the app conversation x is in, or "": a coding run carrying on
// one in a conversation is in that conversation too (apps/coding start.go).
func ConversationOf(x Session) string {
	if !private(x) {
		return ""
	}
	return x.RootID
}

// The modes a conversation's coding runs go in: on their own (auto), each asked
// for first (manual), or reading and answering with a plan only (plan).
const (
	modeAuto   = "auto"
	modeManual = "manual"
	modePlan   = "plan"
)

// The answers a Manual conversation is asked for before a coding run starts, and
// before any other call that may change something.
const (
	startRun = "Start it"
	doCall   = "Do it"
	holdRun  = "Not now"
)

// workspace is what a conversation's coding calls are written (furnish): the
// code the person chose, kept on the conversation's session (repo, base,
// environment, mode), and the model, effort and issue the turn carries.
type workspace struct {
	repo, base, target, mode string
	model, effort            string
	instructions             string
	issue                    int
}

// workspaceOf is what x keeps for its code. A machine is the environment it names;
// the sandbox names none.
func workspaceOf(x Session) *workspace {
	w := &workspace{repo: x.Repo, base: x.Base, mode: x.Mode}
	if x.Environment != sandboxEnvironment {
		w.target = x.Environment
	}
	return w
}

// sandboxEnvironment is the Environment of work in a Hanzo sandbox, as a coding
// run's session says it (apps/coding sandboxEnv).
const sandboxEnvironment = "sandbox"

// talkIn is one thing the person says in the app's conversation.
type talkIn struct {
	// Session is the conversation to continue: one this op answered with before,
	// and the caller's own. Empty starts a new conversation.
	Session string `json:"session,omitempty"`
	// Input is what the person said, up to 128 KiB.
	Input string `json:"input"`
	// Model is the model the person chose, by the id GET /v1/models lists: the
	// turn thinks with it, and so does a coding run the turn starts. Empty, or one
	// the assistant is not offered, is the assistant's own.
	Model string `json:"model,omitempty"`
	// Effort is how hard a coding run the turn starts thinks: low, medium or high.
	// Empty is the run's default.
	Effort string `json:"effort,omitempty"`
	// Repo is the repository the conversation's code is in, as a coding run names
	// one: its name in the caller's org, or owner/name on GitHub. Kept for every
	// later turn; a turn naming another moves the conversation to it, from that
	// repository's default branch unless the turn names a base too.
	Repo string `json:"repo,omitempty"`
	// Base is the branch the conversation's coding runs start from, kept as Repo
	// is. Empty is the repository's default.
	Base string `json:"base,omitempty"`
	// Target is one of the org's machines the conversation's coding runs go to,
	// kept as Repo is, or `sandbox` for the Hanzo sandbox. Empty keeps the
	// conversation's, and a new one works in the sandbox.
	Target string `json:"target,omitempty"`
	// Mode is how the conversation's coding runs go, kept as Repo is: auto starts
	// them, manual asks the person first, plan reads and answers with a plan,
	// writing nothing. Empty keeps the conversation's, and a new one is auto.
	Mode string `json:"mode,omitempty"`
	// Issue is the number of the issue on Repo a coding run this turn starts
	// resolves: its pull request says `Closes #<n>`.
	Issue int `json:"issue,omitempty"`
	// Instructions carries per-turn developer instructions (such as active skills
	// and memories) separate from Input so the user's message stays clean;
	// optional.
	Instructions string `json:"instructions,omitempty"`
	// DeveloperInstructions is the explicit alias for Instructions forwarded to
	// the turn and any coding run it starts; optional.
	DeveloperInstructions string `json:"developerInstructions,omitempty"`
}

// talkOut is the turn that was started.
type talkOut struct {
	// SessionID is the conversation: the same id on every turn of it.
	SessionID string `json:"sessionId"`
	// RunID is this turn's run within the conversation.
	RunID string `json:"runId"`
}

// Talk says one thing in the app's conversation and starts the turn that
// answers it. It answers at once with the conversation and the turn's run; the
// turn itself is read from the session (GET /v1/agent/sessions/{id}, and its
// ?root= stream): the person's words, what the agent is doing, the coding runs it
// starts, a question it asks, and its answer.
//
// With no `session` it starts a conversation. With one it continues that
// conversation in the same session, under a new run, once its last turn has
// answered: 409 while a turn is still answering — what is said then is that
// turn's next input (POST /v1/agent/sessions/{id}/message). A conversation is its
// person's alone: anyone else's answers as absent, and a SuperAdmin acting in an
// org that is not their own holds none there (their sandbox and builds would be
// paid by that org).
//
// Nothing is leased to answer: a turn that only talks uses no sandbox. Code is
// worked on by a coding run — `dev`, with its own tools — which leases the
// conversation's sandbox on the first build; a later one works on the
// conversation's repository and carries on its last run, in that run's sandbox
// and on its branch.
//
// Example: {"input": "make the landing page load faster", "repo": "site"}
func (o sessionOps) talk(ctx context.Context, in *talkIn) (*talkOut, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	sub, ok := principal.SubjectFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a conversation is held by a caller with a stable subject")
	}
	if who := cloud.Who(ctx); who.Admin && strings.TrimSpace(who.Owner) != org {
		return nil, zip.ErrForbidden("a conversation runs as you in your own organization; this one's would be paid by it")
	}
	words := strings.TrimSpace(in.Input)
	switch {
	case words == "":
		return nil, zip.ErrBadRequest("input is required")
	case len(words) > maxInput:
		return nil, zip.ErrBadRequest("input too large")
	case in.Issue < 0:
		return nil, zip.ErrBadRequest("issue must be a positive number")
	}
	mode := strings.TrimSpace(in.Mode)
	switch mode {
	case "", modeAuto, modeManual, modePlan:
	default:
		return nil, zip.ErrBadRequest("mode must be auto, manual or plan")
	}
	actor := billingActor(org, sub)
	ask := &client.RunOnBehalfIn{Org: org, Subject: sub, Ref: builtinAgentName, Input: words, Model: strings.TrimSpace(in.Model)}
	devInst := cmp.Or(strings.TrimSpace(in.DeveloperInstructions), strings.TrimSpace(in.Instructions))
	if len(devInst) > 48<<10 {
		devInst = devInst[:48<<10]
		for len(devInst) > 0 && !utf8.ValidString(devInst) {
			devInst = devInst[:len(devInst)-1]
		}
	}
	// What this turn names for the code; what it leaves out stays as the conversation keeps it.
	named := Session{Repo: strings.TrimSpace(in.Repo), Base: strings.TrimSpace(in.Base), Environment: strings.TrimSpace(in.Target), Mode: mode}
	now := time.Now().Unix()

	if id := strings.TrimSpace(in.Session); id != "" {
		x, err := actable(ctx, sto, org, id)
		if err != nil {
			return nil, err
		}
		if !talks(x) {
			return nil, zip.ErrNotFound("session not found")
		}
		if !resumable(x) {
			if x.Status == StatusRunning {
				return nil, zip.Errorf(http.StatusConflict,
					"the conversation is still answering; say it into the turn (POST /v1/agent/sessions/%s/message)", x.ID)
			}
			return nil, zip.Errorf(http.StatusConflict, "this conversation has ended (%s); start a new one", x.Status)
		}
		if named.Repo != "" || named.Base != "" || named.Environment != "" || named.Mode != "" {
			// Another repository starts from its own default unless a base is named.
			if named.Repo != "" && named.Repo != x.Repo {
				x.Base = ""
			}
			x.Repo, x.Base, x.Environment, x.Mode = cmp.Or(named.Repo, x.Repo), cmp.Or(named.Base, x.Base),
				cmp.Or(named.Environment, x.Environment), cmp.Or(named.Mode, x.Mode)
			if err := sto.Keep(ctx, org, x.ID, x, now); err != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "keep the conversation's code: %v", err)
			}
		}
		t, err := termsOf(ctx, sto, x, ask)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "read the conversation: %v", err)
		}
		t.said, t.ws.model, t.ws.effort, t.ws.instructions, t.ws.issue = words, ask.Model, strings.TrimSpace(in.Effort), devInst, in.Issue
		st, err := resumeTurn(s, ctx, sto, x, ask, t)
		if err != nil {
			return nil, err
		}
		return &talkOut{SessionID: st.SessionID, RunID: st.RunID}, nil
	}

	id := mint.ID("sess")
	x := Session{
		ID: id, Org: org, Agent: builtinAgentName, Actor: actor, Status: StatusRunning, RootID: id,
		Title: runTitle(words), Channel: talkChannel,
		Repo: named.Repo, Base: named.Base, Environment: named.Environment, Mode: cmp.Or(named.Mode, modeAuto),
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
		Payer: payerOf(ctx, org).Subject(), MeteredAt: now,
	}
	t, err := termsOf(ctx, sto, x, ask)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "read the conversation: %v", err)
	}
	t.said, t.ws.model, t.ws.effort, t.ws.instructions, t.ws.issue = words, ask.Model, strings.TrimSpace(in.Effort), devInst, in.Issue
	st, err := runTurn(s, ctx, sto, x, ask, sto.CreateSession, t)
	if err != nil {
		return nil, err
	}
	return &talkOut{SessionID: st.SessionID, RunID: st.RunID}, nil
}

// remembered is the conversation so far, from its own session, oldest first and
// within the budget every conversation is handed in (client.Fit): what the person
// said — the words each turn opened with, and what they said into a turn as it
// worked — and what each turn answered, with the tools it used. Its events have
// one writer, the server — its turns and their followers (appendEvent refuses the
// whole tree) — which writes them as the conversation's person, so an event
// recorded as anyone else's is no part of it, either side.
func remembered(ctx context.Context, sto *Store, x Session) ([]infer.ChatMessage, error) {
	var turns []client.Turn
	var after int64
	for {
		evs, err := sto.ListEvents(ctx, x.Org, x.ID, after, turnPage)
		if err != nil {
			return nil, err
		}
		for _, e := range evs {
			after = e.Seq
			if e.Actor != x.Actor {
				continue
			}
			switch e.Kind {
			case KindMessage:
				var m struct {
					Role  string `json:"role"`
					Text  string `json:"text"`
					Tools string `json:"tools"`
				}
				if json.Unmarshal([]byte(e.Payload), &m) != nil {
					continue
				}
				if m.Role == infer.RoleUser {
					if text := strings.TrimSpace(m.Text); text != "" {
						turns = append(turns, client.Turn{Text: text})
					}
					continue
				}
				if text := WithToolSummary(m.Text, m.Tools); text != "" {
					turns = append(turns, client.Turn{Self: true, Text: text})
				}
			case KindControl:
				var cp controlPayload
				if json.Unmarshal([]byte(e.Payload), &cp) != nil {
					continue
				}
				if text := strings.TrimSpace(cp.Message); text != "" && (cp.Command == CmdMessage || cp.Command == CmdResume) {
					turns = append(turns, client.Turn{Text: text})
				}
			}
		}
		if len(evs) < turnPage {
			break
		}
	}
	return transcript(client.Fit(turns)), nil
}

// furnish writes into a coding call made in the app's conversation what the
// conversation already knows, after the model chose the call and before it is
// made: the code the person chose where the call names none, the model and effort
// the turn carries, a screen for a run in the sandbox, Plan over anything the
// model said, and the run it carries on (carried). A run is one at a time: a call
// carrying on one still working is refused, whoever named it. In Manual the person
// is asked first, and a run they did not start is refused — and so is any other
// call to a tool the server does not mark read-only (MCP readOnlyHint, kept per
// turn by offered): Manual changes nothing without its person. A read, and any
// call off the app, is returned untouched.
func furnish(ctx context.Context, name, args string) (string, error) {
	l := liveFrom(ctx)
	if l == nil || !talks(l.x) {
		return args, nil
	}
	w := l.ws
	if w == nil {
		w = workspaceOf(l.x)
	}
	if _, coding := amend(name, args, codingOp, func(input string) string { return input }); !coding {
		// In Manual nothing changes without the person saying so: a call to a tool
		// the server does not mark read-only is asked first, whatever it is.
		if w.mode == modeManual && !l.reading(name) {
			return args, l.permit(ctx, called(name, args), doCall)
		}
		return args, nil
	}
	var after, prompt string
	out, _ := amend(name, args, codingOp, func(input string) string {
		input = fill(input, "repo", w.repo)
		input = fill(input, "base", w.base)
		input = fill(input, "targetId", w.target)
		input = fill(input, "model", w.model)
		input = fill(input, "effort", w.effort)
		if w.instructions != "" {
			input = fill(input, "developerInstructions", w.instructions)
		}
		if w.issue > 0 {
			input = fill(input, "issue", w.issue)
		}
		if !carries(input, "targetId") {
			input = fill(input, "desktop", true)
		}
		if w.mode == modePlan {
			input = set(input, "mode", modePlan)
		}
		// The run is born in this conversation, whatever the model wrote there.
		input = set(input, "conversation", l.x.ID)
		after, prompt = valueOf(input, "after"), valueOf(input, "prompt")
		return input
	})
	var err error
	if after == "" {
		if after, err = l.carried(ctx); err != nil {
			return "", err
		}
		if after != "" {
			out, _ = amend(name, out, codingOp, func(input string) string { return set(input, "after", after) })
		}
	} else if err := l.idle(ctx, after); err != nil {
		return "", err
	}
	if w.mode == modeManual {
		ask := "Start a coding run?"
		if p := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0]); p != "" {
			ask = "Start a coding run: " + p
		}
		if err := l.permit(ctx, ask, startRun); err != nil {
			return "", err
		}
	}
	return out, nil
}

// called is a tool call as its person is asked about it: the tool, and the
// operation it names when it is a subsystem's.
func called(name, args string) string {
	if op := valueOf(args, "op"); op != "" {
		return "Call " + name + " " + op + "?"
	}
	return "Call " + name + "?"
}

// fill writes v at key unless the call already says one, or v is empty. A model
// that named a repository meant that one.
func fill(args, key string, v any) string {
	if s, ok := v.(string); ok && s == "" {
		return args
	}
	if carries(args, key) {
		return args
	}
	return set(args, key, v)
}

// carries reports whether the call's arguments carry a value at key, under any
// spelling the tool's decoder reads as it (set).
func carries(args, key string) bool {
	obj, err := object(args)
	if err != nil {
		return true // the tool plane refuses arguments it cannot read; nothing is written into them
	}
	for k, raw := range obj {
		if !strings.EqualFold(k, key) {
			continue
		}
		switch strings.TrimSpace(string(raw)) {
		case "", "null", `""`, "0", "false":
		default:
			return true
		}
	}
	return false
}

// valueOf is the string the call carries at key, under any spelling, or "".
func valueOf(args, key string) string {
	obj, err := object(args)
	if err != nil {
		return ""
	}
	for k, raw := range obj {
		var v string
		if strings.EqualFold(k, key) && json.Unmarshal(raw, &v) == nil {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// carried is the run a coding call in the conversation carries on: the latest the
// conversation started, in this turn or one before it, or "" before its first.
// One still working, or paused, is refused (idle).
func (l *live) carried(ctx context.Context) (string, error) {
	l.mu.Lock()
	var id string
	if n := len(l.runs); n > 0 {
		id = l.runs[n-1]
	}
	l.mu.Unlock()
	if id == "" {
		sto, err := l.store()
		if err != nil {
			return "", err
		}
		if id, err = sto.LastSpawn(ctx, l.x.Org, l.x.ID); err != nil {
			return "", fmt.Errorf("agents: read the conversation's coding run: %w", err)
		}
		if id == "" {
			return "", nil
		}
	}
	return id, l.idle(ctx, id)
}

// idle refuses a coding run carrying on one that still has the sandbox: working,
// or paused with its work waiting to be continued. apps/coding refuses it too, for
// every caller; this says why in the conversation's own words.
func (l *live) idle(ctx context.Context, run string) error {
	sto, err := l.store()
	if err != nil {
		return err
	}
	x, err := sto.GetSession(ctx, l.x.Org, run)
	if errors.Is(err, errSessionNotFound) {
		return nil // not this org's run: apps/coding answers for it
	}
	if err != nil {
		return fmt.Errorf("agents: read the conversation's coding run: %w", err)
	}
	switch x.Status {
	case StatusRunning:
		return fmt.Errorf("the coding run %s is still working; wait for it, or stop it from its page", x.ID)
	case StatusPaused:
		return fmt.Errorf("the coding run %s is paused; continue it, or stop it, from its page", x.ID)
	}
	return nil
}

// permit asks the person, in a Manual conversation, whether what the turn is about
// to do — ask, answered with yes — may go ahead, and refuses it unless they said so.
func (l *live) permit(ctx context.Context, ask, yes string) error {
	if len([]rune(ask)) > maxQuestion {
		ask = string([]rune(ask)[:maxQuestion-1]) + "…"
	}
	q, _ := json.Marshal(client.AskIn{Question: ask, Options: []client.Option{{Label: yes}, {Label: holdRun}}})
	said := l.ask(ctx, string(q))
	if said == "The person chose: "+yes {
		return nil
	}
	return fmt.Errorf("the person did not let this go ahead (%s); say what you would have done instead", strings.TrimPrefix(said, "error: "))
}

// offered keeps which of the tools this turn is offered change nothing.
func (l *live) offered(defs []infer.ToolDef) {
	reads := make(map[string]bool, len(defs))
	for _, d := range defs {
		if d.Reads {
			reads[d.Name] = true
		}
	}
	l.mu.Lock()
	l.reads = reads
	l.mu.Unlock()
}

// reading reports whether name is a tool this turn was offered as changing nothing.
func (l *live) reading(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reads[name]
}

// rest leaves the conversation waiting for the person's next words: paused,
// sealed and its run's end written (Store.Rest), its sandbox parked (box.go
// keeps). THE ORDER IS THE POINT: the row says paused before the turn lets its
// hold go — so no sweep ever finds it running and unheld — and the hold is gone
// before anyone is told it rests, so the next words find the session free rather
// than "still stopping". A stop that landed first wins, and nothing is said.
func (l *live) rest(ctx context.Context) {
	now := time.Now().Unix()
	sto, err := l.store()
	rested := false
	if err == nil {
		rested, err = sto.Rest(context.WithoutCancel(ctx), l.x.Org, l.x.ID, l.run, now)
	}
	l.letGo()
	if err != nil {
		l.s.Log.Error("agents: a conversation's turn could not rest", "org", l.x.Org, "session", l.x.ID, "err", err)
		return
	}
	if !rested {
		return
	}
	x := l.x
	x.Status, x.EndedAt, x.UpdatedAt = StatusPaused, now, now
	publishSession(l.s, x, 0, 0)
}

// follow waits, after the conversation has rested, for the coding runs a turn
// started, and says each one's outcome into the conversation when it ends — a
// follow-up that carried one on included (await). It holds nothing and reports no
// step as progress: the run's own page shows those, and the conversation may be
// on its next turn by then.
func (l *live) follow(runs []string) {
	if len(runs) == 0 {
		return
	}
	f := &live{s: l.s, x: l.x, run: l.run, quiet: true}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), followWait)
		defer cancel()
		for _, id := range runs {
			end := f.await(ctx, id)
			payload, _ := json.Marshal(map[string]string{"text": end})
			f.append(context.WithoutCancel(ctx), KindMessage, payload)
		}
	}()
}

// overheard records a SuperAdmin reading another person's conversation, or a run
// in it — the one reach past its person there is — every time it is made. A read
// that cannot be recorded is not made.
func overheard(ctx context.Context, x Session) error {
	if mounted == nil || mounted.State.audit == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "a read of another person's conversation is audited, and no audit is configured")
	}
	who := cloud.Who(ctx)
	entry := audit.Record{
		Actor:    audit.Actor{Org: who.Org, Sub: who.User, Email: who.Email},
		Action:   "conversation.read",
		Resource: audit.Resource{Type: "conversation", ID: x.ID},
		Auth:     audit.AuthContext{Method: "gateway", IsAdmin: who.Admin},
		Outcome:  audit.Outcome{Result: "ok", Status: http.StatusOK},
	}
	if c, ok := cloud.Request(ctx); ok {
		entry.Method, entry.Path, entry.SourceIP = c.Method(), c.Path(), geo.IP(c)
		entry.UserAgent, entry.RequestID = c.Header("User-Agent"), c.RequestID()
	}
	after, err := json.Marshal(map[string]string{"conversation": x.RootID, "session": x.ID, "person": x.Actor})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "audit: %v", err)
	}
	entry.After = after
	if _, err := mounted.State.audit.Append(ctx, entry); err != nil {
		mounted.Log.Warn("agents: a SuperAdmin's read of a conversation was not audited", "session", x.ID, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "a read of another person's conversation is audited, and the audit could not be written")
	}
	return nil
}

// answerIn answers a question a session's run asked its person.
type answerIn struct {
	// ID is the session whose run asked, from the path.
	ID string `json:"id"`
	// Question is the question answered, as the session's `asked` status names it.
	Question string `json:"question" url:"-"`
	// Text is the answer: a pressed option's label, or the person's own words.
	Text string `json:"text" url:"-"`
	// Chosen says Text is one of the options the question offered.
	Chosen bool `json:"chosen,omitempty" url:"-"`
}

// answerView is the answer a question was settled with.
type answerView struct {
	// Question is the question answered.
	Question string `json:"question"`
	// Text is what it was answered with.
	Text string `json:"text"`
}

// AnswerSession answers a question a session's run asked its person — a coding
// run's harness, or a turn of the app's conversation asking whether a coding run
// may start — from the page the session is read on. The run waiting on it goes on
// with the answer. Only the person the question is for may answer, once: a
// question already answered or closed answers 409, and one this session did not
// ask answers 404.
func (o sessionOps) answer(ctx context.Context, in *answerIn) (*answerView, error) {
	sto, org, err := tenantStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	x, err := actable(ctx, sto, org, in.ID)
	if err != nil {
		return nil, err
	}
	sub, ok := principal.SubjectFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("an answer is given by a caller with a stable subject")
	}
	q, err := sto.getQuestion(ctx, org, strings.TrimSpace(in.Question))
	if errors.Is(err, sql.ErrNoRows) || err == nil && q.Session != x.ID {
		return nil, zip.ErrNotFound("no such question in this session")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "question: %v", err)
	}
	got, err := answer(ctx, org, sub, &client.AnswerIn{Question: q.ID, Text: in.Text, Chosen: in.Chosen})
	if err != nil {
		return nil, err
	}
	return &answerView{Question: got.Question, Text: got.Text}, nil
}
