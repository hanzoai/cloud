package websearch

// outcome.go — an engine that returns nothing is either empty or BLIND, and
// telling those apart is the whole reliability of a metasearch.
//
// Until this file, it could not. fetchEngine returned ([]webResult, error) and a
// bot-challenge page came back as (nil, nil) — the same value as a query nobody
// on the web has written about. So an engine could stop working entirely and the
// only symptom was a slightly shorter page. That is how Brave was dropped rather
// than fixed, and how DuckDuckGo sat in the default set for weeks contributing
// zero: both failed SOFT, and soft failure is indistinguishable from calm.
//
// Measured from cluster egress, which is what made the three states obvious:
//
//	lite.duckduckgo.com  static  25,672 bytes, 0 results — "Unfortunately, bots
//	                             use DuckDuckGo too. Select all squares
//	                             containing a duck." A 200. A real page. No results.
//	lite.duckduckgo.com  browser 24,410 bytes, 10 results — the same URL, rendered.
//
// The static fetch and the render disagree about the same page at the same
// second. A design in which "0 results" is an ANSWER cannot represent that
// disagreement, so it silently keeps the wrong half.
//
// THE THREE STATES
//
//	answered  the parser found results.
//	blind     the fetch succeeded and the parser found NOTHING. Either their
//	          markup moved (selector rot) or they served a challenge. From here
//	          those look identical, and the operator's next move is the same for
//	          both: go look at the page.
//	failed    the engine was never reached — transport error, non-200. The parser
//	          never ran, so this says nothing about the parser.
//
// WHY ZERO IS BLIND AND NOT EMPTY. There is no reliable "no results" marker to
// test for, and the measurement says so: Bing NEVER returns zero. Asked three
// distinct nonsense strings it returned ten results each time — Edmonton property
// tax, Bastille Day, Microsoft support — and for a fourth, pornography. Mojeek and
// DDG do return zero, but they also return zero when they serve a captcha, which
// is the case this file exists to catch. Trusting an engine to self-report
// emptiness means trusting the engine that is currently lying to us.
//
// So zero is blind, per engine, always — and the genuine-empty case is recovered
// where the evidence for it actually lives: ACROSS engines. If one engine went
// blind while another answered the same query, the query demonstrably has
// results and that engine is broken NOW. That is the line worth waking someone
// for, and it needs no magic strings.
//
// TWO INSTRUMENTS, DELIBERATELY DIFFERENT WIDTHS:
//
//   - the COUNTER records every outcome, always. An operator reads the ratio:
//     ddg 95% blind is an outage, ddg 3% blind is a handful of obscure queries.
//     A rate is the honest shape for this and needs no per-query judgement.
//   - the LOG line fires only on a CONFIRMED fault — blind while a sibling
//     answered. Narrow on purpose, so it stays worth reading.

import (
	"context"
	"sync"

	luxlog "github.com/luxfi/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// outcome is how one engine's turn ended. A string rather than an int because it
// is published — in the answer's `engines` array and as a metric attribute — and
// a number there would need a decoder ring on both sides.
type outcome string

const (
	answered outcome = "answered"
	blind    outcome = "blind"
	failed   outcome = "failed"
)

// answer is one engine's reply to one query: what it returned, how that turn
// ended, and whether the browser had already been asked.
//
// browsed matters to the reader of a blind: browsed=false means the static fetch
// came back unreadable and escalation was off or unavailable, which is a
// CONFIGURATION fact. browsed=true means we rendered the page in a real browser
// and still could not read it, which is a PARSER fact. Same state, different
// person to wake.
type answer struct {
	engine  string
	results []webResult
	outcome outcome
	browsed bool
	err     error
}

// meterName is this package's OTel scope, spelled the way apps/analytics and
// apps/cron spell theirs.
const meterName = "github.com/hanzoai/cloud/apps/websearch"

var (
	turnsOnce sync.Once
	turns     metric.Int64Counter
)

// countTurn records one engine's outcome.
//
// CARDINALITY is bounded by construction and not by hope: `engine` ranges over
// the engine registry (three names, chosen by us, never by a caller) and
// `outcome` over the three constants above. Nine series, whatever the query
// stream does. The query itself is deliberately NOT an attribute — it is
// caller-supplied and unbounded, which is how a metric becomes an outage.
func countTurn(ctx context.Context, a answer) {
	turnsOnce.Do(func() {
		turns, _ = otel.Meter(meterName).Int64Counter("hanzo_websearch_engine_total",
			metric.WithDescription("Engine turns by outcome. A rising `blind` rate is selector rot or a bot challenge: the engine returned a page and we could not read a single result out of it."))
	})
	if turns == nil {
		return
	}
	turns.Add(ctx, 1, metric.WithAttributes(
		attribute.String("engine", a.engine),
		attribute.String("outcome", string(a.outcome)),
	))
}

// logger is set once by Mount. It stays nil for the in-process library callers
// (compose.go) and in tests, so every use goes through warn. Guarded because
// metaSearch runs its engines concurrently and a test may set it while another
// search is in flight.
var (
	loggerMu sync.RWMutex
	logger   luxlog.Logger
)

func setLogger(l luxlog.Logger) {
	loggerMu.Lock()
	defer loggerMu.Unlock()
	logger = l
}

func warn(msg string, kv ...any) {
	loggerMu.RLock()
	l := logger
	loggerMu.RUnlock()
	if l != nil {
		l.Warn(msg, kv...)
	}
}

// report records every engine's outcome and says something ONLY about a
// confirmed fault: an engine that went blind on a query another engine answered.
//
// The condition is the point. "ddg returned nothing" is not evidence of anything
// on its own — the query may have no answers. "ddg returned nothing while bing
// returned ten" is proof the query has answers and ddg cannot see them. The
// first is noise and the second is the incident, and only the second is logged.
func report(ctx context.Context, query string, answers []answer) {
	anyAnswered := false
	for _, a := range answers {
		countTurn(ctx, a)
		if a.outcome == answered {
			anyAnswered = true
		}
	}
	if !anyAnswered {
		return
	}
	for _, a := range answers {
		if a.outcome != blind {
			continue
		}
		warn("websearch engine returned a page with no results while another engine answered — selector rot or a bot challenge",
			"engine", a.engine, "query", query, "browsed", a.browsed)
	}
}
