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

// ../ap/packages/pieces/community/ninjapipe/src/index.ts
var index_exports = {};
__export(index_exports, {
  ninjapipe: () => ninjapipe,
  ninjapipeAuth: () => ninjapipeAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework60 = require("@activepieces/pieces-framework"), import_pieces_common61 = require("@activepieces/pieces-common"), import_pieces_framework61 = require("@activepieces/pieces-framework"), import_pieces_common62 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/create-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/ninjapipe/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
function getAuth(context) {
  var _a, _b, _c, _d;
  let raw = context.auth, base_url = (_b = (_a = raw == null ? void 0 : raw.props) == null ? void 0 : _a.base_url) != null ? _b : raw == null ? void 0 : raw.base_url, api_key = (_d = (_c = raw == null ? void 0 : raw.props) == null ? void 0 : _c.api_key) != null ? _d : raw == null ? void 0 : raw.api_key;
  if (!base_url || !api_key)
    throw new Error("NinjaPipe connection is missing Base URL or API Key. Reconnect the account.");
  return { base_url, api_key };
}
async function ninjapipeApiCall({
  auth,
  method,
  path,
  body,
  queryParams
}) {
  let sanitizedPath = path.startsWith("/") ? path : `/${path}`, url = `${auth.base_url.replace(/\/+$/, "")}${sanitizedPath}`, cleanQuery = queryParams ? Object.fromEntries(
    Object.entries(queryParams).filter(([, v]) => v != null).map(([k, v]) => [k, String(v)])
  ) : void 0;
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: auth.api_key
    },
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json"
    },
    body,
    queryParams: cleanQuery
  });
}
var RESOURCE_KEYS = [
  "contacts",
  "companies",
  "deals",
  "products",
  "budgets",
  "lists",
  "pipelines",
  "pipeline_items",
  "pipelineItems",
  "invoices",
  "orders",
  "projects",
  "tasks",
  "databins",
  "responses"
];
function extractItems(response) {
  if (!response) return [];
  if (Array.isArray(response)) return response;
  let r = response;
  if (Array.isArray(r.data)) return r.data;
  if (Array.isArray(r.items)) return r.items;
  if (Array.isArray(r.results)) return r.results;
  for (let key of RESOURCE_KEYS)
    if (Array.isArray(r[key])) return r[key];
  return [];
}
function flattenObject(obj, prefix = "") {
  let result = {};
  for (let [key, value] of Object.entries(obj)) {
    let flatKey = prefix ? `${prefix}_${key}` : key;
    value == null ? result[flatKey] = null : Array.isArray(value) ? result[flatKey] = value.map((v) => typeof v == "object" ? JSON.stringify(v) : String(v)).join(", ") : typeof value == "object" ? Object.assign(result, flattenObject(value, flatKey)) : result[flatKey] = value;
  }
  return result;
}
function flattenCustomFields(record) {
  let flat = __spreadValues({}, record), cf = flat.custom_fields;
  if (cf && typeof cf == "object" && !Array.isArray(cf))
    for (let [key, value] of Object.entries(cf))
      key in flat || (flat[key] = value), flat[`cf_${key}`] = value;
  return delete flat.custom_fields, flattenObject(flat);
}
function flattenArray(items) {
  return items.map((item) => typeof item == "object" && item !== null ? flattenCustomFields(item) : { raw_value: item });
}
function toDateOnly(value) {
  if (value == null || value === "") return;
  let str = String(value), isoMatch = str.match(/^(\d{4}-\d{2}-\d{2})/);
  if (isoMatch) return isoMatch[1];
  let d = new Date(str);
  if (!isNaN(d.getTime()))
    return d.toISOString().slice(0, 10);
}
var buildDropdownOption = (item) => {
  var _a, _b, _c, _d;
  return {
    label: (_c = (_a = item.name) != null ? _a : item.title) != null ? _c : String((_b = item.id) != null ? _b : ""),
    value: String((_d = item.id) != null ? _d : "")
  };
}, ninjapipeCommon = {
  countryDropdown: import_pieces_framework.Property.StaticDropdown({
    displayName: "Country",
    description: "Select a country.",
    required: !1,
    options: {
      options: [
        { label: "Germany", value: "DE" },
        { label: "Austria", value: "AT" },
        { label: "Switzerland", value: "CH" },
        { label: "Belgium", value: "BE" },
        { label: "Netherlands", value: "NL" },
        { label: "Luxembourg", value: "LU" },
        { label: "France", value: "FR" },
        { label: "Italy", value: "IT" },
        { label: "Spain", value: "ES" },
        { label: "Portugal", value: "PT" },
        { label: "Poland", value: "PL" },
        { label: "Hungary", value: "HU" },
        { label: "United Kingdom", value: "GB" },
        { label: "United States", value: "US" },
        { label: "Canada", value: "CA" }
      ]
    }
  }),
  pipelineDropdown: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Pipeline",
    description: "Select a pipeline.",
    refreshers: [],
    required: !1,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/pipelines",
          queryParams: { limit: "100" }
        });
        return {
          disabled: !1,
          options: extractItems(response.body).map(buildDropdownOption)
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load pipelines. Check connection." };
      }
    }
  }),
  contactDropdown: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Contact",
    description: "Select a contact.",
    refreshers: [],
    required: !1,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/contacts",
          queryParams: { limit: "100" }
        });
        return {
          disabled: !1,
          options: extractItems(response.body).map((c) => {
            var _a, _b, _c, _d, _e;
            return {
              label: `${(_a = c.first_name) != null ? _a : ""} ${(_b = c.last_name) != null ? _b : ""} <${(_c = c.email) != null ? _c : "no-email"}>`.trim() || c.email || String((_d = c.id) != null ? _d : ""),
              value: String((_e = c.id) != null ? _e : "")
            };
          })
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load contacts. Check connection." };
      }
    }
  }),
  companyDropdown: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Company",
    description: "Select a company.",
    refreshers: [],
    required: !1,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/companies",
          queryParams: { limit: "100" }
        });
        return {
          disabled: !1,
          options: extractItems(response.body).map(buildDropdownOption)
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load companies. Check connection." };
      }
    }
  }),
  projectDropdown: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Project",
    description: "Select a project.",
    refreshers: [],
    required: !1,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/projects",
          queryParams: { limit: "100" }
        });
        return {
          disabled: !1,
          options: extractItems(response.body).map(buildDropdownOption)
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load projects. Check connection." };
      }
    }
  }),
  projectDropdownRequired: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Project",
    description: "Select the project this task belongs to.",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/projects",
          queryParams: { limit: "100" }
        });
        return {
          disabled: !1,
          options: extractItems(response.body).map(buildDropdownOption)
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load projects. Check connection." };
      }
    }
  }),
  taskDropdown: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Task",
    description: "Select a task. Pick the project first.",
    refreshers: ["projectId"],
    required: !0,
    options: async ({ auth, projectId }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      if (!projectId) return { disabled: !0, options: [], placeholder: "Select a project first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: `/projects/${projectId}/tasks`,
          queryParams: { sort_by: "created_at", sort_order: "desc" }
        });
        return {
          disabled: !1,
          options: extractItems(response.body).map(buildDropdownOption)
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load tasks." };
      }
    }
  }),
  taskStatusDropdown: import_pieces_framework.Property.StaticDropdown({
    displayName: "Status",
    description: "Task status.",
    required: !1,
    defaultValue: "To Do",
    options: {
      options: [
        { label: "To Do", value: "To Do" },
        { label: "In Progress", value: "In Progress" },
        { label: "Done", value: "Done" },
        { label: "Cancelled", value: "Cancelled" }
      ]
    }
  }),
  priorityDropdown: import_pieces_framework.Property.StaticDropdown({
    displayName: "Priority",
    description: "Priority level.",
    required: !1,
    defaultValue: "Medium",
    options: {
      options: [
        { label: "High", value: "High" },
        { label: "Medium", value: "Medium" },
        { label: "Low", value: "Low" }
      ]
    }
  }),
  projectStatusDropdown: import_pieces_framework.Property.StaticDropdown({
    displayName: "Status",
    description: "Project status.",
    required: !1,
    defaultValue: "Planning",
    options: {
      options: [
        { label: "Planning", value: "Planning" },
        { label: "In Progress", value: "In Progress" },
        { label: "Completed", value: "Completed" },
        { label: "On Hold", value: "On Hold" },
        { label: "Cancelled", value: "Cancelled" }
      ]
    }
  }),
  budgetCategoryDropdown: import_pieces_framework.Property.StaticDropdown({
    displayName: "Category",
    description: "Budget category.",
    required: !1,
    options: {
      options: [
        { label: "Marketing", value: "Marketing" },
        { label: "Sales", value: "Sales" },
        { label: "Product", value: "Product" },
        { label: "Operations", value: "Operations" }
      ]
    }
  }),
  budgetPeriodDropdown: import_pieces_framework.Property.StaticDropdown({
    displayName: "Period",
    description: "Budget period.",
    required: !1,
    options: {
      options: [
        { label: "Monthly", value: "Monthly" },
        { label: "Quarterly", value: "Quarterly" },
        { label: "Yearly", value: "Yearly" }
      ]
    }
  }),
  budgetStatusDropdown: import_pieces_framework.Property.StaticDropdown({
    displayName: "Status",
    description: "Budget status.",
    required: !1,
    defaultValue: "Active",
    options: {
      options: [
        { label: "Active", value: "Active" },
        { label: "Inactive", value: "Inactive" },
        { label: "Completed", value: "Completed" }
      ]
    }
  }),
  orderStatusDropdown: import_pieces_framework.Property.StaticDropdown({
    displayName: "Status",
    description: "Order status.",
    required: !1,
    defaultValue: "Processing",
    options: {
      options: [
        { label: "Processing", value: "Processing" },
        { label: "Shipped", value: "Shipped" },
        { label: "Delivered", value: "Delivered" },
        { label: "Cancelled", value: "Cancelled" }
      ]
    }
  }),
  paymentStatusDropdown: import_pieces_framework.Property.StaticDropdown({
    displayName: "Payment Status",
    description: "Payment status.",
    required: !1,
    defaultValue: "Pending",
    options: {
      options: [
        { label: "Pending", value: "Pending" },
        { label: "Paid", value: "Paid" },
        { label: "Refunded", value: "Refunded" },
        { label: "Failed", value: "Failed" }
      ]
    }
  }),
  pipelineDropdownRequired: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Pipeline",
    description: "Select a pipeline.",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/pipelines",
          queryParams: { limit: "100" }
        });
        return { disabled: !1, options: extractItems(response.body).map(buildDropdownOption) };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load pipelines." };
      }
    }
  }),
  contactDropdownRequired: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Contact",
    description: "Select a contact.",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/contacts",
          queryParams: { limit: "100" }
        });
        return {
          disabled: !1,
          options: extractItems(response.body).map((c) => {
            var _a, _b, _c, _d, _e;
            return {
              label: `${(_a = c.first_name) != null ? _a : ""} ${(_b = c.last_name) != null ? _b : ""} <${(_c = c.email) != null ? _c : "no-email"}>`.trim() || c.email || String((_d = c.id) != null ? _d : ""),
              value: String((_e = c.id) != null ? _e : "")
            };
          })
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load contacts." };
      }
    }
  }),
  companyDropdownRequired: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Company",
    description: "Select a company.",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/companies",
          queryParams: { limit: "100" }
        });
        return { disabled: !1, options: extractItems(response.body).map(buildDropdownOption) };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load companies." };
      }
    }
  }),
  dealDropdownRequired: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Deal",
    description: "Select a deal.",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/deals",
          queryParams: { limit: "100" }
        });
        return { disabled: !1, options: extractItems(response.body).map(buildDropdownOption) };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load deals." };
      }
    }
  }),
  productDropdownRequired: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Product",
    description: "Select a product.",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/products",
          queryParams: { limit: "100" }
        });
        return { disabled: !1, options: extractItems(response.body).map(buildDropdownOption) };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load products." };
      }
    }
  }),
  budgetDropdownRequired: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Budget",
    description: "Select a budget.",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/budgets",
          queryParams: { limit: "100" }
        });
        return { disabled: !1, options: extractItems(response.body).map(buildDropdownOption) };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load budgets." };
      }
    }
  }),
  orderDropdownRequired: import_pieces_framework.Property.Dropdown({
    auth: ninjapipeAuth,
    displayName: "Order",
    description: "Select an order.",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth) return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        let a = getAuth({ auth }), response = await ninjapipeApiCall({
          auth: a,
          method: import_pieces_common.HttpMethod.GET,
          path: "/orders",
          queryParams: { limit: "100" }
        });
        return {
          disabled: !1,
          options: extractItems(response.body).map((o) => {
            var _a, _b, _c, _d;
            return {
              label: `${(_a = o.order_number) != null ? _a : ""} \u2014 ${(_b = o.customer_name) != null ? _b : ""}`.trim().replace(/^—\s*/, "") || String((_c = o.id) != null ? _c : ""),
              value: String((_d = o.id) != null ? _d : "")
            };
          })
        };
      } catch (e) {
        return { disabled: !0, options: [], placeholder: "Failed to load orders." };
      }
    }
  }),
  limitProperty: import_pieces_framework.Property.Number({
    displayName: "Limit",
    description: "Maximum number of records to return (1-100).",
    required: !1,
    defaultValue: 20
  }),
  statusFilterProperty: import_pieces_framework.Property.ShortText({
    displayName: "Status Filter",
    description: "Filter records by status.",
    required: !1
  }),
  searchProperty: import_pieces_framework.Property.ShortText({
    displayName: "Search",
    description: "Search query string.",
    required: !1
  }),
  ownerFilterProperty: import_pieces_framework.Property.ShortText({
    displayName: "Owner Filter",
    description: "Filter by owner identifier.",
    required: !1
  })
};

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/create-contact.ts
var createContact = (0, import_pieces_framework2.createAction)({
  auth: ninjapipeAuth,
  name: "create_contact",
  displayName: "Create Contact",
  description: "Creates a new contact. Choose what happens if a contact with the same email already exists.",
  audience: "both",
  aiMetadata: { description: 'Create a NinjaPipe contact. Email is the only required field; the "If Contact Exists" mode controls duplicate handling (error, skip, get existing, or update existing) when the email already exists. Default mode (error) is non-idempotent and will fail on re-run; pick "update existing" or "upsert-contact" for safe retries.', idempotent: !1 },
  props: {
    firstName: import_pieces_framework2.Property.ShortText({ displayName: "First Name", required: !1 }),
    lastName: import_pieces_framework2.Property.ShortText({ displayName: "Last Name", required: !1 }),
    email: import_pieces_framework2.Property.ShortText({ displayName: "Email", description: "The email address of the contact.", required: !0 }),
    phone: import_pieces_framework2.Property.ShortText({ displayName: "Phone", required: !1 }),
    company: import_pieces_framework2.Property.ShortText({ displayName: "Company", description: "Company name or ID.", required: !1 }),
    status: import_pieces_framework2.Property.ShortText({ displayName: "Status", required: !1 }),
    owner: import_pieces_framework2.Property.ShortText({ displayName: "Owner", required: !1 }),
    address: import_pieces_framework2.Property.LongText({ displayName: "Address", required: !1 }),
    city: import_pieces_framework2.Property.ShortText({ displayName: "City", required: !1 }),
    zip: import_pieces_framework2.Property.ShortText({ displayName: "ZIP Code", required: !1 }),
    country: ninjapipeCommon.countryDropdown,
    state: import_pieces_framework2.Property.ShortText({ displayName: "State", required: !1 }),
    notes: import_pieces_framework2.Property.LongText({ displayName: "Notes", required: !1 }),
    customFields: import_pieces_framework2.Property.Object({ displayName: "Custom Fields", description: "Key-value pairs sent as custom_fields.", required: !1 }),
    ifContactExists: import_pieces_framework2.Property.StaticDropdown({
      displayName: "If Contact Exists",
      description: "What to do when a contact with this email already exists.",
      required: !1,
      defaultValue: "error",
      options: {
        options: [
          { label: "Throw Error", value: "error" },
          { label: "Skip", value: "skip" },
          { label: "Get Existing", value: "getExisting" },
          { label: "Update Existing", value: "updateExisting" }
        ]
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.firstName && (body.first_name = p.firstName), p.lastName && (body.last_name = p.lastName), p.email && (body.email = p.email), p.phone && (body.phone = p.phone), p.company && (body.company = p.company), p.status && (body.status = p.status), p.owner && (body.owner = p.owner), p.address && (body.address = p.address), p.city && (body.city = p.city), p.zip && (body.zip = p.zip), p.country && (body.country = p.country), p.state && (body.state = p.state), p.notes && (body.notes = p.notes), p.customFields && typeof p.customFields == "object" && (body.custom_fields = p.customFields);
    try {
      let response = await ninjapipeApiCall({
        auth,
        method: import_pieces_common2.HttpMethod.POST,
        path: "/contacts",
        body
      });
      return flattenCustomFields(response.body);
    } catch (error) {
      let err = error;
      if (((_c = (_b = (_a = err.response) == null ? void 0 : _a.status) != null ? _b : err.status) != null ? _c : 0) === 409 && p.ifContactExists && p.ifContactExists !== "error") {
        let search = await ninjapipeApiCall({
          auth,
          method: import_pieces_common2.HttpMethod.GET,
          path: "/contacts",
          queryParams: { search: p.email, limit: "5" }
        }), existing = extractItems(search.body).find(
          (c) => {
            var _a2;
            return ((_a2 = c.email) == null ? void 0 : _a2.toLowerCase()) === p.email.toLowerCase();
          }
        );
        if (!existing)
          throw new Error("Duplicate detected but could not locate existing contact by email.");
        if (p.ifContactExists === "skip")
          return { skipped: !0, reason: "contact_exists", email: p.email, existing_id: existing.id };
        if (p.ifContactExists === "getExisting")
          return flattenCustomFields(existing);
        if (p.ifContactExists === "updateExisting") {
          let update = await ninjapipeApiCall({
            auth,
            method: import_pieces_common2.HttpMethod.PUT,
            path: `/contacts/${encodeURIComponent(String(existing.id))}`,
            body
          });
          return flattenCustomFields(update.body);
        }
      }
      throw new Error(`Failed to create contact: ${(_d = err.message) != null ? _d : String(error)}`);
    }
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/update-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var updateContact = (0, import_pieces_framework3.createAction)({
  auth: ninjapipeAuth,
  name: "update_contact",
  displayName: "Update Contact",
  description: "Updates an existing contact by ID.",
  audience: "both",
  aiMetadata: { description: "Update an existing contact identified by ID, setting only the fields you provide (name, email, phone, company, status, owner, address fields, notes, or custom fields); omitted fields are left unchanged. Use to edit a known contact, not to create one. Idempotent \u2014 re-sending the same values yields the same result.", idempotent: !0 },
  props: {
    contactId: ninjapipeCommon.contactDropdownRequired,
    firstName: import_pieces_framework3.Property.ShortText({ displayName: "First Name", required: !1 }),
    lastName: import_pieces_framework3.Property.ShortText({ displayName: "Last Name", required: !1 }),
    email: import_pieces_framework3.Property.ShortText({ displayName: "Email", required: !1 }),
    phone: import_pieces_framework3.Property.ShortText({ displayName: "Phone", required: !1 }),
    company: import_pieces_framework3.Property.ShortText({ displayName: "Company", required: !1 }),
    status: import_pieces_framework3.Property.ShortText({ displayName: "Status", required: !1 }),
    owner: import_pieces_framework3.Property.ShortText({ displayName: "Owner", required: !1 }),
    address: import_pieces_framework3.Property.LongText({ displayName: "Address", required: !1 }),
    city: import_pieces_framework3.Property.ShortText({ displayName: "City", required: !1 }),
    zip: import_pieces_framework3.Property.ShortText({ displayName: "ZIP Code", required: !1 }),
    country: ninjapipeCommon.countryDropdown,
    state: import_pieces_framework3.Property.ShortText({ displayName: "State", required: !1 }),
    notes: import_pieces_framework3.Property.LongText({ displayName: "Notes", required: !1 }),
    customFields: import_pieces_framework3.Property.Object({ displayName: "Custom Fields", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.firstName && (body.first_name = p.firstName), p.lastName && (body.last_name = p.lastName), p.email && (body.email = p.email), p.phone && (body.phone = p.phone), p.company && (body.company = p.company), p.status && (body.status = p.status), p.owner && (body.owner = p.owner), p.address && (body.address = p.address), p.city && (body.city = p.city), p.zip && (body.zip = p.zip), p.country && (body.country = p.country), p.state && (body.state = p.state), p.notes && (body.notes = p.notes), p.customFields && typeof p.customFields == "object" && (body.custom_fields = p.customFields);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common3.HttpMethod.PUT,
      path: `/contacts/${encodeURIComponent(String(p.contactId))}`,
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/get-contact.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getContact = (0, import_pieces_framework4.createAction)({
  auth: ninjapipeAuth,
  name: "get_contact",
  displayName: "Get Contact",
  description: "Retrieves a contact by ID.",
  audience: "both",
  aiMetadata: { description: "Fetch a single contact by its ID. Read-only lookup for inspecting one known contact; to search or enumerate contacts (e.g. find one by email) use the list-contacts action instead.", idempotent: !0 },
  props: {
    contactId: ninjapipeCommon.contactDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context), response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common4.HttpMethod.GET,
      path: `/contacts/${encodeURIComponent(String(context.propsValue.contactId))}`
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/list-contacts.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var listContacts = (0, import_pieces_framework5.createAction)({
  auth: ninjapipeAuth,
  name: "list_contacts",
  displayName: "List Contacts",
  description: "Retrieves a list of contacts.",
  audience: "both",
  aiMetadata: { description: 'List contacts with optional search text and status/owner filters. Read-only; supports single-page paging or "Return All" to follow pagination across every page. Use to discover contact IDs or find a contact by name/email before acting on it.', idempotent: !0 },
  props: {
    limit: ninjapipeCommon.limitProperty,
    search: ninjapipeCommon.searchProperty,
    statusFilter: ninjapipeCommon.statusFilterProperty,
    ownerFilter: ninjapipeCommon.ownerFilterProperty,
    returnAll: import_pieces_framework5.Property.Checkbox({
      displayName: "Return All",
      description: "Retrieve all contacts using pagination.",
      required: !1,
      defaultValue: !1
    }),
    page: import_pieces_framework5.Property.Number({
      displayName: "Page",
      description: "Page number when not using Return All.",
      required: !1,
      defaultValue: 1
    })
  },
  async run(context) {
    var _a, _b;
    let auth = getAuth(context), qs = {
      limit: String((_a = context.propsValue.limit) != null ? _a : 20),
      page: String((_b = context.propsValue.page) != null ? _b : 1)
    };
    if (context.propsValue.search && (qs.search = context.propsValue.search), context.propsValue.statusFilter && (qs.status = context.propsValue.statusFilter), context.propsValue.ownerFilter && (qs.owner = context.propsValue.ownerFilter), context.propsValue.returnAll) {
      let results = [], page = 1, limit = 100, hasMore = !0;
      for (; hasMore; ) {
        let response2 = await ninjapipeApiCall({
          auth,
          method: import_pieces_common5.HttpMethod.GET,
          path: "/contacts",
          queryParams: __spreadProps(__spreadValues({}, qs), { limit: String(limit), page: String(page) })
        }), items2 = extractItems(response2.body);
        results.push(...items2), hasMore = items2.length === limit && page < 1e3, page++;
      }
      return flattenArray(results);
    }
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common5.HttpMethod.GET,
      path: "/contacts",
      queryParams: qs
    }), items = extractItems(response.body);
    return flattenArray(items);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/delete-contact.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var deleteContact = (0, import_pieces_framework6.createAction)({
  auth: ninjapipeAuth,
  name: "delete_contact",
  displayName: "Delete Contact",
  description: "Deletes a contact by ID.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a contact identified by its ID. Destructive and not reversible; re-running after the contact is gone will fail since the ID no longer exists.", idempotent: !1 },
  props: {
    contactId: ninjapipeCommon.contactDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context);
    return await ninjapipeApiCall({
      auth,
      method: import_pieces_common6.HttpMethod.DELETE,
      path: `/contacts/${encodeURIComponent(String(context.propsValue.contactId))}`
    }), { success: !0, deleted_id: context.propsValue.contactId };
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/upsert-contact.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var upsertContact = (0, import_pieces_framework7.createAction)({
  auth: ninjapipeAuth,
  name: "upsert_contact",
  displayName: "Upsert Contact",
  description: "Creates a contact or updates an existing one by email.",
  audience: "both",
  aiMetadata: { description: "Create-or-update a contact keyed on email: if a contact with the given email exists it is updated, otherwise a new one is created. Email is required and serves as the stable match key, making this safe to re-run (idempotent) unlike plain create-contact.", idempotent: !0 },
  props: {
    firstName: import_pieces_framework7.Property.ShortText({ displayName: "First Name", required: !1 }),
    lastName: import_pieces_framework7.Property.ShortText({ displayName: "Last Name", required: !1 }),
    email: import_pieces_framework7.Property.ShortText({ displayName: "Email", description: "Used as the unique key to decide create or update.", required: !0 }),
    phone: import_pieces_framework7.Property.ShortText({ displayName: "Phone", required: !1 }),
    company: import_pieces_framework7.Property.ShortText({ displayName: "Company", required: !1 }),
    status: import_pieces_framework7.Property.ShortText({ displayName: "Status", required: !1 }),
    owner: import_pieces_framework7.Property.ShortText({ displayName: "Owner", required: !1 }),
    address: import_pieces_framework7.Property.LongText({ displayName: "Address", required: !1 }),
    city: import_pieces_framework7.Property.ShortText({ displayName: "City", required: !1 }),
    zip: import_pieces_framework7.Property.ShortText({ displayName: "ZIP Code", required: !1 }),
    country: ninjapipeCommon.countryDropdown,
    state: import_pieces_framework7.Property.ShortText({ displayName: "State", required: !1 }),
    notes: import_pieces_framework7.Property.LongText({ displayName: "Notes", required: !1 }),
    customFields: import_pieces_framework7.Property.Object({ displayName: "Custom Fields", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.firstName && (body.first_name = p.firstName), p.lastName && (body.last_name = p.lastName), p.email && (body.email = p.email), p.phone && (body.phone = p.phone), p.company && (body.company = p.company), p.status && (body.status = p.status), p.owner && (body.owner = p.owner), p.address && (body.address = p.address), p.city && (body.city = p.city), p.zip && (body.zip = p.zip), p.country && (body.country = p.country), p.state && (body.state = p.state), p.notes && (body.notes = p.notes), p.customFields && typeof p.customFields == "object" && (body.custom_fields = p.customFields);
    let search = await ninjapipeApiCall({
      auth,
      method: import_pieces_common7.HttpMethod.GET,
      path: "/contacts",
      queryParams: { search: p.email, limit: "5" }
    }), existing = extractItems(search.body).find(
      (c) => {
        var _a;
        return ((_a = c.email) == null ? void 0 : _a.toLowerCase()) === p.email.toLowerCase();
      }
    );
    if (existing) {
      let response2 = await ninjapipeApiCall({
        auth,
        method: import_pieces_common7.HttpMethod.PUT,
        path: `/contacts/${encodeURIComponent(String(existing.id))}`,
        body
      });
      return __spreadProps(__spreadValues({}, flattenCustomFields(response2.body)), { _upsert_action: "updated" });
    }
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common7.HttpMethod.POST,
      path: "/contacts",
      body
    });
    return __spreadProps(__spreadValues({}, flattenCustomFields(response.body)), { _upsert_action: "created" });
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/toggle-client-portal.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var toggleClientPortal = (0, import_pieces_framework8.createAction)({
  auth: ninjapipeAuth,
  name: "toggle_client_portal",
  displayName: "Toggle Client Portal",
  description: "Enable or disable client portal access for a contact. When enabling, NinjaPipe auto-generates a secure password and returns it in the response.",
  audience: "both",
  aiMetadata: { description: "Turns client portal access on or off for a given contact via the Enable toggle; the Enable mode optionally sets a portal email and provisions a fresh auto-generated password. Pick this to grant or revoke a customer-facing login. Re-running with the same toggle leaves access in the same on/off state, though enabling again may rotate the password.", idempotent: !0 },
  props: {
    contactId: ninjapipeCommon.contactDropdownRequired,
    enable: import_pieces_framework8.Property.Checkbox({
      displayName: "Enable Client Portal",
      description: "Turn ON to enable, OFF to disable.",
      required: !0,
      defaultValue: !0
    }),
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Portal Email",
      description: "Optional. Used only when enabling. Defaults to the contact's email.",
      required: !1
    })
  },
  async run(context) {
    let auth = getAuth(context), { contactId, enable, email } = context.propsValue, action = enable ? "enable" : "disable", body = {};
    enable && (email && (body.email = email), body.auto_generate_password = !0);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common8.HttpMethod.PUT,
      path: `/client-portal-accounts/${encodeURIComponent(String(contactId))}/${encodeURIComponent(String(action))}`,
      body: enable ? body : void 0
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/create-company.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var createCompany = (0, import_pieces_framework9.createAction)({
  auth: ninjapipeAuth,
  name: "create_company",
  displayName: "Create Company",
  description: "Creates a new company.",
  audience: "both",
  aiMetadata: { description: "Creates a new company (CRM account) in NinjaPipe with optional industry, contact details, address, status, owner, notes, and custom fields. Only Name is required. Not idempotent: each call adds another company, so check List Companies first to avoid duplicates.", idempotent: !1 },
  props: {
    name: import_pieces_framework9.Property.ShortText({ displayName: "Name", required: !0 }),
    industry: import_pieces_framework9.Property.ShortText({ displayName: "Industry", required: !1 }),
    website: import_pieces_framework9.Property.ShortText({ displayName: "Website", required: !1 }),
    email: import_pieces_framework9.Property.ShortText({ displayName: "Email", required: !1 }),
    phone: import_pieces_framework9.Property.ShortText({ displayName: "Phone", required: !1 }),
    address: import_pieces_framework9.Property.LongText({ displayName: "Address", required: !1 }),
    city: import_pieces_framework9.Property.ShortText({ displayName: "City", required: !1 }),
    zip: import_pieces_framework9.Property.ShortText({ displayName: "ZIP Code", required: !1 }),
    country: ninjapipeCommon.countryDropdown,
    state: import_pieces_framework9.Property.ShortText({ displayName: "State", required: !1 }),
    status: import_pieces_framework9.Property.ShortText({ displayName: "Status", required: !1 }),
    owner: import_pieces_framework9.Property.ShortText({ displayName: "Owner", required: !1 }),
    notes: import_pieces_framework9.Property.LongText({ displayName: "Notes", required: !1 }),
    customFields: import_pieces_framework9.Property.Object({ displayName: "Custom Fields", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.name && (body.name = p.name), p.industry && (body.industry = p.industry), p.website && (body.website = p.website), p.email && (body.email = p.email), p.phone && (body.phone = p.phone), p.address && (body.address = p.address), p.city && (body.city = p.city), p.zip && (body.zip = p.zip), p.country && (body.country = p.country), p.state && (body.state = p.state), p.status && (body.status = p.status), p.owner && (body.owner = p.owner), p.notes && (body.notes = p.notes), p.customFields && typeof p.customFields == "object" && (body.custom_fields = p.customFields);
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common9.HttpMethod.POST, path: "/companies", body });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/update-company.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var updateCompany = (0, import_pieces_framework10.createAction)({
  auth: ninjapipeAuth,
  name: "update_company",
  displayName: "Update Company",
  description: "Updates a company by ID.",
  audience: "both",
  aiMetadata: { description: "Updates an existing company (CRM account) identified by Company ID, applying only the fields you supply (name, contact details, address, status, owner, notes, custom fields); omitted fields are left unchanged. Requires a valid company ID; repeating with the same values is safe.", idempotent: !0 },
  props: {
    companyId: ninjapipeCommon.companyDropdownRequired,
    name: import_pieces_framework10.Property.ShortText({ displayName: "Name", required: !1 }),
    industry: import_pieces_framework10.Property.ShortText({ displayName: "Industry", required: !1 }),
    website: import_pieces_framework10.Property.ShortText({ displayName: "Website", required: !1 }),
    email: import_pieces_framework10.Property.ShortText({ displayName: "Email", required: !1 }),
    phone: import_pieces_framework10.Property.ShortText({ displayName: "Phone", required: !1 }),
    address: import_pieces_framework10.Property.LongText({ displayName: "Address", required: !1 }),
    city: import_pieces_framework10.Property.ShortText({ displayName: "City", required: !1 }),
    zip: import_pieces_framework10.Property.ShortText({ displayName: "ZIP Code", required: !1 }),
    country: ninjapipeCommon.countryDropdown,
    state: import_pieces_framework10.Property.ShortText({ displayName: "State", required: !1 }),
    status: import_pieces_framework10.Property.ShortText({ displayName: "Status", required: !1 }),
    owner: import_pieces_framework10.Property.ShortText({ displayName: "Owner", required: !1 }),
    notes: import_pieces_framework10.Property.LongText({ displayName: "Notes", required: !1 }),
    customFields: import_pieces_framework10.Property.Object({ displayName: "Custom Fields", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.name && (body.name = p.name), p.industry && (body.industry = p.industry), p.website && (body.website = p.website), p.email && (body.email = p.email), p.phone && (body.phone = p.phone), p.address && (body.address = p.address), p.city && (body.city = p.city), p.zip && (body.zip = p.zip), p.country && (body.country = p.country), p.state && (body.state = p.state), p.status && (body.status = p.status), p.owner && (body.owner = p.owner), p.notes && (body.notes = p.notes), p.customFields && typeof p.customFields == "object" && (body.custom_fields = p.customFields);
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common10.HttpMethod.PUT, path: `/companies/${encodeURIComponent(String(p.companyId))}`, body });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/get-company.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var getCompany = (0, import_pieces_framework11.createAction)({
  auth: ninjapipeAuth,
  name: "get_company",
  displayName: "Get Company",
  description: "Retrieves a company by ID.",
  audience: "both",
  aiMetadata: { description: "Fetch a single company record by its ID. Read-only lookup for inspecting one company; to enumerate or search companies use a list action instead.", idempotent: !0 },
  props: {
    companyId: ninjapipeCommon.companyDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context), response = await ninjapipeApiCall({ auth, method: import_pieces_common11.HttpMethod.GET, path: `/companies/${encodeURIComponent(String(context.propsValue.companyId))}` });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/list-companies.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var listCompanies = (0, import_pieces_framework12.createAction)({
  auth: ninjapipeAuth,
  name: "list_companies",
  displayName: "List Companies",
  description: "Retrieves a list of companies.",
  audience: "both",
  aiMetadata: { description: "Read-only listing of companies (CRM accounts) in NinjaPipe, with optional search text, status/owner filters, and pagination. Pick this to browse or find companies; enable Return All to page through every match instead of a single page. Safe to repeat.", idempotent: !0 },
  props: {
    limit: ninjapipeCommon.limitProperty,
    search: ninjapipeCommon.searchProperty,
    statusFilter: ninjapipeCommon.statusFilterProperty,
    ownerFilter: ninjapipeCommon.ownerFilterProperty,
    returnAll: import_pieces_framework12.Property.Checkbox({ displayName: "Return All", description: "Retrieve all companies using pagination.", required: !1, defaultValue: !1 }),
    page: import_pieces_framework12.Property.Number({ displayName: "Page", description: "Page number when not using Return All.", required: !1, defaultValue: 1 })
  },
  async run(context) {
    var _a, _b;
    let auth = getAuth(context), qs = { limit: String((_a = context.propsValue.limit) != null ? _a : 20), page: String((_b = context.propsValue.page) != null ? _b : 1) };
    if (context.propsValue.search && (qs.search = context.propsValue.search), context.propsValue.statusFilter && (qs.status = context.propsValue.statusFilter), context.propsValue.ownerFilter && (qs.owner = context.propsValue.ownerFilter), context.propsValue.returnAll) {
      let results = [], page = 1, limit = 100, hasMore = !0;
      for (; hasMore; ) {
        let response2 = await ninjapipeApiCall({ auth, method: import_pieces_common12.HttpMethod.GET, path: "/companies", queryParams: __spreadProps(__spreadValues({}, qs), { limit: String(limit), page: String(page) }) }), items2 = extractItems(response2.body);
        results.push(...items2), hasMore = items2.length === limit && page < 1e3, page++;
      }
      return flattenArray(results);
    }
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common12.HttpMethod.GET, path: "/companies", queryParams: qs }), items = extractItems(response.body);
    return flattenArray(items);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/delete-company.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var deleteCompany = (0, import_pieces_framework13.createAction)({
  auth: ninjapipeAuth,
  name: "delete_company",
  displayName: "Delete Company",
  description: "Deletes a company by ID.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a company identified by its ID. Destructive and not reversible; re-running after the company is gone will fail since the ID no longer exists.", idempotent: !1 },
  props: {
    companyId: ninjapipeCommon.companyDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context);
    return await ninjapipeApiCall({ auth, method: import_pieces_common13.HttpMethod.DELETE, path: `/companies/${encodeURIComponent(String(context.propsValue.companyId))}` }), { success: !0, deleted_id: context.propsValue.companyId };
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/create-deal.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var createDeal = (0, import_pieces_framework14.createAction)({
  auth: ninjapipeAuth,
  name: "create_deal",
  displayName: "Create Deal",
  description: "Creates a new deal.",
  audience: "both",
  aiMetadata: { description: "Creates a new deal (sales opportunity) in NinjaPipe, optionally tied to a pipeline, company, and contact, with value/currency, status, owner, and custom fields. Only Name is required. Not idempotent: each call adds another deal, so guard against duplicates.", idempotent: !1 },
  props: {
    name: import_pieces_framework14.Property.ShortText({ displayName: "Name", required: !0 }),
    title: import_pieces_framework14.Property.ShortText({ displayName: "Title", required: !1 }),
    status: import_pieces_framework14.Property.ShortText({ displayName: "Status", required: !1 }),
    value: import_pieces_framework14.Property.Number({ displayName: "Value", required: !1 }),
    currency: import_pieces_framework14.Property.ShortText({ displayName: "Currency", description: "e.g. EUR, USD", required: !1 }),
    owner: import_pieces_framework14.Property.ShortText({ displayName: "Owner", required: !1 }),
    pipelineId: ninjapipeCommon.pipelineDropdown,
    companyId: ninjapipeCommon.companyDropdown,
    contactId: ninjapipeCommon.contactDropdown,
    notes: import_pieces_framework14.Property.LongText({ displayName: "Notes", required: !1 }),
    customFields: import_pieces_framework14.Property.Object({ displayName: "Custom Fields", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.name && (body.name = p.name), p.title && (body.title = p.title), p.status && (body.status = p.status), p.value !== void 0 && (body.value = p.value), p.currency && (body.currency = p.currency), p.owner && (body.owner = p.owner), p.pipelineId && (body.pipeline_id = p.pipelineId), p.companyId && (body.company_id = p.companyId), p.contactId && (body.contact_id = p.contactId), p.notes && (body.notes = p.notes), p.customFields && typeof p.customFields == "object" && (body.custom_fields = p.customFields);
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common14.HttpMethod.POST, path: "/deals", body });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/update-deal.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var updateDeal = (0, import_pieces_framework15.createAction)({
  auth: ninjapipeAuth,
  name: "update_deal",
  displayName: "Update Deal",
  description: "Updates a deal by ID.",
  audience: "both",
  aiMetadata: { description: "Update an existing deal identified by its ID. Only supplied fields are changed (name, status, value, currency, owner, or associated pipeline/company/contact). Repeating the same update is idempotent.", idempotent: !0 },
  props: {
    dealId: ninjapipeCommon.dealDropdownRequired,
    name: import_pieces_framework15.Property.ShortText({ displayName: "Name", required: !1 }),
    title: import_pieces_framework15.Property.ShortText({ displayName: "Title", required: !1 }),
    status: import_pieces_framework15.Property.ShortText({ displayName: "Status", required: !1 }),
    value: import_pieces_framework15.Property.Number({ displayName: "Value", required: !1 }),
    currency: import_pieces_framework15.Property.ShortText({ displayName: "Currency", required: !1 }),
    owner: import_pieces_framework15.Property.ShortText({ displayName: "Owner", required: !1 }),
    pipelineId: ninjapipeCommon.pipelineDropdown,
    companyId: ninjapipeCommon.companyDropdown,
    contactId: ninjapipeCommon.contactDropdown,
    notes: import_pieces_framework15.Property.LongText({ displayName: "Notes", required: !1 }),
    customFields: import_pieces_framework15.Property.Object({ displayName: "Custom Fields", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.name && (body.name = p.name), p.title && (body.title = p.title), p.status && (body.status = p.status), p.value !== void 0 && (body.value = p.value), p.currency && (body.currency = p.currency), p.owner && (body.owner = p.owner), p.pipelineId && (body.pipeline_id = p.pipelineId), p.companyId && (body.company_id = p.companyId), p.contactId && (body.contact_id = p.contactId), p.notes && (body.notes = p.notes), p.customFields && typeof p.customFields == "object" && (body.custom_fields = p.customFields);
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common15.HttpMethod.PUT, path: `/deals/${encodeURIComponent(String(p.dealId))}`, body });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/get-deal.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var getDeal = (0, import_pieces_framework16.createAction)({
  auth: ninjapipeAuth,
  name: "get_deal",
  displayName: "Get Deal",
  description: "Retrieves a deal by ID.",
  audience: "both",
  aiMetadata: { description: "Fetches a single deal by its ID, returning its full record. Pick this when you already have a deal ID and need its details; use List Deals instead to search or browse. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    dealId: ninjapipeCommon.dealDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context), response = await ninjapipeApiCall({ auth, method: import_pieces_common16.HttpMethod.GET, path: `/deals/${encodeURIComponent(String(context.propsValue.dealId))}` });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/list-deals.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var listDeals = (0, import_pieces_framework17.createAction)({
  auth: ninjapipeAuth,
  name: "list_deals",
  displayName: "List Deals",
  description: "Retrieves a list of deals.",
  audience: "both",
  aiMetadata: { description: "Read-only listing of deals (sales opportunities) in NinjaPipe, with optional search text, status/owner filters, and pagination. Pick this to browse or find deals; enable Return All to page through every match instead of a single page. Safe to repeat.", idempotent: !0 },
  props: {
    limit: ninjapipeCommon.limitProperty,
    search: ninjapipeCommon.searchProperty,
    statusFilter: ninjapipeCommon.statusFilterProperty,
    ownerFilter: ninjapipeCommon.ownerFilterProperty,
    returnAll: import_pieces_framework17.Property.Checkbox({ displayName: "Return All", description: "Retrieve all deals using pagination.", required: !1, defaultValue: !1 }),
    page: import_pieces_framework17.Property.Number({ displayName: "Page", description: "Page number when not using Return All.", required: !1, defaultValue: 1 })
  },
  async run(context) {
    var _a, _b;
    let auth = getAuth(context), qs = { limit: String((_a = context.propsValue.limit) != null ? _a : 20), page: String((_b = context.propsValue.page) != null ? _b : 1) };
    if (context.propsValue.search && (qs.search = context.propsValue.search), context.propsValue.statusFilter && (qs.status = context.propsValue.statusFilter), context.propsValue.ownerFilter && (qs.owner = context.propsValue.ownerFilter), context.propsValue.returnAll) {
      let results = [], page = 1, limit = 100, hasMore = !0;
      for (; hasMore; ) {
        let response2 = await ninjapipeApiCall({ auth, method: import_pieces_common17.HttpMethod.GET, path: "/deals", queryParams: __spreadProps(__spreadValues({}, qs), { limit: String(limit), page: String(page) }) }), items2 = extractItems(response2.body);
        results.push(...items2), hasMore = items2.length === limit && page < 1e3, page++;
      }
      return flattenArray(results);
    }
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common17.HttpMethod.GET, path: "/deals", queryParams: qs }), items = extractItems(response.body);
    return flattenArray(items);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/delete-deal.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common");
var deleteDeal = (0, import_pieces_framework18.createAction)({
  auth: ninjapipeAuth,
  name: "delete_deal",
  displayName: "Delete Deal",
  description: "Deletes a deal by ID.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a deal identified by its ID. Destructive and not reversible; re-running after the deal is gone will fail since the ID no longer exists.", idempotent: !1 },
  props: {
    dealId: ninjapipeCommon.dealDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context);
    return await ninjapipeApiCall({ auth, method: import_pieces_common18.HttpMethod.DELETE, path: `/deals/${encodeURIComponent(String(context.propsValue.dealId))}` }), { success: !0, deleted_id: context.propsValue.dealId };
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/create-product.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common");
var createProduct = (0, import_pieces_framework19.createAction)({
  auth: ninjapipeAuth,
  name: "create_product",
  displayName: "Create Product",
  description: "Creates a new product.",
  audience: "both",
  aiMetadata: { description: "Create a new product with a required name, SKU, and price; optionally set description, currency, status, and custom fields. The SKU must be unique within the workspace, so a duplicate SKU is rejected. Not idempotent \u2014 each successful call creates a distinct product.", idempotent: !1 },
  props: {
    name: import_pieces_framework19.Property.ShortText({ displayName: "Name", description: "Product name.", required: !0 }),
    sku: import_pieces_framework19.Property.ShortText({
      displayName: "SKU",
      description: "Product SKU. Must be unique within your workspace.",
      required: !0
    }),
    price: import_pieces_framework19.Property.Number({
      displayName: "Price",
      description: "Product price.",
      required: !0
    }),
    description: import_pieces_framework19.Property.LongText({ displayName: "Description", required: !1 }),
    currency: import_pieces_framework19.Property.ShortText({ displayName: "Currency", required: !1 }),
    status: import_pieces_framework19.Property.ShortText({ displayName: "Status", required: !1 }),
    customFields: import_pieces_framework19.Property.Object({ displayName: "Custom Fields", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.name && (body.name = p.name), p.sku && (body.sku = p.sku), p.description && (body.description = p.description), p.price !== void 0 && (body.price = p.price), p.currency && (body.currency = p.currency), p.status && (body.status = p.status), p.customFields && typeof p.customFields == "object" && (body.custom_fields = p.customFields);
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common19.HttpMethod.POST, path: "/products", body });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/update-product.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_common20 = require("@activepieces/pieces-common");
var updateProduct = (0, import_pieces_framework20.createAction)({
  auth: ninjapipeAuth,
  name: "update_product",
  displayName: "Update Product",
  description: "Updates a product by ID.",
  audience: "both",
  aiMetadata: { description: "Updates an existing product identified by Product ID, applying only the fields you supply (name, SKU, description, price, currency, status, custom fields); omitted fields are left unchanged. Requires a valid product ID; repeating with the same values is safe.", idempotent: !0 },
  props: {
    productId: ninjapipeCommon.productDropdownRequired,
    name: import_pieces_framework20.Property.ShortText({ displayName: "Name", required: !1 }),
    sku: import_pieces_framework20.Property.ShortText({ displayName: "SKU", required: !1 }),
    description: import_pieces_framework20.Property.LongText({ displayName: "Description", required: !1 }),
    price: import_pieces_framework20.Property.Number({ displayName: "Price", required: !1 }),
    currency: import_pieces_framework20.Property.ShortText({ displayName: "Currency", required: !1 }),
    status: import_pieces_framework20.Property.ShortText({ displayName: "Status", required: !1 }),
    customFields: import_pieces_framework20.Property.Object({ displayName: "Custom Fields", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.name && (body.name = p.name), p.sku && (body.sku = p.sku), p.description && (body.description = p.description), p.price !== void 0 && (body.price = p.price), p.currency && (body.currency = p.currency), p.status && (body.status = p.status), p.customFields && typeof p.customFields == "object" && (body.custom_fields = p.customFields);
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common20.HttpMethod.PUT, path: `/products/${encodeURIComponent(String(p.productId))}`, body });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/get-product.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common21 = require("@activepieces/pieces-common");
var getProduct = (0, import_pieces_framework21.createAction)({
  auth: ninjapipeAuth,
  name: "get_product",
  displayName: "Get Product",
  description: "Retrieves a product by ID.",
  audience: "both",
  aiMetadata: { description: "Fetch a single product by its ID to read its current details. Use when you already have the product ID; to find a product by name or SKU, list/search products first. Read-only and idempotent.", idempotent: !0 },
  props: {
    productId: ninjapipeCommon.productDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context), response = await ninjapipeApiCall({ auth, method: import_pieces_common21.HttpMethod.GET, path: `/products/${encodeURIComponent(String(context.propsValue.productId))}` });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/list-products.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common22 = require("@activepieces/pieces-common");
var listProducts = (0, import_pieces_framework22.createAction)({
  auth: ninjapipeAuth,
  name: "list_products",
  displayName: "List Products",
  description: "Retrieves a list of products.",
  audience: "both",
  aiMetadata: { description: 'List products with optional search text. Read-only; supports single-page paging or "Return All" to follow pagination across every page. Use to discover product IDs or look up a product by name.', idempotent: !0 },
  props: {
    limit: ninjapipeCommon.limitProperty,
    search: ninjapipeCommon.searchProperty,
    returnAll: import_pieces_framework22.Property.Checkbox({ displayName: "Return All", description: "Retrieve all products using pagination.", required: !1, defaultValue: !1 }),
    page: import_pieces_framework22.Property.Number({ displayName: "Page", description: "Page number when not using Return All.", required: !1, defaultValue: 1 })
  },
  async run(context) {
    var _a, _b;
    let auth = getAuth(context), qs = { limit: String((_a = context.propsValue.limit) != null ? _a : 20), page: String((_b = context.propsValue.page) != null ? _b : 1) };
    if (context.propsValue.search && (qs.search = context.propsValue.search), context.propsValue.returnAll) {
      let results = [], page = 1, limit = 100, hasMore = !0;
      for (; hasMore; ) {
        let response2 = await ninjapipeApiCall({ auth, method: import_pieces_common22.HttpMethod.GET, path: "/products", queryParams: __spreadProps(__spreadValues({}, qs), { limit: String(limit), page: String(page) }) }), items2 = extractItems(response2.body);
        results.push(...items2), hasMore = items2.length === limit && page < 1e3, page++;
      }
      return flattenArray(results);
    }
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common22.HttpMethod.GET, path: "/products", queryParams: qs }), items = extractItems(response.body);
    return flattenArray(items);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/delete-product.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_common23 = require("@activepieces/pieces-common");
var deleteProduct = (0, import_pieces_framework23.createAction)({
  auth: ninjapipeAuth,
  name: "delete_product",
  displayName: "Delete Product",
  description: "Deletes a product by ID.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a product identified by Product ID. Pick this to remove a product from the catalog; the change is destructive and cannot be undone. Re-running after deletion typically fails since the ID no longer exists.", idempotent: !1 },
  props: {
    productId: ninjapipeCommon.productDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context);
    return await ninjapipeApiCall({ auth, method: import_pieces_common23.HttpMethod.DELETE, path: `/products/${encodeURIComponent(String(context.propsValue.productId))}` }), { success: !0, deleted_id: context.propsValue.productId };
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/create-budget.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework"), import_pieces_common24 = require("@activepieces/pieces-common");
var createBudget = (0, import_pieces_framework24.createAction)({
  auth: ninjapipeAuth,
  name: "create_budget",
  displayName: "Create Budget",
  description: "Creates a new budget.",
  audience: "both",
  aiMetadata: { description: "Create a new budget in NinjaPipe with a name and an allocated amount (which must be greater than 0); optionally set category, period, currency (defaults to USD), status, and start/end dates. Not idempotent \u2014 each call creates a separate budget, so calling repeatedly produces duplicates.", idempotent: !1 },
  props: {
    name: import_pieces_framework24.Property.ShortText({ displayName: "Name", description: "Budget name.", required: !0 }),
    allocated: import_pieces_framework24.Property.Number({
      displayName: "Allocated Amount",
      description: "Total allocated budget amount. Must be greater than 0.",
      required: !0
    }),
    description: import_pieces_framework24.Property.LongText({ displayName: "Description", required: !1 }),
    category: ninjapipeCommon.budgetCategoryDropdown,
    period: ninjapipeCommon.budgetPeriodDropdown,
    currency: import_pieces_framework24.Property.ShortText({
      displayName: "Currency",
      description: "Currency code, e.g. USD, EUR. Defaults to USD.",
      required: !1
    }),
    status: ninjapipeCommon.budgetStatusDropdown,
    startDate: import_pieces_framework24.Property.DateTime({
      displayName: "Start Date",
      required: !1
    }),
    endDate: import_pieces_framework24.Property.DateTime({
      displayName: "End Date",
      required: !1
    })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {
      name: p.name,
      allocated: p.allocated
    };
    p.description && (body.description = p.description), p.category && (body.category = p.category), p.period && (body.period = p.period), p.currency && (body.currency = p.currency), p.status && (body.status = p.status);
    {
      let v = toDateOnly(p.startDate);
      v && (body.start_date = v);
    }
    {
      let v = toDateOnly(p.endDate);
      v && (body.end_date = v);
    }
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common24.HttpMethod.POST,
      path: "/budgets",
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/update-budget.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework"), import_pieces_common25 = require("@activepieces/pieces-common");
var updateBudget = (0, import_pieces_framework25.createAction)({
  auth: ninjapipeAuth,
  name: "update_budget",
  displayName: "Update Budget",
  description: "Updates a budget by ID.",
  audience: "both",
  aiMetadata: { description: "Update an existing budget identified by its ID. Only supplied fields change (name, allocated amount, category, period, currency, status, start/end dates); allocated must be greater than 0 if set. Repeating the same update is idempotent.", idempotent: !0 },
  props: {
    budgetId: ninjapipeCommon.budgetDropdownRequired,
    name: import_pieces_framework25.Property.ShortText({ displayName: "Name", required: !1 }),
    allocated: import_pieces_framework25.Property.Number({
      displayName: "Allocated Amount",
      description: "Total allocated budget amount. Must be greater than 0 if set.",
      required: !1
    }),
    description: import_pieces_framework25.Property.LongText({ displayName: "Description", required: !1 }),
    category: ninjapipeCommon.budgetCategoryDropdown,
    period: ninjapipeCommon.budgetPeriodDropdown,
    currency: import_pieces_framework25.Property.ShortText({ displayName: "Currency", required: !1 }),
    status: ninjapipeCommon.budgetStatusDropdown,
    startDate: import_pieces_framework25.Property.DateTime({ displayName: "Start Date", required: !1 }),
    endDate: import_pieces_framework25.Property.DateTime({ displayName: "End Date", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.name && (body.name = p.name), p.allocated !== void 0 && p.allocated !== null && (body.allocated = p.allocated), p.description !== void 0 && (body.description = p.description), p.category && (body.category = p.category), p.period && (body.period = p.period), p.currency && (body.currency = p.currency), p.status && (body.status = p.status);
    {
      let v = toDateOnly(p.startDate);
      v && (body.start_date = v);
    }
    {
      let v = toDateOnly(p.endDate);
      v && (body.end_date = v);
    }
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common25.HttpMethod.PUT,
      path: `/budgets/${encodeURIComponent(String(p.budgetId))}`,
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/get-budget.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework"), import_pieces_common26 = require("@activepieces/pieces-common");
var getBudget = (0, import_pieces_framework26.createAction)({
  auth: ninjapipeAuth,
  name: "get_budget",
  displayName: "Get Budget",
  description: "Retrieves a budget by ID.",
  audience: "both",
  aiMetadata: { description: "Fetch a single budget by its ID. Read-only lookup for inspecting one budget; to enumerate or search budgets use a list action instead.", idempotent: !0 },
  props: {
    budgetId: ninjapipeCommon.budgetDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context), response = await ninjapipeApiCall({ auth, method: import_pieces_common26.HttpMethod.GET, path: `/budgets/${encodeURIComponent(String(context.propsValue.budgetId))}` });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/list-budgets.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework"), import_pieces_common27 = require("@activepieces/pieces-common");
var listBudgets = (0, import_pieces_framework27.createAction)({
  auth: ninjapipeAuth,
  name: "list_budgets",
  displayName: "List Budgets",
  description: "Retrieves a list of budgets.",
  audience: "both",
  aiMetadata: { description: "Read-only listing of budgets in NinjaPipe, with optional search text and pagination. Pick this to browse or find budgets; enable Return All to page through every match instead of a single page. Safe to repeat.", idempotent: !0 },
  props: {
    limit: ninjapipeCommon.limitProperty,
    search: ninjapipeCommon.searchProperty,
    returnAll: import_pieces_framework27.Property.Checkbox({ displayName: "Return All", description: "Retrieve all budgets using pagination.", required: !1, defaultValue: !1 }),
    page: import_pieces_framework27.Property.Number({ displayName: "Page", description: "Page number when not using Return All.", required: !1, defaultValue: 1 })
  },
  async run(context) {
    var _a, _b;
    let auth = getAuth(context), qs = { limit: String((_a = context.propsValue.limit) != null ? _a : 20), page: String((_b = context.propsValue.page) != null ? _b : 1) };
    if (context.propsValue.search && (qs.search = context.propsValue.search), context.propsValue.returnAll) {
      let results = [], page = 1, limit = 100, hasMore = !0;
      for (; hasMore; ) {
        let response2 = await ninjapipeApiCall({ auth, method: import_pieces_common27.HttpMethod.GET, path: "/budgets", queryParams: __spreadProps(__spreadValues({}, qs), { limit: String(limit), page: String(page) }) }), items2 = extractItems(response2.body);
        results.push(...items2), hasMore = items2.length === limit && page < 1e3, page++;
      }
      return flattenArray(results);
    }
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common27.HttpMethod.GET, path: "/budgets", queryParams: qs }), items = extractItems(response.body);
    return flattenArray(items);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/delete-budget.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework"), import_pieces_common28 = require("@activepieces/pieces-common");
var deleteBudget = (0, import_pieces_framework28.createAction)({
  auth: ninjapipeAuth,
  name: "delete_budget",
  displayName: "Delete Budget",
  description: "Deletes a budget by ID.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a budget identified by Budget ID. Pick this to remove a budget and its tracking; the change is destructive and cannot be undone. Re-running after deletion typically fails since the ID no longer exists.", idempotent: !1 },
  props: {
    budgetId: ninjapipeCommon.budgetDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context);
    return await ninjapipeApiCall({ auth, method: import_pieces_common28.HttpMethod.DELETE, path: `/budgets/${encodeURIComponent(String(context.propsValue.budgetId))}` }), { success: !0, deleted_id: context.propsValue.budgetId };
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/create-budget-expense.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework"), import_pieces_common29 = require("@activepieces/pieces-common");
var createBudgetExpense = (0, import_pieces_framework29.createAction)({
  auth: ninjapipeAuth,
  name: "create_budget_expense",
  displayName: "Create Budget Expense",
  description: "Adds an expense to a budget. The expense amount is added to the budget's spent amount.",
  audience: "both",
  aiMetadata: { description: "Records an expense against a specific budget, increasing that budget's spent total by the amount given. Requires budget ID, description, amount, category, and date. Not idempotent: each call adds another expense and further increases the spent amount, so avoid duplicate retries.", idempotent: !1 },
  props: {
    budgetId: ninjapipeCommon.budgetDropdownRequired,
    description: import_pieces_framework29.Property.ShortText({
      displayName: "Description",
      description: "Description of the expense.",
      required: !0
    }),
    amount: import_pieces_framework29.Property.Number({
      displayName: "Amount",
      description: "Expense amount (positive number).",
      required: !0
    }),
    category: import_pieces_framework29.Property.ShortText({
      displayName: "Category",
      description: "Expense category (e.g. Marketing, Travel).",
      required: !0
    }),
    date: import_pieces_framework29.Property.DateTime({
      displayName: "Date",
      description: "Expense date.",
      required: !0
    })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, date = toDateOnly(p.date);
    if (!date) throw new Error("Date is required.");
    let body = {
      description: p.description,
      amount: p.amount,
      category: p.category,
      date
    }, response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common29.HttpMethod.POST,
      path: `/budgets/${encodeURIComponent(String(p.budgetId))}/expenses`,
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/create-project.ts
var import_pieces_framework30 = require("@activepieces/pieces-framework"), import_pieces_common30 = require("@activepieces/pieces-common");
var createProject = (0, import_pieces_framework30.createAction)({
  auth: ninjapipeAuth,
  name: "create_project",
  displayName: "Create Project",
  description: "Creates a new project.",
  audience: "both",
  aiMetadata: { description: "Creates a new project in NinjaPipe with optional description, status, priority, due date, linked contact, team members, tags, and extra settings. Only Name is required. Not idempotent: each call creates another project, so avoid retries that would duplicate it.", idempotent: !1 },
  props: {
    name: import_pieces_framework30.Property.ShortText({ displayName: "Name", required: !0 }),
    description: import_pieces_framework30.Property.LongText({ displayName: "Description", required: !1 }),
    status: ninjapipeCommon.projectStatusDropdown,
    priority: ninjapipeCommon.priorityDropdown,
    dueDate: import_pieces_framework30.Property.DateTime({
      displayName: "Due Date",
      required: !1
    }),
    contactId: ninjapipeCommon.contactDropdown,
    team: import_pieces_framework30.Property.Array({
      displayName: "Team",
      description: "Optional list of team members (account IDs).",
      required: !1
    }),
    tags: import_pieces_framework30.Property.Array({ displayName: "Tags", required: !1 }),
    settingsJson: import_pieces_framework30.Property.Object({
      displayName: "Extra Settings",
      description: "Optional metadata (e.g. linked contact, custom fields).",
      required: !1
    })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = { name: p.name };
    p.description && (body.description = p.description), p.status && (body.status = p.status), p.priority && (body.priority = p.priority);
    {
      let v = toDateOnly(p.dueDate);
      v && (body.due_date = v);
    }
    p.team && Array.isArray(p.team) && p.team.length > 0 && (body.team = p.team), p.tags && Array.isArray(p.tags) && p.tags.length > 0 && (body.tags = p.tags);
    let settings = p.settingsJson && typeof p.settingsJson == "object" ? __spreadValues({}, p.settingsJson) : {};
    p.contactId && (settings.contact_id = p.contactId), Object.keys(settings).length > 0 && (body.settings_json = settings);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common30.HttpMethod.POST,
      path: "/projects",
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/update-project.ts
var import_pieces_framework31 = require("@activepieces/pieces-framework"), import_pieces_common31 = require("@activepieces/pieces-common");
var updateProject = (0, import_pieces_framework31.createAction)({
  auth: ninjapipeAuth,
  name: "update_project",
  displayName: "Update Project",
  description: "Updates a project by ID.",
  audience: "both",
  aiMetadata: { description: "Updates an existing project identified by Project ID, applying only the fields you supply (name, description, status, priority, due date, team, tags, extra settings); omitted fields are left unchanged. Requires a valid project ID; repeating with the same values is safe.", idempotent: !0 },
  props: {
    projectId: ninjapipeCommon.projectDropdownRequired,
    name: import_pieces_framework31.Property.ShortText({ displayName: "Name", required: !1 }),
    description: import_pieces_framework31.Property.LongText({ displayName: "Description", required: !1 }),
    status: ninjapipeCommon.projectStatusDropdown,
    priority: ninjapipeCommon.priorityDropdown,
    dueDate: import_pieces_framework31.Property.DateTime({ displayName: "Due Date", required: !1 }),
    team: import_pieces_framework31.Property.Array({ displayName: "Team", required: !1 }),
    tags: import_pieces_framework31.Property.Array({ displayName: "Tags", required: !1 }),
    settingsJson: import_pieces_framework31.Property.Object({ displayName: "Extra Settings", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.name && (body.name = p.name), p.description !== void 0 && (body.description = p.description), p.status && (body.status = p.status), p.priority && (body.priority = p.priority);
    {
      let v = toDateOnly(p.dueDate);
      v && (body.due_date = v);
    }
    p.team && Array.isArray(p.team) && p.team.length > 0 && (body.team = p.team), p.tags && Array.isArray(p.tags) && p.tags.length > 0 && (body.tags = p.tags), p.settingsJson && typeof p.settingsJson == "object" && Object.keys(p.settingsJson).length > 0 && (body.settings_json = p.settingsJson);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common31.HttpMethod.PUT,
      path: `/projects/${encodeURIComponent(String(p.projectId))}`,
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/get-project.ts
var import_pieces_framework32 = require("@activepieces/pieces-framework"), import_pieces_common32 = require("@activepieces/pieces-common");
var getProject = (0, import_pieces_framework32.createAction)({
  auth: ninjapipeAuth,
  name: "get_project",
  displayName: "Get Project",
  description: "Retrieves a project by ID.",
  audience: "both",
  aiMetadata: { description: "Fetch a single project by its ID. Read-only lookup for inspecting one project; to enumerate or search projects use the list-projects action instead.", idempotent: !0 },
  props: {
    projectId: ninjapipeCommon.projectDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context), response = await ninjapipeApiCall({ auth, method: import_pieces_common32.HttpMethod.GET, path: `/projects/${encodeURIComponent(String(context.propsValue.projectId))}` });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/list-projects.ts
var import_pieces_framework33 = require("@activepieces/pieces-framework"), import_pieces_common33 = require("@activepieces/pieces-common");
var listProjects = (0, import_pieces_framework33.createAction)({
  auth: ninjapipeAuth,
  name: "list_projects",
  displayName: "List Projects",
  description: "Retrieves a list of projects.",
  audience: "both",
  aiMetadata: { description: 'List projects with optional search text and status/owner filters. Read-only; supports single-page paging or "Return All" to follow pagination across every page. Use to discover project IDs before fetching or operating on a project.', idempotent: !0 },
  props: {
    limit: ninjapipeCommon.limitProperty,
    search: ninjapipeCommon.searchProperty,
    statusFilter: ninjapipeCommon.statusFilterProperty,
    ownerFilter: ninjapipeCommon.ownerFilterProperty,
    returnAll: import_pieces_framework33.Property.Checkbox({ displayName: "Return All", description: "Retrieve all projects using pagination.", required: !1, defaultValue: !1 }),
    page: import_pieces_framework33.Property.Number({ displayName: "Page", description: "Page number when not using Return All.", required: !1, defaultValue: 1 })
  },
  async run(context) {
    var _a, _b;
    let auth = getAuth(context), qs = { limit: String((_a = context.propsValue.limit) != null ? _a : 20), page: String((_b = context.propsValue.page) != null ? _b : 1) };
    if (context.propsValue.search && (qs.search = context.propsValue.search), context.propsValue.statusFilter && (qs.status = context.propsValue.statusFilter), context.propsValue.ownerFilter && (qs.owner = context.propsValue.ownerFilter), context.propsValue.returnAll) {
      let results = [], page = 1, limit = 100, hasMore = !0;
      for (; hasMore; ) {
        let response2 = await ninjapipeApiCall({ auth, method: import_pieces_common33.HttpMethod.GET, path: "/projects", queryParams: __spreadProps(__spreadValues({}, qs), { limit: String(limit), page: String(page) }) }), items2 = extractItems(response2.body);
        results.push(...items2), hasMore = items2.length === limit && page < 1e3, page++;
      }
      return flattenArray(results);
    }
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common33.HttpMethod.GET, path: "/projects", queryParams: qs }), items = extractItems(response.body);
    return flattenArray(items);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/delete-project.ts
var import_pieces_framework34 = require("@activepieces/pieces-framework"), import_pieces_common34 = require("@activepieces/pieces-common");
var deleteProject = (0, import_pieces_framework34.createAction)({
  auth: ninjapipeAuth,
  name: "delete_project",
  displayName: "Delete Project",
  description: "Deletes a project by ID.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a project identified by Project ID, including its tasks. Pick this to remove a project entirely; the change is destructive and cannot be undone. Re-running after deletion typically fails since the ID no longer exists.", idempotent: !1 },
  props: {
    projectId: ninjapipeCommon.projectDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context);
    return await ninjapipeApiCall({ auth, method: import_pieces_common34.HttpMethod.DELETE, path: `/projects/${encodeURIComponent(String(context.propsValue.projectId))}` }), { success: !0, deleted_id: context.propsValue.projectId };
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/create-task.ts
var import_pieces_framework35 = require("@activepieces/pieces-framework"), import_pieces_common35 = require("@activepieces/pieces-common");
var createTask = (0, import_pieces_framework35.createAction)({
  auth: ninjapipeAuth,
  name: "create_task",
  displayName: "Create Task",
  description: "Creates a task or subtask in a project.",
  audience: "both",
  aiMetadata: { description: "Creates a task inside a given project, with optional description, status, priority, assignee, due date, tags, and settings. Set Parent Task ID to instead create a subtask under an existing task in the same project. Requires a project ID and title; not idempotent, so each call adds another task.", idempotent: !1 },
  props: {
    projectId: ninjapipeCommon.projectDropdownRequired,
    title: import_pieces_framework35.Property.ShortText({ displayName: "Title", description: "Task title.", required: !0 }),
    description: import_pieces_framework35.Property.LongText({ displayName: "Description", required: !1 }),
    status: ninjapipeCommon.taskStatusDropdown,
    priority: ninjapipeCommon.priorityDropdown,
    assigneeId: import_pieces_framework35.Property.ShortText({
      displayName: "Assignee ID",
      description: "Account ID of the user to assign this task to.",
      required: !1
    }),
    dueDate: import_pieces_framework35.Property.DateTime({
      displayName: "Due Date",
      required: !1
    }),
    parentId: import_pieces_framework35.Property.ShortText({
      displayName: "Parent Task ID",
      description: "Set to make this a subtask of another task in the same project.",
      required: !1
    }),
    tags: import_pieces_framework35.Property.Array({ displayName: "Tags", required: !1 }),
    settingsJson: import_pieces_framework35.Property.Object({
      displayName: "Extra Settings",
      description: "Additional task settings as a JSON object.",
      required: !1
    })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = { title: p.title };
    p.description && (body.description = p.description), p.status && (body.status = p.status), p.priority && (body.priority = p.priority), p.assigneeId && (body.assignee_id = p.assigneeId);
    {
      let v = toDateOnly(p.dueDate);
      v && (body.due_date = v);
    }
    p.parentId && (body.parent_id = p.parentId), p.tags && Array.isArray(p.tags) && p.tags.length > 0 && (body.tags = p.tags), p.settingsJson && typeof p.settingsJson == "object" && Object.keys(p.settingsJson).length > 0 && (body.settings_json = p.settingsJson);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common35.HttpMethod.POST,
      path: `/projects/${encodeURIComponent(String(p.projectId))}/tasks`,
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/update-task.ts
var import_pieces_framework36 = require("@activepieces/pieces-framework"), import_pieces_common36 = require("@activepieces/pieces-common");
var updateTask = (0, import_pieces_framework36.createAction)({
  auth: ninjapipeAuth,
  name: "update_task",
  displayName: "Update Task",
  description: "Updates a task within a project.",
  audience: "both",
  aiMetadata: { description: "Update fields on an existing task, identified by its project ID and task ID. Only provided fields are changed; use this to set status, priority, assignee, due date, or to re-parent a subtask within the same project. Repeating the same update is idempotent.", idempotent: !0 },
  props: {
    projectId: ninjapipeCommon.projectDropdownRequired,
    taskId: ninjapipeCommon.taskDropdown,
    title: import_pieces_framework36.Property.ShortText({ displayName: "Title", required: !1 }),
    description: import_pieces_framework36.Property.LongText({ displayName: "Description", required: !1 }),
    status: ninjapipeCommon.taskStatusDropdown,
    priority: ninjapipeCommon.priorityDropdown,
    assigneeId: import_pieces_framework36.Property.ShortText({
      displayName: "Assignee ID",
      description: "Account ID of the user to assign this task to.",
      required: !1
    }),
    dueDate: import_pieces_framework36.Property.DateTime({
      displayName: "Due Date",
      required: !1
    }),
    parentId: import_pieces_framework36.Property.ShortText({
      displayName: "Parent Task ID",
      description: "Move under another task in the same project.",
      required: !1
    }),
    tags: import_pieces_framework36.Property.Array({ displayName: "Tags", required: !1 }),
    settingsJson: import_pieces_framework36.Property.Object({ displayName: "Extra Settings", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.title && (body.title = p.title), p.description !== void 0 && (body.description = p.description), p.status && (body.status = p.status), p.priority && (body.priority = p.priority), p.assigneeId && (body.assignee_id = p.assigneeId);
    {
      let v = toDateOnly(p.dueDate);
      v && (body.due_date = v);
    }
    p.parentId && (body.parent_id = p.parentId), p.tags && Array.isArray(p.tags) && p.tags.length > 0 && (body.tags = p.tags), p.settingsJson && typeof p.settingsJson == "object" && Object.keys(p.settingsJson).length > 0 && (body.settings_json = p.settingsJson);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common36.HttpMethod.PUT,
      path: `/projects/${encodeURIComponent(String(p.projectId))}/tasks/${encodeURIComponent(String(p.taskId))}`,
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/get-task.ts
var import_pieces_framework37 = require("@activepieces/pieces-framework"), import_pieces_common37 = require("@activepieces/pieces-common");
var getTask = (0, import_pieces_framework37.createAction)({
  auth: ninjapipeAuth,
  name: "get_task",
  displayName: "Get Task",
  description: "Retrieves a single task within a project.",
  audience: "both",
  aiMetadata: { description: "Fetch a single task by its ID within a specific project to read its current details; requires both the project ID and the task ID. Use when you already have both IDs. Read-only and idempotent.", idempotent: !0 },
  props: {
    projectId: ninjapipeCommon.projectDropdownRequired,
    taskId: ninjapipeCommon.taskDropdown
  },
  async run(context) {
    let auth = getAuth(context), { projectId, taskId } = context.propsValue, response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common37.HttpMethod.GET,
      path: `/projects/${encodeURIComponent(String(projectId))}/tasks/${encodeURIComponent(String(taskId))}`
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/list-tasks.ts
var import_pieces_framework38 = require("@activepieces/pieces-framework"), import_pieces_common38 = require("@activepieces/pieces-common");
var listTasks = (0, import_pieces_framework38.createAction)({
  auth: ninjapipeAuth,
  name: "list_tasks",
  displayName: "List Tasks",
  description: "Retrieves the tasks of a project as a flat list. Use parent_id on each item to build a tree.",
  audience: "both",
  aiMetadata: { description: "List the tasks of a given project as a flat array, with optional status, priority, and parent-task filters plus sorting. Read-only; pass a parent task ID to fetch its subtasks, or an empty string to fetch only root tasks. Use to discover task IDs before updating a task.", idempotent: !0 },
  props: {
    projectId: ninjapipeCommon.projectDropdownRequired,
    statusFilter: import_pieces_framework38.Property.StaticDropdown({
      displayName: "Status Filter",
      required: !1,
      options: {
        options: [
          { label: "To Do", value: "To Do" },
          { label: "In Progress", value: "In Progress" },
          { label: "Done", value: "Done" },
          { label: "Cancelled", value: "Cancelled" }
        ]
      }
    }),
    priorityFilter: import_pieces_framework38.Property.StaticDropdown({
      displayName: "Priority Filter",
      required: !1,
      options: {
        options: [
          { label: "High", value: "High" },
          { label: "Medium", value: "Medium" },
          { label: "Low", value: "Low" }
        ]
      }
    }),
    parentId: import_pieces_framework38.Property.ShortText({
      displayName: "Parent Task Filter",
      description: "Pass a parent task ID to fetch its subtasks. Empty string to fetch only root tasks.",
      required: !1
    }),
    sortBy: import_pieces_framework38.Property.StaticDropdown({
      displayName: "Sort By",
      required: !1,
      defaultValue: "order_index",
      options: {
        options: [
          { label: "Order Index", value: "order_index" },
          { label: "Created At", value: "created_at" },
          { label: "Updated At", value: "updated_at" },
          { label: "Title", value: "title" },
          { label: "Due Date", value: "due_date" }
        ]
      }
    }),
    sortOrder: import_pieces_framework38.Property.StaticDropdown({
      displayName: "Sort Order",
      required: !1,
      defaultValue: "asc",
      options: {
        options: [
          { label: "Ascending", value: "asc" },
          { label: "Descending", value: "desc" }
        ]
      }
    })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, qs = {};
    p.statusFilter && (qs.status = p.statusFilter), p.priorityFilter && (qs.priority = p.priorityFilter), p.parentId !== void 0 && p.parentId !== null && (qs.parent_id = p.parentId), p.sortBy && (qs.sort_by = p.sortBy), p.sortOrder && (qs.sort_order = p.sortOrder);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common38.HttpMethod.GET,
      path: `/projects/${encodeURIComponent(String(p.projectId))}/tasks`,
      queryParams: qs
    }), items = extractItems(response.body);
    return flattenArray(items);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/delete-task.ts
var import_pieces_framework39 = require("@activepieces/pieces-framework"), import_pieces_common39 = require("@activepieces/pieces-common");
var deleteTask = (0, import_pieces_framework39.createAction)({
  auth: ninjapipeAuth,
  name: "delete_task",
  displayName: "Delete Task",
  description: "Deletes a task within a project. Subtasks may cascade.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a task by ID within a given project; any subtasks may be cascade-deleted along with it. Requires both the project ID and the task ID. Use only when the task should be removed outright; the operation is irreversible and safe to retry since re-deleting settles to the same removed state.", idempotent: !0 },
  props: {
    projectId: ninjapipeCommon.projectDropdownRequired,
    taskId: ninjapipeCommon.taskDropdown
  },
  async run(context) {
    let auth = getAuth(context), { projectId, taskId } = context.propsValue;
    return await ninjapipeApiCall({
      auth,
      method: import_pieces_common39.HttpMethod.DELETE,
      path: `/projects/${encodeURIComponent(String(projectId))}/tasks/${encodeURIComponent(String(taskId))}`
    }), { success: !0, deleted_id: taskId, project_id: projectId };
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/create-pipeline.ts
var import_pieces_framework40 = require("@activepieces/pieces-framework"), import_pieces_common40 = require("@activepieces/pieces-common");
var createPipeline = (0, import_pieces_framework40.createAction)({
  auth: ninjapipeAuth,
  name: "create_pipeline",
  displayName: "Create Pipeline",
  description: "Creates a new pipeline.",
  audience: "both",
  aiMetadata: { description: "Creates a new pipeline of a given type (contacts, deals, or general) with at least one stage, each stage needing an id, name, and hex color. Requires name, pipeline type, and a non-empty stages array. Not idempotent: each call creates another pipeline.", idempotent: !1 },
  props: {
    name: import_pieces_framework40.Property.ShortText({ displayName: "Name", required: !0 }),
    pipelineType: import_pieces_framework40.Property.StaticDropdown({
      displayName: "Pipeline Type",
      description: "Required by NinjaPipe \u2014 what this pipeline tracks.",
      required: !0,
      defaultValue: "general",
      options: {
        options: [
          { label: "Contacts", value: "contacts" },
          { label: "Deals", value: "deals" },
          { label: "General", value: "general" }
        ]
      }
    }),
    stages: import_pieces_framework40.Property.Array({
      displayName: "Stages",
      description: "Pipeline stages. Add at least one. Color must be a hex like #3b82f6.",
      required: !0,
      properties: {
        id: import_pieces_framework40.Property.ShortText({
          displayName: "ID",
          description: 'Stable slug, e.g. "new", "won".',
          required: !0
        }),
        name: import_pieces_framework40.Property.ShortText({
          displayName: "Name",
          description: "Display name shown on the board.",
          required: !0
        }),
        color: import_pieces_framework40.Property.ShortText({
          displayName: "Color",
          description: "Hex color, e.g. #3b82f6.",
          required: !0
        })
      }
    }),
    description: import_pieces_framework40.Property.LongText({ displayName: "Description", required: !1 }),
    status: import_pieces_framework40.Property.ShortText({ displayName: "Status", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue;
    if (!Array.isArray(p.stages) || p.stages.length === 0)
      throw new Error("Stages must contain at least one entry with id, name, and color.");
    let stages = p.stages.map((stage) => {
      if (!(stage != null && stage.id) || !stage.name || !stage.color)
        throw new Error("Each stage requires id, name, and color.");
      return { id: String(stage.id), name: String(stage.name), color: String(stage.color) };
    }), body = {
      name: p.name,
      pipeline_type: p.pipelineType,
      stages
    };
    p.description && (body.description = p.description), p.status && (body.status = p.status);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common40.HttpMethod.POST,
      path: "/pipelines",
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/update-pipeline.ts
var import_pieces_framework41 = require("@activepieces/pieces-framework"), import_pieces_common41 = require("@activepieces/pieces-common");
var updatePipeline = (0, import_pieces_framework41.createAction)({
  auth: ninjapipeAuth,
  name: "update_pipeline",
  displayName: "Update Pipeline",
  description: "Updates a pipeline by ID.",
  audience: "both",
  aiMetadata: { description: "Update an existing pipeline identified by ID, changing only the fields you supply (name, pipeline type, description, or status); omitted fields are left unchanged. Use to edit a known pipeline rather than create one. Idempotent \u2014 applying the same field values repeatedly yields the same result.", idempotent: !0 },
  props: {
    pipelineId: ninjapipeCommon.pipelineDropdownRequired,
    name: import_pieces_framework41.Property.ShortText({ displayName: "Name", required: !1 }),
    pipelineType: import_pieces_framework41.Property.StaticDropdown({
      displayName: "Pipeline Type",
      required: !1,
      options: {
        options: [
          { label: "Contacts", value: "contacts" },
          { label: "Deals", value: "deals" },
          { label: "General", value: "general" }
        ]
      }
    }),
    description: import_pieces_framework41.Property.LongText({ displayName: "Description", required: !1 }),
    status: import_pieces_framework41.Property.ShortText({ displayName: "Status", required: !1 })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.name && (body.name = p.name), p.pipelineType && (body.pipeline_type = p.pipelineType), p.description !== void 0 && (body.description = p.description), p.status && (body.status = p.status);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common41.HttpMethod.PUT,
      path: `/pipelines/${encodeURIComponent(String(p.pipelineId))}`,
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/get-pipeline.ts
var import_pieces_framework42 = require("@activepieces/pieces-framework"), import_pieces_common42 = require("@activepieces/pieces-common");
var getPipeline = (0, import_pieces_framework42.createAction)({
  auth: ninjapipeAuth,
  name: "get_pipeline",
  displayName: "Get Pipeline",
  description: "Retrieves a pipeline by ID.",
  audience: "both",
  aiMetadata: { description: "Fetch a single NinjaPipe sales pipeline by its ID. Read-only lookup for inspecting one pipeline; to enumerate available pipelines use a list action instead.", idempotent: !0 },
  props: {
    pipelineId: ninjapipeCommon.pipelineDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context), response = await ninjapipeApiCall({ auth, method: import_pieces_common42.HttpMethod.GET, path: `/pipelines/${encodeURIComponent(String(context.propsValue.pipelineId))}` });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/list-pipelines.ts
var import_pieces_framework43 = require("@activepieces/pieces-framework"), import_pieces_common43 = require("@activepieces/pieces-common");
var listPipelines = (0, import_pieces_framework43.createAction)({
  auth: ninjapipeAuth,
  name: "list_pipelines",
  displayName: "List Pipelines",
  description: "Retrieves a list of pipelines.",
  audience: "both",
  aiMetadata: { description: "List pipelines in the workspace, optionally filtered by a search term, with paging via limit and page. Set Return All to fetch every matching pipeline across pages; otherwise a single page is returned. Use to discover pipelines or resolve a pipeline ID by name. Read-only and idempotent.", idempotent: !0 },
  props: {
    limit: ninjapipeCommon.limitProperty,
    search: ninjapipeCommon.searchProperty,
    returnAll: import_pieces_framework43.Property.Checkbox({ displayName: "Return All", description: "Retrieve all pipelines using pagination.", required: !1, defaultValue: !1 }),
    page: import_pieces_framework43.Property.Number({ displayName: "Page", description: "Page number when not using Return All.", required: !1, defaultValue: 1 })
  },
  async run(context) {
    var _a, _b;
    let auth = getAuth(context), qs = { limit: String((_a = context.propsValue.limit) != null ? _a : 20), page: String((_b = context.propsValue.page) != null ? _b : 1) };
    if (context.propsValue.search && (qs.search = context.propsValue.search), context.propsValue.returnAll) {
      let results = [], page = 1, limit = 100, hasMore = !0;
      for (; hasMore; ) {
        let response2 = await ninjapipeApiCall({ auth, method: import_pieces_common43.HttpMethod.GET, path: "/pipelines", queryParams: __spreadProps(__spreadValues({}, qs), { limit: String(limit), page: String(page) }) }), items2 = extractItems(response2.body);
        results.push(...items2), hasMore = items2.length === limit && page < 1e3, page++;
      }
      return flattenArray(results);
    }
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common43.HttpMethod.GET, path: "/pipelines", queryParams: qs }), items = extractItems(response.body);
    return flattenArray(items);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/delete-pipeline.ts
var import_pieces_framework44 = require("@activepieces/pieces-framework"), import_pieces_common44 = require("@activepieces/pieces-common");
var deletePipeline = (0, import_pieces_framework44.createAction)({
  auth: ninjapipeAuth,
  name: "delete_pipeline",
  displayName: "Delete Pipeline",
  description: "Deletes a pipeline by ID.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a NinjaPipe pipeline by its ID. Pick this only when the pipeline should be removed entirely; deletion is irreversible and cascades to the pipeline's stages. Safe to retry \u2014 deleting an already-deleted pipeline settles to the same removed state.", idempotent: !0 },
  props: {
    pipelineId: ninjapipeCommon.pipelineDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context);
    return await ninjapipeApiCall({ auth, method: import_pieces_common44.HttpMethod.DELETE, path: `/pipelines/${encodeURIComponent(String(context.propsValue.pipelineId))}` }), { success: !0, deleted_id: context.propsValue.pipelineId };
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/create-order.ts
var import_pieces_framework45 = require("@activepieces/pieces-framework"), import_pieces_common45 = require("@activepieces/pieces-common");
var createOrder = (0, import_pieces_framework45.createAction)({
  auth: ninjapipeAuth,
  name: "create_order",
  displayName: "Create Order",
  description: "Creates a new order. Line items, currency, and custom fields go in Settings JSON.",
  audience: "both",
  aiMetadata: { description: "Creates a new order with customer details, amounts, status, payment status, dates, priority, and tracking; line items, currency, and custom fields go in Settings JSON. Only Customer Name is required, and Order Number defaults to ORD-<timestamp> if omitted. Not idempotent: each call creates another order.", idempotent: !1 },
  props: {
    customerName: import_pieces_framework45.Property.ShortText({
      displayName: "Customer Name",
      description: "Customer display name.",
      required: !0
    }),
    customerEmail: import_pieces_framework45.Property.ShortText({ displayName: "Customer Email", required: !1 }),
    customerCompany: import_pieces_framework45.Property.ShortText({ displayName: "Customer Company", required: !1 }),
    contactId: ninjapipeCommon.contactDropdown,
    orderNumber: import_pieces_framework45.Property.ShortText({
      displayName: "Order Number",
      description: "Defaults to ORD-<timestamp> if omitted.",
      required: !1
    }),
    totalAmount: import_pieces_framework45.Property.Number({ displayName: "Total Amount", required: !1 }),
    status: ninjapipeCommon.orderStatusDropdown,
    paymentStatus: ninjapipeCommon.paymentStatusDropdown,
    orderDate: import_pieces_framework45.Property.DateTime({
      displayName: "Order Date",
      required: !1
    }),
    deliveryDate: import_pieces_framework45.Property.DateTime({
      displayName: "Delivery Date",
      required: !1
    }),
    priority: ninjapipeCommon.priorityDropdown,
    trackingNumber: import_pieces_framework45.Property.ShortText({ displayName: "Tracking Number", required: !1 }),
    notes: import_pieces_framework45.Property.LongText({ displayName: "Notes", required: !1 }),
    settingsJson: import_pieces_framework45.Property.Object({
      displayName: "Settings JSON",
      description: 'Line items, currency, and custom fields. Example: {"currency":"USD","items":[{"name":"\u2026","price":10}],"custom_fields":[]}.',
      required: !1
    })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = { customer_name: p.customerName };
    p.customerEmail && (body.customer_email = p.customerEmail), p.customerCompany && (body.customer_company = p.customerCompany), p.contactId && (body.contact_id = p.contactId), p.orderNumber && (body.order_number = p.orderNumber), p.totalAmount !== void 0 && p.totalAmount !== null && (body.total_amount = p.totalAmount), p.status && (body.status = p.status), p.paymentStatus && (body.payment_status = p.paymentStatus);
    {
      let v = toDateOnly(p.orderDate);
      v && (body.order_date = v);
    }
    {
      let v = toDateOnly(p.deliveryDate);
      v && (body.delivery_date = v);
    }
    p.priority && (body.priority = p.priority), p.trackingNumber && (body.tracking_number = p.trackingNumber), p.notes && (body.notes = p.notes), p.settingsJson && typeof p.settingsJson == "object" && Object.keys(p.settingsJson).length > 0 && (body.settings_json = p.settingsJson);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common45.HttpMethod.POST,
      path: "/orders",
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/update-order.ts
var import_pieces_framework46 = require("@activepieces/pieces-framework"), import_pieces_common46 = require("@activepieces/pieces-common");
var updateOrder = (0, import_pieces_framework46.createAction)({
  auth: ninjapipeAuth,
  name: "update_order",
  displayName: "Update Order",
  description: "Updates an order by ID.",
  audience: "both",
  aiMetadata: { description: "Updates an existing order identified by Order ID, applying only the fields you supply (customer details, amounts, status, payment status, dates, priority, tracking, notes); line items, currency, and custom fields go in Settings JSON. Requires a valid order ID; repeating with the same values is safe.", idempotent: !0 },
  props: {
    orderId: ninjapipeCommon.orderDropdownRequired,
    customerName: import_pieces_framework46.Property.ShortText({ displayName: "Customer Name", required: !1 }),
    customerEmail: import_pieces_framework46.Property.ShortText({ displayName: "Customer Email", required: !1 }),
    customerCompany: import_pieces_framework46.Property.ShortText({ displayName: "Customer Company", required: !1 }),
    contactId: ninjapipeCommon.contactDropdown,
    orderNumber: import_pieces_framework46.Property.ShortText({ displayName: "Order Number", required: !1 }),
    totalAmount: import_pieces_framework46.Property.Number({ displayName: "Total Amount", required: !1 }),
    status: ninjapipeCommon.orderStatusDropdown,
    paymentStatus: ninjapipeCommon.paymentStatusDropdown,
    orderDate: import_pieces_framework46.Property.DateTime({ displayName: "Order Date", required: !1 }),
    deliveryDate: import_pieces_framework46.Property.DateTime({ displayName: "Delivery Date", required: !1 }),
    priority: ninjapipeCommon.priorityDropdown,
    trackingNumber: import_pieces_framework46.Property.ShortText({ displayName: "Tracking Number", required: !1 }),
    notes: import_pieces_framework46.Property.LongText({ displayName: "Notes", required: !1 }),
    settingsJson: import_pieces_framework46.Property.Object({
      displayName: "Settings JSON",
      description: "Line items, currency, and custom fields.",
      required: !1
    })
  },
  async run(context) {
    let auth = getAuth(context), p = context.propsValue, body = {};
    p.customerName && (body.customer_name = p.customerName), p.customerEmail && (body.customer_email = p.customerEmail), p.customerCompany && (body.customer_company = p.customerCompany), p.contactId && (body.contact_id = p.contactId), p.orderNumber && (body.order_number = p.orderNumber), p.totalAmount !== void 0 && p.totalAmount !== null && (body.total_amount = p.totalAmount), p.status && (body.status = p.status), p.paymentStatus && (body.payment_status = p.paymentStatus);
    {
      let v = toDateOnly(p.orderDate);
      v && (body.order_date = v);
    }
    {
      let v = toDateOnly(p.deliveryDate);
      v && (body.delivery_date = v);
    }
    p.priority && (body.priority = p.priority), p.trackingNumber && (body.tracking_number = p.trackingNumber), p.notes !== void 0 && (body.notes = p.notes), p.settingsJson && typeof p.settingsJson == "object" && Object.keys(p.settingsJson).length > 0 && (body.settings_json = p.settingsJson);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common46.HttpMethod.PUT,
      path: `/orders/${encodeURIComponent(String(p.orderId))}`,
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/get-order.ts
var import_pieces_framework47 = require("@activepieces/pieces-framework"), import_pieces_common47 = require("@activepieces/pieces-common");
var getOrder = (0, import_pieces_framework47.createAction)({
  auth: ninjapipeAuth,
  name: "get_order",
  displayName: "Get Order",
  description: "Retrieves an order by ID.",
  audience: "both",
  aiMetadata: { description: "Fetches a single order by its ID, returning its full record. Pick this when you already have an order ID and need its details rather than searching a list. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    orderId: ninjapipeCommon.orderDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context), response = await ninjapipeApiCall({ auth, method: import_pieces_common47.HttpMethod.GET, path: `/orders/${encodeURIComponent(String(context.propsValue.orderId))}` });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/list-orders.ts
var import_pieces_framework48 = require("@activepieces/pieces-framework"), import_pieces_common48 = require("@activepieces/pieces-common");
var listOrders = (0, import_pieces_framework48.createAction)({
  auth: ninjapipeAuth,
  name: "list_orders",
  displayName: "List Orders",
  description: "Retrieves a list of orders.",
  audience: "both",
  aiMetadata: { description: 'List orders with optional search text and status filter. Read-only; supports single-page paging or "Return All" to follow pagination across every page. Use to discover order IDs before fetching or deleting an order.', idempotent: !0 },
  props: {
    limit: ninjapipeCommon.limitProperty,
    search: ninjapipeCommon.searchProperty,
    statusFilter: ninjapipeCommon.statusFilterProperty,
    returnAll: import_pieces_framework48.Property.Checkbox({ displayName: "Return All", description: "Retrieve all orders using pagination.", required: !1, defaultValue: !1 }),
    page: import_pieces_framework48.Property.Number({ displayName: "Page", description: "Page number when not using Return All.", required: !1, defaultValue: 1 })
  },
  async run(context) {
    var _a, _b;
    let auth = getAuth(context), qs = { limit: String((_a = context.propsValue.limit) != null ? _a : 20), page: String((_b = context.propsValue.page) != null ? _b : 1) };
    if (context.propsValue.search && (qs.search = context.propsValue.search), context.propsValue.statusFilter && (qs.status = context.propsValue.statusFilter), context.propsValue.returnAll) {
      let results = [], page = 1, limit = 100, hasMore = !0;
      for (; hasMore; ) {
        let response2 = await ninjapipeApiCall({ auth, method: import_pieces_common48.HttpMethod.GET, path: "/orders", queryParams: __spreadProps(__spreadValues({}, qs), { limit: String(limit), page: String(page) }) }), items2 = extractItems(response2.body);
        results.push(...items2), hasMore = items2.length === limit && page < 1e3, page++;
      }
      return flattenArray(results);
    }
    let response = await ninjapipeApiCall({ auth, method: import_pieces_common48.HttpMethod.GET, path: "/orders", queryParams: qs }), items = extractItems(response.body);
    return flattenArray(items);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/delete-order.ts
var import_pieces_framework49 = require("@activepieces/pieces-framework"), import_pieces_common49 = require("@activepieces/pieces-common");
var deleteOrder = (0, import_pieces_framework49.createAction)({
  auth: ninjapipeAuth,
  name: "delete_order",
  displayName: "Delete Order",
  description: "Deletes an order by ID.",
  audience: "both",
  aiMetadata: { description: "Permanently delete an order identified by its ID. Destructive and not reversible; re-running after the order is gone will fail since the ID no longer exists.", idempotent: !1 },
  props: {
    orderId: ninjapipeCommon.orderDropdownRequired
  },
  async run(context) {
    let auth = getAuth(context);
    return await ninjapipeApiCall({ auth, method: import_pieces_common49.HttpMethod.DELETE, path: `/orders/${encodeURIComponent(String(context.propsValue.orderId))}` }), { success: !0, deleted_id: context.propsValue.orderId };
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/send-to-databin.ts
var import_pieces_framework50 = require("@activepieces/pieces-framework"), import_pieces_common50 = require("@activepieces/pieces-common");
var sendToDatabin = (0, import_pieces_framework50.createAction)({
  auth: ninjapipeAuth,
  name: "send_to_databin",
  displayName: "Send to Databin",
  description: "Sends a JSON payload to a Databin webhook URL on your connected NinjaPipe workspace.",
  audience: "both",
  aiMetadata: { description: "Post an arbitrary JSON payload to a NinjaPipe Databin webhook. The webhook URL must be HTTPS, contain /api/webhooks/, and resolve to the same host as the connected workspace, or the call is rejected. Each send is a fresh delivery, so repeated calls are not idempotent.", idempotent: !1 },
  props: {
    webhookUrl: import_pieces_framework50.Property.ShortText({
      displayName: "Databin Webhook URL",
      description: "The full HTTPS webhook URL from your Databin. Must point to your connected NinjaPipe host and contain /api/webhooks/.",
      required: !0
    }),
    payload: import_pieces_framework50.Property.Json({
      displayName: "Payload",
      description: "JSON object to send.",
      required: !0
    })
  },
  async run(context) {
    var _a;
    let auth = getAuth(context), rawUrl = String((_a = context.propsValue.webhookUrl) != null ? _a : "").trim(), parsed;
    try {
      parsed = new URL(rawUrl);
    } catch (e) {
      throw new Error("Invalid webhook URL: not a valid URL.");
    }
    if (parsed.protocol !== "https:")
      throw new Error("Invalid webhook URL: must use https://.");
    if (!parsed.pathname.includes("/api/webhooks/"))
      throw new Error("Invalid webhook URL: path must contain /api/webhooks/.");
    let expectedHost = (() => {
      try {
        return new URL(auth.base_url).host.toLowerCase();
      } catch (e) {
        return "";
      }
    })(), actualHost = parsed.host.toLowerCase();
    if (!(expectedHost === actualHost || expectedHost && actualHost.endsWith("." + expectedHost.replace(/^www\./, "")) || expectedHost && expectedHost.endsWith("." + actualHost.replace(/^www\./, "")) || actualHost === expectedHost.replace(/^www\./, "") || actualHost.replace(/^www\./, "") === expectedHost.replace(/^www\./, "")))
      throw new Error(
        `Webhook host "${actualHost}" does not match your connected NinjaPipe host "${expectedHost}". Update the connection or use a Databin URL from your workspace.`
      );
    let body = typeof context.propsValue.payload == "string" ? JSON.parse(context.propsValue.payload) : context.propsValue.payload;
    return (await import_pieces_common50.httpClient.sendRequest({
      method: import_pieces_common50.HttpMethod.POST,
      url: rawUrl,
      headers: {
        "Content-Type": "application/json",
        Accept: "application/json"
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/actions/add-to-list.ts
var import_pieces_framework51 = require("@activepieces/pieces-framework"), import_pieces_common51 = require("@activepieces/pieces-common");
var addToList = (0, import_pieces_framework51.createAction)({
  auth: ninjapipeAuth,
  name: "add_to_list",
  displayName: "Add to List",
  description: "Adds a contact OR a company to a CRM list. The list type must match (Contact lists accept contacts, Company lists accept companies). Idempotent \u2014 returns already_in_list:true if already present.",
  audience: "both",
  aiMetadata: { description: "Add a single member to a CRM list by list ID \u2014 provide exactly one of a contact ID or a company ID (not both, not neither), and the member type must match the list type. Idempotent \u2014 adding a member already on the list is a no-op rather than an error.", idempotent: !0 },
  props: {
    listId: import_pieces_framework51.Property.ShortText({
      displayName: "List ID",
      description: "The list UUID (must belong to your workspace).",
      required: !0
    }),
    contactId: ninjapipeCommon.contactDropdown,
    companyId: ninjapipeCommon.companyDropdown
  },
  async run(context) {
    let auth = getAuth(context), { listId, contactId, companyId } = context.propsValue;
    if (!contactId && !companyId)
      throw new Error("Provide either Contact or Company.");
    if (contactId && companyId)
      throw new Error("Provide only one: Contact or Company. Lists are typed.");
    let body = {};
    contactId && (body.contact_id = contactId), companyId && (body.company_id = companyId);
    let response = await ninjapipeApiCall({
      auth,
      method: import_pieces_common51.HttpMethod.POST,
      path: `/lists/${encodeURIComponent(String(listId))}/members`,
      body
    });
    return flattenCustomFields(response.body);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/triggers/new-contact.ts
var import_pieces_framework52 = require("@activepieces/pieces-framework"), import_pieces_common53 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/ninjapipe/src/lib/triggers/common.ts
var import_pieces_common52 = require("@activepieces/pieces-common");
function buildPolling(path, timestampField) {
  return {
    strategy: import_pieces_common52.DedupeStrategy.TIMEBASED,
    items: async ({ auth }) => {
      let a = getAuth({ auth }), response = await ninjapipeApiCall({
        auth: a,
        method: import_pieces_common52.HttpMethod.GET,
        path,
        queryParams: { limit: "100", sort: timestampField, order: "desc" }
      });
      return extractItems(response.body).map((item) => {
        var _a, _b;
        let record = item, ts = (_b = (_a = record[timestampField]) != null ? _a : record.created_at) != null ? _b : record.updated_at;
        return {
          epochMilliSeconds: ts ? new Date(ts).getTime() : Date.now(),
          data: flattenCustomFields(record)
        };
      });
    }
  };
}
function buildProjectTasksPolling(timestampField) {
  return {
    strategy: import_pieces_common52.DedupeStrategy.TIMEBASED,
    items: async ({ auth, propsValue }) => {
      let projectId = propsValue.projectId;
      if (!projectId) return [];
      let a = getAuth({ auth }), response = await ninjapipeApiCall({
        auth: a,
        method: import_pieces_common52.HttpMethod.GET,
        path: `/projects/${projectId}/tasks`,
        queryParams: { sort_by: timestampField, sort_order: "desc" }
      });
      return extractItems(response.body).map((item) => {
        var _a, _b;
        let record = item, ts = (_b = (_a = record[timestampField]) != null ? _a : record.created_at) != null ? _b : record.updated_at;
        return {
          epochMilliSeconds: ts ? new Date(ts).getTime() : Date.now(),
          data: flattenCustomFields(record)
        };
      });
    }
  };
}

// ../ap/packages/pieces/community/ninjapipe/src/lib/triggers/new-contact.ts
var newContact = (0, import_pieces_framework52.createTrigger)({
  auth: ninjapipeAuth,
  name: "new_contact",
  displayName: "New Contact",
  description: "Triggers when a new contact is created.",
  aiMetadata: {
    description: "Fires once for each contact newly created in NinjaPipe, detected by polling on the contact creation timestamp. The event payload is the created contact, including its id, email, first_name, last_name, and created_at. Use it to react to brand-new contacts; it does not fire when an existing contact is later updated."
  },
  type: import_pieces_framework52.TriggerStrategy.POLLING,
  sampleData: { id: "1", email: "example@ninjapipe.app", first_name: "Jane", last_name: "Doe", created_at: "2024-01-01T00:00:00Z" },
  props: {},
  async test(context) {
    return await import_pieces_common53.pollingHelper.test(buildPolling("/contacts", "created_at"), context);
  },
  async onEnable(context) {
    await import_pieces_common53.pollingHelper.onEnable(buildPolling("/contacts", "created_at"), context);
  },
  async onDisable(context) {
    await import_pieces_common53.pollingHelper.onDisable(buildPolling("/contacts", "created_at"), context);
  },
  async run(context) {
    return await import_pieces_common53.pollingHelper.poll(buildPolling("/contacts", "created_at"), context);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/triggers/new-company.ts
var import_pieces_framework53 = require("@activepieces/pieces-framework"), import_pieces_common54 = require("@activepieces/pieces-common");
var newCompany = (0, import_pieces_framework53.createTrigger)({
  auth: ninjapipeAuth,
  name: "new_company",
  displayName: "New Company",
  description: "Triggers when a new company is created.",
  aiMetadata: {
    description: "Fires once for each company newly created in NinjaPipe, detected by polling on the company creation timestamp. The event payload is the created company, including its id, name, and created_at. Use it to react to brand-new companies; it does not fire when an existing company is later updated."
  },
  type: import_pieces_framework53.TriggerStrategy.POLLING,
  sampleData: { id: "1", name: "Acme Inc", created_at: "2024-01-01T00:00:00Z" },
  props: {},
  async test(context) {
    return await import_pieces_common54.pollingHelper.test(buildPolling("/companies", "created_at"), context);
  },
  async onEnable(context) {
    await import_pieces_common54.pollingHelper.onEnable(buildPolling("/companies", "created_at"), context);
  },
  async onDisable(context) {
    await import_pieces_common54.pollingHelper.onDisable(buildPolling("/companies", "created_at"), context);
  },
  async run(context) {
    return await import_pieces_common54.pollingHelper.poll(buildPolling("/companies", "created_at"), context);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/triggers/new-deal.ts
var import_pieces_framework54 = require("@activepieces/pieces-framework"), import_pieces_common55 = require("@activepieces/pieces-common");
var newDeal = (0, import_pieces_framework54.createTrigger)({
  auth: ninjapipeAuth,
  name: "new_deal",
  displayName: "New Deal",
  description: "Triggers when a new deal is created.",
  aiMetadata: {
    description: "Fires once for each deal newly created in NinjaPipe, detected by polling on the deal creation timestamp. The event payload is the created deal, including its id, name, value, and created_at. Use it to react to brand-new deals entering the pipeline; it does not fire when an existing deal is later updated."
  },
  type: import_pieces_framework54.TriggerStrategy.POLLING,
  sampleData: { id: "1", name: "Enterprise Plan", value: 5e3, created_at: "2024-01-01T00:00:00Z" },
  props: {},
  async test(context) {
    return await import_pieces_common55.pollingHelper.test(buildPolling("/deals", "created_at"), context);
  },
  async onEnable(context) {
    await import_pieces_common55.pollingHelper.onEnable(buildPolling("/deals", "created_at"), context);
  },
  async onDisable(context) {
    await import_pieces_common55.pollingHelper.onDisable(buildPolling("/deals", "created_at"), context);
  },
  async run(context) {
    return await import_pieces_common55.pollingHelper.poll(buildPolling("/deals", "created_at"), context);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/triggers/new-task.ts
var import_pieces_framework55 = require("@activepieces/pieces-framework"), import_pieces_common56 = require("@activepieces/pieces-common");
var polling = buildProjectTasksPolling("created_at"), newTask = (0, import_pieces_framework55.createTrigger)({
  auth: ninjapipeAuth,
  name: "new_task",
  displayName: "New Task",
  description: "Triggers when a new task is created in the selected project.",
  aiMetadata: {
    description: "Fires once for each task newly created in the selected NinjaPipe project, detected by polling on the task creation timestamp. The event payload is the created task, including its id, project_id, title, description, status, priority, assignee_id, due_date, parent_id, tags, and timestamps. Use it to react to brand-new work items; it does not fire for subsequent edits to an existing task."
  },
  type: import_pieces_framework55.TriggerStrategy.POLLING,
  sampleData: {
    id: "a97381c8-75d3-4773-92d7-80ad97cf06f9",
    project_id: "dbe92bb3-3239-4710-8216-dccc9ffceaa5",
    title: "Ship release notes",
    description: null,
    status: "To Do",
    priority: "Medium",
    assignee_id: null,
    due_date: null,
    parent_id: null,
    order_index: 0,
    tags: [],
    created_at: "2026-04-28T13:00:00Z",
    updated_at: "2026-04-28T13:00:00Z"
  },
  props: {
    projectId: ninjapipeCommon.projectDropdownRequired
  },
  async test(context) {
    return import_pieces_common56.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common56.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common56.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return import_pieces_common56.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/triggers/new-or-updated-contact.ts
var import_pieces_framework56 = require("@activepieces/pieces-framework"), import_pieces_common57 = require("@activepieces/pieces-common");
var newOrUpdatedContact = (0, import_pieces_framework56.createTrigger)({
  auth: ninjapipeAuth,
  name: "new_or_updated_contact",
  displayName: "New or Updated Contact",
  description: "Triggers when a contact is created or updated.",
  aiMetadata: {
    description: "Fires whenever a contact in NinjaPipe is created or has any field changed, detected by polling on the contact last-updated timestamp. The event payload is the current state of the affected contact, including its id, email, first_name, last_name, and updated_at. A given contact can fire this multiple times as it is edited; the payload does not distinguish a first creation from a later update."
  },
  type: import_pieces_framework56.TriggerStrategy.POLLING,
  sampleData: { id: "1", email: "example@ninjapipe.app", first_name: "Jane", last_name: "Doe", updated_at: "2024-01-01T00:00:00Z" },
  props: {},
  async test(context) {
    return await import_pieces_common57.pollingHelper.test(buildPolling("/contacts", "updated_at"), context);
  },
  async onEnable(context) {
    await import_pieces_common57.pollingHelper.onEnable(buildPolling("/contacts", "updated_at"), context);
  },
  async onDisable(context) {
    await import_pieces_common57.pollingHelper.onDisable(buildPolling("/contacts", "updated_at"), context);
  },
  async run(context) {
    return await import_pieces_common57.pollingHelper.poll(buildPolling("/contacts", "updated_at"), context);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/triggers/new-or-updated-company.ts
var import_pieces_framework57 = require("@activepieces/pieces-framework"), import_pieces_common58 = require("@activepieces/pieces-common");
var newOrUpdatedCompany = (0, import_pieces_framework57.createTrigger)({
  auth: ninjapipeAuth,
  name: "new_or_updated_company",
  displayName: "New or Updated Company",
  description: "Triggers when a company is created or updated.",
  aiMetadata: {
    description: "Fires whenever a company in NinjaPipe is created or has any field changed, detected by polling on the company last-updated timestamp. The event payload is the current state of the affected company, including its id, name, and updated_at. A given company can fire this multiple times as it is edited; the payload does not distinguish a first creation from a later update."
  },
  type: import_pieces_framework57.TriggerStrategy.POLLING,
  sampleData: { id: "1", name: "Acme Inc", updated_at: "2024-01-01T00:00:00Z" },
  props: {},
  async test(context) {
    return await import_pieces_common58.pollingHelper.test(buildPolling("/companies", "updated_at"), context);
  },
  async onEnable(context) {
    await import_pieces_common58.pollingHelper.onEnable(buildPolling("/companies", "updated_at"), context);
  },
  async onDisable(context) {
    await import_pieces_common58.pollingHelper.onDisable(buildPolling("/companies", "updated_at"), context);
  },
  async run(context) {
    return await import_pieces_common58.pollingHelper.poll(buildPolling("/companies", "updated_at"), context);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/triggers/new-or-updated-deal.ts
var import_pieces_framework58 = require("@activepieces/pieces-framework"), import_pieces_common59 = require("@activepieces/pieces-common");
var newOrUpdatedDeal = (0, import_pieces_framework58.createTrigger)({
  auth: ninjapipeAuth,
  name: "new_or_updated_deal",
  displayName: "New or Updated Deal",
  description: "Triggers when a deal is created or updated.",
  aiMetadata: {
    description: "Fires whenever a deal in NinjaPipe is created or has any field changed, detected by polling on the deal last-updated timestamp. The event payload is the current state of the affected deal, including its id, name, value, and updated_at. A given deal can fire this multiple times as it moves through the pipeline; the payload does not distinguish a first creation from a later update."
  },
  type: import_pieces_framework58.TriggerStrategy.POLLING,
  sampleData: { id: "1", name: "Enterprise Plan", value: 5e3, updated_at: "2024-01-01T00:00:00Z" },
  props: {},
  async test(context) {
    return await import_pieces_common59.pollingHelper.test(buildPolling("/deals", "updated_at"), context);
  },
  async onEnable(context) {
    await import_pieces_common59.pollingHelper.onEnable(buildPolling("/deals", "updated_at"), context);
  },
  async onDisable(context) {
    await import_pieces_common59.pollingHelper.onDisable(buildPolling("/deals", "updated_at"), context);
  },
  async run(context) {
    return await import_pieces_common59.pollingHelper.poll(buildPolling("/deals", "updated_at"), context);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/lib/triggers/new-or-updated-task.ts
var import_pieces_framework59 = require("@activepieces/pieces-framework"), import_pieces_common60 = require("@activepieces/pieces-common");
var polling2 = buildProjectTasksPolling("updated_at"), newOrUpdatedTask = (0, import_pieces_framework59.createTrigger)({
  auth: ninjapipeAuth,
  name: "new_or_updated_task",
  displayName: "New or Updated Task",
  description: "Triggers when a task in the selected project is created or updated.",
  aiMetadata: {
    description: "Fires whenever a task in the selected NinjaPipe project is created or has any field changed, detected by polling on the task last-updated timestamp. The event payload is the current state of the affected task, including its id, project_id, title, description, status, priority, assignee_id, due_date, parent_id, tags, and timestamps. A given task can fire this multiple times as it progresses (e.g. status moving to In Progress); the payload does not distinguish a first creation from a later update."
  },
  type: import_pieces_framework59.TriggerStrategy.POLLING,
  sampleData: {
    id: "a97381c8-75d3-4773-92d7-80ad97cf06f9",
    project_id: "dbe92bb3-3239-4710-8216-dccc9ffceaa5",
    title: "Ship release notes",
    description: null,
    status: "In Progress",
    priority: "Medium",
    assignee_id: null,
    due_date: null,
    parent_id: null,
    order_index: 0,
    tags: [],
    created_at: "2026-04-28T13:00:00Z",
    updated_at: "2026-04-28T13:30:00Z"
  },
  props: {
    projectId: ninjapipeCommon.projectDropdownRequired
  },
  async test(context) {
    return import_pieces_common60.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common60.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common60.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return import_pieces_common60.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/ninjapipe/src/index.ts
var ninjapipeAuth = import_pieces_framework60.PieceAuth.CustomAuth({
  displayName: "NinjaPipe API Connection",
  required: !0,
  description: "Connect to your NinjaPipe workspace.\n\n1. Log in to NinjaPipe and open **Workspace Settings > NinjaPipe API**.\n2. Copy your **API key** (starts with `np_`).\n3. Enter your workspace **API Base URL**. The default is `https://www.ninjapipe.app/api`.",
  props: {
    base_url: import_pieces_framework60.Property.ShortText({
      displayName: "API Base URL",
      description: "Your workspace API base URL (e.g. https://www.ninjapipe.app/api).",
      required: !0,
      defaultValue: "https://www.ninjapipe.app/api"
    }),
    api_key: import_pieces_framework60.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your workspace API key from NinjaPipe Settings > API.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    var _a, _b, _c;
    let parsed;
    try {
      parsed = new URL(auth.base_url);
    } catch (e) {
      return { valid: !1, error: "Base URL is not a valid URL." };
    }
    if (parsed.protocol !== "https:")
      return { valid: !1, error: "Base URL must use https://." };
    let host = parsed.hostname.toLowerCase();
    if (host === "localhost" || host === "0.0.0.0" || host.endsWith(".localhost") || /^127\./.test(host) || /^10\./.test(host) || /^192\.168\./.test(host) || /^172\.(1[6-9]|2\d|3[01])\./.test(host) || host === "169.254.169.254" || /^169\.254\./.test(host) || /^fc00:/i.test(host) || /^fe80:/i.test(host) || host === "::1")
      return { valid: !1, error: `Base URL host "${host}" is not allowed (loopback, private, or metadata IP).` };
    try {
      return await import_pieces_common62.httpClient.sendRequest({
        method: import_pieces_common62.HttpMethod.GET,
        url: `${auth.base_url.replace(/\/+$/, "")}/contacts`,
        authentication: {
          type: import_pieces_common62.AuthenticationType.BEARER_TOKEN,
          token: auth.api_key
        },
        queryParams: { limit: "1" }
      }), { valid: !0 };
    } catch (e) {
      let err = e, status = (_b = (_a = err.response) == null ? void 0 : _a.status) != null ? _b : err.status;
      return status === 401 || status === 403 ? { valid: !1, error: "API key was rejected. Check the key in NinjaPipe Settings > API." } : status === 404 ? { valid: !1, error: "Base URL is reachable but /contacts returned 404. Check the Base URL path." } : { valid: !1, error: `Could not connect to NinjaPipe (${(_c = status != null ? status : err.message) != null ? _c : "unknown error"}). Check the Base URL and API Key.` };
    }
  }
}), ninjapipe = (0, import_pieces_framework60.createPiece)({
  displayName: "NinjaPipe",
  description: "NinjaPipe CRM integration with contacts, companies, deals, tasks (under projects), pipelines, products, budgets, projects, orders, lists, and Databin webhook delivery.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/ninjapipe.png",
  categories: [import_pieces_framework61.PieceCategory.SALES_AND_CRM],
  auth: ninjapipeAuth,
  authors: ["hemmilicious"],
  actions: [
    createContact,
    updateContact,
    getContact,
    listContacts,
    deleteContact,
    upsertContact,
    toggleClientPortal,
    createCompany,
    updateCompany,
    getCompany,
    listCompanies,
    deleteCompany,
    createDeal,
    updateDeal,
    getDeal,
    listDeals,
    deleteDeal,
    createProduct,
    updateProduct,
    getProduct,
    listProducts,
    deleteProduct,
    createBudget,
    updateBudget,
    getBudget,
    listBudgets,
    deleteBudget,
    createBudgetExpense,
    createProject,
    updateProject,
    getProject,
    listProjects,
    deleteProject,
    createTask,
    updateTask,
    getTask,
    listTasks,
    deleteTask,
    createPipeline,
    updatePipeline,
    getPipeline,
    listPipelines,
    deletePipeline,
    createOrder,
    updateOrder,
    getOrder,
    listOrders,
    deleteOrder,
    sendToDatabin,
    addToList,
    (0, import_pieces_common61.createCustomApiCallAction)({
      baseUrl: (auth) => {
        var _a, _b;
        return (_b = (_a = auth == null ? void 0 : auth.props) == null ? void 0 : _a.base_url) != null ? _b : "";
      },
      auth: ninjapipeAuth,
      authMapping: async (auth) => {
        var _a;
        return {
          Authorization: `Bearer ${(_a = auth == null ? void 0 : auth.props) == null ? void 0 : _a.api_key}`
        };
      }
    })
  ],
  triggers: [
    newContact,
    newCompany,
    newDeal,
    newTask,
    newOrUpdatedContact,
    newOrUpdatedCompany,
    newOrUpdatedDeal,
    newOrUpdatedTask
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  ninjapipe,
  ninjapipeAuth
});
