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

// ../ap/packages/pieces/community/orimon/src/index.ts
var index_exports = {};
__export(index_exports, {
  orimon: () => orimon
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/orimon/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), orimonAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Orimon API Key",
  description: `
To get your Orimon API Key:

1. **Login to your Orimon Dashboard**
2. **Click the profile icon** (image icon) in the Top Right corner
3. **Select "Profile"** from the menu
4. **Click "Generate Your Secret Key"** to create a new API key
5. **Click the eye icon** to reveal your key
6. **Copy the key** and paste it here

For more details, visit: [Orimon Developer API Docs](https://orimon.gitbook.io/docs/developer-api/getting-started-with-apis)
  `,
  required: !0
});

// ../ap/packages/pieces/community/orimon/src/lib/actions/send-message.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), sendMessage = (0, import_pieces_framework2.createAction)({
  auth: orimonAuth,
  name: "sendMessage",
  displayName: "Send Message",
  description: "Send a message to an Orimon chatbot and get a response",
  audience: "both",
  aiMetadata: {
    description: "Send a text message to an Orimon chatbot for a given tenant and return the bot's reply. Use to drive a conversational exchange with the chatbot. Requires the tenant ID (from the bot configuration page URL); each call posts a new message and generates a fresh conversation session, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    tenantId: import_pieces_framework2.Property.ShortText({
      displayName: "Tenant ID",
      description: 'To get it: Login to your dashboard and click on the bot configuration page. At the top in the URL bar, copy the value starting after "tenant/" to the end.',
      required: !0
    }),
    messageText: import_pieces_framework2.Property.LongText({
      displayName: "Message",
      description: "The message to send to the chatbot",
      required: !0
    }),
    messageId: import_pieces_framework2.Property.ShortText({
      displayName: "Message ID",
      description: "Unique identifier for this message",
      required: !1
    })
  },
  async run(context) {
    let { tenantId, messageText, messageId } = context.propsValue, apiKey = context.auth, payload = {
      type: "message",
      info: {
        psid: `${Math.random().toString(36).substring(2, 15)}_${tenantId}`,
        sender: "user",
        tenantId,
        platformName: "web"
      },
      message: {
        id: messageId || `msg_${Date.now()}`,
        type: "text",
        payload: {
          text: messageText
        }
      }
    };
    try {
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: "https://channel-connector.orimon.ai/orimon/v1/conversation/api/message",
        headers: {
          authorization: `apiKey ${apiKey}`,
          "Content-Type": "application/json"
        },
        body: payload
      })).body;
    } catch (error) {
      throw new Error(
        `Failed to send message to Orimon: ${error instanceof Error ? error.message : "Unknown error"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/orimon/src/lib/triggers/new-lead.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var newLead = (0, import_pieces_framework3.createTrigger)({
  auth: orimonAuth,
  name: "newLead",
  displayName: "New Lead",
  description: "Trigger when a new lead is captured by your Orimon chatbot",
  aiMetadata: {
    description: "Fires when the Orimon chatbot captures a new lead, delivering the lead details (such as contact email, IP/geolocation, and browser/platform info) via a configured webhook."
  },
  props: {
    markdown: import_pieces_framework3.Property.MarkDown({
      value: `
## Webhook Configuration for Orimon

To enable this trigger, follow these steps to configure the webhook in your Orimon account:

### Step-by-Step Setup:

1. **Login to Orimon Dashboard** - [https://orimon.ai/login](https://orimon.ai/login).
2. **Go to Bot Overview**.
3. **Navigate to LeadFlow Integrations** - On the overview page, click on the **Settings** tab, then select **LeadFlow Integrations**
4. **Select Webhook** - Click on the "Webhook" option from the integrations page
5. **Paste the Webhook URL** - In the "Webhook URL" input field, paste the following URL:
\`\`\`text
{{webhookUrl}}
\`\`\`
6. **Submit** - Click the "Submit" button to save the configuration    `
    })
  },
  sampleData: {
    email: "test@test.com",
    host: "channel-connector.orimon.ai",
    origin: "https://bot.orimon.ai",
    referer: "https://bot.orimon.ai/deploy/index.html?tenantId=tenantId&testBot=true&defaultOpen=true",
    clientIp: "111.11.1.111",
    userIp: "111.11.1.111",
    userIpCity: "Mumbai",
    userIpCountry: "IN",
    platform_info: {
      description: "Chrome 142.0.0.0 on Windows 10 64-bit",
      layout: "Blink",
      manufacturer: null,
      name: "Chrome",
      prerelease: null,
      product: null,
      ua: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
      version: "142.0.0.0",
      os: {
        architecture: 64,
        family: "Windows",
        version: "10"
      },
      device: "Desktop",
      device_name: "NA"
    }
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

// ../ap/packages/pieces/community/orimon/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), orimon = (0, import_pieces_framework4.createPiece)({
  displayName: "Orimon",
  auth: orimonAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/orimon.png",
  authors: ["sanket-a11y"],
  actions: [
    sendMessage,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: orimonAuth,
      baseUrl: () => "https://channel-connector.orimon.ai/orimon/v1",
      authMapping: async (auth) => ({
        authorization: `apiKey ${auth.secret_text}`
      })
    })
  ],
  triggers: [newLead]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  orimon
});
