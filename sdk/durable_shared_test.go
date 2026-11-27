package cloud

// durable_shared_test.go — ONE ENGINE ON SEVERAL REPLICAS, through the seam.
//
// The embedded tasks engine kept its rows in shard files of one process, so a schedule
// or a workflow started on one pod lived and died with it. With CLOUD_SQL_DSN set it
// keeps them in a hanzo-sql schema (tasks-<app>), and every replica that opens the
// schema is the same engine. hanzoai/tasks proves the engine's side on both engines;
// these hold the host's: the schema is opened through locus.Open with the seam's
// connection budget and statement checks, a SIGKILLed pod's work is finished by
// another, nothing runs twice when replicas contend, and files that hold an engine's
// old rows move to the schema only when CLOUD_SQL_MOVE says so.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/driver/kv/kvtest"
	"github.com/hanzoai/cloud/internal/driver/postgres"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	tasksworkflow "github.com/hanzoai/tasks/pkg/sdk/workflow"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
	taskstore "github.com/hanzoai/tasks/pkg/tasks/store"
	luxlog "github.com/luxfi/log"
)

const (
	haApp      = "auto" // an app that runs an engine on hanzo-sql (manifest.SQLStores: tasks-auto)
	haChildEnv = "CLOUD_HA_TASKS_CHILD"
	haLogEnv   = "CLOUD_HA_TASKS_LOG"
	haQueue    = "ha-queue"
)

// haActivity is the work a workflow does. Where it runs is told by haLogEnv, a file
// every run of it appends one line to: the replica that is killed blocks in it after
// writing its line, so its work is never finished by it.
func haActivity(ctx context.Context, id string) (string, error) {
	who := "survivor"
	if os.Getenv(haChildEnv) != "" {
		who = "doomed"
	}
	if path := os.Getenv(haLogEnv); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(f, "%s %s\n", who, id)
		_ = f.Close()
	}
	if os.Getenv(haChildEnv) != "" {
		select {} // the pod that is killed in the middle of the step
	}
	haRan.Add(1)
	return id, nil
}

var haRan atomic.Int32

func haWorkflow(ctx tasksworkflow.Context, id string) (string, error) {
	ctx = tasksworkflow.WithActivityOptions(ctx, tasksworkflow.ActivityOptions{StartToCloseTimeout: time.Minute})
	var out string
	if err := tasksworkflow.ExecuteActivity(ctx, haActivity, id).Get(ctx, &out); err != nil {
		return "", err
	}
	return out, nil
}

// haReplica is one pod's engine: its own pool onto the schema, its own listener, and
// the worker its apps hold.
type haReplica struct {
	emb  *tasksengine.Embedded
	cli  tasksclient.Client
	sock string
}

// startHA starts the engine of one replica on a schema of its own pool, and a worker
// for haWorkflow beside it, as InstallDurableIngest and an app would.
func startHA(t *testing.T, name, sock string) *haReplica {
	t.Helper()
	db, err := locus.Open(namespace.System(), "tasks-"+haApp, haDir(t))
	if err != nil {
		t.Fatalf("open the engine's schema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	emb, err := tasksengine.Embed(context.Background(), durableConfig(sock, "", db, name, nil))
	if err != nil {
		t.Fatalf("embed %s: %v", name, err)
	}
	cli, err := tasksclient.Dial(tasksclient.Options{Address: sock, Namespace: "default"})
	if err != nil {
		t.Fatalf("dial %s: %v", name, err)
	}
	w := tasksworker.New(cli, haQueue, tasksworker.Options{Logger: luxlog.New()})
	w.RegisterWorkflowWithOptions(haWorkflow, tasksworker.RegisterWorkflowOptions{Name: "haWorkflow"})
	w.RegisterActivity(haActivity)
	if err := w.Start(); err != nil {
		t.Fatalf("worker %s: %v", name, err)
	}
	t.Cleanup(func() { w.Stop(); cli.Close(); _ = emb.Stop(context.Background()) })
	return &haReplica{emb: emb, cli: cli, sock: sock}
}

// haDir is where the engine's schema would be a file: nowhere, on hanzo-sql, where the
// seam reads no directory.
func haDir(t *testing.T) string {
	t.Helper()
	if locus.DSN() != "" {
		return ""
	}
	return t.TempDir()
}

func haSocket(t *testing.T, name string) string {
	t.Helper()
	// A unix socket path is limited to 107 bytes, and a test's own directory is not short.
	dir, err := os.MkdirTemp("", "ha-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name+".sock")
}

func haWait(t *testing.T, cli tasksclient.Client, id, run string, within time.Duration) tasksclient.WorkflowStatus {
	t.Helper()
	deadline := time.Now().Add(within)
	var last tasksclient.WorkflowStatus
	for time.Now().Before(deadline) {
		if info, err := cli.DescribeWorkflow(context.Background(), id, run); err == nil {
			if last = info.Status; last == tasksclient.WorkflowStatusCompleted || last == tasksclient.WorkflowStatusFailed {
				return last
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}

// The engine of an app that declares one runs on its schema when the deployment has
// hanzo-sql, and on the file it always had when the deployment has not; one process on
// either runs a workflow to its end through the same code.
func TestDurableEngineRunsOnEitherEngine(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv(haLogEnv, "")
		t.Setenv(haChildEnv, "")
		ctx := context.Background()
		dir := t.TempDir()
		db, err := durableDatabase(ctx, haApp, dir)
		if err != nil {
			t.Fatal(err)
		}
		if onSQL := locus.DSN() != ""; onSQL != (db != nil) {
			t.Fatalf("with the DSN %v the engine's database is %v", onSQL, db)
		}
		if db != nil {
			_ = db.Close()
		}
		r := startHA(t, "solo", haSocket(t, "solo"))
		haRan.Store(0)
		run, err := r.cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{ID: "solo-wf", TaskQueue: haQueue}, "haWorkflow", "solo-wf")
		if err != nil {
			t.Fatal(err)
		}
		if got := haWait(t, r.cli, run.GetID(), run.GetRunID(), 20*time.Second); got != tasksclient.WorkflowStatusCompleted {
			t.Fatalf("the workflow did not complete: %d", got)
		}
	})
}

// An app that does not declare an engine on hanzo-sql keeps its engine on files, and a
// word that is not a move policy is refused rather than guessed.
func TestDurableDatabasePolicy(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		ctx := context.Background()
		if db, err := durableDatabase(ctx, "an-app-with-no-engine", t.TempDir()); err != nil || db != nil {
			t.Fatalf("an app that declares no engine on hanzo-sql was given %v, %v", db, err)
		}
		t.Setenv(locus.MoveEnv, "eager")
		if _, err := durableDatabase(ctx, haApp, t.TempDir()); err == nil {
			t.Fatal("a move policy nobody defined was accepted")
		}
		t.Setenv(locus.MoveEnv, "")
		db, err := durableDatabase(ctx, haApp, t.TempDir())
		if err != nil || db == nil {
			t.Fatalf("an engine with no files is born on the schema: %v %v", db, err)
		}
		_ = db.Close()
		// An engine does not open beside a move of its files that is under way, or that
		// stopped halfway: the rows it would write are the move's to clear.
		mdb, err := locus.Open(namespace.System(), "tasks-"+haApp, "")
		if err != nil {
			t.Fatal(err)
		}
		sh, err := taskstore.OpenShared(ctx, mdb)
		if err != nil {
			t.Fatal(err)
		}
		if err := sh.SetMeta(ctx, "moving", "then"); err != nil {
			t.Fatal(err)
		}
		_ = mdb.Close()
		if db, err := durableDatabase(ctx, haApp, t.TempDir()); err == nil {
			_ = db.Close()
			t.Fatal("an engine opened beside a move of its files that was under way")
		}
		for _, app := range []string{"ai", "auto", "code", "compute", "function", "git", "knowledge", "marketing", "studio", "workflow"} {
			if got := manifest.SQLStores["tasks-"+app]; got != app {
				t.Errorf("tasks-%s is declared for %q: the app that embeds the engine must own its schema's connections", app, got)
			}
		}
	})
}

// Files that hold an engine's rows stay where they are until CLOUD_SQL_MOVE says to move
// them; then they move once, before the engine opens, and what was in flight is taken
// up by the engine on the schema.
func TestDurableFilesMoveOnlyWhenAsked(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only")
		}
		t.Setenv(haLogEnv, "")
		t.Setenv(haChildEnv, "")
		ctx := context.Background()
		dir := t.TempDir()

		// An engine on files, with a workflow that finished.
		sock := haSocket(t, "files")
		files, err := tasksengine.Embed(ctx, tasksengine.EmbedConfig{Address: sock, DataDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		cli, err := tasksclient.Dial(tasksclient.Options{Address: sock, Namespace: "default"})
		if err != nil {
			t.Fatal(err)
		}
		w := tasksworker.New(cli, haQueue, tasksworker.Options{Logger: luxlog.New()})
		w.RegisterWorkflowWithOptions(haWorkflow, tasksworker.RegisterWorkflowOptions{Name: "haWorkflow"})
		w.RegisterActivity(haActivity)
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		done, err := cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{ID: "before-the-move", TaskQueue: haQueue}, "haWorkflow", "before-the-move")
		if err != nil {
			t.Fatal(err)
		}
		if got := haWait(t, cli, done.GetID(), done.GetRunID(), 20*time.Second); got != tasksclient.WorkflowStatusCompleted {
			t.Fatalf("the workflow on files did not complete: %d", got)
		}
		w.Stop()
		cli.Close()
		if err := files.Stop(ctx); err != nil {
			t.Fatal(err)
		}

		// The DSN is set and the policy is not: the engine stays on its files.
		t.Setenv(locus.MoveEnv, "")
		if db, err := durableDatabase(ctx, haApp, dir); err != nil || db != nil {
			t.Fatalf("with rows in files and no move policy the engine must keep them: %v %v", db, err)
		}
		// Asked: they move, once.
		t.Setenv(locus.MoveEnv, "lazy")
		db, err := durableDatabase(ctx, haApp, dir)
		if err != nil || db == nil {
			t.Fatalf("the move: %v %v", db, err)
		}
		_ = db.Close()
		if moved, _ := tasksengineMoved(t, dir); !moved {
			t.Fatal("the rows are not recorded as moved")
		}
		// And the engine on the schema has what the files had.
		t.Setenv(locus.MoveEnv, "")
		r := startHA(t, "moved", haSocket(t, "moved"))
		info, err := r.cli.DescribeWorkflow(ctx, done.GetID(), done.GetRunID())
		if err != nil || info.Status != tasksclient.WorkflowStatusCompleted {
			t.Fatalf("the workflow that finished before the move: %v %v", info, err)
		}
		// Moved is moved: the files are no longer asked about.
		if db, err := durableDatabase(ctx, haApp, dir); err != nil || db == nil {
			t.Fatalf("a moved engine stays on the schema: %v %v", db, err)
		} else {
			_ = db.Close()
		}
	})
}

func tasksengineMoved(t *testing.T, dir string) (bool, error) {
	t.Helper()
	db, err := locus.Open(namespace.System(), "tasks-"+haApp, haDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	return tasksengine.Moved(context.Background(), db)
}

// Replicas of one engine, each with a pool of its own held to the seam's connection
// budget, and workflows started on both and signalled from the other while they run:
// every activity runs once, and nothing starves for want of a connection.
func TestSharedEngineReplicasContendOnTheSeam(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("two replicas share a server; a SQLite file has one")
		}
		t.Setenv(haChildEnv, "")
		logPath := filepath.Join(t.TempDir(), "ran.log")
		t.Setenv(haLogEnv, logPath)
		t.Setenv(postgres.ConnsEnv, "12") // a small replica: two engines, four connections each, and room
		a, b := startHA(t, "a", haSocket(t, "a")), startHA(t, "b", haSocket(t, "b"))
		time.Sleep(200 * time.Millisecond)

		const n = 30
		reps := []*haReplica{a, b}
		runs := make([]tasksclient.WorkflowRun, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := fmt.Sprintf("contend-%02d", i)
				run, err := reps[i%2].cli.ExecuteWorkflow(context.Background(), tasksclient.StartWorkflowOptions{ID: id, TaskQueue: haQueue}, "haWorkflow", id)
				if err != nil {
					t.Errorf("start %s: %v", id, err)
					return
				}
				runs[i] = run
				for k := 0; k < 3; k++ { // the other replica pokes it while its owner works it
					_ = reps[(i+1)%2].cli.SignalWorkflow(context.Background(), id, run.GetRunID(), "poke", nil)
				}
			}()
		}
		wg.Wait()
		for i, run := range runs {
			if run == nil {
				continue
			}
			if got := haWait(t, reps[(i+1)%2].cli, run.GetID(), run.GetRunID(), 60*time.Second); got != tasksclient.WorkflowStatusCompleted {
				t.Fatalf("%s did not complete: %d", run.GetID(), got)
			}
		}
		raw, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		ran := map[string]int{}
		for _, line := range strings.Fields(string(raw)) {
			ran[line]++
		}
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("contend-%02d", i)
			if got := ran[id]; got != 1 {
				t.Errorf("%s: its activity ran %d times, want exactly once", id, got)
			}
		}
	})
}

// A pod is killed in the middle of a step, and another pod's engine finishes the
// workflow: the work was owned by the killed pod's engine, whose membership lapses,
// and the survivor takes it up and runs the step once. The pod that dies is a real
// process, killed with SIGKILL; the survivor is this one.
func TestSharedEngineOutlivesAKilledPod(t *testing.T) {
	if os.Getenv(haChildEnv) != "" {
		return // the child runs TestSharedEngineChild alone
	}
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("two replicas share a server; a SQLite file has one")
		}
		logPath := filepath.Join(t.TempDir(), "ran.log")
		t.Setenv(haLogEnv, logPath)
		t.Setenv(haChildEnv, "")
		survivor := startHA(t, "survivor", haSocket(t, "survivor"))
		time.Sleep(200 * time.Millisecond)

		// The pod that will die: the same schema, a listener of its own.
		doomedSock := haSocket(t, "doomed")
		cmd := exec.Command(os.Args[0], "-test.run", "^TestSharedEngineChild$", "-test.v")
		cmd.Env = append(os.Environ(), haChildEnv+"="+doomedSock, haLogEnv+"="+logPath)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		if err := awaitLine(stdout, "HA-CHILD-READY", 60*time.Second); err != nil {
			t.Fatalf("the pod that will die never came up: %v", err)
		}

		// Start the workflow on the doomed pod, and wait for its step to begin there.
		doomed, err := tasksclient.Dial(tasksclient.Options{Address: doomedSock, Namespace: "default"})
		if err != nil {
			t.Fatal(err)
		}
		defer doomed.Close()
		run, err := doomed.ExecuteWorkflow(context.Background(), tasksclient.StartWorkflowOptions{ID: "killed-pod-wf", TaskQueue: haQueue}, "haWorkflow", "killed-pod-wf")
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for !strings.Contains(readFile(logPath), "doomed killed-pod-wf") {
			if time.Now().After(deadline) {
				t.Fatalf("the step never began on the doomed pod; log: %q", readFile(logPath))
			}
			time.Sleep(50 * time.Millisecond)
		}

		// Killed, as the kubelet kills a pod that does not stop: no leave, no goodbye.
		if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()

		// Its membership lapses (15 s, and a beat to notice), and the survivor finishes it.
		if got := haWait(t, survivor.cli, run.GetID(), run.GetRunID(), 90*time.Second); got != tasksclient.WorkflowStatusCompleted {
			t.Fatalf("the workflow did not complete on the survivor after its pod was killed; status=%d log=%q", got, readFile(logPath))
		}
		lines := strings.Split(strings.TrimSpace(readFile(logPath)), "\n")
		var onDoomed, onSurvivor int
		for _, l := range lines {
			switch l {
			case "survivor killed-pod-wf":
				onSurvivor++
			case "doomed killed-pod-wf":
				onDoomed++
			}
		}
		if onDoomed != 1 || onSurvivor != 1 {
			t.Fatalf("the step began %d times on the killed pod and %d on the survivor, want 1 and 1: %q", onDoomed, onSurvivor, lines)
		}
	})
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// TestSharedEngineChild is the pod that dies: an engine on the same schema with a
// worker whose step blocks. It is a no-op unless the parent set the environment.
func TestSharedEngineChild(t *testing.T) {
	sock := os.Getenv(haChildEnv)
	if sock == "" {
		return
	}
	db, err := locus.Open(namespace.System(), "tasks-"+haApp, haDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := tasksengine.Embed(context.Background(), durableConfig(sock, "", db, "doomed", nil)); err != nil {
		t.Fatal(err)
	}
	cli, err := tasksclient.Dial(tasksclient.Options{Address: sock, Namespace: "default"})
	if err != nil {
		t.Fatal(err)
	}
	w := tasksworker.New(cli, haQueue, tasksworker.Options{Logger: luxlog.New()})
	w.RegisterWorkflowWithOptions(haWorkflow, tasksworker.RegisterWorkflowOptions{Name: "haWorkflow"})
	w.RegisterActivity(haActivity)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("HA-CHILD-READY")
	select {} // until the parent kills it
}

// The live feed of one replica's engine carries the events of the others': a browser on
// /v1/workflow/events sees the deployment, not the pod the edge chose.
func TestSharedEngineFeedCrossesPods(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("two replicas share a server; a SQLite file has one")
		}
		t.Setenv(haLogEnv, "")
		t.Setenv(haChildEnv, "")
		pod, _ := kvtest.Mini(t)
		var embs [2]*tasksengine.Embedded
		var clis [2]tasksclient.Client
		for i, name := range []string{"a", "b"} {
			relay, hear := relayEvents(pod(name), haApp)
			db, err := locus.Open(namespace.System(), "tasks-"+haApp, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			sock := haSocket(t, name)
			emb, err := tasksengine.Embed(context.Background(), durableConfig(sock, "", db, name, relay))
			if err != nil {
				t.Fatal(err)
			}
			stop := hear(emb)
			t.Cleanup(func() { stop(); _ = emb.Stop(context.Background()) })
			cli, err := tasksclient.Dial(tasksclient.Options{Address: sock, Namespace: "default"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cli.Close)
			embs[i], clis[i] = emb, cli
		}

		// A browser on pod a.
		srv := httptest.NewServer(embs[0].EventsHandler())
		t.Cleanup(srv.Close)
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		seen := make(chan string, 16)
		go func() {
			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
				if line := sc.Text(); strings.HasPrefix(line, "event: ") {
					seen <- strings.TrimPrefix(line, "event: ")
				}
			}
		}()
		time.Sleep(200 * time.Millisecond)

		// The workflow starts on pod b.
		if _, err := clis[1].ExecuteWorkflow(context.Background(), tasksclient.StartWorkflowOptions{ID: "feed-wf", TaskQueue: haQueue}, "haWorkflow", "feed-wf"); err != nil {
			t.Fatal(err)
		}
		deadline := time.After(10 * time.Second)
		for {
			select {
			case kind := <-seen:
				if kind == "workflow.started" {
					return
				}
			case <-deadline:
				t.Fatal("a browser on pod a never saw the workflow pod b started")
			}
		}
	})
}

// Nobody has chosen how long finished runs are kept, so none is lost to the purge: a
// run closed longer ago than a namespace's retention (720h) is still there after the
// purge would have passed over it. The control, an engine that enforces the period,
// loses that run in hanzoai/tasks's own tests.
func TestDurableKeepsAFinishedRunPastItsRetention(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if locus.DSN() == "" {
			t.Skip("hanzo-sql only: the row is rewritten through a second handle, which a SQLite file does not share")
		}
		t.Setenv(haLogEnv, "")
		t.Setenv(haChildEnv, "")
		ctx := context.Background()
		r := startHA(t, "keeps", haSocket(t, "keeps"))
		run, err := r.cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{ID: "old-wf", TaskQueue: haQueue}, "haWorkflow", "old-wf")
		if err != nil {
			t.Fatal(err)
		}
		if got := haWait(t, r.cli, run.GetID(), run.GetRunID(), 20*time.Second); got != tasksclient.WorkflowStatusCompleted {
			t.Fatalf("the workflow did not complete: %d", got)
		}

		// It closed 800 hours ago.
		db, err := locus.Open(namespace.System(), "tasks-"+haApp, haDir(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		sh, err := taskstore.OpenShared(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		key := "wf/default/old-wf/" + run.GetRunID()
		raw, ok, err := sh.Get(ctx, taskstore.Principal{}, key)
		if err != nil || !ok {
			t.Fatalf("the run's row: %v %v", ok, err)
		}
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		row["closeTime"] = time.Now().Add(-800 * time.Hour).UTC().Format(time.RFC3339)
		raw, _ = json.Marshal(row)
		if err := sh.Put(ctx, taskstore.Principal{}, key, raw); err != nil {
			t.Fatal(err)
		}

		// The purge ticks every five seconds; wait out two of them.
		time.Sleep(11 * time.Second)
		info, err := r.cli.DescribeWorkflow(ctx, run.GetID(), run.GetRunID())
		if err != nil || info.Status != tasksclient.WorkflowStatusCompleted {
			t.Fatalf("a finished run older than 720h was lost to the purge: %v %v", info, err)
		}
		if !keepFinishedRuns {
			t.Fatal("the engine is configured to enforce retention: this test is about the configuration cloud ships")
		}
	})
}
