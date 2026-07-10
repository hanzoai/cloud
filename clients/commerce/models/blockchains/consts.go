package blockchains

// Type is the blockchain identifier
type Type string
type BigNumber string

const (
	// Ethereum Blockchain
	EthereumType Type = "ethereum"

	// Ethereum Morden Testnet
	EthereumMordenType Type = "ethereum-morden"

	// Ethereum Default Testnet
	EthereumRopstenType Type = "ethereum-ropsten"

	// Bitcoin Blockchain
	BitcoinType Type = "bitcoin"

	// Bitcoin Testnet
	BitcoinTestnetType Type = "bitcoin-testnet"
)

type BitcoinTransactionType string

const (
	BitcoinTransactionTypeVIn  BitcoinTransactionType = "vin"
	BitcoinTransactionTypeVOut BitcoinTransactionType = "vout"
)

type ProcessStatus string

const (
	QueuedProcessStatus    ProcessStatus = "queued"
	ReadingProcessStatus   ProcessStatus = "reading"
	PendingProcessStatus   ProcessStatus = "pending"
	ConfirmedProcessStatus ProcessStatus = "confirmed"
)

type Usage string

const (
	SenderUsage   Usage = "sender"
	ReceiverUsage Usage = "receiver"
)

var BlockchainNamespace = "_blockchains"
