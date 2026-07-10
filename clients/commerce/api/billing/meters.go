package billing

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/meter"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createMeterRequest struct {
	Name            string   `json:"name"`
	EventName       string   `json:"eventName"`
	AggregationType string   `json:"aggregationType"`
	Currency        string   `json:"currency"`
	Dimensions      []string `json:"dimensions"`
}

// CreateMeter creates a new usage meter definition.
//
//	POST /v1/billing/meters
func CreateMeter(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createMeterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Name == "" {
		http.Fail(c, 400, "name is required", nil)
		return
	}

	if req.EventName == "" {
		http.Fail(c, 400, "eventName is required", nil)
		return
	}

	aggType := meter.AggregationType(strings.ToLower(req.AggregationType))
	if aggType == "" {
		aggType = meter.AggSum
	}

	cur := currency.Type(strings.ToLower(req.Currency))
	if cur == "" {
		cur = "usd"
	}

	m := meter.New(db)
	m.Name = req.Name
	m.EventName = req.EventName
	m.AggregationType = aggType
	m.Currency = cur
	m.Dimensions = req.Dimensions

	if err := m.Create(); err != nil {
		log.Error("Failed to create meter: %v", err, c)
		http.Fail(c, 500, "failed to create meter", err)
		return
	}

	c.JSON(201, gin.H{
		"id":              m.Id(),
		"name":            m.Name,
		"eventName":       m.EventName,
		"aggregationType": m.AggregationType,
		"currency":        m.Currency,
		"dimensions":      m.Dimensions,
		"createdAt":       m.CreatedAt,
	})
}

// ListMeters returns all meters for the organization.
//
//	GET /v1/billing/meters
func ListMeters(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)

	meters := make([]*meter.Meter, 0)
	q := meter.Query(db).Ancestor(rootKey)

	if _, err := q.GetAll(&meters); err != nil {
		log.Error("Failed to list meters: %v", err, c)
		http.Fail(c, 500, "failed to list meters", err)
		return
	}

	items := make([]gin.H, 0, len(meters))
	for _, m := range meters {
		items = append(items, gin.H{
			"id":              m.Id(),
			"name":            m.Name,
			"eventName":       m.EventName,
			"aggregationType": m.AggregationType,
			"currency":        m.Currency,
			"dimensions":      m.Dimensions,
			"createdAt":       m.CreatedAt,
		})
	}

	c.JSON(200, gin.H{
		"meters": items,
		"count":  len(items),
	})
}

// GetMeter returns a single meter by ID.
//
//	GET /v1/billing/meters/:id
func GetMeter(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	if id == "" {
		http.Fail(c, 400, "meter id is required", nil)
		return
	}

	m := meter.New(db)
	if err := m.GetById(id); err != nil {
		http.Fail(c, 404, "meter not found", err)
		return
	}

	c.JSON(200, gin.H{
		"id":              m.Id(),
		"name":            m.Name,
		"eventName":       m.EventName,
		"aggregationType": m.AggregationType,
		"currency":        m.Currency,
		"dimensions":      m.Dimensions,
		"createdAt":       m.CreatedAt,
	})
}
