package event

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
)

// consent.go is the ONE consent policy. It governs the THIRD PARTIES — Google Analytics
// and the Meta pixel in the browser, and what cloud forwards to Google and Meta from the
// server — and nothing else.
//
// THE FIRST-PARTY STREAM IS ESSENTIAL. Hanzo's own analytics — every page view, event,
// error, identify and the anonymous id on /v1/event — is the org's own data, never sold
// or shared, and is kept for every visitor whatever they chose, wherever they are, under
// Global Privacy Control or Do Not Track alike. So are sign-in, security, abuse and rate
// limits, reliability and transaction state.
//
// The browser's cookie spells a choice in three categories: analytics (Google
// Analytics' cookies), marketing (the Meta pixel and the other ad platforms) and ads
// (Google and Meta using the visit for ad personalization; off is the sale-or-sharing
// opt-out). The policy's purposes map onto them: analytics is analytics, advertising is
// marketing, personalization and the sale or sharing of personal information are ads.
//
// THE RULE IS THE VISITOR'S REGION, decided here per request from the edge's country
// and subdivision (geo.Country, geo.Region) and served with the tag
// config. The lists are data (consent_policy.json):
//
//	eu          a country in `eu` (EU27, the EEA, the UK, Switzerland) — and any visitor
//	            no edge placed, the strict answer to silence. Before a choice Google
//	            Analytics runs under Consent Mode with every flag denied (cookieless
//	            pings, no cookie), Meta's pixels are init'ed held revoked and send
//	            nothing, no other platform loads, and nothing is forwarded.
//	us_privacy  a state in `us_privacy` (a US state privacy law), or a US visitor with no
//	            attested state: everything runs by default; ads off is the opt-out.
//	global      everywhere else: everything runs by default, with a notice.
//
// Global Privacy Control or Do Not Track is `refused`, in every region: no third party
// loads in the browser and nothing is forwarded. Each event states the grants it was
// sent under (`consent`), and that is what forwarding reads.

//go:embed consent_policy.json
var policyJSON []byte

// ConsentPolicy is the policy table.
type ConsentPolicy struct {
	// Version is the policy's version; Floor the oldest version whose stored choices
	// still count. A choice stored under a version outside them is asked again.
	Version  int      `json:"version"`
	Floor    int      `json:"floor"`
	Purposes []string `json:"purposes"`
	// EU are the countries under ModeEU; USPrivacy the subdivisions, by country, under
	// ModeUSPrivacy. A country USPrivacy names with no attested subdivision is under it.
	EU        []string            `json:"eu"`
	USPrivacy map[string][]string `json:"us_privacy"`
}

// Policy is the table every reader shares.
var Policy = func() ConsentPolicy {
	var p ConsentPolicy
	if err := json.Unmarshal(policyJSON, &p); err != nil {
		panic("consent_policy.json: " + err.Error())
	}
	return p
}()

// The rules a visitor can be under.
const (
	ModeEU        = "eu"         // nothing optional before a choice
	ModeUSPrivacy = "us_privacy" // everything on; the visitor opts out of sale or sharing
	ModeGlobal    = "global"     // everything on, with a notice
)

// Grants are the cookie categories a visitor has allowed.
type Grants struct {
	Analytics bool `json:"analytics"`
	Marketing bool `json:"marketing"`
	Ads       bool `json:"ads"`
}

// Decision is what the policy says of one visitor: the rule they are under and what is
// on before they choose.
type Decision struct {
	Region   string `json:"region"`   // where the edge placed them: "DE", "US-CA", "US"; empty when nowhere
	Mode     string `json:"mode"`     // eu, us_privacy or global
	Defaults Grants `json:"defaults"` // the categories granted before a choice: none in eu, all elsewhere
	Refused  bool   `json:"refused"`  // the browser sent Global Privacy Control or Do Not Track
	Version  int    `json:"version"`  // the policy version a choice is stored under
	Floor    int    `json:"floor"`    // the oldest version whose stored choices still count
}

// Decide resolves a visitor from the country and subdivision the edge attested (either
// may be empty) and the browser's Global Privacy Control or Do Not Track signal.
func (p ConsentPolicy) Decide(country, region string, refused bool) Decision {
	country = strings.ToUpper(strings.TrimSpace(country))
	region = strings.ToUpper(strings.TrimSpace(region))
	if len(country) != 2 || country == "XX" || country == "T1" {
		country, region = "", ""
	}
	d := Decision{Region: country, Refused: refused, Version: p.Version, Floor: p.Floor}
	if country != "" && region != "" {
		d.Region = country + "-" + region
	}
	states, listed := p.USPrivacy[country]
	switch {
	case country == "" || slices.Contains(p.EU, country):
		d.Mode = ModeEU
	case listed && (region == "" || slices.Contains(states, region)):
		d.Mode, d.Defaults = ModeUSPrivacy, Grants{true, true, true}
	default:
		d.Mode, d.Defaults = ModeGlobal, Grants{true, true, true}
	}
	return d
}

// ParseGrants reads the `consent` property, a comma list of categories. ok is false
// when the event stated none.
func ParseGrants(v any) (g Grants, ok bool) {
	s, ok := v.(string)
	if !ok {
		return Grants{}, false
	}
	for _, part := range strings.Split(s, ",") {
		switch strings.TrimSpace(part) {
		case "analytics":
			g.Analytics = true
		case "marketing":
			g.Marketing = true
		case "ads":
			g.Ads = true
		}
	}
	return g, true
}

// String spells the grants the way the cookie and the property do.
func (g Grants) String() string {
	var out []string
	if g.Analytics {
		out = append(out, "analytics")
	}
	if g.Marketing {
		out = append(out, "marketing")
	}
	if g.Ads {
		out = append(out, "ads")
	}
	return strings.Join(out, ",")
}

// Effective is what an event may be forwarded under: the grants it stated, else
// nothing; nothing at all when the browser refused (GPC or DNT), in every region.
func (d Decision) Effective(stated any) Grants {
	if d.Refused {
		return Grants{}
	}
	g, _ := ParseGrants(stated)
	return g
}
