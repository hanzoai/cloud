package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/infra"
)

// Publisher sends commerce events to NATS/JetStream.
type Publisher struct {
	pubsub *infra.PubSubClient
}

// NewPublisher creates a new event publisher. Returns nil if pubsub is nil.
func NewPublisher(pubsub *infra.PubSubClient) *Publisher {
	if pubsub == nil {
		return nil
	}
	return &Publisher{pubsub: pubsub}
}

// CommerceEvent is the standard envelope for all commerce events.
type CommerceEvent struct {
	ID             string                 `json:"id"`
	Type           string                 `json:"type"`
	Timestamp      time.Time              `json:"timestamp"`
	OrganizationID string                 `json:"organization_id"`
	UserID         string                 `json:"user_id,omitempty"`
	SessionID      string                 `json:"session_id,omitempty"`
	Data           map[string]interface{} `json:"data"`
	GA4            *GA4EcommerceEvent      `json:"ga4,omitempty"`
	FacebookCAPI   *FacebookCAPIEvent     `json:"facebook_capi,omitempty"`
}

// GA4EcommerceEvent holds GA4 Enhanced Ecommerce format.
type GA4EcommerceEvent struct {
	EventName  string                 `json:"event_name"`
	Currency   string                 `json:"currency,omitempty"`
	Value      float64                `json:"value,omitempty"`
	Items      []GA4Item              `json:"items,omitempty"`
	Parameters map[string]interface{} `json:"parameters,omitempty"`
}

// GA4Item represents a single item in GA4 Enhanced Ecommerce format.
type GA4Item struct {
	ItemID       string  `json:"item_id"`
	ItemName     string  `json:"item_name"`
	ItemBrand    string  `json:"item_brand,omitempty"`
	ItemCategory string  `json:"item_category,omitempty"`
	Price        float64 `json:"price"`
	Quantity     int     `json:"quantity"`
	Currency     string  `json:"currency,omitempty"`
}

// FacebookCAPIEvent holds Facebook Conversions API format.
type FacebookCAPIEvent struct {
	EventName    string                 `json:"event_name"`
	EventTime    int64                  `json:"event_time"`
	ActionSource string                 `json:"action_source"`
	UserData     *FacebookUserData      `json:"user_data,omitempty"`
	CustomData   map[string]interface{} `json:"custom_data,omitempty"`
}

// FacebookUserData for CAPI user matching.
// Email and Phone are SHA256-hashed per Facebook CAPI spec.
type FacebookUserData struct {
	Email           string `json:"em,omitempty"`
	Phone           string `json:"ph,omitempty"`
	ExternalID      string `json:"external_id,omitempty"`
	ClientIPAddress string `json:"client_ip_address,omitempty"`
	ClientUserAgent string `json:"client_user_agent,omitempty"`
	FBC             string `json:"fbc,omitempty"`
	FBP             string `json:"fbp,omitempty"`
}

// LineItemInfo holds minimal line item data for event publishing.
// Avoids importing the lineitem package into the events package.
type LineItemInfo struct {
	ProductID   string
	ProductName string
	SKU         string
	Quantity    int
	PriceCents  int64
}

// ToOrderItems converts LineItemInfo slices to OrderItems for event publishing.
func ToOrderItems(items []LineItemInfo) []OrderItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]OrderItem, len(items))
	for i, item := range items {
		out[i] = OrderItem{
			ProductID:   item.ProductID,
			ProductName: item.ProductName,
			SKU:         item.SKU,
			Quantity:    item.Quantity,
			Price:       float64(item.PriceCents) / 100.0,
		}
	}
	return out
}

// Publish sends an event to the appropriate NATS subject via JetStream.
func (p *Publisher) Publish(ctx context.Context, subject string, event *CommerceEvent) error {
	if p == nil || p.pubsub == nil {
		return nil
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	_, err = p.pubsub.PublishToStream(ctx, subject, data)
	if err != nil {
		return fmt.Errorf("publish to stream %q: %w", subject, err)
	}

	return nil
}

// PublishOrderCreated sends an order.created event after authorization.
func (p *Publisher) PublishOrderCreated(ctx context.Context, orderID, orgName, userID, email string, totalCents int64, currencyCode string, items []OrderItem) error {
	if p == nil {
		return nil
	}

	if currencyCode == "" {
		currencyCode = "USD"
	}

	revenue := float64(totalCents) / 100.0

	ga4Items := make([]GA4Item, len(items))
	for i, item := range items {
		ga4Items[i] = GA4Item{
			ItemID:   item.ProductID,
			ItemName: item.ProductName,
			Price:    item.Price,
			Quantity: item.Quantity,
			Currency: currencyCode,
		}
	}

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             orderID,
		Type:           "order.created",
		Timestamp:      now,
		OrganizationID: orgName,
		UserID:         userID,
		Data: map[string]interface{}{
			"order_id": orderID,
			"revenue":  revenue,
			"currency": currencyCode,
			"email":    email,
		},
		GA4: &GA4EcommerceEvent{
			EventName: "begin_checkout",
			Currency:  currencyCode,
			Value:     revenue,
			Items:     ga4Items,
			Parameters: map[string]interface{}{
				"transaction_id": orderID,
			},
		},
		FacebookCAPI: &FacebookCAPIEvent{
			EventName:    "InitiateCheckout",
			EventTime:    now.Unix(),
			ActionSource: "website",
			UserData: &FacebookUserData{
				Email:      SHA256Hex(email),
				ExternalID: userID,
			},
			CustomData: map[string]interface{}{
				"currency": currencyCode,
				"value":    revenue,
				"order_id": orderID,
			},
		},
	}

	return p.Publish(ctx, SubjectOrderCreated, event)
}

// PublishOrderCompleted sends an order.completed event after capture/payment.
func (p *Publisher) PublishOrderCompleted(ctx context.Context, orderID, orgName, userID, email string, totalCents int64, currencyCode string, items []OrderItem) error {
	if p == nil {
		return nil
	}

	if currencyCode == "" {
		currencyCode = "USD"
	}

	revenue := float64(totalCents) / 100.0

	ga4Items := make([]GA4Item, len(items))
	for i, item := range items {
		ga4Items[i] = GA4Item{
			ItemID:   item.ProductID,
			ItemName: item.ProductName,
			Price:    item.Price,
			Quantity: item.Quantity,
			Currency: currencyCode,
		}
	}

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             orderID,
		Type:           "order.completed",
		Timestamp:      now,
		OrganizationID: orgName,
		UserID:         userID,
		Data: map[string]interface{}{
			"order_id": orderID,
			"revenue":  revenue,
			"currency": currencyCode,
			"email":    email,
		},
		GA4: &GA4EcommerceEvent{
			EventName: "purchase",
			Currency:  currencyCode,
			Value:     revenue,
			Items:     ga4Items,
			Parameters: map[string]interface{}{
				"transaction_id": orderID,
			},
		},
		FacebookCAPI: &FacebookCAPIEvent{
			EventName:    "Purchase",
			EventTime:    now.Unix(),
			ActionSource: "website",
			UserData: &FacebookUserData{
				Email:      SHA256Hex(email),
				ExternalID: userID,
			},
			CustomData: map[string]interface{}{
				"currency": currencyCode,
				"value":    revenue,
				"order_id": orderID,
			},
		},
	}

	return p.Publish(ctx, SubjectOrderCompleted, event)
}

// PublishOrderRefunded sends an order.refunded event.
func (p *Publisher) PublishOrderRefunded(ctx context.Context, orderID, orgName, userID string, refundedCents int64, currencyCode string) error {
	if p == nil {
		return nil
	}

	if currencyCode == "" {
		currencyCode = "USD"
	}

	refundedAmount := float64(refundedCents) / 100.0

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             orderID,
		Type:           "order.refunded",
		Timestamp:      now,
		OrganizationID: orgName,
		UserID:         userID,
		Data: map[string]interface{}{
			"order_id":        orderID,
			"refunded_amount": refundedAmount,
			"currency":        currencyCode,
		},
		GA4: &GA4EcommerceEvent{
			EventName: "refund",
			Currency:  currencyCode,
			Value:     refundedAmount,
			Parameters: map[string]interface{}{
				"transaction_id": orderID,
			},
		},
		FacebookCAPI: &FacebookCAPIEvent{
			EventName:    "Refund",
			EventTime:    now.Unix(),
			ActionSource: "website",
			UserData: &FacebookUserData{
				ExternalID: userID,
			},
			CustomData: map[string]interface{}{
				"currency":        currencyCode,
				"value":           refundedAmount,
				"order_id":        orderID,
			},
		},
	}

	return p.Publish(ctx, SubjectOrderRefunded, event)
}

// PublishReferralLinkCreated sends a referral.link_created event.
func (p *Publisher) PublishReferralLinkCreated(ctx context.Context, orgID, userID, referralCode, referralURL string) error {
	if p == nil {
		return nil
	}

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             referralCode,
		Type:           "referral.link_created",
		Timestamp:      now,
		OrganizationID: orgID,
		UserID:         userID,
		Data: map[string]interface{}{
			"referral_code": referralCode,
			"referral_url":  referralURL,
		},
	}
	return p.Publish(ctx, SubjectReferralLinkCreated, event)
}

// PublishReferralClaimed sends a referral.claimed event when a new user signs up via referral.
func (p *Publisher) PublishReferralClaimed(ctx context.Context, orgID, referrerID, refereeID, referralCode string) error {
	if p == nil {
		return nil
	}

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             refereeID,
		Type:           "referral.claimed",
		Timestamp:      now,
		OrganizationID: orgID,
		UserID:         refereeID,
		Data: map[string]interface{}{
			"referrer_id":   referrerID,
			"referee_id":    refereeID,
			"referral_code": referralCode,
		},
	}
	return p.Publish(ctx, SubjectReferralClaimed, event)
}

// PublishReferralCreditGranted sends a referral.credit_granted event when credits are issued.
func (p *Publisher) PublishReferralCreditGranted(ctx context.Context, orgID, userID, role string, amountCents int64, currencyCode string) error {
	if p == nil {
		return nil
	}

	if currencyCode == "" {
		currencyCode = "USD"
	}

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             fmt.Sprintf("credit-%s-%d", userID, now.UnixMilli()),
		Type:           "referral.credit_granted",
		Timestamp:      now,
		OrganizationID: orgID,
		UserID:         userID,
		Data: map[string]interface{}{
			"role":     role, // "referrer" or "referee"
			"amount":   float64(amountCents) / 100.0,
			"currency": currencyCode,
		},
	}
	return p.Publish(ctx, SubjectReferralCreditGranted, event)
}

// PublishReferralCommissionEarned sends a referral.commission_earned event when a revenue share fee is created.
func (p *Publisher) PublishReferralCommissionEarned(ctx context.Context, orgID, referrerID, orderID string, commissionCents int64, currencyCode string) error {
	if p == nil {
		return nil
	}

	if currencyCode == "" {
		currencyCode = "USD"
	}

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             fmt.Sprintf("commission-%s-%s", referrerID, orderID),
		Type:           "referral.commission_earned",
		Timestamp:      now,
		OrganizationID: orgID,
		UserID:         referrerID,
		Data: map[string]interface{}{
			"order_id":   orderID,
			"commission": float64(commissionCents) / 100.0,
			"currency":   currencyCode,
		},
	}
	return p.Publish(ctx, SubjectReferralCommissionEarned, event)
}

// PublishReferralTierUpgraded sends a referral.tier_upgraded event when a referrer reaches a new tier.
func (p *Publisher) PublishReferralTierUpgraded(ctx context.Context, orgID, userID, previousTier, newTier string, referralCount int) error {
	if p == nil {
		return nil
	}

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             fmt.Sprintf("tier-%s-%d", userID, now.UnixMilli()),
		Type:           "referral.tier_upgraded",
		Timestamp:      now,
		OrganizationID: orgID,
		UserID:         userID,
		Data: map[string]interface{}{
			"previous_tier":  previousTier,
			"new_tier":       newTier,
			"referral_count": referralCount,
		},
	}
	return p.Publish(ctx, SubjectReferralTierUpgraded, event)
}

// PublishContributorRegistered sends a contributor.registered event.
func (p *Publisher) PublishContributorRegistered(ctx context.Context, orgID, userID, githubUsername string) error {
	if p == nil {
		return nil
	}

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             userID,
		Type:           "contributor.registered",
		Timestamp:      now,
		OrganizationID: orgID,
		UserID:         userID,
		Data: map[string]interface{}{
			"github_username": githubUsername,
		},
	}
	return p.Publish(ctx, SubjectContributorRegistered, event)
}

// PublishContributorPayoutCalculated sends a contributor.payout_calculated event.
func (p *Publisher) PublishContributorPayoutCalculated(ctx context.Context, orgID, userID, periodMonth string, amountCents int64, currencyCode string) error {
	if p == nil {
		return nil
	}

	if currencyCode == "" {
		currencyCode = "USD"
	}

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             fmt.Sprintf("payout-calc-%s-%s", userID, periodMonth),
		Type:           "contributor.payout_calculated",
		Timestamp:      now,
		OrganizationID: orgID,
		UserID:         userID,
		Data: map[string]interface{}{
			"period_month": periodMonth,
			"amount":       float64(amountCents) / 100.0,
			"currency":     currencyCode,
		},
	}
	return p.Publish(ctx, SubjectContributorPayoutCalc, event)
}

// PublishContributorPayoutSent sends a contributor.payout_sent event when payout is transferred.
func (p *Publisher) PublishContributorPayoutSent(ctx context.Context, orgID, userID, payoutID, periodMonth string, amountCents int64, currencyCode string) error {
	if p == nil {
		return nil
	}

	if currencyCode == "" {
		currencyCode = "USD"
	}

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             payoutID,
		Type:           "contributor.payout_sent",
		Timestamp:      now,
		OrganizationID: orgID,
		UserID:         userID,
		Data: map[string]interface{}{
			"payout_id":    payoutID,
			"period_month": periodMonth,
			"amount":       float64(amountCents) / 100.0,
			"currency":     currencyCode,
		},
	}
	return p.Publish(ctx, SubjectContributorPayoutSent, event)
}

// PublishCheckoutStarted sends a checkout.started event for hosted sessions.
func (p *Publisher) PublishCheckoutStarted(ctx context.Context, sessionID, orgName string, totalCents int64, currencyCode string) error {
	if p == nil {
		return nil
	}

	if currencyCode == "" {
		currencyCode = "USD"
	}

	value := float64(totalCents) / 100.0

	now := time.Now().UTC()
	event := &CommerceEvent{
		ID:             sessionID,
		Type:           "checkout.started",
		Timestamp:      now,
		OrganizationID: orgName,
		Data: map[string]interface{}{
			"session_id": sessionID,
			"value":      value,
			"currency":   currencyCode,
		},
		GA4: &GA4EcommerceEvent{
			EventName: "begin_checkout",
			Currency:  currencyCode,
			Value:     value,
		},
		FacebookCAPI: &FacebookCAPIEvent{
			EventName:    "InitiateCheckout",
			EventTime:    now.Unix(),
			ActionSource: "website",
			CustomData: map[string]interface{}{
				"currency": currencyCode,
				"value":    value,
			},
		},
	}

	return p.Publish(ctx, SubjectCheckoutStarted, event)
}
