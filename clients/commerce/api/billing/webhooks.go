package billing

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/billingevent"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
	// Blank-import the provider barrel so every provider's init() registers
	// with processor.Global() before HandleProviderWebhook runs. Without this
	// the global registry is empty and tryValidateWebhook can never reach any
	// provider's ValidateWebhook (the per-org payment.ProcessorsForOrg path is
	// separate and unaffected). This is the single owning import for the
	// generic webhook dispatcher; do not scatter barrel imports elsewhere.
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers"
	jsonhttp "github.com/hanzoai/cloud/clients/commerce/util/json/http"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// HandleProviderWebhook is the single ingress for payment-provider webhooks.
// It dispatches to the matching processor in payment/router, validates the
// signature, records the event in billing_events, and — for subscription
// lifecycle events — updates the local subscription row keyed by ProviderId.
//
//	POST /v1/billing/webhooks/:provider
//
// The :provider path segment is informational; signature verification picks
// the right processor regardless. We pass the path segment as a lightweight
// filter so webhook endpoints are URL-scoped per-provider (easier in Stripe
// dashboard configuration).
func HandleProviderWebhook(c *gin.Context) {
	providerHint := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		jsonhttp.Fail(c, http.StatusBadRequest, "cannot read request body", err)
		return
	}

	// Every provider puts its signature in a different header; let the router
	// try each processor with the one most likely to match.
	signature := pickSignatureHeader(c.Request.Header, providerHint)
	if signature == "" {
		jsonhttp.Fail(c, http.StatusBadRequest, "missing webhook signature header", nil)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	event, err := tryValidateWebhook(ctx, providerHint, payload, signature)
	if err != nil || event == nil {
		log.Warn("webhook signature validation failed (provider hint=%s): %v", providerHint, err)
		jsonhttp.Fail(c, http.StatusUnauthorized, "invalid webhook signature", err)
		return
	}

	// Persist the raw event so the app has an audit trail independent of
	// processor-side retention. Webhooks arrive with no session, so resolve
	// the owning org the same way service-token calls do.
	org := resolveWebhookOrg(c)
	if org == nil {
		jsonhttp.Fail(c, http.StatusServiceUnavailable, "organization context unavailable", nil)
		return
	}
	db := datastore.New(org.Namespaced(c))

	// Idempotency: providers retry aggressively (Square retries for up to
	// 72h until it gets a 2xx). If we already recorded this event ID, ack
	// without re-applying side effects.
	if event.ID != "" && eventAlreadyProcessed(db, providerHint, event.ID) {
		c.JSON(http.StatusOK, gin.H{
			"received":  true,
			"type":      event.Type,
			"id":        event.ID,
			"duplicate": true,
		})
		return
	}

	evt := billingevent.New(db)
	evt.Type = event.Type
	evt.ObjectType = providerHint
	evt.ObjectId = event.ID
	evt.Livemode = !org.TestMode() // livemode follows the deployment Square env, not the forced org.Live
	if event.Data != nil {
		evt.Data = event.Data
	}
	if err := evt.Create(); err != nil {
		log.Warn("failed to persist billing event %s: %v", event.ID, err)
		// Do not 500 — event was validated; duplicate persistence is fine.
	}

	// Update local subscription state for lifecycle events.
	if strings.HasPrefix(event.Type, "subscription.") || strings.HasPrefix(event.Type, "invoice.") {
		applySubscriptionEvent(db, event)
	}

	// Settle funds for completed payments. This is the path Square's
	// payment.updated(status=COMPLETED) callback exercises once a card
	// authorization captures — the moment funds are guaranteed.
	if isSettlementEvent(event.Type) {
		applySettlementEvent(db, org, event)
	}

	c.JSON(http.StatusOK, gin.H{
		"received": true,
		"type":     event.Type,
		"id":       event.ID,
	})
}

// resolveWebhookOrg determines which org a sessionless provider webhook
// belongs to. Order: X-Org-Id header, COMMERCE_SERVICE_ORG env, then the
// default "hanzo" org. Mirrors the service-token resolution in
// middleware/accesstoken.go so webhooks and service calls agree on scope.
func resolveWebhookOrg(c *gin.Context) *organization.Organization {
	if org := middleware.GetOrganization(c); org != nil {
		return org
	}
	db := datastore.New(middleware.GetContext(c))
	orgName := strings.TrimSpace(c.GetHeader("X-Org-Id"))
	if orgName == "" {
		orgName = strings.TrimSpace(os.Getenv("COMMERCE_SERVICE_ORG"))
	}
	if orgName == "" {
		orgName = "hanzo"
	}
	// A webhook caller must not be able to provision an org named after a raw
	// API key: reject a bearer-shaped selector before GetOrCreate (incident
	// 2026-07-02).
	if organization.IsSecretLikeName(orgName) {
		log.Warn("webhook org resolve: bearer-shaped org selector; refusing to provision")
		return nil
	}
	org := organization.New(db)
	org.Name = orgName
	if err := org.GetOrCreate("Name=", orgName); err != nil {
		log.Warn("webhook org resolve for '%s' failed: %v", orgName, err)
		return nil
	}
	// Sessionless webhooks carry no per-org test flag, so default to live. This is
	// SAFE for the ledger because settlement test-ness is taken from org.TestMode,
	// where SQUARE_ENVIRONMENT (the deployment authority) overrides this flag: on a
	// SQUARE_ENVIRONMENT=sandbox deploy, TestMode()=true regardless, so a sandbox
	// settlement still credits the TEST bucket — never live (no free-money path).
	org.Live = true
	return org
}

// eventAlreadyProcessed reports whether a billing event with this provider
// object type and ID was already recorded — the idempotency guard against
// provider retries.
func eventAlreadyProcessed(db *datastore.Datastore, objectType, eventID string) bool {
	existing := billingevent.New(db)
	found, err := existing.Query().
		Filter("ObjectType=", objectType).
		Filter("ObjectId=", eventID).
		Get()
	return err == nil && found
}

// isSettlementEvent reports whether an event represents settled (captured)
// funds that should credit a balance.
func isSettlementEvent(eventType string) bool {
	switch eventType {
	case "payment.completed", "payment.updated", "invoice.paid":
		return true
	default:
		return false
	}
}

// applySettlementEvent credits the destination balance for a settled
// payment. It is idempotent: the credit transaction is keyed by the
// provider payment ID (SourceId), so re-delivery never double-credits.
//
// A "payment.updated" event only settles when the payment object reports a
// terminal completed/captured status; pending or failed updates are ignored.
func applySettlementEvent(db *datastore.Datastore, org *organization.Organization, event *processor.WebhookEvent) {
	if event.Data == nil {
		return
	}

	// Square nests the changed resource under data.object.<kind> (e.g.
	// data.object.payment for payment.* events). The processor passes
	// data.object through as event.Data, so unwrap the payment object here.
	// Fall back to event.Data itself for processors that put fields at the
	// top level.
	pay := unwrapObject(event.Data, "payment")

	paymentID := stringField(pay, "id")
	if paymentID == "" {
		return
	}

	// Only act on terminal success. Square reports COMPLETED/CAPTURED; other
	// processors use lowercase. Treat payment.completed as already terminal.
	if event.Type == "payment.updated" {
		switch strings.ToUpper(stringField(pay, "status")) {
		case "COMPLETED", "CAPTURED", "APPROVED":
			// settled — continue
		default:
			return
		}
	}

	// Resolve the user this payment funds. Square carries our identifier in
	// reference_id (set to the order/user at charge time); fall back to the
	// customer object when present.
	user := firstNonEmpty(
		stringField(pay, "reference_id"),
		stringField(pay, "referenceId"),
		stringField(pay, "customer_id"),
		stringField(pay, "customerId"),
	)
	if user == "" {
		log.Warn("settlement %s: no user reference on payment, recorded event only", paymentID)
		return
	}

	// Idempotency: one deposit per provider payment ID.
	dup := transaction.New(db)
	if found, err := dup.Query().
		Filter("SourceKind=", "square-payment").
		Filter("SourceId=", paymentID).
		Get(); err == nil && found {
		return
	}

	amount, cur := settlementAmount(pay)
	if amount <= 0 {
		log.Warn("settlement %s: non-positive amount, recorded event only", paymentID)
		return
	}

	trans := transaction.New(db)
	trans.Type = transaction.Deposit
	trans.DestinationId = strings.ToLower(user)
	trans.DestinationKind = "iam-user"
	trans.Currency = cur
	trans.Amount = amount
	trans.SourceId = paymentID
	trans.SourceKind = "square-payment"
	trans.Event = event.ID
	trans.Notes = "Square settlement " + paymentID
	trans.Metadata = Map{"provider": string(event.Processor), "eventType": event.Type}
	// Settlement test-ness follows the deployment Square environment (org.TestMode),
	// NOT the resolveWebhookOrg forced-live flag: on a SQUARE_ENVIRONMENT=sandbox
	// deploy a sandbox settlement credits the TEST bucket, never the live one.
	trans.Test = org.TestMode()
	// A provider-signature-verified settlement for a real captured payment IS the
	// mint authority (the trust anchor is the per-provider signature checked in
	// HandleProviderWebhook), so authorize this write at the ledger sink.
	trans.SetContext(mintauth.WithAuthorized(trans.Context()))
	if err := trans.Create(); err != nil {
		log.Warn("settlement %s: failed to credit balance: %v", paymentID, err)
	}
}

// settlementAmount extracts the captured amount (cents) and currency from a
// provider payment object. Square nests it under amount_money{amount,currency}.
func settlementAmount(data Map) (currency.Cents, currency.Type) {
	cur := currency.USD
	if m, ok := data["amount_money"].(map[string]interface{}); ok {
		amt := numberField(m, "amount")
		if c := stringField(m, "currency"); c != "" {
			cur = currency.Type(strings.ToLower(c))
		}
		return currency.Cents(amt), cur
	}
	// Flat fallbacks used by other processors.
	if c := stringField(data, "currency"); c != "" {
		cur = currency.Type(strings.ToLower(c))
	}
	return currency.Cents(numberField(data, "amount")), cur
}

// unwrapObject returns data[key] as a map when present (Square nests the
// changed resource one level deep, e.g. data.object.payment), otherwise the
// data map itself (processors that expose fields at the top level).
func unwrapObject(data Map, key string) map[string]interface{} {
	if inner, ok := data[key].(map[string]interface{}); ok {
		return inner
	}
	return data
}

func stringField(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func numberField(m map[string]interface{}, key string) int64 {
	switch n := m[key].(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// applySubscriptionEvent reconciles the local subscription row with a
// lifecycle event from the payment provider.
func applySubscriptionEvent(db *datastore.Datastore, event *processor.WebhookEvent) {
	// Payload contains the Stripe subscription object — look for "id".
	id, _ := event.Data["id"].(string)
	if id == "" {
		return
	}

	sub := subscription.New(db)
	found, err := sub.Query().Filter("ProviderId=", id).Get()
	if err != nil || !found {
		// Unknown subscription — likely created outside commerce.
		return
	}

	if status, ok := event.Data["status"].(string); ok && status != "" {
		sub.Status = subscription.Status(status)
	}
	if event.Type == "subscription.canceled" {
		sub.Canceled = true
		sub.CanceledAt = time.Now().UTC()
	}
	if err := sub.Update(); err != nil {
		log.Warn("webhook: failed to update subscription %s: %v", sub.Id(), err)
	}
}

// tryValidateWebhook walks registered payment processors looking for one that
// validates the signature. If providerHint is non-empty, we try that processor
// first (fast path); otherwise we iterate all available processors.
func tryValidateWebhook(ctx context.Context, providerHint string, payload []byte, signature string) (*processor.WebhookEvent, error) {
	registry := processor.Global()

	// Inbound validation ignores the disabled-processor deny policy (which
	// governs OUTBOUND charge selection only): a processor disabled for new
	// charges — e.g. Stripe — must still validate its existing customers' webhook
	// deliveries. Hence Registered / RegisteredProcessors, not Get / Available.

	// Fast path: provider hint specified.
	if providerHint != "" {
		if p, err := registry.Registered(processor.ProcessorType(providerHint)); err == nil {
			return p.ValidateWebhook(ctx, payload, signature)
		}
	}

	// Fallback: try every registered, configured processor until one succeeds.
	var lastErr error
	for _, p := range registry.RegisteredProcessors() {
		if !p.IsAvailable(ctx) {
			continue
		}
		evt, err := p.ValidateWebhook(ctx, payload, signature)
		if err == nil && evt != nil {
			return evt, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// pickSignatureHeader returns the signature header for the given provider.
// We check the most common header names regardless of hint so a misconfigured
// Stripe endpoint (e.g. /webhooks/paypal) still validates correctly.
func pickSignatureHeader(h http.Header, providerHint string) string {
	candidates := []string{
		"Stripe-Signature",
		"X-Square-Hmacsha256-Signature", // Square (HMAC-SHA256 over notificationURL+body)
		"Paypal-Transmission-Sig",
		"X-Adyen-Signature",
		"X-Paypal-Auth-Algo",
		"X-CC-Webhook-Signature", // Coinbase Commerce
		"X-Signature",
	}
	if providerHint != "" {
		// Try a provider-specific guess first.
		switch providerHint {
		case "stripe":
			if v := h.Get("Stripe-Signature"); v != "" {
				return v
			}
		case "square":
			if v := h.Get("X-Square-Hmacsha256-Signature"); v != "" {
				return v
			}
		case "paypal":
			if v := h.Get("Paypal-Transmission-Sig"); v != "" {
				return v
			}
		case "coinbase":
			if v := h.Get("X-CC-Webhook-Signature"); v != "" {
				return v
			}
		}
	}
	for _, name := range candidates {
		if v := h.Get(name); v != "" {
			return v
		}
	}
	return ""
}
