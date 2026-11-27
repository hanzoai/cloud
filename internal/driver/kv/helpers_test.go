package kv

import (
	"bytes"
	"context"
	"net"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/hanzoai/cloud/sdk/coord"
)

const (
	presenceTTL  = time.Second
	skew         = 500 * time.Millisecond
	refreshEvery = 50 * time.Millisecond
	term         = 600 * time.Millisecond
	openFor      = 300 * time.Millisecond
)

// fast is the periods every Coord of these tests keeps, so a test sees a lease
// change hands in a second rather than a minute.
var fast = coord.Periods{
	Term: term, Effect: 150 * time.Millisecond, Confirm: 50 * time.Millisecond, Watch: 10 * time.Millisecond,
	Presence: presenceTTL, Skew: skew, Refresh: refreshEvery,
	Open: openFor,
}

var secret = bytes.Repeat([]byte{7}, 32)

// eventually waits for ok, up to d.
func eventually(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("after %v: %s", d, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ids(ms []coord.Member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// valkey starts a Valkey server for one test, with extra arguments, and answers its
// URL and a restart that brings it back empty on the same port (a failover that
// lost everything).
func valkey(t testing.TB, args ...string) (string, func()) {
	t.Helper()
	bin, err := exec.LookPath("valkey-server")
	if err != nil {
		t.Skip("valkey-server not installed")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	dir := t.TempDir()
	var cmd *exec.Cmd
	start := func() {
		cmd = exec.Command(bin, append([]string{"--port", strconv.Itoa(port), "--save", "", "--appendonly", "no", "--dir", dir}, args...)...)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		for range 1000 { // ten seconds: a loaded machine starts it slowly
			if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
				c.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("valkey-server did not start")
	}
	start()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	restart := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		start()
	}
	return "kv://127.0.0.1:" + strconv.Itoa(port), restart
}

// miniPod is a pod on the in-process KV at mr, not joined to any live set: a count
// needs no membership.
func miniPod(t *testing.T, mr *miniredis.Miniredis, id string) *coord.Coord {
	t.Helper()
	c, err := coord.New(coord.Config{Periods: fast, URL: "kv://" + mr.Addr(), Deployment: "test", Self: coord.Member{ID: id, Addr: id + ":8443"}, Instance: id, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}
