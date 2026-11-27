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

// ../ap/packages/pieces/community/detecting-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  detectingAi: () => detectingAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/detecting-ai/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.detecting-ai.com", detectingAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can obtain your API key from the [DETECTING-AI.COM Dashboard](https://detecting-ai.com) in the API section.",
  required: !0
});

// ../ap/packages/pieces/community/detecting-ai/src/lib/actions/detect-ai-content.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var detectAiContent = (0, import_pieces_framework2.createAction)({
  name: "detect_ai_content",
  displayName: "Detect AI Content",
  description: "Detect AI-generated content in text",
  audience: "both",
  aiMetadata: { description: "Analyzes a block of text and scores how likely it was AI-generated, using the DetectingAI detection API. Use to verify whether submitted or scraped content is human-written vs. machine-generated. Requires the text to check; a detection-model version (v1 or v2) can be selected. Read-only analysis \u2014 calling it again with the same text returns the same assessment without side effects.", idempotent: !0 },
  auth: detectingAiAuth,
  requireAuth: !0,
  props: {
    text: import_pieces_framework2.Property.LongText({
      displayName: "Text",
      required: !0
    }),
    version: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Version",
      required: !0,
      options: {
        options: [
          {
            label: "v1",
            value: "v1"
          },
          {
            label: "v2",
            value: "v2"
          }
        ]
      },
      defaultValue: "v2"
    })
  },
  async run({ auth, propsValue }) {
    let apiKey = auth.secret_text;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${BASE_URL}/api/detect/`,
      headers: {
        "X-API-Key": apiKey,
        "Content-Type": "application/json"
      },
      body: {
        text: propsValue.text,
        version: propsValue.version
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/detecting-ai/src/lib/actions/check-plagiarism.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var checkPlagiarism = (0, import_pieces_framework3.createAction)({
  name: "check_plagiarism",
  displayName: "Check Plagiarism",
  description: "Check text for plagiarism",
  audience: "both",
  aiMetadata: { description: "Scans a block of text against the DetectingAI plagiarism API to find matching or copied passages from external sources. Use to verify originality of submitted or generated content. Requires the text to check. Read-only analysis \u2014 repeating the call with the same text returns the same result with no side effects.", idempotent: !0 },
  auth: detectingAiAuth,
  requireAuth: !0,
  props: {
    text: import_pieces_framework3.Property.LongText({
      displayName: "Text",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let apiKey = auth.secret_text;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${BASE_URL}/api/plagiarism/`,
      headers: {
        "X-API-Key": apiKey,
        "Content-Type": "application/json"
      },
      body: {
        text: propsValue.text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/detecting-ai/src/lib/actions/humanize-text.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var humanizeText = (0, import_pieces_framework4.createAction)({
  name: "humanize_text",
  displayName: "Humanize Text",
  description: "Humanize text to make it sound more natural",
  audience: "both",
  aiMetadata: { description: "Rewrites a block of text via the DetectingAI humanize API to read as more natural and less machine-generated, typically to reduce AI-detection scores. Use to transform AI-drafted content into a more human-sounding version. Requires the text and a rewriting model (cognia or lexi). Not idempotent \u2014 each call regenerates the text, so repeating it produces a different rewrite.", idempotent: !1 },
  auth: detectingAiAuth,
  requireAuth: !0,
  props: {
    text: import_pieces_framework4.Property.LongText({
      displayName: "Text",
      required: !0
    }),
    model: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Model",
      required: !0,
      options: {
        options: [
          {
            label: "cognia",
            value: "cognia"
          },
          {
            label: "lexi",
            value: "lexi"
          }
        ]
      },
      defaultValue: "lexi"
    })
  },
  async run({ auth, propsValue }) {
    let apiKey = auth.secret_text;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${BASE_URL}/api/humanize/`,
      headers: {
        "X-API-Key": apiKey,
        "Content-Type": "application/json"
      },
      body: {
        text: propsValue.text,
        model: propsValue.model
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/detecting-ai/src/index.ts
var detectingAi = (0, import_pieces_framework5.createPiece)({
  displayName: "DETECTING-AI.COM",
  auth: detectingAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/detecting-ai.png",
  categories: [import_pieces_framework6.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["onyedikachi-david"],
  actions: [
    detectAiContent,
    checkPlagiarism,
    humanizeText,
    (0, import_pieces_common5.createCustomApiCallAction)({
      auth: detectingAiAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        "X-API-Key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  detectingAi
});
