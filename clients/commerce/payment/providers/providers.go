// Package providers is a convenience package that imports all payment provider
// packages, ensuring they are available for the processor registry. Import this
// package with a blank identifier in your main application to register all
// providers:
//
//	import _ "github.com/hanzoai/cloud/clients/commerce/payment/providers"
package providers

import (
	// Import all provider packages so their init() functions register
	// with the global processor registry if they use init-based registration.
	// For providers that require configuration, use their NewProcessor()
	// constructors directly.
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/adyen"
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/bitpay"
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/braintree"
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/circle"
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/coinbase_commerce"
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/lemonsqueezy"
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/moonpay"
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/opennode"
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/paypal"
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/recurly"
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/solanapay"
	// Unified Square provider — follows the braintree/ shape so BD can
	// resolve it via processor.Get(processor.Square) and Configure()
	// per-request from payment_providers creds. This init() wins the
	// registry race over thirdparty/square's env-var init() because it
	// is imported after.
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/square"
	_ "github.com/hanzoai/cloud/clients/commerce/payment/providers/stripe"
	_ "github.com/hanzoai/cloud/clients/commerce/thirdparty/mpc"
	_ "github.com/hanzoai/cloud/clients/commerce/thirdparty/wire"
)
