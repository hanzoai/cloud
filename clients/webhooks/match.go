package webhooks

// match.go — the pure NATS subject matcher. An org subscribes to subject PATTERNS
// (e.g. "commerce.order.*", "commerce.>"); the dispatcher matches a live event's
// subject against them with NATS token-wildcard semantics. No I/O, no state — a
// value function, table-tested.

import "strings"

// subjectMatches reports whether subject matches pattern under NATS wildcard rules:
//
//   - subjects and patterns are '.'-separated tokens;
//   - a literal token matches that exact token;
//   - "*" matches EXACTLY one token;
//   - ">" matches one OR MORE trailing tokens and is only meaningful as the final
//     pattern token (anything after it is ignored).
//
// So "commerce.order.*" matches "commerce.order.created" but not
// "commerce.order.line.created"; "commerce.>" matches "commerce.order" and
// "commerce.order.created" but NOT the bare "commerce". An empty pattern or subject
// never matches.
func subjectMatches(pattern, subject string) bool {
	if pattern == "" || subject == "" {
		return false
	}
	pt := strings.Split(pattern, ".")
	st := strings.Split(subject, ".")
	for i, tok := range pt {
		if tok == ">" {
			// ">" consumes one or more remaining subject tokens: there must be at
			// least one token left at this position.
			return i < len(st)
		}
		if i >= len(st) {
			return false // pattern is longer than the subject
		}
		if tok == "*" {
			continue // matches this one token, whatever it is
		}
		if tok != st[i] {
			return false
		}
	}
	// Every pattern token matched a subject token and neither had a ">": the match is
	// exact only when the token counts are equal.
	return len(pt) == len(st)
}

// matches reports whether this endpoint wants an event on subject. An endpoint with
// an EMPTY events list subscribes to EVERYTHING (the same "empty == all" convention
// commerce's WebhookEndpoint uses); otherwise it matches if ANY of its patterns does.
func (e Endpoint) matches(subject string) bool {
	if len(e.Events) == 0 {
		return true
	}
	for _, p := range e.Events {
		if subjectMatches(p, subject) {
			return true
		}
	}
	return false
}
