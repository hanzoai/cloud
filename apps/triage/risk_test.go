package triage

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// THE PATHS A PERSON MUST SIGN OFF ARE THE REVIEW GATE'S, NOT A COPY THAT DRIFTS.
// highRisk is read against the words and prefixes review.sh holds; a word added to
// one and not the other fails here.
func TestRiskIsTheReviewGates(t *testing.T) {
	raw, err := os.ReadFile("../../.hanzo/review/review.sh")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	set := regexp.MustCompile(`high = \{([^}]*)\}`).FindStringSubmatch(src)
	if set == nil {
		t.Fatal("review.sh no longer names its high-risk words as `high = {...}`")
	}
	var words []string
	for _, w := range regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(set[1], -1) {
		words = append(words, w[1])
	}
	var mine []string
	for w := range riskWords {
		mine = append(mine, w)
	}
	slices.Sort(words)
	slices.Sort(mine)
	if !slices.Equal(words, mine) {
		t.Fatalf("review.sh's high-risk words %v, triage's %v", words, mine)
	}
	prefixes := regexp.MustCompile(`path\.startswith\(\(([^)]*)\)\)`).FindStringSubmatch(src)
	if prefixes == nil {
		t.Fatal("review.sh no longer names its workflow prefixes")
	}
	for _, p := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(prefixes[1], -1) {
		if !highRisk(p[1] + "x.yml") {
			t.Errorf("review.sh treats %s as high risk and triage does not", p[1])
		}
	}
}

func TestHighRisk(t *testing.T) {
	for file, want := range map[string]bool{
		"apps/iam/token.go": true, "sdk/auth_identity.go": false, "apps/billing/x.go": true, "cmd/release.go": true,
		"docs/pay.md": true, "apps/agents/x.go": false, "apps/payments/x.go": false, ".hanzo/workflows/cicd.yml": true,
		".github/actions/x/action.yml": true, "apps/kms/store.go": true, "README.md": false,
	} {
		if got := highRisk(file); got != want {
			t.Errorf("highRisk(%q) = %v, want %v", file, got, want)
		}
	}
}
