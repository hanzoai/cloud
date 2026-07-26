package team

// notify.go generates the Inbox activity-notification feed — the "X mentioned
// you / assigned you / commented on your doc" entries the workbench Inbox shows.
//
// WHY THIS FILE, NOT domainRequest. The classic Inbox
// (notification-resources/InboxNotificationsClientImpl) reads notifications the
// SAME way it reads DocNotifyContext: through findAll —
//   findAll(notification.class.ActivityInboxNotification, {user, archived:false})
//   findAll(notification.class.CommonInboxNotification,   {user, archived:false})
//   findAll(notification.class.DocNotifyContext,          {user})
// The activity feed was empty NOT because domainRequest returned [] (that stub
// serves the SEPARATE, newer @hcengineering/communication operation-domain, a
// different wire shape the classic Inbox never queries), but because no
// server-side trigger ever CREATED the InboxNotification docs findAll returns.
// This file is that trigger — the exact twin of seed.go's projectNotifyContexts,
// which already materializes the contexts the same query path reads.
//
// WHAT IT GENERATES (the core, all ActivityInboxNotification — attachedTo an
// ActivityMessage so it renders in the feed):
//   - mention in a channel message      → notify each @-mentioned member
//   - direct message                    → notify every other participant
//   - comment (ChatMessage on a doc)     → notify the doc's subscribers + mentioned
//   - assignee set on a doc (Issue, …)   → notify the assignee (auto-subscribed),
//                                          attached to a synthesized DocUpdateMessage
//
// Read/unread + counts need no extra machinery: the Inbox derives its badge from
// these docs' isViewed flag, and the client's readNotifications() writes
// isViewed:true through the ordinary tx path (txUpdate) — findAll then returns the
// updated flag. Notifications are idempotent (id derived from message+user) and
// never clobber a read flag on re-projection.

import (
	"regexp"
	"time"
)

// Notification + activity class ids the feed materializes. Kept beside the code
// that builds them (the model-vocabulary-in-one-place rule the rest of the
// package follows).
const (
	clInboxNotification  = "notification:class:InboxNotification"
	clActivityInboxNotif = "notification:class:ActivityInboxNotification"
	clCommonInboxNotif   = "notification:class:CommonInboxNotification"
	clMentionInboxNotif  = "notification:class:MentionInboxNotification"
	clDocUpdateMessage   = "activity:class:DocUpdateMessage"
)

// dncID is the deterministic DocNotifyContext id for (user, objectId) — the ONE
// derivation, shared by seed.go's projectNotifyContexts and ensureNotifyContext so
// a chat-space context and a notification-created context can never diverge or
// duplicate for the same (user, object).
func dncID(user, objectID string) string { return "dnc:" + user + ":" + objectID }

// personSpaceOf resolves a user's PersonSpace id (an InboxNotification /
// DocNotifyContext lives in the recipient's personal space), falling back to the
// common workspace space when the PersonSpace has not been materialized yet.
func (s *session) personSpaceOf(user string) string {
	if user != "" {
		if ps, _ := s.store.get(s.org, s.workspace, "person-space:person-"+user); ps != nil {
			return str(ps["_id"])
		}
	}
	return "core:space:Workspace"
}

// uuidRe matches an account uuid — the id shape every human and bot account uses.
// A Person ref is "person-<uuid>" and a mention reference node embeds that id, so
// this is how we recover the mentioned/assigned account from markup or a ref.
var uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
var personRefRe = regexp.MustCompile(`person-(` + uuidRe.String() + `)`)

// accountFromRef normalizes any account reference to the bare account uuid: a
// Person ref ("person-<uuid>"), a hanzo social id ("hanzo:<uuid>"), or an already
// bare uuid. A value that is none of these returns "" (never notified).
func accountFromRef(ref string) string {
	if m := personRefRe.FindStringSubmatch(ref); m != nil {
		return m[1]
	}
	ref = stripHanzo(ref)
	if uuidRe.MatchString(ref) {
		return uuidRe.FindString(ref)
	}
	return ""
}

// mentionedAccounts extracts every distinct account uuid @-referenced in message
// markup (Chunter stores a mention as a reference node carrying the Person id
// "person-<uuid>"). Order-stable, de-duplicated.
func mentionedAccounts(markup string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range personRefRe.FindAllStringSubmatch(markup, -1) {
		if uid := m[1]; uid != "" && !seen[uid] {
			seen[uid] = true
			out = append(out, uid)
		}
	}
	return out
}

// projectNotifications is the trigger the write path runs after applying one
// content Tx: it fans the Tx out into Inbox notifications for the affected
// accounts. Called from txCreate and txUpdate with the applied Tx `t` and the
// resulting stored `doc` (txUpdate needs `t` to know which fields changed). It is
// a no-op for roster/system docs (no ChatMessage, no assignee) so the projection
// never notifies on its own bookkeeping.
func (s *session) projectNotifications(t, doc map[string]any) {
	if s.hier.isDerived(str(doc["_class"]), clChatMessage) {
		s.notifyChatMessage(doc)
	}
	if txSetsField(t, "assignee") {
		s.notifyAssignee(t, doc)
	}
}

// txSetsField reports whether a create/update Tx set the named field (create:
// present in attributes; update: present in operations). This is how notifyAssignee
// fires ONLY when the assignee actually changed, never on unrelated updates.
func txSetsField(t map[string]any, field string) bool {
	if attrs, ok := t["attributes"].(map[string]any); ok {
		if _, has := attrs[field]; has {
			return true
		}
	}
	if ops, ok := t["operations"].(map[string]any); ok {
		if _, has := ops[field]; has {
			return true
		}
	}
	return false
}

// notifyChatMessage generates the notifications for one freshly-created
// ChatMessage: mentions/participants for a chat-space message, or subscribers +
// mentions for a comment posted on any other document.
func (s *session) notifyChatMessage(msg map[string]any) {
	msgID := str(msg["_id"])
	parentID := str(msg["attachedTo"])
	parentClass := str(msg["attachedToClass"])
	if msgID == "" || parentID == "" {
		return
	}
	author := accountFromRef(str(firstNonNil(msg["createdBy"], msg["modifiedBy"])))
	markup := str(msg["message"])
	now := asInt64(firstNonNil(msg["modifiedOn"], msg["createdOn"]))
	if now == 0 {
		now = time.Now().UnixMilli()
	}

	parent, _ := s.store.get(s.org, s.workspace, parentID)

	// Chat space (channel or DM): recipients are the mentioned members (channel) or
	// every other participant (DM) — the "mentioned you" / "messaged you" cases.
	if parent != nil && (str(parent["_class"]) == clChannel || str(parent["_class"]) == clDirectMessage) {
		members := toStringSlice(parent["members"])
		var recipients []string
		if str(parent["_class"]) == clDirectMessage {
			recipients = members
		} else {
			mentioned := mentionedAccounts(markup)
			recipients = intersect(mentioned, members)
		}
		for _, u := range recipients {
			if u == "" || u == author {
				continue
			}
			ctxID := s.ensureNotifyContext(u, parentID, str(parent["_class"]), str(parent["space"]), now)
			s.putActivityNotification(u, ctxID, parentID, str(parent["_class"]), msgID, clChatMessage, now)
		}
		return
	}

	// Comment on any other doc: recipients are the doc's existing subscribers (they
	// hold a DocNotifyContext for it) plus anyone @-mentioned (auto-subscribed).
	objClass := parentClass
	objSpace := ""
	if parent != nil {
		objClass = str(parent["_class"])
		objSpace = str(parent["space"])
	}
	recipients := map[string]bool{}
	for _, ctx := range s.queryDocs(clDocNotifyContext, map[string]any{"objectId": parentID}) {
		if u := str(ctx["user"]); u != "" {
			recipients[u] = true
		}
	}
	for _, u := range mentionedAccounts(markup) {
		recipients[u] = true
	}
	for u := range recipients {
		if u == "" || u == author {
			continue
		}
		ctxID := s.ensureNotifyContext(u, parentID, objClass, objSpace, now)
		s.putActivityNotification(u, ctxID, parentID, objClass, msgID, clChatMessage, now)
	}
}

// notifyAssignee generates the "assigned you" notification when a Tx sets a doc's
// assignee: it auto-subscribes the assignee (DocNotifyContext), synthesizes the
// DocUpdateMessage the notification attaches to (so the feed renders an activity
// line), and files one ActivityInboxNotification. A self-assignment notifies no
// one.
func (s *session) notifyAssignee(t, doc map[string]any) {
	assignee := accountFromRef(str(doc["assignee"]))
	if assignee == "" {
		return
	}
	author := accountFromRef(str(firstNonNil(t["modifiedBy"], doc["modifiedBy"])))
	if assignee == author {
		return
	}
	objID := str(doc["_id"])
	objClass := str(doc["_class"])
	objSpace := str(doc["space"])
	if objID == "" || objClass == "" {
		return
	}
	now := asInt64(firstNonNil(t["modifiedOn"], doc["modifiedOn"]))
	if now == 0 {
		now = time.Now().UnixMilli()
	}

	ctxID := s.ensureNotifyContext(assignee, objID, objClass, objSpace, now)

	// Synthesize the activity line the notification attaches to. Keyed on the Tx id
	// (or objID) so re-applying the same assignment Tx converges rather than piling
	// up duplicate activity messages.
	updID := "aupd:" + pick(str(t["_id"]), objID) + ":assignee:" + assignee
	if !s.exists(updID) {
		upd := map[string]any{
			"_id": updID, "_class": clDocUpdateMessage, "space": objSpace,
			"attachedTo": objID, "attachedToClass": objClass, "collection": "docUpdateMessages",
			"objectId": objID, "objectClass": objClass, "action": "update", "txId": str(t["_id"]),
			"modifiedBy": acctSystem, "modifiedOn": now, "createdBy": acctSystem, "createdOn": now,
		}
		_ = s.store.put(s.org, s.workspace, upd)
		s.pushDerivedTx(clTxCreate, updID, clDocUpdateMessage, objSpace, map[string]any{"attributes": map[string]any{
			"attachedTo": objID, "attachedToClass": objClass, "collection": "docUpdateMessages",
			"objectId": objID, "objectClass": objClass, "action": "update", "txId": str(t["_id"]),
		}})
	}
	s.putActivityNotification(assignee, ctxID, objID, objClass, updID, clDocUpdateMessage, now)
}

// ensureNotifyContext upserts the recipient's DocNotifyContext for an object and
// bumps its lastUpdateTimestamp (so the Inbox surfaces it as freshly-updated),
// returning the context id. It is the auto-subscribe primitive: a user notified
// about a doc they were not yet watching gets a context so the notification can be
// grouped under it (the Inbox drops notifications whose context it has not loaded).
// The doc shape is IDENTICAL to projectNotifyContexts', so a chat-space member and
// a notification recipient converge on the same context (shared dncID).
func (s *session) ensureNotifyContext(user, objectID, objectClass, objectSpace string, touch int64) string {
	id := dncID(user, objectID)
	now := time.Now().UnixMilli()
	if existing, _ := s.store.get(s.org, s.workspace, id); existing != nil {
		existing["lastUpdateTimestamp"] = touch
		existing["modifiedOn"] = now
		_ = s.store.put(s.org, s.workspace, existing)
		s.pushDerivedTx(clTxUpdate, id, clDocNotifyContext, str(existing["space"]), map[string]any{
			"operations": map[string]any{"lastUpdateTimestamp": touch},
		})
		return id
	}
	ctxSpace := s.personSpaceOf(user)
	ctx := map[string]any{
		"_id": id, "_class": clDocNotifyContext, "space": ctxSpace,
		"user": user, "objectId": objectID, "objectClass": objectClass, "objectSpace": objectSpace,
		"isPinned": false, "hidden": false, "lastUpdateTimestamp": touch,
		"modifiedBy": acctSystem, "modifiedOn": now, "createdBy": acctSystem, "createdOn": now,
	}
	_ = s.store.put(s.org, s.workspace, ctx)
	s.pushDerivedTx(clTxCreate, id, clDocNotifyContext, ctxSpace, map[string]any{"attributes": map[string]any{
		"user": user, "objectId": objectID, "objectClass": objectClass, "objectSpace": objectSpace,
		"isPinned": false, "hidden": false, "lastUpdateTimestamp": touch,
		"createdBy": acctSystem, "createdOn": now,
	}})
	return id
}

// putActivityNotification files one ActivityInboxNotification for a recipient,
// attached to an ActivityMessage (the ChatMessage or DocUpdateMessage). The id is
// derived from (attachedTo, user) so it is idempotent AND a re-projection never
// clobbers a read flag the recipient already set — an existing notification is left
// untouched.
func (s *session) putActivityNotification(user, ctxID, objectID, objectClass, attachedTo, attachedToClass string, createdOn int64) {
	if user == "" || ctxID == "" || attachedTo == "" {
		return
	}
	id := "notif:" + attachedTo + ":" + user
	if s.exists(id) {
		return // idempotent — preserve isViewed/archived the recipient set
	}
	space := s.personSpaceOf(user)
	notif := map[string]any{
		"_id": id, "_class": clActivityInboxNotif, "space": space,
		"user": user, "isViewed": false, "archived": false,
		"docNotifyContext": ctxID, "objectId": objectID, "objectClass": objectClass,
		"attachedTo": attachedTo, "attachedToClass": attachedToClass,
		"modifiedBy": acctSystem, "modifiedOn": createdOn, "createdBy": acctSystem, "createdOn": createdOn,
	}
	_ = s.store.put(s.org, s.workspace, notif)
	s.pushDerivedTx(clTxCreate, id, clActivityInboxNotif, space, map[string]any{"attributes": map[string]any{
		"user": user, "isViewed": false, "archived": false,
		"docNotifyContext": ctxID, "objectId": objectID, "objectClass": objectClass,
		"attachedTo": attachedTo, "attachedToClass": attachedToClass,
		"createdBy": acctSystem, "createdOn": createdOn,
	}})
}

// intersect returns the elements of `a` that also appear in `b` (order of `a`,
// de-duplicated) — mentioned accounts scoped to the space's actual members.
func intersect(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, x := range b {
		set[x] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, x := range a {
		if set[x] && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
