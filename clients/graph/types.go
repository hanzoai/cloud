// types.go holds the console view structs (what this subsystem emits) plus the PURE
// mapping from the upstream chain-data objects to them. The view JSON keys mirror the
// console modules EXACTLY so the Indexer and Oracles pages render with no front-end
// change:
//
//   - indexerView -> console2 IndexerModule.tsx  Indexer {id,chain,network,height,lag,status,updatedAt}
//   - oracleView  -> console2 OraclesModule.tsx  Oracle  {id,name,feed,value,source,status,updatedAt}
//
// Every field is a REAL upstream value or an honest omission. Data the upstream does
// not carry — the chain HEAD (hence true indexing lag) — is left off so the UI renders
// "—", never a fabricated 0.
package graph

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ---- console view structs ----

// indexerView is the shape console2 IndexerModule (Indexer) consumes. `lag` is
// deliberately absent from the output (the indexer REST exposes the indexed height
// but not the chain HEAD, so lag is not derivable — omitted, never invented).
type indexerView struct {
	ID        string `json:"id"`
	Chain     string `json:"chain,omitempty"`
	Network   string `json:"network,omitempty"`
	Height    string `json:"height,omitempty"`
	Lag       string `json:"lag,omitempty"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// oracleView is the shape console2 OraclesModule (Oracle) consumes — one row per
// on-chain price feed. requests/telemetry the graph does not carry are omitted.
type oracleView struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Feed      string `json:"feed,omitempty"`
	Value     string `json:"value,omitempty"`
	Source    string `json:"source,omitempty"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// ---- mapping (PURE) ----

// toIndexerView projects the indexer's /health + latest block into the console row.
// health and block are each nil when their upstream call did not succeed (one may be
// absent in a lighter indexer mode); the view degrades honestly:
//   - chain  ← the real chain_name, else the deployment brand (its chain family).
//   - network ← the deployment env (mainnet|testnet|devnet), the real network tier.
//   - height/updatedAt ← the latest indexed block (blank when none indexed yet).
//   - status ← "degraded" only when /health explicitly reports unhealthy; otherwise a
//     reachable indexer is "active". lag is omitted (not derivable — never faked).
func toIndexerView(health, block map[string]any, brand, env string) indexerView {
	chainName := str(health, "chain_name")
	id := firstNonEmpty(chainName, str(health, "chain_id"), brand, "indexer")

	status := "active"
	if health != nil {
		if h, ok := asBool(health, "healthy"); ok && !h {
			status = "degraded"
		}
	}

	return indexerView{
		ID:        id,
		Chain:     firstNonEmpty(chainName, brand),
		Network:   env,
		Height:    str(block, "height"),
		Status:    status,
		UpdatedAt: asTime(get(block, "timestamp")),
	}
}

// toOracleView maps a graph PriceFeed (a dynamic entity map) to the console oracle
// row. name/feed are the trading pair (e.g. "LUX/USD"); value is the feed's price
// verbatim; source is the O-Chain oracle network these feeds originate from; a listed
// feed carrying a price is "active"; updatedAt is the feed's timestamp.
func toOracleView(f map[string]any) oracleView {
	pair := firstNonEmpty(str(f, "pair"), str(f, "symbol"))
	id := firstNonEmpty(str(f, "id"), pair)
	return oracleView{
		ID:        id,
		Name:      firstNonEmpty(pair, id),
		Feed:      pair,
		Value:     str(f, "price"),
		Source:    firstNonEmpty(str(f, "source"), "O-Chain"),
		Status:    "active",
		UpdatedAt: asTime(get(f, "timestamp")),
	}
}

// ---- tolerant accessors (PURE): the upstreams return dynamic JSON (indexer block
// maps, graph entity maps), so read fields defensively without assuming a Go type. ----

func get(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

// str stringifies a JSON value (string, number, bool) at key, or "" when absent. A
// float64 that is integral (JSON numbers decode as float64) is rendered without a
// trailing ".0" so a block height reads "42", not "42.0".
func str(m map[string]any, key string) string {
	switch v := get(m, key).(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func asBool(m map[string]any, key string) (bool, bool) {
	if b, ok := get(m, key).(bool); ok {
		return b, true
	}
	return false, false
}

// asTime normalizes a timestamp value to an ISO-8601 string the console can pass to
// new Date(): a unix-seconds number becomes RFC3339 UTC; an already-formatted string
// passes through; anything else yields "" (the UI then renders "—").
func asTime(v any) string {
	switch t := v.(type) {
	case float64:
		if t <= 0 {
			return ""
		}
		return time.Unix(int64(t), 0).UTC().Format(time.RFC3339)
	case string:
		return strings.TrimSpace(t)
	default:
		return ""
	}
}
