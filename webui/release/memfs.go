package release

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"time"
)

// memFS is a whole console bundle in RAM, keyed by its site-relative path
// ("index.html", "_next/static/css/bdec3a94ead6ad5f.css").
//
// It exists so a request never touches S3. The alternative — a per-request GET
// against the object store — would put a network hop and a third party's
// availability under every asset of the front door, to save 9MB in a process that
// already holds far more; and it would make the console's latency the store's
// latency. A bundle is loaded whole or not at all, so what is mounted is always
// one complete release rather than a mixture of two.
//
// Directories are not synthesized. fs.Stat on a directory therefore misses, which
// is exactly what the console handler wants: a request for a directory is not a
// file, and falls through to the SPA shell.
type memFS map[string]*memFile

// memFile is one immutable object of a loaded release.
type memFile struct {
	name string
	data []byte
	mod  time.Time
}

// Open implements fs.FS. The returned file is an independent reader over shared,
// never-mutated bytes, so concurrent requests for the same asset cost one map
// lookup and a bytes.Reader — no copy of the body.
func (m memFS) Open(name string) (fs.File, error) {
	f, ok := m[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &openFile{Reader: bytes.NewReader(f.data), info: fileInfo{f}}, nil
}

// openFile is one open handle. It embeds *bytes.Reader for Read/Seek/ReadAt,
// which is what http.ServeContent needs to answer a conditional GET and a range
// request without the handler re-reading anything.
type openFile struct {
	*bytes.Reader
	info fileInfo
}

func (f *openFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *openFile) Close() error               { return nil }

// fileInfo reports a release object's identity. ModTime is the object's own
// LastModified from the store, so a conditional GET is answered against the
// release's real timestamp rather than this process's start time — two pods
// serving the same release agree, and a client's cached copy survives a rollout.
type fileInfo struct{ f *memFile }

func (i fileInfo) Name() string       { return path.Base(i.f.name) }
func (i fileInfo) Size() int64        { return int64(len(i.f.data)) }
func (i fileInfo) Mode() fs.FileMode  { return 0o444 }
func (i fileInfo) ModTime() time.Time { return i.f.mod }
func (i fileInfo) IsDir() bool        { return false }
func (i fileInfo) Sys() any           { return nil }

// compile-time assertions: memFS is an fs.FS whose files seek (http.ServeContent
// falls back to buffering the whole body otherwise).
var (
	_ fs.FS         = memFS(nil)
	_ fs.File       = (*openFile)(nil)
	_ io.ReadSeeker = (*openFile)(nil)
)
