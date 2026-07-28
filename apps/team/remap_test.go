package team

import (
	"encoding/json"
	"testing"
)

// putMigratedDoc writes a doc straight into a workspace store — used to reconstruct
// the exact row shape a Jul-5 team-go→cloud migration left behind, BEFORE any
// reconcile runs.
func putMigratedDoc(t *testing.T, s *session, doc map[string]any) {
	t.Helper()
	if err := s.store.put(s.org, s.workspace, doc); err != nil {
		t.Fatalf("seed migrated doc %v: %v", doc["_id"], err)
	}
}

func socialIdsForKey(s *session, key string) []map[string]any {
	return s.queryDocs(clSocialIdentity, map[string]any{"key": key})
}

// TestRemapMigratedSocialIdExistingPerson is the primary reproduction: the migrated
// workspace ALREADY has the deterministic person-<account> (team-go projected it),
// AND a confirmed hanzo:<account> SocialIdentity still attached to a team-go-era
// Person id (a second Person row for the same account). Because person-<account>
// exists, MemberTxes never re-creates the social identity, so WITHOUT the remap the
// confirmed identity stays on the wrong person and the workbench throws "Confirmed
// social identity is attached to the wrong person" on transactor connect. The remap
// re-points it onto person-<account>, satisfying the exact invariant a fresh
// workspace has.
func TestRemapMigratedSocialIdExistingPerson(t *testing.T) {
	const org = "hanzo"
	const human = "2d4d67ab-30f1-474e-b81f-f60461852259"
	const oldPid = "person-teamgo-legacy-9f3a" // the team-go-era Person id (the wrong one)
	_, sess, _ := rosterServer(t, org, human, "Dave Lorenzini", nil)

	sess.seedWorkspace()

	pid := PersonRef(human)
	socialKey := "hanzo:" + human
	// Migrated rows: BOTH persons carry personUuid==human (same account, two Person
	// docs), and the confirmed social identity is attached to the WRONG one (oldPid).
	putMigratedDoc(t, sess, map[string]any{
		"_id": pid, "_class": clPerson, "space": spaceContacts,
		"name": "Lorenzini,Dave", "personUuid": human, "avatarType": "color",
		"modifiedBy": acctSystem, "modifiedOn": int64(1), "createdBy": acctSystem, "createdOn": int64(1),
	})
	putMigratedDoc(t, sess, map[string]any{
		"_id": oldPid, "_class": clPerson, "space": spaceContacts,
		"name": "Lorenzini,Dave", "personUuid": human, "avatarType": "color",
		"modifiedBy": acctSystem, "modifiedOn": int64(1), "createdBy": acctSystem, "createdOn": int64(1),
	})
	putMigratedDoc(t, sess, map[string]any{
		"_id": socialKey, "_class": clSocialIdentity, "space": spaceContacts,
		"attachedTo": oldPid, "attachedToClass": clPerson, "collection": "socialIds",
		"key": socialKey, "type": "hanzo", "value": human,
		"modifiedBy": acctSystem, "modifiedOn": int64(1), "createdBy": acctSystem, "createdOn": int64(1),
	})

	// Sanity: the migrated state is broken exactly as reported.
	sids := socialIdsForKey(sess, socialKey)
	if len(sids) != 1 || str(sids[0]["attachedTo"]) != oldPid {
		t.Fatalf("precondition: social identity should start attached to oldPid, got %v", sids)
	}

	// Connect → reconcile. This is the code path the transactor runs on every open.
	sess.reconcileRoster()

	// The confirmed identity now attaches to the deterministic person-<account>.
	sids = socialIdsForKey(sess, socialKey)
	if len(sids) != 1 {
		t.Fatalf("social identities keyed %s = %d, want 1", socialKey, len(sids))
	}
	if got := str(sids[0]["attachedTo"]); got != pid {
		t.Fatalf("attachedTo = %q, want %q (remap did not fix the wrong person)", got, pid)
	}
	if got := str(sids[0]["attachedToClass"]); got != clPerson {
		t.Fatalf("attachedToClass = %q, want %q", got, clPerson)
	}

	// Client-facing invariant: the Employee reverse-lookup the workbench issues joins
	// the social identity to person-<account>, never to the team-go-era person.
	req := []byte(`{"id":9,"method":"findAll","params":["contact:mixin:Employee",{"personUuid":"` + human + `"},{"lookup":{"_id":{"socialIds":"contact:class:SocialIdentity"}}}]}`)
	var resp struct {
		Result struct {
			Value []map[string]any `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(sess.handle(req), &resp); err != nil {
		t.Fatal(err)
	}
	// person-<account> is the Employee; oldPid has no Employee mixin so it is not in
	// this result — the workbench renders the account as person-<account>.
	if len(resp.Result.Value) != 1 || str(resp.Result.Value[0]["_id"]) != pid {
		t.Fatalf("employee for account = %v, want single person %s", resp.Result.Value, pid)
	}
	lk, _ := resp.Result.Value[0]["$lookup"].(map[string]any)
	joined, _ := lk["socialIds"].([]any)
	if len(joined) != 1 {
		t.Fatalf("$lookup.socialIds = %v, want the remapped identity", lk["socialIds"])
	}
	if sid := joined[0].(map[string]any); str(sid["attachedTo"]) != pid {
		t.Fatalf("joined social identity still attached to wrong person: %v", sid)
	}
}

// TestRemapMigratedSocialIdStrayDuplicate covers the other migrated shape: a stray
// SocialIdentity with a RANDOM _id (not the deterministic hanzo:<account>) but the
// same confirmed key, attached to a team-go-era person. Here MemberTxes DOES create
// the canonical hanzo:<account> row (person-<account> was absent), so afterwards two
// rows share the key — one canonical, one stray on the wrong person. The remap must
// re-point the stray too, so EVERY confirmed identity for the account resolves to
// person-<account> (no ambiguity the client could resolve to the wrong person).
func TestRemapMigratedSocialIdStrayDuplicate(t *testing.T) {
	const org = "hanzo"
	const human = "7c1e0000-1111-4222-8333-abcdef012345"
	const oldPid = "person-teamgo-legacy-aa11"
	const strayID = "sid-legacy-random-b2c3"
	_, sess, _ := rosterServer(t, org, human, "Ada Lovelace", nil)

	sess.seedWorkspace()
	socialKey := "hanzo:" + human
	putMigratedDoc(t, sess, map[string]any{
		"_id": oldPid, "_class": clPerson, "space": spaceContacts,
		"name": "Lovelace,Ada", "personUuid": human, "avatarType": "color",
		"modifiedBy": acctSystem, "modifiedOn": int64(1), "createdBy": acctSystem, "createdOn": int64(1),
	})
	putMigratedDoc(t, sess, map[string]any{
		"_id": strayID, "_class": clSocialIdentity, "space": spaceContacts,
		"attachedTo": oldPid, "attachedToClass": clPerson, "collection": "socialIds",
		"key": socialKey, "type": "hanzo", "value": human,
		"modifiedBy": acctSystem, "modifiedOn": int64(1), "createdBy": acctSystem, "createdOn": int64(1),
	})

	sess.reconcileRoster()

	pid := PersonRef(human)
	sids := socialIdsForKey(sess, socialKey)
	if len(sids) == 0 {
		t.Fatalf("no social identities keyed %s after reconcile", socialKey)
	}
	for _, sid := range sids {
		if got := str(sid["attachedTo"]); got != pid {
			t.Fatalf("social identity %v attachedTo = %q, want %q (stray not remapped)", sid["_id"], got, pid)
		}
	}
	// The stray row itself was re-pointed in place (not deleted — no data loss).
	stray, _ := sess.store.get(sess.org, sess.workspace, strayID)
	if stray == nil {
		t.Fatalf("stray social identity was deleted, want re-pointed in place")
	}
	if got := str(stray["attachedTo"]); got != pid {
		t.Fatalf("stray attachedTo = %q, want %q", got, pid)
	}
}

// TestRemapIdempotentAndFreshUnaffected proves the guard: on a FRESH workspace the
// remap is a no-op (the social identity is attached to person-<account> from the
// start), and re-running reconcile after a remap produces no further changes.
func TestRemapIdempotentAndFreshUnaffected(t *testing.T) {
	const org = "hanzo"
	const human = "9999aaaa-bbbb-4ccc-8ddd-eeeeffff0000"
	_, sess, _ := rosterServer(t, org, human, "Grace Hopper", nil)

	// Fresh workspace: reconcile builds the deterministic person + social identity.
	sess.seedWorkspace()
	sess.reconcileRoster()

	// A fresh workspace has NOTHING to remap — the invariant already holds.
	if txes := sess.remapMigratedSocialIds(human); txes != nil {
		t.Fatalf("fresh workspace produced remap txes = %v, want none", txes)
	}
	pid := PersonRef(human)
	sids := socialIdsForKey(sess, "hanzo:"+human)
	if len(sids) != 1 || str(sids[0]["attachedTo"]) != pid {
		t.Fatalf("fresh social identity = %v, want single attached to %s", sids, pid)
	}

	// Idempotent: a second reconcile changes nothing and adds no duplicate persons.
	before := len(sess.queryDocs(clPerson, nil))
	sess.reconcileRoster()
	if after := len(sess.queryDocs(clPerson, nil)); after != before {
		t.Fatalf("persons after reconnect = %d, want %d (idempotent)", after, before)
	}
	if txes := sess.remapMigratedSocialIds(human); txes != nil {
		t.Fatalf("second pass produced remap txes = %v, want none (idempotent)", txes)
	}
}
