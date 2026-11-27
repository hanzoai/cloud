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

// ../ap/packages/pieces/community/tenzo/src/index.ts
var index_exports = {};
__export(index_exports, {
  tenzo: () => tenzo,
  tenzoAuth: () => tenzoAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tenzo/src/lib/common/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/tenzo/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var BASE_URL = "https://api.gotenzo.com/v1.0";
function getHeadersFromAuth(auth) {
  return auth.type === import_pieces_framework.AppConnectionType.SECRET_TEXT ? {
    Authorization: `Token ${auth.secret_text}`
  } : auth.type === import_pieces_framework.AppConnectionType.CUSTOM_AUTH ? {
    "X-APP-ID": auth.props.appId,
    "X-API-SECRET": auth.props.apiSecret
  } : {};
}
function getAuthentication(auth) {
  switch (auth.type) {
    case import_pieces_framework.AppConnectionType.OAUTH2:
    case import_pieces_framework.AppConnectionType.CLOUD_OAUTH2:
    case import_pieces_framework.AppConnectionType.PLATFORM_OAUTH2:
      return {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      };
    case import_pieces_framework.AppConnectionType.SECRET_TEXT:
    case import_pieces_framework.AppConnectionType.CUSTOM_AUTH:
      return;
  }
}
async function tenzoApiCall({
  method,
  path,
  auth,
  query,
  body
}) {
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value !== void 0 && (queryParams[key] = String(value));
  let request = {
    method,
    url: `${BASE_URL}${path}`,
    authentication: getAuthentication(auth),
    headers: __spreadValues({
      "Content-Type": "application/json"
    }, getHeadersFromAuth(auth)),
    queryParams,
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}

// ../ap/packages/pieces/community/tenzo/src/lib/common/auth.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), TENZO_SCOPES = [
  "tenzo/area:read",
  "tenzo/location:read",
  "tenzo/sales:read",
  "tenzo/sales/day:read",
  "tenzo/sales/day/location:read",
  "tenzo/sales/day/area:read",
  "tenzo/sales/product:read",
  "tenzo/sales/product/category:read",
  "tenzo/sales/product/item:read",
  "tenzo/sales/product/location:read",
  "tenzo/sales/product/area:read",
  "tenzo/sales/order:read",
  "tenzo/sales/order/location:read",
  "tenzo/sales/order/area:read",
  "tenzo/sales/time_of_day:read",
  "tenzo/sales/time_of_day/location:read",
  "tenzo/sales/time_of_day/area:read",
  "tenzo/forecast:read",
  "tenzo/forecast/day/location:read",
  "tenzo/forecast/time_of_day/location:read",
  "tenzo/forecast/product/location:read",
  "tenzo/schedule/calendar:read",
  "tenzo/schedule/calendar:write",
  "tenzo/schedule/event:read",
  "tenzo/schedule/event:write",
  "tenzo/analytics:read"
], tenzoAuth = [
  import_pieces_framework2.PieceAuth.OAuth2({
    description: "Connect your Tenzo account using OAuth2. Supports both Authorization Code and Client Credentials flows.",
    authUrl: "https://auth.gotenzo.com/o/authorize/",
    tokenUrl: "https://auth.gotenzo.com/o/token/",
    required: !0,
    scope: [...TENZO_SCOPES],
    grantType: "both_client_credentials_and_authorization_code",
    validate: async ({ auth }) => {
      try {
        return await tenzoApiCall({
          method: import_pieces_common2.HttpMethod.GET,
          path: "/areas/",
          auth: {
            type: import_pieces_framework3.AppConnectionType.OAUTH2,
            access_token: auth.access_token
          }
        }), { valid: !0 };
      } catch (e) {
        return {
          valid: !1,
          error: e.message
        };
      }
    }
  }),
  import_pieces_framework2.PieceAuth.SecretText({
    displayName: "API Key",
    required: !0,
    description: 'API Key for Tenzo. Use "Token" prefix authentication method.',
    validate: async ({ auth }) => {
      try {
        return await tenzoApiCall({
          method: import_pieces_common2.HttpMethod.GET,
          path: "/areas/",
          auth: {
            type: import_pieces_framework3.AppConnectionType.SECRET_TEXT,
            secret_text: auth
          }
        }), { valid: !0 };
      } catch (e) {
        return {
          valid: !1,
          error: e.message
        };
      }
    }
  }),
  import_pieces_framework2.PieceAuth.CustomAuth({
    displayName: "App Credentials",
    description: "Use Application ID and API Secret Key for authentication.",
    required: !0,
    props: {
      appId: import_pieces_framework2.Property.ShortText({
        displayName: "Application ID",
        required: !0
      }),
      apiSecret: import_pieces_framework2.PieceAuth.SecretText({
        displayName: "API Secret Key",
        required: !0
      })
    },
    validate: async ({ auth }) => {
      try {
        return await tenzoApiCall({
          method: import_pieces_common2.HttpMethod.GET,
          path: "/areas/",
          auth: {
            type: import_pieces_framework3.AppConnectionType.CUSTOM_AUTH,
            props: {
              appId: auth.appId,
              apiSecret: auth.apiSecret
            }
          }
        }), { valid: !0 };
      } catch (e) {
        return {
          valid: !1,
          error: e.message
        };
      }
    }
  })
];

// ../ap/packages/pieces/community/tenzo/src/lib/triggers/new-daily-forecast.trigger.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/tenzo/src/lib/common/props.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var tenzoCommon = {
  location_id: (required = !0) => import_pieces_framework4.Property.Dropdown({
    displayName: "Location",
    description: "Select the location to filter data",
    required,
    refreshers: [],
    auth: tenzoAuth,
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
      try {
        return {
          options: (await tenzoApiCall({
            method: import_pieces_common3.HttpMethod.GET,
            path: "/locations/",
            auth
          })).results.map((location) => ({
            label: location.name,
            value: location.id.toString()
          }))
        };
      } catch (error) {
        return console.error("Failed to fetch locations:", error), {
          disabled: !0,
          options: [],
          placeholder: "Failed to load locations"
        };
      }
    }
  }),
  area_id: (required = !0) => import_pieces_framework4.Property.Dropdown({
    displayName: "Area",
    description: "Select the area to filter data",
    required,
    refreshers: [],
    auth: tenzoAuth,
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
      try {
        return {
          options: (await tenzoApiCall({
            method: import_pieces_common3.HttpMethod.GET,
            path: "/areas/",
            auth
          })).results.map((area) => ({
            label: area.name,
            value: area.id.toString()
          }))
        };
      } catch (error) {
        return console.error("Failed to fetch areas:", error), {
          disabled: !0,
          options: [],
          placeholder: "Failed to load areas"
        };
      }
    }
  })
};

// ../ap/packages/pieces/community/tenzo/src/lib/triggers/new-daily-forecast.trigger.ts
var polling = {
  strategy: import_pieces_common4.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let { location_id } = propsValue;
    if (!location_id)
      return console.error("Location ID is required for polling"), [];
    console.debug("Polling for new daily forecasts", { location_id, lastFetchEpochMS });
    try {
      let response = await tenzoApiCall({
        method: import_pieces_common4.HttpMethod.GET,
        path: "/forecast/day/",
        auth,
        query: {
          location_id: location_id.toString(),
          page_size: "100"
          // Get more results to reduce API calls
        }
      }), cutoffTime = lastFetchEpochMS ? new Date(lastFetchEpochMS) : /* @__PURE__ */ new Date(0), newForecasts = response.results.filter((forecast) => new Date(forecast.created_at || forecast.last_modified) > cutoffTime);
      return console.debug(`Found ${newForecasts.length} new forecasts out of ${response.results.length} total`), newForecasts.map((forecast) => ({
        epochMilliSeconds: new Date(forecast.created_at || forecast.last_modified).getTime(),
        data: forecast
      }));
    } catch (error) {
      return console.error("Error polling for daily forecasts:", error), [];
    }
  }
}, newDailyForecastTrigger = (0, import_pieces_framework5.createTrigger)({
  auth: tenzoAuth,
  name: "new_daily_forecast",
  displayName: "New Daily Forecast",
  description: "Triggers when there is a new daily forecast for a selected location.",
  aiMetadata: {
    description: "Fires when Tenzo generates a new daily sales forecast for the selected location. Each event represents one forecasted day of business and includes the predicted paid sales (with and without tax), promos, comps, and included service charges, along with the date of business and timestamps. Use it to react to freshly produced forecasts for a specific location."
  },
  props: {
    location_id: tenzoCommon.location_id(!0)
  },
  sampleData: {
    id: 12345,
    location_id: 1,
    date_of_business: "2024-01-15",
    paid_sales_with_tax: 1500.5,
    paid_sales_no_tax: 1364.09,
    promos_with_tax: 50,
    promos_no_tax: 45.45,
    comps_with_tax: 25,
    comps_no_tax: 22.73,
    last_modified: "2024-01-14T10:30:00Z",
    included_service_charge_with_tax: 150.05,
    included_service_charge_no_tax: 136.41,
    created_at: "2024-01-14T10:30:00Z"
  },
  type: import_pieces_framework5.TriggerStrategy.POLLING,
  async test(context) {
    let { auth, propsValue, store, files } = context;
    return await import_pieces_common4.pollingHelper.test(polling, { store, auth, propsValue, files });
  },
  async onEnable(context) {
    let { auth, propsValue, store } = context;
    await import_pieces_common4.pollingHelper.onEnable(polling, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { auth, propsValue, store } = context;
    await import_pieces_common4.pollingHelper.onDisable(polling, { store, auth, propsValue });
  },
  async run(context) {
    let { auth, propsValue, store, files } = context;
    return await import_pieces_common4.pollingHelper.poll(polling, { store, auth, propsValue, files });
  }
});

// ../ap/packages/pieces/community/tenzo/src/lib/triggers/new-summary-payments.trigger.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common5.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let { location_id, area_id, include_deleted = !1 } = propsValue;
    console.debug("Polling for new summary payments", { location_id, area_id, include_deleted, lastFetchEpochMS });
    try {
      let queryParams = {
        page_size: "100",
        include_deleted: include_deleted.toString()
      };
      if (location_id && (queryParams.location_id = location_id), area_id && (queryParams.area_id = area_id), lastFetchEpochMS) {
        let lastModifiedDate = new Date(lastFetchEpochMS).toISOString();
        queryParams.last_modified = lastModifiedDate;
      }
      let response = await tenzoApiCall({
        method: import_pieces_common5.HttpMethod.GET,
        path: "/sales/day/",
        auth,
        query: queryParams
      }), cutoffTime = lastFetchEpochMS ? new Date(lastFetchEpochMS) : /* @__PURE__ */ new Date(0), newSalesData = response.results.filter((sales) => new Date(sales.last_modified) > cutoffTime);
      return console.debug(`Found ${newSalesData.length} new sales records out of ${response.results.length} total`), newSalesData.map((sales) => ({
        epochMilliSeconds: new Date(sales.last_modified).getTime(),
        data: sales
      }));
    } catch (error) {
      return console.error("Error polling for summary payments:", error), [];
    }
  }
}, newSummaryPaymentsTrigger = (0, import_pieces_framework6.createTrigger)({
  auth: tenzoAuth,
  name: "new_summary_payments",
  displayName: "New Summary Payments",
  description: "Triggers when there is new daily sales and payments data.",
  aiMetadata: {
    description: "Fires when Tenzo records new or updated daily sales-and-payments data, optionally scoped to a location and/or area and optionally including deleted records. Each event represents one day-of-business summary with paid sales (with and without tax), promos, comps, included service charges, transaction count, guest count, and last-modified and deletion timestamps. Use it to react to freshly posted or revised daily payment totals."
  },
  props: {
    location_id: tenzoCommon.location_id(!1),
    area_id: tenzoCommon.area_id(!1),
    include_deleted: import_pieces_framework6.Property.Checkbox({
      displayName: "Include Deleted",
      description: "Include data that has been deleted",
      required: !1,
      defaultValue: !1
    })
  },
  sampleData: {
    id: 12345,
    location_id: 1,
    date_of_business: "2024-01-15",
    paid_sales_with_tax: 2500.75,
    paid_sales_no_tax: 2273.41,
    promos_with_tax: 100,
    promos_no_tax: 90.91,
    comps_with_tax: 50,
    comps_no_tax: 45.45,
    included_service_charge_with_tax: 250.08,
    included_service_charge_no_tax: 227.34,
    last_modified: "2024-01-15T18:30:00Z",
    number_transactions: 156,
    guest_count: 89,
    deleted_at: null
  },
  type: import_pieces_framework6.TriggerStrategy.POLLING,
  async test(context) {
    let { auth, propsValue, store, files } = context;
    return await import_pieces_common5.pollingHelper.test(polling2, { store, auth, propsValue, files });
  },
  async onEnable(context) {
    let { auth, propsValue, store } = context;
    await import_pieces_common5.pollingHelper.onEnable(polling2, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { auth, propsValue, store } = context;
    await import_pieces_common5.pollingHelper.onDisable(polling2, { store, auth, propsValue });
  },
  async run(context) {
    let { auth, propsValue, store, files } = context;
    return await import_pieces_common5.pollingHelper.poll(polling2, { store, auth, propsValue, files });
  }
});

// ../ap/packages/pieces/community/tenzo/src/lib/triggers/new-summary-sales.trigger.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var polling3 = {
  strategy: import_pieces_common6.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let { location_id, area_id } = propsValue;
    console.debug("Polling for new summary sales", { location_id, area_id, lastFetchEpochMS });
    try {
      let queryParams = {
        page_size: "100"
      };
      if (location_id && (queryParams.location_id = location_id), area_id && (queryParams.area_id = area_id), lastFetchEpochMS) {
        let lastModifiedDate = new Date(lastFetchEpochMS).toISOString();
        queryParams.last_modified = lastModifiedDate;
      }
      let response = await tenzoApiCall({
        method: import_pieces_common6.HttpMethod.GET,
        path: "/sales/day/",
        auth,
        query: queryParams
      }), cutoffTime = lastFetchEpochMS ? new Date(lastFetchEpochMS) : /* @__PURE__ */ new Date(0), newSalesData = response.results.filter((sales) => new Date(sales.last_modified) > cutoffTime);
      return console.debug(`Found ${newSalesData.length} new sales records out of ${response.results.length} total`), newSalesData.map((sales) => ({
        epochMilliSeconds: new Date(sales.last_modified).getTime(),
        data: sales
      }));
    } catch (error) {
      return console.error("Error polling for summary sales:", error), [];
    }
  }
}, newSummarySalesTrigger = (0, import_pieces_framework7.createTrigger)({
  auth: tenzoAuth,
  name: "new_summary_sales",
  displayName: "New Summary Sales",
  description: "Triggers when there is new daily sales data.",
  aiMetadata: {
    description: "Fires when Tenzo records new or updated daily sales data, optionally scoped to a specific location and/or area. Each event represents one day-of-business sales summary including paid sales (with and without tax), promos, comps, included service charges, transaction count, guest count, and a last-modified timestamp. Use it to react to freshly posted or revised daily sales totals."
  },
  props: {
    location_id: tenzoCommon.location_id(!1),
    area_id: tenzoCommon.area_id(!1)
  },
  sampleData: {
    id: 12345,
    location_id: 1,
    date_of_business: "2024-01-15",
    paid_sales_with_tax: 2500.75,
    paid_sales_no_tax: 2273.41,
    promos_with_tax: 100,
    promos_no_tax: 90.91,
    comps_with_tax: 50,
    comps_no_tax: 45.45,
    included_service_charge_with_tax: 250.08,
    included_service_charge_no_tax: 227.34,
    last_modified: "2024-01-15T18:30:00Z",
    number_transactions: 156,
    guest_count: 89,
    deleted_at: null
  },
  type: import_pieces_framework7.TriggerStrategy.POLLING,
  async test(context) {
    let { auth, propsValue, store, files } = context;
    return await import_pieces_common6.pollingHelper.test(polling3, { store, auth, propsValue, files });
  },
  async onEnable(context) {
    let { auth, propsValue, store } = context;
    await import_pieces_common6.pollingHelper.onEnable(polling3, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { auth, propsValue, store } = context;
    await import_pieces_common6.pollingHelper.onDisable(polling3, { store, auth, propsValue });
  },
  async run(context) {
    let { auth, propsValue, store, files } = context;
    return await import_pieces_common6.pollingHelper.poll(polling3, { store, auth, propsValue, files });
  }
});

// ../ap/packages/pieces/community/tenzo/src/index.ts
var tenzo = (0, import_pieces_framework8.createPiece)({
  displayName: "Tenzo",
  description: "Extract data and insights from the Tenzo platform for sales, forecasting, and analytics.",
  auth: tenzoAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/tenzo.png",
  categories: [import_pieces_framework9.PieceCategory.BUSINESS_INTELLIGENCE],
  authors: ["onyedikachi-david"],
  actions: [],
  triggers: [newDailyForecastTrigger, newSummaryPaymentsTrigger, newSummarySalesTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  tenzo,
  tenzoAuth
});
