package secret

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stand puts an IAM double and a KMS double behind the environment this package
// reads, with a ServiceAccount token projected at a real path.
func stand(t *testing.T, iam, kms http.HandlerFunc) {
	t.Helper()
	i := httptest.NewServer(iam)
	t.Cleanup(i.Close)
	k := httptest.NewServer(kms)
	t.Cleanup(k.Close)

	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("assertion.jwt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(tokenVar, file)
	t.Setenv(iamVar, i.URL)
	t.Setenv(kmsVar, k.URL)
	t.Setenv(envVar, "prod")
	t.Setenv(devVar, "")
}

// issues is an IAM double that admits the assertion and hands back token.
func issues(t *testing.T, token string, count *int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		*count++
		if r.URL.Path != tokenRoute {
			t.Errorf("iam path = %q, want %q", r.URL.Path, tokenRoute)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.PostForm.Get("grant_type"); got != bearerGrant {
			t.Errorf("grant_type = %q, want %q", got, bearerGrant)
		}
		// The assertion is the projected token, trailing newline and all removed.
		if got := r.PostForm.Get("assertion"); got != "assertion.jwt" {
			t.Errorf("assertion = %q, want the projected token", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "expires_in": 3600})
	}
}

// TestBootReadsEachKey is the ordinary path: one login, one read per key, the
// values in memory and nowhere else.
func TestBootReadsEachKey(t *testing.T) {
	logins, reads := 0, 0
	stand(t, issues(t, "bear", &logins), func(w http.ResponseWriter, r *http.Request) {
		reads++
		if got := r.Header.Get("Authorization"); got != "Bearer bear" {
			t.Errorf("authorization = %q, want the minted bearer", got)
		}
		if got := r.URL.Query().Get("env"); got != "prod" {
			t.Errorf("env = %q, want prod", got)
		}
		prefix := "/v1/kms/secrets/cloud/prod/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			t.Fatalf("kms path = %q, want it under %q", r.URL.Path, prefix)
		}
		key := strings.TrimPrefix(r.URL.Path, prefix)
		_ = json.NewEncoder(w).Encode(map[string]string{"env": "prod", "name": key, "value": "value-of-" + key})
	})

	got, err := Boot(context.Background(), "cloud/prod", "DB_URL", "SIGNING_KEY")
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	for _, key := range []string{"DB_URL", "SIGNING_KEY"} {
		if got[key] != "value-of-"+key {
			t.Errorf("%s = %q, want %q", key, got[key], "value-of-"+key)
		}
	}
	if logins != 1 {
		t.Errorf("logins = %d, want 1 — one login per Boot", logins)
	}
	if reads != 2 {
		t.Errorf("reads = %d, want 2", reads)
	}
}

// TestAMissingKeyFailsBootByName: a map short one entry is a service that
// starts and then fails at the first request that needed it, so a 404 fails the
// whole Boot and says which key was not there.
func TestAMissingKeyFailsBootByName(t *testing.T) {
	logins := 0
	stand(t, issues(t, "bear", &logins), func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/SIGNING_KEY") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"env": "prod", "name": "DB_URL", "value": "v"})
	})

	got, err := Boot(context.Background(), "cloud/prod", "DB_URL", "SIGNING_KEY")
	if err == nil {
		t.Fatalf("Boot succeeded with a missing key: %v", got)
	}
	if !strings.Contains(err.Error(), "SIGNING_KEY") {
		t.Errorf("error = %q, want it to name SIGNING_KEY", err)
	}
	if got != nil {
		t.Errorf("Boot returned %v alongside the error; it must return nothing", got)
	}
}

// TestAnExpiredBearerIsWorthOneMoreLogin, and no more. A second 401 is a real
// refusal — retrying it is how a boot loop becomes a login flood against IAM.
func TestAnExpiredBearerIsWorthOneMoreLogin(t *testing.T) {
	logins, reads := 0, 0
	stand(t, issues(t, "bear", &logins), func(w http.ResponseWriter, _ *http.Request) {
		reads++
		w.WriteHeader(http.StatusUnauthorized)
	})

	if _, err := Boot(context.Background(), "cloud/prod", "DB_URL"); err == nil {
		t.Fatal("Boot succeeded against a KMS that refuses the bearer")
	}
	if logins != 2 {
		t.Errorf("logins = %d, want 2 — the first, and one after the 401", logins)
	}
	if reads != 2 {
		t.Errorf("reads = %d, want 2 — the read, and one retry", reads)
	}
}

// TestTheEnvironmentIsTheDevelopmentPathOnly. With HANZO_DEV=1 and no token to
// project, Boot reads the environment; that is the only path by which a secret
// reaches the process from the environment.
func TestTheEnvironmentIsTheDevelopmentPathOnly(t *testing.T) {
	t.Setenv(tokenVar, filepath.Join(t.TempDir(), "absent"))
	t.Setenv(devVar, "1")
	t.Setenv("DB_URL", "postgres://localhost")

	got, err := Boot(context.Background(), "cloud/dev", "DB_URL")
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if got["DB_URL"] != "postgres://localhost" {
		t.Errorf("DB_URL = %q, want the environment's value", got["DB_URL"])
	}

	// The same all-or-nothing contract: a laptop fails where the cluster does.
	if _, err := Boot(context.Background(), "cloud/dev", "DB_URL", "SIGNING_KEY"); err == nil {
		t.Fatal("Boot succeeded with SIGNING_KEY absent from the environment")
	} else if !strings.Contains(err.Error(), "SIGNING_KEY") {
		t.Errorf("error = %q, want it to name SIGNING_KEY", err)
	}
}

// TestNoTokenIsAnErrorInProduction: without HANZO_DEV the absent token is a
// failure, never a quiet fall back to the environment.
func TestNoTokenIsAnErrorInProduction(t *testing.T) {
	logins := 0
	stand(t, issues(t, "bear", &logins), func(http.ResponseWriter, *http.Request) {
		t.Error("kms was read without a service account token")
	})
	absent := filepath.Join(t.TempDir(), "absent")
	t.Setenv(tokenVar, absent)
	t.Setenv("DB_URL", "the environment must not answer this")

	got, err := Boot(context.Background(), "cloud/prod", "DB_URL")
	if err == nil {
		t.Fatalf("Boot succeeded with no service account token: %v", got)
	}
	if !strings.Contains(err.Error(), absent) {
		t.Errorf("error = %q, want it to name the token path %q", err, absent)
	}
	if logins != 0 {
		t.Errorf("logins = %d, want 0 — there was nothing to authenticate with", logins)
	}
}

// TestAKeyIsANameNotAPlace. The subpath belongs in path, and a key that cannot
// hold a separator is what makes `cloud secret fetch` unable to write outside
// the directory it was given.
func TestAKeyIsANameNotAPlace(t *testing.T) {
	for _, key := range []string{"", ".", "..", "a/b", `a\b`, " DB_URL"} {
		if _, err := Boot(context.Background(), "cloud/prod", key); err == nil {
			t.Errorf("Boot accepted %q as a key", key)
		}
	}
}

// TestFetchWritesOneFilePerKey — the delivery for a workload that is not Go.
func TestFetchWritesOneFilePerKey(t *testing.T) {
	logins := 0
	stand(t, issues(t, "bear", &logins), func(w http.ResponseWriter, r *http.Request) {
		key := filepath.Base(r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]string{"env": "prod", "name": key, "value": "value-of-" + key})
	})

	dir := filepath.Join(t.TempDir(), "run")
	args := []string{"fetch", "--path", "cloud/prod", "--key", "DB_URL", "--key", "SIGNING_KEY", "--out", dir}
	if err := Run(context.Background(), args); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, key := range []string{"DB_URL", "SIGNING_KEY"} {
		file := filepath.Join(dir, key)
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if string(body) != "value-of-"+key {
			t.Errorf("%s holds %q, want %q", file, body, "value-of-"+key)
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != fs.FileMode(0o400) {
			t.Errorf("%s mode = %04o, want 0400", file, got)
		}
	}

	// A re-run replaces a 0400 file rather than failing to open it for writing.
	if err := Run(context.Background(), args); err != nil {
		t.Fatalf("second Run: %v", err)
	}
}
