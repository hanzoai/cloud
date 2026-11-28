package brand

import (
	"maps"
	"net"
	"slices"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Info is the PUBLIC per-brand identity used for token validation + URL scoping.
// No secrets.
type Info struct {
	// ID is the canonical brand key.
	ID string
	// IAMIssuer is the OIDC issuer (JWKS source) for this brand — the value the
	// JWT `iss` claim must equal and whose /v1/iam/.well-known/jwks signs tokens.
	IAMIssuer string
	// Discovery is the URL of this brand's OIDC discovery document. It is the one
	// input that says who signs the brand's tokens: the issuer and the JWKS come from
	// the document it names, never from a value kept here.
	Discovery string
	// AlsoIssuers are further issuers whose tokens are this brand's too while an issuer
	// moves: verified exactly, never minted under, empty once the move is over.
	AlsoIssuers []string
	// Domain is the brand's primary marketing/site domain (for response scoping
	// and base-URL derivation, e.g. api.<Domain>).
	Domain string
	// AltDomains are additional registrable domains that ALSO belong to this
	// brand: a request Host under one brands as it (ForHostOK), and an identity
	// host under one mints under its issuer (IssuerByHost). A brand's real serving
	// surfaces span more than its marketing domain — the cloud console runs on
	// <brand>.cloud hosts (console.lux.cloud, console.zoo.cloud) — and a request
	// Host there must brand as its own brand, never fall through to Hanzo.
	// Base-URL/issuer scoping still uses the primary Domain.
	AltDomains []string
	// Framers are the ORIGINS of this brand's own surfaces that show a page of
	// ours in a frame — a sandbox's terminal and its screen (apps/sandbox/page.go).
	// Each one named, and never a wildcard: an apex whose subdomains are CUSTOMER
	// sites (every <slug>.hanzo.app is one) would otherwise let any customer frame
	// a page that types into somebody's sandbox. A surface that starts framing one
	// is added here.
	Framers []string
	// Returns are the PAGES of this brand an OAuth connect may send a person back
	// to (apps/provider/back.go): the page that started a connect names itself,
	// and a deployment of this brand returns there only when it is one of these,
	// exactly — scheme, host, path and the app address `at` — and never another
	// brand's. Pages and not origins: a free path on an origin reaches wherever
	// that origin redirects with the query kept, and the return carries a grant.
	Returns []string
	// SignIn are hosts beyond the identity hosts that a person signs in ON, each
	// one exact: the host and none of its subdomains. Hanzo's sign-in page is
	// hanzo.ai/login, so hanzo.ai is one, while console.hanzo.ai and every
	// <slug>.hanzo.app are not. A host named here gets the credentialed answer on
	// the routes that set or spend the session (IdentityHosts).
	SignIn []string
	// Console is the host a person reaches this brand's cloud console on. It is
	// stated, not derived: Hanzo's console sits under its primary Domain and Lux's
	// and Zoo's under a <brand>.cloud alternate, so no rule over the domains names
	// all of them. Empty for a brand that runs no console of its own.
	//
	// Hanzo's is platform.hanzo.ai, the name its people know it by: console.hanzo.ai
	// and console2 serve the same image (Framers), but a page a refusal or a manifest
	// names a person to is named on platform.
	Console string
	// Sites are the first-party registrable domains beyond Domain and AltDomains:
	// the brand's product sites (hanzo.team, hanzo.chat, lux.exchange), its
	// identity host, its governance and testnet zones. The apex and every
	// subdomain of each is first-party (Owned). An entry may be a single host
	// inside a Published namespace (intel.hanzo.app) to make just that host so.
	Sites []string
	// Published are the domains customers publish sites under — every
	// <slug>.hanzo.app is a customer's. The apex is first-party; a subdomain is
	// not, and is never given a credentialed cross-origin answer (Published).
	Published []string
	// Sales is the mailbox a sales inquiry from the brand's public form is told
	// at (apps/marketing leads.go). Empty for a brand that takes none: its leads
	// are filed and nobody is mailed.
	Sales string
	// Careers is the mailbox a job application from the brand's careers form is
	// told at (apps/marketing leads.go). Empty for a brand that takes none: its
	// candidates are filed and nobody is mailed.
	Careers string
	// Intro is the page a lead books the brand's intro call on, opened with the
	// lead's name, address and reference filled in (apps/crm). Empty for a brand
	// that books no calls: its leads carry no link.
	Intro string
	// CRM is the host the brand's team works its leads on; a lead's mail links
	// there. Empty for a brand with no CRM host of its own.
	CRM string
	// Postal is the postal address the brand's mail to the public states under
	// its name, on one line, as the brand's legal pages and invoices give it
	// (apps/marketing newsletter.go). Empty for a brand that states none.
	Postal string
	// Agent is the brand's agent manifest: the one document an agent reads to
	// get a key, pay and call the API with no person in the loop. Every surface
	// names it the same way (Agent), so a caller that lands anywhere is one hop
	// from it. Empty for a brand that publishes none: its surfaces name nothing.
	Agent string
	// Wallet reports that the brand's IAM opens an account for a wallet it has
	// never seen, from a signed CAIP-122 message alone (/v1/iam/web3), so an
	// agent holding a key pair needs no person to start.
	Wallet bool
	// Guide is the brand's page for agents: what an agent can do, and how to
	// start. The API host's catalog names it beside the documents it serves.
	// Empty for a brand that publishes none.
	Guide string
	// Docs is the brand's documentation site, and Reference the page on it that
	// documents the API. The API host links both (RFC 8631 service-doc). Empty
	// for a brand that publishes none.
	Docs      string
	Reference string
	// Status is the brand's status page, which the API catalog names (RFC 9727
	// status). Empty for a brand that runs none.
	Status string
	// Login is the command a person runs to sign a terminal in through the
	// brand's IAM device flow. Empty for a brand that ships no CLI.
	Login string
	// Pay is the brand's checkout: a payer reads an invoice the brand issued at
	// <Pay>/invoice/<link>, and a person adds credit at <Pay>/amount/card
	// (AddCredit). Empty for a brand that takes no payment there.
	Pay string
}

// brands is the brand→IAM registry. Keys are the canonical brand IDs accepted
// by CLOUD_BRAND. Per HIP-0111 §Brands: hanzo→hanzo.id, lux→lux.id,
// zoo→zoolabs.id (zoo.id does not resolve; the live IAM stamps iss=zoolabs.id
// — verified against /.well-known/openid-configuration), pars→pars.id,
// bootnode→id.bootno.de.
//
// IAMIssuer MUST equal the `iss` IAM actually stamps AND host the signing JWKS.
// For hanzo the live .well-known/openid-configuration on hanzo.ai reports
// issuer=https://hanzo.ai; hanzo.id stays trusted in AlsoIssuers during/after
// the move so existing tokens minted under https://hanzo.id remain valid.
// lux/zoo/pars already correctly point at their own .id issuers.
var brands = map[string]Info{
	"hanzo": {ID: "hanzo", IAMIssuer: "https://hanzo.ai", Discovery: "https://hanzo.ai/.well-known/openid-configuration", AlsoIssuers: []string{"https://hanzo.id"}, Domain: "hanzo.ai", AltDomains: []string{"hanzo.cloud", "hanzo.app"}, Console: "platform.hanzo.ai",
		SignIn:    []string{"hanzo.ai", "hanzo.id"},
		Published: []string{"hanzo.app"},
		Sites: []string{"hanzo.id", "hanzo.bot", "hanzo.chat", "hanzo.team", "hanzo.build", "hanzo.market",
			"hanzo.vote", "hanzo.agency", "hanzo.industries", "hanzo.network", "hanzoskills.com", "intel.hanzo.app"},
		// The site and app apexes, the builder (hanzo.build, build.hanzo.ai),
		// tabs, and the console under each name it is served at — console2,
		// platform, platform2, admin and growth are the same console image.
		// The Bots app draws a bot's computer in a frame wherever it is served:
		// hanzo.bot (and bot.hanzo.ai, the same site) and hanzo.team, beside
		// hanzo.ai and hanzo.app. Without them its screen answered "refused to
		// connect" there.
		Framers: []string{"https://hanzo.ai", "https://hanzo.app", "https://hanzo.cloud",
			"https://hanzo.bot", "https://bot.hanzo.ai", "https://hanzo.team", "https://hanzo.chat",
			"https://hanzo.build", "https://www.hanzo.build", "https://build.hanzo.ai", "https://tabs.hanzo.ai",
			"https://console.hanzo.ai", "https://console2.hanzo.ai", "https://platform.hanzo.ai",
			"https://platform2.hanzo.ai", "https://admin.hanzo.ai", "https://growth.hanzo.ai"},
		// Settings → Integrations and Sync in the app's Dev mode, Dev's Projects
		// and Sync at their paths (/dev/projects, /dev/sync: where the GitHub
		// banner and Sync start a connect now), on hanzo.app, the app's host, and
		// on hanzo.ai while a connect it started is still out, the console's
		// connectors page under the two names it is served at, and Social's
		// Connect screen, where a person who connected a network goes on to post.
		Returns: []string{"https://hanzo.app/?at=-/settings/integrations", "https://hanzo.app/?at=-/sync",
			"https://hanzo.app/dev/projects", "https://hanzo.app/dev/sync",
			"https://hanzo.ai/?at=-/settings/integrations", "https://hanzo.ai/?at=-/sync",
			"https://hanzo.ai/dev/projects", "https://hanzo.ai/dev/sync",
			"https://hanzo.bot/bots", "https://hanzo.team/bots",
			"https://console.hanzo.ai/connectors", "https://platform.hanzo.ai/connectors",
			"https://social.hanzo.ai/accounts",
			"https://post.hanzo.ai/", "https://post.hanzo.ai/launches"},
		// The form at hanzo.ai/contact-sales names the same mailbox and the same
		// booking page to the visitor, and hanzo.ai/careers the careers mailbox.
		Sales: "enterprise@hanzo.ai", Careers: "careers@hanzo.ai", Intro: "https://cal.hanzo.ai/hanzo/intro", CRM: "crm.hanzo.ai",
		// hanzo.ai/legal/terms and /legal/privacy, and commerce's invoice PDF.
		Postal: "1 Ferry Building, San Francisco, CA 94105, USA",
		Agent:  "https://hanzoskills.com/hanzo.md", Wallet: true, Guide: "https://hanzo.ai/agents",
		Docs: "https://docs.hanzo.ai", Reference: "https://docs.hanzo.ai/docs/openapi", Status: "https://status.hanzo.ai",
		Login: "hanzo auth login", Pay: "https://hanzo.ai/pay"},
	"lux": {ID: "lux", IAMIssuer: "https://lux.id", Discovery: "https://lux.id/.well-known/openid-configuration", Domain: "lux.network", AltDomains: []string{"lux.cloud"}, Console: "console.lux.cloud",
		// osage.id is the Osage L1's identity host, run by the Lux estate.
		Sites: []string{"lux.id", "lux.exchange", "lux.market", "lux.chat", "lux.vote", "lux.fund", "lux.credit",
			"lux.tel", "lux.financial", "luxbitco.in", "lux-test.network", "osage.id"},
		Framers: []string{"https://console.lux.cloud"},
		Returns: []string{"https://console.lux.cloud/connectors"},
		Agent:   "https://lux.network/llms.txt"},
	"zoo": {ID: "zoo", IAMIssuer: "https://zoolabs.id", Discovery: "https://zoolabs.id/.well-known/openid-configuration", Domain: "zoo.ngo", AltDomains: []string{"zoo.network", "zoo.cloud"}, Console: "console.zoo.cloud",
		Sites:   []string{"zoolabs.id", "zoolabs.io", "zoo.fund", "zoo.exchange", "zoo-test.network"},
		Framers: []string{"https://console.zoo.cloud"},
		Returns: []string{"https://console.zoo.cloud/connectors"},
		Agent:   "https://zoo.ngo/llms.txt"},
	"pars": {ID: "pars", IAMIssuer: "https://pars.id", Discovery: "https://pars.id/.well-known/openid-configuration", Domain: "pars.network", AltDomains: []string{"pars.ai"},
		Sites: []string{"pars.id", "pars.vote"}},
	"bootnode": {ID: "bootnode", IAMIssuer: "https://id.bootno.de", Discovery: "https://id.bootno.de/.well-known/openid-configuration", Domain: "bootno.de"},
}

// Default is the fallback brand when CLOUD_BRAND is unknown.
const Default = "hanzo"

// For returns the Info for id, falling back to the Hanzo brand for an unknown
// id. Lookup is case-insensitive.
func For(id string) Info {
	if b, ok := brands[strings.ToLower(strings.TrimSpace(id))]; ok {
		return b
	}
	return brands[Default]
}

// Registered reports whether id names a brand in the registry. It is the
// FALLIBLE half of [For], which cannot say so because it answers Hanzo for
// everything it does not know — the right default for rendering a title, and the
// wrong one for any decision that turns on which brand a caller belongs to.
// Lookup is case-insensitive, exactly like For.
func Registered(id string) bool {
	_, ok := brands[strings.ToLower(strings.TrimSpace(id))]
	return ok
}

// ForIssuer resolves the brand whose IAM minted a token, from the token's own
// verified `iss`. It is the reverse of [IssuerFor] over the SAME registry, so
// the two cannot name different brands for one issuer.
//
// It exists because the deployment's brand and the token's brand are two facts,
// not one: cloud accepts every white-label brand's issuer (trustedIssuers), so a
// process configured CLOUD_BRAND=hanzo can hold a validly-signed token minted by
// lux.id. Anything keyed by brand — a tenant key, a derived-key salt — has to be
// able to tell those apart, and it can only do that from a value the ISSUER
// signed rather than one the process assumed.
//
// ok is false for an issuer no brand claims, so a caller fails closed instead of
// silently folding an unknown issuer onto the default brand.
func ForIssuer(iss string) (string, bool) {
	iss = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(iss)), "/")
	if iss == "" {
		return "", false
	}
	for id, b := range brands {
		for _, own := range b.issuers() {
			if strings.TrimSuffix(strings.ToLower(own), "/") == iss {
				return id, true
			}
		}
	}
	return "", false
}

// issuers is every issuer whose tokens are this brand's.
func (b Info) issuers() []string {
	if b.IAMIssuer == "" {
		return nil
	}
	return append([]string{b.IAMIssuer}, b.AlsoIssuers...)
}

// Siblings are the other issuers of the brand issuer belongs to, for a verifier that holds
// one provider per issuer (the object store's STS).
func Siblings(issuer string) []string {
	issuer = strings.TrimSuffix(strings.TrimSpace(issuer), "/")
	for _, b := range brands {
		all := b.issuers()
		if !slices.ContainsFunc(all, func(i string) bool { return strings.TrimSuffix(i, "/") == issuer }) {
			continue
		}
		return slices.DeleteFunc(slices.Clone(all), func(i string) bool { return strings.TrimSuffix(i, "/") == issuer })
	}
	return nil
}

// Trusts reports whether iss is issuer or one of its brand's other issuers.
func Trusts(issuer, iss string) bool {
	return iss == issuer || slices.Contains(Siblings(issuer), iss)
}

// Issuer is the ONE resolution of "which IAM signs my token": a pinned value if
// there is one, else the brand's own. It trims, because an OIDC issuer is compared
// as a literal string — a pinned "https://hanzo.id/" and a derived
// "https://hanzo.id" are two different issuers to a validator, and the one that
// trims and the one that does not were both being used.
//
// It lives HERE, in the leaf, because its two callers cannot share anything
// heavier: package cloud resolves it for Config, and the light host cmd/cloud is
// built specifically NOT to link package cloud, so it had spelled the rule again
// inline — without the trim.
func Issuer(pinned, id string) string {
	if p := strings.TrimRight(strings.TrimSpace(pinned), "/"); p != "" {
		return p
	}
	return IssuerFor(id)
}

// IssuerFor returns the canonical OIDC issuer for a brand id.
func IssuerFor(id string) string {
	return For(id).IAMIssuer
}

// ForHostOK resolves a request Host to a brand id from the same `brands`
// registry, mirroring the hostname→brand semantics of platform.ts's
// getWhiteLabelBrand: a Host at or under a brand's Domain (api.lux.network,
// lux.network) is that brand. The port is stripped and the compare is
// case-insensitive; the longest matching Domain wins so a nested brand domain is
// never shadowed by a shorter one. ok is false when NO brand domain matches, so
// the caller can choose its own fallback (the deployment brand) rather than
// silently emitting Hanzo branding on, say, a Zoo pod hit with an odd Host.
func ForHostOK(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	// A fully-qualified Host may carry a trailing root dot ("api.lux.network.");
	// strip it so the suffix match still resolves the brand instead of failing to
	// neutral.
	host = strings.TrimSuffix(host, ".")
	best, bestLen := "", -1
	for id, b := range brands {
		for _, d := range append([]string{b.Domain}, b.AltDomains...) {
			d = strings.ToLower(d)
			if d == "" {
				continue
			}
			if (host == d || strings.HasSuffix(host, "."+d)) && len(d) > bestLen {
				best, bestLen = id, len(d)
			}
		}
	}
	return best, best != ""
}

// Console is the console host for the brand a request Host belongs to, so a page
// named to a Lux caller is on Lux's console and never on Hanzo's. Empty when that
// brand runs no console.
func Console(host string) string { return For(ForHost(host)).Console }

// ConsolePage is page (a path) on the console of the brand host belongs to, "" for
// a brand that runs no console: a link to a host that does not answer is worse
// than none.
func ConsolePage(host, page string) string {
	console := Console(host)
	if console == "" {
		return ""
	}
	return "https://" + console + page
}

// AddCredit is where a person adds credit to an account of the brand host belongs
// to: the amount step of its checkout (Pay), which every Add credits in Hanzo's
// apps already opens. "" for a brand with no checkout of its own, whose console's
// credits page is then the place (BillingURL).
func AddCredit(host string) string {
	pay := For(ForHost(host)).Pay
	if pay == "" {
		return ""
	}
	return pay + "/amount/card"
}

// Agent is the brand that tells an agent about host: the brand whose domain holds
// it, when that brand publishes a manifest (Info.Agent). ok is false for a host no
// brand's domain holds, for a customer's site inside a Published namespace, and
// for a brand with no manifest. It has NO default: a host nobody claims is told
// about nobody, so one brand's manifest never reaches another brand's surface,
// or a customer's.
func Agent(host string) (Info, bool) {
	host = canonical(host)
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i] // a port names no other site, so it cannot unpublish one
	}
	id, ok := ForHostOK(host)
	if !ok || Published(host) || brands[id].Agent == "" {
		return Info{}, false
	}
	return brands[id], true
}

// Claimed is Agent widened to the first-party sites a brand lists (Sites):
// hanzo.bot and hanzo.id are Hanzo's as surely as hanzo.ai is, though no request
// on them is answered as the brand's API. It decides only where the agent
// discovery documents answer — OAuth metadata and the MCP card (openapi/agent.go,
// apps/sites agentDocs) — so a scanner reading any of a brand's hosts learns how
// to reach its API (hanzo-inc/cloud#420, #421). Everything else keeps Agent.
func Claimed(host string) (Info, bool) {
	if b, ok := Agent(host); ok {
		return b, true
	}
	host = canonical(host)
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	for _, b := range brands {
		if b.Agent == "" {
			continue
		}
		for _, d := range b.Sites {
			if under(host, d) {
				return b, true
			}
		}
	}
	return Info{}, false
}

// ForHost is ForHostOK with the Hanzo default for an unmatched Host.
func ForHost(host string) string {
	if b, ok := ForHostOK(host); ok {
		return b
	}
	return Default
}

// Display is a brand id's human display name: the id with an upper-cased first
// letter (lux → "Lux", hanzo → "Hanzo"). Derived from the id — one source of
// truth with the brands registry, no hand-maintained display list. Used to build
// the white-label console <title>.
func Display(id string) string {
	if id == "" {
		id = Default
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// Issuers returns the OIDC issuer of every configured white-label brand. The
// in-binary identity validator (auth_identity.go) trusts a token whose `iss` is
// any of these, so ONE cloud binary validates hanzo AND lux/zoo/pars tokens. One
// source of truth: derived from the same `brands` registry above.
func Issuers() []string {
	out := make([]string, 0, len(brands))
	for _, b := range brands {
		out = append(out, b.issuers()...)
	}
	return out
}

// IssuerByHost maps every identity host this deployment serves to the issuer it
// mints under: the issuer's own host, plus each brand domain and alternate.
//
// Derived, because it was written by hand — a thirteen-entry blob duplicated in
// two deployment files, so a new brand was three edits from minting under
// another brand's issuer, and a brand's own clients reject that.
func IssuerByHost() map[string]string {
	out := make(map[string]string, len(brands)*3)
	for _, b := range brands {
		if b.IAMIssuer == "" {
			continue
		}
		for _, iss := range b.issuers() {
			if h := issuerHost(iss); h != "" {
				out[h] = b.IAMIssuer
			}
		}
		for _, d := range append([]string{b.Domain}, b.AltDomains...) {
			if d == "" {
				continue
			}
			// Served at the domain and at id./iam. of it — zoolabs.id and
			// id.zoo.network are one brand.
			out[d] = b.IAMIssuer
			out["id."+d] = b.IAMIssuer
			out["iam."+d] = b.IAMIssuer
		}
	}
	return out
}

// IdentityHosts returns every host a person signs in ON for a registered brand:
// its issuer's own host, the id. and iam. hosts of its primary domain — hanzo.id,
// id.hanzo.ai, iam.hanzo.ai, lux.id, id.lux.network, and so on — and the hosts
// its SignIn names, each exact (hanzo.ai, whose /login is Hanzo's sign-in page).
//
// The PRIMARY domain only, where IssuerByHost also walks the alternates. An
// alternate can be a surface customers publish on — every customer site is
// <slug>.hanzo.app — and a name a customer could hold is never a place people
// sign in. A host outside this rule that serves sign-in (osage.id, auth.hanzo.ai)
// is the operator's to pin, in IAM_ISSUER_MAP.
func IdentityHosts() []string {
	out := make([]string, 0, len(brands)*3)
	for _, b := range brands {
		if h := issuerHost(b.IAMIssuer); h != "" {
			out = append(out, h)
		}
		if b.Domain != "" {
			out = append(out, "id."+b.Domain, "iam."+b.Domain)
		}
		out = append(out, b.SignIn...)
	}
	return out
}

// issuerHost is the host an issuer URL names.
func issuerHost(issuer string) string {
	return strings.TrimPrefix(strings.TrimPrefix(issuer, "https://"), "http://")
}

// Framers returns every brand's Framers, sorted and each once: the origins a
// page of ours may be framed by, whichever brand's API host serves it — one
// binary serves every brand, so the page's policy names them all.
func Framers() []string {
	seen := map[string]bool{}
	for _, b := range brands {
		for _, f := range b.Framers {
			seen[f] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// Apex returns the registrable apex of a host: the domain one label below the
// public suffix ("api.hanzo.ai" -> "hanzo.ai", "hanzo.ai" -> "hanzo.ai"). It is
// THE ONE derivation of that value in this binary.
//
// It exists because a deployment's SIBLING hosts are its own: the forge
// (git.<apex>), CI (ci.<apex>), CD (cd.<apex>) and the status page
// (status.<apex>) are neither the API host nor children of it, and any code that
// must decide "is this host mine?" has to reduce to the apex first. Three
// packages needed that reduction and each wrote its own, by three different
// rules that agreed only on the three brand domains we happen to run today:
//
//	platform  publicsuffix                    api.acme.co.uk -> acme.co.uk
//	sites     last two labels                 api.acme.co.uk -> co.uk      (WRONG)
//	git       TrimPrefix "api." + "git."      cloud.hanzo.ai -> git.cloud.hanzo.ai (WRONG)
//
// The sites rule hands a bare public suffix to the self-domain floor, which is
// the set that decides a host is OURS and not a tenant's to claim — so a
// white-label on any multi-label suffix claimed every domain under it. The git
// rule advertised a forge host that platform's own allowlist would then refuse,
// which is verbatim the defect dc84b46d fixed in platform alone: the fix was
// correct and incomplete, because the derivation was duplicated rather than
// shared. One copy cannot disagree with itself.
//
// publicsuffix rather than "last two labels" so a multi-label suffix (co.uk,
// com.au, github.io) yields the registrable domain and not the suffix itself,
// which would trust — or claim — every domain under it. A name with no
// registrable form (localhost, a bare IP) is returned verbatim: it is not
// delegable, so it is its own apex.
func Apex(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return ""
	}
	if i := strings.IndexByte(h, ':'); i >= 0 { // tolerate host:port
		h = h[:i]
	}
	h = strings.TrimSuffix(h, ".") // a fully-qualified Host may carry the root dot
	apex, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil {
		return h
	}
	return apex
}

// Sibling is the host named `label` under host's registrable apex:
// Sibling("api.hanzo.ai", "git") == "git.hanzo.ai". It is how a deployment names
// the surfaces it owns beside its API — the forge, CI, CD, status — from the one
// domain it is configured with, so those names cannot drift apart per package.
func Sibling(host, label string) string {
	apex := Apex(host)
	if apex == "" || label == "" {
		return apex
	}
	return label + "." + apex
}

// APIHost is the public API host a deployment of brand `id` answers on:
// api.<that brand's apex> — api.hanzo.ai, api.lux.network, api.zoo.ngo.
//
// It is here, beside the apex it derives from, because two packages need the
// SAME answer and neither can hold it for the other: package cloud resolves
// Config.Domain from it, and apps/sites (a leaf that must never import the root
// package) resolves the self-domain floor from it. Each used to spell the
// literal "api.hanzo.ai" for itself, so the deployment's own host was stated
// twice, brand-blind in both places, and nothing made the two agree.
func APIHost(id string) string { return "api." + For(id).Domain }

// ZapHost is where brand `id` serves ZAP to browsers: zap.<that brand's apex>. It
// is a name of its own because a WebTransport session cannot cross the proxy the
// API host sits behind.
func ZapHost(id string) string { return "zap." + For(id).Domain }

// GitHost is the code-hosting host of brand `id`: git.<that brand's apex> —
// git.hanzo.ai, git.lux.network, git.zoo.ngo. Same derivation as APIHost and
// beside it for the same reason: a caller scoping a credential to the forge must
// name the forge, and a literal would be brand-blind.
func GitHost(id string) string { return "git." + For(id).Domain }

// Owned reports whether host is a first-party site: at or under a brand's Domain,
// AltDomain or Site. It is the one rule for who may receive a credentialed
// cross-origin answer, derived from this registry so a new brand or site is one
// edit here. A subdomain of a Published namespace is a customer's and is not
// Owned unless a Site names it; the namespace's apex is.
func Owned(host string) bool {
	host = canonical(host)
	for _, b := range brands {
		for _, d := range b.Sites {
			if under(host, d) {
				return true
			}
		}
		for _, d := range slices.Concat([]string{b.Domain}, b.AltDomains) {
			if d != "" && under(host, d) && (host == d || !slices.Contains(b.Published, d)) {
				return true
			}
		}
	}
	return false
}

// Published reports whether host is a customer's site inside a Published
// namespace (a.hanzo.app) and not itself first-party. Such an origin may read
// the API without credentials.
func Published(host string) bool {
	host = canonical(host)
	for _, b := range brands {
		for _, d := range b.Published {
			if host != d && under(host, d) && !Owned(host) {
				return true
			}
		}
	}
	return false
}

func canonical(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// under reports whether host is d or a subdomain of d.
func under(host, d string) bool { return host == d || strings.HasSuffix(host, "."+d) }

// Origin is the origin a client reached at host, where host is the request's own
// Host header and never a forwarded one: a caller sets X-Forwarded-Host to any
// name it likes, and an origin built from it hands that name back in a page or a
// challenge. Every public name is served over TLS, so the scheme is https; the
// plain-HTTP hop between this deployment's processes says nothing about it. Only
// this machine's own names (a name with no dot, localhost, .localhost, .local, a
// loopback or private address) are reached in the clear. The port stays as the
// client sent it.
func Origin(host string) string {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.Trim(strings.ToLower(name), "[]")
	// A name with no dot (a compose service, "cloud") is no public name either.
	local := !strings.Contains(name, ".") || name == "localhost" ||
		strings.HasSuffix(name, ".localhost") || strings.HasSuffix(name, ".local")
	if ip := net.ParseIP(name); ip != nil {
		local = ip.IsLoopback() || ip.IsPrivate()
	}
	if local {
		return "http://" + host
	}
	return "https://" + host
}

// Discoveries are the discovery URLs of every brand this binary fronts, in brand
// order, one per brand that states one.
func Discoveries() []string {
	var out []string
	for _, id := range slices.Sorted(maps.Keys(brands)) {
		b := brands[id]
		if b.Discovery != "" {
			out = append(out, b.Discovery)
		}
		// Every issuer the brand's tokens may carry is trusted by its own document.
		for _, iss := range b.issuers() {
			if d := strings.TrimSuffix(iss, "/") + "/.well-known/openid-configuration"; iss != "" && !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// DiscoveryFor is the discovery URL of a brand id, and empty when it states none.
func DiscoveryFor(id string) string { return For(id).Discovery }
