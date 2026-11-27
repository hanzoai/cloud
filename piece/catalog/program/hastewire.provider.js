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

// ../ap/packages/pieces/community/hastewire/src/index.ts
var index_exports = {};
__export(index_exports, {
  hastewire: () => hastewire
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/hastewire/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), hastewireAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "You can obtain API key from [Settings](https://hastewire.com/humanizer/account/api-keys)."
});

// ../ap/packages/pieces/community/hastewire/src/index.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/hastewire/src/lib/actions/detect-text.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), detectTextAction = (0, import_pieces_framework2.createAction)({
  name: "detect-text",
  auth: hastewireAuth,
  displayName: "Detect Text",
  description: "Analyzes text and returns the likelihood of it being AI-generated or human-written.",
  audience: "both",
  aiMetadata: { description: "Runs AI-detection on a block of text via the Hastewire API, scoring how likely it is to be AI-generated versus human-written. Choose this to classify or vet content before acting on it; it only reads/analyzes the supplied text and does not modify or store it, so repeating the same input yields the same score.", idempotent: !0 },
  props: {
    text: import_pieces_framework2.Property.LongText({
      displayName: "Input Text",
      required: !0
    })
  },
  async run(context) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://hastewire.com/api/v1/detect",
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        text: context.propsValue.text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/hastewire/src/lib/actions/humanize-text.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), humanizeTextAction = (0, import_pieces_framework3.createAction)({
  name: "humanize-text",
  auth: hastewireAuth,
  displayName: "Humanize Text",
  description: "Rephrases a given AI-generated text to be more human like.",
  audience: "both",
  aiMetadata: { description: "Rewrites AI-generated text into a more human-sounding version via the Hastewire API, optionally targeting a writing style (formal, standard, or academic). Choose this to make machine-written content read naturally or to reduce AI-detection signals. Each call performs a fresh generative rewrite, so it is not idempotent and repeated calls may return different output.", idempotent: !1 },
  props: {
    text: import_pieces_framework3.Property.LongText({
      displayName: "Input Text",
      required: !0
    }),
    style: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Text Style",
      required: !1,
      description: "The desired writing style for the output.",
      options: {
        disabled: !1,
        options: [
          { label: "formal", value: "formal" },
          { label: "standard", value: "standard" },
          { label: "academic", value: "academic" }
        ]
      }
    })
  },
  async run(context) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://hastewire.com/api/v1/humanize",
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        text: context.propsValue.text,
        style: context.propsValue.style
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/hastewire/src/index.ts
var hastewire = (0, import_pieces_framework4.createPiece)({
  displayName: "Hastewire",
  auth: hastewireAuth,
  categories: [import_pieces_framework5.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/hastewire.png",
  authors: ["kishanprmr"],
  actions: [detectTextAction, humanizeTextAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  hastewire
});
