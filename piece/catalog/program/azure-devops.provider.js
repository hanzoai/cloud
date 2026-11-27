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

// ../ap/packages/pieces/community/azure-devops/src/index.ts
var index_exports = {};
__export(index_exports, {
  azureDevOps: () => azureDevOps
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/azure-devops/src/lib/actions/create-work-item.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/azure-devops/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
function asAzureAuth(auth) {
  return auth;
}
function narrowString(value, fieldName) {
  if (typeof value != "string" || value.length === 0)
    throw new Error(`Expected non-empty string for "${fieldName}", got ${typeof value}`);
  return value;
}
function isRecord(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}
function generateWebhookToken() {
  let bytes = new Uint8Array(32);
  return crypto.getRandomValues(bytes), Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}
function timingSafeEqual(a, b) {
  if (a.length !== b.length) return !1;
  let diff = 0;
  for (let i = 0; i < a.length; i++)
    diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}
function extractHttpStatus(error) {
  var _a, _b;
  if (typeof error == "object" && error !== null) {
    let err = error, status = (_b = (_a = err.response) == null ? void 0 : _a.status) != null ? _b : err.status;
    if (typeof status == "number") return ` (HTTP ${status})`;
  }
  return "";
}
function parseWebhookPayload(body) {
  if (!isRecord(body)) return null;
  let { eventType, resource } = body;
  return typeof eventType != "string" || !isRecord(resource) || eventType !== "workitem.created" && eventType !== "workitem.updated" && eventType !== "workitem.commented" ? null : {
    eventType,
    resource
  };
}
var authDescription = `To get your Personal Access Token (PAT):

1. Go to **Azure DevOps** and click your profile icon (top-right)
2. Select **Personal access tokens**
3. Click **+ New Token**
4. Give it a name and set expiration
5. Under **Scopes**, select:
   - **Work Items**: Read & Write
   - **Project and Team**: Read
6. Click **Create** and copy the token (you won't see it again)

**Organization URL** is your Azure DevOps URL, e.g. \`https://dev.azure.com/mycompany\``, azureDevOpsAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: authDescription,
  required: !0,
  props: {
    organizationUrl: import_pieces_framework.Property.ShortText({
      displayName: "Organization URL",
      description: "Your Azure DevOps organization URL (e.g. https://dev.azure.com/mycompany)",
      required: !0
    }),
    pat: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Personal Access Token",
      description: "Your Azure DevOps PAT with Work Items (Read & Write) and Project and Team (Read) scopes.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    if (!isValidUrl(auth.organizationUrl))
      return { valid: !1, error: "Organization URL must be a valid HTTP/HTTPS URL" };
    let orgUrl = sanitizeOrgUrl(auth.organizationUrl), encoded = Buffer.from(`:${auth.pat}`).toString("base64");
    try {
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${orgUrl}/_apis/projects`,
        headers: { Authorization: `Basic ${encoded}` },
        queryParams: { "api-version": "7.1" }
      })).body.value !== void 0 ? { valid: !0 } : { valid: !1, error: "Invalid PAT or Organization URL" };
    } catch (e) {
      return { valid: !1, error: "Invalid PAT or Organization URL" };
    }
  }
});
async function azureDevOpsApiCall({
  organizationUrl,
  pat,
  method,
  endpoint,
  body,
  queryParams,
  isJsonPatch = !1
}) {
  let baseUrl = sanitizeOrgUrl(organizationUrl), encoded = Buffer.from(`:${pat}`).toString("base64"), qs = {};
  if (queryParams)
    for (let [key, value] of Object.entries(queryParams))
      value != null && (qs[key] = value);
  let headers = {
    Authorization: `Basic ${encoded}`
  };
  return body !== void 0 && (headers["Content-Type"] = isJsonPatch ? "application/json-patch+json" : "application/json"), (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${baseUrl}${endpoint}`,
    headers,
    queryParams: qs,
    body
  })).body;
}
async function fetchProjects(auth) {
  return azureDevOpsApiCall({
    organizationUrl: auth.props.organizationUrl,
    pat: auth.props.pat,
    method: import_pieces_common.HttpMethod.GET,
    endpoint: "/_apis/projects",
    queryParams: { "api-version": "7.1" }
  });
}
async function fetchWorkItemTypes(auth, project) {
  return azureDevOpsApiCall({
    organizationUrl: auth.props.organizationUrl,
    pat: auth.props.pat,
    method: import_pieces_common.HttpMethod.GET,
    endpoint: `/${encodeURIComponent(project)}/_apis/wit/workitemtypes`,
    queryParams: { "api-version": "7.1" }
  });
}
function flattenWorkItem(workItem) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t;
  let fields = workItem.fields;
  return {
    id: workItem.id,
    rev: workItem.rev,
    url: (_a = workItem.url) != null ? _a : null,
    title: (_b = fields["System.Title"]) != null ? _b : null,
    work_item_type: (_c = fields["System.WorkItemType"]) != null ? _c : null,
    state: (_d = fields["System.State"]) != null ? _d : null,
    reason: (_e = fields["System.Reason"]) != null ? _e : null,
    assigned_to: (_g = (_f = fields["System.AssignedTo"]) == null ? void 0 : _f.displayName) != null ? _g : null,
    assigned_to_email: (_i = (_h = fields["System.AssignedTo"]) == null ? void 0 : _h.uniqueName) != null ? _i : null,
    created_date: (_j = fields["System.CreatedDate"]) != null ? _j : null,
    created_by: (_l = (_k = fields["System.CreatedBy"]) == null ? void 0 : _k.displayName) != null ? _l : null,
    changed_date: (_m = fields["System.ChangedDate"]) != null ? _m : null,
    changed_by: (_o = (_n = fields["System.ChangedBy"]) == null ? void 0 : _n.displayName) != null ? _o : null,
    area_path: (_p = fields["System.AreaPath"]) != null ? _p : null,
    iteration_path: (_q = fields["System.IterationPath"]) != null ? _q : null,
    priority: (_r = fields["Microsoft.VSTS.Common.Priority"]) != null ? _r : null,
    description: (_s = fields["System.Description"]) != null ? _s : null,
    project: (_t = fields["System.TeamProject"]) != null ? _t : null
  };
}
async function fetchWorkItemsByIds({
  organizationUrl,
  pat,
  ids
}) {
  if (ids.length === 0)
    return [];
  let results = [], batchSize = 200;
  for (let i = 0; i < ids.length; i += batchSize) {
    let batch = ids.slice(i, i + batchSize), response = await azureDevOpsApiCall({
      organizationUrl,
      pat,
      method: import_pieces_common.HttpMethod.GET,
      endpoint: "/_apis/wit/workitems",
      queryParams: {
        ids: batch.join(","),
        $expand: "all",
        "api-version": "7.1"
      }
    });
    response.value && results.push(...response.value.map(flattenWorkItem));
  }
  return results;
}
async function createSubscription({
  auth,
  projectId,
  eventType,
  webhookUrl,
  workItemType,
  basicAuthUsername,
  basicAuthPassword
}) {
  let publisherInputs = { projectId };
  workItemType && (publisherInputs.workItemType = workItemType);
  let consumerInputs = {
    url: webhookUrl,
    resourceDetailsToSend: "all",
    messagesToSend: "none",
    detailedMessagesToSend: "none"
  };
  return basicAuthUsername !== void 0 && (consumerInputs.basicAuthUsername = basicAuthUsername), basicAuthPassword !== void 0 && (consumerInputs.basicAuthPassword = basicAuthPassword), azureDevOpsApiCall({
    organizationUrl: auth.props.organizationUrl,
    pat: auth.props.pat,
    method: import_pieces_common.HttpMethod.POST,
    endpoint: "/_apis/hooks/subscriptions",
    queryParams: { "api-version": "7.1" },
    body: {
      publisherId: "tfs",
      eventType,
      resourceVersion: "1.0",
      consumerId: "webHooks",
      consumerActionId: "httpRequest",
      publisherInputs,
      consumerInputs
    }
  });
}
async function deleteSubscription({
  auth,
  subscriptionId
}) {
  await azureDevOpsApiCall({
    organizationUrl: auth.props.organizationUrl,
    pat: auth.props.pat,
    method: import_pieces_common.HttpMethod.DELETE,
    endpoint: `/_apis/hooks/subscriptions/${subscriptionId}`,
    queryParams: { "api-version": "7.1" }
  });
}
async function fetchProjectId(auth, projectName) {
  var _a;
  return (_a = (await azureDevOpsApiCall({
    organizationUrl: auth.props.organizationUrl,
    pat: auth.props.pat,
    method: import_pieces_common.HttpMethod.GET,
    endpoint: `/_apis/projects/${encodeURIComponent(projectName)}`,
    queryParams: { "api-version": "7.1" }
  })).id) != null ? _a : null;
}
var MAX_TEAMS_FOR_MEMBER_DROPDOWN = 25, MEMBER_FETCH_CONCURRENCY = 5;
async function fetchTeamMembers(auth, projectName) {
  var _a, _b;
  let teams = (_a = (await azureDevOpsApiCall({
    organizationUrl: auth.props.organizationUrl,
    pat: auth.props.pat,
    method: import_pieces_common.HttpMethod.GET,
    endpoint: `/_apis/projects/${encodeURIComponent(projectName)}/teams`,
    queryParams: {
      "api-version": "7.1",
      $top: String(MAX_TEAMS_FOR_MEMBER_DROPDOWN)
    }
  })).value) != null ? _a : [], seen = /* @__PURE__ */ new Map();
  for (let i = 0; i < teams.length; i += MEMBER_FETCH_CONCURRENCY) {
    let batch = teams.slice(i, i + MEMBER_FETCH_CONCURRENCY), results = await Promise.all(
      batch.map(
        (team) => azureDevOpsApiCall({
          organizationUrl: auth.props.organizationUrl,
          pat: auth.props.pat,
          method: import_pieces_common.HttpMethod.GET,
          endpoint: `/_apis/projects/${encodeURIComponent(projectName)}/teams/${team.id}/members`,
          queryParams: { "api-version": "7.1", $top: "200" }
        })
      )
    );
    for (let response of results)
      for (let member of (_b = response.value) != null ? _b : []) {
        let identity = member.identity;
        identity != null && identity.id && !seen.has(identity.id) && seen.set(identity.id, identity);
      }
  }
  return Array.from(seen.values());
}
async function fetchWorkItemTypeStates(auth, projectName, workItemType) {
  var _a;
  return (_a = (await azureDevOpsApiCall({
    organizationUrl: auth.props.organizationUrl,
    pat: auth.props.pat,
    method: import_pieces_common.HttpMethod.GET,
    endpoint: `/${encodeURIComponent(projectName)}/_apis/wit/workitemtypes/${encodeURIComponent(workItemType)}`,
    queryParams: { "api-version": "7.1" }
  })).states) != null ? _a : [];
}
function createAssignedToDropdown(required) {
  return import_pieces_framework.Property.Dropdown({
    displayName: "Assigned To",
    description: "Select the person to assign this work item to. Only project team members are listed \u2014 use the Custom API Call action if you need to assign a user outside your teams.",
    refreshers: ["project"],
    required,
    auth: azureDevOpsAuth,
    options: async ({ auth, project }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your Azure DevOps account first"
        };
      if (!project)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please select a project first"
        };
      let typedAuth = auth;
      try {
        let members = await fetchTeamMembers(typedAuth, narrowString(project, "project"));
        return members.length === 0 ? {
          disabled: !1,
          options: [],
          placeholder: "No team members found in this project."
        } : {
          disabled: !1,
          options: members.map((m) => {
            var _a;
            return {
              label: m.uniqueName ? `${m.displayName} (${m.uniqueName})` : m.displayName,
              value: (_a = m.uniqueName) != null ? _a : m.displayName
            };
          })
        };
      } catch (error) {
        return {
          disabled: !0,
          options: [],
          placeholder: `Load failed${extractHttpStatus(error)}. Check PAT.`
        };
      }
    }
  });
}
function createStateDropdown(required, description) {
  return import_pieces_framework.Property.Dropdown({
    displayName: "State",
    description,
    refreshers: ["project", "work_item_type"],
    required,
    auth: azureDevOpsAuth,
    options: async ({ auth, project, work_item_type }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your Azure DevOps account first"
        };
      if (!project)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please select a project first"
        };
      if (!work_item_type)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please select a work item type first"
        };
      let typedAuth = auth;
      try {
        let states = await fetchWorkItemTypeStates(
          typedAuth,
          narrowString(project, "project"),
          narrowString(work_item_type, "work_item_type")
        );
        return states.length === 0 ? {
          disabled: !1,
          options: [],
          placeholder: "No states defined for this work item type."
        } : {
          disabled: !1,
          options: states.map((s) => ({
            label: s.category ? `${s.name} (${s.category})` : s.name,
            value: s.name
          }))
        };
      } catch (error) {
        return {
          disabled: !0,
          options: [],
          placeholder: `Load failed${extractHttpStatus(error)}. Check PAT.`
        };
      }
    }
  });
}
function createWorkItemTypeDropdown(required, description) {
  return import_pieces_framework.Property.Dropdown({
    displayName: "Work Item Type",
    description,
    refreshers: ["project"],
    required,
    auth: azureDevOpsAuth,
    options: async ({ auth, project }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your Azure DevOps account first"
        };
      if (!project)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please select a project first"
        };
      let typedAuth = auth;
      try {
        let response = await fetchWorkItemTypes(typedAuth, narrowString(project, "project"));
        return !response.value || response.value.length === 0 ? {
          disabled: !1,
          options: [],
          placeholder: "No work item types found."
        } : {
          disabled: !1,
          options: response.value.map((t) => ({
            label: t.name,
            value: t.name
          }))
        };
      } catch (error) {
        return {
          disabled: !0,
          options: [],
          placeholder: `Load failed${extractHttpStatus(error)}. Check PAT.`
        };
      }
    }
  });
}
var azureDevOpsCommon = {
  projectDropdown: import_pieces_framework.Property.Dropdown({
    displayName: "Project",
    description: "Select the Azure DevOps project",
    refreshers: [],
    required: !0,
    auth: azureDevOpsAuth,
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your Azure DevOps account first"
        };
      let typedAuth = auth;
      try {
        let response = await fetchProjects(typedAuth);
        return !response.value || response.value.length === 0 ? {
          disabled: !1,
          options: [],
          placeholder: "No projects found. Create one in Azure DevOps first."
        } : {
          disabled: !1,
          options: response.value.map((p) => ({
            label: p.name,
            value: p.name
          }))
        };
      } catch (error) {
        return {
          disabled: !0,
          options: [],
          placeholder: `Load failed${extractHttpStatus(error)}. Check PAT.`
        };
      }
    }
  }),
  workItemTypeDropdown: createWorkItemTypeDropdown(
    !0,
    "Select the type of work item (e.g. Bug, Task, User Story)"
  ),
  workItemTypeDropdownOptional: createWorkItemTypeDropdown(
    !1,
    "Filter by work item type. Leave empty to trigger for all types."
  ),
  assignedToDropdown: createAssignedToDropdown(!1),
  stateDropdown: createStateDropdown(
    !1,
    "New state for the work item. Leave empty to keep the current state."
  ),
  stateDropdownOptional: createStateDropdown(
    !1,
    "Filter by state. Leave empty to trigger for all states."
  ),
  priorityDropdown: import_pieces_framework.Property.StaticDropdown({
    displayName: "Priority",
    description: "The priority of the work item",
    required: !1,
    options: {
      options: [
        { label: "1 - Critical", value: "1" },
        { label: "2 - High", value: "2" },
        { label: "3 - Medium", value: "3" },
        { label: "4 - Low", value: "4" }
      ]
    }
  }),
  escapeWiqlString,
  sanitizeOrgUrl,
  isValidUrl,
  asAuth: asAzureAuth,
  narrowString,
  parseWebhookPayload,
  generateWebhookToken,
  timingSafeEqual,
  extractHttpStatus,
  apiCall: azureDevOpsApiCall,
  fetchWorkItemsByIds,
  flattenWorkItem,
  fetchProjectId,
  fetchTeamMembers,
  fetchWorkItemTypeStates,
  createSubscription,
  deleteSubscription
};
function sanitizeOrgUrl(url) {
  return url.replace(/\/+$/, "");
}
function escapeWiqlString(value) {
  return value.replace(/'/g, "''");
}
function isValidUrl(url) {
  try {
    let parsed = new URL(url);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch (e) {
    return !1;
  }
}
var WEBHOOK_EVENT_OPTIONS = [
  { label: "Work Item Created", value: "workitem.created" },
  { label: "Work Item Updated", value: "workitem.updated" },
  { label: "Work Item Commented", value: "workitem.commented" }
];

// ../ap/packages/pieces/community/azure-devops/src/lib/actions/create-work-item.ts
var createWorkItemAction = (0, import_pieces_framework2.createAction)({
  auth: azureDevOpsAuth,
  name: "create_work_item",
  classification: "WRITE",
  displayName: "Create Work Item",
  description: "Creates a new work item (Bug, Task, User Story, etc.) in Azure DevOps",
  audience: "both",
  aiMetadata: { description: "Creates a new work item of the chosen type (Bug, Task, User Story, etc.) in an Azure DevOps project, optionally setting description, assignee, and priority. Use to file a new tracked item. Requires a project and work item type; not idempotent, as each call creates a separate item with a new ID.", idempotent: !1 },
  props: {
    project: azureDevOpsCommon.projectDropdown,
    work_item_type: azureDevOpsCommon.workItemTypeDropdown,
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Title",
      description: "The title of the work item",
      required: !0
    }),
    description: import_pieces_framework2.Property.LongText({
      displayName: "Description",
      description: "Detailed description of the work item (supports HTML)",
      required: !1
    }),
    assigned_to: azureDevOpsCommon.assignedToDropdown,
    priority: azureDevOpsCommon.priorityDropdown
  },
  async run(context) {
    let { project, work_item_type, title, description, assigned_to, priority } = context.propsValue, auth = context.auth, orgUrl = azureDevOpsCommon.sanitizeOrgUrl(auth.props.organizationUrl), operations = [
      { op: "add", path: "/fields/System.Title", value: title }
    ];
    description && operations.push({
      op: "add",
      path: "/fields/System.Description",
      value: description
    }), assigned_to && operations.push({
      op: "add",
      path: "/fields/System.AssignedTo",
      value: assigned_to
    }), priority && operations.push({
      op: "add",
      path: "/fields/Microsoft.VSTS.Common.Priority",
      value: Number(priority)
    });
    let response = await azureDevOpsCommon.apiCall({
      organizationUrl: orgUrl,
      pat: auth.props.pat,
      method: import_pieces_common2.HttpMethod.POST,
      endpoint: `/${encodeURIComponent(project)}/_apis/wit/workitems/$${encodeURIComponent(azureDevOpsCommon.narrowString(work_item_type, "work_item_type"))}`,
      queryParams: { "api-version": "7.1" },
      body: operations,
      isJsonPatch: !0
    });
    return azureDevOpsCommon.flattenWorkItem(response);
  }
});

// ../ap/packages/pieces/community/azure-devops/src/lib/actions/get-work-item.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getWorkItemAction = (0, import_pieces_framework3.createAction)({
  auth: azureDevOpsAuth,
  name: "get_work_item",
  classification: "READ",
  displayName: "Get Work Item",
  description: "Retrieves a work item by ID from Azure DevOps",
  audience: "both",
  aiMetadata: { description: "Fetches a single Azure DevOps work item by its numeric ID, returning its fields (title, state, assignee, etc.). Use to look up details of a known work item; requires the project and the numeric work item ID, not a title. Read-only and idempotent.", idempotent: !0 },
  props: {
    project: azureDevOpsCommon.projectDropdown,
    work_item_id: import_pieces_framework3.Property.Number({
      displayName: "Work Item ID",
      description: "Numeric ID of the work item (e.g. 123). To pass it dynamically, reference the ID from a previous step (e.g. `{{trigger.id}}`), not the title.",
      required: !0
    })
  },
  async run(context) {
    let { project, work_item_id } = context.propsValue, auth = context.auth, orgUrl = azureDevOpsCommon.sanitizeOrgUrl(auth.props.organizationUrl), response = await azureDevOpsCommon.apiCall({
      organizationUrl: orgUrl,
      pat: auth.props.pat,
      method: import_pieces_common3.HttpMethod.GET,
      endpoint: `/${encodeURIComponent(project)}/_apis/wit/workitems/${work_item_id}`,
      queryParams: {
        $expand: "all",
        "api-version": "7.1"
      }
    });
    return azureDevOpsCommon.flattenWorkItem(response);
  }
});

// ../ap/packages/pieces/community/azure-devops/src/lib/actions/update-work-item.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var updateWorkItemAction = (0, import_pieces_framework4.createAction)({
  auth: azureDevOpsAuth,
  name: "update_work_item",
  classification: "WRITE",
  displayName: "Update Work Item",
  description: "Updates an existing work item in Azure DevOps",
  audience: "both",
  aiMetadata: { description: "Updates fields (title, description, state, assignee, priority) on an existing Azure DevOps work item identified by its numeric ID; only the fields you provide are changed. Use to modify a known work item rather than to look it up or create one. Requires the project and numeric work item ID; the work item type prop only loads the matching state list and does not change the item type. Idempotent, since it replaces fields to a fixed target value keyed on a stable ID.", idempotent: !0 },
  props: {
    project: azureDevOpsCommon.projectDropdown,
    work_item_type: __spreadProps(__spreadValues({}, azureDevOpsCommon.workItemTypeDropdown), {
      description: "Type of the work item. Used only to load the matching list of states below \u2014 it does not change the work item's type. Changing type requires a separate Azure DevOps operation not supported here."
    }),
    work_item_id: import_pieces_framework4.Property.Number({
      displayName: "Work Item ID",
      description: "Numeric ID of the work item (e.g. 123). To pass it dynamically from a previous step, use a reference like `{{trigger.id}}` or the output of a Get/List step \u2014 not the title.",
      required: !0
    }),
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Title",
      description: "New title for the work item",
      required: !1
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "New description for the work item (supports HTML)",
      required: !1
    }),
    state: azureDevOpsCommon.stateDropdown,
    assigned_to: azureDevOpsCommon.assignedToDropdown,
    priority: azureDevOpsCommon.priorityDropdown
  },
  async run(context) {
    let { project, work_item_id, title, description, state, assigned_to, priority } = context.propsValue, auth = context.auth, orgUrl = azureDevOpsCommon.sanitizeOrgUrl(auth.props.organizationUrl), encodedProject = encodeURIComponent(project), operations = [];
    if (isNonEmptyString(title) && operations.push({
      op: "replace",
      path: "/fields/System.Title",
      value: title
    }), isNonEmptyString(description) && operations.push({
      op: "replace",
      path: "/fields/System.Description",
      value: description
    }), isNonEmptyString(state) && operations.push({
      op: "replace",
      path: "/fields/System.State",
      value: state
    }), isNonEmptyString(assigned_to) && operations.push({
      op: "replace",
      path: "/fields/System.AssignedTo",
      value: assigned_to
    }), priority != null && operations.push({
      op: "replace",
      path: "/fields/Microsoft.VSTS.Common.Priority",
      value: Number(priority)
    }), operations.length === 0) {
      let getResponse = await azureDevOpsCommon.apiCall({
        organizationUrl: orgUrl,
        pat: auth.props.pat,
        method: import_pieces_common4.HttpMethod.GET,
        endpoint: `/${encodedProject}/_apis/wit/workitems/${work_item_id}`,
        queryParams: {
          $expand: "all",
          "api-version": "7.1"
        }
      });
      return azureDevOpsCommon.flattenWorkItem(getResponse);
    }
    let response = await azureDevOpsCommon.apiCall({
      organizationUrl: orgUrl,
      pat: auth.props.pat,
      method: import_pieces_common4.HttpMethod.PATCH,
      endpoint: `/${encodedProject}/_apis/wit/workitems/${work_item_id}`,
      queryParams: { "api-version": "7.1" },
      body: operations,
      isJsonPatch: !0
    });
    return azureDevOpsCommon.flattenWorkItem(response);
  }
});
function isNonEmptyString(value) {
  return typeof value == "string" && value.length > 0;
}

// ../ap/packages/pieces/community/azure-devops/src/lib/actions/list-work-items.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var DEFAULT_WIQL = "SELECT [System.Id] FROM WorkItems WHERE [System.TeamProject] = @project AND [System.WorkItemType] <> '' ORDER BY [System.ChangedDate] DESC", listWorkItemsAction = (0, import_pieces_framework5.createAction)({
  auth: azureDevOpsAuth,
  name: "list_work_items",
  classification: "SEARCH",
  displayName: "List Work Items",
  description: "Lists work items from Azure DevOps using a WIQL query",
  audience: "both",
  aiMetadata: { description: "Queries work items in an Azure DevOps project and returns their fields. Leave the WIQL query empty to list all work items ordered by most recently changed, or supply a custom WIQL query to filter (by type, state, assignee, etc.). Use to search or enumerate work items; requires a project, and results are capped by the limit (default 50, max 200). Read-only and idempotent.", idempotent: !0 },
  props: {
    project: azureDevOpsCommon.projectDropdown,
    wiql_query: import_pieces_framework5.Property.LongText({
      displayName: "WIQL Query",
      description: "The WIQL query to execute. Default: SELECT [System.Id] FROM WorkItems WHERE [System.TeamProject] = @project ORDER BY [System.ChangedDate] DESC",
      required: !1
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "Maximum number of work items to return (default: 50, max: 200)",
      required: !1,
      defaultValue: 50
    })
  },
  async run(context) {
    let { project, wiql_query, limit } = context.propsValue, auth = context.auth, orgUrl = azureDevOpsCommon.sanitizeOrgUrl(auth.props.organizationUrl), query = wiql_query || DEFAULT_WIQL, maxItems = Math.max(1, Math.min(limit != null ? limit : 50, 200)), wiqlResponse = await azureDevOpsCommon.apiCall({
      organizationUrl: orgUrl,
      pat: auth.props.pat,
      method: import_pieces_common5.HttpMethod.POST,
      endpoint: `/${encodeURIComponent(project)}/_apis/wit/wiql`,
      queryParams: {
        $top: `${maxItems}`,
        "api-version": "7.1"
      },
      body: { query }
    });
    if (!wiqlResponse.workItems || wiqlResponse.workItems.length === 0)
      return [];
    let ids = wiqlResponse.workItems.map((wi) => wi.id);
    return await azureDevOpsCommon.fetchWorkItemsByIds({
      organizationUrl: orgUrl,
      pat: auth.props.pat,
      ids
    });
  }
});

// ../ap/packages/pieces/community/azure-devops/src/lib/actions/add-comment.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var addCommentAction = (0, import_pieces_framework6.createAction)({
  auth: azureDevOpsAuth,
  name: "add_comment",
  classification: "WRITE",
  displayName: "Add Comment",
  description: "Adds a comment to an existing work item in Azure DevOps",
  audience: "both",
  aiMetadata: { description: "Posts a new comment (HTML supported) on an existing Azure DevOps work item identified by its numeric ID. Use to add a note or update to a known work item. Requires the project, numeric work item ID, and comment text; not idempotent, as each call appends a separate comment.", idempotent: !1 },
  props: {
    project: azureDevOpsCommon.projectDropdown,
    work_item_id: import_pieces_framework6.Property.Number({
      displayName: "Work Item ID",
      description: "Numeric ID of the work item (e.g. 123). To pass it dynamically, reference the ID from a previous step (e.g. `{{trigger.id}}`), not the title.",
      required: !0
    }),
    comment: import_pieces_framework6.Property.LongText({
      displayName: "Comment",
      description: "The comment text to add (supports HTML)",
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i;
    let { project, work_item_id, comment } = context.propsValue, auth = context.auth, orgUrl = azureDevOpsCommon.sanitizeOrgUrl(auth.props.organizationUrl), response = await azureDevOpsCommon.apiCall({
      organizationUrl: orgUrl,
      pat: auth.props.pat,
      method: import_pieces_common6.HttpMethod.POST,
      endpoint: `/${encodeURIComponent(project)}/_apis/wit/workItems/${work_item_id}/comments`,
      // Work-item comments remain in preview on REST 7.1; the stable 7.1
      // endpoint does not exist. See
      // https://learn.microsoft.com/en-us/rest/api/azure/devops/wit/comments
      queryParams: { "api-version": "7.1-preview.4" },
      body: { text: comment }
    });
    return {
      id: response.id,
      work_item_id: response.workItemId,
      text: (_a = response.text) != null ? _a : null,
      created_by: (_c = (_b = response.createdBy) == null ? void 0 : _b.displayName) != null ? _c : null,
      created_by_email: (_e = (_d = response.createdBy) == null ? void 0 : _d.uniqueName) != null ? _e : null,
      created_date: (_f = response.createdDate) != null ? _f : null,
      modified_by: (_h = (_g = response.modifiedBy) == null ? void 0 : _g.displayName) != null ? _h : null,
      modified_date: (_i = response.modifiedDate) != null ? _i : null
    };
  }
});

// ../ap/packages/pieces/community/azure-devops/src/lib/triggers/new-updated-work-item.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var props = {
  project: azureDevOpsCommon.projectDropdown,
  work_item_type: azureDevOpsCommon.workItemTypeDropdownOptional,
  state_filter: azureDevOpsCommon.stateDropdownOptional
}, POLLING_FIRST_RUN_LOOKBACK_DAYS = 14, polling = {
  strategy: import_pieces_common7.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let orgUrl = azureDevOpsCommon.sanitizeOrgUrl(auth.props.organizationUrl), { project, work_item_type, state_filter } = propsValue, projectName = azureDevOpsCommon.narrowString(project, "project"), sinceEpoch = lastFetchEpochMS && lastFetchEpochMS > 0 ? lastFetchEpochMS + 1 : Date.now() - POLLING_FIRST_RUN_LOOKBACK_DAYS * 24 * 60 * 60 * 1e3, sinceDate = new Date(sinceEpoch).toISOString().slice(0, 10), wiqlQuery = `SELECT [System.Id], [System.ChangedDate] FROM WorkItems WHERE [System.TeamProject] = '${azureDevOpsCommon.escapeWiqlString(projectName)}' AND [System.ChangedDate] >= '${sinceDate}'`;
    if (work_item_type) {
      let escapedType = azureDevOpsCommon.escapeWiqlString(
        azureDevOpsCommon.narrowString(work_item_type, "work_item_type")
      );
      wiqlQuery += ` AND [System.WorkItemType] = '${escapedType}'`;
    }
    if (state_filter) {
      let escapedState = azureDevOpsCommon.escapeWiqlString(
        azureDevOpsCommon.narrowString(state_filter, "state_filter")
      );
      wiqlQuery += ` AND [System.State] = '${escapedState}'`;
    }
    wiqlQuery += " ORDER BY [System.ChangedDate] DESC";
    let wiqlResponse = await azureDevOpsCommon.apiCall({
      organizationUrl: orgUrl,
      pat: auth.props.pat,
      method: import_pieces_common7.HttpMethod.POST,
      endpoint: `/${encodeURIComponent(projectName)}/_apis/wit/wiql`,
      queryParams: {
        $top: "200",
        "api-version": "7.1"
      },
      body: { query: wiqlQuery }
    });
    if (!wiqlResponse.workItems || wiqlResponse.workItems.length === 0)
      return [];
    let ids = wiqlResponse.workItems.map((wi) => wi.id);
    return (await azureDevOpsCommon.fetchWorkItemsByIds({
      organizationUrl: orgUrl,
      pat: auth.props.pat,
      ids
    })).map((item) => ({
      epochMilliSeconds: item.changed_date ? new Date(item.changed_date).getTime() : Date.now(),
      data: item
    }));
  }
}, sampleWorkItem = {
  id: 123,
  rev: 1,
  url: "https://dev.azure.com/myorg/myproject/_apis/wit/workItems/123",
  title: "Fix login page bug",
  work_item_type: "Bug",
  state: "Active",
  reason: "New",
  assigned_to: "John Doe",
  assigned_to_email: "john.doe@example.com",
  created_date: "2024-03-15T10:30:00Z",
  created_by: "Jane Smith",
  changed_date: "2024-03-15T14:45:00Z",
  changed_by: "John Doe",
  area_path: "MyProject\\Backend",
  iteration_path: "MyProject\\Sprint 5",
  priority: 2,
  description: "<p>Users cannot log in after password reset.</p>",
  project: "MyProject"
}, newUpdatedWorkItemTrigger = (0, import_pieces_framework7.createTrigger)({
  auth: azureDevOpsAuth,
  name: "new_updated_work_item",
  classification: "READ",
  displayName: "New or Updated Work Item (Polling)",
  description: "Polls Azure DevOps every few minutes and fires when a work item is created or updated. Use this when your Activepieces instance is not reachable from the public internet; otherwise prefer the Instant trigger. Pick a work item type to unlock the state filter.",
  aiMetadata: {
    description: "Fires for each Azure DevOps work item created or changed since the last poll in the chosen project, discovered by periodic polling rather than instantly. Optionally narrow to a single work item type and, once a type is set, to a specific state. Prefer the Instant trigger when the Activepieces instance is publicly reachable."
  },
  props,
  sampleData: sampleWorkItem,
  type: import_pieces_framework7.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common7.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common7.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common7.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common7.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/azure-devops/src/lib/triggers/new-updated-work-item-webhook.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var TRIGGER_STORE_KEY = "azure_devops_workitem_subscription_ids", TRIGGER_TOKEN_STORE_KEY = "azure_devops_workitem_webhook_token", BASIC_AUTH_USERNAME = "activepieces", sampleWebhookOutput = {
  id: 123,
  rev: 2,
  url: "https://dev.azure.com/myorg/_apis/wit/workItems/123",
  title: "Fix login page bug",
  work_item_type: "Bug",
  state: "Active",
  reason: "New",
  assigned_to: "John Doe",
  assigned_to_email: "john.doe@example.com",
  created_date: "2024-03-15T10:30:00Z",
  created_by: "Jane Smith",
  changed_date: "2024-03-15T14:45:00Z",
  changed_by: "John Doe",
  area_path: "MyProject\\Backend",
  iteration_path: "MyProject\\Sprint 5",
  priority: 2,
  description: "<p>Users cannot log in after password reset.</p>",
  project: "MyProject"
}, newUpdatedWorkItemWebhookTrigger = (0, import_pieces_framework8.createTrigger)({
  auth: azureDevOpsAuth,
  name: "new_updated_work_item_webhook",
  classification: "READ",
  displayName: "New or Updated Work Item (Instant)",
  description: "Fires instantly when a work item is created, updated, or commented on in Azure DevOps via a Service Hooks subscription. Requires a public webhook URL reachable from Azure DevOps.",
  aiMetadata: {
    description: "Fires in real time via an Azure DevOps Service Hooks subscription when a work item in the chosen project is created, updated, or commented on, depending on which of those event types are selected. Optionally scope to a single work item type. Requires a publicly reachable webhook URL; use the Polling trigger instead when the instance is not internet-reachable."
  },
  props: {
    project: azureDevOpsCommon.projectDropdown,
    work_item_type: azureDevOpsCommon.workItemTypeDropdownOptional,
    events: import_pieces_framework8.Property.StaticMultiSelectDropdown({
      displayName: "Events",
      description: "Which work item lifecycle events should trigger this flow. Select at least one.",
      required: !0,
      defaultValue: ["workitem.created", "workitem.updated"],
      options: {
        options: [...WEBHOOK_EVENT_OPTIONS]
      }
    })
  },
  sampleData: sampleWebhookOutput,
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let projectName = azureDevOpsCommon.narrowString(
      context.propsValue.project,
      "project"
    ), workItemType = typeof context.propsValue.work_item_type == "string" && context.propsValue.work_item_type.length > 0 ? context.propsValue.work_item_type : void 0, events = filterToHookEvents((_a = context.propsValue.events) != null ? _a : []), projectId = await azureDevOpsCommon.fetchProjectId(
      context.auth,
      projectName
    );
    if (!projectId)
      throw new Error(`Project "${projectName}" not found in Azure DevOps.`);
    let token = azureDevOpsCommon.generateWebhookToken();
    await context.store.put(TRIGGER_TOKEN_STORE_KEY, token);
    let subscriptionIds = [];
    for (let event of events) {
      let subscription = await azureDevOpsCommon.createSubscription({
        auth: context.auth,
        projectId,
        eventType: event,
        webhookUrl: context.webhookUrl,
        workItemType,
        basicAuthUsername: BASIC_AUTH_USERNAME,
        basicAuthPassword: token
      });
      subscriptionIds.push(subscription.id), await context.store.put(TRIGGER_STORE_KEY, subscriptionIds);
    }
  },
  async onDisable(context) {
    var _a;
    let subscriptionIds = (_a = await context.store.get(TRIGGER_STORE_KEY)) != null ? _a : [];
    for (let subscriptionId of subscriptionIds)
      try {
        await azureDevOpsCommon.deleteSubscription({
          auth: context.auth,
          subscriptionId
        });
      } catch (e) {
      }
    await context.store.delete(TRIGGER_STORE_KEY), await context.store.delete(TRIGGER_TOKEN_STORE_KEY);
  },
  async run(context) {
    let expectedToken = await context.store.get(TRIGGER_TOKEN_STORE_KEY);
    if (!expectedToken) return [];
    let authHeader = extractAuthHeader(context.payload.headers);
    if (!authHeader || !verifyBasicAuth(authHeader, BASIC_AUTH_USERNAME, expectedToken))
      return [];
    let payload = azureDevOpsCommon.parseWebhookPayload(context.payload.body);
    return payload ? payload.eventType === "workitem.commented" ? [flattenCommentEvent(payload)] : [flattenResource(payload.resource)] : [];
  },
  async test() {
    return [sampleWebhookOutput];
  }
});
function flattenResource(resource) {
  var _a, _b, _c, _d, _e, _f;
  return azureDevOpsCommon.flattenWorkItem({
    id: (_b = (_a = resource.id) != null ? _a : resource.workItemId) != null ? _b : 0,
    rev: (_c = resource.rev) != null ? _c : 0,
    url: resource.url,
    fields: (_f = (_e = (_d = resource.revision) == null ? void 0 : _d.fields) != null ? _e : resource.fields) != null ? _f : {}
  });
}
function flattenCommentEvent(payload) {
  var _a, _b, _c;
  let { resource } = payload;
  return __spreadProps(__spreadValues({}, flattenResource(resource)), {
    comment: (_c = (_b = (_a = resource.revision) == null ? void 0 : _a.comment) == null ? void 0 : _b.content) != null ? _c : null
  });
}
function extractAuthHeader(headers) {
  var _a;
  let raw = (_a = headers.authorization) != null ? _a : headers.Authorization;
  return typeof raw == "string" ? raw : null;
}
function verifyBasicAuth(header, expectedUser, expectedToken) {
  let prefix = "Basic ";
  if (!header.startsWith(prefix)) return !1;
  let decoded;
  try {
    decoded = Buffer.from(header.slice(prefix.length), "base64").toString("utf-8");
  } catch (e) {
    return !1;
  }
  let sep = decoded.indexOf(":");
  if (sep < 0) return !1;
  let username = decoded.slice(0, sep), password = decoded.slice(sep + 1);
  return azureDevOpsCommon.timingSafeEqual(username, expectedUser) && azureDevOpsCommon.timingSafeEqual(password, expectedToken);
}
function filterToHookEvents(values) {
  let allowed = WEBHOOK_EVENT_OPTIONS.map((o) => o.value);
  return values.filter(
    (v) => typeof v == "string" && allowed.includes(v)
  );
}

// ../ap/packages/pieces/community/azure-devops/src/index.ts
var azureDevOps = (0, import_pieces_framework9.createPiece)({
  displayName: "Azure DevOps",
  description: "Track work, code, and ship software with Azure Boards, Repos, and Pipelines.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/azure-devops.png",
  categories: [import_pieces_framework10.PieceCategory.DEVELOPER_TOOLS],
  auth: azureDevOpsAuth,
  authors: ["majewskibartosz"],
  actions: [
    createWorkItemAction,
    getWorkItemAction,
    updateWorkItemAction,
    listWorkItemsAction,
    addCommentAction,
    (0, import_pieces_common8.createCustomApiCallAction)({
      baseUrl: (auth) => azureDevOpsCommon.sanitizeOrgUrl(
        azureDevOpsCommon.asAuth(auth).props.organizationUrl
      ),
      auth: azureDevOpsAuth,
      authMapping: async (auth) => ({
        Authorization: `Basic ${Buffer.from(
          `:${azureDevOpsCommon.asAuth(auth).props.pat}`
        ).toString("base64")}`
      })
    })
  ],
  triggers: [newUpdatedWorkItemWebhookTrigger, newUpdatedWorkItemTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  azureDevOps
});
