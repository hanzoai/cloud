package projects

import (
	"github.com/hanzoai/cloud"
	"context"
	"net/http"

	"github.com/hanzoai/cloud/apps/sites"
)

// Why this exists, stated plainly, because the defect it answers cost real hours.
//
// A published site is stale until the edge is told otherwise, and telling it is
// the one step in a deploy that is allowed to fail quietly: a purge that cannot
// run degrades to stale-until-TTL rather than failing the release, which is the
// right call and is exactly why nobody notices. Every other signal reads healthy
// while it happens — the deploy succeeds, the Sites plane records the release,
// the ORIGIN serves the new bytes — and only a reader on the public hostname
// sees anything wrong, hours later, with no error anywhere to point at.
//
// The only trace was one Warn line per call in a log. So the way that
// misconfiguration was actually found was by fetching a file from the CDN,
// fetching it again from the origin, and noticing the two lengths differed.
// A subsystem that can only be diagnosed by measuring its own output from the
// outside is one that does not report its state, so now it reports its state.
//
// Modelled on /v1/domain/health, deliberately: that endpoint already answers
// "what can this deployment actually do right now" for the registrar, and a
// second shape for the same question would be a second thing to learn.
//
// NOT A READINESS PROBE. It answers 503 when publishes cannot reach readers
// promptly, which is a true statement about the product and a false one about
// the process — this app deploys fine with no edge at all. Wiring it to a probe
// would take a working deployment out of rotation for a cache credential.
type edgeState struct {
	// Provider is the CDN behind this edge, or "none". It is the first thing an
	// operator wants and the only vendor name this API returns.
	Provider string `json:"provider"`
	// Status is "ok" when a publish reaches readers immediately, else "degraded".
	Status string `json:"status"`
	// Configured is whether the edge holds credentials to act at all. False means
	// every purge is a no-op.
	Configured bool `json:"configured"`
	// Freshness says, in one phrase, how long after a publish a reader sees it.
	// It is the sentence an operator actually wants; the booleans above are how
	// a machine reads the same fact.
	Freshness string `json:"freshness"`
	// Reach is the apexes a publish is invalidated on. A site is served on more
	// than one — the site plane's own and the first-party apex — and a purge that
	// covers one of them looks identical from here to a purge that covers both.
	Reach []string `json:"reach"`
	// Policy is the Cache-Control this edge serves each class of object with. It
	// is DERIVED from the one canonical function, never a second copy: half the
	// confusion when a publish looks stale is not knowing what the TTLs are, and
	// reading them out of the source is not something an operator should have to
	// do to answer "how long until this is live".
	Policy map[string]string `json:"policy"`
	// Error is the blocker, so an operator reads it instead of guessing at it.
	Error string `json:"error,omitempty"`
}

// policy answers the TTL question for each class the edge distinguishes, by
// asking the same function that serves them. Representative keys, not a table:
// a table here would be the copy that drifts.
func policy() map[string]string {
	return map[string]string{
		"document":  sites.CacheControlFor("index.html", ""),
		"immutable": sites.CacheControlFor("app.4f3a9c21.js", ""),
		"asset":     sites.CacheControlFor("logo.svg", ""),
	}
}

// StatusCode is 200 when a publish is immediately live and 503 when it is not.
// The body is the answer either way — the reason IS the payload, so it rides the
// refusal rather than being replaced by an error envelope.
func (e *edgeState) StatusCode() int {
	if e.Status == "ok" {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}

// health reports whether a publish reaches readers, rather than whether it was
// accepted. Those are different questions and only the second one was ever
// visible.
//
// It asks the edge and nothing else. There is no live call to the provider here:
// Configured is a local fact, it is the fact that was missing, and a health check
// that spends a third-party API call is one an operator learns not to run.
func (o ops) edge(_ context.Context, _ *cloud.Unit) (*edgeState, error) {
	if o.s.State.edge.Configured() {
		return &edgeState{
			Provider:   o.s.State.edge.Name(),
			Reach:      o.s.State.edge.Reach(),
			Status:     "ok",
			Configured: true,
			Freshness:  "a publish is live at the edge immediately (purged by cache-tag)",
			Policy:     policy(),
		}, nil
	}
	return &edgeState{
		Provider:   o.s.State.edge.Name(),
		Reach:      o.s.State.edge.Reach(),
		Status:     "degraded",
		Configured: false,
		Freshness:  "a publish is live only after the edge TTL expires",
		Policy:     policy(),
		Error:      "the edge holds no credentials, so every purge is a no-op",
	}, nil
}
