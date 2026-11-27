package marketplace

// The market runs on every replica. These tests stand a second replica beside the
// first: on hanzo-sql a second service with a store pool and a lock of its own over
// the same schema, which is what another pod is; on SQLite the same service, because
// a file belongs to one process.

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/zap-proto/zip"
)

// replicaApp is another replica's hire door beside e's.
func (e *env) replicaApp() *zip.App {
	e.t.Helper()
	if !dialect.Of(mounted.State.store.db).Postgres() {
		return e.app
	}
	e.t.Setenv(locus.Env, e.dsn)
	store, err := Open(cloud.DataDir(), e.master, e.kms)
	e.t.Setenv(locus.Env, "")
	if err != nil {
		e.t.Fatalf("a second replica's store: %v", err)
	}
	e.t.Cleanup(func() { _ = store.Close() })
	s := &cloud.Service[state]{Base: mounted.Base, State: state{store: store, audit: mounted.State.audit}}
	s.State.expertise = newExpertise(store, s.Log)
	app := zip.New(zip.Config{Logger: mounted.Log})
	compose(app)
	app.Group("/v1/marketplace").Post("/jobs", marketOps{s: s}.hire, zip.WithStatus(http.StatusCreated))
	return app
}

// post sends a hire as the buyer to app, answering the status and the body, and
// never failing the test itself, so a goroutine can call it.
func (e *env) post(app *zip.App, proof string, body any) (int, []byte, error) {
	b, _ := json.Marshal(body)
	hr := httptest.NewRequest(http.MethodPost, "/v1/marketplace/jobs", bytes.NewReader(b))
	hr.Header.Set("Content-Type", "application/json")
	w := member(buyer)
	hr.Header.Set("X-Org-Id", w.org)
	hr.Header.Set("X-User-Id", cmp.Or(w.user, "u_"+w.org))
	hr.Header.Set("X-User-Email", "admin@"+w.org+".test")
	hr.Header.Set("X-User-Owner", w.org)
	hr.Header.Set(protocol.HeaderPaymentSignature, proof)
	resp, err := app.Test(hr, zip.TestConfig{Timeout: 60 * time.Second})
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out, nil
}

// meeting makes the first two clearances wait for each other, so each funding has read
// the quote before either has opened the job: the moment two replicas funding one
// payment could each go on to open it. A funding alone at the clearance (the other held
// out by the lock) waits a moment and goes on.
func meeting(s *stand) {
	var mu sync.Mutex
	var n int
	met := make(chan struct{})
	s.set(func(s *stand) {
		s.meet = func() {
			mu.Lock()
			n++
			first := n <= 2
			if n == 2 {
				close(met)
			}
			mu.Unlock()
			if first {
				select {
				case <-met:
				case <-time.After(300 * time.Millisecond):
				}
			}
		}
	})
}

// One payment sent to two replicas at once opens its job once and gives nothing up: the
// funding that finds the job opened by the other answers that job, and the amount set
// aside for it stays set aside. A funding that gave its payment up on losing the race
// would leave a job open with no money behind it.
func TestOnePaymentSentToTwoReplicasOpensOneJobWithItsMoneyHeld(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l, _ := e.shopfront()
		e.fund(buyer, money.FromCents(10_000))
		in := map[string]any{"listing": l.ID, "brief": "Two doors", "amount": "30.00", "attempt": "att-two-replicas"}
		code, b, hd := e.req(http.MethodPost, "/v1/marketplace/jobs", member(buyer), "", in)
		if code != http.StatusPaymentRequired {
			t.Fatalf("quote = %d %s", code, b)
		}
		payment := e.pay(e.challengeOf(hd), 30*24*time.Hour)
		quoted, _ := client.JobOf(e.resource)
		apps := []*zip.App{e.app, e.replicaApp()}
		meeting(e.peers)

		var wg sync.WaitGroup
		codes := make([]int, len(apps))
		bodies := make([][]byte, len(apps))
		for i, app := range apps {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var err error
				if codes[i], bodies[i], err = e.post(app, payment, in); err != nil {
					t.Errorf("replica %d: %v", i, err)
				}
			}()
		}
		wg.Wait()
		for i := range apps {
			var j Job
			if codes[i] != http.StatusCreated || json.Unmarshal(bodies[i], &j) != nil || j.ID != quoted || j.Status != StatusOpen {
				t.Fatalf("replica %d answered %d %s, want the opened job %s", i, codes[i], bodies[i], quoted)
			}
		}
		if got := e.balance(buyer, buyer); got.Cmp(money.FromCents(10_000-3_000)) != 0 {
			t.Fatalf("the buyer holds %s, want 30.00 set aside for the job that opened", got)
		}
		if now := e.job(quoted, member(buyer)); now.Status != StatusOpen {
			t.Fatalf("the job is %s, want open", now.Status)
		}
	})
}
