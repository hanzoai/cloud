// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/infer"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// A streamed completion is bought as ai_chat buys one, for the stated caller with
// the deployment's credential, and its answer crosses the socket as the model API
// writes it, with the price the model API told after the last byte. A refusal
// crosses as its status and body, priced at nothing.
func TestAStreamedCompletionCrossesAsWrittenAndCarriesItsPrice(t *testing.T) {
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"machine-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer iam.Close()
	t.Setenv("IAM_URL", iam.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cret")

	refuse := false
	var asked aiobject.Caller
	was := modelAPI
	modelAPI = func(caller aiobject.Caller) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked = caller
			if refuse {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(`{"error":{"message":"insufficient_balance"}}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, d := range []string{"Hel", "lo"} {
				_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"`+d+`"}}]}`+"\n\n")
				w.(http.Flusher).Flush()
			}
			caller.Bill.Add(4200)
		})
	}
	t.Cleanup(func() { modelAPI = was })

	buy := func() (*http.Response, string, *infer.Bill) {
		t.Helper()
		near, far := net.Pipe()
		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-7"})
		go func() {
			defer func() { _ = far.Close() }()
			_ = buyStreamed(ctx, far, &client.ChatIn{Request: []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)})
		}()
		rctx, bill := infer.WithBill(context.Background())
		r, _ := http.NewRequestWithContext(rctx, http.MethodPost, "http://ai/v1/chat/completions", nil)
		resp, err := infer.StreamResponse(near, r)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp, string(body), bill
	}

	resp, body, bill := buy()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" ||
		strings.Count(body, "data: ") != 2 || !strings.Contains(body, `"Hel"`) || !strings.Contains(body, `"lo"`) {
		t.Errorf("answer %d %s %q, want the model API's stream", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if n, told := bill.Nanos(); !told || n != 4200 {
		t.Errorf("price %d (%v), want the 4200 the model API told, once", n, told)
	}
	if asked.Org != "acme" || asked.Person != "acme/u-7" {
		t.Errorf("the model API was asked for %+v, want acme and acme/u-7", asked)
	}

	refuse = true
	resp, body, bill = buy()
	if resp.StatusCode != http.StatusPaymentRequired || !strings.Contains(body, "insufficient_balance") {
		t.Errorf("a refusal crossed as %d %q, want the 402 and its body", resp.StatusCode, body)
	}
	if _, told := bill.Nanos(); told {
		t.Error("a refusal was priced")
	}
}
