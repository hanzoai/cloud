// Copyright © 2026 Hanzo AI. MIT License.

package kms

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
)

const policyDoc = `
grants:
- holder: hanzo-pkg
  org: hanzo
  from: charts
  read:  [{path: /pkg, env: prod, keys: [htpasswd]}]
- holder: hanzo-ci
  org: hanzo
  why: CI reads the storage bucket's keys
  read:  [{path: /storage, env: prod, keys: ["*"]}]
  write: [{path: /deploy, env: prod, keys: [GITHUB_TOKEN]}]
- holder: hanzo/z
  org: hanzo
  why: a person, named by home org and username
  read:  [{path: /, env: default, keys: [ROOT]}]
`

// Every malformed document is refused WHOLE: a typo must never silently drop a
// grant, and an entry the rule cannot read must never widen one.
func TestParsePolicyRefusesEveryMalformedDocument(t *testing.T) {
	for _, tc := range []struct{ what, doc, want string }{
		{"empty", ``, "empty document"},
		{"enforce true", "enforce: true\ngrants: []\n", "delete the line to enforce"},
		{"unknown top-level field", "grant: []\n", "not found"},
		{"unknown grant field", "grants: [{holder: a, org: hanzo, reads: []}]\n", "not found"},
		{"unknown coord field", "grants: [{holder: a, org: hanzo, read: [{path: /, env: prod, keys: [K], key: K}]}]\n", "not found"},
		{"empty holder", "grants: [{holder: '', org: hanzo}]\n", "holder"},
		{"holder with a space", "grants: [{holder: 'hanzo pkg', org: hanzo}]\n", "holder"},
		{"empty org", "grants: [{holder: a, org: ''}]\n", "storage label"},
		{"org Sanitize would change", "grants: [{holder: a, org: Hanzo}]\n", "storage label"},
		{"org with a traversal", "grants: [{holder: a, org: '../hanzo'}]\n", "storage label"},
		{"empty path", "grants: [{holder: a, org: hanzo, read: [{path: '', env: prod, keys: [K]}]}]\n", "path"},
		{"traversal path", "grants: [{holder: a, org: hanzo, read: [{path: /a/../b, env: prod, keys: [K]}]}]\n", "path"},
		{"empty env", "grants: [{holder: a, org: hanzo, read: [{path: /, keys: [K]}]}]\n", "env"},
		{"wildcard env", "grants: [{holder: a, org: hanzo, read: [{path: /, env: '*', keys: [K]}]}]\n", "env"},
		{"env with a slash", "grants: [{holder: a, org: hanzo, read: [{path: /, env: a/b, keys: [K]}]}]\n", "env"},
		{"no keys", "grants: [{holder: a, org: hanzo, write: [{path: /, env: prod, keys: []}]}]\n", "keys is empty"},
		{"key with a slash", "grants: [{holder: a, org: hanzo, read: [{path: /, env: prod, keys: [a/b]}]}]\n", "not a secret name"},
		{"two documents", "grants: []\n---\ngrants: []\n", "one YAML document"},
		// The shapes a looser YAML reader accepts. universe's gate
		// (scripts/ci/kms_grants.py) refuses each of these too; they are the
		// conformance set the two parsers are held to.
		{"keys as a scalar", "grants: [{holder: a, org: hanzo, read: [{path: /, env: prod, keys: '*'}]}]\n", "cannot unmarshal"},
		{"a repeated field", "grants:\n- holder: a\n  org: hanzo\n  read: [{path: /, env: prod, keys: [K]}]\n  read: [{path: /, env: prod, keys: [K]}]\n", "already defined"},
		{"a top-level note", "note: measuring\ngrants: []\n", "not found"},
		{"enforce as a string", "enforce: 'false'\ngrants: []\n", "cannot unmarshal"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			p, err := ParsePolicy([]byte(tc.doc))
			if err == nil {
				t.Fatalf("accepted: %+v", p)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refused for the wrong reason: %v (want %q)", err, tc.want)
			}
		})
	}
}

// Allow is exact on every axis: holder, org, verb, path, key, env. "*" is any
// key at ONE path, never a path beneath it.
func TestAllowIsExactOnEveryAxis(t *testing.T) {
	p, err := ParsePolicy([]byte(policyDoc))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enforcing() {
		t.Fatal("a document without the enforce line enforces")
	}
	for _, tc := range []struct {
		what            string
		holder, org     string
		v               Verb
		path, name, env string
		want            bool
	}{
		{"the exact key", "hanzo-pkg", "hanzo", Read, "/pkg", "htpasswd", "prod", true},
		{"the same path spelled bare", "hanzo-pkg", "hanzo", Read, "pkg", "htpasswd", "prod", true},
		{"the same path spelled with slashes", "hanzo-pkg", "hanzo", Read, "/pkg/", "htpasswd", "prod", true},
		{"another key at the path", "hanzo-pkg", "hanzo", Read, "/pkg", "OTHER", "prod", false},
		{"another env", "hanzo-pkg", "hanzo", Read, "/pkg", "htpasswd", "default", false},
		{"another org", "hanzo-pkg", "lux", Read, "/pkg", "htpasswd", "prod", false},
		{"the other verb", "hanzo-pkg", "hanzo", Write, "/pkg", "htpasswd", "prod", false},
		{"a path beneath", "hanzo-pkg", "hanzo", Read, "/pkg/sub", "htpasswd", "prod", false},
		{"the parent path", "hanzo-pkg", "hanzo", Read, "/", "htpasswd", "prod", false},
		{"another holder", "hanzo-visor", "hanzo", Read, "/pkg", "htpasswd", "prod", false},
		{"an empty holder", "", "hanzo", Read, "/pkg", "htpasswd", "prod", false},
		{"a wildcard key", "hanzo-ci", "hanzo", Read, "/storage", "ANY_KEY", "prod", true},
		{"a wildcard is one path deep", "hanzo-ci", "hanzo", Read, "/storage/sub", "ANY_KEY", "prod", false},
		{"a wildcard is one verb", "hanzo-ci", "hanzo", Write, "/storage", "ANY_KEY", "prod", false},
		{"a write grant", "hanzo-ci", "hanzo", Write, "/deploy", "GITHUB_TOKEN", "prod", true},
		{"a write grant does not read", "hanzo-ci", "hanzo", Read, "/deploy", "GITHUB_TOKEN", "prod", false},
		{"a person at the org root", "hanzo/z", "hanzo", Read, "", "ROOT", "default", true},
		{"a person is its home and name", "lux/z", "hanzo", Read, "", "ROOT", "default", false},
	} {
		if got := p.Allow(tc.holder, tc.org, tc.v, tc.path, tc.name, tc.env); got != tc.want {
			t.Errorf("%s: Allow = %v, want %v", tc.what, got, tc.want)
		}
	}

	var none *Policy
	if none.Allow("hanzo-pkg", "hanzo", Read, "/pkg", "htpasswd", "prod") || none.Enforcing() {
		t.Error("no document grants nothing and measures: enforcement is a document that says so")
	}
	soft, err := ParsePolicy([]byte("enforce: false\n" + policyDoc))
	if err != nil {
		t.Fatal(err)
	}
	if soft.Enforcing() {
		t.Error("`enforce: false` stands down")
	}
	if !soft.Allow("hanzo-pkg", "hanzo", Read, "/pkg", "htpasswd", "prod") {
		t.Error("`enforce: false` still reports what a grant covers")
	}
}

// memKeeper is the sealed store's two calls, in memory.
type memKeeper map[string][]byte

func (m memKeeper) Get(path, name, env string) ([]byte, error) {
	if b, ok := m[path+"/"+name+"@"+env]; ok {
		return b, nil
	}
	return nil, ErrSecretNotFound
}

func (m memKeeper) Put(path, name, env string, v []byte) error {
	m[path+"/"+name+"@"+env] = append([]byte(nil), v...)
	return nil
}

func writeDoc(t *testing.T, f, doc string) {
	t.Helper()
	if err := os.WriteFile(f, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A document swaps in only when it changed AND parses; a bad edit keeps the
// last good one, and a vanished file keeps it too. The mount's state is what
// the probe reports beside it.
func TestGrantsKeepTheLastGoodDeclaration(t *testing.T) {
	f := filepath.Join(t.TempDir(), "grants.yaml")
	log := luxlog.NewNoOpLogger()

	g := newGrants(f, memKeeper{}, log)
	if live, enforcing, refused := g.status(); live != "" || enforcing || refused != markAbsent {
		t.Fatalf("no file: live=%q enforcing=%v refused=%q", live, enforcing, refused)
	}

	writeDoc(t, f, policyDoc)
	g.reload()
	good := g.policy()
	if good == nil || !good.Allow("hanzo-pkg", "hanzo", Read, "/pkg", "htpasswd", "prod") {
		t.Fatalf("a good document did not load: %+v", good)
	}
	if live, enforcing, refused := g.status(); live != good.Digest || !enforcing || refused != "" {
		t.Fatalf("status after a good load: live=%q enforcing=%v refused=%q", live, enforcing, refused)
	}

	writeDoc(t, f, "grants: [{holder: '', org: hanzo}]\n")
	g.reload()
	if g.policy() != good {
		t.Fatal("a refused document replaced the last good one")
	}
	if _, _, refused := g.status(); refused != digest([]byte("grants: [{holder: '', org: hanzo}]\n")) {
		t.Fatalf("the probe does not name the refused document: %q", refused)
	}

	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	g.reload()
	if g.policy() != good {
		t.Fatal("a vanished document dropped the last good one")
	}

	writeDoc(t, f, "enforce: false\n"+policyDoc)
	g.reload()
	if g.policy() == good || g.policy().Enforcing() {
		t.Fatal("a changed good document did not replace the last one")
	}
}

// A RESTART onto an unusable mount decides exactly as the process before it
// did: the last good declaration is kept in the sealed store and loaded when
// the mount is refused or missing. One typo merged in universe therefore
// neither switches enforcement on (a measuring pod booting into a deny-all) nor
// off (an enforcing pod booting back into the old rule).
func TestARestartOntoARefusedMountKeepsTheLastGoodDeclaration(t *testing.T) {
	log := luxlog.NewNoOpLogger()
	typo := "enforce: false\nnote: measuring since release N\n" + policyDoc

	for _, tc := range []struct {
		what      string
		good      string
		enforcing bool
	}{
		{"a measuring declaration", "enforce: false\n" + policyDoc, false},
		{"an enforcing declaration", policyDoc, true},
	} {
		t.Run(tc.what, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "grants.yaml")
			kept := memKeeper{}
			writeDoc(t, f, tc.good)
			before := newGrants(f, kept, log).policy()
			if before == nil || before.Enforcing() != tc.enforcing {
				t.Fatalf("the good declaration did not load: %+v", before)
			}

			for _, bad := range []string{typo, ""} {
				if bad == "" {
					_ = os.Remove(f)
				} else {
					writeDoc(t, f, bad)
				}
				after := newGrants(f, kept, log)
				p := after.policy()
				if p == nil || p.Digest != before.Digest || p.Enforcing() != tc.enforcing {
					t.Fatalf("a restart onto %q decided differently: %+v", cmpOr(bad, "no mount"), p)
				}
				if !p.Allow("hanzo-pkg", "hanzo", Read, "/pkg", "htpasswd", "prod") {
					t.Fatal("the kept declaration lost a grant")
				}
				if _, _, refused := after.status(); refused == "" {
					t.Fatal("the probe does not say the mount is not what decides")
				}
			}
		})
	}

	// With nothing kept, an unusable mount MEASURES: this release changes no
	// verdict until a declaration it can read says to.
	f := filepath.Join(t.TempDir(), "grants.yaml")
	writeDoc(t, f, typo)
	if p := newGrants(f, memKeeper{}, log).policy(); p != nil || p.Enforcing() {
		t.Fatalf("a refused mount with nothing kept must measure, got %+v", p)
	}
	// A kept copy that does not parse is no copy.
	kept := memKeeper{keptPath + "/" + keptName + "@" + keptEnv: []byte("grants: [{holder: ''}]")}
	if p := newGrants(f, kept, log).policy(); p != nil {
		t.Fatalf("an unparseable kept copy was loaded: %+v", p)
	}
}

// The mount is re-read on the first use after policyPoll — no goroutine, so
// nothing outlives the process that asked.
func TestGrantsReReadTheMountWhenDue(t *testing.T) {
	f := filepath.Join(t.TempDir(), "grants.yaml")
	g := newGrants(f, nil, luxlog.NewNoOpLogger())
	writeDoc(t, f, policyDoc)
	if g.policy() != nil {
		t.Fatal("re-read before it was due")
	}
	g.due.Store(0)
	if g.policy() == nil {
		t.Fatal("not re-read once due")
	}
}

func cmpOr(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// Readers never wait on a re-read and never see a torn one: one caller re-reads
// while the rest decide on the live declaration. Run under -race.
func TestGrantsReReadUnderConcurrentReaders(t *testing.T) {
	f := filepath.Join(t.TempDir(), "grants.yaml")
	writeDoc(t, f, policyDoc)
	g := newGrants(f, memKeeper{}, luxlog.NewNoOpLogger())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			if i%2 == 0 {
				writeDoc(t, f, "enforce: false\n"+policyDoc)
			} else {
				writeDoc(t, f, policyDoc)
			}
			g.due.Store(0)
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
		}
		if p := g.policy(); p == nil || !p.Allow("hanzo-pkg", "hanzo", Read, "/pkg", "htpasswd", "prod") {
			t.Fatalf("a reader saw no declaration, or one missing its grant: %+v", p)
		}
		_, _, _ = g.status()
	}
}
