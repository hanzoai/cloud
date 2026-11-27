package kms_test

// The mutating face of the secret plane opens only for a holder a WRITE grant
// covers (or SuperAdmin, audited). Membership reads names; org admin confers
// nothing over values. These are the probes that hold that line.
//
// Everything here runs through the REAL identity boundary — a signed token, the
// JWKS the harness serves, SanitizeIdentity deriving the principal and its
// holder — so what is asserted is the authority a credential actually carries in
// the fleet, not a header a test wrote. The shapes below are the ones that exist:
//
//	member         a person in the org, no admin bit          reads a granted key
//	org admin      a person who administers that org          reads a granted key
//	machine        client_credentials, as the broker mints    reads a granted key
//	writer         a holder a write grant names               reads + writes + deletes
//
// A machine is the shape that matters most, because a machine credential is the one
// that gets distributed: it is copied into every namespace whose workload needs a
// secret delivered. Delivery is a READ, so a machine keeps everything its job needs
// and holds nothing that could overwrite or destroy a record.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/authz"
)

// mutate performs a write or a delete as the bearer of token and reports the status.
func mutate(t *testing.T, app *zip.App, method, path, token, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// Nobody without a WRITE grant writes — member, machine or org admin — and a
// holder with one writes and deletes exactly what it names.
//
// The read in each refused case is the control. A refusal only means something
// beside a success from the same credential at the same coordinate: without it, a
// fixture that never authenticated at all would present as a working gate.
func TestBlueWriteRequiresAGrant(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, key, path := isoWorld(t)

		body, _ := json.Marshal(map[string]string{
			"name": "DB_PASSWORD", "path": "platform/api", "value": "written-by-a-caller-under-test", "env": "default",
		})

		for _, tc := range []struct {
			name string
			tok  isoTok
		}{
			{"member of the org", isoTok{owner: paasOrgA}},
			// typ and azp are what IAM signs a client_credentials token with; azp is the
			// holder a grant names.
			{"machine (client_credentials, as the login broker mints)", isoTok{owner: paasOrgA, machine: true, azp: paasOrgA + "-platform-kms", typ: authz.Program}},
			{"admin of the org", isoTok{owner: paasOrgA, isAdmin: true}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tok := tc.tok.mint(t, key)

				// CONTROL: this credential authenticates and reads its own granted secret.
				if st, b := mutate(t, app, "GET", path, tok, ""); st != http.StatusOK {
					t.Fatalf("control read = %d %s, want 200 — a credential that cannot read "+
						"proves nothing by failing to write", st, b)
				}

				wSt, wBody := mutate(t, app, "POST", "/v1/kms/secrets", tok, string(body))
				dSt, dBody := mutate(t, app, "DELETE", path, tok, "")
				t.Logf("%-52s read=200 write=%d delete=%d", tc.name, wSt, dSt)
				if wSt != http.StatusForbidden || !strings.Contains(wBody, "may not write /platform/api/DB_PASSWORD@default") {
					t.Errorf("write = %d %s, want 403 naming the ref — no write grant names this "+
						"holder, and org admin is not one", wSt, wBody)
				}
				if dSt != http.StatusForbidden {
					t.Errorf("delete = %d %s, want 403 — destroying a secret is a write, and "+
						"availability is part of what this plane protects", dSt, dBody)
				}
				if st, b := mutate(t, app, "GET", path, tok, ""); st != http.StatusOK || !strings.Contains(b, paasValueA) {
					t.Errorf("after the refused write and delete the record reads %d %s, want maxpower's own value", st, b)
				}
			})
		}

		// The positive control: acme's grant names a write at the same coordinate in
		// ITS org, and it writes, reads back and deletes there — and only there.
		writer := isoTok{owner: paasOrgB}.mint(t, key)
		if st, b := mutate(t, app, "POST", "/v1/kms/secrets", writer, string(body)); st != http.StatusOK {
			t.Fatalf("granted write = %d %s, want 200", st, b)
		}
		if st, b := mutate(t, app, "GET", path, writer, ""); st != http.StatusOK || !strings.Contains(b, "written-by-a-caller-under-test") {
			t.Fatalf("granted read-back = %d %s", st, b)
		}
		if st, b := mutate(t, app, "DELETE", path, writer, ""); st != http.StatusOK {
			t.Fatalf("granted delete = %d %s, want 200", st, b)
		}
		if st, b := mutate(t, app, "GET", path, isoTok{owner: paasOrgA}.mint(t, key), ""); st != http.StatusOK || !strings.Contains(b, paasValueA) {
			t.Fatalf("acme's granted write and delete reached maxpower's record: %d %s", st, b)
		}
	})
}

// A refused write LANDS NOWHERE. A 403 that still committed the record would be the
// worst of both answers, so the refusal is proven by reading the coordinate back
// rather than by trusting the status.
func TestBlueRefusedWriteLandsNowhere(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, key, _ := isoWorld(t)
		member := isoTok{owner: paasOrgA}.mint(t, key)

		body, _ := json.Marshal(map[string]string{
			"name": "NEVER_WRITTEN", "value": "must-not-exist", "env": "default", "path": "blue",
		})
		if st, b := mutate(t, app, "POST", "/v1/kms/secrets", member, string(body)); st != http.StatusForbidden {
			t.Fatalf("member write = %d %s, want 403", st, b)
		}

		// SuperAdmin acting in the org — the one credential that would see the record
		// if one existed, whatever its path.
		req := httptest.NewRequest("GET", "/v1/kms/secrets/blue/NEVER_WRITTEN?env=default", nil)
		req.Header.Set("Authorization", "Bearer "+isoTok{owner: "admin", isAdmin: true}.mint(t, key))
		req.Header.Set("X-Org-Id", paasOrgA)
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		st, b := resp.StatusCode, string(raw)
		if st != http.StatusNotFound {
			t.Fatalf("refused write left a record: GET = %d %s, want 404", st, b)
		}
	})
}
