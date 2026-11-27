package provider

// social_media.go — what a post carries beside its words: photos and a video,
// named by address.
//
// A post stores addresses (apps/social normMedia), never bytes. Most networks
// take the address and fetch the file themselves (Instagram, Threads, Pinterest,
// TikTok, Facebook, Telegram), so nothing here downloads for them. Bluesky,
// Mastodon and YouTube take the file in the request, so this process fetches it.
// The address is the tenant's, which makes that fetch a request-forgery
// primitive by construction: it goes through hanzoai/outbound, which connects
// only to public addresses.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/outbound"
)

// mediaClient fetches a tenant's media address. A var so a test stands in for
// the public internet; production never reassigns it.
var mediaClient = outbound.Client(outbound.Options{Timeout: 10 * time.Minute, Redirects: 5})

var (
	videoAddress = regexp.MustCompile(`(?i)\.(mp4|mov|m4v|webm|mkv|avi|mpe?g|3gp)$`)
	imageAddress = regexp.MustCompile(`(?i)\.(jpe?g|png|gif|webp|avif|heic|heif|bmp|tiff?)$`)
)

// splitMedia sorts a post's addresses into photos and videos, in order. An
// address that names neither is refused: guessing is how a video reaches a
// network that takes only photos.
func splitMedia(media []string) (images, videos []string, err error) {
	for _, m := range media {
		u, perr := url.Parse(m)
		if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, nil, fmt.Errorf("the media address %q is not a web address", firstLine(m))
		}
		switch {
		case videoAddress.MatchString(u.Path):
			videos = append(videos, m)
		case imageAddress.MatchString(u.Path):
			images = append(images, m)
		default:
			return nil, nil, fmt.Errorf("the media address %q does not say whether it is a photo or a video", firstLine(m))
		}
	}
	return images, videos, nil
}

// pathOf is the path of a web address, for matching a file extension.
func pathOf(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	return u.Path
}

// mediaFile is a fetched media address, open for reading.
type mediaFile struct {
	Body io.ReadCloser
	// Type is the content type the host answered, or the one the address's
	// extension names.
	Type string
	// Size is the length in bytes, or -1 when the host did not say.
	Size int64
	// Name is the file name at the address.
	Name string
}

var errMediaTooLarge = errors.New("the media file is larger than this network takes")

// fetchMedia opens a media address. limit is the most the caller will read; a
// host that says it is larger is refused before any byte is read, and one that
// does not say is cut off at the limit by the caller's reader.
func fetchMedia(ctx context.Context, address string, limit int64) (*mediaFile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("the media address %q could not be read", firstLine(address))
	}
	resp, err := mediaClient.Do(req)
	if err != nil {
		if errors.Is(err, outbound.ErrRefused) {
			return nil, fmt.Errorf("the media address %q is not a public address", firstLine(address))
		}
		return nil, fmt.Errorf("the media address %q could not be fetched", firstLine(address))
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("the media address %q answered %d", firstLine(address), resp.StatusCode)
	}
	if limit > 0 && resp.ContentLength > limit {
		_ = resp.Body.Close()
		return nil, errMediaTooLarge
	}
	name := path.Base(resp.Request.URL.Path)
	ctype := resp.Header.Get("Content-Type")
	if mt, _, perr := mime.ParseMediaType(ctype); perr == nil {
		ctype = mt
	}
	if ctype == "" || ctype == "application/octet-stream" || ctype == "binary/octet-stream" {
		ctype = mime.TypeByExtension(path.Ext(name))
	}
	return &mediaFile{Body: resp.Body, Type: ctype, Size: resp.ContentLength, Name: name}, nil
}

// readMedia fetches a small media file into memory, refusing one over limit.
func readMedia(ctx context.Context, address string, limit int64) (*mediaFile, []byte, error) {
	f, err := fetchMedia(ctx, address, limit)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(f.Body, limit+1))
	if err != nil {
		return nil, nil, fmt.Errorf("the media address %q could not be read", firstLine(address))
	}
	if int64(len(b)) > limit {
		return nil, nil, errMediaTooLarge
	}
	return f, b, nil
}

// pollWaits are the pauses between looks at a container, a status, a media
// upload a network is still processing. A var so a test does not wait.
var pollWaits = []time.Duration{
	2 * time.Second, 3 * time.Second, 5 * time.Second, 5 * time.Second, 10 * time.Second,
	10 * time.Second, 15 * time.Second, 15 * time.Second,
}

// errProcessing is a network still working on a file when the looking stopped.
var errProcessing = errors.New("the network is still processing the media; try the post again in a few minutes")

// settle looks until the network says the file is ready, or fails, or the waits
// run out. look answers done, and an error for a network that refused the file.
func settle(ctx context.Context, look func(context.Context) (done bool, err error)) error {
	for i := 0; ; i++ {
		done, err := look(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if i >= len(pollWaits) {
			return errProcessing
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollWaits[i]):
		}
	}
}

// newRequest builds a request with a text body, or none.
func newRequest(ctx context.Context, method, endpoint, body string) (*http.Request, error) {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	return http.NewRequestWithContext(ctx, method, endpoint, r)
}

// newJSONRequest builds a POST with a JSON body.
func newJSONRequest(ctx context.Context, endpoint string, body any) (*http.Request, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := newRequest(ctx, http.MethodPost, endpoint, string(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// postForm sends a form to a Graph-style endpoint and decodes the answer. The
// credential rides the Authorization header, never the address.
func postForm(ctx context.Context, token, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = call(ctx, withBearer(req, token), out)
	return err
}

// sendJSON sends a JSON body with the credential in the header and decodes the
// answer.
func sendJSON(ctx context.Context, token, endpoint string, body, out any) error {
	req, err := newJSONRequest(ctx, endpoint, body)
	if err != nil {
		return err
	}
	_, err = call(ctx, withBearer(req, token), out)
	return err
}

// getJSONAt reads a Graph-style endpoint with the credential in the header.
func getJSONAt(ctx context.Context, token, endpoint string, out any) error {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	_, err = call(ctx, withBearer(req, token), out)
	return err
}
