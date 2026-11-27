package team

// socket.go holds the transactor socket to the rule the typed ops hold: a
// person reads and writes only what they may see, and every write is theirs.
//
// The Team client runs a whole space over one socket — findAll over any class,
// tx of any shape — and until this file the socket trusted it: a guest could
// findAll every direct message in the space, receive every message pushed to
// it, and write a tx whose modifiedBy named somebody else. The typed surface
// gates each op; this gates the socket by the same openTo rule (access.go), so
// there is one answer to "may this person see this" whichever door they came
// through.
//
// Only CLIENT sessions are filtered — the ones serveWS opens for a person. The
// sessions the server builds for its own writes (triggers, projections, the
// typed ops' apply) read the whole store, because a trigger reconciling every
// member's notify context must see every member's.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// roleNow is the socket person's role in the space as last read from IAM. It
// is read by the socket's own loop and by every writer's broadcast, and moved
// by current, so it is read under the session's seat lock.
func (s *session) roleNow() string {
	s.seatMu.Lock()
	defer s.seatMu.Unlock()
	return s.role
}

// current re-asks IAM whether the socket's person is still a member of the
// space, and with what role — ON ACTIVITY, at most once per eventsReadmit, the
// same pace the event stream keeps; never on a timer. False means they are not:
// the caller drops the socket, and a reconnect is refused at the door.
func (s *session) current() bool {
	s.seatMu.Lock()
	if s.gone {
		s.seatMu.Unlock()
		return false
	}
	if time.Since(s.checked) < eventsReadmit {
		s.seatMu.Unlock()
		return true
	}
	s.seatMu.Unlock()
	role, ok := "", false
	if s.server != nil && s.server.accounts != nil {
		role, ok = s.server.accounts.Membership(context.Background(), s.org, s.space, s.account)
	}
	s.seatMu.Lock()
	defer s.seatMu.Unlock()
	if !ok {
		s.gone = true
		return false
	}
	s.role, s.checked = role, time.Now()
	return true
}

// recheck is current for the broadcast path: it runs off the writer's
// goroutine, one at a time per socket, so a write never waits on IAM; a
// person found gone has their socket closed.
func (s *session) recheck() {
	s.seatMu.Lock()
	due := !s.checking && time.Since(s.checked) >= eventsReadmit
	if due {
		s.checking = true
	}
	s.seatMu.Unlock()
	if !due {
		return
	}
	go func() {
		ok := s.current()
		s.seatMu.Lock()
		s.checking = false
		s.seatMu.Unlock()
		if !ok {
			s.drop()
		}
	}()
}

// drop closes the socket; its loop ends and the hub forgets it.
func (s *session) drop() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// sight memoizes what one read or one broadcast has already decided, so a
// findAll over a channel's thousand messages resolves the channel once.
type sight struct {
	docs map[string]map[string]any
	open map[string]bool
}

func newSight() *sight { return &sight{docs: map[string]map[string]any{}, open: map[string]bool{}} }

// doc reads a document through the memo.
func (s *session) doc(id string, sg *sight) map[string]any {
	if d, ok := sg.docs[id]; ok {
		return d
	}
	d, _ := s.store.get(s.org, s.space, id)
	sg.docs[id] = d
	return d
}

// personal reports whether a class is one of a person's own rows — an inbox
// notification or a notify context — which only that person may read or write.
func (s *session) personal(class string) bool {
	return class == clDocNotifyContext || s.hier.isDerived(class, clInboxNotification)
}

// sees reports whether this session's person may read doc.
func (s *session) sees(doc map[string]any, sg *sight) bool {
	if !s.client {
		return true
	}
	if doc == nil {
		return false
	}
	if s.personal(str(doc["_class"])) {
		return str(doc["user"]) == s.account
	}
	return s.within(doc, sg, 0)
}

// within walks from a document to what decides who may see it — a channel, a
// teamspace, a document's teamspace, or the thing a message, reaction or file is
// attached to — and asks openTo of it. Anything that is simply the space's (a
// person, an issue, the model) is open to every member of the space.
func (s *session) within(doc map[string]any, sg *sight, depth int) bool {
	cls := str(doc["_class"])
	id := str(doc["_id"])
	switch {
	case isChannel(cls) || cls == clTeamspace:
		if seen, ok := sg.open[id]; ok && id != "" {
			return seen
		}
		ok := openTo(s.roleNow(), s.account, doc)
		if id != "" {
			sg.open[id] = ok
		}
		return ok
	case cls == clDocument:
		ts := s.doc(str(doc["space"]), sg)
		return ts != nil && s.within(ts, sg, depth+1)
	case s.attachedKind(cls):
		parent := str(doc["attachedTo"])
		if parent == "" {
			return true
		}
		if depth > 4 {
			return false // a chain this deep is stored data gone wrong; refuse it
		}
		p := s.doc(parent, sg)
		if p == nil {
			return false // its container is gone: nothing to show it under
		}
		return s.within(p, sg, depth+1)
	default:
		return true
	}
}

// attachedKind is the classes whose visibility is their container's: messages
// of every kind, reactions, files.
func (s *session) attachedKind(class string) bool {
	return class == clReaction || class == clAttachment || s.hier.isDerived(class, clActivityMessage)
}

// clActivityMessage is the root of every message kind — chat, thread, the
// activity lines a doc update synthesizes.
const clActivityMessage = "activity:class:ActivityMessage"

// seen filters read results to what this session's person may see.
func (s *session) seen(docs []map[string]any) []map[string]any {
	if !s.client {
		return docs
	}
	sg := newSight()
	out := docs[:0:0]
	for _, d := range docs {
		if !s.sees(d, sg) {
			continue
		}
		if lk, ok := d["$lookup"].(map[string]any); ok {
			d["$lookup"] = s.seenLookup(lk, sg)
		}
		out = append(out, d)
	}
	return out
}

// seenLookup filters a joined $lookup the same way: a join is a read.
func (s *session) seenLookup(lk map[string]any, sg *sight) map[string]any {
	out := make(map[string]any, len(lk))
	for k, v := range lk {
		switch j := v.(type) {
		case map[string]any:
			if s.sees(j, sg) {
				out[k] = j
			}
		case []map[string]any:
			kept := []map[string]any{}
			for _, d := range j {
				if s.sees(d, sg) {
					kept = append(kept, d)
				}
			}
			out[k] = kept
		default:
			out[k] = v
		}
	}
	return out
}

// hears reports whether one applied tx may be pushed to this session's person.
// A create is judged by the document it describes, an update or mixin by the
// document as it now stands, a remove by where the removed document lived.
func (s *session) hears(raw json.RawMessage, sg *sight) bool {
	if !s.client {
		return true
	}
	var t map[string]any
	if json.Unmarshal(raw, &t) != nil {
		return false
	}
	cls, id := str(t["objectClass"]), str(t["objectId"])
	switch str(t["_class"]) {
	case clTxCreate:
		return s.sees(createdDoc(t), sg)
	case clTxUpdate, clTxMixin:
		return s.sees(s.doc(id, sg), sg)
	case clTxRemove:
		if s.personal(cls) {
			return ownRow(id, s.account)
		}
		if home := s.doc(str(t["objectSpace"]), sg); home != nil && (isChannel(str(home["_class"])) || str(home["_class"]) == clTeamspace) {
			return s.within(home, sg, 0)
		}
		return true // a removal names an id and nothing of what it held
	}
	return false
}

// ownRow reports whether a removed notification or notify context was the
// person's own, read off the ids this service mints (dncID, putActivityNotification).
// A removal it cannot attribute is not pushed: the client drops it on reload.
func ownRow(id, account string) bool {
	return strings.HasPrefix(id, "dnc:"+account+":") || (strings.HasPrefix(id, "notif:") && strings.HasSuffix(id, ":"+account))
}

// stamp writes the session's own person as the author of a tx and of every tx
// it carries: modifiedBy always, createdBy wherever the tx states one. What the
// client sent in those fields is discarded — a write is the socket's person's.
func (s *session) stamp(t map[string]any) {
	who := "hanzo:" + s.account
	t["modifiedBy"] = who
	if _, has := t["createdBy"]; has || str(t["_class"]) == clTxCreate {
		t["createdBy"] = who
	}
	if inner, ok := t["tx"].(map[string]any); ok {
		s.stamp(inner)
	}
	if txes, ok := t["txes"].([]any); ok {
		for _, in := range txes {
			if m, ok := in.(map[string]any); ok {
				s.stamp(m)
			}
		}
	}
}

// admits reports whether this session's person may make the write t. A write
// is judged by the document as it IS and as it WILL BE: the person must be able
// to see both, a personal row must be theirs on both sides, and changing a channel,
// a message or a page somebody else owns takes the right the typed ops require
// (mayChange). A create over an id that already exists is an update of that
// document and is judged as one. A wrapper is admitted only if everything it
// carries is; the creates it carries are visible to the txes after them, as
// they will be once applied in order.
func (s *session) admits(t map[string]any, sg *sight) bool {
	switch str(t["_class"]) {
	case clTxApplyIf:
		txes, _ := t["txes"].([]any)
		for _, in := range txes {
			m, ok := in.(map[string]any)
			if !ok || !s.admits(m, sg) {
				return false
			}
		}
		return true
	case clTxCollection:
		inner, ok := t["tx"].(map[string]any)
		if !ok {
			return false
		}
		probe := map[string]any{}
		for k, v := range inner {
			probe[k] = v
		}
		probe["attachedTo"], probe["attachedToClass"] = t["objectId"], t["objectClass"]
		probe["collection"] = t["collection"]
		return s.admits(probe, sg)
	case clTxCreate:
		after := createdDoc(t)
		before := s.doc(str(after["_id"]), sg)
		if before != nil {
			// A create over an existing id changes content, never class: an
			// update may not change _class, and this is an update.
			if str(before["_class"]) != str(after["_class"]) || !s.sees(before, sg) || !s.mayChange(before, after, sg) {
				return false
			}
		} else if !s.mayCreate(after) {
			return false
		}
		if !s.sees(after, sg) {
			return false
		}
		sg.docs[str(after["_id"])] = after
		delete(sg.open, str(after["_id"]))
		return true
	case clTxUpdate:
		before := s.doc(str(t["objectId"]), sg)
		if before == nil {
			return true // nothing to change: the store refuses it, and nothing lands
		}
		after := updatedDoc(before, t)
		if !s.sees(before, sg) || !s.sees(after, sg) || !s.mayChange(before, after, sg) {
			return false
		}
		sg.docs[str(after["_id"])] = after
		delete(sg.open, str(after["_id"]))
		return true
	case clTxMixin:
		before := s.doc(str(t["objectId"]), sg)
		return before == nil || (s.sees(before, sg) && s.mayChange(before, before, sg))
	case clTxRemove:
		before := s.doc(str(t["objectId"]), sg)
		return before == nil || (s.sees(before, sg) && s.mayChange(before, nil, sg))
	}
	return true
}

// mayChange is the right to change (after != nil) or remove (after == nil) a
// document the person can already see — the typed ops' rule, on the socket:
//
//   - a channel or teamspace: its owners and the space's admins; a channel nobody
//     owns, anybody in it; a direct message, its people, but never its people
//     list. Anybody else may only join (a public one) or leave — a members change
//     that adds or removes themselves and nothing else.
//   - a message: its author, an owner of the channel it is in, or a space admin.
//   - a reaction or a file: the person who added it, or a space admin.
//   - a page: anybody who can see it may edit; removing it is its author's, a
//     teamspace owner's or a space admin's.
//   - anything else visible: anybody who can see it.
func (s *session) mayChange(before, after map[string]any, sg *sight) bool {
	if r := s.roleNow(); r == roleOwner || r == roleAdmin {
		return true
	}
	cls := str(before["_class"])
	mine := accountFromRef(str(firstNonNil(before["createdBy"], before["createBy"]))) == s.account
	switch {
	case isChannel(cls) || cls == clTeamspace:
		owners := toStringSlice(before["owners"])
		if slices.Contains(owners, s.account) {
			return true
		}
		if after == nil {
			return false
		}
		if cls == clDirectMessage {
			return sameSet(toStringSlice(before["members"]), toStringSlice(after["members"]))
		}
		if len(owners) == 0 && slices.Contains(toStringSlice(before["members"]), s.account) {
			return true
		}
		return onlySelf(before, after, s.account)
	case s.hier.isDerived(cls, clChatMessage):
		if mine {
			return true
		}
		channel := s.channelOf(before, sg)
		return channel != nil && slices.Contains(toStringSlice(channel["owners"]), s.account)
	case cls == clReaction || cls == clAttachment:
		return mine
	case cls == clDocument:
		if after != nil || mine {
			return true
		}
		ts := s.doc(str(before["space"]), sg)
		return ts != nil && slices.Contains(toStringSlice(ts["owners"]), s.account)
	case cls == clPerson || cls == clSocialIdentity:
		// A person record and a social identity are how the workbench knows who
		// somebody IS; only that person (or a space admin) changes theirs.
		return s.selfOwned(before) && (after == nil || s.selfOwned(after))
	}
	return true
}

// mayCreate is the right to create a document that does not exist yet. Most
// things any member who can see where they go may create; a person record or
// a social identity only for oneself, because attaching a `hanzo:<account>`
// identity to a person is how the workbench decides who an account is.
func (s *session) mayCreate(after map[string]any) bool {
	if r := s.roleNow(); r == roleOwner || r == roleAdmin {
		return true
	}
	switch str(after["_class"]) {
	case clPerson:
		// A contact with no account is anybody's to record; one naming an
		// account is that account's.
		return str(after["personUuid"]) == "" || s.selfOwned(after)
	case clSocialIdentity:
		return s.selfOwned(after)
	}
	return true
}

// selfOwned reports whether a person record or social identity is the
// session's person's own: their Person, or an identity attached to it that
// claims no other hanzo account.
func (s *session) selfOwned(doc map[string]any) bool {
	switch str(doc["_class"]) {
	case clPerson:
		return str(doc["personUuid"]) == s.account || str(doc["_id"]) == PersonRef(s.account)
	case clSocialIdentity:
		if str(doc["attachedTo"]) != PersonRef(s.account) {
			return false
		}
		key := "hanzo:" + s.account
		if str(doc["type"]) == "hanzo" && (str(doc["value"]) != s.account || str(doc["key"]) != key) {
			return false
		}
		return str(doc["_id"]) == key || !strings.HasPrefix(str(doc["_id"]), "hanzo:")
	}
	return false
}

// channelOf is the channel a message is in, following a reply to its thread's channel.
func (s *session) channelOf(msg map[string]any, sg *sight) map[string]any {
	for depth, cur := 0, msg; cur != nil && depth < 4; depth++ {
		if isChannel(str(cur["_class"])) {
			return cur
		}
		cur = s.doc(str(cur["attachedTo"]), sg)
	}
	return nil
}

// onlySelf reports whether before → after changes nothing but the person's own
// place in the members list: a join or a leave.
func onlySelf(before, after map[string]any, account string) bool {
	for k := range union(before, after) {
		if k == "members" || k == "modifiedBy" || k == "modifiedOn" {
			continue
		}
		if !deepEqual(before[k], after[k]) {
			return false
		}
	}
	was, now := toStringSlice(before["members"]), toStringSlice(after["members"])
	for _, m := range was {
		if m != account && !slices.Contains(now, m) {
			return false
		}
	}
	for _, m := range now {
		if m != account && !slices.Contains(was, m) {
			return false
		}
	}
	return true
}

// union is the keys of both documents.
func union(a, b map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// sameSet reports whether two id lists hold the same ids.
func sameSet(a, b []string) bool {
	x, y := dedupe(a), dedupe(b)
	if len(x) != len(y) {
		return false
	}
	for _, v := range x {
		if !slices.Contains(y, v) {
			return false
		}
	}
	return true
}
