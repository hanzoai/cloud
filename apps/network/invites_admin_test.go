package network

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/zap-proto/zip"
)

const invitesPath = "/v1/admin/network/invites"

// issued is the answer to issuing: what the operator is shown once.
type issued struct {
	Invites []struct {
		ID        string    `json:"id"`
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"invites"`
}

// issue issues count codes as the operator and answers them, or fails the test.
func (in *intake) issue(t *testing.T, body string) issued {
	t.Helper()
	a := in.send(t, http.MethodPost, invitesPath, body, operator, "")
	if a.code != http.StatusCreated {
		t.Fatalf("issue %s: %d %s", body, a.code, a.body)
	}
	var out issued
	if err := json.Unmarshal([]byte(a.body), &out); err != nil {
		t.Fatalf("issue answered %s: %v", a.body, err)
	}
	return out
}

// listed is one invite as the operator reads it.
type listed struct {
	Data []map[string]any `json:"data"`
}

func (in *intake) list(t *testing.T) listed {
	t.Helper()
	a := in.send(t, http.MethodGet, invitesPath, "", operator, "")
	if a.code != http.StatusOK {
		t.Fatalf("list: %d %s", a.code, a.body)
	}
	var out listed
	if err := json.Unmarshal([]byte(a.body), &out); err != nil {
		t.Fatalf("list answered %s: %v", a.body, err)
	}
	return out
}

// macOf is the HMAC-SHA-256 of code under key, in hex: what the store holds in
// place of the code.
func macOf(key []byte, code string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(code))
	return hex.EncodeToString(m.Sum(nil))
}

// TestTheOperatorIssuesCodesShownOnceAndStoredAsAnHMAC: issuing answers 201 with N
// codes in the one shape, each valid at once; the store, the audit trail and the
// log hold the HMAC and never the code.
func TestTheOperatorIssuesCodesShownOnceAndStoredAsAnHMAC(t *testing.T) {
	in := mountIntake(t)
	before := time.Now().UTC()
	out := in.issue(t, `{"count":3,"note":"launch week","expires_days":7}`)
	if len(out.Invites) != 3 {
		t.Fatalf("issued %d codes, want 3", len(out.Invites))
	}
	key := in.key(t)
	if len(key) < 32 {
		t.Fatalf("the invite key is %d bytes, want at least 32", len(key))
	}
	seen := map[string]bool{}
	for i, inv := range out.Invites {
		if !codeShape.MatchString(inv.Code) || seen[inv.Code] {
			t.Fatalf("code %q is not a new code of the shape HNZ-XXXXX-XXXXX of Crockford characters", inv.Code)
		}
		seen[inv.Code] = true
		want := macOf(key, inv.Code)
		if inv.ID != want[:15] {
			t.Errorf("id %q is not the first 15 characters of the code's HMAC %s", inv.ID, want[:15])
		}
		if d := inv.ExpiresAt.Sub(before); d < 7*24*time.Hour-time.Minute || d > 7*24*time.Hour+time.Minute {
			t.Errorf("expires_at is %v from now, want seven days", d)
		}
		if a := in.verify(t, inv.Code, fmt.Sprintf("203.0.113.%d", 10+i)); a.body != yes {
			t.Errorf("an issued code does not verify: %d %s", a.code, a.body)
		}
	}

	allowed := map[string]bool{"id": true, "code_hmac": true, "status": true, "note": true, "created_by": true,
		"issued_at": true, "expires_at": true, "revoked_at": true, "revoked_by": true,
		"collectionId": true, "collectionName": true}
	rows := records(t, "hanzo", invitesName)
	if len(rows) != 3 {
		t.Fatalf("the store holds %d invites, want 3", len(rows))
	}
	for _, r := range rows {
		for k := range r {
			if !allowed[k] {
				t.Errorf("an invite carries a field %q: %v", k, r)
			}
		}
		if r["status"] != "active" || r["note"] != "launch week" || r["created_by"] != "admin/z" {
			t.Errorf("invite = %v, want active, noted, by admin/z", r)
		}
		h, _ := r["code_hmac"].(string)
		if len(h) != 64 || !strings.HasPrefix(h, fmt.Sprint(r["id"])) {
			t.Errorf("code_hmac %q is not the HMAC the id was cut from", h)
		}
	}
	// Nowhere: not the store, not the trail, not the log.
	trail, _, _ := in.trail.Query(context.Background(), audit.Filter{Limit: 1000})
	tr, _ := json.Marshal(trail)
	st, _ := json.Marshal(append(rows, records(t, "hanzo", waitlistName)...))
	for code := range seen {
		for where, dump := range map[string]string{"the store": string(st), "the audit trail": string(tr), "the log": in.logs.String()} {
			for _, spelling := range []string{code, strings.ReplaceAll(code, "-", ""), strings.ToLower(code)} {
				if strings.Contains(dump, spelling) {
					t.Errorf("%s holds the code %s", where, spelling)
				}
			}
		}
	}
}

// TestTheKeyIsSealedOnFirstIssueAndKeptAfter: issuing seals the key in KMS under
// network/invite-hmac when there is none, and every later issue uses that one.
func TestTheKeyIsSealedOnFirstIssueAndKeptAfter(t *testing.T) {
	in := mountUnsealed(t)
	if _, err := in.kms.GetSecret(context.Background(), "network/invite-hmac"); err == nil {
		t.Fatal("the key existed before any issue")
	}
	a := in.issue(t, `{"count":1}`)
	k1, err := in.kms.GetSecret(context.Background(), "network/invite-hmac")
	if err != nil || len(k1) < 32 {
		t.Fatalf("the sealed key: %d bytes, %v", len(k1), err)
	}
	in.svc.State.seal = sealOf(in.svc.Coord) // a restart: the next read comes from KMS
	b := in.issue(t, `{"count":1}`)
	k2, _ := in.kms.GetSecret(context.Background(), "network/invite-hmac")
	if string(k1) != string(k2) {
		t.Fatal("a second issue replaced the key every code already issued is sealed under")
	}
	if in.verify(t, a.Invites[0].Code, "203.0.113.1").body != yes || in.verify(t, b.Invites[0].Code, "203.0.113.2").body != yes {
		t.Fatal("a code issued before the restart no longer verifies")
	}
}

// TestIssuedCodesAreUnique: a thousand issued codes, in ten issues, are all different
// and all the one shape, and each is found by verify.
func TestIssuedCodesAreUnique(t *testing.T) {
	in := mountIntake(t)
	seen := map[string]bool{}
	var last string
	for range 10 {
		out := in.issue(t, `{"count":100}`)
		if len(out.Invites) != 100 {
			t.Fatalf("issued %d, want 100", len(out.Invites))
		}
		for _, inv := range out.Invites {
			if !codeShape.MatchString(inv.Code) {
				t.Fatalf("code %q is not the shape HNZ-XXXXX-XXXXX", inv.Code)
			}
			if seen[inv.Code] || seen[inv.ID] {
				t.Fatalf("%s %s issued twice", inv.ID, inv.Code)
			}
			seen[inv.Code], seen[inv.ID] = true, true
			last = inv.Code
		}
	}
	if n := len(records(t, "hanzo", invitesName)); n != 1000 {
		t.Fatalf("the store holds %d invites, want 1000", n)
	}
	if a := in.verify(t, last, from(1)); a.body != yes {
		t.Fatalf("the last code issued does not verify: %d %s", a.code, a.body)
	}
}

// TestACollisionIsDrawnAgain: a code whose id is already an invite's is never written
// over; a fresh one is drawn in its place, however many times the draw repeats.
func TestACollisionIsDrawnAgain(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, live, func(r *map[string]any) { (*r)["note"] = "the original" })
	o := ops{s: in.svc}
	key := in.key(t)
	fresh := []string{"HNZ-FRSH1-AAAAA", "HNZ-FRSH2-AAAAA"}
	drawn := []string{live, live, fresh[0], live, fresh[0], fresh[1]} // two collisions with the store, one with itself
	n := 0
	draw := func() (string, error) { c := drawn[n]; n++; return c, nil }
	rows, out, err := o.mint(context.Background(), "hanzo", key, 2, draw, issuance{by: "admin/z", note: "new", issued: time.Now(), expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || len(out) != 2 || out[0].Code != fresh[0] || out[1].Code != fresh[1] {
		t.Fatalf("minted %v, want the two fresh codes", out)
	}
	if err := writeInvites(context.Background(), "hanzo", rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range records(t, "hanzo", invitesName) {
		if r["code_hmac"] == macOf(key, live) && r["note"] != "the original" {
			t.Fatalf("the original invite was written over: %v", r)
		}
	}
	if len(records(t, "hanzo", invitesName)) != 3 {
		t.Fatalf("the store holds %d invites, want the original and two", len(records(t, "hanzo", invitesName)))
	}
}

// TestIssuingIsBounded: a count of 1 to 100 and a life of 1 to 365 days; none is
// one code for thirty days; anything outside is a 400 and issues nothing; a note
// is one line of at most 200 characters.
func TestIssuingIsBounded(t *testing.T) {
	in := mountIntake(t)
	out := in.issue(t, `{}`)
	if len(out.Invites) != 1 {
		t.Fatalf("an empty request issued %d codes, want 1", len(out.Invites))
	}
	if d := time.Until(out.Invites[0].ExpiresAt); d < 29*24*time.Hour || d > 30*24*time.Hour+time.Minute {
		t.Fatalf("a code with no life lasts %v, want thirty days", d)
	}
	n := len(records(t, "hanzo", invitesName))
	for _, body := range []string{`{"count":101}`, `{"count":-1}`, `{"expires_days":-1}`, `{"expires_days":366}`, `{"count":"many"}`, `nope`} {
		if a := in.send(t, http.MethodPost, invitesPath, body, operator, ""); !problem(a, http.StatusBadRequest) {
			t.Errorf("%s: want a 400 problem document, got %d %q %s", body, a.code, a.kind, a.body)
		}
	}
	if got := len(records(t, "hanzo", invitesName)); got != n {
		t.Fatalf("refused requests issued %d codes", got-n)
	}
	in.issue(t, `{"count":100,"expires_days":365}`)
	in.issue(t, `{"count":1,"expires_days":1}`)
	in.issue(t, `{"count":1,"note":`+mustJSON(strings.Repeat("n", 500)+"\nsecond line")+`}`)
	for _, r := range records(t, "hanzo", invitesName) {
		if note, _ := r["note"].(string); len(note) > 200 || strings.ContainsAny(note, "\n\r") {
			t.Fatalf("a note of %d characters %q", len(note), note)
		}
	}
}

// TestTheListNeverShowsACode: the operator reads each invite's id, status, note,
// issue and expiry and who issued it, newest first; never the code, and never its
// HMAC. A revoked invite reads revoked and an expired one expired.
func TestTheListNeverShowsACode(t *testing.T) {
	in := mountIntake(t)
	live := in.issue(t, `{"count":2,"note":"live"}`)
	in.seed(t, lapsed, func(r *map[string]any) {
		(*r)["note"] = "lapsed"
		(*r)["issued_at"] = time.Now().UTC().Add(-48 * time.Hour).Format(baseDate)
		(*r)["expires_at"] = time.Now().UTC().Add(-time.Hour).Format(baseDate)
	})
	if a := in.send(t, http.MethodDelete, invitesPath+"/"+live.Invites[1].ID, "", operator, ""); a.code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", a.code, a.body)
	}

	a := in.send(t, http.MethodGet, invitesPath, "", operator, "")
	if a.code != http.StatusOK {
		t.Fatalf("list: %d %s", a.code, a.body)
	}
	for _, inv := range live.Invites {
		if strings.Contains(a.body, inv.Code) || strings.Contains(a.body, macOf(in.key(t), inv.Code)) {
			t.Fatalf("the list carries a code or its HMAC: %s", a.body)
		}
	}
	for _, word := range []string{"code", "hmac"} {
		if strings.Contains(strings.ToLower(a.body), word) {
			t.Fatalf("the list mentions %q: %s", word, a.body)
		}
	}
	var out listed
	_ = json.Unmarshal([]byte(a.body), &out)
	if len(out.Data) != 3 {
		t.Fatalf("the list holds %d invites, want 3: %s", len(out.Data), a.body)
	}
	status := map[string]string{}
	for _, row := range out.Data {
		for _, field := range []string{"id", "status", "note", "issued_at", "expires_at", "created_by"} {
			if _, ok := row[field]; !ok {
				t.Errorf("a row lacks %s: %v", field, row)
			}
		}
		if len(row) != 6 {
			t.Errorf("a row carries %d fields, want the six: %v", len(row), row)
		}
		status[fmt.Sprint(row["note"])+"/"+fmt.Sprint(row["id"])] = fmt.Sprint(row["status"])
		if row["created_by"] != "admin/z" {
			t.Errorf("created_by = %v", row["created_by"])
		}
	}
	got := map[string]int{}
	for k, v := range status {
		got[strings.SplitN(k, "/", 2)[0]+" "+v]++
	}
	if got["live active"] != 1 || got["live revoked"] != 1 || got["lapsed expired"] != 1 {
		t.Fatalf("statuses = %v, want one live active, one live revoked and one lapsed expired", got)
	}
	if out.Data[len(out.Data)-1]["note"] != "lapsed" {
		t.Errorf("the oldest invite is not last: %v", out.Data)
	}
}

// TestRevokingKillsTheCode: DELETE answers 204 and the code no longer verifies;
// asking again is the same; an id that names nothing is a 404.
func TestRevokingKillsTheCode(t *testing.T) {
	in := mountIntake(t)
	out := in.issue(t, `{"count":2}`)
	keep, kill := out.Invites[0], out.Invites[1]
	for i := range 2 {
		if a := in.send(t, http.MethodDelete, invitesPath+"/"+kill.ID, "", operator, ""); a.code != http.StatusNoContent || a.body != "" {
			t.Fatalf("revoke %d: %d %s", i+1, a.code, a.body)
		}
	}
	if a := in.verify(t, kill.Code, "203.0.113.1"); a.body != no {
		t.Fatalf("a revoked code verifies: %d %s", a.code, a.body)
	}
	if a := in.verify(t, keep.Code, "203.0.113.2"); a.body != yes {
		t.Fatalf("revoking one revoked the other: %d %s", a.code, a.body)
	}
	for _, id := range []string{"0123456789abcde", "zz", strings.Repeat("a", 64), "..%2F"} {
		if a := in.send(t, http.MethodDelete, invitesPath+"/"+id, "", operator, ""); !problem(a, http.StatusNotFound) {
			t.Errorf("revoke %q: want a 404 problem document, got %d %q %s", id, a.code, a.kind, a.body)
		}
	}
	for _, r := range records(t, "hanzo", invitesName) {
		if r["id"] == kill.ID && (r["status"] != "revoked" || r["revoked_by"] != "admin/z" || r["revoked_at"] == "") {
			t.Errorf("revoked invite = %v", r)
		}
	}
}

// TestOnlyASuperAdminAdministersInvites: over the routes, the gate refuses a
// stranger 401 and an ordinary member and an org admin 403, on every op; a
// SuperAdmin is let in. Nothing is issued, listed or revoked by a refused caller.
func TestOnlyASuperAdminAdministersInvites(t *testing.T) {
	in := mountIntake(t)
	out := in.issue(t, `{"count":1}`)
	id := out.Invites[0].ID
	n := len(records(t, "hanzo", invitesName))
	for _, c := range []struct {
		who  caller
		name string
		code int
	}{{visitor, "a stranger", http.StatusUnauthorized}, {ordinary, "a member", http.StatusForbidden}, {chief, "an org admin", http.StatusForbidden}} {
		for _, op := range []struct{ method, path, body string }{
			{http.MethodPost, invitesPath, `{"count":1}`},
			{http.MethodGet, invitesPath, ""},
			{http.MethodDelete, invitesPath + "/" + id, ""},
		} {
			if a := in.send(t, op.method, op.path, op.body, c.who, ""); !problem(a, c.code) {
				t.Errorf("%s %s %s: want a %d problem document, got %d %q %s", c.name, op.method, op.path, c.code, a.code, a.kind, a.body)
			}
		}
	}
	rows := records(t, "hanzo", invitesName)
	if len(rows) != n || rows[0]["status"] != "active" {
		t.Fatalf("a refused caller changed the store: %v", rows)
	}
	if a := in.verify(t, out.Invites[0].Code, "203.0.113.1"); a.body != yes {
		t.Fatalf("a refused revoke killed the code: %d %s", a.code, a.body)
	}
}

// TestEachOpRefusesAloneAndTheRefusalIsAudited: the call plane and MCP invoke an
// op with no route in front of it, so each asks for SuperAdmin itself. With the
// gate removed, a stranger, a member and an org admin are refused 403 by the op,
// the refusal is on the audit trail naming who asked, and nothing is written.
func TestEachOpRefusesAloneAndTheRefusalIsAudited(t *testing.T) {
	in := mountIntakeWith(t, func(app *zip.App, s *cloud.Service[state]) {
		o := ops{s: s}
		a := app.Group("/v1/admin/network") // the routes() group, without its cloud.Gate
		a.Post("/invites", o.issueInvites, zip.WithStatus(http.StatusCreated))
		a.Get("/invites", o.listInvites)
		a.Delete("/invites/:id", o.revokeInvite)
	})
	for _, c := range []struct {
		who  caller
		name string
	}{{visitor, "a stranger"}, {ordinary, "a member"}, {chief, "an org admin"}} {
		for _, op := range []struct{ method, path, body, action string }{
			{http.MethodPost, invitesPath, `{"count":1,"note":"sneaky"}`, "network.invites.issue"},
			{http.MethodGet, invitesPath, "", "network.invites.list"},
			{http.MethodDelete, invitesPath + "/0123456789abcde", "", "network.invites.revoke"},
		} {
			if a := in.send(t, op.method, op.path, op.body, c.who, ""); !problem(a, http.StatusForbidden) {
				t.Fatalf("%s %s %s: want a 403 problem document, got %d %q %s", c.name, op.method, op.path, a.code, a.kind, a.body)
			}
			deny := in.trailOf(t, op.action, "deny")
			if len(deny) == 0 {
				t.Fatalf("%s %s: the refusal is not on the trail", c.name, op.action)
			}
			last := deny[len(deny)-1]
			if last.Outcome.Status != http.StatusForbidden || !strings.Contains(last.Outcome.Reason, "SuperAdmin") || last.Auth.IsAdmin {
				t.Errorf("%s: refusal recorded as %+v", op.action, last)
			}
			if c.who.user != "" && last.Actor.Sub != c.who.user {
				t.Errorf("%s: the refusal names %q, want %q", op.action, last.Actor.Sub, c.who.user)
			}
		}
	}
	if n := len(records(t, "hanzo", invitesName)); n != 0 {
		t.Fatalf("refused calls wrote %d invites", n)
	}
	// The same ops, asked by a SuperAdmin, are served.
	if a := in.send(t, http.MethodPost, invitesPath, `{"count":1}`, operator, ""); a.code != http.StatusCreated {
		t.Fatalf("a SuperAdmin through the ungated route: %d %s", a.code, a.body)
	}
}

// TestIssuingAndRevokingAreAuditedBeforeTheyAreDone: the intent is on the trail
// before the write and the outcome after it; a trail that cannot take the intent
// answers 503 and nothing is written.
func TestIssuingAndRevokingAreAuditedBeforeTheyAreDone(t *testing.T) {
	in := mountIntake(t)
	out := in.issue(t, `{"count":2,"note":"launch","expires_days":9}`)
	intent, ok := in.trailOf(t, "network.invites.issue", "intent"), in.trailOf(t, "network.invites.issue", "success")
	if len(intent) != 1 || len(ok) != 1 || intent[0].Seq >= ok[0].Seq {
		t.Fatalf("issue: intent %v then success %v, want one of each in that order", intent, ok)
	}
	for _, want := range []string{`"count":2`, `"note":"launch"`, `"expires_days":9`} {
		if !strings.Contains(string(intent[0].After), want) {
			t.Errorf("the intent %s lacks %s", intent[0].After, want)
		}
	}
	if !intent[0].Auth.IsAdmin || intent[0].Actor.Sub != "admin/z" {
		t.Errorf("the intent names %+v", intent[0].Actor)
	}
	if !strings.Contains(string(ok[0].After), out.Invites[0].ID) {
		t.Errorf("the outcome %s does not name the invites it made", ok[0].After)
	}

	in.send(t, http.MethodDelete, invitesPath+"/"+out.Invites[0].ID, "", operator, "")
	ri, ro := in.trailOf(t, "network.invites.revoke", "intent"), in.trailOf(t, "network.invites.revoke", "success")
	if len(ri) != 1 || len(ro) != 1 || ri[0].Seq >= ro[0].Seq || ri[0].Resource.ID != out.Invites[0].ID {
		t.Fatalf("revoke: intent %v then success %v", ri, ro)
	}

	in.svc.State.trail = nil
	n := len(records(t, "hanzo", invitesName))
	if a := in.send(t, http.MethodPost, invitesPath, `{"count":1}`, operator, ""); !problem(a, http.StatusServiceUnavailable) {
		t.Fatalf("issuing with no audit trail: want 503, got %d %s", a.code, a.body)
	}
	if a := in.send(t, http.MethodDelete, invitesPath+"/"+out.Invites[1].ID, "", operator, ""); !problem(a, http.StatusServiceUnavailable) {
		t.Fatalf("revoking with no audit trail: want 503, got %d %s", a.code, a.body)
	}
	if got := len(records(t, "hanzo", invitesName)); got != n {
		t.Fatalf("an unaudited issue wrote %d invites", got-n)
	}
	if a := in.verify(t, out.Invites[1].Code, "203.0.113.1"); a.body != yes {
		t.Fatalf("an unaudited revoke took effect: %d %s", a.code, a.body)
	}
}

// TestTheListIsPagedByACursor: a page of limit invites carries an opaque next, which
// handed back gives the following page, newest first, until none is left; no invite
// is missed or shown twice, and a cursor that is not one is a 400.
func TestTheListIsPagedByACursor(t *testing.T) {
	in := mountIntake(t)
	for i := range 7 {
		in.seed(t, fmt.Sprintf("HNZ-PAGE%d-AAAAA", i), func(r *map[string]any) {
			(*r)["issued_at"] = time.Now().UTC().Add(time.Duration(i) * time.Minute).Format(baseDate)
			(*r)["note"] = fmt.Sprint(i)
		})
	}
	var notes []string
	cursor, pages := "", 0
	for {
		path := invitesPath + "?limit=3"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		a := in.send(t, http.MethodGet, path, "", operator, "")
		if a.code != http.StatusOK {
			t.Fatalf("page %d: %d %s", pages+1, a.code, a.body)
		}
		var page struct {
			Data []map[string]any `json:"data"`
			Next string           `json:"next"`
		}
		if err := json.Unmarshal([]byte(a.body), &page); err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Data {
			notes = append(notes, fmt.Sprint(row["note"]))
		}
		pages++
		if page.Next == "" {
			break
		}
		if len(page.Data) != 3 {
			t.Fatalf("page %d holds %d, want 3 before the last", pages, len(page.Data))
		}
		cursor = page.Next
		if pages > 5 {
			t.Fatal("the cursor never ends")
		}
	}
	if pages != 3 || strings.Join(notes, ",") != "6,5,4,3,2,1,0" {
		t.Fatalf("%d pages, notes %v; want 3 pages newest first, 6 down to 0", pages, notes)
	}
	if a := in.send(t, http.MethodGet, invitesPath+"?cursor=not-a-cursor", "", operator, ""); !problem(a, http.StatusBadRequest) {
		t.Fatalf("a bad cursor: want 400, got %d %s", a.code, a.body)
	}
}

// TestAFirstIssueWhileAnotherPodHoldsTheSealLeaseIs503AndMintsNothing: with the lease on the
// key held elsewhere, the issue mints nothing, writes nothing, and answers 503 with
// Retry-After; once the other pod is done it works.
func TestAFirstIssueWhileAnotherPodHoldsTheSealLeaseIs503AndMintsNothing(t *testing.T) {
	in := mountUnsealed(t)
	give, _, err := in.svc.Coord.Lease("network:seal", inviteRef, sealLease)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := in.send(t, http.MethodPost, invitesPath, `{"count":1}`, operator, "")
	if !problem(a, http.StatusServiceUnavailable) {
		t.Fatalf("want a 503 problem document, got %d %q %s", a.code, a.kind, a.body)
	}
	if secs, err := strconv.Atoi(retryAfter(a)); err != nil || secs < 1 {
		t.Fatalf("Retry-After %q, want seconds", retryAfter(a))
	}
	if _, err := in.kms.GetSecret(context.Background(), inviteRef); !errors.Is(err, secret.ErrNone) {
		t.Fatalf("a mint without the lease left %v in KMS", err)
	}
	if n := len(records(t, "hanzo", invitesName)); n != 0 {
		t.Fatalf("a refused issue wrote %d invites", n)
	}
	give()
	in.issue(t, `{"count":1}`)
}

// TestACodeIssuedOnOnePodVerifiesOnAnother, and first issues made at once on two pods end
// with one key: every code, from either pod, verifies on both.
func TestACodeIssuedOnOnePodVerifiesOnAnother(t *testing.T) {
	a := mountUnsealed(t)
	b := a.pod(t)
	done := make(chan []string, 2)
	for _, p := range []*intake{a, b} {
		go func() {
			var codes []string
			for range 3 {
				// A first issue that meets the other pod mid-seal is told to come back.
				for tries := 0; ; tries++ {
					r := p.send(t, http.MethodPost, invitesPath, `{"count":1}`, operator, "")
					if r.code == http.StatusCreated {
						var out issued
						_ = json.Unmarshal([]byte(r.body), &out)
						codes = append(codes, out.Invites[0].Code)
						break
					}
					if r.code != http.StatusServiceUnavailable || tries > 50 {
						t.Errorf("issue: %d %s", r.code, r.body)
						break
					}
				}
			}
			done <- codes
		}()
	}
	var codes []string
	codes = append(codes, <-done...)
	codes = append(codes, <-done...)
	k, err := a.kms.GetSecret(context.Background(), inviteRef)
	if err != nil || len(k) < 32 {
		t.Fatalf("KMS holds %d bytes, %v", len(k), err)
	}
	for i, c := range codes {
		for _, p := range []*intake{a, b} {
			if r := p.verify(t, c, from(i)); r.body != yes {
				t.Fatalf("code %d does not verify on a pod: %d %s", i, r.code, r.body)
			}
		}
	}
	if len(codes) != 6 {
		t.Fatalf("%d codes issued, want 6", len(codes))
	}
}
