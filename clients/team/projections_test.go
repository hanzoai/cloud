package team

import (
	"encoding/json"
	"os"
	"testing"
)

// ingestServer builds a standalone transactor server + a query session sharing one
// store, and publishes it as the `live` singleton so Apply() targets it. Ported
// from team-go/pkg/transactor (server → transServer).
func ingestServer(t *testing.T, org, ws string) (*transServer, *session) {
	t.Helper()
	dir, err := os.MkdirTemp("", "team-ingest")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	srv := &transServer{hub: newHub(), store: newStore(dir), hier: buildHierarchy(modelJSON)}
	live = srv
	t.Cleanup(func() { live = nil })
	sess := &session{server: srv, store: srv.store, hier: srv.hier, org: org, workspace: ws, account: acctSystem}
	return srv, sess
}

// TestIngestMirrorsMemberToEmployee is the #19 core: a member projected via
// Apply(MemberTxes) shows up under BOTH contact:class:Person and the
// contact:mixin:Employee (Team) query, carries the display name, and links a hanzo
// social identity — exactly what the SPA Contacts/Team modules read.
func TestIngestMirrorsMemberToEmployee(t *testing.T) {
	const org, ws = "hanzo", "e48f81fd-12be-4bcd-aecb-3eaa9a9b5b18"
	const uid = "2d4d67ab-30f1-474e-b81f-f60461852259"
	_, sess := ingestServer(t, org, ws)

	Apply(org, ws, acctSystem, MemberTxes(Member{UserID: uid, Name: "Zeekay", Role: "owner", Active: true}, false)...)

	persons := sess.queryDocs(clPerson, nil)
	if len(persons) != 1 {
		t.Fatalf("contact:class:Person count = %d, want 1", len(persons))
	}
	if persons[0]["name"] != ",Zeekay" { // Huly "last,first"; single token → first-name only
		t.Fatalf("person name = %v, want ,Zeekay", persons[0]["name"])
	}
	if persons[0]["avatarType"] != "color" {
		t.Fatalf("person avatarType = %v, want color", persons[0]["avatarType"])
	}
	if persons[0]["personUuid"] != uid {
		t.Fatalf("personUuid = %v, want %s", persons[0]["personUuid"], uid)
	}
	if _, ok := persons[0][mixinEmployee].(map[string]any); !ok {
		t.Fatalf("person missing Employee mixin: %v", persons[0])
	}

	emps := sess.queryDocs(mixinEmployee, nil)
	if len(emps) != 1 {
		t.Fatalf("contact:mixin:Employee (Team) count = %d, want 1", len(emps))
	}
	if active, _ := emps[0][mixinEmployee].(map[string]any)["active"].(bool); !active {
		t.Fatalf("employee not active: %v", emps[0][mixinEmployee])
	}

	sids := sess.queryDocs(clSocialIdentity, map[string]any{"key": "hanzo:" + uid})
	if len(sids) != 1 {
		t.Fatalf("social identity count = %d, want 1", len(sids))
	}
}

// TestMemberUpdatePreservesProfile is the anti-clobber regression: a members-row
// update re-projects the member and MUST NOT clobber Person fields the SPA owns
// (avatar/city/…). It updates only name + the Employee mixin.
func TestMemberUpdatePreservesProfile(t *testing.T) {
	const org, ws = "hanzo", "e48f81fd-12be-4bcd-aecb-3eaa9a9b5b18"
	const uid = "2d4d67ab-30f1-474e-b81f-f60461852259"
	_, sess := ingestServer(t, org, ws)

	Apply(org, ws, acctSystem, MemberTxes(Member{UserID: uid, Name: "Zeekay", Role: "owner", Active: true}, false)...)

	// Simulate the SPA writing profile fields onto the Person.
	pid := PersonRef(uid)
	doc, _ := sess.store.get(org, ws, pid)
	doc["avatar"] = "blob:xyz"
	doc["city"] = "Tokyo"
	if err := sess.store.put(org, ws, doc); err != nil {
		t.Fatal(err)
	}

	Apply(org, ws, acctSystem, MemberTxes(Member{UserID: uid, Name: "Zeekay Kanjo", Role: "admin", Active: true}, true)...)

	after, _ := sess.store.get(org, ws, pid)
	if after["avatar"] != "blob:xyz" {
		t.Fatalf("avatar clobbered: %v (want blob:xyz)", after["avatar"])
	}
	if after["city"] != "Tokyo" {
		t.Fatalf("city clobbered: %v (want Tokyo)", after["city"])
	}
	if after["name"] != "Kanjo,Zeekay" { // "Zeekay Kanjo" → Huly "last,first"
		t.Fatalf("name not updated: %v (want Kanjo,Zeekay)", after["name"])
	}
	if role, _ := after[mixinEmployee].(map[string]any)["role"].(string); role != "ADMIN" {
		t.Fatalf("employee role not refreshed: %v", after[mixinEmployee])
	}
}

// TestIngestBotDeactivationDropsFromTeam proves a bot re-sync with active=false
// removes it from the Employee/Team list (Employee.active=false) while its Person
// survives (authorship history) — the bots-as-members lifecycle.
func TestIngestBotDeactivationDropsFromTeam(t *testing.T) {
	const org, ws = "hanzo", "e48f81fd-12be-4bcd-aecb-3eaa9a9b5b18"
	const bot = "11111111-1111-4111-8111-111111111111"
	_, sess := ingestServer(t, org, ws)

	Apply(org, ws, acctSystem, MemberTxes(Member{UserID: bot, Name: "Zen Agent", Role: "member", IsBot: true, Active: true}, false)...)
	if n := len(sess.queryDocs(mixinEmployee, map[string]any{"active": true})); n != 1 {
		t.Fatalf("active employees after add = %d, want 1", n)
	}

	Apply(org, ws, acctSystem, MemberTxes(Member{UserID: bot, Name: "Zen Agent", Role: "member", IsBot: true, Active: false}, true)...)
	if n := len(sess.queryDocs(mixinEmployee, map[string]any{"active": true})); n != 0 {
		t.Fatalf("active employees after deactivate = %d, want 0", n)
	}
	if n := len(sess.queryDocs(clPerson, nil)); n != 1 {
		t.Fatalf("persons after deactivate = %d, want 1 (history preserved)", n)
	}
}

// TestFindAllReverseLookupSocialIds locks the employees-query join: findAll with
// { lookup: { _id: { socialIds: SocialIdentity } } } must attach each Employee's
// SocialIdentity children (attachedTo == person._id) under $lookup.socialIds — the
// join the SPA needs to populate its socialId→employee maps (blank rows otherwise,
// so bots/members never paint).
func TestFindAllReverseLookupSocialIds(t *testing.T) {
	const org, ws = "hanzo", "e48f81fd-12be-4bcd-aecb-3eaa9a9b5b18"
	const uid = "2d4d67ab-30f1-474e-b81f-f60461852259"
	_, sess := ingestServer(t, org, ws)

	Apply(org, ws, acctSystem, MemberTxes(Member{UserID: uid, Name: "Zeekay", Role: "owner", Active: true}, false)...)

	req := []byte(`{"id":9,"method":"findAll","params":["contact:mixin:Employee",{},{"lookup":{"_id":{"socialIds":"contact:class:SocialIdentity"}}}]}`)
	out := sess.handle(req)

	var resp struct {
		Result struct {
			Value []map[string]any `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Result.Value) != 1 {
		t.Fatalf("employees = %d, want 1", len(resp.Result.Value))
	}
	emp := resp.Result.Value[0]
	if emp["name"] != ",Zeekay" {
		t.Fatalf("employee name lost in lookup path: %v", emp["name"])
	}
	lk, ok := emp["$lookup"].(map[string]any)
	if !ok {
		t.Fatalf("no $lookup on employee: %v", emp)
	}
	sids, ok := lk["socialIds"].([]any)
	if !ok || len(sids) != 1 {
		t.Fatalf("$lookup.socialIds = %v, want 1 social identity", lk["socialIds"])
	}
	sid := sids[0].(map[string]any)
	if sid["key"] != "hanzo:"+uid || sid["attachedTo"] != PersonRef(uid) {
		t.Fatalf("social identity join wrong: %v", sid)
	}
}

// TestFindAllNoLookupUnchanged proves a findAll WITHOUT lookup returns bare docs
// (no $lookup key) — the join is strictly opt-in.
func TestFindAllNoLookupUnchanged(t *testing.T) {
	const org, ws = "hanzo", "e48f81fd-12be-4bcd-aecb-3eaa9a9b5b18"
	const uid = "2d4d67ab-30f1-474e-b81f-f60461852259"
	_, sess := ingestServer(t, org, ws)
	Apply(org, ws, acctSystem, MemberTxes(Member{UserID: uid, Name: "Zeekay", Role: "owner", Active: true}, false)...)

	out := sess.handle([]byte(`{"id":1,"method":"findAll","params":["contact:mixin:Employee",{},{}]}`))
	var resp struct {
		Result struct {
			Value []map[string]any `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if _, has := resp.Result.Value[0]["$lookup"]; has {
		t.Fatal("$lookup must be absent when no lookup option is given")
	}
}
