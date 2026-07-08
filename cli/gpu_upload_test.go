package cli

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestUploadOutputsIntegration exercises the real BYO-GPU result-upload path:
// fetchLocalOutput pulls a finished render from the LOCAL studio's /view and
// postGalleryOutput POSTs it to the org studio's /upload/output with the user's
// IAM token, landing it in orgs/{org}/output (the gallery, S3-mirrored).
//
// It is a live integration test, skipped unless the box is wired for it:
//   HANZO_TOKEN=<iam bearer> \
//   HANZO_UPLOAD_IT_FILE=<name of a file the local studio serves at /view?type=output> \
//   HANZO_STUDIO_UPLOAD_URL=<org studio base, e.g. https://studio.hanzo.ai> \
//   go test ./cli -run TestUploadOutputsIntegration -v
// The local studio is assumed at localComfyUI (127.0.0.1:8188).
func TestUploadOutputsIntegration(t *testing.T) {
	tok := os.Getenv("HANZO_TOKEN")
	file := os.Getenv("HANZO_UPLOAD_IT_FILE")
	if tok == "" || file == "" {
		t.Skip("set HANZO_TOKEN and HANZO_UPLOAD_IT_FILE to run the live upload integration test")
	}
	uploadURL := firstNonEmpty(os.Getenv("HANZO_STUDIO_UPLOAD_URL"), defaultStudioUploadURL)

	w := &worker{
		env:             &Env{}, // ensureToken reads HANZO_TOKEN first, so no creds needed
		http:            &http.Client{Timeout: 60 * time.Second},
		studioUploadURL: uploadURL,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	subfolder := os.Getenv("HANZO_UPLOAD_IT_SUBFOLDER") // "" == top of output
	out := file
	if subfolder != "" {
		out = subfolder + "/" + file
	}

	gallery, err := w.uploadOutputs(ctx, []string{out}, uploadURL)
	if err != nil {
		t.Fatalf("uploadOutputs(%q -> %s): %v", out, uploadURL, err)
	}
	if len(gallery) != 1 {
		t.Fatalf("expected 1 gallery path, got %d: %v", len(gallery), gallery)
	}
	t.Logf("uploaded %q -> %s gallery path %q", out, uploadURL, gallery[0])
}
