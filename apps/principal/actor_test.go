package principal

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// actorOf drives Actor through a real request with the headers a gateway mints,
// so what is measured is what a handler would see.
func actorOf(t *testing.T, h map[string]string) string {
	t.Helper()
	var got string
	app := zip.New(zip.Config{})
	app.Get("/p", func(c *zip.Ctx) error {
		got = Actor(c)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	_ = resp.Body.Close()
	return got
}

// TestActorNamesAPersonOrNobody. The empty answers are the interesting half:
// every one of them is a request with no PERSON behind it, and the honest answer
// there is silence. Naming the credential instead is precisely how a ledger came
// to attribute half its spend to an application.
func TestActorNamesAPersonOrNobody(t *testing.T) {
	cases := []struct {
		name string
		hdr  map[string]string
		want string
	}{
		{"a validated member", map[string]string{"X-Org-Id": "acme", "X-User-Id": "alice"}, "acme/alice"},
		{"nothing at all", nil, ""},
		{
			"an org with no validated user — a client header, not a principal",
			map[string]string{"X-Org-Id": "acme"},
			"",
		},
		{
			"a user with no org — a machine token names no tenant",
			map[string]string{"X-User-Id": "alice"},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := actorOf(t, tc.hdr); got != tc.want {
				t.Fatalf("Actor = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestActorAgreesWithTheFleetsSpelling. metering.IdentityFromGatewayHeaders
// builds "<org>/<sub>" from the same two headers, and an agent run's actor is
// the same string. One spelling is what lets a usage row, a debit and a span
// name one person — so this pins the form, not just non-emptiness.
func TestActorAgreesWithTheFleetsSpelling(t *testing.T) {
	got := actorOf(t, map[string]string{"X-Org-Id": "acme", "X-User-Id": "alice"})
	if want := "acme" + "/" + "alice"; got != want {
		t.Fatalf("Actor = %q, want the fleet's <org>/<sub> form %q", got, want)
	}
}
