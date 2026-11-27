# LLM.md — apps/reference

Guidance for apps/reference. Moved from the root LLM.md; cross-cutting rules stay in the root.

## /v1/risk/reference — the reference plane: two stores, one precedence, versions on the wire

`apps/reference` is the lookup data a risk decision needs but cannot derive:
disposable email domains, hosting and Tor address ranges, crawler user-agent
patterns, delegated autonomous system numbers, card-scheme prefixes, browsers
the fleet sees everywhere, and how current the designation lists the screening
engine holds actually are. It is a row of its own in `manifest/apps.go` (one
prefix, `/v1/risk/reference`), preceding `risk`'s bare `/v1/risk`, so
longest-prefix separates them and neither row moves. It addresses under the risk
product for the same reason `label` does: `spec.Product` reads the product off
the first `/v1` segment, and these six operations belong to the risk product and
not to the KServe model-SERVING product on `/v1/ml`. Six operations, all typed.

**The unit of version and freshness is the SOURCE, not the set.** A set is the
union of its publishers, and each carries its own version, its own as-of and its
own failure. `net` draws on eleven publishers; a set-wide version would make one
publisher's outage either block every other publisher's update or silently
shrink the set. Per source, a publisher that stops answering ages out visibly on
its own row. The set's version is the composition (`aws@<digest>+tor@<digest>+…`),
so a decision records one string an auditor resolves back to a publisher, a
licence and a date, and the set's as-of is the OLDEST contributing publisher —
reporting the newest would let one daily source hide three that died.

**A version IS the content digest, taken over the sorted entries and not over the
bytes.** That is what makes ingest idempotent: the same set is the same version,
so re-taking an unchanged publisher writes zero rows and says `unchanged`, while
a publisher who reorders their file has not minted anything. Resumability is a
`landed` cursor on the manifest, and it is an OPTIMISATION only — the primary key
is `(set, source, version, key)` on a ReplacingMergeTree, so re-writing a chunk
that already landed produces rows the merge deduplicates.

**Two planes, two STORES, and that is the isolation argument.** The baseline
lives in `hanzo.reference_source` + `hanzo.reference_entry`, whose DDL has NO
tenant column — there is nowhere in the shape for an organisation to go, so the
cross-tenant write is unrepresentable rather than refused, and no In struct
carries a scope, org or tenant field either. A tenant's own allow/deny entries
live in that org's own SQLite file via `cloud.OrgNamespace`, which is the same
physical isolation every other per-entity store has. Resolution is override
first, baseline second, first hit wins — and BOTH go through one `candidates`
function, so a deny on `tempbox.example` covers `mail.tempbox.example` in the
tenant's sense exactly as it does in the baseline's.

**What may enter the baseline: published data under terms we hold, and
aggregates no single org could produce.** Every source states its licence
(`Source.Terms`) and it is on the wire. The one derived set (`device`) publishes
only browser identities seen under at least `Orgs`=25 organisations with
`Rows`=1000 observations — `Publishable`, enforced in the statement's WHERE and
again on read — keyed by a digest rather than the identifier, with the count
BANDED. `TestDeriveRefusesWhatOneOrgProduced` is the proof: one organisation
producing a million observations yields an EMPTY baseline.

**A source we may not redistribute is a declared CLIENT, not an omission.**
`pep`, `issuer` and `reputation` are in the catalog with the licence we do not
hold as their refusal, and every lookup against them refuses. An absent set and
an unlicensed one look identical from outside and only one of them is a decision.
