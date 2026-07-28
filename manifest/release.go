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

// PluginsEnv names a release index — the binaries.json that hanzoai/ci already
// writes beside the artifacts it publishes. Point it at one (on S3, or at a
// GitHub release; the format is the same either way) and the host resolves any
// app it cannot find on disk from there.
//
// This is the rung that makes a host shippable with NO plugin binaries in its
// image: the image carries the ~19MB host, and every subsystem arrives over the
// network on first use.
const PluginsEnv = "CLOUD_PLUGINS"

// release is hanzoai/ci's binaries.json verbatim. It is not a second format
// invented for this: CI writes {name,os,arch,url,sha256} per artifact, and a
// host reads url+sum from that one place, so the bits and the digest that
// authorize them can never come from different releases.
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

// key is deliberately name+os+arch: one index serves a mixed-architecture fleet,
// and a host must never be handed the amd64 binary because it asked first.
func key(name, goos, goarch string) string { return name + "/" + goos + "/" + goarch }

// loadIndex fetches the release index ONCE per process. 108 apps resolving
// through here must not become 108 requests, and the set of artifacts does not
// change under a running host — a new one is a new release, which is a new
// index at a new URL.
func loadIndex() (map[string]struct{ url, sum string }, error) {
	index.once.Do(func() {
		src := strings.TrimSpace(os.Getenv(PluginsEnv))
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
			// An entry without a digest is dropped rather than trusted: zip
			// refuses URL without Sum anyway, and failing here names the index
			// instead of failing later naming the plugin.
			if b.SHA256 == "" || b.URL == "" {
				continue
			}
			m[key(b.Name, b.OS, b.Arch)] = struct{ url, sum string }{b.URL, b.SHA256}
		}
		index.byID = m
	})
	return index.byID, index.err
}

// fromRelease resolves this app to a release artifact for the running platform.
//
// It reports false when no index is configured, when the index could not be
// read, or when it lists nothing for this app on this os/arch — in every case
// the caller falls through to the on-disk failure, which names the path a
// developer expects to have built. A missing index is not an error: the default
// deployment still ships its binaries beside the host.
func (a App) fromRelease() (zip.Plugin, bool) {
	byID, err := loadIndex()
	if err != nil || byID == nil {
		return zip.Plugin{}, false
	}
	at, ok := byID[key(a.Name, runtime.GOOS, runtime.GOARCH)]
	if !ok {
		return zip.Plugin{}, false
	}
	// Sum is what makes this safe to do at all — zip verifies the download
	// against it before the file is ever made executable, and reuses a binary
	// already cached under that digest, so a restart costs no download and a
	// rollback to a previously run version is free and offline.
	return zip.Plugin{
		Name: a.Name,
		URL:  at.url,
		Sum:  at.sum,
		Lazy: !a.Eager,
	}, true
}
