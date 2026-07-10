package region

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	regionModel "github.com/hanzoai/cloud/clients/commerce/models/region"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	api := rest.New(regionModel.Region{})

	api.GET("/:regionid/countries", namespaced, ListCountries)
	api.POST("/:regionid/countries", namespaced, AddCountry)
	api.DELETE("/:regionid/countries/:countryCode", namespaced, RemoveCountry)

	api.Route(router, args...)
}

// ListCountries returns all countries for a region.
func ListCountries(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Params.ByName("regionid")

	r := regionModel.New(db)
	if err := r.GetById(id); err != nil {
		http.Fail(c, 404, "No region found with id: "+id, err)
		return
	}

	http.Render(c, 200, r.Countries)
}

// AddCountry adds a country to a region's country list.
func AddCountry(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Params.ByName("regionid")

	r := regionModel.New(db)
	if err := r.GetById(id); err != nil {
		http.Fail(c, 404, "No region found with id: "+id, err)
		return
	}

	country := regionModel.Country{}
	if err := json.Decode(c.Request.Body, &country); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	if country.ISO2 == "" {
		http.Fail(c, 400, "Country iso2 code is required", errors.New("missing iso2"))
		return
	}

	// Check for duplicate
	for _, existing := range r.Countries {
		if existing.ISO2 == country.ISO2 {
			http.Fail(c, 409, "Country already exists in region: "+country.ISO2, errors.New("duplicate country"))
			return
		}
	}

	country.RegionId = r.Id()
	r.Countries = append(r.Countries, country)

	if err := r.Update(); err != nil {
		http.Fail(c, 500, "Failed to update region", err)
		return
	}

	http.Render(c, 200, r)
}

// RemoveCountry removes a country from a region by ISO2 code.
func RemoveCountry(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Params.ByName("regionid")
	countryCode := c.Params.ByName("countryCode")

	r := regionModel.New(db)
	if err := r.GetById(id); err != nil {
		http.Fail(c, 404, "No region found with id: "+id, err)
		return
	}

	found := false
	countries := make([]regionModel.Country, 0, len(r.Countries))
	for _, country := range r.Countries {
		if country.ISO2 == countryCode {
			found = true
			continue
		}
		countries = append(countries, country)
	}

	if !found {
		http.Fail(c, 404, "Country not found in region: "+countryCode, errors.New("country not found"))
		return
	}

	r.Countries = countries

	if err := r.Update(); err != nil {
		http.Fail(c, 500, "Failed to update region", err)
		return
	}

	http.Render(c, 200, r)
}
