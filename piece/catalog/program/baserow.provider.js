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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/baserow/src/index.ts
var index_exports = {};
__export(index_exports, {
  baserow: () => baserow
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework26 = require("@activepieces/pieces-framework"), import_pieces_framework27 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/baserow/src/lib/actions/create-row.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/baserow/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), description = "Connect to Baserow using either a Database Token (recommended) or your Email & Password. Fill in only the fields for your chosen method \u2014 leave the other section blank.";
function isJwtMode(authType, props) {
  return authType === "jwt" ? !0 : authType === "database_token" ? !1 : !!(props.email && props.password && !props.token);
}
var baserowAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "Authentication",
  description,
  required: !0,
  props: {
    authType: import_pieces_framework.Property.StaticDropdown({
      displayName: "Authentication Method",
      description: "Choose **Database Token** (recommended) for scoped, per-table access compatible with 2FA. Choose **Email & Password** only if you need automatic webhook registration on triggers \u2014 2FA accounts are not supported.",
      required: !0,
      defaultValue: "database_token",
      options: {
        disabled: !1,
        options: [
          { label: "Database Token (recommended)", value: "database_token" },
          { label: "Email & Password (JWT)", value: "jwt" }
        ]
      }
    }),
    apiUrl: import_pieces_framework.Property.ShortText({
      displayName: "API URL",
      description: "Your Baserow instance URL. Leave the default for Baserow Cloud.",
      required: !0,
      defaultValue: "https://api.baserow.io"
    }),
    md_token: import_pieces_framework.Property.MarkDown({
      value: `---
#### \u{1F511} Database Token
Go to **Settings \u2192 Database tokens** in Baserow, create a token, copy it, and paste it below. Leave Email and Password blank.`
    }),
    token: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Database Token",
      description: "Your Baserow database token. Leave blank when using Email & Password.",
      required: !1
    }),
    md_jwt: import_pieces_framework.Property.MarkDown({
      value: `---
#### \u{1F464} Email & Password (JWT)
Enter your Baserow login credentials below. Leave Database Token blank. Accounts with 2FA enabled are not supported.`
    }),
    email: import_pieces_framework.Property.ShortText({
      displayName: "Email",
      description: "Your Baserow account email. Leave blank when using Database Token.",
      required: !1
    }),
    password: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Password",
      description: "Your Baserow account password. Leave blank when using Database Token.",
      required: !1
    })
  },
  validate: async ({ auth }) => {
    if (isJwtMode(auth.authType, auth)) {
      if (!auth.email || !auth.password)
        return {
          valid: !1,
          error: "Email and Password are required for JWT authentication."
        };
      try {
        return await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.POST,
          url: `${auth.apiUrl}/api/user/token-auth/`,
          body: { email: auth.email, password: auth.password }
        }), { valid: !0 };
      } catch (e) {
        return {
          valid: !1,
          error: "Invalid email, password, or API URL. Note: 2FA is not supported \u2014 use Database Token if 2FA is enabled."
        };
      }
    }
    if (!auth.token)
      return {
        valid: !1,
        error: "Database Token is required when using Database Token authentication."
      };
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${auth.apiUrl}/api/database/tables/all-tables/`,
        headers: { Authorization: `Token ${auth.token}` }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid database token or API URL." };
    }
  }
}), baserowAuthHelpers = {
  isJwtAuth: (auth) => isJwtMode(auth.props.authType, auth.props)
};

// ../ap/packages/pieces/community/baserow/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/baserow/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
function emptyValueFilter(accessor) {
  return (key) => {
    let val = accessor(key);
    return val != null && (typeof val != "string" || val.length > 0);
  };
}
function prepareQuery(request) {
  let params = {};
  return request && Object.keys(request).filter(emptyValueFilter((k) => request[k])).forEach((k) => {
    params[k] = request[k].toString();
  }), params;
}
var BaserowClient = class {
  constructor(baseUrl, authHeader, isJwt = !1) {
    __publicField(this, "baseUrl", baseUrl);
    __publicField(this, "authHeader", authHeader);
    __publicField(this, "isJwt", isJwt);
  }
  static async getJwtToken({
    apiUrl,
    email,
    password
  }) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${apiUrl}/api/user/token-auth/`,
      body: { email, password }
    })).body.token;
  }
  async makeRequest(method, url, query, body) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method,
      url: `${this.baseUrl}/api${url}`,
      headers: { Authorization: this.authHeader },
      queryParams: query,
      body
    })).body;
  }
  async listTables() {
    return this.isJwt ? (await this.makeRequest(
      import_pieces_common2.HttpMethod.GET,
      "/applications/"
    )).filter((a) => a.type === "database").flatMap((a) => a.tables) : await this.makeRequest(
      import_pieces_common2.HttpMethod.GET,
      "/database/tables/all-tables/"
    );
  }
  async listTableFields(table_id) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.GET,
      `/database/fields/table/${table_id}/`
    );
  }
  async createRow(table_id, request) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.POST,
      `/database/rows/table/${table_id}/`,
      {
        user_field_names: "true"
      },
      request
    );
  }
  async updateRow(table_id, row_id, request) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.PATCH,
      `/database/rows/table/${table_id}/${row_id}/`,
      {
        user_field_names: "true"
      },
      request
    );
  }
  async deleteRow(table_id, row_id) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.DELETE,
      `/database/rows/table/${table_id}/${row_id}/`
    );
  }
  async getRow(table_id, row_id) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.GET,
      `/database/rows/table/${table_id}/${row_id}/`,
      {
        user_field_names: "true"
      }
    );
  }
  async listRows(table_id, page, limit, search, order_by, filters, advancedFilters) {
    let query = prepareQuery(__spreadValues({
      user_field_names: "true",
      page,
      size: limit,
      search,
      order_by
    }, filters));
    return advancedFilters && advancedFilters.filters.length > 0 && (query.filters = JSON.stringify(advancedFilters)), await this.makeRequest(
      import_pieces_common2.HttpMethod.GET,
      `/database/rows/table/${table_id}/`,
      query
    );
  }
  async batchCreateRows(table_id, items) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.POST,
      `/database/rows/table/${table_id}/batch/`,
      { user_field_names: "true" },
      { items }
    );
  }
  async batchUpdateRows(table_id, items) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.PATCH,
      `/database/rows/table/${table_id}/batch/`,
      { user_field_names: "true" },
      { items }
    );
  }
  async batchDeleteRows(table_id, ids) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.POST,
      `/database/rows/table/${table_id}/batch-delete/`,
      void 0,
      { items: ids }
    );
  }
  async listViews(table_id) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.GET,
      `/database/views/table/${table_id}/`,
      { type: "grid" }
    );
  }
  async aggregateField(view_id, field_id, aggregation_type) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.GET,
      `/database/views/grid/${view_id}/aggregation/${field_id}/`,
      { type: aggregation_type }
    );
  }
  async updateFieldSelectOptions({
    fieldId,
    existingOptions,
    newOptions
  }) {
    let palette = ["blue", "green", "orange", "red", "purple", "pink", "cyan", "yellow", "gray"], additions = newOptions.map((value, i) => ({
      value,
      color: palette[i % palette.length]
    }));
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.PATCH,
      `/database/fields/${fieldId}/`,
      void 0,
      { select_options: [...existingOptions, ...additions] }
    );
  }
  async createWebhook({
    tableId,
    url,
    events,
    name
  }) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.POST,
      `/database/webhooks/table/${tableId}/`,
      void 0,
      {
        url,
        events,
        name,
        request_method: "POST",
        use_user_field_names: !0
      }
    );
  }
  async deleteWebhook(webhookId) {
    await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.DELETE,
      url: `${this.baseUrl}/api/database/webhooks/${webhookId}/`,
      headers: { Authorization: this.authHeader }
    });
  }
  async uploadFileFromUrl({ url }) {
    return await this.makeRequest(
      import_pieces_common2.HttpMethod.POST,
      "/user-files/upload-via-url/",
      void 0,
      { url }
    );
  }
};

// ../ap/packages/pieces/community/baserow/src/lib/common/index.ts
async function makeClient(auth) {
  let { apiUrl, token, email, password } = auth.props;
  if (baserowAuthHelpers.isJwtAuth(auth)) {
    if (!email || !password)
      throw new Error(
        "Email and Password are required for JWT authentication. Update your Baserow connection."
      );
    let jwt = await BaserowClient.getJwtToken({ apiUrl, email, password });
    return new BaserowClient(apiUrl, `JWT ${jwt}`, !0);
  }
  if (!token)
    throw new Error(
      "Database Token is required for Database Token authentication. Update your Baserow connection."
    );
  return new BaserowClient(apiUrl, `Token ${token}`);
}
function formatFieldValues(input, fieldTypeMap, options) {
  let result = {};
  for (let key of Object.keys(input)) {
    let value = input[key], fieldType = fieldTypeMap[key];
    if (!(options.skipEmpty && (value == null || value === "" || Array.isArray(value) && value.length === 0)))
      switch (fieldType) {
        case "link_row" /* LINK_TO_TABLE */:
          Array.isArray(value) && value.length > 0 ? result[key] = value.map((id) => parseInt(id, 10)) : result[key] = [];
          break;
        case "multiple_collaborators" /* MULTIPLE_COLLABORATORS */:
          Array.isArray(value) && value.length > 0 ? result[key] = value.map((id) => ({ id: parseInt(id, 10) })) : result[key] = [];
          break;
        case "single_select" /* SINGLE_SELECT */:
          value == null || value === "" || Array.isArray(value) && value.length === 0 ? result[key] = options.skipEmpty ? void 0 : null : result[key] = value;
          break;
        case "multiple_select" /* MULTI_SELECT */:
          value == null || value === "" ? result[key] = options.skipEmpty ? void 0 : [] : result[key] = value;
          break;
        default:
          value == null ? result[key] = options.skipEmpty ? void 0 : null : result[key] = value;
          break;
      }
  }
  for (let key of Object.keys(result))
    result[key] === void 0 && delete result[key];
  return result;
}
async function ensureSelectOptionsExist({
  fields,
  payload,
  client
}) {
  for (let field of fields) {
    if (field.type !== "single_select" /* SINGLE_SELECT */ && field.type !== "multiple_select" /* MULTI_SELECT */)
      continue;
    let value = payload[field.name];
    if (value == null || value === "") continue;
    let requested = collectRequestedSelectValues(value);
    if (requested.length === 0) continue;
    let existingValues = new Set(field.select_options.map((o) => o.value)), missing = (0, import_pieces_framework3.unique)(requested.filter((v) => !existingValues.has(v)));
    if (missing.length === 0) continue;
    let result = await (0, import_pieces_framework3.tryCatch)(
      () => client.updateFieldSelectOptions({
        fieldId: field.id,
        existingOptions: field.select_options,
        newOptions: missing
      })
    );
    result.error && console.error(
      `[baserow] Failed to auto-create missing select options for field "${field.name}":`,
      result.error
    );
  }
}
function collectRequestedSelectValues(value) {
  return Array.isArray(value) ? value.filter((v) => typeof v == "string" && v.length > 0) : typeof value == "string" && value.length > 0 ? [value] : [];
}
var baserowCommon = {
  tableId: (required = !0) => import_pieces_framework2.Property.Dropdown({
    displayName: "Table",
    description: "Select the table.",
    required,
    auth: baserowAuth,
    refreshers: ["auth"],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await (await makeClient(auth)).listTables()).map((t) => ({ label: t.name, value: t.id }))
    } : {
      disabled: !0,
      placeholder: "Connect your account first.",
      options: []
    }
  }),
  rowId: (required = !0) => import_pieces_framework2.Property.Dropdown({
    displayName: "Row",
    description: "Select the row.",
    required,
    auth: baserowAuth,
    refreshers: ["auth", "table_id"],
    options: async ({ auth, table_id }) => !auth || typeof table_id != "number" ? {
      disabled: !0,
      placeholder: "Select a table first.",
      options: []
    } : {
      disabled: !1,
      options: (await (await makeClient(auth)).listRows(table_id, void 0, 200)).results.map((row) => {
        let primaryValue = Object.entries(row).filter(([k]) => k !== "id" && k !== "order").map(([, v]) => typeof v == "string" && v ? v : null).find(Boolean);
        return { label: primaryValue ? `#${row.id} ${primaryValue}` : `Row #${row.id}`, value: row.id };
      })
    }
  }),
  tableFields: (required = !0) => import_pieces_framework2.Property.DynamicProperties({
    auth: baserowAuth,
    displayName: "Table Fields",
    required,
    refreshers: ["table_id"],
    props: async ({ auth, table_id }) => {
      if (!auth || typeof table_id != "number") return {};
      let fields = {};
      try {
        let tableFields = await (await makeClient(auth)).listTableFields(table_id);
        for (let field of tableFields)
          if (!field.read_only && !["file" /* FILE */].includes(field.type))
            switch (field.type) {
              case "boolean" /* BOOLEAN */:
                fields[field.name] = import_pieces_framework2.Property.Checkbox({
                  displayName: field.name,
                  required: !1
                });
                break;
              case "rating" /* RATING */:
                fields[field.name] = import_pieces_framework2.Property.Number({
                  displayName: field.name,
                  required: !1,
                  description: `Enter valid value between 1 and ${field.max_value}.`
                });
                break;
              case "date" /* DATE */:
                fields[field.name] = import_pieces_framework2.Property.DateTime({
                  displayName: field.name,
                  required: !1,
                  description: `Enter date in ${field.date_format} format ${field.date_include_time ? "and time in " + field.date_time_format + " hour format" : ""}.`
                });
                break;
              case "duration" /* DURATION */:
                fields[field.name] = import_pieces_framework2.Property.Number({
                  displayName: field.name,
                  required: !1
                });
                break;
              case "link_row" /* LINK_TO_TABLE */:
                fields[field.name] = import_pieces_framework2.Property.Array({
                  displayName: field.name,
                  required: !1,
                  description: `Enter row ids from table(ID: ${field.link_row_table_id}) that you want to link to.`
                });
                break;
              case "long_text" /* LONG_TEXT */:
                fields[field.name] = import_pieces_framework2.Property.LongText({
                  displayName: field.name,
                  required: !1
                });
                break;
              case "multiple_collaborators" /* MULTIPLE_COLLABORATORS */:
                fields[field.name] = import_pieces_framework2.Property.Array({
                  displayName: field.name,
                  required: !1,
                  description: "Enter user ids that you want to link to."
                });
                break;
              case "single_select" /* SINGLE_SELECT */:
                fields[field.name] = import_pieces_framework2.Property.StaticDropdown({
                  displayName: field.name,
                  required: !1,
                  options: {
                    disabled: !1,
                    options: field.select_options.map((option) => ({
                      label: option.value,
                      value: option.value
                    }))
                  }
                });
                break;
              case "multiple_select" /* MULTI_SELECT */:
                fields[field.name] = import_pieces_framework2.Property.StaticMultiSelectDropdown({
                  displayName: field.name,
                  required: !1,
                  options: {
                    disabled: !1,
                    options: field.select_options.map((option) => ({
                      label: option.value,
                      value: option.value
                    }))
                  }
                });
                break;
              case "number" /* NUMBER */:
                fields[field.name] = import_pieces_framework2.Property.Number({
                  displayName: field.name,
                  required: !1
                });
                break;
              case "email" /* EMAIL */:
              case "phone_number" /* PHONE_NUMBER */:
                fields[field.name] = import_pieces_framework2.Property.ShortText({
                  displayName: field.name,
                  required: !1
                });
                break;
              case "text" /* TEXT */:
                fields[field.name] = import_pieces_framework2.Property.ShortText({
                  displayName: field.name,
                  required: !1,
                  defaultValue: field.text_default
                });
                break;
              case "url" /* URL */:
                fields[field.name] = import_pieces_framework2.Property.ShortText({
                  displayName: field.name,
                  required: !1
                });
                break;
            }
      } catch (error) {
        console.log("Invalid Baserow Table ID.");
      }
      return fields;
    }
  })
};

// ../ap/packages/pieces/community/baserow/src/lib/actions/create-row.ts
var createRowAction = (0, import_pieces_framework4.createAction)({
  name: "baserow_create_row",
  classification: "WRITE",
  displayName: "Create Row",
  description: "Creates a new row in a table.",
  audience: "both",
  aiMetadata: {
    description: "Creates one new row in a chosen Baserow table, setting field values by field name. Use to add a single record; for many rows at once use Batch Create Rows, and to avoid duplicates by a key field use Upsert Row. Not idempotent \u2014 each call inserts another row. Optionally auto-creates missing single/multi-select options referenced in the payload.",
    idempotent: !1
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    table_fields: baserowCommon.tableFields(!0),
    create_missing_select_options: import_pieces_framework4.Property.Checkbox({
      displayName: "Create missing select options",
      description: "When enabled, single/multi-select values that do not yet exist in the field will be added before creating the row. Existing options are preserved.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a;
    let table_id = context.propsValue.table_id, tableFieldsInput = context.propsValue.table_fields, createMissingSelectOptions = (_a = context.propsValue.create_missing_select_options) != null ? _a : !1, client = await makeClient(context.auth), tableSchema = await client.listTableFields(table_id), fieldTypeMap = {};
    for (let column of tableSchema)
      fieldTypeMap[column.name] = column.type;
    let formattedFields = formatFieldValues(tableFieldsInput, fieldTypeMap, {
      skipEmpty: !0
    });
    return createMissingSelectOptions && await ensureSelectOptionsExist({
      fields: tableSchema,
      payload: formattedFields,
      client
    }), await client.createRow(table_id, formattedFields);
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/actions/delete-row.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var deleteRowAction = (0, import_pieces_framework5.createAction)({
  name: "baserow_delete_row",
  classification: "DESTRUCTIVE",
  displayName: "Delete Row",
  description: "Deletes an existing row.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes one row from a Baserow table by its numeric row ID. Use to remove a single known record; for multiple rows use Batch Delete Rows. Destructive \u2014 confirm the row ID first (resolve via Find Row or List Rows if unknown). Not idempotent against changing data: once the row is gone a repeat call targets a no-longer-existing ID.",
    idempotent: !1
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    row_id: baserowCommon.rowId()
  },
  async run(context) {
    let { table_id, row_id } = context.propsValue;
    return await (await makeClient(context.auth)).deleteRow(table_id, row_id), { success: !0 };
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/actions/get-row.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getRowAction = (0, import_pieces_framework6.createAction)({
  name: "baserow_get_row",
  classification: "READ",
  displayName: "Get Row",
  description: "Gets a single row by its ID from a table.",
  audience: "both",
  aiMetadata: {
    description: "Fetches one row from a Baserow table by its numeric row ID. Use when you already know the exact row ID and want its full current field values; to locate a row by a field value instead, use Find Row. Read-only and idempotent.",
    idempotent: !0
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    row_id: baserowCommon.rowId()
  },
  async run(context) {
    let { table_id, row_id } = context.propsValue;
    return await (await makeClient(context.auth)).getRow(table_id, row_id);
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/actions/list-rows.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var listRowsAction = (0, import_pieces_framework7.createAction)({
  name: "baserow_list_rows",
  classification: "SEARCH",
  displayName: "List Rows",
  description: "Lists rows from a table with optional search, sorting, and filtering.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a page of rows from a Baserow table. With no search/filters it returns all rows (paginated); supply a free-text search, an order-by field, or structured field filters (each a JSON object with field ID, operator, and value, combined with AND/OR) to narrow results. Use to browse or query many rows; for a single row by ID use Get Row, or by one field value use Find Row. Read-only and idempotent.",
    idempotent: !0
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    page: import_pieces_framework7.Property.Number({
      displayName: "Page",
      required: !1,
      defaultValue: 1,
      description: "Page number to return. Defaults to 1."
    }),
    limit: import_pieces_framework7.Property.Number({
      displayName: "Page Size",
      required: !1,
      defaultValue: 100,
      description: "Number of rows to return per page. Maximum 200. Defaults to 100."
    }),
    search: import_pieces_framework7.Property.ShortText({
      displayName: "Search",
      required: !1,
      description: "Return only rows whose cell data matches this search term."
    }),
    order_by: import_pieces_framework7.Property.ShortText({
      displayName: "Order By",
      required: !1,
      description: "Field name to sort by. Prefix with **-** for descending or **+** for ascending. Example: `-Name` sorts by Name Z\u2192A."
    }),
    filter_type: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Filter Combination",
      description: "How to combine multiple filters. **AND** requires all filters to match; **OR** requires any one filter to match.",
      required: !1,
      defaultValue: "AND",
      options: {
        disabled: !1,
        options: [
          { label: "AND \u2014 all filters must match", value: "AND" },
          { label: "OR \u2014 any filter can match", value: "OR" }
        ]
      }
    }),
    filter_instructions: import_pieces_framework7.Property.MarkDown({
      value: '**How to add filters** (optional):\n\nEach filter is a JSON object with three keys:\n- `field` \u2014 numeric field ID (in Baserow, click the field header; the ID appears in the page URL)\n- `type` \u2014 operator: `equal`, `not_equal`, `contains`, `contains_not`, `higher_than`, `lower_than`, `is_empty`, `is_not_empty`\n- `value` \u2014 the value to compare against\n\nExample: `{"field": 123, "type": "equal", "value": "Active"}`'
    }),
    filters: import_pieces_framework7.Property.Array({
      displayName: "Filters",
      description: 'Each entry is a JSON object with "field" (numeric ID), "type" (operator), and "value". Leave empty to return all rows.',
      required: !1
    })
  },
  async run(context) {
    let { table_id, page, limit, search, order_by, filter_type, filters } = context.propsValue, client = await makeClient(context.auth), advancedFilters;
    if (filters && filters.length > 0) {
      let parsedFilters = filters.map((f) => {
        var _a, _b;
        let filter = typeof f == "string" ? JSON.parse(f) : f;
        return {
          field: Number(filter.field),
          type: String((_a = filter.type) != null ? _a : "equal"),
          value: String((_b = filter.value) != null ? _b : "")
        };
      });
      advancedFilters = {
        filter_type: filter_type != null ? filter_type : "AND",
        filters: parsedFilters
      };
    }
    let response = await client.listRows(
      table_id,
      page,
      limit,
      search,
      order_by,
      void 0,
      advancedFilters
    );
    return { count: response.count, rows: response.results };
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/actions/update-row.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var updateRowAction = (0, import_pieces_framework8.createAction)({
  name: "baserow_update_row",
  classification: "WRITE",
  displayName: "Update Row",
  description: "Updates fields in an existing row. Empty values are skipped. To clear a field, use Clean Row.",
  audience: "both",
  aiMetadata: {
    description: "Updates field values on one existing Baserow row identified by its numeric row ID. Only the fields you supply are changed and empty values are skipped, so it cannot clear a field \u2014 use Clean Row for that. Requires a known row ID (resolve one first with Find Row or List Rows if needed). Optionally auto-creates missing single/multi-select options. Idempotent: applying the same values to the same row leaves it in the same state.",
    idempotent: !0
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    row_id: baserowCommon.rowId(),
    table_fields: baserowCommon.tableFields(!0),
    create_missing_select_options: import_pieces_framework8.Property.Checkbox({
      displayName: "Create missing select options",
      description: "When enabled, single/multi-select values that do not yet exist in the field will be added before updating the row. Existing options are preserved.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a;
    let table_id = context.propsValue.table_id, row_id = context.propsValue.row_id, tableFieldsInput = context.propsValue.table_fields, createMissingSelectOptions = (_a = context.propsValue.create_missing_select_options) != null ? _a : !1, client = await makeClient(context.auth), tableSchema = await client.listTableFields(table_id), fieldTypeMap = {};
    for (let column of tableSchema)
      fieldTypeMap[column.name] = column.type;
    let formattedFields = formatFieldValues(tableFieldsInput, fieldTypeMap, {
      skipEmpty: !0
    });
    return createMissingSelectOptions && await ensureSelectOptionsExist({
      fields: tableSchema,
      payload: formattedFields,
      client
    }), await client.updateRow(table_id, row_id, formattedFields);
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/actions/find-row.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var findRowAction = (0, import_pieces_framework9.createAction)({
  name: "baserow_find_row",
  classification: "SEARCH",
  displayName: "Find Row",
  description: "Finds a row by matching a field value. Returns the first match.",
  audience: "both",
  aiMetadata: {
    description: "Looks up the first row in a Baserow table whose chosen field exactly equals a given value. Use to resolve a row from a known field value (e.g. an email or external key) when you do not have its row ID; for ID lookups use Get Row, and for multi-condition or partial-text queries use List Rows. The match field must be a filterable type (link, multi-select, file, formula/lookup, and date-type fields are excluded). Read-only and idempotent.",
    idempotent: !0
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    field_name: import_pieces_framework9.Property.Dropdown({
      displayName: "Field",
      description: "Select the field to search by.",
      required: !0,
      auth: baserowAuth,
      refreshers: ["auth", "table_id"],
      options: async ({ auth, table_id }) => {
        if (!auth || typeof table_id != "number")
          return {
            disabled: !0,
            placeholder: "Select a table first.",
            options: []
          };
        let fields = await (await makeClient(auth)).listTableFields(table_id), unsupportedTypes = [
          "link_row" /* LINK_TO_TABLE */,
          "multiple_select" /* MULTI_SELECT */,
          "multiple_collaborators" /* MULTIPLE_COLLABORATORS */,
          "file" /* FILE */,
          "rollup" /* ROLLUP */,
          "lookup" /* LOOKUP */,
          "count" /* COUNT */,
          "last_modified_by" /* LAST_MODIFIED_BY */,
          "created_by" /* CREATED_BY */,
          "date" /* DATE */,
          "last_modified" /* LAST_MODIFIED */,
          "created_on" /* CREATED_ON */,
          "duration" /* DURATION */
        ];
        return {
          disabled: !1,
          options: fields.filter(
            (f) => !unsupportedTypes.includes(f.type)
          ).map((f) => ({
            label: f.name,
            value: `field_${f.id}`
          }))
        };
      }
    }),
    field_value: import_pieces_framework9.Property.ShortText({
      displayName: "Field Value",
      description: "The value to search for (exact match).",
      required: !0
    })
  },
  async run(context) {
    let { table_id, field_name, field_value } = context.propsValue;
    if (!field_name || !field_value)
      return { found: !1, row: null, count: 0 };
    let response = await (await makeClient(context.auth)).listRows(
      table_id,
      void 0,
      1,
      void 0,
      void 0,
      { [`filter__${field_name}__equal`]: field_value }
    );
    return response.results.length === 0 ? { found: !1, count: 0 } : __spreadValues({ found: !0, count: response.count }, response.results[0]);
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/actions/clean-row.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var cleanRowAction = (0, import_pieces_framework10.createAction)({
  name: "baserow_clean_row",
  classification: "DESTRUCTIVE",
  displayName: "Clean Row",
  description: "Sets all fields in a row to empty/null. To update only specific fields, use Update Row instead.",
  audience: "both",
  aiMetadata: {
    description: "Clears the supplied fields on one existing Baserow row (identified by row ID) to empty/null, including blank values that Update Row would skip. Use specifically to wipe field contents rather than set new values; to change only some fields to new values use Update Row. Idempotent: re-clearing the same row leaves it in the same empty state.",
    idempotent: !0
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    row_id: baserowCommon.rowId(),
    table_fields: baserowCommon.tableFields(!0)
  },
  async run(context) {
    let { table_id, row_id } = context.propsValue, tableFieldsInput = context.propsValue.table_fields, client = await makeClient(context.auth), tableSchema = await client.listTableFields(table_id), fieldTypeMap = {};
    for (let column of tableSchema)
      fieldTypeMap[column.name] = column.type;
    let formattedFields = formatFieldValues(tableFieldsInput, fieldTypeMap, {
      skipEmpty: !1
    });
    return await client.updateRow(table_id, row_id, formattedFields);
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/actions/aggregate-field.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var aggregateFieldAction = (0, import_pieces_framework11.createAction)({
  name: "baserow_aggregate_field",
  classification: "READ",
  displayName: "Aggregate Field",
  description: "Calculates an aggregation (sum, average, min, max, count, etc.) over all values of a field in a grid view.",
  audience: "both",
  aiMetadata: {
    description: "Computes a single aggregate value (sum, average, min, max, median, std dev, variance, or empty/non-empty/unique counts) over one field across all rows of a Baserow grid view. Use for roll-up metrics over a whole view rather than fetching rows yourself; numeric aggregations require a number field. Requires a grid view ID and a field ID. Read-only and idempotent.",
    idempotent: !0
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    view_id: import_pieces_framework11.Property.Dropdown({
      displayName: "View",
      description: "Select the grid view to aggregate.",
      required: !0,
      auth: baserowAuth,
      refreshers: ["auth", "table_id"],
      options: async ({ auth, table_id }) => !auth || typeof table_id != "number" ? {
        disabled: !0,
        placeholder: "Select a table first.",
        options: []
      } : {
        disabled: !1,
        options: (await (await makeClient(auth)).listViews(table_id)).map((v) => ({ label: v.name, value: v.id }))
      }
    }),
    field_id: import_pieces_framework11.Property.Dropdown({
      displayName: "Field",
      description: "Select the field to aggregate.",
      required: !0,
      auth: baserowAuth,
      refreshers: ["auth", "table_id"],
      options: async ({ auth, table_id }) => !auth || typeof table_id != "number" ? {
        disabled: !0,
        placeholder: "Select a table first.",
        options: []
      } : {
        disabled: !1,
        options: (await (await makeClient(auth)).listTableFields(table_id)).map((f) => ({ label: f.name, value: f.id }))
      }
    }),
    aggregation_type: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Aggregation Type",
      description: "The calculation to run over the field. **Sum, Average, Min, Max, Median, Std Dev, and Variance** only work with number fields.",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Sum", value: "sum" },
          { label: "Average", value: "average" },
          { label: "Min", value: "min" },
          { label: "Max", value: "max" },
          { label: "Count (non-empty)", value: "not_empty_count" },
          { label: "Count (empty)", value: "empty_count" },
          { label: "Count (unique values)", value: "unique_count" },
          { label: "Median", value: "median" },
          { label: "Standard deviation", value: "std_dev" },
          { label: "Variance", value: "variance" }
        ]
      }
    })
  },
  async run(context) {
    let { view_id, field_id, aggregation_type } = context.propsValue;
    return { result: (await (await makeClient(context.auth)).aggregateField(view_id, field_id, aggregation_type)).value };
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/actions/batch-create-rows.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var batchCreateRowsAction = (0, import_pieces_framework12.createAction)({
  name: "baserow_batch_create_rows",
  classification: "WRITE",
  displayName: "Batch Create Rows",
  description: "Creates multiple rows in a single request. Accepts up to 200 rows.",
  audience: "both",
  aiMetadata: {
    description: "Creates many new rows in a Baserow table in one request from a JSON array of row objects (field names as keys), up to 200 rows per call. Use for bulk inserts instead of repeated Create Row calls. Not idempotent \u2014 each call appends another set of rows, so it can produce duplicates if re-run.",
    idempotent: !1
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    rows: import_pieces_framework12.Property.Json({
      displayName: "Rows",
      description: "A JSON array of objects. Each object represents a row with field names as keys.",
      required: !0,
      defaultValue: [{ Name: "Row 1" }, { Name: "Row 2" }]
    })
  },
  async run(context) {
    let table_id = context.propsValue.table_id, rows = context.propsValue.rows;
    if (!Array.isArray(rows))
      throw new Error("Rows must be a JSON array.");
    let response = await (await makeClient(context.auth)).batchCreateRows(table_id, rows);
    return { count: response.items.length, rows: response.items };
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/actions/batch-update-rows.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var batchUpdateRowsAction = (0, import_pieces_framework13.createAction)({
  name: "baserow_batch_update_rows",
  classification: "WRITE",
  displayName: "Batch Update Rows",
  description: 'Updates multiple rows in a single request. Each row must include an "id" field. Accepts up to 200 rows.',
  audience: "both",
  aiMetadata: {
    description: 'Updates many existing Baserow rows in one request from a JSON array, up to 200 rows per call. Every object must carry an "id" identifying the row plus the fields to change. Use for bulk edits to known rows instead of repeated Update Row calls. Idempotent: re-sending the same id/field values converges those rows to the same state.',
    idempotent: !0
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    rows: import_pieces_framework13.Property.Json({
      displayName: "Rows",
      description: 'A JSON array of objects. Each object must include an "id" field and the fields to update.',
      required: !0,
      defaultValue: [{ id: 1, Name: "Updated Row 1" }]
    })
  },
  async run(context) {
    let table_id = context.propsValue.table_id, rows = context.propsValue.rows;
    if (!Array.isArray(rows))
      throw new Error("Rows must be a JSON array.");
    let response = await (await makeClient(context.auth)).batchUpdateRows(table_id, rows);
    return { count: response.items.length, rows: response.items };
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/actions/batch-delete-rows.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var batchDeleteRowsAction = (0, import_pieces_framework14.createAction)({
  name: "baserow_batch_delete_rows",
  classification: "DESTRUCTIVE",
  displayName: "Batch Delete Rows",
  description: "Deletes multiple rows in a single request. Accepts up to 200 row IDs.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes many Baserow rows in one request from a list of numeric row IDs, up to 200 per call. Use for bulk removal of known rows; for a single row use Delete Row. Destructive \u2014 verify the IDs first (List Rows or Find Row can supply them). Not idempotent against changing data once the rows are removed.",
    idempotent: !1
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    row_ids: import_pieces_framework14.Property.Array({
      displayName: "Row IDs",
      description: "Numeric IDs of the rows to delete. You can get row IDs from the List Rows or Find Row actions.",
      required: !0
    })
  },
  async run(context) {
    let table_id = context.propsValue.table_id, row_ids = context.propsValue.row_ids;
    if (!Array.isArray(row_ids) || row_ids.length === 0)
      throw new Error("Row IDs must be a non-empty array.");
    let ids = row_ids.map((id) => parseInt(String(id), 10)).filter((id) => !isNaN(id));
    return await (await makeClient(context.auth)).batchDeleteRows(table_id, ids), { deleted_count: ids.length };
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/actions/upsert-row.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var upsertRowAction = (0, import_pieces_framework15.createAction)({
  name: "baserow_upsert_row",
  classification: "WRITE",
  displayName: "Upsert Row",
  description: "Creates a new row or updates an existing one by matching a field value.",
  audience: "both",
  aiMetadata: {
    description: "Inserts or updates a single Baserow row keyed on a match field value: if a row whose match field exactly equals the given value exists it is updated, otherwise a new row is created with that value. Use to sync records idempotently without creating duplicates. The match field must be a writable, filterable type (read-only, link, select, file, date, and auto-number fields are excluded). Idempotent on the match key \u2014 repeating the same call converges the row to the same state rather than adding rows.",
    idempotent: !0
  },
  auth: baserowAuth,
  props: {
    table_id: baserowCommon.tableId(),
    match_field: import_pieces_framework15.Property.Dropdown({
      displayName: "Match Field",
      description: "Select the field to search for an existing row.",
      required: !0,
      auth: baserowAuth,
      refreshers: ["auth", "table_id"],
      options: async ({ auth, table_id }) => {
        if (!auth || typeof table_id != "number")
          return {
            disabled: !0,
            placeholder: "Select a table first.",
            options: []
          };
        let fields = await (await makeClient(auth)).listTableFields(table_id), unsupportedTypes = [
          "link_row" /* LINK_TO_TABLE */,
          "multiple_select" /* MULTI_SELECT */,
          "multiple_collaborators" /* MULTIPLE_COLLABORATORS */,
          "file" /* FILE */,
          "rollup" /* ROLLUP */,
          "lookup" /* LOOKUP */,
          "count" /* COUNT */,
          "last_modified_by" /* LAST_MODIFIED_BY */,
          "created_by" /* CREATED_BY */,
          "date" /* DATE */,
          "last_modified" /* LAST_MODIFIED */,
          "created_on" /* CREATED_ON */,
          "duration" /* DURATION */,
          "uuid" /* UUID */,
          "autonumber" /* AUTO_NUMBER */
        ];
        return {
          disabled: !1,
          options: fields.filter((f) => !f.read_only && !unsupportedTypes.includes(f.type)).map((f) => ({ label: f.name, value: `field_${f.id}` }))
        };
      }
    }),
    match_value: import_pieces_framework15.Property.ShortText({
      displayName: "Match Value",
      description: "The value to search for (exact match). If a row with this value is found it will be updated; otherwise a new row is created.",
      required: !0
    }),
    table_fields: baserowCommon.tableFields(!0),
    create_missing_select_options: import_pieces_framework15.Property.Checkbox({
      displayName: "Create missing select options",
      description: "When enabled, single/multi-select values that do not yet exist in the field will be added before creating or updating the row. Existing options are preserved.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { table_id, match_field, match_value, table_fields, create_missing_select_options } = context.propsValue, client = await makeClient(context.auth), tableSchema = await client.listTableFields(table_id), fieldTypeMap = {};
    for (let column of tableSchema)
      fieldTypeMap[column.name] = column.type;
    let formattedFields = formatFieldValues(table_fields, fieldTypeMap, {
      skipEmpty: !0
    }), matchFieldId = parseInt(match_field.replace("field_", ""), 10), matchFieldDef = tableSchema.find((f) => f.id === matchFieldId), existing = await client.listRows(
      table_id,
      void 0,
      1,
      void 0,
      void 0,
      { [`filter__${match_field}__equal`]: match_value }
    );
    if (existing.results.length > 0) {
      matchFieldDef && delete formattedFields[matchFieldDef.name], create_missing_select_options && await ensureSelectOptionsExist({
        fields: tableSchema,
        payload: formattedFields,
        client
      });
      let rowId = existing.results[0].id;
      return { action: "updated", row: await client.updateRow(table_id, rowId, formattedFields) };
    }
    return matchFieldDef && (formattedFields[matchFieldDef.name] = coerceMatchValue({
      value: match_value,
      fieldType: matchFieldDef.type
    })), create_missing_select_options && await ensureSelectOptionsExist({
      fields: tableSchema,
      payload: formattedFields,
      client
    }), { action: "created", row: await client.createRow(table_id, formattedFields) };
  }
});
function coerceMatchValue({
  value,
  fieldType
}) {
  switch (fieldType) {
    case "boolean" /* BOOLEAN */: {
      let normalized = value.trim().toLowerCase();
      return ["true", "1", "yes"].includes(normalized);
    }
    case "number" /* NUMBER */:
    case "rating" /* RATING */: {
      let parsed = Number(value);
      if (Number.isNaN(parsed))
        throw new Error(
          `Match Value "${value}" is not a valid number for the selected ${fieldType} field.`
        );
      return parsed;
    }
    default:
      return value;
  }
}

// ../ap/packages/pieces/community/baserow/src/lib/actions/upload-file.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var uploadFileAction = (0, import_pieces_framework16.createAction)({
  name: "baserow_upload_file",
  classification: "WRITE",
  displayName: "Upload File",
  description: "Uploads a file to Baserow from a URL. Returns the uploaded file object that can be used in file fields. Requires Email & Password (JWT) authentication \u2014 Database Tokens do not have access to the user-files endpoint.",
  audience: "both",
  aiMetadata: {
    description: "Downloads a file from a public URL and uploads it into Baserow user files, producing a file reference you can then assign to a file field via Create/Update Row. Use as the first step before attaching files to rows. Requires Email & Password (JWT) authentication \u2014 Database Token connections cannot reach the user-files endpoint. Not idempotent \u2014 each call uploads a new file copy.",
    idempotent: !1
  },
  auth: baserowAuth,
  props: {
    url: import_pieces_framework16.Property.ShortText({
      displayName: "File URL",
      description: "The public URL of the file to upload to Baserow.",
      required: !0
    })
  },
  async run(context) {
    if (!baserowAuthHelpers.isJwtAuth(context.auth))
      throw new Error(
        "Upload File requires Email & Password (JWT) authentication. Database Tokens are limited to row CRUD operations and cannot access the user-files endpoint. Please create a new Baserow connection using Email & Password."
      );
    let { url } = context.propsValue;
    return await (await makeClient(context.auth)).uploadFileFromUrl({ url });
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/triggers/row-created.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/baserow/src/lib/common/webhook-trigger.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_framework18 = require("@activepieces/pieces-framework");
function dynamicWebhookInstructions(eventLabel) {
  return import_pieces_framework18.Property.DynamicProperties({
    auth: baserowAuth,
    displayName: "Webhook Setup",
    required: !1,
    refreshers: ["auth"],
    props: async ({ auth }) => auth && baserowAuthHelpers.isJwtAuth(auth) ? {
      info: import_pieces_framework18.Property.MarkDown({
        value: "\u2705 **Webhook auto-registered** \u2014 no manual setup needed. The webhook is created and removed automatically when you enable or disable this trigger.",
        variant: import_pieces_framework17.MarkdownVariant.INFO
      })
    } : {
      info: import_pieces_framework18.Property.MarkDown({
        value: `**Manual webhook setup required** (Database Token auth):

1. In Baserow, click the **\xB7\xB7\xB7** menu beside your table and select **Webhooks**.
2. Click **Create webhook +**.
3. Set the HTTP method to **POST**.
4. Paste this URL into the endpoint field:
\`\`\`
{{webhookUrl}}
\`\`\`
5. Under **Events**, select **${eventLabel}**.
6. Click **Save**.`,
        variant: import_pieces_framework17.MarkdownVariant.INFO
      })
    }
  });
}
function createWebhookTriggerHooks({
  events,
  storeKey
}) {
  return {
    async onEnable(context) {
      if (!baserowAuthHelpers.isJwtAuth(context.auth) || !context.propsValue.table_id) return;
      let webhook = await (await makeClient(context.auth)).createWebhook({
        tableId: context.propsValue.table_id,
        url: context.webhookUrl,
        events,
        name: `Activepieces \u2013 ${storeKey}`
      });
      await context.store.put(storeKey, { webhookId: webhook.id });
    },
    async onDisable(context) {
      if (!baserowAuthHelpers.isJwtAuth(context.auth)) return;
      let data = await context.store.get(storeKey);
      if (!(data != null && data.webhookId)) return;
      let client = await makeClient(context.auth);
      try {
        await client.deleteWebhook(data.webhookId);
      } catch (e) {
      }
      await context.store.delete(storeKey);
    }
  };
}

// ../ap/packages/pieces/community/baserow/src/lib/triggers/row-created.ts
var triggerHooks = createWebhookTriggerHooks({
  events: ["rows.created"],
  storeKey: "baserow_row_created_trigger"
}), rowCreatedTrigger = (0, import_pieces_framework19.createTrigger)({
  name: "baserow_row_created",
  classification: "READ",
  auth: baserowAuth,
  displayName: "New Row",
  description: "Triggers when a new row is created in a Baserow table.",
  aiMetadata: {
    description: "Fires when a new row is added to the selected Baserow table, emitting one event per created row. Use to react to individual new records; for the batch form use New Rows (Batch)."
  },
  type: import_pieces_framework19.TriggerStrategy.WEBHOOK,
  props: {
    table_id: baserowCommon.tableId(),
    instructions: dynamicWebhookInstructions("Rows created")
  },
  sampleData: {
    id: 1,
    order: "1.00000000000000000000",
    Name: "Example row"
  },
  onEnable: triggerHooks.onEnable,
  onDisable: triggerHooks.onDisable,
  async run(context) {
    var _a;
    return (_a = context.payload.body.items) != null ? _a : [];
  },
  async test(context) {
    let tableId = context.propsValue.table_id;
    return tableId ? (await (await makeClient(context.auth)).listRows(tableId, 1, 5)).results : [];
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/triggers/row-updated.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var triggerHooks2 = createWebhookTriggerHooks({
  events: ["rows.updated"],
  storeKey: "baserow_row_updated_trigger"
}), rowUpdatedTrigger = (0, import_pieces_framework20.createTrigger)({
  name: "baserow_row_updated",
  classification: "READ",
  auth: baserowAuth,
  displayName: "Updated Row",
  description: "Triggers when an existing row is updated in a Baserow table.",
  aiMetadata: {
    description: "Fires when an existing row in the selected Baserow table changes, emitting one event per updated row with both its new and previous field values. Use to react to individual edits; for the batch form use Updated Rows (Batch)."
  },
  type: import_pieces_framework20.TriggerStrategy.WEBHOOK,
  props: {
    table_id: baserowCommon.tableId(),
    instructions: dynamicWebhookInstructions("Rows updated")
  },
  sampleData: {
    row: {
      id: 1,
      order: "1.00000000000000000000",
      Name: "Updated row"
    },
    previous: {
      id: 1,
      order: "1.00000000000000000000",
      Name: "Original row"
    }
  },
  onEnable: triggerHooks2.onEnable,
  onDisable: triggerHooks2.onDisable,
  async run(context) {
    var _a;
    let body = context.payload.body;
    return ((_a = body.items) != null ? _a : []).map((item, i) => {
      var _a2, _b;
      return {
        row: item,
        previous: (_b = ((_a2 = body.old_items) != null ? _a2 : [])[i]) != null ? _b : null
      };
    }).filter(({ row, previous }) => previous ? JSON.stringify(row) !== JSON.stringify(previous) : !0);
  },
  async test(context) {
    let tableId = context.propsValue.table_id;
    return tableId ? (await (await makeClient(context.auth)).listRows(tableId, 1, 5)).results.map((row) => ({ row, previous: null })) : [];
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/triggers/row-deleted.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var triggerHooks3 = createWebhookTriggerHooks({
  events: ["rows.deleted"],
  storeKey: "baserow_row_deleted_trigger"
}), rowDeletedTrigger = (0, import_pieces_framework21.createTrigger)({
  name: "baserow_row_deleted",
  classification: "READ",
  auth: baserowAuth,
  displayName: "Deleted Row",
  description: "Triggers when a row is deleted from a Baserow table.",
  aiMetadata: {
    description: "Fires when a row is removed from the selected Baserow table, emitting one event per deleted row carrying its ID. Use to react to individual deletions; for the batch form use Deleted Rows (Batch)."
  },
  type: import_pieces_framework21.TriggerStrategy.WEBHOOK,
  props: {
    table_id: baserowCommon.tableId(),
    instructions: dynamicWebhookInstructions("Rows deleted")
  },
  sampleData: {
    id: 1
  },
  onEnable: triggerHooks3.onEnable,
  onDisable: triggerHooks3.onDisable,
  async run(context) {
    var _a;
    return ((_a = context.payload.body.row_ids) != null ? _a : []).map((id) => ({ id }));
  },
  async test(context) {
    let tableId = context.propsValue.table_id;
    return tableId ? (await (await makeClient(context.auth)).listRows(tableId, 1, 5)).results.map((row) => ({ id: row.id })) : [];
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/triggers/rows-created.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var triggerHooks4 = createWebhookTriggerHooks({
  events: ["rows.created"],
  storeKey: "baserow_rows_created_trigger"
}), rowsCreatedTrigger = (0, import_pieces_framework22.createTrigger)({
  name: "baserow_rows_created",
  classification: "READ",
  auth: baserowAuth,
  displayName: "New Rows (Batch)",
  description: "Triggers when new rows are created in a Baserow table. Returns all rows from the event as a single batch.",
  aiMetadata: {
    description: "Fires when one or more new rows are created in the selected Baserow table, delivering all rows from a single create event together as one batch. Use to process bulk inserts at once; for one event per row use New Row."
  },
  type: import_pieces_framework22.TriggerStrategy.WEBHOOK,
  props: {
    table_id: baserowCommon.tableId(),
    instructions: dynamicWebhookInstructions("Rows created")
  },
  sampleData: {
    rows: [
      { id: 1, order: "1.00000000000000000000", Name: "Row 1" },
      { id: 2, order: "2.00000000000000000000", Name: "Row 2" }
    ],
    count: 2
  },
  onEnable: triggerHooks4.onEnable,
  onDisable: triggerHooks4.onDisable,
  async run(context) {
    var _a;
    let rows = (_a = context.payload.body.items) != null ? _a : [];
    return [{ rows, count: rows.length }];
  },
  async test(context) {
    let tableId = context.propsValue.table_id;
    if (!tableId) return [];
    let response = await (await makeClient(context.auth)).listRows(tableId, 1, 5);
    return [{ rows: response.results, count: response.results.length }];
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/triggers/rows-updated.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var triggerHooks5 = createWebhookTriggerHooks({
  events: ["rows.updated"],
  storeKey: "baserow_rows_updated_trigger"
}), rowsUpdatedTrigger = (0, import_pieces_framework23.createTrigger)({
  name: "baserow_rows_updated",
  classification: "READ",
  auth: baserowAuth,
  displayName: "Updated Rows (Batch)",
  description: "Triggers when existing rows are updated in a Baserow table. Returns all rows from the event as a single batch.",
  aiMetadata: {
    description: "Fires when one or more existing rows are updated in the selected Baserow table, delivering all changed rows (each with new and previous values) from a single update event together as one batch. Use to process bulk edits at once; for one event per row use Updated Row."
  },
  type: import_pieces_framework23.TriggerStrategy.WEBHOOK,
  props: {
    table_id: baserowCommon.tableId(),
    instructions: dynamicWebhookInstructions("Rows updated")
  },
  sampleData: {
    rows: [
      {
        row: { id: 1, order: "1.00000000000000000000", Name: "Updated Row 1" },
        previous: { id: 1, order: "1.00000000000000000000", Name: "Original Row 1" }
      }
    ],
    count: 1
  },
  onEnable: triggerHooks5.onEnable,
  onDisable: triggerHooks5.onDisable,
  async run(context) {
    var _a;
    let body = context.payload.body, rows = ((_a = body.items) != null ? _a : []).map((item, i) => {
      var _a2, _b;
      return {
        row: item,
        previous: (_b = ((_a2 = body.old_items) != null ? _a2 : [])[i]) != null ? _b : null
      };
    });
    return [{ rows, count: rows.length }];
  },
  async test(context) {
    let tableId = context.propsValue.table_id;
    if (!tableId) return [];
    let rows = (await (await makeClient(context.auth)).listRows(tableId, 1, 5)).results.map((row) => ({ row, previous: null }));
    return [{ rows, count: rows.length }];
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/triggers/rows-deleted.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework");
var triggerHooks6 = createWebhookTriggerHooks({
  events: ["rows.deleted"],
  storeKey: "baserow_rows_deleted_trigger"
}), rowsDeletedTrigger = (0, import_pieces_framework24.createTrigger)({
  name: "baserow_rows_deleted",
  classification: "READ",
  auth: baserowAuth,
  displayName: "Deleted Rows (Batch)",
  description: "Triggers when rows are deleted from a Baserow table. Returns all deleted row IDs from the event as a single batch.",
  aiMetadata: {
    description: "Fires when one or more rows are deleted from the selected Baserow table, delivering all deleted row IDs from a single delete event together as one batch. Use to process bulk deletions at once; for one event per row use Deleted Row."
  },
  type: import_pieces_framework24.TriggerStrategy.WEBHOOK,
  props: {
    table_id: baserowCommon.tableId(),
    instructions: dynamicWebhookInstructions("Rows deleted")
  },
  sampleData: {
    rows: [{ id: 1 }, { id: 2 }],
    count: 2
  },
  onEnable: triggerHooks6.onEnable,
  onDisable: triggerHooks6.onDisable,
  async run(context) {
    var _a;
    let rows = ((_a = context.payload.body.row_ids) != null ? _a : []).map((id) => ({ id }));
    return [{ rows, count: rows.length }];
  },
  async test(context) {
    let tableId = context.propsValue.table_id;
    if (!tableId) return [];
    let rows = (await (await makeClient(context.auth)).listRows(tableId, 1, 5)).results.map((row) => ({ id: row.id }));
    return [{ rows, count: rows.length }];
  }
});

// ../ap/packages/pieces/community/baserow/src/lib/triggers/row-event.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework");
var triggerHooks7 = createWebhookTriggerHooks({
  events: ["rows.created", "rows.updated", "rows.deleted"],
  storeKey: "baserow_row_event_trigger"
}), rowEventTrigger = (0, import_pieces_framework25.createTrigger)({
  name: "baserow_row_event",
  classification: "READ",
  auth: baserowAuth,
  displayName: "Any Row Change",
  description: "Triggers when a row is created, updated, or deleted in a Baserow table. To react to only one event type, use the dedicated New Row, Updated Row, or Deleted Row triggers.",
  aiMetadata: {
    description: "Fires on any row change \u2014 create, update, or delete \u2014 in the selected Baserow table, tagging each event with its event_type. Use when one flow should handle all row lifecycle events; for a single event type use the dedicated New Row, Updated Row, or Deleted Row triggers."
  },
  type: import_pieces_framework25.TriggerStrategy.WEBHOOK,
  props: {
    table_id: baserowCommon.tableId(),
    instructions: dynamicWebhookInstructions("Rows created, Rows updated, and Rows deleted")
  },
  sampleData: {
    event_type: "rows.created",
    row: { id: 1, order: "1.00000000000000000000", Name: "Example row" },
    previous_row: null
  },
  onEnable: triggerHooks7.onEnable,
  onDisable: triggerHooks7.onDisable,
  async run(context) {
    var _a, _b, _c;
    let body = context.payload.body, eventType = body.event_type;
    return eventType === "rows.created" ? ((_a = body.items) != null ? _a : []).map((row) => ({
      event_type: eventType,
      row,
      previous_row: null
    })) : eventType === "rows.updated" ? ((_b = body.items) != null ? _b : []).map((row, i) => {
      var _a2, _b2;
      return {
        event_type: eventType,
        row,
        previous_row: (_b2 = ((_a2 = body.old_items) != null ? _a2 : [])[i]) != null ? _b2 : null
      };
    }) : eventType === "rows.deleted" ? ((_c = body.row_ids) != null ? _c : []).map((id) => ({
      event_type: eventType,
      row: { id },
      previous_row: null
    })) : [];
  },
  async test(context) {
    let tableId = context.propsValue.table_id;
    return tableId ? (await (await makeClient(context.auth)).listRows(tableId, 1, 5)).results.map((row) => ({
      event_type: "rows.created",
      row,
      previous_row: null
    })) : [];
  }
});

// ../ap/packages/pieces/community/baserow/src/index.ts
async function buildCustomApiAuthHeader(auth) {
  let { apiUrl, token, email, password } = auth.props;
  if (baserowAuthHelpers.isJwtAuth(auth)) {
    if (!email || !password)
      throw new Error("Email and Password are required for JWT authentication.");
    return { Authorization: `JWT ${await BaserowClient.getJwtToken({ apiUrl, email, password })}` };
  }
  if (!token)
    throw new Error("Database Token is required for Database Token authentication.");
  return { Authorization: `Token ${token}` };
}
var baserow = (0, import_pieces_framework26.createPiece)({
  displayName: "Baserow",
  description: "Open-source online database tool, alternative to Airtable",
  auth: baserowAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/baserow.png",
  categories: [import_pieces_framework27.PieceCategory.PRODUCTIVITY],
  authors: ["kishanprmr", "MoShizzle", "abuaboud", "bst1n", "sanket-a11y", "onyedikachi-david"],
  actions: [
    createRowAction,
    getRowAction,
    listRowsAction,
    findRowAction,
    updateRowAction,
    upsertRowAction,
    deleteRowAction,
    cleanRowAction,
    batchCreateRowsAction,
    batchUpdateRowsAction,
    batchDeleteRowsAction,
    aggregateFieldAction,
    uploadFileAction,
    (0, import_pieces_common3.createCustomApiCallAction)({
      baseUrl: (auth) => auth ? auth.props.apiUrl : "",
      auth: baserowAuth,
      authMapping: buildCustomApiAuthHeader
    })
  ],
  triggers: [
    rowCreatedTrigger,
    rowUpdatedTrigger,
    rowDeletedTrigger,
    rowEventTrigger,
    rowsCreatedTrigger,
    rowsUpdatedTrigger,
    rowsDeletedTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  baserow
});
