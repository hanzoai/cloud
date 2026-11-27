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

// ../ap/packages/pieces/community/glide/src/index.ts
var index_exports = {};
__export(index_exports, {
  glide: () => glide
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/glide/src/lib/actions/add-rows.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/glide/src/lib/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/glide/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
async function sendRequest({
  auth,
  method,
  path,
  body,
  queryParams
}) {
  return (await import_pieces_common.httpClient.sendRequest(__spreadValues(__spreadValues({
    method,
    url: `${BASE_URL}${path}`,
    headers: __spreadValues({
      Authorization: `Bearer ${auth.secret_text}`,
      Accept: "application/json"
    }, body !== void 0 ? { "Content-Type": "application/json" } : {})
  }, body !== void 0 ? { body } : {}), queryParams ? { queryParams } : {}))).body;
}
async function validateGlideAuth(auth) {
  await listGlideTables({
    type: import_pieces_framework.AppConnectionType.SECRET_TEXT,
    secret_text: auth
  });
}
async function listGlideTables(auth) {
  return (await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.GET,
    path: "/tables"
  })).data;
}
async function getGlideRows({
  auth,
  tableId,
  limit
}) {
  let rows = [], continuation;
  for (; rows.length < limit; ) {
    let remaining = limit - rows.length, response = await sendRequest({
      auth,
      method: import_pieces_common.HttpMethod.GET,
      path: `/tables/${encodeURIComponent(tableId)}/rows`,
      queryParams: __spreadValues({
        limit: String(remaining)
      }, continuation ? { continuation } : {})
    });
    if (rows.push(...response.data), !response.continuation || response.data.length === 0)
      break;
    continuation = response.continuation;
  }
  return rows;
}
async function addGlideRows({
  auth,
  tableId,
  rows
}) {
  return (await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.POST,
    path: `/tables/${encodeURIComponent(tableId)}/rows`,
    body: rows
  })).data;
}
async function updateGlideRow({
  auth,
  tableId,
  rowId,
  row
}) {
  await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.PATCH,
    path: `/tables/${encodeURIComponent(tableId)}/rows/${encodeURIComponent(rowId)}`,
    body: row
  });
}
async function deleteGlideRow({
  auth,
  tableId,
  rowId
}) {
  await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.DELETE,
    path: `/tables/${encodeURIComponent(tableId)}/rows/${encodeURIComponent(rowId)}`
  });
}
var BASE_URL = "https://api.glideapps.com";

// ../ap/packages/pieces/community/glide/src/lib/auth.ts
var glideAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "Secret Token",
  description: `
To get your token:
1. Open Glide and go to the **Data** editor.
2. Open a Big Table and click **Show API**.
3. Copy the **secret token** shown in the API details.
4. Paste it below.

This piece uses Glide's Big Tables API, so the available actions work with Big Tables only.`,
  required: !0,
  validate: async ({ auth }) => {
    let { error } = await (0, import_pieces_framework3.tryCatch)(() => validateGlideAuth(auth));
    return error ? {
      valid: !1,
      error: "Invalid Glide secret token."
    } : {
      valid: !0
    };
  }
});

// ../ap/packages/pieces/community/glide/src/lib/common/props.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var glideProps = {
  tableId: (required = !0) => import_pieces_framework4.Property.Dropdown({
    auth: glideAuth,
    displayName: "Table",
    description: "Select the Glide Big Table to use.",
    required,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Connect your account first"
        };
      let { data: tables, error } = await (0, import_pieces_framework5.tryCatch)(() => listGlideTables(auth));
      return error ? {
        disabled: !0,
        options: [],
        placeholder: "Failed to load Glide tables. Check your connection."
      } : tables.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No Glide Big Tables found for this token."
      } : {
        disabled: !1,
        options: tables.map((table) => ({
          label: table.name,
          value: table.id
        }))
      };
    }
  })
};

// ../ap/packages/pieces/community/glide/src/lib/common/utils.ts
function parseRequiredString(value, fieldName) {
  if (typeof value != "string" || value.trim().length === 0)
    throw new Error(`${fieldName} is required.`);
  return value.trim();
}
function parseGlideRow(value) {
  if (!isGlideRow(value))
    throw new Error("Row must be a JSON object.");
  return value;
}
function parseGlideRows(value) {
  if (!Array.isArray(value) || !value.every((row) => isGlideRow(row)))
    throw new Error("Rows must be an array of JSON objects.");
  return value;
}
function flattenGlideRows(rows) {
  return normalizeRecords(rows.map(flattenRecord));
}
function flattenRecord(value) {
  let flattened = {};
  return flattenIntoRecord(value, "", flattened), flattened;
}
function normalizeRecords(records) {
  let keys = /* @__PURE__ */ new Set();
  for (let record of records)
    for (let key of Object.keys(record))
      keys.add(key);
  let normalizedKeys = Array.from(keys);
  return records.map((record) => {
    var _a;
    let normalized = {};
    for (let key of normalizedKeys)
      normalized[key] = (_a = record[key]) != null ? _a : null;
    return normalized;
  });
}
function flattenIntoRecord(value, prefix, flattened) {
  if (value === void 0) {
    prefix && (flattened[prefix] = null);
    return;
  }
  if (value === null || typeof value == "string" || typeof value == "number" || typeof value == "boolean") {
    prefix && (flattened[prefix] = value);
    return;
  }
  if (Array.isArray(value)) {
    prefix && (flattened[prefix] = formatArrayValue(value));
    return;
  }
  for (let [key, nestedValue] of Object.entries(value)) {
    let nextPrefix = prefix ? `${prefix}_${key}` : key;
    flattenIntoRecord(nestedValue, nextPrefix, flattened);
  }
}
function formatArrayValue(values) {
  return values.length === 0 ? null : values.map((value) => {
    var _a;
    return value === null ? "null" : typeof value == "string" || typeof value == "number" || typeof value == "boolean" ? String(value) : Array.isArray(value) ? (_a = formatArrayValue(value)) != null ? _a : "" : JSON.stringify(flattenRecord(value));
  }).join(", ");
}
function isGlideRow(value) {
  return typeof value != "object" || value === null || Array.isArray(value) ? !1 : Object.values(value).every((item) => isGlideRowValue(item));
}
function isGlideRowValue(value) {
  return value === null || typeof value == "string" || typeof value == "number" || typeof value == "boolean" ? !0 : Array.isArray(value) ? value.every((item) => isGlideRowValue(item)) : isGlideRow(value);
}

// ../ap/packages/pieces/community/glide/src/lib/actions/add-rows.ts
var addRowsAction = (0, import_pieces_framework6.createAction)({
  auth: glideAuth,
  name: "add-rows",
  displayName: "Add Rows",
  description: "Add one or more rows to a Glide Big Table.",
  audience: "both",
  aiMetadata: {
    description: "Append one or more new rows to a Glide Big Table identified by table ID. Use to insert records; each row is a JSON object whose keys must match the table column IDs. Not idempotent \u2014 every call appends, so re-running creates duplicate rows.",
    idempotent: !1
  },
  props: {
    tableId: glideProps.tableId(),
    rows: import_pieces_framework6.Property.Json({
      displayName: "Rows",
      description: "Provide an array of JSON objects where each object matches the Glide table columns.",
      required: !0
    })
  },
  async run(context) {
    let tableId = parseRequiredString(context.propsValue.tableId, "Table");
    return addGlideRows({
      auth: context.auth,
      tableId,
      rows: parseGlideRows(context.propsValue.rows)
    });
  }
});

// ../ap/packages/pieces/community/glide/src/lib/actions/delete-row.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var deleteRowAction = (0, import_pieces_framework7.createAction)({
  auth: glideAuth,
  name: "delete-row",
  displayName: "Delete Row",
  description: "Delete a row from a Glide Big Table.",
  audience: "both",
  aiMetadata: {
    description: "Permanently remove a single row from a Glide Big Table, targeted by table ID and the specific row ID. Requires a known row ID (look one up first via Get Rows if needed). Idempotent on a stable row ID \u2014 the end state is the row no longer existing, and re-running has no further effect.",
    idempotent: !0
  },
  props: {
    tableId: glideProps.tableId(),
    rowId: import_pieces_framework7.Property.ShortText({
      displayName: "Row ID",
      description: "The Glide row ID to delete.",
      required: !0
    })
  },
  async run(context) {
    let tableId = parseRequiredString(context.propsValue.tableId, "Table"), rowId = parseRequiredString(context.propsValue.rowId, "Row ID");
    return await deleteGlideRow({
      auth: context.auth,
      tableId,
      rowId
    }), {
      success: !0,
      rowId
    };
  }
});

// ../ap/packages/pieces/community/glide/src/lib/actions/get-rows.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var getRowsAction = (0, import_pieces_framework8.createAction)({
  auth: glideAuth,
  name: "get-rows",
  displayName: "Get Rows",
  description: "Retrieve rows from a Glide Big Table.",
  audience: "both",
  aiMetadata: {
    description: "Read rows from a Glide Big Table identified by table ID, paginating up to an optional limit (default 100). Use to fetch existing records for inspection or downstream processing. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    tableId: glideProps.tableId(),
    limit: import_pieces_framework8.Property.Number({
      displayName: "Limit",
      description: "Maximum number of rows to return.",
      required: !1,
      defaultValue: 100
    })
  },
  async run(context) {
    var _a;
    let tableId = parseRequiredString(context.propsValue.tableId, "Table"), rows = await getGlideRows({
      auth: context.auth,
      tableId,
      limit: (_a = context.propsValue.limit) != null ? _a : 100
    });
    return flattenGlideRows(rows);
  }
});

// ../ap/packages/pieces/community/glide/src/lib/actions/list-tables.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var listTablesAction = (0, import_pieces_framework9.createAction)({
  auth: glideAuth,
  name: "list-tables",
  displayName: "List Tables",
  description: "List the Glide Big Tables available to the authenticated team.",
  audience: "both",
  aiMetadata: {
    description: "List all Glide Big Tables accessible to the authenticated team, returning their IDs and names. Use this first to discover the table ID required by the row-level actions (get, add, update, delete). Read-only and idempotent.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    return listGlideTables(context.auth);
  }
});

// ../ap/packages/pieces/community/glide/src/lib/actions/update-row.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var updateRowAction = (0, import_pieces_framework10.createAction)({
  auth: glideAuth,
  name: "update-row",
  displayName: "Update Row",
  description: "Update an existing row in a Glide Big Table.",
  audience: "both",
  aiMetadata: {
    description: "Patch an existing row in a Glide Big Table, targeted by table ID and the specific row ID. Supply a JSON object with only the columns to change; omitted columns are left untouched. Requires a known row ID (look one up first via Get Rows if needed). Idempotent \u2014 repeating with the same values yields the same row state.",
    idempotent: !0
  },
  props: {
    tableId: glideProps.tableId(),
    rowId: import_pieces_framework10.Property.ShortText({
      displayName: "Row ID",
      description: "The Glide row ID to update.",
      required: !0
    }),
    row: import_pieces_framework10.Property.Json({
      displayName: "Row",
      description: "Provide a JSON object containing only the columns you want to update.",
      required: !0
    })
  },
  async run(context) {
    let tableId = parseRequiredString(context.propsValue.tableId, "Table"), rowId = parseRequiredString(context.propsValue.rowId, "Row ID");
    return await updateGlideRow({
      auth: context.auth,
      tableId,
      rowId,
      row: parseGlideRow(context.propsValue.row)
    }), {
      success: !0,
      rowId
    };
  }
});

// ../ap/packages/pieces/community/glide/src/index.ts
var glide = (0, import_pieces_framework11.createPiece)({
  displayName: "Glide",
  description: "Manage Glide Big Tables and rows with Glide\u2019s API.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/glide.png",
  categories: [import_pieces_framework12.PieceCategory.PRODUCTIVITY],
  authors: ["veri5ied"],
  auth: glideAuth,
  actions: [
    addRowsAction,
    getRowsAction,
    updateRowAction,
    deleteRowAction,
    listTablesAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: glideAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  glide
});
