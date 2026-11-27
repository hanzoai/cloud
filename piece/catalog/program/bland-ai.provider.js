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

// ../ap/packages/pieces/community/bland-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  blandAi: () => blandAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bland-ai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), BLAND_AI_BASE_URL = "https://api.bland.ai/v1", blandAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your API key:
1. Log in to your [Bland AI dashboard](https://app.bland.ai)
2. Go to **Profile** (bottom-left menu) -> **Settings**
3. Click **API Keys** and copy your key`,
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b;
    try {
      let response = await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${BLAND_AI_BASE_URL}/calls`,
        headers: {
          authorization: auth,
          "Content-Type": "application/json"
        },
        queryParams: {
          limit: "1"
        }
      });
      return response.status === 200 ? { valid: !0 } : {
        valid: !1,
        error: `Unexpected status: ${response.status}`
      };
    } catch (e) {
      let err = e;
      return ((_a = err.response) == null ? void 0 : _a.status) === 401 || ((_b = err.response) == null ? void 0 : _b.status) === 403 ? {
        valid: !1,
        error: "Invalid API key. Please check your Bland AI account settings."
      } : {
        valid: !1,
        error: `Connection failed: ${String(e).slice(0, 100)}`
      };
    }
  }
});

// ../ap/packages/pieces/community/bland-ai/src/lib/actions/send-call.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bland-ai/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
async function blandApiCall(params) {
  return (await import_pieces_common2.httpClient.sendRequest({
    method: params.method,
    url: `${BLAND_AI_BASE_URL}${params.path}`,
    headers: {
      authorization: params.apiKey,
      "Content-Type": "application/json"
    },
    body: params.body,
    queryParams: params.query
  })).body;
}

// ../ap/packages/pieces/community/bland-ai/src/lib/actions/send-call.ts
var sendCall = (0, import_pieces_framework2.createAction)({
  auth: blandAiAuth,
  name: "send_call",
  classification: "WRITE",
  displayName: "Send Call",
  description: "Initiate an AI phone call to a recipient.",
  audience: "both",
  aiMetadata: {
    description: "Places an outbound AI voice call to a phone number (E.164 format, with country code) via Bland AI. Drive the conversation either by supplying free-text Task instructions for the agent or by referencing a pre-built Pathway ID (the Pathway overrides the Task). Use to actually start a call; not idempotent \u2014 each call triggers a new outbound dial.",
    idempotent: !1
  },
  props: {
    phoneNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Number",
      description: "The number to call in E.164 format (e.g. +12223334444). Must include the country code.",
      required: !0
    }),
    task: import_pieces_framework2.Property.LongText({
      displayName: "Task",
      description: "Instructions for the AI agent \u2014 what it should say, ask, or accomplish during the call. Not required if using a Pathway ID.",
      required: !1
    }),
    pathwayId: import_pieces_framework2.Property.ShortText({
      displayName: "Pathway ID",
      description: "UUID of a pre-built Pathway from your Bland AI dashboard. When provided, it overrides the Task field. Find it in the Pathways section of your dashboard.",
      required: !1
    }),
    firstSentence: import_pieces_framework2.Property.ShortText({
      displayName: "First Sentence",
      description: "Exact opening line the agent will say when the call is answered. Leave blank to let the agent decide.",
      required: !1
    }),
    voice: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Voice",
      description: "The voice the AI agent will use. Choose from Bland AI preset voices optimised for phone quality.",
      required: !1,
      defaultValue: "maya",
      options: {
        options: [
          { label: "Maya \u2014 Young American Female", value: "maya" },
          { label: "Ryan \u2014 Professional American Male", value: "ryan" },
          { label: "Mason \u2014 American Male", value: "mason" },
          { label: "Tina \u2014 Gentle American Female", value: "tina" },
          { label: "Josh \u2014 American Male", value: "josh" },
          { label: "Florian", value: "florian" },
          { label: "Derek", value: "derek" },
          { label: "June", value: "june" },
          { label: "Nat", value: "nat" },
          { label: "Paige", value: "paige" }
        ]
      }
    }),
    model: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Model",
      description: '"Base" is the standard model. "Turbo" is faster with lower latency, recommended for time-sensitive conversations.',
      required: !1,
      defaultValue: "base",
      options: {
        options: [
          { label: "Base (standard)", value: "base" },
          { label: "Turbo (lower latency)", value: "turbo" }
        ]
      }
    }),
    language: import_pieces_framework2.Property.ShortText({
      displayName: "Language",
      description: 'Language code for transcription and speech (e.g. "en", "es", "fr"). Defaults to "babel-en" (English with accent tolerance).',
      required: !1
    }),
    maxDuration: import_pieces_framework2.Property.Number({
      displayName: "Max Duration (minutes)",
      description: "Maximum call length in minutes. The call ends automatically after this time. Defaults to 30.",
      required: !1,
      defaultValue: 30
    }),
    waitForGreeting: import_pieces_framework2.Property.Checkbox({
      displayName: "Wait for Greeting",
      description: "When enabled, the AI waits for the recipient to speak first before responding. Useful when calling into IVR systems.",
      required: !1,
      defaultValue: !1
    }),
    record: import_pieces_framework2.Property.Checkbox({
      displayName: "Record Call",
      description: "When enabled, the call is recorded. The recording URL is included in the post-call data.",
      required: !1,
      defaultValue: !1
    }),
    transferPhoneNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Transfer Phone Number",
      description: "E.164 phone number to transfer the call to if the AI decides a human is needed (e.g. +12223334444).",
      required: !1
    }),
    webhook: import_pieces_framework2.Property.ShortText({
      displayName: "Webhook URL",
      description: "URL that receives a POST request with full call data (transcript, summary, recording URL) after the call ends.",
      required: !1
    }),
    summaryPrompt: import_pieces_framework2.Property.LongText({
      displayName: "Summary Prompt",
      description: "Custom instructions for how the post-call summary should be written (max 2000 characters). Leave blank to use the default summary format.",
      required: !1
    })
  },
  async run(context) {
    let {
      phoneNumber,
      task,
      pathwayId,
      firstSentence,
      voice,
      model,
      language,
      maxDuration,
      waitForGreeting,
      record,
      transferPhoneNumber,
      webhook,
      summaryPrompt
    } = context.propsValue, body = {
      phone_number: phoneNumber
    };
    return task && (body.task = task), pathwayId && (body.pathway_id = pathwayId), firstSentence && (body.first_sentence = firstSentence), voice && (body.voice = voice), model && (body.model = model), language && (body.language = language), maxDuration != null && (body.max_duration = maxDuration), waitForGreeting !== void 0 && (body.wait_for_greeting = waitForGreeting), record !== void 0 && (body.record = record), transferPhoneNumber && (body.transfer_phone_number = transferPhoneNumber), webhook && (body.webhook = webhook), summaryPrompt && (body.summary_prompt = summaryPrompt), blandApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/calls",
      body
    });
  }
});

// ../ap/packages/pieces/community/bland-ai/src/lib/actions/get-call-details.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var getCallDetails = (0, import_pieces_framework3.createAction)({
  auth: blandAiAuth,
  name: "get_call_details",
  classification: "READ",
  displayName: "Get Call Details",
  description: "Retrieve details for a specific Bland AI call.",
  audience: "both",
  aiMetadata: {
    description: "Fetches the full record of one Bland AI call by its call ID (status, transcript, summary, recording URL, and post-call data). Use to inspect or poll the outcome of a call after it has been placed. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    callId: import_pieces_framework3.Property.ShortText({
      displayName: "Call ID",
      description: "The Bland AI call identifier.",
      required: !0
    })
  },
  async run(context) {
    return blandApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      path: `/calls/${encodeURIComponent(context.propsValue.callId)}`
    });
  }
});

// ../ap/packages/pieces/community/bland-ai/src/lib/actions/list-calls.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var listCalls = (0, import_pieces_framework4.createAction)({
  auth: blandAiAuth,
  name: "list_calls",
  classification: "SEARCH",
  displayName: "List Calls",
  description: "List recent Bland AI calls with optional filters.",
  audience: "both",
  aiMetadata: {
    description: "Lists recent Bland AI calls, optionally narrowed by origin (from) and/or destination (to) phone number and capped by a limit (1-100). Leave the number filters empty to retrieve the most recent calls regardless of party. Use to discover call IDs or survey call history. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework4.Property.Number({
      displayName: "Limit",
      description: "Maximum number of calls to return.",
      required: !1,
      defaultValue: 10
    }),
    fromNumber: import_pieces_framework4.Property.ShortText({
      displayName: "From Number",
      description: "Optional origin phone number filter.",
      required: !1
    }),
    toNumber: import_pieces_framework4.Property.ShortText({
      displayName: "To Number",
      description: "Optional destination phone number filter.",
      required: !1
    })
  },
  async run(context) {
    let { limit, fromNumber, toNumber } = context.propsValue, safeLimit = Math.max(1, Math.min(100, limit != null ? limit : 10)), query = __spreadValues(__spreadValues({
      limit: String(safeLimit)
    }, fromNumber ? { from_number: fromNumber } : {}), toNumber ? { to_number: toNumber } : {});
    return blandApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.GET,
      path: "/calls",
      query
    });
  }
});

// ../ap/packages/pieces/community/bland-ai/src/index.ts
var blandAi = (0, import_pieces_framework5.createPiece)({
  displayName: "Bland AI",
  description: "AI phone calling platform for outbound and conversational voice workflows.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/bland-ai.png",
  categories: [import_pieces_framework6.PieceCategory.COMMUNICATION],
  auth: blandAiAuth,
  authors: ["Harmatta"],
  actions: [
    sendCall,
    getCallDetails,
    listCalls,
    (0, import_pieces_common6.createCustomApiCallAction)({
      auth: blandAiAuth,
      baseUrl: () => BLAND_AI_BASE_URL,
      authMapping: async (auth) => ({
        authorization: auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  blandAi
});
