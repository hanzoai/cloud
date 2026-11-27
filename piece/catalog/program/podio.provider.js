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

// ../ap/packages/pieces/community/podio/src/index.ts
var index_exports = {};
__export(index_exports, {
  podio: () => podio
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/podio/src/lib/actions/create-item.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/podio/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/podio/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), PODIO_API_URL = "https://api.podio.com";
async function podioApiCall(options) {
  var _a, _b, _c;
  let { method, accessToken, resourceUri, body, queryParams } = options;
  validateRequiredFields({ accessToken, resourceUri }, ["accessToken", "resourceUri"]);
  let url = `${PODIO_API_URL}${resourceUri}`, headers = {
    Authorization: `Bearer ${accessToken}`,
    "Content-Type": "application/json",
    Accept: "application/json"
  };
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url,
      headers,
      body: body ? JSON.stringify(body) : void 0,
      queryParams
    })).body;
  } catch (error) {
    let statusCode = ((_a = error.response) == null ? void 0 : _a.status) || error.status, errorBody = ((_b = error.response) == null ? void 0 : _b.body) || error.body;
    switch (statusCode) {
      case 400:
        throw new Error(`Bad Request: ${getErrorMessage(errorBody) || "Invalid request parameters. Please check your input values."}`);
      case 401:
        throw new Error("Authentication failed. Your Podio access token is invalid or expired. Please reconnect your Podio account.");
      case 403:
        throw new Error(`Access denied: ${getErrorMessage(errorBody) || "You do not have permission to perform this action. Check your user permissions in Podio."}`);
      case 404:
        throw new Error(`Not found: ${getErrorMessage(errorBody) || "The requested resource was not found. Please verify the ID is correct."}`);
      case 409:
        throw new Error(`Conflict: ${getErrorMessage(errorBody) || "The item has been modified by someone else. Please refresh and try again."}`);
      case 420: {
        let rateLimitMsg = getRateLimitMessage((_c = error.response) == null ? void 0 : _c.headers);
        throw new Error(`Rate limit exceeded: ${rateLimitMsg}`);
      }
      case 422:
        throw new Error(`Validation error: ${getErrorMessage(errorBody) || "The provided data is invalid. Please check required fields and formats."}`);
      case 500:
        throw new Error("Podio server error. Please try again in a few minutes.");
      case 502:
      case 503:
      case 504:
        throw new Error("Podio is temporarily unavailable. Please try again in a few minutes.");
      default:
        throw new Error(`Request failed (${statusCode}): ${getErrorMessage(errorBody) || error.message || "Unknown error occurred"}`);
    }
  }
}
function getErrorMessage(errorBody) {
  if (!errorBody) return null;
  if (typeof errorBody == "string")
    try {
      errorBody = JSON.parse(errorBody);
    } catch (e) {
      return errorBody;
    }
  return errorBody.error_description || errorBody.error || errorBody.message || (errorBody.errors && Array.isArray(errorBody.errors) ? errorBody.errors.join(", ") : null);
}
function getRateLimitMessage(headers) {
  if (!headers)
    return "You have exceeded the API rate limit. Please wait before making more requests.";
  let resetTime = headers["x-rate-limit-reset"] || headers["X-Rate-Limit-Reset"], limit = headers["x-rate-limit-limit"] || headers["X-Rate-Limit-Limit"];
  if (resetTime) {
    let resetDate = new Date(parseInt(resetTime) * 1e3);
    return `Rate limit exceeded (${limit || "1000"} calls/hour). Limit resets at ${resetDate.toLocaleTimeString()}.`;
  }
  return "Rate limit exceeded. Podio allows 1000 API calls per hour for most operations and 250 calls/hour for rate-limited operations.";
}
function validateRequiredFields(data, requiredFields) {
  let missingFields = requiredFields.filter((field) => {
    let value = data[field];
    return value == null || value === "";
  });
  if (missingFields.length > 0)
    throw new Error(`Missing required fields: ${missingFields.join(", ")}`);
}

// ../ap/packages/pieces/community/podio/src/lib/common/auth.ts
function getAccessToken(auth) {
  return auth.access_token;
}
function isTokenExpired(auth) {
  var _a, _b;
  if (!((_a = auth.data) != null && _a.expires_in) || !((_b = auth.data) != null && _b.claimed_at))
    return !1;
  let secondsSinceEpoch = Math.round(Date.now() / 1e3), expiresIn = auth.data.expires_in, claimedAt = auth.data.claimed_at;
  return secondsSinceEpoch + 900 >= claimedAt + expiresIn;
}
function validateAuthData(auth) {
  return auth ? auth.access_token ? isTokenExpired(auth) ? { valid: !1, error: "Access token has expired" } : { valid: !0 } : { valid: !1, error: "Access token is missing" } : { valid: !1, error: "Authentication data is missing" };
}

// ../ap/packages/pieces/community/podio/src/lib/auth.ts
var podioAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: "Connect your Podio account to automate workspace management and item operations.",
  authUrl: "https://podio.com/oauth/authorize",
  tokenUrl: "https://podio.com/oauth/token",
  required: !0,
  scope: ["global:read", "global:write"],
  validate: async ({ auth }) => {
    var _a, _b;
    try {
      let validation = validateAuthData(auth);
      if (!validation.valid)
        return { valid: !1, error: validation.error || "Authentication validation failed" };
      let response = await podioApiCall({
        method: import_pieces_common2.HttpMethod.GET,
        accessToken: auth.access_token,
        resourceUri: "/user/status"
      });
      return response && typeof response == "object" ? { valid: !0 } : { valid: !1, error: "Failed to validate connection with Podio API" };
    } catch (error) {
      return ((_a = error.response) == null ? void 0 : _a.status) === 401 ? { valid: !1, error: "Invalid or expired access token. Please reconnect your Podio account." } : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? { valid: !1, error: "Access denied. Please check your Podio permissions." } : {
        valid: !1,
        error: `Connection validation failed: ${error.message || "Unknown error occurred"}`
      };
    }
  }
});

// ../ap/packages/pieces/community/podio/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var appIdProperty = import_pieces_framework2.Property.Number({
  displayName: "App ID",
  description: "The ID of the Podio app",
  required: !0
}), dynamicAppProperty = import_pieces_framework2.Property.Dropdown({
  auth: podioAuth,
  displayName: "App",
  description: "Select a Podio app",
  required: !0,
  refreshers: ["spaceId"],
  options: async ({ auth, spaceId }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Podio account first",
        options: []
      };
    try {
      let accessToken = getAccessToken(auth), resourceUri = "/app/";
      spaceId && (resourceUri = `/app/space/${spaceId}/`);
      let apps = await podioApiCall({
        method: import_pieces_common3.HttpMethod.GET,
        accessToken,
        resourceUri
      });
      return !apps || apps.length === 0 ? {
        options: [],
        placeholder: spaceId ? "No apps found in this space" : "No apps found"
      } : {
        options: apps.map((app) => {
          var _a;
          return {
            label: `${((_a = app.config) == null ? void 0 : _a.name) || app.name}${app.space ? ` (${app.space.name})` : ""}`,
            value: app.app_id
          };
        })
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load apps. Check your connection."
      };
    }
  }
}), dynamicSpaceProperty = import_pieces_framework2.Property.Dropdown({
  auth: podioAuth,
  displayName: "Space",
  description: "Select a Podio workspace",
  required: !0,
  refreshers: ["orgId"],
  options: async ({ auth, orgId }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Podio account first",
        options: []
      };
    if (!orgId)
      return {
        disabled: !0,
        placeholder: "Select an organization first",
        options: []
      };
    try {
      let accessToken = getAccessToken(auth), resourceUri = `/org/${orgId}/space/`, spaces = await podioApiCall({
        method: import_pieces_common3.HttpMethod.GET,
        accessToken,
        resourceUri
      });
      return !spaces || spaces.length === 0 ? {
        options: [],
        placeholder: "No spaces found in this organization"
      } : {
        options: spaces.map((space) => ({
          label: `${space.name}${space.org ? ` (${space.org.name})` : ""}`,
          value: space.space_id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load spaces. Check your connection."
      };
    }
  }
}), dynamicOrgProperty = import_pieces_framework2.Property.Dropdown({
  auth: podioAuth,
  displayName: "Organization",
  description: "Select a Podio organization",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Podio account first",
        options: []
      };
    try {
      let accessToken = getAccessToken(auth), orgs = await podioApiCall({
        method: import_pieces_common3.HttpMethod.GET,
        accessToken,
        resourceUri: "/org/"
      });
      return !orgs || orgs.length === 0 ? {
        options: [],
        placeholder: "No organizations found"
      } : {
        options: orgs.map((org) => ({
          label: org.name,
          value: org.org_id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load organizations. Check your connection."
      };
    }
  }
}), dynamicItemProperty = import_pieces_framework2.Property.Dropdown({
  auth: podioAuth,
  displayName: "Item",
  description: "Select a Podio item",
  required: !0,
  refreshers: ["appId"],
  options: async ({ auth, appId }) => {
    if (!auth || !appId)
      return {
        disabled: !0,
        placeholder: auth ? "Select an app first" : "Connect your Podio account first",
        options: []
      };
    try {
      let accessToken = getAccessToken(auth), response = await podioApiCall({
        method: import_pieces_common3.HttpMethod.POST,
        accessToken,
        resourceUri: `/item/app/${appId}/filter/`,
        body: { limit: 50 }
      });
      return !response.items || response.items.length === 0 ? {
        options: [],
        placeholder: "No items found in this app"
      } : {
        options: response.items.map((item) => ({
          label: `${item.title || `Item ${item.item_id}`}`,
          value: item.item_id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load items. Check your permissions."
      };
    }
  }
}), dynamicRefTypeProperty = import_pieces_framework2.Property.Dropdown({
  auth: podioAuth,
  displayName: "Reference Type",
  description: "The type of object to reference",
  required: !1,
  refreshers: [],
  options: async () => ({
    options: [
      { label: "Item", value: "item" },
      { label: "Task", value: "task" },
      { label: "Status", value: "status" },
      { label: "App", value: "app" },
      { label: "Space", value: "space" }
    ]
  })
}), dynamicRefIdProperty = import_pieces_framework2.Property.Dropdown({
  auth: podioAuth,
  displayName: "Reference Object",
  description: "Select the specific object to reference",
  required: !1,
  refreshers: ["refType", "appId", "spaceId", "orgId"],
  options: async ({ auth, refType, appId, spaceId, orgId }) => {
    var _a;
    if (!auth || !refType)
      return {
        disabled: !0,
        placeholder: auth ? "Select a reference type first" : "Connect your Podio account first",
        options: []
      };
    try {
      let accessToken = getAccessToken(auth), endpoint = "";
      switch (refType) {
        case "item":
          return appId ? {
            options: ((_a = (await podioApiCall({
              method: import_pieces_common3.HttpMethod.POST,
              accessToken,
              resourceUri: `/item/app/${appId}/filter/`,
              body: { limit: 30 }
            })).items) == null ? void 0 : _a.map((item) => ({
              label: item.title || `Item ${item.item_id}`,
              value: item.item_id
            }))) || []
          } : {
            disabled: !0,
            placeholder: "Select an app first to load items",
            options: []
          };
        case "task": {
          spaceId ? endpoint = `/task/space/${spaceId}/` : endpoint = "/task/";
          let taskResponse = await podioApiCall({
            method: import_pieces_common3.HttpMethod.GET,
            accessToken,
            resourceUri: endpoint,
            queryParams: { limit: 30 }
          });
          return {
            options: (taskResponse == null ? void 0 : taskResponse.map((task) => ({
              label: task.text || `Task ${task.task_id}`,
              value: task.task_id
            }))) || []
          };
        }
        case "status":
          if (!spaceId)
            return {
              disabled: !0,
              placeholder: "Select a space first to load status updates",
              options: []
            };
          try {
            let statusResponse = await podioApiCall({
              method: import_pieces_common3.HttpMethod.GET,
              accessToken,
              resourceUri: `/stream/space/${spaceId}/`,
              queryParams: {
                limit: 50,
                type: "status"
              }
            });
            return statusResponse != null && statusResponse.items ? {
              options: statusResponse.items.filter((item) => item.type === "status" && item.data).map((item) => {
                var _a2;
                return {
                  label: (item.data.value || item.data.text || "").substring(0, 60) + (((_a2 = item.data.value) == null ? void 0 : _a2.length) > 60 ? "..." : "") || `Status ${item.data.status_id}`,
                  value: item.data.status_id
                };
              }).slice(0, 30)
            } : {
              options: [],
              placeholder: "No status updates found in this space"
            };
          } catch (error) {
            return {
              options: [],
              placeholder: "Enter status ID manually (could not load status updates)"
            };
          }
        case "app":
          spaceId ? endpoint = `/app/space/${spaceId}/` : endpoint = "/app/";
          try {
            let appResponse = await podioApiCall({
              method: import_pieces_common3.HttpMethod.GET,
              accessToken,
              resourceUri: endpoint
            });
            return {
              options: (appResponse == null ? void 0 : appResponse.map((app) => {
                var _a2;
                return {
                  label: `${((_a2 = app.config) == null ? void 0 : _a2.name) || app.name}${app.space ? ` (${app.space.name})` : ""}`,
                  value: app.app_id
                };
              })) || []
            };
          } catch (error) {
            return {
              options: [],
              placeholder: spaceId ? "No apps found in this space" : "Failed to load apps"
            };
          }
        case "space": {
          if (!orgId)
            return {
              disabled: !0,
              placeholder: "Select an organization first to load spaces",
              options: []
            };
          let spaceResponse = await podioApiCall({
            method: import_pieces_common3.HttpMethod.GET,
            accessToken,
            resourceUri: `/org/${orgId}/space/`
          });
          return {
            options: (spaceResponse == null ? void 0 : spaceResponse.map((space) => ({
              label: space.name,
              value: space.space_id
            }))) || []
          };
        }
        default:
          return {
            options: [],
            placeholder: "Unknown reference type"
          };
      }
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: `Failed to load ${refType}s. Check your permissions.`
      };
    }
  }
}), spaceIdProperty = import_pieces_framework2.Property.Number({
  displayName: "Space ID",
  description: "The ID of the Podio workspace/space",
  required: !0
}), itemIdProperty = import_pieces_framework2.Property.Number({
  displayName: "Item ID",
  description: "The ID of the Podio item",
  required: !0
}), taskIdProperty = import_pieces_framework2.Property.Number({
  displayName: "Task ID",
  description: "The ID of the Podio task",
  required: !0
}), dynamicFileProperty = import_pieces_framework2.Property.Dropdown({
  auth: podioAuth,
  displayName: "File",
  description: "Select a file from the space",
  required: !0,
  refreshers: ["spaceId"],
  options: async ({ auth, spaceId }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Podio account first",
        options: []
      };
    if (!spaceId)
      return {
        disabled: !0,
        placeholder: "Select a space first to load files",
        options: []
      };
    try {
      let accessToken = getAccessToken(auth), files = await podioApiCall({
        method: import_pieces_common3.HttpMethod.GET,
        accessToken,
        resourceUri: `/file/space/${spaceId}/`,
        queryParams: {
          limit: 50,
          sort_by: "created_on",
          sort_desc: "true"
        }
      });
      return !files || files.length === 0 ? {
        options: [],
        placeholder: "No files found in this space"
      } : {
        options: files.map((file) => {
          var _a;
          let label = file.name || `File ${file.file_id}`;
          if (file.size) {
            let sizeInKB = Math.round(file.size / 1024), sizeText = sizeInKB > 1024 ? `${Math.round(sizeInKB / 1024)}MB` : `${sizeInKB}KB`;
            label += ` (${sizeText})`;
          }
          if (file.mimetype) {
            let fileType = file.mimetype.split("/")[0];
            label = `${{
              image: "\u{1F5BC}\uFE0F",
              video: "\u{1F3A5}",
              audio: "\u{1F3B5}",
              text: "\u{1F4C4}",
              application: "\u{1F4C1}"
            }[fileType] || "\u{1F4C4}"} ${label}`;
          }
          return (_a = file.context) != null && _a.title && (label += ` \u2192 ${file.context.title}`), {
            label,
            value: file.file_id
          };
        })
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load files. Check your connection."
      };
    }
  }
}), dynamicTaskProperty = import_pieces_framework2.Property.Dropdown({
  auth: podioAuth,
  displayName: "Task",
  description: "Select a Podio task",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Podio account first",
        options: []
      };
    try {
      let accessToken = getAccessToken(auth), userInfo = await podioApiCall({
        method: import_pieces_common3.HttpMethod.GET,
        accessToken,
        resourceUri: "/user/status"
      }), userId = (_a = userInfo == null ? void 0 : userInfo.user) == null ? void 0 : _a.user_id;
      if (!userId)
        return {
          disabled: !0,
          options: [],
          placeholder: "Could not determine user ID for task filtering"
        };
      let tasks = await podioApiCall({
        method: import_pieces_common3.HttpMethod.GET,
        accessToken,
        resourceUri: "/task/",
        queryParams: {
          responsible: userId,
          limit: 100,
          view: "full",
          sort_by: "created_on",
          sort_desc: "true"
        }
      });
      return !tasks || tasks.length === 0 ? {
        options: [],
        placeholder: "No tasks found assigned to you"
      } : {
        options: tasks.map((task) => {
          var _a2;
          let label = task.text || `Task ${task.task_id}`;
          return task.status === "completed" ? label = `\u2713 ${label}` : task.due_date && (new Date(task.due_date) < /* @__PURE__ */ new Date() ? label = `\u26A0\uFE0F ${label} (Overdue)` : label = `\u{1F4C5} ${label}`), (_a2 = task.ref) != null && _a2.title && (label += ` \u2192 ${task.ref.title}`), {
            label,
            value: task.task_id
          };
        })
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load tasks. Check your connection."
      };
    }
  }
}), orgIdProperty = import_pieces_framework2.Property.Number({
  displayName: "Organization ID",
  description: "The ID of the Podio organization",
  required: !0
}), refTypeProperty = import_pieces_framework2.Property.Dropdown({
  auth: podioAuth,
  displayName: "Reference Type",
  description: "The type of object",
  required: !0,
  refreshers: [],
  options: async () => ({
    options: [
      { label: "Item", value: "item" },
      { label: "Task", value: "task" },
      { label: "Status", value: "status" },
      { label: "App", value: "app" },
      { label: "Space", value: "space" }
    ]
  })
}), statusProperty = import_pieces_framework2.Property.Dropdown({
  auth: podioAuth,
  displayName: "Status",
  description: "The status filter",
  required: !1,
  refreshers: [],
  options: async () => ({
    options: [
      { label: "Active", value: "active" },
      { label: "Completed", value: "completed" },
      { label: "All", value: "all" }
    ]
  })
}), silentProperty = import_pieces_framework2.Property.Checkbox({
  displayName: "Silent Mode",
  description: "If true, notifications will not be triggered and the object will not appear in activity streams",
  required: !1,
  defaultValue: !1
}), hookProperty = import_pieces_framework2.Property.Checkbox({
  displayName: "Execute Hooks",
  description: "If false, webhooks and integrations will not be triggered for this operation",
  required: !1,
  defaultValue: !0
}), limitProperty = import_pieces_framework2.Property.Number({
  displayName: "Limit",
  description: "Maximum number of results to return (1-500, default: 20)",
  required: !1,
  defaultValue: 20
}), offsetProperty = import_pieces_framework2.Property.Number({
  displayName: "Offset",
  description: "Number of results to skip for pagination (default: 0)",
  required: !1,
  defaultValue: 0
});
function formatFieldValues(appFields, formData) {
  let fields = {};
  for (let field of appFields) {
    let fieldKey = `field_${field.field_id}`, fieldValue = formData[fieldKey];
    if (fieldValue != null && fieldValue !== "") {
      let fieldType = field.type;
      switch (fieldType) {
        case "text":
          fields[field.field_id] = {
            value: fieldValue.toString()
          };
          break;
        case "number":
          fields[field.field_id] = {
            value: fieldValue.toString()
          };
          break;
        case "money":
          typeof fieldValue == "object" && fieldValue.value && (fields[field.field_id] = {
            value: fieldValue.value.toString(),
            currency: fieldValue.currency || "USD"
          });
          break;
        case "date":
          if (typeof fieldValue == "object") {
            let dateObj = {};
            fieldValue.start_date && (dateObj.start_date = fieldValue.start_date), fieldValue.start_time && (dateObj.start_time = fieldValue.start_time), fieldValue.end_date && (dateObj.end_date = fieldValue.end_date), fieldValue.end_time && (dateObj.end_time = fieldValue.end_time), Object.keys(dateObj).length > 0 && (fields[field.field_id] = dateObj);
          }
          break;
        case "contact":
        case "member":
        case "app":
        case "category":
        case "status":
        case "image":
        case "file":
        case "duration":
        case "video":
          fields[field.field_id] = {
            value: Number(fieldValue)
          };
          break;
        case "progress": {
          let progressValue = Number(fieldValue);
          progressValue >= 0 && progressValue <= 100 && (fields[field.field_id] = {
            value: progressValue
          });
          break;
        }
        case "email":
        case "phone":
          typeof fieldValue == "object" && fieldValue.value && (fields[field.field_id] = {
            value: fieldValue.value,
            type: fieldValue.type || (fieldType === "email" ? "work" : "mobile")
          });
          break;
        case "location":
          fields[field.field_id] = {
            value: fieldValue.toString()
          };
          break;
        case "embed":
          typeof fieldValue == "object" && fieldValue.embed && (fields[field.field_id] = {
            embed: fieldValue.embed,
            file: fieldValue.file || null
          });
          break;
        default:
          fields[field.field_id] = {
            value: fieldValue.toString()
          };
      }
    }
  }
  return fields;
}

// ../ap/packages/pieces/community/podio/src/lib/actions/create-item.ts
var createItemAction = (0, import_pieces_framework3.createAction)({
  auth: podioAuth,
  name: "create_item",
  displayName: "Create Item",
  description: "Create a new record in a Podio app with specified field values.",
  audience: "both",
  aiMetadata: { description: "Creates a new item (record) in a specified Podio app, populating its fields. Field values can be supplied via the structured app-field inputs or, for complex cases, a raw legacy-fields JSON map keyed by field id. Use when adding a record to a Podio app; requires an app id and at least one field value. Not idempotent \u2014 each call creates a separate item.", idempotent: !1 },
  props: {
    orgId: import_pieces_framework3.Property.Dropdown({
      auth: podioAuth,
      displayName: "Organization (Optional)",
      description: "Select an organization to filter apps by workspace. Leave empty to see all apps.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your Podio account first",
            options: []
          };
        try {
          let accessToken = getAccessToken(auth), orgs = await podioApiCall({
            method: import_pieces_common4.HttpMethod.GET,
            accessToken,
            resourceUri: "/org/"
          });
          return !orgs || orgs.length === 0 ? {
            options: [],
            placeholder: "No organizations found"
          } : {
            options: orgs.map((org) => ({
              label: org.name,
              value: org.org_id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load organizations. Check your connection."
          };
        }
      }
    }),
    spaceId: import_pieces_framework3.Property.Dropdown({
      auth: podioAuth,
      displayName: "Space (Optional)",
      description: "Select a workspace to filter apps. Leave empty to see all apps in the organization.",
      required: !1,
      refreshers: ["orgId"],
      options: async ({ auth, orgId }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Connect your Podio account first",
            options: []
          };
        if (!orgId)
          return {
            disabled: !0,
            placeholder: "Select an organization first",
            options: []
          };
        try {
          let accessToken = getAccessToken(auth), resourceUri = `/org/${orgId}/space/`, spaces = await podioApiCall({
            method: import_pieces_common4.HttpMethod.GET,
            accessToken,
            resourceUri
          });
          return !spaces || spaces.length === 0 ? {
            options: [],
            placeholder: "No spaces found in this organization"
          } : {
            options: spaces.map((space) => ({
              label: `${space.name}${space.org ? ` (${space.org.name})` : ""}`,
              value: space.space_id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load spaces. Check your connection."
          };
        }
      }
    }),
    appId: dynamicAppProperty,
    externalId: import_pieces_framework3.Property.ShortText({
      displayName: "External ID",
      description: "The external id of the item. This can be used to hold a reference to the item in an external system.",
      required: !1
    }),
    appFields: import_pieces_framework3.Property.DynamicProperties({
      auth: podioAuth,
      displayName: "App Fields",
      description: "Configure values for the fields in the selected app",
      required: !0,
      refreshers: ["appId"],
      props: async ({ auth, appId }) => {
        var _a, _b, _c;
        if (!auth || !appId)
          return {};
        try {
          let accessToken = getAccessToken(auth), fields = (await podioApiCall({
            method: import_pieces_common4.HttpMethod.GET,
            accessToken,
            resourceUri: `/app/${appId}`,
            queryParams: { view: "full" }
          })).fields || [];
          if (!fields || fields.length === 0)
            return {};
          let fieldProperties = {};
          for (let field of fields)
            try {
              let fieldKey = `field_${field.field_id}`, fieldConfig = field.config, fieldType = field.type, isRequired = fieldConfig.required || !1, baseProps = {
                displayName: fieldConfig.label,
                description: fieldConfig.description || `Enter value for ${fieldConfig.label}`,
                required: isRequired
              };
              switch (fieldType) {
                case "text":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.LongText(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Supports plain text, markdown, or HTML.`
                  }));
                  break;
                case "number":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter a numeric value.`
                  }));
                  break;
                case "money":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.Object(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Format: {"value": "amount", "currency": "USD"}`
                  }));
                  break;
                case "date":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.Object(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Format: {"start_date": "YYYY-MM-DD", "start_time": "HH:MM:SS", "end_date": "YYYY-MM-DD", "end_time": "HH:MM:SS"}`
                  }));
                  break;
                case "contact":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter the Podio profile ID of the contact.`
                  }));
                  break;
                case "member":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter the Podio user ID of the member.`
                  }));
                  break;
                case "app":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter the ID of the item from the linked app.`
                  }));
                  break;
                case "category":
                case "status": {
                  let staticOptions = [];
                  (_a = fieldConfig.settings) != null && _a.options && Array.isArray(fieldConfig.settings.options) && (staticOptions = fieldConfig.settings.options.map((option) => ({
                    label: option.text || option.name || option.value || `Option ${option.id}`,
                    value: option.id || option.value
                  }))), staticOptions.length > 0 ? fieldProperties[fieldKey] = import_pieces_framework3.Property.StaticDropdown(__spreadProps(__spreadValues({}, baseProps), {
                    options: {
                      options: staticOptions
                    }
                  })) : fieldProperties[fieldKey] = import_pieces_framework3.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter the ${fieldType} option ID manually.`
                  }));
                  break;
                }
                case "email":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.Object(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Format: {"value": "email@example.com", "type": "work|home|other"}`
                  }));
                  break;
                case "phone":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.Object(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Format: {"value": "phone_number", "type": "mobile|work|home|main|work_fax|private_fax|other"}`
                  }));
                  break;
                case "location":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.ShortText(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter a location (address, coordinates, etc.)`
                  }));
                  break;
                case "image":
                case "file":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter the file ID of the uploaded ${fieldType}.`
                  }));
                  break;
                case "progress":
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter a value between 0 and 100.`
                  }));
                  break;
                default:
                  fieldProperties[fieldKey] = import_pieces_framework3.Property.ShortText(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Field type: ${fieldType}`
                  }));
              }
            } catch (fieldError) {
              let fieldKey = `field_${field.field_id}`;
              fieldProperties[fieldKey] = import_pieces_framework3.Property.ShortText({
                displayName: `${((_b = field.config) == null ? void 0 : _b.label) || "Unknown Field"} (Fallback)`,
                description: `Field type "${field.type}" - Manual entry required. Original error: ${fieldError instanceof Error ? fieldError.message : "Unknown error"}`,
                required: ((_c = field.config) == null ? void 0 : _c.required) || !1
              });
            }
          return fieldProperties;
        } catch (error) {
          return {};
        }
      }
    }),
    legacyFields: import_pieces_framework3.Property.Object({
      displayName: "Advanced: Custom Fields JSON",
      description: `Advanced: Use this for complex field configurations. Format: {"field_id": {"value": "content"}}. Only use if the dynamic fields above don't meet your needs.`,
      required: !1
    }),
    fileIds: import_pieces_framework3.Property.Array({
      displayName: "File IDs",
      description: "Temporary files that have been uploaded and should be attached to this item",
      required: !1
    }),
    tags: import_pieces_framework3.Property.Array({
      displayName: "Tags",
      description: "The tags to put on the item",
      required: !1
    }),
    reminder: import_pieces_framework3.Property.Object({
      displayName: "Reminder",
      description: 'Optional reminder on this item. Format: {"remind_delta": minutes_before_due_date}',
      required: !1
    }),
    recurrence: import_pieces_framework3.Property.Object({
      displayName: "Recurrence",
      description: 'The recurrence for the task, if any. Format: {"name": "weekly|monthly|yearly", "config": {...}, "step": 1, "until": "date"}',
      required: !1
    }),
    linkedAccountId: import_pieces_framework3.Property.Number({
      displayName: "Linked Account ID",
      description: "The linked account to use for the meeting",
      required: !1
    }),
    ref: import_pieces_framework3.Property.Object({
      displayName: "Reference",
      description: 'The reference for the new item, if any. Format: {"type": "item", "id": reference_id}',
      required: !1
    }),
    hook: hookProperty,
    silent: silentProperty
  },
  async run(context) {
    let accessToken = getAccessToken(context.auth), {
      orgId,
      spaceId,
      appId,
      externalId,
      appFields,
      legacyFields,
      fileIds,
      tags,
      reminder,
      recurrence,
      linkedAccountId,
      ref,
      hook,
      silent
    } = context.propsValue;
    if (!appId)
      throw new Error("App selection is required. Please select a Podio app from the dropdown.");
    let formattedFields = {};
    if (appFields && Object.keys(appFields).length > 0)
      try {
        let accessToken2 = getAccessToken(context.auth), fieldDefinitions = (await podioApiCall({
          method: import_pieces_common4.HttpMethod.GET,
          accessToken: accessToken2,
          resourceUri: `/app/${appId}`,
          queryParams: { view: "full" }
        })).fields || [];
        formattedFields = formatFieldValues(fieldDefinitions, appFields);
      } catch (error) {
        throw new Error(`Failed to process app fields: ${error instanceof Error ? error.message : "Unknown error"}`);
      }
    if (legacyFields && typeof legacyFields == "object" && Object.keys(legacyFields).length > 0 && (formattedFields = __spreadValues(__spreadValues({}, formattedFields), legacyFields)), Object.keys(formattedFields).length === 0)
      throw new Error("At least one field value is required to create an item. Please configure the app fields or provide legacy field data.");
    if (fileIds && !Array.isArray(fileIds))
      throw new Error("File IDs must be provided as an array of numbers.");
    if (tags && !Array.isArray(tags))
      throw new Error("Tags must be provided as an array of strings.");
    let body = {
      fields: formattedFields
    };
    externalId && (body.external_id = externalId), fileIds && Array.isArray(fileIds) && fileIds.length > 0 && (body.file_ids = fileIds), tags && Array.isArray(tags) && tags.length > 0 && (body.tags = tags), reminder && typeof reminder == "object" && Object.keys(reminder).length > 0 && reminder.remind_delta && typeof reminder.remind_delta == "number" && (body.reminder = reminder), recurrence && typeof recurrence == "object" && Object.keys(recurrence).length > 0 && recurrence.name && recurrence.config && (body.recurrence = recurrence), linkedAccountId && (body.linked_account_id = linkedAccountId), ref && typeof ref == "object" && Object.keys(ref).length > 0 && ref.type && ref.id && (body.ref = ref);
    let queryParams = {};
    return typeof hook == "boolean" && (queryParams.hook = hook.toString()), typeof silent == "boolean" && (queryParams.silent = silent.toString()), await podioApiCall({
      method: import_pieces_common4.HttpMethod.POST,
      accessToken,
      resourceUri: `/item/app/${appId}/`,
      body,
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/podio/src/lib/actions/update-item.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateItemAction = (0, import_pieces_framework4.createAction)({
  auth: podioAuth,
  name: "update_item",
  displayName: "Update Item",
  description: "Update an existing record in a Podio app with specified field values. Only provided fields will be updated.",
  audience: "both",
  aiMetadata: { description: "Updates fields on an existing Podio item identified by item id; only the fields you supply are changed (via structured app-field inputs or a raw legacy-fields JSON map). Use when modifying an existing record rather than creating one; requires the app id and item id. Idempotent \u2014 repeating with the same values leaves the item in the same state.", idempotent: !0 },
  props: {
    appId: dynamicAppProperty,
    itemId: dynamicItemProperty,
    appFields: import_pieces_framework4.Property.DynamicProperties({
      auth: podioAuth,
      displayName: "App Fields",
      description: "Configure values for the fields you want to update in the selected app",
      required: !1,
      refreshers: ["appId"],
      props: async ({ auth, appId }) => {
        var _a, _b;
        if (!auth || !appId)
          return {};
        try {
          let accessToken = getAccessToken(auth), fields = (await podioApiCall({
            method: import_pieces_common5.HttpMethod.GET,
            accessToken,
            resourceUri: `/app/${appId}`,
            queryParams: { view: "full" }
          })).fields || [];
          if (!fields || fields.length === 0)
            return {};
          let fieldProperties = {};
          for (let field of fields)
            try {
              let fieldKey = `field_${field.field_id}`, fieldConfig = field.config, fieldType = field.type, baseProps = {
                displayName: fieldConfig.label,
                description: fieldConfig.description || `Update value for ${fieldConfig.label}`,
                required: !1
              };
              switch (fieldType) {
                case "text":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.LongText(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Supports plain text, markdown, or HTML.`
                  }));
                  break;
                case "number":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter a numeric value.`
                  }));
                  break;
                case "money":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.Object(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Format: {"value": "amount", "currency": "USD"}`
                  }));
                  break;
                case "date":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.Object(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Format: {"start_date": "YYYY-MM-DD", "start_time": "HH:MM:SS", "end_date": "YYYY-MM-DD", "end_time": "HH:MM:SS"}`
                  }));
                  break;
                case "contact":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter the Podio profile ID of the contact.`
                  }));
                  break;
                case "member":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter the Podio user ID of the member.`
                  }));
                  break;
                case "app":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter the ID of the item from the linked app.`
                  }));
                  break;
                case "category":
                case "status": {
                  let staticOptions = [];
                  (_a = fieldConfig.settings) != null && _a.options && Array.isArray(fieldConfig.settings.options) && (staticOptions = fieldConfig.settings.options.map((option) => ({
                    label: option.text || option.name || option.value || `Option ${option.id}`,
                    value: option.id || option.value
                  }))), staticOptions.length > 0 ? fieldProperties[fieldKey] = import_pieces_framework4.Property.StaticDropdown(__spreadProps(__spreadValues({}, baseProps), {
                    options: {
                      options: staticOptions
                    }
                  })) : fieldProperties[fieldKey] = import_pieces_framework4.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter the ${fieldType} option ID manually.`
                  }));
                  break;
                }
                case "email":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.Object(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Format: {"value": "email@example.com", "type": "work|home|other"}`
                  }));
                  break;
                case "phone":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.Object(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Format: {"value": "phone_number", "type": "mobile|work|home|main|work_fax|private_fax|other"}`
                  }));
                  break;
                case "location":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.ShortText(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter a location (address, coordinates, etc.)`
                  }));
                  break;
                case "image":
                case "file":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter the file ID of the uploaded ${fieldType}.`
                  }));
                  break;
                case "progress":
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.Number(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Enter a value between 0 and 100.`
                  }));
                  break;
                default:
                  fieldProperties[fieldKey] = import_pieces_framework4.Property.ShortText(__spreadProps(__spreadValues({}, baseProps), {
                    description: `${baseProps.description}. Field type: ${fieldType}`
                  }));
              }
            } catch (fieldError) {
              let fieldKey = `field_${field.field_id}`;
              fieldProperties[fieldKey] = import_pieces_framework4.Property.ShortText({
                displayName: `${((_b = field.config) == null ? void 0 : _b.label) || "Unknown Field"} (Fallback)`,
                description: `Field type "${field.type}" - Manual entry required. Original error: ${fieldError instanceof Error ? fieldError.message : "Unknown error"}`,
                required: !1
              });
            }
          return fieldProperties;
        } catch (error) {
          return {};
        }
      }
    }),
    revision: import_pieces_framework4.Property.Number({
      displayName: "Revision",
      description: "The revision of the item that is being updated. Optional for conflict detection.",
      required: !1
    }),
    externalId: import_pieces_framework4.Property.ShortText({
      displayName: "External ID",
      description: "The new external_id of the item",
      required: !1
    }),
    legacyFields: import_pieces_framework4.Property.Object({
      displayName: "Legacy Fields (Advanced)",
      description: "Manual field configuration using field_id or external_id as keys. Use this for advanced scenarios or when dynamic fields are not sufficient.",
      required: !1
    }),
    fileIds: import_pieces_framework4.Property.Array({
      displayName: "File IDs",
      description: "The list of attachments",
      required: !1
    }),
    tags: import_pieces_framework4.Property.Array({
      displayName: "Tags",
      description: "The list of tags",
      required: !1
    }),
    reminder: import_pieces_framework4.Property.Object({
      displayName: "Reminder",
      description: 'Optional reminder on this item. Format: {"remind_delta": minutes_before_due_date}',
      required: !1
    }),
    recurrence: import_pieces_framework4.Property.Object({
      displayName: "Recurrence",
      description: 'The recurrence for the task, if any. Format: {"name": "weekly|monthly|yearly", "config": {...}, "step": 1, "until": "date"}',
      required: !1
    }),
    linkedAccountId: import_pieces_framework4.Property.Number({
      displayName: "Linked Account ID",
      description: "The linked account to use for meetings",
      required: !1
    }),
    ref: import_pieces_framework4.Property.Object({
      displayName: "Reference",
      description: 'The reference of the item. Format: {"type": "reference_type", "id": reference_id}',
      required: !1
    }),
    hook: hookProperty,
    silent: silentProperty
  },
  async run(context) {
    let accessToken = getAccessToken(context.auth), {
      appId,
      itemId,
      appFields,
      revision,
      externalId,
      legacyFields,
      fileIds,
      tags,
      reminder,
      recurrence,
      linkedAccountId,
      ref,
      hook,
      silent
    } = context.propsValue;
    if (!appId)
      throw new Error("App selection is required to update an item. Please select an app first.");
    if (!itemId)
      throw new Error("Item selection is required to update an item. Please select an item from the dropdown.");
    let formattedFields = {};
    if (appFields && Object.keys(appFields).length > 0)
      try {
        let fieldDefinitions = (await podioApiCall({
          method: import_pieces_common5.HttpMethod.GET,
          accessToken,
          resourceUri: `/app/${appId}`,
          queryParams: { view: "full" }
        })).fields || [];
        formattedFields = formatFieldValues(fieldDefinitions, appFields);
      } catch (error) {
        throw new Error(`Failed to process app fields: ${error instanceof Error ? error.message : "Unknown error"}`);
      }
    if (legacyFields && typeof legacyFields == "object" && Object.keys(legacyFields).length > 0 && (formattedFields = __spreadValues(__spreadValues({}, formattedFields), legacyFields)), fileIds && !Array.isArray(fileIds))
      throw new Error("File IDs must be provided as an array of numbers.");
    if (tags && !Array.isArray(tags))
      throw new Error("Tags must be provided as an array of strings.");
    if (reminder && reminder.remind_delta !== void 0 && typeof reminder.remind_delta != "number")
      throw new Error("Reminder remind_delta must be a number representing minutes before due date.");
    if (linkedAccountId && typeof linkedAccountId != "number")
      throw new Error("Linked Account ID must be a number.");
    if (ref && (!ref.type || !ref.id))
      throw new Error('Reference must include both "type" and "id" properties.');
    let body = {};
    if (revision !== void 0) {
      if (typeof revision != "number")
        throw new Error("Revision must be a number.");
      body.revision = revision;
    }
    externalId && externalId.trim() && (body.external_id = externalId.trim()), Object.keys(formattedFields).length > 0 && (body.fields = formattedFields), fileIds && Array.isArray(fileIds) && fileIds.length > 0 && (body.file_ids = fileIds), tags && Array.isArray(tags) && tags.length > 0 && (body.tags = tags), reminder && typeof reminder == "object" && Object.keys(reminder).length > 0 && reminder.remind_delta && typeof reminder.remind_delta == "number" && (body.reminder = reminder), recurrence && typeof recurrence == "object" && Object.keys(recurrence).length > 0 && recurrence.name && recurrence.config && (body.recurrence = recurrence), linkedAccountId && (body.linked_account_id = linkedAccountId), ref && typeof ref == "object" && Object.keys(ref).length > 0 && ref.type && ref.id && (body.ref = ref);
    let queryParams = {};
    return typeof hook == "boolean" && (queryParams.hook = hook.toString()), typeof silent == "boolean" && (queryParams.silent = silent.toString()), await podioApiCall({
      method: import_pieces_common5.HttpMethod.PUT,
      accessToken,
      resourceUri: `/item/${itemId}`,
      body,
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/podio/src/lib/actions/create-task.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createTaskAction = (0, import_pieces_framework5.createAction)({
  auth: podioAuth,
  name: "create_task",
  displayName: "Create Task",
  description: "Add a new task to an item or workspace with due dates, assignees, and attachments.",
  audience: "both",
  aiMetadata: { description: "Creates a Podio task, optionally attached to a reference object (item, status, or another task via the reference type/id) or standalone if no reference is given. Supports due date/time, a responsible assignee, file attachments, labels, reminders, and recurrence. Use to add a to-do; requires task text. Not idempotent \u2014 each call creates a new task.", idempotent: !1 },
  props: {
    text: import_pieces_framework5.Property.LongText({
      displayName: "Task Text",
      description: "The description or text of the task",
      required: !0
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "Additional description for the task",
      required: !1
    }),
    private: import_pieces_framework5.Property.Checkbox({
      displayName: "Private",
      description: "Whether the task should be private (only creator, assignee and assignor can see it)",
      required: !1,
      defaultValue: !1
    }),
    dueDate: import_pieces_framework5.Property.DateTime({
      displayName: "Due Date",
      description: "Due date in local time (date only)",
      required: !1
    }),
    dueTime: import_pieces_framework5.Property.ShortText({
      displayName: "Due Time",
      description: 'Due time in local time (HH:MM:SS format, e.g., "14:30:00")',
      required: !1
    }),
    dueOn: import_pieces_framework5.Property.DateTime({
      displayName: "Due Date & Time (UTC)",
      description: "Complete due date and time in UTC. Alternative to separate date/time fields.",
      required: !1
    }),
    orgId: dynamicOrgProperty,
    spaceId: dynamicSpaceProperty,
    appId: dynamicAppProperty,
    refType: dynamicRefTypeProperty,
    refId: dynamicRefIdProperty,
    responsible: import_pieces_framework5.Property.Object({
      displayName: "Responsible Person",
      description: "Who is responsible for this task. Can be a user ID (number) or contact identifier object with type and id.",
      required: !1
    }),
    fileIds: import_pieces_framework5.Property.Array({
      displayName: "File IDs",
      description: "List of file IDs to attach to this task",
      required: !1
    }),
    labels: import_pieces_framework5.Property.Array({
      displayName: "Labels (Text)",
      description: "List of label names in text form",
      required: !1
    }),
    labelIds: import_pieces_framework5.Property.Array({
      displayName: "Label IDs",
      description: "List of label IDs (alternative to text labels)",
      required: !1
    }),
    reminder: import_pieces_framework5.Property.Object({
      displayName: "Reminder",
      description: 'Reminder settings. Format: {"remind_delta": minutes_before_due_date}',
      required: !1
    }),
    recurrence: import_pieces_framework5.Property.Object({
      displayName: "Recurrence",
      description: 'Recurring task settings. Format: {"name": "weekly|monthly|yearly", "config": {...}, "step": 1, "until": "date"}',
      required: !1
    }),
    externalId: import_pieces_framework5.Property.ShortText({
      displayName: "External ID",
      description: "External system identifier for this task",
      required: !1
    }),
    hook: hookProperty,
    silent: silentProperty
  },
  async run(context) {
    console.log("\u{1F50D} [Podio Task] Action run started with propsValue:", JSON.stringify(context.propsValue, null, 2));
    let accessToken = getAccessToken(context.auth), {
      text,
      description,
      private: isPrivate,
      dueDate,
      dueTime,
      dueOn,
      orgId,
      spaceId,
      appId,
      refType,
      refId,
      responsible,
      fileIds,
      labels,
      labelIds,
      reminder,
      recurrence,
      externalId,
      hook,
      silent
    } = context.propsValue;
    if (!text || typeof text != "string" || text.trim().length === 0)
      throw new Error("Task text is required and cannot be empty.");
    if (refType && !refId)
      throw new Error("Reference Object is required when Reference Type is specified. Please select an object from the dropdown.");
    if (!refType && refId)
      throw new Error("Reference Type is required when Reference Object is specified. Please select a reference type first.");
    if (refType === "item" && !appId)
      throw new Error("App selection is required when referencing items. Please select an app first.");
    if ((refType === "status" || refType === "task") && !spaceId)
      throw new Error("Space selection is required when referencing status updates or tasks. Please select a space first.");
    if (fileIds && !Array.isArray(fileIds))
      throw new Error("File IDs must be provided as an array of numbers.");
    if (labels && !Array.isArray(labels))
      throw new Error("Labels must be provided as an array of strings.");
    if (labelIds && !Array.isArray(labelIds))
      throw new Error("Label IDs must be provided as an array of numbers.");
    if (reminder && typeof reminder != "object")
      throw new Error("Reminder must be an object with remind_delta property.");
    if (recurrence && typeof recurrence != "object")
      throw new Error("Recurrence must be an object with name and config properties.");
    let body = {
      text: text.trim()
    };
    description && description.trim() && (body.description = description.trim()), typeof isPrivate == "boolean" && (body.private = isPrivate), dueOn ? body.due_on = dueOn : (dueDate || dueTime) && (dueDate && (body.due_date = dueDate), dueTime && (body.due_time = dueTime)), refType && refId && (body.ref_type = refType, body.ref_id = refId), responsible && (body.responsible = responsible), fileIds && Array.isArray(fileIds) && fileIds.length > 0 && (body.file_ids = fileIds.filter((id) => typeof id == "number" || typeof id == "string" && !isNaN(Number(id)))), labels && Array.isArray(labels) && labels.length > 0 ? body.labels = labels.filter((label) => typeof label == "string" && label.trim().length > 0).map((label) => label.trim()) : labelIds && Array.isArray(labelIds) && labelIds.length > 0 && (body.label_ids = labelIds.filter((id) => typeof id == "number" || typeof id == "string" && !isNaN(Number(id)))), reminder && typeof reminder == "object" && Object.keys(reminder).length > 0 && reminder.remind_delta && typeof reminder.remind_delta == "number" && (body.reminder = reminder), recurrence && typeof recurrence == "object" && Object.keys(recurrence).length > 0 && recurrence.name && recurrence.config && (body.recurrence = recurrence), externalId && externalId.trim() && (body.external_id = externalId.trim());
    let queryParams = {};
    typeof hook == "boolean" && (queryParams.hook = hook.toString()), typeof silent == "boolean" && (queryParams.silent = silent.toString());
    let resourceUri = "/task/";
    return refType && refId ? (resourceUri = `/task/${refType}/${refId}/`, delete body.ref_type, delete body.ref_id) : console.log("\u{1F50D} [Podio Task] Using general task endpoint:", resourceUri), await podioApiCall({
      method: import_pieces_common6.HttpMethod.POST,
      accessToken,
      resourceUri,
      body,
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/podio/src/lib/actions/update-task.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var updateTaskAction = (0, import_pieces_framework6.createAction)({
  auth: podioAuth,
  name: "update_task",
  displayName: "Update Task",
  description: "Modify an existing task's details or status. Only specified fields will be updated.",
  audience: "both",
  aiMetadata: { description: "Updates an existing Podio task identified by task id; only the fields you supply change, and this includes toggling its completed flag, reassigning it, or editing text, due date, labels, and reminders. Use to edit or complete a known task; requires the task id and at least one field to change. Idempotent \u2014 repeating with the same values yields the same task state.", idempotent: !0 },
  props: {
    taskId: dynamicTaskProperty,
    text: import_pieces_framework6.Property.LongText({
      displayName: "Text",
      description: "The updated text (title) for the task",
      required: !1
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      description: "The updated description for the task",
      required: !1
    }),
    dueDate: import_pieces_framework6.Property.DateTime({
      displayName: "Due Date",
      description: "The new due date for the task (in users timezone)",
      required: !1
    }),
    dueTime: import_pieces_framework6.Property.ShortText({
      displayName: "Due Time",
      description: 'The new due time for the task in local timezone (HH:MM:SS format, e.g., "14:30:00")',
      required: !1
    }),
    private: import_pieces_framework6.Property.Checkbox({
      displayName: "Private",
      description: "Whether the task should be private (only creator, assignee and assignor can see it)",
      required: !1
    }),
    completed: import_pieces_framework6.Property.Checkbox({
      displayName: "Completed",
      description: "Mark the task as completed or not completed",
      required: !1
    }),
    orgId: dynamicOrgProperty,
    spaceId: dynamicSpaceProperty,
    appId: dynamicAppProperty,
    refType: dynamicRefTypeProperty,
    refId: dynamicRefIdProperty,
    responsible: import_pieces_framework6.Property.Number({
      displayName: "Responsible User ID",
      description: "The user ID of the person responsible for this task",
      required: !1
    }),
    labels: import_pieces_framework6.Property.Array({
      displayName: "Labels",
      description: "The labels for the task. Either a list of label ids or label texts",
      required: !1
    }),
    fileIds: import_pieces_framework6.Property.Array({
      displayName: "File IDs",
      description: "The list of files that should be attached to the task",
      required: !1
    }),
    reminder: import_pieces_framework6.Property.Object({
      displayName: "Reminder",
      description: 'Optional reminder on this task. Format: {"remind_delta": minutes_before_due_date}. If empty, existing reminder is deleted.',
      required: !1
    }),
    hook: hookProperty,
    silent: silentProperty
  },
  async run(context) {
    let accessToken = getAccessToken(context.auth), {
      taskId,
      text,
      description,
      dueDate,
      dueTime,
      private: isPrivate,
      completed,
      orgId,
      spaceId,
      appId,
      refType,
      refId,
      responsible,
      labels,
      fileIds,
      reminder,
      hook,
      silent
    } = context.propsValue;
    if (!taskId)
      throw new Error("Task ID is required to update a task. Please provide a valid task ID.");
    if (refType && !refId)
      throw new Error("Reference Object is required when Reference Type is specified. Please select an object from the dropdown.");
    if (!refType && refId)
      throw new Error("Reference Type is required when Reference Object is specified. Please select a reference type first.");
    if (refType === "item" && !appId)
      throw new Error("App selection is required when referencing items. Please select an app first.");
    if ((refType === "status" || refType === "task") && !spaceId)
      throw new Error("Space selection is required when referencing status updates or tasks. Please select a space first.");
    if (fileIds && !Array.isArray(fileIds))
      throw new Error("File IDs must be provided as an array of numbers.");
    if (labels && !Array.isArray(labels))
      throw new Error("Labels must be provided as an array of strings or numbers.");
    if (reminder && typeof reminder == "object" && reminder.remind_delta !== void 0 && typeof reminder.remind_delta != "number" && reminder.remind_delta !== null)
      throw new Error("Reminder remind_delta must be a number (minutes before due date) or null to delete existing reminder.");
    let body = {}, updateCount = 0;
    if (text !== void 0) {
      if (typeof text != "string")
        throw new Error("Text must be a string.");
      body.text = text, updateCount++;
    }
    if (description !== void 0) {
      if (typeof description != "string")
        throw new Error("Description must be a string.");
      body.description = description, updateCount++;
    }
    if (dueDate !== void 0 && (body.due_date = dueDate, updateCount++), dueTime !== void 0 && (body.due_time = dueTime, updateCount++), typeof isPrivate == "boolean" && (body.private = isPrivate, updateCount++), typeof completed == "boolean" && (body.completed = completed, updateCount++), refType && (body.ref_type = refType, updateCount++), refId && (body.ref_id = refId, updateCount++), responsible !== void 0) {
      if (typeof responsible != "number")
        throw new Error("Responsible user ID must be a number.");
      body.responsible = responsible, updateCount++;
    }
    if (labels && Array.isArray(labels) && labels.length > 0 && (body.labels = labels.filter(
      (label) => typeof label == "string" && label.trim().length > 0 || typeof label == "number"
    ), updateCount++), fileIds && Array.isArray(fileIds) && fileIds.length > 0 && (body.file_ids = fileIds.filter(
      (id) => typeof id == "number" || typeof id == "string" && !isNaN(Number(id))
    ), updateCount++), reminder && typeof reminder == "object" && (reminder.remind_delta === null || reminder.remind_delta === "" ? (body.reminder = { remind_delta: null }, updateCount++) : typeof reminder.remind_delta == "number" && (body.reminder = reminder, updateCount++)), updateCount === 0)
      throw new Error("At least one field must be provided to update the task. Please specify which fields you want to modify.");
    let queryParams = {};
    return typeof hook == "boolean" && (queryParams.hook = hook.toString()), typeof silent == "boolean" && (queryParams.silent = silent.toString()), await podioApiCall({
      method: import_pieces_common7.HttpMethod.PUT,
      accessToken,
      resourceUri: `/task/${taskId}`,
      body,
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/podio/src/lib/actions/attach-file.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var attachFileAction = (0, import_pieces_framework7.createAction)({
  auth: podioAuth,
  name: "attach_file",
  displayName: "Attach File",
  description: "Upload and attach a file to an item/task/comment.",
  audience: "both",
  aiMetadata: { description: "Attaches an already-uploaded Podio file to a target object \u2014 an item, task, status update, comment, or space \u2014 selected via the reference type and id. Use when linking an existing file to a Podio object; requires the file id, a space, and the target type/id. Not idempotent \u2014 each call adds another attachment.", idempotent: !1 },
  props: {
    orgId: dynamicOrgProperty,
    spaceId: dynamicSpaceProperty,
    appId: dynamicAppProperty,
    fileId: dynamicFileProperty,
    refType: import_pieces_framework7.Property.Dropdown({
      auth: podioAuth,
      displayName: "Attach To",
      description: "What type of object to attach the file to",
      required: !0,
      refreshers: [],
      options: async () => ({
        options: [
          { label: "Item", value: "item" },
          { label: "Task", value: "task" },
          { label: "Status Update", value: "status" },
          { label: "Comment", value: "comment" },
          { label: "Space", value: "space" }
        ]
      })
    }),
    refId: dynamicRefIdProperty,
    silent: silentProperty
  },
  async run(context) {
    let accessToken = getAccessToken(context.auth), { fileId, orgId, spaceId, appId, refType, refId, silent } = context.propsValue;
    if (!spaceId)
      throw new Error("Space selection is required to load and attach files. Please select a space first.");
    if (!fileId)
      throw new Error("File selection is required. Please select a file from the dropdown.");
    if (!refType)
      throw new Error("Attach To selection is required. Please choose what type of object to attach the file to.");
    if (!refId)
      throw new Error("Reference Object is required. Please select the specific object to attach the file to.");
    if (refType === "item" && !appId)
      throw new Error("App selection is required when attaching files to items. Please select an app first.");
    if ((refType === "status" || refType === "task") && !spaceId)
      throw new Error("Space selection is required when attaching files to status updates or tasks. Please select a space first.");
    let body = {
      ref_type: refType,
      ref_id: refId
    }, queryParams = {};
    return typeof silent == "boolean" && (queryParams.silent = silent.toString()), await podioApiCall({
      method: import_pieces_common8.HttpMethod.POST,
      accessToken,
      resourceUri: `/file/${fileId}/attach`,
      body,
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/podio/src/lib/actions/create-comment.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var createCommentAction = (0, import_pieces_framework8.createAction)({
  auth: podioAuth,
  name: "create_comment",
  displayName: "Create Comment",
  description: "Post a comment on an item or task.",
  audience: "both",
  aiMetadata: { description: "Posts a text comment on a Podio item or task, chosen via the comment-on type and object id, with optional file or embed attachments. Use to add a note or reply to a record; requires the target type/id and comment text. Not idempotent \u2014 each call posts a separate comment.", idempotent: !1 },
  props: {
    orgId: dynamicOrgProperty,
    spaceId: dynamicSpaceProperty,
    appId: dynamicAppProperty,
    type: import_pieces_framework8.Property.Dropdown({
      auth: podioAuth,
      displayName: "Comment On",
      description: "What type of object to comment on",
      required: !0,
      refreshers: [],
      options: async () => ({
        options: [
          { label: "Item", value: "item" },
          { label: "Task", value: "task" }
        ]
      })
    }),
    id: import_pieces_framework8.Property.Dropdown({
      auth: podioAuth,
      displayName: "Object",
      description: "Select the specific object to comment on",
      required: !0,
      refreshers: ["type", "appId", "spaceId"],
      options: async ({ auth, type, appId, spaceId }) => {
        var _a, _b;
        if (!auth || !type)
          return {
            disabled: !0,
            placeholder: auth ? "Select an object type first" : "Connect your Podio account first",
            options: []
          };
        try {
          let accessToken = getAccessToken(auth);
          switch (type) {
            case "item":
              return appId ? {
                options: ((_a = (await podioApiCall({
                  method: import_pieces_common9.HttpMethod.POST,
                  accessToken,
                  resourceUri: `/item/app/${appId}/filter/`,
                  body: { limit: 30 }
                })).items) == null ? void 0 : _a.map((item) => ({
                  label: item.title || `Item ${item.item_id}`,
                  value: item.item_id
                }))) || []
              } : {
                disabled: !0,
                placeholder: "Select an app first to load items",
                options: []
              };
            case "task": {
              let userInfo = await podioApiCall({
                method: import_pieces_common9.HttpMethod.GET,
                accessToken,
                resourceUri: "/user/status"
              }), userId = (_b = userInfo == null ? void 0 : userInfo.user) == null ? void 0 : _b.user_id;
              if (!userId)
                return {
                  disabled: !0,
                  options: [],
                  placeholder: "Could not determine user ID for task filtering"
                };
              let taskQueryParams = {
                responsible: userId,
                limit: 30,
                view: "full",
                sort_by: "created_on",
                sort_desc: "true"
              };
              spaceId && (taskQueryParams.space = spaceId);
              let taskResponse = await podioApiCall({
                method: import_pieces_common9.HttpMethod.GET,
                accessToken,
                resourceUri: "/task/",
                queryParams: taskQueryParams
              });
              return {
                options: (taskResponse == null ? void 0 : taskResponse.map((task) => {
                  var _a2;
                  let label = task.text || `Task ${task.task_id}`;
                  return (_a2 = task.ref) != null && _a2.title && (label += ` \u2192 ${task.ref.title}`), {
                    label,
                    value: task.task_id
                  };
                })) || []
              };
            }
            default:
              return {
                options: [],
                placeholder: "Please select Item or Task"
              };
          }
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: `Failed to load ${type}s. Check your permissions.`
          };
        }
      }
    }),
    value: import_pieces_framework8.Property.LongText({
      displayName: "Comment Text",
      description: "The content of your comment",
      required: !0
    }),
    externalId: import_pieces_framework8.Property.ShortText({
      displayName: "External ID",
      description: "Optional external identifier for the comment",
      required: !1
    }),
    fileIds: import_pieces_framework8.Property.Array({
      displayName: "Attach Files",
      description: "Select files from the space to attach to this comment (enter file IDs from the space)",
      required: !1
    }),
    embedId: import_pieces_framework8.Property.Number({
      displayName: "Embed ID",
      description: "ID of a previously created embedded link",
      required: !1
    }),
    embedUrl: import_pieces_framework8.Property.ShortText({
      displayName: "Embed URL",
      description: "URL to embed in the comment",
      required: !1
    }),
    alertInvite: import_pieces_framework8.Property.Checkbox({
      displayName: "Auto-Invite Mentioned Users",
      description: "Automatically invite mentioned users to the workspace if they lack access",
      required: !1,
      defaultValue: !1
    }),
    silent: silentProperty,
    hook: hookProperty
  },
  async run(context) {
    let accessToken = getAccessToken(context.auth), {
      appId,
      spaceId,
      type,
      id,
      value,
      externalId,
      fileIds,
      embedId,
      embedUrl,
      alertInvite,
      silent,
      hook
    } = context.propsValue;
    if (!type)
      throw new Error("Comment target type is required. Please select what type of object to comment on.");
    if (!id)
      throw new Error("Target object is required. Please select the specific object to comment on.");
    if (!value || typeof value != "string" || value.trim().length === 0)
      throw new Error("Comment text is required and cannot be empty.");
    if (type === "item" && !appId)
      throw new Error("App selection is required when commenting on items. Please select an app first.");
    if (fileIds && !Array.isArray(fileIds))
      throw new Error("File IDs must be provided as an array.");
    if (embedId && typeof embedId != "number")
      throw new Error("Embed ID must be a number.");
    let body = {
      value: value.trim()
    };
    externalId && externalId.trim() && (body.external_id = externalId.trim()), fileIds && Array.isArray(fileIds) && fileIds.length > 0 && (body.file_ids = fileIds), embedId && (body.embed_id = embedId), embedUrl && embedUrl.trim() && (body.embed_url = embedUrl.trim()), typeof alertInvite == "boolean" && (body.alert_invite = alertInvite);
    let queryParams = {};
    return typeof silent == "boolean" && (queryParams.silent = silent.toString()), typeof hook == "boolean" && (queryParams.hook = hook.toString()), await podioApiCall({
      method: import_pieces_common9.HttpMethod.POST,
      accessToken,
      resourceUri: `/comment/${type}/${id}`,
      body,
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/podio/src/lib/actions/create-status.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var createStatusAction = (0, import_pieces_framework9.createAction)({
  auth: podioAuth,
  name: "create_status",
  displayName: "Create Status Update",
  description: "Add a status to an item or workspace stream.",
  audience: "both",
  aiMetadata: { description: "Posts a status update to a Podio workspace (space) stream, optionally with file/embed attachments or an attached poll. Use to share an announcement or post to a space feed; requires the space id and a status message. Not idempotent \u2014 each call posts a new status update.", idempotent: !1 },
  props: {
    orgId: dynamicOrgProperty,
    spaceId: dynamicSpaceProperty,
    value: import_pieces_framework9.Property.LongText({
      displayName: "Status Message",
      description: "What would you like to share?",
      required: !0
    }),
    fileIds: import_pieces_framework9.Property.Array({
      displayName: "Attach Files",
      description: "File IDs to attach to this status update (enter file IDs from the space)",
      required: !1
    }),
    embedId: import_pieces_framework9.Property.Number({
      displayName: "Embed ID",
      description: "ID of a previously created embedded link",
      required: !1
    }),
    embedUrl: import_pieces_framework9.Property.ShortText({
      displayName: "Embed URL",
      description: "URL to embed in the status update",
      required: !1
    }),
    question: import_pieces_framework9.Property.Object({
      displayName: "Add Poll",
      description: 'Create a poll with your status. Format: {"text": "Question?", "options": ["Option 1", "Option 2"]}',
      required: !1
    }),
    alertInvite: import_pieces_framework9.Property.Checkbox({
      displayName: "Auto-Invite Mentioned Users",
      description: "Automatically invite mentioned users to the workspace if they lack access",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let accessToken = getAccessToken(context.auth), {
      spaceId,
      value,
      fileIds,
      embedId,
      embedUrl,
      question,
      alertInvite
    } = context.propsValue;
    if (!spaceId)
      throw new Error("Space selection is required. Please select a workspace to post your status to.");
    if (!value || typeof value != "string" || value.trim().length === 0)
      throw new Error("Status message is required and cannot be empty.");
    if (fileIds && !Array.isArray(fileIds))
      throw new Error("File IDs must be provided as an array.");
    if (embedId && typeof embedId != "number")
      throw new Error("Embed ID must be a number.");
    if (question && typeof question == "object") {
      if (!question.text || typeof question.text != "string")
        throw new Error('Poll question must have a "text" property with the question text.');
      if (!question.options || !Array.isArray(question.options) || question.options.length === 0)
        throw new Error('Poll question must have an "options" array with at least one choice.');
    }
    let body = {
      value: value.trim()
    };
    fileIds && Array.isArray(fileIds) && fileIds.length > 0 && (body.file_ids = fileIds), embedId && (body.embed_id = embedId), embedUrl && embedUrl.trim() && (body.embed_url = embedUrl.trim()), question && typeof question == "object" && question.text && question.options && (body.question = question);
    let queryParams = {};
    return typeof alertInvite == "boolean" && (queryParams.alert_invite = alertInvite.toString()), await podioApiCall({
      method: import_pieces_common10.HttpMethod.POST,
      accessToken,
      resourceUri: `/status/space/${spaceId}/`,
      body,
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/podio/src/lib/actions/find-item.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var findItemAction = (0, import_pieces_framework10.createAction)({
  auth: podioAuth,
  name: "find_item",
  displayName: "Find Item",
  description: "Retrieve a single item by ID or field value.",
  audience: "both",
  aiMetadata: { description: "Looks up Podio items in one of two modes: fetch a single item directly by its item id, or filter items within an app by field criteria (with limit, offset, and sorting). Use to read a known record or search an app for matching records; the filter mode requires an app id. Idempotent \u2014 a read that does not modify data.", idempotent: !0 },
  props: {
    searchType: import_pieces_framework10.Property.Dropdown({
      auth: podioAuth,
      displayName: "Search Method",
      description: "How to find the item",
      required: !0,
      refreshers: [],
      options: async () => ({
        options: [
          { label: "Get by Item ID", value: "by_id" },
          { label: "Filter Items in App", value: "filter" }
        ]
      })
    }),
    itemId: import_pieces_framework10.Property.Number({
      displayName: "Item ID",
      description: "The specific item ID to retrieve",
      required: !1
    }),
    selectedItem: dynamicItemProperty,
    markAsViewed: import_pieces_framework10.Property.Checkbox({
      displayName: "Mark as Viewed",
      description: "Mark any new notifications on this item as viewed",
      required: !1,
      defaultValue: !0
    }),
    orgId: dynamicOrgProperty,
    spaceId: dynamicSpaceProperty,
    appId: dynamicAppProperty,
    filters: import_pieces_framework10.Property.Object({
      displayName: "Search Filters",
      description: "Filter criteria as JSON object. Use field external_id as keys.",
      required: !1
    }),
    limit: limitProperty,
    offset: offsetProperty,
    sortBy: import_pieces_framework10.Property.ShortText({
      displayName: "Sort By",
      description: 'Field to sort results by (e.g., "created_on", "last_edit_on")',
      required: !1
    }),
    sortDesc: import_pieces_framework10.Property.Checkbox({
      displayName: "Sort Descending",
      description: "Sort results in descending order",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let accessToken = getAccessToken(context.auth), {
      searchType,
      itemId,
      selectedItem,
      markAsViewed,
      orgId,
      spaceId,
      appId,
      filters,
      limit,
      offset,
      sortBy,
      sortDesc
    } = context.propsValue;
    if (searchType === "by_id") {
      let finalItemId = selectedItem || itemId;
      if (!finalItemId)
        throw new Error("Item ID is required. Please provide an item ID or select an item from the dropdown.");
      let queryParams = {};
      return typeof markAsViewed == "boolean" && (queryParams.mark_as_viewed = markAsViewed.toString()), await podioApiCall({
        method: import_pieces_common11.HttpMethod.GET,
        accessToken,
        resourceUri: `/item/${finalItemId}`,
        queryParams
      });
    } else if (searchType === "filter") {
      if (!appId)
        throw new Error("App selection is required for filtering. Please select an app first.");
      if (limit && (limit < 1 || limit > 500))
        throw new Error("Limit must be between 1 and 500.");
      if (offset && offset < 0)
        throw new Error("Offset must be 0 or greater.");
      let body = {};
      return filters && typeof filters == "object" && Object.keys(filters).length > 0 && (body.filters = filters), limit && (body.limit = limit), offset && (body.offset = offset), sortBy && sortBy.trim() && (body.sort_by = sortBy.trim(), body.sort_desc = !!sortDesc), await podioApiCall({
        method: import_pieces_common11.HttpMethod.POST,
        accessToken,
        resourceUri: `/item/app/${appId}/filter/`,
        body
      });
    } else
      throw new Error('Invalid search method. Please select either "Get by Item ID" or "Filter Items in App".');
  }
});

// ../ap/packages/pieces/community/podio/src/lib/actions/find-task.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var findTaskAction = (0, import_pieces_framework11.createAction)({
  auth: podioAuth,
  name: "find_task",
  displayName: "Find Task",
  description: "Retrieve a task by ID for further updates.",
  audience: "both",
  aiMetadata: { description: "Fetches a single Podio task by its task id. Use to read a task's current details before updating it or to verify it exists; requires the task id. Idempotent \u2014 a read that does not modify data.", idempotent: !0 },
  props: {
    taskId: dynamicTaskProperty
  },
  async run(context) {
    let accessToken = getAccessToken(context.auth), { taskId } = context.propsValue;
    if (!taskId)
      throw new Error("Task selection is required. Please select a task from the dropdown.");
    return await podioApiCall({
      method: import_pieces_common12.HttpMethod.GET,
      accessToken,
      resourceUri: `/task/${taskId}`
    });
  }
});

// ../ap/packages/pieces/community/podio/src/lib/triggers/new-item.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var newItemTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: podioAuth,
  name: "new_item",
  displayName: "New Item",
  description: "Fires when a new item (record/entry) is created in an app",
  aiMetadata: {
    description: "Fires when a new item (record/entry) is created in the selected Podio app, via an item.create webhook on that app. Represents a newly added record."
  },
  props: {
    appId: dynamicAppProperty
  },
  sampleData: {
    type: "item.create",
    item_id: 1234567890,
    item_revision_id: 1,
    external_id: "external-item-123"
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let accessToken = getAccessToken(context.auth), { appId } = context.propsValue;
    if (!appId)
      throw new Error("App selection is required to enable the New Item trigger. Please select an app.");
    try {
      let response = await podioApiCall({
        method: import_pieces_common13.HttpMethod.POST,
        accessToken,
        resourceUri: `/hook/app/${appId}/`,
        body: {
          url: context.webhookUrl,
          type: "item.create"
        }
      });
      if (!response.hook_id)
        throw new Error("Failed to create webhook: No hook ID returned from Podio API.");
      await context.store.put("podio_new_item_hook_id", response.hook_id), await context.store.put("podio_new_item_app_id", appId);
    } catch (error) {
      throw new Error(`Failed to enable New Item trigger: ${error.message || "Unknown error occurred"}`);
    }
  },
  async onDisable(context) {
    var _a;
    let accessToken = getAccessToken(context.auth), hookId = await context.store.get("podio_new_item_hook_id");
    if (hookId)
      try {
        await podioApiCall({
          method: import_pieces_common13.HttpMethod.DELETE,
          accessToken,
          resourceUri: `/hook/${hookId}`
        });
      } catch (error) {
        if (((_a = error.response) == null ? void 0 : _a.status) === 404)
          return;
        throw new Error(`Failed to disable New Item trigger: ${error.message || "Unknown error occurred"}`);
      } finally {
        await context.store.delete("podio_new_item_hook_id"), await context.store.delete("podio_new_item_app_id");
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return !payload || typeof payload != "object" ? [] : !payload.type || payload.type !== "item.create" ? [] : payload.item_id ? [payload] : [];
  }
});

// ../ap/packages/pieces/community/podio/src/lib/triggers/new-task.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var newTaskTrigger = (0, import_pieces_framework13.createTrigger)({
  auth: podioAuth,
  name: "new_task",
  displayName: "New Task",
  description: "Fires when a new task is added to any workspace",
  aiMetadata: {
    description: "Fires when a new task is created in the selected Podio workspace (space), via a task.create webhook on that space. Represents a newly added task."
  },
  props: {
    orgId: dynamicOrgProperty,
    spaceId: dynamicSpaceProperty
  },
  sampleData: {
    type: "task.create",
    task_id: 987654321
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let accessToken = getAccessToken(context.auth), { spaceId } = context.propsValue;
    if (!spaceId)
      throw new Error("Space selection is required to enable the New Task trigger. Please select a workspace.");
    try {
      let response = await podioApiCall({
        method: import_pieces_common14.HttpMethod.POST,
        accessToken,
        resourceUri: `/hook/space/${spaceId}/`,
        body: {
          url: context.webhookUrl,
          type: "task.create"
        }
      });
      if (!response.hook_id)
        throw new Error("Failed to create webhook: No hook ID returned from Podio API.");
      await context.store.put("podio_new_task_hook_id", response.hook_id), await context.store.put("podio_new_task_space_id", spaceId);
    } catch (error) {
      throw new Error(`Failed to enable New Task trigger: ${error.message || "Unknown error occurred"}`);
    }
  },
  async onDisable(context) {
    var _a;
    let accessToken = getAccessToken(context.auth), hookId = await context.store.get("podio_new_task_hook_id");
    if (hookId)
      try {
        await podioApiCall({
          method: import_pieces_common14.HttpMethod.DELETE,
          accessToken,
          resourceUri: `/hook/${hookId}`
        });
      } catch (error) {
        if (((_a = error.response) == null ? void 0 : _a.status) === 404)
          return;
        throw new Error(`Failed to disable New Task trigger: ${error.message || "Unknown error occurred"}`);
      } finally {
        await context.store.delete("podio_new_task_hook_id"), await context.store.delete("podio_new_task_space_id");
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return !payload || typeof payload != "object" ? [] : !payload.type || payload.type !== "task.create" ? [] : payload.task_id ? [payload] : [];
  }
});

// ../ap/packages/pieces/community/podio/src/lib/triggers/new-activity.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var newActivityTrigger = (0, import_pieces_framework14.createTrigger)({
  auth: podioAuth,
  name: "new_activity",
  displayName: "New Status",
  description: "Fires when a new status is created in a workspace stream",
  aiMetadata: {
    description: "Fires when a new status update is posted to the selected Podio workspace (space) stream, via a status.create webhook on that space. Represents a newly shared status update."
  },
  props: {
    orgId: dynamicOrgProperty,
    spaceId: dynamicSpaceProperty
  },
  sampleData: {
    type: "status.create",
    status_id: 1122334455
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let accessToken = getAccessToken(context.auth), { spaceId } = context.propsValue;
    if (!spaceId)
      throw new Error("Space selection is required to enable the New Status trigger. Please select a workspace.");
    try {
      let response = await podioApiCall({
        method: import_pieces_common15.HttpMethod.POST,
        accessToken,
        resourceUri: `/hook/space/${spaceId}/`,
        body: {
          url: context.webhookUrl,
          type: "status.create"
        }
      });
      if (!response.hook_id)
        throw new Error("Failed to create webhook: No hook ID returned from Podio API.");
      await context.store.put("podio_new_activity_hook_id", response.hook_id), await context.store.put("podio_new_activity_space_id", spaceId);
    } catch (error) {
      throw new Error(`Failed to enable New Status trigger: ${error.message || "Unknown error occurred"}`);
    }
  },
  async onDisable(context) {
    var _a;
    let accessToken = getAccessToken(context.auth), hookId = await context.store.get("podio_new_activity_hook_id");
    if (hookId)
      try {
        await podioApiCall({
          method: import_pieces_common15.HttpMethod.DELETE,
          accessToken,
          resourceUri: `/hook/${hookId}`
        });
      } catch (error) {
        if (((_a = error.response) == null ? void 0 : _a.status) === 404)
          return;
        throw new Error(`Failed to disable New Status trigger: ${error.message || "Unknown error occurred"}`);
      } finally {
        await context.store.delete("podio_new_activity_hook_id"), await context.store.delete("podio_new_activity_space_id");
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return !payload || typeof payload != "object" ? [] : !payload.type || payload.type !== "status.create" ? [] : payload.status_id ? [payload] : [];
  }
});

// ../ap/packages/pieces/community/podio/src/lib/triggers/item-updated.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var itemUpdatedTrigger = (0, import_pieces_framework15.createTrigger)({
  auth: podioAuth,
  name: "item_updated",
  displayName: "Item Updated",
  description: "Fires when an existing item is updated (excluding comments)",
  aiMetadata: {
    description: "Fires when an existing item in the selected Podio app is updated (field changes, not comments), via an item.update webhook on that app. Represents a modified record."
  },
  props: {
    appId: dynamicAppProperty
  },
  sampleData: {
    type: "item.update",
    item_id: 1234567890,
    item_revision_id: 3,
    external_id: "external-item-123"
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let accessToken = getAccessToken(context.auth), { appId } = context.propsValue;
    if (!appId)
      throw new Error("App selection is required to enable the Item Updated trigger. Please select an app.");
    try {
      let response = await podioApiCall({
        method: import_pieces_common16.HttpMethod.POST,
        accessToken,
        resourceUri: `/hook/app/${appId}/`,
        body: {
          url: context.webhookUrl,
          type: "item.update"
        }
      });
      if (!response.hook_id)
        throw new Error("Failed to create webhook: No hook ID returned from Podio API.");
      await context.store.put("podio_item_updated_hook_id", response.hook_id), await context.store.put("podio_item_updated_app_id", appId);
    } catch (error) {
      throw new Error(`Failed to enable Item Updated trigger: ${error.message || "Unknown error occurred"}`);
    }
  },
  async onDisable(context) {
    var _a;
    let accessToken = getAccessToken(context.auth), hookId = await context.store.get("podio_item_updated_hook_id");
    if (hookId)
      try {
        await podioApiCall({
          method: import_pieces_common16.HttpMethod.DELETE,
          accessToken,
          resourceUri: `/hook/${hookId}`
        });
      } catch (error) {
        if (((_a = error.response) == null ? void 0 : _a.status) === 404)
          return;
        throw new Error(`Failed to disable Item Updated trigger: ${error.message || "Unknown error occurred"}`);
      } finally {
        await context.store.delete("podio_item_updated_hook_id"), await context.store.delete("podio_item_updated_app_id");
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return !payload || typeof payload != "object" ? [] : !payload.type || payload.type !== "item.update" ? [] : payload.item_id ? [payload] : [];
  }
});

// ../ap/packages/pieces/community/podio/src/lib/triggers/new-organization.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var newOrganizationTrigger = (0, import_pieces_framework16.createTrigger)({
  auth: podioAuth,
  name: "new_app",
  displayName: "New App",
  description: "Fires when a new app is created in a workspace",
  aiMetadata: {
    description: "Fires when a new app is created in the selected Podio workspace (space), via an app.create webhook on that space. Represents a newly added app."
  },
  props: {
    orgId: dynamicOrgProperty,
    spaceId: dynamicSpaceProperty
  },
  sampleData: {
    type: "app.create",
    app_id: 111222333
  },
  type: import_pieces_framework16.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let accessToken = getAccessToken(context.auth), { spaceId } = context.propsValue;
    if (!spaceId)
      throw new Error("Space selection is required to enable the New App trigger. Please select a workspace.");
    try {
      let response = await podioApiCall({
        method: import_pieces_common17.HttpMethod.POST,
        accessToken,
        resourceUri: `/hook/space/${spaceId}/`,
        body: {
          url: context.webhookUrl,
          type: "app.create"
        }
      });
      if (!response.hook_id)
        throw new Error("Failed to create webhook: No hook ID returned from Podio API.");
      await context.store.put("podio_new_app_hook_id", response.hook_id), await context.store.put("podio_new_app_space_id", spaceId);
    } catch (error) {
      throw new Error(`Failed to enable New App trigger: ${error.message || "Unknown error occurred"}`);
    }
  },
  async onDisable(context) {
    var _a;
    let accessToken = getAccessToken(context.auth), hookId = await context.store.get("podio_new_app_hook_id");
    if (hookId)
      try {
        await podioApiCall({
          method: import_pieces_common17.HttpMethod.DELETE,
          accessToken,
          resourceUri: `/hook/${hookId}`
        });
      } catch (error) {
        if (((_a = error.response) == null ? void 0 : _a.status) === 404)
          return;
        throw new Error(`Failed to disable New App trigger: ${error.message || "Unknown error occurred"}`);
      } finally {
        await context.store.delete("podio_new_app_hook_id"), await context.store.delete("podio_new_app_space_id");
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return !payload || typeof payload != "object" ? [] : !payload.type || payload.type !== "app.create" ? [] : payload.app_id ? [payload] : [];
  }
});

// ../ap/packages/pieces/community/podio/src/lib/triggers/new-workspace.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common");
var newWorkspaceTrigger = (0, import_pieces_framework17.createTrigger)({
  auth: podioAuth,
  name: "member_added",
  displayName: "Member Added",
  description: "Fires when a new member is added to a workspace",
  aiMetadata: {
    description: "Fires when a new member is added to the selected Podio workspace (space), via a member.add webhook on that space. Represents a user gaining access to the workspace."
  },
  props: {
    orgId: dynamicOrgProperty,
    spaceId: dynamicSpaceProperty
  },
  sampleData: {
    type: "member.add",
    space_id: 456789012,
    user_id: 234567
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let accessToken = getAccessToken(context.auth), { spaceId } = context.propsValue;
    if (!spaceId)
      throw new Error("Space selection is required to enable the Member Added trigger. Please select a workspace.");
    try {
      let response = await podioApiCall({
        method: import_pieces_common18.HttpMethod.POST,
        accessToken,
        resourceUri: `/hook/space/${spaceId}/`,
        body: {
          url: context.webhookUrl,
          type: "member.add"
        }
      });
      if (!response.hook_id)
        throw new Error("Failed to create webhook: No hook ID returned from Podio API.");
      await context.store.put("podio_member_added_hook_id", response.hook_id), await context.store.put("podio_member_added_space_id", spaceId);
    } catch (error) {
      throw new Error(`Failed to enable Member Added trigger: ${error.message || "Unknown error occurred"}`);
    }
  },
  async onDisable(context) {
    var _a;
    let accessToken = getAccessToken(context.auth), hookId = await context.store.get("podio_member_added_hook_id");
    if (hookId)
      try {
        await podioApiCall({
          method: import_pieces_common18.HttpMethod.DELETE,
          accessToken,
          resourceUri: `/hook/${hookId}`
        });
      } catch (error) {
        if (((_a = error.response) == null ? void 0 : _a.status) === 404)
          return;
        throw new Error(`Failed to disable Member Added trigger: ${error.message || "Unknown error occurred"}`);
      } finally {
        await context.store.delete("podio_member_added_hook_id"), await context.store.delete("podio_member_added_space_id");
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return !payload || typeof payload != "object" ? [] : !payload.type || payload.type !== "member.add" ? [] : !payload.space_id || !payload.user_id ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/podio/src/index.ts
var podio = (0, import_pieces_framework18.createPiece)({
  displayName: "Podio",
  auth: podioAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/podio.png",
  authors: ["sparkybug", "onyedikachi-david"],
  actions: [
    createItemAction,
    updateItemAction,
    createTaskAction,
    updateTaskAction,
    attachFileAction,
    createCommentAction,
    createStatusAction,
    findItemAction,
    findTaskAction,
    (0, import_pieces_common19.createCustomApiCallAction)({
      baseUrl: () => "https://api.podio.com",
      auth: podioAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [
    newItemTrigger,
    newTaskTrigger,
    newActivityTrigger,
    itemUpdatedTrigger,
    newOrganizationTrigger,
    newWorkspaceTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  podio
});
