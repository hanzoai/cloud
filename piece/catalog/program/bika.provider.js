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

// ../ap/packages/pieces/community/bika/src/index.ts
var index_exports = {};
__export(index_exports, {
  bika: () => bika
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bika/src/lib/actions/create-record.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bika/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bika/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var BikaAuth = import_pieces_framework.PieceAuth.CustomAuth({
  required: !0,
  description: `
    To obtain your Bika token, follow these steps:

    1. Log in to your Bika account.
    2. Visit https://bika.com.
    3. Click on your profile picture (Bottom left).
    4. Click on "My Settings".
    5. Click on "Developer".
    6. Click on "Generate new token".
    7. Copy the token.
    `,
  props: {
    token: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Token",
      description: "The token of the Bika account",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await makeClient(
        auth
      ).listSpaces(), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid Token."
      };
    }
  }
});

// ../ap/packages/pieces/community/bika/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
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
var BikaClient = class {
  constructor(token, bikaUrl = "https://bika.ai") {
    __publicField(this, "token", token);
    __publicField(this, "bikaUrl", bikaUrl);
  }
  async makeRequest(method, resourceUri, query, body = void 0) {
    let baseUrl = this.bikaUrl.replace(/\/$/, "");
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${baseUrl}/api/openapi/bika` + resourceUri,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.token
      },
      queryParams: query,
      body
    })).body;
  }
  async listSpaces() {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/v1/spaces");
  }
  async listDatabases(space_id) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v1/spaces/${space_id}/nodes`);
  }
  async getDatabaseFields(space_id, database_id) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v1/spaces/${space_id}/resources/databases/${database_id}/fields`);
  }
  async createRecord(space_id, database_id, request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      `/v2/spaces/${space_id}/resources/databases/${database_id}/records`,
      void 0,
      request
    );
  }
  async deleteRecord(space_id, database_id, record_id) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.DELETE,
      `/v2/spaces/${space_id}/resources/databases/${database_id}/records/${record_id}`
    );
  }
  async updateRecord(space_id, database_id, record_id, request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      `/v2/spaces/${space_id}/resources/databases/${database_id}/records/${record_id}`,
      void 0,
      request
    );
  }
  async findRecord(space_id, database_id, record_id, query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v2/spaces/${space_id}/resources/databases/${database_id}/records/${record_id}`, query);
  }
  async listRecords(space_id, database_id, query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/v2/spaces/${space_id}/resources/databases/${database_id}/records`, query);
  }
};

// ../ap/packages/pieces/community/bika/src/lib/common/constants.ts
var BikaNumericFieldTypes = [
  "NUMBER" /* NUMBER */,
  "RATING" /* RATING */,
  "CURRENCY" /* CURRENCY */,
  "PERCENT" /* PERCENT */
];

// ../ap/packages/pieces/community/bika/src/lib/common/index.ts
function makeClient(auth) {
  return new BikaClient(auth.token);
}
var BikaCommon = {
  space_id: import_pieces_framework2.Property.Dropdown({
    auth: BikaAuth,
    displayName: "Space",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient(auth.props).listSpaces()).data.map((space) => ({
        label: space.name,
        value: space.id
      }))
    } : {
      disabled: !0,
      options: [],
      placeholder: "Connect your account first."
    }
  }),
  database_id: import_pieces_framework2.Property.Dropdown({
    auth: BikaAuth,
    displayName: "Database",
    required: !0,
    refreshers: ["space_id"],
    options: async ({ auth, space_id }) => !auth || !space_id ? {
      disabled: !0,
      options: [],
      placeholder: "Connect your account first and select space."
    } : {
      disabled: !1,
      options: (await makeClient(auth.props).listDatabases(space_id)).data.map((database) => ({
        label: database.name,
        value: database.id
      }))
    }
  }),
  fields: import_pieces_framework2.Property.DynamicProperties({
    auth: BikaAuth,
    displayName: "Fields",
    description: "The fields to add to the record.",
    required: !0,
    refreshers: ["auth", "space_id", "database_id"],
    props: async ({ auth, space_id, database_id }) => {
      var _a, _b, _c, _d, _e, _f;
      if (!auth || !space_id || !database_id) return {};
      let res = await makeClient(auth.props).getDatabaseFields(space_id, database_id), props = {};
      for (let field of res.data)
        if (![
          "AUTONUMBER" /* AUTONUMBER */,
          "CASCADER" /* CASCADER */,
          "CREATED_BY" /* CREATED_BY */,
          "CREATED_TIME" /* CREATED_TIME */,
          "FORMULA" /* FORMULA */,
          "LAST_MODIFIED_BY" /* LAST_MODIFIED_BY */,
          "LAST_MODIFIED_TIME" /* LAST_MODIFIED_TIME */,
          "LOOKUP" /* LOOKUP */
        ].includes(field.type))
          switch (field.type) {
            case "ATTACHMENT" /* ATTACHMENT */:
              props[field.name] = import_pieces_framework2.Property.File({
                displayName: field.name,
                required: !1
              });
              break;
            case "CHECKBOX" /* CHECKBOX */:
              props[field.name] = import_pieces_framework2.Property.Checkbox({
                displayName: field.name,
                required: !1
              });
              break;
            case "CURRENCY" /* CURRENCY */:
            case "NUMBER" /* NUMBER */:
            case "PERCENT" /* PERCENT */:
            case "RATING" /* RATING */:
              props[field.name] = import_pieces_framework2.Property.Number({
                displayName: field.name,
                required: !1
              });
              break;
            case "DATETIME" /* DATETIME */:
              props[field.name] = import_pieces_framework2.Property.DateTime({
                displayName: field.name,
                required: !1
              });
              break;
            case "EMAIL" /* EMAIL */:
            case "PHONE" /* PHONE */:
            case "SINGLE_TEXT" /* SINGLE_TEXT */:
            case "URL" /* URL */:
              props[field.name] = import_pieces_framework2.Property.ShortText({
                displayName: field.name,
                required: !1
              });
              break;
            case "LONG_TEXT" /* LONG_TEXT */:
              props[field.name] = import_pieces_framework2.Property.LongText({
                displayName: field.name,
                required: !1
              });
              break;
            case "MULTI_SELECT" /* MULTI_SELECT */:
              props[field.name] = import_pieces_framework2.Property.StaticMultiSelectDropdown({
                displayName: field.name,
                required: !1,
                options: {
                  options: ((_b = (_a = field.property) == null ? void 0 : _a.options) == null ? void 0 : _b.map((option) => ({
                    label: option.name,
                    value: option.name
                  }))) || []
                }
              });
              break;
            case "SINGLE_SELECT" /* SINGLE_SELECT */:
              props[field.name] = import_pieces_framework2.Property.StaticDropdown({
                displayName: field.name,
                required: !1,
                options: {
                  options: ((_d = (_c = field.property) == null ? void 0 : _c.options) == null ? void 0 : _d.map((option) => ({
                    label: option.name,
                    value: option.name
                  }))) || []
                }
              });
              break;
            case "MEMBER" /* MEMBER */:
              props[field.name] = import_pieces_framework2.Property.StaticMultiSelectDropdown({
                displayName: field.name,
                required: !1,
                options: {
                  options: ((_f = (_e = field.property) == null ? void 0 : _e.options) == null ? void 0 : _f.map((option) => ({
                    label: option.name,
                    value: option.id
                  }))) || []
                }
              });
              break;
            case "LINK" /* LINK */:
              props[field.name] = import_pieces_framework2.Property.Array({
                displayName: field.name,
                required: !1
              });
              break;
          }
      return props;
    }
  })
};
async function createNewFields(auth, space_id, database_id, fields) {
  var _a, _b;
  if (!auth || !database_id) return fields;
  let newFields = {}, res = await makeClient(auth.props).getDatabaseFields(space_id, database_id);
  for (let field of res.data) {
    if ([
      "AUTONUMBER" /* AUTONUMBER */,
      "CASCADER" /* CASCADER */,
      "CREATED_BY" /* CREATED_BY */,
      "CREATED_TIME" /* CREATED_TIME */,
      "FORMULA" /* FORMULA */,
      "LAST_MODIFIED_TIME" /* LAST_MODIFIED_TIME */,
      "LAST_MODIFIED_BY" /* LAST_MODIFIED_BY */,
      "LOOKUP" /* LOOKUP */
    ].includes(field.type) || !(field.name in fields))
      continue;
    let key = field.name;
    if (BikaNumericFieldTypes.includes(field.type))
      newFields[key] = Number(fields[key]);
    else if (field.type === "MEMBER" /* MEMBER */)
      newFields[key] = (_b = (_a = field.property) == null ? void 0 : _a.options) == null ? void 0 : _b.filter(
        (member) => member.id === `${fields[key]}`
      );
    else if (["MULTI_SELECT" /* MULTI_SELECT */].includes(field.type)) {
      if (!Array.isArray(fields[key]) || fields[key].length === 0)
        continue;
      newFields[key] = fields[key];
    } else
      newFields[key] = fields[key];
  }
  return newFields;
}

// ../ap/packages/pieces/community/bika/src/lib/actions/create-record.ts
var createRecordAction = (0, import_pieces_framework3.createAction)({
  auth: BikaAuth,
  name: "bika_create_record",
  classification: "WRITE",
  displayName: "Create Record",
  description: "Creates a new record in database.",
  audience: "both",
  aiMetadata: { description: "Creates a new record in a Bika.ai database table, populating its fields. Requires a space and database (table) to target; field values must match the database schema (read-only field types like formulas and autonumber are ignored). Not idempotent: each call appends a new record.", idempotent: !1 },
  props: {
    space_id: BikaCommon.space_id,
    database_id: BikaCommon.database_id,
    fields: BikaCommon.fields
  },
  async run(context) {
    let auth = context.auth, databaseId = context.propsValue.database_id, spaceId = context.propsValue.space_id, dynamicFields = context.propsValue.fields, fields = {}, props = Object.entries(dynamicFields);
    for (let [propertyKey, propertyValue] of props)
      propertyValue !== void 0 && propertyValue !== "" && (fields[propertyKey] = propertyValue);
    let newFields = await createNewFields(
      auth,
      spaceId,
      databaseId,
      fields
    ), response = await makeClient(
      context.auth.props
    ).createRecord(spaceId, databaseId, {
      records: [
        {
          fields: __spreadValues({}, newFields)
        }
      ]
    });
    if (!response.success)
      throw new Error(JSON.stringify(response, void 0, 2));
    return response;
  }
});

// ../ap/packages/pieces/community/bika/src/lib/actions/find-records.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var findRecordsAction = (0, import_pieces_framework4.createAction)({
  auth: BikaAuth,
  name: "bika_find_records",
  classification: "SEARCH",
  displayName: "Find Records",
  description: "Finds records in database.",
  audience: "both",
  aiMetadata: { description: "Lists records from a Bika.ai database, optionally narrowed by a filter expression (Bika filter-query-language); with no filter it returns all records up to the configured limits. Use to search or page through a table when you need multiple matching rows rather than a single known ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    space_id: BikaCommon.space_id,
    database_id: BikaCommon.database_id,
    maxRecords: import_pieces_framework4.Property.Number({
      displayName: "Max Records",
      description: "How many records are returned in total.",
      required: !1
    }),
    pageSize: import_pieces_framework4.Property.Number({
      displayName: "Page Size",
      description: "How many records are returned per page (max 1000).",
      required: !1
    }),
    filter: import_pieces_framework4.Property.LongText({
      displayName: "Filter",
      description: "The filter to apply to the records (see https://bika.ai/help/guide/developer/filter-query-language).",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let databaseId = context.propsValue.database_id, spaceId = context.propsValue.space_id, maxRecords = context.propsValue.maxRecords, pageSize = (_a = context.propsValue.pageSize) != null ? _a : 100, filter = context.propsValue.filter, response = await makeClient(
      context.auth.props
    ).listRecords(
      spaceId,
      databaseId,
      prepareQuery({
        pageSize,
        maxRecords,
        filter
      })
    );
    if (!response.success)
      throw new Error(JSON.stringify(response, void 0, 2));
    return response;
  }
});

// ../ap/packages/pieces/community/bika/src/lib/actions/find-record.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var findRecordAction = (0, import_pieces_framework5.createAction)({
  auth: BikaAuth,
  name: "bika_find_record",
  classification: "READ",
  displayName: "Get Record",
  description: "Retrieves a record in database by ID.",
  audience: "both",
  aiMetadata: { description: "Retrieves a single record from a Bika.ai database by its record ID. Use when you already know the exact record ID; to search by field values or fetch multiple rows use Find Records instead. Read-only and idempotent.", idempotent: !0 },
  props: {
    space_id: BikaCommon.space_id,
    database_id: BikaCommon.database_id,
    recordId: import_pieces_framework5.Property.ShortText({
      displayName: "Record ID",
      required: !0
    })
  },
  async run(context) {
    let databaseId = context.propsValue.database_id, spaceId = context.propsValue.space_id, recordId = context.propsValue.recordId, response = await makeClient(
      context.auth.props
    ).findRecord(
      spaceId,
      databaseId,
      recordId
    );
    if (!response.success)
      throw new Error(JSON.stringify(response, void 0, 2));
    return response;
  }
});

// ../ap/packages/pieces/community/bika/src/lib/actions/delete-record.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var deleteRecordAction = (0, import_pieces_framework6.createAction)({
  auth: BikaAuth,
  name: "bika_delete_record",
  classification: "DESTRUCTIVE",
  displayName: "Delete Record",
  description: "Deletes a record in database by ID.",
  audience: "both",
  aiMetadata: { description: "Deletes a single record from a Bika.ai database by its record ID, within a given space and database. Use to permanently remove a known record. Idempotent in effect: once removed, repeating the call leaves the record absent.", idempotent: !0 },
  props: {
    space_id: BikaCommon.space_id,
    database_id: BikaCommon.database_id,
    recordId: import_pieces_framework6.Property.ShortText({
      displayName: "Record ID",
      description: "The ID of the record to delete.",
      required: !0
    })
  },
  async run(context) {
    let databaseId = context.propsValue.database_id, spaceId = context.propsValue.space_id, recordId = context.propsValue.recordId, response = await makeClient(
      context.auth.props
    ).deleteRecord(
      spaceId,
      databaseId,
      recordId
    );
    if (!response.success)
      throw new Error(JSON.stringify(response, void 0, 2));
    return response;
  }
});

// ../ap/packages/pieces/community/bika/src/lib/actions/update-record.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var updateRecordAction = (0, import_pieces_framework7.createAction)({
  auth: BikaAuth,
  name: "bika_update_record",
  classification: "WRITE",
  displayName: "Update Record",
  description: "Updates an existing record in database.",
  audience: "both",
  aiMetadata: { description: "Updates the fields of an existing Bika.ai record identified by its record ID, within a given space and database. Use when modifying a known record; provide only the fields to change (read-only field types are ignored). Idempotent: repeating with the same input leaves the record in the same state.", idempotent: !0 },
  props: {
    space_id: BikaCommon.space_id,
    database_id: BikaCommon.database_id,
    recordId: import_pieces_framework7.Property.ShortText({
      displayName: "Record ID",
      description: "The ID of the record to update.",
      required: !0
    }),
    fields: BikaCommon.fields
  },
  async run(context) {
    let auth = context.auth, databaseId = context.propsValue.database_id, spaceId = context.propsValue.space_id, recordId = context.propsValue.recordId, dynamicFields = context.propsValue.fields, fields = {}, props = Object.entries(dynamicFields);
    for (let [propertyKey, propertyValue] of props)
      propertyValue !== void 0 && propertyValue !== "" && (fields[propertyKey] = propertyValue);
    let newFields = await createNewFields(
      auth,
      spaceId,
      databaseId,
      fields
    ), response = await makeClient(context.auth.props).updateRecord(spaceId, databaseId, recordId, {
      fields: __spreadValues({}, newFields)
    });
    if (!response.success)
      throw new Error(JSON.stringify(response, void 0, 2));
    return response;
  }
});

// ../ap/packages/pieces/community/bika/src/index.ts
var bika = (0, import_pieces_framework8.createPiece)({
  displayName: "Bika.ai",
  auth: BikaAuth,
  description: "Interactive spreadsheets with collaboration",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/bika.png",
  categories: [import_pieces_framework9.PieceCategory.PRODUCTIVITY],
  authors: [
    "codegino"
  ],
  actions: [
    createRecordAction,
    findRecordsAction,
    findRecordAction,
    updateRecordAction,
    deleteRecordAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://bika.ai/api/openapi/bika",
      auth: BikaAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.props.token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  bika
});
