"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/bookedin/src/index.ts
var index_exports = {};
__export(index_exports, {
  bookedin: () => bookedin
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bookedin/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bookedin/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var bookedinAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: `To connect your Bookedin account, please follow these steps to retrieve your API Key:

    1. Log in to your Bookedin Dashboard: dashboard.bookedin.ai
    2. From the left menu, go to **Business**
    3. Open **Settings**
    4. Click on **API Key**
    5. Copy your API Key (starts with **sk_\u2026**)
    6. Paste the key below to authorize the integration

    Your API Key allows Activepieces to securely access your Bookedin leads, agents, and booking data.
    `,
  validate: async ({ auth }) => {
    var _a, _b;
    try {
      let apiKey = extractApiKey(auth);
      return apiKey ? (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${BASE_URL}/agents/`,
        headers: {
          "X-API-Key": apiKey,
          accept: "application/json"
        },
        queryParams: {
          skip: "0",
          limit: "1"
        }
      }), { valid: !0 }) : { valid: !1, error: "API Key is empty" };
    } catch (e) {
      return {
        valid: !1,
        error: ((_b = (_a = e == null ? void 0 : e.response) == null ? void 0 : _a.body) == null ? void 0 : _b.detail) || (e == null ? void 0 : e.message) || "Connection failed"
      };
    }
  }
});

// ../ap/packages/pieces/community/bookedin/src/lib/common/props.ts
var BASE_URL = "https://api.bookedin.ai/api/v1", getBookedinHeaders = (apiKey) => ({
  "X-API-Key": apiKey,
  accept: "application/json"
}), extractApiKey = (auth) => {
  if (typeof auth == "string")
    return auth;
  let authObj = auth;
  return (authObj == null ? void 0 : authObj.secret_text) || (authObj == null ? void 0 : authObj.auth) || "";
}, fetchLeadOptions = async (apiKey) => (await import_pieces_common2.httpClient.sendRequest({
  method: import_pieces_common2.HttpMethod.GET,
  url: `${BASE_URL}/leads/`,
  headers: getBookedinHeaders(apiKey),
  queryParams: {
    limit: "100",
    skip: "0"
  }
})).body.items.map((lead) => {
  var _a, _b, _c, _d, _e;
  let firstName = ((_b = (_a = lead.contact) == null ? void 0 : _a.name) == null ? void 0 : _b.first) || "", lastName = ((_d = (_c = lead.contact) == null ? void 0 : _c.name) == null ? void 0 : _d.last) || "", email = ((_e = lead.contact) == null ? void 0 : _e.email) || "", name = [firstName, lastName].filter(Boolean).join(" ").trim();
  return {
    label: name ? `${name} (${email})` : email || lead.id,
    value: lead.id
  };
}), leadIdDropdown = import_pieces_framework2.Property.Dropdown({
  auth: bookedinAuth,
  displayName: "Lead",
  description: "Select a lead",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your account first",
        options: []
      };
    try {
      let apiKey = extractApiKey(auth);
      return apiKey ? {
        disabled: !1,
        options: await fetchLeadOptions(apiKey)
      } : {
        disabled: !0,
        placeholder: "API key is missing",
        options: []
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load leads. Please check your connection.",
        options: []
      };
    }
  }
}), leadIdsMultiSelectDropdown = import_pieces_framework2.Property.MultiSelectDropdown({
  auth: bookedinAuth,
  displayName: "Leads",
  description: "Select leads to delete (max 500)",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your account first",
        options: []
      };
    try {
      let apiKey = extractApiKey(auth);
      return apiKey ? {
        disabled: !1,
        options: await fetchLeadOptions(apiKey)
      } : {
        disabled: !0,
        placeholder: "API key is missing",
        options: []
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load leads. Please check your connection.",
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/bookedin/src/lib/actions/get-leads.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getLeads = (0, import_pieces_framework3.createAction)({
  name: "getLeads",
  classification: "SEARCH",
  displayName: "Get Leads",
  description: "Get all leads for the current business with pagination metadata.",
  audience: "both",
  aiMetadata: { description: "List leads for the authenticated Bookedin business, returning a page with pagination metadata. Use it to browse or find leads; leave the filter fields empty to fetch all, or narrow by free-text search (name/email/phone), exact email, phone, or source, and page through results with limit/skip. Read-only and idempotent.", idempotent: !0 },
  auth: bookedinAuth,
  props: {
    search: import_pieces_framework3.Property.ShortText({
      displayName: "Search",
      description: "Search text in name, email, or phone number",
      required: !1
    }),
    source: import_pieces_framework3.Property.ShortText({
      displayName: "Source",
      description: 'Filter by lead source (e.g., "API", "Import")',
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "Filter by exact email address",
      required: !1
    }),
    phone: import_pieces_framework3.Property.ShortText({
      displayName: "Phone",
      description: "Filter by phone number",
      required: !1
    }),
    limit: import_pieces_framework3.Property.Number({
      displayName: "Limit",
      description: "Number of leads to return",
      required: !1,
      defaultValue: 100
    }),
    skip: import_pieces_framework3.Property.Number({
      displayName: "Skip",
      description: "Number of leads to skip (pagination)",
      required: !1,
      defaultValue: 0
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    let apiKey = extractApiKey(auth), queryParams = {
      limit: ((_a = propsValue.limit) != null ? _a : 100).toString(),
      skip: ((_b = propsValue.skip) != null ? _b : 0).toString()
    };
    return propsValue.search && (queryParams.search = propsValue.search), propsValue.source && (queryParams.source = propsValue.source), propsValue.email && (queryParams.email = propsValue.email), propsValue.phone && (queryParams.phone = propsValue.phone), (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `${BASE_URL}/leads/`,
      headers: getBookedinHeaders(apiKey),
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/bookedin/src/lib/actions/create-lead.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createLead = (0, import_pieces_framework4.createAction)({
  name: "createLead",
  classification: "WRITE",
  displayName: "Create Lead",
  description: "Creates a new lead in Bookedin AI",
  audience: "both",
  aiMetadata: { description: "Create a new lead in Bookedin for the authenticated business from a contact first name, last name, email, and phone (all required). Use it to add a prospect into the lead pipeline. Not idempotent \u2014 each call creates a separate lead, with no de-duplication on email or phone.", idempotent: !1 },
  auth: bookedinAuth,
  props: {
    firstName: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      required: !0
    }),
    lastName: import_pieces_framework4.Property.ShortText({
      displayName: "Last Name",
      required: !0
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      required: !0
    }),
    phone: import_pieces_framework4.Property.ShortText({
      displayName: "Phone Number",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let apiKey = extractApiKey(auth), payload = {
      contact: {
        name: {
          last: propsValue.lastName,
          first: propsValue.firstName
        },
        email: propsValue.email,
        number: propsValue.phone
      }
    };
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${BASE_URL}/leads/`,
      headers: __spreadProps(__spreadValues({}, getBookedinHeaders(apiKey)), {
        "Content-Type": "application/json"
      }),
      body: payload
    })).body;
  }
});

// ../ap/packages/pieces/community/bookedin/src/lib/actions/get-lead.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getLead = (0, import_pieces_framework5.createAction)({
  name: "getLead",
  classification: "READ",
  displayName: "Get Lead",
  description: "Get a specific lead by ID.",
  audience: "both",
  aiMetadata: { description: "Fetch a single lead by its Bookedin lead ID. Use it to read the full record for one known lead; if you only have a name, email, or phone, find the ID first with Get Leads. Read-only and idempotent.", idempotent: !0 },
  auth: bookedinAuth,
  props: {
    lead_id: leadIdDropdown
  },
  async run({ auth, propsValue }) {
    let apiKey = extractApiKey(auth);
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `${BASE_URL}/leads/${propsValue.lead_id}`,
      headers: getBookedinHeaders(apiKey)
    })).body;
  }
});

// ../ap/packages/pieces/community/bookedin/src/lib/actions/delete-lead.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var deleteLead = (0, import_pieces_framework6.createAction)({
  name: "deleteLead",
  classification: "DESTRUCTIVE",
  displayName: "Delete Lead",
  description: "Delete a lead.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a lead by its Bookedin lead ID. Use it to remove a known lead from the pipeline; this is destructive and cannot be undone. Idempotent on the end state \u2014 once the lead is gone, re-running targets an already-removed record.", idempotent: !0 },
  auth: bookedinAuth,
  props: {
    lead_id: leadIdDropdown
  },
  async run({ auth, propsValue }) {
    let apiKey = extractApiKey(auth);
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.DELETE,
      url: `${BASE_URL}/leads/${propsValue.lead_id}`,
      headers: getBookedinHeaders(apiKey)
    })).body;
  }
});

// ../ap/packages/pieces/community/bookedin/src/lib/actions/get-lead-stats.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var getLeadStats = (0, import_pieces_framework7.createAction)({
  name: "getLeadStats",
  classification: "READ",
  displayName: "Get Lead Stats",
  description: "Get lead statistics (Hot, Warm, Cold, Objectives Met, Total).",
  audience: "both",
  aiMetadata: { description: "Get aggregate lead statistics for the authenticated Bookedin business, such as counts of Hot, Warm, and Cold leads, objectives met, and the total. Use it for a quick pipeline summary rather than fetching individual leads. Takes no input; read-only and idempotent.", idempotent: !0 },
  auth: bookedinAuth,
  props: {},
  async run({ auth }) {
    let apiKey = extractApiKey(auth);
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `${BASE_URL}/leads/stats`,
      headers: getBookedinHeaders(apiKey)
    })).body;
  }
});

// ../ap/packages/pieces/community/bookedin/src/lib/actions/update-lead.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var updateLead = (0, import_pieces_framework8.createAction)({
  name: "updateLead",
  classification: "WRITE",
  displayName: "Update Lead",
  description: "Update a lead.",
  audience: "both",
  aiMetadata: { description: "Update an existing lead identified by its Bookedin lead ID. Supply any of the individual contact fields (first/last name, email, phone) and handling status, and/or pass a raw JSON payload for complex updates that merges over those fields. Use it to edit a known lead; only the fields you provide are changed. Idempotent \u2014 re-applying the same values yields the same lead state.", idempotent: !0 },
  auth: bookedinAuth,
  props: {
    lead_id: leadIdDropdown,
    firstName: import_pieces_framework8.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework8.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    phone: import_pieces_framework8.Property.ShortText({
      displayName: "Phone Number",
      required: !1
    }),
    handling_status: import_pieces_framework8.Property.ShortText({
      displayName: "Handling Status",
      required: !1
    }),
    update_json: import_pieces_framework8.Property.Json({
      displayName: "Update Payload (JSON)",
      description: "Optional JSON body for complex updates. Merges with individual fields above.",
      required: !1,
      defaultValue: {}
    })
  },
  async run({ auth, propsValue }) {
    let apiKey = extractApiKey(auth), basePayload = {};
    if (!(0, import_pieces_framework9.isNil)(propsValue.firstName) || !(0, import_pieces_framework9.isNil)(propsValue.lastName) || !(0, import_pieces_framework9.isNil)(propsValue.email) || !(0, import_pieces_framework9.isNil)(propsValue.phone)) {
      let contact = {}, nameParts = {};
      !(0, import_pieces_framework9.isNil)(propsValue.firstName) && propsValue.firstName !== "" && (nameParts.first = propsValue.firstName), !(0, import_pieces_framework9.isNil)(propsValue.lastName) && propsValue.lastName !== "" && (nameParts.last = propsValue.lastName), Object.keys(nameParts).length > 0 && (contact.name = nameParts), !(0, import_pieces_framework9.isNil)(propsValue.email) && propsValue.email !== "" && (contact.email = propsValue.email), !(0, import_pieces_framework9.isNil)(propsValue.phone) && propsValue.phone !== "" && (contact.number = propsValue.phone), basePayload.contact = contact;
    }
    !(0, import_pieces_framework9.isNil)(propsValue.handling_status) && propsValue.handling_status !== "" && (basePayload.handling_status = propsValue.handling_status);
    let finalPayload = __spreadValues(__spreadValues({}, basePayload), propsValue.update_json || {});
    return (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.PUT,
      url: `${BASE_URL}/leads/${propsValue.lead_id}`,
      headers: __spreadProps(__spreadValues({}, getBookedinHeaders(apiKey)), {
        "Content-Type": "application/json"
      }),
      body: finalPayload
    })).body;
  }
});

// ../ap/packages/pieces/community/bookedin/src/index.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var bookedin = (0, import_pieces_framework10.createPiece)({
  displayName: "Bookedin",
  description: "AI agents for lead conversion and appointment booking.",
  logoUrl: "https://cdn.activepieces.com/pieces/bookedin.png",
  categories: [import_pieces_framework11.PieceCategory.SALES_AND_CRM],
  auth: bookedinAuth,
  minimumSupportedRelease: "0.36.1",
  authors: ["drona2938", "onyedikachi-david"],
  actions: [
    getLeads,
    createLead,
    getLead,
    deleteLead,
    getLeadStats,
    updateLead,
    (0, import_pieces_common9.createCustomApiCallAction)({
      baseUrl: () => BASE_URL,
      auth: bookedinAuth,
      authMapping: async (auth) => ({
        "X-API-Key": extractApiKey(auth)
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  bookedin
});
