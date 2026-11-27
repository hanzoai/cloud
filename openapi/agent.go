package openapi

// THE AGENT'S DOOR: what a crawler or an agent reads on a brand's host before it
// holds a credential.
//
// On the brand's API host (api.hanzo.ai): the home page in the form the caller
// asked for (HTML, Markdown, or the capability index as JSON), robots.txt, the
// sitemap, auth.md and the RFC 9727 API catalog. On the API host and every
// first-party site: the MCP server's card and the A2A agent card. On every host
// the brand claims: /llms.txt and /skill.md. On every host: the RFC 9728
// protected-resource metadata the MCP server's 401 challenge names.
//
// # Every document is a typed operation of this capability
//
// [pages] is the one table. [declare] registers each row as a typed op on
// [core]'s router, so the document projects it under the host's capability (Self)
// like the index and the agent MCP endpoint; [UseAgent] answers the same rows on
// the edge, calling the same writer. A row the edge answers and the document does
// not carry cannot exist.
//
// # Why the edge answers them as middleware
//
// Which brand, and whether to answer at all, is a fact of the request's Host,
// and the console answers / and its own robots.txt on the hosts it is served at.
// A route at / would take the console's own address on every host. So the edge
// answers a row only where its scope holds and yields every other request
// untouched, exactly as the index does inside the subtrees the mounts claim.
//
// # Every address a document names answers
//
// The sitemap is the set [crawlable] reads off the composed document: the host's
// own documents, the well-known files and each capability index, each answering
// 200 without a credential. robots.txt allows exactly that set under /v1/ and
// keeps a crawler out of everything else there, so a crawler that obeys it never
// asks for an address that answers 401.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"html"
	"net/http"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/manifest/a2a"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/mcp"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

const (
	// HomePath is the API host's home page.
	HomePath = "/"
	// RobotsPath is robots.txt (RFC 9309).
	RobotsPath = "/robots.txt"
	// SitemapPath is the sitemap (sitemaps.org 0.9).
	SitemapPath = "/sitemap.xml"
	// LlmsPath is the llmstxt.org page for agents, and SkillPath the same page as
	// an Agent Skill.
	LlmsPath  = "/llms.txt"
	SkillPath = "/skill.md"
	// AuthPath is how an agent signs in, as Markdown.
	AuthPath = "/auth.md"
	// CatalogPath is the RFC 9727 API catalog.
	CatalogPath = "/.well-known/api-catalog"
)

// Signal is the content-usage preference robots.txt states for every crawler
// (contentsignals.org).
const Signal = "Content-Signal: search=yes, ai-input=yes, ai-train=yes"

// Crawlers are the agents robots.txt names, each in the one group every crawler
// reads.
var Crawlers = []string{
	"GPTBot", "OAI-SearchBot", "ChatGPT-User", "ClaudeBot", "Claude-User", "Claude-SearchBot",
	"Claude-Web", "anthropic-ai", "PerplexityBot", "Perplexity-User", "Google-Extended", "CCBot",
	"Applebot-Extended", "Meta-ExternalAgent", "Amazonbot", "cohere-ai", "DuckAssistBot", "Bingbot",
	"Googlebot",
}

// Scopes are the OAuth scopes a client asks the authorization server for to call
// this API: the token's own user and organizations decide what it may do.
var Scopes = []string{"openid", "profile", "email", "offline_access"}

// The media the door answers in.
const (
	mediaHTML     = "text/html; charset=utf-8"
	mediaMarkdown = "text/markdown; charset=utf-8"
	mediaText     = "text/plain; charset=utf-8"
	mediaXML      = "application/xml; charset=utf-8"
	mediaJSON     = "application/json"
	mediaLinkset  = `application/linkset+json; profile="https://www.rfc-editor.org/info/rfc9727"`
	mediaCard     = "application/mcp-server-card+json"
)

// scope is where a row answers.
type scope int

const (
	everyHost   scope = iota // any host the request reached
	brandHost                // a host a brand claims and publishes a manifest for (brand.Agent)
	apiHost                  // the brand's API host alone
	claimedHost              // brandHost, and every first-party site the brand lists (brand.Claimed)
)

// page is one document: its address, where it answers, what it says it is, the
// media it answers in (the first unless the caller's Accept prefers another) and
// the headers it sets, and its writer.
type page struct {
	path    string
	id      string
	scope   scope
	summary string
	media   []string
	headers []string
	write   func(*door) (*zip.Body, error)
}

// pages are the door's documents, read by the document (declare) and by the edge
// (UseAgent).
var pages = []page{
	{HomePath, "get_home", apiHost, "What this API is, as an HTML page, as Markdown, or as the capability index in JSON",
		[]string{mediaHTML, mediaMarkdown, mediaJSON}, []string{"Link", "Vary", "Cache-Control"}, (*door).home},
	{RobotsPath, "get_robots", apiHost, "What a crawler may read on this host, and the content signals for all of it",
		[]string{mediaText}, []string{"Cache-Control"}, (*door).robots},
	{SitemapPath, "get_sitemap", apiHost, "Every address on this host a crawler can read without a credential",
		[]string{mediaXML}, []string{"Cache-Control"}, (*door).sitemap},
	{LlmsPath, "get_llms", brandHost, "How an agent starts with this API, for an agent to read",
		[]string{mediaText}, []string{"Cache-Control"}, (*door).llms},
	{SkillPath, "get_skill", brandHost, "How an agent starts with this API, as an Agent Skill",
		[]string{mediaMarkdown}, []string{"Cache-Control"}, (*door).skill},
	{AuthPath, "get_auth", apiHost, "How an agent or an app signs in to call this API",
		[]string{mediaMarkdown}, []string{"Cache-Control"}, (*door).auth},
	{CatalogPath, "get_api_catalog", apiHost, "The APIs this host publishes, with where each is described (RFC 9727)",
		[]string{mediaLinkset}, []string{"Cache-Control", "Access-Control-Allow-Origin"}, (*door).catalog},
	{mcp.Card, "get_mcp_card", claimedHost, "The agent MCP server's card: its name, version, endpoint, transport and revisions",
		[]string{mediaJSON, mediaCard}, []string{"Cache-Control", "Access-Control-Allow-Origin", "Vary"}, (*door).card},
	{a2a.Card, "get_agent_card", claimedHost, "The A2A agent card for the assistant this API answers as",
		[]string{mediaJSON}, []string{"Cache-Control", "Access-Control-Allow-Origin"}, (*door).agentCard},
	{mcp.Metadata, "get_resource", everyHost, "Which authorization server mints the bearer this API takes (RFC 9728)",
		[]string{mediaJSON}, []string{"Cache-Control", "Access-Control-Allow-Origin"}, (*door).resource},
	{mcp.MetadataMCP, "get_mcp_resource", everyHost, "Which authorization server mints the bearer the MCP server takes (RFC 9728)",
		[]string{mediaJSON}, []string{"Cache-Control", "Access-Control-Allow-Origin"}, (*door).mcpResource},
}

// aliases are second addresses a scanner probes for a page: answered by the
// edge exactly as the page is, where the page is, and never declared — the
// document carries the page once, at its own address, so an alias is no second
// operation in any SDK, CLI or tool list. /.well-known/mcp.json is where
// agent-readiness scanners look for the MCP card (hanzo-inc/cloud#421), and
// /.well-known/agent.json is the A2A card's address before A2A 0.3 (#422).
var aliases = map[string]string{
	"/.well-known/mcp.json": mcp.Card,
	a2a.Legacy:              a2a.Card,
}

// door is one request's facts, as the writers read them.
type door struct {
	b      brand.Info // the brand the request's host belongs to
	own    brand.Info // the deployment's brand, which the resource metadata speaks for
	issuer string     // the deployment's issuer: the authorization server it names
	host   string     // the request's own Host header, never a forwarded one
	origin string     // the origin the request reached: the resource the metadata is about
	accept string     // the medium the caller's Accept prefers, of the row's media
	read   *Reading   // the index's reading of the document
	kept   []string   // the addresses outside /v1 an app answers, which a crawler stays out of
}

// api is the brand's API origin, https://api.hanzo.ai.
func (d *door) api() string { return "https://" + brand.APIHost(d.b.ID) }

// name is the brand's display name, Hanzo.
func (d *door) name() string { return brand.Display(d.b.ID) }

// nothing is what every document reads from its caller.
type nothing struct{}

// declare registers every row as a typed op on app, which is how [core] projects
// them. Its handlers answer for the default brand; the edge answers for the brand
// the request's host is.
func declare(app *zip.App) {
	d := &door{b: brand.For(brand.Default), own: brand.For(brand.Default)}
	for _, p := range pages {
		app.Get(p.path, func(context.Context, *nothing) (*zip.Body, error) { return p.write(d) },
			zip.WithOperationID(p.id), zip.WithSummary(p.summary),
			zip.Produces(p.media...), zip.WithResponseHeader(p.headers...))
	}
}

func init() {
	// A caller reads every one of these before it holds a credential.
	for _, p := range pages {
		spec.Open(p.path, http.MethodGet)
	}
}

// UseAgent answers the door's documents on app, from read, for the hosts each
// row's scope admits, and yields every other request untouched.
//
// kept are the addresses outside /v1 an app answers (manifest prefixes), which
// robots.txt keeps a crawler out of. issuer and home are the deployment's own:
// the authorization server the resource metadata names, and the brand it speaks
// for.
//
// Composed after the published-site edge, so a customer's site answers its own
// addresses, and before the console, whose catch-all would answer each one with
// its shell.
func UseAgent(app *zip.App, read *Reading, kept []string, issuer, home string) {
	at := make(map[string]page, len(pages)+len(aliases))
	for _, p := range pages {
		at[p.path] = p
	}
	for from, to := range aliases {
		for _, p := range pages {
			if p.path == to {
				at[from] = p
			}
		}
	}
	deployment := brand.For(home)
	app.Use(zip.H(func(c *zip.Ctx) error {
		p, ok := at[c.Path()]
		if !ok {
			return c.Continue()
		}
		if m := c.Method(); m != http.MethodGet && m != http.MethodHead {
			return c.Continue()
		}
		host := addressed(c)
		d := &door{own: deployment, issuer: issuer, host: host, origin: brand.Origin(host), read: read, kept: kept}
		if d.b, ok = admits(p.scope, host, deployment); !ok {
			return c.Continue()
		}
		d.accept = negotiate(c, p.media)
		out, err := p.write(d)
		if err != nil {
			return err
		}
		for k, v := range out.Header {
			c.SetHeader(k, v)
		}
		c.SetHeader("Content-Type", out.Type)
		return c.Bytes(http.StatusOK, out.Bytes)
	}))
}

// negotiate is the medium of media the request's Accept prefers (RFC 9110
// §12.5.1), the first when it names none of them.
func negotiate(c *zip.Ctx, media []string) string {
	if len(media) == 1 {
		return media[0]
	}
	offers := make([]string, len(media))
	for i, m := range media {
		offers[i], _, _ = strings.Cut(m, ";")
	}
	if i := slices.Index(offers, c.Fiber().Accepts(offers...)); i >= 0 {
		return media[i]
	}
	return media[0]
}

// admits reports whether a row of scope s answers on host, and for which brand.
func admits(s scope, host string, deployment brand.Info) (brand.Info, bool) {
	if s == everyHost {
		return deployment, true
	}
	if s == claimedHost {
		return brand.Claimed(host)
	}
	b, ok := brand.Agent(host)
	if !ok {
		return brand.Info{}, false
	}
	if s == apiHost && hostname(host) != brand.APIHost(b.ID) {
		return brand.Info{}, false
	}
	return b, true
}

// hostname is a Host without its port, trailing root dot or case.
func hostname(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return host
}

// addressed is the Host this hop was sent to, read off the request line's header
// and never off X-Forwarded-Host. The host trusts its ingress's forwarded headers,
// so c.Host() answers the name the CLIENT typed; for a static site's /llms.txt
// that is the site, while the edge has set Host to the API host of the brand the
// registry says speaks for it (universe infra/sites). The brand is the edge's
// decision, so it is read where the edge wrote it.
func addressed(c *zip.Ctx) string {
	return string(c.Fiber().Request().Header.Host())
}

// body is a document's answer: bytes in one medium, cacheable for five minutes.
func body(media string, b []byte) *zip.Body {
	return &zip.Body{Type: media, Bytes: b, Header: map[string]string{"Cache-Control": "public, max-age=300"}}
}

// shared is body for a JSON document a browser on any origin may read.
func shared(media string, v any) (*zip.Body, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	out := body(media, append(b, '\n'))
	out.Header["Access-Control-Allow-Origin"] = "*"
	return out, nil
}

// crawl is every address this host serves a crawler may read, from the reading.
func (d *door) crawl() ([]string, error) {
	if d.read == nil {
		return nil, nil
	}
	r, err := d.read.render()
	if err != nil {
		return nil, err
	}
	return r.pages, nil
}

// crawlable is every address the composed document says a crawler can read
// without a credential: each GET with no path parameter that is the host's own
// (Self) or a well-known file, and the index of each capability that answers one —
// generally available ones only, sorted. robots.txt and the sitemap name the set
// rather than belong to it.
func crawlable(d *spec.Document, per map[string]*Index) []string {
	var out []string
	for path, item := range d.Paths {
		op := item["get"]
		if op == nil || strings.Contains(path, "{") || path == RobotsPath || path == SitemapPath {
			continue
		}
		if op.Stage != "" && op.Stage != "ga" {
			continue
		}
		if op.App == Self || strings.HasPrefix(path, "/.well-known/") {
			out = append(out, path)
		}
	}
	for name := range per {
		out = append(out, spec.RootPath+"/"+name)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// link is one line of the home page: what it is, where, and what it says.
type link struct{ label, href, says string }

// links are the home page's lines, in both its HTML and its Markdown.
func (d *door) links() []link {
	api := d.api()
	out := []link{
		{"Capabilities", api + spec.RootPath, "every capability, and the address to follow for each"},
		{"OpenAPI", api + Path, "the whole API as one OpenAPI 3.1 document"},
		{"MCP", api + mcp.Path, "the same operations as MCP tools, over streamable HTTP; its card is " + api + mcp.Card},
	}
	if d.b.Docs != "" {
		out = append(out, link{"Docs", d.b.Docs, "guides and the API reference"})
	}
	out = append(out,
		link{"For agents", api + LlmsPath, "how an agent starts, with no person in the loop"},
		link{"Sign in", api + AuthPath, "API keys, tokens and the flows that mint them"},
		link{"API catalog", api + CatalogPath, "where each API on this host is described (RFC 9727)"},
	)
	if d.b.Console != "" {
		out = append(out, link{"API keys", "https://" + d.b.Console + "/api-keys", "create and revoke keys, for a person"})
	}
	if d.b.Status != "" {
		out = append(out, link{"Status", d.b.Status, "whether the API is up"})
	}
	return out
}

// lede is the home page's one sentence.
func (d *door) lede() string {
	return d.name() + "'s API: one key for every capability listed at " + d.api() + spec.RootPath + "."
}

// rel is the Link header the home page answers with: where the catalog, the page
// for agents, the API's description and its documentation are (RFC 8288, 8631,
// 9727).
func (d *door) rel() string {
	l := []string{
		"<" + CatalogPath + `>; rel="api-catalog"`,
		"<" + LlmsPath + `>; rel="describedby"; type="text/plain"`,
		"<" + d.api() + Path + `>; rel="service-desc"; type="application/json"`,
	}
	if d.b.Docs != "" {
		l = append(l, "<"+d.b.Docs+`>; rel="service-doc"`)
	}
	return strings.Join(l, ", ")
}

// home is the API host's home page, in the medium the caller asked for: HTML by
// default, Markdown for Accept: text/markdown, and for Accept: application/json
// the capability index GET /v1 answers, byte for byte.
func (d *door) home() (*zip.Body, error) {
	var out *zip.Body
	switch d.accept {
	case mediaMarkdown:
		var s strings.Builder
		s.WriteString("# " + d.name() + " API\n\n" + d.lede() + "\n\n")
		for _, l := range d.links() {
			s.WriteString("- [" + l.label + "](" + l.href + "): " + l.says + "\n")
		}
		out = body(mediaMarkdown, []byte(s.String()))
	case mediaJSON:
		root := []byte("{}")
		if d.read != nil {
			r, err := d.read.render()
			if err != nil {
				return nil, err
			}
			root = r.endpoints[spec.RootPath]
		}
		out = body(mediaJSON, root)
	default:
		title := html.EscapeString(d.name() + " API")
		var s strings.Builder
		s.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
		s.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
		s.WriteString("<title>" + title + "</title>\n")
		s.WriteString("<meta name=\"description\" content=\"" + html.EscapeString(d.lede()) + "\">\n")
		s.WriteString("<style>:root{color-scheme:light dark}body{font:16px/1.5 system-ui,sans-serif;max-width:40rem;margin:3rem auto;padding:0 1rem}a{color:inherit}</style>\n")
		s.WriteString("</head>\n<body>\n<main>\n<h1>" + title + "</h1>\n<p>" + html.EscapeString(d.lede()) + "</p>\n<ul>\n")
		for _, l := range d.links() {
			s.WriteString("<li><a href=\"" + html.EscapeString(l.href) + "\">" + html.EscapeString(l.label) + "</a>: " + html.EscapeString(l.says) + "</li>\n")
		}
		s.WriteString("</ul>\n</main>\n</body>\n</html>\n")
		out = body(mediaHTML, []byte(s.String()))
	}
	// One URL, three representations: a cache that ignores Vary must not keep one.
	out.Header["Cache-Control"] = "no-store"
	out.Header["Vary"] = "Accept"
	out.Header["Link"] = d.rel()
	return out, nil
}

// robots is robots.txt: one group naming every crawler, the content signals, the
// crawlable addresses under /v1/ allowed and the rest of /v1/ and the apps'
// addresses outside it disallowed, then the sitemap.
func (d *door) robots() (*zip.Body, error) {
	crawl, err := d.crawl()
	if err != nil {
		return nil, err
	}
	var s strings.Builder
	s.WriteString("# " + d.name() + " API. Every address a crawler may read is in the sitemap.\n")
	s.WriteString("User-agent: *\n")
	for _, ua := range Crawlers {
		s.WriteString("User-agent: " + ua + "\n")
	}
	s.WriteString(Signal + "\n")
	s.WriteString("Allow: /\n")
	s.WriteString("Disallow: " + spec.RootPath + "/\n")
	for _, k := range d.kept {
		s.WriteString("Disallow: " + k + "\n")
	}
	for _, p := range crawl {
		if strings.HasPrefix(p, spec.RootPath+"/") {
			s.WriteString("Allow: " + p + "$\n")
		}
	}
	s.WriteString("\nSitemap: " + d.api() + SitemapPath + "\n")
	return body(mediaText, []byte(s.String())), nil
}

// sitemap is every crawlable address, absolute.
func (d *door) sitemap() (*zip.Body, error) {
	crawl, err := d.crawl()
	if err != nil {
		return nil, err
	}
	var s bytes.Buffer
	s.WriteString(xml.Header)
	s.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range crawl {
		s.WriteString("  <url><loc>")
		if err := xml.EscapeText(&s, []byte(d.api()+p)); err != nil {
			return nil, err
		}
		s.WriteString("</loc></url>\n")
	}
	s.WriteString("</urlset>\n")
	return body(mediaXML, s.Bytes()), nil
}

// llms is the brand's page for agents: one sentence, the steps that start an
// account with a wallet where its IAM opens one (Info.Wallet), and the addresses
// the API describes itself at. The steps are ENDPOINT NAMES, never the recipe:
// the recipe is the manifest (Info.Agent), written once beside the runnable file
// that proves it, so a second copy here cannot drift from it.
func (d *door) llms() (*zip.Body, error) {
	return body(mediaText, []byte(llms(d.b))), nil
}

// skill is llms as an Agent Skill: the frontmatter a loader reads, then the same
// page.
func (d *door) skill() (*zip.Body, error) {
	b := d.b
	start := "get a key"
	if b.Wallet {
		start = "get a key with a wallet and no person"
	}
	s := "---\nname: " + b.ID + "\ndescription: \"Use " + brand.Display(b.ID) + "'s API at https://" + brand.APIHost(b.ID) +
		spec.RootPath + " as an agent: " + start + ", call it and pay. Full instructions: " + b.Agent + "\"\n---\n\n" + llms(b)
	return body(mediaMarkdown, []byte(s)), nil
}

func llms(b brand.Info) string {
	name := brand.Display(b.ID)
	api := "https://" + brand.APIHost(b.ID)
	var s strings.Builder
	s.WriteString("# " + name + "\n\n")
	s.WriteString("> " + name + "'s API for agents: one key for every capability listed at " + api + spec.RootPath + ".\n\n")
	s.WriteString("Read the full instructions first: " + b.Agent + "\n")
	if b.Wallet {
		id := b.IAMIssuer
		s.WriteString("\n## Start with a wallet, no person\n\n")
		s.WriteString("1. GET " + id + "/v1/iam/web3/nonce?chain=evm&address=<address>\n")
		s.WriteString("2. Sign the CAIP-122 message it describes with EIP-191 personal_sign\n")
		s.WriteString("3. POST " + id + "/v1/iam/web3/verify (type code, PKCE S256); a first-seen wallet gets its own account and organization\n")
		s.WriteString("4. POST " + id + "/v1/iam/oauth/token, the code for a token\n")
		s.WriteString("5. POST " + api + "/v1/account/keys, an API key\n")
		s.WriteString("6. POST " + api + "/v1/chat/completions, model enso-auto: at no charge while nothing pays\n")
		s.WriteString("7. GET " + api + "/v1/billing/crypto/options, the chains and tokens a deposit address is watched for\n")
		s.WriteString("8. POST " + api + "/v1/billing/crypto/deposit {chain, token}, an address of your own; send that token on that chain to it\n")
		s.WriteString("9. GET " + api + "/v1/billing/balance, `cash` — what the deposit is credited to once the chain confirms it\n")
	}
	s.WriteString("\n## The API\n\n")
	s.WriteString("- Capabilities: " + api + spec.RootPath + " (and GET " + api + spec.RootPath + "/<name> for any capability or resource subtree)\n")
	s.WriteString("- OpenAPI: " + api + Path + " (slice with ?capability=<name>, ?path=/v1/<prefix>, or ?operation=<id>)\n")
	s.WriteString("- MCP: " + api + mcp.Path + "\n")
	if b.Console != "" {
		s.WriteString("- Keys, for a person: https://" + b.Console + "/api-keys\n")
	}
	return s.String()
}

// auth is how a caller signs in: the one header every call takes, and each flow
// the brand's IAM runs to mint what goes in it. IAM owns every sign-in and every
// token; this API validates what it minted and holds no password.
func (d *door) auth() (*zip.Body, error) {
	b, api, id := d.b, d.api(), d.b.IAMIssuer
	var s strings.Builder
	s.WriteString("# " + d.name() + " API: signing in\n\n")
	s.WriteString("Every call takes one header: `Authorization: Bearer <credential>`. The credential is an API key (`sk-live-…`) " +
		"or an access token from " + d.name() + "'s identity server, " + id + ". That server runs every sign-in and mints every " +
		"token; this API validates them and holds no password.\n")
	if b.Wallet {
		s.WriteString("\n## An agent with a wallet and no person\n\n")
		s.WriteString("1. `GET " + id + "/v1/iam/web3/nonce?chain=evm&address=<address>`: a challenge for the wallet.\n")
		s.WriteString("2. Sign the CAIP-122 message it describes with EIP-191 `personal_sign`.\n")
		s.WriteString("3. `POST " + id + "/v1/iam/web3/verify` (type `code`, PKCE `S256`): an authorization code. A first-seen wallet gets its own account and organization.\n")
		s.WriteString("4. `POST " + id + "/v1/iam/oauth/token`: the code for an access token.\n")
		s.WriteString("5. `POST " + api + "/v1/account/keys`: an API key, shown once.\n")
	}
	if b.Agent != "" {
		s.WriteString("\nThe whole recipe, every body included: " + b.Agent + "\n")
	}
	s.WriteString("\n## A terminal or a device with no browser\n\n")
	s.WriteString("OAuth device authorization (RFC 8628): `POST " + id + "/v1/iam/oauth/device` answers a code and a page; a person " +
		"opens the page and enters the code while the device polls `POST " + id + "/v1/iam/oauth/token` with `grant_type=urn:ietf:params:oauth:grant-type:device_code`.")
	if b.Login != "" {
		s.WriteString(" The CLI runs it: `" + b.Login + "`.")
	}
	s.WriteString("\n")
	if b.Console != "" {
		s.WriteString("\n## A person\n\nCreate and revoke API keys at https://" + b.Console + "/api-keys.\n")
	}
	s.WriteString("\n## An app\n\nOpenID Connect against " + id + ": the authorization code flow with PKCE (`S256`). " +
		"Metadata: " + id + "/.well-known/openid-configuration.\n")
	s.WriteString("\n## Scopes\n\n`" + strings.Join(Scopes, "`, `") + "`. What a token may do is decided by its own user and organizations.\n")
	s.WriteString("\n## Discovery\n\n")
	s.WriteString("- Protected resource metadata (RFC 9728): " + api + mcp.Metadata + "\n")
	s.WriteString("- Authorization server metadata (RFC 8414): " + id + "/.well-known/oauth-authorization-server\n")
	s.WriteString("- MCP: `POST " + api + mcp.Path + "` answers a connection or a tool call that carries no credential with 401 and a " +
		"`WWW-Authenticate` header naming the resource metadata at " + api + mcp.MetadataMCP + ". An MCP client signs in with OAuth " +
		"(" + id + " registers clients dynamically, RFC 7591, so none is configured by hand) or sends an API key as the bearer.\n")
	return body(mediaMarkdown, []byte(s.String())), nil
}

// target is one link of an RFC 9264 linkset, and entry one context object.
type target struct {
	Href string `json:"href"`
	Type string `json:"type,omitempty"`
}

type entry struct {
	Anchor      string   `json:"anchor"`
	ServiceDesc []target `json:"service-desc,omitempty"`
	ServiceDoc  []target `json:"service-doc,omitempty"`
	ServiceMeta []target `json:"service-meta,omitempty"`
	Status      []target `json:"status,omitempty"`
	DescribedBy []target `json:"describedby,omitempty"`
}

// catalog is the RFC 9727 API catalog: the API, with its description,
// documentation, resource metadata and status; the MCP server, with its card and
// resource metadata; and the brand's page for agents, with the documents that
// describe it.
func (d *door) catalog() (*zip.Body, error) {
	api, b := d.api(), d.b
	rest := entry{
		Anchor:      api + spec.RootPath,
		ServiceDesc: []target{{api + Path, mediaJSON}},
		ServiceMeta: []target{{api + mcp.Metadata, mediaJSON}},
	}
	if b.Reference != "" {
		rest.ServiceDoc = []target{{b.Reference, "text/html"}}
	}
	if b.Status != "" {
		rest.Status = []target{{b.Status, "text/html"}}
	}
	set := []entry{rest, {
		Anchor:      api + mcp.Path,
		ServiceDesc: []target{{api + mcp.Card, mediaJSON}},
		ServiceMeta: []target{{api + mcp.MetadataMCP, mediaJSON}},
	}}
	if b.Guide != "" {
		guide := entry{Anchor: b.Guide, DescribedBy: []target{
			{api + LlmsPath, "text/plain"}, {api + SkillPath, "text/markdown"}, {api + AuthPath, "text/markdown"},
		}}
		if b.Agent != "" {
			guide.DescribedBy = append(guide.DescribedBy, target{b.Agent, "text/markdown"})
		}
		set = append(set, guide)
	}
	return shared(mediaLinkset, map[string][]entry{"linkset": set})
}

// implementation is who answers an MCP initialize: serverInfo.
type implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type remote struct {
	Type      string   `json:"type"`
	URL       string   `json:"url"`
	Revisions []string `json:"supportedProtocolVersions"`
}

// Card is the MCP server card. Its first fields are SEP-2127's (Final): a
// reverse-DNS name, the version, a description of at most 100 characters, and
// the remote endpoint with the revisions it answers in. The rest are the
// SEP-1649 draft's, which discovery scanners read: serverInfo as initialize
// answers it, the endpoint, its transport, the newest revision and that the
// server serves tools. Every value is the server's own (manifest/mcp,
// provenance), so the card and initialize cannot disagree.
type Card struct {
	Schema      string   `json:"$schema"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Website     string   `json:"websiteUrl,omitempty"`
	Remotes     []remote `json:"remotes"`

	ServerInfo   implementation    `json:"serverInfo"`
	URL          string            `json:"url"`
	Transport    map[string]string `json:"transport"`
	Protocol     string            `json:"protocolVersion"`
	Capabilities map[string]bool   `json:"capabilities"`
}

// cardSchema is the SEP-2127 schema a card conforms to.
const cardSchema = "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json"

// card is the MCP server's card on the brand's API host, in the card's own medium
// when the caller asks for it (SEP-2127) and as JSON otherwise.
func (d *door) card() (*zip.Body, error) {
	labels := strings.Split(d.b.Domain, ".")
	slices.Reverse(labels)
	url, version := d.api()+mcp.Path, env.Version()
	medium := mediaJSON
	if d.accept == mediaCard {
		medium = mediaCard
	}
	out, err := shared(medium, Card{
		Schema:       cardSchema,
		Name:         strings.Join(labels, ".") + "/" + mcp.Name,
		Version:      version,
		Title:        d.name() + " API",
		Description:  d.name() + "'s API as MCP tools: one per capability, its operations named by an argument.",
		Website:      d.b.Docs,
		Remotes:      []remote{{Type: "streamable-http", URL: url, Revisions: mcp.Revisions}},
		ServerInfo:   implementation{Name: mcp.Name, Version: version},
		URL:          url,
		Transport:    map[string]string{"type": "streamable-http"},
		Protocol:     mcp.Protocol,
		Capabilities: map[string]bool{"tools": true},
	})
	if err != nil {
		return nil, err
	}
	out.Header["Vary"] = "Accept"
	return out, nil
}

// AgentCard is the A2A agent card (A2A 1.0 §4.4.1): the assistant the API host
// answers as over A2A (apps/agents/a2a.go), where its endpoint is, the binding
// and protocol version it speaks, what it can do and how a caller signs in.
// Every fact is the server's own (manifest/a2a, provenance), so the card cannot
// name an endpoint or a version the server does not answer with.
type AgentCard struct {
	Name                 string                    `json:"name"`
	Description          string                    `json:"description"`
	Interfaces           []agentInterface          `json:"supportedInterfaces"`
	Provider             agentProvider             `json:"provider"`
	Version              string                    `json:"version"`
	Documentation        string                    `json:"documentationUrl,omitempty"`
	Capabilities         agentCapabilities         `json:"capabilities"`
	SecuritySchemes      map[string]map[string]any `json:"securitySchemes"`
	SecurityRequirements []agentRequirement        `json:"securityRequirements"`
	InputModes           []string                  `json:"defaultInputModes"`
	OutputModes          []string                  `json:"defaultOutputModes"`
	Skills               []agentSkill              `json:"skills"`
}

type agentInterface struct {
	URL      string `json:"url"`
	Binding  string `json:"protocolBinding"`
	Protocol string `json:"protocolVersion"`
}

type agentProvider struct {
	Organization string `json:"organization"`
	URL          string `json:"url"`
}

type agentCapabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
	ExtendedAgentCard bool `json:"extendedAgentCard"`
}

// agentRequirement is one way to satisfy the card's security: the schemes it
// names, each with the scopes it needs.
type agentRequirement struct {
	Schemes map[string]agentScopes `json:"schemes"`
}

type agentScopes struct {
	List []string `json:"list"`
}

type agentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples,omitempty"`
}

// agentCard is the A2A card on the brand's API host and its first-party sites. It
// names the A2A endpoint on the API host whichever of them it is read on, and the
// authorization server that host's resource metadata names (authority), so an
// agent signs in where every other door on that host sends it.
func (d *door) agentCard() (*zip.Body, error) {
	_, issuer := d.authority()
	name := d.name()
	return shared(mediaJSON, AgentCard{
		Name: name,
		Description: name + "'s assistant over A2A: it answers with " + name + "'s cloud as its tools — " +
			"models, agents, storage, compute and the rest of the API — acting with the caller's own credential.",
		Interfaces:    []agentInterface{{URL: d.api() + a2a.Path, Binding: a2a.Binding, Protocol: a2a.Protocol}},
		Provider:      agentProvider{Organization: name, URL: "https://" + d.b.Domain},
		Version:       env.Version(),
		Documentation: d.b.Docs,
		Capabilities:  agentCapabilities{},
		SecuritySchemes: map[string]map[string]any{
			"oauth": {"openIdConnectSecurityScheme": map[string]any{
				"openIdConnectUrl": issuer + "/.well-known/openid-configuration",
				"description":      "Sign in at " + issuer + " and send the access token as Authorization: Bearer.",
			}},
			"apiKey": {"httpAuthSecurityScheme": map[string]any{
				"scheme":      "Bearer",
				"description": "An API key (sk-…) sent as Authorization: Bearer.",
			}},
		},
		SecurityRequirements: []agentRequirement{
			{Schemes: map[string]agentScopes{"oauth": {List: Scopes}}},
			{Schemes: map[string]agentScopes{"apiKey": {List: []string{}}}},
		},
		InputModes:  []string{"text/plain", "application/json"},
		OutputModes: []string{"text/markdown", "text/plain"},
		Skills: []agentSkill{{
			ID:   "assistant",
			Name: name + " assistant",
			Description: "Answers questions and does work in " + name + "'s cloud with the tools the caller's " +
				"credential reaches. Each message is a task, billed as an agent run to the caller.",
			Tags: []string{"assistant", "cloud", "agents", "mcp"},
			Examples: []string{
				"Which models can I call, and what do they cost?",
				"List my agents and what each one last ran.",
			},
		}},
	})
}

// authority is the brand whose metadata speaks for the request's host, and the
// authorization server that brand signs callers in at: the identity host of the
// brand the host belongs to, whose tokens the API trusts beside every other
// brand's (brand.Issuers), so a Lux caller signs in at Lux's; on the
// deployment's own brand, and on a host no brand claims, the deployment's issuer,
// which a white-label deployment pins.
func (d *door) authority() (brand.Info, string) {
	if id, ok := brand.ForHostOK(d.host); ok && id != d.own.ID && brand.IssuerFor(id) != "" {
		return brand.For(id), brand.IssuerFor(id)
	}
	return d.own, d.issuer
}

// Resource is RFC 9728 protected-resource metadata: the resource is the origin
// the client reached, read off the request's own Host header, so the metadata
// describes the host it asked and never one a forwarded header names. The
// authorization server is the host's [door.authority].
type Resource struct {
	Resource      string   `json:"resource"`
	Servers       []string `json:"authorization_servers"`
	Methods       []string `json:"bearer_methods_supported"`
	Scopes        []string `json:"scopes_supported"`
	Name          string   `json:"resource_name,omitempty"`
	Documentation string   `json:"resource_documentation,omitempty"`
}

// resource is the protected-resource metadata of the API: its resource is the
// origin the client reached.
func (d *door) resource() (*zip.Body, error) { return d.protected(d.origin) }

// mcpResource is the same document for the MCP server, at the path-suffixed
// address the MCP authorization spec tries first. Its resource is the server's
// own URL: RFC 9728 §3.3 has the client refuse metadata whose resource is not the
// one it asked about, so an origin here would be metadata for a different thing.
func (d *door) mcpResource() (*zip.Body, error) { return d.protected(d.origin + mcp.Path) }

func (d *door) protected(resource string) (*zip.Body, error) {
	owner, issuer := d.authority()
	return shared(mediaJSON, Resource{
		Resource:      resource,
		Servers:       []string{issuer},
		Methods:       []string{"header"},
		Scopes:        Scopes,
		Name:          brand.Display(owner.ID) + " API",
		Documentation: owner.Docs,
	})
}
