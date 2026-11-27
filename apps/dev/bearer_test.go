package dev

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"zap-proto.io/go/transport/quic"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
)

// bearers records the Bearer each lease carried over the plane.
type bearers struct {
	mu  sync.Mutex
	got []string
}

func (b *bearers) observe(in client.LeaseIn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.got = append(b.got, in.Bearer)
}

func (b *bearers) all() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.got...)
}

// jwt is a bearer the identity boundary would have validated: a JWT, not an API key.
func jwt(sub string) string {
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://hanzo.id","sub":"` + sub + `"}`))
	return "eyJhbGciOiJSUzI1NiJ9." + claims + ".c2lnbmF0dXJl"
}

// The cell is leased with the person's own bearer, the one POST /v1/dev/sessions was
// validated on, so the sandbox signs it in as them before Dev starts. Leased without
// it, the cell held no Hanzo credential and every Dev turn in it failed "Not signed in
// to Hanzo" (#907). Resuming a suspended cell carries it too: the parked pod is gone,
// and the new one is signed in again.
//
// MUTATION: lease without Bearer and the first assertion fails.
func TestACellIsLeasedWithThePersonsOwnBearer(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	seen := &bearers{}
	p.LeaseIns = seen.observe
	app := mount(t)
	tok := jwt("u_alice")
	headers := map[string]string{"Authorization": "Bearer " + tok}

	code, b := asWith(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions", "", headers)
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/dev/sessions = %d %s", code, b)
	}
	if got := seen.all(); len(got) != 1 || got[0] != tok {
		t.Fatalf("the create leased its cell with bearers %q; want the request's own", got)
	}
	var opened Session
	if err := json.Unmarshal(b, &opened); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), tok) {
		t.Error("the session answer echoes the bearer")
	}

	if code, b := asWith(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions/"+opened.ID+"/suspend", "", headers); code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, b)
	}
	if code, b := asWith(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions/"+opened.ID+"/resume", "", headers); code != http.StatusOK {
		t.Fatalf("resume = %d %s", code, b)
	}
	if got := seen.all(); len(got) != 2 || got[1] != tok {
		t.Fatalf("the resume leased its cell with bearers %q; want the request's own", got)
	}
}

// A credential that is not a relayable bearer — an API key, or none at all — leases a
// cell that holds no identity, as every cell did before: nothing is relayed that the
// boundary did not validate as the person's own token.
func TestACellLeasedWithoutABearerCarriesNone(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	seen := &bearers{}
	p.LeaseIns = seen.observe
	app := mount(t)
	if code, b := asWith(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions", "", nil); code != http.StatusCreated {
		t.Fatalf("POST /v1/dev/sessions = %d %s", code, b)
	}
	if code, b := asWith(t, app, "u_bob", http.MethodPost, "/v1/dev/sessions", "", map[string]string{"Authorization": "Bearer sk-0123456789abcdef"}); code != http.StatusCreated {
		t.Fatalf("POST /v1/dev/sessions with an API key = %d %s", code, b)
	}
	for _, got := range seen.all() {
		if got != "" {
			t.Fatalf("a lease carried bearer %q from a request that presented no JWT", got)
		}
	}
}

// A wasm session that needs a workspace takes its cell with the bearer its ZAP stream
// was verified on — the same token #946 reads the issuer of — so the Dev server it is
// upgraded into runs its turns signed in as the person (#908).
func TestAnUpgradedSessionsCellIsLeasedWithTheStreamsBearer(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	seen := &bearers{}
	p.LeaseIns = seen.observe
	rt := serving(t, &model{})
	if err := rt.loop.New("hanzo", "sub-1", "w_up"); err != nil {
		t.Fatal(err)
	}
	valid := accepts{"good": {Owner: "hanzo", Subject: "sub-1", Username: "alice", Issuer: "https://hanzo.id"}}
	out := &frames{}
	b := &bridge{id: "w_up", sess: &quic.Session{}, valid: valid, push: out.push}
	if resp, err := b.dispatch(request(1, "subscribeServerConfig", "good")); resp != nil || err != nil {
		t.Fatalf("dispatch = %v %v", resp, err)
	}

	sock, got := cell(t)
	p.Stream = func(_, _, _, _ string) string { return sock }
	if resp, err := b.dispatch(request(2, "terminal.open", "")); resp != nil || err != nil {
		t.Fatalf("dispatch = %v %v", resp, err)
	}
	for deadline := time.Now().Add(5 * time.Second); len(got()) < 1 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	if len(got()) == 0 {
		t.Fatal("the session was not upgraded into a cell")
	}
	leases := seen.all()
	if len(leases) == 0 || leases[len(leases)-1] != "good" {
		t.Fatalf("the upgrade leased its cell with bearers %q; want the stream's verified bearer", leases)
	}
}
