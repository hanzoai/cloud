package shipnotify

import (
	"encoding/xml"
	"io/ioutil"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/fulfillment"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/email"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

// <?xml version="1.0" encoding="utf-8"?>
// <ShipNotice>
//        <OrderNumber>ABC123</OrderNumber>
//        <CustomerCode>dev@hanzo.ai</CustomerCode>
//        <LabelCreateDate>12/8/2011 12:56 PM</LabelCreateDate>
//        <ShipDate>12/8/2011</ShipDate>
//        <Carrier>USPS</Carrier>
//        <Service>Priority Mail</Service>
//        <TrackingNumber>1Z909084330298430820</TrackingNumber>
//        <ShippingCost>4.95</ShippingCost>
// <Recipient>
//               <Name>The President</Name>
//               <Company>US Govt</Company>
//               <Address1>1600 Pennsylvania Ave</Address1>
//               <Address2></Address2>
//               <City>Washington</City>
//               <State>DC</State>
//               <PostalCode>20500</PostalCode>
//               <Country>US</Country>
//        </Recipient>
//        <Items>
//               <Item>
//                      <SKU>FD88821</SKU>
//                      <Name>My Product Name</Name>
//                      <Quantity>2</Quantity>
//               </Item>
//        </Items>
// </ShipNotice>

func parseDate(s string) time.Time {
	date, err := time.Parse("01/02/2006", s)
	if err != nil {
		log.Panic("Unable to parse date: %v", err)
	}
	return date
}

func parseTime(s string) time.Time {
	date, err := time.Parse("01/02/2006 15:04", s)
	if err != nil {
		log.Panic("Unable to parse time: %v", err)
	}
	return date
}

type Request struct {
	OrderNumber     string
	CustomerCode    string
	LabelCreateDate string
	ShipDate        string
	Carrier         string
	Service         string
	TrackingNumber  string
	ShippingCost    string

	Recipient struct {
		Name       string
		Company    string
		Address1   string
		Address2   string
		City       string
		State      string
		PostalCode string
		Country    string
	}

	Items []struct {
		SKU      string
		Name     string
		Quantity string
	}
}

func ShipNotify(c *gin.Context) {
	query := c.Request.URL.Query()

	// Only support export action
	action := query.Get("action")
	if action != "shipnotify" {
		log.Panic("Invalid action %s, only understand 'shipnotify'", action, c)
	}

	orderNumber := query.Get("order_number")
	id, err := strconv.Atoi(orderNumber)
	if err != nil {
		log.Panic("Unable to convert order_number '%s' to int: %v", orderNumber, err, c)
	}

	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	ord := order.New(db)
	ok, err := ord.Query().Filter("Number=", id).Get()
	if !ok || err != nil {
		log.Warn("Unable to find order '%s': %v", id, err, c)
		c.String(200, "ok\n")
		return
	}

	b, err := ioutil.ReadAll(c.Request.Body)
	if err != nil {
		log.Panic("Failed to read request body: %v", err, c)
	}

	req := Request{}
	if err := xml.Unmarshal(b, &req); err != nil {
		log.Panic("Unable to unmarshal XML: %v", err, c)
	}

	if ord.Fulfillment.Trackings[0].Number != req.TrackingNumber {
		ord.Fulfillment.Status = fulfillment.Tracked
		ord.Fulfillment.Trackings[0].Number = req.TrackingNumber
		ord.Fulfillment.Trackings[0].LabelCreatedAt = parseTime(req.LabelCreateDate)
		ord.Fulfillment.Service = req.Service
		ord.Fulfillment.Carrier = req.Carrier
		ord.Fulfillment.Pricing = currency.CentsFromString(req.ShippingCost)

		usr := user.New(db)
		usr.MustGetById(ord.UserId)

		pay := payment.New(db)
		pay.MustGetById(ord.PaymentIds[0])

		email.SendOrderShipped(db.Context, org, ord, usr, pay)
	}

	ord.MustPut()

	c.String(200, "ok\n")
}
