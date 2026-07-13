package events

import (
	"context"
	"testing"
)

// product_test.go covers the catalog event contract: the subjects are on the COMMERCE
// stream, the envelope carries the join key, and publishing degrades to a no-op when no
// NATS publisher is wired (exactly like the order publishers).

func TestProductSubjectsAreOnCommerceStream(t *testing.T) {
	// StreamSubjects captures `commerce.>`, so the catalog subjects MUST carry that prefix
	// or the COMMERCE consumer never sees them.
	for _, s := range []string{SubjectProductCreated, SubjectProductUpdated} {
		if len(s) < len("commerce.") || s[:len("commerce.")] != "commerce." {
			t.Errorf("subject %q must be prefixed commerce. to land on the COMMERCE stream", s)
		}
	}
	if SubjectProductCreated != "commerce.product.created" || SubjectProductUpdated != "commerce.product.updated" {
		t.Errorf("catalog subjects drifted: %q / %q", SubjectProductCreated, SubjectProductUpdated)
	}
}

func TestNewProductEventEnvelope(t *testing.T) {
	ev := newProductEvent("product.created", "karma", "prod_123", "valentina", "Valentina Dress", "SKU-1")
	if ev.Type != "product.created" || ev.OrganizationID != "karma" || ev.ID != "prod_123" {
		t.Fatalf("envelope header wrong: %+v", ev)
	}
	if ev.Data["slug"] != "valentina" {
		t.Fatalf("slug is the join key content renders against, got %v", ev.Data["slug"])
	}
	if ev.Data["product_id"] != "prod_123" || ev.Data["name"] != "Valentina Dress" || ev.Data["sku"] != "SKU-1" {
		t.Fatalf("catalog context missing: %+v", ev.Data)
	}
	if ev.Timestamp.IsZero() {
		t.Error("event must be timestamped")
	}
}

// A nil / unwired publisher never errors and never panics — the whole loop degrades to a
// no-op when NATS is absent, so the product REST path is never blocked.
func TestPublishProductNoOpWhenUnwired(t *testing.T) {
	var nilPub *Publisher
	if err := nilPub.PublishProductCreated(context.Background(), "karma", "p", "valentina", "n", "s"); err != nil {
		t.Errorf("nil publisher must no-op, got %v", err)
	}
	if err := nilPub.PublishProductUpdated(context.Background(), "karma", "p", "valentina", "n", "s"); err != nil {
		t.Errorf("nil publisher must no-op, got %v", err)
	}
	// A publisher with no pubsub connection (NewPublisher(nil) returns nil, but guard the
	// zero value too) also no-ops via Publish's nil-pubsub guard.
	empty := &Publisher{}
	if err := empty.PublishProductCreated(context.Background(), "karma", "p", "valentina", "n", "s"); err != nil {
		t.Errorf("pubsub-less publisher must no-op, got %v", err)
	}
}
