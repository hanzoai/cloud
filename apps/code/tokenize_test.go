package code

import (
	"strings"
	"testing"
)

func has(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

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
			if !has(got, w) {
				t.Errorf("codeTokens(%q)=%v, missing %q", c.in, got, w)
			}
		}
	}
}

func TestCodeTokensKeepsOperators(t *testing.T) {
	got := codeTokens("a -> b == c && d")
	for _, op := range []string{"->", "==", "&&"} {
		if !has(got, op) {
			t.Errorf("codeTokens dropped operator %q: %v", op, got)
		}
	}
	// The longest operator must win: "==" is one token, not two "=".
	if has(got, "=") {
		t.Errorf("codeTokens split == into =: %v", got)
	}
}

func TestBuildFTSMatch(t *testing.T) {
	m, ok := buildFTSMatch("getUserName")
	if !ok {
		t.Fatal("buildFTSMatch(getUserName) not ok")
	}
	// Subwords ≥3 chars become OR-ed quoted terms; the original identifier too.
	for _, want := range []string{`"getusername"`, `"user"`, `"name"`} {
		if !strings.Contains(m, want) {
			t.Errorf("match %q missing %q", m, want)
		}
	}
	if !strings.Contains(m, " OR ") {
		t.Errorf("expected OR-joined terms, got %q", m)
	}
	// All-short queries yield no usable trigram match.
	if _, ok := buildFTSMatch("a b"); ok {
		t.Error("buildFTSMatch(a b) should be not-ok (tokens <3 chars)")
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
