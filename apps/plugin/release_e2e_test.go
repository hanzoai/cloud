package plugin

// The release lane, end to end, from the host's side. A plugin repo's release
// publishes <name>-<os>-<arch> and a binaries.json; `pin bump` turns that into a
// plugins.lock stanza; the host resolves its plugin through the lock and runs it
// by digest; and an operator swaps releases with POST /v1/admin/plugins/:name/reload
// without a request failing. Everything here is the production code path except
// the object store, which an httptest server stands in for.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/pin"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk/kit"
)

// release is one built release of the scratch plugin.
type release struct {
	version string
	bin     []byte
	sum     string
	url     string
}

// buildRelease builds the scratch plugin the way a release does: CGO_ENABLED=0,
// -trimpath, for the platform under test.
func buildRelease(t *testing.T, version string) release {
	t.Helper()
	kit.Tool(t, "go")
	out := filepath.Join(t.TempDir(), "billing")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+version, "-o", out, "./apps/plugin/testdata/scratch")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, b)
	}
	bin, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bin)
	return release{version: version, bin: bin, sum: hex.EncodeToString(sum[:])}
}

func TestAPluginIsLockedLoadedByDigestAndSwappedWithoutADroppedRequest(t *testing.T) {
	v1, v2 := buildRelease(t, "v1.0.0"), buildRelease(t, "v1.0.1")
	if v1.sum == v2.sum {
		t.Fatal("two releases with one digest")
	}

	var fetches atomic.Int64
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		for _, rel := range []release{v1, v2} {
			if r.URL.Path == "/plugins/hanzoai/billing/"+rel.version+"/billing-"+runtime.GOOS+"-"+runtime.GOARCH {
				_, _ = w.Write(rel.bin)
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer store.Close()
	// pin.Parse wants https; the lock is rewritten to the test server only after
	// the production parser has accepted the shape.
	for _, rel := range []*release{&v1, &v2} {
		rel.url = store.URL + "/plugins/hanzoai/billing/" + rel.version + "/billing-" + runtime.GOOS + "-" + runtime.GOARCH
	}

	// The lock a release of v1.0.0 writes: the publisher's binaries.json, through
	// the same code `pin bump` runs. The https URL is the real shape; the host
	// below is handed the test server's.
	index := func(rel release) string {
		return fmt.Sprintf(`{"repo":"hanzoai/billing","tag":%q,"binaries":[{"name":"billing","os":%q,"arch":%q,"url":%q,"sha256":%q}]}`,
			rel.version, runtime.GOOS, runtime.GOARCH, "https://s3.hanzo.ai/plugins/hanzoai/billing/"+rel.version+"/billing-"+runtime.GOOS+"-"+runtime.GOARCH, rel.sum)
	}
	entries, err := pin.FromIndex(strings.NewReader(index(v1)))
	if err != nil {
		t.Fatal(err)
	}
	locked, err := pin.File{}.With(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := manifest.App{Name: app}.Locked(locked)
	if !ok {
		t.Fatal("the lock does not resolve billing for this platform")
	}
	if spec.Sum != v1.sum || !strings.HasPrefix(spec.URL, "https://s3.hanzo.ai/plugins/hanzoai/billing/v1.0.0/") {
		t.Fatalf("resolved %+v", spec)
	}
	spec.URL = v1.url // the store, in place of s3.hanzo.ai
	// A short directory: a unix socket path is bounded at 103 bytes, and t.TempDir
	// on macOS is longer than that.
	dir, err := os.MkdirTemp("/tmp", "zp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	spec.Dir = dir

	_, do := mountPlugin(t, spec)
	says := func(want string) {
		t.Helper()
		resp, body := do("GET", "/v1/"+app+"/scratch", nil, nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
			t.Fatalf("GET scratch = %d %s, want %s", resp.StatusCode, body, want)
		}
	}
	reload := func(rel release, sum string) (*http.Response, []byte) {
		return do("POST", "/v1/admin/plugins/"+app+"/reload", superAdmin, map[string]string{"url": rel.url, "sum": sum, "scope": scopeHost})
	}

	// Loaded by digest: the first request starts the locked release.
	says("v1.0.0")
	if fetches.Load() != 1 {
		t.Fatalf("fetched %d times, want once", fetches.Load())
	}

	// Swapped under load: not one request fails while the replacement comes up.
	var bad, ran atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			resp, _ := do("GET", "/v1/"+app+"/scratch", nil, nil)
			ran.Add(1)
			if resp.StatusCode != http.StatusOK {
				bad.Add(1)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	resp, body := reload(v2, v2.sum)
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("reload to v1.0.1 = %d %s", resp.StatusCode, body)
	}
	if bad.Load() != 0 || ran.Load() < 5 {
		t.Fatalf("%d of %d requests failed across the swap", bad.Load(), ran.Load())
	}
	says("v1.0.1")

	// A digest that is not the bytes is refused, and the release serving keeps serving.
	resp, body = reload(v2, strings.Repeat("0", 64))
	if strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("a wrong digest was accepted: %d %s", resp.StatusCode, body)
	}
	says("v1.0.1")

	// Rollback is naming the previous digest: the store is gone and it still works,
	// because the digest is the cache key.
	store.Close()
	before := fetches.Load()
	resp, body = reload(v1, v1.sum)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("rollback to v1.0.0 = %d %s", resp.StatusCode, body)
	}
	says("v1.0.0")
	if fetches.Load() != before {
		t.Fatal("the rollback touched the network")
	}

	// And the lock pins nothing but what it was given: bumping to v1.0.1 replaces
	// the stanza whole, which is what a rollback commit reverts.
	next, err := pin.FromIndex(strings.NewReader(index(v2)))
	if err != nil {
		t.Fatal(err)
	}
	bumped, err := locked.With(next[0])
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := bumped.Find("billing"); e.Tag != "v1.0.1" || len(bumped.Entries) != 1 {
		t.Fatalf("bumped lock: %+v", bumped.Entries)
	}
}
