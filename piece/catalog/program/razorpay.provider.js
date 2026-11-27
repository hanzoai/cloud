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

// ../ap/packages/pieces/community/razorpay/src/index.ts
var index_exports = {};
__export(index_exports, {
  razorpay: () => razorpay,
  razorpayAuth: () => razorpayAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/razorpay/src/lib/common/utils.ts
var generateRazorpayAuthHeader = (credentials) => {
  let { keyID, keySecret } = credentials, encodedCredentials = Buffer.from(`${keyID}:${keySecret}`).toString("base64");
  return Promise.resolve({
    Authorization: `Basic ${encodedCredentials}`
  });
}, razorpayURL = {
  apiURL: "https://api.razorpay.com/v1/"
};

// ../ap/packages/pieces/community/razorpay/src/lib/actions/create-payment-link.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common");
var createPaymentlink = (0, import_pieces_framework.createAction)({
  name: "create-payment-link",
  auth: razorpayAuth,
  displayName: "Create Payment Link",
  description: "Create a payment link",
  audience: "both",
  aiMetadata: { description: "Creates a new Razorpay payment link for a given amount and currency, optionally notifying the customer by SMS and/or email. Use to collect a one-off payment from a customer; amount is specified in major currency units (e.g. rupees) and is converted to the smallest unit internally. Not idempotent \u2014 each call generates a new payment link.", idempotent: !1 },
  props: {
    amount: import_pieces_framework.Property.Number({
      displayName: "Amount",
      required: !0
    }),
    currency: import_pieces_framework.Property.ShortText({
      displayName: "Currency",
      required: !0,
      defaultValue: "INR"
    }),
    reference_id: import_pieces_framework.Property.ShortText({
      displayName: "Reference ID",
      required: !1
    }),
    description: import_pieces_framework.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    customer_name: import_pieces_framework.Property.ShortText({
      displayName: "Customer Name",
      required: !1
    }),
    customer_contact: import_pieces_framework.Property.ShortText({
      displayName: "Customer Contact",
      required: !0,
      defaultValue: "+91"
    }),
    notify_sms: import_pieces_framework.Property.Checkbox({
      displayName: "Notify via SMS",
      description: "Send notification via SMS",
      required: !1,
      defaultValue: !0
    }),
    customer_email: import_pieces_framework.Property.ShortText({
      displayName: "Customer Email",
      required: !1
    }),
    notify_email: import_pieces_framework.Property.Checkbox({
      displayName: "Notify via Email",
      description: "Send notification via Email",
      required: !1,
      defaultValue: !0
    }),
    metafield_notes: import_pieces_framework.Property.ShortText({
      displayName: "Notes",
      required: !1
    }),
    callback_url: import_pieces_framework.Property.ShortText({
      displayName: "Callback URL",
      required: !1
    }),
    callback_method: import_pieces_framework.Property.ShortText({
      displayName: "Callback Method",
      required: !1
    })
  },
  async run(context) {
    let paymentLinkData = {
      amount: Math.round(context.propsValue.amount * 100),
      currency: context.propsValue.currency,
      reference_id: context.propsValue.reference_id,
      description: context.propsValue.description,
      customer: {
        name: context.propsValue.customer_name,
        contact: context.propsValue.customer_contact,
        email: context.propsValue.customer_email
      },
      notify: {
        sms: context.propsValue.notify_sms,
        email: context.propsValue.notify_email
      },
      notes: {
        policy_name: context.propsValue.metafield_notes
      },
      callback_url: context.propsValue.callback_url,
      callback_method: context.propsValue.callback_method
    }, authHeader = await generateRazorpayAuthHeader(context.auth.props);
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.razorpay.com/v1/payment_links",
      headers: __spreadProps(__spreadValues({}, authHeader), {
        "Content-Type": "application/json"
      }),
      body: JSON.stringify(paymentLinkData)
    })).body;
  }
});

// ../ap/packages/pieces/community/razorpay/src/index.ts
var razorpayAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  description: `
          Enter your Key ID and Key Secret

          Login to your Dashboard with appropriate credentials.
          Select the mode (Test or Live) for which you want to generate the API key
          Navigate to Settings > API Keys > Generate Key to generate keys for the selected mode.

          The Key ID and Key Secret appear in a pop-out window.
        `,
  required: !0,
  props: {
    keyID: import_pieces_framework2.Property.ShortText({
      displayName: "Key ID",
      required: !0
    }),
    keySecret: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "Key Secret",
      required: !0
    })
  }
}), razorpay = (0, import_pieces_framework2.createPiece)({
  displayName: "Razorpay",
  auth: razorpayAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/razorpay.png",
  authors: ["drona2938"],
  actions: [
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => razorpayURL.apiURL,
      auth: razorpayAuth,
      authMapping: (auth) => generateRazorpayAuthHeader(auth.props)
    }),
    createPaymentlink
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  razorpay,
  razorpayAuth
});
