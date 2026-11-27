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

// ../ap/packages/pieces/community/ask-handle/src/index.ts
var index_exports = {};
__export(index_exports, {
  askHandle: () => askHandle
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/ask-handle/src/lib/common/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), BASE_URL = "https://dashboard.askhandle.com/api/v1", askHandleAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To get your API Key:

1. Go to https://dashboard.askhandle.com
2. Sign in to your account
3. Navigate to API settings
4. Create or copy your API token
5. Paste it here
`,
  required: !0,
  validate: async ({ auth }) => {
    if (!auth)
      return {
        valid: !1,
        error: "API key is required"
      };
    try {
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${BASE_URL}/rooms/`,
        headers: {
          Authorization: `Token ${auth}`,
          "Content-Type": "application/json"
        }
      })).status === 200 ? {
        valid: !0
      } : {
        valid: !1,
        error: "Invalid API key"
      };
    } catch (error) {
      return {
        valid: !1,
        error: "Invalid API key. Please check your API key and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/ask-handle/src/lib/actions/create-message.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/ask-handle/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), BASE_URL2 = "https://dashboard.askhandle.com/api/v1";
async function askHandleApiCall(apiKey, method, path, body) {
  var _a;
  try {
    let response = await import_pieces_common2.httpClient.sendRequest({
      method,
      url: `${BASE_URL2}${path}`,
      headers: {
        Authorization: `Token ${apiKey}`,
        "Content-Type": "application/json"
      },
      body
    });
    if (response.status >= 200 && response.status < 300)
      return response.body;
    throw new Error(
      `AskHandle API error: ${response.status} ${JSON.stringify(response.body)}`
    );
  } catch (error) {
    let statusCode = (_a = error.response) == null ? void 0 : _a.status;
    if (statusCode)
      switch (statusCode) {
        case 400:
          throw new Error(
            "Bad Request: Invalid request parameters. Please check your data."
          );
        case 401:
          throw new Error(
            "Authentication Failed: Invalid API key. Please verify your AskHandle credentials."
          );
        case 403:
          throw new Error(
            "Access Forbidden: You do not have permission to access this resource."
          );
        case 404:
          throw new Error(
            "Resource Not Found: The requested resource does not exist."
          );
        case 500:
        case 502:
        case 503:
        case 504:
          throw new Error(
            `Server Error (${statusCode}): AskHandle API is experiencing issues. Please try again later.`
          );
        default:
          throw new Error(
            `AskHandle API Error (${statusCode}): ${error.message || "Unknown error occurred"}`
          );
      }
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/ask-handle/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var roomDropdown = import_pieces_framework2.Property.Dropdown({
  auth: askHandleAuth,
  displayName: "Room",
  description: "Select a room",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      let response = await askHandleApiCall(
        auth.secret_text,
        import_pieces_common3.HttpMethod.GET,
        "/rooms/"
      );
      return {
        disabled: !1,
        options: (Array.isArray(response) ? response : (response == null ? void 0 : response.results) || []).map((room) => ({
          label: room.name || room.label || `Room ${room.uuid}`,
          value: room.uuid
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading rooms"
      };
    }
  }
}), leadDropdown = import_pieces_framework2.Property.Dropdown({
  auth: askHandleAuth,
  displayName: "Lead",
  description: "Select a lead",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first"
      };
    try {
      let response = await askHandleApiCall(
        auth.secret_text,
        import_pieces_common3.HttpMethod.GET,
        "/leads/"
      );
      return {
        disabled: !1,
        options: (Array.isArray(response) ? response : (response == null ? void 0 : response.results) || []).map((lead) => ({
          label: lead.nickname || lead.email || `Lead ${lead.uuid}`,
          value: lead.uuid
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading leads"
      };
    }
  }
});

// ../ap/packages/pieces/community/ask-handle/src/lib/actions/create-message.ts
var createMessage = (0, import_pieces_framework3.createAction)({
  auth: askHandleAuth,
  name: "create_message",
  classification: "WRITE",
  displayName: "Create Message",
  description: "Send a message to a room",
  audience: "both",
  aiMetadata: { description: "Posts a message to a specific AskHandle chat room, identified by its room UUID. Use to send a reply or note into an existing conversation; optionally attach a sender nickname, email, and phone number. Not idempotent \u2014 each call appends a new message.", idempotent: !1 },
  props: {
    room: roomDropdown,
    body: import_pieces_framework3.Property.LongText({
      displayName: "Message",
      description: "The message content",
      required: !0
    }),
    nickname: import_pieces_framework3.Property.ShortText({
      displayName: "Nickname",
      description: "Sender nickname",
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "Sender email",
      required: !1
    }),
    phone_number: import_pieces_framework3.Property.ShortText({
      displayName: "Phone Number",
      description: "Sender phone number",
      required: !1
    })
  },
  async run(context) {
    let { room, body, nickname, email, phone_number } = context.propsValue, payload = {
      body,
      room: {
        uuid: room
      }
    };
    return nickname && (payload.nickname = nickname), email && (payload.email = email), phone_number && (payload.phone_number = phone_number), await askHandleApiCall(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/messages/",
      payload
    );
  }
});

// ../ap/packages/pieces/community/ask-handle/src/lib/actions/create-lead.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createLead = (0, import_pieces_framework4.createAction)({
  auth: askHandleAuth,
  name: "create_lead",
  classification: "WRITE",
  displayName: "Create Lead",
  description: "Create a new lead",
  audience: "both",
  aiMetadata: { description: "Creates a new lead record in AskHandle from optional contact and context fields (nickname, email, phone, device, originating page title, referrer). Use to capture a prospect. Not idempotent \u2014 each call creates a separate lead, with no de-duplication on email or phone.", idempotent: !1 },
  props: {
    nickname: import_pieces_framework4.Property.ShortText({
      displayName: "Nickname",
      description: "Lead nickname",
      required: !1
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      description: "Lead email",
      required: !1
    }),
    phone_number: import_pieces_framework4.Property.ShortText({
      displayName: "Phone Number",
      description: "Lead phone number",
      required: !1
    }),
    device: import_pieces_framework4.Property.ShortText({
      displayName: "Device",
      description: "Device information",
      required: !1
    }),
    from_page_title: import_pieces_framework4.Property.ShortText({
      displayName: "From Page Title",
      description: "Page title where lead originated",
      required: !1
    }),
    referrer: import_pieces_framework4.Property.ShortText({
      displayName: "Referrer",
      description: "Referrer URL",
      required: !1
    })
  },
  async run(context) {
    let {
      nickname,
      email,
      phone_number,
      device,
      from_page_title,
      referrer
    } = context.propsValue, payload = {};
    return nickname && (payload.nickname = nickname), email && (payload.email = email), phone_number && (payload.phone_number = phone_number), device && (payload.device = device), from_page_title && (payload.from_page_title = from_page_title), referrer && (payload.referrer = referrer), await askHandleApiCall(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      "/leads/",
      payload
    );
  }
});

// ../ap/packages/pieces/community/ask-handle/src/lib/actions/list-rooms.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var listRooms = (0, import_pieces_framework5.createAction)({
  auth: askHandleAuth,
  name: "list_rooms",
  classification: "SEARCH",
  displayName: "List Rooms",
  description: "Get a list of all rooms",
  audience: "both",
  aiMetadata: { description: "Retrieves all chat rooms in the AskHandle account. Use to discover available rooms or look up a room UUID before sending a message. Takes no input and is a read-only lookup, so it is idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    return await askHandleApiCall(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.GET,
      "/rooms/"
    );
  }
});

// ../ap/packages/pieces/community/ask-handle/src/lib/actions/list-leads.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var listLeads = (0, import_pieces_framework6.createAction)({
  auth: askHandleAuth,
  name: "list_leads",
  classification: "SEARCH",
  displayName: "List Leads",
  description: "Get a list of all leads",
  audience: "both",
  aiMetadata: { description: "Retrieves leads from AskHandle, optionally narrowed by a start date, end date, and a maximum result limit; with no filters set it returns all leads. Use to query captured prospects over a time window. Read-only lookup, so it is idempotent.", idempotent: !0 },
  props: {
    start_date: import_pieces_framework6.Property.DateTime({
      displayName: "Start Date",
      description: "Filter leads from this date",
      required: !1
    }),
    end_date: import_pieces_framework6.Property.DateTime({
      displayName: "End Date",
      description: "Filter leads until this date",
      required: !1
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Maximum number of leads to return",
      required: !1
    })
  },
  async run(context) {
    let { start_date, end_date, limit } = context.propsValue, queryParams = [];
    if (start_date) {
      let dateStr = new Date(start_date).toISOString().split("T")[0];
      queryParams.push(`start_date=${dateStr}`);
    }
    if (end_date) {
      let dateStr = new Date(end_date).toISOString().split("T")[0];
      queryParams.push(`end_date=${dateStr}`);
    }
    limit && queryParams.push(`limit=${limit}`);
    let path = queryParams.length > 0 ? `/leads/?${queryParams.join("&")}` : "/leads/";
    return await askHandleApiCall(
      context.auth.secret_text,
      import_pieces_common7.HttpMethod.GET,
      path
    );
  }
});

// ../ap/packages/pieces/community/ask-handle/src/lib/triggers/new-message.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var BASE_URL3 = "https://dashboard.askhandle.com/api/v1", newMessageTrigger = (0, import_pieces_framework7.createTrigger)({
  auth: askHandleAuth,
  name: "new_message",
  classification: "READ",
  displayName: "New Message",
  description: "Triggers when a new message is received",
  aiMetadata: {
    description: "Fires when a new message is added to any AskHandle chat room, including who sent it and whether it came from a support agent. Use to react to incoming conversation activity in real time."
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    uuid: "ffb84155-5f12-4bee-bd50-3c868097e473",
    nickname: "Mary",
    email: "mary@example.com",
    body: "Hello!",
    is_support_sender: !1,
    sent_at: "2021-09-15T12:08:50.676405Z"
  },
  async onEnable(context) {
    try {
      let response = await import_pieces_common8.httpClient.sendRequest({
        method: import_pieces_common8.HttpMethod.POST,
        url: `${BASE_URL3}/webhooks/`,
        headers: {
          Authorization: `Token ${context.auth}`,
          "Content-Type": "application/json"
        },
        body: {
          event: "message.added",
          target: context.webhookUrl
        }
      });
      if (response.status === 200 || response.status === 201) {
        let webhook = response.body;
        await context.store.put("_askhandle_webhook_message", webhook.uuid);
      }
    } catch (error) {
      throw new Error(
        `Failed to enable webhook: ${error.message || String(error)}`
      );
    }
  },
  async onDisable(context) {
    try {
      let webhookId = await context.store.get(
        "_askhandle_webhook_message"
      );
      webhookId && (await import_pieces_common8.httpClient.sendRequest({
        method: import_pieces_common8.HttpMethod.DELETE,
        url: `${BASE_URL3}/webhooks/${webhookId}/`,
        headers: {
          Authorization: `Token ${context.auth}`,
          "Content-Type": "application/json"
        }
      }), await context.store.delete("_askhandle_webhook_message"));
    } catch (error) {
      console.warn("Failed to delete webhook during disable:", error);
    }
  },
  async run(context) {
    let payload = context.payload.body;
    return [payload.data || payload];
  }
});

// ../ap/packages/pieces/community/ask-handle/src/lib/triggers/new-lead.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var BASE_URL4 = "https://dashboard.askhandle.com/api/v1", newLeadTrigger = (0, import_pieces_framework8.createTrigger)({
  auth: askHandleAuth,
  name: "new_lead",
  classification: "READ",
  displayName: "New Lead",
  description: "Triggers when a new lead is created",
  aiMetadata: {
    description: "Fires when a new lead is captured in AskHandle, carrying the contact details and origin context (page, referrer, device). Use to kick off follow-up when a prospect is registered."
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    uuid: "d8a2e337-9d4d-4515-979a-6f590379848f",
    nickname: "John",
    email: "john@example.com",
    phone_number: "+1234567890",
    device: "Desktop",
    from_page_title: "Homepage",
    referrer: "https://example.com",
    created_at: "2021-09-15T12:08:50.676405Z"
  },
  async onEnable(context) {
    try {
      let response = await import_pieces_common9.httpClient.sendRequest({
        method: import_pieces_common9.HttpMethod.POST,
        url: `${BASE_URL4}/webhooks/`,
        headers: {
          Authorization: `Token ${context.auth}`,
          "Content-Type": "application/json"
        },
        body: {
          event: "lead.added",
          target: context.webhookUrl
        }
      });
      if (response.status === 200 || response.status === 201) {
        let webhook = response.body;
        await context.store.put("_askhandle_webhook_lead", webhook.uuid);
      }
    } catch (error) {
      throw new Error(
        `Failed to enable webhook: ${error.message || String(error)}`
      );
    }
  },
  async onDisable(context) {
    try {
      let webhookId = await context.store.get(
        "_askhandle_webhook_lead"
      );
      webhookId && (await import_pieces_common9.httpClient.sendRequest({
        method: import_pieces_common9.HttpMethod.DELETE,
        url: `${BASE_URL4}/webhooks/${webhookId}/`,
        headers: {
          Authorization: `Token ${context.auth}`,
          "Content-Type": "application/json"
        }
      }), await context.store.delete("_askhandle_webhook_lead"));
    } catch (error) {
      console.warn("Failed to delete webhook during disable:", error);
    }
  },
  async run(context) {
    let payload = context.payload.body;
    return [payload.data || payload];
  }
});

// ../ap/packages/pieces/community/ask-handle/src/lib/triggers/new-room.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var BASE_URL5 = "https://dashboard.askhandle.com/api/v1", newRoomTrigger = (0, import_pieces_framework9.createTrigger)({
  auth: askHandleAuth,
  name: "new_room",
  classification: "READ",
  displayName: "New Room",
  description: "Triggers when a new room is created",
  aiMetadata: {
    description: "Fires when a new chat room (conversation) is created in AskHandle, including its label, name, and rating. Use to react when a fresh conversation thread begins."
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    uuid: "de1e39a5-a391-4d7f-836d-cf3589529af8",
    label: "room-label-123",
    name: "Customer Support Room",
    rating: 5,
    is_bot_use: !1,
    created_at: "2021-09-15T12:08:50.676405Z",
    messages: []
  },
  async onEnable(context) {
    try {
      let response = await import_pieces_common10.httpClient.sendRequest({
        method: import_pieces_common10.HttpMethod.POST,
        url: `${BASE_URL5}/webhooks/`,
        headers: {
          Authorization: `Token ${context.auth}`,
          "Content-Type": "application/json"
        },
        body: {
          event: "chat.added",
          target: context.webhookUrl
        }
      });
      if (response.status === 200 || response.status === 201) {
        let webhook = response.body;
        await context.store.put("_askhandle_webhook_room", webhook.uuid);
      }
    } catch (error) {
      throw new Error(
        `Failed to enable webhook: ${error.message || String(error)}`
      );
    }
  },
  async onDisable(context) {
    try {
      let webhookId = await context.store.get(
        "_askhandle_webhook_room"
      );
      webhookId && (await import_pieces_common10.httpClient.sendRequest({
        method: import_pieces_common10.HttpMethod.DELETE,
        url: `${BASE_URL5}/webhooks/${webhookId}/`,
        headers: {
          Authorization: `Token ${context.auth}`,
          "Content-Type": "application/json"
        }
      }), await context.store.delete("_askhandle_webhook_room"));
    } catch (error) {
      console.warn("Failed to delete webhook during disable:", error);
    }
  },
  async run(context) {
    let payload = context.payload.body;
    return [payload.data || payload];
  }
});

// ../ap/packages/pieces/community/ask-handle/src/index.ts
var askHandle = (0, import_pieces_framework10.createPiece)({
  displayName: "AskHandle",
  auth: askHandleAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/ask-handle.png",
  authors: ["onyedikachi-david"],
  actions: [
    createMessage,
    createLead,
    listRooms,
    listLeads
  ],
  triggers: [
    newMessageTrigger,
    newLeadTrigger,
    newRoomTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  askHandle
});
