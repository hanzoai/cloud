package ask

// git.go — what the org keeps in git, as headline numbers.
//
// It answers the questions a founder asks in words rather than in a repo name:
// how much code do we have, how much of it moved lately, what was touched last.
// Every other read of a repository needs the repo NAME up front, which makes it
// useless to a caller whose whole question is "what have we got" — so this reads
// the INVENTORY and reports the rollup.
//
// The inventory is the forge's (git.hanzo.ai), which is where the repositories
// are. It costs one list call, already cached per (actor, org) by the forge
// client, and it needs no walk of any repository: the forge already keeps how
// many repositories there are, how big each is, and when each last moved, which
// is the whole of what an advisor states. A number that costs a monorepo walk is
// a number an advisor will stop asking for.
//
// It reads AS THE CALLER. The figures are about the caller's own org, and a
// repository the caller cannot see is not part of what they have — a machine
// read would answer a member of the org with a count of private repositories
// they may not open.

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/plane"
)

// activeWindow is what "recently" means for a repository. Thirty days is the
// window a monthly business question is asked in ("what did we ship this
// month"), and it is stated once here rather than passed in, because a caller
// that could choose it would be the only reason this read needed an argument.
const activeWindow = 30 * 24 * time.Hour

// inventory is the one call this domain makes, held in a variable so a test can
// drive the rollup without a KMS and a forge behind it. Never reassigned in
// production.
var inventory = forgeRepos

// forgeRepos lists the repositories of the caller's org that the CALLER can see.
//
// Tenancy is two independent controls, neither backing up the other: the org is
// the caller's own plane identity and reaches a forge namespace only through
// forge.Owner's CLOSED table, and the read is sudoed to the caller's own forge
// login so the forge's ACL decides what comes back.
func forgeRepos(ctx context.Context) ([]forge.Repo, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, fmt.Errorf("git figures: org required")
	}
	// The namespace is resolved BEFORE the credential: a tenant with no namespace
	// on the forge is refused without spending a KMS read on it.
	owner, err := forge.Owner(org)
	if err != nil {
		return nil, err
	}
	c, err := forge.Dial(ctx, cloud.KMSPeer{})
	if err != nil {
		return nil, err
	}
	login, err := c.Caller(ctx)
	if err != nil {
		return nil, err
	}
	return c.As(login).Repos(ctx, owner)
}

// gitFigures is the org's git rollup: how many repositories, their total
// footprint, how many moved inside [activeWindow], and which one moved last.
//
// It carries the domain seam's signature ([plane.FiguresIn] is empty and stays
// empty) so the registry stays a value: there is nothing to ask for because
// there is nothing a caller may choose, and the one field this struct might
// plausibly grow is exactly the field that would let one tenant read another's.
//
// An org with no repositories answers figures of zero, not an error. "You have
// no repositories" is a true and useful answer; only a failure to find out is an
// error, and the advisor above tells the two apart.
func gitFigures(ctx context.Context, _ *plane.FiguresIn) (*plane.FiguresOut, error) {
	repos, err := inventory(ctx)
	if err != nil {
		return nil, err
	}

	var kib int64
	var newest time.Time
	var latest string
	active := 0
	since := time.Now().Add(-activeWindow)
	for _, r := range repos {
		kib += r.Size
		if r.UpdatedAt.After(since) {
			active++
		}
		if r.UpdatedAt.After(newest) {
			newest, latest = r.UpdatedAt, r.Name
		}
	}

	figs := []plane.Figure{
		{Label: "Repositories", Value: fmt.Sprint(len(repos))},
		{Label: "Code stored", Value: bytes(kib * 1024)},
		{Label: "Repositories updated", Value: fmt.Sprint(active), Period: "last 30 days"},
	}
	// Only when there IS one. An empty org would otherwise be handed a figure
	// labelled "Most recently updated" with nothing after it, and the advisor
	// above states figures verbatim — it would narrate the blank.
	if latest != "" {
		figs = append(figs, plane.Figure{
			Label:  "Most recently updated",
			Value:  latest,
			Period: newest.UTC().Format("2006-01-02"),
		})
	}
	return &plane.FiguresOut{Figures: figs}, nil
}

// bytes renders a byte count the way a person reads one. The domain that owns a
// number owns what it looks like — see [plane.Figure] — so the formatting is
// here and never on the wire.
func bytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for x := n / u; x >= u; x /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
