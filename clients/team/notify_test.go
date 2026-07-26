package team

import (
	"encoding/json"
	"testing"
	"time"
)

// notifSession builds a standalone transactor session over a fresh per-workspace
// store + the real embedded model — the same shape projections_test.go uses, but
// without the `live` singleton (these tests drive applyTx / findAll directly).
func notifSession(t *testing.T, org, ws string) *session {
	t.Helper()
	dir := t.TempDir()
	srv := &transServer{hub: newHub(), store: newStore(dir), hier: buildHierarchy(modelJSON)}
	return &session{server: srv, store: srv.store, hier: srv.hier, org: org, workspace: ws, account: acctSystem}
}

// applyRaw applies one tx map through the real write path (the same path the live
// `tx` RPC and the in-process Apply bridge run), returning the applied txes.
func (s *session) applyRaw(t *testing.T, tx map[string]any) {
	t.Helper()
	raw, err := json.Marshal(tx)
	if err != nil {
		t.Fatalf("marshal tx: %v", err)
	}
	s.applyTx(raw)
}

// inboxOf runs the EXACT query the workbench Inbox issues for the account's
// activity notifications: findAll(ActivityInboxNotification, {user, archived:false}),
// through the real RPC dispatch — proving the feed the SPA sees is populated.
func (s *session) inboxOf(t *testing.T, user string) []map[string]any {
	t.Helper()
	req, _ := json.Marshal(request{
		ID:     7,
		Method: "findAll",
		Params: rawParams(clActivityInboxNotif, map[string]any{"user": user, "archived": false}),
	})
	out := s.handle(req)
	var resp struct {
		Result struct {
			Value []map[string]any `json:"value"`
			Total int              `json:"total"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal findAll: %v", err)
	}
	return resp.Result.Value
}

func rawParams(vals ...any) []json.RawMessage {
	out := make([]json.RawMessage, len(vals))
	for i, v := range vals {
		b, _ := json.Marshal(v)
		out[i] = b
	}
	return out
}

// chatCreate builds a ChatMessage create tx attached to a parent (channel/DM or a
// commented doc), authored by `author` (a social id).
func chatCreate(parent, parentClass, author, markup string) map[string]any {
	now := time.Now().UnixMilli()
	return map[string]any{
		"_class": clTxCreate, "objectId": newMsgID(), "objectClass": clChatMessage,
		"objectSpace": parent, "attachedTo": parent, "attachedToClass": parentClass,
		"collection": "messages", "createdBy": author, "modifiedBy": author,
		"createdOn": now, "modifiedOn": now,
		"attributes": map[string]any{"message": markup},
	}
}

// TestMentionInChannelProducesActivityNotification is the core "X mentioned you"
// proof: an @-mention of a channel member yields exactly one ActivityInboxNotification
// for that member (unread, attached to the message, grouped under the channel's
// DocNotifyContext) — returned by the same findAll the Inbox issues — and none for
// the author or an un-mentioned member.
func TestMentionInChannelProducesActivityNotification(t *testing.T) {
	const org, ws = "acme", "ws-1"
	const author = "11111111-1111-4111-8111-111111111111"
	const alice = "22222222-2222-4222-8222-222222222222"
	const bob = "33333333-3333-4333-8333-333333333333"
	s := notifSession(t, org, ws)

	chID := "channel-eng"
	putDoc(t, s.server, org, ws, map[string]any{
		"_id": chID, "_class": clChannel, "space": "core:space:Workspace",
		"members": []any{author, alice, bob},
	})

	markup := `<p>hey <span data-type="reference" data-id="` + PersonRef(alice) + `">@Alice</span> look</p>`
	s.applyRaw(t, chatCreate(chID, clChannel, "hanzo:"+author, markup))

	got := s.inboxOf(t, alice)
	if len(got) != 1 {
		t.Fatalf("alice inbox = %d notifications, want 1", len(got))
	}
	n := got[0]
	if n["isViewed"] != false || n["archived"] != false {
		t.Fatalf("notification not unread/active: %+v", n)
	}
	if n["objectId"] != chID || n["attachedToClass"] != clChatMessage {
		t.Fatalf("notification not attached to the channel message: %+v", n)
	}
	if dnc := str(n["docNotifyContext"]); dnc != dncID(alice, chID) {
		t.Fatalf("docNotifyContext = %q, want %q", dnc, dncID(alice, chID))
	}
	// The context the Inbox groups it under exists (auto-subscribe / member context).
	if c, _ := s.store.get(org, ws, dncID(alice, chID)); c == nil {
		t.Fatal("no DocNotifyContext materialized for the mentioned member")
	}
	// Neither the author nor the un-mentioned member is notified.
	if len(s.inboxOf(t, author)) != 0 {
		t.Fatal("author must not be notified of their own mention")
	}
	if len(s.inboxOf(t, bob)) != 0 {
		t.Fatal("un-mentioned member must not be notified in a channel")
	}
}

// TestDirectMessageNotifiesOtherParticipant proves a DM notifies the other
// participant even without an explicit mention (a DM is addressed to them), and
// never the sender.
func TestDirectMessageNotifiesOtherParticipant(t *testing.T) {
	const org, ws = "acme", "ws-dm"
	const author = "11111111-1111-4111-8111-111111111111"
	const peer = "44444444-4444-4444-8444-444444444444"
	s := notifSession(t, org, ws)

	dmID := "dm-1"
	putDoc(t, s.server, org, ws, map[string]any{
		"_id": dmID, "_class": clDirectMessage, "space": "core:space:Workspace",
		"members": []any{author, peer},
	})
	s.applyRaw(t, chatCreate(dmID, clDirectMessage, "hanzo:"+author, "<p>ping</p>"))

	if len(s.inboxOf(t, peer)) != 1 {
		t.Fatalf("peer DM inbox = %d, want 1", len(s.inboxOf(t, peer)))
	}
	if len(s.inboxOf(t, author)) != 0 {
		t.Fatal("DM sender must not be notified")
	}
}

// TestAssigneeChangeProducesActivityNotification is the core "X assigned you"
// proof: setting an Issue's assignee via an update Tx auto-subscribes the assignee
// (DocNotifyContext), synthesizes the DocUpdateMessage the notification attaches
// to, and files one unread ActivityInboxNotification returned by the Inbox query.
func TestAssigneeChangeProducesActivityNotification(t *testing.T) {
	const org, ws = "acme", "ws-track"
	const author = "11111111-1111-4111-8111-111111111111"
	const carol = "55555555-5555-4555-8555-555555555555"
	s := notifSession(t, org, ws)

	issueID := "issue-42"
	putDoc(t, s.server, org, ws, map[string]any{
		"_id": issueID, "_class": "tracker:class:Issue", "space": "tracker:project:X",
		"title": "Fix the thing", "assignee": nil,
	})

	// The assignment update, authored by `author`, setting assignee → carol.
	s.applyRaw(t, map[string]any{
		"_class": clTxUpdate, "objectId": issueID, "objectClass": "tracker:class:Issue",
		"objectSpace": "tracker:project:X", "modifiedBy": "hanzo:" + author,
		"modifiedOn": time.Now().UnixMilli(),
		"operations": map[string]any{"assignee": PersonRef(carol)},
	})

	got := s.inboxOf(t, carol)
	if len(got) != 1 {
		t.Fatalf("carol inbox = %d, want 1 assignment notification", len(got))
	}
	n := got[0]
	if n["objectId"] != issueID || n["attachedToClass"] != clDocUpdateMessage {
		t.Fatalf("assignment notification malformed: %+v", n)
	}
	// Auto-subscribe: carol now has a DocNotifyContext for the issue.
	if c, _ := s.store.get(org, ws, dncID(carol, issueID)); c == nil {
		t.Fatal("assignee was not auto-subscribed to the issue")
	}
	// The activity line the notification attaches to exists (so the feed renders).
	if len(s.queryDocs(clDocUpdateMessage, map[string]any{"objectId": issueID})) != 1 {
		t.Fatal("no DocUpdateMessage synthesized for the assignment")
	}
	// Self-assignment notifies no one.
	s.applyRaw(t, map[string]any{
		"_class": clTxUpdate, "objectId": issueID, "objectClass": "tracker:class:Issue",
		"objectSpace": "tracker:project:X", "modifiedBy": "hanzo:" + author,
		"modifiedOn": time.Now().UnixMilli(),
		"operations": map[string]any{"assignee": PersonRef(author)},
	})
	if len(s.inboxOf(t, author)) != 0 {
		t.Fatal("a self-assignment must not notify the author")
	}
}

// TestCommentOnSubscribedDocNotifies proves a comment (ChatMessage attached to a
// non-chat doc) notifies the doc's existing subscribers — the "commented on your
// doc" case — while not notifying the commenter.
func TestCommentOnSubscribedDocNotifies(t *testing.T) {
	const org, ws = "acme", "ws-comment"
	const author = "11111111-1111-4111-8111-111111111111"
	const watcher = "66666666-6666-4666-8666-666666666666"
	s := notifSession(t, org, ws)

	issueID := "issue-7"
	putDoc(t, s.server, org, ws, map[string]any{
		"_id": issueID, "_class": "tracker:class:Issue", "space": "tracker:project:X", "title": "T",
	})
	// watcher is subscribed to the issue (a prior assignment/collab gave them a context).
	s.ensureNotifyContext(watcher, issueID, "tracker:class:Issue", "tracker:project:X", time.Now().UnixMilli())

	s.applyRaw(t, chatCreate(issueID, "tracker:class:Issue", "hanzo:"+author, "<p>looks good</p>"))

	if len(s.inboxOf(t, watcher)) != 1 {
		t.Fatalf("subscriber comment inbox = %d, want 1", len(s.inboxOf(t, watcher)))
	}
	if len(s.inboxOf(t, author)) != 0 {
		t.Fatal("commenter must not be notified of their own comment")
	}
}

// TestNotificationIdempotentPreservesRead proves the projection is idempotent AND
// never resets a read flag: re-applying the same message create leaves the single
// notification untouched (still read after the client marked it viewed) — the
// invariant that keeps a reconnect/replay from resurrecting cleared notifications.
func TestNotificationIdempotentPreservesRead(t *testing.T) {
	const org, ws = "acme", "ws-idem"
	const author = "11111111-1111-4111-8111-111111111111"
	const alice = "22222222-2222-4222-8222-222222222222"
	s := notifSession(t, org, ws)

	dmID := "dm-idem"
	putDoc(t, s.server, org, ws, map[string]any{
		"_id": dmID, "_class": clDirectMessage, "space": "core:space:Workspace",
		"members": []any{author, alice},
	})
	msg := chatCreate(dmID, clDirectMessage, "hanzo:"+author, "<p>hi</p>")
	s.applyRaw(t, msg)

	got := s.inboxOf(t, alice)
	if len(got) != 1 {
		t.Fatalf("inbox = %d, want 1", len(got))
	}
	notifID := str(got[0]["_id"])

	// The client reads it (isViewed:true) via the ordinary tx update path.
	s.applyRaw(t, map[string]any{
		"_class": clTxUpdate, "objectId": notifID, "objectClass": clActivityInboxNotif,
		"objectSpace": str(got[0]["space"]), "modifiedBy": "hanzo:" + alice,
		"modifiedOn": time.Now().UnixMilli(),
		"operations": map[string]any{"isViewed": true},
	})

	// Re-apply the SAME message create (a replay). No duplicate, read flag intact.
	s.applyRaw(t, msg)
	after := s.inboxOf(t, alice)
	if len(after) != 1 {
		t.Fatalf("inbox after replay = %d, want 1 (idempotent)", len(after))
	}
	if after[0]["isViewed"] != true {
		t.Fatalf("read flag reset by replay: %+v", after[0])
	}
}

// TestUnreadCountReflectsReadState proves the Inbox badge input — the set of
// unread activity notifications for a user (isViewed:false) — is exactly what
// findAll returns, and shrinks as notifications are read.
func TestUnreadCountReflectsReadState(t *testing.T) {
	const org, ws = "acme", "ws-count"
	const author = "11111111-1111-4111-8111-111111111111"
	const alice = "22222222-2222-4222-8222-222222222222"
	s := notifSession(t, org, ws)

	chID := "channel-count"
	putDoc(t, s.server, org, ws, map[string]any{
		"_id": chID, "_class": clChannel, "space": "core:space:Workspace",
		"members": []any{author, alice},
	})
	mention := func() string {
		return `<p><span data-id="` + PersonRef(alice) + `">@a</span></p>`
	}
	s.applyRaw(t, chatCreate(chID, clChannel, "hanzo:"+author, mention()))
	s.applyRaw(t, chatCreate(chID, clChannel, "hanzo:"+author, mention()))

	unread := func() int {
		n := 0
		for _, d := range s.inboxOf(t, alice) {
			if d["isViewed"] == false {
				n++
			}
		}
		return n
	}
	if unread() != 2 {
		t.Fatalf("unread = %d, want 2", unread())
	}
	// Read one.
	first := s.inboxOf(t, alice)[0]
	s.applyRaw(t, map[string]any{
		"_class": clTxUpdate, "objectId": str(first["_id"]), "objectClass": clActivityInboxNotif,
		"objectSpace": str(first["space"]), "modifiedBy": "hanzo:" + alice,
		"modifiedOn": time.Now().UnixMilli(),
		"operations": map[string]any{"isViewed": true},
	})
	if unread() != 1 {
		t.Fatalf("unread after reading one = %d, want 1", unread())
	}
}
