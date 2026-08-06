package git

// figures_rpc.go — what the org keeps in git, as headline numbers.
//
// It answers the questions a founder asks in words rather than in a repo name:
// how much code do we have, how much of it moved lately, what was touched last.
// Every existing plane read here needs the repo NAME up front (GitFiles, GitRev,
// GitStatus all take one), which makes them useless to a caller whose whole
// question is "what have we got" — so this op takes nothing and reports the
// rollup.
//
// It reads the repo METADATA rows, never the object store. Counting commits
// across every repo would mean a walk per repo per question, and the honest
// summary is already in the rows the push path maintains: how many repos, how
// big, and when each last moved. A number that costs a monorepo walk is a
// number an advisor will stop asking for.

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// activeWindow is what "recently" means for a repo. Thirty days is the window a
// monthly business question is asked in ("what did we ship this month"), and it
// is stated once here rather than passed in, because a caller that could choose
// it would be the only reason this op needed an argument at all.
const activeWindow = 30 * 24 * time.Hour

// exposeFigures publishes the org's git rollup on the internal plane. Called
// from Mount.
func exposeFigures() {
	zip.Post[plane.FiguresIn, plane.FiguresOut](cloud.Plane(), "/git/figures", planeFigures,
		zip.WithOperationID(plane.GitFigures),
		zip.WithSummary("The caller's headline git figures"))
}

// planeFigures answers the caller's own org-wide git rollup: how many
// repositories, their total on-disk footprint, how many moved inside
// [activeWindow], and which one moved last.
//
// The org is the CALLER's plane identity — the same rule every op in this app
// follows, and here it is structural rather than checked: [plane.FiguresIn]
// carries no field at all, so there is nothing to validate and nothing an
// argument could widen. Anonymous is refused, never defaulted.
//
// An org with no repositories answers a figure of zero, not an error. "You have
// no repositories" is a true and useful answer; only a failure to find out is an
// error.
func planeFigures(ctx context.Context, _ *plane.FiguresIn) (*plane.FiguresOut, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("git figures: org required")
	}
	s := mounted.Load()
	if s == nil {
		return nil, zip.Errorf(503, "git not mounted")
	}
	store, err := storeFor(s, who.Org)
	if err != nil {
		return nil, zip.ErrInternal("git figures: open failed")
	}
	rows, err := store.ListOrg(ctx, who.Org)
	if err != nil {
		return nil, zip.ErrInternal("git figures: list failed")
	}

	var bytes, newest int64
	var latest string
	active := 0
	since := time.Now().Add(-activeWindow).Unix()
	for _, r := range rows {
		bytes += r.SizeBytes
		if r.UpdatedAt >= since {
			active++
		}
		if r.UpdatedAt > newest {
			newest, latest = r.UpdatedAt, r.Name
		}
	}

	figs := []plane.Figure{
		{Label: "Repositories", Value: fmt.Sprint(len(rows))},
		{Label: "Code stored", Value: humanBytes(bytes)},
		{Label: "Repositories updated", Value: fmt.Sprint(active), Period: "last 30 days"},
	}
	// Only when there IS one. An empty org would otherwise be handed a figure
	// labelled "Last updated" with nothing after it, and the advisor above states
	// figures verbatim — it would narrate the blank.
	if latest != "" {
		figs = append(figs, plane.Figure{
			Label:  "Most recently updated",
			Value:  latest,
			Period: time.Unix(newest, 0).UTC().Format("2006-01-02"),
		})
	}
	return &plane.FiguresOut{Figures: figs}, nil
}
