// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBump_WritesCanonicalAndRefusesBadIndex(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "plugins.lock")
	idx := filepath.Join(dir, "binaries.json")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	good := `{"repo":"hanzoai/dns","tag":"v1.0.0","binaries":[{"name":"dns","os":"linux","arch":"amd64","url":"https://s3.hanzo.ai/p/dns","sha256":"` + strings.Repeat("ab", 32) + `"}]}`
	if err := os.WriteFile(idx, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"bump", idx, lock}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(lock)
	if !strings.Contains(string(got), "dns hanzoai/dns v1.0.0\n  linux/amd64 https://s3.hanzo.ai/p/dns sha256:"+strings.Repeat("ab", 32)) {
		t.Fatalf("lock:\n%s", got)
	}
	before := string(got)
	bad := strings.Replace(good, strings.Repeat("ab", 32), "", 1)
	if err := os.WriteFile(idx, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"bump", idx, lock}); err == nil {
		t.Fatal("a binary without a digest was pinned")
	}
	after, _ := os.ReadFile(lock)
	if string(after) != before {
		t.Fatal("a refused bump changed the lock")
	}
}
