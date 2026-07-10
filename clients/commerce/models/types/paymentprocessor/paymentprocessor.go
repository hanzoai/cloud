package paymentprocessors

import "github.com/hanzoai/cloud/clients/commerce/models/types/currency"

type Processor string

const (
	Stripe Processor = "stripe"
	Amazon           = "amazon"
)

var ProcessorCountryCurrency map[string](map[string]Processor)

func init() {
	ProcessorCountryCurrency := make(map[Processor](map[string][]currency.Type))

	ProcessorCountryCurrency[Stripe] = map[string][]currency.Type{
		"usa": []currency.Type{},
		"can": []currency.Type{
			currency.USD,
			currency.CAD,
		},
	}

	ProcessorCountryCurrency[Amazon] = map[string][]currency.Type{
		"usa": []currency.Type{
			currency.USD,
		},
	}
}
