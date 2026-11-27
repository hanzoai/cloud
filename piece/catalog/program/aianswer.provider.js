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

// ../ap/packages/pieces/community/aianswer/src/index.ts
var index_exports = {};
__export(index_exports, {
  aianswer: () => aianswer
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/aianswer/src/lib/common/models.ts
var aiAnswerConfig = {
  baseUrl: "https://backend-development-e8jn.onrender.com",
  accessTokenHeaderKey: "Authorization"
};

// ../ap/packages/pieces/community/aianswer/src/lib/actions/gmail-get-list-of-agents.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/aianswer/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var aiAnswerAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "AiAnswer API Access Token",
  required: !0,
  description: `
      To obtain your AiAnswer API access token, follow these steps below:
      1. Log in to your AiAnswer account at https://app.aianswer.us .
      2. Navigate to Settings < API Key.
      3. Click on Copy icon to copy your existing Key or click on New API Key to create a new one.
      4. Copy the API Key and paste it below in "AiAnswer API Key".
    `,
  validate: async (auth) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${aiAnswerConfig.baseUrl}/gmail/list_agents`,
        headers: {
          [aiAnswerConfig.accessTokenHeaderKey]: auth.auth
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

// ../ap/packages/pieces/community/aianswer/src/lib/actions/gmail-get-list-of-agents.ts
var gmailGetListOfAgents = (0, import_pieces_framework2.createAction)({
  name: "gmailGetListOfAgents",
  classification: "SEARCH",
  auth: aiAnswerAuth,
  displayName: "Gmail get list of Agents",
  description: "get the lists of agents with Gmail",
  audience: "both",
  aiMetadata: { description: "Lists the available Gmail-connected AI Answer agents on the account, returning their IDs. Use this to discover a valid agent ID before initiating or scheduling a call (those actions require an agent ID). Takes no input; read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `${aiAnswerConfig.baseUrl}/gmail/list_agents`,
      headers: {
        [aiAnswerConfig.accessTokenHeaderKey]: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/aianswer/src/lib/actions/create-phone-call.ts
var import_pieces_common3 = require("@activepieces/pieces-common");
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var createPhoneCall = (0, import_pieces_framework3.createAction)({
  name: "createPhoneCall",
  classification: "WRITE",
  auth: aiAnswerAuth,
  displayName: "Create Phone Call",
  description: "Create a phone call to customer from Agent",
  audience: "both",
  aiMetadata: { description: "Immediately places an outbound phone call from an AI Answer agent to a destination phone number. Use when an agent should call a customer right now; for a future-dated call use the schedule call action instead. Requires a valid agent ID and an E.164-formatted number with country code (e.g. +919876543210); optional key-value details can be attached. Not idempotent \u2014 each call places a new phone call.", idempotent: !1 },
  props: {
    agentID: import_pieces_framework3.Property.ShortText({
      displayName: "Agent ID",
      required: !0
    }),
    phoneNumber: import_pieces_framework3.Property.ShortText({
      displayName: "To Phone Number",
      description: "Enter the phone number, along with country code, in format (e.g., +919876543210)",
      required: !0
    }),
    details: import_pieces_framework3.Property.Object({
      displayName: "Details",
      description: "Optional details with key-value pairs (e.g., customer_id, priority)",
      required: !1
    })
  },
  async run(context) {
    let agentID = context.propsValue.agentID, phoneNumber = context.propsValue.phoneNumber, details = context.propsValue.details || {};
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: `${aiAnswerConfig.baseUrl}/v2/call_agent/${agentID}`,
      headers: {
        [aiAnswerConfig.accessTokenHeaderKey]: context.auth.secret_text
      },
      queryParams: {
        agent_id: agentID,
        to_number: phoneNumber
      },
      body: {
        details
        // Include details object in the request body
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/aianswer/src/lib/actions/get-call-details.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var getCallDetails = (0, import_pieces_framework4.createAction)({
  name: "getCallDetails",
  classification: "READ",
  auth: aiAnswerAuth,
  displayName: "Get Call Details",
  description: "Fetch Call details by Call ID",
  audience: "both",
  aiMetadata: { description: "Retrieves the details and status of a single phone call by its call ID. Use to check the outcome or metadata of a call previously created or scheduled. Requires the call ID; read-only and idempotent.", idempotent: !0 },
  props: {
    callID: import_pieces_framework4.Property.ShortText({
      displayName: "Call ID",
      required: !0
    })
  },
  async run(context) {
    let callID = context.propsValue.callID;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `${aiAnswerConfig.baseUrl}/v2/get_call/${callID}`,
      headers: {
        [aiAnswerConfig.accessTokenHeaderKey]: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/aianswer/src/lib/actions/schedule-call-agent.ts
var import_pieces_common5 = require("@activepieces/pieces-common");
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var scheduleCallAgent = (0, import_pieces_framework5.createAction)({
  name: "scheduleCallAgent",
  classification: "WRITE",
  auth: aiAnswerAuth,
  displayName: "Schedule Call Agent",
  description: "Schedule a call with an agent",
  audience: "both",
  aiMetadata: { description: "Schedules an AI Answer agent to place an outbound call at a future date and time. Use when the call should happen later rather than immediately (for an immediate call use the create phone call action). Requires a valid agent ID, destination phone number, an execution time in YYYY-MM-DD HH:MM:SS format, and a timezone (e.g. Asia/Calcutta); optional prospect details can be attached. Not idempotent \u2014 each call schedules a new call.", idempotent: !1 },
  props: {
    agentID: import_pieces_framework5.Property.ShortText({
      displayName: "Agent ID",
      required: !0
    }),
    phoneNumber: import_pieces_framework5.Property.ShortText({
      displayName: "Phone Number",
      required: !0
    }),
    executionTime: import_pieces_framework5.Property.ShortText({
      displayName: "Execution Time",
      description: "Time to schedule the call in YYYY-MM-DD HH:MM:SS format.",
      required: !0
    }),
    timezone: import_pieces_framework5.Property.ShortText({
      displayName: "Timezone",
      description: "Timezone of the scheduled call (e.g., Asia/Calcutta)",
      required: !0
    }),
    prospectDetails: import_pieces_framework5.Property.Object({
      displayName: "Prospect Details",
      description: "Optional prospect details with key-value pairs (e.g., customer_id, priority)",
      required: !1
    })
  },
  async run(context) {
    let agentID = context.propsValue.agentID, phoneNumber = context.propsValue.phoneNumber, executionTime = context.propsValue.executionTime, timezone = context.propsValue.timezone, prospectDetails = context.propsValue.prospectDetails || {}, requestBody = {
      phone_number: phoneNumber,
      execution_time: executionTime,
      timezone,
      prospect_details: {
        details: prospectDetails
        // Nested details object with custom key-value pairs
      }
    };
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: `${aiAnswerConfig.baseUrl}/v2/schedule_call_agent/${agentID}`,
      headers: {
        [aiAnswerConfig.accessTokenHeaderKey]: context.auth.secret_text
      },
      queryParams: {
        agent_id: agentID
      },
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/aianswer/src/index.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/aianswer/src/lib/actions/get-call-transcript.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var getCallTranscript = (0, import_pieces_framework6.createAction)({
  name: "getCallTranscript",
  classification: "READ",
  auth: aiAnswerAuth,
  // Auth configured here
  displayName: "Get Call Transcript",
  description: "Fetch the transcript of a call by Call ID",
  audience: "both",
  aiMetadata: { description: "Retrieves the conversation transcript of a completed phone call by its call ID. Use to read what was said on a call after it has finished. Requires the call ID; read-only and idempotent.", idempotent: !0 },
  props: {
    callID: import_pieces_framework6.Property.ShortText({
      displayName: "Call ID",
      required: !0
    })
  },
  async run(context) {
    let callID = context.propsValue.callID;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: `${aiAnswerConfig.baseUrl}/v2/get_transcript/${callID}`,
      headers: {
        [aiAnswerConfig.accessTokenHeaderKey]: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/aianswer/src/index.ts
var aianswer = (0, import_pieces_framework7.createPiece)({
  displayName: "AI Answer",
  auth: aiAnswerAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/aianswer.png",
  categories: [
    import_pieces_framework8.PieceCategory.COMMUNICATION,
    import_pieces_framework8.PieceCategory.CUSTOMER_SUPPORT,
    import_pieces_framework8.PieceCategory.ARTIFICIAL_INTELLIGENCE
  ],
  authors: ["drona2938"],
  actions: [
    gmailGetListOfAgents,
    createPhoneCall,
    getCallDetails,
    scheduleCallAgent,
    getCallTranscript,
    (0, import_pieces_common7.createCustomApiCallAction)({
      baseUrl: () => aiAnswerConfig.baseUrl,
      auth: aiAnswerAuth,
      authMapping: async (auth) => ({
        [aiAnswerConfig.accessTokenHeaderKey]: `${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  aianswer
});
