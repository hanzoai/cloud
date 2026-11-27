package transport

// reply_test.go pins WHERE an answer lands, which is most of whether a bot reads
// as a colleague or as a bot.
//
// The four cases are the four places a reply can go — the thread it was asked
// in, the transport it was asked in, a NEW thread under the ask, and a DM. Every
// turn below reports progress before it answers, so both sends are checked for
// place: progress in one place and its answer in another would be a worse bot
// than one that always threaded.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
)

// spoke is an ask typed at the TOP LEVEL of a transport: in no thread, carrying
// its own id, which is the address a thread would be rooted at if one were
// wanted.
func spoke(text string) Message {
	return Message{
		Transport: "slack",
		Account:   "t0fake",
		Channel:   Channel{ID: "C0FAKE", Kind: ChannelGroup},
		ID:        "1699999999.000200",
		Sender:    Sender{ExternalID: "U0FAKE", Org: "acme"},
		Text:      text,
	}
}

// answeredSlowly runs one turn that reports progress and then answers, and
// returns every send it made.
func answeredSlowly(t *testing.T, m Message, output string) []said {
	t.Helper()
	recordTurns(t)
	linked(t)
	(&agent{pages: []*client.TurnEvents{
		{Events: []client.TurnEvent{progress(1, "Working")}, Next: 1},
		{Events: []client.TurnEvent{{Seq: 2, Kind: client.TurnAnswer, Text: output}}, Next: 2, Outcome: client.TurnCompleted},
	}}).install(t)

	h := &heard{}
	turn(service(), listening(h, true), "acme", m)
	got := h.all()
	if h.posts() != 1 {
		t.Fatalf("a turn must be one message, got %d posts: %+v", h.posts(), got)
	}
	if h.last().text != output {
		t.Fatalf("the message must end as the answer, got %q", h.last().text)
	}
	return got
}

// landed asserts every post of the turn went to the same one place.
func landed(t *testing.T, got []said, channel, replyTo string) {
	t.Helper()
	for i, s := range got {
		if s.channel != channel || s.replyTo != replyTo {
			t.Fatalf("message %d (%q) landed in %s/%q, want %s/%q",
				i, s.text, s.channel, s.replyTo, channel, replyTo)
		}
	}
}

// A message asked IN a thread is answered in that thread. Moving it out would
// publish a side conversation to the whole channel, which is the one failure worse
// than burying an answer in a thread nobody opens.
func TestAThreadedAskIsAnsweredInItsThread(t *testing.T) {
	m := spoke("what broke last night")
	m.Channel.Kind = ChannelThread
	m.ReplyTo = "1699999999.000100"

	got := answeredSlowly(t, m, "the database ran out of connections")
	landed(t, got, m.Channel.ID, m.ReplyTo)
}

// A message said OUT LOUD in the transport is answered out loud in the transport.
// This is the case that read as a bot: the reply used to thread under the ask
// itself, so a one-line answer to a one-line question went behind a "1 reply"
// nobody clicks.
func TestATopLevelAskIsAnsweredInTheChannel(t *testing.T) {
	got := answeredSlowly(t, spoke("what broke last night"), "the database ran out of connections")
	// Empty replyTo IS the transport — both posts, so the promise and the answer
	// are in the same place.
	landed(t, got, "C0FAKE", "")
}

// An ask that is really a side discussion opens a thread UNDER ITSELF, so a wall
// of output does not bury the channel. A fenced code block is someone showing code,
// and the answer to code is more code.
func TestAnAsideOpensAThreadUnderTheAsk(t *testing.T) {
	m := spoke("this keeps failing\n```\npanic: assignment to entry in nil map\n```")

	got := answeredSlowly(t, m, "the map is never made; make it in New")
	// Rooted at the ask's OWN id — the message becomes the thread's parent.
	landed(t, got, m.Channel.ID, m.ID)
}

// A DM is unchanged: already a private two-party channel, so there is no transport to
// bury and nobody a side discussion would spare. Even an ask shaped like an
// aside stays inline here, which is what keeps the rule above from following a
// person into their DMs.
func TestADirectMessageIsAnsweredInline(t *testing.T) {
	m := spoke("look at this\n```\npanic: assignment to entry in nil map\n```")
	m.Channel = Channel{ID: "D0FAKE", Kind: ChannelDM}

	got := answeredSlowly(t, m, "the map is never made")
	landed(t, got, "D0FAKE", "")
}

// The rule itself, at its edges, so the threshold is a decision and not an
// accident of whatever the cases above happen to say.
func TestAsideIsALongAskOrOneCarryingCode(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
		want bool
	}{
		{"a question", "what broke last night", false},
		{"a question over a few lines", "what broke\nlast\nnight", false},
		{"exactly the limit", strings.Repeat("line\n", asideLines-1) + "line", false},
		{"one line past it", strings.Repeat("line\n", asideLines) + "line", true},
		{"a fenced block", "why\n" + codeFence + "\npanic\n" + codeFence, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := aside(c.text); got != c.want {
				t.Fatalf("aside(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

// A transport that reports no message id cannot have a thread opened under the
// ask, so the answer stays in the channel rather than being rooted at nothing.
// Discord, Teams and the app surface all arrive this way.
func TestWithoutAMessageIdAnAsideStaysInTheChannel(t *testing.T) {
	m := spoke("look\n" + codeFence + "\npanic\n" + codeFence)
	m.ID = ""

	if to := answerIn(m); to != "" {
		t.Fatalf("with no id to root a thread at, the answer belongs in the channel, got %q", to)
	}
}
