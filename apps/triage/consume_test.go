package triage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/commerce/infra"
	psembed "github.com/hanzoai/pubsub/embed"
)

// A FAULT PUBLISHED ON THE ERROR STREAM IS COUNTED AND FILED, over a real JetStream:
// three error facts for one fingerprint on event.error become one issue, and a
// message that is not an error fact is read and left.
func TestTheErrorStreamFilesAnIssue(t *testing.T) {
	each(t, func(t *testing.T) {
		l := newLoop(t, config(false, false))
		srv, err := psembed.Open(psembed.Options{Port: -1, StoreDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { srv.Shutdown() })
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{URL: srv.ClientURL(), Name: "triage-test", EnableJetStream: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cl.Close() })
		go func() { _ = listen(ctx, l.state, cl) }()

		publish := func(body map[string]any) {
			data, _ := json.Marshal(body)
			for range 100 { // the stream exists once the consumer has made it
				if _, err := cl.JetStream().Publish(ctx, event.ErrorSubject, data); err == nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("the stream never took the fact")
		}
		fact := func(signal string) map[string]any {
			return map[string]any{"signal": signal, "org": "hanzo", "id": "f", "time": time.Now().UTC(), "issue": "abcdefabcdefabcd",
				"product": "cloud", "service": "agents", "path": "/v1/agent/sessions", "message": "row 41 is gone",
				"attributes": map[string]string{"fault.class": "server", "fault.code": "internal", "fault.op": "GET /v1/agent/sessions"}}
		}
		publish(fact("act"))
		for range 3 {
			publish(fact("error"))
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			l.w.mu.Lock()
			n := len(l.w.issues)
			l.w.mu.Unlock()
			if n == 1 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%d issues filed from three errors on the stream, want 1", len(l.w.issues))
	})
}
