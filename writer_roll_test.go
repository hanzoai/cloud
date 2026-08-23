package cloud

// The roll property, tested against the shutdown the fleet actually performs.
//
// A roll of this deployment does NOT close the stores. Every store fd is held by
// a zip plugin child, not by the host: in production the host process holds zero
// descriptors under DataDir and the 112 children hold all of them. When the host
// is signalled it calls zip's shutdown hook, which is stop(in, 0) — straight to
// Process.Kill(), no SIGTERM, no drain. So every store under DataDir is
// crash-terminated on every roll, and the question the board asked ("prove a
// rolling upgrade that does not drop writes") is really this question: does a
// SIGKILL of the process holding the store lose a write that was already
// acknowledged?
//
// It must not, and the reason is the pragma set, not the shutdown path: the orm
// opens every store journal_mode=WAL, synchronous=NORMAL. At NORMAL the WAL is
// written through the OS page cache, which a process death does not touch — only
// an OS crash or power loss can lose a commit. So killing the writer is a case
// SQLite is specified to survive, and the store recovers its head from the WAL at
// next open.
//
// This test pins that, because it is the one property the roll rests on and it is
// a property of a DEPENDENCY's default config: if the orm ever ships
// synchronous=OFF, every roll starts silently losing acknowledged writes and
// nothing else in this repo would notice.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	ormdb "github.com/hanzoai/orm/db"
)

const (
	rollHelperEnv = "CLOUD_WRITER_ROLL_HELPER"
	rollRecords   = 300
	rollKind      = "rollrec"
)

type rollRec struct {
	Seq int    `json:"seq"`
	V   string `json:"v"`
}

// openRollStore opens the store the way the apps do: the orm's own opener with a
// zero-value config, so the pragmas under test are the shipped defaults and not a
// copy of them that could drift.
func openRollStore(t *testing.T, path string) *ormdb.SQLiteDB {
	t.Helper()
	db, err := ormdb.NewSQLiteDB(&ormdb.SQLiteDBConfig{Path: path})
	if err != nil {
		t.Fatalf("open store %q: %v", path, err)
	}
	return db
}

func TestWriterRoll_SIGKILLedChildLosesNoAcknowledgedWrite(t *testing.T) {
	if os.Getenv(rollHelperEnv) != "" {
		t.Skip("helper process")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")

	// The child is the plugin: it opens the store, acknowledges writes, and never
	// gets a chance to close anything.
	cmd := exec.Command(os.Args[0], "-test.run", "TestWriterRollHelper", "-test.v")
	cmd.Env = append(os.Environ(), rollHelperEnv+"="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	if err := awaitLine(stdout, "ROLL-READY", 60*time.Second); err != nil {
		t.Fatalf("child never acknowledged its writes: %v", err)
	}

	// Exactly what zip does to a plugin child on host shutdown: stop(in, 0).
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	_ = cmd.Wait()
	if cmd.ProcessState.ExitCode() != -1 {
		t.Fatalf("child exited %d, wanted signal death", cmd.ProcessState.ExitCode())
	}

	// The new pod opens the same volume and recovers.
	db := openRollStore(t, path)
	defer func() { _ = db.Close() }()

	missing := 0
	for i := 0; i < rollRecords; i++ {
		var got rollRec
		key := db.NewKey(rollKind, fmt.Sprintf("r-%04d", i), 0, nil)
		if err := db.Get(context.Background(), key, &got); err != nil {
			missing++
			continue
		}
		if got.Seq != i {
			t.Fatalf("record %d recovered with seq %d", i, got.Seq)
		}
	}
	if missing != 0 {
		t.Fatalf("SIGKILL of the store-holding process LOST %d of %d acknowledged writes; "+
			"a roll drops writes (check the orm's journal_mode/synchronous defaults)",
			missing, rollRecords)
	}
	t.Logf("SIGKILL of the store holder: %d/%d acknowledged writes recovered, none lost", rollRecords, rollRecords)
}

// TestWriterRollHelper is the child half. It is a no-op unless the parent set the
// env, so a normal `go test ./...` never runs it standalone.
func TestWriterRollHelper(t *testing.T) {
	path := os.Getenv(rollHelperEnv)
	if path == "" {
		t.Skip("not the roll helper")
	}
	db := openRollStore(t, path)
	for i := 0; i < rollRecords; i++ {
		key := db.NewKey(rollKind, fmt.Sprintf("r-%04d", i), 0, nil)
		if _, err := db.Put(context.Background(), key, &rollRec{Seq: i, V: "acknowledged"}); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	// Acknowledged, and deliberately NOT closed — this is the SIGKILL case.
	fmt.Println("ROLL-READY")
	os.Stdout.Sync()
	select {}
}

// awaitLine reads r until it sees want on a line, or the deadline passes.
func awaitLine(r io.Reader, want string, timeout time.Duration) error {
	type res struct{ err error }
	ch := make(chan res, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if strings.Contains(sc.Text(), want) {
				ch <- res{nil}
				return
			}
		}
		ch <- res{fmt.Errorf("stream ended without %q: %v", want, sc.Err())}
	}()
	select {
	case r := <-ch:
		return r.err
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for %q", want)
	}
}
