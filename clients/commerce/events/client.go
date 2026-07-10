// Package events provides a thin HTTP client for the analytics collector.
//
// Commerce fires events via HTTP to the analytics-collector sidecar
// rather than writing directly to ClickHouse. This decouples analytics
// from the commerce binary.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client sends events to the analytics-collector via HTTP.
type Client struct {
	endpoint   string
	httpClient *http.Client
}

// NewClient creates a new analytics client.
// Endpoint should be the analytics-collector base URL (e.g., "http://analytics-collector.hanzo.svc:8091").
func NewClient(endpoint string) *Client {
	return &Client{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Order represents a Commerce order for event emission.
type Order struct {
	ID       string
	UserID   string
	Email    string
	Total    float64
	Currency string
	Items    []OrderItem
	Status   string
	OrgID    string
}

// OrderItem represents an item in an order.
type OrderItem struct {
	ProductID   string
	ProductName string
	SKU         string
	Quantity    int
	Price       float64
}

// EmitOrderCompleted sends an order completed event to the collector.
func (c *Client) EmitOrderCompleted(ctx context.Context, order *Order) error {
	items := make([]map[string]interface{}, len(order.Items))
	var totalQuantity int
	for i, item := range order.Items {
		items[i] = map[string]interface{}{
			"product_id":   item.ProductID,
			"product_name": item.ProductName,
			"sku":          item.SKU,
			"quantity":     item.Quantity,
			"price":        item.Price,
		}
		totalQuantity += item.Quantity
	}

	itemsJSON, _ := json.Marshal(items)

	return c.EmitRaw(ctx, map[string]interface{}{
		"event":           "order_completed",
		"distinct_id":     order.UserID,
		"organization_id": order.OrgID,
		"order_id":        order.ID,
		"revenue":         order.Total,
		"quantity":        totalQuantity,
		"properties": map[string]interface{}{
			"currency":   order.Currency,
			"items":      string(itemsJSON),
			"item_count": len(order.Items),
			"status":     order.Status,
			"email":      order.Email,
		},
	})
}

// EmitReferralLinkCreated sends a referral link creation event to the collector.
func (c *Client) EmitReferralLinkCreated(ctx context.Context, orgID, userID, referralCode, referralURL string) error {
	return c.EmitRaw(ctx, map[string]interface{}{
		"event":           "referral_link_created",
		"distinct_id":     userID,
		"organization_id": orgID,
		"properties": map[string]interface{}{
			"referral_code": referralCode,
			"referral_url":  referralURL,
		},
	})
}

// EmitReferralClaimed sends a referral claimed event to the collector.
func (c *Client) EmitReferralClaimed(ctx context.Context, orgID, referrerID, refereeID, referralCode string) error {
	return c.EmitRaw(ctx, map[string]interface{}{
		"event":           "referral_claimed",
		"distinct_id":     refereeID,
		"organization_id": orgID,
		"properties": map[string]interface{}{
			"referrer_id":   referrerID,
			"referee_id":    refereeID,
			"referral_code": referralCode,
		},
	})
}

// EmitReferralCreditGranted sends a referral credit granted event to the collector.
func (c *Client) EmitReferralCreditGranted(ctx context.Context, orgID, userID, role string, amount float64, currency string) error {
	return c.EmitRaw(ctx, map[string]interface{}{
		"event":           "referral_credit_granted",
		"distinct_id":     userID,
		"organization_id": orgID,
		"revenue":         amount,
		"properties": map[string]interface{}{
			"role":     role,
			"amount":   amount,
			"currency": currency,
		},
	})
}

// EmitReferralCommissionEarned sends a referral commission event to the collector.
func (c *Client) EmitReferralCommissionEarned(ctx context.Context, orgID, referrerID, orderID string, commission float64, currency string) error {
	return c.EmitRaw(ctx, map[string]interface{}{
		"event":           "referral_commission_earned",
		"distinct_id":     referrerID,
		"organization_id": orgID,
		"order_id":        orderID,
		"revenue":         commission,
		"properties": map[string]interface{}{
			"order_id":   orderID,
			"commission": commission,
			"currency":   currency,
		},
	})
}

// EmitReferralTierUpgraded sends a referral tier upgrade event to the collector.
func (c *Client) EmitReferralTierUpgraded(ctx context.Context, orgID, userID, previousTier, newTier string, referralCount int) error {
	return c.EmitRaw(ctx, map[string]interface{}{
		"event":           "referral_tier_upgraded",
		"distinct_id":     userID,
		"organization_id": orgID,
		"properties": map[string]interface{}{
			"previous_tier":  previousTier,
			"new_tier":       newTier,
			"referral_count": referralCount,
		},
	})
}

// EmitContributorRegistered sends a contributor registration event to the collector.
func (c *Client) EmitContributorRegistered(ctx context.Context, orgID, userID, githubUsername string) error {
	return c.EmitRaw(ctx, map[string]interface{}{
		"event":           "contributor_registered",
		"distinct_id":     userID,
		"organization_id": orgID,
		"properties": map[string]interface{}{
			"github_username": githubUsername,
		},
	})
}

// EmitContributorPayoutCalculated sends a payout calculation event to the collector.
func (c *Client) EmitContributorPayoutCalculated(ctx context.Context, orgID, userID, periodMonth string, amount float64, currency string) error {
	return c.EmitRaw(ctx, map[string]interface{}{
		"event":           "contributor_payout_calculated",
		"distinct_id":     userID,
		"organization_id": orgID,
		"revenue":         amount,
		"properties": map[string]interface{}{
			"period_month": periodMonth,
			"amount":       amount,
			"currency":     currency,
		},
	})
}

// EmitContributorPayoutSent sends a payout sent event to the collector.
func (c *Client) EmitContributorPayoutSent(ctx context.Context, orgID, userID, payoutID, periodMonth string, amount float64, currency string) error {
	return c.EmitRaw(ctx, map[string]interface{}{
		"event":           "contributor_payout_sent",
		"distinct_id":     userID,
		"organization_id": orgID,
		"revenue":         amount,
		"properties": map[string]interface{}{
			"payout_id":    payoutID,
			"period_month": periodMonth,
			"amount":       amount,
			"currency":     currency,
		},
	})
}

// EmitRaw sends a raw event to the collector.
func (c *Client) EmitRaw(ctx context.Context, event map[string]interface{}) error {
	if c.endpoint == "" {
		return nil // No collector configured, silently skip
	}

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/event", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send event: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("collector error: status %d", resp.StatusCode)
	}
	return nil
}

// Flush is a no-op for the HTTP client (collector handles batching).
func (c *Client) Flush() error { return nil }

// Close is a no-op for the HTTP client.
func (c *Client) Close() error { return nil }
