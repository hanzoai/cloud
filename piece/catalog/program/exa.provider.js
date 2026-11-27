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

// ../ap/packages/pieces/community/exa/src/index.ts
var index_exports = {};
__export(index_exports, {
  exa: () => exa
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/exa/src/lib/actions/get-contents.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/exa/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.exa.ai";
async function makeRequest(auth, method, path, body) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      "x-api-key": `${auth}`,
      "Content-Type": "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/exa/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var markdownDescription = `
Obtain your API key from [Dashboard Setting](https://dashboard.exa.ai/api-keys).
`, exaAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await makeRequest(
        auth,
        import_pieces_common2.HttpMethod.POST,
        "/search",
        { query: "Activepieces" }
      ), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key."
      };
    }
  }
});

// ../ap/packages/pieces/community/exa/src/lib/actions/get-contents.ts
var getContentsAction = (0, import_pieces_framework2.createAction)({
  name: "get_contents",
  displayName: "Get Contents",
  description: "Retrieve clean HTML content from specified URLs.",
  audience: "both",
  aiMetadata: {
    description: "Crawls and extracts clean page content (and optionally full text) from a given list of URLs via Exa. Use when an agent already has specific URLs and needs their on-page content rather than running a web search; optionally fetch matching subpages by keyword. Requires an array of URLs as input. Read-only and idempotent.",
    idempotent: !0
  },
  auth: exaAuth,
  props: {
    urls: import_pieces_framework2.Property.Array({
      displayName: "URLs",
      required: !0,
      description: "Array of URLs to crawl"
    }),
    text: import_pieces_framework2.Property.Checkbox({
      displayName: "Return Full Text",
      description: "If true, returns full page text. If false, disables text return.",
      required: !1,
      defaultValue: !0
    }),
    livecrawl: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Livecrawl Option",
      description: "Options for livecrawling pages.",
      required: !1,
      options: {
        options: [
          { label: "Never", value: "never" },
          { label: "Fallback", value: "fallback" },
          { label: "Always", value: "always" },
          { label: "Auto", value: "auto" }
        ]
      }
    }),
    livecrawlTimeout: import_pieces_framework2.Property.Number({
      displayName: "Livecrawl Timeout (ms)",
      description: "Timeout for livecrawling in milliseconds.",
      required: !1
    }),
    subpages: import_pieces_framework2.Property.Number({
      displayName: "Number of Subpages",
      description: "Number of subpages to crawl.",
      required: !1
    }),
    subpageTarget: import_pieces_framework2.Property.ShortText({
      displayName: "Subpage Target",
      description: "Keyword(s) to find specific subpages.",
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, body = {
      urls: context.propsValue.urls
    };
    return context.propsValue.text !== void 0 && (body.text = context.propsValue.text), context.propsValue.livecrawl && (body.livecrawl = context.propsValue.livecrawl), context.propsValue.livecrawlTimeout !== void 0 && (body.livecrawlTimeout = context.propsValue.livecrawlTimeout), context.propsValue.subpages !== void 0 && (body.subpages = context.propsValue.subpages), context.propsValue.subpageTarget && (body.subpageTarget = context.propsValue.subpageTarget), (await makeRequest(apiKey, import_pieces_common3.HttpMethod.POST, "/contents", body)).results;
  }
});

// ../ap/packages/pieces/community/exa/src/lib/actions/generate-answer.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var generateAnswerAction = (0, import_pieces_framework3.createAction)({
  name: "generate_answer",
  displayName: "Ask AI",
  description: "Provides direct answers to queries by summarizing results.",
  audience: "both",
  aiMetadata: {
    description: "Asks Exa a natural-language question and returns a synthesized answer grounded in live web search results. Use when an agent wants a direct factual answer rather than a list of links to triage itself; choose the exa or exa-pro model. Requires a query string. Read-only lookup; repeating the same query is safe though phrasing and live web data may vary the wording.",
    idempotent: !0
  },
  auth: exaAuth,
  props: {
    query: import_pieces_framework3.Property.ShortText({
      displayName: "Query",
      description: "Ask a question to get summarized answers from the web.",
      required: !0
    }),
    text: import_pieces_framework3.Property.Checkbox({
      displayName: "Include Text Content",
      description: "If true, includes full text content from the search results",
      required: !1,
      defaultValue: !1
    }),
    model: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Model",
      description: "Choose the Exa model to use for the answer.",
      required: !0,
      options: {
        options: [
          { label: "Exa", value: "exa" },
          { label: "Exa Pro", value: "exa-pro" }
        ]
      },
      defaultValue: "exa"
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, {
      query,
      text,
      model
    } = context.propsValue, body = {
      query,
      text,
      model
    };
    return (await makeRequest(apiKey, import_pieces_common4.HttpMethod.POST, "/answer", body)).answer;
  }
});

// ../ap/packages/pieces/community/exa/src/lib/actions/perform-search.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var performSearchAction = (0, import_pieces_framework4.createAction)({
  name: "perform_search",
  displayName: "Perform Search",
  description: "Search the web using semantic or keyword-based search.",
  audience: "both",
  aiMetadata: {
    description: "Runs a web search through Exa and returns matching results with page text. Use when an agent needs to discover relevant URLs/content for a query; the search type can be auto, neural (semantic), or keyword, and results can be narrowed by category, domain include/exclude lists, crawl/publish date ranges, and required/forbidden text. Requires a query string. Read-only and idempotent.",
    idempotent: !0
  },
  auth: exaAuth,
  props: {
    query: import_pieces_framework4.Property.ShortText({
      displayName: "Query",
      description: "Search query to find related articles and data.",
      required: !0
    }),
    type: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Search Type",
      description: "Type of search to perform.",
      required: !1,
      defaultValue: "auto",
      options: {
        options: [
          { label: "Auto", value: "auto" },
          { label: "Keyword", value: "keyword" },
          { label: "Neural", value: "neural" }
        ]
      }
    }),
    category: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Category",
      description: "Category of data to focus the search on.",
      required: !1,
      options: {
        options: [
          { label: "Company", value: "company" },
          { label: "Research Paper", value: "research paper" },
          { label: "News", value: "news" },
          { label: "PDF", value: "pdf" },
          { label: "GitHub", value: "github" },
          { label: "Tweet", value: "tweet" },
          { label: "Personal Site", value: "personal site" },
          { label: "LinkedIn Profile", value: "linkedin profile" },
          { label: "Financial Report", value: "financial report" }
        ]
      }
    }),
    numResults: import_pieces_framework4.Property.Number({
      displayName: "Number of Results",
      description: "Number of results to return (max 100).",
      required: !1,
      defaultValue: 10
    }),
    includeDomains: import_pieces_framework4.Property.Array({
      displayName: "Include Domains",
      description: "Limit results to only these domains.",
      required: !1
    }),
    excludeDomains: import_pieces_framework4.Property.Array({
      displayName: "Exclude Domains",
      description: "Exclude results from these domains.",
      required: !1
    }),
    startCrawlDate: import_pieces_framework4.Property.DateTime({
      displayName: "Start Crawl Date",
      description: "Only include results crawled after this ISO date.",
      required: !1
    }),
    endCrawlDate: import_pieces_framework4.Property.DateTime({
      displayName: "End Crawl Date",
      description: "Only include results crawled before this ISO date.",
      required: !1
    }),
    startPublishedDate: import_pieces_framework4.Property.DateTime({
      displayName: "Start Published Date",
      description: "Only include results published after this ISO date.",
      required: !1
    }),
    endPublishedDate: import_pieces_framework4.Property.DateTime({
      displayName: "End Published Date",
      description: "Only include results published before this ISO date.",
      required: !1
    }),
    includeText: import_pieces_framework4.Property.Array({
      displayName: "Include Text",
      description: "Strings that must be present in the text of results.",
      required: !1
    }),
    excludeText: import_pieces_framework4.Property.Array({
      displayName: "Exclude Text",
      description: "Strings that must not be present in the text of results.",
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, body = {
      query: context.propsValue.query,
      contents: {
        text: !0
      }
    }, optionalProps = [
      "type",
      "category",
      "numResults",
      "includeDomains",
      "excludeDomains",
      "startCrawlDate",
      "endCrawlDate",
      "startPublishedDate",
      "endPublishedDate",
      "includeText",
      "excludeText"
    ];
    for (let prop of optionalProps) {
      let val = context.propsValue[prop];
      val != null && val !== "" && (body[prop] = val);
    }
    return (await makeRequest(apiKey, import_pieces_common5.HttpMethod.POST, "/search", body)).results;
  }
});

// ../ap/packages/pieces/community/exa/src/lib/actions/find-similar-links.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var findSimilarLinksAction = (0, import_pieces_framework5.createAction)({
  name: "find_similar_links",
  displayName: "Find Similar Links",
  description: "Find pages similar to a given URL.",
  audience: "both",
  aiMetadata: {
    description: "Finds web pages semantically similar to a single reference URL via Exa. Use when an agent has one example page and wants more of the same kind rather than searching by a text query; results can be narrowed by domain include/exclude lists, crawl/publish date ranges, and required/forbidden text. Requires a reference URL. Read-only and idempotent.",
    idempotent: !0
  },
  auth: exaAuth,
  props: {
    url: import_pieces_framework5.Property.ShortText({
      displayName: "URL",
      description: "Reference URL to find semantically similar links.",
      required: !0
    }),
    numResults: import_pieces_framework5.Property.Number({
      displayName: "Number of Results",
      description: "Number of results to return (max 100).",
      required: !1
    }),
    includeDomains: import_pieces_framework5.Property.Array({
      displayName: "Include Domains",
      description: "List of domains to include in results.",
      required: !1
    }),
    excludeDomains: import_pieces_framework5.Property.Array({
      displayName: "Exclude Domains",
      description: "List of domains to exclude from results.",
      required: !1
    }),
    startCrawlDate: import_pieces_framework5.Property.DateTime({
      displayName: "Start Crawl Date (ISO)",
      description: "Include links crawled after this date (ISO format).",
      required: !1
    }),
    endCrawlDate: import_pieces_framework5.Property.DateTime({
      displayName: "End Crawl Date (ISO)",
      description: "Include links crawled before this date (ISO format).",
      required: !1
    }),
    startPublishedDate: import_pieces_framework5.Property.DateTime({
      displayName: "Start Published Date (ISO)",
      description: "Only include links published after this date (ISO format).",
      required: !1
    }),
    endPublishedDate: import_pieces_framework5.Property.DateTime({
      displayName: "End Published Date (ISO)",
      description: "Only include links published before this date (ISO format).",
      required: !1
    }),
    includeText: import_pieces_framework5.Property.Array({
      displayName: "Include Text",
      description: "Strings that must be present in the webpage text (max 1 string of up to 5 words).",
      required: !1
    }),
    excludeText: import_pieces_framework5.Property.Array({
      displayName: "Exclude Text",
      description: "Strings that must not be present in the webpage text (max 1 string of up to 5 words).",
      required: !1
    })
  },
  async run(context) {
    let apiKey = context.auth.secret_text, {
      url,
      numResults,
      includeDomains,
      excludeDomains,
      startCrawlDate,
      endCrawlDate,
      startPublishedDate,
      endPublishedDate,
      includeText,
      excludeText
    } = context.propsValue, body = {
      url
    };
    return numResults !== void 0 && (body.numResults = numResults), includeDomains && (body.includeDomains = includeDomains), excludeDomains && (body.excludeDomains = excludeDomains), startCrawlDate && (body.startCrawlDate = startCrawlDate), endCrawlDate && (body.endCrawlDate = endCrawlDate), startPublishedDate && (body.startPublishedDate = startPublishedDate), endPublishedDate && (body.endPublishedDate = endPublishedDate), includeText && (body.includeText = includeText), excludeText && (body.excludeText = excludeText), (await makeRequest(apiKey, import_pieces_common6.HttpMethod.POST, "/findSimilar", body)).results;
  }
});

// ../ap/packages/pieces/community/exa/src/index.ts
var import_pieces_common7 = require("@activepieces/pieces-common");
var exa = (0, import_pieces_framework6.createPiece)({
  displayName: "Exa",
  description: "AI-powered search and content extraction from the web.",
  auth: exaAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/exa.png",
  categories: [import_pieces_framework7.PieceCategory.ARTIFICIAL_INTELLIGENCE, import_pieces_framework7.PieceCategory.PRODUCTIVITY],
  authors: ["krushnarout", "kishanprmr"],
  actions: [
    getContentsAction,
    generateAnswerAction,
    performSearchAction,
    findSimilarLinksAction,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: exaAuth,
      baseUrl: () => "https://api.exa.ai",
      authMapping: async (auth) => ({
        "x-api-key": `${auth}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  exa
});
