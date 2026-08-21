package destination

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
)

// report.go is the INBOUND half of this plane. Send pushes conversions OUT to a
// platform; Report pulls back what that platform then charged for them — spend,
// impressions, clicks, and the conversions and revenue the platform itself
// attributed. Without it a campaign's conversions sit in the warehouse with no
// cost beside them, and return on ad spend is a number nobody can compute.
//
// It mirrors the outbound shape exactly and, more importantly, shares its
// CUSTODY. A Reporter self-registers from init() (one file per platform), and it
// is keyed by the SAME platform slug as the Destination, which is what lets it
// read the org's existing store Row and the existing KMS-sealed credential
// through the same resolveSecret the fan-out uses. Nothing here invents a secret,
// a store, a connection, or a second definition of who an org is.
//
// Reported numbers land in the warehouse cloud already owns (apps/datastore) as
// hanzo.ad_report, one row per (org, platform, campaign, day) — the grain every
// platform reports at.

// ── the interlingua ──────────────────────────────────────────────────────────

// Window is the closed range of days a pull covers.
type Window struct{ Start, End time.Time }

// maxWindow bounds one pull. A platform restates recent days and paginates old
// ones; a caller asking for years would silently receive one page of an answer it
// believed was complete. Refusing is the honest response to a window we would
// otherwise have to truncate.
const maxWindow = 92 * 24 * time.Hour

// days snaps a window to whole UTC days — the grain every platform reports at and
// the grain the table stores. Snapping here rather than per adapter is what stops
// one call asking two platforms for two different sets of days.
func (w Window) days() Window {
	return Window{Start: w.Start.UTC().Truncate(24 * time.Hour), End: w.End.UTC().Truncate(24 * time.Hour)}
}

// check refuses a window no platform can answer honestly.
func (w Window) check() error {
	if w.Start.IsZero() || w.End.IsZero() {
		return fmt.Errorf("report window needs a start and an end")
	}
	if w.End.Before(w.Start) {
		return fmt.Errorf("report window ends before it starts")
	}
	if w.End.Sub(w.Start) > maxWindow {
		return fmt.Errorf("report window is longer than %d days", int(maxWindow.Hours()/24))
	}
	return nil
}

// day renders a window bound as the ISO date both Meta and Google Ads take.
func day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// Metric is one campaign-day as a platform reported it — the inbound mirror of
// Conversion, and the ONE shape every Reporter renders into.
//
// Spend and Revenue are money in the ad account's own Currency, not minor units:
// the platforms report a decimal amount, and rounding it to cents here would turn
// a figure the org can reconcile against an invoice into one it cannot.
type Metric struct {
	Day         time.Time // UTC midnight of the day reported
	Campaign    string    // the platform's own campaign id
	Name        string    // the campaign's name, as the platform spells it
	Currency    string    // the ad account's currency, ISO 4217
	Spend       float64   // what the org paid the platform
	Impressions int64
	Clicks      int64
	// Conversions is what the PLATFORM attributed, which is not what we sent it:
	// its attribution model decides, and the count is fractional under a
	// data-driven model. Keeping the platform's own number is the point — the
	// conversions we pushed are already in the warehouse to compare it against.
	Conversions float64
	// Revenue is the value the platform attributes to those conversions. It is
	// stored beside Spend rather than as a ratio because return on ad spend is a
	// division a reader does over whatever grouping it asks for; a stored ratio
	// can only ever be re-divided wrongly.
	Revenue float64
}

// Reporter is one ad platform Hanzo pulls reported performance FROM — the inbound
// mirror of Destination, sharing its identity and its custody. ID is the SAME
// platform slug, which is what lets a Reporter read the org's existing
// destination Row and its existing credential rather than a second connection and
// a second secret. Report is pure over (Config, secret, Window) for ONE org: it
// reads no global state and no other tenant's data.
type Reporter interface {
	ID() string
	Report(ctx context.Context, cfg Config, secret string, w Window) ([]Metric, error)
}

// reporters is populated by each report_<platform>.go from its init(), the way
// registry is. A platform with no entry is simply not pulled — every destination
// keeps forwarding, and teaching one to report back is one new file and no change
// to anything here.
var reporters = map[string]Reporter{}

// registerReporter adds a reporter. A nil/empty/duplicate id is a programming
// error and panics at init — two reporters cannot own one platform slug.
func registerReporter(r Reporter) {
	if r == nil || r.ID() == "" {
		panic("destinations: register nil/empty reporter")
	}
	if _, dup := reporters[r.ID()]; dup {
		panic("destinations: duplicate reporter id " + r.ID())
	}
	reporters[r.ID()] = r
}

// ── inbound transport ────────────────────────────────────────────────────────

// getJSON GETs endpoint with headers and decodes the 2xx JSON body into out. It
// is the inbound sibling of postJSON and shares its client, its bounded read and
// its discipline: a reporting credential rides an Authorization header, never the
// URL, and a transport failure is never wrapped, because *url.Error echoes the
// endpoint into every log line that touches it.
func getJSON(ctx context.Context, platform, endpoint string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%s: build report request", platform)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := sendHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s: report request failed", platform)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSendRespBody))
	if err != nil {
		return fmt.Errorf("%s: read report response", platform)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: http %d: %s", platform, resp.StatusCode, truncate(raw, 512))
	}
	if len(raw) == 0 || out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s: decode report response", platform)
	}
	return nil
}

// maxPages breaks a pagination LOOP; it is not a cap on legitimate data, and
// sizing it as one is how a report starts under-reporting spend. At 500 rows a
// page (Meta) and 10,000 (Google Ads) it admits a hundred thousand and two
// million campaign-days — far beyond what the longest window a pull accepts can
// hold — so reaching it means the platform is repeating itself, and erroring
// beats both paging forever and truncating in silence.
const maxPages = 200

// num parses a platform's numeric field. Both Meta and Google Ads render an int64
// as a JSON STRING — proto3's encoding, and Meta's own convention — so counts and
// amounts arrive quoted. An absent or unparseable field reads as zero: a metric
// the platform did not report is zero, not a failed day.
func num(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

// ── the table ────────────────────────────────────────────────────────────────

// hanzo.ad_report is what the platforms said: one row per (org, platform,
// campaign, day). This plane OWNS the table and creates it idempotently, the way
// leaderboard owns hanzo.usage_rollup_daily and dataset owns hanzo.risk_dataset.
//
// ENGINE CHOICE — ReplacingMergeTree over pulled_at, and it is the load-bearing
// decision in this file. A platform RESTATES a day for weeks after it closes, as
// conversions attribute late and spend finalises, so the same (org, platform,
// campaign, day) is pulled again and again and only the LATEST statement is true.
// SummingMergeTree — the engine the usage rollup uses, because a usage ledger
// only ever appends — would accumulate every restatement into a number no
// platform ever reported and that nothing afterwards could separate. Replacing,
// keyed on the report grain and versioned by the pull time, is what makes a
// re-pull idempotent, which is in turn what makes a schedule safe to re-run and a
// backfill safe to overlap a live window.
const reportTable = "hanzo.ad_report"

const reportDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.ad_report (
		org String,
		platform String,
		campaign String,
		day Date,
		name String,
		currency String,
		spend Float64,
		impressions UInt64,
		clicks UInt64,
		conversions Float64,
		revenue Float64,
		pulled_at DateTime
	) ENGINE = ReplacingMergeTree(pulled_at)
	PARTITION BY toYYYYMM(day)
	ORDER BY (org, platform, campaign, day)
	TTL day + INTERVAL 2 YEAR`

// reportColumns is the insert's column list, and reportRow the bound placeholder
// tuple DERIVED from it, so the two cannot drift into an insert that binds the
// wrong value to the wrong column.
const reportColumns = `org,platform,campaign,day,name,currency,spend,impressions,clicks,conversions,revenue,pulled_at`

var reportRow = "(?" + strings.Repeat(",?", strings.Count(reportColumns, ",")) + ")"

// warehouseReady and warehouseExec are cloud's ONE warehouse connection as this
// plane uses it — two calls and no more, so this package cannot reach the
// connection for anything else. Package vars so a test observes the statement
// without standing a warehouse up, the seam metaGraph already is for the outbound
// side; never reassigned in production.
var (
	warehouseReady = datastore.Ready
	warehouseExec  = datastore.Exec
)

// reportProvisioned latches the table's existence. Only SUCCESS latches, so a
// warehouse still connecting at boot is retried on the next pull rather than
// poisoned for the life of the process.
var reportProvisioned atomic.Bool

func ensureReport(ctx context.Context) error {
	if reportProvisioned.Load() {
		return nil
	}
	if !warehouseReady() {
		return fmt.Errorf("destinations: warehouse is not connected")
	}
	// A fresh warehouse has no hanzo database and this pull may be its first
	// writer — the same first step apps/datastore and sbom take.
	if err := warehouseExec(ctx, `CREATE DATABASE IF NOT EXISTS hanzo`); err != nil {
		return fmt.Errorf("destinations: ensure database: %w", err)
	}
	if err := warehouseExec(ctx, reportDDL); err != nil {
		return fmt.Errorf("destinations: ensure %s: %w", reportTable, err)
	}
	reportProvisioned.Store(true)
	return nil
}

// record writes one platform's report for one org. Nothing caller-derived is ever
// statement TEXT: the table and the columns are constants and every value binds,
// so a campaign name holds whatever the platform put in it.
func record(ctx context.Context, org, platform string, ms []Metric) error {
	if len(ms) == 0 {
		return nil
	}
	if err := ensureReport(ctx); err != nil {
		return err
	}
	pulled := time.Now().UTC()
	values := make([]string, 0, len(ms))
	args := make([]any, 0, len(ms)*strings.Count(reportRow, "?"))
	for _, m := range ms {
		values = append(values, reportRow)
		args = append(args, org, platform, m.Campaign, m.Day.UTC(), m.Name, m.Currency,
			m.Spend, m.Impressions, m.Clicks, m.Conversions, m.Revenue, pulled)
	}
	return warehouseExec(ctx, `INSERT INTO `+reportTable+` (`+reportColumns+`) VALUES `+strings.Join(values, ","), args...)
}

// ── the driver ───────────────────────────────────────────────────────────────

// reportTimeout bounds one platform's pull. A reporting API aggregates before it
// answers, so it is slower than the ingest endpoints sendTimeout was cut for.
const reportTimeout = 60 * time.Second

// Pull runs every enabled destination that has a Reporter for org over w, and
// records what each platform reported. It is the seam an on-demand caller and a
// schedule both drive — one driver, not two paths — and it answers with the rows
// written per platform.
func Pull(ctx context.Context, org string, w Window) (map[string]int, error) {
	if mounted == nil {
		return nil, fmt.Errorf("destinations: not mounted")
	}
	return pull(mounted, ctx, org, w)
}

// pull is Pull over an explicit service. FAIL-SOFT per platform, exactly as the
// fan-out is: one platform's stale credential or refused window must not cost an
// org the platforms that did answer. A platform absent from the result reported
// nothing, which is a fact and not an error.
func pull(s *cloud.Service[state], ctx context.Context, org string, w Window) (map[string]int, error) {
	if !validOrg(org) {
		return nil, fmt.Errorf("destinations: org must be a DNS-1123 label")
	}
	w = w.days()
	if err := w.check(); err != nil {
		return nil, err
	}
	rows, err := s.State.store.ListEnabled(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("destinations: read connected destinations: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		rep, ok := reporters[row.Platform]
		if !ok {
			continue // forwards fine; nothing to pull back yet
		}
		dest, ok := s.State.dests[row.Platform]
		if !ok {
			continue
		}
		secret, err := resolveSecret(s, org, dest, row.Config)
		if err != nil {
			s.Log.Debug("destinations pull skipped (no credential)", "platform", row.Platform, "org", org)
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, reportTimeout)
		ms, err := rep.Report(rctx, row.Config, secret, w)
		cancel()
		if err != nil {
			s.Log.Warn("destination report failed", "platform", row.Platform, "org", org, "err", err)
			continue
		}
		if err := record(ctx, org, row.Platform, ms); err != nil {
			s.Log.Warn("destination report not recorded", "platform", row.Platform, "org", org, "err", err)
			continue
		}
		if len(ms) > 0 {
			out[row.Platform] = len(ms)
		}
		s.Log.Debug("destination reported", "platform", row.Platform, "org", org, "rows", len(ms))
	}
	return out, nil
}
