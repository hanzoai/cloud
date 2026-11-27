// Package x402 is pay-per-request over HTTP 402: quote a price, take the payment,
// serve the resource. It speaks x402 PROTOCOL VERSION 2 (the CAIP-2 / PAYMENT-*
// header generation), as specified by
// github.com/x402-foundation/x402/specs/x402-specification-v2.md and its HTTP
// transport binding in specs/transports-v2/http.md.
//
// The full cycle is challenge → the client pays → payload submitted → verify →
// serve → settle, native to the Hanzo cloud binary.
//
// FLOW. A priced resource answers 402 with a PaymentRequired carrying one or more
// PaymentRequirements (what to pay, in what asset, on what network, to whom). The
// client signs an EIP-3009 transferWithAuthorization over exactly those terms and
// retries with the PAYMENT-SIGNATURE header. The subsystem VERIFIES the EIP-712
// signature (secp256k1 recovery via luxfi/crypto — the SAME primitive the wallets
// custody signs with), rejects a REPLAYED authorization (nonce dedup), SETTLES
// exactly once (payer debit through the metering spine so paid usage appears in
// billing/usage like any metered spend, plus a credit to the recipient wallet's
// ledger), and serves — answering with a SettlementResponse on PAYMENT-RESPONSE.
//
// WHAT IS ON THE WIRE IS THE SPEC'S, NOT OURS. Every field name, header name and
// error reason in this file is the one the specification prints, because the wire
// is a contract with other people's clients: an @x402/fetch or x402[httpx] client
// that has never heard of Hanzo must be able to pay us, and our private nouns
// would make that impossible. Internal Go names stay idiomatic only where they do
// not leak (Terms, Settlement, gate).
//
// METERED FEES. The platform's own flat fees — one function invoke, one agent run,
// one provisioned resource — are priced here too, from each route's own fee knob
// and paid into the platform's wallet (metered.go). The route checks a payment
// before the act and settles it once the act is done (sdk/x402fee.go); one payment
// buys one act. GET /v1/x402/prices publishes the table.
//
// CLIENTS. The marketplace registry (another subsystem) owns the mapping
// resource→Terms (price + recipient wallet); x402 only enforces it (Registry +
// Publish). Recipient resolution rides the wallets subsystem
// (wallet.ResolvePaymentTarget). On-chain broadcast of the authorization is a
// Settler client; the LIVE default is ledger settlement.
package x402
