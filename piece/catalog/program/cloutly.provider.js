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

// ../ap/packages/pieces/community/cloutly/src/index.ts
var index_exports = {};
__export(index_exports, {
  cloutly: () => cloutly
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/cloutly/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), cloutlyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Please enter the API Key obtained from Cloutly."
});

// ../ap/packages/pieces/community/cloutly/src/lib/actions/send-review-invite.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), sendReviewInvite = (0, import_pieces_framework2.createAction)({
  auth: cloutlyAuth,
  name: "sendReviewInvite",
  displayName: "Send Review Invite",
  description: "Sends a review invite to your customer.",
  audience: "both",
  aiMetadata: { description: "Sends a Cloutly review-invitation to a customer via email and/or SMS, scoped to a specific business and review campaign. Use to solicit a review from a customer after a purchase or interaction. Requires businessId and campaignId, plus at least one of email or phone number to deliver the invite; an optional delay defers sending by N days. Not idempotent \u2014 each call dispatches a new invite.", idempotent: !1 },
  props: {
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      required: !0
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "The email of the customer to send the invite to (required if Phone Number is empty)",
      required: !1
    }),
    phoneNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Number",
      description: "The phone number of the customer to send the invite to (required if Email is empty)",
      required: !1
    }),
    sourceCustomerId: import_pieces_framework2.Property.ShortText({
      displayName: "Source Customer ID",
      required: !1
    }),
    businessId: import_pieces_framework2.Property.ShortText({
      displayName: "Business ID",
      required: !0
    }),
    campaignId: import_pieces_framework2.Property.ShortText({
      displayName: "Campaign ID",
      required: !0
    }),
    inviteDelayDays: import_pieces_framework2.Property.Number({
      displayName: "Invite Delay Days",
      description: "The number of days to delay the invite (i.e send after X days)",
      required: !1
    }),
    salesRepEmail: import_pieces_framework2.Property.ShortText({
      displayName: "Sales Rep Email",
      description: "The email of the sales rep to associate the review and customer",
      required: !1
    })
  },
  async run(context) {
    let data = {
      firstName: context.propsValue.firstName,
      lastName: context.propsValue.lastName,
      channel: {
        email: context.propsValue.email,
        phoneNumber: context.propsValue.phoneNumber
      },
      source: "api",
      sourceCustomerId: context.propsValue.sourceCustomerId,
      businessId: context.propsValue.businessId,
      campaignId: context.propsValue.campaignId,
      inviteDelayDays: context.propsValue.inviteDelayDays,
      salesRepEmail: context.propsValue.salesRepEmail
    }, apiKey = context.auth.secret_text;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://app.cloutly.com/api/v1/send-review-invite",
      body: data,
      headers: {
        "Content-Type": "application/json",
        "x-app": "activepieces",
        "x-api-key": apiKey
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/cloutly/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var cloutly = (0, import_pieces_framework3.createPiece)({
  displayName: "Cloutly",
  description: "Review Management Tool",
  auth: cloutlyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/cloutly.svg",
  categories: [import_pieces_framework4.PieceCategory.MARKETING],
  authors: ["joshuaheslin"],
  actions: [
    sendReviewInvite,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://app.cloutly.com/api/v1",
      auth: cloutlyAuth,
      authMapping: async (auth) => ({
        "x-app": "activepieces",
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  cloutly
});
