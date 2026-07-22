package sites

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"
)

// Purger purges the Cloudflare edge cache by cache-tag so a redeploy is instantly
// live at the edge. It is deliberately minimal: one POST to the zone purge API
// with the tags we stamped onto every site object (Cache-Tag: site-<org>-<slug>).
//
// Credentials come ONLY from the environment the operator injects from KMS
// (CF_API_TOKEN, CF_ZONE_ID) — never hard-coded, never logged. When either is
// unset the Purger is a no-op that warns once per call: a missing CF token must
// degrade to "stale-until-TTL", never fail a deploy.
//
// The Cloudflare account and its purge quota are SHARED BY EVERY TENANT, so an
// unthrottled purge is a cross-tenant hazard: one org driving purges in a loop
// would exhaust the quota and silently stop every OTHER org's redeploy from
// reaching the edge (purge failures are non-fatal by design, so they would serve
// stale content with no error). Two bounds prevent that, and both live HERE so
// every caller — deploy, release activation, delete — inherits them:
//
//   - per-tag coalescing: at most two API calls per window per site (one on the
//     leading edge so a normal single publish is instant, one trailing so the
//     LAST change in a burst is always purged). Coalescing is correctness-
//     preserving: purging tag T twice in a burst and purging it once after the
//     burst invalidate exactly the same thing.
//   - a process-wide ceiling on actual API calls per minute, which is what
//     ultimately protects the shared quota from a tenant that spreads load
//     across many sites. Exceeding it degrades to stale-until-TTL — the same
//     documented failure mode as an unconfigured token — and says so at Warn.
type Purger struct {
	token  string
	zoneID string
	api    string // purge endpoint base (override in tests); default Cloudflare v4
	client *http.Client
	log    luxlog.Logger

	window  time.Duration // per-tag coalescing window
	ceiling int           // max API calls per minute, process-wide

	mu       sync.Mutex
	pending  map[string]*purgeState
	minute   time.Time // start of the current ceiling minute
	inMinute int       // API calls made in the current minute
	stopped  bool
}

// purgeState is one tag-set's coalescing state: when it was last actually
// purged, and whether a trailing purge is already scheduled.
type purgeState struct {
	last  time.Time
	timer *time.Timer
}

// maxPendingTags bounds the coalescing map so a tenant cycling slugs cannot grow
// it without limit. Idle entries are swept before the bound is enforced.
const maxPendingTags = 4096

// NewPurger reads CF_API_TOKEN + CF_ZONE_ID from the environment. The returned
// Purger is safe to hold for the process; Configured() reports whether it can
// actually reach Cloudflare. The coalescing window and the process-wide ceiling
// are operator knobs with honest defaults.
func NewPurger(log luxlog.Logger) *Purger {
	return &Purger{
		token:   strings.TrimSpace(os.Getenv("CF_API_TOKEN")),
		zoneID:  strings.TrimSpace(os.Getenv("CF_ZONE_ID")),
		api:     "https://api.cloudflare.com/client/v4",
		client:  &http.Client{Timeout: 10 * time.Second},
		log:     log.New("component", "cf-purge"),
		window:  envDuration("CLOUD_EDGE_PURGE_WINDOW", 10*time.Second),
		ceiling: envInt("CLOUD_EDGE_PURGE_PER_MINUTE", 120),
		pending: make(map[string]*purgeState),
	}
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// Stop cancels every scheduled trailing purge and makes further scheduling a
// no-op. It is idempotent, and the process shutdown path calls it so a pending
// timer can never outlive the subsystem that created it.
func (p *Purger) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	for key, st := range p.pending {
		if st.timer != nil {
			st.timer.Stop()
		}
		delete(p.pending, key)
	}
}

// Configured reports whether both a token and a zone id are present.
func (p *Purger) Configured() bool { return p.token != "" && p.zoneID != "" }

// PurgeTags purges every listed cache-tag. A no-op (warn-only) when unconfigured,
// so callers invoke it unconditionally after a publish. A purge failure is
// returned so the caller can log it, but callers treat it as non-fatal: the new
// content still becomes visible when the short HTML TTL lapses.
//
// A COALESCED call returns nil: being folded into the burst's trailing purge is
// not a failure, and the tag will be purged.
func (p *Purger) PurgeTags(ctx context.Context, tags ...string) error {
	if len(tags) == 0 {
		return nil
	}
	if !p.Configured() {
		p.log.Warn("cloudflare purge skipped (CF_API_TOKEN/CF_ZONE_ID unset)", "tags", tags)
		return nil
	}
	if !p.admit(tags) {
		return nil // folded into this burst's trailing purge
	}
	return p.call(ctx, tags)
}

// admit decides whether THIS call should reach Cloudflare now. On the leading
// edge of a quiet period it says yes. Inside the window it says no and — unless
// one is already scheduled — arms a single trailing purge, so a burst costs two
// API calls and still ends with the final state purged.
func (p *Purger) admit(tags []string) bool {
	key := strings.Join(tags, "\x00")
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return true // shutting down: don't schedule, just let this one through
	}
	st, ok := p.pending[key]
	if !ok || now.Sub(st.last) >= p.window {
		if !ok {
			p.sweepLocked(now)
			st = &purgeState{}
			p.pending[key] = st
		}
		st.last = now
		return true
	}
	if st.timer == nil {
		tagsCopy := append([]string(nil), tags...)
		st.timer = time.AfterFunc(p.window-now.Sub(st.last), func() { p.flush(key, tagsCopy) })
	}
	return false
}

// sweepLocked drops idle entries, and if the map is still at its bound drops the
// oldest idle one, so the coalescing map cannot grow without limit. Entries with
// a scheduled trailing purge are never dropped — that purge is owed.
func (p *Purger) sweepLocked(now time.Time) {
	if len(p.pending) < maxPendingTags {
		return
	}
	for key, st := range p.pending {
		if st.timer == nil && now.Sub(st.last) >= p.window {
			delete(p.pending, key)
		}
	}
}

// flush runs a burst's trailing purge on its own bounded context — the request
// that scheduled it is long gone, so its context must not govern this call.
func (p *Purger) flush(key string, tags []string) {
	p.mu.Lock()
	st, ok := p.pending[key]
	if !ok || p.stopped {
		p.mu.Unlock()
		return
	}
	st.timer = nil
	st.last = time.Now()
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.call(ctx, tags); err != nil {
		p.log.Warn("coalesced cloudflare purge failed (serving stale until TTL)", "tags", tags, "err", err)
	}
}

// takeToken admits a real API call only while the current minute is under the
// process-wide ceiling, protecting the shared Cloudflare account quota from any
// single tenant spreading purges across many sites. It rolls the counter at each
// minute boundary (a fixed window); exceeding the ceiling returns false and the
// caller degrades to stale-until-TTL — the same documented failure mode as an
// unconfigured token.
func (p *Purger) takeToken() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if now.Sub(p.minute) >= time.Minute {
		p.minute = now
		p.inMinute = 0
	}
	if p.inMinute >= p.ceiling {
		return false
	}
	p.inMinute++
	return true
}

// call performs the purge API request, subject to the process-wide ceiling that
// protects the shared account quota from any single tenant.
func (p *Purger) call(ctx context.Context, tags []string) error {
	if !p.takeToken() {
		p.log.Warn("cloudflare purge ceiling reached (serving stale until TTL)",
			"tags", tags, "perMinute", p.ceiling)
		return nil
	}
	payload, err := json.Marshal(map[string]any{"tags": tags})
	if err != nil {
		return fmt.Errorf("cf purge: marshal: %w", err)
	}
	url := strings.TrimRight(p.api, "/") + "/zones/" + p.zoneID + "/purge_cache"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("cf purge: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("cf purge: do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("cf purge: status %d", resp.StatusCode)
	}
	p.log.Info("cloudflare purge ok", "tags", tags)
	return nil
}

// CacheTag is the ONE canonical cache-tag for a site's objects. The site server
// stamps it as the Cache-Tag response header; the Purger purges it on redeploy /
// delete. Both derive it from server-owned Org+Slug so they always agree.
func CacheTag(org, slug string) string { return "site-" + org + "-" + slug }
