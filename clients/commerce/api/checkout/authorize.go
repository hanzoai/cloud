package checkout

import (
	"context"
	"math/rand"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/api/checkout/authorizenet"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/balance"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/bitcoin"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/ethereum"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/null"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/paypal"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/square"
	"github.com/hanzoai/cloud/clients/commerce/events"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/fee"
	"github.com/hanzoai/cloud/clients/commerce/models/multi"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/tokensale"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/types/client"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/reflect"

	// temp to test go get ./...
	_ "github.com/hanzoai/cloud/clients/commerce/thirdparty/paymentmethods/plaid"
)

// Decode authorization request, grab user and payment information off it
func decodeAuthorization(c *gin.Context, ord *order.Order) (*user.User, *payment.Payment, *TokenSale, error) {
	a := new(Authorization)
	db := ord.Datastore()

	// Decode request
	if err := json.Decode(c.Request.Body, a); err != nil {
		log.Error("Failed to decode request body: %v\n%v", c.Request.Body, err, c)
		return nil, nil, nil, FailedToDecodeRequestBody
	}

	log.JSON("Authorization:", a)

	// Copy request order into order used everywhere
	if a.Order != nil {
		reflect.Copy(a.Order, ord)
	}

	// Use provided order rather than initialize another order and break references
	a.Order = ord

	// Initialize and normalize models in authorization request
	if err := a.Init(db); err != nil {
		return nil, nil, nil, err
	}

	log.JSON("Order after initalization:", ord)

	return a.User, a.Payment, a.TokenSale, nil
}

func authorize(c *gin.Context, org *organization.Organization, ord *order.Order) (*payment.Payment, error) {
	var fees []*fee.Fee

	// Decode authorization request
	usr, pay, tsPass, err := decodeAuthorization(c, ord)
	if err != nil {
		log.Warn("Could not Decode '%v': '%v'", ord, err, c)
		return nil, err
	}

	log.Info("Decoded:", c)
	log.Info("User: '%v'", json.Encode(usr), c)
	// log.Info("Payment: '%v'", json.Encode(pay), c)
	log.Info("Token Sale: '%v'", json.Encode(tsPass), c)

	// Check if store has been set, if so pull it out of the context
	var stor *store.Store
	v, ok := c.Get("store")
	if ok {
		stor = v.(*store.Store)
		ord.Currency = stor.Currency // Set currency
		log.Info("Using Store '%v'", stor.Id(), c)
	} else if ord.StoreId != "" {
		stor = store.New(ord.Datastore())
		if err := stor.GetById(ord.StoreId); err != nil {
			log.Warn("Store '%v' does not exist: %v", ord.StoreId, err, c)
			stor = nil
		}
		log.Info("Using Store '%v'", ord.StoreId, c)
	}

	log.Info("Order Before Tally: '%v'", json.Encode(ord), c)

	// Update order with information from datastore, and tally
	if err := ord.UpdateAndTally(stor); err != nil {
		log.Error("Invalid or incomplete order error: %v", err, c)
		return nil, InvalidOrIncompleteOrder
	}

	if ord.Total < 1 {
		log.Error("Order : %v", err, c)
		return nil, InvalidOrIncompleteOrder
	}

	// check for reservations
	if err := ord.MakeReservations(); err != nil {
		log.Error("Item already reserved error: %v", err, c)
		return nil, FailedToReserveItem
	}

	log.Info("Order After Tally: '%v'", json.Encode(ord), c)

	// Update order with information from datastore, and tally
	if err := ord.CreateSubscriptionsFromItems(stor); err != nil {
		log.Error("Invalid or incomplete order error: %v", err, c)
		return nil, err
	}

	log.Info("Order After CreateSubscriptions: '%v'", json.Encode(ord), c)

	// Validate token sale only if both password and id are set
	if (ord.TokenSaleId != "") && (tsPass != nil) {
		ts := tokensale.New(ord.Datastore())
		if err := ts.GetById(ord.TokenSaleId); err != nil {
			log.Error("Token sale not found error: %v", err, c)
			return nil, TokenSaleNotFound
		}

		// Create ethereum block chain wallets for funding
		w, err := usr.GetOrCreateWallet(usr.Datastore())
		if err != nil {
			log.Error("Wallet creation error: %v", err, c)
			return nil, WalletCreationError
		}

		_, err = w.CreateAccount("Account for Order "+ord.Id(), blockchains.EthereumType, []byte(tsPass.Passphrase))
		if err != nil {
			log.Error("Funding account creation error: %v", err, c)
			return nil, FundingAccountCreationError
		}
	} else if (ord.TokenSaleId != "") || (tsPass != nil) {
		log.Error("TokensSaleId '%s' or TokenSalePassphrase '%s' is missing", ord.TokenSaleId, tsPass, c)
		return nil, MissingTokenSaleOrPassphrase
	}

	// Override total if test email is used
	if org.IsTestEmail(usr.Email) {
		switch ord.Currency {
		case currency.BTC, currency.XBT:
			ord.Total = currency.Cents(1e5)
		case currency.ETH:
			ord.Total = currency.Cents(1e7)
		default:
			ord.Total = currency.Cents(50 + rand.Int63n(50))
		}

		if pay != nil {
			pay.Test = true
		}
	}

	// Set test mode based on org live status
	if !org.Live {
		ord.Test = true
		if pay != nil {
			pay.Test = true
			pay.Live = false
		}
	}

	usingFiat := payment.IsFiatProcessorType(ord.Type)

	// Ethereum payments are handles in the webhook
	if usingFiat {
		// Use updated order total
		pay.Amount = ord.Total

		// Capture client information to retain information about user at time of checkout
		pay.Client = client.New(c)

		// Calculate affiliate, partner and platform fees
		platformFees, partnerFees := org.Pricing()
		fee, fes, err := ord.CalculateFees(platformFees, partnerFees)
		if err != nil {
			log.Error("Fee calculation error: %v", err, c)
			return nil, FeeCalculationError
		}
		fees = fes
		pay.Fee = fee

		// Save payment Id on order
		ord.PaymentIds = append(ord.PaymentIds, pay.Id())
	}

	log.Info("Using Ord Type %v", ord.Type, c)
	// Handle authorization
	switch ord.Type {
	case accounts.BalanceType:
		log.Info("Using Balance", c)
		err = balance.Authorize(org, ord, usr, pay)
	case accounts.EthereumType:
		log.Info("Using Ethereum", c)
		if ord.Currency != currency.ETH {
			return nil, UnsupportedEthereumCurrency
		}
		err = ethereum.Authorize(org, ord, usr)
		if err != nil {
			log.Error("Ethereum Error: %v", err, c)
		}
	case accounts.BitcoinType:
		log.Info("Using Bitcoin", c)
		if ord.Currency != currency.BTC && ord.Currency != currency.XBT {
			return nil, UnsupportedBitcoinCurrency
		}
		err = bitcoin.Authorize(org, ord, usr)
		if err != nil {
			log.Error("Bitcoin Error: %v", err, c)
		}
	case accounts.NullType:
		log.Info("Using Null", c)
		err = null.Authorize(org, ord, usr, pay)
	case accounts.PayPalType:
		log.Info("Using Paypal", c)
		err = paypal.Authorize(org, ord, usr, pay)
	case accounts.AuthorizeNetType:
		log.Info("Using AuthroizeNet", c)
		if ord.Currency.IsCrypto() {
			return nil, UnsupportedFiatCurrency
		}
		err = authorizenet.Authorize(org, ord, usr, pay)
		if err != nil {
			log.Error("Authorize.net Error: %v", err, c)
		}
	case accounts.SquareType:
		log.Info("Using Square", c)
		if ord.Currency.IsCrypto() {
			return nil, UnsupportedFiatCurrency
		}
		if ord.Total > 5000000 {
			return nil, TransactionLimitReached
		}
		log.Info("Authorizing Square", c)
		err = square.Authorize(org, ord, usr, pay)
		if err != nil {
			log.Error("Square Error: %v", err, c)
			err = AuthorizationFailed
		}
	default:
		log.Info("Using Default (Square)", c)
		if ord.Currency.IsCrypto() {
			return nil, UnsupportedFiatCurrency
		}
		if ord.Total > 5000000 {
			return nil, TransactionLimitReached
		}
		log.Info("Authorizing Square", c)
		err = square.Authorize(org, ord, usr, pay)
		if err != nil {
			log.Error("Square Error: %v", err, c)
			err = AuthorizationFailed
		}
	}

	log.Warn("Done Authorizing: %v", pay, c)

	// Bail on authorization failure
	if err != nil {
		if err2 := ord.CancelReservations(); err2 != nil {
			log.Error("Cancel Reservation Error: %v", err2, c)
		}

		log.Error("Authorize Error: %v", err, c)
		// Update payment status accordingly
		ord.Status = order.Cancelled
		if pay != nil {
			pay.Status = payment.Cancelled
			pay.Account.Error = err.Error()
			pay.MustCreate()
		}

		ord.MustCreate()
		usr.MustCreate()
		return nil, err
	}

	// Batch save user, order, payment, fees
	entities := []interface{}{usr, ord, pay}

	if !usingFiat {
		entities = []interface{}{usr, ord}
	} else {
		// If the charge is not live or test flag is set, then it is a test charge
		ord.Test = pay.Test || !pay.Live

		// Link payments/fees
		for _, fe := range fees {
			fe.PaymentId = pay.Id()
			pay.FeeIds = append(pay.FeeIds, fe.Id())
			entities = append(entities, fe)
		}
	}

	if usr.CreatedAt.IsZero() && !ord.Test {
		if err := counter.IncrUser(usr.Context(), time.Now()); err != nil {
			log.Error("IncrUser Error %v", err, c)
		}
	}

	multi.MustCreate(entities)

	// Emit order_completed analytics event (fire and forget)
	if client, ok := c.Get("events"); ok {
		if ev, ok := client.(*events.Client); ok {
			evtOrd := &events.Order{
				ID:       ord.Id(),
				UserID:   usr.Id(),
				Email:    usr.Email,
				Total:    float64(ord.Total) / 100.0,
				Currency: string(ord.Currency),
				Status:   string(ord.Status),
				OrgID:    org.Name,
			}
			go func() {
				ctx := context.Background()
				ev.EmitOrderCompleted(ctx, evtOrd)
			}()
		}
	}

	// Publish order.created to NATS/JetStream with GA4 + Facebook CAPI (fire and forget)
	if pub, ok := c.Get("publisher"); ok {
		if p, ok := pub.(*events.Publisher); ok {
			orderItems := orderLineItemInfos(ord)
			go func() {
				if pubErr := p.PublishOrderCreated(context.Background(), ord.Id(), org.Name, usr.Id(), usr.Email,
					int64(ord.Total), string(ord.Currency), events.ToOrderItems(orderItems)); pubErr != nil {
					log.Error("PublishOrderCreated: %v", pubErr, c)
				}
			}()
		}
	}

	return pay, nil
}
