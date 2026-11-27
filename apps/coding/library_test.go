package coding

import (
	"context"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// A run files its change and its deliverables in its person's Library — never
// the source it edited — keyed so a second recording files nothing new.
func TestARunFilesWhatItMade(t *testing.T) {
	store := stored(t)
	var mu sync.Mutex
	var got []client.LibraryPutIn
	var who []zip.Caller
	origPut := libraryPut
	libraryPut = func(ctx context.Context, in *client.LibraryPutIn) (*client.LibraryItem, error) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, *in)
		who = append(who, zip.CallerOf(ctx))
		return &client.LibraryItem{ID: "lib_x"}, nil
	}
	t.Cleanup(func() { libraryPut = origPut })

	files := map[string]string{"changes.patch": "diff --git a/x b/x", "files/report.md": "# Report", "files/chart.png": "\x89PNG", "files/main.go": "package main"}
	for k, v := range files {
		_ = store.put(context.Background(), "acme/sess_1/"+k, []byte(v))
	}
	ix := index{CodingArtifacts: client.CodingArtifacts{Artifacts: []client.Artifact{
		{Name: "changes.patch", Kind: kindPatch, Size: 18},
		{Name: "report.md", Kind: kindFile, Size: 8},
		{Name: "chart.png", Kind: kindFile, Size: 4},
		{Name: "main.go", Kind: kindFile, Size: 12},
		{Name: "preview", Kind: kindPreview, URL: "https://x"},
	}}}
	Dispatcher{}.shelve(context.Background(), "acme", "alice", "sess_1", ix)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("filed %d: %+v", len(got), got)
	}
	kinds := map[string]string{}
	for i, in := range got {
		kinds[in.Name] = in.Kind
		if in.Source != "dev" || in.Ref != "sess_1" || in.Origin != "/dev/sess_1" || in.Key != "sess_1/"+in.Name || who[i].User != "alice" || who[i].Org != "acme" {
			t.Errorf("filed %+v as %+v", in, who[i])
		}
	}
	if kinds["changes.patch"] != "code" || kinds["report.md"] != "document" || kinds["chart.png"] != "image" {
		t.Fatalf("kinds = %v", kinds)
	}
	if _, ok := kinds["main.go"]; ok {
		t.Fatal("the source the run edited was filed")
	}
}
