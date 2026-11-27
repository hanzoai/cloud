"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
var __objRest = (source, exclude) => {
  var target = {};
  for (var prop in source)
    __hasOwnProp.call(source, prop) && exclude.indexOf(prop) < 0 && (target[prop] = source[prop]);
  if (source != null && __getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(source))
      exclude.indexOf(prop) < 0 && __propIsEnum.call(source, prop) && (target[prop] = source[prop]);
  return target;
};
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

// ../ap/packages/pieces/community/webscraping-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  webscrapingAi: () => webscrapingAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/webscraping-ai/src/lib/actions/ask-a-question-about-the-web-page.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/webscraping-ai/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var baseRequestProperties = {
  url: import_pieces_framework.Property.ShortText({
    displayName: "URL",
    description: "URL of the target page.",
    required: !0
  }),
  headers: import_pieces_framework.Property.Array({
    displayName: "Custom Headers",
    description: "Add custom HTTP headers (optional)",
    required: !1,
    properties: {
      name: import_pieces_framework.Property.ShortText({
        displayName: "Header Name",
        description: "Header name (e.g., User-Agent, Authorization)",
        required: !0
      }),
      value: import_pieces_framework.Property.ShortText({
        displayName: "Header Value",
        description: "Header value",
        required: !0
      })
    }
  }),
  timeout: import_pieces_framework.Property.Number({
    displayName: "Timeout",
    description: "Maximum page load time in milliseconds (default: 10000, max: 30000)",
    required: !1,
    defaultValue: 1e4
  }),
  js: import_pieces_framework.Property.Checkbox({
    displayName: "Enable JavaScript",
    description: "Execute JavaScript for dynamic content (recommended)",
    defaultValue: !0,
    required: !1
  }),
  jsTimeout: import_pieces_framework.Property.Number({
    displayName: "JavaScript Timeout",
    description: "Maximum JavaScript execution time in milliseconds (default: 2000)",
    required: !1,
    defaultValue: 2e3
  }),
  waitFor: import_pieces_framework.Property.ShortText({
    displayName: "Wait For",
    description: 'CSS selector to wait for dynamic content (e.g., ".content-loaded")',
    required: !1
  }),
  proxy: import_pieces_framework.Property.StaticDropdown({
    displayName: "Proxy Type",
    description: "Use residential proxies for sites that block datacenter IPs (more expensive)",
    required: !1,
    defaultValue: "datacenter",
    options: {
      options: [
        { label: "\u{1F3E2} Datacenter (Fast)", value: "datacenter" },
        { label: "\u{1F3E0} Residential (Stealth)", value: "residential" }
      ]
    }
  }),
  country: import_pieces_framework.Property.StaticDropdown({
    displayName: "Proxy Country",
    description: "Geographic location of the proxy server",
    required: !1,
    defaultValue: "us",
    options: {
      options: [
        { label: "United States", value: "us" },
        { label: "Canada", value: "ca" },
        { label: "United Kingdom", value: "gb" },
        { label: "Germany", value: "de" },
        { label: "France", value: "fr" },
        { label: "Italy", value: "it" },
        { label: "Spain", value: "es" },
        { label: "Russia", value: "ru" },
        { label: "Japan", value: "jp" },
        { label: "South Korea", value: "kr" },
        { label: "India", value: "in" }
      ]
    }
  }),
  customProxy: import_pieces_framework.Property.ShortText({
    displayName: "Custom Proxy",
    description: "Your proxy URL in format: http://user:password@host:port",
    required: !1
  }),
  jsScript: import_pieces_framework.Property.LongText({
    displayName: "JavaScript Code",
    description: 'Custom JavaScript to execute (e.g., document.querySelector("button").click())',
    required: !1
  })
}, webscrapingAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0
}), webscrapingAiCommon = {
  baseUrl: "https://api.webscraping.ai",
  endpoints: {
    askQuestion: "/ai/question",
    getPageHtml: "/html",
    getPageText: "/text",
    getStructuredData: "/ai/fields",
    getAccountInfo: "/account"
  },
  askQuestionProperties: __spreadProps(__spreadValues({
    question: import_pieces_framework.Property.ShortText({
      displayName: "Question",
      description: "Question or instructions to ask the LLM model about the target page.",
      required: !0
    })
  }, baseRequestProperties), {
    device: import_pieces_framework.Property.StaticDropdown({
      displayName: "Device Type",
      description: "Emulate specific device for responsive design testing",
      required: !1,
      options: {
        options: [
          { label: "Desktop", value: "desktop" },
          { label: "Mobile", value: "mobile" },
          { label: "Tablet", value: "tablet" }
        ]
      }
    }),
    errorOn404: import_pieces_framework.Property.Checkbox({
      displayName: "Error on 404",
      description: "Fail the action if the page returns a 404 error",
      required: !1
    }),
    errorOnRedirect: import_pieces_framework.Property.Checkbox({
      displayName: "Error on Redirect",
      description: "Fail the action if the page redirects to another URL",
      required: !1
    }),
    format: import_pieces_framework.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Response format: Text (simple) or JSON (structured)",
      required: !1,
      defaultValue: "text",
      options: {
        options: [
          { label: "Text", value: "text" },
          { label: "JSON", value: "json" }
        ]
      }
    })
  }),
  getPageHtmlProperties: __spreadProps(__spreadValues({}, baseRequestProperties), {
    returnScriptResult: import_pieces_framework.Property.Checkbox({
      displayName: "Return JavaScript Result",
      description: "Return result of the custom JavaScript code (js_script parameter)  execution on the target page (false by default, page HTML will be returned).",
      required: !1
    }),
    device: import_pieces_framework.Property.StaticDropdown({
      displayName: "Device Type",
      description: "Emulate specific device for responsive design testing",
      required: !1,
      options: {
        options: [
          { label: "Desktop", value: "desktop" },
          { label: "Mobile", value: "mobile" },
          { label: "Tablet", value: "tablet" }
        ]
      }
    }),
    errorOn404: import_pieces_framework.Property.Checkbox({
      displayName: "Error on 404",
      description: "Fail the action if the page returns a 404 error",
      required: !1
    }),
    errorOnRedirect: import_pieces_framework.Property.Checkbox({
      displayName: "Error on Redirect",
      description: "Fail the action if the page redirects to another URL",
      required: !1
    }),
    format: import_pieces_framework.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Response format: Text (simple) or JSON (structured)",
      required: !1,
      defaultValue: "text",
      options: {
        options: [
          { label: "Text", value: "text" },
          { label: "JSON", value: "json" }
        ]
      }
    })
  }),
  getPageTextProperties: __spreadProps(__spreadValues({}, baseRequestProperties), {
    textFormat: import_pieces_framework.Property.StaticDropdown({
      displayName: "Text Format",
      description: "Response format: Plain text, JSON (with title/description/content), or XML",
      required: !1,
      defaultValue: "plain",
      options: {
        options: [
          { label: "Plain Text", value: "plain" },
          { label: "JSON", value: "json" },
          { label: "XML", value: "xml" }
        ]
      }
    }),
    returnLinks: import_pieces_framework.Property.Checkbox({
      displayName: "Return Links",
      description: "Include links in response (only works with JSON format)",
      required: !1
    }),
    device: import_pieces_framework.Property.StaticDropdown({
      displayName: "Device Type",
      description: "Emulate specific device for responsive design testing",
      required: !1,
      options: {
        options: [
          { label: "Desktop", value: "desktop" },
          { label: "Mobile", value: "mobile" },
          { label: "Tablet", value: "tablet" }
        ]
      }
    }),
    errorOn404: import_pieces_framework.Property.Checkbox({
      displayName: "Error on 404",
      description: "Fail the action if the page returns a 404 error",
      required: !1
    }),
    errorOnRedirect: import_pieces_framework.Property.Checkbox({
      displayName: "Error on Redirect",
      description: "Fail the action if the page redirects to another URL",
      required: !1
    })
  }),
  getPageStructuredDataProperties: __spreadProps(__spreadValues({
    fields: import_pieces_framework.Property.Object({
      displayName: "Fields to Extract",
      description: 'Define fields to extract (e.g., {"title": "Product title", "price": "Product price"})',
      required: !0
    })
  }, baseRequestProperties), {
    device: import_pieces_framework.Property.StaticDropdown({
      displayName: "Device Type",
      description: "Emulate specific device for responsive design testing",
      required: !1,
      options: {
        options: [
          { label: "Desktop", value: "desktop" },
          { label: "Mobile", value: "mobile" },
          { label: "Tablet", value: "tablet" }
        ]
      }
    }),
    errorOn404: import_pieces_framework.Property.Checkbox({
      displayName: "Error on 404",
      description: "Fail the action if the page returns a 404 error",
      required: !1
    }),
    errorOnRedirect: import_pieces_framework.Property.Checkbox({
      displayName: "Error on Redirect",
      description: "Fail the action if the page redirects to another URL",
      required: !1
    })
  }),
  askQuestion: async (params) => {
    let rawParams = {
      api_key: params.apiKey,
      url: params.url,
      question: params.question,
      timeout: params.timeout,
      js: params.js,
      js_timeout: params.jsTimeout,
      wait_for: params.waitFor,
      proxy: params.proxy,
      country: params.country,
      custom_proxy: params.customProxy,
      device: params.device,
      error_on_404: params.errorOn404,
      error_on_redirect: params.errorOnRedirect,
      js_script: params.jsScript,
      format: params.format,
      headers: params.headers && Array.isArray(params.headers) ? JSON.stringify(Object.fromEntries(params.headers.map((h) => [h.name, h.value]))) : void 0
    }, filtered = (0, import_pieces_framework2.pickBy)(
      rawParams,
      (value) => value !== void 0
    ), queryParams = Object.fromEntries(
      Object.entries(filtered).map(([k, v]) => [k, String(v)])
    );
    return await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${webscrapingAiCommon.baseUrl}${webscrapingAiCommon.endpoints.askQuestion}`,
      queryParams
    });
  },
  getPageHtml: async (params) => {
    let rawParams = {
      api_key: params.apiKey,
      url: params.url,
      headers: params.headers && Array.isArray(params.headers) ? JSON.stringify(Object.fromEntries(params.headers.map((h) => [h.name, h.value]))) : void 0,
      timeout: params.timeout,
      js: params.js,
      js_timeout: params.jsTimeout,
      wait_for: params.waitFor,
      proxy: params.proxy,
      country: params.country,
      custom_proxy: params.customProxy,
      device: params.device,
      error_on_404: params.errorOn404,
      error_on_redirect: params.errorOnRedirect,
      js_script: params.jsScript,
      return_script_result: params.returnScriptResult,
      format: params.format
    }, filtered = (0, import_pieces_framework2.pickBy)(
      rawParams,
      (value) => value !== void 0
    ), queryParams = Object.fromEntries(
      Object.entries(filtered).map(([k, v]) => [k, String(v)])
    );
    return await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${webscrapingAiCommon.baseUrl}${webscrapingAiCommon.endpoints.getPageHtml}`,
      queryParams
    });
  },
  getPageText: async (params) => {
    let rawParams = {
      api_key: params.apiKey,
      text_format: params.textFormat,
      return_links: params.returnLinks,
      url: params.url,
      headers: params.headers && Array.isArray(params.headers) ? JSON.stringify(Object.fromEntries(params.headers.map((h) => [h.name, h.value]))) : void 0,
      timeout: params.timeout,
      js: params.js,
      js_timeout: params.jsTimeout,
      wait_for: params.waitFor,
      proxy: params.proxy,
      country: params.country,
      custom_proxy: params.customProxy,
      device: params.device,
      error_on_404: params.errorOn404,
      error_on_redirect: params.errorOnRedirect,
      js_script: params.jsScript
    }, filtered = (0, import_pieces_framework2.pickBy)(
      rawParams,
      (value) => value !== void 0
    ), queryParams = Object.fromEntries(
      Object.entries(filtered).map(([k, v]) => [k, String(v)])
    );
    return await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${webscrapingAiCommon.baseUrl}${webscrapingAiCommon.endpoints.getPageText}`,
      queryParams
    });
  },
  getPageStructuredData: async (params) => {
    let rawParams = {
      api_key: params.apiKey,
      url: params.url,
      headers: params.headers && Array.isArray(params.headers) ? JSON.stringify(Object.fromEntries(params.headers.map((h) => [h.name, h.value]))) : void 0,
      timeout: params.timeout,
      js: params.js,
      js_timeout: params.jsTimeout,
      wait_for: params.waitFor,
      proxy: params.proxy,
      country: params.country,
      custom_proxy: params.customProxy,
      device: params.device,
      error_on_404: params.errorOn404,
      error_on_redirect: params.errorOnRedirect,
      js_script: params.jsScript
    }, filtered = (0, import_pieces_framework2.pickBy)(
      rawParams,
      (value) => value !== void 0
    ), expandedFields = Object.fromEntries(
      Object.entries(params.fields || {}).map(([key, value]) => [
        `fields[${key}]`,
        value
      ])
    ), queryParams = __spreadValues(__spreadValues({}, Object.fromEntries(
      Object.entries(filtered).map(([k, v]) => [k, String(v)])
    )), expandedFields);
    return await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${webscrapingAiCommon.baseUrl}${webscrapingAiCommon.endpoints.getStructuredData}`,
      queryParams
    });
  },
  getAccountInformation: async ({ apiKey }) => await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${webscrapingAiCommon.baseUrl}${webscrapingAiCommon.endpoints.getAccountInfo}`,
    queryParams: { api_key: apiKey }
  })
};

// ../ap/packages/pieces/community/webscraping-ai/src/lib/actions/ask-a-question-about-the-web-page.ts
var askAQuestionAboutTheWebPage = (0, import_pieces_framework3.createAction)({
  auth: webscrapingAiAuth,
  name: "askAQuestionAboutTheWebPage",
  displayName: "Ask a Question About the Web Page",
  description: "Gets an answer to a question about a given webpage.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a web page (rendering JavaScript), then uses an LLM to answer a natural-language question about its content. Choose this to extract a specific fact or summary from a single URL without parsing HTML yourself, when you have a concrete question rather than needing the full page text or a structured record. Requires the target URL and the question; optional proxy/country/device/header controls tune how the page is fetched. Read-only and idempotent (a GET-style request that does not alter the target site).",
    idempotent: !0
  },
  props: webscrapingAiCommon.askQuestionProperties,
  async run({ auth: apiKey, propsValue }) {
    let _a = propsValue, { device, format, question } = _a, rest = __objRest(_a, ["device", "format", "question"]), allowedCountries = [
      "us",
      "gb",
      "de",
      "it",
      "fr",
      "ca",
      "es",
      "ru",
      "jp",
      "kr",
      "in"
    ], params = __spreadProps(__spreadValues({
      apiKey: apiKey.secret_text,
      question
    }, rest), {
      format: format === "json" || format === "text" ? format : void 0,
      proxy: rest.proxy === "datacenter" || rest.proxy === "residential" ? rest.proxy : void 0,
      country: rest.country && allowedCountries.includes(rest.country) ? rest.country : void 0,
      headers: rest.headers && Array.isArray(rest.headers) ? Object.fromEntries(rest.headers.map((h) => [h.name, h.value])) : void 0,
      device
    });
    return await webscrapingAiCommon.askQuestion(params);
  }
});

// ../ap/packages/pieces/community/webscraping-ai/src/lib/actions/extract-structured-data.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var extractStructuredData = (0, import_pieces_framework4.createAction)({
  auth: webscrapingAiAuth,
  name: "extractStructuredData",
  displayName: "Extract structured data",
  description: "Returns structured data fields extracted from the webpage using an LLM model.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a web page (rendering JavaScript) and uses an LLM to extract a set of caller-defined fields from it, where each field is given as a name plus a natural-language description of what to pull. Choose this when you need specific structured values (e.g. price, title, author) rather than a free-text answer or the raw page. Requires the target URL and the fields map; optional proxy/country/device/header controls tune the fetch. Read-only and idempotent (a GET-style request that does not alter the target site).",
    idempotent: !0
  },
  props: webscrapingAiCommon.getPageStructuredDataProperties,
  async run({ auth: apiKey, propsValue }) {
    let _a = propsValue, { fields, headers } = _a, rest = __objRest(_a, ["fields", "headers"]), allowedCountries = [
      "us",
      "gb",
      "de",
      "it",
      "fr",
      "ca",
      "es",
      "ru",
      "jp",
      "kr",
      "in"
    ], params = __spreadProps(__spreadValues({
      apiKey: apiKey.secret_text
    }, rest), {
      proxy: rest.proxy === "datacenter" || rest.proxy === "residential" ? rest.proxy : void 0,
      country: rest.country && allowedCountries.includes(rest.country) ? rest.country : void 0,
      headers: headers && Array.isArray(headers) ? Object.fromEntries(headers.map((h) => [h.name, h.value])) : void 0,
      fields: fields && typeof fields == "object" ? Object.fromEntries(Object.entries(fields).map(([k, v]) => [k, String(v)])) : {},
      device: rest.device
    });
    return await webscrapingAiCommon.getPageStructuredData(params);
  }
});

// ../ap/packages/pieces/community/webscraping-ai/src/lib/actions/get-account-information.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var getAccountInformation = (0, import_pieces_framework5.createAction)({
  auth: webscrapingAiAuth,
  name: "getAccountInformation",
  displayName: "Get Account Info",
  description: "Get account usage information including remaining API credits and concurrent requests.",
  audience: "both",
  aiMetadata: {
    description: "Looks up the WebScraping AI account status, including remaining API credits and concurrent-request limits. Choose this to check quota or capacity before running a batch of scraping calls, or to surface usage to the caller. Takes no inputs beyond the connection. Read-only and idempotent (a GET-style lookup).",
    idempotent: !0
  },
  props: {},
  async run({ auth: apiKey }) {
    return await webscrapingAiCommon.getAccountInformation({ apiKey: apiKey.secret_text });
  }
});

// ../ap/packages/pieces/community/webscraping-ai/src/lib/actions/get-page-html.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getPageHtml = (0, import_pieces_framework6.createAction)({
  auth: webscrapingAiAuth,
  name: "getPageHtml",
  displayName: "Get Page HTML",
  description: "Retrieves the raw HTML markup of a web page.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a web page (rendering JavaScript) and returns its raw HTML markup. Choose this when you need the full document to parse yourself or feed into other tools, rather than an LLM-derived answer or extracted text. Requires the target URL; optional proxy/country/device/header controls tune the fetch, and flags can error on 404 or redirect or return JS execution results. Read-only and idempotent (a GET-style request that does not alter the target site).",
    idempotent: !0
  },
  props: webscrapingAiCommon.getPageHtmlProperties,
  async run({ auth: apiKey, propsValue }) {
    let _a = propsValue, { format, headers, proxy, device, errorOn404, errorOnRedirect, returnScriptResult } = _a, rest = __objRest(_a, ["format", "headers", "proxy", "device", "errorOn404", "errorOnRedirect", "returnScriptResult"]), allowedCountries = [
      "us",
      "gb",
      "de",
      "it",
      "fr",
      "ca",
      "es",
      "ru",
      "jp",
      "kr",
      "in"
    ], params = __spreadProps(__spreadValues({
      apiKey: apiKey.secret_text
    }, rest), {
      format: format === "json" || format === "text" ? format : void 0,
      proxy: proxy === "datacenter" || proxy === "residential" ? proxy : void 0,
      country: rest.country && allowedCountries.includes(rest.country) ? rest.country : void 0,
      headers: headers && Array.isArray(headers) ? Object.fromEntries(headers.map((h) => [h.name, h.value])) : void 0,
      device,
      errorOn404,
      errorOnRedirect,
      returnScriptResult
    });
    return await webscrapingAiCommon.getPageHtml(params);
  }
});

// ../ap/packages/pieces/community/webscraping-ai/src/lib/actions/scrape-website-text.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var scrapeWebsiteText = (0, import_pieces_framework7.createAction)({
  auth: webscrapingAiAuth,
  name: "scrapeWebsiteText",
  displayName: "Scrape Website Text",
  description: "Returns the visible text content of a webpage specified by the URL.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a web page (rendering JavaScript) and returns its visible text with HTML stripped out, in plain, JSON, or XML form. Choose this when you want clean readable content for summarizing or feeding to an LLM, rather than the raw HTML or a single extracted answer. Requires the target URL; optional proxy/country/device/header controls tune the fetch, and JSON output can additionally return extracted links. Read-only and idempotent (a GET-style request that does not alter the target site).",
    idempotent: !0
  },
  props: webscrapingAiCommon.getPageTextProperties,
  async run({ auth: apiKey, propsValue }) {
    let _a = propsValue, { textFormat, headers, returnLinks } = _a, rest = __objRest(_a, ["textFormat", "headers", "returnLinks"]), allowedCountries = [
      "us",
      "gb",
      "de",
      "it",
      "fr",
      "ca",
      "es",
      "ru",
      "jp",
      "kr",
      "in"
    ], params = __spreadProps(__spreadValues({
      apiKey: apiKey.secret_text
    }, rest), {
      textFormat: textFormat === "json" || textFormat === "plain" || textFormat === "xml" ? textFormat : void 0,
      returnLinks: textFormat === "json" ? returnLinks : void 0,
      proxy: rest.proxy === "datacenter" || rest.proxy === "residential" ? rest.proxy : void 0,
      country: rest.country && allowedCountries.includes(rest.country) ? rest.country : void 0,
      headers: headers && Array.isArray(headers) ? Object.fromEntries(headers.map((h) => [h.name, h.value])) : void 0,
      device: rest.device
    });
    return await webscrapingAiCommon.getPageText(params);
  }
});

// ../ap/packages/pieces/community/webscraping-ai/src/index.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), webscrapingAi = (0, import_pieces_framework8.createPiece)({
  displayName: "WebScraping AI",
  auth: webscrapingAiAuth,
  minimumSupportedRelease: "0.36.1",
  description: "WebScraping AI is a powerful tool that allows you to scrape websites and extract data.",
  categories: [import_pieces_framework9.PieceCategory.DEVELOPER_TOOLS, import_pieces_framework9.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  logoUrl: "https://cdn.activepieces.com/pieces/webscraping-ai.png",
  authors: ["LuizDMM", "onyedikachi-david"],
  actions: [
    askAQuestionAboutTheWebPage,
    getPageHtml,
    scrapeWebsiteText,
    extractStructuredData,
    getAccountInformation
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  webscrapingAi
});
