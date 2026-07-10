package migrations

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore/parallel"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/bigquery"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var UserFields = bigquery.Fields{
	"Id_":       "STRING",
	"FirstName": "STRING",
	"LastName":  "STRING",
	"Email":     "STRING",
	"Metadata_": "STRING",
	"CreatedAt": "TIMESTAMP",
	"UpdatedAt": "TIMESTAMP",
}

var OrderFields = bigquery.Fields{
	"Id_":                 "STRING",
	"UserId":              "STRING",
	"Status":              "STRING",
	"PaymentStatus":       "STRING",
	"FulfillmentStatus":   "STRING",
	"Subtotal":            "INTEGER",
	"Tax":                 "INTEGER",
	"Shipping":            "INTEGER",
	"Discount":            "INTEGER",
	"Total":               "INTEGER",
	"Paid":                "INTEGER",
	"Refunded":            "INTEGER",
	"CouponCodes_0":       "STRING",
	"CouponCodes_1":       "STRING",
	"CouponCodes_3":       "STRING",
	"Items_0_ProductId":   "STRING",
	"Items_0_ProductSlug": "STRING",
	"Items_1_ProductId":   "STRING",
	"Items_1_ProductSlug": "STRING",
	"Items_2_ProductId":   "STRING",
	"Items_2_ProductSlug": "STRING",
	"Items_3_ProductId":   "STRING",
	"Items_3_ProductSlug": "STRING",
	"Items_4_ProductId":   "STRING",
	"Items_4_ProductSlug": "STRING",
	"Metadata_":           "STRING",
	"CreatedAt":           "TIMESTAMP",
	"UpdatedAt":           "TIMESTAMP",
}

var _ = NewBigQuery("bigquery-export-kanoa-user-order",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "kanoa")

		t := time.Now()
		suffix := t.Format("20060102T150405")

		ctx := middleware.GetContext(c)

		client, err := bigquery.NewClient(ctx)
		if err != nil {
			log.Panic("Could not create big query client: %v", err, ctx)
		}

		projectId := config.Env

		client.InsertNewTable(projectId, "datastore", "user"+suffix, UserFields)
		client.InsertNewTable(projectId, "datastore", "order"+suffix, OrderFields)

		return []interface{}{projectId, suffix}
	},
	func(db *ds.Datastore, usr *user.User, rows *[]parallel.BigQueryRow, projectId, tableSuffix string) {
		data := make(bigquery.Row)
		data["Id_"] = usr.Id_
		data["FirstName"] = usr.FirstName
		data["LastName"] = usr.LastName
		data["Email"] = usr.Email
		data["Metadata_"] = usr.Metadata_
		data["CreatedAt"] = usr.CreatedAt
		data["UpdatedAt"] = usr.UpdatedAt

		row := parallel.BigQueryRow{
			ProjectId: projectId,
			DataSetId: "datastore",
			TableId:   "user" + tableSuffix,
			Row:       data,
		}

		*rows = append(*rows, row)
	},
	func(db *ds.Datastore, ord *order.Order, rows *[]parallel.BigQueryRow, projectId, tableSuffix string) {
		data := make(bigquery.Row)
		data["Id_"] = ord.Id_
		data["UserId"] = ord.UserId
		data["Status"] = ord.Status
		data["PaymentStatus"] = ord.PaymentStatus
		data["FulfillmentStatus"] = ord.Fulfillment.Status
		data["Subtotal"] = ord.Subtotal
		data["Tax"] = ord.Tax
		data["Shipping"] = ord.Shipping
		data["Discount"] = ord.Discount
		data["Total"] = ord.Total
		data["Paid"] = ord.Paid
		data["Refunded"] = ord.Refunded
		if len(ord.CouponCodes) > 0 {
			for i, code := range ord.CouponCodes {
				data["CouponCodes_"+strconv.Itoa(i)] = code
			}
		}
		if len(ord.Items) > 0 {
			for i, item := range ord.Items {
				data["Items_"+strconv.Itoa(i)+"_ProductId"] = item.ProductId
				data["Items_"+strconv.Itoa(i)+"_ProductSlug"] = item.ProductSlug
			}
		}
		data["Metadata_"] = ord.Metadata_
		data["CreatedAt"] = ord.CreatedAt
		data["UpdatedAt"] = ord.UpdatedAt

		row := parallel.BigQueryRow{
			ProjectId: projectId,
			DataSetId: "datastore",
			TableId:   "order" + tableSuffix,
			Row:       data,
		}
		*rows = append(*rows, row)
	},
)
