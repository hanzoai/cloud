package ai

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// TestAnEmptyFrameRequestIsRefusedBeforeAnyModel: #773. `{}` on
// /v1/frame/generations answered 200 and generated (and billed) an iro frame;
// an edit with no composite to edit was forwarded too. Both are a 400 that names
// the missing field, and no model is asked.
func TestAnEmptyFrameRequestIsRefusedBeforeAnyModel(t *testing.T) {
	o := callOps{serve: func(*fasthttp.Request) (*fasthttp.Response, error) {
		t.Fatal("the frame service was called for a request with nothing to render")
		return nil, nil
	}}
	cases := []struct {
		name string
		run  func() error
		want string
	}{
		{"alias nil", func() error { _, err := o.frameAlias(context.Background(), nil); return err }, "prompt"},
		{"alias {}", func() error { _, err := o.frameAlias(context.Background(), &aiFrameIn{}); return err }, "prompt"},
		{"generate nil", func() error { _, err := o.frameGenerate(context.Background(), nil); return err }, "prompt"},
		{"generate {}", func() error { _, err := o.frameGenerate(context.Background(), &aiFrameIn{}); return err }, "prompt"},
		{"generate blank prompt", func() error {
			_, err := o.frameGenerate(context.Background(), &aiFrameIn{Prompt: "   ", Model: "iro"})
			return err
		}, "prompt"},
		{"edit {}", func() error { _, err := o.frameEdit(context.Background(), &aiFrameIn{}); return err }, "background_b64"},
		{"edit instruction only", func() error {
			_, err := o.frameEdit(context.Background(), &aiFrameIn{Instruction: "make it dark"})
			return err
		}, "background_b64"},
		{"edit source, no change", func() error {
			_, err := o.frameEdit(context.Background(), &aiFrameIn{BackgroundB64: "aGk="})
			return err
		}, "nothing to change"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.run()
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
