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

// ../ap/packages/pieces/community/clicdata/src/index.ts
var index_exports = {};
__export(index_exports, {
  clicdata: () => clicdata
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clicdata/src/lib/common/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/clicdata/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var DEFAULT_API_VERSION = "2022.01", BASE_URL = "https://api.clicdata.com";
function getHeadersFromAuth(auth) {
  if (auth.type === import_pieces_framework.AppConnectionType.SECRET_TEXT)
    return {
      "CLICDATA-API-KEY": auth.secret_text
    };
  if (auth.type === import_pieces_framework.AppConnectionType.CUSTOM_AUTH) {
    let encodedEmailAndPassword = Buffer.from(
      `${auth.props.userEmail}:${auth.props.userPassword}`
    ).toString("base64");
    return {
      Authorization: `Basic ${Buffer.from(
        `${auth.props.clientId}:${encodedEmailAndPassword}`
      ).toString("base64")}`
    };
  }
  return {};
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
async function clicdataApiCall({
  method,
  path,
  auth,
  query,
  body,
  apiVersion
}) {
  var _a, _b;
  let queryParams = {
    api_version: apiVersion != null ? apiVersion : DEFAULT_API_VERSION
  };
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
  }, response = await import_pieces_common.httpClient.sendRequest(
    request
  );
  if (!response.body.success) {
    let errorMsg = ((_a = response.body.error) == null ? void 0 : _a.description) || ((_b = response.body.error) == null ? void 0 : _b.code) || "ClicData API error";
    throw new Error(errorMsg);
  }
  return response.body;
}

// ../ap/packages/pieces/community/clicdata/src/lib/common/auth.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), CLICDATA_SCOPES = [
  "account_read",
  "data_read",
  "data_write",
  "table_read",
  "table_write"
], clicdataAuth = [
  import_pieces_framework2.PieceAuth.OAuth2({
    description: "Connect your ClicData account using OAuth2. Supports both Authorization Code (user-based) and Client Credentials (server-to-server) flows. Configure your preferred flow in ClicData under Account > API Apps.",
    authUrl: "https://api.clicdata.com/oauth20/authorize",
    tokenUrl: "https://api.clicdata.com/oauth20/token",
    required: !0,
    scope: [...CLICDATA_SCOPES],
    grantType: "both_client_credentials_and_authorization_code",
    validate: async ({ auth }) => {
      try {
        return await clicdataApiCall({
          method: import_pieces_common2.HttpMethod.GET,
          path: "/account",
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
    description: "API key generated in ClicData under Account > API Apps",
    validate: async ({ auth }) => {
      try {
        return await clicdataApiCall({
          method: import_pieces_common2.HttpMethod.GET,
          path: "/account",
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
    displayName: "Basic Auth",
    description: "Sign in using your ClicData user credentials (used to build the Basic token).",
    required: !0,
    props: {
      clientId: import_pieces_framework2.Property.ShortText({
        displayName: "Client ID",
        required: !0
      }),
      userEmail: import_pieces_framework2.Property.ShortText({
        displayName: "User Email",
        required: !0
      }),
      userPassword: import_pieces_framework2.PieceAuth.SecretText({
        displayName: "User Password",
        required: !0
      })
    },
    validate: async ({ auth }) => {
      try {
        return await clicdataApiCall({
          method: import_pieces_common2.HttpMethod.GET,
          path: "/account",
          auth: {
            type: import_pieces_framework3.AppConnectionType.CUSTOM_AUTH,
            props: {
              clientId: auth.clientId,
              userEmail: auth.userEmail,
              userPassword: auth.userPassword
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

// ../ap/packages/pieces/community/clicdata/src/lib/actions/insert-row.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clicdata/src/lib/common/props.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), clicdataCommonProps = {
  apiVersion: import_pieces_framework4.Property.ShortText({
    displayName: "API Version",
    required: !1,
    description: "Defaults to 2022.01"
  }),
  table_id: import_pieces_framework4.Property.Dropdown({
    auth: clicdataAuth,
    displayName: "Table",
    description: "Select the table to insert data into",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account first"
        };
      try {
        return {
          disabled: !1,
          options: (await clicdataApiCall({
            method: import_pieces_common3.HttpMethod.GET,
            path: "/table",
            auth,
            query: {
              page: 1,
              page_size: 100
            },
            apiVersion: "2025.3"
          })).result.map((table) => ({
            label: table.name,
            value: table.id
          }))
        };
      } catch (error) {
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load tables"
        };
      }
    }
  })
};

// ../ap/packages/pieces/community/clicdata/src/lib/actions/insert-row.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), insertRow = (0, import_pieces_framework5.createAction)({
  auth: clicdataAuth,
  name: "insert_row",
  classification: "WRITE",
  displayName: "Insert Row",
  description: "Insert rows into a ClicData table (maximum 500 rows)",
  audience: "both",
  aiMetadata: { description: "Appends rows to a ClicData table by table ID. Use to push new records into an existing table; supply the data as an array of objects keyed by column name. Capped at 500 rows per call and not idempotent \u2014 each call appends the rows again, creating duplicates if repeated.", idempotent: !1 },
  props: {
    table_id: clicdataCommonProps.table_id,
    data: import_pieces_framework5.Property.Json({
      displayName: "Row Data",
      description: "Array of objects representing rows to insert. Each object should have column names as keys. Maximum 500 rows.",
      required: !0,
      defaultValue: [
        {
          Column1: "value1",
          Column2: "value2"
        }
      ]
    }),
    apiVersion: clicdataCommonProps.apiVersion
  },
  async run(context) {
    let { table_id, data, apiVersion } = context.propsValue;
    if (!Array.isArray(data))
      throw new Error("Data must be an array of objects");
    if (data.length > 500)
      throw new Error("Maximum 500 rows allowed per request");
    if (data.some((item) => typeof item != "object" || item === null))
      throw new Error("Each row must be an object with column names as keys");
    return (await clicdataApiCall({
      method: import_pieces_common4.HttpMethod.POST,
      path: `/data/${table_id}/insert`,
      auth: context.auth,
      body: {
        data
      },
      apiVersion: apiVersion || void 0
    })).result;
  }
});

// ../ap/packages/pieces/community/clicdata/src/lib/actions/refresh-table.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), refreshTable = (0, import_pieces_framework6.createAction)({
  auth: clicdataAuth,
  name: "refresh_table",
  classification: "WRITE",
  displayName: "Refresh Table",
  description: "Refresh the selected table depending on the refresh mode",
  audience: "both",
  aiMetadata: { description: "Triggers a refresh of a ClicData table by table ID to re-pull or recompute its data. The refresh mode is required and controls how data is applied \u2014 reload, rebuild, update, append, or update+append. Use after a source changes to update the table. Not idempotent: append-based modes add rows on each call, so repeated runs change the result.", idempotent: !1 },
  props: {
    table_id: clicdataCommonProps.table_id,
    mode: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Refresh Mode",
      description: "Select the refresh mode for the table",
      required: !0,
      options: {
        options: [
          {
            label: "Reload",
            value: "reload"
          },
          {
            label: "Rebuild",
            value: "rebuild"
          },
          {
            label: "Update",
            value: "update"
          },
          {
            label: "Append",
            value: "append"
          },
          {
            label: "Update + Append",
            value: "updateappend"
          }
        ]
      },
      defaultValue: "reload"
    }),
    apiVersion: clicdataCommonProps.apiVersion
  },
  async run(context) {
    let { table_id, mode, apiVersion } = context.propsValue;
    return (await clicdataApiCall({
      method: import_pieces_common5.HttpMethod.POST,
      path: `/table/${table_id}/refresh`,
      auth: context.auth,
      body: {
        mode
      },
      apiVersion: apiVersion || "2025.3"
    })).result;
  }
});

// ../ap/packages/pieces/community/clicdata/src/index.ts
var clicdata = (0, import_pieces_framework7.createPiece)({
  displayName: "Clicdata",
  auth: clicdataAuth,
  minimumSupportedRelease: "0.36.1",
  description: "ClicData enables True Performance with an end-to-end data analytics platform: connect, transform, automate, visualize and share data from 300+ sources.",
  logoUrl: "https://cdn.activepieces.com/pieces/clicdata.png",
  categories: [import_pieces_framework8.PieceCategory.BUSINESS_INTELLIGENCE],
  authors: ["onyedikachi-david"],
  actions: [insertRow, refreshTable],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  clicdata
});
