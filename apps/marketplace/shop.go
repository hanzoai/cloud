package marketplace

// shop.go — the one catalog: every public listing of every kind, read by anyone.
//
// A shop window names what is sold, who sells it and what buyers made of it, and
// nothing else. The seller is its org and one public fact — whether its tax form
// is certified and valid — and never its payout wallet, its screening or its
// forms. Reputation is only what the parties of settled jobs said and did.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	principalpeer "github.com/hanzoai/cloud/client/principal"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

const (
	// maxShop is the most listings one org publishes: what one seller can put in
	// front of the shop is bounded, and every other seller's listings stay in it.
	maxShop = 1000
	// shopPage and maxPage are the default and largest page.
	shopPage = 48
	maxPage  = 200
	// askPublic bounds the one crossing a shop read makes.
	askPublic = 5 * time.Second
)

// The operations a buyer takes on a listing, as the command line and the MCP
// server name them: a tool is installed, everything else is hired. The hanzo
// command line names an operation by its path (hanzoai/cli genproduct.rs fold):
// a leaf that answers one method is its own verb, and a collection that also
// lists takes "create" for its POST.
const (
	opInstall = "post_marketplace_install"
	opHire    = "post_marketplace_jobs"
	cliTool   = "hanzo marketplace install --tool %s"
	cliHire   = "hanzo marketplace jobs create --listing %s --brief <brief> --amount <usd>"
)

// shopQuery searches the shop.
type shopQuery struct {
	// Q keeps listings whose title, description, category, thing or seller
	// contains every word of it, case-insensitively.
	Q string `json:"q"`
	// Kind keeps one kind: agent, persona, app, skill, mcp, tool or expert.
	Kind string `json:"kind"`
	// Category keeps one category, exactly.
	Category string `json:"category"`
	// Price keeps free listings or priced ones.
	Price string `json:"price"`
	// Rating keeps listings rated at least this many stars, 1 to 5.
	Rating int `json:"rating"`
	// Seller keeps one seller org's listings.
	Seller string `json:"seller"`
	// Area keeps the experts who work on one area: configure, base, commerce,
	// bot, agents, cloud, models, creative or freelance.
	Area string `json:"area"`
	// Limit is the page size: 48 by default, 200 at most.
	Limit int `json:"limit"`
	// Offset is where the page starts.
	Offset int `json:"offset"`
	// Sort orders experts, with kind=expert: tokens (the token tier), level, points
	// (the rewards program's, put last for an expert whose reward profile is not
	// public), rating (unrated last), jobs (jobs completed), rate (the hourly rate,
	// none last) or newest. It is newest by default.
	Sort string `json:"sort"`
	// Dir is the direction of Sort: desc by default, or asc. An expert the order
	// cannot place stays last either way.
	Dir string `json:"dir"`
	// Language keeps the experts who work in a language, a BCP 47 tag such as en,
	// es or zh-cn; zh keeps zh-cn and zh-tw as well. With kind=expert.
	Language string `json:"language"`
	// Verified, true, keeps the experts staff verified; false keeps everyone. With
	// kind=expert.
	Verified bool `json:"verified"`
	// MinLevel keeps the experts whose public reward profile shows this level or
	// higher; 0 keeps everyone. With kind=expert.
	MinLevel int `json:"minLevel"`
	// Badge keeps the experts whose public reward profile holds a badge, by id such
	// as first-merge. With kind=expert.
	Badge string `json:"badge"`
	// TokenTier keeps the experts whose token tier is at least this: 100k, 1m, 5m,
	// 10m, 100m, 1b, 10b, 100b or 1t. With kind=expert.
	TokenTier string `json:"tokenTier"`
	// RateMin keeps the experts whose hourly rate is at least this decimal USD amount;
	// an expert with no hourly rate is kept by neither RateMin nor RateMax. With
	// kind=expert.
	RateMin string `json:"rateMin"`
	// RateMax keeps the experts whose hourly rate is at most this decimal USD amount.
	// With kind=expert.
	RateMax string `json:"rateMax"`
	// Product orders the experts by their score in a product, best first, those
	// with none last: dev, bot, team, base, commerce, cloud, agents, kai, enso, zen
	// or creative. It is the order, so it is not given with Sort. With kind=expert.
	Product string `json:"product"`
}

// Shop is one page of the catalog.
type Shop struct {
	// Listings is this page, newest first.
	Listings []ShopListing `json:"listings"`
	// Total is how many listings the search matched, past this page.
	Total int `json:"total"`
	// Facets counts the matches by kind, category, price and rating, each
	// counted as if its own filter were not applied.
	Facets Facets `json:"facets"`
}

// Facets are the shop's counts.
type Facets struct {
	// Kind counts listings of each kind.
	Kind map[string]int `json:"kind"`
	// Category counts listings in each category; an uncategorized one is not
	// counted.
	Category map[string]int `json:"category"`
	// Price counts free and priced listings.
	Price map[string]int `json:"price"`
	// Rating counts listings by their rounded rating, "1" to "5", and those with
	// no rating yet as "unrated".
	Rating map[string]int `json:"rating"`
}

// ShopListing is one public listing as anyone may read it.
type ShopListing struct {
	// ID is the listing.
	ID string `json:"id"`
	// PublisherOrg is the org that sells it.
	PublisherOrg string `json:"publisherOrg"`
	// Kind is what it sells: agent, persona, app, skill, mcp, tool or expert.
	Kind string `json:"kind"`
	// Tool names the thing sold, as the seller named it; empty for an expert, who
	// is a person and not a thing.
	Tool string `json:"tool"`
	// Title is the shop-window name.
	Title string `json:"title"`
	// Description is the long copy.
	Description string `json:"description"`
	// Category groups it; empty is ungrouped.
	Category string `json:"category"`
	// Price is the exact USD price — per call for a tool, per job otherwise, the
	// fixed engagement for an expert; "0" is free.
	Price money.Amount `json:"price"`
	// Currency labels Price.
	Currency string `json:"currency"`
	// Public is always true here.
	Public bool `json:"public"`
	// Docs is where its documentation lives, when the seller said.
	Docs string `json:"docs,omitempty"`
	// Expert is the person behind an expert listing — rate, areas, languages, skills,
	// links, time zone, hours and whether staff verified them, and, where they can be
	// told, their rewards standing and scores in products; absent on any other kind.
	Expert *Expert `json:"expert,omitempty"`
	// Capability is the Kai capability metadata behind a capability listing —
	// shared representation, route, evaluation metrics and license; absent on any
	// other kind.
	Capability *CapabilitySpec `json:"capability,omitempty"`
	// CreatedAt is when it was published, unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when it was last edited, unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
	// Seller is what anyone may know about the seller.
	Seller Seller `json:"seller"`
	// Reputation is what buyers made of this listing.
	Reputation Reputation `json:"reputation"`
	// Links are the ways to act on it.
	Links Links `json:"links"`
}

// Seller is the public face of an org that sells.
type Seller struct {
	// Org is the seller org.
	Org string `json:"org"`
	// Documented is true while the seller's tax form is certified and valid —
	// what its agents' TaxPrincipalCredentials rest on. False as well when that
	// could not be asked just now.
	Documented bool `json:"documented"`
	// Reputation is what buyers made of the seller, across its listings.
	Reputation Reputation `json:"reputation"`
}

// Reputation is what the parties of settled jobs said and did.
type Reputation struct {
	// Rating is the mean stars buyers gave, 1 to 5 to one decimal; null until the
	// first review.
	Rating *float64 `json:"rating"`
	// Reviews is how many buyers rated.
	Reviews int64 `json:"reviews"`
	// Installs is how many orgs installed a tool listing's tool.
	Installs int64 `json:"installs"`
	// Jobs counts the jobs released, and those that were ever disputed.
	Jobs JobCounts `json:"jobs"`
}

// JobCounts counts jobs.
type JobCounts struct {
	// Completed is released jobs.
	Completed int64 `json:"completed"`
	// Disputed is jobs ever disputed.
	Disputed int64 `json:"disputed"`
}

// Links are the ways to act on a listing.
type Links struct {
	// Docs is the listing's documentation, when the seller named one.
	Docs string `json:"docs,omitempty"`
	// CLI is the hanzo command that buys it.
	CLI string `json:"cli"`
	// MCP is the MCP tool and operation that buys it.
	MCP MCPRef `json:"mcp"`
}

// MCPRef names an MCP tool and one of its operations.
type MCPRef struct {
	// Tool is the MCP tool: marketplace.
	Tool string `json:"tool"`
	// Op is the operation within it.
	Op string `json:"op"`
}

// shopRef addresses one public listing.
type shopRef struct {
	// ID is the listing, from the path.
	ID string `json:"id"`
}

// Searches every public listing of every kind — agents and personas to hire,
// apps, skills, Kai capabilities, MCP servers and tools — newest first, with
// facets by kind, category, price and rating, paged. Each listing carries its
// seller's public face, its reputation from settled jobs and installs, and the
// command and MCP operation that buy it. It needs no credential and answers the
// same to everyone.
//
// With kind=expert it also takes sort and dir, language, verified, minLevel,
// badge, tokenTier, rateMin and rateMax, and product: the experts the ordinary
// filters match are kept by these, ordered and paged, and total is how many were
// kept (facets stay the ordinary filters'). An expert whose reward profile is public
// shows it — level, points, the three rarest badges, the token tier — and each shows
// the products it scores highest in with the parts of the score, and those it ranks
// among the best three of all experts in. A private profile shows nothing of the
// rewards program and is put last by an order that reads it. A value a parameter
// cannot take, and any of these without kind=expert, is a 400.
//
// Example: {"q": "research", "kind": "agent", "limit": 48}
func (o marketOps) shop(ctx context.Context, in *shopQuery) (*Shop, error) {
	if in.Kind != "" && !slices.Contains(Kinds, in.Kind) {
		return nil, zip.ErrBadRequest("kind is one of " + strings.Join(Kinds, ", "))
	}
	if in.Price != "" && in.Price != "free" && in.Price != "priced" {
		return nil, zip.ErrBadRequest("price is free or priced")
	}
	if in.Area != "" && !slices.Contains(Areas, in.Area) {
		return nil, zip.ErrBadRequest("area is one of " + strings.Join(Areas, ", "))
	}
	if in.Rating < 0 || in.Rating > 5 {
		return nil, zip.ErrBadRequest("rating is 1 to 5 stars")
	}
	limit, offset := in.Limit, in.Offset
	if limit <= 0 {
		limit = shopPage
	}
	if limit > maxPage || offset < 0 {
		return nil, zip.ErrBadRequest(fmt.Sprintf("limit is at most %d and offset is not negative", maxPage))
	}
	r, err := rosterOf(ctx, in)
	if err != nil {
		return nil, err
	}
	st := o.s.State.store
	q := search{words: strings.Fields(strings.ToLower(in.Q)), seller: in.Seller,
		area: in.Area, kind: in.Kind, category: in.Category, price: in.Price, rating: in.Rating}
	if r != nil {
		return o.roster(ctx, q, r, limit, offset)
	}
	page, total, facets, err := st.catalog(ctx, q, limit, offset)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "shop: %v", err)
	}
	reps, err := st.reputations(ctx, page)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "shop: %v", err)
	}
	out := &Shop{Total: total, Facets: facets}
	if out.Listings, err = o.window(ctx, page, reps); err != nil {
		return nil, err
	}
	return out, nil
}

// Reads one public listing as the shop shows it — its seller's public face, its
// reputation and the ways to buy it. A private listing and one that does not
// exist are the same 404. It needs no credential.
//
// Example: {"id": "lst_4f1c"}
func (o marketOps) shopItem(ctx context.Context, in *shopRef) (*ShopListing, error) {
	st := o.s.State.store
	l, found, err := st.Public(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "shop: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("listing not found")
	}
	reps, err := st.reputations(ctx, []Listing{l})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "shop: %v", err)
	}
	out, err := o.window(ctx, []Listing{l}, reps)
	if err != nil {
		return nil, err
	}
	return &out[0], nil
}

// window renders listings as the shop shows them: each seller asked about once,
// principal asked once for the page.
func (o marketOps) window(ctx context.Context, ls []Listing, reps map[string]*tally) ([]ShopListing, error) {
	out := make([]ShopListing, 0, len(ls))
	if len(ls) == 0 {
		return out, nil
	}
	sellers := map[string]*Seller{}
	var orgs []string
	for _, l := range ls {
		if sellers[l.PublisherOrg] != nil {
			continue
		}
		t, err := o.s.State.store.sellerTally(ctx, l.PublisherOrg)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "shop: %v", err)
		}
		sellers[l.PublisherOrg] = &Seller{Org: l.PublisherOrg, Reputation: reputation(t)}
		orgs = append(orgs, l.PublisherOrg)
	}
	ask, cancel := context.WithTimeout(cloud.For(context.Background(), ""), askPublic)
	defer cancel()
	if pub, err := principalpeer.PrincipalPublic(ask, &client.PublicIn{Orgs: orgs}); err == nil {
		for _, p := range pub.Principals {
			if s := sellers[p.Org]; s != nil {
				s.Documented = p.Documented
			}
		}
	} else {
		o.s.Log.Warn("marketplace: sellers' public facts", "err", err)
	}
	dressed := o.s.State.expertise.dress(ctx, ls)
	for _, l := range ls {
		links := Links{Docs: l.Docs, CLI: fmt.Sprintf(cliHire, l.ID), MCP: MCPRef{Tool: "marketplace", Op: opHire}}
		if l.Kind == KindTool || l.Kind == KindCapability {
			links.CLI, links.MCP.Op = fmt.Sprintf(cliTool, l.Tool), opInstall
		}
		tool := l.Tool
		if l.Kind == KindExpert {
			tool = ""
		}
		expert := l.Expert.window()
		if d, ok := dressed[l.ID]; ok {
			d.into(expert)
		}
		out = append(out, ShopListing{ID: l.ID, PublisherOrg: l.PublisherOrg, Kind: l.Kind, Tool: tool, Title: l.Title,
			Description: l.Description, Category: l.Category, Price: l.Price, Currency: l.Currency, Public: true,
			Docs: l.Docs, Expert: expert, Capability: l.Capability, CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
			Seller: *sellers[l.PublisherOrg], Reputation: reputation(*reps[l.ID]), Links: links})
	}
	return out, nil
}

// reputation renders a tally.
func reputation(t tally) Reputation {
	r := Reputation{Reviews: t.ratings, Installs: t.installs, Jobs: JobCounts{Completed: t.completed, Disputed: t.disputed}}
	if t.ratings > 0 {
		mean := math.Round(float64(t.stars)/float64(t.ratings)*10) / 10
		r.Rating = &mean
	}
	return r
}
