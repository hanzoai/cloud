package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	square "github.com/square/square-go-sdk/v3"
	sqcheckout "github.com/square/square-go-sdk/v3/checkout"
	sqpaymentlinks "github.com/square/square-go-sdk/v3/checkout/paymentlinks"
	"github.com/square/square-go-sdk/v3/core"
	"github.com/square/square-go-sdk/v3/option"

	hostedcheckout "github.com/hanzoai/cloud/clients/commerce/checkout"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/events"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/coupon"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// checkoutLineItem is a provider-agnostic line item used by both Stripe and Square checkout flows.
type checkoutLineItem struct {
	Name     string
	Quantity int
	Amount   int64 // cents
}

type checkoutSessionCustomer struct {
	FullName string `json:"fullName"`
	Email    string `json:"email"`
	Address  string `json:"address"`
	City     string `json:"city"`
	Zip      string `json:"zip"`
}

type checkoutSessionHat struct {
	HatColor  string `json:"hatColor"`
	TextColor string `json:"textColor"`
	Text      string `json:"text"`
	BackText  string `json:"backText"`
	Font      string `json:"font"`
	TextStyle string `json:"textStyle"`
	FlagCode  string `json:"flagCode"`
	Size      string `json:"size"`
}

type checkoutSessionItem struct {
	ID        string             `json:"id"`
	Quantity  int                `json:"quantity"`
	UnitPrice float64            `json:"unitPrice"` // ignored; server computes price
	// Catalog references. When any is set, the server prices the item from the
	// org's real per-org store listing (never from client input) — this is the
	// generic per-org storefront path. The legacy custom-hat path (Hat below) is
	// used only when NO catalog reference resolves to a listing.
	ProductId   string             `json:"productId,omitempty"`
	ProductSlug string             `json:"productSlug,omitempty"`
	VariantSku  string             `json:"variantSku,omitempty"`
	Name        string             `json:"name,omitempty"`
	Hat         checkoutSessionHat `json:"hat"`
}

type checkoutSessionRequest struct {
	Company      string                  `json:"company"`
	ProviderHint string                  `json:"providerHint"`
	Currency     string                  `json:"currency"`
	// NOTE: there is deliberately NO org/tenant field. The org is SOLELY the
	// authenticated principal's (middleware.GetOrganization) — a client can only
	// ever mint checkout for its OWN tenant, never one it names in the body.
	Project      string                  `json:"project"`
	Customer     checkoutSessionCustomer `json:"customer"`
	Items        []checkoutSessionItem   `json:"items"`
	SuccessURL   string                  `json:"successUrl"`
	CancelURL    string                  `json:"cancelUrl"`
	CouponCode   string                  `json:"couponCode,omitempty"`
	ReferrerId   string                  `json:"referrerId,omitempty"`
	AffiliateId  string                  `json:"affiliateId,omitempty"`
}

// couponDiscount holds the resolved discount for a checkout session.
type couponDiscount struct {
	Code          string `json:"code"`
	Type          string `json:"type"`
	Amount        int    `json:"amount"`
	DiscountCents int64  `json:"discountCents"`
}

type checkoutSessionResponse struct {
	CheckoutURL   string          `json:"checkoutUrl"`
	SessionID     string          `json:"sessionId"`
	Discount      *couponDiscount `json:"discount,omitempty"`
	OriginalTotal int64           `json:"originalTotal,omitempty"`
	FinalTotal    int64           `json:"finalTotal,omitempty"`
}

// resolveCoupon looks up a coupon by code in the datastore and returns it
// if valid. Returns nil (no error) when couponCode is empty.
func resolveCoupon(c *gin.Context, org *organization.Organization, couponCode string) (*coupon.Coupon, error) {
	if couponCode == "" {
		return nil, nil
	}
	// Per-org store (Red MED-1): coupons are per-org merchant entities, created in
	// the org's own store. datastore.New(c) binds the shared systemDB (and drops
	// the namespace), so with the resolver installed it would never find the
	// caller's coupon → every hosted-checkout coupon silently "not found".
	db := datastore.NewNamespaced(org.Namespaced(c))
	cpn := coupon.New(db)
	ok, err := cpn.Query().Filter("Code_=", strings.ToUpper(strings.TrimSpace(couponCode))).Get()
	if err != nil || !ok {
		return nil, errors.New("coupon not found")
	}
	if !cpn.ValidFor(time.Now()) || !cpn.Redeemable() {
		return nil, errors.New("coupon expired or fully redeemed")
	}
	return cpn, nil
}

// applyDiscount computes the discount amount in cents given a subtotal and coupon.
// Returns the discount in cents (always >= 0).
func applyDiscount(subtotalCents int64, cpn *coupon.Coupon) int64 {
	if cpn == nil {
		return 0
	}
	switch cpn.Type {
	case coupon.Percent:
		// Amount field holds the percentage (e.g. 10 = 10%)
		discount := subtotalCents * int64(cpn.Amount) / 100
		if discount > subtotalCents {
			discount = subtotalCents
		}
		return discount
	case coupon.Flat:
		// Amount field holds cents (e.g. 500 = $5.00)
		discount := int64(cpn.Amount)
		if discount > subtotalCents {
			discount = subtotalCents
		}
		return discount
	default:
		return 0
	}
}

func normalizeColor(color string) string {
	return strings.ToLower(strings.TrimSpace(color))
}

func isWhite(color string) bool {
	c := normalizeColor(color)
	return c == "#fff" || c == "#ffffff" || c == "white" || c == "rgb(255,255,255)"
}

func isBlack(color string) bool {
	c := normalizeColor(color)
	return c == "#000" || c == "#000000" || c == "black" || c == "rgb(0,0,0)"
}

func hatPriceCents(h checkoutSessionHat) int64 {
	// Match storefront pricing logic: $80 only for true white-on-white or black-on-black.
	whiteOnWhite := isWhite(h.HatColor) && isWhite(h.TextColor)
	blackOnBlack := isBlack(h.HatColor) && isBlack(h.TextColor)
	if whiteOnWhite || blackOnBlack {
		return 8000
	}
	return 5000
}

func safeName(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "MEGA Hat"
	}
	// Square line item name max is limited; keep it tight.
	if len(s) > 60 {
		return s[:60]
	}
	return s
}

// hasCatalogRef reports whether a checkout item references a catalog listing
// (as opposed to a pure custom-hat item priced by configuration).
func hasCatalogRef(it checkoutSessionItem) bool {
	return strings.TrimSpace(it.ProductId) != "" ||
		strings.TrimSpace(it.ProductSlug) != "" ||
		strings.TrimSpace(it.VariantSku) != ""
}

// itemRef returns a human-readable reference for error messages.
func itemRef(it checkoutSessionItem) string {
	for _, s := range []string{it.ProductId, it.ProductSlug, it.VariantSku, it.ID} {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return "(unspecified)"
}

// loadOrgCatalog loads the org's default (first) store listings from its own
// per-org datastore (Red MED-1: NewNamespaced, not the shared systemDB). Returns
// nil when the org has no store — callers then treat every item as a legacy hat.
func loadOrgCatalog(c *gin.Context, org *organization.Organization) store.Listings {
	db := datastore.NewNamespaced(org.Namespaced(c))
	s := store.New(db)
	var stores []store.Store
	if _, err := s.Query().All().Limit(1).GetAll(&stores); err != nil || len(stores) == 0 {
		return nil
	}
	return stores[0].Listings
}

// catalogPrice resolves a checkout item against the org's real catalog and
// returns its listing name and price in cents. The price is authoritative —
// server-side, from the stored listing — never the client's UnitPrice. Returns
// ok=false when no visible, priced listing matches (caller rejects the item).
func catalogPrice(listings store.Listings, it checkoutSessionItem) (string, int64, bool) {
	if listings == nil {
		return "", 0, false
	}
	pid := strings.TrimSpace(it.ProductId)
	slug := strings.TrimSpace(it.ProductSlug)
	sku := strings.TrimSpace(it.VariantSku)
	id := strings.TrimSpace(it.ID)
	for key, l := range listings {
		if l.Hidden != nil && *l.Hidden {
			continue
		}
		match := (pid != "" && (l.ProductId == pid || l.VariantId == pid)) ||
			(slug != "" && l.Slug == slug) ||
			(sku != "" && l.SKU == sku) ||
			(id != "" && (key == id || l.ProductId == id || l.Slug == id || l.SKU == id))
		if !match {
			continue
		}
		if l.Price == nil {
			// Matched listing with no price cannot be sold.
			return "", 0, false
		}
		name := key
		if l.Name != nil && strings.TrimSpace(*l.Name) != "" {
			name = strings.TrimSpace(*l.Name)
		}
		if len(name) > 60 {
			name = name[:60]
		}
		return name, int64(*l.Price), true
	}
	return "", 0, false
}

func isValidRedirect(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	if u.Host == "" {
		return false
	}
	return true
}

// websiteURLs returns the org's registered website URLs — its OWN domains — for
// the checkout redirect allowlist.
func websiteURLs(org *organization.Organization) []string {
	if org == nil || len(org.Websites) == 0 {
		return nil
	}
	out := make([]string, 0, len(org.Websites))
	for _, w := range org.Websites {
		if u := strings.TrimSpace(w.Url); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// squareCheckoutClientForOrg creates a Square Payment Links client using the
// org's KMS-hydrated credentials. Falls back to env vars if the org has no
// Square credentials configured (backwards compat for single-tenant deploys).
func squareCheckoutClientForOrg(org *organization.Organization) (*sqpaymentlinks.Client, string, error) {
	isSandbox := org.TestMode()
	sqCfg := org.SquareConfig(isSandbox)

	token := sqCfg.AccessToken
	locationID := sqCfg.LocationId

	// FAIL-CLOSED in production. An org that has not configured its OWN
	// per-tenant Square credentials (KMS) can NEVER borrow the deployment's
	// env-configured account to mint a REAL square.link. There is no env
	// fallback and no platform-org special case for live money — the whole
	// "anonymous env-token / platform-org" lending path is gone. Only SANDBOX
	// (no money at risk) keeps the env fallback, so the per-org storefront
	// sandbox demo still mints intents behind authentication.
	if token == "" {
		if !isSandbox {
			return nil, "", errors.New("square is not configured for this organization")
		}
		token = strings.TrimSpace(os.Getenv("SQUARE_ACCESS_TOKEN"))
		locationID = strings.TrimSpace(os.Getenv("SQUARE_LOCATION_ID"))
		if t := strings.TrimSpace(os.Getenv("SQUARE_SANDBOX_ACCESS_TOKEN")); t != "" {
			token = t
		}
		if l := strings.TrimSpace(os.Getenv("SQUARE_SANDBOX_LOCATION_ID")); l != "" {
			locationID = l
		}
	}

	if token == "" || locationID == "" {
		return nil, "", errors.New("square is not configured for this organization")
	}

	baseURL := "https://connect.squareup.com"
	if isSandbox {
		baseURL = "https://connect.squareupsandbox.com"
	}

	client := sqpaymentlinks.NewClient(core.NewRequestOptions(
		option.WithToken(token),
		option.WithBaseURL(baseURL),
	))
	return client, locationID, nil
}

// Sessions creates a provider-agnostic hosted checkout session.
//
// Currently implemented using Square Payment Links (hosted checkout URL).
// When providerHint is "wire", returns wire transfer instructions instead.
func Sessions(c *gin.Context) {
	var req checkoutSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "Invalid request", err)
		return
	}

	if len(req.Items) == 0 {
		http.Fail(c, 400, "No items", errors.New("items is required"))
		return
	}

	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = "USD"
	}
	if currency != "USD" {
		http.Fail(c, 400, "Unsupported currency", errors.New("only USD is supported"))
		return
	}

	// Wire transfer: return instructions URL instead of creating a payment link.
	if strings.ToLower(strings.TrimSpace(req.ProviderHint)) == "wire" {
		sessionID := uuid.New().String()
		baseURL := strings.TrimSpace(os.Getenv("BASE_URL"))
		if baseURL == "" {
			baseURL = "https://api.hanzo.ai"
		}
		wireURL := baseURL + "/v1/checkout/wire/instructions"

		http.Render(c, 200, checkoutSessionResponse{
			CheckoutURL: wireURL,
			SessionID:   sessionID,
		})
		return
	}

	// Org is SOLELY the authenticated principal's — the route's
	// TokenRequired(Admin, Published) branch (service token / Published
	// storefront token) or the IAM identity middleware set it. There is NO
	// request-body/header/env org override on the mint path: one org source, so
	// a caller can only ever mint checkout for its OWN tenant. Fail closed.
	org, ok := authedOrg(c)
	if !ok {
		http.Fail(c, 401, "Authentication required",
			errors.New("no authenticated organization for checkout"))
		return
	}

	// The minted payment link's post-payment redirect MUST target one of the
	// org's OWN domains — otherwise a link carrying the org's brand and a real
	// charge becomes an open-redirect / phishing pivot.
	sites := websiteURLs(org)
	if !hostedcheckout.AllowedCheckoutRedirect(req.SuccessURL, org.Name, sites, c.Request.Host) {
		http.Fail(c, 400, "Invalid successUrl",
			errors.New("successUrl must be one of the organization's own domains"))
		return
	}
	if strings.TrimSpace(req.CancelURL) != "" &&
		!hostedcheckout.AllowedCheckoutRedirect(req.CancelURL, org.Name, sites, c.Request.Host) {
		http.Fail(c, 400, "Invalid cancelUrl",
			errors.New("cancelUrl must be one of the organization's own domains"))
		return
	}

	// Resolve coupon before any external calls so we can fail fast.
	cpn, err := resolveCoupon(c, org, req.CouponCode)
	if err != nil {
		http.Fail(c, 400, "Invalid coupon", err)
		return
	}

	// Load the org's real per-org catalog once. Items that reference a catalog
	// listing (productId/slug/sku) are priced from the listing SERVER-SIDE — the
	// client-supplied UnitPrice is never trusted. Items with no catalog reference
	// fall back to the legacy custom-hat pricing. An item that DOES reference the
	// catalog but matches no listing is rejected (400) — never silently mispriced.
	catalog := loadOrgCatalog(c, org)

	// Compute subtotal and line items (provider-agnostic).
	items := make([]checkoutLineItem, 0, len(req.Items))
	var subtotalCents int64
	for _, it := range req.Items {
		if it.Quantity <= 0 {
			http.Fail(c, 400, "Invalid quantity", fmt.Errorf("quantity must be > 0 for item '%s'", it.ID))
			return
		}
		var name string
		var amount int64
		if hasCatalogRef(it) {
			n, a, ok := catalogPrice(catalog, it)
			if !ok {
				http.Fail(c, 400, "Unknown product", fmt.Errorf("no priced listing for item '%s'", itemRef(it)))
				return
			}
			name, amount = n, a
		} else {
			name = safeName(it.Hat.Text)
			amount = hatPriceCents(it.Hat)
		}
		subtotalCents += amount * int64(it.Quantity)
		items = append(items, checkoutLineItem{Name: name, Quantity: it.Quantity, Amount: amount})
	}

	// Apply coupon discount.
	discountCents := applyDiscount(subtotalCents, cpn)
	finalCents := subtotalCents - discountCents

	// Select payment provider, honoring the SAME disabled-processor policy
	// SelectProcessor enforces (processor.DisabledByPolicy). Stripe is disabled
	// by default — "we do NOT use Stripe" — so neither an explicit stripe hint
	// nor a hydrated Stripe token routes a NEW checkout to Stripe; Square is
	// used. Stripe checkout code stays for historical / existing-customer flows.
	hint := strings.ToLower(strings.TrimSpace(req.ProviderHint))
	stripeAllowed := !processor.DisabledByPolicy(processor.Stripe)
	var sessionResp checkoutSessionResponse

	switch {
	case stripeAllowed && (hint == "stripe" || (hint == "" && org.StripeToken() != "")):
		sessionResp, err = createStripeCheckout(c, org, items, subtotalCents, discountCents, finalCents, cpn, currency, req)

	default:
		// Square Payment Links (default).
		sessionResp, err = createSquareCheckout(c, org, items, subtotalCents, discountCents, finalCents, cpn, currency, req)
	}

	if err != nil {
		http.Fail(c, 500, "Failed to create checkout session", err)
		return
	}

	if cpn != nil {
		sessionResp.OriginalTotal = subtotalCents
		sessionResp.FinalTotal = finalCents
		sessionResp.Discount = &couponDiscount{
			Code:          cpn.Code(),
			Type:          string(cpn.Type),
			Amount:        cpn.Amount,
			DiscountCents: discountCents,
		}
	}
	// Publish checkout.started to NATS/JetStream (fire and forget)
	if pub, ok := c.Get("publisher"); ok {
		if p, ok := pub.(*events.Publisher); ok {
			go func() {
				if pubErr := p.PublishCheckoutStarted(context.Background(), sessionResp.SessionID, org.Name, finalCents, currency); pubErr != nil {
					log.Error("PublishCheckoutStarted: %v", pubErr, c)
				}
			}()
		}
	}

	http.Render(c, 200, sessionResp)
}

// createStripeCheckout creates a Stripe Checkout Session using the org's Stripe credentials.
func createStripeCheckout(c *gin.Context, org *organization.Organization, items []checkoutLineItem, subtotalCents, discountCents, finalCents int64, cpn *coupon.Coupon, currency string, req checkoutSessionRequest) (checkoutSessionResponse, error) {
	sk := org.StripeToken()
	if sk == "" {
		return checkoutSessionResponse{}, errors.New("stripe is not configured for this organization")
	}

	// Build Stripe Checkout Session line_items
	params := url.Values{}
	params.Set("mode", "payment")
	params.Set("success_url", req.SuccessURL)
	if isValidRedirect(req.CancelURL) {
		params.Set("cancel_url", req.CancelURL)
	}
	if req.Customer.Email != "" {
		params.Set("customer_email", strings.TrimSpace(req.Customer.Email))
	}

	for i, it := range items {
		prefix := fmt.Sprintf("line_items[%d]", i)
		params.Set(prefix+"[price_data][currency]", strings.ToLower(currency))
		params.Set(prefix+"[price_data][unit_amount]", fmt.Sprintf("%d", it.Amount))
		params.Set(prefix+"[price_data][product_data][name]", it.Name)
		params.Set(prefix+"[quantity]", fmt.Sprintf("%d", it.Quantity))
	}

	// Apply coupon as a discount if present
	if discountCents > 0 && cpn != nil {
		// Create an inline coupon via discounts parameter
		params.Set("discounts[0][coupon]", "")
		// Use a promotion code or just adjust — Stripe Checkout doesn't support inline flat discounts
		// directly, so we add a negative line item or use automatic_tax. For simplicity, adjust via
		// a coupon created on-the-fly is complex. Instead, we adjust the unit amounts proportionally
		// or note the discount in metadata.
		params.Set("metadata[coupon_code]", cpn.Code())
		params.Set("metadata[discount_cents]", fmt.Sprintf("%d", discountCents))
		params.Del("discounts[0][coupon]")
	}

	params.Set("metadata[org]", org.Name)
	if req.ReferrerId != "" {
		params.Set("metadata[referrer_id]", req.ReferrerId)
	}
	if req.AffiliateId != "" {
		params.Set("metadata[affiliate_id]", req.AffiliateId)
	}

	stripeReq, err := nethttp.NewRequestWithContext(c.Request.Context(), nethttp.MethodPost,
		"https://api.stripe.com/v1/checkout/sessions",
		strings.NewReader(params.Encode()))
	if err != nil {
		return checkoutSessionResponse{}, fmt.Errorf("stripe request: %w", err)
	}
	stripeReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	stripeReq.SetBasicAuth(sk, "")

	resp, err := nethttp.DefaultClient.Do(stripeReq)
	if err != nil {
		return checkoutSessionResponse{}, fmt.Errorf("stripe API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return checkoutSessionResponse{}, fmt.Errorf("stripe read: %w", err)
	}

	if resp.StatusCode >= 400 {
		return checkoutSessionResponse{}, fmt.Errorf("stripe error %d: %s", resp.StatusCode, string(body))
	}

	var stripeResp struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &stripeResp); err != nil {
		return checkoutSessionResponse{}, fmt.Errorf("stripe parse: %w", err)
	}

	return checkoutSessionResponse{
		CheckoutURL: stripeResp.URL,
		SessionID:   stripeResp.ID,
	}, nil
}

// createSquareCheckout creates a Square Payment Link using the org's Square credentials.
func createSquareCheckout(c *gin.Context, org *organization.Organization, items []checkoutLineItem, subtotalCents, discountCents, finalCents int64, cpn *coupon.Coupon, currency string, req checkoutSessionRequest) (checkoutSessionResponse, error) {
	client, locationID, err := squareCheckoutClientForOrg(org)
	if err != nil {
		return checkoutSessionResponse{}, err
	}

	lineItems := make([]*square.OrderLineItem, 0, len(items))
	for _, it := range items {
		name := it.Name
		qty := fmt.Sprintf("%d", it.Quantity)
		amount := it.Amount
		cur := square.CurrencyUsd

		lineItems = append(lineItems, &square.OrderLineItem{
			Name:     &name,
			Quantity: qty,
			BasePriceMoney: &square.Money{
				Amount:   &amount,
				Currency: &cur,
			},
		})
	}

	referenceID := uuid.New().String()
	order := &square.Order{
		LocationID:  locationID,
		LineItems:   lineItems,
		ReferenceID: &referenceID,
	}

	if discountCents > 0 && cpn != nil {
		discountUID := "coupon-discount"
		discountName := strings.ToUpper(cpn.Code())
		discountType := square.OrderLineItemDiscountTypeFixedAmount
		discountScope := square.OrderLineItemDiscountScopeOrder
		cur := square.CurrencyUsd
		order.Discounts = []*square.OrderLineItemDiscount{
			{
				UID:   &discountUID,
				Name:  &discountName,
				Type:  &discountType,
				Scope: &discountScope,
				AmountMoney: &square.Money{
					Amount:   &discountCents,
					Currency: &cur,
				},
			},
		}
	}

	redirectURL := req.SuccessURL
	askShipping := false
	checkoutOptions := &square.CheckoutOptions{
		RedirectURL:           &redirectURL,
		AskForShippingAddress: &askShipping,
	}

	buyerEmail := strings.TrimSpace(req.Customer.Email)
	buyerName := strings.TrimSpace(req.Customer.FullName)
	buyerAddressLine1 := strings.TrimSpace(req.Customer.Address)
	buyerCity := strings.TrimSpace(req.Customer.City)
	buyerZip := strings.TrimSpace(req.Customer.Zip)

	var prePop *square.PrePopulatedData
	if buyerEmail != "" || buyerAddressLine1 != "" {
		prePop = &square.PrePopulatedData{
			BuyerEmail: &buyerEmail,
			BuyerAddress: &square.Address{
				FirstName:    &buyerName,
				AddressLine1: &buyerAddressLine1,
				Locality:     &buyerCity,
				PostalCode:   &buyerZip,
			},
		}
	}

	desc := strings.TrimSpace(req.Company)
	if desc == "" {
		desc = "Checkout"
	}
	idempotency := uuid.New().String()

	createReq := &sqcheckout.CreatePaymentLinkRequest{
		IdempotencyKey:   &idempotency,
		Description:      &desc,
		Order:            order,
		CheckoutOptions:  checkoutOptions,
		PrePopulatedData: prePop,
	}

	sqResp, err := client.Create(c.Request.Context(), createReq)
	if err != nil {
		return checkoutSessionResponse{}, fmt.Errorf("square API: %w", err)
	}
	if len(sqResp.Errors) > 0 {
		return checkoutSessionResponse{}, fmt.Errorf("square: %s", sqResp.Errors[0].String())
	}
	if sqResp.PaymentLink == nil || sqResp.PaymentLink.ID == nil || sqResp.PaymentLink.URL == nil {
		return checkoutSessionResponse{}, errors.New("square: missing payment link in response")
	}

	return checkoutSessionResponse{
		CheckoutURL: *sqResp.PaymentLink.URL,
		SessionID:   *sqResp.PaymentLink.ID,
	}, nil
}
