// Package referrals is referral ATTRIBUTION: who referred whom, and whether that
// referee ever became a real customer.
//
// Every org has a stable code and share link, a new org claims it at signup, and
// the edge advances signup → qualified once the referee actually makes metered
// spend. That attribution record IS this package's product.
//
// IT MOVES NO MONEY. There is no bonus, no grant, no deposit and no ledger write
// here. Credit enters an org by exactly two paths — a manual per-org admin grant
// against the auditable ledger, and an invite that carries credit to a new org —
// and this package is neither. A referral REWARD is an affiliate PAYABLE: it is
// tracked in hanzoai/commerce and settled by wire or to a connected wallet, never
// minted as platform credit.
//
// The loop, end to end:
//
//  1. Every org has a STABLE referral code (deriveCode: deterministic base32 of a
//     hash of the org id) and a link https://<brand>/?ref=<code>.
//  2. A new org signs up via a link → the console posts POST /v1/referral/claim
//     with the code → we record referrer↔referee at status signup. Self-referral
//     is blocked; one referral per referee ever (idempotent).
//  3. The referee QUALIFIES once they have made metered spend — the honest signal
//     that they used the product rather than merely signed up. Qualification is a
//     WRITE, so it happens on the admin sweep (POST /v1/admin/referral/sweep, the
//     cron path) and NOWHERE else. GET /v1/referral is a pure read: it reports
//     the attribution, it never advances it.
//
// Surface:
//
//	GET  /v1/referral                 (org)          my code, link, my referrals
//	POST /v1/referral/claim           (org=referee)  record a referral from a ?ref code
//	GET  /v1/admin/referral/bonuses   (SuperAdmin) every referral edge + a summary
//	POST /v1/admin/referral/sweep     (SuperAdmin) qualify-check every pending referral
//
// ONE OWNER FOR THE ADMIN PREFIX. The cross-tenant referral ANALYTICS board (top
// referrers, conversion) reads apps/affiliates' OWN tables, so it answers at
// GET /v1/admin/affiliate/referrals — the second segment names the capability
// that serves it (HIP-0139 §3.2). What is left under /v1/admin/referral is this
// package's alone: the edge directory and the qualify sweep.
//
// serve.go auto-registers GET /v1/referral/health.
package referral
