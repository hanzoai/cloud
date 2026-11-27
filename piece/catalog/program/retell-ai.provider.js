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

// ../ap/packages/pieces/community/retell-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  retellAi: () => retellAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/retell-ai/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/retell-ai/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function retellAiApiCall({
  method,
  url,
  body,
  auth
}) {
  var _a, _b, _c;
  let request = {
    method,
    url: `https://api.retellai.com${url}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: auth.props.apiKey
    },
    headers: {
      "Content-Type": "application/json",
      accept: "application/json"
    },
    body
  };
  try {
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  } catch (error) {
    let err = error, status = (_a = err.response) == null ? void 0 : _a.status, apiMessage = ((_c = (_b = err.response) == null ? void 0 : _b.body) == null ? void 0 : _c.message) || "No details provided";
    switch (status) {
      case 400:
        throw new Error(
          `Bad Request: The request format is incorrect or missing required data. Please check your input values. Details: ${apiMessage}`
        );
      case 401:
        throw new Error(
          `Authentication Error: Your API key is invalid or expired. Please check your Retell AI API key in the connection settings. Details: ${apiMessage}`
        );
      case 403:
        throw new Error(
          `Access Forbidden: Your API key doesn't have permission for this operation. Please check your Retell AI account permissions. Details: ${apiMessage}`
        );
      case 404:
        throw new Error(
          `Resource Not Found: The requested resource doesn't exist in your Retell AI account. Details: ${apiMessage}`
        );
      case 409:
        throw new Error(
          `Conflict: The resource already exists or there's a data conflict. Details: ${apiMessage}`
        );
      case 429:
        throw new Error(
          `Rate Limit Exceeded: Too many requests to Retell AI API. Please wait a moment before trying again. Details: ${apiMessage}`
        );
      case 500:
        throw new Error(
          `Server Error: Retell AI is experiencing technical difficulties. Please try again later or contact Retell AI support. Details: ${apiMessage}`
        );
      default: {
        let fallbackMessage = err.message || "Unknown error occurred";
        throw new Error(
          `Retell AI API Error (Status ${status || "Unknown"}): ${apiMessage || fallbackMessage}`
        );
      }
    }
  }
}

// ../ap/packages/pieces/community/retell-ai/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), RetellAiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your Retell AI API Key.",
  required: !0
}), retellAiAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `
  Please follow these steps to get your Retell AI API key:
  
  1. Log in to your Retell AI dashboard.
  2. Navigate to the API section.
  3. Generate a new API key or copy your existing one.
  4. Use this API key to authenticate your requests.`,
  props: {
    apiKey: RetellAiAuth
  },
  validate: async ({ auth }) => {
    try {
      return await retellAiApiCall({
        method: import_pieces_common2.HttpMethod.GET,
        url: "/list-chat",
        auth: {
          type: import_pieces_framework2.AppConnectionType.CUSTOM_AUTH,
          props: auth
        }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key"
      };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/retell-ai/src/lib/actions/make-phone-call.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/retell-ai/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var agentIdDropdown = (displayName, required = !1) => import_pieces_framework3.Property.Dropdown({
  auth: retellAiAuth,
  displayName,
  description: "Select the Retell AI agent.",
  required,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Connect your Retell AI account first"
      };
    try {
      let agents = await retellAiApiCall({
        auth,
        method: import_pieces_common3.HttpMethod.GET,
        url: "/list-agents",
        body: {
          limit: 100
        }
      }), agentList = Array.isArray(agents) ? agents : [];
      return agentList.length === 0 ? {
        disabled: !0,
        options: [],
        placeholder: "No agents found in your workspace."
      } : {
        disabled: !1,
        options: agentList.map((agent) => ({
          label: `${agent.agent_name || "Unnamed Agent"} (${agent.agent_id})`,
          value: agent.agent_id
        }))
      };
    } catch (error) {
      let errorMessage = error instanceof Error ? error.message : "Unknown error occurred";
      return {
        disabled: !0,
        options: [],
        placeholder: `Error loading agents: ${errorMessage}`
      };
    }
  }
}), callIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: retellAiAuth,
  displayName: "Call ID",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Connect your Retell AI account first"
      };
    try {
      let response = await retellAiApiCall({
        auth,
        method: import_pieces_common3.HttpMethod.POST,
        url: "/v2/list-calls",
        body: {
          limit: 50,
          sort_order: "descending"
        }
      });
      return !response || response.length === 0 ? {
        disabled: !0,
        options: [],
        placeholder: "No calls found in your workspace."
      } : {
        disabled: !1,
        options: response.map((call) => ({
          label: `${call.call_id} (${call.call_status} - ${call.call_type})`,
          value: call.call_id
        }))
      };
    } catch (error) {
      let errorMessage = error instanceof Error ? error.message : "Unknown error occurred";
      return {
        disabled: !0,
        options: [],
        placeholder: `Error loading calls: ${errorMessage}`
      };
    }
  }
}), voiceIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: retellAiAuth,
  displayName: "Voice",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Connect your Retell AI account first"
      };
    try {
      let voices = await retellAiApiCall({
        auth,
        method: import_pieces_common3.HttpMethod.GET,
        url: "/list-voices"
      });
      return voices.length === 0 ? {
        disabled: !0,
        options: [],
        placeholder: "No voices found in your workspace."
      } : {
        disabled: !1,
        options: voices.map((voice) => {
          let voiceInfo = voice.accent ? `${voice.gender}, ${voice.accent}` : voice.gender;
          return {
            label: `${voice.voice_name} (${voiceInfo})`,
            value: voice.voice_id
          };
        })
      };
    } catch (error) {
      let errorMessage = error instanceof Error ? error.message : "Unknown error occurred";
      return {
        disabled: !0,
        options: [],
        placeholder: `Error loading voices: ${errorMessage}`
      };
    }
  }
}), numberProviderDropdown = import_pieces_framework3.Property.StaticDropdown({
  displayName: "Number Provider",
  description: "The provider to purchase the phone number from.",
  required: !1,
  options: {
    options: [
      { label: "Twilio", value: "twilio" },
      { label: "Telnyx", value: "telnyx" }
    ]
  },
  defaultValue: "twilio"
}), languageDropdown = import_pieces_framework3.Property.StaticDropdown({
  displayName: "Language",
  description: "The language for the agent to use.",
  required: !1,
  options: {
    options: [
      { label: "English (US)", value: "en-US" },
      { label: "English (UK)", value: "en-GB" },
      { label: "Spanish", value: "es-ES" },
      { label: "French", value: "fr-FR" },
      { label: "German", value: "de-DE" },
      { label: "Italian", value: "it-IT" },
      { label: "Portuguese", value: "pt-PT" },
      { label: "Dutch", value: "nl-NL" },
      { label: "Japanese", value: "ja-JP" },
      { label: "Chinese (Mandarin)", value: "zh-CN" }
    ]
  },
  defaultValue: "en-US"
}), sttModeDropdown = import_pieces_framework3.Property.StaticDropdown({
  displayName: "Speech-to-Text Mode",
  description: "The speech-to-text mode for the agent.",
  required: !1,
  options: {
    options: [
      { label: "Fast", value: "fast" },
      { label: "Standard", value: "standard" }
    ]
  },
  defaultValue: "fast"
}), denoisingModeDropdown = import_pieces_framework3.Property.StaticDropdown({
  displayName: "Denoising Mode",
  description: "The denoising mode for audio processing.",
  required: !1,
  options: {
    options: [
      { label: "Noise Cancellation", value: "noise-cancellation" },
      { label: "None", value: "none" }
    ]
  },
  defaultValue: "noise-cancellation"
});

// ../ap/packages/pieces/community/retell-ai/src/lib/actions/make-phone-call.ts
var makePhoneCall = (0, import_pieces_framework4.createAction)({
  auth: retellAiAuth,
  name: "make_phone_call",
  displayName: "Make Phone Call",
  description: "Initiate a new outbound phone call using Retell AI agents.",
  audience: "both",
  aiMetadata: { description: "Place an outbound phone call from a Retell-owned/imported number to a destination number, dialed by a Retell AI voice agent. Use to start a live AI-driven call. Both numbers must be in E.164 format; the agent version, metadata, dynamic prompt variables, and custom SIP headers can optionally be overridden per call. Not idempotent: each call places a new outbound call.", idempotent: !1 },
  props: {
    fromNumber: import_pieces_framework4.Property.ShortText({
      displayName: "From Number",
      description: "The number you own in E.164 format. Must be a number purchased from Retell or imported to Retell.",
      required: !0
    }),
    toNumber: import_pieces_framework4.Property.ShortText({
      displayName: "To Number",
      description: "The number you want to call, in E.164 format. If using a number purchased from Retell, only US numbers are supported as destination.",
      required: !0
    }),
    overrideAgentId: agentIdDropdown("Agent"),
    overrideAgentVersion: import_pieces_framework4.Property.Number({
      displayName: "Override Agent Version",
      description: "For this particular call, override the agent version used with this version. This does not bind the agent version to this number, this is for one time override.",
      required: !1
    }),
    metadata: import_pieces_framework4.Property.Object({
      displayName: "Metadata",
      description: "An arbitrary object for storage purpose only. You can put anything here like your internal customer id associated with the call.",
      required: !1
    }),
    dynamicVariables: import_pieces_framework4.Property.Object({
      displayName: "Dynamic Variables",
      description: "Add optional dynamic variables in key value pairs of string that injects into your Response Engine prompt and tool description. Only applicable for Response Engine.",
      required: !1
    }),
    customSipHeaders: import_pieces_framework4.Property.Object({
      displayName: "Custom SIP Headers",
      description: "Add optional custom SIP headers to the call.",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let {
      fromNumber,
      toNumber,
      overrideAgentId,
      overrideAgentVersion,
      metadata,
      dynamicVariables,
      customSipHeaders
    } = propsValue, e164Regex = /^\+[1-9]\d{1,14}$/;
    if (!e164Regex.test(fromNumber))
      throw new Error("From Number must be in E.164 format (e.g., +14157774444)");
    if (!e164Regex.test(toNumber))
      throw new Error("To Number must be in E.164 format (e.g., +12137774445)");
    let body = {
      from_number: fromNumber,
      to_number: toNumber
    };
    return overrideAgentId && (body.override_agent_id = overrideAgentId), overrideAgentVersion !== void 0 && (body.override_agent_version = overrideAgentVersion), metadata && (body.metadata = metadata), dynamicVariables && (body.retell_llm_dynamic_variables = dynamicVariables), customSipHeaders && (body.custom_sip_headers = customSipHeaders), await retellAiApiCall({
      method: import_pieces_common4.HttpMethod.POST,
      url: "/v2/create-phone-call",
      auth,
      body
    });
  }
});

// ../ap/packages/pieces/community/retell-ai/src/lib/actions/create-phone-number.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var createPhoneNumber = (0, import_pieces_framework5.createAction)({
  auth: retellAiAuth,
  name: "create_phone_number",
  displayName: "Create Phone Number",
  description: "Buys a new phone number and binds agents.",
  audience: "both",
  aiMetadata: { description: "Purchase a new US/Canada phone number from Retell and bind inbound and/or outbound AI agents to it. Either request a number by area code or pass a specific number in E.164 format; toll-free can be requested. Use to provision a callable number for an agent. Not idempotent: each call purchases a new number and incurs cost.", idempotent: !1 },
  props: {
    inboundAgentId: agentIdDropdown("Inbound Agent"),
    outboundAgentId: agentIdDropdown("Outbound Agent"),
    inboundAgentVersion: import_pieces_framework5.Property.Number({
      displayName: "Inbound Agent Version",
      description: "Version of the inbound agent to bind to the number. If not provided, will default to latest version.",
      required: !1
    }),
    outboundAgentVersion: import_pieces_framework5.Property.Number({
      displayName: "Outbound Agent Version",
      description: "Version of the outbound agent to bind to the number. If not provided, will default to latest version.",
      required: !1
    }),
    areaCode: import_pieces_framework5.Property.Number({
      displayName: "Area Code",
      description: "Area code of the number to obtain. Format is a 3 digit integer. Currently only supports US area code.",
      required: !1
    }),
    nickname: import_pieces_framework5.Property.ShortText({
      displayName: "Nickname",
      description: "Nickname of the number. This is for your reference only.",
      required: !0
    }),
    inboundWebhookUrl: import_pieces_framework5.Property.ShortText({
      displayName: "Inbound Webhook URL",
      description: "If set, will send a webhook for inbound calls, where you can to override agent id, set dynamic variables and other fields specific to that call.",
      required: !1
    }),
    numberProvider: numberProviderDropdown,
    countryCode: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Country Code",
      description: "The country code for the phone number",
      required: !1,
      defaultValue: "US",
      options: {
        options: [
          { label: "United States", value: "US" },
          { label: "Canada", value: "CA" }
        ]
      }
    }),
    tollFree: import_pieces_framework5.Property.Checkbox({
      displayName: "Toll Free",
      description: "Whether to purchase a toll-free number. Toll-free numbers incur higher costs.",
      required: !1,
      defaultValue: !1
    }),
    phoneNumber: import_pieces_framework5.Property.ShortText({
      displayName: "Phone Number",
      description: "The number you are trying to purchase in E.164 format of the number (+country code then number with no space and no special characters).",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let {
      inboundAgentId,
      outboundAgentId,
      inboundAgentVersion,
      outboundAgentVersion,
      areaCode,
      nickname,
      inboundWebhookUrl,
      numberProvider,
      countryCode,
      tollFree,
      phoneNumber
    } = propsValue;
    if (phoneNumber && !/^\+[1-9]\d{1,14}$/.test(phoneNumber))
      throw new Error("Phone Number must be in E.164 format (e.g., +14157774444)");
    let body = {
      nickname
    };
    return inboundAgentId !== void 0 && (body.inbound_agent_id = inboundAgentId || null), outboundAgentId !== void 0 && (body.outbound_agent_id = outboundAgentId || null), inboundAgentVersion !== void 0 && (body.inbound_agent_version = inboundAgentVersion), outboundAgentVersion !== void 0 && (body.outbound_agent_version = outboundAgentVersion), areaCode !== void 0 && (body.area_code = areaCode), inboundWebhookUrl !== void 0 && (body.inbound_webhook_url = inboundWebhookUrl), numberProvider !== void 0 && (body.number_provider = numberProvider), countryCode !== void 0 && (body.country_code = countryCode), tollFree !== void 0 && (body.toll_free = tollFree), phoneNumber !== void 0 && (body.phone_number = phoneNumber), await retellAiApiCall({
      method: import_pieces_common5.HttpMethod.POST,
      url: "/create-phone-number",
      auth,
      body
    });
  }
});

// ../ap/packages/pieces/community/retell-ai/src/lib/actions/get-call.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var getCall = (0, import_pieces_framework6.createAction)({
  auth: retellAiAuth,
  name: "get_call",
  displayName: "Get Call",
  description: "Retrieve detailed data of a specific call (e.g., transcript), given a Call ID.",
  audience: "both",
  aiMetadata: { description: "Look up the full details of a single Retell call by its Call ID, including status, transcript, recording, and metadata. Use to inspect the outcome or content of a known call. Read-only and idempotent.", idempotent: !0 },
  props: {
    callId: callIdDropdown
  },
  async run({ propsValue, auth }) {
    let { callId } = propsValue;
    if (!callId)
      throw new Error("Call ID is required");
    return await retellAiApiCall({
      method: import_pieces_common6.HttpMethod.GET,
      url: `/v2/get-call/${callId}`,
      auth
    });
  }
});

// ../ap/packages/pieces/community/retell-ai/src/lib/actions/get-phone-number.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var getPhoneNumber = (0, import_pieces_framework7.createAction)({
  auth: retellAiAuth,
  name: "get_phone_number",
  displayName: "Get Phone Number",
  description: "Retrieve full details for an existing phone number in Retell AI.",
  audience: "both",
  aiMetadata: { description: "Look up the configuration of a phone number registered in Retell, including its bound inbound/outbound agents and webhook settings. The number, supplied in E.164 format, is the unique identifier. Use to inspect an existing number before reusing or updating it. Read-only and idempotent.", idempotent: !0 },
  props: {
    phoneNumber: import_pieces_framework7.Property.ShortText({
      displayName: "Phone Number",
      description: 'E.164 format of the number (+country code, then number with no space, no special characters), used as the unique identifier for phone number APIs. Example: "+14157774444"',
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let { phoneNumber } = propsValue;
    if (!/^\+[1-9]\d{1,14}$/.test(phoneNumber))
      throw new Error("Phone Number must be in E.164 format (e.g., +14157774444)");
    return await retellAiApiCall({
      method: import_pieces_common7.HttpMethod.GET,
      url: `/get-phone-number/${encodeURIComponent(phoneNumber)}`,
      auth
    });
  }
});

// ../ap/packages/pieces/community/retell-ai/src/lib/actions/get-voice.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var getVoice = (0, import_pieces_framework8.createAction)({
  auth: retellAiAuth,
  name: "get_voice",
  displayName: "Get Voice",
  description: "Retrieve details for a specific voice model or configuration by ID in Retell AI.",
  audience: "both",
  aiMetadata: { description: "Look up the details of a single Retell voice by its Voice ID, such as provider, accent, gender, and preview. Use to inspect a voice before assigning it to an agent. Read-only and idempotent.", idempotent: !0 },
  props: {
    voiceId: voiceIdDropdown
  },
  async run({ propsValue, auth }) {
    let { voiceId } = propsValue;
    if (!voiceId)
      throw new Error("Voice ID is required");
    return await retellAiApiCall({
      method: import_pieces_common8.HttpMethod.GET,
      url: `/get-voice/${encodeURIComponent(voiceId)}`,
      auth
    });
  }
});

// ../ap/packages/pieces/community/retell-ai/src/lib/actions/get-agent.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var getAgent = (0, import_pieces_framework9.createAction)({
  auth: retellAiAuth,
  name: "get_agent",
  displayName: "Get Agent",
  description: "Fetch details of a Retell AI agent by Agent ID.",
  audience: "both",
  aiMetadata: { description: "Look up the configuration of a single Retell AI agent by its Agent ID, optionally pinned to a specific version (defaults to the latest). Use to inspect an agent's settings before placing a call or binding it to a number. Read-only and idempotent.", idempotent: !0 },
  props: {
    agentId: agentIdDropdown("Agent ID", !0),
    version: import_pieces_framework9.Property.Number({
      displayName: "Version",
      description: "Optional version of the API to use for this request. If not provided, will default to latest version.",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let { agentId, version } = propsValue;
    if (!agentId)
      throw new Error("Agent ID is required");
    let url = `/get-agent/${encodeURIComponent(agentId)}`;
    return version !== void 0 && (url += `?version=${version}`), await retellAiApiCall({
      method: import_pieces_common9.HttpMethod.GET,
      url,
      auth
    });
  }
});

// ../ap/packages/pieces/community/retell-ai/src/lib/triggers/new-call.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var newCallTrigger = (0, import_pieces_framework10.createTrigger)({
  name: "new_call",
  displayName: "New Call",
  description: "Triggers when a new outgoing or incoming call is created.",
  aiMetadata: {
    description: "Fires when a new call is created in Retell, for either inbound or outbound direction, via the call.created webhook event. Each event represents a freshly initiated call with its call ID, numbers, direction, agent, and metadata. Requires a manually configured webhook in the Retell dashboard."
  },
  auth: retellAiAuth,
  props: {
    webhookInstructions: import_pieces_framework10.Property.MarkDown({
      value: `
      To use this trigger, you need to manually set up a webhook in your Retell AI account:

      1. Login to your Retell AI dashboard.
      2. Navigate to the Webhooks section in your settings.
      3. Click on "Add Webhook" or "Create New Webhook".
      4. Add the following URL in the **Webhook URL** field:
      \`\`\`text
      {{webhookUrl}}
      \`\`\`
      5. Select the **call.created** event type.
      6. Click Save to create the webhook.
      `
    })
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  sampleData: {
    call_id: "119c3f8e47135a29e65947eeb34cf12d",
    status: "initiated",
    from_number: "+15551234567",
    to_number: "+15557654321",
    direction: "outbound",
    agent_id: "ag_01h9pc3e7jnb2rvnj5jkxgf5d4",
    created_at: "2023-09-15T14:30:00.000Z",
    metadata: {
      custom_field: "value"
    }
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/retell-ai/src/index.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), retellAi = (0, import_pieces_framework11.createPiece)({
  displayName: "Retell AI",
  auth: retellAiAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/retell-ai.png",
  authors: ["aryel780"],
  categories: [import_pieces_framework12.PieceCategory.BUSINESS_INTELLIGENCE, import_pieces_framework12.PieceCategory.PRODUCTIVITY, import_pieces_framework12.PieceCategory.COMMUNICATION],
  actions: [
    makePhoneCall,
    createPhoneNumber,
    getCall,
    getPhoneNumber,
    getVoice,
    getAgent,
    (0, import_pieces_common10.createCustomApiCallAction)({
      auth: retellAiAuth,
      baseUrl: () => "https://api.retellai.com",
      authMapping: async (auth) => {
        let { apiKey } = auth.props;
        return {
          Authorization: `Bearer ${apiKey}`
        };
      }
    })
  ],
  triggers: [newCallTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  retellAi
});
