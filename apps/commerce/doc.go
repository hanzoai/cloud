// Package commerce is selling: checkout, subscriptions, invoices, spend alerts,
// payment webhooks and the storefront catalog.
//
// It is the merchant half, embedded from hanzoai/commerce and mounted on cloud's
// own router. It is not the wallet — EmbedConfig.Ledger injects apps/finance, so a
// credit minted here lands in the one ledger of record.
//
// This file mounts that MODULE into a cloud binary (HIP-0106) via the NATIVE
// co-residence contract: commerce registers its routes
// directly on the HOST's zip app (EmbedConfig.App) — one router, one specificity
// space, zero handler adaptation. This adapter narrows cloud.Deps, boots the
// embed, and wires the in-process clients. Direction is one-way: cloud → commerce.
//
// PCI SCOPE. Commerce is a LIGHT ROUTER, NOT in PCI-DSS scope: tokens + intent IDs
// only, NEVER a PAN. PAN-touching paths call the out-of-process Payments / Vault
// (ZAP-RPC); when those clients are absent the payment handlers fail closed while
// tenant config + admin stay served — Mount warns loudly at startup.
//
// FAIL-SOFT. A broken Embed does NOT crash the binary: commerce degrades to a 503
// on its own prefixes while every co-resident subsystem stays up.
package commerce
