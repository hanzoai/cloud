package bot

import "testing"

// reader is the control UI's side of the rule, transcribed from
// ui/src/pages/chat/chat-gateway.ts: take the increment when the snapshot ends
// with it, and otherwise fall back to the snapshot. A test that asserted only
// on what the server produced would prove the server agrees with itself; this
// runs what the client would do with it and asks what ends up on screen.
type reader struct{ held string }

func (r *reader) read(d delta) {
	switch {
	case d.Replace:
		r.held = d.Text
	case r.held == "":
		r.held = d.Full
	case len(d.Full) >= len(d.Text) && d.Full[:len(d.Full)-len(d.Text)] == r.held:
		r.held += d.Text
	default:
		// The increment does not line up with what is held. This is the heal:
		// the snapshot is authoritative and the client takes it whole.
		r.held = d.Full
	}
}

// A stream of chunks reaches a reader as the text the model actually said. This
// is the case one delta per turn never reaches, and the case a model backend
// reaches on its second chunk.
func TestAStreamReadsBackAsWhatWasSaid(t *testing.T) {
	for _, c := range []struct {
		name  string
		chunk []string
	}{
		{"one chunk", []string{"the whole answer"}},
		{"many chunks", []string{"the ", "whole ", "answer"}},
		{"a chunk that adds nothing", []string{"the ", "the ", "whole answer"}},
		{"a revision", []string{"the wrong answer", "the right answer"}},
		{"a revision mid-stream", []string{"one ", "one two ", "ONE TWO THREE"}},
		{"empty first", []string{"", "something"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var (
				r    reader
				said string
				sent int
			)
			// Asserted after every delta, not only at the end. A stream that is
			// wrong in the middle and right at the last chunk still shows a
			// person the wrong text for as long as the turn takes.
			for i, ch := range c.chunk {
				said = stepText(said, ch, c.name)
				d, ok := deltaOf(r.held, said)
				if !ok {
					continue
				}
				sent++
				r.read(d)
				if r.held != said {
					t.Fatalf("after chunk %d the reader holds %q, the model had said %q", i+1, r.held, said)
				}
			}
			if sent == 0 {
				t.Fatal("nothing was sent, so this proved nothing")
			}
		})
	}
}

// stepText builds what has been said so far. A revision case replaces rather
// than appends, which is what makes it a revision.
func stepText(said, chunk, name string) string {
	if name == "a revision" || name == "a revision mid-stream" {
		return chunk
	}
	return said + chunk
}

// Nothing to send raises no event, so a snapshot that did not move does not
// reach a client at all.
func TestAnUnchangedSnapshotSendsNothing(t *testing.T) {
	if _, ok := deltaOf("held", "held"); ok {
		t.Error("an unchanged snapshot produced a delta")
	}
	if _, ok := deltaOf("", ""); ok {
		t.Error("an empty turn produced a delta")
	}
}

// The increment is a tail of the snapshot whenever one exists, and the whole
// text when it does not. A client that appends without checking still lands on
// the right text in the first case, which is why the second must say so.
func TestARevisionIsMarkedRatherThanAppended(t *testing.T) {
	d, ok := deltaOf("the wrong", "the right")
	if !ok {
		t.Fatal("a revision produced no delta")
	}
	if !d.Replace {
		t.Error("a revision was sent as an increment; a client would append it")
	}
	if d.Text != "the right" {
		t.Errorf("a replacement carries %q, want the whole text", d.Text)
	}

	d, ok = deltaOf("the ", "the whole")
	if !ok {
		t.Fatal("a continuation produced no delta")
	}
	if d.Replace {
		t.Error("a continuation was marked as a replacement")
	}
	if d.Text != "whole" {
		t.Errorf("the increment is %q, want only what is new", d.Text)
	}
}
