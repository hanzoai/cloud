package catalogsync

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/events"
	"github.com/hanzoai/cloud/clients/content"
	luxlog "github.com/luxfi/log"
)

// catalogsync_test.go proves the consumer's ONE job: parse a COMMERCE product event and
// dispatch product.created → content.EnsureCatalogAsset (the `generate` seam), with the
// right ACK/NAK semantics. NATS itself is not exercised — the mapping is what matters.

func eventJSON(t *testing.T, typ, org, slug string) []byte {
	t.Helper()
	ev := events.CommerceEvent{Type: typ, OrganizationID: org, Data: map[string]interface{}{}}
	if slug != "" {
		ev.Data["slug"] = slug
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func withStubGenerate(fn func(context.Context, string, string) (content.CatalogAssetResult, error), body func()) {
	orig := generate
	generate = fn
	defer func() { generate = orig }()
	body()
}

func TestHandleEventDispatchesProductCreated(t *testing.T) {
	var calls [][2]string
	withStubGenerate(func(_ context.Context, org, slug string) (content.CatalogAssetResult, error) {
		calls = append(calls, [2]string{org, slug})
		return content.CatalogAssetResult{Org: org, Slug: slug, Created: true, Name: "Asset-1"}, nil
	}, func() {
		if err := handleEvent(context.Background(), luxlog.New("test"), eventJSON(t, "product.created", "karma", "valentina")); err != nil {
			t.Fatalf("handle: %v", err)
		}
	})
	if len(calls) != 1 || calls[0] != [2]string{"karma", "valentina"} {
		t.Fatalf("generate must be called once with (org, slug), got %+v", calls)
	}
}

// A genuine content fault is returned so the message is NAK'd and redelivered (bounded).
func TestHandleEventReturnsErrorForRetry(t *testing.T) {
	withStubGenerate(func(context.Context, string, string) (content.CatalogAssetResult, error) {
		return content.CatalogAssetResult{}, errors.New("store blip")
	}, func() {
		if err := handleEvent(context.Background(), luxlog.New("test"), eventJSON(t, "product.created", "karma", "valentina")); err == nil {
			t.Fatal("a genuine generate error must be returned (NAK for retry)")
		}
	})
}

// A quiet skip (errNotConfigured / already-exists mapped to a nil-error result) ACKs — no
// retry loop for a benign no-op.
func TestHandleEventAcksOnQuietSkip(t *testing.T) {
	withStubGenerate(func(_ context.Context, org, slug string) (content.CatalogAssetResult, error) {
		return content.CatalogAssetResult{Org: org, Slug: slug, Skipped: "not_configured"}, nil
	}, func() {
		if err := handleEvent(context.Background(), luxlog.New("test"), eventJSON(t, "product.created", "karma", "valentina")); err != nil {
			t.Fatalf("a quiet skip must ACK (nil), got %v", err)
		}
	})
}

// Non-actionable messages ACK without ever calling generate: undecodable JSON, the wrong
// event type, or a missing org/slug.
func TestHandleEventIgnoresNonActionable(t *testing.T) {
	called := false
	withStubGenerate(func(context.Context, string, string) (content.CatalogAssetResult, error) {
		called = true
		return content.CatalogAssetResult{}, nil
	}, func() {
		log := luxlog.New("test")
		cases := [][]byte{
			[]byte("not json{"),                              // undecodable → ACK, no loop
			eventJSON(t, "product.updated", "karma", "v"),    // wrong type (subject filter belt-and-suspenders)
			eventJSON(t, "product.created", "", "valentina"), // missing org
			eventJSON(t, "product.created", "karma", ""),     // missing slug
		}
		for i, data := range cases {
			if err := handleEvent(context.Background(), log, data); err != nil {
				t.Errorf("case %d must ACK (nil), got %v", i, err)
			}
		}
	})
	if called {
		t.Fatal("generate must not be called for non-actionable events")
	}
}
