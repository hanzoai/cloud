package block

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/models/blockchains"
)

func init() { orm.Register[Block]("block") }

//
// Datastructure for Bitcoin Block
//

type BitcoinBlock struct {
	// BitcoinBlockHeight
	BitcoinBlockHeight int64  `json:"bitcoinBlockHeight"`
	BitcoinBlockHash   string `json:"bitcoinBlockHash"`
	// We don't care about this
	// BitcoinBlockConfirmations     int64  `json:"bitcoinBlockConfirmations"`
	BitcoinBlockStrippedSize      int64  `json:"bitcoinBlockStrippedSize"`
	BitcoinBlockSize              int64  `json:"bitcoinBlockSize"`
	BitcoinBlockWeight            int64  `json:"bitcoinBlockWeight"`
	BitcoinBlockVersion           int64  `json:"bitcoinBlockVersion"`
	BitcoinBlockVersionHex        string `json:"bitcoinBlockVersionHex"`
	BitcoinBlockMerkleroot        string `json:"bitcoinBlockMerkleroot"`
	BitcoinBlockTime              int64  `json:"bitcoinBlockTime"`
	BitcoinBlockMedianTime        int64  `json:"bitcoinBlockMedianTime"`
	BitcoinBlockNonce             int64  `json:"bitcoinBlockNonce"`
	BitcoinBlockBits              string `json:"bitcoinBlockBits"`
	BitcoinBlockDifficulty        int64  `json:"bitcoinBlockDifficulty"`
	BitcoinBlockChainwork         string `json:"bitcoinBlockChainwork"`
	BitcoinBlockPreviousBlockHash string `json:"bitcoinBlockPreviousBlockHash"`
}

//
// Datastructure for Ethereum Block
//
// We cannot use a named embedded struct because NodeJS client has to interact
// with this and for whatever reason, the js client cannot generate property
// names with '.' in them like the go client can.  You get an error:
//
// Error: property.name contains a path delimiter, and the entity contains one or more indexed entity value.
//

type EthereumBlock struct {
	EthereumBlockNumber           int64     `json:"ethereumBlockNumber"`
	EthereumBlockHash             string    `json:"ethereumBlockHash"`
	EthereumBlockParentHash       string    `json:"ethereumBlockParentHash"`
	EthereumBlockNonce            string    `json:"ethereumBlockNonce"`
	EthereumBlockSha3Uncles       string    `json:"ethereumBlockSha3Uncles"`
	EthereumBlockLogsBloom        string    `json:"ethereumBlockLogsBloom"`
	EthereumBlockTransactionsRoot string    `json:"ethereumBlockTransactionsRoot"`
	EthereumBlockStateRoot        string    `json:"ethereumBlockStateRoot"`
	EthereumBlockMiner            string    `json:"ethereumBlockMiner"`
	EthereumBlockDifficulty       BigNumber `json:"ethereumBlockDifficulty"`
	EthereumBlockTotalDifficulty  BigNumber `json:"ethereumBlockTotalDifficulty"`
	EthereumBlockExtraData        string    `json:"ethereumBlockExtraData"`
	EthereumBlockSize             int64     `json:"ethereumBlockSize"`
	EthereumBlockGasLimit         int64     `json:"ethereumBlockGasLimit"`
	EthereumBlockGasUsed          int64     `json:"ethereumBlockGasUsed"`
	EthereumBlockTimeStamp        int64     `json:"ethereumBlockTimestamp"`
	EthereumBlockUncles           []string  `json:"ethereumBlockUncles"`
}

//
// Datastructure combining all the different types of blockchain blocks
//

type Block struct {
	mixin.Model[Block]

	BitcoinBlock
	EthereumBlock

	Type Type `json:"type"`

	// Status        ProcessStatus `json:"status"`
	Confirmations int64 `json:"confirmations"`
}

func New(db *datastore.Datastore) *Block {
	b := new(Block)
	nsDb := datastore.New(db.Context)
	nsDb.SetNamespace(BlockchainNamespace)
	b.Init(nsDb)
	return b
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("block")
}
