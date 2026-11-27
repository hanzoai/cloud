// Copyright © 2026 Hanzo AI. MIT License.

package ai

// chat_rpc.go — the completion, embeddings or rerank a sibling buys, answered by
// the model API in this process, for the person the caller names.
//
// A sibling used to send its completion through the router carrying a static key
// (CLOUD_AGENT_KEY). The identity boundary deletes every identity header on
// ingress and re-mints them from the credential, so the model API saw the key and
// nobody else; once the key's IAM user was deleted it saw a fallback service
// identity, and every usage row a Slack turn wrote said so: "application
// hanzo/cloud-agent is buying inference for nobody".
//
// Here the caller rides the plane, and reaches the model API stated in process
// (aimod.For, aiobject.Caller): the org, the person as "<org>/<name>" (the user id
// when no name crossed) and the project, carried as a value no request from the
// network can write. The model API books the call to that org, names that person
// and project on it, and tells a family that org. The credential is this
// deployment's own IAM application (iamurl.Machine), minted in this process: nothing
// a sibling holds crosses, and nothing is stored that IAM can leave dangling.
//
// THE CUSTOMER IS CHARGED ONCE, BY THE CALLER, AT THE MODEL API'S PRICE. The
// caller's own meter debits the customer (metered_ai.go), so the model API covers
// the call (PaidBy "caller"): it asks no wallet and debits none, the deployment's
// whose token carried it least of all. What it WOULD have debited any other caller
// for the same answer is told to the call's Bill (hanzoai/ai object.Bill), and the
// answer carries it back as Chat.Billed: the caller charges exactly that and has no
// price of its own. A zen SKU is priced by zen the same way (apps/zen Record).
//
// A ZEN SKU IS ANSWERED BY ZEN, HERE, FOR THE ORG THE CALLER STATED. Zen is
// co-resident in this process and claims its SKUs ahead of the model API, which is
// how every HTTP completion reaches it. A plane completion entered the model API
// directly and skipped that claim, so a zen SKU asked over the plane was relayed by
// the model API to the standalone zen service under the deployment's own org: served
// by another process, under another tenant, beside the zen every external caller
// reaches. Zen's own handler (ServeZen) now answers it first, with the stated org
// and project as its tenant, and the model API sees only what zen does not claim.

import (
	"bytes"
	"cmp"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	aimod "github.com/hanzoai/ai"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// modelAPI is hanzoai/ai's own app entered directly for a stated caller: the edge
// and zen's Claim, which an HTTP completion passes first, never see a plane
// completion. A var so a test can stand in for it, since the real one needs a
// database.
var modelAPI = aimod.For

// machine is the deployment's token source, built once so a burst of completions
// mints one token. Nil when the deployment holds no IAM identity.
var machine = sync.OnceValue(iam.Machine)

// Zen answers a plane request at path (/v1/chat/completions, /v1/embeddings,
// /v1/rerank) whose model the co-resident zen claims, for the caller ctx states,
// and reports false when zen does not claim it.
type Zen func(ctx context.Context, path string, request []byte) (*client.Chat, bool)

// zen is the co-resident zen's plane handler, installed by apps/zen in the process
// that carries both apps. Nil elsewhere, and then every plane completion goes to the
// model API.
var zen atomic.Pointer[Zen]

// ServeZen installs the co-resident zen's plane handler. apps/zen calls it from Use.
func ServeZen(fn Zen) { zen.Store(&fn) }

// exposeChat publishes the model calls a sibling buys on the internal plane. Use
// calls it.
func exposeChat() {
	cloud.Plane().Post("/ai/chat", planeChat,
		zip.WithOperationID(client.AIChat),
		zip.WithSummary("One chat completion, bought for the caller"))
	cloud.Plane().Post("/ai/chat/stream", planeChatStream,
		zip.WithOperationID(client.AIChatStream),
		zip.WithSummary("One chat completion, bought for the caller and streamed as it is produced"))
	cloud.Plane().Post("/ai/embed", planeEmbed,
		zip.WithOperationID(client.AIEmbed),
		zip.WithSummary("Embeddings of the caller's inputs, bought for the caller"))
	cloud.Plane().Post("/ai/rerank", planeRerank,
		zip.WithOperationID(client.AIRerank),
		zip.WithSummary("The caller's documents ordered against a query, bought for the caller"))
	cloud.Plane().Post("/ai/transcribe", planeTranscribe,
		zip.WithOperationID(client.AITranscribe),
		zip.WithSummary("The caller's recording put into words, bought for the caller"))
}

// planeChat buys one chat completion from the model API for the caller and
// answers with the model API's own status and body, so a refusal reads as it
// does over HTTP.
func planeChat(ctx context.Context, in *client.ChatIn) (*client.Chat, error) {
	return buy(ctx, "/v1/chat/completions", in)
}

// planeEmbed buys embeddings of the caller's inputs from the model API for the
// caller and answers with the model API's own status and body.
func planeEmbed(ctx context.Context, in *client.ChatIn) (*client.Chat, error) {
	return buy(ctx, "/v1/embeddings", in)
}

// planeRerank buys the caller's documents ordered against a query from the model
// API for the caller and answers with the model API's own status and body.
func planeRerank(ctx context.Context, in *client.ChatIn) (*client.Chat, error) {
	return buy(ctx, "/v1/rerank", in)
}

// planeTranscribe buys the caller's recording put into words from the model API for
// the caller — the multipart body as it would cross HTTP — and answers with the
// model API's own status and body.
func planeTranscribe(ctx context.Context, in *client.ChatIn) (*client.Chat, error) {
	return buy(ctx, "/v1/audio/transcriptions", in)
}

// billKey keys the Bill a plane call is told its price on.
type billKey struct{}

// BillOf is the Bill the plane call ctx belongs to is told its price on, nil off
// the plane. Zen tells it from Record; the model API from the caller's grant.
func BillOf(ctx context.Context) *aiobject.Bill {
	b, _ := ctx.Value(billKey{}).(*aiobject.Bill)
	return b
}

// limiter paces a sibling's call (rate.go). Use sets it before the plane serves, so
// the read in buy needs no lock.
var limiter *limits.Limiter

// buy answers one request at path for the caller: zen when it claims the model,
// else the model API entered as this deployment, naming the caller's person and
// project. Whichever answers prices the call on one Bill, and the answer carries
// the price back. A call past its person's requests a minute is refused 429 before
// either is asked, as it is on HTTP, and so is one whose model or mode needs a plan
// entitlement its payer's plan does not include (entitle.go), and one the model's
// capability record refuses (capable.go).
func buy(ctx context.Context, path string, in *client.ChatIn) (*client.Chat, error) {
	if out := pace(ctx, limiter, path); out != nil {
		return out, nil
	}
	if out := entitled(ctx, limiter, path, in); out != nil {
		return out, nil
	}
	in, refused := screen(ctx, path, in)
	if refused != nil {
		return refused, nil
	}
	bill := &aiobject.Bill{}
	ctx = context.WithValue(ctx, billKey{}, bill)
	out, err := answered(ctx, path, in, bill)
	if err != nil || out == nil {
		return out, err
	}
	if n, told := bill.Nanos(); told {
		out.Billed = &n
	}
	return out, nil
}

// answered is the request at path answered by zen or the model API.
func answered(ctx context.Context, path string, in *client.ChatIn, bill *aiobject.Bill) (*client.Chat, error) {
	if fn := zen.Load(); fn != nil && in.ContentType == "" {
		if out, ok := (*fn)(ctx, path, in.Request); ok {
			return out, nil
		}
	}
	return serve(ctx, http.MethodPost, path, in.Request, in.ContentType, bill)
}

// serve answers one request to the model API entered as this deployment, naming
// the caller's person and project, and telling bill what the call cost.
func serve(ctx context.Context, method, path string, body []byte, contentType string, bill *aiobject.Bill) (*client.Chat, error) {
	rec := httptest.NewRecorder()
	if err := enter(ctx, rec, method, path, body, contentType, bill); err != nil {
		return nil, err
	}
	return &client.Chat{Status: rec.Code, Body: rec.Body.Bytes()}, nil
}

// enter has the model API, entered as this deployment for the caller ctx states,
// answer one request into w.
func enter(ctx context.Context, w http.ResponseWriter, method, path string, body []byte, contentType string, bill *aiobject.Bill) error {
	ts := machine()
	if ts == nil {
		return zip.ErrInternal("ai: this deployment holds no IAM identity (IAM_CLIENT_ID, IAM_CLIENT_SECRET) to buy a model call with")
	}
	tok, err := ts.Token()
	if err != nil {
		return zip.ErrInternal("ai: mint the deployment's token: " + err.Error())
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(ctx)
	if len(body) > 0 {
		req.Header.Set("Content-Type", cmp.Or(contentType, "application/json"))
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	who := cloud.Who(ctx)
	caller := aiobject.Caller{Org: who.Org, Bill: bill}
	if who.Org != "" && who.User != "" {
		caller.Person = who.Org + "/" + cmp.Or(who.Name, who.User)
	}
	if who.Org != "" {
		caller.Project = who.Project
	}
	modelAPI(caller).ServeHTTP(w, req)
	return nil
}
