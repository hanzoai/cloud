package billing

// usage.go — the read-side product/agent attribution for the customer usage
// ledger. Commerce persists each billed call's metering SURFACE (`provider`) and
// billed UNIT (`model`) but has no first-class `product` field, while the console's
// per-product Metrics dashboard groups on `metadata.product` (and `metadata.agent`).
// This file is the ONE read-side adapter that maps the persisted schema to those
// axes, so the breakdowns the UI already renders populate from the SAME charged
// ledger — no fabricated numbers, no double-count. When the meter/commerce begin
// persisting `product`/`agent` natively, a row that already carries them WINS, so
// this degrades to a no-op (forward-compatible).

import "encoding/json"

// productOf maps ONE usage row's metadata to the console's canonical product id.
// A row that already carries metadata.product (or the legacy `surface`) wins.
// Otherwise: a token-metered row is LLM inference; every other row is attributed
// by its metering surface (provider), with the two surfaces whose provider label
// differs from the product id normalized (agent→agents; provisioning→the kind).
func productOf(md map[string]any) string {
	if p := mStr(md, "product"); p != "" {
		return p
	}
	if p := mStr(md, "surface"); p != "" {
		return p
	}
	if mNum(md, "promptTokens") > 0 || mNum(md, "completionTokens") > 0 ||
		mNum(md, "totalTokens") > 0 || mBool(md, "premium") {
		return "inference" // LLM/token spend, whatever upstream served it.
	}
	switch p := mStr(md, "provider"); p {
	case "":
		return ""
	case "agent":
		return "agents" // meter surface (singular) → console product id (plural).
	case "provisioning":
		if k := mStr(md, "model"); k != "" {
			return k // the provisioned kind IS the product: sql/kv/vector/docdb/...
		}
		return "provisioning"
	case "security.scan":
		return "security"
	default:
		return p // functions, s3, automations, tracker, compute, ...
	}
}

// agentOf returns the agent identity a row is attributed to, or "" when the row is
// not an agent invocation. Commerce does not yet persist an agent field, so this is
// empty for today's agent-run debits (the agent NAME is not on the ledger row) —
// tracked in LLM.md as the one remaining cross-repo step for the agent axis.
func agentOf(md map[string]any) string {
	for _, k := range []string{"agent", "agentName", "agentId"} {
		if v := mStr(md, k); v != "" {
			return v
		}
	}
	return ""
}

// enrichUsageLedger rewrites commerce's GetUsage envelope ({user,count,usage:[...]}):
// it injects the canonical product/agent dims onto every row, optionally filters to
// ONE product, and optionally reduces to a per-product rollup. Returns (nil,false)
// when the body is not the expected envelope — the caller then returns it verbatim,
// so a schema drift downstream degrades to passthrough rather than data loss.
func enrichUsageLedger(body []byte, product, groupBy string) ([]byte, bool) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, false
	}
	rawUsage, ok := env["usage"]
	if !ok {
		return nil, false
	}
	var rows []map[string]any
	if err := json.Unmarshal(rawUsage, &rows); err != nil {
		return nil, false
	}

	kept := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		md, _ := r["metadata"].(map[string]any)
		if md == nil {
			md = map[string]any{}
		}
		p := productOf(md)
		if p != "" {
			md["product"] = p // console reads metadata.product directly.
		}
		if a := agentOf(md); a != "" {
			md["agent"] = a
		}
		r["metadata"] = md
		if product != "" && p != product {
			continue // server-side ?product= filter (was silently ignored).
		}
		kept = append(kept, r)
	}

	if groupBy == "product" {
		return marshalGroups(env, kept)
	}

	env["usage"] = mustRaw(kept)
	env["count"] = mustRaw(len(kept))
	out, err := json.Marshal(env)
	if err != nil {
		return nil, false
	}
	return out, true
}

// productGroup is one row of the ?groupBy=product rollup — requests + spend for a
// single product, in first-seen order (stable for the UI).
type productGroup struct {
	Product     string `json:"product"`
	Requests    int    `json:"requests"`
	AmountCents int64  `json:"amountCents"`
}

// marshalGroups reduces the enriched+filtered rows to a per-product rollup envelope
// {user,groupBy:"product",count,groups:[{product,requests,amountCents}]}.
func marshalGroups(env map[string]json.RawMessage, rows []map[string]any) ([]byte, bool) {
	byID := map[string]*productGroup{}
	order := []string{}
	for _, r := range rows {
		md, _ := r["metadata"].(map[string]any)
		p := productOf(md)
		if p == "" {
			p = "other"
		}
		g := byID[p]
		if g == nil {
			g = &productGroup{Product: p}
			byID[p] = g
			order = append(order, p)
		}
		g.Requests++
		g.AmountCents += int64(mNum(r, "amount"))
	}
	groups := make([]productGroup, 0, len(order))
	for _, p := range order {
		groups = append(groups, *byID[p])
	}
	out := map[string]any{"groupBy": "product", "groups": groups, "count": len(rows)}
	if u, ok := env["user"]; ok {
		out["user"] = u
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return b, true
}

// --- metadata coercers: read a JSON-decoded map[string]any safely -------------

func mStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func mNum(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}

func mBool(m map[string]any, k string) bool {
	b, _ := m[k].(bool)
	return b
}

func mustRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
