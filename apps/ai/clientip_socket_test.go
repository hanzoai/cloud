// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package ai

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clientip"
	"github.com/zap-proto/zip"
)

// THE TOPOLOGY THAT SHIPS: a HOST and a CHILD with a socket between them.
//
// ai is its own OS process (the pod runs `/cloud` and `/ai` side by side) and the
// host reaches it over a unix socket. Every earlier test in this family ran both
// halves in ONE process and passed while production was broken — function, adapter,
// composition, each one short of the seam that ships. This one puts the socket in.
//
// It is also the observation the diagnosis rested on: the child's RemoteAddr is
// logged, so the address the handler actually sees is measured rather than deduced.
func TestTheCallersAddressCrossesTheProcessBoundary(t *testing.T) {
	type arrival struct{ stamped, remote string }
	seen := make(chan arrival, 4)

	// The CHILD: ai's shape — an http.Handler behind AdaptNetHTTP, in its own app.
	landing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- arrival{stamped: clientip.ClientIPAcross(r), remote: r.RemoteAddr}
		w.WriteHeader(http.StatusNoContent)
	})
	child := zip.New(zip.Config{DisableStartupMessage: true})
	child.All("/v1/*", zip.AdaptNetHTTP(landing))

	// SHORT PATH ON PURPOSE: a unix socket address is bounded (~104 bytes on darwin)
	// and t.TempDir() names itself after the test, which overruns it — the listener
	// then fails silently and the test reads as "the child never started".
	dir, err := os.MkdirTemp("", "zt")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	go func() { _ = child.Listen(sock) }()
	t.Cleanup(func() { _ = child.Shutdown() })
	for i := 0; ; i++ {
		c, err := net.Dial("unix", sock)
		if err == nil {
			_ = c.Close()
			break
		}
		if i > 100 {
			t.Fatalf("child never began listening on %s", sock)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The HOST: stamp FIRST, then include the child. Order is load-bearing — zip
	// visits an included App with the stack as it stood at the inclusion site, so a
	// Use written after this would never reach it.
	remote, perr := zip.Proxy("/v1", sock)
	if perr != nil {
		t.Fatalf("proxying to the child: %v", perr)
	}
	host := zip.New(zip.Config{DisableStartupMessage: true})
	host.Use(zip.H(clientip.StampClientIP))
	host.Use(remote)

	call := func(forwarded string) arrival {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/chat/public", nil)
		req.Header.Set("X-Forwarded-For", forwarded)
		resp, err := host.Fiber().Test(req)
		if err != nil {
			t.Fatalf("serving through the host: %v", err)
		}
		_ = resp.Body.Close()
		select {
		case a := <-seen:
			return a
		case <-time.After(5 * time.Second):
			t.Fatal("the request never reached the child")
			return arrival{}
		}
	}

	a := call("203.0.113.10")
	b := call("203.0.113.11")

	// THE OBSERVATION. What the child sees on its own is the same for every caller,
	// which is exactly why the host has to tell it.
	t.Logf("child RemoteAddr: %q and %q", a.remote, b.remote)
	t.Logf("stamped by host:  %q and %q", a.stamped, b.stamped)

	if a.stamped == "" || b.stamped == "" {
		t.Fatalf("no address survived the process boundary: %q and %q", a.stamped, b.stamped)
	}
	if a.stamped == b.stamped {
		t.Fatalf("two callers arrived at the child as ONE address (%q): the per-visitor ceiling "+
			"is one bucket for everyone", a.stamped)
	}
	if a.remote == b.remote && a.remote != "" {
		t.Logf("confirmed: the child's own RemoteAddr is constant (%q) — anything derived "+
			"from it is one visitor for the whole internet", a.remote)
	}
}
