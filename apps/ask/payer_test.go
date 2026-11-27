package ask

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// payerAI records the completion request the narration sent, so a test can read
// the account it names.
type payerAI struct {
	mu  sync.Mutex
	req *infer.ChatRequest
}

func (p *payerAI) ChatCompletion(_ context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.req = req
	return &infer.ChatResponse{Content: "Your MRR is $1."}, nil
}

func (p *payerAI) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

func (p *payerAI) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) {
	return nil, nil
}

func (p *payerAI) payer() account.Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.req == nil {
		return account.Account{}
	}
	return p.req.Payer
}

// A person in the signup org pays for their own question. The org's own account
// there is the platform's pool, and a narration billed to it is Hanzo paying for a
// stranger's model call.
func TestASignupOrgPersonsNarrationIsTheirs(t *testing.T) {
	ai := &payerAI{}
	app := newAskApp(t, ai, byOrg{account.SignupOrg: money("$1")}, nil, nil)

	body, _ := json.Marshal(askRequest{Question: "what is my MRR?"})
	req := httptest.NewRequest(http.MethodPost, "/v1/ask", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", account.SignupOrg)
	req.Header.Set("X-User-Owner", account.SignupOrg)
	req.Header.Set("X-User-Id", "8d2f0c4e-0000-4000-8000-000000000001")
	req.Header.Set("X-User-Name", "alice")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ask answered %d", resp.StatusCode)
	}

	got := ai.payer()
	if want := account.Person(account.SignupOrg, "alice"); got != want {
		t.Fatalf("the narration names %q as its payer, want %q (the person, never the platform pool %q)",
			got.Subject(), want.Subject(), account.SignupOrg)
	}
}
