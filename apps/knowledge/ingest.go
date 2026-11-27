package knowledge

import (
	azip "archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"mime"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
	luxlog "github.com/luxfi/log"
)

// ingest.go is a file's durable ingest: one workflow per version of a file on
// the process's embedded tasks engine (cloud.EmbeddedTasks), the platform's one
// durable queue. The workflow runs the stages in order — extract, toc,
// passages, graph, embed — each an activity retried with backoff on its own,
// each reading what the stage before it wrote to the org's store, so a retry
// redoes one stage and never the file. The file's record says which stage it is
// in, and the embed stage counts its progress as it goes.
//
// A file is READY when the graph stage ends: its contents, passages and links
// are written and its full text is searched. Embedding comes last because it is
// the slow stage — the embedding model costs seconds a passage — and nothing a
// person reads waits on it: the vectors fill in under a ready file, and a file
// whose embedding never succeeds stays ready, searched by its words, with the
// reason on it.
//
// A file no stage can read (a kind nothing reads, a size past the bound, no text
// in it) is STORED with the reason and the workflow ends; a stage whose retries
// run out leaves the file FAILED with the last reason. An archive is unpacked in
// its extract stage: every file inside is written to the bucket beside it and
// registered, so each gets an ingest of its own.
//
// The worker starts with the mount, so ingests queued before a restart resume.
// With no engine — an embed that failed at boot — the same stages run on a
// detached goroutine instead, without retry.

const (
	ingestQueue     = "cloud-knowledge-files"
	ingestNamespace = "default"
	// engineWait bounds how long the mount waits for the process's engine.
	engineWait = 5 * time.Minute
	// embedBatch is how many passages one embedding call carries. A round sends
	// up to embedLanes() calls at once and records what came back, so progress is
	// written as it is made and a retry resumes from the last round.
	embedBatch = 16
	// zipEntries and zipBytes bound what one archive unpacks to.
	zipEntries = 2000
	zipBytes   = 2 << 30
)

// stageBudget is how long one attempt at a stage may run. Embedding is the long
// one: a passage costs the embedding model seconds.
func stageBudget(stage string) time.Duration {
	switch stage {
	case stageEmbed:
		return 3 * time.Hour
	case stageToc, stageExtract:
		return 30 * time.Minute
	}
	return 20 * time.Minute
}

// IngestIn names one version of one file. Org rides the input because the
// engine's one namespace is shared: the input IS the tenant, as in every queue
// on this engine.
type IngestIn struct {
	Org  string `json:"org"`
	ID   string `json:"id"`
	Etag string `json:"etag"`
	// From is the stage this run starts at: empty for a new version, which
	// starts at extract, and the stage a stalled run was left in when the
	// resume sweep starts it again (resume).
	From string `json:"from,omitempty"`
}

// stagesFrom is the stages a run starting at from goes through, in order. A
// stage this build does not run starts the version over from extract.
func stagesFrom(from string) []string {
	if i := slices.Index(stages, from); i > 0 {
		return stages[i:]
	}
	return stages
}

// FileIngestWorkflow ingests one version of a file, stage by stage. A stage
// that cannot succeed leaves the file failed with its reason.
func FileIngestWorkflow(ctx workflow.Context, in IngestIn) error {
	for _, stage := range stagesFrom(in.From) {
		// The worker heartbeats a running stage on its own (at half the timeout),
		// so a process that dies mid-stage — a deploy, every time — is noticed in
		// a minute and the stage resumes from what it wrote, instead of waiting
		// out the stage's whole budget. A resumed stage costs little, so a long
		// file can outlive several deploys within its attempts.
		act := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: stageBudget(stage),
			HeartbeatTimeout:    time.Minute,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    5 * time.Second,
				BackoffCoefficient: 2.0,
				MaximumInterval:    2 * time.Minute,
				MaximumAttempts:    10,
			},
		})
		var more bool
		if err := workflow.ExecuteActivity(act, FileStageActivity, in, stage).Get(act, &more); err != nil {
			fin := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
				StartToCloseTimeout: time.Minute,
				RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, MaximumAttempts: 3},
			})
			_ = workflow.ExecuteActivity(fin, FileFailedActivity, in, stage, err.Error()).Get(fin, nil)
			return err
		}
		if !more {
			return nil
		}
	}
	return nil
}

// FileStageActivity runs one stage of one version of a file and answers whether
// the workflow goes on: false once the file is stored, ready, or superseded by
// a newer version. A file no retry can read is stored here and answers false;
// every other failure is returned for a retry.
func FileStageActivity(ctx context.Context, in IngestIn, stage string) (bool, error) {
	x := idx
	if x == nil {
		return false, errors.New("knowledge is not mounted in this process")
	}
	return x.stage(cloud.For(ctx, in.Org), in, stage)
}

// ForgetIn names one forgotten file. Org rides the input, as in IngestIn.
type ForgetIn struct {
	Org string `json:"org"`
	ID  string `json:"id"`
}

// FileForgetWorkflow removes what the index holds of a forgotten file. Its one
// activity resumes from the cursor on the file's record, so a deploy mid-way
// costs one batch.
func FileForgetWorkflow(ctx workflow.Context, in ForgetIn) error {
	act := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Hour,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    10,
		},
	})
	return workflow.ExecuteActivity(act, FileForgetActivity, in).Get(act, nil)
}

// FileForgetActivity removes a forgotten file's full-text rows a batch at a
// time, then its passages, sections and record.
func FileForgetActivity(ctx context.Context, in ForgetIn) error {
	x := idx
	if x == nil {
		return errors.New("knowledge is not mounted in this process")
	}
	return x.unindex(cloud.For(ctx, in.Org), in.Org, in.ID)
}

// unindex removes what the index holds of a forgotten file: its full-text rows
// in the index every org shares, forgetBatch at a time, moving the record's
// cursor (done of total) past each batch; then its rows here and its record.
// A file registered again meanwhile is no longer forgetting and is left to its
// ingest, whose extract forgets what remains of the old version.
func (x *indexer) unindex(ctx context.Context, org, id string) error {
	st, err := x.storeFor(org)
	if err != nil {
		return err
	}
	for {
		var (
			status      string
			done, total int
		)
		switch err := st.db.QueryRowContext(ctx, `SELECT status, done, total FROM files WHERE id=$1`, id).Scan(&status, &done, &total); {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("kb: read file: %w", err)
		case status != fileForgetting:
			return nil
		}
		if done >= total {
			break
		}
		hi := min(total, done+forgetBatch)
		if err := unlex(ctx, org, id, done, hi); err != nil {
			return err
		}
		res, err := st.db.ExecContext(ctx, `UPDATE files SET done=$1, updated=$2 WHERE id=$3 AND status=$4`,
			hi, time.Now().Unix(), id, fileForgetting)
		if err != nil {
			return fmt.Errorf("kb: record forget: %w", err)
		}
		if held(res) != nil {
			return nil // registered again during the batch
		}
	}
	tx, err := st.d.BeginWrite(ctx, st.db)
	if err != nil {
		return fmt.Errorf("kb: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM files WHERE id=$1 AND status=$2`, id, fileForgetting)
	if err != nil {
		return fmt.Errorf("kb: forget file: %w", err)
	}
	if held(res) != nil {
		return nil // registered again since the last batch
	}
	for _, q := range unrows {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return fmt.Errorf("kb: forget file: %w", err)
		}
	}
	return tx.Commit()
}

// FileFailedActivity marks a version of a file failed at a stage with reason:
// the end of a workflow whose retries ran out.
func FileFailedActivity(ctx context.Context, in IngestIn, stage, reason string) error {
	x := idx
	if x == nil {
		return errors.New("knowledge is not mounted in this process")
	}
	st, err := x.storeFor(in.Org)
	if err != nil {
		return err
	}
	return failed(ctx, st, in.ID, in.Etag, stage, reason)
}

// failed records that a stage's retries ran out. A file that was already ready
// stays ready — its words are searched — and says why search by meaning is not
// available; any earlier stage leaves the file failed.
func failed(ctx context.Context, st *store, id, etag, stage, reason string) error {
	if stage == stageEmbed {
		return st.mark(ctx, id, etag, fileReady, "", clip("searched by its words only: its vectors could not be written ("+reason+")", 500))
	}
	return st.mark(ctx, id, etag, fileFailed, stage, clip(reason, 500))
}

// stage runs one stage, recording on the file that it is running. After the
// graph stage the file is ready, and the embed stage runs under a ready file.
func (x *indexer) stage(ctx context.Context, in IngestIn, stage string) (bool, error) {
	st, err := x.storeFor(in.Org)
	if err != nil {
		return false, err
	}
	f, err := st.file(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // the record is gone; nothing is owed
	}
	if err != nil {
		return false, err
	}
	if f.etag != in.Etag {
		return false, nil // superseded: the newer version has its own run
	}
	running := fileIndexing
	if stage == stageEmbed {
		running = fileReady
	}
	if err := st.mark(ctx, f.ID, f.etag, running, stage, ""); err != nil {
		return false, err
	}
	// The stage's model calls are the registrant's, read off the record: the
	// request that registered the file is long gone.
	ctx = cloud.WithPayer(ctx, cloud.Payer{Wallet: f.wallet(in.Org), Project: f.Project})
	switch stage {
	case stageExtract:
		err = x.extractStage(ctx, st, in.Org, f)
	case stageToc:
		err = x.tocStage(ctx, st, in.Org, f)
	case stagePassages:
		err = x.passageStage(ctx, st, in.Org, f)
	case stageEmbed:
		err = x.embedStage(ctx, st, in.Org, f)
	case stageGraph:
		err = x.graphStage(ctx, st, in.Org, f)
	default:
		return false, fmt.Errorf("kb: no ingest stage %q", stage)
	}
	if errors.Is(err, errSuperseded) {
		return false, nil // forgotten or replaced mid-stage: nothing more is owed
	}
	if errors.Is(err, errSkip) {
		return false, st.mark(ctx, f.ID, f.etag, fileStoredAs, stage, clip(err.Error(), 500))
	}
	// An index of another space refuses every retry alike, and each would buy
	// a round of embeddings first: the file stays ready, searched by its words,
	// and says why until the org's index is rebuilt (POST /v1/knowledge/reindex).
	if errors.Is(err, vec.ErrMixed) {
		return false, failed(ctx, st, f.ID, f.etag, stage, err.Error())
	}
	if err != nil {
		return false, err
	}
	switch stage {
	case readyAfter:
		if err := st.mark(ctx, f.ID, f.etag, fileReady, stageEmbed, ""); err != nil {
			return false, err
		}
		announce(in.Org, fileIndexed, f)
	case stages[len(stages)-1]:
		return false, st.mark(ctx, f.ID, f.etag, fileReady, "", "")
	}
	return true, nil
}

// ---- extract ----

// extractStage reads a file a segment at a time and writes each segment's
// sections and links before it reads the next. The file's resume point (Done,
// of Total) moves past each segment in the same write, so a retry resumes where
// the last attempt stopped, and a file read from its start first forgets what
// an earlier version left. The org's bound on indexed text and the room the
// index's volume keeps free can stop it short of the end; the file is then
// clipped, and its note says how far it was read and why.
func (x *indexer) extractStage(ctx context.Context, st *store, org string, f File) error {
	if f.Done == 0 {
		if err := st.forget(ctx, org, f.ID); err != nil {
			return err
		}
		f.Chars = 0
	}
	src, err := x.segments(ctx, org, f)
	if errors.Is(err, errGone) {
		return fmt.Errorf("%w: %v", errSkip, errGone)
	}
	if err != nil {
		return err
	}
	used, err := st.orgChars(ctx, f.ID)
	if err != nil {
		return err
	}
	bound := orgText() - used
	g, err := st.grower(ctx, f)
	if err != nil {
		return err
	}
	full := fmt.Sprintf("a workspace's files are indexed for %s of text in all", size(orgText()))
	at, total, why, read := f.Done, src.total(), "", false
	for at < total {
		if int64(g.at) >= bound {
			why = full
			break
		}
		if !roomy() {
			why = "the index has no more room"
			break
		}
		seg, end, err := src.next(ctx, at)
		if errors.Is(err, errGone) {
			return fmt.Errorf("%w: %v", errSkip, errGone)
		}
		if err != nil {
			return err
		}
		if end <= at {
			return fmt.Errorf("kb: the object ends at byte %d, short of its size %d", at, total)
		}
		if over := int64(g.at+len(seg.text)) - bound; over > 0 {
			seg = head(seg, len(seg.text)-int(over))
			end, why = min(end, at+int64(len(seg.text))), full
		}
		read = read || strings.TrimSpace(seg.text) != ""
		base := g.at
		add, his := g.grow(f.Name, seg)
		for i := range seg.refs {
			seg.refs[i].at += base
		}
		if err := st.putSegment(ctx, f, add, his, seg.refs, end, total, g.at, why != ""); err != nil {
			return err
		}
		at = end
		if why != "" {
			break
		}
	}
	if g.at == 0 || !read && f.Done == 0 {
		if why != "" {
			return fmt.Errorf("%w: %s", errSkip, why)
		}
		return fmt.Errorf("%w: no text found in the file", errSkip)
	}
	note := ""
	if why != "" {
		note = fmt.Sprintf("Indexed its first %s of %s: %s.", size(at), size(total), why)
	}
	return st.note(ctx, f.ID, f.etag, "Indexed", note)
}

// orgText is how much text one org's files are indexed for in all: past it a
// file is indexed as far as the bound and says so. Every byte of text indexed
// costs the index about twelve more on its volume (passages, their terms, the
// sections they cite), so this is what one org may hold of it.
// CLOUD_FILES_ORG_TEXT states it in bytes for a deployment; 2 GiB by default.
func orgText() int64 {
	if n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("CLOUD_FILES_ORG_TEXT")), 10, 64); err == nil && n > 0 {
		return n
	}
	return 2 << 30
}

// roomy reports whether the volume the index lives on keeps its floor free: a
// fifth of it, at least 8 GiB and at most 16. A segment is written only while it
// does, so a file can never fill the volume every org's index shares. A volume
// that cannot be read is not refused.
func roomy() bool {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(cloud.DataDir(), &fs); err != nil {
		return true
	}
	bs := uint64(fs.Bsize)
	floor := min(max(fs.Blocks*bs/5, 8<<30), 16<<30)
	return fs.Bavail*bs > floor
}

// size is a byte count as a person reads it.
func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// orgChars is the text the org's other files are indexed for.
func (s *store) orgChars(ctx context.Context, id string) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT CAST(COALESCE(SUM(chars),0) AS BIGINT) FROM files WHERE id<>$1`, id).Scan(&n); err != nil {
		return 0, fmt.Errorf("kb: read text indexed: %w", err)
	}
	return n, nil
}

// head is the first n bytes of a segment, cut at a rune boundary, with the
// headings and links inside them.
func head(p parsed, n int) parsed {
	for n > 0 && n < len(p.text) && !utf8.RuneStart(p.text[n]) {
		n--
	}
	out := parsed{title: p.title, text: p.text[:n]}
	for _, m := range p.marks {
		if m.at < n {
			out.marks = append(out.marks, m)
		}
	}
	for _, r := range p.refs {
		if r.at < n {
			out.refs = append(out.refs, r)
		}
	}
	return out
}

// segments is a file's text, a segment at a time.
type segments interface {
	// next answers the segment that starts at position at — a byte of the
	// object, or of the text a whole read made — and the position after it.
	next(ctx context.Context, at int64) (parsed, int64, error)
	// total is how many positions there are: the object's size, or the length
	// of the text a whole read made.
	total() int64
}

// segSlack is how far past segText a segment of a streamed file may run to end
// on a line.
const segSlack = 64 << 10

// segments opens a file's text: a file whose lines parse on their own is read
// from the store a segment at a time, however long it is; anything else is read
// whole, up to maxFile, and its text is cut into segments.
func (x *indexer) segments(ctx context.Context, org string, f File) (segments, error) {
	first, err := objects.span(ctx, org, f.Bucket, f.Key, 0, segSlack)
	if err != nil {
		return nil, err
	}
	if kind := readAs(f.Name, f.Type, first); streams(kind) {
		s := &streamed{x: x, org: org, f: f, kind: kind}
		if kind == kindCSV {
			if i := bytes.IndexByte(first, '\n'); i >= 0 {
				s.header = first[:i+1]
			}
		}
		return s, nil
	}
	if f.Size > maxFile {
		return nil, fmt.Errorf("%w: files of this kind over %d MB are stored but not indexed", errSkip, maxFile>>20)
	}
	data, err := objects.read(ctx, org, f.Bucket, f.Key, maxFile)
	if err != nil {
		return nil, err
	}
	var p parsed
	if readAs(f.Name, f.Type, data) == kindZip {
		if f.Parent != "" {
			return nil, fmt.Errorf("%w: an archive inside an archive is stored but not unpacked", errSkip)
		}
		p, err = x.unpack(ctx, org, f, data)
	} else {
		p, err = x.extract(ctx, org, f.Project, f.Name, f.Type, data)
	}
	if err != nil {
		return nil, err
	}
	return &whole{p: p}, nil
}

// streamed reads a text file from the store a segment at a time. A CSV's
// later segments carry its header row, so each reads as the table it is part of.
type streamed struct {
	x      *indexer
	org    string
	f      File
	kind   fileKind
	header []byte
}

func (s *streamed) total() int64 { return s.f.Size }

func (s *streamed) next(ctx context.Context, at int64) (parsed, int64, error) {
	data, err := s.x.spanOf(ctx, s.org, s.f, at)
	if err != nil {
		return parsed{}, 0, err
	}
	end := at + int64(len(data))
	if at > 0 && len(s.header) > 0 {
		data = append(append([]byte(nil), s.header...), data...)
	}
	p, err := s.x.parse(ctx, s.org, s.f.Project, s.f.Name, s.f.Type, s.kind, data)
	// A segment before the last ends at the end of a line (spanOf), and its text
	// says so: parsing it alone trims that line break, which would run its last
	// word into the next segment's first.
	if err == nil && end < s.f.Size && p.text != "" && !strings.HasSuffix(p.text, "\n") {
		p.text += "\n"
	}
	return p, end, err
}

// spanOf reads the segment of f at at: segText bytes, run on to the end of their
// line (at most segSlack more), or the rest of the file.
func (x *indexer) spanOf(ctx context.Context, org string, f File, at int64) ([]byte, error) {
	data, err := objects.span(ctx, org, f.Bucket, f.Key, at, segText+segSlack)
	if err != nil {
		return nil, err
	}
	if at+int64(len(data)) >= f.Size || len(data) <= segText {
		return data, nil
	}
	cut := segText + bytes.IndexByte(data[segText:], '\n') + 1
	if cut <= segText {
		cut = segText
		for cut > 0 && !utf8.RuneStart(data[cut]) {
			cut--
		}
	}
	return data[:cut], nil
}

// whole cuts the text of a file read whole into segments at paragraph
// breaks.
type whole struct{ p parsed }

func (w *whole) total() int64 { return int64(len(w.p.text)) }

func (w *whole) next(_ context.Context, at int64) (parsed, int64, error) {
	text := w.p.text[at:]
	n := len(text)
	if n > segText+segText/2 {
		n = cutOne(text, segText)
	}
	lo, hi := int(at), int(at)+n
	seg := parsed{text: text[:n]}
	if at == 0 {
		seg.title = w.p.title
	}
	for _, m := range w.p.marks {
		if m.at >= lo && m.at < hi {
			m.at -= lo
			seg.marks = append(seg.marks, m)
		}
	}
	for _, r := range w.p.refs {
		if r.at >= lo && r.at < hi {
			r.at -= lo
			seg.refs = append(seg.refs, r)
		}
	}
	return seg, int64(hi), nil
}

// unpack writes every file in an archive to the bucket beside it — under a
// folder named for the archive — registers each, and answers the archive's own
// text: the list of what it held.
//
// Each file it holds is registered as the archive's uploader's, with no admin
// authority: nobody is behind the run to hold any. So a file at one of those
// keys that someone else manages is left as it is, its object unwritten.
func (x *indexer) unpack(ctx context.Context, org string, f File, data []byte) (parsed, error) {
	zr, err := azip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return parsed{}, fmt.Errorf("%w: open archive: %v", errSkip, err)
	}
	st, err := x.storeFor(org)
	if err != nil {
		return parsed{}, err
	}
	by := actor{subject: f.uploader, payer: f.wallet(org)}
	dir := strings.TrimSuffix(f.Key, path.Ext(f.Key))
	var (
		w     writer
		n     int
		total uint64
	)
	w.heading(1, "Contents of "+f.Name, "")
	for _, e := range zr.File {
		name := strings.TrimPrefix(e.Name, "/")
		if e.FileInfo().IsDir() || strings.HasPrefix(name, "__MACOSX/") || strings.HasPrefix(path.Base(name), ".") {
			continue
		}
		if n >= zipEntries || total+e.UncompressedSize64 > zipBytes {
			w.line(fmt.Sprintf("… and more: only the first %d files are unpacked", n))
			break
		}
		if e.UncompressedSize64 > maxPart {
			w.line(name + " — too large to unpack")
			continue
		}
		key, ok := object.Key(dir + "/" + name)
		if !ok {
			continue
		}
		switch prev, err := st.fileAt(ctx, f.Bucket, key); {
		case err == nil && by.manages(prev) != nil:
			w.line(name + " — another person's file, left as it is")
			continue
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return parsed{}, err
		}
		rc, err := e.Open()
		if err != nil {
			w.line(name + " — unreadable")
			continue
		}
		body, err := io.ReadAll(io.LimitReader(rc, maxPart+1))
		_ = rc.Close()
		if err != nil || len(body) > maxPart {
			w.line(name + " — unreadable")
			continue
		}
		if err := objects.write(ctx, org, f.Bucket, key, mime.TypeByExtension(strings.ToLower(path.Ext(name))), body); err != nil {
			return parsed{}, err
		}
		if _, err := x.register(ctx, org, f.Bucket, key, f.Project, f.ID, by); err != nil {
			return parsed{}, err
		}
		total += e.UncompressedSize64
		n++
		w.line(fmt.Sprintf("%s — %d bytes", name, len(body)))
	}
	if n == 0 {
		return parsed{}, fmt.Errorf("%w: the archive holds no files", errSkip)
	}
	return w.done(f.Name), nil
}

// ---- toc ----

// tocStage summarizes a file's sections a page at a time and records the
// entities each names: a model reads the first summaryMax sections that hold
// text, and the rest are summarized by their first sentences. A retry resumes
// after the last section summarized.
func (x *indexer) tocStage(ctx context.Context, st *store, org string, f File) error {
	_, outline, err := st.outline(ctx, f.ID)
	if err != nil {
		return err
	}
	children := map[int][]string{}
	for _, s := range outline {
		if s.parent >= 0 {
			children[s.parent] = append(children[s.parent], s.title)
		}
	}
	summarized := map[int]bool{}
	rows, err := st.db.QueryContext(ctx,
		`SELECT id FROM sections WHERE file=$1 AND (id=0 OR length(trim(body))>0) ORDER BY id LIMIT $2`, f.ID, summaryMax)
	if err != nil {
		return fmt.Errorf("kb: read sections: %w", err)
	}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("kb: read section: %w", err)
		}
		summarized[id] = true
	}
	_ = rows.Close()
	from := -1
	if err := st.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),-1) FROM sections WHERE file=$1 AND summary<>''`, f.ID).Scan(&from); err != nil {
		return fmt.Errorf("kb: read sections: %w", err)
	}
	for {
		page, err := st.page(ctx, f.ID, from)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		if err := st.putReadouts(ctx, f.ID, x.readSections(ctx, org, f.Project, f.Name, page, summarized, children)); err != nil {
			return err
		}
		from = page[len(page)-1].id
		if err := st.progress(ctx, f.ID, f.etag, int64(from+1), int64(len(outline))); err != nil {
			return err
		}
	}
}

// ---- passages ----

// passageStage cuts every section's body into passages — never across a
// section — and writes each to the org's full-text index under its path, a
// page of sections at a time. A page is written to the full-text index first
// and to the file's passages after, in one transaction, so a retry resumes
// after the last page whose passages were written and rewrites any full-text
// rows a cut-off page left under the same keys.
func (x *indexer) passageStage(ctx context.Context, st *store, org string, f File) error {
	byID, outline, err := st.outline(ctx, f.ID)
	if err != nil {
		return err
	}
	from, seq := -1, 0
	if err := st.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(section),-1), COALESCE(MAX(seq)+1,0) FROM file_passages WHERE file=$1`, f.ID).
		Scan(&from, &seq); err != nil {
		return fmt.Errorf("kb: read passages: %w", err)
	}
	for {
		page, err := st.page(ctx, f.ID, from)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return st.count(ctx, f.ID, f.etag, "passages", seq)
		}
		type row struct{ seq, section, part, lo, hi int }
		var (
			rows []row
			docs []map[string]any
		)
		for _, s := range page {
			path := trail(byID, s.id)
			for part, sp := range split(s.body) {
				rows = append(rows, row{seq, s.id, part, sp.lo, sp.hi})
				doc := map[string]any{
					"key": passageKey(f.ID, seq), "doctype": "file", "file": f.ID, "section": s.id,
					"seq": seq, "title": path, "text": s.body[sp.lo:sp.hi], "user": "",
				}
				if f.Project != "" {
					doc["project"] = f.Project
				}
				docs = append(docs, doc)
				seq++
			}
		}
		// A forgotten file's rows are removed by key up to the passages it had,
		// so nothing is written for one: the shared index is asked only while the
		// record holds this version, and the passages only in a transaction that
		// still finds it.
		if err := st.holds(ctx, f.ID, f.etag); err != nil {
			return err
		}
		for lo := 0; lo < len(docs); lo += lexicalBatch {
			if err := lexicalErr(lexical.PutAll(ctx, org, filesIndex, "key", docs[lo:min(len(docs), lo+lexicalBatch)])); err != nil {
				return fmt.Errorf("kb: write full-text rows: %w", err)
			}
		}
		tx, err := st.d.BeginWrite(ctx, st.db)
		if err != nil {
			return fmt.Errorf("kb: begin: %w", err)
		}
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM files WHERE id=$1 AND etag=$2`, f.ID, f.etag).Scan(&one); err != nil {
			_ = tx.Rollback()
			if errors.Is(err, sql.ErrNoRows) {
				return errSuperseded
			}
			return fmt.Errorf("kb: read file: %w", err)
		}
		for _, r := range rows {
			if _, err := tx.ExecContext(ctx, `INSERT INTO file_passages (file,seq,section,part,lo,hi) VALUES ($1,$2,$3,$4,$5,$6)`,
				f.ID, r.seq, r.section, r.part, r.lo, r.hi); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("kb: write passage: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("kb: write passages: %w", err)
		}
		from = page[len(page)-1].id
		if err := st.count(ctx, f.ID, f.etag, "passages", seq); err != nil {
			return err
		}
		if err := st.progress(ctx, f.ID, f.etag, int64(from+1), int64(len(outline))); err != nil {
			return err
		}
	}
}

// lexicalBatch is how many passages one write to the full-text index carries:
// the index is one store every org shares, and a batch is one transaction in
// it, so other writers take turns between batches.
const lexicalBatch = 256

// ---- embed ----

// maxVectors is how many of one file's passages are embedded, in document
// order: past it a file is searched by its words alone, and says so. Every
// question about a file is scored against its vectors, so this bounds the cost
// of a question as much as the cost of an ingest.
const maxVectors = 20_000

// embedStage embeds every passage, of the first maxVectors, that has no vector
// of the configured model, a round at a time, recording progress after each
// round. A passage is embedded under its path, so a passage that never names
// its section still means it. Only the passages a round embeds are read.
func (x *indexer) embedStage(ctx context.Context, st *store, org string, f File) error {
	if !x.enabled() {
		return nil // no vector leg in this deployment: full text is the whole index
	}
	// An index of another space refuses before a passage's vector is bought.
	if err := kbIndex.Admit(ctx, st.db, x.space); err != nil {
		return fmt.Errorf("kb: %w", err)
	}
	byID, _, err := st.outline(ctx, f.ID)
	if err != nil {
		return err
	}
	var passages int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_passages WHERE file=$1`, f.ID).Scan(&passages); err != nil {
		return fmt.Errorf("kb: count passages: %w", err)
	}
	want := min(passages, maxVectors)
	note := ""
	if passages > maxVectors {
		note = fmt.Sprintf("Searched by meaning in its first %d passages of %d, and by its words throughout.", maxVectors, passages)
	}
	if err := st.note(ctx, f.ID, f.etag, "Searched by meaning", note); err != nil {
		return err
	}
	lanes := embedLanes()
	for {
		// The passages with no vector of this space's model, and the text each is a span of.
		// The spans are bytes of the section's text and are cut in Go, where a
		// string is bytes on either engine.
		rows, err := st.db.QueryContext(ctx, `
SELECT p.seq, p.section, p.lo, p.hi, s.body
FROM file_passages p JOIN sections s ON s.file=p.file AND s.id=p.section
WHERE p.file=$1 AND p.seq<$2 AND (p.vec IS NULL OR p.model<>$3) ORDER BY p.seq LIMIT $4`,
			f.ID, maxVectors, x.space.Model, embedBatch*lanes)
		if err != nil {
			return fmt.Errorf("kb: read passages: %w", err)
		}
		var (
			seqs  []int
			texts []string
		)
		for rows.Next() {
			var (
				seq, section int
				h            hit
				body         string
			)
			if err := rows.Scan(&seq, &section, &h.lo, &h.hi, &body); err != nil {
				_ = rows.Close()
				return fmt.Errorf("kb: read passage: %w", err)
			}
			seqs = append(seqs, seq)
			texts = append(texts, trail(byID, section)+"\n\n"+h.passage(body))
		}
		_ = rows.Close()
		if len(seqs) == 0 {
			return nil
		}
		vecs, err := x.embedLanes(ctx, org, f.Project, texts)
		if err != nil {
			return fmt.Errorf("embed passages: %w", err)
		}
		sp, err := x.space.Of(vecs...)
		if err != nil {
			return fmt.Errorf("embed passages: %w", err)
		}
		tx, err := st.d.BeginWrite(ctx, st.db)
		if err != nil {
			return fmt.Errorf("kb: begin: %w", err)
		}
		if err := kbIndex.Claim(ctx, tx, sp); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("kb: write vectors: %w", err)
		}
		for i, seq := range seqs {
			if _, err := tx.ExecContext(ctx, `UPDATE file_passages SET vec=$1, model=$2 WHERE file=$3 AND seq=$4`,
				vec.Encode(vecs[i]), sp.Model, f.ID, seq); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("kb: write vector: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("kb: write vectors: %w", err)
		}
		var done int
		if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_passages WHERE file=$1 AND model=$2 AND vec IS NOT NULL`,
			f.ID, x.space.Model).Scan(&done); err != nil {
			return fmt.Errorf("kb: count vectors: %w", err)
		}
		if err := st.count(ctx, f.ID, f.etag, "embedded", done); err != nil {
			return err
		}
		if err := st.progress(ctx, f.ID, f.etag, int64(done), int64(want)); err != nil {
			return err
		}
	}
}

// ---- the queue ----

// queue is this process's client on the engine and the worker draining it.
type queue struct {
	mu  sync.Mutex
	cli tasksclient.Client
	w   tasksworker.Worker
}

var errNoEngine = errors.New("tasks engine not ready")

// client dials the engine and starts the worker, once; an engine that is not up
// yet answers errNoEngine and is asked again next time.
func (q *queue) client() (tasksclient.Client, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cli != nil {
		return q.cli, nil
	}
	eng := cloud.EmbeddedTasks()
	if eng == nil {
		return nil, errNoEngine
	}
	cli, err := tasksclient.Dial(tasksclient.Options{Address: eng.Address(), Namespace: ingestNamespace})
	if err != nil {
		return nil, fmt.Errorf("dial engine: %w", err)
	}
	w := tasksworker.New(cli, ingestQueue, tasksworker.Options{})
	w.RegisterWorkflow(FileIngestWorkflow)
	w.RegisterActivity(FileStageActivity)
	w.RegisterActivity(FileFailedActivity)
	w.RegisterWorkflow(FileForgetWorkflow)
	w.RegisterActivity(FileForgetActivity)
	if err := w.Start(); err != nil {
		cli.Close()
		return nil, fmt.Errorf("start worker: %w", err)
	}
	q.cli, q.w = cli, w
	return cli, nil
}

func (q *queue) close() {
	q.mu.Lock()
	cli, w := q.cli, q.w
	q.cli, q.w = nil, nil
	q.mu.Unlock()
	if w != nil {
		w.Stop()
	}
	if cli != nil {
		cli.Close()
	}
}

// await waits for the process's engine, then starts the worker, so ingests
// queued before a restart resume without a new upload to wake them.
func (q *queue) await(ctx context.Context, log luxlog.Logger) {
	deadline := time.Now().Add(engineWait)
	for {
		if _, err := q.client(); err == nil {
			log.Info("kb file ingest live on the durable tasks engine", "queue", ingestQueue)
			return
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			log.Warn("kb file ingest: no tasks engine; ingests run on a goroutine without retry")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// erase starts removing what the index holds of a forgotten file. With no engine
// it runs on a goroutine bound to the mount.
func (x *indexer) erase(org, id string) {
	// Both outlive the request, whose path they may be read from: a request's
	// bytes are reused by the next one, so they are copied here.
	org, id = strings.Clone(org), strings.Clone(id)
	if cli, err := x.q.client(); err == nil {
		wid := fmt.Sprintf("forget/%s/%s/%d", org, id, time.Now().UnixNano())
		_, err = cli.ExecuteWorkflow(context.Background(), tasksclient.StartWorkflowOptions{ID: wid, TaskQueue: ingestQueue}, FileForgetWorkflow, ForgetIn{Org: org, ID: id})
		if err == nil {
			return
		}
		x.log.Warn("kb: queue a file forget failed; running it inline", "org", org, "file", id, "err", err)
	}
	x.wg.Add(1)
	go func() {
		defer x.wg.Done()
		if err := x.unindex(cloud.For(x.ctx, org), org, id); err != nil {
			x.log.Warn("kb: file forget failed", "org", org, "file", id, "err", err)
		}
	}()
}

// enqueue starts the ingest of one version of a file at stage from (empty:
// extract). With no engine it runs the same stages on a goroutine bound to the
// mount.
func (x *indexer) enqueue(org string, f File, from string) {
	in := IngestIn{Org: org, ID: f.ID, Etag: f.etag, From: from}
	if cli, err := x.q.client(); err == nil {
		id := fmt.Sprintf("file/%s/%s/%d", org, f.ID, time.Now().UnixNano())
		_, err = cli.ExecuteWorkflow(context.Background(), tasksclient.StartWorkflowOptions{ID: id, TaskQueue: ingestQueue}, FileIngestWorkflow, in)
		if err == nil {
			return
		}
		x.log.Warn("kb: queue a file ingest failed; running it inline", "org", org, "file", f.ID, "err", err)
	}
	x.wg.Add(1)
	go func() {
		defer x.wg.Done()
		ctx := cloud.For(x.ctx, org)
		for _, stage := range stagesFrom(in.From) {
			sctx, cancel := context.WithTimeout(ctx, stageBudget(stage))
			more, err := x.stage(sctx, in, stage)
			cancel()
			if err != nil {
				x.log.Warn("kb: file ingest failed", "org", org, "file", f.ID, "stage", stage, "err", err)
				if st, serr := x.storeFor(org); serr == nil {
					_ = failed(context.Background(), st, f.ID, f.etag, stage, err.Error())
				}
				return
			}
			if !more {
				return
			}
		}
	}()
}

// ---- resume ----

// A file's record says what is under way: queued, a stage while indexing, embed
// while a ready file's vectors are written, forgetting. Something must be
// driving that — a workflow on the engine, or the goroutine enqueue starts
// without one — and when nothing is, the record says so for ever: a run on a
// goroutine ends with its process, a workflow whose failure could not be
// recorded ends with the record still naming its stage. The resume sweep is
// what notices.
//
// A run that is alive touches its record at least once per stage attempt: each
// attempt marks the stage as it starts, and the stages record progress as they
// go. An attempt is bounded by its stage's budget and a retry follows within
// minutes, so a record untouched for longer than the budget plus resumeGrace is
// one nothing is driving, and the sweep starts it again at the stage it names.
// Every stage resumes from what it wrote, so starting one again costs what was
// left of it.
const (
	// resumeEvery is how often the sweep looks, and how long after the mount
	// it first looks — after a restart the engine retries its own runs first.
	resumeEvery = 10 * time.Minute
	// resumeGrace is what a live run may add to a stage's budget between two
	// touches of its record: a retry's backoff, a heartbeat noticed missing, a
	// worker picking the activity up.
	resumeGrace = 10 * time.Minute
	// forgetBudget is the forget activity's own bound (FileForgetWorkflow).
	forgetBudget = 3 * time.Hour
)

// stalled answers whether nothing can be driving the work f's record says is
// under way, as of now, and the stage a run starting it again begins at.
func stalled(f File, now time.Time) (from string, stale bool) {
	budget := stageBudget(f.Stage)
	switch {
	case f.Status == fileQueued:
		from, budget = "", stageBudget(stageExtract)
	case f.Status == fileIndexing, f.Status == fileReady && f.Stage != "":
		from = f.Stage
	case f.Status == fileForgetting:
		budget = forgetBudget
	default:
		return "", false // at rest: ready, stored, failed
	}
	return from, now.Sub(time.Unix(f.Updated, 0)) > budget+resumeGrace
}

// resumeLoop runs the sweep every resumeEvery until the mount ends.
func (x *indexer) resumeLoop() {
	t := time.NewTicker(resumeEvery)
	defer t.Stop()
	for {
		select {
		case <-x.ctx.Done():
			return
		case now := <-t.C:
			x.resume(now)
		}
	}
}

// resume starts again every stalled file in the orgs this writer owns. An org
// with nothing under way is settled out of the sweep until its next use.
func (x *indexer) resume(now time.Time) {
	err := x.stores.Each(func(ns namespace.Namespace, st *store, err error) {
		if err != nil || x.ctx.Err() != nil || ns.ID() == "" || !x.stores.Owned(ns) {
			return
		}
		since := time.Now()
		files, err := st.underway(x.ctx)
		if err != nil {
			x.log.Warn("kb: resume sweep: read files", "namespace", ns, "err", err)
			return
		}
		if len(files) == 0 {
			x.stores.Settle(ns, since)
			return
		}
		for _, f := range files {
			from, stale := stalled(f, now)
			if !stale {
				continue
			}
			// The touch is what keeps the next pass from starting it twice while
			// this run waits for a worker.
			if err := st.mark(x.ctx, f.ID, f.etag, f.Status, f.Stage, f.Error); err != nil {
				x.log.Warn("kb: resume sweep: touch file", "namespace", ns, "file", f.ID, "err", err)
				continue
			}
			// The run is told the org as the file was registered by it. A record
			// from before records carried it is named by the store's own name,
			// which is that org only when folding it changes nothing.
			if f.org == "" {
				if n, err := principal.OrgNamespace(ns.ID(), ""); err != nil || n != ns {
					x.log.Warn("kb: file stalled with nothing driving it, and its record does not name its org; register it again to restart it",
						"namespace", ns, "file", f.ID, "status", f.Status, "stage", f.Stage)
					continue
				}
				f.org = ns.ID()
			}
			x.log.Warn("kb: file stalled with nothing driving it; starting it again",
				"org", f.org, "file", f.ID, "status", f.Status, "stage", f.Stage, "idle", now.Sub(time.Unix(f.Updated, 0)).Round(time.Second))
			if f.Status == fileForgetting {
				x.erase(f.org, f.ID)
				continue
			}
			x.enqueue(f.org, f, from)
		}
	})
	if err != nil && x.ctx.Err() == nil {
		x.log.Warn("kb: resume sweep", "err", err)
	}
}

// logger is this process's knowledge logger, or the default where knowledge is
// not mounted.
func logger() luxlog.Logger {
	if x := idx; x != nil && x.log != nil {
		return x.log
	}
	return luxlog.Default()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// embedLanes is how many embedding calls this process keeps in flight for
// ingest: the embedding service's replicas each answer one call at a time, so
// as many calls as it has replicas keep all of them busy. CLOUD_EMBED_LANES
// states it for a deployment; four by default.
func embedLanes() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CLOUD_EMBED_LANES"))); err == nil && n > 0 {
		return min(n, 64)
	}
	return 4
}

// ingestLanes admits that many ingest calls at once across every file this
// process is embedding, so files share the lanes and a question's embedding —
// which takes no lane — never queues behind a whole file.
var ingestLanes = make(chan struct{}, embedLanes())

// embedLanes embeds texts in calls of embedBatch, each in a lane, and answers
// the vectors in order.
func (x *indexer) embedLanes(ctx context.Context, org, project string, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	errs := make(chan error, (len(texts)+embedBatch-1)/embedBatch)
	var wg sync.WaitGroup
	for lo := 0; lo < len(texts); lo += embedBatch {
		hi := min(lo+embedBatch, len(texts))
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			select {
			case ingestLanes <- struct{}{}:
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
			defer func() { <-ingestLanes }()
			vecs, err := x.embed(ctx, org, project, texts[lo:hi])
			if err != nil {
				errs <- err
				return
			}
			copy(out[lo:hi], vecs)
		}(lo, hi)
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return nil, err
	}
	return out, nil
}
