package dataroom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud/clients/gojabase"
)

// ingest.go is the in-process ingestion seam: it lets a sibling subsystem (Hanzo
// Company's formation + import + fundraising flows) push a document into a tenant's
// data room WITHOUT an HTTP hop, reusing the SAME object-storage + bundle path the
// /v1/dataroom/documents upload handler uses (uploadDocument). The bytes go to the
// VFS blob store under the tenant-segmented key; the metadata row is recorded via
// the bundle's documents.create route in one transaction. It is org-scoped: the
// caller MUST pass an already-validated org (the mounted host isolates per tenant).

// ErrNotMounted is returned when dataroom has not been mounted (no host/blob). A
// caller treats it as "data room unavailable".
var ErrNotMounted = errors.New("dataroom: subsystem not mounted")

// Ingest stores document bytes for org and records the metadata row, returning the
// new dataroom document id. It mirrors uploadDocument exactly (VFS.Put +
// documents.create) so the in-proc path and the HTTP path can never diverge.
func Ingest(ctx context.Context, org, name, contentType string, data []byte) (string, error) {
	if mounted == nil || mounted.State.host == nil || mounted.State.blob == nil {
		return "", ErrNotMounted
	}
	if org == "" {
		return "", fmt.Errorf("dataroom.Ingest: empty org")
	}
	if len(data) == 0 {
		return "", fmt.Errorf("dataroom.Ingest: empty document")
	}
	if len(data) > maxUpload {
		return "", fmt.Errorf("dataroom.Ingest: document too large")
	}
	if name == "" {
		name = "document"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	rk, err := randKey()
	if err != nil {
		return "", fmt.Errorf("dataroom.Ingest: storage key: %w", err)
	}
	key := "dataroom/" + gojabase.TenantSegment(org) + "/" + rk
	if err := mounted.State.blob.Put(ctx, key, data); err != nil {
		return "", fmt.Errorf("dataroom.Ingest: blob put: %w", err)
	}
	resp, err := mounted.State.host.Dispatch(ctx, org, gojabase.Request{
		Route: "documents.create",
		Body:  map[string]any{"name": name, "fileKey": key, "contentType": contentType, "fileSize": len(data)},
	})
	if err != nil {
		return "", fmt.Errorf("dataroom.Ingest: dispatch: %w", err)
	}
	if resp.Status != http.StatusOK {
		return "", fmt.Errorf("dataroom.Ingest: documents.create returned %d: %s", resp.Status, string(resp.Body))
	}
	var out struct {
		Document struct {
			ID string `json:"id"`
		} `json:"document"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil || out.Document.ID == "" {
		return "", fmt.Errorf("dataroom.Ingest: malformed documents.create response: %s", string(resp.Body))
	}
	return out.Document.ID, nil
}
