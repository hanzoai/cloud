package billing

import (
	"encoding/json"
	"testing"
)

func TestProductOf(t *testing.T) {
	cases := []struct {
		name string
		md   map[string]any
		want string
	}{
		{"functions", map[string]any{"provider": "functions", "model": "invoke"}, "functions"},
		{"s3", map[string]any{"provider": "s3", "model": "op"}, "s3"},
		{"agent→agents", map[string]any{"provider": "agent", "model": "gpt-4o"}, "agents"},
		{"provisioning kind sql", map[string]any{"provider": "provisioning", "model": "sql"}, "sql"},
		{"provisioning kind kv", map[string]any{"provider": "provisioning", "model": "kv"}, "kv"},
		{"provisioning no kind", map[string]any{"provider": "provisioning"}, "provisioning"},
		{"security.scan→security", map[string]any{"provider": "security.scan"}, "security"},
		{"automations", map[string]any{"provider": "automations", "model": "automations"}, "automations"},
		{"ai by prompt tokens", map[string]any{"provider": "openai", "model": "gpt-4o", "promptTokens": float64(10)}, "inference"},
		{"ai by completion tokens", map[string]any{"provider": "anthropic", "completionTokens": float64(5)}, "inference"},
		{"ai by premium", map[string]any{"provider": "openai", "premium": true}, "inference"},
		{"explicit product wins", map[string]any{"product": "chat", "provider": "openai", "promptTokens": float64(9)}, "chat"},
		{"legacy surface", map[string]any{"surface": "search"}, "search"},
		{"empty", map[string]any{"model": "gpt-4o-mini"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := productOf(c.md); got != c.want {
				t.Fatalf("productOf(%v) = %q, want %q", c.md, got, c.want)
			}
		})
	}
}

// ledger builds a commerce GetUsage envelope with the given rows.
func ledger(rows ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"user": "maxpower", "count": len(rows), "usage": rows})
	return b
}

func row(cents int64, md map[string]any) map[string]any {
	return map[string]any{"transactionId": "t", "amount": cents, "metadata": md}
}

func TestEnrichUsage_InjectsProduct(t *testing.T) {
	body := ledger(
		row(1, map[string]any{"provider": "functions", "model": "invoke"}),
		row(2, map[string]any{"provider": "agent", "model": "gpt-4o"}),
	)
	out, ok := enrichUsageLedger(body, "", "")
	if !ok {
		t.Fatal("enrichUsageLedger returned !ok on a valid envelope")
	}
	var got struct {
		Usage []struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	if len(got.Usage) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got.Usage))
	}
	if got.Usage[0].Metadata["product"] != "functions" {
		t.Fatalf("row0 product: want functions, got %v", got.Usage[0].Metadata["product"])
	}
	if got.Usage[1].Metadata["product"] != "agents" {
		t.Fatalf("row1 product: want agents, got %v", got.Usage[1].Metadata["product"])
	}
}

func TestEnrichUsage_FilterByProduct(t *testing.T) {
	body := ledger(
		row(1, map[string]any{"provider": "functions", "model": "invoke"}),
		row(2, map[string]any{"provider": "s3", "model": "op"}),
		row(3, map[string]any{"provider": "functions", "model": "invoke"}),
	)
	out, ok := enrichUsageLedger(body, "functions", "")
	if !ok {
		t.Fatal("!ok")
	}
	var got struct {
		Count int `json:"count"`
		Usage []struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Count != 2 || len(got.Usage) != 2 {
		t.Fatalf("filter product=functions: want 2, got count=%d rows=%d", got.Count, len(got.Usage))
	}
	for _, r := range got.Usage {
		if r.Metadata["product"] != "functions" {
			t.Fatalf("filtered row not functions: %v", r.Metadata["product"])
		}
	}
}

func TestEnrichUsage_GroupByProduct(t *testing.T) {
	body := ledger(
		row(10, map[string]any{"provider": "functions", "model": "invoke"}),
		row(20, map[string]any{"provider": "functions", "model": "invoke"}),
		row(5, map[string]any{"provider": "s3", "model": "op"}),
		row(7, map[string]any{"provider": "openai", "promptTokens": float64(3)}),
	)
	out, ok := enrichUsageLedger(body, "", "product")
	if !ok {
		t.Fatal("!ok")
	}
	var got struct {
		GroupBy string         `json:"groupBy"`
		Count   int            `json:"count"`
		Groups  []productGroup `json:"groups"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	if got.GroupBy != "product" || got.Count != 4 {
		t.Fatalf("group envelope: groupBy=%q count=%d", got.GroupBy, got.Count)
	}
	by := map[string]productGroup{}
	for _, g := range got.Groups {
		by[g.Product] = g
	}
	if g := by["functions"]; g.Requests != 2 || g.AmountCents != 30 {
		t.Fatalf("functions group: %+v", g)
	}
	if g := by["s3"]; g.Requests != 1 || g.AmountCents != 5 {
		t.Fatalf("s3 group: %+v", g)
	}
	if g := by["inference"]; g.Requests != 1 || g.AmountCents != 7 {
		t.Fatalf("inference group: %+v", g)
	}
}

func TestEnrichUsage_GroupFilterCombined(t *testing.T) {
	body := ledger(
		row(10, map[string]any{"provider": "functions", "model": "invoke"}),
		row(5, map[string]any{"provider": "s3", "model": "op"}),
	)
	out, ok := enrichUsageLedger(body, "s3", "product")
	if !ok {
		t.Fatal("!ok")
	}
	var got struct {
		Groups []productGroup `json:"groups"`
	}
	_ = json.Unmarshal(out, &got)
	if len(got.Groups) != 1 || got.Groups[0].Product != "s3" || got.Groups[0].AmountCents != 5 {
		t.Fatalf("filter+group product=s3: %+v", got.Groups)
	}
}

func TestEnrichUsage_MalformedReturnsNotOk(t *testing.T) {
	if _, ok := enrichUsageLedger([]byte(`not json`), "", ""); ok {
		t.Fatal("non-JSON body must return ok=false so the caller passes it through verbatim")
	}
	if _, ok := enrichUsageLedger([]byte(`{"balance":100}`), "", ""); ok {
		t.Fatal("envelope without usage[] must return ok=false")
	}
}
