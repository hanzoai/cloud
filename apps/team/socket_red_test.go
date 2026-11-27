package team

import (
	"net/http"
	"slices"
	"testing"
	"time"
)

// Red re-review: three ways a socket write or condition still reaches a channel
// the person cannot see. Each asserts the rule socket.go states; each fails
// today.

// redDM opens a DM between Ada and Bob with one message in it. Gus (a guest of
// the space, not in the DM) is the socket under test.
func redDM(t *testing.T) (*cast, teamChannel, *session) {
	t.Helper()
	c := newCast(t)
	var direct teamDirect
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/dms", c.ada,
		map[string]any{"space": c.ws, "members": []string{c.bobID}}, &direct)
	c.say(t, c.ada, direct.Channel.ID, "just us")
	return c, direct.Channel, socketFor(c, c.gusID, roleGuest)
}

// A create whose id already exists replaces that document. The create is
// admitted on its own attributes, so a person can re-create a channel they cannot
// see under the same id with themselves in it.
func TestRedSocketCreateOverAnInvisibleID(t *testing.T) {
	c, dm, gus := redDM(t)
	now := time.Now().UnixMilli()
	rpc(t, gus, "tx", map[string]any{
		"_class": clTxCreate, "objectId": dm.ID, "objectClass": clDirectMessage,
		"objectSpace": spaceChannels, "modifiedOn": now,
		"attributes": map[string]any{"members": []string{c.gusID}, "private": true},
	})
	after, _ := mounted.State.trans.store.get(c.org, c.ws, dm.ID)
	if slices.Contains(toStringSlice(after["members"]), c.gusID) {
		t.Fatalf("a create over a DM the guest cannot see replaced it: members now %v", after["members"])
	}
}

// attachedTo carried inside attributes (not on the tx) is stored by txCreate
// but never checked by admits, so a message lands in a channel its author
// cannot see.
func TestRedSocketAttachedToInAttributes(t *testing.T) {
	c, dm, gus := redDM(t)
	now := time.Now().UnixMilli()
	rpc(t, gus, "tx", map[string]any{
		"_class": clTxCreate, "objectId": "red-msg", "objectClass": clChatMessage,
		"objectSpace": dm.ID, "modifiedOn": now,
		"attributes": map[string]any{"message": "<p>hi</p>", "attachedTo": dm.ID,
			"attachedToClass": clDirectMessage, "collection": "messages"},
	})
	if got, _ := mounted.State.trans.store.get(c.org, c.ws, "red-msg"); got != nil && str(got["attachedTo"]) == dm.ID {
		t.Fatal("a guest wrote a message into a DM they are not in")
	}
}

// TxApplyIf evaluates its conditions over the whole store, so its success flag
// answers questions about documents the person cannot read.
func TestRedSocketApplyIfConditionsSeeOnlyWhatThePersonMay(t *testing.T) {
	_, dm, gus := redDM(t)
	probe := func(pattern string) any {
		res := rpc(t, gus, "tx", map[string]any{
			"_class": clTxApplyIf, "txes": []any{},
			"match": []any{map[string]any{"_class": clChatMessage, "query": map[string]any{
				"attachedTo": dm.ID, "message": map[string]any{"$like": pattern}}}},
		})
		r, _ := res["result"].(map[string]any)
		return r["success"]
	}
	if hit, miss := probe("%just%"), probe("%zzzz%"); hit != miss {
		t.Fatalf("an ApplyIf condition distinguished text in a DM the guest cannot read (%v vs %v)", hit, miss)
	}
}

// A create over an existing id is judged as an update, but an update may not
// change _class and this create can: a DM's participant re-creates it as a
// public channel with the same people, and the conversation is open to the
// whole space.
func TestRedSocketCreateOverKeepsTheClass(t *testing.T) {
	c, dm, _ := redDM(t)
	ada := socketFor(c, c.adaID, roleMember)
	rpc(t, ada, "tx", map[string]any{
		"_class": clTxCreate, "objectId": dm.ID, "objectClass": clChannel,
		"objectSpace": spaceChannels, "modifiedOn": time.Now().UnixMilli(),
		"attributes": map[string]any{"name": "open", "private": false,
			"members": []string{c.adaID, c.bobID}},
	})
	after, _ := mounted.State.trans.store.get(c.org, c.ws, dm.ID)
	if str(after["_class"]) != clDirectMessage {
		t.Fatalf("a create over a DM changed its class to %s", after["_class"])
	}
}
