package bot

import "strings"

// The rule for turning what a model has said so far into what a client is told
// next. It lives here, alone, because it is the one piece of this turn that a
// reader cannot check by looking at the answer: an increment that does not line
// up with what the client already holds shows as text that is subtly wrong
// rather than as an error.
//
// A client keeps the text it has and appends what arrives. So the increment is
// the tail of the full text past what it holds — and when the full text is not
// a continuation of that, because a model revised what it had already said,
// there is no tail to send and the whole text replaces it. Both cases carry the
// full text beside the increment, and a client that cannot line the two up
// falls back to it, which is why a stream can heal instead of drifting.
//
// One delta per turn is the only case this surface reaches today, and it is
// also the case that hides the mistake: with nothing held, the increment IS the
// whole text and any implementation looks right. A model backend that streams
// reaches the other case on its second chunk. So the rule is written once, and
// a turn asks for it rather than computing it.
type delta struct {
	Text    string // what to append, or the whole text when Replace is set
	Replace bool   // the held text is not a prefix of this; take Text as all of it
	Full    string // everything said so far — what a client heals from
}

// deltaOf reads the increment between what a client holds and what has been
// said. held is what previous deltas of this turn already sent, "" at the start.
// It answers false when there is nothing to send, so an unchanged snapshot
// raises no event.
func deltaOf(held, full string) (delta, bool) {
	switch {
	case full == held:
		return delta{}, false
	case held == "":
		return delta{Text: full, Full: full}, true
	case strings.HasPrefix(full, held):
		return delta{Text: full[len(held):], Full: full}, true
	default:
		// The model revised what it had said. There is no increment that gets a
		// client from what it holds to this, so it is replaced outright.
		return delta{Text: full, Replace: true, Full: full}, true
	}
}
