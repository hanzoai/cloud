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

// ../ap/packages/pieces/community/alt-text-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  altTextAi: () => altTextAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/alt-text-ai/src/lib/actions/generate-alt-text.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/alt-text-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/alt-text-ai/src/lib/common/constants.ts
var BASE_URL = "https://alttext.ai/api/v1";

// ../ap/packages/pieces/community/alt-text-ai/src/lib/common/auth.ts
var altTextAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "You can obtain your API key from [Account Settings](https://alttext.ai/account/api_keys).",
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: BASE_URL + "/account",
        headers: {
          "X-API-Key": auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key"
      };
    }
  }
});

// ../ap/packages/pieces/community/alt-text-ai/src/lib/actions/generate-alt-text.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var generateAltTextAction = (0, import_pieces_framework2.createAction)({
  name: "generate-alt-text",
  classification: "READ",
  auth: altTextAiAuth,
  displayName: "Generate Alt Text",
  description: "Generates a descriptive alt text for a given image.",
  audience: "both",
  aiMetadata: { description: "Sends an image to AltText.ai and returns AI-generated descriptive alt text for accessibility/SEO. Use when you have an image (uploaded file passed as raw base64) and need a human-readable description; optionally bias the output with keywords, negative keywords, or a keyword-source text. Each call submits the image for fresh generation, so it is not idempotent.", idempotent: !1 },
  props: {
    image: import_pieces_framework2.Property.File({
      displayName: "Image",
      required: !0
    }),
    keywords: import_pieces_framework2.Property.Array({
      displayName: "Keywords",
      required: !1,
      description: "keywords / phrases to be considered when generating the alt text."
    }),
    negativeKeywords: import_pieces_framework2.Property.Array({
      displayName: "Negative Keywords",
      required: !1,
      description: "negative keywords / phrases to be removed from any generated alt text."
    }),
    keywordSource: import_pieces_framework2.Property.LongText({
      displayName: "Keyword Source",
      required: !1,
      description: "Text to use as the source of keywords for the alt text."
    })
  },
  async run(context) {
    let { image, keywords, keywordSource, negativeKeywords } = context.propsValue;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: BASE_URL + "/images",
      headers: {
        "X-API-Key": context.auth.secret_text
      },
      body: {
        image: {
          raw: image.base64
        },
        keywords,
        keyword_source: keywordSource,
        negative_keywords: negativeKeywords
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/alt-text-ai/src/index.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
var import_pieces_framework4 = require("@activepieces/pieces-framework"), altTextAi = (0, import_pieces_framework3.createPiece)({
  displayName: "AltText.ai",
  categories: [import_pieces_framework4.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  auth: altTextAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/alt-text-ai.png",
  authors: ["kishanprmr"],
  actions: [
    generateAltTextAction,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: altTextAiAuth,
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
  altTextAi
});
