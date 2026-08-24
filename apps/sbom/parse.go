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

// Pure core of the SBOM lens: the wire types, the CycloneDX parser, the row
// builder, and the datastore value coercers. Everything here is I/O-free so the
// tests drive it with inline documents — no datastore needed — exactly as the
// analytics lens proves out its assemblers. The handlers (sbom.go) are the thin
// orchestration that persists these rows and reads them back.

package sbom

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// sbomTable is the ONE global, cross-tenant SBOM store: an SBOM belongs to an
// image digest, not a tenant, so any tenant deploying that image resolves the
// same component set. ReplacingMergeTree(ingested_at) dedupes a re-ingest by the
// component identity in the ORDER BY, keeping the latest.
const sbomTable = "hanzo.sbom_component"

// maxComponents caps a resolve response so a pathological SBOM can't unbound the
// payload. We fetch one past it to detect (and honestly report) truncation.
const maxComponents = 5000

// ── Wire types (the console + CI contract) ──────────────────────────────────

// SbomComponent is one flattened dependency: name/version/type/purl + a single
// license string (first id | name | expression found, else "").
type SbomComponent struct {
	// Name is the dependency's package name as CycloneDX records it.
	Name string `json:"name"`
	// Version is the resolved version of that package.
	Version string `json:"version"`
	// Type is the CycloneDX component type: library, application, framework, …
	Type string `json:"type"`
	// Purl is the package URL — the ecosystem-qualified identity a vulnerability
	// feed can be joined on.
	Purl string `json:"purl"`
	// License is the FIRST license fact found for the component: its SPDX id, else
	// its name, else the expression. Empty when the document declares none.
	License string `json:"license"`
}

// SbomIngest is the POST /v1/sbom body from CI: the image identity + a raw
// CycloneDX document whose components[] we flatten and persist.
type SbomIngest struct {
	// ImageDigest is the content-addressed digest (sha256:…) the components are
	// keyed under. Required — it, not a tenant, is what an SBOM belongs to.
	ImageDigest string `json:"imageDigest"`
	// ImageRef is the human-readable image reference the digest was published as.
	// A resolve matches on either this or the digest.
	ImageRef string `json:"imageRef"`
	// SourceRepo is the repository the image was built from.
	SourceRepo string `json:"sourceRepo"`
	// GitSha is the commit the image was built from.
	GitSha string `json:"gitSha"`
	// Format names the document format; "cyclonedx" is the only one parsed.
	Format string `json:"format"`
	// Document is the raw CycloneDX bill of materials, any JSON. Its components[]
	// are flattened and persisted; nothing else is read or stored.
	Document json.RawMessage `json:"document"`
}

// SbomView is the GET /v1/sbom/{ref} response the console renders.
type SbomView struct {
	// ImageDigest is the content-addressed digest the components are keyed under,
	// and is what identifies this bill of materials however it was asked for.
	ImageDigest string `json:"imageDigest"`
	// ImageRef is the image reference recorded alongside the digest. Empty when
	// the components were ingested against a digest with no ref.
	ImageRef string `json:"imageRef"`
	// SourceRepo is the repository the image was built from. Empty unless the
	// ingest declared it — a pull from the registry does not learn it.
	SourceRepo string `json:"sourceRepo"`
	// GitSha is the commit the image was built from. Empty on the same terms as
	// sourceRepo.
	GitSha string `json:"gitSha"`
	// IngestedAt is when the bill of materials was recorded, RFC 3339.
	IngestedAt string `json:"ingestedAt"`
	// ComponentCount is how many components this response carries — after the cap,
	// so it matches components exactly rather than the image's true total.
	ComponentCount int `json:"componentCount"`
	// Truncated is true when the image has MORE components than the cap returns.
	Truncated bool `json:"truncated,omitempty"`
	// Components is the flattened dependency set, ordered by type then name.
	Components []SbomComponent `json:"components"`
}

// ── CycloneDX shapes (only the fields we consume) ───────────────────────────

type cdxDoc struct {
	Components []cdxComponent `json:"components"`
}

type cdxComponent struct {
	Name     string       `json:"name"`
	Version  string       `json:"version"`
	Type     string       `json:"type"`
	Purl     string       `json:"purl"`
	Licenses []cdxLicense `json:"licenses"`
}

// cdxLicense covers the three CycloneDX shapes: {license:{id}}, {license:{name}},
// and {expression}.
type cdxLicense struct {
	License    *cdxLicenseInner `json:"license"`
	Expression string           `json:"expression"`
}

type cdxLicenseInner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ── Pure parse ──────────────────────────────────────────────────────────────

// parseComponents flattens a raw CycloneDX document into SbomComponent rows. A
// malformed document is a hard error (the caller maps it to 400); a valid
// document with no components yields an empty slice (honest, not an error).
func parseComponents(document json.RawMessage) ([]SbomComponent, error) {
	if len(strings.TrimSpace(string(document))) == 0 {
		return nil, fmt.Errorf("empty document")
	}
	var doc cdxDoc
	if err := json.Unmarshal(document, &doc); err != nil {
		return nil, fmt.Errorf("invalid CycloneDX document: %w", err)
	}
	out := make([]SbomComponent, 0, len(doc.Components))
	for _, c := range doc.Components {
		out = append(out, SbomComponent{
			Name:    c.Name,
			Version: c.Version,
			Type:    c.Type,
			Purl:    c.Purl,
			License: flattenLicense(c.Licenses),
		})
	}
	return out, nil
}

// flattenLicense reduces a CycloneDX licenses[] array to a single string: the
// first license id, else the first license name, else the first expression, else
// "". First non-empty wins in array order.
func flattenLicense(ls []cdxLicense) string {
	for _, l := range ls {
		if l.License != nil {
			if v := strings.TrimSpace(l.License.ID); v != "" {
				return v
			}
			if v := strings.TrimSpace(l.License.Name); v != "" {
				return v
			}
		}
		if v := strings.TrimSpace(l.Expression); v != "" {
			return v
		}
	}
	return ""
}

// ── Persist row building ─────────────────────────────────────────────────────

// insertBatch builds the ONE multi-row INSERT for a component set: a single
// statement with a value-tuple per component and every value bound POSITIONALLY
// (datastore-go renders `?` into the statement — nothing is interpolated). Empty
// components → empty stmt, so the caller skips the write. ingested_at is DEFAULT
// now(), so it is not in the column list.
func insertBatch(in SbomIngest, comps []SbomComponent) (string, []any) {
	if len(comps) == 0 {
		return "", nil
	}
	const cols = "(image_digest, image_ref, source_repo, git_sha, component_name, component_version, component_type, purl, license)"
	tuples := make([]string, 0, len(comps))
	args := make([]any, 0, len(comps)*9)
	for _, c := range comps {
		tuples = append(tuples, "(?, ?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(args,
			in.ImageDigest, in.ImageRef, in.SourceRepo, in.GitSha,
			c.Name, c.Version, c.Type, c.Purl, c.License)
	}
	stmt := "INSERT INTO " + sbomTable + " " + cols + " VALUES " + strings.Join(tuples, ", ")
	return stmt, args
}

// buildView assembles the resolve response from the datastore rows (already
// ordered by type,name). The image identity comes from the first row; components
// come from every row up to the cap. Pure.
func buildView(rows []map[string]any) SbomView {
	truncated := len(rows) > maxComponents
	if truncated {
		rows = rows[:maxComponents]
	}
	v := SbomView{Truncated: truncated, Components: make([]SbomComponent, 0, len(rows))}
	if len(rows) > 0 {
		head := rows[0]
		v.ImageDigest = aString(head["image_digest"])
		v.ImageRef = aString(head["image_ref"])
		v.SourceRepo = aString(head["source_repo"])
		v.GitSha = aString(head["git_sha"])
		v.IngestedAt = aTime(head["ingested_at"]).Format(time.RFC3339)
	}
	for _, r := range rows {
		v.Components = append(v.Components, SbomComponent{
			Name:    aString(r["component_name"]),
			Version: aString(r["component_version"]),
			Type:    aString(r["component_type"]),
			Purl:    aString(r["purl"]),
			License: aString(r["license"]),
		})
	}
	v.ComponentCount = len(v.Components)
	return v
}

// ── Value coercion ──────────────────────────────────────────────────────────
//
// The direct datastore driver decodes String→string and DateTime→time.Time.
// These coercers accept those natives (and a string fallback for time) so a
// transport change can't crash a read. Mirrors the analytics lens's coercers.

func aString(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case fmt.Stringer:
		return s.String()
	default:
		return fmt.Sprintf("%v", s)
	}
}

func aTime(v any) time.Time {
	if t, ok := v.(time.Time); ok {
		return t.UTC()
	}
	s := strings.TrimSpace(aString(v))
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
