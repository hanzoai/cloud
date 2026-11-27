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

// ../ap/packages/pieces/community/afforai/src/index.ts
var index_exports = {};
__export(index_exports, {
  afforai: () => afforai
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/afforai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), afforaiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: `
  To obtain your API Key, follow these steps:
  1. Log in to your Afforai account.
  2. Navigate to **API** section on left panel.
  3. On the top-right, you can find you API key.
  `
});

// ../ap/packages/pieces/community/afforai/src/lib/actions/ask-chatbot.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), askChatbotAction = (0, import_pieces_framework2.createAction)({
  auth: afforaiAuth,
  name: "afforai_ask_chatbot",
  classification: "READ",
  displayName: "Ask Chatbot",
  description: "Gets AI-generated completions for a given chatbot.",
  audience: "both",
  aiMetadata: { description: "Sends a conversation history to a specific Afforai chatbot (identified by its Chatbot/session ID) and returns an AI-generated reply grounded in that chatbot's documents. Choose this to query an existing knowledge base rather than to manage documents. Optionally enable deeper document search and/or live Google search to broaden sources. Not idempotent: each call produces a fresh, potentially different completion.", idempotent: !1 },
  props: {
    sessionID: import_pieces_framework2.Property.ShortText({
      displayName: "Chatbot ID",
      required: !0,
      description: "You can find Chatbot ID by clicking settings button under **Actions** for given chatbot."
    }),
    history: import_pieces_framework2.Property.Array({
      displayName: "Chat History",
      required: !0,
      properties: {
        role: import_pieces_framework2.Property.StaticDropdown({
          displayName: "Role",
          description: "The role of the message sender.",
          required: !0,
          options: {
            disabled: !1,
            options: [
              {
                label: "user",
                value: "user"
              },
              {
                label: "assistant",
                value: "assistant"
              }
            ]
          }
        }),
        content: import_pieces_framework2.Property.LongText({
          displayName: "Message",
          description: "The content of the message.",
          required: !0
        })
      }
    }),
    powerful: import_pieces_framework2.Property.Checkbox({
      displayName: "AI should search more deeply for information in the given files ?",
      required: !0
    }),
    google: import_pieces_framework2.Property.Checkbox({
      displayName: "AI to search for information on Google?",
      required: !0
    })
  },
  async run(context) {
    let { sessionID, powerful, google } = context.propsValue, history = context.propsValue.history, request = {
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.afforai.com/api/api_completion",
      body: {
        apiKey: context.auth,
        sessionID,
        history,
        powerful,
        google
      }
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/afforai/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var afforai = (0, import_pieces_framework3.createPiece)({
  displayName: "Afforai",
  description: "Helps you search, summarize, and translate knowledge from hundreds of documents to help you produce trustworthy research.",
  auth: afforaiAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/afforai.png",
  categories: [import_pieces_framework4.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["kishanprmr", "abuaboud"],
  actions: [askChatbotAction],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  afforai
});
