package websearch

// cache.go — the same query does not scrape the same engine twice.
//
// WHY THIS IS CORRECTNESS AND NOT SPEED. Every engine here is a public search
// page fetched over HTTP, and every one of them rate-limits. When lite.duckduckgo
// is served a challenge instead of results, parseDDG finds nothing and
// fetchEngine returns ZERO RESULTS WITHOUT AN ERROR — by design, so one unhappy
// engine cannot fail a request that another engine can answer. The cost of that
// design is that a rate-limited engine is indistinguishable from a disabled one:
// both are silently absent.
//
// Measured before this existed, four queries run back to back through the real
// engines (bing then ddg, ~2.4s total):
//
//	post quantum cryptography lattice   bing 10   ddg 10
//	firecracker microvm kvm setup       bing 10   ddg  0   <- challenged
//	gvisor runsc syscall interception   bing 10   ddg  0   <- challenged
//	rust tokio select cancellation      bing 10   ddg 10
//
// DDG answered the first request and then stopped answering. Nothing was broken;
// it was simply asked four times in three seconds. A cache removes the repeat ask
// entirely, which is the only fix that does not involve asking someone else's
// server more nicely and hoping.
//
// SO: A HIT IS NEVER STORED WHEN IT IS EMPTY. Caching a challenge page's zero
// results would pin the failure for the whole TTL and make the engine look
// permanently dead — the exact defect this file exists to end. Only a non-empty
// answer is worth remembering.
//
// IN-PROCESS, BOUNDED, NO DEPENDENCY. Not Redis and not a datastore: a search
// result is derived, public, and cheap to re-fetch, so the correct home for it is
// the memory of the process that asked. Bounded by count with the oldest entry
// evicted, so a long-running host cannot grow one query at a time.

import (
	"os"
	"strings"
	"sync"
	"time"
)

// cacheTTL is how long an engine's answer for a query stands. Long enough that a
// person refining a question ("...lattice" then "...lattice kyber") does not
// re-ask the parts that overlap, short enough that the web is allowed to change
// within a session. WEBSEARCH_CACHE_TTL overrides; 0 disables the cache.
func cacheTTL() time.Duration {
	if v := strings.TrimSpace(os.Getenv("WEBSEARCH_CACHE_TTL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 15 * time.Minute
}

// cacheMax bounds the entry count. Each entry is one engine's page of results
// for one query, so a few thousand is small; the bound exists so an adversarial
// query stream cannot grow the process without limit.
const cacheMax = 2048

type cacheEntry struct {
	results []webResult
	stored  time.Time
}

var (
	cacheMu sync.Mutex
	cached  = map[string]cacheEntry{}
)

// cacheKey names one engine's answer to one question, and the name is the
// REQUEST URL — the endpoint plus the encoded query — not the engine's label.
//
// Keyed on the label instead, the cache answers for a request it never made.
// Every engine endpoint here is an env override (WEBSEARCH_BING_URL,
// WEBSEARCH_DDG_URL), so "bing" is not one address; it is whichever address is
// configured right now. Three tests proved it before this was written: pointed at
// a stub server, they got the PREVIOUS caller's real results and reported that
// the engine had not been reached, that a failing engine had returned rows, and
// that a page with no sources had sources.
//
// The request URL is the honest identity of a question. A different endpoint is a
// different question, in production exactly as in a test.
func cacheKey(requestURL string) string { return requestURL }

// cacheGet returns a stored non-empty answer that has not expired.
func cacheGet(requestURL string) ([]webResult, bool) {
	ttl := cacheTTL()
	if ttl <= 0 {
		return nil, false
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	e, ok := cached[cacheKey(requestURL)]
	if !ok || time.Since(e.stored) > ttl {
		return nil, false
	}
	return e.results, true
}

// cachePut remembers a non-empty answer. An EMPTY answer is never stored — see
// the file comment: an engine that was challenged must be allowed to answer the
// next time it is asked.
func cachePut(requestURL string, results []webResult) {
	if len(results) == 0 || cacheTTL() <= 0 {
		return
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if len(cached) >= cacheMax {
		evictOldest()
	}
	cached[cacheKey(requestURL)] = cacheEntry{results: results, stored: time.Now()}
}

// evictOldest drops the least recently stored entry. Called with cacheMu held.
// A full scan is right at this size and has no bookkeeping to go wrong; if the
// bound ever grows by an order of magnitude this becomes a heap, not a rewrite.
func evictOldest() {
	var oldestKey string
	var oldest time.Time
	for k, v := range cached {
		if oldestKey == "" || v.stored.Before(oldest) {
			oldestKey, oldest = k, v.stored
		}
	}
	delete(cached, oldestKey)
}

// cacheSize is for tests and for the one log line that says whether the cache is
// doing anything.
func cacheSize() int {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	return len(cached)
}

// cacheReset empties the cache. Tests only — production has no reason to forget.
func cacheReset() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cached = map[string]cacheEntry{}
}
