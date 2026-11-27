// Copyright © 2026 Hanzo AI. MIT License.

package ai

// media_validate_test.go drives `{}` through planeMedia — the media_rpc path
// that prod mounts for /v1/frame/generations and /v1/frame/edits — and expects
// a 400 before any default scene is applied or any model is called (#773).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// planeMediaRefused drives a raw `{}` media request for the given path through
// planeMedia and returns the error it answers, if any.
func planeMediaRefused(t *testing.T, path string) error {
	t.Helper()
	body, _ := json.Marshal(map[string]string{})
	out, err := planeMedia(context.Background(), &client.MediaIn{
		Method:  http.MethodPost,
		Path:    path,
		Request: body,
	})
	if err != nil {
		return err
	}
	if out.Status == http.StatusBadRequest {
		return zip.ErrBadRequest(string(out.Body))
	}
	if out.Status != http.StatusOK {
		return zip.Errorf(out.Status, "%s", string(out.Body))
	}
	return nil
}

func TestAnEmptyFrameMediaRequestIsRefusedBeforeAnyModel(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"alias {}", "/v1/frame", "prompt"},
		{"generations {}", "/v1/frame/generations", "prompt"},
		{"edits {}", "/v1/frame/edits", "background_b64"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := planeMediaRefused(t, c.path)
			var he *zip.HTTPError
			if !errors.As(err, &he) || he.Status != http.StatusBadRequest {
				t.Fatalf("answered %v, want 400", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("the refusal %q does not name %q", err, c.want)
			}
		})
	}
}
