package projects

// storage.go — what an org's sites hold in the object store, and what holding it
// costs.
//
// Publishing a static site is free (billing.go). What a site costs is the room its
// files take, billed as usage on the one cloud.Meter every paid surface records on,
// under the same exactly-once span rule (cloud.RuntimeCharge) the agent-hour and
// sandbox-disk meters use.
//
// WHAT IS COUNTED is everything the org keeps in the sites bucket, under "<org>/":
// each site's serving prefix, the releases retained for rollback, and any build
// output a release was promoted from. It is LISTED from the store, never summed from
// deployment rows: the sizes a CI run or a sandbox reports are the builder's word,
// and the listing is what the platform actually holds. Each key is listed once, so a
// republish — which purges the prefix (publishSite) or reconciles it against the
// keys it uploaded (land) — replaces bytes rather than adding a second copy.
//
// WHAT IS FREE is the first freeSiteBytes an org holds: the free tier the catalog
// publishes for object storage. Only the excess is priced, so an org whose sites fit
// in the tier is never debited and never gated, whatever its balance.
//
// HOW IT IS BILLED is one span at a time. Every hour the sweep lists each org,
// records the size, and charges the span since the org was last billed at that
// size. The watermark moves before the debit is emitted and the debit's ref is
// derived from the span, so a double tick or a retried debit bills a span once. A
// publish re-lists its org when it lands (refresh), which keeps the size the gate
// reads current between sweeps; it bills nothing itself.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
)

const (
	// gb is the unit the catalog prices storage in: a decimal gigabyte.
	gb int64 = 1_000_000_000

	// hoursPerMonth is a 30-day month, the month provisioning's GB-day charge
	// divides by, so a GB-month here and a GB-month there are the same span.
	hoursPerMonth int64 = 30 * 24

	// storageGBMonthMicros is the compiled floor: $0.02 per GB-month, in micro-USD.
	// It is the object-storage price @hanzo/plans publishes (services.json, the
	// "S3 / Storage" row, "$0.02/GB/mo"); TestTheStorageFloorIsTheCatalogs pins the
	// two together. An operator retunes it with a row in commerce's meter authority
	// (product hosting, meter storageMeter), and this is charged until they do.
	storageGBMonthMicros int64 = 20_000

	// freeSiteBytes is the storage an org's sites hold for nothing: 5 GB, the
	// "freeTier" of the same catalog row, pinned by the same test.
	freeSiteBytes int64 = 5 * gb

	// storageMeter addresses the price in the meter authority, under the hosting
	// product the debits carry.
	storageMeter = "storage-gb-month"

	// storageKind names the storage gate and the storage debit in the meter's logs.
	storageKind = "storage"

	// storageModel is the invoice line a storage debit reads as.
	storageModel = "sites/storage"

	// storageSweep is how often every org is re-listed and billed. The charge is
	// (now − watermark) at the size measured, so the cadence decides how often money
	// moves and how fresh the size is, never how much a span costs.
	storageSweep = time.Hour
)

// storageRate is the published price of one GB-month of site storage, in
// micro-USD, falling back to storageGBMonthMicros. A var so a test can publish a
// price without an authority beside it.
var storageRate = func(ctx context.Context) int64 {
	return cloud.RateMicros(ctx, hostingProvider, storageMeter, storageGBMonthMicros)
}

// billable is the part of held the org pays for: what lies past the free tier.
func billable(held int64) int64 { return max(held-freeSiteBytes, 0) }

// hourMicros prices holding `excess` bytes for one hour at perGBMonth micro-USD,
// rounded half up. Big integers, because bytes times a price in micro-USD passes
// int64 at a few hundred terabytes.
func hourMicros(excess, perGBMonth int64) int64 {
	if excess <= 0 || perGBMonth <= 0 {
		return 0
	}
	return divRound(excess, perGBMonth, gb*hoursPerMonth, false)
}

// monthCents prices holding `excess` bytes for one month, in whole cents rounded
// UP, so a gate that asks for it never admits a wallet a cent short of the month.
func monthCents(excess, perGBMonth int64) int64 {
	if excess <= 0 || perGBMonth <= 0 {
		return 0
	}
	return divRound(excess, perGBMonth, gb*10_000, true)
}

// divRound is a×b/den, rounded half up, or up when ceil is set.
func divRound(a, b, den int64, ceil bool) int64 {
	n := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	d := big.NewInt(den)
	if ceil {
		n.Add(n, new(big.Int).Sub(d, big.NewInt(1)))
	} else {
		n.Add(n, new(big.Int).Rsh(d, 1))
	}
	return n.Quo(n, d).Int64()
}

// ── the record ───────────────────────────────────────────────────────────────

// storageDDL is one row per org: the size last measured, the instant (in
// nanoseconds) the listing that measured it began, and the second through which
// that org's storage has been billed. The org is the key because the sites bucket
// keys every object by it.
const storageDDL = `
CREATE TABLE IF NOT EXISTS site_storage (
  org         TEXT PRIMARY KEY,
  bytes       BIGINT NOT NULL DEFAULT 0,
  measured_ns BIGINT NOT NULL DEFAULT 0,
  metered_at  BIGINT NOT NULL DEFAULT 0
);`

// holding is one org's row. The zero value is an org never measured.
type holding struct {
	Bytes      int64
	MeasuredNS int64
	MeteredAt  int64
}

// Holding reads org's row, the zero holding when it has none.
func (s *Store) Holding(ctx context.Context, org string) (holding, error) {
	var h holding
	err := s.db.QueryRowContext(ctx,
		`SELECT bytes, measured_ns, metered_at FROM site_storage WHERE org=$1`, org).
		Scan(&h.Bytes, &h.MeasuredNS, &h.MeteredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return holding{}, nil
	}
	if err != nil {
		return holding{}, fmt.Errorf("read storage: %w", err)
	}
	return h, nil
}

// Measured records that org held `bytes` by a listing that began at `began`
// (unix nanoseconds). A listing that began before the one on the row is dropped:
// the later a listing starts, the later the state it is certain to have seen, so a
// slow listing never overwrites a newer one.
func (s *Store) Measured(ctx context.Context, org string, bytes, began int64) error {
	if err := dialect.Text(org); err != nil {
		return fmt.Errorf("record storage: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO site_storage (org, bytes, measured_ns) VALUES ($1,$2,$3)
		 ON CONFLICT(org) DO UPDATE SET bytes=excluded.bytes, measured_ns=excluded.measured_ns
		 WHERE excluded.measured_ns > site_storage.measured_ns`,
		org, bytes, began)
	if err != nil {
		return fmt.Errorf("record storage: %w", err)
	}
	return nil
}

// AdvanceStorage moves org's billing watermark from was to now, and reports
// whether this call moved it. It is a compare-and-set, so of two callers billing
// the same span exactly one wins it.
func (s *Store) AdvanceStorage(ctx context.Context, org string, was, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE site_storage SET metered_at=$1 WHERE org=$2 AND metered_at=$3`, now, org, was)
	if err != nil {
		return false, fmt.Errorf("advance storage meter: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("advance storage meter: %w", err)
	}
	return n == 1, nil
}

// Holders is every org the sweep lists: each org with a project, and each org
// the meter holds a row for, which is how an org that deleted its last project is
// billed until its leftovers are gone and its row is forgotten (Forget).
func (s *Store) Holders(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org FROM projects UNION SELECT org FROM site_storage ORDER BY org`)
	if err != nil {
		return nil, fmt.Errorf("list holders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var org string
		if err := rows.Scan(&org); err != nil {
			return nil, fmt.Errorf("list holders: %w", err)
		}
		out = append(out, org)
	}
	return out, rows.Err()
}

// Holder is the org whose site has been kept under namespace ns longest, "" when
// no site is kept there. A namespace is that org's to keep adding to (namespace.go).
func (s *Store) Holder(ctx context.Context, ns string) (string, error) {
	var org string
	err := s.db.QueryRowContext(ctx,
		`SELECT org FROM projects WHERE namespace=$1 ORDER BY created_at, org LIMIT 1`, ns).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("holder: %w", err)
	}
	return org, nil
}

// Keeps reports whether org keeps anything here: a project, or a measured
// footprint. A namespace no site is kept under yet may not take such an org's
// name (namespace.go).
func (s *Store) Keeps(ctx context.Context, org string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM projects WHERE org=$1 UNION SELECT 1 FROM site_storage WHERE org=$2 LIMIT 1`, org, org).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("keeps: %w", err)
	}
	return true, nil
}

// Away lists org's projects kept under a namespace other than org's own.
func (s *Store) Away(ctx context.Context, org string) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+projectCols+` FROM projects WHERE org=$1 AND namespace<>$2 ORDER BY slug`, org, org)
	if err != nil {
		return nil, fmt.Errorf("away: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("away: scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Lodged names the slugs OTHER orgs keep under the namespace ns.
func (s *Store) Lodged(ctx context.Context, ns string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT slug FROM projects WHERE namespace=$1 AND org<>$2`, ns, ns)
	if err != nil {
		return nil, fmt.Errorf("lodged: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, fmt.Errorf("lodged: scan: %w", err)
		}
		out[slug] = true
	}
	return out, rows.Err()
}

// Forget drops org's row once it holds nothing and has no project, in one
// statement so a publish that lands between the two facts keeps it. An org that
// leaves is then off the sweep, and one that returns is a first sight: the time it
// was gone is never billed at whatever it holds on its return.
func (s *Store) Forget(ctx context.Context, org string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM site_storage WHERE org=$1 AND bytes=0
		   AND NOT EXISTS (SELECT 1 FROM projects WHERE org=$2)`, org, org)
	if err != nil {
		return fmt.Errorf("forget storage: %w", err)
	}
	return nil
}

// ── measuring ────────────────────────────────────────────────────────────────

// footprint is what org keeps in the sites bucket: the summed size of every
// object under "<org>/", and of every site it keeps under a GitHub org's
// namespace (namespace.go) — `<namespace>/<slug>/` and its releases. The trailing
// slash is the tenant boundary — "acme" never sums "acme-corp" — and an org that
// is not one safe path segment is refused rather than listed, since "" or ".."
// would widen the prefix to other tenants. A site ANOTHER org keeps under a
// namespace that shares this org's name is that org's, and is not summed here:
// a GitHub account and an IAM org can share a name without sharing an owner.
//
// A release an estate site was KEPT as (record.go) is not summed: it is the
// platform's copy for rolling the site back, which its org did not ask to store,
// and counting it would bill the org for it and could carry it past the free tier
// into the storage gate, refusing its next publish.
//
// A listing runs to its end however long that takes. The org decides how many
// objects it holds, so a deadline here is one an org can outgrow, and an org whose
// listing never finishes is never measured: never billed, and never gated. What
// bounds a listing is ctx — the process's life — and the client's per-page response
// timeout; a slow org only delays the orgs after it, whose spans are billed whole.
func footprint(ctx context.Context, store *Store, cli object.Store, bucket, org string) (int64, error) {
	if !orgPathSafe(org) {
		return 0, fmt.Errorf("storage: %q is not an org this bucket keys by", org)
	}
	lodged, err := store.Lodged(ctx, org)
	if err != nil {
		return 0, err
	}
	away, err := store.Away(ctx, org)
	if err != nil {
		return 0, err
	}
	kept, err := store.KeptPrefixes(ctx, org)
	if err != nil {
		return 0, err
	}
	total, err := sum(ctx, cli, bucket, org+"/", lodged, kept)
	if err != nil {
		return 0, err
	}
	for _, p := range away {
		for _, prefix := range []string{sitePrefix(p.Namespace, p.Slug) + "/", releaseSpace(p.Namespace) + "/" + p.Slug + "/"} {
			n, err := sum(ctx, cli, bucket, prefix, nil, kept)
			if err != nil {
				return 0, err
			}
			total += n
		}
	}
	return total, nil
}

// sum is the size of every object under prefix, less any site whose slug is in
// skip: `<prefix><slug>/…` and `<prefix>.releases/<slug>/…`, and less any kept
// release: an object under one of the release prefixes kept names.
func sum(ctx context.Context, cli object.Store, bucket, prefix string, skip, kept map[string]bool) (int64, error) {
	var total int64
	for obj, err := range cli.List(ctx, bucket, object.List{Prefix: prefix}) {
		if err != nil {
			return 0, fmt.Errorf("list %s: %w", prefix, err)
		}
		if len(kept) > 0 && kept[releaseOf(obj.Key)] {
			continue
		}
		if len(skip) > 0 {
			rel := strings.TrimPrefix(obj.Key, prefix)
			rel = strings.TrimPrefix(rel, ".releases/")
			if slug, _, _ := strings.Cut(rel, "/"); skip[slug] {
				continue
			}
		}
		total += obj.Size
	}
	return total, nil
}

// releaseOf is the release prefix `<namespace>/.releases/<slug>/<id>` an object
// key lies under, or "" when it lies under none.
func releaseOf(key string) string {
	ns, rest, ok := strings.Cut(key, "/.releases/")
	if !ok || strings.Contains(ns, "/") {
		return ""
	}
	slug, rest, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	id, _, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	return releasePrefix(ns, slug, id)
}

// measure lists org's footprint and records it. It answers the size measured.
func measure(s *cloud.Service[state], ctx context.Context, org string) (int64, error) {
	cli := s.State.blob.store
	began := time.Now().UnixNano()
	bytes, err := footprint(ctx, s.State.store, cli, s.State.blob.bucket, org)
	if err != nil {
		return 0, err
	}
	return bytes, s.State.store.Measured(ctx, org, bytes, began)
}

// holdings runs the re-measures a publish asks for, off the request, and lets
// Shutdown wait for the ones in flight before the store closes under them. A burst
// of publishes from one org costs one listing in flight and at most one more after
// it, never one per publish.
type holdings struct {
	life context.Context

	mu     sync.Mutex
	closed bool
	busy   map[string]bool // an org whose listing is running
	again  map[string]bool // an org published to while its listing ran
	wg     sync.WaitGroup
}

func newHoldings(life context.Context) *holdings {
	return &holdings{life: life, busy: map[string]bool{}, again: map[string]bool{}}
}

// refresh re-measures org in the background. A publish calls it once its bytes
// have landed, so the storage gate reads the org's new size on the next publish
// rather than at the next sweep. It bills nothing: the sweep bills.
func refresh(s *cloud.Service[state], org string) {
	h := s.State.held
	if h == nil || !s.State.blob.configured() {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	if h.busy[org] {
		h.again[org] = true
		h.mu.Unlock()
		return
	}
	h.busy[org] = true
	h.wg.Add(1)
	h.mu.Unlock()
	s.Go("projects.storage.refresh", []any{"org", org}, func() {
		defer h.wg.Done()
		// Released on every exit, a panic included, so an org is never left marked
		// busy with nothing running to clear it.
		done := false
		defer func() {
			if !done {
				h.mu.Lock()
				delete(h.busy, org)
				delete(h.again, org)
				h.mu.Unlock()
			}
		}()
		for {
			if _, err := measure(s, h.life, org); err != nil && h.life.Err() == nil {
				s.Log.Warn("storage: re-measure failed (the next sweep measures it)", "org", org, "err", err)
			}
			// Deciding to stop and clearing busy happen under one lock, so a publish
			// that lands between them is either seen here or starts its own listing.
			h.mu.Lock()
			if !h.again[org] || h.life.Err() != nil {
				delete(h.busy, org)
				delete(h.again, org)
				done = true
				h.mu.Unlock()
				return
			}
			delete(h.again, org)
			h.mu.Unlock()
		}
	})
}

// wait stops new re-measures and waits for the running ones to end.
func (h *holdings) wait() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.wg.Wait()
}

// ── billing ──────────────────────────────────────────────────────────────────

// sweepStorage is one tick of the storage meter: for every org the store holds,
// it measures the footprint, records it, and charges the span since the org was
// last billed at the size just measured. It answers how many orgs were debited.
//
// list, hold and emit are the edges a test replaces: list is the object store,
// hold is the meter's fence, asked before each org's span is claimed so a pod that
// no longer holds the meter claims nothing, and emit is the ledger. An org whose
// listing fails is skipped with its watermark
// where it was, so the span is billed whole by the next sweep that can see it;
// nothing is billed on a size nobody measured.
//
// First sight starts the clock and charges nothing (cloud.RuntimeCharge), so
// storage held before this meter existed is never billed backwards.
func sweepStorage(
	ctx context.Context,
	store *Store,
	now int64,
	perGBMonth int64,
	list func(ctx context.Context, org string) (int64, error),
	hold func(context.Context) error,
	emit func(org string, u meter.Usage),
) (int, error) {
	orgs, err := store.Holders(ctx)
	if err != nil {
		return 0, err
	}
	debited := 0
	for _, org := range orgs {
		if ctx.Err() != nil {
			return debited, ctx.Err()
		}
		began := time.Now().UnixNano()
		bytes, err := list(ctx, org)
		if err != nil {
			continue
		}
		if err := store.Measured(ctx, org, bytes, began); err != nil {
			return debited, err
		}
		h, err := store.Holding(ctx, org)
		if err != nil {
			return debited, err
		}
		row := cloud.Running{
			ID:        org + "#sites",
			Payer:     org,
			Model:     storageModel,
			MeteredAt: h.MeteredAt,
			Rate:      hourMicros(billable(bytes), perGBMonth),
		}
		advance := func(ctx context.Context, _ string, was, at int64) (bool, error) {
			if err := hold(ctx); err != nil {
				return false, err
			}
			return store.AdvanceStorage(ctx, org, was, at)
		}
		charged, err := cloud.RuntimeCharge(ctx, row, now, advance, func(payer string, u meter.Usage) {
			// The span is hosting's, so it is summed under the hosting service a
			// hosting spend cap reads — the meter stamps its own provider there
			// when the field is empty.
			u.Service = ""
			emit(payer, u)
		})
		if err != nil {
			return debited, err
		}
		if charged > 0 {
			debited++
		}
		if bytes == 0 {
			if err := store.Forget(ctx, org); err != nil {
				return debited, err
			}
		}
	}
	return debited, nil
}

// meterStorage runs sweepStorage every storageSweep until life ends. Storage
// belongs to the org — a site is the org's, not the publisher's — so every debit
// lands on the org's own account.
func meterStorage(s *cloud.Service[state], life context.Context) {
	if !s.State.blob.configured() {
		return
	}
	cli := s.State.blob.store
	list := func(ctx context.Context, org string) (int64, error) {
		bytes, err := footprint(ctx, s.State.store, cli, s.State.blob.bucket, org)
		if err != nil {
			s.Log.Warn("storage meter: listing failed (billed by the next sweep)", "org", org, "err", err)
		}
		return bytes, err
	}
	emit := func(org string, u meter.Usage) {
		s.State.bill.Record(account.Org(org), storageKind, u)
	}
	lead := s.Coord.Lead("projects:storage-meter")
	t := time.NewTicker(storageSweep)
	defer t.Stop()
	for {
		select {
		case <-life.Done():
			return
		case <-t.C:
			lead.Do(life, func(ctx context.Context) {
				n, err := sweepStorage(ctx, s.State.store, time.Now().Unix(), storageRate(ctx), list, lead.Hold, emit)
				if err != nil && ctx.Err() == nil {
					s.Log.Warn("storage meter: sweep stopped early", "debited", n, "err", err)
				}
			})
		}
	}
}
