package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/hanzoai/cloud/internal/magic"
)

// whatsapp_media.go downloads one inbound media object from the Cloud API.
//
// Meta hands a message an id, not the bytes. The bytes are two GETs: the id
// returns a URL, and that URL returns the file. The URL expires in about five
// minutes and is only useful with the same bearer that read it, so it never
// leaves this process and never appears in an error.

// maxWhatsAppMedia is the most one message's media will be buffered. A larger
// file is refused whole rather than truncated: half a document that reads as a
// whole one is worse than being told it did not arrive.
const maxWhatsAppMedia = 16 << 20 // 16 MiB

// whatsappInlineImage is the largest image folded into the turn as a data URL.
// Past it the agent still receives the file's id, name and sniffed type — the
// same facts a Slack message's files carry — and not a prompt the size of the
// file.
const whatsappInlineImage = 256 << 10

// whatsappInlineText is the most of a text document copied into the turn.
const whatsappInlineText = 64 << 10

// whatsappHTTP is the client both media reads use. Tests point the origin at
// an httptest server; the client itself stays this one.
var whatsappHTTP = channelHTTP

// whatsappDownloaded is one fetched file. The fields a Slack message publishes
// on a file (id, name, mimetype, size) are the ones the agent is shown; Kind
// is the envelope's closed set.
type whatsappDownloaded struct {
	ID    string
	Name  string
	MIME  string
	Size  int64
	Kind  string
	Body  []byte
	Text  string // a text document, inlined into the turn
	Image string // a small image, as a data URL
}

// fetchWhatsAppMedia reads one media id: GET /{id} for the URL, then GET that
// URL, both with the org's bearer. filename is the name the message carried,
// which the Graph response does not repeat.
func fetchWhatsAppMedia(ctx context.Context, token, mediaID, filename string) (whatsappDownloaded, error) {
	if token == "" {
		return whatsappDownloaded{}, fmt.Errorf("whatsapp: no token")
	}
	if mediaID == "" || strings.ContainsAny(mediaID, "/?#") {
		return whatsappDownloaded{}, fmt.Errorf("whatsapp: media id is empty")
	}
	meta, err := loadWhatsAppMedia(ctx, token, mediaID)
	if err != nil {
		return whatsappDownloaded{}, err
	}
	if meta.FileSize > maxWhatsAppMedia {
		return whatsappDownloaded{}, fmt.Errorf("whatsapp: media is %d bytes, larger than %d", meta.FileSize, maxWhatsAppMedia)
	}
	if !whatsappMediaURL(meta.URL) {
		// The address is not echoed. A Graph response that pointed somewhere
		// else would spend the bearer there, and the refusal says that without
		// repeating the address.
		return whatsappDownloaded{}, fmt.Errorf("whatsapp: media url is not on a Meta host")
	}
	body, err := whatsappMediaBytes(ctx, token, meta.URL)
	if err != nil {
		return whatsappDownloaded{}, err
	}
	mime := whatsappSniff(body)
	if mime == "" || mime == "application/octet-stream" {
		if meta.MIME != "" {
			mime = meta.MIME
		}
	}
	out := whatsappDownloaded{
		ID: mediaID, Name: filename, MIME: mime, Size: int64(len(body)), Body: body,
	}
	out.Text = whatsappTextBody(mime, body)
	out.Image = whatsappImageURL(mime, body)
	return out, nil
}

type whatsappMediaMeta struct {
	URL      string
	MIME     string
	FileSize int64
}

func loadWhatsAppMedia(ctx context.Context, token, mediaID string) (whatsappMediaMeta, error) {
	endpoint := whatsappOrigin() + "/v21.0/" + url.PathEscape(mediaID)
	var raw struct {
		URL      string `json:"url"`
		MIME     string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	}
	if err := whatsappGet(ctx, token, endpoint, &raw); err != nil {
		return whatsappMediaMeta{}, err
	}
	if raw.URL == "" {
		return whatsappMediaMeta{}, fmt.Errorf("whatsapp: media %s has no download url", mediaID)
	}
	return whatsappMediaMeta{URL: raw.URL, MIME: raw.MIME, FileSize: raw.FileSize}, nil
}

func whatsappMediaBytes(ctx context.Context, token, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: media request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := whatsappHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: media download failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWhatsAppMedia+1))
	if err != nil {
		return nil, fmt.Errorf("whatsapp: media read failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("whatsapp: media download returned %d", resp.StatusCode)
	}
	if len(body) > maxWhatsAppMedia {
		return nil, fmt.Errorf("whatsapp: media is larger than %d bytes", maxWhatsAppMedia)
	}
	return body, nil
}

// whatsappMediaURL reports whether a download address is one the bearer may
// be spent on. The configured origin's own host is allowed so a test server
// is the same rule production uses, rather than a branch that exists only in
// tests. Production answers come from Meta's lookaside hosts.
func whatsappMediaURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	origin, oerr := url.Parse(whatsappOrigin())
	sameOrigin := oerr == nil && origin.Host != "" && strings.EqualFold(u.Host, origin.Host)
	if u.Scheme == "http" && sameOrigin {
		return true
	}
	if u.Scheme != "https" {
		return false
	}
	if sameOrigin {
		return true
	}
	host := strings.ToLower(u.Host)
	return host == "lookaside.fbsbx.com" || strings.HasSuffix(host, ".fbsbx.com") || strings.HasSuffix(host, ".fbcdn.net")
}

// whatsappSniff names a type from the bytes. magic is the allow-list this
// process serves files under; DetectContentType covers the text and container
// types that allow-list deliberately does not name.
func whatsappSniff(body []byte) string {
	if kind := magic.Type(body); kind != "" {
		return kind
	}
	return http.DetectContentType(body)
}

func whatsappTextBody(mime string, body []byte) string {
	if len(body) == 0 || len(body) > whatsappInlineText || !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return ""
	}
	if strings.HasPrefix(mime, "text/") || mime == "application/json" || mime == "application/xml" ||
		strings.HasSuffix(mime, "+json") || strings.HasSuffix(mime, "+xml") {
		return string(body)
	}
	return ""
}

func whatsappImageURL(mime string, body []byte) string {
	if !strings.HasPrefix(mime, "image/") || len(body) == 0 || len(body) > whatsappInlineImage {
		return ""
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(body)
}

// whatsappTurnText is what the agent reads. The file line is a slackFile —
// id, name, mimetype, size — which is the attachment a Slack message carries.
// A text document follows in full, and a small image follows as a data URL.
func whatsappTurnText(said string, d *whatsappDownloaded) string {
	if d == nil {
		return said
	}
	file := slackFile{ID: d.ID, Name: d.Name, Mimetype: d.MIME, Size: d.Size}
	raw, err := json.Marshal(file)
	if err != nil {
		return said
	}
	var b strings.Builder
	b.WriteString(said)
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString("attachment: ")
	b.Write(raw)
	if d.Text != "" {
		b.WriteString("\n")
		b.WriteString(d.Text)
	}
	if d.Image != "" {
		b.WriteString("\n")
		b.WriteString(d.Image)
	}
	return b.String()
}

func whatsappGet(ctx context.Context, token, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("whatsapp: request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := whatsappHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp: request failed")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return whatsappStatus(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("whatsapp: unreadable response")
	}
	return nil
}
