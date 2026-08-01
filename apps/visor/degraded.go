package visor

// A partial answer must be able to say it is partial.
//
// Several surfaces here fold two independent sources — Visor (managed clusters,
// DOKS worker nodes) and the org's own BYO registry — and each one already
// decided, correctly, that a Visor outage must not take the whole page with it:
// a page that 502s on an optional provider is worse than one that shows what it
// can. What none of them did was SAY so. The Visor error was written to a log
// and the response went out as a 200 with the remaining half, so "the provider
// is down" and "you own nothing" were the same three bytes on the wire: [].
//
// That is not a hypothetical failure mode, it is the one we shipped. Visor in
// production sat four tags behind the commit that introduced /v1/k8s/clusters,
// so every call 404'd, and GET /v1/k8s/clusters answered {"clusters":[]} to an
// operator running eight of them. The only trace was a Warn nobody reads during
// a fleet check — which is precisely when you are least able to tell the
// difference and most harmed by getting it wrong.
//
// So the fold stays and the silence goes. `degraded` is additive and omitted
// when everything answered, so a healthy response is byte-identical to what it
// was and no consumer has to change to keep working; a consumer that wants to
// distinguish an outage from an empty estate now can.

import (
	"strings"
)

// sourceFailure names one data source that did not answer, and why. It rides
// beside the data rather than replacing it: the result is a PARTIAL SUCCESS, not
// an error, because the other source's rows are real and withholding them would
// reintroduce the outage-takes-the-page behaviour these handlers deliberately
// avoid.
type sourceFailure struct {
	// Source is the dependency that failed, named as an operator names it.
	Source string `json:"source"`
	// Reason is a terse, log-safe summary — never the upstream's response body.
	Reason string `json:"reason"`
}

// visorDown records a Visor failure for the `degraded` list. It is the ONE place
// a Visor error becomes a user-facing reason, so every surface reports an outage
// with the same words.
func visorDown(err error) []sourceFailure {
	return []sourceFailure{{Source: "visor", Reason: terse(err)}}
}

// terse reduces an upstream error to a single short line fit for a JSON field.
//
// It exists because the raw error is not fit for one. The Visor client formats a
// non-2xx as "visor: upstream %d: %s" with a snippet of the response BODY, and
// Visor answers an unknown path with an HTML error page — so the unabridged
// string is a DOCTYPE and a stylesheet, embedded in an API response and, worse,
// in every log line that carries it. First line only, HTML dropped, hard-capped.
func terse(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	// Drop a markup tail: the message we want ends where the body snippet begins.
	if i := strings.Index(s, "<"); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), ":"))
	const cap = 160
	if len(s) > cap {
		s = strings.TrimSpace(s[:cap]) + "…"
	}
	return s
}
