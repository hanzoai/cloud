package destinations

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/analytics"
	"github.com/hanzoai/cloud/clients/integrations"
)

// fanout.go is the clients/analytics fan-out consumer — the sink installed at Mount.
// For each ENABLED destination of an org it translates the accepted batch to
// normalized Conversions and delivers it. It is:
//
//   - BOUNDED. A per-service semaphore caps concurrent fan-out; a saturated system
//     DROPS the batch with a warning rather than growing unbounded HTTP.
//   - FAIL-SOFT. The sink has no caller to return to: a store error, a missing
//     credential, or a per-destination Send failure is logged, never propagated, and
//     never blocks another destination or a later batch. A panic is recovered.
//   - ONE TRANSLATION. The batch is translated once (translate.go) and every
//     destination renders from the same normalized Conversions.

// consume is the analytics.SetSink handler. It runs on the goroutine analytics
// detached, so it may do bounded synchronous work here.
func consume(s *cloud.Service[state], org string, evs []analytics.SinkEvent) {
	defer func() {
		if r := recover(); r != nil {
			s.Log.Warn("destinations fan-out panic", "org", org, "err", r)
		}
	}()
	if len(evs) == 0 {
		return
	}
	// Bound concurrent fan-out; drop (fail-soft) when saturated.
	select {
	case s.State.sem <- struct{}{}:
		defer func() { <-s.State.sem }()
	default:
		s.Log.Warn("destinations fan-out saturated; dropping batch", "org", org, "events", len(evs))
		return
	}

	ctx := context.Background()
	rows, err := s.State.store.ListEnabled(ctx, org)
	if err != nil {
		s.Log.Warn("destinations fan-out store read failed", "org", org, "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}

	// Translate the batch ONCE; every destination renders from the same conversions.
	batch := make([]Conversion, 0, len(evs))
	for _, ev := range evs {
		batch = append(batch, Translate(ev))
	}

	for _, row := range rows {
		dest, ok := s.State.dests[row.Platform]
		if !ok {
			continue
		}
		secret, serr := resolveSecret(s, org, dest, row.Config)
		if serr != nil {
			s.Log.Debug("destinations fan-out skipped (no credential)", "platform", row.Platform, "org", org)
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, sendTimeout)
		res, err := dest.Send(sctx, row.Config, secret, batch)
		cancel()
		if err != nil {
			s.Log.Warn("destination send failed", "platform", row.Platform, "org", org, "err", err)
			continue
		}
		s.Log.Debug("destination forwarded", "platform", row.Platform, "org", org, "sent", res.Sent)
	}
}

// resolveSecret resolves a destination's primary credential: its own KMS-sealed
// secret first, else the Spec.Fallback integrations token (Meta CAPI reuses the
// meta_ads OAuth token). Returns an error when neither resolves — the fan-out then
// skips the destination (fail-soft) and the console shows live=false.
func resolveSecret(s *cloud.Service[state], org string, dest Destination, _ Config) (string, error) {
	spec := dest.Spec()
	if len(spec.Secrets) > 0 && kmsReady(s) {
		if v, err := kmsGet(s, kmsPath(org, dest.ID()), spec.Secrets[0]); err == nil && len(v) > 0 {
			return string(v), nil
		}
	}
	if spec.Fallback != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if v, err := integrations.TokenFor(ctx, org, spec.Fallback, "access_token"); err == nil && len(v) > 0 {
			return string(v), nil
		}
	}
	return "", fmt.Errorf("no credential for %s", dest.ID())
}
