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

// ../ap/packages/pieces/community/chatfly/src/index.ts
var index_exports = {};
__export(index_exports, {
  chatfly: () => chatfly
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chatfly/src/lib/actions/send-message.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/chatfly/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), chatflyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Enter your ChatFly API key",
  required: !0
});

// ../ap/packages/pieces/community/chatfly/src/lib/actions/send-message.ts
var sendMessageAction = (0, import_pieces_framework2.createAction)({
  auth: chatflyAuth,
  name: "send_message",
  classification: "READ",
  displayName: "Send Message",
  description: "Send a message to ChatFly bot",
  audience: "both",
  aiMetadata: { description: "Send a user message to a ChatFly AI chatbot and get back its streaming chat response. Choose this to query or converse with a deployed ChatFly bot. Requires the target bot ID and a session ID that ties the message to a conversation thread; each call posts a new message, so it is not idempotent.", idempotent: !1 },
  props: {
    bot_id: import_pieces_framework2.Property.ShortText({
      displayName: "Bot ID",
      description: "The ID of your bot",
      required: !0
    }),
    message: import_pieces_framework2.Property.LongText({
      displayName: "Message",
      description: "The message to send",
      required: !0
    }),
    session_id: import_pieces_framework2.Property.ShortText({
      displayName: "Session ID",
      description: "The ID of the session",
      required: !0
    })
  },
  async run(context) {
    let { bot_id, message, session_id } = context.propsValue;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://backend.chatfly.co/api/chat/get-streaming-response",
      headers: {
        "Content-Type": "application/json",
        Accept: "application/json"
      },
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        bot_id,
        message,
        session_id
      },
      responseType: "text"
    })).body;
  }
});

// ../ap/packages/pieces/community/chatfly/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var chatfly = (0, import_pieces_framework3.createPiece)({
  displayName: "Chatfly",
  description: "ChatFly allows you to build AI chatbots trained on your data.",
  categories: [import_pieces_framework4.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  auth: chatflyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/chatfly.png",
  authors: ["onyedikachi-david"],
  actions: [sendMessageAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  chatfly
});
