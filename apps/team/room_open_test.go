package team

import (
	"testing"
)

// TestOpenRoomIsReadBackByListRooms is the property the create op has to hold:
// it is the same endpoint, not a second store. A room opened over REST is a document
// the transactor owns, so the listing that reads the transactor's own documents
// finds it with no sync step.
func TestOpenRoomIsReadBackByListRooms(t *testing.T) {
	b, _, _ := roomFixture(t, "acme")
	ctx := orgCtx(t, "acme")

	made, err := b.openRoom(ctx, &teamRoomNew{Name: "bugfix-1010", Topic: "the one that bit us", Life: lifeBound, Bindings: []string{"issue:1010"}})
	if err != nil {
		t.Fatalf("openRoom: %v", err)
	}
	if made.Name != "bugfix-1010" || made.Topic != "the one that bit us" {
		t.Fatalf("made = %+v", made)
	}
	if made.Life != lifeBound {
		t.Fatalf("life = %q, want %q", made.Life, lifeBound)
	}
	if len(made.Bindings) != 1 || made.Bindings[0] != "issue:1010" {
		t.Fatalf("bindings = %v, want [issue:1010]", made.Bindings)
	}
	if made.Direct || made.Private || made.Archived {
		t.Fatalf("a named public room read as direct/private/archived: %+v", made)
	}

	got, err := b.listRooms(ctx, nil)
	if err != nil {
		t.Fatalf("listRooms: %v", err)
	}
	if len(got.Rooms) != 1 || got.Rooms[0].ID != made.ID {
		t.Fatalf("listing = %+v, want the room openRoom answered (%s)", got.Rooms, made.ID)
	}
	if got.Rooms[0].Life != lifeBound {
		t.Fatalf("facet did not persist: %+v", got.Rooms[0])
	}
}

// TestOpenRoomRefusesASecondRoomOfTheSameName pins the conflict. Two rooms that
// read identically in a sidebar give a reader no way to tell which one a message
// landed in, so the second is refused rather than stored.
func TestOpenRoomRefusesASecondRoomOfTheSameName(t *testing.T) {
	b, _, _ := roomFixture(t, "acme", "general")
	ctx := orgCtx(t, "acme")

	if _, err := b.openRoom(ctx, &teamRoomNew{Name: "General"}); err == nil {
		t.Fatal("a second room named General was accepted; the name check does nothing")
	}
	got, err := b.listRooms(ctx, nil)
	if err != nil {
		t.Fatalf("listRooms: %v", err)
	}
	if len(got.Rooms) != 1 {
		t.Fatalf("rooms = %d, want 1 — the refused create still wrote", len(got.Rooms))
	}
}

// TestOpenRoomRefusesAPrivateRoomWithNoMembers pins the one create that would
// produce a document nobody can reach.
func TestOpenRoomRefusesAPrivateRoomWithNoMembers(t *testing.T) {
	b, _, _ := roomFixture(t, "acme")
	ctx := orgCtx(t, "acme")

	if _, err := b.openRoom(ctx, &teamRoomNew{Name: "hush", Private: true}); err == nil {
		t.Fatal("a private room with no members was created; nobody could enter it")
	}
}

// TestOpenRoomNeedsAName pins that the one required field is required.
func TestOpenRoomNeedsAName(t *testing.T) {
	b, _, _ := roomFixture(t, "acme")
	ctx := orgCtx(t, "acme")

	if _, err := b.openRoom(ctx, &teamRoomNew{Topic: "nameless"}); err == nil {
		t.Fatal("a room with no name was created")
	}
}

// TestOpenRoomRefusesAnotherTenantsSpace is the isolation property. The space
// comes from the request and the org from the validated principal, so a named
// space that the org does not own must not resolve.
func TestOpenRoomRefusesAnotherTenantsSpace(t *testing.T) {
	b, _, _ := roomFixture(t, "acme")
	ctx := orgCtx(t, "acme")

	if _, err := b.openRoom(ctx, &teamRoomNew{Name: "trespass", Space: "00000000-0000-0000-0000-000000000000"}); err == nil {
		t.Fatal("a room was opened in a space the org does not own")
	}
}
