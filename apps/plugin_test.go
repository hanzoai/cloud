package apps

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// shellPluginRE is the pattern the Makefile and the Dockerfile use to learn which
// plugin binaries to build (there, paren-free — `PluginSpec."[a-z0-9-]+"` — because
// make counts parens inside $(shell ...); here it takes a capture group and so
// writes the '(' out). They cannot link this package to ask Wire(), so they read
// the same text, and this test is what stops the text and the compiled list from
// drifting apart. Keep the three in lockstep.
var shellPluginRE = regexp.MustCompile(`PluginSpec.("[a-z0-9-]+")`)

// The failure this guards is not a build error, it is a boot error: a plugin the
// image forgot to build makes zip.Load's fork/exec fail, MountAll returns, and
// cloud refuses to start. Nothing about compiling cloud notices.
func TestPluginBinaries(t *testing.T) {
	src, err := os.ReadFile("apps.go")
	if err != nil {
		t.Fatal(err)
	}
	var fromText []string
	for _, m := range shellPluginRE.FindAllStringSubmatch(string(src), -1) {
		fromText = append(fromText, strings.Trim(m[1], `"`)) // the shell's `cut -d'"' -f2`
	}
	compiled := cloud.PluginNames(Wire())

	if len(compiled) == 0 {
		t.Fatal("Wire() declares no plugin — if that is deliberate, delete this test with the last PluginSpec")
	}
	if len(fromText) != len(compiled) {
		t.Fatalf("the shell sees %v, Wire() says %v — Makefile/Dockerfile would build the wrong set", fromText, compiled)
	}
	for i, name := range compiled {
		if fromText[i] != name {
			t.Fatalf("plugin %d: shell sees %q, Wire() says %q", i, fromText[i], name)
		}
		// The entrypoint has to exist, or `go build ./cmd/<name>` fails in the
		// image build rather than here, where the reason is legible.
		dir := filepath.Join("..", "cmd", name)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			t.Fatalf("plugin %q has no cmd/%s — an extracted app with no binary cannot be mounted", name, name)
		}
	}
}
