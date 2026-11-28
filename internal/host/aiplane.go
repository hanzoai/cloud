package host

// aiplane.go — a completion, embeddings or a rerank crosses to the fleet's own `ai`
// by name, carrying the org, project and person it is for.
//
// Through the router a completion carried a key, and the identity boundary
// deletes every identity header on ingress and re-mints them from the credential,
// so the model API could name the key and never the person a run acts for:
// "application hanzo/cloud-agent is buying inference for nobody". Over the plane
// the caller is stated — the org and the user ride the call — and no credential
// crosses; `ai` authenticates to its own model API as the deployment
// (apps/ai/chat_rpc.go).
//
// The crossing is an http.RoundTripper under go-openai, so the request is encoded,
// the answer decoded and a refusal read exactly as over HTTP: a 402 is the same
// *openai.APIError either way, and the route walk and the retry above cannot tell
// the two apart.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/plane"
)

// overPlane returns c with its buffered completions, embeddings, reranks and
// transcriptions sent to the fleet's own `ai` over the peer plane, where the model
// API prices each one for the caller and the answer carries that price back
// (infer.Bill). The model list stays on c's gateway, and so does any call where no
// `ai` runs — plane.ErrNoPeer is the one error a caller may read as absence. A
// stream crosses too, as ai_chat_stream: its answer arrives a delta at a time on a
// socket, and its price after the last one (sdk/infer planestream.go).
//
// Only the M2M gateway client crosses. A static key is returned as it is: the
// operator named that credential, and its owner's ledger and providers are whose
// the completion is. So is the disabled stub, which holds no gateway to fall back
// to, and no machine identity for `ai` to buy the completion with either.
func overPlane(c infer.Client) infer.Client {
	link, ok := infer.Via(c, planeTrip{})
	if !ok {
		return c
	}
	return &planeAI{gateway: c, link: link}
}

// planeAI is the gateway client with its model calls crossing the plane.
type planeAI struct {
	gateway infer.Client // the model list, and every call where no ai runs
	link    infer.Client // the same client, its completions, embeddings, reranks and transcriptions crossing to ai
}

// ChatStream answers over the plane as ChatCompletion does, a delta at a time; the
// model list stays on the gateway, whose catalog it is.
func (p *planeAI) ChatStream(ctx context.Context, req *infer.ChatRequest, emit func(delta string) error) (*infer.ChatResponse, error) {
	resp, err := p.link.(infer.StreamCompleter).ChatStream(stated(ctx, req.Org, req.Project, req.Actor, req.Payer), req, emit)
	if errors.Is(err, plane.ErrNoPeer) {
		return p.gateway.(infer.StreamCompleter).ChatStream(ctx, req, emit)
	}
	return resp, err
}

func (p *planeAI) Models(ctx context.Context) ([]string, error) {
	return p.gateway.(infer.ModelLister).Models(ctx)
}

// ChatCompletion answers over the plane: the same request, route walk, span and
// reading as the gateway's, with the caller stated on the hop.
func (p *planeAI) ChatCompletion(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	resp, err := p.link.ChatCompletion(stated(ctx, req.Org, req.Project, req.Actor, req.Payer), req)
	if errors.Is(err, plane.ErrNoPeer) {
		return p.gateway.ChatCompletion(ctx, req)
	}
	return resp, err
}

// Embed answers over the plane as ChatCompletion does, for the org and project
// the request names.
func (p *planeAI) Embed(ctx context.Context, req *infer.EmbedRequest) ([][]float32, error) {
	if req == nil {
		return p.gateway.Embed(ctx, req)
	}
	out, err := p.link.Embed(stated(ctx, req.Org, req.Project, "", req.Payer), req)
	if errors.Is(err, plane.ErrNoPeer) {
		return p.gateway.Embed(ctx, req)
	}
	return out, err
}

// Rerank answers over the plane as ChatCompletion does, for the org and project
// the request names.
func (p *planeAI) Rerank(ctx context.Context, req *infer.RerankRequest) ([]float64, error) {
	if req == nil {
		return p.gateway.Rerank(ctx, req)
	}
	out, err := p.link.Rerank(stated(ctx, req.Org, req.Project, "", req.Payer), req)
	if errors.Is(err, plane.ErrNoPeer) {
		return p.gateway.Rerank(ctx, req)
	}
	return out, err
}

// Transcribe answers over the plane as ChatCompletion does, for the org and project
// the request names: the recording crosses as the multipart body it would be over
// HTTP.
func (p *planeAI) Transcribe(ctx context.Context, req *infer.TranscribeRequest) (string, error) {
	if req == nil {
		return p.gateway.(infer.Transcriber).Transcribe(ctx, req)
	}
	out, err := p.link.(infer.Transcriber).Transcribe(stated(ctx, req.Org, req.Project, "", req.Payer), req)
	if errors.Is(err, plane.ErrNoPeer) {
		return p.gateway.(infer.Transcriber).Transcribe(ctx, req)
	}
	return out, err
}

// stated is ctx carrying the caller a model call is for: the request's org and
// project, the user its actor ("<org>/<user>") names in that org, and the payer's
// billing-account claim so apps/ai resolves the exact subscription and plan
// allowance even on detached runs or embeddings/reranks/transcriptions that carry
// no actor. A run on a detached context has no request to forward, so this is how
// its org, payer and person reach `ai`; a caller serving a request forwards the
// identity the boundary minted instead, because zip prefers that over anything
// stated. A person and a project never cross into another org's call, and a name
// and address cross only with the user they belong to.
func stated(ctx context.Context, org, project, actor string, payer account.Account) context.Context {
	who := zip.CallerOf(ctx)
	if org != "" && org != who.Org {
		who.Org, who.User, who.Project = org, "", ""
	}
	if project != "" {
		who.Project = project
	}
	if o, user, ok := strings.Cut(actor, "/"); ok && o == who.Org && user != "" {
		who.User = user
	}
	if who.User != zip.CallerOf(ctx).User {
		who.Name, who.Email = "", ""
	}
	if !payer.Zero() {
		who.Account = payer.String()
	}
	return zip.WithCaller(ctx, who)
}

// planeOp is the op a model API request crosses as, by the path it was built
// for: embeddings, reranks and transcriptions have ops of their own, and
// everything else a completion client sends is a completion.
func planeOp(path string) string {
	switch {
	case strings.HasSuffix(path, "/embeddings"):
		return client.AIEmbed
	case strings.HasSuffix(path, "/rerank"):
		return client.AIRerank
	case strings.HasSuffix(path, "/audio/transcriptions"):
		return client.AITranscribe
	}
	return client.AIChat
}

// planeTrip carries one model API request to `ai` as the op its path names and
// hands the model API's answer back as the response the client reads. The price
// the model API told for the call is told to the call's Bill (infer.BillOf), so the
// meter that made the call charges it.
type planeTrip struct{}

func (planeTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}
	in := &client.ChatIn{Request: body}
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		in.ContentType = ct
	}
	op := planeOp(r.URL.Path)
	if op == client.AIChat && infer.IsStream(body) {
		return streamTrip(r, in)
	}
	out, err := plane.Call[client.ChatIn, client.Chat](r.Context(), cloud.AIApp, op, in)
	if err != nil {
		return nil, err
	}
	if out == nil || out.Status == 0 {
		return nil, fmt.Errorf("cloud: %s answered nothing for %s", cloud.AIApp, r.URL.Path)
	}
	if out.Covered {
		infer.BillOf(r.Context()).Cover()
	} else if out.Billed != nil {
		b := infer.BillOf(r.Context())
		b.Tell(*out.Billed)
		if out.Cash {
			b.SetCash()
		}
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", out.Status, http.StatusText(out.Status)),
		StatusCode:    out.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(out.Body)),
		ContentLength: int64(len(out.Body)),
		Request:       r,
	}, nil
}

// streamTrip carries a completion asked for streamed: ai answers the socket its
// answer is framed onto, and the response reads it as it arrives. The price the
// trailer carries is told to the call's Bill when the body is read to its end, or
// closed (infer.StreamResponse), so the meter that made the call charges it once.
func streamTrip(r *http.Request, in *client.ChatIn) (*http.Response, error) {
	out, err := plane.Call[client.ChatIn, client.Streamed](r.Context(), cloud.AIApp, client.AIChatStream, in)
	if err != nil {
		return nil, err
	}
	if out == nil || out.Socket == "" {
		return nil, fmt.Errorf("cloud: %s answered no stream for %s", cloud.AIApp, r.URL.Path)
	}
	conn, err := net.DialTimeout("unix", out.Socket, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("cloud: open %s's stream: %w", cloud.AIApp, err)
	}
	return infer.StreamResponse(conn, r)
}
