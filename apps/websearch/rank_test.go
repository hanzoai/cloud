package websearch

import (
	"testing"
	"time"
)

// r is a result at a URL, with an optional snippet.
func r(url, content string) webResult {
	return webResult{URL: url, Title: url, Content: content}
}

// TestAgreementOutranksEngineOrder is the defect this ranking exists to fix, in
// the shape it actually occurred: the first-named engine returned three
// irrelevant hits for "post quantum cryptography lattice" (it matched the word
// "post") and the second returned the right ones. Under engine-order merging the
// user's page opened with the wrong three.
func TestAgreementOutranksEngineOrder(t *testing.T) {
	bing := []webResult{r("https://post.ca.gov/Training", ""), r("https://post.ca.gov/post-profile", ""), r("https://blog.cloudflare.com/lattice-crypto-primer/", "")}
	ddg := []webResult{r("https://blog.cloudflare.com/lattice-crypto-primer/", "lattice primer"), r("https://www.redhat.com/pqc", "")}

	got := rankMerged([][]webResult{bing, ddg}, 10)
	if len(got) == 0 {
		t.Fatal("no results")
	}
	// Both engines returned the cloudflare URL; nothing else was agreed on. It
	// leads, even though it was bing's THIRD hit and bing was named first.
	if got[0].URL != "https://blog.cloudflare.com/lattice-crypto-primer/" {
		t.Fatalf("agreed-on result did not lead: %s", got[0].URL)
	}
	// And the richer copy survived the dedupe — the engine with a snippet wins
	// the row, whichever engine found the URL first.
	if got[0].Content != "lattice primer" {
		t.Fatalf("the richer copy was dropped: %q", got[0].Content)
	}
}

// TestRankIsTotalAndStable pins determinism. A search page that reorders itself
// between identical requests cannot be debugged, and map iteration is random.
func TestRankIsTotalAndStable(t *testing.T) {
	a := []webResult{r("https://a.example/1", ""), r("https://b.example/2", ""), r("https://c.example/3", "")}
	b := []webResult{r("https://c.example/3", ""), r("https://d.example/4", "")}

	first := rankMerged([][]webResult{a, b}, 10)
	for i := 0; i < 25; i++ {
		again := rankMerged([][]webResult{a, b}, 10)
		if len(again) != len(first) {
			t.Fatalf("length moved: %d then %d", len(first), len(again))
		}
		for j := range first {
			if first[j].URL != again[j].URL {
				t.Fatalf("order moved at %d: %s then %s", j, first[j].URL, again[j].URL)
			}
		}
	}
}

// TestChallengedEngineContributesNothing — a nil entry is an engine that failed
// or was served a challenge. It must not shift the others or produce empty rows.
func TestChallengedEngineContributesNothing(t *testing.T) {
	live := []webResult{r("https://a.example/1", ""), r("https://b.example/2", "")}
	got := rankMerged([][]webResult{nil, live, nil}, 10)
	if len(got) != 2 || got[0].URL != "https://a.example/1" {
		t.Fatalf("a challenged engine changed the answer: %+v", got)
	}
}

// TestRankHonoursTheCap — the merge cap is the page size, not a suggestion.
func TestRankHonoursTheCap(t *testing.T) {
	var many []webResult
	for i := 0; i < 50; i++ {
		many = append(many, r("https://e.example/"+string(rune('a'+i%26))+string(rune('0'+i/26)), ""))
	}
	if got := rankMerged([][]webResult{many}, 20); len(got) != 20 {
		t.Fatalf("cap not honoured: %d", len(got))
	}
}

// ── the cache ────────────────────────────────────────────────────────────────

// TestCacheNeverStoresAnEmptyAnswer is the whole reason the cache is safe to put
// in front of a rate-limited engine. Caching a challenge page's zero results
// would pin the failure for the TTL and make the engine look permanently dead.
func TestCacheNeverStoresAnEmptyAnswer(t *testing.T) {
	cacheReset()
	cachePut("https://ddg.test/?q=q", nil)
	if _, ok := cacheGet("https://ddg.test/?q=q"); ok {
		t.Fatal("an empty answer was cached — a challenged engine would stay dead for the whole TTL")
	}
	cachePut("https://ddg.test/?q=q", []webResult{r("https://x.example/1", "")})
	if _, ok := cacheGet("https://ddg.test/?q=q"); !ok {
		t.Fatal("a real answer was not cached")
	}
}

// TestCacheKeyedByRequestURL is the defect that broke three existing tests before
// this key was corrected. Keyed on the engine LABEL, the cache answered for a
// request it never made: every endpoint here is an env override, so "bing" is not
// one address but whichever address is configured right now. Pointed at a stub,
// TestSearchValidatedPrincipalBypassesKey reported the engine was never reached,
// TestMetaSearchDegradesOnEngineFailure got rows from a failing engine, and
// answer's TestSSEEmptySourcesStillTerminates found sources it had removed.
func TestCacheKeyedByRequestURL(t *testing.T) {
	cacheReset()
	cachePut("https://bing.test/search?q=clojure", []webResult{r("https://bing.example/", "")})

	// Same engine, same words, DIFFERENT endpoint — a different question.
	if _, ok := cacheGet("https://stub.test/search?q=clojure"); ok {
		t.Fatal("a stubbed endpoint read the real endpoint's answer")
	}
	// Same endpoint, different query.
	if _, ok := cacheGet("https://bing.test/search?q=rust"); ok {
		t.Fatal("one query read another query's answer")
	}
	// The identical request hits.
	if _, ok := cacheGet("https://bing.test/search?q=clojure"); !ok {
		t.Fatal("the same request did not hit")
	}
}

// TestCacheExpires — the web is allowed to change.
func TestCacheExpires(t *testing.T) {
	cacheReset()
	t.Setenv("WEBSEARCH_CACHE_TTL", "1ns")
	cachePut("https://bing.test/?q=q", []webResult{r("https://x.example/1", "")})
	time.Sleep(2 * time.Millisecond)
	if _, ok := cacheGet("https://bing.test/?q=q"); ok {
		t.Fatal("an expired entry was served")
	}
}

// TestCacheDisabled — TTL 0 means no cache at all, in both directions.
func TestCacheDisabled(t *testing.T) {
	cacheReset()
	t.Setenv("WEBSEARCH_CACHE_TTL", "0s")
	cachePut("https://bing.test/?q=q", []webResult{r("https://x.example/1", "")})
	if cacheSize() != 0 {
		t.Fatal("an entry was stored with the cache disabled")
	}
	if _, ok := cacheGet("https://bing.test/?q=q"); ok {
		t.Fatal("a lookup succeeded with the cache disabled")
	}
}

// TestCacheIsBounded — an adversarial query stream must not grow the process one
// query at a time.
func TestCacheIsBounded(t *testing.T) {
	cacheReset()
	for i := 0; i < cacheMax+64; i++ {
		cachePut("https://bing.test/?q="+string(rune(i%1000))+"-"+time.Now().Format("150405.000000000"), []webResult{r("https://x.example/1", "")})
	}
	if n := cacheSize(); n > cacheMax {
		t.Fatalf("cache grew past its bound: %d > %d", n, cacheMax)
	}
}
