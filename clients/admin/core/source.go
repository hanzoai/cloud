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
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Rows  int    `json:"rows"`
	Error string `json:"error"`
	At    string `json:"at"`
}

// SrcOf builds a SourceStatus freshness row for an aggregator.
func SrcOf(name string, err error, rows int, at string) SourceStatus {
	s := SourceStatus{Name: name, OK: err == nil, Rows: rows, At: at}
	if err != nil {
		s.Error = err.Error()
	}
	return s
}
