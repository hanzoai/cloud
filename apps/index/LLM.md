# LLM.md — apps/index

Guidance for apps/index. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The index (`apps/index`, `/v1/index`)

The in-binary index, speaking the Meilisearch REST dialect so a Meilisearch client
repoints by changing one host. It replaced the standalone Meilisearch containers
(`chat-meilisearch`, `search-fts5`).

**THE TWO AXES ARE SEPARATE AND A MOVE ONLY TOUCHES ONE.** The ADDRESS decides
audience — `public.go` reads the path's first segment, so those two left the
customer contract by moving and needed no flag (openapi.yaml 1646 → 1644 paths).
The TAG is the OWNER (HIP-0139 §4, `openapi/compose.go:225`: `op.Tags = []string{op.App}`), so
they are still tagged `search` and `openapi/floor.json`'s `search: 3` is still
correct — apps/search still owns three operations. **Do not read a floor product
as a path prefix.** I misread it exactly that way, concluded the ratchet had
failed to fire on a 3→1 shrink, and was about to "fix" a correct design; the
counts agreed the whole time because they were never counting what I thought.

**`/v1/search` IS THE ONE ENTRY, and the backends keep their own.** A caller asks
`POST /v1/search` and does not choose a backend: `apps/search` fuses the three
retrieval stores the platform runs — the lexical index (`apps/index`), the vector
index (`apps/knowledge`) and the org's own repositories (`apps/code`) — by
Reciprocal Rank Fusion, and reports per leg whether it answered, is degraded, is
unprovisioned or was excluded by the mode. The other three addresses stay exactly
what they are, because each is a backend's own surface rather than an endpoint a caller
should have to pick between: `/v1/index` speaks the Meilisearch dialect so a
Meilisearch client repoints by changing one host, `/v1/code/search` answers spans
with repo and line because that is what a coding agent wants, `/v1/knowledge/search`
is the KB's own semantic read. **Fusing them is not deleting them.**

The modes name RETRIEVAL KINDS and not legs — `text` runs every lexical leg, which
is now the index AND the code corpus; `semantic` runs the vector one — so adding a
leg never adds a mode. `apps/code/query.go` is that leg's in-process client and is
the SAME shape `apps/index/query.go` publishes (`Ready()` + a query), in-process
for the two reasons index's own comment gives: a fused query is an agent tool call
on a few-hundred-millisecond budget, and a second network path to the same store
would be a second way to do one thing. A deployment without the code index reports
that leg `disabled`, which is not a fault and does not make the answer partial.

Tenancy is the point: a standalone Meilisearch has ONE
global keyspace behind a master key, so every consumer sharing an instance shares its
indexes — here the tenant is `principal.Org` and every query filters `WHERE org=?`,
so two orgs may both hold an index named `messages`. The credential is the org's
ordinary API key, because the JS client already sends `Authorization: Bearer`.

**The index is a term table, NOT FTS5, and must stay that way.** FTS5 is a
compile-time module and this binary links the SYSTEM SQLite so the SQLCipher codec is
real; that library ships `ENABLE_FTS3` + `HAS_CODEC` with no fts5, and the
`sqlite_fts5` build tag only affects the VENDORED amalgamation, so it is inert here.
An index built on FTS5 opens on a pure-Go build, passes its tests, and then cannot
create a single table in the shipped image. `terms` is keyed
`(org, uid, term, pk)` so a prefix query is an index range scan; it behaves the same
in every build lane. Verify any SQLite module against the production lane
(`-tags "libsqlite3 sqlite_fts5"` + `-lsqlcipher`) before designing on it.

**Fourteen of the seventeen operations are TYPED ops** (`apps/index/typed.go`), so
the search plane reaches a schema, an MCP tool, a CLI command and a generated SDK
method — it published seventeen operationIds and prose and nothing else. Typing
did not move the dialect: every model spells the map its handler assembled, in
ALPHABETICAL json-tag order, because encoding/json sorts a map's keys and a
Meilisearch client reads these bodies byte for byte
(`TestTypedAnswersAreByteIdenticalToTheMaps`). Nor did it move the REFUSALS,
which is the harder half: `{message, code, type, link}` is a wire contract its
client branches on, so an op returns a `*fault` and `envelope()` writes those
bytes back — the shape apps/goja's BundleErr and cloud's own `Denied` already
take. The three that stay untyped are the document writes: their body is a
top-level JSON ARRAY on a `:uid` route, and zip binds a path parameter by walking
struct fields, so a slice In binds no uid and a struct In cannot decode the array
— they declare both body shapes through `spec.Register` + `spec.OneOf`
instead. Paging keeps STRINGS for `limit`/`offset` because the handler defaults on
absent-or-unparseable and honours an explicit 0.

The store is the `index` subsystem, and a rename must carry the WHOLE family: cek
keeps the wrapped data key beside it as `<path>.dek`, so moving the `.db` alone
strands the key and every document becomes undecryptable — data loss that presents
as an empty index.
