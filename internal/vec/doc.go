// Package vec is the float32 embedding as this codebase stores and scores it: the
// blob a vector is kept as in a per-org SQLite row, the similarity two vectors
// are compared by, the bounded best-k a scan keeps, the batching an embedding
// request is sent in, and the space a vector is in (space.go): the one rule by
// which an index is created in one model revision, dimension, normalization and
// task prefix and never holds or answers a vector of another.
//
// It knows nothing about orgs, stores or documents, which is what lets the code
// index (apps/code) and the knowledge index (apps/knowledge) share it: both keep
// vectors as rows in the org's own database, record their space beside them, and
// score them in Go.
package vec
