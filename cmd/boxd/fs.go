// The box's filesystem surface: list, read, write, delete, search.
//
// Every path a caller supplies is PROJECT-RELATIVE and is resolved through
// `resolve`, which is the only function in this file allowed to produce an
// absolute path. That is the containment property this file is responsible for:
// boxd runs untrusted code by design, but its own HTTP surface must not be a
// second way out of the project directory. `../../etc/shadow` and a symlink
// pointing at /etc both come back 400, not a file.
//
// NOT here: patch. See wire.PatchNote — update/rewrite semantics are defined
// once, in the client, and a Go copy here would be an untested second
// definition of the same fact on the far side of a network boundary.
package main

import (
	"bufio"
	"encoding/base64"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/apps/sandbox/wire"
)

// skip is what never appears in a listing or a search: build output and vendor
// trees. Not a security control — a caller can still read node_modules by name.
// It is an ATTENTION control: an agent handed 40,000 paths finds nothing, and
// the first thing every one of these clients did was filter this list itself.
// Doing it box-side means the tree never crosses the network to be discarded.
var skip = map[string]bool{
	".git": true, "node_modules": true, ".next": true, "dist": true,
	"build": true, "target": true, ".venv": true, "__pycache__": true,
	".cache": true, ".turbo": true, "vendor": true,
}

// resolve turns a project-relative request path into an absolute path inside
// the workdir, or an error. It is deliberately paranoid in three stages:
// lexical cleaning, prefix containment, and — for paths that already exist —
// symlink evaluation, because a symlink created by a previous run is a path
// that is textually inside the box and physically outside it.
func (b *box) resolve(rel string) (string, bool) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return b.workdir, true
	}
	clean := path.Clean("/" + strings.ReplaceAll(rel, `\`, "/"))
	abs := filepath.Join(b.workdir, filepath.FromSlash(clean))
	if !within(b.workdir, abs) {
		return "", false
	}
	// If it exists, follow it: a symlink is only a containment hole once it
	// resolves outside. A path that does not exist yet (a create) has nothing to
	// evaluate and the lexical check above is the whole answer.
	if real, err := filepath.EvalSymlinks(abs); err == nil && !within(b.workdir, real) {
		return "", false
	}
	return abs, true
}

func within(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(os.PathSeparator))
}

// relOf is resolve's inverse: an absolute path back to the "/"-rooted,
// project-relative form every wire.Entry carries. Callers never see /work.
func (b *box) relOf(abs string) string {
	r, err := filepath.Rel(b.workdir, abs)
	if err != nil {
		return abs
	}
	return "/" + filepath.ToSlash(r)
}

// fsList walks the project. depth<=0 means the whole tree (minus `skip`);
// depth=1 is one directory. The cap is a hard limit, not a page: a caller that
// hits it is asking the wrong question and a truncated 50k-entry answer is not
// more useful than a small one.
func (b *box) fsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	root, ok := b.resolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "path escapes the project")
		return
	}
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	limit := envInt("BOX_LIST_LIMIT", 20000)

	out := make([]wire.Entry, 0, 256)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable subtree is skipped, never fatal
		}
		if p == root {
			return nil
		}
		if d.IsDir() && skip[d.Name()] {
			return filepath.SkipDir
		}
		rel := b.relOf(p)
		if depth > 0 && strings.Count(strings.Trim(rel, "/"), "/")+1 > depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil // callers want files; the paths carry the structure
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		out = append(out, wire.Entry{
			Path: rel, Size: info.Size(), Mode: info.Mode().Perm().String(), IsDir: false,
		})
		if len(out) >= limit {
			return io.EOF // sentinel: stop the walk, not an error to report
		}
		return nil
	})
	if err != nil && err != io.EOF {
		writeErr(w, http.StatusInternalServerError, "list: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wire.ListResult{Entries: out})
}

// fsRead streams a file's BYTES, not JSON. A source file, a generated PNG and a
// 40 MB core dump are all legitimate answers here, and wrapping bytes in a JSON
// string would base64 every one of them for the benefit of none.
func (b *box) fsRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	abs, ok := b.resolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "path escapes the project")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		// 404 for a missing file specifically: every client of this treats
		// "not there" as null and anything else as a failure, and collapsing
		// the two makes a broken box look like an empty project.
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "no such file")
			return
		}
		writeErr(w, http.StatusInternalServerError, "read: "+err.Error())
		return
	}
	defer func() { _ = f.Close() }()
	if st, serr := f.Stat(); serr == nil && st.IsDir() {
		writeErr(w, http.StatusBadRequest, "path is a directory")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (b *box) fsWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeErr(w, http.StatusMethodNotAllowed, "POST or PUT only")
		return
	}
	var req wire.WriteRequest
	if err := bind(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "body: "+err.Error())
		return
	}
	abs, ok := b.resolve(req.Path)
	if !ok || abs == b.workdir {
		writeErr(w, http.StatusBadRequest, "path escapes the project")
		return
	}
	data := []byte(req.Content)
	if req.ContentB64 != "" {
		d, err := base64.StdEncoding.DecodeString(req.ContentB64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "contentB64: "+err.Error())
			return
		}
		data = d
	}
	if err := os.MkdirAll(filepath.Dir(abs), dirMode); err != nil {
		writeErr(w, http.StatusInternalServerError, "mkdir: "+err.Error())
		return
	}
	mode := fileMode
	if req.Mode != "" {
		if m, err := strconv.ParseUint(req.Mode, 8, 32); err == nil {
			mode = os.FileMode(m).Perm()
		}
	}
	if err := os.WriteFile(abs, data, mode); err != nil {
		writeErr(w, http.StatusInternalServerError, "write: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fsRoot serves the bare /v1/box/fs address, which exists for exactly one verb.
func (b *box) fsRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "DELETE only")
		return
	}
	abs, ok := b.resolve(r.URL.Query().Get("path"))
	if !ok || abs == b.workdir {
		// Refusing to delete the workdir itself is not paranoia: `path=` empty
		// resolves to the root, and an rm -rf of the project by omitting a query
		// parameter is the kind of thing that only happens once.
		writeErr(w, http.StatusBadRequest, "path escapes the project")
		return
	}
	if err := os.RemoveAll(abs); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fsSearch greps the checkout ON THE BOX. This is the one operation where a box
// is not merely equivalent to an in-process map but categorically better: the
// tree never crosses the network to be searched and discarded.
//
// The default is a case-insensitive SUBSTRING, not a regex, and `regex=1` is
// opt-in — a model that emits `(a+)+$` against a large tree should not be able
// to wedge the box it is running in.
func (b *box) fsSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	q := r.URL.Query()
	needle := q.Get("q")
	if strings.TrimSpace(needle) == "" {
		writeJSON(w, http.StatusOK, wire.SearchResult{Matches: []wire.Match{}})
		return
	}
	root, ok := b.resolve(q.Get("path"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "path escapes the project")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 50
	}

	var re *regexp.Regexp
	if q.Get("regex") == "1" || q.Get("regex") == "true" {
		var err error
		if re, err = regexp.Compile(needle); err != nil {
			writeErr(w, http.StatusBadRequest, "regex: "+err.Error())
			return
		}
	}
	lower := strings.ToLower(needle)

	out := make([]wire.Match, 0, limit)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if info, ierr := d.Info(); ierr != nil || info.Size() > maxGrepFile {
			return nil // a binary or a bundle: not what anyone is grepping for
		}
		f, oerr := os.Open(p)
		if oerr != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		rel := b.relOf(p)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), maxLine)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			hit := false
			if re != nil {
				hit = re.MatchString(line)
			} else {
				hit = strings.Contains(strings.ToLower(line), lower)
			}
			if !hit {
				continue
			}
			out = append(out, wire.Match{Path: rel, Line: n, Text: truncate(strings.TrimSpace(line), 200)})
			if len(out) >= limit {
				return io.EOF
			}
		}
		return nil
	})
	writeJSON(w, http.StatusOK, wire.SearchResult{Matches: out})
}

const (
	maxGrepFile = 2 << 20 // 2 MiB — above this it is a bundle, not source
	maxLine     = 1 << 20
)

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
