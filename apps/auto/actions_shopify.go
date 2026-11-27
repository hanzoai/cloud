package auto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/plane"
)

// Shopify connector. The id is the registry's user-scoped key connector, whose
// native Verify seals the Admin API token under "api_key" and echoes the store
// identifier into the connection's ExternalID (apps/provider/payments.go,
// keyverify.go). The actions read BOTH from that one connection: the token through
// rc.Token (bound to the validated org by dispatch), the store through the org's
// connection metadata. No action input may name a host — a caller-supplied host
// would send this tenant's token to a store of the caller's choosing.
//
// No vendored piece is involved: Shopify's piece actions read the secret "auth"
// and would 424 against a native api_key connection. These are native actions.

const (
	shopifyProviderID = "shopify"
	// shopifyTokenSecret mirrors provider.apiKeySecret (keyverify.go), which is
	// unexported. Go cannot import an unexported identifier, so the name is
	// duplicated here exactly as actions_slack.go duplicates "bot_token".
	shopifyTokenSecret = "api_key"
	// shopifyAPIVersion is pinned deliberately: a floating "unstable" version
	// would change the wire under a running automation.
	shopifyAPIVersion  = "2026-07"
	shopifyGraphQLPath = "/admin/api/" + shopifyAPIVersion + "/graphql.json"
	shopifyPageDefault = 20
	shopifyPageMax     = 100
	shopifyReadMax     = 5 << 20
	shopifyErrorMax    = 512
)

var (
	shopifyClient = &http.Client{Timeout: 15 * time.Second}
	// shopifyStoreForOrg asks the integrations APP, over the peer plane as the
	// validated org, which Shopify account the org holds. Reading provider.Connections
	// from here is not allowed: that reads provider's own `mounted` global, which
	// only provider's Use fills, and an app may not reach into another app's global.
	// The answer's Account is the provider-side external id — the store handle
	// Verify echoed into the connection — the same fact the token is custodied
	// beside. A package var so a test can swap the plane call; production never
	// reassigns it.
	shopifyStoreForOrg = func(ctx context.Context, org string) (string, error) {
		c, err := providerpeer.IntegrationsConnection(plane.For(ctx, org),
			&client.ConnectionIn{Provider: shopifyProviderID})
		if err != nil {
			return "", fmt.Errorf("shopify connection read: %w", err)
		}
		if c == nil || !c.Connected || c.Account == "" {
			return "", fmt.Errorf("shopify not connected: no connection for org")
		}
		return c.Account, nil
	}
)

func init() {
	provider.RegisterActions(shopifyProviderID, "Shopify", map[string]*provider.Action{
		"list_orders": {
			Name:        "list_orders",
			DisplayName: "List Orders",
			Description: "List recent orders from the connected Shopify store, newest first.",
			Props: []provider.PropSpec{
				{Name: "first", Type: "number", Description: "Page size, 1-100 (default 20)."},
				{Name: "after", Type: "string", Description: "Cursor from a previous page's endCursor."},
				{Name: "query", Type: "string", Description: "Shopify order search query, e.g. status:any."},
			},
			Run: runShopifyListOrders,
		},
		"get_order": {
			Name:        "get_order",
			DisplayName: "Get Order",
			Description: "Read one Shopify order by its GraphQL id, including its line items.",
			Props: []provider.PropSpec{
				{Name: "id", Type: "string", Required: true, Description: "Order GID, e.g. gid://shopify/Order/123."},
			},
			Run: runShopifyGetOrder,
		},
		"list_products": {
			Name:        "list_products",
			DisplayName: "List Products",
			Description: "List products in the connected store, newest first.",
			Props: []provider.PropSpec{
				{Name: "first", Type: "number", Description: "Page size, 1-100 (default 20)."},
				{Name: "after", Type: "string", Description: "Cursor from a previous page's endCursor."},
				{Name: "query", Type: "string", Description: "Shopify product search query."},
			},
			Run: runShopifyListProducts,
		},
		"get_product": {
			Name:        "get_product",
			DisplayName: "Get Product",
			Description: "Read one Shopify product by its GraphQL id, including its variants.",
			Props: []provider.PropSpec{
				{Name: "id", Type: "string", Required: true, Description: "Product GID, e.g. gid://shopify/Product/123."},
			},
			Run: runShopifyGetProduct,
		},
		"list_customers": {
			Name:        "list_customers",
			DisplayName: "List Customers",
			Description: "List customers of the connected store, newest first.",
			Props: []provider.PropSpec{
				{Name: "first", Type: "number", Description: "Page size, 1-100 (default 20)."},
				{Name: "after", Type: "string", Description: "Cursor from a previous page's endCursor."},
				{Name: "query", Type: "string", Description: "Shopify customer search query."},
			},
			Run: runShopifyListCustomers,
		},
		"create_product_draft": {
			Name:        "create_product_draft",
			DisplayName: "Create Product Draft",
			Description: "Create a DRAFT product. It never publishes: no productPublish is issued.",
			Props: []provider.PropSpec{
				{Name: "title", Type: "string", Required: true, Description: "Product title."},
				{Name: "descriptionHtml", Type: "string", Description: "Product body HTML."},
				{Name: "vendor", Type: "string", Description: "Product vendor."},
				{Name: "productType", Type: "string", Description: "Product type."},
				{Name: "tags", Type: "array", Description: "Product tags, as an array of strings."},
			},
			Run: runShopifyCreateProductDraft,
		},
	}, nil)
}

// Queries are static strings; every caller value rides as a GraphQL variable, so
// no input can alter the query shape. A pinned version keeps the field set stable.
const (
	shopifyOrdersQuery = `query ShopifyOrders($first: Int!, $after: String, $query: String) {
  orders(first: $first, after: $after, query: $query, sortKey: CREATED_AT, reverse: true) {
    edges { cursor node { id name createdAt displayFinancialStatus displayFulfillmentStatus
      totalPriceSet { shopMoney { amount currencyCode } } customer { id displayName email } } }
    pageInfo { hasNextPage endCursor }
  }
}`
	shopifyOrderQuery = `query ShopifyOrder($id: ID!) {
  order(id: $id) { id name createdAt displayFinancialStatus displayFulfillmentStatus
    totalPriceSet { shopMoney { amount currencyCode } }
    lineItems(first: 50) { nodes { title quantity sku } } }
}`
	shopifyProductsQuery = `query ShopifyProducts($first: Int!, $after: String, $query: String) {
  products(first: $first, after: $after, query: $query, sortKey: CREATED_AT, reverse: true) {
    edges { cursor node { id title handle status vendor productType totalInventory createdAt } }
    pageInfo { hasNextPage endCursor }
  }
}`
	shopifyProductQuery = `query ShopifyProduct($id: ID!) {
  product(id: $id) { id title handle descriptionHtml status vendor productType
    variants(first: 50) { nodes { id title price sku inventoryQuantity } } }
}`
	shopifyCustomersQuery = `query ShopifyCustomers($first: Int!, $after: String, $query: String) {
  customers(first: $first, after: $after, query: $query, sortKey: CREATED_AT, reverse: true) {
    edges { cursor node { id displayName email phone numberOfOrders
      amountSpent { amount currencyCode } createdAt } }
    pageInfo { hasNextPage endCursor }
  }
}`
	shopifyProductDraftMutation = `mutation ShopifyCreateProductDraft($input: ProductInput!) {
  productCreate(input: $input) { product { id title handle status createdAt } userErrors { field message } }
}`
)

func runShopifyListOrders(ctx context.Context, rc provider.RunContext) (any, error) {
	return shopifyGraphQL(ctx, rc, shopifyOrdersQuery, shopifyPageVars(rc.Input))
}

func runShopifyGetOrder(ctx context.Context, rc provider.RunContext) (any, error) {
	id := strInput(rc.Input, "id")
	if id == "" {
		return nil, fmt.Errorf("shopify get_order: id is required")
	}
	return shopifyGraphQL(ctx, rc, shopifyOrderQuery, map[string]any{"id": id})
}

func runShopifyListProducts(ctx context.Context, rc provider.RunContext) (any, error) {
	return shopifyGraphQL(ctx, rc, shopifyProductsQuery, shopifyPageVars(rc.Input))
}

func runShopifyGetProduct(ctx context.Context, rc provider.RunContext) (any, error) {
	id := strInput(rc.Input, "id")
	if id == "" {
		return nil, fmt.Errorf("shopify get_product: id is required")
	}
	return shopifyGraphQL(ctx, rc, shopifyProductQuery, map[string]any{"id": id})
}

func runShopifyListCustomers(ctx context.Context, rc provider.RunContext) (any, error) {
	return shopifyGraphQL(ctx, rc, shopifyCustomersQuery, shopifyPageVars(rc.Input))
}

func runShopifyCreateProductDraft(ctx context.Context, rc provider.RunContext) (any, error) {
	title := strInput(rc.Input, "title")
	if title == "" {
		return nil, fmt.Errorf("shopify create_product_draft: title is required")
	}
	input := map[string]any{"title": title, "status": "DRAFT"}
	for _, key := range []string{"descriptionHtml", "vendor", "productType"} {
		if v := strInput(rc.Input, key); v != "" {
			input[key] = v
		}
	}
	if tags, ok := rc.Input["tags"].([]any); ok && len(tags) > 0 {
		input["tags"] = tags
	}
	out, err := shopifyGraphQL(ctx, rc, shopifyProductDraftMutation, map[string]any{"input": input})
	if err != nil {
		return nil, err
	}
	if err := shopifyUserErrors(out); err != nil {
		return nil, err
	}
	return out, nil
}

// shopifyUserErrors reads the mutation's own error channel. Shopify reports a
// refused write in data.productCreate.userErrors with a 200, so a caller would
// otherwise read a failed draft as a success.
func shopifyUserErrors(data any) error {
	m, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	created, ok := m["productCreate"].(map[string]any)
	if !ok {
		return nil
	}
	errs, ok := created["userErrors"].([]any)
	if !ok || len(errs) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		if m, ok := e.(map[string]any); ok {
			if msg, ok := m["message"].(string); ok {
				msgs = append(msgs, msg)
			}
		}
	}
	if len(msgs) == 0 {
		return fmt.Errorf("shopify create_product_draft: refused")
	}
	return fmt.Errorf("shopify create_product_draft: %s", strings.Join(msgs, "; "))
}

func shopifyPageVars(in map[string]any) map[string]any {
	first := numInput(in, "first")
	switch {
	case first == 0:
		first = shopifyPageDefault
	case first < 1:
		first = 1
	case first > shopifyPageMax:
		first = shopifyPageMax
	}
	return map[string]any{"first": int(first), "after": strInput(in, "after"), "query": strInput(in, "query")}
}

type shopifyGraphError struct {
	Message string `json:"message"`
}

// shopifyGraphQL is the ONE Admin API call. Credential first (fail closed), then
// the org's stored store identifier — never an input field — normalized by the
// same rule Verify uses. Both the host and the token come from the connection.
func shopifyGraphQL(ctx context.Context, rc provider.RunContext, query string, variables map[string]any) (any, error) {
	tok, err := rc.Token(shopifyTokenSecret)
	if err != nil {
		return nil, fmt.Errorf("shopify not connected: %w", err)
	}
	tok = bytes.TrimSpace(tok)
	if len(tok) == 0 {
		return nil, fmt.Errorf("shopify not connected: empty credential")
	}
	store, err := shopifyStoreForOrg(ctx, rc.Org)
	if err != nil {
		return nil, fmt.Errorf("shopify: %w", err)
	}
	// Normalize FIRST, even when an operator override supplies the origin: a bad
	// stored identifier is a refusal, never a value that silently goes unused.
	host, err := provider.ShopHost(store)
	if err != nil {
		return nil, fmt.Errorf("shopify: invalid store identifier: %w", err)
	}
	origin := env.Or("SHOPIFY_API_BASE", "")
	if origin == "" {
		origin = "https://" + host
	}
	origin = strings.TrimRight(origin, "/")

	body, err := jsonBytes(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, fmt.Errorf("shopify: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+shopifyGraphQLPath, body)
	if err != nil {
		return nil, fmt.Errorf("shopify: build request: %w", err)
	}
	req.Header.Set("X-Shopify-Access-Token", string(tok))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := shopifyClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("shopify request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, shopifyReadMax))
	if err != nil {
		return nil, fmt.Errorf("shopify: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("shopify api %d: %s", resp.StatusCode, shopifySnippet(raw))
	}
	var answer struct {
		Data   json.RawMessage     `json:"data"`
		Errors []shopifyGraphError `json:"errors"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fmt.Errorf("shopify: decode: %w", err)
	}
	if len(answer.Errors) > 0 {
		msgs := make([]string, 0, len(answer.Errors))
		for _, e := range answer.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, fmt.Errorf("shopify graphql: %s", strings.Join(msgs, "; "))
	}
	var data any
	if len(answer.Data) > 0 && string(answer.Data) != "null" {
		if err := json.Unmarshal(answer.Data, &data); err != nil {
			return nil, fmt.Errorf("shopify: decode data: %w", err)
		}
	}
	return data, nil
}

// shopifySnippet bounds an upstream error body; it is a diagnostic, never a
// second unbounded store of response data.
func shopifySnippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > shopifyErrorMax {
		s = s[:shopifyErrorMax] + "…"
	}
	return s
}
