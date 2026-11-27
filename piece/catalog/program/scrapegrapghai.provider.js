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

// ../ap/packages/pieces/community/scrapegrapghai/src/index.ts
var index_exports = {};
__export(index_exports, {
  scrapegraphai: () => scrapegraphai
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/scrapegrapghai/src/lib/actions/smart-scraper.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/scrapegrapghai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), markdownDescription = `
Follow these steps to obtain your ScrapeGraphAI API Key:

1. Visit [ScrapeGraphAI](https://scrapegraphai.com) and create an account.
2. Log in and navigate to your dashboard.
3. Locate and copy your API key from the dashboard.
`, scrapegraphaiAuth = import_pieces_framework.PieceAuth.SecretText({
  description: markdownDescription,
  displayName: "API Key",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: "https://api.scrapegraphai.com/v1/smartscraper",
        headers: {
          "Content-Type": "application/json",
          "SGAI-APIKEY": auth
        },
        body: {
          user_prompt: "test",
          website_url: "https://www.example.com"
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

// ../ap/packages/pieces/community/scrapegrapghai/src/lib/actions/smart-scraper.ts
var smartScraper = (0, import_pieces_framework2.createAction)({
  name: "smart_scraper",
  displayName: "Smart Scraper",
  description: "Extract content from a webpage using AI by providing a natural language prompt.",
  audience: "both",
  aiMetadata: { description: "Fetches a live webpage by URL and uses AI to extract the information described in a natural-language prompt; optionally pass an output schema to shape the result into structured fields. Choose this when you have a public URL and want targeted data from it rather than the full raw page. The page is fetched server-side from the given URL; for HTML you already hold, use Local Scraper instead. Read-only and safe to retry.", idempotent: !0 },
  auth: scrapegraphaiAuth,
  props: {
    website_url: import_pieces_framework2.Property.ShortText({
      displayName: "Website URL",
      description: "The webpage URL to scrape.",
      required: !0
    }),
    user_prompt: import_pieces_framework2.Property.LongText({
      displayName: "Extraction Prompt",
      description: "Describe what information you want to extract in natural language.",
      required: !0
    }),
    output_schema: import_pieces_framework2.Property.Json({
      displayName: "Output Schema",
      description: "Optional schema to structure the output data.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.scrapegraphai.com/v1/smartscraper",
      headers: {
        "Content-Type": "application/json",
        "SGAI-APIKEY": auth.secret_text
      },
      body: {
        website_url: propsValue.website_url,
        user_prompt: propsValue.user_prompt,
        output_schema: propsValue.output_schema
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/scrapegrapghai/src/lib/actions/local-scraper.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var localScraper = (0, import_pieces_framework3.createAction)({
  name: "local_scraper",
  displayName: "Local Scraper",
  description: "Extract content from HTML content using AI by providing a natural language prompt.",
  audience: "both",
  aiMetadata: { description: "Uses AI to extract information described in a natural-language prompt from raw HTML you supply directly (max 2MB); optionally pass an output schema to shape the result into structured fields. Choose this when you already have the page HTML in hand and do not want the service to fetch a URL \u2014 for a live URL use Smart Scraper instead. Read-only and safe to retry.", idempotent: !0 },
  auth: scrapegraphaiAuth,
  props: {
    website_html: import_pieces_framework3.Property.LongText({
      displayName: "HTML Content",
      description: "The HTML content to process (max 2MB).",
      required: !0
    }),
    user_prompt: import_pieces_framework3.Property.LongText({
      displayName: "Extraction Prompt",
      description: "Describe what information you want to extract in natural language.",
      required: !0
    }),
    output_schema: import_pieces_framework3.Property.Json({
      displayName: "Output Schema",
      description: "Optional schema to structure the output data.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://api.scrapegraphai.com/v1/localscraper",
      headers: {
        "Content-Type": "application/json",
        "SGAI-APIKEY": auth.secret_text
      },
      body: {
        website_html: propsValue.website_html,
        user_prompt: propsValue.user_prompt,
        output_schema: propsValue.output_schema
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/scrapegrapghai/src/lib/actions/markdownify.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var markdownify = (0, import_pieces_framework4.createAction)({
  name: "markdownify",
  displayName: "Convert to Markdown",
  description: "Convert any webpage into clean, readable Markdown format.",
  audience: "both",
  aiMetadata: { description: "Fetches a webpage by URL and returns its content as clean, readable Markdown. Choose this when you need the whole page as text for an LLM or document rather than a targeted extraction \u2014 use Smart Scraper instead when you want specific fields described by a prompt. Requires a public URL; read-only and safe to retry.", idempotent: !0 },
  auth: scrapegraphaiAuth,
  props: {
    website_url: import_pieces_framework4.Property.ShortText({
      displayName: "Website URL",
      description: "The webpage URL to convert to Markdown",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: "https://api.scrapegraphai.com/v1/markdownify",
      headers: {
        "Content-Type": "application/json",
        "SGAI-APIKEY": auth.secret_text
      },
      body: {
        website_url: propsValue.website_url
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/scrapegrapghai/src/index.ts
var scrapegraphai = (0, import_pieces_framework5.createPiece)({
  displayName: "ScrapeGraphAI",
  description: "AI-powered web scraping and content extraction.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/scrapegraphai.jpg",
  categories: [import_pieces_framework6.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["OsamaHaikal"],
  auth: scrapegraphaiAuth,
  actions: [
    smartScraper,
    localScraper,
    markdownify,
    (0, import_pieces_common5.createCustomApiCallAction)({
      baseUrl: () => "https://api.scrapegraphai.com/v1",
      auth: scrapegraphaiAuth,
      authMapping: async (auth) => ({
        "SGAI-APIKEY": `${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  scrapegraphai
});
