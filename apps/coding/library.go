package coding

// library.go — what a run made, filed in its person's Library.
//
// A run's artifacts are kept beside it (artifact.go) so the run can be read,
// resumed and published. What a PERSON made is a narrower set, and it is filed
// where everything else they made is: the run's whole change, as one patch, and
// every kept file that is a deliverable — a document, a report, an image, media,
// a model, an archive. Never the source the run edited: that is the repository's,
// and a Library that listed every .go file a run touched would bury the poster
// under the parser.
//
// The Library is the one write path (apps/library): this asks it over the plane as
// the run's person, keyed by session and name, so a run that is recorded twice
// files each thing once.

import (
	"context"
	"errors"
	"io"
	"mime"
	"path"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	librarypeer "github.com/hanzoai/cloud/client/library"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

const (
	// shelfMax bounds how many things one run files.
	shelfMax = 16
	// shelfWait bounds how long filing them may take.
	shelfWait = 2 * time.Minute
)

// libraryPut files one thing in the Library. A value so a test stands in for it.
var libraryPut = librarypeer.LibraryPut

// deliverables are the extensions of the files a run files as made: what a
// person opens, sends or shows, as opposed to source a build reads.
var deliverables = map[string]string{
	".md": "document", ".markdown": "document", ".txt": "document", ".pdf": "document", ".csv": "document",
	".docx": "document", ".doc": "document", ".odt": "document", ".rtf": "document", ".html": "document", ".xlsx": "document",
	".pptx": "slides", ".ppt": "slides", ".key": "slides", ".odp": "slides",
	".png": "image", ".jpg": "image", ".jpeg": "image", ".gif": "image", ".webp": "image", ".avif": "image", ".svg": "image",
	".mp4": "video", ".webm": "video", ".mov": "video",
	".mp3": "audio", ".wav": "audio", ".ogg": "audio", ".m4a": "audio",
	".glb": "model", ".gltf": "model", ".usdz": "model", ".stl": "model", ".obj": "model",
	".ttf": "type", ".otf": "type", ".woff": "type", ".woff2": "type",
	".zip": "file", ".tar": "file", ".gz": "file", ".tgz": "file",
}

// filedAs is what one artifact is filed as: its kind and media type, or false
// for one the Library does not keep.
func filedAs(a client.Artifact) (kind, media string, ok bool) {
	if a.Kind == kindPatch {
		return "code", "text/x-diff", true
	}
	if a.Kind != kindFile {
		return "", "", false
	}
	ext := strings.ToLower(path.Ext(a.Name))
	kind, ok = deliverables[ext]
	if !ok {
		return "", "", false
	}
	media = mime.TypeByExtension(ext)
	if media == "" {
		media = "application/octet-stream"
	}
	media, _, _ = strings.Cut(media, ";")
	return kind, media, true
}

// shelve files what run session of org made in person's Library. It runs after
// the run's end is recorded, never before it, and logs rather than fails: a run
// that worked is not undone by a Library that did not take its files.
func (d Dispatcher) shelve(ctx context.Context, org, person, session string, ix index) {
	if person == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, shelfWait)
	defer cancel()
	as := zip.WithCaller(cloud.For(ctx, org), zip.Caller{Org: org, User: person})
	filed := 0
	for _, a := range ix.Artifacts {
		kind, media, ok := filedAs(a)
		if !ok || a.Size <= 0 || a.Size > artifactFile {
			continue
		}
		if filed == shelfMax {
			break
		}
		r, _, err := runs.open(ctx, prefix(org, session)+"/"+keyOf(a))
		if err != nil {
			d.logf("coding: an artifact was not read for the Library", "org", org, "session", session, "name", a.Name, "err", err)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(r, artifactFile+1))
		_ = r.Close()
		if err != nil || int64(len(data)) > artifactFile {
			continue
		}
		_, err = libraryPut(as, &client.LibraryPutIn{
			Kind: kind, Source: "dev", Name: path.Base(a.Name), Mime: media, Data: data,
			Ref: session, Origin: "/dev/" + session, Key: session + "/" + a.Name,
		})
		if errors.Is(err, plane.ErrNoPeer) {
			return
		}
		if err != nil {
			d.logf("coding: an artifact was not kept in the Library", "org", org, "session", session, "name", a.Name, "err", err)
			continue
		}
		filed++
	}
}
