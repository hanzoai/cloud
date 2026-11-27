// Package kvtest starts the KV a test of pods that share one needs: a Valkey
// server, or miniredis in this process. Importing it links the KV driver, so the
// pods it makes dial the KV as production's do.
package kvtest

import (
	"context"
	"net"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	_ "github.com/hanzoai/cloud/internal/driver/kv"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
)

// KV starts a Valkey server for one test and answers its KV_URL. A machine without
// valkey-server skips the test.
func KV(t testing.TB) string {
	t.Helper()
	bin, err := exec.LookPath("valkey-server")
	if err != nil {
		t.Skip("valkey-server not installed")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	cmd := exec.Command(bin, "--port", port, "--save", "", "--appendonly", "no", "--dir", t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for range 1000 { // ten seconds: a loaded machine starts it slowly
		if c, err := net.Dial("tcp", "127.0.0.1:"+port); err == nil {
			c.Close()
			return "kv://127.0.0.1:" + port
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("valkey-server did not start")
	return ""
}

// Mini is a deployment's KV in this process that speaks the KV's protocol
// (miniredis): the pods it makes dial it as they would a Valkey, subscriptions and
// scripts included, and need no valkey-server — so a test of what pods say to each
// other runs on every machine and in CI, and over a connection that can drop.
func Mini(t testing.TB) (pod func(id string) *coord.Coord, kv *miniredis.Miniredis) {
	t.Helper()
	kv = miniredis.RunT(t)
	return func(id string) *coord.Coord {
		c, err := coord.New(coord.Config{
			URL:        "kv://" + kv.Addr(),
			Deployment: "test",
			Self:       coord.Member{ID: id, Addr: id + ":8443"},
			Instance:   id,
			Secret:     coordtest.Secret,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close(context.Background()) })
		return c
	}, kv
}

// Pod is the root of pod id on the KV at url: it joins the live set, and leaves it
// when the test ends.
func Pod(t testing.TB, url, id string) *coord.Coord {
	t.Helper()
	c, err := coord.New(coord.Config{
		URL:        url,
		Deployment: "test",
		Self:       coord.Member{ID: id, Addr: id + ":8443"},
		Instance:   id,
		Secret:     coordtest.Secret,
		Root:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Start(context.Background())
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}
