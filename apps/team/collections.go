package team

// collections.go keeps the COUNTERS a parent document carries for its attached
// children — a message's `replies` and `reactions`, a message's `attachments`, a
// document's `comments` — and the thread facts a reply moves on the message it
// answers (`lastReply`, `repliedPersons`).
//
// These are the platform's own server triggers: the reference transactor
// increments `parent[collection]` on every attached create and decrements it on
// every attached remove, for exactly the attributes the model declares as a
// Collection, and the chunter trigger stamps a thread's last reply and who is in
// it. A client never writes them — the Team client renders "3 replies" from the
// number a trigger kept — so a transactor that does not run them shows every
// thread as empty, whichever client wrote the replies.
//
// Each is written the way every trigger here is: persisted beside the write that
// caused it, and recorded on the session as a DERIVED tx, so the Team client's
// socket and the event stream receive the same $inc the reference server would
// have sent.

import "time"

// The attached children a message carries, beside the chunter classes chat.go
// names: a reply, a reaction, a file.
const (
	clThreadMessage = "chunter:class:ThreadMessage"
	clReaction      = "activity:class:Reaction"
	clAttachment    = "attachment:class:Attachment"
)

// countChild moves the parent's counter for one attached child that was just
// created (delta +1) or removed (delta -1). A parent that is gone, a child that
// names no collection, and a collection the model does not count are all
// no-ops, as they are upstream.
func (s *session) countChild(child map[string]any, delta float64) {
	parentID, collection := str(child["attachedTo"]), str(child["collection"])
	if parentID == "" || collection == "" {
		return
	}
	parent, _ := s.store.get(s.org, s.space, parentID)
	if parent == nil || !s.hier.counts(str(parent["_class"]), collection) {
		return
	}
	cur, _ := asFloat(parent[collection])
	if cur+delta < 0 {
		delta = -cur // a count never goes below zero, whatever the history held
	}
	parent[collection] = cur + delta
	ops := map[string]any{"$inc": map[string]any{collection: delta}}
	if delta > 0 && s.hier.isDerived(str(child["_class"]), clThreadMessage) {
		// The chunter trigger: a reply stamps when its thread was last answered and
		// adds its author to the people in it, once.
		at := asInt64(firstNonNil(child["createdOn"], child["modifiedOn"]))
		if at == 0 {
			at = time.Now().UnixMilli()
		}
		parent["lastReply"] = at
		ops["lastReply"] = at
		if who := accountFromRef(str(child["createdBy"])); who != "" {
			person := PersonRef(who)
			if !containsAny(parent["repliedPersons"], person) {
				parent["repliedPersons"] = append(anySlice(parent["repliedPersons"]), person)
				ops["$push"] = map[string]any{"repliedPersons": person}
			}
		}
	}
	_ = s.store.put(s.org, s.space, parent)
	s.pushDerivedTx(clTxUpdate, parentID, str(parent["_class"]), str(parent["space"]), map[string]any{"operations": ops})
}

// containsAny reports whether a stored JSON array holds v.
func containsAny(arr any, v string) bool {
	for _, x := range anySlice(arr) {
		if x == v {
			return true
		}
	}
	return false
}

// anySlice is a stored JSON array, or nil.
func anySlice(v any) []any {
	arr, _ := v.([]any)
	return arr
}
