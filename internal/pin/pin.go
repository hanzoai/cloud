// Copyright 2026 Hanzo AI, Inc. All rights reserved.

// Package pin reads plugins.lock: the host's pin of every plugin that is built
// and released on its own. One stanza per plugin, one line per platform:
//
//	dns  hanzoai/dns  v1.2.3
//	  linux/amd64  https://s3.hanzo.ai/plugins/hanzoai/dns/v1.2.3/dns-linux-amd64  sha256:<64 hex>
//	  linux/arm64  https://s3.hanzo.ai/plugins/hanzoai/dns/v1.2.3/dns-linux-arm64  sha256:<64 hex>
//
// A plugin is pinned to a semver tag and to the digest of each binary; a line
// without a digest, a floating tag or a second stanza for one name is a parse
// error, so nothing the host fetches is ever unverified. The file is compiled
// into the host: a bump is a commit, and review and rollback are a diff.
package pin

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed plugins.lock
var embedded string

var (
	nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	tagRE  = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	sumRE  = regexp.MustCompile(`^sha256:([0-9a-f]{64})$`)
	platRE = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)
)

// Binary is one platform's artifact: where it is, and the hex SHA-256 that
// authorizes running it.
type Binary struct {
	URL string
	Sum string
}

// Entry is one plugin pinned at one release.
type Entry struct {
	Name     string
	Repo     string
	Tag      string
	Binaries map[string]Binary // keyed "os/arch"
}

// File is a parsed plugins.lock.
type File struct {
	Entries []Entry
}

// Find returns the entry pinned for name.
func (f File) Find(name string) (Entry, bool) {
	for _, e := range f.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Repos reports whether repo (owner/name) is a plugin repository this file pins.
func (f File) Repos(repo string) bool {
	for _, e := range f.Entries {
		if strings.EqualFold(e.Repo, repo) {
			return true
		}
	}
	return false
}

// Parse reads a lock. It is strict: every stanza needs a semver tag and at least
// one platform, every platform needs an https URL and a sha256 digest.
func Parse(r io.Reader) (File, error) {
	var f File
	seen := map[string]bool{}
	var cur *Entry
	finish := func(line int) error {
		if cur == nil {
			return nil
		}
		if len(cur.Binaries) == 0 {
			return fmt.Errorf("plugins.lock:%d: %s has no platform", line, cur.Name)
		}
		f.Entries = append(f.Entries, *cur)
		cur = nil
		return nil
	}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		raw := sc.Text()
		if i := strings.IndexByte(raw, '#'); i >= 0 {
			raw = raw[:i]
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		fields := strings.Fields(raw)
		if raw[0] != ' ' && raw[0] != '\t' {
			if err := finish(n); err != nil {
				return File{}, err
			}
			if len(fields) != 3 {
				return File{}, fmt.Errorf("plugins.lock:%d: want `name owner/repo vX.Y.Z`", n)
			}
			name, repo, tag := fields[0], fields[1], fields[2]
			if !nameRE.MatchString(name) {
				return File{}, fmt.Errorf("plugins.lock:%d: bad plugin name %q", n, name)
			}
			if !repoRE.MatchString(repo) {
				return File{}, fmt.Errorf("plugins.lock:%d: bad repo %q", n, repo)
			}
			if !tagRE.MatchString(tag) {
				return File{}, fmt.Errorf("plugins.lock:%d: %s is pinned to %q, which is not a vX.Y.Z tag", n, name, tag)
			}
			if seen[name] {
				return File{}, fmt.Errorf("plugins.lock:%d: %s appears twice", n, name)
			}
			seen[name] = true
			cur = &Entry{Name: name, Repo: repo, Tag: tag, Binaries: map[string]Binary{}}
			continue
		}
		if cur == nil {
			return File{}, fmt.Errorf("plugins.lock:%d: a platform line before any plugin", n)
		}
		if len(fields) != 3 {
			return File{}, fmt.Errorf("plugins.lock:%d: want `os/arch url sha256:<hex>`", n)
		}
		plat, url, digest := fields[0], fields[1], fields[2]
		if !platRE.MatchString(plat) {
			return File{}, fmt.Errorf("plugins.lock:%d: bad platform %q", n, plat)
		}
		if !strings.HasPrefix(url, "https://") {
			return File{}, fmt.Errorf("plugins.lock:%d: %s %s url must be https", n, cur.Name, plat)
		}
		m := sumRE.FindStringSubmatch(digest)
		if m == nil {
			return File{}, fmt.Errorf("plugins.lock:%d: %s %s has no sha256 digest; nothing without one is ever run", n, cur.Name, plat)
		}
		if _, dup := cur.Binaries[plat]; dup {
			return File{}, fmt.Errorf("plugins.lock:%d: %s lists %s twice", n, cur.Name, plat)
		}
		cur.Binaries[plat] = Binary{URL: url, Sum: m[1]}
	}
	if err := sc.Err(); err != nil {
		return File{}, err
	}
	if err := finish(n); err != nil {
		return File{}, err
	}
	return f, nil
}

var (
	curMu   sync.RWMutex
	curInit bool
	current File
	failure error
)

// Current is the lock compiled into this binary (or overridden by SetCurrent).
// An unparseable lock is an error and never a partial file: the host then runs
// only what its image carries.
func Current() (File, error) {
	curMu.RLock()
	if curInit {
		f, err := current, failure
		curMu.RUnlock()
		return f, err
	}
	curMu.RUnlock()

	curMu.Lock()
	defer curMu.Unlock()
	if !curInit {
		current, failure = Parse(strings.NewReader(embedded))
		curInit = true
	}
	return current, failure
}

// SetCurrent replaces the active plugins.lock parsed by Current.
func SetCurrent(raw string) error {
	f, err := Parse(strings.NewReader(raw))
	curMu.Lock()
	current, failure, curInit = f, err, true
	curMu.Unlock()
	return err
}

// header is the comment every canonical plugins.lock opens with.
const header = `# plugins.lock: every plugin released on its own, pinned to a semver tag and to
# the sha256 of each binary. A plugin repo's release workflow (hanzoai/ci
# plugin.yml) rewrites a stanza with ` + "`pin bump`" + ` when it tags; a human reviews it
# as any other commit.
#
#   name  owner/repo  vX.Y.Z
#     os/arch  https://...  sha256:<64 hex>
#
# A line without a digest does not parse, and a digest is the only thing that
# authorizes a fetched binary to run. Rollback is reverting the stanza.
`

// Format is the canonical text of f: the header, then each stanza sorted by name
// and each platform sorted. Parse(f.Format()) is f, and the committed lock is held
// to this form, so a bump is always a minimal diff.
func (f File) Format() string {
	var b strings.Builder
	b.WriteString(header)
	es := append([]Entry(nil), f.Entries...)
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
	for _, e := range es {
		fmt.Fprintf(&b, "\n%s %s %s\n", e.Name, e.Repo, e.Tag)
		plats := make([]string, 0, len(e.Binaries))
		for p := range e.Binaries {
			plats = append(plats, p)
		}
		sort.Strings(plats)
		for _, p := range plats {
			fmt.Fprintf(&b, "  %s %s sha256:%s\n", p, e.Binaries[p].URL, e.Binaries[p].Sum)
		}
	}
	return b.String()
}

// With returns f with e in place of any entry of the same name. Entries are
// checked by the parser, so an entry that would not parse is not added.
func (f File) With(e Entry) (File, error) {
	out := File{}
	for _, old := range f.Entries {
		if old.Name != e.Name {
			out.Entries = append(out.Entries, old)
		}
	}
	out.Entries = append(out.Entries, e)
	return Parse(strings.NewReader(out.Format()))
}

// FromIndex reads the binaries.json the artifact publisher writes
// (apps/platform artifact_publish.sh) and answers one entry per plugin name in
// it, pinned at the index's repo and tag. A file the recipe produced that is not
// a platform binary (os "any") is not a plugin and is skipped.
func FromIndex(r io.Reader) ([]Entry, error) {
	var idx struct {
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
	if err := json.NewDecoder(r).Decode(&idx); err != nil {
		return nil, fmt.Errorf("binaries.json: %w", err)
	}
	by := map[string]*Entry{}
	var order []string
	for _, b := range idx.Binaries {
		if b.OS == "any" || b.Arch == "any" {
			continue
		}
		e := by[b.Name]
		if e == nil {
			e = &Entry{Name: b.Name, Repo: idx.Repo, Tag: idx.Tag, Binaries: map[string]Binary{}}
			by[b.Name] = e
			order = append(order, b.Name)
		}
		e.Binaries[b.OS+"/"+b.Arch] = Binary{URL: b.URL, Sum: b.SHA256}
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("binaries.json: no platform binary")
	}
	var out []Entry
	for _, n := range order {
		// One validation path: what is written must parse.
		if _, err := (File{}).With(*by[n]); err != nil {
			return nil, fmt.Errorf("binaries.json: %s: %w", n, err)
		}
		out = append(out, *by[n])
	}
	return out, nil
}
