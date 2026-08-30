package channels

import (
	"context"
	"net/http"
	"testing"
)

func countAgentRows(ctx context.Context, st *store, org string) (int, error) {
	var n int
	err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_agent WHERE org = ?`, org).Scan(&n)
	return n, err
}

// agent_test.go proves the binding: the PUT is org-admin only, an unknown channel
// is a 404, the default and a room are set and read back as one shape, unbind
// removes a binding, and a turn resolves room → default → built-in — for
// the org that bound it and no other.
func TestAgentBinding(t *testing.T) {
	e := newApp(t)

	// Not admin: refused. Unknown channel: 404.
	if got := req(t, e, http.MethodPut, "/v1/channels/agent", "acme",
		map[string]any{"channel": "slack", "default": "eng"}); got.Code != http.StatusForbidden {
		t.Fatalf("non-admin PUT want 403, got %d (%s)", got.Code, got.Body)
	}
	if got := reqAdmin(t, e, http.MethodPut, "/v1/channels/agent", "acme",
		map[string]any{"channel": "irc", "default": "eng"}); got.Code != http.StatusNotFound {
		t.Fatalf("unknown channel want 404, got %d", got.Code)
	}

	// Nothing bound: the built-in answers.
	var v channelAgents
	got := req(t, e, http.MethodGet, "/v1/channels/agent?channel=slack", "acme", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("GET want 200, got %d (%s)", got.Code, got.Body)
	}
	v = channelAgents{}
	decodeJSON(t, got.Body, &v)
	if v.Default != defaultAgent || len(v.Rooms) != 0 {
		t.Fatalf("unbound view = %+v", v)
	}

	// Bind a default and a room; both come back in one shape.
	got = reqAdmin(t, e, http.MethodPut, "/v1/channels/agent", "acme",
		map[string]any{"channel": "slack", "default": "eng", "rooms": map[string]string{"C024BE91L": "des"}})
	if got.Code != http.StatusOK {
		t.Fatalf("PUT want 200, got %d (%s)", got.Code, got.Body)
	}
	v = channelAgents{}
	decodeJSON(t, got.Body, &v)
	if v.Default != "eng" || v.Rooms["C024BE91L"] != "des" {
		t.Fatalf("bound view = %+v", v)
	}

	// A turn resolves the room first, then the default; another org sees nothing.
	st := e.store(t)
	ctx := context.Background()
	in := func(room string) Message { return Message{Channel: "slack", Room: Room{ID: room, Kind: RoomGroup}} }
	if got := agentFor(ctx, st, "acme", in("C024BE91L")); got != "des" {
		t.Fatalf("room binding: got %q", got)
	}
	if got := agentFor(ctx, st, "acme", in("C0OTHER")); got != "eng" {
		t.Fatalf("default binding: got %q", got)
	}
	if got := agentFor(ctx, st, "beta", in("C024BE91L")); got != defaultAgent {
		t.Fatalf("other org must see the built-in, got %q", got)
	}
	if got := agentFor(ctx, st, "acme", Message{Channel: "discord", Room: Room{ID: "C024BE91L", Kind: RoomGroup}}); got != defaultAgent {
		t.Fatalf("binding is per transport, got %q", got)
	}

	// Unbind removes; an absent field leaves alone.
	got = reqAdmin(t, e, http.MethodPut, "/v1/channels/agent", "acme",
		map[string]any{"channel": "slack", "unbind": []string{"C024BE91L"}})
	if got.Code != http.StatusOK {
		t.Fatalf("remove want 200, got %d (%s)", got.Code, got.Body)
	}
	v = channelAgents{}
	decodeJSON(t, got.Body, &v)
	if v.Default != "eng" || len(v.Rooms) != 0 {
		t.Fatalf("after remove = %+v", v)
	}
	// Naming the built-in as the default is the same as having none.
	got = reqAdmin(t, e, http.MethodPut, "/v1/channels/agent", "acme", map[string]any{"channel": "slack", "default": defaultAgent})
	if got.Code != http.StatusOK {
		t.Fatalf("restore want 200, got %d", got.Code)
	}
	if got := agentFor(ctx, st, "acme", in("C0OTHER")); got != defaultAgent {
		t.Fatalf("after restoring the default, got %q", got)
	}
	if n, err := countAgentRows(ctx, st, "acme"); err != nil || n != 0 {
		t.Fatalf("default binding to the built-in must not be stored: rows=%d err=%v", n, err)
	}
}
