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

// ../ap/packages/pieces/community/llmrails/src/index.ts
var index_exports = {};
__export(index_exports, {
  llmrails: () => llmrails,
  llmrailsAuth: () => llmrailsAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/llmrails/src/lib/actions/datastore-search.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var datastoreSearch = (0, import_pieces_framework.createAction)({
  auth: llmrailsAuth,
  name: "search",
  displayName: "Datastore search",
  description: "Search in datastore",
  audience: "both",
  aiMetadata: {
    description: "Runs a semantic search query against an LLMRails datastore (vector store) and returns matching passages. Use to retrieve relevant context from a knowledge base for RAG or question answering. Requires the target datastore ID and a query text; optionally combines dense and sparse embeddings (hybrid) and can return a summarized answer over the results. Read-only and idempotent \u2014 repeating the same query returns the same results without side effects.",
    idempotent: !0
  },
  props: {
    datastoreId: import_pieces_framework.Property.ShortText({
      displayName: "Datastore ID",
      required: !0
    }),
    text: import_pieces_framework.Property.ShortText({
      displayName: "Text",
      required: !0,
      description: "Search query"
    }),
    hybrid: import_pieces_framework.Property.Checkbox({
      displayName: "Hybrid search",
      description: "Hybrid search is combining dense and sparse embeddings.",
      required: !0,
      defaultValue: !0
    }),
    summarize: import_pieces_framework.Property.Checkbox({
      displayName: "Summarize",
      description: "Summarizes the datastore search results",
      required: !0,
      defaultValue: !0
    })
  },
  async run(context) {
    let request = {
      url: `https://api.llmrails.com/v1/datastores/${context.propsValue.datastoreId}/search`,
      method: import_pieces_common.HttpMethod.POST,
      body: {
        text: context.propsValue.text,
        summarize: context.propsValue.summarize,
        hybrid: context.propsValue.hybrid
      },
      headers: {
        "X-API-KEY": context.auth.secret_text,
        Accept: "application/json"
      }
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/llmrails/src/index.ts
var markdownDescription = `
Follow these instructions to get your LLMRails API Key:

1. Visit the following website: https://console.llmrails.com/api-keys.
2. Once on the website, click on create a key.
3. Once you have created a key, copy it and use it for the Api key field on the site.
`, llmrailsAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0,
  validate: async ({ auth }) => auth.startsWith("api_") ? {
    valid: !0
  } : {
    valid: !1,
    error: "Invalid Api Key"
  }
}), llmrails = (0, import_pieces_framework2.createPiece)({
  displayName: "LLMRails",
  description: "LLM Rails Platform",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/llmrails.png",
  categories: [import_pieces_framework3.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["w95", "kishanprmr", "MoShizzle", "abuaboud"],
  auth: llmrailsAuth,
  actions: [
    datastoreSearch,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://api.llmrails.com/v1",
      auth: llmrailsAuth,
      authMapping: async (auth) => ({
        "X-API-KEY": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  llmrails,
  llmrailsAuth
});
