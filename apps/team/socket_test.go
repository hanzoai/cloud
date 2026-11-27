package team

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/team/token"
)

// socketFor is the session serveWS would open for a person of the cast's
// space: a CLIENT, with the role IAM holds for them.
func socketFor(c *cast, account, role string) *session {
	srv := mounted.State.trans
	return &session{server: srv, store: srv.store, hier: srv.hier, org: c.org, space: c.ws,
		account: account, role: role, client: true}
}

// rpc runs one JSON-RPC call through the socket's own dispatch.
func rpc(t *testing.T, s *session, method string, params ...any) map[string]any {
	t.Helper()
	req, _ := json.Marshal(request{ID: 3, Method: method, Params: rawParams(params...)})
	var out map[string]any
	if err := json.Unmarshal(s.handle(req), &out); err != nil {
		t.Fatalf("%s reply: %v", method, err)
	}
	return out
}

// found is the ids a findAll answered.
func found(t *testing.T, s *session, class string, query map[string]any) []string {
	t.Helper()
	res, _ := rpc(t, s, "findAll", class, query)["result"].(map[string]any)
	var ids []string
	for _, d := range anySlice(res["value"]) {
		if m, ok := d.(map[string]any); ok {
			ids = append(ids, str(m["_id"]))
		}
	}
	return ids
}

// TestSocketReadsAreWhatThePersonMaySee: over the socket, a guest cannot read
// a DM they are not in, its messages, or anybody's inbox; the people in the DM
// can; and a push of a DM message reaches only them.
func TestSocketReadsAreWhatThePersonMaySee(t *testing.T) {
	c := newCast(t)
	var direct teamDirect
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/dms", c.ada,
		map[string]any{"space": c.ws, "members": []string{c.bobID}}, &direct)
	m := c.say(t, c.ada, direct.Channel.ID, "just us")

	gus := socketFor(c, c.gusID, roleGuest)
	bob := socketFor(c, c.bobID, roleMember)
	if ids := found(t, gus, clDirectMessage, map[string]any{}); len(ids) != 0 {
		t.Fatalf("a guest's socket read DMs %v", ids)
	}
	if ids := found(t, gus, clChatMessage, map[string]any{"attachedTo": direct.Channel.ID}); len(ids) != 0 {
		t.Fatalf("a guest's socket read the DM's messages %v", ids)
	}
	if ids := found(t, gus, clDocNotifyContext, map[string]any{}); len(ids) != 0 {
		t.Fatalf("a guest's socket read notify contexts %v", ids)
	}
	if ids := found(t, gus, clActivityInboxNotif, map[string]any{"user": c.bobID}); len(ids) != 0 {
		t.Fatalf("a guest's socket read bob's inbox %v", ids)
	}
	if ids := found(t, bob, clChatMessage, map[string]any{"attachedTo": direct.Channel.ID}); len(ids) != 1 || ids[0] != m.ID {
		t.Fatalf("bob's socket read %v, want the DM's message", ids)
	}
	for _, id := range found(t, bob, clDocNotifyContext, map[string]any{}) {
		if !strings.HasPrefix(id, "dnc:"+c.bobID+":") {
			t.Fatalf("bob's socket read somebody else's notify context %s", id)
		}
	}
	one, _ := rpc(t, gus, "findOne", clDirectMessage, map[string]any{"_id": direct.Channel.ID})["result"].(map[string]any)
	if one != nil {
		t.Fatalf("findOne handed a guest the DM: %v", one)
	}

	push := mustJSON(attachedCreateTx("x1", clChatMessage, direct.Channel.ID, direct.Channel.ID, clDirectMessage,
		"messages", "hanzo:"+c.adaID, map[string]any{"message": "{}"}))
	if gus.hears(push, newSight()) {
		t.Fatal("a guest's socket would be pushed a DM message")
	}
	if !bob.hears(push, newSight()) {
		t.Fatal("bob's socket would not be pushed his own DM's message")
	}
}

// TestSocketWritesAreThePersons: a tx names its author from the session
// whatever it claims, and a write into a channel the person cannot see is refused
// whole.
func TestSocketWritesAreThePersons(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	var direct teamDirect
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/dms", c.ada,
		map[string]any{"space": c.ws, "members": []string{c.bobID}}, &direct)

	bob := socketFor(c, c.bobID, roleMember)
	forged := attachedCreateTx("forged-1", clChatMessage, general.ID, general.ID, clChannel,
		"messages", "hanzo:"+c.adaID, map[string]any{"message": textMarkup("it was ada", nil)})
	forged["createdBy"] = "hanzo:" + c.adaID
	if out := rpc(t, bob, "tx", forged); out["error"] != nil {
		t.Fatalf("bob's own post was refused: %v", out)
	}
	stored, _ := mounted.State.trans.store.get(c.org, c.ws, "forged-1")
	if stored == nil || str(stored["createdBy"]) != "hanzo:"+c.bobID || str(stored["modifiedBy"]) != "hanzo:"+c.bobID {
		t.Fatalf("stored = %v, want it authored by bob, whatever the tx claimed", stored)
	}

	gus := socketFor(c, c.gusID, roleGuest)
	intrude := attachedCreateTx("intrude-1", clChatMessage, direct.Channel.ID, direct.Channel.ID, clDirectMessage,
		"messages", "hanzo:"+c.gusID, map[string]any{"message": textMarkup("hi", nil)})
	if out := rpc(t, gus, "tx", intrude); out["error"] == nil {
		t.Fatalf("a guest's post into a DM they are not in was accepted: %v", out)
	}
	if d, _ := mounted.State.trans.store.get(c.org, c.ws, "intrude-1"); d != nil {
		t.Fatal("the refused post landed")
	}
	// Nor may anybody join a private channel they cannot see by writing its members.
	secret := c.channel(t, "secret", true)
	join := updateTx(secret.ID, clChannel, spaceChannels, "hanzo:"+c.bobID, map[string]any{"members": []string{c.adaID, c.bobID}})
	if out := rpc(t, bob, "tx", join); out["error"] == nil {
		t.Fatalf("bob wrote himself into a private channel: %v", out)
	}
	// Nor forge a notification for somebody else.
	note := createTx("notif:x:"+c.adaID, clActivityInboxNotif, "core:space:Workspace", "hanzo:"+c.bobID,
		map[string]any{"user": c.adaID, "isViewed": false})
	if out := rpc(t, bob, "tx", note); out["error"] == nil {
		t.Fatalf("bob filed a notification for ada: %v", out)
	}
}

// TestSocketIsRefusedToANonMember: a space token for somebody who holds no
// member row in the space opens no socket — a token outliving a removal is not
// a way back in.
func TestSocketIsRefusedToANonMember(t *testing.T) {
	c := newCast(t)
	tok, err := token.Generate(c.malID, c.ws, map[string]any{"org": c.org}, time.Now().Add(time.Hour).Unix(), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, c.app, http.MethodGet, "/v1/team/transactor/"+tok, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("a non-member's space token = %d, want 401", code)
	}
	member, err := token.Generate(c.bobID, c.ws, map[string]any{"org": c.org}, time.Now().Add(time.Hour).Unix(), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, c.app, http.MethodGet, "/v1/team/transactor/"+member, nil, nil); code == http.StatusUnauthorized {
		t.Fatal("a member's space token was refused before the upgrade")
	}
}

// TestSocketWritesAreJudgedAsTheyWillBe: an update is admitted only if the
// person can see the document before AND after, authorship cannot be
// rewritten, and changing somebody else's channel or message takes the right the
// typed ops require.
func TestSocketWritesAreJudgedAsTheyWillBe(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	var direct teamDirect
	c.must(t, http.StatusCreated, http.MethodPost, "/v1/team/dms", c.ada,
		map[string]any{"space": c.ws, "members": []string{c.bobID}}, &direct)
	guests := c.channel(t, "guests", true, c.gusID)
	gusMsg := c.say(t, c.gus, guests.ID, "mine")
	adaMsg := c.say(t, c.ada, general.ID, "ada's")
	bobMsg := c.say(t, c.bob, general.ID, "bob's")

	gus := socketFor(c, c.gusID, roleGuest)
	bob := socketFor(c, c.bobID, roleMember)
	refused := func(s *session, tx map[string]any, why string) {
		t.Helper()
		if out := rpc(t, s, "tx", tx); out["error"] == nil {
			t.Fatalf("%s was admitted: %v", why, out)
		}
	}
	// Moving his own message into a DM he is not in: visible before, not after.
	refused(gus, updateTx(gusMsg.ID, clChatMessage, guests.ID, "", map[string]any{"attachedTo": direct.Channel.ID, "space": direct.Channel.ID}),
		"a guest moving a message into a DM they cannot see")
	// Somebody else's message, somebody else's channel.
	refused(bob, updateTx(adaMsg.ID, clChatMessage, general.ID, "", map[string]any{"message": textMarkup("edited by bob", nil)}),
		"a member editing another person's message")
	refused(bob, map[string]any{"_class": clTxRemove, "objectId": adaMsg.ID, "objectClass": clChatMessage, "objectSpace": general.ID},
		"a member removing another person's message")
	refused(bob, updateTx(general.ID, clChannel, spaceChannels, "", map[string]any{"name": "bobs-now"}),
		"a member renaming a channel somebody else owns")
	refused(bob, updateTx(general.ID, clChannel, spaceChannels, "", map[string]any{"members": []string{c.adaID, c.bobID, c.gusID}}),
		"a member adding somebody else to a channel somebody else owns")
	// Leaving is his own business.
	if out := rpc(t, bob, "tx", updateTx(general.ID, clChannel, spaceChannels, "", map[string]any{"members": []string{c.adaID}})); out["error"] != nil {
		t.Fatalf("a member leaving a channel was refused: %v", out)
	}
	// Authorship is not an operation: a createdBy in an update is dropped.
	if out := rpc(t, bob, "tx", updateTx(bobMsg.ID, clChatMessage, general.ID, "", map[string]any{
		"createdBy": "hanzo:" + c.adaID, "message": textMarkup("still bob's", nil)})); out["error"] != nil {
		t.Fatalf("bob editing his own message was refused: %v", out)
	}
	stored, _ := mounted.State.trans.store.get(c.org, c.ws, bobMsg.ID)
	if str(stored["createdBy"]) != "hanzo:"+c.bobID {
		t.Fatalf("createdBy = %v, want bob's — an update may not rewrite authorship", stored["createdBy"])
	}
}

// TestSocketGuestsGetNoOrgSearch: a guest's socket is answered with nothing
// before the org's search index is asked.
func TestSocketGuestsGetNoOrgSearch(t *testing.T) {
	c := newCast(t)
	res, _ := rpc(t, socketFor(c, c.gusID, roleGuest), "searchFulltext", "anything")["result"].(map[string]any)
	if res == nil || len(anySlice(res["docs"])) != 0 {
		t.Fatalf("a guest's search = %v, want no hits", res)
	}
}

// TestSocketDropsAPersonTheSpaceNoLongerHolds: on its next call a socket
// re-asks IAM and a person who holds no member row is refused.
func TestSocketDropsAPersonTheSpaceNoLongerHolds(t *testing.T) {
	c := newCast(t)
	gone := socketFor(c, c.malID, roleMember) // opened when they were a member; checked long ago
	if out := rpc(t, gone, "findAll", clChannel, map[string]any{}); out["error"] == nil {
		t.Fatalf("a socket whose person the space no longer holds still answered: %v", out)
	}
	if out := rpc(t, socketFor(c, c.bobID, roleMember), "findAll", clChannel, map[string]any{}); out["error"] != nil {
		t.Fatalf("a member's socket was refused: %v", out)
	}
}

// TestSocketPersonRecordsAreTheirOwn: a person record and a social identity
// are changed only by that person (or a space admin): nobody renames somebody
// else, flips their Employee mixin, or attaches another account's identity to
// themselves.
func TestSocketPersonRecordsAreTheirOwn(t *testing.T) {
	c := newCast(t)
	bob := socketFor(c, c.bobID, roleMember)
	// The roster projection writes the people the workbench shows.
	for _, id := range []string{c.adaID, c.bobID} {
		sess := &session{server: mounted.State.trans, store: mounted.State.trans.store, hier: mounted.State.trans.hier,
			org: c.org, space: c.ws, account: acctSystem}
		if _, err := apply(sess, mounted.State.trans, c.ws, MemberTxes(Member{UserID: id, Name: id, Active: true}, false)...); err != nil {
			t.Fatal(err)
		}
	}
	refused := func(tx map[string]any, why string) {
		t.Helper()
		if out := rpc(t, bob, "tx", tx); out["error"] == nil {
			t.Fatalf("%s was admitted: %v", why, out)
		}
	}
	refused(updateTx(PersonRef(c.adaID), clPerson, spaceContacts, "", map[string]any{"name": "Mallory,Ada"}),
		"renaming somebody else")
	refused(mixinTx(PersonRef(c.adaID), clPerson, spaceContacts, mixinEmployee, "", map[string]any{"active": false}),
		"deactivating somebody else")
	refused(attachedCreateTx("hanzo:"+c.adaID+"-x", clSocialIdentity, spaceContacts, PersonRef(c.bobID), clPerson, "socialIds", "",
		map[string]any{"key": "hanzo:" + c.adaID, "type": "hanzo", "value": c.adaID}),
		"claiming another account's identity")
	refused(createTx("person-new", clPerson, spaceContacts, "", map[string]any{"name": "x", "personUuid": c.adaID}),
		"creating a person record for another account")
	if out := rpc(t, bob, "tx", updateTx(PersonRef(c.bobID), clPerson, spaceContacts, "", map[string]any{"city": "Oslo"})); out["error"] != nil {
		t.Fatalf("bob editing his own person record was refused: %v", out)
	}
	if out := rpc(t, socketFor(c, c.adaID, roleOwner), "tx",
		updateTx(PersonRef(c.bobID), clPerson, spaceContacts, "", map[string]any{"city": "Bergen"})); out["error"] != nil {
		t.Fatalf("the space's owner editing a person record was refused: %v", out)
	}
}

// TestSocketCheckAndWriteDoNotDeadlockUnderLoad: sockets posting while typed
// ops post into the same space all land — the socket's held lock and the typed
// path's per-document lock never wait on each other forever.
func TestSocketCheckAndWriteDoNotDeadlockUnderLoad(t *testing.T) {
	c := newCast(t)
	general := c.channel(t, "general", false, c.bobID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := range 10 {
			wg.Add(2)
			go func() {
				defer wg.Done()
				bob := socketFor(c, c.bobID, roleMember)
				rpc(t, bob, "tx", attachedCreateTx(newMsgID(), clChatMessage, general.ID, general.ID, clChannel,
					"messages", "", map[string]any{"message": textMarkup("socket", nil)}))
			}()
			go func() {
				defer wg.Done()
				_, _ = call(t, c.app, http.MethodPost, "/v1/team/channels/"+general.ID+"/messages", c.ada,
					map[string]any{"space": c.ws, "text": "typed " + string(rune('a'+i))})
			}()
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("socket and typed writers deadlocked")
	}
	channel, _ := mounted.State.trans.store.get(c.org, c.ws, general.ID)
	if n, _ := asFloat(channel["messages"]); n != 20 {
		t.Fatalf("messages counter = %v, want 20", n)
	}
}
