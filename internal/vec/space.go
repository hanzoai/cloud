package vec

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
)

// Space is what makes two vectors comparable: the model that embedded them at
// the revision it was served at, how many components they have, what was done
// to their length, and the task prefixes their text was embedded under. One
// model name answers in many spaces (another revision, a shortened dimension, a
// retrieval prefix where a classification one was used), and a cosine across
// two of them is a number that means nothing, so a vector of one space is never
// stored beside, or scored against, a vector of another.
//
// An index records its space when its first vector is written (Index.Claim) and
// never changes it in place: nothing here rewrites a record. Every later write
// and every query is admitted only in that space (Index.Admit) and is otherwise
// refused with a *MixedError that names what differs. To embed in another space
// an index is rebuilt: its vectors and its record go in one transaction
// (Index.Forget), and the next write creates it anew.
type Space struct {
	// Model is the model_revision: the model's name, then "@" and its revision
	// when the deployment states one (CLOUD_EMBED_REVISION).
	Model string
	// Dimensions is how many components each vector has. It is 0 in a space
	// nothing has been embedded in yet (Policy): the model answers it, and Of
	// records what it answered.
	Dimensions int
	// Normalization is what was done to a vector's length before it was kept:
	// NormNone (as the model answered) or NormL2 (unit length).
	Normalization string
	// Task is the task policy the text was embedded under.
	Task Task
}

// Task is a task policy: the prefix put before a query's text and the one put
// before a document's text when each is embedded. A model can be asked for
// retrieval with a query prefix unlike its document prefix, or for
// classification with others again; the zero Task embeds text as it is.
type Task struct {
	Query    string
	Document string
}

// The normalizations a space keeps its vectors in.
const (
	NormNone = "none" // as the model answered
	NormL2   = "l2"   // scaled to unit length
)

// RevisionEnv states the revision of the embedding model a deployment serves,
// which a model name alone does not: the served model can change under it.
const RevisionEnv = "CLOUD_EMBED_REVISION"

// Policy is the space this deployment embeds in with model, before it has
// embedded anything: the model at RevisionEnv's revision when one is set, kept
// as the model answers, with no task prefix. Every vector store here embeds
// under it; its dimension is the model's to answer (Of).
func Policy(model string) Space {
	if rev := strings.TrimSpace(os.Getenv(RevisionEnv)); rev != "" {
		model += "@" + rev
	}
	return Space{Model: model, Normalization: NormNone}
}

// Of is s with the dimension of vs. They must all share one, and none may be
// empty: no space holds a batch that does not, so it is refused rather than
// stored or scored. No vectors at all is s as it is.
func (s Space) Of(vs ...[]float32) (Space, error) {
	for i, v := range vs {
		if len(v) == 0 {
			return s, fmt.Errorf("vec: vector %d of %d is empty", i+1, len(vs))
		}
		if len(v) != len(vs[0]) {
			return s, fmt.Errorf("vec: a %d-dimensional vector among %d-dimensional ones", len(v), len(vs[0]))
		}
	}
	if len(vs) > 0 {
		s.Dimensions = len(vs[0])
	}
	return s, nil
}

// Documents embeds texts as documents of s through embed, each after the
// document prefix, every vector shaped as s says, and answers them with the
// space they are in. An embed that answers nothing (a disabled embedder) is no
// vectors and no error; an answer that is not one vector per text, or holds two
// dimensions, is refused.
func (s Space) Documents(texts []string, embed func([]string) ([][]float32, error)) ([][]float32, Space, error) {
	return s.embed(s.Task.Document, texts, embed)
}

// Query embeds one query of s through embed, after the query prefix, shaped as
// s says, and answers it with the space it is in. A nil vector with no error is
// an embedder with nothing to answer.
func (s Space) Query(text string, embed func([]string) ([][]float32, error)) ([]float32, Space, error) {
	vs, got, err := s.embed(s.Task.Query, []string{text}, embed)
	if err != nil || len(vs) == 0 {
		return nil, got, err
	}
	return vs[0], got, nil
}

func (s Space) embed(prefix string, texts []string, embed func([]string) ([][]float32, error)) ([][]float32, Space, error) {
	in := texts
	if prefix != "" {
		in = make([]string, len(texts))
		for i, t := range texts {
			in[i] = prefix + t
		}
	}
	vs, err := embed(in)
	if err != nil {
		return nil, s, err
	}
	if len(vs) == 0 {
		return nil, s, nil
	}
	if len(vs) != len(texts) {
		return nil, s, fmt.Errorf("vec: %d vectors for %d texts", len(vs), len(texts))
	}
	got, err := s.Of(vs...)
	if err != nil {
		return nil, s, err
	}
	for _, v := range vs {
		s.shape(v)
	}
	return vs, got, nil
}

// shape applies s's normalization to v in place. A zero vector has no
// direction and stays as it is.
func (s Space) shape(v []float32) {
	if s.Normalization != NormL2 {
		return
	}
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return
	}
	k := 1 / math.Sqrt(n)
	for i := range v {
		v[i] = float32(float64(v[i]) * k)
	}
}

// ErrMixed is every refusal of a vector or a query in a space its index was
// not created in; the error itself is a *MixedError.
var ErrMixed = errors.New("vec: vectors of another space")

// MixedError is a write or a query refused by an index of another space.
type MixedError struct {
	// Index is the index that refused.
	Index string
	// Held is the space the index was created in. An index written before
	// spaces were recorded has a Held of what is known of its vectors: no
	// model, no dimension, NormNone and no task prefix.
	Held Space
	// Got is the space of the refused vectors or query.
	Got Space
}

func (e *MixedError) Is(target error) bool { return target == ErrMixed }

// Fields names the fields that differ, as an index records them.
func (e *MixedError) Fields() []string {
	var out []string
	for _, f := range e.diff() {
		out = append(out, f.name)
	}
	return out
}

func (e *MixedError) Error() string {
	var held, got []string
	for _, f := range e.diff() {
		held = append(held, f.name+"="+f.held)
		got = append(got, f.name+"="+f.got)
	}
	created := "was created with"
	if e.Held.Model == "" {
		created = "holds vectors written before it recorded a space, with"
	}
	return fmt.Sprintf("vec: index %q %s %s; refused vectors with %s. An index's space never changes in place: rebuild it, or create a new index, to embed in another",
		e.Index, created, strings.Join(held, ", "), strings.Join(got, ", "))
}

type field struct{ name, held, got string }

// diff is the fields Held and Got differ on. An unknown model or dimension (an
// index from before spaces, a space nothing has been embedded in yet) differs
// from nothing.
func (e *MixedError) diff() []field {
	h, g := e.Held, e.Got
	var out []field
	if h.Model != "" && g.Model != "" && h.Model != g.Model {
		out = append(out, field{"model_revision", fmt.Sprintf("%q", h.Model), fmt.Sprintf("%q", g.Model)})
	}
	if h.Dimensions != 0 && g.Dimensions != 0 && h.Dimensions != g.Dimensions {
		out = append(out, field{"dimensions", fmt.Sprint(h.Dimensions), fmt.Sprint(g.Dimensions)})
	}
	if h.Normalization != g.Normalization {
		out = append(out, field{"normalization", fmt.Sprintf("%q", h.Normalization), fmt.Sprintf("%q", g.Normalization)})
	}
	if h.Task.Query != g.Task.Query {
		out = append(out, field{"task.query", fmt.Sprintf("%q", h.Task.Query), fmt.Sprintf("%q", g.Task.Query)})
	}
	if h.Task.Document != g.Task.Document {
		out = append(out, field{"task.document", fmt.Sprintf("%q", h.Task.Document), fmt.Sprintf("%q", g.Task.Document)})
	}
	return out
}

// mix answers a refusal when held and got differ, nil when they are one space.
func mix(index string, held, got Space) error {
	if e := (&MixedError{Index: index, Held: held, Got: got}); len(e.diff()) > 0 {
		return e
	}
	return nil
}

// SpaceDDL is the table an index's space is recorded in, one row per index, in
// the database that holds the index's vectors. It is SQL both engines share
// (sdk/dialect), and a store runs it in its own migration.
const SpaceDDL = `
CREATE TABLE IF NOT EXISTS vector_spaces (
  name           TEXT   PRIMARY KEY,
  model_revision TEXT   NOT NULL,
  dimensions     BIGINT NOT NULL,
  normalization  TEXT   NOT NULL,
  task_query     TEXT   NOT NULL,
  task_document  TEXT   NOT NULL
);
`

// Index is one store's vectors as an index with a space: its Name, the row it
// is recorded under in vector_spaces, and Held, a query that answers a row when
// the index holds any vector, which is how a store written before spaces were
// recorded is told from an empty one.
type Index struct {
	Name string
	Held string
}

// Querier reads: a *sql.DB or a *sql.Tx.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Execer reads and writes: a *sql.Tx, for the write that creates an index.
type Execer interface {
	Querier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Space reads the space the index was created in; ok is false while it has
// none, which is an index with no vectors yet or one written before spaces
// were recorded.
func (x Index) Space(ctx context.Context, q Querier) (s Space, ok bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT model_revision, dimensions, normalization, task_query, task_document
FROM vector_spaces WHERE name=$1`, x.Name).Scan(&s.Model, &s.Dimensions, &s.Normalization, &s.Task.Query, &s.Task.Document)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Space{}, false, nil
	case err != nil:
		return Space{}, false, fmt.Errorf("vec: read the space of index %q: %w", x.Name, err)
	}
	return s, true, nil
}

// Admit answers whether the index takes vectors, or answers a query, of space
// s, and only reads. It is nil when the index was created in s or holds no
// vectors yet, and a *MixedError otherwise. A space whose dimension is not
// known yet (0) is checked on its other fields, so a store can refuse before it
// buys an embedding no write would keep.
//
// An index written before spaces were recorded has no record, and what is known
// of its vectors is that every one was kept as the model answered with no task
// prefix: it admits a space that shares those, and its model and dimension are
// left to the store's own reading, which reads only rows of the query's.
func (x Index) Admit(ctx context.Context, q Querier, s Space) error {
	held, ok, err := x.Space(ctx, q)
	if err != nil {
		return err
	}
	if ok {
		return mix(x.Name, held, s)
	}
	var one int
	switch err := q.QueryRowContext(ctx, x.Held).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("vec: read whether index %q holds vectors: %w", x.Name, err)
	}
	return mix(x.Name, Space{Normalization: NormNone}, s)
}

// Claim admits vectors of space s for a write, inside the writer's own
// transaction, and creates the index in s when it has no record yet: an empty
// index, or one written before spaces were recorded that s may adopt (Admit).
// s must carry its dimension (Of). The record is only ever inserted, never
// rewritten, and a writer that raced this one to an empty index is read back
// and compared, so of two first writes in two spaces one is refused.
func (x Index) Claim(ctx context.Context, tx Execer, s Space) error {
	if s.Dimensions <= 0 {
		return fmt.Errorf("vec: index %q: a write must carry the dimension of its vectors", x.Name)
	}
	if err := x.Admit(ctx, tx, s); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vector_spaces (name, model_revision, dimensions, normalization, task_query, task_document)
VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (name) DO NOTHING`,
		x.Name, s.Model, s.Dimensions, s.Normalization, s.Task.Query, s.Task.Document); err != nil {
		return fmt.Errorf("vec: record the space of index %q: %w", x.Name, err)
	}
	held, _, err := x.Space(ctx, tx)
	if err != nil {
		return err
	}
	return mix(x.Name, held, s)
}

// Forget removes the index's record. Only a rebuild calls it, in the
// transaction that removes every vector the index holds, so the next write
// creates the index anew in whatever space it brings.
func (x Index) Forget(ctx context.Context, tx Execer) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM vector_spaces WHERE name=$1`, x.Name); err != nil {
		return fmt.Errorf("vec: forget the space of index %q: %w", x.Name, err)
	}
	return nil
}
