// sync_piece.go is the LONG-TAIL connector pull: for a provider whose list+fetch is
// an activepieces JS "piece" (not native Go), it runs that piece through the isolated
// auto engine runner, then files each returned record as a kb-source via the SAME
// framework.Ingest path the native connectors use. This is the hybrid model's payoff:
// Go connectors (github/slack/google) and JS connectors (notion + the ~280 activepieces
// apps) both land in the ONE per-org knowledge store + index — the user never sees
// which is which.
//
// SECURITY (the tenant + secret boundary, unchanged from the native path):
//   - org is the caller's VALIDATED tenant (resolved once in syncConnector via
//     principal.Tenant), passed here explicitly and stamped as X-Org-Id on the
//     in-cluster call to the auto engine. cloud is the trust boundary; the engine
//     scopes to that org.
//   - the OAuth token comes from THIS org's KMS path (read in syncConnector) and is
//     handed to the piece as `auth`. The piece runner holds NO KMS; it only ever sees
//     the single credential for this one org's pull. A piece cannot reach another
//     org's token.
//   - every filed doc is written into `org` only (framework.Ingest), so a JS-sourced
//     doc is as org-isolated as a manual page.

package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/knowledge/notion"
)

// autoUpstream is the in-cluster base URL of the auto engine (auto.hanzo.svc). The KB
// lane calls its on-demand piece-run endpoint to pull a long-tail connector's records.
// Overridable via AUTO_UPSTREAM (shared with the /v1/auto proxy subsystem's default).
func autoUpstream() string {
	if v := strings.TrimSpace(os.Getenv("AUTO_UPSTREAM")); v != "" {
		return v
	}
	return "http://auto.hanzo.svc.cluster.local:80"
}

// pieceRunSecret is the shared secret the auto engine's /v1/auto/pieces/{piece}/run
// endpoint requires (its X-Piece-Run-Secret gate). cloud is the ONLY legitimate caller
// of that endpoint — it resolves each org's real token and pins the provider URL — so
// the engine gates piece execution on this secret to close the cross-org-write + SSRF
// forge that a presence-only X-Org-Id check would leave open. Both sides read the SAME
// value from KMS (auto-secrets/PIECES_RUNNER_SECRET). Absent here, pieceSync fails
// closed (an empty header can't match the engine's non-empty secret).
func pieceRunSecret() string { return strings.TrimSpace(os.Getenv("PIECES_RUNNER_SECRET")) }

// pieceConnector declares how ONE long-tail provider pulls through a piece: which
// activepieces piece + action to run, the props to send, how to shape the OAuth token
// into the piece's `auth`, and how to turn a returned record into a normalized
// ingestDoc. Adding a connector = one entry here + the provider in the `providers`
// set + its oauthConfig. The ~280 activepieces apps all fit this one interface.
type pieceConnector struct {
	piece  string
	action string
	// props builds the action input. Static for a simple list; may embed a cursor.
	props func(cursor string) map[string]any
	// auth shapes the OAuth access token into the piece's expected credential value.
	auth func(token string) any
	// records extracts the list of items from the piece's raw output.
	records func(output any) []map[string]any
	// normalize maps ONE record to a kb-source document.
	normalize func(rec map[string]any) ingestDoc
}

// pieceConnectors is the closed set of long-tail connectors. An unknown provider is
// rejected in pieceSync (fail closed). notion is the first, proving the path.
var pieceConnectors = map[string]pieceConnector{
	"notion": {
		piece:  "notion",
		action: "custom_api_call",
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
			// Notion is an OAuth2 piece: the connection value carries the access token.
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

// pieceRunResult is the auto engine's /v1/auto/pieces/{piece}/run response.
type pieceRunResult struct {
	Ok     bool   `json:"ok"`
	Output any    `json:"output"`
	Error  string `json:"error"`
}

// pieceSync runs a long-tail provider's piece through the auto engine and files each
// returned record as a kb-source. org is the validated tenant; token is this org's
// OAuth access token (from KMS). Returns the count ingested and a resume cursor.
func (s *svc) pieceSync(ctx context.Context, org, provider, token string) (int, string, error) {
	conn, ok := pieceConnectors[provider]
	if !ok {
		return 0, "", fmt.Errorf("unknown provider %q", provider)
	}
	out, err := s.runPiece(ctx, org, conn.piece, conn.action, conn.auth(token), conn.props(""))
	if err != nil {
		return 0, "", err
	}
	if !out.Ok {
		return 0, "", fmt.Errorf("%s piece: %s", provider, out.Error)
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

// runPiece calls the auto engine's on-demand piece-run endpoint in-cluster, stamping
// the validated org as X-Org-Id and the piece-run secret as X-Piece-Run-Secret (so the
// engine accepts the call as cloud's connector, not an arbitrary in-cluster pod — the
// engine trusts X-Org-Id absolutely and gates this write+SSRF surface on the secret).
// The credential is sent as `auth` in the body; the engine's runner hands it to the
// piece and touches nothing else. A 5xx/timeout is an infrastructure error; an
// ok:false body is a piece-level failure surfaced to the caller.
func (s *svc) runPiece(ctx context.Context, org, piece, action string, auth any, props map[string]any) (pieceRunResult, error) {
	secret := pieceRunSecret()
	if secret == "" {
		// Fail closed: without the secret the engine will refuse (403), so surface a
		// clear "not configured" rather than making a doomed call.
		return pieceRunResult{}, fmt.Errorf("auto piece run: PIECES_RUNNER_SECRET not configured")
	}
	body, err := json.Marshal(map[string]any{"action": action, "auth": auth, "props": props})
	if err != nil {
		return pieceRunResult{}, err
	}
	u := autoUpstream() + "/v1/auto/pieces/" + piece + "/run"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return pieceRunResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)              // the validated tenant; the engine scopes to it
	req.Header.Set("X-Piece-Run-Secret", secret) // authorizes cloud as the caller (RED H1/M1)
	resp, err := syncHTTP.Do(req)
	if err != nil {
		return pieceRunResult{}, fmt.Errorf("auto piece run: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return pieceRunResult{}, err
	}
	if resp.StatusCode/100 != 2 {
		return pieceRunResult{}, fmt.Errorf("auto piece run status %d: %s", resp.StatusCode, truncate(raw, 200))
	}
	var out pieceRunResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return pieceRunResult{}, fmt.Errorf("decode piece run: %w", err)
	}
	return out, nil
}
