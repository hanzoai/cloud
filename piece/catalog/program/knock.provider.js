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

// ../ap/packages/pieces/community/knock/src/index.ts
var index_exports = {};
__export(index_exports, {
  knock: () => knock
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/knock/src/lib/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), KNOCK_API_BASE_URL = "https://api.knock.app/v1", knockAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Knock Secret API Key",
  description: "Your Knock secret API key (starts with sk_). Found under Developer > API Keys in the Knock dashboard.",
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b;
    if (!auth)
      return { valid: !1, error: "API key is required." };
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${KNOCK_API_BASE_URL}/users?page_size=1`,
        headers: knockHeaders(auth)
      }), { valid: !0 };
    } catch (e) {
      let err = e;
      return ((_a = err.response) == null ? void 0 : _a.status) === 401 || ((_b = err.response) == null ? void 0 : _b.status) === 403 ? {
        valid: !1,
        error: "Invalid API key or insufficient permissions."
      } : {
        valid: !1,
        error: `Connection failed: ${String(e).slice(0, 100)}`
      };
    }
  }
});
function knockHeaders(apiKey) {
  return {
    Authorization: `Bearer ${apiKey}`,
    "Content-Type": "application/json",
    Accept: "application/json"
  };
}

// ../ap/packages/pieces/community/knock/src/lib/actions/trigger-workflow.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/knock/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
async function knockApiCall(params) {
  let { apiKey, method, path, body, query } = params, url = new URL(`${KNOCK_API_BASE_URL}${path}`);
  if (query) {
    for (let [key, value] of Object.entries(query))
      if (value !== void 0)
        if (Array.isArray(value))
          for (let v of value)
            url.searchParams.append(key, v);
        else
          url.searchParams.set(key, value);
  }
  let request = {
    method,
    url: url.toString(),
    headers: knockHeaders(apiKey),
    body: body != null ? body : void 0
  };
  return (await import_pieces_common2.httpClient.sendRequest(request)).body;
}

// ../ap/packages/pieces/community/knock/src/lib/actions/trigger-workflow.ts
var triggerWorkflow = (0, import_pieces_framework2.createAction)({
  auth: knockAuth,
  name: "trigger_workflow",
  displayName: "Trigger Workflow",
  description: "Trigger a Knock notification workflow for one or more recipients.",
  audience: "both",
  aiMetadata: {
    description: "Triggers a Knock notification workflow (identified by its workflow key) to deliver notifications to one or more recipient user IDs, optionally attaching template data, an actor, and a tenant. Choose this to send notifications to users; the recipients must already exist in Knock or be identified inline. Not idempotent \u2014 each call enqueues a fresh notification run.",
    idempotent: !1
  },
  props: {
    workflowKey: import_pieces_framework2.Property.ShortText({
      displayName: "Workflow Key",
      description: 'The key of the workflow to trigger (e.g. "new-comment").',
      required: !0
    }),
    recipients: import_pieces_framework2.Property.Array({
      displayName: "Recipient IDs",
      description: "One or more user IDs to receive the notification.",
      required: !0
    }),
    actorId: import_pieces_framework2.Property.ShortText({
      displayName: "Actor ID",
      description: "The user ID of the person performing the action (optional).",
      required: !1
    }),
    data: import_pieces_framework2.Property.Object({
      displayName: "Data",
      description: "Key-value data passed to the workflow template (optional).",
      required: !1
    }),
    tenant: import_pieces_framework2.Property.ShortText({
      displayName: "Tenant",
      description: "Optional tenant identifier for multi-tenant workflows.",
      required: !1
    })
  },
  async run(context) {
    let { workflowKey, recipients, actorId, data, tenant } = context.propsValue, body = {
      recipients
    };
    return actorId && (body.actor = actorId), data && Object.keys(data).length > 0 && (body.data = data), tenant && (body.tenant = tenant), knockApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      path: `/workflows/${encodeURIComponent(workflowKey)}/trigger`,
      body
    });
  }
});

// ../ap/packages/pieces/community/knock/src/lib/actions/identify-user.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var identifyUser = (0, import_pieces_framework3.createAction)({
  auth: knockAuth,
  name: "identify_user",
  displayName: "Identify User",
  description: "Create or update a user in Knock.",
  audience: "both",
  aiMetadata: {
    description: "Creates or updates (upserts) a Knock user identified by a stable user ID, setting profile fields such as name, email, phone, avatar, and arbitrary custom properties. Choose this to register or refresh a recipient before sending notifications. Idempotent \u2014 repeating with the same user ID and inputs converges to the same user record.",
    idempotent: !0
  },
  props: {
    userId: import_pieces_framework3.Property.ShortText({
      displayName: "User ID",
      description: "A unique identifier for the user in your system.",
      required: !0
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Name",
      description: "The user display name.",
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "The user email address.",
      required: !1
    }),
    phoneNumber: import_pieces_framework3.Property.ShortText({
      displayName: "Phone Number",
      description: "Phone number in E.164 format (e.g. +14155552671).",
      required: !1
    }),
    avatar: import_pieces_framework3.Property.ShortText({
      displayName: "Avatar URL",
      description: "URL to the user avatar image.",
      required: !1
    }),
    customProperties: import_pieces_framework3.Property.Object({
      displayName: "Custom Properties",
      description: "Additional key-value properties to set on the user.",
      required: !1
    })
  },
  async run(context) {
    let { userId, name, email, phoneNumber, avatar, customProperties } = context.propsValue, body = {};
    return customProperties && Object.keys(customProperties).length > 0 && Object.assign(body, customProperties), name && (body.name = name), email && (body.email = email), phoneNumber && (body.phone_number = phoneNumber), avatar && (body.avatar = avatar), knockApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.PUT,
      path: `/users/${encodeURIComponent(userId)}`,
      body
    });
  }
});

// ../ap/packages/pieces/community/knock/src/lib/actions/get-user.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var getUser = (0, import_pieces_framework4.createAction)({
  auth: knockAuth,
  name: "get_user",
  displayName: "Get User",
  description: "Retrieve a user from Knock by their ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single Knock user record by its unique user ID. Choose this to look up an existing recipient profile. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    userId: import_pieces_framework4.Property.ShortText({
      displayName: "User ID",
      description: "The unique identifier of the user to retrieve.",
      required: !0
    })
  },
  async run(context) {
    return knockApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.GET,
      path: `/users/${encodeURIComponent(context.propsValue.userId)}`
    });
  }
});

// ../ap/packages/pieces/community/knock/src/lib/actions/delete-user.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var deleteUser = (0, import_pieces_framework5.createAction)({
  auth: knockAuth,
  name: "delete_user",
  displayName: "Delete User",
  description: "Delete a user from Knock by their ID.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a Knock user identified by its unique user ID. Choose this to remove a recipient from Knock. Destructive and not idempotent \u2014 the first call removes the user and a repeat call fails to find it.",
    idempotent: !1
  },
  props: {
    userId: import_pieces_framework5.Property.ShortText({
      displayName: "User ID",
      description: "The unique identifier of the user to delete.",
      required: !0
    })
  },
  async run(context) {
    return knockApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.DELETE,
      path: `/users/${encodeURIComponent(context.propsValue.userId)}`
    });
  }
});

// ../ap/packages/pieces/community/knock/src/lib/actions/get-message.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var getMessage = (0, import_pieces_framework6.createAction)({
  auth: knockAuth,
  name: "get_message",
  displayName: "Get Message",
  description: "Retrieve a Knock message by its ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single Knock message (a delivered or queued notification instance) by its unique message ID. Choose this to inspect the status or contents of one notification. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    messageId: import_pieces_framework6.Property.ShortText({
      displayName: "Message ID",
      description: "The unique identifier of the message.",
      required: !0
    })
  },
  async run(context) {
    return knockApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      path: `/messages/${encodeURIComponent(context.propsValue.messageId)}`
    });
  }
});

// ../ap/packages/pieces/community/knock/src/lib/actions/list-messages.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var listMessages = (0, import_pieces_framework7.createAction)({
  auth: knockAuth,
  name: "list_messages",
  displayName: "List Messages",
  description: "List Knock messages with optional filters and pagination.",
  audience: "both",
  aiMetadata: {
    description: "Lists Knock messages (notification instances), optionally narrowed by channel, delivery status, source workflow, or tenant; with no filters it returns all messages. Choose this to find messages or audit delivery. Supports cursor pagination via the after cursor and a page size clamped to 1-50. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    channel_id: import_pieces_framework7.Property.ShortText({
      displayName: "Channel ID",
      description: "Filter messages by channel ID.",
      required: !1
    }),
    status: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter messages by delivery status.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Queued", value: "queued" },
          { label: "Sent", value: "sent" },
          { label: "Delivered", value: "delivered" },
          { label: "Bounced", value: "bounced" },
          { label: "Undelivered", value: "undelivered" },
          { label: "Not sent", value: "not_sent" }
        ]
      }
    }),
    source: import_pieces_framework7.Property.ShortText({
      displayName: "Workflow Key",
      description: "Filter by the workflow that generated the message.",
      required: !1
    }),
    tenant: import_pieces_framework7.Property.ShortText({
      displayName: "Tenant",
      description: "Filter messages by tenant.",
      required: !1
    }),
    page_size: import_pieces_framework7.Property.Number({
      displayName: "Page Size",
      description: "Number of messages per page (1\u201350, default 50).",
      required: !1,
      defaultValue: 50
    }),
    after: import_pieces_framework7.Property.ShortText({
      displayName: "After Cursor",
      description: "Cursor for forward pagination.",
      required: !1
    })
  },
  async run(context) {
    let { channel_id, status, source, tenant, page_size, after } = context.propsValue, rawPageSize = page_size != null ? page_size : 50, clampedPageSize = Math.max(1, Math.min(50, rawPageSize)), query = {
      channel_id: channel_id || void 0,
      status: status || void 0,
      source: source || void 0,
      tenant: tenant || void 0,
      page_size: String(clampedPageSize),
      after: after || void 0
    };
    return knockApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.GET,
      path: "/messages",
      query
    });
  }
});

// ../ap/packages/pieces/community/knock/src/index.ts
var knock = (0, import_pieces_framework8.createPiece)({
  displayName: "Knock",
  description: "Notification infrastructure for developers. Manage users, trigger workflows, and track messages.",
  auth: knockAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/knock.png",
  categories: [import_pieces_framework9.PieceCategory.COMMUNICATION],
  authors: ["Harmatta"],
  actions: [
    triggerWorkflow,
    identifyUser,
    getUser,
    deleteUser,
    getMessage,
    listMessages,
    (0, import_pieces_common9.createCustomApiCallAction)({
      baseUrl: () => KNOCK_API_BASE_URL,
      auth: knockAuth,
      authMapping: async (auth) => knockHeaders(auth.secret_text)
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  knock
});
