package knowledge

import (
	azip "archive/zip"
	"bytes"
	"compress/lzw"
	"compress/zlib"
	"context"
	"crypto/md5"
	"encoding/ascii85"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// file_test.go proves a workspace file end to end against real stores: an
// object registered in one org is read into text and structure, summarized,
// cut into passages that never cross a section, embedded, linked into the
// org's graph, found by a phrase inside it, retrieved table of contents first —
// and invisible to every other org.

// memObjects is the org's object store in memory, keyed by org, bucket and key
// exactly as the physical store separates them.
type memObjects struct {
	mu   sync.Mutex
	objs map[string]memObject
}

type memObject struct {
	data []byte
	typ  string
}

func (m *memObjects) put(org, bucket, key, typ string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[org+"\x00"+bucket+"\x00"+key] = memObject{data: data, typ: typ}
}

func (m *memObjects) get(org, bucket, key string) (memObject, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[org+"\x00"+bucket+"\x00"+key]
	return o, ok
}

func (m *memObjects) stat(_ context.Context, org, bucket, key string) (stored, error) {
	o, ok := m.get(org, bucket, key)
	if !ok {
		return stored{}, zip.ErrNotFound("no such object in that bucket")
	}
	sum := md5.Sum(o.data)
	return stored{size: int64(len(o.data)), typ: o.typ, etag: hex.EncodeToString(sum[:])}, nil
}

func (m *memObjects) read(_ context.Context, org, bucket, key string, max int64) ([]byte, error) {
	o, ok := m.get(org, bucket, key)
	if !ok {
		return nil, errGone
	}
	if int64(len(o.data)) > max {
		return nil, fmt.Errorf("too large")
	}
	return o.data, nil
}

func (m *memObjects) span(_ context.Context, org, bucket, key string, at, n int64) ([]byte, error) {
	o, ok := m.get(org, bucket, key)
	if !ok {
		return nil, errGone
	}
	at = min(at, int64(len(o.data)))
	return o.data[at:min(int64(len(o.data)), at+n)], nil
}

func (m *memObjects) write(_ context.Context, org, bucket, key, typ string, data []byte) error {
	m.put(org, bucket, key, typ, data)
	return nil
}

// readerAI is fakeAI with a reader's habits: it summarizes each numbered
// section by its title and names the entities it was told to see, describes a
// picture and transcribes a recording by the words planted for them, and picks
// the sections whose titles hold the planted keyword.
type readerAI struct {
	fakeAI
	entities            []string
	pickWord            string
	caption, transcript string
}

var (
	numberedRe = regexp.MustCompile(`(?m)^\[(\d+)\] (.*)$`)
	labelRe    = regexp.MustCompile(`(?m)^\s*(D\d+\.S\d+) (.*)$`)
)

func (r readerAI) ChatCompletion(_ context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	for _, m := range req.Messages {
		if len(m.Images) > 0 {
			return &infer.ChatResponse{Content: r.caption}, nil
		}
	}
	p := req.Prompt
	switch {
	case strings.Contains(p, "For each numbered section"):
		type entry struct {
			I       int        `json:"i"`
			Summary string     `json:"summary"`
			E       [][]string `json:"e"`
		}
		var out struct {
			S []entry `json:"s"`
		}
		locs := numberedRe.FindAllStringSubmatchIndex(p, -1)
		for k, loc := range locs {
			end := len(p)
			if k+1 < len(locs) {
				end = locs[k+1][0]
			}
			var n int
			fmt.Sscan(p[loc[2]:loc[3]], &n)
			e := entry{I: n, Summary: "Covers " + p[loc[4]:loc[5]]}
			for _, name := range r.entities {
				if strings.Contains(p[loc[0]:end], name) {
					e.E = append(e.E, []string{name, "org"})
				}
			}
			out.S = append(out.S, e)
		}
		b, _ := json.Marshal(out)
		return &infer.ChatResponse{Content: "Here you go:\n```json\n" + string(b) + "\n```"}, nil
	case strings.Contains(p, "You choose where"):
		var pick []string
		for _, m := range labelRe.FindAllStringSubmatch(p, -1) {
			title := strings.SplitN(m[2], " — ", 2)[0]
			for _, w := range strings.Split(r.pickWord, "|") {
				if w != "" && strings.Contains(title, w) {
					pick = append(pick, m[1])
					break
				}
			}
		}
		b, _ := json.Marshal(map[string]any{"pick": pick})
		return &infer.ChatResponse{Content: string(b)}, nil
	}
	return &infer.ChatResponse{Content: "x"}, nil
}

func (r readerAI) Transcribe(_ context.Context, req *infer.TranscribeRequest) (string, error) {
	if req.Model != "zen-scribe" || len(req.Audio) == 0 {
		return "", fmt.Errorf("transcribe: model %q, %d bytes", req.Model, len(req.Audio))
	}
	return r.transcript, nil
}

// mountFiles mounts framework and knowledge over memory objects, with ai as both
// the embed and the completions client.
func mountFiles(t *testing.T, ai infer.Client) (*zip.App, *memObjects) {
	t.Helper()
	return mountFilesBehind(t, ai, compose)
}

// mountFilesBehind is mountFiles with front installed ahead of every mount: the
// test composer, or the real identity boundary (manage_test.go).
func mountFilesBehind(t *testing.T, ai infer.Client, front func(*zip.App)) (*zip.App, *memObjects) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	front(app)
	planetest.Manager(t)
	planetest.Entitled(t, func(_, product string) bool { return product == Module })
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := framework.Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("framework.Use: %v", err)
	}
	t.Setenv("CLOUD_DOMAIN", "api.test")
	if err := lexical.Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("index.Use: %v", err)
	}
	if err := Use(app, cloud.Deps{Embed: ai, AI: ai}); err != nil {
		t.Fatalf("kb.Use: %v", err)
	}
	mem := &memObjects{objs: map[string]memObject{}}
	prev := objects
	objects = mem
	t.Cleanup(func() {
		objects = prev
		_ = Shutdown()
		_ = lexical.Shutdown()
		_ = framework.Shutdown()
	})
	return app, mem
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %T: %v (%s)", v, err, b)
	}
	return v
}

// settle polls a file until its ingest ends — its vectors written too — and
// answers it.
func settle(t *testing.T, app *zip.App, org, id string) File {
	t.Helper()
	// A file several segments long is embedded in about nine seconds alone and took
	// past a minute on a gate runner sharing its node, so the wait is bounded by the
	// test binary's own deadline rather than by a guess at the runner's speed: an
	// ingest that never ends still fails here, naming the file, before go test
	// panics.
	deadline := time.Now().Add(10 * time.Minute)
	if d, ok := t.Deadline(); ok {
		deadline = d.Add(-30 * time.Second)
	}
	for {
		code, b := req(t, app, http.MethodGet, "/v1/knowledge/files/"+id, org, nil)
		if code != http.StatusOK {
			t.Fatalf("get file %s in %s: %d %s", id, org, code, b)
		}
		f := decode[File](t, b)
		if (f.Status == fileReady && f.Stage != stageEmbed) || f.Status == fileFailed || f.Status == fileStoredAs {
			return f
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s still %s at %s", id, f.Status, f.Stage)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func register(t *testing.T, app *zip.App, org, bucket, key string) File {
	t.Helper()
	code, b := req(t, app, http.MethodPost, "/v1/knowledge/files", org, map[string]any{"bucket": bucket, "key": key})
	if code != http.StatusOK {
		t.Fatalf("register %s/%s in %s: %d %s", bucket, key, org, code, b)
	}
	return decode[File](t, b)
}

// ingestOne stores data at key in org A's acme bucket, registers it and waits
// for its ingest.
func ingestOne(t *testing.T, app *zip.App, mem *memObjects, key, typ string, data []byte) File {
	t.Helper()
	mem.put("A", "acme", key, typ, data)
	f := register(t, app, "A", "acme", key)
	return settle(t, app, "A", f.ID)
}

func searchFilesIn(t *testing.T, app *zip.App, org string, body map[string]any) fileSearchOut {
	t.Helper()
	code, b := req(t, app, http.MethodPost, "/v1/knowledge/files/search", org, body)
	if code != http.StatusOK {
		t.Fatalf("search in %s: %d %s", org, code, b)
	}
	return decode[fileSearchOut](t, b)
}

func tocOf(t *testing.T, app *zip.App, org, id string) tocOut {
	t.Helper()
	code, b := req(t, app, http.MethodGet, "/v1/knowledge/files/"+id+"/toc", org, nil)
	if code != http.StatusOK {
		t.Fatalf("toc of %s: %d %s", id, code, b)
	}
	return decode[tocOut](t, b)
}

func TestAFileIsStoredIndexedAndFoundByAPhrase(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var (
			mu     sync.Mutex
			stated []string
		)
		prev := capture
		capture = func(_ context.Context, in *client.EventIn) (*client.EventCaptured, error) {
			mu.Lock()
			defer mu.Unlock()
			for _, a := range in.Attributes {
				if strings.Contains(a.Value, "notes.md") {
					t.Errorf("the event carried the file's name: %+v", a)
				}
			}
			stated = append(stated, in.Name)
			return &client.EventCaptured{}, nil
		}
		t.Cleanup(func() { capture = prev })

		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		mem.put("A", "acme", "chat/notes.md", "text/markdown",
			[]byte("# Contract notes\n\nIntroductory words.\n\n## Termination\n\nThe termination notice period is ninety days.\n\n## Renewal\n\nRenewal is automatic each year."))

		f := register(t, app, "A", "acme", "chat/notes.md")
		if f.ID == "" || f.Name != "notes.md" || f.Type != "text/markdown" || f.Size == 0 || f.Status != fileQueued {
			t.Fatalf("registered file = %+v", f)
		}
		got := settle(t, app, "A", f.ID)
		if got.Status != fileReady || got.Stage != "" || got.Sections != 3 || got.Passages == 0 || got.Embedded != got.Passages {
			t.Fatalf("file did not become ready: %+v", got)
		}

		out := searchFilesIn(t, app, "A", map[string]any{"query": "termination notice period"})
		if len(out.Passages) == 0 {
			t.Fatal("search found nothing")
		}
		p := out.Passages[0]
		if !strings.Contains(p.Text, "ninety days") || p.File.ID != f.ID || p.Section.Title != "Termination" ||
			p.Cite != "notes.md › Termination › ¶1" || p.Section.Path != "Contract notes › Termination" || p.Via != "search" {
			t.Fatalf("top passage = %+v", p)
		}

		again := register(t, app, "A", "acme", "chat/notes.md")
		if again.ID != f.ID || again.Status != fileReady {
			t.Fatalf("re-register = %+v, want the ready file back", again)
		}

		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			n := len(stated)
			mu.Unlock()
			if n >= 2 || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		// Each statement is detached, so they may land in either order.
		sort.Strings(stated)
		if strings.Join(stated, ",") != fileIndexed+","+fileStored {
			t.Fatalf("stated = %v, want stored and indexed, once each", stated)
		}
	})
}

func TestTheTocFollowsTheDocumentsStructure(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		page := `<!doctype html><html><head><title>Trust Center</title><style>.x{}</style></head><body>
<nav aria-label="Site"><a href="/">Home</a> <a href="#security">Security</a></nav>
<main><h1 id="top">Acme Trust Center</h1><p>We take security seriously.</p>
<h2 id="security">Security</h2><p>All data is encrypted at rest with AES-256.</p>
<h3 id="backups">Backups</h3><p>Backups are kept for 35 days.</p>
<h2 id="privacy">Privacy</h2><p>See <a href="#backups">backups</a>.</p></main>
<footer>Copyright Acme</footer><script>var x = "not text";</script></body></html>`
		f := ingestOne(t, app, mem, "trust.html", "text/html", []byte(page))
		if f.Status != fileReady {
			t.Fatalf("html: %+v", f)
		}
		toc := tocOf(t, app, "A", f.ID)
		var titles []string
		byTitle := map[string]tocEntry{}
		for _, s := range toc.Sections {
			titles = append(titles, fmt.Sprintf("%d:%s", s.Level, s.Title))
			byTitle[s.Title] = s
			if s.Summary == "" {
				t.Fatalf("section %q has no summary", s.Title)
			}
		}
		want := "0:Trust Center|1:Site|1:Acme Trust Center|2:Security|3:Backups|2:Privacy|1:Footer"
		if strings.Join(titles, "|") != want {
			t.Fatalf("toc = %s\nwant  %s", strings.Join(titles, "|"), want)
		}
		if byTitle["Backups"].Parent != byTitle["Security"].ID || byTitle["Security"].Parent != byTitle["Acme Trust Center"].ID {
			t.Fatal("the tree does not nest Backups under Security under the page heading")
		}

		// Every passage lies inside its own section's text: none crosses a boundary.
		st, _ := idx.storeFor("A")
		secs, _ := st.sectionsOf(context.Background(), f.ID, true)
		body := map[int]string{}
		for _, s := range secs {
			body[s.id] = s.body
			if strings.Contains(s.body, "not text") || strings.Contains(s.body, ".x{}") {
				t.Fatal("script or style text was indexed")
			}
		}
		rows, err := st.db.Query(`SELECT section,lo,hi FROM file_passages WHERE file=$1`, f.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var sec, lo, hi int
			_ = rows.Scan(&sec, &lo, &hi)
			if lo < 0 || hi > len(body[sec]) || lo >= hi {
				t.Fatalf("passage [%d,%d) runs outside section %d (%d bytes)", lo, hi, sec, len(body[sec]))
			}
		}

		// The in-page link from Privacy to #backups is an edge.
		code, b := req(t, app, http.MethodGet, fmt.Sprintf("/v1/knowledge/files/%s/sections/%d", f.ID, byTitle["Privacy"].ID), "A", nil)
		if code != http.StatusOK {
			t.Fatalf("open section: %d %s", code, b)
		}
		sec := decode[sectionOut](t, b)
		if !strings.Contains(sec.Text, "See backups") || sec.Section.Path != "Trust Center › Acme Trust Center › Privacy" {
			t.Fatalf("opened section = %+v", sec)
		}
		linked := false
		for _, l := range sec.Links {
			if l.Section.Title == "Backups" && l.Kind == linkHref {
				linked = true
			}
		}
		if !linked {
			t.Fatalf("Privacy's link to #backups is not an edge: %+v", sec.Links)
		}
	})
}

func TestEveryKindOfFileIsReadIntoText(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{
			fakeAI:     fakeAI{dims: 1024},
			caption:    "A bar chart titled Quarterly Revenue showing growth in the third quarter.",
			transcript: "Welcome to the standup. The release ships on Thursday.",
		})
		cases := []struct {
			key, typ string
			data     []byte
			query    string
			want     string
			section  string
		}{
			{"report.pdf", "application/pdf", onePagePDF("Indemnification is capped at one million dollars"), "indemnification capped", "one million", ""},
			{"memo.docx", docxType, docx([2]string{"Heading1", "Merger"}, [2]string{"", "The board approved the merger"}), "board approved merger", "merger", "Merger"},
			{"main.go", "", []byte("package main\n\n// handshakeTimeout bounds the dial.\nconst handshakeTimeout = 30\n\nfunc dial() error {\n\treturn nil\n}\n"), "handshakeTimeout", "handshakeTimeout", ""},
			{"sales.csv", "text/csv", []byte("region,revenue\nNorth,120\nSouth,340\n"), "South revenue", "region: South", "Rows 1–2"},
			{"budget.xlsx", xlsxType, xlsx("Budget", [][]string{{"item", "cost"}, {"servers", "9000"}}), "servers cost", "item: servers", "Rows 1–1"},
			{"chart.png", "image/png", []byte("\x89PNG\r\n\x1a\nnot a real picture"), "quarterly revenue chart", "Quarterly Revenue", ""},
			{"standup.m4a", "audio/mp4", []byte("not a real recording"), "release ships thursday", "Thursday", ""},
		}
		for _, c := range cases {
			f := ingestOne(t, app, mem, c.key, c.typ, c.data)
			if f.Status != fileReady {
				t.Fatalf("%s: %+v", c.key, f)
			}
			out := searchFilesIn(t, app, "A", map[string]any{"query": c.query, "files": []string{f.ID}})
			if len(out.Passages) == 0 || !strings.Contains(out.Passages[0].Text, c.want) {
				t.Fatalf("%s: search %q = %+v, want a passage holding %q", c.key, c.query, out, c.want)
			}
			if c.section != "" && out.Passages[0].Section.Title != c.section {
				t.Fatalf("%s: passage is in %q, want %q", c.key, out.Passages[0].Section.Title, c.section)
			}
		}
	})
}

// A PDF's text is read whatever standard filter its page is stored under, and
// through a chain of them — ASCII85 was the one a stored document arrived in and
// came back as nothing.
func TestAPDFBehindATextFilterIsRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		a85 := func(b []byte) []byte {
			out := make([]byte, ascii85.MaxEncodedLen(len(b)))
			return append(out[:ascii85.Encode(out, b)], "~>"...)
		}
		flate := func(b []byte) []byte {
			var w bytes.Buffer
			z := zlib.NewWriter(&w)
			_, _ = z.Write(b)
			_ = z.Close()
			return w.Bytes()
		}
		lzw0 := func(b []byte) []byte { // compress/lzw is PDF's LZW with EarlyChange 0
			var w bytes.Buffer
			z := lzw.NewWriter(&w, lzw.MSB, 8)
			_, _ = z.Write(b)
			_ = z.Close()
			return w.Bytes()
		}
		for _, c := range []struct {
			key, entries string
			encode       func([]byte) []byte
		}{
			{"a85.pdf", "/Filter /ASCII85Decode", a85},
			{"hex.pdf", "/Filter /ASCIIHexDecode", func(b []byte) []byte { return []byte(hex.EncodeToString(b) + ">") }},
			{"lzw.pdf", "/Filter /LZWDecode /DecodeParms << /EarlyChange 0 >>", lzw0},
			{"chain.pdf", "/Filter [/ASCII85Decode /FlateDecode]", func(b []byte) []byte { return a85(flate(b)) }},
		} {
			f := ingestOne(t, app, mem, c.key, "application/pdf", filteredPDF("Indemnification is capped at one million dollars", c.entries, c.encode))
			if f.Status != fileReady {
				t.Fatalf("%s: %+v", c.key, f)
			}
			out := searchFilesIn(t, app, "A", map[string]any{"query": "indemnification capped", "files": []string{f.ID}})
			if len(out.Passages) == 0 || !strings.Contains(out.Passages[0].Text, "one million") {
				t.Fatalf("%s: search = %+v, want the page's text", c.key, out)
			}
		}
	})
}

func TestALongTextGetsSyntheticSections(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		var b strings.Builder
		for i := 0; b.Len() < 6*synthMax; i++ {
			fmt.Fprintf(&b, "Paragraph %d describes routine operations and nothing of note.\n\n", i)
		}
		b.WriteString("The vault combination is seven three nine.")
		f := ingestOne(t, app, mem, "log.txt", "text/plain", []byte(b.String()))
		if f.Status != fileReady || f.Sections < 6 {
			t.Fatalf("long text: %+v", f)
		}
		toc := tocOf(t, app, "A", f.ID)
		synthetic := 0
		for _, s := range toc.Sections[1:] {
			if s.Synthetic {
				synthetic++
			}
		}
		if synthetic < 5 {
			t.Fatalf("a %d-byte text with no headings has %d synthetic sections", b.Len(), synthetic)
		}
		st, _ := idx.storeFor("A")
		secs, _ := st.sectionsOf(context.Background(), f.ID, true)
		var joined strings.Builder
		for _, s := range secs {
			if len(s.body) > synthMax {
				t.Fatalf("section %d holds %d bytes, over %d", s.id, len(s.body), synthMax)
			}
			joined.WriteString(s.body)
		}
		if joined.String() != b.String() {
			t.Fatal("the sections' bodies do not tile the text")
		}
		out := searchFilesIn(t, app, "A", map[string]any{"query": "vault combination", "files": []string{f.ID}})
		if len(out.Passages) == 0 || !strings.Contains(out.Passages[0].Text, "seven three nine") {
			t.Fatalf("the tail of a long file was not found: %+v", out)
		}
	})
}

func TestAnArchiveUnpacksIntoWorkspaceFiles(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		var buf bytes.Buffer
		zw := azip.NewWriter(&buf)
		for name, text := range map[string]string{
			"docs/alpha.md":     "# Alpha\n\nThe alpha launch is in May.",
			"docs/beta.txt":     "Beta testers number forty.",
			"__MACOSX/._x":      "junk",
			"docs/.hidden.txt":  "hidden",
			"docs/inner/zz.csv": "k,v\na,1\n",
		} {
			w, _ := zw.Create(name)
			_, _ = w.Write([]byte(text))
		}
		_ = zw.Close()
		z := ingestOne(t, app, mem, "chat/bundle.zip", "application/zip", buf.Bytes())
		if z.Status != fileReady {
			t.Fatalf("archive: %+v", z)
		}
		for _, key := range []string{"chat/bundle/docs/alpha.md", "chat/bundle/docs/beta.txt", "chat/bundle/docs/inner/zz.csv"} {
			if _, ok := mem.get("A", "acme", key); !ok {
				t.Fatalf("%s was not unpacked into the bucket", key)
			}
			child := settle(t, app, "A", fileID("acme", key))
			if child.Status != fileReady || child.Parent != z.ID {
				t.Fatalf("%s: %+v", key, child)
			}
		}
		if _, ok := mem.get("A", "acme", "chat/bundle/__MACOSX/._x"); ok {
			t.Fatal("archive junk was unpacked")
		}
		out := searchFilesIn(t, app, "A", map[string]any{"query": "alpha launch May"})
		if len(out.Passages) == 0 || out.Passages[0].File.Name != "alpha.md" {
			t.Fatalf("an unpacked file is not searchable: %+v", out)
		}
	})
}

func TestRetrieveReadsTheTocThenFollowsTheGraph(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}, entities: []string{"Northwind Audit"}, pickWord: "Backups"})
		// More sections than are read whole, so a model chooses among them.
		policy := "# Security policy\n\nGeneral words about security.\n\n## Backups\n\nBackups run nightly and are reviewed by Northwind Audit. Retention is set out in retention.md.\n\n## Access\n\nAccess is granted by role."
		for _, topic := range []string{"Laptops", "Visitors", "Badges", "Printers", "Vendors", "Training"} {
			policy += "\n\n## " + topic + "\n\nRoutine words about " + strings.ToLower(topic) + "."
		}
		retention := "# Retention schedule\n\n## Backup retention\n\nBackup snapshots are kept for 35 days, then destroyed.\n\n## Logs\n\nLogs are kept for one year. Northwind Audit samples them quarterly."
		p := ingestOne(t, app, mem, "policy.md", "text/markdown", []byte(policy))
		r := ingestOne(t, app, mem, "retention.md", "text/markdown", []byte(retention))
		if p.Status != fileReady || r.Status != fileReady {
			t.Fatalf("policy %+v, retention %+v", p, r)
		}
		// policy.md was indexed before retention.md existed: its citation resolves
		// when retention.md arrives.
		code, b := req(t, app, http.MethodGet, "/v1/knowledge/files/"+p.ID+"/graph", "A", nil)
		if code != http.StatusOK {
			t.Fatalf("graph: %d %s", code, b)
		}
		g := decode[fileGraph](t, b)
		cites, shares := false, false
		for _, l := range g.Links {
			cites = cites || l.File == r.ID && l.Kind == linkCites && l.Direction == "out"
			shares = shares || l.File == r.ID && l.Kind == "entity" && strings.Contains(l.Label, "Northwind Audit")
		}
		if !cites || !shares {
			t.Fatalf("policy.md's graph = %+v, want a citation of and shared entities with retention.md", g.Links)
		}

		code, b = req(t, app, http.MethodPost, "/v1/knowledge/files/retrieve", "A", map[string]any{"query": "how long are backups kept", "files": []string{p.ID}})
		if code != http.StatusOK {
			t.Fatalf("retrieve: %d %s", code, b)
		}
		out := decode[retrieveOut](t, b)
		if out.Picked != "model" || len(out.Sections) == 0 || out.Sections[0].Section.Title != "Backups" {
			t.Fatalf("the ToC pick = %+v", out)
		}
		var toc, graph *citedPassage
		for i := range out.Passages {
			switch out.Passages[i].Via {
			case "toc":
				if toc == nil {
					toc = &out.Passages[i]
				}
			case "graph":
				if graph == nil && out.Passages[i].File.ID == r.ID {
					graph = &out.Passages[i]
				}
			}
		}
		if toc == nil || toc.Cite != "policy.md › Backups › ¶1" {
			t.Fatalf("the drilled passage = %+v", toc)
		}
		if graph == nil || graph.Why == "" {
			t.Fatalf("retrieval did not follow the graph into retention.md: %+v", out.Passages)
		}
	})
}

func TestAFileIsInvisibleToAnotherOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		f := ingestOne(t, app, mem, "secret.txt", "text/plain", []byte("the launch code is aurora borealis"))
		if f.Status != fileReady {
			t.Fatalf("A's file: %+v", f)
		}
		for _, path := range []string{"/v1/knowledge/files/" + f.ID, "/v1/knowledge/files/" + f.ID + "/toc",
			"/v1/knowledge/files/" + f.ID + "/sections/0", "/v1/knowledge/files/" + f.ID + "/graph"} {
			if code, b := req(t, app, http.MethodGet, path, "B", nil); code != http.StatusNotFound {
				t.Fatalf("B read %s: %d %s", path, code, b)
			}
		}
		if code, b := req(t, app, http.MethodPost, "/v1/knowledge/files", "B", map[string]any{"bucket": "acme", "key": "secret.txt"}); code != http.StatusNotFound {
			t.Fatalf("B registered A's object: %d %s", code, b)
		}
		for _, body := range []map[string]any{{"query": "launch code aurora", "files": []string{f.ID}}, {"query": "launch code aurora"}} {
			if out := searchFilesIn(t, app, "B", body); len(out.Passages) != 0 {
				t.Fatalf("CROSS-ORG LEAK: B found A's passages: %+v", out)
			}
			code, b := req(t, app, http.MethodPost, "/v1/knowledge/files/retrieve", "B", body)
			if code != http.StatusOK || len(decode[retrieveOut](t, b).Passages) != 0 {
				t.Fatalf("CROSS-ORG LEAK: B retrieved A's passages: %d %s", code, b)
			}
		}
		if code, _ := req(t, app, http.MethodGet, "/v1/knowledge/files", "", nil); code/100 == 2 {
			t.Fatal("an org-less caller listed files")
		}
	})
}

func TestAFileThatCannotBeReadIsStoredWithItsReason(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		f := ingestOne(t, app, mem, "blob.bin", "application/octet-stream", []byte{0, 1, 2, 3, 0xff, 0xfe, 0})
		if f.Status != fileStoredAs || !strings.Contains(f.Error, "bin files are stored but not indexed") {
			t.Fatalf("an unreadable file = %+v", f)
		}
	})
}

func TestAnEmbeddingOutageLeavesTheFileReadableByItsWords(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, downAI{fakeAI{dims: 1024}})
		f := ingestOne(t, app, mem, "notes.txt", "text/plain", []byte("words that will never be embedded"))
		if f.Status != fileReady || f.Stage != "" || !strings.Contains(f.Error, "searched by its words only") || f.Embedded != 0 {
			t.Fatalf("a file whose embedding failed = %+v, want ready, searched by its words, saying why", f)
		}
		// Its full-text rows and contents were written before the embed stage, so it is found and opened.
		if out := searchFilesIn(t, app, "A", map[string]any{"query": "never embedded"}); len(out.Passages) == 0 {
			t.Fatal("a file whose embedding failed lost its full-text leg too")
		}
	})
}

func TestAFileIsReadyBeforeItsVectors(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ai := &slowEmbed{fakeAI: fakeAI{dims: 1024}, gate: make(chan struct{})}
		app, mem := mountFiles(t, ai)
		mem.put("A", "acme", "chat/plan.md", "text/markdown", []byte("# Plan\n\n## Launch\n\nThe launch window opens on a Tuesday."))
		f := register(t, app, "A", "acme", "chat/plan.md")
		// Embedding is held: the file still reaches ready, its passages searchable.
		deadline := time.Now().Add(20 * time.Second)
		var got File
		for {
			_, b := req(t, app, http.MethodGet, "/v1/knowledge/files/"+f.ID, "A", nil)
			got = decode[File](t, b)
			if got.Status == fileReady || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if got.Status != fileReady || got.Stage != stageEmbed || got.Embedded != 0 || got.Passages == 0 {
			t.Fatalf("with embedding held, the file = %+v, want ready with its vectors pending", got)
		}
		if out := searchFilesIn(t, app, "A", map[string]any{"query": "launch window"}); len(out.Passages) == 0 {
			t.Fatal("a ready file awaiting its vectors is not found by its words")
		}
		close(ai.gate)
		done := settle(t, app, "A", f.ID)
		if done.Stage != "" || done.Embedded != done.Passages {
			t.Fatalf("once embedding is released the file = %+v, want every passage embedded", done)
		}
	})
}

func TestForgetRemovesTheIndexAndKeepsTheObject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		f := ingestOne(t, app, mem, "gone.md", "text/markdown", []byte("# Gone\n\nThe phrase is zebra crossing."))
		if code, b := req(t, app, http.MethodDelete, "/v1/knowledge/files/"+f.ID, "A", nil); code != http.StatusOK {
			t.Fatalf("forget: %d %s", code, b)
		}
		if code, _ := req(t, app, http.MethodGet, "/v1/knowledge/files/"+f.ID, "A", nil); code != http.StatusNotFound {
			t.Fatal("a forgotten file still answers")
		}
		if out := searchFilesIn(t, app, "A", map[string]any{"query": "zebra crossing"}); len(out.Passages) != 0 {
			t.Fatalf("a forgotten file is still found: %+v", out)
		}
		if _, ok := mem.get("A", "acme", "gone.md"); !ok {
			t.Fatal("forgetting a file deleted its object")
		}
	})
}

func TestRegisterRefusesWhatIsNotAnObjectOfTheOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		for _, body := range []map[string]any{
			{"bucket": "Acme", "key": "a.txt"},
			{"bucket": "acme", "key": "../a.txt"},
			{"bucket": "acme", "key": "/a.txt"},
			{"bucket": "acme"},
		} {
			if code, b := req(t, app, http.MethodPost, "/v1/knowledge/files", "A", body); code != http.StatusBadRequest {
				t.Fatalf("register %v = %d %s, want 400", body, code, b)
			}
		}
		if code, b := req(t, app, http.MethodPost, "/v1/knowledge/files", "A", map[string]any{"bucket": "acme", "key": "absent.txt"}); code != http.StatusNotFound {
			t.Fatalf("register of an absent object = %d %s, want 404", code, b)
		}
	})
}

func TestCutAtTilesTheText(t *testing.T) {
	text := strings.Repeat("alpha beta gamma delta. ", 3000) + "omega"
	pieces := cutAt(text, synthPart)
	if strings.Join(pieces, "") != text {
		t.Fatal("cutAt lost or reordered text")
	}
	for i, p := range pieces {
		if len(p) > synthPart+synthPart/2 {
			t.Fatalf("piece %d is %d bytes", i, len(p))
		}
	}
}

func TestReadAsTrustsBytesOverNames(t *testing.T) {
	for _, c := range []struct {
		name, typ string
		data      []byte
		want      fileKind
	}{
		{"x.txt", "text/plain", onePagePDF("a"), kindPDF},
		{"x.pdf", "application/pdf", []byte("plain words"), kindText},
		{"x.bin", "", []byte("<!DOCTYPE html><html><body>hi</body></html>"), kindHTML},
		{"x.zip", "application/zip", []byte("PK\x03\x04rest"), kindZip},
		{"x.pptx", "", []byte("PK\x03\x04rest"), kindNone},
		{"x.bin", "", []byte{0, 1, 2}, kindNone},
		{"x.ts", "", []byte("export function f() {}"), kindCode},
	} {
		if got := readAs(c.name, c.typ, c.data); got != c.want {
			t.Errorf("readAs(%s, %s) = %v, want %v", c.name, c.typ, got, c.want)
		}
	}
}

// onePagePDF is a one-page PDF drawing line in Helvetica with glyph widths, so
// word breaks are found the way they are in a real document.
func onePagePDF(line string) []byte {
	return filteredPDF(line, "", func(b []byte) []byte { return b })
}

// filteredPDF is onePagePDF with its page content stored under a filter: the
// stream entries that name it, and the encoder that writes it.
func filteredPDF(line, entries string, encode func([]byte) []byte) []byte {
	stream := encode(fmt.Appendf(nil, "BT /F1 12 Tf 72 720 Td (%s) Tj ET", line))
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d %s >>\nstream\n%s\nendstream", len(stream), entries, stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 32 /LastChar 126 /Widths [" + strings.Repeat("500 ", 95) + "] >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

// docx is a DOCX whose document part holds one paragraph per (style, text).
func docx(paras ...[2]string) []byte {
	var body strings.Builder
	for _, p := range paras {
		style := ""
		if p[0] != "" {
			style = fmt.Sprintf(`<w:pPr><w:pStyle w:val="%s"/></w:pPr>`, p[0])
		}
		fmt.Fprintf(&body, "<w:p>%s<w:r><w:t>%s</w:t></w:r></w:p>", style, p[1])
	}
	return zipOf(map[string]string{"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() + `</w:body></w:document>`})
}

// xlsx is a one-sheet workbook whose cells are shared strings.
func xlsx(sheet string, rows [][]string) []byte {
	var strs []string
	var data strings.Builder
	for r, row := range rows {
		fmt.Fprintf(&data, `<row r="%d">`, r+1)
		for c, v := range row {
			fmt.Fprintf(&data, `<c r="%c%d" t="s"><v>%d</v></c>`, 'A'+c, r+1, len(strs))
			strs = append(strs, v)
		}
		data.WriteString(`</row>`)
	}
	var shared strings.Builder
	for _, s := range strs {
		fmt.Fprintf(&shared, `<si><t>%s</t></si>`, s)
	}
	return zipOf(map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="` + sheet + `" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` + shared.String() + `</sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` + data.String() + `</sheetData></worksheet>`,
	})
}

func zipOf(files map[string]string) []byte {
	var buf bytes.Buffer
	zw := azip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.Bytes()
}

// slowEmbed holds every embedding of a passage under "Plan" until gate closes;
// a query's embedding passes, so search runs while the passages wait.
type slowEmbed struct {
	fakeAI
	gate chan struct{}
}

func (s *slowEmbed) Embed(ctx context.Context, r *infer.EmbedRequest) ([][]float32, error) {
	for _, in := range r.Inputs {
		if strings.Contains(in, "Plan") {
			select {
			case <-s.gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			break
		}
	}
	return s.fakeAI.Embed(ctx, r)
}

func TestRetrieveReadsEveryChosenSection(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// Words alone rank the passages, so the order is the text's and not a fake
		// embedding's.
		app, mem := mountFiles(t, wordsOnly{readerAI{fakeAI: fakeAI{dims: 1024}, pickWord: "Rotation|Escalation"}})
		var b strings.Builder
		b.WriteString("# Key policy\n\nOpening words.\n\n## Rotation\n\n")
		for i := range 300 {
			fmt.Fprintf(&b, "Keys rotate on schedule %d; a rotation that fails is retried and the rotation keys are re-wrapped when they rotate.\n\n", i)
		}
		b.WriteString("## Escalation\n\nWhen it fails twice, the on-call Marisol Okafor is paged.\n")
		f := ingestOne(t, app, mem, "keys.md", "text/markdown", []byte(b.String()))
		code, body := req(t, app, http.MethodPost, "/v1/knowledge/files/retrieve", "A",
			map[string]any{"query": "how often do keys rotate and who is paged when a rotation fails", "files": []string{f.ID}, "limit": 4})
		if code != http.StatusOK {
			t.Fatalf("retrieve: %d %s", code, body)
		}
		out := decode[retrieveOut](t, body)
		var chose bool
		for _, sec := range out.Sections {
			chose = chose || sec.Section.Title == "Escalation"
		}
		if !chose {
			t.Fatalf("picked %+v, want Escalation among them", out.Sections)
		}
		// Rotation's passages match the question's words far more often than
		// Escalation's one; the chosen section is read all the same.
		var heard bool
		for _, p := range out.Passages {
			heard = heard || strings.Contains(p.Cite, "Escalation") && strings.Contains(p.Text, "Marisol Okafor")
		}
		if !heard || len(out.Passages) > 4 {
			t.Fatalf("passages %+v, want Escalation's among at most 4", out.Passages)
		}
	})
}

// wordsOnly reads and picks like readerAI and embeds nothing.
type wordsOnly struct{ readerAI }

func (wordsOnly) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) {
	return nil, errAIDown
}

func TestRetrieveFollowsACitationIntoTheCitedFilesBestPassage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}, pickWord: "Summary"})
		policy := "# Key rotation policy\n\n## Scope\n\nEvery signing key.\n\n## Rotation\n\nSigning keys rotate every 47 days.\n\n## Escalation\n\nIf a rotation fails twice, page Ingrid Halvorsen."
		report := "# Incident report\n\n## Summary\n\nA signing key failed to rotate twice; it had been in service for 52 days. The intervals are set in rotation.md.\n\n## Timeline\n\nDetected at 03:10."
		p := ingestOne(t, app, mem, "rotation.md", "text/markdown", []byte(policy))
		r := ingestOne(t, app, mem, "incident.md", "text/markdown", []byte(report))
		code, b := req(t, app, http.MethodPost, "/v1/knowledge/files/retrieve", "A",
			map[string]any{"query": "was the failing key past its rotation interval, and who should have been paged", "files": []string{r.ID}})
		if code != http.StatusOK {
			t.Fatalf("retrieve: %d %s", code, b)
		}
		out := decode[retrieveOut](t, b)
		var graphed *citedPassage
		for i := range out.Passages {
			if out.Passages[i].Via == "graph" && out.Passages[i].File.ID == p.ID {
				graphed = &out.Passages[i]
			}
		}
		// The report names rotation.md as a whole: the graph reads the policy's
		// passage that answers, not its empty root.
		if graphed == nil || graphed.Section.ID == 0 || graphed.Why == "" {
			t.Fatalf("retrieval did not follow the report's citation into rotation.md: %+v", out.Passages)
		}
	})
}
