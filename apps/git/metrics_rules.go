package git

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// metrics_rules.go — what a figure of the estate's open source adoption may be,
// and how a series of them rolls up (#456). Pure: no store, no request. The ops
// that take and answer figures are metrics.go, the table they live in is
// metrics_store.go.
//
// A FIGURE is one entity's one metric on one UTC day: a repository's stars on
// GitHub, a package's downloads on npm. The collector reads them off GitHub and
// the registries once a day and posts them; nothing here computes one.

// Ecosystems a figure can come from. "github" is where repositories' figures
// come from; the rest are package registries.
var ecosystems = map[string]bool{"github": true, "npm": true, "pypi": true, "crates": true, "dockerhub": true}

// kinds of entity a figure is about.
var kinds = map[string]bool{"repo": true, "package": true, "org": true}

// metricKinds is the closed table of metrics and how each rolls up over a month
// or a group. A GAUGE is a running total read off the source (stars, all-time
// downloads), so a month is its LAST day and two days never add. A COUNT is what
// happened that day (a day's downloads, clones, views), so a month is the sum.
var metricKinds = map[string]string{
	"stars":           "gauge",
	"forks":           "gauge",
	"open_issues":     "gauge",
	"watchers":        "gauge",
	"downloads_total": "gauge",
	"pulls_total":     "gauge",

	"downloads":         "count",
	"pulls":             "count",
	"release_downloads": "count",
	"clones":            "count",
	"views":             "count",
	"unique_clones":     "count",
	"unique_views":      "count",
}

const (
	// maxBatch bounds rows and packages per POST, each.
	maxBatch = 5000
	// maxNameLen is npm's limit on a package name, the longest of the registries.
	maxNameLen = 214
	maxOrgLen  = 100
	// maxDaySpan and maxMonthSpan bound a read's window, in days, inclusive.
	maxDaySpan   = 400
	maxMonthSpan = 3660
	// defaultSpan is the window a read with no from gets: 90 days to today.
	defaultSpan = 90
	// maxSeries and maxRows bound one read's answer and what it reads to make it.
	maxSeries = 1000
	maxRows   = 500000
)

const dayLayout = "2006-01-02"

// metricRow is one figure as stored.
type metricRow struct {
	Date, Kind, Ecosystem, Org, Name, Metric string
	Value                                    int64
}

// packageRef registers a package as published from a GitHub repository.
type packageRef struct {
	Ecosystem, Name, Upstream string
}

// metricSeries is one group's one metric over the window.
type metricSeries struct {
	// Key names the group: an ecosystem ("npm"), an org ("hanzoai"), or an
	// entity as ecosystem/name ("npm/@hanzo/ui", "github/hanzoai/ui").
	Key string `json:"key"`
	// Metric is the metric's name, as posted ("stars", "downloads").
	Metric string `json:"metric"`
	// Kind is how the metric rolls up: "gauge" takes an entity's last day in a
	// month, "count" sums its days. A group sums its entities either way.
	Kind string `json:"kind"`
	// Points are the series' values, oldest first. A day or month with no figure
	// has no point.
	Points []metricPoint `json:"points"`
}

// metricPoint is one value of a series.
type metricPoint struct {
	// Date is YYYY-MM-DD at day granularity and YYYY-MM at month granularity.
	Date string `json:"date"`
	// Value is the group's figure for that date.
	Value int64 `json:"value"`
}

// metricWindow is a checked read window.
type metricWindow struct {
	From, To, Granularity, By string
}

var repoSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// normUpstream reads a GitHub repository address in any of the forms a mirror,
// a link or a collector spells it (https URL, scp-style SSH, bare host/path, with
// or without .git) as github.com/<owner>/<repo>, in the case given. Any other
// host, an address carrying a credential, and a path that is not exactly
// owner/repo are not an upstream.
func normUpstream(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	var host, path string
	switch {
	case strings.HasPrefix(s, "git@github.com:"):
		host, path = "github.com", strings.TrimPrefix(s, "git@github.com:")
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" && u.Scheme != "git") {
			return "", false
		}
		host, path = u.Host, u.Path
	default:
		if strings.Contains(s, "@") {
			return "", false
		}
		host, path, _ = strings.Cut(s, "/")
	}
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	if host != "github.com" {
		return "", false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !repoSegment.MatchString(parts[0]) || !repoSegment.MatchString(parts[1]) {
		return "", false
	}
	return "github.com/" + parts[0] + "/" + parts[1], true
}

// checkBatch validates one POST whole, trimming as it goes: the first bad row
// or package refuses all of it, named by its index, so nothing is half-written.
// A package's upstream comes back normalised and lowercased, the registry's key.
func checkBatch(rows []metricRow, pkgs []packageRef, now time.Time) ([]metricRow, []packageRef, error) {
	if len(rows) == 0 && len(pkgs) == 0 {
		return nil, nil, errors.New("rows or packages are required")
	}
	if len(rows) > maxBatch {
		return nil, nil, fmt.Errorf("rows: at most %d per call", maxBatch)
	}
	if len(pkgs) > maxBatch {
		return nil, nil, fmt.Errorf("packages: at most %d per call", maxBatch)
	}
	y, m, d := now.UTC().Date()
	latest := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)

	outRows := make([]metricRow, len(rows))
	for i, r := range rows {
		r.Date, r.Kind, r.Ecosystem = strings.TrimSpace(r.Date), strings.TrimSpace(r.Kind), strings.TrimSpace(r.Ecosystem)
		r.Org, r.Name, r.Metric = strings.TrimSpace(r.Org), strings.TrimSpace(r.Name), strings.TrimSpace(r.Metric)
		if err := checkRow(r, latest); err != nil {
			return nil, nil, fmt.Errorf("rows[%d]: %w", i, err)
		}
		outRows[i] = r
	}

	outPkgs := make([]packageRef, len(pkgs))
	for i, p := range pkgs {
		p.Ecosystem, p.Name = strings.TrimSpace(p.Ecosystem), strings.TrimSpace(p.Name)
		up, err := checkPackage(p)
		if err != nil {
			return nil, nil, fmt.Errorf("packages[%d]: %w", i, err)
		}
		p.Upstream = up
		outPkgs[i] = p
	}
	return outRows, outPkgs, nil
}

func checkRow(r metricRow, latest time.Time) error {
	if err := dialect.Text(r.Date, r.Kind, r.Ecosystem, r.Org, r.Name, r.Metric); err != nil {
		return err
	}
	if err := checkName(r.Name); err != nil {
		return err
	}
	if len(r.Org) > maxOrgLen {
		return fmt.Errorf("org is longer than %d", maxOrgLen)
	}
	if !kinds[r.Kind] {
		return fmt.Errorf("unknown kind %q", r.Kind)
	}
	if !ecosystems[r.Ecosystem] {
		return fmt.Errorf("unknown ecosystem %q", r.Ecosystem)
	}
	if _, ok := metricKinds[r.Metric]; !ok {
		return fmt.Errorf("unknown metric %q", r.Metric)
	}
	day, err := time.Parse(dayLayout, r.Date)
	if err != nil {
		return fmt.Errorf("date %q is not YYYY-MM-DD", r.Date)
	}
	if day.After(latest) {
		return fmt.Errorf("date %q is in the future", r.Date)
	}
	if r.Value < 0 {
		return fmt.Errorf("value %d is negative", r.Value)
	}
	return nil
}

func checkPackage(p packageRef) (string, error) {
	if err := dialect.Text(p.Ecosystem, p.Name, p.Upstream); err != nil {
		return "", err
	}
	if err := checkName(p.Name); err != nil {
		return "", err
	}
	if p.Ecosystem == "github" {
		return "", fmt.Errorf("ecosystem %q is not a package registry", p.Ecosystem)
	}
	if !ecosystems[p.Ecosystem] {
		return "", fmt.Errorf("unknown ecosystem %q", p.Ecosystem)
	}
	up, ok := normUpstream(p.Upstream)
	if !ok {
		return "", fmt.Errorf("upstream %q is not github.com/<owner>/<repo>", p.Upstream)
	}
	return strings.ToLower(up), nil
}

func checkName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("name is longer than %d", maxNameLen)
	}
	return nil
}

// checkWindow resolves a read's window: to defaults to today (UTC), from to the
// 90 days ending at to, granularity to "day" and by to "name". A window wider
// than its granularity allows is refused rather than cut, so a caller never
// mistakes a cut answer for the whole one.
func checkWindow(from, to, granularity, by string, now time.Time) (metricWindow, error) {
	w := metricWindow{Granularity: granularity, By: by}
	if w.Granularity == "" {
		w.Granularity = "day"
	}
	if w.By == "" {
		w.By = "name"
	}
	limit := maxDaySpan
	switch w.Granularity {
	case "day":
	case "month":
		limit = maxMonthSpan
	default:
		return w, fmt.Errorf("granularity %q: want day or month", granularity)
	}
	switch w.By {
	case "ecosystem", "org", "name":
	default:
		return w, fmt.Errorf("by %q: want ecosystem, org or name", by)
	}
	end := now.UTC()
	if to != "" {
		t, err := time.Parse(dayLayout, to)
		if err != nil {
			return w, fmt.Errorf("to %q is not YYYY-MM-DD", to)
		}
		end = t
	}
	end = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -(defaultSpan - 1))
	if from != "" {
		t, err := time.Parse(dayLayout, from)
		if err != nil {
			return w, fmt.Errorf("from %q is not YYYY-MM-DD", from)
		}
		start = t
	}
	if start.After(end) {
		return w, fmt.Errorf("from %s is after to %s", start.Format(dayLayout), end.Format(dayLayout))
	}
	if days := int(end.Sub(start).Hours()/24) + 1; days > limit {
		return w, fmt.Errorf("%d days at %s granularity: at most %d", days, w.Granularity, limit)
	}
	w.From, w.To = start.Format(dayLayout), end.Format(dayLayout)
	return w, nil
}

// rollup turns stored figures into series: each entity's figures are first put
// into the granularity's buckets (a gauge keeps its latest day in the bucket, a
// count sums its days), then the entities of a group are summed per bucket.
// Series come back ordered by key then metric, points oldest first.
func rollup(rows []metricRow, granularity, by string) []metricSeries {
	type cell struct {
		date  string
		value int64
	}
	type entityBucket struct{ entity, metric, bucket string }
	type groupMetric struct{ key, metric string }

	entities := map[entityBucket]cell{}
	groupOf := map[string]string{}
	for _, r := range rows {
		entity := r.Ecosystem + "/" + r.Name
		switch by {
		case "ecosystem":
			groupOf[entity] = r.Ecosystem
		case "org":
			groupOf[entity] = r.Org
		default:
			groupOf[entity] = entity
		}
		bucket := r.Date
		if granularity == "month" && len(bucket) >= 7 {
			bucket = bucket[:7]
		}
		k := entityBucket{entity, r.Metric, bucket}
		c, seen := entities[k]
		if metricKinds[r.Metric] == "count" {
			c.value += r.Value
		} else if !seen || r.Date >= c.date {
			c = cell{date: r.Date, value: r.Value}
		}
		entities[k] = c
	}

	groups := map[groupMetric]map[string]int64{}
	for k, c := range entities {
		g := groupMetric{groupOf[k.entity], k.metric}
		if groups[g] == nil {
			groups[g] = map[string]int64{}
		}
		groups[g][k.bucket] += c.value
	}

	out := make([]metricSeries, 0, len(groups))
	for g, buckets := range groups {
		s := metricSeries{Key: g.key, Metric: g.metric, Kind: metricKinds[g.metric], Points: make([]metricPoint, 0, len(buckets))}
		for date, v := range buckets {
			s.Points = append(s.Points, metricPoint{Date: date, Value: v})
		}
		sort.Slice(s.Points, func(i, j int) bool { return s.Points[i].Date < s.Points[j].Date })
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}
