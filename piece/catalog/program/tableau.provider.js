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

// ../ap/packages/pieces/community/tableau/src/index.ts
var index_exports = {};
__export(index_exports, {
  tableau: () => tableau
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_framework17 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tableau/src/lib/actions/download-view.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/tableau/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdown = `
## Tableau Authentication Setup

### For Tableau Cloud:
- **Server URL**: Use your Tableau Cloud URL (e.g., https://10az.online.tableau.com or https://us-east-1.online.tableau.com)
- **Site Content URL**: Your site name (leave empty for default site)

### For Tableau Server:
- **Server URL**: Your Tableau Server URL (e.g., https://tableau.yourcompany.com)
- **Site Content URL**: Your site name (leave empty for default site)

### Authentication Methods:
Choose **either** Username/Password **or** Personal Access Token:

#### Option 1: Username and Password
- Enter your Tableau username and password
- For Tableau Cloud, use your email address as username
- For Tableau Server, use your domain\\username if using Active Directory

#### Option 2: Personal Access Token (Recommended)
- Go to your Tableau account settings
- Generate a Personal Access Token
- Enter the token name and secret

**Security Note:** Personal Access Tokens are recommended for production use as they provide better security and can be revoked individually.
`, tableauAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdown,
  required: !0,
  props: {
    serverUrl: import_pieces_framework.Property.ShortText({
      displayName: "Server URL",
      description: "Your Tableau Server or Tableau Cloud URL (e.g., https://10az.online.tableau.com)",
      required: !0
    }),
    apiVersion: import_pieces_framework.Property.ShortText({
      displayName: "API Version",
      description: "Tableau REST API version (e.g., 3.19, 3.20, 3.26). Check your Tableau server documentation for supported versions. Use 3.26 for most Tableau Cloud instances.",
      required: !0,
      defaultValue: "3.26"
    }),
    siteContentUrl: import_pieces_framework.Property.ShortText({
      displayName: "Site Content URL",
      description: "Your Tableau site name (leave empty for default site)",
      required: !1
    }),
    // Username/Password authentication
    username: import_pieces_framework.Property.ShortText({
      displayName: "Username",
      description: "Your Tableau username (email for Tableau Cloud, or domain\\username for Server)",
      required: !1
    }),
    password: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Password",
      description: "Your Tableau password",
      required: !1
    }),
    // PAT authentication
    personalAccessTokenName: import_pieces_framework.Property.ShortText({
      displayName: "Personal Access Token Name",
      description: "Name of your Personal Access Token",
      required: !1
    }),
    personalAccessTokenSecret: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Personal Access Token Secret",
      description: "Secret value of your Personal Access Token",
      required: !1
    })
  },
  validate: async ({ auth }) => {
    let hasUsernamePassword = auth.username && auth.password, hasPersonalAccessToken = auth.personalAccessTokenName && auth.personalAccessTokenSecret;
    return !hasUsernamePassword && !hasPersonalAccessToken ? {
      valid: !1,
      error: "Either Username/Password or Personal Access Token must be provided."
    } : hasUsernamePassword && hasPersonalAccessToken ? {
      valid: !1,
      error: "Please provide either Username/Password OR Personal Access Token, not both."
    } : {
      valid: !0
    };
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common");
function ensureString(body) {
  return typeof body == "string" ? body : typeof body == "object" ? JSON.stringify(body) : String(body);
}
function getTableauHeaders(authToken) {
  return {
    "X-Tableau-Auth": authToken,
    Accept: "application/xml",
    "Content-Type": "application/xml"
  };
}
function buildTableauUrl(serverUrl, apiVersion, siteId, endpoint) {
  return `${serverUrl}/api/${apiVersion}/sites/${siteId}/${endpoint}`;
}
var authCache = /* @__PURE__ */ new Map();
function getCacheKey(auth) {
  return `${auth.serverUrl}-${auth.siteContentUrl || "default"}-${auth.username || auth.personalAccessTokenName}`;
}
async function getAuthToken(auth) {
  var _a, _b, _c, _d, _e;
  let cacheKey = getCacheKey(auth), now = Date.now(), cached = authCache.get(cacheKey);
  if (cached && cached.expiresAt > now + 3e5)
    return { token: cached.token, siteId: cached.siteId };
  let credentialsXml;
  if (auth.username && auth.password)
    credentialsXml = `<credentials name="${auth.username}" password="${auth.password}">`;
  else if (auth.personalAccessTokenName && auth.personalAccessTokenSecret)
    credentialsXml = `<credentials personalAccessTokenName="${auth.personalAccessTokenName}" personalAccessTokenSecret="${auth.personalAccessTokenSecret}">`;
  else
    throw new Error("Authentication credentials are missing");
  let siteXml = auth.siteContentUrl ? `<site contentUrl="${auth.siteContentUrl}" />` : "<site />", signinXml = `<?xml version="1.0" encoding="UTF-8"?>
<tsRequest>
  ${credentialsXml}
    ${siteXml}
  </credentials>
</tsRequest>`, apiVersion = auth.apiVersion || "3.26", signinResponse = await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${auth.serverUrl}/api/${apiVersion}/auth/signin`,
    headers: {
      "Content-Type": "application/xml",
      Accept: "application/xml"
    },
    body: signinXml
  });
  if (signinResponse.status !== 200)
    throw new Error(`Authentication failed: ${signinResponse.status} - ${JSON.stringify(signinResponse.body)}`);
  let authToken = null, siteId = null;
  if (typeof signinResponse.body == "object" && signinResponse.body !== null) {
    let body = signinResponse.body;
    if ((_a = body.tsResponse) != null && _a.credentials ? (authToken = body.tsResponse.credentials.token || body.tsResponse.credentials["@token"], siteId = ((_b = body.tsResponse.credentials.site) == null ? void 0 : _b.id) || ((_c = body.tsResponse.credentials.site) == null ? void 0 : _c["@id"])) : body.credentials && (authToken = body.credentials.token || body.credentials["@token"], siteId = ((_d = body.credentials.site) == null ? void 0 : _d.id) || ((_e = body.credentials.site) == null ? void 0 : _e["@id"])), !authToken || !siteId) {
      let responseXml = JSON.stringify(signinResponse.body), tokenMatch = responseXml.match(/token["']?\s*:\s*["']?([^"',}\s]+)/), siteIdMatch = responseXml.match(/id["']?\s*:\s*["']?([^"',}\s]+)/);
      authToken = tokenMatch ? tokenMatch[1] : null, siteId = siteIdMatch ? siteIdMatch[1] : null;
    }
  } else {
    let responseXml = ensureString(signinResponse.body), tokenMatch = responseXml.match(/token="([^"]+)"/);
    authToken = tokenMatch ? tokenMatch[1] : null;
    let siteIdMatch = responseXml.match(/<site[^>]*id="([^"]+)"/);
    siteId = siteIdMatch ? siteIdMatch[1] : null;
  }
  if (!authToken || !siteId)
    throw new Error(`Failed to obtain authentication token or site ID from response. Response type: ${typeof signinResponse.body}. Response: ${JSON.stringify(signinResponse.body).substring(0, 500)}`);
  let tokenLifetime = auth.serverUrl.includes(".online.tableau.com") ? 7200 * 1e3 : 14400 * 1e3;
  return authCache.set(cacheKey, {
    token: authToken,
    siteId,
    expiresAt: now + tokenLifetime
  }), { token: authToken, siteId };
}
async function listExtractRefreshTasks(auth) {
  let { token: authToken, siteId } = await getAuthToken(auth), apiVersion = auth.apiVersion || "3.26", listTasksUrl = buildTableauUrl(auth.serverUrl, apiVersion, siteId, "tasks/extractRefreshes"), response = await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: listTasksUrl,
    headers: getTableauHeaders(authToken)
  });
  if (response.status !== 200)
    throw new Error(`Failed to list extract refresh tasks: ${response.status}`);
  let responseBody = ensureString(response.body), tasks = [], taskMatches = responseBody.matchAll(/<extractRefresh[^>]*id="([^"]+)"[^>]*priority="([^"]*)"[^>]*consecutiveFailedCount="([^"]*)"[^>]*type="([^"]*)"/g);
  for (let match of taskMatches) {
    let [, taskId, priority, consecutiveFailedCount, type] = match, scheduleMatch = responseBody.match(new RegExp(`<extractRefresh[^>]*id="${taskId}"[^>]*>[\\s\\S]*?<schedule[^>]*frequency="([^"]*)"[^>]*nextRunAt="([^"]*)"[^>]*>`)), frequency = scheduleMatch ? scheduleMatch[1] : null, nextRunAt = scheduleMatch ? scheduleMatch[2] : null, workbookMatch = responseBody.match(new RegExp(`<extractRefresh[^>]*id="${taskId}"[^>]*>[\\s\\S]*?<workbook[^>]*id="([^"]+)"`)), datasourceMatch = responseBody.match(new RegExp(`<extractRefresh[^>]*id="${taskId}"[^>]*>[\\s\\S]*?<datasource[^>]*id="([^"]+)"`)), workbookId = workbookMatch ? workbookMatch[1] : null, datasourceId = datasourceMatch ? datasourceMatch[1] : null;
    tasks.push({
      id: taskId,
      priority: parseInt(priority),
      consecutiveFailedCount: parseInt(consecutiveFailedCount),
      type,
      schedule: {
        frequency,
        nextRunAt
      },
      workbookId,
      datasourceId
    });
  }
  return tasks;
}
async function queryWorkbooks(auth) {
  let { token: authToken, siteId } = await getAuthToken(auth), apiVersion = auth.apiVersion || "3.26", queryWorkbooksUrl = buildTableauUrl(auth.serverUrl, apiVersion, siteId, "workbooks"), response = await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: queryWorkbooksUrl,
    headers: getTableauHeaders(authToken)
  });
  if (response.status !== 200)
    throw new Error(`Failed to query workbooks: ${response.status}`);
  let responseBody = ensureString(response.body), workbooks = [], workbookMatches = responseBody.matchAll(/<workbook[^>]*id="([^"]+)"[^>]*name="([^"]+)"[^>]*contentUrl="([^"]*)"[^>]*>/g);
  for (let match of workbookMatches) {
    let [, workbookId, name, contentUrl] = match;
    workbooks.push({
      id: workbookId,
      name,
      contentUrl
    });
  }
  return workbooks;
}
async function queryViews(auth) {
  let { token: authToken, siteId } = await getAuthToken(auth), apiVersion = auth.apiVersion || "3.26", queryViewsUrl = buildTableauUrl(auth.serverUrl, apiVersion, siteId, "views"), response = await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: queryViewsUrl,
    headers: getTableauHeaders(authToken)
  });
  if (response.status !== 200)
    throw new Error(`Failed to query views: ${response.status}`);
  let responseBody = ensureString(response.body), views = [], viewMatches = responseBody.matchAll(/<view[^>]*id="([^"]+)"[^>]*name="([^"]+)"[^>]*contentUrl="([^"]*)"[^>]*viewUrlName="([^"]*)"/g);
  for (let match of viewMatches) {
    let [, viewId, name, contentUrl, viewUrlName] = match;
    views.push({
      id: viewId,
      name,
      contentUrl,
      viewUrlName
    });
  }
  return views;
}
async function createWebhook(auth, webhookData) {
  let { token: authToken, siteId } = await getAuthToken(auth), apiVersion = auth.apiVersion || "3.26", createWebhookUrl = buildTableauUrl(auth.serverUrl, apiVersion, siteId, "webhooks"), requestBody = `<?xml version="1.0" encoding="UTF-8"?>
<tsRequest>
  <webhook name="${webhookData.name}" event="${webhookData.event}">
    <webhook-destination>
      <webhook-destination-http method="POST" url="${webhookData.destinationUrl}" />
    </webhook-destination>
  </webhook>
</tsRequest>`, response = await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: createWebhookUrl,
    headers: __spreadProps(__spreadValues({}, getTableauHeaders(authToken)), {
      "Content-Type": "application/xml"
    }),
    body: requestBody
  });
  if (response.status !== 201)
    throw new Error(`Failed to create webhook: ${response.status} - ${response.body}`);
  let webhookIdMatch = ensureString(response.body).match(/<webhook[^>]*id="([^"]+)"/), webhookId = webhookIdMatch ? webhookIdMatch[1] : null;
  if (!webhookId)
    throw new Error("Failed to extract webhook ID from response");
  return webhookId;
}
async function deleteWebhook(auth, webhookId) {
  let { token: authToken, siteId } = await getAuthToken(auth), apiVersion = auth.apiVersion || "3.26", deleteWebhookUrl = buildTableauUrl(auth.serverUrl, apiVersion, siteId, `webhooks/${webhookId}`), response = await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.DELETE,
    url: deleteWebhookUrl,
    headers: getTableauHeaders(authToken)
  });
  if (response.status !== 204)
    throw new Error(`Failed to delete webhook: ${response.status} - ${response.body}`);
}

// ../ap/packages/pieces/community/tableau/src/lib/actions/download-view.ts
var downloadView = (0, import_pieces_framework2.createAction)({
  name: "download_view",
  displayName: "Download View",
  description: "Download a view from a workbook in the specified format",
  audience: "both",
  aiMetadata: { description: "Exports a single Tableau view (identified by its view ID) as a file in one of four formats \u2014 CSV data, PNG image, PDF, or Excel crosstab \u2014 each producing a different output and accepting format-specific sizing/page options. Use to capture a view's rendered content or underlying data for sharing or downstream processing. Read-only against Tableau and safe to repeat, though the rendered result reflects data current at call time (subject to the cache max-age).", idempotent: !0 },
  auth: tableauAuth,
  props: {
    viewId: import_pieces_framework2.Property.Dropdown({
      auth: tableauAuth,
      displayName: "View",
      description: "Select the view to download",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your Tableau account first"
          };
        try {
          return {
            disabled: !1,
            options: (await queryViews(auth)).map((view) => ({
              label: view.name,
              value: view.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load views. Please check your authentication."
          };
        }
      }
    }),
    format: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Format",
      description: "The format to download the view in",
      required: !0,
      options: {
        options: [
          { label: "CSV", value: "csv" },
          { label: "Image (PNG)", value: "image" },
          { label: "PDF", value: "pdf" },
          { label: "Excel Crosstab", value: "excel" }
        ]
      }
    }),
    maxAge: import_pieces_framework2.Property.Number({
      displayName: "Max Age (minutes)",
      description: "Maximum age of cached data in minutes (default: 60)",
      required: !1,
      defaultValue: 60
    }),
    resolution: import_pieces_framework2.Property.ShortText({
      displayName: "Image Resolution",
      description: "Resolution for image format (high for maximum pixel density)",
      required: !1
    }),
    vizWidth: import_pieces_framework2.Property.Number({
      displayName: "Visualization Width",
      description: "Width of the rendered image/PDF in pixels",
      required: !1
    }),
    vizHeight: import_pieces_framework2.Property.Number({
      displayName: "Visualization Height",
      description: "Height of the rendered image/PDF in pixels",
      required: !1
    }),
    pageType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Page Type",
      description: "Page size for PDF format",
      required: !1,
      options: {
        options: [
          { label: "Letter", value: "Letter" },
          { label: "Legal", value: "Legal" },
          { label: "A4", value: "A4" },
          { label: "A3", value: "A3" },
          { label: "A5", value: "A5" },
          { label: "B5", value: "B5" },
          { label: "Executive", value: "Executive" },
          { label: "Folio", value: "Folio" },
          { label: "Ledger", value: "Ledger" },
          { label: "Note", value: "Note" },
          { label: "Quarto", value: "Quarto" },
          { label: "Tabloid", value: "Tabloid" }
        ]
      }
    }),
    orientation: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Orientation",
      description: "Page orientation for PDF format",
      required: !1,
      options: {
        options: [
          { label: "Portrait", value: "Portrait" },
          { label: "Landscape", value: "Landscape" }
        ]
      }
    })
  },
  async run({ auth, propsValue, files }) {
    let { viewId, format, maxAge = 60, resolution, vizWidth, vizHeight, pageType, orientation } = propsValue, tableauAuth2 = auth, { token: authToken, siteId } = await getAuthToken(tableauAuth2), apiVersion = tableauAuth2.apiVersion || "3.26", downloadUrl = buildTableauUrl(tableauAuth2.serverUrl, apiVersion, siteId, `views/${viewId}`), queryParams = new URLSearchParams();
    switch (maxAge && maxAge > 0 && queryParams.append("maxAge", maxAge.toString()), format) {
      case "csv":
        downloadUrl += "/data";
        break;
      case "image":
        downloadUrl += "/image", resolution && queryParams.append("resolution", resolution), vizWidth && vizHeight && (queryParams.append("vizWidth", vizWidth.toString()), queryParams.append("vizHeight", vizHeight.toString()));
        break;
      case "pdf":
        downloadUrl += "/pdf", vizWidth && vizHeight && (queryParams.append("vizWidth", vizWidth.toString()), queryParams.append("vizHeight", vizHeight.toString())), pageType && queryParams.append("type", pageType), orientation && queryParams.append("orientation", orientation);
        break;
      case "excel":
        downloadUrl += "/crosstab/excel";
        break;
      default:
        throw new Error(`Unsupported format: ${format}`);
    }
    let queryString = queryParams.toString();
    queryString && (downloadUrl += `?${queryString}`);
    let downloadResponse = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: downloadUrl,
      headers: {
        "X-Tableau-Auth": authToken
      }
    });
    if (downloadResponse.status !== 200)
      throw new Error(`Download failed: ${downloadResponse.status} - ${downloadResponse.body}`);
    let fileExtension;
    switch (format) {
      case "csv":
        fileExtension = "csv";
        break;
      case "image":
        fileExtension = "png";
        break;
      case "pdf":
        fileExtension = "pdf";
        break;
      case "excel":
        fileExtension = "xlsx";
        break;
      default:
        fileExtension = "bin";
    }
    let fileName = `tableau_view_${viewId}.${fileExtension}`, fileData;
    Buffer.isBuffer(downloadResponse.body) ? fileData = downloadResponse.body : typeof downloadResponse.body == "string" ? fileData = Buffer.from(downloadResponse.body) : fileData = Buffer.from(JSON.stringify(downloadResponse.body));
    let fileUrl = await files.write({
      fileName,
      data: fileData
    });
    return {
      success: !0,
      fileName,
      fileUrl,
      format,
      size: fileData.length
    };
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/actions/run-extract-refresh-task.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
function ensureString2(body) {
  return typeof body == "string" ? body : typeof body == "object" ? JSON.stringify(body) : String(body);
}
var runExtractRefreshTask = (0, import_pieces_framework3.createAction)({
  name: "run_extract_refresh_task",
  displayName: "Run Extract Refresh Task",
  description: "Runs the specified extract refresh task and returns job information",
  audience: "both",
  aiMetadata: { description: "Triggers an existing extract refresh task (by task ID) to run immediately, queuing a background job on the Tableau site and returning the new job's identifier. Use to force an on-demand refresh of a scheduled extract rather than waiting for its schedule. Not idempotent: each call enqueues a new refresh job.", idempotent: !1 },
  auth: tableauAuth,
  props: {
    taskId: import_pieces_framework3.Property.Dropdown({
      auth: tableauAuth,
      displayName: "Extract Refresh Task",
      description: "Select the extract refresh task to run",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please authenticate first"
          };
        try {
          return {
            disabled: !1,
            options: (await listExtractRefreshTasks(auth)).map((task) => {
              var _a;
              let label = `Task ${task.id}`;
              return task.workbookId ? label += ` (Workbook: ${task.workbookId})` : task.datasourceId && (label += ` (Datasource: ${task.datasourceId})`), (_a = task.schedule) != null && _a.frequency && (label += ` - ${task.schedule.frequency}`), {
                label,
                value: task.id
              };
            })
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load tasks. Please check your authentication."
          };
        }
      }
    })
  },
  async run({ auth, propsValue }) {
    let { taskId } = propsValue, tableauAuth2 = auth, { token: authToken, siteId } = await getAuthToken(tableauAuth2), apiVersion = tableauAuth2.apiVersion || "3.26", runTaskUrl = buildTableauUrl(tableauAuth2.serverUrl, apiVersion, siteId, `tasks/extractRefreshes/${taskId}/runNow`), response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.POST,
      url: runTaskUrl,
      headers: __spreadProps(__spreadValues({}, getTableauHeaders(authToken)), {
        "Content-Type": "application/xml"
      }),
      body: '<?xml version="1.0" encoding="UTF-8"?><tsRequest></tsRequest>'
    });
    if (response.status !== 200 && response.status !== 202)
      throw new Error(`Failed to run extract refresh task: ${response.status} - ${response.body}`);
    let responseBody = ensureString2(response.body), jobIdMatch = responseBody.match(/<job[^>]*id="([^"]+)"/), jobId = jobIdMatch ? jobIdMatch[1] : null, modeMatch = responseBody.match(/mode="([^"]+)"/), mode = modeMatch ? modeMatch[1] : null, typeMatch = responseBody.match(/type="([^"]+)"/), type = typeMatch ? typeMatch[1] : null;
    return {
      success: !0,
      taskId,
      job: {
        id: jobId,
        mode,
        type
      },
      message: "Extract refresh task has been queued successfully"
    };
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/actions/refresh-workbook.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
function ensureString3(body) {
  return typeof body == "string" ? body : typeof body == "object" ? JSON.stringify(body) : String(body);
}
var refreshWorkbook = (0, import_pieces_framework4.createAction)({
  name: "refresh_workbook",
  displayName: "Refresh Workbook",
  description: "Refreshes the specified workbook immediately",
  audience: "both",
  aiMetadata: { description: "Queues an immediate extract refresh for a workbook (by workbook ID), starting a background job on the Tableau site and returning its job identifier. Use to update a workbook's extracted data on demand. Not idempotent: each call enqueues a new refresh job.", idempotent: !1 },
  auth: tableauAuth,
  props: {
    workbookId: import_pieces_framework4.Property.Dropdown({
      auth: tableauAuth,
      displayName: "Workbook",
      description: "Select the workbook to refresh",
      required: !0,
      refreshers: ["auth"],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please authenticate first"
          };
        try {
          return {
            disabled: !1,
            options: (await queryWorkbooks(auth)).map((workbook) => ({
              label: workbook.name || `Workbook ${workbook.id}`,
              value: workbook.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load workbooks. Please check your authentication."
          };
        }
      }
    })
  },
  async run({ auth, propsValue }) {
    let { workbookId } = propsValue, tableauAuth2 = auth, { token: authToken, siteId } = await getAuthToken(tableauAuth2), apiVersion = tableauAuth2.apiVersion || "3.26", refreshUrl = buildTableauUrl(tableauAuth2.serverUrl, apiVersion, siteId, `workbooks/${workbookId}/refresh`), response = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: refreshUrl,
      headers: __spreadProps(__spreadValues({}, getTableauHeaders(authToken)), {
        "Content-Type": "application/xml"
      }),
      body: '<?xml version="1.0" encoding="UTF-8"?><tsRequest></tsRequest>'
    });
    if (response.status !== 202)
      throw new Error(`Failed to refresh workbook: ${response.status} - ${response.body}`);
    let responseBody = ensureString3(response.body), jobIdMatch = responseBody.match(/<job[^>]*id="([^"]+)"/), jobId = jobIdMatch ? jobIdMatch[1] : null, modeMatch = responseBody.match(/mode="([^"]+)"/), mode = modeMatch ? modeMatch[1] : null, typeMatch = responseBody.match(/type="([^"]+)"/), type = typeMatch ? typeMatch[1] : null, createdAtMatch = responseBody.match(/createdAt="([^"]+)"/), createdAt = createdAtMatch ? createdAtMatch[1] : null, workbookNameMatch = responseBody.match(/<workbook[^>]*name="([^"]+)"/), workbookName = workbookNameMatch ? workbookNameMatch[1] : null;
    return {
      success: !0,
      workbookId,
      workbookName,
      job: {
        id: jobId,
        mode,
        type,
        createdAt
      },
      message: "Workbook refresh has been queued successfully"
    };
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/actions/find-view.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
function ensureString4(body) {
  return typeof body == "string" ? body : typeof body == "object" ? JSON.stringify(body) : String(body);
}
var findView = (0, import_pieces_framework5.createAction)({
  name: "find_view",
  displayName: "Find View",
  description: "Finds a view based on name (URL name)",
  audience: "both",
  aiMetadata: { description: 'Looks up a single Tableau view by its URL name (the slug from /views/workbook/<name>, e.g. "Sheet1") and returns its metadata including the resolved view ID, owner, workbook, and project. Use to resolve a human-facing view name into the ID needed by other actions; errors if no matching view exists. Read-only and idempotent.', idempotent: !0 },
  auth: tableauAuth,
  props: {
    viewName: import_pieces_framework5.Property.ShortText({
      displayName: "View Name",
      description: "The URL name of the view to find (e.g., Sheet1 from /views/workbook/Sheet1)",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { viewName } = propsValue, tableauAuth2 = auth, { token: authToken, siteId } = await getAuthToken(tableauAuth2), apiVersion = tableauAuth2.apiVersion || "3.26", findViewUrl = buildTableauUrl(tableauAuth2.serverUrl, apiVersion, siteId, `views?filter=viewUrlName:eq:${encodeURIComponent(viewName)}`), response = await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: findViewUrl,
      headers: getTableauHeaders(authToken)
    });
    if (response.status !== 200)
      throw new Error(`Failed to find view: ${response.status} - ${response.body}`);
    let responseBody = ensureString4(response.body), viewIdMatch = responseBody.match(/<view[^>]*id="([^"]+)"/), viewId = viewIdMatch ? viewIdMatch[1] : null, nameMatch = responseBody.match(/name="([^"]+)"/), name = nameMatch ? nameMatch[1] : null, contentUrlMatch = responseBody.match(/contentUrl="([^"]+)"/), contentUrl = contentUrlMatch ? contentUrlMatch[1] : null, createdAtMatch = responseBody.match(/createdAt="([^"]+)"/), createdAt = createdAtMatch ? createdAtMatch[1] : null, updatedAtMatch = responseBody.match(/updatedAt="([^"]+)"/), updatedAt = updatedAtMatch ? updatedAtMatch[1] : null, viewUrlNameMatch = responseBody.match(/viewUrlName="([^"]+)"/), viewUrlName = viewUrlNameMatch ? viewUrlNameMatch[1] : null, workbookIdMatch = responseBody.match(/<workbook[^>]*id="([^"]+)"/), workbookId = workbookIdMatch ? workbookIdMatch[1] : null, ownerIdMatch = responseBody.match(/<owner[^>]*id="([^"]+)"/), ownerId = ownerIdMatch ? ownerIdMatch[1] : null, projectIdMatch = responseBody.match(/<project[^>]*id="([^"]+)"/), projectId = projectIdMatch ? projectIdMatch[1] : null;
    if (!viewId)
      throw new Error(`View with name "${viewName}" not found`);
    return {
      success: !0,
      view: {
        id: viewId,
        name,
        contentUrl,
        createdAt,
        updatedAt,
        viewUrlName,
        workbook: workbookId ? { id: workbookId } : null,
        owner: ownerId ? { id: ownerId } : null,
        project: projectId ? { id: projectId } : null,
        tags: []
      },
      message: `View "${viewName}" found successfully`
    };
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/actions/find-workbook.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
function ensureString5(body) {
  return typeof body == "string" ? body : typeof body == "object" ? JSON.stringify(body) : String(body);
}
var findWorkbook = (0, import_pieces_framework6.createAction)({
  name: "find_workbook",
  displayName: "Find Workbook",
  description: "Finds a workbook based on ID or content URL",
  audience: "both",
  aiMetadata: { description: "Looks up a single Tableau workbook and returns its full metadata (name, project, owner, views, timestamps). Search By selects the lookup mode: by workbook ID, or by content URL (the slug from /workbooks/<name>) \u2014 supply the field matching the chosen mode. Use to resolve a workbook reference into details or its ID; errors if not found. Read-only and idempotent.", idempotent: !0 },
  auth: tableauAuth,
  props: {
    searchType: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Search By",
      description: "Choose whether to search by workbook ID or content URL",
      required: !0,
      options: {
        options: [
          { label: "Workbook ID", value: "id" },
          { label: "Content URL", value: "contentUrl" }
        ]
      }
    }),
    workbookId: import_pieces_framework6.Property.ShortText({
      displayName: "Workbook ID",
      description: "The ID of the workbook to find",
      required: !1
    }),
    contentUrl: import_pieces_framework6.Property.ShortText({
      displayName: "Content URL",
      description: 'The content URL of the workbook to find (e.g., "MyWorkbook" from /workbooks/MyWorkbook)',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let { searchType, workbookId, contentUrl } = propsValue, tableauAuth2 = auth;
    if (searchType === "id" && !workbookId)
      throw new Error("Workbook ID is required when searching by ID");
    if (searchType === "contentUrl" && !contentUrl)
      throw new Error("Content URL is required when searching by content URL");
    let { token: authToken, siteId } = await getAuthToken(tableauAuth2), apiVersion = tableauAuth2.apiVersion || "3.26", findWorkbookUrl;
    searchType === "id" ? findWorkbookUrl = buildTableauUrl(tableauAuth2.serverUrl, apiVersion, siteId, `workbooks/${workbookId}`) : findWorkbookUrl = buildTableauUrl(tableauAuth2.serverUrl, apiVersion, siteId, `workbooks/${encodeURIComponent(contentUrl)}?key=contentUrl`);
    let response = await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: findWorkbookUrl,
      headers: getTableauHeaders(authToken)
    });
    if (response.status !== 200)
      throw new Error(`Failed to find workbook: ${response.status} - ${response.body}`);
    let responseBody = ensureString5(response.body), workbookIdMatch = responseBody.match(/<workbook[^>]*id="([^"]+)"/), foundWorkbookId = workbookIdMatch ? workbookIdMatch[1] : null, nameMatch = responseBody.match(/name="([^"]+)"/), name = nameMatch ? nameMatch[1] : null, descriptionMatch = responseBody.match(/description="([^"]*)"/), description = descriptionMatch ? descriptionMatch[1] : "", workbookContentUrlMatch = responseBody.match(/contentUrl="([^"]+)"/), workbookContentUrl = workbookContentUrlMatch ? workbookContentUrlMatch[1] : null, webpageUrlMatch = responseBody.match(/webpageUrl="([^"]+)"/), webpageUrl = webpageUrlMatch ? webpageUrlMatch[1] : null, showTabsMatch = responseBody.match(/showTabs="([^"]+)"/), showTabs = showTabsMatch ? showTabsMatch[1] : null, sizeMatch = responseBody.match(/size="([^"]+)"/), size = sizeMatch ? sizeMatch[1] : null, createdAtMatch = responseBody.match(/createdAt="([^"]+)"/), createdAt = createdAtMatch ? createdAtMatch[1] : null, updatedAtMatch = responseBody.match(/updatedAt="([^"]+)"/), updatedAt = updatedAtMatch ? updatedAtMatch[1] : null, encryptExtractsMatch = responseBody.match(/encryptExtracts="([^"]+)"/), encryptExtracts = encryptExtractsMatch ? encryptExtractsMatch[1] : null, defaultViewIdMatch = responseBody.match(/defaultViewId="([^"]+)"/), defaultViewId = defaultViewIdMatch ? defaultViewIdMatch[1] : null, projectIdMatch = responseBody.match(/<project[^>]*id="([^"]+)"/), projectId = projectIdMatch ? projectIdMatch[1] : null, projectNameMatch = responseBody.match(/<project[^>]*>[\s\S]*?<name[^>]*>([^<]+)<\/name>/), projectName = projectNameMatch ? projectNameMatch[1] : null, ownerIdMatch = responseBody.match(/<owner[^>]*id="([^"]+)"/), ownerId = ownerIdMatch ? ownerIdMatch[1] : null, ownerNameMatch = responseBody.match(/<owner[^>]*>[\s\S]*?<name[^>]*>([^<]+)<\/name>/), ownerName = ownerNameMatch ? ownerNameMatch[1] : null, views = [], viewMatches = responseBody.matchAll(/<view[^>]*id="([^"]+)"[^>]*name="([^"]+)"[^>]*contentUrl="([^"]+)"/g);
    for (let match of viewMatches) {
      let [, viewId, viewName, viewContentUrl] = match;
      views.push({
        id: viewId,
        name: viewName,
        contentUrl: viewContentUrl
      });
    }
    if (!foundWorkbookId) {
      let searchValue = searchType === "id" ? workbookId : contentUrl;
      throw new Error(`Workbook with ${searchType} "${searchValue}" not found`);
    }
    return {
      success: !0,
      workbook: {
        id: foundWorkbookId,
        name,
        description,
        contentUrl: workbookContentUrl,
        webpageUrl,
        showTabs: showTabs === "true",
        size: size ? parseInt(size) : null,
        createdAt,
        updatedAt,
        encryptExtracts: encryptExtracts === "true",
        defaultViewId,
        project: projectId ? { id: projectId, name: projectName } : null,
        owner: ownerId ? { id: ownerId, name: ownerName } : null,
        views,
        tags: []
      },
      searchType,
      searchValue: searchType === "id" ? workbookId : contentUrl,
      message: "Workbook found successfully"
    };
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/triggers/workbook-events.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var workbookEventTrigger = (0, import_pieces_framework7.createTrigger)({
  name: "workbook_event",
  displayName: "Workbook Event",
  description: "Triggers when a workbook event occurs (created, updated, or deleted)",
  aiMetadata: {
    description: "Fires when a workbook lifecycle event happens on the Tableau site \u2014 one of created, updated, or deleted, selected via the Event Type. Represents a single workbook change and carries the affected workbook's name and ID."
  },
  auth: tableauAuth,
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  props: {
    eventType: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Event Type",
      description: "Select the workbook event to trigger on",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Created", value: "WorkbookCreated" },
          { label: "Updated", value: "WorkbookUpdated" },
          { label: "Deleted", value: "WorkbookDeleted" }
        ]
      },
      defaultValue: "WorkbookCreated"
    })
  },
  sampleData: {
    resource: "WORKBOOK",
    event_type: "WorkbookCreated",
    resource_name: "Sample Workbook",
    site_luid: "site-uuid",
    resource_luid: "workbook-uuid",
    created_at: "2023-12-01T10:00:00Z"
  },
  async onEnable(context) {
    var _a;
    let eventType = context.propsValue.eventType, webhookData = {
      name: `ActivePieces_Workbook_${eventType}_${context.webhookUrl.substring(context.webhookUrl.lastIndexOf("/") + 1)}`,
      event: eventType,
      destinationUrl: context.webhookUrl
    }, webhookId = await createWebhook(context.auth, webhookData);
    await ((_a = context.store) == null ? void 0 : _a.put("webhook_id", webhookId));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("webhook_id"));
    webhookId && await deleteWebhook(context.auth, webhookId);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/triggers/workbook-refresh-events.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var workbookRefreshEventTrigger = (0, import_pieces_framework8.createTrigger)({
  name: "workbook_refresh_event",
  displayName: "Workbook Refresh Event",
  description: "Triggers when a workbook refresh event occurs (started, succeeded, or failed)",
  aiMetadata: {
    description: "Fires when a workbook extract refresh reaches a chosen stage on the Tableau site \u2014 refresh started, succeeded, or failed, selected via the Event Type. Use to react to the progress or outcome of a workbook refresh; the payload identifies the affected workbook."
  },
  auth: tableauAuth,
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  props: {
    eventType: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Event Type",
      description: "Select the workbook refresh event to trigger on",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Refresh Started", value: "WorkbookRefreshStarted" },
          { label: "Refresh Succeeded", value: "WorkbookRefreshSucceeded" },
          { label: "Refresh Failed", value: "WorkbookRefreshFailed" }
        ]
      },
      defaultValue: "WorkbookRefreshStarted"
    })
  },
  sampleData: {
    resource: "EXTRACTS",
    event_type: "WorkbookRefreshStarted",
    resource_name: "Sample Workbook",
    site_luid: "site-uuid",
    resource_luid: "workbook-uuid",
    created_at: "2023-12-01T10:00:00Z"
  },
  async onEnable(context) {
    var _a;
    let eventType = context.propsValue.eventType, webhookData = {
      name: `ActivePieces_Workbook_Refresh_${eventType}_${context.webhookUrl.substring(context.webhookUrl.lastIndexOf("/") + 1)}`,
      event: eventType,
      destinationUrl: context.webhookUrl
    }, webhookId = await createWebhook(context.auth, webhookData);
    await ((_a = context.store) == null ? void 0 : _a.put("webhook_id", webhookId));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("webhook_id"));
    webhookId && await deleteWebhook(context.auth, webhookId);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/triggers/datasource-events.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var datasourceEventTrigger = (0, import_pieces_framework9.createTrigger)({
  name: "datasource_event",
  displayName: "Datasource Event",
  description: "Triggers when a datasource event occurs (created, updated, or deleted)",
  aiMetadata: {
    description: "Fires when a datasource lifecycle event happens on the Tableau site \u2014 one of created, updated, or deleted, selected via the Event Type. Represents a single datasource change and carries the affected datasource's name and ID."
  },
  auth: tableauAuth,
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  props: {
    eventType: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Event Type",
      description: "Select the datasource event to trigger on",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Created", value: "DatasourceCreated" },
          { label: "Updated", value: "DatasourceUpdated" },
          { label: "Deleted", value: "DatasourceDeleted" }
        ]
      },
      defaultValue: "DatasourceCreated"
    })
  },
  sampleData: {
    resource: "DATASOURCE",
    event_type: "DatasourceCreated",
    resource_name: "Sample Datasource",
    site_luid: "site-uuid",
    resource_luid: "datasource-uuid",
    created_at: "2023-12-01T10:00:00Z"
  },
  async onEnable(context) {
    var _a;
    let eventType = context.propsValue.eventType, webhookData = {
      name: `ActivePieces_Datasource_${eventType}_${context.webhookUrl.substring(context.webhookUrl.lastIndexOf("/") + 1)}`,
      event: eventType,
      destinationUrl: context.webhookUrl
    }, webhookId = await createWebhook(context.auth, webhookData);
    await ((_a = context.store) == null ? void 0 : _a.put("webhook_id", webhookId));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("webhook_id"));
    webhookId && await deleteWebhook(context.auth, webhookId);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/triggers/datasource-refresh-events.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var datasourceRefreshEventTrigger = (0, import_pieces_framework10.createTrigger)({
  name: "datasource_refresh_event",
  displayName: "Datasource Refresh Event",
  description: "Triggers when a datasource refresh event occurs (started, succeeded, or failed)",
  aiMetadata: {
    description: "Fires when a datasource extract refresh reaches a chosen stage on the Tableau site \u2014 refresh started, succeeded, or failed, selected via the Event Type. Use to react to the progress or outcome of a datasource refresh; the payload identifies the affected datasource."
  },
  auth: tableauAuth,
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  props: {
    eventType: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Event Type",
      description: "Select the datasource refresh event to trigger on",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Refresh Started", value: "DatasourceRefreshStarted" },
          { label: "Refresh Succeeded", value: "DatasourceRefreshSucceeded" },
          { label: "Refresh Failed", value: "DatasourceRefreshFailed" }
        ]
      },
      defaultValue: "DatasourceRefreshStarted"
    })
  },
  sampleData: {
    resource: "EXTRACTS",
    event_type: "DatasourceRefreshStarted",
    resource_name: "Sample Datasource",
    site_luid: "site-uuid",
    resource_luid: "datasource-uuid",
    created_at: "2023-12-01T10:00:00Z"
  },
  async onEnable(context) {
    var _a;
    let eventType = context.propsValue.eventType, webhookData = {
      name: `ActivePieces_Datasource_Refresh_${eventType}_${context.webhookUrl.substring(context.webhookUrl.lastIndexOf("/") + 1)}`,
      event: eventType,
      destinationUrl: context.webhookUrl
    }, webhookId = await createWebhook(context.auth, webhookData);
    await ((_a = context.store) == null ? void 0 : _a.put("webhook_id", webhookId));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("webhook_id"));
    webhookId && await deleteWebhook(context.auth, webhookId);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/triggers/user-deleted.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var userDeletedTrigger = (0, import_pieces_framework11.createTrigger)({
  name: "user_deleted",
  displayName: "User Deleted",
  description: "Triggers when a user is deleted",
  aiMetadata: {
    description: "Fires when a user is removed from the Tableau site. Represents a single user-deletion event and carries the deleted user's name and ID."
  },
  auth: tableauAuth,
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    resource: "USER",
    event_type: "UserDeleted",
    resource_name: "Deleted User",
    site_luid: "site-uuid",
    resource_luid: "user-uuid",
    created_at: "2023-12-01T10:00:00Z"
  },
  async onEnable(context) {
    var _a;
    let webhookData = {
      name: `ActivePieces_User_Deleted_${context.webhookUrl.substring(context.webhookUrl.lastIndexOf("/") + 1)}`,
      event: "UserDeleted",
      destinationUrl: context.webhookUrl
    }, webhookId = await createWebhook(context.auth, webhookData);
    await ((_a = context.store) == null ? void 0 : _a.put("webhook_id", webhookId));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("webhook_id"));
    webhookId && await deleteWebhook(context.auth, webhookId);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/triggers/view-deleted.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var viewDeletedTrigger = (0, import_pieces_framework12.createTrigger)({
  name: "view_deleted",
  displayName: "View Deleted",
  description: "Triggers when a workbook view is deleted",
  aiMetadata: {
    description: "Fires when a view within a workbook is deleted on the Tableau site. Represents a single view-deletion event and carries the deleted view's name and ID."
  },
  auth: tableauAuth,
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    resource: "VIEW",
    event_type: "ViewDeleted",
    resource_name: "Deleted View",
    site_luid: "site-uuid",
    resource_luid: "view-uuid",
    created_at: "2023-12-01T10:00:00Z"
  },
  async onEnable(context) {
    var _a;
    let webhookData = {
      name: `ActivePieces_View_Deleted_${context.webhookUrl.substring(context.webhookUrl.lastIndexOf("/") + 1)}`,
      event: "ViewDeleted",
      destinationUrl: context.webhookUrl
    }, webhookId = await createWebhook(context.auth, webhookData);
    await ((_a = context.store) == null ? void 0 : _a.put("webhook_id", webhookId));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("webhook_id"));
    webhookId && await deleteWebhook(context.auth, webhookId);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/triggers/label-events.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var labelEventTrigger = (0, import_pieces_framework13.createTrigger)({
  name: "label_event",
  displayName: "Label Event",
  description: "Triggers when a label event occurs (created, updated, or deleted)",
  aiMetadata: {
    description: "Fires when a label (e.g. a data-quality or sensitivity label on a content asset) changes on the Tableau site \u2014 one of created, updated, or deleted, selected via the Event Type. Carries the affected asset's name and ID."
  },
  auth: tableauAuth,
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  props: {
    eventType: import_pieces_framework13.Property.StaticDropdown({
      displayName: "Event Type",
      description: "Select the label event to trigger on",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Created", value: "LabelCreated" },
          { label: "Updated", value: "LabelUpdated" },
          { label: "Deleted", value: "LabelDeleted" }
        ]
      },
      defaultValue: "LabelCreated"
    })
  },
  sampleData: {
    resource: "DATASOURCE",
    event_type: "LabelCreated",
    resource_name: "Sample Asset",
    site_luid: "site-uuid",
    resource_luid: "asset-uuid",
    created_at: "2023-12-01T10:00:00Z"
  },
  async onEnable(context) {
    var _a;
    let eventType = context.propsValue.eventType, webhookData = {
      name: `ActivePieces_Label_${eventType}_${context.webhookUrl.substring(context.webhookUrl.lastIndexOf("/") + 1)}`,
      event: eventType,
      destinationUrl: context.webhookUrl
    }, webhookId = await createWebhook(context.auth, webhookData);
    await ((_a = context.store) == null ? void 0 : _a.put("webhook_id", webhookId));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("webhook_id"));
    webhookId && await deleteWebhook(context.auth, webhookId);
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/tableau/src/lib/triggers/new-job.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var import_pieces_common8 = require("@activepieces/pieces-common");
function ensureString6(body) {
  return typeof body == "string" ? body : typeof body == "object" ? JSON.stringify(body) : String(body);
}
var polling = {
  strategy: import_pieces_common7.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let tableauAuth2 = auth.props, { token: authToken, siteId } = await getAuthToken(tableauAuth2), apiVersion = tableauAuth2.apiVersion || "3.26", queryJobsUrl = buildTableauUrl(tableauAuth2.serverUrl, apiVersion, siteId, "jobs"), filters = [];
    if (propsValue.jobType && filters.push(`jobType:eq:${propsValue.jobType}`), propsValue.status && filters.push(`status:eq:${propsValue.status}`), lastFetchEpochMS) {
      let isoDate = new Date(lastFetchEpochMS).toISOString();
      filters.push(`createdAt:gt:${isoDate}`);
    }
    filters.length > 0 && (queryJobsUrl += `?filter=${filters.join(",")}`);
    let response = await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.GET,
      url: queryJobsUrl,
      headers: getTableauHeaders(authToken)
    });
    if (response.status !== 200)
      throw new Error(`Failed to query jobs: ${response.status} - ${response.body}`);
    let responseBody = ensureString6(response.body), jobs = [], jobMatches = responseBody.matchAll(/<backgroundJob[^>]*id="([^"]+)"[^>]*status="([^"]*)"[^>]*createdAt="([^"]*)"[^>]*startedAt="([^"]*)"[^>]*endedAt="([^"]*)"[^>]*priority="([^"]*)"[^>]*jobType="([^"]*)"/g);
    for (let match of jobMatches) {
      let [, jobId, status, createdAt, startedAt, endedAt, priority, jobType] = match;
      jobs.push({
        id: jobId,
        status,
        createdAt,
        startedAt,
        endedAt,
        priority: parseInt(priority),
        jobType
      });
    }
    return jobs.map((job) => ({
      epochMilliSeconds: new Date(job.createdAt).getTime(),
      data: job
    }));
  }
}, newJobTrigger = (0, import_pieces_framework14.createTrigger)({
  name: "new_job",
  displayName: "New Job",
  description: "Triggers when a job matches the specified criteria",
  aiMetadata: {
    description: "Polls the Tableau site's background jobs and fires for each newly created job, optionally narrowed by Job Type (e.g. refresh extracts, run flow) and Status (e.g. Success, Failed, InProgress); leaving a filter empty matches all values for it. Use to watch for new or completed background jobs."
  },
  auth: tableauAuth,
  type: import_pieces_framework14.TriggerStrategy.POLLING,
  props: {
    jobType: import_pieces_framework14.Property.StaticDropdown({
      displayName: "Job Type",
      description: "Filter by job type (optional)",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "All Job Types", value: "" },
          { label: "Refresh Extracts", value: "refresh_extracts" },
          { label: "Increment Extracts", value: "increment_extracts" },
          { label: "Run Flow", value: "run_flow" },
          { label: "Refresh Workbook", value: "refresh_workbook" },
          { label: "Refresh Datasource", value: "refresh_datasource" }
        ]
      },
      defaultValue: ""
    }),
    status: import_pieces_framework14.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter by job status (optional)",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "All Statuses", value: "" },
          { label: "Success", value: "Success" },
          { label: "Failed", value: "Failed" },
          { label: "InProgress", value: "InProgress" },
          { label: "Pending", value: "Pending" },
          { label: "Cancelled", value: "Cancelled" }
        ]
      },
      defaultValue: ""
    })
  },
  sampleData: {
    id: "919055e5-25db-4a2b-9611-1408dd06632d",
    status: "Success",
    createdAt: "2023-12-01T10:00:00Z",
    startedAt: "2023-12-01T10:00:15Z",
    endedAt: "2023-12-01T10:01:00Z",
    priority: 50,
    jobType: "refresh_extracts"
  },
  onEnable: async (context) => {
    await import_pieces_common7.pollingHelper.onEnable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  onDisable: async (context) => {
    await import_pieces_common7.pollingHelper.onDisable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  run: async (context) => await import_pieces_common7.pollingHelper.poll(polling, context),
  test: async (context) => await import_pieces_common7.pollingHelper.test(polling, context)
});

// ../ap/packages/pieces/community/tableau/src/lib/triggers/list-extract-refresh-tasks.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var import_pieces_common10 = require("@activepieces/pieces-common");
function ensureString7(body) {
  return typeof body == "string" ? body : typeof body == "object" ? JSON.stringify(body) : String(body);
}
var polling2 = {
  strategy: import_pieces_common9.DedupeStrategy.LAST_ITEM,
  items: async ({ auth }) => {
    let tableauAuth2 = auth.props, { token: authToken, siteId } = await getAuthToken(tableauAuth2), apiVersion = tableauAuth2.apiVersion || "3.26", listTasksUrl = buildTableauUrl(tableauAuth2.serverUrl, apiVersion, siteId, "tasks/extractRefreshes"), response = await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: listTasksUrl,
      headers: getTableauHeaders(authToken)
    });
    if (response.status !== 200)
      throw new Error(`Failed to list extract refresh tasks: ${response.status} - ${response.body}`);
    let responseBody = ensureString7(response.body), tasks = [], taskMatches = responseBody.matchAll(/<extractRefresh[^>]*id="([^"]+)"[^>]*priority="([^"]*)"[^>]*consecutiveFailedCount="([^"]*)"[^>]*type="([^"]*)"/g);
    for (let match of taskMatches) {
      let [, taskId, priority, consecutiveFailedCount, type] = match, scheduleMatch = responseBody.match(new RegExp(`<extractRefresh[^>]*id="${taskId}"[^>]*>[\\s\\S]*?<schedule[^>]*id="([^"]*)"[^>]*name="([^"]*)"[^>]*state="([^"]*)"[^>]*priority="([^"]*)"[^>]*createdAt="([^"]*)"[^>]*updatedAt="([^"]*)"[^>]*type="([^"]*)"[^>]*frequency="([^"]*)"[^>]*nextRunAt="([^"]*)"`)), schedule = null;
      if (scheduleMatch) {
        let [, scheduleId, scheduleName, state, schedulePriority, createdAt, updatedAt, scheduleType, frequency, nextRunAt] = scheduleMatch;
        schedule = {
          id: scheduleId,
          name: scheduleName,
          state,
          priority: parseInt(schedulePriority),
          createdAt,
          updatedAt,
          type: scheduleType,
          frequency,
          nextRunAt
        };
      } else {
        let cloudScheduleMatch = responseBody.match(new RegExp(`<extractRefresh[^>]*id="${taskId}"[^>]*>[\\s\\S]*?<schedule[^>]*frequency="([^"]*)"[^>]*nextRunAt="([^"]*)"`));
        if (cloudScheduleMatch) {
          let [, frequency, nextRunAt] = cloudScheduleMatch;
          schedule = {
            frequency,
            nextRunAt
          };
          let frequencyDetailsMatch = responseBody.match(new RegExp(`<extractRefresh[^>]*id="${taskId}"[^>]*>[\\s\\S]*?<frequencyDetails[^>]*start="([^"]*)"`));
          if (frequencyDetailsMatch) {
            let [, start] = frequencyDetailsMatch;
            schedule.frequencyDetails = {
              start
            };
            let intervalMatches = [...responseBody.matchAll(new RegExp(`<extractRefresh[^>]*id="${taskId}"[^>]*>.*?<interval[^>]*>.*?<weekDay>([^<]+)</weekDay>`, "gs"))];
            intervalMatches.length > 0 && (schedule.frequencyDetails.intervals = intervalMatches.map((m) => ({ weekDay: m[1] })));
          }
        }
      }
      let workbookMatch = responseBody.match(new RegExp(`<extractRefresh[^>]*id="${taskId}"[^>]*>[\\s\\S]*?<workbook[^>]*id="([^"]+)"`)), datasourceMatch = responseBody.match(new RegExp(`<extractRefresh[^>]*id="${taskId}"[^>]*>[\\s\\S]*?<datasource[^>]*id="([^"]+)"`)), workbookId = workbookMatch ? workbookMatch[1] : null, datasourceId = datasourceMatch ? datasourceMatch[1] : null;
      tasks.push({
        id: taskId,
        priority: parseInt(priority),
        consecutiveFailedCount: parseInt(consecutiveFailedCount),
        type,
        schedule,
        workbookId,
        datasourceId
      });
    }
    return tasks.map((task) => ({
      id: task.id,
      data: task
    }));
  }
}, listExtractRefreshTasksTrigger = (0, import_pieces_framework15.createTrigger)({
  name: "list_extract_refresh_tasks",
  displayName: "List Extract Refresh Tasks",
  description: "Lists extract refresh tasks for the site",
  aiMetadata: {
    description: "Polls the configured extract refresh tasks on the Tableau site and fires for each newly seen task, surfacing its schedule and the workbook or datasource it targets. Use to detect when a new extract refresh task is added to the site."
  },
  auth: tableauAuth,
  type: import_pieces_framework15.TriggerStrategy.POLLING,
  props: {},
  sampleData: {
    id: "0ece2369-c4eb-4382-be0f-961039d708a0",
    priority: 50,
    consecutiveFailedCount: 5,
    type: "RefreshExtractTask",
    schedule: {
      frequency: "Weekly",
      nextRunAt: "2023-06-08T04:50:00Z",
      frequencyDetails: {
        start: "21:50:00",
        intervals: [
          { weekDay: "Thursday" }
        ]
      }
    },
    workbookId: "7e766949-7166-4b3d-90ba-784f7575743b",
    datasourceId: null
  },
  onEnable: async (context) => {
    await import_pieces_common9.pollingHelper.onEnable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  onDisable: async (context) => {
    await import_pieces_common9.pollingHelper.onDisable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  run: async (context) => await import_pieces_common9.pollingHelper.poll(polling2, context),
  test: async (context) => await import_pieces_common9.pollingHelper.test(polling2, context)
});

// ../ap/packages/pieces/community/tableau/src/index.ts
var tableau = (0, import_pieces_framework16.createPiece)({
  displayName: "Tableau",
  description: "Business intelligence and analytics platform for data visualization",
  auth: tableauAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/tableau.png",
  categories: [import_pieces_framework17.PieceCategory.BUSINESS_INTELLIGENCE],
  authors: ["onyedikachi-david"],
  actions: [downloadView, runExtractRefreshTask, refreshWorkbook, findView, findWorkbook],
  triggers: [
    workbookEventTrigger,
    workbookRefreshEventTrigger,
    datasourceEventTrigger,
    datasourceRefreshEventTrigger,
    userDeletedTrigger,
    viewDeletedTrigger,
    labelEventTrigger,
    newJobTrigger,
    listExtractRefreshTasksTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  tableau
});
