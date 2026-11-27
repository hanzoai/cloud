package git

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// metrics_store.go — where the estate's open source figures live (#456).
//
// ONE database for the platform, not one per org like the repo rows (store.go):
// the figures are the estate's, across every GitHub org it publishes from, and no
// tenant owns them. It is the system namespace's "forge_metrics" (a schema on
// hanzo-sql, a SQLite file without it), opened by openMetrics (metrics.go)
// through the one opener, locus.Use.
//
// Two tables:
//
//	forge_metrics   one figure per (date, ecosystem, name, metric). A re-post of
//	                the same key REPLACES the value, so a collector that re-sends
//	                a window (GitHub keeps traffic for 14 days, so every run sends
//	                all 14) never double-counts. Nothing deletes a row.
//	forge_packages  which GitHub repository each package is published from, keyed
//	                (ecosystem, name) and found by the repository's upstream,
//	                lowercased. That is how a repo row learns its packages
//	                (withPackages, metrics.go) without mapping an IAM org onto a
//	                forge org.

// metricsSubsystem is the store's name in the system namespace.
const metricsSubsystem = "forge_metrics"

type metricsStore struct {
	db *sql.DB
}

// metricQuery filters a read. An empty field matches everything; From and To
// are inclusive YYYY-MM-DD.
type metricQuery struct {
	Ecosystem, Org, Name, Metric, From, To string
}

// newMetricsStore takes an open database and brings it to the current schema.
func newMetricsStore(db *sql.DB) (*metricsStore, error) {
	m := &metricsStore{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS forge_metrics (
  date       TEXT   NOT NULL,
  kind       TEXT   NOT NULL,
  ecosystem  TEXT   NOT NULL,
  org        TEXT   NOT NULL DEFAULT '',
  name       TEXT   NOT NULL,
  metric     TEXT   NOT NULL,
  value      BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (date, ecosystem, name, metric)
);
CREATE INDEX IF NOT EXISTS ix_forge_metrics_eco_org_metric_date ON forge_metrics(ecosystem, org, metric, date);
CREATE INDEX IF NOT EXISTS ix_forge_metrics_metric_date ON forge_metrics(metric, date);
CREATE TABLE IF NOT EXISTS forge_packages (
  ecosystem  TEXT   NOT NULL,
  name       TEXT   NOT NULL,
  upstream   TEXT   NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (ecosystem, name)
);
CREATE INDEX IF NOT EXISTS ix_forge_packages_upstream ON forge_packages(upstream);
`)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("forge metrics: migrate: %w", err)
	}
	return m, nil
}

func (m *metricsStore) Close() error { return m.db.Close() }

// put writes one checked batch (checkBatch) in ONE transaction: every figure and
// every package lands, or none does.
func (m *metricsStore) put(ctx context.Context, rows []metricRow, pkgs []packageRef) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	if len(rows) > 0 {
		st, err := tx.PrepareContext(ctx, `
INSERT INTO forge_metrics (date, kind, ecosystem, org, name, metric, value, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (date, ecosystem, name, metric) DO UPDATE SET
  kind = excluded.kind, org = excluded.org, value = excluded.value, updated_at = excluded.updated_at`)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		for _, r := range rows {
			if _, err := st.ExecContext(ctx, r.Date, r.Kind, r.Ecosystem, r.Org, r.Name, r.Metric, r.Value, now); err != nil {
				return err
			}
		}
	}
	if len(pkgs) > 0 {
		st, err := tx.PrepareContext(ctx, `
INSERT INTO forge_packages (ecosystem, name, upstream, updated_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (ecosystem, name) DO UPDATE SET upstream = excluded.upstream, updated_at = excluded.updated_at`)
		if err != nil {
			return err
		}
		defer func() { _ = st.Close() }()
		for _, p := range pkgs {
			if _, err := st.ExecContext(ctx, p.Ecosystem, p.Name, strings.ToLower(p.Upstream), now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// query reads the figures q matches, ordered (ecosystem, name, metric, date), at
// most limit of them; more says there were more than limit.
func (m *metricsStore) query(ctx context.Context, q metricQuery, limit int) (rows []metricRow, more bool, err error) {
	var where []string
	var args []any
	add := func(clause, v string) {
		if v == "" {
			return
		}
		args = append(args, v)
		where = append(where, clause+" $"+strconv.Itoa(len(args)))
	}
	add("ecosystem =", q.Ecosystem)
	add("org =", q.Org)
	add("name =", q.Name)
	add("metric =", q.Metric)
	add("date >=", q.From)
	add("date <=", q.To)
	sqlText := `SELECT date, kind, ecosystem, org, name, metric, value FROM forge_metrics`
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit+1)
	sqlText += " ORDER BY ecosystem, name, metric, date LIMIT $" + strconv.Itoa(len(args))

	rs, err := m.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		var r metricRow
		if err := rs.Scan(&r.Date, &r.Kind, &r.Ecosystem, &r.Org, &r.Name, &r.Metric, &r.Value); err != nil {
			return nil, false, err
		}
		if len(rows) == limit {
			return rows, true, nil
		}
		rows = append(rows, r)
	}
	return rows, false, rs.Err()
}

// packagesFor answers the packages registered against each upstream, keyed by
// the upstream lowercased, in (ecosystem, name) order. One read, whatever the
// number of upstreams; none asked, none read.
func (m *metricsStore) packagesFor(ctx context.Context, upstreams []string) (map[string][]packageRef, error) {
	out := map[string][]packageRef{}
	if len(upstreams) == 0 {
		return out, nil
	}
	seen := map[string]bool{}
	var args []any
	var marks []string
	for _, u := range upstreams {
		u = strings.ToLower(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		args = append(args, u)
		marks = append(marks, "$"+strconv.Itoa(len(args)))
	}
	if len(args) == 0 {
		return out, nil
	}
	rs, err := m.db.QueryContext(ctx, `SELECT ecosystem, name, upstream FROM forge_packages WHERE upstream IN (`+
		strings.Join(marks, ", ")+`) ORDER BY ecosystem, name`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		var p packageRef
		if err := rs.Scan(&p.Ecosystem, &p.Name, &p.Upstream); err != nil {
			return nil, err
		}
		out[p.Upstream] = append(out[p.Upstream], p)
	}
	return out, rs.Err()
}

// allPackages answers every registered package, in (ecosystem, name) order.
func (m *metricsStore) allPackages(ctx context.Context) ([]packageRef, error) {
	rs, err := m.db.QueryContext(ctx, `SELECT ecosystem, name, upstream FROM forge_packages ORDER BY ecosystem, name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []packageRef
	for rs.Next() {
		var p packageRef
		if err := rs.Scan(&p.Ecosystem, &p.Name, &p.Upstream); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rs.Err()
}

// latestBefore returns the most recent metric value recorded strictly before
// beforeDay (YYYY-MM-DD) for each entity in ecosystem with metric, keyed by
// entity name. Used to compute daily deltas from cumulative gauges (Docker Hub
// pulls_total, crates.io downloads_total, GitHub release downloads_total).
func (m *metricsStore) latestBefore(ctx context.Context, ecosystem, metric, beforeDay string) (map[string]int64, error) {
	rs, err := m.db.QueryContext(ctx, `
SELECT m.name, m.value
FROM forge_metrics m
INNER JOIN (
  SELECT name, MAX(date) AS max_date
  FROM forge_metrics
  WHERE ecosystem = $1 AND metric = $2 AND date < $3
  GROUP BY name
) latest ON m.name = latest.name AND m.date = latest.max_date
WHERE m.ecosystem = $4 AND m.metric = $5`, ecosystem, metric, beforeDay, ecosystem, metric)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	out := map[string]int64{}
	for rs.Next() {
		var name string
		var value int64
		if err := rs.Scan(&name, &value); err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, rs.Err()
}

// PurgeUnownedPackages removes package registrations and package-ecosystem
// metric rows whose package name does not belong to the Hanzo estate
// (isEstatePackageName). GitHub repository rows are untouched.
func (m *metricsStore) PurgeUnownedPackages(ctx context.Context) (pkgsDeleted, metricsDeleted int64, err error) {
	return m.purgeUnownedPackages(ctx, isEstatePackageName)
}

func (m *metricsStore) purgeUnownedPackages(ctx context.Context, owned func(eco, name string) bool) (pkgsDeleted, metricsDeleted int64, err error) {
	type key struct{ eco, name string }
	var badPkgs []key
	rs, err := m.db.QueryContext(ctx, `SELECT ecosystem, name FROM forge_packages`)
	if err != nil {
		return 0, 0, err
	}
	for rs.Next() {
		var k key
		if err := rs.Scan(&k.eco, &k.name); err != nil {
			_ = rs.Close()
			return 0, 0, err
		}
		if !owned(k.eco, k.name) {
			badPkgs = append(badPkgs, k)
		}
	}
	if err := rs.Err(); err != nil {
		_ = rs.Close()
		return 0, 0, err
	}
	_ = rs.Close()

	var badMetrics []key
	mrs, err := m.db.QueryContext(ctx, `SELECT DISTINCT ecosystem, name FROM forge_metrics WHERE ecosystem <> 'github'`)
	if err != nil {
		return 0, 0, err
	}
	for mrs.Next() {
		var k key
		if err := mrs.Scan(&k.eco, &k.name); err != nil {
			_ = mrs.Close()
			return 0, 0, err
		}
		if !owned(k.eco, k.name) {
			badMetrics = append(badMetrics, k)
		}
	}
	if err := mrs.Err(); err != nil {
		_ = mrs.Close()
		return 0, 0, err
	}
	_ = mrs.Close()

	if len(badPkgs) == 0 && len(badMetrics) == 0 {
		return 0, 0, nil
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if len(badPkgs) > 0 {
		st, err := tx.PrepareContext(ctx, `DELETE FROM forge_packages WHERE ecosystem = $1 AND name = $2`)
		if err != nil {
			return 0, 0, err
		}
		defer func() { _ = st.Close() }()
		for _, k := range badPkgs {
			res, err := st.ExecContext(ctx, k.eco, k.name)
			if err != nil {
				return 0, 0, err
			}
			if n, err := res.RowsAffected(); err == nil {
				pkgsDeleted += n
			}
		}
	}
	if len(badMetrics) > 0 {
		st, err := tx.PrepareContext(ctx, `DELETE FROM forge_metrics WHERE ecosystem = $1 AND name = $2`)
		if err != nil {
			return 0, 0, err
		}
		defer func() { _ = st.Close() }()
		for _, k := range badMetrics {
			res, err := st.ExecContext(ctx, k.eco, k.name)
			if err != nil {
				return 0, 0, err
			}
			if n, err := res.RowsAffected(); err == nil {
				metricsDeleted += n
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return pkgsDeleted, metricsDeleted, nil
}
