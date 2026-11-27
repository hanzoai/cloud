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

// ../ap/packages/pieces/community/folk/src/index.ts
var index_exports = {};
__export(index_exports, {
  folk: () => folk
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_framework21 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/folk/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/folk/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var folkClient = {
  baseUrl: "https://api.folk.app",
  async testConnection(apiKey) {
    await this.makeRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: "/v1/people?limit=1",
      apiKey
    });
  },
  async makeRequest({
    method,
    url,
    apiKey,
    body,
    headers
  }) {
    let response = await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${this.baseUrl}${url}`,
      headers: __spreadValues({
        Authorization: `Bearer ${apiKey.secret_text}`,
        "Content-Type": "application/json"
      }, headers),
      body
    });
    if (response.status >= 400)
      throw new Error(
        `Folk API error: ${response.status} ${JSON.stringify(response.body)}`
      );
    return response.body;
  },
  async getPeople({
    apiKey,
    limit = 100,
    offset = 0
  }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `/v1/people/list?limit=${limit}&offset=${offset}`,
      apiKey
    });
  },
  async getPeopleWithFilters({
    apiKey,
    limit = 20,
    cursor,
    combinator = "and",
    nameFilter,
    emailFilter
  }) {
    let params = new URLSearchParams();
    limit && params.append("limit", limit.toString()), cursor && params.append("cursor", cursor), (nameFilter || emailFilter) && (combinator && params.append("combinator", combinator), nameFilter && params.append("filter[fullName][like]", nameFilter), emailFilter && params.append("filter[emails][like]", emailFilter));
    let queryString = params.toString(), url = queryString ? `/v1/people?${queryString}` : "/v1/people";
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.GET,
      url,
      apiKey
    });
  },
  async getPerson({
    apiKey,
    contactId
  }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `/v1/people/${contactId}`,
      apiKey
    });
  },
  async searchPerson({ apiKey, query }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `/v1/people/search?query=${encodeURIComponent(query)}`,
      apiKey
    });
  },
  async createPerson({ apiKey, data }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "/v1/people",
      apiKey,
      body: data
    });
  },
  async updatePerson({
    apiKey,
    contactId,
    data
  }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.PATCH,
      url: `/v1/people/${contactId}`,
      apiKey,
      body: data
    });
  },
  async getCompanies({
    apiKey,
    limit = 100,
    offset = 0
  }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `/v1/companies/list?limit=${limit}&offset=${offset}`,
      apiKey
    });
  },
  async getCompaniesWithFilters({
    apiKey,
    limit = 20,
    cursor,
    combinator = "and",
    nameFilter
  }) {
    let params = new URLSearchParams();
    limit && params.append("limit", limit.toString()), cursor && params.append("cursor", cursor), nameFilter && (combinator && params.append("combinator", combinator), params.append("filter[name][like]", nameFilter));
    let queryString = params.toString(), url = queryString ? `/v1/companies?${queryString}` : "/v1/companies";
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.GET,
      url,
      apiKey
    });
  },
  async getCompany({
    apiKey,
    companyId
  }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `/v1/companies/${companyId}`,
      apiKey
    });
  },
  async searchCompany({ apiKey, query }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `/v1/companies?limit=100&filter[name][like]=${encodeURIComponent(
        query
      )}`,
      apiKey
    });
  },
  async createCompany({ apiKey, data }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "/v1/companies",
      apiKey,
      body: data
    });
  },
  async updateCompany({
    apiKey,
    companyId,
    data
  }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.PATCH,
      url: `/v1/companies/${companyId}`,
      apiKey,
      body: data
    });
  },
  async getGroups({ apiKey }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: "/v1/groups",
      apiKey
    });
  },
  async createWebhook({
    apiKey,
    name,
    targetUrl,
    subscribedEvents
  }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "/v1/webhooks",
      apiKey,
      body: {
        name,
        targetUrl,
        subscribedEvents
      }
    });
  },
  async deleteWebhook({ apiKey, webhookId }) {
    return this.makeRequest({
      method: import_pieces_common.HttpMethod.DELETE,
      url: `/v1/webhooks/${webhookId}`,
      apiKey,
      body: {}
    });
  }
};

// ../ap/packages/pieces/community/folk/src/lib/common/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), folkAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: 'Your Folk API key. You can view and manage your API keys in your workspace settings under the "API" section at https://app.folk.app/apps/contacts/network/settings/api-keys',
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b;
    try {
      return await folkClient.testConnection({
        secret_text: auth,
        type: import_pieces_framework2.AppConnectionType.SECRET_TEXT
      }), {
        valid: !0,
        message: "API key validated successfully. Connected to Folk."
      };
    } catch (error) {
      return (_a = error.message) != null && _a.includes("401") || (_b = error.message) != null && _b.includes("403") ? {
        valid: !1,
        error: "Invalid API key. Please check your API key and try again."
      } : {
        valid: !1,
        error: `Authentication failed: ${error.message}. Please verify your API key is correct.`
      };
    }
  }
});

// ../ap/packages/pieces/community/folk/src/lib/triggers/company-added.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/folk/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var folkProps = {
  group_id: (required = !1, displayName = "Group ID") => import_pieces_framework3.Property.Dropdown({
    auth: folkAuth,
    displayName,
    description: "Enter a Folk group ID (e.g., grp_abc123). Groups help you organize your contacts.",
    required,
    refreshers: [],
    options: async ({ auth }) => {
      var _a;
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Connect your account to see groups.",
          options: []
        };
      try {
        let groups = ((_a = (await folkClient.getGroups({
          apiKey: auth
        })).data) == null ? void 0 : _a.items) || [];
        return {
          disabled: !1,
          placeholder: groups.length === 0 ? "No groups found" : "Select a group",
          options: groups.map((group) => ({
            label: group.name,
            value: group.id
          }))
        };
      } catch (error) {
        return {
          disabled: !0,
          placeholder: "Error loading groups. Check your API key.",
          options: []
        };
      }
    }
  }),
  company_id: (required = !1) => import_pieces_framework3.Property.Dropdown({
    auth: folkAuth,
    displayName: "Company",
    description: "Select a company from your Folk workspace.",
    required,
    refreshers: [],
    options: async ({ auth }) => {
      var _a;
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Connect your account to see companies.",
          options: []
        };
      try {
        let companies = ((_a = (await folkClient.getCompaniesWithFilters({
          apiKey: auth,
          limit: 100
        })).data) == null ? void 0 : _a.items) || [];
        return {
          disabled: !1,
          placeholder: companies.length === 0 ? "No companies found" : "Select a company",
          options: companies.slice(0, 100).map((company) => {
            var _a2;
            let emails = (_a2 = company.emails) == null ? void 0 : _a2.map((e) => e.email).filter(Boolean).join(", ");
            return {
              label: emails ? `${company.name} (${emails})` : company.name || `Company ${company.id}`,
              value: company.id
            };
          })
        };
      } catch (error) {
        return {
          disabled: !0,
          placeholder: "Error loading companies. Check your API key.",
          options: []
        };
      }
    }
  }),
  companyIds: (required = !1) => import_pieces_framework3.Property.MultiSelectDropdown({
    auth: folkAuth,
    displayName: "Companies",
    description: "Select companies to associate with the person.",
    required,
    refreshers: [],
    options: async ({ auth }) => {
      var _a;
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Connect your account to see companies.",
          options: []
        };
      try {
        let companies = ((_a = (await folkClient.getCompaniesWithFilters({
          apiKey: auth,
          limit: 100
        })).data) == null ? void 0 : _a.items) || [];
        return {
          disabled: !1,
          placeholder: companies.length === 0 ? "No companies found" : "Select companies",
          options: companies.map((company) => ({
            label: company.name,
            value: company.id
          }))
        };
      } catch (error) {
        return {
          disabled: !0,
          placeholder: "Error loading companies. Check your API key.",
          options: []
        };
      }
    }
  }),
  person_id: (required = !1) => import_pieces_framework3.Property.Dropdown({
    auth: folkAuth,
    displayName: "Person",
    description: "Select a person from your Folk workspace.",
    required,
    refreshers: [],
    options: async ({ auth }) => {
      var _a;
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Connect your account to see people.",
          options: []
        };
      try {
        let people = ((_a = (await folkClient.getPeopleWithFilters({
          apiKey: auth,
          limit: 100
        })).data) == null ? void 0 : _a.items) || [];
        return {
          disabled: !1,
          placeholder: people.length === 0 ? "No people found" : "Select a person",
          options: people.slice(0, 100).map((person) => {
            var _a2;
            let name = person.fullName || person.name || `${person.firstName || ""} ${person.lastName || ""}`.trim(), emails = (_a2 = person.emails) == null ? void 0 : _a2.map((e) => e.email).filter(Boolean).join(", ");
            return {
              label: emails ? `${name} (${emails})` : name || `Person ${person.id}`,
              value: person.id
            };
          })
        };
      } catch (error) {
        return {
          disabled: !0,
          placeholder: "Error loading people. Check your API key.",
          options: []
        };
      }
    }
  })
};

// ../ap/packages/pieces/community/folk/src/lib/triggers/company-added.ts
var companyAdded = (0, import_pieces_framework4.createTrigger)({
  auth: folkAuth,
  name: "company_added",
  displayName: "Company Added",
  description: "Fires when a new company is added to your Folk workspace.",
  aiMetadata: {
    description: "Fires when a new company is created in the connected Folk workspace, optionally scoped to a single group. The event payload carries the new company ID and a URL to fetch its full details."
  },
  props: {
    groupId: folkProps.group_id(!1, "Group ID")
  },
  sampleData: {
    id: "evt_6004f2ef-dcc3-4c56-a704-9326730ad100",
    type: "company.created",
    createdAt: "2025-09-30T09:55:52.715Z",
    source: "https://api.folk.app/v1/webhooks/wbk_2f15ec4f-0f70-4884-a43d-1f88ef7a6665",
    data: {
      id: "com_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      url: "https://api.folk.app/v1/companies/com_05837e2d-9bf7-4f17-8729-e95f1be4de67"
    }
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a, _b;
    let eventConfig = {
      eventType: "company.created"
    };
    (_a = context.propsValue) != null && _a.groupId && (eventConfig.filter = {
      groupId: context.propsValue.groupId
    });
    let subscribedEvents = [eventConfig], webhook = await folkClient.createWebhook({
      apiKey: context.auth,
      name: `Activepieces Company Added - ${Date.now()}`,
      targetUrl: context.webhookUrl,
      subscribedEvents
    });
    await ((_b = context.store) == null ? void 0 : _b.put("_webhookId", webhook.data.id));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("_webhookId"));
    if (webhookId)
      try {
        await folkClient.deleteWebhook({
          apiKey: context.auth,
          webhookId
        });
      } catch (error) {
        console.warn("Failed to delete webhook:", error);
      }
  },
  async run(context) {
    let payloadBody = context.payload.body;
    return payloadBody.type !== "company.created" ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/folk/src/lib/triggers/company-removed.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var companyRemoved = (0, import_pieces_framework5.createTrigger)({
  auth: folkAuth,
  name: "company_removed",
  displayName: "Company Removed",
  description: "Fires when a company is removed from your Folk workspace.",
  aiMetadata: {
    description: "Fires when a company is deleted from the connected Folk workspace, optionally scoped to a single group. The event payload carries the deleted company ID along with a snapshot of its name and emails."
  },
  props: {
    groupId: folkProps.group_id(!1, "Group ID")
  },
  sampleData: {
    id: "evt_6004f2ef-dcc3-4c56-a704-9326730ad100",
    type: "company.deleted",
    createdAt: "2025-09-30T09:55:52.715Z",
    source: "https://api.folk.app/v1/webhooks/wbk_2f15ec4f-0f70-4884-a43d-1f88ef7a6665",
    data: {
      id: "com_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      url: "https://api.folk.app/v1/companies/com_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      details: {
        name: "ACME Inc.",
        emails: ["hello@acme.com"]
      }
    }
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a, _b;
    let eventConfig = {
      eventType: "company.deleted"
    };
    (_a = context.propsValue) != null && _a.groupId && (eventConfig.filter = {
      groupId: context.propsValue.groupId
    });
    let subscribedEvents = [eventConfig], webhook = await folkClient.createWebhook({
      apiKey: context.auth,
      name: `Activepieces Company Removed - ${Date.now()}`,
      targetUrl: context.webhookUrl,
      subscribedEvents
    });
    await ((_b = context.store) == null ? void 0 : _b.put("_webhookId", webhook.data.id));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("_webhookId"));
    if (webhookId)
      try {
        await folkClient.deleteWebhook({
          apiKey: context.auth,
          webhookId
        });
      } catch (error) {
        console.warn("Failed to delete webhook:", error);
      }
  },
  async run(context) {
    let payloadBody = context.payload.body;
    return payloadBody.type !== "company.deleted" ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/folk/src/lib/triggers/company-updated.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var companyUpdated = (0, import_pieces_framework6.createTrigger)({
  auth: folkAuth,
  name: "company_updated",
  displayName: "Company Updated",
  description: "Fires when a company is updated in your Folk workspace.",
  aiMetadata: {
    description: "Fires when an existing company is modified in the connected Folk workspace, optionally scoped to a single group. The event payload carries the company ID and a list of field-level changes (including custom field values) describing what was set, added, or removed."
  },
  props: {
    groupId: folkProps.group_id(!1, "Group ID")
  },
  sampleData: {
    id: "evt_6004f2ef-dcc3-4c56-a704-9326730ad100",
    type: "company.updated",
    createdAt: "2025-09-30T09:55:52.715Z",
    source: "https://api.folk.app/v1/webhooks/wbk_2f15ec4f-0f70-4884-a43d-1f88ef7a6665",
    data: {
      id: "com_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      url: "https://api.folk.app/v1/companies/com_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      changes: [
        {
          path: ["name"],
          type: "set",
          value: "ACME Inc."
        },
        {
          path: ["description"],
          type: "set"
        },
        {
          path: [
            "customFieldValues",
            "grp_bc984b3f-0386-434d-82d7-a91eb6badd71",
            "Status"
          ],
          type: "set",
          value: "In Progress"
        },
        {
          path: [
            "customFieldValues",
            "grp_bc984b3f-0386-434d-82d7-a91eb6badd71",
            "Tags"
          ],
          type: "remove",
          value: ["VIP"]
        },
        {
          path: [
            "customFieldValues",
            "grp_bc984b3f-0386-434d-82d7-a91eb6badd71",
            "Assigned to"
          ],
          type: "add",
          value: [{ id: "usr_3b7d7b65-4c2d-4b65-822a-6bc759e1e951" }]
        }
      ]
    }
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a, _b;
    let eventConfig = {
      eventType: "company.updated"
    };
    (_a = context.propsValue) != null && _a.groupId && (eventConfig.filter = {
      groupId: context.propsValue.groupId
    });
    let subscribedEvents = [eventConfig], webhook = await folkClient.createWebhook({
      apiKey: context.auth,
      name: `Activepieces Company Updated - ${Date.now()}`,
      targetUrl: context.webhookUrl,
      subscribedEvents
    });
    await ((_b = context.store) == null ? void 0 : _b.put("_webhookId", webhook.data.id));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("_webhookId"));
    if (webhookId)
      try {
        await folkClient.deleteWebhook({
          apiKey: context.auth,
          webhookId
        });
      } catch (error) {
        console.warn("Failed to delete webhook:", error);
      }
  },
  async run(context) {
    let payloadBody = context.payload.body;
    return payloadBody.type !== "company.updated" ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/folk/src/lib/triggers/company-custom-field-updated.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var companyCustomFieldUpdated = (0, import_pieces_framework7.createTrigger)({
  auth: folkAuth,
  name: "company_custom_field_updated",
  displayName: "Company Groups Updated",
  description: "Fires when a company's group assignments are updated in your Folk workspace.",
  aiMetadata: {
    description: "Fires when a company's group memberships change in the connected Folk workspace (added to or removed from groups). The event payload carries the company ID and the list of group additions and removals."
  },
  props: {},
  sampleData: {
    id: "evt_6004f2ef-dcc3-4c56-a704-9326730ad100",
    type: "company.groups_updated",
    createdAt: "2025-09-30T09:55:52.715Z",
    source: "https://api.folk.app/v1/webhooks/wbk_2f15ec4f-0f70-4884-a43d-1f88ef7a6665",
    data: {
      id: "com_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      url: "https://api.folk.app/v1/companies/com_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      changes: [
        {
          path: ["groups"],
          type: "add",
          value: [{ id: "grp_bc984b3f-0386-434d-82d7-a91eb6badd71" }]
        },
        {
          path: ["groups"],
          type: "remove",
          value: [{ id: "grp_3b7d7b65-4c2d-4b65-822a-6bc759e1e951" }]
        }
      ]
    }
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let subscribedEvents = [
      {
        eventType: "company.groups_updated"
      }
    ], webhook = await folkClient.createWebhook({
      apiKey: context.auth,
      name: `Activepieces Company Groups Updated - ${Date.now()}`,
      targetUrl: context.webhookUrl,
      subscribedEvents
    });
    await ((_a = context.store) == null ? void 0 : _a.put("_webhookId", webhook.data.id));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("_webhookId"));
    if (webhookId)
      try {
        await folkClient.deleteWebhook({
          apiKey: context.auth,
          webhookId
        });
      } catch (error) {
        console.warn("Failed to delete webhook:", error);
      }
  },
  async run(context) {
    let payloadBody = context.payload.body;
    return payloadBody.type !== "company.groups_updated" ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/folk/src/lib/triggers/person-added.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var personAdded = (0, import_pieces_framework8.createTrigger)({
  auth: folkAuth,
  name: "person_added",
  displayName: "Person Added",
  description: "Fires when a new person is added to your Folk workspace.",
  aiMetadata: {
    description: "Fires when a new person (contact) is created in the connected Folk workspace, optionally scoped to a single group. The event payload carries the new person ID and a URL to fetch their full details."
  },
  props: {
    groupId: folkProps.group_id(!1, "Group ID")
  },
  sampleData: {
    id: "evt_6004f2ef-dcc3-4c56-a704-9326730ad100",
    type: "person.created",
    createdAt: "2025-09-30T09:55:52.715Z",
    source: "https://api.folk.app/v1/webhooks/wbk_2f15ec4f-0f70-4884-a43d-1f88ef7a6665",
    data: {
      id: "per_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      url: "https://api.folk.app/v1/people/per_05837e2d-9bf7-4f17-8729-e95f1be4de67"
    }
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a, _b;
    let eventConfig = {
      eventType: "person.created"
    };
    (_a = context.propsValue) != null && _a.groupId && (eventConfig.filter = {
      groupId: context.propsValue.groupId
    });
    let subscribedEvents = [eventConfig], webhook = await folkClient.createWebhook({
      apiKey: context.auth,
      name: `Activepieces Person Added - ${Date.now()}`,
      targetUrl: context.webhookUrl,
      subscribedEvents
    });
    await ((_b = context.store) == null ? void 0 : _b.put("_webhookId", webhook.data.id));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("_webhookId"));
    if (webhookId)
      try {
        await folkClient.deleteWebhook({
          apiKey: context.auth,
          webhookId
        });
      } catch (error) {
        console.warn("Failed to delete webhook:", error);
      }
  },
  async run(context) {
    let payloadBody = context.payload.body;
    return payloadBody.type !== "person.created" ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/folk/src/lib/triggers/person-removed.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var personRemoved = (0, import_pieces_framework9.createTrigger)({
  auth: folkAuth,
  name: "person_removed",
  displayName: "Person Removed",
  description: "Fires when a person is removed from your Folk workspace.",
  aiMetadata: {
    description: "Fires when a person (contact) is deleted from the connected Folk workspace, optionally scoped to a single group. The event payload carries the deleted person ID along with a snapshot of their name and emails."
  },
  props: {
    groupId: folkProps.group_id(!1, "Group ID")
  },
  sampleData: {
    id: "evt_6004f2ef-dcc3-4c56-a704-9326730ad100",
    type: "person.deleted",
    createdAt: "2025-09-30T09:55:52.715Z",
    source: "https://api.folk.app/v1/webhooks/wbk_2f15ec4f-0f70-4884-a43d-1f88ef7a6665",
    data: {
      id: "per_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      url: "https://api.folk.app/v1/people/per_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      details: {
        name: "John Doe",
        emails: ["john@doe.com"]
      }
    }
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a, _b;
    let eventConfig = {
      eventType: "person.deleted"
    };
    (_a = context.propsValue) != null && _a.groupId && (eventConfig.filter = {
      groupId: context.propsValue.groupId
    });
    let subscribedEvents = [eventConfig], webhook = await folkClient.createWebhook({
      apiKey: context.auth,
      name: `Activepieces Person Removed - ${Date.now()}`,
      targetUrl: context.webhookUrl,
      subscribedEvents
    });
    await ((_b = context.store) == null ? void 0 : _b.put("_webhookId", webhook.data.id));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("_webhookId"));
    if (webhookId)
      try {
        await folkClient.deleteWebhook({
          apiKey: context.auth,
          webhookId
        });
      } catch (error) {
        console.warn("Failed to delete webhook:", error);
      }
  },
  async run(context) {
    let payloadBody = context.payload.body;
    return payloadBody.type !== "person.deleted" ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/folk/src/lib/triggers/person-updated.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var personUpdated = (0, import_pieces_framework10.createTrigger)({
  auth: folkAuth,
  name: "person_updated",
  displayName: "Person Updated",
  description: "Fires when a person is updated in your Folk workspace.",
  aiMetadata: {
    description: "Fires when an existing person (contact) is modified in the connected Folk workspace, optionally scoped to a single group. The event payload carries the person ID and a list of field-level changes (including company links and custom field values) describing what was set, added, or removed."
  },
  props: {
    groupId: folkProps.group_id(!1, "Group ID")
  },
  sampleData: {
    id: "evt_6004f2ef-dcc3-4c56-a704-9326730ad100",
    type: "person.updated",
    createdAt: "2025-09-30T09:55:52.715Z",
    source: "https://api.folk.app/v1/webhooks/wbk_2f15ec4f-0f70-4884-a43d-1f88ef7a6665",
    data: {
      id: "per_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      url: "https://api.folk.app/v1/people/per_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      changes: [
        {
          path: ["firstName"],
          type: "set",
          value: "John"
        },
        {
          path: ["description"],
          type: "set"
        },
        {
          path: ["companies"],
          type: "add",
          value: [{ id: "com_3b7d7b65-4c2d-4b65-822a-6bc759e1e951" }]
        },
        {
          path: [
            "customFieldValues",
            "grp_bc984b3f-0386-434d-82d7-a91eb6badd71",
            "Status"
          ],
          type: "set",
          value: "In Progress"
        },
        {
          path: [
            "customFieldValues",
            "grp_bc984b3f-0386-434d-82d7-a91eb6badd71",
            "Tags"
          ],
          type: "remove",
          value: ["VIP"]
        },
        {
          path: [
            "customFieldValues",
            "grp_bc984b3f-0386-434d-82d7-a91eb6badd71",
            "Assigned to"
          ],
          type: "add",
          value: [{ id: "usr_3b7d7b65-4c2d-4b65-822a-6bc759e1e951" }]
        }
      ]
    }
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a, _b;
    let eventConfig = {
      eventType: "person.updated"
    };
    (_a = context.propsValue) != null && _a.groupId && (eventConfig.filter = {
      groupId: context.propsValue.groupId
    });
    let subscribedEvents = [eventConfig], webhook = await folkClient.createWebhook({
      apiKey: context.auth,
      name: `Activepieces Person Updated - ${Date.now()}`,
      targetUrl: context.webhookUrl,
      subscribedEvents
    });
    await ((_b = context.store) == null ? void 0 : _b.put("_webhookId", webhook.data.id));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("_webhookId"));
    if (webhookId)
      try {
        await folkClient.deleteWebhook({
          apiKey: context.auth,
          webhookId
        });
      } catch (error) {
        console.warn("Failed to delete webhook:", error);
      }
  },
  async run(context) {
    let payloadBody = context.payload.body;
    return payloadBody.type !== "person.updated" ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/folk/src/lib/triggers/person-custom-field-updated.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var personCustomFieldUpdated = (0, import_pieces_framework11.createTrigger)({
  auth: folkAuth,
  name: "person_custom_field_updated",
  displayName: "Person Groups Updated",
  description: "Fires when a person's group assignments are updated in your Folk workspace.",
  aiMetadata: {
    description: "Fires when a person's group memberships change in the connected Folk workspace (added to or removed from groups). The event payload carries the person ID and the list of group additions and removals."
  },
  props: {},
  sampleData: {
    id: "evt_6004f2ef-dcc3-4c56-a704-9326730ad100",
    type: "person.groups_updated",
    createdAt: "2025-09-30T09:55:52.715Z",
    source: "https://api.folk.app/v1/webhooks/wbk_2f15ec4f-0f70-4884-a43d-1f88ef7a6665",
    data: {
      id: "per_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      url: "https://api.folk.app/v1/people/per_05837e2d-9bf7-4f17-8729-e95f1be4de67",
      changes: [
        {
          path: ["groups"],
          type: "add",
          value: [{ id: "grp_bc984b3f-0386-434d-82d7-a91eb6badd71" }]
        },
        {
          path: ["groups"],
          type: "remove",
          value: [{ id: "grp_3b7d7b65-4c2d-4b65-822a-6bc759e1e951" }]
        }
      ]
    }
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let subscribedEvents = [
      {
        eventType: "person.groups_updated"
      }
    ], webhook = await folkClient.createWebhook({
      apiKey: context.auth,
      name: `Activepieces Person Groups Updated - ${Date.now()}`,
      targetUrl: context.webhookUrl,
      subscribedEvents
    });
    await ((_a = context.store) == null ? void 0 : _a.put("_webhookId", webhook.data.id));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("_webhookId"));
    if (webhookId)
      try {
        await folkClient.deleteWebhook({
          apiKey: context.auth,
          webhookId
        });
      } catch (error) {
        console.warn("Failed to delete webhook:", error);
      }
  },
  async run(context) {
    let payloadBody = context.payload.body;
    return payloadBody.type !== "person.groups_updated" ? [] : [payloadBody];
  }
});

// ../ap/packages/pieces/community/folk/src/lib/actions/create-company.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var createCompany = (0, import_pieces_framework12.createAction)({
  auth: folkAuth,
  name: "createCompany",
  displayName: "Create Company",
  description: "Create a new company in your Folk workspace. You can add emails, links, phone numbers, and assign it to a group.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new company record in the Folk CRM, optionally attaching addresses, emails, phone numbers, URLs, an industry, and a group assignment. Use when a company does not yet exist in Folk; only the company name is required. Not idempotent: each call creates a separate company even with identical input, so guard against duplicates (e.g. search with Find Company first).",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework12.Property.ShortText({
      displayName: "Company Name",
      description: "The name of the company",
      required: !0
    }),
    description: import_pieces_framework12.Property.LongText({
      displayName: "Description",
      description: "A short description of the company",
      required: !1
    }),
    industry: import_pieces_framework12.Property.ShortText({
      displayName: "Industry",
      description: "The industry the company operates in",
      required: !1
    }),
    addresses: import_pieces_framework12.Property.Array({
      displayName: "Addresses",
      description: "Physical addresses for the company. The first address is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework12.Property.ShortText({
          displayName: "Address",
          required: !0
        })
      }
    }),
    emails: import_pieces_framework12.Property.Array({
      displayName: "Emails",
      description: "Email addresses for the company. The first email is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework12.Property.ShortText({
          displayName: "Email",
          required: !0
        })
      }
    }),
    phones: import_pieces_framework12.Property.Array({
      displayName: "Phone Numbers",
      description: "Phone numbers for the company. The first phone is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework12.Property.ShortText({
          displayName: "Phone Number",
          required: !0
        })
      }
    }),
    urls: import_pieces_framework12.Property.Array({
      displayName: "URLs",
      description: "Website URLs for the company. The first URL is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework12.Property.ShortText({
          displayName: "URL",
          required: !0
        })
      }
    }),
    groupId: folkProps.group_id(!1, "Group ID")
  },
  async run(context) {
    let { name, description, industry, addresses, emails, phones, urls, groupId } = context.propsValue, companyData = {
      name
    };
    return description && (companyData.description = description), industry && (companyData.industry = industry), addresses && Array.isArray(addresses) && addresses.length > 0 && (companyData.addresses = addresses.map((addr) => addr.value || addr)), emails && Array.isArray(emails) && emails.length > 0 && (companyData.emails = emails.map((e) => e.value || e)), phones && Array.isArray(phones) && phones.length > 0 && (companyData.phones = phones.map((p) => p.value || p)), urls && Array.isArray(urls) && urls.length > 0 && (companyData.urls = urls.map((u) => u.value || u)), groupId && (companyData.groups = [{ id: groupId }]), {
      company: (await folkClient.createCompany({
        apiKey: context.auth,
        data: companyData
      })).data,
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/folk/src/lib/actions/update-company.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var updateCompany = (0, import_pieces_framework13.createAction)({
  auth: folkAuth,
  name: "updateCompany",
  displayName: "Update Company",
  description: "Update an existing company in your Folk workspace. Select the company from the dropdown to update its details.",
  audience: "both",
  aiMetadata: {
    description: "Updates fields on an existing Folk company identified by its company ID; only the fields you supply are changed. Use when you already know the target company ID (look it up first with Find Company or Get Company). Not idempotent in general: re-running mutates the record on each call and supplied arrays such as emails or groups replace prior values, so resubmitting the same input is not guaranteed to be a no-op.",
    idempotent: !1
  },
  props: {
    companyId: folkProps.company_id(!0),
    name: import_pieces_framework13.Property.ShortText({
      displayName: "Company Name",
      description: "The name of the company",
      required: !1
    }),
    description: import_pieces_framework13.Property.LongText({
      displayName: "Description",
      description: "A short description of the company",
      required: !1
    }),
    industry: import_pieces_framework13.Property.ShortText({
      displayName: "Industry",
      description: "The industry the company operates in",
      required: !1
    }),
    addresses: import_pieces_framework13.Property.Array({
      displayName: "Addresses",
      description: "Physical addresses for the company. The first address is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework13.Property.ShortText({
          displayName: "Address",
          required: !0
        })
      }
    }),
    emails: import_pieces_framework13.Property.Array({
      displayName: "Emails",
      description: "Email addresses for the company. The first email is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework13.Property.ShortText({
          displayName: "Email",
          required: !0
        })
      }
    }),
    phones: import_pieces_framework13.Property.Array({
      displayName: "Phone Numbers",
      description: "Phone numbers for the company. The first phone is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework13.Property.ShortText({
          displayName: "Phone Number",
          required: !0
        })
      }
    }),
    urls: import_pieces_framework13.Property.Array({
      displayName: "URLs",
      description: "Website URLs for the company. The first URL is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework13.Property.ShortText({
          displayName: "URL",
          required: !0
        })
      }
    }),
    groupId: folkProps.group_id(!1, "Group ID")
  },
  async run(context) {
    let { companyId, name, description, industry, addresses, emails, phones, urls, groupId } = context.propsValue, companyData = {};
    return name && (companyData.name = name), description && (companyData.description = description), industry && (companyData.industry = industry), addresses && Array.isArray(addresses) && (companyData.addresses = addresses.map((addr) => addr.value || addr)), emails && Array.isArray(emails) && (companyData.emails = emails.map((e) => e.value || e)), phones && Array.isArray(phones) && (companyData.phones = phones.map((p) => p.value || p)), urls && Array.isArray(urls) && (companyData.urls = urls.map((u) => u.value || u)), groupId && (companyData.groups = [{ id: groupId }]), {
      company: (await folkClient.updateCompany({
        apiKey: context.auth,
        companyId,
        data: companyData
      })).data,
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/folk/src/lib/actions/create-person.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var createPerson = (0, import_pieces_framework14.createAction)({
  auth: folkAuth,
  name: "createPerson",
  displayName: "Create Person",
  description: "Create a new person in your Folk workspace. You can link them to a company, add contact details, and assign to a group.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new person (contact) record in the Folk CRM, optionally linking them to companies by ID or name, setting a job title and birthday, and adding emails, phones, addresses, URLs, and a group. Use to add a new contact when none exists; provide a name via first/last or full name. Not idempotent: each call creates a separate person, so check for existing contacts first to avoid duplicates.",
    idempotent: !1
  },
  props: {
    firstName: import_pieces_framework14.Property.ShortText({
      displayName: "First Name",
      description: "The first name of the person",
      required: !1
    }),
    lastName: import_pieces_framework14.Property.ShortText({
      displayName: "Last Name",
      description: "The last name of the person",
      required: !1
    }),
    fullName: import_pieces_framework14.Property.ShortText({
      displayName: "Full Name",
      description: "The full name of the person (alternative to first/last)",
      required: !1
    }),
    description: import_pieces_framework14.Property.LongText({
      displayName: "Description",
      description: "A short description of the person",
      required: !1
    }),
    birthday: import_pieces_framework14.Property.ShortText({
      displayName: "Birthday",
      description: "The birthday in YYYY-MM-DD format",
      required: !1
    }),
    jobTitle: import_pieces_framework14.Property.ShortText({
      displayName: "Job Title",
      description: "The job title of the person",
      required: !1
    }),
    companyNames: import_pieces_framework14.Property.Array({
      displayName: "Company Names",
      description: "The names of companies to associate with the person. The first company will be primary.",
      required: !1,
      properties: {
        value: import_pieces_framework14.Property.ShortText({
          displayName: "Company Name",
          required: !0
        })
      }
    }),
    companyIds: folkProps.companyIds(!1),
    addresses: import_pieces_framework14.Property.Array({
      displayName: "Addresses",
      description: "Physical addresses for the person. The first address is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework14.Property.ShortText({
          displayName: "Address",
          required: !0
        })
      }
    }),
    emails: import_pieces_framework14.Property.Array({
      displayName: "Emails",
      description: "Email addresses for the person. The first email is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework14.Property.ShortText({
          displayName: "Email",
          required: !0
        })
      }
    }),
    phones: import_pieces_framework14.Property.Array({
      displayName: "Phone Numbers",
      description: "Phone numbers for the person. The first phone is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework14.Property.ShortText({
          displayName: "Phone Number",
          required: !0
        })
      }
    }),
    urls: import_pieces_framework14.Property.Array({
      displayName: "URLs",
      description: "Website URLs for the person. The first URL is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework14.Property.ShortText({
          displayName: "URL",
          required: !0
        })
      }
    }),
    groupId: folkProps.group_id(!1, "Group ID")
  },
  async run(context) {
    let {
      firstName,
      lastName,
      fullName,
      description,
      birthday,
      jobTitle,
      companyNames,
      companyIds,
      addresses,
      emails,
      phones,
      urls,
      groupId
    } = context.propsValue, personData = {};
    firstName && (personData.firstName = firstName), lastName && (personData.lastName = lastName), fullName && (personData.fullName = fullName), description && (personData.description = description), birthday && (personData.birthday = birthday), jobTitle && (personData.jobTitle = jobTitle);
    let companies = [];
    if (companyIds)
      for (let companyId of companyIds)
        companies.push({ id: companyId });
    return companyNames && Array.isArray(companyNames) && companies.push(...companyNames.map((c) => ({ name: c.value || c }))), companies.length > 0 && (personData.companies = companies), addresses && Array.isArray(addresses) && addresses.length > 0 && (personData.addresses = addresses.map((addr) => addr.value || addr)), emails && Array.isArray(emails) && emails.length > 0 && (personData.emails = emails.map((e) => e.value || e)), phones && Array.isArray(phones) && phones.length > 0 && (personData.phones = phones.map((p) => p.value || p)), urls && Array.isArray(urls) && urls.length > 0 && (personData.urls = urls.map((u) => u.value || u)), groupId && (personData.groups = [{ id: groupId }]), (await folkClient.createPerson({
      apiKey: context.auth,
      data: personData
    })).data;
  }
});

// ../ap/packages/pieces/community/folk/src/lib/actions/update-person.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var updatePerson = (0, import_pieces_framework15.createAction)({
  auth: folkAuth,
  name: "updatePerson",
  displayName: "Update Person",
  description: "Update an existing person in your Folk workspace with new information.",
  audience: "both",
  aiMetadata: {
    description: "Updates fields on an existing Folk person identified by their person ID; only the fields you supply are changed, and supplied company names that do not yet exist are created. Use when you already know the target person ID (look it up first with List People or Get Person). Not idempotent in general: re-running mutates the record each call and supplied arrays replace prior values, so resubmitting the same input is not guaranteed to be a no-op.",
    idempotent: !1
  },
  props: {
    personId: folkProps.person_id(!0),
    firstName: import_pieces_framework15.Property.ShortText({
      displayName: "First Name",
      description: "The first name of the person",
      required: !1
    }),
    lastName: import_pieces_framework15.Property.ShortText({
      displayName: "Last Name",
      description: "The last name of the person",
      required: !1
    }),
    fullName: import_pieces_framework15.Property.ShortText({
      displayName: "Full Name",
      description: "The full name of the person (alternative to first/last)",
      required: !1
    }),
    description: import_pieces_framework15.Property.LongText({
      displayName: "Description",
      description: "A short description of the person",
      required: !1
    }),
    birthday: import_pieces_framework15.Property.ShortText({
      displayName: "Birthday",
      description: "The birthday in YYYY-MM-DD format",
      required: !1
    }),
    jobTitle: import_pieces_framework15.Property.ShortText({
      displayName: "Job Title",
      description: "The job title of the person",
      required: !1
    }),
    companyIds: folkProps.companyIds(!1),
    companyNames: import_pieces_framework15.Property.Array({
      displayName: "Company Names",
      description: "Company names to create or associate with. Companies will be created if they don't exist.",
      required: !1,
      properties: {
        value: import_pieces_framework15.Property.ShortText({
          displayName: "Company Name",
          required: !0
        })
      }
    }),
    addresses: import_pieces_framework15.Property.Array({
      displayName: "Addresses",
      description: "Physical addresses for the person. The first address is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework15.Property.ShortText({
          displayName: "Address",
          required: !0
        })
      }
    }),
    emails: import_pieces_framework15.Property.Array({
      displayName: "Emails",
      description: "Email addresses for the person. The first email is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework15.Property.ShortText({
          displayName: "Email",
          required: !0
        })
      }
    }),
    phones: import_pieces_framework15.Property.Array({
      displayName: "Phone Numbers",
      description: "Phone numbers for the person. The first phone is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework15.Property.ShortText({
          displayName: "Phone Number",
          required: !0
        })
      }
    }),
    urls: import_pieces_framework15.Property.Array({
      displayName: "URLs",
      description: "Website URLs for the person. The first URL is primary.",
      required: !1,
      properties: {
        value: import_pieces_framework15.Property.ShortText({
          displayName: "URL",
          required: !0
        })
      }
    }),
    groupId: folkProps.group_id(!1, "Group ID")
  },
  async run(context) {
    let {
      personId,
      firstName,
      lastName,
      fullName,
      description,
      birthday,
      jobTitle,
      companyIds,
      companyNames,
      addresses,
      emails,
      phones,
      urls,
      groupId
    } = context.propsValue, personData = {};
    firstName && (personData.firstName = firstName), lastName && (personData.lastName = lastName), fullName && (personData.fullName = fullName), description && (personData.description = description), birthday && (personData.birthday = birthday), jobTitle && (personData.jobTitle = jobTitle);
    let companies = [];
    if (companyIds)
      for (let companyId of companyIds)
        companies.push({ id: companyId });
    return companyNames && Array.isArray(companyNames) && companies.push(...companyNames.map((c) => ({ name: c.value || c }))), companies.length > 0 && (personData.companies = companies), addresses && Array.isArray(addresses) && addresses.length > 0 && (personData.addresses = addresses.map((addr) => addr.value || addr)), emails && Array.isArray(emails) && emails.length > 0 && (personData.emails = emails.map((e) => e.value || e)), phones && Array.isArray(phones) && phones.length > 0 && (personData.phones = phones.map((p) => p.value || p)), urls && Array.isArray(urls) && urls.length > 0 && (personData.urls = urls.map((u) => u.value || u)), groupId && (personData.groups = [{ id: groupId }]), {
      data: (await folkClient.updatePerson({
        apiKey: context.auth,
        contactId: personId,
        data: personData
      })).data,
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/folk/src/lib/actions/find-company.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var findCompany = (0, import_pieces_framework16.createAction)({
  auth: folkAuth,
  name: "findCompany",
  displayName: "Find Company",
  description: "Search for companies in your Folk workspace by name or email address.",
  audience: "both",
  aiMetadata: {
    description: "Searches the Folk CRM for companies matching a query string against company name or email, returning the matching companies. Use to resolve a company name or email into a company ID before updating or linking it. The search query is required. Read-only and idempotent: repeating the same query returns the same matches without side effects.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework16.Property.ShortText({
      displayName: "Search Query",
      description: "Enter company name or email to search for",
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { query } = context.propsValue, response = await folkClient.searchCompany({
      apiKey: context.auth,
      query
    });
    return {
      companies: ((_a = response.data) == null ? void 0 : _a.items) || [],
      count: ((_c = (_b = response.data) == null ? void 0 : _b.items) == null ? void 0 : _c.length) || 0,
      pagination: (_d = response.data) == null ? void 0 : _d.pagination
    };
  }
});

// ../ap/packages/pieces/community/folk/src/lib/actions/get-company.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var getCompany = (0, import_pieces_framework17.createAction)({
  auth: folkAuth,
  name: "getCompany",
  displayName: "Get Company",
  description: "Retrieve detailed information about a company from your Folk workspace.",
  audience: "both",
  aiMetadata: {
    description: "Fetches the full record of a single Folk company by its company ID. Use when you already have the company ID and need its current details; if you only have a name or email, find the ID first with Find Company. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    companyId: folkProps.company_id(!0)
  },
  async run(context) {
    let { companyId } = context.propsValue;
    return {
      company: (await folkClient.getCompany({
        apiKey: context.auth,
        companyId
      })).data
    };
  }
});

// ../ap/packages/pieces/community/folk/src/lib/actions/find-person.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var findPerson = (0, import_pieces_framework18.createAction)({
  auth: folkAuth,
  name: "findPerson",
  displayName: "List People",
  description: "Retrieve a paginated list of people in your Folk workspace with optional filtering.",
  audience: "both",
  aiMetadata: {
    description: "Lists people (contacts) in the Folk CRM, returning a single cursor-paginated page. With no filters it returns all people; optional name and email filters (contains-match) combined by AND or OR narrow the results. Use to browse contacts or resolve a name/email into a person ID; pass the cursor to page through additional results. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework18.Property.Number({
      displayName: "Limit",
      description: "The number of items to return (1-100)",
      required: !1,
      defaultValue: 20
    }),
    cursor: import_pieces_framework18.Property.ShortText({
      displayName: "Cursor",
      description: "A cursor for pagination. Use the nextLink value from a previous response.",
      required: !1
    }),
    combinator: import_pieces_framework18.Property.StaticDropdown({
      displayName: "Filter Combinator",
      description: "The logical operator to combine multiple filters",
      required: !1,
      defaultValue: "and",
      options: {
        options: [
          { label: "AND", value: "and" },
          { label: "OR", value: "or" }
        ]
      }
    }),
    nameFilter: import_pieces_framework18.Property.ShortText({
      displayName: "Name Filter",
      description: "Filter by person name (contains)",
      required: !1
    }),
    emailFilter: import_pieces_framework18.Property.ShortText({
      displayName: "Email Filter",
      description: "Filter by email address (contains)",
      required: !1
    })
  },
  async run(context) {
    let { limit, cursor, combinator, nameFilter, emailFilter } = context.propsValue;
    return {
      data: (await folkClient.getPeopleWithFilters({
        apiKey: context.auth,
        limit: limit || 20,
        cursor,
        combinator,
        nameFilter,
        emailFilter
      })).data,
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/folk/src/lib/actions/get-person.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var getPerson = (0, import_pieces_framework19.createAction)({
  auth: folkAuth,
  name: "getPerson",
  displayName: "Get Person",
  description: "Retrieve detailed information about a person from your Folk workspace.",
  audience: "both",
  aiMetadata: {
    description: "Fetches the full record of a single Folk person (contact) by their person ID. Use when you already have the person ID and need their current details; if you only have a name or email, find the ID first with List People. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    personId: folkProps.person_id(!0)
  },
  async run(context) {
    let { personId } = context.propsValue;
    return {
      data: (await folkClient.getPerson({
        apiKey: context.auth,
        contactId: personId
      })).data,
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/folk/src/index.ts
var folk = (0, import_pieces_framework20.createPiece)({
  displayName: "Folk",
  auth: folkAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/folk.png",
  authors: ["sparkybug"],
  categories: [import_pieces_framework21.PieceCategory.SALES_AND_CRM],
  description: "Folk is a CRM for building relationships at scale. Manage your contacts, companies, and relationships in one place.",
  actions: [
    createCompany,
    updateCompany,
    createPerson,
    updatePerson,
    findCompany,
    getCompany,
    findPerson,
    getPerson
  ],
  triggers: [
    companyAdded,
    companyRemoved,
    companyUpdated,
    companyCustomFieldUpdated,
    personAdded,
    personRemoved,
    personUpdated,
    personCustomFieldUpdated
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  folk
});
