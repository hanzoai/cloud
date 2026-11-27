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

// ../ap/packages/pieces/community/bolna/src/index.ts
var index_exports = {};
__export(index_exports, {
  bolna: () => bolna
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bolna/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bolna/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = " https://api.bolna.ai";
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

// ../ap/packages/pieces/community/bolna/src/lib/common/auth.ts
var bolnaaiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Bolna AI API Key",
  description: `
To get your API Key:

1. Login to the dashboard at https://platform.bolna.ai
2. Navigate to Developers tab from the left menu bar after login
3. Click the button Generate a new API Key to generate a key
4. Get your API Key
`,
  required: !0,
  validate: async ({ auth }) => {
    if (auth)
      try {
        return await makeRequest(auth, import_pieces_common2.HttpMethod.GET, "/agent/all"), {
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

// ../ap/packages/pieces/community/bolna/src/lib/actions/make-phone-call.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bolna/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var agentId = import_pieces_framework2.Property.Dropdown({
  displayName: "Agent",
  description: "Select the Bolna Voice AI agent",
  required: !0,
  refreshers: [],
  auth: bolnaaiAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (await makeRequest(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/agent/all"
        )).map((agent) => ({
          label: agent.agent_name,
          value: agent.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/bolna/src/lib/actions/make-phone-call.ts
var makePhoneCall = (0, import_pieces_framework3.createAction)({
  auth: bolnaaiAuth,
  name: "makePhoneCall",
  classification: "WRITE",
  displayName: "Make Phone Call",
  description: "Initiate an outbound phone call from a Bolna Voice AI agent to a recipient.",
  audience: "both",
  aiMetadata: {
    description: "Place an outbound voice call to a recipient using a Bolna AI agent. Requires an agent ID and a recipient phone number in E.164 format; optionally set a from-number, schedule the call for a future ISO 8601 time (omit to dial immediately), and pass dynamic user variables referenced in the agent prompt. Each call is not idempotent \u2014 repeating it places another call.",
    idempotent: !1
  },
  props: {
    agentId,
    recipientPhoneNumber: import_pieces_framework3.Property.ShortText({
      displayName: "Recipient Phone Number",
      description: "Phone number of the recipient with country code (E.164 format, e.g., +10123456789)",
      required: !0
    }),
    fromPhoneNumber: import_pieces_framework3.Property.ShortText({
      displayName: "From Phone Number",
      description: "Phone number of the sender with country code (E.164 format, e.g., +19876543007). Optional.",
      required: !1
    }),
    scheduledAt: import_pieces_framework3.Property.ShortText({
      displayName: "Scheduled At",
      description: "The scheduled date and time in ISO 8601 format with time zone (e.g., 2025-08-21T10:35:00Z). Leave empty to call immediately.",
      required: !1
    }),
    userData: import_pieces_framework3.Property.Json({
      displayName: "User Data",
      description: "Additional user dynamic variables as defined in the agent prompt (JSON object)",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth.secret_text, payload = {
      agent_id: context.propsValue.agentId,
      recipient_phone_number: context.propsValue.recipientPhoneNumber
    };
    return context.propsValue.fromPhoneNumber && (payload.from_phone_number = context.propsValue.fromPhoneNumber), context.propsValue.scheduledAt && (payload.scheduled_at = context.propsValue.scheduledAt), context.propsValue.userData && (payload.user_data = context.propsValue.userData), await makeRequest(auth, import_pieces_common4.HttpMethod.POST, "/call", payload);
  }
});

// ../ap/packages/pieces/community/bolna/src/lib/triggers/call-completion-report.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var callCompletionReport = (0, import_pieces_framework4.createTrigger)({
  auth: bolnaaiAuth,
  name: "callCompletionReport",
  classification: "READ",
  displayName: "Call Completion Report",
  description: "Triggers when  Bolna call report  it is completed",
  aiMetadata: {
    description: "Fires when a Bolna AI voice call finishes and its execution report is pushed to the configured webhook, emitting only calls with a completed status. Use to act on call outcomes such as transcript, duration, cost, and recording once a conversation has ended. Requires manually configuring the Bolna agent to push execution data to the webhook URL."
  },
  props: {
    markdown: import_pieces_framework4.Property.MarkDown({
      value: `## Bolna AI Webhook Setup
                To use this trigger, you need to manually set up a webhook in your Bolna AI account:
    
                1. Login to your Bolna AI account.
                2. Navigate to **Agent Setup** tab on the left navigation menu.
                3. Go to **Analytics** .
                4. Add to the **Push all execution data to webhook**  and specify the following URL:
                \`\`\`text
                {{webhookUrl}}
                \`\`\`
                5. Click Save Agent.
                `
    })
  },
  sampleData: {
    id: "7e8391e5-dd-4d3b-ddd-dfe255c71848",
    agent_id: "5a12b25a-dddd-4e77-90a5-9b4fd4577e44",
    batch_id: null,
    created_at: "2025-11-19T08:48:27.193399",
    updated_at: "2025-11-19T08:49:25.219095",
    scheduled_at: "2025-11-19T08:48:27.193376",
    answered_by_voice_mail: null,
    conversation_duration: 24.2,
    total_cost: 3.54,
    transcript: `assistant: Hello from Bolna
user:  hello
assistant:  Hello! Am I speaking with you? Is this a good time to talk? How can I assist you today?
`,
    usage_breakdown: {
      llmModel: { "azure/gpt-4.1-mini": { input: 1397, output: 23 } },
      llmTokens: 0,
      synthesizer_model: "eleven_turbo_v2_5",
      transcriber_model: "nova-3",
      llm_usage_breakdown: {
        hangup: null,
        analytics: null,
        extraction: null,
        conversation: {
          input: 1397,
          model: "azure/gpt-4.1-mini",
          output: 23,
          provider: "azure",
          provider_connected: !1
        },
        summarization: null
      },
      synthesizer_provider: "elevenlabs",
      transcriber_duration: 0,
      transcriber_provider: "deepgram",
      synthesizer_characters: 111,
      synthesizer_usage_breakdown: {
        provider_connected: !1,
        welcome_message_cache: !0,
        conversation_characters: 111,
        welcome_message_characters: 0
      },
      transcriber_usage_breakdown: {
        provider_connected: !1,
        transcriber_duration: 0
      }
    },
    cost_breakdown: {
      llm: 0.06,
      network: 0.375,
      platform: 2,
      synthesizer: 1.11,
      transcriber: 0,
      llm_breakdown: {
        hangup: 0,
        analytics: 0,
        extraction: 0,
        conversation: 0.06,
        summarization: 0
      },
      transfer_cost: 0,
      synthesizer_breakdown: { conversation: 1.11, welcome_message: 0 },
      transcriber_breakdown: { analytics: 0, conversation: 0 }
    },
    extracted_data: null,
    summary: null,
    error_message: null,
    status: "completed",
    agent_extraction: null,
    workflow_retries: null,
    rescheduled_at: null,
    custom_extractions: null,
    campaign_id: null,
    smart_status: null,
    user_number: "+9199999999",
    agent_number: "+916565656565",
    telephony_data: {
      duration: "24",
      to_number: "+9199999999",
      from_number: "+916565656565",
      recording_url: "https://bolna-recordings-india.s3.ap-south-1.amazonaws.com/plivo/e3v16015b-vvvv12ef-42fd-898c-52cevvv62058v8cd.mp3",
      hosted_telephony: !0,
      provider_call_id: "e316c015b-12ef-4c2fd-c898c-52ce620ccc588cd",
      call_type: "outbound",
      provider: "plivo",
      hangup_by: "Callee",
      hangup_reason: "Call recipient hungup",
      hangup_provider_code: 4e3
    },
    transfer_call_data: null,
    context_details: {
      recipient_data: null,
      recipient_phone_number: "+9199999990"
    },
    batch_run_details: null,
    provider: "plivo"
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return context.payload.body.status != "completed" ? [] : [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bolna/src/index.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), bolna = (0, import_pieces_framework5.createPiece)({
  displayName: "Bolna AI",
  auth: bolnaaiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/bolna.png",
  authors: ["sanket-a11y"],
  actions: [
    makePhoneCall,
    (0, import_pieces_common5.createCustomApiCallAction)({
      auth: bolnaaiAuth,
      baseUrl: () => "https://api.bolna.ai",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [callCompletionReport]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  bolna
});
