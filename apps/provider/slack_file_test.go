package provider

// slack_file_test.go covers reading a message's files and then reading one
// file's bytes. The assertions that matter are the two custody ones: the request
// to Slack carries the CALLER'S OWN workspace token, and Slack's private download
// URL never appears in anything we hand back.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// pdfBytes and textBytes are the two type outcomes: bytes magic will name, and
// bytes it will not.
var pdfBytes = []byte("%PDF-1.4\n1 0 obj\n<< >>\nendobj\n")
var textBytes = []byte("quarterly numbers, as promised\n")

// slackFileApp stubs the Web API with a workspace holding one message and two
// files on it. url_private_download points back at the same stub, which is what
// production shape looks like (an address Slack hands us, not one we build) and
// what slackOwnedURL admits by the same rule it admits files.slack.com.
func slackFileApp(t *testing.T, seen *[]string) *zip.App {
	t.Helper()
	return slackMessageApp(t, func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.URL.Path)
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/conversations.list":
			_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C123","name":"hanzo-gtm","is_member":true}]}`))
		case "/conversations.history":
			// The raw Slack shape, url_private INCLUDED — so the test proves the
			// projection drops it rather than proving the stub never sent one.
			_, _ = w.Write([]byte(`{"ok":true,"messages":[{"ts":"1710000000.000002","user":"U1","text":"numbers attached","files":[
				{"id":"F123","name":"report.pdf","title":"Q3 report","mimetype":"application/pdf","size":29,
				 "permalink":"https://acme.slack.com/files/U1/F123/report.pdf",
				 "url_private":"https://files.slack.com/files-pri/T1-F123/report.pdf",
				 "url_private_download":"https://files.slack.com/files-pri/T1-F123/download/report.pdf"}]}]}`))
		case "/files.info":
			switch r.Form.Get("file") {
			case "F123":
				_, _ = w.Write([]byte(`{"ok":true,"file":{"name":"report.pdf","size":29,"url_private_download":"` +
					slackWebAPIBase + `/files-pri/F123"}}`))
			case "F456":
				_, _ = w.Write([]byte(`{"ok":true,"file":{"name":"notes.txt","size":31,"url_private_download":"` +
					slackWebAPIBase + `/files-pri/F456"}}`))
			case "F789":
				// A file in a conversation this bot was never invited to reads
				// exactly like one that never existed, which is the honest answer.
				_, _ = w.Write([]byte(`{"ok":false,"error":"file_not_found"}`))
			case "FEVIL":
				_, _ = w.Write([]byte(`{"ok":true,"file":{"name":"x","url_private_download":"https://evil.example/steal"}}`))
			default:
				_, _ = w.Write([]byte(`{"ok":false,"error":"file_not_found"}`))
			}
		case "/files-pri/F123", "/files-pri/F456":
			// THE TOKEN, on the download and not only on the API call.
			if got := r.Header.Get("Authorization"); got != "Bearer xoxb-acme" {
				t.Errorf("download used the wrong token: %q", got)
			}
			if r.URL.Path == "/files-pri/F123" {
				_, _ = w.Write(pdfBytes)
			} else {
				_, _ = w.Write(textBytes)
			}
		default:
			t.Errorf("unexpected Slack call: %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
}

// getFile issues the read and returns the headers too, which `req` discards and
// which are half of what this route has to get right.
func getFile(t *testing.T, app *zip.App, path, org string) (int, http.Header, []byte) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodGet, path, nil)
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

// A message's files are listed, and the private URL is not among them.
func TestSlackMessageFilesAreListedWithoutThePrivateURL(t *testing.T) {
	var seen []string
	app := slackFileApp(t, &seen)

	r := req(t, app, http.MethodGet, "/v1/provider/slack/messages?channel=C123", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("read: %d %s", r.Code, r.Body)
	}
	var out slackMessagesOut
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || len(out.Messages[0].Files) != 1 {
		t.Fatalf("the message lost its files: %+v", out)
	}
	f := out.Messages[0].Files[0]
	if f.ID != "F123" || f.Name != "report.pdf" || f.Title != "Q3 report" ||
		f.Mimetype != "application/pdf" || f.Size != 29 ||
		f.Permalink != "https://acme.slack.com/files/U1/F123/report.pdf" {
		t.Fatalf("file projection lost a field: %+v", f)
	}
	// The whole reason the shape is a projection and not Slack's own object: a
	// credentialed address must not travel to a reader that cannot use it.
	if strings.Contains(string(r.Body), "url_private") || strings.Contains(string(r.Body), "files-pri") {
		t.Fatalf("the private download URL reached the caller: %s", r.Body)
	}
}

// The bytes come back, typed from the bytes themselves.
func TestSlackFileReadServesTheBytes(t *testing.T) {
	var seen []string
	app := slackFileApp(t, &seen)

	code, hdr, body := getFile(t, app, "/v1/provider/slack/file?id=F123", "acme")
	if code != http.StatusOK {
		t.Fatalf("read F123: %d %s", code, body)
	}
	if !bytes.Equal(body, pdfBytes) {
		t.Fatalf("body is not the file: %q", body)
	}
	// magic names a PDF from its own bytes, so it is served as one.
	if ct := hdr.Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", ct)
	}
	if hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("a served upload must be nosniff")
	}

	// Bytes magic will not name leave inert, under an attachment disposition —
	// this origin is api.hanzo.ai, and an upload must never be rendered here.
	code, hdr, body = getFile(t, app, "/v1/provider/slack/file?id=F456", "acme")
	if code != http.StatusOK || !bytes.Equal(body, textBytes) {
		t.Fatalf("read F456: %d %q", code, body)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("unnamed bytes must be inert, got %q", ct)
	}
	if cd := hdr.Get("Content-Disposition"); !strings.Contains(cd, "notes.txt") {
		t.Fatalf("Content-Disposition = %q, want the filename", cd)
	}
}

// Every refusal, and the ones that must never reach Slack at all.
func TestSlackFileGates(t *testing.T) {
	var seen []string
	app := slackFileApp(t, &seen)

	for _, tc := range []struct {
		path, org string
		status    int
	}{
		{"/v1/provider/slack/file?id=F123", "", 401},            // no identity
		{"/v1/provider/slack/file?id=F123", "unconnected", 400}, // no workspace
		{"/v1/provider/slack/file", "acme", 400},                // no id
		{"/v1/provider/slack/file?id=not-a-file", "acme", 400},  // not an id
	} {
		code, _, body := getFile(t, app, tc.path, tc.org)
		if code != tc.status {
			t.Errorf("%s (%s): %d %s", tc.path, tc.org, code, body)
		}
	}
	if len(seen) != 0 {
		t.Fatalf("an unauthenticated or malformed read reached Slack: %v", seen)
	}

	// A file this workspace's bot cannot see is a 404, not a 502: "not yours" and
	// "not there" are the same fact from out here.
	if code, _, _ := getFile(t, app, "/v1/provider/slack/file?id=F789", "acme"); code != http.StatusNotFound {
		t.Fatalf("an invisible file must be 404, got %d", code)
	}
}

// The bot token is spent on Slack and nowhere else. If files.info ever names an
// address off Slack, the read is refused rather than made bare — a live
// workspace credential must not be handed to whoever owns that host.
func TestSlackFileRefusesADownloadOffSlack(t *testing.T) {
	var seen []string
	app := slackFileApp(t, &seen)

	code, _, body := getFile(t, app, "/v1/provider/slack/file?id=FEVIL", "acme")
	if code != http.StatusBadGateway {
		t.Fatalf("an off-Slack download must be refused, got %d %s", code, body)
	}
	for _, p := range seen {
		if strings.Contains(p, "steal") {
			t.Fatal("the token was sent off Slack")
		}
	}
}
