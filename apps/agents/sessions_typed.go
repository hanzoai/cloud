package agents

// sessions_typed.go is the session WRITE plane's typed half: append a turn, and
// the four steering commands.
//
// THE REFUSAL THAT KEPT THESE RAW HAS EXPIRED, and it is worth reading because it
// was precise rather than vague. It said: the guard gate answers 422 IN BAND with
// the findings that refused the write — {status, code, error, findings:[…]} — and
// zip's error carried {status, code, error} and nothing else, so a typed op would
// silently drop the array that tells an author WHICH secret to rotate. It ended
// "they go typed when zip can express a response with a body per status."
//
// zip.HTTPError.Detail is that: it carries RFC 9457 extension members, MERGED
// under the envelope, so the refusal returns `findings` on the error itself and
// the op is in the registry.
//
// WHAT MOVED, because the wire did move and an earlier draft of this comment said
// it had not: a returned error renders through cloud.ErrorHandler →
// HTTPError.MarshalJSON, which at the pinned zip is RFC 9457 problem-details. So
// the hand-written `error` key the raw handler produced is `detail` now, with
// `type` and `title` beside it. `code` and `findings` — the discriminator a client
// branches on and the payload an author acts on — are unchanged.
//
// It is the right direction: EVERY other refusal in this fleet already renders
// that way, because every propagated error goes through that one handler. The raw
// handler's `error` key was one address answering in a vocabulary the other ~2400
// do not use. TestGuardRefusesSecretInTranscript (provenance_test.go) asserts the
// whole body now — it used to declare an `error` field and never check it, which is
// exactly how a key moves unnoticed.
//
// THE RESERVED SET IS type, title, status, detail AND code — measured, not
// inherited. Members are copied FIRST and the problem-details envelope written
// OVER them, so a domain key with one of those five names is silently displaced.
//
// `error` is NOT among them, and an earlier version of this comment said it was.
// The envelope wrote `error` at zip v1.31.x and writes `detail` now, so a body
// carrying its own nested {"error":{code,message}} — the money wire's — rides
// Detail intact today; verified by marshalling one. What keeps cloud.Denied on its
// middleware is therefore NOT expressibility. It is that DenyEnvelope writes that
// object BARE, with no envelope around it, and Detail would wrap it in one: the
// nested body survives exactly and gains type/title/status/detail beside it. That
// is a wire change to the MONEY path and a decision for whoever owns it, not a
// mechanical conversion.
//
// Here the member is `findings`, which collides with none of the five.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/zap-proto/zip"
)

// refusedLeak is the 422 a guarded write answers with, as a RETURNED error.
//
// It carries everything refuseLeak wrote: `findings` merges under the envelope, so
// the count, the rule that fired, and every finding with its masked preview and
// the SHA-256 fingerprint an author matches against the value they rotate all
// arrive as they did. The sentence is `detail` rather than `error` (see the header
// — the envelope is the fleet's, not this route's). The secret itself is never
// here, because it was never stored.
func refusedLeak(f []leakFinding) error {
	return (&zip.HTTPError{
		Status: http.StatusUnprocessableEntity,
		Code:   "secret_in_transcript",
		Msg: "transcript rejected: " + strconv.Itoa(len(f)) + " secret(s) detected (" +
			f[0].Rule + "). Secrets belong in KMS and are referenced by name; rotate the exposed value.",
	}).With(map[string]any{"findings": f})
}

// eventIn appends one turn to a session's transcript.
type eventIn struct {
	// ID is the session to append to, from the path.
	ID string `json:"id"`
	// Kind is what this turn IS: message, tool-call, spawn, log, status, control
	// or progress. Anything else is refused — the vocabulary is closed so a
	// reader can branch on it.
	Kind string `json:"kind" url:"-"`
	// Payload is the turn's own body, any valid JSON up to 64 KiB. It is SCANNED
	// for credentials before it is stored, and a hit refuses the whole append with
	// 422 and the findings — this plane never redacts a secret into a transcript
	// it then keeps.
	//
	// Its SHAPE is the writer's business for every kind but one: a `progress`
	// turn is the run reporting on itself in a shape this surface publishes —
	// {"pct":0-100 (optional), "phase":"queued|running|blocked|done",
	// "activity":"≤120 chars"} — so a malformed one is refused with 400 instead
	// of being stored as prose nobody reads.
	Payload json.RawMessage `json:"payload,omitempty" url:"-"`
	// Actor is who produced the turn. Empty takes the validated caller, which is
	// what an agent writing its own transcript wants; naming one is for a surface
	// recording on somebody else's behalf.
	Actor string `json:"actor,omitempty" url:"-"`
}

// AppendEvent records one turn of a session's transcript and answers 201 with it.
//
// A `progress` turn additionally MOVES THE SESSION'S PROGRESS, marked as the run's
// own word rather than an estimate, and pushes the updated session onto the live
// stream — so a board's bar follows the run without polling and without a second
// write path. See progress.go.
//
// THE TURN IS SCANNED BEFORE IT IS STORED. The same engine the code-security
// surface runs reads the payload at this boundary, and a credential in it refuses
// the append with 422 rather than redacting it — a redacted transcript is one
// that still had the secret in it once, and this way the author learns which
// value to rotate. The refusal carries every finding: the rule, the severity, the
// line, a MASKED preview and the fingerprint. The secret is never in the answer.
func (o sessionOps) appendEvent(ctx context.Context, in *eventIn) (*eventView, error) {
	sto, org, err := tenantStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	x, err := actable(ctx, sto, org, in.ID)
	if err != nil {
		return nil, err
	}
	// The app's conversation is written by its turns alone (talk.go), and its coding
	// runs by the coding plane (LogSessionEvent): what they say is what its next turn
	// is handed as the conversation, so no caller adds to either.
	if private(x) {
		return nil, zip.ErrForbidden("a conversation is written by its turns; say something with POST /v1/agent/turns")
	}
	kind := strings.TrimSpace(in.Kind)
	if !validKind(kind) {
		return nil, zip.ErrBadRequest("kind must be message|tool-call|spawn|log|status|control")
	}
	if len(in.Payload) > maxEventPayload {
		return nil, zip.ErrBadRequest("payload too large")
	}
	if len(in.Payload) > 0 && !json.Valid(in.Payload) {
		return nil, zip.ErrBadRequest("payload must be valid JSON")
	}
	if leaks := guardEvent(string(in.Payload)); leaks != nil {
		return nil, refusedLeak(leaks)
	}
	// A progress turn is read BEFORE it is stored, so a malformed report is
	// refused whole rather than landing in the transcript as a turn that moved
	// nothing. Every other kind's payload stays opaque.
	var report Progress
	if kind == KindProgress {
		p, perr := parseReport(in.Payload)
		if perr != nil {
			return nil, perr
		}
		report = p
	}
	actor := strings.TrimSpace(in.Actor)
	if actor == "" {
		actor = billingActor(org, callerUser(ctx))
	}
	if len(actor) > maxActor {
		return nil, zip.ErrBadRequest("actor too long")
	}
	e, err := sto.AppendEvent(ctx, Event{
		ID: mint.ID("evt"), SessionID: in.ID, Org: org, Kind: kind, Actor: actor,
		Payload: string(in.Payload), CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "append: %v", err)
	}
	publishEvent(o.s, x, e)
	if kind == KindProgress {
		if err := reported(ctx, o.s, sto, org, x, report, e); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "progress: %v", err)
		}
	}
	v := toEventView(e)
	return &v, nil
}

// controlIn steers a running session. pause, resume and stop usually carry no
// body at all; message requires one of the two fields.
type controlIn struct {
	// ID is the session to steer, from the path.
	ID string `json:"id"`
	// Message is free text for the running agent, up to 16 KiB, scanned for
	// credentials before it is stored like the payload. On a stop it is recorded
	// as the cancellation reason.
	Message string `json:"message,omitempty" url:"-"`
	// Payload is a structured argument for the command: any valid JSON up to
	// 64 KiB, scanned for credentials before it is stored (a hit refuses the
	// command with 422 and the findings). A task-backed session receives it as
	// the forwarded signal's argument.
	Payload json.RawMessage `json:"payload,omitempty" url:"-"`
}

// controlResult is what a steering command answers.
type controlResult struct {
	// Command is the verb that was recorded: pause, resume, stop or message.
	Command string `json:"command"`
	// Event is the durable control event the command became. The intent is
	// recorded whether or not it reached an engine, which is what makes a
	// stream-consuming surface able to act on it.
	Event eventView `json:"event"`
	// Forwarded is whether the command also reached the durable-execution engine.
	// FALSE IS NOT A FAILURE: a session with no workflow link, or a deployment
	// with no tasks backend, is record-only by design. A forward that was
	// attempted and failed is a 502, never a false here.
	Forwarded bool `json:"forwarded"`
	// Run is the run a resume started, under the same session. Empty for every
	// other command.
	Run string `json:"run,omitempty"`
}

// PauseSession asks a running session to pause. Recorded durably, and forwarded
// to the durable-execution engine when the session is task-backed.
//
// A chat turn's run is paused where it runs: it stops, the session stays live as
// `paused`, and its sandbox is PARKED — the pod stops and every file stays, with
// nothing billed for compute — until a resume picks it up.
func (o sessionOps) pause(ctx context.Context, in *controlIn) (*controlResult, error) {
	return o.control(ctx, in, CmdPause)
}

// ResumeSession asks a paused session to continue, on the same terms as a pause.
//
// A chat turn that was paused or STOPPED runs again: the same session, in the same
// sandbox with every file it had written, told `message` next ("Continue where you
// left off." when there is none). Only the person whose turn it was may resume
// it, since the agent runs as them.
func (o sessionOps) resume(ctx context.Context, in *controlIn) (*controlResult, error) {
	return o.control(ctx, in, CmdResume)
}

// StopSession ends a running session. `message` is recorded as the cancellation
// reason, which is what a later reader of the transcript sees.
//
// STOPPING IS NOT DELETING: the session, its transcript and anything it produced
// stay readable. A session that has already finished is 409 rather than a second
// stop.
func (o sessionOps) stop(ctx context.Context, in *controlIn) (*controlResult, error) {
	return o.control(ctx, in, CmdStop)
}

// MessageSession sends a steering message to a running session — the endpoint a
// human or another agent interrupts through. It requires a `message` or a
// `payload`; the other three commands do not.
//
// A chat turn's run READS it: the message is handed to the model as the person's
// next words at its next step, and a message that arrives while the model is
// writing its answer gets a step of its own before the answer stands. A turn that
// has already answered takes no more and answers 409 — what is said after an
// answer is a new turn.
func (o sessionOps) message(ctx context.Context, in *controlIn) (*controlResult, error) {
	return o.control(ctx, in, CmdMessage)
}

// control is the HTTP half of the one core the four commands share (apply): the
// org the gateway validated, and the caller it names. The session is one the
// caller may see (visible): a member steers the runs they started, a SuperAdmin
// and an admin of a customer's org the org's, and another member's answers as
// absent.
func (o sessionOps) control(ctx context.Context, in *controlIn, command string) (*controlResult, error) {
	sto, org, err := tenantStore(ctx, &o.s.State)
	if err != nil {
		return nil, err
	}
	x, err := actable(ctx, sto, org, in.ID)
	if err != nil {
		return nil, err
	}
	return apply(o.s, ctx, sto, x, billingActor(org, callerUser(ctx)), in, command, client.Reply{}, client.Origin{})
}

// apply is the one core the four commands share, so none can drift from the
// others about authorization, the guard gate, or what forwarding means — and so
// the chat surfaces' plane op (steer.go) and the HTTP API are one path. actor is
// who is asking; to and from are where a resumed turn is being read, empty off a
// chat surface.
func apply(s *cloud.Service[state], ctx context.Context, sto *Store, x Session, actor string, in *controlIn, command string,
	to client.Reply, from client.Origin) (*controlResult, error) {
	org := x.Org
	// The app's conversation is moved from the app (talk.go): a bridge that named it
	// would narrate its turn into a channel and pay for it from the bridge's call.
	if private(x) && strings.TrimSpace(to.Channel) != "" {
		return nil, zip.ErrForbidden("a Dev conversation goes on in Dev")
	}
	// A turn is resumed from stopped as well as from paused: stopping one parks
	// its sandbox for exactly this.
	turn := command == CmdResume && resumable(x)
	if isTerminalStatus(x.Status) && !turn {
		return nil, zip.Errorf(http.StatusConflict, "session is %s; cannot %s a finished session", x.Status, command)
	}
	if len(in.Message) > maxControlMsg {
		return nil, zip.ErrBadRequest("message too long")
	}
	if len(in.Payload) > maxEventPayload {
		return nil, zip.ErrBadRequest("payload too large")
	}
	if len(in.Payload) > 0 && !json.Valid(in.Payload) {
		return nil, zip.ErrBadRequest("payload must be valid JSON")
	}
	if leaks := append(guardEvent(in.Message), guardEvent(string(in.Payload))...); leaks != nil {
		return nil, refusedLeak(leaks)
	}
	if command == CmdMessage && strings.TrimSpace(in.Message) == "" && len(in.Payload) == 0 {
		return nil, zip.ErrBadRequest("message requires a 'message' or 'payload'")
	}
	// The run of a resumed turn is the agent acting AS the person whose turn it
	// was, so nobody else may start it. The org already scoped the lookup.
	if turn && actor != x.Actor {
		return nil, zip.ErrForbidden("only the person whose turn this was can resume it")
	}
	// What the resumed turn runs on is read BEFORE the resume is recorded, so the
	// app's conversation (talk.go) does not hand its run the words twice: once as
	// what was said before, and again as what it is told now.
	var ask *client.RunOnBehalfIn
	var t terms
	if turn {
		sub, _ := strings.CutPrefix(x.Actor, org+"/")
		ask = &client.RunOnBehalfIn{
			Subject: sub, Input: cmp.Or(strings.TrimSpace(in.Message), carryOn), Reply: to, Origin: from,
		}
		var err error
		if t, err = termsOf(ctx, sto, x, ask); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "resume: %v", err)
		}
	}

	if command == CmdStop {
		e, forwarded, err := stop(ctx, sto, x, reasonOf(in.Message))
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "stop: %v", err)
		}
		return &controlResult{Command: command, Event: toEventView(e), Forwarded: forwarded}, nil
	}

	body := controlReq{Message: in.Message, Payload: in.Payload}
	cp, _ := json.Marshal(controlPayload{Command: command, Message: in.Message, Payload: in.Payload})
	// A message to a TURN is its next input, and a turn that has answered takes
	// none (steer.go): the check and the record are one step.
	record := sto.AppendEvent
	if command == CmdMessage && x.Run != "" {
		record = sto.Steer
	}
	e, err := record(ctx, Event{
		ID: mint.ID("evt"), SessionID: x.ID, Org: org, Kind: KindControl,
		Actor: actor, Payload: string(cp), CreatedAt: time.Now().Unix(),
	})
	if errors.Is(err, errAnswered) {
		return nil, zip.Errorf(http.StatusConflict, "the turn has already answered; say it as a new turn")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "record control: %v", err)
	}
	publishEvent(s, x, e)

	switch {
	// A TURN runs here, so it is resumed here: nothing else holds it.
	case turn:
		st, err := resumeTurn(s, ctx, sto, x, ask, t)
		if err != nil {
			return nil, err
		}
		return &controlResult{Command: command, Event: toEventView(e), Run: st.RunID}, nil
	// A run executing HERE — a turn, or a coding run (coding holds its own; Hold)
	// — is paused here: it stops, and its session stays live, paused.
	case command == CmdPause && running.interrupt(ctx, x.ID, ErrPaused):
		at := time.Now().Unix()
		if ok, err := sto.Pause(ctx, org, x.ID, at); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "pause: %v", err)
		} else if ok {
			x.Status, x.EndedAt, x.UpdatedAt = StatusPaused, at, at
			publishSession(s, x, 0, 0)
		}
		return &controlResult{Command: command, Event: toEventView(e)}, nil
	}

	// The intent is ALREADY durably recorded, so a forward failure is reported
	// without losing the command.
	forwarded := false
	if x.TaskWorkflowID != "" && s.State.tasks != nil && s.State.tasks.Enabled() {
		ferr := s.State.tasks.Signal(ctx, x.TaskWorkflowID, x.TaskRunID, command, signalPayload(body))
		if ferr != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "control recorded but tasks forward failed: %v", ferr)
		}
		forwarded = true
	}
	return &controlResult{Command: command, Event: toEventView(e), Forwarded: forwarded}, nil
}

// carryOn is what a resumed turn is told when its resume says nothing.
const carryOn = "Continue where you left off."

// callerUser is the validated user id a write is attributed to, or empty off the
// HTTP path — where there is no attested caller and the org alone names the actor.
func callerUser(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return c.User()
	}
	return ""
}
