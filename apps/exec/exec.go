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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

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
	Lang string `json:"lang" validate:"required"`
	Code string `json:"code" validate:"required"`
	Args []string `json:"args,omitempty"`
	// Files are inputs the host already put in some session. Each names the session
	// its bytes live in, which is usually — and ideally — the session this run wants.
	Files     []CodeFile `json:"files,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
	// RuntimeSessionHint is the stateful-session hint. It is carried so a client
	// that sends it is not silently misread, and it selects nothing here: every
	// session in this implementation is already a warm sandbox, so there is no
	// second kind of runtime for a hint to choose between.
	RuntimeSessionHint string `json:"runtime_session_hint,omitempty"`
}

// CodeFile is one file in a session. ID is its path RELATIVE to the session's
// artifact directory, which is what makes a download a read and not a lookup: there
// is no id table to keep, because the id already says where the bytes are.
type CodeFile struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	StorageSessionID string `json:"storage_session_id,omitempty"`
}

// CodeResult is one run. A program that exited non-zero is a SUCCESSFUL call carrying a
// failed program — its diagnostics are on Stderr and the status stays 200, because
// "the code threw" and "the interpreter is down" are different facts and the caller
// renders them differently.
type CodeResult struct {
	SessionID string    `json:"session_id"`
	Stdout    string    `json:"stdout"`
	Stderr    string    `json:"stderr"`
	Files     []CodeFile `json:"files,omitempty"`
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

// run is the typed op: the caller's tenant, then the interpreter.
func run(ctx context.Context, in *CodeRun) (*CodeResult, error) { return Run(ctx, brandOrg, in) }

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
		if f.StorageSessionID == "" || f.StorageSessionID == sb.ID {
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
		&plane.PathIn{ID: f.StorageSessionID, Path: f.ID})
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

// callCtx is the context every sandbox call is made on, and it decides WHOSE
// sandboxes that call makes.
//
// The chat server forwards the end user's validated IAM bearer when it can resolve
// one (crud.js codeAuthHeaders), so a request that arrived with a principal already
// names its tenant — and that context is passed through UNCHANGED. Not re-stated,
// not re-pointed: the identity the edge minted is the one the peer must apply its
// rules to.
//
// A request with no principal is the shared service key, which carries no tenant at
// all. The honest reading of that is the deployment's own brand org — one key, one
// deployment, one tenant — and stating it needs a context with NO REQUEST behind it,
// because zip reads a stated caller only there. That is not an obstacle to work
// around, it is the anti-laundering rule: on a request context the header wins, so
// no handler can ever assert an org a caller did not arrive with.
//
// Detaching would also drop the client's disconnect, which on a code run means a
// sandbox executing for nobody. AfterFunc puts exactly that one thing back: the
// values are the deployment's, the cancellation is still the request's.
func callCtx(ctx context.Context, org string) (context.Context, func()) {
	if cloud.Who(ctx).Org != "" {
		return context.WithCancel(ctx)
	}
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
		if f.StorageSessionID != "" {
			return f.StorageSessionID
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
	ctx, done := callCtx(c.Context(), brandOrg)
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
	ctx, done := callCtx(c.Context(), brandOrg)
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
	ctx, done := callCtx(c.Context(), brandOrg)
	defer done()
	b, err := cloud.Ask[plane.PathIn, plane.Blob](ctx, peer, plane.SandboxRead, &plane.PathIn{ID: sid})
	if err != nil {
		return err
	}
	// One stat pass for the whole listing, not one call per entry: `lastModified` is
	// what the client reads, and asking the sandbox once for every mtime beats N
	// round trips through the apiserver to assemble the same table.
	stamps := modtimes(ctx, sid)
	out := make([]listing, 0, len(b.Entries))
	for _, e := range b.Entries {
		out = append(out, listing{Name: sid + "/" + e, LastModified: stamps[e]})
	}
	return c.JSON(http.StatusOK, out)
}

// modtimes reads every entry's mtime in one command, as RFC3339 so the client's
// Date parse of it is unambiguous. A sandbox that cannot answer yields an empty
// table rather than a failed listing: the names are the answer, the stamps are the
// freshness hint beside them.
func modtimes(ctx context.Context, sid string) map[string]string {
	ran, err := cloud.Ask[plane.RunIn, plane.Ran](ctx, peer, plane.SandboxRun, &plane.RunIn{
		ID: sid, Argv: []string{"sh", "-c",
			`find . -maxdepth 1 -mindepth 1 -exec date -u -r {} +%Y-%m-%dT%H:%M:%SZ \; -exec echo {} \;`}})
	if err != nil || ran.ExitCode != 0 {
		return map[string]string{}
	}
	out, rows := map[string]string{}, strings.Split(ran.Stdout, "\n")
	for i := 0; i+1 < len(rows); i += 2 {
		out[strings.TrimPrefix(strings.TrimSpace(rows[i+1]), "./")] = strings.TrimSpace(rows[i])
	}
	return out
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

// guard enforces that key in constant time. Unset ⇒ 503 (fail closed, never open);
// wrong ⇒ 401. It wraps the whole surface, so no route can be added past it.
func guard(next zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		want := apiKey()
		if want == "" {
			return zip.Errorf(http.StatusServiceUnavailable, "code execution not configured")
		}
		got := strings.TrimSpace(c.Header("X-API-Key"))
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return zip.Errorf(http.StatusUnauthorized, "invalid api key")
		}
		return next(c)
	}
}

// Mount registers the code-interpreter surface.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("exec.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("exec.Mount: nil deps.Logger")
	}
	if b := strings.TrimSpace(deps.Brand); b != "" {
		brandOrg = b
	}
	// ONE credential check for the whole surface, installed as middleware rather
	// than wrapped around each handler — because a TYPED op takes no handler chain,
	// so a per-route wrap could not have covered POST /v1/exec at all. cloud's scope
	// already bounds this to the subsystem's own prefixes; the `owned` check keeps it
	// true when the router is a bare app, which is what a test mounts on.
	app.Use(zip.H(func(c *zip.Ctx) error {
		if !owned(c.Path()) {
			return c.Continue()
		}
		return guard(func(c *zip.Ctx) error { return c.Continue() })(c)
	}))

	zip.Post[CodeRun, CodeResult](app, Path, run,
		zip.WithSummary("Run a code snippet in a sandboxed interpreter"))
	app.Post(Path+"/programmatic", programmatic)
	app.Post("/v1/upload", upload)
	app.Get("/v1/download/*", download)
	app.Get("/v1/files/:sid", files)

	deps.Logger.New("subsystem", "exec").Info("code interpreter mounted over sandboxes",
		"peer", peer, "brandOrg", brandOrg, "langs", len(langs))
	return nil
}

// prefixes are the /v1 segments this subsystem owns. The guard reads it, so a route
// added under any of them is credential-checked without anyone remembering to.
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
