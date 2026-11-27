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

// ../ap/packages/pieces/community/dappier/src/index.ts
var index_exports = {};
__export(index_exports, {
  dappier: () => dappier,
  dappierAuth: () => dappierAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/dappier/src/lib/actions/real-time-data.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/dappier/src/lib/common/index.ts
var dappierCommon = {
  baseUrl: "https://api.dappier.com"
};

// ../ap/packages/pieces/community/dappier/src/lib/actions/real-time-data.ts
var realTimeWebSearch = (0, import_pieces_framework.createAction)({
  name: "real_time_web_search",
  auth: dappierAuth,
  displayName: "Real Time Data",
  description: "Access real-time Google web search results including the latest news, weather, travel, deals and more.",
  audience: "both",
  aiMetadata: {
    description: "Runs a real-time Google-style web search via Dappier and returns AI-summarized results for a free-text query covering general topics like news, weather, travel, and deals. Choose this for broad, up-to-the-minute open-web lookups rather than a specific vertical (stocks, sports, or lifestyle). Requires a natural-language search query; read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework.Property.ShortText({
      displayName: "Search Query",
      description: "Enter your search query",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${dappierCommon.baseUrl}/app/aimodel/am_01j0rzq4tvfscrgzwac7jv1p4c`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: {
        query: propsValue.query
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dappier/src/lib/actions/sports-news.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var sportsNewsSearch = (0, import_pieces_framework2.createAction)({
  name: "sports_news_search",
  auth: dappierAuth,
  displayName: "Sports News",
  description: "Real-time news, updates, and personalized content from top sports sources like Sportsnaut, Forever Blueshirts, Minnesota Sports Fan, LAFB Network, Bounding Into Sports, and Ringside Intel.",
  audience: "both",
  aiMetadata: {
    description: "Searches Dappier sports-news sources for articles matching a natural-language query or URL. Choose this for sports-specific content rather than the general web search action. Optionally tune result count, restrict to a preferred domain, and pick the search algorithm (semantic, most recent, most recent + semantic, or trending) to control whether matching is contextual or recency-driven. Read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework2.Property.ShortText({
      displayName: "Search Query",
      description: "Enter a natural language query or URL",
      required: !0
    }),
    similarity_top_k: import_pieces_framework2.Property.Number({
      displayName: "Number of Results",
      description: "Number of articles to return (default is 9)",
      required: !1
    }),
    ref: import_pieces_framework2.Property.ShortText({
      displayName: "Preferred Domain",
      description: "Restrict results to a domain (e.g., sportsnaut.com)",
      required: !1
    }),
    num_articles_ref: import_pieces_framework2.Property.Number({
      displayName: "Guaranteed Articles from Domain",
      description: "Minimum number of articles to match the preferred domain",
      required: !1
    }),
    search_algorithm: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Search Algorithm",
      description: "Choose how to match articles",
      required: !1,
      options: {
        options: [
          { label: "Semantic (Contextual)", value: "semantic" },
          { label: "Most Recent", value: "most_recent" },
          { label: "Most Recent + Semantic", value: "most_recent_semantic" },
          { label: "Trending", value: "trending" }
        ]
      }
    })
  },
  async run({ propsValue, auth }) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${dappierCommon.baseUrl}/app/v2/search?data_model_id=dm_01j0pb465keqmatq9k83dthx34`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: {
        query: propsValue.query,
        similarity_top_k: propsValue.similarity_top_k,
        ref: propsValue.ref,
        num_articles_ref: propsValue.num_articles_ref,
        search_algorithm: propsValue.search_algorithm
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dappier/src/lib/actions/stock-market-data.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var stockMarketDataSearch = (0, import_pieces_framework3.createAction)({
  name: "stock_market_data_search",
  auth: dappierAuth,
  displayName: "Stock Market Data",
  description: "Access real-time financial news, stock prices, and trades from polygon.io, with AI-powered insights and up-to-the-minute updates to keep you informed on all your financial interests.",
  audience: "both",
  aiMetadata: {
    description: "Queries Dappier for real-time stock market data \u2014 financial news, live prices, and trades sourced from polygon.io \u2014 with AI-generated insights for a free-text query. Choose this for finance and equities questions specifically, rather than the general web search action. Requires a natural-language stock market query; read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework3.Property.ShortText({
      displayName: "Search Query",
      description: "Enter your stock market query",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${dappierCommon.baseUrl}/app/aimodel/am_01j749h8pbf7ns8r1bq9s2evrh`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: {
        query: propsValue.query
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dappier/src/lib/actions/lifestyle-news.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var lifestyleNewsSearch = (0, import_pieces_framework4.createAction)({
  name: "lifestyle_news_search",
  auth: dappierAuth,
  displayName: "Lifestyle News",
  description: "Real-time updates, analysis, and personalized content from top sources like The Mix, Snipdaily, Nerdable, and Familyproof.",
  audience: "both",
  aiMetadata: {
    description: "Searches Dappier lifestyle-news sources for articles matching a natural-language query or URL. Choose this for lifestyle content rather than the general web search, stock, or sports actions. Optionally tune result count, restrict to a preferred domain, and pick the search algorithm (semantic, most recent, most recent + semantic, or trending) to control whether matching is contextual or recency-driven. Read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework4.Property.ShortText({
      displayName: "Search Query",
      description: "Enter a natural language query or URL",
      required: !0
    }),
    similarity_top_k: import_pieces_framework4.Property.Number({
      displayName: "Number of Results",
      description: "Number of articles to return (default is 9)",
      required: !1
    }),
    ref: import_pieces_framework4.Property.ShortText({
      displayName: "Preferred Domain",
      description: "Restrict results to a domain (e.g., familyproof.com)",
      required: !1
    }),
    num_articles_ref: import_pieces_framework4.Property.Number({
      displayName: "Guaranteed Articles from Domain",
      description: "Minimum number of articles to match the preferred domain",
      required: !1
    }),
    search_algorithm: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Search Algorithm",
      description: "Choose how to match articles",
      required: !1,
      options: {
        options: [
          { label: "Semantic (Contextual)", value: "semantic" },
          { label: "Most Recent", value: "most_recent" },
          { label: "Most Recent + Semantic", value: "most_recent_semantic" },
          { label: "Trending", value: "trending" }
        ]
      }
    })
  },
  async run({ propsValue, auth }) {
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${dappierCommon.baseUrl}/app/v2/search?data_model_id=dm_01j0q82s4bfjmsqkhs3ywm3x6y`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: {
        query: propsValue.query,
        similarity_top_k: propsValue.similarity_top_k,
        ref: propsValue.ref,
        num_articles_ref: propsValue.num_articles_ref,
        search_algorithm: propsValue.search_algorithm
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/dappier/src/index.ts
var dappierAuth = import_pieces_framework5.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Enter your Dappier API Key. You can generate one at https://platform.dappier.com/profile/api-keys."
}), dappier = (0, import_pieces_framework5.createPiece)({
  displayName: "Dappier",
  logoUrl: "https://cdn.activepieces.com/pieces/dappier.png",
  description: "Enable fast, free real-time web search and access premium data from trusted media brands\u2014news, financial markets, sports, entertainment, weather, and more. Build powerful AI agents with Dappier",
  auth: dappierAuth,
  authors: [],
  actions: [
    realTimeWebSearch,
    stockMarketDataSearch,
    sportsNewsSearch,
    lifestyleNewsSearch
  ],
  triggers: [],
  categories: [
    import_pieces_framework6.PieceCategory.ARTIFICIAL_INTELLIGENCE,
    import_pieces_framework6.PieceCategory.BUSINESS_INTELLIGENCE
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  dappier,
  dappierAuth
});
