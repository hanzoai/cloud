package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchWhatsAppMediaDownloadsWithTheBearerAndSniffs(t *testing.T) {
	var sawMetaAuth, sawFileAuth string
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v21.0/MEDIA":
			sawMetaAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"url":"` + srv.URL + `/file","mime_type":"text/plain","file_size":12}`))
		case "/file":
			sawFileAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(png)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("WHATSAPP_API_BASE", srv.URL)

	got, err := fetchWhatsAppMedia(context.Background(), "tok-1", "MEDIA", "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	if sawMetaAuth != "Bearer tok-1" || sawFileAuth != "Bearer tok-1" {
		t.Fatalf("auth meta=%q file=%q", sawMetaAuth, sawFileAuth)
	}
	if got.MIME != "image/png" {
		t.Fatalf("mime = %q, want the sniffed image/png rather than the claimed text/plain", got.MIME)
	}
	if got.Name != "shot.png" || got.ID != "MEDIA" || got.Size != int64(len(png)) {
		t.Fatalf("file = %+v", got)
	}
	if got.Image == "" || !strings.HasPrefix(got.Image, "data:image/png;base64,") {
		t.Fatalf("image was not folded into a data url: %q", got.Image)
	}
	if strings.Contains(got.Image, "tok-1") || strings.Contains(errString(err), srv.URL) {
		t.Fatal("the bearer or the download url leaked into the result")
	}
}

func TestFetchWhatsAppMediaRefusesAForeignURLAndAnOversizeFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v21.0/OFF":
			_, _ = w.Write([]byte(`{"url":"https://evil.example/steal","mime_type":"image/png"}`))
		case "/v21.0/BIG":
			_, _ = w.Write([]byte(`{"url":"https://lookaside.fbsbx.com/x","file_size":999999999}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("WHATSAPP_API_BASE", srv.URL)

	if _, err := fetchWhatsAppMedia(context.Background(), "tok", "OFF", ""); err == nil || !strings.Contains(err.Error(), "not on a Meta host") {
		t.Fatalf("foreign url: %v", err)
	}
	if _, err := fetchWhatsAppMedia(context.Background(), "tok", "BIG", ""); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("oversize: %v", err)
	}
	if _, err := fetchWhatsAppMedia(context.Background(), "tok", "a/b", ""); err == nil {
		t.Fatal("a media id with a slash was fetched")
	}
}

func TestWhatsAppMediaURLAllowsTheOriginAndMetaOnly(t *testing.T) {
	t.Setenv("WHATSAPP_API_BASE", "http://127.0.0.1:9")
	if !whatsappMediaURL("http://127.0.0.1:9/file") {
		t.Fatal("the configured origin was refused")
	}
	if whatsappMediaURL("http://evil.example/file") {
		t.Fatal("a foreign http url was allowed")
	}
	if !whatsappMediaURL("https://lookaside.fbsbx.com/whatsapp_business/attachments/x") {
		t.Fatal("Meta's lookaside host was refused")
	}
	if whatsappMediaURL("https://evil.example/file") {
		t.Fatal("a foreign https url was allowed")
	}
}

func TestPostWhatsAppAddressesAGroup(t *testing.T) {
	var got struct {
		recipient, to, auth string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth = r.Header.Get("Authorization")
		var body struct {
			Recipient string `json:"recipient_type"`
			To        string `json:"to"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got.recipient, got.to = body.Recipient, body.To
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.out"}]}`))
	}))
	defer srv.Close()
	t.Setenv("WHATSAPP_API_BASE", srv.URL)

	id, err := postWhatsApp(context.Background(), "tok", "PN1", "GRP", "wamid.in", "hi", true)
	if err != nil {
		t.Fatal(err)
	}
	if id != "wamid.out" || got.recipient != "group" || got.to != "GRP" || got.auth != "Bearer tok" {
		t.Fatalf("send id=%s recipient=%s to=%s auth=%s", id, got.recipient, got.to, got.auth)
	}

	if _, err := postWhatsApp(context.Background(), "tok", "PN1", "1555", "", "hi", false); err != nil {
		t.Fatal(err)
	}
	if got.recipient != "individual" || got.to != "1555" {
		t.Fatalf("direct = %s to %s", got.recipient, got.to)
	}
}

func TestCreateWhatsAppGroupAndInviteLink(t *testing.T) {
	var created struct{ subject, description string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v21.0/PN1/groups":
			var body struct {
				Subject     string `json:"subject"`
				Description string `json:"description"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			created.subject, created.description = body.Subject, body.Description
			_, _ = w.Write([]byte(`{"id":"GRP"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v21.0/GRP/invite_link":
			_, _ = w.Write([]byte(`{"invite_link":"https://chat.whatsapp.com/JOIN"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("WHATSAPP_API_BASE", srv.URL)

	id, err := createWhatsAppGroup(context.Background(), "tok", "PN1", "Feedback", "from the group")
	if err != nil {
		t.Fatal(err)
	}
	if id != "GRP" || created.subject != "Feedback" || created.description != "from the group" {
		t.Fatalf("created id=%s subject=%q description=%q", id, created.subject, created.description)
	}
	link, err := whatsappInviteLink(context.Background(), "tok", "GRP")
	if err != nil || link != "https://chat.whatsapp.com/JOIN" {
		t.Fatalf("link = %q (%v)", link, err)
	}
	if _, err := createWhatsAppGroup(context.Background(), "tok", "PN1", "  ", ""); err == nil {
		t.Fatal("an empty subject created a group")
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
