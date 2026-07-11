package core

// The fleet ACTIVITY + TIME-SERIES model, shared by the analytics board and the revenue
// board (one implementation, DRY): real per-customer signup + usage folded into a pure
// activity value, and the continuous bucketed spend series both boards render.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/iam"
)

// SeriesPoint is one bucketed point (count OR cents, per the series). T is the bucket
// key (RFC3339 date / "2006-01" month).
type SeriesPoint struct {
	T     string `json:"t"`
	Value int64  `json:"value"`
}

// TxnPoint is one dated usage event (a commerce `withdraw`, in cents).
type TxnPoint struct {
	T     time.Time
	Cents int64
}

// CustActivity is one customer's real analytics input: when they signed up (IAM
// createdTime) and their consumption events (commerce withdraws). Deposits are NOT
// activity (a credit grant is not the customer using the product), so only withdraws
// feed active/retention/churn/usage — the honest "used it" signal.
type CustActivity struct {
	Org        string
	Display    string
	Created    time.Time
	HasCreated bool
	Usage      []TxnPoint
	SpendCents int64
}

// ActiveIn reports whether the customer had a usage event in the given bucket.
func (ca CustActivity) ActiveIn(bucket string, interval string) bool {
	for _, p := range ca.Usage {
		if BucketKeyOf(p.T, interval) == bucket {
			return true
		}
	}
	return false
}

// ActiveSince reports whether the customer had a usage event at or after cut.
func (ca CustActivity) ActiveSince(cut time.Time) bool {
	for _, p := range ca.Usage {
		if !p.T.Before(cut) {
			return true
		}
	}
	return false
}

// FleetActivity reads every org's signup time (already on the org row) + usage ledger,
// folded into the pure activity model. Returns (acts, ok) where ok is false if ANY org's
// ledger read failed (the caller marks the source degraded and flags the ledger-backed
// metrics as not-fully-computed). Fanned out concurrently with a bound.
func FleetActivity(s *cloud.Service[State], ctx context.Context, orgs []iam.Org) ([]CustActivity, bool) {
	acts := make([]CustActivity, len(orgs))
	oks := make([]bool, len(orgs))
	sem := make(chan struct{}, MaxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, o := range orgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o iam.Org) {
			defer wg.Done()
			defer func() { <-sem }()
			ca := CustActivity{Org: o.Name, Display: Display(o.DisplayName, o.Name)}
			if t, err := time.Parse(time.RFC3339, o.CreatedTime); err == nil {
				ca.Created = t.UTC()
				ca.HasCreated = true
			}
			rows, err := s.State.Commerce.Ledger(ctx, o.Name, 2000)
			oks[i] = err == nil
			for _, r := range rows {
				if strings.ToLower(r.Kind) != "withdraw" {
					continue // only consumption is "activity"; deposits are credits
				}
				t, perr := ParseTxnTime(r.At)
				if perr != nil {
					continue
				}
				amt := int64(r.Amount)
				if amt < 0 {
					amt = -amt
				}
				ca.Usage = append(ca.Usage, TxnPoint{T: t, Cents: amt})
				ca.SpendCents += amt
			}
			acts[i] = ca
		}(i, o)
	}
	wg.Wait()
	allOK := true
	for _, ok := range oks {
		if !ok {
			allOK = false
			break
		}
	}
	return acts, allOK
}

// SpendSeries buckets fleet usage cents into a continuous series over since..now. Shared
// by the analytics usage/revenue trend and the revenue board's spend trend (one
// implementation, DRY). A bucket with no usage is an honest 0, not a gap.
func SpendSeries(acts []CustActivity, since, now time.Time, interval string) []SeriesPoint {
	buckets := EnumerateBuckets(since, now, interval)
	idx := IndexOf(buckets)
	out := make([]SeriesPoint, len(buckets))
	for i, b := range buckets {
		out[i] = SeriesPoint{T: b}
	}
	for _, a := range acts {
		for _, p := range a.Usage {
			if p.T.Before(since) || p.T.After(now) {
				continue
			}
			if i, ok := idx[BucketKeyOf(p.T, interval)]; ok {
				out[i].Value += p.Cents
			}
		}
	}
	return out
}

// ── pure time-bucket helpers ─────────────────────────────────────────────────

func MonthKey(t time.Time) string { return t.UTC().Format("2006-01") }
func DayKey(t time.Time) string   { return t.UTC().Format("2006-01-02") }

// WeekKey buckets to the ISO week's Monday (a stable weekly key).
func WeekKey(t time.Time) string {
	u := t.UTC()
	// back up to Monday
	wd := int(u.Weekday())
	if wd == 0 {
		wd = 7
	}
	monday := u.AddDate(0, 0, -(wd - 1))
	return monday.Format("2006-01-02")
}

func BucketKeyOf(t time.Time, interval string) string {
	switch interval {
	case "month":
		return MonthKey(t)
	case "week":
		return WeekKey(t)
	default:
		return DayKey(t)
	}
}

// EnumerateBuckets lists every bucket key from since..now inclusive so a series has a
// continuous axis (a zero-usage bucket is an honest 0, not a gap).
func EnumerateBuckets(since, now time.Time, interval string) []string {
	if since.After(now) {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	step := func(t time.Time) time.Time {
		switch interval {
		case "month":
			return t.AddDate(0, 1, 0)
		case "week":
			return t.AddDate(0, 0, 7)
		default:
			return t.AddDate(0, 0, 1)
		}
	}
	// cap iterations so a bad range can never spin unbounded
	for t, n := since, 0; !t.After(now) && n < 800; t, n = step(t), n+1 {
		k := BucketKeyOf(t, interval)
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	// ensure the final bucket (now) is present
	last := BucketKeyOf(now, interval)
	if !seen[last] {
		out = append(out, last)
	}
	return out
}

// IndexOf maps each bucket key to its position for O(1) fold-in.
func IndexOf(buckets []string) map[string]int {
	m := make(map[string]int, len(buckets))
	for i, b := range buckets {
		m[b] = i
	}
	return m
}

// ParseTxnTime accepts the commerce ledger's RFC3339 forms.
func ParseTxnTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Parse("2006-01-02T15:04:05Z", s)
}
