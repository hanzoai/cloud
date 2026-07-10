package runner

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The /v1 control surface is loopback + reachability-is-auth. These tests pin
// the two browser-bypass defenses localGuard adds on top of the loopback bind.

func TestLocalGuard_RejectsNonLocalHost(t *testing.T) {
	d := newTestDaemon(t)
	mux := http.NewServeMux()
	d.registerV1(mux)
	// A DNS-rebinding attack presents the attacker's Host, not 127.0.0.1.
	req := httptest.NewRequest(http.MethodGet, "http://evil.example.com/v1/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-local host: code=%d want 403", rec.Code)
	}
}

func TestLocalGuard_RejectsCrossOriginPost(t *testing.T) {
	d := newTestDaemon(t)
	mux := http.NewServeMux()
	d.registerV1(mux)
	// Loopback host but a cross-site Origin => CSRF from a page the operator
	// visits. Must be refused AND must not flip state.
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/pause", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin pause: code=%d want 403", rec.Code)
	}
	if d.state.IsPaused() {
		t.Fatal("cross-origin request must not pause the daemon")
	}
}

func TestLocalGuard_AllowsLocalOrigin(t *testing.T) {
	d := newTestDaemon(t)
	mux := http.NewServeMux()
	d.registerV1(mux)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/pause", nil)
	req.Header.Set("Origin", "http://localhost:7777")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local-origin pause: code=%d want 200", rec.Code)
	}
	if !d.state.IsPaused() {
		t.Fatal("local-origin pause should flip state")
	}
}

// The child actions-runner runs untrusted workflow code; the daemon must not
// hand it operator secrets via the environment, but must keep PATH/HOME so
// builds work.

func TestSanitizedEnv_DropsSecretsKeepsPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/x")
	t.Setenv("GITHUB_TOKEN", "ghp_secret")
	t.Setenv("MY_API_KEY", "sk-secret")
	t.Setenv("HANZO_API_KEY", "secret")

	env := sanitizedEnv()
	has := func(prefix string) bool {
		for _, kv := range env {
			if strings.HasPrefix(kv, prefix) {
				return true
			}
		}
		return false
	}
	if !has("PATH=") || !has("HOME=") {
		t.Fatal("sanitizedEnv must keep PATH and HOME")
	}
	if has("GITHUB_TOKEN=") || has("MY_API_KEY=") || has("HANZO_API_KEY=") {
		t.Fatal("sanitizedEnv must drop secret-looking vars")
	}
}

func TestSecretEnvKey(t *testing.T) {
	for _, k := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "DB_PASSWORD", "MY_API_KEY", "HANZO_TOKEN", "KMS_SERVICE_TOKEN", "app_private_key"} {
		if !secretEnvKey(k) {
			t.Errorf("secretEnvKey(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"PATH", "HOME", "LANG", "GOOS", "TERM", "SHELL"} {
		if secretEnvKey(k) {
			t.Errorf("secretEnvKey(%q) = true, want false", k)
		}
	}
}

// Forks carry untrusted contributor code; the default poll must skip them.
func TestAllowForks_DefaultsOff(t *testing.T) {
	var c Config
	if c.AllowForks {
		t.Fatal("AllowForks must default to false (forks skipped)")
	}
}
