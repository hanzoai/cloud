package team

import (
	"encoding/json"
	"github.com/hanzoai/cloud/internal/sqltest"
	"strings"
	"testing"
)

// TestAttachedChildrenMoveTheirParentsCounters is the platform's collection
// trigger, through the ordinary write path: a reply raises the message's
// `replies` and stamps the thread, a reaction raises `reactions`, and removing
// the reaction lowers it again — each also published as a derived $inc, so a
// live client's copy moves with the store's.
func TestAttachedChildrenMoveTheirParentsCounters(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "acme", "11111111-1111-4111-8111-111111111111"
		_, sess, _ := rosterServer(t, org, human, "Ada", nil)
		sess.seedSpace()
		write := func(tx map[string]any) {
			t.Helper()
			raw, err := json.Marshal(tx)
			if err != nil {
				t.Fatal(err)
			}
			sess.applyTx(raw)
		}
		get := func(id string) map[string]any {
			t.Helper()
			d, err := sess.store.get(org, sess.space, id)
			if err != nil || d == nil {
				t.Fatalf("get %s: %v", id, err)
			}
			return d
		}
		write(createTx("ch", clChannel, spaceChannels, acctSystem, map[string]any{"name": "general", "members": []any{human}}))
		write(attachedCreateTx("m1", clChatMessage, "ch", "ch", clChannel, "messages", "hanzo:"+human, map[string]any{"message": "<p>hi</p>"}))
		if n, _ := asFloat(get("ch")["messages"]); n != 1 {
			t.Fatalf("channel messages = %v, want 1", n)
		}

		sess.derived = nil
		write(attachedCreateTx("r1", clThreadMessage, "ch", "m1", clChatMessage, "replies", "hanzo:"+human,
			map[string]any{"message": "<p>re</p>", "objectId": "ch", "objectClass": clChannel}))
		m := get("m1")
		if n, _ := asFloat(m["replies"]); n != 1 || asInt64(m["lastReply"]) == 0 {
			t.Fatalf("message after a reply = %v, want replies 1 and a lastReply", m)
		}
		if got := toStringSlice(m["repliedPersons"]); len(got) != 1 || got[0] != PersonRef(human) {
			t.Fatalf("repliedPersons = %v, want the replier once", got)
		}
		derived := string(joinRaw(sess.derived))
		if !strings.Contains(derived, `"$inc":{"replies":1}`) || !strings.Contains(derived, `"objectId":"m1"`) {
			t.Fatalf("derived = %s, want the $inc published for live clients", derived)
		}

		write(attachedCreateTx("x1", clReaction, "ch", "m1", clChatMessage, "reactions", "hanzo:"+human, map[string]any{"emoji": "👍"}))
		if n, _ := asFloat(get("m1")["reactions"]); n != 1 {
			t.Fatalf("reactions = %v, want 1", n)
		}
		write(removeTx(get("x1"), "hanzo:"+human))
		if n, _ := asFloat(get("m1")["reactions"]); n != 0 {
			t.Fatalf("reactions after remove = %v, want 0", n)
		}
		// A collection the model does not count moves nothing.
		write(attachedCreateTx("z1", clReaction, "ch", "m1", clChatMessage, "nonsense", "hanzo:"+human, map[string]any{"emoji": "x"}))
		if _, has := get("m1")["nonsense"]; has {
			t.Fatal("an undeclared collection grew a counter")
		}
	})
}

func joinRaw(raws []json.RawMessage) []byte {
	var out []byte
	for _, r := range raws {
		out = append(out, r...)
	}
	return out
}

// TestACreateOfAnExistingIDCountsOnce: re-applying a create is an upsert of
// the same child, not a second one, so the parent's counter moves once.
func TestACreateOfAnExistingIDCountsOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "acme", "11111111-1111-4111-8111-111111111111"
		_, sess, _ := rosterServer(t, org, human, "Ada", nil)
		sess.seedSpace()
		for _, tx := range []map[string]any{
			createTx("ch", clChannel, spaceChannels, acctSystem, map[string]any{"name": "g", "members": []any{human}}),
			attachedCreateTx("m1", clChatMessage, "ch", "ch", clChannel, "messages", "hanzo:"+human, map[string]any{"message": "x"}),
			attachedCreateTx("m1", clChatMessage, "ch", "ch", clChannel, "messages", "hanzo:"+human, map[string]any{"message": "x"}),
		} {
			raw, _ := json.Marshal(tx)
			sess.applyTx(raw)
		}
		ch, _ := sess.store.get(org, sess.space, "ch")
		if n, _ := asFloat(ch["messages"]); n != 1 {
			t.Fatalf("messages after a repeated create = %v, want 1", n)
		}
	})
}

// TestAWriteThatDoesNotLandIsRefused: an update, mixin or remove of a document
// that is gone applies nothing, and apply answers 409 instead of a success the
// store never recorded.
func TestAWriteThatDoesNotLandIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const org, human = "acme", "11111111-1111-4111-8111-111111111111"
		srv, sess, ws := rosterServer(t, org, human, "Ada", nil)
		sess.seedSpace()
		for _, tx := range []map[string]any{
			updateTx("gone", clChatMessage, "ch", acctSystem, map[string]any{"message": "y"}),
			mixinTx("gone", clChannel, spaceChannels, mixinChannel, acctSystem, map[string]any{"life": "bound"}),
			{"_class": clTxRemove, "objectId": "gone", "objectClass": clChatMessage, "objectSpace": "ch"},
		} {
			if _, err := apply(sess, srv, ws, tx); zipStatus(err) != 409 {
				t.Errorf("%s of a missing document = %v, want 409", tx["_class"], err)
			}
		}
	})
}
