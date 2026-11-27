package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// catalog_rpc.go — the brand's skills, answered to the tool plane over the plane.
//
// The tool plane lists every skill an org can activate at GET /v1/tool, and this
// app holds the brand's catalogue. They are separate processes (manifest/apps.go),
// so a provider registered here would sit in a registry nothing serves. The tool
// app asks this op instead and lists what it answers.

// serveCatalog publishes the brand's catalogue on the plane. Use calls it once the
// embedded tree is open. The brand is the deployment's: a plane call carries no
// Host, and the catalogue a tool plane lists is the one this deployment serves.
func serveCatalog(fsys fs.FS, brand string) {
	cloud.Plane().Post("/skills/catalog",
		func(_ context.Context, _ *client.SkillCatalogIn) (*client.SkillCatalog, error) {
			out, err := readCatalog(fsys, brand)
			if err != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "skills: %v", err)
			}
			return out, nil
		},
		zip.WithOperationID(client.SkillsCatalog),
		zip.WithSummary("List the deployment brand's agent skills for the tool plane"))
	cloud.Plane().Post("/skills/documents",
		func(_ context.Context, in *client.SkillDocsIn) (*client.SkillDocs, error) {
			return readDocuments(fsys, brand, in.Names), nil
		},
		zip.WithOperationID(client.SkillsDocuments),
		zip.WithSummary("Read the brand's skill documents by name"))
}

// maxDocuments bounds one documents answer; a SKILL.md is a few KiB.
const maxDocuments = 16

// readDocuments answers each named skill's SKILL.md, in the order asked. A name
// is the catalogue's own (`git_repos`), or its tool name (`skill_git_repos`); one
// the brand does not carry, or of any other shape, is left out.
func readDocuments(fsys fs.FS, brand string, names []string) *client.SkillDocs {
	out := &client.SkillDocs{Skills: []client.SkillDoc{}}
	seen := map[string]bool{}
	for _, n := range names {
		if len(out.Skills) == maxDocuments {
			break
		}
		name := strings.TrimPrefix(strings.TrimSpace(n), "skill_")
		if seen[name] || !skillID.MatchString(name) {
			continue
		}
		seen[name] = true
		b, err := fs.ReadFile(fsys, path.Join(brand, name, "SKILL.md"))
		if err != nil {
			if md, ok := builtinSkills[name]; ok {
				out.Skills = append(out.Skills, client.SkillDoc{Name: name, Content: md})
			}
			continue
		}
		out.Skills = append(out.Skills, client.SkillDoc{Name: name, Content: string(b)})
	}
	return out
}

// readCatalog reads one brand's index.json into the entries the tool plane lists.
func readCatalog(fsys fs.FS, brand string) (*client.SkillCatalog, error) {
	b, err := fs.ReadFile(fsys, path.Join(brand, "index.json"))
	if err != nil {
		return nil, fmt.Errorf("no catalogue for brand %q: %w", brand, err)
	}
	var idx struct {
		Skills []client.SkillEntry `json:"skills"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("catalogue for brand %q: %w", brand, err)
	}
	if idx.Skills == nil {
		idx.Skills = []client.SkillEntry{}
	}
	return &client.SkillCatalog{Brand: brand, Skills: idx.Skills}, nil
}
