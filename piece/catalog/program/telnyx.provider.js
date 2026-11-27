"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/telnyx/src/index.ts
var index_exports = {};
__export(index_exports, {
  telnyx: () => telnyx
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/telnyx/src/lib/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/telnyx/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), TELNYX_API_BASE_URL = "https://api.telnyx.com/v2";
async function telnyxRequest({
  apiKey,
  method,
  path,
  body
}) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${TELNYX_API_BASE_URL}${path}`,
    headers: {
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json",
      Accept: "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/telnyx/src/lib/auth.ts
var telnyxAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your Telnyx API key.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await telnyxRequest({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.GET,
        path: "/messaging_profiles"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key or unable to reach the Telnyx API."
      };
    }
  }
});

// ../ap/packages/pieces/community/telnyx/src/lib/actions/send-sms.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var sendSmsAction = (0, import_pieces_framework2.createAction)({
  auth: telnyxAuth,
  name: "send_sms",
  classification: "WRITE",
  displayName: "Send SMS",
  description: "Send an SMS message using the Telnyx Messages API.",
  audience: "both",
  aiMetadata: {
    description: "Sends an outbound SMS via Telnyx from a sender number/ID to a destination phone number (E.164) with the given message body; optionally scope it to a specific messaging profile. Use to deliver a text message to a recipient. Not idempotent: each call sends a new message, so repeating it sends duplicates.",
    idempotent: !1
  },
  props: {
    from: import_pieces_framework2.Property.ShortText({
      displayName: "From",
      description: "The sending number or sender ID.",
      required: !0
    }),
    to: import_pieces_framework2.Property.ShortText({
      displayName: "To",
      description: "The destination phone number in E.164 format.",
      required: !0
    }),
    text: import_pieces_framework2.Property.LongText({
      displayName: "Text",
      description: "The message body to send.",
      required: !0
    }),
    messaging_profile_id: import_pieces_framework2.Property.ShortText({
      displayName: "Messaging Profile ID",
      description: "Optional Telnyx messaging profile ID.",
      required: !1
    })
  },
  async run(context) {
    let { from, to, text, messaging_profile_id } = context.propsValue;
    return await telnyxRequest({
      apiKey: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/messages",
      body: __spreadValues({
        from,
        to,
        text
      }, messaging_profile_id ? { messaging_profile_id } : {})
    });
  }
});

// ../ap/packages/pieces/community/telnyx/src/lib/actions/make-call.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var makeCallAction = (0, import_pieces_framework3.createAction)({
  auth: telnyxAuth,
  name: "make_call",
  classification: "WRITE",
  displayName: "Make Call",
  description: "Initiate an outbound call using the Telnyx Call Control API.",
  audience: "both",
  aiMetadata: {
    description: "Places an outbound voice call via the Telnyx Call Control API from a caller ID (E.164) to a destination number or SIP URI, using a required Call Control Application (connection) ID; can optionally play an audio file (WAV/MP3) on answer and override the webhook destination and ring timeout. Use to dial a recipient programmatically. Not idempotent: each call initiates a new call.",
    idempotent: !1
  },
  props: {
    connection_id: import_pieces_framework3.Property.ShortText({
      displayName: "Connection ID",
      description: "The Telnyx Call Control Application ID (formerly connection ID) used to place the call.",
      required: !0
    }),
    from: import_pieces_framework3.Property.ShortText({
      displayName: "From",
      description: "The caller ID number in E.164 format.",
      required: !0
    }),
    to: import_pieces_framework3.Property.ShortText({
      displayName: "To",
      description: "The destination number or SIP URI.",
      required: !0
    }),
    audio_url: import_pieces_framework3.Property.ShortText({
      displayName: "Audio URL",
      description: "Optional WAV or MP3 URL to play when the call is answered.",
      required: !1
    }),
    webhook_url: import_pieces_framework3.Property.ShortText({
      displayName: "Webhook URL",
      description: "Optional URL that overrides the default Telnyx webhook destination for this call.",
      required: !1
    }),
    timeout_secs: import_pieces_framework3.Property.Number({
      displayName: "Timeout (seconds)",
      description: "How long to let the destination ring before timing out.",
      required: !1,
      defaultValue: 30
    })
  },
  async run(context) {
    let { connection_id, from, to, audio_url, webhook_url, timeout_secs } = context.propsValue;
    return await telnyxRequest({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/calls",
      body: __spreadValues(__spreadValues(__spreadValues({
        connection_id,
        from,
        to
      }, audio_url ? { audio_url } : {}), webhook_url ? { webhook_url } : {}), timeout_secs ? { timeout_secs } : {})
    });
  }
});

// ../ap/packages/pieces/community/telnyx/src/lib/triggers/message-received.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var messageReceivedTrigger = (0, import_pieces_framework4.createTrigger)({
  auth: telnyxAuth,
  name: "message_received",
  classification: "READ",
  displayName: "Message Received",
  description: "Triggers when Telnyx delivers a message.received webhook event.",
  aiMetadata: {
    description: "Fires when Telnyx delivers an inbound message via its message.received webhook, representing an incoming SMS/MMS received on a configured messaging profile. Requires manually configuring the Telnyx messaging webhook (v2) to the given URL; events for other event types are ignored."
  },
  props: {
    webhook_instructions: import_pieces_framework4.Property.MarkDown({
      value: `
To use this trigger, configure your Telnyx messaging webhook manually:

1. Open your Telnyx portal.
2. Go to the messaging profile or inbound messaging configuration you use for SMS.
3. Set the webhook URL to:

\`\`\`text
{{webhookUrl}}
\`\`\`

4. Make sure webhook API version is **v2**.
5. Enable or send **message.received** events to this webhook URL.
      `
    })
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  sampleData: {
    event_type: "message.received",
    id: "b301ed3f-1490-491f-995f-6e64e69674d4",
    occurred_at: "2024-01-15T20:16:07.588+00:00",
    payload: {
      completed_at: null,
      cost: { amount: "0.0000", currency: "USD" },
      direction: "inbound",
      encoding: "GSM-7",
      errors: [],
      from: {
        carrier: "T-Mobile USA",
        line_type: "long_code",
        phone_number: "+13125550001"
      },
      id: "84cca175-9755-4859-b67f-4730d7f58aa3",
      media: [],
      messaging_profile_id: "740572b6-099c-44a1-89b9-6c92163bc68d",
      organization_id: "47a530f8-4362-4526-829b-bcee17fd9f7a",
      parts: 1,
      received_at: "2024-01-15T20:16:07.503+00:00",
      record_type: "message",
      sent_at: null,
      tags: [],
      text: "Hello from Telnyx!",
      to: [
        {
          carrier: "Telnyx",
          line_type: "Wireless",
          phone_number: "+17735550002",
          status: "webhook_delivered"
        }
      ],
      type: "SMS",
      valid_until: null,
      webhook_failover_url: null,
      webhook_url: "https://example.com/webhooks"
    },
    record_type: "event"
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    var _a;
    let body = context.payload.body;
    return ((_a = body == null ? void 0 : body.data) == null ? void 0 : _a.event_type) !== "message.received" ? [] : [body.data];
  }
});

// ../ap/packages/pieces/community/telnyx/src/index.ts
var telnyx = (0, import_pieces_framework5.createPiece)({
  displayName: "Telnyx",
  description: "Telecom API platform for SMS messaging, voice calls, and messaging webhooks.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/telnyx.png",
  categories: [import_pieces_framework6.PieceCategory.COMMUNICATION, import_pieces_framework6.PieceCategory.DEVELOPER_TOOLS],
  auth: telnyxAuth,
  authors: ["Harmatta", "sanket-a11y"],
  actions: [
    sendSmsAction,
    makeCallAction,
    (0, import_pieces_common5.createCustomApiCallAction)({
      baseUrl: () => "https://api.telnyx.com/v2",
      auth: telnyxAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [messageReceivedTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  telnyx
});
