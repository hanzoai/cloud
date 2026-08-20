// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package ai

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/ai/address"
	"github.com/hanzoai/cloud/clientip"
	"github.com/zap-proto/zip"
)

// A MEASUREMENT, NOT A FIX. It answers one question and writes down the answer:
// what does the host stamp, and what does the child receive.
//
// Five fixes have been written on an inference about where the value is lost, and
// each was tested at the level the inference named — which is never the level that
// ships. So this logs BOTH ENDS of the socket in one pass. Three outcomes select
// between the live candidates:
//
//	present and correct → the transit is fine; the collapse is downstream of it
//	present but EMPTY   → the host stamped nothing (its ClientIP answered "")
//	ABSENT              → the value never crossed: not stamped, or stripped
//
// The mount is zip.Load — the same door the host uses for a real plugin — over a
// real unix socket, because the rung under test is TRANSIT between two endpoints
// that are each individually correct.
func TestWhatTheChildActuallyReceives(t *testing.T) {
	type seen struct {
		got     string
		present bool
		all     []string
	}
	arrived := make(chan seen, 4)

	// ---- the CHILD: ai's shape, on its own socket ----
	landing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := r.Header[http.CanonicalHeaderKey(address.Header)]
		s := seen{present: ok}
		if ok && len(v) > 0 {
			s.got = v[0]
		}
		for k := range r.Header {
			s.all = append(s.all, k)
		}
		arrived <- s
		w.WriteHeader(http.StatusNoContent)
	})
	child := zip.New(zip.Config{DisableStartupMessage: true})
	child.All("/v1/*", zip.AdaptNetHTTP(landing))

	dir, err := os.MkdirTemp("", "zt")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	go func() { _ = child.Listen(sock) }()
	t.Cleanup(func() { _ = child.Shutdown() })
	for i := 0; ; i++ {
		if c, e := net.Dial("unix", sock); e == nil {
			_ = c.Close()
			break
		} else if i > 150 {
			t.Fatalf("child never listened on %s", sock)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// ---- the HOST: stamp, observe what it stamped, then mount the plugin ----
	var stamped string
	var stampRan bool
	host := zip.New(zip.Config{DisableStartupMessage: true})
	host.Use(zip.H(clientip.StampClientIP))
	host.Use(zip.H(func(c *zip.Ctx) error {
		stampRan = true
		stamped = string(c.Fiber().Request().Header.Peek(address.Header))
		return c.Continue()
	}))

	// zip.Load with Addr: the REAL plugin mount door, pointed at an already-running
	// instance so nothing is spawned. This is the hop that ships.
	leaf, err := zip.Load(zip.Plugin{Name: "probe", Addr: sock}, "/v1")
	if err != nil {
		t.Fatalf("mounting the plugin: %v", err)
	}
	host.Use(leaf)

	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/chat/public", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	resp, err := host.Fiber().Test(req)
	if err != nil {
		t.Fatalf("serving: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case s := <-arrived:
		t.Logf("HOST  middleware ran = %v", stampRan)
		t.Logf("HOST  stamped        = %q", stamped)
		t.Logf("CHILD header present = %v", s.present)
		t.Logf("CHILD received       = %q", s.got)
		t.Logf("CHILD saw headers    = %v", s.all)
		switch {
		case !s.present:
			t.Logf("VERDICT: ABSENT at the child — the value never crossed")
		case s.got == "":
			t.Logf("VERDICT: PRESENT BUT EMPTY — the host stamped nothing")
		default:
			t.Logf("VERDICT: PRESENT AND CORRECT — transit is fine")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the request never reached the child (host middleware ran=%v, stamped=%q)", stampRan, stamped)
	}
	fmt.Fprintln(os.Stderr, "measurement complete")
}
