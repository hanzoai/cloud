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

// ../ap/packages/pieces/community/fireberry/src/index.ts
var index_exports = {};
__export(index_exports, {
  fireberry: () => fireberry
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/fireberry/src/lib/actions/create-record.action.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/fireberry/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), fireberryAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Enter your Fireberry API Key. You can generate it from your Fireberry account settings.",
  required: !0
});

// ../ap/packages/pieces/community/fireberry/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/fireberry/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var FIREBERRY_API_BASE_URL = "https://api.fireberry.com", MAX_RETRIES = 3, RETRY_DELAY_MS = 1e3;
function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
function normalizeQueryParams(params) {
  if (!params) return;
  let result = {};
  for (let key in params)
    Object.prototype.hasOwnProperty.call(params, key) && (result[key] = String(params[key]));
  return result;
}
var FireberryClient = class {
  constructor(apiKey) {
    __publicField(this, "apiKey");
    this.apiKey = apiKey.secret_text;
  }
  parseError(error) {
    var _a;
    if ((_a = error == null ? void 0 : error.response) != null && _a.body)
      try {
        let body = typeof error.response.body == "string" ? JSON.parse(error.response.body) : error.response.body;
        if (body != null && body.error) {
          if (typeof body.error == "string") return body.error;
          if (body.error.message) return body.error.message;
        }
        if (body != null && body.message) return body.message;
      } catch (e) {
        return "Unknown error";
      }
    return error != null && error.message ? error.message : "Unknown error";
  }
  shouldRetry(status) {
    return status === 429 || status >= 500 && status < 600;
  }
  async request({
    method,
    resourceUri,
    body,
    queryParams
  }) {
    var _a;
    let request = {
      method,
      url: `${FIREBERRY_API_BASE_URL}${resourceUri}`,
      headers: {
        tokenid: this.apiKey,
        "Content-Type": "application/json"
      },
      body,
      queryParams: normalizeQueryParams(queryParams),
      timeout: 1e4
      // 10 seconds
    }, lastError = null;
    for (let attempt = 0; attempt < MAX_RETRIES; attempt++)
      try {
        let response = await import_pieces_common.httpClient.sendRequest(request);
        if (response.status >= 200 && response.status < 300)
          return response.body;
        {
          let errorMsg = this.parseError({ response });
          if (this.shouldRetry(response.status) && attempt < MAX_RETRIES - 1) {
            await delay(RETRY_DELAY_MS * (attempt + 1));
            continue;
          }
          throw new Error(`Fireberry API error (${response.status}): ${errorMsg}`);
        }
      } catch (error) {
        let status = (_a = error == null ? void 0 : error.response) == null ? void 0 : _a.status;
        if (status && this.shouldRetry(status) && attempt < MAX_RETRIES - 1) {
          await delay(RETRY_DELAY_MS * (attempt + 1)), lastError = error;
          continue;
        }
        let errorMsg = this.parseError(error);
        throw new Error(`Fireberry API error${status ? ` (${status})` : ""}: ${errorMsg}`);
      }
    throw new Error(`Fireberry API error: ${this.parseError(lastError)}`);
  }
  async getObjectsMetadata() {
    return this.request({
      method: import_pieces_common.HttpMethod.GET,
      resourceUri: "/metadata/records"
    });
  }
  async getObjectFieldsMetadata(object) {
    return this.request({
      method: import_pieces_common.HttpMethod.GET,
      resourceUri: `/metadata/records/${object}/fields`
    });
  }
  async getPicklistValues(object, fieldName) {
    return this.request({
      method: import_pieces_common.HttpMethod.GET,
      resourceUri: `/metadata/records/${object}/fields/${fieldName}/values`
    });
  }
  async batchCreate(object, records) {
    return this.request({
      method: import_pieces_common.HttpMethod.POST,
      resourceUri: `/api/v3/record/${object}/batch/create`,
      body: { data: records }
    });
  }
  async batchUpdate(object, records) {
    return this.request({
      method: import_pieces_common.HttpMethod.POST,
      resourceUri: `/api/v3/record/${object}/batch/update`,
      body: { data: records }
    });
  }
  async batchDelete(object, ids) {
    return this.request({
      method: import_pieces_common.HttpMethod.POST,
      resourceUri: `/api/v3/record/${object}/batch/delete`,
      body: { data: ids }
    });
  }
};

// ../ap/packages/pieces/community/fireberry/src/lib/common/props.ts
var objectTypeDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Object Type",
  required: !0,
  refreshers: [],
  auth: fireberryAuth,
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await new FireberryClient(auth).getObjectsMetadata()).data.map((obj) => ({
      label: obj.name,
      value: obj.systemName
    }))
  } : {
    disabled: !0,
    options: [],
    placeholder: "Connect your Fireberry account"
  }
}), objectFields = import_pieces_framework2.Property.DynamicProperties({
  displayName: "Fields",
  refreshers: ["objectType"],
  required: !0,
  auth: fireberryAuth,
  props: async ({ auth, objectType }) => {
    var _a;
    if (!auth || !objectType) return {};
    let objectTypeStr = typeof objectType == "string" ? objectType : objectType == null ? void 0 : objectType.value, client = new FireberryClient(auth), metadata = await client.getObjectFieldsMetadata(objectTypeStr), props = {}, fieldTypeMap = {
      "a1e7ed6f-5083-477b-b44c-9943a6181359": "text",
      "ce972d02-5013-46d4-9d1d-f09df1ac346a": "datetime",
      "6a34bfe3-fece-4da1-9136-a7b1e5ae3319": "number",
      "a8fcdf65-91bc-46fd-82f6-1234758345a1": "lookup",
      "b4919f2e-2996-48e4-a03c-ba39fb64386c": "picklist",
      "80108f9d-1e75-40fa-9fa9-02be4ddc1da1": "longtext"
    }, picklistCache = {}, picklistFields = metadata.data.filter(
      (field) => fieldTypeMap[field.systemFieldTypeId] === "picklist"
    );
    for (let field of picklistFields)
      if (!["objecttypecode", "resultcode"].includes(field.fieldName.toLowerCase()))
        try {
          let picklistData = await client.getPicklistValues(objectTypeStr, field.fieldName);
          (_a = picklistData.data) != null && _a.values && Array.isArray(picklistData.data.values) && (picklistCache[field.fieldName] = picklistData.data.values);
        } catch (error) {
          picklistCache[field.fieldName] = [];
        }
    for (let field of metadata.data) {
      let systemFields = ["createdby", "modifiedby", "deletedby", "createdon", "modifiedon", "deletedon"];
      if (field.fieldName.endsWith("id") && !field.label || systemFields.includes(field.fieldName.toLowerCase()))
        continue;
      let fieldType = fieldTypeMap[field.systemFieldTypeId] || "text", isRequired = !1;
      switch (fieldType) {
        case "text":
          props[field.fieldName] = import_pieces_framework2.Property.ShortText({
            displayName: field.label || field.fieldName,
            required: isRequired
          });
          break;
        case "number":
          props[field.fieldName] = import_pieces_framework2.Property.Number({
            displayName: field.label || field.fieldName,
            required: isRequired
          });
          break;
        case "datetime":
          props[field.fieldName] = import_pieces_framework2.Property.DateTime({
            displayName: field.label || field.fieldName,
            required: isRequired,
            description: "Date and time in UTC format"
          });
          break;
        case "picklist": {
          if (["objecttypecode", "resultcode"].includes(field.fieldName.toLowerCase()))
            props[field.fieldName] = import_pieces_framework2.Property.ShortText({
              displayName: field.label || field.fieldName,
              required: isRequired,
              description: "Enter the numeric value for this field"
            });
          else {
            let values = picklistCache[field.fieldName] || [];
            if (values.length > 0 && values.length <= 20) {
              let options = values.map((option) => ({
                label: option.name || option.value,
                value: option.value
              }));
              props[field.fieldName] = import_pieces_framework2.Property.StaticDropdown({
                displayName: field.label || field.fieldName,
                required: isRequired,
                options: {
                  disabled: !1,
                  options
                }
              });
            } else
              props[field.fieldName] = import_pieces_framework2.Property.ShortText({
                displayName: field.label || field.fieldName,
                required: isRequired,
                description: values.length > 20 ? `Enter numeric value (${values.length} options available)` : "Enter the value for this field"
              });
          }
          break;
        }
        case "longtext": {
          props[field.fieldName] = import_pieces_framework2.Property.LongText({
            displayName: field.label || field.fieldName,
            required: isRequired,
            description: "Long text content"
          });
          break;
        }
        case "lookup": {
          let description = "Record ID (GUID)";
          field.fieldName.includes("account") ? description = "Account record ID" : field.fieldName.includes("contact") ? description = "Contact record ID" : field.fieldName.includes("owner") ? description = "User record ID for owner" : field.fieldName.includes("product") ? description = "Product record ID" : field.fieldName.includes("user") && (description = "User record ID"), props[field.fieldName] = import_pieces_framework2.Property.ShortText({
            displayName: field.label || field.fieldName,
            required: isRequired,
            description
          });
          break;
        }
        default: {
          props[field.fieldName] = import_pieces_framework2.Property.ShortText({
            displayName: field.label || field.fieldName,
            required: isRequired,
            description: `${fieldType} field`
          });
          break;
        }
      }
    }
    return props;
  }
});

// ../ap/packages/pieces/community/fireberry/src/lib/actions/create-record.action.ts
var createRecordAction = (0, import_pieces_framework3.createAction)({
  name: "create_record",
  displayName: "Create Record",
  description: "Create a new record in Fireberry.",
  audience: "both",
  aiMetadata: { description: "Creates a new record of a given Fireberry object type (Account, Contact, etc.), with field values supplied as an object/JSON. Use when adding a new CRM record. Not idempotent: each call inserts another record, so repeating it produces duplicates.", idempotent: !1 },
  auth: fireberryAuth,
  props: {
    objectType: objectTypeDropdown,
    fields: objectFields
  },
  async run({ auth, propsValue }) {
    let client = new FireberryClient(auth), { objectType, fields } = propsValue, fieldsObj = typeof fields == "string" ? JSON.parse(fields) : fields;
    if (typeof fieldsObj != "object" || fieldsObj === null)
      throw new Error("Fields must be an object");
    return await client.batchCreate(objectType, [fieldsObj]);
  }
});

// ../ap/packages/pieces/community/fireberry/src/lib/actions/update-record.action.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var recordDropdown = import_pieces_framework4.Property.Dropdown({
  auth: fireberryAuth,
  displayName: "Record",
  required: !0,
  refreshers: ["objectType"],
  options: async ({ auth, objectType }) => {
    var _a, _b;
    if (!auth || !objectType)
      return {
        disabled: !0,
        options: [],
        placeholder: "Select object type first"
      };
    try {
      let objectTypeStr = typeof objectType == "string" ? objectType : objectType == null ? void 0 : objectType.value, response = await new FireberryClient(auth).request({
        method: import_pieces_common2.HttpMethod.GET,
        resourceUri: `/api/record/${objectTypeStr}?$top=50`
      });
      if (!((_a = response.data) != null && _a.Records) || !Array.isArray(response.data.Records))
        return {
          disabled: !1,
          options: [],
          placeholder: ((_b = response.data) == null ? void 0 : _b.Total_Records) === 0 ? "No records found" : "Error loading records"
        };
      let primaryField = response.data.PrimaryField, primaryKey = response.data.PrimaryKey;
      return {
        disabled: !1,
        options: response.data.Records.map((record) => {
          var _a2;
          return {
            label: record[primaryField] || record.name || record.title || record.subject || record.firstname || record.lastname || record.email || record.accountname || record.contactname || `Record ${((_a2 = record[primaryKey]) == null ? void 0 : _a2.substring(0, 8)) || "Unknown"}`,
            value: record[primaryKey]
          };
        }).slice(0, 50)
      };
    } catch (error) {
      return {
        disabled: !1,
        options: [],
        placeholder: "Error loading records"
      };
    }
  }
}), updateFields = import_pieces_framework4.Property.DynamicProperties({
  displayName: "Fields to Update",
  refreshers: ["objectType"],
  required: !0,
  auth: fireberryAuth,
  props: async ({ auth, objectType }) => {
    var _a;
    if (!auth || !objectType) return {};
    let objectTypeStr = typeof objectType == "string" ? objectType : objectType == null ? void 0 : objectType.value, client = new FireberryClient(auth);
    try {
      let metadata = await client.getObjectFieldsMetadata(objectTypeStr), props = {}, fieldTypeMap = {
        "a1e7ed6f-5083-477b-b44c-9943a6181359": "text",
        "ce972d02-5013-46d4-9d1d-f09df1ac346a": "datetime",
        "6a34bfe3-fece-4da1-9136-a7b1e5ae3319": "number",
        "a8fcdf65-91bc-46fd-82f6-1234758345a1": "lookup",
        "b4919f2e-2996-48e4-a03c-ba39fb64386c": "picklist",
        "80108f9d-1e75-40fa-9fa9-02be4ddc1da1": "longtext"
      }, picklistCache = {}, picklistFields = metadata.data.filter(
        (field) => fieldTypeMap[field.systemFieldTypeId] === "picklist"
      );
      for (let field of picklistFields)
        if (!["objecttypecode", "resultcode"].includes(field.fieldName.toLowerCase()))
          try {
            let picklistData = await client.getPicklistValues(objectTypeStr, field.fieldName);
            (_a = picklistData.data) != null && _a.values && Array.isArray(picklistData.data.values) && (picklistCache[field.fieldName] = picklistData.data.values);
          } catch (error) {
            picklistCache[field.fieldName] = [];
          }
      for (let field of metadata.data) {
        let systemFields = ["createdby", "modifiedby", "deletedby", "createdon", "modifiedon", "deletedon"];
        if (field.fieldName.endsWith("id") && !field.label || systemFields.includes(field.fieldName.toLowerCase()))
          continue;
        let fieldType = fieldTypeMap[field.systemFieldTypeId] || "text", isRequired = !1;
        switch (fieldType) {
          case "text":
            props[field.fieldName] = import_pieces_framework4.Property.ShortText({
              displayName: field.label || field.fieldName,
              required: isRequired,
              description: "Leave empty to keep current value"
            });
            break;
          case "number":
            props[field.fieldName] = import_pieces_framework4.Property.Number({
              displayName: field.label || field.fieldName,
              required: isRequired,
              description: "Leave empty to keep current value"
            });
            break;
          case "datetime":
            props[field.fieldName] = import_pieces_framework4.Property.DateTime({
              displayName: field.label || field.fieldName,
              required: isRequired,
              description: "Leave empty to keep current value"
            });
            break;
          case "picklist": {
            if (["objecttypecode", "resultcode"].includes(field.fieldName.toLowerCase()))
              props[field.fieldName] = import_pieces_framework4.Property.ShortText({
                displayName: field.label || field.fieldName,
                required: isRequired,
                description: "Enter numeric value (leave empty to keep current)"
              });
            else {
              let values = picklistCache[field.fieldName] || [];
              if (values.length > 0 && values.length <= 20) {
                let options = values.map((option) => ({
                  label: option.name || option.value,
                  value: option.value
                }));
                props[field.fieldName] = import_pieces_framework4.Property.StaticDropdown({
                  displayName: field.label || field.fieldName,
                  required: isRequired,
                  options: {
                    disabled: !1,
                    options: [
                      { label: "-- Keep Current Value --", value: "" },
                      ...options
                    ]
                  }
                });
              } else
                props[field.fieldName] = import_pieces_framework4.Property.ShortText({
                  displayName: field.label || field.fieldName,
                  required: isRequired,
                  description: values.length > 20 ? `Enter numeric value (${values.length} options available, leave empty to keep current)` : "Enter value (leave empty to keep current)"
                });
            }
            break;
          }
          case "longtext": {
            props[field.fieldName] = import_pieces_framework4.Property.LongText({
              displayName: field.label || field.fieldName,
              required: isRequired,
              description: "Leave empty to keep current value"
            });
            break;
          }
          case "lookup": {
            let description = "Record ID (leave empty to keep current)";
            field.fieldName.includes("account") ? description = "Account record ID (leave empty to keep current)" : field.fieldName.includes("contact") ? description = "Contact record ID (leave empty to keep current)" : field.fieldName.includes("owner") ? description = "User record ID for owner (leave empty to keep current)" : field.fieldName.includes("product") ? description = "Product record ID (leave empty to keep current)" : field.fieldName.includes("user") && (description = "User record ID (leave empty to keep current)"), props[field.fieldName] = import_pieces_framework4.Property.ShortText({
              displayName: field.label || field.fieldName,
              required: isRequired,
              description
            });
            break;
          }
          default: {
            props[field.fieldName] = import_pieces_framework4.Property.ShortText({
              displayName: field.label || field.fieldName,
              required: isRequired,
              description: "Leave empty to keep current value"
            });
            break;
          }
        }
      }
      return props;
    } catch (error) {
      return console.error("Error fetching update fields:", error), {};
    }
  }
}), updateRecordAction = (0, import_pieces_framework4.createAction)({
  name: "update_record",
  displayName: "Update Record",
  description: "Update an existing record in Fireberry.",
  audience: "both",
  aiMetadata: { description: "Updates an existing Fireberry record, identified by its record ID under a given object type, applying only the field values you provide (empty fields are skipped and left unchanged). Use when modifying a known record. Idempotent: re-running with the same id and field values converges on the same record state.", idempotent: !0 },
  auth: fireberryAuth,
  props: {
    objectType: objectTypeDropdown,
    recordId: recordDropdown,
    fields: updateFields
  },
  async run({ auth, propsValue }) {
    let client = new FireberryClient(auth), { objectType, recordId, fields } = propsValue;
    if (!recordId)
      throw new Error("Record ID is required");
    let fieldsObj = typeof fields == "string" ? JSON.parse(fields) : fields;
    if (typeof fieldsObj != "object" || fieldsObj === null)
      throw new Error("Fields must be an object");
    let updateData = {};
    for (let [key, value] of Object.entries(fieldsObj))
      value !== "" && value !== null && value !== void 0 && (updateData[key] = value);
    let recordToUpdate = { id: recordId, record: updateData };
    return await client.batchUpdate(objectType, [recordToUpdate]);
  }
});

// ../ap/packages/pieces/community/fireberry/src/lib/actions/delete-record.action.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var recordsToDeleteDropdown = import_pieces_framework5.Property.MultiSelectDropdown({
  auth: fireberryAuth,
  displayName: "Records to Delete",
  required: !0,
  refreshers: ["objectType"],
  options: async ({ auth, objectType }) => {
    var _a, _b;
    if (!auth || !objectType)
      return {
        disabled: !0,
        options: [],
        placeholder: "Select object type first"
      };
    try {
      let objectTypeStr = typeof objectType == "string" ? objectType : objectType == null ? void 0 : objectType.value, response = await new FireberryClient(auth).request({
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: `/api/record/${objectTypeStr}?$top=100`
      });
      if (!((_a = response.data) != null && _a.Records) || !Array.isArray(response.data.Records))
        return {
          disabled: !1,
          options: [],
          placeholder: ((_b = response.data) == null ? void 0 : _b.Total_Records) === 0 ? "No records found" : "Error loading records"
        };
      let primaryField = response.data.PrimaryField, primaryKey = response.data.PrimaryKey;
      return {
        disabled: !1,
        options: response.data.Records.map((record) => {
          var _a2;
          return {
            label: record[primaryField] || record.name || record.title || record.subject || record.firstname || record.lastname || record.email || record.accountname || record.contactname || `Record ${((_a2 = record[primaryKey]) == null ? void 0 : _a2.substring(0, 8)) || "Unknown"}`,
            value: record[primaryKey]
          };
        }).slice(0, 100)
      };
    } catch (error) {
      return {
        disabled: !1,
        options: [],
        placeholder: "Error loading records"
      };
    }
  }
}), deleteRecordAction = (0, import_pieces_framework5.createAction)({
  name: "delete_record",
  displayName: "Delete Records",
  description: "Delete records from Fireberry.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes one or more Fireberry records (max 20 per call) of a given object type, selected by their record IDs; requires the confirmation flag to be set. Use when removing known records. Not idempotent and irreversible: a repeat call on already-deleted IDs has no further effect but the deletion itself cannot be undone.", idempotent: !1 },
  auth: fireberryAuth,
  props: {
    objectType: objectTypeDropdown,
    recordIds: recordsToDeleteDropdown,
    confirmDeletion: import_pieces_framework5.Property.Checkbox({
      displayName: "Confirm Deletion",
      required: !0,
      description: "Check this box to confirm you want to permanently delete the selected records. This action cannot be undone."
    })
  },
  async run({ auth, propsValue }) {
    let client = new FireberryClient(auth), { objectType, recordIds, confirmDeletion } = propsValue;
    if (!confirmDeletion)
      throw new Error("You must confirm deletion by checking the confirmation box");
    if (!recordIds || !Array.isArray(recordIds) || recordIds.length === 0)
      throw new Error("At least one record must be selected for deletion");
    if (recordIds.length > 20)
      throw new Error("Maximum 20 records can be deleted at once");
    let result = await client.batchDelete(objectType, recordIds);
    return {
      success: !0,
      deletedCount: recordIds.length,
      recordIds,
      message: `Successfully deleted ${recordIds.length} record(s)`,
      apiResponse: result
    };
  }
});

// ../ap/packages/pieces/community/fireberry/src/lib/actions/find-record.action.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var fieldsToReturn = import_pieces_framework6.Property.DynamicProperties({
  displayName: "Fields to Return",
  refreshers: ["objectType"],
  required: !1,
  auth: fireberryAuth,
  props: async ({ auth, objectType }) => {
    if (!auth || !objectType) return {};
    let objectTypeStr = typeof objectType == "string" ? objectType : objectType == null ? void 0 : objectType.value, client = new FireberryClient(auth);
    try {
      let metadata = await client.getObjectFieldsMetadata(objectTypeStr), props = {};
      for (let field of metadata.data) {
        let systemFields = ["createdby", "modifiedby", "deletedby"];
        field.fieldName.endsWith("id") && !field.label || systemFields.includes(field.fieldName.toLowerCase()) || (props[field.fieldName] = import_pieces_framework6.Property.Checkbox({
          displayName: field.label || field.fieldName,
          required: !1,
          description: `Include ${field.label || field.fieldName} in search results`
        }));
      }
      return props;
    } catch (error) {
      return console.error("Error fetching fields for selection:", error), {};
    }
  }
}), findRecordAction = (0, import_pieces_framework6.createAction)({
  name: "find_record",
  displayName: "Find Records",
  description: "Search for records in Fireberry.",
  audience: "both",
  aiMetadata: { description: 'Queries records of a given Fireberry object type with optional pagination, sorting, and field selection. Leave the search query empty to fetch all records, or supply criteria (e.g. "accountname=John") to filter to matches. Use to look up or list records before acting on them. Read-only and idempotent.', idempotent: !0 },
  auth: fireberryAuth,
  props: {
    objectType: objectTypeDropdown,
    searchQuery: import_pieces_framework6.Property.LongText({
      displayName: "Search Query",
      required: !1,
      description: 'Enter search criteria (e.g., "accountname=John" or "email contains @example.com"). Leave empty to get all records.'
    }),
    fieldsToReturn,
    sortBy: import_pieces_framework6.Property.ShortText({
      displayName: "Sort By Field",
      required: !1,
      description: 'System name of field to sort by (e.g., "createdon", "accountname")'
    }),
    sortOrder: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Sort Order",
      required: !1,
      defaultValue: "desc",
      options: {
        disabled: !1,
        options: [
          { label: "Descending (newest first)", value: "desc" },
          { label: "Ascending (oldest first)", value: "asc" }
        ]
      }
    }),
    pageSize: import_pieces_framework6.Property.Number({
      displayName: "Page Size",
      required: !1,
      defaultValue: 25,
      description: "Number of records to return (max 50)"
    }),
    pageNumber: import_pieces_framework6.Property.Number({
      displayName: "Page Number",
      required: !1,
      defaultValue: 1,
      description: "Page number to retrieve (max 10)"
    })
  },
  async run({ auth, propsValue }) {
    let client = new FireberryClient(auth), { objectType, searchQuery, fieldsToReturn: fieldsToReturn2, sortBy, sortOrder, pageSize, pageNumber } = propsValue, selectedFields = [];
    if (fieldsToReturn2 && typeof fieldsToReturn2 == "object")
      for (let [fieldName, isSelected] of Object.entries(fieldsToReturn2))
        isSelected === !0 && selectedFields.push(fieldName);
    let targetObject = (await client.getObjectsMetadata()).data.find((obj) => obj.systemName === objectType);
    if (!targetObject)
      throw new Error(`Object type '${objectType}' not found`);
    let queryBody = {
      objecttype: parseInt(targetObject.objectType)
    };
    selectedFields.length > 0 && (queryBody.fields = selectedFields.join(",")), searchQuery && searchQuery.trim() && (queryBody.query = searchQuery.trim()), sortBy && sortBy.trim() && (queryBody.sort_by = sortBy.trim(), queryBody.sort_type = sortOrder || "desc"), pageSize && (queryBody.page_size = Math.min(Math.max(1, pageSize), 50)), pageNumber && (queryBody.page_number = Math.min(Math.max(1, pageNumber), 10));
    let response = await client.request({
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: "/api/query",
      body: queryBody
    });
    if (!response.success)
      throw new Error("Query failed");
    return {
      success: !0,
      query: queryBody,
      results: {
        objectName: response.data.ObjectName,
        systemName: response.data.SystemName,
        objectType: response.data.ObjectType,
        pageNumber: response.data.PageNum,
        sortBy: response.data.SortBy,
        sortDescending: response.data.SortBy_Desc,
        isLastPage: response.data.IsLastPage,
        primaryKey: response.data.PrimaryKey,
        primaryField: response.data.PrimaryField,
        columns: response.data.Columns,
        records: response.data.Data,
        recordCount: response.data.Data.length
      }
    };
  }
});

// ../ap/packages/pieces/community/fireberry/src/lib/triggers/record-created-updated.trigger.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var recordCreatedOrUpdatedTrigger = (0, import_pieces_framework7.createTrigger)({
  name: "record_created_or_updated",
  displayName: "Record Created or Updated",
  description: "Fires when a record is created or updated in Fireberry.",
  aiMetadata: {
    description: "Polls a chosen Fireberry object type and fires for records that were recently created or modified. The trigger type selects which events qualify: created only, updated only, or both. Each emitted item represents one matching record."
  },
  auth: fireberryAuth,
  props: {
    objectType: objectTypeDropdown,
    triggerType: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Trigger Type",
      required: !0,
      defaultValue: "both",
      options: {
        disabled: !1,
        options: [
          { label: "Created or Updated", value: "both" },
          { label: "Created Only", value: "created" },
          { label: "Updated Only", value: "updated" }
        ]
      }
    }),
    lookbackMinutes: import_pieces_framework7.Property.Number({
      displayName: "Lookback Period (minutes)",
      required: !1,
      defaultValue: 60,
      description: "How far back to look for records on first run (default: 60 minutes)"
    })
  },
  type: import_pieces_framework7.TriggerStrategy.POLLING,
  sampleData: {
    accountname: "Sample Account",
    emailaddress1: "sample@example.com",
    createdon: "2025-07-17T10:39:30.003",
    modifiedon: "2025-07-17T10:39:30.003",
    accountid: "12345678-1234-1234-1234-123456789abc"
  },
  async test({ auth, propsValue }) {
    let client = new FireberryClient(auth), { objectType } = propsValue;
    if (!objectType)
      return [];
    try {
      let targetObject = (await client.getObjectsMetadata()).data.find((obj) => obj.systemName === objectType);
      if (!targetObject)
        throw new Error(`Object type '${objectType}' not found`);
      let objectNumber = parseInt(targetObject.objectType), sevenDaysAgo = new Date(Date.now() - 10080 * 60 * 1e3).toISOString().split("T")[0], response = await client.request({
        method: import_pieces_common5.HttpMethod.POST,
        resourceUri: "/api/query",
        body: {
          objecttype: objectNumber,
          query: `modifiedon >= '${sevenDaysAgo}'`,
          sort_by: "modifiedon",
          sort_type: "desc",
          page_size: 3
        }
      });
      if (!response.success)
        throw new Error("Failed to fetch sample data from Fireberry");
      let records = response.data.Data || [];
      if (records.length === 0) {
        let fallbackResponse = await client.request({
          method: import_pieces_common5.HttpMethod.POST,
          resourceUri: "/api/query",
          body: {
            objecttype: objectNumber,
            sort_by: "modifiedon",
            sort_type: "desc",
            page_size: 3
          }
        });
        if (fallbackResponse.success && fallbackResponse.data.Data)
          return fallbackResponse.data.Data;
      }
      return records;
    } catch (error) {
      return console.error("Failed to generate sample data:", error), [{
        [objectType === "Contact" ? "firstname" : "accountname"]: "Sample Record",
        createdon: (/* @__PURE__ */ new Date()).toISOString(),
        modifiedon: (/* @__PURE__ */ new Date()).toISOString()
      }];
    }
  },
  async onEnable({ store }) {
    await store.put("lastPollTime", (/* @__PURE__ */ new Date()).toISOString());
  },
  async onDisable({ store }) {
    await store.delete("lastPollTime");
  },
  async run({ auth, propsValue, store }) {
    var _a, _b, _c;
    let client = new FireberryClient(auth), { objectType, triggerType, lookbackMinutes } = propsValue, lastPollTime = await store.get("lastPollTime");
    if (!lastPollTime) {
      let lookback = lookbackMinutes || 60;
      lastPollTime = new Date(Date.now() - lookback * 60 * 1e3).toISOString();
    }
    let targetObject = (await client.getObjectsMetadata()).data.find((obj) => obj.systemName === objectType);
    if (!targetObject)
      throw new Error(`Object type '${objectType}' not found`);
    let objectNumber = parseInt(targetObject.objectType), query = "", cutoffDate = new Date(lastPollTime).toISOString().split("T")[0];
    triggerType === "created" ? query = `createdon >= '${cutoffDate}'` : triggerType === "updated" ? query = `modifiedon >= '${cutoffDate}'` : query = `modifiedon >= '${cutoffDate}'`;
    try {
      let response = await client.request({
        method: import_pieces_common5.HttpMethod.POST,
        resourceUri: "/api/query",
        body: {
          objecttype: objectNumber,
          query,
          sort_by: "modifiedon",
          sort_type: "desc",
          page_size: 50
        }
      });
      if (!response.success)
        throw new Error("Failed to fetch records from Fireberry");
      let records = response.data.Data || [];
      return await store.put("lastPollTime", (/* @__PURE__ */ new Date()).toISOString()), records.reverse();
    } catch (error) {
      throw (_a = error.message) != null && _a.includes("429") ? new Error("Rate limit exceeded. Please try again later.") : (_b = error.message) != null && _b.includes("401") || (_c = error.message) != null && _c.includes("403") ? new Error("Authentication failed. Please check your Fireberry API key.") : (console.error("Fireberry trigger error:", error), new Error(`Failed to fetch records: ${error.message || "Unknown error"}`));
    }
  }
});

// ../ap/packages/pieces/community/fireberry/src/index.ts
var fireberry = (0, import_pieces_framework8.createPiece)({
  displayName: "Fireberry",
  auth: fireberryAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/fireberry.png",
  authors: ["sparkybug", "onyedikachi-david"],
  categories: [import_pieces_framework9.PieceCategory.SALES_AND_CRM],
  actions: [
    createRecordAction,
    updateRecordAction,
    deleteRecordAction,
    findRecordAction
  ],
  triggers: [
    recordCreatedOrUpdatedTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  fireberry
});
