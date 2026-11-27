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

// ../ap/packages/pieces/community/pagerduty/src/index.ts
var index_exports = {};
__export(index_exports, {
  pagerduty: () => pagerduty
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pagerduty/src/lib/actions/acknowledge-incident.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pagerduty/src/lib/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pagerduty/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), PAGERDUTY_API_BASE_URL = "https://api.pagerduty.com", PAGERDUTY_ACCEPT_HEADER = "application/vnd.pagerduty+json;version=2";
function buildPagerDutyUrl(path, query) {
  let url = new URL(`${PAGERDUTY_API_BASE_URL}${path}`);
  if (query) {
    for (let [key, value] of Object.entries(query))
      if (!(value === void 0 || value === ""))
        if (Array.isArray(value))
          for (let item of value)
            url.searchParams.append(key, item);
        else
          url.searchParams.set(key, value);
  }
  return url.toString();
}
function pagerDutyHeaders(apiKey, fromEmail) {
  let headers = {
    Authorization: `Token token=${apiKey}`,
    Accept: PAGERDUTY_ACCEPT_HEADER,
    "Content-Type": "application/json"
  };
  return fromEmail && (headers.From = fromEmail), headers;
}
async function pagerDutyApiCall(params) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: params.method,
    url: buildPagerDutyUrl(params.path, params.query),
    headers: pagerDutyHeaders(params.apiKey, params.fromEmail),
    body: params.body
  })).body;
}

// ../ap/packages/pieces/community/pagerduty/src/lib/auth.ts
var pagerDutyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "PagerDuty API Key",
  description: "PagerDuty REST API token from Integrations > API Access Keys. Activepieces sends it as Authorization: Token token=<API_KEY>.",
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b;
    try {
      return await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `${PAGERDUTY_API_BASE_URL}/incidents`,
        queryParams: {
          limit: "1"
        },
        headers: pagerDutyHeaders(auth)
      }), {
        valid: !0
      };
    } catch (e) {
      let err = e;
      return ((_a = err.response) == null ? void 0 : _a.status) === 401 || ((_b = err.response) == null ? void 0 : _b.status) === 403 ? {
        valid: !1,
        error: "Invalid PagerDuty API key or insufficient permissions."
      } : {
        valid: !1,
        error: `Connection failed: ${String(e).slice(0, 100)}`
      };
    }
  }
});

// ../ap/packages/pieces/community/pagerduty/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var serviceIdProp = import_pieces_framework2.Property.Dropdown({
  auth: pagerDutyAuth,
  displayName: "Service",
  description: "The PagerDuty service to associate with the incident.",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your account first."
      };
    try {
      let services = [], offset = 0, limit = 100, more = !0;
      for (; more; ) {
        let data = await pagerDutyApiCall({
          apiKey: auth.secret_text,
          method: import_pieces_common3.HttpMethod.GET,
          path: "/services",
          query: { limit: String(limit), offset: String(offset) }
        });
        services.push(...data.services), more = data.more, offset += limit;
      }
      return {
        disabled: !1,
        options: services.map((s) => ({ label: s.name, value: s.id }))
      };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Error loading services. Check your API key."
      };
    }
  }
}), incidentIdProp = import_pieces_framework2.Property.ShortText({
  displayName: "Incident ID",
  description: "The PagerDuty incident ID, for example PABC123.",
  required: !0
}), fromEmailProp = import_pieces_framework2.Property.ShortText({
  displayName: "From Email",
  description: "PagerDuty REST write operations require the email address of a valid PagerDuty user on the account.",
  required: !0
}), urgencyProp = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Urgency",
  description: "The urgency for the incident.",
  required: !0,
  defaultValue: "high",
  options: {
    disabled: !1,
    options: [
      { label: "High", value: "high" },
      { label: "Low", value: "low" }
    ]
  }
}), optionalUrgencyProp = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Urgency",
  description: "Optional urgency filter.",
  required: !1,
  options: {
    disabled: !1,
    options: [
      { label: "High", value: "high" },
      { label: "Low", value: "low" }
    ]
  }
}), statusesProp = import_pieces_framework2.Property.StaticMultiSelectDropdown({
  displayName: "Statuses",
  description: "Optional incident status filters.",
  required: !1,
  options: {
    disabled: !1,
    options: [
      { label: "Triggered", value: "triggered" },
      { label: "Acknowledged", value: "acknowledged" },
      { label: "Resolved", value: "resolved" }
    ]
  }
}), instructionProp = import_pieces_framework2.Property.MarkDown({
  value: `To receive new incident events, create a webhook subscription in PagerDuty with the following settings:
  - Go to **Integrations** > **Generic Webhook(V3)** in the PagerDuty dashboard.
  - Set the **Endpoint URL** {{webhookUrl}} to the URL of this trigger.
  - Add Scope types and Scope
  - Select event type.
  - create webhook
`
});

// ../ap/packages/pieces/community/pagerduty/src/lib/actions/acknowledge-incident.ts
var acknowledgeIncident = (0, import_pieces_framework3.createAction)({
  auth: pagerDutyAuth,
  name: "acknowledge_incident",
  displayName: "Acknowledge Incident",
  description: "Acknowledge an existing PagerDuty incident.",
  audience: "both",
  aiMetadata: {
    description: "Sets an existing PagerDuty incident to the acknowledged status, requiring the incident ID and the acting user email (From). Use to mark that someone is working on an incident. Idempotent \u2014 re-acknowledging an already-acknowledged incident leaves it in the same state.",
    idempotent: !0
  },
  props: {
    incidentId: incidentIdProp,
    fromEmail: fromEmailProp
  },
  async run(context) {
    var _a;
    let { incidentId, fromEmail } = context.propsValue, response = await pagerDutyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.PUT,
      path: `/incidents/${encodeURIComponent(incidentId)}`,
      fromEmail,
      body: {
        incident: {
          type: "incident",
          status: "acknowledged"
        }
      }
    });
    return (_a = response.incident) != null ? _a : response;
  }
});

// ../ap/packages/pieces/community/pagerduty/src/lib/actions/create-incident.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var createIncident = (0, import_pieces_framework4.createAction)({
  auth: pagerDutyAuth,
  name: "create_incident",
  displayName: "Create Incident",
  description: "Create a new PagerDuty incident using the REST API.",
  audience: "both",
  aiMetadata: {
    description: "Opens a new incident on a PagerDuty service, requiring the requester email (From), the target service ID, and a title. Use to programmatically raise an alert/incident. Not idempotent \u2014 each call creates an incident, unless an Incident Key matching an already-open incident on the same service is supplied, in which case PagerDuty rejects the duplicate.",
    idempotent: !1
  },
  props: {
    fromEmail: fromEmailProp,
    serviceId: serviceIdProp,
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Title",
      description: "Incident title / summary.",
      required: !0
    }),
    urgency: urgencyProp,
    details: import_pieces_framework4.Property.LongText({
      displayName: "Details",
      description: "Additional incident body text.",
      required: !1
    }),
    incidentKey: import_pieces_framework4.Property.ShortText({
      displayName: "Incident Key",
      description: "A string that identifies the incident. Duplicate requests with the same key and open incident on the same service will be rejected (de-duplication).",
      required: !1
    }),
    assigneeIds: import_pieces_framework4.Property.Array({
      displayName: "Assignee IDs",
      description: "Comma-separated list of PagerDuty user IDs to assign the incident to. Cannot be used together with Escalation Policy ID.",
      required: !1
    }),
    priorityId: import_pieces_framework4.Property.ShortText({
      displayName: "Priority ID",
      description: "The ID of the priority to assign to this incident.",
      required: !1
    }),
    conferenceNumber: import_pieces_framework4.Property.ShortText({
      displayName: "Conference Number",
      description: "Phone number of the conference bridge.",
      required: !1
    }),
    conferenceUrl: import_pieces_framework4.Property.ShortText({
      displayName: "Conference URL",
      description: "URL for the conference bridge.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let {
      fromEmail,
      serviceId,
      title,
      urgency,
      details,
      incidentKey,
      assigneeIds,
      priorityId,
      conferenceNumber,
      conferenceUrl
    } = context.propsValue, incident = {
      type: "incident",
      title,
      service: {
        id: serviceId,
        type: "service_reference"
      },
      urgency
    };
    if (details && (incident.body = {
      type: "incident_body",
      details
    }), incidentKey && (incident.incident_key = incidentKey), assigneeIds && assigneeIds.length > 0 && (incident.assignments = assigneeIds.map((id) => ({
      assignee: { id, type: "user_reference" }
    }))), priorityId && (incident.priority = {
      id: priorityId,
      type: "priority_reference"
    }), conferenceNumber || conferenceUrl) {
      let bridge = {};
      conferenceNumber && (bridge.conference_number = conferenceNumber), conferenceUrl && (bridge.conference_url = conferenceUrl), incident.conference_bridge = bridge;
    }
    let response = await pagerDutyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/incidents",
      fromEmail,
      body: { incident }
    });
    return (_a = response.incident) != null ? _a : response;
  }
});

// ../ap/packages/pieces/community/pagerduty/src/lib/actions/get-incident.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var getIncident = (0, import_pieces_framework5.createAction)({
  auth: pagerDutyAuth,
  name: "get_incident",
  displayName: "Get Incident",
  description: "Retrieve a PagerDuty incident by ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single PagerDuty incident by its incident ID. Use when you already have the ID and need the full current details of one incident. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    incidentId: incidentIdProp
  },
  async run(context) {
    var _a;
    let response = await pagerDutyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.GET,
      path: `/incidents/${encodeURIComponent(context.propsValue.incidentId)}`
    });
    return (_a = response.incident) != null ? _a : response;
  }
});

// ../ap/packages/pieces/community/pagerduty/src/lib/actions/list-incidents.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var listIncidents = (0, import_pieces_framework6.createAction)({
  auth: pagerDutyAuth,
  name: "list_incidents",
  displayName: "List Incidents",
  description: "List PagerDuty incidents with optional filters and pagination.",
  audience: "both",
  aiMetadata: {
    description: "Lists PagerDuty incidents, optionally narrowed by status, urgency, and a since/until time window, with limit/offset paging (limit clamped to 1-100). Use to find or enumerate incidents; with no filters it returns the most recent incidents. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    statuses: statusesProp,
    urgency: optionalUrgencyProp,
    since: import_pieces_framework6.Property.DateTime({
      displayName: "Since",
      description: "Optional start of the search window.",
      required: !1
    }),
    until: import_pieces_framework6.Property.DateTime({
      displayName: "Until",
      description: "Optional end of the search window.",
      required: !1
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Maximum number of incidents to return (1\u2013100).",
      required: !1,
      defaultValue: 25
    }),
    offset: import_pieces_framework6.Property.Number({
      displayName: "Offset",
      description: "Pagination offset (number of records to skip).",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let { statuses, urgency, since, until, limit, offset } = context.propsValue, rawLimit = limit != null ? limit : 25, clampedLimit = Math.max(1, Math.min(100, rawLimit)), query = {
      since: since || void 0,
      until: until || void 0,
      limit: String(clampedLimit),
      offset: String(Math.max(0, offset != null ? offset : 0))
    };
    return statuses && statuses.length > 0 && (query["statuses[]"] = statuses), urgency && (query["urgencies[]"] = [urgency]), await pagerDutyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      path: "/incidents",
      query
    });
  }
});

// ../ap/packages/pieces/community/pagerduty/src/lib/actions/resolve-incident.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var resolveIncident = (0, import_pieces_framework7.createAction)({
  auth: pagerDutyAuth,
  name: "resolve_incident",
  displayName: "Resolve Incident",
  description: "Resolve an existing PagerDuty incident.",
  audience: "both",
  aiMetadata: {
    description: "Sets an existing PagerDuty incident to the resolved status, requiring the incident ID and the acting user email (From); an optional resolution note is added to the resolve log entry. Use to close out an incident. Idempotent on the status \u2014 re-resolving leaves the incident resolved.",
    idempotent: !0
  },
  props: {
    incidentId: incidentIdProp,
    fromEmail: fromEmailProp,
    resolution: import_pieces_framework7.Property.LongText({
      displayName: "Resolution",
      description: "Optional resolution note. PagerDuty adds this to the resolve log entry.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { incidentId, fromEmail, resolution } = context.propsValue, response = await pagerDutyApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.PUT,
      path: `/incidents/${encodeURIComponent(incidentId)}`,
      fromEmail,
      body: {
        incident: __spreadValues({
          type: "incident",
          status: "resolved"
        }, resolution ? { body: { type: "incident_body", details: resolution } } : {})
      }
    });
    return (_a = response.incident) != null ? _a : response;
  }
});

// ../ap/packages/pieces/community/pagerduty/src/lib/triggers/incident-acknowledged.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var incidentAcknowledged = (0, import_pieces_framework8.createTrigger)({
  auth: pagerDutyAuth,
  name: "incidentAcknowledged",
  displayName: "Incident Acknowledged",
  description: "Triggers when an incident is acknowledged.",
  aiMetadata: {
    description: "Fires when a PagerDuty incident is acknowledged, on the incident.acknowledged webhook event. Represents a responder taking ownership of an incident, carrying the incident details and the acknowledging agent."
  },
  props: {
    instruction: instructionProp
  },
  sampleData: {
    event: {
      id: "01AAAAAAAAAAAAAAAAAAAAAAAAA0",
      event_type: "incident.acknowledged",
      resource_type: "incident",
      occurred_at: "2024-01-01T00:30:00.000Z",
      agent: {
        id: "PABC123",
        type: "user_reference",
        self: "https://api.pagerduty.com/users/PABC123",
        html_url: "https://your-subdomain.pagerduty.com/users/PABC123",
        summary: "Jane Doe"
      },
      client: null,
      data: {
        id: "QABC1234567890",
        type: "incident",
        self: "https://api.pagerduty.com/incidents/QABC1234567890",
        html_url: "https://your-subdomain.pagerduty.com/incidents/QABC1234567890",
        number: 1,
        status: "acknowledged",
        incident_key: "abc123def456abc123def456abc12345",
        created_at: "2024-01-01T00:00:00Z",
        reopened_at: null,
        title: "High CPU usage on production server",
        service: {
          id: "PSVC123",
          type: "service_reference",
          self: "https://api.pagerduty.com/services/PSVC123",
          html_url: "https://your-subdomain.pagerduty.com/services/PSVC123",
          summary: "Production API"
        },
        assignees: [
          {
            id: "PABC123",
            type: "user_reference",
            self: "https://api.pagerduty.com/users/PABC123",
            html_url: "https://your-subdomain.pagerduty.com/users/PABC123",
            summary: "Jane Doe"
          }
        ],
        escalation_policy: {
          id: "PESC123",
          type: "escalation_policy_reference",
          self: "https://api.pagerduty.com/escalation_policies/PESC123",
          html_url: "https://your-subdomain.pagerduty.com/escalation_policies/PESC123",
          summary: "Default"
        },
        teams: [],
        priority: {
          id: "PPRI123",
          type: "priority_reference",
          self: "https://api.pagerduty.com/priorities/PPRI123",
          html_url: "https://your-subdomain.pagerduty.com/account/settings/incidents",
          summary: "P1"
        },
        urgency: "high",
        conference_bridge: null,
        resolve_reason: null,
        incident_type: { name: "incident_default" }
      }
    }
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(_context) {
  },
  async onDisable(_context) {
  },
  async run(context) {
    var _a;
    let payload = context.payload.body;
    return ((_a = payload == null ? void 0 : payload.event) == null ? void 0 : _a.event_type) !== "incident.acknowledged" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/pagerduty/src/lib/triggers/incident-resolved.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var incidentResolved = (0, import_pieces_framework9.createTrigger)({
  auth: pagerDutyAuth,
  name: "incidentResolved",
  displayName: "Incident Resolved",
  description: "Triggers when an incident is resolved.",
  aiMetadata: {
    description: "Fires when a PagerDuty incident is resolved (closed), on the incident.resolved webhook event. Represents an incident transitioning to the resolved state, carrying its final details."
  },
  props: { instruction: instructionProp },
  sampleData: {
    event: {
      id: "01AAAAAAAAAAAAAAAAAAAAAAAAA0",
      event_type: "incident.resolved",
      resource_type: "incident",
      occurred_at: "2024-01-01T01:00:00.000Z",
      agent: {
        id: "PABC123",
        type: "user_reference",
        self: "https://api.pagerduty.com/users/PABC123",
        html_url: "https://your-subdomain.pagerduty.com/users/PABC123",
        summary: "Jane Doe"
      },
      client: null,
      data: {
        id: "QABC1234567890",
        type: "incident",
        self: "https://api.pagerduty.com/incidents/QABC1234567890",
        html_url: "https://your-subdomain.pagerduty.com/incidents/QABC1234567890",
        number: 1,
        status: "resolved",
        incident_key: "abc123def456abc123def456abc12345",
        created_at: "2024-01-01T00:00:00Z",
        reopened_at: null,
        title: "High CPU usage on production server",
        service: {
          id: "PSVC123",
          type: "service_reference",
          self: "https://api.pagerduty.com/services/PSVC123",
          html_url: "https://your-subdomain.pagerduty.com/services/PSVC123",
          summary: "Production API"
        },
        assignees: [
          {
            id: "PABC123",
            type: "user_reference",
            self: "https://api.pagerduty.com/users/PABC123",
            html_url: "https://your-subdomain.pagerduty.com/users/PABC123",
            summary: "Jane Doe"
          }
        ],
        escalation_policy: {
          id: "PESC123",
          type: "escalation_policy_reference",
          self: "https://api.pagerduty.com/escalation_policies/PESC123",
          html_url: "https://your-subdomain.pagerduty.com/escalation_policies/PESC123",
          summary: "Default"
        },
        teams: [],
        priority: {
          id: "PPRI123",
          type: "priority_reference",
          self: "https://api.pagerduty.com/priorities/PPRI123",
          html_url: "https://your-subdomain.pagerduty.com/account/settings/incidents",
          summary: "P1"
        },
        urgency: "high",
        conference_bridge: null,
        resolve_reason: null,
        incident_type: { name: "incident_default" }
      }
    }
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(_context) {
  },
  async onDisable(_context) {
  },
  async run(context) {
    var _a;
    let payload = context.payload.body;
    return ((_a = payload == null ? void 0 : payload.event) == null ? void 0 : _a.event_type) !== "incident.resolved" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/pagerduty/src/lib/triggers/new-incident.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var newIncident = (0, import_pieces_framework10.createTrigger)({
  auth: pagerDutyAuth,
  name: "newIncident",
  displayName: "New Incident",
  description: "Triggers when a new incident is created",
  aiMetadata: {
    description: "Fires when a new incident is triggered (opened) in PagerDuty, on the incident.triggered webhook event. Represents a freshly raised incident, carrying its details, service, assignees, and urgency."
  },
  props: {
    instruction: instructionProp
  },
  sampleData: {
    event: {
      id: "01AAAAAAAAAAAAAAAAAAAAAAAAA0",
      event_type: "incident.triggered",
      resource_type: "incident",
      occurred_at: "2024-01-01T00:00:00.000Z",
      agent: {
        id: "PABC123",
        type: "user_reference",
        self: "https://api.pagerduty.com/users/PABC123",
        html_url: "https://your-subdomain.pagerduty.com/users/PABC123",
        summary: "Jane Doe"
      },
      client: null,
      data: {
        id: "QABC1234567890",
        type: "incident",
        self: "https://api.pagerduty.com/incidents/QABC1234567890",
        html_url: "https://your-subdomain.pagerduty.com/incidents/QABC1234567890",
        number: 1,
        status: "triggered",
        incident_key: "abc123def456abc123def456abc12345",
        created_at: "2024-01-01T00:00:00Z",
        reopened_at: null,
        title: "High CPU usage on production server",
        service: {
          id: "PSVC123",
          type: "service_reference",
          self: "https://api.pagerduty.com/services/PSVC123",
          html_url: "https://your-subdomain.pagerduty.com/services/PSVC123",
          summary: "Production API"
        },
        assignees: [
          {
            id: "PABC123",
            type: "user_reference",
            self: "https://api.pagerduty.com/users/PABC123",
            html_url: "https://your-subdomain.pagerduty.com/users/PABC123",
            summary: "Jane Doe"
          }
        ],
        escalation_policy: {
          id: "PESC123",
          type: "escalation_policy_reference",
          self: "https://api.pagerduty.com/escalation_policies/PESC123",
          html_url: "https://your-subdomain.pagerduty.com/escalation_policies/PESC123",
          summary: "Default"
        },
        teams: [],
        priority: {
          id: "PPRI123",
          type: "priority_reference",
          self: "https://api.pagerduty.com/priorities/PPRI123",
          html_url: "https://your-subdomain.pagerduty.com/account/settings/incidents",
          summary: "P1"
        },
        urgency: "high",
        conference_bridge: null,
        resolve_reason: null,
        incident_type: { name: "incident_default" }
      }
    }
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    var _a;
    let payload = context.payload.body;
    return ((_a = payload == null ? void 0 : payload.event) == null ? void 0 : _a.event_type) !== "incident.triggered" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/pagerduty/src/index.ts
var pagerduty = (0, import_pieces_framework11.createPiece)({
  displayName: "PagerDuty",
  description: "Incident management workflows for PagerDuty REST API v2.",
  auth: pagerDutyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/pagerduty.png",
  categories: [import_pieces_framework12.PieceCategory.DEVELOPER_TOOLS],
  authors: ["Harmatta", "sanket-a11y"],
  actions: [
    createIncident,
    listIncidents,
    getIncident,
    acknowledgeIncident,
    resolveIncident,
    (0, import_pieces_common9.createCustomApiCallAction)({
      auth: pagerDutyAuth,
      baseUrl: () => PAGERDUTY_API_BASE_URL,
      description: "Make a custom API call to PagerDuty REST API v2. Authorization, Accept, and Content-Type headers are injected automatically. For write endpoints that require a From header, add it manually in the headers field.",
      authMapping: async (auth) => pagerDutyHeaders(auth.secret_text)
    })
  ],
  triggers: [newIncident, incidentResolved, incidentAcknowledged]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pagerduty
});
