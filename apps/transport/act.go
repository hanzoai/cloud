package transport

// act.go — a press of one of a run's buttons.
//
// A run's message carries its controls (live.go for a turn, apps/coding
// progress.go for a coding run), and the platform's adapter draws them and hands
// a press back here. The adapter knows the workspace and who pressed; what the
// press MEANS is decided here, beside the words that already mean the same things
// — "stop" and "continue" — and authorized the same way: the presser is resolved
// from the platform's signed identity to a person (chat_identity), and agents
// acts only on that person's own session. Anyone else is refused 403, which the
// adapter tells them privately.
//
// An option pressed on a question a run asked (ask_user) is an answer, authorized
// the same way: only the person the question is for, as agents decides.
//
// A press never posts a second message. The run's own message shows the result:
// the turn or coding run that is still going says it stopped or paused, a turn
// that is continued is rendered again into the very message that was pressed, and
// a paused run that is stopped — which has nothing left running to say so — has
// its message edited here.

import (
	"context"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// codingStoppedText is a stopped coding run's message when nothing running is
// left to write it: what it did so far was pushed when it paused.
const codingStoppedText = "Stopped. Work so far is kept on its branch."

// paused is agents' status for a session whose run is paused.
const paused = "paused"

// exposeAct publishes the press on the plane. Mount calls it.
func exposeAct() {
	cloud.Plane().Post("/transport/act", planeAct,
		zip.WithOperationID(client.TransportAct),
		zip.WithSummary("Stop, pause or continue a run from a button on its message"))
}

// planeAct applies one press of a run's button as the person who pressed it.
//
// The org is the caller's plane identity, from the Slack-verified team, and never
// the payload's. The presser must resolve to a linked person, and the session
// must be theirs: otherwise 403.
func planeAct(ctx context.Context, in *client.TransportActIn) (*client.SessionControlled, error) {
	s := mounted.Load()
	if s == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "transport: not mounted")
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("transport: a press needs the org on the call")
	}
	c := in.Controls
	switch {
	case in.Command == client.SessionAnswer && in.Question == "":
		return nil, zip.ErrBadRequest("transport: an answer names the question it is for")
	case in.Command == client.SessionAnswer:
	case c.Session == "":
		return nil, zip.ErrBadRequest("transport: a press names the run it is for")
	case in.Command != client.SessionStop && in.Command != client.SessionPause && in.Command != client.SessionResume:
		return nil, zip.ErrBadRequest("transport: command must be stop|pause|resume|answer")
	}
	who, err := whoIsAsking(ctx, s, org, Message{
		Transport: in.Provider, Account: in.Account, Sender: Sender{ExternalID: in.User},
	})
	if err != nil {
		return nil, err
	}
	if who.Subject == "" {
		return nil, zip.ErrForbidden("transport: only the person whose run this is can steer it")
	}
	// An option pressed on a question (apps/agents/ask.go): agents answers it for
	// that person, once, and shows the answer on the question's own message.
	if in.Command == client.SessionAnswer {
		if _, err := answerQuestion(ctx, "agent", client.AgentsQuestionAnswer, &client.AnswerIn{
			Subject: who.Subject, Question: in.Question, Text: in.Answer, Chosen: true, By: in.User,
		}); err != nil {
			return nil, err
		}
		return &client.SessionControlled{}, nil
	}
	tr, ok := transportFor(in.Provider)
	if !ok {
		return nil, zip.ErrBadRequest("transport: no transport for " + in.Provider)
	}
	reply := Message{Transport: in.Provider, Account: in.Account, Channel: Channel{ID: in.Channel}, ReplyTo: in.Thread}
	to := client.Reply{Provider: in.Provider, Channel: in.Channel, Thread: in.Thread, User: c.Owner}

	// A paused coding run is continued by a follow-up run, which only the coding
	// engine can start; it narrates into the pressed message.
	if in.Command == client.SessionResume && c.Kind == client.ControlsCoding {
		if _, err := steerCoding(ctx, "agent", client.AgentsCodingSteer, &client.CodingSteerIn{
			Subject: who.Subject, SessionID: c.Session, Reply: to, Update: in.Message,
		}); err != nil {
			return nil, err
		}
		return &client.SessionControlled{Was: paused}, nil
	}

	out, err := controlTurn(ctx, "agent", client.AgentsSessionControl, &client.SessionControlIn{
		Subject: who.Subject, SessionID: c.Session, Command: in.Command,
		Reply: to, Origin: client.Origin{Account: in.Account},
	})
	if err != nil {
		return nil, err
	}
	switch {
	case in.Command == client.SessionResume:
		// The same session, under a new run, shown in the pressed message.
		r := rendering{
			org: org, session: c.Session, run: out.Run, subject: who.Subject, owner: c.Owner,
			reply: reply, message: in.Message, started: time.Now().Unix(),
		}
		s.State.store.openTurn(ctx, s, r)
		spawn(s, org, func() {
			ctx, cancel := turnContext(org)
			defer cancel()
			ctx, span := turnTracer.Start(ctx, "agent.turn "+in.Provider+" continued", trace.WithSpanKind(trace.SpanKindInternal))
			defer span.End()
			span.SetAttributes(
				attribute.String("hanzo.org", org),
				attribute.String("hanzo.chat.provider", in.Provider),
				attribute.String("hanzo.agent.run_id", out.Run),
				attribute.String("hanzo.agent.session_id", c.Session),
			)
			render(ctx, s, tr, r, span)
		})
	case in.Command == client.SessionStop && out.Was == paused:
		// Nothing is running to say it stopped, so the message is told here.
		text := stoppedText
		if c.Kind == client.ControlsCoding {
			text = codingStoppedText
		}
		reply.Text, reply.Edit = text, in.Message
		if _, err := tr.send(ctx, s, org, reply); err != nil {
			s.Log.Warn("transport: a stopped run's message was not updated", "org", org, "session", c.Session, "err", err)
		}
	}
	return out, nil
}
