package agents

// attest.go — where a fact the agent files during a chat turn came from.
//
// A fact filed in a turn was said in a conversation, and that message is its
// provenance. The model cannot know the message's address and is never believed
// about it, the same rule replyto.go keeps for a run's channel: after the model has
// chosen to file and before the call goes out, the call is stamped with the
// turn's own message, over whatever the model wrote there — source
// "<provider>://<account>/<channel>/<message>", evidence its permalink. Both
// graph ops that file are stamped: graphAssert on every assertion, graphIngest
// on the source every assertion it records is named for.
//
// A fact is filed only from a channel everyone in the workspace can read. The graph
// is read by the whole organization, so a fact said in a DM or a private channel
// and filed there would be read by people the channel was closed to.
//
// WHO filed it is not stamped here and cannot be. The graph takes `by` from the
// validated principal and never from the body (apps/graph/graph.go), and a
// dispatch runs as the run's own actor — the Slack user's linked Hanzo identity
// (surface.go) — so the attribution is already the person who said it.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// The graph ops that file. Each declares its own id, so the MCP server publishes
// it unrenamed (client/verbs.go, Phrase).
const (
	assertOp = "graphAssert"
	ingestOp = "graphIngest"
)

// errUnshared refuses a filing from a channel the organization cannot read.
var errUnshared = errors.New("not filed: the graph is read by the whole organization, " +
	"and this conversation is not a public channel")

// attest stamps the turn's message onto a call that files facts, in every
// spelling of the call, and refuses one from a channel that is not public. A turn
// with no conversation behind it — the app surface, a schedule — stamps nothing:
// the model's source is then the caller's, as it is for any other client of the
// graph.
func attest(ctx context.Context, name, args string) (string, error) {
	s, ok := ctx.Value(replyKey{}).(said)
	if !ok {
		return args, nil
	}
	source, evidence := cite(s)
	out, asserts := amend(name, args, assertOp, func(input string) string {
		// The batch in the graph's own shape: graphAssertIn holds nothing else,
		// and re-encoding it leaves no second spelling of the key behind.
		var in struct {
			Assertions []json.RawMessage `json:"assertions"`
		}
		if json.Unmarshal([]byte(input), &in) != nil {
			return input
		}
		for i, f := range in.Assertions {
			in.Assertions[i] = json.RawMessage(set(set(string(f), "source", source), "evidence", evidence))
		}
		b, err := json.Marshal(in)
		if err != nil {
			return input
		}
		return string(b)
	})
	out, ingests := amend(name, out, ingestOp, func(input string) string { return set(input, "source", source) })
	if (asserts || ingests) && !s.Public {
		return "", errUnshared
	}
	return out, nil
}

// cite is the message a turn answers: its address, and the page a person opens
// to read it. The address is positional — workspace, channel, message — so a turn
// that answers no message (a slash command) still names its channel. Only Slack
// has a permalink this process can build without asking the workspace: Slack
// resolves slack.com/archives to whichever workspace the reader is signed in to.
func cite(s said) (source, evidence string) {
	source = strings.TrimSuffix(s.Provider+"://"+s.Account+"/"+s.Channel+"/"+s.Message, "/")
	if s.Provider != "slack" || s.Message == "" {
		return source, ""
	}
	evidence = "https://slack.com/archives/" + s.Channel + "/p" + strings.ReplaceAll(s.Message, ".", "")
	if s.Thread != "" && s.Thread != s.Message {
		evidence += "?thread_ts=" + s.Thread + "&cid=" + s.Channel
	}
	return source, evidence
}
