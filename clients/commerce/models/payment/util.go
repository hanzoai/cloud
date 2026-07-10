package payment

import "github.com/hanzoai/cloud/clients/commerce/models/types/accounts"

// Is the payment processor type handle fiat
func IsFiatProcessorType(typ accounts.Type) bool {
	switch typ {
	case accounts.BitcoinType:
		return false
	case accounts.EthereumType:
		return false
	default:
		return true
	}
}
