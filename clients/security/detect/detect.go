// Package detect is the pure, dependency-free secret-detection engine behind
// Hanzo's native code-security surface. It scans source for hardcoded secrets
// with zero external tooling (no semgrep/gitleaks binary, no network) — the one
// Semgrep-class capability that ships complete today. The logic ports the
// concept behind hanzoai/guard (the LLM-boundary redactor) to code at rest, and
// is the substrate a native AST/SAST engine (on hanzoai/ast) grows onto per the
// plan of record in hanzoai/security POSTURE.md.
//
// It is deliberately a LEAF: only the standard library is imported, and the API
// is pure functions over (path, content) → findings — no I/O, no store, no
// HTTP, no cloud deps. That decomplection is the point: the HTTP subsystem
// (clients/security) AND the `hanzo security scan` CLI both consume THIS engine,
// so the detection logic exists once and neither surface drags the other in.
//
// THE ONE INVARIANT: a Finding NEVER carries the raw secret. It carries a masked
// preview (first/last few chars, middle starred) plus a SHA-256 fingerprint of
// the secret — enough to locate it, dedupe identical occurrences, and confirm a
// rotation happened, and nothing more. Persisting the plaintext would make the
// findings DB a secret store, exactly the thing we scan to prevent (global rule:
// never store secrets in the clear).
package detect

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"strings"
)

// Severity ranks a finding. Ordered so higher is worse; used for sorting and
// for the /v1/security/findings?minSeverity filter.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
)

// severityRank maps a severity to its sort weight (higher = worse). Unknown
// severities rank 0 so a typo can never outrank a real critical.
var severityRank = map[string]int{
	SeverityCritical: 4,
	SeverityHigh:     3,
	SeverityMedium:   2,
	SeverityLow:      1,
}

// SeverityRank exposes the ordering for callers that filter/sort findings.
func SeverityRank(sev string) int { return severityRank[sev] }

// SeveritiesAtOrAbove returns the severity names ranked >= min (unordered), so a
// store can build an `IN (...)` filter without reaching into the rank map. An
// unknown min yields every severity (rank 0 floor), which is the safe "no
// filter" behavior.
func SeveritiesAtOrAbove(min string) []string {
	floor := severityRank[min]
	out := make([]string, 0, len(severityRank))
	for sev, rank := range severityRank {
		if rank >= floor {
			out = append(out, sev)
		}
	}
	return out
}

// RuleCount is the number of detection rules in the catalog (for health/log
// lines that report engine size without materializing the catalog).
func RuleCount() int { return len(rules) }

// Rule is one secret-detection pattern. A rule is EITHER a direct regex whose
// whole match is the secret (Pattern, with an optional Group capturing the
// secret sub-match), OR — when MinEntropy > 0 — an assignment rule that only
// fires when the captured value's Shannon entropy clears the threshold, which
// is how generic `secret = "..."` lines avoid flagging every lowercase word.
type Rule struct {
	ID          string
	Name        string
	Severity    string
	Description string
	re          *regexp.Regexp
	group       int     // capture group holding the secret (0 = whole match)
	minEntropy  float64 // >0: only fire when the captured value's entropy >= this
}

// Finding is one detected secret. It is the redacted, storable record — it
// pins WHERE (path, line) and WHAT rule fired, and carries a masked Preview
// plus the SHA-256 Fingerprint of the raw secret, never the secret itself.
type Finding struct {
	RuleID      string
	RuleName    string
	Severity    string
	Path        string
	Line        int
	Preview     string // masked: first/last chars kept, middle starred
	Fingerprint string // hex SHA-256 of the raw secret — dedupe/rotation key
}

// rules is the built-in catalog. Order is irrelevant (Scan sorts output); each
// entry is independent and complete. High-specificity provider patterns come
// first only for readability. The generic entropy rule is last — it is the
// catch-all and the one most tuned to avoid false positives.
var rules = buildRules()

func buildRules() []Rule {
	def := []struct {
		id, name, sev, desc, pat string
		group                    int
		entropy                  float64
	}{
		{"private-key", "Private key block", SeverityCritical,
			"PEM private key material (RSA/EC/OPENSSH/DSA/PGP).",
			`-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----`, 0, 0},
		{"aws-access-key-id", "AWS access key ID", SeverityCritical,
			"An AWS access key ID (AKIA/ASIA...).",
			`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`, 0, 0},
		{"aws-secret-access-key", "AWS secret access key", SeverityCritical,
			"A value assigned to an aws secret access key field.",
			`(?i)aws_?secret_?access_?key["' ]*[:=]["' ]*([A-Za-z0-9/+=]{40})`, 1, 0},
		{"gcp-api-key", "Google API key", SeverityHigh,
			"A Google/GCP API key (AIza...).",
			`\bAIza[0-9A-Za-z_\-]{35}\b`, 0, 0},
		{"github-token", "GitHub token", SeverityHigh,
			"A GitHub personal-access / OAuth / app token (ghp_/gho_/ghu_/ghs_/ghr_).",
			`\bgh[pousr]_[A-Za-z0-9]{36,}\b`, 0, 0},
		{"github-pat-fine", "GitHub fine-grained PAT", SeverityHigh,
			"A GitHub fine-grained personal access token.",
			`\bgithub_pat_[A-Za-z0-9_]{82}\b`, 0, 0},
		{"slack-token", "Slack token", SeverityHigh,
			"A Slack API token (xoxb/xoxa/xoxp/xoxr/xoxs).",
			`\bxox[baprs]-[0-9A-Za-z\-]{10,}\b`, 0, 0},
		{"stripe-secret-key", "Stripe secret key", SeverityCritical,
			"A live Stripe secret/restricted key.",
			`\b(?:sk|rk)_live_[0-9A-Za-z]{24,}\b`, 0, 0},
		{"npm-token", "npm access token", SeverityHigh,
			"An npm access token (npm_...).",
			`\bnpm_[A-Za-z0-9]{36}\b`, 0, 0},
		{"slack-webhook", "Slack webhook URL", SeverityMedium,
			"A Slack incoming-webhook URL (carries a send capability).",
			`https://hooks\.slack\.com/services/[A-Za-z0-9_/]+`, 0, 0},
		{"jwt", "JSON Web Token", SeverityMedium,
			"A JWT (header.payload.signature) — often a bearer credential.",
			`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\b`, 0, 0},
		{"generic-secret", "Generic assigned secret", SeverityMedium,
			"A high-entropy value assigned to a secret/password/token/apikey field.",
			`(?i)\b(?:password|passwd|secret|api_?key|access_?token|auth_?token|client_?secret)\b["' ]*[:=]["' ]*["']?([A-Za-z0-9/+=_\-.]{16,})["']?`, 1, 3.5},
	}
	out := make([]Rule, 0, len(def))
	for _, d := range def {
		out = append(out, Rule{
			ID: d.id, Name: d.name, Severity: d.sev, Description: d.desc,
			re: regexp.MustCompile(d.pat), group: d.group, minEntropy: d.entropy,
		})
	}
	return out
}

// ScanContent runs every rule over one file's content and returns the findings,
// most severe first (then by line). It is pure and allocation-light: no I/O,
// safe to call concurrently. Path is echoed into each finding for locating; it
// is not read from disk. Findings are de-duplicated within the file by
// (rule, line, fingerprint) so a rule matching the same secret twice on one
// line yields one finding. (Named ScanContent, not Scan, so the engine entry
// point never collides with the store's Scan record type.)
func ScanContent(path, content string) []Finding {
	if content == "" {
		return nil
	}
	lineStarts := indexLineStarts(content)
	seen := make(map[string]struct{})
	var out []Finding
	for i := range rules {
		r := &rules[i]
		for _, m := range r.re.FindAllStringSubmatchIndex(content, -1) {
			// Resolve the secret span (whole match, or the capture group).
			ss, se := m[0], m[1]
			if r.group > 0 && len(m) > 2*r.group+1 && m[2*r.group] >= 0 {
				ss, se = m[2*r.group], m[2*r.group+1]
			}
			secret := content[ss:se]
			if r.minEntropy > 0 && shannonEntropy(secret) < r.minEntropy {
				continue // low-entropy assignment (e.g. secret = "changeme") — skip
			}
			fp := Fingerprint(secret)
			line := lineOf(lineStarts, ss)
			key := r.ID + "\x00" + itoa(line) + "\x00" + fp
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, Finding{
				RuleID: r.ID, RuleName: r.Name, Severity: r.Severity,
				Path: path, Line: line, Preview: mask(secret), Fingerprint: fp,
			})
		}
	}
	sortFindings(out)
	return out
}

// RuleView is the catalog entry exposed at /v1/security/rules — the rule
// identity without its internal regex.
type RuleView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
}

// Rules returns the detection catalog (what the engine can find), most severe
// first, for /v1/security/rules.
func Rules() []RuleView {
	out := make([]RuleView, 0, len(rules))
	for i := range rules {
		out = append(out, RuleView{
			ID: rules[i].ID, Name: rules[i].Name,
			Severity: rules[i].Severity, Description: rules[i].Description,
		})
	}
	// stable severity-desc, then id, so the catalog reads worst-first.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && lessRuleView(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func lessRuleView(a, b RuleView) bool {
	if ra, rb := severityRank[a.Severity], severityRank[b.Severity]; ra != rb {
		return ra > rb
	}
	return a.ID < b.ID
}

// ---- helpers (pure) ----

// Fingerprint is the hex SHA-256 of a raw secret. Identical secrets across
// files/scans share a fingerprint (dedupe + rotation tracking); the original is
// not recoverable from it.
func Fingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// mask keeps the first 4 and last 4 characters and stars the middle, so a
// finding is human-recognizable ("AKIA...7XQ") without disclosing the secret.
// Short secrets (<= 8) are fully starred — keeping any of a short secret would
// disclose too much of it.
func mask(secret string) string {
	n := len(secret)
	if n <= 8 {
		return strings.Repeat("*", n)
	}
	return secret[:4] + strings.Repeat("*", n-8) + secret[n-4:]
}

// shannonEntropy returns the per-character Shannon entropy (bits) of s. Used to
// separate a real high-entropy secret from a low-entropy placeholder in the
// generic assignment rule.
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]float64
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	var h float64
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

// indexLineStarts returns the byte offset at which each line begins, so a match
// offset maps to a 1-based line in O(log lines) via lineOf.
func indexLineStarts(s string) []int {
	starts := []int{0}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineOf maps a byte offset to its 1-based line via binary search over starts.
func lineOf(starts []int, off int) int {
	lo, hi := 0, len(starts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if starts[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}

// sortFindings orders findings most-severe first, then by line, then rule — a
// stable, deterministic order so identical input yields identical output (the
// scan summary and any diff over it are reproducible).
func sortFindings(fs []Finding) {
	for i := 1; i < len(fs); i++ {
		for j := i; j > 0 && lessFinding(fs[j], fs[j-1]); j-- {
			fs[j], fs[j-1] = fs[j-1], fs[j]
		}
	}
}

func lessFinding(a, b Finding) bool {
	if ra, rb := severityRank[a.Severity], severityRank[b.Severity]; ra != rb {
		return ra > rb
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.RuleID < b.RuleID
}

// itoa is a tiny int→string for building dedupe keys without fmt.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
