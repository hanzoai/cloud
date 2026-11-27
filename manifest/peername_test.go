package manifest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A PEER IS NAMED ONCE, and this is the test that makes that true.
//
// The connectors app was renamed integration -> provider: the row here moved,
// the routes moved to /v1/provider, the document regenerated. Four callers went
// on asking the router for "integration" as a bare string, and the router
// answered what it honestly could — `app is not deployed here`. Slack messages
// arrived, answered 200, and produced nothing at all. The one in
// apps/transport/turn.go was the worst of them: a turn that cannot resolve who
// asked never reaches the model, so the failure was SILENCE rather than an
// error anybody would see.
//
// A literal in four files cannot be renamed once. A constant can, so the rule
// is that the name of an app in this manifest never appears as a quoted string
// in a call that resolves a peer.
func TestNoAppIsAddressedByALiteral(t *testing.T) {
	// The ops that take a peer name as their first argument.
	call := regexp.MustCompile(`(?:client\.Call\[[^\]]*\]|identify|ask)\(\s*\w+\s*,\s*"([a-z][a-z0-9]*)"`)

	root := ".."
	var bad []string
	for _, dir := range []string{"apps", "clients"} {
		_ = filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, m := range call.FindAllStringSubmatch(string(src), -1) {
				for _, a := range Apps {
					if a.Name == m[1] {
						bad = append(bad, path+`: reaches "`+m[1]+`" by literal`)
					}
				}
			}
			return nil
		})
	}
	for _, b := range bad {
		t.Errorf("%s — use the constant beside its manifest row, so a rename reaches every caller", b)
	}
}
