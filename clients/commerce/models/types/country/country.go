package country

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pariz/gountries"
)

type Query gountries.Query

type Country struct {
	gountries.Country
}

type SubDivision gountries.SubDivision

var Countries []Country
var ByISO3166_2 map[string]Country

// Error returns a formatted error
func makeError(errMsg, errType string) error {
	return fmt.Errorf("gountries error. %s: %s", errMsg, errType)
}

func init() {
	Countries = make([]Country, 0)
	ByISO3166_2 = make(map[string]Country)

	q := gountries.New()

	// Generate the list of countries and the map
	for _, country := range q.Countries {
		Countries = append(Countries, Country{country})
		ByISO3166_2[country.Codes.Alpha2] = Country{country}
	}

	// Sort the list of countries by their common name
	nameToIsoMap := make(map[string]string)
	sortedNames := make([]string, len(Countries))

	i := 0
	for iso, country := range ByISO3166_2 {
		name := country.Name.Common
		sortedNames[i] = name
		nameToIsoMap[name] = iso
		i++
	}

	sort.Strings(sortedNames)

	// Make the country list sorted by common name
	for i, name := range sortedNames {
		Countries[i] = ByISO3166_2[nameToIsoMap[name]]
	}
}

func FindByISO3166_2(code string) (Country, error) {
	codeU := strings.ToUpper(code)
	if c, ok := ByISO3166_2[codeU]; ok {
		return c, nil
	}

	return Country{}, makeError("Could not find country with code %s", code)
}

func (c Country) FindSubDivision(nameOrCode string) (SubDivision, error) {
	nameOrCodeU := strings.ToUpper(nameOrCode)
	sds := c.SubDivisions()
	for _, sd := range sds {
		if sd.Code == nameOrCodeU || strings.ToUpper(sd.Name) == nameOrCodeU {
			return SubDivision(sd), nil
		}
	}

	return SubDivision{}, makeError("Could not find subdivision with name or code %s", nameOrCode)
}
