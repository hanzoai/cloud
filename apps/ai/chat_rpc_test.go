// Copyright © 2026 Hanzo AI. MIT License.

package ai

// chat_rpc_test.go drives the completion a sibling buys over the REAL socket — the
// wire a Slack turn's process uses — against a model API that records what
// arrived, with the deployment's identity minted by a stand-in IAM.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

func TestASiblingsCompletionNamesThePersonToTheModelAPI(t *testing.T) {
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/iam/oauth/token" {
			t.Errorf("IAM asked %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"machine-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer iam.Close()
	t.Setenv("IAM_URL", iam.URL)
	t.Setenv("IAM_CLIENT_ID", "hanzo-cloud")
	t.Setenv("IAM_CLIENT_SECRET", "s3cret")

	type arrival struct {
		path, auth, org, body, contentType string
		caller                             aiobject.Caller
	}
	seen := make(chan arrival, 1)
	refuse := false
	var price int64 // what the model API tells the caller's Bill, when above zero
	was := modelAPI
	modelAPI = func(caller aiobject.Caller) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if price > 0 {
				caller.Bill.Add(price)
			}
			seen <- arrival{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get(zip.HeaderOrg), string(body), r.Header.Get("Content-Type"), caller}
			w.Header().Set("Content-Type", "application/json")
			if refuse {
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(`{"error":{"message":"insufficient_balance"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion"}`))
		})
	}
	t.Cleanup(func() { modelAPI = was })

	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", kit.Sockets(t))
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	exposeChat()
	exposeMedia()
	stop, err := cloud.ServePlane("ai", nil)
	if err != nil {
		t.Fatalf("serve ai: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
	// Dialled, not called by name: by name this process would answer itself, and
	// what is under test is what crosses the socket.
	conn, err := zip.Dial(zip.SocketPath("ai"))
	if err != nil {
		t.Fatalf("dial ai: %v", err)
	}
	const request = `{"model":"m","messages":[{"role":"user","content":"ping"}]}`
	call := func(who zip.Caller) *client.Chat {
		t.Helper()
		out, err := zip.Call[client.ChatIn, client.Chat](zip.WithCaller(context.Background(), who), conn,
			client.AIChat, &client.ChatIn{Request: []byte(request)})
		if err != nil {
			t.Fatalf("ai_chat: %v", err)
		}
		return out
	}

	t.Run("the customer is stated and the credential is the deployment's", func(t *testing.T) {
		out := call(zip.Caller{Org: "acme", User: "u-7"})
		got := <-seen
		if got.path != "/v1/chat/completions" || got.body != request {
			t.Errorf("model API saw %s %q, want the request verbatim at /v1/chat/completions", got.path, got.body)
		}
		if got.caller.Org != "acme" || got.caller.Person != "acme/u-7" {
			t.Errorf("caller = %+v, want acme and acme/u-7", got.caller)
		}
		if got.auth != "Bearer machine-token" {
			t.Errorf("Authorization = %q, want the deployment's machine token", got.auth)
		}
		if got.org != "" {
			t.Errorf("X-Org-Id = %q; the deployment's token covers its own org only", got.org)
		}
		if out.Status != http.StatusOK || string(out.Body) != `{"id":"c1","object":"chat.completion"}` {
			t.Errorf("answer = %d %s, want the model API's own", out.Status, out.Body)
		}
	})

	t.Run("a person is named by username, as their own rows are", func(t *testing.T) {
		call(zip.Caller{Org: "acme", User: "5f3c-uuid", Name: "alice"})
		if got := <-seen; got.caller.Person != "acme/alice" {
			t.Errorf("person = %q, want acme/alice", got.caller.Person)
		}
	})

	t.Run("a caller with no person names nobody", func(t *testing.T) {
		call(zip.Caller{Org: "acme"})
		if got := <-seen; got.caller.Person != "" || got.caller.Org != "acme" {
			t.Errorf("caller = %+v, want acme naming no person", got.caller)
		}
	})

	t.Run("the project rides as attribution", func(t *testing.T) {
		call(zip.Caller{Org: "acme", Project: "p1"})
		if got := <-seen; got.caller.Project != "p1" || got.org != "" {
			t.Errorf("project = %q, X-Org-Id = %q; want p1 and none", got.caller.Project, got.org)
		}
	})

	t.Run("a model zen claims is answered by zen for the stated caller", func(t *testing.T) {
		claimed := make(chan zip.Caller, 1)
		ServeZen(func(ctx context.Context, path string, request []byte) (*client.Chat, bool) {
			if path != "/v1/chat/completions" || !strings.Contains(string(request), `"model":"zen5-flash"`) {
				return nil, false
			}
			claimed <- cloud.Who(ctx)
			return &client.Chat{Status: http.StatusOK, Body: []byte(`{"id":"z1"}`)}, true
		})
		t.Cleanup(func() { zen.Store(nil) })

		out, err := zip.Call[client.ChatIn, client.Chat](zip.WithCaller(context.Background(), zip.Caller{Org: "acme", Project: "p1"}), conn,
			client.AIChat, &client.ChatIn{Request: []byte(`{"model":"zen5-flash","messages":[]}`)})
		if err != nil {
			t.Fatalf("ai_chat: %v", err)
		}
		if who := <-claimed; who.Org != "acme" || who.Project != "p1" {
			t.Errorf("zen was handed caller %+v, want acme / p1", who)
		}
		if out.Status != http.StatusOK || string(out.Body) != `{"id":"z1"}` {
			t.Errorf("answer = %d %s, want zen's", out.Status, out.Body)
		}
		select {
		case got := <-seen:
			t.Errorf("a zen SKU reached the model API too: %+v", got)
		default:
		}

		call(zip.Caller{Org: "acme", User: "u-7"}) // "m" is not zen's
		if got := <-seen; got.path != "/v1/chat/completions" {
			t.Errorf("a model zen does not claim did not reach the model API: %+v", got)
		}
	})

	// A model's capability record is asked on the plane as on HTTP (#801): a forced
	// tool choice to Fable 5.2 is refused 400 in OpenAI's shape before the model API
	// is called, and a sampling parameter it refuses is dropped from what is sent.
	t.Run("a request the model's record refuses is answered before the model API", func(t *testing.T) {
		caller := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-7"})
		forced := `{"model":"anthropic/claude-fable-5.2","messages":[],"tool_choice":"required"}`
		out, err := zip.Call[client.ChatIn, client.Chat](caller, conn, client.AIChat, &client.ChatIn{Request: []byte(forced)})
		if err != nil {
			t.Fatalf("ai_chat: %v", err)
		}
		if out.Status != http.StatusBadRequest || !strings.Contains(string(out.Body), `"code":"tool_choice_unsupported"`) {
			t.Errorf("answer = %d %s, want 400 tool_choice_unsupported", out.Status, out.Body)
		}
		select {
		case got := <-seen:
			t.Errorf("a refused request reached the model API: %+v", got)
		default:
		}

		sampled := `{"model":"anthropic/claude-fable-5.2","messages":[],"temperature":0.2}`
		if _, err := zip.Call[client.ChatIn, client.Chat](caller, conn, client.AIChat, &client.ChatIn{Request: []byte(sampled)}); err != nil {
			t.Fatalf("ai_chat: %v", err)
		}
		if got := <-seen; strings.Contains(got.body, "temperature") || !strings.Contains(got.body, `"model":"anthropic/claude-fable-5.2"`) {
			t.Errorf("model API saw %q, want the request without temperature", got.body)
		}
	})

	t.Run("embeddings and reranks are bought for the caller at their own paths", func(t *testing.T) {
		claimed := make(chan string, 1)
		ServeZen(func(ctx context.Context, path string, request []byte) (*client.Chat, bool) {
			if !strings.Contains(string(request), `"model":"zen-embedding"`) {
				return nil, false
			}
			claimed <- path + " " + cloud.Who(ctx).Org + "/" + cloud.Who(ctx).Project
			return &client.Chat{Status: http.StatusOK, Body: []byte(`{"data":[]}`)}, true
		})
		t.Cleanup(func() { zen.Store(nil) })
		caller := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-7", Project: "p1"})

		const embed = `{"model":"zen-embedding","input":["x"]}`
		if _, err := zip.Call[client.ChatIn, client.Chat](caller, conn, client.AIEmbed, &client.ChatIn{Request: []byte(embed)}); err != nil {
			t.Fatalf("ai_embed: %v", err)
		}
		if got := <-claimed; got != "/v1/embeddings acme/p1" {
			t.Errorf("zen was handed %q, want /v1/embeddings for acme/p1", got)
		}

		const other = `{"model":"qwen3-embedding","input":["x"]}`
		if _, err := zip.Call[client.ChatIn, client.Chat](caller, conn, client.AIEmbed, &client.ChatIn{Request: []byte(other)}); err != nil {
			t.Fatalf("ai_embed: %v", err)
		}
		if got := <-seen; got.path != "/v1/embeddings" || got.body != other || got.caller.Person != "acme/u-7" || got.caller.Project != "p1" || got.auth != "Bearer machine-token" {
			t.Errorf("model API saw %+v, want the embeddings request at /v1/embeddings for acme/u-7, p1", got)
		}

		const rerank = `{"model":"qwen3-reranker","query":"q","documents":["a"]}`
		if _, err := zip.Call[client.ChatIn, client.Chat](caller, conn, client.AIRerank, &client.ChatIn{Request: []byte(rerank)}); err != nil {
			t.Fatalf("ai_rerank: %v", err)
		}
		if got := <-seen; got.path != "/v1/rerank" || got.body != rerank || got.caller.Person != "acme/u-7" || got.caller.Project != "p1" {
			t.Errorf("model API saw %+v, want the rerank at /v1/rerank for acme/u-7, p1", got)
		}
	})

	t.Run("media calls for a zen SKU are answered by zen for the stated caller", func(t *testing.T) {
		claimed := make(chan string, 1)
		ServeZen(func(ctx context.Context, path string, request []byte) (*client.Chat, bool) {
			if !strings.Contains(string(request), `"model":"iro"`) {
				return nil, false
			}
			claimed <- path + " " + cloud.Who(ctx).Org + "/" + cloud.Who(ctx).User
			return &client.Chat{Status: http.StatusOK, Body: []byte(`{"data":[{"b64_json":"AA=="}]}`)}, true
		})
		t.Cleanup(func() { zen.Store(nil) })
		caller := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-7"})

		const img = `{"model":"iro","prompt":"a white canvas"}`
		out, err := zip.Call[client.MediaIn, client.Chat](caller, conn, client.AIMedia, &client.MediaIn{
			Method: http.MethodPost, Path: "/v1/images/generations", Request: []byte(img),
		})
		if err != nil {
			t.Fatalf("ai_media: %v", err)
		}
		if got := <-claimed; got != "/v1/images/generations acme/u-7" {
			t.Errorf("zen was handed %q, want /v1/images/generations for acme/u-7", got)
		}
		if out.Status != http.StatusOK || string(out.Body) != `{"data":[{"b64_json":"AA=="}]}` {
			t.Errorf("answer = %d %s, want zen's image", out.Status, out.Body)
		}
	})

	t.Run("the price the model API told crosses back with the answer", func(t *testing.T) {
		if out := call(zip.Caller{Org: "acme", User: "u-7"}); out.Billed != nil {
			t.Errorf("an answer nobody priced says it was billed %d", *out.Billed)
		}
		<-seen
		price = 5_500_000_000
		t.Cleanup(func() { price = 0 })
		out := call(zip.Caller{Org: "acme", User: "u-7"})
		<-seen
		if out.Billed == nil || *out.Billed != 5_500_000_000 {
			t.Errorf("billed %v, want 5500000000 nano-USD: what the model API priced the answer at", out.Billed)
		}
	})

	t.Run("zen prices its own SKU on the same bill", func(t *testing.T) {
		ServeZen(func(ctx context.Context, _ string, request []byte) (*client.Chat, bool) {
			if !strings.Contains(string(request), `"model":"zen5-flash"`) {
				return nil, false
			}
			BillOf(ctx).Add(1_300)
			return &client.Chat{Status: http.StatusOK, Body: []byte(`{"id":"z1"}`)}, true
		})
		t.Cleanup(func() { zen.Store(nil) })
		out, err := zip.Call[client.ChatIn, client.Chat](zip.WithCaller(context.Background(), zip.Caller{Org: "acme"}), conn,
			client.AIChat, &client.ChatIn{Request: []byte(`{"model":"zen5-flash","messages":[]}`)})
		if err != nil {
			t.Fatalf("ai_chat: %v", err)
		}
		if out.Billed == nil || *out.Billed != 1_300 {
			t.Errorf("billed %v, want zen's 1300 nano-USD", out.Billed)
		}
	})

	t.Run("a recording crosses as its multipart body and reaches the model API", func(t *testing.T) {
		const form = "--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\n\r\nRIFF\r\n--b--\r\n"
		_, err := zip.Call[client.ChatIn, client.Chat](zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-7"}), conn,
			client.AITranscribe, &client.ChatIn{Request: []byte(form), ContentType: "multipart/form-data; boundary=b"})
		if err != nil {
			t.Fatalf("ai_transcribe: %v", err)
		}
		got := <-seen
		if got.path != "/v1/audio/transcriptions" || got.body != form || got.contentType != "multipart/form-data; boundary=b" {
			t.Errorf("model API saw %s %q as %q, want the multipart body at /v1/audio/transcriptions", got.path, got.body, got.contentType)
		}
	})

	t.Run("a refusal crosses as the model API wrote it", func(t *testing.T) {
		refuse = true
		out := call(zip.Caller{Org: "acme", User: "u-7"})
		<-seen
		if out.Status != http.StatusPaymentRequired || string(out.Body) != `{"error":{"message":"insufficient_balance"}}` {
			t.Errorf("answer = %d %s, want the 402 verbatim", out.Status, out.Body)
		}
	})
}
