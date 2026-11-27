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

// ../ap/packages/pieces/community/dumpling-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  dumplingAi: () => dumplingAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/dumpling-ai/src/lib/actions/web-search.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/dumpling-ai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), dumplingAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: `
  You can obtain API key from [API Section](https://app.dumplingai.com/api-keys).`,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        url: "https://app.dumplingai.com/api/v1/search",
        method: import_pieces_common.HttpMethod.POST,
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        },
        body: {
          query: "Activepieces"
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return { valid: !1, error: "Invalid API Key." };
    }
  }
});

// ../ap/packages/pieces/community/dumpling-ai/src/lib/actions/web-search.ts
var webSearch = (0, import_pieces_framework2.createAction)({
  name: "web_search",
  auth: dumplingAuth,
  displayName: "Web Search",
  description: "Search the web and optionally retrieve content from top results.",
  audience: "both",
  aiMetadata: { description: 'Runs a live web search on Dumpling AI for a query, with optional country/location/language and date-range filtering. Can operate in two modes: results-only, or with "Scrape Results" enabled it also fetches and returns the page content of the top N results (markdown, HTML, or screenshot). Use to find current web pages or grab their content in one step. Not idempotent: each call is a fresh billed search whose results reflect live web state.', idempotent: !1 },
  props: {
    query: import_pieces_framework2.Property.ShortText({
      displayName: "Search Query",
      required: !0
    }),
    country: import_pieces_framework2.Property.ShortText({
      displayName: "Country",
      required: !1,
      description: 'Two-letter country code for location bias (e.g., "US" for United States).'
    }),
    location: import_pieces_framework2.Property.ShortText({
      displayName: "Location",
      required: !1,
      description: 'Specific location to focus the search (e.g., "New York, NY").'
    }),
    language: import_pieces_framework2.Property.ShortText({
      displayName: "Language",
      required: !1,
      description: 'Language code for the search results (e.g., "en" for English).'
    }),
    dateRange: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Date Range",
      required: !1,
      options: {
        options: [
          { label: "Any Time", value: "anyTime" },
          { label: "Past Hour", value: "pastHour" },
          { label: "Past Day", value: "pastDay" },
          { label: "Past Week", value: "pastWeek" },
          { label: "Past Month", value: "pastMonth" },
          { label: "Past Year", value: "pastYear" }
        ]
      },
      description: "Filter results by date."
    }),
    page: import_pieces_framework2.Property.Number({
      displayName: "Page Number",
      required: !1,
      description: "Page number for paginated results."
    }),
    scrapeResults: import_pieces_framework2.Property.Checkbox({
      displayName: "Scrape Results",
      required: !1,
      defaultValue: !1,
      description: "Whether to scrape top search results."
    }),
    numResultsToScrape: import_pieces_framework2.Property.Number({
      displayName: "Number of Results to Scrape",
      required: !1,
      defaultValue: 3,
      description: "Number of top results to scrape (max: 10)."
    }),
    scrapeFormat: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Scrape Format",
      required: !1,
      defaultValue: "markdown",
      options: {
        options: [
          { label: "Markdown", value: "markdown" },
          { label: "HTML", value: "html" },
          { label: "Screenshot", value: "screenshot" }
        ]
      },
      description: "Format of scraped content"
    }),
    cleanedOutput: import_pieces_framework2.Property.Checkbox({
      displayName: "Clean Output",
      required: !1,
      defaultValue: !0,
      description: "Whether the scraped output should be cleaned."
    })
  },
  async run(context) {
    let {
      query,
      country,
      location,
      language,
      dateRange,
      page,
      scrapeResults,
      numResultsToScrape,
      scrapeFormat,
      cleanedOutput
    } = context.propsValue, requestBody = {
      query
    };
    return country && (requestBody.country = country), location && (requestBody.location = location), language && (requestBody.language = language), dateRange && (requestBody.dateRange = dateRange), page && (requestBody.page = page), scrapeResults !== void 0 && (requestBody.scrapeResults = scrapeResults), numResultsToScrape && (requestBody.numResultsToScrape = numResultsToScrape), scrapeResults && (requestBody.scrapeOptions = {}, scrapeFormat && (requestBody.scrapeOptions.format = scrapeFormat), cleanedOutput !== void 0 && (requestBody.scrapeOptions.cleaned = cleanedOutput)), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://app.dumplingai.com/api/v1/search",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${context.auth.secret_text}`
      },
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/dumpling-ai/src/lib/actions/search-news.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var searchNews = (0, import_pieces_framework3.createAction)({
  name: "search_news",
  auth: dumplingAuth,
  displayName: "Search News",
  description: "Search for news articles using Google News.",
  audience: "both",
  aiMetadata: { description: "Searches Google News via Dumpling AI for a query, with optional country/location/language and date-range filtering plus pagination. Choose this over the general web search when the agent needs news articles specifically. Not idempotent: each call is a fresh billed search reflecting live news at request time.", idempotent: !1 },
  props: {
    query: import_pieces_framework3.Property.ShortText({
      displayName: "Search Query",
      required: !0,
      description: "The search query for Google News."
    }),
    country: import_pieces_framework3.Property.ShortText({
      displayName: "Country",
      required: !1,
      description: 'Country code for location bias (e.g., "US" for United States).'
    }),
    location: import_pieces_framework3.Property.ShortText({
      displayName: "Location",
      required: !1,
      description: 'Specific location to focus the search (e.g., "New York, NY").'
    }),
    language: import_pieces_framework3.Property.ShortText({
      displayName: "Language",
      required: !1,
      description: 'Language code for the search results (e.g., "en" for English).'
    }),
    dateRange: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Date Range",
      required: !1,
      options: {
        options: [
          { label: "Any Time", value: "anyTime" },
          { label: "Past Hour", value: "pastHour" },
          { label: "Past Day", value: "pastDay" },
          { label: "Past Week", value: "pastWeek" },
          { label: "Past Month", value: "pastMonth" },
          { label: "Past Year", value: "pastYear" }
        ]
      },
      description: "Filter results by date."
    }),
    page: import_pieces_framework3.Property.Number({
      displayName: "Page Number",
      required: !1,
      description: "Page number for paginated results."
    })
  },
  async run(context) {
    let { query, country, location, language, dateRange, page } = context.propsValue, requestBody = {
      query
    };
    return country && (requestBody.country = country), location && (requestBody.location = location), language && (requestBody.language = language), dateRange && (requestBody.dateRange = dateRange), page && (requestBody.page = page), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: "https://app.dumplingai.com/api/v1/search-news",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${context.auth.secret_text}`
      },
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/dumpling-ai/src/lib/actions/generate-image.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var generateImage = (0, import_pieces_framework4.createAction)({
  name: "generate_image",
  auth: dumplingAuth,
  displayName: "Generate Image",
  description: "Generate images based on a text prompt using AI.",
  audience: "both",
  aiMetadata: { description: "Generates one or more images from a text prompt using a selectable FLUX or recraft model on Dumpling AI, with optional aspect ratio, output format, image count, and seed. Use when the agent needs to create images from a description. Not idempotent: each call is a fresh billed generation; even with a fixed seed it produces new output and consumes credits.", idempotent: !1 },
  props: {
    model: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Model",
      required: !0,
      description: "The model to use for image generation",
      defaultValue: "FLUX.1-schnell",
      options: {
        options: [
          { label: "FLUX.1-schnell", value: "FLUX.1-schnell" },
          { label: "FLUX.1-dev", value: "FLUX.1-dev" },
          { label: "FLUX.1-pro", value: "FLUX.1-pro" },
          { label: "FLUX.1.1-pro", value: "FLUX.1.1-pro" },
          { label: "recraft-v3", value: "recraft-v3" }
        ]
      }
    }),
    prompt: import_pieces_framework4.Property.LongText({
      displayName: "Prompt",
      required: !0,
      description: "The text prompt for image generation"
    }),
    aspect_ratio: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Aspect Ratio",
      required: !1,
      description: "Aspect ratio of the generated image",
      defaultValue: "1:1",
      options: {
        options: [
          { label: "Square (1:1)", value: "1:1" },
          { label: "Landscape 16:9", value: "16:9" },
          { label: "Landscape 21:9", value: "21:9" },
          { label: "Landscape 3:2", value: "3:2" },
          { label: "Landscape 4:3", value: "4:3" },
          { label: "Portrait 2:3", value: "2:3" },
          { label: "Portrait 3:4", value: "3:4" },
          { label: "Portrait 4:5", value: "4:5" },
          { label: "Portrait 9:16", value: "9:16" },
          { label: "Portrait 9:21", value: "9:21" }
        ]
      }
    }),
    num_outputs: import_pieces_framework4.Property.Number({
      displayName: "Number of Images",
      required: !1,
      defaultValue: 1,
      description: "Number of images to generate (1-4)"
    }),
    seed: import_pieces_framework4.Property.Number({
      displayName: "Seed",
      required: !1,
      description: "Seed for reproducible results"
    }),
    output_format: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Output Format",
      required: !1,
      description: "Format of the generated image",
      defaultValue: "webp",
      options: {
        options: [
          { label: "WebP", value: "webp" },
          { label: "JPG", value: "jpg" },
          { label: "PNG", value: "png" }
        ]
      }
    })
  },
  async run(context) {
    let { model, prompt, aspect_ratio, num_outputs, seed, output_format } = context.propsValue, input = {
      prompt
    };
    seed !== void 0 && (input.seed = seed), aspect_ratio && (input.aspect_ratio = aspect_ratio), num_outputs && (input.num_outputs = num_outputs), output_format && (input.output_format = output_format);
    let requestBody = {
      model,
      input
    };
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: "https://app.dumplingai.com/api/v1/generate-ai-image",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${context.auth.secret_text}`
      },
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/dumpling-ai/src/lib/actions/scrape-website.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var scrapeWebsite = (0, import_pieces_framework5.createAction)({
  name: "scrape_website",
  auth: dumplingAuth,
  displayName: "Scrape Website",
  description: "Scrapes data from a specified URL and format the result.",
  audience: "both",
  aiMetadata: { description: "Fetches the content of a single given URL via Dumpling AI and returns it as markdown, HTML, or a screenshot, with optional content cleaning and JavaScript rendering. Use to read one specific page; use Crawl Website instead to follow links across many pages. Not idempotent: each call is a fresh billed fetch reflecting the live page.", idempotent: !1 },
  props: {
    url: import_pieces_framework5.Property.ShortText({
      displayName: "URL",
      required: !0
    }),
    format: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Output Format",
      required: !1,
      defaultValue: "markdown",
      options: {
        options: [
          { label: "Markdown", value: "markdown" },
          { label: "HTML", value: "html" },
          { label: "Screenshot", value: "screenshot" }
        ]
      },
      description: "The format of the output"
    }),
    cleaned: import_pieces_framework5.Property.Checkbox({
      displayName: "Clean Output ?",
      required: !1,
      defaultValue: !0,
      description: "Whether the output should be cleaned."
    }),
    renderJs: import_pieces_framework5.Property.Checkbox({
      displayName: "Render JavaScript ?",
      required: !1,
      defaultValue: !0,
      description: "Whether to render JavaScript before scraping."
    })
  },
  async run(context) {
    let { url, format, cleaned, renderJs } = context.propsValue, requestBody = {
      url
    };
    return format && (requestBody.format = format), cleaned !== void 0 && (requestBody.cleaned = cleaned), renderJs !== void 0 && (requestBody.renderJs = renderJs), (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: "https://app.dumplingai.com/api/v1/scrape",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${context.auth.secret_text}`
      },
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/dumpling-ai/src/lib/actions/crawl-website.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var crawlWebsite = (0, import_pieces_framework6.createAction)({
  name: "crawl_website",
  auth: dumplingAuth,
  displayName: "Crawl Website",
  description: "Crawl a website and return structured content from multiple pages.",
  audience: "both",
  aiMetadata: { description: "Crawls a website starting from a given URL via Dumpling AI, following links up to a configurable page limit and depth, and returns each page as markdown, text, or raw content. Use when the agent needs content from many pages of a site at once; use Scrape Website for a single known URL. Not idempotent: each call is a fresh billed crawl reflecting live site state.", idempotent: !1 },
  props: {
    url: import_pieces_framework6.Property.ShortText({
      displayName: "URL",
      required: !0,
      description: "The website URL to crawl."
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Page Limit",
      required: !1,
      defaultValue: 5,
      description: "Maximum number of pages to crawl."
    }),
    depth: import_pieces_framework6.Property.Number({
      displayName: "Crawl Depth",
      required: !1,
      defaultValue: 2,
      description: "Depth of crawling (distance between base URL path and sub paths)."
    }),
    format: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Output Format",
      required: !1,
      defaultValue: "markdown",
      options: {
        options: [
          { label: "Markdown", value: "markdown" },
          { label: "Text", value: "text" },
          { label: "Raw", value: "raw" }
        ]
      },
      description: "Format of the output content."
    })
  },
  async run(context) {
    let { url, limit, depth, format } = context.propsValue, requestBody = {
      url
    };
    return limit !== void 0 && (requestBody.limit = limit), depth !== void 0 && (requestBody.depth = depth), format && (requestBody.format = format), (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: "https://app.dumplingai.com/api/v1/crawl",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${context.auth.secret_text}`
      },
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/dumpling-ai/src/lib/actions/extract-document.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var extractDocument = (0, import_pieces_framework7.createAction)({
  name: "extract_document",
  auth: dumplingAuth,
  displayName: "Extract Document Data",
  description: "Extract structured data from documents using vision-capable AI.",
  audience: "both",
  aiMetadata: { description: "Extracts data from an uploaded document (file or base64) using vision-capable AI on Dumpling AI, guided by a free-text extraction prompt, optionally returning the result as JSON. Use to pull fields or content out of PDFs, images, or scanned files. Not idempotent: each call is a fresh billed AI extraction whose output may vary.", idempotent: !1 },
  props: {
    file: import_pieces_framework7.Property.File({
      displayName: "File",
      required: !0,
      description: "File URL or base64-encoded file."
    }),
    prompt: import_pieces_framework7.Property.LongText({
      displayName: "Extraction Prompt",
      required: !0,
      description: "The prompt describing what data to extract from the document."
    }),
    jsonMode: import_pieces_framework7.Property.Checkbox({
      displayName: "JSON Mode",
      required: !1,
      defaultValue: !1,
      description: "Whether to return the result in JSON format."
    })
  },
  async run(context) {
    let { file, prompt, jsonMode } = context.propsValue, requestBody = {
      inputMethod: "base64",
      files: [file.base64],
      prompt
    };
    return jsonMode !== void 0 && (requestBody.jsonMode = jsonMode), (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: "https://app.dumplingai.com/api/v1/extract-document",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${context.auth.secret_text}`
      },
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/dumpling-ai/src/index.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var dumplingAi = (0, import_pieces_framework8.createPiece)({
  displayName: "Dumpling AI",
  description: "Transform unstructured website content into clean, AI-ready data",
  auth: dumplingAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/dumpling-ai.png",
  authors: ["neo773"],
  categories: [import_pieces_framework9.PieceCategory.ARTIFICIAL_INTELLIGENCE, import_pieces_framework9.PieceCategory.PRODUCTIVITY],
  actions: [
    webSearch,
    searchNews,
    generateImage,
    scrapeWebsite,
    crawlWebsite,
    extractDocument,
    (0, import_pieces_common8.createCustomApiCallAction)({
      baseUrl: () => "https://app.dumplingai.com/api/v1",
      auth: dumplingAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  dumplingAi
});
