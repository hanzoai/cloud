package event

// audience.go — who sent a request: a program, the site's own team, or a person.
//
// ONE classifier, read by the two places that answer a visitor. The tag config (GET
// /v1/project/tags, apps/projects) decides from it what the page loads, and ingest
// (visitors) stamps it on every event the request carries, server-side, so a client
// that predates it is covered and no client can clear it.
//
//	agent     the user agent names an AI agent acting for a person (a coding agent, an
//	          assistant's user-initiated fetch). It is counted, with its family, and is
//	          not a person: it loads no tag and reaches no ad platform.
//	bot       the user agent is automation or a crawler, or the address is inside a
//	          cloud provider's published ranges (clouds.go): a person does not browse
//	          from a data centre.
//	internal  a validated person on the team that owns the site, or an address inside
//	          the project's internal networks.
//	person    everyone else.
//
// Nothing here is configured. The internal networks are an optional project setting,
// empty unless a project names some.

import (
	"net/netip"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// The four audiences, as GET /v1/project/tags spells them.
const (
	Agent    = "agent"
	Bot      = "bot"
	Internal = "internal"
	Person   = "person"
)

// Audience classifies request c for the site org owns, whose internal networks are
// networks (CIDRs).
func Audience(c *zip.Ctx, org string, networks []string) string {
	return callerOf(c, org).audience(networks)
}

// caller is what one request says about who sent it, read once per request.
type caller struct {
	ip     netip.Addr
	agent  string
	bot    bool
	member bool
}

// callerOf reads the request: the address cloud already trusts at ingest
// (geo.IP), its user agent, and the principal the identity boundary verified.
func callerOf(c *zip.Ctx, org string) caller {
	ip, _ := netip.ParseAddr(strings.TrimSpace(geo.IP(c)))
	ip = ip.Unmap()
	ua := c.Header("User-Agent")
	return caller{ip: ip, agent: AgentFamily(ua), bot: robot(ua) || clouds.Load().has(ip), member: member(c, org)}
}

func (k caller) audience(networks []string) string {
	switch {
	case k.agent != "":
		return Agent
	case k.bot:
		return Bot
	case k.member || inside(k.ip, networks):
		return Internal
	}
	return Person
}

// member reports whether the validated person behind c is on org's own team. The
// membership set, the SuperAdmin bit and the org-admin bit are what the identity
// boundary (cloud.SanitizeIdentity) minted from the verified token; a client's copy
// never survives it, so an absent or unverifiable bearer is nobody.
//
// A brand's own org is where IAM files every self-serve account of that brand, as a
// plain member, so there a member is a customer: the team is a SuperAdmin or an admin
// of that org. In any other org every member is the team.
func member(c *zip.Ctx, org string) bool {
	if org == "" || !principal.Validated(c) {
		return false
	}
	if brand.Registered(org) {
		acting, _ := principal.Org(c)
		return principal.Super(c) || (acting == org && principal.IsOrgAdmin(c))
	}
	return slices.Contains(principal.Orgs(c), org)
}

// FromPerson reports whether an event is a person's visit: stamped neither agent, bot nor
// internal, by ingest or by the page itself (a driven browser, a teammate's marked
// one). Anything else stays in its org's stream, marked, and reaches no ad platform.
func FromPerson(props map[string]any) bool {
	return !flagged(props, Bot) && !flagged(props, Internal) && !flagged(props, Agent)
}

// flagged reads a mark a client may send as a bool or as "true"/"1".
func flagged(props map[string]any, key string) bool {
	switch v := props[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}

// inside reports whether ip is in one of networks.
func inside(ip netip.Addr, networks []string) bool {
	if !ip.IsValid() {
		return false
	}
	for _, n := range networks {
		if p, err := netip.ParsePrefix(n); err == nil && p.Contains(ip) {
			return true
		}
	}
	return false
}

// robot reports whether a user agent is a program's: a driven or headless browser, a
// crawler or link preview, an uptime check or scanner, or a bare HTTP library. A
// browser always names itself, so an empty agent is a program too.
func robot(ua string) bool {
	l := strings.ToLower(strings.TrimSpace(ua))
	if l == "" || l == "node" {
		return true
	}
	// Cubot makes phones; its model names are the one browser agent that says "bot".
	l = strings.ReplaceAll(l, "cubot", "")
	for _, t := range robots {
		if strings.Contains(l, t) {
			return true
		}
	}
	return false
}

// robots are the lower-case fragments that name a program in a user agent.
var robots = []string{
	// Driven and headless browsers.
	"headless", "playwright", "puppeteer", "selenium", "webdriver", "phantomjs", "slimerjs",
	"cypress", "lighthouse",
	// Crawlers, archivers, AI agents and link previews.
	"bot", "crawl", "spider", "slurp", "preview", "archiver", "scrapy", "facebookexternalhit",
	"facebookcatalog", "meta-externalagent", "whatsapp", "embedly", "google-inspectiontool",
	"mediapartners-google", "feedfetcher", "chatgpt-user", "anthropic-ai", "claude-user",
	"claude-web", "cohere-ai", "perplexity", "validator",
	// Uptime checks and scanners.
	"uptime", "pingdom", "statuscake", "synthetics", "checkly", "monitor", "kube-probe",
	"healthcheck", "googlehc", "zgrab", "masscan", "nuclei", "censys",
	// HTTP libraries and API tools.
	"curl/", "wget/", "httpie/", "python-requests", "python-urllib", "python-httpx", "aiohttp",
	"go-http-client", "java/", "apache-httpclient", "node-fetch", "undici", "axios/",
	"libwww-perl", "guzzlehttp", "postmanruntime", "insomnia/", "deno/", "bun/",
}

// AgentFamily names the AI agent a user agent belongs to, or "" when it is none. An
// agent here acts for a person: a coding agent or an assistant fetching a page the
// person asked about. A training crawler or a search index is a bot (robots), and a
// bare HTTP library names no agent. The fragments are lower case and ordered: the first
// family with a fragment in the user agent wins.
func AgentFamily(ua string) string {
	l := strings.ToLower(ua)
	if l == "" {
		return ""
	}
	for _, a := range agents {
		for _, f := range a.fragments {
			if strings.Contains(l, f) {
				return a.family
			}
		}
	}
	return ""
}

var agents = []struct {
	family    string
	fragments []string
}{
	{"claude", []string{"claude-cli", "claude-code", "claude-user"}},
	{"cursor", []string{"cursor/", "cursor-agent"}},
	{"codex", []string{"codex"}},
	{"chatgpt", []string{"chatgpt-user"}},
	{"grok", []string{"grok"}},
	{"hermes", []string{"hermes"}},
	{"gemini", []string{"gemini-cli", "geminicli", "gemini-deep-research"}},
	{"perplexity", []string{"perplexity-user"}},
	{"mistral", []string{"mistralai-user"}},
	{"copilot", []string{"copilot"}},
	{"windsurf", []string{"windsurf", "codeium"}},
	{"cline", []string{"cline/"}},
	{"aider", []string{"aider/"}},
	{"opencode", []string{"opencode"}},
	{"openclaw", []string{"openclaw"}},
	{"hanzo", []string{"hanzo-dev", "hanzo-bot", "hanzo-code", "hanzo-mcp", "hanzo-cli", "hanzo/"}},
	{"mcp", []string{"mcp-client", "mcp/"}},
}
