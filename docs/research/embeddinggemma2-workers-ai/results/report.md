# EmbeddingGemma 2: Hanzo Engine vs Cloudflare Workers AI

- **hanzo**: `http://127.0.0.1:1234/v1/embeddings` model `google/embeddinggemma-2`, hanzo-server 079edc61 (1.7.108 + engine#119), F32 CPU, NUM_THREADS=4 (2026-11-20T01:40:01Z; batch 4, concurrency 2)
- **cloudflare**: `https://api.cloudflare.com/client/v4/accounts/{account}/ai/run/@cf/google/embeddinggemma-2` model `@cf/google/embeddinggemma-2`,  (2026-11-21T04:04:04Z; batch 32, concurrency 4)

## text
mteb/scifact@cf10ab6856b1: 300 documents, 100 queries.

| policy | dim | hanzo ndcg@10 | cloudflare ndcg@10 | hanzo recall@10 | cloudflare recall@10 | hanzo recall@100 | cloudflare recall@100 | hanzo mrr@10 | cloudflare mrr@10 |
|---|---|---|---|---|---|---|---|---|---|
| task | 768 | 0.9623 | 0.9623 | 0.9900 | 0.9900 | 1.0000 | 1.0000 | 0.9575 | 0.9575 |
| task | 512 | 0.9688 | 0.9651 | 0.9900 | 0.9900 | 1.0000 | 1.0000 | 0.9645 | 0.9595 |
| task | 256 | 0.9519 | 0.9519 | 0.9900 | 0.9900 | 1.0000 | 1.0000 | 0.9403 | 0.9403 |
| task | 128 | 0.9467 | 0.9467 | 0.9800 | 0.9800 | 1.0000 | 1.0000 | 0.9407 | 0.9407 |
| none | 768 | 0.9611 | 0.9624 | 0.9800 | 0.9800 | 1.0000 | 1.0000 | 0.9583 | 0.9600 |
| none | 512 | 0.9630 | 0.9630 | 0.9800 | 0.9800 | 1.0000 | 1.0000 | 0.9583 | 0.9583 |
| none | 256 | 0.9551 | 0.9545 | 0.9800 | 0.9800 | 1.0000 | 1.0000 | 0.9483 | 0.9475 |
| none | 128 | 0.9257 | 0.9258 | 0.9750 | 0.9750 | 0.9900 | 0.9800 | 0.9147 | 0.9148 |

Vector agreement (cosine between the two sides' vectors for the same input, 768d) and top-10 overlap per width:

| policy | docs mean / min | queries mean / min | top-10 768d | top-10 512d | top-10 256d | top-10 128d |
|---|---|---|---|---|---|---|
| task | 0.999984 / 0.999947 | 0.999970 / 0.999926 | 0.987 | 0.990 | 0.995 | 0.990 |
| none | 0.999978 / 0.999907 | 0.999974 / 0.999871 | 0.989 | 0.989 | 0.988 | 0.981 |

Throughput and latency (first policy; documents embedded at 768):

| side | docs/s | tokens/s | doc tokens | bytes sent | query p50 s | p90 s | p99 s | dims probe min cos |
|---|---|---|---|---|---|---|---|---|
| hanzo | 0.17 | 56.3 | 97008 | 473194 | 0.8526 | 1.2412 | 1.4114 | 1.0000000 |
| cloudflare | 86.26 | 27892.9 | 97008 | 467894 | 0.5631 | 1.4256 | 3.2291 | 0.9999629 |

## code
mteb/CodeSearchNetRetrieval@68e8f0731a65: 300 documents, 100 queries.

| policy | dim | hanzo ndcg@10 | cloudflare ndcg@10 | hanzo recall@10 | cloudflare recall@10 | hanzo recall@100 | cloudflare recall@100 | hanzo mrr@10 | cloudflare mrr@10 |
|---|---|---|---|---|---|---|---|---|---|
| task | 768 | 0.9604 | 0.9601 | 0.9900 | 0.9900 | 1.0000 | 1.0000 | 0.9503 | 0.9500 |
| task | 512 | 0.9577 | 0.9577 | 0.9900 | 0.9900 | 1.0000 | 1.0000 | 0.9467 | 0.9467 |
| task | 256 | 0.9616 | 0.9616 | 0.9900 | 0.9900 | 1.0000 | 1.0000 | 0.9517 | 0.9517 |
| task | 128 | 0.9433 | 0.9433 | 0.9900 | 0.9900 | 1.0000 | 1.0000 | 0.9275 | 0.9275 |
| none | 768 | 0.9607 | 0.9607 | 0.9900 | 0.9900 | 1.0000 | 1.0000 | 0.9510 | 0.9510 |
| none | 512 | 0.9571 | 0.9571 | 0.9900 | 0.9900 | 1.0000 | 1.0000 | 0.9460 | 0.9460 |
| none | 256 | 0.9517 | 0.9517 | 0.9900 | 0.9900 | 1.0000 | 1.0000 | 0.9387 | 0.9387 |
| none | 128 | 0.9433 | 0.9440 | 0.9900 | 0.9900 | 1.0000 | 1.0000 | 0.9275 | 0.9283 |

Vector agreement (cosine between the two sides' vectors for the same input, 768d) and top-10 overlap per width:

| policy | docs mean / min | queries mean / min | top-10 768d | top-10 512d | top-10 256d | top-10 128d |
|---|---|---|---|---|---|---|
| task | 0.999991 / 0.999984 | 0.999988 / 0.999981 | 0.995 | 0.995 | 0.998 | 0.994 |
| none | 0.999991 / 0.999985 | 0.999987 / 0.999981 | 0.997 | 0.994 | 0.988 | 0.993 |

Throughput and latency (first policy; documents embedded at 768):

| side | docs/s | tokens/s | doc tokens | bytes sent | query p50 s | p90 s | p99 s | dims probe min cos |
|---|---|---|---|---|---|---|---|---|
| hanzo | 0.30 | 72.7 | 72054 | 274059 | 1.1128 | 2.4186 | 3.4036 | 1.0000000 |
| cloudflare | 110.34 | 26502.4 | 72054 | 268759 | 0.4947 | 0.6721 | 0.7099 | 0.9999721 |

## image
nlphuji/flickr_1k_test_image_text_retrieval@c4a4cdc67b77: 300 documents, 100 queries.
- **hanzo: not supported** — `HTTP 200 with an all-zero vector (1 prompt tokens): not an embedding`

| policy | dim | cloudflare recall@1 | cloudflare recall@5 | cloudflare recall@10 | cloudflare ndcg@10 |
|---|---|---|---|---|---|
| task | 768 | 0.8300 | 0.9500 | 0.9900 | 0.9138 |
| task | 512 | 0.8500 | 0.9500 | 0.9900 | 0.9233 |
| task | 256 | 0.8000 | 0.9500 | 0.9700 | 0.8982 |
| task | 128 | 0.6300 | 0.8600 | 0.9000 | 0.7642 |
| none | 768 | 0.8800 | 0.9600 | 0.9900 | 0.9337 |
| none | 512 | 0.8800 | 0.9600 | 0.9900 | 0.9354 |
| none | 256 | 0.8400 | 0.9400 | 0.9700 | 0.9087 |
| none | 128 | 0.6800 | 0.8800 | 0.9100 | 0.7914 |

Throughput and latency (first policy; documents embedded at 768):

| side | docs/s | tokens/s | doc tokens | bytes sent | query p50 s | p90 s | p99 s | dims probe min cos |
|---|---|---|---|---|---|---|---|---|
| cloudflare | 12.15 | 3213.6 | 79349 | 57074566 | 0.4894 | 0.7233 | 1.2007 | 0.9999792 |

## Full cost, USD: 1,000,000 documents, 1,000,000 queries a month

Inference (width-independent). Owned = CPU replica-hours at the production rate.

| suite | tokens/doc | tokens/query | rebuild: Workers AI | rebuild: owned (replica-h) | rebuild: media S3 to CF (GB) | queries/month: Workers AI | queries/month: owned (replica-h) |
|---|---|---|---|---|---|---|---|
| text | 323.4 | 25.7 | 0.55 | 54.01 (233.3) | 0 | 0.0436 | 4.29 (18.51) |
| code | 240.2 | 91.9 | 0.41 | 40.12 (173.3) | 0 | 0.1563 | 15.36 (66.33) |
| image | 264.5 | 23.8 | 0.45 | not supported | 17.11 (190.2) | 0.0404 | not supported |

Owned floor: two always-on replicas at 2 CPUs requested each, 169.00 a month whatever the load (shared with the other embedding model in the same process).

Storage and search, a month:

| dim | vectors GB | Vectorize (stored + queried dims) | vectors in R2 | owned store (gp3, x2 for the graph) |
|---|---|---|---|---|
| 768 | 3.072 | 15.24 | 0.046 | 0.49 |
| 512 | 2.048 | 9.99 | 0.031 | 0.33 |
| 256 | 1.024 | 4.74 | 0.015 | 0.16 |
| 128 | 0.512 | 2.12 | 0.008 | 0.08 |

Prices:
- `cf_per_m_tokens` = 0.0017: Workers AI pricing: @cf/google/embeddinggemma-2, $0.0017 per M input tokens
- `cf_neurons_per_m_tokens` = 154: Workers AI pricing: 154 neurons per M input tokens
- `cf_free_neurons_per_day` = 10000: Workers AI pricing: 10,000 neurons a day at no charge
- `cf_tokens_per_image` = 260: Workers AI model page: an image is about 260 tokens (used only until a run measures it)
- `vectorize_queried_per_m_dims` = 0.01: Vectorize pricing: $0.01 per M queried vector dimensions
- `vectorize_stored_per_100m_dims` = 0.05: Vectorize pricing: $0.05 per 100 M stored vector dimensions
- `vectorize_queried_included` = 50000000: Vectorize pricing: 50 M queried dimensions a month included (Paid)
- `vectorize_stored_included` = 10000000: Vectorize pricing: 10 M stored dimensions included (Paid)
- `r2_per_gb_month` = 0.015: R2 pricing: Standard storage $0.015 per GB-month, egress free
- `s3_egress_per_gb` = 0.09: AWS S3 data transfer out to the internet, first 10 TB/month, us-east-1
- `gp3_per_gb_month` = 0.08: AWS EBS gp3 $0.08 per GB-month, us-east-1 (universe infra/aws/main.tf)
- `replica_hour` = 0.2315: AWS m7a.xlarge on demand, $169/month list, us-east-1 (universe infra/aws/main.tf); one engine replica works on about four cores (universe values/hanzo/engine.yaml)
- `replica_tokens_per_s` = 385: engine 1.7.104, EmbeddingGemma 2 on four cores in the production pod envelope, ~310-token passages (universe values/hanzo/engine.yaml, 2026-10-08)
