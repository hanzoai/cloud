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

// ../ap/packages/pieces/community/bocha-search/src/index.ts
var index_exports = {};
__export(index_exports, {
  bocha: () => bocha
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bocha-search/src/lib/actions/web-search.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bocha-search/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bocha-search/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function makeRequest({
  token,
  method,
  path,
  body
}) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token
    },
    headers: {
      "Content-Type": "application/json"
    },
    body
  })).body;
}
var BASE_URL = "https://api.bochaai.com/v1";

// ../ap/packages/pieces/community/bocha-search/src/lib/common/auth.ts
var markdownDescription = `
Follow these steps to obtain your Bocha API Key:

1. Visit [Bocha AI Open Platform](https://open.bochaai.com) and create an account.
2. Navigate to **API KEY Management** in the dashboard.
3. Copy your API key.
`, bochaAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await makeRequest({
        token: auth,
        method: import_pieces_common2.HttpMethod.POST,
        path: "/web-search",
        body: { query: "test", count: 1 }
      }), {
        valid: !0
      };
    } catch (e) {
      let response = isRecord(e) ? e.response : void 0, status = isRecord(response) && typeof response.status == "number" ? response.status : void 0;
      return status === 401 || status === 403 ? { valid: !1, error: "Invalid API Key" } : { valid: !1, error: "Could not connect to Bocha API. Please try again." };
    }
  }
});
function isRecord(value) {
  return typeof value == "object" && value !== null;
}

// ../ap/packages/pieces/community/bocha-search/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), freshnessDropdown = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Freshness",
  description: "Filter results by time range. Defaults to no limit.",
  required: !1,
  defaultValue: "noLimit",
  options: {
    options: [
      { label: "No Limit", value: "noLimit" },
      { label: "One Day", value: "oneDay" },
      { label: "One Week", value: "oneWeek" },
      { label: "One Month", value: "oneMonth" },
      { label: "One Year", value: "oneYear" }
    ]
  }
}), countProp = import_pieces_framework2.Property.Number({
  displayName: "Count",
  description: "Number of results to return (1-50). Defaults to 10.",
  required: !1,
  defaultValue: 10
});

// ../ap/packages/pieces/community/bocha-search/src/lib/actions/web-search.ts
var webSearchAction = (0, import_pieces_framework3.createAction)({
  name: "web_search",
  classification: "SEARCH",
  displayName: "Web Search",
  description: "Search the web using Bocha.",
  audience: "both",
  aiMetadata: {
    description: "Runs a Bocha web search for a query and returns matching web pages, optionally with per-result text summaries. Pick this to look up current information on the public web; results can be narrowed by freshness window, capped result count, and include/exclude domain lists (up to 100 domains each, separated by | or ,). Read-only and idempotent \u2014 repeating the same query just fetches results again.",
    idempotent: !0
  },
  auth: bochaAuth,
  props: {
    query: import_pieces_framework3.Property.ShortText({
      displayName: "Query",
      description: "The search query.",
      required: !0
    }),
    freshness: freshnessDropdown,
    summary: import_pieces_framework3.Property.Checkbox({
      displayName: "Show Summary",
      description: "Include a text summary for each search result.",
      required: !1,
      defaultValue: !0
    }),
    include: import_pieces_framework3.Property.ShortText({
      displayName: "Include Domains",
      description: "Limit search to specific domains. Separate multiple domains with | or , (max 100).",
      required: !1
    }),
    exclude: import_pieces_framework3.Property.ShortText({
      displayName: "Exclude Domains",
      description: "Exclude specific domains from search results. Separate multiple domains with | or , (max 100).",
      required: !1
    }),
    count: countProp
  },
  async run({ auth, propsValue }) {
    var _a;
    let body = {
      query: propsValue.query,
      count: propsValue.count
    };
    return propsValue.freshness && (body.freshness = propsValue.freshness), propsValue.summary !== void 0 && propsValue.summary !== null && (body.summary = propsValue.summary), propsValue.include && (body.include = propsValue.include), propsValue.exclude && (body.exclude = propsValue.exclude), (_a = (await makeRequest({
      token: auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/web-search",
      body
    })).data) != null ? _a : {};
  }
});

// ../ap/packages/pieces/community/bocha-search/src/index.ts
var bocha = (0, import_pieces_framework4.createPiece)({
  displayName: "Bocha",
  description: "Web search API for AI agents, providing real-time web pages, images, and news.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/bocha-search.png",
  categories: [import_pieces_framework5.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["BochaLab"],
  auth: bochaAuth,
  actions: [
    webSearchAction,
    (0, import_pieces_common4.createCustomApiCallAction)({
      baseUrl: () => BASE_URL,
      auth: bochaAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  bocha
});
