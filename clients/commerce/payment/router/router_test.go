package router

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// TestMain runs the router's strategy tests with NO disabled-processor policy.
// These tests use Stripe/Adyen/PayPal mocks as fixtures to exercise routing
// strategies (primary-fallback, round-robin, weighted, currency-based, circuit
// breaker), which are orthogonal to the production deny policy (which disables
// Stripe for new-charge selection by default). The explicit "none" sentinel
// keeps every fixture selectable without weakening the production default (an
// empty value now keeps the safe default — see disabledProcessors).
func TestMain(m *testing.M) {
	os.Setenv("COMMERCE_DISABLED_PROCESSORS", "none")
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Mock processor
// ---------------------------------------------------------------------------

type mockProcessor struct {
	*processor.BaseProcessor
	chargeErr    error
	authErr      error
	captureErr   error
	refundErr    error
	txErr        error
	webhookErr   error
	available    bool
	chargeCalls  int64
	authCalls    int64
	captureCalls int64
	refundCalls  int64
}

func newMock(pt processor.ProcessorType, avail bool) *mockProcessor {
	base := processor.NewBaseProcessor(pt, []currency.Type{currency.USD, currency.EUR})
	base.SetConfigured(avail)
	return &mockProcessor{
		BaseProcessor: base,
		available:     avail,
	}
}

func (m *mockProcessor) IsAvailable(_ context.Context) bool { return m.available }

func (m *mockProcessor) Charge(_ context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	atomic.AddInt64(&m.chargeCalls, 1)
	if m.chargeErr != nil {
		return nil, m.chargeErr
	}
	return &processor.PaymentResult{
		Success:       true,
		TransactionID: "tx_" + string(m.Type()),
		Status:        "charged",
	}, nil
}

func (m *mockProcessor) Authorize(_ context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	atomic.AddInt64(&m.authCalls, 1)
	if m.authErr != nil {
		return nil, m.authErr
	}
	return &processor.PaymentResult{
		Success:       true,
		TransactionID: "auth_" + string(m.Type()),
		Status:        "authorized",
	}, nil
}

func (m *mockProcessor) Capture(_ context.Context, txID string, amount currency.Cents) (*processor.PaymentResult, error) {
	atomic.AddInt64(&m.captureCalls, 1)
	if m.captureErr != nil {
		return nil, m.captureErr
	}
	return &processor.PaymentResult{
		Success:       true,
		TransactionID: txID,
		Status:        "captured",
	}, nil
}

func (m *mockProcessor) Refund(_ context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	atomic.AddInt64(&m.refundCalls, 1)
	if m.refundErr != nil {
		return nil, m.refundErr
	}
	return &processor.RefundResult{
		Success:  true,
		RefundID: "ref_" + req.TransactionID,
	}, nil
}

func (m *mockProcessor) GetTransaction(_ context.Context, txID string) (*processor.Transaction, error) {
	if m.txErr != nil {
		return nil, m.txErr
	}
	return &processor.Transaction{
		ID:     txID,
		Status: "complete",
		Amount: 1000,
	}, nil
}

func (m *mockProcessor) ValidateWebhook(_ context.Context, payload []byte, sig string) (*processor.WebhookEvent, error) {
	if m.webhookErr != nil {
		return nil, m.webhookErr
	}
	return &processor.WebhookEvent{
		ID:        "evt_1",
		Type:      "charge.completed",
		Processor: m.Type(),
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setupRegistry(mocks ...*mockProcessor) *processor.Registry {
	reg := processor.NewRegistry(nil)
	for _, m := range mocks {
		reg.Register(m)
	}
	return reg
}

func baseReq() processor.PaymentRequest {
	return processor.PaymentRequest{
		Amount:   5000,
		Currency: currency.USD,
	}
}

// ---------------------------------------------------------------------------
// Tests: PrimaryFallback
// ---------------------------------------------------------------------------

func TestPrimaryFallback_HappyPath(t *testing.T) {
	m1 := newMock("stripe", true)
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	result, err := r.Charge(context.Background(), baseReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.TransactionID != "stripe:tx_stripe" {
		t.Fatalf("expected prefixed txID, got %q", result.TransactionID)
	}
	if atomic.LoadInt64(&m1.chargeCalls) != 1 {
		t.Fatalf("expected 1 call to stripe, got %d", m1.chargeCalls)
	}
	if atomic.LoadInt64(&m2.chargeCalls) != 0 {
		t.Fatal("square should not have been called")
	}
}

func TestPrimaryFallback_PrimaryFails(t *testing.T) {
	m1 := newMock("stripe", true)
	m1.chargeErr = fmt.Errorf("stripe down")
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	result, err := r.Charge(context.Background(), baseReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TransactionID != "square:tx_square" {
		t.Fatalf("expected square txID, got %q", result.TransactionID)
	}
}

func TestPrimaryFallback_AllFail(t *testing.T) {
	m1 := newMock("stripe", true)
	m1.chargeErr = fmt.Errorf("stripe down")
	m2 := newMock("square", true)
	m2.chargeErr = fmt.Errorf("square down")
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	_, err := r.Charge(context.Background(), baseReq())
	if err == nil {
		t.Fatal("expected error when all processors fail")
	}
}

// ---------------------------------------------------------------------------
// Tests: RoundRobin
// ---------------------------------------------------------------------------

func TestRoundRobin(t *testing.T) {
	m1 := newMock("stripe", true)
	m2 := newMock("square", true)
	m3 := newMock("paypal", true)
	reg := setupRegistry(m1, m2, m3)

	r := NewRouter(reg, Config{
		Strategy:   RoundRobin,
		Processors: []processor.ProcessorType{"stripe", "square", "paypal"},
	})

	// Send 6 requests; each processor should get 2.
	for i := 0; i < 6; i++ {
		result, err := r.Charge(context.Background(), baseReq())
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !result.Success {
			t.Fatalf("request %d: expected success", i)
		}
	}

	s := atomic.LoadInt64(&m1.chargeCalls)
	q := atomic.LoadInt64(&m2.chargeCalls)
	p := atomic.LoadInt64(&m3.chargeCalls)
	if s != 2 || q != 2 || p != 2 {
		t.Fatalf("expected 2/2/2, got stripe=%d square=%d paypal=%d", s, q, p)
	}
}

// ---------------------------------------------------------------------------
// Tests: CurrencyBased
// ---------------------------------------------------------------------------

func TestCurrencyBased(t *testing.T) {
	mStripe := newMock("stripe", true)
	mAdyen := newMock("adyen", true)
	reg := setupRegistry(mStripe, mAdyen)

	r := NewRouter(reg, Config{
		Strategy:   CurrencyBased,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "adyen"},
		CurrencyMap: map[string]processor.ProcessorType{
			"eur": "adyen",
			"usd": "stripe",
		},
	})

	// EUR request -> adyen
	eurReq := baseReq()
	eurReq.Currency = currency.EUR
	result, err := r.Charge(context.Background(), eurReq)
	if err != nil {
		t.Fatal(err)
	}
	if result.TransactionID != "adyen:tx_adyen" {
		t.Fatalf("EUR should route to adyen, got %q", result.TransactionID)
	}

	// USD request -> stripe
	result, err = r.Charge(context.Background(), baseReq())
	if err != nil {
		t.Fatal(err)
	}
	if result.TransactionID != "stripe:tx_stripe" {
		t.Fatalf("USD should route to stripe, got %q", result.TransactionID)
	}
}

// ---------------------------------------------------------------------------
// Tests: WeightedRandom
// ---------------------------------------------------------------------------

func TestWeightedRandom(t *testing.T) {
	m1 := newMock("stripe", true)
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   WeightedRandom,
		Processors: []processor.ProcessorType{"stripe", "square"},
		Weights: map[processor.ProcessorType]int{
			"stripe": 90,
			"square": 10,
		},
	})

	for i := 0; i < 100; i++ {
		_, err := r.Charge(context.Background(), baseReq())
		if err != nil {
			t.Fatal(err)
		}
	}

	s := atomic.LoadInt64(&m1.chargeCalls)
	q := atomic.LoadInt64(&m2.chargeCalls)
	if s+q != 100 {
		t.Fatalf("total calls should be 100, got %d", s+q)
	}
	// With 90/10 weights, stripe should get the majority.
	// Allowing generous margin for randomness.
	if s < 50 {
		t.Fatalf("stripe (weight 90) got only %d/100 calls", s)
	}
}

// ---------------------------------------------------------------------------
// Tests: LeastLoad
// ---------------------------------------------------------------------------

func TestLeastLoad(t *testing.T) {
	m1 := newMock("stripe", true)
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   LeastLoad,
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	// Artificially set stripe inflight high.
	if c := r.getInflight("stripe"); c != nil {
		atomic.StoreInt64(c, 100)
	}

	result, err := r.Charge(context.Background(), baseReq())
	if err != nil {
		t.Fatal(err)
	}
	// Square has lower load, should be selected.
	if result.TransactionID != "square:tx_square" {
		t.Fatalf("expected square (least load), got %q", result.TransactionID)
	}
}

// ---------------------------------------------------------------------------
// Tests: Capture routes to correct processor
// ---------------------------------------------------------------------------

func TestCapture_RoutesToOriginalProcessor(t *testing.T) {
	m1 := newMock("stripe", true)
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	// Simulate an auth that went through square.
	txID := "square:auth_square"

	result, err := r.Capture(context.Background(), txID, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if atomic.LoadInt64(&m2.captureCalls) != 1 {
		t.Fatal("capture should have gone to square")
	}
	if atomic.LoadInt64(&m1.captureCalls) != 0 {
		t.Fatal("capture should NOT have gone to stripe")
	}
}

func TestCapture_NoPrefixReturnsError(t *testing.T) {
	m1 := newMock("stripe", true)
	reg := setupRegistry(m1)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe"},
	})

	_, err := r.Capture(context.Background(), "no_prefix_id", 5000)
	if err == nil {
		t.Fatal("expected error for unprefixed transaction ID")
	}
}

// ---------------------------------------------------------------------------
// Tests: Refund routes to correct processor
// ---------------------------------------------------------------------------

func TestRefund_RoutesToOriginalProcessor(t *testing.T) {
	m1 := newMock("stripe", true)
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	req := processor.RefundRequest{
		TransactionID: "square:tx_original",
		Amount:        2000,
	}

	result, err := r.Refund(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if atomic.LoadInt64(&m2.refundCalls) != 1 {
		t.Fatal("refund should have gone to square")
	}
	// Refund ID should also be prefixed.
	if result.RefundID != "square:ref_tx_original" {
		t.Fatalf("expected prefixed refund ID, got %q", result.RefundID)
	}
}

// ---------------------------------------------------------------------------
// Tests: GetTransaction
// ---------------------------------------------------------------------------

func TestGetTransaction_WithPrefix(t *testing.T) {
	m1 := newMock("stripe", true)
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	tx, err := r.GetTransaction(context.Background(), "square:tx_123")
	if err != nil {
		t.Fatal(err)
	}
	if tx.ID != "square:tx_123" {
		t.Fatalf("expected prefixed ID, got %q", tx.ID)
	}
}

func TestGetTransaction_WithoutPrefix_TriesAll(t *testing.T) {
	m1 := newMock("stripe", true)
	m1.txErr = fmt.Errorf("not found")
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	tx, err := r.GetTransaction(context.Background(), "raw_tx_456")
	if err != nil {
		t.Fatal(err)
	}
	if tx.ID != "square:raw_tx_456" {
		t.Fatalf("expected square-prefixed ID, got %q", tx.ID)
	}
}

// ---------------------------------------------------------------------------
// Tests: ValidateWebhook
// ---------------------------------------------------------------------------

func TestValidateWebhook(t *testing.T) {
	m1 := newMock("stripe", true)
	m1.webhookErr = fmt.Errorf("bad sig")
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	event, err := r.ValidateWebhook(context.Background(), []byte("payload"), "sig")
	if err != nil {
		t.Fatal(err)
	}
	if event.Processor != "square" {
		t.Fatalf("expected square to validate, got %s", event.Processor)
	}
}

func TestValidateWebhook_AllFail(t *testing.T) {
	m1 := newMock("stripe", true)
	m1.webhookErr = fmt.Errorf("bad")
	m2 := newMock("square", true)
	m2.webhookErr = fmt.Errorf("bad")
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	_, err := r.ValidateWebhook(context.Background(), []byte("x"), "y")
	if err == nil {
		t.Fatal("expected error when all processors fail webhook validation")
	}
}

// ---------------------------------------------------------------------------
// Tests: Circuit breaker
// ---------------------------------------------------------------------------

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	m1 := newMock("stripe", true)
	m1.chargeErr = fmt.Errorf("fail")
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold: 3,
			ResetTimeout:     1 * time.Second,
			HalfOpenMax:      1,
		},
	})

	// 3 failures should open the breaker.
	for i := 0; i < 3; i++ {
		r.Charge(context.Background(), baseReq())
	}

	// Now stripe breaker is open; next call should go straight to square
	// without even trying stripe.
	stripeBefore := atomic.LoadInt64(&m1.chargeCalls)
	result, err := r.Charge(context.Background(), baseReq())
	if err != nil {
		t.Fatal(err)
	}
	if result.TransactionID != "square:tx_square" {
		t.Fatalf("expected square after circuit opens, got %q", result.TransactionID)
	}
	stripeAfter := atomic.LoadInt64(&m1.chargeCalls)
	if stripeAfter != stripeBefore {
		t.Fatal("stripe should not have received more calls while circuit is open")
	}
}

func TestCircuitBreaker_HalfOpenRecovery(t *testing.T) {
	m1 := newMock("stripe", true)
	m1.chargeErr = fmt.Errorf("fail")
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold: 2,
			ResetTimeout:     50 * time.Millisecond,
			HalfOpenMax:      1,
		},
	})

	// Open the breaker.
	for i := 0; i < 2; i++ {
		r.Charge(context.Background(), baseReq())
	}

	// Wait for reset timeout.
	time.Sleep(60 * time.Millisecond)

	// Fix stripe.
	m1.chargeErr = nil

	// Next call should probe stripe (half-open) and succeed.
	result, err := r.Charge(context.Background(), baseReq())
	if err != nil {
		t.Fatal(err)
	}
	if result.TransactionID != "stripe:tx_stripe" {
		t.Fatalf("expected stripe after recovery, got %q", result.TransactionID)
	}
}

// ---------------------------------------------------------------------------
// Tests: IsAvailable / SupportedCurrencies
// ---------------------------------------------------------------------------

func TestIsAvailable(t *testing.T) {
	m1 := newMock("stripe", false)
	m2 := newMock("square", false)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	if r.IsAvailable(context.Background()) {
		t.Fatal("should not be available when no processors are available")
	}

	m2.available = true
	m2.SetConfigured(true)
	if !r.IsAvailable(context.Background()) {
		t.Fatal("should be available when at least one processor is available")
	}
}

func TestSupportedCurrencies(t *testing.T) {
	m1 := newMock("stripe", true)
	reg := setupRegistry(m1)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe"},
	})

	currencies := r.SupportedCurrencies()
	if len(currencies) == 0 {
		t.Fatal("expected non-empty currencies")
	}

	found := make(map[currency.Type]bool)
	for _, c := range currencies {
		found[c] = true
	}
	if !found[currency.USD] || !found[currency.EUR] {
		t.Fatalf("expected USD and EUR, got %v", currencies)
	}
}

// ---------------------------------------------------------------------------
// Tests: Type
// ---------------------------------------------------------------------------

func TestType(t *testing.T) {
	reg := processor.NewRegistry(nil)
	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Processors: []processor.ProcessorType{},
	})
	if r.Type() != "router" {
		t.Fatalf("expected type 'router', got %q", r.Type())
	}
}

// ---------------------------------------------------------------------------
// Tests: Authorize
// ---------------------------------------------------------------------------

func TestAuthorize(t *testing.T) {
	m1 := newMock("stripe", true)
	reg := setupRegistry(m1)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe"},
	})

	result, err := r.Authorize(context.Background(), baseReq())
	if err != nil {
		t.Fatal(err)
	}
	if result.TransactionID != "stripe:auth_stripe" {
		t.Fatalf("expected prefixed auth txID, got %q", result.TransactionID)
	}
	if atomic.LoadInt64(&m1.authCalls) != 1 {
		t.Fatal("expected 1 authorize call")
	}
}

// ---------------------------------------------------------------------------
// Tests: MaxRetries
// ---------------------------------------------------------------------------

func TestMaxRetries(t *testing.T) {
	m1 := newMock("stripe", true)
	m1.chargeErr = fmt.Errorf("fail")
	m2 := newMock("square", true)
	m2.chargeErr = fmt.Errorf("fail")
	m3 := newMock("paypal", true)
	reg := setupRegistry(m1, m2, m3)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square", "paypal"},
		MaxRetries: 1, // Only 1 retry after initial = 2 total attempts
	})

	_, err := r.Charge(context.Background(), baseReq())
	if err == nil {
		t.Fatal("expected error: only 2 attempts allowed, both fail")
	}
	// paypal should never be tried (maxRetries=1 means 2 total).
	if atomic.LoadInt64(&m3.chargeCalls) != 0 {
		t.Fatal("paypal should not have been tried with maxRetries=1")
	}
}

// ---------------------------------------------------------------------------
// Tests: Concurrency safety
// ---------------------------------------------------------------------------

func TestConcurrentCharges(t *testing.T) {
	m1 := newMock("stripe", true)
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   RoundRobin,
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	var wg sync.WaitGroup
	const n = 100
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Charge(context.Background(), baseReq())
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent charge failed: %v", err)
	}

	total := atomic.LoadInt64(&m1.chargeCalls) + atomic.LoadInt64(&m2.chargeCalls)
	if total != n {
		t.Fatalf("expected %d total calls, got %d", n, total)
	}
}

// ---------------------------------------------------------------------------
// Tests: No processors configured
// ---------------------------------------------------------------------------

func TestNoProcessors(t *testing.T) {
	reg := processor.NewRegistry(nil)
	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Processors: []processor.ProcessorType{},
	})

	_, err := r.Charge(context.Background(), baseReq())
	if err == nil {
		t.Fatal("expected error with no processors")
	}
}

// ---------------------------------------------------------------------------
// Tests: Unsuccessful result triggers fallback
// ---------------------------------------------------------------------------

func TestUnsuccessfulResultFallback(t *testing.T) {
	m1 := newMock("stripe", true)
	// Override Charge to return non-success result without error.
	origCharge := m1.Charge
	_ = origCharge
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	// We need a custom processor that returns success=false with no error.
	failProc := &failResultProcessor{
		BaseProcessor: processor.NewBaseProcessor("stripe", []currency.Type{currency.USD}),
	}
	failProc.SetConfigured(true)
	reg.Register(failProc)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	result, err := r.Charge(context.Background(), baseReq())
	if err != nil {
		t.Fatal(err)
	}
	if result.TransactionID != "square:tx_square" {
		t.Fatalf("expected fallback to square, got %q", result.TransactionID)
	}
}

// failResultProcessor returns Success=false without an error.
type failResultProcessor struct {
	*processor.BaseProcessor
}

func (f *failResultProcessor) IsAvailable(_ context.Context) bool { return true }
func (f *failResultProcessor) Charge(_ context.Context, _ processor.PaymentRequest) (*processor.PaymentResult, error) {
	return &processor.PaymentResult{
		Success:      false,
		ErrorMessage: "declined",
	}, nil
}
func (f *failResultProcessor) Authorize(_ context.Context, _ processor.PaymentRequest) (*processor.PaymentResult, error) {
	return nil, fmt.Errorf("not impl")
}
func (f *failResultProcessor) Capture(_ context.Context, _ string, _ currency.Cents) (*processor.PaymentResult, error) {
	return nil, fmt.Errorf("not impl")
}
func (f *failResultProcessor) Refund(_ context.Context, _ processor.RefundRequest) (*processor.RefundResult, error) {
	return nil, fmt.Errorf("not impl")
}
func (f *failResultProcessor) GetTransaction(_ context.Context, _ string) (*processor.Transaction, error) {
	return nil, fmt.Errorf("not impl")
}
func (f *failResultProcessor) ValidateWebhook(_ context.Context, _ []byte, _ string) (*processor.WebhookEvent, error) {
	return nil, fmt.Errorf("not impl")
}

// ---------------------------------------------------------------------------
// Tests: parseTransactionID edge cases
// ---------------------------------------------------------------------------

func TestParseTransactionID_EmptyRawID(t *testing.T) {
	reg := processor.NewRegistry(nil)
	r := NewRouter(reg, Config{Processors: []processor.ProcessorType{}})

	_, _, err := r.parseTransactionID("stripe:")
	if err == nil {
		t.Fatal("expected error for empty raw ID")
	}
}

func TestParseTransactionID_NoColon(t *testing.T) {
	reg := processor.NewRegistry(nil)
	r := NewRouter(reg, Config{Processors: []processor.ProcessorType{}})

	_, _, err := r.parseTransactionID("no-colon-here")
	if err == nil {
		t.Fatal("expected error for no colon")
	}
}

// ---------------------------------------------------------------------------
// Tests: prefixResult nil safety
// ---------------------------------------------------------------------------

func TestPrefixResult_NilResult(t *testing.T) {
	reg := processor.NewRegistry(nil)
	r := NewRouter(reg, Config{Processors: []processor.ProcessorType{}})

	// Should not panic.
	r.prefixResult(nil, "stripe")
}

// ---------------------------------------------------------------------------
// Tests: getProcessor errors
// ---------------------------------------------------------------------------

func TestGetProcessor_NotFound(t *testing.T) {
	reg := processor.NewRegistry(nil)
	r := NewRouter(reg, Config{Processors: []processor.ProcessorType{}})

	_, err := r.getProcessor(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing processor")
	}
}

func TestGetProcessor_NotAvailable(t *testing.T) {
	m := newMock("stripe", false) // not available
	reg := setupRegistry(m)
	r := NewRouter(reg, Config{Processors: []processor.ProcessorType{"stripe"}})

	_, err := r.getProcessor(context.Background(), "stripe")
	if err == nil {
		t.Fatal("expected error for unavailable processor")
	}
}

// ---------------------------------------------------------------------------
// Tests: Capture error paths
// ---------------------------------------------------------------------------

func TestCapture_ProcessorNotFound(t *testing.T) {
	reg := processor.NewRegistry(nil)
	r := NewRouter(reg, Config{Processors: []processor.ProcessorType{}})

	_, err := r.Capture(context.Background(), "nonexistent:tx-1", 1000)
	if err == nil {
		t.Fatal("expected error for missing processor")
	}
}

func TestCapture_ProcessorUnavailable(t *testing.T) {
	m := newMock("stripe", false)
	reg := setupRegistry(m)
	r := NewRouter(reg, Config{Processors: []processor.ProcessorType{"stripe"}})

	_, err := r.Capture(context.Background(), "stripe:tx-1", 1000)
	if err == nil {
		t.Fatal("expected error for unavailable processor")
	}
}

func TestCapture_ProcessorError(t *testing.T) {
	m := newMock("stripe", true)
	m.captureErr = fmt.Errorf("capture failed")
	reg := setupRegistry(m)
	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe"},
	})

	_, err := r.Capture(context.Background(), "stripe:tx-1", 1000)
	if err == nil {
		t.Fatal("expected error for capture failure")
	}
}

// ---------------------------------------------------------------------------
// Tests: Refund error paths
// ---------------------------------------------------------------------------

func TestRefund_NoPrefixReturnsError(t *testing.T) {
	m := newMock("stripe", true)
	reg := setupRegistry(m)
	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe"},
	})

	_, err := r.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "no_prefix_id",
		Amount:        500,
	})
	if err == nil {
		t.Fatal("expected error for unprefixed transaction ID")
	}
}

func TestRefund_ProcessorNotFound(t *testing.T) {
	reg := processor.NewRegistry(nil)
	r := NewRouter(reg, Config{Processors: []processor.ProcessorType{}})

	_, err := r.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "nonexistent:tx-1",
		Amount:        500,
	})
	if err == nil {
		t.Fatal("expected error for missing processor")
	}
}

func TestRefund_ProcessorError(t *testing.T) {
	m := newMock("stripe", true)
	m.refundErr = fmt.Errorf("refund failed")
	reg := setupRegistry(m)
	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe"},
	})

	_, err := r.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "stripe:tx-1",
		Amount:        500,
	})
	if err == nil {
		t.Fatal("expected error for refund failure")
	}
}

// ---------------------------------------------------------------------------
// Tests: GetTransaction error paths
// ---------------------------------------------------------------------------

func TestGetTransaction_PrefixedButProcessorNotFound(t *testing.T) {
	reg := processor.NewRegistry(nil)
	r := NewRouter(reg, Config{Processors: []processor.ProcessorType{}})

	_, err := r.GetTransaction(context.Background(), "nonexistent:tx-1")
	if err == nil {
		t.Fatal("expected error for missing processor")
	}
}

func TestGetTransaction_PrefixedButProcessorError(t *testing.T) {
	m := newMock("stripe", true)
	m.txErr = fmt.Errorf("not found")
	reg := setupRegistry(m)
	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe"},
	})

	_, err := r.GetTransaction(context.Background(), "stripe:tx-bad")
	if err == nil {
		t.Fatal("expected error for transaction not found")
	}
}

func TestGetTransaction_WithoutPrefix_AllFail(t *testing.T) {
	m1 := newMock("stripe", true)
	m1.txErr = fmt.Errorf("not found")
	m2 := newMock("square", true)
	m2.txErr = fmt.Errorf("not found")
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	_, err := r.GetTransaction(context.Background(), "raw_tx_missing")
	if err == nil {
		t.Fatal("expected error when all processors fail")
	}
}

func TestGetTransaction_WithoutPrefix_UnavailableSkipped(t *testing.T) {
	m1 := newMock("stripe", false) // unavailable
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	tx, err := r.GetTransaction(context.Background(), "raw_tx_456")
	if err != nil {
		t.Fatal(err)
	}
	if tx.ID != "square:raw_tx_456" {
		t.Fatalf("expected square-prefixed ID, got %q", tx.ID)
	}
}

// ---------------------------------------------------------------------------
// Tests: ValidateWebhook - unavailable processor skipped
// ---------------------------------------------------------------------------

func TestValidateWebhook_UnavailableSkipped(t *testing.T) {
	m1 := newMock("stripe", false) // unavailable
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
	})

	event, err := r.ValidateWebhook(context.Background(), []byte("payload"), "sig")
	if err != nil {
		t.Fatal(err)
	}
	if event.Processor != "square" {
		t.Fatalf("expected square, got %s", event.Processor)
	}
}

// ---------------------------------------------------------------------------
// Tests: IsAvailable - registry lookup error
// ---------------------------------------------------------------------------

func TestIsAvailable_RegistryError(t *testing.T) {
	reg := processor.NewRegistry(nil)
	// "stripe" not registered, so Get will fail.
	r := NewRouter(reg, Config{
		Processors: []processor.ProcessorType{"stripe"},
	})

	if r.IsAvailable(context.Background()) {
		t.Fatal("expected not available when processor not in registry")
	}
}

// ---------------------------------------------------------------------------
// Tests: selectCandidates - default strategy
// ---------------------------------------------------------------------------

func TestSelectCandidates_DefaultStrategy(t *testing.T) {
	m := newMock("stripe", true)
	reg := setupRegistry(m)

	r := NewRouter(reg, Config{
		Strategy:   Strategy("unknown_strategy"),
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe"},
	})

	result, err := r.Charge(context.Background(), baseReq())
	if err != nil {
		t.Fatal(err)
	}
	if result.TransactionID != "stripe:tx_stripe" {
		t.Fatalf("expected stripe txID, got %q", result.TransactionID)
	}
}

// ---------------------------------------------------------------------------
// Tests: WeightedRandom with no weights (falls back to shuffled)
// ---------------------------------------------------------------------------

func TestWeightedRandom_NoWeights(t *testing.T) {
	m1 := newMock("stripe", true)
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   WeightedRandom,
		Processors: []processor.ProcessorType{"stripe", "square"},
		Weights:    nil, // No weights.
	})

	for i := 0; i < 10; i++ {
		result, err := r.Charge(context.Background(), baseReq())
		if err != nil {
			t.Fatal(err)
		}
		if !result.Success {
			t.Fatal("expected success")
		}
	}

	total := atomic.LoadInt64(&m1.chargeCalls) + atomic.LoadInt64(&m2.chargeCalls)
	if total != 10 {
		t.Fatalf("expected 10 total calls, got %d", total)
	}
}

// ---------------------------------------------------------------------------
// Tests: WeightedRandom with zero/negative weight defaults to 1
// ---------------------------------------------------------------------------

func TestWeightedRandom_ZeroWeight(t *testing.T) {
	m1 := newMock("stripe", true)
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   WeightedRandom,
		Processors: []processor.ProcessorType{"stripe", "square"},
		Weights: map[processor.ProcessorType]int{
			"stripe": 0,  // defaults to 1
			"square": -1, // defaults to 1
		},
	})

	for i := 0; i < 20; i++ {
		_, err := r.Charge(context.Background(), baseReq())
		if err != nil {
			t.Fatal(err)
		}
	}

	total := atomic.LoadInt64(&m1.chargeCalls) + atomic.LoadInt64(&m2.chargeCalls)
	if total != 20 {
		t.Fatalf("expected 20 total, got %d", total)
	}
}

// ---------------------------------------------------------------------------
// Tests: RoundRobin with empty processors
// ---------------------------------------------------------------------------

func TestRoundRobin_EmptyProcessors(t *testing.T) {
	reg := processor.NewRegistry(nil)
	r := NewRouter(reg, Config{
		Strategy:   RoundRobin,
		Processors: []processor.ProcessorType{},
	})

	_, err := r.Charge(context.Background(), baseReq())
	if err == nil {
		t.Fatal("expected error with no processors")
	}
}

// ---------------------------------------------------------------------------
// Tests: LeastLoad with empty processors
// ---------------------------------------------------------------------------

func TestLeastLoad_EmptyProcessors(t *testing.T) {
	reg := processor.NewRegistry(nil)
	r := NewRouter(reg, Config{
		Strategy:   LeastLoad,
		Processors: []processor.ProcessorType{},
	})

	_, err := r.Charge(context.Background(), baseReq())
	if err == nil {
		t.Fatal("expected error with no processors")
	}
}

// ---------------------------------------------------------------------------
// Tests: Circuit breaker - half-open max exceeded
// ---------------------------------------------------------------------------

func TestCircuitBreaker_HalfOpenMaxExceeded(t *testing.T) {
	m1 := newMock("stripe", true)
	m1.chargeErr = fmt.Errorf("fail")
	m2 := newMock("square", true)
	reg := setupRegistry(m1, m2)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "square"},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold: 2,
			ResetTimeout:     50 * time.Millisecond,
			HalfOpenMax:      1,
		},
	})

	// Open the breaker.
	for i := 0; i < 2; i++ {
		r.Charge(context.Background(), baseReq())
	}

	// Wait for reset timeout to enter half-open.
	time.Sleep(60 * time.Millisecond)

	// First call should go to stripe (half-open probe), fails, re-opens.
	r.Charge(context.Background(), baseReq())

	// Immediately after, another call should skip stripe (half-open max exceeded/re-opened).
	stripeBefore := atomic.LoadInt64(&m1.chargeCalls)
	result, err := r.Charge(context.Background(), baseReq())
	if err != nil {
		t.Fatal(err)
	}
	if result.TransactionID != "square:tx_square" {
		t.Fatalf("expected square, got %q", result.TransactionID)
	}
	// Stripe should not have been called (still open after half-open failure).
	stripeAfter := atomic.LoadInt64(&m1.chargeCalls)
	if stripeAfter != stripeBefore {
		t.Fatal("stripe should not have been called while circuit re-opened")
	}
}

// ---------------------------------------------------------------------------
// Tests: Circuit breaker - failure in half-open re-opens
// ---------------------------------------------------------------------------

func TestCircuitBreaker_FailureInHalfOpenReopens(t *testing.T) {
	cb := newCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 1,
		ResetTimeout:     10 * time.Millisecond,
		HalfOpenMax:      1,
	})

	// Trigger open.
	cb.failure()
	if cb.state != cbOpen {
		t.Fatal("expected open state")
	}

	// Wait for reset timeout.
	time.Sleep(15 * time.Millisecond)

	// Allow should transition to half-open.
	if !cb.allow() {
		t.Fatal("expected allow in half-open")
	}
	if cb.state != cbHalfOpen {
		t.Fatal("expected half-open state")
	}

	// Failure in half-open re-opens.
	cb.failure()
	if cb.state != cbOpen {
		t.Fatal("expected re-open after failure in half-open")
	}

	// Subsequent allow should return false (just re-opened).
	if cb.allow() {
		t.Fatal("expected deny immediately after re-open")
	}
}

// ---------------------------------------------------------------------------
// Tests: routePayment - all circuit breakers open, no processor available
// ---------------------------------------------------------------------------

func TestRoutePayment_NoCBAllowed(t *testing.T) {
	m1 := newMock("stripe", true)
	m1.chargeErr = fmt.Errorf("fail")
	reg := setupRegistry(m1)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe"},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold: 1,
			ResetTimeout:     10 * time.Second,
			HalfOpenMax:      1,
		},
	})

	// Open the breaker with 1 failure.
	r.Charge(context.Background(), baseReq())

	// Now all breakers are open, should get NO_PROCESSOR error.
	_, err := r.Charge(context.Background(), baseReq())
	if err == nil {
		t.Fatal("expected error when all circuit breakers are open")
	}
}

// ---------------------------------------------------------------------------
// Tests: routePayment - processor not in registry
// ---------------------------------------------------------------------------

func TestRoutePayment_ProcessorNotInRegistry(t *testing.T) {
	reg := processor.NewRegistry(nil)
	// "unknown" is listed but not registered.
	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "unknown",
		Processors: []processor.ProcessorType{"unknown"},
	})

	_, err := r.Charge(context.Background(), baseReq())
	if err == nil {
		t.Fatal("expected error for unregistered processor")
	}
}

// ---------------------------------------------------------------------------
// Tests: routePayment - processor not available
// ---------------------------------------------------------------------------

func TestRoutePayment_ProcessorNotAvailable(t *testing.T) {
	m := newMock("stripe", false) // not available
	reg := setupRegistry(m)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe"},
	})

	_, err := r.Charge(context.Background(), baseReq())
	if err == nil {
		t.Fatal("expected error for unavailable processor")
	}
}

// ---------------------------------------------------------------------------
// Tests: CurrencyBased - unknown currency falls back
// ---------------------------------------------------------------------------

func TestCurrencyBased_FallbackToPrimary(t *testing.T) {
	mStripe := newMock("stripe", true)
	mAdyen := newMock("adyen", true)
	reg := setupRegistry(mStripe, mAdyen)

	r := NewRouter(reg, Config{
		Strategy:   CurrencyBased,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe", "adyen"},
		CurrencyMap: map[string]processor.ProcessorType{
			"eur": "adyen",
		},
	})

	// GBP not in map -> falls back to primary (stripe).
	gbpReq := baseReq()
	gbpReq.Currency = currency.GBP
	result, err := r.Charge(context.Background(), gbpReq)
	if err != nil {
		t.Fatal(err)
	}
	if result.TransactionID != "stripe:tx_stripe" {
		t.Fatalf("expected stripe for GBP fallback, got %q", result.TransactionID)
	}
}

// ---------------------------------------------------------------------------
// Tests: NewRouter - processor not in registry (skip in currency collection)
// ---------------------------------------------------------------------------

func TestNewRouter_ProcessorNotInRegistry(t *testing.T) {
	reg := processor.NewRegistry(nil)
	// "ghost" is not registered.
	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Processors: []processor.ProcessorType{"ghost"},
	})

	// Should create router without error, just no currencies.
	currencies := r.SupportedCurrencies()
	if len(currencies) != 0 {
		t.Fatalf("expected empty currencies for unregistered processor, got %d", len(currencies))
	}
}

// ---------------------------------------------------------------------------
// Tests: Refund - empty RefundID not prefixed
// ---------------------------------------------------------------------------

func TestRefund_EmptyRefundIDNotPrefixed(t *testing.T) {
	m := &emptyRefundProcessor{
		BaseProcessor: processor.NewBaseProcessor("stripe", []currency.Type{currency.USD}),
	}
	m.SetConfigured(true)
	reg := processor.NewRegistry(nil)
	reg.Register(m)

	r := NewRouter(reg, Config{
		Strategy:   PrimaryFallback,
		Primary:    "stripe",
		Processors: []processor.ProcessorType{"stripe"},
	})

	result, err := r.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "stripe:tx-1",
		Amount:        500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RefundID != "" {
		t.Fatalf("expected empty refund ID, got %q", result.RefundID)
	}
}

// emptyRefundProcessor returns a result with empty RefundID.
type emptyRefundProcessor struct {
	*processor.BaseProcessor
}

func (e *emptyRefundProcessor) IsAvailable(_ context.Context) bool { return true }
func (e *emptyRefundProcessor) Charge(_ context.Context, _ processor.PaymentRequest) (*processor.PaymentResult, error) {
	return nil, fmt.Errorf("not impl")
}
func (e *emptyRefundProcessor) Authorize(_ context.Context, _ processor.PaymentRequest) (*processor.PaymentResult, error) {
	return nil, fmt.Errorf("not impl")
}
func (e *emptyRefundProcessor) Capture(_ context.Context, _ string, _ currency.Cents) (*processor.PaymentResult, error) {
	return nil, fmt.Errorf("not impl")
}
func (e *emptyRefundProcessor) Refund(_ context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	return &processor.RefundResult{
		Success:  true,
		RefundID: "", // empty
	}, nil
}
func (e *emptyRefundProcessor) GetTransaction(_ context.Context, _ string) (*processor.Transaction, error) {
	return nil, fmt.Errorf("not impl")
}
func (e *emptyRefundProcessor) ValidateWebhook(_ context.Context, _ []byte, _ string) (*processor.WebhookEvent, error) {
	return nil, fmt.Errorf("not impl")
}
