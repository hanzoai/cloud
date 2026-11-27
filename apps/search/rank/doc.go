// Package rank is the ONE rank-fusion implementation in the codebase.
//
// It is a leaf: it imports nothing and knows nothing about documents, orgs, or
// stores. That is deliberate. Fusion is needed at two DIFFERENT levels — across
// the tiers inside one corpus (clients/code fuses lexical + symbolic + semantic)
// and across corpora at the /v1/search surface — and a package that knew about
// either level could only serve that one, which is how a codebase ends up with
// two copies of the same algorithm drifting apart.
//
// Callers pass ranked KEYS and get back fused keys with provenance; mapping keys
// to payloads stays with the caller, who is the only one who knows what a key
// means. The key is a type parameter for the same reason: the code index fuses
// chunk ids (int64) and /v1/search fuses document addresses (string), and one
// implementation serves both without either rendering its key as the other's.
package rank
