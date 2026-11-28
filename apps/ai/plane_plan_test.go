// Copyright © 2026 Hanzo AI. MIT License.

package ai

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/infer"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// payerFor returns a test limiter for one subject/plan/wallet setup using the
// carried plan policy (pro-3x, max-20x, unlimited, etc.).
func payerFor(t *testing.T, plan, settled string, cash, credit int64) *limits.Limiter {
	t.Helper()
	now := time.Now().UTC()
	var rows []client.Subscription
	if plan != "" {
		rows = []client.Subscription{{
			ID: "sub-1", PlanID: "rec-" + plan, Status: "active", Quantity: 1, ProviderType: "square", Settled: settled,
			Seats: 1, ChargedCents: 1500, Plan: client.SubscriptionPlan{Price: 1500, Interval: "month"},
			CurrentPeriodStart: now.AddDate(0, 0, -1).Format(time.RFC3339),
			CurrentPeriodEnd:   now.AddDate(0, 1, -1).Format(time.RFC3339),
		}}
	}
	l, err := limits.Open(t.TempDir(),
		func(context.Context, string, string) (string, string, error) {
			if plan == "" {
				return "", "", nil
			}
			return plan, "sub-1", nil
		},
		func(context.Context, string, string) ([]client.Subscription, error) { return rows, nil },
		func(context.Context) ([]client.PlanRow, error) {
			return []client.PlanRow{
				{Slug: "pro-3x", Name: "Pro", Category: "personal", Price: 1500, Status: "active"},
				{Slug: "max-15x", Name: "Max 15x", Category: "personal", Price: 7500, Status: "active"},
				{Slug: "max-20x", Name: "Max 20x", Category: "personal", Price: 20000, Status: "active"},
				{Slug: "unlimited", Name: "Unlimited", Category: "personal", Price: 50000, Status: "active"},
			}, nil
		},
		func(context.Context, string, string) (int64, int64, error) { return cash, credit, nil },
		carried)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// setupPlaneStub installs a fake IAM + modelAPI that bills priceNanos on 200 OK
// (or answers statusCode when >= 400), and returns a cleanup-wired helper.
func setupPlaneStub(t *testing.T, priceNanos *int64, statusCode *int) *int {
	t.Helper()
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"machine-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(iam.Close)
	t.Setenv("IAM_URL", iam.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cret")

	calls := 0
	wasModel := modelAPI
	modelAPI = func(caller aiobject.Caller) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			code := http.StatusOK
			if statusCode != nil && *statusCode != 0 {
				code = *statusCode
			}
			if code >= http.StatusBadRequest {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"error":{"message":"upstream error"}}`))
				return
			}
			if priceNanos != nil && *priceNanos > 0 {
				caller.Bill.Add(*priceNanos)
			}
			if strings.Contains(r.URL.Path, "chat") && strings.Contains(string(readAll(r.Body)), `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}`+"\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion"}`))
		})
	}
	t.Cleanup(func() { modelAPI = wasModel })
	return &calls
}

func readAll(r io.Reader) []byte {
	if r == nil {
		return nil
	}
	b, _ := io.ReadAll(r)
	return b
}

func runStream(t *testing.T, ctx context.Context, body string) (*http.Response, string, *infer.Bill) {
	t.Helper()
	near, far := net.Pipe()
	go func() {
		defer func() { _ = far.Close() }()
		_ = buyStreamed(ctx, far, &client.ChatIn{Request: []byte(body)})
	}()
	rctx, bill := infer.WithBill(context.Background())
	r, _ := http.NewRequestWithContext(rctx, http.MethodPost, "http://ai/v1/chat/completions", nil)
	resp, err := infer.StreamResponse(near, r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp, string(got), bill
}

// Paid subscribers on the internal plane (Cloud Agents, Coding, Studio, Ask, etc.)
// MUST consume their included Hanzo-model allowance with ZERO wallet debit on both
// buffered (buy) and streamed (buyStreamed) calls, while third-party models, fast
// mode, and exhausted allowances debit the wallet or refuse 402/429.
func TestPlaneRespectsPlanAllowanceAcrossBufferedAndStreamedCalls(t *testing.T) {
	wasLim := limiter
	t.Cleanup(func() { limiter = wasLim; zen.Store(nil) })

	// 1. Pro (pro-3x) subscriber with $0 wallet calling enso-auto, zen6-coder,
	//    zen-embedding, and zen-scribe (multipart) via buy AND buyStreamed:
	//    admitted, served, settled against limits, ZERO wallet debit!
	t.Run("pro-3x subscriber at $0 wallet consumes plan allowance with zero wallet debit", func(t *testing.T) {
		price := int64(500_000_000) // $0.50 per call
		status := http.StatusOK
		calls := setupPlaneStub(t, &price, &status)
		zen.Store(nil)

		lim := payerFor(t, "pro-3x", "card", 0, 0)
		limiter = lim

		mvee := account.Person(account.SignupOrg, "mvee85560")
		ctx := zip.WithCaller(context.Background(), zip.Caller{
			Org: account.SignupOrg, User: "mvee85560", Name: "mvee85560", Account: mvee.String(),
		})

		// Read-only Covers check returns true without incrementing counters.
		cov, err := planePlanCovers(ctx, &client.PlanCoversIn{
			Subject: mvee.Subject(), Org: mvee.Org(), Actor: "hanzo/mvee85560", Model: "enso-auto",
		})
		if err != nil || !cov.Covered {
			t.Fatalf("planePlanCovers(pro-3x, enso-auto) = %+v %v, want covered=true", cov, err)
		}
		if s, _ := lim.Read(ctx, mvee.Subject(), mvee.Org()); s.Session.Used != 0 {
			t.Fatalf("planePlanCovers mutated session count to %d", s.Session.Used)
		}

		// Buffered chat on enso-auto → covered, nil Billed, settled into limits.
		out, err := buy(ctx, "/v1/chat/completions", &client.ChatIn{
			Request: []byte(`{"model":"enso-auto","messages":[{"role":"user","content":"hi"}]}`),
		})
		if err != nil || out.Status != http.StatusOK || !out.Covered || out.Billed != nil {
			t.Fatalf("pro-3x enso-auto buy = status %d covered=%v billed=%v err=%v; want 200 covered=true billed=nil",
				out.Status, out.Covered, out.Billed, err)
		}

		// Streamed chat on zen6-coder (served by co-resident zen!) → covered, settled into limits.
		ServeZen(func(ctx context.Context, path string, req []byte) (*client.Chat, bool) {
			if !strings.Contains(string(req), `"zen6-coder"`) && !strings.Contains(string(req), `"zen-embedding"`) {
				return nil, false
			}
			BillOf(ctx).Add(price)
			return &client.Chat{Status: http.StatusOK, Body: []byte(`{"id":"z1"}`)}, true
		})
		t.Cleanup(func() { zen.Store(nil) })

		resp, _, bill := runStream(t, ctx, `{"model":"zen6-coder","stream":true,"messages":[{"role":"user","content":"code"}]}`)
		if resp.StatusCode != http.StatusOK || !bill.Covered() {
			t.Fatalf("pro-3x zen6-coder stream = status %d covered=%v, want 200 covered=true", resp.StatusCode, bill.Covered())
		}
		if n, told := bill.Nanos(); told {
			t.Fatalf("plan-covered stream told wallet bill %d", n)
		}

		// Embeddings on zen-embedding (even with detached account-only caller, no User!).
		embedCtx := zip.WithCaller(context.Background(), zip.Caller{Org: account.SignupOrg, Account: mvee.String()})
		out, err = buy(embedCtx, "/v1/embeddings", &client.ChatIn{
			Request: []byte(`{"model":"zen-embedding","input":["hello"]}`),
		})
		if err != nil || out.Status != http.StatusOK || !out.Covered || out.Billed != nil {
			t.Fatalf("pro-3x zen-embedding = status %d covered=%v billed=%v err=%v; want 200 covered=true billed=nil",
				out.Status, out.Covered, out.Billed, err)
		}

		// Multipart transcription on zen-scribe → covered, nil Billed.
		const form = "--b\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nzen-scribe\r\n--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\n\r\nRIFF\r\n--b--\r\n"
		out, err = buy(embedCtx, "/v1/audio/transcriptions", &client.ChatIn{
			Request: []byte(form), ContentType: "multipart/form-data; boundary=b",
		})
		if err != nil || out.Status != http.StatusOK || !out.Covered || out.Billed != nil {
			t.Fatalf("pro-3x zen-scribe transcribe = status %d covered=%v billed=%v err=%v; want 200 covered=true billed=nil",
				out.Status, out.Covered, out.Billed, err)
		}

		// Verify all 4 calls ($2.00 total) settled against limits and counted 4 requests.
		s, err := lim.Read(ctx, mvee.Subject(), mvee.Org())
		if err != nil {
			t.Fatal(err)
		}
		if got, want := s.Classes[limits.ClassOurs].Period.Used, 4*price; got != want {
			t.Fatalf("pro-3x settled plan spend = %d, want %d", got, want)
		}
		if s.Session.Used != 4 || *calls != 2 {
			t.Fatalf("session=%d modelAPI calls=%d, want 4 and 2", s.Session.Used, *calls)
		}
	})

	// 2. Unlimited subscriber with $100 wallet calling enso-auto & zen6-coder → ZERO wallet debit!
	t.Run("unlimited subscriber with $100 wallet is never wallet-debited for Hanzo models", func(t *testing.T) {
		price := int64(2 * usd)
		status := http.StatusOK
		setupPlaneStub(t, &price, &status)
		zen.Store(nil)

		lim := payerFor(t, "unlimited", "external:comp", 100*usd, 0)
		limiter = lim
		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", Account: account.Org("hanzo").String()})

		cov, err := planePlanCovers(ctx, &client.PlanCoversIn{Subject: "hanzo", Org: "hanzo", Model: "enso-auto"})
		if err != nil || !cov.Covered {
			t.Fatalf("unlimited Covers(enso-auto) = %+v %v, want true", cov, err)
		}
		out, err := buy(ctx, "/v1/chat/completions", &client.ChatIn{
			Request: []byte(`{"model":"enso-auto","messages":[{"role":"user","content":"hi"}]}`),
		})
		if err != nil || out.Status != http.StatusOK || !out.Covered || out.Billed != nil {
			t.Fatalf("unlimited enso-auto = status %d covered=%v billed=%v err=%v; want 200 covered=true billed=nil",
				out.Status, out.Covered, out.Billed, err)
		}
		resp, _, bill := runStream(t, ctx, `{"model":"enso-auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		if resp.StatusCode != http.StatusOK || !bill.Covered() {
			t.Fatalf("unlimited enso-auto stream = status %d covered=%v; want 200 covered=true", resp.StatusCode, bill.Covered())
		}
	})

	// 3. Pro subscriber after exhausting $15 Hanzo allowance:
	//    - with $5 cash wallet → falls back to PaysPrepaid (out.Billed set, wallet debited)
	//    - with $0 wallet → refused 429 plan_allowance_used before upstream
	t.Run("exhausted pro-3x falls back to wallet when funded and refuses 429 when empty", func(t *testing.T) {
		price := int64(15 * usd) // exhausts entire $15 Pro allowance in 1 call
		status := http.StatusOK
		calls := setupPlaneStub(t, &price, &status)
		zen.Store(nil)

		cash := int64(5 * usd)
		now := time.Now().UTC()
		rows := []client.Subscription{{
			ID: "sub-1", PlanID: "rec-pro-3x", Status: "active", Quantity: 1, ProviderType: "square", Settled: "card",
			Seats: 1, ChargedCents: 1500, Plan: client.SubscriptionPlan{Price: 1500, Interval: "month"},
			CurrentPeriodStart: now.AddDate(0, 0, -1).Format(time.RFC3339),
			CurrentPeriodEnd:   now.AddDate(0, 1, -1).Format(time.RFC3339),
		}}
		lim, err := limits.Open(t.TempDir(),
			func(context.Context, string, string) (string, string, error) { return "pro-3x", "sub-1", nil },
			func(context.Context, string, string) ([]client.Subscription, error) { return rows, nil },
			func(context.Context) ([]client.PlanRow, error) {
				return []client.PlanRow{{Slug: "max-15x", Name: "Max 15x", Category: "personal", Price: 7500, Status: "active"}}, nil
			},
			func(context.Context, string, string) (int64, int64, error) { return cash, 0, nil },
			carried)
		if err != nil {
			t.Fatal(err)
		}
		limiter = lim
		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-1", Name: "alice"})

		// First call consumes the full $15 allowance → plan-covered.
		out, err := buy(ctx, "/v1/chat/completions", &client.ChatIn{Request: []byte(`{"model":"enso-auto","messages":[]}`)})
		if err != nil || out.Status != http.StatusOK || !out.Covered || out.Billed != nil {
			t.Fatalf("first call = status %d covered=%v billed=%v err=%v; want covered", out.Status, out.Covered, out.Billed, err)
		}

		// Now plan is exhausted → Covers must be false.
		if cov, _ := planePlanCovers(ctx, &client.PlanCoversIn{Subject: "acme", Org: "acme", Model: "enso-auto"}); cov.Covered {
			t.Fatal("exhausted pro-3x still reported covered=true")
		}

		// Second call with $5 cash wallet → overage falls back to wallet debit!
		price = 250_000_000 // $0.25
		out, err = buy(ctx, "/v1/chat/completions", &client.ChatIn{Request: []byte(`{"model":"enso-auto","messages":[]}`)})
		if err != nil || out.Status != http.StatusOK || out.Covered || out.Billed == nil || *out.Billed != price {
			t.Fatalf("overage call with cash = status %d covered=%v billed=%v err=%v; want 200 billed=%d",
				out.Status, out.Covered, out.Billed, err, price)
		}

		// Empty the wallet → third call refused 429 plan_allowance_used BEFORE modelAPI.
		cash = 0
		beforeCalls := *calls
		out, err = buy(ctx, "/v1/chat/completions", &client.ChatIn{Request: []byte(`{"model":"enso-auto","messages":[]}`)})
		if err != nil || out.Status != http.StatusTooManyRequests || !strings.Contains(string(out.Body), limits.CodePlanAllowance) {
			t.Fatalf("exhausted + empty wallet = %d %s err=%v; want 429 %s", out.Status, out.Body, err, limits.CodePlanAllowance)
		}
		if *calls != beforeCalls {
			t.Fatal("refused call reached modelAPI")
		}
	})

	// 4. Third-party ClassPremium model on Pro/Unlimited:
	//    - Covers == false
	//    - $0 cash (even with granted credit!) → refused 402 prepaid_required
	//    - $10 cash → served, wallet-debited with Cash=true
	t.Run("third-party premium model on monthly plan requires prepaid cash", func(t *testing.T) {
		price := int64(400_000_000)
		status := http.StatusOK
		calls := setupPlaneStub(t, &price, &status)
		zen.Store(nil)

		// Pro with $0 cash and $10 granted credit → refused 402 prepaid_required!
		limiter = payerFor(t, "pro-3x", "card", 0, 10*usd)
		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-1", Name: "alice"})

		if cov, _ := planePlanCovers(ctx, &client.PlanCoversIn{Subject: "acme", Org: "acme", Model: "anthropic/claude-opus-4.7"}); cov.Covered {
			t.Fatal("pro-3x reported covered=true for third-party premium model")
		}
		out, err := buy(ctx, "/v1/chat/completions", &client.ChatIn{Request: []byte(`{"model":"anthropic/claude-opus-4.7","messages":[]}`)})
		if err != nil || out.Status != http.StatusPaymentRequired || !strings.Contains(string(out.Body), limits.CodePrepaid) {
			t.Fatalf("pro-3x premium with credit only = %d %s err=%v; want 402 %s", out.Status, out.Body, err, limits.CodePrepaid)
		}
		if *calls != 0 {
			t.Fatal("refused premium call reached modelAPI")
		}

		// Unlimited with $10 cash → NOT covered by plan, billed as cash-only wallet debit!
		limiter = payerFor(t, "unlimited", "external:comp", 10*usd, 10*usd)
		if cov, _ := planePlanCovers(ctx, &client.PlanCoversIn{Subject: "acme", Org: "acme", Model: "anthropic/claude-opus-4.7"}); cov.Covered {
			t.Fatal("unlimited reported covered=true for third-party premium model")
		}
		out, err = buy(ctx, "/v1/chat/completions", &client.ChatIn{Request: []byte(`{"model":"anthropic/claude-opus-4.7","messages":[]}`)})
		if err != nil || out.Status != http.StatusOK || out.Covered || out.Billed == nil || *out.Billed != price || !out.Cash {
			t.Fatalf("unlimited premium with cash = status %d covered=%v billed=%v cash=%v err=%v; want 200 billed=%d cash=true",
				out.Status, out.Covered, out.Billed, out.Cash, err, price)
		}
	})

	// 5. Unserved upstream error (503) on plan-covered grant → Release gives back
	//    session/day count, zero plan spend settled (both buy and buyStreamed).
	t.Run("unserved upstream error releases plan request slot and settles zero spend", func(t *testing.T) {
		price := int64(500_000_000)
		status := http.StatusServiceUnavailable
		setupPlaneStub(t, &price, &status)
		zen.Store(nil)

		lim := payerFor(t, "pro-3x", "card", 0, 0)
		limiter = lim
		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-1", Name: "alice"})

		out, err := buy(ctx, "/v1/chat/completions", &client.ChatIn{Request: []byte(`{"model":"enso-auto","messages":[]}`)})
		if err != nil || out.Status != http.StatusServiceUnavailable || out.Covered || out.Billed != nil {
			t.Fatalf("503 buy = status %d covered=%v billed=%v err=%v; want 503 uncovered unbilled",
				out.Status, out.Covered, out.Billed, err)
		}
		resp, _, bill := runStream(t, ctx, `{"model":"enso-auto","stream":true,"messages":[]}`)
		if resp.StatusCode != http.StatusServiceUnavailable || bill.Covered() {
			t.Fatalf("503 stream = status %d covered=%v; want 503 uncovered", resp.StatusCode, bill.Covered())
		}
		s, err := lim.Read(ctx, "acme", "acme")
		if err != nil {
			t.Fatal(err)
		}
		if s.Session.Used != 0 || s.Day.Used != 0 || s.Classes[limits.ClassOurs].Period.Used != 0 {
			t.Fatalf("unserved errors leaked counts/spend: session=%d day=%d used=%d",
				s.Session.Used, s.Day.Used, s.Classes[limits.ClassOurs].Period.Used)
		}
	})

	// 6. Fast mode on Unlimited & Max 20x: never covered by plan, requires prepaid cash.
	t.Run("fast mode on unlimited and max-20x requires prepaid cash", func(t *testing.T) {
		price := int64(800_000_000)
		status := http.StatusOK
		calls := setupPlaneStub(t, &price, &status)
		zen.Store(nil)

		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-1", Name: "alice"})
		for _, plan := range []string{"unlimited", "max-20x"} {
			// $0 cash wallet → Covers=false, buy refuses 402 prepaid_required.
			limiter = payerFor(t, plan, "card", 0, 10*usd)
			if cov, _ := planePlanCovers(ctx, &client.PlanCoversIn{Subject: "acme", Org: "acme", Model: "enso-auto", Fast: true}); cov.Covered {
				t.Fatalf("%s reported covered=true for fast mode", plan)
			}
			for _, body := range []string{
				`{"model":"enso-auto","fast":true,"messages":[]}`,
				`{"model":"enso-auto","speed":"fast","messages":[]}`,
			} {
				out, err := buy(ctx, "/v1/chat/completions", &client.ChatIn{Request: []byte(body)})
				var e struct {
					Error map[string]string `json:"error"`
				}
				_ = json.Unmarshal(out.Body, &e)
				if err != nil || out.Status != http.StatusPaymentRequired || e.Error["code"] != limits.CodePrepaid {
					t.Fatalf("%s fast (%s) at $0 cash = %d %s err=%v; want 402 %s", plan, body, out.Status, out.Body, err, limits.CodePrepaid)
				}
			}
			if *calls != 0 {
				t.Fatalf("%s refused fast call reached modelAPI", plan)
			}

			// $10 cash wallet → served as prepaid cash debit (NOT plan covered).
			limiter = payerFor(t, plan, "card", 10*usd, 0)
			out, err := buy(ctx, "/v1/chat/completions", &client.ChatIn{Request: []byte(`{"model":"enso-auto","fast":true,"messages":[]}`)})
			if err != nil || out.Status != http.StatusOK || out.Covered || out.Billed == nil || *out.Billed != price || !out.Cash {
				t.Fatalf("%s fast with $10 cash = status %d covered=%v billed=%v cash=%v err=%v; want 200 billed=%d cash=true",
					plan, out.Status, out.Covered, out.Billed, out.Cash, err, price)
			}
			*calls = 0
		}
	})
}
