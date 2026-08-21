// Package lsp is live semantic code intelligence — definitions, references,
// types, hover, outline and diagnostics — over a repository AND its resolved
// dependencies, with no toolchain on the caller's machine.
//
// # Where it sits
//
// At /v1/lsp, beside code rather than inside it. code and lsp are two reads of
// ONE repository: code is lexical, symbolic and semantic search — fast,
// approximate, always available — and lsp is a real language server — exact,
// typed, and able to follow a symbol out of the repository and into a
// dependency. An agent searches with code and is certain with lsp. Two reads of
// one thing are two capabilities, and each answers at the address its own name
// spells; they are cross-referenced siblings, which is the link a reader
// actually follows.
//
// # What this package is
//
// A PROXY. The language servers run in hanzoai/lsp, a jailed daemon on its own
// deployment, because answering a cross-dependency question means running a
// third-party toolchain over untrusted bytes (see daemon.go). This side owns the
// three things the daemon must never hold: the tenant, the repository and the
// ledger.
//
//   - TENANT. Every request resolves its org from the validated principal, and
//     that org is the daemon's isolation key. A caller supplies a repo SLUG,
//     never an owner and never a URL, so there is no input from which one tenant
//     could name another tenant's repository.
//   - REPOSITORY. The revision and the tree come from the forge, read AS THE
//     CALLER, so the forge's own ACL decides which repositories answer. The
//     daemon holds no git credential — one that could fetch any repository is
//     exactly what must not exist next to an unjailed compiler — so the tree is
//     pushed to it, never pulled by it.
//   - LEDGER. The gate runs before the work and the debit after it (meter.go).
//
// # Positions are the LSP's, not a translation of them
//
// line and character are 0-BASED, and character counts UTF-16 code units, per the
// LSP specification. That is deliberately not the 1-based line an editor shows a
// human: these callers are agents and editors that already speak LSP, and a
// service that silently re-based positions would corrupt every multi-byte line —
// an emoji before the cursor is one UTF-16 unit in the protocol's arithmetic and
// two in Go's. Positions pass through untouched, so the protocol's answer is the
// answer.
package lsp

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/forge"
	"github.com/zap-proto/zip"
)

// Query is one position in one file of one repository — the value every op here
// takes, because every op here is one question about one position.
type Query struct {
	// Repo is the repository NAME within the caller's own org, e.g. "cloud".
	// Not a URL and not an owner/name pair: the owner is the validated
	// principal's org, so this names a repository the caller already owns.
	Repo string `json:"repo"`

	// Rev is a branch, tag or commit sha. Empty means the default branch. It is
	// resolved to a commit before anything else happens, so an answer is always
	// about one immutable tree.
	Rev string `json:"rev,omitempty"`

	// Path is the repo-relative file, e.g. "apps/lsp/lsp.go".
	Path string `json:"path"`

	// Line is 0-based, per the LSP specification.
	Line int `json:"line"`

	// Character is a 0-based UTF-16 code-unit offset within Line, per the LSP
	// specification — not a byte offset and not a rune index.
	Character int `json:"character"`

	// Relation refines locate: definition, reference, type or implementation.
	// Empty means definition. Every other op ignores it.
	Relation string `json:"relation,omitempty"`
}

// Answer carries whichever result the op produces. Exactly one result field is
// populated, so a client reads the field its op names and never discriminates a
// union.
type Answer struct {
	Op   string `json:"op"`
	Lang string `json:"lang"`
	Repo string `json:"repo"`
	Rev  string `json:"rev"`
	Path string `json:"path"`

	// Cold reports that this request paid to PREPARE the revision — the tree
	// write, the dependency fetch and the language server's first index. It is
	// the billed event, surfaced so a caller can see what it was charged for.
	Cold bool `json:"cold"`

	Locations   []Location   `json:"locations,omitempty"`
	Hover       string       `json:"hover,omitempty"`
	Symbols     []Symbol     `json:"symbols,omitempty"`
	Completions []Completion `json:"completions,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Location is one place an answer resolved to. External false means Path is
// repo-relative; true means the answer left the repository and Path is the module
// coordinate it landed in ("golang.org/x/mod@v0.14.0/semver/semver.go"), which is
// the whole reason this service exists.
type Location struct {
	Path     string `json:"path"`
	External bool   `json:"external,omitempty"`
	Range    Range  `json:"range"`
}

// Position is the LSP's: 0-based line, 0-based UTF-16 character.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a half-open span between two positions.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Symbol is one entry in a file's outline.
type Symbol struct {
	Name   string `json:"name"`
	Kind   int    `json:"kind"`
	Detail string `json:"detail,omitempty"`
	Range  Range  `json:"range"`
}

// Completion is one candidate at a position.
type Completion struct {
	Label  string `json:"label"`
	Kind   int    `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Diagnostic is one problem the server reported. Severity is the LSP's: 1 error,
// 2 warning, 3 information, 4 hint.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// The two shapes a caller may name, narrowed HERE so a malformed one costs no
// socket. slug is a repository name under an owner; ref is a branch, tag or sha
// whose leading character is alphanumeric, which is what stops it being read as a
// flag by anything downstream that shells out.
var (
	slug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)
	ref  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,199}$`)
)

// relations is the CLOSED set locate refines by. The door names what it serves,
// so an unknown string is a 400 here rather than an arbitrary method handed to a
// language server.
var relations = []string{"definition", "reference", "type", "implementation"}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/lsp describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// hover renders the type and documentation of the symbol at a position, as the
// language server itself renders it.
//
// Positions are the LSP's: line and character are 0-BASED and character counts
// UTF-16 code units, so an editor's 1-based line must have 1 subtracted before it
// is sent. The repository is named by slug and is always one in the caller's own
// org; rev pins a branch, tag or commit sha, and empty means the default branch.
//
// Example: {"repo":"cloud","path":"apps/lsp/lsp.go","line":120,"character":18}
func (s *state) hover(ctx context.Context, in *Query) (*Answer, error) {
	return s.query(ctx, in, "hover")
}

// locate finds where a symbol lives: its definition, its references, its type or
// its implementations, chosen by relation (definition, reference, type,
// implementation — empty means definition).
//
// It resolves THROUGH dependencies. An answer whose external flag is set left the
// repository, and its path is then the module coordinate it landed in — which is
// the question a static index cannot answer and this service exists for.
//
// Example: {"repo":"cloud","path":"apps/lsp/lsp.go","line":120,"character":18,"relation":"definition"}
func (s *state) locate(ctx context.Context, in *Query) (*Answer, error) {
	return s.query(ctx, in, "locate")
}

// symbols outlines one file: every declaration in it, with its kind and its span.
// The position is ignored — the answer is the whole file.
//
// Example: {"repo":"cloud","path":"apps/lsp/lsp.go"}
func (s *state) symbols(ctx context.Context, in *Query) (*Answer, error) {
	return s.query(ctx, in, "symbols")
}

// diagnostics reports every problem the language server finds in one file —
// compile errors, type errors and lints, each with its span and its severity (1
// error, 2 warning, 3 information, 4 hint). The position is ignored.
//
// Example: {"repo":"cloud","path":"apps/lsp/lsp.go"}
func (s *state) diagnostics(ctx context.Context, in *Query) (*Answer, error) {
	return s.query(ctx, in, "diagnostics")
}

// complete offers the candidates a language server has at a position, typed and
// resolved through the repository's dependencies rather than guessed from text.
//
// Example: {"repo":"cloud","path":"apps/lsp/lsp.go","line":120,"character":18}
func (s *state) complete(ctx context.Context, in *Query) (*Answer, error) {
	return s.query(ctx, in, "complete")
}

// query is the ONE path every op takes, in the order that order matters:
//
//	principal → repository → price → resolve → ask → (prepare → ask) → debit
//
// The gate is before the resolve so an out-of-funds caller is refused rather than
// served work nobody can be billed for; the debit is after the answer so nothing
// is charged for work that failed.
func (s *state) query(ctx context.Context, in *Query, op string) (*Answer, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	repo := strings.TrimSpace(in.Repo)
	if !slug.MatchString(repo) {
		return nil, zip.ErrBadRequest("repo must be a repository name in your org")
	}
	want := strings.TrimSpace(in.Rev)
	if want != "" && !ref.MatchString(want) {
		return nil, zip.ErrBadRequest("rev must be a branch, tag or commit sha")
	}
	path := strings.TrimSpace(in.Path)
	if path == "" {
		return nil, zip.ErrBadRequest("path is required")
	}
	if in.Line < 0 || in.Character < 0 {
		return nil, zip.ErrBadRequest("line and character are 0-based and cannot be negative")
	}
	relation := ""
	if op == "locate" {
		if relation = strings.TrimSpace(in.Relation); relation == "" {
			relation = "definition"
		}
		if !slices.Contains(relations, relation) {
			return nil, zip.ErrBadRequest("relation must be one of " + strings.Join(relations, ", "))
		}
	}

	// MONEY GATE — before the first byte of work, at the PREPARE price, because
	// whether this revision is already prepared is not known until the daemon is
	// asked and is not a fact about the caller.
	c, onHTTP := cloud.Request(ctx)
	if onHTTP {
		if err := s.gate(ctx, c, org); err != nil {
			return nil, err
		}
	}

	// The commit, from the forge, for the caller's own org. The daemon keys a
	// root by a RESOLVED sha and refuses anything else — which is what makes a
	// root immutable, and therefore what removes cache invalidation from the
	// whole service: a branch moves, a commit never does.
	sha, err := s.rev(ctx, c, org, repo, want)
	if err != nil {
		return nil, err
	}

	q := &question{
		Org: org, Repo: repo, Rev: sha,
		Op: op, Relation: relation,
		Path: path, Line: in.Line, Character: in.Character,
	}
	out := &Answer{Repo: repo, Rev: sha, Path: path}

	err = s.daemon.ask(ctx, q, out)
	if errors.Is(err, errNeedTree) {
		// The daemon holds no root for this commit and cannot go and get one. Send
		// the tree, then ask again — ONCE. A second 409 is the daemon evicting a
		// root as fast as this fills it, and retrying that is a loop, not a fix.
		if err = s.prepare(ctx, c, org, repo, sha, out); err != nil {
			return nil, err
		}
		err = s.daemon.ask(ctx, q, out)
	}
	if err != nil {
		if decided(err) {
			return nil, err
		}
		s.Log.Warn("lsp query failed", "org", org, "repo", repo, "op", op, "err", err)
		return nil, zip.ErrInternal("language server did not answer")
	}

	if onHTTP {
		s.charge(c, org, out.Cold)
	}
	return out, nil
}

// coldSlots bounds how many cold prepares run at once. A prepare holds the whole
// tree resident several times over — the forge bytes, the string copy files()
// makes, and the JSON body the daemon encodes — all live until daemon.root
// returns. unpack bounds ONE tree (maxTree); nothing bounded the PROCESS, so a
// caller firing N hovers at N distinct cold shas — each a fresh root, since the
// daemon keys roots by resolved sha — pulled N whole trees into memory at once
// and turned a per-hover charge into an OOM. This is that ceiling. A warm root
// never reaches prepare (query asks the daemon first), so this gates only the
// slow, rare cold path: serializing it a few wide costs a little tail latency and
// buys a hard bound on resident memory.
var coldSlots = make(chan struct{}, 4)

// prepare hands the daemon the tree for one commit and records on out whether
// that call actually built it.
func (s *state) prepare(ctx context.Context, c *zip.Ctx, org, repo, sha string, out *Answer) error {
	// Hold a slot for the whole memory-resident window — the read AND the handoff
	// to the daemon, since the tree stays live until root() returns. A caller that
	// hangs up while waiting releases its place rather than pinning one.
	select {
	case coldSlots <- struct{}{}:
		defer func() { <-coldSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	files, err := s.files(ctx, c, org, repo, sha)
	if err != nil {
		return err
	}
	got, err := s.daemon.root(ctx, &tree{Org: org, Repo: repo, Rev: sha, Files: files})
	if err != nil {
		if decided(err) {
			return err
		}
		s.Log.Warn("lsp prepare failed", "org", org, "repo", repo, "rev", sha, "err", err)
		return zip.ErrInternal("language server did not accept the tree")
	}
	out.Cold = got.Cold
	return nil
}

// decided reports whether err already carries the status and message a client
// should see. Anything else is this deployment's problem and not the caller's, so
// it is logged where it happened and answered generically.
func decided(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he)
}

// ── the forge ────────────────────────────────────────────────────────────────

// The two reads this package makes of the repository, held in variables for the
// one thing a variable buys here: a test can drive a query without a forge
// behind it. Neither is ever reassigned in production — the only writer is a
// test, and the compiler holds each stand-in to the same signature.
//
// They are two calls rather than one because they cost differently. resolveRev
// is a ref lookup and runs on EVERY request; readTree is the whole repository
// and runs only when the daemon says it holds no root. Folding them into one
// would drag a monorepo across the wire to answer a hover.
var (
	resolveRev = forgeRev
	readTree   = forgeTree
)

// reader is the forge, scoped to the CALLER and pointed at their own namespace.
//
// AS THE HUMAN, never as the machine, and that is the whole tenancy story here.
// The repository is named by a slug the caller sent, and the machine credential
// is a site administrator — so a machine read would answer any repository in the
// namespace to any member of the org, including the private ones that member
// cannot open themselves. Sudo makes the forge's own ACL the answer.
//
// The namespace comes from forge.Owner's CLOSED table applied to the validated
// principal's org, so no caller-supplied value decides which org is read, and it
// is resolved BEFORE the credential — a tenant with no namespace on the forge is
// refused without spending a KMS read on it.
//
// Fail closed at every step: a caller with no proved forge identity is refused
// rather than served under the deployment's.
func reader(ctx context.Context, org string) (*forge.Client, string, error) {
	owner, err := forge.Owner(org)
	if err != nil {
		return nil, "", err
	}
	c, err := forge.Dial(ctx, cloud.KMSPeer{})
	if err != nil {
		return nil, "", err
	}
	login, err := c.Caller(ctx)
	if err != nil {
		return nil, "", err
	}
	return c.As(login), owner, nil
}

// forgeRev resolves a branch, tag or sha to the commit it names.
func forgeRev(ctx context.Context, org, repo, ref string) (string, error) {
	c, owner, err := reader(ctx, org)
	if err != nil {
		return "", err
	}
	return c.Resolve(ctx, owner, repo, ref)
}

// forgeTree reads the whole repository at one commit — the empty prefix is the
// tree's root, so this selects every file beneath it.
func forgeTree(ctx context.Context, org, repo, sha string) (forge.Tree, error) {
	c, owner, err := reader(ctx, org)
	if err != nil {
		return forge.Tree{}, err
	}
	return c.Files(ctx, owner, repo, sha, "")
}

// rev resolves what the caller named to the commit it names, in the caller's own
// org. An empty ref is the repository's default branch.
//
// It is on the HOT PATH: every op resolves before it asks the daemon anything,
// because the daemon keys a root by an immutable sha and refuses anything else.
// The read is one small GET (the commit's diff and signature are asked off — see
// forge.Client.Resolve), and it is deliberately not cached: a branch moves, and
// a remembered branch→commit would serve an answer about a tree nobody has.
func (s *state) rev(ctx context.Context, c *zip.Ctx, org, repo, want string) (string, error) {
	sha, err := resolveRev(as(ctx, c, org), org, repo, want)
	if err != nil || sha == "" {
		s.Log.Warn("lsp resolve failed", "org", org, "repo", repo, "ref", want, "err", err)
		return "", zip.ErrNotFound("no such repository or revision in your org")
	}
	return sha, nil
}

// files reads the repository's TEXT at one commit, from the forge that holds it.
// Nothing here checks anything out: a language server needs the bytes of some
// files at one revision, which is a tree read, not a packfile.
//
// Binary and truncated blobs are dropped rather than sent. A language server
// parses source; a binary spends the daemon's tree budget on bytes no server will
// read, and a truncated file is a HALF file, which type-checks to errors that are
// not in the repository.
func (s *state) files(ctx context.Context, c *zip.Ctx, org, repo, sha string) ([]file, error) {
	got, err := readTree(as(ctx, c, org), org, repo, sha)
	if err != nil {
		s.Log.Warn("lsp tree read failed", "org", org, "repo", repo, "rev", sha, "err", err)
		return nil, zip.ErrInternal("repository unavailable")
	}
	out := make([]file, 0, len(got.Files))
	for _, f := range got.Files {
		if f.Truncated || !text(f.Data) {
			continue
		}
		out = append(out, file{Path: f.Path, Content: string(f.Data)})
	}
	if len(out) == 0 {
		return nil, zip.ErrBadRequest("this revision holds no source the language servers read")
	}
	return out, nil
}

// text reports whether a blob is source. NUL and invalid UTF-8 are what separate
// a compiled object or an image from a file a parser can open.
func text(b []byte) bool {
	return len(b) > 0 && !bytes.ContainsRune(b, 0) && utf8.Valid(b)
}

// ── the identity seam ────────────────────────────────────────────────────────

// as is the context a forge read rides: THIS request's principal, delegated
// unchanged, so the forge answers for the caller's own authority and this
// package can never name another org. It is what [reader] resolves the caller's
// forge login from, so the delegation is not decoration — drop it and the read
// has nobody to act as and refuses.
//
// Off the HTTP path there is no request to delegate, so the already-validated
// org is stated explicitly instead. That names the tenant but not the person, so
// a read made there has no forge identity and is refused: this surface is
// reached over HTTP, and a caller with no proved identity must not be served
// under the deployment's.
func as(ctx context.Context, c *zip.Ctx, org string) context.Context {
	if c == nil {
		return cloud.For(ctx, org)
	}
	return cloud.As(c, "")
}
