package georate

import (
	"strings"
	"unicode"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/types/country"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

type GeoRate struct {
	Country string `json:"country"`
	State   string `json:"state"`

	// Only take a city OR postal code, not both
	City string `json:"city"`
	// Comma separates postal codes
	PostalCodes string         `json:"postalCode"`
	Above       currency.Cents `json:"above"`
	Below       currency.Cents `json:"below"`

	// TODO: Support Product Category Tags
	// ProductCategory string `json:"productCategory"`

	// Support both percent and currency
	// Use store's currency in implementation
	Percent float64        `json:"percent"`
	Cost    currency.Cents `json:"cost"`
}

// Create and validate that a GeoRate's requirements are valid and exist
func New(ctr, st, ct, pcs string, ab, bl currency.Cents, pt float64, cst currency.Cents) GeoRate {
	if c, err := country.FindByISO3166_2(ctr); err != nil {
		ctr = ""
		st = ""
		ct = ""
		pcs = ""
	} else {
		ctr = c.Codes.Alpha2
		if sd, err := c.FindSubDivision(st); err != nil {
			st = ""
			ct = ""
			pcs = ""
		} else {
			st = sd.Code
		}
	}

	if ct != "" {
		pcs = ""
	}

	// Trim leading/trailing commas
	pcs = strings.Trim(pcs, ",")

	return GeoRate{
		clean(ctr),
		clean(st),
		clean(ct),
		clean(pcs),
		ab,
		bl,
		pt,
		cst,
	}
}

// UpperCase and remove all spaces from a string
func clean(str string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, str))
}

// Match GeoRate with country, state, city/postal code.  Report if there's a
// match and the level of match.  If false, also return level of any partial match
func (g GeoRate) Match(ctr, st, ct, pc string, c currency.Cents) (bool, int) {
	ctr = clean(ctr)
	st = clean(st)
	ct = clean(ct)
	pc = clean(pc)

	if c > 0 && g.Above > 0 {
		if c < g.Above {
			log.Debug("amount is lower than Above")
			return false, -1
		}
	}
	if c > 0 && g.Below > 0 {
		if c >= g.Below {
			log.Debug("amount is higher than Below")
			return false, -1
		}
	}

	if ctr == "" && ct == "" && pc == "" {
		log.Debug("Invalid Input")
		return false, 0
	}

	if g.Country == "" {
		log.Debug("Country is Wild Card")
		return true, 0
	}

	if g.Country == ctr {
		if g.State == "" {
			log.Debug("Country Match")
			return true, 1
		}

		if g.State == st {
			if g.City == "" && g.PostalCodes == "" {
				log.Debug("State Match")
				return true, 2
			}

			if g.City != "" && g.City == ct {
				log.Debug("City Match")
				return true, 3
			}

			if g.PostalCodes != "" {
				codes := strings.Split(g.PostalCodes, ",")
				for _, code := range codes {
					if code == pc {
						log.Debug("Postal Code Match")
						return true, 3
					}
				}
			}

			log.Debug("City/Postal Code Mismatch")
			return false, 2
		}
		log.Debug("State Mismatch")
		return false, 1
	}

	log.Debug("No Match")
	return false, 0
}

// Match across an array of georates, return result with highest match level,
// return first result if there is a tie
func Match(grs []GeoRate, ctr, st, ct, pc string, c currency.Cents) (*GeoRate, int, int) {
	var retGr *GeoRate
	currentLevel := -2
	idx := -1

	for i, gr := range grs {
		if isMatch, level := gr.Match(ctr, st, ct, pc, c); isMatch && level > currentLevel {
			if level == 3 {
				return &gr, level, i
			}

			retGr = &grs[i]
			currentLevel = level
			idx = i
		}
	}

	return retGr, currentLevel, idx
}
