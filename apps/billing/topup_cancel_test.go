package billing

// A payment is cancelled by the person who opened it, while it is unconfirmed, and by no
// one else. The stand-in commerce keeps the rules the real one keeps (its own, its state)
// so the route is shown to carry the caller's wallet and to keep commerce's refusals.

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
	"os"
)

func cancelPeer(t *testing.T, owned map[string]string) *sync.Map {
	t.Helper()
	dir, err := os.MkdirTemp("", "zc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	state := &sync.Map{} // intent id -> state
	peer := zip.New(zip.Config{Logger: luxlog.New("test")})
	peer.Post("/billing/topup/intent/cancel", func(_ context.Context, in *client.TopupIntentRef) (*client.TopupIntentCancelled, error) {
		if owned[in.ID] != in.Subject {
			return nil, zip.Errorf(http.StatusNotFound, "payment not found")
		}
		st, _ := state.Load(in.ID)
		if s, _ := st.(string); s == "succeeded" || s == "credited" {
			return nil, zip.Errorf(http.StatusConflict, "payment already %s", s)
		}
		state.Store(in.ID, "canceled")
		return &client.TopupIntentCancelled{IntentID: in.ID, State: "canceled"}, nil
	}, zip.WithOperationID(client.BillingTopupIntentCancel))
	sock := zip.SocketPath("commerce")
	go func() { _ = peer.Listen(sock) }()
	t.Cleanup(func() { _ = peer.Shutdown() })
	for range 300 {
		if _, err := os.Stat(sock); err == nil {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the stand-in commerce never listened on %s", sock)
	return nil
}

func TestACallerCancelsOnlyTheirOwnUnconfirmedPayment(t *testing.T) {
	state := cancelPeer(t, map[string]string{"pi_mine": "hanzo/alice", "pi_paid": "hanzo/alice", "pi_theirs": "hanzo/bob"})
	state.Store("pi_paid", "succeeded")
	app := mountApp(t)
	for _, tc := range []struct {
		name, id string
		who      map[string]string
		want     int
	}{
		{"own unconfirmed", "pi_mine", signup, http.StatusOK},
		{"own again, idempotent", "pi_mine", signup, http.StatusOK},
		{"another person's", "pi_theirs", signup, http.StatusNotFound},
		{"confirmed", "pi_paid", signup, http.StatusConflict},
		{"anonymous", "pi_mine", nil, http.StatusUnauthorized},
	} {
		code, body := act(t, app, http.MethodPost, "/v1/billing/topup/intent/"+tc.id+"/cancel", `{}`, tc.who)
		if code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, code, clip(body), tc.want)
		}
	}
	if s, _ := state.Load("pi_theirs"); s != nil {
		t.Errorf("another person's payment moved to %v", s)
	}
	if s, _ := state.Load("pi_paid"); s != "succeeded" {
		t.Errorf("a confirmed payment moved to %v", s)
	}
}
