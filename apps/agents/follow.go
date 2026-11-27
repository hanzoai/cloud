package agents

// follow.go — a turn answers for the coding runs it starts.
//
// A coding run is admitted with a 202 and works for minutes after the tool loop
// that asked for it has moved on. The turn used to end at the model's reply ("I
// have started a run") while the run narrated into a SECOND message of its own,
// so one question got two messages, and a run that failed after the reply — or
// whose narration could not be posted — failed where nobody was looking.
//
// Now the turn holds its session open until the runs it started have ended.
// Their steps are the turn's progress lines, and their outcome — the pull
// request, or what failed — is the last paragraph of the turn's answer. One
// conversation, one message, one ending. The run posts nothing of its own: the
// dispatcher names the turn in the run's address (Reply.Turn, replyto.go), and a
// run that carries one leaves the talking to the turn.
//
// The turn only WATCHES the run — it has its own session, its own sandbox and
// its own budget — through the session store both share in this process. What
// the watching adds is the run's hands: while the turn follows it, the message's
// buttons are the run's (a spawn event, readTurn), a message said into its
// thread redirects it and the turn follows the follow-up (steer.go in
// apps/coding says where), and a stop of the turn stops the run it is watching.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hanzoai/cloud/sdk/audit"
)

// followWait bounds how long a turn waits for the runs it started. A run can take
// longer (apps/coding ceiling, the longest a session may live); the turn then answers that it
// is still going and names the session to read it by, and the run carries on.
const followWait = 35 * time.Minute

// followPoll is the longest a turn goes without reading its run again. The bus
// wakes it on every event the run appends; this is for the one a bus dropped, so
// a lost notice costs seconds and never the outcome.
const followPoll = 2 * time.Second

// maxOutcome bounds how much of a run's own account of its failure is put into
// the answer — its first line is the part that names the fault.
const maxOutcome = 300

// coded records what a coding call came back with: the run it admitted, or why
// it was refused. Any other call is not a run and changes nothing.
//
// A refusal is kept only until a later call starts a run. A model that named the
// wrong repository, read the candidates and asked again has started the run it
// meant; the first refusal is then history, not news.
func (l *live) coded(name, args, out string, err error) {
	if l == nil {
		return
	}
	if _, ok := amend(name, args, codingOp, func(input string) string { return input }); !ok {
		return
	}
	var started struct {
		SessionID string `json:"sessionId"`
	}
	if err == nil {
		_ = json.Unmarshal([]byte(out), &started)
	}
	l.mu.Lock()
	switch {
	case err != nil:
		l.refused = err.Error()
	case strings.TrimSpace(started.SessionID) == "":
		l.refused = "the coding run answered without naming its session"
	default:
		l.runs = append(l.runs, started.SessionID)
		l.refused = ""
	}
	l.mu.Unlock()
}

// conclude waits for the runs this turn started and returns answer with their
// outcomes after it, one paragraph each. A turn that started none, and had no
// coding call refused, returns answer untouched.
//
// It waits on the TURN's context, so a stop or a pause ends the wait; what is
// said then is that the run was still going, which is true.
func (l *live) conclude(ctx context.Context, answer string) string {
	if l == nil {
		return answer
	}
	l.mu.Lock()
	runs, refused := append([]string(nil), l.runs...), l.refused
	l.mu.Unlock()
	parts := []string{}
	if a := strings.TrimSpace(answer); a != "" {
		parts = append(parts, a)
	}
	if len(runs) == 0 && refused != "" {
		parts = append(parts, "The coding run did not start: "+reason(refused))
	}
	for _, id := range runs {
		l.spawned(ctx, id, false)
		parts = append(parts, l.await(ctx, id))
	}
	return strings.Join(parts, "\n\n")
}

// started is the coding runs this turn admitted, in order.
func (l *live) started() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.runs...)
}

// refusal is answer with why the turn's coding call did not start a run, when one
// was refused and none started: what conclude says of it, without waiting.
func (l *live) refusal(answer string) string {
	l.mu.Lock()
	runs, refused := len(l.runs), l.refused
	l.mu.Unlock()
	if runs > 0 || refused == "" {
		return answer
	}
	parts := []string{}
	if a := strings.TrimSpace(answer); a != "" {
		parts = append(parts, a)
	}
	return strings.Join(append(parts, "The coding run did not start: "+reason(refused)), "\n\n")
}

// await follows one run until it ends, or pauses — through every follow-up that
// carries it on — reporting each of its steps as the turn's progress, and
// answers its outcome.
func (l *live) await(ctx context.Context, id string) string {
	ctx, cancel := context.WithTimeout(ctx, followWait)
	defer cancel()
	var (
		after   int64
		handed  bool // stopped for a follow-up, which will name itself
		updates <-chan streamUpdate
	)
read:
	for {
		sto, err := l.store()
		if err != nil {
			return stillGoing(ctx, id)
		}
		// The status FIRST, then the events. A run appends its last word before it
		// closes its session, so a session read as over here has every event it
		// will ever have by the read below — the other order can see the close and
		// miss the word, and report a run that said how it ended as one that did not.
		x, err := sto.GetSession(ctx, l.x.Org, id)
		if err != nil {
			return l.stopped(ctx, id)
		}
		evs, err := sto.ListEvents(ctx, l.x.Org, id, after, turnPage)
		if err != nil {
			return l.stopped(ctx, id)
		}
		for _, e := range evs {
			after = e.Seq
			t := narrate(e)
			switch {
			case t.next != "":
				// A follow-up carries the run on: the rest of this page is the old
				// run's, and the new one is read from its start.
				id, after, handed = t.next, 0, false
				l.spawned(ctx, id, false)
				continue read
			case t.handed:
				handed = true
				l.report(ctx, t.line)
			case t.paused:
				l.spawned(ctx, id, true)
				return t.end
			case t.end != "":
				return t.end
			case t.line != "":
				l.report(ctx, t.line)
			}
		}
		if isTerminalStatus(x.Status) && !handed && len(evs) < turnPage {
			return "The coding run ended (" + x.Status + ") without saying how (session " + id + ")."
		}
		if updates == nil {
			if l.s == nil || l.s.State.bus == nil {
				updates = tick()
			} else {
				ch, unsub := l.s.State.bus.subscribe(l.x.Org)
				defer unsub()
				updates = ch
			}
			continue // read once more now that nothing can be missed
		}
		select {
		case <-ctx.Done():
			return l.stopped(ctx, id)
		case _, ok := <-updates:
			if !ok {
				updates = tick()
			}
		case <-time.After(followPoll):
		}
	}
}

// stopped is how a turn that stopped waiting leaves the run it followed. A turn
// that was STOPPED stops its run too — the run is what the person was watching,
// and it keeps its work on its branch (apps/coding steer.go) — and anything else
// leaves it going and says so.
func (l *live) stopped(ctx context.Context, id string) string {
	if !errors.Is(context.Cause(ctx), ErrStopped) {
		return stillGoing(ctx, id)
	}
	ctx = context.WithoutCancel(ctx)
	sto, err := l.store()
	if err == nil {
		var x Session
		if x, err = sto.GetSession(ctx, l.x.Org, id); err == nil {
			_, _, err = stop(ctx, sto, x, "the turn that started it was stopped")
		}
	}
	if err != nil {
		return "The coding run could not be stopped with the turn: " + reason(err.Error()) + " (session " + id + ")."
	}
	return "The coding run was stopped with the turn (session " + id + ")."
}

// spawned tells the turn's readers which run it is following, and whether that
// run has paused — which is what puts that run's buttons on the turn's message.
func (l *live) spawned(ctx context.Context, id string, paused bool) {
	payload, _ := json.Marshal(map[string]any{"session": id, "paused": paused})
	l.append(context.WithoutCancel(ctx), KindSpawn, payload)
}

// stillGoing is what a turn says about a run it stopped watching before the run
// ended.
func stillGoing(ctx context.Context, id string) string {
	if ctx.Err() == nil {
		return "The coding run's progress could not be read (session " + id + ")."
	}
	return "The coding run was still going when this reply stopped waiting for it (session " + id + ")."
}

// told is one event of a followed run, read for the person who asked.
type told struct {
	line   string // a step, shown as the turn's progress
	end    string // the run's outcome: it is over, or paused
	next   string // the follow-up run that carries this one on
	handed bool   // stopped for a new instruction; its follow-up names itself next
	paused bool   // the run is paused, keeping its work, until Continue or Stop
}

// narrate reads one event of a coding run's session in words for the person who
// asked: a line of progress, the run's outcome, or the follow-up it handed to.
//
// It reads the run's OWN account of itself (apps/coding mirrors a status when it
// starts, ends, pauses or hands over, and a tool-call per step) and nothing the
// sandbox streams: a command's output is the model's to read, not a line for the
// channel. An event it cannot read changes nothing, because a run that has pushed a
// branch must not be disturbed by a payload this cannot parse.
func narrate(e Event) told {
	var p struct {
		Status   string `json:"status"`
		Step     string `json:"step"`
		Message  string `json:"message"`
		Repo     string `json:"repo"`
		Branch   string `json:"branch"`
		Target   string `json:"target"`
		URL      string `json:"url"`
		Error    string `json:"error"`
		PRError  string `json:"prError"`
		Next     string `json:"next"`
		Changed  bool   `json:"changed"`
		Followed bool   `json:"followed"`
		TimeUp   bool   `json:"timeUp"`
		// Unrenewable is a run ended short of the one credential its harness holds.
		Unrenewable bool `json:"unrenewable"`
	}
	if json.Unmarshal([]byte(e.Payload), &p) != nil {
		return told{}
	}
	// Where a stopped or paused run's work is. Both keep it on the run's branch.
	kept := " Nothing had changed yet."
	if p.Changed && p.Branch != "" {
		kept = " Work so far is on `" + p.Branch + "`."
	}
	switch e.Kind {
	case KindStatus:
		switch p.Status {
		case "started":
			return told{line: "Working on " + p.Repo + " in " + p.Branch}
		case "routed":
			return told{line: "Sent to " + p.Target + " to work on " + p.Repo}
		case "error":
			// The engine's own prefix says which package failed, which the
			// sentence already says.
			return told{end: "The coding run failed: " + reason(strings.TrimPrefix(p.Error, "coding: "))}
		case "done":
			switch {
			case !p.Changed:
				return told{end: "The coding run finished: nothing needed to change."}
			case p.URL != "":
				return told{end: "Opened a pull request for `" + p.Branch + "`: " + p.URL}
			case p.PRError != "":
				return told{end: "Pushed `" + p.Branch + "`, but the pull request was not opened: " + reason(p.PRError)}
			}
			return told{end: "Pushed `" + p.Branch + "`."}
		case "stopped":
			if p.Followed {
				return told{line: "Stopped for your new instruction;" + kept + " a follow-up run takes it from here", handed: true}
			}
			if p.Unrenewable {
				return told{end: "The coding run ended when its model credential was about to: its harness cannot renew it." + kept + " Start a new run with `after` set to this session to carry on."}
			}
			if p.TimeUp {
				return told{end: "The coding run used its time." + kept + " Start a new run with `after` set to this session to carry on."}
			}
			return told{end: "The coding run stopped." + kept}
		case "paused":
			return told{end: "The coding run is paused." + kept, paused: true}
		case "parked":
			// Waiting to go on by itself: the turn is done with it, and its buttons are
			// Continue (now) and Stop, as a paused run's are.
			return told{end: "The coding run is " + reason(p.Message) + "." + kept, paused: true}
		case "followed":
			if p.Next != "" {
				return told{next: p.Next}
			}
			return told{end: "The follow-up run could not start: " + reason(p.Error)}
		case "asked":
			// The question itself is its own message, with its answers as buttons
			// (ask.go); the run's line says what it is waiting on.
			return told{line: "Waiting for your answer"}
		case "answered":
			return told{line: "Got your answer; carrying on"}
		}
	case KindToolCall:
		// "exit" is the sandbox's word for one command ending; the run's own steps
		// are what a person follows.
		if p.Step != "exit" {
			return told{line: sentence(p.Message)}
		}
	}
	return told{}
}

// reason is a run's own account of a failure as it may be posted into a channel:
// credentials redacted, the first line, bounded.
func reason(why string) string {
	why = strings.TrimSpace(audit.RedactText(why))
	if i := strings.IndexByte(why, '\n'); i >= 0 {
		why = strings.TrimSpace(why[:i])
	}
	if why == "" {
		return "no reason was given"
	}
	if len(why) > maxOutcome {
		cut := maxOutcome
		for cut > 0 && !utf8.RuneStart(why[cut]) {
			cut--
		}
		why = why[:cut] + "…"
	}
	return why
}

// sentence starts a step's message with a capital, the way a progress line reads.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return ""
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
