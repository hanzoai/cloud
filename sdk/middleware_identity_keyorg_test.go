package cloud

// A KEY ACTS IN THE ORG ITS CALLER SELECTED, OR THE REQUEST IS REFUSED.
//
// A key is filed in one org, and X-Org-Id used to be discarded for a key: the
// request ran, and billed, in the key's org whatever the caller selected
// (hanzo-inc/cloud#429). Now the selection is IAM's to admit (keys/principal
// ?org=): the holder's home or an org they are a member of acts, and pays; any
// other is 403. These drive real keys through SanitizeIdentity against a stub IAM.

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// iamOrgServer answers keys/principal for sk-alice, filed in acme, whose holder
// acme/alice is also a member of team. With legacy set it predates ?org= and
// ignores it, as an IAM older than the parameter does.
func iamOrgServer(t *testing.T, legacy bool, asked *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("accessKey") != "sk-alice" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","msg":"the entity does not exist","code":"key_unknown"}`))
			return
		}
		org := r.URL.Query().Get("org")
		if org != "" {
			asked.Add(1)
		}
		if legacy {
			org = ""
		}
		switch org {
		case "", "acme":
			_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"acme","name":"alice","org":"acme","key":"alice-key"}}`))
		case "team":
			_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"acme","name":"alice","org":"team","billing_account":"org:team","key":"alice-key","keyOrg":"acme"}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","msg":"the entity does not exist","code":"key_org_not_open"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func keyOrgApp(t *testing.T, legacy bool, asked *atomic.Int32) *zip.App {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	v := newIdentityValidator(testIssuer, jwksServer(t, &key.PublicKey).URL, 0)
	iam := iamOrgServer(t, legacy, asked)
	v.keys = &iamKeys{base: iam.URL, auth: "Basic test", http: iam.Client(),
		cache: NewCache[keyID, keyAnswer](time.Minute), refused: NewCache[keyID, keyAnswer](time.Minute)}
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v))
	app.Raw(http.MethodGet, "/v1/billing/balance", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{
			"org": c.Header(authz.HeaderOrg), "key": c.Header(HeaderKey), "billing": c.Header("X-Billing-Account-Id"),
		})
	})
	return app
}

type keyOrgSeen struct{ Org, Key, Billing string }

func sendSelecting(t *testing.T, app *zip.App, org string) (int, keyOrgSeen, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/balance", nil)
	req.Header.Set("Authorization", "Bearer sk-alice")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("GET selecting %q: %v", org, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var seen keyOrgSeen
	if resp.StatusCode == http.StatusOK {
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("selecting %q: %s", org, b)
		}
		seen = keyOrgSeen{m["org"], m["key"], m["billing"]}
	}
	return resp.StatusCode, seen, string(b)
}

func TestAKeyActsAndPaysInTheOrgItsCallerSelected(t *testing.T) {
	var asked atomic.Int32
	app := keyOrgApp(t, false, &asked)

	// No selection, or the key's own org: the key acts where it is filed, and
	// IAM is never asked about an org.
	for _, sel := range []string{"", "acme"} {
		code, seen, body := sendSelecting(t, app, sel)
		if code != http.StatusOK || seen.Org != "acme" || seen.Key != "acme/alice-key" {
			t.Fatalf("selecting %q: %d %+v %s, want acme with key acme/alice-key", sel, code, seen, body)
		}
	}
	if n := asked.Load(); n != 0 {
		t.Fatalf("IAM was asked about an org %d times for selections it did not need to admit", n)
	}

	// An org the holder is a member of: the request acts and pays there, and the
	// key's own policy is still addressed where the key is filed.
	code, seen, body := sendSelecting(t, app, "team")
	if code != http.StatusOK || seen.Org != "team" || seen.Billing != "org:team" || seen.Key != "acme/alice-key" {
		t.Fatalf("selecting team: %d %+v %s, want org team, billing org:team, key acme/alice-key", code, seen, body)
	}

	// An org the holder does not act in is refused, never quietly billed to acme.
	code, _, body = sendSelecting(t, app, "other")
	if code != http.StatusForbidden || !strings.Contains(body, "key_org_not_open") {
		t.Fatalf("selecting other: %d %s, want 403 key_org_not_open", code, body)
	}

	// A selection is asked of IAM every time: a membership removed ends it on the
	// next request.
	before := asked.Load()
	sendSelecting(t, app, "team")
	if asked.Load() == before {
		t.Fatalf("a selected org was answered from a cache; a removed membership would outlive it")
	}
}

// An IAM that predates ?org= answers the key's own org. The request keeps that
// answer — what it got before — instead of being refused for IAM's version, and
// is never reported as acting in the org it selected.
func TestAnOlderIAMLeavesTheKeyInItsOwnOrg(t *testing.T) {
	var asked atomic.Int32
	app := keyOrgApp(t, true, &asked)
	code, seen, body := sendSelecting(t, app, "team")
	if code != http.StatusOK || seen.Org != "acme" || seen.Key != "acme/alice-key" {
		t.Fatalf("selecting team against an older IAM: %d %+v %s, want the key's own org acme", code, seen, body)
	}
}
