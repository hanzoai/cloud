// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package manifest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"
)

// Plugins names a release index: hanzoai/ci's binaries.json, on S3 or a GitHub
// release. Set it and a host ships with no plugin binaries at all — every
// subsystem arrives over the network on first use.
const Plugins = "CLOUD_PLUGINS"

// release is ci's binaries.json verbatim, so url and the digest authorizing it
// come from one file and cannot disagree.
type release struct {
	Repo     string `json:"repo"`
	Tag      string `json:"tag"`
	Binaries []struct {
		Name   string `json:"name"`
		OS     string `json:"os"`
		Arch   string `json:"arch"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"binaries"`
}

var index struct {
	once sync.Once
	byID map[string]struct{ url, sum string }
	err  error
}

// key is name+os+arch: one index serves a mixed-arch fleet.
func key(name, goos, goarch string) string { return name + "/" + goos + "/" + goarch }

// fetch fetches the release index ONCE per process. 108 apps resolving
// through here must not become 108 requests, and the set of artifacts does not
// change under a running host — a new one is a new release, which is a new
// index at a new URL.
func fetch() (map[string]struct{ url, sum string }, error) {
	index.once.Do(func() {
		src := strings.TrimSpace(os.Getenv(Plugins))
		if src == "" {
			return
		}
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Get(src)
		if err != nil {
			index.err = fmt.Errorf("plugins index %s: %w", src, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			index.err = fmt.Errorf("plugins index %s: %s", src, resp.Status)
			return
		}
		var rel release
		if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
			index.err = fmt.Errorf("plugins index %s: %w", src, err)
			return
		}
		m := make(map[string]struct{ url, sum string }, len(rel.Binaries))
		for _, b := range rel.Binaries {
			// No digest, no trust — and failing here names the index.
			if b.SHA256 == "" || b.URL == "" {
				continue
			}
			m[key(b.Name, b.OS, b.Arch)] = struct{ url, sum string }{b.URL, b.SHA256}
		}
		index.byID = m
	})
	return index.byID, index.err
}

// remote resolves this app to a release artifact for the running platform.
// False on no index, unreadable index, or nothing for this os/arch; the caller
// then falls through to the on-disk failure. No index is not an error — the
// default deployment ships its binaries beside the host.
func (a App) remote() (zip.Plugin, bool) {
	byID, err := fetch()
	if err != nil || byID == nil {
		return zip.Plugin{}, false
	}
	// Same ladder as on disk: a dedicated artifact wins, else the multi-call one
	// serving this app. One published binary answers all 108 — 196MB instead of
	// 4.4GB, because the ~36MB core every plugin links is shipped once.
	at, ok := byID[key(a.Name, runtime.GOOS, runtime.GOARCH)]
	var args []string
	if !ok {
		if at, ok = byID[key(MultiCall, runtime.GOOS, runtime.GOARCH)]; !ok {
			return zip.Plugin{}, false
		}
		args = []string{"--enable=" + a.Name}
	}
	// Sum is what makes fetching code safe to execute: zip verifies before
	// chmod, and caches by digest, so restart and rollback touch no network.
	return zip.Plugin{
		Name: a.Name,
		URL:  at.url,
		Sum:  at.sum,
		Args: args,
		Lazy: !a.Eager,
	}, true
}
