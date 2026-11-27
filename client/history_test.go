package client

import (
	"strings"
	"testing"
)

// The budget every surface hands a conversation in: the newest turns that fit,
// oldest first, each cut to its own cap with an answer's tools kept whole.
func TestFitKeepsTheNewestTurnsWithinTheBudget(t *testing.T) {
	var turns []Turn
	for i := range 20 {
		turns = append(turns, Turn{Text: strings.Repeat(string(rune('a'+i)), 6000)})
	}
	turns = append(turns, Turn{Self: true, Text: strings.Repeat("p", 6000) + ToolsMarker + "- git(repo=site) -> ok"})
	got := Fit(turns)
	total := 0
	for _, x := range got {
		if len(x.Text) > TurnBytes+64 {
			t.Fatalf("a turn of %d bytes was handed over", len(x.Text))
		}
		total += min(len(x.Text), TurnBytes)
	}
	if total > HistoryBytes {
		t.Fatalf("%d bytes handed over, over the %d budget", total, HistoryBytes)
	}
	last := got[len(got)-1]
	if !last.Self || !strings.HasSuffix(last.Text, ToolsMarker+"- git(repo=site) -> ok") {
		t.Fatalf("the newest turn, its tools kept, must close the conversation: %q", last.Text[len(last.Text)-40:])
	}
	if got[0].Text[0] == 'a' {
		t.Fatal("the oldest turns were kept and the newest dropped")
	}
	if Fit(nil) == nil || len(Fit([]Turn{{Text: "hi"}})) != 1 {
		t.Fatal("a short conversation is handed over whole")
	}
}
