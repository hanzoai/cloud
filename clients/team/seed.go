package team

// This file seeds a brand-new workspace's system spaces and runs the server-side
// triggers (PersonSpace materialization) — ported VERBATIM from
// github.com/hanzoai/team-go/pkg/transactor/seed.go.

import (
	"encoding/json"
	"time"
)

// System identity used as modifiedBy/createdBy for seeded + trigger docs.
const acctSystem = "core:account:System"

// systemSpaces are the space ids the real platform bootstrap creates via
// migration (NOT in the model). They must exist so findOne(core.class.Space,
// {_id}) and space-scoped queries resolve. Seeded once per workspace on first
// connect.
func systemSpaces() []map[string]any {
	now := time.Now().UnixMilli()
	base := func(id, class, name string, extra map[string]any) map[string]any {
		d := map[string]any{
			"_id": id, "_class": class, "space": "core:space:Space",
			"name": name, "description": "", "private": false,
			"archived": false, "members": []any{},
			"modifiedBy": acctSystem, "modifiedOn": now,
			"createdBy": acctSystem, "createdOn": now,
		}
		for k, v := range extra {
			d[k] = v
		}
		return d
	}
	return []map[string]any{
		base("core:space:Space", "core:class:TypedSpace", "Spaces", map[string]any{"type": "core:spaceType:SpacesType"}),
		base("core:space:Tx", "core:class:SystemSpace", "Space for all txes", nil),
		base("core:space:DerivedTx", "core:class:SystemSpace", "Space for derived txes", nil),
		base("core:space:Model", "core:class:SystemSpace", "Space for model", nil),
		base("core:space:Configuration", "core:class:SystemSpace", "Space for config", nil),
		base("core:space:Workspace", "core:class:SystemSpace", "Space for common things", nil),
		base("contact:space:Contacts", "core:class:SystemSpace", "Contacts", nil),
	}
}

// seedWorkspace writes the system spaces into a brand-new workspace exactly once
// (count==0). Idempotent: put is an upsert keyed by _id.
func (s *session) seedWorkspace() {
	n, err := s.store.count(s.org, s.workspace)
	if err != nil || n > 0 {
		return
	}
	for _, sp := range systemSpaces() {
		_ = s.store.put(s.org, s.workspace, sp)
	}
}

// trigger emulates the server-side reactions the real transactor runs on certain
// writes. Today: OnEmployeeCreate -> a per-person PersonSpace, which is what the
// workbench's findOne(PersonSpace,{person}) needs after login (without it the
// console logs "Failed to find space for employee"); and the chunter →
// DocNotifyContext projection — the chat navigator lists EXACTLY the contexts of
// the signed-in account (InboxNotificationsClient queries {user}), which upstream
// server triggers materialize. Without it every channel/DM vanishes from the nav
// on reload even though the doc persisted.
func (s *session) trigger(doc map[string]any) {
	if sub, ok := doc["contact:mixin:Employee"].(map[string]any); ok {
		if active, _ := sub["active"].(bool); active {
			s.ensurePersonSpace(str(doc["_id"]))
		}
	}
	switch str(doc["_class"]) {
	case clChannel, clDirectMessage:
		s.projectNotifyContexts(doc, 0)
	case clChatMessage:
		// A message bumps its channel's contexts (ordering + badges) and heals
		// contexts for channels created before this projection existed.
		if ch, _ := s.store.get(s.org, s.workspace, str(doc["attachedTo"])); ch != nil {
			if cls := str(ch["_class"]); cls == clChannel || cls == clDirectMessage {
				s.projectNotifyContexts(ch, time.Now().UnixMilli())
			}
		}
	}
}

// clChannel completes the chunter vocabulary chat.go carries.
const clChannel = "chunter:class:Channel"

const clDocNotifyContext = "notification:class:DocNotifyContext"

// projectNotifyContexts reconciles the DocNotifyContext set of one chat space
// (Channel or DirectMessage) to EXACTLY its members: absent contexts are created,
// contexts of departed members removed, and — when touch > 0 — surviving members'
// lastUpdateTimestamp is bumped. Every write is mirrored as a synthetic tx on
// s.derived so live sessions' context queries refresh (tx() broadcasts them).
// Idempotent: context ids derive from (member, space) so repeats converge, and a
// client-created context for the same (user, objectId) suppresses ours.
func (s *session) projectNotifyContexts(space map[string]any, touch int64) {
	spaceID := str(space["_id"])
	if spaceID == "" {
		return
	}
	members, _ := space["members"].([]any)
	now := time.Now().UnixMilli()
	existing := s.queryDocs(clDocNotifyContext, map[string]any{"objectId": spaceID})
	byUser := map[string][]map[string]any{}
	for _, c := range existing {
		u := str(c["user"])
		byUser[u] = append(byUser[u], c)
	}
	seen := map[string]bool{}
	for _, m := range members {
		user := str(m)
		if user == "" {
			continue
		}
		seen[user] = true
		if ctxs := byUser[user]; len(ctxs) > 0 {
			if touch > 0 {
				for _, ctx := range ctxs {
					ctx["lastUpdateTimestamp"] = touch
					ctx["modifiedOn"] = now
					_ = s.store.put(s.org, s.workspace, ctx)
					s.pushDerivedTx(clTxUpdate, str(ctx["_id"]), clDocNotifyContext, str(ctx["space"]), map[string]any{
						"operations": map[string]any{"lastUpdateTimestamp": touch},
					})
				}
			}
			continue
		}
		ctxSpace := "core:space:Workspace"
		if ps, _ := s.store.get(s.org, s.workspace, "person-space:person-"+user); ps != nil {
			ctxSpace = str(ps["_id"])
		}
		ctx := map[string]any{
			"_id": "dnc:" + user + ":" + spaceID, "_class": clDocNotifyContext, "space": ctxSpace,
			"user": user, "objectId": spaceID, "objectClass": space["_class"], "objectSpace": space["space"],
			"isPinned": false, "hidden": false, "lastUpdateTimestamp": now,
			"modifiedBy": acctSystem, "modifiedOn": now, "createdBy": acctSystem, "createdOn": now,
		}
		_ = s.store.put(s.org, s.workspace, ctx)
		attrs := map[string]any{}
		for k, v := range ctx {
			if k != "_id" && k != "_class" && k != "space" && k != "modifiedBy" && k != "modifiedOn" {
				attrs[k] = v
			}
		}
		s.pushDerivedTx(clTxCreate, str(ctx["_id"]), clDocNotifyContext, ctxSpace, map[string]any{"attributes": attrs})
	}
	for user, ctxs := range byUser {
		if seen[user] {
			continue
		}
		for _, ctx := range ctxs {
			_ = s.store.del(s.org, s.workspace, str(ctx["_id"]))
			s.pushDerivedTx(clTxRemove, str(ctx["_id"]), clDocNotifyContext, str(ctx["space"]), nil)
		}
	}
}

// pushDerivedTx appends one synthetic CUD tx (already-applied; broadcast-only) to
// the session's derived list. extra carries the class-specific fields
// (attributes / operations).
func (s *session) pushDerivedTx(txClass, objectID, objectClass, objectSpace string, extra map[string]any) {
	now := time.Now().UnixMilli()
	tx := map[string]any{
		"_id": newMsgID(), "_class": txClass, "space": "core:space:DerivedTx",
		"objectId": objectID, "objectClass": objectClass, "objectSpace": objectSpace,
		"modifiedBy": acctSystem, "modifiedOn": now, "createdBy": acctSystem, "createdOn": now,
	}
	for k, v := range extra {
		tx[k] = v
	}
	b, err := json.Marshal(tx)
	if err != nil {
		return
	}
	s.derived = append(s.derived, b)
}

// ensurePersonSpace creates the PersonSpace for a person if absent. The _id is
// derived from the person ref so repeated Employee txes converge (upsert).
func (s *session) ensurePersonSpace(person string) {
	if person == "" {
		return
	}
	id := "person-space:" + person
	if existing, _ := s.store.get(s.org, s.workspace, id); existing != nil {
		return
	}
	now := time.Now().UnixMilli()
	_ = s.store.put(s.org, s.workspace, map[string]any{
		"_id": id, "_class": "contact:class:PersonSpace", "space": "core:space:Space",
		"name": "Personal space", "description": "", "private": true, "archived": false,
		"person": person, "members": []any{s.account},
		"modifiedBy": acctSystem, "modifiedOn": now,
		"createdBy": acctSystem, "createdOn": now,
	})
}
