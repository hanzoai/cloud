package authorizenet

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httputil"

	"github.com/hanzoai/goauthorizenet"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/refs"
	json2 "github.com/hanzoai/cloud/clients/commerce/util/json"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

type Client struct {
	client         *http.Client
	ctx            context.Context
	loginId        string
	transactionKey string
	Key            string
	test           bool
}

func New(ctx context.Context, loginId string, transactionKey string, key string, test bool) *Client {

	// Set deadline
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, time.Second*55)
	defer cancel()

	// Set HTTP Client
	httpClient := &http.Client{Timeout: 55 * time.Second}

	return &Client{httpClient, ctx, loginId, transactionKey, key, test}

}

func (c Client) getTestValue() string {
	if c.test {
		return "test"
	}
	return "live"
}

func ToStringExpirationDate(month int, year int) string {

	y := strconv.Itoa(year)
	l := len(y)
	twoDigitYear := y
	if l > 2 {
		twoDigitYear = y[l-2:]
	}

	if month < 10 {
		return "0" + strconv.Itoa(month) + "/" + twoDigitYear
	}
	return strconv.Itoa(month) + "/" + twoDigitYear
}

func HanzoToAuthorizeSubscription(sub *order.Subscription) *authorizenet.Subscription {

	interval := authorizenet.IntervalMonthly()
	if sub.Interval == Yearly {
		interval = authorizenet.IntervalYearly()
	}
	subscription := authorizenet.Subscription{
		Name:        sub.ProductId + sub.PlanId,
		Amount:      sub.Currency.ToStringNoSymbol(sub.Price),
		TrialAmount: "0.00",
		PaymentSchedule: &authorizenet.PaymentSchedule{
			StartDate:        sub.PeriodStart.Format("2006-01-02"),
			TotalOccurrences: "9999",
			TrialOccurrences: strconv.Itoa(sub.TrialPeriodsRemaining()),
			Interval:         interval,
		},
		Payment: &authorizenet.Payment{
			CreditCard: authorizenet.CreditCard{
				CardNumber:     sub.Account.Number,
				ExpirationDate: ToStringExpirationDate(sub.Account.Month, sub.Account.Year),
				CardCode:       sub.Account.CVC,
			},
		},
		BillTo: &authorizenet.BillTo{
			FirstName: sub.Buyer.FirstName,
			LastName:  sub.Buyer.LastName,
			Address:   sub.Buyer.BillingAddress.Line1,
			City:      sub.Buyer.BillingAddress.City,
			State:     sub.Buyer.BillingAddress.State,
			Zip:       sub.Buyer.BillingAddress.PostalCode,
			Country:   sub.Buyer.BillingAddress.Country,
		},
	}
	return &subscription
}

// Covert a payment model into a card card we can use for authorization
func PaymentToNewTransaction(pay *payment.Payment) *authorizenet.NewTransaction {
	newTransaction := authorizenet.NewTransaction{
		Amount:     pay.Currency.ToStringNoSymbol(pay.Amount),
		RefTransId: pay.Account.RefTransId,
		CreditCard: authorizenet.CreditCard{
			CardNumber:     pay.Account.Number,
			ExpirationDate: ToStringExpirationDate(pay.Account.Month, pay.Account.Year),
			CardCode:       pay.Account.CVC,
		},
		BillTo: &authorizenet.BillTo{
			Address: pay.Buyer.BillingAddress.Line1,
			City:    pay.Buyer.BillingAddress.City,
			State:   pay.Buyer.BillingAddress.State,
			Zip:     pay.Buyer.BillingAddress.PostalCode,
			Country: pay.Buyer.BillingAddress.Country,
		},
	}
	log.Warn("Payment %v", json2.Encode(pay), pay.Datastore().Context)
	log.Warn("New Transaction %v", json2.Encode(newTransaction), pay.Datastore().Context)
	return &newTransaction
}

func PaymentToPreviousTransaction(pay *payment.Payment) *authorizenet.PreviousTransaction {
	prevTransaction := authorizenet.PreviousTransaction{
		Amount: pay.Currency.ToStringNoSymbol(pay.Amount),
		RefId:  pay.Account.TransId,
	}
	return &prevTransaction
}

func PopulatePaymentWithResponse(pay *payment.Payment, tran *authorizenet.TransactionResponse) (*payment.Payment, error) {
	msgs := make([]string, 0)
	for _, msg := range tran.Response.Message.Message {
		msgs = append(msgs, "Code: "+msg.Code+", "+msg.Description)
	}

	errMsgs := make([]string, 0)
	for _, msg := range tran.Response.Errors {
		errMsgs = append(errMsgs, "Code: "+msg.ErrorCode+", "+msg.ErrorText)
	}

	pay.Account.AuthCode = tran.Response.AuthCode
	pay.Account.AvsResultCode = tran.Response.AvsResultCode
	pay.Account.CvvResultCode = tran.Response.CvvResultCode
	pay.Account.CavvResultCode = tran.Response.CavvResultCode
	pay.Account.TransId = tran.Response.TransID
	pay.Account.RefTransId = tran.Response.RefTransID
	pay.Account.TransHash = tran.Response.TransHash
	pay.Account.TestRequest = tran.Response.TestRequest
	pay.Account.AccountNumber = tran.Response.AccountNumber
	pay.Account.AccountType = tran.Response.AccountType
	pay.Account.Messages = strings.Join(msgs, ", ")
	pay.Account.ErrorMessages = strings.Join(errMsgs, ", ")

	if len(errMsgs) > 0 {
		return pay, errors.New(pay.Account.ErrorMessages)
	}

	return pay, nil
}

func PopulateSubscriptionWithResponse(sub *order.Subscription, tran *authorizenet.SubscriptionResponse) *order.Subscription {
	if tran.SubscriptionID != "" {
		sub.Ref.AuthorizeNet.SubscriptionId = tran.SubscriptionID
	}
	if tran.Profile.CustomerProfileID != "" {
		sub.Ref.AuthorizeNet.CustomerProfileId = tran.Profile.CustomerProfileID
	}
	if tran.Profile.CustomerPaymentProfileID != "" {
		sub.Ref.AuthorizeNet.CustomerPaymentProfileId = tran.Profile.CustomerPaymentProfileID
	}
	sub.Ref.Type = refs.AuthorizeNetRefType

	return sub
}

func (c Client) NewSubscription(sub *order.Subscription) (*order.Subscription, error) {
	//authorizenet.SetAPIInfo(c.loginId, c.transactionKey, c.getTestValue())
	//authorizenet.SetHTTPClient(c.client)

	subscription := HanzoToAuthorizeSubscription(sub)

	response, err := ChargeSubscription(c.ctx, *subscription, c.test)

	if err != nil {
		log.Error("Authorize.net NewSubscription 1 %v, Error %v", json2.Encode(sub), err, c.ctx)
		return sub, err
	}

	if response.Approved() {
		sub, err = PopulateSubscriptionWithResponse(sub, response), nil
		if err != nil {
			log.Error("Authorize.net NewSubscription 2 %v", err, c.ctx)
		}
		sub.Status = order.ActiveSubscriptionStatus
		return sub, err
	} else {
		log.Warn("NewSubscription Failed")
		log.Debug("Authorize: Authorize.Net API did not approve transaction")
		return sub, NewSubscriptionFailedError
	}
}

// Update subscribe to a plan
func (c Client) UpdateSubscription(sub *order.Subscription) (*order.Subscription, error) {
	/*
		//authorizenet.SetAPIInfo(c.loginId, c.transactionKey, c.getTestValue())
		//authorizenet.SetHTTPClient(c.client)

		subscription := HanzoToAuthorizeSubscription(sub)
		subscription.SubscriptionId = sub.Ref.AuthorizeNet.SubscriptionId

		response, err := subscription.Update()

		if err != nil {
			log.Error("Authorize.net UpdateSubscription 1 %v, Error %v", json2.Encode(sub), err, c.ctx)
			return sub, err
		}

		if response.Approved() {
			sub, err = PopulateSubscriptionWithResponse(sub, response), nil
			if err != nil {
				log.Error("Authorize.net NewSubscription 2 %v", err, c.ctx)
			}
			return sub, err
		} else {
			log.Warn("UpdateSubscription Failed")
			log.Debug("Authorize: Authorize.Net API did not approve transaction")
			return sub, UpdateSubscriptionFailedError
		}
		return sub, err
	*/
	return nil, nil
}

// Subscribe to a plan
func (c Client) CancelSubscription(sub *order.Subscription) (*order.Subscription, error) {
	/*
		authorizenet.SetAPIInfo(c.loginId, c.transactionKey, c.getTestValue())
		authorizenet.SetHTTPClient(c.client)

		s := authorizenet.SetSubscription{
			Id: sub.Ref.AuthorizeNet.SubscriptionId,
		}
		_, err := s.Cancel()

		if err == nil {
			sub.Canceled = true
			sub.Status = order.CancelledSubscriptionStatus
			return sub, nil
		}
		return sub, err
	*/
	return nil, nil
}

// Do authorization, return token
func (c Client) Authorize(pay *payment.Payment) (*payment.Payment, error) {
	/*
		newTransaction := PaymentToNewTransaction(pay)

		log.Debug("Authorize: Setting API Info")
		authorizenet.SetAPIInfo(c.loginId, c.transactionKey, c.getTestValue())
		authorizenet.SetHTTPClient(c.client)

		log.JSON(newTransaction)

		log.Debug("Authorize: Invoking Authorize.net API")
		response, err := AuthOnly(c.ctx, *newTransaction, c.test)

		if err != nil {
			log.Error("Authorize.net Authorize 1 %v / %v, Error %v", json2.Encode(pay), json2.Encode(newTransaction), err, c.ctx)
			return pay, err
		}

		log.Debug("Authorize: Returned from Authorize.net API")
		if response.Approved() {
			log.Warn("Approved")
			pay, err := PopulatePaymentWithResponse(pay, response)
			if err != nil {
				log.Error("Authorize.net Authorize 2 %v", err, c.ctx)
			}
			return pay, err
		} else {
			log.Warn("Not Approved")
			log.Debug("Authorize: Authorize.Net API did not approve transaction")
			log.Debug("Authorize: Authorize.Net payment amount: %v", pay.Amount)
			log.Debug("Authorize: Authorize.Net card number: %v", pay.Account.Number)
			log.Debug("Authorize: Authorize.Net card expiration: %v", ToStringExpirationDate(pay.Account.Month, pay.Account.Year))
			log.Debug("Authorize: Authorize.Net returned error: %v", err, c.ctx)
			return pay, AuthorizeNotApprovedError
		}
	*/
	return nil, nil
}

// Attempts to refund payment and updates the payment in datastore
func (c Client) RefundPayment(pay *payment.Payment, refundAmount currency.Cents) (*payment.Payment, error) {
	if refundAmount > pay.Amount {
		return pay, RefundGreaterThanPaymentError
	}

	if refundAmount+pay.AmountRefunded > pay.Amount {
		return pay, RefundGreaterThanPaymentError
	}

	if pay.Status == payment.Unpaid {
		return pay, UnableToRefundUnpaidTransactionError
	}

	//authorizenet.SetAPIInfo(c.loginId, c.transactionKey, c.getTestValue())
	//authorizenet.SetHTTPClient(c.client)
	newTransaction := PaymentToNewTransaction(pay)
	newTransaction.Amount = pay.Currency.ToStringNoSymbol(refundAmount)
	newTransaction.RefTransId = pay.Account.TransId
	var tr = authorizenet.TransactionRequest{
		TransactionType: "refundTransaction",
		Amount:          newTransaction.Amount,
		RefTransId:      newTransaction.RefTransId,
		Payment: &authorizenet.Payment{
			CreditCard: newTransaction.CreditCard,
		},
	}

	response, err := SendTransactionRequest(c.ctx, tr, c.test)

	if err != nil {
		return pay, err
	}

	log.JSON(response)

	if response.Approved() {
		// Authorize.Net does not return the specific amount
		// refunded in this transaction. If the response is
		// approved you can only assume things went fine.
		pay.AmountRefunded = currency.Cents(refundAmount)
		if pay.AmountRefunded == pay.Amount {
			pay.Status = payment.Refunded
		}
		return pay, pay.Put()
	} else {
		log.Debug("Authorize: Authorize.Net API did not approve transaction")
		log.Debug("Authorize: Authorize.Net refTransId: %v", newTransaction.RefTransId)
		log.Debug("Authorize: Authorize.Net payment amount: %v", newTransaction.Amount)
		// log.Debug("Authorize: Authorize.Net card number: %v", newTransaction.CreditCard.CardNumber)
		// log.Debug("Authorize: Authorize.Net card expiration: %v", newTransaction.CreditCard.ExpirationDate)
		log.Debug("Authorize: Authorize.Net returned error: %v", err, c.ctx)

		if err == nil {
			err = MinimumRefundTimeNotReachedError
		}

		return pay, err
	}
}

// Create new charge
func (c Client) Charge(pay *payment.Payment) (*payment.Payment, error) {

	/*newTransaction := PaymentToNewTransaction(pay)

	authorizenet.SetAPIInfo(c.loginId, c.transactionKey, c.getTestValue())
	authorizenet.SetHTTPClient(c.client)
	response, err := newTransaction.Charge()

	if err != nil {
		log.Error("Authorize.net Charge 1 %v", err, c.ctx)
		return pay, err
	}

	if response.Approved() {
		pay, err = PopulatePaymentWithResponse(pay, response)
		if err != nil {
			log.Error("Authorize.net Charge 2 %v", err, c.ctx)
		} else {
			pay.Captured = true
		}
		return pay, err
	} else {
		return pay, ChargeNotApprovedError
	}*/
	return nil, nil
}

// Capture charge
func (c Client) Capture(pay *payment.Payment) (*payment.Payment, error) {
	/*log.Debug("Capture charge '%s'", pay.Account.AuthCode)
	oldTransaction := PaymentToPreviousTransaction(pay)

	authorizenet.SetAPIInfo(c.loginId, c.transactionKey, c.getTestValue())
	authorizenet.SetHTTPClient(c.client)
	response, err := Capture(c.ctx, *oldTransaction, c.test)

	if err != nil {
		log.Error("Authorize.net Capture 1 %v", err, c.ctx)
		return pay, err
	}

	if response.Approved() {
		pay, err = PopulatePaymentWithResponse(pay, response)
		if err != nil {
			log.Error("Authorize.net Capture 2 %v", err, c.ctx)
		} else {
			pay.Captured = true
		}
		return pay, err
	} else {
		return pay, CaptureNotApprovedError
	}*/
	return nil, nil
}

func AuthOnly(ctx context.Context, tranx authorizenet.NewTransaction, test bool) (*authorizenet.TransactionResponse, error) {
	var new authorizenet.TransactionRequest
	new = authorizenet.TransactionRequest{
		TransactionType: "authOnlyTransaction",
		Amount:          tranx.Amount,
		Payment: &authorizenet.Payment{
			CreditCard: tranx.CreditCard,
		},
	}
	response, err := SendTransactionRequest(ctx, new, test)
	return response, err
}

func Capture(ctx context.Context, tranx authorizenet.PreviousTransaction, test bool) (*authorizenet.TransactionResponse, error) {
	var new authorizenet.TransactionRequest
	new = authorizenet.TransactionRequest{
		TransactionType: "priorAuthCaptureTransaction",
		RefTransId:      tranx.RefId,
	}
	response, err := SendTransactionRequest(ctx, new, test)
	return response, err
}

func SendTransactionRequest(ctx context.Context, input authorizenet.TransactionRequest, test bool) (*authorizenet.TransactionResponse, error) {
	action := authorizenet.CreatePayment{
		CreateTransactionRequest: authorizenet.CreateTransactionRequest{
			//MerchantAuthentication: authorizenet.GetAuthentication(),
			TransactionRequest: input,
		},
	}

	jsoned, err := json.Marshal(action)
	if err != nil {
		return nil, err
	}

	response, err := SendRequest(ctx, jsoned, test)
	if err != nil {
		return nil, err
	}

	var dat authorizenet.TransactionResponse

	log.Warn("Returned Data: %s", response, ctx)
	err = json.Unmarshal(response, &dat)
	if err != nil {
		return nil, err
	}
	return &dat, err
}

func SendRequest(ctx context.Context, input []byte, test bool) ([]byte, error) {
	api_endpoint := "https://apitest.authorize.net/xml/v1/request.api"
	if !test {
		api_endpoint = "https://api.authorize.net/xml/v1/request.api"
	}
	log.Warn("Using testmode %v to send to %s", test, api_endpoint, ctx)

	req, err := http.NewRequest("POST", api_endpoint, bytes.NewBuffer(input))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 55 * time.Second}

	resp, err := client.Do(req)

	if err != nil {
		return nil, err
	}
	log.Warn("Request %s", string(input), ctx)

	dump, _ := httputil.DumpResponse(resp, true)
	log.Warn("Response %s", string(dump), ctx)

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	return body, err
}

func ChargeSubscription(ctx context.Context, sub authorizenet.Subscription, test bool) (*authorizenet.SubscriptionResponse, error) {
	return SendSubscription(ctx, sub, test)
}

func SendSubscription(ctx context.Context, sub authorizenet.Subscription, test bool) (*authorizenet.SubscriptionResponse, error) {
	action := authorizenet.CreateSubscriptionRequest{
		ARBCreateSubscriptionRequest: authorizenet.ARBCreateSubscriptionRequest{
			//MerchantAuthentication: authorizenet.GetAuthentication(),
			Subscription: sub,
		},
	}

	jsoned, err := json.Marshal(action)
	if err != nil {
		return nil, err
	}

	response, err := SendRequest(ctx, jsoned, test)
	if err != nil {
		return nil, err
	}

	var dat authorizenet.SubscriptionResponse
	err = json.Unmarshal(response, &dat)
	if err != nil {
		return nil, err
	}
	return &dat, err
}
