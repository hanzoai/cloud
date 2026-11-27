package social

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func newSocialKMS(t *testing.T) *kms.Client {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	kc, err := kms.New(kms.Config{
		DataDir:      t.TempDir(),
		MasterKeyB64: base64.StdEncoding.EncodeToString(key),
	}, luxlog.New("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kc.Close() })
	return kc
}

func mountSocialWith(t *testing.T, kc secret.Store) *zip.App {
	t.Helper()
	_ = Shutdown()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{KMS: kc, MasterKey: testMaster}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// The secret is sealed and never returned. Leaving it out keeps it. An empty
// string clears it. An empty client id clears the app. A network left out of
// the body is unchanged.
//
// It runs on SQLite alone: the secret is sealed in the KMS store, which is not ported
// to hanzo-sql, and with the DSN set that store does not open.
func TestCredentialsStayMaskedAndPartial(t *testing.T) {
	kc := newSocialKMS(t)
	app := mountSocialWith(t, kc)
	const org = "acme"
	const secret = "sekrit-value"

	code, body := req(t, app, http.MethodGet, "/v1/social/credentials", org, nil)
	if code != http.StatusOK || strings.Contains(string(body), secret) {
		t.Fatalf("list empty: %d %s", code, body)
	}
	var empty orgApps
	if err := json.Unmarshal(body, &empty); err != nil || len(empty.Apps) != 0 || len(empty.Data) != 0 {
		t.Fatalf("empty = %+v %s", empty, body)
	}

	code, body = req(t, app, http.MethodPut, "/v1/social/credentials", org, map[string]any{
		"apps": []map[string]any{{"provider": "x", "clientId": "cid-1", "clientSecret": secret}},
	})
	if code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("the secret came back: %s", body)
	}
	got := decodeApps(t, body)
	if len(got.Apps) != 1 || got.Apps[0].Provider != "x" || got.Apps[0].ClientID != "cid-1" || !got.Apps[0].SecretSet {
		t.Fatalf("saved = %+v", got)
	}
	if len(got.Data) != 1 || got.Data[0].ClientID != "cid-1" {
		t.Fatalf("data = %+v", got.Data)
	}
	sealed, err := kc.GetSecret(t.Context(), appRef(appPath(org, "x"), "client_secret"))
	if err != nil || string(sealed) != secret {
		t.Fatalf("sealed = %q err=%v", sealed, err)
	}

	code, body = req(t, app, http.MethodPut, "/v1/social/credentials", org, map[string]any{
		"apps": []map[string]any{{"provider": "x", "clientId": "cid-2"}},
	})
	got = decodeApps(t, body)
	if code != http.StatusOK || got.Apps[0].ClientID != "cid-2" || !got.Apps[0].SecretSet {
		t.Fatalf("omit secret: %d %+v %s", code, got, body)
	}

	code, body = req(t, app, http.MethodPut, "/v1/social/credentials", org, map[string]any{
		"apps": []map[string]any{{"provider": "linkedin", "clientId": "li", "clientSecret": "li-secret"}},
	})
	got = decodeApps(t, body)
	if code != http.StatusOK || len(got.Apps) != 2 {
		t.Fatalf("second network: %d %+v", code, got)
	}
	if strings.Contains(string(body), "li-secret") || strings.Contains(string(body), secret) {
		t.Fatalf("a secret came back: %s", body)
	}

	code, body = req(t, app, http.MethodPut, "/v1/social/credentials", org, map[string]any{
		"apps": []map[string]any{{"provider": "x", "clientId": "cid-2", "clientSecret": ""}},
	})
	got = decodeApps(t, body)
	if code != http.StatusOK || !secretCleared(got, "x") {
		t.Fatalf("cleared secret: %d %+v", code, got)
	}

	code, body = req(t, app, http.MethodPut, "/v1/social/credentials", org, map[string]any{
		"apps": []map[string]any{{"provider": "x", "clientId": ""}},
	})
	got = decodeApps(t, body)
	if code != http.StatusOK || hasProvider(got, "x") || !hasProvider(got, "linkedin") {
		t.Fatalf("cleared app: %d %+v", code, got)
	}

	code, body = req(t, app, http.MethodPut, "/v1/social/credentials", org, map[string]any{
		"apps": []map[string]any{{"provider": "myspace", "clientId": "no"}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("unknown provider: %d %s", code, body)
	}
}

// The providers read answers "can I connect this" for the CALLER, so an app the
// org sealed on the Keys page counts as configured even with no deployment env.
// The secret stays sealed; an org without the app still sees the env names.
func TestProvidersReadOrgSealedApps(t *testing.T) {
	for _, k := range []string{
		"FACEBOOK_CLIENT_ID", "FACEBOOK_CLIENT_SECRET", "FACEBOOK_APP_ID", "FACEBOOK_APP_SECRET",
		"INSTAGRAM_CLIENT_ID", "INSTAGRAM_CLIENT_SECRET",
	} {
		t.Setenv(k, "")
	}
	const org = "acme"
	const secretValue = "meta-secret-never-returned"
	app := mountSocialWith(t, newSocialKMS(t))

	code, body := req(t, app, http.MethodPut, "/v1/social/credentials", org, map[string]any{
		"apps": []map[string]any{{"provider": "instagram", "clientId": "ig-id", "clientSecret": secretValue}},
	})
	if code != http.StatusOK {
		t.Fatalf("seal app: %d %s", code, body)
	}

	code, body = req(t, app, http.MethodGet, "/v1/social/providers", org, nil)
	if code != http.StatusOK {
		t.Fatalf("providers: %d %s", code, body)
	}
	if strings.Contains(string(body), secretValue) || strings.Contains(string(body), "ig-id") {
		t.Fatalf("a credential value came back: %s", body)
	}
	var out socialProviders
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	byName := map[string]socialProvider{}
	for _, c := range out.Data {
		byName[c.Provider] = c
	}
	if !byName["instagram"].CredentialsConfigured || len(byName["instagram"].MissingCredentials) != 0 {
		t.Fatalf("sealed instagram = %+v", byName["instagram"])
	}
	if byName["facebook"].CredentialsConfigured || len(byName["facebook"].MissingCredentials) != 4 {
		t.Fatalf("unsealed facebook = %+v", byName["facebook"])
	}

	code, body = req(t, app, http.MethodGet, "/v1/social/providers", "other", nil)
	if code != http.StatusOK {
		t.Fatalf("other org providers: %d %s", code, body)
	}
	out = socialProviders{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	for _, c := range out.Data {
		if c.Provider == "instagram" && (c.CredentialsConfigured || len(c.MissingCredentials) != 6) {
			t.Fatalf("another org inherited the app: %+v", c)
		}
	}
}

func decodeApps(t *testing.T, body []byte) orgApps {
	t.Helper()
	var got orgApps
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func hasProvider(got orgApps, provider string) bool {
	for _, a := range got.Apps {
		if a.Provider == provider {
			return true
		}
	}
	return false
}

func secretCleared(got orgApps, provider string) bool {
	for _, a := range got.Apps {
		if a.Provider == provider {
			return !a.SecretSet && a.ClientID != ""
		}
	}
	return false
}
