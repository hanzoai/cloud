# LLM.md — apps/affiliate

Guidance for apps/affiliate. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The affiliate's standard rate, declines and the staff's deal note (`apps/affiliate`)

A new application is born at **15%** (`defaultRateBps` 1500, the owner's standard rate; it was 20%).
Existing rows keep the rate they hold; `POST /v1/admin/affiliate/:id/rate` moves one. **What the
rate is a share OF is the owner's open question**: today it is a share of Hanzo's margin on a
referred org's spend (`defaultMarginBps` 4000, the `affiliate_margin_bps` switch), so 15% of margin
is 6% of revenue, where the owner's public line says 15% of revenue. The base is left as it was.

`POST /v1/admin/affiliate/:id/decline` turns an application down (status `declined`, no code, no
accrual; `409` unless it is `applied`, since an approved affiliate is suspended; approving a
declined application reverses it). `POST /v1/admin/affiliate/:id/note {note}` sets the staff's note
on the deal (at most 500 characters, empty clears, control characters refused). Both are
SuperAdmin only and write to the audit trail (`affiliate.decline`, `affiliate.note`) with the
actor (`by`) and, for a note, the note. The note is on the admin directory rows only (`note`,
`declinedAt`): no partner-facing read carries it, and a declined applicant reads `declined` on its
own `GET /v1/affiliate`. `apps/affiliate/affiliates_test.go` holds the proofs.
