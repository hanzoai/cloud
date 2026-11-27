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

// ../ap/packages/pieces/community/kommo/src/index.ts
var index_exports = {};
__export(index_exports, {
  kommo: () => kommo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_framework15 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/kommo/src/lib/triggers/lead-status-changed.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/kommo/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = `
Please follow [Generate Long Live Token](https://developers.kommo.com/docs/long-lived-token) guide for generating token.

Your Kommo account subdomain (e.g., "mycompany" if your URL is mycompany.kommo.com).

`, kommoAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdownDescription,
  required: !0,
  props: {
    subdomain: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Subdomain",
      required: !0
    }),
    apiToken: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Token",
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function makeRequest(auth, method, path, body) {
  let url = `https://${auth.subdomain}.kommo.com/api/v4${path}`;
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url,
    headers: {
      Authorization: `Bearer ${auth.apiToken}`,
      "Content-Type": "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/kommo/src/lib/triggers/lead-status-changed.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), leadStatusChangedTrigger = (0, import_pieces_framework2.createTrigger)({
  auth: kommoAuth,
  name: "lead_status_changed",
  displayName: "Lead Status Changed",
  description: "Triggers when a lead status is changed.",
  aiMetadata: {
    description: "Fires when a lead in the Kommo CRM account moves to a different status (e.g. advances or regresses within its pipeline). Represents a stage change on an existing lead, emitting the full updated lead record."
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let { subdomain, apiToken } = context.auth.props, webhook = await makeRequest(
      { subdomain, apiToken },
      import_pieces_common2.HttpMethod.POST,
      "/webhooks",
      {
        destination: context.webhookUrl,
        settings: ["status_lead"]
      }
    );
    await context.store.put("webhookId", webhook.id);
  },
  async onDisable(context) {
    let { subdomain, apiToken } = context.auth.props;
    await context.store.get("webhookId") && await makeRequest(
      { subdomain, apiToken },
      import_pieces_common2.HttpMethod.DELETE,
      "/webhooks",
      { destination: context.webhookUrl }
    );
  },
  async run(context) {
    let { subdomain, apiToken } = context.auth.props, leadId = context.payload.body.leads.status[0].id;
    return leadId ? [await makeRequest({ apiToken, subdomain }, import_pieces_common2.HttpMethod.GET, `/leads/${leadId}`)] : [];
  },
  async test(context) {
    var _a, _b;
    let { subdomain, apiToken } = context.auth.props, response = await makeRequest({ subdomain, apiToken }, import_pieces_common2.HttpMethod.GET, "/leads?limit=5&order[updated_at]=desc");
    return (_b = (_a = response == null ? void 0 : response._embedded) == null ? void 0 : _a.leads) != null ? _b : [];
  },
  sampleData: {
    id: 256988,
    name: "John Doe",
    price: 100,
    responsible_user_id: 13290567,
    group_id: 0,
    status_id: 86521115,
    pipeline_id: 11273979,
    loss_reason_id: null,
    created_by: 13290567,
    updated_by: 13290567,
    created_at: 1748800059,
    updated_at: 1748800060,
    closed_at: null,
    closest_task_at: null,
    is_deleted: !1,
    custom_fields_values: null,
    score: null,
    account_id: 34678947,
    labor_cost: null,
    is_price_computed: !1,
    _links: {
      self: {
        href: ""
      }
    },
    _embedded: {
      tags: [],
      companies: [
        {
          id: 722828,
          _links: {
            self: {
              href: ""
            }
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/triggers/new-contact-added.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), newContactAddedTrigger = (0, import_pieces_framework3.createTrigger)({
  auth: kommoAuth,
  name: "new_contact_added",
  displayName: "New Contact Added",
  description: "Triggers when a new contact is added.",
  aiMetadata: {
    description: "Fires when a new contact is created in the Kommo CRM account, emitting the full new contact record. Represents a person being added to the CRM, regardless of source."
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let { subdomain, apiToken } = context.auth.props, webhook = await makeRequest(
      { subdomain, apiToken },
      import_pieces_common3.HttpMethod.POST,
      "/webhooks",
      {
        destination: context.webhookUrl,
        settings: ["add_contact"]
      }
    );
    await context.store.put("webhookId", webhook.id);
  },
  async onDisable(context) {
    let { subdomain, apiToken } = context.auth.props;
    await context.store.get("webhookId") && await makeRequest(
      { subdomain, apiToken },
      import_pieces_common3.HttpMethod.DELETE,
      "/webhooks",
      { destination: context.webhookUrl }
    );
  },
  async run(context) {
    let { subdomain, apiToken } = context.auth.props, contactId = context.payload.body.contacts.add[0].id;
    return contactId ? [await makeRequest({ apiToken, subdomain }, import_pieces_common3.HttpMethod.GET, `/contacts/${contactId}`)] : [];
  },
  async test(context) {
    var _a, _b;
    let { subdomain, apiToken } = context.auth.props, response = await makeRequest({ subdomain, apiToken }, import_pieces_common3.HttpMethod.GET, "/contacts?limit=5&order[updated_at]=desc");
    return (_b = (_a = response == null ? void 0 : response._embedded) == null ? void 0 : _a.contacts) != null ? _b : [];
  },
  sampleData: {
    id: 722830,
    name: "John Doe",
    first_name: "",
    last_name: "",
    responsible_user_id: 13290567,
    group_id: 0,
    created_by: 13290567,
    updated_by: 13290567,
    created_at: 1748800060,
    updated_at: 1748800060,
    closest_task_at: null,
    is_deleted: !1,
    is_unsorted: !1,
    custom_fields_values: null,
    account_id: 34678947,
    _links: {
      self: {
        href: ""
      }
    },
    _embedded: {
      tags: [],
      companies: [
        {
          id: 722828,
          _links: {
            self: {
              href: ""
            }
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/triggers/new-lead-created.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), newLeadCreatedTrigger = (0, import_pieces_framework4.createTrigger)({
  auth: kommoAuth,
  name: "new_lead_created",
  displayName: "New Lead Created",
  description: "Triggers when a new lead is created.",
  aiMetadata: {
    description: "Fires when a new lead (deal) is created in the Kommo CRM account, emitting the full new lead record. Represents a new sales opportunity entering the CRM, regardless of source."
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let { subdomain, apiToken } = context.auth.props, webhook = await makeRequest(
      { subdomain, apiToken },
      import_pieces_common4.HttpMethod.POST,
      "/webhooks",
      {
        destination: context.webhookUrl,
        settings: ["add_lead"]
      }
    );
    await context.store.put("webhookId", webhook.id);
  },
  async onDisable(context) {
    let { subdomain, apiToken } = context.auth.props;
    await context.store.get("webhookId") && await makeRequest(
      { subdomain, apiToken },
      import_pieces_common4.HttpMethod.DELETE,
      "/webhooks",
      { destination: context.webhookUrl }
    );
  },
  async run(context) {
    let { subdomain, apiToken } = context.auth.props, leadId = context.payload.body.leads.add[0].id;
    return leadId ? [await makeRequest({ apiToken, subdomain }, import_pieces_common4.HttpMethod.GET, `/leads/${leadId}`)] : [];
  },
  async test(context) {
    var _a, _b;
    let { subdomain, apiToken } = context.auth.props, response = await makeRequest({ subdomain, apiToken }, import_pieces_common4.HttpMethod.GET, "/leads?limit=5&order[updated_at]=desc");
    return (_b = (_a = response == null ? void 0 : response._embedded) == null ? void 0 : _a.leads) != null ? _b : [];
  },
  sampleData: {
    id: 256988,
    name: "John Doe",
    price: 100,
    responsible_user_id: 13290567,
    group_id: 0,
    status_id: 86521115,
    pipeline_id: 11273979,
    loss_reason_id: null,
    created_by: 13290567,
    updated_by: 13290567,
    created_at: 1748800059,
    updated_at: 1748800060,
    closed_at: null,
    closest_task_at: null,
    is_deleted: !1,
    custom_fields_values: null,
    score: null,
    account_id: 34678947,
    labor_cost: null,
    is_price_computed: !1,
    _links: {
      self: {
        href: ""
      }
    },
    _embedded: {
      tags: [],
      companies: [
        {
          id: 722828,
          _links: {
            self: {
              href: ""
            }
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/triggers/new-task-created.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), newTaskCreatedTrigger = (0, import_pieces_framework5.createTrigger)({
  auth: kommoAuth,
  name: "new_task_created",
  displayName: "New Task Created",
  description: "Triggered when a new task is created.",
  aiMetadata: {
    description: "Fires when a new task is created in the Kommo CRM account, emitting the full new task record (including its linked entity and due date). Represents a follow-up or to-do being scheduled against a lead, contact, or company."
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let { subdomain, apiToken } = context.auth.props, webhook = await makeRequest(
      { subdomain, apiToken },
      import_pieces_common5.HttpMethod.POST,
      "/webhooks",
      {
        destination: context.webhookUrl,
        settings: ["add_task"]
      }
    );
    await context.store.put("webhookId", webhook.id);
  },
  async onDisable(context) {
    let { subdomain, apiToken } = context.auth.props;
    await context.store.get("webhookId") && await makeRequest(
      { subdomain, apiToken },
      import_pieces_common5.HttpMethod.DELETE,
      "/webhooks",
      { destination: context.webhookUrl }
    );
  },
  async run(context) {
    let { subdomain, apiToken } = context.auth.props, taskId = context.payload.body.task.add[0].id;
    return taskId ? [await makeRequest({ apiToken, subdomain }, import_pieces_common5.HttpMethod.GET, `/tasks/${taskId}`)] : [];
  },
  sampleData: {
    id: 12040,
    created_by: 13290567,
    updated_by: 13290567,
    created_at: 1748805952,
    updated_at: 1748805952,
    responsible_user_id: 13290567,
    group_id: 0,
    entity_id: 722830,
    entity_type: "contacts",
    duration: 0,
    is_completed: !1,
    task_type_id: 1,
    text: "Test",
    result: [],
    complete_till: 1748975340,
    account_id: 34678947,
    _links: {
      self: {
        href: ""
      }
    }
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/actions/find-lead.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var findLeadAction = (0, import_pieces_framework6.createAction)({
  auth: kommoAuth,
  name: "find_lead",
  displayName: "Find Lead",
  description: "Finds an existing lead.",
  audience: "both",
  aiMetadata: { description: "Searches leads in a Kommo CRM account by a free-text query matched against the leads' filled fields, returning all matching leads. Use to look up a lead (e.g. by name, contact info, or other field content) before referencing, updating, or reporting on it; the query is required. Read-only and idempotent.", idempotent: !0 },
  props: {
    query: import_pieces_framework6.Property.ShortText({
      displayName: "Query",
      required: !0,
      description: "Search query (Searches through the filled fields of the lead)."
    })
  },
  async run(context) {
    var _a, _b;
    let { subdomain, apiToken } = context.auth.props, result = await makeRequest(
      { apiToken, subdomain },
      import_pieces_common6.HttpMethod.GET,
      `/leads?query=${encodeURIComponent(context.propsValue.query)}`
    ), leads = (_b = (_a = result == null ? void 0 : result._embedded) == null ? void 0 : _a.leads) != null ? _b : [];
    return {
      found: leads.length > 0,
      result: leads
    };
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/actions/update-contact.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/kommo/src/lib/common/props.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var pipelineDropdown = (required = !1) => import_pieces_framework7.Property.Dropdown({
  auth: kommoAuth,
  displayName: "Pipeline",
  required,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your Kommo account",
        options: []
      };
    let { subdomain, apiToken } = auth.props;
    return {
      disabled: !1,
      options: (((_a = (await makeRequest({ subdomain, apiToken }, import_pieces_common7.HttpMethod.GET, "/leads/pipelines"))._embedded) == null ? void 0 : _a.pipelines) || []).map(
        (pipeline) => ({
          label: pipeline.name,
          value: pipeline.id
        })
      )
    };
  }
}), statusDropdown = (required = !1) => import_pieces_framework7.Property.Dropdown({
  auth: kommoAuth,
  displayName: "Status",
  required,
  refreshers: ["pipelineId"],
  options: async ({ auth, pipelineId }) => {
    var _a;
    if (!auth || !pipelineId)
      return {
        disabled: !0,
        placeholder: "Select a pipeline first",
        options: []
      };
    let { subdomain, apiToken } = auth.props;
    return {
      disabled: !1,
      options: (((_a = (await makeRequest(
        { subdomain, apiToken },
        import_pieces_common7.HttpMethod.GET,
        `/leads/pipelines/${pipelineId}/statuses`
      ))._embedded) == null ? void 0 : _a.statuses) || []).map(
        (status) => ({
          label: status.name,
          value: status.id
        })
      )
    };
  }
}), userDropdown = (required = !1) => import_pieces_framework7.Property.Dropdown({
  auth: kommoAuth,
  displayName: "Unique identified of a responsible user",
  required,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your Kommo account",
        options: []
      };
    let { subdomain, apiToken } = auth.props;
    return {
      disabled: !1,
      options: (((_a = (await makeRequest({ subdomain, apiToken }, import_pieces_common7.HttpMethod.GET, "/users"))._embedded) == null ? void 0 : _a.users) || []).map((user) => ({
        label: `${user.name} (${user.email})`,
        value: user.id
      }))
    };
  }
}), lossReasonDropdown = (required = !1) => import_pieces_framework7.Property.Dropdown({
  auth: kommoAuth,
  displayName: "Loss Reason",
  required,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your Kommo account",
        options: []
      };
    let { subdomain, apiToken } = auth.props;
    return {
      disabled: !1,
      options: (((_a = (await makeRequest({ subdomain, apiToken }, import_pieces_common7.HttpMethod.GET, "/leads/loss_reasons"))._embedded) == null ? void 0 : _a.loss_reasons) || []).map(
        (reason) => ({
          label: reason.name,
          value: reason.id
        })
      )
    };
  }
}), leadDropdown = import_pieces_framework7.Property.Dropdown({
  auth: kommoAuth,
  displayName: "Lead",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your Kommo account",
        options: []
      };
    let { subdomain, apiToken } = auth.props;
    return {
      disabled: !1,
      options: (((_a = (await makeRequest({ subdomain, apiToken }, import_pieces_common7.HttpMethod.GET, "/leads"))._embedded) == null ? void 0 : _a.leads) || []).map((lead) => ({
        label: lead.name,
        value: lead.id
      }))
    };
  }
}), companyDropdown = import_pieces_framework7.Property.Dropdown({
  auth: kommoAuth,
  displayName: "Company",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your Kommo account",
        options: []
      };
    let { subdomain, apiToken } = auth.props;
    return {
      disabled: !1,
      options: (((_a = (await makeRequest({ subdomain, apiToken }, import_pieces_common7.HttpMethod.GET, "/companies"))._embedded) == null ? void 0 : _a.companies) || []).map(
        (company) => ({
          label: company.name,
          value: company.id.toString()
        })
      )
    };
  }
}), contactDropdown = import_pieces_framework7.Property.Dropdown({
  auth: kommoAuth,
  displayName: "Contact",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your Kommo account",
        options: []
      };
    let { subdomain, apiToken } = auth.props;
    return {
      disabled: !1,
      options: (((_a = (await makeRequest({ subdomain, apiToken }, import_pieces_common7.HttpMethod.GET, "/contacts"))._embedded) == null ? void 0 : _a.contacts) || []).map(
        (contact) => ({
          label: contact.name,
          value: contact.id
        })
      )
    };
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/actions/update-contact.ts
var updateContactAction = (0, import_pieces_framework8.createAction)({
  auth: kommoAuth,
  name: "update_contact",
  displayName: "Update Contact",
  description: "Updates an existing contact.",
  audience: "both",
  aiMetadata: { description: "Updates an existing Kommo CRM contact identified by contact ID, changing only the supplied fields (name, email/phone, responsible user) and adding or removing tags. Use to amend a known contact; resolve the contact ID first via Find Contact if unknown. Idempotent \u2014 repeating the same update yields the same contact state, though tag add/delete depends on the current tag set.", idempotent: !0 },
  props: {
    contactId: contactDropdown,
    name: import_pieces_framework8.Property.ShortText({
      displayName: "Full Name",
      required: !1
    }),
    first_name: import_pieces_framework8.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework8.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    phone: import_pieces_framework8.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    responsible_user_id: userDropdown(),
    tags_to_add: import_pieces_framework8.Property.Array({
      displayName: "Tags to Add",
      description: "List of tag names or IDs to add.",
      required: !1
    }),
    tags_to_delete: import_pieces_framework8.Property.Array({
      displayName: "Tags to Delete",
      description: "List of tag names or IDs to remove.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b;
    let {
      contactId,
      name,
      first_name,
      last_name,
      email,
      phone,
      responsible_user_id
    } = context.propsValue, tagsToAdd = (_a = context.propsValue.tags_to_add) != null ? _a : [], tagsToDelete = (_b = context.propsValue.tags_to_delete) != null ? _b : [], { subdomain, apiToken } = context.auth.props, customFields = [];
    email && customFields.push({
      field_code: "EMAIL",
      values: [{ value: email }]
    }), phone && customFields.push({
      field_code: "PHONE",
      values: [{ value: phone }]
    });
    let updatePayload = __spreadValues({}, customFields.length > 0 ? { custom_fields_values: customFields } : {});
    return name && (updatePayload.name = name), first_name && (updatePayload.first_name = first_name), last_name && (updatePayload.last_name = last_name), responsible_user_id && (updatePayload.responsible_user_id = responsible_user_id), tagsToAdd.length > 0 && (updatePayload.tags_to_add = tagsToAdd.map((tag) => ({ name: tag }))), tagsToDelete.length > 0 && (updatePayload.tags_to_delete = tagsToDelete.map((tag) => ({ name: tag }))), await makeRequest(
      { subdomain, apiToken },
      import_pieces_common8.HttpMethod.PATCH,
      `/contacts/${contactId}`,
      updatePayload
    );
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/actions/create-new-lead.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var createLeadAction = (0, import_pieces_framework9.createAction)({
  auth: kommoAuth,
  name: "create_lead",
  displayName: "Create New Lead",
  description: "Creates a new lead.",
  audience: "both",
  aiMetadata: { description: "Creates a new lead (deal) in a Kommo CRM account with name, optional price, pipeline/status, responsible user, loss reason, and tags. Use when registering a new sales opportunity. Not idempotent \u2014 each call creates a separate lead even with identical input.", idempotent: !1 },
  props: {
    name: import_pieces_framework9.Property.ShortText({
      displayName: "Lead Name",
      required: !0
    }),
    price: import_pieces_framework9.Property.Number({
      displayName: "Price",
      required: !1
    }),
    pipelineId: pipelineDropdown(!0),
    statusId: statusDropdown(!0),
    responsible_user_id: userDropdown(),
    loss_reason_id: lossReasonDropdown(),
    tags_to_add: import_pieces_framework9.Property.Array({
      displayName: "Tags to Add",
      description: "List of tags to add.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { name, price, statusId, pipelineId, loss_reason_id, responsible_user_id } = context.propsValue, tagsToAdd = (_a = context.propsValue.tags_to_add) != null ? _a : [], { apiToken, subdomain } = context.auth.props, body = {
      name
    };
    return price && (body.price = price), statusId && (body.status_id = statusId), pipelineId && (body.pipeline_id = pipelineId), loss_reason_id && (body.loss_reason_id = loss_reason_id), responsible_user_id && (body.responsible_user_id = responsible_user_id), tagsToAdd.length > 0 && (body.tags_to_add = tagsToAdd.map((tag) => ({ name: tag }))), await makeRequest({ apiToken, subdomain }, import_pieces_common9.HttpMethod.POST, "/leads", [body]);
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/actions/create-new-contact.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var createContactAction = (0, import_pieces_framework10.createAction)({
  auth: kommoAuth,
  name: "create_contact",
  displayName: "Create New Contact",
  description: "Add a new contact.",
  audience: "both",
  aiMetadata: { description: "Creates a new contact in a Kommo CRM account with name, optional email/phone (stored as custom fields), responsible user, and tags. Use when adding a person not already in the CRM; consider finding the contact first to avoid duplicates. Not idempotent \u2014 each call creates a separate contact even with identical input.", idempotent: !1 },
  props: {
    name: import_pieces_framework10.Property.ShortText({
      displayName: "Full Name",
      required: !0
    }),
    first_name: import_pieces_framework10.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework10.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    email: import_pieces_framework10.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    phone: import_pieces_framework10.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    responsible_user_id: userDropdown(),
    tags_to_add: import_pieces_framework10.Property.Array({
      displayName: "Tags to Add",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let {
      name,
      first_name,
      last_name,
      email,
      phone,
      responsible_user_id
    } = context.propsValue, tagsToAdd = (_a = context.propsValue.tags_to_add) != null ? _a : [], { subdomain, apiToken } = context.auth.props, customFields = [];
    email && customFields.push({
      field_code: "EMAIL",
      values: [{ value: email }]
    }), phone && customFields.push({
      field_code: "PHONE",
      values: [{ value: phone }]
    });
    let contactPayload = __spreadValues({}, customFields.length > 0 ? { custom_fields_values: customFields } : {});
    return name && (contactPayload.name = name), first_name && (contactPayload.first_name = first_name), last_name && (contactPayload.last_name = last_name), responsible_user_id && (contactPayload.responsible_user_id = responsible_user_id), tagsToAdd.length > 0 && (contactPayload.tags_to_add = tagsToAdd.map((tag) => ({ name: tag }))), await makeRequest(
      { apiToken, subdomain },
      import_pieces_common10.HttpMethod.POST,
      "/contacts",
      [contactPayload]
    );
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/actions/find-contact.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), findContactAction = (0, import_pieces_framework11.createAction)({
  auth: kommoAuth,
  name: "find_contact",
  displayName: "Find Contact",
  description: "Finds an existing contact.",
  audience: "both",
  aiMetadata: { description: "Searches contacts in a Kommo CRM account by a free-text query matched against the contacts' filled fields, returning all matching contacts. Use to resolve a contact (e.g. by name, email, or phone) before referencing or updating it; the query is required. Read-only and idempotent.", idempotent: !0 },
  props: {
    query: import_pieces_framework11.Property.ShortText({
      displayName: "Query",
      required: !0,
      description: "Search query (Searches through the filled fields of the contact)."
    })
  },
  async run(context) {
    var _a, _b;
    let { query } = context.propsValue, { subdomain, apiToken } = context.auth.props, result = await makeRequest(
      { apiToken, subdomain },
      import_pieces_common11.HttpMethod.GET,
      `/contacts?query=${encodeURIComponent(query)}`
    ), contacts = (_b = (_a = result == null ? void 0 : result._embedded) == null ? void 0 : _a.contacts) != null ? _b : [];
    return {
      found: contacts.length > 0,
      result: contacts
    };
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/actions/find-company.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var findCompanyAction = (0, import_pieces_framework12.createAction)({
  auth: kommoAuth,
  name: "find_company",
  displayName: "Find Company",
  description: "Find an existing company.",
  audience: "both",
  aiMetadata: { description: "Searches companies in a Kommo CRM account by a free-text query matched against the companies' filled fields, returning all matching companies. Use to resolve a company record (e.g. by name) before referencing or linking it; the query is required. Read-only and idempotent.", idempotent: !0 },
  props: {
    query: import_pieces_framework12.Property.ShortText({
      displayName: "Query",
      required: !0,
      description: "Search query (Searches through the filled fields of the company)."
    })
  },
  async run(context) {
    var _a, _b;
    let { query } = context.propsValue, { subdomain, apiToken } = context.auth.props, result = await makeRequest(
      { apiToken, subdomain },
      import_pieces_common12.HttpMethod.GET,
      `/companies?query=${encodeURIComponent(query || "")}`
    ), companies = (_b = (_a = result == null ? void 0 : result._embedded) == null ? void 0 : _a.companies) != null ? _b : [];
    return {
      found: companies.length > 0,
      result: companies
    };
  }
});

// ../ap/packages/pieces/community/kommo/src/lib/actions/update-lead.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var updateLeadAction = (0, import_pieces_framework13.createAction)({
  auth: kommoAuth,
  name: "update_lead",
  displayName: "Update Lead",
  description: "Update existing lead info.",
  audience: "both",
  aiMetadata: { description: "Updates an existing Kommo CRM lead (deal) identified by lead ID, changing only the supplied fields (name, price, pipeline/status, responsible user, loss reason) and adding or removing tags. Use to move a lead through its pipeline or amend its details; resolve the lead ID first via Find Lead if unknown. Idempotent \u2014 repeating the same update yields the same lead state.", idempotent: !0 },
  props: {
    leadId: leadDropdown,
    name: import_pieces_framework13.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    price: import_pieces_framework13.Property.Number({
      displayName: "Price",
      required: !1
    }),
    pipelineId: pipelineDropdown(!1),
    statusId: statusDropdown(!1),
    responsible_user_id: userDropdown(!1),
    loss_reason_id: lossReasonDropdown(!1),
    tags_to_add: import_pieces_framework13.Property.Array({
      displayName: "Tags to Add",
      required: !1
    }),
    tags_to_delete: import_pieces_framework13.Property.Array({
      displayName: "Tags to Delete",
      required: !1
    })
  },
  async run(context) {
    var _a, _b;
    let {
      leadId,
      name,
      price,
      statusId,
      pipelineId,
      responsible_user_id,
      loss_reason_id
    } = context.propsValue, tagsToAdd = (_a = context.propsValue.tags_to_add) != null ? _a : [], tagsToDelete = (_b = context.propsValue.tags_to_delete) != null ? _b : [], { subdomain, apiToken } = context.auth.props, updatePayload = {};
    return name && (updatePayload.name = name), price && (updatePayload.price = price), statusId && (updatePayload.status_id = statusId), pipelineId && (updatePayload.pipeline_id = pipelineId), loss_reason_id && (updatePayload.loss_reason_id = loss_reason_id), responsible_user_id && (updatePayload.responsible_user_id = responsible_user_id), tagsToAdd.length > 0 && (updatePayload.tags_to_add = tagsToAdd.map((tag) => ({ name: tag }))), tagsToDelete.length > 0 && (updatePayload.tags_to_delete = tagsToDelete.map((tag) => ({ name: tag }))), await makeRequest(
      { apiToken, subdomain },
      import_pieces_common13.HttpMethod.PATCH,
      `/leads/${leadId}`,
      updatePayload
    );
  }
});

// ../ap/packages/pieces/community/kommo/src/index.ts
var import_pieces_common14 = require("@activepieces/pieces-common");
var kommo = (0, import_pieces_framework14.createPiece)({
  displayName: "Kommo",
  auth: kommoAuth,
  logoUrl: "https://cdn.activepieces.com/pieces/kommo.png",
  categories: [import_pieces_framework15.PieceCategory.COMMUNICATION, import_pieces_framework15.PieceCategory.SALES_AND_CRM],
  authors: ["krushnarout", "kishanprmr"],
  actions: [
    findLeadAction,
    updateContactAction,
    createLeadAction,
    updateLeadAction,
    createContactAction,
    findContactAction,
    findCompanyAction,
    (0, import_pieces_common14.createCustomApiCallAction)({
      auth: kommoAuth,
      baseUrl: (auth) => auth ? `https://${auth.props.subdomain}.kommo.com/api/v4` : "",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.apiToken}`
      })
    })
  ],
  triggers: [leadStatusChangedTrigger, newContactAddedTrigger, newLeadCreatedTrigger, newTaskCreatedTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  kommo
});
