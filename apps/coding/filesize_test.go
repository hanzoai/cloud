package coding

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const maxModuleFileLines = 1000

// TestModuleFilesUnder1000Lines enforces the 1,000-line ceiling across
// apps/coding, apps/dev (including apps/dev/loop), and plugin/agent so modules
// stay cohesive and never grow into multi-thousand-line monoliths. Generated
// *_gen.go projections are excluded.
func TestModuleFilesUnder1000Lines(t *testing.T) {
	for _, root := range []string{".", "../../apps/dev", "../../plugin/agent"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_gen.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read %s: %v", p, err)
			}
			lines := bytes.Count(b, []byte{'\n'})
			if len(b) > 0 && b[len(b)-1] != '\n' {
				lines++
			}
			if lines > maxModuleFileLines {
				t.Errorf("%s has %d lines (limit %d): split into focused modules under %d lines",
					p, lines, maxModuleFileLines, maxModuleFileLines)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
