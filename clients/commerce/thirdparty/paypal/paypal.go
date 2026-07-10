package paypal

import (
	"context"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/paypal/responses"
)

type Client struct {
	ctx context.Context
}

func New(ctx context.Context) *Client {
	return &Client{ctx: ctx}
}

func escape(s string) string {
	return strings.Replace(s, "%", "%%", -1)
}

func setupHeaders(req *http.Request, ord *order.Order, org *organization.Organization) {
	if config.IsProduction {
		req.Header.Set("X-PAYPAL-SECURITY-USERID", org.Paypal.Live.SecurityUserId)
		req.Header.Set("X-PAYPAL-SECURITY-PASSWORD", org.Paypal.Live.SecurityPassword)
		req.Header.Set("X-PAYPAL-SECURITY-SIGNATURE", org.Paypal.Live.SecuritySignature)
		req.Header.Set("X-PAYPAL-APPLICATION-ID", org.Paypal.Live.ApplicationId)
	} else {
		req.Header.Set("X-PAYPAL-SECURITY-USERID", org.Paypal.Test.SecurityUserId)
		req.Header.Set("X-PAYPAL-SECURITY-PASSWORD", org.Paypal.Test.SecurityPassword)
		req.Header.Set("X-PAYPAL-SECURITY-SIGNATURE", org.Paypal.Test.SecuritySignature)
		req.Header.Set("X-PAYPAL-APPLICATION-ID", org.Paypal.Test.ApplicationId)
	}

	req.Header.Set("X-PAYPAL-REQUEST-DATA-FORMAT", "NV")
	req.Header.Set("X-PAYPAL-RESPONSE-DATA-FORMAT", "JSON")
}

func (c Client) Pay(pay *payment.Payment, ord *order.Order, org *organization.Organization) (string, error) {
	data := url.Values{}
	data.Set("actionType", "PAY")
	// Standard sandbox APP ID, for testing
	if config.IsProduction {
		data.Set("clientDetails.applicationId", org.Paypal.Live.ApplicationId)
	} else {
		data.Set("clientDetails.applicationId", org.Paypal.Test.ApplicationId)
	}
	// IP address from which request is sent.
	data.Set("clientDetails.ipAddress", pay.Client.Ip)

	cur := pay.Currency

	data.Set("currencyCode", cur.Code())

	amount := cur.ToStringNoSymbol(pay.Amount)
	fee := cur.ToStringNoSymbol(pay.Fee)

	// Organization is primary receiver
	data.Set("receiverList.receiver(0).primary", "true")
	data.Set("receiverList.receiver(0).amount", amount)
	if config.IsProduction {
		data.Set("receiverList.receiver(0).email", org.Paypal.Live.Email)
	} else {
		data.Set("receiverList.receiver(0).email", org.Paypal.Test.Email)
	}

	// We take our fee as the second receiver
	data.Set("receiverList.receiver(1).amount", fee)
	data.Set("receiverList.receiver(1).email", config.Paypal.Email)
	data.Set("receiverList.receiver(1).primary", "false")

	data.Set("requestEnvelope.errorLanguage", "en-US")
	data.Set("returnUrl", org.Paypal.ConfirmUrl+"#checkoutsuccess")
	data.Set("cancelUrl", org.Paypal.CancelUrl+"#checkoutfailure")
	data.Set("ipnNotificationUrl", config.Paypal.IpnUrl+org.Name)
	data.Set("feesPayer", "PRIMARYRECEIVER")

	// Make payment request
	req, err := http.NewRequest("POST", config.Paypal.Api+"/AdaptivePayments/Pay", strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}

	setupHeaders(req, ord, org)

	req.PostForm = data

	dump, _ := httputil.DumpRequestOut(req, true)
	log.Debug("%v", escape(string(dump)), c.ctx)

	client := &http.Client{Timeout: 55 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		log.Error("Request Came Back With Error %v", err, c.ctx)
		return "", err
	} else {
		defer res.Body.Close()
	}

	responseBytes, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Error("Could Not Decode Response %v", err, c.ctx)
		return "", err
	}

	log.Debug("Response Bytes: %v", string(responseBytes), c.ctx)

	paymentResponse := responses.ParallelPaymentResponse{}
	err = json.Unmarshal(responseBytes, &paymentResponse)

	if err != nil {
		log.Error("Could Not Unmarshal Response: %v", err, c.ctx)
		return "", err
	}

	errs := len(paymentResponse.Error)
	if errs > 0 {
		errStr := ""
		if errs > 1 {
			errStr = " and " + strconv.Itoa(errs) + " others"
		}
		log.Error("PayPal Error: %v", paymentResponse.Error[0].Message+errStr, c.ctx)
		return "", errors.New("PayPal Error: " + paymentResponse.Error[0].Message + errStr)
	}

	// Update payment with PayKey
	pay.Account.PayKey = paymentResponse.PayKey

	return paymentResponse.PayKey, nil
}

func (c Client) SetPaymentOptions(pay *payment.Payment, ord *order.Order, org *organization.Organization) error {
	cur := pay.Currency

	data := url.Values{}

	data.Set("requestEnvelope.errorLanguage", "en-US")
	data.Set("payKey", pay.Account.PayKey)

	// Can configure display options here -- probably should
	// data.Set("displayOptions.businessName", org.FullName)
	// data.Set("displayOptions.headerImageUrl", "")
	// data.Set("displayOptions.emailHeaderImageUrl", "")
	// data.Set("displayOptions.emailMarketingImageUrl", "")

	// Set receiver email to match organizations
	if config.IsProduction {
		data.Set("receiverOptions[0].receiver.email", org.Paypal.Live.Email)
	} else {
		data.Set("receiverOptions[0].receiver.email", org.Paypal.Test.Email)
	}

	// Set order id for reference later
	data.Set("receiverOptions[0].customId", ord.Id())

	// Simple description
	data.Set("receiverOptions[0].description", ord.DescriptionLong())

	// Add invoice data
	if pay.Test {
		data.Set("receiverOptions[0].invoiceData.item[0].itemCount", "1")
		data.Set("receiverOptions[0].invoiceData.item[0].name", "test")
		data.Set("receiverOptions[0].invoiceData.item[0].price", "0.50")
	} else {
		// Add each line item
		for i, lineItem := range ord.Items {
			n := strconv.Itoa(i)
			data.Set("receiverOptions[0].invoiceData.item["+n+"].identifier", lineItem.DisplayId())
			data.Set("receiverOptions[0].invoiceData.item["+n+"].name", lineItem.String())
			data.Set("receiverOptions[0].invoiceData.item["+n+"].itemCount", strconv.Itoa(lineItem.Quantity))
			data.Set("receiverOptions[0].invoiceData.item["+n+"].itemPrice", cur.ToStringNoSymbol(lineItem.Price))
			data.Set("receiverOptions[0].invoiceData.item["+n+"].price", cur.ToStringNoSymbol(lineItem.TotalPrice()))
		}
	}

	// Add shipping, tax
	data.Set("receiverOptions[0].invoiceData.totalShipping", cur.ToStringNoSymbol(ord.Shipping))
	data.Set("receiverOptions[0].invoiceData.totalTax", cur.ToStringNoSymbol(ord.Tax))

	// Make request
	req, err := http.NewRequest("POST", config.Paypal.Api+"/AdaptivePayments/SetPaymentOptions", strings.NewReader(data.Encode()))
	if err != nil {
		log.Error("Request Came Back With Error %v", err, c.ctx)
		return err
	}

	setupHeaders(req, ord, org)

	req.PostForm = data

	dump, _ := httputil.DumpRequestOut(req, true)
	log.Debug("%v", escape(string(dump)), c.ctx)

	client := &http.Client{Timeout: 55 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		log.Error("Request Came Back With Error %v", err, c.ctx)
		return err
	} else {
		defer res.Body.Close()
	}

	responseBytes, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Error("Could Not Decode Response %v", err, c.ctx)
		return err
	}

	log.Debug("Response Bytes: %v", string(responseBytes), c.ctx)

	setPaymentOptionsResponse := responses.SetPaymentOptionsResponse{}
	err = json.Unmarshal(responseBytes, &setPaymentOptionsResponse)

	if err != nil {
		log.Error("Could Not Unmarshal Response: %v", err, c.ctx)
		return err
	}

	if setPaymentOptionsResponse.ResponseEnvelope.Ack != "Success" {
		log.Error("Problem encountered while setting payment options.  Returned code: %v", setPaymentOptionsResponse.ResponseEnvelope.Error)
	}

	return nil
}

func (c Client) GetPayKey(pay *payment.Payment, ord *order.Order, org *organization.Organization) (string, error) {
	payKey, err := c.Pay(pay, ord, org)
	c.SetPaymentOptions(pay, ord, org)
	return payKey, err
}

func (c Client) GetPaymentDetails(payKey string, ord *order.Order, org *organization.Organization) (*responses.PaymentDetailsResponse, error) {
	req, err := http.NewRequest("POST", config.Paypal.Api+"/AdaptivePayments/PaymentDetails", nil)
	if err != nil {
		return nil, err
	}

	setupHeaders(req, ord, org)

	return nil, nil
}
