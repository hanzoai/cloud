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

// ../ap/packages/pieces/community/millionverifier/src/index.ts
var index_exports = {};
__export(index_exports, {
  millionverifier: () => millionverifier
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/millionverifier/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), millionVerifierAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "MillionVerifier API Key",
  description: `API Key for MillionVerifier.

API key you can find on this page: https://app.millionverifier.com/api

**Test API key for development:** \`API_KEY_FOR_TEST\`. This API key will return random results.`,
  required: !0
});

// ../ap/packages/pieces/community/millionverifier/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/millionverifier/src/lib/actions/verify-email.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), verifyEmail = (0, import_pieces_framework2.createAction)({
  auth: millionVerifierAuth,
  name: "verifyEmail",
  displayName: "Verify Email",
  description: "Verify email address using Million Verifier API",
  audience: "both",
  aiMetadata: { description: "Validates a single email address against the MillionVerifier real-time API, returning its deliverability status (e.g. deliverable, undeliverable, risky). Use to check whether an email is valid and safe to send to before adding it to a list or sending mail. Requires one email address; the call is read-only and idempotent.", idempotent: !0 },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email Address",
      description: "The email address to verify",
      required: !0
    }),
    timeout: import_pieces_framework2.Property.Number({
      displayName: "Timeout",
      description: "Timeout in seconds (default: 10)",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    let { email, timeout = 10 } = context.propsValue, apiKey = context.auth.secret_text, url = `https://millionverifier.com/api/v3/?${new URLSearchParams({
      api: apiKey,
      email,
      timeout: timeout.toString()
    }).toString()}`;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url
    })).body;
  }
});

// ../ap/packages/pieces/community/millionverifier/src/index.ts
var millionverifier = (0, import_pieces_framework3.createPiece)({
  displayName: "MillionVerifier",
  auth: millionVerifierAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/millionverifier.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework4.PieceCategory.COMMUNICATION],
  description: "MillionVerifier is an email verifier service and API",
  actions: [verifyEmail],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  millionverifier
});
