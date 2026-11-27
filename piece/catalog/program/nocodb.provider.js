"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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

// ../ap/packages/pieces/community/nocodb/src/index.ts
var index_exports = {};
__export(index_exports, {
  nocodb: () => nocodb
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/nocodb/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), nocodbAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `
  1. Log in to your NocoDB Account.
  2. Click on your profile-pic(bottom-left) and navigate to **Account Settings->Tokens**.
  3. Create new token with any name and copy API Token.
  4. Your Base URL is where your app is hosted.`,
  props: {
    baseUrl: import_pieces_framework.Property.ShortText({
      displayName: "NocoDB Base URL",
      required: !0,
      defaultValue: "https://app.nocodb.com"
    }),
    apiToken: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Token",
      required: !0
    }),
    version: import_pieces_framework.Property.StaticDropdown({
      displayName: "API Version",
      description: "Required only for self-hosted instances. Not needed for the cloud version.",
      required: !1,
      defaultValue: 0,
      options: {
        options: [
          { label: "Before v0.90.0", value: 1 },
          { label: "v0.90.0 to v0.199.0", value: 2 },
          { label: "v0.200.0 Onwards", value: 3 },
          { label: "v0.260.0 Onwards", value: 4 }
        ]
      }
    })
  },
  required: !0
});

// ../ap/packages/pieces/community/nocodb/src/lib/actions/create-record.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/nocodb/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/nocodb/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var NocoDBClient = class {
  constructor(hostUrl, apiToken) {
    __publicField(this, "hostUrl", hostUrl);
    __publicField(this, "apiToken", apiToken);
  }
  async makeRequest(method, resourceUri, query, body = void 0) {
    let baseUrl = this.hostUrl.replace(/\/$/, ""), params = {};
    if (query)
      for (let [key, value] of Object.entries(query))
        value != null && (params[key] = String(value));
    let request = {
      method,
      url: baseUrl + "/api" + resourceUri,
      headers: {
        "xc-token": this.apiToken
      },
      queryParams: params,
      body
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  }
  async listWorkspaces() {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/v1/workspaces/"
    );
  }
  async listBases(workspaceId, version = 3) {
    if (workspaceId && workspaceId !== "none") {
      let endpoint = version === 4 ? `/v3/meta/workspaces/${workspaceId}/bases/` : `/v1/workspaces/${workspaceId}/bases/`;
      return await this.makeRequest(
        import_pieces_common.HttpMethod.GET,
        endpoint
      );
    } else {
      let endpoint = version === 4 ? "/v3/meta/workspaces/nc/bases/" : version === 3 ? "/v2/meta/bases/" : "/v1/db/meta/projects/";
      return await this.makeRequest(
        import_pieces_common.HttpMethod.GET,
        endpoint
      );
    }
  }
  async listTables(baseId, version = 3) {
    let endpoint = version === 4 ? `/v3/meta/bases/${baseId}/tables` : version === 3 ? `/v2/meta/bases/${baseId}/tables` : `/v1/db/meta/projects/${baseId}/tables`;
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      endpoint
    );
  }
  async getTable(_baseId, tableId, version = 3) {
    let endpoint = version === 3 ? `/v2/meta/tables/${tableId}/` : `/v1/db/meta/tables/${tableId}/`;
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, endpoint);
  }
  async getTableV3(baseId, tableId, _version = 4) {
    let endpoint = `/v3/meta/bases/${baseId}/tables/${tableId}`;
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, endpoint);
  }
  async createRecord(baseId, tableId, recordInput, version = 3) {
    var _a, _b;
    let endpoint = version === 4 ? `/v3/data/${baseId}/${tableId}/records` : version === 3 ? `/v2/tables/${tableId}/records` : `/v1/db/data/noco/${tableId}`, response = await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      endpoint,
      void 0,
      version === 4 ? { fields: recordInput } : recordInput
    );
    return version === 4 && (_b = (_a = response.records) == null ? void 0 : _a[0]) != null ? _b : response;
  }
  async getRecord(baseId, tableId, recordId, version = 3) {
    let endpoint = version === 4 ? `/v3/data/${baseId}/${tableId}/records/${recordId}` : version === 3 ? `/v2/tables/${tableId}/records/${recordId}` : `/v1/db/data/noco/${tableId}/${recordId}`;
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, endpoint);
  }
  async updateRecord(baseId, tableId, recordInput, version = 3) {
    var _a, _b;
    let endpoint = version === 4 ? `/v3/data/${baseId}/${tableId}/records` : version === 3 ? `/v2/tables/${tableId}/records/` : `/v1/db/data/noco/${tableId}`, response = await this.makeRequest(
      import_pieces_common.HttpMethod.PATCH,
      endpoint,
      void 0,
      recordInput
    );
    return version === 4 && (_b = (_a = response.records) == null ? void 0 : _a[0]) != null ? _b : response;
  }
  async deleteRecord(baseId, tableId, recordId, version = 3) {
    var _a, _b;
    let endpoint = version === 4 ? `/v3/data/${baseId}/${tableId}/records/` : version === 3 ? `/v2/tables/${tableId}/records/` : `/v1/db/data/noco/${tableId}/${recordId}`, body = version === 4 ? { id: recordId } : version === 3 ? { Id: recordId } : void 0, response = await this.makeRequest(import_pieces_common.HttpMethod.DELETE, endpoint, void 0, body);
    return version === 4 && (_b = (_a = response.records) == null ? void 0 : _a[0]) != null ? _b : response;
  }
  async listRecords(baseId, tableId, params, version = 3) {
    let endpoint = version === 4 ? `/v3/data/${baseId}/${tableId}/records` : version === 3 ? `/v2/tables/${tableId}/records/` : `/v1/db/data/noco/${tableId}`;
    if (version === 4 && params.sort && typeof params.sort == "string") {
      let sortItems = params.sort.split(",");
      params.sort = JSON.stringify(
        sortItems.map((item) => item.startsWith("-") ? {
          field: item.substring(1),
          direction: "desc"
        } : {
          field: item,
          direction: "asc"
        })
      );
    }
    let makeRequest = async () => await this.makeRequest(import_pieces_common.HttpMethod.GET, endpoint, params);
    return makeRequest();
  }
};

// ../ap/packages/pieces/community/nocodb/src/lib/common/index.ts
function makeClient(auth) {
  return new NocoDBClient(auth.props.baseUrl, auth.props.apiToken);
}
var nocodbCommon = {
  workspaceId: import_pieces_framework2.Property.Dropdown({
    auth: nocodbAuth,
    displayName: "Workspace ID",
    refreshers: [],
    required: !1,
    description: 'For self-hosted instances,select "No Workspace".',
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Please connect your account first.",
          options: []
        };
      let client = makeClient(
        auth
      );
      try {
        return {
          disabled: !1,
          options: (await client.listWorkspaces()).list.map((workspace) => ({
            label: workspace.title,
            value: workspace.id
          }))
        };
      } catch (error) {
        return {
          disabled: !1,
          options: [
            {
              label: "No Workspace",
              value: "none"
            }
          ]
        };
      }
    }
  }),
  baseId: import_pieces_framework2.Property.Dropdown({
    auth: nocodbAuth,
    displayName: "Base ID",
    refreshers: ["workspaceId"],
    required: !0,
    options: async ({ auth, workspaceId }) => {
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Please connect your account first.",
          options: []
        };
      try {
        return {
          disabled: !1,
          options: (await makeClient(
            auth
          ).listBases(
            workspaceId || void 0,
            auth.props.version || 3
          )).list.map((base) => ({
            label: base.title,
            value: base.id
          }))
        };
      } catch (error) {
        return console.error("Error fetching bases:", error), {
          disabled: !0,
          placeholder: "Error fetching bases. Please check your connection and version.",
          options: []
        };
      }
    }
  }),
  tableId: import_pieces_framework2.Property.Dropdown({
    auth: nocodbAuth,
    displayName: "Table ID",
    refreshers: ["workspaceId", "baseId"],
    required: !0,
    options: async ({ auth, baseId }) => !auth || !baseId ? {
      disabled: !0,
      placeholder: "Please connect your account first and select base.",
      options: []
    } : {
      disabled: !1,
      options: (await makeClient(
        auth
      ).listTables(
        baseId,
        auth.props.version || 3
      )).list.map((table) => ({
        label: table.title,
        value: table.id
      }))
    }
  }),
  columnId: import_pieces_framework2.Property.MultiSelectDropdown({
    auth: nocodbAuth,
    displayName: "Fields",
    description: "Allows you to specify the fields that you wish to include in your API response. By default, all the fields are included in the response.",
    refreshers: ["workspaceId", "baseId", "tableId"],
    required: !1,
    options: async ({ auth, baseId, tableId }) => {
      if (!auth || !baseId || !tableId)
        return {
          disabled: !0,
          placeholder: "Please connect your account first and select base.",
          options: []
        };
      let client = makeClient(
        auth
      ), authVersion = auth.props.version || 3, response = authVersion === 4 ? await client.getTableV3(
        baseId,
        tableId,
        authVersion
      ) : await client.getTable(
        baseId,
        tableId,
        authVersion
      );
      return {
        disabled: !1,
        options: authVersion === 4 ? response.fields.map((field) => ({
          label: field.title,
          value: field.title
        })) : response.columns.map((column) => ({
          label: column.title,
          value: column.title
        }))
      };
    }
  }),
  tableColumns: import_pieces_framework2.Property.DynamicProperties({
    auth: nocodbAuth,
    displayName: "Table Columns",
    refreshers: ["baseId", "tableId"],
    required: !0,
    props: async ({ auth, baseId, tableId }) => {
      var _a, _b, _c, _d;
      if (!auth) return {};
      if (!baseId) return {};
      if (!tableId) return {};
      let fields = {}, client = makeClient(
        auth
      ), authVersion = auth.props.version || 3, response = authVersion === 4 ? await client.getTableV3(
        baseId,
        tableId,
        authVersion
      ) : await client.getTable(
        baseId,
        tableId,
        authVersion
      ), columns = authVersion === 4 ? response.fields : response.columns;
      for (let column of columns != null ? columns : [])
        switch (authVersion === 4 ? column.type : column.uidt) {
          case "SingleLineText":
          case "PhoneNumber":
          case "Email":
          case "URL":
            fields[column.title] = import_pieces_framework2.Property.ShortText({
              displayName: column.title,
              required: !1
            });
            break;
          case "LongText":
            fields[column.title] = import_pieces_framework2.Property.LongText({
              displayName: column.title,
              required: !1
            });
            break;
          case "Number":
          case "Decimal":
          case "Percent":
          case "Rating":
          case "Currency":
          case "Year":
            fields[column.title] = import_pieces_framework2.Property.Number({
              displayName: column.title,
              required: !1
            });
            break;
          case "Checkbox":
            fields[column.title] = import_pieces_framework2.Property.Checkbox({
              displayName: column.title,
              required: !0
            });
            break;
          case "MultiSelect": {
            let options = authVersion === 4 ? (() => {
              var _a2;
              let colOptions = column.options;
              return (_a2 = colOptions == null ? void 0 : colOptions.choices) == null ? void 0 : _a2.map((option) => ({
                label: option.title,
                value: option.title
              }));
            })() : (_b = (_a = column.colOptions) == null ? void 0 : _a.options) == null ? void 0 : _b.map(
              (option) => ({
                label: option.title,
                value: option.title
              })
            );
            fields[column.title] = import_pieces_framework2.Property.StaticMultiSelectDropdown({
              displayName: column.title,
              required: !1,
              options: {
                disabled: !1,
                options: options != null ? options : []
              }
            });
            break;
          }
          case "SingleSelect": {
            let options = authVersion === 4 ? (() => {
              var _a2;
              let colOptions = column.options;
              return (_a2 = colOptions == null ? void 0 : colOptions.choices) == null ? void 0 : _a2.map((option) => ({
                label: option.title,
                value: option.title
              }));
            })() : (_d = (_c = column.colOptions) == null ? void 0 : _c.options) == null ? void 0 : _d.map(
              (option) => ({
                label: option.title,
                value: option.title
              })
            );
            fields[column.title] = import_pieces_framework2.Property.StaticDropdown({
              displayName: column.title,
              required: !1,
              options: {
                disabled: !1,
                options: options != null ? options : []
              }
            });
            break;
          }
          case "Date": {
            let columnMeta = authVersion === 4 ? column.options : column.meta;
            fields[column.title] = import_pieces_framework2.Property.ShortText({
              displayName: column.title,
              required: !1,
              description: columnMeta != null && columnMeta.date_format ? `Please provide date in ${columnMeta.date_format} format.` : ""
            });
            break;
          }
          case "Time":
            fields[column.title] = import_pieces_framework2.Property.ShortText({
              displayName: column.title,
              required: !1,
              description: "Please provide time in HH:mm:ss format."
            });
            break;
          case "DateTime":
            fields[column.title] = import_pieces_framework2.Property.DateTime({
              displayName: column.title,
              required: !1
            });
            break;
          case "JSON":
            fields[column.title] = import_pieces_framework2.Property.Json({
              displayName: column.title,
              required: !1
            });
            break;
          default:
            fields[column.title] = import_pieces_framework2.Property.ShortText({
              displayName: column.title,
              required: !1
            });
            break;
        }
      return fields;
    }
  })
};

// ../ap/packages/pieces/community/nocodb/src/lib/actions/create-record.ts
var createRecordAction = (0, import_pieces_framework3.createAction)({
  auth: nocodbAuth,
  name: "nocodb-create-record",
  displayName: "Create a Record",
  description: "Creates a new record in the given table.",
  audience: "both",
  aiMetadata: {
    description: "Inserts a new row into a specific NocoDB table, with column values supplied as fields. Use when an agent needs to add data to a base; the target table is required and the create is determined by base/table plus the provided column values. Not idempotent: each call appends another record, so repeating it creates duplicates.",
    idempotent: !1
  },
  props: {
    workspaceId: nocodbCommon.workspaceId,
    baseId: nocodbCommon.baseId,
    tableId: nocodbCommon.tableId,
    tableColumns: nocodbCommon.tableColumns
  },
  async run(context) {
    let { baseId, tableId, tableColumns } = context.propsValue, recordInput = {};
    return Object.entries(tableColumns).forEach(([key, value]) => {
      Array.isArray(value) ? recordInput[key] = value.join(",") : recordInput[key] = value;
    }), await makeClient(context.auth).createRecord(baseId, tableId, recordInput, context.auth.props.version || 3);
  }
});

// ../ap/packages/pieces/community/nocodb/src/lib/actions/delete-record.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var deleteRecordAction = (0, import_pieces_framework4.createAction)({
  auth: nocodbAuth,
  name: "nocodb-delete-record",
  displayName: "Delete a Record",
  description: "Deletes a record with the given Record ID.",
  audience: "both",
  aiMetadata: {
    description: "Permanently removes a single row from a NocoDB table, identified by its numeric Record ID within the chosen base and table. Use when an agent needs to delete a known record. Idempotent: repeating the call with the same Record ID leaves the record absent and produces no additional effect.",
    idempotent: !0
  },
  props: {
    workspaceId: nocodbCommon.workspaceId,
    baseId: nocodbCommon.baseId,
    tableId: nocodbCommon.tableId,
    recordId: import_pieces_framework4.Property.Number({
      displayName: "Record ID",
      required: !0
    })
  },
  async run(context) {
    let { baseId, tableId, recordId } = context.propsValue;
    return await makeClient(context.auth).deleteRecord(baseId, tableId, recordId, context.auth.props.version || 3);
  }
});

// ../ap/packages/pieces/community/nocodb/src/lib/actions/update-record.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var updateRecordAction = (0, import_pieces_framework5.createAction)({
  auth: nocodbAuth,
  name: "nocodb-update-record",
  displayName: "Update a Record",
  description: "Updates an existing record with the given Record ID.",
  audience: "both",
  aiMetadata: {
    description: "Patches an existing NocoDB row identified by its numeric Record ID, overwriting only the supplied column values within the chosen base and table. Use when an agent needs to modify a record it already knows by ID. Idempotent: re-applying the same field values to the same Record ID yields the same final state.",
    idempotent: !0
  },
  props: {
    workspaceId: nocodbCommon.workspaceId,
    baseId: nocodbCommon.baseId,
    tableId: nocodbCommon.tableId,
    recordId: import_pieces_framework5.Property.Number({
      displayName: "Record ID",
      required: !0
    }),
    tableColumns: nocodbCommon.tableColumns
  },
  async run(context) {
    let { baseId, tableId, recordId, tableColumns } = context.propsValue, authVersion = context.auth.props.version || 3, recordInput = {};
    return authVersion === 4 ? (recordInput.id = recordId, recordInput.fields = {}) : recordInput = {
      Id: recordId
    }, Object.entries(tableColumns).forEach(([key, value]) => {
      authVersion === 4 ? recordInput.fields[key] = value : Array.isArray(value) ? recordInput[key] = value.join(",") : recordInput[key] = value;
    }), await makeClient(context.auth).updateRecord(baseId, tableId, recordInput, authVersion);
  }
});

// ../ap/packages/pieces/community/nocodb/src/lib/actions/get-record.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getRecordAction = (0, import_pieces_framework6.createAction)({
  auth: nocodbAuth,
  name: "nocodb-get-record",
  displayName: "Get a Record",
  description: "Gets a record by the Record ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single NocoDB row by its numeric Record ID from the chosen base and table. Use when an agent already knows the exact Record ID and needs that row's current field values; to find records by criteria instead, use Search Records. Idempotent read-only lookup.",
    idempotent: !0
  },
  props: {
    workspaceId: nocodbCommon.workspaceId,
    baseId: nocodbCommon.baseId,
    tableId: nocodbCommon.tableId,
    recordId: import_pieces_framework6.Property.Number({
      displayName: "Record ID",
      required: !0
    })
  },
  async run(context) {
    let { baseId, tableId, recordId } = context.propsValue;
    return await makeClient(context.auth).getRecord(baseId, tableId, recordId, context.auth.props.version || 3);
  }
});

// ../ap/packages/pieces/community/nocodb/src/lib/actions/search-records.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var searchRecordsAction = (0, import_pieces_framework7.createAction)({
  auth: nocodbAuth,
  name: "nocodb-search-records",
  displayName: "Search Records",
  description: "Returns a list of records matching the where condition.",
  audience: "both",
  aiMetadata: {
    description: "Lists rows from a NocoDB table within the chosen base, optionally narrowed by a Where filter expression and ordered by a Sort field list; leaving the Where condition empty returns all records up to the Limit. Use when an agent needs to find or browse records by criteria rather than by a known Record ID. Idempotent read-only query.",
    idempotent: !0
  },
  props: {
    workspaceId: nocodbCommon.workspaceId,
    baseId: nocodbCommon.baseId,
    tableId: nocodbCommon.tableId,
    columnId: nocodbCommon.columnId,
    whereCondition: import_pieces_framework7.Property.LongText({
      displayName: "Where",
      required: !1,
      description: "Enables you to define specific conditions for filtering records.See docs [here](https://docs.nocodb.com/0.109.7/developer-resources/rest-apis/#comparison-operators)."
    }),
    limit: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      required: !0,
      defaultValue: 10,
      description: "Enables you to set a limit on the number of records you want to retrieve."
    }),
    sort: import_pieces_framework7.Property.LongText({
      displayName: "Sort",
      required: !1,
      description: "Comma separated field names without space.Example: **field1,-field2** will sort the records first by 'field1' in ascending order and then by 'field2' in descending order."
    })
  },
  async run(context) {
    let { baseId, tableId, columnId, limit, whereCondition, sort } = context.propsValue, client = makeClient(context.auth), authVersion = context.auth.props.version || 3, response = await client.listRecords(
      baseId,
      tableId,
      {
        fields: columnId ? columnId.join(",") : void 0,
        where: whereCondition,
        sort,
        offset: 0,
        limit
      },
      authVersion
    );
    return authVersion === 4 ? response.records : response.list;
  }
});

// ../ap/packages/pieces/community/nocodb/src/index.ts
var nocodb = (0, import_pieces_framework8.createPiece)({
  displayName: "NocoDB",
  auth: nocodbAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/nocodb.png",
  authors: ["kishanprmr"],
  actions: [
    createRecordAction,
    deleteRecordAction,
    updateRecordAction,
    getRecordAction,
    searchRecordsAction
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  nocodb
});
