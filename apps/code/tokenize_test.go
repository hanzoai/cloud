package code

import (
	"slices"
	"strings"
	"testing"
)

func TestCodeTokensCamelAndSnake(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"getUserName", []string{"getusername", "get", "user", "name"}},
		{"MAX_RETRIES", []string{"max_retries", "max", "retries"}},
		{"parseURLPath", []string{"parseurlpath", "parse", "url", "path"}},
		{"HTTPServer", []string{"httpserver", "http", "server"}},
	}
	for _, c := range cases {
		got := codeTokens(c.in)
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("codeTokens(%q)=%v, missing %q", c.in, got, w)
			}
		}
	}
}

func TestCodeTokensKeepsOperators(t *testing.T) {
	got := codeTokens("a -> b == c && d")
	for _, op := range []string{"->", "==", "&&"} {
		if !slices.Contains(got, op) {
			t.Errorf("codeTokens dropped operator %q: %v", op, got)
		}
	}
	// The longest operator must win: "==" is one token, not two "=".
	if slices.Contains(got, "=") {
		t.Errorf("codeTokens split == into =: %v", got)
	}
}

func TestFTSTerms(t *testing.T) {
	terms := ftsTerms("getUserName")
	// Subwords ≥3 chars are terms, any of which may match; the original identifier too.
	for _, want := range []string{"getusername", "user", "name"} {
		if !slices.Contains(terms, want) {
			t.Errorf("terms %q missing %q", terms, want)
		}
	}
	// All-short queries yield no usable term.
	if got := ftsTerms("a b"); len(got) != 0 {
		t.Errorf("ftsTerms(a b) = %q, want none (tokens <3 chars)", got)
	}
}

func TestPrimaryTerms(t *testing.T) {
	// The unsplit identifiers only: no subwords, lowered, ≥3 chars, each once.
	got := primaryTerms("loadHistory LoadHistory ab ->")
	if !slices.Equal(got, []string{"loadhistory"}) {
		t.Errorf("primaryTerms = %q, want [loadhistory]", got)
	}
	if got := primaryTerms("a b"); len(got) != 0 {
		t.Errorf("primaryTerms(a b) = %q, want none", got)
	}
}

func TestEstimateTokensMonotone(t *testing.T) {
	short := estimateTokens("abcd")
	long := estimateTokens(strings.Repeat("abcd", 100))
	if long <= short {
		t.Errorf("estimateTokens not monotone: short=%d long=%d", short, long)
	}
	if estimateTokens("") != 1 {
		t.Errorf("estimateTokens(empty)=%d, want 1", estimateTokens(""))
	}
}
