package ethereum

import (
	"context"
	ej "encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/common/hexutil"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/crypto"
	"github.com/luxfi/geth/rlp"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains/blocktransaction"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"

	. "github.com/hanzoai/cloud/clients/commerce/models/blockchains"
)

type EthGasStationResponse struct {
	BlockNum float64 `json:"blockNum"`

	BlockTime float64 `json:"block_time"`
	Speed     float64 `json:"speed"`

	FastestWait float64 `json:"fastestWait"`
	Fastest     float64 `json:"fastest"`

	FastWait float64 `json:"fastWait"`
	Fast     float64 `json:"fast"`

	AverageWait float64 `json:"avgWait"`
	Average     float64 `json:"average"`

	SafeLowWait float64 `json:"safeLowWait"`
	SafeLow     float64 `json:"safeLow"`
}

type Client struct {
	ctx        context.Context
	httpClient *http.Client

	address string

	IsTest   bool
	Commands []string
	Chain    ChainId
}

type JsonRpcError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type JsonRpcResponse struct {
	Id             int64         `json:"id"`
	JsonRpcVersion string        `json:"jsonrpc"`
	Result         ej.RawMessage `json:"result"`
	Error          JsonRpcError  `json:"error"`
}

var JsonRpcVersion = "2.0"
var JsonRpcMessage = `{"jsonrpc":"%s","method":"%s","params":%s,"id":%v}`

var IdMismatch = errors.New("Ids do not match!")
var InvalidChainId = errors.New("Invalid ChainId")

var IsTest = false

// Flip to Universal Test Mode
func Test(b bool) bool {
	IsTest = b
	return b
}

// Create a new Ethereum JSON-RPC client
func New(ctx context.Context, address string) Client {
	// Set timeout
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, time.Second*55)
	defer cancel()

	httpClient := &http.Client{Timeout: 55 * time.Second}

	return Client{ctx: ctx, httpClient: httpClient, address: address, IsTest: false, Commands: []string{}}
}

func paramsToString(parts ...interface{}) string {
	str, err := ej.Marshal(parts)
	if err != nil {
		panic(err)
	}

	return string(str)
}

// Flip to Test Mode
func (c Client) Test(b bool) bool {
	c.IsTest = b
	return b
}

// Post a JSON-RPC Command
func (c Client) Post(jsonRpcCommand string, id int64) (*JsonRpcResponse, error) {
	c.Commands = append(c.Commands, jsonRpcCommand)

	if c.IsTest || IsTest {
		jrr := &JsonRpcResponse{Result: ej.RawMessage([]byte(`"0x0"`))}
		return jrr, nil
	}

	log.Info("Posting command to Geth Node '%s': '%s'", c.address, jsonRpcCommand, c.ctx)
	res, err := c.httpClient.Post(c.address, "application/json", strings.NewReader(jsonRpcCommand))
	if err != nil {
		return nil, err
	}

	jrr := &JsonRpcResponse{}

	if err := json.Decode(res.Body, jrr); err != nil {
		return nil, err
	}

	log.Info("Received response from Geth Node '%s': %v", c.address, json.Encode(jrr), c.ctx)

	// This could mean there's a man in the middle attack?
	if jrr.Id != id {
		return nil, IdMismatch
	}

	if jrr.Error.Code != 0 {
		return jrr, errors.New(jrr.Error.Message)
	}
	return jrr, nil
}

// Send a transaction
func (c Client) SendTransaction(chainId ChainId, pk, from string, to string, amount, gasLimit, gasPrice *big.Int, data []byte) (string, error) {
	// Figure out what chain we are using
	var chainType Type
	switch chainId {
	case MainNet:
		chainType = EthereumType
	case Morden:
		chainType = EthereumMordenType
	case Ropsten:
		chainType = EthereumRopstenType
	default:
		return "", InvalidChainId
	}

	ctx := c.ctx

	log.Error("Sending Transaction on Chain '%s'", chainType, ctx)

	// Setup defaults
	if gasLimit.Cmp(big.NewInt(0)) <= 0 {
		gasLimit = big.NewInt(DefaultGas)
	}

	if gasPrice.Cmp(big.NewInt(0)) <= 0 {
		if price, err := c.GasPrice(); err != nil || price.Cmp(big.NewInt(0)) == 0 {
			log.Error("Could Not Determine Gas Price '%s': %v", price, err, ctx)
			gasPrice = big.NewInt(DefaultGasPrice)
		} else {
			log.Error("Current Gas Price is '%s'", price, ctx)
			gasPrice = price
		}
	}

	// Decode the private key
	privKey, err := crypto.HexToECDSA(pk)
	if err != nil {
		log.Error("Could Not Decode Hex '%s': %v", pk, err, ctx)
		return "", err
	}

	// Create a queued transaction to record this happened
	db := datastore.New(ctx)

	// Get Nonce from db
	nonce := uint64(0)

	nonceBt := blocktransaction.New(db)
	ok, err := nonceBt.Query().Filter("Type=", chainType).Filter("EthereumTransactionFrom=", from).Order("-EthereumTransactionNonce").Get()
	if err != nil {
		log.Error("Could Not Find Last BlockTransaction Due to Error: %v", err, ctx)
		return "", err
	} else if ok {
		nonce = uint64(nonceBt.EthereumTransactionNonce) + 1
	}

	// Create a signer for the particular chain using the modern signature
	// algorithm
	signer := types.NewEIP155Signer(big.NewInt(int64(chainId)))
	gasLimitUint64 := gasLimit.Uint64()
	tx := types.NewTransaction(nonce, common.HexToAddress(to), amount, gasLimitUint64, gasPrice, data)

	log.Info("Unsigned Transaction: %v", tx, ctx)

	// Sign the transaction
	signedTx, err := types.SignTx(tx, signer, privKey)
	if err != nil {
		log.Error("Could Not Sign Transaction: %v", err, ctx)
		return "", err
	}

	log.Info("Signed Transaction: %v", signedTx, ctx)

	// get RLP of transaction
	bytes, err := rlp.EncodeToBytes(signedTx)
	if err != nil {
		log.Error("Could Not Encode To Bytes: %v", err, ctx)
		return "", err
	}

	txHex := hexutil.Encode(bytes)

	id := rand.Int64()

	log.Info("Sending signed transaction: %s", txHex, c.ctx)
	jsonRpcCommand := fmt.Sprintf(JsonRpcMessage, JsonRpcVersion, "eth_sendRawTransaction", paramsToString(txHex), id)

	jrr, err := c.Post(jsonRpcCommand, id)
	if err != nil {
		return "", err
	}

	hash := string(jrr.Result)
	hash = hash[1 : len(hash)-1]

	bt := blocktransaction.New(db)
	bt.Id_ = string(chainType) + "/" + from + "/" + hash
	bt.UseStringKey = true
	bt.EthereumTransactionHash = hash
	bt.EthereumTransactionNonce = int64(nonce)
	bt.EthereumTransactionFrom = from
	bt.EthereumTransactionTo = to
	bt.EthereumTransactionValue = BigNumber(amount.String())
	bt.EthereumTransactionGasPrice = BigNumber(gasPrice.String())
	bt.EthereumTransactionGas = BigNumber(gasLimit.String())

	bt.Address = from
	bt.Usage = SenderUsage
	bt.Type = chainType
	bt.Status = QueuedProcessStatus

	if err := bt.Create(); err != nil {
		return "", err
	}

	return hash, nil
}

// Get the current average gasprice
func (c Client) GasPrice() (*big.Int, error) {
	gasPrice, _, err := c.GasPrice2()
	if err == nil {
		return gasPrice, nil
	} else {
		log.Warn("EthGasStation returned error '%s'", err, c.ctx)
	}

	id := rand.Int64()

	log.Info("Getting Gas Price", c.ctx)
	jsonRpcCommand := fmt.Sprintf(JsonRpcMessage, JsonRpcVersion, "eth_gasPrice", "", id)

	jrr, err := c.Post(jsonRpcCommand, id)
	if err != nil {
		return big.NewInt(DefaultGasPrice), err
	}

	priceHex := string(jrr.Result)
	priceHex = priceHex[1 : len(priceHex)-1]

	log.Info("Gas Price is %s", priceHex, c.ctx)

	a, err := hexutil.DecodeBig(priceHex)
	if err != nil {
		return big.NewInt(DefaultGasPrice), err
	}

	// 1 is the min price
	if a.Cmp(big.NewInt(0)) == 0 {
		return big.NewInt(1), nil
	}

	return a, nil
}

// Get the current average gasprice via https://ethgasstation.info/json/ethgasAPI.json
func (c Client) GasPrice2() (*big.Int, *EthGasStationResponse, error) {
	if c.IsTest || IsTest || true {
		return big.NewInt(1), nil, nil
	}

	log.Info("Getting prices from ethgasstation", c.ctx)
	res, err := c.httpClient.Get("https://ethgasstation.info/json/ethgasAPI.json")
	if err != nil {
		return nil, nil, err
	}

	egr := &EthGasStationResponse{}

	if err := json.Decode(res.Body, egr); err != nil {
		return nil, nil, err
	}

	log.Info("Gas Price is %v", egr, c.ctx)

	return big.NewInt(int64(egr.SafeLow + 1)), egr, nil
}

func (c Client) GetBalance(address string) (balanceInWei *big.Int, err error) {
	log.Info("Getting ethereum balance for address: %v", address)
	id := rand.Int64()
	jsonRpcCommand := fmt.Sprintf(JsonRpcMessage, JsonRpcVersion, "eth_getBalance", paramsToString(address), id)
	jrr, err := c.Post(jsonRpcCommand, id)
	if err != nil {
		return big.NewInt(0), err
	}

	balanceHex := string(jrr.Result)
	balanceHex = balanceHex[1 : len(balanceHex)-1]

	log.Info("Address balance is %v", balanceHex)

	a, err := hexutil.DecodeBig(balanceHex)
	if err != nil {
		return big.NewInt(0), err
	}
	return a, nil
}
