package wallet

import (
	"errors"
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/util/blockchain"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"
)

type CreateAccountRequest struct {
	Name       string `json:"name"`
	Blockchain string `json:"blockchain"`
}

type PayFromAccountRequest struct {
	Name   string         `json:"name"`
	To     string         `json:"to"`
	Amount currency.Cents `json:"amount"`
	Fee    currency.Cents `json:"fee"`
}

type PayFromAccountResponse struct {
	TransactionId string `json:"transactionId"`
}

func Get(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	u := user.New(db)
	if err := u.GetById(id); err != nil {
		http.Fail(c, 400, "Could not query user", err)
		return
	}

	userWallet, err := returnWallet(u, db)
	if err != nil || userWallet == nil {
		http.Fail(c, 400, "Unable to user retrieve wallet from datastore", err)
	}
	u.MustUpdate()

	http.Render(c, 200, userWallet)
}

func GetAccount(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	u := user.New(db)
	if err := u.GetById(id); err != nil {
		http.Fail(c, 400, "Could not query user", err)
		return
	}

	userWallet, err := returnWallet(u, db)
	if err != nil {
		http.Fail(c, 400, "Unable to retrieve wallet from datastore", err)
	}
	log.Debug("Requested account name: %v", c.Params.ByName("name"))
	account, success := userWallet.GetAccountByName(c.Params.ByName("name"))
	if !success {
		http.Fail(c, 404, "Requested account name was not found.", errors.New("Requested account name was not found."))
		return
	}
	u.MustUpdate()
	http.Render(c, 200, account)
}

func CreateAccount(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	u := user.New(db)
	if err := u.GetById(id); err != nil {
		http.Fail(c, 400, "Could not query user", err)
		return
	}

	userWallet, err := returnWallet(u, db)
	if err != nil {
		http.Fail(c, 400, "Unable to retrieve wallet from datastore", err)
	}
	request := CreateAccountRequest{}
	if err := json.Decode(c.Request.Body, &request); err != nil {
		http.Fail(c, 400, "Failed to decode request body", err)
		return
	}
	log.Debug("Blockchain requested for account creation: %v", request.Blockchain)
	blockchainType := blockchains.Type(request.Blockchain)
	account, err := userWallet.CreateAccount(request.Name, blockchainType, []byte(u.WalletPassphrase))
	if err != nil {
		http.Fail(c, 400, "Failed to create requested account", err)
		return
	}
	u.MustUpdate()

	http.Render(c, 200, account)
}

func Send(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	u := user.New(db)
	if err := u.GetById(id); err != nil {
		http.Fail(c, 400, "Could not query user", err)
		return
	}

	userWallet, err := returnWallet(u, db)
	if err != nil {
		http.Fail(c, 400, "Unable to retrieve wallet from datastore", err)
	}
	request := PayFromAccountRequest{}
	if err := json.Decode(c.Request.Body, &request); err != nil {
		http.Fail(c, 400, "Failed to decode request body", err)
		return
	}

	account, success := userWallet.GetAccountByName(request.Name)
	if !success {
		http.Fail(c, 404, "Requested account name was not found.", errors.New("Requested account name was not found."))
		return
	}
	transactionId, err := blockchain.MakePayment(middleware.GetContext(c), *account, request.To, request.Amount, request.Fee, []byte(u.WalletPassphrase))
	if err != nil {
		http.Fail(c, 400, "Failed to make payment.", err)
		return
	}
	u.MustUpdate()

	http.Render(c, 200, PayFromAccountResponse{transactionId})
}

func returnWallet(u *user.User, db *datastore.Datastore) (*wallet.Wallet, error) {
	ret, err := u.GetOrCreateWallet(db)
	if err != nil {
		return nil, err
	}
	if u.WalletPassphrase == "" {
		u.WalletPassphrase = rand.SecretKey()
	}

	return ret, nil
}
