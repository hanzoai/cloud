// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package event

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/object"
	"github.com/zap-proto/zip"
)

// ── a map, built the way a bundler writes one ───────────────────────────────

func encVLQ(b *strings.Builder, n int) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	v := n << 1
	if n < 0 {
		v = (-n << 1) | 1
	}
	for {
		d := v & 31
		v >>= 5
		if v > 0 {
			d |= 32
		}
		b.WriteByte(chars[d])
		if v == 0 {
			return
		}
	}
}

// seg is one mapping, 0-based as the format stores it. name < 0 states none.
type seg struct{ line, col, src, srcLine, srcCol, name int }

// buildMap renders a v3 map. Segments must be in (line, col) order.
func buildMap(sources, names []string, segs []seg) []byte {
	var m strings.Builder
	line, pCol, pSrc, pLine, pSCol, pName := 0, 0, 0, 0, 0, 0
	first := true
	for _, s := range segs {
		for line < s.line {
			m.WriteByte(';')
			line, pCol, first = line+1, 0, true
		}
		if !first {
			m.WriteByte(',')
		}
		first = false
		encVLQ(&m, s.col-pCol)
		encVLQ(&m, s.src-pSrc)
		encVLQ(&m, s.srcLine-pLine)
		encVLQ(&m, s.srcCol-pSCol)
		pCol, pSrc, pLine, pSCol = s.col, s.src, s.srcLine, s.srcCol
		if s.name >= 0 {
			encVLQ(&m, s.name-pName)
			pName = s.name
		}
	}
	b, _ := json.Marshal(map[string]any{
		"version": 3, "file": "index.js", "sources": sources, "names": names,
		"mappings": m.String(), "sourcesContent": []string{"export function render() { throw new Error('x') }"},
	})
	return b
}

// Two builds of ONE source: the same throw at src/app.tsx:42:5 inside render(),
// minified to different columns, mangled names and content-hashed chunk names.
var (
	mapA = buildMap([]string{"../src/app.tsx", "../node_modules/react-dom/client.js"}, []string{"render", "flush"}, []seg{
		{0, 100, 1, 9, 0, 1},   // vendor code
		{0, 1233, 0, 41, 4, 0}, // the throw
		{0, 1300, 0, 50, 0, -1},
	})
	mapB = buildMap([]string{"webpack://app/./src/app.tsx", "webpack://app/./node_modules/react-dom/client.js"}, []string{"flush", "render"}, []seg{
		{0, 40, 1, 9, 0, 0},
		{0, 986, 0, 41, 4, 1},
	})
)

const (
	fileA = "/assets/index-3f9aAAAA.js"
	fileB = "/assets/index-77c1BBBB.js"
)

// localMaps binds a fresh on-disk store and an empty cache for one test.
func localMaps(t *testing.T) object.Blobs {
	t.Helper()
	b := object.NewBlobs(&object.Local{Dir: t.TempDir()}, "team-blobs")
	orig, _ := currentSourcemaps()
	bindSourcemaps(b)
	t.Cleanup(func() { bindSourcemaps(orig) })
	return b
}

// putMap files a map the way the upload does, without the HTTP door.
func putMap(t *testing.T, b object.Blobs, org, release, file string, data []byte) {
	t.Helper()
	ctx := context.Background()
	if err := b.Put(ctx, mapKey(org, release, file), data); err != nil {
		t.Fatal(err)
	}
	if err := b.Put(ctx, releaseKey(org, release), []byte(release)); err != nil {
		t.Fatal(err)
	}
}

func browserError(release, stack string) CaptureEvent {
	return CaptureEvent{Type: "error", Release: release, Error: &Exception{Type: "TypeError", Message: "Cannot read properties of undefined (reading 'id')", Stack: stack}}
}

// stackAt is a V8 stack: the throw at col, called from vendored code at vcol.
func stackAt(file string, col, vcol int, fn string) string {
	return fmt.Sprintf("TypeError: x\n    at %s (https://hanzo.app%s:1:%d)\n    at https://hanzo.app%s:1:%d", fn, file, col, file, vcol)
}

// ingestOne runs one event through the write core and returns the fact it landed.
func ingestOne(t *testing.T, org string, e CaptureEvent) fact {
	t.Helper()
	w := fakeWarehouse(t)
	if _, err := ingestEvents(context.Background(), org, sourceEvent, []CaptureEvent{e}); err != nil {
		t.Fatal(err)
	}
	if len(w.facts) != 1 {
		t.Fatalf("landed %d facts, want 1", len(w.facts))
	}
	return w.facts[0]
}

// ── the decoder ─────────────────────────────────────────────────────────────

func TestSourceMapLookupMapsAGeneratedPosition(t *testing.T) {
	sm, err := parseSourceMap(mapA)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := sm.lookup(1, 1234)
	if !ok || got != (mapped{source: "src/app.tsx", line: 42, column: 5, name: "render"}) {
		t.Fatalf("lookup(1,1234) = %+v %v", got, ok)
	}
	// A column inside a segment's span maps to that segment.
	if got, ok := sm.lookup(1, 1250); !ok || got.line != 42 {
		t.Fatalf("lookup(1,1250) = %+v %v", got, ok)
	}
	// Before the first segment, and past the last line, nothing.
	if _, ok := sm.lookup(1, 50); ok {
		t.Fatal("a column before every segment mapped")
	}
	if _, ok := sm.lookup(2, 1); ok {
		t.Fatal("a line the map does not have mapped")
	}
	smB, err := parseSourceMap(mapB)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := smB.lookup(1, 987); !ok || got.source != "src/app.tsx" || got.name != "render" {
		t.Fatalf("webpack source = %+v %v, want src/app.tsx", got, ok)
	}
}

func TestSourceMapRefusesWhatItCannotRead(t *testing.T) {
	for name, body := range map[string]string{
		"not json":       "{",
		"version 2":      `{"version":2,"sources":[],"names":[],"mappings":""}`,
		"index map":      `{"version":3,"sections":[{"offset":{"line":0,"column":0},"map":{}}]}`,
		"bad character":  `{"version":3,"sources":["a.ts"],"names":[],"mappings":"AA!A"}`,
		"truncated":      `{"version":3,"sources":["a.ts"],"names":[],"mappings":"AAA"}`,
		"source outside": `{"version":3,"sources":["a.ts"],"names":[],"mappings":"ACAA"}`,
		"name outside":   `{"version":3,"sources":["a.ts"],"names":[],"mappings":"AAAAC"}`,
		"overflow":       `{"version":3,"sources":["a.ts"],"names":[],"mappings":"gggggggggggA"}`,
	} {
		if _, err := parseSourceMap([]byte(body)); err == nil {
			t.Errorf("%s parsed", name)
		}
	}
}

// ── resolve on ingest ───────────────────────────────────────────────────────

// A frame from a release with a map is stored at its original place, and the
// browser's own frame is kept beside it for forensics.
func TestIngestResolvesAFrameThroughItsReleasesMap(t *testing.T) {
	b := localMaps(t)
	putMap(t, b, "acme", "sha-a", fileA, mapA)
	f := ingestOne(t, "acme", browserError("sha-a", stackAt(fileA, 1234, 101, "e")))

	top := f.frames[0]
	if top.file != "src/app.tsx" || top.line != 42 || top.column != 5 || top.function != "render" || !top.own {
		t.Fatalf("top frame = %+v, want render at src/app.tsx:42:5, first-party", top)
	}
	// The second frame lands in vendored code and is no longer first-party.
	if v := f.frames[1]; v.file != "node_modules/react-dom/client.js" || v.own {
		t.Fatalf("vendor frame = %+v, want node_modules and not own", v)
	}
	var kept []minifiedFrame
	if err := json.Unmarshal([]byte(f.attributes[minifiedKey]), &kept); err != nil || len(kept) != 2 {
		t.Fatalf("%s = %q (%v)", minifiedKey, f.attributes[minifiedKey], err)
	}
	if kept[0] != (minifiedFrame{Function: "e", File: "https://hanzo.app" + fileA, Line: 1, Column: 1234}) {
		t.Fatalf("minified frame = %+v", kept[0])
	}
	if f.issue != fingerprint(&f) {
		t.Fatal("the stored issue is not the fingerprint of the stored (mapped) frames")
	}
	// What leaves on the bus is the mapped frame.
	if m := wire(f); m.Frames[0].File != "src/app.tsx" || m.Frames[0].Line != 42 {
		t.Fatalf("published frame = %+v", m.Frames[0])
	}
}

// THE POINT OF RESOLVING ON INGEST: one fault, two deploys whose bundles differ in
// chunk name, column and mangled name, is ONE group once both resolve — and two
// groups when they do not, which is what proves the comparison is not vacuous.
func TestTheSameFaultFromTwoBuildsGroupsOnceBothResolve(t *testing.T) {
	b := localMaps(t)
	evA := browserError("sha-a", stackAt(fileA, 1234, 101, "e"))
	evB := browserError("sha-b", stackAt(fileB, 987, 41, "t"))

	before := [2]string{ingestOne(t, "acme", evA).issue, ingestOne(t, "acme", evB).issue}
	if before[0] == before[1] {
		t.Fatal("unmapped, the two builds already group together; the test proves nothing")
	}

	putMap(t, b, "acme", "sha-a", fileA, mapA)
	putMap(t, b, "acme", "sha-b", fileB, mapB)
	bindSourcemaps(b) // a fresh cache: the absences above are not remembered
	a, bb := ingestOne(t, "acme", evA), ingestOne(t, "acme", evB)
	if a.issue != bb.issue {
		t.Fatalf("mapped, the builds group apart: %s vs %s (frames %+v / %+v)", a.issue, bb.issue, a.frames[0], bb.frames[0])
	}
	if a.issue == before[0] || a.issue == before[1] {
		t.Fatal("the mapped group is one of the minified ones")
	}
}

// A MISSING MAP IS NOT AN ERROR: no upload for the release, a release with maps but
// not for this file, and an event with no release all store the frame as sent.
func TestAMissingMapLeavesTheFrameAlone(t *testing.T) {
	b := localMaps(t)
	putMap(t, b, "acme", "sha-a", fileA, mapA)
	for name, e := range map[string]CaptureEvent{
		"release never uploaded": browserError("sha-none", stackAt(fileA, 1234, 101, "e")),
		"file not in release":    browserError("sha-a", stackAt("/assets/other-1111.js", 1234, 101, "e")),
		"no release":             browserError("", stackAt(fileA, 1234, 101, "e")),
		"position unmapped":      browserError("sha-a", stackAt(fileA, 10, 5, "e")),
	} {
		want, _ := normalize("acme", time.Now(), e)
		got := ingestOne(t, "acme", e)
		if got.frames[0] != want.frames[0] || got.issue != want.issue {
			t.Errorf("%s: frame %+v issue %s, want %+v %s", name, got.frames[0], got.issue, want.frames[0], want.issue)
		}
		if _, ok := got.attributes[minifiedKey]; ok {
			t.Errorf("%s: kept a minified copy of frames nothing mapped", name)
		}
	}
}

// One org's maps never resolve another org's frames, whatever the release says.
func TestAnotherOrgsMapResolvesNothing(t *testing.T) {
	b := localMaps(t)
	putMap(t, b, "globex", "sha-a", fileA, mapA)
	f := ingestOne(t, "acme", browserError("sha-a", stackAt(fileA, 1234, 101, "e")))
	if strings.HasPrefix(f.frames[0].file, "src/") {
		t.Fatalf("acme's frame resolved through globex's map: %+v", f.frames[0])
	}
}

// A fingerprint a peer stated is kept even when its frames map.
func TestAStatedFingerprintSurvivesResolution(t *testing.T) {
	b := localMaps(t)
	putMap(t, b, "acme", "sha-a", fileA, mapA)
	e := browserError("sha-a", stackAt(fileA, 1234, 101, "e"))
	e.stated = statedFingerprint("0123456789abcdef")
	if f := ingestOne(t, "acme", e); f.issue != "0123456789abcdef" || f.frames[0].file != "src/app.tsx" {
		t.Fatalf("issue %s frame %+v", f.issue, f.frames[0])
	}
}

// ── bounds ──────────────────────────────────────────────────────────────────

// counting wraps a store and counts reads, optionally holding each one.
type counting struct {
	object.Blobs
	gets atomic.Int64
	hold chan struct{}
}

func (c *counting) Get(ctx context.Context, key string) ([]byte, error) {
	c.gets.Add(1)
	if c.hold != nil {
		select {
		case <-c.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.Blobs.Get(ctx, key)
}

// A map over the size bound is never parsed, however it got into the store.
func TestAnOversizedMapIsNotParsed(t *testing.T) {
	b := localMaps(t)
	big := append(append([]byte{}, mapA[:len(mapA)-1]...), []byte(`,"x":"`+strings.Repeat("a", maxMapBytes)+`"}`)...)
	putMap(t, b, "acme", "sha-a", fileA, big)
	if f := ingestOne(t, "acme", browserError("sha-a", stackAt(fileA, 1234, 101, "e"))); f.frames[0].file == "src/app.tsx" {
		t.Fatal("a map over maxMapBytes was parsed")
	}
}

// One event asks for at most maxMapsPerEvent maps, and a release nobody uploaded
// costs one read for every event that names it, not one per event.
func TestResolutionReadsAreBounded(t *testing.T) {
	base := localMaps(t)
	c := &counting{Blobs: base}
	putMap(t, base, "acme", "sha-a", fileA, mapA)
	bindSourcemaps(c)

	var stack strings.Builder
	stack.WriteString("Error: x\n")
	for i := range 20 {
		fmt.Fprintf(&stack, "    at f (https://hanzo.app/assets/chunk-%02d.js:1:10)\n", i)
	}
	ingestOne(t, "acme", browserError("sha-a", stack.String()))
	if n := c.gets.Load(); n != 1+maxMapsPerEvent {
		t.Fatalf("one event read %d objects, want the marker plus %d maps", n, maxMapsPerEvent)
	}

	c.gets.Store(0)
	for range 5 {
		ingestOne(t, "acme", browserError("sha-unknown", stackAt(fileA, 1234, 101, "e")))
	}
	if n := c.gets.Load(); n != 1 {
		t.Fatalf("five events of an unknown release read %d objects, want 1", n)
	}
}

// A store that does not answer holds an ingest for the budget and no longer, and
// the fault is stored with the frames it came with.
func TestASlowStoreCannotHoldIngest(t *testing.T) {
	base := localMaps(t)
	putMap(t, base, "acme", "sha-a", fileA, mapA)
	c := &counting{Blobs: base, hold: make(chan struct{})}
	bindSourcemaps(c)
	t.Cleanup(func() { close(c.hold) })

	start := time.Now()
	f := ingestOne(t, "acme", browserError("sha-a", stackAt(fileA, 1234, 101, "e")))
	if took := time.Since(start); took > resolveBudget+time.Second {
		t.Fatalf("ingest took %v against a stuck store; the budget is %v", took, resolveBudget)
	}
	if f.frames[0].file == "src/app.tsx" {
		t.Fatal("resolved through a store that never answered")
	}
}

// Concurrent events of one release read its map once.
func TestConcurrentEventsShareOneLoad(t *testing.T) {
	base := localMaps(t)
	putMap(t, base, "acme", "sha-a", fileA, mapA)
	c := &counting{Blobs: base}
	bindSourcemaps(c)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, _ := normalize("acme", time.Now(), browserError("sha-a", stackAt(fileA, 1234, 101, "e")))
			resolveFaults(context.Background(), []fact{f}, []bool{false})
		}()
	}
	wg.Wait()
	if n := c.gets.Load(); n != 2 {
		t.Fatalf("16 concurrent events read %d objects, want 2 (the marker and the map)", n)
	}
}

// ── the upload door ─────────────────────────────────────────────────────────

var ciApp = map[string]string{"X-User-Id": "app:acme/acme-ci", "X-Org-Id": "acme", "X-User-IsApp": "true"}

func upload(t *testing.T, app *zip.App, hdr map[string]string, query string, body []byte) (int, []byte) {
	t.Helper()
	return send(t, app, http.MethodPut, sourcemapPath+"?"+query, hdr, string(body))
}

// Only the org's build identity uploads, and only for its own org.
func TestOnlyTheOrgsBuildIdentityUploadsSourcemaps(t *testing.T) {
	app := mountApp(t)
	b := localMaps(t)
	q := "release=sha-a&file=" + fileA
	for name, hdr := range map[string]map[string]string{
		"anonymous":          nil,
		"a member":           {"X-User-Id": "u-ada", "X-Org-Id": "acme"},
		"an org admin":       {"X-User-Id": "u-ada", "X-Org-Id": "acme", "X-User-IsOrgAdmin": "true"},
		"an operator as app": {"X-User-Id": "app:acme/acme-ci", "X-Org-Id": "acme", "X-User-IsApp": "true", zip.HeaderActedBy: "admin/z"},
		"an org api key":     {"X-User-Id": "u-ada", "X-Org-Id": "acme", "Authorization": "Bearer sk-acmeacmeacmeacmeacme0000"},
	} {
		want := http.StatusForbidden
		if hdr == nil {
			want = http.StatusUnauthorized
		}
		if code, body := upload(t, app, hdr, q, mapA); code != want {
			t.Errorf("%s = %d (%s), want %d", name, code, body, want)
		}
	}
	if code, body := upload(t, app, ciApp, q+"&org=globex", mapA); code != http.StatusForbidden {
		t.Errorf("naming another org = %d (%s), want 403", code, body)
	}
	if _, err := b.Get(context.Background(), releaseKey("acme", "sha-a")); err == nil {
		t.Fatal("a refused upload stored something")
	}
	if _, err := b.Get(context.Background(), releaseKey("globex", "sha-a")); err == nil {
		t.Fatal("an upload naming globex stored under globex")
	}

	code, body := upload(t, app, ciApp, "release=sha-a&file=https://hanzo.app"+fileA+"?v=2", mapA)
	if code != http.StatusCreated {
		t.Fatalf("the build identity = %d (%s), want 201", code, body)
	}
	var r sourcemapReceipt
	if err := json.Unmarshal(body, &r); err != nil || r.Release != "sha-a" || r.File != fileA || r.Bytes != len(mapA) {
		t.Fatalf("receipt = %s (%v)", body, err)
	}
	// And ingest reads what it wrote.
	if f := ingestOne(t, "acme", browserError("sha-a", stackAt(fileA, 1234, 101, "e"))); f.frames[0].file != "src/app.tsx" {
		t.Fatalf("an uploaded map did not resolve: %+v", f.frames[0])
	}
}

func TestSourcemapUploadBounds(t *testing.T) {
	app := mountApp(t)
	localMaps(t)
	for name, tc := range map[string]struct {
		query string
		body  []byte
		want  int
	}{
		"no release":    {"file=" + fileA, mapA, http.StatusBadRequest},
		"spaced":        {"release=a%20b&file=" + fileA, mapA, http.StatusBadRequest},
		"long release":  {"release=" + strings.Repeat("a", maxRelease+1) + "&file=" + fileA, mapA, http.StatusBadRequest},
		"no file":       {"release=sha-a", mapA, http.StatusBadRequest},
		"not a script":  {"release=sha-a&file=/assets/index.css", mapA, http.StatusBadRequest},
		"empty":         {"release=sha-a&file=" + fileA, nil, http.StatusBadRequest},
		"not a map":     {"release=sha-a&file=" + fileA, []byte(`{"hello":1}`), http.StatusUnprocessableEntity},
		"an index map":  {"release=sha-a&file=" + fileA, []byte(`{"version":3,"sections":[{}]}`), http.StatusUnprocessableEntity},
		"over the size": {"release=sha-a&file=" + fileA, []byte(strings.Repeat(" ", maxMapBytes+1)), http.StatusRequestEntityTooLarge},
	} {
		if code, body := upload(t, app, ciApp, tc.query, tc.body); code != tc.want {
			t.Errorf("%s = %d (%.200s), want %d", name, code, body, tc.want)
		}
	}
}

// With no object store, an upload is an honest 503 and nothing pretends to keep it.
func TestSourcemapUploadWithNoStoreIs503(t *testing.T) {
	app := mountApp(t)
	orig, _ := currentSourcemaps()
	bindSourcemaps(object.NoBlobs())
	t.Cleanup(func() { bindSourcemaps(orig) })
	if code, body := upload(t, app, ciApp, "release=sha-a&file="+fileA, mapA); code != http.StatusServiceUnavailable {
		t.Fatalf("no store = %d (%s), want 503", code, body)
	}
}

func TestServedPathReadsFramesAndUploadsAlike(t *testing.T) {
	for in, want := range map[string]string{
		"https://hanzo.app/assets/a-1.js":           "/assets/a-1.js",
		"https://hanzo.app:443/assets/a-1.js?v=2#x": "/assets/a-1.js",
		"/assets/a-1.mjs":                           "/assets/a-1.mjs",
		"assets/a-1.js":                             "",
		"https://hanzo.app":                         "",
		"/assets/a.css":                             "",
		"<anonymous>":                               "",
	} {
		if got := servedPath(in); got != want {
			t.Errorf("servedPath(%q) = %q, want %q", in, got, want)
		}
	}
}
