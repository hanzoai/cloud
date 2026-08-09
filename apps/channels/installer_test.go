package channels

// The first approved pairing becomes the org's channel owner — but nobody could
// be approved until an admin approved, and approving needs an admin surface. For
// an org that installed the bot itself that is a circle, and it is the reason a
// real workspace sat at "Pairing code: …" with nobody able to clear it.
//
// The person who completed the OAuth was ALREADY an admin of that org. These pin
// that they get through, and — the part that matters — that nobody else does.

import (
	"context"
	"testing"
)

func TestInstallerGetsThroughBeforeAnyoneIsOwner(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	v, err := dmGate(ctx, st, "acme", "slack", "Uinstaller", "Uinstaller", true)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Allow || v.Reason != dmInstaller {
		t.Fatalf("installer verdict = %+v, want allow/dmInstaller — the person who "+
			"installed the bot should not be asked to approve themselves", v)
	}
}

// THE BOUND THAT MATTERS. Anyone else still pairs, including someone claiming a
// different installer id: the value comes from the connection the adapter
// resolved, never from the message.
func TestOnlyTheInstaller(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	v, err := dmGate(ctx, st, "acme", "slack", "Ustranger", "Uinstaller", true)
	if err != nil {
		t.Fatal(err)
	}
	if v.Allow {
		t.Fatalf("a stranger was admitted alongside the installer: %+v", v)
	}
	if !v.Pair {
		t.Fatalf("a stranger should still be asked to pair, got %+v", v)
	}
}

// An install that predates the field carries no installer, and the gate then
// behaves exactly as it did before — nobody is admitted by an empty string.
func TestNoInstallerAdmitsNobody(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	v, err := dmGate(ctx, st, "acme", "slack", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if v.Allow {
		t.Fatalf("an empty sender matched an empty installer: %+v", v)
	}
}

// ONCE AN OWNER EXISTS, the bootstrap stops. The org has decided who may speak,
// and this must not reopen that.
func TestInstallerStopsOnceOwnerExists(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO channel_owner (org, entry, created_at) VALUES (?,?,?)`,
		"acme", "slack:Usomeone", 1); err != nil {
		t.Fatal(err)
	}

	v, err := dmGate(ctx, st, "acme", "slack", "Uinstaller", "Uinstaller", true)
	if err != nil {
		t.Fatal(err)
	}
	if v.Allow {
		t.Fatalf("the installer bypassed an org that already has an owner: %+v", v)
	}
}
