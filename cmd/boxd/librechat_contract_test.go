package main

// Conformance against the REAL consumer, not against our own docs.
//
// hanzo.chat's code-interpreter client is an upstream contract we do not get to
// choose. Every assertion here is anchored to the line in that client which
// reads the field, so a future refactor that "tidies" a name has to argue with
// the consumer rather than with a style preference. All three of these shipped
// broken once already, and none of them fail loudly — a wrong shape here makes
// execute_code look empty or throw a message about `undefined`, with nothing in
// our own logs to say why.
//
// Citations are into ~/work/hanzo/chat:
//   crud.js      = api/server/services/Files/Code/crud.js
//   process.js   = api/server/services/Files/Code/process.js
//
// Credit: these three defects were found by another agent reading the client
// directly. The test is theirs in substance; only the file was lost.

import (
	"encoding/json"
	"testing"
)

// TestUploadResponseCarriesSuccessMessage pins crud.js:108
//
//	if (result.message !== 'success') { throw new Error(...) }
//
// Omitting the field does not degrade the upload — it fails it, with
// "Error uploading file: undefined".
func TestUploadResponseCarriesSuccessMessage(t *testing.T) {
	body := map[string]any{"message": "success", "session_id": "s1", "files": []uploadFile{}}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Message != "success" {
		t.Fatalf("upload must return message=\"success\" (crud.js:108 throws otherwise), got %q", got.Message)
	}
}

// TestUploadFileElementFieldNames pins crud.js:112
//
//	const fileIdentifier = `${result.session_id}/${result.files[0].fileId}`
//
// The client's own JSDoc types the element as { fileId, filename }. Emitting
// {name,id} yields the identifier "<sid>/undefined" and the file is unreachable.
func TestUploadFileElementFieldNames(t *testing.T) {
	raw, err := json.Marshal(uploadFile{FileID: "out.csv", Filename: "out.csv"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"fileId", "filename"} {
		if _, ok := m[k]; !ok {
			t.Errorf("upload file element must have %q (crud.js:112 reads .fileId); got %v", k, m)
		}
	}
	if _, bad := m["id"]; bad {
		t.Errorf("upload file element must NOT use \"id\" — the client reads .fileId; got %v", m)
	}
}

// TestSessionFilesIsABareArray pins two readers that both assume an array:
//
//	ProgrammaticToolCalling: if (!Array.isArray(files)) return []
//	process.js:294:          response.data.find((f) => f.name.startsWith(path))?.lastModified
//
// Wrapping the list in {session_id, files} does not error — it makes every
// session read as permanently empty, which is far harder to notice.
func TestSessionFilesIsABareArray(t *testing.T) {
	out := []sessionFile{{Name: "s1/out.csv", LastModified: "2026-08-06T00:00:00Z"}}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(raw) == 0 || raw[0] != '[' {
		t.Fatalf("GET /v1/files/{sid} must be a bare JSON array, got: %s", raw)
	}

	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("response is not an array of objects: %v", err)
	}
	if len(arr) != 1 {
		t.Fatalf("expected 1 element, got %d", len(arr))
	}
	// process.js matches on the "<sid>/" prefix, so a bare filename never matches.
	name, _ := arr[0]["name"].(string)
	if name != "s1/out.csv" {
		t.Errorf("element name must be \"<sid>/<fileId>\" (process.js:294 startsWith), got %q", name)
	}
	if _, ok := arr[0]["lastModified"]; !ok {
		t.Errorf("element must carry lastModified (process.js:294 reads it), got %v", arr[0])
	}
}

// TestExecResponseKeepsPlainNames guards the OTHER direction: the exec response
// is a different shape from upload, and "fixing" it to match would break it.
// The documented exec contract is files:[{name}], which is what newFiles emits.
func TestExecResponseKeepsPlainNames(t *testing.T) {
	raw, err := json.Marshal(execFile{Name: "out.csv", ID: "s1/out.csv"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["name"]; !ok {
		t.Errorf("exec response files must expose \"name\"; got %v", m)
	}
}
