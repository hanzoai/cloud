package account

import (
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// Keys against the real IAM, behind cloud's real identity boundary, with
// hanzo-console on IAM_KEY_MINT_ALLOWED_APPS as the deployment grants it. That
// client serves org hanzo, and IAM confines its own reads to hanzo; every customer
// here acts in another org, so what they see is what IAM shows their own token.

// A founder manages keys in the org they founded: both classes created and listed,
// one renamed and one revoked.
func TestKeys_RealIAM_AFounderManagesKeysInTheirOrg(t *testing.T) {
	id := hanzoID(t)
	t.Setenv("IAM_KEY_MINT_ALLOWED_APPS", "hanzo-console")
	id.signup(t, "bob@acme.test")
	app := cloudOver(t, id)
	if code, body := bearerCall(t, app, http.MethodPost, "/v1/account/orgs", id.signin(t, "bob@acme.test"), "", `{"name":"Side Project"}`); code != http.StatusOK {
		t.Fatalf("founding side-project: %d %s", code, body)
	}
	manages(t, app, id.signin(t, "bob@acme.test"), "side-project")
}

// A member who administers nothing manages the keys they hold, and a colleague in
// the same org sees none of them and cannot act on them.
func TestKeys_RealIAM_AMemberManagesTheKeysTheyHold(t *testing.T) {
	id := hanzoID(t)
	t.Setenv("IAM_KEY_MINT_ALLOWED_APPS", "hanzo-console")
	id.signup(t, "bob@acme.test")
	id.signup(t, "carol@acme.test")
	if u := userByEmail(t, id, "bob@acme.test"); u.Owner != "acme" || u.IsAdmin {
		t.Fatalf("bob is %s admin=%v, want a plain member of acme", u.Owner, u.IsAdmin)
	}
	app := cloudOver(t, id)
	held := manages(t, app, id.signin(t, "bob@acme.test"), "")

	carol := id.signin(t, "carol@acme.test")
	if keys := keysOf(t)(bearerCall(t, app, http.MethodGet, "/v1/account/keys", carol, "", "")); len(keys) != 0 {
		t.Fatalf("carol was shown bob's keys: %+v", keys)
	}
	if code, body := bearerCall(t, app, http.MethodDelete, "/v1/account/keys/"+held, carol, "", ""); code != http.StatusNotFound {
		t.Fatalf("carol revoking bob's key: %d %s, want 404", code, body)
	}
}

// The client presenting only its own credential, as it does for a caller who
// arrived with an API key, reads no org but the one it serves: IAM refuses it, so
// one client secret is never a reader of another tenant's keys.
func TestKeys_RealIAM_TheMintClientAloneReadsNoOtherOrg(t *testing.T) {
	id := hanzoID(t)
	t.Setenv("IAM_KEY_MINT_ALLOWED_APPS", "hanzo-console")
	id.signup(t, "bob@acme.test")
	app := mountApp(t, id.url, "hanzo-console", consoleSecret)
	bob := map[string]string{"X-User-Id": "bob", "X-User-Name": "bob", "X-User-Owner": "acme", "X-Org-Id": "acme"}
	code, body := callH(t, app, http.MethodGet, "/v1/account/keys", bob, "")
	if code == http.StatusOK || !strings.Contains(string(body), "iam denied (403)") {
		t.Fatalf("the mint client read acme's keys on its own credential: %d %s", code, body)
	}
}

// manages drives the key surface as token, acting in org ("" is the token's own):
// a secret and a publishable key created and listed, the secret one renamed, the
// publishable one revoked and still listed. It returns the secret key's id.
func manages(t *testing.T, app *zip.App, token, org string) string {
	t.Helper()
	call := func(method, path, body string) (int, []byte) {
		return bearerCall(t, app, method, path, token, org, body)
	}
	keysOf(t)(call(http.MethodGet, "/v1/account/keys", ""))
	sec := created(t)(call(http.MethodPost, "/v1/account/keys", `{"name":"server","type":"secret"}`))
	pub := created(t)(call(http.MethodPost, "/v1/account/keys", `{"name":"web","type":"publishable"}`))
	if !strings.HasPrefix(sec.Key, "sk-") || !strings.HasPrefix(pub.Key, "pk-") {
		t.Fatalf("created %q and %q, want an sk- and a pk-", sec.Key, pub.Key)
	}
	keys := keysOf(t)(call(http.MethodGet, "/v1/account/keys", ""))
	for _, k := range []apiKey{sec, pub} {
		if _, ok := byID(keys, k.ID); !ok {
			t.Fatalf("key %s is not listed: %+v", k.ID, keys)
		}
	}

	code, body := call(http.MethodPatch, "/v1/account/keys/"+sec.ID, `{"name":"renamed"}`)
	var edited apiKey
	if code != http.StatusOK {
		t.Fatalf("rename: %d %s", code, body)
	}
	mustJSON(t, body, &edited)
	if edited.Name != "renamed" || edited.Status != statusActive {
		t.Fatalf("renamed key = %+v", edited)
	}
	if code, body := call(http.MethodDelete, "/v1/account/keys/"+pub.ID, ""); code != http.StatusOK {
		t.Fatalf("revoke: %d %s", code, body)
	}
	keys = keysOf(t)(call(http.MethodGet, "/v1/account/keys", ""))
	if k, ok := byID(keys, pub.ID); !ok || k.Status != statusRevoked {
		t.Fatalf("revoked key listed as %+v (listed %v)", k, ok)
	}
	if k, _ := byID(keys, sec.ID); k.Name != "renamed" || k.Status != statusActive {
		t.Fatalf("renamed key listed as %+v", k)
	}
	return sec.ID
}
