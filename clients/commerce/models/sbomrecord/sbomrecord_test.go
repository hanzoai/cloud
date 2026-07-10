package sbomrecord

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
)

func testDB() *datastore.Datastore { return datastore.New(context.Background()) }

func TestKind(t *testing.T) {
	if (&SBOMRecord{}).Kind() != "sbom-record" {
		t.Fatalf("kind = %q", (&SBOMRecord{}).Kind())
	}
}

func TestSaveLoad_RoundTripsComponents(t *testing.T) {
	s := New(testDB())
	s.ImageRef = "ghcr.io/hanzoai/commerce:1.0.0"
	s.ImageDigest = "sha256:abc"
	s.Service = "commerce"
	s.Components = []Component{
		{PURL: "pkg:golang/github.com/gin-gonic/gin@v1.12.0", Name: "gin", Ecosystem: "golang", Version: "v1.12.0", Scope: "direct"},
		{PURL: "pkg:npm/react@18.3.1", Name: "react", Ecosystem: "npm", Version: "18.3.1", Scope: "transitive"},
	}

	ps, err := s.Save()
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if s.ComponentCount != 2 {
		t.Fatalf("ComponentCount = %d, want 2", s.ComponentCount)
	}
	if s.Components_ == "" {
		t.Fatal("Components_ JSON blob not populated on Save")
	}

	// Load into a fresh record from the saved properties.
	loaded := New(testDB())
	if err := loaded.Load(ps); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Components) != 2 {
		t.Fatalf("loaded %d components, want 2", len(loaded.Components))
	}
	if loaded.Components[0].PURL != "pkg:golang/github.com/gin-gonic/gin@v1.12.0" {
		t.Fatalf("loaded component PURL = %q", loaded.Components[0].PURL)
	}
	if loaded.Format != "cyclonedx" {
		t.Fatalf("default format = %q, want cyclonedx", loaded.Format)
	}
}
