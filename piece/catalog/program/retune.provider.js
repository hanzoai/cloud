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

// ../ap/packages/pieces/community/retune/src/index.ts
var index_exports = {};
__export(index_exports, {
  retune: () => retune
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/retune/src/lib/actions/ask-chatbot.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/retune/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), markdownDescription = `
Follow these instructions to get your re:tune chat ID and API Key:

1. Visit the chatbots page: https://retune.so/chats
2. Once on the website, locate and click on the chatbot you want to use
3. From the URL, copy the chat ID you want to use;
e.g from this: https://retune.so/chat/acewocwe-123123-123123-123123/ your chat ID is "acewocwe-123123-123123-123123"
4. To get the API key, go to https://retune.so/settings
5. Scroll to the bottom to find "Re:tune API Keys" and copy your key below
`, retuneAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdownDescription,
  required: !0,
  props: {
    // There is no way to programmatically get the user's chatbots, so we have this
    chatId: import_pieces_framework.Property.ShortText({
      displayName: "Chat ID",
      description: "The ID of the chat you want to use.",
      required: !0
    }),
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your re:tune API key.",
      required: !0
    })
  },
  validate: async (auth) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        url: `https://retune.so/api/chat/${auth.auth.chatId}/threads`,
        method: import_pieces_common.HttpMethod.POST,
        headers: {
          "X-Workspace-API-Key": auth.auth.apiKey
        },
        body: {}
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key"
      };
    }
  }
});

// ../ap/packages/pieces/community/retune/src/lib/actions/ask-chatbot.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), askChatbot = (0, import_pieces_framework2.createAction)({
  auth: retuneAuth,
  name: "ask_chatbot",
  displayName: "Ask Chatbot",
  description: "Sends a message to an existing thread with a chatbot.",
  audience: "both",
  aiMetadata: { description: "Sends a user message to an existing re:tune chatbot conversation thread and returns the bot's generated reply. Use this to interact with a configured re:tune chatbot; requires both a thread ID (a conversation context that must already exist) and the message text. Not idempotent \u2014 each call appends a turn to the thread and produces a fresh AI response.", idempotent: !1 },
  props: {
    thread: import_pieces_framework2.Property.Dropdown({
      auth: retuneAuth,
      displayName: "Thread",
      description: "The thread you want to send the message to.",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        options: (await import_pieces_common2.httpClient.sendRequest({
          url: `https://retune.so/api/chat/${auth.props.chatId}/threads`,
          method: import_pieces_common2.HttpMethod.POST,
          headers: {
            "X-Workspace-API-Key": auth.props.apiKey
          },
          body: {}
        })).body.threads.map((item) => {
          var _a;
          return {
            label: (_a = item.name) != null ? _a : item.id,
            value: item.id
          };
        })
      } : {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      }
    }),
    message: import_pieces_framework2.Property.ShortText({
      displayName: "Message",
      description: "The message you want to send.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { thread, message } = propsValue;
    return (await import_pieces_common2.httpClient.sendRequest({
      url: `https://retune.so/api/chat/${auth.props.chatId}/response`,
      method: import_pieces_common2.HttpMethod.POST,
      headers: {
        "X-Workspace-API-Key": auth.props.apiKey
      },
      body: {
        threadId: thread,
        input: message
      }
    })).body.response.value;
  }
});

// ../ap/packages/pieces/community/retune/src/index.ts
var retune = (0, import_pieces_framework3.createPiece)({
  displayName: "re:tune",
  description: "Everything you need to transform your business with AI, from custom chatbots to autonomous agents.",
  auth: retuneAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/retune.png",
  categories: [import_pieces_framework4.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    askChatbot,
    (0, import_pieces_common3.createCustomApiCallAction)({
      baseUrl: () => "https://retune.so/api",
      auth: retuneAuth,
      authMapping: async (auth) => ({
        "X-Workspace-API-Key": auth.props.apiKey
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  retune
});
