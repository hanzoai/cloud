// sync_provider.go is the LONG-TAIL connector pull: for a provider whose list+fetch
// is a JavaScript connector (not native Go), it runs that connector in auto's
// in-process runtime over the plane (client/auto AutoProviderRun), then files each
// returned record as a kb.source via the SAME framework.Ingest path the native
// connectors use. Go connectors (github/slack/google) and JS connectors (notion and
// the long tail) both land in the ONE per-org knowledge store + index.
//
// SECURITY (the tenant + secret boundary, unchanged from the native path):
//   - org is the caller's VALIDATED tenant (resolved once in syncConnector via
//     principal.Org), passed here explicitly and stated as the plane caller's org.
//     The runtime reads the org off the call, never off the request.
//   - the OAuth token comes from THIS org's KMS path (read in syncConnector) and is
//     handed to the connector as `auth`. The runtime holds NO KMS; it only ever
//     sees the single credential for this one org's pull.
//   - every filed doc is written into `org` only (framework.Ingest), so a JS-sourced
//     doc is as org-isolated as a manual page.

package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/knowledge/notion"
	"github.com/hanzoai/cloud/client"
	autopeer "github.com/hanzoai/cloud/client/auto"
	"github.com/hanzoai/cloud/sdk"
)

// providerRun runs one connector action in auto's runtime. A value so a test can
// answer for auto; production never reassigns it.
var providerRun = autopeer.AutoProviderRun

// providerProgram declares how ONE long-tail provider pulls through a provider: which
// activepieces provider + action to run, the props to send, how to shape the OAuth token
// into the provider's `auth`, and how to turn a returned record into a normalized
// ingestDoc. Adding a connector = one entry here + the provider in the `providers`
// set + its oauthConfig. The ~280 activepieces apps all fit this one interface.
type providerProgram struct {
	provider string
	action   string
	// props builds the action input. Static for a simple list; may embed a cursor.
	props func(cursor string) map[string]any
	// auth shapes the OAuth access token into the provider's expected credential value.
	auth func(token string) any
	// records extracts the list of items from the provider's raw output.
	records func(output any) []map[string]any
	// normalize maps ONE record to a kb.source document.
	normalize func(rec map[string]any) ingestDoc
}

// providerPrograms is the closed set of long-tail connectors. An unknown provider is
// rejected in providerSync (fail closed). notion is the first, proving the path.
var providerPrograms = map[string]providerProgram{
	"notion": {
		provider: "notion",
		action:   "custom_api_call",
		// Pull the workspace's pages/databases via Notion's search endpoint. The
		// custom_api_call `url` is a DYNAMIC prop resolving to { url }.
		props: func(cursor string) map[string]any {
			body := map[string]any{"page_size": 100}
			if cursor != "" {
				body["start_cursor"] = cursor
			}
			return map[string]any{
				"url":       map[string]any{"url": "https://api.notion.com/v1/search"},
				"method":    "POST",
				"headers":   map[string]any{"Notion-Version": "2022-06-28"},
				"body_type": "json",
				"body":      map[string]any{"data": body},
			}
		},
		auth: func(token string) any {
			// Notion is an OAuth2 provider: the connection value carries the access token.
			return map[string]any{"access_token": token}
		},
		records: notion.Results,
		normalize: func(rec map[string]any) ingestDoc {
			d := notion.Normalize(rec)
			return ingestDoc{
				Title:      d.Title,
				Body:       d.Body,
				ExternalID: d.ExternalID,
				URL:        d.URL,
				Timestamp:  d.Timestamp,
			}
		},
	},
}

// providerRunResult is how one connector action went.
type providerRunResult struct {
	Ok     bool   `json:"ok"`
	Output any    `json:"output"`
	Error  string `json:"error"`
}

// providerSync runs a long-tail provider's connector in auto's runtime and files each
// returned record as a kb.source. org is the validated tenant; token is this org's
// OAuth access token (from KMS). Returns the count ingested and a resume cursor.
func providerSync(s *cloud.Service[state], ctx context.Context, org, provider, token string) (int, string, error) {
	conn, ok := providerPrograms[provider]
	if !ok {
		return 0, "", fmt.Errorf("unknown provider %q", provider)
	}
	// A connector run is real capacity — see meter.go. Authorized here, where the decision to run one is made, so the
	// native-Go connectors beside it stay free.
	ch, err := afford(s, ctx)
	if err != nil {
		return 0, "", err
	}
	defer ch.Release()
	out, err := runProvider(s, ctx, org, conn.provider, conn.action, conn.auth(token), conn.props(""))
	if err != nil {
		return 0, "", err
	}
	charge(ch)
	if !out.Ok {
		return 0, "", fmt.Errorf("%s provider: %s", provider, out.Error)
	}
	recs := conn.records(out.Output)
	n := 0
	for _, r := range recs {
		if n >= maxSyncDocs {
			break
		}
		d := conn.normalize(r)
		if strings.TrimSpace(d.Title) == "" && strings.TrimSpace(d.Body) == "" {
			continue // nothing indexable
		}
		if _, e := fileDoc(ctx, org, provider, d); e == nil {
			n++
		}
	}
	return n, time.Now().UTC().Format(time.RFC3339), nil
}

// runProvider runs one connector action in auto's in-process runtime, as org. The
// credential is sent as `auth`; the runtime hands it to the connector and touches
// nothing else. An error is the plane's; ok:false is the connector's own failure,
// surfaced to the caller.
func runProvider(_ *cloud.Service[state], ctx context.Context, org, provider, action string, auth any, props map[string]any) (providerRunResult, error) {
	a, err := json.Marshal(auth)
	if err != nil {
		return providerRunResult{}, err
	}
	p, err := json.Marshal(props)
	if err != nil {
		return providerRunResult{}, err
	}
	got, err := providerRun(cloud.For(ctx, org), &client.ProviderRunIn{Provider: provider, Action: action, Auth: a, Props: p})
	if err != nil {
		return providerRunResult{}, fmt.Errorf("auto provider run: %w", err)
	}
	out := providerRunResult{Ok: got.Ok, Error: got.Error}
	if len(got.Output) > 0 {
		if err := json.Unmarshal(got.Output, &out.Output); err != nil {
			return providerRunResult{}, fmt.Errorf("decode provider run: %w", err)
		}
	}
	return out, nil
}
