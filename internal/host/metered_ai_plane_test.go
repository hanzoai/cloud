package host

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/sdk/infer"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// pricedPlane serves the model ops as app "ai" beside the fake commerce, each
// answer carrying the price the model API told for it, and records what crossed.
func pricedPlane(t *testing.T, billed int64, seen chan<- *client.ChatIn) {
	t.Helper()
	answer := func(ctx context.Context, in *client.ChatIn) (*client.Chat, error) {
		seen <- in
		body := `{"id":"c1","object":"chat.completion","model":"enso-ultra",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1000000,"completion_tokens":0,"total_tokens":1000000}}`
		if in.ContentType != "" {
			body = `{"text":"hello there"}`
		}
		return &client.Chat{Status: http.StatusOK, Body: []byte(body), Billed: &billed}, nil
	}
	app := zip.New(zip.Config{AppName: "ai"})
	app.Post("/ai/chat", answer, zip.WithOperationID(client.AIChat))
	app.Post("/ai/transcribe", answer, zip.WithOperationID(client.AITranscribe))
	planetest.Bind(t, "ai", app)
}

// THE PRICE THE MODEL API TOLD IS THE PRICE THE PAYER IS CHARGED, across the
// plane. ai prices the call for the account the request names and says what it
// came to; the answer carries it back, and the meter debits exactly that to that
// account, once. A million enso-ultra prompt tokens list at $5.50, and the debit
// is $5.50 to Ann — not $2.00 at a rate of the meter's own, and not to her org's
// pool.
func TestThePriceTheModelAPIToldIsWhatThePayerIsCharged(t *testing.T) {
	g := newGatewayStub(t)
	fc := &fakeCommerce{balanceBody: `{"available":100000}`}
	srv := fc.server(t)
	seen := make(chan *client.ChatIn, 2)
	pricedPlane(t, 5_500_000_000, seen)

	m := cloud.MeteredAIClient(overPlane(pickCompletionsClient(prodLikeConfig(t, g, ""), luxlog.New("test"))), cloud.Deps{Metering: mustClient(t, srv.URL)})
	ann := account.Person(account.SignupOrg, "ann")
	resp, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{
		Model: "enso-ultra", Prompt: "ping", Org: account.SignupOrg, Payer: ann, Actor: account.SignupOrg + "/ann",
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	<-seen
	if g.chatAuth != "" {
		t.Fatalf("the completion reached the gateway carrying %q; it crosses the plane", g.chatAuth)
	}
	if resp.Billed == nil || *resp.Billed != 5_500_000_000 {
		t.Fatalf("the answer says it was billed %v, want 5500000000 nano-USD", resp.Billed)
	}
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("want one debit, got %d", fc.usages())
	}
	d, _ := fc.debits.last()
	if d.In.Subject != ann.Subject() || d.Org != account.SignupOrg {
		t.Fatalf("debited %q in %q, want %q in %q", d.In.Subject, d.Org, ann.Subject(), account.SignupOrg)
	}
	if got := microsOf(d.In.Amount); got != 5_500_000 {
		t.Fatalf("debited %d micro-USD, want 5500000 — the model API's price", got)
	}
	time.Sleep(100 * time.Millisecond)
	if n := fc.usages(); n != 1 {
		t.Fatalf("one call made %d debits", n)
	}
}

// A TRANSCRIPTION CROSSES THE PLANE AS WELL, as the multipart body it would be over
// HTTP. Over the gateway it was the deployment's own call, billed there to the
// deployment's org, while the meter charged the customer a second time at its own
// rate. Now the model API prices it once and the meter debits that price.
func TestATranscriptionCrossesThePlaneAndIsChargedItsPrice(t *testing.T) {
	g := newGatewayStub(t)
	fc := &fakeCommerce{balanceBody: `{"available":100000}`}
	srv := fc.server(t)
	seen := make(chan *client.ChatIn, 1)
	pricedPlane(t, 6_000_000, seen)

	m := cloud.MeteredAIClient(overPlane(pickCompletionsClient(prodLikeConfig(t, g, ""), luxlog.New("test"))), cloud.Deps{Metering: mustClient(t, srv.URL)})
	text, err := m.(infer.Transcriber).Transcribe(context.Background(), &infer.TranscribeRequest{
		Model: "whisper-1", Audio: []byte("RIFF....WAVE"), Name: "note.wav", Org: "acme", Payer: account.Org("acme"),
	})
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if text != "hello there" {
		t.Fatalf("transcript %q, want the model API's own", text)
	}
	in := <-seen
	if !strings.HasPrefix(in.ContentType, "multipart/form-data") || !strings.Contains(string(in.Request), "RIFF....WAVE") {
		t.Fatalf("the recording crossed as %q (%d bytes), want its multipart body", in.ContentType, len(in.Request))
	}
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("want one debit, got %d", fc.usages())
	}
	d, _ := fc.debits.last()
	if got := microsOf(d.In.Amount); got != 6_000 {
		t.Fatalf("debited %d micro-USD, want 6000 — the model API's price", got)
	}
}
