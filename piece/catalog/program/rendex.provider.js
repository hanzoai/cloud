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

// ../ap/packages/pieces/community/rendex/src/index.ts
var index_exports = {};
__export(index_exports, {
  rendex: () => rendex,
  rendexAuth: () => rendexAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/rendex/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/rendex/src/lib/common/common.ts
var RENDEX_BASE_URL = "https://api.rendex.dev";

// ../ap/packages/pieces/community/rendex/src/lib/common/auth.ts
var rendexAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your Rendex API key. Create one in your dashboard at rendex.dev under API Keys (the free tier includes 500 renders per month).",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${RENDEX_BASE_URL}/v1/credential-check`,
        headers: {
          Authorization: `Bearer ${auth}`
        }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API key" };
    }
  }
});

// ../ap/packages/pieces/community/rendex/src/lib/actions/render-to-image.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var renderToImage = (0, import_pieces_framework2.createAction)({
  auth: rendexAuth,
  name: "render_to_image",
  displayName: "Render to Image",
  description: "Render raw HTML, a URL, or Markdown to an image (PNG, JPEG, WebP) or a PDF.",
  audience: "both",
  aiMetadata: { description: "Renders source content into a downloadable image (PNG/JPEG/WebP) or PDF via the Rendex screenshot API, returning the rendered file. The Source mode determines what the content input means: raw HTML markup, a live page URL to capture, or Markdown. Use to turn HTML/Markdown or a webpage into a captured image or PDF; optionally capture the full scrollable page instead of just the viewport. Not idempotent: each call performs a fresh remote render that produces a new file, and in URL mode the capture reflects the live page at request time.", idempotent: !1 },
  props: {
    source_type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Source",
      description: "Render from raw HTML, a live URL, or Markdown.",
      required: !0,
      defaultValue: "html",
      options: {
        options: [
          { label: "HTML", value: "html" },
          { label: "URL", value: "url" },
          { label: "Markdown", value: "markdown" }
        ]
      }
    }),
    content: import_pieces_framework2.Property.DynamicProperties({
      auth: rendexAuth,
      displayName: "Content",
      description: "The content to render, based on the Source you selected.",
      required: !0,
      refreshers: ["source_type"],
      props: async ({ source_type }) => source_type === "url" ? {
        url: import_pieces_framework2.Property.ShortText({
          displayName: "URL",
          description: "The page URL to render.",
          required: !0
        })
      } : source_type === "markdown" ? {
        markdown: import_pieces_framework2.Property.LongText({
          displayName: "Markdown",
          description: "The Markdown to render.",
          required: !0
        })
      } : {
        html: import_pieces_framework2.Property.LongText({
          displayName: "HTML",
          description: "The HTML markup to render.",
          required: !0
        })
      }
    }),
    format: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Format",
      required: !1,
      defaultValue: "png",
      options: {
        options: [
          { label: "PNG", value: "png" },
          { label: "JPEG", value: "jpeg" },
          { label: "WebP", value: "webp" },
          { label: "PDF", value: "pdf" }
        ]
      }
    }),
    full_page: import_pieces_framework2.Property.Checkbox({
      displayName: "Full Page",
      description: "Capture the full scrollable page instead of just the viewport.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { auth, propsValue, files } = context, { source_type, content, format, full_page } = propsValue, sourceValue = content[source_type];
    if (!sourceValue)
      throw new Error("Please provide content for the selected source.");
    let body = {
      [source_type]: sourceValue,
      format: format != null ? format : "png",
      fullPage: full_page != null ? full_page : !1
    }, _a = (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${RENDEX_BASE_URL}/v1/screenshot/json`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body
    })).body.data, { image } = _a, metadata = __objRest(_a, ["image"]), file = await files.write({
      fileName: `rendex-capture.${metadata.format}`,
      data: Buffer.from(image, "base64")
    });
    return __spreadValues({ file }, metadata);
  }
});

// ../ap/packages/pieces/community/rendex/src/index.ts
var rendex = (0, import_pieces_framework3.createPiece)({
  displayName: "Rendex",
  description: "Render raw HTML, a URL, or Markdown to a PNG, JPEG, or WebP image or a PDF via the Rendex API.",
  auth: rendexAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/rendex.png",
  categories: [import_pieces_framework4.PieceCategory.CONTENT_AND_FILES, import_pieces_framework4.PieceCategory.DEVELOPER_TOOLS],
  authors: ["Dan425953"],
  actions: [
    renderToImage,
    (0, import_pieces_common3.createCustomApiCallAction)({
      baseUrl: () => RENDEX_BASE_URL,
      auth: rendexAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  rendex,
  rendexAuth
});
