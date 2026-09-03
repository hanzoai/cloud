var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __export = (target, all) => {
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: true });
};
var __copyProps = (to, from, except, desc) => {
  if (from && typeof from === "object" || typeof from === "function") {
    for (let key of __getOwnPropNames(from))
      if (!__hasOwnProp.call(to, key) && key !== except)
        __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  }
  return to;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: true }), mod);

// ../auto/packages/pieces/community/brave-search/src/index.ts
var index_exports = {};
__export(index_exports, {
  braveSearch: () => braveSearch
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../auto/packages/pieces/community/brave-search/src/lib/actions/web-search.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common");

// ../auto/packages/pieces/community/brave-search/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var braveSearchAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: true,
  description: "Your Brave Search API Key (get it from https://brave.com/search/api/)"
});

// ../auto/packages/pieces/community/brave-search/src/lib/actions/web-search.ts
var braveWebSearchAction = (0, import_pieces_framework2.createAction)({
  auth: braveSearchAuth,
  name: "web_search",
  displayName: "Web Search",
  description: "Search the web using Brave Search",
  props: {
    query: import_pieces_framework2.Property.ShortText({
      displayName: "Query",
      description: "The search query",
      required: true
    }),
    count: import_pieces_framework2.Property.Number({
      displayName: "Count",
      description: "Number of results (1-20)",
      required: false,
      defaultValue: 10
    })
  },
  async run(context) {
    const query = context.propsValue.query;
    const count = context.propsValue.count;
    const response = await import_pieces_common.httpClient.sendRequest({
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
    });
    return response.body;
  }
});

// ../auto/packages/pieces/community/brave-search/src/index.ts
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
      authMapping: async (auth) => {
        return {
          "X-Subscription-Token": auth.secret_text
        };
      }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  braveSearch
});
