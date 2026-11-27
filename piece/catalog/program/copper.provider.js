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

// ../ap/packages/pieces/community/copper/src/index.ts
var index_exports = {};
__export(index_exports, {
  copper: () => copper
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework34 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/copper/src/lib/common/constants.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/copper/src/lib/common/requests.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function fireHttpRequest({
  method,
  path,
  auth,
  body
}) {
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
      "X-PW-AccessToken": auth.props.apiKey,
      "X-PW-Application": "developer_api",
      "X-PW-UserEmail": auth.props.email
    },
    body
  }).then((res) => res.body).catch((err) => {
    throw err;
  });
}
var CopperApiService = {
  async fetchCurrentUser(auth) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.GET,
      path: `/v1${API_ENDPOINTS.USERS}/me`,
      auth
    });
  },
  async fetchLeads(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.LEADS}/search`,
      auth,
      body: payload
    });
  },
  async createTask(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.TASKS}`,
      auth,
      body: payload
    });
  },
  async createProject(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.PROJECTS}`,
      auth,
      body: payload
    });
  },
  async updateProject(auth, projectId, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.PUT,
      path: `/v1${API_ENDPOINTS.PROJECTS}/${projectId}`,
      auth,
      body: payload
    });
  },
  async fetchProjects(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.PROJECTS}/search`,
      auth,
      body: payload
    });
  },
  async createCompany(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.COMPANIES}`,
      auth,
      body: payload
    });
  },
  async updateCompany(auth, companyId, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.PUT,
      path: `/v1${API_ENDPOINTS.COMPANIES}/${companyId}`,
      auth,
      body: payload
    });
  },
  async fetchCompanies(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.COMPANIES}/search`,
      auth,
      body: payload
    });
  },
  async fetchActivityTypes(auth) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.GET,
      path: "/v1/activity_types",
      auth
    });
  },
  async fetchContactTypes(auth) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.GET,
      path: "/v1/contact_types",
      auth
    });
  },
  async fetchLeadStatuses(auth) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.GET,
      path: "/v1/lead_statuses",
      auth
    });
  },
  async fetchCustomerSources(auth) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.GET,
      path: "/v1/customer_sources",
      auth
    });
  },
  async fetchLossReasons(auth) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.GET,
      path: "/v1/loss_reasons",
      auth
    });
  },
  async createActivity(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: "/v1/activities",
      auth,
      body: payload
    });
  },
  async fetchActivities(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: "/v1/activities/search",
      auth,
      body: payload
    });
  },
  async createOpportunity(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.OPPORTUNITIES}`,
      auth,
      body: payload
    });
  },
  async updateOpportunity(auth, opportunityId, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.PUT,
      path: `/v1${API_ENDPOINTS.OPPORTUNITIES}/${opportunityId}`,
      auth,
      body: payload
    });
  },
  async fetchOpportunities(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.OPPORTUNITIES}/search`,
      auth,
      body: payload
    });
  },
  async fetchTasks(auth) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.TASKS}/search`,
      auth
    });
  },
  async fetchPipelines(auth) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.GET,
      path: `/v1${API_ENDPOINTS.PIPELINES}`,
      auth
    });
  },
  async createLead(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.LEADS}`,
      auth,
      body: payload
    });
  },
  async updateLead(auth, leadId, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.PUT,
      path: `/v1${API_ENDPOINTS.LEADS}/${leadId}`,
      auth,
      body: payload
    });
  },
  async convertLead(auth, leadId, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.LEADS}/${leadId}/convert`,
      auth,
      body: payload
    });
  },
  async createPerson(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.PEOPLE}`,
      auth,
      body: payload
    });
  },
  async updatePerson(auth, personId, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.PUT,
      path: `/v1${API_ENDPOINTS.PEOPLE}/${personId}`,
      auth,
      body: payload
    });
  },
  async fetchPeople(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.PEOPLE}/search`,
      auth,
      body: payload
    });
  },
  async fetchUsers(auth) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.USERS}/search`,
      auth
    });
  },
  async createWebhook(auth, payload) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.POST,
      path: `/v1${API_ENDPOINTS.WEBHOOKS}`,
      auth,
      body: payload
    });
  },
  async deleteWebhook(auth, webhookId) {
    return await fireHttpRequest({
      method: import_pieces_common.HttpMethod.DELETE,
      path: `/v1${API_ENDPOINTS.WEBHOOKS}/${webhookId}`,
      auth
    });
  }
};

// ../ap/packages/pieces/community/copper/src/lib/common/constants.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), BASE_URL = "https://api.copper.com/developer_api", CopperAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "",
  required: !0,
  props: {
    email: import_pieces_framework.Property.ShortText({
      displayName: "Email Address",
      description: "Email Address of the Token Owner",
      required: !0
    }),
    apiKey: import_pieces_framework.Property.ShortText({
      displayName: "API Key",
      description: "Your API Key in settings > integrations",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await CopperApiService.fetchCurrentUser({ props: auth, type: import_pieces_framework2.AppConnectionType.CUSTOM_AUTH }), {
        valid: !0
      };
    } catch (err) {
      return {
        valid: !1,
        error: "Invalid API Credentials, please check your credentials and try again"
      };
    }
  }
}), API_ENDPOINTS = {
  USERS: "/users",
  WEBHOOKS: "/webhooks",
  PEOPLE: "/people",
  LEADS: "/leads",
  COMPANIES: "/companies",
  OPPORTUNITIES: "/opportunities",
  TASKS: "/tasks",
  PIPELINES: "/pipelines",
  PROJECTS: "/projects"
}, isNonEmptyStr = (v) => typeof v == "string" && v.trim().length > 0, toUnix = (iso) => iso ? Math.floor(new Date(iso).getTime() / 1e3) : void 0;

// ../ap/packages/pieces/community/copper/src/lib/triggers/new-person.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var CACHE_KEY = "copper_new_person_trigger_key", newPerson = (0, import_pieces_framework3.createTrigger)({
  auth: CopperAuth,
  name: "newPerson",
  displayName: "New Person",
  description: "Triggers when a new person/contact is created.",
  aiMetadata: {
    description: "Fires when a new person (contact) is created in Copper CRM, via a Copper webhook. Emits the newly created person record."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await CopperApiService.createWebhook(context.auth, {
      target: context.webhookUrl,
      type: "person",
      event: "new"
    });
    await context.store.put(CACHE_KEY, {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY);
    cachedWebhookData && await CopperApiService.deleteWebhook(
      context.auth,
      cachedWebhookData.webhookId
    ).then(async () => {
      await context.store.delete(CACHE_KEY);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/copper/src/lib/triggers/new-lead.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var CACHE_KEY2 = "copper_new_lead_trigger_key", newLead = (0, import_pieces_framework4.createTrigger)({
  auth: CopperAuth,
  name: "newLead",
  displayName: "New Lead",
  description: "Triggers when a new lead is created.",
  aiMetadata: {
    description: "Fires when a new lead is created in Copper CRM, via a Copper webhook. Emits the newly created lead record."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await CopperApiService.createWebhook(context.auth, {
      target: context.webhookUrl,
      type: "lead",
      event: "new"
    });
    await context.store.put(CACHE_KEY2, {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY2);
    cachedWebhookData && await CopperApiService.deleteWebhook(
      context.auth,
      cachedWebhookData.webhookId
    ).then(async () => {
      await context.store.delete(CACHE_KEY2);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/copper/src/lib/triggers/new-task.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var CACHE_KEY3 = "copper_new_task_trigger_key", newTask = (0, import_pieces_framework5.createTrigger)({
  auth: CopperAuth,
  name: "newTask",
  displayName: "New Task",
  description: "Triggers when a new task is created.",
  aiMetadata: {
    description: "Fires when a new task is created in Copper CRM, via a Copper webhook. Emits the newly created task record."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await CopperApiService.createWebhook(context.auth, {
      target: context.webhookUrl,
      type: "task",
      event: "new"
    });
    await context.store.put(CACHE_KEY3, {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY3);
    cachedWebhookData && await CopperApiService.deleteWebhook(
      context.auth,
      cachedWebhookData.webhookId
    ).then(async () => {
      await context.store.delete(CACHE_KEY3);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/copper/src/lib/triggers/updated-lead.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var CACHE_KEY4 = "copper_updated_lead_trigger_key", updatedLead = (0, import_pieces_framework6.createTrigger)({
  auth: CopperAuth,
  name: "updatedLead",
  displayName: "Updated Lead",
  description: "Triggers when a lead is modified.",
  aiMetadata: {
    description: "Fires when an existing lead is modified in Copper CRM (any field change), via a Copper webhook. Emits the updated lead record."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await CopperApiService.createWebhook(context.auth, {
      target: context.webhookUrl,
      type: "lead",
      event: "update"
    });
    await context.store.put(CACHE_KEY4, {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY4);
    cachedWebhookData && await CopperApiService.deleteWebhook(
      context.auth,
      cachedWebhookData.webhookId
    ).then(async () => {
      await context.store.delete(CACHE_KEY4);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/copper/src/lib/triggers/updated-task.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var CACHE_KEY5 = "copper_updated_task_trigger_key", updatedTask = (0, import_pieces_framework7.createTrigger)({
  auth: CopperAuth,
  name: "updatedTask",
  displayName: "Updated Task",
  description: "Triggers when a task is updated.",
  aiMetadata: {
    description: "Fires when an existing task is modified in Copper CRM (any field change), via a Copper webhook. Emits the updated task record."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await CopperApiService.createWebhook(context.auth, {
      target: context.webhookUrl,
      type: "task",
      event: "update"
    });
    await context.store.put(CACHE_KEY5, {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY5);
    cachedWebhookData && await CopperApiService.deleteWebhook(
      context.auth,
      cachedWebhookData.webhookId
    ).then(async () => {
      await context.store.delete(CACHE_KEY5);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/copper/src/lib/triggers/updated-opportunity.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var CACHE_KEY6 = "copper_updated_opportunity_trigger_key", updatedOpportunity = (0, import_pieces_framework8.createTrigger)({
  auth: CopperAuth,
  name: "updatedOpportunity",
  displayName: "Updated Opportunity",
  description: "Triggers when an opportunity changes.",
  aiMetadata: {
    description: "Fires when an existing opportunity (deal) is modified in Copper CRM (any field change), via a Copper webhook. Emits the updated opportunity record. For stage- or status-specific changes use the dedicated Updated Opportunity Stage / Status triggers instead."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await CopperApiService.createWebhook(context.auth, {
      target: context.webhookUrl,
      type: "opportunity",
      event: "update"
    });
    await context.store.put(CACHE_KEY6, {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY6);
    cachedWebhookData && await CopperApiService.deleteWebhook(
      context.auth,
      cachedWebhookData.webhookId
    ).then(async () => {
      await context.store.delete(CACHE_KEY6);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/copper/src/lib/triggers/updated-opportunity-stage.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var CACHE_KEY7 = "copper_updated_opportunity_stage_trigger_key", updatedOpportunityStage = (0, import_pieces_framework9.createTrigger)({
  auth: CopperAuth,
  name: "updatedOpportunityStage",
  displayName: "Updated Opportunity Stage",
  description: "Triggers when an opportunity stage changes",
  aiMetadata: {
    description: "Fires only when an opportunity moves to a different pipeline stage in Copper CRM (other opportunity edits are ignored), via a Copper webhook. Emits a stage-change event with the previous and current stage id and label."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await CopperApiService.createWebhook(context.auth, {
      target: context.webhookUrl,
      type: "opportunity",
      event: "update"
    });
    await context.store.put(CACHE_KEY7, {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY7);
    cachedWebhookData && await CopperApiService.deleteWebhook(
      context.auth,
      cachedWebhookData.webhookId
    ).then(async () => {
      await context.store.delete(CACHE_KEY7);
    });
  },
  async run(context) {
    let body = context.payload.body, ids = Array.isArray(body.ids) ? body.ids : [], updatedAttrs = body.updated_attributes, idChanged = Array.isArray(updatedAttrs.stage_id) && updatedAttrs.stage_id[0] !== updatedAttrs.stage_id[1], labelChanged = Array.isArray(updatedAttrs.stage) && updatedAttrs.stage[0] !== updatedAttrs.stage[1];
    return idChanged && labelChanged ? ids.map((id) => {
      var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p;
      return {
        id,
        change_type: "stage_change",
        previous_stage_id: (_b = (_a = updatedAttrs.stage_id) == null ? void 0 : _a[0]) != null ? _b : null,
        current_stage_id: (_d = (_c = updatedAttrs.stage_id) == null ? void 0 : _c[1]) != null ? _d : null,
        previous_stage_label: (_f = (_e = updatedAttrs.stage) == null ? void 0 : _e[0]) != null ? _f : null,
        current_stage_label: (_h = (_g = updatedAttrs.stage) == null ? void 0 : _g[1]) != null ? _h : null,
        previous_last_stage_at: (_j = (_i = updatedAttrs.last_stage_at) == null ? void 0 : _i[0]) != null ? _j : null,
        current_last_stage_at: (_l = (_k = updatedAttrs.last_stage_at) == null ? void 0 : _k[1]) != null ? _l : null,
        previous_days_in_stage: (_n = (_m = updatedAttrs.days_in_stage) == null ? void 0 : _m[0]) != null ? _n : null,
        current_days_in_stage: (_p = (_o = updatedAttrs.days_in_stage) == null ? void 0 : _o[1]) != null ? _p : null,
        subscription_id: body.subscription_id,
        timestamp: body.timestamp
      };
    }) : [];
  }
});

// ../ap/packages/pieces/community/copper/src/lib/triggers/updated-opportunity-status.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var CACHE_KEY8 = "copper_updated_opportunity_status_trigger_key", updatedOpportunityStatus = (0, import_pieces_framework10.createTrigger)({
  auth: CopperAuth,
  name: "updatedOpportunityStatus",
  displayName: "Updated Opportunity Status",
  description: "Triggers when an opportunity's status changes.",
  aiMetadata: {
    description: "Fires only when an opportunity's status changes in Copper CRM, e.g. Open to Won/Lost/Abandoned (other opportunity edits are ignored), via a Copper webhook. Emits a status-change event with the previous and current status."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await CopperApiService.createWebhook(context.auth, {
      target: context.webhookUrl,
      type: "opportunity",
      event: "update"
    });
    await context.store.put(CACHE_KEY8, {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY8);
    cachedWebhookData && await CopperApiService.deleteWebhook(
      context.auth,
      cachedWebhookData.webhookId
    ).then(async () => {
      await context.store.delete(CACHE_KEY8);
    });
  },
  async run(context) {
    var _a;
    let body = context.payload.body, ids = Array.isArray(body == null ? void 0 : body.ids) ? body.ids : [], updatedAttrs = (_a = body == null ? void 0 : body.updated_attributes) != null ? _a : {};
    return Array.isArray(updatedAttrs.status) && updatedAttrs.status.length === 2 && updatedAttrs.status[0] !== updatedAttrs.status[1] ? ids.map((id) => {
      var _a2, _b, _c, _d;
      return {
        id,
        change_type: "status_change",
        previous_status: (_b = (_a2 = updatedAttrs.status) == null ? void 0 : _a2[0]) != null ? _b : null,
        current_status: (_d = (_c = updatedAttrs.status) == null ? void 0 : _c[1]) != null ? _d : null,
        subscription_id: body.subscription_id,
        timestamp: body.timestamp
      };
    }) : [];
  }
});

// ../ap/packages/pieces/community/copper/src/lib/triggers/updated-project.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var CACHE_KEY9 = "copper_updated_project_trigger_key", updatedProject = (0, import_pieces_framework11.createTrigger)({
  auth: CopperAuth,
  name: "updatedProject",
  displayName: "Updated Project",
  description: "Triggers when a project is updated.",
  aiMetadata: {
    description: "Fires when an existing project is modified in Copper CRM (any field change), via a Copper webhook. Emits the updated project record."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await CopperApiService.createWebhook(context.auth, {
      target: context.webhookUrl,
      type: "project",
      event: "update"
    });
    await context.store.put(CACHE_KEY9, {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY9);
    cachedWebhookData && await CopperApiService.deleteWebhook(
      context.auth,
      cachedWebhookData.webhookId
    ).then(async () => {
      await context.store.delete(CACHE_KEY9);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/copper/src/lib/triggers/updated-lead-status.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var CACHE_KEY10 = "copper_updated_lead_status_trigger_key", updatedLeadStatus = (0, import_pieces_framework12.createTrigger)({
  auth: CopperAuth,
  name: "updatedLeadStatus",
  displayName: "Updated Lead Status",
  description: "Triggers when the status of a lead changes.",
  aiMetadata: {
    description: "Fires only when a lead's status changes in Copper CRM (other lead edits are ignored), via a Copper webhook. Emits a status-change event with the previous and current status."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let response = await CopperApiService.createWebhook(context.auth, {
      target: context.webhookUrl,
      type: "lead",
      event: "update"
    });
    await context.store.put(CACHE_KEY10, {
      webhookId: response.id
    });
  },
  async onDisable(context) {
    let cachedWebhookData = await context.store.get(CACHE_KEY10);
    cachedWebhookData && await CopperApiService.deleteWebhook(
      context.auth,
      cachedWebhookData.webhookId
    ).then(async () => {
      await context.store.delete(CACHE_KEY10);
    });
  },
  async run(context) {
    var _a;
    let body = context.payload.body, ids = Array.isArray(body == null ? void 0 : body.ids) ? body.ids : [], updatedAttrs = (_a = body == null ? void 0 : body.updated_attributes) != null ? _a : {};
    return Array.isArray(updatedAttrs.status) && updatedAttrs.status.length === 2 && updatedAttrs.status[0] !== updatedAttrs.status[1] ? ids.map((id) => {
      var _a2, _b, _c, _d;
      return {
        id,
        change_type: "status_change",
        previous_status: (_b = (_a2 = updatedAttrs.status) == null ? void 0 : _a2[0]) != null ? _b : null,
        current_status: (_d = (_c = updatedAttrs.status) == null ? void 0 : _c[1]) != null ? _d : null,
        subscription_id: body.subscription_id,
        timestamp: body.timestamp
      };
    }) : [];
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/create-person.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var createPerson = (0, import_pieces_framework13.createAction)({
  auth: CopperAuth,
  name: "createPerson",
  displayName: "Create Person",
  description: "Adds a new person/contact.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new person (contact) in Copper CRM with a name, one or more categorized emails, and optional phone numbers and address. Use to add a contact to the CRM. Requires at least one valid email. Not idempotent: each call creates a separate person record even with identical input.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework13.Property.ShortText({ displayName: "Full Name", required: !0 }),
    emails: import_pieces_framework13.Property.Array({
      displayName: "Emails",
      required: !0,
      properties: {
        email: import_pieces_framework13.Property.ShortText({
          displayName: "Email",
          required: !0
        }),
        category: import_pieces_framework13.Property.ShortText({
          displayName: "Category (e.g., work, personal)",
          required: !0
        })
      },
      defaultValue: []
    }),
    phone_numbers: import_pieces_framework13.Property.Array({
      displayName: "Phone Numbers",
      required: !1,
      properties: {
        number: import_pieces_framework13.Property.ShortText({
          displayName: "Number",
          required: !0
        }),
        category: import_pieces_framework13.Property.ShortText({
          displayName: "Category (e.g., mobile, work, personal)",
          required: !0
        })
      },
      defaultValue: []
    }),
    address_street: import_pieces_framework13.Property.ShortText({
      displayName: "Street",
      required: !1
    }),
    address_city: import_pieces_framework13.Property.ShortText({ displayName: "City", required: !1 }),
    address_state: import_pieces_framework13.Property.ShortText({
      displayName: "State/Region",
      required: !1
    }),
    address_postal_code: import_pieces_framework13.Property.ShortText({
      displayName: "Postal Code",
      required: !1
    }),
    address_country: import_pieces_framework13.Property.ShortText({
      displayName: "Country",
      required: !1
    })
  },
  async run(context) {
    let {
      name,
      emails,
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country,
      phone_numbers
    } = context.propsValue, normEmails = (Array.isArray(emails) ? emails : []).map((row) => {
      var _a;
      return {
        email: String((_a = row == null ? void 0 : row.email) != null ? _a : "").trim().toLowerCase(),
        category: isNonEmptyStr(row == null ? void 0 : row.category) ? String(row.category).trim() : void 0
      };
    }).filter((e) => isNonEmptyStr(e.email));
    if (normEmails.length === 0)
      throw new Error("Please provide at least one valid email.");
    let seen = /* @__PURE__ */ new Set(), dedupedEmails = normEmails.filter(
      (e) => !seen.has(e.email) && (seen.add(e.email), !0)
    ), normPhones = (Array.isArray(phone_numbers) ? phone_numbers : []).map((p) => {
      var _a;
      return {
        number: String((_a = p == null ? void 0 : p.number) != null ? _a : "").trim(),
        category: isNonEmptyStr(p == null ? void 0 : p.category) ? String(p.category).trim() : void 0
      };
    }).filter((p) => isNonEmptyStr(p.number)), address = [
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country
    ].some(isNonEmptyStr) ? {
      street: isNonEmptyStr(address_street) ? address_street.trim() : void 0,
      city: isNonEmptyStr(address_city) ? address_city.trim() : void 0,
      state: isNonEmptyStr(address_state) ? address_state.trim() : void 0,
      postal_code: isNonEmptyStr(address_postal_code) ? address_postal_code.trim() : void 0,
      country: isNonEmptyStr(address_country) ? address_country.trim() : void 0
    } : void 0, body = __spreadValues(__spreadValues({
      name,
      emails: dedupedEmails
    }, normPhones.length ? { phone_numbers: normPhones } : {}), address ? { address } : {});
    return await CopperApiService.createPerson(context.auth, body);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/update-person.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/copper/src/lib/common/props.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var peopleDropdown = (refreshers) => import_pieces_framework14.Property.Dropdown({
  displayName: "Person",
  description: "select a person",
  required: !0,
  refreshers,
  auth: CopperAuth,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchPeople(auth)).map((person) => ({
          label: person.name,
          value: JSON.stringify(person)
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch campaigns", e), {
        options: [],
        placeholder: "Unable to load campaigns"
      };
    }
  }
}), leadDropdown = (refreshers) => import_pieces_framework14.Property.Dropdown({
  auth: CopperAuth,
  displayName: "Lead",
  description: "select a Lead",
  required: !0,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchLeads(auth)).map((lead) => ({
          label: lead.name,
          value: JSON.stringify(lead)
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch leads", e), {
        options: [],
        placeholder: "Unable to load leads"
      };
    }
  }
}), companyDropdown = ({
  refreshers,
  required = !1
}) => import_pieces_framework14.Property.Dropdown({
  auth: CopperAuth,
  displayName: "Company",
  description: "select a Company",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchCompanies(auth)).map((company) => ({
          label: company.name,
          value: JSON.stringify(company)
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch companies", e), {
        options: [],
        placeholder: "Unable to load companies"
      };
    }
  }
}), multiCompanyDropdown = ({
  refreshers,
  required = !1
}) => import_pieces_framework14.Property.MultiSelectDropdown({
  auth: CopperAuth,
  displayName: "Company",
  description: "select Companies",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchCompanies(auth)).map((company) => ({
          label: company.name,
          value: company.id
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch companies", e), {
        options: [],
        placeholder: "Unable to load companies"
      };
    }
  }
}), primaryContactsDropdown = ({
  refreshers,
  required = !1
}) => import_pieces_framework14.Property.Dropdown({
  auth: CopperAuth,
  displayName: "Primary Contact",
  description: "select a primary contact",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchPeople(auth)).map((contact) => ({
          label: contact.name,
          value: contact.id
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch opportunities", e), {
        options: [],
        placeholder: "Unable to load opportunities"
      };
    }
  }
}), multiPrimaryContactsDropdown = ({
  refreshers,
  required = !1
}) => import_pieces_framework14.Property.MultiSelectDropdown({
  auth: CopperAuth,
  displayName: "Primary Contacts",
  description: "select primary contacts",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchPeople(auth)).map((contact) => ({
          label: contact.name,
          value: contact.id
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch opportunities", e), {
        options: [],
        placeholder: "Unable to load opportunities"
      };
    }
  }
}), usersDropdown = ({
  refreshers,
  required = !1
}) => import_pieces_framework14.Property.Dropdown({
  auth: CopperAuth,
  displayName: "Assignee",
  description: "select a user to assign to",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchUsers(auth)).map((user) => ({
          label: user.name,
          value: user.id
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch opportunities", e), {
        options: [],
        placeholder: "Unable to load opportunities"
      };
    }
  }
}), multiUsersDropdown = ({
  refreshers,
  required = !1
}) => import_pieces_framework14.Property.MultiSelectDropdown({
  auth: CopperAuth,
  displayName: "Assignee",
  description: "select assignees",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchUsers(auth)).map((user) => ({
          label: user.name,
          value: user.id
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch opportunities", e), {
        options: [],
        placeholder: "Unable to load opportunities"
      };
    }
  }
}), opportunityDropdown = ({
  refreshers,
  required = !1
}) => import_pieces_framework14.Property.Dropdown({
  auth: CopperAuth,
  displayName: "Opportunity",
  description: "select an Opportunity",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchOpportunities(auth)).map((opportunity) => ({
          label: opportunity.name,
          value: JSON.stringify(opportunity)
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch opportunities", e), {
        options: [],
        placeholder: "Unable to load opportunities"
      };
    }
  }
}), multiOpportunityDropdown = ({
  refreshers,
  required = !1
}) => import_pieces_framework14.Property.MultiSelectDropdown({
  auth: CopperAuth,
  displayName: "Opportunity",
  description: "select Opportunities",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchOpportunities(auth)).map((opportunity) => ({
          label: opportunity.name,
          value: opportunity.id
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch opportunities", e), {
        options: [],
        placeholder: "Unable to load opportunities"
      };
    }
  }
}), pipelinesDropdown = ({
  refreshers,
  required = !1
}) => import_pieces_framework14.Property.Dropdown({
  auth: CopperAuth,
  displayName: "Pipeline",
  description: "select a Pipeline",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchPipelines(auth)).map((pipeline) => ({
          label: pipeline.name,
          value: JSON.stringify(pipeline)
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch pipelines", e), {
        options: [],
        placeholder: "Unable to load pipelines"
      };
    }
  }
}), multiPipelinesDropdown = ({
  refreshers,
  required = !1
}) => import_pieces_framework14.Property.MultiSelectDropdown({
  auth: CopperAuth,
  displayName: "Pipeline",
  description: "select a Pipeline",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchPipelines(auth)).map((pipeline) => ({
          label: pipeline.name,
          value: JSON.stringify(pipeline)
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch pipelines", e), {
        options: [],
        placeholder: "Unable to load pipelines"
      };
    }
  }
}), projectsDropdown = ({
  refreshers,
  required = !1
}) => import_pieces_framework14.Property.Dropdown({
  auth: CopperAuth,
  displayName: "Project",
  description: "select a Project",
  required,
  refreshers,
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchProjects(auth)).map((project) => ({
          label: project.name,
          value: JSON.stringify(project)
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch projects", e), {
        options: [],
        placeholder: "Unable to load projects"
      };
    }
  }
}), ActivityTypesDropdown = (entity) => import_pieces_framework14.Property.Dropdown({
  auth: CopperAuth,
  displayName: "Activity Type",
  description: "Select activity Type",
  required: !0,
  refreshers: ["auth"],
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      let response = await CopperApiService.fetchActivityTypes(auth);
      return {
        options: (entity && response[entity] ? response[entity] : [...response.user, ...response.system]).map((item) => ({
          label: item.name,
          value: JSON.stringify(item)
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch activity types", e), {
        options: [],
        placeholder: "Unable to load activity types"
      };
    }
  }
}), MultiActivityTypesDropdown = ({
  entity,
  required = !1
}) => import_pieces_framework14.Property.MultiSelectDropdown({
  auth: CopperAuth,
  displayName: "Activity Type",
  description: "Select activity Type",
  required,
  refreshers: ["auth"],
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      let response = await CopperApiService.fetchActivityTypes(auth);
      return {
        options: (entity && response[entity] ? response[entity] : [...response.user, ...response.system]).map((item) => ({
          label: item.name,
          value: JSON.stringify(item)
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch activity types", e), {
        options: [],
        placeholder: "Unable to load activity types"
      };
    }
  }
}), MultiContactTypesDropdown = ({
  required = !1
}) => import_pieces_framework14.Property.MultiSelectDropdown({
  auth: CopperAuth,
  displayName: "Contact Type",
  description: "Select contact Type",
  required,
  refreshers: ["auth"],
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchContactTypes(auth)).map((item) => ({
          label: item.name,
          value: item.id
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch contact types", e), {
        options: [],
        placeholder: "Unable to load contact types"
      };
    }
  }
}), MultiLeadStatusDropdown = ({
  required = !1
}) => import_pieces_framework14.Property.MultiSelectDropdown({
  auth: CopperAuth,
  displayName: "Lead Status",
  description: "Select lead status",
  required,
  refreshers: ["auth"],
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchLeadStatuses(auth)).map((item) => ({
          label: item.name,
          value: item.id
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch lead statuses", e), {
        options: [],
        placeholder: "Unable to load lead statuses"
      };
    }
  }
}), MultiCustomerSourceDropdown = ({
  required = !1
}) => import_pieces_framework14.Property.MultiSelectDropdown({
  auth: CopperAuth,
  displayName: "Customer Source",
  description: "Select customer source.",
  required,
  refreshers: ["auth"],
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchCustomerSources(auth)).map((item) => ({
          label: item.name,
          value: item.id
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch customer sources", e), {
        options: [],
        placeholder: "Unable to load customer sources"
      };
    }
  }
}), MultiLossReasonsDropdown = ({
  required = !1
}) => import_pieces_framework14.Property.MultiSelectDropdown({
  auth: CopperAuth,
  displayName: "Loss Reason",
  description: "Select loss reason.",
  required,
  refreshers: ["auth"],
  async options({ auth }) {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Copper account first",
        options: []
      };
    try {
      return {
        options: (await CopperApiService.fetchLossReasons(auth)).map((item) => ({
          label: item.name,
          value: item.id
        }))
      };
    } catch (e) {
      return console.error("Failed to fetch loss reasons", e), {
        options: [],
        placeholder: "Unable to load loss reasons"
      };
    }
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/update-person.ts
var updatePerson = (0, import_pieces_framework15.createAction)({
  auth: CopperAuth,
  name: "updatePerson",
  displayName: "Update Person",
  description: "Updates a person based on matching criteria.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing person (contact) in Copper CRM, identified by person ID, overwriting name, emails, phone numbers, and address with the supplied values. Use to modify a known contact; requires the target person ID and at least one valid email. Idempotent: re-applying the same values leaves the record in the same state.",
    idempotent: !0
  },
  props: {
    personId: peopleDropdown(["auth"]),
    fields: import_pieces_framework15.Property.DynamicProperties({
      displayName: "",
      description: "",
      refreshers: ["auth", "personId"],
      required: !1,
      auth: CopperAuth,
      props: async ({ auth, personId }) => {
        var _a, _b, _c, _d, _e;
        if (!auth || !personId) return {};
        let person = JSON.parse(personId);
        return {
          name: import_pieces_framework15.Property.ShortText({
            displayName: "Full Name",
            required: !0,
            defaultValue: person.name
          }),
          emails: import_pieces_framework15.Property.Array({
            displayName: "Emails",
            required: !0,
            properties: {
              email: import_pieces_framework15.Property.ShortText({
                displayName: "Email",
                required: !0
              }),
              category: import_pieces_framework15.Property.ShortText({
                displayName: "Category (e.g., work, personal)",
                required: !0
              })
            },
            defaultValue: person.emails
          }),
          phone_numbers: import_pieces_framework15.Property.Array({
            displayName: "Phone Numbers",
            required: !1,
            properties: {
              number: import_pieces_framework15.Property.ShortText({
                displayName: "Number",
                required: !0
              }),
              category: import_pieces_framework15.Property.ShortText({
                displayName: "Category (e.g., mobile, work, personal)",
                required: !0
              })
            },
            defaultValue: person.phone_numbers
          }),
          address_street: import_pieces_framework15.Property.ShortText({
            displayName: "Street",
            required: !1,
            defaultValue: (_a = person.address) == null ? void 0 : _a.street
          }),
          address_city: import_pieces_framework15.Property.ShortText({
            displayName: "City",
            required: !1,
            defaultValue: (_b = person.address) == null ? void 0 : _b.city
          }),
          address_state: import_pieces_framework15.Property.ShortText({
            displayName: "State/Region",
            required: !1,
            defaultValue: (_c = person.address) == null ? void 0 : _c.state
          }),
          address_postal_code: import_pieces_framework15.Property.ShortText({
            displayName: "Postal Code",
            required: !1,
            defaultValue: (_d = person.address) == null ? void 0 : _d.postal_code
          }),
          address_country: import_pieces_framework15.Property.ShortText({
            displayName: "Country",
            required: !1,
            defaultValue: (_e = person.address) == null ? void 0 : _e.country
          })
        };
      }
    })
  },
  async run(context) {
    let fields = context.propsValue.fields, personId = context.propsValue.personId, person = JSON.parse(personId), {
      name,
      emails,
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country,
      phone_numbers
    } = fields, normEmails = (Array.isArray(emails) ? emails : []).map((row) => {
      var _a;
      return {
        email: String((_a = row == null ? void 0 : row.email) != null ? _a : "").trim().toLowerCase(),
        category: isNonEmptyStr(row == null ? void 0 : row.category) ? String(row.category).trim() : void 0
      };
    }).filter((e) => isNonEmptyStr(e.email));
    if (normEmails.length === 0)
      throw new Error("Please provide at least one valid email.");
    let seen = /* @__PURE__ */ new Set(), dedupedEmails = normEmails.filter(
      (e) => !seen.has(e.email) && (seen.add(e.email), !0)
    ), normPhones = (Array.isArray(phone_numbers) ? phone_numbers : []).map((p) => {
      var _a;
      return {
        number: String((_a = p == null ? void 0 : p.number) != null ? _a : "").trim(),
        category: isNonEmptyStr(p == null ? void 0 : p.category) ? String(p.category).trim() : void 0
      };
    }).filter((p) => isNonEmptyStr(p.number)), address = [
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country
    ].some(isNonEmptyStr) ? {
      street: isNonEmptyStr(address_street) ? address_street.trim() : void 0,
      city: isNonEmptyStr(address_city) ? address_city.trim() : void 0,
      state: isNonEmptyStr(address_state) ? address_state.trim() : void 0,
      postal_code: isNonEmptyStr(address_postal_code) ? address_postal_code.trim() : void 0,
      country: isNonEmptyStr(address_country) ? address_country.trim() : void 0
    } : void 0, body = __spreadValues(__spreadValues({
      name,
      emails: dedupedEmails
    }, normPhones.length ? { phone_numbers: normPhones } : {}), address ? { address } : {});
    return await CopperApiService.updatePerson(context.auth, person.id, body);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/create-lead.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var createLead = (0, import_pieces_framework16.createAction)({
  auth: CopperAuth,
  name: "createLead",
  displayName: "Create Lead",
  description: "Adds a new lead.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new lead in Copper CRM with a name, a single categorized email, and optional phone numbers and address. Use to capture a prospect not yet qualified into a person/opportunity. Not idempotent: each call creates a separate lead even with identical input.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework16.Property.ShortText({ displayName: "Full Name", required: !0 }),
    email: import_pieces_framework16.Property.ShortText({
      displayName: "Email",
      required: !0
    }),
    category: import_pieces_framework16.Property.ShortText({
      displayName: "Category of the email address (e.g., work, personal)",
      required: !0
    }),
    phone_numbers: import_pieces_framework16.Property.Array({
      displayName: "Phone Numbers",
      required: !1,
      properties: {
        number: import_pieces_framework16.Property.ShortText({
          displayName: "Number",
          required: !0
        }),
        category: import_pieces_framework16.Property.ShortText({
          displayName: "Category (e.g., mobile, work, personal)",
          required: !0
        })
      },
      defaultValue: []
    }),
    address_street: import_pieces_framework16.Property.ShortText({
      displayName: "Street",
      required: !1
    }),
    address_city: import_pieces_framework16.Property.ShortText({ displayName: "City", required: !1 }),
    address_state: import_pieces_framework16.Property.ShortText({
      displayName: "State/Region",
      required: !1
    }),
    address_postal_code: import_pieces_framework16.Property.ShortText({
      displayName: "Postal Code",
      required: !1
    }),
    address_country: import_pieces_framework16.Property.ShortText({
      displayName: "Country",
      required: !1
    })
  },
  async run(context) {
    let {
      name,
      email,
      category,
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country,
      phone_numbers
    } = context.propsValue, normPhones = (Array.isArray(phone_numbers) ? phone_numbers : []).map((p) => {
      var _a;
      return {
        number: String((_a = p == null ? void 0 : p.number) != null ? _a : "").trim(),
        category: isNonEmptyStr(p == null ? void 0 : p.category) ? String(p.category).trim() : void 0
      };
    }).filter((p) => isNonEmptyStr(p.number)), address = [
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country
    ].some(isNonEmptyStr) ? {
      street: isNonEmptyStr(address_street) ? address_street.trim() : void 0,
      city: isNonEmptyStr(address_city) ? address_city.trim() : void 0,
      state: isNonEmptyStr(address_state) ? address_state.trim() : void 0,
      postal_code: isNonEmptyStr(address_postal_code) ? address_postal_code.trim() : void 0,
      country: isNonEmptyStr(address_country) ? address_country.trim() : void 0
    } : void 0, body = __spreadValues(__spreadValues({
      name,
      email: {
        email,
        category
      }
    }, normPhones.length ? { phone_numbers: normPhones } : {}), address ? { address } : {});
    return await CopperApiService.createLead(context.auth, body);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/update-lead.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var updateLead = (0, import_pieces_framework17.createAction)({
  auth: CopperAuth,
  name: "updateLead",
  displayName: "Update Lead",
  description: "Updates an existing lead.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing lead in Copper CRM, identified by lead ID, overwriting name, email, phone numbers, and address with the supplied values. Use to modify a known lead; requires the target lead ID. Idempotent: re-applying the same values leaves the record in the same state.",
    idempotent: !0
  },
  props: {
    leadId: leadDropdown(["auth"]),
    fields: import_pieces_framework17.Property.DynamicProperties({
      displayName: "",
      description: "",
      refreshers: ["auth", "leadId"],
      required: !1,
      auth: CopperAuth,
      props: async ({ auth, leadId }) => {
        var _a, _b, _c, _d, _e;
        if (!auth || !leadId) return {};
        let lead = JSON.parse(leadId);
        return {
          name: import_pieces_framework17.Property.ShortText({
            displayName: "Full Name",
            required: !0,
            defaultValue: lead.name
          }),
          email: import_pieces_framework17.Property.ShortText({
            displayName: "Email",
            required: !0,
            defaultValue: lead.email.email
          }),
          category: import_pieces_framework17.Property.ShortText({
            displayName: "Category of the email address (e.g., work, personal)",
            required: !0,
            defaultValue: lead.email.category
          }),
          phone_numbers: import_pieces_framework17.Property.Array({
            displayName: "Phone Numbers",
            required: !1,
            properties: {
              number: import_pieces_framework17.Property.ShortText({
                displayName: "Number",
                required: !0
              }),
              category: import_pieces_framework17.Property.ShortText({
                displayName: "Category (e.g., mobile, work, personal)",
                required: !0
              })
            },
            defaultValue: lead.phone_numbers
          }),
          address_street: import_pieces_framework17.Property.ShortText({
            displayName: "Street",
            required: !1,
            defaultValue: (_a = lead.address) == null ? void 0 : _a.street
          }),
          address_city: import_pieces_framework17.Property.ShortText({
            displayName: "City",
            required: !1,
            defaultValue: (_b = lead.address) == null ? void 0 : _b.city
          }),
          address_state: import_pieces_framework17.Property.ShortText({
            displayName: "State/Region",
            required: !1,
            defaultValue: (_c = lead.address) == null ? void 0 : _c.state
          }),
          address_postal_code: import_pieces_framework17.Property.ShortText({
            displayName: "Postal Code",
            required: !1,
            defaultValue: (_d = lead.address) == null ? void 0 : _d.postal_code
          }),
          address_country: import_pieces_framework17.Property.ShortText({
            displayName: "Country",
            required: !1,
            defaultValue: (_e = lead.address) == null ? void 0 : _e.country
          })
        };
      }
    })
  },
  async run(context) {
    let fields = context.propsValue.fields, leadId = context.propsValue.leadId, lead = JSON.parse(leadId), {
      name,
      email,
      category,
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country,
      phone_numbers
    } = fields, normPhones = (Array.isArray(phone_numbers) ? phone_numbers : []).map((p) => {
      var _a;
      return {
        number: String((_a = p == null ? void 0 : p.number) != null ? _a : "").trim(),
        category: isNonEmptyStr(p == null ? void 0 : p.category) ? String(p.category).trim() : void 0
      };
    }).filter((p) => isNonEmptyStr(p.number)), address = [
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country
    ].some(isNonEmptyStr) ? {
      street: isNonEmptyStr(address_street) ? address_street.trim() : void 0,
      city: isNonEmptyStr(address_city) ? address_city.trim() : void 0,
      state: isNonEmptyStr(address_state) ? address_state.trim() : void 0,
      postal_code: isNonEmptyStr(address_postal_code) ? address_postal_code.trim() : void 0,
      country: isNonEmptyStr(address_country) ? address_country.trim() : void 0
    } : void 0, body = __spreadValues(__spreadValues({
      name,
      email: {
        email,
        category
      }
    }, normPhones.length ? { phone_numbers: normPhones } : {}), address ? { address } : {});
    return await CopperApiService.updateLead(context.auth, lead.id, body);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/convert-lead.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var convertLead = (0, import_pieces_framework18.createAction)({
  auth: CopperAuth,
  name: "convertLead",
  displayName: "Convert Lead",
  description: "Converts a lead into a person (optionally with company/opportunity).",
  audience: "both",
  aiMetadata: {
    description: "Converts a qualified lead in Copper CRM into a person, optionally linking it to an existing company and/or creating an opportunity from the selected opportunity template. Use once a lead is ready to be promoted into the contact/deal pipeline; requires the lead ID. Not idempotent: it consumes the lead and creates new records, so repeating it is not safe.",
    idempotent: !1
  },
  props: {
    leadId: leadDropdown(["auth"]),
    companyId: companyDropdown({ refreshers: ["auth"] }),
    opportunityId: opportunityDropdown({ refreshers: ["auth"] })
  },
  async run(context) {
    let { leadId, companyId, opportunityId } = context.propsValue, opportunity = opportunityId ? JSON.parse(opportunityId) : null, company = companyId ? JSON.parse(companyId) : null, lead = JSON.parse(leadId), payload = __spreadValues({
      person: {
        name: lead.name
      },
      company: __spreadValues({}, company ? {
        id: company.id
      } : {
        name: ""
      })
    }, opportunity ? {
      opportunity: {
        name: opportunity.name,
        pipeline_id: opportunity.pipeline_id,
        pipeline_stage_id: opportunity.pipeline_stage_id,
        monetary_value: opportunity.monetary_value || void 0,
        assignee_id: opportunity.assignee_id
      }
    } : {});
    return await CopperApiService.convertLead(context.auth, lead.id, {
      details: payload
    });
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/create-company.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var createCompany = (0, import_pieces_framework19.createAction)({
  auth: CopperAuth,
  name: "createCompany",
  displayName: "Create Company",
  description: "Adds a new company.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new company in Copper CRM with a name and optional email domain, details, phone numbers, address, and a primary contact. Use to add an organization record. Not idempotent: each call creates a separate company even with identical input.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework19.Property.ShortText({ displayName: "Full Name", required: !0 }),
    email_domain: import_pieces_framework19.Property.ShortText({
      displayName: "Email Domain",
      description: "E.g. democompany.com",
      required: !1
    }),
    details: import_pieces_framework19.Property.ShortText({
      displayName: "Details",
      required: !1
    }),
    phone_numbers: import_pieces_framework19.Property.Array({
      displayName: "Phone Numbers",
      required: !1,
      properties: {
        number: import_pieces_framework19.Property.ShortText({
          displayName: "Number",
          required: !0
        }),
        category: import_pieces_framework19.Property.ShortText({
          displayName: "Category (e.g., mobile, work, personal)",
          required: !0
        })
      },
      defaultValue: []
    }),
    address_street: import_pieces_framework19.Property.ShortText({
      displayName: "Street",
      required: !1
    }),
    address_city: import_pieces_framework19.Property.ShortText({ displayName: "City", required: !1 }),
    address_state: import_pieces_framework19.Property.ShortText({
      displayName: "State/Region",
      required: !1
    }),
    address_postal_code: import_pieces_framework19.Property.ShortText({
      displayName: "Postal Code",
      required: !1
    }),
    address_country: import_pieces_framework19.Property.ShortText({
      displayName: "Country",
      required: !1
    }),
    primaryContactId: primaryContactsDropdown({ refreshers: ["auth"] })
  },
  async run(context) {
    let {
      name,
      email_domain,
      details,
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country,
      phone_numbers,
      primaryContactId
    } = context.propsValue, normPhones = (Array.isArray(phone_numbers) ? phone_numbers : []).map((p) => {
      var _a;
      return {
        number: String((_a = p == null ? void 0 : p.number) != null ? _a : "").trim(),
        category: isNonEmptyStr(p == null ? void 0 : p.category) ? String(p.category).trim() : void 0
      };
    }).filter((p) => isNonEmptyStr(p.number)), address = [
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country
    ].some(isNonEmptyStr) ? {
      street: isNonEmptyStr(address_street) ? address_street.trim() : void 0,
      city: isNonEmptyStr(address_city) ? address_city.trim() : void 0,
      state: isNonEmptyStr(address_state) ? address_state.trim() : void 0,
      postal_code: isNonEmptyStr(address_postal_code) ? address_postal_code.trim() : void 0,
      country: isNonEmptyStr(address_country) ? address_country.trim() : void 0
    } : void 0, body = __spreadProps(__spreadValues(__spreadValues({
      name,
      email_domain,
      details
    }, normPhones.length ? { phone_numbers: normPhones } : {}), address ? { address } : {}), {
      primary_contact_id: primaryContactId
    });
    return await CopperApiService.createCompany(context.auth, body);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/update-company.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var updateCompany = (0, import_pieces_framework20.createAction)({
  auth: CopperAuth,
  name: "updateCompany",
  displayName: "Update Company",
  description: "Updates a company record.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing company in Copper CRM, identified by company ID, overwriting name, email domain, details, phone numbers, address, and primary contact with the supplied values. Use to modify a known company; requires the target company ID. Idempotent: re-applying the same values leaves the record in the same state.",
    idempotent: !0
  },
  props: {
    companyId: companyDropdown({ refreshers: ["auth"], required: !0 }),
    fields: import_pieces_framework20.Property.DynamicProperties({
      auth: CopperAuth,
      displayName: "",
      description: "",
      refreshers: ["auth", "companyId"],
      required: !1,
      props: async ({ auth, companyId }) => {
        var _a, _b, _c, _d, _e;
        if (!auth || !companyId) return {};
        let company = JSON.parse(companyId);
        return {
          name: import_pieces_framework20.Property.ShortText({
            displayName: "Full Name",
            required: !0,
            defaultValue: company.name
          }),
          email_domain: import_pieces_framework20.Property.ShortText({
            displayName: "Email Domain",
            description: "E.g. democompany.com",
            required: !1,
            defaultValue: company.email_domain
          }),
          details: import_pieces_framework20.Property.ShortText({
            displayName: "Details",
            required: !1,
            defaultValue: company.details
          }),
          phone_numbers: import_pieces_framework20.Property.Array({
            displayName: "Phone Numbers",
            required: !1,
            properties: {
              number: import_pieces_framework20.Property.ShortText({
                displayName: "Number",
                required: !0
              }),
              category: import_pieces_framework20.Property.ShortText({
                displayName: "Category (e.g., mobile, work, personal)",
                required: !0
              })
            },
            defaultValue: company.phone_numbers
          }),
          address_street: import_pieces_framework20.Property.ShortText({
            displayName: "Street",
            required: !1,
            defaultValue: (_a = company.address) == null ? void 0 : _a.street
          }),
          address_city: import_pieces_framework20.Property.ShortText({
            displayName: "City",
            required: !1,
            defaultValue: (_b = company.address) == null ? void 0 : _b.city
          }),
          address_state: import_pieces_framework20.Property.ShortText({
            displayName: "State/Region",
            required: !1,
            defaultValue: (_c = company.address) == null ? void 0 : _c.state
          }),
          address_postal_code: import_pieces_framework20.Property.ShortText({
            displayName: "Postal Code",
            required: !1,
            defaultValue: (_d = company.address) == null ? void 0 : _d.postal_code
          }),
          address_country: import_pieces_framework20.Property.ShortText({
            displayName: "Country",
            required: !1,
            defaultValue: (_e = company.address) == null ? void 0 : _e.country
          })
        };
      }
    }),
    primaryContactId: primaryContactsDropdown({ refreshers: ["auth"] })
  },
  async run(context) {
    let { fields, companyId, primaryContactId } = context.propsValue, company = JSON.parse(companyId), {
      name,
      email_domain,
      details,
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country,
      phone_numbers
    } = fields, normPhones = (Array.isArray(phone_numbers) ? phone_numbers : []).map((p) => {
      var _a;
      return {
        number: String((_a = p == null ? void 0 : p.number) != null ? _a : "").trim(),
        category: isNonEmptyStr(p == null ? void 0 : p.category) ? String(p.category).trim() : void 0
      };
    }).filter((p) => isNonEmptyStr(p.number)), address = [
      address_street,
      address_city,
      address_state,
      address_postal_code,
      address_country
    ].some(isNonEmptyStr) ? {
      street: isNonEmptyStr(address_street) ? address_street.trim() : void 0,
      city: isNonEmptyStr(address_city) ? address_city.trim() : void 0,
      state: isNonEmptyStr(address_state) ? address_state.trim() : void 0,
      postal_code: isNonEmptyStr(address_postal_code) ? address_postal_code.trim() : void 0,
      country: isNonEmptyStr(address_country) ? address_country.trim() : void 0
    } : void 0, body = __spreadProps(__spreadValues(__spreadValues({
      name,
      email_domain,
      details
    }, normPhones.length ? { phone_numbers: normPhones } : {}), address ? { address } : {}), {
      primary_contact_id: primaryContactId
    });
    return await CopperApiService.updateCompany(context.auth, company.id, body);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/create-opportunity.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var createOpportunity = (0, import_pieces_framework21.createAction)({
  auth: CopperAuth,
  name: "createOpportunity",
  displayName: "Create Opportunity",
  description: "Adds a new opportunity.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new opportunity (deal) in Copper CRM with a name, placing it in a chosen pipeline and stage and optionally linking a primary contact. Use to open a new deal in the sales pipeline; requires a name and pipeline. Not idempotent: each call creates a separate opportunity even with identical input.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework21.Property.ShortText({
      displayName: "Name",
      description: "The name of the opportunity",
      required: !0
    }),
    pipelineId: pipelinesDropdown({ refreshers: ["auth"] }),
    pipelineStageId: import_pieces_framework21.Property.Dropdown({
      auth: CopperAuth,
      displayName: "Pipeline Stage",
      description: "Select a stage",
      refreshers: ["auth", "pipelineId"],
      required: !1,
      async options({ auth, pipelineId }) {
        return auth ? pipelineId ? {
          options: JSON.parse(pipelineId).stages.map((stage) => ({
            label: stage.name,
            value: stage.id
          }))
        } : {
          disabled: !0,
          placeholder: "Select a pipeline first",
          options: []
        } : {
          disabled: !0,
          placeholder: "Connect your Copper account first",
          options: []
        };
      }
    }),
    primaryContactId: primaryContactsDropdown({ refreshers: ["auth"] })
  },
  async run(context) {
    let { name, primaryContactId, pipelineId, pipelineStageId } = context.propsValue, pipeline = JSON.parse(pipelineId);
    return await CopperApiService.createOpportunity(context.auth, {
      name,
      primary_contact_id: primaryContactId,
      pipeline_id: pipeline.id,
      pipeline_stage_id: pipelineStageId
    });
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/update-opportunity.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var updateOpportunity = (0, import_pieces_framework22.createAction)({
  auth: CopperAuth,
  name: "updateOpportunity",
  displayName: "Update Opportunity",
  description: "Updates an opportunity using match criteria.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing opportunity (deal) in Copper CRM, identified by opportunity ID, overwriting its name, pipeline, stage, and primary contact with the supplied values. Use to move a deal between stages/pipelines or rename it; requires the target opportunity ID and a pipeline. Idempotent: re-applying the same values leaves the record in the same state.",
    idempotent: !0
  },
  props: {
    opportunityId: opportunityDropdown({
      refreshers: ["auth"],
      required: !0
    }),
    updateFields: import_pieces_framework22.Property.DynamicProperties({
      auth: CopperAuth,
      displayName: "",
      description: "",
      required: !1,
      refreshers: ["auth", "opportunityId"],
      props: async ({
        auth,
        opportunityId
      }) => {
        if (!auth || !opportunityId) return {};
        let opportunity = JSON.parse(opportunityId);
        return {
          name: import_pieces_framework22.Property.ShortText({
            displayName: "Name",
            description: "The name of the opportunity",
            required: !0,
            defaultValue: opportunity.name
          })
        };
      }
    }),
    pipelineId: pipelinesDropdown({ refreshers: ["auth"] }),
    pipelineStageId: import_pieces_framework22.Property.Dropdown({
      auth: CopperAuth,
      displayName: "Pipeline Stage",
      description: "Select a stage",
      refreshers: ["auth", "pipelineId"],
      required: !1,
      async options({ auth, pipelineId }) {
        return auth ? pipelineId ? {
          options: JSON.parse(pipelineId).stages.map((stage) => ({
            label: stage.name,
            value: stage.id
          }))
        } : {
          disabled: !0,
          placeholder: "Select a pipeline first",
          options: []
        } : {
          disabled: !0,
          placeholder: "Connect your Copper account first",
          options: []
        };
      }
    }),
    primaryContactId: primaryContactsDropdown({ refreshers: ["auth"] })
  },
  async run(context) {
    let {
      updateFields,
      primaryContactId,
      pipelineId,
      pipelineStageId,
      opportunityId
    } = context.propsValue, pipeline = JSON.parse(pipelineId), opportunity = JSON.parse(opportunityId);
    return await CopperApiService.updateOpportunity(
      context.auth,
      opportunity.id,
      {
        name: updateFields.name,
        primary_contact_id: primaryContactId,
        pipeline_id: pipeline.id,
        pipeline_stage_id: pipelineStageId
      }
    );
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/create-project.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var createProject = (0, import_pieces_framework23.createAction)({
  auth: CopperAuth,
  name: "createProject",
  displayName: "Create Project",
  description: "Adds a new project.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new project in Copper CRM with a name and optional details. Use to add a project record. Requires a name. Not idempotent: each call creates a separate project even with identical input.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework23.Property.ShortText({
      displayName: "Name",
      description: "The name of the project",
      required: !0
    }),
    details: import_pieces_framework23.Property.ShortText({
      displayName: "Details",
      description: "The details of the project",
      required: !1
    })
  },
  async run(context) {
    let { name, details } = context.propsValue;
    return await CopperApiService.createProject(context.auth, {
      name,
      details
    });
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/update-project.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework");
var updateProject = (0, import_pieces_framework24.createAction)({
  auth: CopperAuth,
  name: "updateProject",
  displayName: "Update Project",
  description: "Updates a project record.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing project in Copper CRM, identified by project ID, overwriting its name and details with the supplied values. Use to modify a known project; requires the target project ID. Idempotent: re-applying the same values leaves the record in the same state.",
    idempotent: !0
  },
  props: {
    projectId: projectsDropdown({ refreshers: ["auth"], required: !0 }),
    updateFields: import_pieces_framework24.Property.DynamicProperties({
      displayName: "",
      description: "",
      refreshers: ["auth", "projectId"],
      auth: CopperAuth,
      required: !1,
      props: async ({ auth, projectId }) => {
        if (!auth || !projectId) return {};
        let project = JSON.parse(projectId);
        return {
          name: import_pieces_framework24.Property.ShortText({
            displayName: "Name",
            description: "The name of the project",
            required: !0,
            defaultValue: project.name
          }),
          details: import_pieces_framework24.Property.ShortText({
            displayName: "Details",
            description: "The details of the project",
            required: !1,
            defaultValue: project.details
          })
        };
      }
    })
  },
  async run(context) {
    let { projectId, updateFields } = context.propsValue, { name, details } = updateFields, project = JSON.parse(projectId);
    return await CopperApiService.updateProject(context.auth, project.id, {
      name,
      details
    });
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/create-task.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework");
var createTask = (0, import_pieces_framework25.createAction)({
  auth: CopperAuth,
  name: "createTask",
  displayName: "Create Task",
  description: "Adds a new task under a person, lead, or opportunity.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new task in Copper CRM with a name and optional details, assignee, due/reminder dates, priority, and tags, optionally linked to a related record (person, company, lead, opportunity, or project). Use to schedule follow-up work; requires a task name. Not idempotent: each call creates a separate task even with identical input.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework25.Property.ShortText({ displayName: "Task Name", required: !0 }),
    details: import_pieces_framework25.Property.ShortText({
      displayName: "Details",
      description: "Details fo this task",
      required: !1
    }),
    custom_activity_type_id: ActivityTypesDropdown("user"),
    assigneeId: usersDropdown({ refreshers: ["auth"] }),
    entity: import_pieces_framework25.Property.StaticDropdown({
      displayName: "Related Record Type",
      description: "Choose the type of Copper record this task should be linked to (e.g. Person, Company, Lead, Opportunity, or Project).",
      required: !1,
      options: {
        options: [
          { label: "Person", value: "person" },
          { label: "Company", value: "company" },
          { label: "Lead", value: "lead" },
          { label: "Opportunity", value: "opportunity" },
          { label: "Project", value: "project" }
        ]
      }
    }),
    entityItemId: import_pieces_framework25.Property.Dropdown({
      auth: CopperAuth,
      displayName: "Related Record",
      description: "Select the specific record (from the chosen type above) that this task should be attached to. For example, pick the Person or Opportunity the task relates to.",
      required: !1,
      refreshers: ["auth", "entity"],
      async options(propsValue) {
        let auth = propsValue.auth, entity = propsValue.entity;
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your Copper account first",
            options: []
          };
        if (!entity)
          return {
            disabled: !0,
            placeholder: "Select a Parent Entity first",
            options: []
          };
        let fetchFn = {
          person: CopperApiService.fetchPeople,
          company: CopperApiService.fetchCompanies,
          lead: CopperApiService.fetchLeads,
          opportunity: CopperApiService.fetchOpportunities,
          project: CopperApiService.fetchProjects
        }[entity];
        try {
          return {
            options: (await fetchFn(auth)).map((item) => {
              var _a, _b;
              return {
                label: (_b = (_a = item.name) != null ? _a : item.title) != null ? _b : `#${item.id}`,
                value: item.id
              };
            })
          };
        } catch (e) {
          return console.error("Failed to fetch entity items", e), {
            options: [],
            placeholder: "Unable to load entity items"
          };
        }
      }
    }),
    due_date: import_pieces_framework25.Property.DateTime({
      displayName: "Due Date/Time",
      required: !1,
      description: "Enter date and time in 24-hour format, e.g. `2025-09-09 11:40` (11:40 AM) or `2025-09-09 13:00` (1:00 PM)."
    }),
    reminder_date: import_pieces_framework25.Property.DateTime({
      displayName: "Reminder Date/Time",
      required: !1,
      description: "Enter date and time in 24-hour format, e.g. `2025-09-09 11:40` (11:40 AM) or `2025-09-09 13:00` (1:00 PM)"
    }),
    priority: import_pieces_framework25.Property.StaticDropdown({
      displayName: "Priority",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "None", value: "None" },
          { label: "Low", value: "Low" },
          { label: "Medium", value: "Medium" },
          { label: "High", value: "High" }
        ]
      }
    }),
    tags: import_pieces_framework25.Property.Array({
      displayName: "Tags",
      required: !1,
      properties: {
        tag: import_pieces_framework25.Property.ShortText({ displayName: "Tag", required: !0 })
      },
      defaultValue: []
    })
  },
  async run(context) {
    let {
      name,
      details,
      custom_activity_type_id,
      assigneeId,
      priority,
      tags,
      due_date,
      reminder_date,
      entity,
      entityItemId
    } = context.propsValue, custom_activity_type = JSON.parse(custom_activity_type_id), tagList = Array.isArray(tags) ? tags.map((t) => {
      var _a;
      return String((_a = t == null ? void 0 : t.tag) != null ? _a : "").trim();
    }).filter(Boolean) : [], payload = __spreadValues(__spreadProps(__spreadValues({
      name,
      details,
      custom_activity_type_id: custom_activity_type.id
    }, assigneeId ? { assignee_id: assigneeId } : {}), {
      due_date: toUnix(due_date),
      reminder_date: toUnix(reminder_date),
      priority,
      tags: tagList
    }), entity && entityItemId && {
      related_resource: {
        type: entity,
        id: entityItemId
      }
    });
    return await CopperApiService.createTask(context.auth, payload);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/create-activity.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework");
var createActivity = (0, import_pieces_framework26.createAction)({
  auth: CopperAuth,
  name: "createActivity",
  displayName: "Create Activity",
  description: "Logs an activity related to CRM entities.",
  audience: "both",
  aiMetadata: {
    description: "Logs an activity (e.g. a note or interaction of a chosen activity type) against a parent Copper record \u2014 a person, company, lead, opportunity, project, or task. Use to record an interaction or note on an existing record; requires the parent entity type, the specific parent record, and an activity type. Not idempotent: each call appends a separate activity entry.",
    idempotent: !1
  },
  props: {
    entity: import_pieces_framework26.Property.StaticDropdown({
      displayName: "Parent Entity",
      description: "Select parent entity",
      required: !0,
      options: {
        options: [
          { label: "Person", value: "person" },
          { label: "Company", value: "company" },
          { label: "Lead", value: "lead" },
          { label: "Opportunity", value: "opportunity" },
          { label: "Project", value: "project" },
          { label: "Task", value: "task" }
        ]
      }
    }),
    entityItemId: import_pieces_framework26.Property.Dropdown({
      auth: CopperAuth,
      displayName: "Parent Entity Resource",
      description: "Select Resource",
      required: !0,
      refreshers: ["auth", "entity"],
      async options(propsValue) {
        let auth = propsValue.auth, entity = propsValue.entity;
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your Copper account first",
            options: []
          };
        if (!entity)
          return {
            disabled: !0,
            placeholder: "Select a Parent Entity first",
            options: []
          };
        let fetchFn = {
          person: CopperApiService.fetchPeople,
          company: CopperApiService.fetchCompanies,
          lead: CopperApiService.fetchLeads,
          opportunity: CopperApiService.fetchOpportunities,
          task: CopperApiService.fetchTasks,
          project: CopperApiService.fetchProjects
        }[entity];
        try {
          return {
            options: (await fetchFn(auth)).map((item) => ({
              label: item.name,
              value: item.id
            }))
          };
        } catch (e) {
          return console.error("Failed to fetch entity items", e), {
            options: [],
            placeholder: "Unable to load entity items"
          };
        }
      }
    }),
    details: import_pieces_framework26.Property.ShortText({
      displayName: "Details",
      description: "The details of the project",
      required: !1
    }),
    type: ActivityTypesDropdown("user")
  },
  async run(context) {
    let { type, entity, entityItemId, details } = context.propsValue, activityType = JSON.parse(type), payload = {
      parent: {
        type: entity,
        id: entityItemId
      },
      type: {
        category: activityType.category,
        id: activityType.id
      },
      details
    };
    return await CopperApiService.createActivity(context.auth, payload);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/search-for-an-activity.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework");
var searchForAnActivity = (0, import_pieces_framework27.createAction)({
  auth: CopperAuth,
  name: "searchForAnActivity",
  displayName: "Search for an Activity",
  description: "Find an existing activity by type/criteria.",
  audience: "both",
  aiMetadata: {
    description: "Searches logged activities in Copper CRM, optionally narrowing by parent record (person, company, lead, opportunity, project, or task), activity types, and an activity-date range, with paging. Use to retrieve activity history; with no filters it returns recent activities across the account. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    entity: import_pieces_framework27.Property.StaticDropdown({
      displayName: "Parent Entity",
      description: "Select parent entity",
      required: !1,
      options: {
        options: [
          { label: "Person", value: "person" },
          { label: "Company", value: "company" },
          { label: "Lead", value: "lead" },
          { label: "Opportunity", value: "opportunity" },
          { label: "Project", value: "project" },
          { label: "Task", value: "task" }
        ]
      }
    }),
    entityItemId: import_pieces_framework27.Property.Dropdown({
      auth: CopperAuth,
      displayName: "Parent Entity Resource",
      description: "Select Resource",
      required: !1,
      refreshers: ["auth", "entity"],
      async options(propsValue) {
        let auth = propsValue.auth, entity = propsValue.entity;
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your Copper account first",
            options: []
          };
        if (!entity)
          return {
            disabled: !0,
            placeholder: "Select a Parent Entity first",
            options: []
          };
        let fetchFn = {
          person: CopperApiService.fetchPeople,
          company: CopperApiService.fetchCompanies,
          lead: CopperApiService.fetchLeads,
          opportunity: CopperApiService.fetchOpportunities,
          task: CopperApiService.fetchTasks,
          project: CopperApiService.fetchProjects
        }[entity];
        try {
          return {
            options: (await fetchFn(auth)).map((item) => ({
              label: item.name,
              value: item.id
            }))
          };
        } catch (e) {
          return console.error("Failed to fetch entity items", e), {
            options: [],
            placeholder: "Unable to load entity items"
          };
        }
      }
    }),
    activity_types: MultiActivityTypesDropdown({}),
    page_size: import_pieces_framework27.Property.Number({
      displayName: "Page Size",
      description: "Default 50. Max 200.",
      required: !1,
      defaultValue: 50
    }),
    page_number: import_pieces_framework27.Property.Number({
      displayName: "Page Number",
      required: !1,
      defaultValue: 1
    }),
    minimum_activity_date: import_pieces_framework27.Property.DateTime({
      displayName: "Minimum Activity Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 11:40. The timestamp of the earliest activity date."
    }),
    maximum_activity_date: import_pieces_framework27.Property.DateTime({
      displayName: "Maximum Activity Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest activity date."
    }),
    full_result: import_pieces_framework27.Property.Checkbox({
      displayName: "Full Result",
      description: "(Optional) If set, search performance improves but duplicate activity logs may be returned",
      required: !1,
      defaultValue: !1
    })
  },
  async run(ctx) {
    let {
      page_size,
      page_number,
      activity_types,
      entity,
      entityItemId,
      minimum_activity_date,
      maximum_activity_date,
      full_result
    } = ctx.propsValue, parsed_activity_types = (activity_types || []).map(
      (activity) => {
        let parsed_activity = JSON.parse(activity);
        return {
          id: parsed_activity.id,
          category: parsed_activity.category
        };
      }
    ), payload = __spreadProps(__spreadValues({}, entity && entityItemId && {
      parent: { id: entityItemId, type: entity }
    }), {
      activity_types: parsed_activity_types,
      page_number,
      page_size,
      minimum_activity_date: toUnix(minimum_activity_date),
      maximum_activity_date: toUnix(maximum_activity_date),
      full_result
    });
    return await CopperApiService.fetchActivities(ctx.auth, payload);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/search-for-a-person.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework");
var searchForAPerson = (0, import_pieces_framework28.createAction)({
  auth: CopperAuth,
  name: "searchForAPerson",
  displayName: "Search for a Person",
  description: "Lookup a person using match criteria.",
  audience: "both",
  aiMetadata: {
    description: "Searches people (contacts) in Copper CRM, filtering by any combination of name, email, phone, contact type, assignee, company, opportunity, location, tags, socials, interaction counts/dates, and creation date, with sorting and paging. Use to find one or more contacts; with no filters it lists people across the account. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework28.Property.ShortText({
      displayName: "Name",
      description: "Full name of the People to search for.",
      required: !1
    }),
    phone_number: import_pieces_framework28.Property.ShortText({
      displayName: "Phone Number",
      description: "Phone Number of the People to search for.",
      required: !1
    }),
    emails: import_pieces_framework28.Property.Array({
      displayName: "Emails",
      required: !1,
      description: "Emails of the People to search for.",
      defaultValue: []
    }),
    contact_type_ids: MultiContactTypesDropdown({}),
    assignee_ids: multiUsersDropdown({ refreshers: ["auth"] }),
    company_ids: multiCompanyDropdown({ refreshers: ["auth"] }),
    opportunity_ids: multiOpportunityDropdown({ refreshers: ["auth"] }),
    city: import_pieces_framework28.Property.ShortText({
      displayName: "City",
      description: "The city in which People must be located.",
      required: !1
    }),
    state: import_pieces_framework28.Property.ShortText({
      displayName: "State",
      description: "The state or province in which People must be located.",
      required: !1
    }),
    postal_code: import_pieces_framework28.Property.ShortText({
      displayName: "Postal Code",
      description: "The postal code in which People must be located.",
      required: !1
    }),
    country: import_pieces_framework28.Property.ShortText({
      displayName: "Country",
      description: "The two character country code where People must be located.",
      required: !1
    }),
    tags: import_pieces_framework28.Property.Array({
      displayName: "Tags",
      description: "Filter People to those that match at least one of the tags specified.",
      required: !1,
      defaultValue: []
    }),
    socials: import_pieces_framework28.Property.Array({
      displayName: "Socials",
      description: "Filter People to those that match at least one of the social accounts specified.",
      required: !1,
      defaultValue: []
    }),
    followed: import_pieces_framework28.Property.StaticDropdown({
      displayName: "Followed",
      description: "Filter by followed state",
      required: !1,
      options: {
        options: [
          {
            label: "followed",
            value: "1"
          },
          {
            label: "not followed",
            value: "2"
          }
        ]
      }
    }),
    age: import_pieces_framework28.Property.Number({
      displayName: "Age",
      description: "The maximum age in seconds that People must be.",
      required: !1
    }),
    page_size: import_pieces_framework28.Property.Number({
      displayName: "Page Size",
      description: "Default 50. Max 200.",
      required: !1,
      defaultValue: 50
    }),
    page_number: import_pieces_framework28.Property.Number({
      displayName: "Page Number",
      required: !1,
      defaultValue: 1
    }),
    sort_by: import_pieces_framework28.Property.StaticDropdown({
      displayName: "Sort By",
      description: "The field on which to sort the results",
      required: !1,
      options: {
        options: [
          {
            label: "Name",
            value: "name"
          },
          {
            label: "Title",
            value: "title"
          },
          {
            label: "Email",
            value: "email"
          },
          {
            label: "Phone",
            value: "phone"
          },
          {
            label: "Date Modified",
            value: "date_modified"
          },
          {
            label: "Date Created",
            value: "date_created"
          },
          {
            label: "City",
            value: "city"
          },
          {
            label: "State",
            value: "state"
          },
          {
            label: "Country",
            value: "country"
          },
          {
            label: "Zip",
            value: "zip"
          },
          {
            label: "Socials",
            value: "socials"
          }
        ]
      }
    }),
    sort_direction: import_pieces_framework28.Property.StaticDropdown({
      displayName: "Sort Direction",
      description: "The direction in which to sort the result",
      required: !1,
      options: {
        options: [
          {
            label: "Ascending",
            value: "asc"
          },
          {
            label: "Descending",
            value: "desc"
          }
        ]
      }
    }),
    minimum_interaction_count: import_pieces_framework28.Property.Number({
      displayName: "Minimum Interaction Count",
      required: !1,
      description: "The minimum number of interactions People must have had."
    }),
    maximum_interaction_count: import_pieces_framework28.Property.Number({
      displayName: "Maximum Interaction Count",
      required: !1,
      description: "The maximum number of interactions People must have had."
    }),
    minimum_interaction_date: import_pieces_framework28.Property.DateTime({
      displayName: "Minimum Interaction Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date of the last interaction."
    }),
    maximum_interaction_date: import_pieces_framework28.Property.DateTime({
      displayName: "Maximum Interaction Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date of the last interaction."
    }),
    minimum_created_date: import_pieces_framework28.Property.DateTime({
      displayName: "Minimum Created Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date People are created."
    }),
    maximum_created_date: import_pieces_framework28.Property.DateTime({
      displayName: "Maximum Created Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date People are Created."
    })
  },
  async run(context) {
    let {
      name,
      phone_number,
      emails,
      contact_type_ids,
      assignee_ids,
      company_ids,
      opportunity_ids,
      city,
      state,
      postal_code,
      country,
      tags,
      socials,
      followed,
      age,
      page_size,
      page_number,
      sort_by,
      sort_direction,
      minimum_interaction_count,
      maximum_interaction_count,
      minimum_interaction_date,
      maximum_interaction_date,
      minimum_created_date,
      maximum_created_date
    } = context.propsValue, payload = {
      name,
      phone_number,
      emails,
      contact_type_ids,
      assignee_ids,
      company_ids,
      opportunity_ids,
      city,
      state,
      postal_code,
      country,
      tags,
      socials,
      followed,
      age,
      page_size,
      page_number,
      sort_by,
      sort_direction,
      minimum_interaction_count,
      maximum_interaction_count,
      minimum_interaction_date: toUnix(minimum_interaction_date),
      maximum_interaction_date: toUnix(maximum_interaction_date),
      minimum_created_date: toUnix(minimum_created_date),
      maximum_created_date: toUnix(maximum_created_date)
    };
    return await CopperApiService.fetchPeople(context.auth, payload);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/search-for-a-lead.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework");
var searchForALead = (0, import_pieces_framework29.createAction)({
  auth: CopperAuth,
  name: "searchForALead",
  displayName: "Search for a Lead",
  description: "Lookup a lead using match criteria.",
  audience: "both",
  aiMetadata: {
    description: "Searches leads in Copper CRM, filtering by any combination of name, email, phone, assignee, status, customer source, location, tags, socials, monetary value, interaction counts/dates, and creation/modification dates, with sorting and paging; can optionally include already-converted leads. Use to find one or more leads; with no filters it lists leads across the account. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework29.Property.ShortText({
      displayName: "Name",
      description: "Full name of the Lead to search for.",
      required: !1
    }),
    phone_number: import_pieces_framework29.Property.ShortText({
      displayName: "Phone Number",
      description: "Phone Number of the Lead to search for.",
      required: !1
    }),
    emails: import_pieces_framework29.Property.ShortText({
      displayName: "Emails",
      required: !1,
      description: "Emails of the Lead to search for.	"
    }),
    assignee_ids: multiUsersDropdown({ refreshers: ["auth"] }),
    status_ids: MultiLeadStatusDropdown({}),
    customer_source_ids: MultiCustomerSourceDropdown({}),
    city: import_pieces_framework29.Property.ShortText({
      displayName: "City",
      description: "The city in which Lead must be located.",
      required: !1
    }),
    state: import_pieces_framework29.Property.ShortText({
      displayName: "State",
      description: "The state or province in which Lead must be located.",
      required: !1
    }),
    postal_code: import_pieces_framework29.Property.ShortText({
      displayName: "Postal Code",
      description: "The postal code in which Lead must be located.",
      required: !1
    }),
    country: import_pieces_framework29.Property.ShortText({
      displayName: "Country",
      description: "The two character country code where Lead must be located.",
      required: !1
    }),
    tags: import_pieces_framework29.Property.Array({
      displayName: "Tags",
      description: "Filter Lead to those that match at least one of the tags specified.",
      required: !1,
      defaultValue: []
    }),
    socials: import_pieces_framework29.Property.Array({
      displayName: "Socials",
      description: "Filter Lead to those that match at least one of the social accounts specified.",
      required: !1,
      defaultValue: []
    }),
    followed: import_pieces_framework29.Property.StaticDropdown({
      displayName: "Followed",
      description: "Filter by followed state",
      required: !1,
      options: {
        options: [
          {
            label: "followed",
            value: "1"
          },
          {
            label: "not followed",
            value: "2"
          }
        ]
      }
    }),
    age: import_pieces_framework29.Property.Number({
      displayName: "Age",
      description: "The maximum age in seconds that Lead must be.",
      required: !1
    }),
    page_size: import_pieces_framework29.Property.Number({
      displayName: "Page Size",
      description: "Default 50. Max 200.",
      required: !1,
      defaultValue: 50
    }),
    page_number: import_pieces_framework29.Property.Number({
      displayName: "Page Number",
      required: !1,
      defaultValue: 1
    }),
    sort_by: import_pieces_framework29.Property.StaticDropdown({
      displayName: "Sort By",
      description: "The field on which to sort the results",
      required: !1,
      options: {
        options: [
          {
            label: "Name",
            value: "name"
          },
          {
            label: "Company Name",
            value: "company_name"
          },
          {
            label: "Title",
            value: "title"
          },
          {
            label: "Value",
            value: "value"
          },
          {
            label: "Email",
            value: "email"
          },
          {
            label: "Phone",
            value: "phone"
          },
          {
            label: "Date Modified",
            value: "date_modified"
          },
          {
            label: "Date Created",
            value: "date_created"
          },
          {
            label: "City",
            value: "city"
          },
          {
            label: "State",
            value: "state"
          },
          {
            label: "Country",
            value: "country"
          },
          {
            label: "Zip",
            value: "zip"
          },
          {
            label: "Inactive Days",
            value: "inactive_days"
          },
          {
            label: "Socials",
            value: "socials"
          }
        ]
      }
    }),
    sort_direction: import_pieces_framework29.Property.StaticDropdown({
      displayName: "Sort Direction",
      description: "The direction in which to sort the result",
      required: !1,
      options: {
        options: [
          {
            label: "Ascending",
            value: "asc"
          },
          {
            label: "Descending",
            value: "desc"
          }
        ]
      }
    }),
    include_converted_leads: import_pieces_framework29.Property.Checkbox({
      displayName: "Include Converted Leads",
      description: "Specify if response should contain converted leads.",
      required: !1,
      defaultValue: !1
    }),
    minimum_monetary_value: import_pieces_framework29.Property.Number({
      displayName: "Minimum Monetary Value",
      required: !1,
      description: "The minimum monetary value Leads must have."
    }),
    maximum_monetary_value: import_pieces_framework29.Property.Number({
      displayName: "Maximum Monetary Value",
      required: !1,
      description: "The maximum monetary value Leads must have."
    }),
    minimum_interaction_count: import_pieces_framework29.Property.Number({
      displayName: "Minimum Interaction Count",
      required: !1,
      description: "The minimum number of interactions Lead must have had."
    }),
    maximum_interaction_count: import_pieces_framework29.Property.Number({
      displayName: "Maximum Interaction Count",
      required: !1,
      description: "The maximum number of interactions Lead must have had."
    }),
    minimum_interaction_date: import_pieces_framework29.Property.DateTime({
      displayName: "Minimum Interaction Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date of the last interaction."
    }),
    maximum_interaction_date: import_pieces_framework29.Property.DateTime({
      displayName: "Maximum Interaction Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date of the last interaction."
    }),
    minimum_created_date: import_pieces_framework29.Property.DateTime({
      displayName: "Minimum Created Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date Lead are created."
    }),
    maximum_created_date: import_pieces_framework29.Property.DateTime({
      displayName: "Maximum Created Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date Lead are Created."
    }),
    minimum_modified_date: import_pieces_framework29.Property.DateTime({
      displayName: "Minimum Modified Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date Lead are Modified."
    }),
    maximum_modified_date: import_pieces_framework29.Property.DateTime({
      displayName: "Maximum Modified Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date Lead are Modified."
    })
  },
  async run(context) {
    let {
      name,
      phone_number,
      emails,
      assignee_ids,
      status_ids,
      customer_source_ids,
      city,
      state,
      postal_code,
      country,
      tags,
      socials,
      followed,
      age,
      page_size,
      page_number,
      sort_by,
      sort_direction,
      minimum_interaction_count,
      maximum_interaction_count,
      minimum_interaction_date,
      maximum_interaction_date,
      minimum_created_date,
      maximum_created_date,
      minimum_monetary_value,
      maximum_monetary_value,
      minimum_modified_date,
      maximum_modified_date
    } = context.propsValue, payload = {
      name,
      phone_number,
      emails,
      assignee_ids,
      status_ids,
      customer_source_ids,
      city,
      state,
      postal_code,
      country,
      tags,
      socials,
      followed,
      age,
      page_size,
      page_number,
      sort_by,
      sort_direction,
      minimum_interaction_count,
      maximum_interaction_count,
      minimum_interaction_date: toUnix(minimum_interaction_date),
      maximum_interaction_date: toUnix(maximum_interaction_date),
      minimum_created_date: toUnix(minimum_created_date),
      maximum_created_date: toUnix(maximum_created_date),
      minimum_monetary_value,
      maximum_monetary_value,
      minimum_modified_date: toUnix(minimum_modified_date),
      maximum_modified_date: toUnix(maximum_modified_date)
    };
    return await CopperApiService.fetchLeads(context.auth, payload);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/search-for-a-company.ts
var import_pieces_framework30 = require("@activepieces/pieces-framework");
var searchForACompany = (0, import_pieces_framework30.createAction)({
  auth: CopperAuth,
  name: "searchForACompany",
  displayName: "Search for a Company",
  description: "Lookup a company.",
  audience: "both",
  aiMetadata: {
    description: "Searches companies in Copper CRM, filtering by any combination of name, email domain, phone, contact type, assignee, location, tags, socials, interaction counts/dates, and creation date, with sorting and paging. Use to find one or more companies; with no filters it lists companies across the account. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework30.Property.ShortText({
      displayName: "Name",
      description: "Full name of the Company to search for.",
      required: !1
    }),
    phone_number: import_pieces_framework30.Property.ShortText({
      displayName: "Phone Number",
      description: "Phone Number of the Company to search for.",
      required: !1
    }),
    email_domains: import_pieces_framework30.Property.ShortText({
      displayName: "Email Domain",
      required: !1,
      description: "Email Domain of the Company to search for."
    }),
    contact_type_ids: MultiContactTypesDropdown({}),
    assignee_ids: multiUsersDropdown({ refreshers: ["auth"] }),
    city: import_pieces_framework30.Property.ShortText({
      displayName: "City",
      description: "The city in which Company must be located.",
      required: !1
    }),
    state: import_pieces_framework30.Property.ShortText({
      displayName: "State",
      description: "The state or province in which Company must be located.",
      required: !1
    }),
    postal_code: import_pieces_framework30.Property.ShortText({
      displayName: "Postal Code",
      description: "The postal code in which Company must be located.",
      required: !1
    }),
    country: import_pieces_framework30.Property.ShortText({
      displayName: "Country",
      description: "The two character country code where Company must be located.",
      required: !1
    }),
    tags: import_pieces_framework30.Property.Array({
      displayName: "Tags",
      description: "Filter Company to those that match at least one of the tags specified.",
      required: !1,
      defaultValue: []
    }),
    socials: import_pieces_framework30.Property.Array({
      displayName: "Socials",
      description: "Filter Company to those that match at least one of the social accounts specified.",
      required: !1,
      defaultValue: []
    }),
    followed: import_pieces_framework30.Property.StaticDropdown({
      displayName: "Followed",
      description: "Filter by followed state",
      required: !1,
      options: {
        options: [
          {
            label: "followed",
            value: "1"
          },
          {
            label: "not followed",
            value: "2"
          }
        ]
      }
    }),
    age: import_pieces_framework30.Property.Number({
      displayName: "Age",
      description: "The maximum age in seconds that Company must be.",
      required: !1
    }),
    page_size: import_pieces_framework30.Property.Number({
      displayName: "Page Size",
      description: "Default 50. Max 200.",
      required: !1,
      defaultValue: 50
    }),
    page_number: import_pieces_framework30.Property.Number({
      displayName: "Page Number",
      required: !1,
      defaultValue: 1
    }),
    sort_by: import_pieces_framework30.Property.StaticDropdown({
      displayName: "Sort By",
      description: "The field on which to sort the results",
      required: !1,
      options: {
        options: [
          {
            label: "Name",
            value: "name"
          },
          {
            label: "Phone",
            value: "phone"
          },
          {
            label: "Contact",
            value: "contact"
          },
          {
            label: "Contact First Name",
            value: "contact_first_name"
          },
          {
            label: "Contact Last Name",
            value: "contact_last_name"
          },
          {
            label: "Date Modified",
            value: "date_modified"
          },
          {
            label: "Date Created",
            value: "date_created"
          },
          {
            label: "Email Domain",
            value: "email_domain"
          },
          {
            label: "City",
            value: "city"
          },
          {
            label: "State",
            value: "state"
          },
          {
            label: "Country",
            value: "country"
          },
          {
            label: "Zip",
            value: "zip"
          },
          {
            label: "Assignee",
            value: "assignee"
          },
          {
            label: "Contact Group",
            value: "contact_group"
          },
          {
            label: "Last Interaction",
            value: "last_interaction"
          },
          {
            label: "Interaction Count",
            value: "interaction_count"
          },
          {
            label: "Primary Website",
            value: "primary_website"
          },
          {
            label: "Socials",
            value: "socials"
          }
        ]
      }
    }),
    sort_direction: import_pieces_framework30.Property.StaticDropdown({
      displayName: "Sort Direction",
      description: "The direction in which to sort the result",
      required: !1,
      options: {
        options: [
          {
            label: "Ascending",
            value: "asc"
          },
          {
            label: "Descending",
            value: "desc"
          }
        ]
      }
    }),
    minimum_interaction_count: import_pieces_framework30.Property.Number({
      displayName: "Minimum Interaction Count",
      required: !1,
      description: "The minimum number of interactions Company must have had."
    }),
    maximum_interaction_count: import_pieces_framework30.Property.Number({
      displayName: "Maximum Interaction Count",
      required: !1,
      description: "The maximum number of interactions Company must have had."
    }),
    minimum_interaction_date: import_pieces_framework30.Property.DateTime({
      displayName: "Minimum Interaction Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date of the last interaction."
    }),
    maximum_interaction_date: import_pieces_framework30.Property.DateTime({
      displayName: "Maximum Interaction Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date of the last interaction."
    }),
    minimum_created_date: import_pieces_framework30.Property.DateTime({
      displayName: "Minimum Created Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date Company are created."
    }),
    maximum_created_date: import_pieces_framework30.Property.DateTime({
      displayName: "Maximum Created Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date Company are Created."
    })
  },
  async run(context) {
    let {
      name,
      phone_number,
      email_domains,
      contact_type_ids,
      assignee_ids,
      city,
      state,
      postal_code,
      country,
      tags,
      socials,
      followed,
      age,
      page_size,
      page_number,
      sort_by,
      sort_direction,
      minimum_interaction_count,
      maximum_interaction_count,
      minimum_interaction_date,
      maximum_interaction_date,
      minimum_created_date,
      maximum_created_date
    } = context.propsValue, payload = {
      name,
      phone_number,
      email_domains,
      contact_type_ids,
      assignee_ids,
      city,
      state,
      postal_code,
      country,
      tags,
      socials,
      followed,
      age,
      page_size,
      page_number,
      sort_by,
      sort_direction,
      minimum_interaction_count,
      maximum_interaction_count,
      minimum_interaction_date: toUnix(minimum_interaction_date),
      maximum_interaction_date: toUnix(maximum_interaction_date),
      minimum_created_date: toUnix(minimum_created_date),
      maximum_created_date: toUnix(maximum_created_date)
    };
    return await CopperApiService.fetchCompanies(context.auth, payload);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/search-for-an-opportunity.ts
var import_pieces_framework31 = require("@activepieces/pieces-framework");
var searchForAnOpportunity = (0, import_pieces_framework31.createAction)({
  auth: CopperAuth,
  name: "searchForAnOpportunity",
  displayName: "Search for an Opportunity",
  description: "Lookup an opportunity.",
  audience: "both",
  aiMetadata: {
    description: "Searches opportunities (deals) in Copper CRM, filtering by any combination of name, assignee, status, priority, pipeline and stage, company, primary contact, customer source, loss reason, tags, monetary value, interaction counts/dates, close/stage-change dates, and creation/modification dates, with sorting and paging. Use to find one or more deals; with no filters it lists opportunities across the account. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework31.Property.ShortText({
      displayName: "Name",
      description: "Full name of the Opportunity to search for.",
      required: !1
    }),
    assignee_ids: multiUsersDropdown({ refreshers: ["auth"] }),
    company_ids: multiCompanyDropdown({ refreshers: ["auth"] }),
    status_ids: import_pieces_framework31.Property.StaticMultiSelectDropdown({
      displayName: "Status",
      description: "Filter by Opportunity status",
      required: !1,
      options: {
        options: [
          {
            label: "Open",
            value: "0"
          },
          {
            label: "Won",
            value: "1"
          },
          {
            label: "Lost",
            value: "2"
          },
          {
            label: "Abandoned",
            value: "3"
          }
        ]
      }
    }),
    priorities: import_pieces_framework31.Property.StaticMultiSelectDropdown({
      displayName: "Priority",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "None", value: "None" },
          { label: "Low", value: "Low" },
          { label: "Medium", value: "Medium" },
          { label: "High", value: "High" }
        ]
      }
    }),
    pipeline_ids: multiPipelinesDropdown({ refreshers: ["auth"] }),
    pipeline_stage_ids: import_pieces_framework31.Property.MultiSelectDropdown({
      auth: CopperAuth,
      displayName: "Pipeline Stage",
      description: "Select a stage",
      refreshers: ["auth", "pipeline_ids"],
      required: !1,
      async options({ auth, pipeline_ids }) {
        return auth ? pipeline_ids ? {
          options: pipeline_ids.flatMap((pipeline) => {
            var _a;
            return (_a = JSON.parse(pipeline).stages) != null ? _a : [];
          }).map((stage) => ({
            label: stage.name,
            value: stage.id
          }))
        } : {
          disabled: !0,
          placeholder: "Select a pipeline first",
          options: []
        } : {
          disabled: !0,
          placeholder: "Connect your Copper account first",
          options: []
        };
      }
    }),
    primary_contact_ids: multiPrimaryContactsDropdown({ refreshers: ["auth"] }),
    customer_source_ids: MultiCustomerSourceDropdown({}),
    loss_reason_ids: MultiLossReasonsDropdown({}),
    tags: import_pieces_framework31.Property.Array({
      displayName: "Tags",
      description: "Filter People to those that match at least one of the tags specified.",
      required: !1,
      defaultValue: []
    }),
    followed: import_pieces_framework31.Property.StaticDropdown({
      displayName: "Followed",
      description: "Filter by followed state",
      required: !1,
      options: {
        options: [
          {
            label: "followed",
            value: "1"
          },
          {
            label: "not followed",
            value: "2"
          }
        ]
      }
    }),
    page_size: import_pieces_framework31.Property.Number({
      displayName: "Page Size",
      description: "Default 50. Max 200.",
      required: !1,
      defaultValue: 50
    }),
    page_number: import_pieces_framework31.Property.Number({
      displayName: "Page Number",
      required: !1,
      defaultValue: 1
    }),
    sort_by: import_pieces_framework31.Property.StaticDropdown({
      displayName: "Sort By",
      description: "The field on which to sort the results",
      required: !1,
      options: {
        options: [
          {
            label: "Assignee",
            value: "assignee"
          },
          {
            label: "Company Name",
            value: "company_name"
          },
          {
            label: "Customer Source ID",
            value: "customer_source_id"
          },
          {
            label: "Date Created",
            value: "date_created"
          },
          {
            label: "Date Modified",
            value: "date_modified"
          },
          {
            label: "Inactive Days",
            value: "inactive_days"
          },
          {
            label: "Interaction Count",
            value: "interaction_count"
          },
          {
            label: "Last Interaction",
            value: "last_interaction"
          },
          {
            label: "Monetary Unit",
            value: "monetary_unit"
          },
          {
            label: "Monetary Value",
            value: "monetary_value"
          },
          {
            label: "Name",
            value: "name"
          },
          {
            label: "Primary Contact",
            value: "primary_contact"
          },
          {
            label: "Priority",
            value: "priority"
          },
          {
            label: "Stage",
            value: "stage"
          },
          {
            label: "Status",
            value: "status"
          }
        ]
      }
    }),
    sort_direction: import_pieces_framework31.Property.StaticDropdown({
      displayName: "Sort Direction",
      description: "The direction in which to sort the result",
      required: !1,
      options: {
        options: [
          {
            label: "Ascending",
            value: "asc"
          },
          {
            label: "Descending",
            value: "desc"
          }
        ]
      }
    }),
    minimum_monetary_value: import_pieces_framework31.Property.Number({
      displayName: "Minimum Monetary Value",
      required: !1,
      description: "The minimum monetary value Opportunities must have."
    }),
    maximum_monetary_value: import_pieces_framework31.Property.Number({
      displayName: "Maximum Monetary Value",
      required: !1,
      description: "The maximum monetary value Opportunities must have."
    }),
    minimum_interaction_count: import_pieces_framework31.Property.Number({
      displayName: "Minimum Interaction Count",
      required: !1,
      description: "The minimum number of interactions Opportunity must have had."
    }),
    maximum_interaction_count: import_pieces_framework31.Property.Number({
      displayName: "Maximum Interaction Count",
      required: !1,
      description: "The maximum number of interactions Opportunity must have had."
    }),
    minimum_close_date: import_pieces_framework31.Property.DateTime({
      displayName: "Minimum Interaction Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date close date."
    }),
    maximum_close_date: import_pieces_framework31.Property.DateTime({
      displayName: "Maximum Interaction Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date close date."
    }),
    minimum_interaction_date: import_pieces_framework31.Property.DateTime({
      displayName: "Minimum Interaction Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date of the last interaction."
    }),
    maximum_interaction_date: import_pieces_framework31.Property.DateTime({
      displayName: "Maximum Interaction Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date of the last interaction."
    }),
    minimum_stage_change_date: import_pieces_framework31.Property.DateTime({
      displayName: "Minimum Stage Change Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date of a stage change."
    }),
    maximum_stage_change_date: import_pieces_framework31.Property.DateTime({
      displayName: "Maximum Stage Change Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date of a stage change."
    }),
    minimum_created_date: import_pieces_framework31.Property.DateTime({
      displayName: "Minimum Created Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date Opportunity are created."
    }),
    maximum_created_date: import_pieces_framework31.Property.DateTime({
      displayName: "Maximum Created Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date Opportunity are Created."
    }),
    minimum_modified_date: import_pieces_framework31.Property.DateTime({
      displayName: "Minimum Modified Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date Opportunity are Modified."
    }),
    maximum_modified_date: import_pieces_framework31.Property.DateTime({
      displayName: "Maximum Modified Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date Opportunity are Modified."
    })
  },
  async run(context) {
    let {
      name,
      assignee_ids,
      status_ids,
      customer_source_ids,
      pipeline_ids,
      pipeline_stage_ids,
      company_ids,
      tags,
      followed,
      page_size,
      page_number,
      sort_by,
      sort_direction,
      priorities,
      minimum_interaction_count,
      maximum_interaction_count,
      minimum_interaction_date,
      maximum_interaction_date,
      minimum_created_date,
      maximum_created_date,
      minimum_monetary_value,
      maximum_monetary_value,
      minimum_modified_date,
      maximum_modified_date,
      minimum_close_date,
      minimum_stage_change_date,
      maximum_close_date,
      maximum_stage_change_date,
      primary_contact_ids,
      loss_reason_ids
    } = context.propsValue, payload = {
      name,
      assignee_ids,
      status_ids,
      customer_source_ids,
      pipeline_ids: (pipeline_ids != null ? pipeline_ids : []).flatMap((pipeline) => {
        var _a;
        return (_a = JSON.parse(pipeline).id) != null ? _a : [];
      }),
      pipeline_stage_ids,
      company_ids,
      primary_contact_ids,
      loss_reason_ids,
      tags,
      priorities,
      followed,
      page_size,
      page_number,
      sort_by,
      sort_direction,
      minimum_interaction_count,
      maximum_interaction_count,
      minimum_interaction_date: toUnix(minimum_interaction_date),
      maximum_interaction_date: toUnix(maximum_interaction_date),
      minimum_created_date: toUnix(minimum_created_date),
      maximum_created_date: toUnix(maximum_created_date),
      minimum_monetary_value,
      maximum_monetary_value,
      minimum_modified_date: toUnix(minimum_modified_date),
      maximum_modified_date: toUnix(maximum_modified_date),
      minimum_close_date: toUnix(minimum_close_date),
      minimum_stage_change_date: toUnix(minimum_stage_change_date),
      maximum_close_date: toUnix(maximum_close_date),
      maximum_stage_change_date: toUnix(maximum_stage_change_date)
    };
    return await CopperApiService.fetchOpportunities(context.auth, payload);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/actions/search-for-a-project.ts
var import_pieces_framework32 = require("@activepieces/pieces-framework");
var searchForAProject = (0, import_pieces_framework32.createAction)({
  auth: CopperAuth,
  name: "searchForAProject",
  displayName: "Search for a Project",
  description: "Lookup a project.",
  audience: "both",
  aiMetadata: {
    description: "Searches projects in Copper CRM, filtering by any combination of name, assignee, status (Open/Completed), tags, followed state, and creation/modification dates, with sorting and paging. Use to find one or more projects; with no filters it lists projects across the account. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework32.Property.ShortText({
      displayName: "Name",
      description: "Full name of the Opportunity to search for.",
      required: !1
    }),
    assignee_ids: multiUsersDropdown({ refreshers: ["auth"] }),
    statuses: import_pieces_framework32.Property.StaticMultiSelectDropdown({
      displayName: "Status",
      description: "Filter by Opportunity status",
      required: !1,
      options: {
        options: [
          {
            label: "Open",
            value: "Open"
          },
          {
            label: "Completed",
            value: "Completed"
          }
        ]
      }
    }),
    tags: import_pieces_framework32.Property.Array({
      displayName: "Tags",
      description: "Filter People to those that match at least one of the tags specified.",
      required: !1,
      defaultValue: []
    }),
    followed: import_pieces_framework32.Property.StaticDropdown({
      displayName: "Followed",
      description: "Filter by followed state",
      required: !1,
      options: {
        options: [
          {
            label: "followed",
            value: "1"
          },
          {
            label: "not followed",
            value: "2"
          }
        ]
      }
    }),
    page_size: import_pieces_framework32.Property.Number({
      displayName: "Page Size",
      description: "Default 50. Max 200.",
      required: !1,
      defaultValue: 50
    }),
    page_number: import_pieces_framework32.Property.Number({
      displayName: "Page Number",
      required: !1,
      defaultValue: 1
    }),
    sort_by: import_pieces_framework32.Property.StaticDropdown({
      displayName: "Sort By",
      description: "The field on which to sort the results",
      required: !1,
      options: {
        options: [
          {
            label: "Name",
            value: "name"
          },
          {
            label: "Assigned To",
            value: "assigned_to"
          },
          {
            label: "Related To",
            value: "related_to"
          },
          {
            label: "Status",
            value: "status"
          },
          {
            label: "Date Modified",
            value: "date_modified"
          },
          {
            label: "Date Created",
            value: "date_created"
          }
        ]
      }
    }),
    sort_direction: import_pieces_framework32.Property.StaticDropdown({
      displayName: "Sort Direction",
      description: "The direction in which to sort the result",
      required: !1,
      options: {
        options: [
          {
            label: "Ascending",
            value: "asc"
          },
          {
            label: "Descending",
            value: "desc"
          }
        ]
      }
    }),
    minimum_created_date: import_pieces_framework32.Property.DateTime({
      displayName: "Minimum Created Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date Opportunity are created."
    }),
    maximum_created_date: import_pieces_framework32.Property.DateTime({
      displayName: "Maximum Created Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date Opportunity are Created."
    }),
    minimum_modified_date: import_pieces_framework32.Property.DateTime({
      displayName: "Minimum Modified Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the earliest date Opportunity are Modified."
    }),
    maximum_modified_date: import_pieces_framework32.Property.DateTime({
      displayName: "Maximum Modified Date",
      required: !1,
      description: "24-hour format, e.g. 2025-09-10 13:00. The timestamp of the latest date Opportunity are Modified."
    })
  },
  async run(context) {
    let {
      name,
      assignee_ids,
      statuses,
      tags,
      followed,
      page_size,
      page_number,
      sort_by,
      sort_direction,
      minimum_created_date,
      maximum_created_date,
      minimum_modified_date,
      maximum_modified_date
    } = context.propsValue, payload = {
      name,
      assignee_ids: assignee_ids || [],
      statuses,
      followed,
      page_size,
      page_number,
      sort_by,
      tags,
      sort_direction,
      minimum_created_date: toUnix(minimum_created_date),
      maximum_created_date: toUnix(maximum_created_date),
      minimum_modified_date: toUnix(minimum_modified_date),
      maximum_modified_date: toUnix(maximum_modified_date)
    };
    return await CopperApiService.fetchProjects(context.auth, payload);
  }
});

// ../ap/packages/pieces/community/copper/src/lib/triggers/new-activity.ts
var import_pieces_framework33 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common2.DedupeStrategy.TIMEBASED,
  items: async ({ auth, lastFetchEpochMS }) => {
    let minCreatedUnix = lastFetchEpochMS != null ? Math.max(0, Math.floor(lastFetchEpochMS / 1e3) - 1) : void 0, collected = [], pageSize = 100, page = 1, hasMore = !0;
    for (; hasMore; ) {
      let batch = await CopperApiService.fetchActivities(auth, {
        minimum_activity_date: minCreatedUnix,
        page_size: pageSize,
        page_number: page
      });
      if (!batch.length) break;
      collected.push(...batch), batch.length < pageSize && (hasMore = !1), page += 1;
    }
    let out = collected.map((a) => {
      var _a;
      return {
        epochMilliSeconds: ((_a = a.activity_date) != null ? _a : 0) * 1e3,
        data: a
      };
    });
    return out.sort((a, b) => a.epochMilliSeconds - b.epochMilliSeconds), out;
  }
}, newActivity = (0, import_pieces_framework33.createTrigger)({
  auth: CopperAuth,
  name: "newActivity",
  displayName: "New Activity",
  description: "Triggers when a new activity is logged",
  aiMetadata: {
    description: "Fires when a new activity (note or logged interaction) is recorded in Copper CRM. Polls for activities by creation date, emitting one event per newly logged activity."
  },
  props: {},
  sampleData: {},
  type: import_pieces_framework33.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common2.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common2.pollingHelper.onEnable(polling, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common2.pollingHelper.onDisable(polling, { store, auth, propsValue });
  },
  async run(context) {
    return await import_pieces_common2.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/copper/src/index.ts
var import_pieces_framework35 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common"), copper = (0, import_pieces_framework34.createPiece)({
  displayName: "Copper",
  auth: CopperAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/copper.png",
  authors: ["gs03-dev"],
  categories: [import_pieces_framework35.PieceCategory.SALES_AND_CRM, import_pieces_framework35.PieceCategory.PRODUCTIVITY],
  actions: [
    createPerson,
    updatePerson,
    createLead,
    updateLead,
    convertLead,
    createCompany,
    updateCompany,
    createOpportunity,
    updateOpportunity,
    createProject,
    updateProject,
    createTask,
    createActivity,
    searchForAnActivity,
    searchForAPerson,
    searchForALead,
    searchForACompany,
    searchForAnOpportunity,
    searchForAProject,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: CopperAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        Accept: "application/json",
        "Content-Type": "application/json",
        "X-PW-AccessToken": auth.props.apiKey,
        "X-PW-Application": "developer_api",
        "X-PW-UserEmail": auth.props.email
      })
    })
  ],
  triggers: [
    newActivity,
    newPerson,
    newLead,
    newTask,
    updatedLead,
    updatedTask,
    updatedOpportunity,
    updatedOpportunityStage,
    updatedOpportunityStatus,
    updatedProject,
    updatedLeadStatus
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  copper
});
