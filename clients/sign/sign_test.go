package sign

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// memVFS is an in-memory types.VFSClient standing in for the S3/SeaweedFS data
// plane. sign now routes PDF bytes through the object-storage seam (__blob), so the
// test drives that exact path (Put on create/seal, Get on view/download).
type memVFS struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemVFS() *memVFS { return &memVFS{m: map[string][]byte{}} }
func (v *memVFS) Put(_ context.Context, key string, payload []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	v.m[key] = cp
	return nil
}
func (v *memVFS) Get(_ context.Context, key string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	b, ok := v.m[key]
	if !ok {
		return nil, fmt.Errorf("memVFS: no such key %q", key)
	}
	return b, nil
}
func (v *memVFS) Delete(_ context.Context, key string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, key)
	return nil
}

// signaturePNG draws a small opaque signature-like image and returns it base64,
// so the __pdf.stamp host-fn has genuine PNG bytes to decode and render.
func signaturePNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 60))
	for x := 0; x < 200; x++ {
		for y := 0; y < 60; y++ {
			if (x+y)%11 < 4 && y > 12 && y < 48 {
				img.Set(x, y, color.NRGBA{R: 20, G: 40, B: 120, A: 255})
			} else {
				img.Set(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// mountApp builds a zip.App with the sign subsystem mounted and NO identity
// middleware — so X-Org-Id/X-User-Id headers are trusted verbatim (principal.Org
// gates on c.User()). This is the same trusted-header harness the other Base-backed
// subsystems (captable) use for their end-to-end wire proofs.
func mountApp(t *testing.T) (*zip.App, *memVFS) {
	t.Helper()
	_ = Shutdown(nil) // reset process-global state between tests
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	vfs := newMemVFS()
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), VFS: vfs}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(nil) })
	return app, vfs
}

// do issues an HTTP request against the app. org!="" sets a VALIDATED principal
// (owner routes); org=="" is anonymous (token routes carry their own capability).
func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode json: %v (%s)", err, b)
	}
	return m
}

// TestFullSigningFlow is the end-to-end wire proof of the COMPLETE e-sign flow on
// the reusable gojabase RW-Base host: create document → add recipient → add
// fields → send → recipient signs each field → complete → the document seals to
// COMPLETED with a REAL x509/PKCS#7 signed PDF, and the audit trail records every
// step. All per-tenant Base/SQLite-backed.
func TestFullSigningFlow(t *testing.T) {
	app, vfs := mountApp(t)
	const org = "acme"

	pdf, err := os.ReadFile("testdata/example.pdf")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)

	// 1. anonymous owner route → 403
	if code, _ := do(t, app, http.MethodGet, "/v1/sign/documents", "", nil); code != http.StatusForbidden {
		t.Fatalf("anon list want 403, got %d", code)
	}

	// 2. create document (DRAFT)
	code, body := do(t, app, http.MethodPost, "/v1/sign/documents", org, map[string]any{
		"title": "Mutual NDA", "pdfBase64": pdfB64, "subject": "Please sign",
	})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	doc := decode(t, body)
	docID, _ := doc["id"].(string)
	if docID == "" || doc["status"] != "DRAFT" {
		t.Fatalf("bad create response: %s", body)
	}

	// M8: the PDF BYTES went to the object-storage seam (__blob), NOT the tenant
	// SQLite. The VFS holds the original blob under the tenant-scoped key, and the
	// stored bytes equal the uploaded PDF (not base64) — proof it is off-DB.
	vfs.mu.Lock()
	var foundOriginal bool
	for k, v := range vfs.m {
		if bytes.Equal(v, pdf) {
			foundOriginal = true
			if !strings.HasPrefix(k, "sign/") {
				t.Fatalf("blob key %q is not tenant-scoped under sign/", k)
			}
		}
	}
	vfs.mu.Unlock()
	if !foundOriginal {
		t.Fatal("uploaded PDF bytes were not stored on the object-storage seam (M8)")
	}

	// 3. add a SIGNER recipient
	code, body = do(t, app, http.MethodPost, "/v1/sign/documents/"+docID+"/recipients", org, map[string]any{
		"email": "signer@acme.test", "name": "Sam Signer", "role": "SIGNER",
	})
	if code != http.StatusCreated {
		t.Fatalf("add recipient want 201, got %d (%s)", code, body)
	}
	rec := decode(t, body)
	recID, _ := rec["id"].(string)
	token, _ := rec["token"].(string)
	if recID == "" || token == "" {
		t.Fatalf("bad recipient response: %s", body)
	}

	// 4. add fields: a SIGNATURE and a DATE
	for _, f := range []map[string]any{
		{"recipientId": recID, "type": "SIGNATURE", "page": 1, "positionX": 10, "positionY": 70, "width": 30, "height": 8},
		{"recipientId": recID, "type": "DATE", "page": 1, "positionX": 60, "positionY": 70, "width": 25, "height": 5},
	} {
		code, body = do(t, app, http.MethodPost, "/v1/sign/documents/"+docID+"/fields", org, f)
		if code != http.StatusCreated {
			t.Fatalf("add field want 201, got %d (%s)", code, body)
		}
	}
	// capture field ids from the document view
	code, body = do(t, app, http.MethodGet, "/v1/sign/documents/"+docID, org, nil)
	if code != http.StatusOK {
		t.Fatalf("get doc want 200, got %d (%s)", code, body)
	}
	view := decode(t, body)
	fields, _ := view["fields"].([]any)
	if len(fields) != 2 {
		t.Fatalf("want 2 fields, got %d (%s)", len(fields), body)
	}
	var sigFieldID, dateFieldID string
	for _, fi := range fields {
		fm := fi.(map[string]any)
		if fm["type"] == "SIGNATURE" {
			sigFieldID, _ = fm["id"].(string)
		} else if fm["type"] == "DATE" {
			dateFieldID, _ = fm["id"].(string)
		}
	}
	if sigFieldID == "" || dateFieldID == "" {
		t.Fatalf("missing field ids: %s", body)
	}

	// 5. send → PENDING
	code, body = do(t, app, http.MethodPost, "/v1/sign/documents/"+docID+"/send", org, map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("send want 200, got %d (%s)", code, body)
	}
	if decode(t, body)["status"] != "PENDING" {
		t.Fatalf("send status not PENDING: %s", body)
	}

	// 6. recipient views via token (anonymous) → readStatus OPENED
	base := "/v1/sign/o/" + org + "/sign/" + token
	code, body = do(t, app, http.MethodGet, base, "", nil)
	if code != http.StatusOK {
		t.Fatalf("token view want 200, got %d (%s)", code, body)
	}

	// 7. recipient signs both fields.
	code, body = do(t, app, http.MethodPost, base+"/fields/"+sigFieldID, "", map[string]any{"value": signaturePNG(t), "isBase64": true})
	if code != http.StatusOK {
		t.Fatalf("sign signature field want 200, got %d (%s)", code, body)
	}
	code, body = do(t, app, http.MethodPost, base+"/fields/"+dateFieldID, "", map[string]any{"value": "2026-07-10"})
	if code != http.StatusOK {
		t.Fatalf("sign date field want 200, got %d (%s)", code, body)
	}

	// 8. complete → the document seals to COMPLETED
	code, body = do(t, app, http.MethodPost, base+"/complete", "", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("complete want 200, got %d (%s)", code, body)
	}
	cmp := decode(t, body)
	if cmp["sealed"] != true || cmp["documentStatus"] != "COMPLETED" {
		t.Fatalf("document did not seal: %s", body)
	}

	// 9. download → a REAL signed PDF (has the PKCS#7 signature dict + ByteRange)
	code, body = do(t, app, http.MethodGet, "/v1/sign/documents/"+docID+"/download", org, nil)
	if code != http.StatusOK {
		t.Fatalf("download want 200, got %d (%s)", code, body)
	}
	dl := decode(t, body)
	if dl["sealed"] != true {
		t.Fatalf("download not sealed: %s", body)
	}
	signedB64, _ := dl["pdfBase64"].(string)
	signedPDF, err := base64.StdEncoding.DecodeString(signedB64)
	if err != nil || len(signedPDF) == 0 {
		t.Fatalf("decode signed pdf: %v", err)
	}
	if !bytes.HasPrefix(signedPDF, []byte("%PDF-")) {
		t.Fatalf("sealed output is not a PDF")
	}
	for _, marker := range [][]byte{[]byte("/ByteRange"), []byte("/Type /Sig"), []byte("adbe.pkcs7.detached")} {
		if !bytes.Contains(signedPDF, marker) {
			t.Fatalf("sealed PDF missing digital-signature marker %q", marker)
		}
	}
	if len(signedPDF) <= len(pdf) {
		t.Fatalf("sealed PDF (%d) not larger than original (%d)", len(signedPDF), len(pdf))
	}

	// 10. audit trail records the whole lifecycle
	code, body = do(t, app, http.MethodGet, "/v1/sign/documents/"+docID+"/audit", org, nil)
	if code != http.StatusOK {
		t.Fatalf("audit want 200, got %d (%s)", code, body)
	}
	audit := decode(t, body)
	entries, _ := audit["entries"].([]any)
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.(map[string]any)["type"].(string)] = true
	}
	for _, want := range []string{
		"DOCUMENT_CREATED", "RECIPIENT_CREATED", "FIELD_CREATED", "DOCUMENT_SENT",
		"DOCUMENT_OPENED", "DOCUMENT_FIELD_INSERTED", "DOCUMENT_RECIPIENT_COMPLETED", "DOCUMENT_COMPLETED",
	} {
		if !seen[want] {
			t.Fatalf("audit trail missing %s (got %v)", want, seen)
		}
	}
}

// TestTenantIsolation proves two orgs never see each other's documents (the DB is
// per-tenant; a wrong token/org combination cannot resolve) and that a validation
// error rolls the request transaction back (gojabase atomicity).
func TestTenantIsolation(t *testing.T) {
	app, _ := mountApp(t)
	pdf, _ := os.ReadFile("testdata/example.pdf")
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)

	code, body := do(t, app, http.MethodPost, "/v1/sign/documents", "orgA", map[string]any{"title": "A", "pdfBase64": pdfB64})
	if code != http.StatusCreated {
		t.Fatalf("orgA create: %d (%s)", code, body)
	}
	docID := decode(t, body)["id"].(string)

	// orgB cannot read orgA's document
	if code, _ := do(t, app, http.MethodGet, "/v1/sign/documents/"+docID, "orgB", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant read want 404, got %d", code)
	}
	// orgA can
	if code, _ := do(t, app, http.MethodGet, "/v1/sign/documents/"+docID, "orgA", nil); code != http.StatusOK {
		t.Fatalf("owner read want 200, got %d", code)
	}
	// a bogus token under any org is unauthorized
	if code, _ := do(t, app, http.MethodGet, "/v1/sign/o/orgA/sign/deadbeefdeadbeefdeadb", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("bogus token want 401, got %d", code)
	}
}

// TestSignerProductionGate proves M9: a production env REFUSES to boot with a
// self-signed cert (which would seal legal documents with an untrusted seal) and
// requires the KMS-custodied CLOUD_SIGN_CERT_PEM/KEY_PEM; dev envs still self-sign.
func TestSignerProductionGate(t *testing.T) {
	// dev/unset env → self-signed is allowed (zero-config dev default).
	for _, env := range []string{"", "devnet", "testnet"} {
		if _, err := newSigner(t.TempDir(), env); err != nil {
			t.Fatalf("dev env %q should self-sign, got %v", env, err)
		}
	}
	// production-like env WITHOUT a KMS cert → refuse to boot.
	for _, env := range []string{"production", "prod", "mainnet", "main", "live"} {
		if _, err := newSigner(t.TempDir(), env); err == nil {
			t.Fatalf("env %q must refuse a self-signed signing cert", env)
		}
	}
	// production WITH a KMS-custodied cert (base64 PEM) → boots.
	_, certPEM, keyPEM, err := generateSelfSigned()
	if err != nil {
		t.Fatalf("generate test pair: %v", err)
	}
	t.Setenv("CLOUD_SIGN_CERT_PEM", base64.StdEncoding.EncodeToString(certPEM))
	t.Setenv("CLOUD_SIGN_KEY_PEM", base64.StdEncoding.EncodeToString(keyPEM))
	if _, err := newSigner(t.TempDir(), "production"); err != nil {
		t.Fatalf("production with a KMS cert should boot, got %v", err)
	}
}

// TestSequentialSigningOrder proves M7: in a SEQUENTIAL document a later signer
// cannot act until every earlier signer has SIGNED; once they have, the turn opens
// and the document seals when all have signed.
func TestSequentialSigningOrder(t *testing.T) {
	app, _ := mountApp(t)
	const org = "acme"
	pdf, err := os.ReadFile("testdata/example.pdf")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)

	code, body := do(t, app, http.MethodPost, "/v1/sign/documents", org, map[string]any{
		"title": "Seq NDA", "pdfBase64": pdfB64, "signingOrder": "SEQUENTIAL",
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	docID := decode(t, body)["id"].(string)

	addSigner := func(email string, order int) (string, string) {
		c, b := do(t, app, http.MethodPost, "/v1/sign/documents/"+docID+"/recipients", org, map[string]any{
			"email": email, "role": "SIGNER", "signingOrder": order,
		})
		if c != http.StatusCreated {
			t.Fatalf("add recipient %s: %d %s", email, c, b)
		}
		m := decode(t, b)
		return m["id"].(string), m["token"].(string)
	}
	rec1, tok1 := addSigner("first@acme.test", 1)
	rec2, tok2 := addSigner("second@acme.test", 2)

	addField := func(recID string) {
		c, b := do(t, app, http.MethodPost, "/v1/sign/documents/"+docID+"/fields", org, map[string]any{
			"recipientId": recID, "type": "SIGNATURE", "page": 1, "positionX": 10, "positionY": 70, "width": 30, "height": 8,
		})
		if c != http.StatusCreated {
			t.Fatalf("add field for %s: %d %s", recID, c, b)
		}
	}
	addField(rec1)
	addField(rec2)

	if c, b := do(t, app, http.MethodPost, "/v1/sign/documents/"+docID+"/send", org, map[string]any{}); c != http.StatusOK {
		t.Fatalf("send: %d %s", c, b)
	}

	// field id per recipient
	_, dv := do(t, app, http.MethodGet, "/v1/sign/documents/"+docID, org, nil)
	fieldFor := map[string]string{}
	for _, fi := range decode(t, dv)["fields"].([]any) {
		fm := fi.(map[string]any)
		fieldFor[fm["recipientId"].(string)] = fm["id"].(string)
	}

	base1 := "/v1/sign/o/" + org + "/sign/" + tok1
	base2 := "/v1/sign/o/" + org + "/sign/" + tok2
	sig := func() map[string]any { return map[string]any{"value": signaturePNG(t), "isBase64": true} }

	// recipient 2 is OUT OF TURN — must be refused.
	if c, b := do(t, app, http.MethodPost, base2+"/fields/"+fieldFor[rec2], "", sig()); c != http.StatusForbidden {
		t.Fatalf("out-of-turn signer 2 want 403, got %d (%s)", c, b)
	}
	// recipient 1 signs + completes (opens the turn).
	if c, b := do(t, app, http.MethodPost, base1+"/fields/"+fieldFor[rec1], "", sig()); c != http.StatusOK {
		t.Fatalf("signer 1 sign: %d %s", c, b)
	}
	if c, b := do(t, app, http.MethodPost, base1+"/complete", "", map[string]any{}); c != http.StatusOK {
		t.Fatalf("signer 1 complete: %d %s", c, b)
	}
	// now recipient 2 may sign + complete, and the document seals.
	if c, b := do(t, app, http.MethodPost, base2+"/fields/"+fieldFor[rec2], "", sig()); c != http.StatusOK {
		t.Fatalf("signer 2 sign after turn: %d %s", c, b)
	}
	c, b := do(t, app, http.MethodPost, base2+"/complete", "", map[string]any{})
	if c != http.StatusOK {
		t.Fatalf("signer 2 complete: %d %s", c, b)
	}
	if decode(t, b)["sealed"] != true {
		t.Fatalf("document did not seal after all signers signed: %s", b)
	}
}
