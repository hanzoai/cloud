# LLM.md — apps/knowledge

Guidance for apps/knowledge. Moved from the root LLM.md; cross-cutting rules stay in the root.

## A knowledge document may belong to one person

`kb.memory` and `kb.source` carry `owner`: empty is the org's, otherwise the
subject of the member it belongs to. `ownerOnSave` (before_save) lets a caller
claim a document only for themself and never moves an owner; `docMeta` copies
the owner into the vector payload and `ownerFilter` bounds every search — the
org itself (subject "") reaches only unowned points, a person reaches those and
their own. The subject is read from the validated principal (`subject(ctx)`,
`principal.Minted`) at `/v1/knowledge/search` and `/v1/search`
(`search.ForOrg(ctx, org, subject, in)`); the Team transactor passes "". What is
NOT here: a sync that pulls a person's own DMs through their connector —
integrations holds that token and the plane hands no token to another app, so
that sync has to be driven from integrations.

## Both legs of knowledge, and a rebuild for either

Knowledge now writes the lexical leg too: `lexical.go` puts one row per document
into the org's `kb` index (`apps/index`) on save and removes it on trash — key
`doctype/name` (the identity the legs fuse on), `text` (what a reranker reads),
`user` (the owner, so `index.Query`'s users bound reaches a person's rows for
that person alone; `/v1/search` passes `{"", subject}`). A deployment without
the index app is `ErrNotMounted`, which is no lexical leg and not a failure.
`POST /v1/knowledge/reindex` (org admin) empties the org's vector index and the
record of its embedding space in one transaction, re-embeds every page, memory
and source, restarts the embed stage of every ready file, and reconciles the
lexical corpus — the one way to change the model, its revision, dimension,
normalization or task prefix under a live org (next section), and the way back
after a vector outage. Production serves `zen-embedding` (1024) and `zen-rerank`.

## An index keeps one embedding space, and only a rebuild changes it

`internal/vec/space.go` is the one rule for every vector store here (knowledge,
apps/code): a `vec.Space` is `model_revision` (CLOUD_EMBED_MODEL, plus
`@CLOUD_EMBED_REVISION` when set), `dimensions` (what the model answers),
`normalization` (`none` today) and the `task` prefixes (query, document; none
today). `vec.Policy(model)` is the process's; `Space.Documents` / `Space.Query`
put the prefix on and shape the vectors. Each store records its index's space in
`vector_spaces` with its first vector (`Index.Claim`, insert-only) and admits a
write or a query only in that space (`Index.Admit`); anything else is a
`*vec.MixedError` naming the fields that differ. Knowledge's index is ONE space
over `chunks` and `file_passages`. A refused document save keeps no passages
(the failure contract); a refused file stays ready, searched by its words, with
the reason in its error and no retry; a refused query is the degraded vector leg.
Both stores check the policy before buying an embedding, so a refusal costs
nothing but a dimension change (seen only once the model answers). Stores from
before spaces have vectors and no record: they keep answering (rows are still
read by the query's model and dimension) and the first write adopts them, unless
the process embeds with a normalization or prefix, which those vectors never
had. apps/code's rebuild is `POST /v1/code/reindex` (org admin, `reindex.go`):
it re-embeds every chunk of every repository from the index's own text, and
replaces the old vectors and record in the transaction that writes the first new
page, so the index is never without a record another space could take. In the
space it is already in, it embeds the first chunk (a probe: the model may answer
at another dimension under the same name) and then only chunks with no vector,
so a reindex that stopped part way is finished by running it again. Neither
rebuild is automatic: during a rollout two pods embed in two spaces, and an index
that moved itself would move back and forth, billing the org each time.

## Workspace files: upload, ingest in stages, retrieve ToC-first

`apps/s3/multipart.go` + `apps/knowledge/{file,extract,toc,ingest,filegraph,filesearch}.go`.

- **Upload.** Small: `POST /v1/s3/buckets/:b/objects` (one presigned PUT). Large: `POST
  /v1/s3/buckets/:b/uploads` → `…/:upload/parts` (≤64 presigned part URLs, 5 min) →
  `GET …/:upload?key=` (stored parts, to resume) → `…/:upload/complete` (the server
  lists the parts and completes, so the browser never reads an ETag) or `DELETE`.
  `PartSize` 16 MiB: Cloudflare in front of s3.hanzo.ai refuses a body over 100 MB.
- **Register.** `POST /v1/knowledge/files {bucket,key}` → a record in the org's
  knowledge.db (id = hash of bucket+key), `file_stored` on the event plane, and a
  durable workflow on the process's embedded tasks engine (queue
  `cloud-knowledge-files`). Status queued → indexing → ready | stored (kept, not
  indexed, Error says why) | failed; `stage` names the stage.
- **Stages**, each an activity retried on its own: extract (text + headings: HTML
  h1–h6/landmarks, Markdown, PDF outline else type size — its text read behind
  any standard filter chain, a /Contents array or inline images, `internal/pdftext/plain.go`, its words kept apart in a standard font written without /Widths, `widths.go`, each glyph read through its font's /ToUnicode CMap (Type0/CID, subset, CJK), `face.go`/`cmap.go` —
  DOCX heading styles, code
  symbols, CSV/XLSX sheets in 100-row sections; picture → zen-vl, recording →
  zen-scribe; .zip unpacked into `<dir>/<stem>/…`, each entry its own file) → toc
  (`CLOUD_FILES_MODEL`, default zen5-flash: one-line summary + entities per
  section, batched; first sentence stands in when no model answers) → passages
  (`split()` inside each section body, never across; one full-text row each in
  index uid `files`, key `<file>#<seq>`) → graph (hrefs, "see Section 4.2", file
  names cited; a reference to a file not yet uploaded resolves when it arrives)
  → **ready** → embed (16 a round, progress in `embedded`, Stage `embed` on a
  ready file). Embedding is last because it is the slow stage and nothing a
  person reads waits on it; a file whose vectors never land stays ready,
  searched by its words, with the reason in Error.
- **A record that claims work nothing drives is started again.** Every 10 min
  (first pass 10 min after mount, so the engine retries its own runs first) the
  org's writer reads files queued, indexing, ready with a stage, or forgetting,
  and one untouched for longer than its stage's budget + 10 min (an attempt marks
  its stage as it starts, progress bumps `updated`) is re-enqueued at that stage
  (`IngestIn.From`) or re-forgotten. The record carries the org it was registered
  by (`files.org`), since the store's folded namespace cannot name it. The build
  before graph moved ahead of embed marked a finished file ready AT stage graph;
  opening the store rewrites that to ready with no stage (`finishedEarlier`).
- **Sections tile the document**: bodies concatenated are the text; a body over
  16 KB is cut into synthetic subsections; a document whose one H1 opens it is
  titled by it.
- **Whole files, a segment at a time.** extract reads 4 MB of text (`segText`)
  per step and writes that segment's sections and links, with the file's resume
  point (`done` of `total`), in one transaction before reading the next — text,
  Markdown, code and CSV by ranged reads of the object (`objects.span`), however
  long; a PDF, DOCX, sheet, HTML page or archive read whole (≤ 256 MB) and its
  text cut into segments. A retry resumes at `done`; `done == 0` forgets what an
  earlier version left. Headings open in one segment stay open in the next
  (`grower`, read back from the rows, so nothing else is state). toc, passages,
  graph and embed walk the sections a page (~4 MB of text) at a time, so memory
  never holds a whole file; toc and passages resume after the last page written.
  `done`/`total` are the running stage's progress (bytes read, sections, passages
  embedded), on the chip and in Drive.
- **Bounds, said on the file.** Every byte of text indexed costs the shared
  index ~12 more on cloud's volume (a local-path PVC on the node's root disk).
  So: an org's files are indexed for `CLOUD_FILES_ORG_TEXT` bytes of text in all
  (2 GiB); a segment is written only while that volume keeps a fifth of itself
  free (8–16 GiB); a file's first 20,000 passages (`maxVectors`) get vectors and
  the rest are searched by words; the model summarizes 600 sections and the rest
  get their first sentence. A file stopped by a bound is `clipped` and its `note`
  says how far it was read and why ("Indexed its first 1.0 GB of 3.0 GB: …");
  a chat turn carries that note to the model.
- **Reads that stay fast at any size.** The shared index's search is one range
  seek per query word (a UNION ALL; OR-ed ranges made SQLite read every term an
  org holds — 9–15 s a query once a 150 MB log was in), its matches drive the
  join and fetch each document by key (CROSS JOIN; the other order walked every
  document, 1.6 s for a word nothing holds), ranked by how many of the query's
  words a document holds (Meilisearch's "words"). `TestSearchReadsEachWordsRangeAlone`
  holds the plan. A drill inside
  sections reads only those sections: their vectors by the (file, section)
  index, their words scored in process by `index.Coverage`; a section with more
  than 200 under it is searched as its whole file. `cite` reads a section's
  lineage, not the file's whole ToC, and the ToC pick is shown the sections the
  search matched first. A page of passages reaches the index as one plane call
  (`index_put_all`) and one transaction with its terms in key order: 15.5 → 126
  passages a second on prod.
- **Forget.** `DELETE /v1/knowledge/files/:id` answers at once. The record turns
  `forgetting` (`visible` in file.go: no read — get, list, search, retrieve, the
  plane — finds it; links to and from it go; its text leaves the org's bound;
  a running ingest's next write gets `errSuperseded` and its stage stops), and
  `FileForgetWorkflow` on the same queue removes its full-text rows 500 keys a
  transaction with the record's done of total as cursor, then its passages,
  sections and record. A per-key delete in the shared index costs about a WAL
  page per row (136 KB of WAL per passage at 500 a transaction, measured), so a
  60k-passage file keeps the index busy for minutes; nothing waits on it.
  A path param is borrowed from the request's buffer, which the next request
  reuses: `strings.Clone` anything a goroutine keeps (`erase`).
- **Read**: `files/search` (vector + full text, RRF, every passage cites `file ›
  section path › ¶n`), `files/:id/toc`, `files/:id/sections/:n`, `files/:id/graph`,
  `files/retrieve` (search finds candidate documents, a model picks sections from
  their ToCs, hybrid drills inside them, the graph adds up to 4 linked passages in
  any file). Candidates of 8 sections or fewer are read whole (`picked: whole`),
  ones with no headings of their own (a log's size-cut parts) skip the pick, and
  a pick waits 4 s; the search's best sections are always read beside a pick. `/v1/search` gets the passages as its `files` leg over the plane op
  `knowledge_files`. All typed, so all are MCP tools on the fleet endpoint.
