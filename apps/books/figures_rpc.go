package books

// figures_rpc.go — the ledger's headline numbers, on the internal plane.
//
// It is the SAME snapshot GET /v1/books/metrics returns, answered over the
// socket instead of the edge, because the caller that wants it most is in
// another process. The unified advisor (/v1/ask) ships as its own plugin
// binary: it mounts /v1/ask and nothing else, so the in-process read it used to
// make for these figures could never reach this app and every money question
// fell through to "I can answer questions about your finances today" with no
// figures behind it. One op fixes that, and fixes it for every future peer at
// the same time.
//
// It recomputes nothing. computeMetrics is the one aggregation and
// metricsFigures is the one formatter, both shared verbatim with the HTTP read,
// so a figure here is byte-identical to the same figure on the edge — books owns
// both the number and how it is spelled, in exactly one place.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// exposeFigures publishes the ledger's headline read on the internal plane.
// Called from Mount.
func exposeFigures() {
	zip.Post[plane.FiguresIn, plane.FiguresOut](cloud.Plane(), "/books/figures", planeFigures,
		zip.WithOperationID(plane.BooksFigures),
		zip.WithSummary("The caller's headline ledger figures"))
}

// planeFigures answers the caller's own all-time ledger snapshot as formatted
// figures.
//
// The org is the CALLER's plane identity and there is no argument that could
// name another: [plane.FiguresIn] is empty, and the identity zip forwards is the
// one the gateway minted. Anonymous is REFUSED rather than defaulted — a books
// read with no principal behind it is not a read of nobody's ledger, it is a
// read of the first org whose name a bug supplies.
//
// The LIVE ledger, never the sandbox. A sandbox figure narrated as an answer
// about the business would be a fabricated number wearing a real one's label,
// and the selector that would let a caller ask for it is the same argument this
// op refuses to grow.
func planeFigures(ctx context.Context, _ *plane.FiguresIn) (*plane.FiguresOut, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("books figures: org required")
	}
	if mounted == nil {
		return nil, zip.Errorf(503, "books not mounted")
	}
	st, err := mounted.storeFor(who.Org, false)
	if err != nil {
		return nil, zip.ErrInternal("books figures: open failed")
	}
	m, err := computeMetrics(ctx, st, "", "")
	if err != nil {
		return nil, zip.ErrInternal("books figures: metrics failed")
	}
	return &plane.FiguresOut{Figures: planeFiguresOf(metricsFigures(m))}, nil
}

// planeFiguresOf carries books' own figures onto the plane shape unchanged. It is
// a projection and never a computation: same labels, same already-formatted
// values, same period. The two types are separate because the plane package
// cannot import an app, not because the figures differ.
func planeFiguresOf(in []Figure) []plane.Figure {
	out := make([]plane.Figure, 0, len(in))
	for _, f := range in {
		out = append(out, plane.Figure{Label: f.Label, Value: f.Value, Period: f.Period})
	}
	return out
}
