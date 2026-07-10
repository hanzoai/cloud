package blocktransaction

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/models/blockchains"
)

func init() { orm.Register[BlockTransaction]("blocktransaction") }

// Datastructure for Bitcoin Transaction
type BitcoinTransaction struct {
	BitcoinTransactionBlockHash   string `json:"bitcoinTransactionBlockHash"`
	BitcoinTransactionBlockHeight int64  `json:"bitcoinTransactionBlockHeight"`

	BitcoinTransactionTxId          string                 `json:"bitcoinTransactionTxId"`
	BitcoinTransactionHash          string                 `json:"bitcoinTransactionHash"`
	BitcoinTransactionVersion       int64                  `json:"bitcoinTransactionVersion"`
	BitcoinTransactionSize          int64                  `json:"bitcoinTransactionSize"`
	BitcoinTransactionVSize         int64                  `json:"bitcoinTransactionVSize"`
	BitcoinTransactionLocktime      int64                  `json:"bitcoinTransactionLocktime"`
	BitcoinTransactionHex           string                 `json:"bitcoinTransactionHex"`
	BitcoinTransactionConfirmations int64                  `json:"bitcoinTransactionConfirmations"`
	BitcoinTransactionTime          int64                  `json:"bitcoinTransactionTime"`
	BitcoinTransactionBlockTime     int64                  `json:"bitcoinTransactionBlockTime"`
	BitcoinTransactionType          BitcoinTransactionType `json:"bitcoinTransactionType"`
	BitcoinTransactionUsed          bool                   `json:"bitcoinTransactionUsed"`
}

type BitcoinVIn struct {
	BitcoinTransactionVInTransactionTxId  string `json:"bitcoinTransactionVInTransactionTxId"`
	BitcoinTransactionVInTransactionIndex int64  `json:"bitcoinTransactionVInTransactionIndex"`
	BitcoinTransactionVInIndex            int64  `json:"bitcoinTransactionVInIndex"`
	BitcoinTransactionVInValue            int64  `json:"bitcoinTransactionVInValue"`
}

type BitcoinVOut struct {
	BitcoinTransactionVOutIndex int64 `json:"bitcoinTransactionVOutIndex"`
	BitcoinTransactionVOutValue int64 `json:"bitcoinTransactionVOutValue"`
}

// Datastructure for Ethereum Transaction
type EthereumTransaction struct {
	EthereumTransactionBlockHash   string `json:"ethereumTransactionBlockHash"`
	EthereumTransactionBlockNumber int64  `json:"ethereumTransactionBlockNumber"`

	EthereumTransactionHash             string    `json:"ethereumTransactionHash"`
	EthereumTransactionNonce            int64     `json:"ethereumTransactionNonce"`
	EthereumTransactionTransactionIndex int64     `json:"ethereumTransactionTransactionIndex"`
	EthereumTransactionFrom             string    `json:"ethereumTransactionFrom"`
	EthereumTransactionTo               string    `json:"ethereumTransactionTo"`
	EthereumTransactionValue            BigNumber `json:"ethereumTransactionValue"`
	EthereumTransactionGasPrice         BigNumber `json:"ethereumTransactionGasPrice"`
	EthereumTransactionGas              BigNumber `json:"ethereumTransactionGas"`
	EthereumTransactionInput            string    `json:"ethereumTransactionInput"`
}

// Datastructure for Bitcoin Transaction Receipt
type BitcoinTransactionReceipt struct {
}

// Figure out how to support this later
// Datastructure for Ethereum Transaction Logs
// type EthereumTransactionLog struct {
// 	LogIndex         int64  `json:"logIndex"`
// 	BlockHash        string `json:"blockHash"`
// 	BlockNumber      int64  `json:"blockNumber"`
// 	TransactionHash  string `json:"transactionHash"`
// 	TransactionIndex int64  `json:"transactionIndex"`
// 	Address          string `json:"address"`
// 	Data             string `json:"data"`
// 	Topics           string `json:"topics"`
// }

// Datastructure for Ethereum Transaction Receipt
type EthereumTransactionReceipt struct {
	EthereumTransactionReceiptBlockHash         string `json:"ethereumTransactionReceiptBlockHash"`
	EthereumTransactionReceiptBlockNumber       int64  `json:"ethereumTransactionReceiptBlockNumber"`
	EthereumTransactionReceiptTransactionHash   string `json:"ethereumTransactionReceiptTransactionHash"`
	EthereumTransactionReceiptTransactionIndex  int64  `json:"ethereumTransactionReceiptTransactionIndex"`
	EthereumTransactionReceiptFrom              string `json:"ethereumTransactionReceiptFrom"`
	EthereumTransactionReceiptTo                string `json:"ethereumTransactionReceiptTo"`
	EthereumTransactionReceiptCumulativeGasUsed int64  `json:"ethereumTransactionReceiptCumulativeGasUsed"`
	EthereumTransactionReceiptGasUsed           int64  `json:"ethereumTransactionReceiptGasUsed"`
	EthereumTransactionReceiptContractAddress   string `json:"ethereumTransactionReceiptContractAddress"`
	// Logs              []EthereumTransactionLog `json:"logs,omitempty"`
}

// Datastructure combining all the different types of transactions
type BlockTransaction struct {
	mixin.Model[BlockTransaction]

	Address string `json:"address"`

	BitcoinTransaction
	BitcoinVIn
	BitcoinVOut
	EthereumTransaction
	EthereumTransactionReceipt

	Type          Type          `json:"type"`
	Status        ProcessStatus `json:"status"`
	Usage         Usage         `json:"usage"`
	Confirmations int64         `json:"confirmations"`
}

func New(db *datastore.Datastore) *BlockTransaction {
	b := new(BlockTransaction)
	nsDb := datastore.New(db.Context)
	nsDb.SetNamespace(BlockchainNamespace)
	b.Init(nsDb)
	return b
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("blocktransaction")
}
