package projects

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/internal/consent"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/locate"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// tags.go — GET /v1/project/tags, the PUBLIC per-site browser-tag config the
// hosted tag (track.js / /v1/event/tag.js) fetches to know which client-side pixels to
// inject. Under /v1/project because the endpoint follows the store it reads, which is
// this app's (HIP-0139 §3.1).
//
// It lives HERE, in the projects app, because a project IS a site and carries its own
// Project.Tags — and this is the process that OWNS the project store. (It first lived in
// the destinations app and read empty in production: destinations runs in a different
// process, so its cross-app reach for the store resolved to nil. The rule the key
// resolver already follows: the endpoint that reads the project store must be served by
// the process that holds it.)
//
// Dual-resolved per site: by the publishable KEY when it is a per-site project key
// (ResolveKey), else by the request HOST (ResolveHost) for an org-level key — so
// hanzo.ai and hanzo.chat inject different pixels under one org. Public + pk--keyed like
// /v1/event(.js); NON-SECRET ids only; FAIL-SAFE to an empty set so a page never breaks.
//
// It also says who is asking (event.Audience), and the set answers for them: a bot
// is sent no tag, the site's own team Google Analytics alone, which the page tags
// internal. A page that sends its signed-in person's IAM bearer is how a member is
// recognised; the bearer is identity, never the key.

type browserTagOut struct {
	Platform string `json:"platform"`
	Type     string `json:"type"`
	ID       string `json:"id"`
	// Events maps one of OUR event names to the platform's own conversion id for it:
	// a Google Ads `AW-…/label`, a LinkedIn conversion rule, an X event id. Those
	// platforms name a conversion by rule and not by event, so an event with no rule
	// there is not sent to it. Stored as `platform:event` keys beside the platform's
	// own id.
	Events map[string]string `json:"events,omitempty"`
}

type tagConfig struct {
	Tags []browserTagOut `json:"tags"`
	// Consent is the rule that binds THIS visitor's third-party tags, by the region the
	// edge placed them in: `eu` (also any visitor no edge placed; before a choice Google
	// Analytics runs cookieless and Meta is held revoked), `us_privacy` (everything runs,
	// the visitor opts out) or `global` (everything runs, with a notice), the categories
	// granted before a choice, and whether the browser refused (Global Privacy Control or
	// Do Not Track: no third party loads, in any region). The tag set is the same
	// under every rule: the page gates it by this. The first-party stream is not
	// governed by it.
	Consent event.Decision `json:"consent"`
	// Audience is who the visitor is to this site: "bot" (automation, a crawler, or an
	// address inside a cloud provider's published ranges) is sent no tag; "internal"
	// (a member of the org that owns the site, by the IAM bearer the page sent, or an
	// address in the project's internal networks) is sent Google Analytics alone, to
	// load tagged traffic_type internal; "person" is everyone else.
	Audience string `json:"audience"`
}

func init() {
	spec.Register("/v1/project/tags", http.MethodGet, nil, tagConfig{})
	spec.Describe("/v1/project/tags", http.MethodGet,
		"The site's browser tag set for the hosted tag — which pixels to inject, by publishable key",
		"Returns the client-side pixels the SITE has connected (GA4, Google Ads, LinkedIn, Meta, "+
			"OpenAI, Pinterest, Reddit, TikTok, X) with their "+
			"NON-SECRET ids, so the hosted tag injects them first-party and stamps each browser event "+
			"with the same event_id the server-side Conversions API uses — deduping the two. Resolved "+
			"per site: by the publishable key on ?key= when it names a project, else by the request "+
			"host, so hanzo.ai and hanzo.chat carry different tags under one org. WITHOUT a resolvable "+
			"site it answers an empty set at 200 — a page never breaks on its tag config. "+
			"`audience` says who is asking — `bot` (automation, a crawler, or a cloud provider's "+
			"published address), `internal` (a member of the site's org, by the IAM bearer the page "+
			"sends, or an address in the project's internal networks) or `person` — and the set "+
			"answers for it: nothing for a bot, Google Analytics alone for the team. A site that "+
			"reports to several Meta pixels names each (comma separated on the project's `meta`), and "+
			"each is a tag of its own. "+
			"`consent` is the visitor's rule for the third-party tags, decided per request from the "+
			"country and US state Cloudflare attests: `eu` (the EU, the EEA, the UK and Switzerland, "+
			"and any visitor no edge placed: before a choice Google Analytics runs cookieless under "+
			"Consent Mode denied, Meta is held revoked, nothing else loads), `us_privacy` (a US state "+
			"with a privacy law, or a US visitor with no attested state: everything runs, the visitor "+
			"opts out of sale or sharing) or `global` (everywhere else: everything runs, with a "+
			"notice); `defaults` is the categories granted before a choice, `refused` says the request "+
			"carried Global Privacy Control or Do Not Track (no third party loads, in any region), and "+
			"a choice's grants count when stored under a version from `floor` to `version`. "+
			"The tag set is the same under every rule; the page gates it. The answer is per visitor "+
			"and never cached by a shared cache. The site's own first-party stream is not governed by it.")
}

// mountTags registers GET /v1/project/tags as a public, raw net/http handler
// (like the event plane's /v1/event/tag.js) that reads THIS process's project store
// directly.
func mountTags(app cloud.Router, s *cloud.Service[state]) {
	project := app.Group("/v1/project")
	project.Raw(http.MethodGet, "/tags", func(c *zip.Ctx) error {
		return serveTags(s, c)
	})
}

// buildTags maps a site's tag config (platform → non-secret pixel id) to injectable
// browser tags, in stable platform order. A platform with no client pixel, or an empty
// id, is omitted. Meta may name several pixels, comma separated — one per brand a site
// reports to — and each is a tag of its own carrying the platform's rules (pixels).
func buildTags(tags map[string]string) []browserTagOut {
	platforms := make([]string, 0, len(tags))
	rules := map[string]map[string]string{}
	for p, v := range tags {
		if platform, name, ok := strings.Cut(p, ":"); ok {
			if v = strings.TrimSpace(v); v != "" && name != "" {
				if rules[platform] == nil {
					rules[platform] = map[string]string{}
				}
				rules[platform][name] = v
			}
			continue
		}
		platforms = append(platforms, p)
	}
	sort.Strings(platforms)
	out := make([]browserTagOut, 0, len(platforms))
	for _, platform := range platforms {
		typ, ok := event.BrowserTags[platform]
		if !ok {
			continue
		}
		for _, id := range pixels(platform, tags[platform]) {
			out = append(out, browserTagOut{Platform: platform, Type: typ, ID: id, Events: rules[platform]})
		}
	}
	return out
}

// pixels are the ids a platform's entry names: Meta's, a comma list of numeric pixel
// ids, trimmed, each once, in the order given, anything not all digits dropped; any
// other platform's, the one id it holds.
func pixels(platform, v string) []string {
	if platform != "meta" {
		if id := strings.TrimSpace(v); id != "" {
			return []string{id}
		}
		return nil
	}
	var out []string
	for _, id := range strings.Split(v, ",") {
		id = strings.TrimSpace(id)
		if id != "" && strings.Trim(id, "0123456789") == "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// tagsKey lifts the publishable key: a publishable Authorization: Bearer first, then
// ?key=, then ?ingest_key=. Any other bearer is the visitor's IAM token — who is asking,
// not which site — and never a key.
func tagsKey(c *zip.Ctx) string {
	if b := c.Header("Authorization"); strings.HasPrefix(b, "Bearer ") {
		if k := strings.TrimSpace(strings.TrimPrefix(b, "Bearer ")); cloud.IsPublishableKey(k) {
			return k
		}
	}
	if k := strings.TrimSpace(c.Query("key")); k != "" {
		return k
	}
	return strings.TrimSpace(c.Query("ingest_key"))
}

// tagsHost lifts the site host for the org-key derivation path: ?host= first, else the
// Origin, else the Referer — reduced to a bare hostname.
func tagsHost(c *zip.Ctx) string {
	for _, raw := range []string{c.Query("host"), c.Header("Origin"), c.Header("Referer")} {
		if h := hostnameOf(raw); h != "" {
			return h
		}
	}
	return ""
}

func hostnameOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil {
			return u.Hostname()
		}
	}
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.IndexByte(raw, ':'); i > 0 {
		raw = raw[:i]
	}
	return raw
}

// siteOf answers the site a request names: its project, whose tags it loads. A verified
// host binding that carries its own tags wins over the key's project: one org key
// serves many sites, and each reports to its own stream. Otherwise the key's project
// answers, then a bare host binding. false when nothing resolves.
func siteOf(ctx context.Context, store *Store, key, host string) (Project, bool) {
	var byHost Project
	hostOK := false
	if host != "" {
		if p, err := store.ResolveHost(ctx, host); err == nil {
			if len(p.Tags) > 0 {
				return p, true
			}
			byHost, hostOK = p, true
		}
	}
	if key != "" {
		if p, err := store.ResolveKey(ctx, key); err == nil {
			return p, true
		}
	}
	return byHost, hostOK
}

// ownerOf is the org a request's site belongs to, and that project's internal
// networks: the resolved project's, else what the key names through the one
// admission every endpoint uses (an org-level key names an org and no project).
func ownerOf(ctx context.Context, site Project, found bool, key string) (string, []string) {
	if found {
		return site.Org, site.InternalNetworks
	}
	if key != "" {
		if at, ok := event.Admit(ctx, key); ok {
			return at.Org, at.InternalNetworks
		}
	}
	return "", nil
}

// forAudience is the tag set an audience loads: none for a bot or an agent, Google Analytics
// alone for the site's own team, all of it for a person.
func forAudience(tags []browserTagOut, audience string) []browserTagOut {
	switch audience {
	case event.Bot, event.Agent:
		return []browserTagOut{}
	case event.Internal:
		out := []browserTagOut{}
		for _, t := range tags {
			if t.Type == "ga" {
				out = append(out, t)
			}
		}
		return out
	}
	return tags
}

// hanzoMetaPixel is Hanzo AI's primary Meta pixel ID carried by hanzo.ai, hanzo.app,
// hanzo.bot, hanzo.team, platform.hanzo.ai, and pay.hanzo.ai. Any hanzo-org site
// carrying it also receives Hanzo AI's canonical LinkedIn Insight Tag (10406532,
// sponsoredAccount 557450290); legacy Boost tag 10420116 is disabled and replaced.
const (
	hanzoMetaPixel           = "1790308611893001"
	hanzoLinkedInPartnerID   = "10406532"
	hanzoOpenAIPixelID       = "TXKHZduRxZZGp4B1ysfiwm"
	disabledLinkedInPartner  = "10420116"
)

func withHanzoBrandTags(org string, tags map[string]string) map[string]string {
	metaPixel := strings.TrimSpace(env.Or("HANZO_META_PIXEL_ID", hanzoMetaPixel))
	if org != "hanzo" || metaPixel == "" || !slices.Contains(pixels("meta", tags["meta"]), metaPixel) {
		return tags
	}
	merged := make(map[string]string, len(tags)+2)
	if li := strings.TrimSpace(env.Or("HANZO_LINKEDIN_PARTNER_ID", hanzoLinkedInPartnerID)); li != "" {
		merged["linkedin"] = li
	}
	if oai := strings.TrimSpace(env.Or("OPENAI_PIXEL_ID", hanzoOpenAIPixelID)); oai != "" {
		merged["openai"] = oai
	}
	for k, v := range tags {
		v = strings.TrimSpace(v)
		if v == "" || (k == "linkedin" && v == disabledLinkedInPartner) {
			continue
		}
		merged[k] = v
	}
	return merged
}

// serveTags writes the site's browser tag config. Public + cross-origin, non-secret,
// fail-safe. A raw net/http handler so it sets CORS + cache directly, like /v1/event/tag.js.
func serveTags(s *cloud.Service[state], c *zip.Ctx) error {
	c.SetHeader("Content-Type", "application/json; charset=utf-8")
	c.SetHeader("Access-Control-Allow-Origin", "*")
	// The answer names the visitor's region, their browser's refusal and who they are,
	// so no shared cache may keep it for another, and it varies by every header it
	// turns on.
	c.SetHeader("Cache-Control", "private, max-age=60")
	c.Fiber().Vary("Authorization", "Sec-GPC", "DNT", "CF-IPCountry", "CF-Region-Code")

	ctx, key := c.Context(), tagsKey(c)
	site, found := siteOf(ctx, s.State.store, key, tagsHost(c))
	org, networks := ownerOf(ctx, site, found, key)
	out := tagConfig{
		Tags:     []browserTagOut{},
		Consent:  event.Policy.Decide(visitorCountry(c), geo.Region(c), consent.OptedOut(c)),
		Audience: event.Audience(c, org, networks),
	}
	if found {
		out.Tags = forAudience(buildTags(withHanzoBrandTags(site.Org, site.Tags)), out.Audience)
	}
	body, err := json.Marshal(out)
	if err != nil {
		body = []byte(`{"tags":[],"consent":{"region":"","mode":"eu","defaults":{"analytics":false,"marketing":false,"ads":false},"refused":false,"version":3,"floor":2},"audience":"person"}`)
	}
	return c.Bytes(http.StatusOK, body)
}

// visitorCountry is the country that picks a visitor's consent regime: what the edge
// stated, else the country the visitor's resolved address is registered in. api.hanzo.ai
// is not behind Cloudflare, so the edge states nothing there, and a visitor with no
// country was put in the strictest regime everywhere in the world, which is why GA saw
// no one. The address's country is a weak signal, read here and nowhere that decides
// money or risk.
func visitorCountry(c *zip.Ctx) string {
	if country := geo.Country(c); country != "" {
		return country
	}
	return locate.Country(geo.IP(c))
}
