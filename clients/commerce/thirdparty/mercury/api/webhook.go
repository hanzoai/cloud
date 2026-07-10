package api

import (
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mercury"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

var (
	ErrSignatureInvalid = errors.New("invalid Mercury webhook signature")
	ErrPayloadRead      = errors.New("failed to read webhook payload")
	ErrPayloadParse     = errors.New("failed to parse webhook payload")
)

// Webhook handles Mercury bank webhook POSTs.
//
// POST /api/v1/mercury/webhook
//
// On transaction.created with direction="credit":
//  1. Verify HMAC signature via Mercury-Signature header
//  2. Parse reference from externalMemo or note (format: "orgName:orderId")
//  3. Look up the order in the org namespace
//  4. Mark order as paid if it is pending
//
// Returns 200 OK to Mercury regardless of processing outcome.
func Webhook(c *gin.Context) {
	// Read raw body for signature verification.
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Error("Mercury webhook: %v", ErrPayloadRead, c)
		c.String(200, "ok")
		return
	}

	// Verify signature.
	signature := c.GetHeader("Mercury-Signature")
	secret := config.Mercury.WebhookSecret
	if secret != "" && !mercury.VerifySignature(body, signature, secret) {
		log.Error("Mercury webhook: %v", ErrSignatureInvalid, c)
		c.String(200, "ok")
		return
	}

	// Parse payload.
	var payload mercury.WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Error("Mercury webhook: %v: %v", ErrPayloadParse, err, c)
		c.String(200, "ok")
		return
	}

	log.Info("Mercury webhook received: eventType=%s resourceId=%s",
		payload.EventType, payload.ResourceID, c)

	// Only process incoming credits on transaction.created.
	if payload.EventType != "transaction.created" {
		c.String(200, "ok")
		return
	}

	tx := payload.Data
	if tx.Direction != "credit" {
		c.String(200, "ok")
		return
	}

	// Extract order reference from externalMemo or note.
	// Expected format: "orgName:orderId"
	ref := tx.ExternalMemo
	if ref == "" {
		ref = tx.Note
	}
	if ref == "" {
		log.Info("Mercury webhook: credit transaction %s has no reference, skipping", tx.ID, c)
		c.String(200, "ok")
		return
	}

	orgName, orderID := parseReference(ref)
	if orgName == "" || orderID == "" {
		log.Info("Mercury webhook: could not parse reference %q from transaction %s", ref, tx.ID, c)
		c.String(200, "ok")
		return
	}

	// Create a namespaced datastore for the org.
	ctx := nscontext.WithNamespace(c.Request.Context(), orgName)
	db := datastore.New(ctx)

	// Look up the order.
	ord := order.New(db)
	if err := ord.GetById(orderID); err != nil {
		log.Error("Mercury webhook: order %s not found in org %s: %v", orderID, orgName, err, c)
		c.String(200, "ok")
		return
	}

	// Only credit orders that are still awaiting payment.
	if ord.PaymentStatus != payment.Unpaid {
		log.Info("Mercury webhook: order %s already has payment status %s, skipping",
			orderID, ord.PaymentStatus, c)
		c.String(200, "ok")
		return
	}

	// Mark order as paid (same logic as wire credit handler).
	ord.Status = order.Open
	ord.PaymentStatus = payment.Paid
	if err := ord.Put(); err != nil {
		log.Error("Mercury webhook: failed to update order %s: %v", orderID, err, c)
		c.String(200, "ok")
		return
	}

	log.Info("Mercury webhook: credited order=%s org=%s amount=%.2f txId=%s counterparty=%s",
		orderID, orgName, tx.Amount, tx.ID, tx.CounterpartyName, c)

	c.String(200, "ok")
}

// parseReference splits a wire reference string into org name and order ID.
// Accepted formats:
//
//	"orgName:orderId"
//	"orderId" (returns empty orgName)
func parseReference(ref string) (orgName, orderID string) {
	ref = strings.TrimSpace(ref)
	if idx := strings.Index(ref, ":"); idx > 0 && idx < len(ref)-1 {
		return ref[:idx], ref[idx+1:]
	}
	return "", ref
}
