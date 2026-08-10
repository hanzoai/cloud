// Package exec is the code interpreter: run a snippet in a sandbox, and move files
// in and out of the session that sandbox IS.
//
// # A session is a sandbox
//
// That is the whole design, and it is why this subsystem now holds no store, no
// session table and no lifetime of its own. The upstream contract hands a
// `session_id` back on every reply and takes it back on later calls; a sandbox has
// an id, a lease and files. They are the same object, so `session_id` IS the
// sandbox id and everything else falls out:
//
//	POST /v1/exec            lease the session's sandbox → write the program →
//	                         run it → report what it printed and what it wrote
//	POST /v1/upload          write into the session's sandbox
//	GET  /v1/download/{sid}/{id}  read one file back out
//	GET  /v1/files/{sid}     list what the session holds
//
// Nothing here ends a lease. apps/sandbox's reaper does, on the two clocks it
// already keeps — the ttl the lease was taken for, and an hour of nobody touching
// it. Deleting the sandbox at the end of a run was the obvious other design and it
// is wrong: the reply carries a session id, and the contract's next three calls
// address it. A sandbox torn down at the end of POST /v1/exec would answer 404 to
// every download of the plot that run had just made.
//
// # What was here before
//
// A reverse proxy to code-exec.hanzo.svc.cluster.local:8000, and that Service has
// had ZERO endpoints for 33 days: /v1/exec has been answering 503 in production the
// whole time. The proxy was not failing to reach the executor — there was no
// executor. Pointing it somewhere better was never the fix, because the thing being
// pointed at did not exist; the fix is that cloud already runs the one compute
// primitive, and this subsystem composes over it.
//
// # The wire is NOT ours
//
// hanzo.chat drives this through @hanzochat/agents' CodeExecutor and its own
// Files/Code client, so the shapes below are MEASURED from those two callers rather
// than designed here — including the details that are easy to get subtly wrong:
// download is addressed by TWO segments (`{session_id}/{fileId}`, crud.js), the
// file listing answers a BARE JSON ARRAY of {name, lastModified} whose `name` is
// that same two-segment identifier (process.js getSessionInfo), and upload answers
// {message:"success", session_id, files:[{fileId, filename}]} and is checked for
// `message === 'success'` before anything else is read.
//
// # Auth
//
// The gateway bypasses these paths — the credential is an opaque service key on
// X-API-Key, not a JWT — so this subsystem enforces that key itself against
// CODE_EXEC_API_KEY, in constant time, failing CLOSED when none is configured.
//
// The chat server ALSO forwards the end user's validated IAM bearer when it can
// resolve one (crud.js codeAuthHeaders), which is what lets a session be scoped to
// a real tenant. When it is there, the sandboxes are that org's. When it is not,
// they belong to the deployment's own brand org — one tenant for one deployment,
// which is what a shared service key with no tenant in it actually means.
package exec

import (
	"context"
	"crypto/subtle"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
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

// peer is the app that owns sandboxes. Reached over the internal plane and never
// imported: apps ship as separate plugin binaries, so an import would give this
// process a SECOND sandbox service — its own per-org SQLite handles on the same
// files, its own reaper racing the real one. cloud.Ask collapses to an in-process
// dispatch wherever the fleet fuses the two (plane.Ask, zip.Serving), so the hop is
// a hop only where it is really a hop.
const peer = "sandboxes"

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
type CodeRun struct {
	// Lang selects the toolchain, and with it the filename the code is written to
	// and the line that runs it: py, js, ts, bash, r, php, go, rs, c, cpp, java, d,
	// f90. Anything else is refused rather than guessed at — a run in the wrong
	// language fails somewhere deep in a compiler, which reads as an outage.
	Lang string `json:"lang" validate:"required"`
	// Code is the WHOLE program, not a fragment: it is written to a single file and
	// that file is what runs, so a compiled language needs its entry point and an
	// interpreted one runs top to bottom.
	Code string `json:"code" validate:"required"`
	// Args become the PROGRAM's argv, never the compiler's. For the compiled
	// languages the toolchain builds first and these are passed to the binary it
	// produced.
	Args []string `json:"args,omitempty"`
	// Files are inputs the host already put in some session. Each names the session
	// its bytes live in, which is usually — and ideally — the session this run wants.
	Files []CodeFile `json:"files,omitempty"`
	// SessionID continues an EXISTING sandbox, which is what makes runs stateful:
	// the same filesystem, so one run's output file is the next run's input. Empty
	// leases a fresh sandbox and the id it got comes back on the result.
	SessionID string `json:"session_id,omitempty"`
	// UserID attributes the run inside the caller's org. It is a label, never a
	// tenant: the org is resolved from the validated principal and a value here
	// cannot widen what the run may reach.
	UserID string `json:"user_id,omitempty"`
	// RuntimeSessionHint is the stateful-session hint. It is carried so a client
	// that sends it is not silently misread, and it selects nothing here: every
	// session in this implementation is already a warm sandbox, so there is no
	// second kind of runtime for a hint to choose between.
	RuntimeSessionHint string `json:"runtime_session_hint,omitempty"`
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
	// also how it is fetched: GET /v1/download/{session}/{id}.
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
func (f CodeFile) Session() string { return firstNonEmpty(f.StorageSessionID, f.SessionID) }

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
	// the directory. Fetch each from GET /v1/download/{session}/{id}.
	Files []CodeFile `json:"files,omitempty"`
}

// listing is one row of GET /v1/files/{sid}. The client matches on `name` having
// the `{session}/{id}` identifier as a PREFIX, so `name` carries that identifier
// whole rather than the bare filename.
type listing struct {
	Name         string `json:"name"`
	LastModified string `json:"lastModified"`
}

// uploaded is the answer POST /v1/upload owes. `message` is checked for the literal
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
// `files` names bytes already uploaded to a session (POST /v1/upload), copied in
// before the program starts. `files` on the ANSWER is what the program created or
// changed, by comparison against a marker taken at start — so it is the run's real
// output, not a listing of the directory — and each is fetched from
// GET /v1/download/{session}/{name}.
//
// The tenant is the caller's, never the body's, at every door. A typed op is also
// an MCP tool and an op-plane op; MCP's tools/call invokes it directly, with no
// route and therefore no middleware, so nothing there could have checked a
// credential. tenantOf refuses a context carrying neither a validated principal nor
// exec's own admission marker, so those doors fail closed without a second gate to
// keep in step.
func run(ctx context.Context, in *CodeRun) (*CodeResult, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return Run(ctx, org, in)
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
	ctx, done := callCtx(ctx, org)
	defer done()

	// WHICH session. The one the caller named, else the one its input files already
	// live in, else a fresh lease. Running where the inputs already are is not an
	// optimisation — it is the only way a file uploaded in one call is readable by
	// the next without a second copy of it existing somewhere.
	sb, err := lease(ctx, firstNonEmpty(in.SessionID, storageSession(in.Files)))
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
	ran, err := cloud.Ask[plane.RunIn, plane.Ran](ctx, peer, plane.SandboxRun,
		&plane.RunIn{ID: sb.ID, Argv: argv})
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
	ran, err := cloud.Ask[plane.RunIn, plane.Ran](ctx, peer, plane.SandboxRun, &plane.RunIn{
		ID: id, Argv: []string{"sh", "-c", "find . -type f -newer " + marker + " 2>/dev/null"}})
	if err != nil || ran.ExitCode != 0 {
		return nil
	}
	var out []CodeFile
	for _, p := range strings.Split(ran.Stdout, "\n") {
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
	b, err := cloud.Ask[plane.PathIn, plane.Blob](ctx, peer, plane.SandboxRead,
		&plane.PathIn{ID: f.Session(), Path: f.ID})
	if err != nil || b.Dir {
		return fmt.Errorf("read %s: %v", f.ID, err)
	}
	_, err = write(ctx, into, firstNonEmpty(f.ID, f.Name), b.Data)
	return err
}

// End drops a session's sandbox now instead of leaving it to the reaper. Nothing on
// the code-interpreter surface calls it — a session there outlives its run, because
// the reply hands back an id the next three calls address — but a FUNCTION invoke is
// over the moment it answers, and holding a pod for the reaper to notice would be
// fifteen idle minutes of somebody's node per call.
func End(ctx context.Context, org, session string) error {
	cctx, done := callCtx(ctx, org)
	defer done()
	_, err := cloud.Ask[plane.EndIn, struct{}](cctx, peer, plane.SandboxEnd, &plane.EndIn{ID: session})
	return err
}

func lease(ctx context.Context, id string) (*plane.Leased, error) {
	return cloud.Ask[plane.LeaseIn, plane.Leased](ctx, peer, plane.SandboxLease,
		&plane.LeaseIn{ID: id, Class: "exec"})
}

func write(ctx context.Context, id, p string, data []byte) (*plane.Wrote, error) {
	return cloud.Ask[plane.WriteIn, plane.Wrote](ctx, peer, plane.SandboxWrite,
		&plane.WriteIn{ID: id, Path: p, Data: data})
}

// admitted is the fact THIS subsystem's credential check produced, carried on the
// context rather than re-derived downstream.
//
// It exists because the check and its consequence had drifted apart. The credential
// was verified in middleware keyed on a lowercase path list, and the TENANT was
// decided separately in callCtx — so a request that never passed the check could
// still reach a handler, and a handler had no way to ask whether it had. Two ways
// in that the list did not cover:
//
//   - PATH CASE. fiber routes case-insensitively (cloud.RoutePath exists for
//     exactly this), so `POST /V1/EXEC` matched the route and missed the list.
//     With no key at all it ran code; with CODE_EXEC_API_KEY unset it ran code
//     where the documented behaviour is a 503.
//   - THE OTHER DOORS. A typed op is also an MCP tool and an op-plane op, and
//     neither is a `/v1/...` request. MCP's tools/call invokes the op DIRECTLY
//     (zip typed.go:474, registeredOp.direct) — no route, so no route middleware,
//     so no list could ever have covered it.
//
// Both are the same defect: authorization inferred from the SPELLING of a request
// instead of being a property of the request. So the middleware now parks this
// marker, and every path into this subsystem reads it. A door that does not run
// exec's middleware does not carry the marker and is refused — by construction,
// not by remembering to add it to a list.
type admittedKey struct{}

func admit(ctx context.Context) context.Context {
	return context.WithValue(ctx, admittedKey{}, true)
}

func isAdmitted(ctx context.Context) bool {
	ok, _ := ctx.Value(admittedKey{}).(bool)
	return ok
}

// tenantOf is the ONE tenant decision, and it never reads a header.
//
// THE BUG IT REPLACES: it used to prefer cloud.Who(ctx).Org. cloud.Who is
// zip.CallerOf, which reads the X-Org-Id REQUEST HEADER (zip caller.go:377) — and
// for a request carrying no validated bearer, SanitizeIdentity deliberately
// RESTORES the client's own header (middleware_identity.go:455). So the caller
// named the tenant, storeFor opened that org's SQLite file, and `X-Org-Id:
// victim-corp` ran code in the victim's store and read its artifacts back out.
//
// principal.OrgFrom is the org a VALIDATED principal resolved to and nothing else
// (principal.OrgOf: an empty user claim means "the org that rode along is
// untrusted"). Every other app in this repo resolves through it; this one was the
// outlier, and plane.go's own note — "an org in the argument is an org the caller
// chose" — is the rule it was breaking.
//
// The untenanted fallback is the deployment's brand org, and it is reachable ONLY
// through the admission marker. That is what keeps it from being a way in: the
// shared service key carries no tenant, so a request bearing it acts for the
// deployment — but a request that never presented it acts for nobody and is
// refused.
func tenantOf(ctx context.Context) (string, error) {
	if org, ok := principal.OrgFrom(ctx); ok {
		return org, nil
	}
	if isAdmitted(ctx) {
		return brandOrg, nil
	}
	return "", zip.ErrForbidden("code execution requires a validated principal or the service key")
}

// callCtx is the context every sandbox call is made on.
//
// It ALWAYS detaches and states the resolved tenant, with no branch. The earlier
// version passed the request context through whenever it already carried a caller,
// which meant the peer read the org off the request headers — the same
// attacker-controlled value tenantOf now refuses to trust. One path, and the org
// the peer sees is exactly the one this subsystem decided.
//
// zip reads a STATED caller only on a context with no request behind it
// (zip.CallerOf prefers the request), which is the anti-laundering rule and the
// reason the detach is not optional. Detaching would also drop the client's
// disconnect, which on a code run means a sandbox executing for nobody — AfterFunc
// puts exactly that one thing back: the values are ours, the cancellation is still
// the request's.
func callCtx(ctx context.Context, org string) (context.Context, func()) {
	out, cancel := context.WithCancel(cloud.For(context.Background(), org))
	stop := context.AfterFunc(ctx, cancel)
	return out, func() { stop(); cancel() }
}

// brandOrg is set at Mount from deps.Brand — WHOSE deployment this process is. It
// is not configuration and not a new env var: a value the process already carries,
// read once where it is handed in, so a Lux deployment's untenanted sessions belong
// to Lux and not to hanzo.
var brandOrg = "hanzo"

func storageSession(fs []CodeFile) string {
	for _, f := range fs {
		if s := f.Session(); s != "" {
			return s
		}
	}
	return ""
}

func Languages() []string {
	out := make([]string, 0, len(langs))
	for k := range langs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// ---- the three routes that cannot be typed ops -----------------------------

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
	ctx, done := callCtx(c.Context(), org)
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
	ctx, done := callCtx(c.Context(), org)
	defer done()
	b, err := cloud.Ask[plane.PathIn, plane.Blob](ctx, peer, plane.SandboxRead,
		&plane.PathIn{ID: sid, Path: p})
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

// files lists what a session holds. It is a zip.Ctx handler and not a typed op
// because the client reads a BARE JSON ARRAY — `response.data.find(...)` over the
// body itself — and an object wrapper would be a wire change on a contract this
// repo does not own.
func files(c *zip.Ctx) error {
	sid := strings.TrimSpace(c.Param("sid"))
	if sid == "" {
		return zip.ErrBadRequest("session id required")
	}
	org, err := tenantOf(c.Context())
	if err != nil {
		return err
	}
	ctx, done := callCtx(c.Context(), org)
	defer done()

	// ONE `find`, RECURSIVE, and it is the same traversal the artifact sweep makes.
	//
	// The listing used to be `ls -1A` — top level only — while `produced` collected
	// with `find`. So a run that wrote /mnt/data/out/plot.png reported that artifact
	// in its reply and then omitted it here, and the client's
	// `name.startsWith(session/id)` found nothing and read the file as EXPIRED. Two
	// traversals of one directory is two answers about what a session holds; there is
	// one now.
	ran, err := cloud.Ask[plane.RunIn, plane.Ran](ctx, peer, plane.SandboxRun, &plane.RunIn{
		ID: sid, Argv: []string{"sh", "-c",
			`find . -type f -exec date -u -r {} +%Y-%m-%dT%H:%M:%SZ \; -print`}})
	if err != nil {
		return err
	}
	// The rows come in pairs: the stamp, then the path it belongs to.
	rows := strings.Split(ran.Stdout, "\n")
	out := make([]listing, 0, len(rows)/2)
	for i := 0; i+1 < len(rows); i += 2 {
		p := strings.TrimPrefix(strings.TrimSpace(rows[i+1]), "./")
		if p == "" {
			continue
		}
		out = append(out, listing{Name: sid + "/" + p, LastModified: strings.TrimSpace(rows[i])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return c.JSON(http.StatusOK, out)
}

// programmatic refuses, and names what it would take to stop refusing.
//
// /exec/programmatic is NOT this contract's sibling — it is a different protocol on
// an adjacent path: a multi-round-trip loop where the server suspends a Python
// program on a tool call, returns the pending calls with a continuation_token, and
// resumes when the client posts the results back (@hanzochat/agents
// ProgrammaticToolCalling). Implementing it means implementing suspension and
// resumption, which is a program, not an endpoint.
//
// So it answers 501 with that fact rather than being routed into `run`, which would
// hand the caller a CodeResult its parser cannot read — a wrong answer, where this is a
// refusal a client can act on.
func programmatic(c *zip.Ctx) error {
	return zip.Errorf(http.StatusNotImplemented,
		"programmatic tool calling is a different protocol from /v1/exec: it suspends a "+
			"run on each tool call and resumes it from a continuation token. This deployment "+
			"serves /v1/exec only")
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

// ---- auth ------------------------------------------------------------------

// apiKey is the shared service key the chat server presents on X-API-Key. It is
// KMS-sourced and synced into the pod env as CODE_EXEC_API_KEY.
func apiKey() string { return strings.TrimSpace(os.Getenv("CODE_EXEC_API_KEY")) }

// checkKey enforces the shared service key in constant time. Unset ⇒ 503 (fail
// closed, never open); wrong ⇒ 401.
//
// It answers an error instead of wrapping a handler, because a wrapper is a thing a
// route can be registered around and this must be a thing a route cannot avoid.
func checkKey(c *zip.Ctx) error {
	want := apiKey()
	if want == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "code execution not configured")
	}
	got := strings.TrimSpace(c.Header("X-API-Key"))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return zip.Errorf(http.StatusUnauthorized, "invalid api key")
	}
	return nil
}

// Mount registers the code-interpreter surface.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("exec.Mount: nil app")
	}
	if b := strings.TrimSpace(deps.Brand); b != "" {
		brandOrg = b
	}
	// The credential check, and the two facts it produces.
	//
	// cloud.RoutePath, not c.Path(): fiber routes case-insensitively and ignores a
	// trailing slash, so the raw spelling is what the CLIENT sent and RoutePath is
	// the form THE ROUTER MATCHED. `POST /V1/EXEC` reached this middleware, missed a
	// lowercase prefix test, and ran code with no key at all. Every other gate in
	// this repo already normalizes here (middleware_abuse.go:160,
	// middleware_ratelimit.go:100); this one did not.
	//
	// It parks BOTH facts on the request context, which is what makes the check
	// reach past the router: principal.WithOrg carries the VALIDATED org so a typed
	// op can resolve it (typed.go:82 rebuilds from c.Context(), so this is inherited),
	// and admit records that the service key checked out. Nothing downstream re-reads
	// a header or a path to decide either one.
	app.Use(zip.H(func(c *zip.Ctx) error {
		if !owned(cloud.RoutePath(c.Path())) {
			return c.Continue()
		}
		if err := checkKey(c); err != nil {
			return err
		}
		c.SetContext(principal.WithOrg(admit(c.Context()), c))
		return c.Continue()
	}))

	// Registered on the *zip.App rather than on the cloud.Router, and that is what
	// makes the prose reach the document. zipdoc resolves a typed op's path
	// STATICALLY, and a `cloud.Router` parameter is an interface it cannot follow to
	// a prefix — so it refuses to lift, the op publishes a summary and no
	// description, and openapi.Complete accepts that because either one satisfies
	// it. The subsystem scope adds no prefix here (every path below is absolute), so
	// this is the same registration, spelled where the generator can read it.
	//
	// It is only the ROUTES that move: the credential middleware above stays on the
	// scoped router, where the prefix guard applies to it.
	reg := cloud.ZipApp(app)
	if reg == nil {
		return fmt.Errorf("exec.Mount: router carries no typed-op registry")
	}
	zip.Post[CodeRun, CodeResult](reg, Path, run,
		zip.WithSummary("Run a code snippet in a sandboxed interpreter"))
	app.Post(Path+"/programmatic", programmatic)
	app.Post("/v1/upload", upload)
	app.Get("/v1/download/*", download)
	app.Get("/v1/files/:sid", files)

	luxlog.Default().New("subsystem", "exec").Info("code interpreter mounted over sandboxes",
		"peer", peer, "brandOrg", brandOrg, "langs", len(langs))
	return nil
}

// prefixes are the /v1 segments this subsystem owns.
//
// It is still a list that mirrors the router, and a list that mirrors the router
// will eventually diverge from it. What changed is the CONSEQUENCE of that
// divergence: a path this list misses no longer reaches a handler that will act —
// tenantOf refuses a context with no admission marker on it — so a stale entry here
// is now a 403 on a route that should have worked, and never a route that works
// without a credential. Fail-closed on drift instead of fail-open.
//
// p must already be cloud.RoutePath-normalized.
var prefixes = []string{Path, "/v1/upload", "/v1/download", "/v1/files"}

func owned(p string) bool {
	for _, pre := range prefixes {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

// Prose for the four untyped routes, declared beside the wire facts that keep them
// untyped. POST /v1/exec is a typed op and carries its own doc comment; these four
// have no comment for zipdoc to lift, so they would otherwise publish an
// operationId and nothing else.
func init() {
	openapi.Describe(Path+"/programmatic", http.MethodPost,
		"Programmatic tool calling (not served here)",
		"Answers 501. This address belongs to a DIFFERENT protocol from /v1/exec: the "+
			"server suspends a program on each tool call, returns the pending calls with a "+
			"continuation token, and resumes when the client posts results back. Serving it "+
			"means implementing suspension and resumption, so it refuses in the open rather "+
			"than answering with a shape the caller's parser cannot read.")
	openapi.Describe("/v1/upload", http.MethodPost,
		"Upload a file into an execution session",
		"Takes a multipart upload and writes the file into the session's sandbox, so a "+
			"later run can read it. Answers the session id and the identifier the file is "+
			"addressed by; `session_id` in the form joins an existing session instead of "+
			"opening one.\n\nThe body is multipart/form-data, which is why this is not a "+
			"typed operation: every non-empty typed body is decoded as JSON.")
	openapi.Describe("/v1/download/*", http.MethodGet,
		"Download a file from a session",
		"Fetches one file's BYTES from a session, addressed as {session_id}/{fileId} — a "+
			"plot, a generated CSV, whatever a run wrote. The content type is derived from the "+
			"name and defaults to application/octet-stream.\n\nThis is the one address whose "+
			"success body is not JSON, which is why it is not a typed operation: a typed "+
			"operation always marshals a Go value.")
	openapi.Describe("/v1/files/:sid", http.MethodGet,
		"List the files in an execution session",
		"Lists what a session's sandbox holds — the uploads a run can read and the "+
			"artifacts it produced — each then fetched from /v1/download.\n\nIt answers a BARE "+
			"JSON ARRAY of {name, lastModified}, where `name` is the same {session_id}/{fileId} "+
			"identifier download takes, because that is what the client matches on. An object "+
			"wrapper would be a wire change, which is why this is not a typed operation.")
}
