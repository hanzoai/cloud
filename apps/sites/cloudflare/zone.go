package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// The zone id is DISCOVERED, not configured.
//
// It was the last thing this edge needed from the process environment, and it was
// the odd one out: the token now comes from the Cloudflare integration, which
// holds an account — and a token that spans an account can be asked which zone
// serves a given name. `Zone:Read` is already in the integration's scope set, so
// nothing new is granted to make this work. A second env var to carry a fact the
// credential can answer is a fact that can disagree with the credential.
//
// LAZY, and that is the whole design constraint. Resolving at construction would
// turn a pure constructor into one that blocks on a network call and can fail, in
// a composition root that runs at process start — so the lookup happens on the
// first purge that needs it and the answer is kept for the process. A publish is
// the only thing that wants a zone id, so nothing pays for this until something
// does.
//
// FAIL SOFT, like everything else here. A lookup that cannot answer leaves the
// zone empty, and an empty zone is the already-documented unconfigured posture:
// serve stale until the TTL, say so at Warn, never fail the deploy that asked.

// zoneName is the apex whose zone fronts the site plane. It is the one fact this
// package still takes from the environment, and it is a NAME rather than an id —
// a name is a thing an operator can read and check, and CLOUD_SITES_APEX already
// carries it everywhere else in the estate for exactly this zone.
func zoneName() string {
	if v := strings.TrimSpace(getenv("CLOUD_SITES_APEX")); v != "" {
		return v
	}
	return "hanzo.app"
}

// zone returns the zone id, discovering it once if it was not supplied.
//
// The caller holds no lock: discovery is guarded here, and a second purge that
// arrives mid-lookup simply waits for the same answer rather than issuing a
// second identical request against a shared quota.
func (p *Edge) zone(ctx context.Context) string {
	p.zoneMu.Lock()
	defer p.zoneMu.Unlock()
	if p.zoneID != "" || p.token == "" || p.zoneTried {
		return p.zoneID
	}
	p.zoneTried = true // one attempt per process; a retry loop here would hammer a shared quota

	name := zoneName()
	id, err := p.lookupZone(ctx, name)
	if err != nil {
		p.log.Warn("cloudflare zone lookup failed; publishes are live only after the edge TTL",
			"zone", name, "err", err)
		return ""
	}
	if id == "" {
		p.log.Warn("cloudflare account holds no zone by that name; publishes are live only after the edge TTL",
			"zone", name)
		return ""
	}
	p.zoneID = id
	p.log.Info("cloudflare zone discovered", "zone", name, "id", id)
	return id
}

// lookupZone asks which zone serves a name. It reads the FIRST match: a
// Cloudflare account cannot hold two zones with the same name, so a filtered list
// is either empty or a single answer.
func (p *Edge) lookupZone(ctx context.Context, name string) (string, error) {
	endpoint := strings.TrimRight(p.api, "/") + "/zones?name=" + url.QueryEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("cf zones: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("cf zones: do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("cf zones: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// The body carries Cloudflare's own reason — a token without Zone:Read
		// says so here, and an operator should read that rather than guess.
		return "", fmt.Errorf("cf zones: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out struct {
		Success bool `json:"success"`
		Result  []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("cf zones: decode: %w", err)
	}
	if !out.Success || len(out.Result) == 0 {
		return "", nil
	}
	return out.Result[0].ID, nil
}
