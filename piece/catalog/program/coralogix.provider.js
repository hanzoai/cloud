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

// ../ap/packages/pieces/community/coralogix/src/index.ts
var index_exports = {};
__export(index_exports, {
  coralogix: () => coralogix
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/coralogix/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), coralogixAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "Coralogix Credentials",
  description: "Management API uses api.coralogix.com. Select ingestion region domain and provide both API keys.",
  required: !0,
  props: {
    personalOrTeamApiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Personal/Team API Key",
      description: "Used for Coralogix management APIs (for example alert definitions, incidents, dashboards).",
      required: !0
    }),
    sendYourDataApiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Send-Your-Data API Key",
      description: "Used for Coralogix ingestion APIs (for example /logs/v1/singles). Find it in Settings \u2192 API Keys \u2192 Send Your Data.",
      required: !1
    }),
    coralogixDomain: import_pieces_framework.Property.StaticDropdown({
      displayName: "Ingestion Region Domain",
      description: "Select your Coralogix regional domain for ingestion endpoints.",
      required: !0,
      defaultValue: "eu1.coralogix.com",
      options: {
        options: [
          { label: "EU1 (<team>.coralogix.com)", value: "eu1.coralogix.com" },
          { label: "EU2 (<team>.app.eu2.coralogix.com)", value: "eu2.coralogix.com" },
          { label: "US1 (<team>.app.coralogix.us)", value: "us1.coralogix.com" },
          { label: "US2 (<team>.app.cx498.coralogix.com)", value: "us2.coralogix.com" },
          { label: "AP1 (<team>.app.coralogix.in)", value: "ap1.coralogix.com" },
          { label: "AP2 (<team>.app.coralogixsg.com)", value: "ap2.coralogix.com" },
          { label: "AP3 (<team>.app.ap3.coralogix.com)", value: "ap3.coralogix.com" }
        ]
      }
    })
  }
});

// ../ap/packages/pieces/community/coralogix/src/lib/actions/send-logs.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/coralogix/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), MANAGEMENT_DOMAIN = {
  "eu1.coralogix.com": "api.coralogix.com",
  "eu2.coralogix.com": "api.eu2.coralogix.com",
  "us1.coralogix.com": "api.coralogix.us",
  "us2.coralogix.com": "api.cx498.coralogix.com",
  "ap1.coralogix.com": "api.coralogix.in",
  "ap2.coralogix.com": "api.coralogixsg.com",
  "ap3.coralogix.com": "api.ap3.coralogix.com"
};
async function makeRequest(auth, apiType, method, path, body, queryParams) {
  var _a;
  let coralogixDomain = auth.props.coralogixDomain, domain = apiType === "management" ? (_a = MANAGEMENT_DOMAIN[coralogixDomain]) != null ? _a : "api.coralogix.com" : `ingress.${coralogixDomain}`, apiKey = apiType === "management" ? auth.props.personalOrTeamApiKey : auth.props.sendYourDataApiKey;
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `https://${domain}${path}`,
    headers: {
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json"
    },
    queryParams,
    body
  })).body;
}

// ../ap/packages/pieces/community/coralogix/src/lib/actions/send-logs.ts
var sendLogs = (0, import_pieces_framework2.createAction)({
  auth: coralogixAuth,
  name: "sendLogs",
  displayName: "Send Logs",
  description: "Send a single log to Coralogix Log Ingestion API.",
  audience: "both",
  aiMetadata: { description: "Ingests a single log record into Coralogix via the log-ingestion API, tagged with an application and subsystem name and an optional severity. Use to emit a custom log or event from a flow. Not idempotent: each call appends a new log entry.", idempotent: !1 },
  requireAuth: !0,
  props: {
    text: import_pieces_framework2.Property.LongText({
      displayName: "Log Message",
      description: "The log message text or a JSON string.",
      required: !0
    }),
    applicationName: import_pieces_framework2.Property.ShortText({
      displayName: "Application Name",
      description: "The application name for this log.",
      required: !0,
      defaultValue: "my-app"
    }),
    subsystemName: import_pieces_framework2.Property.ShortText({
      displayName: "Subsystem Name",
      description: "The subsystem name for this log.",
      required: !0,
      defaultValue: "auth-service"
    }),
    severity: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Severity",
      required: !1,
      options: {
        options: [
          { label: "Debug (1)", value: 1 },
          { label: "Verbose (2)", value: 2 },
          { label: "Info (3)", value: 3 },
          { label: "Warn (4)", value: 4 },
          { label: "Error (5)", value: 5 },
          { label: "Critical (6)", value: 6 }
        ]
      }
    }),
    category: import_pieces_framework2.Property.ShortText({
      displayName: "Category",
      required: !1
    }),
    className: import_pieces_framework2.Property.ShortText({
      displayName: "Class Name",
      required: !1
    }),
    computerName: import_pieces_framework2.Property.ShortText({
      displayName: "Computer Name",
      required: !1
    }),
    methodName: import_pieces_framework2.Property.ShortText({
      displayName: "Method Name",
      required: !1
    }),
    threadId: import_pieces_framework2.Property.ShortText({
      displayName: "Thread ID",
      required: !1
    }),
    timestamp: import_pieces_framework2.Property.Number({
      displayName: "Timestamp",
      description: "UTC milliseconds since 1970. Leave empty to use ingestion time.",
      required: !1
    }),
    hiResTimestamp: import_pieces_framework2.Property.ShortText({
      displayName: "High Resolution Timestamp",
      description: "UTC nanoseconds since 1970 as a string. Optional alternative to timestamp.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let {
      text,
      applicationName,
      subsystemName,
      severity,
      category,
      className,
      computerName,
      methodName,
      threadId,
      timestamp,
      hiResTimestamp
    } = propsValue, logRecord = {
      applicationName,
      subsystemName
    };
    return logRecord.text = text, severity != null && (logRecord.severity = Number(severity)), category && (logRecord.category = category), className && (logRecord.className = className), computerName && (logRecord.computerName = computerName), methodName && (logRecord.methodName = methodName), threadId && (logRecord.threadId = threadId), timestamp !== void 0 && (logRecord.timestamp = timestamp), hiResTimestamp && (logRecord.hiResTimestamp = hiResTimestamp), {
      success: !0,
      sent: 1,
      response: await makeRequest(
        auth,
        "ingestion",
        import_pieces_common2.HttpMethod.POST,
        "/logs/v1/singles",
        [logRecord]
      )
    };
  }
});

// ../ap/packages/pieces/community/coralogix/src/lib/actions/list-incidents.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var listIncidents = (0, import_pieces_framework3.createAction)({
  auth: coralogixAuth,
  name: "listIncidents",
  displayName: "List Incidents",
  description: "Retrieve a filtered list of Coralogix incidents.",
  audience: "both",
  aiMetadata: { description: "Lists Coralogix incidents, optionally filtered by status, severity, application, and subsystem; leave all filters empty to return every incident. Use to discover incidents and their IDs before acting on them, or to report on current incident state. Read-only and idempotent.", idempotent: !0 },
  requireAuth: !0,
  props: {
    status: import_pieces_framework3.Property.StaticMultiSelectDropdown({
      displayName: "Status",
      description: "Filter by incident status. Leave empty to return all.",
      required: !1,
      options: {
        options: [
          { label: "Triggered", value: "INCIDENT_STATUS_TRIGGERED" },
          { label: "Acknowledged", value: "INCIDENT_STATUS_ACKNOWLEDGED" },
          { label: "Resolved", value: "INCIDENT_STATUS_RESOLVED" }
        ]
      }
    }),
    severity: import_pieces_framework3.Property.StaticMultiSelectDropdown({
      displayName: "Severity",
      description: "Filter by incident severity. Leave empty to return all.",
      required: !1,
      options: {
        options: [
          { label: "Critical", value: "INCIDENT_SEVERITY_CRITICAL" },
          { label: "Error", value: "INCIDENT_SEVERITY_ERROR" },
          { label: "Warning", value: "INCIDENT_SEVERITY_WARNING" },
          { label: "Info", value: "INCIDENT_SEVERITY_INFO" },
          { label: "Low", value: "INCIDENT_SEVERITY_LOW" }
        ]
      }
    }),
    applicationName: import_pieces_framework3.Property.Array({
      displayName: "Application Names",
      description: "Filter by application name(s). Leave empty to return all.",
      required: !1,
      defaultValue: []
    }),
    subsystemName: import_pieces_framework3.Property.Array({
      displayName: "Subsystem Names",
      description: "Filter by subsystem name(s). Leave empty to return all.",
      required: !1,
      defaultValue: []
    }),
    pageSize: import_pieces_framework3.Property.Number({
      displayName: "Page Size",
      description: "Maximum number of incidents to return.",
      required: !1,
      defaultValue: 20
    })
  },
  async run({ auth, propsValue }) {
    let { status, severity, applicationName, subsystemName, pageSize } = propsValue, params = [];
    status && status.length > 0 && status.forEach(
      (s) => params.push(`filter.status=${encodeURIComponent(s)}`)
    ), severity && severity.length > 0 && severity.forEach(
      (s) => params.push(`filter.severity=${encodeURIComponent(s)}`)
    ), applicationName && applicationName.length > 0 && applicationName.forEach(
      (a) => params.push(`filter.applicationName=${encodeURIComponent(a)}`)
    ), subsystemName && subsystemName.length > 0 && subsystemName.forEach(
      (s) => params.push(`filter.subsystemName=${encodeURIComponent(s)}`)
    ), params.push(`pagination.pageSize=${pageSize != null ? pageSize : 20}`);
    let queryString = params.join("&");
    return await makeRequest(
      auth,
      "management",
      import_pieces_common3.HttpMethod.POST,
      `/mgmt/openapi/latest/incidents/incidents/v1?${queryString}`
    );
  }
});

// ../ap/packages/pieces/community/coralogix/src/lib/actions/acknowledge-incidents.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var acknowledgeIncidents = (0, import_pieces_framework4.createAction)({
  auth: coralogixAuth,
  name: "acknowledgeIncidents",
  displayName: "Acknowledge Incidents",
  description: "Acknowledge one or more Coralogix incidents by incident ID.",
  audience: "both",
  aiMetadata: { description: "Marks one or more Coralogix incidents as acknowledged, given a list of incident IDs. Use when an operator has seen an incident and is taking ownership. Idempotent: re-acknowledging already-acknowledged incidents leaves them in the same state.", idempotent: !0 },
  requireAuth: !0,
  props: {
    incidentIds: import_pieces_framework4.Property.Array({
      displayName: "Incident IDs",
      description: "List of incident IDs to acknowledge.",
      required: !0,
      defaultValue: []
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let endpoint = `/mgmt/openapi/latest/incidents/incidents/v1/acknowledge?${((_a = propsValue.incidentIds) != null ? _a : []).map((id) => `incident_ids=${encodeURIComponent(id)}`).join("&")}`;
    return await makeRequest(
      auth,
      "management",
      import_pieces_common4.HttpMethod.POST,
      endpoint
    );
  }
});

// ../ap/packages/pieces/community/coralogix/src/lib/actions/resolve-incidents.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var resolveIncidents = (0, import_pieces_framework5.createAction)({
  auth: coralogixAuth,
  name: "resolveIncidents",
  displayName: "Resolve Incidents",
  description: "Mark one or more Coralogix incidents as resolved.",
  audience: "both",
  aiMetadata: { description: "Marks one or more Coralogix incidents as resolved by incident ID; choose this for incidents that were actually fixed (use Close Incidents for false positives or noise). Idempotent: re-resolving already-resolved incidents yields the same state.", idempotent: !0 },
  requireAuth: !0,
  props: {
    incidentIds: import_pieces_framework5.Property.Array({
      displayName: "Incident IDs",
      description: "List of incident IDs to resolve. Find the ID in Coralogix \u2192 Alerts \u2192 Incidents \u2192 click an incident \u2192 copy the ID from the URL.",
      required: !0,
      defaultValue: []
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let queryString = ((_a = propsValue.incidentIds) != null ? _a : []).map((id) => `incident_ids=${encodeURIComponent(id)}`).join("&");
    return await makeRequest(
      auth,
      "management",
      import_pieces_common5.HttpMethod.POST,
      `/mgmt/openapi/latest/incidents/incidents/v1/resolve?${queryString}`
    );
  }
});

// ../ap/packages/pieces/community/coralogix/src/lib/actions/close-incidents.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var closeIncidents = (0, import_pieces_framework6.createAction)({
  auth: coralogixAuth,
  name: "closeIncidents",
  displayName: "Close Incidents",
  description: "Close one or more Coralogix incidents (e.g. false positives or noise). Use Resolve for incidents that were actually fixed.",
  audience: "both",
  aiMetadata: { description: "Closes one or more Coralogix incidents by incident ID; choose this for false positives or noise rather than Resolve Incidents (which is for incidents that were genuinely fixed). Idempotent: re-closing already-closed incidents yields the same state.", idempotent: !0 },
  requireAuth: !0,
  props: {
    incidentIds: import_pieces_framework6.Property.Array({
      displayName: "Incident IDs",
      description: "List of incident IDs to close. Find the ID in Coralogix \u2192 Alerts \u2192 Incidents \u2192 click an incident \u2192 copy the ID from the URL.",
      required: !0,
      defaultValue: []
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let queryString = ((_a = propsValue.incidentIds) != null ? _a : []).map((id) => `incident_ids=${encodeURIComponent(id)}`).join("&");
    return await makeRequest(
      auth,
      "management",
      import_pieces_common6.HttpMethod.POST,
      `/mgmt/openapi/latest/incidents/incidents/v1/close?${queryString}`
    );
  }
});

// ../ap/packages/pieces/community/coralogix/src/lib/actions/assign-incidents.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var assignIncidents = (0, import_pieces_framework7.createAction)({
  auth: coralogixAuth,
  name: "assignIncidents",
  displayName: "Assign Incidents",
  description: "Assign one or more Coralogix incidents to a specific user.",
  audience: "both",
  aiMetadata: { description: "Assigns one or more Coralogix incidents to a specific user, given a list of incident IDs and the target Coralogix user ID. Use to route incidents to an on-call owner. Idempotent: reassigning to the same user leaves ownership unchanged.", idempotent: !0 },
  requireAuth: !0,
  props: {
    incidentIds: import_pieces_framework7.Property.Array({
      displayName: "Incident IDs",
      description: 'List of incident IDs to assign. Find IDs via "List Incidents" or the Coralogix Incidents page URL.',
      required: !0,
      defaultValue: []
    }),
    assignToUserId: import_pieces_framework7.Property.ShortText({
      displayName: "Assign To (User ID)",
      description: "The Coralogix user ID to assign the incidents to.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let queryString = ((_a = propsValue.incidentIds) != null ? _a : []).map((id) => `incident_ids=${encodeURIComponent(id)}`).join("&");
    return await makeRequest(
      auth,
      "management",
      import_pieces_common7.HttpMethod.POST,
      `/mgmt/openapi/latest/incidents/incidents/v1/by-user?${queryString}`,
      { assigned_to: { user_id: propsValue.assignToUserId } }
    );
  }
});

// ../ap/packages/pieces/community/coralogix/src/lib/actions/get-incident-by-id.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var getIncidentById = (0, import_pieces_framework8.createAction)({
  auth: coralogixAuth,
  name: "getIncidentById",
  displayName: "Get Incident By ID",
  description: "Retrieve a Coralogix incident by its incident ID.",
  audience: "both",
  aiMetadata: { description: "Fetches a single Coralogix incident by its incident ID. Use to inspect full details of a known incident (e.g. one surfaced by List Incidents or a trigger). Read-only and idempotent.", idempotent: !0 },
  requireAuth: !0,
  props: {
    incidentId: import_pieces_framework8.Property.ShortText({
      displayName: "Incident ID",
      description: "The Coralogix incident ID to retrieve.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let incidentId = propsValue.incidentId;
    return await makeRequest(
      auth,
      "management",
      import_pieces_common8.HttpMethod.GET,
      `/mgmt/openapi/latest/incidents/incidents/v1/${encodeURIComponent(
        incidentId
      )}`
    );
  }
});

// ../ap/packages/pieces/community/coralogix/src/lib/actions/get-incident-events.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var getIncidentEvents = (0, import_pieces_framework9.createAction)({
  auth: coralogixAuth,
  name: "getIncidentEvents",
  displayName: "Get Incident Events",
  description: "Get related events for a specific incident.",
  audience: "both",
  aiMetadata: { description: "Retrieves the timeline of related events for a single Coralogix incident, given its incident ID. Use to trace how an incident evolved before deciding how to act on it. Read-only and idempotent.", idempotent: !0 },
  requireAuth: !0,
  props: {
    incidentId: import_pieces_framework9.Property.ShortText({
      displayName: "Incident ID",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let incidentId = propsValue.incidentId;
    return await makeRequest(
      auth,
      "management",
      import_pieces_common9.HttpMethod.GET,
      `/mgmt/openapi/latest/incidents/incidents/v1/${encodeURIComponent(
        incidentId
      )}/events`
    );
  }
});

// ../ap/packages/pieces/community/coralogix/src/lib/actions/set-alert-active.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var setAlertActive = (0, import_pieces_framework10.createAction)({
  auth: coralogixAuth,
  name: "setAlertActive",
  displayName: "Enable / Disable Alert",
  description: "Enable or disable a Coralogix alert definition. Use this to mute alerts during maintenance windows.",
  audience: "both",
  aiMetadata: { description: "Enables or disables a Coralogix alert definition by alert ID, controlled by an enable/disable flag. Use to mute an alert during a maintenance window and re-enable it afterward. Idempotent: setting the alert to a state it is already in has no further effect.", idempotent: !0 },
  requireAuth: !0,
  props: {
    alertId: import_pieces_framework10.Property.ShortText({
      displayName: "Alert ID",
      description: 'The ID of the alert to enable or disable. Use the "List Alert Definitions" action to find IDs.',
      required: !0
    }),
    active: import_pieces_framework10.Property.Checkbox({
      displayName: "Enable Alert",
      description: "Turn ON to enable the alert, turn OFF to disable it.",
      required: !0,
      defaultValue: !0
    })
  },
  async run({ auth, propsValue }) {
    let { alertId, active } = propsValue;
    return await makeRequest(
      auth,
      "management",
      import_pieces_common10.HttpMethod.POST,
      `/mgmt/openapi/latest/alerts/alerts-general/v3/${encodeURIComponent(alertId)}:setActive?active=${active}`
    );
  }
});

// ../ap/packages/pieces/community/coralogix/src/index.ts
var import_pieces_common12 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/coralogix/src/lib/triggers/new-alert-event.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var newAlertEvent = (0, import_pieces_framework11.createTrigger)({
  auth: coralogixAuth,
  name: "newAlertEvent",
  displayName: "New Alert Event",
  description: "Triggers when Coralogix sends an alert-event webhook payload to this flow.",
  aiMetadata: {
    description: "Fires when a configured Coralogix alert triggers and posts its outbound webhook to this flow, delivering the alert event (application, subsystem, severity, and matched fields). Requires the generic outbound webhook to be set up on the alert in Coralogix."
  },
  props: {
    setupInstructions: import_pieces_framework11.Property.MarkDown({
      value: `
### Webhook setup in Coralogix

1. In Coralogix, go to **Alerts** and open the alert definition you want.
2. Open **Outbound Webhooks** and click **Add webhook**.
3. Select **Generic webhook**.
4. In **Webhook URL**, paste: \`{{webhookUrl}}\`
5. Keep method as **POST** and content type as **application/json**.
6. Save the webhook, then trigger a test alert to verify events are received.
      `
    })
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  sampleData: {
    value: "0",
    source: "S3",
    schema: "nginx",
    metric: "",
    log: "",
    correlationId: "",
    category: "Security",
    applicationName: "Critical app",
    subsystemName: "payments",
    incidentIdentifier: "abc123",
    alertDescription: "check this out",
    timestamp: 1713175564848,
    tenantUrl: "https://api.coralogix.com",
    fields: [
      {
        key: "coralogix.metadata.timestamp",
        value: ["2024-04-15T07:26:04.848Z"]
      },
      {
        key: "coralogix.metadata.severity",
        value: [3]
      },
      {
        key: "ip",
        value: ["::1"]
      },
      {
        key: "log",
        value: [
          '127.0.0.1 - - [17/May/2015:08:05:12 +0000] "GET / HTTP/1.1" 200 612 "-" "curl/7.64.1"'
        ]
      },
      {
        key: "message",
        value: ["test"]
      },
      {
        key: "metric",
        value: [""]
      },
      {
        key: "region",
        value: ["us-central1"]
      },
      {
        key: "source",
        value: ["S3"]
      }
    ]
  },
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    let body = context.payload.body;
    return [body != null ? body : context.payload];
  }
});

// ../ap/packages/pieces/community/coralogix/src/lib/triggers/new-incident.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var STORE_KEY = "coralogix_seen_incident_ids", newIncident = (0, import_pieces_framework12.createTrigger)({
  auth: coralogixAuth,
  name: "newIncident",
  displayName: "New Incident",
  description: "Triggers when a new incident appears in Coralogix. Polls on a schedule \u2014 ideal for paging on-call, creating Jira tickets, or posting to Slack.",
  aiMetadata: {
    description: "Fires for each newly triggered Coralogix incident, discovered by polling on a schedule; can be narrowed to specific severities. Represents a freshly opened incident the flow has not seen before \u2014 use to drive paging, ticketing, or notifications."
  },
  type: import_pieces_framework12.TriggerStrategy.POLLING,
  props: {
    severity: import_pieces_framework12.Property.StaticMultiSelectDropdown({
      displayName: "Severity Filter",
      description: "Only trigger for incidents with these severities. Leave empty for all.",
      required: !1,
      options: {
        options: [
          { label: "Critical", value: "INCIDENT_SEVERITY_CRITICAL" },
          { label: "Error", value: "INCIDENT_SEVERITY_ERROR" },
          { label: "Warning", value: "INCIDENT_SEVERITY_WARNING" },
          { label: "Info", value: "INCIDENT_SEVERITY_INFO" },
          { label: "Low", value: "INCIDENT_SEVERITY_LOW" }
        ]
      }
    })
  },
  sampleData: {
    id: "incident-abc-123",
    name: "High error rate detected",
    status: "INCIDENT_STATUS_TRIGGERED",
    severity: "INCIDENT_SEVERITY_CRITICAL",
    createdAt: "2024-04-15T07:26:04.848Z",
    applicationName: ["my-app"],
    subsystemName: ["payments"]
  },
  async onEnable(context) {
    var _a;
    let ids = ((_a = (await makeRequest(
      context.auth,
      "management",
      import_pieces_common11.HttpMethod.POST,
      "/mgmt/openapi/latest/incidents/incidents/v1?filter.status=INCIDENT_STATUS_TRIGGERED&pagination.pageSize=100"
    )).incidents) != null ? _a : []).map((i) => i.id);
    await context.store.put(STORE_KEY, ids);
  },
  async onDisable() {
  },
  async run(context) {
    var _a, _b;
    let seenIds = (_a = await context.store.get(STORE_KEY)) != null ? _a : [], severityFilter = context.propsValue.severity, params = [
      "filter.status=INCIDENT_STATUS_TRIGGERED",
      "pagination.pageSize=100"
    ];
    severityFilter && severityFilter.length > 0 && severityFilter.forEach(
      (s) => params.push(`filter.severity=${encodeURIComponent(s)}`)
    );
    let queryString = params.join("&"), incidents = (_b = (await makeRequest(
      context.auth,
      "management",
      import_pieces_common11.HttpMethod.POST,
      `/mgmt/openapi/latest/incidents/incidents/v1?${queryString}`
    )).incidents) != null ? _b : [], newIncidents = incidents.filter((i) => !seenIds.includes(i.id)), allIds = incidents.map((i) => i.id), mergedIds = Array.from(/* @__PURE__ */ new Set([...seenIds, ...allIds]));
    return await context.store.put(STORE_KEY, mergedIds), newIncidents;
  }
});

// ../ap/packages/pieces/community/coralogix/src/index.ts
var coralogix = (0, import_pieces_framework13.createPiece)({
  displayName: "Coralogix",
  auth: coralogixAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/coralogix.png",
  authors: ["sanket-a11y"],
  actions: [
    sendLogs,
    listIncidents,
    acknowledgeIncidents,
    resolveIncidents,
    closeIncidents,
    assignIncidents,
    getIncidentById,
    getIncidentEvents,
    setAlertActive,
    (0, import_pieces_common12.createCustomApiCallAction)({
      auth: coralogixAuth,
      baseUrl: (auth) => {
        var _a, _b;
        return `https://${(_b = {
          "eu1.coralogix.com": "api.coralogix.com",
          "eu2.coralogix.com": "api.eu2.coralogix.com",
          "us1.coralogix.com": "api.coralogix.us",
          "us2.coralogix.com": "api.cx498.coralogix.com",
          "ap1.coralogix.com": "api.coralogix.in",
          "ap2.coralogix.com": "api.coralogixsg.com",
          "ap3.coralogix.com": "api.ap3.coralogix.com"
        }[(_a = auth == null ? void 0 : auth.props) == null ? void 0 : _a.coralogixDomain]) != null ? _b : "api.coralogix.com"}`;
      },
      authMapping: async (auth) => ({
        "Content-Type": "application/json",
        Authorization: `Bearer ${auth.props.personalOrTeamApiKey}`
      })
    })
  ],
  triggers: [newAlertEvent, newIncident]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  coralogix
});
