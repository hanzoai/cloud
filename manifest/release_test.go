// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package manifest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// reset clears the process-wide index so each test observes its own server.
func reset() { index.once = sync.Once{}; index.byID = nil; index.err = nil }

func serveIndex(t *testing.T, body string, hits *int64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits != nil {
			atomic.AddInt64(hits, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func indexFor(name string) string {
	return fmt.Sprintf(`{"repo":"hanzoai/cloud","tag":"v1.2.3","binaries":[
	  {"name":%q,"os":%q,"arch":%q,"url":"https://s3.hanzo.ai/p/%s","sha256":"abc123"},
	  {"name":%q,"os":"plan9","arch":"mips","url":"https://s3.hanzo.ai/p/wrong","sha256":"dead"}
	]}`, name, runtime.GOOS, runtime.GOARCH, name, name)
}

func TestFromRelease_ResolvesURLAndSumForThisPlatform(t *testing.T) {
	reset()
	t.Setenv(Plugins, serveIndex(t, indexFor("dns"), nil))

	p, ok := App{Name: "dns"}.remote()
	if !ok {
		t.Fatal("expected dns to resolve from the release index")
	}
	if p.URL != "https://s3.hanzo.ai/p/dns" || p.Sum != "abc123" {
		t.Fatalf("url/sum = %q/%q", p.URL, p.Sum)
	}
	if p.Name != "dns" || !p.Lazy {
		t.Fatalf("name/lazy = %q/%v", p.Name, p.Lazy)
	}
}

// The whole point of the rung: an image that ships NO plugin binaries still
// resolves every app. dir is empty, so the on-disk rungs all miss.
func TestPlugin_EmptyImageFallsThroughToRelease(t *testing.T) {
	reset()
	t.Setenv(Plugins, serveIndex(t, indexFor("books"), nil))

	p := App{Name: "books"}.Plugin()
	if p.URL == "" {
		t.Fatalf("expected a release URL, got Path=%q", p.Path)
	}
	if p.Sum == "" {
		t.Fatal("URL without Sum — zip refuses this, and so should we")
	}
}

func TestFromRelease_EagerAppIsNotLazy(t *testing.T) {
	reset()
	t.Setenv(Plugins, serveIndex(t, indexFor("o11y"), nil))

	p, ok := App{Name: "o11y", Eager: true}.remote()
	if !ok || p.Lazy {
		t.Fatalf("eager app must start with the host: ok=%v lazy=%v", ok, p.Lazy)
	}
}

// 108 apps must not become 108 requests.
func TestLoadIndex_FetchedOncePerProcess(t *testing.T) {
	reset()
	var hits int64
	t.Setenv(Plugins, serveIndex(t, indexFor("dns"), &hits))

	for i := 0; i < 25; i++ {
		App{Name: "dns"}.remote()
	}
	if hits != 1 {
		t.Fatalf("fetched the index %d times, want 1", hits)
	}
}

func TestFromRelease_WrongPlatformIsNotAMatch(t *testing.T) {
	reset()
	body := `{"binaries":[{"name":"dns","os":"plan9","arch":"mips","url":"u","sha256":"s"}]}`
	t.Setenv(Plugins, serveIndex(t, body, nil))

	if _, ok := (App{Name: "dns"}).remote(); ok {
		t.Fatal("resolved a plan9/mips artifact on this host")
	}
}

func TestFromRelease_EntryWithoutDigestIsDropped(t *testing.T) {
	reset()
	body := fmt.Sprintf(`{"binaries":[{"name":"dns","os":%q,"arch":%q,"url":"u","sha256":""}]}`,
		runtime.GOOS, runtime.GOARCH)
	t.Setenv(Plugins, serveIndex(t, body, nil))

	if _, ok := (App{Name: "dns"}).remote(); ok {
		t.Fatal("accepted an artifact with no digest — that is the ACE vector")
	}
}

// No index configured is the DEFAULT deployment, not an error: plugins ship
// beside the host.
func TestFromRelease_NoIndexConfigured(t *testing.T) {
	reset()
	t.Setenv(Plugins, "")
	if _, ok := (App{Name: "dns"}).remote(); ok {
		t.Fatal("resolved from an index that was never configured")
	}
}

func TestFromRelease_UnreachableIndexIsNotFatal(t *testing.T) {
	reset()
	t.Setenv(Plugins, "http://127.0.0.1:1/nope.json")

	if _, ok := (App{Name: "dns"}).remote(); ok {
		t.Fatal("resolved from an unreachable index")
	}
	// Plugin() must still answer, naming the path a developer expects.
	if p := (App{Name: "dns"}).Plugin(); p.Path == "" {
		t.Fatal("Plugin() gave no fallback path when the index was unreachable")
	}
}

// An explicit operator override outranks the index — naming a binary and
// silently fetching a different one would be a lie.
func TestPlugin_ExplicitBinBeatsRelease(t *testing.T) {
	reset()
	t.Setenv(Plugins, serveIndex(t, indexFor("dns"), nil))
	t.Setenv("CLOUD_DNS_BIN", "/opt/mine/dns")

	if p := (App{Name: "dns"}).Plugin(); p.Path != "/opt/mine/dns" || p.URL != "" {
		t.Fatalf("explicit BIN lost to the index: path=%q url=%q", p.Path, p.URL)
	}
}

func TestPlugin_AddrBeatsRelease(t *testing.T) {
	reset()
	t.Setenv(Plugins, serveIndex(t, indexFor("dns"), nil))
	t.Setenv("CLOUD_DNS_ADDR", "10.0.0.9:9000")

	if p := (App{Name: "dns"}).Plugin(); p.Addr != "10.0.0.9:9000" || p.URL != "" {
		t.Fatalf("ADDR lost to the index: addr=%q url=%q", p.Addr, p.URL)
	}
}
