package coord

import (
	"context"
	"errors"
	"time"
)

// Window is one count a Quota charges: the key it is kept under, how many it admits,
// and how long the key outlives its first charge. A caller names the period in the
// key ("2026-10-03"), so a new period is a new key and nothing has to reset; TTL only
// has to outlast the period the key names.
type Window struct {
	Key   string
	Limit int
	TTL   time.Duration
}

// Quota charges one to every window at once, and only when each is below its limit:
// a window at its limit refuses the call and nothing is charged, so a refusal is not
// usage and one window never spends a unit on a call another refused. It answers
// each window's count after the call, and whether the call was charged.
//
// A KV that cannot be asked does not admit everyone. The count falls back to this
// process's, and on a deployment of several writers each pod admits only its share,
// the limit over the writers declared, so the pods together never admit more than
// the limit. A deployment of one writer is this process, and counts exactly.
func (c *Coord) Quota(ctx context.Context, name string, ws []Window) ([]int, bool) {
	used, _, ok := c.count(ctx, "quota", name, ws, func(limit int) int { return limit / c.declared })
	return used, ok
}

// Ceiling is Quota for a RATE ceiling: every window charged at once, or none, in one
// question to the KV however many windows a request binds, and each window's time
// to its reset as the KV measures it, so a Retry-After never depends on two clocks
// agreeing. It differs from Quota in the one place the two purposes do — what a pod
// admits when the KV cannot be asked. A quota is money, so its share rounds down and
// the pods never admit more than the limit. A rate ceiling exists to keep the API
// up, so its share rounds UP: during an outage the pods admit at most one request a
// window each past the limit, and a ceiling of 2 on three pods never becomes a
// ceiling of 0 that refuses every request because the counter is down.
func (c *Coord) Ceiling(ctx context.Context, name string, ws []Window) ([]int, []time.Duration, bool) {
	return c.count(ctx, "ceiling", name, ws, func(limit int) int { return (limit + c.declared - 1) / c.declared })
}

// count is Quota's and Ceiling's one count. share is what one pod admits of a limit
// while the KV cannot be asked, on a deployment of several writers.
func (c *Coord) count(ctx context.Context, kind, name string, ws []Window, share func(int) int) ([]int, []time.Duration, bool) {
	keys, limits, ttls := make([]string, len(ws)), make([]int, len(ws)), make([]time.Duration, len(ws))
	for i, w := range ws {
		keys[i], limits[i], ttls[i] = c.Key(kind+":"+name, w.Key), w.Limit, w.TTL
	}
	if c.shared {
		used, wait, ok, err := c.b.Quota(ctx, keys, limits, ttls)
		if err == nil {
			return used, wait, ok
		}
		c.log.warn(kind, "coord: the shared count is not answering; this pod admits its share", kind, name, "err", err)
	}
	if c.Peered() {
		for i := range limits {
			limits[i] = share(limits[i])
		}
	}
	used, wait, ok, _ := c.local.Quota(ctx, keys, limits, ttls)
	return used, wait, ok
}

// Unquota gives back one unit Quota charged to each key under name — a call that
// was admitted and then not served — never taking a count below zero. A KV that
// cannot be asked gives it back in this process's count.
func (c *Coord) Unquota(ctx context.Context, name string, keys []string) {
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = c.Key("quota:"+name, k)
	}
	if c.shared {
		err := c.b.Unquota(ctx, full)
		if err == nil {
			return
		}
		c.log.warn("unquota", "coord: the shared count is not answering; giving back in this process's", "quota", name, "err", err)
	}
	_ = c.local.Unquota(ctx, full)
}

// ErrHeld is a lease someone else holds.
var ErrHeld = errors.New("coord: the lease is held")

// Lease is the right to do one thing at a time across every pod: it answers the function
// that takes the lease on key under name for at most ttl. That function answers the one that
// gives the lease back, and the time the holder has left of it, so it can bound its work
// inside it. A holder that dies loses the lease after ttl, so size ttl above the work and
// below the wait a caller can bear. Giving the lease back after ttl has gone does nothing: by then it may be
// another taker's, and lowering the count would let a third in beside them. Time is
// measured from before the lease was asked for, on the monotonic clock, so a holder never
// thinks it has longer than the count does. It is a Quota of one: a second taker is
// ErrHeld, and a KV that cannot be asked on a deployment of several writers gives each pod a
// share of one, which is none, so no pod is granted a lease it cannot be sure is exclusive.
// A deployment of one writer is this process and counts exactly.
func (c *Coord) Lease(name, key string, ttl time.Duration) func(ctx context.Context) (release func(), held time.Duration, err error) {
	return func(ctx context.Context) (func(), time.Duration, error) {
		start := time.Now()
		if _, ok := c.Quota(ctx, name, []Window{{Key: key, Limit: 1, TTL: ttl}}); !ok {
			return nil, 0, ErrHeld
		}
		// What is left of the lease, counted from before the store was asked: a slow answer
		// takes its delay out of the holder's time. None left is a lease already over.
		left := ttl - time.Since(start)
		if left <= 0 {
			return nil, 0, ErrHeld
		}
		return func() {
			if time.Since(start) >= ttl {
				return
			}
			c.Unquota(context.WithoutCancel(ctx), name, []string{key})
		}, left, nil
	}
}

// Counted is what each key under name has been charged by Quota, read without
// charging. A KV that cannot be asked answers this process's counts.
func (c *Coord) Counted(ctx context.Context, name string, keys []string) []int {
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = c.Key("quota:"+name, k)
	}
	if c.shared {
		used, err := c.b.Counted(ctx, full)
		if err == nil {
			return used
		}
		c.log.warn("counted", "coord: the shared count is not answering; reading this process's", "quota", name, "err", err)
	}
	used, _ := c.local.Counted(ctx, full)
	return used
}

// quota is Quota's count in this process: one slot per key, gone when its TTL is.
// Expired slots are swept at most once a minute, so the map holds the keys of the
// periods still running and a sweep never runs per call.
func (b *mem) Quota(_ context.Context, keys []string, limits []int, ttls []time.Duration) ([]int, []time.Duration, bool, error) {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Sub(b.quotaSwept) >= time.Minute {
		b.quotaSwept = now
		for k, s := range b.quotas {
			if !now.Before(s.reset) {
				delete(b.quotas, k)
			}
		}
	}
	used := make([]int, len(keys))
	room := true
	for i, k := range keys {
		if s := b.quotas[k]; s != nil && now.Before(s.reset) {
			used[i] = s.n
		}
		if used[i] >= limits[i] {
			room = false
		}
	}
	if room {
		for i, k := range keys {
			s := b.quotas[k]
			if s == nil || !now.Before(s.reset) {
				s = &slot{reset: now.Add(ttls[i])}
				b.quotas[k] = s
			}
			s.n++
			used[i] = s.n
		}
	}
	wait := make([]time.Duration, len(keys))
	for i, k := range keys {
		wait[i] = ttls[i]
		if s := b.quotas[k]; s != nil && now.Before(s.reset) {
			wait[i] = s.reset.Sub(now)
		}
	}
	return used, wait, room, nil
}

func (b *mem) Unquota(_ context.Context, keys []string) error {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, k := range keys {
		if s := b.quotas[k]; s != nil && now.Before(s.reset) && s.n > 0 {
			s.n--
		}
	}
	return nil
}

func (b *mem) Counted(_ context.Context, keys []string) ([]int, error) {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	used := make([]int, len(keys))
	for i, k := range keys {
		if s := b.quotas[k]; s != nil && now.Before(s.reset) {
			used[i] = s.n
		}
	}
	return used, nil
}
