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

// ../ap/packages/pieces/community/fathom-analytics/src/index.ts
var index_exports = {};
__export(index_exports, {
  fathomAnalytics: () => fathomAnalytics
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/fathom-analytics/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), FATHOM_API_BASE = "https://api.usefathom.com/v1", fathomAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: `
To create an API token:
1. Go to [https://app.usefathom.com/api](https://app.usefathom.com/api)
2. Click **Create new**
3. Give your token a name and set permissions (Admin, Read Only, or site-specific)
4. Click **Save changes**
5. Copy your API token \u2014 you won't be able to retrieve it again after navigating away
  `,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${FATHOM_API_BASE}/account`,
        headers: {
          Authorization: `Bearer ${auth}`
        }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API token. Please check your Fathom API credentials."
      };
    }
  }
});

// ../ap/packages/pieces/community/fathom-analytics/src/lib/actions/list-sites.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var listSites = (0, import_pieces_framework2.createAction)({
  name: "list_sites",
  displayName: "List Sites",
  description: "Return a list of all sites this API key has access to.",
  audience: "both",
  aiMetadata: { description: "List all Fathom Analytics sites the API key can access, with optional cursor pagination (starting_after / ending_before) and a per-page limit. Use it to discover available site IDs before calling site-, event-, or aggregation-scoped actions. Read-only and safe to repeat.", idempotent: !0 },
  auth: fathomAuth,
  props: {
    limit: import_pieces_framework2.Property.Number({
      displayName: "Limit",
      description: "Number of sites to return (1\u2013100). Default is 10.",
      required: !1,
      defaultValue: 10
    }),
    starting_after: import_pieces_framework2.Property.ShortText({
      displayName: "Starting After (Cursor)",
      description: "Cursor for forward pagination \u2014 the last site ID from the previous page.",
      required: !1
    }),
    ending_before: import_pieces_framework2.Property.ShortText({
      displayName: "Ending Before (Cursor)",
      description: "Cursor for backward pagination \u2014 the first site ID from the previous page.",
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue } = context, params = {};
    return propsValue.limit != null && (params.limit = String(propsValue.limit)), propsValue.starting_after && (params.starting_after = propsValue.starting_after), propsValue.ending_before && (params.ending_before = propsValue.ending_before), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `${FATHOM_API_BASE}/sites`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: params
    })).body;
  }
});

// ../ap/packages/pieces/community/fathom-analytics/src/lib/actions/get-site.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getSite = (0, import_pieces_framework3.createAction)({
  name: "get_site",
  displayName: "Get Site",
  description: "Retrieve details for a specific site by its ID.",
  audience: "both",
  aiMetadata: { description: "Fetch the details of a single Fathom Analytics site by its site ID (e.g. CDBUGS). Use it when you already know the target site ID and need its metadata; call List Sites first if you only have a name. Read-only and safe to repeat.", idempotent: !0 },
  auth: fathomAuth,
  props: {
    site_id: import_pieces_framework3.Property.ShortText({
      displayName: "Site ID",
      description: "The unique identifier for the Fathom site (e.g., CDBUGS).",
      required: !0
    })
  },
  async run(context) {
    let { auth, propsValue } = context;
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `${FATHOM_API_BASE}/sites/${propsValue.site_id}`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/fathom-analytics/src/lib/actions/create-event.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createEvent = (0, import_pieces_framework4.createAction)({
  name: "create_event",
  displayName: "Create Event",
  description: "Create a custom event (goal) for a specific site.",
  audience: "both",
  aiMetadata: { description: "Create a new custom event (conversion goal) under a Fathom Analytics site, identified by site ID and a goal name. Use it to set up a goal before tracking conversions against it. Not idempotent \u2014 each call creates another event, so repeated calls with the same name can produce duplicates.", idempotent: !1 },
  auth: fathomAuth,
  props: {
    site_id: import_pieces_framework4.Property.ShortText({
      displayName: "Site ID",
      description: "The unique identifier for the Fathom site.",
      required: !0
    }),
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Event Name",
      description: 'The name of the event/goal (e.g., "Signup", "Purchase").',
      required: !0
    })
  },
  async run(context) {
    let { auth, propsValue } = context;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${FATHOM_API_BASE}/sites/${propsValue.site_id}/events`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: {
        name: propsValue.name
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/fathom-analytics/src/lib/actions/list-events.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var listEvents = (0, import_pieces_framework5.createAction)({
  name: "list_events",
  displayName: "List Events",
  description: "List all custom events (goals) for a specific site.",
  audience: "both",
  aiMetadata: { description: "List the custom events (conversion goals) defined on a Fathom Analytics site, identified by site ID, with optional cursor pagination and a per-page limit. Use it to discover event IDs and names before aggregating event metrics or creating new goals. Read-only and safe to repeat.", idempotent: !0 },
  auth: fathomAuth,
  props: {
    site_id: import_pieces_framework5.Property.ShortText({
      displayName: "Site ID",
      description: "The unique identifier for the Fathom site.",
      required: !0
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "Number of events to return (1\u2013100). Default is 10.",
      required: !1,
      defaultValue: 10
    }),
    starting_after: import_pieces_framework5.Property.ShortText({
      displayName: "Starting After (Cursor)",
      description: "Cursor for forward pagination \u2014 the last event ID from the previous page.",
      required: !1
    }),
    ending_before: import_pieces_framework5.Property.ShortText({
      displayName: "Ending Before (Cursor)",
      description: "Cursor for backward pagination \u2014 the first event ID from the previous page.",
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue } = context, params = {};
    return propsValue.limit != null && (params.limit = String(propsValue.limit)), propsValue.starting_after && (params.starting_after = propsValue.starting_after), propsValue.ending_before && (params.ending_before = propsValue.ending_before), (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `${FATHOM_API_BASE}/sites/${propsValue.site_id}/events`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams: params
    })).body;
  }
});

// ../ap/packages/pieces/community/fathom-analytics/src/lib/actions/get-aggregation.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/fathom-analytics/src/lib/common.ts
var ENTITY_OPTIONS = [
  { label: "Pageview", value: "pageview" },
  { label: "Event", value: "event" }
], AGGREGATION_OPTIONS = [
  { label: "Pageviews", value: "pageviews" },
  { label: "Visits", value: "visits" },
  { label: "Uniques", value: "uniques" },
  { label: "Avg Duration", value: "avg_duration" },
  { label: "Bounce Rate", value: "bounce_rate" }
], DATE_GROUPING_OPTIONS = [
  { label: "Hour", value: "hour" },
  { label: "Day", value: "day" },
  { label: "Month", value: "month" },
  { label: "Year", value: "year" }
], FIELD_GROUPING_OPTIONS = [
  { label: "Pathname", value: "pathname" },
  { label: "Hostname", value: "hostname" },
  { label: "Referrer Hostname", value: "referrer_hostname" },
  { label: "Referrer Pathname", value: "referrer_pathname" },
  { label: "Browser", value: "browser" },
  { label: "Browser Version", value: "browser_version" },
  { label: "Device Type", value: "device_type" },
  { label: "Country Code", value: "country_code" },
  { label: "City", value: "city" },
  { label: "UTM Source", value: "utm_source" },
  { label: "UTM Medium", value: "utm_medium" },
  { label: "UTM Campaign", value: "utm_campaign" },
  { label: "UTM Term", value: "utm_term" },
  { label: "UTM Content", value: "utm_content" }
];

// ../ap/packages/pieces/community/fathom-analytics/src/lib/actions/get-aggregation.ts
var getAggregation = (0, import_pieces_framework6.createAction)({
  name: "get_aggregation",
  displayName: "Get Aggregation",
  description: "Generate a custom analytics aggregation report. Supports flexible grouping, filtering, and date ranges.",
  audience: "both",
  aiMetadata: { description: 'Run a custom analytics aggregation report over a Fathom Analytics site, returning chosen metrics with optional date-range, date/field grouping, filters, sorting, and limit. Operates in two modes set by entity type: "pageview" aggregates page traffic (requires the site ID in entity_id), while "event" aggregates a specific conversion goal (requires both site_id and the event ID in entity_name). Use it to answer traffic or conversion questions; read-only and safe to repeat.', idempotent: !0 },
  auth: fathomAuth,
  props: {
    entity: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Entity Type",
      description: "Whether to aggregate pageviews or events.",
      required: !0,
      options: {
        options: ENTITY_OPTIONS
      },
      defaultValue: "pageview"
    }),
    entity_id: import_pieces_framework6.Property.ShortText({
      displayName: "Site ID (for Pageviews)",
      description: 'The Fathom site ID to query (e.g., CDBUGS). Required when Entity Type is "Pageview".',
      required: !1
    }),
    site_id: import_pieces_framework6.Property.ShortText({
      displayName: "Site ID (for Events)",
      description: 'The Fathom site ID that owns the event. Required when Entity Type is "Event".',
      required: !1
    }),
    entity_name: import_pieces_framework6.Property.ShortText({
      displayName: "Event ID",
      description: 'The ID of the specific event to aggregate. Required when Entity Type is "Event". Find event IDs via the List Events action.',
      required: !1
    }),
    aggregates: import_pieces_framework6.Property.StaticMultiSelectDropdown({
      displayName: "Aggregates",
      description: "The metrics to include in the report.",
      required: !0,
      options: {
        options: AGGREGATION_OPTIONS
      }
    }),
    date_grouping: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Date Grouping",
      description: "Group results by this time unit.",
      required: !1,
      options: {
        options: DATE_GROUPING_OPTIONS
      }
    }),
    field_grouping: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Field Grouping",
      description: "Group results by this field (e.g., pathname, browser). Makes the report actionable by breaking down totals.",
      required: !1,
      options: {
        options: FIELD_GROUPING_OPTIONS
      }
    }),
    date_from: import_pieces_framework6.Property.ShortText({
      displayName: "Date From",
      description: "Start date for the report in ISO 8601 format (e.g., 2024-01-01T00:00:00.000000Z).",
      required: !1
    }),
    date_to: import_pieces_framework6.Property.ShortText({
      displayName: "Date To",
      description: "End date for the report in ISO 8601 format (e.g., 2024-12-31T23:59:59.999999Z).",
      required: !1
    }),
    timezone: import_pieces_framework6.Property.ShortText({
      displayName: "Timezone",
      description: 'Timezone for the report (e.g., "America/New_York", "Europe/London"). Defaults to UTC.',
      required: !1
    }),
    sort_by: import_pieces_framework6.Property.ShortText({
      displayName: "Sort By",
      description: 'Field and direction to sort by (e.g., "pageviews:desc" or "uniques:asc").',
      required: !1
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Maximum number of results to return.",
      required: !1,
      defaultValue: 20
    }),
    filters: import_pieces_framework6.Property.Json({
      displayName: "Filters",
      description: 'Optional array of filter objects. Example: [{"property": "pathname", "operator": "is", "value": "/blog"}]',
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue } = context, queryParams = {
      entity: propsValue.entity,
      aggregates: Array.isArray(propsValue.aggregates) ? propsValue.aggregates.join(",") : String(propsValue.aggregates)
    };
    return propsValue.entity === "event" ? (propsValue.site_id && (queryParams.site_id = propsValue.site_id), propsValue.entity_name && (queryParams.entity_name = propsValue.entity_name)) : propsValue.entity_id && (queryParams.entity_id = propsValue.entity_id), propsValue.date_grouping && (queryParams.date_grouping = propsValue.date_grouping), propsValue.field_grouping && (queryParams.field_grouping = propsValue.field_grouping), propsValue.date_from && (queryParams.date_from = propsValue.date_from), propsValue.date_to && (queryParams.date_to = propsValue.date_to), propsValue.timezone && (queryParams.timezone = propsValue.timezone), propsValue.sort_by && (queryParams.sort_by = propsValue.sort_by), propsValue.limit != null && (queryParams.limit = String(propsValue.limit)), propsValue.filters && (queryParams.filters = JSON.stringify(propsValue.filters)), (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: `${FATHOM_API_BASE}/aggregations`,
      headers: {
        Authorization: `Bearer ${auth.secret_text}`
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/fathom-analytics/src/index.ts
var fathomAnalytics = (0, import_pieces_framework7.createPiece)({
  displayName: "Fathom Analytics",
  description: "Privacy-focused website analytics. Query your site traffic, manage sites and events, and generate custom reports.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/fathom-analytics.png",
  categories: [import_pieces_framework8.PieceCategory.MARKETING, import_pieces_framework8.PieceCategory.BUSINESS_INTELLIGENCE],
  auth: fathomAuth,
  actions: [
    listSites,
    getSite,
    createEvent,
    listEvents,
    getAggregation,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: fathomAuth,
      baseUrl: () => FATHOM_API_BASE,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  authors: ["Harmatta", "sanket-a11y"],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  fathomAnalytics
});
