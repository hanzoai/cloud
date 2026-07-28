// Package fqdn is the ONE place a public hostname is normalized, validated, and
// proven to be under a caller's control.
//
// Two subsystems bind customer hostnames — /v1/platform apps (clients/platform)
// and PaaS sites (clients/projects) — and both must answer the same three
// questions: what is the canonical form of this name, is it syntactically a
// hostname at all, and does the caller actually control it. Each carried its own
// copy of the answer, and the copies had already drifted: both compiled the same
// hostname regexp independently, and only the apps path stripped a trailing root
// dot before matching, so `example.com.` normalized on one endpoint and was
// rejected as malformed on the other. One concept, one implementation, here.
//
// Ownership proof is the DNS-01 model: the claimant publishes a per-claim token
// as a TXT record at `_hanzo-challenge.<host>`. Only someone who controls that
// zone's DNS can, so a matching token proves control. This is the boundary that
// stops a tenant claiming a host it does not own, so the check is deliberately
// small enough to read in one sitting.
//
// Everything here is pure except Verify (one DNS read) and Token (one rng read):
// no store, no HTTP, no service state. That is what lets both callers share it.
package fqdn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
)

// ChallengePrefix is the label under which the TXT ownership token is published.
// A leading underscore keeps it out of the space of real service hostnames, per
// the RFC 8552 convention for attribute leaves.
const ChallengePrefix = "_hanzo-challenge."

// Challenge is the record name a claimant publishes the token at.
func Challenge(h string) string { return ChallengePrefix + h }

// nameRE is a syntactic public FQDN: one or more DNS labels then an alphabetic
// TLD, lowercase, no scheme/port/path. It is the boundary guard — a value that
// could never be a real domain is refused before any store or DNS work happens.
// Callers pass Clean's output; the pattern itself assumes normalized input.
var nameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// maxLen is the maximum length of a DNS name in presentation form.
const maxLen = 253

// Clean canonicalizes a hostname as typed by a human: surrounding space removed,
// lowercased, and the optional trailing root dot stripped (`example.com.` and
// `example.com` name the same host, and only one of them can be a map key).
func Clean(raw string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
}

// Valid reports whether h is a syntactically well-formed public hostname. It
// expects Clean's output — Valid does not normalize, so that a caller cannot
// accidentally store one form and match another.
func Valid(h string) bool { return len(h) <= maxLen && nameRE.MatchString(h) }

// Record is one DNS record a claimant must publish, as rendered to the customer.
type Record struct {
	Type  string `json:"type"`  // TXT | CNAME
	Name  string `json:"name"`  // the record name the customer creates
	Value string `json:"value"` // the record value
}

// Records are the exact records a customer publishes for a custom host: the TXT
// ownership token, plus the CNAME that routes traffic at their Hanzo host once
// the claim verifies.
func Records(h, token, target string) []Record {
	return []Record{
		{Type: "TXT", Name: Challenge(h), Value: token},
		{Type: "CNAME", Name: h, Value: target},
	}
}

// Token mints a fresh per-claim ownership token. 128 bits of rand: a claimant
// must publish the exact value, so it only has to be unguessable, not long.
func Token() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Proven is the ownership rule, and nothing else: does token appear among the TXT
// records published at the challenge name?
//
// It is a pure predicate over the DNS answer — no context, no resolver, no clock.
// That is deliberate. This one expression is the boundary that stops a tenant
// claiming a host it does not control, so it is worth being able to read, review,
// and exhaust by table test without standing up a fake resolver.
//
// It fails CLOSED: an empty answer, or records none of which match, are "not
// proven". An empty token is never proven — otherwise a zone publishing an empty
// TXT record would hand ownership to a caller whose token was never minted.
//
// A zone legitimately carries many TXT records at one name, so every answer is
// checked rather than just the first, and each is trimmed because resolvers and
// zone files differ on surrounding whitespace.
func Proven(txts []string, token string) bool {
	if token == "" {
		return false
	}
	for _, t := range txts {
		if strings.TrimSpace(t) == token {
			return true
		}
	}
	return false
}

// Resolver is the minimal DNS surface ownership proof needs. *net.Resolver
// satisfies it, so production needs no adapter; tests inject a fake when they
// want to exercise the lookup, though the rule itself (Proven) needs none.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// ErrUnproven is returned by Verify when ownership is not (yet) demonstrated. It
// is an expected outcome, not a fault: a customer who has just claimed a host has
// not published the record yet, and the caller renders this as "pending" rather
// than as an error. Callers test with errors.Is; its message is customer-facing
// and names exactly what to publish.
type ErrUnproven struct {
	Name  string // the record name to publish at
	Token string // the value it must carry
}

func (e *ErrUnproven) Error() string {
	return fmt.Sprintf(
		"TXT record %s = %q not found yet — publish it and retry (DNS can take a few minutes to propagate)",
		e.Name, e.Token)
}

// Verify reads the challenge records for h and applies Proven to them. It returns
// nil exactly when ownership is proven, and *ErrUnproven otherwise.
//
// A lookup error is not surfaced as a distinct outcome: a SERVFAIL and an absent
// record are the same fact to a claimant — the proof is not visible — and
// collapsing them keeps "not proven" a single path that cannot be mistaken for
// "proven" by a caller that forgets to check one of two error returns.
func Verify(ctx context.Context, r Resolver, h, token string) error {
	txts, err := r.LookupTXT(ctx, Challenge(h))
	if err == nil && Proven(txts, token) {
		return nil
	}
	return &ErrUnproven{Name: Challenge(h), Token: token}
}
