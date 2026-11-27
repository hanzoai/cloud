package exec

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"path"
	"slices"
	"sort"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/exec describe` and by the Dockerfile before every build.
//
// Without this directive the package builds, tests pass, and the ONE typed op here
// publishes a summary with no description: openapi.Complete accepts either, so the
// gap is invisible to every gate and visible in every SDK. POST /v1/exec shipped
// exactly that way.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Path is where the code interpreter answers. It lives HERE, in the app that serves
// it, because three consumers once disagreed about it in production and nothing
// could see the disagreement.
const Path = "/v1/exec"

// marker is the file whose mtime separates "was already here" from "this run made
// it". It lives in /tmp, which is deliberately NOT the artifact directory, so the
// marker can never be collected as one of the run's own outputs.
const marker = "/tmp/.hanzo-run"

// langs is the CLOSED set the tool schema advertises (@hanzochat/agents
// CodeExecutor SUPPORTED_LANGUAGES), each mapped to the file its source goes in and
// the line that runs it.
//
// `"$@"` is written into each template rather than appended to it, because the
// compiled languages run something OTHER than the file they compile — the arguments
// belong to `./main`, not to `cc`. A table that appended would have handed every
// caller's argv to the compiler.
var langs = map[string]struct{ file, run string }{
	"py":   {"main.py", `python3 main.py "$@"`},
	"js":   {"main.js", `node main.js "$@"`},
	"ts":   {"main.ts", `deno run -A main.ts "$@"`},
	"bash": {"main.sh", `sh main.sh "$@"`},
	"r":    {"main.R", `Rscript main.R "$@"`},
	"php":  {"main.php", `php main.php "$@"`},
	"go":   {"main.go", `go run main.go "$@"`},
	"rs":   {"main.rs", `rustc -O -o main main.rs && ./main "$@"`},
	"c":    {"main.c", `cc -O2 -o main main.c && ./main "$@"`},
	"cpp":  {"main.cpp", `c++ -O2 -o main main.cpp && ./main "$@"`},
	"java": {"main.java", `java main.java "$@"`},
	"d":    {"main.d", `rdmd main.d "$@"`},
	"f90":  {"main.f90", `gfortran -O2 -o main main.f90 && ./main "$@"`},
}

// CodeRun is what the code tool posts. Every field is one the client actually
// sends: lang/code/args from the model's tool call, files/session_id/user_id from
// the host's injection, runtime_session_hint from the stateful-session path.
//
// EVERY FIELD IS BODY-ONLY, and `url:"-"` is what says so. zip binds an input from
// three sources in INCREASING authority — body, then query, then path (typed.go
// invoke) — and bindURL matches a query key case-insensitively against each
// field's url name, so a field left unmarked gains a `?field=` twin that OUTRANKS
// the body. On this operation that is not cosmetic. Measured against the live
// route before the tags existed, with a complete and valid body:
//
//	?code=            → 400 "field \"code\" is required"   the program was BLANKED
//	?code=<anything>  → the substituted program is what RUNS
//	?lang=zzz         → 400, the query beating a valid body
//	?session_id=X     → the run is redirected into a different sandbox
//
// None of it crosses a tenant — the org comes from tenantOf and the peer is scoped
// by it — but it silently substitutes what executes, on a parameter no caller
// means as input. The URL carries no part of this operation's input, so the whole
// struct declines it; the slices are declined too, because setScalar leaving them
// alone today is a property of their KIND rather than a decision anybody made.
type CodeRun struct {
	// Lang selects the toolchain, and with it the filename the code is written to
	// and the line that runs it: py, js, ts, bash, r, php, go, rs, c, cpp, java, d,
	// f90. Anything else is refused rather than guessed at — a run in the wrong
	// language fails somewhere deep in a compiler, which reads as an outage.
	Lang string `json:"lang" url:"-" validate:"required"`
	// Code is the WHOLE program, not a fragment: it is written to a single file and
	// that file is what runs, so a compiled language needs its entry point and an
	// interpreted one runs top to bottom.
	Code string `json:"code" url:"-" validate:"required"`
	// Args become the PROGRAM's argv, never the compiler's. For the compiled
	// languages the toolchain builds first and these are passed to the binary it
	// produced.
	Args []string `json:"args,omitempty" url:"-"`
	// Files are inputs the host already put in some session. Each names the session
	// its bytes live in, which is usually — and ideally — the session this run wants.
	Files []CodeFile `json:"files,omitempty" url:"-"`
	// SessionID continues an EXISTING sandbox, which is what makes runs stateful:
	// the same filesystem, so one run's output file is the next run's input. Empty
	// leases a fresh sandbox and the id it got comes back on the result.
	SessionID string `json:"session_id,omitempty" url:"-"`
	// UserID attributes the run inside the caller's org. It is a label, never a
	// tenant: the org is resolved from the validated principal and a value here
	// cannot widen what the run may reach.
	UserID string `json:"user_id,omitempty" url:"-"`
	// RuntimeSessionHint is the stateful-session hint. It is carried so a client
	// that sends it is not silently misread, and it selects nothing here: every
	// session in this implementation is already a warm sandbox, so there is no
	// second kind of runtime for a hint to choose between.
	RuntimeSessionHint string `json:"runtime_session_hint,omitempty" url:"-"`
}

// CodeFile is one file in a session. ID is its path RELATIVE to the session's
// artifact directory, which is what makes a download a read and not a lookup: there
// is no id table to keep, because the id already says where the bytes are.
//
// TWO SPELLINGS OF ONE FIELD, and both are read. @hanzochat/agents' FileRef calls it
// `storage_session_id` (tools.d.ts, and the split from the execution session is
// deliberate there); hanzo.chat's own primer sends `session_id`
// (Files/Code/process.js pushFile). Reading only the first meant every file a user
// attached arrived with an empty session, was skipped by the copy loop, and was
// skipped by the "not available" note as well — so the CSV was invisible and nothing
// said so. Answering with `storage_session_id` keeps the reply on the agents shape.
type CodeFile struct {
	// ID is the file's path RELATIVE to its session's artifact directory, which is
	// also how it is fetched: GET /v1/exec/download/{session}/{id}.
	ID string `json:"id"`
	// Name is the display name. On an ANSWER it carries the `{session}/{id}`
	// identifier whole, because the client matches on that prefix.
	Name string `json:"name"`
	// StorageSessionID names the session holding the bytes, and is the spelling the
	// answer always uses.
	StorageSessionID string `json:"storage_session_id,omitempty"`
	// SessionID is the other accepted spelling of the same fact on the way IN. Both
	// are read; whichever is set wins.
	SessionID string `json:"session_id,omitempty"`
}

// Session is the session a file's bytes live in, whichever name the caller used.
func (f CodeFile) Session() string {
	return cmp.Or(strings.TrimSpace(f.StorageSessionID), strings.TrimSpace(f.SessionID))
}

// CodeResult is one run. A program that exited non-zero is a SUCCESSFUL call carrying a
// failed program — its diagnostics are on Stderr and the status stays 200, because
// "the code threw" and "the interpreter is down" are different facts and the caller
// renders them differently.
type CodeResult struct {
	// SessionID is the sandbox this run used — the one that was passed in, or the
	// fresh one that was leased. Pass it to the next run to keep the filesystem.
	SessionID string `json:"session_id"`
	// Stdout is what the program wrote to standard output.
	Stdout string `json:"stdout"`
	// Stderr is what the program wrote to standard error, INCLUDING a compiler's
	// diagnostics and the trace of a program that exited non-zero. Its presence is
	// not a failed call.
	Stderr string `json:"stderr"`
	// Files are what this run CREATED OR CHANGED, decided by mtime against a marker
	// taken before the program started — so it is the run's output, not a listing of
	// the directory. Fetch each from GET /v1/exec/download/{session}/{id}.
	Files []CodeFile `json:"files,omitempty"`
}

// listing is one row of GET /v1/exec/files/{sid}. The client matches on `name` having
// the `{session}/{id}` identifier as a PREFIX, so `name` carries that identifier
// whole rather than the bare filename.
type listing struct {
	// Name is the file's IDENTIFIER, `{session_id}/{fileId}` whole — never the bare
	// filename, and never URL-escaped. It is exactly what GET /v1/exec/download
	// takes after its prefix, and hanzo.chat matches it as a PREFIX
	// (`name.startsWith(session + "/")`) to decide which rows belong to a session
	// it is holding. `fileId` is the path RELATIVE to the session's artifact
	// directory, so it carries `/` for anything the run wrote in a sub-directory.
	Name string `json:"name"`
	// LastModified is when the BYTES last changed, as RFC 3339 in UTC to the
	// second — `2026-01-02T03:04:05Z`, the sandbox's own `date -u -r` on the file.
	// It is an mtime and not a creation time, so a file a later run overwrote
	// carries that run's clock. Never empty: a row exists only because `find`
	// stat-ed the file.
	LastModified string `json:"lastModified"`
}

// uploaded is the answer POST /v1/exec/upload owes. `message` is checked for the literal
// "success" before any other field is read (crud.js), so it is not decoration.
type uploaded struct {
	Message   string         `json:"message"`
	SessionID string         `json:"session_id"`
	Files     []uploadedFile `json:"files"`
}

type uploadedFile struct {
	FileID   string `json:"fileId"`
	Filename string `json:"filename"`
}

// ---- the composition -------------------------------------------------------

// run executes a program in a throwaway sandbox and answers with what it printed
// and what it left behind.
//
// `lang` names one of the thirteen the sandbox image carries — py, js, ts, bash, r,
// php, go, rs, c, cpp, java, d, f90 — and `code` is the whole program, not a
// fragment: a compiled language is compiled and then run, an interpreted one is
// interpreted, and `args` becomes the program's own argv either way. Nothing is
// installed for you; the image is the environment.
//
// A PROGRAM THAT FAILS IS A SUCCESSFUL CALL. A non-zero exit answers 200 with the
// diagnostics on `stderr`, because "the code threw" and "the interpreter is down"
// are different facts a caller renders differently. Only the second is an error
// status.
//
// Runs are stateful through `session_id`. Omit it and the run gets a fresh sandbox
// whose id comes back on the answer; pass that id again and the next run sees the
// same filesystem, so a program can write a file one call and read it the next.
// `files` names bytes already uploaded to a session (POST /v1/exec/upload), copied in
// before the program starts. `files` on the ANSWER is what the program created or
// changed, by comparison against a marker taken at start — so it is the run's real
// output, not a listing of the directory — and each is fetched from
// GET /v1/exec/download/{session}/{name}.
//
// The tenant is the caller's, never the body's, at every entry point. A typed op is
// also an MCP tool and an op-plane op; MCP's tools/call invokes it directly, with no
// route and therefore no middleware, so nothing there could have checked a
// credential. tenantOf refuses a context carrying no validated principal, so those
// entry points fail closed without a second gate to keep in step.
func run(ctx context.Context, in *CodeRun) (*CodeResult, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	// Running a program costs a sandbox, and this entry point is where THIS
	// subsystem's callers pay for it. Both halves sit here rather than inside Run
	// because Run is shared: apps/functions charges its own invoke fee around the
	// same call, and a charge one layer down would bill that path twice. See meter.go.
	ch, err := afford(ctx)
	if err != nil {
		return nil, cloud.Denied(err)
	}
	defer ch.Release()
	out, err := Run(ctx, org, in)
	if err != nil {
		return nil, err
	}
	charge(ch)
	return out, nil
}

// Run is the whole interpreter, and it is composition rather than implementation:
// lease a sandbox, put the program in it, run it, read back what changed.
//
// It is EXPORTED because a second subsystem in this process runs snippets too —
// apps/functions invokes a customer function, which is this operation with a
// shorter lease — and the alternative was a second copy of the language table, the
// artifact sweep and the session rule. The org is a parameter and never read from
// the argument: a caller that could name the tenant could run in another one.
func Run(ctx context.Context, org string, in *CodeRun) (*CodeResult, error) {
	l, ok := langs[strings.ToLower(strings.TrimSpace(in.Lang))]
	if !ok {
		return nil, zip.ErrBadRequest("lang must be one of " + strings.Join(Languages(), ", "))
	}
	if strings.TrimSpace(in.Code) == "" {
		return nil, zip.ErrBadRequest("code required")
	}
	ctx, done, err := callCtx(ctx, org)
	if err != nil {
		return nil, err
	}
	defer done()

	// WHICH session. The one the caller named, else the one its input files already
	// live in, else a fresh lease. Running where the inputs already are is not an
	// optimisation — it is the only way a file uploaded in one call is readable by
	// the next without a second copy of it existing somewhere.
	sb, err := lease(ctx, cmp.Or(strings.TrimSpace(in.SessionID), storageSession(in.Files)))
	if err != nil {
		return nil, err
	}
	// Inputs that live in ANOTHER session are copied in. A source whose lease has
	// since ended cannot be copied and says so on stderr rather than being skipped
	// in silence — a run that quietly cannot see its own input reads as a bug in the
	// code the model wrote.
	var missing []string
	for _, f := range in.Files {
		src := f.Session()
		if src == "" {
			// A file with no session at all names bytes this deployment cannot find.
			// Reported rather than skipped: the previous silence is what made an
			// attached CSV invisible with no error anywhere.
			missing = append(missing, f.Name)
			continue
		}
		if src == sb.ID {
			continue
		}
		if err := carry(ctx, f, sb.ID); err != nil {
			missing = append(missing, f.Name)
		}
	}
	if _, err := write(ctx, sb.ID, l.file, []byte(in.Code)); err != nil {
		return nil, err
	}

	// ONE shell line: stamp the marker, then exec the program. The marker has to be
	// written INSIDE this command and not before it, because anything stamped by a
	// separate call would be older than the program file this call just wrote, and
	// the collection below would then report the source as one of the run's outputs.
	argv := append([]string{"sh", "-c", ": > " + marker + "\n" + l.run, "sh"}, in.Args...)
	ran, err := sandboxpeer.SandboxRun(ctx,
		&client.RunIn{ID: sb.ID, Argv: argv})
	if err != nil {
		return nil, err
	}
	out := &CodeResult{SessionID: sb.ID, Stdout: ran.Stdout, Stderr: ran.Stderr, Files: produced(ctx, sb.ID)}
	if len(missing) > 0 {
		out.Stderr = strings.TrimRight("input files not available in this session: "+
			strings.Join(missing, ", ")+"\n"+out.Stderr, "\n")
	}
	return out, nil
}

// produced lists what the run wrote: everything under the artifact directory newer
// than the marker this run stamped. `find` and not a listing diff, because a diff
// would need a snapshot taken before the run and would still miss a file that was
// overwritten rather than created.
//
// A failure here is NOT a failed run. The program's output is already in hand, and
// answering 500 because the artifact sweep tripped would throw away the one thing
// the caller asked for.
func produced(ctx context.Context, id string) []CodeFile {
	ran, err := sandboxpeer.SandboxRun(ctx, &client.RunIn{
		ID: id, Argv: []string{"sh", "-c", "find . -type f -newer " + marker + " 2>/dev/null"}})
	if err != nil || ran.ExitCode != 0 {
		return nil
	}
	var out []CodeFile
	for p := range strings.SplitSeq(ran.Stdout, "\n") {
		p = strings.TrimPrefix(strings.TrimSpace(p), "./")
		if p == "" {
			continue
		}
		out = append(out, CodeFile{ID: p, Name: path.Base(p), StorageSessionID: id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// carry copies one input file from the session it lives in into the session that is
// about to run. Read then write — two plane calls, because the bytes have to cross
// two pods and there is no third place for them to meet.
func carry(ctx context.Context, f CodeFile, into string) error {
	b, err := sandboxpeer.SandboxRead(ctx,
		&client.PathIn{ID: f.Session(), Path: f.ID})
	if err != nil || b.Dir {
		return fmt.Errorf("read %s: %v", f.ID, err)
	}
	_, err = write(ctx, into, cmp.Or(strings.TrimSpace(f.ID), strings.TrimSpace(f.Name)), b.Data)
	return err
}

// End drops a session's sandbox now instead of leaving it to the reaper. Nothing on
// the code-interpreter surface calls it — a session there outlives its run, because
// the reply hands back an id the next three calls address — but a FUNCTION invoke is
// over the moment it answers, and holding a pod for the reaper to notice would be
// fifteen idle minutes of somebody's node per call.
func End(ctx context.Context, org, session string) error {
	cctx, done, err := callCtx(ctx, org)
	if err != nil {
		return err
	}
	defer done()
	_, err = sandboxpeer.SandboxEnd(cctx, &client.EndIn{ID: session})
	return err
}

func lease(ctx context.Context, id string) (*client.Leased, error) {
	return sandboxpeer.SandboxLease(ctx,
		&client.LeaseIn{ID: id, Class: "exec"})
}

func write(ctx context.Context, id, p string, data []byte) (*client.Wrote, error) {
	return sandboxpeer.SandboxWrite(ctx,
		&client.WriteIn{ID: id, Path: p, Data: data})
}

// tenantOf is exec's admission: the org a validated principal resolved to, and
// nothing else.
//
// principal.Acting is the fleet's rule — from outside, the org the validated
// principal resolved to, because the identity boundary restores an unvalidated
// caller's own org header for the data path and a tenant read from that is a
// tenant the caller chose; from inside, the caller an entry point stated, which
// nothing outside can write.
//
// It used to be that rule PLUS an untenanted case: a shared service key carried
// no tenant, so a request bearing it acted for the deployment itself. That key
// was this subsystem authenticating callers on its own, which is IAM's job and
// nobody else's. With it gone there is no caller without a tenant, so there is
// no second case to add — the rule is the whole rule.
func tenantOf(ctx context.Context) (string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	return org, nil
}

// callCtx is the context every sandbox call is made on.
//
// It ALWAYS detaches and states the resolved tenant, with no branch. Passing the
// request context through where it already carries a caller would let the peer
// read the org off the headers instead — the value tenantOf just declined to
// decide on. One path, and the org the peer sees is exactly the one this
// subsystem decided.
//
// zip reads a STATED caller only on a context with no request behind it
// (zip.CallerOf prefers the request), which is the anti-laundering rule and the
// reason the detach is not optional. Detaching would also drop the client's
// disconnect, which on a code run means a sandbox executing for nobody — AfterFunc
// puts exactly that one thing back: the values are ours, the cancellation is still
// the request's.
//
// THE PERSON IS STATED WITH THE TENANT. apps/sandbox records who leased a sandbox
// and lets nobody else in the org act on it but an admin, so a code session is its
// caller's own — and a session id another member names is a sandbox that member
// does not hold, answered as absent. The subject is the one a sandbox is held by
// (principal.SubjectFrom), never X-User-Id, whose fallback to a username or a
// display name is somebody else's too: a caller whose credential names no such
// subject holds no session and is refused. The admin bits are not carried,
// because this surface acts on the caller's own sessions and nobody else's.
func callCtx(ctx context.Context, org string) (context.Context, func(), error) {
	sub, keyed := principal.SubjectFrom(ctx)
	if !keyed {
		return nil, nil, zip.ErrForbidden("exec: a session is held by the person it is for, and this credential names no subject to hold it by")
	}
	who := zip.Caller{User: strings.TrimSpace(sub)}
	out, cancel := context.WithCancel(cloud.For(zip.WithCaller(context.Background(), who), org))
	stop := context.AfterFunc(ctx, cancel)
	return out, func() { stop(); cancel() }, nil
}

func storageSession(fs []CodeFile) string {
	for _, f := range fs {
		if s := f.Session(); s != "" {
			return s
		}
	}
	return ""
}

func Languages() []string { return slices.Sorted(maps.Keys(langs)) }

// ---- the two routes that cannot be typed ops -------------------------------
//
// TWO, and the count is a TEST rather than this line: typed_wire_test.go reads the
// live router and requires the typed ops and the named refusals to SUM to the
// served surface. That header said THREE while it sat above four handlers, one of
// which was already typed — which is what a count in a comment always eventually
// says, because a comment cannot fail.

// upload puts a multipart file into a session and answers the identifier the client
// will address it by. It is a zip.Ctx handler and not a typed op because zip decodes
// every non-empty typed body as JSON, so an In here would turn a working upload into
// a 400.
func upload(c *zip.Ctx) error {
	fh, err := c.Fiber().FormFile("file")
	if err != nil {
		return zip.ErrBadRequest("multipart field `file` required")
	}
	f, err := fh.Open()
	if err != nil {
		return zip.Errorf(http.StatusBadRequest, "open upload: %v", err)
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, fh.Size)
	if _, err := readFull(f, buf); err != nil {
		return zip.Errorf(http.StatusBadRequest, "read upload: %v", err)
	}
	org, err := tenantOf(c.Context())
	if err != nil {
		return err
	}
	ctx, done, err := callCtx(c.Context(), org)
	if err != nil {
		return err
	}
	defer done()
	sb, err := lease(ctx, strings.TrimSpace(c.Fiber().FormValue("session_id")))
	if err != nil {
		return err
	}
	name := path.Base(fh.Filename)
	if _, err := write(ctx, sb.ID, name, buf); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, uploaded{Message: "success", SessionID: sb.ID,
		Files: []uploadedFile{{FileID: name, Filename: name}}})
}

// download answers one file's BYTES under a content type guessed from its name. It
// is a zip.Ctx handler and not a typed op for the reason that cannot be worked
// around: a typed op always marshals JSON, and this is the one address whose success
// body is a stream.
//
// The path is `{session_id}/{fileId}` and the fileId may itself contain separators,
// so it is split ONCE — everything after the first segment is the path inside the
// session.
func download(c *zip.Ctx) error {
	sid, p, ok := strings.Cut(strings.TrimPrefix(c.Param("*1"), "/"), "/")
	if !ok || sid == "" || p == "" {
		return zip.ErrBadRequest("download path is {session_id}/{fileId}")
	}
	org, err := tenantOf(c.Context())
	if err != nil {
		return err
	}
	ctx, done, err := callCtx(c.Context(), org)
	if err != nil {
		return err
	}
	defer done()
	b, err := sandboxpeer.SandboxRead(ctx,
		&client.PathIn{ID: sid, Path: p})
	if err != nil {
		return err
	}
	if b.Dir {
		return zip.ErrNotFound("that path is a directory")
	}
	ct := mime.TypeByExtension(path.Ext(p))
	if ct == "" {
		ct = "application/octet-stream"
	}
	c.SetHeader("Content-Type", ct)
	return c.Bytes(http.StatusOK, b.Data)
}

// sessionRef addresses one session's file listing.
type sessionRef struct {
	// SID is the session identifier — the sandbox this listing is of. The URL is
	// the addressing authority: a path segment binds after the body and after the
	// query, so the address decides which session is read whatever else is sent.
	SID string `json:"sid"`
}

// listings is what a session holds. It is a NAMED SLICE, not an envelope struct,
// because the wire is a BARE JSON ARRAY — the client reads
// `response.data.find(...)` over the body itself — and wrapping it in an object
// would be a wire change on a contract this repo does not own.
//
// That distinction is the whole reason this route could be typed at all. The
// obvious typed shape is `{files: [...]}` and it is wrong; apps/provisioning found
// the same thing for its listing and took the same answer. A named slice publishes
// an array and marshals to one.
type listings []listing

// listFiles lists the files in an execution session.
//
// Everything the session's sandbox holds — the uploads a run can read and the
// artifacts it produced — each then fetched from GET /v1/exec/download.
//
// The answer is a BARE JSON ARRAY of {name, lastModified}, where `name` is the
// same {session_id}/{fileId} identifier download takes, because that is what the
// client matches on. The obvious typed shape, `{files: […]}`, would have been a
// silent wire change: the request still succeeds and `response.data.find(...)`
// finds nothing, which reads as a session holding no files.
//
// It lists with one recursive `find`, the same traversal the artifact sweep
// makes, so a nested artifact a run reports in its reply is listed here too: one
// traversal of the directory, one answer about what a session holds.
func listFiles(ctx context.Context, in *sessionRef) (*listings, error) {
	sid := strings.TrimSpace(in.SID)
	if sid == "" {
		return nil, zip.ErrBadRequest("session id required")
	}
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	ctx, done, err := callCtx(ctx, org)
	if err != nil {
		return nil, err
	}
	defer done()

	// ONE `find`, RECURSIVE, and it is the same traversal the artifact sweep makes.
	//
	// The listing used to be `ls -1A` — top level only — while `produced` collected
	// with `find`. So a run that wrote /mnt/data/out/plot.png reported that artifact
	// in its reply and then omitted it here, and the client's
	// `name.startsWith(session/id)` found nothing and read the file as EXPIRED. Two
	// traversals of one directory is two answers about what a session holds; there is
	// one now.
	ran, err := sandboxpeer.SandboxRun(ctx, &client.RunIn{
		ID: sid, Argv: []string{"sh", "-c",
			`find . -type f -exec date -u -r {} +%Y-%m-%dT%H:%M:%SZ \; -print`}})
	if err != nil {
		return nil, err
	}
	// The rows come in pairs: the stamp, then the path it belongs to.
	rows := strings.Split(ran.Stdout, "\n")
	out := make(listings, 0, len(rows)/2)
	for i := 0; i+1 < len(rows); i += 2 {
		p := strings.TrimPrefix(strings.TrimSpace(rows[i+1]), "./")
		if p == "" {
			continue
		}
		out = append(out, listing{Name: sid + "/" + p, LastModified: strings.TrimSpace(rows[i])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return &out, nil
}

func readFull(r interface{ Read([]byte) (int, error) }, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := r.Read(b[n:])
		n += m
		if err != nil {
			if n == len(b) {
				return n, nil
			}
			return n, err
		}
	}
	return n, nil
}

// Mount registers the code-interpreter surface.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("exec: nil app")
	}
	// The meter that pays for a run. Bound here because this subsystem's handlers
	// are free functions with no service value to hang it off. See meter.go.
	bindMeter(cloud.NewMeter(deps, "exec"))

	// The identity a typed op reads, parked on the request AHEAD of the leaves.
	//
	// A typed op receives a context and never a request, so the validated principal
	// has to be put there by middleware; fiber runs middleware in registration
	// order, so one installed after its leaves never runs. cloud.Serve installs
	// this app-wide and that is why nothing was live-broken — but no package test
	// runs Serve, so exec's own suite proved the org path with the org absent, and
	// meter_test.go had to add a Bridge by hand to resolve a payer at all.
	//
	// It also retired a copy: exec's own credential middleware used to call
	// principal.WithOrg itself — Bridge's org half, restated — while parking none of
	// Bridge's other facts. Two partial implementations of one fact on one request
	// is the drift; there is one now, and what the middleware below adds is visibly
	// the ADMISSION and nothing else.
	app.Use(cloud.Bridge())

	// The credential check, and the fact it produces.
	//
	// cloud.RoutePath, not c.Path(): fiber routes case-insensitively and ignores a
	// trailing slash, so the raw spelling is what the CLIENT sent and RoutePath is
	// the form THE ROUTER MATCHED. `POST /V1/EXEC` reached this middleware, missed a
	// lowercase prefix test, and ran code with no key at all. Every other gate in
	// this repo already normalizes here (middleware_abuse.go:160,
	// middleware_ratelimit.go:100); this one did not.
	//
	// It parks its fact on the request context, which is what makes the check reach
	// past the router: admit records that the service key checked out, and a typed
	// op reads it back because zip rebuilds from c.Context(). The VALIDATED org is
	// Bridge's, above — one owner for one fact. Nothing downstream re-reads a header
	// or a path to decide either.

	g := app.Group("/v1/exec")
	g.Post("", run,
		zip.WithSummary("Run a code snippet in a sandboxed interpreter"))
	g.Raw(http.MethodPost, "/upload", upload)
	g.Raw(http.MethodGet, "/download/*", download)
	g.Get("/files/:sid", listFiles)

	luxlog.Default().New("subsystem", "exec").Info("code interpreter mounted over sandboxes",
		"peer", sandboxpeer.App, "langs", len(langs))
	return nil
}

// Prose for the two untyped routes, declared beside the wire facts that keep them
// untyped. A typed op carries its own doc comment, which zipdoc lifts; these two
// have no comment to lift from, so they would otherwise publish an operationId and
// nothing else.
//
// A DECLARATION FOR A TYPED OP IS DEAD, and two used to sit here saying so nowhere.
// Describe writes a structural operation and Fold then REPLACES it with the typed
// one (openapi/openapi.go), so the prose was written, reviewed and discarded — the
// published summary for the file listing was its Go identifier, "Files lists what a
// session holds.", while the sentence declared here rendered nowhere.
// openapi.Complete cannot catch it either: its orphan check asks only whether the
// ADDRESS is served, and it is. So the rule is the shape of the route, not a
// preference — a typed op is described by its comment, and only a raw route is
// described here.
func init() {
	spec.Describe(Path+"/upload", http.MethodPost,
		"Upload a file into an execution session",
		"Takes a multipart upload and writes the file into the session's sandbox, so a "+
			"later run can read it. Answers the session id and the identifier the file is "+
			"addressed by; `session_id` in the form joins an existing session instead of "+
			"opening one.\n\nThe body is multipart/form-data, which is why this is not a "+
			"typed operation: every non-empty typed body is decoded as JSON.")
	spec.Describe(Path+"/download/*", http.MethodGet,
		"Download a file from a session",
		"Fetches one file's BYTES from a session, addressed as {session_id}/{fileId} — a "+
			"plot, a generated CSV, whatever a run wrote. The content type is derived from the "+
			"name and defaults to application/octet-stream.\n\nThis is the one address whose "+
			"success body is not JSON, which is why it is not a typed operation: a typed "+
			"operation always marshals a Go value.")
}
