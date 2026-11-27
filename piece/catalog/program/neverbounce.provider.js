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

// ../ap/packages/pieces/community/neverbounce/src/index.ts
var index_exports = {};
__export(index_exports, {
  neverbounce: () => neverbounce
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/neverbounce/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), neverbounceAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your Neverbounce API Key. Get it from https://neverbounce.com/",
  required: !0
});

// ../ap/packages/pieces/community/neverbounce/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/neverbounce/src/lib/actions/verify-email-address.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var verifyEmailAddress = (0, import_pieces_framework2.createAction)({
  auth: neverbounceAuth,
  name: "verifyEmailAddress",
  displayName: "Verify Email Address",
  description: "Verify a single email address using NeverBounce API",
  audience: "both",
  aiMetadata: { description: "Checks the deliverability of a single email address via NeverBounce, returning a verification result (e.g. valid, invalid, disposable, catch-all) so an agent can decide whether to keep, flag, or discard the address before sending. Use for one-off real-time validation of a single recipient; for large lists use a bulk/list-verification flow instead. Requires the email address as input. Read-only and idempotent: re-running with the same address does not change anything.", idempotent: !0 },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email Address",
      description: "The email address to verify",
      required: !0
    })
  },
  async run(context) {
    let queryParams = {
      key: context.auth.secret_text,
      email: context.propsValue.email
    };
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: "https://api.neverbounce.com/v4.2/single/check",
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/neverbounce/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), neverbounce = (0, import_pieces_framework3.createPiece)({
  displayName: "NeverBounce",
  auth: neverbounceAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/neverbounce.png",
  categories: [import_pieces_framework4.PieceCategory.COMMUNICATION],
  description: "NeverBounce is an email verification service that improves deliverability and helps businesses adhere to strict deliverability guidelines.",
  authors: ["sanket-a11y"],
  actions: [
    verifyEmailAddress,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: neverbounceAuth,
      baseUrl: () => "https://api.neverbounce.com/v4.2",
      authLocation: "queryParams",
      authMapping: async (auth) => ({
        key: auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  neverbounce
});
