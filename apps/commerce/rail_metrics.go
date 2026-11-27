// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// rail_metrics.go — per-rail payment outcome counters, processor cancellation
// classification, RPC cursor lag gauges, and stalled-deposit watcher alerts.
//
// Zero credentials, bearer tokens, client secrets, API keys, or RPC path/query
// tokens ever enter metric labels, logs, audit rows, or bus pulses.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/commerce/billing/depositledger"
	commercedatastore "github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/cryptopaymentintent"
	"github.com/hanzoai/commerce/models/organization"
	commercensctx "github.com/hanzoai/commerce/util/nscontext"
	luxlog "github.com/luxfi/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/coord"
)

const (
	defaultConfirmingSLA = 30 * time.Minute
	defaultMaxLagBlocks  = uint64(500)
)

// WatcherHealthConfig tunes stalled-deposit and cursor-lag detection.
type WatcherHealthConfig struct {
	Now            time.Time
	ConfirmingSLA  time.Duration
	MaxLagBlocks   uint64
	HeadBlocks     map[string]uint64 // optional chain:token -> latest chain head block
	LastSyncError  error
	Audit          *audit.Recorder
	Pods           *coord.Coord
	AlertWindowKey string
}

// AssetHealth is one watched asset's cursor and lag state.
type AssetHealth struct {
	Chain     string `json:"chain"`
	Token     string `json:"token"`
	Contract  string `json:"contract"`
	Endpoint  string `json:"endpoint"`
	LastBlock uint64 `json:"lastBlock"`
	HeadBlock uint64 `json:"headBlock,omitempty"`
	LagBlocks uint64 `json:"lagBlocks,omitempty"`
	Stalled   bool   `json:"stalled"`
	Reason    string `json:"reason,omitempty"`
}

// StalledDeposit describes a deposit intent stuck in confirming (or sighted pending) past SLA.
type StalledDeposit struct {
	Org       string `json:"org"`
	IntentID  string `json:"intentId"`
	Chain     string `json:"chain"`
	Token     string `json:"token"`
	Status    string `json:"status"`
	AgeSec    int64  `json:"ageSec"`
	Reason    string `json:"reason"`
}

// WatcherHealthReport summarizes native deposit watcher health across all watched assets and orgs.
type WatcherHealthReport struct {
	Running         bool             `json:"running"`
	Healthy         bool             `json:"healthy"`
	Assets          []AssetHealth    `json:"assets"`
	StalledDeposits []StalledDeposit `json:"stalledDeposits"`
	Problems        []string         `json:"problems"`
}

// RailMetricsSnap is an in-process snapshot of rail counters and gauges for diagnostics and tests.
type RailMetricsSnap struct {
	ProcessorOutcomes map[string]int64
	CryptoEvents      map[string]int64
	CursorLagBlocks   map[string]uint64
	StalledAlerts     int64
}

type commerceRailTelemetry struct {
	mu                sync.Mutex
	processorOutcomes map[string]int64
	cryptoEvents      map[string]int64
	cursorLag         map[string]uint64
	prevBlock         map[string]uint64
	prevBlockAt       map[string]time.Time
	stalledAlerts     int64

	once          sync.Once
	otelProcCount metric.Int64Counter
	otelCrypCount metric.Int64Counter
	otelLagGauge  metric.Int64Gauge
	otelStallCnt  metric.Int64Counter
}

var railTelemetry = &commerceRailTelemetry{
	processorOutcomes: map[string]int64{},
	cryptoEvents:      map[string]int64{},
	cursorLag:         map[string]uint64{},
	prevBlock:         map[string]uint64{},
	prevBlockAt:       map[string]time.Time{},
}

func (t *commerceRailTelemetry) initOTel() {
	t.once.Do(func() {
		meter := otel.Meter("hanzo.cloud/commerce.rails")
		t.otelProcCount, _ = meter.Int64Counter(
			"hanzo.commerce.processor.outcomes",
			metric.WithDescription("Processor top-up and checkout outcomes by classification, currency, and method"),
		)
		t.otelCrypCount, _ = meter.Int64Counter(
			"hanzo.commerce.crypto.events",
			metric.WithDescription("Native crypto deposit lifecycle events by event, chain, and token"),
		)
		t.otelLagGauge, _ = meter.Int64Gauge(
			"hanzo.commerce.crypto.cursor_lag_blocks",
			metric.WithDescription("Deposit watcher RPC cursor lag in blocks per chain and token"),
		)
		t.otelStallCnt, _ = meter.Int64Counter(
			"hanzo.commerce.crypto.watcher_stalled_alerts",
			metric.WithDescription("Stalled deposit watcher or SLA-breached confirming deposit alerts"),
		)
	})
}

// ClassifyProcessorOutcome maps processor PaymentIntent status, cancellation_reason,
// last_payment_error code, currency, and payment method into a canonical forensic class.
func ClassifyProcessorOutcome(status, cancellationReason, lastErrorCode, currency, method string) string {
	st := strings.ToLower(strings.TrimSpace(status))
	cr := strings.ToLower(strings.TrimSpace(cancellationReason))
	ec := strings.ToLower(strings.TrimSpace(lastErrorCode))
	cur := strings.ToLower(strings.TrimSpace(currency))
	meth := strings.ToLower(strings.TrimSpace(method))

	if ec != "" {
		switch {
		case strings.Contains(ec, "authentication") || strings.Contains(ec, "three_d_secure"):
			return "3ds_authentication_failed"
		case strings.Contains(ec, "currency") || strings.Contains(ec, "payment_method_unactivated") || strings.Contains(ec, "unsupported"):
			return "currency_method_unsupported"
		case strings.Contains(ec, "declined") || strings.Contains(ec, "insufficient_funds") || strings.Contains(ec, "expired_card") || strings.Contains(ec, "incorrect_cvc"):
			return "card_declined"
		default:
			return "processor_error"
		}
	}

	switch st {
	case "succeeded":
		return "succeeded"
	case "canceled", "cancelled":
		switch cr {
		case "requested_by_customer":
			return "customer_canceled"
		case "fraudulent":
			return "fraud_blocked"
		case "duplicate":
			return "duplicate_intent"
		case "automatic":
			return "expired_intent"
		default:
			return "checkout_abandoned"
		}
	case "requires_action", "pending":
		if meth == "crypto" || meth == "stablecoin" {
			return "awaiting_crypto_checkout"
		}
		return "awaiting_customer_action"
	case "requires_payment_method", "created":
		if cur != "" && cur != "usd" && (meth == "crypto" || meth == "stablecoin") {
			return "currency_method_unsupported"
		}
		return "awaiting_payment_method"
	case "processing":
		return "processing"
	default:
		return cleanLabel(st, "unknown")
	}
}

// RecordProcessorOutcome increments processor outcome metrics safely without sensitive labels.
func RecordProcessorOutcome(currency, method, classification string) {
	cur := cleanLabel(currency, "usd")
	meth := cleanLabel(method, "auto")
	cls := cleanLabel(classification, "unknown")

	railTelemetry.initOTel()
	if railTelemetry.otelProcCount != nil {
		railTelemetry.otelProcCount.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("currency", cur),
			attribute.String("method", meth),
			attribute.String("classification", cls),
		))
	}
	key := cls + ":" + cur + ":" + meth
	railTelemetry.mu.Lock()
	railTelemetry.processorOutcomes[key]++
	railTelemetry.mu.Unlock()
}

// RecordCryptoEvent increments native crypto deposit lifecycle counters safely.
func RecordCryptoEvent(event, chain, token string) {
	ev := cleanLabel(event, "unknown")
	ch := cleanLabel(chain, "unknown")
	tk := cleanLabel(token, "unknown")

	railTelemetry.initOTel()
	if railTelemetry.otelCrypCount != nil {
		railTelemetry.otelCrypCount.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("event", ev),
			attribute.String("chain", ch),
			attribute.String("token", tk),
		))
	}
	key := ev + ":" + ch + ":" + tk
	railTelemetry.mu.Lock()
	railTelemetry.cryptoEvents[key]++
	railTelemetry.mu.Unlock()
}

// RailMetricsSnapshot returns a copy of current rail counters and lag gauges.
func RailMetricsSnapshot() RailMetricsSnap {
	railTelemetry.mu.Lock()
	defer railTelemetry.mu.Unlock()
	out := RailMetricsSnap{
		ProcessorOutcomes: make(map[string]int64, len(railTelemetry.processorOutcomes)),
		CryptoEvents:      make(map[string]int64, len(railTelemetry.cryptoEvents)),
		CursorLagBlocks:   make(map[string]uint64, len(railTelemetry.cursorLag)),
		StalledAlerts:     railTelemetry.stalledAlerts,
	}
	for k, v := range railTelemetry.processorOutcomes {
		out.ProcessorOutcomes[k] = v
	}
	for k, v := range railTelemetry.cryptoEvents {
		out.CryptoEvents[k] = v
	}
	for k, v := range railTelemetry.cursorLag {
		out.CursorLagBlocks[k] = v
	}
	return out
}

// CheckDepositWatcherHealth evaluates cursor lag, sync errors, and SLA-breached deposits,
// emitting a deduplicated alert pulse + audit entry when the watcher or a deposit stalls.
func CheckDepositWatcherHealth(ctx context.Context, cfg WatcherHealthConfig) WatcherHealthReport {
	now := cfg.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	sla := cfg.ConfirmingSLA
	if sla <= 0 {
		sla = defaultConfirmingSLA
	}
	maxLag := cfg.MaxLagBlocks
	if maxLag == 0 {
		maxLag = defaultMaxLagBlocks
	}

	svc := depositledger.Default()
	st := svc.Status(ctx)
	rep := WatcherHealthReport{
		Running:         st.Running,
		Healthy:         true,
		Assets:          make([]AssetHealth, 0, len(st.Assets)),
		StalledDeposits: []StalledDeposit{},
		Problems:        []string{},
	}

	if cfg.LastSyncError != nil {
		rep.Healthy = false
		rep.Problems = append(rep.Problems, "watcher_sync_error")
	}

	railTelemetry.initOTel()
	for _, a := range st.Assets {
		key := strings.ToLower(a.Chain) + ":" + strings.ToLower(a.Token)
		ah := AssetHealth{
			Chain:     strings.ToLower(a.Chain),
			Token:     strings.ToLower(a.Token),
			Contract:  canonicalContract(a.Chain, a.Token, a.Contract),
			Endpoint:  a.Endpoint,
			LastBlock: a.LastBlock,
		}
		if head, ok := cfg.HeadBlocks[key]; ok && head > 0 {
			ah.HeadBlock = head
			if head > a.LastBlock {
				ah.LagBlocks = head - a.LastBlock
			}
			if ah.LagBlocks > maxLag {
				ah.Stalled = true
				ah.Reason = fmt.Sprintf("cursor lags head by %d blocks (max %d)", ah.LagBlocks, maxLag)
			}
		}
		if !ah.Stalled && cfg.LastSyncError != nil && !strings.HasPrefix(a.Contract, "kraken:") {
			ah.Stalled = true
			ah.Reason = "watcher sync error stalled cursor advancement"
		}
		railTelemetry.mu.Lock()
		railTelemetry.cursorLag[key] = ah.LagBlocks
		railTelemetry.prevBlock[key] = a.LastBlock
		railTelemetry.prevBlockAt[key] = now
		railTelemetry.mu.Unlock()

		if railTelemetry.otelLagGauge != nil {
			railTelemetry.otelLagGauge.Record(ctx, int64(ah.LagBlocks), metric.WithAttributes(
				attribute.String("chain", ah.Chain),
				attribute.String("token", ah.Token),
			))
		}
		if ah.Stalled {
			rep.Healthy = false
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s: %s", key, ah.Reason))
		}
		rep.Assets = append(rep.Assets, ah)
	}

	// Scan intents across orgs for confirming (or sighted pending) deposits exceeding SLA.
	orgs := make([]*organization.Organization, 0)
	if _, err := organization.Query(commercedatastore.New(context.Background())).GetAll(&orgs); err == nil {
		for _, o := range orgs {
			name := strings.TrimSpace(o.Name)
			if name == "" {
				continue
			}
			db := commercedatastore.New(commercensctx.WithNamespace(context.Background(), name))
			rows := make([]*cryptopaymentintent.CryptoPaymentIntent, 0)
			keys, qerr := cryptopaymentintent.Query(db).GetAll(&rows)
			if qerr != nil {
				continue
			}
			for idx, in := range rows {
				intentID := in.Id()
				if intentID == "" && idx < len(keys) {
					intentID = db.EncodeKey(keys[idx])
				}
				sightedPending := in.Status == cryptopaymentintent.Pending && strings.TrimSpace(in.TxHash) != ""
				if in.Status != cryptopaymentintent.Confirming && !sightedPending {
					continue
				}
				refTime := in.UpdatedAt
				if refTime.IsZero() {
					refTime = in.CreatedAt
				}
				if refTime.IsZero() {
					continue
				}
				age := now.Sub(refTime)
				if age < sla {
					continue
				}
				rep.Healthy = false
				sd := StalledDeposit{
					Org:      name,
					IntentID: intentID,
					Chain:    string(in.Chain),
					Token:    in.Token,
					Status:   string(in.Status),
					AgeSec:   int64(age.Seconds()),
					Reason:   fmt.Sprintf("deposit %s in %s for %ds exceeds %s SLA", intentID, in.Status, int64(age.Seconds()), sla),
				}
				rep.StalledDeposits = append(rep.StalledDeposits, sd)
				rep.Problems = append(rep.Problems, sd.Reason)
			}
		}
	}

	if !rep.Healthy {
		emitWatcherStallAlert(ctx, cfg, rep, now)
	}
	return rep
}

func emitWatcherStallAlert(ctx context.Context, cfg WatcherHealthConfig, rep WatcherHealthReport, now time.Time) {
	window := cfg.AlertWindowKey
	if window == "" {
		window = now.UTC().Format("2006-01-02T15")
	}
	pods := cfg.Pods
	if pods == nil {
		pods = coord.Default()
	}
	if pods != nil {
		if first, err := pods.Once(ctx, "commerce:crypto_watcher_stall", window, 2*time.Hour); err == nil && !first {
			return
		}
	}

	railTelemetry.mu.Lock()
	railTelemetry.stalledAlerts++
	railTelemetry.mu.Unlock()
	if railTelemetry.otelStallCnt != nil {
		railTelemetry.otelStallCnt.Add(ctx, 1)
	}

	summary := strings.Join(rep.Problems, "; ")
	if len(summary) > 512 {
		summary = summary[:512]
	}
	if cfg.Audit != nil {
		detail, _ := json.Marshal(map[string]any{
			"running":         rep.Running,
			"stalledDeposits": len(rep.StalledDeposits),
			"problems":        rep.Problems,
		})
		_, _ = cfg.Audit.Append(ctx, audit.Record{
			Actor:    audit.Actor{Org: "admin", Sub: "commerce"},
			Action:   "billing.crypto.watcher_stalled",
			Resource: audit.Resource{Type: "crypto_watcher", ID: window},
			Auth:     audit.AuthContext{Method: "service"},
			Outcome:  audit.Outcome{Result: "failure", Status: http.StatusServiceUnavailable, Reason: "crypto deposit watcher stalled or SLA exceeded"},
			After:    audit.Redact(detail),
		})
	}
	if err := bus.Announce(bus.Pulse{
		Kind:   "crypto_watcher_stalled",
		Key:    "crypto_watcher/" + window,
		Org:    "admin",
		Title:  "Crypto deposit watcher stalled",
		Detail: summary,
	}); err != nil {
		luxlog.Default().Warn("crypto watcher stall alert pulse failed", "window", window)
	}
}

var (
	watcherMonitorMu     sync.Mutex
	watcherMonitorCancel context.CancelFunc
	watcherMonitorDone   chan struct{}
)

func stopWatcherHealthMonitor() {
	watcherMonitorMu.Lock()
	cancel, done := watcherMonitorCancel, watcherMonitorDone
	watcherMonitorCancel, watcherMonitorDone = nil, nil
	watcherMonitorMu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func startWatcherHealthMonitor(parent context.Context, lg luxlog.Logger, trail *audit.Recorder) {
	stopWatcherHealthMonitor()
	if testing.Testing() {
		// Package tests swap global commerce/datastore singletons per test and exercise
		// CheckDepositWatcherHealth synchronously.
		return
	}
	watcherMonitorMu.Lock()
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	watcherMonitorCancel = cancel
	watcherMonitorDone = done
	watcherMonitorMu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				healAllWatchedCollisions(ctx)
				rep := CheckDepositWatcherHealth(ctx, WatcherHealthConfig{
					Now:   now.UTC(),
					Audit: trail,
				})
				if !rep.Healthy {
					lg.Warn("crypto deposit watcher health degraded",
						"running", rep.Running,
						"stalled_deposits", len(rep.StalledDeposits),
						"problems", len(rep.Problems))
				}
			}
		}
	}()
}

func cleanLabel(s, fallback string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 64 {
		return fallback
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' {
			continue
		}
		return fallback
	}
	return s
}
