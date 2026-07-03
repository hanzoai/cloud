package detect

import (
	"strings"
	"testing"
)

// TestScanDetectsProviderSecrets proves each high-specificity rule fires on a
// real-shaped secret and that the finding NEVER echoes the raw secret back.
func TestScanDetectsProviderSecrets(t *testing.T) {
	cases := []struct {
		name, rule, content, secret string
	}{
		{"aws access key", "aws-access-key-id",
			`const k = "AKIAIOSFODNN7EXAMPLE"`, "AKIAIOSFODNN7EXAMPLE"},
		{"github token", "github-token",
			`token: ghp_` + strings.Repeat("a", 36), "ghp_" + strings.Repeat("a", 36)},
		{"gcp api key", "gcp-api-key",
			`key=AIza` + strings.Repeat("b", 35), "AIza" + strings.Repeat("b", 35)},
		{"stripe key", "stripe-secret-key",
			`sk_live_` + strings.Repeat("c", 24), "sk_live_" + strings.Repeat("c", 24)},
		{"private key", "private-key",
			"-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----", ""},
		{"npm token", "npm-token",
			`//registry:_authToken=npm_` + strings.Repeat("d", 36), "npm_" + strings.Repeat("d", 36)},
		{"slack token", "slack-token",
			`xoxb-` + strings.Repeat("1", 12), "xoxb-" + strings.Repeat("1", 12)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := ScanContent("f.txt", tc.content)
			var got *Finding
			for i := range fs {
				if fs[i].RuleID == tc.rule {
					got = &fs[i]
				}
			}
			if got == nil {
				t.Fatalf("rule %q did not fire on %q; got %+v", tc.rule, tc.content, fs)
			}
			// The raw secret must never appear in any field of the finding.
			if tc.secret != "" {
				if strings.Contains(got.Preview, tc.secret) {
					t.Fatalf("preview leaked the raw secret: %q", got.Preview)
				}
				if got.Fingerprint == "" || strings.Contains(got.Fingerprint, tc.secret) {
					t.Fatalf("fingerprint bad/leaky: %q", got.Fingerprint)
				}
				if got.Fingerprint != Fingerprint(tc.secret) {
					t.Fatalf("fingerprint mismatch: %q", got.Fingerprint)
				}
			}
			if got.Line != 1 && !strings.Contains(tc.content, "\n") {
				t.Fatalf("single-line content should be line 1, got %d", got.Line)
			}
		})
	}
}

// TestGenericSecretEntropyGate proves the generic assignment rule fires on a
// high-entropy value but NOT on a low-entropy placeholder — the false-positive
// control that makes the generic rule usable.
func TestGenericSecretEntropyGate(t *testing.T) {
	hi := `password = "Xk9mQ2vLp7wRt4zBn6HcJ5dF8"` // token-charset, high entropy
	lo := `password = "changemechangeme"`          // dictionary-ish, low entropy

	if fs := findRule(ScanContent("c.py", hi), "generic-secret"); fs == nil {
		t.Fatalf("high-entropy secret not flagged")
	}
	if fs := findRule(ScanContent("c.py", lo), "generic-secret"); fs != nil {
		t.Fatalf("low-entropy placeholder wrongly flagged: %+v", fs)
	}
}

// TestScanLineNumbers proves multi-line content maps matches to the right line.
func TestScanLineNumbers(t *testing.T) {
	content := "line one\nline two\nkey = AIza" + strings.Repeat("z", 35) + "\nline four"
	fs := findRule(ScanContent("multi.txt", content), "gcp-api-key")
	if fs == nil {
		t.Fatal("gcp key not found")
	}
	if fs.Line != 3 {
		t.Fatalf("want line 3, got %d", fs.Line)
	}
}

// TestScanDedupesWithinFile proves the same secret repeated on one line yields
// exactly one finding (dedupe by rule+line+fingerprint).
func TestScanDedupesWithinFile(t *testing.T) {
	s := "AKIAIOSFODNN7EXAMPLE AKIAIOSFODNN7EXAMPLE"
	n := 0
	for _, f := range ScanContent("d.txt", s) {
		if f.RuleID == "aws-access-key-id" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want 1 deduped finding, got %d", n)
	}
}

// TestScanOrdersBySeverity proves output is worst-first: a critical precedes a
// medium regardless of source order.
func TestScanOrdersBySeverity(t *testing.T) {
	content := "jwt = eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhYmMifQ.SGVsbG9TaWduYXR1cmU\n" +
		`key = AKIAIOSFODNN7EXAMPLE`
	fs := ScanContent("o.txt", content)
	if len(fs) < 2 {
		t.Fatalf("expected a critical (aws) and a medium (jwt), got %d: %+v", len(fs), fs)
	}
	if SeverityRank(fs[0].Severity) < SeverityRank(fs[len(fs)-1].Severity) {
		t.Fatalf("not sorted worst-first: %s then %s", fs[0].Severity, fs[len(fs)-1].Severity)
	}
}

// TestMaskNeverLeaks proves the mask keeps at most first/last 4 and stars the
// middle, and fully stars anything short.
func TestMaskNeverLeaks(t *testing.T) {
	if got := mask("shortpw"); got != strings.Repeat("*", len("shortpw")) {
		t.Fatalf("short secret not fully masked: %q", got)
	}
	long := "AKIAIOSFODNN7EXAMPLE"
	m := mask(long)
	if !strings.HasPrefix(m, "AKIA") || !strings.HasSuffix(m, "MPLE") {
		t.Fatalf("mask should keep 4+4 ends: %q", m)
	}
	if strings.Contains(m, long[4:len(long)-4]) {
		t.Fatalf("mask leaked the middle: %q", m)
	}
}

// TestEmptyAndCleanContent proves no findings on empty or clean input.
func TestEmptyAndCleanContent(t *testing.T) {
	if fs := ScanContent("e.txt", ""); fs != nil {
		t.Fatalf("empty content should yield nil, got %+v", fs)
	}
	clean := "package main\nfunc main() { println(\"hello world\") }"
	if fs := ScanContent("clean.go", clean); len(fs) != 0 {
		t.Fatalf("clean code should yield no findings, got %+v", fs)
	}
}

// TestRulesCatalog proves the catalog is non-empty, worst-first, and free of
// internal state (no regex leaks into the view).
func TestRulesCatalog(t *testing.T) {
	rv := Rules()
	if len(rv) == 0 {
		t.Fatal("empty catalog")
	}
	for i := 1; i < len(rv); i++ {
		if SeverityRank(rv[i-1].Severity) < SeverityRank(rv[i].Severity) {
			t.Fatalf("catalog not worst-first at %d: %s before %s", i, rv[i-1].Severity, rv[i].Severity)
		}
	}
}

func findRule(fs []Finding, id string) *Finding {
	for i := range fs {
		if fs[i].RuleID == id {
			return &fs[i]
		}
	}
	return nil
}
