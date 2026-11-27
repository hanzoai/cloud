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

// ../ap/packages/pieces/community/whatsable/src/index.ts
var index_exports = {};
__export(index_exports, {
  whatsable: () => whatsable,
  whatsableAuth: () => whatsableAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/whatsable/src/lib/actions/send-message.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var sendMessage = (0, import_pieces_framework.createAction)({
  name: "sendMessage",
  displayName: "Send Message",
  description: "",
  audience: "both",
  aiMetadata: { description: "Sends a WhatsApp text message to a single recipient through a connected Whatsable (WhatsApp Business) account. Use to deliver a one-off outbound message; requires the recipient phone number and the message text. Not idempotent: each call sends a new message, so repeating it delivers duplicates.", idempotent: !1 },
  auth: whatsableAuth,
  props: {
    to: import_pieces_framework.Property.ShortText({
      displayName: "To",
      description: "The recipient of the message",
      required: !0
    }),
    text: import_pieces_framework.Property.LongText({
      displayName: "Message",
      description: "The message to send",
      required: !0
    })
  },
  async run(ctx) {
    let { to, text } = ctx.propsValue;
    return await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://dashboard.whatsable.app/api/whatsapp/messages/send",
      headers: {
        Authorization: ctx.auth.secret_text
      },
      body: {
        to,
        text
      }
    });
  }
});

// ../ap/packages/pieces/community/whatsable/src/index.ts
var whatsableAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "Whatsable Auth Token",
  description: "The auth token for Whatsable",
  required: !0
}), whatsable = (0, import_pieces_framework2.createPiece)({
  displayName: "Whatsable",
  description: "Manage your WhatsApp business account",
  auth: whatsableAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/whatsable.png",
  authors: ["abuaboud"],
  actions: [sendMessage],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  whatsable,
  whatsableAuth
});
