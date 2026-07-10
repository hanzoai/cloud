package engine

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billingevent"
	"github.com/hanzoai/cloud/clients/commerce/models/webhookendpoint"
	"github.com/hanzoai/cloud/clients/commerce/util/json"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// EmitBillingEvent creates an append-only billing event record and dispatches
// it to all matching webhook endpoints.
func EmitBillingEvent(db *datastore.Datastore, eventType, objectType, objectId, customerId string, data, previousData Map) (*billingevent.BillingEvent, error) {
	evt := billingevent.New(db)
	evt.Type = eventType
	evt.ObjectType = objectType
	evt.ObjectId = objectId
	evt.CustomerId = customerId
	evt.Data = data
	evt.PreviousData = previousData
	evt.Pending = true

	if err := evt.Create(); err != nil {
		return nil, fmt.Errorf("failed to create billing event: %w", err)
	}

	// Dispatch webhooks asynchronously (best-effort in this call)
	go func() {
		_ = DispatchWebhooks(db, evt)
	}()

	return evt, nil
}

// DispatchWebhooks sends the event to all matching webhook endpoints.
func DispatchWebhooks(db *datastore.Datastore, evt *billingevent.BillingEvent) error {
	// Query all enabled endpoints
	iter := webhookendpoint.Query(db).
		Filter("Status=", "enabled").
		Run()

	var dispatched int
	for {
		ep := webhookendpoint.New(db)
		if _, err := iter.Next(ep); err != nil {
			break
		}

		if !ep.MatchesEvent(evt.Type) {
			continue
		}

		if err := deliverWebhook(ep, evt); err != nil {
			// Log but don't fail — retry logic can be added later
			continue
		}
		dispatched++
	}

	// Mark event as fully dispatched
	evt.Pending = false
	_ = evt.Update()

	return nil
}

// deliverWebhook POSTs the event payload to a webhook endpoint with HMAC signature.
func deliverWebhook(ep *webhookendpoint.WebhookEndpoint, evt *billingevent.BillingEvent) error {
	payload := json.EncodeBytes(Map{
		"id":           evt.Id(),
		"type":         evt.Type,
		"objectType":   evt.ObjectType,
		"objectId":     evt.ObjectId,
		"customerId":   evt.CustomerId,
		"data":         evt.Data,
		"previousData": evt.PreviousData,
		"livemode":     evt.Livemode,
		"created":      evt.Created,
	})

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	signature := computeSignature(timestamp, payload, ep.Secret)

	req, err := http.NewRequest("POST", ep.Url, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Commerce-Signature", fmt.Sprintf("t=%s,v1=%s", timestamp, signature))
	req.Header.Set("Commerce-Event-Type", evt.Type)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook delivery failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook endpoint returned status %d", resp.StatusCode)
	}

	return nil
}

// computeSignature creates an HMAC-SHA256 signature for webhook verification.
func computeSignature(timestamp string, payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhookSignature verifies a webhook payload signature.
func VerifyWebhookSignature(payload []byte, signatureHeader, secret string) error {
	parts := strings.Split(signatureHeader, ",")
	var timestamp, signature string
	for _, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v1":
			signature = kv[1]
		}
	}

	if timestamp == "" || signature == "" {
		return fmt.Errorf("invalid signature header format")
	}

	expected := computeSignature(timestamp, payload, secret)
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}
