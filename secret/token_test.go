package secret

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestReaderAdmissionRejectsMachineAndForeignIssuer(t *testing.T) {
	for _, tc := range []struct {
		role, issuer string
		ok           bool
	}{{"reader", "https://hanzo.id", true}, {"machine", "https://hanzo.id", false}, {"repository", "https://hanzo.id", false}, {"reader", "https://foreign.test", false},
		// An issuer move: either spelling of the platform's issuer, and never an organization's own (one path segment under it).
		{"reader", "https://hanzo.ai", true}, {"machine", "https://hanzo.ai", false}, {"reader", "https://hanzo.ai/acme", false}} {
		t.Run(tc.role+tc.issuer, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test" {
					t.Error("wrong credential")
				}
				json.NewEncoder(w).Encode(map[string]any{"authority": "hanzo-iam", "issuer": tc.issuer, "credentialRole": tc.role})
			}))
			defer srv.Close()
			err := verifyForgeReader(t.Context(), "test", srv.URL)
			if (err == nil) != tc.ok {
				t.Fatalf("admission error: %v", err)
			}
		})
	}
}
func TestWorkloadTokenFileNeverContainsAssertionAndRefusesShortExpiry(t *testing.T) {
	for _, ttl := range []int{3600, 60} {
		t.Run(strconv.Itoa(ttl), func(t *testing.T) {
			stand(t, func(w http.ResponseWriter, r *http.Request) {
				r.ParseForm()
				if r.Form.Get("assertion") != "assertion.jwt" {
					t.Fatal("wrong workload assertion")
				}
				json.NewEncoder(w).Encode(map[string]any{"access_token": "eyNative.e30.signature", "expires_in": ttl})
			}, func(http.ResponseWriter, *http.Request) { t.Fatal("token exchange must not use KMS") })
			path := t.TempDir() + "/token"
			err := Run(t.Context(), []string{"token", "--out", path})
			if ttl < 1800 {
				if err == nil {
					t.Fatal("short expiry admitted")
				}
				if _, e := os.Stat(path); !os.IsNotExist(e) {
					t.Fatal("refused token reached a file")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, e := os.ReadFile(path)
			if e != nil || string(b) != "eyNative.e30.signature" || strings.Contains(string(b), "assertion") {
				t.Fatal("wrong token file")
			}
			st, _ := os.Stat(path)
			if st.Mode().Perm() != 0400 {
				t.Fatal("token file must be owner read only")
			}
		})
	}
}
