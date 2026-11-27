package studio

// library.go — Studio's files are Library items.
//
// Studio holds no bucket and no file table. Every file a run makes is written to
// the one Library (apps/library, /v1/library) over the plane, AS THE RUN'S PERSON,
// so it is theirs exactly as an upload of theirs would be: the Library keys it under
// their org and their subject, checks every read, and mints every signed URL. A run
// reads an input the same way — library_get as its person — so a graph can only
// name a file its person could open.
//
// A file is written idempotently: its key is <run>/<step>/<item> (with the output's
// index past the first), so a part retried after a lost reply finds the item it
// made rather than making a second.
//
// Small files go up whole (library_put). A render worker's file, and anything past
// putMax, is reserved first (library_reserve), PUT to the signed URL the Library
// answers, and committed (library_commit), which is when the Library has seen it.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// putMax is the largest file library_put takes whole.
const putMax = 10 << 20

// Item is a Library item as Studio reads it: a file a run made or a person
// uploaded, with signed URLs good until expires.
type Item struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	Source  string  `json:"source"`
	Name    string  `json:"name"`
	Mime    string  `json:"mime"`
	Bytes   int64   `json:"bytes"`
	Width   int     `json:"width,omitempty"`
	Height  int     `json:"height,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
	Pages   int     `json:"pages,omitempty"`
	Owner   string  `json:"owner"`
	Mine    bool    `json:"mine"`
	Shared  bool    `json:"shared"`
	Ref     string  `json:"ref,omitempty"`
	Origin  string  `json:"origin,omitempty"`
	// URL opens the file inline; Download saves it; Preview is its still.
	URL      string `json:"url"`
	Download string `json:"download,omitempty"`
	Preview  string `json:"preview,omitempty"`
	Expires  string `json:"expires,omitempty"`
	Created  string `json:"created,omitempty"`
}

// libraryIn describes a file being written.
type libraryIn struct {
	Kind    string
	Name    string
	Mime    string
	Ref     string
	Origin  string
	Key     string
	Width   int
	Height  int
	Seconds float64
	Pages   int
}

// upload is where a reserved file is PUT.
type upload struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Expires string            `json:"expires,omitempty"`
}

// shelf is the Library, reached as the person ctx states.
type shelf interface {
	put(ctx context.Context, in libraryIn, data []byte) (Item, error)
	reserve(ctx context.Context, in libraryIn, size int64) (Item, upload, error)
	commit(ctx context.Context, id string) (Item, error)
	get(ctx context.Context, id string) (Item, error)
}

// tracker is the cross-product jobs board (apps/auto), reached as the person.
type tracker interface {
	track(ctx context.Context, in jobIn) error
}

// jobIn is one run's state on the jobs board.
type jobIn struct {
	Kind     string
	Title    string
	Ref      string
	Origin   string
	State    string
	Progress float64
	Summary  string
	Error    string
	Outputs  []string
	Cost     int64
}

// origin is the app path back to a run.
func origin(run string) string { return "/studio?run=" + run }

// itemKey is the idempotency key of one output of one part.
func itemKey(p Part, n int) string {
	if n == 0 {
		return fmt.Sprintf("%s/%s/%d", p.Run, p.Step, p.Item)
	}
	return fmt.Sprintf("%s/%s/%d.%d", p.Run, p.Step, p.Item, n)
}

// libraryKind is a file's Library kind from its media type and the op that made
// it.
func libraryKind(mime, op string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	case mime == "model/gltf-binary":
		return "model"
	case op == "render.slides" || strings.Contains(mime, "presentationml"):
		return "slides"
	case mime == "application/pdf", strings.Contains(mime, "wordprocessingml"), mime == "text/markdown":
		return "document"
	}
	return "file"
}

// keep writes bytes a part made as a Library item of the run's person: one
// function, whatever the size.
func (s *state) keep(ctx context.Context, r runRow, p Part, n int, data []byte) (Item, error) {
	mime := detect(data)
	if mime == "" {
		mime = "application/octet-stream"
	}
	in := libraryIn{
		Kind: libraryKind(mime, p.Op), Name: nameFor(p, n, mime), Mime: mime,
		Ref: p.Run, Origin: origin(p.Run), Key: itemKey(p, n),
	}
	if w, h, ok := dims(data); ok {
		in.Width, in.Height = w, h
	}
	as := s.as(ctx, r)
	if len(data) <= putMax {
		return s.lib.put(as, in, data)
	}
	item, up, err := s.lib.reserve(as, in, int64(len(data)))
	if err != nil {
		return Item{}, err
	}
	if err := putTo(ctx, up, data); err != nil {
		return Item{}, err
	}
	return s.lib.commit(as, item.ID)
}

// putTo PUTs a file to a reserved upload.
func putTo(ctx context.Context, up upload, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, up.URL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	for k, v := range up.Headers {
		req.Header.Set(k, v)
	}
	req.ContentLength = int64(len(data))
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("upload the file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("upload the file: the store answered %d", resp.StatusCode)
	}
	return nil
}

// readable is a Library item the person ctx states may read; a miss is the one 404.
func (s *state) readable(ctx context.Context, id string) (Item, error) {
	it, err := s.lib.get(ctx, id)
	if err != nil {
		if status(err) == http.StatusNotFound || status(err) == http.StatusForbidden {
			return Item{}, gone("item " + id)
		}
		return Item{}, err
	}
	return it, nil
}

// read loads an item's bytes, for a step that hands them to a model.
func (s *state) read(ctx context.Context, it Item) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, it.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("read %s: the store answered %d", it.ID, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFetch))
}

// maxFetch bounds a file Studio reads back.
const maxFetch = 256 << 20

// ── what a file is ───────────────────────────────────────────────────────

// detect names a file's type by its bytes, never by a name or a declared type.
func detect(b []byte) string {
	has := func(off int, sig string) bool { return len(b) >= off+len(sig) && string(b[off:off+len(sig)]) == sig }
	switch {
	case has(0, "\x89PNG\r\n\x1a\n"):
		return "image/png"
	case has(0, "\xff\xd8\xff"):
		return "image/jpeg"
	case has(0, "RIFF") && has(8, "WEBP"):
		return "image/webp"
	case has(0, "GIF87a"), has(0, "GIF89a"):
		return "image/gif"
	case has(4, "ftypqt"):
		return "video/quicktime"
	case has(4, "ftypM4A"):
		return "audio/mp4"
	case has(4, "ftyp"):
		return "video/mp4"
	case has(0, "\x1a\x45\xdf\xa3"):
		return "video/webm"
	case has(0, "ID3"), len(b) > 2 && b[0] == 0xff && b[1]&0xe0 == 0xe0:
		return "audio/mpeg"
	case has(0, "RIFF") && has(8, "WAVE"):
		return "audio/wav"
	case has(0, "OggS"):
		return "audio/ogg"
	case has(0, "fLaC"):
		return "audio/flac"
	case has(0, "glTF"):
		return "model/gltf-binary"
	case has(0, "%PDF-"):
		return "application/pdf"
	}
	return ""
}

// dims reads a PNG's or a JPEG's pixel size from its header.
func dims(b []byte) (int, int, bool) {
	if len(b) >= 24 && string(b[:8]) == "\x89PNG\r\n\x1a\n" && string(b[12:16]) == "IHDR" {
		return int(uint32(b[16])<<24 | uint32(b[17])<<16 | uint32(b[18])<<8 | uint32(b[19])),
			int(uint32(b[20])<<24 | uint32(b[21])<<16 | uint32(b[22])<<8 | uint32(b[23])), true
	}
	if len(b) > 4 && b[0] == 0xff && b[1] == 0xd8 {
		for i := 2; i+9 < len(b); {
			if b[i] != 0xff {
				return 0, 0, false
			}
			m := b[i+1]
			size := int(b[i+2])<<8 | int(b[i+3])
			if m >= 0xc0 && m <= 0xcf && m != 0xc4 && m != 0xc8 && m != 0xcc {
				return int(b[i+7])<<8 | int(b[i+8]), int(b[i+5])<<8 | int(b[i+6]), true
			}
			i += 2 + size
		}
	}
	return 0, 0, false
}

// extension is a media type's usual file extension.
func extension(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav":
		return ".wav"
	case "application/pdf":
		return ".pdf"
	case "model/gltf-binary":
		return ".glb"
	case "text/markdown":
		return ".md"
	}
	switch {
	case strings.Contains(mime, "presentationml"):
		return ".pptx"
	case strings.Contains(mime, "wordprocessingml"):
		return ".docx"
	}
	return ""
}

// nameFor names one output of a part after its step.
func nameFor(p Part, n int, mime string) string {
	name := p.Step
	if p.Items > 1 {
		name = fmt.Sprintf("%s-%d", name, p.Item+1)
	}
	if n > 0 {
		name = fmt.Sprintf("%s-%d", name, n+1)
	}
	return name + extension(mime)
}
