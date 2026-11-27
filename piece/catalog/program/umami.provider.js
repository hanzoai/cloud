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

// ../ap/packages/pieces/community/umami/src/index.ts
var index_exports = {};
__export(index_exports, {
  umami: () => umami
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/umami/src/lib/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), selfHostedAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "Self-hosted (Username & Password)",
  description: "Connect to your own Umami instance using the same credentials you use to log in to your dashboard.",
  required: !0,
  props: {
    baseUrl: import_pieces_framework.Property.ShortText({
      displayName: "Instance URL",
      description: "Base URL of your Umami instance (e.g. `https://umami.example.com`). Do not include a trailing slash.",
      required: !0
    }),
    username: import_pieces_framework.Property.ShortText({
      displayName: "Username",
      description: "Your Umami login username.",
      required: !0
    }),
    password: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Password",
      description: "Your Umami login password.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      let baseUrl = auth.baseUrl.replace(/\/+$/, "");
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: `${baseUrl}/api/auth/login`,
        body: { username: auth.username, password: auth.password }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Connection failed. Check that the URL is reachable and your username/password are correct."
      };
    }
  },
  refresh: {
    generate: async ({ auth }) => {
      let baseUrl = auth.baseUrl.replace(/\/+$/, "");
      return { access_token: (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.POST,
        url: `${baseUrl}/api/auth/login`,
        body: { username: auth.username, password: auth.password }
      })).body.token };
    },
    // Umami does not return expires_in; default to 55 min so the token refreshes
    // well before a typical 1-hour server-side JWT expiry.
    defaultExpiresIn: 3300
  }
}), cloudAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Umami Cloud (API Key)",
  description: "Connect to Umami Cloud using an API key. Create one in **Settings \u2192 API Keys**.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.umami.is/v1/websites",
        headers: { "x-umami-api-key": auth },
        queryParams: { pageSize: "1" }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key. Check your key in Umami Cloud \u2192 Settings \u2192 API Keys."
      };
    }
  }
}), umamiAuth = [selfHostedAuth, cloudAuth];
function getBaseUrl(auth) {
  return auth.type === import_pieces_framework2.AppConnectionType.SECRET_TEXT ? "https://api.umami.is/v1" : auth.props.baseUrl.replace(/\/+$/, "") + "/api";
}
function getAuthHeaders(auth) {
  return auth.type === import_pieces_framework2.AppConnectionType.SECRET_TEXT ? { "x-umami-api-key": auth.secret_text } : { Authorization: `Bearer ${auth.access_token}` };
}

// ../ap/packages/pieces/community/umami/src/lib/actions/get-website-stats.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/umami/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
async function umamiApiCall({
  auth,
  method,
  path,
  body,
  queryParams
}) {
  let baseUrl = getBaseUrl(auth), headers = getAuthHeaders(auth);
  return await import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${baseUrl}${path}`,
    headers,
    queryParams,
    body
  });
}
var umamiCommon = {
  websiteDropdown: import_pieces_framework3.Property.Dropdown({
    displayName: "Website",
    description: "Select the website to use.",
    auth: umamiAuth,
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account first."
        };
      try {
        return {
          disabled: !1,
          options: (await umamiApiCall({
            auth,
            method: import_pieces_common2.HttpMethod.GET,
            path: "/websites",
            queryParams: { pageSize: "100" }
          })).body.data.map((w) => ({
            label: `${w.name} (${w.domain})`,
            value: w.id
          }))
        };
      } catch (e) {
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load websites. Check your connection."
        };
      }
    }
  }),
  dateRange: {
    startDate: import_pieces_framework3.Property.DateTime({
      displayName: "Start Date",
      description: "Beginning of the date range.",
      required: !0
    }),
    endDate: import_pieces_framework3.Property.DateTime({
      displayName: "End Date",
      description: "End of the date range.",
      required: !0
    })
  }
};

// ../ap/packages/pieces/community/umami/src/lib/actions/get-website-stats.ts
var getWebsiteStats = (0, import_pieces_framework4.createAction)({
  auth: umamiAuth,
  name: "get_website_stats",
  displayName: "Get Website Stats",
  description: "Returns a summary of visitors, pageviews, bounce rate, and average visit duration for a date range.",
  audience: "both",
  aiMetadata: {
    description: "Fetch aggregate analytics for one Umami-tracked website over a date range: total visitors, pageviews, bounce rate, and average visit duration. Use for a high-level traffic summary rather than per-dimension breakdowns or time series. Requires a website ID and a start/end date. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    websiteId: umamiCommon.websiteDropdown,
    startDate: umamiCommon.dateRange.startDate,
    endDate: umamiCommon.dateRange.endDate
  },
  async run(context) {
    let { websiteId, startDate, endDate } = context.propsValue;
    return (await umamiApiCall({
      auth: context.auth,
      method: import_pieces_common3.HttpMethod.GET,
      path: `/websites/${websiteId}/stats`,
      queryParams: {
        startAt: String(new Date(startDate).getTime()),
        endAt: String(new Date(endDate).getTime())
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/umami/src/lib/actions/get-website-metrics.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getWebsiteMetrics = (0, import_pieces_framework5.createAction)({
  auth: umamiAuth,
  name: "get_website_metrics",
  displayName: "Get Website Metrics",
  description: "Returns a ranked list for a chosen metric category (e.g. top pages, browsers, or countries) over a date range.",
  audience: "both",
  aiMetadata: {
    description: 'Fetch a ranked breakdown of one Umami-tracked website by a single chosen dimension (the Metric prop selects which, e.g. pages, referrers, browsers, countries, devices, or events) over a date range. Use to answer "top N by X" questions; pick a different metric type per call. Requires a website ID, a start/end date, and the metric type. Read-only and idempotent.',
    idempotent: !0
  },
  props: {
    websiteId: umamiCommon.websiteDropdown,
    startDate: umamiCommon.dateRange.startDate,
    endDate: umamiCommon.dateRange.endDate,
    type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Metric",
      description: "Which metric to rank results by.",
      required: !0,
      defaultValue: "url",
      options: {
        options: [
          { label: "Pages", value: "url" },
          { label: "Referrers", value: "referrer" },
          { label: "Browsers", value: "browser" },
          { label: "Operating Systems", value: "os" },
          { label: "Devices", value: "device" },
          { label: "Countries", value: "country" },
          { label: "Regions", value: "region" },
          { label: "Cities", value: "city" },
          { label: "Languages", value: "language" },
          { label: "Screen Sizes", value: "screen" },
          { label: "Events", value: "event" },
          { label: "Page Titles", value: "title" },
          { label: "Tags", value: "tag" },
          { label: "Hosts", value: "host" },
          { label: "Query Parameters", value: "query" }
        ]
      }
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Max Results",
      description: "Maximum number of results to return.",
      required: !1,
      defaultValue: 500
    })
  },
  async run(context) {
    let { websiteId, startDate, endDate, type, limit } = context.propsValue;
    return (await umamiApiCall({
      auth: context.auth,
      method: import_pieces_common4.HttpMethod.GET,
      path: `/websites/${websiteId}/metrics`,
      queryParams: {
        startAt: String(new Date(startDate).getTime()),
        endAt: String(new Date(endDate).getTime()),
        type,
        limit: String(limit != null ? limit : 500)
      }
    })).body.map((item) => ({
      name: item.x,
      count: item.y
    }));
  }
});

// ../ap/packages/pieces/community/umami/src/lib/actions/get-active-visitors.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getActiveVisitors = (0, import_pieces_framework6.createAction)({
  auth: umamiAuth,
  name: "get_active_visitors",
  displayName: "Get Active Visitors",
  description: "Returns the number of people currently browsing a website in real time.",
  audience: "both",
  aiMetadata: {
    description: 'Fetch the current real-time count of active visitors on one Umami-tracked website. Use for "how many people are on the site right now"; for historical totals use Get Website Stats instead. Requires only a website ID. Read-only and idempotent, though the live count naturally changes between calls.',
    idempotent: !0
  },
  props: {
    websiteId: umamiCommon.websiteDropdown
  },
  async run(context) {
    let { websiteId } = context.propsValue;
    return (await umamiApiCall({
      auth: context.auth,
      method: import_pieces_common5.HttpMethod.GET,
      path: `/websites/${websiteId}/active`
    })).body;
  }
});

// ../ap/packages/pieces/community/umami/src/lib/actions/get-pageviews.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getPageviews = (0, import_pieces_framework7.createAction)({
  auth: umamiAuth,
  name: "get_pageviews",
  displayName: "Get Pageviews",
  description: "Returns pageview and session counts over time, broken down by hour, day, week, month, or year.",
  audience: "both",
  aiMetadata: {
    description: "Fetch a time series of pageview and session counts for one Umami-tracked website across a date range, bucketed by a configurable unit (hour, day, week, month, or year). Use for trend or charting questions rather than a single aggregate total. Requires a website ID and a start/end date; the grouping unit and timezone are optional. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    websiteId: umamiCommon.websiteDropdown,
    startDate: umamiCommon.dateRange.startDate,
    endDate: umamiCommon.dateRange.endDate,
    unit: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Group By",
      description: "How to bucket the results over time.",
      required: !1,
      defaultValue: "day",
      options: {
        options: [
          { label: "Hour", value: "hour" },
          { label: "Day", value: "day" },
          { label: "Week", value: "week" },
          { label: "Month", value: "month" },
          { label: "Year", value: "year" }
        ]
      }
    }),
    timezone: import_pieces_framework7.Property.ShortText({
      displayName: "Timezone",
      description: "Timezone for date grouping, e.g. Europe/Paris or America/New_York. Defaults to UTC.",
      required: !1,
      defaultValue: "UTC"
    })
  },
  async run(context) {
    let { websiteId, startDate, endDate, unit, timezone } = context.propsValue, response = await umamiApiCall({
      auth: context.auth,
      method: import_pieces_common6.HttpMethod.GET,
      path: `/websites/${websiteId}/pageviews`,
      queryParams: {
        startAt: String(new Date(startDate).getTime()),
        endAt: String(new Date(endDate).getTime()),
        unit: unit != null ? unit : "day",
        timezone: timezone != null ? timezone : "UTC"
      }
    });
    return response.body.pageviews.map((pv, i) => {
      var _a, _b;
      return {
        date: pv.x,
        pageviews: pv.y,
        sessions: (_b = (_a = response.body.sessions[i]) == null ? void 0 : _a.y) != null ? _b : 0
      };
    });
  }
});

// ../ap/packages/pieces/community/umami/src/lib/actions/send-event.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var import_pieces_framework9 = require("@activepieces/pieces-framework"), sendEvent = (0, import_pieces_framework8.createAction)({
  auth: umamiAuth,
  name: "send_event",
  displayName: "Send Event",
  description: "Records a custom event or pageview on a website. Leave the event name blank to track a plain pageview.",
  audience: "both",
  aiMetadata: {
    description: "Send a tracking hit to Umami for one website: with an event name it records a named custom event (optionally with extra event properties), or with the event name left blank it records a plain pageview. Use to push analytics data into Umami, not to read it. Requires a website ID and a page path. Not idempotent \u2014 each call appends a new event.",
    idempotent: !1
  },
  props: {
    websiteId: umamiCommon.websiteDropdown,
    url: import_pieces_framework8.Property.ShortText({
      displayName: "Page Path",
      description: "Path of the page to record, e.g. /pricing.",
      required: !0
    }),
    eventName: import_pieces_framework8.Property.ShortText({
      displayName: "Event Name",
      description: "Name of the custom event, e.g. signup-button-click. Leave blank to record a pageview.",
      required: !1
    }),
    eventData: import_pieces_framework8.Property.Object({
      displayName: "Event Properties",
      description: "Extra key-value pairs to attach to the event.",
      required: !1
    }),
    referrer: import_pieces_framework8.Property.ShortText({
      displayName: "Referrer",
      description: "URL the visitor came from.",
      required: !1
    }),
    title: import_pieces_framework8.Property.ShortText({
      displayName: "Page Title",
      description: "Title of the page being recorded.",
      required: !1
    })
  },
  async run(context) {
    let { websiteId, url, eventName, eventData, referrer, title } = context.propsValue, auth = context.auth, sendUrl = auth.type === import_pieces_framework9.AppConnectionType.SECRET_TEXT ? "https://cloud.umami.is/api/send" : `${auth.props.baseUrl.replace(/\/+$/, "")}/api/send`, payload = {
      website: websiteId,
      url
    };
    referrer && (payload.referrer = referrer), title && (payload.title = title), eventName && (payload.name = eventName, eventData && Object.keys(eventData).length > 0 && (payload.data = eventData));
    let response = await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.POST,
      url: sendUrl,
      headers: {
        "Content-Type": "application/json",
        "User-Agent": "Mozilla/5.0 (compatible; Activepieces/1.0)"
      },
      body: {
        type: "event",
        payload
      }
    });
    return { success: response.status >= 200 && response.status < 300 };
  }
});

// ../ap/packages/pieces/community/umami/src/lib/actions/list-websites.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var listWebsites = (0, import_pieces_framework10.createAction)({
  auth: umamiAuth,
  name: "list_websites",
  displayName: "List Websites",
  description: "Returns all websites tracked in your Umami account.",
  audience: "both",
  aiMetadata: {
    description: "List all websites tracked in the connected Umami account, returning each website ID, name, and domain. Use this first to discover the website ID that the analytics and event actions require. Takes only an optional result limit. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework10.Property.Number({
      displayName: "Max Results",
      description: "Maximum number of websites to return.",
      required: !1,
      defaultValue: 100
    })
  },
  async run(context) {
    let { limit } = context.propsValue;
    return (await umamiApiCall({
      auth: context.auth,
      method: import_pieces_common8.HttpMethod.GET,
      path: "/websites",
      queryParams: {
        pageSize: String(limit != null ? limit : 100)
      }
    })).body.data.map((w) => {
      var _a;
      return {
        id: w.id,
        name: w.name,
        domain: w.domain,
        share_id: (_a = w.shareId) != null ? _a : null,
        created_at: w.createdAt
      };
    });
  }
});

// ../ap/packages/pieces/community/umami/src/lib/triggers/new-event.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var props = {
  websiteId: umamiCommon.websiteDropdown
}, polling = {
  strategy: import_pieces_common9.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let now = Date.now(), startAt = lastFetchEpochMS > 0 ? lastFetchEpochMS : now - 1440 * 60 * 1e3, allEvents = [], pageSize = 20, page = 1, hasMore = !0;
    for (; hasMore; ) {
      let response = await umamiApiCall({
        auth,
        method: import_pieces_common9.HttpMethod.GET,
        path: `/websites/${propsValue.websiteId}/events`,
        queryParams: {
          startAt: String(startAt),
          endAt: String(now),
          pageSize: String(pageSize),
          page: String(page)
        }
      });
      allEvents.push(...response.body.data), hasMore = response.body.data.length === pageSize, page++;
    }
    return allEvents.map((event) => ({
      epochMilliSeconds: new Date(event.createdAt).getTime(),
      data: event
    }));
  }
}, newEvent = (0, import_pieces_framework11.createTrigger)({
  auth: umamiAuth,
  name: "new_event",
  displayName: "New Event",
  description: "Triggers when a new custom event is recorded on a website.",
  aiMetadata: {
    description: "Fires when a new custom event is recorded on the selected Umami-tracked website, polling the events feed since the last run. Each fire represents one tracked event (name, URL path, timestamp)."
  },
  props,
  sampleData: {
    id: "abc123",
    event_name: "signup-button-click",
    url_path: "/pricing",
    created_at: "2024-01-15T10:30:00Z"
  },
  type: import_pieces_framework11.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common9.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common9.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common9.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common9.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/umami/src/lib/triggers/new-session.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var props2 = {
  websiteId: umamiCommon.websiteDropdown
}, polling2 = {
  strategy: import_pieces_common10.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let now = Date.now(), startAt = lastFetchEpochMS > 0 ? lastFetchEpochMS : now - 1440 * 60 * 1e3, allSessions = [], pageSize = 20, page = 1, hasMore = !0;
    for (; hasMore; ) {
      let response = await umamiApiCall({
        auth,
        method: import_pieces_common10.HttpMethod.GET,
        path: `/websites/${propsValue.websiteId}/sessions`,
        queryParams: {
          startAt: String(startAt),
          endAt: String(now),
          pageSize: String(pageSize),
          page: String(page)
        }
      });
      allSessions.push(...response.body.data), hasMore = response.body.data.length === pageSize, page++;
    }
    return allSessions.map((session) => ({
      epochMilliSeconds: new Date(session.createdAt).getTime(),
      data: session
    }));
  }
}, newSession = (0, import_pieces_framework12.createTrigger)({
  auth: umamiAuth,
  name: "new_session",
  displayName: "New Session",
  description: "Triggers when a new visitor session is detected on a website.",
  aiMetadata: {
    description: "Fires when a new visitor session is detected on the selected Umami-tracked website, polling the sessions feed since the last run. Each fire represents one session with its visitor context (browser, OS, device, country, timestamp)."
  },
  props: props2,
  sampleData: {
    id: "session-abc123",
    browser: "Chrome",
    os: "Windows 11",
    device: "desktop",
    country: "FR",
    created_at: "2024-01-15T10:30:00Z"
  },
  type: import_pieces_framework12.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common10.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common10.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common10.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common10.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/umami/src/index.ts
var umami = (0, import_pieces_framework13.createPiece)({
  displayName: "Umami",
  description: "Privacy-focused, open-source web analytics.",
  minimumSupportedRelease: "0.86.0",
  logoUrl: "https://cdn.activepieces.com/pieces/umami.png",
  categories: [import_pieces_framework14.PieceCategory.BUSINESS_INTELLIGENCE],
  auth: umamiAuth,
  authors: ["bst1n"],
  actions: [
    getWebsiteStats,
    getWebsiteMetrics,
    getActiveVisitors,
    getPageviews,
    sendEvent,
    listWebsites,
    (0, import_pieces_common11.createCustomApiCallAction)({
      auth: umamiAuth,
      baseUrl: (auth) => auth ? getBaseUrl(auth) : "https://api.umami.is/v1",
      authMapping: async (auth) => getAuthHeaders(auth)
    })
  ],
  triggers: [newEvent, newSession]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  umami
});
