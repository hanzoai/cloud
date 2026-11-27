package knowledge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/zap-proto/zip"
)

// opsLog is a Markdown log about 2.3 segments long. Its first fact is in the
// first segment, its last in the third; "## Week 9" opens just before the
// first segment ends and its "### Incident" sits past that end, so the
// heading's section is open across the cut.
func opsLog() (string, int) {
	var b strings.Builder
	b.WriteString("# Ops log\n\nThe first fact: the relay in Lisbon was retired in March.\n\n")
	week := 0
	line := func() {
		fmt.Fprintf(&b, "Routine entry %d for week %d: queues drained, nothing of note.\n", b.Len(), week)
	}
	for b.Len() < segText-200<<10 {
		if b.Len() > week*450<<10 {
			week++
			fmt.Fprintf(&b, "\n## Week %d\n\n", week)
		}
		line()
	}
	week++
	fmt.Fprintf(&b, "\n## Week %d\n\n", week)
	for b.Len() < segText+300<<10 {
		line()
	}
	incident := b.Len()
	b.WriteString("\n### Incident\n\nThe vault combination was rotated to seven three nine.\n\n")
	for b.Len() < 2*segText+segText/3 {
		line()
	}
	b.WriteString("\nThe last fact: the archive moved to the Oslo region.\n")
	return b.String(), incident
}

func TestAFileLongerThanASegmentIsIndexedWhole(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		text, _ := opsLog()
		f := ingestOne(t, app, mem, "ops.md", "text/markdown", []byte(text))
		// Each segment is read as text on its own, so the whitespace at its two
		// ends is not counted: a few bytes short of the object, never more.
		if f.Status != fileReady || f.Clipped || f.Note != "" || f.Chars > len(text) || f.Chars < len(text)-16 {
			t.Fatalf("a %d-byte file = %+v, want ready and whole", len(text), f)
		}
		st, _ := idx.storeFor("A")
		secs, err := st.sectionsOf(context.Background(), f.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		var joined strings.Builder
		byID := map[int]section{}
		for _, s := range secs {
			joined.WriteString(s.body)
			byID[s.id] = s
		}
		if strings.Join(strings.Fields(joined.String()), " ") != strings.Join(strings.Fields(text), " ") {
			t.Fatal("the sections' bodies do not tile the text")
		}
		for _, s := range secs[1:] {
			p := byID[s.parent]
			if p.level >= s.level || p.lo > s.lo || p.hi < s.hi {
				t.Fatalf("section %d %q (level %d, %d-%d) sits under %d %q (level %d, %d-%d)", s.id, s.title, s.level, s.lo, s.hi, p.id, p.title, p.level, p.lo, p.hi)
			}
		}
		for _, s := range secs {
			if s.title == "Incident" && !strings.HasPrefix(byID[s.parent].title, "Week ") {
				t.Fatalf("the heading past the first segment sits under %q, not the week still open", byID[s.parent].title)
			}
		}
		for q, want := range map[string]string{
			"relay Lisbon retired":      "Lisbon",
			"vault combination":         "seven three nine",
			"archive moved Oslo region": "Oslo",
		} {
			out := searchFilesIn(t, app, "A", map[string]any{"query": q, "files": []string{f.ID}})
			if len(out.Passages) == 0 || !strings.Contains(out.Passages[0].Text, want) {
				t.Fatalf("%q in a whole file: %+v", q, out.Passages)
			}
		}
		out := searchFilesIn(t, app, "A", map[string]any{"query": "vault combination", "files": []string{f.ID}})
		if !strings.Contains(out.Passages[0].Cite, "Week") || !strings.Contains(out.Passages[0].Cite, "Incident") {
			t.Fatalf("the fact past the cut is cited as %q", out.Passages[0].Cite)
		}
	})
}

// failOnce is an object store whose read of a span starting at or past at fails
// once, as a store that drops a connection mid-file does.
type failOnce struct {
	*memObjects
	at    int64
	mu    sync.Mutex
	hit   bool
	reads []int64 // where each segment read after the failure began
}

func (f *failOnce) span(ctx context.Context, org, bucket, key string, at, n int64) ([]byte, error) {
	f.mu.Lock()
	trip := at >= f.at && !f.hit
	if f.hit && n > segSlack {
		f.reads = append(f.reads, at)
	}
	f.hit = f.hit || trip
	f.mu.Unlock()
	if trip {
		return nil, errors.New("connection reset")
	}
	return f.memObjects.span(ctx, org, bucket, key, at, n)
}

func TestAnExtractThatFailsMidFileResumesWhereItStopped(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		text, _ := opsLog()
		flaky := &failOnce{memObjects: mem, at: segText}
		objects = flaky
		st, err := idx.storeFor("A")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		queued := func(key string) IngestIn {
			mem.put("A", "acme", key, "text/markdown", []byte(text))
			f := File{ID: fileID("acme", key), Name: key, Type: "text/markdown", Size: int64(len(text)), Bucket: "acme", Key: key,
				Status: fileQueued, etag: "v1"}
			if err := st.queue(ctx, f); err != nil {
				t.Fatal(err)
			}
			return IngestIn{Org: "A", ID: f.ID, Etag: "v1"}
		}
		cut := queued("cut.md")
		if _, err := idx.stage(ctx, cut, stageExtract); err == nil {
			t.Fatal("the first attempt read past the failure")
		}
		mid, _ := st.file(ctx, cut.ID)
		if mid.Done != segText+int64(strings.IndexByte(text[segText:], '\n'))+1 || mid.Sections == 0 {
			t.Fatalf("after the failure the file = %+v, want its first segment written and its resume point past it", mid)
		}
		if _, err := idx.stage(ctx, cut, stageExtract); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		if len(flaky.reads) == 0 || flaky.reads[0] != mid.Done {
			t.Fatalf("the retry read segments from %v, want from the resume point %d", flaky.reads, mid.Done)
		}
		flaky.reads = nil
		whole := queued("whole.md")
		if _, err := idx.stage(ctx, whole, stageExtract); err != nil {
			t.Fatalf("the clean read: %v", err)
		}
		_, a, _ := st.outline(ctx, cut.ID)
		_, b, _ := st.outline(ctx, whole.ID)
		if len(a) != len(b) {
			t.Fatalf("resumed: %d sections, read whole: %d", len(a), len(b))
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("section %d resumed %+v, read whole %+v", i, a[i], b[i])
			}
		}
		got, _ := st.file(ctx, cut.ID)
		clean, _ := st.file(ctx, whole.ID)
		if got.Chars != clean.Chars || got.Done != int64(len(text)) || got.Total != int64(len(text)) {
			t.Fatalf("the resumed file = %+v, read whole = %+v", got, clean)
		}
	})
}

func TestTheOrgBoundClipsAFileAndSaysSo(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_FILES_ORG_TEXT", strconv.Itoa(segText+segText/2))
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		text, _ := opsLog()
		f := ingestOne(t, app, mem, "ops.md", "text/markdown", []byte(text))
		if f.Status != fileReady || !f.Clipped || f.Chars != segText+segText/2 {
			t.Fatalf("a file past the org's bound = %+v, want ready and clipped at %d", f, segText+segText/2)
		}
		if !strings.Contains(f.Note, "Indexed its first 6 MB of 9 MB") || !strings.Contains(f.Note, "6 MB of text in all") {
			t.Fatalf("the note = %q", f.Note)
		}
		if out := searchFilesIn(t, app, "A", map[string]any{"query": "relay Lisbon", "files": []string{f.ID}}); len(out.Passages) == 0 {
			t.Fatal("the beginning of a clipped file is not found")
		}
		for _, p := range searchFilesIn(t, app, "A", map[string]any{"query": "archive Oslo region", "files": []string{f.ID}}).Passages {
			if strings.Contains(p.Text, "Oslo") {
				t.Fatalf("text past the bound was indexed: %q", p.Text)
			}
		}
		// Another file of the org has nothing left of the bound: stored, saying why.
		g := ingestOne(t, app, mem, "more.md", "text/markdown", []byte("# More\n\nAnything at all."))
		if g.Status != fileStoredAs || !strings.Contains(g.Error, "of text in all") {
			t.Fatalf("a file past a spent bound = %+v", g)
		}
	})
}

func TestAWholeReadIsCutIntoSegmentsWithTheirHeadingsAndLinks(t *testing.T) {
	var b strings.Builder
	var p parsed
	for b.Len() < 2*segText+segText/2 {
		if b.Len()%(300<<10) < 80 {
			p.marks = append(p.marks, mark{at: b.Len(), level: 2, title: fmt.Sprintf("At %d", b.Len())})
			p.refs = append(p.refs, ref{at: b.Len(), target: fmt.Sprintf("https://example.com/%d", b.Len())})
		}
		b.WriteString("A sentence of an extracted document, long enough to matter.\n\n")
	}
	p.text, p.title = b.String(), "Report"
	w := &whole{p: p}
	var (
		text        strings.Builder
		marks, refs int
		at          int64
		first       = true
	)
	for at < w.total() {
		seg, end, err := w.next(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		if first != (seg.title == "Report") {
			t.Fatalf("segment at %d titled %q", at, seg.title)
		}
		for _, m := range seg.marks {
			if p.text[int(at)+m.at:int(at)+m.at+1] != "A" || m.title != fmt.Sprintf("At %d", int(at)+m.at) {
				t.Fatalf("mark %q lands at %d of the text", m.title, int(at)+m.at)
			}
			marks++
		}
		refs += len(seg.refs)
		text.WriteString(seg.text)
		first, at = false, end
	}
	if text.String() != p.text || marks != len(p.marks) || refs != len(p.refs) {
		t.Fatalf("segments: %d bytes of %d, %d of %d marks, %d of %d links", text.Len(), len(p.text), marks, len(p.marks), refs, len(p.refs))
	}
}

// TestAPageOfPassagesIsOneCallToTheIndexProcess is plugin/knowledge beside
// plugin/index, as deployed: a page of a file's passages reaches the index
// process as one call carrying all of them, where a call apiece paid a round
// trip and a commit for every passage of a file.
func TestAPageOfPassagesIsOneCallToTheIndexProcess(t *testing.T) {
	if lexical.Ready() {
		t.Fatal("split test: an index is mounted in this process")
	}
	runDir(t)
	peer := zip.New(zip.Config{AppName: "index", DisableStartupMessage: true})
	batches := record[client.IndexPutAllIn](peer, "/index/put_all", client.IndexPutAll)
	singles := record[client.IndexPutIn](peer, "/index/put", client.IndexPut)
	serve(t, peer, "index")

	docs := make([]map[string]any, 300)
	for i := range docs {
		docs[i] = map[string]any{"key": passageKey("f1", i), "text": fmt.Sprintf("passage %d", i)}
	}
	if err := lexical.PutAll(context.Background(), "A", filesIndex, "key", docs); err != nil {
		t.Fatal(err)
	}
	got := batches.seen()
	if len(got) != 1 || got[0].org != "A" || len(got[0].in.Docs) != len(docs) || len(singles.seen()) != 0 {
		t.Fatalf("300 passages reached the index process as %d batch call(s) and %d single ones", len(got), len(singles.seen()))
	}
}

// pickPrompts is an AI that keeps every ToC pick it is asked to make and picks
// nothing, so a test reads exactly what a model was shown.
type pickPrompts struct {
	fakeAI
	mu   sync.Mutex
	seen []string
}

func (p *pickPrompts) ChatCompletion(_ context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	p.mu.Lock()
	p.seen = append(p.seen, req.Prompt)
	p.mu.Unlock()
	return &infer.ChatResponse{Content: `{"pick":[]}`}, nil
}

// TestThePickSeesTheSectionTheWordsMatched is a log: hundreds of shallow parts,
// and the one the search matched is far past the 80 lines a pick is shown. The
// model is shown it, and the sections above it, first.
func TestThePickSeesTheSectionTheWordsMatched(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ai := &pickPrompts{fakeAI: fakeAI{dims: 1024}}
		mountFiles(t, ai)
		secs := []section{{id: 0, parent: -1, level: 0, title: "edge.log"}}
		for i := 1; i <= 300; i++ {
			secs = append(secs, section{id: i, parent: 0, level: 1, title: fmt.Sprintf("edge.log — part %d", i+1), synthetic: true})
		}
		files := map[string]File{"f1": {ID: "f1", Name: "edge.log"}}
		idx.pick(context.Background(), "A", "", "what happened in Lagos", []string{"f1"}, files,
			map[string][]section{"f1": secs}, []phit{{file: "f1", section: 287}})
		if len(ai.seen) != 1 || !strings.Contains(ai.seen[0], "D1.S287 edge.log — part 288") {
			t.Fatalf("the pick was not shown the section the words matched:\n%s", strings.Join(ai.seen, "\n---\n"))
		}
		if lines := len(labelRe.FindAllString(ai.seen[0], -1)); lines > 80 {
			t.Fatalf("the pick was shown %d sections, past its 80", lines)
		}
	})
}

// TestRetrieveFindsALineDeepInALog asks a headingless log of thousands of parts
// about one line near its end: retrieval reads the file through the index and
// returns that line, however many parts sit at the top level, and no model is
// asked to choose among parts that have no headings.
func TestRetrieveFindsALineDeepInALog(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, noPick{readerAI: readerAI{fakeAI: fakeAI{dims: 1024}}, t: t})
		var b strings.Builder
		for i := 0; b.Len() < 3<<20; i++ {
			fmt.Fprintf(&b, "2026-09-%02dT%02d:%02d:09Z api.hanzo.ai GET /v1/models 200 %dms tenant=t%d\n", i%28+1, i%24, i%60, i%900, i%400)
		}
		b.WriteString("Deep anomaly: the Lagos edge shed every request from tenant=t808 for six minutes.\n")
		for i := 0; i < 2000; i++ {
			fmt.Fprintf(&b, "2026-09-%02dT%02d:%02d:09Z api.hanzo.ai GET /v1/models 200 %dms tenant=t%d\n", i%28+1, i%24, i%60, i%900, i%400)
		}
		f := ingestOne(t, app, mem, "edge.log", "text/plain", []byte(b.String()))
		if f.Status != fileReady || f.Sections < 200 {
			t.Fatalf("the log = %+v", f)
		}
		code, body := req(t, app, http.MethodPost, "/v1/knowledge/files/retrieve", "A",
			map[string]any{"query": "Lagos edge shed requests tenant t808", "files": []string{f.ID}})
		if code != http.StatusOK {
			t.Fatalf("retrieve: %d %s", code, body)
		}
		out := decode[retrieveOut](t, body)
		if out.Picked != "search" {
			t.Fatalf("a headingless log's sections were picked by %q", out.Picked)
		}
		for _, p := range out.Passages {
			if strings.Contains(p.Text, "Lagos edge shed every request") {
				return
			}
		}
		t.Fatalf("retrieve answered without the line: %+v", out.Passages)
	})
}

// TestAPickThatMissesTheMatchKeepsIt is the model choosing a section the words
// did not match: the section that holds the answer is read beside its pick, and
// the answer comes back.
func TestAPickThatMissesTheMatchKeepsIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}, pickWord: "Bravo"})
		var b strings.Builder
		b.WriteString("# Shift log\n\n")
		for _, name := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot", "Golf", "Hotel", "India", "Juliet", "Kilo"} {
			fmt.Fprintf(&b, "## %s\n\n", name)
			for i := 0; i < 60; i++ {
				fmt.Fprintf(&b, "2026-09-%02dT%02d:%02d:09Z api.hanzo.ai GET /v1/models 200 %dms tenant=t%d\n", i%28+1, i%24, i%60, i%900, i%400)
			}
			if name == "Juliet" {
				b.WriteString("Deep anomaly: the Lagos edge shed every request from tenant=t808 for six minutes.\n")
			}
			b.WriteString("\n")
		}
		f := ingestOne(t, app, mem, "shifts.md", "text/markdown", []byte(b.String()))
		code, body := req(t, app, http.MethodPost, "/v1/knowledge/files/retrieve", "A",
			map[string]any{"query": "Lagos edge shed requests tenant t808", "files": []string{f.ID}})
		if code != http.StatusOK {
			t.Fatalf("retrieve: %d %s", code, body)
		}
		out := decode[retrieveOut](t, body)
		if out.Picked != "model" {
			t.Fatalf("the model's pick was not taken: %q", out.Picked)
		}
		for _, p := range out.Passages {
			if strings.Contains(p.Text, "Lagos edge shed every request") {
				return
			}
		}
		t.Fatalf("the model's pick crowded out the matched line: %+v", out.Sections)
	})
}

// noPick is a model that must not be asked to pick sections.
type noPick struct {
	readerAI
	t *testing.T
}

func (n noPick) ChatCompletion(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	if strings.Contains(req.Prompt, "You choose where") {
		n.t.Error("a model was asked to pick sections")
	}
	return n.readerAI.ChatCompletion(ctx, req)
}

// TestAShortDocumentIsReadWhole is a question about a four-section report: no
// model is asked to choose among its sections, every one is read, and the
// policy it cites is reached along the graph.
func TestAShortDocumentIsReadWhole(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, noPick{readerAI: readerAI{fakeAI: fakeAI{dims: 1024}}, t: t})
		ingestOne(t, app, mem, "rotation.md", "text/markdown", []byte("# Orchard Key Rotation Policy\n\n## Rotation\n\nSigning keys rotate every 47 days.\n\n## Escalation\n\nIf a rotation fails twice, page the on-call named Ingrid Halvorsen."))
		inc := ingestOne(t, app, mem, "incident.md", "text/markdown", []byte("# Incident report\n\n## Summary\n\nThe signing key failed to rotate twice; it was 52 days old. The rotation interval and who is paged are set in rotation.md.\n\n## Timeline\n\n03:10 the rotation failed; 03:40 the retry failed.\n\n## Follow-up\n\nNobody was paged."))
		code, body := req(t, app, http.MethodPost, "/v1/knowledge/files/retrieve", "A",
			map[string]any{"query": "who should have been paged when the rotation failed twice", "files": []string{inc.ID}})
		if code != http.StatusOK {
			t.Fatalf("retrieve: %d %s", code, body)
		}
		out := decode[retrieveOut](t, body)
		if out.Picked != "whole" || len(out.Sections) != 4 {
			t.Fatalf("a short report was not read whole: %q, %d sections", out.Picked, len(out.Sections))
		}
		var timeline, graph bool
		for _, p := range out.Passages {
			timeline = timeline || p.Via == "toc" && strings.Contains(p.Text, "03:40")
			graph = graph || p.Via == "graph" && strings.Contains(p.Text, "Ingrid Halvorsen")
		}
		if !timeline || !graph {
			t.Fatalf("read whole and followed: timeline %v, graph %v", timeline, graph)
		}
	})
}
