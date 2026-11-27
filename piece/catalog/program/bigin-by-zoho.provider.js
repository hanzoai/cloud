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

// ../ap/packages/pieces/community/bigin-by-zoho/src/index.ts
var index_exports = {};
__export(index_exports, {
  biginByZoho: () => biginByZoho
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework28 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/triggers/new-contact-created.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/common/constants.ts
var DATA_CENTER_REGIONS = [
  {
    REGION: "US",
    ACCOUNTS_DOMAIN: "https://accounts.zoho.com",
    API_DOMAIN: "https://www.zohoapis.com",
    LABEL: "United States (.com)"
  },
  {
    REGION: "AU",
    ACCOUNTS_DOMAIN: "https://accounts.zoho.com.au",
    API_DOMAIN: "https://www.zohoapis.com.au",
    LABEL: "Australia (.com.au)"
  },
  {
    REGION: "EU",
    ACCOUNTS_DOMAIN: "https://accounts.zoho.eu",
    API_DOMAIN: "https://www.zohoapis.eu",
    LABEL: "Europe (.eu)"
  },
  {
    REGION: "IN",
    ACCOUNTS_DOMAIN: "https://accounts.zoho.in",
    API_DOMAIN: "https://www.zohoapis.in",
    LABEL: "India (.in)"
  },
  {
    REGION: "CN",
    ACCOUNTS_DOMAIN: "https://accounts.zoho.com.cn",
    API_DOMAIN: "https://www.zohoapis.com.cn",
    LABEL: "China (.com.cn)"
  },
  {
    REGION: "JP",
    ACCOUNTS_DOMAIN: "https://accounts.zoho.jp",
    API_DOMAIN: "https://www.zohoapis.jp",
    LABEL: "Japan (.jp)"
  },
  {
    REGION: "SA",
    ACCOUNTS_DOMAIN: "https://accounts.zoho.sa",
    API_DOMAIN: "https://www.zohoapis.sa",
    LABEL: "Saudi Arabia (.sa)"
  },
  {
    REGION: "CA",
    ACCOUNTS_DOMAIN: "https://accounts.zohocloud.ca",
    API_DOMAIN: "https://www.zohoapis.ca",
    LABEL: "Canada (.ca)"
  }
], ZOHO_WEBHOOK_TOKEN = "bigin_webhook_token", API_ENDPOINTS = {
  WEBHOOKS: "/actions/watch",
  USERS: "/users",
  COMPANIES: "/Accounts",
  PIPELINES: "/Pipelines",
  CONTACTS: "/Contacts",
  TASKS: "/Tasks",
  CALLS: "/Calls",
  EVENTS: "/Events",
  MODULES: "/settings/modules",
  LAYOUTS: "/settings/layouts",
  TAGS: "/settings/tags",
  PRODUCTS: "/Products",
  FIELDS: "/settings/fields"
};

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/common/request.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function fireHttpRequest({
  method,
  path,
  body,
  access_token
}) {
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: path,
    headers: {
      Accept: "application/json",
      Authorization: `Zoho-oauthtoken ${access_token}`,
      "Content-Type": "application/json"
    },
    body
  }).then((res) => res.body).catch((err) => {
    throw new Error(
      `Error in request to ${path}: ${err.message || JSON.stringify(err)}`
    );
  });
}
var biginApiService = {
  createWebhook: async (access_token, payload, api_domain) => await fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.WEBHOOKS}`,
    body: payload,
    access_token
  }),
  deleteWebhook: async (access_token, api_domain, channel_id) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.DELETE,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.WEBHOOKS}?channel_ids=${channel_id}`,
    access_token
  }),
  fetchUsers: async (access_token, api_domain, params) => {
    let qs = new URLSearchParams();
    params != null && params.type && qs.set("type", params.type), params != null && params.page && qs.set("page", String(params.page)), params != null && params.per_page && qs.set("per_page", String(params.per_page));
    let query = qs.toString();
    return fireHttpRequest({
      method: import_pieces_common.HttpMethod.GET,
      path: `${api_domain}/bigin/v2${API_ENDPOINTS.USERS}${query ? `?${query}` : ""}`,
      access_token
    });
  },
  createCompany: async (access_token, api_domain, body) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.COMPANIES}`,
    access_token,
    body
  }),
  updateCompany: async (access_token, api_domain, body) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.PUT,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.COMPANIES}`,
    access_token,
    body
  }),
  fetchCompanies: async (access_token, api_domain) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.COMPANIES}?fields=Owner,Account_Name,Phone,Website,Tag,Description,Billing_Street,Billing_State,Billing_Country,Billing_Code`,
    access_token
  }),
  createContact: async (access_token, api_domain, body) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.CONTACTS}`,
    access_token,
    body
  }),
  updateContact: async (access_token, api_domain, body) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.PUT,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.CONTACTS}`,
    access_token,
    body
  }),
  fetchTags: async (access_token, api_domain, module2) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.TAGS}?module=${module2}`,
    access_token
  }),
  fetchContacts: async (access_token, api_domain) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.CONTACTS}?fields=First_Name,Last_Name,Title,Email,Mobile,Email_Opt_Out,Owner,Account_Name,Tag,Description,Mailing_Street,Mailing_City,Mailing_State,Mailing_Country,Mailing_Zip`,
    access_token
  }),
  fetchPipelinesRecords: async (access_token, api_domain) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.PIPELINES}?fields=Deal_Name,Stage,Owner,Description,Contact_Name,Account_Name,Amount,Closing_Date,Sub_Pipeline,Tag`,
    access_token
  }),
  createPipelineRecord: async (access_token, api_domain, body) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.PIPELINES}`,
    access_token,
    body
  }),
  updatePipelineRecord: async (access_token, api_domain, body) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.PUT,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.PIPELINES}`,
    access_token,
    body
  }),
  fetchProducts: async (access_token, api_domain) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.PRODUCTS}?fields=Product_Name,Owner,Product_Code,Product_Category,Unit_Price,Description,Tag,Product_Active`,
    access_token
  }),
  fetchTasks: async (access_token, api_domain) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.TASKS}?fields=Subject,Status,Owner,Due_Date,Description,Priority,Related_To,Tags,Remind_At,Recurring_Activity,$related_module`,
    access_token
  }),
  createCall: async (access_token, api_domain, body) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.CALLS}`,
    access_token,
    body
  }),
  createEvent: async (access_token, api_domain, body) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.EVENTS}`,
    access_token,
    body
  }),
  fetchEvents: async (access_token, api_domain) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.EVENTS}?fields=Owner,Event_Title,Start_DateTime,End_DateTime,Description,Recurring_Activity,Participants,Related_To,Tag,Remind_At,$related_module,All_day,Venue`,
    access_token
  }),
  updateEvent: async (access_token, api_domain, body) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.PUT,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.EVENTS}`,
    access_token,
    body
  }),
  createTask: async (access_token, api_domain, body) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.POST,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.TASKS}`,
    access_token,
    body
  }),
  updateTask: async (access_token, api_domain, body) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.PUT,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.TASKS}`,
    access_token,
    body
  }),
  fetchModules: async (access_token, api_domain) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.MODULES}`,
    access_token
  }),
  fetchLayouts: async (access_token, api_domain, module_name) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.LAYOUTS}?module=${module_name}`,
    access_token
  }),
  fetchModuleFields: async (access_token, api_domain, module_name) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    path: `${api_domain}/bigin/v2${API_ENDPOINTS.FIELDS}?module=${module_name}`,
    access_token
  }),
  searchRecords: async (access_token, api_domain, module_name, query) => fireHttpRequest({
    method: import_pieces_common.HttpMethod.GET,
    path: `${api_domain}/bigin/v2/${module_name}/search?${encodeURIComponent(query.key)}=${encodeURIComponent(query.value)}`,
    access_token
  })
};

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/common/helpers.ts
function handleDropdownError(msg) {
  return {
    disabled: !0,
    options: [],
    placeholder: msg
  };
}
function getSafeLabel(item) {
  var _a, _b;
  return item.First_Name || item.Last_Name ? `${(_a = item.First_Name) != null ? _a : ""} ${(_b = item.Last_Name) != null ? _b : ""}`.trim() : (item == null ? void 0 : item.Deal_Name) || (item == null ? void 0 : item.Pipeline_Name) || (item == null ? void 0 : item.Account_Name) || (item == null ? void 0 : item.id);
}
function formatDateTime(input) {
  let date = new Date(input);
  if (isNaN(date.getTime()))
    throw new Error("Invalid date provided");
  return date.toISOString().replace(/\.\d{3}Z$/, "+00:00");
}
function formatDateOnly(input) {
  let date = new Date(input), pad = (n) => n.toString().padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(
    date.getDate()
  )}`;
}
function getZohoBiginAccountAuthorizationUrl(region) {
  return region === "CN" ? "https://accounts.zoho.com.cn" : "https://accounts.zoho.com";
}

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/auth.ts
var biginAuth = import_pieces_framework.PieceAuth.OAuth2({
  authUrl: "{domain}/oauth/v2/auth",
  tokenUrl: "{domain}/oauth/v2/token",
  required: !0,
  scope: [
    "ZohoBigin.modules.ALL",
    "ZohoBigin.settings.ALL",
    "ZohoBigin.users.ALL",
    "ZohoBigin.notifications.ALL",
    "ZohoSearch.securesearch.READ"
  ],
  props: {
    domain: import_pieces_framework.Property.StaticDropdown({
      displayName: "Your Data Center Region",
      description: "Select your Zoho data center region for your account",
      required: !0,
      options: {
        options: DATA_CENTER_REGIONS.map((region) => ({
          label: region.LABEL,
          value: getZohoBiginAccountAuthorizationUrl(region.REGION)
        }))
      },
      defaultValue: getZohoBiginAccountAuthorizationUrl(
        DATA_CENTER_REGIONS[0].REGION
      )
    })
  },
  validate: async ({ auth }) => {
    var _a;
    let { domain } = auth.props;
    if (!domain)
      return {
        valid: !1,
        error: "Please select your data center region."
      };
    try {
      let region = DATA_CENTER_REGIONS.find(
        (r) => r.ACCOUNTS_DOMAIN === domain || getZohoBiginAccountAuthorizationUrl(r.REGION) === domain
      ), apiDomain = (_a = region == null ? void 0 : region.API_DOMAIN) != null ? _a : "https://www.zohoapis.com";
      return await biginApiService.fetchModules(auth.access_token, apiDomain), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Could not validate OAuth credentials. please check your Client ID, Secret, and Region."
      };
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/triggers/new-contact-created.ts
var CACHE_KEY = "bigin_created_contact_trigger", newContactCreated = (0, import_pieces_framework2.createTrigger)({
  auth: biginAuth,
  name: "newContactCreated",
  classification: "READ",
  displayName: "New Contact Created",
  description: "Triggers when a new contact is created",
  aiMetadata: {
    description: "Fires when a new contact (person) record is created in Bigin CRM, via a Contacts.create webhook. Represents a newly added person."
  },
  props: {},
  sampleData: {
    server_time: 1754252081534,
    query_params: {},
    module: "Contacts",
    resource_uri: "https://www.zohoapis.com/bigin/v1/Contacts",
    ids: ["6913232000000583260"],
    affected_fields: [],
    operation: "insert",
    channel_id: "1722705400"
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    try {
      let channel_id = Date.now().toString(), { api_domain } = context.auth.data, webhookData = {
        watch: [
          {
            channel_id,
            events: ["Contacts.create"],
            notify_url: context.webhookUrl,
            token: ZOHO_WEBHOOK_TOKEN
          }
        ]
      };
      await biginApiService.createWebhook(context.auth.access_token, webhookData, api_domain), await context.store.put(CACHE_KEY, channel_id);
    } catch (error) {
      console.error("Failed to enable Zoho webhook");
    }
  },
  async onDisable(context) {
    let channel_id = await context.store.get(CACHE_KEY);
    if (!channel_id) return;
    let { api_domain } = context.auth.data;
    await biginApiService.deleteWebhook(
      context.auth.access_token,
      api_domain,
      channel_id
    ), await context.store.delete(CACHE_KEY);
  },
  async run(context) {
    let { token } = context.payload.body;
    if (token !== ZOHO_WEBHOOK_TOKEN)
      throw new Error("Invalid webhook token");
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/index.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/triggers/company-updated.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var CACHE_KEY2 = "bigin_company_updated_trigger", companyUpdated = (0, import_pieces_framework3.createTrigger)({
  auth: biginAuth,
  name: "companyUpdated",
  classification: "READ",
  displayName: "Company Updated",
  description: "Triggers when a company record is updated",
  aiMetadata: {
    description: "Fires when an existing company (account) record is edited in Bigin CRM, via an Accounts.edit webhook. Represents a change to an organization already in the CRM."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    try {
      let channel_id = Date.now().toString(), { api_domain } = context.auth.data, webhookData = {
        watch: [
          {
            channel_id,
            events: ["Accounts.edit"],
            notify_url: context.webhookUrl,
            token: ZOHO_WEBHOOK_TOKEN
          }
        ]
      };
      await biginApiService.createWebhook(
        context.auth.access_token,
        webhookData,
        api_domain
      ), await context.store.put(CACHE_KEY2, channel_id);
    } catch (error) {
      console.error("Failed to enable Zoho webhook");
    }
  },
  async onDisable(context) {
    let channel_id = await context.store.get(CACHE_KEY2);
    if (!channel_id) return;
    let { api_domain } = context.auth.data;
    await biginApiService.deleteWebhook(
      context.auth.access_token,
      api_domain,
      channel_id
    ), await context.store.delete(CACHE_KEY2);
  },
  async run(context) {
    let { token } = context.payload.body;
    if (token !== ZOHO_WEBHOOK_TOKEN)
      throw new Error("Invalid webhook token");
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/triggers/contact-updated.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var CACHE_KEY3 = "bigin_updated_contact_trigger", contactUpdated = (0, import_pieces_framework4.createTrigger)({
  auth: biginAuth,
  name: "contactUpdated",
  classification: "READ",
  displayName: "Contact Updated",
  description: "Triggers when a contact is updated",
  aiMetadata: {
    description: "Fires when an existing contact (person) record is edited in Bigin CRM, via a Contacts.edit webhook. Represents a change to a person already in the CRM."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    try {
      let channel_id = Date.now().toString(), { api_domain } = context.auth.data, webhookData = {
        watch: [
          {
            channel_id,
            events: ["Contacts.edit"],
            notify_url: context.webhookUrl,
            token: ZOHO_WEBHOOK_TOKEN
          }
        ]
      };
      await biginApiService.createWebhook(context.auth.access_token, webhookData, api_domain), await context.store.put(CACHE_KEY3, channel_id);
    } catch (error) {
      console.error("Failed to enable Zoho webhook");
    }
  },
  async onDisable(context) {
    let channel_id = await context.store.get(CACHE_KEY3);
    if (!channel_id) return;
    let { api_domain } = context.auth.data;
    await biginApiService.deleteWebhook(
      context.auth.access_token,
      api_domain,
      channel_id
    ), await context.store.delete(CACHE_KEY3);
  },
  async run(context) {
    let { token } = context.payload.body;
    if (token !== ZOHO_WEBHOOK_TOKEN)
      throw new Error("Invalid webhook token");
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/triggers/new-call-created.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var CACHE_KEY4 = "bigin_new_call_created_trigger", newCallCreated = (0, import_pieces_framework5.createTrigger)({
  auth: biginAuth,
  name: "newCallCreated",
  classification: "READ",
  displayName: "New Call Created",
  description: "Triggers when a new call log is created",
  aiMetadata: {
    description: "Fires when a new call log entry is created in Bigin CRM, via a Calls.create webhook. Represents a newly recorded phone interaction."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    try {
      let channel_id = Date.now().toString(), { api_domain } = context.auth.data, webhookData = {
        watch: [
          {
            channel_id,
            events: ["Calls.create"],
            notify_url: context.webhookUrl,
            token: ZOHO_WEBHOOK_TOKEN
          }
        ]
      };
      await biginApiService.createWebhook(
        context.auth.access_token,
        webhookData,
        api_domain
      ), await context.store.put(CACHE_KEY4, channel_id);
    } catch (error) {
      console.error("Failed to enable Zoho webhook");
    }
  },
  async onDisable(context) {
    let channel_id = await context.store.get(CACHE_KEY4);
    if (!channel_id) return;
    let { api_domain } = context.auth.data;
    await biginApiService.deleteWebhook(
      context.auth.access_token,
      api_domain,
      channel_id
    ), await context.store.delete(CACHE_KEY4);
  },
  async run(context) {
    let { token } = context.payload.body;
    if (token !== ZOHO_WEBHOOK_TOKEN)
      throw new Error("Invalid webhook token");
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/triggers/new-company-created.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var CACHE_KEY5 = "bigin_new_company_created_trigger", newCompanyCreated = (0, import_pieces_framework6.createTrigger)({
  auth: biginAuth,
  name: "newCompanyCreated",
  classification: "READ",
  displayName: "New Company Created",
  description: "Triggers when a new company record is created",
  aiMetadata: {
    description: "Fires when a new company (account) record is created in Bigin CRM, via an Accounts.create webhook. Represents a newly added organization."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    try {
      let channel_id = Date.now().toString(), { api_domain } = context.auth.data, webhookData = {
        watch: [
          {
            channel_id,
            events: ["Accounts.create"],
            notify_url: context.webhookUrl,
            token: ZOHO_WEBHOOK_TOKEN
          }
        ]
      };
      await biginApiService.createWebhook(
        context.auth.access_token,
        webhookData,
        api_domain
      ), await context.store.put(CACHE_KEY5, channel_id);
    } catch (error) {
      console.error("Failed to enable Zoho webhook");
    }
  },
  async onDisable(context) {
    let channel_id = await context.store.get(CACHE_KEY5);
    if (!channel_id) return;
    let { api_domain } = context.auth.data;
    await biginApiService.deleteWebhook(
      context.auth.access_token,
      api_domain,
      channel_id
    ), await context.store.delete(CACHE_KEY5);
  },
  async run(context) {
    let { token } = context.payload.body;
    if (token !== ZOHO_WEBHOOK_TOKEN)
      throw new Error("Invalid webhook token");
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/triggers/new-task-created.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var CACHE_KEY6 = "bigin_new_task_created_trigger", newTaskCreated = (0, import_pieces_framework7.createTrigger)({
  auth: biginAuth,
  name: "newTaskCreated",
  classification: "READ",
  displayName: "New Task Created",
  description: "Triggers when a new task is created",
  aiMetadata: {
    description: "Fires when a new task is created in Bigin CRM, via a Tasks.create webhook. Represents a newly added piece of follow-up work."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    try {
      let channel_id = Date.now().toString(), { api_domain } = context.auth.data, webhookData = {
        watch: [
          {
            channel_id,
            events: ["Tasks.create"],
            notify_url: context.webhookUrl,
            token: ZOHO_WEBHOOK_TOKEN
          }
        ]
      };
      await biginApiService.createWebhook(
        context.auth.access_token,
        webhookData,
        api_domain
      ), await context.store.put(CACHE_KEY6, channel_id);
    } catch (error) {
      console.error("Failed to enable Zoho webhook");
    }
  },
  async onDisable(context) {
    let channel_id = await context.store.get(CACHE_KEY6);
    if (!channel_id) return;
    let { api_domain } = context.auth.data;
    await biginApiService.deleteWebhook(
      context.auth.access_token,
      api_domain,
      channel_id
    ), await context.store.delete(CACHE_KEY6);
  },
  async run(context) {
    let { token } = context.payload.body;
    if (token !== ZOHO_WEBHOOK_TOKEN)
      throw new Error("Invalid webhook token");
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/triggers/new-event-created.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var CACHE_KEY7 = "bigin_new_event_created_trigger", newEventCreated = (0, import_pieces_framework8.createTrigger)({
  auth: biginAuth,
  name: "newEventCreated",
  classification: "READ",
  displayName: "New Event Created",
  description: "Triggers when a new event is created",
  aiMetadata: {
    description: "Fires when a new calendar event is created in Bigin CRM, via an Events.create webhook. Represents a newly scheduled meeting or appointment."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    try {
      let channel_id = Date.now().toString(), { api_domain } = context.auth.data, webhookData = {
        watch: [
          {
            channel_id,
            events: ["Events.create"],
            notify_url: context.webhookUrl,
            token: ZOHO_WEBHOOK_TOKEN
          }
        ]
      };
      await biginApiService.createWebhook(
        context.auth.access_token,
        webhookData,
        api_domain
      ), await context.store.put(CACHE_KEY7, channel_id);
    } catch (error) {
      console.error("Failed to enable Zoho webhook");
    }
  },
  async onDisable(context) {
    let channel_id = await context.store.get(CACHE_KEY7);
    if (!channel_id) return;
    let { api_domain } = context.auth.data;
    await biginApiService.deleteWebhook(
      context.auth.access_token,
      api_domain,
      channel_id
    ), await context.store.delete(CACHE_KEY7);
  },
  async run(context) {
    let { token } = context.payload.body;
    if (token !== ZOHO_WEBHOOK_TOKEN)
      throw new Error("Invalid webhook token");
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/triggers/new-pipeline-record.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var CACHE_KEY8 = "bigin_new_pipeline_record_created_trigger", newPipelineRecordCreated = (0, import_pieces_framework9.createTrigger)({
  auth: biginAuth,
  name: "newPipelineRecordCreated",
  classification: "READ",
  displayName: "New Pipeline Record Created",
  description: "Triggers when a new pipeline record is created",
  aiMetadata: {
    description: "Fires when a new pipeline record (deal) is created in Bigin CRM, via a Pipelines.create webhook. Represents a newly opened sales opportunity."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    try {
      let channel_id = Date.now().toString(), { api_domain } = context.auth.data, webhookData = {
        watch: [
          {
            channel_id,
            events: ["Pipelines.create"],
            notify_url: context.webhookUrl,
            token: ZOHO_WEBHOOK_TOKEN
          }
        ]
      };
      await biginApiService.createWebhook(
        context.auth.access_token,
        webhookData,
        api_domain
      ), await context.store.put(CACHE_KEY8, channel_id);
    } catch (error) {
      console.error("Failed to enable Zoho webhook");
    }
  },
  async onDisable(context) {
    let channel_id = await context.store.get(CACHE_KEY8);
    if (!channel_id) return;
    let { api_domain } = context.auth.data;
    await biginApiService.deleteWebhook(
      context.auth.access_token,
      api_domain,
      channel_id
    ), await context.store.delete(CACHE_KEY8);
  },
  async run(context) {
    let { token } = context.payload.body;
    if (token !== ZOHO_WEBHOOK_TOKEN)
      throw new Error("Invalid webhook token");
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/triggers/pipeline-record-updated.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var CACHE_KEY9 = "bigin_pipeline_record_updated_trigger", pipelineRecordUpdated = (0, import_pieces_framework10.createTrigger)({
  auth: biginAuth,
  name: "pipelineRecordUpdated",
  classification: "READ",
  displayName: "Pipeline Record Updated",
  description: "Triggers when a pipeline record is updated",
  aiMetadata: {
    description: "Fires when an existing pipeline record (deal) is edited in Bigin CRM, via a Pipelines.edit webhook. Represents a change to a deal already in the CRM, such as a stage or amount update."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    try {
      let channel_id = Date.now().toString(), { api_domain } = context.auth.data, webhookData = {
        watch: [
          {
            channel_id,
            events: ["Pipelines.edit"],
            notify_url: context.webhookUrl,
            token: ZOHO_WEBHOOK_TOKEN
          }
        ]
      };
      await biginApiService.createWebhook(
        context.auth.access_token,
        webhookData,
        api_domain
      ), await context.store.put(CACHE_KEY9, channel_id);
    } catch (error) {
      console.error("Failed to enable Zoho webhook");
    }
  },
  async onDisable(context) {
    let channel_id = await context.store.get(CACHE_KEY9);
    if (!channel_id) return;
    let { api_domain } = context.auth.data;
    await biginApiService.deleteWebhook(
      context.auth.access_token,
      api_domain,
      channel_id
    ), await context.store.delete(CACHE_KEY9);
  },
  async run(context) {
    let { token } = context.payload.body;
    if (token !== ZOHO_WEBHOOK_TOKEN)
      throw new Error("Invalid webhook token");
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/create-company.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/common/props.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var usersDropdown = import_pieces_framework11.Property.Dropdown({
  auth: biginAuth,
  displayName: "Owner",
  description: "Select the owner to which the record will be assigned.",
  required: !1,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return handleDropdownError("Please connect your account first");
    let { api_domain } = auth;
    try {
      let response = await biginApiService.fetchUsers(
        auth.access_token,
        api_domain
      ), users = Array.isArray(response == null ? void 0 : response.users) ? response.users : [];
      return users.length === 0 ? handleDropdownError("No users found") : {
        disabled: !1,
        options: users.map((user) => ({
          label: (typeof user.full_name == "string" && user.full_name.trim() !== "" ? user.full_name : [user.first_name, user.last_name].filter((p) => typeof p == "string" && p.trim() !== "").join(" ")) || user.email || user.id,
          value: user.id
        }))
      };
    } catch (error) {
      return handleDropdownError("Failed to load users");
    }
  }
}), companyDropdown = import_pieces_framework11.Property.Dropdown({
  auth: biginAuth,
  displayName: "Company",
  description: "The ID of the company to which the record will be associated. If not provided, the record will not be associated with any company.",
  required: !1,
  refreshers: ["auth"],
  options: async ({ auth }) => auth ? {
    options: (await biginApiService.fetchCompanies(
      auth.access_token,
      auth.api_domain
    )).data.map((a) => ({
      label: a.Account_Name,
      value: a.id
    }))
  } : handleDropdownError("Please Connect your account first")
}), contactsDropdown = import_pieces_framework11.Property.Dropdown({
  auth: biginAuth,
  displayName: "Contacts",
  description: "The ID of the contact to which the record will be associated. If not provided, the record will not be associated with any contact.",
  required: !1,
  refreshers: ["auth"],
  options: async ({ auth }) => auth ? {
    options: (await biginApiService.fetchContacts(
      auth.access_token,
      auth.api_domain
    )).data.map((a) => ({
      label: a.First_Name + " " + a.Last_Name,
      value: a.id
    }))
  } : handleDropdownError("Please Connect your account first")
}), multiContactsDropdown = import_pieces_framework11.Property.MultiSelectDropdown({
  auth: biginAuth,
  displayName: "Secondary Contacts",
  description: "Provide a list of additional contacts associated with the record",
  required: !1,
  refreshers: ["auth"],
  options: async ({ auth }) => auth ? {
    options: (await biginApiService.fetchContacts(
      auth.access_token,
      auth.api_domain
    )).data.map((a) => ({
      label: a.First_Name + " " + a.Last_Name,
      value: a.id
    }))
  } : handleDropdownError("Please Connect your account first")
}), pipelineRecordsDropdown = import_pieces_framework11.Property.Dropdown({
  auth: biginAuth,
  displayName: "Pipeline Record",
  description: "Select a pipeline record",
  required: !1,
  refreshers: ["auth"],
  options: async ({ auth }) => auth ? {
    options: (await biginApiService.fetchPipelinesRecords(
      auth.access_token,
      auth.api_domain
    )).data.map((a) => ({
      label: a.Deal_Name,
      value: JSON.stringify(a)
    }))
  } : handleDropdownError("Please Connect your account first")
}), layoutsDropdown = (displayName, description, module_name) => import_pieces_framework11.Property.Dropdown({
  auth: biginAuth,
  displayName,
  description,
  required: !1,
  refreshers: ["auth"],
  options: async ({ auth }) => auth ? {
    options: (await biginApiService.fetchLayouts(
      auth.access_token,
      auth.api_domain,
      module_name
    )).layouts.map((a) => ({
      label: a.name,
      value: JSON.stringify(a)
    }))
  } : handleDropdownError("Please Connect your account first")
}), productsDropdown = import_pieces_framework11.Property.MultiSelectDropdown({
  auth: biginAuth,
  displayName: "Associated Products",
  description: "Provide a list of products associated with the record",
  required: !1,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth) return handleDropdownError("Please Connect your account first");
    let resp = await biginApiService.fetchProducts(
      auth.access_token,
      auth.api_domain
    );
    return !Array.isArray(resp.data) || resp.data.length === 0 ? handleDropdownError("No products found. Please add products first.") : {
      options: resp.data.map((a) => ({
        label: a.Product_Name,
        value: JSON.stringify(a)
      }))
    };
  }
}), tagsDropdown = (module2, defaultValue) => import_pieces_framework11.Property.MultiSelectDropdown({
  auth: biginAuth,
  displayName: "Tags",
  description: `Select tags to associate with this module, "${module2}".`,
  required: !1,
  refreshers: ["auth"],
  defaultValue: defaultValue || [],
  options: async ({ auth }) => {
    if (!auth) return handleDropdownError("Please Connect your account first");
    try {
      let resp = await biginApiService.fetchTags(
        auth.access_token,
        auth.api_domain,
        module2
      ), tags = Array.isArray(resp == null ? void 0 : resp.tags) ? resp.tags : [];
      return tags.length === 0 ? handleDropdownError("No tags found. Please add tags first.") : {
        options: tags.map((a) => ({
          label: a.name,
          value: a.name
        }))
      };
    } catch (e) {
      return handleDropdownError("Failed to load tags");
    }
  }
}), SubPipelineorStageDropdown = (displayName, description, field_label, defaultValue) => import_pieces_framework11.Property.Dropdown({
  auth: biginAuth,
  displayName,
  description,
  required: !0,
  refreshers: ["auth", "pipeline"],
  defaultValue,
  options: async ({ auth, pipeline }) => {
    if (!auth)
      return handleDropdownError("Please connect your account first");
    if (!pipeline)
      return handleDropdownError("Please select a Pipeline first");
    let { access_token, api_domain } = auth, response = await biginApiService.fetchLayouts(
      access_token,
      api_domain,
      "Pipelines"
    ), layouts = (response == null ? void 0 : response.layouts) || [], parsedPipeline = JSON.parse(pipeline), PipelineInfo = layouts.find((l) => l.id === parsedPipeline.id).sections.find(
      (s) => s.api_name === "Pipeline Information"
    );
    return {
      options: (PipelineInfo == null ? void 0 : PipelineInfo.fields.find(
        (f) => f.field_label === field_label
      )).pick_list_values.map((pl) => ({
        label: pl.display_value,
        value: pl.actual_value
      }))
    };
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/create-company.ts
var createCompany = (0, import_pieces_framework12.createAction)({
  auth: biginAuth,
  name: "createCompany",
  classification: "WRITE",
  displayName: "Create Company",
  description: "Creates a Company Record",
  audience: "both",
  aiMetadata: { description: "Creates a new company (account) record in Bigin CRM with a name plus optional phone, website, billing address, owner, tags, and description. Use to add an organization the business deals with. Not idempotent: each call creates a new company even if one with the same name already exists, so search first if you need to avoid duplicates.", idempotent: !1 },
  props: {
    accountName: import_pieces_framework12.Property.ShortText({
      displayName: "Account Name",
      description: "Provide the name of the company",
      required: !0
    }),
    phone: import_pieces_framework12.Property.ShortText({
      displayName: "Phone",
      description: "Provide a phone number for the company",
      required: !1
    }),
    website: import_pieces_framework12.Property.ShortText({
      displayName: "Website",
      description: "Provide a website URL for the company",
      required: !1
    }),
    tag: tagsDropdown("Accounts"),
    description: import_pieces_framework12.Property.LongText({
      displayName: "Description",
      description: "Provide additional descriptions or notes related to the company",
      required: !1
    }),
    owner: usersDropdown,
    billingStreet: import_pieces_framework12.Property.ShortText({
      displayName: "Billing Street",
      description: "The street address of the company",
      required: !1
    }),
    billingCity: import_pieces_framework12.Property.ShortText({
      displayName: "Billing City",
      description: "The city where the company is located",
      required: !1
    }),
    billingState: import_pieces_framework12.Property.ShortText({
      displayName: "Billing State",
      description: "The state or province where the company is located",
      required: !1
    }),
    billingCountry: import_pieces_framework12.Property.ShortText({
      displayName: "Billing Country",
      description: "The country of the company",
      required: !1
    }),
    billingCode: import_pieces_framework12.Property.ShortText({
      displayName: "Billing Code",
      description: "The ZIP or postal code of the company",
      required: !1
    })
  },
  async run(context) {
    try {
      let {
        accountName,
        owner,
        phone,
        website,
        tag,
        description,
        billingStreet,
        billingCity,
        billingState,
        billingCountry,
        billingCode
      } = context.propsValue, record = {
        Account_Name: accountName,
        Phone: phone,
        Website: website,
        Description: description,
        Billing_Street: billingStreet,
        Billing_City: billingCity,
        Billing_State: billingState,
        Billing_Country: billingCountry,
        Billing_Code: billingCode,
        Owner: owner ? { id: owner } : void 0,
        Tag: Array.isArray(tag) && tag.length > 0 ? tag.map((t) => ({ name: t })) : void 0
      };
      return Object.keys(record).forEach((k) => {
        let v = record[k];
        (v == null || typeof v == "string" && v.trim() === "" || Array.isArray(v) && v.length === 0) && delete record[k];
      }), {
        message: "Company created successfully",
        data: (await biginApiService.createCompany(
          context.auth.access_token,
          context.auth.api_domain,
          { data: [record] }
        )).data[0]
      };
    } catch (error) {
      throw console.error("Error creating company:", error), new Error(
        error instanceof Error ? `Failed to create company: ${error.message}` : "Failed to create company due to an unknown error"
      );
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/update-company.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var updateCompany = (0, import_pieces_framework13.createAction)({
  auth: biginAuth,
  name: "updateCompany",
  classification: "WRITE",
  displayName: "Update Company",
  description: "Updates an existing Company and prepopulates its fields for editing.",
  audience: "both",
  aiMetadata: { description: "Updates an existing company (account) record in Bigin CRM, identified by selecting the company; its current fields are prepopulated and any you set are overwritten (owner, tags, billing address, and module-defined fields). Use to modify an organization that already exists. Idempotent: re-sending the same field values leaves the record in the same state.", idempotent: !0 },
  props: {
    companyId: import_pieces_framework13.Property.Dropdown({
      auth: biginAuth,
      displayName: "Select Company",
      description: "Choose a company to update",
      required: !0,
      refreshers: ["auth"],
      options: async (context) => context.auth ? {
        options: (await biginApiService.fetchCompanies(
          context.auth.access_token,
          context.auth.data.api_domain
        )).data.map((company) => ({
          label: company.Account_Name,
          value: JSON.stringify(company)
        }))
      } : handleDropdownError("Please connect your account first")
    }),
    owner: usersDropdown,
    companyDetails: import_pieces_framework13.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Company Details",
      description: "These fields will be prepopulated with company data",
      refreshers: ["companyId", "auth"],
      required: !0,
      props: async ({ companyId, auth }) => {
        var _a, _b, _c;
        if (!companyId) return {};
        let company = JSON.parse(companyId), { access_token, data } = auth, api_domain = data.api_domain, [fieldsResp, usersResp] = await Promise.all([
          biginApiService.fetchModuleFields(access_token, api_domain, "Accounts"),
          biginApiService.fetchUsers(access_token, api_domain)
        ]), usersOptions = (usersResp.users || []).map((u) => {
          var _a2, _b2;
          return {
            label: `${(_a2 = u.first_name) != null ? _a2 : ""} ${(_b2 = u.last_name) != null ? _b2 : ""}`.trim(),
            value: u.id
          };
        }), props = {}, fields = fieldsResp.fields || [];
        for (let f of fields) {
          let apiName = f.api_name;
          if (f.read_only || f.field_read_only || !f.view_type || f.view_type.edit !== !0 || apiName === "Tag" || apiName === "id") continue;
          let defaultValue = (_a = company[apiName]) != null ? _a : void 0;
          switch (apiName === "Owner" && (defaultValue = (_b = company.Owner) == null ? void 0 : _b.id), (_c = f.data_type) == null ? void 0 : _c.toLowerCase()) {
            case "picklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework13.Property.StaticDropdown({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue,
                options: { options }
              });
              break;
            }
            case "multiselectpicklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework13.Property.StaticMultiSelectDropdown({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue,
                options: { options }
              });
              break;
            }
            case "boolean": {
              props[apiName] = import_pieces_framework13.Property.Checkbox({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue: !!defaultValue
              });
              break;
            }
            case "date": {
              props[apiName] = import_pieces_framework13.Property.ShortText({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue
              });
              break;
            }
            case "datetime": {
              props[apiName] = import_pieces_framework13.Property.DateTime({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue
              });
              break;
            }
            case "integer":
            case "long":
            case "double":
            case "decimal":
            case "currency":
            case "percent": {
              props[apiName] = import_pieces_framework13.Property.Number({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue
              });
              break;
            }
            default: {
              if (apiName === "Owner") {
                props[apiName] = import_pieces_framework13.Property.StaticDropdown({
                  displayName: "Owner",
                  description: f.tooltip || void 0,
                  required: !1,
                  defaultValue,
                  options: { options: usersOptions }
                });
                break;
              }
              if (apiName === "Description") {
                props[apiName] = import_pieces_framework13.Property.LongText({
                  displayName: "Description",
                  description: f.tooltip || void 0,
                  required: !1,
                  defaultValue
                });
                break;
              }
              props[apiName] = import_pieces_framework13.Property.ShortText({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue: typeof defaultValue == "string" ? defaultValue : void 0
              });
            }
          }
        }
        return props;
      }
    }),
    tag: tagsDropdown("Accounts")
  },
  async run(context) {
    try {
      let companyId = JSON.parse(context.propsValue.companyId).id, updates = context.propsValue.companyDetails, body = { id: companyId };
      for (let [apiName, value] of Object.entries(updates || {}))
        value == null || typeof value == "string" && value.trim() === "" || (apiName === "Owner" ? body.Owner = { id: value } : apiName === "Tag" ? body.Tag = value.map((t) => ({ name: t })) : body[apiName] = value);
      return context.propsValue.owner && (body.Owner = { id: context.propsValue.owner }), context.propsValue.tag && Array.isArray(context.propsValue.tag) && context.propsValue.tag.length > 0 && (body.Tag = context.propsValue.tag.map((t) => ({ name: t }))), {
        message: "Company updated successfully",
        data: (await biginApiService.updateCompany(
          context.auth.access_token,
          context.auth.api_domain,
          { data: [body] }
        )).data[0]
      };
    } catch (error) {
      throw console.error("Error updating company:", error), new Error(error);
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/create-contact.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var createContact = (0, import_pieces_framework14.createAction)({
  auth: biginAuth,
  name: "createContact",
  classification: "WRITE",
  displayName: "Create Contact",
  description: "Creates a Contact Record",
  audience: "both",
  aiMetadata: { description: "Creates a new contact (person) record in Bigin CRM. Last name is required; optionally set email, mobile, title, owner, associated company, tags, mailing address, and description. Use to add a person to the CRM. Not idempotent: each call creates a new contact even with identical details, so search first to avoid duplicates.", idempotent: !1 },
  props: {
    firstName: import_pieces_framework14.Property.ShortText({
      displayName: "First Name",
      description: "First name of the contact",
      required: !1
    }),
    lastName: import_pieces_framework14.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the contact",
      required: !0
    }),
    title: import_pieces_framework14.Property.ShortText({
      displayName: "Title",
      description: "Job title of the contact",
      required: !1
    }),
    email: import_pieces_framework14.Property.ShortText({
      displayName: "Email",
      description: "Email address of the contact",
      required: !1
    }),
    mobile: import_pieces_framework14.Property.ShortText({
      displayName: "Mobile",
      description: "Mobile phone number",
      required: !1
    }),
    emailOptOut: import_pieces_framework14.Property.Checkbox({
      displayName: "Email Opt Out",
      description: "Whether the contact has opted out of emails",
      required: !1,
      defaultValue: !1
    }),
    owner: usersDropdown,
    accountName: companyDropdown,
    tag: tagsDropdown("Contacts"),
    description: import_pieces_framework14.Property.LongText({
      displayName: "Description",
      description: "Provide additional descriptions or notes related to the contact",
      required: !1
    }),
    mailingStreet: import_pieces_framework14.Property.ShortText({
      displayName: "Mailing Street",
      description: "Street address for mailing",
      required: !1
    }),
    mailingCity: import_pieces_framework14.Property.ShortText({
      displayName: "Mailing City",
      description: "City for mailing address",
      required: !1
    }),
    mailingState: import_pieces_framework14.Property.ShortText({
      displayName: "Mailing State",
      description: "State for mailing address",
      required: !1
    }),
    mailingCountry: import_pieces_framework14.Property.ShortText({
      displayName: "Mailing Country",
      description: "Country for mailing address",
      required: !1
    }),
    mailingZip: import_pieces_framework14.Property.ShortText({
      displayName: "Mailing Zip",
      description: "ZIP/postal code",
      required: !1
    })
  },
  async run(context) {
    try {
      let {
        firstName,
        lastName,
        title,
        email,
        mobile,
        emailOptOut,
        owner,
        accountName,
        tag,
        description,
        mailingStreet,
        mailingCity,
        mailingState,
        mailingCountry,
        mailingZip
      } = context.propsValue, record = {
        First_Name: firstName,
        Last_Name: lastName,
        Title: title,
        Email: email,
        Mobile: mobile,
        Email_Opt_Out: emailOptOut,
        Owner: owner ? { id: owner } : void 0,
        Account_Name: accountName ? { id: accountName } : void 0,
        Tag: tag != null && tag.length ? tag.map((t) => ({ name: t })) : void 0,
        Description: description,
        Mailing_Street: mailingStreet,
        Mailing_City: mailingCity,
        Mailing_State: mailingState,
        Mailing_Country: mailingCountry,
        Mailing_Zip: mailingZip
      };
      Object.keys(record).forEach((k) => {
        let v = record[k];
        (v == null || typeof v == "string" && v.trim() === "" || Array.isArray(v) && v.length === 0) && delete record[k];
      });
      let payload = { data: [record] };
      return {
        message: "Contact created successfully",
        data: (await biginApiService.createContact(
          context.auth.access_token,
          context.auth.data.api_domain,
          payload
        )).data[0]
      };
    } catch (error) {
      throw console.error("Error creating contact:", error), new Error(
        error instanceof Error ? `Failed to create contact: ${error.message}` : "Failed to create contact due to an unknown error"
      );
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/update-contact.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var updateContact = (0, import_pieces_framework15.createAction)({
  auth: biginAuth,
  name: "updateContact",
  classification: "WRITE",
  displayName: "Update Contact",
  description: "Select and update an existing Contact record.",
  audience: "both",
  aiMetadata: { description: "Updates an existing contact (person) record in Bigin CRM, identified by selecting the contact; its current fields are prepopulated and any you set are overwritten (owner, associated company, tags, and module-defined fields). Use to modify a person who already exists. Idempotent: re-sending the same field values leaves the record in the same state.", idempotent: !0 },
  props: {
    contactId: import_pieces_framework15.Property.Dropdown({
      auth: biginAuth,
      displayName: "Select Contact",
      description: "Choose a contact to update",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => auth ? {
        options: (await biginApiService.fetchContacts(
          auth.access_token,
          auth.data.api_domain
        )).data.map((c) => {
          var _a;
          return {
            label: `${(_a = c.First_Name) != null ? _a : ""} ${c.Last_Name}`.trim(),
            value: JSON.stringify(c)
          };
        })
      } : handleDropdownError("Please connect first")
    }),
    contactDetails: import_pieces_framework15.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Contact Fields",
      description: "Edit any of these fields",
      refreshers: ["contactId", "auth"],
      required: !0,
      props: async ({ contactId, auth }) => {
        var _a, _b, _c, _d;
        if (!contactId) return {};
        let contact = JSON.parse(contactId), { access_token, data } = auth, api_domain = data.api_domain, [fieldsResp, usersResp, companiesResp] = await Promise.all([
          biginApiService.fetchModuleFields(access_token, api_domain, "Contacts"),
          biginApiService.fetchUsers(access_token, api_domain),
          biginApiService.fetchCompanies(access_token, api_domain)
        ]), usersOptions = (usersResp.users || []).map((u) => {
          var _a2, _b2;
          return {
            label: `${(_a2 = u.first_name) != null ? _a2 : ""} ${(_b2 = u.last_name) != null ? _b2 : ""}`.trim(),
            value: u.id
          };
        }), companyOptions = (companiesResp.data || []).map((c) => ({
          label: c.Account_Name,
          value: c.id
        })), props = {}, fields = fieldsResp.fields || [];
        for (let f of fields) {
          let apiName = f.api_name;
          if (f.read_only || f.field_read_only || !f.view_type || f.view_type.edit !== !0 || apiName === "Tag" || apiName === "id") continue;
          let defaultValue = (_a = contact[apiName]) != null ? _a : void 0;
          switch (apiName === "Owner" && (defaultValue = (_b = contact.Owner) == null ? void 0 : _b.id), apiName === "Account_Name" && (defaultValue = (_c = contact.Account_Name) == null ? void 0 : _c.id), (_d = f.data_type) == null ? void 0 : _d.toLowerCase()) {
            case "picklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework15.Property.StaticDropdown({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue,
                options: { options }
              });
              break;
            }
            case "multiselectpicklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework15.Property.StaticMultiSelectDropdown({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue,
                options: { options }
              });
              break;
            }
            case "boolean": {
              props[apiName] = import_pieces_framework15.Property.Checkbox({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue: !!defaultValue
              });
              break;
            }
            case "date": {
              props[apiName] = import_pieces_framework15.Property.ShortText({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue
              });
              break;
            }
            case "datetime": {
              props[apiName] = import_pieces_framework15.Property.DateTime({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue
              });
              break;
            }
            case "integer":
            case "long":
            case "double":
            case "decimal":
            case "currency":
            case "percent": {
              props[apiName] = import_pieces_framework15.Property.Number({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue
              });
              break;
            }
            default: {
              if (apiName === "Owner") {
                props[apiName] = import_pieces_framework15.Property.StaticDropdown({
                  displayName: "Owner",
                  description: f.tooltip || void 0,
                  required: !1,
                  defaultValue,
                  options: { options: usersOptions }
                });
                break;
              }
              if (apiName === "Account_Name") {
                props[apiName] = import_pieces_framework15.Property.StaticDropdown({
                  displayName: "Company",
                  description: f.tooltip || void 0,
                  required: !1,
                  defaultValue,
                  options: { options: companyOptions }
                });
                break;
              }
              if (apiName === "Description") {
                props[apiName] = import_pieces_framework15.Property.LongText({
                  displayName: "Description",
                  description: f.tooltip || void 0,
                  required: !1,
                  defaultValue
                });
                break;
              }
              props[apiName] = import_pieces_framework15.Property.ShortText({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue: typeof defaultValue == "string" ? defaultValue : void 0
              });
            }
          }
        }
        return props;
      }
    }),
    accountName: companyDropdown,
    tag: tagsDropdown("Contacts")
  },
  async run(context) {
    try {
      let { contactDetails, contactId, accountName, tag } = context.propsValue, record = { id: JSON.parse(contactId).id };
      if (contactDetails && typeof contactDetails == "object")
        for (let [apiName, value] of Object.entries(contactDetails))
          value == null || typeof value == "string" && value.trim() === "" || Array.isArray(value) && value.length === 0 || (apiName === "Owner" || apiName === "Account_Name" ? record[apiName] = { id: value } : apiName === "Tag" ? record.Tag = value.map((t) => ({ name: t })) : record[apiName] = value);
      accountName && (record.Account_Name = { id: accountName }), tag && Array.isArray(tag) && tag.length > 0 && (record.Tag = tag.map((t) => ({ name: t })));
      let payload = { data: [record] };
      return {
        message: "Contact updated successfully",
        data: (await biginApiService.updateContact(
          context.auth.access_token,
          context.auth.data.api_domain,
          payload
        )).data[0]
      };
    } catch (error) {
      throw console.error("Error updating contact:", error), new Error(
        error instanceof Error ? `Failed to update contact: ${error.message}` : "Failed to update contact due to an unknown error"
      );
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/create-task.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var createTask = (0, import_pieces_framework16.createAction)({
  auth: biginAuth,
  name: "createTask",
  classification: "WRITE",
  displayName: "Create Task",
  description: "Creates a new Task",
  audience: "both",
  aiMetadata: { description: "Creates a new task in Bigin CRM with a subject plus optional due date, owner, priority, status, description, and tags. Supports optional recurrence (daily/weekly/monthly/yearly RRULE) and reminders, and can be linked to a related Contact, Pipeline, or Company record. Use to schedule follow-up work. Not idempotent: each call creates a new task.", idempotent: !1 },
  props: {
    subject: import_pieces_framework16.Property.ShortText({
      displayName: "Subject",
      description: "Provide the subject or title of the task",
      required: !0
    }),
    owner: usersDropdown,
    dueDate: import_pieces_framework16.Property.DateTime({
      displayName: "Due Date",
      description: "Provide the due date of the task (YYYY-MM-DD format)",
      required: !1
    }),
    enableRecurring: import_pieces_framework16.Property.Checkbox({
      displayName: "Make Task Recurring",
      description: "make this task recurring",
      required: !1
    }),
    recurringInfo: import_pieces_framework16.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Recurring Info",
      description: "Please note: Due Date must be set above for recurring tasks",
      refreshers: ["enableRecurring"],
      required: !1,
      props: (propsValue, ctx) => propsValue.enableRecurring ? {
        freq: import_pieces_framework16.Property.StaticDropdown({
          displayName: "Frequency",
          required: !0,
          options: {
            options: [
              { label: "Daily", value: "DAILY" },
              { label: "Weekly", value: "WEEKLY" },
              { label: "Monthly", value: "MONTHLY" },
              { label: "Yearly", value: "YEARLY" }
            ]
          }
        }),
        interval: import_pieces_framework16.Property.Number({
          displayName: "Interval",
          required: !0,
          description: "Indicates the time gap between each event. The INTERVAL value range from 1 to 99. For example, an INTERVAL of 2 for a WEEKLY recurring event means that there will be a two-week gap between each event.",
          defaultValue: 1
        }),
        count: import_pieces_framework16.Property.Number({
          displayName: "Count",
          required: !0,
          defaultValue: 1,
          description: "Indicates the number of events you want to create. THE COUNT value range from 1 to 99. For example, a COUNT of 3 creates three individual events."
        }),
        byday: import_pieces_framework16.Property.StaticDropdown({
          displayName: "By Day",
          required: !1,
          description: "Indicates the day of the week the event repeats. The possible values are SU, MO, TU, WE, TH, FR, or SA. This is applicable only for weekly, and monthly events.",
          options: {
            options: [
              { label: "Sunday", value: "SU" },
              { label: "Monday", value: "MO" },
              { label: "Tuesday", value: "TU" },
              { label: "Wednesday", value: "WE" },
              { label: "Thursday", value: "TH" },
              { label: "Friday", value: "FR" },
              { label: "Saturday", value: "SA" }
            ]
          }
        }),
        bymonthday: import_pieces_framework16.Property.Number({
          displayName: "By Month Day",
          required: !1,
          description: " Indicates the day of the month the event repeats. The BYMONTHDAY value range from 1 to 31. This is applicable only for weekly and monthly events."
        }),
        bysetpos: import_pieces_framework16.Property.StaticDropdown({
          displayName: "By Set Position",
          required: !1,
          description: "Indicates the week of the month the event repeats. The possible values are 1 for the first week of the month, 2 for the second week of the month, 3 the for third week of the month, 4 for the fourth week of the month, or -1 for the last week of the month. This is applicable only for weekly and monthly events.",
          options: {
            options: [
              { label: "First Week of the Month", value: "1" },
              { label: "Second Week of the Month", value: "2" },
              { label: "Third Week of the Month", value: "3" },
              { label: "Fourth Week of the Month", value: "4" },
              { label: "Last Week of the Month", value: "-1" }
            ]
          }
        }),
        until: import_pieces_framework16.Property.ShortText({
          displayName: "Until (YYYY-MM-DD)",
          description: "Date the recurrence ends. Format: YYYY-MM-DD",
          required: !1
        })
      } : {}
    }),
    enableReminder: import_pieces_framework16.Property.Checkbox({
      displayName: "Enable Reminder",
      description: "Enable reminder for this task",
      required: !1
    }),
    reminderInfo: import_pieces_framework16.Property.DynamicProperties({
      displayName: "Reminder Information",
      auth: biginAuth,
      refreshers: ["enableReminder"],
      required: !1,
      props: (propsValue, ctx) => propsValue.enableReminder ? {
        reminderAction: import_pieces_framework16.Property.StaticDropdown({
          displayName: "Reminder Action",
          description: "How the reminder should be shown",
          required: !1,
          options: {
            options: [
              { label: "Email", value: "EMAIL" },
              { label: "Popup", value: "POPUP" },
              { label: "Email and Popup", value: "EMAILANDPOPUP" }
            ]
          }
        }),
        reminderType: import_pieces_framework16.Property.StaticDropdown({
          displayName: "Reminder Type",
          description: "When to trigger the reminder",
          required: !1,
          options: {
            options: [
              { label: "Specific Date & Time", value: "datetime" },
              { label: "Days Before", value: "days_before" },
              { label: "Weeks Before", value: "weeks_before" }
            ]
          }
        }),
        reminderDateTime: import_pieces_framework16.Property.DateTime({
          displayName: "Reminder Date & Time",
          description: "Specific date and time for reminder (for non-recurring tasks)",
          required: !1
        }),
        reminderDaysBefore: import_pieces_framework16.Property.Number({
          displayName: "Days Before",
          description: "Number of days before the task to send reminder",
          required: !1,
          defaultValue: 1
        }),
        reminderWeeksBefore: import_pieces_framework16.Property.Number({
          displayName: "Weeks Before",
          description: "Number of weeks before the task to send reminder",
          required: !1,
          defaultValue: 1
        }),
        reminderTime: import_pieces_framework16.Property.ShortText({
          displayName: "Reminder Time",
          description: "Time for reminder in HH:MM format (24-hour, for recurring tasks)",
          required: !1
        })
      } : {}
    }),
    relatedModule: import_pieces_framework16.Property.StaticDropdown({
      displayName: "Related Module",
      description: "Select the type of entity the task is related to. Options: Contacts, Pipelines, Companies.",
      required: !1,
      defaultValue: "Contacts",
      options: {
        options: [
          { label: "Contacts", value: "Contacts" },
          { label: "Pipelines", value: "Pipelines" },
          { label: "Companies", value: "Companies" }
        ]
      }
    }),
    relatedTo: import_pieces_framework16.Property.Dropdown({
      auth: biginAuth,
      displayName: "Related To",
      description: "Select the specific record the task is related to.",
      required: !1,
      refreshers: ["auth", "relatedModule"],
      defaultValue: {},
      options: async ({ auth, relatedModule }) => {
        if (!auth) return handleDropdownError("Please connect first");
        if (!relatedModule) return { options: [] };
        let { access_token, data } = auth, api_domain = data.api_domain, fetchFn = {
          Contacts: () => biginApiService.fetchContacts(access_token, api_domain),
          Pipelines: () => biginApiService.fetchPipelinesRecords(access_token, api_domain),
          Companies: () => biginApiService.fetchCompanies(access_token, api_domain)
        }[relatedModule], response = await fetchFn();
        return {
          options: ((response == null ? void 0 : response.data) || []).map((item) => ({
            label: getSafeLabel(item),
            value: item.id
          }))
        };
      }
    }),
    description: import_pieces_framework16.Property.LongText({
      displayName: "Description",
      description: "Provide additional descriptions or notes related to the task",
      required: !1
    }),
    priority: import_pieces_framework16.Property.StaticDropdown({
      displayName: "Priority",
      description: "Provide the priority level of the task",
      required: !1,
      options: {
        options: [
          { label: "High", value: "High" },
          { label: "Normal", value: "Normal" },
          { label: "Low", value: "Low" },
          { label: "Lowest", value: "Lowest" },
          { label: "Highest", value: "Highest" }
        ]
      }
    }),
    status: import_pieces_framework16.Property.StaticDropdown({
      displayName: "Status",
      description: "Provide the current status of the task.",
      required: !1,
      options: {
        options: [
          { label: "In Progress", value: "In Progress" },
          { label: "Completed", value: "Completed" },
          { label: "Deferred", value: "Deferred" },
          { label: "Waiting for input", value: "Waiting on someone else" },
          { label: "Not Started", value: "Not Started" }
        ]
      }
    }),
    tag: tagsDropdown("Tasks")
  },
  async run({ auth, propsValue }) {
    let { access_token, data } = auth, api_domain = data.api_domain, taskData = {
      Subject: propsValue.subject
    };
    if (propsValue.owner && (taskData.Owner = { id: propsValue.owner }), propsValue.dueDate && (taskData.Due_Date = formatDateOnly(propsValue.dueDate)), propsValue.enableRecurring && propsValue.recurringInfo) {
      let recurringInfo = propsValue.recurringInfo, rruleParts = [];
      if (!["DAILY", "WEEKLY", "MONTHLY", "YEARLY"].includes(recurringInfo.freq))
        throw new Error("Invalid recurrence frequency. Allowed values: DAILY, WEEKLY, MONTHLY, YEARLY");
      if (rruleParts.push(`FREQ=${recurringInfo.freq}`), recurringInfo.interval !== void 0) {
        let interval = Number(recurringInfo.interval);
        if (Number.isNaN(interval) || interval < 1 || interval > 99)
          throw new Error("Interval must be a number between 1 and 99");
        rruleParts.push(`INTERVAL=${recurringInfo.interval}`);
      }
      if (recurringInfo.count !== void 0) {
        let count = Number(recurringInfo.count);
        if (Number.isNaN(count) || count < 1 || count > 99)
          throw new Error("Count must be a number between 1 and 99");
        rruleParts.push(`COUNT=${recurringInfo.count}`);
      }
      if (recurringInfo.byday && rruleParts.push(`BYDAY=${recurringInfo.byday}`), recurringInfo.bymonthday !== void 0) {
        let bymonthday = Number(recurringInfo.bymonthday);
        if (Number.isNaN(bymonthday) || bymonthday < 1 || bymonthday > 31)
          throw new Error("BYMONTHDAY must be a number between 1 and 31");
        rruleParts.push(`BYMONTHDAY=${recurringInfo.bymonthday}`);
      }
      if (recurringInfo.bysetpos) {
        if (!["1", "2", "3", "4", "-1"].includes(String(recurringInfo.bysetpos)))
          throw new Error("BYSETPOS must be one of 1, 2, 3, 4, -1");
        rruleParts.push(`BYSETPOS=${recurringInfo.bysetpos}`);
      }
      if (recurringInfo.until) {
        let until = formatDateOnly(recurringInfo.until);
        rruleParts.push(`UNTIL=${until}`);
      }
      taskData.Recurring_Activity = {
        RRULE: rruleParts.join(";") + ";"
      };
    }
    if (propsValue.enableReminder && propsValue.reminderInfo) {
      let reminderInfo = propsValue.reminderInfo, alarmValue = `ACTION=${reminderInfo.reminderAction || "EMAIL"}`, reminderTime = reminderInfo.reminderTime || "09:00";
      if (propsValue.recurringInfo && reminderInfo.reminderType === "days_before") {
        let daysBefore = reminderInfo.reminderDaysBefore || 1;
        alarmValue += `;TRIGGER=P${daysBefore}D;TRIGGER_TIME=${reminderTime}`;
      } else if (propsValue.recurringInfo && reminderInfo.reminderType === "weeks_before") {
        let weeksBefore = reminderInfo.reminderWeeksBefore || 1;
        alarmValue += `;TRIGGER=P${weeksBefore}W;TRIGGER_TIME=${reminderTime}`;
      } else reminderInfo.reminderDateTime && (alarmValue += `;TRIGGER=DATE-TIME:${formatDateTime(reminderInfo.reminderDateTime)}`);
      taskData.Remind_At = {
        ALARM: alarmValue
      };
    }
    if (propsValue.relatedTo && propsValue.relatedModule) {
      let relatedModule = {
        Contacts: "Contacts",
        Pipelines: "Deals",
        Companies: "Accounts"
      }[propsValue.relatedModule] || "Contacts";
      taskData.Related_To = { id: propsValue.relatedTo }, taskData.$related_module = relatedModule;
    }
    propsValue.description && (taskData.Description = propsValue.description), propsValue.priority && (taskData.Priority = propsValue.priority), propsValue.status && (taskData.Status = propsValue.status), propsValue.tag && Array.isArray(propsValue.tag) && propsValue.tag.length > 0 && (taskData.Tag = propsValue.tag.map((tagName) => ({ name: tagName })));
    let payload = { data: [taskData] };
    try {
      return (await biginApiService.createTask(
        access_token,
        api_domain,
        payload
      )).data[0];
    } catch (error) {
      throw console.error("Error creating task:", error), new Error(
        error instanceof Error ? `Failed to create task: ${error.message}` : "Failed to create task due to an unknown error"
      );
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/update-task.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var updateTask = (0, import_pieces_framework17.createAction)({
  auth: biginAuth,
  name: "updateTask",
  classification: "WRITE",
  displayName: "Update Task",
  description: "updates a Task",
  audience: "both",
  aiMetadata: { description: "Updates an existing task in Bigin CRM, identified by selecting the task; its current fields are prepopulated and any you set are overwritten (subject, owner, due date, priority, status, description, tags), with optional recurrence and reminder settings and a related Contact/Pipeline/Company link. Use to modify a task that already exists. Idempotent: re-sending the same field values leaves the record in the same state.", idempotent: !0 },
  props: {
    taskId: import_pieces_framework17.Property.Dropdown({
      displayName: "Select Task",
      description: "Choose a task to update",
      auth: biginAuth,
      required: !0,
      refreshers: ["auth"],
      options: async (context) => context.auth ? {
        options: (await biginApiService.fetchTasks(
          context.auth.access_token,
          context.auth.data.api_domain
        )).data.map((task) => ({
          label: task.Subject,
          value: JSON.stringify(task)
        }))
      } : handleDropdownError("Please connect your account first")
    }),
    owner: usersDropdown,
    taskDetails: import_pieces_framework17.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Task Details",
      description: "These fields will be prepopulated with task data",
      refreshers: ["taskId", "auth"],
      required: !0,
      props: async ({ taskId, auth }) => {
        var _a, _b;
        if (!taskId || !auth) return {};
        let task = JSON.parse(taskId), { access_token, data } = auth, api_domain = data.api_domain, fieldsResp = await biginApiService.fetchModuleFields(
          access_token,
          api_domain,
          "Tasks"
        ), props = {};
        for (let f of fieldsResp.fields || []) {
          let apiName = f.api_name;
          if (f.read_only || f.field_read_only || !f.view_type || f.view_type.edit !== !0 || apiName === "Tag" || apiName === "id") continue;
          let defaultValue = (_a = task[apiName]) != null ? _a : void 0;
          switch ((_b = f.data_type) == null ? void 0 : _b.toLowerCase()) {
            case "picklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework17.Property.StaticDropdown({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue,
                options: { options }
              });
              break;
            }
            case "multiselectpicklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework17.Property.StaticMultiSelectDropdown({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue,
                options: { options }
              });
              break;
            }
            case "boolean": {
              props[apiName] = import_pieces_framework17.Property.Checkbox({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue: !!defaultValue
              });
              break;
            }
            case "date": {
              props[apiName] = import_pieces_framework17.Property.ShortText({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || "Format: YYYY-MM-DD",
                required: !1,
                defaultValue
              });
              break;
            }
            case "datetime": {
              props[apiName] = import_pieces_framework17.Property.DateTime({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || "Format: ISO 8601 (YYYY-MM-DDTHH:mm:ss\xB1HH:mm)",
                required: !1,
                defaultValue
              });
              break;
            }
            case "integer":
            case "long":
            case "double":
            case "decimal":
            case "currency":
            case "percent": {
              props[apiName] = import_pieces_framework17.Property.Number({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue
              });
              break;
            }
            default: {
              if (apiName === "Description") {
                props[apiName] = import_pieces_framework17.Property.LongText({
                  displayName: "Description",
                  required: !1,
                  defaultValue
                });
                break;
              }
              props[apiName] = import_pieces_framework17.Property.ShortText({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue: typeof defaultValue == "string" ? defaultValue : void 0
              });
            }
          }
        }
        return props;
      }
    }),
    enableRecurring: import_pieces_framework17.Property.Checkbox({
      displayName: "Make Task Recurring",
      description: "make this task recurring",
      required: !1
    }),
    recurringInfo: import_pieces_framework17.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Recurring Info",
      description: "Please note: Due Date must be set above for recurring tasks",
      refreshers: ["enableRecurring"],
      required: !1,
      props: (propsValue, ctx) => propsValue.enableRecurring ? {
        freq: import_pieces_framework17.Property.StaticDropdown({
          displayName: "Frequency",
          required: !0,
          options: {
            options: [
              { label: "Daily", value: "DAILY" },
              { label: "Weekly", value: "WEEKLY" },
              { label: "Monthly", value: "MONTHLY" }
            ]
          }
        }),
        interval: import_pieces_framework17.Property.Number({
          displayName: "Interval",
          required: !0,
          description: "Indicates the time gap between each event. The INTERVAL value range from 1 to 99. For example, an INTERVAL of 2 for a WEEKLY recurring event means that there will be a two-week gap between each event."
        }),
        count: import_pieces_framework17.Property.Number({
          displayName: "Count",
          required: !0,
          description: "Indicates the number of events you want to create. THE COUNT value range from 1 to 99. For example, a COUNT of 3 creates three individual events."
        }),
        byday: import_pieces_framework17.Property.StaticDropdown({
          displayName: "By Day",
          required: !1,
          description: "Indicates the day of the week the event repeats. The possible values are SU, MO, TU, WE, TH, FR, or SA. This is applicable only for weekly, and monthly events.",
          options: {
            options: [
              { label: "Sunday", value: "SU" },
              { label: "Monday", value: "MO" },
              { label: "Tuesday", value: "TU" },
              { label: "Wednesday", value: "WE" },
              { label: "Thursday", value: "TH" },
              { label: "Friday", value: "FR" },
              { label: "Saturday", value: "SA" }
            ]
          }
        }),
        bymonthday: import_pieces_framework17.Property.Number({
          displayName: "By Month Day",
          required: !1,
          description: " Indicates the day of the month the event repeats. The BYMONTHDAY value range from 1 to 31. This is applicable only for weekly and monthly events."
        }),
        bysetpos: import_pieces_framework17.Property.StaticDropdown({
          displayName: "By Set Position",
          required: !1,
          description: "Indicates the week of the month the event repeats. The possible values are 1 for the first week of the month, 2 for the second week of the month, 3 the for third week of the month, 4 for the fourth week of the month, or -1 for the last week of the month. This is applicable only for weekly and monthly events.",
          options: {
            options: [
              { label: "First Week of the Month", value: "1" },
              { label: "Second Week of the Month", value: "2" },
              { label: "Third Week of the Month", value: "3" },
              { label: "Fourth Week of the Month", value: "4" },
              { label: "Last Week of the Month", value: "-1" }
            ]
          }
        })
      } : {}
    }),
    enableReminder: import_pieces_framework17.Property.Checkbox({
      displayName: "Enable Reminder",
      description: "Enable reminder for this task",
      required: !1
    }),
    reminderInfo: import_pieces_framework17.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Reminder Information",
      refreshers: ["enableReminder"],
      required: !1,
      props: (propsValue, ctx) => propsValue.enableReminder ? {
        reminderAction: import_pieces_framework17.Property.StaticDropdown({
          displayName: "Reminder Action",
          description: "How the reminder should be shown",
          required: !1,
          options: {
            options: [
              { label: "Email", value: "EMAIL" },
              { label: "Popup", value: "POPUP" },
              { label: "Email and Popup", value: "EMAILANDPOPUP" }
            ]
          }
        }),
        reminderType: import_pieces_framework17.Property.StaticDropdown({
          displayName: "Reminder Type",
          description: "When to trigger the reminder",
          required: !1,
          options: {
            options: [
              { label: "Specific Date & Time", value: "datetime" },
              { label: "Days Before", value: "days_before" },
              { label: "Weeks Before", value: "weeks_before" }
            ]
          }
        }),
        reminderDateTime: import_pieces_framework17.Property.DateTime({
          displayName: "Reminder Date & Time",
          description: "Specific date and time for reminder (for non-recurring tasks)",
          required: !1
        }),
        reminderDaysBefore: import_pieces_framework17.Property.Number({
          displayName: "Days Before",
          description: "Number of days before the task to send reminder",
          required: !1
        }),
        reminderWeeksBefore: import_pieces_framework17.Property.Number({
          displayName: "Weeks Before",
          description: "Number of weeks before the task to send reminder",
          required: !1
        }),
        reminderTime: import_pieces_framework17.Property.ShortText({
          displayName: "Reminder Time",
          description: "Time for reminder in HH:MM format (24-hour, for recurring tasks)",
          required: !1
        })
      } : {}
    }),
    relatedModule: import_pieces_framework17.Property.Dropdown({
      displayName: "Related Module",
      description: "Select the type of entity the task is related to. Options: Contacts, Pipelines, Companies.",
      required: !1,
      refreshers: ["auth"],
      defaultValue: "Contacts",
      auth: biginAuth,
      options: async () => ({
        options: [
          { label: "Contacts", value: "Contacts" },
          { label: "Pipelines", value: "Pipelines" },
          { label: "Companies", value: "Companies" }
        ]
      })
    }),
    relatedTo: import_pieces_framework17.Property.Dropdown({
      auth: biginAuth,
      displayName: "Related To",
      description: "Select the specific record the task is related to.",
      required: !1,
      refreshers: ["auth", "relatedModule"],
      options: async ({ auth, relatedModule }) => {
        if (!auth) return handleDropdownError("Please connect first");
        if (!relatedModule) return { options: [] };
        let { access_token, data } = auth, api_domain = data.api_domain, fetchFn = {
          Contacts: () => biginApiService.fetchContacts(access_token, api_domain),
          Pipelines: () => biginApiService.fetchPipelinesRecords(access_token, api_domain),
          Companies: () => biginApiService.fetchCompanies(access_token, api_domain)
        }[relatedModule], response = await fetchFn();
        return {
          options: ((response == null ? void 0 : response.data) || []).map((item) => ({
            label: getSafeLabel(item),
            value: item.id
          }))
        };
      }
    }),
    tag: tagsDropdown("Tasks")
  },
  async run({ auth, propsValue }) {
    let { access_token, data } = auth, api_domain = data.api_domain, taskId = JSON.parse(propsValue.taskId).id, taskDetails = propsValue.taskDetails, taskData = {
      Subject: taskDetails.subject,
      id: taskId
    };
    if (taskDetails.owner && (taskData.Owner = { id: taskDetails.owner }), taskDetails.dueDate) {
      let dueDate = new Date(taskDetails.dueDate);
      taskData.Due_Date = dueDate.toISOString().split("T")[0];
    }
    if (propsValue.enableRecurring && propsValue.recurringInfo) {
      if (!taskDetails.dueDate)
        throw new Error("Due Date is required when creating recurring tasks");
      let recurringInfo = propsValue.recurringInfo, rruleParts = [`FREQ=${recurringInfo.freq}`], startDate = new Date(taskDetails.dueDate);
      rruleParts.push(`DTSTART=${startDate.toISOString().split("T")[0]}`), recurringInfo.interval && rruleParts.push(`INTERVAL=${recurringInfo.interval}`), recurringInfo.count && rruleParts.push(`COUNT=${recurringInfo.count}`), recurringInfo.byday && rruleParts.push(`BYDAY=${recurringInfo.byday}`), recurringInfo.bymonthday && rruleParts.push(`BYMONTHDAY=${recurringInfo.bymonthday}`), recurringInfo.bysetpos && rruleParts.push(`BYSETPOS=${recurringInfo.bysetpos}`), recurringInfo.until && rruleParts.push(`UNTIL=${recurringInfo.until}`), taskData.Recurring_Activity = {
        RRULE: rruleParts.join(";") + ";"
      };
    }
    if (propsValue.enableReminder && propsValue.reminderInfo) {
      let reminderInfo = propsValue.reminderInfo, alarmValue = `ACTION=${reminderInfo.reminderAction || "EMAIL"}`, reminderTime = reminderInfo.reminderTime || "09:00";
      if (propsValue.recurringInfo && reminderInfo.reminderType === "days_before") {
        let daysBefore = reminderInfo.reminderDaysBefore || 1;
        alarmValue += `;TRIGGER=-P${daysBefore}D;TRIGGER_TIME=${reminderTime}`;
      } else if (propsValue.recurringInfo && reminderInfo.reminderType === "weeks_before") {
        let weeksBefore = reminderInfo.reminderWeeksBefore || 1;
        alarmValue += `;TRIGGER=-P${weeksBefore}W;TRIGGER_TIME=${reminderTime}`;
      } else if (reminderInfo.reminderDateTime) {
        let reminderDate = new Date(reminderInfo.reminderDateTime);
        alarmValue += `;TRIGGER=DATE-TIME:${reminderDate.toISOString()}`;
      }
      taskData.Remind_At = {
        ALARM: alarmValue
      };
    }
    if (propsValue.relatedTo && propsValue.relatedModule) {
      let relatedModule = {
        Contacts: "Contacts",
        Pipelines: "Deals",
        Companies: "Accounts"
      }[propsValue.relatedModule] || "Contacts";
      taskData.Related_To = { id: propsValue.relatedTo }, taskData.$related_module = relatedModule;
    }
    taskDetails.description && (taskData.Description = taskDetails.description), taskDetails.priority && (taskData.Priority = taskDetails.priority), taskDetails.status && (taskData.Status = taskDetails.status), taskDetails.tag && Array.isArray(taskDetails.tag) && taskDetails.tag.length > 0 && (taskData.Tag = taskDetails.tag.map((tagName) => ({ name: tagName })));
    let payload = { data: [taskData] };
    try {
      return (await biginApiService.updateTask(
        access_token,
        api_domain,
        payload
      )).data[0];
    } catch (error) {
      throw console.error("Error updating task:", error), new Error(error);
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/create-call.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var createCall = (0, import_pieces_framework18.createAction)({
  auth: biginAuth,
  name: "createCall",
  classification: "WRITE",
  displayName: "Create Call",
  description: "Creates a Call Log Entry Record",
  audience: "both",
  aiMetadata: { description: "Logs a call in Bigin CRM with a required start time, duration in minutes, and type (Outbound, Inbound, or Missed), plus optional subject, description, agenda, reminder, dialed number, owner, contact, and a related Pipeline or Company record. Use to record a phone interaction. Not idempotent: each call creates a new call log entry.", idempotent: !1 },
  props: {
    callStartTime: import_pieces_framework18.Property.DateTime({
      displayName: "Call Start Time",
      description: "Provide the start time of the call in ISO8601 format.",
      required: !0
    }),
    callDuration: import_pieces_framework18.Property.Number({
      displayName: "Call Duration (minutes)",
      description: "Provide the duration of the call in minutes (numeric). For example, 30 for a 30-minute call.",
      required: !0
    }),
    callType: import_pieces_framework18.Property.StaticDropdown({
      displayName: "Call Type",
      description: "Type of call",
      required: !0,
      options: {
        options: [
          { label: "Outbound", value: "Outbound" },
          { label: "Inbound", value: "Inbound" },
          { label: "Missed", value: "Missed" }
        ]
      }
    }),
    owner: usersDropdown,
    contactName: contactsDropdown,
    subject: import_pieces_framework18.Property.ShortText({
      displayName: "Subject",
      description: "Subject of the call",
      required: !1
    }),
    description: import_pieces_framework18.Property.LongText({
      displayName: "Description",
      description: "Description or notes about the call",
      required: !1
    }),
    callAgenda: import_pieces_framework18.Property.LongText({
      displayName: "Call Agenda",
      description: "Agenda or purpose of the call",
      required: !1
    }),
    reminder: import_pieces_framework18.Property.DateTime({
      displayName: "Reminder",
      description: "Reminder date and time for the call",
      required: !1
    }),
    dialledNumber: import_pieces_framework18.Property.ShortText({
      displayName: "Dialed Number",
      description: "Provide the number dialed for the call.",
      required: !1
    }),
    relatedModule: import_pieces_framework18.Property.StaticDropdown({
      displayName: "Related Module",
      description: "Select the type of entity the call is related to.",
      required: !1,
      defaultValue: "Pipelines",
      options: {
        options: [
          { label: "Pipelines", value: "Pipelines" },
          { label: "Companies", value: "Companies" }
        ]
      }
    }),
    relatedTo: import_pieces_framework18.Property.Dropdown({
      auth: biginAuth,
      displayName: "Related To",
      description: "Select the specific record the call is related to.",
      required: !1,
      refreshers: ["auth", "relatedModule"],
      defaultValue: {},
      options: async ({ auth, relatedModule }) => {
        if (!auth) return handleDropdownError("Please connect first");
        if (!relatedModule) return { options: [] };
        let { access_token, data } = auth, api_domain = data.api_domain, fetchFn = {
          Pipelines: () => biginApiService.fetchPipelinesRecords(access_token, api_domain),
          Companies: () => biginApiService.fetchCompanies(access_token, api_domain)
        }[relatedModule], response = await fetchFn();
        return {
          options: ((response == null ? void 0 : response.data) || []).map((item) => ({
            label: getSafeLabel(item),
            value: item.id
          }))
        };
      }
    }),
    tag: tagsDropdown("Calls")
  },
  async run({ auth, propsValue }) {
    try {
      let { access_token, data } = auth, api_domain = data.api_domain, payload = {
        Call_Start_Time: formatDateTime(propsValue.callStartTime),
        Call_Duration: Number(propsValue.callDuration),
        Call_Type: propsValue.callType
      };
      if (propsValue.subject && (payload.Subject = propsValue.subject), propsValue.description && (payload.Description = propsValue.description), propsValue.callAgenda && (payload.Call_Agenda = propsValue.callAgenda), propsValue.reminder && (payload.Reminder = formatDateTime(propsValue.reminder)), propsValue.dialledNumber && (payload.Dialled_Number = propsValue.dialledNumber), propsValue.owner && (payload.Owner = { id: propsValue.owner }), propsValue.contactName && (payload.Contact_Name = { id: propsValue.contactName }), propsValue.relatedTo && propsValue.relatedModule) {
        let relatedModuleMap = {
          Pipelines: "Deals",
          Companies: "Accounts"
        };
        payload.$related_module = relatedModuleMap[propsValue.relatedModule] || "Deals", payload.Related_To = { id: propsValue.relatedTo };
      }
      return propsValue.tag && (payload.Tag = propsValue.tag.map((t) => ({ name: t }))), {
        message: "Call log Entry created successfully",
        data: (await biginApiService.createCall(
          access_token,
          api_domain,
          { data: [payload] }
        )).data[0]
      };
    } catch (error) {
      throw console.error("Error creating call log entry:", error), new Error(
        error instanceof Error ? `Failed to create call log entry: ${error.message}` : "Failed to create call log entry due to an unknown error"
      );
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/create-event.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var createEvent = (0, import_pieces_framework19.createAction)({
  auth: biginAuth,
  name: "createEvent",
  classification: "WRITE",
  displayName: "Create Event",
  description: "Creates a new event in Bigin",
  audience: "both",
  aiMetadata: { description: "Creates a calendar event in Bigin CRM with a title, start and end date/time (end must be after start), plus optional all-day flag, venue, participants, description, and tags. Supports optional recurrence (daily/weekly/monthly/yearly RRULE) and reminders, and can be linked to a related Contact, Pipeline, or Company record. Use to schedule a meeting or appointment. Not idempotent: each call creates a new event.", idempotent: !1 },
  props: {
    eventTitle: import_pieces_framework19.Property.ShortText({
      displayName: "Event Title",
      description: "Provide the title or name of the event",
      required: !0
    }),
    owner: usersDropdown,
    startDateTime: import_pieces_framework19.Property.DateTime({
      displayName: "Start Date & Time",
      description: "Start date and time of the event",
      required: !0
    }),
    endDateTime: import_pieces_framework19.Property.DateTime({
      displayName: "End Date & Time",
      description: "End date and time of the event",
      required: !0
    }),
    allDay: import_pieces_framework19.Property.Checkbox({
      displayName: "All Day Event",
      description: "Mark this as an all-day event",
      required: !1
    }),
    enableRecurring: import_pieces_framework19.Property.Checkbox({
      displayName: "Make Event Recurring",
      description: "Make this event recurring",
      required: !1
    }),
    recurringInfo: import_pieces_framework19.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Recurring Info",
      refreshers: ["enableRecurring"],
      required: !1,
      props: (propsValue, ctx) => propsValue.enableRecurring ? {
        freq: import_pieces_framework19.Property.StaticDropdown({
          displayName: "Frequency",
          required: !0,
          options: {
            options: [
              { label: "Daily", value: "DAILY" },
              { label: "Weekly", value: "WEEKLY" },
              { label: "Monthly", value: "MONTHLY" },
              { label: "Yearly", value: "YEARLY" }
            ]
          }
        }),
        interval: import_pieces_framework19.Property.Number({
          displayName: "Interval",
          required: !0,
          description: "Indicates the time gap between each event. The INTERVAL value range from 1 to 99. For example, an INTERVAL of 2 for a WEEKLY recurring event means that there will be a two-week gap between each event.",
          defaultValue: 1
        }),
        count: import_pieces_framework19.Property.Number({
          displayName: "Count",
          required: !0,
          defaultValue: 1,
          description: "Indicates the number of events you want to create. THE COUNT value range from 1 to 99. For example, a COUNT of 3 creates three individual events."
        }),
        byday: import_pieces_framework19.Property.StaticDropdown({
          displayName: "By Day",
          required: !1,
          description: "Indicates the day of the week the event repeats. The possible values are SU, MO, TU, WE, TH, FR, or SA. This is applicable only for weekly, and monthly events.",
          options: {
            options: [
              { label: "Sunday", value: "SU" },
              { label: "Monday", value: "MO" },
              { label: "Tuesday", value: "TU" },
              { label: "Wednesday", value: "WE" },
              { label: "Thursday", value: "TH" },
              { label: "Friday", value: "FR" },
              { label: "Saturday", value: "SA" }
            ]
          }
        }),
        bymonthday: import_pieces_framework19.Property.Number({
          displayName: "By Month Day",
          required: !1,
          description: " Indicates the day of the month the event repeats. The BYMONTHDAY value range from 1 to 31. This is applicable only for weekly and monthly events."
        }),
        bysetpos: import_pieces_framework19.Property.StaticDropdown({
          displayName: "By Set Position",
          required: !1,
          description: "Indicates the week of the month the event repeats. The possible values are 1 for the first week of the month, 2 for the second week of the month, 3 the for third week of the month, 4 for the fourth week of the month, or -1 for the last week of the month. This is applicable only for weekly and monthly events.",
          options: {
            options: [
              { label: "First Week of the Month", value: "1" },
              { label: "Second Week of the Month", value: "2" },
              { label: "Third Week of the Month", value: "3" },
              { label: "Fourth Week of the Month", value: "4" },
              { label: "Last Week of the Month", value: "-1" }
            ]
          }
        }),
        until: import_pieces_framework19.Property.ShortText({
          displayName: "Until (YYYY-MM-DD)",
          description: "Date the recurrence ends. Format: YYYY-MM-DD",
          required: !1
        })
      } : {}
    }),
    enableReminder: import_pieces_framework19.Property.Checkbox({
      displayName: "Enable Reminder",
      description: "Enable reminder for this event",
      required: !1
    }),
    reminderInfo: import_pieces_framework19.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Reminder Information",
      refreshers: ["enableReminder"],
      required: !1,
      props: (propsValue, ctx) => propsValue.enableReminder ? {
        reminderList: import_pieces_framework19.Property.Array({
          displayName: "Reminder List",
          description: 'Add multiple reminders (e.g., "1 hour", "30 minutes")',
          required: !1,
          properties: {
            unit: import_pieces_framework19.Property.Number({
              displayName: "Time Value",
              description: "Number of time units (e.g., 1, 30)",
              required: !0
            }),
            period: import_pieces_framework19.Property.StaticDropdown({
              displayName: "Time Period",
              required: !0,
              options: {
                options: [
                  { label: "Days", value: "days" },
                  { label: "Weeks", value: "weeks" }
                ]
              }
            })
          }
        })
      } : {}
    }),
    venue: import_pieces_framework19.Property.ShortText({
      displayName: "Venue",
      description: "Location or venue of the event",
      required: !1
    }),
    relatedModule: import_pieces_framework19.Property.StaticDropdown({
      displayName: "Related Module",
      description: "Select the type of entity the event is related to",
      required: !1,
      defaultValue: "Contacts",
      options: {
        options: [
          { label: "Contacts", value: "Contacts" },
          { label: "Pipelines", value: "Pipelines" },
          { label: "Companies", value: "Companies" }
        ]
      }
    }),
    relatedTo: import_pieces_framework19.Property.Dropdown({
      auth: biginAuth,
      displayName: "Related To",
      description: "Select the specific record the event is related to",
      required: !1,
      refreshers: ["auth", "relatedModule"],
      defaultValue: {},
      options: async ({ auth, relatedModule }) => {
        if (!auth) return handleDropdownError("Please connect your account first");
        if (!relatedModule) return { options: [] };
        let { access_token, data } = auth, api_domain = data.api_domain, fetchFn = {
          Contacts: () => biginApiService.fetchContacts(access_token, api_domain),
          Pipelines: () => biginApiService.fetchPipelinesRecords(access_token, api_domain),
          Companies: () => biginApiService.fetchCompanies(access_token, api_domain)
        }[relatedModule || "Contacts"], response = await fetchFn();
        return {
          options: ((response == null ? void 0 : response.data) || []).map((item) => ({
            label: getSafeLabel(item),
            value: item.id
          }))
        };
      }
    }),
    participants: import_pieces_framework19.Property.Array({
      displayName: "Participants",
      description: "Add participants to the event",
      required: !1,
      properties: {
        type: import_pieces_framework19.Property.StaticDropdown({
          displayName: "Participant Type",
          required: !0,
          options: {
            options: [
              { label: "User", value: "user" },
              { label: "Email", value: "email" },
              { label: "Contact", value: "contact" }
            ]
          }
        }),
        participant: import_pieces_framework19.Property.ShortText({
          displayName: "Participant ID/Email",
          description: "User ID, email address, or contact ID based on type",
          required: !0
        })
      }
    }),
    description: import_pieces_framework19.Property.LongText({
      displayName: "Description",
      description: "Additional descriptions or notes related to the event",
      required: !1
    }),
    tag: tagsDropdown("Events")
  },
  async run({ auth, propsValue }) {
    let { access_token, data } = auth, api_domain = data.api_domain, startDate = new Date(propsValue.startDateTime);
    if (new Date(propsValue.endDateTime) <= startDate)
      throw new Error("End date/time must be after start date/time");
    let eventData = {
      Event_Title: propsValue.eventTitle,
      Start_DateTime: formatDateTime(propsValue.startDateTime),
      End_DateTime: formatDateTime(propsValue.endDateTime)
    };
    if (propsValue.owner && (eventData.Owner = { id: propsValue.owner }), propsValue.allDay && (eventData.All_day = propsValue.allDay), propsValue.enableRecurring && propsValue.recurringInfo) {
      let recurringInfo = propsValue.recurringInfo, rruleParts = [];
      if (!["DAILY", "WEEKLY", "MONTHLY", "YEARLY"].includes(recurringInfo.freq))
        throw new Error("Invalid recurrence frequency. Allowed values: DAILY, WEEKLY, MONTHLY, YEARLY");
      if (rruleParts.push(`FREQ=${recurringInfo.freq}`), recurringInfo.interval !== void 0) {
        let interval = Number(recurringInfo.interval);
        if (Number.isNaN(interval) || interval < 1 || interval > 99)
          throw new Error("Interval must be a number between 1 and 99");
        rruleParts.push(`INTERVAL=${recurringInfo.interval}`);
      }
      if (recurringInfo.count !== void 0) {
        let count = Number(recurringInfo.count);
        if (Number.isNaN(count) || count < 1 || count > 99)
          throw new Error("Count must be a number between 1 and 99");
        rruleParts.push(`COUNT=${recurringInfo.count}`);
      }
      if (recurringInfo.byday && rruleParts.push(`BYDAY=${recurringInfo.byday}`), recurringInfo.bymonthday !== void 0) {
        let bymonthday = Number(recurringInfo.bymonthday);
        if (Number.isNaN(bymonthday) || bymonthday < 1 || bymonthday > 31)
          throw new Error("BYMONTHDAY must be a number between 1 and 31");
        rruleParts.push(`BYMONTHDAY=${recurringInfo.bymonthday}`);
      }
      if (recurringInfo.bysetpos) {
        if (!["1", "2", "3", "4", "-1"].includes(String(recurringInfo.bysetpos)))
          throw new Error("BYSETPOS must be one of 1, 2, 3, 4, -1");
        rruleParts.push(`BYSETPOS=${recurringInfo.bysetpos}`);
      }
      if (recurringInfo.until) {
        let until = formatDateOnly(recurringInfo.until);
        rruleParts.push(`UNTIL=${until}`);
      }
      eventData.Recurring_Activity = {
        RRULE: rruleParts.join(";") + ";"
      };
    }
    if (propsValue.enableReminder && propsValue.reminderInfo) {
      let reminderList = propsValue.reminderInfo.reminderList;
      eventData.Remind_At = reminderList.map((reminder) => ({
        unit: reminder.unit,
        period: reminder.period
      }));
    }
    if (propsValue.venue && (eventData.Venue = propsValue.venue), propsValue.relatedTo && propsValue.relatedModule) {
      let relatedModule = {
        Contacts: "Contacts",
        Pipelines: "Deals",
        Companies: "Accounts"
      }[propsValue.relatedModule];
      eventData.Related_To = { id: propsValue.relatedTo }, eventData.$related_module = relatedModule;
    }
    propsValue.participants && Array.isArray(propsValue.participants) && (eventData.Participants = propsValue.participants.map(
      (participant) => ({
        type: participant.type,
        participant: participant.participant
      })
    )), propsValue.description && (eventData.Description = propsValue.description), propsValue.tag && Array.isArray(propsValue.tag) && propsValue.tag.length > 0 && (eventData.Tag = propsValue.tag.map((tagName) => ({ name: tagName })));
    let payload = { data: [eventData] };
    try {
      return (await biginApiService.createEvent(
        access_token,
        api_domain,
        payload
      )).data[0];
    } catch (error) {
      throw console.error("Error creating event:", error), new Error(
        error instanceof Error ? `Failed to create event: ${error.message}` : "Failed to create event due to an unknown error"
      );
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/update-event.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var updateEvent = (0, import_pieces_framework20.createAction)({
  auth: biginAuth,
  name: "updateEvent",
  classification: "WRITE",
  displayName: "Update Event",
  description: "Updates an existing event in Bigin",
  audience: "both",
  aiMetadata: { description: "Updates an existing calendar event in Bigin CRM, identified by selecting the event; its current fields are prepopulated and any you set are overwritten (title, start/end time, all-day, venue, participants, owner, tags), with optional recurrence and reminder settings. Use to modify an event that already exists. Idempotent: re-sending the same field values leaves the record in the same state.", idempotent: !0 },
  props: {
    eventId: import_pieces_framework20.Property.Dropdown({
      auth: biginAuth,
      displayName: "Select Event",
      description: "Choose the event to update",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return handleDropdownError("Please connect your account first");
        let { access_token, data } = auth, api_domain = data.api_domain;
        try {
          let response = await biginApiService.fetchEvents(
            access_token,
            api_domain
          );
          return {
            options: ((response == null ? void 0 : response.data) || []).map((event) => ({
              label: `${event.Event_Title} - ${event.Start_DateTime}`,
              value: JSON.stringify(event)
            }))
          };
        } catch (error) {
          return handleDropdownError("Failed to fetch events");
        }
      }
    }),
    eventFields: import_pieces_framework20.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Event Fields",
      refreshers: ["auth", "eventId"],
      required: !1,
      props: async ({ eventId, auth }) => {
        var _a, _b;
        if (!eventId) return {};
        let event = JSON.parse(eventId), { access_token, data } = auth, api_domain = data.api_domain, fieldsResp = await biginApiService.fetchModuleFields(
          access_token,
          api_domain,
          "Events"
        ), props = {};
        for (let f of fieldsResp.fields || []) {
          let apiName = f.api_name;
          if (f.read_only || f.field_read_only || !f.view_type || f.view_type.edit !== !0 || apiName === "Tag" || apiName === "id" || apiName === "Owner" || apiName === "Related_To" || apiName === "$related_module" || apiName === "Recurring_Activity" || apiName === "Remind_At" || apiName === "Participants")
            continue;
          let defaultValue = (_a = event[apiName]) != null ? _a : void 0;
          switch ((_b = f.data_type) == null ? void 0 : _b.toLowerCase()) {
            case "picklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework20.Property.StaticDropdown({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue,
                options: { options }
              });
              break;
            }
            case "multiselectpicklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework20.Property.StaticMultiSelectDropdown({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue,
                options: { options }
              });
              break;
            }
            case "boolean": {
              props[apiName] = import_pieces_framework20.Property.Checkbox({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue: !!defaultValue
              });
              break;
            }
            case "date": {
              props[apiName] = import_pieces_framework20.Property.ShortText({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || "Format: YYYY-MM-DD",
                required: !1,
                defaultValue
              });
              break;
            }
            case "datetime": {
              props[apiName] = import_pieces_framework20.Property.DateTime({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || "Format: ISO 8601 (YYYY-MM-DDTHH:mm:ss\xB1HH:mm)",
                required: !1,
                defaultValue
              });
              break;
            }
            case "integer":
            case "long":
            case "double":
            case "decimal":
            case "currency":
            case "percent": {
              props[apiName] = import_pieces_framework20.Property.Number({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue
              });
              break;
            }
            default: {
              if (apiName === "Description") {
                props[apiName] = import_pieces_framework20.Property.LongText({
                  displayName: "Description",
                  required: !1,
                  defaultValue
                });
                break;
              }
              props[apiName] = import_pieces_framework20.Property.ShortText({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue: typeof defaultValue == "string" ? defaultValue : void 0
              });
            }
          }
        }
        return props.Participants = import_pieces_framework20.Property.Array({
          displayName: "Participants",
          description: "Add participants to the event",
          required: !1,
          defaultValue: event.Participants || [],
          properties: {
            type: import_pieces_framework20.Property.StaticDropdown({
              displayName: "Participant Type",
              required: !0,
              options: {
                options: [
                  { label: "User", value: "user" },
                  { label: "Email", value: "email" },
                  { label: "Contact", value: "contact" }
                ]
              }
            }),
            participant: import_pieces_framework20.Property.ShortText({
              displayName: "Participant ID/Email",
              description: "User ID, email address, or contact ID based on type",
              required: !0
            })
          }
        }), props;
      }
    }),
    enableRecurring: import_pieces_framework20.Property.Checkbox({
      displayName: "Make Event Recurring",
      description: "Make this event recurring",
      required: !1
    }),
    recurringInfo: import_pieces_framework20.Property.DynamicProperties({
      displayName: "Recurring Info",
      refreshers: ["enableRecurring"],
      required: !1,
      auth: biginAuth,
      props: (propsValue, ctx) => propsValue.enableRecurring ? {
        freq: import_pieces_framework20.Property.StaticDropdown({
          displayName: "Frequency",
          required: !0,
          options: {
            options: [
              { label: "Daily", value: "DAILY" },
              { label: "Weekly", value: "WEEKLY" },
              { label: "Monthly", value: "MONTHLY" }
            ]
          }
        }),
        interval: import_pieces_framework20.Property.Number({
          displayName: "Interval",
          required: !0,
          description: "Indicates the time gap between each event. The INTERVAL value range from 1 to 99. For example, an INTERVAL of 2 for a WEEKLY recurring event means that there will be a two-week gap between each event.",
          defaultValue: 1
        }),
        count: import_pieces_framework20.Property.Number({
          displayName: "Count",
          required: !0,
          defaultValue: 1,
          description: "Indicates the number of events you want to create. THE COUNT value range from 1 to 99. For example, a COUNT of 3 creates three individual events."
        }),
        byday: import_pieces_framework20.Property.StaticDropdown({
          displayName: "By Day",
          required: !1,
          description: "Indicates the day of the week the event repeats. The possible values are SU, MO, TU, WE, TH, FR, or SA. This is applicable only for weekly, and monthly events.",
          options: {
            options: [
              { label: "Sunday", value: "SU" },
              { label: "Monday", value: "MO" },
              { label: "Tuesday", value: "TU" },
              { label: "Wednesday", value: "WE" },
              { label: "Thursday", value: "TH" },
              { label: "Friday", value: "FR" },
              { label: "Saturday", value: "SA" }
            ]
          }
        }),
        bymonthday: import_pieces_framework20.Property.Number({
          displayName: "By Month Day",
          required: !1,
          description: " Indicates the day of the month the event repeats. The BYMONTHDAY value range from 1 to 31. This is applicable only for weekly and monthly events."
        }),
        bysetpos: import_pieces_framework20.Property.StaticDropdown({
          displayName: "By Set Position",
          required: !1,
          description: "Indicates the week of the month the event repeats. The possible values are 1 for the first week of the month, 2 for the second week of the month, 3 the for third week of the month, 4 for the fourth week of the month, or -1 for the last week of the month. This is applicable only for weekly and monthly events.",
          options: {
            options: [
              { label: "First Week of the Month", value: "1" },
              { label: "Second Week of the Month", value: "2" },
              { label: "Third Week of the Month", value: "3" },
              { label: "Fourth Week of the Month", value: "4" },
              { label: "Last Week of the Month", value: "-1" }
            ]
          }
        })
      } : {}
    }),
    enableReminder: import_pieces_framework20.Property.Checkbox({
      displayName: "Enable Reminder",
      description: "Enable reminder for this event",
      required: !1
    }),
    reminderInfo: import_pieces_framework20.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Reminder Information",
      refreshers: ["enableReminder"],
      required: !1,
      props: (propsValue, ctx) => propsValue.enableReminder ? {
        reminderList: import_pieces_framework20.Property.Array({
          displayName: "Reminder List",
          description: 'Add multiple reminders (e.g., "1 hour", "30 minutes")',
          required: !1,
          properties: {
            unit: import_pieces_framework20.Property.Number({
              displayName: "Time Value",
              description: "Number of time units (e.g., 1, 30)",
              required: !0
            }),
            period: import_pieces_framework20.Property.StaticDropdown({
              displayName: "Time Period",
              required: !0,
              options: {
                options: [
                  { label: "Days", value: "days" },
                  { label: "Weeks", value: "weeks" }
                ]
              }
            })
          }
        })
      } : {}
    }),
    owner: usersDropdown,
    relatedModule: import_pieces_framework20.Property.Dropdown({
      auth: biginAuth,
      displayName: "Related Module",
      description: "Select the type of entity the event is related to",
      required: !1,
      refreshers: ["auth"],
      options: async () => ({
        options: [
          { label: "Contacts", value: "Contacts" },
          { label: "Pipelines", value: "Pipelines" },
          { label: "Companies", value: "Companies" }
        ]
      })
    }),
    relatedTo: import_pieces_framework20.Property.Dropdown({
      auth: biginAuth,
      displayName: "Related To",
      description: "Select the specific record the event is related to",
      required: !1,
      refreshers: ["auth", "relatedModule"],
      options: async ({ auth, relatedModule }) => {
        if (!auth)
          return handleDropdownError("Please connect your account first");
        if (!relatedModule) return { options: [] };
        let { access_token, api_domain } = auth, fetchFn = {
          Contacts: () => biginApiService.fetchContacts(access_token, api_domain),
          Pipelines: () => biginApiService.fetchPipelinesRecords(access_token, api_domain),
          Companies: () => biginApiService.fetchCompanies(access_token, api_domain)
        }[relatedModule || "Contacts"], response = await fetchFn();
        return {
          options: ((response == null ? void 0 : response.data) || []).map((item) => ({
            label: getSafeLabel(item),
            value: item.id
          }))
        };
      }
    }),
    tag: tagsDropdown("Events")
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    let { access_token, api_domain } = auth, eventData = {
      id: JSON.parse(propsValue.eventId).id
    }, eventFields = propsValue.eventFields;
    if (propsValue.owner && (eventData.Owner = { id: propsValue.owner }), eventFields && typeof eventFields == "object") {
      for (let [apiName, value] of Object.entries(eventFields))
        if (!(value == null || typeof value == "string" && value.trim() === "" || Array.isArray(value) && value.length === 0)) {
          if (apiName === "Participants") {
            eventData.Participants = value.map((p) => ({
              type: p.type,
              participant: p.participant
            }));
            continue;
          }
          if (apiName === "Start_DateTime" || apiName === "End_DateTime") {
            eventData[apiName] = formatDateTime(value);
            continue;
          }
          if (apiName === "All_day") {
            eventData.All_day = value;
            continue;
          }
          eventData[apiName] = value;
        }
    }
    if (propsValue.enableRecurring) {
      let rruleParts = [], { recurringInfo } = propsValue;
      recurringInfo.freq && rruleParts.push(`FREQ=${recurringInfo.freq}`), recurringInfo.interval && rruleParts.push(`INTERVAL=${recurringInfo.interval}`), recurringInfo.count && rruleParts.push(`COUNT=${recurringInfo.count}`), recurringInfo.byday && rruleParts.push(`BYDAY=${recurringInfo.byday}`), recurringInfo.bymonthday && rruleParts.push(`BYMONTHDAY=${recurringInfo.bymonthday}`), recurringInfo.bysetpos && rruleParts.push(`BYSETPOS=${recurringInfo.bysetpos}`), rruleParts.length > 0 && (eventData.Recurring_Activity = {
        RRULE: rruleParts.join(";") + ";"
      });
    }
    if (propsValue.enableReminder && ((_a = propsValue.reminderInfo) != null && _a.reminderList)) {
      let reminderList = (_b = propsValue.reminderInfo) == null ? void 0 : _b.reminderList;
      eventData.Remind_At = reminderList.map((reminder) => ({
        unit: reminder.unit,
        period: reminder.period
      }));
    }
    if (eventFields != null && eventFields.venue && (eventData.Venue = eventFields.venue), propsValue.relatedTo && propsValue.relatedModule) {
      let relatedModule = {
        Contacts: "Contacts",
        Pipelines: "Deals",
        Companies: "Accounts"
      }[propsValue.relatedModule];
      eventData.Related_To = { id: propsValue.relatedTo }, eventData.$related_module = relatedModule;
    }
    if (propsValue.tag && Array.isArray(propsValue.tag) && propsValue.tag.length > 0 && (eventData.Tag = propsValue.tag.map((tagName) => ({ name: tagName }))), Object.keys(eventData).length === 0)
      throw new Error("No fields to update. Please modify at least one field.");
    let payload = { data: [eventData] };
    try {
      return (await biginApiService.updateEvent(
        access_token,
        api_domain,
        payload
      )).data[0];
    } catch (error) {
      throw console.error("Error updating event:", error), new Error(
        error instanceof Error ? `Failed to update event: ${error.message}` : "Failed to update event due to an unknown error"
      );
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/create-pipeline-record.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var createPipelineRecord = (0, import_pieces_framework21.createAction)({
  auth: biginAuth,
  name: "createPipeline",
  classification: "WRITE",
  displayName: "Create Pipeline",
  description: "Creates a new pipeline record in Bigin",
  audience: "both",
  aiMetadata: { description: "Creates a new pipeline record (deal) in Bigin CRM with a deal name, pipeline, sub-pipeline, stage, and closing date, plus optional amount, owner, company, contact, secondary contacts, associated products, tags, and module-defined fields. Use to open a new sales opportunity. Not idempotent: each call creates a new deal.", idempotent: !1 },
  props: {
    dealName: import_pieces_framework21.Property.ShortText({
      displayName: "Deal Name",
      description: "Provide the name for the pipeline record (deal)",
      required: !0
    }),
    pipeline: layoutsDropdown(
      "Pipelines",
      "Provide the Team Pipeline to which the pipeline record (deal) belongs",
      "Pipelines"
    ),
    subPipeline: SubPipelineorStageDropdown(
      "Sub Pipeline",
      "Pick one of the configured sub-pipelines",
      "Sub Pipeline"
    ),
    stage: SubPipelineorStageDropdown(
      "Stage",
      "Provide the current stage of the pipeline record (deal) within the Sub-Pipeline",
      "Stage"
    ),
    amount: import_pieces_framework21.Property.Number({
      displayName: "Amount",
      description: "The amount of the pipeline record (deal)",
      required: !1
    }),
    secondaryContacts: multiContactsDropdown,
    closingDate: import_pieces_framework21.Property.DateTime({
      displayName: "Closing Date",
      description: "Provide the expected or actual closing date of the pipeline record (deal) in YYYY-MM-DD format",
      required: !0
    }),
    owner: usersDropdown,
    accountName: companyDropdown,
    contactName: contactsDropdown,
    associatedProducts: productsDropdown,
    tag: tagsDropdown("Pipelines"),
    additionalFields: import_pieces_framework21.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Additional Fields",
      description: "Optional fields from the Pipelines module",
      refreshers: ["auth"],
      required: !1,
      props: async ({ auth }) => {
        var _a;
        if (!auth) return {};
        let { access_token, api_domain } = auth, fieldsResp = await biginApiService.fetchModuleFields(
          access_token,
          api_domain,
          "Pipelines"
        ), props = {};
        for (let f of fieldsResp.fields || []) {
          let apiName = f.api_name;
          if (["Deal_Name", "Sub_Pipeline", "Stage", "Owner", "Account_Name", "Contact_Name", "Tag", "Pipeline", "Associated_Products", "Secondary_Contacts", "Closing_Date", "id"].includes(apiName) || f.read_only || f.field_read_only || !f.view_type || f.view_type.create !== !0) continue;
          let display = f.display_label || f.field_label || apiName;
          switch ((_a = f.data_type) == null ? void 0 : _a.toLowerCase()) {
            case "picklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework21.Property.StaticDropdown({
                displayName: display,
                required: !1,
                options: { options }
              });
              break;
            }
            case "multiselectpicklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework21.Property.StaticMultiSelectDropdown({
                displayName: display,
                required: !1,
                options: { options }
              });
              break;
            }
            case "boolean": {
              props[apiName] = import_pieces_framework21.Property.Checkbox({
                displayName: display,
                required: !1
              });
              break;
            }
            case "date": {
              props[apiName] = import_pieces_framework21.Property.ShortText({
                displayName: display,
                required: !1
              });
              break;
            }
            case "datetime": {
              props[apiName] = import_pieces_framework21.Property.DateTime({
                displayName: display,
                required: !1
              });
              break;
            }
            case "integer":
            case "long":
            case "double":
            case "decimal":
            case "currency":
            case "percent": {
              props[apiName] = import_pieces_framework21.Property.Number({
                displayName: display,
                required: !1
              });
              break;
            }
            default:
              props[apiName] = import_pieces_framework21.Property.ShortText({
                displayName: display,
                required: !1
              });
          }
        }
        return props;
      }
    })
  },
  async run({ propsValue, auth }) {
    var _a;
    let payload = {
      Deal_Name: propsValue.dealName,
      Sub_Pipeline: propsValue.subPipeline,
      Stage: propsValue.stage
    };
    if (propsValue.owner && (payload.Owner = { id: propsValue.owner }), propsValue.accountName && (payload.Account_Name = { id: propsValue.accountName }), propsValue.contactName && (payload.Contact_Name = { id: propsValue.contactName }), propsValue.amount && (payload.Amount = propsValue.amount), propsValue.closingDate ? payload.Closing_Date = formatDateOnly(propsValue.closingDate) : payload.Closing_Date = formatDateOnly(/* @__PURE__ */ new Date()), propsValue.pipeline) {
      let pipeline = JSON.parse(propsValue.pipeline);
      payload.Pipeline = { id: pipeline.id, name: pipeline.name };
    }
    if (propsValue.associatedProducts && (payload.Associated_Products = propsValue.associatedProducts.map(
      (product) => {
        let p = JSON.parse(product);
        return { id: p.id, name: p.name };
      }
    )), propsValue.tag && (payload.Tag = propsValue.tag.map((t) => ({ name: t }))), propsValue.additionalFields && typeof propsValue.additionalFields == "object")
      for (let [apiName, value] of Object.entries(propsValue.additionalFields))
        value == null || typeof value == "string" && value.trim() === "" || Array.isArray(value) && value.length === 0 || ["Deal_Name", "Sub_Pipeline", "Stage", "Owner", "Account_Name", "Contact_Name", "Tag", "Pipeline", "Associated_Products", "Secondary_Contacts", "Closing_Date"].includes(apiName) || (payload[apiName] = value);
    try {
      let { access_token, data } = auth, api_domain = data.api_domain, created = (await biginApiService.createPipelineRecord(
        access_token,
        api_domain,
        { data: [payload] }
      )).data[0];
      if (propsValue.secondaryContacts && Array.isArray(propsValue.secondaryContacts) && ((_a = created == null ? void 0 : created.details) != null && _a.id)) {
        let secondaries = propsValue.secondaryContacts.map((value) => String(value)).filter((id) => id !== propsValue.contactName);
        secondaries.length > 0 && await biginApiService.updatePipelineRecord(
          access_token,
          api_domain,
          {
            data: [
              {
                id: created.details.id,
                Secondary_Contacts: secondaries.map((id) => ({ id }))
              }
            ]
          }
        );
      }
      return created;
    } catch (error) {
      throw console.error("Error creating pipeline:", error), new Error(
        error instanceof Error ? `Failed to create pipeline: ${error.message}` : "Failed to create pipeline due to an unknown error"
      );
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/update-pipeline-record.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var updatePipelineRecord = (0, import_pieces_framework22.createAction)({
  auth: biginAuth,
  name: "updatePipeline",
  classification: "WRITE",
  displayName: "Update Pipeline",
  description: "updates a pipeline record in Bigin",
  audience: "both",
  aiMetadata: { description: "Updates an existing pipeline record (deal) in Bigin CRM, identified by selecting the record; any fields you set are overwritten (deal name, sub-pipeline, stage, amount, closing date, owner, company, contact, secondary contacts, associated products, tags). Use to advance a deal stage or revise its details. Idempotent: re-sending the same field values leaves the record in the same state.", idempotent: !0 },
  props: {
    pipelineRecordId: pipelineRecordsDropdown,
    pipelineDetails: import_pieces_framework22.Property.DynamicProperties({
      auth: biginAuth,
      displayName: "Pipeline Details",
      description: "These fields will be prepopulated with pipeline data",
      refreshers: ["pipelineRecordId", "auth"],
      required: !0,
      props: async ({ pipelineRecordId, auth }) => {
        var _a, _b;
        let pipelineData = JSON.parse(pipelineRecordId);
        if (!pipelineData.id)
          throw new Error("Pipeline ID is required to update the record");
        let { access_token, api_domain } = auth, fieldsResp = await biginApiService.fetchModuleFields(
          access_token,
          api_domain,
          "Pipelines"
        ), props = {};
        for (let f of fieldsResp.fields || []) {
          let apiName = f.api_name;
          if (f.read_only || f.field_read_only || !f.view_type || f.view_type.edit !== !0 || apiName === "Tag" || apiName === "id" || apiName === "Secondary_Contacts") continue;
          let defaultValue = (_a = pipelineData[apiName]) != null ? _a : void 0;
          switch ((_b = f.data_type) == null ? void 0 : _b.toLowerCase()) {
            case "picklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework22.Property.StaticDropdown({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue,
                options: { options }
              });
              break;
            }
            case "multiselectpicklist": {
              let options = (f.pick_list_values || []).map((pl) => ({
                label: pl.display_value,
                value: pl.actual_value
              }));
              props[apiName] = import_pieces_framework22.Property.StaticMultiSelectDropdown({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue,
                options: { options }
              });
              break;
            }
            case "boolean": {
              props[apiName] = import_pieces_framework22.Property.Checkbox({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue: !!defaultValue
              });
              break;
            }
            case "date": {
              props[apiName] = import_pieces_framework22.Property.ShortText({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || "Format: YYYY-MM-DD",
                required: !1,
                defaultValue
              });
              break;
            }
            case "datetime": {
              props[apiName] = import_pieces_framework22.Property.DateTime({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || "Format: ISO 8601 (YYYY-MM-DDTHH:mm:ss\xB1HH:mm)",
                required: !1,
                defaultValue
              });
              break;
            }
            case "integer":
            case "long":
            case "double":
            case "decimal":
            case "currency":
            case "percent": {
              props[apiName] = import_pieces_framework22.Property.Number({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue
              });
              break;
            }
            default:
              props[apiName] = import_pieces_framework22.Property.ShortText({
                displayName: f.display_label || f.field_label || apiName,
                description: f.tooltip || void 0,
                required: !1,
                defaultValue: typeof defaultValue == "string" ? defaultValue : void 0
              });
          }
        }
        return props;
      }
    }),
    pipeline: layoutsDropdown(
      "Pipelines",
      "Provide the Team Pipeline to which the pipeline record (deal) belongs",
      "Pipelines"
    ),
    subPipeline: SubPipelineorStageDropdown(
      "Sub Pipeline",
      "Pick one of the configured sub-pipelines",
      "Sub Pipeline"
    ),
    stage: SubPipelineorStageDropdown(
      "Stage",
      "Provide the current stage of the pipeline record (deal) within the Sub-Pipeline",
      "Stage"
    ),
    owner: usersDropdown,
    accountName: companyDropdown,
    contactName: contactsDropdown,
    secondaryContacts: multiContactsDropdown,
    associatedProducts: productsDropdown,
    tag: tagsDropdown("Pipelines")
  },
  async run({ propsValue, auth }) {
    let pipelineData = JSON.parse(propsValue.pipelineRecordId), payload = {
      id: pipelineData.id
    };
    if (propsValue.owner && (payload.Owner = propsValue.owner), propsValue.accountName && (payload.Account_Name = { id: propsValue.accountName }), propsValue.contactName && (payload.Contact_Name = { id: propsValue.contactName }), propsValue.subPipeline && (payload["Sub Pipeline"] = propsValue.subPipeline), propsValue.stage && (payload.Stage = propsValue.stage), propsValue.pipelineDetails.dealName && (payload.Deal_Name = propsValue.pipelineDetails.dealName), propsValue.pipelineDetails.amount && (payload.Amount = propsValue.pipelineDetails.amount), propsValue.pipelineDetails.closingDate && (payload.Closing_Date = formatDateOnly(
      propsValue.pipelineDetails.closingDate
    )), propsValue.pipeline) {
      let pipeline = JSON.parse(propsValue.pipeline);
      payload.Pipeline = { id: pipeline.id, name: pipeline.name };
    }
    propsValue.associatedProducts && (payload.Associated_Products = propsValue.associatedProducts.map(
      (product) => {
        let p = JSON.parse(product);
        return { id: p.id, name: p.name };
      }
    )), propsValue.tag && (payload.Tag = propsValue.tag.map((t) => ({ name: t })));
    let { access_token, api_domain } = auth;
    try {
      let response = await biginApiService.updatePipelineRecord(
        access_token,
        api_domain,
        { data: [payload] }
      );
      if (propsValue.secondaryContacts && Array.isArray(propsValue.secondaryContacts)) {
        let secondaries = propsValue.secondaryContacts.map((v) => String(v)).filter((id) => id !== propsValue.contactName);
        secondaries.length > 0 && await biginApiService.updatePipelineRecord(
          access_token,
          api_domain,
          {
            data: [
              {
                id: pipelineData.id,
                Secondary_Contacts: secondaries.map((id) => ({ id }))
              }
            ]
          }
        );
      }
      return {
        message: "Pipeline record updated successfully",
        pipelineRecord: response.data[0]
      };
    } catch (error) {
      throw console.error("Error updating pipeline record:", error), new Error(error);
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/search-pipeline-records.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var searchPipelineRecord = (0, import_pieces_framework23.createAction)({
  auth: biginAuth,
  name: "searchPipelineRecord",
  classification: "SEARCH",
  displayName: "Search Pipeline Record",
  description: "Searches deals by name via criteria or word",
  audience: "both",
  aiMetadata: { description: "Searches pipeline records (deals) in Bigin CRM and returns matches. Choose between Criteria mode (matches the Deal Name with equals or starts-with) and Word mode (a free-text word search across the module). Use to find a deal by name before referencing or updating it. Idempotent: read-only, repeating the search returns the same matches.", idempotent: !0 },
  props: {
    mode: import_pieces_framework23.Property.StaticDropdown({
      displayName: "Search Mode",
      required: !0,
      defaultValue: "criteria",
      options: {
        options: [
          { label: "Criteria (Deal Name)", value: "criteria" },
          { label: "Word", value: "word" }
        ]
      }
    }),
    dealName: import_pieces_framework23.Property.ShortText({
      displayName: "Search Term",
      description: "Deal Name (criteria) or word",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { dealName, mode } = propsValue, { access_token, data } = auth, api_domain = data.api_domain;
    try {
      return {
        message: "Pipeline record search completed successfully",
        data: (await biginApiService.searchRecords(
          access_token,
          api_domain,
          "Pipelines",
          mode === "word" ? { key: "word", value: dealName } : { key: "criteria", value: `(Deal_Name:equals:${dealName})OR(Deal_Name:starts_with:${dealName})` }
        )).data
      };
    } catch (error) {
      throw new Error(`Error searching pipeline record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/search-company-record.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework");
var searchCompanyRecord = (0, import_pieces_framework24.createAction)({
  auth: biginAuth,
  name: "searchCompanyRecord",
  classification: "SEARCH",
  displayName: "Search Company Record",
  description: "Searches companies by full name (criteria) or word",
  audience: "both",
  aiMetadata: { description: "Searches company (account) records in Bigin CRM and returns matches. Choose between Criteria mode (matches the Account Name with equals or starts-with) and Word mode (a free-text word search across the module). Use to find a company by name before referencing or updating it. Idempotent: read-only, repeating the search returns the same matches.", idempotent: !0 },
  props: {
    mode: import_pieces_framework24.Property.StaticDropdown({
      displayName: "Search Mode",
      required: !0,
      defaultValue: "criteria",
      options: {
        options: [
          { label: "Criteria (full name)", value: "criteria" },
          { label: "Word", value: "word" }
        ]
      }
    }),
    companyName: import_pieces_framework24.Property.ShortText({
      displayName: "Search Term",
      description: "Company full name (criteria) or word",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { companyName, mode } = propsValue, { access_token, data } = auth, api_domain = data.api_domain;
    try {
      return {
        message: "Company record search completed successfully",
        data: (await biginApiService.searchRecords(
          access_token,
          api_domain,
          "Accounts",
          mode === "word" ? { key: "word", value: companyName } : {
            key: "criteria",
            value: `(Account_Name:equals:${companyName})OR(Account_Name:starts_with:${companyName})`
          }
        )).data
      };
    } catch (error) {
      throw new Error(`Error searching company record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/search-contact-record.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework");
var searchContactRecord = (0, import_pieces_framework25.createAction)({
  auth: biginAuth,
  name: "searchContactRecord",
  classification: "SEARCH",
  displayName: "Search Contact Record",
  description: "Searches contacts by criteria, email, phone, or word",
  audience: "both",
  aiMetadata: { description: "Searches contact (person) records in Bigin CRM and returns matches. The search mode selects what the term is matched against: Criteria (name/email/mobile with equals or starts-with), a dedicated Email lookup, a dedicated Phone lookup, or a free-text Word search. Use to find a person by name, email, or phone before referencing or updating them. Idempotent: read-only, repeating the search returns the same matches.", idempotent: !0 },
  props: {
    mode: import_pieces_framework25.Property.StaticDropdown({
      displayName: "Search Mode",
      description: "Choose how to search Contacts",
      required: !0,
      defaultValue: "criteria",
      options: {
        options: [
          { label: "Criteria (name/email/mobile)", value: "criteria" },
          { label: "Email", value: "email" },
          { label: "Phone", value: "phone" },
          { label: "Word", value: "word" }
        ]
      }
    }),
    searchTerm: import_pieces_framework25.Property.ShortText({
      displayName: "Search Term",
      description: "Text, email, phone, or word based on the selected mode",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { searchTerm, mode } = propsValue, { access_token, data } = auth, api_domain = data.api_domain, queryKey = "criteria", queryValue = "";
    mode === "email" ? (queryKey = "email", queryValue = searchTerm) : mode === "phone" ? (queryKey = "phone", queryValue = searchTerm) : mode === "word" ? (queryKey = "word", queryValue = searchTerm) : queryValue = ["First_Name", "Last_Name", "Email", "Mobile"].flatMap((key) => [
      `${key}:equals:${searchTerm}`,
      `${key}:starts_with:${searchTerm}`
    ]).map((condition) => `(${condition})`).join("OR");
    try {
      return {
        message: "Contact record search completed successfully",
        data: (await biginApiService.searchRecords(
          access_token,
          api_domain,
          "Contacts",
          {
            key: queryKey,
            value: queryValue
          }
        )).data
      };
    } catch (error) {
      throw new Error(`Error searching contact record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/search-product.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework");
var searchProductRecord = (0, import_pieces_framework26.createAction)({
  auth: biginAuth,
  name: "searchProductRecord",
  classification: "SEARCH",
  displayName: "Search Product Record",
  description: "Searches products by name/code via criteria or word",
  audience: "both",
  aiMetadata: { description: "Searches product records in Bigin CRM and returns matches. Choose between Criteria mode (matches Product Name or Product Code with equals or starts-with) and Word mode (a free-text word search across the module). Use to find a product by name or code before associating it with a deal. Idempotent: read-only, repeating the search returns the same matches.", idempotent: !0 },
  props: {
    mode: import_pieces_framework26.Property.StaticDropdown({
      displayName: "Search Mode",
      required: !0,
      defaultValue: "criteria",
      options: {
        options: [
          { label: "Criteria (name/code)", value: "criteria" },
          { label: "Word", value: "word" }
        ]
      }
    }),
    searchTerm: import_pieces_framework26.Property.ShortText({
      displayName: "Search Term",
      description: "Product name/code (criteria) or word",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { searchTerm, mode } = propsValue, { access_token, data } = auth, api_domain = data.api_domain, criteriaValue = ["Product_Name", "Product_Code"].flatMap((key) => [
      `${key}:equals:${searchTerm}`,
      `${key}:starts_with:${searchTerm}`
    ]).map((condition) => `(${condition})`).join("OR");
    try {
      return {
        message: "Product record search completed successfully",
        data: (await biginApiService.searchRecords(
          access_token,
          api_domain,
          "Products",
          mode === "word" ? { key: "word", value: searchTerm } : { key: "criteria", value: criteriaValue }
        )).data
      };
    } catch (error) {
      throw new Error(`Error searching product record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/lib/actions/search-user.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework");
var searchUser = (0, import_pieces_framework27.createAction)({
  auth: biginAuth,
  name: "searchUser",
  classification: "SEARCH",
  displayName: "Search User",
  description: "Locate users by email.",
  audience: "both",
  aiMetadata: { description: "Finds Bigin CRM org users whose email contains the given term (case-insensitive substring match), filtering client-side over the user list; an optional user-type filter (e.g. active, admin, current user) and pagination narrow the fetched set. Use to resolve a user id for assigning records as owner. Idempotent: read-only, repeating the search returns the same matches.", idempotent: !0 },
  props: {
    email: import_pieces_framework27.Property.ShortText({
      displayName: "Email",
      description: "User email address (full or partial, case-insensitive match)",
      required: !0
    }),
    type: import_pieces_framework27.Property.StaticDropdown({
      displayName: "User Type (optional)",
      required: !1,
      options: {
        options: [
          { label: "All Users", value: "AllUsers" },
          { label: "Active Users", value: "ActiveUsers" },
          { label: "Deactive Users", value: "DeactiveUsers" },
          { label: "Confirmed Users", value: "ConfirmedUsers" },
          { label: "Not Confirmed Users", value: "NotConfirmedUsers" },
          { label: "Deleted Users", value: "DeletedUsers" },
          { label: "Active Confirmed Users", value: "ActiveConfirmedUsers" },
          { label: "Admin Users", value: "AdminUsers" },
          { label: "Active Confirmed Admins", value: "ActiveConfirmedAdmins" },
          { label: "Current User", value: "CurrentUser" }
        ]
      }
    }),
    page: import_pieces_framework27.Property.Number({
      displayName: "Page",
      required: !1,
      description: "Page index (default 1)"
    }),
    per_page: import_pieces_framework27.Property.Number({
      displayName: "Per Page",
      required: !1,
      description: "Records per page (max 200, default 200)"
    })
  },
  async run({ auth, propsValue }) {
    let { access_token, data } = auth, api_domain = data.api_domain, emailTerm = String(propsValue.email || "").toLowerCase(), params = {};
    propsValue.type && (params.type = propsValue.type), propsValue.page && (params.page = Number(propsValue.page)), propsValue.per_page && (params.per_page = Number(propsValue.per_page));
    let resp = await biginApiService.fetchUsers(access_token, api_domain, params), filtered = (Array.isArray(resp.users) ? resp.users : []).filter((u) => String(u.email || "").toLowerCase().includes(emailTerm));
    return {
      message: "User search completed successfully",
      count: filtered.length,
      data: filtered
    };
  }
});

// ../ap/packages/pieces/community/bigin-by-zoho/src/index.ts
var biginByZoho = (0, import_pieces_framework28.createPiece)({
  displayName: "Bigin by Zoho CRM",
  description: "Bigin by Zoho CRM is a lightweight CRM designed for small businesses to manage contacts, companies, deals (pipeline records), tasks, calls, and events.",
  auth: biginAuth,
  minimumSupportedRelease: "0.36.1",
  categories: [import_pieces_framework29.PieceCategory.SALES_AND_CRM],
  logoUrl: "https://cdn.activepieces.com/pieces/bigin-by-zoho.png",
  authors: ["gs03dev"],
  actions: [
    createCompany,
    updateCompany,
    createContact,
    updateContact,
    createTask,
    updateTask,
    createCall,
    createEvent,
    updateEvent,
    createPipelineRecord,
    updatePipelineRecord,
    searchPipelineRecord,
    searchCompanyRecord,
    searchContactRecord,
    searchProductRecord,
    searchUser
  ],
  triggers: [
    newContactCreated,
    contactUpdated,
    newCompanyCreated,
    companyUpdated,
    newCallCreated,
    newTaskCreated,
    newEventCreated,
    newPipelineRecordCreated,
    pipelineRecordUpdated
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  biginByZoho
});
