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

// ../ap/packages/pieces/community/brave-search/src/index.ts
var index_exports = {};
__export(index_exports, {
  braveSearch: () => braveSearch
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/brave-search/src/lib/actions/web-search.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/brave-search/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), braveSearchAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Your Brave Search API Key (get it from https://brave.com/search/api/)"
});

// ../ap/packages/pieces/community/brave-search/src/lib/actions/web-search.ts
var braveWebSearchAction = (0, import_pieces_framework2.createAction)({
  auth: braveSearchAuth,
  name: "web_search",
  classification: "SEARCH",
  displayName: "Web Search",
  description: "Search the web using Brave Search",
  audience: "both",
  aiMetadata: {
    description: "Runs a web search query against the Brave Search API and returns the matching web results. Use it to retrieve current information from the public web for a given text query; set the optional count (1-20) to control how many results come back. Read-only lookup that returns the same results for the same query.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework2.Property.ShortText({
      displayName: "Query",
      description: "The search query",
      required: !0
    }),
    count: import_pieces_framework2.Property.Number({
      displayName: "Count",
      description: "Number of results (1-20)",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    let query = context.propsValue.query, count = context.propsValue.count;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: "https://api.search.brave.com/res/v1/web/search",
      headers: {
        "X-Subscription-Token": context.auth.secret_text,
        Accept: "application/json"
      },
      queryParams: {
        q: query,
        count
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/brave-search/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var braveSearch = (0, import_pieces_framework3.createPiece)({
  displayName: "Brave Search",
  description: "Privacy-preserving search engine",
  auth: braveSearchAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/brave-search.png",
  authors: ["ErisMorn", "sanket-a11y"],
  actions: [
    braveWebSearchAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: braveSearchAuth,
      baseUrl: () => "https://api.search.brave.com/res/v1",
      authMapping: async (auth) => ({
        "X-Subscription-Token": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  braveSearch
});
