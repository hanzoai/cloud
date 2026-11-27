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

// ../ap/packages/pieces/community/chatsistant/src/index.ts
var index_exports = {};
__export(index_exports, {
  chatsistant: () => chatsistant
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chatsistant/src/lib/actions/send-message.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/chatsistant/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chatsistant/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://app.chatsistant.com/api/v1";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${api_key}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/chatsistant/src/lib/common/auth.ts
var chatsistantAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Chatsistant API Key",
  description: `
To get your API Key:

1. Go to [Chatsistant platform](https://app.chatsistant.com/)
2. Sign up or log in to your account
3. Click on the "Account" menu in the navigation bar
4. Navigate to the "API Keys" section
5. Click "Generate new key"
6. Enter a description for your key and click "Generate"
7. Copy and save your API key securely
`,
  required: !0,
  validate: async ({ auth }) => {
    if (auth)
      try {
        return await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/chatbots", {}), {
          valid: !0
        };
      } catch (error) {
        return {
          valid: !1,
          error: "Invalid Api Key"
        };
      }
    return {
      valid: !1,
      error: "Invalid Api Key"
    };
  }
});

// ../ap/packages/pieces/community/chatsistant/src/lib/actions/send-message.ts
var sendMessage = (0, import_pieces_framework2.createAction)({
  auth: chatsistantAuth,
  name: "sendMessage",
  classification: "WRITE",
  displayName: "Send Message",
  description: "Send a message to a chatbot session",
  audience: "both",
  aiMetadata: {
    description: "Sends a user message to a Chatsistant chatbot and returns its streamed reply. Provide the chatbot UUID and the message text; supply a session UUID to continue an existing conversation, or omit it to start a fresh session automatically. Not idempotent \u2014 each call appends a new message and generates a new response.",
    idempotent: !1
  },
  props: {
    chatbot_uuid: import_pieces_framework2.Property.ShortText({
      displayName: "Chatbot UUID",
      description: "The UUID of the chatbot to send the message to",
      required: !0
    }),
    query: import_pieces_framework2.Property.LongText({
      displayName: "Message",
      description: "The message to send to the chatbot",
      required: !0
    }),
    session_uuid: import_pieces_framework2.Property.ShortText({
      displayName: "Session UUID",
      description: "The UUID of the session to send the message to",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { chatbot_uuid, session_uuid, query } = propsValue;
    if (session_uuid)
      return await makeRequest(
        auth.secret_text,
        import_pieces_common3.HttpMethod.POST,
        `/session/${session_uuid}/message/stream`,
        {
          query
        }
      );
    {
      let session_id = (await makeRequest(
        auth.secret_text,
        import_pieces_common3.HttpMethod.POST,
        `/chatbot/${chatbot_uuid}/session/create`,
        {}
      )).uuid;
      return await makeRequest(
        auth.secret_text,
        import_pieces_common3.HttpMethod.POST,
        `/session/${session_id}/message/stream`,
        {
          query
        }
      );
    }
  }
});

// ../ap/packages/pieces/community/chatsistant/src/lib/triggers/form-submission.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var formSubmission = (0, import_pieces_framework3.createTrigger)({
  auth: chatsistantAuth,
  name: "formSubmission",
  classification: "READ",
  displayName: "form submission",
  description: "Triggered when a form is submitted",
  aiMetadata: {
    description: "Fires when a visitor submits a form within a Chatsistant chatbot conversation, delivering the captured form fields (such as name and email) along with the originating session identifier. Requires the Form Submission webhook to be configured in the Chatsistant account."
  },
  props: {
    markdown: import_pieces_framework3.Property.MarkDown({
      value: `## Chatsistant Webhook Setup
            To use this trigger, you need to manually set up a webhook in your Chatsistant account:

            1. Login to your Chatsistant account.
            2. Navigate to **Customizations** tab on the left navigation menu.
            3. Scroll down to **Webhook** and click to expand it.
            4. Select the **Form Submission** event and specify the following URL:
            \`\`\`text
            {{webhookUrl}}
            \`\`\`
            5. Click Save to register the webhook.
            `
    })
  },
  sampleData: {
    Email: "test@gmail.com",
    name: "Test User",
    session_uuid: "cf014deb3b3b438d935f1ee7042ff66f"
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/chatsistant/src/index.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), chatsistant = (0, import_pieces_framework4.createPiece)({
  displayName: "Chatsistant",
  auth: chatsistantAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/chatsistant.png",
  authors: ["sanket-a11y"],
  actions: [
    sendMessage,
    (0, import_pieces_common4.createCustomApiCallAction)({
      baseUrl: () => "https://app.chatsistant.com/api/v1",
      auth: chatsistantAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth}`
      })
    })
  ],
  triggers: [formSubmission]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  chatsistant
});
