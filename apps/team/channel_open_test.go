package team

import (
	"github.com/hanzoai/cloud/internal/sqltest"
	"testing"
)

// TestOpenChannelIsReadBackByListChannels is the property the create op has to hold:
// it is the same endpoint, not a second store. A channel opened over REST is a document
// the transactor owns, so the listing that reads the transactor's own documents
// finds it with no sync step.
func TestOpenChannelIsReadBackByListChannels(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, _ := channelFixture(t, "acme")
		ctx := orgCtx(t, "acme")

		opened, err := b.openChannel(ctx, &teamChannelNew{Name: "bugfix-1010", Topic: "the one that bit us", Life: lifeBound, Bindings: []string{"issue:1010"}})
		if err != nil {
			t.Fatalf("openChannel: %v", err)
		}
		made := opened.Channel
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
			t.Fatalf("a named public channel read as direct/private/archived: %+v", made)
		}

		got, err := b.listChannels(ctx, nil)
		if err != nil {
			t.Fatalf("listChannels: %v", err)
		}
		if len(got.Channels) != 1 || got.Channels[0].ID != made.ID {
			t.Fatalf("listing = %+v, want the channel openChannel answered (%s)", got.Channels, made.ID)
		}
		if got.Channels[0].Life != lifeBound {
			t.Fatalf("facet did not persist: %+v", got.Channels[0])
		}
	})
}

// TestOpenChannelRefusesASecondChannelOfTheSameName pins the conflict. Two channels that
// read identically in a sidebar give a reader no way to tell which one a message
// landed in, so the second is refused rather than stored.
func TestOpenChannelRefusesASecondChannelOfTheSameName(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, _ := channelFixture(t, "acme", "general")
		ctx := orgCtx(t, "acme")

		if _, err := b.openChannel(ctx, &teamChannelNew{Name: "General"}); err == nil {
			t.Fatal("a second channel named General was accepted; the name check does nothing")
		}
		got, err := b.listChannels(ctx, nil)
		if err != nil {
			t.Fatalf("listChannels: %v", err)
		}
		if len(got.Channels) != 1 {
			t.Fatalf("channels = %d, want 1 — the refused create still wrote", len(got.Channels))
		}
	})
}

// TestOpenChannelRefusesAPrivateChannelWithNoMembers pins the one create that would
// produce a document nobody can reach.
func TestOpenChannelRefusesAPrivateChannelWithNoMembers(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, _ := channelFixture(t, "acme")
		ctx := orgCtx(t, "acme")

		if _, err := b.openChannel(ctx, &teamChannelNew{Name: "hush", Private: true}); err == nil {
			t.Fatal("a private channel with no members was created; nobody could enter it")
		}
	})
}

// TestOpenChannelNeedsAName pins that the one required field is required.
func TestOpenChannelNeedsAName(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, _ := channelFixture(t, "acme")
		ctx := orgCtx(t, "acme")

		if _, err := b.openChannel(ctx, &teamChannelNew{Topic: "nameless"}); err == nil {
			t.Fatal("a channel with no name was created")
		}
	})
}

// TestOpenChannelRefusesAnotherTenantsSpace is the isolation property. The space
// comes from the request and the org from the validated principal, so a named
// space that the org does not own must not resolve.
func TestOpenChannelRefusesAnotherTenantsSpace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		b, _, _ := channelFixture(t, "acme")
		ctx := orgCtx(t, "acme")

		if _, err := b.openChannel(ctx, &teamChannelNew{Name: "trespass", Space: "00000000-0000-0000-0000-000000000000"}); err == nil {
			t.Fatal("a channel was opened in a space the org does not own")
		}
	})
}
