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

// ../ap/packages/pieces/community/sofya/src/index.ts
var index_exports = {};
__export(index_exports, {
  sofya: () => sofya
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sofya/src/lib/actions/search.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/sofya/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/sofya/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://sofya.co/v1";
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

// ../ap/packages/pieces/community/sofya/src/lib/common/auth.ts
var markdownDescription = `
Follow these steps to obtain your Sofya API Key:

1. Visit [Sofya](https://sofya.co) and create an account (sign up with GitHub for 1,000 free credits per month on most accounts).
2. Open your [dashboard](https://sofya.co/dashboard).
3. Copy your API key (it starts with \`ay_live_\`).
`, sofyaAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: markdownDescription,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await makeRequest({
        token: auth,
        method: import_pieces_common2.HttpMethod.GET,
        path: "/auth/me"
      }), { valid: !0 };
    } catch (e) {
      let response = isRecord(e) ? e.response : void 0, status = isRecord(response) && typeof response.status == "number" ? response.status : void 0;
      return status === 401 || status === 403 ? { valid: !1, error: "Invalid API Key" } : {
        valid: !1,
        error: "Could not connect to Sofya. Please try again."
      };
    }
  }
});
function isRecord(value) {
  return typeof value == "object" && value !== null;
}

// ../ap/packages/pieces/community/sofya/src/lib/actions/search.ts
var searchAction = (0, import_pieces_framework2.createAction)({
  name: "search",
  displayName: "Search the Web",
  description: "Search the web and get full page content, not just snippets.",
  audience: "both",
  aiMetadata: {
    description: "Run a web search through Sofya for a natural-language query and return extracted page content (not just snippets), optionally with an AI-synthesized answer. Narrow scope with a general vs. news topic, a recency window, result count, and domain include/exclude lists. Use to look up current or external information from the web. Read-only and idempotent: the same query returns equivalent results, though live web data may shift over time.",
    idempotent: !0
  },
  auth: sofyaAuth,
  props: {
    query: import_pieces_framework2.Property.LongText({
      displayName: "Query",
      description: "The search query.",
      required: !0
    }),
    search_depth: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Search Depth",
      description: '"basic" fetches pages and returns extracted content (3 credits). "snippets" returns SERP snippets only, without fetching pages (1 credit).',
      required: !1,
      defaultValue: "basic",
      options: {
        options: [
          { label: "Basic", value: "basic" },
          { label: "Snippets", value: "snippets" }
        ]
      }
    }),
    topic: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Topic",
      description: 'Use "news" for current events, breaking news, or any time-sensitive query.',
      required: !1,
      defaultValue: "general",
      options: {
        options: [
          { label: "General", value: "general" },
          { label: "News", value: "news" }
        ]
      }
    }),
    max_results: import_pieces_framework2.Property.Number({
      displayName: "Maximum Results",
      description: "Number of results to return (1-20).",
      required: !1,
      defaultValue: 10
    }),
    include_answer: import_pieces_framework2.Property.Checkbox({
      displayName: "Include Answer",
      description: "Add an AI-synthesized answer based on the search results (adds 5 credits).",
      required: !1,
      defaultValue: !1
    }),
    freshness: import_pieces_framework2.Property.ShortText({
      displayName: "Freshness",
      description: 'Filter by recency: "day", "week", "month", "year", or a range "YYYY-MM-DD:YYYY-MM-DD".',
      required: !1
    }),
    include_domains: import_pieces_framework2.Property.Array({
      displayName: "Include Domains",
      description: "Only include results from these domains (max 10).",
      required: !1
    }),
    exclude_domains: import_pieces_framework2.Property.Array({
      displayName: "Exclude Domains",
      description: "Exclude results from these domains (max 10).",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      query: propsValue.query,
      search_depth: propsValue.search_depth,
      topic: propsValue.topic,
      max_results: propsValue.max_results,
      include_answer: propsValue.include_answer
    };
    return propsValue.freshness && (body.freshness = propsValue.freshness), propsValue.include_domains && propsValue.include_domains.length > 0 && (body.include_domains = propsValue.include_domains), propsValue.exclude_domains && propsValue.exclude_domains.length > 0 && (body.exclude_domains = propsValue.exclude_domains), await makeRequest({
      token: auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/search",
      body
    });
  }
});

// ../ap/packages/pieces/community/sofya/src/lib/actions/fetch.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var fetchAction = (0, import_pieces_framework3.createAction)({
  name: "fetch",
  displayName: "Fetch URL Content",
  description: "Fetch URLs and return clean markdown. Supports web pages, PDFs, and documents.",
  audience: "both",
  aiMetadata: {
    description: "Fetch one or more URLs and return their content as clean markdown (also supports PDFs and documents). Use when you already have exact URLs (e.g. from a prior search) and need their full text rather than running a new query. Costs 1 credit per URL, up to 10 URLs; failed URLs are not charged. Read-only and idempotent.",
    idempotent: !0
  },
  auth: sofyaAuth,
  props: {
    urls: import_pieces_framework3.Property.Array({
      displayName: "URLs",
      description: "The URLs to fetch (max 10).",
      required: !0
    }),
    include_raw_html: import_pieces_framework3.Property.Checkbox({
      displayName: "Include Raw HTML",
      description: "Also include the raw HTML source in each result (null for non-HTML content such as PDFs).",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    return await makeRequest({
      token: auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/fetch",
      body: {
        urls: propsValue.urls,
        include_raw_html: propsValue.include_raw_html
      }
    });
  }
});

// ../ap/packages/pieces/community/sofya/src/lib/actions/extract.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var extractAction = (0, import_pieces_framework4.createAction)({
  name: "extract",
  displayName: "Extract Data from Page",
  description: "Use AI to pull specific data from a webpage \u2014 pricing, specs, contacts, and more.",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single web page and use AI to extract specific information described by a prompt (e.g. pricing tiers, specs, author and date), returning targeted text rather than the full page. If the page has no usable text, the response reports low content instead of fabricating an answer. Costs 5 credits. Read-only and idempotent.",
    idempotent: !0
  },
  auth: sofyaAuth,
  props: {
    url: import_pieces_framework4.Property.ShortText({
      displayName: "URL",
      description: "The URL to extract information from.",
      required: !0
    }),
    prompt: import_pieces_framework4.Property.LongText({
      displayName: "Prompt",
      description: 'What information to extract (e.g. "list all pricing tiers with features").',
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return await makeRequest({
      token: auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/extract",
      body: {
        url: propsValue.url,
        prompt: propsValue.prompt
      }
    });
  }
});

// ../ap/packages/pieces/community/sofya/src/lib/actions/research.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var researchAction = (0, import_pieces_framework5.createAction)({
  name: "research",
  displayName: "Deep Research",
  description: "Research any topic \u2014 searches up to 30 sources and synthesizes a cited report.",
  audience: "both",
  aiMetadata: {
    description: "Perform comprehensive research on a topic: Sofya decomposes the query into sub-queries, searches and reads multiple sources in parallel, then synthesizes a structured report with citations. Best for open-ended or comparative questions that need coverage from many angles; for simple lookups use Search instead. Costs 25 credits. Not idempotent: it selects sources and synthesizes a report at request time, so repeated calls with the same query can differ.",
    idempotent: !1
  },
  auth: sofyaAuth,
  props: {
    query: import_pieces_framework5.Property.LongText({
      displayName: "Query",
      description: "The research question or topic.",
      required: !0
    }),
    topic: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Topic",
      description: 'Use "news" to prioritize recent news articles.',
      required: !1,
      defaultValue: "general",
      options: {
        options: [
          { label: "General", value: "general" },
          { label: "News", value: "news" }
        ]
      }
    }),
    max_sources: import_pieces_framework5.Property.Number({
      displayName: "Maximum Sources",
      description: "Maximum number of sources to use (5-30).",
      required: !1,
      defaultValue: 20
    }),
    freshness: import_pieces_framework5.Property.ShortText({
      displayName: "Freshness",
      description: 'Filter by recency: "day", "week", "month", "year", or a range "YYYY-MM-DD:YYYY-MM-DD".',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      query: propsValue.query,
      topic: propsValue.topic,
      max_sources: propsValue.max_sources
    };
    return propsValue.freshness && (body.freshness = propsValue.freshness), await makeRequest({
      token: auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      path: "/research",
      body
    });
  }
});

// ../ap/packages/pieces/community/sofya/src/index.ts
var sofya = (0, import_pieces_framework6.createPiece)({
  displayName: "Sofya",
  description: "Web tools for AI agents: search the web, fetch pages as clean markdown, extract structured data with AI, and run deep multi-source research.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/sofya.png",
  categories: [import_pieces_framework7.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["yusufgurdogan"],
  auth: sofyaAuth,
  actions: [
    searchAction,
    fetchAction,
    extractAction,
    researchAction,
    (0, import_pieces_common7.createCustomApiCallAction)({
      baseUrl: () => BASE_URL,
      auth: sofyaAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  sofya
});
