package git

import "testing"

func TestDotRepositoryName(t *testing.T) {
	for _, name := range []string{".github", ".profile"} {
		if !nameRE.MatchString(normalizeName(name + ".git")) {
			t.Fatalf("valid dot repository rejected: %s", name)
		}
	}
	for _, name := range []string{".", "..", "../repo", "a/b", "_repo"} {
		if nameRE.MatchString(name) {
			t.Fatalf("unsafe or ambiguous name accepted: %s", name)
		}
	}
}
