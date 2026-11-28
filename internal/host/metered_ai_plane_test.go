package host

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"

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

// End-to-end proof of the P0 sibling plan-allowance fix:
//   - A Pro (pro-3x) subscriber with $0 wallet calling enso-auto (chat, stream)
//     and zen-embedding (embed) skips the wallet hold via AIPlanCovers, crosses
//     with their billing account, and settles with ZERO wallet debit.
//   - An Unlimited subscriber with $100 wallet calling enso-auto makes ZERO
//     wallet debits.
//   - A third-party premium model (or exhausted plan) reports Covered=false:
//     refused 402 at $0 wallet BEFORE reaching the plane, and cash-debited when
//     funded.
func TestMeteredAIOverPlaneHonorsPlanAllowance(t *testing.T) {
	g := newGatewayStub(t)
	fc := &fakeCommerce{balanceBody: `{"available":0}`}
	srv := fc.server(t)

	var seenCallers []zip.Caller
	sockDir, err := os.MkdirTemp("", "ais")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	var streamSeq int

	app := zip.New(zip.Config{AppName: "ai"})
	app.Post("/ai/plan/covers", func(ctx context.Context, in *client.PlanCoversIn) (*client.PlanCoversOut, error) {
		covered := strings.HasPrefix(in.Model, "enso") || strings.HasPrefix(in.Model, "zen")
		return &client.PlanCoversOut{Covered: covered && !in.Fast}, nil
	}, zip.WithOperationID(client.AIPlanCovers))
	app.Post("/ai/chat", func(ctx context.Context, in *client.ChatIn) (*client.Chat, error) {
		seenCallers = append(seenCallers, cloud.Who(ctx))
		body := `{"id":"c1","object":"chat.completion","model":"enso-auto",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`
		if strings.Contains(string(in.Request), `"anthropic/`) {
			billed := int64(900_000)
			return &client.Chat{Status: http.StatusOK, Body: []byte(body), Billed: &billed, Cash: true}, nil
		}
		return &client.Chat{Status: http.StatusOK, Body: []byte(body), Covered: true}, nil
	}, zip.WithOperationID(client.AIChat))
	app.Post("/ai/chat/stream", func(ctx context.Context, in *client.ChatIn) (*client.Streamed, error) {
		seenCallers = append(seenCallers, cloud.Who(ctx))
		streamSeq++
		sock := filepath.Join(sockDir, fmt.Sprintf("s%d.sock", streamSeq))
		ln, err := net.Listen("unix", sock)
		if err != nil {
			return nil, err
		}
		go func() {
			defer func() { _ = ln.Close() }()
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			sw := infer.NewStreamWriter(conn)
			sw.Header().Set("Content-Type", "text/event-stream")
			_, _ = sw.Write([]byte(`data: {"id":"c1","object":"chat.completion.chunk","model":"enso-auto","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n"))
			_, _ = sw.Write([]byte("data: [DONE]\n\n"))
			_ = sw.EndCovered()
		}()
		return &client.Streamed{Socket: sock, ExpiresIn: 30}, nil
	}, zip.WithOperationID(client.AIChatStream))
	app.Post("/ai/embed", func(ctx context.Context, in *client.ChatIn) (*client.Chat, error) {
		seenCallers = append(seenCallers, cloud.Who(ctx))
		body := `{"object":"list","data":[{"index":0,"embedding":[0.25,0.75]}]}`
		return &client.Chat{Status: http.StatusOK, Body: []byte(body), Covered: true}, nil
	}, zip.WithOperationID(client.AIEmbed))
	planetest.Bind(t, "ai", app)

	m := cloud.MeteredAIClient(overPlane(pickCompletionsClient(prodLikeConfig(t, g, ""), luxlog.New("test"))), cloud.Deps{Metering: mustClient(t, srv.URL)})
	mvee := account.Person(account.SignupOrg, "mvee85560")

	// 1. Pro subscriber at $0 wallet: buffered chat, streamed chat, and embed ALL succeed with zero wallet debit.
	resp, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{
		Model: "enso-auto", Prompt: "ping", Org: account.SignupOrg, Payer: mvee, Actor: account.SignupOrg + "/mvee85560",
	})
	if err != nil {
		t.Fatalf("pro-3x $0 wallet buffered chat failed: %v", err)
	}
	if resp.Content != "pong" || resp.Billed != nil {
		t.Fatalf("buffered chat = content %q billed %v, want pong and nil", resp.Content, resp.Billed)
	}

	var deltas []string
	sresp, err := m.(infer.StreamCompleter).ChatStream(context.Background(), &infer.ChatRequest{
		Model: "enso-auto", Prompt: "ping", Org: account.SignupOrg, Payer: mvee, Actor: account.SignupOrg + "/mvee85560",
	}, func(d string) error { deltas = append(deltas, d); return nil })
	if err != nil {
		t.Fatalf("pro-3x $0 wallet streamed chat failed: %v", err)
	}
	if sresp.Content != "hi" || len(deltas) != 1 || sresp.Billed != nil {
		t.Fatalf("streamed chat = content %q deltas %v billed %v, want hi and nil", sresp.Content, deltas, sresp.Billed)
	}

	vecs, err := m.Embed(context.Background(), &infer.EmbedRequest{
		Model: "zen-embedding", Inputs: []string{"hello"}, Org: account.SignupOrg, Payer: mvee,
	})
	if err != nil || len(vecs) != 1 {
		t.Fatalf("pro-3x $0 wallet embed failed: vecs=%d err=%v", len(vecs), err)
	}
	// Verify Embed carried the billing account claim across the plane even without Actor!
	if got := seenCallers[len(seenCallers)-1].Account; got != mvee.String() {
		t.Fatalf("embed plane caller.Account = %q, want %q", got, mvee.String())
	}

	time.Sleep(150 * time.Millisecond)
	if n := fc.usages(); n != 0 {
		t.Fatalf("plan-covered calls posted %d wallet debits, want 0", n)
	}

	// 2. Third-party model at $0 wallet → refused 402 BEFORE reaching the plane.
	beforeCalls := len(seenCallers)
	_, err = m.ChatCompletion(context.Background(), &infer.ChatRequest{
		Model: "anthropic/claude-opus-4.7", Prompt: "ping", Org: account.SignupOrg, Payer: mvee, Actor: account.SignupOrg + "/mvee85560",
	})
	if !errors.Is(err, meter.ErrInsufficientBalance) {
		t.Fatalf("third-party at $0 wallet = %v, want ErrInsufficientBalance", err)
	}
	if len(seenCallers) != beforeCalls {
		t.Fatal("refused third-party call reached the plane")
	}

	// 3. Funded ($100) wallet: Hanzo model still posts ZERO debits; third-party posts cash-only debit!
	fc.balanceBody = `{"available":10000}`
	if _, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{
		Model: "enso-auto", Prompt: "ping", Org: "hanzo", Payer: account.Org("hanzo"),
	}); err != nil {
		t.Fatalf("funded plan-covered chat failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := fc.usages(); n != 0 {
		t.Fatalf("funded plan-covered call debited wallet (%d debits)", n)
	}

	resp, err = m.ChatCompletion(context.Background(), &infer.ChatRequest{
		Model: "anthropic/claude-opus-4.7", Prompt: "ping", Org: "hanzo", Payer: account.Org("hanzo"),
	})
	if err != nil {
		t.Fatalf("funded third-party chat failed: %v", err)
	}
	if resp.Billed == nil || *resp.Billed != 900_000 {
		t.Fatalf("funded third-party billed = %v, want 900000", resp.Billed)
	}
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("want 1 debit for third-party call, got %d", fc.usages())
	}
	d, _ := fc.debits.last()
	if !d.In.Usage.Cash || microsOf(d.In.Amount) != 900 {
		t.Fatalf("third-party debit = cash:%v micros:%d, want cash:true micros:900", d.In.Usage.Cash, microsOf(d.In.Amount))
	}
}
