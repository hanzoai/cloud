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

// ../ap/packages/pieces/community/mailercheck/src/index.ts
var index_exports = {};
__export(index_exports, {
  mailercheck: () => mailercheck
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mailercheck/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), mailercheckAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Mailercheck API Key",
  required: !0
});

// ../ap/packages/pieces/community/mailercheck/src/lib/actions/verify-an-email-address.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), verifyAnEmailAddress = (0, import_pieces_framework2.createAction)({
  auth: mailercheckAuth,
  name: "verifyAnEmailAddress",
  displayName: "Verify an Email Address",
  description: "Verify a single email address in real-time",
  audience: "both",
  aiMetadata: { description: "Validates one email address in real time against MailerCheck and reports its deliverability status (e.g. valid, invalid, risky). Use to check a single address before sending or storing it; pass exactly one email. Read-only lookup that does not change anything, so repeating it for the same address is safe.", idempotent: !0 },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email Address",
      description: "The email address to verify",
      required: !0
    })
  },
  async run(context) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://app.mailercheck.com/api/check/single",
      body: {
        email: context.propsValue.email
      },
      headers: {
        Authorization: `Bearer ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/mailercheck/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), mailercheck = (0, import_pieces_framework3.createPiece)({
  displayName: "Mailercheck",
  auth: mailercheckAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/mailercheck.png",
  categories: [import_pieces_framework4.PieceCategory.SALES_AND_CRM],
  description: "MailerCheck is an easy-to-use email and campaign analysis tool. Anyone using an email service provider can keep their email lists clean and their campaigns deliverable.",
  authors: ["sanket-a11y"],
  actions: [
    verifyAnEmailAddress,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: mailercheckAuth,
      baseUrl: () => "https://app.mailercheck.com/api",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mailercheck
});
