package research

// decision.go is the decision thread: the evidence a decision rests on, the DDT
// Decision records that name it, the evaluations that read it, and the actions and
// human commitments that follow from them. DAOSoft's Decision Digital Thread
// (DDT-1.0) records travel VERBATIM inside these envelopes; nothing of DDT is
// renamed, and the server checks of a DDT record only what the envelope needs and
// what only a person may change.
//
//	POST /v1/research/evidence                 file one piece of evidence (versions per uri)
//	POST /v1/research/evidence/sysml           file a SysML v2 model's elements as evidence
//	GET  /v1/research/evidence                 a uri's versions (?uri=), or every uri's newest (?org=)
//	GET  /v1/research/evidence/dependents      the decisions and evaluations resting on a uri
//	GET  /v1/research/evidence/{id}            one version, with its content
//	POST /v1/research/decisions                file a DDT Decision (versions per decision id)
//	GET  /v1/research/decisions                list them (?id= ?decision= ?org=)
//	POST /v1/research/evaluations              file a DDT Evaluation Run with Kai's Decision Package
//	GET  /v1/research/evaluations              list them (?id= ?decision= ?org=)
//	POST /v1/research/actions                  file a DDT Action as a DRAFT
//	GET  /v1/research/actions                  list them (?id= ?decision= ?status=)
//	POST /v1/research/actions/{id}/authorize   a person authorizes the state of one they read
//	POST /v1/research/commitments              a person commits to a decision
//	GET  /v1/research/commitments              list them (?decision=)
//
// Storage is store.go's: the same per-org SQLite, the same orm `_entities`, the
// same append clock deciding which version is newest, the same private-by-default
// grant. Evidence content goes to the hash-addressed blob keyspace the diary's
// artifacts use, so a hash is an address.
//
// HUMAN ONLY. Authorizing an action, committing to a decision, changing what a
// DDT Decision's agentPermissions.mayNotChange names (its lifecycleState among
// them) and granting a decision, an evaluation or evidence a visibility are a
// person's acts: each refuses, 403, every caller that is not a person
// signed in with their own token in their own org — principal.Signed, the
// predicate apps/git seats a person by.
//
// BOUND TO WHAT WAS READ. An authorization names the hash of the action state the
// person read, and a commitment the evaluation filing it rests on; every filing
// is kept, so what a person acted on stays readable.
//
// RECONSIDER. A write that files a new version of a uri that already had one
// publishes one research.reconsider on the bus (topic), naming every uri it
// changed and the decisions and evaluations that now rest on an older version,
// so a client reruns only what changed.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"
)

// The closed vocabularies a write is checked against.
var (
	// evidenceKinds is what a piece of evidence may be.
	evidenceKinds = map[string]bool{"requirement": true, "test": true, "model": true, "analysis": true, "document": true, "element": true}
	// rels are the edges a link may name.
	rels = map[string]bool{"satisfies": true, "verifies": true, "derives": true, "supersedes": true, "refines": true, "dependsOn": true}
	// threadKinds are the decision thread's kinds a grant may name, in the order a
	// refusal names them.
	threadKinds = []string{"decision", "evaluation", "evidence"}
)

// An action's two states: filed by anyone as a draft, authorized by a person.
const (
	draft      = "DRAFT"
	authorized = "AUTHORIZED"
)

// kaiRun begins the id Kai's DDT export gives an Evaluation Run (hanzo-inc/kai
// program/src/ddt.rs): RUN_KAI_ and its package hash's first 16 hex digits,
// uppercased.
const kaiRun = "RUN_KAI_"

// ── the wire ──────────────────────────────────────────────────────────────────

// Source is the system a piece of evidence was read from.
type Source struct {
	// System names the system of record: sysml, ddt, plm, techdata. Free text.
	System string `json:"system"`
	// Revision is that system's own revision of what was read: a SysML commit,
	// a PLM baseline. Empty when the system has none.
	Revision string `json:"revision,omitempty"`
}

// Link is one typed edge from a piece of evidence to another, by uri.
type Link struct {
	// Rel is the edge: satisfies, verifies, derives, supersedes, refines or
	// dependsOn.
	Rel string `json:"rel"`
	// URI is the evidence the edge points at.
	URI string `json:"uri"`
}

// Evidence is one version of a named piece of evidence: a requirement, a test, a
// model, an analysis, a document, or one SysML element. Versions accrue per uri:
// content whose hash is the uri's newest is that version, any other content is the
// next version — even content an older version held, so a revert is a change —
// and every earlier version is kept.
type Evidence struct {
	// ID is this version's record id, `ev_` and 32 hex digits. The server's; a
	// write ignores it.
	ID string `json:"id"`
	// URI is the evidence's stable name, the key its versions accrue under. A
	// SysML element's is sysml://<project>@<commit>#<elementId>. Required.
	URI string `json:"uri"`
	// Hash is the lowercase hex SHA-256 of the content's canonical JSON (keys
	// sorted, no whitespace, shortest numbers — the form DDT and Kai hash). The
	// server computes it; a hash sent beside the content must agree. A `sha256:`
	// prefix is accepted and dropped.
	Hash string `json:"hash"`
	// Kind is requirement, test, model, analysis, document, or element for a
	// SysML element. Required.
	Kind string `json:"kind"`
	// Source is the system the evidence was read from.
	Source Source `json:"source"`
	// Links are its typed edges to other evidence, by uri.
	Links []Link `json:"links"`
	// Content is the evidence itself, any JSON value of at most 16 MiB, kept in
	// the hash-addressed blob store. Required: the server files what it hashed
	// and nothing it did not. Only GET /v1/research/evidence/{id} and the write
	// that filed it answer it; a listing leaves it out.
	Content json.RawMessage `json:"content,omitempty"`
	// Time is when this version was filed, RFC 3339 UTC. The server's.
	Time string `json:"time"`
	// Version is this version's ordinal for its uri: 1 for the first content
	// filed under it, then one more for each filing whose hash is not the uri's
	// newest. The server's.
	Version int `json:"version"`
	// By is the user who filed this version. The server's.
	By string `json:"by"`
	// Visibility is private, org or public. A write records private; a grant
	// (POST /v1/research/grants, kind evidence, the uri as its id) moves every
	// version.
	Visibility string `json:"visibility"`
}

// Dependency is one piece of evidence a record rests on.
type Dependency struct {
	// URI names the evidence.
	URI string `json:"uri"`
	// Hash is the version the record read, lowercase hex. Empty when the record
	// names the uri and no version of it.
	Hash string `json:"hash,omitempty"`
}

// Charter is DAOSoft's decision charter: the question a decision answers, and on
// what terms.
type Charter struct {
	// Question is what the decision answers, in a sentence. Required when a
	// charter is sent.
	Question string `json:"question"`
	// Scope is what the decision covers.
	Scope string `json:"scope,omitempty"`
	// Owner is who owns the decision.
	Owner string `json:"owner,omitempty"`
	// Due is when the decision is due.
	Due string `json:"due,omitempty"`
	// Criteria are what the options are judged by.
	Criteria []string `json:"criteria,omitempty"`
}

// Program names the Decision Program bound from a decision.
type Program struct {
	// ID is the program's id, apu.select.
	ID string `json:"id"`
	// Version is the program's version.
	Version int `json:"version"`
	// Hash is the bound program's hash, lowercase hex. A `sha256:` prefix is
	// accepted and dropped.
	Hash string `json:"hash"`
}

// Decision is one version of a DDT Decision record. Versions accrue per DDT id:
// content that is the id's newest version is that version, and any other content
// is the next version, naming the one it supersedes — even content an older
// version held, so a revert is a change.
type Decision struct {
	// ID is this version's record id, `dec_` and 32 hex digits. The server's.
	ID string `json:"id"`
	// DecisionID is the DDT decision's id: the record's `id` member, trimmed —
	// the same reading ?decision= and a commitment's decisionId get. The server's.
	DecisionID string `json:"decisionId"`
	// Decision is the DDT-1.0 Decision record, stored verbatim. The server checks
	// that schemaVersion is "DDT-1.0" and that id is present. A version that
	// changes lifecycleState, agentPermissions, or any member the newest
	// version's agentPermissions.mayNotChange names is a person's to file: any
	// other caller is refused 403. Required.
	Decision json.RawMessage `json:"decision"`
	// Charter is the decision's charter, when one is filed with it.
	Charter *Charter `json:"charter,omitempty"`
	// Program is the Decision Program bound from the decision, when one is.
	Program *Program `json:"program,omitempty"`
	// Version is this version's ordinal for its DDT id: 1, 2, … The server's.
	Version int `json:"version"`
	// Supersedes is the record id of the version this one follows. Empty on the
	// first. The server's.
	Supersedes string `json:"supersedes,omitempty"`
	// Depends is every evidence uri the decision names — its `evidence` and each
	// assumption's `evidence` — once each, with a hash where the record gives one.
	// DERIVED by the server.
	Depends []Dependency `json:"depends"`
	// Time is when this version was filed, RFC 3339 UTC. The server's.
	Time string `json:"time"`
	// By is the user who filed it. The server's.
	By string `json:"by"`
	// Visibility is private, org or public. A write records private; a grant
	// (POST /v1/research/grants, kind decision, the DDT id) moves every version.
	Visibility string `json:"visibility"`
}

// Recommendation is what a Decision Package recommends, lifted from it: the part
// DDT's export leaves out.
type Recommendation struct {
	// Selection is the option the package selects. Empty when it selects none.
	Selection string `json:"selection,omitempty"`
	// Ranking is the package's ranking of the options, verbatim.
	Ranking json.RawMessage `json:"ranking,omitempty"`
	// Flips are the parameter values at which the top option changes, verbatim.
	Flips json.RawMessage `json:"flips,omitempty"`
	// Readiness is DDT's readiness result: the package's joined gate verdict on
	// the decision's class policy. Absent when the package has no verdict and the
	// Evaluation Run names no policy.
	Readiness *Readiness `json:"readiness,omitempty"`
}

// Readiness is DDT's readiness result, as Kai's export writes it into an
// Evaluation Run's outputs.readiness.
type Readiness struct {
	// DecisionClassPolicyID is the decision class policy ruled on, as the
	// Evaluation Run's outputs.readiness names it.
	DecisionClassPolicyID string `json:"decisionClassPolicyId,omitempty"`
	// Verdict is the join of the sealed package's gate verdicts: allow, ask or
	// deny. Empty when no gate rules.
	Verdict string `json:"verdict,omitempty"`
}

// Evaluation is one filing of a DDT Evaluation Run with the Decision Package it
// produced.
type Evaluation struct {
	// ID is this filing's record id, `eval_` and 32 hex digits, over the
	// Evaluation Run id and the filing's hash: the same run filed with other
	// content is another evaluation, and both are kept. The server's.
	ID string `json:"id"`
	// Hash is this filing's content hash, lowercase hex, over its decision id and
	// the canonical JSON of its Evaluation Run and package. A commitment resting
	// on the evaluation records it. The server's.
	Hash string `json:"hash"`
	// DecisionID is the DDT decision evaluated. Taken from the Evaluation Run's
	// decisionId when absent; the two must agree when both are sent.
	DecisionID string `json:"decisionId"`
	// EvaluationRun is the DDT Evaluation Run, verbatim: id, evaluator, inputs
	// (uri → hash) and outputs. Its id is required. A run is bound to one
	// package: every filing of one run id names the same outputs.hash (409
	// otherwise), and an id beginning RUN_KAI_ is Kai's name for that package,
	// RUN_KAI_ and the first 16 hex digits of outputs.hash, uppercased (422
	// otherwise). With a package its outputs.hash must be the package's hash, and
	// its inputs, when sent, exactly the evidence the package read.
	// outputs.readiness, when sent, is DDT's readiness result:
	// {decisionClassPolicyId: string, verdict: string or null}, its verdict the
	// package's.
	EvaluationRun json.RawMessage `json:"evaluationRun"`
	// Package is Kai's full Decision Package, verbatim. Its `hash` is required
	// and must be its content hash as Kai computes it: the SHA-256 of the
	// canonical JSON of every member but hash and trace. It is kept in the
	// hash-addressed blob store.
	Package json.RawMessage `json:"package,omitempty"`
	// Recommendation is lifted from Package: its selection, ranking, flips and
	// readiness. DERIVED by the server; absent without a package.
	Recommendation *Recommendation `json:"recommendation,omitempty"`
	// Depends is every uri the evaluation read, with the hash it read: the
	// package's evidence, or without a package the Evaluation Run's inputs.
	// DERIVED by the server: a depends sent with the record must name exactly
	// those, in any order.
	Depends []Dependency `json:"depends"`
	// Time is when this evaluation was filed, RFC 3339 UTC. The server's.
	Time string `json:"time"`
	// By is the user who filed it. The server's.
	By string `json:"by"`
	// Visibility is private, org or public. A write records private; a grant
	// (POST /v1/research/grants, kind evaluation, the Evaluation Run id) moves
	// every filing of it.
	Visibility string `json:"visibility"`
}

// Action is a DDT Action, verbatim, with who it is assigned to and whether a
// person has authorized it. Anyone files one, as a DRAFT; only a person moves it
// to AUTHORIZED. Never public.
type Action struct {
	// ID is the action's record id, `act_` and 32 hex digits, one per DDT Action
	// id. The server's.
	ID string `json:"id"`
	// Hash is this state's content hash, lowercase hex, over the DDT Action, the
	// assignee and the authorization. Authorizing names it, so a person
	// authorizes the state they read. The server's.
	Hash string `json:"hash"`
	// Action is the DDT Action, verbatim: id, decisionId, status, verdict, rule,
	// description. Its id is required; its status, when present, must be DRAFT;
	// and it carries no authorizedBy or authorizedAt, which are the server's.
	// Authorizing writes AUTHORIZED into it.
	Action json.RawMessage `json:"action"`
	// DecisionID is the DDT Action's decisionId. DERIVED by the server.
	DecisionID string `json:"decisionId"`
	// Assignee is who the action is assigned to.
	Assignee string `json:"assignee"`
	// Status is DRAFT until a person authorizes the action, then AUTHORIZED. The
	// server's: a write files DRAFT, and a DRAFT never replaces an AUTHORIZED
	// action.
	Status string `json:"status"`
	// AuthorizedBy is the person who authorized it. The server's.
	AuthorizedBy string `json:"authorizedBy,omitempty"`
	// AuthorizedAt is when it was authorized, RFC 3339 UTC. The server's.
	AuthorizedAt string `json:"authorizedAt,omitempty"`
	// Time is when this state of the action was filed, RFC 3339 UTC. The
	// server's.
	Time string `json:"time"`
	// By is the user who filed this state. The server's.
	By string `json:"by"`
}

// Commitment is DAOSoft's human commitment: a person commits a decision to an
// option. Never public.
type Commitment struct {
	// ID is the commitment's record id, `com_` and 32 hex digits. The server's.
	ID string `json:"id"`
	// DecisionID is the DDT decision committed. It must be filed. Required.
	DecisionID string `json:"decisionId"`
	// EvaluationID is the evaluation filing the commitment rests on, by its
	// record id. When sent it must be filed, for the same decision.
	EvaluationID string `json:"evaluationId,omitempty"`
	// EvaluationHash is that filing's hash, recorded by the server. When sent it
	// must be the named filing's (409 otherwise).
	EvaluationHash string `json:"evaluationHash,omitempty"`
	// Selection is the option committed to. Required.
	Selection string `json:"selection"`
	// Note is what the person wants on the record beside it.
	Note string `json:"note,omitempty"`
	// By is the person who committed. The server's.
	By string `json:"by"`
	// Time is when they committed, RFC 3339 UTC. The server's.
	Time string `json:"time"`
}

// Dependent is a decision or evaluation resting on a piece of evidence.
type Dependent struct {
	// ID is its record id: the decision version's, or the evaluation's.
	ID string `json:"id"`
	// DecisionID is the DDT decision it belongs to.
	DecisionID string `json:"decisionId"`
	// Hash is the version of the evidence it read, lowercase hex. Empty when it
	// names the uri and no version.
	Hash string `json:"hash,omitempty"`
	// Stale is true when Hash is not the uri's newest version.
	Stale bool `json:"stale"`
}

// Change is one uri a write moved to a new version.
type Change struct {
	// URI is the evidence that changed.
	URI string `json:"uri"`
	// From is the hash of the version replaced.
	From string `json:"from"`
	// To is the hash of the new version.
	To string `json:"to"`
}

// Reconsider is the event a write that files new versions publishes, once per
// write: which versions replaced which, and the decisions and evaluations now
// resting on an older one.
type Reconsider struct {
	// Org is the org whose evidence changed.
	Org string `json:"org"`
	// Changes are the uris the write moved, each from its replaced version to its
	// new one.
	Changes []Change `json:"changes"`
	// Decisions are the record ids of the latest version of each decision naming
	// a changed uri without its new hash.
	Decisions []string `json:"decisions"`
	// Evaluations are the record ids of the evaluations that read an older
	// version of a changed uri.
	Evaluations []string `json:"evaluations"`
}

// topic is where research.reconsider is published for org on the bus: the org's
// own subject, t-<org>-research.reconsider.
func topic(org string) string { return bus.Phys(org, "research.reconsider") }

// ── stored shapes ─────────────────────────────────────────────────────────────

type evidenceRecord struct {
	URI string `json:"uri"`
	// Version is the ordinal the write decided, inside the transaction that read
	// the uri's newest.
	Version    int    `json:"version"`
	Hash       string `json:"hash"`
	Seq        int64  `json:"seq"`
	Kind       string `json:"kind"`
	Source     Source `json:"source"`
	Links      []Link `json:"links"`
	Time       string `json:"time"`
	By         string `json:"by"`
	Visibility string `json:"visibility"`
}

func (r evidenceRecord) stable() string { return r.URI }
func (r evidenceRecord) key() string    { return "v:" + hashJoin(r.URI, strconv.Itoa(r.Version)) }
func (r evidenceRecord) clock() int64   { return r.Seq }
func (r evidenceRecord) gone() bool     { return false }
func (r evidenceRecord) id() string {
	return "ev_" + hashJoin(r.URI, strconv.Itoa(r.Version), r.Hash)[:32]
}

func (r evidenceRecord) wire() Evidence {
	return Evidence{
		ID: r.id(), URI: r.URI, Hash: r.Hash, Kind: r.Kind, Source: r.Source, Links: r.Links,
		Time: r.Time, Version: r.Version, By: r.By, Visibility: r.Visibility,
	}
}

type decisionRecord struct {
	DecisionID string `json:"decision_id"`
	// Version is the ordinal the write decided, inside the transaction that read
	// the decision's newest.
	Version     int             `json:"version"`
	ContentHash string          `json:"content_hash"`
	Seq         int64           `json:"seq"`
	Decision    json.RawMessage `json:"decision"`
	Charter     *Charter        `json:"charter,omitempty"`
	Program     *Program        `json:"program,omitempty"`
	Depends     []Dependency    `json:"depends"`
	Time        string          `json:"time"`
	By          string          `json:"by"`
	Visibility  string          `json:"visibility"`
}

func (r decisionRecord) stable() string { return r.DecisionID }
func (r decisionRecord) key() string    { return "d:" + hashJoin(r.DecisionID, strconv.Itoa(r.Version)) }
func (r decisionRecord) clock() int64   { return r.Seq }
func (r decisionRecord) gone() bool     { return false }
func (r decisionRecord) id() string {
	return "dec_" + hashJoin(r.DecisionID, strconv.Itoa(r.Version), r.ContentHash)[:32]
}

// An evaluation record is the filing's index: what it rests on and where its
// bodies are. The Evaluation Run and the package sit in the hash-addressed blob
// store, so a read of what rests on a uri decodes no package.
type evaluationRecord struct {
	RunID       string `json:"run_id"`
	DecisionID  string `json:"decision_id"`
	ContentHash string `json:"content_hash"`
	Seq         int64  `json:"seq"`
	// Seal is the package hash the run names, its outputs.hash; "" when it names
	// none. Every filing of one run id holds the same.
	Seal string `json:"seal"`
	// RunBlob and PackageBlob address the Evaluation Run's and the package's
	// bytes in the blob store; PackageBlob is "" without a package.
	RunBlob     string       `json:"run_blob"`
	PackageBlob string       `json:"package_blob,omitempty"`
	Depends     []Dependency `json:"depends"`
	Time        string       `json:"time"`
	By          string       `json:"by"`
	Visibility  string       `json:"visibility"`
}

func (r evaluationRecord) stable() string { return r.RunID }
func (r evaluationRecord) key() string    { return "w:" + hashJoin(r.RunID, r.ContentHash) }
func (r evaluationRecord) clock() int64   { return r.Seq }
func (r evaluationRecord) gone() bool     { return false }
func (r evaluationRecord) id() string     { return "eval_" + hashJoin(r.RunID, r.ContentHash)[:32] }

// evaluation is a filing as the wire answers it: its bodies read from the blob
// store, and the recommendation lifted from its package.
func (s *store) evaluation(ctx context.Context, r evaluationRecord) Evaluation {
	out := Evaluation{
		ID: r.id(), Hash: r.ContentHash, DecisionID: r.DecisionID, EvaluationRun: s.blob(ctx, r.RunBlob),
		Depends: r.Depends, Time: r.Time, By: r.By, Visibility: r.Visibility,
	}
	if r.PackageBlob == "" {
		return out
	}
	out.Package = s.blob(ctx, r.PackageBlob)
	var run, outputs, pkg map[string]json.RawMessage
	_ = json.Unmarshal(out.EvaluationRun, &run)
	_ = json.Unmarshal(run["outputs"], &outputs)
	_ = json.Unmarshal(out.Package, &pkg)
	readiness, _ := readinessOf(outputs)
	out.Recommendation = recommend(pkg, readiness)
	return out
}

type actionRecord struct {
	ActionID     string          `json:"action_id"`
	DecisionID   string          `json:"decision_id"`
	ContentHash  string          `json:"content_hash"`
	Seq          int64           `json:"seq"`
	Action       json.RawMessage `json:"action"`
	Assignee     string          `json:"assignee"`
	Status       string          `json:"status"`
	AuthorizedBy string          `json:"authorized_by"`
	AuthorizedAt string          `json:"authorized_at"`
	Time         string          `json:"time"`
	By           string          `json:"by"`
}

func (r actionRecord) stable() string { return r.ActionID }
func (r actionRecord) key() string    { return "y:" + hashJoin(r.ActionID, r.ContentHash) }
func (r actionRecord) clock() int64   { return r.Seq }
func (r actionRecord) gone() bool     { return false }
func (r actionRecord) id() string     { return "act_" + hashJoin(r.ActionID)[:32] }

// sealed sets the action's content hash over everything that makes this state of
// it: the DDT Action, the assignee and the authorization.
func (r *actionRecord) sealed() error {
	c, err := canon(r.Action)
	if err != nil {
		return err
	}
	r.ContentHash = hashJoin(string(c), r.Assignee, r.Status, r.AuthorizedBy, r.AuthorizedAt)
	return nil
}

func (r actionRecord) wire() Action {
	return Action{
		ID: r.id(), Hash: r.ContentHash, Action: r.Action, DecisionID: r.DecisionID, Assignee: r.Assignee,
		Status: r.Status, AuthorizedBy: r.AuthorizedBy, AuthorizedAt: r.AuthorizedAt, Time: r.Time, By: r.By,
	}
}

type commitmentRecord struct {
	DecisionID     string `json:"decision_id"`
	EvaluationID   string `json:"evaluation_id"`
	EvaluationHash string `json:"evaluation_hash"`
	Selection      string `json:"selection"`
	Note           string `json:"note"`
	By             string `json:"by"`
	Seq            int64  `json:"seq"`
	Time           string `json:"time"`
}

// A commitment is its own record: one person committing one decision to one
// option on one evaluation filing with one note is one commitment, so a retry
// files nothing new. The evaluation id already names its filing's content.
func (r commitmentRecord) stable() string { return r.key() }
func (r commitmentRecord) key() string {
	return "c:" + hashJoin(r.DecisionID, r.EvaluationID, r.Selection, r.Note, r.By)
}
func (r commitmentRecord) clock() int64 { return r.Seq }
func (r commitmentRecord) gone() bool   { return false }
func (r commitmentRecord) id() string {
	return "com_" + hashJoin(r.DecisionID, r.EvaluationID, r.Selection, r.Note, r.By)[:32]
}

func (r commitmentRecord) wire() Commitment {
	return Commitment{
		ID: r.id(), DecisionID: r.DecisionID, EvaluationID: r.EvaluationID, EvaluationHash: r.EvaluationHash,
		Selection: r.Selection, Note: r.Note, By: r.By, Time: r.Time,
	}
}

// ── canonical JSON and hashes ─────────────────────────────────────────────────

// canon is a JSON value in the canonical form DDT and Kai hash (hanzo-inc/kai
// program/src/canon.rs): no whitespace; object keys sorted by code point;
// strings escaped as serde_json escapes them; integers as themselves; every other
// number as the shortest decimal that reads back as the same float64, without
// an exponent, with -0 written 0.
func canon(raw []byte) ([]byte, error) {
	v, err := decode(raw)
	if err != nil {
		return nil, err
	}
	return encode(v)
}

// decode reads one JSON value with its numbers as written, refusing anything
// after it.
func decode(raw []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}

// encode is a decoded value written in canonical form.
func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := write(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// sealOf is a Decision Package's content hash as Kai computes it (hanzo-inc/kai
// program/src/package.rs, Package::content): the SHA-256 of the canonical JSON of
// every member but `hash` and `trace`, whose wall times and cache sources vary
// from run to run.
func sealOf(pkg json.RawMessage) (string, error) {
	v, err := decode(pkg)
	if err != nil {
		return "", err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return "", errors.New("not a JSON object")
	}
	delete(m, "hash")
	delete(m, "trace")
	c, err := encode(m)
	if err != nil {
		return "", err
	}
	return sha(c), nil
}

func write(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case json.Number:
		n, err := number(string(x))
		if err != nil {
			return err
		}
		b.WriteString(n)
	case string:
		escape(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := write(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			escape(b, k)
			b.WriteByte(':')
			if err := write(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	}
	return nil
}

// number is one JSON number as canon writes it: a literal with no fraction or
// exponent that fits 64 bits is an integer, and everything else a float64. A
// number past float64's range has no canonical form, as serde_json reads none.
func number(s string) (string, error) {
	if !strings.ContainsAny(s, ".eE") {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return strconv.FormatInt(i, 10), nil
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return strconv.FormatUint(u, 10), nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", errors.New("number out of range: " + s)
	}
	if f == 0 {
		return "0", nil
	}
	return strconv.FormatFloat(f, 'f', -1, 64), nil
}

// escape writes s as serde_json does: `"` and `\` escaped, the five named control
// characters by name, every other control character as \u00XX, and all else as
// its UTF-8.
func escape(b *bytes.Buffer, s string) {
	const hexits = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hexits[c>>4])
				b.WriteByte(hexits[c&0xf])
				continue
			}
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

// digest is a hash as an envelope carries it: lowercase hex with no `sha256:`
// prefix, the spelling DDT and Kai write.
func digest(h string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "sha256:")
}

// isDigest reports whether h is 64 lowercase hex digits.
func isDigest(h string) bool { return len(h) == 64 && isHex(h) }

// isHex reports whether s is lowercase hex digits, at least one.
func isHex(s string) bool {
	return s != "" && strings.Trim(s, "0123456789abcdef") == ""
}

// sha is the lowercase hex SHA-256 of b.
func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// object reads a JSON object's members, or refuses with what it is.
func object(raw json.RawMessage, what string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s must be a JSON object", what)
	}
	return m, nil
}

// named is an id as the thread files and finds it: trimmed, its case kept. A
// write reads a DDT id through it and every ?decision= read compares through it,
// so a listing finds exactly what the write filed.
func named(s string) string { return strings.TrimSpace(s) }

// text is a member read as a string through named, "" when absent or not one.
// The member is the one spelled k exactly.
func text(m map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(m[k], &s)
	return named(s)
}

// absent reports whether a raw JSON member is missing or null.
func absent(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// same reports whether two members hold the same JSON value, absent being a
// value of its own.
func same(a, b json.RawMessage) bool {
	if absent(a) || absent(b) {
		return absent(a) == absent(b)
	}
	ca, ea := canon(a)
	cb, eb := canon(b)
	return ea == nil && eb == nil && bytes.Equal(ca, cb)
}

// ── what only a person changes ────────────────────────────────────────────────

// guarded is what of a filed DDT Decision only a person may change: its
// lifecycleState, its agentPermissions (or an agent could lift its own limits
// one version ahead of using them), and every member its
// agentPermissions.mayNotChange names.
func guarded(dec map[string]json.RawMessage) []string {
	out := []string{"lifecycleState", "agentPermissions"}
	var perms map[string]json.RawMessage
	_ = json.Unmarshal(dec["agentPermissions"], &perms)
	var names []string
	_ = json.Unmarshal(perms["mayNotChange"], &names)
	for _, n := range names {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// changed is the first member guarded(was) names whose value now differs, or "".
func changed(was, now map[string]json.RawMessage) string {
	for _, k := range guarded(was) {
		if !same(was[k], now[k]) {
			return k
		}
	}
	return ""
}

// withStatus is a DDT record with its status member set, the rest as it was.
func withStatus(raw json.RawMessage, status string) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["status"], _ = json.Marshal(status)
	return json.Marshal(m)
}

// ── derived dependencies ──────────────────────────────────────────────────────

// dependsOf is every evidence uri a DDT Decision names — `evidence` and each
// assumption's `evidence` — once each, in the order named, keeping a hash where
// an entry is an object carrying one.
func dependsOf(dec map[string]json.RawMessage) []Dependency {
	var out []Dependency
	at := map[string]int{}
	add := func(raw json.RawMessage) {
		var entries []json.RawMessage
		if json.Unmarshal(raw, &entries) != nil {
			entries = []json.RawMessage{raw}
		}
		for _, e := range entries {
			var d Dependency
			if json.Unmarshal(e, &d.URI) != nil {
				var named struct{ URI, Hash string }
				if json.Unmarshal(e, &named) != nil {
					continue
				}
				d = Dependency{URI: named.URI, Hash: digest(named.Hash)}
			}
			if d.URI = strings.TrimSpace(d.URI); d.URI == "" {
				continue
			}
			if i, ok := at[d.URI]; ok {
				if out[i].Hash == "" {
					out[i].Hash = d.Hash
				}
				continue
			}
			at[d.URI] = len(out)
			out = append(out, d)
		}
	}
	add(dec["evidence"])
	var assumptions []map[string]json.RawMessage
	_ = json.Unmarshal(dec["assumptions"], &assumptions)
	for _, a := range assumptions {
		add(a["evidence"])
	}
	return out
}

// readOf is every uri an Evaluation Run read, with the hash it read, from its
// inputs (uri → hash), ordered by uri.
func readOf(run map[string]json.RawMessage) []Dependency {
	var inputs map[string]string
	_ = json.Unmarshal(run["inputs"], &inputs)
	return sorted(inputs)
}

// readBy is every uri a Decision Package read, with the hash it read, from its
// evidence ({uri, hash, …} each), ordered by uri: what Kai's export writes as the
// Evaluation Run's inputs. Evidence of another shape, or one uri read at two
// hashes, is refused 422.
func readBy(pkg map[string]json.RawMessage) ([]Dependency, error) {
	var evidence []map[string]json.RawMessage
	if !absent(pkg["evidence"]) && json.Unmarshal(pkg["evidence"], &evidence) != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "package.evidence is a list of {uri, hash}")
	}
	read := make(map[string]string, len(evidence))
	for _, e := range evidence {
		uri, h := text(e, "uri"), digest(text(e, "hash"))
		if was, ok := read[uri]; ok && was != h {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "the package reads %s at two hashes", uri)
		}
		read[uri] = h
	}
	return sorted(read), nil
}

// readinessOf is an Evaluation Run's outputs.readiness, nil when it has none:
// DDT's readiness result, {decisionClassPolicyId: string, verdict: string or
// null}, Kai writing null when no gate rules. Any other shape is refused 422, so
// a verdict is never stated in a form the check against the package skips.
func readinessOf(outputs map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	raw := outputs["readiness"]
	if absent(raw) {
		return nil, nil
	}
	var r map[string]json.RawMessage
	var s string
	if json.Unmarshal(raw, &r) != nil || r == nil ||
		absent(r["decisionClassPolicyId"]) || json.Unmarshal(r["decisionClassPolicyId"], &s) != nil ||
		!absent(r["verdict"]) && json.Unmarshal(r["verdict"], &s) != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, `evaluationRun.outputs.readiness is DDT's readiness result: {"decisionClassPolicyId": string, "verdict": string or null}`)
	}
	return r, nil
}

// sorted is uri → hash as dependencies, envelope-spelled and ordered by uri.
func sorted(read map[string]string) []Dependency {
	out := make([]Dependency, 0, len(read))
	for uri, h := range read {
		if uri = strings.TrimSpace(uri); uri != "" {
			out = append(out, Dependency{URI: uri, Hash: digest(h)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URI < out[j].URI })
	return out
}

// recommend lifts a Decision Package's recommendation as Kai's DDT export does:
// selection, ranking, flips, and readiness — the package's joined gate verdict on
// the policy the Evaluation Run's outputs.readiness names.
func recommend(pkg, readiness map[string]json.RawMessage) *Recommendation {
	r := &Recommendation{Selection: text(pkg, "selection")}
	if v, p := text(pkg, "verdict"), text(readiness, "decisionClassPolicyId"); v != "" || p != "" {
		r.Readiness = &Readiness{DecisionClassPolicyID: p, Verdict: v}
	}
	if !absent(pkg["ranking"]) {
		r.Ranking = pkg["ranking"]
	}
	if !absent(pkg["flips"]) {
		r.Flips = pkg["flips"]
	}
	return r
}

// ── store ─────────────────────────────────────────────────────────────────────

// putBlobs writes content to the hash-addressed blob keyspace the diary's
// artifacts use. The address is the hash of the bytes, so a write that finds the
// address held finds the same bytes, and a blob is written before any record
// names it.
func (s *store) putBlobs(ctx context.Context, blobs map[string][]byte) error {
	if len(blobs) == 0 {
		return nil
	}
	return s.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error { return putBlobsTx(ctx, tx, blobs) })
}

// putBlobsTx is putBlobs inside a transaction the caller holds, so a record and
// the blobs it names are written together or not at all.
func putBlobsTx(ctx context.Context, tx orm.DB, blobs map[string][]byte) error {
	for h, b := range blobs {
		if _, err := tx.CreateIfAbsent(ctx, tx.NewKey(kindArtBlob, artBlobKeyID(h), 0, nil), &artBlob{Content: b}); err != nil {
			return err
		}
	}
	return nil
}

// blob is the content held under a hash, or nil.
func (s *store) blob(ctx context.Context, h string) json.RawMessage {
	var b artBlob
	if s.db.Get(ctx, s.db.NewKey(kindArtBlob, artBlobKeyID(h), 0, nil), &b) != nil || !json.Valid(b.Content) {
		return nil
	}
	return b.Content
}

// byClock orders versions oldest first, by the append clock.
func byClock[T version](recs []T) {
	sort.Slice(recs, func(i, j int) bool { return recs[i].clock() < recs[j].clock() })
}

// evidence is every version of every uri, grouped by uri, oldest first.
func (s *store) evidence(ctx context.Context) (map[string][]evidenceRecord, error) {
	recs, err := loadAll[evidenceRecord](ctx, s, kindEvidence)
	if err != nil {
		return nil, err
	}
	return byURI(recs), nil
}

// byURI groups evidence versions by uri, each uri's oldest first.
func byURI(recs []evidenceRecord) map[string][]evidenceRecord {
	out := map[string][]evidenceRecord{}
	for _, r := range recs {
		out[r.URI] = append(out[r.URI], r)
	}
	for _, vs := range out {
		sort.Slice(vs, func(i, j int) bool { return vs[i].Version < vs[j].Version })
	}
	return out
}

// decisions is every version of every decision, newest first, each with its
// ordinal and the version it supersedes.
func (s *store) decisions(ctx context.Context) ([]Decision, error) {
	recs, err := loadAll[decisionRecord](ctx, s, kindDecision)
	if err != nil {
		return nil, err
	}
	byClock(recs)
	prev := map[string]string{}
	out := make([]Decision, 0, len(recs))
	for _, r := range recs {
		out = append(out, Decision{
			ID: r.id(), DecisionID: r.DecisionID, Decision: r.Decision, Charter: r.Charter, Program: r.Program,
			Version: r.Version, Supersedes: prev[r.DecisionID], Depends: r.Depends,
			Time: r.Time, By: r.By, Visibility: r.Visibility,
		})
		prev[r.DecisionID] = r.id()
	}
	slices.Reverse(out)
	return out, nil
}

// newest is each record's newest version — the canonical fold — newest first.
func newest[T version](recs []T) []T {
	out := make([]T, 0, len(recs))
	for _, r := range canonical(recs) {
		out = append(out, r)
	}
	byClock(out)
	slices.Reverse(out)
	return out
}

// latest is newest over every version of kind the store holds.
func latest[T version](ctx context.Context, s *store, kind string) ([]T, error) {
	recs, err := loadAll[T](ctx, s, kind)
	if err != nil {
		return nil, err
	}
	return newest(recs), nil
}

// rests reports the dependency ds holds on uri, read at hash when one is given.
func rests(ds []Dependency, uri, hash string) (Dependency, bool) {
	for _, d := range ds {
		if d.URI == uri && (hash == "" || d.Hash == hash) {
			return d, true
		}
	}
	return Dependency{}, false
}

// dependents is the decisions (latest version of each) and evaluations (latest
// filing of each run) whose depends names uri, with hash when one is given, and
// the uri's newest hash.
func (s *store) dependents(ctx context.Context, uri, hash string) (top string, decs, evals []Dependent, err error) {
	ev, err := s.evidence(ctx)
	if err != nil {
		return "", nil, nil, err
	}
	if vs := ev[uri]; len(vs) > 0 {
		top = vs[len(vs)-1].Hash
	}
	stale := func(h string) bool { return h != "" && top != "" && h != top }
	ds, err := latest[decisionRecord](ctx, s, kindDecision)
	if err != nil {
		return "", nil, nil, err
	}
	for _, r := range ds {
		if d, ok := rests(r.Depends, uri, hash); ok {
			decs = append(decs, Dependent{ID: r.id(), DecisionID: r.DecisionID, Hash: d.Hash, Stale: stale(d.Hash)})
		}
	}
	es, err := latest[evaluationRecord](ctx, s, kindEvaluation)
	if err != nil {
		return "", nil, nil, err
	}
	for _, r := range es {
		if d, ok := rests(r.Depends, uri, hash); ok {
			evals = append(evals, Dependent{ID: r.id(), DecisionID: r.DecisionID, Hash: d.Hash, Stale: stale(d.Hash)})
		}
	}
	return top, decs, evals, nil
}

// threadHolding counts, per thread kind, the versions whose stable id is id — a
// DDT decision id, an Evaluation Run id, an evidence uri — inside setGrant's
// transaction.
func threadHolding(ctx context.Context, tx orm.DB, id string) (map[string]int, error) {
	out := map[string]int{}
	var err error
	if out["decision"], err = holding[decisionRecord](ctx, tx, kindDecision, id, nil); err != nil {
		return nil, err
	}
	if out["evaluation"], err = holding[evaluationRecord](ctx, tx, kindEvaluation, id, nil); err != nil {
		return nil, err
	}
	out["evidence"], err = holding[evidenceRecord](ctx, tx, kindEvidence, id, nil)
	return out, err
}

// grantThread sets the visibility of every version of the one thread kind's id,
// inside setGrant's transaction. These kinds are org-wide, so the grant's project
// does not narrow them.
func grantThread(ctx context.Context, tx orm.DB, kind, id, vis string) (int, error) {
	switch kind {
	case "decision":
		return holding(ctx, tx, kindDecision, id, func(r *decisionRecord) { r.Visibility = vis })
	case "evaluation":
		return holding(ctx, tx, kindEvaluation, id, func(r *evaluationRecord) { r.Visibility = vis })
	default:
		return holding(ctx, tx, kindEvidence, id, func(r *evidenceRecord) { r.Visibility = vis })
	}
}

// holding counts the versions of kind whose stable id is id, and with set writes
// each back changed by it, in place: visibility is not content, so a version keeps
// its key.
func holding[T version](ctx context.Context, tx orm.DB, kind, id string, set func(*T)) (int, error) {
	var recs []T
	if _, err := tx.Query(kind).GetAll(ctx, &recs); err != nil {
		return 0, err
	}
	n := 0
	for i := range recs {
		r := recs[i]
		if r.stable() != id {
			continue
		}
		n++
		if set == nil {
			continue
		}
		set(&r)
		if _, err := tx.Put(ctx, tx.NewKey(kind, r.key(), 0, nil), &r); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// ── the surface ───────────────────────────────────────────────────────────────

// threadRoutes mounts the decision thread under research's own /v1/research:
// evidence, then the decision kinds. Dependents is registered before {id}, which
// would otherwise take its address. The group is made here, where zipdoc reads
// its prefix.
func threadRoutes(app cloud.Router, o ops) {
	g := app.Group("/v1/research")
	g.Post("/evidence", o.postEvidence)
	g.Post("/evidence/sysml", o.postSysML)
	g.Get("/evidence", o.listEvidence)
	g.Get("/evidence/dependents", o.dependents)
	g.Get("/evidence/:id", o.getEvidence)
	g.Post("/decisions", o.postDecision)
	g.Get("/decisions", o.listDecisions)
	g.Post("/evaluations", o.postEvaluation)
	g.Get("/evaluations", o.listEvaluations)
	g.Post("/actions", o.postAction)
	g.Get("/actions", o.listActions)
	g.Post("/actions/:id/authorize", o.authorize)
	g.Post("/commitments", o.postCommitment)
	g.Get("/commitments", o.listCommitments)
}

// now is the time a write files, RFC 3339 UTC.
func now() string { return time.Now().UTC().Format(time.RFC3339) }

// by is the user a write is filed by: the validated caller's.
func by(ctx context.Context) string { return strings.TrimSpace(cloud.Who(ctx).User) }

// signed is the person a human-only act is made by: someone signed in with their
// own token, acting in their own org (principal.Signed). An API key, an
// application, an agent acting for a person, a support session and a SuperAdmin
// switched into another org are refused 403.
func signed(ctx context.Context) (principal.Principal, error) {
	if c, ok := cloud.Request(ctx); ok {
		if p, ok := principal.Signed(c); ok {
			return p, nil
		}
	}
	return principal.Principal{}, zip.ErrForbidden("only a person signed in as themselves, in their own org, may do this: an API key, an application or an agent may not")
}

// inTx runs fn in one write transaction, the read it decides on and the write
// together. A refusal fn returns (a *zip.HTTPError) is answered as it is and
// writes nothing; any other error is the store's, answered 500 naming what.
func (o ops) inTx(ctx context.Context, st *store, org, what string, fn func(orm.DB) error) error {
	var refused error
	err := st.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
		refused = nil
		err := fn(tx)
		if _, ok := errors.AsType[*zip.HTTPError](err); ok {
			refused = err
		}
		return err
	})
	if refused != nil {
		return refused
	}
	if err != nil {
		return o.failed(what, org, err)
	}
	return nil
}

// reader is the store a read answers from, and whether it answers the public
// read: a caller with an org reads its own; a caller with none that names an org
// in ?org= reads that org's, its public records alone.
func (o ops) reader(ctx context.Context, org string) (*store, bool, error) {
	if _, ok := principal.OrgFrom(ctx); !ok && org != "" {
		st, ok, err := published(o.s.State.stores, org)
		if err != nil {
			o.s.Log.Error("research store open failed", "org", org, "err", err)
			return nil, false, zip.Errorf(http.StatusInternalServerError, "research store unavailable")
		}
		if !ok {
			return nil, false, missing()
		}
		return st, true, nil
	}
	st, _, err := o.orgStore(ctx)
	return st, false, err
}

// public keeps what the org granted public, and answers the 404 an unserved
// address gets until it has granted something — so a stranger's read never
// tells which orgs keep a decision thread.
func public[T any](rs []T, vis func(T) string) ([]T, error) {
	var out []T
	for _, r := range rs {
		if vis(r) == "public" {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, missing()
	}
	return out, nil
}

// failed logs a store failure and answers 500 naming the act.
func (o ops) failed(what, org string, err error) error {
	o.s.Log.Error("research "+what+" failed", "org", org, "err", err)
	return zip.Errorf(http.StatusInternalServerError, "research %s failed", what)
}

// evidenceOut is a set of evidence versions.
type evidenceOut struct {
	// Data are the versions, newest first. Content is left out; read one by id
	// for it.
	Data []Evidence `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// FileEvidence files one version of a piece of evidence with its content. The
// server hashes the content over its canonical JSON and keeps it in the
// hash-addressed blob store; a hash sent beside it must agree, and evidence
// without content is refused. Content whose hash is the uri's newest is that
// version, answered as it stands; any other content is the uri's next version —
// a revert to an older version's content too — and the decisions and evaluations
// resting on the version it replaces are named on the bus as research.reconsider.
// The answer is the version, with its ordinal for the uri and its content.
//
// Example: {"uri": "techdata://sph/tsb/22", "kind": "document", "source": {"system": "techdata", "revision": "A"}, "content": {"bulletin": "TSB-22", "endurance_h": 8.1}}
func (o ops) postEvidence(ctx context.Context, in *Evidence) (*Evidence, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	rec := evidenceRecord{
		URI: strings.TrimSpace(in.URI), Kind: in.Kind, Source: in.Source, Links: in.Links,
		Time: now(), By: by(ctx), Visibility: "private",
	}
	if rec.URI == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "evidence needs a uri")
	}
	if !evidenceKinds[rec.Kind] {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "kind must be requirement, test, model, analysis, document or element")
	}
	for _, l := range rec.Links {
		if !rels[l.Rel] || strings.TrimSpace(l.URI) == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "a link names a uri and a rel: satisfies, verifies, derives, supersedes, refines or dependsOn")
		}
	}
	if absent(in.Content) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "evidence carries its content: the server files what it hashed")
	}
	c, err := canon(in.Content)
	if err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "content is not JSON: %v", err)
	}
	if len(c) > maxArtifactBytes {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "evidence content too large")
	}
	rec.Hash = sha(c)
	if h := digest(in.Hash); h != "" && h != rec.Hash {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "hash does not match the content (the content hashes to %s)", rec.Hash)
	}
	got, err := o.file(ctx, st, org, []evidenceRecord{rec}, map[string][]byte{rec.Hash: c})
	if err != nil {
		return nil, err
	}
	out := got[0]
	out.Content = c
	return &out, nil
}

// file appends evidence versions with their content in one write transaction that
// reads each uri's newest: a record whose hash is its uri's newest is that version;
// any other is the uri's next version, and a change when the uri had one. The
// changes of one write are published as one research.reconsider. The cost is one
// read of the org's evidence and one of its decisions and evaluations, whatever
// the batch holds.
func (o ops) file(ctx context.Context, st *store, org string, recs []evidenceRecord, blobs map[string][]byte) ([]Evidence, error) {
	if err := st.putBlobs(ctx, blobs); err != nil {
		return nil, o.failed("evidence write", org, err)
	}
	out := make([]Evidence, len(recs))
	var changes []Change
	err := o.inTx(ctx, st, org, "evidence write", func(tx orm.DB) error {
		changes = nil
		var held []evidenceRecord
		if _, err := tx.Query(kindEvidence).GetAll(ctx, &held); err != nil {
			return err
		}
		top := map[string]evidenceRecord{}
		for _, r := range held {
			if r.Version > top[r.URI].Version {
				top[r.URI] = r
			}
		}
		var fresh []evidenceRecord
		var at []int
		for i, r := range recs {
			cur, had := top[r.URI]
			if had && cur.Hash == r.Hash {
				out[i] = cur.wire()
				continue
			}
			r.Version = cur.Version + 1
			top[r.URI] = r
			fresh, at = append(fresh, r), append(at, i)
			if had {
				changes = append(changes, Change{URI: r.URI, From: cur.Hash, To: r.Hash})
			}
		}
		made, err := appendTx(ctx, tx, kindEvidence, fresh, func(r *evidenceRecord, seq int64) { r.Seq = seq })
		if err != nil {
			return err
		}
		for j, i := range at {
			if !made[j] {
				return errors.New("a version decided in this transaction was already held")
			}
			out[i] = fresh[j].wire()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	if len(changes) > 0 {
		o.reconsider(ctx, st, org, changes)
	}
	return out, nil
}

// reconsider publishes one research.reconsider for the changes of one write: the
// latest version of each decision naming a changed uri without its new hash, and
// the latest filing of each run that read an older version of one. Best effort,
// after the write is durable: a bus that is down loses the event, and GET
// /v1/research/evidence/dependents still answers stale for every one of them.
func (o ops) reconsider(ctx context.Context, st *store, org string, changes []Change) {
	to := make(map[string]string, len(changes))
	for _, c := range changes {
		to[c.URI] = c.To
	}
	ev := Reconsider{Org: org, Changes: changes, Decisions: []string{}, Evaluations: []string{}}
	ds, err := latest[decisionRecord](ctx, st, kindDecision)
	if err != nil {
		o.s.Log.Warn("research reconsider: decisions unread", "org", org, "err", err)
		return
	}
	for _, d := range ds {
		if slices.ContainsFunc(d.Depends, func(x Dependency) bool { h, ok := to[x.URI]; return ok && x.Hash != h }) {
			ev.Decisions = append(ev.Decisions, d.id())
		}
	}
	es, err := latest[evaluationRecord](ctx, st, kindEvaluation)
	if err != nil {
		o.s.Log.Warn("research reconsider: evaluations unread", "org", org, "err", err)
		return
	}
	for _, e := range es {
		if slices.ContainsFunc(e.Depends, func(x Dependency) bool { h, ok := to[x.URI]; return ok && x.Hash != "" && x.Hash != h }) {
			ev.Evaluations = append(ev.Evaluations, e.id())
		}
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if err := bus.Publish(topic(org), b); err != nil {
		o.s.Log.Warn("research reconsider: not published", "org", org, "changes", len(changes), "err", err)
		return
	}
}

// sysmlIn is a SysML v2 model's elements, as the SysML v2 API serves them.
type sysmlIn struct {
	// Project is the SysML project the elements belong to, without `@` or `#`,
	// which delimit an element's uri. Required.
	Project string `json:"project"`
	// Commit is the project commit they were read at: lowercase hex, no prefix.
	// Required.
	Commit string `json:"commit"`
	// Elements are SysML v2 API JSON elements: each has `@id` and `@type`, and a
	// relationship has `source`/`target` or `relatedElement`, arrays of
	// {"@id"}. At most 20000, counting with the elements every link a Satisfy,
	// Verify, Derive, Refine or Dependency relationship names: each of its
	// sources to each of its targets.
	Elements []json.RawMessage `json:"elements"`
}

// element is what an import reads of a SysML v2 API element.
type element struct {
	ID      string `json:"@id"`
	Type    string `json:"@type"`
	Source  []ref  `json:"source"`
	Target  []ref  `json:"target"`
	Related []ref  `json:"relatedElement"`
}

type ref struct {
	ID string `json:"@id"`
}

// relationship reports whether an element is a relationship: one that names
// what it relates.
func (e element) relationship() bool { return e.Source != nil || e.Target != nil || e.Related != nil }

// ends are a relationship's sources and targets: source/target, else
// relatedElement's first and the rest.
func (e element) ends() (from, to []ref) {
	if len(e.Source) > 0 || len(e.Target) > 0 {
		return e.Source, e.Target
	}
	if len(e.Related) > 1 {
		return e.Related[:1], e.Related[1:]
	}
	return nil, nil
}

// relOf is the link a SysML relationship type files: Satisfy*, Verify* (and
// RequirementVerificationMembership, the metaclass SysML v2 writes a verify
// as), Derive*, Refine* and Dependency.
func relOf(t string) (string, bool) {
	switch {
	case strings.HasPrefix(t, "Satisfy"):
		return "satisfies", true
	case strings.HasPrefix(t, "Verify"), t == "RequirementVerificationMembership":
		return "verifies", true
	case strings.HasPrefix(t, "Derive"):
		return "derives", true
	case strings.HasPrefix(t, "Refine"):
		return "refines", true
	case t == "Dependency":
		return "dependsOn", true
	}
	return "", false
}

// FileSysML files a SysML v2 model's elements as evidence, in one write. Each
// element that is not a relationship becomes one `element` record, uri
// sysml://<project>@<commit>#<@id>, hashed over its canonical JSON, the element
// itself its content. A Satisfy, Verify, Derive, Refine or Dependency
// relationship becomes a link on its source element's record (satisfies,
// verifies, derives, refines, dependsOn) to its target's uri; other
// relationships file nothing. The project may not hold `@` or `#` and the commit
// is lowercase hex, so a uri names one element of one import. Every element that
// changed publishes, together, one research.reconsider. The answer is the
// records filed, without content.
//
// Example: {"project": "ltv", "commit": "c0ffee", "elements": [{"@id": "e1", "@type": "RequirementUsage", "declaredName": "payload"}, {"@id": "e2", "@type": "PartUsage", "declaredName": "diesel"}, {"@id": "r1", "@type": "SatisfyRequirementUsage", "source": [{"@id": "e2"}], "target": [{"@id": "e1"}]}]}
func (o ops) postSysML(ctx context.Context, in *sysmlIn) (*evidenceOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	project, commit := strings.TrimSpace(in.Project), strings.TrimSpace(in.Commit)
	if project == "" || commit == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "a SysML import names its project and commit")
	}
	if strings.ContainsAny(project, "@#") {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "a SysML project may not hold @ or #: they delimit an element's uri")
	}
	if !isHex(commit) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "a SysML commit is lowercase hex, without a prefix")
	}
	if err := bounded(len(in.Elements), in.Elements); err != nil {
		return nil, err
	}
	uri := func(id string) string { return "sysml://" + project + "@" + commit + "#" + id }
	var recs []evidenceRecord
	at := map[string]int{}
	blobs := map[string][]byte{}
	var links []element
	t, who := now(), by(ctx)
	for i, raw := range in.Elements {
		var e element
		if json.Unmarshal(raw, &e) != nil || strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.Type) == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "element %d needs an @id and an @type", i)
		}
		if e.relationship() {
			links = append(links, e)
			continue
		}
		if _, dup := at[e.ID]; dup {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "element %q appears twice", e.ID)
		}
		c, err := canon(raw)
		if err != nil {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "element %d is not JSON: %v", i, err)
		}
		h := sha(c)
		blobs[h] = c
		at[e.ID] = len(recs)
		recs = append(recs, evidenceRecord{
			URI: uri(e.ID), Hash: h, Kind: "element", Source: Source{System: "sysml", Revision: commit},
			Links: []Link{}, Time: t, By: who, Visibility: "private",
		})
	}
	// Every link a relationship names, each source to each target, counts against
	// the bound before one is made.
	n := len(in.Elements)
	for _, e := range links {
		if _, ok := relOf(e.Type); ok {
			from, to := e.ends()
			if n += len(from) * len(to); n > maxBatchItems {
				break
			}
		}
	}
	if err := bounded(n); err != nil {
		return nil, err
	}
	type linkOn struct {
		at   int
		link Link
	}
	made := map[linkOn]bool{}
	for _, e := range links {
		rel, ok := relOf(e.Type)
		if !ok {
			continue
		}
		from, to := e.ends()
		for _, f := range from {
			i, ok := at[f.ID]
			if !ok {
				continue
			}
			for _, x := range to {
				if l := (linkOn{i, Link{Rel: rel, URI: uri(x.ID)}}); x.ID != "" && !made[l] {
					made[l] = true
					recs[i].Links = append(recs[i].Links, l.link)
				}
			}
		}
	}
	if len(recs) == 0 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "the elements hold nothing but relationships")
	}
	got, err := o.file(ctx, st, org, recs, blobs)
	if err != nil {
		return nil, err
	}
	return &evidenceOut{Data: got, Total: len(got)}, nil
}

// evidenceIn narrows an evidence read. Both are query parameters.
type evidenceIn struct {
	// URI narrows to one uri's versions, newest first. Without it the answer is
	// every uri's newest version.
	URI string `json:"uri"`
	// Org is the org whose public evidence a caller with no org of its own reads.
	// A caller with an org reads its own, and this is not consulted.
	Org string `json:"org"`
}

// ListEvidence answers one uri's versions, newest first (?uri=), or every uri's
// newest version, without content. A caller with no org of its own reads the
// evidence the org ?org= names granted public, and the 404 an unserved address
// gets until that org has granted some; a version filed after a grant is private
// until it is granted too.
func (o ops) listEvidence(ctx context.Context, in *evidenceIn) (*evidenceOut, error) {
	st, open, err := o.reader(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	ev, err := st.evidence(ctx)
	if err != nil {
		return nil, o.failed("evidence read", in.Org, err)
	}
	var all, picked []evidenceRecord
	for uri, vs := range ev {
		all = append(all, vs...)
		for i, v := range vs {
			if uri == in.URI || in.URI == "" && i == len(vs)-1 {
				picked = append(picked, v)
			}
		}
	}
	if open {
		if _, err := public(all, func(v evidenceRecord) string { return v.Visibility }); err != nil {
			return nil, err
		}
		picked = slices.DeleteFunc(picked, func(v evidenceRecord) bool { return v.Visibility != "public" })
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].Seq > picked[j].Seq })
	out := make([]Evidence, len(picked))
	for i, v := range picked {
		out[i] = v.wire()
	}
	return &evidenceOut{Data: out, Total: len(out)}, nil
}

// idIn names one record by its record id, in the path.
type idIn struct {
	// ID is the record id.
	ID string `path:"id"`
}

// GetEvidence answers one version of a piece of evidence by its record id, with
// its content.
func (o ops) getEvidence(ctx context.Context, in *idIn) (*Evidence, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := loadAll[evidenceRecord](ctx, st, kindEvidence)
	if err != nil {
		return nil, o.failed("evidence read", org, err)
	}
	for _, v := range recs {
		if v.id() == in.ID {
			out := v.wire()
			out.Content = st.blob(ctx, v.Hash)
			return &out, nil
		}
	}
	return nil, zip.ErrNotFound("no evidence " + strconv.Quote(in.ID))
}

// dependentsIn names the evidence asked about. Both are query parameters.
type dependentsIn struct {
	// URI is the evidence. Required.
	URI string `json:"uri"`
	// Hash narrows to the records that read this version, lowercase hex.
	Hash string `json:"hash"`
}

// dependentsOut is what rests on one piece of evidence.
type dependentsOut struct {
	// URI is the evidence asked about.
	URI string `json:"uri"`
	// Newest is the hash of its newest version. Empty when none is filed.
	Newest string `json:"newest,omitempty"`
	// Decisions are the decisions whose depends names the uri, the latest version
	// of each.
	Decisions []Dependent `json:"decisions"`
	// Evaluations are the evaluations whose depends names it, the latest filing
	// of each Evaluation Run.
	Evaluations []Dependent `json:"evaluations"`
}

// ListDependents answers the decisions and evaluations whose depends names a
// uri (?hash= narrows to those that read that version), each with the hash it
// read and stale: true when that hash is not the uri's newest version. A
// decision names a uri without a version unless its DDT record gives one, so it
// is never stale by hash; its evaluations are.
func (o ops) dependents(ctx context.Context, in *dependentsIn) (*dependentsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	uri := strings.TrimSpace(in.URI)
	if uri == "" {
		return nil, zip.ErrBadRequest("dependents names the evidence: ?uri=")
	}
	top, decs, evals, err := st.dependents(ctx, uri, digest(in.Hash))
	if err != nil {
		return nil, o.failed("dependents read", org, err)
	}
	return &dependentsOut{URI: uri, Newest: top, Decisions: orEmpty(decs), Evaluations: orEmpty(evals)}, nil
}

// orEmpty answers an empty list as [] rather than null.
func orEmpty[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

// decisionsOut is a decision listing.
type decisionsOut struct {
	// Data are the decision versions, newest first.
	Data []Decision `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// FileDecision files a DDT-1.0 Decision record, with its charter and the program
// bound from it when there are. The server checks the record's schemaVersion and
// id, and derives depends from every evidence uri it names. Content that is the
// decision id's newest version answers that version; any other content files its
// next version, which names the one it supersedes — a revert to an older
// version's content too, decided in one transaction against the newest, as
// evidence versions are. A version that changes
// the filed record's lifecycleState, its agentPermissions, or a member its
// agentPermissions.mayNotChange names is a person's to file: an API key, an
// application or an agent is refused 403, as DDT's agentPermissions say.
//
// Example: {"decision": {"id": "DEC-SPH-0412", "schemaVersion": "DDT-1.0", "lifecycleState": "IN_ANALYSIS", "evidence": ["techdata://sph/tsb/22"]}, "charter": {"question": "Which auxiliary power source?"}, "program": {"id": "apu.select", "version": 1, "hash": "0b0e88445da82d8e86cd5d40f243fbfa981be4f486f9769789c102d9955d8baf"}}
func (o ops) postDecision(ctx context.Context, in *Decision) (*Decision, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	dec, err := object(in.Decision, "decision")
	if err != nil {
		return nil, err
	}
	id := text(dec, "id")
	if text(dec, "schemaVersion") != "DDT-1.0" || id == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, `decision must be a DDT-1.0 Decision: schemaVersion "DDT-1.0" and an id`)
	}
	if in.Charter != nil && strings.TrimSpace(in.Charter.Question) == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "a charter names its question")
	}
	if in.Program != nil {
		in.Program.Hash = digest(in.Program.Hash)
	}
	c, err := canon(in.Decision)
	if err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "decision is not JSON: %v", err)
	}
	rec := decisionRecord{
		DecisionID: id, Decision: in.Decision, Charter: in.Charter, Program: in.Program,
		Depends: dependsOf(dec), Time: now(), By: by(ctx), Visibility: "private",
	}
	rec.ContentHash = hashJoin(string(c), marshal(rec.Charter), marshal(rec.Program))
	_, agent := signed(ctx)
	var filed string
	err = o.inTx(ctx, st, org, "decision write", func(tx orm.DB) error {
		var recs []decisionRecord
		if _, err := tx.Query(kindDecision).GetAll(ctx, &recs); err != nil {
			return err
		}
		var top decisionRecord
		for _, r := range recs {
			if r.DecisionID == id && r.Version > top.Version {
				top = r
			}
		}
		if top.Version > 0 && top.ContentHash == rec.ContentHash {
			filed = top.id()
			return nil
		}
		if agent != nil && top.Version > 0 {
			prev, err := object(top.Decision, "the filed decision")
			if err != nil {
				return err
			}
			if k := changed(prev, dec); k != "" {
				return zip.ErrForbidden("only a person signed in as themselves may change " + k + " of decision " + strconv.Quote(id) + ": DDT's agentPermissions keep it from agents")
			}
		}
		next := rec
		next.Version = top.Version + 1
		made, err := appendTx(ctx, tx, kindDecision, []decisionRecord{next}, func(r *decisionRecord, seq int64) { r.Seq = seq })
		if err != nil {
			return err
		}
		if !made[0] {
			return errors.New("a version decided in this transaction was already held")
		}
		filed = next.id()
		return nil
	})
	if err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	ds, err := st.decisions(ctx)
	if err != nil {
		return nil, o.failed("decision read", org, err)
	}
	for _, d := range ds {
		if d.ID == filed {
			return &d, nil
		}
	}
	return nil, o.failed("decision read", org, errors.New("a filed version is not in the store"))
}

// decisionsIn narrows a decision listing. All are query parameters.
type decisionsIn struct {
	// ID narrows to one version, by its record id.
	ID string `json:"id"`
	// Decision narrows to one DDT decision's versions, by its DDT id, read as the
	// write reads it: trimmed, its case kept.
	Decision string `json:"decision"`
	// Org is the org whose public decisions a caller with no org of its own
	// reads. A caller with an org reads its own, and this is not consulted.
	Org string `json:"org"`
}

// ListDecisions answers every version of the org's decisions, newest first, each
// with its ordinal and the version it supersedes. ?decision= narrows to one DDT
// decision and ?id= to one version. A caller with no org of its own reads the
// decisions the org ?org= names granted public, and the 404 an unserved address
// gets until that org has granted one.
func (o ops) listDecisions(ctx context.Context, in *decisionsIn) (*decisionsOut, error) {
	st, open, err := o.reader(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	ds, err := st.decisions(ctx)
	if err != nil {
		return nil, o.failed("decision read", in.Org, err)
	}
	if open {
		if ds, err = public(ds, func(d Decision) string { return d.Visibility }); err != nil {
			return nil, err
		}
	}
	want := named(in.Decision)
	out := []Decision{}
	for _, d := range ds {
		if (in.ID == "" || d.ID == in.ID) && (want == "" || d.DecisionID == want) {
			out = append(out, d)
		}
	}
	return &decisionsOut{Data: out, Total: len(out)}, nil
}

// evaluationsOut is an evaluation listing.
type evaluationsOut struct {
	// Data are the evaluations, newest first.
	Data []Evaluation `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// FileEvaluation files a DDT Evaluation Run with the Decision Package Kai
// produced. The server checks that the run has an id; with a package, that the
// package's hash is its content hash as Kai computes it (every member but hash
// and trace), that the run's outputs.hash is that hash, so the package filed is
// the one Kai sealed, that the run's inputs, when sent, are what the package
// read, and that the run's outputs.readiness states the package's verdict. A run
// is bound to one package: a run id beginning RUN_KAI_ is Kai's name for its
// package's hash, and a filing naming another package hash under a run id already
// filed is refused 409 — a new package is a new run. It lifts the recommendation
// as Kai's DDT export does: the package's selection, ranking and flips, and
// readiness, its verdict on the policy outputs.readiness names; depends is every
// uri the package read (the run's inputs without one), with the hash it read. The
// same run filed with the same content is the evaluation filed; with other
// content it is another evaluation, and the earlier one is kept and readable by
// its id.
//
// Example: {"evaluationRun": {"id": "RUN_KAI_08B02A2DE43C89D7", "decisionId": "DEC-SPH-0412", "inputs": {"techdata://sph/tsb/22": "sha256:31513dddf264c7a242a5e1e32979a6e6d293cc9c579d3f21a4bd42e527a907ab"}, "outputs": {"hash": "sha256:08b02a2de43c89d728db50d8db720e04d618259c1ac926e78a38e7fc6abc9e43"}}}
func (o ops) postEvaluation(ctx context.Context, in *Evaluation) (*Evaluation, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	run, err := object(in.EvaluationRun, "evaluationRun")
	if err != nil {
		return nil, err
	}
	runID := text(run, "id")
	if runID == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "evaluationRun needs its id")
	}
	decID, decNamed := named(in.DecisionID), text(run, "decisionId")
	switch {
	case decID == "":
		decID = decNamed
	case decNamed != "" && decNamed != decID:
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "decisionId %q is not the evaluationRun's %q", decID, decNamed)
	}
	if decID == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "an evaluation names its decisionId")
	}
	rc, err := canon(in.EvaluationRun)
	if err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "evaluationRun is not JSON: %v", err)
	}
	var outputs map[string]json.RawMessage
	_ = json.Unmarshal(run["outputs"], &outputs)
	readiness, err := readinessOf(outputs)
	if err != nil {
		return nil, err
	}
	rec := evaluationRecord{
		RunID: runID, DecisionID: decID, Seal: digest(text(outputs, "hash")), RunBlob: sha(in.EvaluationRun),
		Depends: readOf(run), Time: now(), By: by(ctx), Visibility: "private",
	}
	blobs := map[string][]byte{rec.RunBlob: in.EvaluationRun}
	var pc []byte
	if !absent(in.Package) {
		pkg, err := object(in.Package, "package")
		if err != nil {
			return nil, err
		}
		own := digest(text(pkg, "hash"))
		if own == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "a package carries its hash: Kai's, over every member but hash and trace")
		}
		seal, err := sealOf(in.Package)
		if err != nil {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "package is not JSON: %v", err)
		}
		if seal != own {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "the package's hash %s is not its content's %s: a package is filed as Kai sealed it", own, seal)
		}
		if rec.Seal != seal {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "evaluationRun.outputs.hash %q is not the package's hash %s", rec.Seal, seal)
		}
		if !absent(pkg["verdict"]) && text(pkg, "verdict") == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "package.verdict is allow, ask, deny or null")
		}
		if said, sealed := text(readiness, "verdict"), text(pkg, "verdict"); readiness != nil && said != sealed {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "evaluationRun.outputs.readiness.verdict %q is not the package's verdict %q", said, sealed)
		}
		read, err := readBy(pkg)
		if err != nil {
			return nil, err
		}
		if !absent(run["inputs"]) && !slices.Equal(rec.Depends, read) {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "evaluationRun.inputs are not the evidence the package read: Kai's export writes the package's evidence, uri to hash")
		}
		if pc, err = canon(in.Package); err != nil {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "package is not JSON: %v", err)
		}
		rec.Depends, rec.PackageBlob = read, sha(in.Package)
		blobs[rec.PackageBlob] = in.Package
	}
	if strings.HasPrefix(runID, kaiRun) && (len(rec.Seal) < 16 || runID != kaiRun+strings.ToUpper(rec.Seal[:16])) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "evaluation run %q is not Kai's: Kai names a run %s and the first 16 hex digits of its package's hash, uppercased (outputs.hash %q)", runID, kaiRun, rec.Seal)
	}
	if len(in.Depends) > 0 {
		sent := make(map[string]string, len(in.Depends))
		for _, d := range in.Depends {
			sent[d.URI] = d.Hash
		}
		if s := sorted(sent); len(s) != len(in.Depends) || !slices.Equal(s, rec.Depends) {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "depends must be what the evaluation read (the package's evidence, else evaluationRun.inputs), each uri with its hash; the server derives it")
		}
	}
	rec.ContentHash = hashJoin(decID, string(rc), string(pc))
	err = o.inTx(ctx, st, org, "evaluation write", func(tx orm.DB) error {
		var held []evaluationRecord
		if _, err := tx.Query(kindEvaluation).GetAll(ctx, &held); err != nil {
			return err
		}
		// A verified package's hash binds the run; until one is filed, the first
		// stated hash does, and a verified package outranks it.
		verified := ""
		for _, e := range held {
			if e.RunID == runID && e.PackageBlob != "" {
				verified = e.Seal
			}
		}
		for _, e := range held {
			if e.RunID != runID {
				continue
			}
			if e.DecisionID != rec.DecisionID {
				return zip.ErrConflict("evaluation run " + strconv.Quote(runID) + " belongs to decision " + strconv.Quote(e.DecisionID) + ", not " + strconv.Quote(rec.DecisionID))
			}
			bound := e.Seal
			if verified != "" {
				bound = verified
			} else if rec.PackageBlob != "" {
				bound = rec.Seal
			}
			if bound != rec.Seal {
				return zip.ErrConflict("evaluation run " + strconv.Quote(runID) + " is bound to package " + strconv.Quote(bound) + ", not " + strconv.Quote(rec.Seal) + ": a new package is a new run")
			}
		}
		made, err := appendTx(ctx, tx, kindEvaluation, []evaluationRecord{rec}, func(r *evaluationRecord, seq int64) { r.Seq = seq })
		if err != nil || !made[0] {
			return err
		}
		return putBlobsTx(ctx, tx, blobs)
	})
	if err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	var got evaluationRecord
	if err := st.db.Get(ctx, st.db.NewKey(kindEvaluation, rec.key(), 0, nil), &got); err != nil {
		return nil, o.failed("evaluation read", org, err)
	}
	out := st.evaluation(ctx, got)
	return &out, nil
}

// evaluationsIn narrows an evaluation listing. All are query parameters.
type evaluationsIn struct {
	// ID narrows to one evaluation filing, by its record id — a filing a later
	// one of the same run replaced too.
	ID string `json:"id"`
	// Decision narrows to one DDT decision's evaluations, read as the write reads
	// a decision id: trimmed, its case kept.
	Decision string `json:"decision"`
	// Org is the org whose public evaluations a caller with no org of its own
	// reads. A caller with an org reads its own, and this is not consulted.
	Org string `json:"org"`
}

// ListEvaluations answers the org's evaluations, newest first, the latest filing
// of each Evaluation Run. ?decision= narrows to one DDT decision, and ?id= reads
// one filing, whether or not a later filing of its run replaced it. A caller with
// no org of its own reads the evaluations the org ?org= names granted public, and
// the 404 an unserved address gets until that org has granted one.
func (o ops) listEvaluations(ctx context.Context, in *evaluationsIn) (*evaluationsOut, error) {
	st, open, err := o.reader(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	all, err := loadAll[evaluationRecord](ctx, st, kindEvaluation)
	if err != nil {
		return nil, o.failed("evaluation read", in.Org, err)
	}
	es := newest(all)
	if in.ID != "" {
		es = slices.DeleteFunc(all, func(e evaluationRecord) bool { return e.id() != in.ID })
	}
	if open {
		if es, err = public(es, func(e evaluationRecord) string { return e.Visibility }); err != nil {
			return nil, err
		}
	}
	want := named(in.Decision)
	out := []Evaluation{}
	for _, e := range es {
		if want == "" || e.DecisionID == want {
			out = append(out, st.evaluation(ctx, e))
		}
	}
	return &evaluationsOut{Data: out, Total: len(out)}, nil
}

// actionsOut is an action listing.
type actionsOut struct {
	// Data are the actions, newest first, each in its latest state.
	Data []Action `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// FileAction files a DDT Action as a DRAFT, with who it is assigned to. Anyone
// may: Kai, an agent, a person. The DDT Action's own status, when it has one, is
// DRAFT, and it carries no authorizedBy or authorizedAt (422); an action is
// authorized by a person, never filed so. Filing it again
// with other content or another assignee files its next state, with a new hash;
// an action a person has authorized is not replaced by a draft (409).
//
// Example: {"action": {"id": "RUN_KAI_08B02A2DE43C89D7/anchor", "decisionId": "DEC-SPH-0412", "status": "DRAFT", "verdict": "ask", "rule": "kai.anchor", "description": "Does the charter favor one option before the options were measured?"}, "assignee": "ada@army.mil"}
func (o ops) postAction(ctx context.Context, in *Action) (*Action, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	act, err := object(in.Action, "action")
	if err != nil {
		return nil, err
	}
	if !absent(act["status"]) && text(act, "status") != draft {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "an action is filed as a DRAFT: a person authorizes it, through its authorize")
	}
	for _, k := range []string{"authorizedBy", "authorizedAt"} {
		if _, ok := act[k]; ok {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "a draft carries no %s: who authorized an action, and when, is the server's, written when a person authorizes it", k)
		}
	}
	rec := actionRecord{
		ActionID: text(act, "id"), DecisionID: text(act, "decisionId"), Action: in.Action,
		Assignee: strings.TrimSpace(in.Assignee), Status: draft, Time: now(), By: by(ctx),
	}
	if rec.ActionID == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "action needs its id")
	}
	if err := rec.sealed(); err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "action is not JSON: %v", err)
	}
	err = o.inTx(ctx, st, org, "action write", func(tx orm.DB) error {
		cur, ok, err := actionIn(ctx, tx, rec.id())
		if err != nil {
			return err
		}
		if ok && cur.Status == authorized {
			return zip.ErrConflict("action " + strconv.Quote(rec.ActionID) + " is authorized; a draft does not replace it")
		}
		_, err = appendTx(ctx, tx, kindAction, []actionRecord{rec}, func(r *actionRecord, seq int64) { r.Seq = seq })
		return err
	})
	if err != nil {
		return nil, err
	}
	return o.answerAction(ctx, st, org, rec.id())
}

// actionIn is one action's latest state, by record id, read inside tx.
func actionIn(ctx context.Context, tx orm.DB, id string) (actionRecord, bool, error) {
	var recs []actionRecord
	if _, err := tx.Query(kindAction).GetAll(ctx, &recs); err != nil {
		return actionRecord{}, false, err
	}
	for _, a := range canonical(recs) {
		if a.id() == id {
			return a, true, nil
		}
	}
	return actionRecord{}, false, nil
}

// answerAction ships the write and answers the action's latest state.
func (o ops) answerAction(ctx context.Context, st *store, org, id string) (*Action, error) {
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	cur, ok, err := st.action(ctx, id)
	if err != nil || !ok {
		return nil, o.failed("action read", org, err)
	}
	out := cur.wire()
	return &out, nil
}

// action is one action's latest state, by record id.
func (s *store) action(ctx context.Context, id string) (actionRecord, bool, error) {
	as, err := latest[actionRecord](ctx, s, kindAction)
	if err != nil {
		return actionRecord{}, false, err
	}
	for _, a := range as {
		if a.id() == id {
			return a, true, nil
		}
	}
	return actionRecord{}, false, nil
}

// actionsIn narrows an action listing. All are query parameters.
type actionsIn struct {
	// ID narrows to one action, by its record id.
	ID string `json:"id"`
	// Decision narrows to one DDT decision's actions, read as the write reads a
	// decision id: trimmed, its case kept.
	Decision string `json:"decision"`
	// Status narrows to DRAFT or AUTHORIZED.
	Status string `json:"status"`
}

// ListActions answers the org's actions, newest first, each in its latest
// state with its hash. ?decision=, ?status= and ?id= narrow it. Actions are never
// public.
func (o ops) listActions(ctx context.Context, in *actionsIn) (*actionsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	as, err := latest[actionRecord](ctx, st, kindAction)
	if err != nil {
		return nil, o.failed("action read", org, err)
	}
	want := named(in.Decision)
	out := []Action{}
	for _, a := range as {
		if (in.ID == "" || a.id() == in.ID) && (want == "" || a.DecisionID == want) && (in.Status == "" || a.Status == in.Status) {
			out = append(out, a.wire())
		}
	}
	return &actionsOut{Data: out, Total: len(out)}, nil
}

// authorizeIn names the action a person authorizes and the state of it they read.
type authorizeIn struct {
	// ID is the action's record id, in the path.
	ID string `path:"id"`
	// Hash is the hash of the state the person read: the action's `hash` as a
	// listing answered it. Required.
	Hash string `json:"hash"`
}

// AuthorizeAction moves an action to AUTHORIZED, the state the person read: the
// request names that state's hash, and an action that has changed since — refiled
// by an agent, or authorized by someone else — is refused 409. The read and the
// write are one transaction, so of two people authorizing one draft at once one
// is answered and the other refused 409. It writes AUTHORIZED into the stored DDT
// Action and stamps the person who authorized it and when. Only a person signed
// in with their own token, in their own org, may: an API key, an application or
// an agent acting for someone is refused 403. The draft it replaces is kept.
//
// Example: {"hash": "5b0e1f0c2a8d4e6f9a1b3c5d7e9f1a2b5b0e1f0c2a8d4e6f9a1b3c5d7e9f1a2b"}
func (o ops) authorize(ctx context.Context, in *authorizeIn) (*Action, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	p, err := signed(ctx)
	if err != nil {
		return nil, err
	}
	read := digest(in.Hash)
	if !isDigest(read) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "authorize names the hash of the action state it approves, 64 hex digits")
	}
	err = o.inTx(ctx, st, org, "action write", func(tx orm.DB) error {
		cur, ok, err := actionIn(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		if !ok {
			return zip.ErrNotFound("no action " + strconv.Quote(in.ID))
		}
		if cur.Status == authorized {
			return zip.ErrConflict("action " + strconv.Quote(cur.ActionID) + " is already authorized by " + cur.AuthorizedBy)
		}
		if cur.ContentHash != read {
			return zip.ErrConflict("action " + strconv.Quote(cur.ActionID) + " has changed since it was read: its hash is now " + cur.ContentHash)
		}
		t := now()
		next := cur
		if next.Action, err = withStatus(cur.Action, authorized); err != nil {
			return err
		}
		next.Status, next.AuthorizedBy, next.AuthorizedAt, next.Time, next.By = authorized, p.User, t, t, p.User
		if err := next.sealed(); err != nil {
			return err
		}
		_, err = appendTx(ctx, tx, kindAction, []actionRecord{next}, func(r *actionRecord, seq int64) { r.Seq = seq })
		return err
	})
	if err != nil {
		return nil, err
	}
	return o.answerAction(ctx, st, org, in.ID)
}

// commitmentsOut is a commitment listing.
type commitmentsOut struct {
	// Data are the commitments, newest first.
	Data []Commitment `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// Commit records a person's commitment of a decision to an option, resting on
// an evaluation filing when one is named; the server records that filing's hash,
// and the filing stays readable by its id whatever is filed after it. Only a
// person signed in with their own token, in their own org, may: an API key, an
// application or an agent acting for someone is refused 403, as DDT's
// agentPermissions.mayNotCreate DecisionCommitment says. The decision must be
// filed, and a named evaluation filed for it.
//
// Example: {"decisionId": "DEC-SPH-0412", "evaluationId": "eval_5b0e1f0c2a8d4e6f9a1b3c5d7e9f1a2b", "selection": "A2", "note": "Battery-only meets silent watch with the lowest sustainment burden."}
func (o ops) postCommitment(ctx context.Context, in *Commitment) (*Commitment, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := holdable(in); err != nil {
		return nil, err
	}
	p, err := signed(ctx)
	if err != nil {
		return nil, err
	}
	rec := commitmentRecord{
		DecisionID: named(in.DecisionID), EvaluationID: strings.TrimSpace(in.EvaluationID),
		Selection: strings.TrimSpace(in.Selection), Note: in.Note, By: p.User, Time: now(),
	}
	if rec.DecisionID == "" || rec.Selection == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "a commitment names its decisionId and selection")
	}
	sent := digest(in.EvaluationHash)
	if sent != "" && rec.EvaluationID == "" {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "evaluationHash is the hash of the evaluation named by evaluationId")
	}
	ds, err := latest[decisionRecord](ctx, st, kindDecision)
	if err != nil {
		return nil, o.failed("decision read", org, err)
	}
	if !slices.ContainsFunc(ds, func(d decisionRecord) bool { return d.DecisionID == rec.DecisionID }) {
		return nil, zip.ErrNotFound("no decision " + strconv.Quote(rec.DecisionID))
	}
	if rec.EvaluationID != "" {
		es, err := loadAll[evaluationRecord](ctx, st, kindEvaluation)
		if err != nil {
			return nil, o.failed("evaluation read", org, err)
		}
		i := slices.IndexFunc(es, func(e evaluationRecord) bool {
			return e.id() == rec.EvaluationID && e.DecisionID == rec.DecisionID
		})
		if i < 0 {
			return nil, zip.ErrNotFound("no evaluation " + strconv.Quote(rec.EvaluationID) + " of decision " + strconv.Quote(rec.DecisionID))
		}
		if rec.EvaluationHash = es[i].ContentHash; sent != "" && sent != rec.EvaluationHash {
			return nil, zip.ErrConflict("evaluation " + strconv.Quote(rec.EvaluationID) + " is hashed " + rec.EvaluationHash + ", not " + sent)
		}
	}
	if _, err := putAll(ctx, st, kindCommitment, []commitmentRecord{rec}, func(r *commitmentRecord, seq int64) { r.Seq = seq }); err != nil {
		return nil, o.failed("commitment write", org, err)
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	var got commitmentRecord
	if err := st.db.Get(ctx, st.db.NewKey(kindCommitment, rec.key(), 0, nil), &got); err != nil {
		return nil, o.failed("commitment read", org, err)
	}
	out := got.wire()
	return &out, nil
}

// commitmentsIn narrows a commitment listing. It is a query parameter.
type commitmentsIn struct {
	// Decision narrows to one DDT decision's commitments, read as the write reads
	// a decision id: trimmed, its case kept.
	Decision string `json:"decision"`
}

// ListCommitments answers the org's commitments, newest first. ?decision=
// narrows to one decision. Commitments are never public.
func (o ops) listCommitments(ctx context.Context, in *commitmentsIn) (*commitmentsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := latest[commitmentRecord](ctx, st, kindCommitment)
	if err != nil {
		return nil, o.failed("commitment read", org, err)
	}
	want := named(in.Decision)
	out := []Commitment{}
	for _, c := range cs {
		if want == "" || c.DecisionID == want {
			out = append(out, c.wire())
		}
	}
	return &commitmentsOut{Data: out, Total: len(out)}, nil
}
