// Package tax is how companies formed on Hanzo exchange W-9s, W-8s and 1099s
// with each other, from the payments that moved between them on Hanzo's rails.
//
// Every company is both sides. As a PAYEE it keeps one tax profile — its W-9, or
// its W-8BEN or W-8BEN-E when it is a foreign person — and decides which payers
// may read it; it receives the Copy B statements
// furnished to it in its inbox. As a PAYER it asks a counterparty for its W-9,
// reads the payments it made — every rail's, as the event plane records them —
// with each one classified by the rule that decides it, prepares 1099-NEC and
// 1099-MISC drafts for every payee over the year's threshold, reviews and
// furnishes them, corrects a furnished one by superseding it, and exports the
// year's return for the IRS.
//
// It serves /v1/tax. What it does NOT do is part of the product and is said on
// every operation it applies to: it does not file with the IRS (filing through
// IRIS needs the payer's own Transmitter Control Code, and is an owner step
// recorded here, never performed), it does not run IRS TIN Matching (that needs
// IRS e-Services enrollment; the payer records the result), and it does not mail
// paper (a statement owed on paper is marked owed, and the payer records the
// mailing). Nothing here reports a return filed, a TIN matched, or a statement
// mailed that a person did not report.
//
// TENANCY. Every op acts for the caller's own org, from the validated IAM claim.
// Reading another org's W-9 requires a live grant from that org; the grant lives
// in the payee's own file and only its admin can give or revoke it. An org admin
// writes the profile, grants W-9s and moves 1099s; a member may read the org's
// own profile and the statements it received. SuperAdmin (owner == "admin") is
// admitted wherever an org admin is, is recorded as such on the trail, and is
// NOT admitted to any answer that carries a payee's TIN in full — the TIN read,
// the IRS export and its re-read — nor to signing an org's W-9 certification:
// those are the org's own admin's alone.
//
// ANOTHER ORG'S FILE IS READ AND WRITTEN ONLY WHERE IT IS CURRENT: on the replica
// that is its elected writer (see other). A grant is decided in the payee's file,
// and a copy of that file that has not seen the payee's revocation must not answer
// the payer.
package tax
