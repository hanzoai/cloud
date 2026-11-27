// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package limits

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLLMDocNamesTheDefaultFallback holds the repository's LLM.md files to DefaultFallback
// wherever it says what the constant is. The constant became enso-auto when enso-free
// was retired (2026-11-07) while LLM.md went on naming enso-free, and an agent reading
// it took a Free chat's failure for a missing enso-free (hanzo-inc/cloud#600).
func TestLLMDocNamesTheDefaultFallback(t *testing.T) {
	// Every LLM.md in the repository: the guidance lives beside the code it
	// describes, so the sentence may sit in any of them.
	var all strings.Builder
	err := filepath.WalkDir(filepath.Join("..", "..", ".."), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "LLM.md" && d.Type().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			all.Write(b)
			all.WriteByte('\n')
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read the LLM.md files: %v", err)
	}
	doc := []byte(all.String())
	const lead = "`limits.DefaultFallback`, `"
	rest, said := string(doc), 0
	for {
		i := strings.Index(rest, lead)
		if i < 0 {
			break
		}
		rest = rest[i+len(lead):]
		named, _, _ := strings.Cut(rest, "`")
		if named != DefaultFallback {
			t.Errorf("LLM.md says limits.DefaultFallback is %q; the constant is %q", named, DefaultFallback)
		}
		said++
	}
	if said == 0 {
		t.Fatalf("LLM.md no longer says what limits.DefaultFallback is (%s…); say it beside the constant's name", lead)
	}
}
