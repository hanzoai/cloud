package agents

// owner_test.go — whose session it is, named so no credential can be somebody else.

import "testing"

// A session's owner is <org>/<subject>. An API key's subject is its holder's row,
// <owner>/<name> (cloud auth_apikey.go), so a key held in its home org owns
// sessions as hanzo/hanzo/z: the org the session lives in, then the holder. It
// reads doubled and is not. A person's token owns them as hanzo/<their IAM sub>,
// and a username may be any 1-63 of [a-z0-9._-] (iam schema.Username), a UUID
// included. Collapsed to <subject> when the subject starts with the org, a key
// held by a user named after a colleague's sub would own that colleague's
// sessions: read them, steer them, stop them.
func TestAKeysSessionsAreNeverAPersons(t *testing.T) {
	const sub = "2d4d67ab-30f1-474e-b81f-f60461852259"
	person := billingActor("hanzo", sub)
	key := billingActor("hanzo", "hanzo/"+sub)
	if person == key {
		t.Fatalf("a key held by a user named %s owns that person's sessions as %q", sub, key)
	}
	if got := billingActor("hanzo", "hanzo/z"); got != "hanzo/hanzo/z" {
		t.Fatalf("a key held at home owns its sessions as %q, want hanzo/hanzo/z", got)
	}
	if got := billingActor("hanzo", "acme/z"); got != "hanzo/acme/z" {
		t.Fatalf("a member's key from acme owns its sessions in hanzo as %q, want hanzo/acme/z", got)
	}
}
