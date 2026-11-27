package provider

// slack_file.go reads the CONTENT of a file attached to a Slack message.
//
// Reading a conversation used to stop at the text. A message whose whole point
// was the PDF on it came back as a line of prose and an attachment list nobody
// could open, so an agent asked about a document in a channel could see that one
// existed and never what it said.
//
// Slack addresses a file's bytes with url_private_download: an address that is
// useless without the workspace's bot token and dangerous with it. So the URL
// never leaves this process. It is resolved from files.info at the moment of the
// read, spent here with the org's OWN custodied token, and what crosses back to
// the caller is the content — which is the thing they asked for, and the only
// thing that carries no credential.
//
// UNTYPED, unlike every other op in slack_messages.go, and for the reason
// dataroom's document route is: the response IS the bytes. There is no In/Out
// pair for a stream, and wrapping one in a base64 envelope would make every
// reader decode a file to look at it.

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud/internal/magic"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// maxSlackFile bounds what one read will carry. The bytes are buffered to be
// served, so this is a memory bound on a multi-tenant process and not a policy
// about what Slack may hold: a file larger than this is REFUSED by name rather
// than truncated, because half a document that reads as a whole one is worse
// than being told to fetch it another way.
const maxSlackFile = 32 << 20 // 32 MiB

// slackFileID is Slack's own file-id shape. Checking it here turns a typo into a
// 400 that says so, instead of a round trip that comes back as a Slack error
// code about a file that never existed.
var slackFileID = regexp.MustCompile(`^[FW][A-Z0-9]{2,31}$`)

// slackFileRead streams one Slack file's bytes to the caller.
//
// The ORG IS THE CALLER'S, taken from the validated identity and never from the
// request, which is the whole tenancy gate: it selects the bot token the read is
// made with, so a caller cannot name a workspace they have not connected. A file
// their workspace's bot cannot see comes back 404 from Slack's own answer, never
// from a guess made here.
func slackFileRead(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return principal.Refused(c)
	}
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		return zip.ErrBadRequest("id is required: a file id from a message's files")
	}
	if !slackFileID.MatchString(id) {
		return zip.ErrBadRequest("id must be a Slack file id, for example F024BE7LH")
	}
	ctx := c.Context()
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		return zip.ErrBadRequest("slack is not connected for this org")
	}
	info, err := slackFileInfo(ctx, string(tok), id)
	if err != nil {
		return err
	}
	if info.Size > maxSlackFile {
		return zip.Errorf(http.StatusRequestEntityTooLarge,
			"slack file %s is %d bytes, larger than this read serves (%d)", id, info.Size, maxSlackFile)
	}
	data, err := slackFileBytes(ctx, string(tok), info.URLPrivateDownload)
	if err != nil {
		s.Log.Error("slack: file download failed", "org", org, "file", id, "err", err) // never the URL
		return err
	}
	// The served type is the one magic reads out of the BYTES, never the mimetype
	// Slack recorded — that is whatever the uploader's client claimed, and this
	// origin is api.hanzo.ai, where a response served as markup runs beside every
	// console and reads whatever that console holds. So a file renders in place
	// only when its bytes say picture or PDF; everything else leaves inert, as
	// application/octet-stream under an attachment disposition. nosniff keeps the
	// declared type binding, so a PDF that is also valid markup is still a PDF.
	c.SetHeader("X-Content-Type-Options", "nosniff")
	if kind := magic.Type(data); kind != "" {
		c.SetHeader("Content-Type", kind)
	} else {
		c.SetHeader("Content-Type", "application/octet-stream")
		c.SetHeader("Content-Disposition", slackDisposition(info.Name))
	}
	return c.Bytes(http.StatusOK, data)
}

// slackFileDetail is what files.info answers about one file. url_private_download
// is read and spent inside this file and is never published.
type slackFileDetail struct {
	Name               string
	Size               int64
	URLPrivateDownload string
}

// slackFileInfo asks Slack about one file, as the org's bot.
//
// A file the bot cannot see is a 404 and not a 502: Slack says file_not_found
// both for a file that never existed and for one in a conversation this app was
// never invited to, and from out here those are the same fact — there is no such
// file for this workspace's bot. Every other refusal keeps slackReadError's
// actionable wording, because "add files:read and reconnect" is a thing the
// reader can actually do.
func slackFileInfo(ctx context.Context, tok, id string) (slackFileDetail, error) {
	var r struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Needed string `json:"needed"`
		File   struct {
			Name               string `json:"name"`
			Size               int64  `json:"size"`
			URLPrivateDownload string `json:"url_private_download"`
		} `json:"file"`
	}
	if err := slackPostForm(ctx, tok, slackWebAPIBase+"/files.info", url.Values{"file": {id}}, &r); err != nil {
		return slackFileDetail{}, err
	}
	if !r.OK {
		switch r.Error {
		case "file_not_found", "file_deleted":
			return slackFileDetail{}, zip.ErrNotFound(
				"no such file for this workspace; invite Hanzo to the conversation it was shared in")
		}
		return slackFileDetail{}, slackReadError("files.info", r.Error, r.Needed)
	}
	if strings.TrimSpace(r.File.URLPrivateDownload) == "" {
		// A file Slack will describe but not hand over — an external or tombstoned
		// one. Said as itself rather than served as zero bytes, which would read as
		// an empty document.
		return slackFileDetail{}, zip.ErrNotFound("slack has no downloadable content for this file")
	}
	return slackFileDetail{Name: r.File.Name, Size: r.File.Size, URLPrivateDownload: r.File.URLPrivateDownload}, nil
}

// slackFileBytes fetches the content, bounded, with the bot token.
//
// The TOKEN ONLY EVER GOES TO SLACK. The URL arrives in an API response rather
// than being built here, so the one thing worth checking is where it points: an
// address off Slack would send a live workspace credential to whoever owns it,
// and the read is refused rather than retried bare. Real downloads come from
// files.slack.com, which is why this is a suffix rule and not host equality.
func slackFileBytes(ctx context.Context, tok, raw string) ([]byte, error) {
	if !slackOwnedURL(raw) {
		return nil, zip.Errorf(http.StatusBadGateway, "slack returned a download address outside slack.com")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "slack file request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := slackHTTP.Do(req)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "slack file fetch failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, zip.Errorf(http.StatusBadGateway, "slack file http %d", resp.StatusCode)
	}
	// Bounded independently of the size files.info reported: that number is
	// Slack's word about the file, and this is the read that actually allocates.
	// One byte over the cap is a refusal, so a truncated file can never be served
	// as a whole one.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSlackFile+1))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "slack file read failed")
	}
	if len(data) > maxSlackFile {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "slack file is larger than this read serves")
	}
	return data, nil
}

// slackOwnedURL reports whether an address belongs to Slack, and is what decides
// whether the bot token is spent on it.
//
// slackWebAPIBase's own host is accepted so the stub a test points the Web API at
// is reachable by the same rule production uses, rather than by a branch that
// only exists in tests.
func slackOwnedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if base, berr := url.Parse(slackWebAPIBase); berr == nil && base.Host != "" && u.Host == base.Host {
		return true
	}
	return u.Scheme == "https" && (u.Host == "slack.com" || strings.HasSuffix(u.Host, ".slack.com"))
}

// slackDisposition names the download without letting the name reach the wire
// raw: mime.FormatMediaType quotes and percent-encodes, so a name carrying a line
// break or a quote becomes a parameter value rather than a second header. Only
// the last path segment is offered, and a name that survives neither is omitted —
// a bare attachment is complete on its own.
func slackDisposition(name string) string {
	base := path.Base(strings.TrimSpace(name))
	if base == "." || base == "/" || base == "" {
		return "attachment"
	}
	if d := mime.FormatMediaType("attachment", map[string]string{"filename": base}); d != "" {
		return d
	}
	return "attachment"
}
