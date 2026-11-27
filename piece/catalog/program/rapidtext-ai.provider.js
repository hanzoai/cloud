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

// ../ap/packages/pieces/community/rapidtext-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  rapidtextAi: () => rapidtextAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/rapidtext-ai/src/lib/actions/generate-article.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/rapidtext-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), rapidTextAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can obtain your API key from [Dashboard Settings](app.rapidtextai.com).",
  required: !0
});

// ../ap/packages/pieces/community/rapidtext-ai/src/lib/actions/generate-article.ts
var import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/rapidtext-ai/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), modelDropdown = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Model",
  required: !0,
  options: {
    disabled: !1,
    options: [
      { label: "Gemini 2.0 Flash", value: "gemini-2.0-flash" },
      { label: "GPT-4o Mini", value: "gpt-4o-mini" },
      { label: "GPT-4", value: "gpt-4" },
      { label: "DeepSeek V3", value: "deepseek-chat" },
      { label: "GPT-4o", value: "gpt-4o" },
      { label: "Grok-2", value: "grok-2" },
      { label: "Gemini 1.5 Pro", value: "gemini-1.5-pro" },
      { label: "Gemini 2.0 Pro", value: "gemini-2.0-pro" },
      { label: "DeepSeek R1", value: "deepseek-reasoner" }
    ]
  }
});

// ../ap/packages/pieces/community/rapidtext-ai/src/lib/actions/generate-article.ts
var generateArticleAction = (0, import_pieces_framework3.createAction)({
  name: "generate-article",
  auth: rapidTextAiAuth,
  displayName: "Generate Article",
  description: "Generates an article.",
  audience: "both",
  aiMetadata: { description: "Generates a long-form article from a text prompt using a selected RapidTextAI model. Choose this for full article/blog drafting rather than short freeform chat completions (use Send Prompt for that). Requires a prompt and a model selection; each call produces fresh non-deterministic output, so it is not idempotent.", idempotent: !1 },
  props: {
    model: modelDropdown,
    prompt: import_pieces_framework3.Property.LongText({
      displayName: "Prompt",
      required: !0
    })
  },
  async run(context) {
    let { model, prompt } = context.propsValue;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://app.rapidtextai.com/openai/v1/chat/completionsarticle",
      queryParams: {
        gigsixkey: context.auth.secret_text
      },
      body: {
        model,
        messages: [
          {
            role: "user",
            content: prompt
          }
        ]
      }
    })).body.choices[0].message.content;
  }
});

// ../ap/packages/pieces/community/rapidtext-ai/src/lib/actions/send-prompt.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");
var sendPromptAction = (0, import_pieces_framework4.createAction)({
  name: "create-prompt",
  auth: rapidTextAiAuth,
  displayName: "Send Prompt",
  description: "Send prompt to RapidTextAI.",
  audience: "both",
  aiMetadata: { description: "Sends a freeform prompt to a selected RapidTextAI model and returns the completion, with optional sampling controls (max tokens, temperature, top-p, frequency/presence penalties). Choose this for general chat-style or short text generation; use Generate Article for full article drafting. Requires a prompt and a model; each call produces fresh non-deterministic output, so it is not idempotent.", idempotent: !1 },
  props: {
    model: modelDropdown,
    prompt: import_pieces_framework4.Property.LongText({
      displayName: "Prompt",
      required: !0
    }),
    max_tokens: import_pieces_framework4.Property.Number({
      displayName: "Max Tokens",
      required: !1,
      defaultValue: 2048
    }),
    temperature: import_pieces_framework4.Property.Number({
      displayName: "Temperature",
      required: !1,
      description: "Sampling temperature between 0 and 2.",
      defaultValue: 1
    }),
    top_p: import_pieces_framework4.Property.Number({
      displayName: "Top N",
      description: "Nucleus sampling parameter",
      defaultValue: 1,
      required: !1
    }),
    frequency_penalty: import_pieces_framework4.Property.Number({
      displayName: "Frequency Penalty",
      required: !1,
      description: "Penalty for new tokens based on frequency"
    }),
    presence_penalty: import_pieces_framework4.Property.Number({
      displayName: "Presence Penalty",
      required: !1,
      description: "Penalty for new tokens based on presence"
    })
  },
  async run(context) {
    let {
      model,
      prompt,
      max_tokens,
      temperature,
      top_p,
      frequency_penalty,
      presence_penalty
    } = context.propsValue;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://app.rapidtextai.com/openai/completion",
      queryParams: {
        gigsixkey: context.auth.secret_text
      },
      body: {
        model,
        messages: [
          { role: "system", content: "You are a helpful assistant." },
          {
            role: "user",
            content: prompt
          }
        ],
        max_tokens,
        temperature,
        top_p,
        frequency_penalty,
        presence_penalty
      }
    })).body.choices[0].message.content;
  }
});

// ../ap/packages/pieces/community/rapidtext-ai/src/index.ts
var rapidtextAi = (0, import_pieces_framework5.createPiece)({
  displayName: "RapidText AI",
  auth: rapidTextAiAuth,
  minimumSupportedRelease: "0.36.1",
  categories: [import_pieces_framework6.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  logoUrl: "https://cdn.activepieces.com/pieces/rapidtext-ai.png",
  authors: ["kishanprmr"],
  actions: [generateArticleAction, sendPromptAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  rapidtextAi
});
