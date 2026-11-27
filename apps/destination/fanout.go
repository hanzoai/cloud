package destination

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/cloud/client"
	eventpeer "github.com/hanzoai/cloud/client/event"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/text"
)

// fanout.go forwards one org's events to that org's enabled destinations. For
// each one it:
//
//   - TRANSLATES once (translate.go), and every destination renders from the
//     same conversions;
//   - RECALLS the person's touch (touch.go): a browser event records its click,
//     a sale commerce stated reads it back;
//   - ADMITS per destination: an ad platform is sent only the moments it
//     optimises on, and only with the visitor's consent;
//   - DELIVERS with a retry ladder, and records every attempt on the org's own
//     event plane as a destination_sent fact.
//
// The sink has no caller to answer, so every failure is logged and recorded,
// never returned, and one destination failing never stops another.

// job is one batch bound for one destination of one org.
type job struct {
	org    string
	dest   Destination
	cfg    Config
	secret string
	batch  []Conversion
}

// plan translates org's events, recalls their touches, and says what each of the
// org's enabled destinations is sent. It reads the store and dials nothing, so
// the listener runs it in line and hands only real sends to workers: a page view
// in an org with nothing connected costs a query, never a worker.
func plan(s *cloud.Service[state], org string, evs []event.SinkEvent) []job {
	if len(evs) == 0 {
		return nil
	}
	ctx := context.Background()
	rows, err := s.State.store.ListEnabled(ctx, org)
	if err != nil {
		s.Log.Warn("destinations fan-out store read failed", "org", org, "err", err)
		return nil
	}
	rows = withBrandDefaults(ctx, s, org, rows)
	// A touch is kept only where it can be used: an org with a destination, or
	// the brand org, whose sales commerce states for every customer.
	if len(rows) == 0 && org != s.Brand {
		return nil
	}
	batch := make([]Conversion, 0, len(evs))
	for _, ev := range evs {
		cv := Translate(ev)
		if err := s.State.store.recall(ctx, org, &cv); err != nil {
			s.Log.Warn("destinations touch failed", "org", org, "err", err)
		}
		stale(&cv)
		batch = append(batch, cv)
	}
	var out []job
	for _, row := range rows {
		dest, ok := s.State.dests[row.Platform]
		if !ok {
			continue
		}
		sent := admit(dest, row.Config, batch)
		if len(sent) == 0 {
			continue
		}
		secret, serr := resolveSecret(context.Background(), s, org, dest, row.Config)
		if serr != nil {
			s.Log.Debug("destinations fan-out skipped (no credential)", "platform", row.Platform, "org", org)
			continue
		}
		out = append(out, job{org: org, dest: dest, cfg: row.Config, secret: secret, batch: sent})
	}
	return out
}

// consume plans org's events and delivers them in line.
func consume(s *cloud.Service[state], org string, evs []event.SinkEvent) {
	for _, j := range plan(s, org, evs) {
		deliver(context.Background(), s, j)
	}
}

// firstParty are Hanzo's own lenses, with the host each reads this very plane
// from. They take the whole stream, but only when the org runs its own: the
// hosted lens IS this plane, so a copy sent there would be every event counted
// twice, and Insights' capture path lands back on /v1/event and would forward
// the copy again, forever.
var firstParty = map[string]string{insightsID: insightsHost}

// forwarded marks an event this app sent to a lens, so a copy that comes back
// round the plane is never forwarded again.
const forwarded = "$forwarded"

// adStandard are the moments an ad platform optimises on. A page view is not
// one of them: the platform's own tag already counts it, and a second copy
// from here would be counted again. A custom event names nothing a platform
// can act on.
var adStandard = map[StandardEvent]bool{
	EventViewContent: true, EventViewList: true, EventSearch: true, EventLead: true, EventSignUp: true,
	EventStartCheckout: true, EventAddToCart: true, EventPurchase: true, EventContact: true,
}

// catalogKey is the column of the platform table each destination reads.
var catalogKey = map[string]string{ga4ID: "ga4", metaID: "meta", linkedinID: "linkedin", xID: "x", tiktokID: "tiktok", openaiID: "openai"}

// namesOn is what a destination calls a conversion, in the order it is sent. The
// platform table of @hanzo/events decides for an event it lists, so the browser tag
// and this forwarder agree name for name; for an event it does not list (the
// schema.org / GA4 action names) the normalized standard event's name is used.
func namesOn(id string, cv Conversion, byStandard map[StandardEvent]string) []string {
	if event.Listed(cv.Name) {
		return event.Platform(cv.Name, catalogKey[id])
	}
	if n := byStandard[cv.Standard]; n != "" {
		return []string{n}
	}
	return nil
}

// sends reports whether a destination is sent this conversion at all. An event the
// platform table lists is sent wherever the table names it. One it does not list
// is sent only when it is a moment an ad platform optimises on (adStandard), which
// keeps a page view and a custom event out.
func sends(id string, cv Conversion) bool {
	if event.Listed(cv.Name) {
		switch id {
		case ga4ID, metaID, tiktokID, linkedinID, xID, openaiID:
			return len(event.Platform(cv.Name, catalogKey[id])) > 0
		case googleadsID:
			return googleAction[cv.Standard] != ""
		}
		return adStandard[cv.Standard]
	}
	switch id {
	case googleadsID:
		return googleAction[cv.Standard] != ""
	case ga4ID:
		return adStandard[cv.Standard] || cv.Standard == EventSelect || cv.Standard == EventLogin
	}
	return adStandard[cv.Standard]
}

// testTraffic reports whether a destination can take a test-mode order as test
// traffic: GA4 files it under debug_mode, Meta under a Test Event Code the org
// set. Every other platform has no test channel here, so it is sent none, and a
// sandbox order never reaches an ad account as a conversion.
func testTraffic(id string, cfg Config) bool {
	switch id {
	case ga4ID:
		return true
	case metaID:
		return cfg.get("testEventCode") != ""
	}
	return false
}

// admit is what one destination is sent out of a batch.
//
// Consent decides first. Marketing covers every ad platform; GA4 is measurement,
// so it is Analytics. Then GA4 is sent only what its own tag did not already
// deliver: GA4 deduplicates nothing but a purchase's transaction_id, so a copy
// of an event the page's gtag sent would be counted twice. Meta deduplicates on
// event_id, so it is sent every conversion and keeps one.
//
// MONEY IS STATED, NEVER TYPED. A browser can post any value under any name —
// a publishable key is in every page, and a person's session is in their own
// hands — so only a conversion someone accountable stated carries money to a
// third party: a peer app on the plane (commerce's sales, the CRM's leads) or
// the org's own secret key (its backend). Anything else is sent with no value,
// and a purchase not at all: the sales a platform bids on are the ones commerce
// states.
func admit(dest Destination, cfg Config, batch []Conversion) []Conversion {
	if hosted, ok := firstParty[dest.ID()]; ok {
		if h := strings.TrimRight(cfg.get("host"), "/"); h == "" || h == strings.TrimRight(hosted, "/") {
			return nil
		}
		return batch
	}
	out := make([]Conversion, 0, len(batch))
	for _, cv := range batch {
		switch {
		case !stated(cv) && (cv.Standard == EventPurchase || event.CarriesMoney(cv.Name)):
			continue
		case cv.Sandbox && !testTraffic(dest.ID(), cfg):
			continue
		case dest.ID() == ga4ID:
			if !cv.Consent.Analytics || cv.Tags["ga"] || !sends(ga4ID, cv) {
				continue
			}
		case !cv.Consent.Marketing || !sends(dest.ID(), cv):
			continue
		}
		if !stated(cv) || cv.Sandbox {
			cv = priceless(cv)
		}
		out = append(out, firewall(cv))
	}
	return out
}

// stated reports a conversion someone accountable stated: a peer app on the
// plane wire, or the org's own secret key.
func stated(cv Conversion) bool { return cv.Source == sourcePlane || cv.Vouched }

// priceless is a conversion with every figure a sender typed taken off it.
func priceless(cv Conversion) Conversion {
	cv.Value = 0
	if len(cv.Items) > 0 {
		items := make([]Item, len(cv.Items))
		for i, it := range cv.Items {
			it.Price = 0
			items[i] = it
		}
		cv.Items = items
	}
	return cv
}

// ladder is the wait before each retry. A transport failure, a 429 or a 5xx is
// tried again three times over about twenty seconds; any other refusal is the
// platform's answer and is final. With sendTimeout that holds a worker for at
// most about a minute.
var ladder = []time.Duration{time.Second, 4 * time.Second, 16 * time.Second}

// pause waits between attempts, or until ctx ends; a test makes it instant.
var pause = func(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// lensTimeout bounds the one attempt a self-hosted lens gets: its host is the
// org's own choosing, and a host that answers slowly holds a worker for nothing.
const lensTimeout = 5 * time.Second

// deliver sends one job, retrying what can succeed on a second try, and records
// every attempt. A self-hosted lens gets one short attempt. ctx ending (the app
// stopping) ends the retries, never an attempt under way.
func deliver(ctx context.Context, s *cloud.Service[state], j job) {
	defer func() {
		if r := recover(); r != nil {
			s.Log.Warn("destination send panic", "platform", j.dest.ID(), "org", j.org, "err", r)
		}
	}()
	_, lens := firstParty[j.dest.ID()]
	budget, tries := sendTimeout, len(ladder)
	if lens {
		budget, tries = lensTimeout, 0
	}
	for attempt := 1; ; attempt++ {
		// The attempt outlives the app stopping, so a send already under way is
		// finished rather than cut off; stopping only ends the retries.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
		res, err := j.dest.Send(sctx, j.cfg, j.secret, j.batch)
		cancel()
		again := err != nil && retryable(err) && attempt <= tries && ctx.Err() == nil
		audit(s, j.org, j.dest.ID(), j.batch, attempt, res, err, again)
		if !again {
			return
		}
		d := ladder[attempt-1]
		pause(ctx, d+rand.N(d/4+1))
		if ctx.Err() != nil {
			audit(s, j.org, j.dest.ID(), j.batch, attempt+1, Result{}, errStopped, false)
			return
		}
	}
}

// sendError is a platform's refusal or a transport failure, carrying the HTTP
// status (0 when nothing answered) so a delivery knows whether trying again can
// help. Its text never carries a credential (send.go).
type sendError struct {
	status int
	msg    string
}

func (e *sendError) Error() string { return e.msg }

func retryable(err error) bool {
	var se *sendError
	if !errors.As(err, &se) {
		return false
	}
	return se.status == 0 || se.status == http.StatusTooManyRequests || se.status >= 500
}

// auditProduct is the surface a delivery record is filed under.
const auditProduct = "destination"

// capture records one fact on the org's event plane. A variable so a test sees
// what leaves; production never reassigns it.
var capture = eventpeer.EventCapture

// audit records one attempt: a log line, and one destination_sent fact per
// conversion on the org's own event plane, where the org's lenses and the
// warehouse keep it beside the event it forwarded. status is sent, retrying or
// failed.
func audit(s *cloud.Service[state], org, platform string, batch []Conversion, attempt int, res Result, err error, again bool) {
	status := "sent"
	switch {
	case err != nil && again:
		status = "retrying"
	case err != nil:
		status = "failed"
	}
	if err != nil {
		s.Log.Warn("destination send", "platform", platform, "org", org, "attempt", attempt, "status", status, "err", err)
	} else {
		s.Log.Info("destination send", "platform", platform, "org", org, "attempt", attempt, "sent", res.Sent)
	}
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), 5*time.Second)
	defer cancel()
	for _, cv := range batch {
		attrs := []client.Signal{
			{Name: "platform", Value: platform},
			{Name: "event", Value: cv.Name},
			{Name: "event_id", Value: cv.EventID},
			{Name: "attempt", Value: strconv.Itoa(attempt)},
			{Name: "status", Value: status},
		}
		if err != nil {
			attrs = append(attrs, client.Signal{Name: "error", Value: text.Cut(err.Error(), 256)})
		}
		if _, rerr := capture(ctx, &client.EventIn{
			Name:       "destination_sent",
			Product:    auditProduct,
			Subject:    cv.User.ExternalID,
			Attributes: attrs,
		}); rerr != nil {
			s.Log.Debug("destination send not recorded", "org", org, "err", rerr)
			return
		}
	}
}

// resolveSecret resolves a destination's primary credential: its own KMS-sealed
// secret first, else the Spec.Fallback integrations token (Meta CAPI reuses the
// meta_ads OAuth token). Returns an error when neither resolves — the fan-out then
// skips the destination (fail-soft) and the console shows live=false.
func resolveSecret(ctx context.Context, s *cloud.Service[state], org string, dest Destination, _ Config) (string, error) {
	spec := dest.Spec()
	if len(spec.Secrets) > 0 {
		if v, err := kmsGet(ctx, s, org, dest.ID(), spec.Secrets[0]); err == nil && len(v) > 0 {
			return string(v), nil
		}
	}
	if spec.Fallback != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if v, err := provider.TokenFor(ctx, org, spec.Fallback, "access_token"); err == nil && len(v) > 0 {
			return string(v), nil
		}
	}
	if org == "hanzo" && dest.ID() == linkedinID {
		if tok := strings.TrimSpace(env.Or("LINKEDIN_CONVERSIONS_ACCESS_TOKEN", "")); tok != "" {
			return tok, nil
		}
	}
	if org == "hanzo" && dest.ID() == openaiID {
		if tok := strings.TrimSpace(env.Or("OPENAI_CONVERSIONS_API_KEY", env.Or("OPENAI_PIXEL_API_KEY", ""))); tok != "" {
			return tok, nil
		}
	}
	return "", fmt.Errorf("no credential for %s", dest.ID())
}

// withBrandDefaults seeds Hanzo's non-secret LinkedIn and OpenAI destination configs for org == "hanzo"
// whenever a credential resolves (via KMS, OAuth integration, or env) and no explicit row has been
// persisted yet, or fills any unset keys on an existing row.
func withBrandDefaults(ctx context.Context, s *cloud.Service[state], org string, rows []Row) []Row {
	if org != "hanzo" {
		return rows
	}
	openaiDefault := Config{}
	if pid := strings.TrimSpace(env.Or("OPENAI_PIXEL_ID", "TXKHZduRxZZGp4B1ysfiwm")); pid != "" {
		openaiDefault["pixelId"] = pid
	}
	for _, entry := range []struct {
		id       string
		defaults Config
	}{
		{linkedinID, hanzoLinkedInDefaults},
		{openaiID, openaiDefault},
	} {
		dest, ok := s.State.dests[entry.id]
		if !ok {
			continue
		}
		found := false
		for i := range rows {
			if rows[i].Platform == entry.id {
				found = true
				if rows[i].Config == nil {
					rows[i].Config = Config{}
				}
				for k, v := range entry.defaults {
					if rows[i].Config.get(k) == "" {
						rows[i].Config[k] = v
					}
				}
				break
			}
		}
		if !found {
			if _, err := resolveSecret(ctx, s, org, dest, entry.defaults); err == nil {
				rows = append(rows, Row{Org: org, Platform: entry.id, Enabled: true, Config: entry.defaults})
			}
		}
	}
	return rows
}
