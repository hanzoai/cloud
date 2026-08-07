package ai

// THE PUBLIC CONTRACT, v1: inference.
//
// Every line below puts one operation into the published SDKs, the public CLI,
// the MCP tool list and docs.hanzo.ai. Nothing else in the fleet's 1782 paths is
// there, because the projection is default-deny and this is the only file in the
// repository that has said anything (openapi/public.go).
//
// # The rule that earns a line
//
// AN OPERATION IS IN v1 IF IT IS A MODEL CALL, OR IF IT IS THE CATALOG OF MODELS
// TO CALL. That is the whole of it, and everything below is one of the two. It is
// NOT "everything hanzoai/ai serves": that module also answers 109 paths under
// /v1/ai/* — articles, assets, deployments, connections, dashboards — which are
// the app builder's CRUD and not inference, and which the rule therefore leaves
// out without anyone having to name them.
//
// # What is deliberately NOT here
//
// Each of these was measured and left out, and each is one line away if that is
// wrong. Default-deny means the cost of leaving something out is that somebody
// asks for it, and the cost of leaving something in is that we support it
// forever.
//
//	POST /v1/chat                        a second spelling of /v1/chat/completions,
//	                                     down to the same summary. One operation
//	                                     has one address in a published contract;
//	                                     two would be two SDK methods and two CLI
//	                                     verbs for one call. The right fix is for
//	                                     hanzoai/ai to tag it openapi.Compat.
//	POST /v1/generate-text-to-speech-audio        the inherited casdoor-shaped
//	GET  /v1/generate-text-to-speech-audio-stream spellings of /v1/audio/speech.
//	                                     Same call, older name; same answer.
//	GET  /v1/models/{model}/access       entitlement, not inference — the waitlist
//	POST /v1/models/{model}/access       standing for a gated model. It is console
//	                                     workflow, and a customer reaches it in the
//	                                     console. Add it the day an SDK needs it.
//
// # Adding the next product
//
// One line per operation, in the app that serves it, in a file named public.go
// beside the routes. Then `make describe`, which regenerates public.yaml, and the
// diff shows exactly what was published next to the line that published it.

import (
	"net/http"

	"github.com/hanzoai/cloud/openapi"
)

func init() {
	// THE CATALOG — what can be called, and by whom. Both are unauthenticated and
	// secret-free by their own declaration; a client that cannot read the catalog
	// before it holds a credential cannot show a model picker.
	openapi.Public("/v1/models", http.MethodGet)
	openapi.Public("/v1/models/providers", http.MethodGet)

	// TEXT. Four wire formats over one router: OpenAI's chat and legacy
	// completions, OpenAI's Responses, and Anthropic's Messages. They are not
	// duplicates of each other — a caller picks the one its SDK already speaks,
	// which is the entire reason all four are served — and count_tokens is part of
	// the Messages contract rather than an operation of its own (Claude Code calls
	// it before every request).
	openapi.Public("/v1/chat/completions", http.MethodPost)
	openapi.Public("/v1/completions", http.MethodPost)
	openapi.Public("/v1/responses", http.MethodPost)
	openapi.Public("/v1/messages", http.MethodPost)
	openapi.Public("/v1/messages/count_tokens", http.MethodPost)

	// VECTORS — the two operations that turn text into numbers and numbers back
	// into an order. zen-rerank is a first-party model and /v1/rerank is its door.
	openapi.Public("/v1/embeddings", http.MethodPost)
	openapi.Public("/v1/rerank", http.MethodPost)

	// MEDIA. zen-image, zen-video and zen-music are first-party models on the live
	// catalog, and each has its own door — they are NOT reachable through
	// /v1/chat/completions, so publishing the text surface alone would ship a
	// catalog listing models no generated client can call.
	openapi.Public("/v1/images/generations", http.MethodPost)
	openapi.Public("/v1/audio/speech", http.MethodPost)
	openapi.Public("/v1/audio/transcriptions", http.MethodPost)
	openapi.Public("/v1/audio/voice", http.MethodPost)
	openapi.Public("/v1/audio/music", http.MethodPost)
	openapi.Public("/v1/audio/foley", http.MethodPost)

	// Video generation is ASYNC — create returns a job, and the client polls it and
	// then downloads the result. All three are the one operation from a caller's
	// side, so publishing the create alone would publish a call whose answer
	// nothing in the SDK can resolve.
	openapi.Public("/v1/videos/generations", http.MethodPost)
	openapi.Public("/v1/videos/{id}", http.MethodGet)
	openapi.Public("/v1/videos/{id}/content", http.MethodGet)
}
