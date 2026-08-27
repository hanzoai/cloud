package team

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
)

// roomFixture builds a transactor bound to one seeded workspace and creates
// `n` named channels in it through the REAL write path (applyTx), so the tests
// below read exactly the documents a Team client would have produced.
func roomFixture(t *testing.T, org string, names ...string) (*roomBridge, *session, string) {
	t.Helper()
	srv, sess, ws := rosterServer(t, org, "u-1", "Human", nil)
	for i, name := range names {
		tx := map[string]any{
			"_class":      clTxCreate,
			"objectId":    "ch-" + name,
			"objectClass": clChannel,
			"objectSpace": "core:space:Space",
			"attributes":  map[string]any{"name": name, "private": false, "archived": false, "members": []any{"u-1"}},
			"modifiedBy":  acctSystem,
			"modifiedOn":  time.Now().UnixMilli() + int64(i),
		}
		raw, err := json.Marshal(tx)
		if err != nil {
			t.Fatalf("marshal create: %v", err)
		}
		sess.applyTx(raw)
	}
	return &roomBridge{trans: srv, accounts: srv.accounts}, sess, ws
}

// TestListRoomsReadsTheTransactorsOwnDocuments is the property the whole
// surface rests on: these ops are a second DOOR, never a second store. A channel
// created through the transactor write path appears in the REST listing with no
// sync step, because both read the same per-workspace docs table.
func TestListRoomsReadsTheTransactorsOwnDocuments(t *testing.T) {
	b, _, ws := roomFixture(t, "acme", "bugfix-1010", "general")
	ctx := orgCtx(t, "acme")

	got, err := b.listRooms(ctx, nil)
	if err != nil {
		t.Fatalf("listRooms: %v", err)
	}
	if len(got.Rooms) != 2 {
		t.Fatalf("channels = %d, want 2: %+v", len(got.Rooms), got.Rooms)
	}
	// Sorted by name, so the order is a fact a client can diff against.
	if got.Rooms[0].Name != "bugfix-1010" || got.Rooms[1].Name != "general" {
		t.Fatalf("order = %q,%q, want bugfix-1010,general", got.Rooms[0].Name, got.Rooms[1].Name)
	}
	c := got.Rooms[0]
	if c.Workspace != ws {
		t.Errorf("workspace = %q, want %q", c.Workspace, ws)
	}
	// An unclassified channel reads as persistent with no bindings — never as a
	// hole. Every channel that predates the facet takes this path.
	if c.Life != lifeStanding {
		t.Errorf("kind = %q, want %q for a channel carrying no facet", c.Life, lifeStanding)
	}
	if c.Bindings == nil || len(c.Bindings) != 0 {
		t.Errorf("bindings = %#v, want an empty list (not null)", c.Bindings)
	}
	if c.Members == nil {
		t.Error("members = nil, want an empty list — absent members must not marshal to null")
	}
}

// TestBindRoomRoundTrips proves the facet SURVIVES: it is written as a
// platform mixin through applyTx and read back off the stored document, so the
// value a caller set is the value the next reader sees.
func TestBindRoomRoundTrips(t *testing.T) {
	b, _, ws := roomFixture(t, "acme", "bugfix-1010")
	ctx := orgCtx(t, "acme")

	out, err := b.bindRoom(ctx, &teamRoomBind{
		ID: "ch-bugfix-1010", Workspace: ws, Life: lifeBound,
		Bindings: []string{"repo:hanzoai/cloud", "issue:1010"},
	})
	if err != nil {
		t.Fatalf("bindRoom: %v", err)
	}
	if out.Life != lifeBound {
		t.Errorf("kind = %q, want %q", out.Life, lifeBound)
	}
	if len(out.Bindings) != 2 || out.Bindings[0] != "repo:hanzoai/cloud" || out.Bindings[1] != "issue:1010" {
		t.Errorf("bindings = %#v, want the two stated", out.Bindings)
	}
	// Read it again through the LIST op, which reads the store rather than the
	// value bindRoom just returned — this is what proves it was persisted and
	// not merely echoed.
	got, err := b.listRooms(ctx, nil)
	if err != nil {
		t.Fatalf("listRooms: %v", err)
	}
	if got.Rooms[0].Life != lifeBound || len(got.Rooms[0].Bindings) != 2 {
		t.Fatalf("re-read = %+v, want the facet the bind wrote", got.Rooms[0])
	}
}

// TestBindRoomSurvivesASubsequentClientWrite is the reason the facet is a
// MIXIN rather than a table beside the document. The Team client goes on writing
// to the same channel; txUpdate MERGES operations into the stored document, so a
// rename must not take the work facet with it.
func TestBindRoomSurvivesASubsequentClientWrite(t *testing.T) {
	b, sess, ws := roomFixture(t, "acme", "bugfix-1010")
	ctx := orgCtx(t, "acme")

	if _, err := b.bindRoom(ctx, &teamRoomBind{
		ID: "ch-bugfix-1010", Workspace: ws, Life: lifeBound, Bindings: []string{"issue:1010"},
	}); err != nil {
		t.Fatalf("bindRoom: %v", err)
	}
	// A client renames the channel and archives it — an ordinary TxUpdateDoc, the
	// shape the SPA sends.
	raw, err := json.Marshal(map[string]any{
		"_class":     clTxUpdate,
		"objectId":   "ch-bugfix-1010",
		"operations": map[string]any{"name": "bugfix-1010-done", "archived": true},
		"modifiedBy": acctSystem,
		"modifiedOn": time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	sess.applyTx(raw)

	got, err := b.listRooms(ctx, nil)
	if err != nil {
		t.Fatalf("listRooms: %v", err)
	}
	c := got.Rooms[0]
	if c.Name != "bugfix-1010-done" {
		t.Errorf("name = %q, want the client's rename to have landed", c.Name)
	}
	// ARCHIVED IS THE PLATFORM'S, and this is the assertion that says so: the
	// client set it and the facet never held it, so there is one answer.
	if !c.Archived {
		t.Error("archived = false, want the client's archive to be what this surface reports")
	}
	if c.Life != lifeBound || len(c.Bindings) != 1 {
		t.Errorf("facet = (%q,%#v), want it untouched by an unrelated client write", c.Life, c.Bindings)
	}
}

// TestBindRoomRefusesAnotherOrgsWorkspace is the tenancy gate, and the FIRST
// version of this test passed with the gate deleted — worth recording, because
// the reason is the same one that makes the gate worth having.
//
// Cross-tenant ACCESS is already impossible without any check here: the store
// keys every file on cloud.OrgNamespace(org, workspace) and the org comes from
// the validated principal, so a caller naming another org's workspace uuid opens
// a DIFFERENT file — an empty one — and gets a 404 either way. Asserting only the
// 404 therefore measures the store's physical isolation, not this gate.
//
// What the gate uniquely buys is that the empty file is never CREATED. cek.Open
// materialises on first use, so without it any caller could name arbitrary
// workspace uuids and mint one encrypted database per guess, on a shared volume,
// unbounded. So the assertion is on the open-handle cache: a refused pair must
// leave no store behind.
func TestBindRoomRefusesAnotherOrgsWorkspace(t *testing.T) {
	b, _, ws := roomFixture(t, "acme", "bugfix-1010")

	// A caller validated as a DIFFERENT org names acme's workspace verbatim.
	_, err := b.bindRoom(orgCtx(t, "evil"), &teamRoomBind{
		ID: "ch-bugfix-1010", Workspace: ws, Life: lifeBound,
	})
	if err == nil {
		t.Fatal("bindRoom admitted a caller from another org")
	}
	// 404 and not 403: a caller who may not touch this workspace must not learn
	// from the status that it exists.
	if got := zipStatus(err); got != 404 {
		t.Errorf("status = %d, want 404 (a refusal must not be an existence oracle)", got)
	}
	// THE ASSERTION THAT BITES: the refusal opened no store for the unowned pair.
	evil, err := cloud.OrgNamespace("evil", ws)
	if err != nil {
		t.Fatalf("namespace: %v", err)
	}
	b.trans.store.mu.Lock()
	_, opened := b.trans.store.dbs[evil]
	b.trans.store.mu.Unlock()
	if opened {
		t.Error("a refused workspace was opened as a store — an unbounded file-creation path")
	}
	// And acme's document is untouched.
	got, err := b.listRooms(orgCtx(t, "acme"), nil)
	if err != nil {
		t.Fatalf("listRooms: %v", err)
	}
	if got.Rooms[0].Life != lifeStanding {
		t.Error("the refused write landed anyway")
	}
}

// TestBindRoomRefusesAnUnreadableLife keeps the vocabulary closed. HIP-0523 §2
// names exactly two lives; a third value stored here would be one every future
// reader has to interpret.
func TestBindRoomRefusesAnUnreadableLife(t *testing.T) {
	b, _, ws := roomFixture(t, "acme", "bugfix-1010")
	if _, err := b.bindRoom(orgCtx(t, "acme"), &teamRoomBind{
		ID: "ch-bugfix-1010", Workspace: ws, Life: "ephemeral",
	}); err == nil {
		t.Fatal("bindRoom stored an unknown life")
	}
}

// TestBindingsAreShapeCheckedAndNotResolved states the boundary exactly: a
// binding must be "<kind>:<ref>" so a reader can dispatch on the kind, and its
// REF is opaque here because the app that owns projects is the app that can say
// whether one exists.
func TestBindingsAreShapeCheckedAndNotResolved(t *testing.T) {
	b, _, ws := roomFixture(t, "acme", "bugfix-1010")
	ctx := orgCtx(t, "acme")

	for _, bad := range []string{"norefhere", "repo:", ":hanzoai/cloud"} {
		if _, err := b.bindRoom(ctx, &teamRoomBind{
			ID: "ch-bugfix-1010", Workspace: ws, Bindings: []string{bad},
		}); err == nil {
			t.Errorf("binding %q was accepted, want a refusal", bad)
		}
	}
	// A ref naming a project that does not exist is FINE — team does not resolve
	// it, and refusing here would put this app in the business of knowing every
	// other plane's nouns.
	if _, err := b.bindRoom(ctx, &teamRoomBind{
		ID: "ch-bugfix-1010", Workspace: ws, Bindings: []string{"project:no-such-project"},
	}); err != nil {
		t.Errorf("an unresolvable ref was refused: %v — shape is checked, existence is not", err)
	}
}

// TestBindingsReplaceRatherThanMerge pins the one semantic a caller has to know:
// an explicit list REPLACES, so a wrong binding can be corrected and an empty
// list unbinds — while an ABSENT list leaves the channel alone, so a caller that
// only sets the kind does not silently erase what the channel is about.
func TestBindingsReplaceRatherThanMerge(t *testing.T) {
	b, _, ws := roomFixture(t, "acme", "bugfix-1010")
	ctx := orgCtx(t, "acme")
	bind := func(in *teamRoomBind) *teamRoom {
		t.Helper()
		in.ID, in.Workspace = "ch-bugfix-1010", ws
		out, err := b.bindRoom(ctx, in)
		if err != nil {
			t.Fatalf("bindRoom: %v", err)
		}
		return out
	}

	bind(&teamRoomBind{Bindings: []string{"repo:a", "repo:b"}})

	// Absent (nil) leaves them alone.
	if got := bind(&teamRoomBind{Life: lifeBound}); len(got.Bindings) != 2 {
		t.Fatalf("bindings = %#v after a kind-only write, want them untouched", got.Bindings)
	}
	// A stated list replaces.
	if got := bind(&teamRoomBind{Bindings: []string{"repo:c"}}); len(got.Bindings) != 1 || got.Bindings[0] != "repo:c" {
		t.Fatalf("bindings = %#v, want the stated list to have replaced", got.Bindings)
	}
	// An explicit empty list unbinds — and the kind survives it.
	got := bind(&teamRoomBind{Bindings: []string{}})
	if len(got.Bindings) != 0 {
		t.Fatalf("bindings = %#v, want an explicit empty list to unbind", got.Bindings)
	}
	if got.Life != lifeBound {
		t.Errorf("kind = %q, want unbinding to leave the lifecycle intent alone", got.Life)
	}
}

// TestDirectMessagesAreRooms is the taxonomy, held as a test: a room between
// people is a channel with no name, not a different kind of thing, so one list
// carries both and one bind op works on either.
func TestDirectMessagesAreRooms(t *testing.T) {
	b, sess, ws := roomFixture(t, "acme", "general")
	ctx := orgCtx(t, "acme")

	raw, err := json.Marshal(map[string]any{
		"_class":      clTxCreate,
		"objectId":    "dm-1",
		"objectClass": clDirectMessage,
		"objectSpace": "core:space:Space",
		"attributes":  map[string]any{"members": []any{"u-1", "u-2"}, "private": true},
		"modifiedBy":  acctSystem,
		"modifiedOn":  time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("marshal dm: %v", err)
	}
	sess.applyTx(raw)

	got, err := b.listRooms(ctx, nil)
	if err != nil {
		t.Fatalf("listRooms: %v", err)
	}
	if len(got.Rooms) != 2 {
		t.Fatalf("channels = %d, want the named channel AND the dm", len(got.Rooms))
	}
	var dm *teamRoom
	for i := range got.Rooms {
		if got.Rooms[i].ID == "dm-1" {
			dm = &got.Rooms[i]
		}
	}
	if dm == nil {
		t.Fatal("the direct message is absent from the channel list")
	}
	if !dm.Direct || !dm.Private || len(dm.Members) != 2 {
		t.Errorf("dm = %+v, want direct+private with both members", *dm)
	}
	// And it binds like any other channel.
	if _, err := b.bindRoom(ctx, &teamRoomBind{ID: "dm-1", Workspace: ws, Life: lifeBound}); err != nil {
		t.Errorf("a direct message refused a bind: %v", err)
	}
}

// orgCtx builds the context a typed op receives for a validated caller of `org`
// — the same slot cloud.Bridge parks and principal.Acting reads, so these tests
// resolve tenancy exactly as the served route does.
func orgCtx(t *testing.T, org string) context.Context {
	t.Helper()
	return principal.WithActing(context.Background(), org)
}

// zipStatus reads the HTTP status off a refusal. A refusal's STATUS is part of
// its contract here — 404-not-403 on the tenancy gate is the whole point of that
// assertion — so a test that only checked for non-nil would miss the property.
func zipStatus(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}
