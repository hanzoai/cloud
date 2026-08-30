package seo

// typed.go is the PUBLISHED surface: seven typed ops, their request and answer
// shapes in our names, and the one function every one of them runs through.
//
// ONE REGISTRATION IS FOUR PROJECTIONS. A zip typed op becomes the REST route,
// the OpenAPI operation, the MCP tool and the CLI command from the same entry, and
// the operation id is the name in all four. So the ids are the product's
// vocabulary — seoKeyword, seoRank — and not derived verbs: a model choosing from
// a list reads the name before it reads anything else.

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/money"
	"github.com/zap-proto/zip"
)

// Go drops comments at compile time, so cmd/zipdoc is the ONLY path from a
// handler's prose to the published document, the SDKs and the MCP tool
// description. Its output is committed; `make zipdoc-check` fails on drift.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// The addresses, spelled once so the registration and the prose cannot disagree.
// Whole paths, declared on the app — a Group root composes to a trailing slash,
// which then lands in the document, the operation id and every generated SDK.
const (
	keywordPath    = "/v1/seo/keywords"
	ideaPath       = "/v1/seo/ideas"
	rankPath       = "/v1/seo/rankings"
	competitorPath = "/v1/seo/competitors"
	backlinkPath   = "/v1/seo/backlinks"
	auditPath      = "/v1/seo/audit"
	ratePath       = "/v1/seo/rates"
)

// Defaults and the bound on what one call may buy.
//
// The list endpoints are priced PER ROW, so an unbounded limit is an unbounded
// charge; maxRows is the vendor's own ceiling and defaultRows is what a caller who
// did not think about it gets. Both are stated here because the quote the caller
// is authorized against is computed from the limit, and a limit that could be
// anything is a quote that means nothing.
const (
	defaultRows = 100
	maxRows     = 1000

	// The vendor addresses a market by a numeric code and a language by an ISO one.
	// 2840 is the United States.
	//
	// The CODE is what this surface takes, and the vendor's name form is
	// deliberately not offered: several of their endpoints reject a location_name
	// they accept elsewhere, so a field that works on four ops and 400s on the fifth
	// would be a shape a caller cannot learn. One spelling, everywhere.
	defaultLocation = 2840
	defaultLanguage = "en"
)

type ops struct{ s *cloud.Service[*state] }

func routes(app cloud.Router, s *cloud.Service[*state]) {
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		s.Log.Error("seo: the router carries no typed-op registry; the surface would serve routes no projection knows")
		return
	}
	o := ops{s}

	// The six that ask the vendor a question are POSTs. They READ, and they are
	// still writes in the sense that matters here: each one spends money at an
	// upstream, and cloud prices by method — a GET costs nothing by rule. The bodies
	// also carry lists of phrases, which a query string is the wrong shape for.
	zip.Post(zapp, keywordPath, o.keyword,
		zip.WithOperationID("seoKeyword"),
		zip.WithSummary("How often named phrases are searched, and what a click costs"))
	zip.Post(zapp, ideaPath, o.idea,
		zip.WithOperationID("seoIdea"),
		zip.WithSummary("Grow a seed phrase into the phrases nobody named yet"))
	zip.Post(zapp, rankPath, o.rank,
		zip.WithOperationID("seoRank"),
		zip.WithSummary("Every phrase a domain already places for, with its position"))
	zip.Post(zapp, competitorPath, o.competitor,
		zip.WithOperationID("seoCompetitor"),
		zip.WithSummary("The domains that place for the same phrases"))
	zip.Post(zapp, backlinkPath, o.backlink,
		zip.WithOperationID("seoBacklink"),
		zip.WithSummary("Who links to a target, and how much of it is broken or spam"))
	zip.Post(zapp, auditPath, o.audit,
		zip.WithOperationID("seoAudit"),
		zip.WithSummary("Fetch one page and report what it gets wrong"))

	// The rate card is a GET and therefore free, which is the point: asking what a
	// call costs must not require the balance the call would spend.
	zip.Get(zapp, ratePath, o.rate,
		zip.WithOperationID("seoRate"),
		zip.WithSummary("What each call on this surface costs, from the vendor's own list"))
}

// run is the whole shape of one op, so the six do not each carry their own
// version of it.
//
// FOUR STEPS, IN THIS ORDER, AND THE ORDER IS THE POINT:
//
//  1. Refuse without a validated principal. This surface spends real money at a
//     vendor, and a caller with no ledger is a call nobody pays for. It fails
//     closed OFF the HTTP path too — the CLI projection invokes an op with no
//     request at all, and it lands here.
//  2. Authorize the caller's balance against the vendor's QUOTE, before the
//     call. A caller who cannot afford it is refused having spent nothing.
//  3. Make the call.
//  4. Debit exactly what the vendor CHARGED — their number, off their answer,
//     not a recomputation of it.
//
// A FAILED CALL IS STILL DEBITED WHEN THE VENDOR BILLED FOR IT, and that is
// deliberate rather than an oversight of the usual "failed work is not billed"
// rule. The usual rule holds where the work is ours and a failure costs us
// nothing; here a partly-served task is money that has already left. Debiting what
// they took keeps the two ledgers equal. A refusal that cost nothing — an
// unverified account, a malformed field — reports cost zero and debits nothing, so
// the common case is the same as the usual rule anyway.
func run[T any](ctx context.Context, o ops, t task, want int, body any) ([]T, money.Amount, error) {
	c, err := who(ctx)
	if err != nil {
		return nil, money.Zero(), err
	}
	ledger := principal.Payer(c)
	project, validated := principal.ValidatedProject(c)

	// CentsUp, never Cents: the vendor's cheapest call is $0.00012, and rounded to
	// nearest that is zero — a charge a spend cap would not weigh at all. Rounding
	// away from zero refuses a caller a fraction of a cent early rather than
	// admitting them for free.
	if err := o.s.Bill.Authorize(ctx, ledger, project, validated, t.kind, o.s.State.quote(ctx, t, want).CentsUp()); err != nil {
		return nil, money.Zero(), cloud.Denied(err)
	}

	out, charged, err := rows[T](ctx, o.s.State, t, body)
	if !charged.IsZero() {
		u := metering.Usage{
			Amount:    charged,
			Model:     t.kind,
			Project:   project,
			Actor:     c.User(),
			RequestID: c.RequestID(),
			ClientIP:  cloud.ClientIP(c),
		}
		if err != nil {
			u.Status = "error"
		}
		// Ref is deliberately left unset so the meter mints one. It is the ledger's
		// idempotency key and it names an ACT, never a thing: keyed on a phrase or a
		// domain, the second search for the same word would move no money at all.
		o.s.Bill.Record(ledger, t.kind, u)
	}
	return out, charged, err
}

// who is the standard preamble: this surface serves a VALIDATED principal and
// nobody else.
//
// THE TWO FACTS ARE NOT THE SAME FACT, and reading one for the other is the whole
// point of this function. cloud.Request says a request EXISTS; ValidatedFrom says
// the identity middleware minted the caller from a verified credential rather than
// the caller writing X-Org-Id on a bearer-less request themselves. A surface that
// checks only the first admits a forged header, and this one spends money at a
// vendor per call.
//
// It fails closed OFF the HTTP path too, where there is no request at all: the CLI
// projection invokes an op with nothing to read, and the honest answer there is
// that there is no principal — not that there is a default one.
func who(ctx context.Context) (*zip.Ctx, error) {
	c, live := cloud.Request(ctx)
	if !live || !principal.ValidatedFrom(ctx) {
		return nil, zip.ErrForbidden("no validated principal")
	}
	return c, nil
}

// market fills in the location and language a caller left out, and bounds what one
// call may buy. One function, so seven ops cannot disagree about the defaults.
func market(location *int, language *string) {
	if *location <= 0 {
		*location = defaultLocation
	}
	if strings.TrimSpace(*language) == "" {
		*language = defaultLanguage
	}
}

func bounded(limit int) int {
	switch {
	case limit <= 0:
		return defaultRows
	case limit > maxRows:
		return maxRows
	}
	return limit
}

// phrases refuses an empty ask. The vendor charges for a request with no keywords
// in it, and answers nothing.
func phrases(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, k := range in {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return nil, zip.ErrBadRequest("name at least one keyword")
	}
	return out, nil
}

func target(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", zip.ErrBadRequest("name a domain")
	}
	return s, nil
}

// ── the measurements ─────────────────────────────────────────────────────────

// seoMetric is one phrase and what it is worth. It is the answer to both
// seoKeyword and seoIdea, because an idea IS a keyword with metrics — the two ops
// differ in whether you named the phrase, not in what comes back about it.
type seoMetric struct {
	// Keyword is the phrase.
	Keyword string `json:"keyword"`
	// Volume is the average monthly searches.
	Volume int `json:"volume"`
	// CPC is the average cost of one advertising click, in USD. It is a reported
	// statistic about somebody else's auction, not an amount this API moves.
	CPC float64 `json:"cpc"`
	// Competition is how contested the advertising is, from 0 to 1. The upstream
	// reports it as an index out of a hundred on one endpoint and as this fraction
	// on another; it is the fraction here in both cases.
	Competition float64 `json:"competition"`
	// Level is the same fact as a word: low, medium or high.
	Level string `json:"level,omitempty"`
	// Difficulty is how hard the first page is to reach organically, 0 to 100.
	// Present on seoIdea, which measures it; absent on seoKeyword, which does not.
	Difficulty int `json:"difficulty,omitempty"`
}

// seoRanking is one phrase a domain places for, and where.
type seoRanking struct {
	// Keyword is the phrase searched.
	Keyword string `json:"keyword"`
	// Position is the absolute rank on the results page, counting every element —
	// so it is what a person scrolling actually passes, not the organic-only rank.
	Position int `json:"position"`
	// URL is the page of the target that placed.
	URL string `json:"url"`
	// Title is that result's headline.
	Title string `json:"title,omitempty"`
	// Volume is the phrase's average monthly searches.
	Volume int `json:"volume"`
	// Traffic is the estimated monthly visits this placement earns.
	Traffic float64 `json:"traffic"`
}

// seoDomain is one competitor and how it does across the phrases asked about.
type seoDomain struct {
	// Domain is the competitor.
	Domain string `json:"domain"`
	// Position is its average rank across the phrases.
	Position float64 `json:"position"`
	// Keywords is how many of the phrases it places for.
	Keywords int `json:"keywords"`
	// Visibility is its share of the possible attention across those phrases.
	Visibility float64 `json:"visibility"`
	// Traffic is the estimated monthly visits those placements earn.
	Traffic float64 `json:"traffic"`
}

// ── seoKeyword ───────────────────────────────────────────────────────────────

// seoKeywordIn names the phrases to measure.
type seoKeywordIn struct {
	// Keywords are the phrases. At least one; blanks are dropped.
	Keywords []string `json:"keywords"`
	// Location is the market, as the upstream's numeric code. Defaults to 2840,
	// the United States.
	Location int `json:"location,omitempty"`
	// Language is the ISO code. Defaults to "en".
	Language string `json:"language,omitempty"`
}

// seoKeywordOut is what those phrases are worth.
type seoKeywordOut struct {
	// Keywords is one measurement per phrase, in the order the upstream answered.
	Keywords []seoMetric `json:"keywords"`
	// Cost is what this call cost, in USD, as an exact decimal string. It is the
	// upstream's own number and it is what was debited.
	Cost string `json:"cost"`
}

// keyword measures phrases the caller already has.
//
// It answers, for each phrase named, how many people search it in a month, what an
// advertising click on it costs, and how contested that advertising is. This is the
// ground fact of search: everything else on this surface is a question about
// phrases, and this is the one that says whether a phrase is worth having.
//
// Give it phrases you already suspect. To find phrases you have not thought of,
// use seoIdea; to find the ones a site already places for, use seoRank.
//
// The market defaults to the United States in English. It is priced per request
// rather than per phrase, so asking about fifty phrases costs what asking about
// one does.
func (o ops) keyword(ctx context.Context, in *seoKeywordIn) (*seoKeywordOut, error) {
	words, err := phrases(in.Keywords)
	if err != nil {
		return nil, err
	}
	market(&in.Location, &in.Language)
	found, charged, err := run[volume](ctx, o, keyword, len(words), map[string]any{
		"keywords":      words,
		"location_code": in.Location,
		"language_code": in.Language,
	})
	if err != nil {
		return nil, err
	}
	out := &seoKeywordOut{Keywords: make([]seoMetric, 0, len(found)), Cost: charged.String()}
	for _, v := range found {
		out.Keywords = append(out.Keywords, seoMetric{
			Keyword: v.Keyword,
			Volume:  v.Volume,
			CPC:     v.CPC,
			// The index is out of a hundred and the published field is a fraction.
			Competition: float64(v.Index) / 100,
			Level:       strings.ToLower(v.Level),
		})
	}
	return out, nil
}

// ── seoIdea ──────────────────────────────────────────────────────────────────

// seoIdeaIn names the seeds to grow from.
type seoIdeaIn struct {
	// Keywords are the seeds. At least one; blanks are dropped.
	Keywords []string `json:"keywords"`
	// Location is the market, as the upstream's numeric code. Defaults to 2840.
	Location int `json:"location,omitempty"`
	// Language is the ISO code. Defaults to "en".
	Language string `json:"language,omitempty"`
	// Limit is how many phrases to return, 1 to 1000. Defaults to 100. It is what
	// this call is priced on, because the upstream charges per row.
	Limit int `json:"limit,omitempty"`
}

// seoIdeaOut is the phrases grown from those seeds.
type seoIdeaOut struct {
	// Keywords is the phrases found, each measured.
	Keywords []seoMetric `json:"keywords"`
	// Total is how many the upstream holds, which is usually more than Limit
	// returned — it is what raising the limit would reach.
	Total int `json:"total"`
	// Cost is what this call cost, in USD, as an exact decimal string.
	Cost string `json:"cost"`
}

// idea grows a seed phrase into the phrases nobody named yet.
//
// It takes phrases you have and returns phrases in the same category that you do
// not — relevant rather than merely containing the seed — each with its search
// volume, click cost, competition and how hard its first page is to reach. This is
// where a keyword list comes FROM; seoKeyword is where a list you already have gets
// measured.
//
// It is priced per row, so Limit is the knob that decides what the call costs.
// Total says how many more there were.
func (o ops) idea(ctx context.Context, in *seoIdeaIn) (*seoIdeaOut, error) {
	seeds, err := phrases(in.Keywords)
	if err != nil {
		return nil, err
	}
	market(&in.Location, &in.Language)
	in.Limit = bounded(in.Limit)
	found, charged, err := run[ideas](ctx, o, idea, in.Limit, map[string]any{
		"keywords":      seeds,
		"location_code": in.Location,
		"language_code": in.Language,
		"limit":         in.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := &seoIdeaOut{Keywords: []seoMetric{}, Cost: charged.String()}
	if len(found) == 0 {
		return out, nil
	}
	out.Total = found[0].Total
	for _, it := range found[0].Items {
		out.Keywords = append(out.Keywords, seoMetric{
			Keyword:     it.Keyword,
			Volume:      it.Info.Volume,
			CPC:         it.Info.CPC,
			Competition: it.Info.Competition,
			Level:       strings.ToLower(it.Info.Level),
			Difficulty:  it.Properties.Difficulty,
		})
	}
	return out, nil
}

// ── seoRank ──────────────────────────────────────────────────────────────────

// seoRankIn names the domain to look up.
type seoRankIn struct {
	// Domain is the site, with or without a subdomain — "hanzo.ai", "docs.hanzo.ai".
	Domain string `json:"domain"`
	// Location is the market, as the upstream's numeric code. Defaults to 2840.
	Location int `json:"location,omitempty"`
	// Language is the ISO code. Defaults to "en".
	Language string `json:"language,omitempty"`
	// Limit is how many placements to return, 1 to 1000. Defaults to 100.
	Limit int `json:"limit,omitempty"`
}

// seoRankOut is where that domain places.
type seoRankOut struct {
	// Rankings is one row per phrase the domain places for.
	Rankings []seoRanking `json:"rankings"`
	// Total is how many placements the upstream holds for this domain.
	Total int `json:"total"`
	// Cost is what this call cost, in USD, as an exact decimal string.
	Cost string `json:"cost"`
}

// rank reports every phrase a domain already places for.
//
// For each one it gives the phrase, the position on the results page, the page of
// the site that placed, that result's headline, the phrase's monthly searches and
// the visits the placement is estimated to earn. It is the single most direct
// question about a site's search visibility — yours or a competitor's, since it
// takes any domain.
//
// Position is the ABSOLUTE rank, counting every element on the page — the ads, the
// answer boxes, the map — because that is what a person scrolling actually passes.
// An organic-only rank flatters a result that sits below half a screen of other
// things.
//
// It is priced per row, so Limit decides what the call costs, and Total says how
// many more there were.
func (o ops) rank(ctx context.Context, in *seoRankIn) (*seoRankOut, error) {
	domain, err := target(in.Domain)
	if err != nil {
		return nil, err
	}
	market(&in.Location, &in.Language)
	in.Limit = bounded(in.Limit)
	found, charged, err := run[ranked](ctx, o, rank, in.Limit, map[string]any{
		"target":        domain,
		"location_code": in.Location,
		"language_code": in.Language,
		"limit":         in.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := &seoRankOut{Rankings: []seoRanking{}, Cost: charged.String()}
	if len(found) == 0 {
		return out, nil
	}
	out.Total = found[0].Total
	for _, it := range found[0].Items {
		out.Rankings = append(out.Rankings, seoRanking{
			Keyword:  it.Keyword.Keyword,
			Position: it.Element.Item.Rank,
			URL:      it.Element.Item.URL,
			Title:    it.Element.Item.Title,
			Volume:   it.Keyword.Info.Volume,
			Traffic:  it.Element.Item.Traffic,
		})
	}
	return out, nil
}

// ── seoCompetitor ────────────────────────────────────────────────────────────

// seoCompetitorIn names the phrases to compete on.
type seoCompetitorIn struct {
	// Keywords are the phrases. At least one; blanks are dropped.
	Keywords []string `json:"keywords"`
	// Location is the market, as the upstream's numeric code. Defaults to 2840.
	Location int `json:"location,omitempty"`
	// Language is the ISO code. Defaults to "en".
	Language string `json:"language,omitempty"`
	// Limit is how many domains to return, 1 to 1000. Defaults to 100.
	Limit int `json:"limit,omitempty"`
}

// seoCompetitorOut is who else places for them.
type seoCompetitorOut struct {
	// Competitors is one row per domain, strongest first.
	Competitors []seoDomain `json:"competitors"`
	// Total is how many domains the upstream holds for these phrases.
	Total int `json:"total"`
	// Cost is what this call cost, in USD, as an exact decimal string.
	Cost string `json:"cost"`
}

// competitor names the domains that place for the same phrases.
//
// Given a set of phrases it returns the sites that appear across them, with each
// one's average position, how many of the phrases it places for, its share of the
// available attention and the visits that earns. It answers "who am I actually up
// against here", which is a different question from "who do I think my competitors
// are" and frequently a different answer.
//
// Pair it with seoRank: this says who is in the race, seoRank says where any one of
// them finishes. It is priced per row, so Limit decides the cost.
func (o ops) competitor(ctx context.Context, in *seoCompetitorIn) (*seoCompetitorOut, error) {
	words, err := phrases(in.Keywords)
	if err != nil {
		return nil, err
	}
	market(&in.Location, &in.Language)
	in.Limit = bounded(in.Limit)
	found, charged, err := run[rivals](ctx, o, competitor, in.Limit, map[string]any{
		"keywords":      words,
		"location_code": in.Location,
		"language_code": in.Language,
		"limit":         in.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := &seoCompetitorOut{Competitors: []seoDomain{}, Cost: charged.String()}
	if len(found) == 0 {
		return out, nil
	}
	out.Total = found[0].Total
	for _, it := range found[0].Items {
		out.Competitors = append(out.Competitors, seoDomain{
			Domain:     it.Domain,
			Position:   it.Position,
			Keywords:   it.Keywords,
			Visibility: it.Visibility,
			Traffic:    it.Traffic,
		})
	}
	return out, nil
}

// ── seoBacklink ──────────────────────────────────────────────────────────────

// seoBacklinkIn names the target to summarise.
type seoBacklinkIn struct {
	// Target is a domain, a subdomain or a single page URL. A domain summarises the
	// whole site; a URL summarises that page.
	Target string `json:"target"`
}

// seoBacklinkOut is that target's link profile in one row.
type seoBacklinkOut struct {
	// Target is the target as the upstream resolved it.
	Target string `json:"target"`
	// Rank is the upstream's authority score for the target, 0 to 1000.
	Rank int `json:"rank"`
	// Backlinks is how many links point at it.
	Backlinks int `json:"backlinks"`
	// Domains is how many distinct sites those links come from — the number that
	// matters, since a thousand links from one site is one site.
	Domains int `json:"domains"`
	// Pages is how many distinct pages link in.
	Pages int `json:"pages"`
	// Broken is how many of those links point at something that no longer answers.
	Broken int `json:"broken"`
	// Spam is the share of the profile judged spam, 0 to 100.
	Spam int `json:"spam"`
	// FirstSeen is when the upstream first saw a link to this target, RFC 3339.
	FirstSeen string `json:"firstSeen,omitempty"`
	// Cost is what this call cost, in USD, as an exact decimal string.
	Cost string `json:"cost"`
}

// backlink summarises who links to a target.
//
// It returns the authority score, how many links point at it and from how many
// distinct sites, how many of those are broken, and how much of the profile reads
// as spam. Distinct sites is the number to read: a thousand links from one domain
// is one endorsement, and a profile that grew fast in links and not in domains is
// usually a profile somebody bought.
//
// The target can be a whole domain, a subdomain, or one page URL — the summary is
// scoped to whatever is named. It is priced per request, so a domain with ten
// million links costs the same as one with ten.
func (o ops) backlink(ctx context.Context, in *seoBacklinkIn) (*seoBacklinkOut, error) {
	site, err := target(in.Target)
	if err != nil {
		return nil, err
	}
	found, charged, err := run[links](ctx, o, backlink, 1, map[string]any{
		"target": site,
		// The live profile, not everything ever seen: a summary that counts links
		// which were removed years ago describes a site that no longer exists.
		"backlinks_status_type": "live",
	})
	if err != nil {
		return nil, err
	}
	out := &seoBacklinkOut{Target: site, Cost: charged.String()}
	if len(found) == 0 {
		return out, nil
	}
	l := found[0]
	if l.Target != "" {
		out.Target = l.Target
	}
	out.Rank, out.Backlinks, out.Domains = l.Rank, l.Backlinks, l.Domains
	out.Pages, out.Broken, out.Spam = l.Pages, l.Broken, l.Spam
	out.FirstSeen = l.FirstSeen
	return out, nil
}

// ── seoAudit ─────────────────────────────────────────────────────────────────

// seoAuditIn names the page to check.
type seoAuditIn struct {
	// URL is the page, absolute and http or https.
	URL string `json:"url"`
}

// seoAuditOut is what that page gets right and wrong.
type seoAuditOut struct {
	// URL is the address actually read, after redirects.
	URL string `json:"url"`
	// Status is the HTTP status the page answered with.
	Status int `json:"status"`
	// Score is the upstream's on-page score, 0 to 100.
	Score float64 `json:"score"`
	// Title is the page's title.
	Title string `json:"title,omitempty"`
	// Description is its meta description.
	Description string `json:"description,omitempty"`
	// Words is how many words of readable text the page carries.
	Words int `json:"words,omitempty"`
	// Checks is every named finding, each a yes or no — "is_https", "no_h1_tag",
	// "high_loading_time", around fifty of them. It is an OPEN set the upstream adds
	// to, so it is published as an object of booleans rather than as fifty declared
	// fields that would be wrong the next time they name a fifty-first.
	Checks map[string]bool `json:"checks,omitempty"`
	// Cost is what this call cost, in USD, as an exact decimal string.
	Cost string `json:"cost"`
}

// audit fetches one page and reports what it gets wrong.
//
// It returns the page's on-page score, its title and description, how much readable
// text it carries, and the full set of named checks — is it https, does it have one
// h1, is the title duplicated, is it slow, is it a redirect, is anything on it
// broken. It is the technical half of search visibility, and it is the half a
// developer can act on this afternoon.
//
// ONE PAGE, LIVE, IN THIS REQUEST. It is deliberately not a site crawl: a crawl is
// a job with a lifecycle, and this answers the same questions about the page
// somebody is actually looking at, now, with no task id to poll. Point it at the
// pages that matter one at a time.
//
// It is priced per page fetched, which is one.
func (o ops) audit(ctx context.Context, in *seoAuditIn) (*seoAuditOut, error) {
	url := strings.TrimSpace(in.URL)
	if url == "" {
		return nil, zip.ErrBadRequest("name a url")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, zip.ErrBadRequest("the url must be absolute and http or https")
	}
	found, charged, err := run[page](ctx, o, audit, 1, map[string]any{"url": url})
	if err != nil {
		return nil, err
	}
	out := &seoAuditOut{URL: url, Cost: charged.String()}
	if len(found) == 0 || len(found[0].Items) == 0 {
		return out, nil
	}
	it := found[0].Items[0]
	if it.URL != "" {
		out.URL = it.URL
	}
	out.Status, out.Score = it.Status, it.Score
	out.Title, out.Description = it.Meta.Title, it.Meta.Description
	out.Words, out.Checks = it.Meta.Content.Words, it.Checks
	return out, nil
}

// ── seoRate ──────────────────────────────────────────────────────────────────

// seoRateIn takes nothing. The rate card is the same for everyone.
type seoRateIn struct{}

// seoCharge is what one op costs.
type seoCharge struct {
	// Op is the operation id — seoKeyword, seoRank — so a line here and a tool in a
	// model's list are the same name.
	Op string `json:"op"`
	// Request is the flat charge for making the call, in USD, as an exact decimal
	// string.
	Request string `json:"request"`
	// Result is the charge for each row returned, in USD, as an exact decimal
	// string. Zero for the ops priced per request.
	Result string `json:"result"`
}

// seoRateOut is the whole card.
type seoRateOut struct {
	// Rates is one row per op on this surface.
	Rates []seoCharge `json:"rates"`
}

// rate publishes what every call on this surface costs.
//
// The numbers are read from the upstream's own published price list, not from a
// table kept here, so a price change on their side moves this card within the hour
// and moves what is debited with it. That is the whole of the pricing model: this
// surface resells at cost, and the cost is theirs to state.
//
// A row has two numbers because a call has two costs: a flat charge for asking, and
// a charge per row returned. An op priced per request reports zero for the second,
// and for one priced per row the total is `request + result x limit` — which is the
// amount your balance is authorized against before the call, and roughly what you
// will be debited after it.
//
// It is a read and it is free: asking what something costs must not require the
// balance that would pay for it. If the upstream cannot be reached the card comes
// back empty rather than stale — a price nobody can confirm is not a price.
func (o ops) rate(ctx context.Context, _ *seoRateIn) (*seoRateOut, error) {
	// Free, but not open: publishing the card costs a fetch at the vendor, and a
	// surface that answers an unvalidated caller answers a forged header.
	if _, err := who(ctx); err != nil {
		return nil, err
	}
	card := o.s.State.charges(ctx)
	out := &seoRateOut{Rates: make([]seoCharge, 0, len(card))}
	// The six, in the order they are registered — the order somebody reads them in,
	// not whatever a map iteration produces.
	for _, t := range []task{keyword, idea, rank, competitor, backlink, audit} {
		c, ok := card[t.rate]
		if !ok {
			continue
		}
		out.Rates = append(out.Rates, seoCharge{
			Op:      t.kind,
			Request: c.request.String(),
			Result:  c.result.String(),
		})
	}
	if len(out.Rates) == 0 {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "seo: the upstream price list is not available, so no rate can be quoted")
	}
	return out, nil
}
