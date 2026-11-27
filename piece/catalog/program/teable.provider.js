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

// ../ap/packages/pieces/community/teable/src/index.ts
var index_exports = {};
__export(index_exports, {
  teable: () => teable
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/teable/src/lib/actions/create-record.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/teable/src/lib/common/index.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/teable/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/teable/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/teable/src/lib/common/constants.ts
var TeableComputedFieldTypes = [
  "attachment" /* ATTACHMENT */,
  "formula" /* FORMULA */,
  "rollup" /* ROLLUP */,
  "conditionalRollup" /* CONDITIONAL_ROLLUP */,
  "createdTime" /* CREATED_TIME */,
  "lastModifiedTime" /* LAST_MODIFIED_TIME */,
  "createdBy" /* CREATED_BY */,
  "lastModifiedBy" /* LAST_MODIFIED_BY */,
  "autoNumber" /* AUTO_NUMBER */,
  "button" /* BUTTON */
], TEABLE_CLOUD_URL = "https://app.teable.ai";

// ../ap/packages/pieces/community/teable/src/lib/common/client.ts
function emptyValueFilter(accessor) {
  return (key) => {
    let val = accessor(key);
    return val != null && (typeof val != "string" || val.length > 0) && (!Array.isArray(val) || val.length > 0);
  };
}
function prepareQuery(request) {
  let params = {};
  return request && Object.keys(request).filter(emptyValueFilter((k) => request[k])).forEach((k) => {
    let val = request[k];
    params[k] = Array.isArray(val) ? val : val.toString();
  }), params;
}
var TeableClient = class {
  constructor(token, teableUrl = TEABLE_CLOUD_URL) {
    __publicField(this, "token", token);
    __publicField(this, "apiBase");
    this.apiBase = `${teableUrl}/api`;
  }
  async makeRequest(method, resourceUri, query, body = void 0) {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: this.apiBase + resourceUri,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.token
      },
      queryParams: query,
      body
    })).body;
  }
  /**
   * GET /api/base/access/all
   * Returns all bases the token has access to.
   * @see https://help.teable.ai/en/api-reference/base/get-baseaccessall
   */
  async listBases() {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/base/access/all");
  }
  /**
   * GET /api/base/{baseId}/table
   * Returns all tables in the given base.
   * @see https://help.teable.ai/en/api-reference/table/list-tables
   */
  async listTables(baseId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/base/${baseId}/table`);
  }
  /**
   * GET /api/table/{tableId}/field
   * Returns all fields in the given table.
   * @see https://help.teable.ai/en/api-reference/field/list-fields
   */
  async listFields(tableId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/table/${tableId}/field`);
  }
  /**
   * GET /api/table/{tableId}/record/{recordId}
   * Retrieves a single record by its ID.
   * @see https://help.teable.ai/en/api-reference/record/get-record
   */
  async getRecord(tableId, recordId, query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/table/${tableId}/record/${recordId}`, query);
  }
  /**
   * GET /api/table/{tableId}/record
   * Retrieves a list of records with support for filtering, sorting, and pagination.
   * @see https://help.teable.ai/en/api-reference/record/list-records
   */
  async listRecords(tableId, query) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/table/${tableId}/record`, query);
  }
  /**
   * POST /api/table/{tableId}/record
   * Creates one or more records.
   */
  async createRecord(tableId, request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      `/table/${tableId}/record`,
      void 0,
      request
    );
  }
  /**
   * DELETE /api/table/{tableId}/record/{recordId}
   * Deletes a single record.
   */
  async deleteRecord(tableId, recordId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.DELETE,
      `/table/${tableId}/record/${recordId}`
    );
  }
  /**
   * PATCH /api/table/{tableId}/record/{recordId}
   * Updates a single record.
   */
  async updateRecord(tableId, recordId, request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.PATCH,
      `/table/${tableId}/record/${recordId}`,
      void 0,
      request
    );
  }
  /**
   * POST /api/table/{tableId}/record/{recordId}/{fieldId}/uploadAttachment
   * Uploads a file as an attachment to a specific field in a record.
   */
  async uploadAttachment(tableId, recordId, fieldId, form) {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${this.apiBase}/table/${tableId}/record/${recordId}/${fieldId}/uploadAttachment`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.token
      },
      body: form
    })).body;
  }
};

// ../ap/packages/pieces/community/teable/src/lib/auth.ts
var TeableAuth = [
  import_pieces_framework.PieceAuth.OAuth2({
    description: "Connect your Teable account using OAuth2 (Teable Cloud only).",
    authUrl: "https://app.teable.ai/api/oauth/authorize",
    tokenUrl: "https://app.teable.ai/api/oauth/access_token",
    required: !0,
    scope: [
      "base|read",
      "table|read",
      "field|read",
      "record|read",
      "record|create",
      "record|update",
      "record|delete"
    ]
  }),
  import_pieces_framework.PieceAuth.CustomAuth({
    displayName: "Personal Access Token",
    description: `
Authenticate using a Teable Personal Access Token. Also use this option for self-hosted instances.

To obtain your token:
1. Log in to your Teable account.
2. Click your profile icon (top-right corner).
3. Go to **Settings** > **Personal Access Token**.
4. Click **New Token**, set a name and the required scopes.
5. Copy and save the generated token.
    `,
    required: !0,
    props: {
      token: import_pieces_framework.PieceAuth.SecretText({
        displayName: "Personal Access Token",
        description: "Your Teable personal access token.",
        required: !0
      }),
      baseUrl: import_pieces_framework.Property.ShortText({
        displayName: "Base URL",
        description: "Teable Cloud: https://app.teable.ai \u2014 for self-hosted, enter your own domain.",
        required: !1
      })
    },
    validate: async ({ auth }) => {
      try {
        return await new TeableClient(
          auth.token,
          auth.baseUrl || TEABLE_CLOUD_URL
        ).listBases(), { valid: !0 };
      } catch (e) {
        return {
          valid: !1,
          error: e instanceof Error ? e.message : "Invalid credentials. Check your token and Base URL."
        };
      }
    }
  })
];
function getTeableToken(auth) {
  return auth.type === import_pieces_framework2.AppConnectionType.OAUTH2 ? auth.access_token : auth.props.token;
}
function getTeableBaseUrl(auth) {
  return auth.type === import_pieces_framework2.AppConnectionType.OAUTH2 ? TEABLE_CLOUD_URL : auth.props.baseUrl || TEABLE_CLOUD_URL;
}

// ../ap/packages/pieces/community/teable/src/lib/common/index.ts
function makeClient(auth) {
  return new TeableClient(
    getTeableToken(auth),
    getTeableBaseUrl(auth)
  );
}
var TeableCommon = {
  base_id: import_pieces_framework3.Property.Dropdown({
    auth: TeableAuth,
    displayName: "Base",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient(auth).listBases()).map((b) => ({ label: b.name, value: b.id }))
    } : { disabled: !0, options: [], placeholder: "Connect your account first." }
  }),
  table_id: import_pieces_framework3.Property.Dropdown({
    auth: TeableAuth,
    displayName: "Table",
    description: "The table inside the selected base.",
    required: !0,
    refreshers: ["base_id"],
    options: async ({ auth, base_id }) => !auth || !base_id ? { disabled: !0, options: [], placeholder: "Select a base first." } : {
      disabled: !1,
      options: (await makeClient(auth).listTables(base_id)).map((t) => ({ label: t.name, value: t.id }))
    }
  }),
  record_id: import_pieces_framework3.Property.Dropdown({
    auth: TeableAuth,
    displayName: "Record",
    description: "The record to act on.",
    required: !0,
    refreshers: ["table_id"],
    options: async ({ auth, table_id }) => !auth || !table_id ? { disabled: !0, options: [], placeholder: "Select a table first." } : {
      disabled: !1,
      options: (await makeClient(auth).listRecords(table_id)).records.map((r) => {
        let primaryValue = Object.values(r.fields)[0];
        return { label: primaryValue != null && primaryValue !== "" ? String(primaryValue) : r.id, value: r.id };
      })
    }
  }),
  fields: import_pieces_framework3.Property.DynamicProperties({
    auth: TeableAuth,
    displayName: "Fields",
    description: "The fields to add to the record.",
    required: !0,
    refreshers: ["auth", "table_id"],
    props: async ({ auth, table_id }) => {
      var _a, _b;
      if (!auth || !table_id) return {};
      let fields = await makeClient(auth).listFields(table_id), props = {};
      for (let field of fields) {
        if (field.isComputed || TeableComputedFieldTypes.includes(field.type))
          continue;
        let choices = (_b = (_a = field.options) == null ? void 0 : _a.choices) != null ? _b : [];
        switch (field.type) {
          case "checkbox" /* CHECKBOX */:
            props[field.name] = import_pieces_framework3.Property.Checkbox({ displayName: field.name, required: !1 });
            break;
          case "number" /* NUMBER */:
          case "rating" /* RATING */:
            props[field.name] = import_pieces_framework3.Property.Number({ displayName: field.name, required: !1 });
            break;
          case "date" /* DATE */:
            props[field.name] = import_pieces_framework3.Property.DateTime({ displayName: field.name, required: !1 });
            break;
          case "longText" /* LONG_TEXT */:
            props[field.name] = import_pieces_framework3.Property.LongText({ displayName: field.name, required: !1 });
            break;
          case "multipleSelect" /* MULTIPLE_SELECT */:
            props[field.name] = import_pieces_framework3.Property.StaticMultiSelectDropdown({
              displayName: field.name,
              required: !1,
              options: { options: choices.map((c) => ({ label: c.name, value: c.name })) }
            });
            break;
          case "singleSelect" /* SINGLE_SELECT */:
            props[field.name] = import_pieces_framework3.Property.StaticDropdown({
              displayName: field.name,
              required: !1,
              options: { options: choices.map((c) => ({ label: c.name, value: c.name })) }
            });
            break;
          case "link" /* LINK */:
          case "user" /* USER */:
            props[field.name] = import_pieces_framework3.Property.Array({ displayName: field.name, required: !1 });
            break;
          case "singleLineText" /* SINGLE_LINE_TEXT */:
          default:
            props[field.name] = import_pieces_framework3.Property.ShortText({ displayName: field.name, required: !1 });
            break;
        }
      }
      return props;
    }
  })
};

// ../ap/packages/pieces/community/teable/src/lib/actions/create-record.ts
var createRecordAction = (0, import_pieces_framework4.createAction)({
  auth: TeableAuth,
  name: "teable_create_record",
  classification: "WRITE",
  displayName: "Create Record",
  description: "Creates a new record in a Teable table.",
  audience: "both",
  aiMetadata: {
    description: "Insert a new row into a specific Teable table within a base, populating its field values. Use when an agent needs to add data to a no-code database; requires the target table ID and a fields object keyed by field name. Not idempotent \u2014 each call appends another record even with identical input.",
    idempotent: !1
  },
  props: {
    base_id: TeableCommon.base_id,
    table_id: TeableCommon.table_id,
    fields: TeableCommon.fields
  },
  async run(context) {
    let { table_id } = context.propsValue, dynamicFields = context.propsValue.fields, fields = {};
    for (let [key, value] of Object.entries(dynamicFields))
      value != null && value !== "" && (fields[key] = value);
    return await makeClient(context.auth).createRecord(table_id, {
      records: [{ fields }]
    });
  }
});

// ../ap/packages/pieces/community/teable/src/lib/actions/find-records.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var findRecordsAction = (0, import_pieces_framework5.createAction)({
  auth: TeableAuth,
  name: "teable_list_records",
  classification: "SEARCH",
  displayName: "List Records",
  description: "Retrieves a list of records from a table with optional filtering, sorting, and pagination.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a list of records from a Teable table, returning all rows when no filter is supplied or only matching rows when a filter expression or specific record IDs are given. Supports pagination via take/skip. Use to query or scan a table; read-only and idempotent. Requires the table ID.",
    idempotent: !0
  },
  props: {
    base_id: TeableCommon.base_id,
    table_id: TeableCommon.table_id,
    filter: import_pieces_framework5.Property.LongText({
      displayName: "Filter",
      description: "A filter expression for the records. Use the visual query builder at https://app.teable.ai/developer/tool/query-builder to build one.",
      required: !1
    }),
    cellFormat: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Cell Format",
      description: "The format of the cell values in the response.",
      required: !1,
      defaultValue: "json",
      options: {
        options: [
          { label: "JSON", value: "json" },
          { label: "Text", value: "text" }
        ]
      }
    }),
    take: import_pieces_framework5.Property.Number({
      displayName: "Take",
      description: "The record count you want to take, maximum is 1000.",
      required: !1,
      defaultValue: 100
    }),
    skip: import_pieces_framework5.Property.Number({
      displayName: "Skip",
      description: "The records count you want to skip.",
      required: !1,
      defaultValue: 0
    }),
    selectedRecordIds: import_pieces_framework5.Property.Array({
      displayName: "Selected Record IDs",
      description: "Filter selected records by record ids.",
      required: !1
    })
  },
  async run(context) {
    let { table_id, filter, cellFormat, take, skip, selectedRecordIds } = context.propsValue;
    return await makeClient(context.auth).listRecords(
      table_id,
      prepareQuery({
        filter,
        cellFormat,
        take,
        skip,
        selectedRecordIds
      })
    );
  }
});

// ../ap/packages/pieces/community/teable/src/lib/actions/find-record.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var findRecordAction = (0, import_pieces_framework6.createAction)({
  auth: TeableAuth,
  name: "teable_get_record",
  classification: "READ",
  displayName: "Get Record",
  description: "Retrieves a single record from a table by its ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetches one record from a Teable table by its exact record ID. Use when the agent already knows the record ID and needs its current field values; for lookups by field value use List Records with a filter instead. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    base_id: TeableCommon.base_id,
    table_id: TeableCommon.table_id,
    record_id: TeableCommon.record_id,
    cellFormat: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Cell Format",
      description: "The format of the cell values in the response.",
      required: !1,
      defaultValue: "json",
      options: {
        options: [
          { label: "JSON", value: "json" },
          { label: "Text", value: "text" }
        ]
      }
    })
  },
  async run(context) {
    let { table_id, record_id, cellFormat } = context.propsValue;
    return await makeClient(context.auth).getRecord(table_id, record_id, prepareQuery({ cellFormat }));
  }
});

// ../ap/packages/pieces/community/teable/src/lib/actions/delete-record.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var deleteRecordAction = (0, import_pieces_framework7.createAction)({
  auth: TeableAuth,
  name: "teable_delete_record",
  classification: "DESTRUCTIVE",
  displayName: "Delete Record",
  description: "Deletes a record from a Teable table by its ID.",
  audience: "both",
  aiMetadata: {
    description: "Permanently removes a single record from a Teable table by its record ID. Use when the agent must delete a known row. Effectively idempotent on the end state (the record stays gone), though a repeat call on an already-deleted ID may error.",
    idempotent: !0
  },
  props: {
    base_id: TeableCommon.base_id,
    table_id: TeableCommon.table_id,
    record_id: TeableCommon.record_id
  },
  async run(context) {
    let { table_id, record_id } = context.propsValue;
    return await makeClient(context.auth).deleteRecord(table_id, record_id);
  }
});

// ../ap/packages/pieces/community/teable/src/lib/actions/update-record.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var updateRecordAction = (0, import_pieces_framework8.createAction)({
  auth: TeableAuth,
  name: "teable_update_record",
  classification: "WRITE",
  displayName: "Update Record",
  description: "Updates an existing record in a Teable table.",
  audience: "both",
  aiMetadata: {
    description: "Updates the field values of an existing record in a Teable table, identified by its record ID. Use to modify a known row; only the supplied non-empty fields are sent. Idempotent \u2014 re-applying the same field values to the same record yields the same state.",
    idempotent: !0
  },
  props: {
    base_id: TeableCommon.base_id,
    table_id: TeableCommon.table_id,
    record_id: TeableCommon.record_id,
    fields: TeableCommon.fields
  },
  async run(context) {
    let { table_id, record_id } = context.propsValue, dynamicFields = context.propsValue.fields, fields = {};
    for (let [key, value] of Object.entries(dynamicFields))
      value != null && value !== "" && (fields[key] = value);
    return await makeClient(context.auth).updateRecord(table_id, record_id, { record: { fields } });
  }
});

// ../ap/packages/pieces/community/teable/src/lib/actions/upload-attachment.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var uploadAttachmentAction = (0, import_pieces_framework9.createAction)({
  auth: TeableAuth,
  name: "teable_upload_attachment",
  classification: "WRITE",
  displayName: "Upload Attachment",
  description: "Uploads a file as an attachment to a field in a Teable record.",
  audience: "both",
  aiMetadata: {
    description: "Attaches a file to an attachment-type field of an existing Teable record, accepting either a public URL or binary file data. Use to add a document or image to a known record; requires the table ID, record ID, and the attachment field. Not idempotent \u2014 each call adds another attachment to the field.",
    idempotent: !1
  },
  props: {
    base_id: TeableCommon.base_id,
    table_id: TeableCommon.table_id,
    record_id: TeableCommon.record_id,
    field_id: import_pieces_framework9.Property.Dropdown({
      auth: TeableAuth,
      displayName: "Attachment Field",
      description: "The attachment field to upload the file to.",
      required: !0,
      refreshers: ["table_id"],
      options: async ({ auth, table_id }) => !auth || !table_id ? { disabled: !0, options: [], placeholder: "Select a table first." } : {
        disabled: !1,
        options: (await makeClient(auth).listFields(table_id)).filter((f) => f.type === "attachment").map((f) => ({ label: f.name, value: f.id }))
      }
    }),
    file: import_pieces_framework9.Property.File({
      displayName: "File",
      description: "The file to upload. Accepts a URL or a binary file.",
      required: !0
    })
  },
  async run(context) {
    let { table_id, record_id, field_id } = context.propsValue, client = makeClient(context.auth), file = context.propsValue.file, form = new FormData();
    if (typeof file == "string" && /^https?:\/\//i.test(file))
      form.append("fileUrl", file);
    else {
      let apFile = file, mimeType = apFile.extension ? `application/${apFile.extension.replace(/^\./, "")}` : "application/octet-stream";
      form.append(
        "file",
        new Blob([new Uint8Array(apFile.data)], { type: mimeType }),
        apFile.filename
      );
    }
    return await client.uploadAttachment(table_id, record_id, field_id, form);
  }
});

// ../ap/packages/pieces/community/teable/src/index.ts
var teable = (0, import_pieces_framework10.createPiece)({
  displayName: "Teable",
  auth: TeableAuth,
  description: "No-code database built on PostgreSQL",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/teable.png",
  categories: [import_pieces_framework11.PieceCategory.PRODUCTIVITY],
  authors: ["codegino", "onyedikachi-david"],
  actions: [
    createRecordAction,
    findRecordsAction,
    findRecordAction,
    updateRecordAction,
    deleteRecordAction,
    uploadAttachmentAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: TeableAuth,
      baseUrl: (auth) => `${getTeableBaseUrl(auth)}/api`,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${getTeableToken(auth)}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  teable
});
