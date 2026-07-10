package counter

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type searchReq struct {
	Tag     string    `json:"tag"`
	StoreId string    `json:"storeId"`
	Geo     string    `json:"geo"`
	Period  string    `json:"period"`
	After   time.Time `json:"after"`
	Before  time.Time `json:"before"`
}

type searchRes struct {
	Count int `json:"count"`
}

type productRes struct {
	Count  int `json:"count"`
	Amount int `json:"amount"`
}

func search(c *gin.Context) {
	req := searchReq{}
	if err := json.Decode(c.Request.Body, &req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	ctx := middleware.GetContext(c)
	db := datastore.New(ctx)
	q := db.Query(counter.ShardKind)

	if req.Before.IsZero() {
		req.Before = time.Now()
	}

	if req.Period == "" {
		req.Period = string(counter.None)
	}

	// Index Order Is Tag, StoreId, Period, Time, always query in this order
	q = q.Filter("Tag=", req.Tag).Filter("StoreId=", req.StoreId).Filter("Geo=", req.Geo)

	if req.Period == string(counter.Total) {
		q = q.Filter("Period=", req.Period)
	} else {
		q = q.Filter("Period=", req.Period).Filter("Time>", req.After).Filter("Time<=", req.Before)
	}

	shards := []counter.Shard{}

	res := searchRes{
		Count: 0,
	}

	log.Warn("Searching for %v", req, c)
	if _, err := q.GetAll(&shards); err != nil {
		log.Error("Counter Search Error %v", err, c)
	} else {
		log.Warn("Result Count %v", len(shards), c)
		for _, shard := range shards {
			res.Count += shard.Count
		}
	}

	http.Render(c, 200, res)
}

func searchProduct(c *gin.Context) {
	productId := c.Params.ByName("productid")

	ctx := middleware.GetContext(c)
	db := datastore.New(ctx)
	prod := product.New(db)

	if err := prod.GetById(productId); err != nil {
		http.Fail(c, 404, "No product found with id: "+productId, err)
		return
	}

	tag1 := "product." + prod.Id() + ".revenue"
	tag2 := "product." + prod.Id() + ".sold"

	q1 := db.Query(counter.ShardKind)
	q2 := db.Query(counter.ShardKind)

	// Index Order Is Tag, StoreId, Period, Time, always query in this order
	q1 = q1.Filter("Tag=", tag1).Filter("Geo=", "").Filter("Period=", counter.Total)
	q2 = q2.Filter("Tag=", tag2).Filter("Geo=", "").Filter("Period=", counter.Total)

	shards1 := []counter.Shard{}

	res := productRes{
		Count:  0,
		Amount: 0,
	}

	log.Warn("Searching for %v", productId, c)
	if _, err := q1.GetAll(&shards1); err != nil {
		log.Error("Counter Search Error %v", err, c)
	} else {
		log.Warn("Result Count %v", len(shards1), c)
		for _, shard := range shards1 {
			res.Amount += shard.Count
		}
	}

	shards2 := []counter.Shard{}

	if _, err := q2.GetAll(&shards2); err != nil {
		log.Error("Counter Search Error %v", err, c)
	} else {
		log.Warn("Result Count %v", len(shards2), c)
		for _, shard := range shards2 {
			res.Count += shard.Count
		}
	}

	http.Render(c, 200, res)
}

func topLine(c *gin.Context) {
	ctx := middleware.GetContext(c)
	db := datastore.New(ctx)

	tag1 := "order.revenue"
	tag2 := "order.count"

	q1 := db.Query(counter.ShardKind)
	q2 := db.Query(counter.ShardKind)

	// Index Order Is Tag, StoreId, Period, Time, always query in this order
	q1 = q1.Filter("Tag=", tag1).Filter("Geo=", "").Filter("Period=", counter.Total)
	q2 = q2.Filter("Tag=", tag2).Filter("Geo=", "").Filter("Period=", counter.Total)

	shards1 := []counter.Shard{}

	res := productRes{
		Count:  0,
		Amount: 0,
	}

	if _, err := q1.GetAll(&shards1); err != nil {
		log.Error("Counter Search Error %v", err, c)
	} else {
		log.Warn("Result Count %v", len(shards1), c)
		for _, shard := range shards1 {
			res.Amount += shard.Count
		}
	}

	shards2 := []counter.Shard{}

	if _, err := q2.GetAll(&shards2); err != nil {
		log.Error("Counter Search Error %v", err, c)
	} else {
		log.Warn("Result Count %v", len(shards2), c)
		for _, shard := range shards2 {
			res.Count += shard.Count
		}
	}

	http.Render(c, 200, res)
}
