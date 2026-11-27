package auto

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

func TestProductOutcomeUsesDurableTriggersOnceWithinItsTenant(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newApp(t)
		starts := captureStarter(t)
		seedWebhookFlow(t, mounted.State.store, "acme", "research", "improvement.reviewed")
		seedWebhookFlow(t, mounted.State.store, "globex", "research", "improvement.reviewed")
		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "acme/worker"})
		ev := &client.ProductEvent{Source: "research", Name: "improvement.reviewed", Key: "evidence-version-1", Payload: json.RawMessage(`{"msg":"verified regression"}`)}
		first, err := planeProductEvent(ctx, ev)
		if err != nil || first.Matched != 1 {
			t.Fatalf("first: %+v %v", first, err)
		}
		again, err := planeProductEvent(ctx, ev)
		if err != nil || again.Matched != 0 {
			t.Fatalf("duplicate: %+v %v", again, err)
		}
		if len(*starts) != 1 || (*starts)[0].Owner != "acme" {
			t.Fatalf("starts: %+v", *starts)
		}
		if _, err := planeProductEvent(context.Background(), ev); err == nil {
			t.Fatal("event without verified tenant accepted")
		}
		ev.Key = "new"
		ev.Depth = maxCausationDepth
		if out, err := planeProductEvent(ctx, ev); err != nil || out.Matched != 0 {
			t.Fatalf("causation limit: %+v %v", out, err)
		}
	})
}

func TestProductOutcomeRequiresAnObjectAndStableIdentity(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	for _, ev := range []client.ProductEvent{
		{Source: "research", Name: "done", Payload: json.RawMessage(`{}`)},
		{Source: "research", Name: "done", Key: "id", Payload: json.RawMessage(`[]`)},
		{Source: "research", Name: "done", Key: "id", Payload: json.RawMessage(`null`)},
		{Source: "research", Name: "done", Key: "id", Depth: -1, Payload: json.RawMessage(`{}`)},
	} {
		if _, err := planeProductEvent(ctx, &ev); err == nil {
			t.Fatalf("accepted malformed event: %+v", ev)
		}
	}
}
