"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __objRest = (source, exclude) => {
  var target = {};
  for (var prop in source)
    __hasOwnProp.call(source, prop) && exclude.indexOf(prop) < 0 && (target[prop] = source[prop]);
  if (source != null && __getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(source))
      exclude.indexOf(prop) < 0 && __propIsEnum.call(source, prop) && (target[prop] = source[prop]);
  return target;
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

// ../ap/packages/pieces/community/pollybot-ai/src/index.ts
var index_exports = {};
__export(index_exports, {
  pollybotAi: () => pollybotAi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pollybot-ai/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), pollybotAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Connect to your PollyBot instance",
  required: !0,
  props: {
    apiKey: import_pieces_framework.Property.LongText({
      displayName: "PollyBot API Key",
      description: "Your API Key starting with `sk-workspace_...`. Find your API Key in your [PollyBot Dashboard Settings](https://pollybot.app/docs/authentication).",
      required: !0
    }),
    chatbotId: import_pieces_framework.Property.ShortText({
      displayName: "Chatbot ID",
      description: "The unique ID for the specific chatbot (e.g., `cmhm6o40a05h8mn12dv3tc456`). Learn more in the [PollyBot Authentication docs](https://pollybot.app/docs/authentication).",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    var _a, _b;
    let url = `https://pollybot.app/api/v1/chatbots/${auth.chatbotId}/leads`;
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url,
        headers: {
          Authorization: `Bearer ${auth.apiKey}`,
          "Content-Type": "application/json"
        },
        queryParams: {
          limit: "1"
          // Minimal fetch for validation
        }
      }), { valid: !0 };
    } catch (e) {
      let error = e;
      return {
        valid: !1,
        error: `Authentication failed: ${((_b = (_a = error == null ? void 0 : error.response) == null ? void 0 : _a.body) == null ? void 0 : _b.error) || error.message || "Unknown error"}`
      };
    }
  }
});

// ../ap/packages/pieces/community/pollybot-ai/src/lib/actions/create-lead.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/pollybot-ai/src/lib/common/common.ts
var baseUrl = "https://pollybot.app/api/v1", leadStatusOptions = {
  NEW: "NEW",
  CONTACTED: "CONTACTED",
  QUALIFIED: "QUALIFIED",
  NEGOTIATING: "NEGOTIATING",
  CONVERTED: "CONVERTED",
  LOST: "LOST",
  UNRESPONSIVE: "UNRESPONSIVE"
}, leadPriorityOptions = {
  LOW: "LOW",
  MEDIUM: "MEDIUM",
  HIGH: "HIGH",
  URGENT: "URGENT"
}, preferredMethodOptions = {
  email: "email",
  phone: "phone",
  discord: "discord"
}, urgencyOptions = {
  low: "low",
  medium: "medium",
  high: "high"
};
function formatError(e) {
  var _a, _b;
  let error = e, status = (_a = error.response) == null ? void 0 : _a.status, errorData = ((_b = error.response) == null ? void 0 : _b.body) || {}, message = errorData.error || error.message || "Unknown Error", code = errorData.code ? ` (${errorData.code})` : "", details = errorData.details ? ` Details: ${JSON.stringify(errorData.details)}` : "";
  return `Error [${status}]${code}: ${message}.${details}`;
}

// ../ap/packages/pieces/community/pollybot-ai/src/lib/actions/create-lead.ts
var createLead = (0, import_pieces_framework2.createAction)({
  name: "create_lead",
  displayName: "Create Lead",
  description: "Creates a new lead in your PollyBot chatbot.",
  audience: "both",
  aiMetadata: {
    description: "Create a new lead record under the configured PollyBot chatbot. Requires name. Automatically assigns status NEW and priority MEDIUM.",
    idempotent: !1
  },
  auth: pollybotAuth,
  props: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      required: !0,
      description: "Lead's full name (1-100 characters)"
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      required: !1,
      description: "Valid email address. Must be unique for this chatbot."
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone",
      required: !1,
      description: "The lead's phone number (maximum 20 characters)."
    }),
    discord: import_pieces_framework2.Property.ShortText({
      displayName: "Discord",
      required: !1,
      description: "The lead's Discord username or handle (maximum 50 characters)."
    }),
    company: import_pieces_framework2.Property.ShortText({
      displayName: "Company",
      required: !1,
      description: "The company or organization name (maximum 100 characters)."
    }),
    message: import_pieces_framework2.Property.LongText({
      displayName: "Message",
      required: !1,
      description: "The inquiry or message submitted by the lead (maximum 1000 characters)."
    }),
    preferredMethod: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Preferred Method",
      required: !1,
      options: {
        options: Object.entries(preferredMethodOptions).map(
          ([value, label]) => ({ label, value })
        )
      },
      description: "The contact method preferred by the lead.",
      defaultValue: "email"
    }),
    urgency: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Urgency",
      required: !1,
      options: {
        options: Object.entries(urgencyOptions).map(([value, label]) => ({
          label,
          value
        }))
      },
      description: "The urgency level reported by the lead.",
      defaultValue: "low"
    }),
    source: import_pieces_framework2.Property.ShortText({
      displayName: "Source",
      required: !1,
      description: 'The origin of the lead (maximum 50 characters, defaults to "api").'
    }),
    customFields: import_pieces_framework2.Property.Json({
      displayName: "Custom Fields",
      required: !1,
      description: 'Custom data as a valid JSON object (e.g., {"plan": "enterprise"})'
    }),
    tags: import_pieces_framework2.Property.Array({
      displayName: "Tags",
      required: !1,
      description: "Tags attached to the lead (maximum 10 tags, each up to 50 characters)."
    })
  },
  async run({ auth, propsValue }) {
    let requestBody = Object.fromEntries(
      Object.entries(propsValue).filter(
        ([_, v]) => v != null && v !== ""
      )
    );
    try {
      let response = await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.POST,
        url: `${baseUrl}/chatbots/${auth.props.chatbotId}/leads`,
        headers: {
          Authorization: `Bearer ${auth.props.apiKey}`
        },
        body: requestBody
      });
      return response.body.data || response.body;
    } catch (e) {
      throw new Error(formatError(e));
    }
  }
});

// ../ap/packages/pieces/community/pollybot-ai/src/lib/actions/get-lead.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getLead = (0, import_pieces_framework3.createAction)({
  // auth: check https://www.activepieces.com/docs/developers/piece-reference/authentication,
  name: "get_Lead",
  displayName: "Get Lead",
  description: "Retrieves a specific lead by ID.",
  audience: "both",
  aiMetadata: { description: "Fetch a single lead from the configured PollyBot chatbot by its unique lead ID. Use when you already have a lead ID and need its current details. Read-only and idempotent.", idempotent: !0 },
  auth: pollybotAuth,
  props: {
    id: import_pieces_framework3.Property.ShortText({
      displayName: "Lead ID",
      required: !0,
      description: "The unique identifier of the lead to retrieve."
    })
  },
  async run({ auth, propsValue }) {
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.GET,
        url: `${baseUrl}/chatbots/${auth.props.chatbotId}/leads/${propsValue.id}`,
        headers: {
          Authorization: `Bearer ${auth.props.apiKey}`
        }
      });
      return response.body.data || response.body;
    } catch (e) {
      throw new Error(formatError(e));
    }
  }
});

// ../ap/packages/pieces/community/pollybot-ai/src/lib/actions/update-lead.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var updateLead = (0, import_pieces_framework4.createAction)({
  name: "update_lead",
  displayName: "Update Lead",
  description: "Updates an existing lead. Supports partial updates.",
  audience: "both",
  aiMetadata: {
    description: "Update an existing lead in the configured PollyBot chatbot. Replaces provided fields, leaves omitted fields intact. Requires at least one field to change.",
    idempotent: !0
  },
  auth: pollybotAuth,
  props: {
    id: import_pieces_framework4.Property.ShortText({
      displayName: "Lead ID",
      required: !0,
      description: "The unique ID of the lead to update."
    }),
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Name",
      required: !1,
      description: "The full name of the lead (1-100 characters)."
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      required: !1,
      description: "Valid email address. Must be unique for this chatbot."
    }),
    phone: import_pieces_framework4.Property.ShortText({
      displayName: "Phone",
      required: !1,
      description: "The lead's phone number (maximum 20 characters)."
    }),
    discord: import_pieces_framework4.Property.ShortText({
      displayName: "Discord",
      required: !1,
      description: "The lead's Discord username or handle (maximum 50 characters)."
    }),
    company: import_pieces_framework4.Property.ShortText({
      displayName: "Company",
      required: !1,
      description: "The company or organization name (maximum 100 characters)."
    }),
    message: import_pieces_framework4.Property.LongText({
      displayName: "Message",
      required: !1,
      description: "The inquiry or message submitted by the lead (maximum 1000 characters)."
    }),
    preferredMethod: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Preferred Method",
      required: !1,
      options: {
        options: Object.entries(preferredMethodOptions).map(
          ([value, label]) => ({ label, value })
        )
      },
      description: "The contact method preferred by the lead."
    }),
    urgency: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Urgency",
      required: !1,
      options: {
        options: Object.entries(urgencyOptions).map(([value, label]) => ({
          label,
          value
        }))
      },
      description: "The urgency level reported by the lead."
    }),
    status: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Status",
      required: !1,
      options: {
        options: Object.entries(leadStatusOptions).map(([value, label]) => ({
          label,
          value
        }))
      },
      description: "Update the pipeline status of the lead."
    }),
    priority: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Priority",
      required: !1,
      options: {
        options: Object.entries(leadPriorityOptions).map(([value, label]) => ({
          label,
          value
        }))
      },
      description: "Update the priority level of the lead."
    }),
    notes: import_pieces_framework4.Property.LongText({
      displayName: "Notes",
      required: !1,
      description: "Internal notes regarding the lead (maximum 2000 characters)."
    }),
    customFields: import_pieces_framework4.Property.Json({
      displayName: "Custom Fields",
      required: !1,
      description: "Replaces the stored customFields object entirely."
    }),
    tags: import_pieces_framework4.Property.Array({
      displayName: "Tags",
      required: !1,
      description: "Replaces the stored tags array entirely."
    })
  },
  async run({ auth, propsValue }) {
    let _a = propsValue, { id } = _a, fieldsToUpdate = __objRest(_a, ["id"]), requestBody = Object.fromEntries(
      Object.entries(fieldsToUpdate).filter(
        ([_, v]) => v != null && v !== ""
      )
    );
    if (Object.keys(requestBody).length === 0)
      throw new Error("At least one field must be provided to update.");
    try {
      let response = await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.PUT,
        url: `${baseUrl}/chatbots/${auth.props.chatbotId}/leads/${id}`,
        headers: {
          Authorization: `Bearer ${auth.props.apiKey}`
        },
        body: requestBody
      });
      return response.body.data || response.body;
    } catch (e) {
      throw new Error(formatError(e));
    }
  }
});

// ../ap/packages/pieces/community/pollybot-ai/src/lib/actions/delete-lead.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var deleteLead = (0, import_pieces_framework5.createAction)({
  name: "delete_lead",
  displayName: "Delete Lead",
  description: "Permanently deletes a lead from your PollyBot chatbot.",
  audience: "both",
  aiMetadata: {
    description: "Permanently delete a lead from the configured PollyBot chatbot by its unique lead ID. Use only when a lead should be removed for good; this cannot be undone. Idempotent on the end state once the lead is gone, though a repeat call on a missing ID may error.",
    idempotent: !0
  },
  auth: pollybotAuth,
  props: {
    id: import_pieces_framework5.Property.ShortText({
      displayName: "Lead ID",
      required: !0,
      description: "The unique identifier of the lead to delete."
    })
  },
  async run({ auth, propsValue }) {
    try {
      return (await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.DELETE,
        url: `${baseUrl}/chatbots/${auth.props.chatbotId}/leads/${propsValue.id}`,
        headers: {
          Authorization: `Bearer ${auth.props.apiKey}`
        }
      })).body;
    } catch (e) {
      throw new Error(formatError(e));
    }
  }
});

// ../ap/packages/pieces/community/pollybot-ai/src/lib/actions/list-leads.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var listLeads = (0, import_pieces_framework6.createAction)({
  name: "list_leads",
  displayName: "List Leads",
  description: "Retrieves a list of leads with optional filtering.",
  audience: "both",
  aiMetadata: {
    description: "List leads for the configured PollyBot chatbot. Supports pagination, search, and filtering.",
    idempotent: !0
  },
  auth: pollybotAuth,
  props: {
    page: import_pieces_framework6.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 1
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      required: !1,
      defaultValue: 50,
      description: "The maximum number of leads to return (max 100)"
    }),
    status: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Status",
      required: !1,
      options: {
        options: Object.entries(leadStatusOptions).map(([value, label]) => ({
          label,
          value
        }))
      },
      description: "Filter leads by their current pipeline status."
    }),
    priority: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Priority",
      required: !1,
      options: {
        options: Object.entries(leadPriorityOptions).map(([value, label]) => ({
          label,
          value
        }))
      },
      description: "Filter leads by their assigned priority level."
    }),
    search: import_pieces_framework6.Property.ShortText({
      displayName: "Search",
      required: !1,
      description: "Case-insensitive substring search matching name, email, or company."
    }),
    sortBy: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Sort By",
      required: !1,
      defaultValue: "createdAt",
      options: {
        options: [
          { label: "Created At", value: "createdAt" },
          { label: "Updated At", value: "updatedAt" },
          { label: "Name", value: "name" },
          { label: "Email", value: "email" },
          { label: "Status", value: "status" },
          { label: "Priority", value: "priority" }
        ]
      },
      description: "Field to sort the retrieved leads by."
    }),
    sortOrder: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Sort Order",
      required: !1,
      defaultValue: "desc",
      options: {
        options: [
          { label: "Descending", value: "desc" },
          { label: "Ascending", value: "asc" }
        ]
      },
      description: "Direction to sort results."
    })
  },
  async run({ auth, propsValue }) {
    let { page, limit, status, priority, search, sortBy, sortOrder } = propsValue, queryParams = {
      page: (page != null ? page : 1).toString(),
      limit: Math.min(limit != null ? limit : 50, 100).toString()
    };
    status && (queryParams.status = status), priority && (queryParams.priority = priority), search && (queryParams.search = search), sortBy && (queryParams.sortBy = sortBy), sortOrder && (queryParams.sortOrder = sortOrder);
    try {
      let response = await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.GET,
        url: `${baseUrl}/chatbots/${auth.props.chatbotId}/leads`,
        headers: {
          Authorization: `Bearer ${auth.props.apiKey}`
        },
        queryParams
      }), data = response.body.data || response.body;
      return Array.isArray(data.leads) ? data.leads : [];
    } catch (e) {
      throw new Error(formatError(e));
    }
  }
});

// ../ap/packages/pieces/community/pollybot-ai/src/index.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pollybot-ai/src/lib/triggers/new-lead.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var newLead = (0, import_pieces_framework7.createTrigger)({
  auth: pollybotAuth,
  name: "newLead",
  displayName: "New Lead",
  description: "Triggers when a new lead is created in PollyBot AI chatbot.",
  aiMetadata: {
    description: "Fires when a new lead is created in the specified PollyBot AI chatbot (the LEAD_CREATED webhook event), delivering the new lead record. Events for other chatbots are filtered out by the configured Chatbot ID."
  },
  props: {
    chatbotid: import_pieces_framework7.Property.ShortText({
      displayName: "Chatbot ID",
      description: "The Id of the chatbot to monitor for new leads.",
      required: !0
    }),
    instruction: import_pieces_framework7.Property.MarkDown({
      value: `## PollyBot AI Webhook Setup
To use this trigger, you need to manually set up a webhook in your PollyBot AI account:

1. Login to your PollyBot AI account.
2. Navigate to the **Chatbots** section from the left navigation menu.
3. Select the desired chatbot for which you want to set up the webhook.
4. Go to the **Settings** tab.
5. Find the **Webhooks** section and click **Add Webhook**.
6. Choose the **Lead Created** event and specify the following URL:
\`\`\`text
{{webhookUrl}}
\`\`\`
7. Click Save to register the webhook.`
    })
  },
  sampleData: {
    data: {
      id: "cmipr3rf400t3n42y5plvmhd5",
      chatbotId: "cmipnh1je00sxn42y1j34wqnd",
      conversationId: null,
      name: "Jane Cooper",
      email: "jane.cooper@acme.com",
      phone: "+14155550132",
      company: "Acme Inc.",
      message: "Interested in the enterprise plan.",
      source: "api",
      status: "NEW",
      priority: "MEDIUM",
      assignedAgentId: null,
      notes: null,
      tags: ["enterprise", "pricing"],
      customFields: { plan: "enterprise" },
      followUpDate: null,
      lastContactAt: null,
      convertedAt: null,
      createdAt: "2026-08-19T09:14:32.118Z",
      updatedAt: "2026-08-19T09:14:32.118Z",
      discord: null,
      preferredMethod: "email",
      urgency: "low",
      chatbots: {
        id: "cmipnh1je00sxn42y1j34wqnd",
        name: "Support Bot"
      }
    },
    event: "LEAD_CREATED",
    chatbotId: "cmipnh1je00sxn42y1j34wqnd",
    timestamp: 1764750871
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return context.payload.body.data.chatbotId !== context.propsValue.chatbotid ? [] : [context.payload.body];
  }
});

// ../ap/packages/pieces/community/pollybot-ai/src/index.ts
var pollybotAi = (0, import_pieces_framework8.createPiece)({
  displayName: "PollyBot AI",
  description: "Automate lead management with PollyBot AI chatbot integration.",
  auth: pollybotAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/pollybot-ai.png",
  categories: [import_pieces_framework9.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  authors: ["Trayshmhirk", "sanket-a11y"],
  actions: [createLead, getLead, updateLead, deleteLead, listLeads],
  triggers: [newLead]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pollybotAi
});
