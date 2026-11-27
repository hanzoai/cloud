package tools

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// How well detect picks a skill for a Chat message, measured the way hanzo.app
// uses it (@hanzo/app chosen.ts): detect's skills at its default threshold, the
// top 8, then the ones at 0.8 or more that the person could pick from `+` (an
// org's own skills, or the built-in code-review, dev and ui; never an API
// descriptor such as audit_audit), at most two.
//
// testdata/detect/messages.json holds realistic messages and every skill that
// would be a right pick for each, or none. "tune" was used to choose the
// ranker's constants; "held" was scored once, after. org.json is the skills one
// org holds from its repositories, as its listing answered on 2026-11-20 (40 of
// them stored with the description ">"); the brand's catalogue is apps/skills'.
//
// Before this ranking (word overlap; Kai, asked 96 yes/no questions, never
// answered inside its 3 s), an org with its own skills: "tune" 14/25 picks right
// (0.56) and a fitting skill found for 13 of 32 messages (0.41), "held" 11/24
// (0.46) and 9 of 28 (0.32); an org with none: "tune" 3/7 (0.43) and 2 of 11
// (0.18), "held" 4/6 (0.67) and 2 of 10 (0.20). Messages no skill fits were left
// alone 12/12, 12/12, 31/33 and 29/30.

type labelledMessage struct {
	Prompt string   `json:"prompt"`
	Skills []string `json:"skills"`
}

type rankScenario struct {
	name   string
	cands  []Tool
	choose map[string]bool
}

func rankScenarios(t *testing.T) []rankScenario {
	t.Helper()
	var o struct{ Skills []Skill }
	readJSON(t, "testdata/detect/org.json", &o)
	var ix struct {
		Skills []struct{ Name, Description string }
	}
	readJSON(t, "../skills/catalog/hanzo/index.json", &ix)
	var brand []Tool
	for _, s := range ix.Skills {
		brand = append(brand, Tool{Name: "skill_" + s.Name, Source: SourceSkill, Description: s.Description})
	}
	own, choose := slices.Clone(brand), map[string]bool{}
	for _, s := range o.Skills {
		// What the store lists: a stored block header is read again from the file.
		d := s.Description
		if blockScalar(strings.TrimSpace(d)) {
			d = frontmatterDescription(s.Content)
		}
		own = append(own, Tool{Name: "skill_" + s.Name, Source: SourceSkill, Description: d})
		choose[s.Name] = true
	}
	none := slices.Clone(brand)
	for _, d := range defaultSkillNames {
		none = append(none, Tool{Name: "skill_" + d, Source: SourceSkill, Description: builtinSkillSummaries[d]})
	}
	return []rankScenario{
		{"an org with its own skills", own, choose},
		{"an org with none of its own", none, map[string]bool{"code-review": true, "dev": true, "ui": true}},
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// chatPicks is what hanzo.app takes from detect's answer for one message.
func chatPicks(probs map[string]float64, choose map[string]bool) []string {
	type rec struct {
		name string
		p    float64
	}
	var recs []rec
	for n, p := range probs {
		if p >= defaultDetectThreshold && strings.HasPrefix(n, "skill_") {
			recs = append(recs, rec{n, p})
		}
	}
	slices.SortFunc(recs, func(a, b rec) int {
		if c := cmp.Compare(b.p, a.p); c != 0 {
			return c
		}
		return cmp.Compare(a.name, b.name)
	})
	var out []string
	for _, r := range recs[:min(len(recs), 8)] {
		n := strings.TrimPrefix(r.name, "skill_")
		if r.p >= 0.8 && !strings.Contains(n, "_") && choose[n] && len(out) < 2 {
			out = append(out, n)
		}
	}
	return out
}

type rankScore struct {
	picks, right int // skills chosen, and how many of them fit
	want, found  int // messages a skill fits, and how many got one that does
	none, quiet  int // messages no skill fits, and how many got none
	wrong        []string
}

func (s rankScore) precision() float64 { return float64(s.right) / float64(max(s.picks, 1)) }
func (s rankScore) recall() float64    { return float64(s.found) / float64(max(s.want, 1)) }

func (s rankScore) String() string {
	return fmt.Sprintf("right %d/%d (%.2f), found %d/%d (%.2f), none left alone %d/%d",
		s.right, s.picks, s.precision(), s.found, s.want, s.recall(), s.quiet, s.none)
}

func scoreRanking(sc rankScenario, set []labelledMessage) rankScore {
	var s rankScore
	for _, m := range set {
		var want []string
		for _, n := range m.Skills {
			if sc.choose[n] {
				want = append(want, n)
			}
		}
		got := chatPicks(relevance(m.Prompt, sc.cands), sc.choose)
		s.picks += len(got)
		hit := false
		for _, g := range got {
			if slices.Contains(want, g) {
				s.right++
				hit = true
			} else {
				s.wrong = append(s.wrong, fmt.Sprintf("%q chose %s, want %v", m.Prompt, g, want))
			}
		}
		switch {
		case len(want) == 0:
			s.none++
			if len(got) == 0 {
				s.quiet++
			}
		default:
			s.want++
			if hit {
				s.found++
			}
		}
	}
	return s
}

func TestDetectRanking(t *testing.T) {
	var sets struct{ Tune, Held []labelledMessage }
	readJSON(t, "testdata/detect/messages.json", &sets)
	// The floors sit a little under what this ranking measured, so a change to
	// the catalogue moves them without failing; a worse ranking fails.
	floors := map[string][2]float64{ // precision, recall
		"tune/an org with its own skills":  {0.95, 0.80}, // measured 29/29, 27/32
		"held/an org with its own skills":  {0.80, 0.70}, // 24/28, 21/28
		"tune/an org with none of its own": {0.80, 0.50}, // 8/9, 7/12
		"held/an org with none of its own": {0.55, 0.45}, // 7/11, 5/10
	}
	for _, sc := range rankScenarios(t) {
		for name, set := range map[string][]labelledMessage{"tune": sets.Tune, "held": sets.Held} {
			s := scoreRanking(sc, set)
			t.Logf("%s, %s: %v", name, sc.name, s)
			for _, w := range s.wrong {
				t.Logf("    %s", w)
			}
			f := floors[name+"/"+sc.name]
			if s.precision() < f[0] || s.recall() < f[1] {
				t.Errorf("%s, %s: precision %.2f recall %.2f, want at least %.2f and %.2f", name, sc.name, s.precision(), s.recall(), f[0], f[1])
			}
		}
	}
}
