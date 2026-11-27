# LLM.md — apps/search

Guidance for apps/search. Moved from the root LLM.md; cross-cutting rules stay in the root.

## Rerank: the fourth stage of /v1/search

`AIClient.Rerank` sits beside `Embed`: the gateway's Cohere-shaped `/rerank`
(`sdk/infer/http.go`), metered on the same known-before-the-call estimate
(`metered_ai.go`), served as `zen-rerank` (`CLOUD_RERANK_MODEL`): Qwen3-Reranker-0.6B
on dgx's GPU through the router rung of the zen catalog (universe
`infra/lan/engines/dgx-hanzo-embed`), one rung, so with dgx away the stage reads
`degraded`. 10 passages (~12k tokens) rerank in ~2.5 s. `/v1/search`
runs it over the whole fused window before paging and reports it in `Backends`
as `rerank` — `disabled` without a read-scope client (`deps.Embed`), `degraded`
when the call fails (fused order stands), `ok` with the rows reordered by
relevance and a `rerank` origin appended to each row's provenance. The text it
scores is what each leg carried: `knowledge.Semantic` reads the document from
the store (`Hit.Text`, never on the wire — the vector payload holds no body by
design), the lexical row is scored through `knowledge.Text`, a code span through
its snippet. Every `AIClient` fake stubs `Rerank`; adding a fake means adding
that line.
