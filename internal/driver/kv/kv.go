package kv

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	kvc "github.com/hanzokv/go/v9"

	"github.com/hanzoai/cloud/sdk/coord"
)

// callTimeout bounds one question to the KV.
const callTimeout = 300 * time.Millisecond

// kv answers across every pod that reaches the same Hanzo KV. Everything that must
// be atomic is one script, and every expiry is read from the server's own clock
// (TIME), so two pods whose clocks disagree still agree on who is alive.
type kv struct {
	c *kvc.Client
	// openFor is how long the breaker stays open after the KV fails to answer
	// (Periods.Open): every question in that time is answered at once without asking
	// it, so a KV that hangs costs the deployment one timeout per openFor, not one
	// per request.
	openFor time.Duration

	mu       sync.Mutex
	openTill time.Time // the breaker is open until then
	probing  bool      // one question is finding out whether the KV is back

	bus kvBus // the pod's subscriptions (bus.go)
}

// A process that links this package answers across pods when KV_URL names a KV.
func init() {
	coord.Register(coord.Driver{Backend: func(url string, p coord.Periods) (coord.Backend, error) {
		return newKV(url, p.OpenFor())
	}})
}

func newKV(url string, openFor time.Duration) (*kv, error) {
	opt, err := kvc.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("coord: KV_URL: %w", err)
	}
	opt.DialTimeout = callTimeout
	opt.ReadTimeout = callTimeout
	opt.WriteTimeout = callTimeout
	opt.PoolTimeout = callTimeout
	opt.MaxRetries = -1
	return &kv{c: kvc.NewClient(opt), openFor: openFor}, nil
}

// ask runs one question through the breaker. While it is open every question is
// refused at once without asking; when it has been open for openFor, ONE question
// asks, and the rest are refused until that one is answered. So a KV that hangs
// costs one caller one timeout per openFor, and every other caller nothing. A
// refusal the KV itself gives (a script error, NOREPLICAS) is an answer and leaves
// the breaker shut, and so does a caller that went away before the answer came: its
// request ended, which says nothing about the KV, and opening on it would send every
// other caller to this process's counts for openFor.
func (b *kv) ask(caller context.Context, q func(context.Context) error) error {
	b.mu.Lock()
	switch {
	case b.openTill.IsZero():
	case b.probing || time.Now().Before(b.openTill):
		b.mu.Unlock()
		return coord.ErrUnavailable
	default:
		b.probing = true
	}
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(caller, callTimeout)
	defer cancel()
	err := q(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	if err != nil && caller.Err() != nil {
		return err
	}
	if err != nil && unanswered(err) {
		b.openTill = time.Now().Add(b.openFor)
		return fmt.Errorf("%w: %v", coord.ErrUnavailable, err)
	}
	b.openTill = time.Time{}
	return err
}

// unanswered reports whether err means the KV did not answer at all.
func unanswered(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var kverr kvc.Error
	if errors.As(err, &kverr) {
		return false // the server answered, with a refusal
	}
	return !errors.Is(err, kvc.Nil)
}

// A live member is a score in the set (its expiry, in server milliseconds) and an
// entry in each hash: where it is, and which signed instance put it there. Joining
// sweeps the expired first, so a pod that died is gone from all three without
// anyone having to notice. Another instance under the id is answered, not obeyed:
// the script hands back what it holds and writes nothing, and the joiner decides
// in Go whether that is a live pod (ARGV[5] = "force" overwrites it).
var joinScript = kvc.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local dead = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now)
for _, id in ipairs(dead) do
  redis.call('HDEL', KEYS[2], id)
  redis.call('HDEL', KEYS[3], id)
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
if ARGV[5] ~= 'force' and redis.call('ZSCORE', KEYS[1], ARGV[1]) then
  local held = redis.call('HGET', KEYS[3], ARGV[1])
  if held and held ~= ARGV[3] then
    return {0, redis.call('HGET', KEYS[2], ARGV[1]) or '', held}
  end
end
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[4]), ARGV[1])
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
redis.call('HSET', KEYS[3], ARGV[1], ARGV[3])
return {1}
`)

// Reading the set writes nothing, so it answers on a KV that refuses writes
// (NOREPLICAS). It returns only members whose expiry lies inside one TTL of now: an
// entry that claims to live longer was written by something other than a pod.
var membersScript = kvc.NewScript(`#!lua flags=no-writes
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local ids = redis.call('ZRANGEBYSCORE', KEYS[1], '(' .. now, now + tonumber(ARGV[1]))
local out = {}
for _, id in ipairs(ids) do
  local a = redis.call('HGET', KEYS[2], id)
  local i = redis.call('HGET', KEYS[3], id)
  if a and i then
    table.insert(out, id)
    table.insert(out, a)
    table.insert(out, i)
  end
end
return out
`)

var leaveScript = kvc.NewScript(`
if redis.call('HGET', KEYS[3], ARGV[1]) == ARGV[2] then
  redis.call('ZREM', KEYS[1], ARGV[1])
  redis.call('HDEL', KEYS[2], ARGV[1])
  redis.call('HDEL', KEYS[3], ARGV[1])
end
return 1
`)

// The fixed window mem.spend counts, over keys every pod shares: narrowest first,
// a refused request still charged to the key that refused it, and a wider key
// charged only for what the narrower admitted.
var spendScript = kvc.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local limit = tonumber(ARGV[1])
local period = tonumber(ARGV[2])
local left = nil
local tight = 0
local reset = 0
for i, k in ipairs(KEYS) do
  local refused = (left ~= nil and left < 0)
  local n = tonumber(redis.call('GET', k) or '0')
  local ttl = redis.call('PTTL', k)
  local fresh = (ttl <= 0)
  if not (fresh and refused) then
    if fresh then
      n = 0
      ttl = period
    end
    local room = limit * tonumber(ARGV[2 + i]) - n
    local r = now + ttl
    if not refused then
      if fresh then
        redis.call('SET', k, 1, 'PX', period)
      else
        redis.call('INCR', k)
      end
      room = room - 1
      if left == nil or room < left then
        left = room
        tight = r
      end
    end
    if room <= 0 and r > reset then reset = r end
  end
end
if reset == 0 then reset = tight end
if left == nil then left = 9007199254740991 end
return {left, reset}
`)

// The token bucket mem.take keeps, every pod sharing it: the time the next token is
// due, admitted when no more than a full bucket ahead of now.
var takeScript = kvc.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local interval = tonumber(ARGV[1])
local tat = tonumber(redis.call('GET', KEYS[1]) or '0')
if tat < now then tat = now end
local nxt = tat + interval
if nxt - now > 60000 then return 0 end
redis.call('SET', KEYS[1], nxt, 'PX', nxt - now)
return 1
`)

func (b *kv) Join(ctx context.Context, set string, r coord.Record, ttl time.Duration, force bool) (*coord.Record, error) {
	mode := ""
	if force {
		mode = "force"
	}
	var out []any
	err := b.ask(ctx, func(ctx context.Context) error {
		var err error
		out, err = joinScript.Run(ctx, b.c, []string{set, set + ":addr", set + ":instance"},
			r.ID, r.Addr+"|"+strconv.FormatInt(r.At, 10)+"|"+r.Sig, r.Instance, ttl.Milliseconds(), mode).Slice()
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 3 {
		addr, _ := out[1].(string)
		inst, _ := out[2].(string)
		held := parseRecord(r.ID, addr, inst)
		return &held, nil
	}
	return nil, nil
}

// parseRecord reads one member back from its two hash fields. A field no pod wrote
// parses to a record with no signature, which verifies as nobody.
func parseRecord(id, addr, instance string) coord.Record {
	parts := strings.SplitN(addr, "|", 3)
	if len(parts) != 3 {
		return coord.Record{ID: id, Instance: instance}
	}
	at, _ := strconv.ParseInt(parts[1], 10, 64)
	return coord.Record{ID: id, Addr: parts[0], At: at, Sig: parts[2], Instance: instance}
}

func (b *kv) Leave(ctx context.Context, set, id, instance string) error {
	return b.ask(ctx, func(ctx context.Context) error {
		return leaveScript.Run(ctx, b.c, []string{set, set + ":addr", set + ":instance"}, id, instance).Err()
	})
}

func (b *kv) Members(ctx context.Context, set string, ttl time.Duration) ([]coord.Record, error) {
	var flat []string
	err := b.ask(ctx, func(ctx context.Context) error {
		var err error
		flat, err = membersScript.Run(ctx, b.c, []string{set, set + ":addr", set + ":instance"}, ttl.Milliseconds()).StringSlice()
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]coord.Record, 0, len(flat)/3)
	for i := 0; i+2 < len(flat); i += 3 {
		out = append(out, parseRecord(flat[i], flat[i+1], flat[i+2]))
	}
	return out, nil
}

func (b *kv) Spend(ctx context.Context, keys []string, scales []int, limit int, period time.Duration) (int, time.Time, error) {
	args := make([]any, 0, 2+len(scales))
	args = append(args, limit, period.Milliseconds())
	for _, s := range scales {
		args = append(args, s)
	}
	var res []int64
	err := b.ask(ctx, func(ctx context.Context) error {
		var err error
		res, err = spendScript.Run(ctx, b.c, keys, args...).Int64Slice()
		return err
	})
	if err != nil {
		return 0, time.Time{}, err
	}
	if len(res) != 2 {
		return 0, time.Time{}, fmt.Errorf("coord: spend answered %d values", len(res))
	}
	return int(res[0]), time.UnixMilli(res[1]), nil
}

func (b *kv) Take(ctx context.Context, key string, perMin int) (bool, error) {
	if perMin <= 0 {
		return true, nil
	}
	var n int64
	err := b.ask(ctx, func(ctx context.Context) error {
		var err error
		n, err = takeScript.Run(ctx, b.c, []string{key}, (time.Minute / time.Duration(perMin)).Milliseconds()).Int64()
		return err
	})
	return n == 1, err
}

func (b *kv) Once(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	var first bool
	err := b.ask(ctx, func(ctx context.Context) error {
		var err error
		first, err = b.c.SetNX(ctx, key, 1, ttl).Result()
		return err
	})
	return first, err
}

// A key's holds are a sorted set whose members are "<id>:<cents>" scored by the server
// millisecond they lapse. Committing sweeps the lapsed, adds this one and answers the sum
// of the rest; the key itself lapses with its last hold. Every expiry is the server's
// clock, so pods whose clocks disagree still agree on which holds are live.
var commitScript = kvc.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[2]), ARGV[1])
local last = redis.call('ZRANGE', KEYS[1], -1, -1, 'WITHSCORES')
redis.call('PEXPIRE', KEYS[1], math.max(tonumber(last[2]) - now, 1))
local sum = 0
for _, m in ipairs(redis.call('ZRANGE', KEYS[1], 0, -1)) do
  sum = sum + tonumber(string.match(m, ':(-?%d+)$'))
end
return sum
`)

var committedScript = kvc.NewScript(`#!lua flags=no-writes
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local sum = 0
for _, m in ipairs(redis.call('ZRANGEBYSCORE', KEYS[1], '(' .. now, '+inf')) do
  sum = sum + tonumber(string.match(m, ':(-?%d+)$'))
end
return sum
`)

func holdMember(id string, cents int64) string { return id + ":" + strconv.FormatInt(cents, 10) }

func (b *kv) Commit(ctx context.Context, key, id string, cents int64, ttl time.Duration) (int64, error) {
	var total int64
	err := b.ask(ctx, func(ctx context.Context) error {
		var err error
		total, err = commitScript.Run(ctx, b.c, []string{key}, holdMember(id, cents), max(ttl.Milliseconds(), 1)).Int64()
		return err
	})
	return total, err
}

func (b *kv) Uncommit(ctx context.Context, key, id string, cents int64) error {
	return b.ask(ctx, func(ctx context.Context) error {
		return b.c.ZRem(ctx, key, holdMember(id, cents)).Err()
	})
}

func (b *kv) Committed(ctx context.Context, key string) (int64, error) {
	var total int64
	err := b.ask(ctx, func(ctx context.Context) error {
		var err error
		total, err = committedScript.Run(ctx, b.c, []string{key}).Int64()
		return err
	})
	return total, err
}

// Every window is read before any is charged, so the call is charged to all of them
// or to none. A key's expiry is set by any charge that finds it without one — the
// charge that creates it, and one that finds a count whose expiry was lost — so no
// count outlives its window and holds a caller refused for good. It answers each
// window's count, then each one's time to its reset (PTTL; a key not yet charged has
// its whole TTL ahead of it), then whether the call was charged.
var quotaScript = kvc.NewScript(`
local n = #KEYS
local out = {}
local room = true
for i = 1, n do
  out[i] = tonumber(redis.call('GET', KEYS[i]) or '0')
  if out[i] >= tonumber(ARGV[2 * i - 1]) then room = false end
end
if room then
  for i = 1, n do
    out[i] = redis.call('INCR', KEYS[i])
    if redis.call('PTTL', KEYS[i]) < 0 then redis.call('PEXPIRE', KEYS[i], ARGV[2 * i]) end
  end
end
for i = 1, n do
  local ttl = redis.call('PTTL', KEYS[i])
  if ttl < 0 then ttl = tonumber(ARGV[2 * i]) end
  out[n + i] = ttl
end
out[2 * n + 1] = room and 1 or 0
return out
`)

func (b *kv) Quota(ctx context.Context, keys []string, limits []int, ttls []time.Duration) ([]int, []time.Duration, bool, error) {
	args := make([]any, 0, 2*len(keys))
	for i := range keys {
		args = append(args, limits[i], max(ttls[i].Milliseconds(), 1))
	}
	var res []int64
	err := b.ask(ctx, func(ctx context.Context) error {
		var err error
		res, err = quotaScript.Run(ctx, b.c, keys, args...).Int64Slice()
		return err
	})
	if err != nil {
		return nil, nil, false, err
	}
	n := len(keys)
	if len(res) != 2*n+1 {
		return nil, nil, false, fmt.Errorf("coord: quota answered %d values for %d windows", len(res), n)
	}
	used, wait := make([]int, n), make([]time.Duration, n)
	for i := range keys {
		used[i] = int(res[i])
		wait[i] = time.Duration(res[n+i]) * time.Millisecond
	}
	return used, wait, res[2*n] == 1, nil
}

// unquotaScript takes one from each key still above zero, keeping its expiry.
var unquotaScript = kvc.NewScript(`
for i = 1, #KEYS do
  if tonumber(redis.call('GET', KEYS[i]) or '0') > 0 then redis.call('DECR', KEYS[i]) end
end
return 1
`)

func (b *kv) Unquota(ctx context.Context, keys []string) error {
	return b.ask(ctx, func(ctx context.Context) error {
		return unquotaScript.Run(ctx, b.c, keys).Err()
	})
}

func (b *kv) Counted(ctx context.Context, keys []string) ([]int, error) {
	var vals []any
	err := b.ask(ctx, func(ctx context.Context) error {
		var err error
		vals, err = b.c.MGet(ctx, keys...).Result()
		return err
	})
	if err != nil {
		return nil, err
	}
	used := make([]int, len(keys))
	for i, v := range vals {
		if s, ok := v.(string); ok {
			used[i], _ = strconv.Atoi(s)
		}
	}
	return used, nil
}

// spanScript answers the span under KEYS[1] running at ARGV[1] (unix seconds), one
// lasting ARGV[2] seconds: its start, or 0 when none runs. ARGV[3] "open" opens one
// at ARGV[1] when none runs, its record kept ARGV[4] milliseconds.
var spanScript = kvc.NewScript(`
local s = tonumber(redis.call('GET', KEYS[1]) or '0')
local now = tonumber(ARGV[1])
if s > 0 and now < s + tonumber(ARGV[2]) then return s end
if ARGV[3] ~= 'open' then return 0 end
redis.call('SET', KEYS[1], now, 'PX', ARGV[4])
return now
`)

func (b *kv) Span(ctx context.Context, key string, now, length int64, open bool) (int64, error) {
	mode := ""
	if open {
		mode = "open"
	}
	keep := (time.Duration(length)*time.Second + coord.SpanKeep).Milliseconds()
	var start int64
	err := b.ask(ctx, func(ctx context.Context) error {
		var err error
		start, err = spanScript.Run(ctx, b.c, []string{key}, now, length, mode, keep).Int64()
		return err
	})
	return start, err
}
