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

// ../ap/packages/pieces/community/denser-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  denserAi: () => denserAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/denser-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), denserAiAuth = import_pieces_framework.PieceAuth.CustomAuth({
  props: {
    chatbotId: import_pieces_framework.Property.ShortText({
      displayName: "Chatbot ID",
      description: "The Chatbot ID is available on the [main dashboard of Denser Chatbot](https://denser.ai/u/chatbots). Simply choose the ID of the chatbot you want",
      required: !0
    }),
    apiKey: import_pieces_framework.Property.ShortText({
      displayName: "API key",
      description: "",
      required: !0
    })
  },
  required: !0
});

// ../ap/packages/pieces/community/denser-ai/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/denser-ai/src/lib/actions/process-input-text.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/denser-ai/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://denser.ai/api";
async function makeRequest(method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/denser-ai/src/lib/actions/process-input-text.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), processInputText = (0, import_pieces_framework2.createAction)({
  auth: denserAiAuth,
  name: "processInputText",
  displayName: "Process input text",
  description: "Input text processed by the chatbot",
  audience: "both",
  aiMetadata: {
    description: "Sends a question to a configured Denser.ai chatbot and returns its generated answer, optionally steered by a custom prompt, a chosen LLM model, and inline citations. Use it to query a Denser.ai knowledge-base chatbot for a natural-language response. Requires a question and the chatbot connection; not idempotent, since each call produces a freshly generated answer.",
    idempotent: !1
  },
  props: {
    question: import_pieces_framework2.Property.LongText({
      displayName: "Question",
      description: "The question to be processed by the chatbot",
      required: !0
    }),
    prompt: import_pieces_framework2.Property.LongText({
      displayName: "Prompt",
      description: 'The prompt to be used by the chatbot e.g., "Please provide your answer in the following format: ..."',
      required: !1
    }),
    model: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Model",
      description: "The model to be used by the chatbot (e.g., gpt-3.5, gpt-4)",
      required: !1,
      options: {
        options: [
          { label: "gpt-3.5", value: "gpt-3.5" },
          { label: "gpt-4o-mini", value: "gpt-4o-mini" },
          { label: "gpt-4", value: "gpt-4" },
          { label: "gpt-4o", value: "gpt-4o" },
          { label: "claude-3-5-sonnet", value: "claude-3-5-sonnet" },
          { label: "claude-3-5-haiku", value: "claude-3-5-haiku" },
          { label: "claude-3-7-sonnet", value: "claude-3-7-sonnet" }
        ]
      },
      defaultValue: " gpt-4o-mini"
    }),
    citation: import_pieces_framework2.Property.Checkbox({
      displayName: "Citations",
      description: "Whether to include citations in the response",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { question, prompt, model, citation } = context.propsValue, { apiKey, chatbotId } = context.auth.props;
    return await makeRequest(import_pieces_common2.HttpMethod.POST, "/query", {
      question,
      prompt,
      model,
      citation,
      key: apiKey,
      chatbotId
    });
  }
});

// ../ap/packages/pieces/community/denser-ai/src/index.ts
var denserAi = (0, import_pieces_framework3.createPiece)({
  displayName: "Denser.ai",
  auth: denserAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/denser-ai.png",
  categories: [import_pieces_framework4.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["sanket-a11y"],
  actions: [processInputText],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  denserAi
});
