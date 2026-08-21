package core

import "errors"

// ErrPartialRevenue marks a revenue read that succeeded at the org-list level but had
// one or more per-org failures — the fleet total is real but PARTIAL. SrcOf reports it
// as a not-ok source so the console shows a degraded state rather than presenting an
// under-count as authoritative.
var ErrPartialRevenue = errors.New("partial: one or more org revenue reads failed")

// SourceStatus is the freshness of one upstream the aggregator pulls from
// (overview.sources[] / revenue.sources[] / finance.sources[] / analytics.sources[]).
type SourceStatus struct {
	// Name identifies the upstream read, dotted by system: "do.volumes", "do.balance",
	// "k8s.hanzo-k8s", "iam.orgs", "billing.subscriptions". It is stable, so a console
	// can keep per-source state across reads.
	Name string `json:"name"`
	// OK is whether that read succeeded. False is the whole point of this row: an
	// aggregator answers with what it got rather than failing, so the only way a reader
	// can tell a real zero from a missing source is here.
	OK bool `json:"ok"`
	// Rows is how many records came back. Zero with ok=true is a genuine empty result;
	// zero with ok=false means nothing was read at all.
	Rows int `json:"rows"`
	// Error is why the read failed, and is empty exactly when OK is true.
	Error string `json:"error"`
	// At is when the aggregator ran, RFC3339. It is the READ's timestamp, shared by every
	// row in the list — not a per-source last-success time, so it never implies a stale
	// source is fresh.
	At string `json:"at"`
}

// SrcOf builds a SourceStatus freshness row for an aggregator.
func SrcOf(name string, err error, rows int, at string) SourceStatus {
	s := SourceStatus{Name: name, OK: err == nil, Rows: rows, At: at}
	if err != nil {
		s.Error = err.Error()
	}
	return s
}
