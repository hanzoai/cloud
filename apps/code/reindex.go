package code

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// reindex.go is the one way an org's code index reaches another embedding space
// (internal/vec). The index records the space of its first vector and refuses
// any other, so after the deployment's model, revision, dimension,
// normalization or task prefix changes, every index pass writes no vectors and
// the semantic tier answers degraded until this runs. It is explicit, an org
// admin's call, and never automatic: during a rollout two pods embed in two
// spaces, and an index that moved itself whenever it saw another would move back
// and forth between them, billing the org for each move.

// defaultReindexPage is how many chunks a reindex embeds and writes at a time.
const defaultReindexPage = 256

// reindexPage is defaultReindexPage, which a test shrinks so a few files take
// several pages.
var reindexPage = defaultReindexPage

// reindexPageWait bounds one page's embeddings.
const reindexPageWait = time.Minute

// reindexResult is what a reindex did and where the index stands.
type reindexResult struct {
	// Chunks is how many chunks the org's code index holds, across every
	// repository.
	Chunks int `json:"chunks"`
	// Embedded is how many chunks this call embedded and wrote: every chunk
	// when the index moved to this deployment's embedding space, and otherwise
	// the first chunk (embedded again to learn the dimension the model answers
	// now) and the chunks that had no vector.
	Embedded int `json:"embedded"`
	// Vectors is how many chunks have a vector now. Equal to Chunks when the
	// semantic tier covers the whole index.
	Vectors int `json:"vectors"`
	// ModelRevision is the embedding model, and its revision when the deployment
	// states one, that the index is in now. Empty for an index with no chunks.
	ModelRevision string `json:"model_revision"`
	// Dimensions is how many components each of the index's vectors has. 0 for
	// an index with no chunks.
	Dimensions int `json:"dimensions"`
}

// reindex rebuilds the caller org's code index in the embedding space this
// deployment embeds in, from the code the index already holds, across every
// repository. An index created in another space (another model, revision,
// dimension, normalization or task prefix) has every chunk embedded again, and
// its old vectors and the record of their space are replaced in the same
// transaction as the first new ones. An index already in this space keeps its
// vectors: the first chunk is embedded again to learn whether the model still
// answers at the index's dimension, then only the chunks that have no vector
// are, so a reindex that stopped part way is finished by calling it again. It
// requires ORG ADMIN, bills the embeddings to the caller's org and runs inline.
// An embedding failure part way answers 502 with how far it got; the index is
// then in the new space with the chunks it reached.
//
// The request has no body. Response: {"chunks": 2048, "embedded": 2048, "vectors": 2048, "model_revision": "zen-embedding", "dimensions": 1024}
func (s *service) reindex(ctx context.Context, _ *cloud.Unit) (*reindexResult, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	c, ok := cloud.Request(ctx)
	if !ok || !(principal.IsOrgAdmin(c) || principal.Super(c)) {
		return nil, zip.ErrForbidden("reindex requires org admin")
	}
	if !s.embed.Enabled() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the semantic tier is not configured here")
	}
	store, err := s.storeFor(org)
	if err != nil {
		return nil, zip.ErrInternal("open index")
	}
	payer, project := billed(ctx)
	out, err := s.reembed(ctx, org, payer, project, store)
	if err != nil {
		s.log.Warn("code reindex stopped", "org", org, "embedded", out.Embedded, "chunks", out.Chunks, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway,
			"reindex: embedded %d of %d chunks, then: %v. The index is in this deployment's embedding space with the chunks it reached; reindex again to embed the rest",
			out.Embedded, out.Chunks, err)
	}
	return &out, nil
}

// reembed is reindex's work over one org's store. On an error it answers how
// far it got.
func (s *service) reembed(ctx context.Context, org string, payer account.Account, project string, st *Store) (reindexResult, error) {
	var out reindexResult
	chunks, _, err := st.coverage(ctx)
	if err != nil {
		return out, err
	}
	out.Chunks = chunks
	sp := s.embed.Space()
	held, ok, err := codeIndex.Space(ctx, st.db)
	if err != nil {
		return out, err
	}
	// The index moves when it has no record (an empty one, or one from before
	// spaces whose vectors' model cannot be told) or one of another policy. One
	// of this policy may still be of another dimension, which only the model's
	// answer tells: its first chunk is embedded alone, as a probe.
	moving := !ok || held.Model != sp.Model || held.Normalization != sp.Normalization || held.Task != sp.Task
	probe := !moving
	embed := func(texts []string) ([][]float32, vec.Space, error) {
		ectx, cancel := context.WithTimeout(ctx, reindexPageWait)
		defer cancel()
		vs, got, err := sp.Documents(texts, func(in []string) ([][]float32, error) {
			return s.embed.Embed(ectx, org, payer, project, in)
		})
		if err == nil && len(vs) != len(texts) {
			err = errors.New("the embedder answered no vectors")
		}
		return vs, got, err
	}
	var after int64
	for first := true; ; first = false {
		n, missing := reindexPage, !first
		if probe && first {
			n = 1
		}
		ids, texts, err := st.chunkPage(ctx, after, missing, n)
		if err != nil {
			return out, err
		}
		if len(ids) == 0 {
			if first && moving {
				// Nothing to embed: the record of another space goes, so the
				// next write creates the index in its own.
				if err := st.clearVectors(ctx); err != nil {
					return out, err
				}
			}
			break
		}
		vs, got, err := embed(texts)
		if err != nil {
			return out, err
		}
		if probe && first && got.Dimensions != held.Dimensions {
			moving = true
		}
		wrote, err := st.putVectors(ctx, got, ids, vs, first && moving)
		if err != nil {
			return out, err
		}
		out.Embedded += wrote
		if probe && first {
			// After the probe every chunk is read again from the start: a move
			// has just cleared them all, and otherwise the chunks before it
			// may be missing too.
			if !moving {
				if err := st.dropOtherDimensions(ctx, held.Dimensions); err != nil {
					return out, err
				}
			}
			after = 0
			continue
		}
		after = ids[len(ids)-1]
	}
	if out.Chunks, out.Vectors, err = st.coverage(ctx); err != nil {
		return out, err
	}
	if now, ok, err := codeIndex.Space(ctx, st.db); err != nil {
		return out, err
	} else if ok {
		out.ModelRevision, out.Dimensions = now.Model, now.Dimensions
	}
	return out, nil
}
