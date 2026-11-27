package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// bigLog is a log of a few hundred passages whose first line names an early
// word and whose last names a late one, so a test can ask which of a file's
// full-text rows remain.
func bigLog() []byte {
	var b strings.Builder
	b.WriteString("Opening line: the quokka relay came up first.\n")
	for i := 0; b.Len() < 700_000; i++ {
		fmt.Fprintf(&b, "2026-09-%02dT%02d:%02d:09Z api.hanzo.ai GET /v1/models 200 %dms tenant=t%d\n", i%28+1, i%24, i%60, i%900, i%400)
	}
	b.WriteString("Closing line: the narwhal relay went down last.\n")
	return []byte(b.String())
}

// indexed reports whether the org's full-text index still holds a row of file
// for word.
func indexed(t *testing.T, org, file, word string) bool {
	t.Helper()
	rows, err := lexical.Query(cloud.For(context.Background(), org), org, filesIndex, word, []string{""}, 50, 0)
	if err != nil {
		t.Fatalf("query the index for %q: %v", word, err)
	}
	for _, raw := range rows {
		var row struct {
			File string `json:"file"`
		}
		if json.Unmarshal(raw, &row) == nil && row.File == file {
			return true
		}
	}
	return false
}

// rowsOf counts what the org's store holds of a file: its record (in any
// status), passages and sections.
func rowsOf(t *testing.T, st *store, id string) (files, passages, sections int) {
	t.Helper()
	for _, q := range []struct {
		sql string
		n   *int
	}{
		{`SELECT COUNT(*) FROM files WHERE id=$1`, &files},
		{`SELECT COUNT(*) FROM file_passages WHERE file=$1`, &passages},
		{`SELECT COUNT(*) FROM sections WHERE file=$1`, &sections},
	} {
		if err := st.db.QueryRow(q.sql, id).Scan(q.n); err != nil {
			t.Fatal(err)
		}
	}
	return
}

// TestForgettingAFileAnswersAtOnceAndFinishesBehind is a file of hundreds of
// passages forgotten: the call answers before its full-text rows are removed,
// no read finds the file from then on, and what the index held of it is gone
// once the removal behind it finishes.
func TestForgettingAFileAnswersAtOnceAndFinishesBehind(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		f := ingestOne(t, app, mem, "edge.log", "text/plain", bigLog())
		if f.Passages < 300 || !indexed(t, "A", f.ID, "narwhal") {
			t.Fatalf("the log was not indexed whole: %+v", f)
		}
		if code, b := req(t, app, http.MethodDelete, "/v1/knowledge/files/"+f.ID, "A", nil); code != http.StatusOK {
			t.Fatalf("forget: %d %s", code, b)
		}
		if code, _ := req(t, app, http.MethodGet, "/v1/knowledge/files/"+f.ID, "A", nil); code != http.StatusNotFound {
			t.Fatal("a forgotten file still answers")
		}
		if code, _ := req(t, app, http.MethodDelete, "/v1/knowledge/files/"+f.ID, "A", nil); code != http.StatusNotFound {
			t.Fatal("a forgotten file can be forgotten again")
		}
		_, b := req(t, app, http.MethodGet, "/v1/knowledge/files?bucket=acme", "A", nil)
		if strings.Contains(string(b), f.ID) {
			t.Fatalf("a forgotten file is still listed: %s", b)
		}
		if out := searchFilesIn(t, app, "A", map[string]any{"query": "narwhal relay"}); len(out.Passages) != 0 {
			t.Fatalf("a forgotten file is still found: %+v", out.Passages[0].Cite)
		}
		st, _ := idx.storeFor("A")
		deadline := time.Now().Add(20 * time.Second)
		for {
			files, passages, sections := rowsOf(t, st, f.ID)
			if files == 0 {
				if passages != 0 || sections != 0 {
					t.Fatalf("the record went before its rows: %d passages, %d sections", passages, sections)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the forgotten file's rows were never removed")
			}
			time.Sleep(20 * time.Millisecond)
		}
		for _, w := range []string{"quokka", "narwhal", "tenant"} {
			if indexed(t, "A", f.ID, w) {
				t.Fatalf("the index still holds the forgotten file's %q", w)
			}
		}
		if _, ok := mem.get("A", "acme", "edge.log"); !ok {
			t.Fatal("forgetting a file deleted its object")
		}
	})
}

// releaseOf forgets a file to every reader as its own record names it, which is
// what forgetFile does once its caller is admitted.
func releaseOf(ctx context.Context, st *store, id string) error {
	f, err := st.file(ctx, id)
	if err != nil {
		return err
	}
	return st.release(ctx, id, f.uploader)
}

// TestAForgetResumesFromItsCursor is a removal that stopped part-way: started
// again, it removes the full-text rows past its cursor and does not walk the
// ones before it again.
func TestAForgetResumesFromItsCursor(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		f := ingestOne(t, app, mem, "edge.log", "text/plain", bigLog())
		st, _ := idx.storeFor("A")
		ctx := cloud.For(context.Background(), "A")
		if err := releaseOf(ctx, st, f.ID); err != nil {
			t.Fatal(err)
		}
		// A previous attempt is recorded as having removed the first 100 passages'
		// rows; it did not, so the quokka's row stays exactly when the cursor holds.
		if _, err := st.db.Exec(`UPDATE files SET done=100 WHERE id=$1`, f.ID); err != nil {
			t.Fatal(err)
		}
		if err := idx.unindex(ctx, "A", f.ID); err != nil {
			t.Fatal(err)
		}
		if !indexed(t, "A", f.ID, "quokka") {
			t.Fatal("the removal walked the rows before its cursor again")
		}
		if indexed(t, "A", f.ID, "narwhal") {
			t.Fatal("the removal left rows past its cursor")
		}
		if files, passages, _ := rowsOf(t, st, f.ID); files != 0 || passages != 0 {
			t.Fatalf("after the removal: %d records, %d passages", files, passages)
		}
	})
}

// TestAStageWritesNothingForAForgottenFile is an ingest still running when its
// file is forgotten: its next write is refused, to the shared index as much as
// to the store, so nothing is left behind under a file that is gone.
func TestAStageWritesNothingForAForgottenFile(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		view := ingestOne(t, app, mem, "edge.log", "text/plain", bigLog())
		st, _ := idx.storeFor("A")
		ctx := cloud.For(context.Background(), "A")
		f, err := st.file(ctx, view.ID)
		if err != nil {
			t.Fatal(err)
		}
		// The passages were not written yet when the file was forgotten.
		if err := st.forget(ctx, "A", f.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`UPDATE files SET sections=0 WHERE id=$1`, f.ID); err != nil {
			t.Fatal(err)
		}
		mem.put("A", "acme", "edge.log", "text/plain", bigLog())
		if err := idx.extractStage(ctx, st, "A", f); err != nil {
			t.Fatal(err)
		}
		if err := st.release(ctx, f.ID, f.uploader); err != nil {
			t.Fatal(err)
		}
		if err := idx.passageStage(ctx, st, "A", f); !errors.Is(err, errSuperseded) {
			t.Fatalf("a forgotten file's passages stage = %v, want it superseded", err)
		}
		if _, passages, _ := rowsOf(t, st, f.ID); passages != 0 {
			t.Fatalf("the stage wrote %d passages for a forgotten file", passages)
		}
		if indexed(t, "A", f.ID, "narwhal") {
			t.Fatal("the stage wrote to the shared index for a forgotten file")
		}
		if err := st.progress(ctx, f.ID, f.etag, 1, 2); !errors.Is(err, errSuperseded) {
			t.Fatalf("progress for a forgotten file = %v", err)
		}
		if more, err := idx.stage(ctx, IngestIn{Org: "A", ID: f.ID, Etag: f.etag}, stageToc); more || err != nil {
			t.Fatalf("a stage of a forgotten file = %v %v, want nothing owed", more, err)
		}
	})
}

// TestAFileRegisteredAgainWhileForgottenIsLeftToItsIngest is an object
// registered again before its old index was removed: the removal leaves the
// new record alone, and the new ingest reads the file whole.
func TestAFileRegisteredAgainWhileForgottenIsLeftToItsIngest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		f := ingestOne(t, app, mem, "edge.log", "text/plain", bigLog())
		st, _ := idx.storeFor("A")
		ctx := cloud.For(context.Background(), "A")
		if err := releaseOf(ctx, st, f.ID); err != nil {
			t.Fatal(err)
		}
		again := register(t, app, "A", "acme", "edge.log")
		if again.ID != f.ID || again.Status != fileQueued {
			t.Fatalf("registered again = %+v", again)
		}
		got := settle(t, app, "A", f.ID)
		// The new record's cursor is its ingest's: one at the start of a stage reads
		// as a removal with everything still to do, which the removal must not take.
		if _, err := st.db.Exec(`UPDATE files SET done=0, total=passages WHERE id=$1`, f.ID); err != nil {
			t.Fatal(err)
		}
		if err := idx.unindex(ctx, "A", f.ID); err != nil {
			t.Fatal(err)
		}
		if code, b := req(t, app, http.MethodGet, "/v1/knowledge/files/"+f.ID, "A", nil); code != http.StatusOK || got.Status != fileReady {
			t.Fatalf("the file registered again = %d %s", code, b)
		}
		if !indexed(t, "A", f.ID, "narwhal") || !indexed(t, "A", f.ID, "quokka") {
			t.Fatal("the removal took the full-text rows of the file registered again")
		}
	})
}
