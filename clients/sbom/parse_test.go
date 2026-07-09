// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package sbom

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A small but representative CycloneDX document: a library with a license id, an
// operating-system component with NO license, a component with a license
// expression, and a library with a license name. Proves the parser flattens
// components + the three license shapes correctly.
const sampleCycloneDX = `{
  "bomFormat": "CycloneDX",
  "specVersion": "1.5",
  "components": [
    {
      "type": "library",
      "name": "golang.org/x/crypto",
      "version": "v0.21.0",
      "purl": "pkg:golang/golang.org/x/crypto@v0.21.0",
      "licenses": [ { "license": { "id": "BSD-3-Clause" } } ]
    },
    {
      "type": "operating-system",
      "name": "debian",
      "version": "12"
    },
    {
      "type": "library",
      "name": "openssl",
      "version": "3.0.11",
      "purl": "pkg:deb/debian/openssl@3.0.11",
      "licenses": [ { "expression": "MIT OR Apache-2.0" } ]
    },
    {
      "type": "library",
      "name": "leftpad",
      "version": "1.0.0",
      "licenses": [ { "license": { "name": "Custom Community License" } } ]
    }
  ]
}`

func TestParseComponents(t *testing.T) {
	comps, err := parseComponents(json.RawMessage(sampleCycloneDX))
	if err != nil {
		t.Fatalf("parseComponents: %v", err)
	}
	if len(comps) != 4 {
		t.Fatalf("want 4 components, got %d", len(comps))
	}

	want := []SbomComponent{
		{Name: "golang.org/x/crypto", Version: "v0.21.0", Type: "library", Purl: "pkg:golang/golang.org/x/crypto@v0.21.0", License: "BSD-3-Clause"},
		{Name: "debian", Version: "12", Type: "operating-system", Purl: "", License: ""},
		{Name: "openssl", Version: "3.0.11", Type: "library", Purl: "pkg:deb/debian/openssl@3.0.11", License: "MIT OR Apache-2.0"},
		{Name: "leftpad", Version: "1.0.0", Type: "library", Purl: "", License: "Custom Community License"},
	}
	for i, w := range want {
		if comps[i] != w {
			t.Fatalf("component[%d] = %+v, want %+v", i, comps[i], w)
		}
	}
}

// TestFlattenLicensePriority: id wins over name wins over expression, first
// non-empty in array order; empty when none present.
func TestFlattenLicensePriority(t *testing.T) {
	cases := []struct {
		name string
		in   []cdxLicense
		want string
	}{
		{"id-over-name", []cdxLicense{{License: &cdxLicenseInner{ID: "MIT", Name: "The MIT License"}}}, "MIT"},
		{"name-when-no-id", []cdxLicense{{License: &cdxLicenseInner{Name: "Proprietary"}}}, "Proprietary"},
		{"expression", []cdxLicense{{Expression: "Apache-2.0 AND MIT"}}, "Apache-2.0 AND MIT"},
		{"first-wins", []cdxLicense{{License: &cdxLicenseInner{ID: "GPL-3.0"}}, {Expression: "MIT"}}, "GPL-3.0"},
		{"none", nil, ""},
		{"empty-entry", []cdxLicense{{License: &cdxLicenseInner{}}}, ""},
	}
	for _, tc := range cases {
		if got := flattenLicense(tc.in); got != tc.want {
			t.Fatalf("%s: flattenLicense = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseEmptyDocumentErrors(t *testing.T) {
	if _, err := parseComponents(json.RawMessage("")); err == nil {
		t.Fatal("empty document must error")
	}
	if _, err := parseComponents(json.RawMessage("not json")); err == nil {
		t.Fatal("malformed document must error")
	}
}

func TestParseNoComponentsIsEmptyNotError(t *testing.T) {
	comps, err := parseComponents(json.RawMessage(`{"bomFormat":"CycloneDX","specVersion":"1.5"}`))
	if err != nil {
		t.Fatalf("valid doc with no components must not error: %v", err)
	}
	if len(comps) != 0 {
		t.Fatalf("want 0 components, got %d", len(comps))
	}
}

// TestInsertBatch: ONE multi-row statement, values bound POSITIONALLY (never
// interpolated). Two components → two tuples, 18 args, imageDigest bound as the
// leading arg of each row.
func TestInsertBatch(t *testing.T) {
	in := SbomIngest{ImageDigest: "sha256:abc", ImageRef: "ghcr.io/hanzoai/foo:v1", SourceRepo: "hanzoai/foo", GitSha: "deadbeef"}
	comps := []SbomComponent{
		{Name: "a", Version: "1", Type: "library", Purl: "pkg:a", License: "MIT"},
		{Name: "b", Version: "2", Type: "library", Purl: "pkg:b", License: ""},
	}
	stmt, args := insertBatch(in, comps)
	if !strings.HasPrefix(stmt, "INSERT INTO "+sbomTable) {
		t.Fatalf("stmt must INSERT INTO %s: %q", sbomTable, stmt)
	}
	if strings.Count(stmt, "(?, ?, ?, ?, ?, ?, ?, ?, ?)") != 2 {
		t.Fatalf("want 2 value tuples, got stmt %q", stmt)
	}
	if len(args) != 18 {
		t.Fatalf("want 18 bound args (2x9), got %d", len(args))
	}
	// No component/image value is interpolated into the SQL text.
	for _, v := range []string{"sha256:abc", "pkg:a", "MIT", "hanzoai/foo"} {
		if strings.Contains(stmt, v) {
			t.Fatalf("value %q must be bound, not interpolated into %q", v, stmt)
		}
	}
	if args[0] != "sha256:abc" || args[9] != "sha256:abc" {
		t.Fatalf("imageDigest must lead each row's args, got %v / %v", args[0], args[9])
	}
}

func TestInsertBatchEmptyNoop(t *testing.T) {
	stmt, args := insertBatch(SbomIngest{ImageDigest: "sha256:x"}, nil)
	if stmt != "" || args != nil {
		t.Fatalf("empty components must yield no statement, got %q / %v", stmt, args)
	}
}

// TestBuildView assembles the resolve response from native ClickHouse scan types
// (string + time.Time), preserving row order and deriving componentCount.
func TestBuildView(t *testing.T) {
	ts := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	rows := []map[string]any{
		{
			"image_digest": "sha256:abc", "image_ref": "ghcr.io/hanzoai/foo:v1", "source_repo": "hanzoai/foo", "git_sha": "deadbeef",
			"component_name": "debian", "component_version": "12", "component_type": "operating-system", "purl": "", "license": "", "ingested_at": ts,
		},
		{
			"image_digest": "sha256:abc", "image_ref": "ghcr.io/hanzoai/foo:v1", "source_repo": "hanzoai/foo", "git_sha": "deadbeef",
			"component_name": "x/crypto", "component_version": "v0.21.0", "component_type": "library", "purl": "pkg:golang/x/crypto", "license": "BSD-3-Clause", "ingested_at": ts,
		},
	}
	v := buildView(rows)
	if v.ImageDigest != "sha256:abc" || v.ImageRef != "ghcr.io/hanzoai/foo:v1" || v.SourceRepo != "hanzoai/foo" || v.GitSha != "deadbeef" {
		t.Fatalf("view header wrong: %+v", v)
	}
	if v.IngestedAt != ts.Format(time.RFC3339) {
		t.Fatalf("ingestedAt = %q, want %q", v.IngestedAt, ts.Format(time.RFC3339))
	}
	if v.ComponentCount != 2 || len(v.Components) != 2 {
		t.Fatalf("componentCount = %d, want 2", v.ComponentCount)
	}
	if v.Truncated {
		t.Fatal("must not be truncated")
	}
	if v.Components[0].Name != "debian" || v.Components[1].License != "BSD-3-Clause" {
		t.Fatalf("row order/coercion wrong: %+v", v.Components)
	}
}

func TestBuildViewTruncates(t *testing.T) {
	rows := make([]map[string]any, maxComponents+5)
	for i := range rows {
		rows[i] = map[string]any{"component_name": "c", "component_type": "library"}
	}
	v := buildView(rows)
	if !v.Truncated {
		t.Fatal("over-cap result must report truncated")
	}
	if v.ComponentCount != maxComponents {
		t.Fatalf("componentCount capped at %d, got %d", maxComponents, v.ComponentCount)
	}
}
