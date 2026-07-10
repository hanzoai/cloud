package analytics

import (
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

var t bool
var f bool

func init() {
	t = true
	f = false
}

type Analytics struct {
	Integrations []Integration `json:"integrations,omitempty"`
}

func (a Analytics) UpdateStoredDisabledStatus() Analytics {
	for i, integration := range a.Integrations {
		if integration.Disabled_ == nil {
			a.Integrations[i].Disabled = false
			continue
		}

		a.Integrations[i].Disabled = *integration.Disabled_
	}

	return a
}

func (a Analytics) UpdateShownDisabledStatus() Analytics {
	for i, integration := range a.Integrations {
		if integration.Disabled {
			a.Integrations[i].Disabled_ = &t
		} else {
			a.Integrations[i].Disabled_ = &f
		}
	}

	return a
}

// Only JSON encode enabled integrations
func (a Analytics) SnippetJSON() []byte {
	enabled := Analytics{}
	for _, integration := range a.Integrations {
		if !integration.Disabled {
			integration.Disabled_ = nil
			enabled.Integrations = append(enabled.Integrations, integration)
		}
	}

	return json.EncodeBytes(enabled)
}

type Integration struct {
	Name      string `json:"-"`
	Disabled  bool   `json:"-"`
	Disabled_ *bool  `json:"disabled,omitempty" datastore:"-"`

	// Common to all integrations
	Type          string `json:"type"`
	Event         string `json:"event,omitempty"`
	Id            string `json:"id,omitempty"`
	IntegrationId string `json:"-"`

	// Sampling percentage
	Sampling float64 `json:"sampling,omitempty"`

	// Available integrations
	Custom
	FacebookConversions
	FacebookPixel
	GoogleAdWords
	GoogleAnalytics
}

// Override value for a given event
type Value struct {
	Percent float64        `json:"percent,omitempty"`
	Value   currency.Cents `json:"value,omitempty"`
}

// Event specific value overrides
type Values struct {
	Currency         currency.Type `json:"currency,omitempty"`
	ViewedProduct    Value         `json:"viewedProduct,omitempty"`
	AddedProduct     Value         `json:"addedProduct,omitempty"`
	InitiateCheckout Value         `json:"initiateCheckout,omitempty"`
	AddPaymentInfo   Value         `json:"addPaymentInfo,omitempty"`
}

// Integration specific properties
type Custom struct {
	Code string `json:"code,omitempty"`
}

type FacebookPixel struct {
	Values Values `json:"values,omitempty"`
}

type FacebookConversions struct {
	Value    string `json:"value,omitempty"`
	Currency string `json:"currency,omitempty"`
}

type GoogleAnalytics struct {
}

type GoogleAdWords struct {
}

type Heap struct {
}

type Sentry struct {
}
