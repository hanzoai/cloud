package meet

// A join token is what admits a participant to the media server, so it is the one
// act here that costs anything. Three facts: a mint bills the caller's own org, an
// unfunded caller gets no token at all, and the lobby beside it stays free.
//
// The refusal has to precede the mint rather than merely skip the debit — a token
// already handed out is a seat already taken, whatever the ledger says afterwards.

import (
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/internal/planetest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// billedUse is mountWith plus a ledger: the same real identity boundary and the
// same workspace authority, with a meter that has money behind it.
func billedUse(t *testing.T, l *planetest.Ledger) *zip.App {
	t.Helper()
	iamIssuer(t)
	t.Setenv(keyFileEnv, keyFileWith(t, keyBody("APIkey", "apisecret")))
	st := load()
	st.authority = holds(map[string]string{workspaceA: token.RoleMember})
	sharedKey(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.IdentityMiddleware(&cloud.Config{IAMIssuer: iamtest.Issuer, JWKSURL: jwksURL}))
	app.Use(cloud.Bridge())
	if err := serve(app, cloud.Deps{Metering: l.Client(t), Env: "mainnet"}, st); err != nil {
		t.Fatalf("serve: %v", err)
	}
	return app
}

// A mint bills the caller's own org at the declared fee.
func TestMintBillsTheCaller(t *testing.T) {
	l := planetest.Money(t, 100000)
	app := billedUse(t, l)

	code, tok := ask(t, app, roomIn(workspaceA), "person-42", access(t, ada))
	if code != http.StatusOK {
		t.Fatalf("mint = %d, want 200", code)
	}
	if tok == "" {
		t.Fatal("no token, so nothing was sold")
	}
	if !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("debits = %d, want 1 — an admitted seat must bill", l.Count())
	}
	_, cents, model, _ := l.Charged()
	if cents != defaultFeeCents {
		t.Errorf("debit = %dc, want the declared fee %dc", cents, defaultFeeCents)
	}
	if model != seat {
		t.Errorf("debit unit = %q, want %q", model, seat)
	}
}

// An unfunded caller gets no token. The gate precedes the mint, so the seat is
// never handed out rather than handed out unbilled.
func TestUnfundedCallerGetsNoSeat(t *testing.T) {
	l := planetest.Money(t, 0)
	app := billedUse(t, l)

	code, tok := ask(t, app, roomIn(workspaceA), "person-42", access(t, ada))
	if code == http.StatusOK {
		t.Fatalf("an unfunded caller was admitted with token %q; the gate must refuse first", tok)
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a refused mint, want 0", n)
	}
}
