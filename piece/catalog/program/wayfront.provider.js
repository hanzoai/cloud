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

// ../ap/packages/pieces/community/wayfront/src/index.ts
var index_exports = {};
__export(index_exports, {
  wayfront: () => wayfront
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework20 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/wayfront/src/lib/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/wayfront/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
function buildBaseUrl(workspaceUrl) {
  return `https://${workspaceUrl.replace(/^https?:\/\//, "").replace(/\/$/, "")}/api`;
}
async function makeRequest({
  workspaceUrl,
  apiToken,
  method,
  path,
  body,
  queryParams
}) {
  let baseUrl = buildBaseUrl(workspaceUrl);
  return import_pieces_common.httpClient.sendRequest({
    method,
    url: `${baseUrl}${path}`,
    headers: {
      Authorization: `Bearer ${apiToken}`,
      "Content-Type": "application/json"
    },
    body,
    queryParams
  });
}
async function validateAuth(workspaceUrl, apiToken) {
  try {
    return await makeRequest({
      workspaceUrl,
      apiToken,
      method: import_pieces_common.HttpMethod.GET,
      path: "/team",
      queryParams: { limit: "1" }
    }), { success: !0 };
  } catch (e) {
    return { success: !1, error: "Invalid workspace URL or API token." };
  }
}
function wayfrontApiClient(workspaceUrl, apiToken) {
  return {
    get: (path, queryParams) => makeRequest({ workspaceUrl, apiToken, method: import_pieces_common.HttpMethod.GET, path, queryParams }),
    post: (path, body) => makeRequest({ workspaceUrl, apiToken, method: import_pieces_common.HttpMethod.POST, path, body }),
    put: (path, body) => makeRequest({ workspaceUrl, apiToken, method: import_pieces_common.HttpMethod.PUT, path, body }),
    delete: (path) => makeRequest({ workspaceUrl, apiToken, method: import_pieces_common.HttpMethod.DELETE, path }),
    validateAuth: () => validateAuth(workspaceUrl, apiToken)
  };
}
var teamMembersDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Assigned To",
  description: "Select a team member to assign this activity to.",
  refreshers: [],
  auth: wayfrontAuth,
  required: !1,
  options: async ({ auth }) => {
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your account first." };
    let { workspaceUrl, apiToken } = auth;
    try {
      return {
        disabled: !1,
        options: (await wayfrontApiClient(workspaceUrl, apiToken).get("/team", { limit: "100" })).body.data.map((member) => ({
          label: member.name ? `${member.name} (${member.email})` : member.email,
          value: member.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load team members. Check your connection."
      };
    }
  }
}), clientsDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Client",
  description: "Select a client from your Wayfront workspace.",
  refreshers: [],
  auth: wayfrontAuth,
  required: !0,
  options: async ({ auth }) => {
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your account first." };
    let { workspaceUrl, apiToken } = auth;
    try {
      return {
        disabled: !1,
        options: (await wayfrontApiClient(workspaceUrl, apiToken).get("/clients", { limit: "100" })).body.data.map((client) => ({
          label: client.name ? `${client.name} (${client.email})` : client.email,
          value: client.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load clients. Check your connection."
      };
    }
  }
}), activitiesDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Activity",
  description: "Select an activity for the chosen client.",
  refreshers: ["user_id"],
  auth: wayfrontAuth,
  required: !0,
  options: async ({ auth, user_id }) => {
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your account first." };
    if (!user_id)
      return { disabled: !0, options: [], placeholder: "Please select a client first." };
    let { workspaceUrl, apiToken } = auth;
    try {
      return {
        disabled: !1,
        options: (await wayfrontApiClient(workspaceUrl, apiToken).get(`/clients/${user_id}/activities`, { limit: "100" })).body.data.map((activity) => ({
          label: `#${activity.id}: ${activity.content.length > 60 ? activity.content.slice(0, 60) + "\u2026" : activity.content}`,
          value: activity.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load activities. Check your connection."
      };
    }
  }
}), ticketsDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Ticket",
  description: "Select a ticket from your Wayfront workspace.",
  refreshers: [],
  auth: wayfrontAuth,
  required: !0,
  options: async ({ auth }) => {
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your account first." };
    let { workspaceUrl, apiToken } = auth;
    try {
      return {
        disabled: !1,
        options: (await wayfrontApiClient(workspaceUrl, apiToken).get("/tickets", { limit: "100" })).body.data.map((ticket) => {
          var _a, _b;
          return {
            label: `#${(_a = ticket.number) != null ? _a : ticket.id}: ${ticket.subject}`,
            value: (_b = ticket.number) != null ? _b : String(ticket.id)
          };
        })
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load tickets. Check your connection."
      };
    }
  }
});
function flattenUser(user) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t, _u, _v, _w, _x, _y, _z;
  return {
    id: user.id,
    name: user.name,
    first_name: user.name_f,
    last_name: user.name_l,
    email: user.email,
    company: (_a = user.company) != null ? _a : null,
    phone: (_b = user.phone) != null ? _b : null,
    tax_id: (_c = user.tax_id) != null ? _c : null,
    affiliate_link: (_d = user.aff_link) != null ? _d : null,
    note: (_e = user.note) != null ? _e : null,
    balance: (_f = user.balance) != null ? _f : null,
    optin: (_g = user.optin) != null ? _g : null,
    stripe_id: (_h = user.stripe_id) != null ? _h : null,
    status: (_i = user.status) != null ? _i : null,
    status_id: (_j = user.status_id) != null ? _j : null,
    role_id: (_k = user.role_id) != null ? _k : null,
    referrer_user_id: (_l = user.referrer_user_id) != null ? _l : null,
    employee_id: (_m = user.employee_id) != null ? _m : null,
    spent: (_n = user.spent) != null ? _n : null,
    address_line_1: (_p = (_o = user.address) == null ? void 0 : _o.line_1) != null ? _p : null,
    address_line_2: (_r = (_q = user.address) == null ? void 0 : _q.line_2) != null ? _r : null,
    address_city: (_t = (_s = user.address) == null ? void 0 : _s.city) != null ? _t : null,
    address_state: (_v = (_u = user.address) == null ? void 0 : _u.state) != null ? _v : null,
    address_postcode: (_x = (_w = user.address) == null ? void 0 : _w.postcode) != null ? _x : null,
    address_country: (_z = (_y = user.address) == null ? void 0 : _y.country) != null ? _z : null,
    team_member_ids: Array.isArray(user.team_member_ids) ? user.team_member_ids.join(", ") : null,
    team_owner_ids: Array.isArray(user.team_owner_ids) ? user.team_owner_ids.join(", ") : null,
    created_at: user.created_at
  };
}
function flattenActivity(activity) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n;
  return {
    id: activity.id,
    content: activity.content,
    scheduled_at: (_a = activity.scheduled_at) != null ? _a : null,
    completed_at: (_b = activity.completed_at) != null ? _b : null,
    created_at: activity.created_at,
    updated_at: activity.updated_at,
    author_id: (_d = (_c = activity.author) == null ? void 0 : _c.id) != null ? _d : null,
    author_name: (_f = (_e = activity.author) == null ? void 0 : _e.name) != null ? _f : null,
    author_email: (_h = (_g = activity.author) == null ? void 0 : _g.email) != null ? _h : null,
    assignee_id: (_j = (_i = activity.assignee) == null ? void 0 : _i.id) != null ? _j : null,
    assignee_name: (_l = (_k = activity.assignee) == null ? void 0 : _k.name) != null ? _l : null,
    assignee_email: (_n = (_m = activity.assignee) == null ? void 0 : _m.email) != null ? _n : null
  };
}
function flattenTicket(ticket) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l;
  return {
    id: ticket.id,
    number: (_a = ticket.number) != null ? _a : null,
    subject: ticket.subject,
    user_id: ticket.user_id,
    client_name: (_c = (_b = ticket.client) == null ? void 0 : _b.name) != null ? _c : null,
    client_email: (_e = (_d = ticket.client) == null ? void 0 : _d.email) != null ? _e : null,
    status: (_f = ticket.status) != null ? _f : null,
    status_id: (_g = ticket.status_id) != null ? _g : null,
    source: (_h = ticket.source) != null ? _h : null,
    order_id: (_i = ticket.order_id) != null ? _i : null,
    note: (_j = ticket.note) != null ? _j : null,
    tags: Array.isArray(ticket.tags) ? ticket.tags.join(", ") : null,
    employees: Array.isArray(ticket.employees) ? ticket.employees.map((e) => [e.name_f, e.name_l].filter(Boolean).join(" ") || String(e.id)).join(", ") : null,
    last_message_at: (_k = ticket.last_message_at) != null ? _k : null,
    date_closed: (_l = ticket.date_closed) != null ? _l : null,
    created_at: ticket.created_at,
    updated_at: ticket.updated_at
  };
}
function flattenOrder(order) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s;
  return {
    id: order.id,
    number: (_a = order.number) != null ? _a : null,
    service: (_b = order.service) != null ? _b : null,
    service_id: (_c = order.service_id) != null ? _c : null,
    user_id: order.user_id,
    client_name: (_e = (_d = order.client) == null ? void 0 : _d.name) != null ? _e : null,
    client_email: (_g = (_f = order.client) == null ? void 0 : _f.email) != null ? _g : null,
    status: (_h = order.status) != null ? _h : null,
    status_id: (_i = order.status_id) != null ? _i : null,
    price: (_j = order.price) != null ? _j : null,
    quantity: (_k = order.quantity) != null ? _k : null,
    invoice_id: (_l = order.invoice_id) != null ? _l : null,
    note: (_m = order.note) != null ? _m : null,
    paysys: (_n = order.paysys) != null ? _n : null,
    currency: (_o = order.currency) != null ? _o : null,
    tags: Array.isArray(order.tags) ? order.tags.join(", ") : null,
    employees: Array.isArray(order.employees) ? order.employees.map((e) => [e.name_f, e.name_l].filter(Boolean).join(" ") || String(e.id)).join(", ") : null,
    linked_orders: Array.isArray(order.linked_orders) ? order.linked_orders.join(", ") : null,
    date_started: (_p = order.date_started) != null ? _p : null,
    date_completed: (_q = order.date_completed) != null ? _q : null,
    date_due: (_r = order.date_due) != null ? _r : null,
    last_message_at: (_s = order.last_message_at) != null ? _s : null,
    created_at: order.created_at,
    updated_at: order.updated_at
  };
}
var ordersDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Order",
  description: "Select an order from your Wayfront workspace.",
  refreshers: [],
  auth: wayfrontAuth,
  required: !0,
  options: async ({ auth }) => {
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your account first." };
    let { workspaceUrl, apiToken } = auth;
    try {
      return {
        disabled: !1,
        options: (await wayfrontApiClient(workspaceUrl, apiToken).get("/orders", { limit: "100" })).body.data.map((order) => {
          var _a, _b;
          return {
            label: `#${(_a = order.number) != null ? _a : order.id}: ${order.service}`,
            value: (_b = order.number) != null ? _b : String(order.id)
          };
        })
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load orders. Check your connection."
      };
    }
  }
});

// ../ap/packages/pieces/community/wayfront/src/lib/auth.ts
var wayfrontAuth = import_pieces_framework2.PieceAuth.CustomAuth({
  description: "Provide your Wayfront workspace URL and bearer token.",
  props: {
    workspaceUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Workspace URL",
      description: "Your Wayfront workspace URL (e.g. example.wayfront.com)",
      required: !0
    }),
    apiToken: import_pieces_framework2.PieceAuth.SecretText({
      displayName: "API Token",
      description: "Your Wayfront bearer token",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    let { success, error } = await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).validateAuth();
    return success ? { valid: !0 } : { valid: !1, error: error != null ? error : "Authentication failed." };
  },
  required: !0
});

// ../ap/packages/pieces/community/wayfront/src/lib/actions/create-client.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var createClientAction = (0, import_pieces_framework3.createAction)({
  auth: wayfrontAuth,
  name: "create_client",
  displayName: "Create Client",
  description: "Creates a new client in your Wayfront workspace.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new client record in a Wayfront workspace from contact details (name, email, company, phone, address, status, balance, custom fields). Use to onboard a customer or lead into the CRM. All fields are optional, so passing none creates an empty client. Not idempotent: each call adds a separate client even with identical input.",
    idempotent: !1
  },
  props: {
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "The email address of the client.",
      required: !1
    }),
    name_f: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    name_l: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    company: import_pieces_framework3.Property.ShortText({
      displayName: "Company",
      required: !1
    }),
    phone: import_pieces_framework3.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    note: import_pieces_framework3.Property.LongText({
      displayName: "Note",
      required: !1
    }),
    status_id: import_pieces_framework3.Property.Number({
      displayName: "Status ID",
      description: "Numeric ID of the client status. Find status IDs in your Wayfront workspace settings.",
      required: !1
    }),
    referrer_user_id: import_pieces_framework3.Property.Number({
      displayName: "Referrer Team Member ID",
      description: "ID of the team member who referred this client.",
      required: !1
    }),
    tax_id: import_pieces_framework3.Property.ShortText({
      displayName: "Tax ID",
      required: !1
    }),
    balance: import_pieces_framework3.Property.Number({
      displayName: "Balance",
      description: "Starting account balance for the client.",
      required: !1
    }),
    optin: import_pieces_framework3.Property.ShortText({
      displayName: "Opt-in",
      description: 'Opt-in status for the client, e.g. "Yes" or "No".',
      required: !1
    }),
    stripe_id: import_pieces_framework3.Property.ShortText({
      displayName: "Stripe Customer ID",
      description: "The Stripe customer ID to link to this client.",
      required: !1
    }),
    created_at: import_pieces_framework3.Property.ShortText({
      displayName: "Created Date",
      description: "Override the creation date. Format: 2021-09-01T00:00:00+00:00. Leave empty to use the current date.",
      required: !1
    }),
    address_line_1: import_pieces_framework3.Property.ShortText({
      displayName: "Address Line 1",
      required: !1
    }),
    address_city: import_pieces_framework3.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    address_state: import_pieces_framework3.Property.ShortText({
      displayName: "State / Province",
      required: !1
    }),
    address_postcode: import_pieces_framework3.Property.ShortText({
      displayName: "Postcode / ZIP",
      required: !1
    }),
    address_country: import_pieces_framework3.Property.ShortText({
      displayName: "Country",
      description: "Two-letter country code, e.g. US or GB.",
      required: !1
    }),
    custom_fields: import_pieces_framework3.Property.Object({
      displayName: "Custom Fields",
      description: "Additional custom fields as key-value pairs.",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth, p = context.propsValue, hasAddress = !(0, import_pieces_framework4.isNil)(p.address_line_1) || !(0, import_pieces_framework4.isNil)(p.address_city) || !(0, import_pieces_framework4.isNil)(p.address_state) || !(0, import_pieces_framework4.isNil)(p.address_postcode) || !(0, import_pieces_framework4.isNil)(p.address_country), body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, !(0, import_pieces_framework4.isNil)(p.email) && { email: p.email }), !(0, import_pieces_framework4.isNil)(p.name_f) && { name_f: p.name_f }), !(0, import_pieces_framework4.isNil)(p.name_l) && { name_l: p.name_l }), !(0, import_pieces_framework4.isNil)(p.company) && { company: p.company }), !(0, import_pieces_framework4.isNil)(p.phone) && { phone: p.phone }), !(0, import_pieces_framework4.isNil)(p.note) && { note: p.note }), !(0, import_pieces_framework4.isNil)(p.status_id) && { status_id: p.status_id }), !(0, import_pieces_framework4.isNil)(p.referrer_user_id) && { referrer_user_id: p.referrer_user_id }), !(0, import_pieces_framework4.isNil)(p.tax_id) && { tax_id: p.tax_id }), !(0, import_pieces_framework4.isNil)(p.balance) && { balance: p.balance }), !(0, import_pieces_framework4.isNil)(p.optin) && { optin: p.optin }), !(0, import_pieces_framework4.isNil)(p.stripe_id) && { stripe_id: p.stripe_id }), !(0, import_pieces_framework4.isNil)(p.created_at) && { created_at: p.created_at }), !(0, import_pieces_framework4.isNil)(p.custom_fields) && { custom_fields: p.custom_fields }), hasAddress && {
      address: __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, !(0, import_pieces_framework4.isNil)(p.address_line_1) && { line_1: p.address_line_1 }), !(0, import_pieces_framework4.isNil)(p.address_city) && { city: p.address_city }), !(0, import_pieces_framework4.isNil)(p.address_state) && { state: p.address_state }), !(0, import_pieces_framework4.isNil)(p.address_postcode) && { postcode: p.address_postcode }), !(0, import_pieces_framework4.isNil)(p.address_country) && { country: p.address_country })
    }), response = await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).post(
      "/clients",
      body
    );
    return flattenUser(response.body);
  }
});

// ../ap/packages/pieces/community/wayfront/src/lib/actions/update-client.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var updateClientAction = (0, import_pieces_framework5.createAction)({
  auth: wayfrontAuth,
  name: "update_client",
  displayName: "Update Client",
  description: "Updates a client in your Wayfront workspace. Only provided fields are changed.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Wayfront client identified by its user ID, applying a partial patch \u2014 only the fields you supply (contact details, status, balance, assigned employees, address, custom fields) are changed; omitted fields are left untouched. Use to edit a known client; requires a valid user ID. Idempotent: re-sending the same patch leaves the client in the same state.",
    idempotent: !0
  },
  props: {
    user_id: clientsDropdown,
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    name_f: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    name_l: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    company: import_pieces_framework5.Property.ShortText({
      displayName: "Company",
      required: !1
    }),
    phone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    note: import_pieces_framework5.Property.LongText({
      displayName: "Note",
      required: !1
    }),
    status_id: import_pieces_framework5.Property.Number({
      displayName: "Status ID",
      description: "Numeric ID of the client status. Find status IDs in your Wayfront workspace settings.",
      required: !1
    }),
    referrer_user_id: import_pieces_framework5.Property.Number({
      displayName: "Referrer Team Member ID",
      description: "ID of the team member who referred this client.",
      required: !1
    }),
    employee_id: import_pieces_framework5.Property.ShortText({
      displayName: "Employee IDs",
      description: "Comma-separated IDs of employees to assign to this client (e.g. 1,2,3).",
      required: !1
    }),
    tax_id: import_pieces_framework5.Property.ShortText({
      displayName: "Tax ID",
      required: !1
    }),
    balance: import_pieces_framework5.Property.Number({
      displayName: "Balance",
      description: "Updated account balance for the client.",
      required: !1
    }),
    optin: import_pieces_framework5.Property.ShortText({
      displayName: "Opt-in",
      description: 'Opt-in status, e.g. "Yes" or "No".',
      required: !1
    }),
    stripe_id: import_pieces_framework5.Property.ShortText({
      displayName: "Stripe Customer ID",
      description: "The Stripe customer ID linked to this client.",
      required: !1
    }),
    created_at: import_pieces_framework5.Property.ShortText({
      displayName: "Created Date",
      description: "Override the creation date. Format: 2021-09-01T00:00:00+00:00.",
      required: !1
    }),
    address_line_1: import_pieces_framework5.Property.ShortText({
      displayName: "Address Line 1",
      required: !1
    }),
    address_city: import_pieces_framework5.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    address_state: import_pieces_framework5.Property.ShortText({
      displayName: "State / Province",
      required: !1
    }),
    address_postcode: import_pieces_framework5.Property.ShortText({
      displayName: "Postcode / ZIP",
      required: !1
    }),
    address_country: import_pieces_framework5.Property.ShortText({
      displayName: "Country",
      description: "Two-letter country code, e.g. US or GB.",
      required: !1
    }),
    custom_fields: import_pieces_framework5.Property.Object({
      displayName: "Custom Fields",
      description: "Additional custom fields as key-value pairs.",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth, p = context.propsValue, employeeIds = p.employee_id ? p.employee_id.split(",").map((s) => parseInt(s.trim(), 10)).filter((n) => !isNaN(n)) : void 0, hasAddress = !(0, import_pieces_framework6.isNil)(p.address_line_1) || !(0, import_pieces_framework6.isNil)(p.address_city) || !(0, import_pieces_framework6.isNil)(p.address_state) || !(0, import_pieces_framework6.isNil)(p.address_postcode) || !(0, import_pieces_framework6.isNil)(p.address_country), body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, !(0, import_pieces_framework6.isNil)(p.email) && { email: p.email }), !(0, import_pieces_framework6.isNil)(p.name_f) && { name_f: p.name_f }), !(0, import_pieces_framework6.isNil)(p.name_l) && { name_l: p.name_l }), !(0, import_pieces_framework6.isNil)(p.company) && { company: p.company }), !(0, import_pieces_framework6.isNil)(p.phone) && { phone: p.phone }), !(0, import_pieces_framework6.isNil)(p.note) && { note: p.note }), !(0, import_pieces_framework6.isNil)(p.status_id) && { status_id: p.status_id }), !(0, import_pieces_framework6.isNil)(p.referrer_user_id) && { referrer_user_id: p.referrer_user_id }), employeeIds !== void 0 && { employee_id: employeeIds }), !(0, import_pieces_framework6.isNil)(p.tax_id) && { tax_id: p.tax_id }), !(0, import_pieces_framework6.isNil)(p.balance) && { balance: p.balance }), !(0, import_pieces_framework6.isNil)(p.optin) && { optin: p.optin }), !(0, import_pieces_framework6.isNil)(p.stripe_id) && { stripe_id: p.stripe_id }), !(0, import_pieces_framework6.isNil)(p.created_at) && { created_at: p.created_at }), !(0, import_pieces_framework6.isNil)(p.custom_fields) && { custom_fields: p.custom_fields }), hasAddress && {
      address: __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, !(0, import_pieces_framework6.isNil)(p.address_line_1) && { line_1: p.address_line_1 }), !(0, import_pieces_framework6.isNil)(p.address_city) && { city: p.address_city }), !(0, import_pieces_framework6.isNil)(p.address_state) && { state: p.address_state }), !(0, import_pieces_framework6.isNil)(p.address_postcode) && { postcode: p.address_postcode }), !(0, import_pieces_framework6.isNil)(p.address_country) && { country: p.address_country })
    }), response = await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).put(
      `/clients/${p.user_id}`,
      body
    );
    return flattenUser(response.body);
  }
});

// ../ap/packages/pieces/community/wayfront/src/lib/actions/create-activity.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var createActivityAction = (0, import_pieces_framework7.createAction)({
  auth: wayfrontAuth,
  name: "create_activity",
  displayName: "Create Activity",
  description: "Creates a new activity log entry for a client in Wayfront.",
  audience: "both",
  aiMetadata: {
    description: "Logs a new activity (note) against a specific Wayfront client, optionally scheduling it for a future date and assigning it to a team member. With no scheduled date it is logged immediately. Use to record an interaction or queue a follow-up; requires the client user ID and content. Not idempotent: each call appends a separate activity.",
    idempotent: !1
  },
  props: {
    user_id: clientsDropdown,
    content: import_pieces_framework7.Property.LongText({
      displayName: "Content",
      description: "The activity note or description (max 10,000 characters).",
      required: !0
    }),
    scheduled_at: import_pieces_framework7.Property.ShortText({
      displayName: "Scheduled At",
      description: "Schedule this activity for a future date and time. Format: 2026-04-15T14:00:00Z (ISO 8601). Leave empty to log it immediately.",
      required: !1
    }),
    assigned_to: teamMembersDropdown
  },
  async run(context) {
    let auth = context.auth, p = context.propsValue, body = { content: p.content };
    p.scheduled_at && (body.scheduled_at = p.scheduled_at), p.assigned_to && (body.assigned_to = p.assigned_to);
    let response = await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).post(
      `/clients/${p.user_id}/activities`,
      body
    );
    return flattenActivity(response.body);
  }
});

// ../ap/packages/pieces/community/wayfront/src/lib/actions/update-activity.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var updateActivityAction = (0, import_pieces_framework8.createAction)({
  auth: wayfrontAuth,
  name: "update_activity",
  displayName: "Update Activity",
  description: "Updates an existing activity for a client in Wayfront.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing activity on a Wayfront client, changing its content and optionally its assignee or scheduled date (rescheduling only applies to already-scheduled activities). Use to edit a known activity; requires both the client user ID and the activity ID. Idempotent: re-sending the same values yields the same activity state.",
    idempotent: !0
  },
  props: {
    user_id: clientsDropdown,
    activity_id: activitiesDropdown,
    content: import_pieces_framework8.Property.LongText({
      displayName: "Content",
      description: "The updated activity note or description (max 10,000 characters).",
      required: !0
    }),
    scheduled_at: import_pieces_framework8.Property.ShortText({
      displayName: "Scheduled At",
      description: "Reschedule the activity. Format: 2026-05-01T14:00:00Z (ISO 8601). Only applies to already-scheduled activities.",
      required: !1
    }),
    assigned_to: teamMembersDropdown
  },
  async run(context) {
    let auth = context.auth, p = context.propsValue, body = { content: p.content };
    p.scheduled_at && (body.scheduled_at = p.scheduled_at), p.assigned_to && (body.assigned_to = p.assigned_to);
    let response = await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).put(
      `/clients/${p.user_id}/activities/${p.activity_id}`,
      body
    );
    return flattenActivity(response.body);
  }
});

// ../ap/packages/pieces/community/wayfront/src/lib/actions/complete-activity.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var completeActivityAction = (0, import_pieces_framework9.createAction)({
  auth: wayfrontAuth,
  name: "complete_activity",
  displayName: "Complete Activity",
  description: "Marks a scheduled activity as complete for a client in Wayfront.",
  audience: "both",
  aiMetadata: {
    description: "Marks a specific scheduled activity on a Wayfront client as complete. Use to close out a follow-up or task once done; requires both the client user ID and the activity ID. Idempotent: completing an already-completed activity leaves it complete with no additional effect.",
    idempotent: !0
  },
  props: {
    user_id: clientsDropdown,
    activity_id: activitiesDropdown
  },
  async run(context) {
    let auth = context.auth, p = context.propsValue, response = await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).post(
      `/clients/${p.user_id}/activities/${p.activity_id}/complete`
    );
    return flattenActivity(response.body);
  }
});

// ../ap/packages/pieces/community/wayfront/src/lib/actions/create-ticket.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var createTicketAction = (0, import_pieces_framework10.createAction)({
  auth: wayfrontAuth,
  name: "create_ticket",
  displayName: "Create Ticket",
  description: "Creates a new support ticket for a client in Wayfront.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new support ticket for a specific Wayfront client, with a subject and optional note, status, assigned employees, tags, linked order reference, and metadata. Use to open a support case from an inbound request. Requires the client user ID and a subject. Not idempotent: each call opens a separate ticket.",
    idempotent: !1
  },
  props: {
    user_id: clientsDropdown,
    subject: import_pieces_framework10.Property.ShortText({
      displayName: "Subject",
      description: "The subject line of the ticket.",
      required: !0
    }),
    note: import_pieces_framework10.Property.LongText({
      displayName: "Note",
      description: "An internal note to attach to the ticket.",
      required: !1
    }),
    status: import_pieces_framework10.Property.Number({
      displayName: "Status ID",
      description: "Numeric ID of the ticket status. Find status IDs in your Wayfront workspace settings.",
      required: !1
    }),
    employees: import_pieces_framework10.Property.ShortText({
      displayName: "Assigned Employee IDs",
      description: "Comma-separated IDs of employees to assign to this ticket (e.g. 1,2,3).",
      required: !1
    }),
    tags: import_pieces_framework10.Property.ShortText({
      displayName: "Tags",
      description: "Comma-separated list of tags to apply to the ticket (e.g. billing,urgent).",
      required: !1
    }),
    order: import_pieces_framework10.Property.ShortText({
      displayName: "Order Reference",
      description: "The order number or reference to link to this ticket.",
      required: !1
    }),
    metadata: import_pieces_framework10.Property.ShortText({
      displayName: "Metadata",
      description: "Comma-separated metadata values to attach to the ticket.",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth, p = context.propsValue, employeeIds = p.employees ? p.employees.split(",").map((s) => parseInt(s.trim(), 10)).filter((n) => !isNaN(n)) : void 0, tags = p.tags ? p.tags.split(",").map((s) => s.trim()).filter(Boolean) : void 0, metadata = p.metadata ? p.metadata.split(",").map((s) => s.trim()).filter(Boolean) : void 0, body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      user_id: p.user_id,
      subject: p.subject
    }, !(0, import_pieces_framework11.isNil)(p.note) && { note: p.note }), !(0, import_pieces_framework11.isNil)(p.status) && { status: p.status }), employeeIds !== void 0 && { "employees[]": employeeIds }), tags !== void 0 && { "tags[]": tags }), !(0, import_pieces_framework11.isNil)(p.order) && { order: p.order }), metadata !== void 0 && { "metadata[]": metadata }), response = await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).post(
      "/tickets",
      body
    );
    return flattenTicket(response.body);
  }
});

// ../ap/packages/pieces/community/wayfront/src/lib/actions/list-tickets.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var listTicketsAction = (0, import_pieces_framework12.createAction)({
  auth: wayfrontAuth,
  name: "list_tickets",
  displayName: "List Tickets",
  description: "Returns a list of tickets from your Wayfront workspace, sorted by most recent first.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a page of support tickets from the Wayfront workspace, newest first, with configurable page size and page number for pagination. Use to browse, scan, or look up recent tickets. Read-only and idempotent: it returns current data without modifying anything.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework12.Property.Number({
      displayName: "Limit",
      description: "Maximum number of tickets to return per page. Defaults to 20.",
      required: !1,
      defaultValue: 20
    }),
    page: import_pieces_framework12.Property.Number({
      displayName: "Page",
      description: "Page number to retrieve when results span multiple pages. Starts at 1.",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    var _a, _b;
    let auth = context.auth, p = context.propsValue, queryParams = {
      limit: String((_a = p.limit) != null ? _a : 20),
      page: String((_b = p.page) != null ? _b : 1)
    };
    return (await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).get("/tickets", queryParams)).body.data.map(flattenTicket);
  }
});

// ../ap/packages/pieces/community/wayfront/src/lib/actions/update-ticket.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_framework14 = require("@activepieces/pieces-framework");
var updateTicketAction = (0, import_pieces_framework13.createAction)({
  auth: wayfrontAuth,
  name: "update_ticket",
  displayName: "Update Ticket",
  description: "Updates a ticket in Wayfront. Only the fields you provide will be changed.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Wayfront ticket identified by its reference, applying a partial patch to subject, status, assigned employees, tags, linked order, or metadata; omitted fields are left untouched, but supplying tags replaces all existing tags. Use to change a ticket\u2019s state or routing. Idempotent: re-sending the same patch yields the same ticket state.",
    idempotent: !0
  },
  props: {
    ticket_ref: ticketsDropdown,
    subject: import_pieces_framework13.Property.ShortText({
      displayName: "Subject",
      description: "Updated subject line for the ticket.",
      required: !1
    }),
    status: import_pieces_framework13.Property.Number({
      displayName: "Status ID",
      description: "Numeric ID of the ticket status. Find status IDs in your Wayfront workspace settings.",
      required: !1
    }),
    employees: import_pieces_framework13.Property.ShortText({
      displayName: "Assigned Employee IDs",
      description: "Comma-separated IDs of employees to assign to this ticket (e.g. 1,2,3).",
      required: !1
    }),
    tags: import_pieces_framework13.Property.ShortText({
      displayName: "Tags",
      description: "Comma-separated list of tags (e.g. billing,urgent). Replaces all existing tags.",
      required: !1
    }),
    order: import_pieces_framework13.Property.ShortText({
      displayName: "Order Reference",
      description: "The order number or reference to link to this ticket.",
      required: !1
    }),
    metadata: import_pieces_framework13.Property.ShortText({
      displayName: "Metadata",
      description: "Comma-separated metadata values to attach to the ticket.",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth, p = context.propsValue, employeeIds = p.employees ? p.employees.split(",").map((s) => parseInt(s.trim(), 10)).filter((n) => !isNaN(n)) : void 0, tags = p.tags ? p.tags.split(",").map((s) => s.trim()).filter(Boolean) : void 0, metadata = p.metadata ? p.metadata.split(",").map((s) => s.trim()).filter(Boolean) : void 0, body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, !(0, import_pieces_framework14.isNil)(p.subject) && { subject: p.subject }), !(0, import_pieces_framework14.isNil)(p.status) && { status: p.status }), employeeIds !== void 0 && { "employees[]": employeeIds }), tags !== void 0 && { "tags[]": tags }), !(0, import_pieces_framework14.isNil)(p.order) && { order: p.order }), metadata !== void 0 && { "metadata[]": metadata }), response = await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).put(
      `/tickets/${p.ticket_ref}`,
      body
    );
    return flattenTicket(response.body);
  }
});

// ../ap/packages/pieces/community/wayfront/src/lib/actions/list-orders.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var listOrdersAction = (0, import_pieces_framework15.createAction)({
  auth: wayfrontAuth,
  name: "list_orders",
  displayName: "List Orders",
  description: "Returns a list of orders from your Wayfront workspace, sorted by most recent first.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a page of orders from the Wayfront workspace, newest first, with configurable page size and page number for pagination. Use to browse, scan, or look up recent orders. Read-only and idempotent: it returns current data without modifying anything.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework15.Property.Number({
      displayName: "Limit",
      description: "Maximum number of orders to return per page. Defaults to 20.",
      required: !1,
      defaultValue: 20
    }),
    page: import_pieces_framework15.Property.Number({
      displayName: "Page",
      description: "Page number to retrieve when results span multiple pages. Starts at 1.",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    var _a, _b;
    let auth = context.auth, p = context.propsValue;
    return (await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).get("/orders", {
      limit: String((_a = p.limit) != null ? _a : 20),
      page: String((_b = p.page) != null ? _b : 1)
    })).body.data.map(flattenOrder);
  }
});

// ../ap/packages/pieces/community/wayfront/src/lib/actions/create-order.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_framework17 = require("@activepieces/pieces-framework");
var createOrderAction = (0, import_pieces_framework16.createAction)({
  auth: wayfrontAuth,
  name: "create_order",
  displayName: "Create Order",
  description: "Creates a new order for a client in Wayfront.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new order for a specific Wayfront client. Identify the service either by a free-text service title or by an existing service ID (supply one of the two); optionally set status, order number, due/start/completion/created dates, assigned employees, tags, linked orders, and metadata. Requires the client user ID. Not idempotent: each call creates a separate order.",
    idempotent: !1
  },
  props: {
    user_id: clientsDropdown,
    service: import_pieces_framework16.Property.ShortText({
      displayName: "Service / Order Title",
      description: 'The name of the service or title for this order (e.g. "Instagram Followers"). Required if no Service ID is provided.',
      required: !1
    }),
    service_id: import_pieces_framework16.Property.Number({
      displayName: "Service ID",
      description: "ID of an existing service in Wayfront to attach to this order. Required if no Service / Order Title is provided.",
      required: !1
    }),
    note: import_pieces_framework16.Property.LongText({
      displayName: "Note",
      description: "An internal note to attach to the order.",
      required: !1
    }),
    status: import_pieces_framework16.Property.Number({
      displayName: "Status ID",
      description: "Numeric ID of the order status. Find status IDs in your Wayfront workspace settings.",
      required: !1
    }),
    number: import_pieces_framework16.Property.ShortText({
      displayName: "Order Number",
      description: "Custom order reference number (e.g. B1C2D3E4F5). Leave empty to auto-generate.",
      required: !1
    }),
    date_due: import_pieces_framework16.Property.ShortText({
      displayName: "Due Date",
      description: "Due date for the order. Format: 2021-09-01T00:00:00+00:00 (ISO 8601).",
      required: !1
    }),
    date_started: import_pieces_framework16.Property.ShortText({
      displayName: "Start Date",
      description: "Date the order was started. Format: 2021-09-01T00:00:00+00:00 (ISO 8601).",
      required: !1
    }),
    date_completed: import_pieces_framework16.Property.ShortText({
      displayName: "Completion Date",
      description: "Date the order was completed. Format: 2021-09-01T00:00:00+00:00 (ISO 8601).",
      required: !1
    }),
    created_at: import_pieces_framework16.Property.ShortText({
      displayName: "Created Date",
      description: "Override the creation date. Format: 2021-09-01T00:00:00+00:00. Leave empty to use now.",
      required: !1
    }),
    employees: import_pieces_framework16.Property.ShortText({
      displayName: "Assigned Employee IDs",
      description: "Comma-separated IDs of employees to assign to this order (e.g. 1,2,3).",
      required: !1
    }),
    tags: import_pieces_framework16.Property.ShortText({
      displayName: "Tags",
      description: "Comma-separated list of tags to apply to the order (e.g. vip,rush).",
      required: !1
    }),
    linked_orders: import_pieces_framework16.Property.ShortText({
      displayName: "Linked Order Numbers",
      description: "Comma-separated order numbers to link to this order (e.g. B1C2D3,E4F5G6).",
      required: !1
    }),
    metadata: import_pieces_framework16.Property.ShortText({
      displayName: "Metadata",
      description: "Comma-separated metadata values to attach to the order.",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth, p = context.propsValue, employeeIds = p.employees ? p.employees.split(",").map((s) => parseInt(s.trim(), 10)).filter((n) => !isNaN(n)) : void 0, tags = p.tags ? p.tags.split(",").map((s) => s.trim()).filter(Boolean) : void 0, linkedOrders = p.linked_orders ? p.linked_orders.split(",").map((s) => s.trim()).filter(Boolean) : void 0, metadata = p.metadata ? p.metadata.split(",").map((s) => s.trim()).filter(Boolean) : void 0, body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      user_id: p.user_id
    }, !(0, import_pieces_framework17.isNil)(p.service) && { service: p.service }), !(0, import_pieces_framework17.isNil)(p.service_id) && { service_id: p.service_id }), !(0, import_pieces_framework17.isNil)(p.note) && { note: p.note }), !(0, import_pieces_framework17.isNil)(p.status) && { status: p.status }), !(0, import_pieces_framework17.isNil)(p.number) && { number: p.number }), !(0, import_pieces_framework17.isNil)(p.date_due) && { date_due: p.date_due }), !(0, import_pieces_framework17.isNil)(p.date_started) && { date_started: p.date_started }), !(0, import_pieces_framework17.isNil)(p.date_completed) && { date_completed: p.date_completed }), !(0, import_pieces_framework17.isNil)(p.created_at) && { created_at: p.created_at }), employeeIds !== void 0 && { "employees[]": employeeIds }), tags !== void 0 && { "tags[]": tags }), linkedOrders !== void 0 && { "linked_orders[]": linkedOrders }), metadata !== void 0 && { "metadata[]": metadata }), response = await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).post(
      "/orders",
      body
    );
    return flattenOrder(response.body);
  }
});

// ../ap/packages/pieces/community/wayfront/src/lib/actions/update-order.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_framework19 = require("@activepieces/pieces-framework");
var updateOrderAction = (0, import_pieces_framework18.createAction)({
  auth: wayfrontAuth,
  name: "update_order",
  displayName: "Update Order",
  description: "Updates an order in Wayfront. Only the fields you provide will be changed.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Wayfront order identified by its reference, applying a partial patch to note, status, service, dates, assigned employees, tags, linked orders, form data, or metadata; omitted fields are left untouched, but supplying tags or linked orders replaces the existing set. Use to advance or amend a known order. Idempotent: re-sending the same patch yields the same order state.",
    idempotent: !0
  },
  props: {
    order_ref: ordersDropdown,
    note: import_pieces_framework18.Property.LongText({
      displayName: "Note",
      description: "Updated internal note for the order.",
      required: !1
    }),
    status: import_pieces_framework18.Property.Number({
      displayName: "Status ID",
      description: "Numeric ID of the order status. Find status IDs in your Wayfront workspace settings.",
      required: !1
    }),
    service_id: import_pieces_framework18.Property.Number({
      displayName: "Service ID",
      description: "ID of the service to attach to this order.",
      required: !1
    }),
    date_due: import_pieces_framework18.Property.ShortText({
      displayName: "Due Date",
      description: "Updated due date. Format: 2021-09-01T00:00:00+00:00 (ISO 8601).",
      required: !1
    }),
    date_started: import_pieces_framework18.Property.ShortText({
      displayName: "Start Date",
      description: "Updated start date. Format: 2021-09-01T00:00:00+00:00 (ISO 8601).",
      required: !1
    }),
    date_completed: import_pieces_framework18.Property.ShortText({
      displayName: "Completion Date",
      description: "Updated completion date. Format: 2021-09-01T00:00:00+00:00 (ISO 8601).",
      required: !1
    }),
    created_at: import_pieces_framework18.Property.ShortText({
      displayName: "Created Date",
      description: "Override the creation date. Format: 2021-09-01T00:00:00+00:00 (ISO 8601).",
      required: !1
    }),
    employees: import_pieces_framework18.Property.ShortText({
      displayName: "Assigned Employee IDs",
      description: "Comma-separated IDs of employees to assign to this order (e.g. 1,2,3).",
      required: !1
    }),
    tags: import_pieces_framework18.Property.ShortText({
      displayName: "Tags",
      description: "Comma-separated list of tags (e.g. vip,rush). Replaces all existing tags.",
      required: !1
    }),
    linked_orders: import_pieces_framework18.Property.ShortText({
      displayName: "Linked Order Numbers",
      description: "Comma-separated order numbers to link (e.g. B1C2D3,E4F5G6). Replaces existing links.",
      required: !1
    }),
    form_data: import_pieces_framework18.Property.ShortText({
      displayName: "Form Data",
      description: "Comma-separated form data values to attach to the order.",
      required: !1
    }),
    metadata: import_pieces_framework18.Property.ShortText({
      displayName: "Metadata",
      description: "Comma-separated metadata values to attach to the order.",
      required: !1
    })
  },
  async run(context) {
    let auth = context.auth, p = context.propsValue, employeeIds = p.employees ? p.employees.split(",").map((s) => parseInt(s.trim(), 10)).filter((n) => !isNaN(n)) : void 0, tags = p.tags ? p.tags.split(",").map((s) => s.trim()).filter(Boolean) : void 0, linkedOrders = p.linked_orders ? p.linked_orders.split(",").map((s) => s.trim()).filter(Boolean) : void 0, formData = p.form_data ? p.form_data.split(",").map((s) => s.trim()).filter(Boolean) : void 0, metadata = p.metadata ? p.metadata.split(",").map((s) => s.trim()).filter(Boolean) : void 0, body = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, !(0, import_pieces_framework19.isNil)(p.note) && { note: p.note }), !(0, import_pieces_framework19.isNil)(p.status) && { status: p.status }), !(0, import_pieces_framework19.isNil)(p.service_id) && { service_id: p.service_id }), !(0, import_pieces_framework19.isNil)(p.date_due) && { date_due: p.date_due }), !(0, import_pieces_framework19.isNil)(p.date_started) && { date_started: p.date_started }), !(0, import_pieces_framework19.isNil)(p.date_completed) && { date_completed: p.date_completed }), !(0, import_pieces_framework19.isNil)(p.created_at) && { created_at: p.created_at }), employeeIds !== void 0 && { "employees[]": employeeIds }), tags !== void 0 && { "tags[]": tags }), linkedOrders !== void 0 && { "linked_orders[]": linkedOrders }), formData !== void 0 && { "form_data[]": formData }), metadata !== void 0 && { "metadata[]": metadata }), response = await wayfrontApiClient(auth.workspaceUrl, auth.apiToken).put(
      `/orders/${p.order_ref}`,
      body
    );
    return flattenOrder(response.body);
  }
});

// ../ap/packages/pieces/community/wayfront/src/index.ts
var wayfront = (0, import_pieces_framework20.createPiece)({
  displayName: "Wayfront",
  description: "Unify your process, automate steps, and help clients find what they need.",
  auth: wayfrontAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/wayfront.png",
  authors: ["onyedikachi-david"],
  actions: [
    createClientAction,
    updateClientAction,
    createActivityAction,
    updateActivityAction,
    completeActivityAction,
    createTicketAction,
    listTicketsAction,
    updateTicketAction,
    listOrdersAction,
    createOrderAction,
    updateOrderAction
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  wayfront
});
