package prefs

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/zap-proto/zip"
)

// decodePatch validates an inbound PATCH body and returns it as a key-wise patch.
//
// It is deliberately strict about SHAPE and deliberately ignorant of MEANING: the
// body must be a JSON OBJECT within the size and key bounds, but the server never
// interprets what a preference means. That is what lets a surface add a new key
// without a cloud release — and it is safe precisely because this plane holds no
// secrets and is readable only by its own owner.
//
// A null VALUE is meaningful and preserved: it is how a client deletes a key
// (see mergeDoc). A null BODY is not a patch and is refused.
func decodePatch(body []byte, maxBytes, maxKeys int) (map[string]any, error) {
	if len(body) == 0 {
		return nil, zip.ErrBadRequest("a JSON object body is required")
	}
	if len(body) > maxBytes {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge,
			"preferences patch exceeds %d bytes", maxBytes)
	}
	var patch map[string]any
	if err := json.Unmarshal(body, &patch); err != nil {
		return nil, zip.ErrBadRequest("body must be a JSON object: " + err.Error())
	}
	if patch == nil {
		// `null` unmarshals into a nil map without error — an explicit refusal
		// beats silently treating it as an empty patch.
		return nil, zip.ErrBadRequest("body must be a JSON object, not null")
	}
	if len(patch) > maxKeys {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge,
			"preferences patch exceeds %d keys", maxKeys)
	}
	return patch, nil
}

// mergeDoc applies a SHALLOW key-wise merge of patch onto the stored document.
//
// Shallow, not deep: a preference value is a scalar or a small opaque blob the
// client owns wholesale, so a deep merge would make it impossible to REPLACE a
// nested object — the client could only ever add keys to it. Shallow keeps
// "set this key to exactly this value" expressible, which is what a preference is.
//
// A null value DELETES its key. Without that, a key could be set but never
// cleared, and every client would have to invent its own "unset" sentinel.
//
// A corrupt stored document is treated as empty rather than failing the write:
// preferences are not a system of record, and refusing to save a theme forever
// because one row got mangled is worse than starting that user fresh.
func mergeDoc(stored string, patch map[string]any) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(stored), &doc); err != nil || doc == nil {
		doc = map[string]any{}
	}
	for k, v := range patch {
		if v == nil {
			delete(doc, k)
			continue
		}
		doc[k] = v
	}
	if len(doc) > maxKeys {
		return "", zip.Errorf(http.StatusRequestEntityTooLarge,
			"preferences document exceeds %d keys", maxKeys)
	}
	merged, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("encode prefs: %w", err)
	}
	if len(merged) > maxDoc {
		return "", zip.Errorf(http.StatusRequestEntityTooLarge,
			"preferences document exceeds %d bytes", maxDoc)
	}
	return string(merged), nil
}
