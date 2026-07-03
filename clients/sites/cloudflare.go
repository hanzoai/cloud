package sites

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
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
type Purger struct {
	token  string
	zoneID string
	api    string // purge endpoint base (override in tests); default Cloudflare v4
	client *http.Client
	log    luxlog.Logger
}

// NewPurger reads CF_API_TOKEN + CF_ZONE_ID from the environment. The returned
// Purger is safe to hold for the process; Configured() reports whether it can
// actually reach Cloudflare.
func NewPurger(log luxlog.Logger) *Purger {
	return &Purger{
		token:  strings.TrimSpace(os.Getenv("CF_API_TOKEN")),
		zoneID: strings.TrimSpace(os.Getenv("CF_ZONE_ID")),
		api:    "https://api.cloudflare.com/client/v4",
		client: &http.Client{Timeout: 10 * time.Second},
		log:    log.New("component", "cf-purge"),
	}
}

// Configured reports whether both a token and a zone id are present.
func (p *Purger) Configured() bool { return p.token != "" && p.zoneID != "" }

// PurgeTags purges every listed cache-tag. A no-op (warn-only) when unconfigured,
// so callers invoke it unconditionally after a publish. A purge failure is
// returned so the caller can log it, but callers treat it as non-fatal: the new
// content still becomes visible when the short HTML TTL lapses.
func (p *Purger) PurgeTags(ctx context.Context, tags ...string) error {
	if len(tags) == 0 {
		return nil
	}
	if !p.Configured() {
		p.log.Warn("cloudflare purge skipped (CF_API_TOKEN/CF_ZONE_ID unset)", "tags", tags)
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
