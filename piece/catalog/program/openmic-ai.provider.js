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

// ../ap/packages/pieces/community/openmic-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  openmicAi: () => openmicAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/openmic-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/openmic-ai/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.openmic.ai/v1";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        Authorization: `Bearer ${api_key.secret_text}`,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/openmic-ai/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), openmicAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Openmic AI API Key",
  description: `
To get your Openmic AI API key:
1. Log in to your Openmic dashboard at https://app.openmic.ai
2. Navigate to  API Keys section in the left Navigation
3. Generate a new API key
4. Copy and paste the key here
  `,
  required: !0,
  validate: async ({ auth }) => {
    if (auth)
      try {
        return await makeRequest({
          secret_text: auth,
          type: import_pieces_framework2.AppConnectionType.SECRET_TEXT
        }, import_pieces_common2.HttpMethod.GET, "/calls"), {
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

// ../ap/packages/pieces/community/openmic-ai/src/lib/actions/create-phone-call.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var createPhoneCall = (0, import_pieces_framework3.createAction)({
  auth: openmicAiAuth,
  name: "createPhoneCall",
  displayName: "Create Phone Call",
  description: "Create a new outbound phone call using OpenMic AI",
  audience: "both",
  aiMetadata: { description: "Places a new outbound AI phone call from one E.164 number to another via OpenMic AI. Use to initiate a voice call programmatically; optionally override the default bot/agent, attach a customer ID, inject dynamic prompt variables, or set a callback URL for call events. Not idempotent \u2014 each call dials a new outbound call.", idempotent: !1 },
  props: {
    fromNumber: import_pieces_framework3.Property.ShortText({
      displayName: "From Number",
      description: "The number you own in E.164 format (e.g., +1234567890)",
      required: !0
    }),
    toNumber: import_pieces_framework3.Property.ShortText({
      displayName: "To Number",
      description: "The number you want to call in E.164 format (e.g., +0987654321)",
      required: !0
    }),
    overrideAgentId: import_pieces_framework3.Property.ShortText({
      displayName: "Agent ID",
      description: "The bot UID to override the default agent ",
      required: !1
    }),
    customerId: import_pieces_framework3.Property.ShortText({
      displayName: "Customer ID",
      description: "Customer identifier for tracking ",
      required: !1
    }),
    dynamicVariables: import_pieces_framework3.Property.Object({
      displayName: "Dynamic Variables",
      description: "Key-value pairs to replace in the prompt",
      required: !1
    }),
    callbackUrl: import_pieces_framework3.Property.ShortText({
      displayName: "Callback URL",
      description: "Callback URL to receive call events ",
      required: !1
    })
  },
  async run(context) {
    let body = {
      from_number: context.propsValue.fromNumber,
      to_number: context.propsValue.toNumber
    };
    return context.propsValue.overrideAgentId && (body.override_agent_id = context.propsValue.overrideAgentId), context.propsValue.customerId && (body.customer_id = context.propsValue.customerId), context.propsValue.dynamicVariables && (body.dynamic_variables = context.propsValue.dynamicVariables), context.propsValue.callbackUrl && (body.callback_url = context.propsValue.callbackUrl), (await makeRequest(
      context.auth,
      import_pieces_common3.HttpMethod.POST,
      "/create-phone-call",
      body
    )).body;
  }
});

// ../ap/packages/pieces/community/openmic-ai/src/lib/actions/find-bot.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var findBot = (0, import_pieces_framework4.createAction)({
  auth: openmicAiAuth,
  name: "findBot",
  displayName: "Find Bot",
  description: "Retrieve details of a specific bot by its UID",
  audience: "both",
  aiMetadata: { description: "Fetches the full details of a single OpenMic AI bot by its UID. Use when you already have a bot UID and need its configuration; to discover UIDs or browse bots, use Get Bots instead. Read-only and idempotent.", idempotent: !0 },
  props: {
    uid: import_pieces_framework4.Property.ShortText({
      displayName: "Bot UID",
      description: "The unique identifier of the bot",
      required: !0
    })
  },
  async run(context) {
    return (await makeRequest(
      context.auth,
      import_pieces_common4.HttpMethod.GET,
      `/bots/${context.propsValue.uid}`
    )).body;
  }
});

// ../ap/packages/pieces/community/openmic-ai/src/lib/actions/find-call.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");
var findCall = (0, import_pieces_framework5.createAction)({
  auth: openmicAiAuth,
  name: "findCall",
  displayName: "Find Call",
  description: "Retrieve details of a specific call by its ID",
  audience: "both",
  aiMetadata: { description: "Fetches the full details of a single OpenMic AI call by its call ID. Use when you already have a call ID and need its record; to discover IDs or browse calls, use Get Calls instead. Read-only and idempotent.", idempotent: !0 },
  props: {
    callId: import_pieces_framework5.Property.ShortText({
      displayName: "Call ID",
      description: "The unique identifier of the call",
      required: !0
    })
  },
  async run(context) {
    return await makeRequest(
      context.auth,
      import_pieces_common5.HttpMethod.GET,
      `/call/${context.propsValue.callId}`
    );
  }
});

// ../ap/packages/pieces/community/openmic-ai/src/lib/actions/get-bots.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var getBots = (0, import_pieces_framework6.createAction)({
  auth: openmicAiAuth,
  name: "getBots",
  displayName: "Get Bots",
  description: "Retrieve all bots with optional filtering and pagination",
  audience: "both",
  aiMetadata: { description: "Lists OpenMic AI bots, with optional filters by name (partial match) and created-after/created-before date range plus limit/offset pagination. With no filters it returns all bots; supply filters to narrow the set. Use to discover bots and their UIDs. Read-only and idempotent.", idempotent: !0 },
  props: {
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Bot Name",
      description: "Filter by bot name (partial match)",
      required: !1
    }),
    createdAfter: import_pieces_framework6.Property.ShortText({
      displayName: "Created After",
      description: "Filter bots created after this date (ISO 8601 format)",
      required: !1
    }),
    createdBefore: import_pieces_framework6.Property.ShortText({
      displayName: "Created Before",
      description: "Filter bots created before this date (ISO 8601 format)",
      required: !1
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Maximum number of bots to return (1-100)",
      required: !1,
      defaultValue: 10
    }),
    offset: import_pieces_framework6.Property.Number({
      displayName: "Offset",
      description: "Number of bots to skip for pagination",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let params = new URLSearchParams();
    context.propsValue.limit && params.append("limit", String(context.propsValue.limit)), context.propsValue.offset !== void 0 && params.append("offset", String(context.propsValue.offset)), context.propsValue.name && params.append("name", context.propsValue.name), context.propsValue.createdAfter && params.append("created_after", context.propsValue.createdAfter), context.propsValue.createdBefore && params.append("created_before", context.propsValue.createdBefore);
    let queryString = params.toString(), endpoint = queryString ? `/bots?${queryString}` : "/bots";
    return await makeRequest(context.auth, import_pieces_common6.HttpMethod.GET, endpoint);
  }
});

// ../ap/packages/pieces/community/openmic-ai/src/lib/actions/get-calls.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common");
var getCalls = (0, import_pieces_framework7.createAction)({
  auth: openmicAiAuth,
  name: "getCalls",
  displayName: "Get Calls",
  description: "Retrieve all calls with optional filtering and pagination",
  audience: "both",
  aiMetadata: { description: "Lists OpenMic AI calls, with optional filters by customer ID, from/to phone number, bot ID, date range, call status, and call type plus limit/offset pagination. With no filters it returns all calls; supply filters to narrow the set. Use to discover calls and their IDs. Read-only and idempotent.", idempotent: !0 },
  props: {
    customerId: import_pieces_framework7.Property.ShortText({
      displayName: "Customer ID",
      description: "Filter by customer ID",
      required: !1
    }),
    fromNumber: import_pieces_framework7.Property.ShortText({
      displayName: "From Number",
      description: "Filter by originating phone number",
      required: !1
    }),
    toNumber: import_pieces_framework7.Property.ShortText({
      displayName: "To Number",
      description: "Filter by destination phone number",
      required: !1
    }),
    botId: import_pieces_framework7.Property.ShortText({
      displayName: "Bot ID",
      description: "Filter by bot ID",
      required: !1
    }),
    fromDate: import_pieces_framework7.Property.ShortText({
      displayName: "From Date",
      description: "Filter calls from this date (ISO 8601 format)",
      required: !1
    }),
    toDate: import_pieces_framework7.Property.ShortText({
      displayName: "To Date",
      description: "Filter calls to this date (ISO 8601 format)",
      required: !1
    }),
    callStatus: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Call Status",
      description: "Filter by call status",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Registered", value: "registered" },
          { label: "Ongoing", value: "ongoing" },
          { label: "Ended", value: "ended" },
          { label: "Error", value: "error" }
        ]
      }
    }),
    callType: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Call Type",
      description: "Filter by call type",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Phone Call", value: "phonecall" },
          { label: "Web Call", value: "webcall" }
        ]
      }
    }),
    limit: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      description: "Maximum number of calls to return (1-100)",
      required: !1,
      defaultValue: 10
    }),
    offset: import_pieces_framework7.Property.Number({
      displayName: "Offset",
      description: "Number of calls to skip for pagination",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let params = new URLSearchParams();
    context.propsValue.limit && params.append("limit", String(context.propsValue.limit)), context.propsValue.offset !== void 0 && params.append("offset", String(context.propsValue.offset)), context.propsValue.customerId && params.append("customer_id", context.propsValue.customerId), context.propsValue.fromNumber && params.append("from_number", context.propsValue.fromNumber), context.propsValue.toNumber && params.append("to_number", context.propsValue.toNumber), context.propsValue.botId && params.append("bot_id", context.propsValue.botId), context.propsValue.fromDate && params.append("from_date", context.propsValue.fromDate), context.propsValue.toDate && params.append("to_date", context.propsValue.toDate), context.propsValue.callStatus && params.append("call_status", context.propsValue.callStatus), context.propsValue.callType && params.append("call_type", context.propsValue.callType);
    let queryString = params.toString(), endpoint = queryString ? `/calls?${queryString}` : "/calls";
    return await makeRequest(context.auth, import_pieces_common7.HttpMethod.GET, endpoint);
  }
});

// ../ap/packages/pieces/community/openmic-ai/src/index.ts
var import_pieces_common8 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/openmic-ai/src/lib/triggers/new-post-call-summary.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_framework9 = require("@activepieces/pieces-framework"), webhookConfigDescription = `
## Post-call Webhook Configuration

### Steps to Configure in OpenMic AI:
1. Open your bot in OpenMic AI
2. Click on the **Post-Call** tab
3. Go to the **Webhook** section
4. In the **Post-Call Webhook URL** field, paste the URL below:

\`\`\`
{{webhookUrl}}
\`\`\`

5. Save your configuration

[Learn more about post-call webhooks](https://docs.openmic.ai/post-call-webhook)
`, newPostCallSummary = (0, import_pieces_framework8.createTrigger)({
  auth: openmicAiAuth,
  name: "newPostCallSummary",
  displayName: "New Post-call Summary",
  description: "Trigger when a new call completes with comprehensive call data and analysis",
  aiMetadata: {
    description: "Fires when an OpenMic AI call finishes, delivering the end-of-call report via the post-call webhook \u2014 including transcript, summary, duration, disconnection reason, success evaluation, and any extracted data. Requires the bot to be configured with the post-call webhook URL."
  },
  props: {
    markdown: import_pieces_framework8.Property.MarkDown({
      variant: import_pieces_framework9.MarkdownVariant.INFO,
      value: `

${webhookConfigDescription}`
    })
  },
  sampleData: {
    type: "end-of-call-report",
    sessionId: "cmdx5w8oc0005q671s3cbg063",
    toPhoneNumber: "+916297653534",
    fromPhoneNumber: "+16167948654",
    callDuration: "0:00:45.723362",
    callType: "phonecall",
    disconnectionReason: "user_ended_call",
    direction: "outbound",
    createdAt: "2025-08-04T13:44:26.604Z",
    endedAt: "2025-08-04T13:45:03.325Z",
    callPickup: "yes",
    sessionType: "voice",
    transcript: [
      ["assistant", "Hello I am Jay."],
      ["user", "Yeah. Hi."],
      [
        "assistant",
        "Hey Soumyadip! Great to hear from you. How can I help you today? Want me to reserve some raisins for you?"
      ],
      ["user", "Gotcha."],
      [
        "assistant",
        "Awesome! I'll go ahead and reserve a pack of raisins for you at Shri Balaji Traders. Just let me know when you want to pick them up or if you'd like me to help with an online order. Cheers!"
      ]
    ],
    summary: "Jay greeted Soumyadip and offered to reserve a pack of raisins at Shri Balaji Traders. The conversation focused on confirming the reservation, with options for pickup or online order.",
    isSuccessful: !1,
    successEvaluation: !0,
    extractedData: {
      product: "Raisins"
    },
    dynamicVariables: {
      customerName: "Soumyadip Moni"
    }
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/openmic-ai/src/index.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var openmicAi = (0, import_pieces_framework10.createPiece)({
  displayName: "OpenMic AI",
  auth: openmicAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/openmic-ai.png",
  authors: ["sanket-a11y"],
  description: "An AI-powered platform that automates phone calls using advanced language models.",
  categories: [import_pieces_framework11.PieceCategory.COMMUNICATION],
  actions: [
    createPhoneCall,
    findBot,
    findCall,
    getBots,
    getCalls,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: openmicAiAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [newPostCallSummary]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  openmicAi
});
