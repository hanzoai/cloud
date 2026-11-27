// Package link is the unified AI login manager's registry: the org+user-scoped
// record of WHICH provider accounts (Claude Max, ChatGPT Plus, a Hanzo API key,
// a raw provider key) a developer has signed into, ON WHICH MACHINES, with each
// account's latest usage snapshot. It is the cross-machine view console renders
// as "AI Providers / Accounts" and the source the redundancy route policy reads.
//
// THE ATOM is a Link: one (user, device, provider, account) binding plus its
// kind, status, last-seen, and the latest usage snapshot. A device is the
// (machine, host, os) projection shared by a machine's Links — not a separate
// stored entity, so there is no device/link join and no orphan-device GC. A
// user's accounts across every machine are just that user's Links.
//
// NO SECRET LIVES HERE. The provider's OAuth token / API key stays device-local
// (exactly as @hanzo/usage keeps it, reading each provider's own login to meter
// usage); the registry holds LINK METADATA + usage snapshots only. The collector
// (@hanzo/usage's reporter) registers a Link and pushes its usage using the SAME
// IAM bearer it already carries — never a provider secret.
//
// BILLING FOLLOWS THE ACCOUNT, not the registry. This store never touches
// commerce — it holds no metering client, so it is structurally incapable of
// creating a charge. A subscription account's inference bills the user's monthly
// plan (metered here for visibility only); an api-key / hanzo account's inference
// bills via commerce on the existing gateway path, unchanged. Each Link carries
// the mode that says which (BillingMode), so a usage event's billing is explicit.
//
// ISOLATION: org is the tenant key and user is the owner key; every read and
// write leads with `org=? AND user=?` bound predicates, so a caller sees and
// mutates only their OWN accounts within their OWN org — fail-closed.
package link
