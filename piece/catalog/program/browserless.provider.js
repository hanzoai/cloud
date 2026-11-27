"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
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

// ../ap/packages/pieces/community/browserless/src/index.ts
var index_exports = {};
__export(index_exports, {
  browserless: () => browserless
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/browserless/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), browserlessAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `
    To obtain your API credentials:

    1. Sign up for a free Browserless account at https://www.browserless.io
    2. Navigate to your dashboard
    3. Find your API Key/Token in the account settings
    4. Choose your preferred regional endpoint for optimal performance

    Regional Endpoints:
    \u2022 US West (SFO): https://production-sfo.browserless.io
    \u2022 Europe UK (London): https://production-lon.browserless.io
    \u2022 Europe (Amsterdam): https://production-ams.browserless.io

    For custom/dedicated instances, select "Custom" and enter your specific endpoint URL.
    `,
  props: {
    apiToken: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Token",
      description: "Your Browserless API token (found in your dashboard)",
      required: !0
    }),
    region: import_pieces_framework.Property.StaticDropdown({
      displayName: "Region",
      description: "Choose the regional endpoint closest to you for optimal performance",
      required: !0,
      options: {
        options: [
          {
            label: "US West (San Francisco)",
            value: "https://production-sfo.browserless.io"
          },
          {
            label: "Europe UK (London)",
            value: "https://production-lon.browserless.io"
          },
          {
            label: "Europe (Amsterdam)",
            value: "https://production-ams.browserless.io"
          },
          {
            label: "Custom Endpoint",
            value: "custom"
          }
        ]
      }
    }),
    customBaseUrl: import_pieces_framework.Property.ShortText({
      displayName: "Custom Base URL",
      description: "Enter your custom Browserless endpoint URL",
      required: !1
    })
  },
  required: !0
});

// ../ap/packages/pieces/community/browserless/src/lib/actions/capture-screenshot.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/browserless/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
function convertBinaryToBase64(binaryData) {
  return typeof binaryData == "string" ? Buffer.from(binaryData, "binary").toString("base64") : binaryData instanceof ArrayBuffer ? Buffer.from(binaryData).toString("base64") : Buffer.isBuffer(binaryData) ? binaryData.toString("base64") : Buffer.from(String(binaryData), "binary").toString("base64");
}
var browserlessCommon = {
  async apiCall({
    auth,
    method,
    resourceUri,
    body,
    headers = {}
  }) {
    let baseUrl = auth.region === "custom" ? auth.customBaseUrl : auth.region;
    if (!baseUrl)
      throw new Error("Base URL is required. Please configure your Browserless endpoint.");
    let url = `${baseUrl}${resourceUri}`, requestHeaders = __spreadValues({
      "Content-Type": "application/json",
      Accept: "*/*"
    }, headers), responseType = "json";
    (resourceUri.includes("/screenshot") || resourceUri.includes("/pdf")) && (responseType = "arraybuffer");
    let requestConfig = {
      method,
      url,
      headers: requestHeaders,
      body: body ? JSON.stringify(body) : void 0,
      queryParams: { token: auth.apiToken },
      responseType
    };
    return await import_pieces_common.httpClient.sendRequest(requestConfig);
  }
};

// ../ap/packages/pieces/community/browserless/src/lib/actions/capture-screenshot.ts
var captureScreenshot = (0, import_pieces_framework2.createAction)({
  name: "capture_screenshot",
  classification: "READ",
  displayName: "Capture Screenshot",
  description: "Take a screenshot of a web page",
  audience: "both",
  aiMetadata: { description: "Renders a web page in a headless browser and returns a PNG or JPEG screenshot of it. Use to capture the visual state of a public URL; the page URL is required, and options like full-page capture, viewport size, clipping region, and waiting for a CSS selector control what is rendered. Not idempotent: each call runs a fresh headless-browser render, so the returned image reflects the live page at the moment of the call.", idempotent: !1 },
  auth: browserlessAuth,
  props: {
    url: import_pieces_framework2.Property.ShortText({
      displayName: "URL",
      description: "The URL of the page to capture",
      required: !0
    }),
    imageType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Image Type",
      description: "Format of the screenshot image",
      required: !1,
      defaultValue: "png",
      options: {
        options: [
          { label: "PNG", value: "png" },
          { label: "JPEG", value: "jpeg" }
        ]
      }
    }),
    quality: import_pieces_framework2.Property.Number({
      displayName: "Quality",
      description: "Image quality (0-100, only for JPEG)",
      required: !1
    }),
    fullPage: import_pieces_framework2.Property.Checkbox({
      displayName: "Full Page",
      description: "Capture the full scrollable page",
      required: !1,
      defaultValue: !1
    }),
    width: import_pieces_framework2.Property.Number({
      displayName: "Viewport Width",
      description: "Width of the browser viewport in pixels",
      required: !1
    }),
    height: import_pieces_framework2.Property.Number({
      displayName: "Viewport Height",
      description: "Height of the browser viewport in pixels",
      required: !1
    }),
    waitForSelector: import_pieces_framework2.Property.ShortText({
      displayName: "Wait for Selector",
      description: "CSS selector to wait for before taking screenshot",
      required: !1
    }),
    delay: import_pieces_framework2.Property.Number({
      displayName: "Delay (ms)",
      description: "Delay in milliseconds before taking screenshot",
      required: !1
    }),
    omitBackground: import_pieces_framework2.Property.Checkbox({
      displayName: "Omit Background",
      description: "Hide default white background for transparent screenshots",
      required: !1,
      defaultValue: !1
    }),
    clipX: import_pieces_framework2.Property.Number({
      displayName: "Clip X Position",
      description: "X coordinate of the top-left corner for clipping",
      required: !1
    }),
    clipY: import_pieces_framework2.Property.Number({
      displayName: "Clip Y Position",
      description: "Y coordinate of the top-left corner for clipping",
      required: !1
    }),
    clipWidth: import_pieces_framework2.Property.Number({
      displayName: "Clip Width",
      description: "Width of the clipping area",
      required: !1
    }),
    clipHeight: import_pieces_framework2.Property.Number({
      displayName: "Clip Height",
      description: "Height of the clipping area",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let requestBody = {
      url: context.propsValue.url,
      options: {
        type: context.propsValue.imageType || "png",
        fullPage: context.propsValue.fullPage || !1
      }
    };
    context.propsValue.quality && context.propsValue.imageType === "jpeg" && (requestBody.options.quality = context.propsValue.quality), context.propsValue.width && context.propsValue.height && (requestBody.viewport = {
      width: context.propsValue.width,
      height: context.propsValue.height
    }), context.propsValue.waitForSelector && (requestBody.waitForSelector = {
      selector: context.propsValue.waitForSelector
    }), context.propsValue.delay && (requestBody.waitForTimeout = context.propsValue.delay), context.propsValue.omitBackground && (requestBody.options.omitBackground = context.propsValue.omitBackground), context.propsValue.clipX !== void 0 && context.propsValue.clipY !== void 0 && context.propsValue.clipWidth !== void 0 && context.propsValue.clipHeight !== void 0 && (requestBody.options.clip = {
      x: context.propsValue.clipX,
      y: context.propsValue.clipY,
      width: context.propsValue.clipWidth,
      height: context.propsValue.clipHeight
    });
    let response = await browserlessCommon.apiCall({
      auth: context.auth.props,
      method: import_pieces_common2.HttpMethod.POST,
      resourceUri: "/screenshot",
      body: requestBody
    }), imageType = context.propsValue.imageType || "png", fileName = `screenshot.${imageType}`, fileData;
    return response.body instanceof ArrayBuffer ? fileData = Buffer.from(response.body) : Buffer.isBuffer(response.body) ? fileData = response.body : typeof response.body == "string" ? fileData = Buffer.from(response.body, "latin1") : fileData = Buffer.from(String(response.body), "latin1"), {
      success: !0,
      file: await context.files.write({
        data: fileData,
        fileName
      }),
      screenshotBase64: convertBinaryToBase64(fileData),
      metadata: {
        url: context.propsValue.url,
        type: imageType,
        fullPage: context.propsValue.fullPage || !1,
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        contentType: ((_a = response.headers) == null ? void 0 : _a["content-type"]) || `image/${imageType}`,
        fileName
      }
    };
  }
});

// ../ap/packages/pieces/community/browserless/src/lib/actions/generate-pdf.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var generatePdf = (0, import_pieces_framework3.createAction)({
  name: "generate_pdf",
  classification: "READ",
  displayName: "Generate PDF",
  description: "Convert a web page to PDF",
  audience: "both",
  aiMetadata: { description: "Renders content in a headless browser and returns it as a PDF file. Source the content either from a page URL or from a raw HTML string (provide exactly one, not both). Use to produce a printable PDF of a page or supplied markup, with control over paper format, orientation, margins, and headers/footers. Not idempotent: each call runs a fresh headless-browser render, so repeating it produces a new PDF of the page or markup as it renders at that moment.", idempotent: !1 },
  auth: browserlessAuth,
  props: {
    url: import_pieces_framework3.Property.ShortText({
      displayName: "URL",
      description: "The URL of the page to convert to PDF",
      required: !1
    }),
    html: import_pieces_framework3.Property.LongText({
      displayName: "HTML Content",
      description: "HTML content to render as PDF (alternative to URL)",
      required: !1
    }),
    format: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Paper Format",
      description: "Paper format for the PDF",
      required: !1,
      defaultValue: "A4",
      options: {
        options: [
          { label: "A0", value: "A0" },
          { label: "A1", value: "A1" },
          { label: "A2", value: "A2" },
          { label: "A3", value: "A3" },
          { label: "A4", value: "A4" },
          { label: "A5", value: "A5" },
          { label: "A6", value: "A6" },
          { label: "Letter", value: "Letter" },
          { label: "Legal", value: "Legal" },
          { label: "Ledger", value: "Ledger" },
          { label: "Tabloid", value: "Tabloid" }
        ]
      }
    }),
    landscape: import_pieces_framework3.Property.Checkbox({
      displayName: "Landscape",
      description: "Use landscape orientation",
      required: !1,
      defaultValue: !1
    }),
    printBackground: import_pieces_framework3.Property.Checkbox({
      displayName: "Print Background",
      description: "Include background graphics",
      required: !1,
      defaultValue: !0
    }),
    marginTop: import_pieces_framework3.Property.ShortText({
      displayName: "Top Margin",
      description: 'Top margin (e.g., "10mm", "0.4in")',
      required: !1
    }),
    marginRight: import_pieces_framework3.Property.ShortText({
      displayName: "Right Margin",
      description: 'Right margin (e.g., "10mm", "0.4in")',
      required: !1
    }),
    marginBottom: import_pieces_framework3.Property.ShortText({
      displayName: "Bottom Margin",
      description: 'Bottom margin (e.g., "10mm", "0.4in")',
      required: !1
    }),
    marginLeft: import_pieces_framework3.Property.ShortText({
      displayName: "Left Margin",
      description: 'Left margin (e.g., "10mm", "0.4in")',
      required: !1
    }),
    headerTemplate: import_pieces_framework3.Property.LongText({
      displayName: "Header Template",
      description: "HTML template for the print header",
      required: !1
    }),
    footerTemplate: import_pieces_framework3.Property.LongText({
      displayName: "Footer Template",
      description: "HTML template for the print footer",
      required: !1
    }),
    displayHeaderFooter: import_pieces_framework3.Property.Checkbox({
      displayName: "Display Header/Footer",
      description: "Display header and footer",
      required: !1,
      defaultValue: !1
    }),
    scale: import_pieces_framework3.Property.Number({
      displayName: "Scale",
      description: "Scale of the webpage rendering (0.1 - 2.0)",
      required: !1
    }),
    waitForSelector: import_pieces_framework3.Property.ShortText({
      displayName: "Wait for Selector",
      description: "CSS selector to wait for before generating PDF",
      required: !1
    }),
    waitForSelectorTimeout: import_pieces_framework3.Property.Number({
      displayName: "Wait for Selector Timeout",
      description: "Timeout in milliseconds for waiting for selector",
      required: !1
    }),
    waitForSelectorVisible: import_pieces_framework3.Property.Checkbox({
      displayName: "Wait for Selector Visible",
      description: "Wait for selector to be visible",
      required: !1,
      defaultValue: !0
    }),
    waitForSelectorHidden: import_pieces_framework3.Property.Checkbox({
      displayName: "Wait for Selector Hidden",
      description: "Wait for selector to be hidden",
      required: !1,
      defaultValue: !1
    }),
    preferCSSPageSize: import_pieces_framework3.Property.Checkbox({
      displayName: "Prefer CSS Page Size",
      description: "Give any CSS @page size declared in the page priority over format",
      required: !1,
      defaultValue: !1
    }),
    pageRanges: import_pieces_framework3.Property.ShortText({
      displayName: "Page Ranges",
      description: 'Paper ranges to print, e.g. "1-5, 8, 11-13"',
      required: !1
    }),
    width: import_pieces_framework3.Property.ShortText({
      displayName: "Custom Width",
      description: 'Custom width of paper (e.g., "8.5in", "210mm")',
      required: !1
    }),
    height: import_pieces_framework3.Property.ShortText({
      displayName: "Custom Height",
      description: 'Custom height of paper (e.g., "11in", "297mm")',
      required: !1
    }),
    omitBackground: import_pieces_framework3.Property.Checkbox({
      displayName: "Omit Background",
      description: "Hide default white background and allow transparent PDFs",
      required: !1,
      defaultValue: !1
    }),
    tagged: import_pieces_framework3.Property.Checkbox({
      displayName: "Tagged PDF",
      description: "Generate tagged (accessible) PDF",
      required: !1,
      defaultValue: !1
    }),
    outline: import_pieces_framework3.Property.Checkbox({
      displayName: "Generate Outline",
      description: "Generate document outline",
      required: !1,
      defaultValue: !1
    }),
    timeout: import_pieces_framework3.Property.Number({
      displayName: "Timeout (ms)",
      description: "Maximum time to wait for the page to load",
      required: !1
    }),
    waitForFunction: import_pieces_framework3.Property.LongText({
      displayName: "Wait for Function",
      description: "JavaScript function to wait for before generating PDF",
      required: !1
    }),
    waitForFunctionPolling: import_pieces_framework3.Property.ShortText({
      displayName: "Wait for Function Polling",
      description: 'Polling interval for wait function ("raf", "mutation", or number in ms)',
      required: !1
    }),
    waitForFunctionTimeout: import_pieces_framework3.Property.Number({
      displayName: "Wait for Function Timeout",
      description: "Timeout in milliseconds for wait function (0 to disable)",
      required: !1
    }),
    userAgent: import_pieces_framework3.Property.ShortText({
      displayName: "User Agent",
      description: "Custom user agent string to use for the request",
      required: !1
    }),
    waitForTimeout: import_pieces_framework3.Property.Number({
      displayName: "Wait Timeout (ms)",
      description: "Timeout in milliseconds to wait before generating PDF",
      required: !1
    }),
    bestAttempt: import_pieces_framework3.Property.Checkbox({
      displayName: "Best Attempt",
      description: "Attempt to proceed when awaited events fail or timeout",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    if (!context.propsValue.url && !context.propsValue.html)
      throw new Error("Either URL or HTML content must be provided");
    if (context.propsValue.url && context.propsValue.html)
      throw new Error("Cannot provide both URL and HTML content. Choose one.");
    let requestBody = {
      options: {
        format: context.propsValue.format || "A4",
        landscape: context.propsValue.landscape || !1,
        printBackground: context.propsValue.printBackground !== !1,
        displayHeaderFooter: context.propsValue.displayHeaderFooter || !1
      }
    };
    context.propsValue.url ? requestBody.url = context.propsValue.url : context.propsValue.html && (requestBody.html = context.propsValue.html), context.propsValue.userAgent && (requestBody.userAgent = context.propsValue.userAgent), context.propsValue.waitForTimeout && (requestBody.waitForTimeout = context.propsValue.waitForTimeout), context.propsValue.bestAttempt && (requestBody.bestAttempt = context.propsValue.bestAttempt);
    let margin = {};
    if (context.propsValue.marginTop && (margin.top = context.propsValue.marginTop), context.propsValue.marginRight && (margin.right = context.propsValue.marginRight), context.propsValue.marginBottom && (margin.bottom = context.propsValue.marginBottom), context.propsValue.marginLeft && (margin.left = context.propsValue.marginLeft), Object.keys(margin).length > 0 && (requestBody.options.margin = margin), context.propsValue.headerTemplate && (requestBody.options.headerTemplate = context.propsValue.headerTemplate), context.propsValue.footerTemplate && (requestBody.options.footerTemplate = context.propsValue.footerTemplate), context.propsValue.scale && (requestBody.options.scale = Math.max(0.1, Math.min(2, context.propsValue.scale))), context.propsValue.waitForSelector) {
      let waitForSelectorObj = {
        selector: context.propsValue.waitForSelector
      };
      context.propsValue.waitForSelectorTimeout !== void 0 && (waitForSelectorObj.timeout = context.propsValue.waitForSelectorTimeout), context.propsValue.waitForSelectorVisible !== void 0 && (waitForSelectorObj.visible = context.propsValue.waitForSelectorVisible), context.propsValue.waitForSelectorHidden !== void 0 && (waitForSelectorObj.hidden = context.propsValue.waitForSelectorHidden), requestBody.options.waitForSelector = waitForSelectorObj;
    }
    if (context.propsValue.preferCSSPageSize && (requestBody.options.preferCSSPageSize = context.propsValue.preferCSSPageSize), context.propsValue.pageRanges && (requestBody.options.pageRanges = context.propsValue.pageRanges), context.propsValue.width && (requestBody.options.width = context.propsValue.width), context.propsValue.height && (requestBody.options.height = context.propsValue.height), context.propsValue.omitBackground && (requestBody.options.omitBackground = context.propsValue.omitBackground), context.propsValue.tagged && (requestBody.options.tagged = context.propsValue.tagged), context.propsValue.outline && (requestBody.options.outline = context.propsValue.outline), context.propsValue.timeout && (requestBody.options.timeout = context.propsValue.timeout), context.propsValue.waitForFunction) {
      let waitForFunctionObj = {
        fn: context.propsValue.waitForFunction
      };
      context.propsValue.waitForFunctionPolling !== void 0 && (waitForFunctionObj.polling = context.propsValue.waitForFunctionPolling), context.propsValue.waitForFunctionTimeout !== void 0 && (waitForFunctionObj.timeout = context.propsValue.waitForFunctionTimeout), requestBody.options.waitForFunction = waitForFunctionObj;
    }
    let response = await browserlessCommon.apiCall({
      auth: context.auth.props,
      method: import_pieces_common3.HttpMethod.POST,
      resourceUri: "/pdf",
      body: requestBody
    }), fileName = "document.pdf", fileData;
    return response.body instanceof ArrayBuffer ? fileData = Buffer.from(response.body) : Buffer.isBuffer(response.body) ? fileData = response.body : typeof response.body == "string" ? fileData = Buffer.from(response.body, "latin1") : fileData = Buffer.from(String(response.body), "latin1"), {
      success: !0,
      file: await context.files.write({
        data: fileData,
        fileName
      }),
      pdfBase64: convertBinaryToBase64(fileData),
      metadata: {
        source: context.propsValue.url ? "url" : "html",
        url: context.propsValue.url || null,
        hasHtml: !!context.propsValue.html,
        format: context.propsValue.format || "A4",
        landscape: context.propsValue.landscape || !1,
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        fileName,
        contentType: "application/pdf"
      }
    };
  }
});

// ../ap/packages/pieces/community/browserless/src/lib/actions/scrape-url.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var scrapeUrl = (0, import_pieces_framework4.createAction)({
  name: "scrape_url",
  classification: "READ",
  displayName: "Scrape URL",
  description: "Extract content from a web page",
  audience: "both",
  aiMetadata: { description: "Loads a web page in a headless browser and extracts the elements matched by the CSS selectors you supply. Use to pull specific text or attributes from a page rather than capturing an image; both the page URL and at least one element selector are required. Read-only: re-running with the same input re-scrapes the page and returns equivalent data without side effects.", idempotent: !0 },
  auth: browserlessAuth,
  props: {
    url: import_pieces_framework4.Property.ShortText({
      displayName: "URL",
      description: "The URL of the page to scrape",
      required: !0
    }),
    elements: import_pieces_framework4.Property.Array({
      displayName: "Elements to Extract",
      description: "CSS selectors for elements to extract",
      required: !0,
      properties: {
        selector: import_pieces_framework4.Property.ShortText({
          displayName: "CSS Selector",
          description: "CSS selector for the element",
          required: !0
        }),
        timeout: import_pieces_framework4.Property.Number({
          displayName: "Timeout (ms)",
          description: "Timeout in milliseconds for this specific selector",
          required: !1
        })
      }
    }),
    waitForSelector: import_pieces_framework4.Property.ShortText({
      displayName: "Wait for Selector",
      description: "CSS selector to wait for before scraping",
      required: !1
    }),
    waitForSelectorTimeout: import_pieces_framework4.Property.Number({
      displayName: "Wait for Selector Timeout",
      description: "Timeout in milliseconds for waiting for selector",
      required: !1
    }),
    waitForSelectorVisible: import_pieces_framework4.Property.Checkbox({
      displayName: "Wait for Selector Visible",
      description: "Wait for selector to be visible",
      required: !1,
      defaultValue: !0
    }),
    waitForSelectorHidden: import_pieces_framework4.Property.Checkbox({
      displayName: "Wait for Selector Hidden",
      description: "Wait for selector to be hidden",
      required: !1,
      defaultValue: !1
    }),
    waitForTimeout: import_pieces_framework4.Property.Number({
      displayName: "Wait Timeout (ms)",
      description: "Timeout in milliseconds to wait before scraping",
      required: !1
    }),
    waitForEvent: import_pieces_framework4.Property.ShortText({
      displayName: "Wait for Event",
      description: "Event name to wait for before scraping",
      required: !1
    }),
    waitForEventTimeout: import_pieces_framework4.Property.Number({
      displayName: "Wait for Event Timeout",
      description: "Timeout in milliseconds for wait event",
      required: !1
    }),
    debugConsole: import_pieces_framework4.Property.Checkbox({
      displayName: "Debug Console",
      description: "Include console logs in debug output",
      required: !1,
      defaultValue: !1
    }),
    debugCookies: import_pieces_framework4.Property.Checkbox({
      displayName: "Debug Cookies",
      description: "Include cookies in debug output",
      required: !1,
      defaultValue: !1
    }),
    debugNetwork: import_pieces_framework4.Property.Checkbox({
      displayName: "Debug Network",
      description: "Include network requests in debug output",
      required: !1,
      defaultValue: !1
    }),
    bestAttempt: import_pieces_framework4.Property.Checkbox({
      displayName: "Best Attempt",
      description: "Attempt to proceed when awaited events fail or timeout",
      required: !1,
      defaultValue: !1
    }),
    userAgent: import_pieces_framework4.Property.ShortText({
      displayName: "User Agent",
      description: "Custom user agent string",
      required: !1
    }),
    cookies: import_pieces_framework4.Property.Array({
      displayName: "Cookies",
      description: "Cookies to set before scraping",
      required: !1,
      properties: {
        name: import_pieces_framework4.Property.ShortText({
          displayName: "Cookie Name",
          required: !0
        }),
        value: import_pieces_framework4.Property.ShortText({
          displayName: "Cookie Value",
          required: !0
        }),
        domain: import_pieces_framework4.Property.ShortText({
          displayName: "Domain",
          required: !1
        })
      }
    }),
    timeout: import_pieces_framework4.Property.Number({
      displayName: "Timeout (ms)",
      description: "Maximum time to wait for the page to load",
      required: !1,
      defaultValue: 3e4
    }),
    waitUntil: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Wait Until",
      description: "When to consider navigation complete",
      required: !1,
      defaultValue: "networkidle2",
      options: {
        options: [
          { label: "Load Event", value: "load" },
          { label: "DOM Content Loaded", value: "domcontentloaded" },
          { label: "Network Idle 0", value: "networkidle0" },
          { label: "Network Idle 2", value: "networkidle2" }
        ]
      }
    }),
    waitForFunction: import_pieces_framework4.Property.LongText({
      displayName: "Wait for Function",
      description: "JavaScript function to wait for before scraping (should return true when ready)",
      required: !1
    }),
    viewportWidth: import_pieces_framework4.Property.Number({
      displayName: "Viewport Width",
      description: "Browser viewport width in pixels",
      required: !1,
      defaultValue: 1920
    }),
    viewportHeight: import_pieces_framework4.Property.Number({
      displayName: "Viewport Height",
      description: "Browser viewport height in pixels",
      required: !1,
      defaultValue: 1080
    })
  },
  async run(context) {
    let requestBody = {
      url: context.propsValue.url,
      elements: (context.propsValue.elements || []).map((element) => {
        let selector = element.selector;
        if (typeof selector == "string")
          try {
            let parsed = JSON.parse(selector);
            parsed.selector && (selector = parsed.selector);
          } catch (e) {
            console.error("Error parsing selector:", e);
          }
        else typeof selector == "object" && selector.selector && (selector = selector.selector);
        let elementConfig = {
          selector
        };
        return element.timeout !== void 0 && (elementConfig.timeout = element.timeout), elementConfig;
      })
    };
    if ((context.propsValue.timeout || context.propsValue.waitUntil) && (requestBody.gotoOptions = {}, context.propsValue.timeout && (requestBody.gotoOptions.timeout = context.propsValue.timeout), context.propsValue.waitUntil && (requestBody.gotoOptions.waitUntil = context.propsValue.waitUntil)), context.propsValue.waitForSelector) {
      let waitForSelectorObj = {
        selector: context.propsValue.waitForSelector
      };
      context.propsValue.waitForSelectorTimeout !== void 0 && (waitForSelectorObj.timeout = context.propsValue.waitForSelectorTimeout), context.propsValue.waitForSelectorVisible !== void 0 && (waitForSelectorObj.visible = context.propsValue.waitForSelectorVisible), context.propsValue.waitForSelectorHidden !== void 0 && (waitForSelectorObj.hidden = context.propsValue.waitForSelectorHidden), requestBody.waitForSelector = waitForSelectorObj;
    }
    if (context.propsValue.waitForTimeout && (requestBody.waitForTimeout = context.propsValue.waitForTimeout), context.propsValue.waitForEvent) {
      let waitForEventObj = {
        event: context.propsValue.waitForEvent
      };
      context.propsValue.waitForEventTimeout !== void 0 && (waitForEventObj.timeout = context.propsValue.waitForEventTimeout), requestBody.waitForEvent = waitForEventObj;
    }
    let debugOpts = {};
    return context.propsValue.debugConsole && (debugOpts.console = !0), context.propsValue.debugCookies && (debugOpts.cookies = !0), context.propsValue.debugNetwork && (debugOpts.network = !0), Object.keys(debugOpts).length > 0 && (requestBody.debugOpts = debugOpts), context.propsValue.bestAttempt && (requestBody.bestAttempt = context.propsValue.bestAttempt), context.propsValue.waitForFunction && (requestBody.waitForFunction = {
      fn: context.propsValue.waitForFunction
    }), context.propsValue.userAgent && (requestBody.userAgent = context.propsValue.userAgent), context.propsValue.viewportWidth && context.propsValue.viewportHeight && (requestBody.viewport = {
      width: context.propsValue.viewportWidth,
      height: context.propsValue.viewportHeight
    }), context.propsValue.cookies && context.propsValue.cookies.length > 0 && (requestBody.cookies = context.propsValue.cookies.map((cookie) => __spreadValues({
      name: cookie.name,
      value: cookie.value
    }, cookie.domain && { domain: cookie.domain }))), {
      success: !0,
      data: (await browserlessCommon.apiCall({
        auth: context.auth.props,
        method: import_pieces_common4.HttpMethod.POST,
        resourceUri: "/scrape",
        body: requestBody
      })).body,
      metadata: {
        url: context.propsValue.url,
        elementsCount: (context.propsValue.elements || []).length,
        timestamp: (/* @__PURE__ */ new Date()).toISOString()
      }
    };
  }
});

// ../ap/packages/pieces/community/browserless/src/lib/actions/run-bql-query.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var runBqlQuery = (0, import_pieces_framework5.createAction)({
  name: "run_bql_query",
  classification: "WRITE",
  displayName: "Run BQL Query",
  description: "Execute Browser Query Language (BQL) GraphQL-based queries for advanced browser automation",
  audience: "both",
  aiMetadata: { description: "Runs a raw Browser Query Language (BQL) GraphQL query against a headless Chromium browser for advanced automation such as navigation, clicking, typing, and multi-step scripted browser flows. Use when the simpler screenshot/scrape/PDF actions cannot express the interaction; requires a valid BQL query string and supports session options like stealth, proxy, and cookies. Not idempotent: the query can perform stateful browser actions and side-effecting navigation, so repeating it may not yield the same result.", idempotent: !1 },
  auth: browserlessAuth,
  props: {
    query: import_pieces_framework5.Property.LongText({
      displayName: "BQL Query",
      description: 'GraphQL-based BQL query for browser automation. Example: mutation { goto(url: "https://example.com") { status } }',
      required: !0
    }),
    variables: import_pieces_framework5.Property.Object({
      displayName: "Query Variables",
      description: "Variables to pass to the BQL query (JSON object)",
      required: !1
    }),
    operationName: import_pieces_framework5.Property.ShortText({
      displayName: "Operation Name",
      description: "Name of the GraphQL operation to execute",
      required: !1
    }),
    timeout: import_pieces_framework5.Property.Number({
      displayName: "Timeout (ms)",
      description: "Maximum execution time in milliseconds",
      required: !1,
      defaultValue: 3e4
    }),
    stealth: import_pieces_framework5.Property.Checkbox({
      displayName: "Stealth Mode",
      description: "Enable stealth mode for bot detection bypass",
      required: !1,
      defaultValue: !0
    }),
    headless: import_pieces_framework5.Property.Checkbox({
      displayName: "Headless Mode",
      description: "Run browser in headless mode (set to false for GUI)",
      required: !1,
      defaultValue: !0
    }),
    humanlike: import_pieces_framework5.Property.Checkbox({
      displayName: "Human-like Behavior",
      description: "Enable human-like mouse movement, typing, and delays",
      required: !1,
      defaultValue: !1
    }),
    proxy: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Proxy Type",
      description: "Type of proxy to use",
      required: !1,
      options: {
        options: [
          { label: "Residential", value: "residential" },
          { label: "None", value: "none" }
        ]
      }
    }),
    proxyCountry: import_pieces_framework5.Property.ShortText({
      displayName: "Proxy Country",
      description: "Country code for residential proxy (e.g., us, gb, de)",
      required: !1
    }),
    proxySticky: import_pieces_framework5.Property.Checkbox({
      displayName: "Sticky Proxy",
      description: "Maintain same proxy IP across session",
      required: !1,
      defaultValue: !1
    }),
    blockAds: import_pieces_framework5.Property.Checkbox({
      displayName: "Block Ads",
      description: "Enable ad blocker (uBlock Origin)",
      required: !1,
      defaultValue: !1
    }),
    blockConsentModals: import_pieces_framework5.Property.Checkbox({
      displayName: "Block Consent Modals",
      description: "Automatically block/dismiss cookie consent banners",
      required: !1,
      defaultValue: !1
    }),
    record: import_pieces_framework5.Property.Checkbox({
      displayName: "Record Session",
      description: "Enable session recording for debugging",
      required: !1,
      defaultValue: !1
    }),
    slowMo: import_pieces_framework5.Property.Number({
      displayName: "Slow Motion (ms)",
      description: "Add delays between browser actions in milliseconds",
      required: !1
    }),
    ignoreHTTPSErrors: import_pieces_framework5.Property.Checkbox({
      displayName: "Ignore HTTPS Errors",
      description: "Ignore HTTPS certificate errors during navigation",
      required: !1,
      defaultValue: !1
    }),
    userAgent: import_pieces_framework5.Property.ShortText({
      displayName: "User Agent",
      description: "Custom user agent string",
      required: !1
    }),
    viewportWidth: import_pieces_framework5.Property.Number({
      displayName: "Viewport Width",
      description: "Browser viewport width",
      required: !1
    }),
    viewportHeight: import_pieces_framework5.Property.Number({
      displayName: "Viewport Height",
      description: "Browser viewport height",
      required: !1
    }),
    cookies: import_pieces_framework5.Property.Array({
      displayName: "Cookies",
      description: "Cookies to set before executing BQL query",
      required: !1,
      properties: {
        name: import_pieces_framework5.Property.ShortText({
          displayName: "Cookie Name",
          required: !0
        }),
        value: import_pieces_framework5.Property.ShortText({
          displayName: "Cookie Value",
          required: !0
        }),
        url: import_pieces_framework5.Property.ShortText({
          displayName: "URL",
          description: "Request-URI to associate with the cookie",
          required: !1
        }),
        domain: import_pieces_framework5.Property.ShortText({
          displayName: "Domain",
          required: !1
        }),
        path: import_pieces_framework5.Property.ShortText({
          displayName: "Path",
          required: !1
        }),
        secure: import_pieces_framework5.Property.Checkbox({
          displayName: "Secure",
          description: "Indicates if the cookie is secure",
          required: !1,
          defaultValue: !1
        }),
        httpOnly: import_pieces_framework5.Property.Checkbox({
          displayName: "HTTP Only",
          description: "Indicates if the cookie is HTTP-only",
          required: !1,
          defaultValue: !1
        }),
        sameSite: import_pieces_framework5.Property.StaticDropdown({
          displayName: "SameSite",
          description: "SameSite policy for the cookie",
          required: !1,
          options: {
            options: [
              { label: "Strict", value: "Strict" },
              { label: "Lax", value: "Lax" },
              { label: "None", value: "None" }
            ]
          }
        }),
        expires: import_pieces_framework5.Property.Number({
          displayName: "Expires",
          description: "Expiration date as timestamp (session cookie if not set)",
          required: !1
        })
      }
    })
  },
  async run(context) {
    var _a;
    let requestBody = {
      query: context.propsValue.query
    };
    context.propsValue.variables && (requestBody.variables = context.propsValue.variables), context.propsValue.operationName && (requestBody.operationName = context.propsValue.operationName);
    let resourceUri = "/chromium/bql", queryParams = [];
    if (context.propsValue.timeout && queryParams.push(`timeout=${context.propsValue.timeout}`), context.propsValue.stealth !== void 0 && queryParams.push(`stealth=${context.propsValue.stealth}`), context.propsValue.headless !== void 0 && queryParams.push(`headless=${context.propsValue.headless}`), context.propsValue.humanlike && queryParams.push("humanlike=true"), context.propsValue.proxy && context.propsValue.proxy !== "none" && (queryParams.push(`proxy=${context.propsValue.proxy}`), context.propsValue.proxyCountry && queryParams.push(`proxyCountry=${context.propsValue.proxyCountry}`), context.propsValue.proxySticky && queryParams.push("proxySticky=true")), context.propsValue.blockAds && queryParams.push("blockAds=true"), context.propsValue.blockConsentModals && queryParams.push("blockConsentModals=true"), context.propsValue.record && queryParams.push("record=true"), context.propsValue.slowMo && queryParams.push(`slowMo=${context.propsValue.slowMo}`), context.propsValue.ignoreHTTPSErrors && queryParams.push("ignoreHTTPSErrors=true"), context.propsValue.userAgent && queryParams.push(`userAgent=${encodeURIComponent(context.propsValue.userAgent)}`), context.propsValue.viewportWidth && context.propsValue.viewportHeight && queryParams.push(`viewport=${context.propsValue.viewportWidth}x${context.propsValue.viewportHeight}`), context.propsValue.cookies && context.propsValue.cookies.length > 0) {
      let cookiesJson = JSON.stringify(context.propsValue.cookies.map((cookie) => __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
        name: cookie.name,
        value: cookie.value
      }, cookie.url && { url: cookie.url }), cookie.domain && { domain: cookie.domain }), cookie.path && { path: cookie.path }), cookie.secure !== void 0 && { secure: cookie.secure }), cookie.httpOnly !== void 0 && { httpOnly: cookie.httpOnly }), cookie.sameSite && { sameSite: cookie.sameSite }), cookie.expires !== void 0 && { expires: cookie.expires })));
      queryParams.push(`cookies=${encodeURIComponent(cookiesJson)}`);
    }
    queryParams.length > 0 && (resourceUri += `?${queryParams.join("&")}`);
    let response = await browserlessCommon.apiCall({
      auth: context.auth.props,
      method: import_pieces_common5.HttpMethod.POST,
      resourceUri,
      body: requestBody
    }), parsedResult;
    try {
      parsedResult = typeof response.body == "string" ? JSON.parse(response.body) : response.body;
    } catch (error) {
      parsedResult = response.body;
    }
    return {
      success: !0,
      data: (parsedResult == null ? void 0 : parsedResult.data) || null,
      errors: (parsedResult == null ? void 0 : parsedResult.errors) || null,
      result: parsedResult,
      metadata: {
        browserType: "chromium",
        executionTime: ((_a = response.headers) == null ? void 0 : _a["x-response-time"]) || "unknown",
        timestamp: (/* @__PURE__ */ new Date()).toISOString(),
        stealth: context.propsValue.stealth || !1
      }
    };
  }
});

// ../ap/packages/pieces/community/browserless/src/lib/actions/get-website-performance.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getWebsitePerformance = (0, import_pieces_framework6.createAction)({
  name: "get_website_performance",
  classification: "READ",
  displayName: "Get Website Performance",
  description: "Analyze website performance metrics using Lighthouse",
  audience: "both",
  aiMetadata: { description: "Runs a Lighthouse audit on a public URL in a headless browser and returns performance, accessibility, best-practices, SEO, and PWA scores plus key metrics. Use to measure a page's quality; the URL is required, and you can scope the audit to specific categories and simulate a desktop or mobile device with network throttling. Read-only analysis: re-running with the same input re-audits the page without side effects (scores may vary slightly run to run).", idempotent: !0 },
  auth: browserlessAuth,
  props: {
    url: import_pieces_framework6.Property.ShortText({
      displayName: "URL",
      description: "The URL of the website to analyze",
      required: !0
    }),
    categories: import_pieces_framework6.Property.Array({
      displayName: "Performance Categories",
      description: "Select which performance categories to analyze (performance, accessibility, best-practices, seo, pwa)",
      required: !1,
      properties: {
        category: import_pieces_framework6.Property.StaticDropdown({
          displayName: "Category",
          required: !0,
          options: {
            options: [
              { label: "Performance", value: "performance" },
              { label: "Accessibility", value: "accessibility" },
              { label: "Best Practices", value: "best-practices" },
              { label: "SEO", value: "seo" },
              { label: "PWA", value: "pwa" }
            ]
          }
        })
      }
    }),
    device: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Device Type",
      description: "Device type for performance analysis",
      required: !1,
      defaultValue: "desktop",
      options: {
        options: [
          { label: "Desktop", value: "desktop" },
          { label: "Mobile", value: "mobile" }
        ]
      }
    }),
    throttling: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Network Throttling",
      description: "Network throttling simulation",
      required: !1,
      defaultValue: "mobileSlow4G",
      options: {
        options: [
          { label: "No Throttling", value: "none" },
          { label: "Slow 4G", value: "mobileSlow4G" },
          { label: "Regular 4G", value: "mobileRegular4G" },
          { label: "Fast 4G", value: "mobileFast4G" }
        ]
      }
    }),
    onlyCategories: import_pieces_framework6.Property.Checkbox({
      displayName: "Only Category Scores",
      description: "Return only category scores without detailed audit results",
      required: !1,
      defaultValue: !1
    }),
    locale: import_pieces_framework6.Property.ShortText({
      displayName: "Locale",
      description: "Locale for the analysis (e.g., en-US, de-DE)",
      required: !1,
      defaultValue: "en-US"
    }),
    userAgent: import_pieces_framework6.Property.ShortText({
      displayName: "User Agent",
      description: "Custom user agent string",
      required: !1
    }),
    timeout: import_pieces_framework6.Property.Number({
      displayName: "Timeout (ms)",
      description: "Maximum time to wait for the analysis to complete",
      required: !1,
      defaultValue: 6e4
    }),
    waitForSelector: import_pieces_framework6.Property.ShortText({
      displayName: "Wait for Selector",
      description: "CSS selector to wait for before running performance analysis",
      required: !1
    }),
    emulateMediaType: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Emulate Media Type",
      description: "Emulate CSS media type",
      required: !1,
      options: {
        options: [
          { label: "Screen", value: "screen" },
          { label: "Print", value: "print" }
        ]
      }
    }),
    budgets: import_pieces_framework6.Property.Array({
      displayName: "Performance Budgets",
      description: "Lighthouse performance budgets for resource sizes",
      required: !1,
      properties: {
        resourceType: import_pieces_framework6.Property.StaticDropdown({
          displayName: "Resource Type",
          description: "Type of resource to budget",
          required: !0,
          options: {
            options: [
              { label: "Document", value: "document" },
              { label: "Script", value: "script" },
              { label: "Stylesheet", value: "stylesheet" },
              { label: "Image", value: "image" },
              { label: "Media", value: "media" },
              { label: "Font", value: "font" },
              { label: "Other", value: "other" },
              { label: "Third-party", value: "third-party" }
            ]
          }
        }),
        budget: import_pieces_framework6.Property.Number({
          displayName: "Budget Size (KB)",
          description: "Maximum allowed size in kilobytes",
          required: !0
        })
      }
    }),
    stealth: import_pieces_framework6.Property.Checkbox({
      displayName: "Stealth Mode",
      description: "Enable stealth mode for bot detection bypass",
      required: !1,
      defaultValue: !1
    }),
    blockAds: import_pieces_framework6.Property.Checkbox({
      displayName: "Block Ads",
      description: "Enable ad blocker during performance analysis",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t, _u, _v, _w, _x, _y, _z, _A, _B, _C, _D;
    let requestBody = {
      url: context.propsValue.url
    }, lighthouseConfig = {
      extends: "lighthouse:default",
      settings: {}
    };
    if (context.propsValue.categories && context.propsValue.categories.length > 0 && (lighthouseConfig.settings.onlyCategories = context.propsValue.categories.map((cat) => cat.category)), context.propsValue.locale && (lighthouseConfig.settings.locale = context.propsValue.locale), context.propsValue.device && (lighthouseConfig.settings.formFactor = context.propsValue.device), context.propsValue.throttling && context.propsValue.throttling !== "none")
      switch (lighthouseConfig.settings.throttling = { rttMs: 150, throughputKbps: 1638.4, cpuSlowdownMultiplier: 4 }, context.propsValue.throttling) {
        case "mobileSlow4G":
          lighthouseConfig.settings.throttling = { rttMs: 150, throughputKbps: 1638.4, cpuSlowdownMultiplier: 4 };
          break;
        case "mobileRegular4G":
          lighthouseConfig.settings.throttling = { rttMs: 100, throughputKbps: 2048, cpuSlowdownMultiplier: 3 };
          break;
        case "mobileFast4G":
          lighthouseConfig.settings.throttling = { rttMs: 50, throughputKbps: 4096, cpuSlowdownMultiplier: 2 };
          break;
      }
    context.propsValue.userAgent && (lighthouseConfig.settings.userAgent = context.propsValue.userAgent), context.propsValue.timeout && (lighthouseConfig.settings.timeout = context.propsValue.timeout), context.propsValue.waitForSelector && (lighthouseConfig.settings.waitForSelector = context.propsValue.waitForSelector), context.propsValue.emulateMediaType && (lighthouseConfig.settings.emulatedFormFactor = context.propsValue.emulateMediaType), context.propsValue.onlyCategories && !lighthouseConfig.settings.onlyCategories && (lighthouseConfig.settings.onlyCategories = ["performance", "accessibility", "best-practices", "seo", "pwa"]), requestBody.config = lighthouseConfig, context.propsValue.budgets && context.propsValue.budgets.length > 0 && (requestBody.budgets = context.propsValue.budgets.map((budget) => ({
      resourceType: budget.resourceType,
      budget: budget.budget * 1024
    })));
    let resourceUri = "/performance", queryParams = [];
    context.propsValue.stealth && queryParams.push("stealth=true"), context.propsValue.blockAds && queryParams.push("blockAds=true"), queryParams.length > 0 && (resourceUri += `?${queryParams.join("&")}`);
    let response = await browserlessCommon.apiCall({
      auth: context.auth.props,
      method: import_pieces_common6.HttpMethod.POST,
      resourceUri,
      body: requestBody
    }), performanceData = response.body, summary = {
      url: context.propsValue.url,
      formFactor: context.propsValue.device || "desktop",
      timestamp: (/* @__PURE__ */ new Date()).toISOString()
    };
    if (performanceData.lhr && performanceData.lhr.categories) {
      let categories = performanceData.lhr.categories;
      summary.scores = {
        performance: (_a = categories.performance) != null && _a.score ? Math.round(categories.performance.score * 100) : null,
        accessibility: (_b = categories.accessibility) != null && _b.score ? Math.round(categories.accessibility.score * 100) : null,
        bestPractices: (_c = categories["best-practices"]) != null && _c.score ? Math.round(categories["best-practices"].score * 100) : null,
        seo: (_d = categories.seo) != null && _d.score ? Math.round(categories.seo.score * 100) : null,
        pwa: (_e = categories.pwa) != null && _e.score ? Math.round(categories.pwa.score * 100) : null
      };
    }
    if (performanceData.lhr && performanceData.lhr.audits) {
      let audits = performanceData.lhr.audits;
      summary.metrics = {
        firstContentfulPaint: {
          value: ((_f = audits["first-contentful-paint"]) == null ? void 0 : _f.displayValue) || null,
          score: (_g = audits["first-contentful-paint"]) != null && _g.score ? Math.round(audits["first-contentful-paint"].score * 100) : null
        },
        largestContentfulPaint: {
          value: ((_h = audits["largest-contentful-paint"]) == null ? void 0 : _h.displayValue) || null,
          score: (_i = audits["largest-contentful-paint"]) != null && _i.score ? Math.round(audits["largest-contentful-paint"].score * 100) : null
        },
        firstMeaningfulPaint: {
          value: ((_j = audits["first-meaningful-paint"]) == null ? void 0 : _j.displayValue) || null,
          score: (_k = audits["first-meaningful-paint"]) != null && _k.score ? Math.round(audits["first-meaningful-paint"].score * 100) : null
        },
        speedIndex: {
          value: ((_l = audits["speed-index"]) == null ? void 0 : _l.displayValue) || null,
          score: (_m = audits["speed-index"]) != null && _m.score ? Math.round(audits["speed-index"].score * 100) : null
        },
        timeToInteractive: {
          value: ((_n = audits.interactive) == null ? void 0 : _n.displayValue) || null,
          score: (_o = audits.interactive) != null && _o.score ? Math.round(audits.interactive.score * 100) : null
        },
        totalBlockingTime: {
          value: ((_p = audits["total-blocking-time"]) == null ? void 0 : _p.displayValue) || null,
          score: (_q = audits["total-blocking-time"]) != null && _q.score ? Math.round(audits["total-blocking-time"].score * 100) : null
        },
        cumulativeLayoutShift: {
          value: ((_r = audits["cumulative-layout-shift"]) == null ? void 0 : _r.displayValue) || null,
          score: (_s = audits["cumulative-layout-shift"]) != null && _s.score ? Math.round(audits["cumulative-layout-shift"].score * 100) : null
        }
      }, summary.opportunities = [], ((_v = (_u = (_t = audits["unused-css-rules"]) == null ? void 0 : _t.details) == null ? void 0 : _u.items) == null ? void 0 : _v.length) > 0 && summary.opportunities.push({
        type: "unused-css",
        title: "Remove unused CSS",
        potentialSavings: audits["unused-css-rules"].displayValue || "Unknown"
      }), ((_y = (_x = (_w = audits["unused-javascript"]) == null ? void 0 : _w.details) == null ? void 0 : _x.items) == null ? void 0 : _y.length) > 0 && summary.opportunities.push({
        type: "unused-javascript",
        title: "Remove unused JavaScript",
        potentialSavings: audits["unused-javascript"].displayValue || "Unknown"
      }), ((_B = (_A = (_z = audits["render-blocking-resources"]) == null ? void 0 : _z.details) == null ? void 0 : _A.items) == null ? void 0 : _B.length) > 0 && summary.opportunities.push({
        type: "render-blocking",
        title: "Eliminate render-blocking resources",
        potentialSavings: audits["render-blocking-resources"].displayValue || "Unknown"
      });
    }
    return {
      success: !0,
      summary,
      fullReport: performanceData,
      metadata: {
        analysisTime: ((_C = response.headers) == null ? void 0 : _C["x-response-time"]) || "unknown",
        lighthouseVersion: ((_D = performanceData.lhr) == null ? void 0 : _D.lighthouseVersion) || "unknown"
      }
    };
  }
});

// ../ap/packages/pieces/community/browserless/src/index.ts
var browserless = (0, import_pieces_framework7.createPiece)({
  displayName: "Browserless",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/browserless.png",
  categories: [import_pieces_framework8.PieceCategory.DEVELOPER_TOOLS],
  description: "Browserless is a headless browser automation tool that allows you to scrape websites, take screenshots, and more.",
  authors: ["owuzo", "onyedikachi-david"],
  auth: browserlessAuth,
  actions: [
    captureScreenshot,
    generatePdf,
    scrapeUrl,
    runBqlQuery,
    getWebsitePerformance
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  browserless
});
