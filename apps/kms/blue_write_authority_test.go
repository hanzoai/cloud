package kms_test

// The mutating face of the secret plane requires admin authority over the org; the
// reading face admits a member. These are the probes that hold that line.
//
// Everything here runs through the REAL identity boundary — a signed token, the
// JWKS the harness serves, SanitizeIdentity deriving the principal — so what is
// asserted is the authority a credential actually carries in the fleet, not a header
// a test wrote. The three shapes below are the three that exist:
//
//	member         a person in the org, no admin bit          reads
//	org admin      a person who administers that org          reads + writes
//	machine        client_credentials: no membership at all   reads
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

	"github.com/zap-proto/zip"
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
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// A member and a machine READ but do not WRITE; an org admin does both.
//
// The read in each case is the control. A refusal only means something beside a
// success from the same credential at the same coordinate: without it, a fixture
// that never authenticated at all would present as a working gate.
func TestBlueWriteRequiresOrgAdmin(t *testing.T) {
	app, key, path := isoWorld(t)

	body, _ := json.Marshal(map[string]string{
		"name": "PLANTED", "value": "written-by-a-caller-under-test", "env": "default",
	})

	for _, tc := range []struct {
		name     string
		tok      isoTok
		mayWrite bool
	}{
		{"member of the org", isoTok{owner: paasOrgA}, false},
		{"machine (client_credentials, as the login broker mints)", isoTok{owner: paasOrgA, machine: true, azp: "hanzo-platform"}, false},
		{"admin of the org", isoTok{owner: paasOrgA, isAdmin: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tok := tc.tok.mint(t, key)

			// CONTROL: this credential authenticates and reads its own secret.
			if st, b := mutate(t, app, "GET", path, tok, ""); st != http.StatusOK {
				t.Fatalf("control read = %d %s, want 200 — a credential that cannot read "+
					"proves nothing by failing to write", st, b)
			}

			wSt, wBody := mutate(t, app, "POST", "/v1/kms/secrets", tok, string(body))
			dSt, dBody := mutate(t, app, "DELETE", path, tok, "")
			t.Logf("%-52s read=200 write=%d delete=%d", tc.name, wSt, dSt)

			if tc.mayWrite {
				if wSt != http.StatusOK {
					t.Errorf("org admin write = %d %s, want 200", wSt, wBody)
				}
				if dSt != http.StatusOK {
					t.Errorf("org admin delete = %d %s, want 200", dSt, dBody)
				}
				return
			}
			if wSt != http.StatusForbidden {
				t.Errorf("write = %d %s, want 403 — a caller without admin authority over the "+
					"org must not replace one of its secrets", wSt, wBody)
			}
			if dSt != http.StatusForbidden {
				t.Errorf("delete = %d %s, want 403 — destroying a secret is an administrative "+
					"act, and availability is part of what this plane protects", dSt, dBody)
			}
		})
	}
}

// A refused write LANDS NOWHERE. A 403 that still committed the record would be the
// worst of both answers, so the refusal is proven by reading the coordinate back
// rather than by trusting the status.
func TestBlueRefusedWriteLandsNowhere(t *testing.T) {
	app, key, _ := isoWorld(t)
	member := isoTok{owner: paasOrgA}.mint(t, key)

	body, _ := json.Marshal(map[string]string{
		"name": "NEVER_WRITTEN", "value": "must-not-exist", "env": "default", "path": "blue",
	})
	if st, b := mutate(t, app, "POST", "/v1/kms/secrets", member, string(body)); st != http.StatusForbidden {
		t.Fatalf("member write = %d %s, want 403", st, b)
	}

	// The org's OWN admin — the credential that would see the record if one existed.
	admin := isoTok{owner: paasOrgA, isAdmin: true}.mint(t, key)
	st, b := mutate(t, app, "GET", "/v1/kms/secrets/blue/NEVER_WRITTEN?env=default", admin, "")
	if st != http.StatusNotFound {
		t.Fatalf("refused write left a record: GET = %d %s, want 404", st, b)
	}
}
