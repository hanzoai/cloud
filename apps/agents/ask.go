package agents

// ask.go — a run asking its person a question, and the answer coming back.
//
// A run with a decision only its person can make had two moves, both wrong: guess,
// or end on a question in prose that nothing could answer back into. ask_user is
// the third: the run names the question and the answers it offers, the person
// sees it where they are already reading the run — buttons in its thread — and
// the run waits for the choice and goes on with it.
//
// ONE PRIMITIVE, EVERY RUN. A chat turn asks from its tool loop (askTool); a
// coding run's harness asks through the MCP server its sandbox is given
// (plugin/agent), with a credential that can do nothing else. Both land in Ask:
// one question row, one rendering, one wait, one answer path.
//
// THE ADDRESS IS NEVER THE MODEL'S. A run asks in the conversation it is already
// bound to, and only the person it runs for can answer — neither is an argument,
// for the reason replyto.go gives: a model that could name a channel or a person
// could put a question in front of anyone.
//
// AN ANSWER IS WRITTEN ONCE. A press and a reply in the thread both land in
// answer, which moves the row from open in one guarded update; the loser of a
// race is told the question was already answered, and the run that asked is
// handed exactly one answer. A question nobody answered in time is closed the same
// way, so a late press finds it closed rather than answering into nothing.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// AskWait is the longest a run waits for its answer. A coding run blocks on it; a
// chat turn waits no longer than its own budget leaves room for (askTool).
const AskWait = 30 * time.Minute

// The bounds on a question: what one Slack button and one section hold, and few
// enough options to choose between at a glance.
const (
	maxQuestion    = 500
	maxLabel       = 75
	maxDescription = 150
	minOptions     = 2
	maxOptions     = 8
)

// The states a question is in. It is open until it is answered or closed, and
// either is final.
const (
	questionOpen     = "open"
	questionAnswered = "answered"
	questionClosed   = "closed"
)

// AskTool is ask_user as a model is offered it, by a chat turn's loop and by a
// coding run's MCP server alike.
var AskTool = infer.ToolDef{
	Name: "ask_user",
	Description: "Ask the person you are working for to choose, when a decision needs them: offer 2 to 8 " +
		"answers and wait for theirs. They see the question as buttons where they are reading your " +
		"work, and may answer in their own words instead. Use it rather than guessing at a choice " +
		"that is theirs to make, or ending your work on a question. It returns their answer, or says " +
		"none came.",
	Schema: json.RawMessage(`{"type":"object","required":["question","options"],"additionalProperties":false,"properties":{` +
		`"question":{"type":"string","maxLength":500,"description":"What to ask, in one or two sentences."},` +
		`"options":{"type":"array","minItems":2,"maxItems":8,"description":"The answers to offer.","items":{"type":"object","required":["label"],"additionalProperties":false,"properties":{` +
		`"label":{"type":"string","maxLength":75,"description":"The answer, as short as it can be said."},` +
		`"description":{"type":"string","maxLength":150,"description":"What choosing it means, when the label does not say."}}}}}}`),
}

// question is one question a run asked.
type question struct {
	ID, Org, Session, Actor string
	To                      client.Reply // where it is asked, and whose it is
	Message                 string       // the message asking it, once posted
	Text                    string
	Options                 []client.Option
	Status, Answer, By      string
	CreatedAt, AnsweredAt   int64
}

// migrateQuestions creates the question table, in the org's one file beside the
// sessions the questions belong to.
func (s *Store) migrateQuestions(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS agent_questions (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  session     TEXT NOT NULL,
  actor       TEXT NOT NULL,
  provider    TEXT NOT NULL DEFAULT '',
  channel     TEXT NOT NULL DEFAULT '',
  thread      TEXT NOT NULL DEFAULT '',
  owner       TEXT NOT NULL DEFAULT '',
  message     TEXT NOT NULL DEFAULT '',
  text        TEXT NOT NULL,
  options     TEXT NOT NULL,
  status      TEXT NOT NULL,
  answer      TEXT NOT NULL DEFAULT '',
  answered_by TEXT NOT NULL DEFAULT '',
  created_at  BIGINT NOT NULL,
  answered_at BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_questions_open ON agent_questions(org, provider, channel, thread, status);`)
	if err != nil {
		return fmt.Errorf("migrate questions: %w", err)
	}
	return nil
}

const questionCols = `id,org,session,actor,provider,channel,thread,owner,message,text,options,status,answer,answered_by,created_at,answered_at`

func (s *Store) putQuestion(ctx context.Context, q question) error {
	opts, _ := json.Marshal(q.Options)
	if err := dialect.Text(q.ID, q.Org, q.Session, q.Actor, q.To.Provider, q.To.Channel, q.To.Thread,
		q.To.User, q.Message, q.Text, q.Status, q.Answer, q.By); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_questions (`+questionCols+`) VALUES (`+placeholders(questionCols)+`)`,
		q.ID, q.Org, q.Session, q.Actor, q.To.Provider, q.To.Channel, q.To.Thread, q.To.User, q.Message,
		q.Text, string(opts), q.Status, q.Answer, q.By, q.CreatedAt, q.AnsweredAt)
	return err
}

func scanQuestion(sc interface{ Scan(...any) error }) (question, error) {
	var q question
	var opts string
	err := sc.Scan(&q.ID, &q.Org, &q.Session, &q.Actor, &q.To.Provider, &q.To.Channel, &q.To.Thread, &q.To.User,
		&q.Message, &q.Text, &opts, &q.Status, &q.Answer, &q.By, &q.CreatedAt, &q.AnsweredAt)
	if err == nil {
		_ = json.Unmarshal([]byte(opts), &q.Options)
	}
	return q, err
}

func (s *Store) getQuestion(ctx context.Context, org, id string) (question, error) {
	return scanQuestion(s.db.QueryRowContext(ctx,
		`SELECT `+questionCols+` FROM agent_questions WHERE org=$1 AND id=$2`, org, id))
}

// openQuestion is actor's latest open question in one conversation.
func (s *Store) openQuestion(ctx context.Context, org, actor string, to client.Reply) (question, error) {
	return scanQuestion(s.db.QueryRowContext(ctx,
		`SELECT `+questionCols+` FROM agent_questions
		 WHERE org=$1 AND provider=$2 AND channel=$3 AND thread=$4 AND status=$5 AND actor=$6
		 ORDER BY created_at DESC LIMIT 1`,
		org, to.Provider, to.Channel, to.Thread, questionOpen, actor))
}

// resolve moves an open question to status and reports whether this call did:
// whichever of an answer and a close lands first is the question's end.
func (s *Store) resolve(ctx context.Context, org, id, status, answer, by string, at int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE agent_questions SET status=$1, answer=$2, answered_by=$3, answered_at=$4 WHERE org=$5 AND id=$6 AND status=$7`,
		status, answer, by, at, org, id, questionOpen)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// The plane calls a question makes, as values so a test can stand in for them.
var postQuestion = plane.Call[client.ChatSendIn, client.ChatSendOut]

// waiters holds, per question, the channel its asker waits on in this process. An
// answer closes it; a poll of the row is the fallback for one given elsewhere.
var waiters = struct {
	sync.Mutex
	m map[string]chan struct{}
}{m: map[string]chan struct{}{}}

// askPoll is how often a waiting asker reads its question back, for an answer
// this process was not told about.
var askPoll = 2 * time.Second

// Ask puts a question to the person session runs for, in the conversation to, and
// waits up to wait for their answer. It answers the run: their answer, or a
// sentence saying none came. An error is a question that could not be asked.
//
// ctx is the RUN's: a run stopped or paused while it waits stops waiting, and its
// question is closed rather than left to be answered into nothing.
func Ask(ctx context.Context, org, session string, to client.Reply, in client.AskIn, wait time.Duration) (string, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return "", err
	}
	if err := checkAsk(in); err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return halted(ctx), nil // stopped or paused before it asked: nobody to ask
	}
	x, err := sto.GetSession(ctx, org, session)
	if err != nil {
		return "", fmt.Errorf("agents: ask: %w", err)
	}
	// The app's conversation, and each run in it, is read on its own page (talk.go),
	// where the question is drawn from its transcript and answered
	// (sessionOps.answer); anywhere else a question needs a conversation to be
	// posted in.
	app := private(x)
	if !app && strings.TrimSpace(to.Channel) == "" {
		return "", errors.New("this run is not being read anywhere a question can be asked; decide for yourself and say what you chose")
	}
	now := time.Now().Unix()
	q := question{
		ID: mint.ID("qst"), Org: org, Session: session, Actor: x.Actor, To: to,
		Text: strings.TrimSpace(in.Question), Options: in.Options, Status: questionOpen, CreatedAt: now,
	}
	if err := sto.putQuestion(ctx, q); err != nil {
		return "", fmt.Errorf("agents: ask: %w", err)
	}
	ch := make(chan struct{})
	waiters.Lock()
	waiters.m[q.ID] = ch
	waiters.Unlock()
	defer func() {
		waiters.Lock()
		delete(waiters.m, q.ID)
		waiters.Unlock()
	}()

	if !app {
		out, err := postQuestion(cloud.For(context.WithoutCancel(ctx), org), manifest.Provider, client.ChatSend, &client.ChatSendIn{
			Org: org, Provider: to.Provider, Channel: to.Channel, ReplyTo: to.Thread, Text: asked(q),
			Question: client.Question{ID: q.ID, Text: q.Text, Options: q.Options, Owner: to.User},
		})
		if err != nil {
			_, _ = sto.resolve(context.WithoutCancel(ctx), org, q.ID, questionClosed, "", "", time.Now().Unix())
			return "", fmt.Errorf("the question could not be shown to the person: %w", err)
		}
		if out != nil && out.MessageID != "" {
			q.Message = out.MessageID
			_, _ = sto.db.ExecContext(ctx, `UPDATE agent_questions SET message=$1 WHERE org=$2 AND id=$3`, q.Message, org, q.ID)
		}
	}
	transcribe(ctx, sto, x, map[string]any{"status": "asked", "question": q.ID, "text": q.Text, "options": q.Options})

	timer := time.NewTimer(wait)
	defer timer.Stop()
	poll := time.NewTicker(askPoll)
	defer poll.Stop()
	for {
		select {
		case <-ch:
		case <-poll.C:
		case <-timer.C:
			return closeQuestion(ctx, sto, q, "No answer came, so the run went on without one.",
				"No answer came within "+wait.Round(time.Minute).String()+". Decide for yourself, and say what you chose and why.")
		case <-ctx.Done():
			shown := "The run stopped before this was answered."
			if errors.Is(context.Cause(ctx), ErrPaused) {
				shown = "The run was paused before this was answered."
			}
			return closeQuestion(ctx, sto, q, shown, halted(ctx))
		}
		got, err := sto.getQuestion(ctx, org, q.ID)
		if err == nil && got.Status == questionAnswered {
			return answerFor(got), nil
		}
	}
}

// halted is what a run that stopped waiting is told: why it stopped.
func halted(ctx context.Context) string {
	if errors.Is(context.Cause(ctx), ErrPaused) {
		return "The run was paused before the question was answered."
	}
	return "The run stopped before the question was answered."
}

// closeQuestion closes q unanswered — unless an answer beat the close, which then
// stands — and takes its options away.
func closeQuestion(ctx context.Context, sto *Store, q question, shown, told string) (string, error) {
	ctx = context.WithoutCancel(ctx)
	closed, err := sto.resolve(ctx, q.Org, q.ID, questionClosed, "", "", time.Now().Unix())
	if err == nil && !closed {
		if got, gerr := sto.getQuestion(ctx, q.Org, q.ID); gerr == nil && got.Status == questionAnswered {
			return answerFor(got), nil
		}
	}
	show(ctx, q, "_"+shown+"_\n"+esc(q.Text))
	return told, nil
}

// answerFor is what the run is told its person answered.
func answerFor(q question) string {
	for _, o := range q.Options {
		if o.Label == q.Answer {
			return "The person chose: " + q.Answer
		}
	}
	return "The person answered in their own words: " + q.Answer
}

// asked is a question as text: the notification a transport shows beside its
// buttons, and the whole message on one that has none.
func asked(q question) string {
	var b strings.Builder
	b.WriteString(q.Text)
	for i, o := range q.Options {
		fmt.Fprintf(&b, "\n%d. %s", i+1, o.Label)
		if o.Description != "" {
			b.WriteString(" — " + o.Description)
		}
	}
	return b.String()
}

// show edits a question's message to text and takes its options away.
func show(ctx context.Context, q question, text string) {
	if q.Message == "" || q.To.Provider != "slack" {
		return // nothing to edit, or a transport that cannot
	}
	if _, err := postQuestion(cloud.For(ctx, q.Org), manifest.Provider, client.ChatSend, &client.ChatSendIn{
		Org: q.Org, Provider: q.To.Provider, Channel: q.To.Channel, ReplyTo: q.To.Thread, Edit: q.Message, Text: text,
	}); err != nil && mounted != nil {
		mounted.Log.Warn("agents: a question's message was not updated", "org", q.Org, "question", q.ID, "err", err)
	}
}

// transcribe puts a question's moment on its session's transcript.
func transcribe(ctx context.Context, sto *Store, x Session, payload map[string]any) {
	b, _ := json.Marshal(payload)
	if e, err := sto.AppendEvent(context.WithoutCancel(ctx), Event{
		ID: mint.ID("evt"), SessionID: x.ID, Org: x.Org, Kind: KindStatus, Actor: x.Actor,
		Payload: string(b), CreatedAt: time.Now().Unix(),
	}); err == nil {
		publishEvent(mounted, x, e)
	}
}

// checkAsk refuses a question a person could not answer from their screen.
func checkAsk(in client.AskIn) error {
	q := strings.TrimSpace(in.Question)
	switch {
	case q == "":
		return errors.New("ask_user needs a question")
	case len(q) > maxQuestion:
		return fmt.Errorf("a question is at most %d characters", maxQuestion)
	case len(in.Options) < minOptions || len(in.Options) > maxOptions:
		return fmt.Errorf("ask_user offers %d to %d options, not %d", minOptions, maxOptions, len(in.Options))
	}
	seen := map[string]bool{}
	for _, o := range in.Options {
		l := strings.TrimSpace(o.Label)
		switch {
		case l == "" || l != o.Label:
			return errors.New("every option needs a label with no space around it")
		case len([]rune(l)) > maxLabel:
			return fmt.Errorf("an option's label is at most %d characters: %q", maxLabel, l)
		case len([]rune(o.Description)) > maxDescription:
			return fmt.Errorf("an option's description is at most %d characters", maxDescription)
		case seen[l]:
			return fmt.Errorf("two options are both %q", l)
		}
		seen[l] = true
	}
	return nil
}

// slackID is the shape of a Slack user id, the one value a question's message
// names someone by.
var slackID = regexp.MustCompile(`^[A-Z0-9]{2,32}$`)

// asks reports whether this run can ask its person: a turn's own run, not an
// agent it called, answering in a conversation — a bridge's, or the app's.
func (l *live) asks(ctx context.Context) bool {
	_, ok := ctx.Value(replyKey{}).(said)
	return l != nil && (ok || talks(l.x)) && agentDepth(ctx) == 0
}

// ask is ask_user in a turn: the question goes to the turn's person where the
// turn is being read, and the turn waits for the answer — no longer than its own
// budget leaves room to go on with it (a completion's worth).
func (l *live) ask(ctx context.Context, args string) string {
	var in client.AskIn
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "error: ask_user's arguments are not {question, options}: " + err.Error()
	}
	var to client.Reply
	if s, ok := ctx.Value(replyKey{}).(said); ok {
		to = s.Reply
	}
	wait := AskWait
	if d, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(d)-modelBudget)
	}
	if wait <= 0 {
		return "error: this turn has no time left to wait for an answer; decide for yourself and say what you chose"
	}
	out, err := Ask(ctx, l.x.Org, l.x.ID, to, in, wait)
	if err != nil {
		return "error: " + err.Error()
	}
	return out
}

// answer settles a question with what its person said or pressed, once, and
// shows it on the question's message.
func answer(ctx context.Context, org, subject string, in *client.AnswerIn) (*client.Answered, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return nil, err
	}
	actor := billingActor(org, subject)
	var q question
	if id := strings.TrimSpace(in.Question); id != "" {
		q, err = sto.getQuestion(ctx, org, id)
	} else {
		q, err = sto.openQuestion(ctx, org, actor, in.Reply)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, zip.ErrNotFound("no question of yours is open there")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "question: %v", err)
	}
	if q.Actor != actor {
		return nil, zip.ErrForbidden("only the person this question is for can answer it")
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, zip.ErrBadRequest("an answer needs text")
	}
	if len(text) > maxControlMsg {
		return nil, zip.ErrBadRequest("answer too long")
	}
	if in.Chosen && !offered(q, text) {
		return nil, zip.ErrBadRequest("that is not one of the question's options")
	}
	by := strings.TrimSpace(in.By)
	ok, err := sto.resolve(ctx, org, q.ID, questionAnswered, text, by, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "answer: %v", err)
	}
	if !ok {
		return nil, zip.Errorf(http.StatusConflict, "that question is no longer open")
	}
	waiters.Lock()
	if ch := waiters.m[q.ID]; ch != nil {
		close(ch)
		delete(waiters.m, q.ID)
	}
	waiters.Unlock()

	how := "answered"
	if in.Chosen {
		how = "chosen"
	}
	line := "✓ " + esc(text)
	if q.To.Provider == "slack" && slackID.MatchString(by) {
		line += " — " + how + " by <@" + by + ">"
	}
	show(ctx, q, line)
	if x, err := sto.GetSession(ctx, org, q.Session); err == nil {
		transcribe(ctx, sto, x, map[string]any{"status": "answered", "question": q.ID, "answer": text, "chosen": in.Chosen})
	}
	return &client.Answered{Question: q.ID, Text: text}, nil
}

func offered(q question, label string) bool {
	for _, o := range q.Options {
		if o.Label == label {
			return true
		}
	}
	return false
}

// esc keeps model text from being read as markup where a question is shown: an
// unescaped `<!channel>` in a question would page a whole workspace.
func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// planeQuestionAnswer answers a question for the person a surface names — a press
// on one of its options, or what they said in its thread.
//
// The org is the caller's plane identity, and the question has to be the named
// person's own: anyone else's answers 403, so a surface can say whose it is.
func planeQuestionAnswer(ctx context.Context, in *client.AnswerIn) (*client.Answered, error) {
	if mounted == nil {
		return nil, fmt.Errorf("%w: agents", cloud.ErrNoPeer)
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("agents: an answer needs the org on the call")
	}
	if strings.TrimSpace(in.Subject) == "" {
		return nil, zip.ErrForbidden("agents: an answer needs the person it is from")
	}
	return answer(ctx, org, in.Subject, in)
}
