package git

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/apps/git/objects"
)

func readAll(t *testing.T, b objects.Blobs, org string, task int64) []string {
	t.Helper()
	r, truncated, err := logTail(context.Background(), b, org, task, 1<<30)
	if err != nil || truncated {
		t.Fatalf("logTail: %v truncated=%v", err, truncated)
	}
	defer r.Close()
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var l struct{ Content string }
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		out = append(out, l.Content)
	}
	return out
}

// An append is one object named by its first line; a resend writes the same object, and
// the log reads back whole, in order, with a line of any bytes as the line it was.
func TestRunLogIsObjectPerAppend(t *testing.T) {
	ctx := context.Background()
	b := objects.NewMemory()
	line := func(c ...string) []LogLine {
		var out []LogLine
		for _, s := range c {
			out = append(out, LogLine{Content: s})
		}
		return out
	}
	if n, err := putLog(ctx, b, "acme", 7, 0, line("a", "b\nc")); err != nil || n != 6 {
		t.Fatalf("first append: %d, %v", n, err)
	}
	if _, err := putLog(ctx, b, "acme", 7, 2, line("\xff\x00d")); err != nil {
		t.Fatal(err)
	}
	if _, err := putLog(ctx, b, "acme", 7, 2, line("\xff\x00d")); err != nil { // resent
		t.Fatal(err)
	}
	got := readAll(t, b, "acme", 7)
	if len(got) != 3 || got[1] != "b\nc" {
		t.Fatalf("log = %q", got)
	}
	if got := readAll(t, b, "acme", 8); len(got) != 0 {
		t.Fatalf("another task's log = %q", got)
	}
	if _, err := putLog(ctx, b, "a/b", 1, 0, line("x")); err == nil {
		t.Fatal("an org that is a path was accepted")
	}
}

// The files this host kept are carried once, read as the first lines of the log, and
// renamed so a second pass carries nothing.
func TestOldRunLogsAreCarriedOnce(t *testing.T) {
	ctx := context.Background()
	b := objects.NewMemory()
	dir := t.TempDir()
	old := legacyLog(dir, "acme", 5)
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("{\"content\":\"old1\"}\n{\"content\":\"old2\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := legacyLog(dir, "acme", 6)
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := carryLogs(ctx, b, dir)
	if err != nil || n != 2 {
		t.Fatalf("carried %d: %v", n, err)
	}
	if _, err := putLog(ctx, b, "acme", 5, 2, []LogLine{{Content: "new"}}); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, b, "acme", 5)
	if len(got) != 3 || got[0] != "old1" || got[2] != "new" {
		t.Fatalf("log = %q", got)
	}
	if _, err := os.Stat(old + ".carried"); err != nil {
		t.Fatal("the file was not renamed")
	}
	if n, err := carryLogs(ctx, b, dir); err != nil || n != 0 {
		t.Fatalf("second pass carried %d: %v", n, err)
	}
}
