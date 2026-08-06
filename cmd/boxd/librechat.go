// The code-interpreter contract we do not own.
//
// `@librechat/agents`' CodeExecutor fixes these five paths, this request shape
// and this response shape. hanzo.chat's execute_code tool is that client, and
// /v1/functions invoke speaks the same shape on purpose so there is ONE
// executor rather than one per caller. We cannot rename any of it, so it is
// spelled here, verbatim, beside the code that must not rename it — and NOT in
// apps/sandbox/wire, which is the contract we do own.
//
//	POST /v1/exec               {lang, code, files?, args?} → {session_id, stdout, stderr, files:[{name}]}
//	POST /v1/exec/programmatic  same
//	POST /v1/upload             multipart, into a session
//	GET  /v1/download/{id}      the bytes of one artifact
//	GET  /v1/files/{sid}        what a session holds
//
// A "session" here is one directory under /tmp with a random name. It is NOT a
// tenant boundary and must never be treated as one — separation between orgs is
// the pod, and the whole exec pool is one pod per call. The session exists so a
// chat turn can upload a CSV, run a script over it and download the plot.
//
// PATH NOTE, and a live bug this fixes: the two existing consumers disagreed
// about whether the upstream is asked for `/exec` or `/v1/exec`. apps/exec is a
// path-preserving reverse proxy, so it asks for `/v1/exec`; apps/functions built
// `upstream + "/exec"`. No single value of CODE_EXEC_UPSTREAM satisfied both.
// Serving under /v1/ and fixing the one-line join in apps/functions/invoke.go
// settles it in the direction that needs no `/api/` and no path rewrite.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/sandbox/wire"
)

// execRequest is the client's shape. `files` are names already uploaded into the
// session; `args` land on stdin, which is how /v1/functions passes its input.
type execRequest struct {
	Lang      string   `json:"lang"`
	Code      string   `json:"code"`
	Args      []string `json:"args,omitempty"`
	Files     []any    `json:"files,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
	UserID    string   `json:"user_id,omitempty"`
}

type execFile struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
	Path string `json:"path,omitempty"`
}

// uploadFile is what POST /v1/upload returns per file. The field names are NOT
// ours to choose: the client reads result.files[0].fileId
// (chat api/server/services/Files/Code/crud.js:112). Emitting {name,id} makes
// the identifier "<sid>/undefined", which fails silently — the upload appears to
// succeed and the file is unreachable forever after.
type uploadFile struct {
	FileID   string `json:"fileId"`
	Filename string `json:"filename"`
}

// sessionFile is one element of GET /v1/files/{sid}, which is a BARE ARRAY.
// process.js:294 does response.data.find((f) => f.name.startsWith(path))?.lastModified
// — so `name` carries the "<sid>/<fileId>" prefix the caller matches on, and
// lastModified must be present or every lookup returns undefined.
type sessionFile struct {
	Name         string `json:"name"`
	LastModified string `json:"lastModified"`
}

type execResponse struct {
	SessionID string     `json:"session_id"`
	Stdout    string     `json:"stdout"`
	Stderr    string     `json:"stderr"`
	Files     []execFile `json:"files"`
}

// langs maps the client's language ids to how they are run. The set is CLOSED:
// an unknown lang is 400, never a shell fallback, because "run whatever string
// arrives in `lang`" is a command-injection surface with a friendly name.
var langs = map[string][]string{
	"py": {"python3", "-"}, "python": {"python3", "-"},
	"js": {"node", "-"}, "node": {"node", "-"}, "javascript": {"node", "-"},
	"ts": {"bun", "run", "-"}, "typescript": {"bun", "run", "-"},
	"sh": {"/bin/sh", "-s"}, "bash": {"/bin/bash", "-s"}, "shell": {"/bin/sh", "-s"},
	"go": {"go", "run", "-"},
	"rb": {"ruby", "-"}, "ruby": {"ruby", "-"},
}

// sessions is the session directory registry. Sessions live under /tmp, which
// in a box is a tmpfs with a size limit, so a runaway session cannot fill the
// project volume; they are reaped by TTL and on process exit.
type sessions struct {
	mu   sync.Mutex
	root string
	seen map[string]time.Time
}

func newSessions() *sessions {
	root := envOr("BOX_SESSION_DIR", filepath.Join(os.TempDir(), "boxsess"))
	_ = os.MkdirAll(root, dirMode)
	s := &sessions{root: root, seen: map[string]time.Time{}}
	go s.reap()
	return s
}

func (s *sessions) dir(id string) (string, string, bool) {
	if id == "" {
		id = newID()
	}
	// A session id reaches us from a URL path and a request body. It names a
	// directory, so it is validated as an id and never as a path — one "../" here
	// would turn /v1/files/{sid} into an arbitrary directory listing.
	if !validID(id) {
		return "", "", false
	}
	p := filepath.Join(s.root, id)
	if err := os.MkdirAll(p, dirMode); err != nil {
		return "", "", false
	}
	s.mu.Lock()
	s.seen[id] = time.Now()
	s.mu.Unlock()
	return id, p, true
}

func (s *sessions) reap() {
	ttl := time.Duration(envInt("BOX_SESSION_TTL_SEC", 3600)) * time.Second
	for range time.Tick(5 * time.Minute) {
		cut := time.Now().Add(-ttl)
		s.mu.Lock()
		for id, at := range s.seen {
			if at.Before(cut) {
				_ = os.RemoveAll(filepath.Join(s.root, id))
				delete(s.seen, id)
			}
		}
		s.mu.Unlock()
	}
}

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (b *box) librechatRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/exec", b.lcExec)
	mux.HandleFunc("/v1/exec/programmatic", b.lcExec)
	mux.HandleFunc("/v1/upload", b.lcUpload)
	mux.HandleFunc("/v1/download/", b.lcDownload)
	mux.HandleFunc("/v1/files/", b.lcFiles)
}

func (b *box) lcExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req execRequest
	if err := bind(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "body: "+err.Error())
		return
	}
	argv, ok := langs[strings.ToLower(strings.TrimSpace(req.Lang))]
	if !ok {
		writeErr(w, http.StatusBadRequest, "unsupported lang: "+req.Lang)
		return
	}
	sid, dir, ok := b.sessions.dir(req.SessionID)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid session_id")
		return
	}
	before := listNames(dir)

	// The code goes in on STDIN, never as an argv element and never as a file
	// the interpreter is pointed at by a caller-supplied name. `go run -` is the
	// odd one out and needs a real file, so it gets one, inside the session.
	req.Code = strings.ReplaceAll(req.Code, "\r\n", "\n")
	er := wire.ExecRequest{
		Argv: argv, Stdin: req.Code,
		TimeoutSec: envInt("BOX_EXEC_TIMEOUT_SEC", 120),
		Env:        map[string]string{"HOME": dir},
	}
	if argv[0] == "go" {
		f := filepath.Join(dir, "main.go")
		if err := os.WriteFile(f, []byte(req.Code), fileMode); err != nil {
			writeErr(w, http.StatusInternalServerError, "stage: "+err.Error())
			return
		}
		er.Argv, er.Stdin = []string{"go", "run", f}, ""
	}
	if len(req.Args) > 0 && er.Stdin == "" {
		er.Argv = append(er.Argv, req.Args...)
	} else if len(req.Args) > 0 {
		// stdin already carries the code, so args cannot also ride stdin. They
		// become argv, which is where a program looks for them anyway.
		er.Argv = append(er.Argv, "--")
		er.Argv = append(er.Argv, req.Args...)
	}

	// runIn, not runCmd: the session directory is one THIS process minted under
	// /tmp, not a path the caller named, so it is not the caller's cwd to be
	// contained under the project root.
	res, err := b.runIn(r.Context(), dir, er)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The contract has no exit-code field, so a non-zero exit is reported the
	// only way it can be: through stderr, at 200. That is the client's model —
	// it shows stderr to the model and lets it decide.
	stderr := res.Stderr
	if res.TimedOut {
		stderr = strings.TrimSpace(stderr + "\nexecution timed out")
	}
	writeJSON(w, http.StatusOK, execResponse{
		SessionID: sid, Stdout: res.Stdout, Stderr: stderr,
		Files: newFiles(dir, before),
	})
}

func (b *box) lcUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "multipart: "+err.Error())
		return
	}
	sid, dir, ok := b.sessions.dir(r.FormValue("session_id"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid session_id")
		return
	}
	out := []uploadFile{}
	for _, hs := range r.MultipartForm.File {
		for _, h := range hs {
			// filepath.Base is the whole containment story for an upload name:
			// the client controls it, and "../../etc/cron.d/x" is a real thing
			// that gets tried.
			name := filepath.Base(h.Filename)
			if name == "." || name == "/" || name == ".." {
				continue
			}
			src, err := h.Open()
			if err != nil {
				continue
			}
			data := make([]byte, 0, h.Size)
			buf := make([]byte, 32*1024)
			for {
				n, rerr := src.Read(buf)
				data = append(data, buf[:n]...)
				if rerr != nil {
					break
				}
			}
			_ = src.Close()
			if os.WriteFile(filepath.Join(dir, name), data, fileMode) == nil {
				out = append(out, uploadFile{FileID: name, Filename: name})
			}
		}
	}
	// message:"success" is load-bearing, not decorative: crud.js:108 does
	// `if (result.message !== 'success') throw`, so omitting it fails EVERY
	// upload with "Error uploading file: undefined".
	writeJSON(w, http.StatusOK, map[string]any{"message": "success", "session_id": sid, "files": out})
}

// lcDownload serves /v1/download/{id} where id is "{sid}/{name}" — the id this
// box mints in lcExec and lcUpload, so it round-trips by construction.
func (b *box) lcDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/download/")
	sid, name, found := strings.Cut(id, "/")
	if !found || !validID(sid) {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	name = filepath.Base(name)
	p := filepath.Join(b.sessions.root, sid, name)
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such file")
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, time.Time{}, f)
}

func (b *box) lcFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	sid := strings.TrimPrefix(r.URL.Path, "/v1/files/")
	if !validID(sid) {
		writeErr(w, http.StatusBadRequest, "bad session id")
		return
	}
	dir := filepath.Join(b.sessions.root, sid)
	out := []sessionFile{}
	for _, n := range listNames(dir) {
		mod := ""
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil {
			mod = fi.ModTime().UTC().Format(time.RFC3339)
		}
		out = append(out, sessionFile{Name: sid + "/" + n, LastModified: mod})
	}
	// A BARE ARRAY. ProgrammaticToolCalling guards with Array.isArray and
	// process.js:294 calls .find directly, so wrapping this in an object makes
	// the session look permanently empty rather than erroring.
	writeJSON(w, http.StatusOK, out)
}

func listNames(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// newFiles is what the run PRODUCED — the delta, not the listing. A session that
// already held an uploaded CSV must not report it as an artifact of every
// subsequent run, or the chat client re-attaches the same file forever.
func newFiles(dir string, before []string) []execFile {
	had := make(map[string]bool, len(before))
	for _, n := range before {
		had[n] = true
	}
	sid := filepath.Base(dir)
	out := []execFile{}
	for _, n := range listNames(dir) {
		if !had[n] && n != "main.go" {
			out = append(out, execFile{Name: n, ID: sid + "/" + n})
		}
	}
	return out
}
