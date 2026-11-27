# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package seo

struct seoAuditIn {
    URL text @0
}

struct seoBacklinkIn {
    Target text @0
}

struct seoBacklinkOut {
    Target    text @0
    Rank      i64  @8
    Backlinks i64  @16
    Domains   i64  @24
    Pages     i64  @32
    Broken    i64  @40
    Spam      i64  @48
    FirstSeen text @56
    Cost      text @64
}

struct seoCompetitorIn {
    Keywords list<text> @0
    Location i64        @8
    Language text       @16
    Limit    i64        @24
}

struct seoCompetitorOut {
    Competitors list<bytes> @0
    Total       i64         @8
    Cost        text        @16
}

struct seoIdeaIn {
    Keywords list<text> @0
    Location i64        @8
    Language text       @16
    Limit    i64        @24
}

struct seoIdeaOut {
    Keywords list<bytes> @0
    Total    i64         @8
    Cost     text        @16
}

struct seoKeywordIn {
    Keywords list<text> @0
    Location i64        @8
    Language text       @16
}

struct seoKeywordOut {
    Keywords list<bytes> @0
    Cost     text        @8
}

struct seoRankIn {
    Domain   text @0
    Location i64  @8
    Language text @16
    Limit    i64  @24
}

struct seoRankOut {
    Rankings list<bytes> @0
    Total    i64         @8
    Cost     text        @16
}

struct seoRateOut {
    Rates list<bytes> @0
}

interface seo {
    # Summarises who links to a target.
    # It returns the authority score, how many links point at it and from how many
    # distinct sites, how many of those are broken, and how much of the profile reads
    # as spam. Distinct sites is the number to read: a thousand links from one domain
    # is one endorsement, and a profile that grew fast in links and not in domains is
    # usually a profile somebody bought.
    # The target can be a whole domain, a subdomain, or one page URL — the summary is
    # scoped to whatever is named. It is priced per request, so a domain with ten
    # million links costs the same as one with ten.
    seoBacklink(req: seoBacklinkIn) returns (rep: seoBacklinkOut)
    # Names the domains that place for the same phrases.
    # Given a set of phrases it returns the sites that appear across them, with each
    # one's average position, how many of the phrases it places for, its share of the
    # available attention and the visits that earns. It answers "who am I actually up
    # against here", which is a different question from "who do I think my competitors
    # are" and frequently a different answer.
    # Pair it with seoRank: this says who is in the race, seoRank says where any one of
    # them finishes. It is priced per row, so Limit decides the cost.
    seoCompetitor(req: seoCompetitorIn) returns (rep: seoCompetitorOut)
    # Grows a seed phrase into the phrases nobody named yet.
    # It takes phrases you have and returns phrases in the same category that you do
    # not — relevant rather than merely containing the seed — each with its search
    # volume, click cost, competition and how hard its first page is to reach. This is
    # where a keyword list comes FROM; seoKeyword is where a list you already have gets
    # measured.
    # It is priced per row, so Limit is the knob that decides what the call costs.
    # Total says how many more there were.
    seoIdea(req: seoIdeaIn) returns (rep: seoIdeaOut)
    # Measures phrases the caller already has.
    # It answers, for each phrase named, how many people search it in a month, what an
    # advertising click on it costs, and how contested that advertising is. This is the
    # ground fact of search: everything else on this surface is a question about
    # phrases, and this is the one that says whether a phrase is worth having.
    # Give it phrases you already suspect. To find phrases you have not thought of,
    # use seoIdea; to find the ones a site already places for, use seoRank.
    # The market defaults to the United States in English. It is priced per request
    # rather than per phrase, so asking about fifty phrases costs what asking about
    # one does.
    seoKeyword(req: seoKeywordIn) returns (rep: seoKeywordOut)
    # Reports every phrase a domain already places for.
    # For each one it gives the phrase, the position on the results page, the page of
    # the site that placed, that result's headline, the phrase's monthly searches and
    # the visits the placement is estimated to earn. It is the single most direct
    # question about a site's search visibility — yours or a competitor's, since it
    # takes any domain.
    # Position is the ABSOLUTE rank, counting every element on the page — the ads, the
    # answer boxes, the map — because that is what a person scrolling actually passes.
    # An organic-only rank flatters a result that sits below half a screen of other
    # things.
    # It is priced per row, so Limit decides what the call costs, and Total says how
    # many more there were.
    seoRank(req: seoRankIn) returns (rep: seoRankOut)
    # Publishes what every call on this surface costs.
    # The numbers are read from the upstream's own published price list, not from a
    # table kept here, so a price change on their side moves this card within the hour
    # and moves what is debited with it. That is the whole of the pricing model: this
    # surface resells at cost, and the cost is theirs to state.
    # A row has two numbers because a call has two costs: a flat charge for asking, and
    # a charge per row returned. An op priced per request reports zero for the second,
    # and for one priced per row the total is `request + result x limit` — which is the
    # amount your balance is authorized against before the call, and roughly what you
    # will be debited after it.
    # It is a read and it is free: asking what something costs must not require the
    # balance that would pay for it. If the upstream cannot be reached the card comes
    # back empty rather than stale — a price nobody can confirm is not a price.
    seoRate() returns (rep: seoRateOut)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   seoAudit  seoAuditOut.Checks  map[string]bool  (map)
#
# opaque (5) — crosses, arrives without its name:
#   seoCompetitorOut.Competitors  seo.seoDomain (list element)
#   seoIdeaOut.Keywords  seo.seoMetric (list element)
#   seoKeywordOut.Keywords  seo.seoMetric (list element)
#   seoRankOut.Rankings  seo.seoRanking (list element)
#   seoRateOut.Rates  seo.seoCharge (list element)
