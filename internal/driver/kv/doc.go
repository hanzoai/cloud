// Package kv is the Hanzo KV driver for sdk/coord: the shared backend that makes
// counts, single-use tokens, holds, spans and the live set agree across pods, and
// the bus the KV carries when no pubsub cluster is configured. Linking it registers
// it; KV_URL then names the KV.
package kv
