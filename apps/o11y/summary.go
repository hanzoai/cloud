package o11y

// summary.go — GET /v1/o11y/summary, the PUBLIC platform status document.
//
// WHY IT LIVES HERE. o11y already owns the ONE availability signal this platform
// has: mountProbes (probes.go) knocks on every fleetTarget every 30s and records
// hanzo_service_up{service=…} — "availability is the one signal that cannot be
// pushed". A public status page is nothing more than the outward projection of
// that gauge, so it belongs to the app that produces it. Anywhere else and the
// projection either re-probes the fleet (a second copy of the target table, and a
// public endpoint that fans out 21 in-cluster requests per hit) or takes a
// cross-process hop for data this app already holds. o11y is also Eager in the
// manifest, which matters more than it looks: a status endpoint is read exactly
// when the platform is on fire, and a lazily-spawned plugin would pay a cold
// start at that moment. It answers UNDER /v1/o11y, not beside it: it was a
// top-level /v1/summary, and a capability's every address carries the
// capability's name (HIP-0139 §3). Unauthenticated is not the same as
// un-prefixed — the two facts are independent, and the next paragraph is the
// one that matters for a reader during an outage.
//
// WHY IT IS UNAUTHENTICATED. A status endpoint that requires a login is useless
// during an outage: IAM is one of the things that can be down, and the reader is
// often a logged-out customer asking "is it you or me?". It carries no tenant
// data — every field is a fact about OUR OWN services, identical for every
// caller — so there is nothing to scope. The mechanism is the one this binary
// already uses for /v1/health (serve.go), /v1/platform/health ("liveness must be
// probe-able without a JWT") and o11y.Anonymous's exemption in gate(): the identity
// middleware never rejects, it only strips and re-mints, and a route is public by
// simply not calling a principal gate. No bypass is invented here and none is
// needed — this handler just does not ask who is calling. Reads are never
// Billable (spend.go admits GET unconditionally), so no gate stands in front of
// it either.
//
// WHY THIS SHAPE. The panel that consumes it (insights
// sidePanelStatusIncidentIoLogic) was written against incident.io's status-page
// summary schema, and those field names are plain descriptive nouns — page_title,
// ongoing_incidents, affected_components — not a vendor's branding. Keeping the
// shape means the client parses what we send; inventing a synonym for
// "ongoing_incidents" would buy nothing and break it.
//
// WHAT IT WILL NOT DO. It never answers "operational" from an absence of data.
// If the availability source cannot be read, we do not KNOW that the platform is
// healthy, and a green status page is the one output worse than no status page —
// so that case is a 503 that says so, never an empty incident list. Incidents are
// derived only from measurements: a service is listed because its own health
// probe failed, and the impact wording follows from counting, not from an
// invented table of which services are allowed to matter.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// summaryTTL bounds how stale the served snapshot may be. The prober writes
// hanzo_service_up every 30s, so re-reading the store faster than this buys no
// freshness — it only multiplies load on the telemetry store at exactly the
// moment (an outage) when everyone reloads the status page at once. It is also
// what makes an unauthenticated endpoint safe to expose: a flood costs one store
// read per TTL, not one per request.
const summaryTTL = 15 * time.Second

// upMetric is the gauge the fleet prober records (probes.go → prober.Prober):
// 1 = the target answered its own health URL, 0 = it did not. Every target emits
// on every cycle — "an unreachable target records 0, not nothing" — so a missing
// series means the prober is not reporting, which is a different fact from down.
const upMetric = "hanzo_service_up"

// deploymentBrand is this deployment's own brand id, pinned at mount. The status
// document is white-labelled per request from the Host (a lux caller must never
// be shown Hanzo's status page), and this is the fallback for a Host that matches
// no brand domain — an in-cluster caller, a probe, a raw IP.
var deploymentBrand string

// StatusComponent is one piece of the platform an incident affects. Name is the
// service the fleet prober knows it by.
type StatusComponent struct {
	// ID is the component's stable handle, which on this platform IS the service
	// name — there is no separate component registry to allocate ids from.
	ID string `json:"id"`
	// Name is the service as the fleet prober knows it (the `service` label on
	// hanzo_service_up), so a reader can match a component to what is being probed.
	Name string `json:"name"`
	// CurrentStatus is this component's own condition: "full_outage" for a
	// service that did not answer its health probe at all.
	CurrentStatus string `json:"current_status"`
}

// StatusIncident is one ongoing incident. Every field is measured: an incident
// exists because a probe failed, and LastUpdateAt is when that measurement was
// taken.
type StatusIncident struct {
	// ID is derived from the service, so the same outage keeps one id across reads
	// rather than being reported as a new incident every 15 seconds.
	ID string `json:"id"`
	// Name is the one-line headline, built from the service that stopped answering.
	Name string `json:"name"`
	// Status is always "investigating" — the member of the client's closed set that
	// means detected, cause not yet established, which is exactly what an automated
	// prober knows. Nothing here ever claims "identified": that would assert a
	// diagnosis no measurement made.
	Status string `json:"status"`
	// URL points at the HUMAN status page, not back at this JSON. Every link in this
	// document goes to the same place.
	URL string `json:"url"`
	// LastUpdateAt is when the failing measurement this incident reports was
	// read, RFC3339 UTC.
	LastUpdateAt string `json:"last_update_at"`
	// LastUpdateMessage says what was observed, not what is being done about it —
	// there is no operator writing updates here, only the probe that failed.
	LastUpdateMessage string `json:"last_update_message"`
	// CurrentWorstImpact is the incident's impact on the PLATFORM, which is not
	// the same question as the component's own condition above.
	CurrentWorstImpact string `json:"current_worst_impact"`
	// AffectedComponents is what this incident covers. It is COUNTED rather than
	// classified: some services down is a partial outage and every probed service
	// down is a full one, because deciding that one service is critical and another
	// is not would need a judgement nobody has measured.
	AffectedComponents []StatusComponent `json:"affected_components"`
}

// StatusMaintenance is one planned maintenance window. Hanzo has no maintenance
// scheduling plane, so both maintenance lists below are always empty — which is a
// true statement ("nothing is scheduled"), not a placeholder. The type is part of
// the published contract because a client reading the document has to know those
// fields are arrays of objects.
type StatusMaintenance struct {
	// ID is the window's handle.
	ID string `json:"id"`
	// Name is its one-line headline.
	Name string `json:"name"`
	// Status is where the window is in its life, in the client's own vocabulary.
	Status string `json:"status"`
	// URL points at the human status page, as every link in this document does.
	URL string `json:"url"`
	// LastUpdateAt is when the window was last revised, RFC3339 UTC.
	LastUpdateAt string `json:"last_update_at"`
	// LastUpdateMessage is the text of that revision.
	LastUpdateMessage string `json:"last_update_message"`
	// AffectedComponents is what the window touches.
	AffectedComponents []StatusComponent `json:"affected_components"`
	// StartsAt is when work begins, RFC3339 UTC.
	StartsAt string `json:"starts_at,omitempty"`
	// EndsAt is when it is expected to finish, RFC3339 UTC.
	EndsAt string `json:"ends_at,omitempty"`
}

// StatusSummary is the public platform status document.
type StatusSummary struct {
	// PageTitle is the brand's own status-page title, resolved per request from the
	// Host — a lux caller must never be shown Hanzo's.
	PageTitle string `json:"page_title"`
	// PageURL is the HUMAN status page — an HTML page for people, distinct from
	// this JSON endpoint. Every link in this document points there.
	PageURL string `json:"page_url"`
	// OngoingIncidents is one entry per service that failed its health probe, sorted
	// by name. Empty means every probed service answered — which is a measurement,
	// not an absence of reports.
	OngoingIncidents []StatusIncident `json:"ongoing_incidents"`
	// InProgressMaintenances is always empty: this platform has no maintenance
	// scheduling plane, so "nothing is running" is a true statement rather than a
	// placeholder.
	InProgressMaintenances []StatusMaintenance `json:"in_progress_maintenances"`
	// ScheduledMaintenances is always empty, for the same reason.
	ScheduledMaintenances []StatusMaintenance `json:"scheduled_maintenances"`
	// CheckedAt is when the underlying availability read was taken, RFC3339 UTC.
	// Not part of the status-page schema the panel parses (which ignores unknown
	// fields); it is here because a status document with no timestamp cannot be
	// told apart from a stale one.
	CheckedAt string `json:"checked_at"`
}

// noArgs is the empty input of a read that takes nothing.
type noArgs struct{}

// mountSummary registers the public status face. It is a TYPED op so the one
// registry every projection reads (OpenAPI, MCP, the CLI) carries it — this is a
// published contract other people's clients call, and a raw route would be
// invisible to all three. The handler reaches for the request — to brand per Host
// and to set Cache-Control — which cloud.Bridge parks on the context, but this
// registers no Bridge of its own: the composer installs one at the root ahead of
// every route, so a second copy on a /v1/o11y/summary node would gate a node that holds
// no routes (the leaf below is registered through `a`) and zip refuses to compose
// middleware that could never run. Without a request the handler still answers,
// branded for the deployment rather than for the caller's Host — see handleSummary.
func mountSummary(a *zip.App, deps cloud.Deps) {
	deploymentBrand = deps.Brand
	zip.Get(a, "/v1/o11y/summary", handleSummary)
}

// GetSummary reports whether the platform is up. It returns the public status
// document: the incidents currently open against Hanzo's own services, derived
// from the fleet health probes, plus the address of the human status page. No
// authentication is required and no tenant data is involved — the answer is the
// same for every caller.
//
// A service that fails its health probe becomes one incident naming that service.
// When the availability source itself cannot be read the endpoint answers 503
// rather than an empty incident list, because "we cannot tell" and "everything is
// fine" are different answers and only one of them is true.
//
// Example: {}
func handleSummary(ctx context.Context, _ *noArgs) (*StatusSummary, error) {
	brandID := deploymentBrand
	c, hasReq := cloud.Request(ctx)
	if hasReq {
		if b, ok := cloud.BrandForHostOK(c.Host()); ok {
			brandID = b
		}
	}

	up, checkedAt, err := fleetAvailability(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"platform status unavailable: the fleet availability signal could not be read (%v)", err)
	}

	if hasReq {
		// The document is identical for every caller and is recomputed at most
		// once per TTL, so let an intermediary hold it exactly that long — never
		// longer than the server would.
		c.SetHeader("Cache-Control", "public, max-age="+strconv.Itoa(int(summaryTTL.Seconds())))
	}
	return buildSummary(brandID, up, checkedAt), nil
}

// serviceUp is one probed service and whether it answered. Shared with the
// availability read (availability.go), which publishes it directly — it is the
// same value, so it is the same type, and the tags are what let the one type
// reach the wire without a parallel copy of itself.
type serviceUp struct {
	// Name is the service as the fleet prober knows it (probes.go's target name,
	// which is the `service` label on hanzo_service_up).
	Name string `json:"name"`
	// Up is true when the service answered its own health URL on the last cycle.
	Up bool `json:"up"`
}

// buildSummary projects the availability read into the status document. Pure —
// same inputs, same document — so the incident vocabulary is testable without a
// metrics store.
func buildSummary(brandID string, up []serviceUp, checkedAt time.Time) *StatusSummary {
	info := cloud.BrandFor(brandID)
	page := "https://status." + info.Domain
	stamp := checkedAt.UTC().Format(time.RFC3339)

	down := make([]serviceUp, 0, len(up))
	for _, s := range up {
		if !s.Up {
			down = append(down, s)
		}
	}
	sort.Slice(down, func(i, j int) bool { return down[i].Name < down[j].Name })

	// Impact is COUNTED, not classified. Some services down is a partial outage
	// of the platform; every probed service down is a full one. Deciding that
	// service X is "critical" and Y is not would need a table nobody has measured,
	// and inventing one is how a status page starts lying in both directions.
	impact := "partial_outage"
	if len(down) == len(up) {
		impact = "full_outage"
	}

	incidents := make([]StatusIncident, 0, len(down))
	for _, s := range down {
		incidents = append(incidents, StatusIncident{
			ID:   "service-" + s.Name,
			Name: s.Name + " is not responding",
			// The client's status vocabulary is a closed set of three, and this is
			// the member that means "detected, cause not yet established" — which
			// is exactly what an automated prober knows. Claiming "identified"
			// would assert a diagnosis nothing here has made.
			Status:             "investigating",
			URL:                page,
			LastUpdateAt:       stamp,
			LastUpdateMessage:  s.Name + " did not answer its health check.",
			CurrentWorstImpact: impact,
			AffectedComponents: []StatusComponent{{
				ID: s.Name, Name: s.Name, CurrentStatus: "full_outage",
			}},
		})
	}

	return &StatusSummary{
		PageTitle:              cloud.BrandDisplay(info.ID) + " status",
		PageURL:                page,
		OngoingIncidents:       incidents,
		InProgressMaintenances: []StatusMaintenance{},
		ScheduledMaintenances:  []StatusMaintenance{},
		CheckedAt:              stamp,
	}
}

// availabilitySnapshot is the cached fleet read plus when it was taken.
type availabilitySnapshot struct {
	mu       sync.Mutex
	services []serviceUp
	at       time.Time
}

var availability availabilitySnapshot

// fleetAvailability returns the current up/down state of every probed service,
// re-reading the telemetry store at most once per summaryTTL.
//
// An error means we do not know — the metrics store is unset, unreachable, or
// reports no hanzo_service_up at all (the prober is not running, or has never
// completed a cycle). The caller must NOT read that as healthy. A stale snapshot
// is preferred to an error while one exists, because the last real measurement is
// still a measurement; only when there has never been one does the read fail.
func fleetAvailability(ctx context.Context) ([]serviceUp, time.Time, error) {
	availability.mu.Lock()
	defer availability.mu.Unlock()

	now := time.Now()
	if len(availability.services) > 0 && now.Sub(availability.at) < summaryTTL {
		return availability.services, availability.at, nil
	}

	series, err := latestGauge(ctx, upMetric)
	if err != nil {
		if len(availability.services) > 0 {
			return availability.services, availability.at, nil
		}
		return nil, time.Time{}, err
	}
	if len(series) == 0 {
		if len(availability.services) > 0 {
			return availability.services, availability.at, nil
		}
		return nil, time.Time{}, errNoProbeData
	}

	// One series per probed service. A service label is the only identity the
	// gauge carries; a series without one cannot be attributed and is dropped
	// rather than reported under an empty name.
	out := make([]serviceUp, 0, len(series))
	for _, s := range series {
		name := strings.TrimSpace(s.Labels["service"])
		if name == "" {
			continue
		}
		out = append(out, serviceUp{Name: name, Up: s.Value == 1})
	}
	if len(out) == 0 {
		if len(availability.services) > 0 {
			return availability.services, availability.at, nil
		}
		return nil, time.Time{}, errNoProbeData
	}

	availability.services, availability.at = out, now
	return out, now, nil
}

// errNoProbeData is the "we cannot tell" case: the metrics store answered, but
// carries no fleet availability to report.
var errNoProbeData = errors.New("no " + upMetric + " series reported")
