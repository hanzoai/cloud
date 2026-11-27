package skills

import (
	"encoding/json"
	"io/fs"
	"path"
	"reflect"
	"testing"
)

type workRow struct {
	Name  string   `json:"name"`
	Works []string `json:"works"`
}

func readRows(t *testing.T, name string) []workRow {
	t.Helper()
	b, err := fs.ReadFile(catalogFS, name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var doc struct {
		Skills []workRow `json:"skills"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return doc.Skills
}

// A client reads which skills suit a kind of work off the catalogue, so a product's own
// index must say what the top index says of the same skill.
func TestAProductIndexCarriesTheWorksOfTheTopIndex(t *testing.T) {
	top := map[string][]string{}
	recommended := 0
	for _, r := range readRows(t, "catalog/hanzo/index.json") {
		top[r.Name] = r.Works
		if len(r.Works) > 0 {
			recommended++
		}
	}
	if recommended == 0 {
		t.Fatal("no skill of the catalogue is recommended for any work")
	}
	dirs, err := fs.Glob(catalogFS, "catalog/hanzo/_*/index.json")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no product indexes: %v", err)
	}
	for _, f := range dirs {
		for _, r := range readRows(t, f) {
			if !reflect.DeepEqual(r.Works, top[r.Name]) {
				t.Errorf("%s: %s works %v, the top index says %v", path.Base(path.Dir(f)), r.Name, r.Works, top[r.Name])
			}
		}
	}
}
