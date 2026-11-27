// pulse.go — THE PULSE: real activity, adoption and spread, as time series.
//
//	GET /v1/leaderboard/pulse     public; counts by day and by hour, no org, no name
//
// Thirty days by day and the last day by hour of: tokens used, orgs active, orgs
// signed up, orgs that signed up through a referral link, orgs trying a model for the
// first time, and the variety of models in use. Each is read from where cloud already
// keeps it (the usage rollup and ledger in the warehouse, IAM's directory, the
// referral ledger), and a series whose source cannot be read is left out and named in
// `missing`, never drawn as zeroes.
//
// Two series are not recorded anywhere today, so they are always missing: orgs
// trying a PRODUCT for the first time (the usage ledger has a model column and no
// product one), and shares (no share press or verification is written to the
// warehouse, only counted on the marketing side, and a press cannot be verified).
//
// PUBLIC, like the standing: the op asks nobody who they are, and what it answers is
// aggregates that name no org.

package leaderboard

import (
	"context"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	referralpeer "github.com/hanzoai/cloud/client/referral"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

const (
	// pulseDays and pulseHours are the length of the two windows.
	pulseDays  = 30
	pulseHours = 24
	// pulseTTL is how long a read of the pulse is reused.
	pulseTTL = time.Minute
	// triedModels is how many models the first-try tally names.
	triedModels = 10
)

// bases are the models the public listing does not name: those Zen is built from
// (apps/research served.go keeps the same list, for the same reason).
var bases = regexp.MustCompile(`(^|[^a-z])(qwen|kimi|glm|alibaba|moonshot|zhipu|laya)`)

// referralSeriesFn is the referral ledger's moments, a variable so a test can answer it.
var referralSeriesFn = defaultReferralSeries

var defaultReferralSeries = referralpeer.ReferralSeries

// PulseSeries is one count over time.
type PulseSeries struct {
	// Days is one count a day, aligned with PulseView.Days.
	Days []int64 `json:"days"`
	// Hours is one count an hour, aligned with PulseView.Hours. Absent for a series
	// recorded by the day alone.
	Hours []int64 `json:"hours,omitempty"`
}

// PulseTally is how many orgs did something, by name.
type PulseTally struct {
	// Name is the model.
	Name string `json:"name"`
	// Count is how many orgs used it for the first time in the window.
	Count int64 `json:"count"`
}

// PulseView is the whole answer of GET /v1/leaderboard/pulse.
type PulseView struct {
	// Available is false when no series could be read at all.
	Available bool `json:"available"`
	// Days are the thirty days counted, oldest first, "2006-01-02" in UTC, ending today.
	Days []string `json:"days"`
	// Hours are the twenty-four hours counted, oldest first, RFC 3339 in UTC, ending in the current hour.
	Hours []string `json:"hours"`
	// Tokens is the tokens used.
	Tokens *PulseSeries `json:"tokens,omitempty"`
	// Orgs is the orgs that used anything.
	Orgs *PulseSeries `json:"orgs,omitempty"`
	// Signups is the orgs that signed up.
	Signups *PulseSeries `json:"signups,omitempty"`
	// Referred is the orgs that signed up through a referral link.
	Referred *PulseSeries `json:"referred,omitempty"`
	// Models is the orgs that used a model for the first time, by day.
	Models *PulseSeries `json:"models,omitempty"`
	// Variety is how many distinct Hanzo models were asked for, platform wide, in each
	// day and each hour (the ledger's model column; "models" above is the first tries).
	// An id is compared lower-cased and trimmed, so "Zen4" and "zen4 " are one model; the
	// empty id and any id with a "/" in it, the vendor's path for the model served, do
	// not count. Two different ids for one model are two here: no alias table is read.
	Variety *PulseSeries `json:"variety,omitempty"`
	// Tries is the models orgs used for the first time in the window, most first.
	Tries []PulseTally `json:"tries"`
	// Missing names every series not drawn: the ones nothing records (products,
	// shares) and the ones whose source could not be read just now.
	Missing []string `json:"missing"`
}

// reader is what a read of the board says a source's failure to.
type reader interface{ Warn(msg string, args ...any) }

// pulses is the one shared read of the pulse.
var pulses memo[PulseView]

// stamp reads a time cell: the driver's time.Time, or a string a JSON transport gives.
func stamp(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t.UTC(), true
	case string:
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
			if at, err := time.Parse(layout, strings.TrimSpace(t)); err == nil {
				return at.UTC(), true
			}
		}
	}
	return time.Time{}, false
}

// axes is the windows' labels, and the index each moment falls on.
type axes struct {
	days, hours   []string
	dayAt, hourAt map[string]int
	from, hour0   time.Time
}

func newAxes(now time.Time) axes {
	a := axes{dayAt: map[string]int{}, hourAt: map[string]int{}}
	a.from = now.Truncate(24*time.Hour).AddDate(0, 0, -(pulseDays - 1))
	a.hour0 = now.Truncate(time.Hour).Add(-(pulseHours - 1) * time.Hour)
	for i := range pulseDays {
		d := a.from.AddDate(0, 0, i).Format(time.DateOnly)
		a.days, a.dayAt[d] = append(a.days, d), i
	}
	for i := range pulseHours {
		h := a.hour0.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		a.hours, a.hourAt[h] = append(a.hours, h), i
	}
	return a
}

func (a axes) series(hourly bool) *PulseSeries {
	s := &PulseSeries{Days: make([]int64, pulseDays)}
	if hourly {
		s.Hours = make([]int64, pulseHours)
	}
	return s
}

// day and hour add n to the bucket t falls in, if it falls in the window.
func (a axes) day(s *PulseSeries, t time.Time, n int64) {
	if i, ok := a.dayAt[t.UTC().Format(time.DateOnly)]; ok {
		s.Days[i] += n
	}
}

func (a axes) hour(s *PulseSeries, t time.Time, n int64) {
	if i, ok := a.hourAt[t.UTC().Truncate(time.Hour).Format(time.RFC3339)]; ok && s.Hours != nil {
		s.Hours[i] += n
	}
}

// modelID is the Hanzo model a ledger row asked for, keyed as the gateway keys it:
// lower case and trimmed. It names none when the id is empty or holds a "/", the
// vendor's path for the model that served the request.
func modelID(raw string) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(raw))
	return id, id != "" && !strings.Contains(id, "/")
}

// distinct adds to s, through add, how many different models the (col, model) rows name
// in each bucket col falls in. An id counts once in a bucket however it was spelled.
func (a axes) distinct(s *PulseSeries, rows []map[string]any, col string, add func(*PulseSeries, time.Time, int64)) {
	seen := map[int64]map[string]bool{}
	for _, r := range rows {
		t, ok := stamp(r[col])
		id, named := modelID(aString(r["model"]))
		if !ok || !named {
			continue
		}
		if seen[t.Unix()] == nil {
			seen[t.Unix()] = map[string]bool{}
		}
		seen[t.Unix()][id] = true
	}
	for at, ids := range seen {
		add(s, time.Unix(at, 0), int64(len(ids)))
	}
}

// readPulse reads every series from its source. A source that fails leaves its
// series out and named; none of them fails the others.
func readPulse(ctx context.Context, log reader) (*PulseView, error) {
	root, cancel := context.WithTimeout(context.Background(), lineTimeout)
	defer cancel()
	a := newAxes(nowFn().UTC())
	v := &PulseView{Days: a.days, Hours: a.hours, Tries: []PulseTally{}, Missing: []string{"products", "shares"}}
	miss := func(name string) { v.Missing = append(v.Missing, name) }
	run := func(sql string, args []any) ([]map[string]any, error) { return queryDatastore(root, sql, args...) }

	if !datastoreEnabled() {
		log.Warn("pulse: the warehouse is not connected")
	} else if err := EnsureUsageRollup(ctx); err != nil {
		log.Warn("pulse: the usage rollup is not ready", "err", err)
	} else {
		v.Tokens, v.Orgs = a.series(true), a.series(true)
		if rows, err := run(pulseDaysSQL(a.from, a.from.AddDate(0, 0, pulseDays))); err == nil {
			for _, r := range rows {
				if t, ok := stamp(r["day"]); ok {
					a.day(v.Tokens, t, aInt64(r["total_tokens"]))
					a.day(v.Orgs, t, aInt64(r["orgs"]))
				}
			}
		} else {
			log.Warn("pulse: tokens by day not read", "err", err)
			v.Tokens, v.Orgs = nil, nil
		}
		if v.Tokens != nil {
			if rows, err := run(pulseHoursSQL(a.hour0)); err == nil {
				for _, r := range rows {
					if t, ok := stamp(r["hour"]); ok {
						a.hour(v.Tokens, t, aInt64(r["total_tokens"]))
						a.hour(v.Orgs, t, aInt64(r["orgs"]))
					}
				}
			} else {
				// The days are read; only the hours are not, and that is not a series.
				log.Warn("pulse: tokens by hour not read", "err", err)
				v.Tokens.Hours, v.Orgs.Hours = nil, nil
			}
		}
		v.Variety = a.series(true)
		if rows, err := run(pulseVarietyDaysSQL(a.from, a.from.AddDate(0, 0, pulseDays))); err == nil {
			a.distinct(v.Variety, rows, "day", a.day)
			if rows, err := run(pulseVarietyHoursSQL(a.hour0)); err == nil {
				a.distinct(v.Variety, rows, "hour", a.hour)
			} else {
				// As the tokens: the days are read, only the hours are not.
				log.Warn("pulse: model variety by hour not read", "err", err)
				v.Variety.Hours = nil
			}
		} else {
			log.Warn("pulse: model variety by day not read", "err", err)
			v.Variety = nil
		}
		v.Models = a.series(false)
		if rows, err := run(pulseTriesSQL(a.from)); err == nil {
			byModel := map[string]int64{}
			for _, r := range rows {
				t, ok := stamp(r["day"])
				name := aString(r["model"])
				if !ok || name == "" || bases.MatchString(strings.ToLower(name)) {
					continue
				}
				n := aInt64(r["tries"])
				a.day(v.Models, t, n)
				byModel[name] += n
			}
			for name, n := range byModel {
				v.Tries = append(v.Tries, PulseTally{Name: name, Count: n})
			}
			sort.Slice(v.Tries, func(i, j int) bool {
				if v.Tries[i].Count != v.Tries[j].Count {
					return v.Tries[i].Count > v.Tries[j].Count
				}
				return v.Tries[i].Name < v.Tries[j].Name
			})
			v.Tries = v.Tries[:min(len(v.Tries), triedModels)]
		} else {
			log.Warn("pulse: first model tries not read", "err", err)
			v.Models = nil
		}
	}
	if v.Tokens == nil {
		miss("tokens")
		miss("orgs")
	}
	if v.Models == nil {
		miss("models")
	}
	if v.Variety == nil {
		miss("variety")
	}

	if dir, err := directoryFn(cloud.For(root, authz.AdminOrg)); err == nil {
		v.Signups = a.series(true)
		for _, o := range dir.Orgs {
			if since := began(o.Created); o.Name != "" && !authz.IsHeldOrg(o.Name) && since.Year() < 9999 {
				a.day(v.Signups, since, 1)
				a.hour(v.Signups, since, 1)
			}
		}
	} else {
		miss("signups")
	}

	bctx := cloud.For(root, cloud.Brand())
	if out, err := referralSeriesFn(bctx, &client.ReferralSeriesIn{Since: a.from.Unix()}); err == nil {
		v.Referred = a.series(true)
		for _, at := range out.At {
			t := time.Unix(at, 0)
			a.day(v.Referred, t, 1)
			a.hour(v.Referred, t, 1)
		}
	} else {
		miss("referred")
	}

	v.Available = v.Tokens != nil || v.Signups != nil || v.Referred != nil || v.Models != nil || v.Variety != nil
	slices.Sort(v.Missing)
	return v, nil
}

// Returns how fast the platform is being used and is spreading, as counts by day for
// thirty days and by hour for the last day: tokens used, orgs active, orgs signed up,
// orgs that came through a referral link, orgs using a model for the first time (with
// the models most tried), and how many different models were in use (variety). Every
// series is read from where cloud records it; one that cannot be read is left out and
// named in `missing`, and the ones nothing records today (orgs trying a product,
// shares) are always named there. Counts only: no org, no name. No credential is
// needed to read it.
//
// Example: {}
func (o boardOps) pulse(ctx context.Context, _ *cloud.Unit) (*PulseView, error) {
	noStore(ctx)
	v, err := pulses.get(pulseTTL, func() (*PulseView, error) { return readPulse(ctx, o.s.Log) })
	if err != nil {
		return nil, zip.Errorf(503, "the pulse could not be read")
	}
	return v, nil
}
