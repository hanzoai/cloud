package team

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/hanzoai/cloud/clients/team/token"
)

// collabDocID builds the collaborator-client encodeDocumentId shape, URL-escaped
// the way fetch() sends it (encodeURIComponent).
func collabDocID(ws, objClass, objID, attr string) string {
	return url.PathEscape(ws + "|" + objClass + "|" + objID + "|" + attr)
}

// TestCollabRPCRoundTrip proves the REAL collaborator-client contract end to end:
// createContent stores the markup snapshot and returns a timestamped blob ref
// (makeCollabJsonId shape "<objectId>-<field>-<ms>"), and getContent with that
// ref as source returns the exact markup — the read the tracker/description
// preview lane does.
func TestCollabRPCRoundTrip(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureWorkspace(context.Background(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	auth := bearerFor(t, acct, org)
	docID := collabDocID(ws.UUID, "tracker:class:Issue", "issue-1", "description")
	markup := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"hello"}]}]}`

	code, body := call(t, app, http.MethodPost, "/collaborator/rpc/"+docID, auth, map[string]any{
		"method": "createContent", "payload": map[string]any{"content": map[string]string{"description": markup}},
	})
	if code != http.StatusOK {
		t.Fatalf("createContent = %d (%s)", code, body)
	}
	var created struct {
		Content map[string]string `json:"content"`
		Error   string            `json:"error"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Error != "" {
		t.Fatalf("createContent body %s (err %v)", body, err)
	}
	ref := created.Content["description"]
	if ref == "" {
		t.Fatalf("no blob ref in %s", body)
	}

	code, body = call(t, app, http.MethodPost, "/collaborator/rpc/"+docID, auth, map[string]any{
		"method": "getContent", "payload": map[string]any{"source": ref},
	})
	if code != http.StatusOK {
		t.Fatalf("getContent = %d (%s)", code, body)
	}
	var got struct {
		Content map[string]string `json:"content"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Content["description"] != markup {
		t.Fatalf("markup round-trip mismatch: %q", got.Content["description"])
	}

	// updateContent answers the empty object the client expects.
	code, body = call(t, app, http.MethodPost, "/collaborator/rpc/"+docID, auth, map[string]any{
		"method": "updateContent", "payload": map[string]any{"content": map[string]string{"description": markup}},
	})
	if code != http.StatusOK || string(body) != "{}" {
		t.Fatalf("updateContent = %d (%s)", code, body)
	}
}

// TestCollabCreateContentSeedsYLog is the tracker "description dropped on create"
// bar: the New-Issue dialog's createContent must seed the live-editing update log
// (the one collabws.go replays), not only the snapshot blob — else the freshly
// created issue's collaborative editor shows empty. updateContent must NEVER touch
// the log (a live-edited doc must not be clobbered).
func TestCollabCreateContentSeedsYLog(t *testing.T) {
	vfs := newMemVFS()
	app := mountTeamVFS(t, vfs)
	ctx := context.Background()
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureWorkspace(ctx, org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	auth := bearerFor(t, acct, org)
	docID := collabDocID(ws.UUID, "tracker:class:Issue", "issue-seed", "description")
	markup := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"hi"}]}]}`
	update := []byte{0x01, 0x02, 0x03, 0x04} // opaque Y.js update — the lane never parses it
	logKey := blobKey(org, ws.UUID, yLogBlobID(collabDoc{objectID: "issue-seed", objectAttr: "description"}))

	code, body := call(t, app, http.MethodPost, "/collaborator/rpc/"+docID, auth, map[string]any{
		"method": "createContent",
		"payload": map[string]any{
			"content": map[string]string{"description": markup},
			"updates": map[string]string{"description": base64.StdEncoding.EncodeToString(update)},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("createContent = %d (%s)", code, body)
	}
	data, err := vfs.Get(ctx, logKey)
	if err != nil {
		t.Fatalf("createContent did not seed the ydoc log: %v", err)
	}
	if log := unmarshalYLog(data); len(log) != 1 || !bytes.Equal(log[0], update) {
		t.Fatalf("seeded log = %v, want one entry %v", log, update)
	}

	// updateContent must not clobber the log the live editors own.
	code, body = call(t, app, http.MethodPost, "/collaborator/rpc/"+docID, auth, map[string]any{
		"method": "updateContent",
		"payload": map[string]any{
			"content": map[string]string{"description": markup},
			"updates": map[string]string{"description": base64.StdEncoding.EncodeToString([]byte{0x09})},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("updateContent = %d (%s)", code, body)
	}
	data, _ = vfs.Get(ctx, logKey)
	if log := unmarshalYLog(data); len(log) != 1 || !bytes.Equal(log[0], update) {
		t.Fatalf("updateContent clobbered the seeded log: %v", log)
	}
}

// TestCollabRPCTenancy is the red bar: no token → 401; a foreign workspace in the
// documentId → 404 (no oracle); a workspace token pinned to another workspace →
// 404 even for a member of the named one.
func TestCollabRPCTenancy(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	const acctA, acctB = "aaaaaaaa-0000-4000-8000-00000000000a", "bbbbbbbb-0000-4000-8000-00000000000b"
	wsA, _ := mounted.State.accounts.EnsureWorkspace(ctx, "org-a", acctA, "Alice")
	wsB, _ := mounted.State.accounts.EnsureWorkspace(ctx, "org-b", acctB, "Bob")
	docA := collabDocID(wsA.UUID, "tracker:class:Issue", "issue-1", "description")
	payload := map[string]any{"method": "createContent", "payload": map[string]any{"content": map[string]string{"description": "{}"}}}

	if code, _ := call(t, app, http.MethodPost, "/collaborator/rpc/"+docA, nil, payload); code != http.StatusUnauthorized {
		t.Fatalf("unauth = %d, want 401", code)
	}
	// org-b caller naming org-a's workspace → 404.
	if code, _ := call(t, app, http.MethodPost, "/collaborator/rpc/"+docA, bearerFor(t, acctB, "org-b"), payload); code != http.StatusNotFound {
		t.Fatalf("cross-org = %d, want 404", code)
	}
	// A WORKSPACE token names its workspace; a documentId for a different one is
	// refused even though the account is a member of both.
	if _, err := mounted.State.accounts.EnsureWorkspace(ctx, "org-b", acctB, "Bob Two"); err != nil {
		t.Fatal(err)
	}
	wsTok, err := token.Generate(acctB, wsB.UUID, map[string]any{"org": "org-b"}, expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	otherDoc := collabDocID("00000000-0000-4000-8000-00000000dead", "tracker:class:Issue", "i", "description")
	code, _ := call(t, app, http.MethodPost, "/collaborator/rpc/"+otherDoc,
		map[string]string{"Authorization": "Bearer " + wsTok}, payload)
	if code != http.StatusNotFound {
		t.Fatalf("workspace-token mismatch = %d, want 404", code)
	}
}

// TestChannelProjectsNotifyContexts is the chat-nav regression bar: creating a
// chunter Channel materializes one DocNotifyContext per member (the docs the
// nav's {user} query lists), a message bumps lastUpdateTimestamp, a member pull
// removes the departed member's context, and every projection write is mirrored
// on the broadcast (derived) list so live sessions refresh.
func TestChannelProjectsNotifyContexts(t *testing.T) {
	s := testSession()
	const member2 = "11111111-2222-4333-8444-555555555555"

	mk := func(m map[string]any) []byte { b, _ := json.Marshal(m); return b }
	s.handle(mk(map[string]any{"id": 1, "method": "tx", "params": []any{map[string]any{
		"_id": "tx-1", "_class": clTxCreate, "space": "core:space:Tx",
		"objectId": "chan-1", "objectClass": clChannel, "objectSpace": "core:space:Workspace",
		"modifiedBy": s.account, "modifiedOn": 1, "attributes": map[string]any{
			"name": "verify-channel", "members": []any{s.account, member2}, "private": false, "archived": false,
		},
	}}}))

	ctxs := s.queryDocs(clDocNotifyContext, map[string]any{"objectId": "chan-1"})
	if len(ctxs) != 2 {
		t.Fatalf("contexts after create = %d, want 2", len(ctxs))
	}
	users := map[string]bool{}
	for _, c := range ctxs {
		users[str(c["user"])] = true
		if str(c["objectClass"]) != clChannel || c["hidden"] != false {
			t.Fatalf("bad context %v", c)
		}
	}
	if !users[s.account] || !users[member2] {
		t.Fatalf("context users = %v", users)
	}

	// A chat message bumps every member context's lastUpdateTimestamp.
	before := ctxs[0]["lastUpdateTimestamp"]
	s.handle(mk(map[string]any{"id": 2, "method": "tx", "params": []any{map[string]any{
		"_id": "tx-2", "_class": clTxCreate, "space": "core:space:Tx",
		"objectId": "msg-1", "objectClass": clChatMessage, "objectSpace": "chan-1",
		"attachedTo": "chan-1", "attachedToClass": clChannel, "collection": "messages",
		"modifiedBy": s.account, "modifiedOn": 2, "attributes": map[string]any{"message": "hi"},
	}}}))
	ctxs = s.queryDocs(clDocNotifyContext, map[string]any{"objectId": "chan-1", "user": s.account})
	if len(ctxs) != 1 {
		t.Fatalf("contexts after message = %d, want 1", len(ctxs))
	}
	after, _ := asFloat(ctxs[0]["lastUpdateTimestamp"])
	beforeF, _ := asFloat(before)
	if after < beforeF {
		t.Fatalf("lastUpdateTimestamp did not advance: %v -> %v", before, ctxs[0]["lastUpdateTimestamp"])
	}

	// Member leaves → their context is removed; the other survives.
	s.handle(mk(map[string]any{"id": 3, "method": "tx", "params": []any{map[string]any{
		"_id": "tx-3", "_class": clTxUpdate, "space": "core:space:Tx",
		"objectId": "chan-1", "objectClass": clChannel, "objectSpace": "core:space:Workspace",
		"modifiedBy": s.account, "modifiedOn": 3,
		"operations": map[string]any{"$pull": map[string]any{"members": member2}},
	}}}))
	if got := s.queryDocs(clDocNotifyContext, map[string]any{"objectId": "chan-1"}); len(got) != 1 || str(got[0]["user"]) != s.account {
		t.Fatalf("contexts after leave = %v", got)
	}
}
