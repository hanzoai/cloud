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

// ../ap/packages/pieces/community/plausible/src/index.ts
var index_exports = {};
__export(index_exports, {
  plausible: () => plausible,
  plausibleAuth: () => plausibleAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common"), import_pieces_framework23 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/plausible/src/lib/actions/get-realtime-visitors.action.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/plausible/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var plausibleCommon = {
  baseUrl: "https://plausible.io/api/v1"
};
async function plausibleApiCall({
  apiKey,
  method,
  endpoint,
  body,
  queryParams
}) {
  let request = {
    url: `${plausibleCommon.baseUrl}${endpoint}`,
    method,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: apiKey
    },
    body,
    queryParams
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}
async function getSites(apiKey) {
  return (await plausibleApiCall({
    apiKey,
    method: import_pieces_common.HttpMethod.GET,
    endpoint: "/sites"
  })).sites;
}
var siteIdDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Site",
  description: "Select a site",
  required: !0,
  auth: plausibleAuth,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await getSites(auth.secret_text)).map((site) => ({
      label: site.domain,
      value: site.domain
    }))
  } : {
    disabled: !0,
    placeholder: "Connect your account first",
    options: []
  }
});
async function getTeams(apiKey) {
  return (await plausibleApiCall({
    apiKey,
    method: import_pieces_common.HttpMethod.GET,
    endpoint: "/sites/teams"
  })).teams;
}
var teamIdDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Team",
  description: "Select a team",
  required: !1,
  refreshers: [],
  auth: plausibleAuth,
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await getTeams(auth.secret_text)).filter((team) => team.api_available).map((team) => ({
      label: team.name,
      value: team.id
    }))
  } : {
    disabled: !0,
    placeholder: "Connect your account first",
    options: []
  }
});
async function getGoals(apiKey, siteId) {
  return (await plausibleApiCall({
    apiKey,
    method: import_pieces_common.HttpMethod.GET,
    endpoint: "/sites/goals",
    queryParams: { site_id: siteId }
  })).goals;
}
var goalIdDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Goal",
  description: "Select a goal",
  required: !0,
  auth: plausibleAuth,
  refreshers: ["site_id"],
  options: async ({ auth, site_id }) => auth ? site_id ? {
    disabled: !1,
    options: (await getGoals(auth.secret_text, site_id)).map((goal) => ({
      label: goal.display_name,
      value: goal.id
    }))
  } : {
    disabled: !0,
    placeholder: "Select a site first",
    options: []
  } : {
    disabled: !0,
    placeholder: "Connect your account first",
    options: []
  }
});
async function getCustomProperties(apiKey, siteId) {
  return (await plausibleApiCall({
    apiKey,
    method: import_pieces_common.HttpMethod.GET,
    endpoint: "/sites/custom-props",
    queryParams: { site_id: siteId }
  })).custom_properties;
}
var customPropertyDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Custom Property",
  description: "Select a custom property",
  required: !0,
  auth: plausibleAuth,
  refreshers: ["site_id"],
  options: async ({ auth, site_id }) => auth ? site_id ? {
    disabled: !1,
    options: (await getCustomProperties(auth.secret_text, site_id)).map((prop) => ({
      label: prop.property,
      value: prop.property
    }))
  } : {
    disabled: !0,
    placeholder: "Select a site first",
    options: []
  } : {
    disabled: !0,
    placeholder: "Connect your account first",
    options: []
  }
});
async function getGuests(apiKey, siteId) {
  return (await plausibleApiCall({
    apiKey,
    method: import_pieces_common.HttpMethod.GET,
    endpoint: "/sites/guests",
    queryParams: { site_id: siteId }
  })).guests;
}
var guestEmailDropdown = import_pieces_framework.Property.Dropdown({
  displayName: "Guest",
  description: "Select a guest",
  required: !0,
  auth: plausibleAuth,
  refreshers: ["site_id"],
  options: async ({ auth, site_id }) => auth ? site_id ? {
    disabled: !1,
    options: (await getGuests(auth.secret_text, site_id)).map((guest) => ({
      label: `${guest.email} (${guest.role})`,
      value: guest.email
    }))
  } : {
    disabled: !0,
    placeholder: "Select a site first",
    options: []
  } : {
    disabled: !0,
    placeholder: "Connect your account first",
    options: []
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/get-realtime-visitors.action.ts
var getRealtimeVisitors = (0, import_pieces_framework2.createAction)({
  name: "get_realtime_visitors",
  auth: plausibleAuth,
  displayName: "Get Realtime Visitors",
  description: "Get the number of current visitors on your site",
  audience: "both",
  aiMetadata: { description: "Fetches the count of visitors active on a Plausible-tracked site in real time. Use to check current live traffic on a specific site (chosen by domain). Read-only; safe to call repeatedly.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown
  },
  async run({ auth, propsValue }) {
    return { visitors: await plausibleApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common2.HttpMethod.GET,
      endpoint: "/stats/realtime/visitors",
      queryParams: { site_id: propsValue.site_id }
    }) };
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/get-aggregate-stats.action.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getAggregateStats = (0, import_pieces_framework3.createAction)({
  name: "get_aggregate_stats",
  auth: plausibleAuth,
  displayName: "Get Aggregate Stats",
  description: "Get aggregated analytics metrics for a time period",
  audience: "both",
  aiMetadata: { description: "Returns aggregated analytics metrics (such as visitors, pageviews, bounce rate, or visit duration) for a site over a preset time period like the last 7 or 30 days. Use for high-level totals across a window rather than per-dimension breakdowns. Requires a site and at least one metric; read-only and safe to repeat.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown,
    period: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Period",
      required: !0,
      options: {
        options: [
          { label: "Today", value: "day" },
          { label: "Last 7 days", value: "7d" },
          { label: "Last 30 days", value: "30d" },
          { label: "This month", value: "month" },
          { label: "Last 6 months", value: "6mo" },
          { label: "Last 12 months", value: "12mo" }
        ]
      }
    }),
    metrics: import_pieces_framework3.Property.StaticMultiSelectDropdown({
      displayName: "Metrics",
      required: !0,
      options: {
        options: [
          { label: "Visitors", value: "visitors" },
          { label: "Pageviews", value: "pageviews" },
          { label: "Bounce Rate", value: "bounce_rate" },
          { label: "Visit Duration", value: "visit_duration" },
          { label: "Views Per Visit", value: "views_per_visit" }
        ]
      }
    })
  },
  async run({ auth, propsValue }) {
    let metricsStr = Array.isArray(propsValue.metrics) ? propsValue.metrics.join(",") : propsValue.metrics, response = await plausibleApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      endpoint: "/stats/aggregate",
      queryParams: {
        site_id: propsValue.site_id,
        period: propsValue.period,
        metrics: metricsStr
      }
    });
    return Object.fromEntries(
      Object.entries(response.results).map(([k, v]) => [k, v.value])
    );
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/get-breakdown.action.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getBreakdown = (0, import_pieces_framework4.createAction)({
  name: "get_breakdown",
  auth: plausibleAuth,
  displayName: "Get Traffic Breakdown",
  description: "Break down traffic by page, source, country, or other dimensions",
  audience: "both",
  aiMetadata: { description: "Breaks down a site's visitor stats by a single chosen dimension (page, source, country, browser, OS, or device) over a preset time period, returning the top entries up to an optional limit. Use to see which pages, sources, or segments drive traffic rather than overall totals. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown,
    period: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Period",
      required: !0,
      options: {
        options: [
          { label: "Today", value: "day" },
          { label: "Last 7 days", value: "7d" },
          { label: "Last 30 days", value: "30d" },
          { label: "This month", value: "month" },
          { label: "Last 6 months", value: "6mo" },
          { label: "Last 12 months", value: "12mo" }
        ]
      }
    }),
    property: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Breakdown By",
      required: !0,
      options: {
        options: [
          { label: "Page", value: "event:page" },
          { label: "Source", value: "visit:source" },
          { label: "Country", value: "visit:country" },
          { label: "Browser", value: "visit:browser" },
          { label: "OS", value: "visit:os" },
          { label: "Device", value: "visit:device" }
        ]
      }
    }),
    limit: import_pieces_framework4.Property.Number({ displayName: "Limit", required: !1, defaultValue: 10 })
  },
  async run({ auth, propsValue }) {
    var _a;
    return (await plausibleApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      endpoint: "/stats/breakdown",
      queryParams: {
        site_id: propsValue.site_id,
        period: propsValue.period,
        property: propsValue.property,
        limit: String((_a = propsValue.limit) != null ? _a : 10)
      }
    })).results;
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/list-sites.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var listSites = (0, import_pieces_framework5.createAction)({
  auth: plausibleAuth,
  name: "list_sites",
  displayName: "List Sites",
  description: "Get a list of sites your Plausible account can access",
  audience: "both",
  aiMetadata: { description: "Lists all sites the authenticated Plausible account can access, each with its domain and timezone. Use to discover available site identifiers (domains) before calling actions that require a specific site. Takes no input; read-only and safe to repeat.", idempotent: !0 },
  props: {},
  async run(context) {
    return { sites: await getSites(context.auth.secret_text) };
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/get-site.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getSite = (0, import_pieces_framework6.createAction)({
  auth: plausibleAuth,
  name: "get_site",
  displayName: "Get Site",
  description: "Get details of a site including tracker script configuration",
  audience: "both",
  aiMetadata: { description: "Fetches the configuration details of a single Plausible site, including its tracker script settings. Use to inspect one site identified by its domain; for enumerating sites use List Sites instead. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown
  },
  async run(context) {
    return await plausibleApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.GET,
      endpoint: `/sites/${encodeURIComponent(context.propsValue.site_id)}`
    });
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/create-site.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createSite = (0, import_pieces_framework7.createAction)({
  auth: plausibleAuth,
  name: "create_site",
  displayName: "Create Site",
  description: "Create a new site in your Plausible account",
  audience: "both",
  aiMetadata: { description: "Creates a new site in the Plausible account, optionally setting timezone, owning team, and tracker options like 404 tracking, hash-based routing, outbound-link, file-download, and form-submission tracking. Use when registering a new domain for analytics. The domain must be globally unique; not idempotent, as a repeat call with the same domain fails or creates a duplicate.", idempotent: !1 },
  props: {
    domain: import_pieces_framework7.Property.ShortText({
      displayName: "Domain",
      description: "Domain of the site (must be globally unique)",
      required: !0
    }),
    timezone: import_pieces_framework7.Property.ShortText({
      displayName: "Timezone",
      description: "Timezone name according to IANA database (e.g., Europe/London). Defaults to Etc/UTC",
      required: !1
    }),
    team_id: teamIdDropdown,
    track_404_pages: import_pieces_framework7.Property.Checkbox({
      displayName: "Track 404 Pages",
      description: "Enable tracking of 404 error pages",
      required: !1,
      defaultValue: !1
    }),
    hash_based_routing: import_pieces_framework7.Property.Checkbox({
      displayName: "Hash-Based Routing",
      description: "Enable hash-based routing for single-page applications",
      required: !1,
      defaultValue: !1
    }),
    outbound_links: import_pieces_framework7.Property.Checkbox({
      displayName: "Outbound Links",
      description: "Track clicks on outbound links",
      required: !1,
      defaultValue: !1
    }),
    file_downloads: import_pieces_framework7.Property.Checkbox({
      displayName: "File Downloads",
      description: "Track file downloads",
      required: !1,
      defaultValue: !1
    }),
    form_submissions: import_pieces_framework7.Property.Checkbox({
      displayName: "Form Submissions",
      description: "Track form submissions",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let domain = context.propsValue.domain, timezone = context.propsValue.timezone, team_id = context.propsValue.team_id, track_404_pages = context.propsValue.track_404_pages, hash_based_routing = context.propsValue.hash_based_routing, outbound_links = context.propsValue.outbound_links, file_downloads = context.propsValue.file_downloads, form_submissions = context.propsValue.form_submissions, body = {
      domain
    };
    timezone && (body.timezone = timezone), team_id && (body.team_id = team_id);
    let trackerConfig = {};
    return track_404_pages && (trackerConfig.track_404_pages = !0), hash_based_routing && (trackerConfig.hash_based_routing = !0), outbound_links && (trackerConfig.outbound_links = !0), file_downloads && (trackerConfig.file_downloads = !0), form_submissions && (trackerConfig.form_submissions = !0), Object.keys(trackerConfig).length > 0 && (body.tracker_script_configuration = trackerConfig), await plausibleApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      endpoint: "/sites",
      body
    });
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/update-site.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var updateSite = (0, import_pieces_framework8.createAction)({
  auth: plausibleAuth,
  name: "update_site",
  displayName: "Update Site",
  description: "Update an existing site in your Plausible account",
  audience: "both",
  aiMetadata: { description: "Updates an existing site identified by its domain, optionally renaming the domain and toggling tracker options such as 404 tracking, hash-based routing, outbound-link, file-download, and form-submission tracking; only the fields you provide are changed. Use to modify an already-registered site. Idempotent, since applying the same values yields the same resulting configuration.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown,
    new_domain: import_pieces_framework8.Property.ShortText({
      displayName: "New Domain",
      description: "New domain name for the site (leave empty to keep current)",
      required: !1
    }),
    track_404_pages: import_pieces_framework8.Property.Checkbox({
      displayName: "Track 404 Pages",
      description: "Enable tracking of 404 error pages",
      required: !1
    }),
    hash_based_routing: import_pieces_framework8.Property.Checkbox({
      displayName: "Hash-Based Routing",
      description: "Enable hash-based routing for single-page applications",
      required: !1
    }),
    outbound_links: import_pieces_framework8.Property.Checkbox({
      displayName: "Outbound Links",
      description: "Track clicks on outbound links",
      required: !1
    }),
    file_downloads: import_pieces_framework8.Property.Checkbox({
      displayName: "File Downloads",
      description: "Track file downloads",
      required: !1
    }),
    form_submissions: import_pieces_framework8.Property.Checkbox({
      displayName: "Form Submissions",
      description: "Track form submissions",
      required: !1
    })
  },
  async run(context) {
    let site_id = context.propsValue.site_id, new_domain = context.propsValue.new_domain, track_404_pages = context.propsValue.track_404_pages, hash_based_routing = context.propsValue.hash_based_routing, outbound_links = context.propsValue.outbound_links, file_downloads = context.propsValue.file_downloads, form_submissions = context.propsValue.form_submissions, body = {};
    new_domain && (body.domain = new_domain);
    let trackerConfig = {};
    return track_404_pages != null && (trackerConfig.track_404_pages = track_404_pages), hash_based_routing != null && (trackerConfig.hash_based_routing = hash_based_routing), outbound_links != null && (trackerConfig.outbound_links = outbound_links), file_downloads != null && (trackerConfig.file_downloads = file_downloads), form_submissions != null && (trackerConfig.form_submissions = form_submissions), Object.keys(trackerConfig).length > 0 && (body.tracker_script_configuration = trackerConfig), await plausibleApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.PUT,
      endpoint: `/sites/${encodeURIComponent(site_id)}`,
      body
    });
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/delete-site.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var deleteSite = (0, import_pieces_framework9.createAction)({
  auth: plausibleAuth,
  name: "delete_site",
  displayName: "Delete Site",
  description: "Delete a site and all its data from your Plausible account. This action is permanent and may take up to 48 hours to complete.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a site (identified by its domain) and all of its analytics data from the Plausible account; the removal may take up to 48 hours to finalize. Use only when a site should be fully and irreversibly removed. Idempotent in effect, since the target ends up deleted regardless of repeats.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown
  },
  async run(context) {
    return await plausibleApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.DELETE,
      endpoint: `/sites/${encodeURIComponent(context.propsValue.site_id)}`
    });
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/create-shared-link.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var createSharedLink = (0, import_pieces_framework10.createAction)({
  auth: plausibleAuth,
  name: "create_shared_link",
  displayName: "Create Shared Link",
  description: "Find or create a shared link for a site",
  audience: "both",
  aiMetadata: { description: "Returns a public shared dashboard link for a site, creating one with the given name if it does not already exist (find-or-create). Use to obtain a shareable analytics URL for a specific site and link name. Idempotent, as repeating with the same site and name returns the existing link rather than duplicating it.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown,
    name: import_pieces_framework10.Property.ShortText({
      displayName: "Link Name",
      description: "Name of the shared link (e.g., Wordpress)",
      required: !0
    })
  },
  async run(context) {
    return await plausibleApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.PUT,
      endpoint: "/sites/shared-links",
      body: {
        site_id: context.propsValue.site_id,
        name: context.propsValue.name
      }
    });
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/list-goals.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var listGoals = (0, import_pieces_framework11.createAction)({
  auth: plausibleAuth,
  name: "list_goals",
  displayName: "List Goals",
  description: "Get a list of goals for a site",
  audience: "both",
  aiMetadata: { description: "Lists the conversion goals configured for a site, including each goal's id, type, and display name. Use to discover existing goals or obtain a goal id before deleting one. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown
  },
  async run(context) {
    return { goals: await getGoals(
      context.auth.secret_text,
      context.propsValue.site_id
    ) };
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/create-goal.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var createGoal = (0, import_pieces_framework12.createAction)({
  auth: plausibleAuth,
  name: "create_goal",
  displayName: "Create Goal",
  description: "Find or create a goal for a site",
  audience: "both",
  aiMetadata: { description: "Returns a conversion goal for a site, creating it if it does not already exist (find-or-create). Supports two goal types: an event goal (matched by event name) or a page goal (matched by page path, which may use wildcards); supply the field matching the chosen type. Idempotent, as repeating with the same definition returns the existing goal rather than duplicating it.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown,
    goal_type: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Goal Type",
      description: "Type of goal to create",
      required: !0,
      options: {
        options: [
          { label: "Event", value: "event" },
          { label: "Page", value: "page" }
        ]
      }
    }),
    event_name: import_pieces_framework12.Property.ShortText({
      displayName: "Event Name",
      description: "Name of the event (required if goal type is Event)",
      required: !1
    }),
    page_path: import_pieces_framework12.Property.ShortText({
      displayName: "Page Path",
      description: "Page path to track (required if goal type is Page). Supports wildcards.",
      required: !1
    }),
    display_name: import_pieces_framework12.Property.ShortText({
      displayName: "Display Name",
      description: "Custom display name for the goal in the dashboard",
      required: !1
    })
  },
  async run(context) {
    let goal_type = context.propsValue.goal_type, event_name = context.propsValue.event_name, page_path = context.propsValue.page_path, display_name = context.propsValue.display_name, body = {
      site_id: context.propsValue.site_id,
      goal_type
    };
    return goal_type === "event" && event_name && (body.event_name = event_name), goal_type === "page" && page_path && (body.page_path = page_path), display_name && (body.display_name = display_name), await plausibleApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.PUT,
      endpoint: "/sites/goals",
      body
    });
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/delete-goal.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var deleteGoal = (0, import_pieces_framework13.createAction)({
  auth: plausibleAuth,
  name: "delete_goal",
  displayName: "Delete Goal",
  description: "Delete a goal from a site",
  audience: "both",
  aiMetadata: { description: "Deletes a conversion goal from a site, identified by the site and the goal id. Use List Goals first to obtain the goal id. Idempotent in effect, since the goal ends up removed regardless of repeats.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown,
    goal_id: goalIdDropdown
  },
  async run(context) {
    return await plausibleApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.DELETE,
      endpoint: `/sites/goals/${encodeURIComponent(context.propsValue.goal_id)}`,
      body: {
        site_id: context.propsValue.site_id
      }
    });
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/list-custom-properties.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var listCustomProperties = (0, import_pieces_framework14.createAction)({
  auth: plausibleAuth,
  name: "list_custom_properties",
  displayName: "List Custom Properties",
  description: "Get a list of custom properties for a site",
  audience: "both",
  aiMetadata: { description: "Lists the custom event properties configured for a site. Use to discover which custom properties exist before creating or deleting one. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown
  },
  async run(context) {
    return { custom_properties: await getCustomProperties(
      context.auth.secret_text,
      context.propsValue.site_id
    ) };
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/create-custom-property.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var createCustomProperty = (0, import_pieces_framework15.createAction)({
  auth: plausibleAuth,
  name: "create_custom_property",
  displayName: "Create Custom Property",
  description: "Create a custom property for a site",
  audience: "both",
  aiMetadata: { description: "Registers a custom event property by name for a site so it can be tracked. Use to enable a new custom dimension on a specific site. Idempotent, as adding the same property name again leaves the site with that property present.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown,
    property: import_pieces_framework15.Property.ShortText({
      displayName: "Property Name",
      description: "Name of the custom property",
      required: !0
    })
  },
  async run(context) {
    return await plausibleApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.PUT,
      endpoint: "/sites/custom-props",
      body: {
        site_id: context.propsValue.site_id,
        property: context.propsValue.property
      }
    });
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/delete-custom-property.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var deleteCustomProperty = (0, import_pieces_framework16.createAction)({
  auth: plausibleAuth,
  name: "delete_custom_property",
  displayName: "Delete Custom Property",
  description: "Delete a custom property from a site",
  audience: "both",
  aiMetadata: { description: "Removes a custom event property from a site, identified by the site and the property name. Use to stop tracking a custom dimension. Idempotent in effect, since the property ends up removed regardless of repeats.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown,
    property: customPropertyDropdown
  },
  async run(context) {
    return await plausibleApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.DELETE,
      endpoint: `/sites/custom-props/${encodeURIComponent(context.propsValue.property)}`,
      body: {
        site_id: context.propsValue.site_id
      }
    });
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/list-guests.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var listGuests = (0, import_pieces_framework17.createAction)({
  auth: plausibleAuth,
  name: "list_guests",
  displayName: "List Guests",
  description: "Get a list of guests for a site",
  audience: "both",
  aiMetadata: { description: "Lists the guest users and pending invitations for a site, with each guest's email, role, and status. Use to audit who has access or to find a guest email before removing access. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown
  },
  async run(context) {
    return { guests: await getGuests(
      context.auth.secret_text,
      context.propsValue.site_id
    ) };
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/invite-guest.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var inviteGuest = (0, import_pieces_framework18.createAction)({
  auth: plausibleAuth,
  name: "invite_guest",
  displayName: "Invite Guest",
  description: "Invite a guest to access a site or find an existing invitation",
  audience: "both",
  aiMetadata: { description: "Grants a guest access to a site at a chosen role (viewer or editor) by email, or returns the existing invitation if one is already present (find-or-create). Use to share a site with another user. Idempotent, as re-inviting the same email returns the existing invitation rather than sending a duplicate.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown,
    email: import_pieces_framework18.Property.ShortText({
      displayName: "Email",
      description: "Guest's email address",
      required: !0
    }),
    role: import_pieces_framework18.Property.StaticDropdown({
      displayName: "Role",
      description: "Role to assign to the guest",
      required: !0,
      options: {
        options: [
          { label: "Viewer", value: "viewer" },
          { label: "Editor", value: "editor" }
        ]
      }
    })
  },
  async run(context) {
    return await plausibleApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common14.HttpMethod.PUT,
      endpoint: "/sites/guests",
      body: {
        site_id: context.propsValue.site_id,
        email: context.propsValue.email,
        role: context.propsValue.role
      }
    });
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/remove-guest.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var removeGuest = (0, import_pieces_framework19.createAction)({
  auth: plausibleAuth,
  name: "remove_guest",
  displayName: "Remove Guest",
  description: "Remove a guest or invitation from a site",
  audience: "both",
  aiMetadata: { description: "Revokes a guest's access to a site (or cancels a pending invitation), identified by the site and the guest email. Use to remove someone's access. Idempotent in effect, since the guest ends up without access regardless of repeats.", idempotent: !0 },
  props: {
    site_id: siteIdDropdown,
    email: guestEmailDropdown
  },
  async run(context) {
    return await plausibleApiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common15.HttpMethod.DELETE,
      endpoint: `/sites/guests/${encodeURIComponent(context.propsValue.email)}`,
      body: {
        site_id: context.propsValue.site_id
      }
    });
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/actions/list-teams.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var listTeams = (0, import_pieces_framework20.createAction)({
  auth: plausibleAuth,
  name: "list_teams",
  displayName: "List Teams",
  description: "Get a list of teams your Plausible account can access",
  audience: "both",
  aiMetadata: { description: "Lists the teams the authenticated Plausible account can access, with each team's id and name. Use to discover a team id before creating a site under a specific team. Takes no input; read-only and safe to repeat.", idempotent: !0 },
  props: {},
  async run(context) {
    return { teams: await getTeams(context.auth.secret_text) };
  }
});

// ../ap/packages/pieces/community/plausible/src/lib/triggers/traffic-spike.trigger.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var trafficSpike = (0, import_pieces_framework21.createTrigger)({
  name: "traffic_spike",
  auth: plausibleAuth,
  displayName: "Traffic Spike",
  description: "Triggers once when current visitors exceed a threshold, then resets when they drop below it",
  aiMetadata: {
    description: "Fires once when a site's real-time visitor count crosses above a configured threshold, representing the onset of a traffic spike. It does not fire again until the count drops back below the threshold and then rises above it once more, so each sustained spike yields a single event."
  },
  type: import_pieces_framework21.TriggerStrategy.POLLING,
  props: {
    site_id: siteIdDropdown,
    threshold: import_pieces_framework21.Property.Number({
      displayName: "Visitor Threshold",
      description: "Trigger when visitors exceed this number",
      required: !0,
      defaultValue: 100
    })
  },
  async onEnable({ store }) {
    await store.put("wasAboveThreshold", !1, import_pieces_framework21.StoreScope.FLOW);
  },
  async onDisable({ store }) {
    await store.delete("wasAboveThreshold", import_pieces_framework21.StoreScope.FLOW);
  },
  async run({ auth, propsValue, store }) {
    var _a;
    let visitors = await plausibleApiCall({
      apiKey: auth.secret_text,
      method: import_pieces_common16.HttpMethod.GET,
      endpoint: "/stats/realtime/visitors",
      queryParams: { site_id: propsValue.site_id }
    }), threshold = (_a = propsValue.threshold) != null ? _a : 100, isAbove = visitors >= threshold, wasAbove = await store.get("wasAboveThreshold", import_pieces_framework21.StoreScope.FLOW);
    return await store.put("wasAboveThreshold", isAbove, import_pieces_framework21.StoreScope.FLOW), isAbove && !wasAbove ? [
      {
        site_id: propsValue.site_id,
        current_visitors: visitors,
        threshold,
        triggered_at: (/* @__PURE__ */ new Date()).toISOString()
      }
    ] : [];
  },
  sampleData: {
    site_id: "example.com",
    current_visitors: 150,
    threshold: 100,
    triggered_at: "2024-01-01T12:00:00Z"
  }
});

// ../ap/packages/pieces/community/plausible/src/index.ts
var BASE_URL = "https://plausible.io/api/v1", plausibleAuth = import_pieces_framework22.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your API key:
1. Log in to your Plausible Analytics account
2. Click your account name in the top-right menu and go to **Settings**
3. Go to **API Keys** in the left sidebar
4. Click **New API Key**, choose **Stats API** or **Sites API**, and save the key`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common17.httpClient.sendRequest({
        method: import_pieces_common17.HttpMethod.GET,
        url: `${BASE_URL}/sites`,
        authentication: {
          type: import_pieces_common17.AuthenticationType.BEARER_TOKEN,
          token: auth
        }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API key. Please check your Plausible API key." };
    }
  }
}), plausible = (0, import_pieces_framework22.createPiece)({
  displayName: "Plausible Analytics",
  description: "Privacy-friendly website analytics",
  auth: plausibleAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/plausible.png",
  categories: [import_pieces_framework23.PieceCategory.MARKETING],
  authors: ["Tosh94"],
  actions: [
    getRealtimeVisitors,
    getAggregateStats,
    getBreakdown,
    listSites,
    getSite,
    createSite,
    updateSite,
    deleteSite,
    createSharedLink,
    listGoals,
    createGoal,
    deleteGoal,
    listCustomProperties,
    createCustomProperty,
    deleteCustomProperty,
    listGuests,
    inviteGuest,
    removeGuest,
    listTeams,
    (0, import_pieces_common17.createCustomApiCallAction)({
      baseUrl: () => BASE_URL,
      auth: plausibleAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [trafficSpike]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  plausible,
  plausibleAuth
});
