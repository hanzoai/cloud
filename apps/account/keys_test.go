package account

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// serveKeys models IAM's key collection the way IAM serves it: rows keyed by
// (owner, name), a create that refuses a taken name, reads that mask the secret,
// an update that writes every editable field, keeps the class, and makes a
// revoke final. No DELETE: this surface never deletes a key, and a regression
// that tried would answer 405 here.
func (f *fakeIAM) serveKeys(mux *http.ServeMux, ok func(http.ResponseWriter, any), bad func(http.ResponseWriter, string),
	resource func(http.ResponseWriter, any), missing func(http.ResponseWriter, string)) {
	fail := func(w http.ResponseWriter, r *http.Request) bool {
		if f.refuseKeys != 0 && (!f.refuseKeyWrites || r.Method != http.MethodGet) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.refuseKeys)
			_, _ = w.Write([]byte(`{"error":"iam says no"}`))
			return true
		}
		if f.failKeys {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"status":500,"error":"store unavailable"}`))
		}
		return f.failKeys
	}

	mux.HandleFunc("GET /v1/iam/keys", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		if fail(w, r) {
			return
		}
		owner := r.URL.Query().Get("owner")
		f.mu.Lock()
		defer f.mu.Unlock()
		var rows []*fakeKey
		for _, k := range f.keys {
			if k.row["owner"] == owner {
				rows = append(rows, k)
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].row["createdTime"].(string) > rows[j].row["createdTime"].(string) })
		out := []map[string]any{}
		for _, k := range rows {
			out = append(out, masked(k.row))
		}
		ok(w, map[string]any{"keys": out})
	})

	mux.HandleFunc("POST /v1/iam/keys", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		if fail(w, r) {
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		owner, name, user := str(in["owner"]), str(in["name"]), str(in["user"])
		scope := str(in["scope"])
		f.mu.Lock()
		defer f.mu.Unlock()
		f.mintedFor = append(f.mintedFor, user)
		if f.failMintKey {
			bad(w, "mint failed")
			return
		}
		if home, _, _ := strings.Cut(user, "/"); home != owner && !f.members[user+"|"+owner] {
			bad(w, "key user must be a member of the key's owner")
			return
		}
		if _, taken := f.keys[owner+"/"+name]; taken {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"status":"error","msg":"key already exists"}`))
			return
		}
		f.made++
		tag := fmt.Sprintf("%s-%04d", strings.NewReplacer("/", "-", "~", "-").Replace(owner+"-"+name), f.made)
		pub, secret := "pk-live-"+tag, "sk-live-"+tag
		publish := strings.HasPrefix(scope, "publish") && !f.ignoreKeyType
		row := map[string]any{
			"owner": owner, "name": name, "user": user, "displayName": in["displayName"],
			"scope": scope, "state": in["state"], "expireTime": in["expireTime"],
			"accessKey":   pub,
			"createdTime": time.Date(2026, 1, 1, 0, 0, f.made, 0, time.UTC).Format(time.RFC3339),
		}
		row["updatedTime"] = row["createdTime"]
		presented := pub
		out := clone(row)
		if !publish {
			presented = secret
			out["accessSecret"] = secret
		}
		row["prefix"] = presented[:12]
		out["prefix"] = row["prefix"]
		f.keys[owner+"/"+name] = &fakeKey{row: row, secret: presented}
		ok(w, out)
	})

	mux.HandleFunc("GET /v1/iam/keys/{owner}/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		if fail(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		k, held := f.keys[r.PathValue("owner")+"/"+r.PathValue("name")]
		if !held {
			missing(w, "key not found")
			return
		}
		resource(w, masked(k.row))
	})

	mux.HandleFunc("PUT /v1/iam/keys/{owner}/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.capture(r)
		if fail(w, r) {
			return
		}
		ref := r.PathValue("owner") + "/" + r.PathValue("name")
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		k, held := f.keys[ref]
		if !held {
			missing(w, "key not found")
			return
		}
		if k.row["state"] == keyRevoked {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"status":"error","msg":"a revoked key is final"}`))
			return
		}
		if strings.HasPrefix(str(in["scope"]), "publish") != strings.HasPrefix(str(k.row["scope"]), "publish") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","msg":"a key's class is fixed"}`))
			return
		}
		for _, field := range []string{"displayName", "user", "expireTime", "state", "scope"} {
			k.row[field] = in[field]
		}
		f.edited = append(f.edited, ref)
		if k.row["state"] == keyRevoked {
			k.row["revoker"], k.row["revokeTime"] = in["revoker"], "2026-02-01T00:00:00Z"
			f.revoked = append(f.revoked, ref)
		}
		resource(w, masked(k.row))
	})
}

func str(v any) string { s, _ := v.(string); return s }

func clone(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[k] = v
	}
	return out
}

// masked is a row as IAM reads it back: no confidential half.
func masked(row map[string]any) map[string]any {
	out := clone(row)
	delete(out, "accessSecret")
	return out
}

// works asks what IAM's resolver would: does this credential still name a live
// key? A revoked, expired or absent key does not.
func (f *fakeIAM) works(credential string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.secret != credential {
			continue
		}
		if st := str(k.row["state"]); st != "" && st != "Active" {
			return false
		}
		if exp := str(k.row["expireTime"]); exp != "" {
			t, err := time.Parse(time.RFC3339, exp)
			return err == nil && time.Now().Before(t)
		}
		return true
	}
	return false
}

// seed files a key row directly, as another IAM client would have.
func (f *fakeIAM) seed(owner, name, user string, row map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.made++
	r := map[string]any{
		"owner": owner, "name": name, "user": user, "state": "Active",
		"accessKey":   "pk-live-seed-" + name,
		"createdTime": time.Date(2026, 1, 1, 0, 0, f.made, 0, time.UTC).Format(time.RFC3339),
	}
	for k, v := range row {
		r[k] = v
	}
	f.keys[owner+"/"+name] = &fakeKey{row: r, secret: "sk-live-seed-" + name}
}

// keysOf reads a listing answer: the keys a caller sees.
func keysOf(t *testing.T) func(int, []byte) []apiKey {
	return func(code int, body []byte) []apiKey {
		t.Helper()
		if code != http.StatusOK {
			t.Fatalf("list keys: want 200, got %d (%s)", code, body)
		}
		var st apiKeyList
		mustJSON(t, body, &st)
		return st.Keys
	}
}

// created reads a create answer: the new key, with its secret.
func created(t *testing.T) func(int, []byte) apiKey {
	return func(code int, body []byte) apiKey {
		t.Helper()
		if code != http.StatusOK {
			t.Fatalf("create key: want 200, got %d (%s)", code, body)
		}
		var k apiKey
		mustJSON(t, body, &k)
		return k
	}
}

func byID(keys []apiKey, id string) (apiKey, bool) {
	for _, k := range keys {
		if k.ID == id {
			return k, true
		}
	}
	return apiKey{}, false
}

func TestKeys_RequireValidatedPrincipal(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// No X-User-Id: no validated principal, so 403, and IAM is never touched, even
	// with a forged X-Org-Id.
	for _, w := range []struct{ method, path string }{
		{http.MethodGet, "/v1/account/keys"},
		{http.MethodPost, "/v1/account/keys"},
		{http.MethodPatch, "/v1/account/keys/k1"},
		{http.MethodDelete, "/v1/account/keys/k1"},
	} {
		if code, _ := call(t, app, w.method, w.path, "", "victim", ""); code != http.StatusForbidden {
			t.Fatalf("%s %s with a forged org and no principal: want 403, got %d", w.method, w.path, code)
		}
	}
	if len(f.mintedFor)+len(f.edited) != 0 {
		t.Fatalf("IAM reached on the unauthenticated path: minted=%v edited=%v", f.mintedFor, f.edited)
	}
}

// THE OWNER'S REPORT: creating a key and reloading must show the old keys too.
// A second create is a new credential beside the first, never a rotation.
func TestKeys_ASecondCreateLeavesTheFirstWorkingAndListed(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	if keys := keysOf(t)(call(t, app, http.MethodGet, "/v1/account/keys", "alice", "acme", "")); len(keys) != 0 {
		t.Fatalf("a new user holds no keys, got %+v", keys)
	}
	first := created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", `{"name":"production"}`))
	second := created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", `{"name":"staging"}`))

	for _, k := range []apiKey{first, second} {
		if k.Type != keyTypeSecret || !strings.HasPrefix(k.Key, "sk-") || k.Status != statusActive {
			t.Fatalf("a create returns its secret once, got %+v", k)
		}
		if k.Prefix == "" || !strings.HasPrefix(k.Key, k.Prefix) {
			t.Fatalf("the prefix %q must be the head of the secret", k.Prefix)
		}
		if k.Creator != "acme/alice" {
			t.Fatalf("a key names its creator, got %q", k.Creator)
		}
	}
	// The confidential client authenticated as `hanzo-console`, NOT as the caller.
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("hanzo-console:s3cr3t")); f.gotAuth != want {
		t.Fatalf("IAM auth: want the confidential client's Basic, got %q", f.gotAuth)
	}
	if first.ID == second.ID || first.Key == second.Key {
		t.Fatalf("two creates must be two keys: %+v %+v", first, second)
	}
	if !f.works(first.Key) || !f.works(second.Key) {
		t.Fatal("the second create stopped the first key working")
	}
	if len(f.edited) != 0 {
		t.Fatalf("a create touched another key: %v", f.edited)
	}

	_, body := call(t, app, http.MethodGet, "/v1/account/keys", "alice", "acme", "")
	keys := keysOf(t)(http.StatusOK, body)
	if len(keys) != 2 {
		t.Fatalf("a reload must list both keys, got %s", body)
	}
	for _, want := range []apiKey{first, second} {
		got, listed := byID(keys, want.ID)
		if !listed || got.Name != want.Name || got.Prefix != want.Prefix || got.Status != statusActive {
			t.Fatalf("key %s listed as %+v, want name %q prefix %q", want.ID, got, want.Name, want.Prefix)
		}
	}
	if keys[0].ID != second.ID {
		t.Fatalf("keys list newest first, got %s first", keys[0].ID)
	}
	if strings.Contains(string(body), first.Key) || strings.Contains(string(body), second.Key) {
		t.Fatalf("the listing leaked a secret: %s", body)
	}
}

// Revoking acts on exactly the key its id names, and the key stays listed.
func TestKeys_RevokeRemovesExactlyOne(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	var made []apiKey
	for _, name := range []string{"a", "b", "c"} {
		made = append(made, created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", `{"name":"`+name+`"}`)))
	}
	code, body := call(t, app, http.MethodDelete, "/v1/account/keys/"+made[1].ID, "alice", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("revoke: want 200, got %d (%s)", code, body)
	}
	var gone apiKey
	mustJSON(t, body, &gone)
	if gone.ID != made[1].ID || gone.Status != statusRevoked || gone.Revoker != "acme/alice" || gone.Revoked == "" {
		t.Fatalf("revoke answered %+v", gone)
	}
	if len(f.revoked) != 1 || f.revoked[0] != "acme/"+made[1].ID {
		t.Fatalf("exactly one key must be revoked, got %v", f.revoked)
	}
	if f.works(made[1].Key) {
		t.Fatal("the revoked key still works")
	}
	if !f.works(made[0].Key) || !f.works(made[2].Key) {
		t.Fatal("revoking one key stopped another")
	}

	keys := keysOf(t)(call(t, app, http.MethodGet, "/v1/account/keys", "alice", "acme", ""))
	if len(keys) != 3 {
		t.Fatalf("a revoked key stays listed for audit, got %d keys", len(keys))
	}
	for _, k := range keys {
		want := statusActive
		if k.ID == made[1].ID {
			want = statusRevoked
		}
		if k.Status != want {
			t.Fatalf("key %s is %s, want %s", k.ID, k.Status, want)
		}
	}

	// Revoking it again changes nothing; editing it is refused.
	if code, _ := call(t, app, http.MethodDelete, "/v1/account/keys/"+made[1].ID, "alice", "acme", ""); code != http.StatusOK || len(f.revoked) != 1 {
		t.Fatalf("a second revoke: %d, revoked %v", code, f.revoked)
	}
	if code, _ := call(t, app, http.MethodPatch, "/v1/account/keys/"+made[1].ID, "alice", "acme", `{"name":"x"}`); code != http.StatusConflict {
		t.Fatalf("editing a revoked key: want 409, got %d", code)
	}
}

// An edit changes the key's policy and leaves its secret, which keeps working.
func TestKeys_EditTakesEffectWithoutChangingTheSecret(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	k := created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", `{"name":"ci"}`))

	when := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	code, body := call(t, app, http.MethodPatch, "/v1/account/keys/"+k.ID, "alice", "acme",
		`{"name":"deploy","limit":["model:zen5"],"expires":"`+when+`"}`)
	if code != http.StatusOK {
		t.Fatalf("edit: want 200, got %d (%s)", code, body)
	}
	var got apiKey
	mustJSON(t, body, &got)
	if got.Name != "deploy" || got.Expires != when || len(got.Limit) != 1 || got.Limit[0] != "model:zen5" {
		t.Fatalf("the edit did not take: %+v", got)
	}
	if got.Prefix != k.Prefix || got.Key != "" {
		t.Fatalf("an edit must not reissue or reveal the secret: %+v", got)
	}
	if !f.works(k.Key) {
		t.Fatal("the edited key stopped working")
	}

	// An absent field is kept; an empty limit and an empty expiry clear them.
	_, body = call(t, app, http.MethodPatch, "/v1/account/keys/"+k.ID, "alice", "acme", `{"limit":[],"expires":""}`)
	got = apiKey{}
	mustJSON(t, body, &got)
	if got.Name != "deploy" || got.Expires != "" || len(got.Limit) != 0 {
		t.Fatalf("clearing the limit and expiry gave %+v", got)
	}

	for _, bad := range []string{`{"limit":["galaxy:x"]}`, `{"expires":"tomorrow"}`, `{"expires":"2001-01-01T00:00:00Z"}`} {
		if code, _ := call(t, app, http.MethodPatch, "/v1/account/keys/"+k.ID, "alice", "acme", bad); code != http.StatusBadRequest {
			t.Fatalf("edit %s: want 400, got %d", bad, code)
		}
	}
}

// A key past its expiry is listed as expired and no longer works.
func TestKeys_AnExpiredKeyIsListedAsExpired(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	f.seed("acme", "old", "alice", map[string]any{"expireTime": "2026-01-02T00:00:00Z"})

	keys := keysOf(t)(call(t, app, http.MethodGet, "/v1/account/keys", "alice", "acme", ""))
	if len(keys) != 1 || keys[0].Status != statusExpired || keys[0].Expires != "2026-01-02T00:00:00Z" {
		t.Fatalf("want one expired key, got %+v", keys)
	}
	if f.works("sk-live-seed-old") {
		t.Fatal("an expired key works")
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", `{"expires":"2001-01-01T00:00:00Z"}`); code != http.StatusBadRequest {
		t.Fatalf("a key that is born expired: want 400, got %d", code)
	}
}

// A member sees and manages their own keys; an org admin sees and revokes every
// key the org holds.
func TestKeys_AMemberSeesTheirOwnAndAnAdminSeesTheOrgs(t *testing.T) {
	f := newFakeIAM()
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	app := auditedApp(t, f.server(t).URL, trail)
	alice := created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", `{"name":"alice"}`))
	bob := created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "bob", "acme", `{"name":"bob"}`))

	mine := keysOf(t)(call(t, app, http.MethodGet, "/v1/account/keys", "alice", "acme", ""))
	if len(mine) != 1 || mine[0].ID != alice.ID {
		t.Fatalf("alice must see only her key, got %+v", mine)
	}
	// Another member's key answers exactly as a missing one.
	for _, m := range []string{http.MethodPatch, http.MethodDelete} {
		if code, _ := call(t, app, m, "/v1/account/keys/"+bob.ID, "alice", "acme", `{"name":"mine now"}`); code != http.StatusNotFound {
			t.Fatalf("%s on another member's key: want 404, got %d", m, code)
		}
	}
	if !f.works(bob.Key) {
		t.Fatal("a member reached another member's key")
	}

	admin := map[string]string{"X-User-Id": "carol", "X-Org-Id": "acme", "X-User-IsOrgAdmin": "true"}
	keys := keysOf(t)(callH(t, app, http.MethodGet, "/v1/account/keys", admin, ""))
	if len(keys) != 2 {
		t.Fatalf("an org admin sees every key of the org, got %+v", keys)
	}
	// Seeing is not editing: a key's policy is its holder's.
	if code, _ := callH(t, app, http.MethodPatch, "/v1/account/keys/"+bob.ID, admin, `{"limit":[]}`); code != http.StatusForbidden {
		t.Fatalf("an org admin edited a member's key: %d", code)
	}
	if code, body := callH(t, app, http.MethodDelete, "/v1/account/keys/"+bob.ID, admin, ""); code != http.StatusOK {
		t.Fatalf("an org admin revokes a member's key: %d %s", code, body)
	}
	if f.works(bob.Key) || !f.works(alice.Key) {
		t.Fatal("the admin's revoke did not act on exactly bob's key")
	}
	keys = keysOf(t)(callH(t, app, http.MethodGet, "/v1/account/keys", admin, ""))
	if k, _ := byID(keys, bob.ID); k.Revoker != "acme/carol" {
		t.Fatalf("the revoke must name the admin who did it, got %+v", k)
	}
	if n, _ := trail.Head(); n != 1 {
		t.Fatalf("revoking a member's key must be on the audit trail once, got %d", n)
	}

	// Another org's admin sees none of it.
	other := map[string]string{"X-User-Id": "mallory", "X-Org-Id": "evil", "X-User-IsOrgAdmin": "true"}
	if keys := keysOf(t)(callH(t, app, http.MethodGet, "/v1/account/keys", other, "")); len(keys) != 0 {
		t.Fatalf("another org's admin saw acme's keys: %+v", keys)
	}
}

// A publishable key is a TYPE on the one resource, and lists its full value.
func TestKeys_PublishableIsATypeAndListsItsValue(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	pub := created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", `{"type":"publishable","name":"web"}`))
	if pub.Type != keyTypePublishable || !strings.HasPrefix(pub.Key, "pk-") {
		t.Fatalf("want a pk- publishable key, got %+v", pub)
	}
	sec := created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", `{"type":"secret"}`))
	keys := keysOf(t)(call(t, app, http.MethodGet, "/v1/account/keys", "alice", "acme", ""))
	if len(keys) != 2 {
		t.Fatalf("want both keys, got %+v", keys)
	}
	got, _ := byID(keys, pub.ID)
	if got.Key != pub.Key || got.Type != keyTypePublishable {
		t.Fatalf("a publishable key lists its full value, got %+v", got)
	}
	if got, _ := byID(keys, sec.ID); got.Key != "" || got.Type != keyTypeSecret || got.Name != "key 2" {
		t.Fatalf("a secret key lists no value and a default name, got %+v", got)
	}
	// A publishable key's reach can change; its class cannot.
	if code, body := call(t, app, http.MethodPatch, "/v1/account/keys/"+pub.ID, "alice", "acme", `{"limit":["model:zen5"]}`); code != http.StatusOK {
		t.Fatalf("narrow a publishable key: %d %s", code, body)
	}
	keys = keysOf(t)(call(t, app, http.MethodGet, "/v1/account/keys", "alice", "acme", ""))
	if got, _ := byID(keys, pub.ID); got.Type != keyTypePublishable || len(got.Limit) != 1 {
		t.Fatalf("the narrowed publishable key is %+v", got)
	}
}

// An unrecognized type is REFUSED, never defaulted: a caller asking for a
// browser-safe key must never be handed a session-equivalent secret.
func TestKeys_UnknownTypeRefused(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	for _, typ := range []string{"public", "publishible", "sk", "SECRET"} {
		if code, _ := call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", `{"type":"`+typ+`"}`); code != http.StatusBadRequest {
			t.Fatalf("POST type=%q: want 400, got %d", typ, code)
		}
	}
	if len(f.mintedFor) != 0 {
		t.Fatalf("a refused type reached IAM: %v", f.mintedFor)
	}
}

// An IAM that does not honour the publish scope answers with an sk-. Cloud must
// not hand that back as a publishable key.
func TestKeys_RefusesAKeyWhosePrefixContradictsItsType(t *testing.T) {
	f := newFakeIAM()
	f.ignoreKeyType = true
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	code, body := call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", `{"type":"publishable"}`)
	if code != http.StatusServiceUnavailable || strings.Contains(string(body), "sk-") {
		t.Fatalf("want a 503 carrying no secret, got %d (%s)", code, body)
	}
}

// The org's abuse ceiling counts live keys only, and refuses past it by name.
func TestKeys_TheCeilingCountsLiveKeys(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	for i := range keyCeiling {
		f.seed("acme", fmt.Sprintf("bulk-%d", i), "bob", nil)
	}
	f.seed("acme", "gone", "bob", map[string]any{"state": keyRevoked})
	code, body := call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", "")
	if code != http.StatusConflict || !strings.Contains(string(body), "1000") {
		t.Fatalf("past the ceiling: want 409 naming it, got %d (%s)", code, body)
	}
	f.mu.Lock()
	f.keys["acme/bulk-0"].row["state"] = keyRevoked
	f.mu.Unlock()
	created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", ""))
}

// A degraded IAM is reported, never served as "you have no keys".
func TestKeys_AnUnreadableIAMIsNotAnEmptyList(t *testing.T) {
	f := newFakeIAM()
	f.failKeys = true
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	if code, body := call(t, app, http.MethodGet, "/v1/account/keys", "alice", "acme", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d (%s)", code, body)
	}
}

func TestKeys_NotConfigured_501(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "", "")
	if code, body := call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", ""); code != http.StatusNotImplemented {
		t.Fatalf("unconfigured create: want 501, got %d (%s)", code, body)
	}
}

func TestKeys_MintUpstreamFailure_503(t *testing.T) {
	f := newFakeIAM()
	f.failMintKey = true
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	if code, body := call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("mint upstream failure: want 503, got %d (%s)", code, body)
	}
}

// AN IAM FAILURE IS NEVER A 502. The edge in front of api.hanzo.ai replaces an
// origin 502 with a page of its own that carries no CORS headers, so a person
// creating a key on hanzo.ai read every IAM failure as "Failed to fetch" (#755).
// IAM's refusal keeps its status and its words, and a 401 (IAM refusing a sign-in
// cloud already accepted) is a 403, never a sign-out; anything else (IAM down, a
// 5xx, an answer that cannot be used) is 503, saying what could not be done.
func TestKeys_AnIAMFailureIsNeverA502(t *testing.T) {
	cases := []struct {
		name         string
		refuse       int
		writes, down bool
		mint         bool
		method       string
		want         int
		says         string
	}{
		{name: "list, IAM down", down: true, method: http.MethodGet, want: http.StatusServiceUnavailable, says: "could not read API keys"},
		{name: "create, IAM down", down: true, method: http.MethodPost, want: http.StatusServiceUnavailable, says: "could not read API keys"},
		{name: "create, the mint failed", mint: true, method: http.MethodPost, want: http.StatusServiceUnavailable, says: "could not create an API key"},
		{name: "create, IAM refuses the request", refuse: http.StatusBadRequest, method: http.MethodPost, want: http.StatusBadRequest, says: "iam says no"},
		{name: "create, IAM forbids it", refuse: http.StatusForbidden, method: http.MethodPost, want: http.StatusForbidden, says: "could not read API keys"},
		{name: "create, IAM refuses the sign-in", refuse: http.StatusUnauthorized, method: http.MethodPost, want: http.StatusForbidden, says: "could not read API keys"},
		{name: "create, IAM is busy", refuse: http.StatusTooManyRequests, method: http.MethodPost, want: http.StatusTooManyRequests, says: "iam says no"},
		{name: "create, the write is refused", refuse: http.StatusForbidden, writes: true, method: http.MethodPost, want: http.StatusForbidden, says: "could not create an API key"},
		{name: "edit, the write fails", refuse: http.StatusInternalServerError, writes: true, method: http.MethodPatch, want: http.StatusServiceUnavailable, says: "could not edit API key"},
		{name: "revoke, the write is refused", refuse: http.StatusForbidden, writes: true, method: http.MethodDelete, want: http.StatusForbidden, says: "could not revoke API key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIAM()
			app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
			mine := created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme", ""))
			f.mu.Lock()
			f.refuseKeys, f.refuseKeyWrites, f.failKeys, f.failMintKey = tc.refuse, tc.writes, tc.down, tc.mint
			f.mu.Unlock()
			path, body := "/v1/account/keys", ""
			switch tc.method {
			case http.MethodPatch:
				path, body = path+"/"+mine.ID, `{"name":"renamed"}`
			case http.MethodDelete:
				path += "/" + mine.ID
			}
			code, raw := call(t, app, tc.method, path, "alice", "acme", body)
			if code != tc.want || !strings.Contains(string(raw), tc.says) {
				t.Fatalf("%s %s: %d %s, want %d saying %q", tc.method, path, code, raw, tc.want, tc.says)
			}
		})
	}
}

// The direct-Bearer path stamps X-User-Id with the subject UUID and X-User-Name
// with the username. The key must name the username.
func TestKeys_DirectBearerPath_MintsByUsernameNotUUID(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	h := map[string]string{"X-User-Id": "2d4d67ab-30f1-474e-b81f-f60461852259", "X-User-Name": "z", "X-Org-Id": "hanzo"}
	k := created(t)(callH(t, app, http.MethodPost, "/v1/account/keys", h, ""))
	if len(f.mintedFor) != 1 || f.mintedFor[0] != "hanzo/z" || k.Creator != "hanzo/z" {
		t.Fatalf("direct-path mint must name hanzo/z, got %v %+v", f.mintedFor, k)
	}
}

// A person working in an org they joined by membership keeps keys there. The row
// is filed in that org, names their own row, and is theirs alone; IAM refuses the
// same create for someone who is not a member.
func TestKeys_AMemberKeepsKeysInTheOrgTheyJoined(t *testing.T) {
	f := newFakeIAM()
	f.members["agency/josh|client"] = true
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	member := map[string]string{"X-User-Id": "josh", "X-User-Name": "josh", "X-Org-Id": "client", "X-User-Owner": "agency"}

	k := created(t)(callH(t, app, http.MethodPost, "/v1/account/keys", member, ""))
	if len(f.mintedFor) != 1 || f.mintedFor[0] != "agency/josh" || k.Creator != "agency/josh" {
		t.Fatalf("the key must name the member's own row agency/josh, got %v %+v", f.mintedFor, k)
	}
	if !strings.HasPrefix(k.ID, "agency~josh-secret-") {
		t.Fatalf("a member's key id carries their home, got %q", k.ID)
	}
	if keys := keysOf(t)(callH(t, app, http.MethodGet, "/v1/account/keys", member, "")); len(keys) != 1 {
		t.Fatalf("the member's key should be listed to them: %+v", keys)
	}
	namesake := map[string]string{"X-User-Id": "josh", "X-User-Name": "josh", "X-Org-Id": "client", "X-User-Owner": "client"}
	if keys := keysOf(t)(callH(t, app, http.MethodGet, "/v1/account/keys", namesake, "")); len(keys) != 0 {
		t.Fatalf("a namesake in client was shown the member's key: %+v", keys)
	}
	if code, _ := callH(t, app, http.MethodDelete, "/v1/account/keys/"+k.ID, namesake, ""); code != http.StatusNotFound {
		t.Fatalf("a namesake revoked the member's key: %d", code)
	}
	if code, _ := callH(t, app, http.MethodDelete, "/v1/account/keys/"+k.ID, member, ""); code != http.StatusOK || f.works(k.Key) {
		t.Fatalf("the member revokes their own key: %d", code)
	}

	stranger := map[string]string{"X-User-Id": "stray", "X-User-Name": "stray", "X-Org-Id": "client", "X-User-Owner": "elsewhere"}
	if code, body := callH(t, app, http.MethodPost, "/v1/account/keys", stranger, ""); code == http.StatusOK {
		t.Fatalf("a non-member created a key in client: %s", body)
	}
}

// A request that ARRIVES on a member's key carries the holder as "<home>/<name>".
// Key management acts on the holder's own keys, never the acting org's namesake.
func TestKeys_AMemberKeyManagesItsHoldersKeys(t *testing.T) {
	f := newFakeIAM()
	f.members["agency/josh|client"] = true
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	onKey := map[string]string{"X-User-Id": "agency/josh", "X-User-Name": "josh", "X-Org-Id": "client", "X-User-Owner": "client"}

	k := created(t)(callH(t, app, http.MethodPost, "/v1/account/keys", onKey, ""))
	if len(f.mintedFor) != 1 || f.mintedFor[0] != "agency/josh" {
		t.Fatalf("a member key minted for %v, want the holder agency/josh", f.mintedFor)
	}
	if code, _ := callH(t, app, http.MethodDelete, "/v1/account/keys/"+k.ID, onKey, ""); code != http.StatusOK || len(f.revoked) != 1 {
		t.Fatalf("revoke on a member key: %d %v", code, f.revoked)
	}
}

// A request that arrives on a LIMITED key — here one with only a budget — may
// revoke keys and may not create or edit them: either could hand its holder a key
// reaching or spending further than the one they presented, and a grant only ever
// narrows.
func TestKeys_ALimitedKeyCannotWidenItself(t *testing.T) {
	f := newFakeIAM()
	f.seed("acme", "k1", "alice", nil)
	t.Setenv("IAM_URL", f.server(t).URL)
	t.Setenv("IAM_MINT_CLIENT_ID", "hanzo-console")
	t.Setenv("IAM_MINT_CLIENT_SECRET", "s3cr3t")
	t.Setenv("CLOUD_BRAND", "hanzo")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// The boundary, attesting a key that may reach one capability.
	app.Use(zip.H(func(c *zip.Ctx) error {
		principal.Mint(c, principal.Principal{Org: "acme", User: "alice", Subject: "acme/alice",
			Kind: principal.Key, Holder: "acme/alice", Limit: []string{"spend:month:500"}})
		return c.Continue()
	}))
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	key := map[string]string{"X-User-Id": "alice", "X-Org-Id": "acme", "Authorization": "Bearer sk-limited"}
	if code, body := callH(t, app, http.MethodPost, "/v1/account/keys", key, ""); code != http.StatusForbidden || !strings.Contains(string(body), "limited") {
		t.Fatalf("a limited key created a key: %d %s", code, body)
	}
	if code, _ := callH(t, app, http.MethodPatch, "/v1/account/keys/k1", key, `{"limit":[]}`); code != http.StatusForbidden {
		t.Fatalf("a limited key edited a key: %d", code)
	}
	if code, body := callH(t, app, http.MethodDelete, "/v1/account/keys/k1", key, ""); code != http.StatusOK {
		t.Fatalf("a limited key revokes: %d %s", code, body)
	}
	if len(f.mintedFor) != 0 || len(f.revoked) != 1 {
		t.Fatalf("minted %v revoked %v", f.mintedFor, f.revoked)
	}
}

// A key's budget and rate are set at creation, listed, and edited without the
// secret changing; they ride the key's grant, never its reach.
func TestKeys_BudgetAndRateAreTheKeysOwn(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	k := created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "alice", "acme",
		`{"name":"ci","limit":["read:*"],"budget":{"month":5000,"total":0},"rate":60}`))
	if k.Budget == nil || k.Budget.Month != 5000 || k.Budget.Total != 0 || k.Rate != 60 {
		t.Fatalf("created %+v budget %+v", k, k.Budget)
	}
	if len(k.Limit) != 1 || k.Limit[0] != "read:*" {
		t.Fatalf("a budget or rate leaked into the limit: %v", k.Limit)
	}
	f.mu.Lock()
	scope := str(f.keys["acme/"+k.ID].row["scope"])
	f.mu.Unlock()
	if scope != "read:*,spend:month:5000,rate:minute:60" {
		t.Fatalf("stored scope %q", scope)
	}

	code, body := call(t, app, http.MethodPatch, "/v1/account/keys/"+k.ID, "alice", "acme", `{"budget":{"month":0,"total":20000}}`)
	if code != http.StatusOK {
		t.Fatalf("edit budget: %d %s", code, body)
	}
	var got apiKey
	mustJSON(t, body, &got)
	if got.Budget == nil || got.Budget.Month != 0 || got.Budget.Total != 20000 || got.Rate != 60 || len(got.Limit) != 1 {
		t.Fatalf("editing the budget kept the rest? %+v %+v", got, got.Budget)
	}
	if !f.works(k.Key) {
		t.Fatal("editing the budget changed the secret")
	}
	for _, bad := range []string{`{"budget":{"month":-1}}`, `{"rate":-5}`, `{"limit":["spend:month:1"]}`} {
		if code, _ := call(t, app, http.MethodPatch, "/v1/account/keys/"+k.ID, "alice", "acme", bad); code != http.StatusBadRequest {
			t.Fatalf("edit %s: want 400, got %d", bad, code)
		}
	}
}

// When a key was last used is IAM's record, listed as the key's own.
func TestKeys_LastUsedIsListed(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	f.seed("acme", "used", "alice", map[string]any{"usedTime": "2026-10-04T12:00:00Z"})
	f.seed("acme", "idle", "alice", nil)
	keys := keysOf(t)(call(t, app, http.MethodGet, "/v1/account/keys", "alice", "acme", ""))
	used, _ := byID(keys, "used")
	idle, _ := byID(keys, "idle")
	if used.Used != "2026-10-04T12:00:00Z" || idle.Used != "" {
		t.Fatalf("used %q idle %q", used.Used, idle.Used)
	}
}

// auditedApp is mountApp with an audit trail, which revoking another person's key
// requires.
func auditedApp(t *testing.T, base string, trail *audit.Recorder) *zip.App {
	t.Helper()
	t.Setenv("IAM_URL", base)
	t.Setenv("IAM_MINT_CLIENT_ID", "hanzo-console")
	t.Setenv("IAM_MINT_CLIENT_SECRET", "s3cr3t")
	t.Setenv("CLOUD_BRAND", "hanzo")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Use(app, cloud.Deps{Audit: trail}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app
}

// With no audit trail, an admin cannot revoke someone else's key, and a member can
// still revoke their own.
func TestKeys_RevokingAnotherPersonsKeyNeedsTheAuditTrail(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")
	bob := created(t)(call(t, app, http.MethodPost, "/v1/account/keys", "bob", "acme", ""))
	admin := map[string]string{"X-User-Id": "carol", "X-Org-Id": "acme", "X-User-IsOrgAdmin": "true"}
	if code, _ := callH(t, app, http.MethodDelete, "/v1/account/keys/"+bob.ID, admin, ""); code != http.StatusServiceUnavailable || !f.works(bob.Key) {
		t.Fatalf("an unaudited revoke of another person's key: %d", code)
	}
	if code, _ := call(t, app, http.MethodDelete, "/v1/account/keys/"+bob.ID, "bob", "acme", ""); code != http.StatusOK || f.works(bob.Key) {
		t.Fatalf("bob revoking his own key: %d", code)
	}
}
