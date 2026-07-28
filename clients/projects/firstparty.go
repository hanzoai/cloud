package projects

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// firstparty.go — the platform's OWN published example apps, declared by name.
//
// WHY THIS EXISTS. Project.Official is the badge that says "Hanzo published
// this", and createProject raises it only for a SuperAdmin (`body.Official &&
// c.IsAdmin()`). That gate is correct and stays: a tenant asking for
// official:true must always get false, or the badge means nothing.
//
// But the platform's own example catalogue was published by a SCRIPT holding an
// ordinary org-admin token, not a SuperAdmin one — so Official was never raised
// on a single one of them. The consequence was not cosmetic: the cross-org
// catalog partitions rows by authorship, so 74 apps Hanzo wrote and hosts were
// filed as somebody else's work, and the gallery rail labelled them
// "third-party". The directory was making a claim about authorship, and the
// claim was wrong in our own disfavour.
//
// The fix is NOT to weaken the gate — an easier gate would let any tenant do
// what the script did. It is to notice that "which apps are ours" is not a
// request parameter at all. It is a FACT the platform knows about itself, so it
// is declared here, in the platform's own source, and applied to the store as a
// projection of that declaration. A tenant cannot edit this file; a request
// cannot reach it; there is no principal that can make the claim by asking.
//
// ONE DECLARATION. firstparty.json is the only place the set is written down.
// The apps repo (hanzoai/examples, hanzo-app/products.json) keys its product
// identities — name, mark, byline, the named agent that built each one — off the
// SAME slugs, and its publish step asserts against the live catalog that each
// one came back official. So the two halves cannot drift silently: the content
// side fails loudly if this side has not been deployed.
//
// FORWARD-ONLY, RAISE-ONLY. backfillOfficial runs on every migrate, so the
// declaration is the source of truth and the column is derived from it — a
// restored backup or a fresh region converges without a manual step. It only
// ever RAISES: a project outside the manifest is left exactly as it is, so a
// badge a real SuperAdmin set by hand is never revoked by a deploy.

//go:embed firstparty.json
var firstPartyJSON []byte

// firstParty is the declaration in firstparty.json: the org the platform
// publishes its own examples from, and the slugs of those examples.
type firstParty struct {
	Org   string   `json:"org"`
	Slugs []string `json:"slugs"`
}

// loadFirstParty decodes and validates the embedded declaration. It is strict —
// a malformed or empty manifest is a build-time mistake, and failing the mount
// is better than silently badging nothing (or, worse, badging an empty org,
// which would match every row with an empty org key).
func loadFirstParty() (firstParty, error) {
	var f firstParty
	if err := json.Unmarshal(firstPartyJSON, &f); err != nil {
		return f, fmt.Errorf("firstparty: decode: %w", err)
	}
	if strings.TrimSpace(f.Org) == "" {
		return f, fmt.Errorf("firstparty: org is required")
	}
	if len(f.Slugs) == 0 {
		return f, fmt.Errorf("firstparty: no slugs declared")
	}
	for _, s := range f.Slugs {
		if !slugRE.MatchString(s) {
			return f, fmt.Errorf("firstparty: %q is not a valid project slug", s)
		}
	}
	return f, nil
}

// backfillOfficial projects the embedded declaration onto the store: every
// declared slug in the platform org is marked official. Returns the number of
// rows it actually changed (0 once converged, which is the steady state).
//
// The predicate carries `official=0` so a converged run is a no-op UPDATE rather
// than a rewrite of 74 rows on every boot, and so the count it reports is the
// number of badges genuinely raised.
func backfillOfficial(ctx context.Context, db *sql.DB) (int64, error) {
	f, err := loadFirstParty()
	if err != nil {
		return 0, err
	}
	args := make([]any, 0, len(f.Slugs)+1)
	args = append(args, f.Org)
	for _, s := range f.Slugs {
		args = append(args, s)
	}
	q := `UPDATE projects SET official=1 WHERE official=0 AND org=? AND slug IN (?` +
		strings.Repeat(",?", len(f.Slugs)-1) + `)`
	res, err := db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("firstparty: backfill: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // driver without RowsAffected: the UPDATE still applied
	}
	return n, nil
}
