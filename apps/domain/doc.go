// Package domain is Hanzo Domains: search a name, see the price, buy it from
// your prepaid wallet.
//
// This is the REGISTRATION product. The price carries Hanzo's markup, the
// purchase is billed through the customer's prepaid wallet, and the domain is
// born pointing at Hanzo's own authoritative nameservers.
//
// This is DISTINCT from Hanzo DNS (hanzoai/dns): DNS manages records for a domain you
// already control; Domains ACQUIRES the domain. After a purchase, Domains hands the
// new zone to hanzoai/dns and points the registrar's nameservers at it — the two
// products compose (buy here, manage records there).
//
// Wholesale is resold from a registrar behind the Registrar interface, which names no
// vendor: the vocabulary it is asked in and the registry of registrars that answer are
// registrar.go, and each registrar is one file (namecom.go is the worked example). The
// core here is transport-free: it orchestrates availability → price → authorize →
// register → provision-zone → capture → record over four interfaces (Registrar,
// Biller, Zones, Store), so the policy is unit-testable with no HTTP/registrar/billing
// backend. mount.go is the thin cloud adapter that binds the real backends and exposes
// /v1/domain/*.
package domain
