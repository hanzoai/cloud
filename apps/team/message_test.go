package team

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/namespace"
)

// A room is a conversation, over the API, end to end.
//
// This drives the REAL HTTP path rather than calling the ops, because the write
// resolves its AUTHOR from the caller's verified credential — the one fact a
// body may not carry — and a direct call has no request to resolve it from. A
// test that called the op would prove the store round-trips and say nothing
// about who a message is attributed to.
func TestRoomMessagesRoundTripOverHTTP(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureSpace(context.Background(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	auth := bearerFor(t, acct, org)

	code, body := call(t, app, http.MethodPost, "/v1/team/rooms", auth,
		map[string]any{"name": "bugfix-1010", "space": ws.UUID})
	if code != http.StatusCreated {
		t.Fatalf("open room = %d (%s)", code, body)
	}
	var room teamRoom
	if err := json.Unmarshal(body, &room); err != nil {
		t.Fatalf("room: %v (%s)", err, body)
	}

	const said = "deploying now"
	code, body = call(t, app, http.MethodPost, "/v1/team/rooms/"+room.ID+"/messages", auth,
		map[string]any{"space": ws.UUID, "text": said})
	if code != http.StatusCreated {
		t.Fatalf("send = %d (%s)", code, body)
	}
	var sent teamMessage
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("message: %v (%s)", err, body)
	}
	// THE AUTHOR IS THE CREDENTIAL'S. Nothing in the request said who wrote this.
	if sent.Author != acct {
		t.Fatalf("author = %q, want the calling account %q", sent.Author, acct)
	}
	if sent.Text != said {
		t.Fatalf("text = %q, want %q — the markup wrapper did not survive the read", sent.Text, said)
	}
	if sent.Room != room.ID || sent.CreatedOn == 0 {
		t.Fatalf("message = %+v, want it addressed to %s with a stamp", sent, room.ID)
	}

	code, body = call(t, app, http.MethodGet,
		"/v1/team/rooms/"+room.ID+"/messages?space="+ws.UUID, auth, nil)
	if code != http.StatusOK {
		t.Fatalf("read = %d (%s)", code, body)
	}
	var page teamMessages
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("messages: %v (%s)", err, body)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != sent.ID || page.Messages[0].Text != said {
		t.Fatalf("read back %+v, want the one message just sent", page.Messages)
	}
}

// Another org's caller reaches neither half. 404 and not 403, so a probe learns
// nothing about which rooms exist.
func TestRoomMessagesAreOrgScoped(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureSpace(context.Background(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	auth := bearerFor(t, acct, org)
	code, body := call(t, app, http.MethodPost, "/v1/team/rooms", auth,
		map[string]any{"name": "private-plans", "space": ws.UUID})
	if code != http.StatusCreated {
		t.Fatalf("open room = %d (%s)", code, body)
	}
	var room teamRoom
	if err := json.Unmarshal(body, &room); err != nil {
		t.Fatal(err)
	}

	const other, otherAcct = "globex", "660e8400-e29b-41d4-a716-446655440001"
	if _, err := mounted.State.accounts.EnsureSpace(context.Background(), other, otherAcct, "Bob"); err != nil {
		t.Fatal(err)
	}
	intruder := bearerFor(t, otherAcct, other)

	if code, _ := call(t, app, http.MethodGet,
		"/v1/team/rooms/"+room.ID+"/messages?space="+ws.UUID, intruder, nil); code != http.StatusNotFound {
		t.Fatalf("cross-org read = %d, want 404", code)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/team/rooms/"+room.ID+"/messages", intruder,
		map[string]any{"space": ws.UUID, "text": "hello"}); code != http.StatusNotFound {
		t.Fatalf("cross-org send = %d, want 404", code)
	}

	// AND THE REFUSAL CAME FROM THE OWNERSHIP CHECK, which is the half a status
	// code cannot show. Every store is keyed (org, space), so an intruder naming
	// another org's space uuid reads THEIR OWN empty store and gets a 404 either
	// way — the test passes with the check deleted, measured. What the check
	// actually buys is that the request is refused BEFORE a store is opened, so a
	// probe cannot make cloud mint an unbounded set of empty per-space databases
	// keyed by uuids the caller chose. The absence of that file is the assertion.
	ns, err := cloud.OrgNamespace(other, ws.UUID)
	if err != nil {
		t.Fatal(err)
	}
	path, err := namespace.Path(mounted.State.trans.store.dir, ns, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("a store was opened for %s at another org's space %s (%s): the ownership check ran too late",
			other, ws.UUID, filepath.Dir(path))
	}
}

// A message needs something to say, and there is a ceiling on how much.
func TestSendRefusesEmptyAndOversized(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureSpace(context.Background(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	auth := bearerFor(t, acct, org)
	code, body := call(t, app, http.MethodPost, "/v1/team/rooms", auth,
		map[string]any{"name": "general", "space": ws.UUID})
	if code != http.StatusCreated {
		t.Fatalf("open room = %d (%s)", code, body)
	}
	var room teamRoom
	if err := json.Unmarshal(body, &room); err != nil {
		t.Fatal(err)
	}

	if code, _ := call(t, app, http.MethodPost, "/v1/team/rooms/"+room.ID+"/messages", auth,
		map[string]any{"space": ws.UUID, "text": "   "}); code != http.StatusBadRequest {
		t.Fatalf("empty message = %d, want 400", code)
	}
	big := make([]byte, messageBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/team/rooms/"+room.ID+"/messages", auth,
		map[string]any{"space": ws.UUID, "text": string(big)}); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized message = %d, want 413", code)
	}
}
