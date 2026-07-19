package metering_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/metering"
)

// SEV1 regression: a HANGING/hot-looping commerce authorize must NEVER hang the
// completion path — the cap fails OPEN fast. A funded caller whose /authorize blocks
// for 10s must still get an ALLOW verdict in ~capAuthorizeTimeout (well under the hang),
// never a wait. This is the safety the missing timeout lacked.
func TestAuthorizeVerdict_CapHang_FailsOpenFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/spend-alerts/authorize") {
			time.Sleep(10 * time.Second) // simulate the legacy-org hot-loop / stuck handler
			return
		}
		_, _ = io.WriteString(w, `{"available":100000}`) // balance: funded
	}))
	defer srv.Close()

	c, _ := metering.New(metering.Config{BaseURL: srv.URL, Token: "t", Org: "hanzo"})

	start := time.Now()
	v, err := c.AuthorizeVerdict(context.Background(), metering.AuthInput{User: "hanzo", Org: "hanzo", AmountCents: 1})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a hanging cap check must FAIL-OPEN (nil err), got %v", err)
	}
	if !v.Allow {
		t.Fatalf("a hanging cap check must ALLOW (fail-open), got %+v", v)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("cap check took %s against a 10s hang — the completion would HANG; must return in ~1.5s", elapsed)
	}
}
