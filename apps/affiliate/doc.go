// Package affiliates is a partner program that pays commission on what your
// referrals spend.
//
// Partners apply, get approved with a commission rate and a share link, and
// earn an ONGOING COMMISSION on the metered spend of every customer they refer,
// accrued per period and paid out in credits or cash.
//
// It is one of THREE programs in this repo built on the same shape — apply/connect,
// approve, attribute, accrue at-most-once per (party, counterparty, period), pay
// out against pending = accrued − paid. apps/referrals is the one-time bonus for
// both sides; apps/authors is the royalty for OSS authors on deploy spend. All
// three share the commerce ledger path (a credits payout is a grant, tag
// grant:affiliate) and each carries a byte-identical copy of commerce.go over
// apps/payout.
//
// The loop, end to end:
//
//  1. An org APPLIES to be an affiliate (POST /v1/affiliate/apply), optionally
//     requesting a vanity code. Staff APPROVE it (POST /v1/admin/affiliate/:id/
//     approve), which mints the code (vanity if free, else a derived slug) and sets
//     a commission rate (default 15%). The affiliate now has a link
//     https://<brand>/?aff=<code>.
//  2. A new org signs up via the link → the console posts POST /v1/affiliate/
//     attribute with the code → we record referred_org↔affiliate (first-touch,
//     one per referred org, self-attribution blocked).
//  3. The ACCRUAL SWEEP (POST /v1/admin/affiliate/sweep, SuperAdmin; also lazy
//     on the affiliate's own dashboard read) folds over each affiliate's referred
//     orgs: commission = the referred org's metered spend THIS PERIOD × the rate,
//     accrued into the affiliate's balance as an affiliate_event. The accrual is
//     LATCHED at-most-once per (affiliate, referred_org, period) — a re-run in the
//     same period never double-accrues, mirroring the referral credit latch.
//  4. Staff RECORD a payout of accrued commission (POST /v1/admin/affiliate/:id/payout,
//     record-only — a human settles it):
//     a "credits" method issues a commerce grant into the affiliate's wallet; cash
//     methods (wire/paypal/…) are record-only. A payout can never exceed pending
//     (accrued − paid), guarded atomically.
//
// Surface:
//
//	GET  /v1/affiliate                        (org)          my status, code, link, referred count, accrued/pending/paid, payouts
//	POST /v1/affiliate/apply                  (org)          apply to the program (optional vanity code)
//	POST /v1/affiliate/attribute              (org=referred) record attribution from an ?aff code
//	GET  /v1/admin/affiliate                  (SuperAdmin) every affiliate + a summary
//	POST /v1/admin/affiliate/:id/approve      (SuperAdmin) approve + mint the code
//	POST /v1/admin/affiliate/:id/suspend      (SuperAdmin) suspend
//	POST /v1/admin/affiliate/:id/decline      (SuperAdmin) turn an application down
//	POST /v1/admin/affiliate/:id/rate         (SuperAdmin) set the direct commission rate
//	POST /v1/admin/affiliate/:id/note         (SuperAdmin) set the staff's note on the deal
//	POST /v1/admin/affiliate/:id/payout       (SuperAdmin) RECORD a payout (record-only; a human settles it)
//	POST /v1/admin/affiliate/sweep            (SuperAdmin) accrue commission for every referred org this period
//	GET  /v1/admin/affiliate/referrals        (SuperAdmin) cross-tenant referral board over this app's attribution spine
//
// serve.go auto-registers GET /v1/affiliate/health.
package affiliate
