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

// ../ap/packages/pieces/community/line/src/index.ts
var index_exports = {};
__export(index_exports, {
  line: () => line,
  lineAuth2: () => lineAuth2
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/line/src/lib/actions/push-message.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), pushMessage = (0, import_pieces_framework.createAction)({
  name: "push_message",
  // Must be a unique across the piece, this shouldn't be changed.
  classification: "WRITE",
  auth: lineAuth2,
  displayName: "Push Message",
  description: "Push message to the line account",
  audience: "both",
  aiMetadata: {
    description: "Sends a text message from the LINE bot directly to a single recipient identified by their LINE user ID. Use this to proactively message a user (not as a reply within a webhook session); the user ID is typically captured from an incoming message webhook payload. Not idempotent \u2014 each call delivers a new message.",
    idempotent: !1
  },
  props: {
    userId: import_pieces_framework.Property.ShortText({
      displayName: "User Id",
      description: "The user id can be obtained from the webhook payload",
      required: !0
    }),
    text: import_pieces_framework.Property.ShortText({
      displayName: "Text",
      required: !0
    })
  },
  async run(context) {
    return import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.line.me/v2/bot/message/push",
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body: {
        to: context.propsValue.userId,
        messages: [
          {
            type: "text",
            text: context.propsValue.text
          }
        ]
      }
    });
  }
});

// ../ap/packages/pieces/community/line/src/lib/trigger/new-message.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), markdown = `
- Create Line bot account from Developer Console
- Go to the "Messaging API" section.
- In the webhook settings, paste this URL: 
  \`{{webhookUrl}}\`
- Publish Activepieces flow first then click "Verify" button
`, newMessage = (0, import_pieces_framework2.createTrigger)({
  name: "new-message",
  classification: "READ",
  displayName: "New Message",
  auth: import_pieces_framework2.PieceAuth.None(),
  description: "Triggers when a new message is received",
  aiMetadata: {
    description: "Fires when the LINE bot receives an inbound message event from a user via the Messaging API webhook. Each emitted item represents one active message event, carrying the sender details and message content needed to react or reply."
  },
  props: {
    md: import_pieces_framework2.Property.MarkDown({
      value: markdown
    })
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  sampleData: {},
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let { events } = context.payload.body;
    return events ? events.filter(
      (event) => event.mode === "active" && event.type === "message"
    ) : [];
  }
});

// ../ap/packages/pieces/community/line/src/index.ts
var lineAuth2 = import_pieces_framework3.PieceAuth.SecretText({
  displayName: "Bot Token",
  required: !0
}), line = (0, import_pieces_framework3.createPiece)({
  displayName: "Line Bot",
  description: "Build chatbots for LINE",
  auth: lineAuth2,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/line.png",
  categories: [import_pieces_framework4.PieceCategory.COMMUNICATION],
  authors: ["kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    pushMessage,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://api.line.me/v2",
      auth: lineAuth2,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newMessage]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  line,
  lineAuth2
});
