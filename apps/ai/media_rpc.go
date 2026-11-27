// Copyright © 2026 Hanzo AI. MIT License.

package ai

// media_rpc.go — the media call a sibling buys: an image, speech, music, a video
// or a 3D model (and the reads of a video or 3D job it started), answered by the
// model API in this process for the person the caller names, exactly as a sibling's
// completion is (chat_rpc.go). The caller's own meter charges the customer; the
// model API covers the call.
//
// Only the media routes are reachable, and only by their own methods: an id in a
// path is one segment of letters, digits, _ and -, so a path can name a job and
// nothing past it.

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// media is every route a sibling may buy, by method.
var media = []struct {
	method string
	path   *regexp.Regexp
}{
	{http.MethodPost, regexp.MustCompile(`^/v1/images/(generations|edits)$`)},
	{http.MethodPost, regexp.MustCompile(`^/v1/frames?(/(generations|edits?))?$`)},
	{http.MethodPost, regexp.MustCompile(`^/v1/audio/(speech|music|voice|foley)$`)},
	{http.MethodPost, regexp.MustCompile(`^/v1/(videos|3d)/generations$`)},
	{http.MethodPost, regexp.MustCompile(`^/v1/((tabular|relational|structured-data)/)?predictions$`)},
	{http.MethodGet, regexp.MustCompile(`^/v1/(videos|3d)/[A-Za-z0-9_-]{1,128}(/content)?$`)},
}

// exposeMedia publishes the media call on the internal plane. Use calls it.
func exposeMedia() {
	cloud.Plane().Post("/ai/media", planeMedia,
		zip.WithOperationID(client.AIMedia),
		zip.WithSummary("One media call, bought for the caller"))
}

// planeMedia buys one media call from the model API for the caller and answers
// with the model API's own status and body. Co-resident zen gets first refusal on
// POST media routes so Zen creative SKUs (iro, koe, uta, oto, koma, mori) are
// served in-process on the stated tenant.
func planeMedia(ctx context.Context, in *client.MediaIn) (*client.Chat, error) {
	for _, m := range media {
		if in.Method == m.method && m.path.MatchString(in.Path) {
			if in.Method == http.MethodPost {
				// A frame request with nothing to render is refused before any
				// default scene is applied or any model is asked (#773).
				if isFramePath(in.Path) {
					if err := frameRequestError(in.Path, decodeFrame(in.Request)); err != nil {
						return nil, err
					}
				}
				return buy(ctx, in.Path, &client.ChatIn{Request: in.Request})
			}
			return serve(ctx, in.Method, in.Path, in.Request, "", nil)
		}
	}
	return nil, zip.ErrBadRequest("ai: not a media route: " + in.Method + " " + in.Path)
}

// isFramePath reports whether path is one of the /v1/frame* routes mounted on
// the media plane (/v1/frame, /v1/frame/generations, /v1/frame/edits, and their
// plural/singular spellings).
func isFramePath(path string) bool {
	return regexp.MustCompile(`^/v1/frames?(/(generations|edits?))?$`).MatchString(path)
}

// decodeFrame reads one frame request body; an unreadable or empty body decodes
// to the zero value so frameRequestError refuses it.
func decodeFrame(body []byte) *aiFrameIn {
	var in aiFrameIn
	if len(body) > 0 {
		_ = json.Unmarshal(body, &in)
	}
	return &in
}
