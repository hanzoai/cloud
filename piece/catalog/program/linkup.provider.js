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

// ../ap/packages/pieces/community/linkup/src/index.ts
var index_exports = {};
__export(index_exports, {
  linkup: () => linkup
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/linkup/src/lib/common/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), BASE_URL = "https://api.linkup.so", linkupAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To get your API Key:

1. Go to your Linkup account dashboard
2. Navigate to API settings
3. Copy your API key
4. Paste it here
`,
  required: !0,
  validate: async ({ auth }) => {
    if (!auth)
      return {
        valid: !1,
        error: "API key is required"
      };
    try {
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${BASE_URL}/v1/credits/balance`,
        headers: {
          Authorization: `Bearer ${auth}`,
          "Content-Type": "application/json"
        }
      })).status === 200 ? {
        valid: !0
      } : {
        valid: !1,
        error: "Invalid API key"
      };
    } catch (error) {
      return {
        valid: !1,
        error: "Invalid API key. Please check your API key and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/linkup/src/lib/actions/search.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/linkup/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), BASE_URL2 = "https://api.linkup.so";
async function linkupApiCall({
  method,
  path,
  queryParams,
  body,
  auth
}) {
  var _a, _b;
  let url = `${BASE_URL2}${path}`, headers = {
    Authorization: `Bearer ${auth.secret_text}`,
    "Content-Type": "application/json"
  }, qs = {};
  if (queryParams)
    for (let [key, value] of Object.entries(queryParams))
      value != null && (qs[key] = String(value));
  try {
    return (await import_pieces_common2.httpClient.sendRequest({
      method,
      url,
      headers,
      queryParams: qs,
      body
    })).body;
  } catch (error) {
    let statusCode = ((_a = error.response) == null ? void 0 : _a.status) || error.status, errorBody = ((_b = error.response) == null ? void 0 : _b.body) || error.body;
    if (statusCode === 401 || statusCode === 403)
      throw new Error("Authentication failed. Please check your API key.");
    if (statusCode === 429)
      throw new Error("Rate limit exceeded. Please wait a moment and try again.");
    if (statusCode >= 400 && statusCode < 500) {
      let errorMessage = (errorBody == null ? void 0 : errorBody.message) || (errorBody == null ? void 0 : errorBody.error) || error.message || "Request failed";
      throw new Error(`Request failed: ${errorMessage}`);
    }
    throw error;
  }
}

// ../ap/packages/pieces/community/linkup/src/lib/actions/search.ts
var search = (0, import_pieces_framework2.createAction)({
  auth: linkupAuth,
  name: "search",
  displayName: "Search the Web",
  description: "Search the web for relevant content based on a natural language query. Perfect for grounding LLM responses with factual data.",
  audience: "both",
  aiMetadata: { description: "Runs a web search through Linkup for a natural-language query and returns grounding data from the live internet. Choose it to retrieve up-to-date facts, sources, or answers to enrich an LLM response. The Output Type selects what comes back: searchResults (raw matches), sourcedAnswer (a synthesized answer with citations), or structured (custom JSON shaped by the schema you supply, which is required in that mode); narrow scope with depth, date range, and include/exclude domain filters. Read-only and idempotent.", idempotent: !0 },
  props: {
    q: import_pieces_framework2.Property.ShortText({
      displayName: "Query",
      description: "The natural language question or query to search for",
      required: !0
    }),
    depth: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Search Depth",
      description: "Standard returns results faster; deep takes longer but yields more comprehensive results",
      required: !0,
      defaultValue: "standard",
      options: {
        options: [
          { label: "Standard", value: "standard" },
          { label: "Deep", value: "deep" }
        ]
      }
    }),
    outputType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Output Type",
      description: "Format of the response. Use structured for custom JSON format.",
      required: !0,
      defaultValue: "searchResults",
      options: {
        options: [
          { label: "Search Results", value: "searchResults" },
          { label: "Sourced Answer", value: "sourcedAnswer" },
          { label: "Structured", value: "structured" }
        ]
      }
    }),
    structuredOutputSchema: import_pieces_framework2.Property.LongText({
      displayName: "Structured Output Schema",
      description: 'JSON schema (as a string) for the desired response format. Required only when Output Type is "structured".',
      required: !1
    }),
    includeSources: import_pieces_framework2.Property.Checkbox({
      displayName: "Include Sources",
      description: 'Include sources in the response. Only relevant when Output Type is "structured".',
      required: !1,
      defaultValue: !1
    }),
    includeImages: import_pieces_framework2.Property.Checkbox({
      displayName: "Include Images",
      description: "Include images in search results",
      required: !1,
      defaultValue: !1
    }),
    fromDate: import_pieces_framework2.Property.ShortText({
      displayName: "From Date",
      description: "Start date for search results in ISO 8601 format (YYYY-MM-DD), e.g., 2025-01-01",
      required: !1
    }),
    toDate: import_pieces_framework2.Property.ShortText({
      displayName: "To Date",
      description: "End date for search results in ISO 8601 format (YYYY-MM-DD), e.g., 2025-01-01",
      required: !1
    }),
    includeDomains: import_pieces_framework2.Property.Array({
      displayName: "Include Domains",
      description: "Domains to search within (up to 100 domains). Leave empty to search all domains.",
      required: !1
    }),
    excludeDomains: import_pieces_framework2.Property.Array({
      displayName: "Exclude Domains",
      description: "Domains to exclude from search results",
      required: !1
    }),
    includeInlineCitations: import_pieces_framework2.Property.Checkbox({
      displayName: "Include Inline Citations",
      description: 'Include inline citations in the answer. Only relevant when Output Type is "sourcedAnswer".',
      required: !1,
      defaultValue: !1
    }),
    maxResults: import_pieces_framework2.Property.Number({
      displayName: "Max Results",
      description: "Maximum number of results to return",
      required: !1
    })
  },
  async run(context) {
    let {
      q,
      depth,
      outputType,
      structuredOutputSchema,
      includeSources,
      includeImages,
      fromDate,
      toDate,
      includeDomains,
      excludeDomains,
      includeInlineCitations,
      maxResults
    } = context.propsValue;
    if (outputType === "structured" && !structuredOutputSchema)
      throw new Error('Structured Output Schema is required when Output Type is "structured"');
    let body = {
      q,
      depth,
      outputType
    };
    return outputType === "structured" && structuredOutputSchema && (body.structuredOutputSchema = structuredOutputSchema), outputType === "structured" && includeSources !== void 0 && (body.includeSources = includeSources), outputType === "sourcedAnswer" && includeInlineCitations !== void 0 && (body.includeInlineCitations = includeInlineCitations), includeImages !== void 0 && (body.includeImages = includeImages), fromDate && (body.fromDate = fromDate), toDate && (body.toDate = toDate), includeDomains && includeDomains.length > 0 && (body.includeDomains = includeDomains), excludeDomains && excludeDomains.length > 0 && (body.excludeDomains = excludeDomains), maxResults != null && (body.maxResults = maxResults), await linkupApiCall({
      method: import_pieces_common3.HttpMethod.POST,
      path: "/v1/search",
      body,
      auth: context.auth
    });
  }
});

// ../ap/packages/pieces/community/linkup/src/lib/actions/fetch.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var fetch = (0, import_pieces_framework3.createAction)({
  auth: linkupAuth,
  name: "fetch",
  displayName: "Fetch Webpage",
  description: "Fetch a webpage and convert it to markdown format. Optionally render JavaScript, include raw HTML, or extract images.",
  audience: "both",
  aiMetadata: { description: "Retrieves a single webpage by URL through Linkup and returns its content as markdown. Use it to read the contents of a specific known page (not to search for pages). Optionally render JavaScript before fetching, include the raw HTML, or extract images. Read-only and idempotent.", idempotent: !0 },
  props: {
    url: import_pieces_framework3.Property.ShortText({
      displayName: "URL",
      description: "The URL of the webpage to fetch",
      required: !0
    }),
    renderJs: import_pieces_framework3.Property.Checkbox({
      displayName: "Render JavaScript",
      description: "Render JavaScript on the webpage before fetching",
      required: !1,
      defaultValue: !1
    }),
    includeRawHtml: import_pieces_framework3.Property.Checkbox({
      displayName: "Include Raw HTML",
      description: "Include the raw HTML in the response",
      required: !1,
      defaultValue: !1
    }),
    extractImages: import_pieces_framework3.Property.Checkbox({
      displayName: "Extract Images",
      description: "Extract images from the webpage",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { url, renderJs, includeRawHtml, extractImages } = context.propsValue, body = {
      url
    };
    return renderJs !== void 0 && (body.renderJs = renderJs), includeRawHtml !== void 0 && (body.includeRawHtml = includeRawHtml), extractImages !== void 0 && (body.extractImages = extractImages), await linkupApiCall({
      method: import_pieces_common4.HttpMethod.POST,
      path: "/v1/fetch",
      body,
      auth: context.auth
    });
  }
});

// ../ap/packages/pieces/community/linkup/src/index.ts
var linkup = (0, import_pieces_framework4.createPiece)({
  displayName: "Linkup",
  description: "Linkup is a web search engine for AI apps. Connect your AI application to the internet and get grounding data to enrich your AI's output.",
  auth: linkupAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/linkup.png",
  categories: [import_pieces_framework5.PieceCategory.DEVELOPER_TOOLS],
  authors: ["onyedikachi-david"],
  actions: [search, fetch],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  linkup
});
