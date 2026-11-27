package git

// logs.go — where a daemon's task logs live: the object store, one object per
// append. A replica keeps nothing of them on its own disk.
//
// A task's log is the objects under logs/<org>/<task>/, read in key order. Each is the
// JSON lines one append carried, named by the index of its first line, so a runner that
// resends an append writes the same key again and the log has the lines once. The old
// log, one file per task under <data>/git/logs/<org>/, is carried into the store once
// (carryLogs) and left beside where it was with the name ".carried".

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/apps/git/objects"
)

// legacyChunk names the carried file's object. It sorts before the first chunk that can
// follow it (a file held lines, so the next append starts past index 0) and after none.
const legacyChunk = "000000000000-file"

// logPrefix is the key prefix of one task's log.
func logPrefix(org string, task int64) (string, error) {
	if strings.ContainsAny(org, `/\.`) || org == "" {
		return "", fmt.Errorf("git runner: %q is not an org", org)
	}
	return "logs/" + org + "/" + strconv.FormatInt(task, 10) + "/", nil
}

// putLog writes the lines of one append, the first of which is line first of the log, and
// reports the bytes of content written. One record per line as JSON, so a line carrying
// anything at all — a newline, a control byte, invalid UTF-8 — reads back as the line it
// was. No lines write nothing: a task that printed nothing reads as an empty log.
// LogLine is one JSON-encoded console line appended by an actions runner.
type LogLine struct {
	Timestamp int64  `json:"timestamp"`
	Stream    string `json:"stream"`
	Content   string `json:"content"`
}

func putLog(ctx context.Context, blobs objects.Blobs, org string, task, first int64, lines []LogLine) (int64, error) {
	if len(lines) == 0 {
		return 0, nil
	}
	prefix, err := logPrefix(org, task)
	if err != nil {
		return 0, err
	}
	var buf strings.Builder
	var n int64
	enc := json.NewEncoder(&buf)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			return 0, fmt.Errorf("encode log: %w", err)
		}
		n += int64(len(l.Content)) + 1
	}
	key := fmt.Sprintf("%s%012d", prefix, first)
	if err := blobs.Put(ctx, key, strings.NewReader(buf.String()), int64(buf.Len())); err != nil {
		return 0, fmt.Errorf("write log: %w", err)
	}
	return n, nil
}

// logTail is a reader over the end of a task's log: at most the last limit bytes,
// whichever chunk they start in. It reports whether it left the beginning out.
func logTail(ctx context.Context, blobs objects.Blobs, org string, task, limit int64) (io.ReadCloser, bool, error) {
	prefix, err := logPrefix(org, task)
	if err != nil {
		return nil, false, err
	}
	var chunks []objects.Blob
	var total int64
	if err := blobs.List(ctx, prefix, func(b objects.Blob) error {
		chunks = append(chunks, b)
		total += b.Size
		return nil
	}); err != nil {
		return nil, false, err
	}
	from := max(total-limit, 0)
	t := &chain{ctx: ctx, blobs: blobs}
	var at int64
	for _, c := range chunks {
		start := at
		at += c.Size
		if at <= from {
			continue
		}
		skip := max(from-start, 0)
		t.parts = append(t.parts, part{key: c.Key, off: skip, n: c.Size - skip})
	}
	return t, from > 0, nil
}

type part struct {
	key    string
	off, n int64
}

// chain reads its parts one after another, opening each when the one before is done.
type chain struct {
	ctx   context.Context
	blobs objects.Blobs
	parts []part
	cur   io.ReadCloser
}

func (c *chain) Read(p []byte) (int, error) {
	for {
		if c.cur == nil {
			if len(c.parts) == 0 {
				return 0, io.EOF
			}
			next := c.parts[0]
			c.parts = c.parts[1:]
			r, err := c.blobs.Range(c.ctx, next.key, next.off, next.n)
			if errors.Is(err, objects.ErrNoBlob) {
				continue // collected between the listing and the read
			}
			if err != nil {
				return 0, err
			}
			c.cur = r
		}
		n, err := c.cur.Read(p)
		if errors.Is(err, io.EOF) {
			_ = c.cur.Close()
			c.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *chain) Close() error {
	if c.cur == nil {
		return nil
	}
	return c.cur.Close()
}

// legacyLog is where a task's log used to be: under the git data root, partitioned by org.
func legacyLog(dataDir, org string, task int64) string {
	return filepath.Join(dataDir, "git", "logs", org, strconv.FormatInt(task, 10)+".jsonl")
}

// carryOne copies one old log file into the store and renames it, so it is carried once.
// A file with nothing in it is only renamed. A copy that fails leaves the file as it was.
func carryOne(ctx context.Context, blobs objects.Blobs, path, org string, task int64) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	if fi.Size() > 0 {
		prefix, err := logPrefix(org, task)
		if err == nil {
			err = blobs.Put(ctx, prefix+legacyChunk, f, fi.Size())
		}
		if err != nil {
			_ = f.Close()
			return fmt.Errorf("carry %s: %w", path, err)
		}
	}
	_ = f.Close()
	return os.Rename(path, path+".carried")
}

// carryLogs carries every old log file under dataDir into the store, and reports how many.
// It is idempotent: a file is renamed once its bytes are stored, and a copy of one whose
// rename failed is the same object again.
func carryLogs(ctx context.Context, blobs objects.Blobs, dataDir string) (int, error) {
	root := filepath.Join(dataDir, "git", "logs")
	orgs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var n int
	var first error
	for _, o := range orgs {
		if !o.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, o.Name()))
		if err != nil {
			first = errors.Join(first, err)
			continue
		}
		for _, e := range files {
			base, ok := strings.CutSuffix(e.Name(), ".jsonl")
			if !ok {
				continue
			}
			task, err := strconv.ParseInt(base, 10, 64)
			if err != nil {
				continue
			}
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			if err := carryOne(ctx, blobs, filepath.Join(root, o.Name(), e.Name()), o.Name(), task); err != nil {
				first = errors.Join(first, err)
				continue
			}
			n++
		}
	}
	return n, first
}
