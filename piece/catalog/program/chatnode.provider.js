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

// ../ap/packages/pieces/community/chatnode/src/index.ts
var index_exports = {};
__export(index_exports, {
  chatnode: () => chatnode
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chatnode/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/chatnode/src/lib/common/constants.ts
var BASE_URL = "https://api.public.chatnode.ai/v1";

// ../ap/packages/pieces/community/chatnode/src/lib/common/auth.ts
var chatnodeAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "To generate an API key, go to Team Settings and navigate to the API Access tab. There, you\u2019ll find the option to generate a new API key.",
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: BASE_URL + "/auth_me",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key"
      };
    }
  }
});

// ../ap/packages/pieces/community/chatnode/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chatnode/src/lib/actions/ask-chatbot.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");
var askChatbotAction = (0, import_pieces_framework2.createAction)({
  name: "ask-chatbot",
  classification: "WRITE",
  auth: chatnodeAuth,
  displayName: "Ask Chatbot",
  description: "Sends a message to your bot and get back an answer.",
  audience: "both",
  aiMetadata: {
    description: "Sends a user message to a ChatNode chatbot (identified by Bot ID) and returns the bot's generated answer. Choose this to query a ChatNode-trained knowledge-base bot conversationally. Optionally pass a Chat Session ID to continue an existing conversation thread; omitting it starts a fresh session each call. Not idempotent: each call posts a new message and produces a new model-generated response.",
    idempotent: !1
  },
  props: {
    botId: import_pieces_framework2.Property.ShortText({
      displayName: "Bot ID",
      required: !0,
      description: "You can obtain Bot ID from bot's settings menu."
    }),
    message: import_pieces_framework2.Property.LongText({
      displayName: "Message",
      required: !0
    }),
    chatSessionId: import_pieces_framework2.Property.ShortText({
      displayName: "Chat Session ID",
      description: "The chat session id to keep track of a unique conversation. If not provided, it will default to generate a new one each time.You can obtain it from URL after /chats/.",
      required: !1
    })
  },
  async run(context) {
    let { botId, message, chatSessionId } = context.propsValue;
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: BASE_URL + `/${botId}`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        message,
        chat_session_id: chatSessionId
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/chatnode/src/index.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
var chatnode = (0, import_pieces_framework3.createPiece)({
  displayName: "ChatNode",
  auth: chatnodeAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/chatnode.png",
  categories: [
    import_pieces_framework4.PieceCategory.ARTIFICIAL_INTELLIGENCE,
    import_pieces_framework4.PieceCategory.PRODUCTIVITY
  ],
  authors: ["kishanprmr"],
  actions: [
    askChatbotAction,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: chatnodeAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  chatnode
});
