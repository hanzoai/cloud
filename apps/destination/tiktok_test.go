package destination

import "testing"

// TestTikTokEcommerce proves the ecommerce translation: our line items → TikTok's native
// contents[] + content_type, alongside value/currency, for Value-Based Optimization.
func TestTikTokEcommerce(t *testing.T) {
	body := tiktokBuild(Config{"pixelCode": "PC1"}, []Conversion{{
		Standard: EventPurchase, Name: "order_completed", Value: 59.98, Currency: "USD", EventID: "ord-9",
		User: UserData{Email: "a@b.com"},
		Items: []Item{
			{ID: "SKU1", Name: "Widget", Category: "tools", Brand: "Acme", Price: 24.99, Quantity: 2},
			{ID: "SKU2", Price: 10.0, Quantity: 1},
		},
	}})
	p := body.Data[0].Properties
	if p["value"] != 59.98 || p["currency"] != "USD" || p["content_type"] != "product" {
		t.Errorf("value/currency/content_type: %+v", p)
	}
	contents, ok := p["contents"].([]map[string]any)
	if !ok || len(contents) != 2 {
		t.Fatalf("contents = %+v", p["contents"])
	}
	if contents[0]["content_id"] != "SKU1" || contents[0]["content_name"] != "Widget" ||
		contents[0]["content_category"] != "tools" || contents[0]["brand"] != "Acme" ||
		contents[0]["price"] != 24.99 || contents[0]["quantity"] != 2.0 {
		t.Errorf("content[0]: %+v", contents[0])
	}
	// A non-commerce event carries no properties block (nothing to over-send).
	pv := tiktokBuild(Config{"pixelCode": "PC1"}, []Conversion{{Standard: EventPageView, Name: "$pageview", User: UserData{ExternalID: "v1"}}})
	if pv.Data[0].Properties != nil {
		t.Errorf("pageview must carry no properties, got %+v", pv.Data[0].Properties)
	}
}
