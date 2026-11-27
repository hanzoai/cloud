package skills

import (
	"encoding/json"
	"io/fs"
	"testing"
)

// The plane answers the brand's catalogue entry for entry: every skill index.json
// names, with its description, and nothing the index does not name.
func TestTheCatalogOpAnswersTheBrandsIndex(t *testing.T) {
	sub, err := fs.Sub(catalogFS, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	for _, brand := range []string{"hanzo", "lux", "zoo"} {
		got, err := readCatalog(sub, brand)
		if err != nil {
			t.Fatalf("%s: %v", brand, err)
		}
		raw, _ := fs.ReadFile(sub, brand+"/index.json")
		var idx struct {
			Skills []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"skills"`
		}
		if err := json.Unmarshal(raw, &idx); err != nil {
			t.Fatal(err)
		}
		if got.Brand != brand || len(got.Skills) != len(idx.Skills) || len(got.Skills) == 0 {
			t.Fatalf("%s: answered %d skills for an index of %d", brand, len(got.Skills), len(idx.Skills))
		}
		for i, sk := range idx.Skills {
			if got.Skills[i].Name != sk.Name || got.Skills[i].Description != sk.Description {
				t.Fatalf("%s: entry %d is %+v, index says %+v", brand, i, got.Skills[i], sk)
			}
		}
	}
	if _, err := readCatalog(sub, "nobody"); err == nil {
		t.Fatal("a brand with no catalogue must be an error")
	}
}

// The documents op answers the served SKILL.md for a catalogue name or its tool
// name, in the order asked, and nothing for a name the brand does not carry.
func TestTheDocumentsOpAnswersTheServedSkill(t *testing.T) {
	sub, err := fs.Sub(catalogFS, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := readCatalog(sub, "hanzo")
	if err != nil || len(cat.Skills) < 2 {
		t.Fatalf("catalogue: %v", err)
	}
	a, b := cat.Skills[0].Name, cat.Skills[1].Name
	got := readDocuments(sub, "hanzo", []string{"skill_" + b, a, "../zoo/" + a, "nope_nope", a})
	if len(got.Skills) != 2 || got.Skills[0].Name != b || got.Skills[1].Name != a {
		t.Fatalf("documents: %+v", got.Skills)
	}
	want, _ := fs.ReadFile(sub, "hanzo/"+a+"/SKILL.md")
	if got.Skills[1].Content != string(want) {
		t.Fatal("the document must be the served SKILL.md, byte for byte")
	}
	many := make([]string, 0, len(cat.Skills))
	for _, sk := range cat.Skills {
		many = append(many, sk.Name)
	}
	if n := len(readDocuments(sub, "hanzo", many).Skills); n != maxDocuments {
		t.Fatalf("one answer is bounded at %d, answered %d", maxDocuments, n)
	}
	builtins := readDocuments(sub, "hanzo", []string{"code-review", "skill_dev", "ui"})
	if len(builtins.Skills) != 3 || builtins.Skills[0].Name != "code-review" || builtins.Skills[1].Name != "dev" || builtins.Skills[2].Name != "ui" {
		t.Fatalf("builtins: %+v", builtins.Skills)
	}
}
