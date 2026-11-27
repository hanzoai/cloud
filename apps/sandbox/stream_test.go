package sandbox

// stream_test.go — a stream into a sandbox's socket carries bytes untouched, for the
// sandbox's lessee only, and once.

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

func TestAStreamCarriesBytesUntouchedAndOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := owning(t)
		reading(t)
		s.State.rt.str = &socat{streamer: s.State.rt.str}
		s.State.web = newRelay(s.State.rt)
		// A unix socket path is bounded at about 100 bytes; t.TempDir is longer.
		short, err := os.MkdirTemp("/tmp", "sb")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(short) })
		t.Setenv("ZIP_RUNTIME_DIR", short)

		// The listener in the cell: it answers each byte it is sent with its complement.
		cell := filepath.Join(short, "zap.sock")
		ln, err := net.Listen("unix", cell)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer func() { _ = c.Close() }()
					b := make([]byte, 1)
					for {
						if _, err := c.Read(b); err != nil {
							return
						}
						b[0] = ^b[0]
						if _, err := c.Write(b); err != nil {
							return
						}
					}
				}()
			}
		}()

		person := func(sub string) context.Context {
			return zip.WithCaller(context.Background(), zip.Caller{
				Org: account.SignupOrg, Owner: account.SignupOrg, User: sub, Name: sub,
			})
		}
		alice := person("8d2f0c4e-0000-4000-8000-000000000001")
		leased, err := planeLease(alice, &client.LeaseIn{Class: "exec"})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}

		if _, err := planeStream(alice, &client.StreamIn{ID: leased.ID, Path: "relative.sock"}); err == nil {
			t.Error("a stream to a relative path was opened")
		}
		if _, err := planeStream(person("8d2f0c4e-0000-4000-8000-000000000002"), &client.StreamIn{ID: leased.ID, Path: cell}); err == nil {
			t.Error("a person who did not lease the sandbox was given a stream into it")
		}

		out, err := planeStream(alice, &client.StreamIn{ID: leased.ID, Path: cell})
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		c, err := net.DialTimeout("unix", out.Socket, 5*time.Second)
		if err != nil {
			t.Fatalf("dial %s: %v", out.Socket, err)
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := c.Write([]byte{0x01, 0xfe}); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 2)
		if _, err := io.ReadFull(c, got); err != nil || got[0] != 0xfe || got[1] != 0x01 {
			t.Fatalf("the cell answered %x, %v; want the complement, unchanged in transit", got, err)
		}

		if _, err := net.DialTimeout("unix", out.Socket, time.Second); err == nil {
			t.Error("a stream's socket took a second connection")
		}
	})
}
