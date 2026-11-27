// Copyright © 2026 Hanzo AI. MIT License.

package kms

// policy.go is the grant rule for secret VALUES.
//
// A caller may open, write or delete a value only if it is SuperAdmin holding an
// access token (and the act is audited), or a grant declared in git covers the
// exact (holder, org, verb, path, key, env). Everything else is denied. Org
// admin confers nothing over values: administering an org is self-service, and
// reading its API keys is not part of that.
//
// Listing names and metadata is narrower than membership. A SuperAdmin
// (audited) sees every name, and so does an admin of the org outside the held
// orgs; any other caller sees only the names its own grants open, and is refused
// where none of them reaches. A member learns nothing about what an org holds
// from membership alone.
//
// The grants are one YAML document, declared in universe
// (charts/app/values/hanzo/kms-grants.yaml → ConfigMap kms-grants) and mounted
// read-only at GrantsFile. The file is the whole policy: there is no API that
// writes a grant, so a grant exists exactly when a reviewed commit says so.
//
// A document that cannot be read or parsed changes NOTHING — not while the
// process runs and not across a restart. The last good one is kept sealed in
// this process's own store (outside every org's subtree) and is what a restart
// loads when the mount is absent or refused, so one typo merged in universe can
// neither widen access nor switch enforcement on or off at the next reboot.
//
//	enforce: false        # rollout only; the one allowed value. Delete the line to enforce.
//	grants:
//	- holder: hanzo-pkg   # a clientId, or "<home org>/<username>" for a person
//	  org: hanzo
//	  read:  [{path: /pkg, env: prod, keys: [htpasswd]}]   # keys: ["*"] = any key at that exact path
//	  write: []           # write covers put and delete

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"gopkg.in/yaml.v3"
)

// GrantsFile is where the deployment mounts the grant document. It is not a
// setting — no flag, env var or config field reaches it, because a knob could
// only make the process read a file nobody reviewed. It is a variable so an
// in-process harness can mount a declaration of its own before Use; nothing in
// the binary assigns it, and code linked into this process can already open
// the store directly, so repointing it grants nothing new.
var GrantsFile = "/etc/cloud/kms/grants.yaml"

// policyPoll is how often the mounted document is re-read. A ConfigMap mount
// updates in place on the kubelet's resync, so a merged grant takes effect
// within a resync plus one poll, with no release.
const policyPoll = 30 * time.Second

// The coordinate the last good document is kept at in this process's sealed
// store. It is not under /orgs, so no org's REST surface can name it, and it is
// sealed like every value, so a copy nobody's master key opened is no copy.
const (
	keptPath = "/kms"
	keptName = "grants"
	keptEnv  = "kept"
)

// Verb is what a grant permits. Write covers put and delete: removing a value
// is as consequential as replacing it.
type Verb string

const (
	Read  Verb = "read"
	Write Verb = "write"
)

// Coord is one declared coordinate: a path beneath the org root, an
// environment, and the keys at that exact path. "*" is any key at the path —
// never any path beneath it.
type Coord struct {
	Path string   `yaml:"path"`
	Env  string   `yaml:"env"`
	Keys []string `yaml:"keys"`
}

// grant is one holder's access within one org. From and Why are provenance
// for the reviewer and the CI check (universe scripts/ci/kms_grants.py); the
// rule does not read them.
type grant struct {
	Holder string  `yaml:"holder"`
	Org    string  `yaml:"org"`
	From   string  `yaml:"from"`
	Why    string  `yaml:"why"`
	Read   []Coord `yaml:"read"`
	Write  []Coord `yaml:"write"`
}

type document struct {
	Enforce *bool   `yaml:"enforce"`
	Grants  []grant `yaml:"grants"`
}

type slot struct {
	holder, org, path, env string
	verb                   Verb
}

// Policy is a parsed grant document. A nil *Policy is a valid policy: it grants
// nothing and, in this release, MEASURES — no document has ever loaded, so the
// rule each caller had before still decides and every value a grant would not
// cover is logged as would-deny. Enforcement is a document without the
// `enforce: false` line, never the absence of a document.
type Policy struct {
	enforce bool
	// Digest is the sha256 of the document's bytes, reported by the health
	// probe so a reader can prove which declaration is live.
	Digest string
	names  map[slot]map[string]bool
}

// ParsePolicy reads one grant document. It refuses the whole document on any
// malformed entry, so a typo can never silently drop or widen a grant: an
// unknown field, an empty holder or org, an org that is not already a storage
// label, a path that is not a clean subpath, an empty or wildcard env, a bad
// key name, and `enforce: true` — enforcement is the ABSENCE of the line, so
// there is one way to say it.
func ParsePolicy(b []byte) (*Policy, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var doc document
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("kms grants: empty document")
		}
		return nil, fmt.Errorf("kms grants: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("kms grants: exactly one YAML document")
	}
	if doc.Enforce != nil && *doc.Enforce {
		return nil, errors.New("kms grants: `enforce: true` is not a value — delete the line to enforce")
	}
	p := &Policy{enforce: doc.Enforce == nil, Digest: digest(b), names: map[slot]map[string]bool{}}
	for i, g := range doc.Grants {
		if !validHolder(g.Holder) {
			return nil, fmt.Errorf("kms grants[%d]: holder %q must be a non-empty name without spaces or control characters", i, g.Holder)
		}
		if g.Org == "" || namespace.Sanitize(g.Org) != g.Org {
			return nil, fmt.Errorf("kms grants[%d] (%s): org %q is not a storage label", i, g.Holder, g.Org)
		}
		for _, set := range []struct {
			verb   Verb
			coords []Coord
		}{{Read, g.Read}, {Write, g.Write}} {
			for j, c := range set.coords {
				where := fmt.Sprintf("kms grants[%d] (%s) %s[%d]", i, g.Holder, set.verb, j)
				if strings.TrimSpace(c.Path) == "" || !ValidSubpath(c.Path) {
					return nil, fmt.Errorf("%s: path %q must be a clean '/'-separated subpath (\"/\" for the org root)", where, c.Path)
				}
				if c.Env == "" || c.Env == "*" || !validEnv(c.Env) {
					return nil, fmt.Errorf("%s: env %q must name one environment", where, c.Env)
				}
				if len(c.Keys) == 0 {
					return nil, fmt.Errorf("%s: keys is empty — list the keys, or \"*\" for every key at %s", where, c.Path)
				}
				k := slot{g.Holder, g.Org, normalizePath(c.Path), c.Env, set.verb}
				if p.names[k] == nil {
					p.names[k] = map[string]bool{}
				}
				for _, name := range c.Keys {
					if name != "*" && !validName(name) {
						return nil, fmt.Errorf("%s: key %q is not a secret name", where, name)
					}
					p.names[k][name] = true
				}
			}
		}
	}
	return p, nil
}

// Allow reports whether a grant covers holder acting in org on one value. A
// nil policy and an empty holder allow nothing. Paths compare through
// normalizePath (kms.go) — the same trimming OrgPath applies before it keys
// the store — so a grant and the record it covers agree on every spelling of
// one address.
func (p *Policy) Allow(holder, org string, v Verb, path, name, env string) bool {
	if p == nil || holder == "" {
		return false
	}
	n := p.names[slot{holder, org, normalizePath(path), env, v}]
	return n[name] || n["*"]
}

// Opens reports whether a grant lets holder read or write one value: what a
// listing shows a caller who is neither SuperAdmin nor an admin of the org.
func (p *Policy) Opens(holder, org, path, name, env string) bool {
	return p.Allow(holder, org, Read, path, name, env) || p.Allow(holder, org, Write, path, name, env)
}

// Reaches reports whether any grant of holder in org names a coordinate inside
// the subtree at sub, in env when env is named: whether a listing there has
// anything to show that holder. A nil policy and an empty holder reach nothing.
func (p *Policy) Reaches(holder, org, sub, env string) bool {
	if p == nil || holder == "" {
		return false
	}
	root := normalizePath(sub)
	for s := range p.names {
		if s.holder == holder && s.org == org && (env == "" || s.env == env) &&
			(root == "/" || s.path == root || strings.HasPrefix(s.path, root+"/")) {
			return true
		}
	}
	return false
}

// Enforcing reports whether a denial is served: a loaded document without the
// `enforce: false` line. No document measures — this release changes no verdict
// until a declaration says to, so a pod that booted before its mount existed,
// or one that has never read a good document, keeps the rule it had.
func (p *Policy) Enforcing() bool { return p != nil && p.enforce }

// digestOf is the loaded document's digest, or "" when none is loaded.
func (p *Policy) digestOf() string {
	if p == nil {
		return ""
	}
	return p.Digest
}

// keeper is where the last good declaration outlives the process: the embedded
// store (*Client), which seals it under the master key like any value.
type keeper interface {
	Get(path, name, env string) ([]byte, error)
	Put(path, name, env string, value []byte) error
}

// grants is the live declaration, where it came from, and what the mount last
// said. It re-reads the mount on the first use after policyPoll has passed —
// on the request or health probe that asks — so there is no goroutine whose
// life has to be tied to anything.
type grants struct {
	file string
	kept keeper
	log  luxlog.Logger

	live atomic.Pointer[Policy]
	// mounted is what the mount last read as: the document's digest, or
	// markAbsent / markUnreadable.
	mounted atomic.Pointer[string]
	due     atomic.Int64 // unix nanos of the next re-read
	mu      sync.Mutex   // one re-read at a time; the others use the live one
}

const (
	markAbsent     = "absent"
	markUnreadable = "unreadable"
)

// newGrants reads the mount once, before any value route exists, so no
// verdict is ever reached against a declaration that has not been read.
func newGrants(file string, kept keeper, log luxlog.Logger) *grants {
	g := &grants{file: file, kept: kept, log: log}
	g.reload()
	return g
}

// policy is the declaration in force, re-reading the mount when it is due.
func (g *grants) policy() *Policy {
	if g == nil {
		return nil
	}
	if time.Now().UnixNano() >= g.due.Load() && g.mu.TryLock() {
		g.reload()
		g.mu.Unlock()
	}
	return g.live.Load()
}

// status is what the health probe reports: the digest in force, whether it
// enforces, and — when the mount is not what is in force — what the mount says
// instead, so a refused document is visible the moment it lands rather than at
// the next restart.
func (g *grants) status() (live string, enforcing bool, refused string) {
	if g == nil {
		return "", false, ""
	}
	p := g.policy()
	live, enforcing = p.digestOf(), p.Enforcing()
	if m := g.mounted.Load(); m != nil && *m != live {
		refused = *m
	}
	return live, enforcing, refused
}

// reload reads the mount once. A document that changed AND parses becomes the
// live one and is kept; anything else keeps what is live. With nothing live —
// a boot — a refused or missing mount falls back to the kept document, so a
// restart decides exactly as the process before it did.
func (g *grants) reload() {
	g.due.Store(time.Now().Add(policyPoll).UnixNano())
	mark, b, p, err := g.read()
	prev := g.mounted.Swap(&mark)
	changed := prev == nil || *prev != mark
	switch {
	case p != nil && p.Digest != g.live.Load().digestOf():
		g.live.Store(p)
		g.keep(b, mark)
		g.log.Info("kms grants: declaration loaded", "file", g.file, "digest", p.Digest, "slots", len(p.names), "enforce", p.enforce)
	case p != nil:
	case g.live.Load() == nil:
		if kp := g.fromKept(); kp != nil {
			g.live.Store(kp)
			g.log.Error("kms grants: the mounted declaration is not usable; the one kept from the last good load decides",
				"file", g.file, "mounted", mark, "kept", kp.Digest, "enforce", kp.enforce, "err", err)
		} else if changed {
			g.log.Warn("kms grants: no usable declaration and none kept; the rule each caller had before decides",
				"file", g.file, "mounted", mark, "err", err)
		}
	case changed:
		g.log.Error("kms grants: the mounted declaration is not usable; keeping the live one",
			"file", g.file, "mounted", mark, "live", g.live.Load().digestOf(), "err", err)
	}
}

// read returns what the mount holds: its mark, its bytes, and the parsed
// document when it parses.
func (g *grants) read() (string, []byte, *Policy, error) {
	b, err := os.ReadFile(g.file)
	if errors.Is(err, fs.ErrNotExist) {
		return markAbsent, nil, nil, err
	}
	if err != nil {
		return markUnreadable, nil, nil, err
	}
	p, err := ParsePolicy(b)
	return digest(b), b, p, err
}

// keep seals the document that just became live, so a restart that finds the
// mount unusable decides as this process did. A store that cannot take it (no
// master key, a read-only replica) only loses that guarantee, and says so.
func (g *grants) keep(b []byte, mark string) {
	if g.kept == nil {
		return
	}
	if err := g.kept.Put(keptPath, keptName, keptEnv, b); err != nil {
		g.log.Warn("kms grants: the live declaration could not be kept for a restart", "digest", mark, "err", err)
	}
}

// fromKept is the kept document, or nil when there is none that parses.
func (g *grants) fromKept() *Policy {
	if g.kept == nil {
		return nil
	}
	b, err := g.kept.Get(keptPath, keptName, keptEnv)
	if err != nil {
		return nil
	}
	p, err := ParsePolicy(b)
	if err != nil {
		g.log.Error("kms grants: the kept declaration does not parse", "err", err)
		return nil
	}
	return p
}

func validHolder(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, r := range h {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
