package team

import (
	"testing"

	"github.com/google/uuid"
)

// TestBotUserIDDeterministic proves the SAME agent id always yields the SAME (and
// valid) member uuid, and distinct agents never collide — so re-syncs upsert in
// place (no duplicate bot members).
func TestBotUserIDDeterministic(t *testing.T) {
	a := botUserID("agent_xyz")
	if a != botUserID("agent_xyz") {
		t.Fatal("botUserID not deterministic")
	}
	if botUserID("agent_1") == botUserID("agent_2") {
		t.Fatal("distinct agents collided to same member uuid")
	}
	if uuid.Validate(a) != nil {
		t.Fatalf("botUserID(%q) = %q is not a valid uuid", "agent_xyz", a)
	}
	if botUserID("") != "" {
		t.Fatal("botUserID(empty) must be empty")
	}
	// Distinct namespace from the human derivation (accountID) — a bot and a human
	// with colliding raw ids must not map to the same member key.
	if botUserID("agent_x") == accountID("agent_x") {
		t.Fatal("bot and human derivations must not collide")
	}
}

// TestBotActive maps agent status → Employee.active: live states keep a bot in the
// Team list; any other (archived/retired) drops it while its Person survives.
func TestBotActive(t *testing.T) {
	for _, s := range []string{"", "active", "ready"} {
		if !botActive(s) {
			t.Fatalf("botActive(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"archived", "retired", "disabled", "error"} {
		if botActive(s) {
			t.Fatalf("botActive(%q) = true, want false", s)
		}
	}
}

// TestHulyName locks the Person.name convention "last,first": a two-token name
// splits, a single token (most bots) becomes ",token", and empty stays ",".
func TestHulyName(t *testing.T) {
	cases := map[string]string{
		"Zeekay Kanjo":       "Kanjo,Zeekay",
		"maxpower-assistant": ",maxpower-assistant",
		"":                   ",",
		"  Ada  Lovelace  ":  "Lovelace,Ada",
	}
	for in, want := range cases {
		if got := hulyName(in); got != want {
			t.Errorf("hulyName(%q) = %q, want %q", in, got, want)
		}
	}
}
