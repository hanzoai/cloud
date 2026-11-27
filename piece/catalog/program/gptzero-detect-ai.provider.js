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

// ../ap/packages/pieces/community/gptzero-detect-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  gptzeroDetectAi: () => gptzeroDetectAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/gptzero-detect-ai/src/lib/actions/scan-file.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/gptzero-detect-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), gptzeroDetectAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "GPTZero API Key",
  description: "https://app.gptzero.me/app/api to get your API key",
  required: !0
});

// ../ap/packages/pieces/community/gptzero-detect-ai/src/lib/actions/scan-file.ts
var scanFile = (0, import_pieces_framework2.createAction)({
  auth: gptzeroDetectAiAuth,
  name: "scanFile",
  displayName: "Scan File",
  description: "Scan a file for AI-generated content detection",
  audience: "both",
  aiMetadata: {
    description: "Submits a document file to GPTZero to estimate how likely its text was AI-generated, returning per-document and per-sentence detection scores. Choose this over Scan Text when the content lives in a file (the file must be passed as a File input, not a path or URL). Read-only analysis: re-running on the same file is safe and yields the same result.",
    idempotent: !0
  },
  props: {
    file: import_pieces_framework2.Property.File({
      displayName: "File",
      description: "The file to scan for AI-generated content",
      required: !0
    })
  },
  async run(context) {
    let file = context.propsValue.file, formData = new FormData(), fileBuffer = Buffer.from(file.base64, "base64"), mimeType = file.extension ? `application/${file.extension}` : "application/octet-stream", blob = new Blob([fileBuffer], { type: mimeType });
    return formData.append("files", blob, file.filename), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.gptzero.me/v2/predict/files",
      headers: {
        "x-api-key": context.auth.secret_text
      },
      body: formData
    })).body;
  }
});

// ../ap/packages/pieces/community/gptzero-detect-ai/src/lib/actions/scan-text.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var scanText = (0, import_pieces_framework3.createAction)({
  auth: gptzeroDetectAiAuth,
  name: "scanText",
  displayName: "Scan Text",
  description: "Scan text content for AI-generated content detection",
  audience: "both",
  aiMetadata: {
    description: "Submits a raw text string to GPTZero to estimate how likely it was AI-generated, returning per-document and per-sentence detection scores. Choose this when you already have the text inline; use Scan File instead when the content is in a file. Read-only analysis: re-running with the same text is safe and yields the same result.",
    idempotent: !0
  },
  props: {
    text: import_pieces_framework3.Property.LongText({
      displayName: "Text",
      description: "The text content to scan for AI-generated content",
      required: !0
    })
  },
  async run(context) {
    let payload = {
      document: context.propsValue.text
    };
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.gptzero.me/v2/predict/text",
      headers: {
        "x-api-key": context.auth.secret_text,
        "content-type": "application/json"
      },
      body: payload
    })).body;
  }
});

// ../ap/packages/pieces/community/gptzero-detect-ai/src/index.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var gptzeroDetectAi = (0, import_pieces_framework4.createPiece)({
  displayName: "GPTZero",
  auth: gptzeroDetectAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/gptzero-detect-ai.png",
  authors: ["sanket-a11y"],
  description: "Detect AI-generated text with GPTZero API",
  categories: [import_pieces_framework5.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  actions: [scanFile, scanText],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  gptzeroDetectAi
});
