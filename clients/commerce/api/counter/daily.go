package counter

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type dailyReq struct {
	StoreId string `json:"storeId"`
	Geo     string `json:"geo"`

	After  time.Time `json:"after"`
	Before time.Time `json:"before"`
}

type dailyCount struct {
	Date                           time.Time `json:"date"`
	ProjectedRevenueAmount         int       `json:"projectedRevenueAmount"`
	ProjectedRevenueRefundedAmount int       `json:"projectedRevenueRefundedAmount"`

	OrderAmount int `json:"orderAmount"`
	OrderCount  int `json:"orderCount"`

	OrderRefundedAmount int `json:"orderRefundedAmount"`
	OrderRefundedCount  int `json:"orderRefundedCount"`
}

type dailyRes struct {
	After  time.Time    `json:"after"`
	Before time.Time    `json:"before"`
	Counts []dailyCount `json:"counts"`
}

func daily(c *gin.Context) {
	org := middleware.GetOrganization(c)

	req := dailyReq{}
	if err := json.Decode(c.Request.Body, &req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	if req.Before.IsZero() {
		req.Before = time.Now()
	}

	if req.After.IsZero() {
		req.After = org.CreatedAt
	}

	// Get Dailies
	ctx := middleware.GetContext(c)
	db := datastore.New(ctx)

	res := dailyRes{
		Before: req.Before,
		After:  req.After,
		Counts: []dailyCount{},
	}

	start := req.After

	getByTag := func(tag string, start time.Time, end time.Time) int {
		count := 0

		q := db.Query(counter.ShardKind)
		// Index Order Is Tag, StoreId, Period, Time, always query in this order
		q = q.Filter("Tag=", tag).Filter("StoreId=", req.StoreId).Filter("Geo=", req.Geo)
		q = q.Filter("Period=", counter.Hourly).Filter("Time>", start).Filter("Time<=", end)

		shards := []counter.Shard{}

		log.Warn("Searching for %v", req, c)
		if _, err := q.GetAll(&shards); err != nil {
			log.Error("Counter Search Error %v", err, c)
		} else {
			log.Warn("Result Count %v", len(shards), c)
			for _, shard := range shards {
				count += shard.Count
			}
		}

		return count
	}

	for start.Before(req.Before) {
		end := start.Add(time.Hour * 24)

		res.Counts = append(res.Counts, dailyCount{
			Date: start,

			ProjectedRevenueAmount:         getByTag("order.projected.revenue", start, end),
			ProjectedRevenueRefundedAmount: getByTag("order.projected.refunded.amount", start, end),

			OrderAmount: getByTag("order.revenue", start, end),
			OrderCount:  getByTag("order.count", start, end),

			OrderRefundedAmount: getByTag("order.refunded.amount", start, end),
			OrderRefundedCount:  getByTag("order.refunded.count", start, end),
		})

		start = end
		startYear, startMonth, startDay := start.Date()
		start = time.Date(startYear, startMonth, startDay, 0, 0, 0, 0, req.After.Location())
	}

	// Get Top Lines

	http.Render(c, 200, res)
}
