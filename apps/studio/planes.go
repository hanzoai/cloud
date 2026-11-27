package studio

// planes.go — the Library and the jobs board, over the plane. Every call states
// the caller ctx carries: the run's person in the run's org (state.as), so the
// Library files what it writes under them and checks what it reads against them,
// and the board shows the run among their jobs.

import (
	"context"
	"encoding/json"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
)

// source is the Library and the board's name for Studio.
const source = "studio"

// The peers, by the names their sockets answer on.
const (
	libraryApp = "library"
	autoApp    = "auto"
)

type planeLibrary struct{}

func putIn(in libraryIn) client.LibraryPutIn {
	return client.LibraryPutIn{
		Kind: in.Kind, Source: source, Name: in.Name, Mime: in.Mime, Ref: in.Ref, Origin: in.Origin, Key: in.Key,
		Width: int64(in.Width), Height: int64(in.Height), Seconds: in.Seconds, Pages: int64(in.Pages),
	}
}

func (planeLibrary) put(ctx context.Context, in libraryIn, data []byte) (Item, error) {
	req := putIn(in)
	req.Data = data
	it, err := plane.Call[client.LibraryPutIn, client.LibraryItem](ctx, libraryApp, client.LibraryPut, &req)
	if err != nil {
		return Item{}, err
	}
	return item(it)
}

func (planeLibrary) reserve(ctx context.Context, in libraryIn, size int64) (Item, upload, error) {
	req := putIn(in)
	req.Bytes = size
	out, err := plane.Call[client.LibraryPutIn, client.LibraryReserved](ctx, libraryApp, client.LibraryReserve, &req)
	if err != nil {
		return Item{}, upload{}, err
	}
	it, err := item(&out.Item)
	if err != nil {
		return Item{}, upload{}, err
	}
	up := upload{URL: out.Upload.URL, Expires: out.Upload.Expires, Headers: map[string]string{}}
	for _, h := range out.Upload.Headers {
		up.Headers[h.Name] = h.Value
	}
	return it, up, nil
}

func (planeLibrary) commit(ctx context.Context, id string) (Item, error) {
	it, err := plane.Call[client.LibraryRef, client.LibraryItem](ctx, libraryApp, client.LibraryCommit, &client.LibraryRef{ID: id})
	if err != nil {
		return Item{}, err
	}
	return item(it)
}

func (planeLibrary) get(ctx context.Context, id string) (Item, error) {
	it, err := plane.Call[client.LibraryRef, client.LibraryItem](ctx, libraryApp, client.LibraryGet, &client.LibraryRef{ID: id})
	if err != nil {
		return Item{}, err
	}
	return item(it)
}

// item is the Library's item as Studio's wire carries it: the same JSON.
func item(it *client.LibraryItem) (Item, error) {
	b, err := json.Marshal(it)
	if err != nil {
		return Item{}, err
	}
	var out Item
	return out, json.Unmarshal(b, &out)
}

type planeJobs struct{}

func (planeJobs) track(ctx context.Context, in jobIn) error {
	progress, cost := in.Progress, in.Cost
	_, err := plane.Call[client.JobTrackIn, client.Job](ctx, autoApp, client.AutoJobTrack, &client.JobTrackIn{
		Product: source, Kind: in.Kind, Title: in.Title, Ref: in.Ref, Origin: in.Origin, State: in.State,
		Progress: &progress, Summary: in.Summary, Error: in.Error, Outputs: in.Outputs, Cost: &cost,
	})
	return err
}
