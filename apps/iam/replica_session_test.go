// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/iam/pkg/model"
	iamstore "github.com/hanzoai/iam/pkg/store"
	iamserver "github.com/hanzoai/iam/server"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"
	"golang.org/x/crypto/bcrypt"

	"github.com/hanzoai/cloud"
)

// A SESSION MINTED ON ONE POD IS ACCEPTED BY THE NEXT — AND THE STORE IS WHY.
//
// This is the property cloud.Config.Validate now checks for instead of pinning the
// deployment to one replica. The rule it replaced said embedded IAM keeps sessions
// in memory, so a second pod would miss one the first minted. Nothing in IAM keeps
// a session in memory. A session is a signed cookie plus a revocation row, and both
// of the facts that verify it — the platform signing cert that keys the MAC, and
// the row that says the id is still live — are read from the orm.DB on every
// request. Two pods over ONE store therefore agree, and two pods over TWO stores
// cannot, whatever either one holds in memory.
//
// So the test is the pair. The first half would pass for the wrong reason if these
// two pods shared anything but the store; the second half is what rules that out,
// by changing ONLY the store and watching the same cookie stop working.
//
// The pods are separate iamserver.NewApp instances rather than two Mount calls
// because Mount publishes its handle through package state (embeddedDB), and two
// grafts in one process would share it — which is the one thing a test about two
// pods must not do. NewApp takes the store as its argument, so what these two share
// is exactly what a real pair of pods shares and nothing else.
func TestASessionMintedOnOnePodIsAcceptedByTheNext(t *testing.T) {
	signingKeys(t)
	shared := identityStore(t)

	podA := iamserver.NewApp(shared)
	podB := iamserver.NewApp(shared)

	cookie := signIn(t, podA)

	// The whole claim, in one call: pod B never saw the sign-in.
	owner, name := account(t, podB, cookie)
	if owner != "hanzo" || name != "alice" {
		t.Fatalf("pod B resolved %q/%q from a session pod A minted, want hanzo/alice — "+
			"a session that does not survive the hop is what pins a deployment to one replica",
			owner, name)
	}

	// AND THE CONTROL. A pod on its OWN store is the per-pod file case — the state
	// every replica lands in when IAM_STORE_BACKEND names no shared backend. It
	// holds its own certs and its own session rows, so the same cookie resolves
	// nobody. Without this half, the half above would also pass if these apps were
	// sharing process state, and the test would be proving nothing.
	podC := iamserver.NewApp(identityStore(t))
	if owner, name := account(t, podC, cookie); owner != "" || name != "" {
		t.Fatalf("a pod on a SEPARATE identity store resolved %q/%q from another pod's "+
			"session cookie, want nobody — then the two stores are not separate and this "+
			"test cannot detect the divergence it exists to detect", owner, name)
	}
}

// The boot check and this test have to be reading the same fact, or the check is
// enforcing a condition nothing demonstrates. IAMStoreShared is what Validate calls
// to decide whether N replicas may run; "sqlite" and empty are the per-pod file the
// control above builds, which is why they are the values that refuse.
func TestTheBootCheckNamesTheStoreThisTestVaried(t *testing.T) {
	c := &cloud.Config{
		Brand: "hanzo", Domain: "api.hanzo.ai", DataDir: t.TempDir(),
		Enable: []string{"iam"}, Replicas: 3,
	}
	c.IAMStore = "" // the per-pod file — podC's case
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() allowed 3 replicas on the per-pod identity store this test just " +
			"showed does not carry a session across pods")
	}
	c.IAMStore = "sql" // a store every replica reaches — podA/podB's case
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil: this test just showed a shared store carries "+
			"the session across pods", err)
	}
}

// identityStore builds one populated identity store and returns the handle. It is a
// store, not a pod: what makes two pods share one is that they are handed the same
// return value.
//
// Seeded with what a session actually needs, and nothing decorative:
//   - an admin-owned signing cert whose key comes from the IAM_SIGNING_KEYS mount,
//     because the PrivateKey column is memory-only (schema.Cert marks it db:"-")
//     and that key is what the session MAC is derived from;
//   - an admin-owned application naming it, because the cert is chosen from the
//     REFERENCED set (upstream picks that way so every replica picks the same one);
//   - the org, and a user with a bcrypt password to sign in as.
func identityStore(t *testing.T) orm.DB {
	t.Helper()
	db, err := iamstore.Open("sqlite", t.TempDir()+"/iam.db", "")
	if err != nil {
		t.Fatalf("open identity store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	cert := orm.New[model.Cert](db)
	cert.Owner, cert.Name = "admin", "cert-hanzo"
	cert.Type, cert.CryptoAlgorithm, cert.BitSize = "x509", "RS256", 2048
	cert.SetId("admin/cert-hanzo")
	if err := cert.CreateCtx(ctx); err != nil {
		t.Fatalf("seed signing cert: %v", err)
	}

	app := orm.New[model.Application](db)
	app.Owner, app.Name, app.ClientId = "admin", "hanzo-console", "hanzo-console"
	app.Organization, app.Cert = "hanzo", "cert-hanzo"
	app.EnablePassword, app.ExpireInHours = true, 1
	app.SetId("admin/hanzo-console")
	if err := app.CreateCtx(ctx); err != nil {
		t.Fatalf("seed application: %v", err)
	}

	org := orm.New[model.Organization](db)
	org.Owner, org.Name, org.DisplayName = "admin", "hanzo", "Hanzo"
	org.SetId("admin/hanzo")
	if err := org.CreateCtx(ctx); err != nil {
		t.Fatalf("seed organization: %v", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := orm.New[model.User](db)
	user.Owner, user.Name, user.Email = "hanzo", "alice", "alice@hanzo.test"
	user.EmailVerified = true
	user.PasswordHash, user.PasswordType = string(hash), "bcrypt"
	user.SetId("hanzo/alice")
	if err := user.CreateCtx(ctx); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return db
}

// signIn drives the portal login and returns the session cookie it set. A login
// that sets no cookie is a failed test, not an empty string to carry forward.
func signIn(t *testing.T, pod *zip.App) string {
	t.Helper()
	form := url.Values{
		"organization": {"hanzo"}, "application": {"hanzo-console"},
		"username": {"alice"}, "password": {password}, "type": {"login"},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/iam/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "hanzo.id"
	resp, err := pod.Test(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, raw := range resp.Header.Values("Set-Cookie") {
		if name, _, ok := strings.Cut(raw, "="); ok && strings.HasSuffix(name, "hanzo_session") {
			kv, _, _ := strings.Cut(raw, ";")
			return kv
		}
	}
	t.Fatalf("login set no session cookie (status %d): %s", resp.StatusCode, body)
	return ""
}

// account asks a pod who the cookie is. Empty owner and name mean nobody, which is
// the honest reading of IAM's envelope: an anonymous caller gets status "error"
// with no data rather than a 401.
func account(t *testing.T, pod *zip.App, cookie string) (owner, name string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/iam/account", nil)
	req.Header.Set("Cookie", cookie)
	req.Host = "hanzo.id"
	resp, err := pod.Test(req)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var env struct {
		Status string `json:"status"`
		Name   string `json:"name"`
		Data   struct {
			Owner string `json:"owner"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode account envelope %q: %v", body, err)
	}
	if env.Status != "ok" {
		return "", ""
	}
	return env.Data.Owner, env.Name
}

// signingKeys projects one RS256 key under the cert's name into the directory IAM
// reads signing material from, and points IAM_SIGNING_KEYS at it for the test
// process.
//
// EVERY pod here reads the SAME directory, deliberately. That is what a deployment
// does — one Secret, projected into every replica — so a control pod that rejected
// the cookie merely because its key differed would be proving something no
// deployment ever does. Holding the key fixed leaves the STORE as the only thing
// that varies, which is the whole subject of this file.
func signingKeys(t *testing.T) {
	t.Helper()
	mountSigningKey(t, "cert-hanzo")
}

const password = "correct-horse-battery-staple"
