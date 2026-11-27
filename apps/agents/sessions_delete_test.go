package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/mint"
)

func TestDeleteSession_StopsRemovesSubtreeAndEvents(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})

		// Register root, child, grandchild, and an unrelated sibling root.
		code, b := asMember(t, app, http.MethodPost, "/v1/agent/sessions", "alice", false, map[string]any{"agent": "dev", "title": "root"})
		if code != http.StatusCreated {
			t.Fatalf("root register = %d %s", code, b)
		}
		var root sessionView
		mustJSON(t, b, &root)

		code, b = asMember(t, app, http.MethodPost, "/v1/agent/sessions", "alice", false, map[string]any{"agent": "sub", "parentSessionId": root.ID})
		if code != http.StatusCreated {
			t.Fatalf("child register = %d %s", code, b)
		}
		var child sessionView
		mustJSON(t, b, &child)

		code, b = asMember(t, app, http.MethodPost, "/v1/agent/sessions", "alice", false, map[string]any{"agent": "sub2", "parentSessionId": child.ID})
		if code != http.StatusCreated {
			t.Fatalf("grandchild register = %d %s", code, b)
		}
		var gchild sessionView
		mustJSON(t, b, &gchild)

		code, b = asMember(t, app, http.MethodPost, "/v1/agent/sessions", "alice", false, map[string]any{"agent": "dev", "title": "keep-me"})
		if code != http.StatusCreated {
			t.Fatalf("sibling register = %d %s", code, b)
		}
		var keep sessionView
		mustJSON(t, b, &keep)

		// Append events to root and child so we can confirm events are purged too.
		sto := storeOf(t, &mounted.State, "acme")
		ctx := context.Background()
		for _, sid := range []string{root.ID, child.ID, keep.ID} {
			if _, err := sto.AppendEvent(ctx, Event{
				ID: mint.ID("evt"), SessionID: sid, Org: "acme", Kind: KindLog, Actor: "acme/alice", Payload: `{"ok":true}`, CreatedAt: time.Now().Unix(),
			}); err != nil {
				t.Fatalf("append %s: %v", sid, err)
			}
		}

		// Attach a live in-process run handle + Stopped hook to verify delete stops live runs first.
		runCtx, cancel := context.WithCancelCause(ctx)
		done, ok := Hold(root.ID, cancel)
		if !ok {
			t.Fatal("Hold returned false")
		}
		defer done()

		var stoppedIDs []string
		prevStopped := Stopped
		Stopped = func(org, id string) {
			if org == "acme" {
				stoppedIDs = append(stoppedIDs, id)
			}
		}
		t.Cleanup(func() { Stopped = prevStopped })

		// Delete root as its owner.
		code, b = asMember(t, app, http.MethodDelete, "/v1/agent/sessions/"+root.ID, "alice", false, nil)
		if code != http.StatusOK {
			t.Fatalf("DELETE root = %d %s, want 200", code, b)
		}
		var out sessionDeleted
		mustJSON(t, b, &out)
		if !out.Deleted || out.ID != root.ID {
			t.Fatalf("DELETE body = %+v, want {Deleted:true ID:%s}", out, root.ID)
		}

		// Live run was interrupted with ErrStopped and Stopped callback fired for all 3 live nodes.
		if !errors.Is(context.Cause(runCtx), ErrStopped) {
			t.Fatalf("live run cancel cause = %v, want ErrStopped", context.Cause(runCtx))
		}
		if len(stoppedIDs) != 3 {
			t.Fatalf("Stopped fired %d times (%v), want 3", len(stoppedIDs), stoppedIDs)
		}

		// All three nodes and their events are gone; double-delete returns 404.
		for _, sid := range []string{root.ID, child.ID, gchild.ID} {
			if code, _ := asMember(t, app, http.MethodGet, "/v1/agent/sessions/"+sid, "alice", false, nil); code != http.StatusNotFound {
				t.Errorf("GET deleted %s = %d, want 404", sid, code)
			}
			if n, err := sto.CountEvents(ctx, "acme", sid); err != nil || n != 0 {
				t.Errorf("events left for deleted %s = %d (%v), want 0", sid, n, err)
			}
		}
		if code, _ := asMember(t, app, http.MethodDelete, "/v1/agent/sessions/"+root.ID, "alice", false, nil); code != http.StatusNotFound {
			t.Fatalf("second DELETE = %d, want 404", code)
		}

		// Unrelated sibling session and its event remain intact.
		if code, b := asMember(t, app, http.MethodGet, "/v1/agent/sessions/"+keep.ID, "alice", false, nil); code != http.StatusOK {
			t.Fatalf("sibling GET = %d %s, want 200", code, b)
		}
		if n, err := sto.CountEvents(ctx, "acme", keep.ID); err != nil || n != 1 {
			t.Fatalf("sibling events = %d (%v), want 1", n, err)
		}
	})
}

func TestDeleteSession_AuthzAndTenantIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})

		// Unauthenticated request gets 401.
		req := httptest.NewRequest(http.MethodDelete, "/v1/agent/sessions/sess_missing", nil)
		resp, err := app.Test(req, deadline)
		if err != nil {
			t.Fatalf("unauth DELETE: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauth DELETE = %d, want 401", resp.StatusCode)
		}

		// Register a normal session and a private conversation session for alice in acme.
		code, b := asMember(t, app, http.MethodPost, "/v1/agent/sessions", "alice", false, map[string]any{"agent": "dev", "status": StatusDone})
		if code != http.StatusCreated {
			t.Fatalf("register regular = %d %s", code, b)
		}
		var regular sessionView
		mustJSON(t, b, &regular)

		code, b = asMember(t, app, http.MethodPost, "/v1/agent/sessions", "alice", false, map[string]any{"agent": "dev", "channel": talkChannel, "status": StatusDone})
		if code != http.StatusCreated {
			t.Fatalf("register private = %d %s", code, b)
		}
		var priv sessionView
		mustJSON(t, b, &priv)

		// Cross-org caller gets 404 and cannot delete acme's session.
		if code, _ := do(t, app, http.MethodDelete, "/v1/agent/sessions/"+regular.ID, "evil", nil); code != http.StatusNotFound {
			t.Fatalf("cross-org DELETE = %d, want 404", code)
		}

		// Org admin cannot delete another member's private conversation session.
		if code, _ := asMember(t, app, http.MethodDelete, "/v1/agent/sessions/"+priv.ID, "carol", true, nil); code != http.StatusNotFound {
			t.Fatalf("org-admin DELETE private = %d, want 404", code)
		}

		// Org admin CAN delete a member's regular session in a customer org.
		code, b = asMember(t, app, http.MethodDelete, "/v1/agent/sessions/"+regular.ID, "carol", true, nil)
		if code != http.StatusOK {
			t.Fatalf("org-admin DELETE regular = %d %s, want 200", code, b)
		}
		var del sessionDeleted
		if err := json.Unmarshal(b, &del); err != nil || !del.Deleted || del.ID != regular.ID {
			t.Fatalf("org-admin DELETE body = %s, want deleted=true id=%s", b, regular.ID)
		}

		// Owner CAN delete their own private conversation session.
		if code, b := asMember(t, app, http.MethodDelete, "/v1/agent/sessions/"+priv.ID, "alice", false, nil); code != http.StatusOK {
			t.Fatalf("owner DELETE private = %d %s, want 200", code, b)
		}
	})
}
