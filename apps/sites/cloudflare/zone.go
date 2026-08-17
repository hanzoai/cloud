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

// zoneNames are the apexes whose zones front the site plane — plural, and that
// is the whole correction.
//
// A published site is reachable on TWO apexes, not one. `<slug>.hanzo.app` is the
// site plane's own, and CLOUD_SITES_FIRSTPARTY_APEX (hanzo.ai) serves our
// first-party sites off an allowlist pinned to one org — sites.Server carries
// both fields for exactly this reason. Purging only the first left every
// first-party host serving whatever it had cached: measured on hanzo.ai and
// cloud.hanzo.ai, both HIT with the pre-deploy bytes while the origin had the new
// ones, and a purge that returned 200 the whole time. Right call, wrong zone.
//
// Names rather than ids, still: a name is a thing an operator can read and check,
// and both already exist in the environment for this exact purpose. Deduplicated
// because a deployment may point both at one apex, and purging a zone twice is
// two calls against a quota shared by every tenant.
func zoneNames() []string {
	out := make([]string, 0, 2)
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if v := getenv("CLOUD_SITES_APEX"); strings.TrimSpace(v) != "" {
		add(v)
	} else {
		add("hanzo.app")
	}
	if v := getenv("CLOUD_SITES_FIRSTPARTY_APEX"); strings.TrimSpace(v) != "" {
		add(v)
	} else {
		add("hanzo.ai")
	}
	return out
}

// zones returns every zone id a purge must reach, discovering them once.
//
// An explicitly supplied zone (CF_ZONE_ID) is honoured as the whole answer: an
// operator who pins one is saying which zone they mean, and discovering more
// behind their back would purge zones they did not ask for.
//
// The caller holds no lock: discovery is guarded here, and a second purge that
// arrives mid-lookup waits for the same answer rather than issuing a second
// identical request against a shared quota. One attempt per process either way —
// a retry loop here hammers a quota shared by every tenant.
func (p *Edge) zones(ctx context.Context) []string {
	p.zoneMu.Lock()
	defer p.zoneMu.Unlock()
	if p.zoneID != "" {
		return []string{p.zoneID}
	}
	if p.token == "" || p.zoneTried {
		return p.zoneIDs
	}
	p.zoneTried = true

	for _, name := range zoneNames() {
		id, err := p.lookupZone(ctx, name)
		switch {
		case err != nil:
			p.log.Warn("cloudflare zone lookup failed; publishes on it are live only after the edge TTL",
				"zone", name, "err", err)
		case id == "":
			// Not an error: a deployment need not own every apex it is
			// configured with, and one it does not own has nothing to purge.
			p.log.Info("cloudflare account holds no zone by that name; skipping it", "zone", name)
		default:
			p.zoneIDs = append(p.zoneIDs, id)
			p.log.Info("cloudflare zone discovered", "zone", name, "id", id)
		}
	}
	if len(p.zoneIDs) == 0 {
		p.log.Warn("cloudflare resolved no zone at all; publishes are live only after the edge TTL")
	}
	return p.zoneIDs
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
