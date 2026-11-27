package marketplace

// roster.go — the shop's expert listing when it is asked for an order or a filter
// the store cannot give: what the rewards program says of an expert, a rating, a
// count of jobs or a score in a product.
//
// Those are not columns, so the experts the ordinary filters match are read whole
// (at most maxRoster), dressed, filtered, ordered and paged here. A read that uses
// none of these parameters never comes through this file and costs what it always
// cost.
//
// An order puts the experts it cannot place — a private reward profile for a reward
// order, no rating for the rating order, no hourly rate for the rate order, a score
// of nothing for a product — last, in either direction, and breaks every tie by the
// newer listing and then the id, so no two experts are equal and a page boundary
// never repeats or skips one.

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// Sorts are the orders an expert read takes, newest by default.
var Sorts = []string{"tokens", "level", "points", "rating", "jobs", "rate", "newest"}

// badgeID is what a badge's id looks like.
var badgeID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// roster is an expert read that asked for more than the store gives.
type roster struct {
	sort     string
	desc     bool
	language string
	verified bool
	minLevel int
	badge    string
	tier     int // the lowest tier wanted, counted from 1; 0 for none
	rateMin  *money.Amount
	rateMax  *money.Amount
	product  string
}

// rosterOf reads the parameters of in that only an expert read takes, refusing a
// value that is not one. It answers nil when none was given.
func rosterOf(ctx context.Context, in *shopQuery) (*roster, error) {
	// The binder reads a number or a flag it cannot parse as nothing, and a
	// refusal is better than a filter that silently does not filter.
	if c, ok := cloud.Request(ctx); ok {
		if v := c.Query("minLevel"); v != "" {
			if n, err := strconv.Atoi(v); err != nil || n < 0 {
				return nil, zip.ErrBadRequest("minLevel is a whole number of 0 or more")
			}
		}
		if v := c.Query("verified"); v != "" {
			if _, err := strconv.ParseBool(v); err != nil {
				return nil, zip.ErrBadRequest("verified is true or false")
			}
		}
	}
	asked := in.Sort != "" || in.Dir != "" || in.Language != "" || in.Verified || in.MinLevel != 0 || in.Badge != "" ||
		in.TokenTier != "" || in.RateMin != "" || in.RateMax != "" || in.Product != ""
	if !asked {
		return nil, nil
	}
	if in.Kind != KindExpert {
		return nil, zip.ErrBadRequest("sort, dir, language, verified, minLevel, badge, tokenTier, rateMin, rateMax and product are for kind=expert")
	}
	r := &roster{sort: strings.TrimSpace(in.Sort), desc: true, verified: in.Verified, minLevel: in.MinLevel,
		language: strings.ToLower(strings.TrimSpace(in.Language)), badge: strings.TrimSpace(in.Badge), product: strings.TrimSpace(in.Product)}
	if r.sort == "" {
		r.sort = "newest"
	}
	if !slices.Contains(Sorts, r.sort) {
		return nil, zip.ErrBadRequest("sort is one of " + strings.Join(Sorts, ", "))
	}
	switch strings.TrimSpace(in.Dir) {
	case "", "desc":
	case "asc":
		r.desc = false
	default:
		return nil, zip.ErrBadRequest("dir is asc or desc")
	}
	if r.product != "" {
		if !slices.Contains(Products, r.product) {
			return nil, zip.ErrBadRequest("product is one of " + strings.Join(Products, ", "))
		}
		if strings.TrimSpace(in.Sort) != "" {
			return nil, zip.ErrBadRequest("product orders by its own score: leave sort out")
		}
	}
	if r.language != "" && !language.MatchString(r.language) {
		return nil, zip.ErrBadRequest("language is a BCP 47 tag such as en, es or zh-cn")
	}
	if r.minLevel < 0 {
		return nil, zip.ErrBadRequest("minLevel is a whole number of 0 or more")
	}
	if r.badge != "" && !badgeID.MatchString(r.badge) {
		return nil, zip.ErrBadRequest("badge is a badge id such as first-merge")
	}
	if t := strings.ToLower(strings.TrimSpace(in.TokenTier)); t != "" {
		if r.tier = 1 + slices.IndexFunc(tiers, func(x Tier) bool { return x.Param == t }); r.tier == 0 {
			return nil, zip.ErrBadRequest("tokenTier is one of " + strings.Join(tierParams(), ", "))
		}
	}
	for name, f := range map[string]struct {
		in string
		to **money.Amount
	}{"rateMin": {in.RateMin, &r.rateMin}, "rateMax": {in.RateMax, &r.rateMax}} {
		if s := strings.TrimSpace(f.in); s != "" {
			a, err := money.ParseUSD(s)
			if err != nil || a.IsNeg() {
				return nil, zip.ErrBadRequest(name + " is a decimal USD amount of 0 or more")
			}
			*f.to = &a
		}
	}
	if r.rateMin != nil && r.rateMax != nil && r.rateMin.Cmp(*r.rateMax) > 0 {
		return nil, zip.ErrBadRequest("rateMin is more than rateMax")
	}
	return r, nil
}

// tierParams are the names the tokenTier filter takes.
func tierParams() []string {
	out := make([]string, len(tiers))
	for i, t := range tiers {
		out[i] = t.Param
	}
	return out
}

// row is an expert in the roster: the listing, what buyers said of it and what the
// shop adds.
type row struct {
	l Listing
	t tally
	d dressing
}

// keeps reports whether an expert meets every filter.
func (r *roster) keeps(x row) bool {
	e := x.l.Expert
	if r.verified && !e.Verified {
		return false
	}
	if r.language != "" && !slices.ContainsFunc(e.Languages, func(l string) bool { return l == r.language || strings.HasPrefix(l, r.language+"-") }) {
		return false
	}
	if r.rateMin != nil || r.rateMax != nil {
		if e.Rate.Sign() <= 0 || (r.rateMin != nil && e.Rate.Cmp(*r.rateMin) < 0) || (r.rateMax != nil && e.Rate.Cmp(*r.rateMax) > 0) {
			return false
		}
	}
	if r.minLevel > 0 && (x.d.person == nil || x.d.person.Level < r.minLevel) {
		return false
	}
	if r.badge != "" && (x.d.person == nil || !slices.Contains(x.d.person.Held, r.badge)) {
		return false
	}
	if r.tier > 0 && (x.d.person == nil || rank(x.d.person.Held) < r.tier) {
		return false
	}
	return true
}

// by is the order asked for: whether it can place an expert at all, and how it
// compares two it can, the lower first.
func (r *roster) by() (can func(row) bool, asc func(a, b row) int) {
	yes := func(row) bool { return true }
	public := func(x row) bool { return x.d.person != nil }
	switch {
	case r.product != "":
		return func(x row) bool { return x.d.score(r.product) > 0 },
			func(a, b row) int { return cmp.Compare(a.d.score(r.product), b.d.score(r.product)) }
	case r.sort == "tokens":
		return public, func(a, b row) int { return cmp.Compare(rank(a.d.person.Held), rank(b.d.person.Held)) }
	case r.sort == "level":
		return public, func(a, b row) int { return cmp.Compare(a.d.person.Level, b.d.person.Level) }
	case r.sort == "points":
		return public, func(a, b row) int { return cmp.Compare(a.d.person.Points, b.d.person.Points) }
	case r.sort == "rating":
		// The rating as the shop shows it, to a decimal, so two that read alike tie.
		return func(x row) bool { return x.t.ratings > 0 },
			func(a, b row) int { return cmp.Compare(*reputation(a.t).Rating, *reputation(b.t).Rating) }
	case r.sort == "jobs":
		return yes, func(a, b row) int { return cmp.Compare(a.t.completed, b.t.completed) }
	case r.sort == "rate":
		// A rate is an exact amount, past what a float holds.
		return func(x row) bool { return x.l.Expert.Rate.Sign() > 0 },
			func(a, b row) int { return a.l.Expert.Rate.Cmp(b.l.Expert.Rate) }
	}
	return yes, func(a, b row) int { return cmp.Compare(a.l.CreatedAt, b.l.CreatedAt) }
}

// order sorts rows in the order asked for.
func (r *roster) order(rows []row) {
	can, asc := r.by()
	slices.SortFunc(rows, func(a, b row) int {
		switch ca, cb := can(a), can(b); {
		case ca != cb:
			return cmp.Compare(boolInt(cb), boolInt(ca))
		case ca:
			c := asc(a, b)
			if r.desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		return cmp.Or(cmp.Compare(b.l.CreatedAt, a.l.CreatedAt), strings.Compare(a.l.ID, b.l.ID))
	})
}

// roster answers an expert read that asked for more than the store gives: the
// experts the ordinary filters match, kept by the new ones, put in the order asked
// for and paged. Total is how many were kept; Facets are the ordinary ones.
func (o marketOps) roster(ctx context.Context, q search, r *roster, limit, offset int) (*Shop, error) {
	st := o.s.State.store
	all, _, facets, err := st.catalog(ctx, q, maxRoster+1, 0)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "shop: %v", err)
	}
	if len(all) > maxRoster {
		return nil, zip.ErrBadRequest(fmt.Sprintf("more than %d experts match: narrow them with q or area first", maxRoster))
	}
	reps, err := st.reputations(ctx, all)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "shop: %v", err)
	}
	dressed := o.s.State.expertise.dress(ctx, all)
	rows := make([]row, 0, len(all))
	for _, l := range all {
		if x := (row{l: l, t: *reps[l.ID], d: dressed[l.ID]}); r.keeps(x) {
			rows = append(rows, x)
		}
	}
	r.order(rows)
	out := &Shop{Total: len(rows), Facets: facets}
	rows = rows[min(offset, len(rows)):]
	rows = rows[:min(limit, len(rows))]
	page := make([]Listing, len(rows))
	for i, x := range rows {
		page[i] = x.l
	}
	if out.Listings, err = o.window(ctx, page, reps); err != nil {
		return nil, err
	}
	return out, nil
}
