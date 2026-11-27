package team

// This file applies platform Tx (Create/Update/Remove/Mixin/Collection/ApplyIf)
// to the per-space docs store. It is the write path both
// the live SPA (tx RPC) and the in-process projection (Apply) run through.

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// Tx class ids.
const (
	clTxUpdate     = "core:class:TxUpdateDoc"
	clTxRemove     = "core:class:TxRemoveDoc"
	clTxMixin      = "core:class:TxMixin"
	clTxApplyIf    = "core:class:TxApplyIf"
	clTxCollection = "core:class:TxCollectionCUD"
)

// applyTx applies one platform Tx to the space store and returns (txResult,
// appliedTxes). appliedTxes is the flat list of CUD txes that actually landed —
// the transactor broadcasts them so live queries refresh.
func (s *session) applyTx(raw json.RawMessage) (any, []json.RawMessage) {
	var t map[string]any
	if err := json.Unmarshal(raw, &t); err != nil {
		return map[string]any{}, nil
	}
	// A tx that did not land reports NOTHING applied, so nothing is broadcast
	// and a caller that asked for the write (apply) refuses instead of
	// answering a success the store never recorded: a store that failed, an
	// update or mixin of a document that is gone, a remove of one already gone.
	switch str(t["_class"]) {
	case clTxCreate:
		if !s.txCreate(t) {
			return map[string]any{}, nil
		}
		return map[string]any{}, []json.RawMessage{raw}
	case clTxUpdate:
		obj := s.txUpdate(t)
		if obj == nil {
			return map[string]any{}, nil
		}
		if b, _ := t["retrieve"].(bool); b {
			return map[string]any{"object": obj}, []json.RawMessage{raw}
		}
		return map[string]any{}, []json.RawMessage{raw}
	case clTxRemove:
		if !s.txRemove(t) {
			return map[string]any{}, nil
		}
		return map[string]any{}, []json.RawMessage{raw}
	case clTxMixin:
		if !s.txMixin(t) {
			return map[string]any{}, nil
		}
		return map[string]any{}, []json.RawMessage{raw}
	case clTxCollection:
		// Older shape: a wrapper carrying an inner tx + collection context.
		if inner, ok := t["tx"].(map[string]any); ok {
			inner["attachedTo"] = t["objectId"]
			inner["attachedToClass"] = t["objectClass"]
			inner["collection"] = t["collection"]
			ib, _ := json.Marshal(inner)
			return s.applyTx(ib)
		}
		return map[string]any{}, nil
	case clTxApplyIf:
		return s.txApplyIf(t)
	default:
		return map[string]any{}, nil
	}
}

// txCreate ports createDoc2Doc: build the stored Doc from the create tx. It
// reports whether the document was stored.
//
// A create whose id ALREADY EXISTS is an upsert of the same document, not a
// new child: it does not move the parent's counter a second time. The
// existence check and the write share the space's write lock, so two creates
// of one id at once count once.
func (s *session) txCreate(t map[string]any) bool {
	doc := createdDoc(t)
	unlock, ok := s.writeLock()
	if !ok {
		return false
	}
	was, _ := s.store.get(s.org, s.space, str(doc["_id"]))
	if was != nil {
		// A create over an existing id replaces the document's CONTENT, never
		// what it is or who made it: a DM re-created as a public channel would
		// open a private conversation to the whole space, and a re-create would
		// otherwise rewrite its authorship.
		if str(was["_class"]) != str(doc["_class"]) {
			_ = unlock()
			return false
		}
		doc["createdBy"], doc["createdOn"] = was["createdBy"], was["createdOn"]
	}
	if err := s.store.put(s.org, s.space, doc); err != nil {
		_ = unlock()
		return false
	}
	if was == nil {
		s.countChild(doc, 1) // the parent's counter for this child rises (collections.go)
	}
	if !s.released(unlock) {
		return false
	}
	s.trigger(doc)                 // emulate server triggers (PersonSpace, notify contexts)
	s.projectNotifications(t, doc) // fan mentions/comments/assignments into the Inbox feed
	return true
}

// writeLock takes the space's write lock for one document's read-modify-write
// and answers its release — or, when this session already HOLDS the lock
// (hold: the socket checking and applying a person's tx as one step), a
// release that does nothing, because the lock is not reentrant and the holder
// releases it once, at the end. ok is false when the lock could not be had, and
// then nothing was written.
//
// Until the release the session reads and writes the space through the store the lock
// answered (docStore.lock): on hanzo-sql the transaction that holds the lock, so
// the writes land together when it commits, and not before.
func (s *session) writeLock() (release func() error, ok bool) {
	if s.holding {
		return func() error { return nil }, true
	}
	base := s.store
	held, end, err := base.lock(s.org, s.space)
	if err != nil {
		s.lockFailed(err)
		return nil, false
	}
	s.store = held
	return func() error { s.store = base; return end() }, true
}

// hold takes the space's write lock for everything until the returned release:
// the socket's check of a tx and the tx's application are one step, so nothing
// another writer does can land between what was checked and what is written. ok is
// false when the lock could not be had.
func (s *session) hold() (release func() error, ok bool) {
	base := s.store
	held, end, err := base.lock(s.org, s.space)
	if err != nil {
		s.lockFailed(err)
		return nil, false
	}
	s.store, s.holding = held, true
	return func() error {
		s.holding, s.store = false, base
		return end()
	}, true
}

// released ends a held lock and reports whether what was written under it landed.
func (s *session) released(release func() error) bool {
	if err := release(); err != nil {
		s.lockFailed(err)
		return false
	}
	return true
}

// lockFailed says why a write was not made: its space's lock could not be taken, or
// its transaction did not commit.
func (s *session) lockFailed(err error) {
	if s.server != nil && s.server.log != nil {
		s.server.log.Warn("team: a write was not made", "org", s.org, "space", s.space, "err", err)
	}
}

// reserved is the document fields the TX states, never its attributes or
// operations: where a document lives, what it is attached to, what it is, and
// who wrote it. A create that carried any of them in its attributes used to
// store them — so a message could name a channel its tx never did, past every check
// that reads the tx. They are dropped from attributes on every create, and the
// identity and authorship ones from every update's operations.
var reserved = []string{
	"_id", "_class", "space", "attachedTo", "attachedToClass", "collection",
	"modifiedBy", "modifiedOn", "createdBy", "createdOn",
}

// immutable is what no update may change: what a document is, and who made it.
// Where it lives (space) and what it hangs off (attachedTo) may move — a page
// moves between teamspaces — and the write gate judges the document as it will
// be after the move.
var immutable = []string{"_id", "_class", "createdBy", "createdOn"}

// createdDoc is THE document a create tx stores — the one builder txCreate
// writes and the socket's write gate judges, so the gate reads exactly what
// would land.
func createdDoc(t map[string]any) map[string]any {
	doc := map[string]any{}
	if a, ok := t["attributes"].(map[string]any); ok {
		maps.Copy(doc, a)
	}
	for _, k := range reserved {
		delete(doc, k)
	}
	if at := str(t["attachedTo"]); at != "" {
		doc["attachedTo"] = t["attachedTo"]
		doc["attachedToClass"] = t["attachedToClass"]
		doc["collection"] = t["collection"]
	}
	doc["_id"] = t["objectId"]
	doc["_class"] = t["objectClass"]
	doc["space"] = t["objectSpace"]
	doc["modifiedBy"] = t["modifiedBy"]
	doc["modifiedOn"] = t["modifiedOn"]
	doc["createdBy"] = firstNonNil(t["createdBy"], t["modifiedBy"])
	doc["createdOn"] = firstNonNil(t["createdOn"], t["modifiedOn"])
	return doc
}

// updatedDoc is a document as an update tx would leave it — the same
// operations txUpdate applies, to a copy.
func updatedDoc(before map[string]any, t map[string]any) map[string]any {
	after := clone(before)
	if ops, ok := t["operations"].(map[string]any); ok {
		applyOps(after, scrub(ops))
	}
	return after
}

// scrub drops the immutable fields from an update's operations, including
// under an operator ($set-style keys are bare; $unset, $rename name fields).
func scrub(ops map[string]any) map[string]any {
	out := make(map[string]any, len(ops))
	for k, v := range ops {
		if slices.Contains(immutable, k) {
			continue
		}
		if fields, ok := v.(map[string]any); ok && strings.HasPrefix(k, "$") {
			kept := map[string]any{}
			for f, fv := range fields {
				if !slices.Contains(immutable, f) {
					kept[f] = fv
				}
			}
			v = kept
		}
		out[k] = v
	}
	return out
}

// clone is a deep copy of a stored document.
func clone(doc map[string]any) map[string]any {
	b, err := json.Marshal(doc)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// txRemove deletes a document and lowers its parent's counter, reporting
// whether there was a document to delete.
func (s *session) txRemove(t map[string]any) bool {
	unlock, ok := s.writeLock()
	if !ok {
		return false
	}
	gone, _ := s.store.get(s.org, s.space, str(t["objectId"]))
	if gone == nil {
		_ = unlock()
		return false
	}
	if err := s.store.del(s.org, s.space, str(t["objectId"])); err != nil {
		_ = unlock()
		return false
	}
	s.countChild(gone, -1) // the parent's counter for this child falls
	return s.released(unlock)
}

// txUpdate ports updateDoc2Doc/applyUpdate.
func (s *session) txUpdate(t map[string]any) map[string]any {
	unlock, ok := s.writeLock()
	if !ok {
		return nil
	}
	doc, _ := s.store.get(s.org, s.space, str(t["objectId"]))
	if doc == nil {
		_ = unlock()
		return nil
	}
	if ops, ok := t["operations"].(map[string]any); ok {
		applyOps(doc, scrub(ops))
	}
	doc["modifiedBy"] = t["modifiedBy"]
	doc["modifiedOn"] = t["modifiedOn"]
	if err := s.store.put(s.org, s.space, doc); err != nil {
		_ = unlock()
		return nil
	}
	if !s.released(unlock) {
		return nil
	}
	s.trigger(doc)                 // member joins/leaves reconcile the chat notify contexts
	s.projectNotifications(t, doc) // an assignee change fans an "assigned you" notification
	return doc
}

// txMixin ports updateMixin4Doc: mixin data lives under the mixin's class id
// key. It reports whether the document was there to carry it.
func (s *session) txMixin(t map[string]any) bool {
	mixin := str(t["mixin"])
	if mixin == "" {
		return false
	}
	unlock, ok := s.writeLock()
	if !ok {
		return false
	}
	doc, _ := s.store.get(s.org, s.space, str(t["objectId"]))
	if doc == nil {
		_ = unlock()
		return false
	}
	sub, _ := doc[mixin].(map[string]any)
	if sub == nil {
		sub = map[string]any{}
	}
	if attrs, ok := t["attributes"].(map[string]any); ok {
		applyOps(sub, attrs)
	}
	doc[mixin] = sub
	doc["modifiedBy"] = t["modifiedBy"]
	doc["modifiedOn"] = t["modifiedOn"]
	if err := s.store.put(s.org, s.space, doc); err != nil {
		_ = unlock()
		return false
	}
	if !s.released(unlock) {
		return false
	}
	s.trigger(doc)
	return true
}

// txApplyIf evaluates match/notMatch then applies txes atomically.
func (s *session) txApplyIf(t map[string]any) (any, []json.RawMessage) {
	if !s.condsPass(t["match"], true) || !s.condsPass(t["notMatch"], false) {
		return map[string]any{"success": false}, nil
	}
	var applied []json.RawMessage
	if txes, ok := t["txes"].([]any); ok {
		for _, inner := range txes {
			ib, _ := json.Marshal(inner)
			_, a := s.applyTx(ib)
			applied = append(applied, a...)
		}
	}
	return map[string]any{"success": true}, applied
}

// condsPass checks each {_class, query} condition. wantMatch=true requires ≥1
// match; false requires 0.
func (s *session) condsPass(raw any, wantMatch bool) bool {
	conds, ok := raw.([]any)
	if !ok {
		return true
	}
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		class := str(cm["_class"])
		query, _ := cm["query"].(map[string]any)
		// A condition is a read: for a person's socket it sees only what that
		// person may (socket.go), or its success flag would answer questions
		// about documents they cannot open.
		found := len(s.seen(s.queryDocs(class, query))) > 0
		if found != wantMatch {
			return false
		}
	}
	return true
}

// applyOps applies a set of update operations (mutates m in place). Bare keys are
// dot-path sets; $-keys are operators.
func applyOps(m map[string]any, ops map[string]any) {
	for k, v := range ops {
		if strings.HasPrefix(k, "$") {
			applyOperator(m, k, v)
		} else {
			setObjectValue(m, k, v)
		}
	}
}

func applyOperator(m map[string]any, op string, arg any) {
	fields, ok := arg.(map[string]any)
	if !ok {
		return
	}
	switch op {
	case "$push":
		for f, v := range fields {
			arr, _ := m[f].([]any)
			if mod, ok := v.(map[string]any); ok {
				if each, ok := mod["$each"].([]any); ok {
					arr = append(arr, each...)
					m[f] = arr
					continue
				}
			}
			m[f] = append(arr, v)
		}
	case "$pull":
		for f, v := range fields {
			arr, _ := m[f].([]any)
			out := arr[:0]
			for _, item := range arr {
				if !deepEqual(item, v) {
					out = append(out, item)
				}
			}
			m[f] = out
		}
	case "$unset":
		for f := range fields {
			delete(m, f)
		}
	case "$inc":
		for f, v := range fields {
			cur, _ := asFloat(m[f])
			add, _ := asFloat(v)
			m[f] = cur + add
		}
	case "$rename":
		for from, to := range fields {
			if ts, ok := to.(string); ok {
				if val, ok := m[from]; ok {
					m[ts] = val
					delete(m, from)
				}
			}
		}
	}
}

// setObjectValue sets a (possibly dotted) path on m, creating intermediate maps.
func setObjectValue(m map[string]any, path string, value any) {
	segs := strings.Split(path, ".")
	cur := m
	for i := 0; i < len(segs)-1; i++ {
		next, ok := cur[segs[i]].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[segs[i]] = next
		}
		cur = next
	}
	cur[segs[len(segs)-1]] = value
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func firstNonNil(vs ...any) any {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}
