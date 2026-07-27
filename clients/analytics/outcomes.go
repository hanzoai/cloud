package analytics

// outcomes.go — the MEASUREMENT seam the experiments primitive composes. An
// experiment's per-variant metric is read from the ONE analytics events plane
// (hanzo.events), never a second event store: outcomes are already captured by
// distinct_id (capture.go), so an experiment only needs to fold them per subject and
// join each subject to its flags variant. This is the scientific-growth loop's
// MEASUREMENT half — analytics measures, the experiment tests, flags decides.

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/datastore"
)

// SubjectOutcome is one subject's (distinct_id's) participation in an experiment
// window: whether it fired the Exposed (enrolled / saw the arm) event and whether it
// fired the Converted (metric) event. It is the per-subject grain the experiments
// primitive joins to a flags variant assignment to produce per-variant samples.
type SubjectOutcome struct {
	Subject   string
	Exposed   bool
	Converted bool
}

// Outcomes returns, for one org over [start,end), each subject's exposure +
// conversion for an experiment's two event names, read from hanzo.events. It is the
// measurement seam the experiments primitive composes: flags assignment joins to
// these outcomes by distinct_id.
//
// TENANT ISOLATION is the eventsWhere invariant — org is bound POSITIONALLY, never
// interpolated — and every event name is a BOUND parameter too, so neither a hostile
// org slug nor a hostile event name can escape into SQL. exposureEvent may be ""
// (then every returned subject is Exposed: the population is "appeared in-window");
// metricEvent is required. Fails closed with a 503 when the warehouse is absent.
func Outcomes(ctx context.Context, org, exposureEvent, metricEvent string, start, end time.Time) ([]SubjectOutcome, error) {
	if err := requireDatastore(); err != nil {
		return nil, err
	}
	if metricEvent == "" {
		return nil, fmt.Errorf("analytics: outcomes needs a metric event")
	}
	if err := EnsureEventsTable(ctx); err != nil {
		return nil, err
	}
	sql, args := outcomesSQL(org, exposureEvent, metricEvent, start, end)
	rows, err := datastore.Query(ctx, sql, args...)
	if err != nil {
		return nil, warehouseErr("outcomes", err)
	}
	out := make([]SubjectOutcome, 0, len(rows))
	for _, r := range rows {
		subject := aString(r["subject"])
		if subject == "" {
			continue
		}
		out = append(out, SubjectOutcome{
			Subject:   subject,
			Exposed:   aInt64(r["exposed"]) > 0,
			Converted: aInt64(r["converted"]) > 0,
		})
	}
	return out, nil
}

// outcomesSQL builds the per-subject exposure/conversion query over hanzo.events —
// the pure, I/O-free core so the isolation invariant is testable without a warehouse.
// TENANCY: org rides eventsWhere as a BOUND parameter (never interpolated) and every
// event name is BOUND too, so nothing user-derived escapes into SQL. The SELECT
// maxIf placeholders appear first in the string, then eventsWhere's [start,end,org],
// then the event-set IN — the args slice follows that exact positional order.
func outcomesSQL(org, exposureEvent, metricEvent string, start, end time.Time) (string, []any) {
	where, wargs := eventsWhere(org, start, end)
	if exposureEvent == "" {
		args := append([]any{metricEvent}, wargs...)
		return "SELECT distinct_id AS subject, 1 AS exposed, " +
			"maxIf(1, event = ?) AS converted FROM " + eventsTable +
			" WHERE " + where + " GROUP BY distinct_id", args
	}
	args := append([]any{exposureEvent, metricEvent}, wargs...)
	args = append(args, exposureEvent, metricEvent)
	return "SELECT distinct_id AS subject, " +
		"maxIf(1, event = ?) AS exposed, " +
		"maxIf(1, event = ?) AS converted FROM " + eventsTable +
		" WHERE " + where + " AND event IN (?, ?) GROUP BY distinct_id", args
}
