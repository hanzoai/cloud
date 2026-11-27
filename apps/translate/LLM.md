# apps/translate — POST /v1/translate (HIP-0516)

Text in, the same text out in the language asked for. One endpoint, two tiers,
one memory per org. Three operations: `POST /v1/translate` (raw, its bodies
declared through `spec.Register` as `Brief` → `Delivery`), and the typed
review lane `GET`/`PUT /v1/translate/memory`.

| file | what it holds |
|---|---|
| translate.go | the route, the wire types, the handler, request parsing |
| engine.go | the two engines: quality (model plane) and bulk (MADLAD backend) |
| keep.go | a string's shape (ICU reader, tags, URLs, printf) and `kept` |
| hangup.go | the request context that ends when the caller's socket closes |
| memory.go | the per-org memory: key, migration, recall, the review ladder |

## Tiers

- **quality** (default) asks the model plane (`deps.AI`, metered there: the
  plane bills its own tokens, nothing is charged here). `model` names the model;
  empty sends `aimodel.DefaultModel` (enso-auto).
- **bulk** posts to the MADLAD-400 backend at `TRANSLATE_BULK_URL`; unset ⇒ 503
  for that tier. Never falls back to quality. Gated and debited HERE through
  `s.Bill` (the same `cloud.Meter` and commerce ledger every metered surface
  uses): provider/service `translate`, model `bulk`, priced per 1000 source
  characters of the strings that came back whole (meter authority row
  `translate/bulk-chars`, floor 20 µUSD). The gate reserves on everything sent;
  the debit lands after the reply and skips failed strings, and a call where
  every string failed (502) is billed nothing. `model` with `tier: bulk` is 400.

## Pointing quality at an org's own model

Send `model: "<org alias>"`. It goes through the SAME metered plane as the
default; the plane (hanzoai/ai) decides whether the org may use it, as for
chat — there is no second check here. The name is validated
(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}$`) and passed through `aimodel.ZenModel`,
which rewrites any name containing an upstream family word (qwen, glm, kimi,
deepseek, minimax) to enso-auto. So a model on an org's linked GPU must be
named by an alias that carries none of those words. `Delivery.model` names
the model the call runs on.

## The memory key

`(source text, declared source language, target, glossary version, tier,
format, model)`, sha256 over NUL-separated parts (`MemoryEntry.key`).

- model is the NAMED model, `""` for the default — so changing
  `aimodel.DefaultModel` does not orphan every default-model entry (and its
  approvals). Naming the default (or an upstream family, which ZenModel makes
  the default) keys as `""` too.
- Source language: `recall` falls back from a declared language to `""`, never
  the reverse. A string translated with its language detected (and every row
  made before the key carried the language) serves a caller that starts
  declaring `source`; a `de` row never answers `en`.
- Review writes (`PUT /memory`) must name the same `lang`, `format`, `model`
  the translate calls use, or they land on a different entry.
- New rows store `request_id` (the translate call's X-Request-Id).

### Migration (memory.go `migrate`, `rekey`)

Runs on every open. Adds `lang`, `format` (nullable), `model`, `request_id`
when missing, then moves every row with `format IS NULL` (partial index
`ix_entry_unmoved`) to `lang ""`, `model ""`, under ALL THREE formats — the
format an old row was made under was never recorded, so reviewed html or
markdown work stays found. `move` never undoes newer work: a machine row is
replaced by human work or newer machine work; a human row only by human work at
least as high on the ladder and at least as new. The old row is deleted,
`rekeyBatch` (500) rows per transaction. Rows
an older binary writes mid-rollout carry `format NULL` and move on the next
open. Tests: `TestAnOldMemoryMovesToTheNewKey`, `TestOldFormatsKeepTheirReviews`.

## Batch safety (quality)

- `chunks`: strings packed in order into parts of ≤ 8 KiB (`chunkBudget`), each
  string weighing `len + 32`; a larger string is its own part, never split.
- `ceiling`: `MaxTokens = 4096 (thinking) + 2 × part weight`, capped at 32768;
  the model API lowers it to the model's own max (`clampMaxTokens`). It also
  bounds the prepaid hold.
- A part whose reply is unusable — finish other than `stop` (empty is let
  through to `decode`), not the envelope, wrong count — is asked again ONE
  STRING AT A TIME (a merged or split string fails every neighbour in a shared
  reply). Strings that fail `kept` in a usable reply are asked once more together.
- `kept` (keep.go) compares SHAPES, as sets, each read in one pass (braces
  are paired once; nesting past 32 is one token; a shape holds ≤ 1024 tokens):
  `{placeholders}` and `{{template}}` names; printf conversions — unnumbered
  ones as one ORDERED sequence, `%1$s`/`%(name)s` by themselves, `%%` ignored;
  ICU arguments — name, type, a select's keys, a plural's `=N` and offset, `#`,
  the arguments EVERY branch holds, and whether it has `other` and only CLDR
  categories — while branch WORDS are translatable and categories may differ by
  language; tags read the way a browser reads them (quoted values hold `<`/`>`,
  an unclosed tag runs to the end), with EVERY attribute and its value (entities
  decoded, whitespace and control characters dropped), except `alt`, `title`,
  `aria-label`, `placeholder`, whose values are words; markdown link and image
  targets; URLs. Lost or added either way fails, so an added handler, a moved
  href/action or a `javascript:` target fails unless the source has the same.
  Apostrophes are text, not ICU quotes (`l'{app}`).
- A string still failing comes back with `error` naming the CLASS ("the
  translation lost a URL"), never the token, no text, not stored; `usage.failed`
  counts it.
- Bulk replies are checked with `kept` too (no retry).

## Bounds

- 512 strings, 32768 per string (400), 262144 characters in all (`maxTotal`,
  413 "split it"). maxTotal keeps a call to a few dozen parts. A review page
  holds at most 1000 rows.
- At most `2 × parts` model calls per request, one-string retries included,
  and a part's retries never take a later part's first ask; past it strings
  fail with `whyBudget`.
- The call stops after two parts in a row in which no string came back
  (`whyStopped`).
- The engine runs under `deadline` (90 s) and `hangup`: the socket is peeked
  (`MSG_PEEK|MSG_DONTWAIT`, never read) every 250 ms and the context ends when
  the peer closes. fasthttp ends no request context on disconnect. A connection
  that is not a socket (app.Test) is not watched; behind a proxy it sees the
  proxy's connection. The memory is written on the request's own context, so
  what finished before either is kept.
- Parts run one after another, so a call near maxTotal on a slow model ends at
  the deadline with the rest marked `whyTime`; sending it again continues from
  the memory.
- No string returned, or the engine failed: 502, code `translate_failed`, a
  fixed message. Nothing from the caller's text reaches the body, the log line
  or a span.

## Observability contract

- One span `translate` per request: `hanzo.feature=translate`, `hanzo.org`,
  `hanzo.project`, `hanzo.translate.{tier,target,source,format,model,strings,
  cached,translated,failed,characters,chunks,retries,stored}`. A fully cached
  call has `stored 0`, `chunks 0`.
- Bulk: child span `translate.bulk` (feature, target, strings, characters,
  `http.response.status_code`).
- Quality: every completion carries `infer.ChatRequest.Feature = "translate"`,
  stamped `hanzo.feature` on cloud's gen_ai span (`sdk/infer/http.go`
  `setFeatureAttr`, beside `hanzo.org`/`hanzo.project`/`hanzo.agent.run_id`).
- No customer text in spans or logs. Per-string `error` reasons name a class,
  never a token. The engine-failure log carries tier, target, model, stored and
  the error's class (`errClass`: deadline, cancelled, busy, engine), never its
  text.
- Not carried (needs a decision): the plane's `aiobject.Caller` (org, person,
  project) and hanzoai/ai's `hanzo.cloud_usage` row have no feature field, and
  `metering.Usage` has none either; quality usage rows are `service ai` like
  chat. Join them to the translate span by trace id.

## Tests

`go test ./apps/translate/` — batch_test.go (chunks, retries, shapes, partial
failure), bounds_test.go (413, deadline, hangup over a real socket, stop rule,
call budget, one-string fallback, no echo, bulk billing, old formats),
migrate_test.go (key separation, old-file migration),
surface_test.go (spec body, model passthrough, bulk+model, spans),
translate_test.go, typed_wire_test.go. Gen_ai marker:
`go test -run TestGenAISpanCarriesTheFeature ./clients/`.

## HIP-0516 drift (hanzoai/hips, not edited here)

- §5 names `TRANSLATE_PRICE_UUSD_PER_1K_CHARS`; that env var is gone — the
  price is the meter-authority row `translate/bulk-chars` over a 20 µUSD floor.
- §8 says `beta`; the manifest row (`manifest/apps.go`) declares no Stage, so
  translate is GA and public.
- §1 says the route is raw because `text | batch` is a union; the code's
  reason is the bulk spend denial's nested 402/503 body (typed_wire_test.go).
- §3/§4 state no total limit: the 262144-character cap (413), the 90 s
  deadline, the two-calls-per-part budget and the two-failing-parts stop are
  new and belong in the spec.
- §6: the call now emits its own spans (`translate`, `translate.bulk`).
- §3's key tuple predates source language, format and model; §1's line refs
  (`translate.go:59`, `:181`) and §5's (`:340,361`, `engine.go:234`) moved.
