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

// ../ap/packages/pieces/community/uptimerobot/src/index.ts
var index_exports = {};
__export(index_exports, {
  uptimeRobot: () => uptimeRobot
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/uptimerobot/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/uptimerobot/src/lib/common/form.ts
function toFormUrlEncoded(obj) {
  return Object.entries(obj).filter(([, v]) => v != null).map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`).join("&");
}

// ../ap/packages/pieces/community/uptimerobot/src/lib/auth.ts
var uptimeRobotAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your API key:
1. Log in to your [UptimeRobot Dashboard](https://dashboard.uptimerobot.com/)
2. Click **Integrations & API** in the left sidebar
3. Scroll to the **API** section
4. Click **Show** next to your Main API Key
5. Copy the key and paste it here

The key starts with \`u\` followed by a long string of numbers and letters.

**Note:** Free plans are limited to 10 API requests per minute. Paid plans support up to 5,000 req/min.`,
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b;
    let { data: response, error } = await (0, import_pieces_framework2.tryCatch)(
      () => import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: "https://api.uptimerobot.com/v2/getAccountDetails",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: toFormUrlEncoded({ api_key: auth, format: "json" })
      })
    );
    return error ? {
      valid: !1,
      error: "Could not validate API key. Please check your connection."
    } : response.body.stat === "ok" ? { valid: !0 } : {
      valid: !1,
      error: (_b = (_a = response.body.error) == null ? void 0 : _a.message) != null ? _b : "Invalid API key"
    };
  }
});

// ../ap/packages/pieces/community/uptimerobot/src/lib/actions/get-monitors.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/uptimerobot/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var BASE_URL = "https://api.uptimerobot.com/v2", MAX_MONITORS_PER_PAGE = 50, MAX_DROPDOWN_MONITORS = 500, MONITOR_TYPE_MAP = {
  1: "HTTP",
  2: "Keyword",
  3: "Ping",
  4: "Port",
  5: "Heartbeat"
}, MONITOR_STATUS_MAP = {
  0: "Paused",
  1: "Not checked yet",
  2: "Up",
  8: "Seems down",
  9: "Down"
}, uptimeRobotCommon = {
  apiCall: callApi,
  fetchAllMonitors,
  fetchFlatMonitorById,
  flattenMonitor,
  buildMonitorOptions,
  monitorDropdown: import_pieces_framework3.Property.Dropdown({
    displayName: "Monitor",
    description: "Select the monitor to act on",
    refreshers: [],
    refreshOnSearch: !0,
    required: !0,
    auth: uptimeRobotAuth,
    options: async ({ auth }, { searchValue }) => buildMonitorOptions({ auth, searchValue: searchValue != null ? searchValue : void 0 })
  }),
  monitorDropdownOptional: import_pieces_framework3.Property.Dropdown({
    displayName: "Monitor (optional)",
    description: 'Select the monitor to act on, or leave blank and use "Monitor ID" below to pass a dynamic value.',
    refreshers: [],
    refreshOnSearch: !0,
    required: !1,
    auth: uptimeRobotAuth,
    options: async ({ auth }, { searchValue }) => buildMonitorOptions({ auth, searchValue: searchValue != null ? searchValue : void 0 })
  }),
  monitorMultiSelect: import_pieces_framework3.Property.MultiSelectDropdown({
    displayName: "Monitors",
    description: "Filter to specific monitors. Leave empty to include all monitors in your account.",
    refreshers: [],
    refreshOnSearch: !0,
    required: !1,
    auth: uptimeRobotAuth,
    options: async ({ auth }, { searchValue }) => buildMonitorOptions({ auth, searchValue: searchValue != null ? searchValue : void 0 })
  })
};
function statusLabel(status) {
  var _a;
  return (_a = MONITOR_STATUS_MAP[status]) != null ? _a : String(status);
}
function getHttpStatus(err) {
  if (typeof err != "object" || err === null || !("response" in err)) return;
  let { response } = err;
  if (typeof response != "object" || response === null || !("status" in response))
    return;
  let { status } = response;
  return typeof status == "number" ? status : void 0;
}
async function callApi({
  apiKey,
  endpoint,
  body
}) {
  var _a, _b;
  let payload = __spreadValues({ api_key: apiKey, format: "json" }, body), { data: response, error } = await (0, import_pieces_framework4.tryCatch)(
    () => import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${BASE_URL}/${endpoint}`,
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: toFormUrlEncoded(payload)
    })
  );
  if (error) {
    let status = getHttpStatus(error);
    throw status === 429 ? new Error(
      "UptimeRobot rate limit exceeded (Free plan: 10 req/min). Wait a moment and retry, or upgrade your plan."
    ) : status !== void 0 && status >= 500 ? new Error(
      `UptimeRobot is unavailable (HTTP ${status}). Please try again shortly.`
    ) : new Error(
      `UptimeRobot API request failed${status ? ` (HTTP ${status})` : ""}: ${endpoint}`
    );
  }
  if (response.body.stat === "fail")
    throw new Error((_b = (_a = response.body.error) == null ? void 0 : _a.message) != null ? _b : "UptimeRobot API request failed");
  return response.body;
}
async function fetchAllMonitors({
  apiKey,
  additionalBody,
  shouldStop
}) {
  let all = [], offset = 0;
  for (; ; ) {
    let data = await callApi({
      apiKey,
      endpoint: "getMonitors",
      body: __spreadValues({ limit: MAX_MONITORS_PER_PAGE, offset }, additionalBody)
    });
    if (all.push(...data.monitors), shouldStop && shouldStop(all) || (offset += MAX_MONITORS_PER_PAGE, data.monitors.length < MAX_MONITORS_PER_PAGE) || offset >= data.pagination.total) break;
  }
  return all;
}
function flattenMonitor({ monitor }) {
  var _a, _b;
  return {
    id: String(monitor.id),
    friendly_name: monitor.friendly_name,
    url: monitor.url,
    type: (_a = MONITOR_TYPE_MAP[monitor.type]) != null ? _a : String(monitor.type),
    status: statusLabel(monitor.status),
    interval_seconds: monitor.interval,
    uptime_ratio_30d: (_b = monitor.custom_uptime_ratio) != null ? _b : null,
    average_response_time_ms: monitor.average_response_time ? Number(monitor.average_response_time) : null
  };
}
async function fetchFlatMonitorById({
  apiKey,
  id
}) {
  let monitor = (await callApi({
    apiKey,
    endpoint: "getMonitors",
    body: {
      monitors: String(id),
      custom_uptime_ratios: "30",
      response_times: 1
    }
  })).monitors[0];
  return monitor ? flattenMonitor({ monitor }) : null;
}
async function buildMonitorOptions({
  auth,
  searchValue
}) {
  if (!auth)
    return {
      disabled: !0,
      options: [],
      placeholder: "Please connect your UptimeRobot account first"
    };
  try {
    let additionalBody = {};
    searchValue && (additionalBody.search = searchValue);
    let monitors = await fetchAllMonitors({
      apiKey: auth.secret_text,
      additionalBody,
      shouldStop: (all) => all.length >= MAX_DROPDOWN_MONITORS
    });
    if (monitors.length === 0)
      return {
        disabled: !1,
        options: [],
        placeholder: searchValue ? "No monitors match your search" : "No monitors found. Create one in UptimeRobot first."
      };
    let capped = monitors.slice(0, MAX_DROPDOWN_MONITORS), truncated = monitors.length >= MAX_DROPDOWN_MONITORS;
    return __spreadValues({
      disabled: !1,
      options: capped.map((m) => ({
        label: `${m.friendly_name} (${m.url}) \u2014 ${statusLabel(m.status)}`,
        value: String(m.id)
      }))
    }, truncated ? {
      placeholder: `Showing first ${MAX_DROPDOWN_MONITORS} monitors \u2014 type to search for more`
    } : {});
  } catch (e) {
    return {
      disabled: !0,
      options: [],
      placeholder: "Failed to load monitors. Check your API key."
    };
  }
}

// ../ap/packages/pieces/community/uptimerobot/src/lib/actions/get-monitors.ts
var getMonitorsAction = (0, import_pieces_framework5.createAction)({
  auth: uptimeRobotAuth,
  name: "get_monitors",
  displayName: "Get Monitors",
  description: "List and search your UptimeRobot monitors with optional filters. Leave the Monitors / Monitor IDs fields empty to return all monitors.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves UptimeRobot monitors, with two modes: leave the monitor ID fields empty to fetch all monitors, or pass specific monitor IDs (and optional type/status filters) to narrow the result. Use this to look up a monitor or check current up/down status before editing, pausing, or deleting one. Read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    monitor_ids: uptimeRobotCommon.monitorMultiSelect,
    monitor_ids_csv: import_pieces_framework5.Property.ShortText({
      displayName: "Or Enter Monitor IDs (comma-separated)",
      description: 'Use this to pass dynamic Monitor IDs from a previous step (e.g. "123, 456"). Merged with any selections above.',
      required: !1
    }),
    type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Monitor Type",
      description: "Only HTTP monitors are supported at the moment. Other UptimeRobot types (Keyword, Ping, Port, Heartbeat) will be added in a future release. Leave empty to return monitors of all types.",
      required: !1,
      options: {
        options: [{ label: "HTTP", value: 1 }]
      }
    }),
    status: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Status",
      description: "Only return monitors with this status. Leave empty for all statuses.",
      required: !1,
      options: {
        options: [
          { label: "Up", value: 2 },
          { label: "Down", value: 9 },
          { label: "Paused", value: 0 },
          { label: "Seems down", value: 8 },
          { label: "Not checked yet", value: 1 }
        ]
      }
    })
  },
  async run(context) {
    let { monitor_ids, monitor_ids_csv, type, status } = context.propsValue, additionalBody = {
      custom_uptime_ratios: "30",
      response_times: 1
    }, selectedIds = Array.isArray(monitor_ids) ? monitor_ids.filter((v) => typeof v == "string") : [], csvIds = typeof monitor_ids_csv == "string" ? monitor_ids_csv.split(",").map((s) => s.trim()).filter((s) => s.length > 0) : [], allIds = Array.from(/* @__PURE__ */ new Set([...selectedIds, ...csvIds]));
    return allIds.length > 0 && (additionalBody.monitors = allIds.join("-")), type != null && (additionalBody.types = String(type)), status != null && (additionalBody.statuses = String(status)), (await uptimeRobotCommon.fetchAllMonitors({
      apiKey: context.auth.secret_text,
      additionalBody
    })).map(
      (monitor) => uptimeRobotCommon.flattenMonitor({ monitor })
    );
  }
});

// ../ap/packages/pieces/community/uptimerobot/src/lib/actions/create-monitor.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var createMonitorAction = (0, import_pieces_framework6.createAction)({
  auth: uptimeRobotAuth,
  name: "create_monitor",
  displayName: "Create HTTP Monitor",
  description: "Create a new HTTP uptime monitor in UptimeRobot",
  audience: "both",
  aiMetadata: {
    description: "Creates a new HTTP uptime monitor in UptimeRobot for a given URL and friendly name. Use when an agent needs to start monitoring a new website or endpoint. Requires the full URL including protocol; the check interval has plan-dependent minimums (300s on Free). Each call creates a separate monitor, so it is not safe to repeat blindly.",
    idempotent: !1
  },
  props: {
    friendly_name: import_pieces_framework6.Property.ShortText({
      displayName: "Monitor Name",
      description: "A name for this monitor (e.g. 'Company Website' or 'API Server')",
      required: !0
    }),
    url: import_pieces_framework6.Property.ShortText({
      displayName: "URL",
      description: "The full URL to monitor, including the protocol (e.g. 'https://example.com').",
      required: !0
    }),
    interval: import_pieces_framework6.Property.Number({
      displayName: "Check Interval (seconds)",
      description: "How often UptimeRobot checks this URL, in seconds. Minimum 300s (5 min) on the Free plan, 60s on Solo/Team, 30s on Enterprise.",
      required: !1,
      defaultValue: 300
    })
  },
  async run(context) {
    let { friendly_name, url, interval } = context.propsValue, body = { friendly_name, url, type: 1 };
    interval != null && (body.interval = interval);
    let created = await uptimeRobotCommon.apiCall({
      apiKey: context.auth.secret_text,
      endpoint: "newMonitor",
      body
    }), flat = await uptimeRobotCommon.fetchFlatMonitorById({
      apiKey: context.auth.secret_text,
      id: created.monitor.id
    });
    if (!flat)
      throw new Error("Monitor was created but could not be retrieved.");
    return flat;
  }
});

// ../ap/packages/pieces/community/uptimerobot/src/lib/actions/edit-monitor.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var editMonitorAction = (0, import_pieces_framework7.createAction)({
  auth: uptimeRobotAuth,
  name: "edit_monitor",
  displayName: "Edit Monitor",
  description: "Update the settings of an existing UptimeRobot monitor",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing UptimeRobot monitor's name, monitored URL, and/or check interval. The target monitor can be chosen from the dropdown or, to drive it from a prior step, passed as a raw Monitor ID that overrides the dropdown. Only the fields you set are changed; omitted fields keep their current value. Repeating the same call leaves the monitor in the same state.",
    idempotent: !0
  },
  props: {
    monitor: uptimeRobotCommon.monitorDropdownOptional,
    monitor_id_override: import_pieces_framework7.Property.ShortText({
      displayName: "Or Enter Monitor ID",
      description: "Use this to pass a dynamic Monitor ID from a previous step (e.g. {{create_monitor.id}}). If set, this overrides the dropdown selection above.",
      required: !1
    }),
    friendly_name: import_pieces_framework7.Property.ShortText({
      displayName: "New Monitor Name",
      description: "Update the monitor's name. Leave empty to keep the current name.",
      required: !1
    }),
    url: import_pieces_framework7.Property.ShortText({
      displayName: "New URL or IP Address",
      description: "Update the URL or IP being monitored. Leave empty to keep the current value.",
      required: !1
    }),
    interval: import_pieces_framework7.Property.Number({
      displayName: "New Check Interval (seconds)",
      description: "Minimum 300s (5 min) on the Free plan, 60s on Solo/Team, 30s on Enterprise. Values below the plan minimum are rejected by UptimeRobot. Leave empty to keep the current interval.",
      required: !1
    })
  },
  async run(context) {
    let { monitor, monitor_id_override, friendly_name, url, interval } = context.propsValue, overrideId = typeof monitor_id_override == "string" ? monitor_id_override.trim() : "", monitorId = overrideId.length > 0 ? overrideId : monitor;
    if (!monitorId)
      throw new Error("Please select a monitor or enter a Monitor ID.");
    let body = { id: Number(monitorId) };
    friendly_name && (body.friendly_name = friendly_name), url && (body.url = url), interval != null && (body.interval = interval);
    let edited = await uptimeRobotCommon.apiCall({
      apiKey: context.auth.secret_text,
      endpoint: "editMonitor",
      body
    }), flat = await uptimeRobotCommon.fetchFlatMonitorById({
      apiKey: context.auth.secret_text,
      id: edited.monitor.id
    });
    if (!flat)
      throw new Error("Monitor was updated but could not be retrieved.");
    return flat;
  }
});

// ../ap/packages/pieces/community/uptimerobot/src/lib/actions/delete-monitor.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var deleteMonitorAction = (0, import_pieces_framework8.createAction)({
  auth: uptimeRobotAuth,
  name: "delete_monitor",
  displayName: "Delete Monitor",
  description: "Permanently delete a monitor from UptimeRobot. This cannot be undone.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a UptimeRobot monitor, identified either by the dropdown selection or by a raw Monitor ID passed from a prior step (the raw ID overrides the dropdown). This is irreversible and destructive, so confirm the target before calling; repeating it on an already-deleted monitor will fail.",
    idempotent: !1
  },
  props: {
    monitor: uptimeRobotCommon.monitorDropdownOptional,
    monitor_id_override: import_pieces_framework8.Property.ShortText({
      displayName: "Or Enter Monitor ID",
      description: "Use this to pass a dynamic Monitor ID from a previous step. If set, this overrides the dropdown selection above.",
      required: !1
    })
  },
  async run(context) {
    let { monitor, monitor_id_override } = context.propsValue, overrideId = typeof monitor_id_override == "string" ? monitor_id_override.trim() : "", monitorId = overrideId.length > 0 ? overrideId : monitor;
    if (!monitorId)
      throw new Error("Please select a monitor or enter a Monitor ID.");
    return await uptimeRobotCommon.apiCall({
      apiKey: context.auth.secret_text,
      endpoint: "deleteMonitor",
      body: { id: Number(monitorId) }
    }), { id: String(monitorId), deleted: !0 };
  }
});

// ../ap/packages/pieces/community/uptimerobot/src/lib/actions/pause-resume-monitor.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var pauseResumeMonitorAction = (0, import_pieces_framework9.createAction)({
  auth: uptimeRobotAuth,
  name: "pause_resume_monitor",
  displayName: "Pause or Resume Monitor",
  description: "Temporarily pause or resume monitoring for a specific monitor.",
  audience: "both",
  aiMetadata: {
    description: "Pauses or resumes monitoring for a single UptimeRobot monitor; the Action prop selects which (pause stops all checks and alerts, resume restarts them). The target is chosen from the dropdown or by a raw Monitor ID passed from a prior step, which overrides the dropdown. Sets a fixed paused/active state, so repeating the same call leaves the monitor in the same state.",
    idempotent: !0
  },
  props: {
    monitor: uptimeRobotCommon.monitorDropdownOptional,
    monitor_id_override: import_pieces_framework9.Property.ShortText({
      displayName: "Or Enter Monitor ID",
      description: "Use this to pass a dynamic Monitor ID from a previous step. If set, this overrides the dropdown selection above.",
      required: !1
    }),
    action: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Action",
      description: "Pause stops all checks and alerts. Resume restarts monitoring.",
      required: !0,
      options: {
        options: [
          { label: "Pause", value: 0 },
          { label: "Resume", value: 1 }
        ]
      }
    })
  },
  async run(context) {
    let { monitor, monitor_id_override } = context.propsValue, overrideId = typeof monitor_id_override == "string" ? monitor_id_override.trim() : "", monitorId = overrideId.length > 0 ? overrideId : monitor;
    if (!monitorId)
      throw new Error("Please select a monitor or enter a Monitor ID.");
    let newStatus = Number(context.propsValue.action);
    await uptimeRobotCommon.apiCall({
      apiKey: context.auth.secret_text,
      endpoint: "editMonitor",
      body: { id: Number(monitorId), status: newStatus }
    });
    let flat = await uptimeRobotCommon.fetchFlatMonitorById({
      apiKey: context.auth.secret_text,
      id: monitorId
    });
    if (!flat)
      throw new Error("Monitor status changed but could not be retrieved.");
    return __spreadProps(__spreadValues({}, flat), {
      action_performed: newStatus === 0 ? "paused" : "resumed"
    });
  }
});

// ../ap/packages/pieces/community/uptimerobot/src/index.ts
var uptimeRobot = (0, import_pieces_framework10.createPiece)({
  displayName: "UptimeRobot",
  description: "Monitor your websites, APIs, and servers. Get alerted when things go down.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/uptimerobot.png",
  categories: [import_pieces_framework11.PieceCategory.DEVELOPER_TOOLS],
  auth: uptimeRobotAuth,
  authors: ["majewskibartosz"],
  actions: [
    getMonitorsAction,
    createMonitorAction,
    editMonitorAction,
    deleteMonitorAction,
    pauseResumeMonitorAction
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  uptimeRobot
});
