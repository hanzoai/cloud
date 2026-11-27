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

// ../ap/packages/pieces/community/smsmode/src/index.ts
var index_exports = {};
__export(index_exports, {
  smsmode: () => smsmode
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/smsmode/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), smsmodeAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Smsmode API Key",
  description: "Smsmode API Key is required to authenticate requests",
  required: !0
});

// ../ap/packages/pieces/community/smsmode/src/lib/actions/send-message.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), sendMessage = (0, import_pieces_framework2.createAction)({
  auth: smsmodeAuth,
  name: "sendMessage",
  displayName: "Send Message",
  description: "Send an SMS message to a recipient",
  audience: "both",
  aiMetadata: {
    description: "Sends an outbound SMS to a single recipient phone number via smsmode. Use to deliver a one-off text notification or alert. Requires the recipient number (international format, e.g. 33600000001) and the message text. Not idempotent: each call dispatches a new message.",
    idempotent: !1
  },
  props: {
    to: import_pieces_framework2.Property.ShortText({
      displayName: "Recipient Phone Number",
      description: "The phone number to send the message to (e.g., 33600000001)",
      required: !0
    }),
    text: import_pieces_framework2.Property.LongText({
      displayName: "Message Text",
      description: "The content of the SMS message",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      recipient: {
        to: propsValue.to
      },
      body: {
        text: propsValue.text
      }
    };
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://rest.smsmode.com/sms/v1/messages",
      headers: {
        "X-Api-Key": auth.secret_text,
        "Content-Type": "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/smsmode/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework"), smsmode = (0, import_pieces_framework3.createPiece)({
  displayName: "smsmode",
  auth: smsmodeAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/smsmode.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework4.PieceCategory.COMMUNICATION],
  description: "smsmode is an SMS messaging platform that allows you to send and receive SMS messages easily.",
  actions: [
    sendMessage,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: smsmodeAuth,
      baseUrl: () => "https://rest.smsmode.com/sms/v1",
      authMapping: async (auth) => ({
        "X-Api-Key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  smsmode
});
