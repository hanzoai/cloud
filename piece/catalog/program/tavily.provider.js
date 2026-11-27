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

// ../ap/packages/pieces/community/tavily/src/index.ts
var index_exports = {};
__export(index_exports, {
  tavily: () => tavily
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tavily/src/lib/actions/search.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/tavily/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), markdownDescription = `
Follow these steps to obtain your Tavily API Key:

1. Visit [tavily](https://tavily.com/) and create an account.
2. Log in and navigate to your dashboard.
3. Locate and copy your API key from the dashboard.
`, tavilyAuth = import_pieces_framework.PieceAuth.SecretText({
  description: markdownDescription,
  displayName: "API Key",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.tavily.com/usage",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
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

// ../ap/packages/pieces/community/tavily/src/lib/actions/search.ts
var searchAction = (0, import_pieces_framework2.createAction)({
  name: "search",
  classification: "SEARCH",
  displayName: "Search",
  description: "Search for data based on a query.",
  audience: "both",
  aiMetadata: { description: "Run a web search through the Tavily search engine for a natural-language query and return relevant results; tune scope with search depth, a general vs. news topic, recency windows, and domain include/exclude lists, and optionally include images, raw page content, or an LLM-generated answer summary. Use to look up current or external information from the web. Read-only and idempotent: the same query returns equivalent results without side effects, though live web data may shift over time.", idempotent: !0 },
  auth: tavilyAuth,
  props: {
    query: import_pieces_framework2.Property.LongText({
      displayName: "Search Query",
      description: "The search query you want to execute with Tavily.",
      required: !0
    }),
    search_depth: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Search Depth",
      description: 'The depth of the search. It can be "basic" or "advanced".',
      required: !1,
      defaultValue: "basic",
      options: {
        options: [
          { label: "Basic", value: "basic" },
          { label: "Advanced", value: "advanced" }
        ]
      }
    }),
    topic: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Topic",
      description: "The category of the search. This will determine which of our agents will be used for the search.",
      required: !1,
      defaultValue: "general",
      options: {
        options: [
          { label: "General", value: "general" },
          { label: "News", value: "news" }
        ]
      }
    }),
    days: import_pieces_framework2.Property.Number({
      displayName: "Days",
      description: 'The number of days back from the current date to include in the search results. Only available when using the "news" search topic. Default is 3.',
      required: !1,
      defaultValue: 3
    }),
    time_range: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Time Range",
      description: "The time range back from the current date to include in the search results.",
      required: !1,
      options: {
        options: [
          { label: "Day", value: "day" },
          { label: "Week", value: "week" },
          { label: "Month", value: "month" },
          { label: "Year", value: "year" },
          { label: "Day (Short)", value: "d" },
          { label: "Week (Short)", value: "w" },
          { label: "Month (Short)", value: "m" },
          { label: "Year (Short)", value: "y" }
        ]
      }
    }),
    max_results: import_pieces_framework2.Property.Number({
      displayName: "Maximum Results",
      description: "The maximum number of search results to return.",
      required: !1,
      defaultValue: 5
    }),
    include_images: import_pieces_framework2.Property.Checkbox({
      displayName: "Include Images",
      description: "Include a list of query-related images in the response.",
      required: !1,
      defaultValue: !1
    }),
    include_image_descriptions: import_pieces_framework2.Property.Checkbox({
      displayName: "Include Image Descriptions",
      description: "When include_images is set to True, this option adds descriptive text for each image.",
      required: !1,
      defaultValue: !1
    }),
    include_answer: import_pieces_framework2.Property.Checkbox({
      displayName: "Include Answer",
      description: "Include a short answer to original query, generated by an LLM based on Tavily's search results.",
      required: !1,
      defaultValue: !1
    }),
    include_raw_content: import_pieces_framework2.Property.Checkbox({
      displayName: "Include Raw Content",
      description: "Include the cleaned and parsed HTML content of each search result.",
      required: !1,
      defaultValue: !1
    }),
    include_domains: import_pieces_framework2.Property.Array({
      displayName: "Include Domains",
      description: "A list of domains to specifically include in the search results.",
      required: !1
    }),
    exclude_domains: import_pieces_framework2.Property.Array({
      displayName: "Exclude Domains",
      description: "A list of domains to specifically exclude from the search results.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.tavily.com/search",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      headers: {
        "Content-Type": "application/json"
      },
      body: {
        query: propsValue.query,
        search_depth: propsValue.search_depth,
        topic: propsValue.topic,
        days: propsValue.days,
        time_range: propsValue.time_range,
        max_results: propsValue.max_results,
        include_images: propsValue.include_images,
        include_image_descriptions: propsValue.include_image_descriptions,
        include_answer: propsValue.include_answer,
        include_raw_content: propsValue.include_raw_content,
        include_domains: propsValue.include_domains,
        exclude_domains: propsValue.exclude_domains
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/tavily/src/lib/actions/extract.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var extractAction = (0, import_pieces_framework3.createAction)({
  name: "extract",
  classification: "READ",
  displayName: "Extract Content",
  description: "Retrieve raw web content from specified URLs.",
  audience: "both",
  aiMetadata: { description: "Fetch and return the cleaned, parsed content of one or more specific web pages by passing their URLs, optionally including images found on those pages. Use when you already have exact URLs (e.g. from a prior search) and need their full text rather than performing a new query. Read-only and idempotent: repeating the call with the same URLs has no side effects.", idempotent: !0 },
  auth: tavilyAuth,
  props: {
    urls: import_pieces_framework3.Property.Array({
      displayName: "URLs",
      description: "The URLs you want to extract with Tavily.",
      required: !0
    }),
    include_images: import_pieces_framework3.Property.Checkbox({
      displayName: "Include Images",
      description: "Include a list of images extracted from the URLs in the response.",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://api.tavily.com/extract",
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      },
      headers: {
        "Content-Type": "application/json"
      },
      body: {
        urls: propsValue.urls,
        include_images: propsValue.include_images
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/tavily/src/index.ts
var tavily = (0, import_pieces_framework4.createPiece)({
  displayName: "Tavily",
  description: "Search engine tailored for AI agents.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/tavily.jpg",
  categories: [import_pieces_framework5.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["OsamaHaikal"],
  auth: tavilyAuth,
  actions: [
    searchAction,
    extractAction,
    (0, import_pieces_common4.createCustomApiCallAction)({
      baseUrl: () => "https://api.tavily.com",
      auth: tavilyAuth,
      authMapping: async (auth) => ({ Authorization: `Bearer ${auth.secret_text}` })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  tavily
});
