package knowledge

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// TestAFileAnEarlierBuildFinishedReadsFinished pins the record the earlier build
// left on every file it finished: ready, AT its last stage, graph. Read by this
// build, that is a file whose vectors are still being written, which nothing
// will ever write — five workspace files sat that way in production. Opening
// the store rewrites it into this build's finished record.
func TestAFileAnEarlierBuildFinishedReadsFinished(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		f := ingestOne(t, app, mem, "chat/rotation.md", "text/markdown", []byte("# Rotation\n\n## Keys\n\nKeys rotate every ninety days."))
		st, err := idx.storeFor("A")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`UPDATE files SET stage=$1 WHERE id=$2`, stageGraph, f.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := openStore(st.db); err != nil { // the upgraded build opens the same file
			t.Fatal(err)
		}
		_, b := req(t, app, http.MethodGet, "/v1/knowledge/files/"+f.ID, "A", nil)
		if got := decode[File](t, b); got.Status != fileReady || got.Stage != "" || got.Embedded != got.Passages {
			t.Fatalf("a file the earlier build finished reads %+v, want ready with no stage", got)
		}
	})
}

// TestAStalledFileIsStartedAgainAtItsStage leaves a file's record naming work
// nothing is driving — the shape a run on a goroutine leaves when its process
// ends, or a workflow whose failure could not be recorded — and shows the sweep
// starting it again at that stage, through to finished. A record touched within
// its stage's budget is a run still alive, and is left alone.
func TestAStalledFileIsStartedAgainAtItsStage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, mem := mountFiles(t, readerAI{fakeAI: fakeAI{dims: 1024}})
		body := []byte("# Plan\n\n## Launch\n\nThe launch window opens on a Tuesday.\n\n## Risks\n\nSee Launch.")
		st, err := idx.storeFor("A")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		shapes := []struct {
			name, status, stage string
			idle                time.Duration
			unembed             bool
		}{
			{"indexing at graph", fileIndexing, stageGraph, 2 * time.Hour, false},
			{"ready at embed", fileReady, stageEmbed, 4 * time.Hour, true},
			{"queued", fileQueued, "", time.Hour, false},
		}
		for _, s := range shapes {
			f := ingestOne(t, app, mem, "stall/"+s.name+".md", "text/markdown", body)
			if s.unembed {
				if _, err := st.db.Exec(`UPDATE file_passages SET vec=NULL, model='' WHERE file=$1`, f.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := st.db.Exec(`UPDATE files SET embedded=0 WHERE id=$1`, f.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.db.Exec(`UPDATE files SET status=$1, stage=$2, updated=$3 WHERE id=$4`,
				s.status, s.stage, now.Add(-s.idle).Unix(), f.ID); err != nil {
				t.Fatal(err)
			}
		}
		live := ingestOne(t, app, mem, "stall/live.md", "text/markdown", body)
		if _, err := st.db.Exec(`UPDATE files SET status=$1, stage=$2, updated=$3 WHERE id=$4`,
			fileIndexing, stageGraph, now.Add(-time.Minute).Unix(), live.ID); err != nil {
			t.Fatal(err)
		}
		// A record from before records named their org, in an org whose store is
		// named by its folded form ("A"): no run may start it under the folded name.
		nameless := ingestOne(t, app, mem, "stall/nameless.md", "text/markdown", body)
		if _, err := st.db.Exec(`UPDATE files SET status=$1, stage=$2, org='', updated=$3 WHERE id=$4`,
			fileIndexing, stageGraph, now.Add(-2*time.Hour).Unix(), nameless.ID); err != nil {
			t.Fatal(err)
		}

		idx.resume(now)

		for _, s := range shapes {
			f, err := st.fileAt(context.Background(), "acme", "stall/"+s.name+".md")
			if err != nil {
				t.Fatal(err)
			}
			got := settle(t, app, "A", f.ID)
			if got.Status != fileReady || got.Stage != "" || got.Passages == 0 || got.Embedded != got.Passages {
				t.Fatalf("%s: a stalled file ends %+v, want ready with every passage embedded", s.name, got)
			}
		}
		time.Sleep(100 * time.Millisecond)
		for id, why := range map[string]string{
			live.ID:     "a run still within its budget was started again",
			nameless.ID: "a record that does not name its org was started under its store's folded name",
		} {
			got, err := st.file(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != fileIndexing || got.Stage != stageGraph {
				t.Fatalf("%s: %+v", why, got)
			}
		}
	})
}

// TestStalledWeighsEachStageAgainstItsOwnBudget pins the bound: a record is
// stalled only once it has gone untouched longer than its stage's attempt may
// run plus the grace a retry needs, so a long embed is never mistaken for a
// stopped one, and a finished or stored file is never stalled at all.
func TestStalledWeighsEachStageAgainstItsOwnBudget(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(idle time.Duration) int64 { return now.Add(-idle).Unix() }
	cases := []struct {
		f     File
		from  string
		stale bool
	}{
		{File{Status: fileReady, Stage: stageEmbed, Updated: at(2 * time.Hour)}, stageEmbed, false},
		{File{Status: fileReady, Stage: stageEmbed, Updated: at(4 * time.Hour)}, stageEmbed, true},
		{File{Status: fileIndexing, Stage: stageGraph, Updated: at(25 * time.Minute)}, stageGraph, false},
		{File{Status: fileIndexing, Stage: stageGraph, Updated: at(45 * time.Minute)}, stageGraph, true},
		{File{Status: fileIndexing, Stage: stageToc, Updated: at(35 * time.Minute)}, stageToc, false},
		{File{Status: fileQueued, Updated: at(time.Hour)}, "", true},
		{File{Status: fileForgetting, Updated: at(time.Hour)}, "", false},
		{File{Status: fileReady, Updated: at(48 * time.Hour)}, "", false},
		{File{Status: fileStoredAs, Stage: stageExtract, Updated: at(48 * time.Hour)}, "", false},
		{File{Status: fileFailed, Stage: stageToc, Updated: at(48 * time.Hour)}, "", false},
	}
	for _, c := range cases {
		from, stale := stalled(c.f, now)
		if from != c.from || stale != c.stale {
			t.Errorf("stalled(%s/%s, idle %s) = (%q, %v), want (%q, %v)",
				c.f.Status, c.f.Stage, now.Sub(time.Unix(c.f.Updated, 0)), from, stale, c.from, c.stale)
		}
	}
}
