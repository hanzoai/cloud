package manifest

import "testing"

// The stage vocabulary is CLOSED (HIP-0139 §8): a row is ga, beta or alpha, and
// ga is the absence of a word.
//
// A misspelling is what this catches, and it is silent in every direction that
// matters. StageOf answers whatever the row says; cloud.Stage refuses on any
// non-empty value, so the capability still goes behind a flag; and the weave
// stamps the misspelling onto every operation and out through public.yaml into
// the SDKs, where "x-stage: bet" is a word no consumer has a rule for. Nothing
// else goes red.
func TestStageVocabulary(t *testing.T) {
	for _, a := range Apps {
		switch a.Stage {
		case "", Beta, Alpha:
		default:
			t.Errorf("%s: stage %q — a row is ga (empty), %q or %q, and nothing else",
				a.Name, a.Stage, Beta, Alpha)
		}
	}
}

// staged is EVERY row that is not ga, by name, and it is a golden so that
// promoting or demoting a capability is a visible diff rather than a number that
// moved.
//
// It is spelled as names and not as a count for the reason the frozen order is
// spelled as names: a count that stays 44 while two rows swap stages is a green
// test over a changed product. The list is the fact; its length is a consequence.
var staged = map[string]string{
	// Not customer surface: the R&D evidence plane and the launch-control gate for
	// Hanzo's own hosted services. Both are ours, so neither belongs in the public
	// contract, the generated SDKs or the agent tool list.
	//
	// graph is no longer here. It is the assertion plane, it carries retention,
	// and it is a customer capability — so it publishes, and TestStageOfReadsTheRow
	// below asks a still-staged row instead.
	"admission": Alpha,
	"research":  Alpha,
}

func TestStagedRowsAreTheOnesDeclared(t *testing.T) {
	have := map[string]string{}
	for _, a := range Apps {
		if !a.GA() {
			have[a.Name] = a.Stage
		}
	}
	for name, want := range staged {
		got, listed := have[name]
		if !listed {
			t.Errorf("%s is ga in Apps and %s here — a promotion is a deliberate edit to both", name, want)
			continue
		}
		if got != want {
			t.Errorf("%s: Apps says %q, this list says %q", name, got, want)
		}
	}
	for name, got := range have {
		if _, listed := staged[name]; !listed {
			t.Errorf("%s is %q in Apps and absent here — a capability held back from customers is "+
				"a product decision, so say it in both places", name, got)
		}
	}
}

// StageOf reads the row, and a name that was never routed here is ga — the same
// answer the zero value gives, so a caller needs no second branch for it.
func TestStageOfReadsTheRow(t *testing.T) {
	if got := StageOf("research"); got != Alpha {
		t.Errorf("StageOf(research) = %q, want %q", got, Alpha)
	}
	if got := StageOf("graph"); got != "" {
		t.Errorf("StageOf(graph) = %q, want ga", got)
	}
	if got := StageOf("iam"); got != "" {
		t.Errorf("StageOf(iam) = %q, want ga", got)
	}
	if got := StageOf("nosuchapp"); got != "" {
		t.Errorf("StageOf(nosuchapp) = %q, want ga", got)
	}
}
