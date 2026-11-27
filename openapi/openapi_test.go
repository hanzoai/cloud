package openapi

import (
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/sdk/spec"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func newApp() *zip.App {
	return zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
}

// Mount serves the document off the app it is registered on, and the document
// includes its OWN route — proof the lazy build sees the final table.
func TestMountServesLiveSpecIncludingItself(t *testing.T) {
	app := newApp()
	app.Raw(http.MethodGet, "/v1/kms/health", func(c *zip.Ctx) error { return c.JSON(200, "ok") })
	Use(app, spec.Info{Title: "Hanzo Cloud", Version: "v1"})

	doc, err := spec.Of(app, spec.Info{Title: "Hanzo Cloud", Version: "v1"})
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if _, ok := doc.Paths[Path]; !ok {
		t.Errorf("document omits its own endpoint %s; have %v", Path, doc.Paths)
	}
	if _, ok := doc.Paths["/v1/kms/health"]; !ok {
		t.Errorf("document omits /v1/kms/health")
	}
}

// The fold is the whole point of two projections: a typed op keeps the shape the
// router proves (address, product tag) and GAINS everything only its Go types
// know — schema, responses, query parameters — while the raw route beside it is
// untouched. Both are in the one document.
type secretIn struct {
	Org   string `json:"org"`
	Limit int    `json:"limit"`
}
type secretOut struct {
	Names []string `json:"names"`
}
