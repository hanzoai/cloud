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

// ../ap/packages/pieces/community/datafuel/src/index.ts
var index_exports = {};
__export(index_exports, {
  datafuel: () => datafuel
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/datafuel/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), dataFuelAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "You can obtain API key from [Settings](https://app.datafuel.dev/account/api_key).",
  required: !0
});

// ../ap/packages/pieces/community/datafuel/src/lib/actions/crawl-website.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/datafuel/src/lib/common/constants.ts
var BASE_URL = "https://api.datafuel.dev/";

// ../ap/packages/pieces/community/datafuel/src/lib/actions/crawl-website.ts
var crawlWebsiteAction = (0, import_pieces_framework2.createAction)({
  name: "crawl-website",
  auth: dataFuelAuth,
  displayName: "Crawl Website",
  description: "Crawl a website into a markdown format.",
  audience: "both",
  aiMetadata: {
    description: "Crawls a website starting from a URL, following links up to a configurable depth and page limit, and returns each page as markdown; optionally extract structured data by supplying a JSON schema (OpenAI function-calling format) and an AI prompt. Use to gather content across many pages of a site, not just one. Submits a new crawl job and polls until it finishes, so each call starts fresh work and is not idempotent.",
    idempotent: !1
  },
  props: {
    url: import_pieces_framework2.Property.ShortText({
      displayName: "URL",
      required: !0
    }),
    prompt: import_pieces_framework2.Property.LongText({
      displayName: "AI Prompt",
      description: "Prompt to crawl data",
      required: !1
    }),
    depth: import_pieces_framework2.Property.Number({
      displayName: "Depth",
      description: "The depth of the crawl 1 depth mean only the first level of links will be scraped",
      required: !0
    }),
    limit: import_pieces_framework2.Property.Number({
      displayName: "Limit",
      description: "The maximum number of pages to scrape",
      required: !0
    }),
    jsonSchema: import_pieces_framework2.Property.Json({
      displayName: "JSON Schema",
      required: !1,
      description: "JSON schema definition for structured data extraction.Format should follow OpenAI's function calling schema format (https://platform.openai.com/docs/guides/structured-outputs)"
    })
  },
  async run(context) {
    let { url, prompt, depth, limit, jsonSchema } = context.propsValue, jobId = (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: BASE_URL + "/crawl",
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        url,
        ai_prompt: prompt,
        json_schema: jsonSchema,
        depth,
        limit
      }
    })).body.job_id, status = "pending", timeoutAt = Date.now() + 300 * 1e3;
    for (; status !== "finished" && Date.now() < timeoutAt; ) {
      await new Promise((resolve) => setTimeout(resolve, 5e3));
      let pollResponse = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: BASE_URL + "/list_scrapes",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: context.auth.secret_text
        },
        queryParams: {
          job_id: jobId,
          markdown: "true"
        }
      });
      if (status = pollResponse.body[0].job_status, status === "finished") return pollResponse.body;
    }
    throw new Error("Crawl Job timed out or failed.");
  }
});

// ../ap/packages/pieces/community/datafuel/src/lib/actions/scrape-website.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");
var scrapeWebsiteAction = (0, import_pieces_framework3.createAction)({
  name: "scrape-website",
  auth: dataFuelAuth,
  displayName: "Crawl Website",
  description: "Crawl a website into a markdown format.",
  audience: "both",
  aiMetadata: {
    description: "Scrapes a single URL and returns its content as markdown; optionally extract structured data by supplying a JSON schema (OpenAI function-calling format) and an AI prompt. Use when you need one page rather than crawling a whole site (use Crawl Website for multi-page traversal). Submits a new scrape job and polls until it finishes, so each call starts fresh work and is not idempotent.",
    idempotent: !1
  },
  props: {
    url: import_pieces_framework3.Property.ShortText({
      displayName: "URL",
      required: !0
    }),
    prompt: import_pieces_framework3.Property.LongText({
      displayName: "AI Prompt",
      description: "Prompt to crawl data",
      required: !1
    }),
    jsonSchema: import_pieces_framework3.Property.Json({
      displayName: "JSON Schema",
      required: !1,
      description: "JSON schema definition for structured data extraction.Format should follow OpenAI's function calling schema format (https://platform.openai.com/docs/guides/structured-outputs)"
    })
  },
  async run(context) {
    let { url, prompt, jsonSchema } = context.propsValue, jobId = (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: BASE_URL + "/scrape",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        url,
        ai_prompt: prompt,
        json_schema: jsonSchema
      }
    })).body.job_id, status = "pending", timeoutAt = Date.now() + 300 * 1e3;
    for (; status !== "finished" && Date.now() < timeoutAt; ) {
      await new Promise((resolve) => setTimeout(resolve, 5e3));
      let pollResponse = await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: BASE_URL + "/list_scrapes",
        authentication: {
          type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
          token: context.auth.secret_text
        },
        queryParams: {
          job_id: jobId
        }
      });
      if (status = pollResponse.body[0].job_status, status === "finished") return pollResponse.body;
    }
    throw new Error("Crawl Job timed out or failed.");
  }
});

// ../ap/packages/pieces/community/datafuel/src/lib/actions/get-scrape-result.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var getScrapeAction = (0, import_pieces_framework4.createAction)({
  name: "get-scrape",
  auth: dataFuelAuth,
  displayName: "Get Scrape Result",
  description: "Retrieves the deatils about scrape.",
  audience: "both",
  aiMetadata: {
    description: "Looks up the result of a previously submitted scrape or crawl job by its job ID, optionally including the markdown content and the AI-extracted JSON result. Use to fetch the output of a job started by Crawl Website or Scrape Website. Requires a valid job ID; this is a read-only lookup and is idempotent.",
    idempotent: !0
  },
  props: {
    jobId: import_pieces_framework4.Property.ShortText({
      displayName: "Job ID",
      required: !0
    }),
    aiResponse: import_pieces_framework4.Property.Checkbox({
      displayName: "AI JSON Result",
      required: !0
    }),
    markdownResponse: import_pieces_framework4.Property.Checkbox({
      displayName: "Markdown Result",
      required: !0
    })
  },
  async run(context) {
    let { jobId, aiResponse, markdownResponse } = context.propsValue;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: BASE_URL + "/list_scrapes",
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      queryParams: {
        job_id: jobId,
        markdown: markdownResponse ? "true" : "false",
        ai_response: aiResponse ? "true" : "false"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/datafuel/src/index.ts
var import_pieces_common4 = require("@activepieces/pieces-common");
var datafuel = (0, import_pieces_framework5.createPiece)({
  displayName: "DataFuel",
  auth: dataFuelAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/datafuel.png",
  authors: ["kishanprmr"],
  actions: [
    crawlWebsiteAction,
    scrapeWebsiteAction,
    getScrapeAction,
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: dataFuelAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  datafuel
});
