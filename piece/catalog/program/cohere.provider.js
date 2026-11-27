"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __export = (target, all) => {
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: !0 });
}, __copyProps = (to, from, except, desc) => {
  if (from && typeof from == "object" || typeof from == "function")
    for (let key of __getOwnPropNames(from))
      !__hasOwnProp.call(to, key) && key !== except && __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  return to;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: !0 }), mod);

// ../ap/packages/pieces/community/cohere/src/index.ts
var index_exports = {};
__export(index_exports, {
  cohere: () => cohere
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/cohere/src/lib/actions/generate-text.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/cohere/src/lib/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), authDes = `
To obtain your Cohere API Key:

1. Go to the [Cohere Dashboard](https://dashboard.cohere.com/api-keys).
2. Sign in or create a free account.
3. Navigate to **API Keys** in the left sidebar.
4. Click **Create Trial Key** (free) or use a production key.
5. Copy the key and paste it below.

`, cohereAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: authDes,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        url: "https://api.cohere.com/v2/models",
        method: import_pieces_common.HttpMethod.GET,
        headers: {
          Authorization: `Bearer ${auth}`
        }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API Key" };
    }
  }
});

// ../ap/packages/pieces/community/cohere/src/lib/actions/generate-text.ts
var COHERE_CHAT_URL = "https://api.cohere.com/v2/chat", COHERE_MODELS_URL = "https://api.cohere.com/v1/models", MAX_RETRIES = 3, INITIAL_BACKOFF_MS = 1e3;
async function generateWithRetry(apiKey, request) {
  var _a, _b, _c;
  let lastError;
  for (let attempt = 0; attempt < MAX_RETRIES; attempt++)
    try {
      return (await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.POST,
        url: COHERE_CHAT_URL,
        headers: {
          Authorization: `Bearer ${apiKey}`,
          "Content-Type": "application/json"
        },
        body: request
      })).body;
    } catch (error) {
      lastError = error instanceof Error ? error : new Error(String(error));
      let errorAny = error, statusCode = (_a = errorAny.response) == null ? void 0 : _a.status;
      if (!(statusCode === 429 || statusCode !== void 0 && statusCode >= 500) || attempt === MAX_RETRIES - 1) {
        if ((_b = errorAny.response) != null && _b.body) {
          let errorBody = errorAny.response.body;
          throw new Error(
            `Cohere API error (${statusCode}): ${(_c = errorBody.message) != null ? _c : lastError.message}`
          );
        }
        throw lastError;
      }
      let backoffMs = INITIAL_BACKOFF_MS * Math.pow(2, attempt);
      await new Promise((resolve) => setTimeout(resolve, backoffMs));
    }
  throw lastError != null ? lastError : new Error("Generate text failed after retries");
}
var generateText = (0, import_pieces_framework2.createAction)({
  audience: "both",
  auth: cohereAuth,
  name: "generate_text",
  displayName: "Generate Text",
  description: "Generate a text response using Cohere AI Chat API",
  aiMetadata: { description: "Runs a single-turn completion against Cohere's v2 Chat API - one user prompt in, one generated message out - with optional model, temperature, and max-token controls. This is the only purpose-built action in the Cohere piece and covers only chat-style generation, not multi-turn conversation history, embeddings, rerank, or classify; use the piece's Custom API Call action to reach those Cohere endpoints. Requires a prompt and a chat-capable model name. Not idempotent: each call bills a fresh generation and the wording varies unless temperature is set to 0.", idempotent: !1 },
  props: {
    prompt: import_pieces_framework2.Property.LongText({
      displayName: "Prompt",
      description: "The user message to send to the model",
      required: !0
    }),
    model: import_pieces_framework2.Property.Dropdown({
      displayName: "Model",
      description: "The Cohere model to use for generation",
      required: !0,
      defaultValue: "command-a-03-2025",
      auth: cohereAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Enter your API key first",
            options: []
          };
        try {
          return { disabled: !1, options: (await import_pieces_common2.httpClient.sendRequest({
            method: import_pieces_common2.HttpMethod.GET,
            url: COHERE_MODELS_URL,
            headers: {
              Authorization: `Bearer ${auth.secret_text}`
            },
            queryParams: {
              page_size: "1000",
              endpoint: "chat"
            }
          })).body.models.map((model) => ({
            label: model.name,
            value: model.name
          })) };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Couldn't load models, check your API key or try again."
          };
        }
      }
    }),
    temperature: import_pieces_framework2.Property.Number({
      displayName: "Temperature",
      description: "Controls randomness (0.0 = deterministic, 1.0 = maximum randomness). Default: 0.3",
      required: !1,
      defaultValue: 0.3
    }),
    maxTokens: import_pieces_framework2.Property.Number({
      displayName: "Max Tokens",
      description: "Maximum number of tokens to generate. Leave empty for model default.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
    let { prompt, model, temperature, maxTokens } = context.propsValue, apiKey = context.auth.secret_text, request = {
      model,
      messages: [{ role: "user", content: prompt }],
      stream: !1
    };
    temperature != null && (request.temperature = temperature), maxTokens != null && (request.max_tokens = maxTokens);
    let response = await generateWithRetry(apiKey, request);
    return {
      text: (_d = (_c = (_b = (_a = response.message) == null ? void 0 : _a.content) == null ? void 0 : _b[0]) == null ? void 0 : _c.text) != null ? _d : "",
      model,
      finishReason: response.finish_reason,
      usage: {
        inputTokens: (_g = (_f = (_e = response.usage) == null ? void 0 : _e.billed_units) == null ? void 0 : _f.input_tokens) != null ? _g : 0,
        outputTokens: (_j = (_i = (_h = response.usage) == null ? void 0 : _h.billed_units) == null ? void 0 : _i.output_tokens) != null ? _j : 0
      }
    };
  }
});

// ../ap/packages/pieces/community/cohere/src/index.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), cohere = (0, import_pieces_framework3.createPiece)({
  displayName: "Cohere",
  description: "Generate text using Cohere AI language models",
  auth: cohereAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/cohere.png",
  authors: ["AhmadTash"],
  actions: [
    generateText,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: cohereAuth,
      baseUrl: () => "https://api.cohere.com/v2",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  cohere
});
