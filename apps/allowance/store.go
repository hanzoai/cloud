// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package allowance

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
)

// Store counts what each subject has taken, per window, on the KV every pod of the
// deployment shares (sdk/coord Quota), so a subject spread over pods is held to
// one count. With no KV the count is this process's, which is the whole deployment
// when it is one pod; a KV that stops answering holds each pod to its share.
//
// THE PERIOD IS THE KEY, WHICH IS WHY NOTHING RESETS ANYTHING. A count is kept under
// the window, the period it belongs to and the subject ("day:2026-10-03:<subject>"),
// so tomorrow's calls land on a key nobody has charged and yesterday's expires on its
// own. There is no scheduler, no sweep, and no window during which a job has not run
// yet. A session's period is its start in unix seconds ("session:1793652000:<subject>"),
// which the KV keeps per subject (coord.Span) because only the call that opened the
// session knew it.
//
// Isolation is the subject in the key. The value is the person or wallet the gate
// resolved from a verified credential, never a client-supplied header, so one caller
// can neither read nor spend another's allowance.
type Store struct{ c *coord.Coord }

// openStore is the store over this process's Coord.
func openStore() *Store { return &Store{c: coord.Default()} }

// quotaName names the allowance's counts among the deployment's quotas.
const quotaName = "allowance"

// keep is how long a key outlives the end of the period it names, so a pod whose
// clock runs a little behind still finds the count it is adding to.
const keep = time.Hour

// key is where one window of subject's count is kept.
func key(subject string, b bound) string { return b.row + ":" + b.period + ":" + subject }

// keys is where each window of bs keeps subject's count.
func keys(subject string, bs []bound) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = key(subject, b)
	}
	return out
}

// Take admits ONE unit against every window in bs at once, counting it as it is
// admitted, and answers each window's count afterwards and whether it was admitted.
//
// EVERY WINDOW IS ASKED BEFORE ANY IS CHARGED, in one step on the KV: a window at its
// ceiling refuses the unit and no window is charged for it. So a refusal is not usage,
// a caller at their daily ceiling never burns an hourly slot by being turned away, and
// two calls arriving together cannot both take the last unit.
//
// A SESSION OPENS AT THE CALL IT ADMITS, as a plan's session does (apps/ai/limits).
// Take places each session window of bs, in place, at the session running at now,
// or, when none runs, at a session this call would open at its own instant; the
// caller reads the window's reset from it. A call that is refused opens nothing.
// Calls that arrive together while none runs each count under their own instant and
// then open: the first to open holds the session, and a call that lost moves its
// unit into that one, held to the same ceiling there. So however many arrive at
// once, the session that opens admits no more than its limit.
func (s *Store) Take(ctx context.Context, subject string, bs []bound, now time.Time) ([]int64, bool) {
	s.place(ctx, subject, bs, now, true)
	used, ok := s.quota(ctx, subject, bs, now)
	if !ok {
		return used, ok
	}
	was := slices.Clone(bs)
	moved := false
	for i := range bs {
		b := &bs[i]
		if !b.opens {
			continue
		}
		b.opens = false
		if start := s.c.Span(ctx, quotaName, spanKey(subject, *b), now, b.span); stamp(start) != b.period {
			b.at(start)
			moved = true
		}
	}
	if !moved {
		return used, ok
	}
	s.c.Unquota(ctx, quotaName, keys(subject, was))
	return s.quota(ctx, subject, bs, now)
}

// quota counts one unit against every window of bs, or none.
func (s *Store) quota(ctx context.Context, subject string, bs []bound, now time.Time) ([]int64, bool) {
	ws := make([]coord.Window, len(bs))
	for i, b := range bs {
		ws[i] = coord.Window{Key: key(subject, b), Limit: int(b.limit), TTL: b.resets.Sub(now) + keep}
	}
	used, ok := s.c.Quota(ctx, quotaName, ws)
	return wide(used, len(bs)), ok
}

// Give hands back the one unit Take counted against every window in bs: the call it
// admitted was not served. The calendar windows are the ones at at; a session is the
// one that opened at start — the session the take answered (client.Allowance Start)
// — and no other, so a unit taken in a session that has since ended never lands in
// the next one. A start of 0 gives nothing back to a session.
func (s *Store) Give(ctx context.Context, subject string, bs []bound, start int64) {
	for i := range bs {
		if b := &bs[i]; b.span > 0 {
			b.period, b.resets = "", time.Time{}
			if start > 0 {
				b.at(time.Unix(start, 0))
			}
		}
	}
	s.c.Unquota(ctx, quotaName, keys(subject, bs))
}

// Read answers what subject has taken in each window of bs at now, without taking
// anything. A session that is not running has taken nothing and no reset.
func (s *Store) Read(ctx context.Context, subject string, bs []bound, now time.Time) []int64 {
	s.place(ctx, subject, bs, now, false)
	return wide(s.c.Counted(ctx, quotaName, keys(subject, bs)), len(bs))
}

// place sets each session window of bs at the session running at now. When none
// runs, open places it at a session opening at now (bound.opens); otherwise it has
// no period, so it counts nothing, and no reset. A calendar window is already placed.
func (s *Store) place(ctx context.Context, subject string, bs []bound, now time.Time, open bool) {
	for i := range bs {
		b := &bs[i]
		if b.span <= 0 {
			continue
		}
		start := s.c.Spanned(ctx, quotaName, spanKey(subject, *b), now, b.span)
		switch {
		case !start.IsZero():
			b.at(start)
		case open:
			b.at(now)
			b.opens = true
		default:
			b.period, b.resets = "", time.Time{}
		}
	}
}

// spanKey is where subject's session on b's row keeps its start.
func spanKey(subject string, b bound) string { return b.row + ":" + subject }

// at places a session window at the session starting at start, in whole seconds: its
// period is that start and it resets one span later.
func (b *bound) at(start time.Time) {
	start = time.Unix(start.Unix(), 0).UTC()
	b.period, b.resets = stamp(start), start.Add(b.span)
}

// opened is when the session of bs a call counts in opened, unix seconds: 0 where
// no session bounds the subject, none runs, or one was only proposed (bound.opens)
// by a call that was then refused.
func opened(bs []bound) int64 {
	for _, b := range bs {
		if b.span > 0 && b.period != "" && !b.opens {
			return b.resets.Add(-b.span).Unix()
		}
	}
	return 0
}

// stamp is a session's period: its start in unix seconds.
func stamp(start time.Time) string { return strconv.FormatInt(start.Unix(), 10) }

// wide is used as the int64 counts the wire carries, n of them.
func wide(used []int, n int) []int64 {
	out := make([]int64, n)
	for i := 0; i < n && i < len(used); i++ {
		out[i] = int64(used[i])
	}
	return out
}

// Day is the period a count belongs to: the UTC calendar day. One rule, one
// timezone — a period that moved with the caller would let a traveller take two
// days' worth in one afternoon.
func Day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// Hour is the period an hourly count belongs to: the UTC calendar hour. Same one
// rule and one timezone as Day, for the same reason — a window that moved with the
// caller would let a traveller take two of them.
func Hour(t time.Time) string { return t.UTC().Format("2006-01-02T15") }

// NextHour is when an hourly count starts again: the top of the next UTC hour.
func NextHour(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), 0, 0, 0, time.UTC).Add(time.Hour)
}

// Midnight is when the current period ends and counting starts again: the first
// instant of the next UTC day. It is what the product shows as "resets at".
func Midnight(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
}
