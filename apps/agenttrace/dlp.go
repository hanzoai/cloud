// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

// ContentCaptureMode governs how sensitive GenAI payloads (gen_ai.prompt and
// gen_ai.completion) are handled before span emission.
//
// Default is CaptureRedact so prompts, completions, PII, and API keys never
// leak into OTLP spans unless an operator explicitly opts in.
type ContentCaptureMode string

const (
	// CaptureRedact replaces prompt/completion payloads with "[REDACTED]" and
	// scrubs secret patterns from all string attributes (default, safest).
	CaptureRedact ContentCaptureMode = "redact"

	// CaptureHash replaces prompt/completion payloads with a deterministic
	// "sha256:<hex>;bytes=<n>" digest for deduplication/correlation without
	// exposing plaintext, while still scrubbing secrets from other attributes.
	CaptureHash ContentCaptureMode = "hash"

	// CaptureRaw retains raw prompt/completion text verbatim. Explicit opt-in
	// ONLY (e.g., debugging or negative-regime overhead benchmarking). Note:
	// explicit secret header keys (authorization, api_key, password, etc.) are
	// still scrubbed to prevent credential leakage.
	CaptureRaw ContentCaptureMode = "raw"
)

// Valid reports whether m is a recognized ContentCaptureMode.
func (m ContentCaptureMode) Valid() bool {
	switch m {
	case CaptureRedact, CaptureHash, CaptureRaw:
		return true
	default:
		return false
	}
}

const redactedPlaceholder = "[REDACTED]"

var (
	reAPIKey = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|hz_(?:live|test|sk)_[A-Za-z0-9_-]{12,}|ghp_[A-Za-z0-9]{20,}|gho_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{20,}|xox[baprs]-[0-9A-Za-z-]{10,})\b`)
	reBearer = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{12,}`)
	reJWT    = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)
	rePEM    = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	reSSN    = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	reEmail  = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
)

func mayHaveAPIKey(s string) bool {
	if len(s) < 15 {
		return false
	}
	return strings.Contains(s, "sk-") ||
		strings.Contains(s, "hz_") ||
		strings.Contains(s, "ghp_") ||
		strings.Contains(s, "gho_") ||
		strings.Contains(s, "AKIA") ||
		strings.Contains(s, "AIza") ||
		strings.Contains(s, "xox")
}

func mayHaveBearer(s string) bool {
	if len(s) < 19 {
		return false
	}
	return strings.Contains(s, "earer ") || strings.Contains(s, "EARER ") || strings.Contains(s, "earer\t")
}

func mayHaveSSN(s string) bool {
	if len(s) < 11 {
		return false
	}
	for i := 3; i <= len(s)-8; i++ {
		if s[i] == '-' && s[i+3] == '-' &&
			s[i-1] >= '0' && s[i-1] <= '9' &&
			s[i+1] >= '0' && s[i+1] <= '9' {
			return true
		}
	}
	return false
}

// isSensitiveKey reports whether an attribute key always carries credentials or
// secrets and must be redacted unconditionally.
func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	switch {
	case strings.Contains(k, "authorization"),
		strings.Contains(k, "api_key"),
		strings.Contains(k, "apikey"),
		strings.Contains(k, "password"),
		strings.Contains(k, "passwd"),
		strings.Contains(k, "secret"),
		strings.Contains(k, "private_key"),
		strings.Contains(k, "access_token"),
		strings.Contains(k, "refresh_token"),
		strings.Contains(k, "cookie"):
		return true
	default:
		return false
	}
}

// isContentKey reports whether an attribute key holds a GenAI prompt or completion payload.
func isContentKey(key string) bool {
	return key == AttrGenAIPrompt || key == AttrGenAICompletion
}

// DigestString returns a canonical "sha256:<hex>" digest for query or content correlation.
func DigestString(s string) string {
	sum := sha256.Sum256([]byte(s))
	var out [7 + 64]byte
	copy(out[0:7], "sha256:")
	hex.Encode(out[7:], sum[:])
	return string(out[:])
}

// FormatContentHash returns "sha256:<hex>;bytes=<len>" for CaptureHash mode.
func FormatContentHash(s string) string {
	return DigestString(s) + ";bytes=" + strconv.Itoa(len(s))
}

// ContainsSecret reports whether text contains any known credential, token, or
// PII pattern detected by the DLP gate.
func ContainsSecret(text string) bool {
	if len(text) < 6 || text == redactedPlaceholder {
		return false
	}
	if mayHaveAPIKey(text) && reAPIKey.MatchString(text) {
		return true
	}
	if mayHaveBearer(text) && reBearer.MatchString(text) {
		return true
	}
	if strings.Contains(text, "eyJ") && reJWT.MatchString(text) {
		return true
	}
	if strings.Contains(text, "PRIVATE KEY") && rePEM.MatchString(text) {
		return true
	}
	if mayHaveSSN(text) && reSSN.MatchString(text) {
		return true
	}
	if strings.IndexByte(text, '@') >= 0 && reEmail.MatchString(text) {
		return true
	}
	return false
}

// ScrubSecrets replaces all matching secret/PII substrings in text with
// "[REDACTED]" and returns the scrubbed string and whether any redaction occurred.
func ScrubSecrets(text string) (string, bool) {
	if len(text) < 6 || text == redactedPlaceholder {
		return text, false
	}
	changed := false
	out := text
	if mayHaveAPIKey(out) && reAPIKey.MatchString(out) {
		out = reAPIKey.ReplaceAllString(out, redactedPlaceholder)
		changed = true
	}
	if mayHaveBearer(out) && reBearer.MatchString(out) {
		out = reBearer.ReplaceAllString(out, redactedPlaceholder)
		changed = true
	}
	if strings.Contains(out, "eyJ") && reJWT.MatchString(out) {
		out = reJWT.ReplaceAllString(out, redactedPlaceholder)
		changed = true
	}
	if strings.Contains(out, "PRIVATE KEY") && rePEM.MatchString(out) {
		out = rePEM.ReplaceAllString(out, redactedPlaceholder)
		changed = true
	}
	if mayHaveSSN(out) && reSSN.MatchString(out) {
		out = reSSN.ReplaceAllString(out, redactedPlaceholder)
		changed = true
	}
	if strings.IndexByte(out, '@') >= 0 && reEmail.MatchString(out) {
		out = reEmail.ReplaceAllString(out, redactedPlaceholder)
		changed = true
	}
	return out, changed
}

// SanitizeContent applies mode to a prompt/completion payload.
func SanitizeContent(raw string, mode ContentCaptureMode) (val string, redacted bool) {
	if raw == "" {
		return "", false
	}
	switch mode {
	case CaptureRaw:
		return raw, false
	case CaptureHash:
		return FormatContentHash(raw), true
	default: // CaptureRedact
		return redactedPlaceholder, true
	}
}

// SanitizeAttr applies the DLP policy to an arbitrary span attribute.
func SanitizeAttr(a Attr, mode ContentCaptureMode) (Attr, bool) {
	if isSensitiveKey(a.Key) {
		return StringAttr(a.Key, redactedPlaceholder), true
	}
	if a.Kind != AttrString {
		return a, false
	}
	if isContentKey(a.Key) {
		v, red := SanitizeContent(a.Str, mode)
		return StringAttr(a.Key, v), red
	}
	if mode == CaptureRaw {
		return a, false
	}
	cleaned, red := ScrubSecrets(a.Str)
	if !red {
		return a, false
	}
	return StringAttr(a.Key, cleaned), true
}
