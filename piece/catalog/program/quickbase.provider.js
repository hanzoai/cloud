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

// ../ap/packages/pieces/community/quickbase/src/index.ts
var index_exports = {};
__export(index_exports, {
  quickbase: () => quickbase
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/quickbase/src/lib/actions/create-record.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickbase/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdown = `
## Quickbase Authentication Setup

### 1. Get Your User Token
- Log in to your Quickbase account
- Go to **My Preferences** \u2192 **My User Information**
- Click on **Manage User Tokens**
- Click **New User Token**
- Enter a name for your token and click **Create**
- Copy the generated token (it will only be shown once)

### 2. Required Permissions
Your user token needs access to:
- Read/write permissions for the apps and tables you want to use
- Admin permissions for creating/deleting records (if needed)

**Security Note:** Keep your user token secure - it provides access to your Quickbase data.
`, quickbaseAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdown,
  required: !0,
  props: {
    realmHostname: import_pieces_framework.Property.ShortText({
      displayName: "Realm Hostname",
      description: "Enter your Quickbase Realm Hostname (e.g., yourrealm.quickbase.com)",
      required: !0
    }),
    userToken: import_pieces_framework.Property.ShortText({
      displayName: "User Token",
      description: "Enter your Quickbase User Token",
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/quickbase/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickbase/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var QuickbaseClient = class {
  constructor(realmHostname, userToken) {
    __publicField(this, "baseUrl", "https://api.quickbase.com/v1");
    __publicField(this, "userToken");
    __publicField(this, "realmHostname");
    this.realmHostname = realmHostname, this.userToken = userToken;
  }
  async makeRequest(endpoint, method, data) {
    var _a;
    let url = `${this.baseUrl}${endpoint}`, request = {
      method,
      url,
      headers: {
        "QB-Realm-Hostname": this.realmHostname,
        Authorization: `QB-USER-TOKEN ${this.userToken}`,
        "Content-Type": "application/json"
      },
      body: data || void 0
    }, lastError;
    for (let attempt = 1; attempt <= 3; attempt++)
      try {
        let response = await import_pieces_common.httpClient.sendRequest(request);
        if (response.status === 429) {
          let retryAfter = (_a = response.headers) == null ? void 0 : _a["retry-after"], retryAfterValue = Array.isArray(retryAfter) ? retryAfter[0] : retryAfter, delay = retryAfterValue ? parseInt(retryAfterValue) * 1e3 : Math.pow(2, attempt) * 1e3;
          await this.sleep(delay);
          continue;
        }
        if (response.status >= 500) {
          if (attempt === 3)
            throw new Error(`Server error: ${response.status}`);
          await this.sleep(Math.pow(2, attempt) * 1e3);
          continue;
        }
        if (response.status >= 400) {
          let errorMessage = `HTTP ${response.status}`;
          try {
            let errorData = response.body;
            errorData.errors && errorData.errors.length > 0 && (errorMessage = errorData.errors[0].message, errorData.errors[0].description && (errorMessage += ` - ${errorData.errors[0].description}`));
          } catch (e) {
            errorMessage = response.body ? String(response.body) : errorMessage;
          }
          throw new Error(errorMessage);
        }
        return response.body;
      } catch (error) {
        if (lastError = error instanceof Error ? error : new Error(String(error)), attempt === 3 || !this.isRetryableError(error))
          throw lastError;
        await this.sleep(Math.pow(2, attempt) * 1e3);
      }
    throw lastError;
  }
  isRetryableError(error) {
    return !!(error instanceof Error && error.message.includes("network"));
  }
  sleep(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }
  async get(endpoint) {
    return this.makeRequest(endpoint, import_pieces_common.HttpMethod.GET);
  }
  async post(endpoint, data) {
    return this.makeRequest(endpoint, import_pieces_common.HttpMethod.POST, data);
  }
  async patch(endpoint, data) {
    return this.makeRequest(endpoint, import_pieces_common.HttpMethod.PATCH, data);
  }
  async delete(endpoint, data) {
    return this.makeRequest(endpoint, import_pieces_common.HttpMethod.DELETE, data);
  }
};

// ../ap/packages/pieces/community/quickbase/src/lib/common/props.ts
var recordIdProp = import_pieces_framework2.Property.ShortText({
  displayName: "Record ID",
  description: "The ID of the record",
  required: !0
}), fieldsMapperProp = import_pieces_framework2.Property.Object({
  displayName: "Fields",
  description: "Map the fields to update. Use field names or field IDs.",
  required: !0
}), createDynamicFieldsMapperProp = () => import_pieces_framework2.Property.DynamicProperties({
  displayName: "Field Values",
  auth: quickbaseAuth,
  description: "Select and set values for table fields",
  required: !0,
  refreshers: ["appId", "tableId"],
  props: async ({ auth, appId, tableId }) => {
    if (!auth || !appId || !tableId)
      return {};
    try {
      let fields = await new QuickbaseClient(auth.props.realmHostname, auth.props.userToken).get(`/fields?tableId=${tableId}`), props3 = {};
      console.log("fields", JSON.stringify(fields, null, 2));
      for (let field of fields) {
        if (field.id === 3) continue;
        let propKey = `field_${field.id}`, isRequired = field.required || !1;
        field.fieldType === "checkbox" ? props3[propKey] = import_pieces_framework2.Property.Checkbox({
          displayName: `${field.label}${isRequired ? " *" : ""}`,
          description: `${field.fieldType} field${isRequired ? " (required)" : ""}`,
          required: isRequired
        }) : field.fieldType === "number" ? props3[propKey] = import_pieces_framework2.Property.Number({
          displayName: `${field.label}${isRequired ? " *" : ""}`,
          description: `${field.fieldType} field${isRequired ? " (required)" : ""}`,
          required: isRequired
        }) : field.fieldType === "date " || field.fieldType === "timestamp" ? props3[propKey] = import_pieces_framework2.Property.DateTime({
          displayName: `${field.label}${isRequired ? " *" : ""}`,
          description: `${field.fieldType} field${isRequired ? " (required)" : ""}`,
          required: isRequired
        }) : props3[propKey] = import_pieces_framework2.Property.ShortText({
          displayName: `${field.label}${isRequired ? " *" : ""}`,
          description: `${field.fieldType} field${isRequired ? " (required)" : ""}`,
          required: isRequired
        });
      }
      return props3;
    } catch (error) {
      return {};
    }
  }
}), filtersProp = import_pieces_framework2.Property.Object({
  displayName: "Filters",
  description: "Filter criteria to find records. Use field names or field IDs as keys.",
  required: !1
}), recordsArrayProp = import_pieces_framework2.Property.Array({
  displayName: "Records",
  description: "Array of records to create or update",
  required: !0
}), maxRecordsProp = import_pieces_framework2.Property.Number({
  displayName: "Maximum Records",
  description: "Maximum number of records to return",
  required: !1,
  defaultValue: 100
}), createAppIdProp = () => import_pieces_framework2.Property.Dropdown({
  auth: quickbaseAuth,
  displayName: "App",
  description: "Select the Quickbase app",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please connect your account first",
        options: []
      };
    try {
      return {
        options: (await new QuickbaseClient(auth.props.realmHostname, auth.props.userToken).get("/apps")).map((app) => ({
          label: app.name,
          value: app.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load apps",
        options: []
      };
    }
  }
}), createTableIdProp = () => import_pieces_framework2.Property.Dropdown({
  auth: quickbaseAuth,
  displayName: "Table",
  description: "Select the table",
  required: !0,
  refreshers: ["appId"],
  options: async ({ auth, appId }) => {
    if (!auth || !appId)
      return {
        disabled: !0,
        placeholder: "Please select an app first",
        options: []
      };
    try {
      return {
        options: (await new QuickbaseClient(auth.props.realmHostname, auth.props.userToken).get(`/tables?appId=${appId}`)).map((table) => ({
          label: table.name,
          value: table.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load tables",
        options: []
      };
    }
  }
}), createMergeFieldProp = () => import_pieces_framework2.Property.Dropdown({
  auth: quickbaseAuth,
  displayName: "Merge Field",
  description: "Field to use for matching existing records (for upsert operations)",
  required: !0,
  refreshers: ["appId", "tableId"],
  options: async ({ auth, appId, tableId }) => {
    if (!auth || !appId || !tableId)
      return {
        disabled: !0,
        placeholder: "Please select app and table first",
        options: []
      };
    try {
      return {
        options: (await new QuickbaseClient(auth.props.realmHostname, auth.props.userToken).get(`/fields?tableId=${tableId}`)).filter((field) => field.unique || field.fieldType === "text" || field.fieldType === "email").map((field) => ({
          label: field.label,
          value: field.id.toString()
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load fields",
        options: []
      };
    }
  }
}), createSortFieldProp = () => import_pieces_framework2.Property.Dropdown({
  auth: quickbaseAuth,
  displayName: "Sort Field",
  description: "Field to sort records by (optional)",
  required: !1,
  refreshers: ["appId", "tableId"],
  options: async ({ auth, appId, tableId }) => {
    if (!auth || !appId || !tableId)
      return {
        disabled: !0,
        placeholder: "Please select app and table first",
        options: []
      };
    try {
      let fields = await new QuickbaseClient(auth.props.realmHostname, auth.props.userToken).get(`/fields?tableId=${tableId}`);
      return {
        options: [
          { label: "No sorting", value: "" },
          ...fields.filter((field) => ["text", "number", "date", "timestamp"].includes(field.fieldType)).map((field) => ({
            label: `${field.label} (${field.fieldType})`,
            value: field.id.toString()
          }))
        ]
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load fields",
        options: []
      };
    }
  }
}), createSortOrderProp = () => import_pieces_framework2.Property.StaticDropdown({
  displayName: "Sort Order",
  description: "Order to sort records",
  required: !1,
  defaultValue: "ASC",
  options: {
    options: [
      { label: "Ascending (A-Z, 1-9)", value: "ASC" },
      { label: "Descending (Z-A, 9-1)", value: "DESC" }
    ]
  }
}), createRecordIdProp = () => import_pieces_framework2.Property.Dropdown({
  auth: quickbaseAuth,
  displayName: "Record",
  description: "Select a record from the table",
  required: !0,
  refreshers: ["appId", "tableId"],
  options: async ({ auth, appId, tableId }) => {
    if (!auth || !appId || !tableId)
      return {
        disabled: !0,
        placeholder: "Please select app and table first",
        options: []
      };
    try {
      let client = new QuickbaseClient(auth.props.realmHostname, auth.props.userToken), fields = await client.get(`/fields?tableId=${tableId}`), displayField = fields.find((f) => f.fieldType === "text" && f.label.toLowerCase().includes("name")) || fields.find((f) => f.fieldType === "text") || fields[1], query = {
        from: tableId,
        select: [3, (displayField == null ? void 0 : displayField.id) || 6],
        options: { top: 50 }
      };
      return {
        options: (await client.post("/records/query", query)).data.map((record) => {
          var _a, _b;
          let recordId = (_a = record[3]) == null ? void 0 : _a.value;
          return {
            label: `${((_b = record[(displayField == null ? void 0 : displayField.id.toString()) || "6"]) == null ? void 0 : _b.value) || `Record ${recordId}`} (ID: ${recordId})`,
            value: recordId.toString()
          };
        })
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load records",
        options: []
      };
    }
  }
}), appIdProp = createAppIdProp(), tableIdProp = createTableIdProp(), mergeFieldProp = createMergeFieldProp(), sortFieldProp = createSortFieldProp(), sortOrderProp = createSortOrderProp(), recordIdDropdownProp = createRecordIdProp(), dynamicFieldsMapperProp = createDynamicFieldsMapperProp();

// ../ap/packages/pieces/community/quickbase/src/lib/actions/create-record.ts
var createRecord = (0, import_pieces_framework3.createAction)({
  name: "create_record",
  displayName: "Create Record",
  description: "Create a new record in a Quickbase table",
  audience: "both",
  aiMetadata: {
    description: "Insert a single new record into a Quickbase table, setting the given field values. Use when adding one new row; for many rows or to upsert against an existing key use Create / Update Records From Array, and to avoid duplicates use Find or Create Record. Requires the app and table; each call inserts a fresh record, so it is not idempotent.",
    idempotent: !1
  },
  auth: quickbaseAuth,
  props: {
    appId: appIdProp,
    tableId: tableIdProp,
    fields: dynamicFieldsMapperProp
  },
  async run(context) {
    let { appId, tableId, fields } = context.propsValue, client = new QuickbaseClient(context.auth.props.realmHostname, context.auth.props.userToken), tableFields = await client.get(`/fields?tableId=${tableId}`), recordData = {};
    for (let [key, value] of Object.entries(fields))
      if (key.startsWith("field_") && value) {
        let fieldId = key.replace("field_", "");
        recordData[fieldId] = { value };
      }
    let response = await client.post("/records", {
      to: tableId,
      data: [recordData],
      fieldsToReturn: tableFields.map((f) => f.id)
    });
    return {
      recordId: response.metadata.createdRecordIds[0],
      record: response.data[0],
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/quickbase/src/lib/actions/update-record.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var updateRecord = (0, import_pieces_framework4.createAction)({
  name: "update_record",
  displayName: "Update Record",
  description: "Update an existing record in a Quickbase table",
  audience: "both",
  aiMetadata: {
    description: "Update the field values of one existing Quickbase record, identified by its record ID. Use when you already know which record to change; to create when none exists use Create or Find or Create Record. Requires the app, table, and record ID; only the provided fields are written. Repeating with the same input re-writes the same values, so it is idempotent.",
    idempotent: !0
  },
  auth: quickbaseAuth,
  props: {
    appId: appIdProp,
    tableId: tableIdProp,
    recordId: recordIdDropdownProp,
    fields: dynamicFieldsMapperProp
  },
  async run(context) {
    let { appId, tableId, recordId, fields } = context.propsValue, client = new QuickbaseClient(context.auth.props.realmHostname, context.auth.props.userToken), tableFields = await client.get(`/fields?tableId=${tableId}`), recordData = {
      3: { value: recordId }
    };
    for (let [key, value] of Object.entries(fields))
      if (key.startsWith("field_") && value) {
        let fieldId = key.replace("field_", "");
        recordData[fieldId] = { value };
      }
    let response = await client.post("/records", {
      to: tableId,
      data: [recordData],
      fieldsToReturn: tableFields.map((f) => f.id)
    });
    return {
      recordId,
      record: response.data[0],
      updated: response.metadata.updatedRecordIds.includes(Number(recordId)),
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/quickbase/src/lib/actions/delete-record.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var deleteRecord = (0, import_pieces_framework5.createAction)({
  name: "delete_record",
  displayName: "Delete Record",
  description: "Delete a record from a Quickbase table",
  audience: "both",
  aiMetadata: {
    description: "Permanently delete a single record from a Quickbase table, matched by its record ID. Use when removing one specific row. Requires the app, table, and record ID. Idempotent: re-running after the record is gone deletes nothing further (numberDeleted will be 0), but the destructive removal cannot be undone.",
    idempotent: !0
  },
  auth: quickbaseAuth,
  props: {
    appId: appIdProp,
    tableId: tableIdProp,
    recordId: recordIdDropdownProp
  },
  async run(context) {
    let { appId, tableId, recordId } = context.propsValue, response = await new QuickbaseClient(context.auth.props.realmHostname, context.auth.props.userToken).delete("/records", {
      from: tableId,
      where: `{3.EX.${recordId}}`
    });
    return {
      recordId,
      deleted: response.numberDeleted > 0,
      numberDeleted: response.numberDeleted,
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/quickbase/src/lib/actions/find-record.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickbase/src/lib/common/utils.ts
function generateDeduplicationKey(recordId, lastModified) {
  return `${recordId}-${lastModified}`;
}
function mapFieldsToRecord(fieldsData, fields) {
  let record = {};
  for (let [key, value] of Object.entries(fieldsData)) {
    let fieldId;
    if (/^\d+$/.test(key))
      fieldId = key;
    else {
      let field = fields.find((f) => f.label.toLowerCase() === key.toLowerCase());
      if (field)
        fieldId = field.id.toString();
      else
        throw new Error(`Field not found: ${key}`);
    }
    record[fieldId] = { value };
  }
  return record;
}
function extractRecordValues(record) {
  let values = {};
  for (let [fieldId, fieldData] of Object.entries(record))
    values[fieldId] = fieldData.value;
  return values;
}
function chunkArray(array, chunkSize) {
  let chunks = [];
  for (let i = 0; i < array.length; i += chunkSize)
    chunks.push(array.slice(i, i + chunkSize));
  return chunks;
}
function buildWhereClause(filters) {
  let conditions = [];
  for (let [fieldId, value] of Object.entries(filters))
    if (value != null) {
      if (typeof value == "string")
        conditions.push(`{${fieldId}.EX.'${value.replace(/'/g, "''")}'}`);
      else if (typeof value == "number")
        conditions.push(`{${fieldId}.EX.${value}}`);
      else if (typeof value == "boolean")
        conditions.push(`{${fieldId}.EX.${value}}`);
      else if (Array.isArray(value)) {
        let arrayConditions = value.map(
          (v) => typeof v == "string" ? `{${fieldId}.EX.'${v.replace(/'/g, "''")}'}` : `{${fieldId}.EX.${v}}`
        );
        conditions.push(`(${arrayConditions.join("OR")})`);
      }
    }
  return conditions.join("AND");
}
function validateRequiredFields(data, requiredFields) {
  let missingFields = requiredFields.filter(
    (field) => data[field] === void 0 || data[field] === null || data[field] === ""
  );
  if (missingFields.length > 0)
    throw new Error(`Missing required fields: ${missingFields.join(", ")}`);
}

// ../ap/packages/pieces/community/quickbase/src/lib/actions/find-record.ts
var findRecord = (0, import_pieces_framework6.createAction)({
  name: "find_record",
  displayName: "Find Record",
  description: "Search for records in a Quickbase table using filters",
  audience: "both",
  aiMetadata: {
    description: "Query records from a Quickbase table, optionally narrowing with filter conditions, sorting, and a max-record cap. With no filters it returns all records (up to the limit); with filters it returns only matching rows. Use to look up or list records before acting on them. Requires the app and table. Read-only and idempotent.",
    idempotent: !0
  },
  auth: quickbaseAuth,
  props: {
    appId: appIdProp,
    tableId: tableIdProp,
    filters: filtersProp,
    sortField: sortFieldProp,
    sortOrder: sortOrderProp,
    maxRecords: maxRecordsProp
  },
  async run(context) {
    let { appId, tableId, filters, sortField, sortOrder, maxRecords } = context.propsValue, client = new QuickbaseClient(context.auth.props.realmHostname, context.auth.props.userToken), tableFields = await client.get(`/fields?tableId=${tableId}`), whereClause = filters ? buildWhereClause(filters) : "", query = {
      from: tableId,
      select: tableFields.map((f) => f.id),
      where: whereClause,
      options: {
        top: maxRecords || 100
      }
    };
    sortField && (query.sortBy = [{
      fieldId: parseInt(sortField),
      order: sortOrder || "ASC"
    }]);
    let response = await client.post("/records/query", query);
    return {
      records: response.data.map((record) => {
        var _a, _b;
        return {
          id: (_a = record[3]) == null ? void 0 : _a.value,
          displayName: (_b = record[3]) == null ? void 0 : _b.value,
          raw: extractRecordValues(record)
        };
      }),
      totalRecords: response.metadata.totalRecords,
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/quickbase/src/lib/actions/find-or-create-record.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var findOrCreateRecord = (0, import_pieces_framework7.createAction)({
  name: "find_or_create_record",
  displayName: "Find or Create Record",
  description: "Find an existing record or create a new one if not found",
  audience: "both",
  aiMetadata: {
    description: "Look up a record by matching a chosen merge field value, returning it if found, otherwise create a new record with the supplied fields. Use to avoid duplicates when the record may or may not already exist. Requires the app, table, a merge field, and a value for it. Idempotent on the merge key: once the record exists, repeat calls find rather than recreate it.",
    idempotent: !0
  },
  auth: quickbaseAuth,
  props: {
    appId: appIdProp,
    tableId: tableIdProp,
    mergeField: mergeFieldProp,
    fields: fieldsMapperProp
  },
  async run(context) {
    var _a, _b;
    let { appId, tableId, mergeField, fields } = context.propsValue, client = new QuickbaseClient(context.auth.props.realmHostname, context.auth.props.userToken), tableFields = await client.get(
      `/fields?tableId=${tableId}`
    ), mergeValue = fields[mergeField] || fields[((_a = tableFields.find((f) => f.id.toString() === mergeField)) == null ? void 0 : _a.label) || ""];
    if (!mergeValue)
      throw new Error("Merge field value is required");
    let searchQuery = {
      from: tableId,
      select: tableFields.map((f) => f.id),
      where: `{${mergeField}.EX.'${mergeValue}'}`,
      options: { top: 1 }
    };
    try {
      let searchResponse = await client.post(
        "/records/query",
        searchQuery
      );
      if (searchResponse.data.length > 0) {
        let existingRecord = searchResponse.data[0];
        return {
          recordId: (_b = existingRecord[3]) == null ? void 0 : _b.value,
          record: extractRecordValues(existingRecord),
          created: !1,
          found: !0,
          success: !0
        };
      }
    } catch (error) {
      console.log(error);
    }
    let requiredFields = tableFields.filter((f) => f.required).map((f) => f.id.toString());
    validateRequiredFields(fields, requiredFields);
    let recordData = mapFieldsToRecord(fields, tableFields), createResponse = await client.post(
      "/records",
      {
        to: tableId,
        data: [recordData],
        fieldsToReturn: tableFields.map((f) => f.id)
      }
    );
    return {
      recordId: createResponse.metadata.createdRecordIds[0],
      record: createResponse.data[0],
      created: !0,
      found: !1,
      success: !0
    };
  }
});

// ../ap/packages/pieces/community/quickbase/src/lib/actions/create-update-records-bulk.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var createUpdateRecordsBulk = (0, import_pieces_framework8.createAction)({
  name: "create_update_records_bulk",
  displayName: "Create / Update Records From Array",
  description: "Bulk create or update multiple records based on a merge key",
  audience: "both",
  aiMetadata: {
    description: "Upsert many Quickbase records in one call from an array, chunked in batches of 1000. A merge-behavior mode controls handling of existing rows: overwrite all fields on the matched merge key, update only the provided fields, or always insert new records (no merge). Use for batch imports or syncs. Requires the app, table, merge field, and records array. Idempotent only in the merge modes (keyed on the merge field); the always-create mode inserts new rows on each call and is not idempotent.",
    idempotent: !1
  },
  auth: quickbaseAuth,
  props: {
    appId: appIdProp,
    tableId: tableIdProp,
    mergeField: mergeFieldProp,
    records: import_pieces_framework8.Property.Array({
      displayName: "Records",
      description: "Array of records to create or update",
      required: !0
    }),
    mergeFieldBehavior: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Merge Behavior",
      description: "How to handle existing records when upserting",
      required: !0,
      defaultValue: "merge-overwrite",
      options: {
        options: [
          {
            label: "Merge and overwrite all fields",
            value: "merge-overwrite"
          },
          {
            label: "Merge and update only provided fields",
            value: "merge-update"
          },
          {
            label: "Always create new records (no merge)",
            value: "always-create"
          }
        ]
      }
    })
  },
  async run(context) {
    let { appId, tableId, mergeField, records, mergeFieldBehavior } = context.propsValue, client = new QuickbaseClient(context.auth.props.realmHostname, context.auth.props.userToken);
    if (!Array.isArray(records) || records.length === 0)
      throw new Error("Records array is required and cannot be empty");
    let tableFields = await client.get(
      `/fields?tableId=${tableId}`
    ), requiredFields = tableFields.filter((f) => f.required).map((f) => f.id.toString()), processedRecords = records.map((record) => (mergeFieldBehavior !== "always-create" && validateRequiredFields(record, requiredFields), mapFieldsToRecord(record, tableFields))), chunks = chunkArray(processedRecords, 1e3), results = {
      createdRecords: [],
      updatedRecords: [],
      unchangedRecords: [],
      totalProcessed: 0,
      success: !0
    };
    for (let chunk of chunks) {
      let requestData = {
        to: tableId,
        data: chunk,
        fieldsToReturn: tableFields.map((f) => f.id)
      };
      mergeFieldBehavior !== "always-create" && (requestData.mergeFieldId = parseInt(mergeField), requestData.upsert = mergeFieldBehavior === "merge-overwrite" ? "true" : "false");
      let response = await client.post(
        "/records",
        requestData
      );
      results.createdRecords.push(...response.metadata.createdRecordIds), results.updatedRecords.push(...response.metadata.updatedRecordIds), results.unchangedRecords.push(...response.metadata.unchangedRecordIds), results.totalProcessed += response.metadata.totalNumberOfRecordsProcessed;
    }
    return results;
  }
});

// ../ap/packages/pieces/community/quickbase/src/lib/triggers/new-record.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), props = {
  appId: appIdProp,
  tableId: tableIdProp
}, polling = {
  strategy: import_pieces_common2.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    let { appId, tableId } = propsValue, client = new QuickbaseClient(auth.props.realmHostname, auth.props.userToken), tableFields = await client.get(`/fields?tableId=${tableId}`), dateCreatedField = tableFields.find(
      (f) => f.fieldType === "timestamp" && f.label.toLowerCase().includes("created")
    );
    if (!dateCreatedField)
      throw new Error(
        'No date created field found in table. A timestamp field with "created" in the name is required.'
      );
    let query = {
      from: tableId,
      select: tableFields.map((f) => f.id),
      sortBy: [{ fieldId: dateCreatedField.id, order: "DESC" }],
      options: { top: 100 }
    }, response = await client.post("/records/query", query), isTest = lastFetchEpochMS === 0;
    return response.data.map((record) => {
      var _a, _b, _c, _d;
      let recordId = (_a = record[3]) == null ? void 0 : _a.value, dateCreated = (_b = record[dateCreatedField.id.toString()]) == null ? void 0 : _b.value;
      return {
        epochMilliSeconds: dateCreated ? new Date(dateCreated).getTime() : Date.now(),
        data: {
          id: generateDeduplicationKey(recordId, dateCreated),
          created_at: dateCreated,
          updated_at: ((_d = record[((_c = tableFields.find(
            (f) => f.fieldType === "timestamp" && f.label.toLowerCase().includes("modified")
          )) == null ? void 0 : _c.id.toString()) || ""]) == null ? void 0 : _d.value) || dateCreated,
          recordId,
          tableId,
          appId,
          fields: extractRecordValues(record)
        }
      };
    }).filter((item) => isTest || item.epochMilliSeconds > lastFetchEpochMS);
  }
}, newRecord = (0, import_pieces_framework9.createTrigger)({
  name: "new_record",
  displayName: "New Record",
  description: "Triggers when a new record is created in a Quickbase table",
  aiMetadata: {
    description: 'Fires when a new record is added to the selected Quickbase table. Detected by polling the table ordered on its created-timestamp field, so the table must have a timestamp field whose name contains "created". Represents a freshly created row, not edits to existing ones.'
  },
  auth: quickbaseAuth,
  props,
  type: import_pieces_framework9.TriggerStrategy.POLLING,
  sampleData: {},
  async onEnable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common2.pollingHelper.onEnable(polling, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common2.pollingHelper.onDisable(polling, { store, auth, propsValue });
  },
  async run(context) {
    let { store, auth, propsValue, files } = context;
    return await import_pieces_common2.pollingHelper.poll(polling, { store, auth, propsValue, files });
  },
  async test(context) {
    let { store, auth, propsValue, files } = context;
    return await import_pieces_common2.pollingHelper.test(polling, { store, auth, propsValue, files });
  }
});

// ../ap/packages/pieces/community/quickbase/src/lib/triggers/new-or-updated-record.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var props2 = {
  appId: appIdProp,
  tableId: tableIdProp
}, polling2 = {
  strategy: import_pieces_common3.DedupeStrategy.TIMEBASED,
  items: async ({ auth, propsValue, lastFetchEpochMS }) => {
    var _a;
    let { tableId } = propsValue, client = new QuickbaseClient(auth.props.realmHostname, auth.props.userToken), fields = await client.get(`/fields?tableId=${tableId}`), select = fields.map((f) => f.id), modifiedField = fields.find(
      (f) => /modified/i.test(f.label) && (f.fieldType === "timestamp" || f.fieldType === "date")
    ), anyTimeField = fields.find((f) => f.fieldType === "timestamp" || f.fieldType === "date"), timeFieldId = ((_a = modifiedField || anyTimeField) == null ? void 0 : _a.id) || 6, query = {
      from: tableId,
      select,
      sortBy: [
        {
          fieldId: timeFieldId,
          order: "DESC"
        }
      ],
      options: {
        top: 100
      }
    }, response = await client.post("/records/query", query), isTest = lastFetchEpochMS === 0;
    return response.data.map((record) => {
      var _a2;
      let timeFieldKey = timeFieldId.toString(), rawValue = (_a2 = record[timeFieldKey]) == null ? void 0 : _a2.value, epoch;
      return rawValue ? epoch = new Date(rawValue).getTime() : epoch = Date.now(), {
        epochMilliSeconds: epoch,
        data: record
      };
    }).filter((item) => item.epochMilliSeconds > lastFetchEpochMS);
  }
}, newOrUpdatedRecord = (0, import_pieces_framework10.createTrigger)({
  auth: quickbaseAuth,
  name: "new_or_updated_record",
  displayName: "New or Updated Record",
  description: "Triggers when a record is created or updated in a Quickbase table",
  aiMetadata: {
    description: "Fires when a record in the selected Quickbase table is either created or modified. Detected by polling the table ordered on its most recent modified/created timestamp field, so it surfaces both brand-new rows and edits to existing ones. Use when changes to records matter, not just new ones."
  },
  props: props2,
  type: import_pieces_framework10.TriggerStrategy.POLLING,
  sampleData: {},
  async onEnable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common3.pollingHelper.onEnable(polling2, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common3.pollingHelper.onDisable(polling2, { store, auth, propsValue });
  },
  async run(context) {
    let { store, auth, propsValue, files } = context;
    return await import_pieces_common3.pollingHelper.poll(polling2, { store, auth, propsValue, files });
  },
  async test(context) {
    let { store, auth, propsValue, files } = context;
    return await import_pieces_common3.pollingHelper.test(polling2, { store, auth, propsValue, files });
  }
});

// ../ap/packages/pieces/community/quickbase/src/index.ts
var quickbase = (0, import_pieces_framework11.createPiece)({
  displayName: "Quickbase",
  auth: quickbaseAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/quickbase.png",
  categories: [import_pieces_framework12.PieceCategory.PRODUCTIVITY],
  authors: ["sparkybug", "sanket-a11y"],
  actions: [
    createRecord,
    updateRecord,
    deleteRecord,
    findRecord,
    findOrCreateRecord,
    createUpdateRecordsBulk,
    (0, import_pieces_common4.createCustomApiCallAction)({
      auth: quickbaseAuth,
      baseUrl: (auth) => "https://api.quickbase.com/v1",
      authMapping: async (auth) => ({
        "QB-Realm-Hostname": auth.props.realmHostname,
        Authorization: `QB-USER-TOKEN ${auth.props.userToken}`,
        "Content-Type": "application/json"
      })
    })
  ],
  triggers: [newRecord, newOrUpdatedRecord]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  quickbase
});
