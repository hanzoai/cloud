package ipn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

// Read body from response
func readBody(res *http.Response) (string, error) {
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", errors.New(fmt.Sprintf("Invalid status code: %v", res.Status))
	}

	bytes, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	return string(bytes), nil
}

func respond(ctx context.Context, message url.Values) (string, error) {
	req, err := http.NewRequest("POST", config.Paypal.PaypalIpnUrl, bytes.NewBufferString(message.Encode()))
	if err != nil {
		log.Panic("Could create request: %s", err, ctx)
	}

	dump, _ := httputil.DumpRequestOut(req, true)
	log.Debug("IPN response: %s", string(dump), ctx)

	// Set timeout
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, time.Second*30)
	defer cancel()

	// Create client
	client := &http.Client{Timeout: 55 * time.Second}

	// Make Post request
	res, err := client.Do(req)
	if err != nil {
		log.Panic("Unable to make request: %v", err, ctx)
	}

	return readBody(res)
}

func Webhook(c *gin.Context) {
	orgName := c.Params.ByName("organization")
	if orgName == "" {
		log.Panic("Organization not specified", c)
	}

	// Get org
	db := datastore.New(c)
	org := organization.New(db)
	err := org.GetById(orgName)

	// Get namespaced db
	db.SetNamespace(org.Name)

	ctx := db.Context

	// Parse form
	if err := c.Request.ParseForm(); err != nil {
		log.Panic("Failed to parse request from PayPal", c)
	}

	form := c.Request.Form
	log.Debug("IPN message: %v", form, ctx)

	// Append cmd=_notify-validate
	form.Add("cmd", "_notify-validate")

	// Send command as received with cmd=_notify-validate, in its own request client.  Check to make sure Paypal responds with "VALIDATED".
	c.String(200, "")

	// Send response
	status, err := respond(ctx, form)
	if err != nil {
		log.Panic("Failed to respond to PayPal: %s", err, ctx)
	}

	if status != "VERIFIED" {
		log.Panic("Response was not verified", ctx)
	}

	// Parse form into ipnMessage for ease of use.
	ipnMessage := NewIpnMessage(form)

	// Update payment
	pay := payment.New(db)
	_, err = pay.Query().Filter("Account.PayKey=", ipnMessage.PayKey).Get()
	if err != nil {
		log.Panic("Could not find PayKey: %s", err, ctx)
		return
	}

	ord := order.New(db)
	err = ord.GetById(pay.OrderId)
	if err != nil {
		log.Panic("Could not find Order: %s", err, ctx)
		return
	}

	if ipnMessage.Status != "Completed" {
		switch ipnMessage.Status {
		case "Processing", "Pending", "Created":
			return
		case "Refunded", "Partially_Refunded", "Reversed":
			pay.Status = payment.Refunded
			ord.Status = order.Cancelled
		// Denied, Failed, Voided
		default:
			pay.Status = payment.Failed
			ord.Status = order.Cancelled
		}

		ord.PaymentStatus = pay.Status

		// No need to call Refund API.
		pay.MustUpdate()
		ord.MustUpdate()
		return
	}

	if pay.Amount != ipnMessage.Amount || pay.Currency != ipnMessage.Currency {
		// Probably fraud.
		pay.Status = payment.Fraudulent
		pay.MustUpdate()

		ord.Status = order.Cancelled
		ord.PaymentStatus = pay.Status
		ord.MustUpdate()

		// call refund API
		return
	}

	// Looking good.
	pay.Status = payment.Paid
	pay.MustUpdate()

	// TODO: Make this part of the payment model API
	// checkoutApi.CompleteCapture(c, org, ord, []*aeds.Key{pay.Key().(*aeds.Key)}, []*payment.Payment{pay})
}

func Route(router router.Router, args ...gin.HandlerFunc) {
	api := router.Group("paypal")
	api.POST("/ipn/:organization", Webhook)
}
