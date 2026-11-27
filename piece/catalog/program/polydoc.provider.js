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

// ../ap/packages/pieces/community/polydoc/src/index.ts
var index_exports = {};
__export(index_exports, {
  polydoc: () => polydoc
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/polydoc/src/lib/actions/capture-screenshot.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/polydoc/src/lib/common/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/polydoc/src/lib/common/constants.ts
var DEFAULT_BASE_URL = "https://api.polydoc.tech", PDF_CONVERT_PATH = "/pdf/convert", SCREENSHOT_CONVERT_PATH = "/screenshot/convert", SOURCE_TYPES = [
  { label: "URL (render a web page)", value: "url" },
  { label: "HTML (render an inline HTML string)", value: "html" },
  { label: "Template (render a saved PolyDoc template by ID)", value: "template" }
], DELIVERY_MODES = [
  { label: "Download (return the file)", value: "download" },
  { label: "Cloud Storage (upload to a presigned URL)", value: "cloudStorage" },
  { label: "Webhook (deliver the file to a URL)", value: "webhook" }
], PAGE_FORMATS = ["A3", "A4", "A5", "Ledger", "Legal", "Letter", "Tabloid"], IMAGE_TYPES = ["png", "jpeg", "webp"], SCREENSHOT_OUTPUT_ENCODINGS = [
  { label: "Binary file (write the image to a file)", value: "binaryFile" },
  { label: "Base64 (return the image as a base64 string)", value: "base64" }
], EINVOICE_STANDARDS = [
  { label: "ZUGFeRD (Germany / EU)", value: "zugferd" },
  { label: "Factur-X (France / EU)", value: "facturx" }
], EINVOICE_PROFILES = ["minimum", "basicwl", "basic", "en16931", "extended"];

// ../ap/packages/pieces/community/polydoc/src/lib/common/auth.ts
var AUTH_GUIDE = `Get an API key from the PolyDoc dashboard (dashboard.polydoc.tech, API Keys). It is sent as Authorization: Bearer <key>.

Turn on **Sandbox** to test against sandbox quota (output is watermarked). Leave it off for production conversions.`, polydocAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: AUTH_GUIDE,
  required: !0,
  props: {
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your PolyDoc API key from dashboard.polydoc.tech.",
      required: !0
    }),
    sandbox: import_pieces_framework.Property.Checkbox({
      displayName: "Sandbox",
      description: "Use sandbox quota (watermarked output).",
      required: !1,
      defaultValue: !1
    })
  },
  validate: async ({ auth }) => {
    var _a;
    let apiKey = auth.apiKey;
    if (!apiKey)
      return { valid: !1, error: "API key is required." };
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: `${DEFAULT_BASE_URL.replace(/\/+$/, "")}${SCREENSHOT_CONVERT_PATH}`,
        headers: {
          Authorization: `Bearer ${apiKey}`,
          "Content-Type": "application/json",
          "X-Sandbox": auth.sandbox ? "true" : "false"
        },
        body: { source: "<p>polydoc</p>", screenshot: { type: "png" } },
        responseType: "arraybuffer"
      }), { valid: !0 };
    } catch (error) {
      let status = (_a = error == null ? void 0 : error.response) == null ? void 0 : _a.status;
      return status === 401 ? { valid: !1, error: "Invalid PolyDoc API key." } : {
        valid: !1,
        error: `Could not validate the PolyDoc API key (${status ? `the API returned HTTP ${status}` : "the API was unreachable"}). Check the key and try again.`
      };
    }
  }
});

// ../ap/packages/pieces/community/polydoc/src/lib/common/build-request-body.ts
function isPlainObject(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}
function mergeDeep(target, source) {
  let out = __spreadValues({}, target);
  for (let [key, value] of Object.entries(source))
    isPlainObject(value) && isPlainObject(out[key]) ? out[key] = mergeDeep(out[key], value) : out[key] = value;
  return out;
}
function resolveSource(params) {
  var _a, _b, _c;
  switch (params.sourceType) {
    case "url":
      return (_a = params.url) != null ? _a : "";
    case "html":
      return (_b = params.html) != null ? _b : "";
    case "template":
      return `[template:${(_c = params.templateId) != null ? _c : ""}]`;
    default:
      return "";
  }
}
function buildLayout(opts) {
  var _a, _b, _c, _d;
  let layout = {};
  typeof opts.format == "string" && opts.format !== "" && (layout.format = opts.format);
  for (let flag of ["landscape", "printBackground", "outline", "tagged"])
    typeof opts[flag] == "boolean" && (layout[flag] = opts[flag]);
  return typeof opts.scale == "number" && (layout.scale = opts.scale), typeof opts.pageRanges == "string" && opts.pageRanges !== "" && (layout.pageRanges = opts.pageRanges), ["marginTop", "marginRight", "marginBottom", "marginLeft"].some((m) => opts[m] !== void 0 && opts[m] !== "") && (layout.margin = {
    top: (_a = opts.marginTop) != null ? _a : "2",
    right: (_b = opts.marginRight) != null ? _b : "1",
    bottom: (_c = opts.marginBottom) != null ? _c : "2",
    left: (_d = opts.marginLeft) != null ? _d : "1"
  }), Object.keys(layout).length > 0 ? layout : void 0;
}
function buildScreenshot(opts) {
  let shot = {};
  typeof opts.imageType == "string" && opts.imageType !== "" && (shot.type = opts.imageType), typeof opts.fullPage == "boolean" && (shot.fullPage = opts.fullPage), typeof opts.quality == "number" && (shot.quality = opts.quality), opts.encoding === "base64" && (shot.encoding = "base64");
  let hasViewportWidth = typeof opts.viewportWidth == "number", hasViewportHeight = typeof opts.viewportHeight == "number";
  if (hasViewportWidth !== hasViewportHeight)
    throw new Error("Both viewport width and height are required to set a custom viewport.");
  if (hasViewportWidth && hasViewportHeight) {
    let viewport = {
      width: opts.viewportWidth,
      height: opts.viewportHeight
    };
    typeof opts.devicePixelRatio == "number" && opts.devicePixelRatio > 0 && (viewport.devicePixelRatio = opts.devicePixelRatio), shot.viewport = viewport;
  }
  return Object.keys(shot).length > 0 ? shot : void 0;
}
function buildRequestBody(params) {
  var _a, _b;
  let endpoint = params.operation === "screenshot" ? SCREENSHOT_CONVERT_PATH : PDF_CONVERT_PATH, body = {
    source: resolveSource(params)
  };
  if (params.templateData && Object.keys(params.templateData).length > 0 && (body.templateData = params.templateData), params.filename && (body.filename = params.filename), params.tag && (body.tag = params.tag), typeof params.timeout == "number" && params.timeout > 0 && (body.timeout = params.timeout), params.operation === "pdf") {
    let layout = params.pdfOptions ? buildLayout(params.pdfOptions) : void 0;
    layout && (body.layout = layout);
  }
  if (params.operation === "screenshot") {
    let shot = params.screenshotOptions ? buildScreenshot(params.screenshotOptions) : void 0;
    shot && (body.screenshot = shot);
  }
  if (params.operation === "einvoice") {
    let eInvoice = {
      standard: params.eInvoiceStandard,
      profile: params.eInvoiceProfile,
      invoice: (_a = params.invoice) != null ? _a : {}
    };
    typeof params.eInvoiceVerify == "boolean" && (eInvoice.verify = params.eInvoiceVerify), body.eInvoice = eInvoice;
  }
  let delivery = (_b = params.delivery) != null ? _b : { mode: "download" }, isBinary = delivery.mode === "download";
  delivery.mode === "cloudStorage" && delivery.presignedUrl && (body.cloudStorage = {
    presignedUrl: delivery.presignedUrl
  }), delivery.mode === "webhook" && delivery.webhook && (body.webhook = delivery.webhook);
  let merged = params.advanced && Object.keys(params.advanced).length > 0 ? mergeDeep(body, params.advanced) : body;
  return {
    endpoint,
    body: merged,
    isBinary
  };
}
function defaultFilename(operation, imageType) {
  return operation === "screenshot" ? `screenshot.${imageType === "jpeg" ? "jpg" : imageType != null ? imageType : "png"}` : "document.pdf";
}

// ../ap/packages/pieces/community/polydoc/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
async function polyDocRequest(auth, request) {
  var _a;
  let baseUrl = DEFAULT_BASE_URL.replace(/\/+$/, ""), res = await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.POST,
    url: `${baseUrl}${request.endpoint}`,
    headers: {
      Authorization: `Bearer ${auth.props.apiKey}`,
      "Content-Type": "application/json",
      "X-Sandbox": auth.props.sandbox ? "true" : "false"
    },
    body: request.body,
    responseType: request.isBinary ? "arraybuffer" : "json"
  });
  return { status: res.status, headers: (_a = res.headers) != null ? _a : {}, body: res.body };
}
function extractApiErrorMessage(error) {
  var _a;
  let err = error, payload = (_a = err == null ? void 0 : err.response) == null ? void 0 : _a.body;
  if (payload instanceof ArrayBuffer ? payload = Buffer.from(payload).toString("utf8") : Buffer.isBuffer(payload) && (payload = payload.toString("utf8")), typeof payload == "string") {
    let text = payload;
    try {
      payload = JSON.parse(text);
    } catch (e) {
      return text || void 0;
    }
  }
  if (payload && typeof payload == "object" && !Array.isArray(payload)) {
    let p = payload, message = p.message;
    if (typeof message == "string")
      return message;
    let errorField = p.error;
    if (typeof errorField == "string")
      return errorField;
  }
}

// ../ap/packages/pieces/community/polydoc/src/lib/common/output.ts
async function shapeOutput(params) {
  var _a, _b;
  let { response, isBinary, files, operation, filename, imageType } = params;
  if (!isBinary)
    return (_a = response.body) != null ? _a : { success: !0 };
  let contentType = ((_b = headerValue(response.headers["content-type"])) != null ? _b : "application/octet-stream").split(";")[0].trim(), buffer = toBuffer(response.body), fileName = filename || defaultFilename(operation, imageType);
  return {
    success: !0,
    file: await files.write({ fileName, data: buffer }),
    fileName,
    contentType,
    sizeBytes: buffer.length,
    conversionId: headerValue(response.headers["x-conversion-id"]),
    creditUsed: headerValue(response.headers["x-credit-used"])
  };
}
function headerValue(value) {
  return Array.isArray(value) ? value[0] : value;
}
function toBuffer(body) {
  if (Buffer.isBuffer(body))
    return body;
  if (body instanceof ArrayBuffer)
    return Buffer.from(body);
  if (ArrayBuffer.isView(body))
    return Buffer.from(body.buffer, body.byteOffset, body.byteLength);
  if (typeof body == "string")
    return Buffer.from(body, "binary");
  throw new Error(
    "PolyDoc returned an unexpected response body for a binary delivery; expected file bytes."
  );
}

// ../ap/packages/pieces/community/polydoc/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var sourceTypeProp = (defaultValue = "url") => import_pieces_framework2.Property.StaticDropdown({
  displayName: "Source Type",
  description: "Where the content comes from",
  required: !0,
  defaultValue,
  options: {
    disabled: !1,
    options: SOURCE_TYPES.map((o) => ({ label: o.label, value: o.value }))
  }
}), sourceProp = import_pieces_framework2.Property.LongText({
  displayName: "Source",
  description: "The URL, inline HTML, or saved template ID, matching the Source Type above.",
  required: !0
}), templateDataProp = import_pieces_framework2.Property.Json({
  displayName: "Template Data",
  description: "Data passed to the Liquid template renderer (used only when Source Type is Template).",
  required: !1,
  defaultValue: {}
}), deliveryModeProp = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Delivery",
  description: "How the generated file is returned",
  required: !0,
  defaultValue: "download",
  options: {
    disabled: !1,
    options: DELIVERY_MODES.map((o) => ({ label: o.label, value: o.value }))
  }
}), presignedUrlProp = import_pieces_framework2.Property.ShortText({
  displayName: "Presigned URL",
  description: "HTTP PUT presigned URL from your storage provider (required for Cloud Storage delivery).",
  required: !1
}), webhookUrlProp = import_pieces_framework2.Property.ShortText({
  displayName: "Webhook URL",
  description: "URL the generated file is delivered to (required for Webhook delivery).",
  required: !1
}), webhookOptionsProp = import_pieces_framework2.Property.Json({
  displayName: "Webhook Options",
  description: "Extra webhook settings merged with the URL: async, method, headers, retries, retryDelay, timeout.",
  required: !1,
  defaultValue: {}
}), filenameProp = import_pieces_framework2.Property.ShortText({
  displayName: "Filename",
  description: "Output filename (overrides the default).",
  required: !1
}), tagProp = import_pieces_framework2.Property.ShortText({
  displayName: "Tag",
  description: "Label for logging and analytics (max 30 characters).",
  required: !1
}), timeoutProp = import_pieces_framework2.Property.Number({
  displayName: "Timeout (ms)",
  description: "Conversion timeout in milliseconds.",
  required: !1
}), advancedProp = import_pieces_framework2.Property.Json({
  displayName: "Advanced (JSON)",
  description: "Raw fields deep-merged into the request body for any API option not exposed above (e.g. pdf.watermark, pdf.pdfa, render, request).",
  required: !1,
  defaultValue: {}
});
function asJsonObject(value) {
  let candidate = value;
  if (typeof candidate == "string") {
    let trimmed = candidate.trim();
    if (trimmed === "")
      return;
    try {
      candidate = JSON.parse(trimmed);
    } catch (e) {
      return;
    }
  }
  if (candidate && typeof candidate == "object" && !Array.isArray(candidate)) {
    let obj = candidate;
    return Object.keys(obj).length > 0 ? obj : void 0;
  }
}
function resolveSourceParams(props) {
  var _a, _b;
  let sourceType = (_a = props.sourceType) != null ? _a : "url", source = (_b = props.source) != null ? _b : "";
  return sourceType === "html" ? { sourceType, html: source } : sourceType === "template" ? { sourceType, templateId: source, templateData: asJsonObject(props.templateData) } : { sourceType, url: source };
}
function resolveDelivery(props) {
  var _a, _b, _c, _d;
  let mode = (_a = props.deliveryMode) != null ? _a : "download";
  if (mode === "cloudStorage") {
    let presignedUrl = (_b = props.presignedUrl) != null ? _b : "";
    if (!presignedUrl)
      throw new Error("Cloud Storage delivery requires a Presigned URL.");
    return { mode, presignedUrl };
  }
  if (mode === "webhook") {
    let url = (_c = props.webhookUrl) != null ? _c : "";
    if (!url)
      throw new Error("Webhook delivery requires a Webhook URL.");
    let extra = (_d = asJsonObject(props.webhookOptions)) != null ? _d : {};
    return { mode, webhook: __spreadValues({ url }, extra) };
  }
  return { mode: "download" };
}
function resolveMetadata(props) {
  return {
    filename: props.filename || void 0,
    tag: props.tag || void 0,
    timeout: typeof props.timeout == "number" ? props.timeout : void 0,
    advanced: asJsonObject(props.advanced)
  };
}

// ../ap/packages/pieces/community/polydoc/src/lib/actions/capture-screenshot.ts
var captureScreenshot = (0, import_pieces_framework3.createAction)({
  auth: polydocAuth,
  name: "capture_screenshot",
  displayName: "Capture Screenshot",
  description: "Capture a screenshot of a URL, inline HTML, or a saved template.",
  aiMetadata: {
    description: "Captures a screenshot (PNG, JPEG, or WebP) of a URL, an inline HTML string, or a saved PolyDoc template. Supports full-page capture, custom viewport, and base64 or file output. Not idempotent: each call performs a fresh capture on PolyDoc that produces a new file and consumes account credits, and a URL capture reflects the page as it is at capture time.",
    idempotent: !1
  },
  props: {
    sourceType: sourceTypeProp("url"),
    source: sourceProp,
    templateData: templateDataProp,
    imageType: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Image Type",
      required: !1,
      defaultValue: "png",
      options: { disabled: !1, options: IMAGE_TYPES.map((t) => ({ label: t, value: t })) }
    }),
    fullPage: import_pieces_framework3.Property.Checkbox({
      displayName: "Full Page",
      description: "Capture the entire scrollable page.",
      required: !1,
      defaultValue: !1
    }),
    quality: import_pieces_framework3.Property.Number({
      displayName: "Quality",
      description: "Compression quality for JPEG/WebP (0 to 100).",
      required: !1
    }),
    viewportWidth: import_pieces_framework3.Property.Number({
      displayName: "Viewport Width",
      description: "Viewport width in CSS pixels.",
      required: !1
    }),
    viewportHeight: import_pieces_framework3.Property.Number({
      displayName: "Viewport Height",
      description: "Viewport height in CSS pixels.",
      required: !1
    }),
    devicePixelRatio: import_pieces_framework3.Property.Number({
      displayName: "Device Pixel Ratio",
      description: "Device pixel ratio, e.g. 2 for retina (0 to 10).",
      required: !1
    }),
    outputEncoding: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Output",
      description: "Return the image as a file (default) or as a base64 string.",
      required: !1,
      defaultValue: "binaryFile",
      options: {
        disabled: !1,
        options: SCREENSHOT_OUTPUT_ENCODINGS.map((o) => ({ label: o.label, value: o.value }))
      }
    }),
    deliveryMode: deliveryModeProp,
    presignedUrl: presignedUrlProp,
    webhookUrl: webhookUrlProp,
    webhookOptions: webhookOptionsProp,
    filename: filenameProp,
    tag: tagProp,
    timeout: timeoutProp,
    advanced: advancedProp
  },
  async run(context) {
    var _a;
    let props = context.propsValue, base64 = props.outputEncoding === "base64", params = __spreadProps(__spreadValues(__spreadValues({
      operation: "screenshot"
    }, resolveSourceParams(props)), resolveMetadata(props)), {
      delivery: resolveDelivery(props),
      screenshotOptions: {
        imageType: props.imageType,
        fullPage: props.fullPage,
        quality: props.quality,
        encoding: base64 ? "base64" : void 0,
        viewportWidth: props.viewportWidth,
        viewportHeight: props.viewportHeight,
        devicePixelRatio: props.devicePixelRatio
      }
    }), request = buildRequestBody(params);
    base64 && (request.isBinary = !1);
    try {
      let response = await polyDocRequest(context.auth, request);
      return await shapeOutput({
        response,
        isBinary: request.isBinary,
        files: context.files,
        operation: "screenshot",
        filename: params.filename,
        imageType: props.imageType
      });
    } catch (error) {
      throw new Error((_a = extractApiErrorMessage(error)) != null ? _a : error.message);
    }
  }
});

// ../ap/packages/pieces/community/polydoc/src/lib/actions/convert-pdf.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var convertToPdf = (0, import_pieces_framework4.createAction)({
  auth: polydocAuth,
  name: "convert_to_pdf",
  displayName: "Convert to PDF",
  description: "Convert HTML, a URL, or a saved template to a PDF.",
  aiMetadata: {
    description: "Converts a URL, an inline HTML string, or a saved PolyDoc template into a PDF and returns the file (or delivers it to cloud storage / a webhook). Use it to render documents like invoices, reports, or contracts. Not idempotent: each call performs a fresh conversion on PolyDoc that produces a new file and consumes account credits, and the cloud-storage and webhook delivery modes send the file to an external destination.",
    idempotent: !1
  },
  props: {
    sourceType: sourceTypeProp("url"),
    source: sourceProp,
    templateData: templateDataProp,
    format: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Page Format",
      required: !1,
      defaultValue: "A4",
      options: { disabled: !1, options: PAGE_FORMATS.map((f) => ({ label: f, value: f })) }
    }),
    landscape: import_pieces_framework4.Property.Checkbox({ displayName: "Landscape", required: !1, defaultValue: !1 }),
    printBackground: import_pieces_framework4.Property.Checkbox({
      displayName: "Print Background",
      description: "Print background graphics and colors.",
      required: !1,
      defaultValue: !0
    }),
    scale: import_pieces_framework4.Property.Number({
      displayName: "Scale",
      description: "Render scale (0.1 to 2).",
      required: !1
    }),
    pageRanges: import_pieces_framework4.Property.ShortText({
      displayName: "Page Ranges",
      description: 'Pages to include, e.g. "1-5, 8". Empty means all pages.',
      required: !1
    }),
    outline: import_pieces_framework4.Property.Checkbox({
      displayName: "Outline (Bookmarks)",
      description: "Generate PDF bookmarks from HTML headings.",
      required: !1,
      defaultValue: !1
    }),
    tagged: import_pieces_framework4.Property.Checkbox({
      displayName: "Tagged (Accessible)",
      description: "Produce a tagged, accessible PDF.",
      required: !1,
      defaultValue: !1
    }),
    marginTop: import_pieces_framework4.Property.ShortText({
      displayName: "Margin Top",
      description: "Top margin with optional unit, e.g. 10mm, 1cm, 0.5in.",
      required: !1
    }),
    marginRight: import_pieces_framework4.Property.ShortText({ displayName: "Margin Right", required: !1 }),
    marginBottom: import_pieces_framework4.Property.ShortText({ displayName: "Margin Bottom", required: !1 }),
    marginLeft: import_pieces_framework4.Property.ShortText({ displayName: "Margin Left", required: !1 }),
    deliveryMode: deliveryModeProp,
    presignedUrl: presignedUrlProp,
    webhookUrl: webhookUrlProp,
    webhookOptions: webhookOptionsProp,
    filename: filenameProp,
    tag: tagProp,
    timeout: timeoutProp,
    advanced: advancedProp
  },
  async run(context) {
    var _a;
    let props = context.propsValue, params = __spreadProps(__spreadValues(__spreadValues({
      operation: "pdf"
    }, resolveSourceParams(props)), resolveMetadata(props)), {
      delivery: resolveDelivery(props),
      pdfOptions: {
        format: props.format,
        landscape: props.landscape,
        printBackground: props.printBackground,
        scale: props.scale,
        pageRanges: props.pageRanges,
        outline: props.outline,
        tagged: props.tagged,
        marginTop: props.marginTop,
        marginRight: props.marginRight,
        marginBottom: props.marginBottom,
        marginLeft: props.marginLeft
      }
    }), request = buildRequestBody(params);
    try {
      let response = await polyDocRequest(context.auth, request);
      return await shapeOutput({
        response,
        isBinary: request.isBinary,
        files: context.files,
        operation: "pdf",
        filename: params.filename
      });
    } catch (error) {
      throw new Error((_a = extractApiErrorMessage(error)) != null ? _a : error.message);
    }
  }
});

// ../ap/packages/pieces/community/polydoc/src/lib/actions/generate-einvoice.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var SAMPLE_INVOICE = {
  number: "INV-001",
  issueDate: "2026-01-31",
  dueDate: "2026-03-02",
  currencyCode: "EUR",
  seller: {
    name: "Your Company GmbH",
    address: { line1: "Main St 1", city: "Berlin", postalCode: "10115", countryCode: "DE" },
    taxId: "DE123456789"
  },
  buyer: {
    name: "Customer SARL",
    address: { line1: "Rue 2", city: "Paris", postalCode: "75001", countryCode: "FR" }
  },
  lines: [
    { description: "Widget", quantity: 2, unitPrice: 10, lineTotal: 20, vatRate: 19, vatCategoryCode: "S" }
  ],
  taxSummary: [{ categoryCode: "S", rate: 19, taxableAmount: 20, taxAmount: 3.8 }],
  paymentTerms: "Net 30 days",
  totalNetAmount: 20,
  totalTaxAmount: 3.8,
  totalGrossAmount: 23.8
}, generateEinvoice = (0, import_pieces_framework5.createAction)({
  auth: polydocAuth,
  name: "generate_einvoice",
  displayName: "Generate E-Invoice",
  description: "Generate a hybrid e-invoice PDF (Factur-X / ZUGFeRD, EN 16931) from a visual layout plus structured invoice data.",
  aiMetadata: {
    description: "Generates a hybrid e-invoice PDF (Factur-X / ZUGFeRD, EN 16931) by embedding structured invoice XML into a visual PDF layout. EN 16931 requires payment terms or a due date, a seller tax ID for VAT category S, and consistent totals. Not idempotent: each call performs a fresh generation on PolyDoc that produces a new file and consumes account credits, and the cloud-storage and webhook delivery modes send the file to an external destination.",
    idempotent: !1
  },
  props: {
    sourceType: sourceTypeProp("html"),
    source: sourceProp,
    templateData: templateDataProp,
    standard: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Standard",
      description: "The hybrid e-invoice standard to embed.",
      required: !0,
      defaultValue: "zugferd",
      options: {
        disabled: !1,
        options: EINVOICE_STANDARDS.map((o) => ({ label: o.label, value: o.value }))
      }
    }),
    profile: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Profile",
      description: "The data granularity profile to validate against.",
      required: !0,
      defaultValue: "en16931",
      options: {
        disabled: !1,
        options: EINVOICE_PROFILES.map((p) => ({ label: p, value: p }))
      }
    }),
    invoice: import_pieces_framework5.Property.Json({
      displayName: "Invoice Data",
      description: "Structured invoice data: number, dates, currency, seller, buyer, lines, totals. EN 16931 needs payment terms (or due date), seller taxId for VAT category S, and consistent totals. See docs.polydoc.tech.",
      required: !0,
      defaultValue: SAMPLE_INVOICE
    }),
    verify: import_pieces_framework5.Property.Checkbox({
      displayName: "Verify",
      description: "Validate PDF/A and e-invoice compliance (returns an error if it fails).",
      required: !1,
      defaultValue: !1
    }),
    deliveryMode: deliveryModeProp,
    presignedUrl: presignedUrlProp,
    webhookUrl: webhookUrlProp,
    webhookOptions: webhookOptionsProp,
    filename: filenameProp,
    tag: tagProp,
    timeout: timeoutProp,
    advanced: advancedProp
  },
  async run(context) {
    var _a, _b;
    let props = context.propsValue, params = __spreadProps(__spreadValues(__spreadValues({
      operation: "einvoice"
    }, resolveSourceParams(props)), resolveMetadata(props)), {
      delivery: resolveDelivery(props),
      eInvoiceStandard: props.standard,
      eInvoiceProfile: props.profile,
      eInvoiceVerify: props.verify === !0,
      invoice: (_a = asJsonObject(props.invoice)) != null ? _a : {}
    }), request = buildRequestBody(params);
    try {
      let response = await polyDocRequest(context.auth, request);
      return await shapeOutput({
        response,
        isBinary: request.isBinary,
        files: context.files,
        operation: "einvoice",
        filename: params.filename
      });
    } catch (error) {
      throw new Error((_b = extractApiErrorMessage(error)) != null ? _b : error.message);
    }
  }
});

// ../ap/packages/pieces/community/polydoc/src/index.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), polydoc = (0, import_pieces_framework6.createPiece)({
  displayName: "PolyDoc",
  description: "Convert HTML or a URL to PDF, capture screenshots, and generate EU e-invoices (Factur-X / ZUGFeRD).",
  auth: polydocAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/polydoc.png",
  categories: [import_pieces_framework7.PieceCategory.CONTENT_AND_FILES, import_pieces_framework7.PieceCategory.PRODUCTIVITY],
  authors: ["polydoc-tech", "sanket-a11y"],
  actions: [
    convertToPdf,
    captureScreenshot,
    generateEinvoice,
    (0, import_pieces_common3.createCustomApiCallAction)({
      baseUrl: () => "https://api.podio.com",
      auth: polydocAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.apikey}`,
        "X-Sandbox": auth.props.sandbox ? "true" : "false"
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  polydoc
});
