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

// ../ap/packages/pieces/community/knack/src/index.ts
var index_exports = {};
__export(index_exports, {
  knack: () => knack
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/knack/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/knack/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function knackApiCall({
  method,
  resourceUri,
  query,
  body,
  auth
}) {
  var _a, _b;
  let { apiKey, applicationId } = auth.props;
  if (!apiKey || !applicationId)
    throw new Error("Knack API key and Application ID are required for authentication");
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (queryParams[key] = String(value));
  let request = {
    method,
    url: `https://api.knack.com/v1${resourceUri}`,
    headers: {
      "X-Knack-Application-ID": applicationId,
      "X-Knack-REST-API-Key": apiKey,
      "Content-Type": "application/json"
    },
    queryParams,
    body
  };
  try {
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  } catch (error) {
    let statusCode = (_a = error.response) == null ? void 0 : _a.status, errorData = (_b = error.response) == null ? void 0 : _b.data;
    switch (statusCode) {
      case 400:
        throw new Error(
          `Bad Request: ${(errorData == null ? void 0 : errorData.message) || "Invalid request parameters"}. Please check your data and field values.`
        );
      case 401:
        throw new Error(
          "Authentication Failed: Invalid API key or Application ID. Please verify your Knack credentials in the connection settings."
        );
      case 403:
        throw new Error(
          "Access Forbidden: You do not have permission to access this resource. Please check your Knack account permissions."
        );
      case 404:
        throw new Error(
          "Resource Not Found: The requested object or resource does not exist. Please verify the identifier is correct."
        );
      case 429:
        throw new Error(
          "Rate Limit Exceeded: Too many requests in a short time period. Please wait before trying again."
        );
      case 500:
        throw new Error(
          "Internal Server Error: Knack is experiencing technical difficulties. Please try again later or contact Knack support."
        );
      case 502:
      case 503:
      case 504:
        throw new Error(
          "Service Unavailable: Knack service is temporarily unavailable. Please try again in a few minutes."
        );
      default: {
        let errorMessage = (errorData == null ? void 0 : errorData.message) || error.message || "Unknown error occurred";
        throw new Error(
          `Knack API Error (${statusCode || "Unknown"}): ${errorMessage}`
        );
      }
    }
  }
}

// ../ap/packages/pieces/community/knack/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), knackAuth = import_pieces_framework.PieceAuth.CustomAuth({
  props: {
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your Knack API Key available in the Settings section of the Builder.",
      required: !0
    }),
    applicationId: import_pieces_framework.Property.ShortText({
      displayName: "Application ID",
      description: "Your Application ID available in the Settings section of the Builder.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await knackApiCall({
        method: import_pieces_common2.HttpMethod.GET,
        auth: {
          type: import_pieces_framework2.AppConnectionType.CUSTOM_AUTH,
          props: auth
        },
        resourceUri: "/objects"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key or Application ID"
      };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/knack/src/lib/actions/create-record.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/knack/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var objectDropdown = import_pieces_framework3.Property.Dropdown({
  displayName: "Object",
  required: !0,
  refreshers: [],
  auth: knackAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first.",
        options: []
      };
    let typedAuth = auth;
    try {
      return {
        disabled: !1,
        options: (await knackApiCall({
          method: import_pieces_common3.HttpMethod.GET,
          auth: typedAuth,
          resourceUri: "/objects"
        })).objects.map((object) => ({
          label: object.name,
          value: object.key
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: `Error loading objects: ${error.message}`,
        options: []
      };
    }
  }
}), fieldIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: knackAuth,
  displayName: "Field ID",
  required: !0,
  description: "Field to find the record by",
  refreshers: ["object"],
  options: async ({ auth, object }) => {
    if (!auth || !object)
      return {
        disabled: !0,
        placeholder: "Please select an object first.",
        options: []
      };
    let typedAuth = auth;
    try {
      return {
        disabled: !1,
        options: (await knackApiCall({
          method: import_pieces_common3.HttpMethod.GET,
          auth: typedAuth,
          resourceUri: `/objects/${object}`
        })).object.fields.map((field) => ({
          label: field.name,
          value: field.key
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: `Error loading objects: ${error.message}`,
        options: []
      };
    }
  }
}), recordFields = import_pieces_framework3.Property.DynamicProperties({
  auth: knackAuth,
  displayName: "Record Fields",
  refreshers: ["object"],
  required: !0,
  props: async ({ auth, object }) => {
    if (!auth || !object)
      return {};
    let props = {}, typedAuth = auth, response = await knackApiCall({
      method: import_pieces_common3.HttpMethod.GET,
      auth: typedAuth,
      resourceUri: `/objects/${object}`
    });
    for (let field of response.object.fields)
      switch (field.type) {
        case "short_text":
        case "email":
        case "phone":
        case "link":
          props[field.key] = import_pieces_framework3.Property.ShortText({
            displayName: field.name,
            required: !1
          });
          break;
        case "paragraph_text":
        case "address":
        case "rich_text":
          props[field.key] = import_pieces_framework3.Property.LongText({
            displayName: field.name,
            required: !1
          });
          break;
        case "number":
        case "rating":
        case "currency":
          props[field.key] = import_pieces_framework3.Property.Number({
            displayName: field.name,
            required: !1
          });
          break;
        case "multiple_choice": {
          let options = field.format.options.map((option) => ({
            label: option,
            value: option
          }));
          props[field.key] = field.format.type === "multi" ? import_pieces_framework3.Property.StaticMultiSelectDropdown({
            displayName: field.name,
            required: !1,
            options: {
              options
            }
          }) : import_pieces_framework3.Property.StaticDropdown({
            displayName: field.name,
            required: !1,
            options: { options }
          });
          break;
        }
        case "boolean":
          props[field.key] = import_pieces_framework3.Property.Checkbox({
            displayName: field.name,
            required: !1
          });
          break;
      }
    return props;
  }
});
function knackTransformFields(objectDetails, recordFields2) {
  let fields = objectDetails.object.fields, keyToNameMap = {};
  for (let field of fields)
    field && field.key && field.name && (keyToNameMap[field.key] = field.name);
  let transformed = {};
  for (let [key, value] of Object.entries(recordFields2)) {
    let isRaw = key.endsWith("_raw"), baseKey = isRaw ? key.slice(0, -4) : key, fieldName = keyToNameMap[baseKey];
    if (fieldName) {
      let transformedKey = isRaw ? `${fieldName} raw` : fieldName;
      transformed[transformedKey] = value;
    } else
      transformed[key] = value;
  }
  return transformed;
}

// ../ap/packages/pieces/community/knack/src/lib/actions/create-record.ts
var createRecordAction = (0, import_pieces_framework4.createAction)({
  auth: knackAuth,
  name: "create_record",
  displayName: "Create Record",
  description: "Creates a new record into a specified object/table.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new record in a chosen Knack object (table), with field values supplied as key-value data keyed by Knack field IDs. Use when adding a new row to a Knack database. Not idempotent: each call inserts another record, and a duplicate unique value returns a conflict error.",
    idempotent: !1
  },
  props: {
    object: objectDropdown,
    recordFields
  },
  async run({ propsValue, auth }) {
    let { object: objectKey, recordFields: recordData } = propsValue;
    try {
      let response = await knackApiCall({
        method: import_pieces_common4.HttpMethod.POST,
        auth,
        resourceUri: `/objects/${objectKey}/records`,
        body: recordData
      }), objectDetails = await knackApiCall({
        method: import_pieces_common4.HttpMethod.GET,
        auth,
        resourceUri: `/objects/${objectKey}`
      });
      return knackTransformFields(objectDetails, response);
    } catch (error) {
      throw error.message.includes("409") ? new Error(
        "Conflict: The record could not be created due to a conflict, such as a duplicate unique value."
      ) : error.message.includes("400") ? new Error(
        "Bad Request: Invalid request parameters. Please check your Record Data JSON and field values."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication Failed: Please check your API Key, Application ID, and user permissions."
      ) : error.message.includes("429") ? new Error(
        "Rate Limit Exceeded: Too many requests. Please wait a moment before trying again."
      ) : new Error(`Failed to create Knack record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/knack/src/lib/actions/delete-record.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var deleteRecordAction = (0, import_pieces_framework5.createAction)({
  auth: knackAuth,
  name: "delete_record",
  displayName: "Delete Record",
  description: "Deletes an existing record from a table.",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a single record from a chosen Knack object (table), identified by its record ID. Use when removing a known row. Requires a valid record ID; a missing ID returns a not-found error. Not idempotent: the record is removed on the first successful call and repeating it fails.",
    idempotent: !1
  },
  props: {
    object: objectDropdown,
    recordId: import_pieces_framework5.Property.ShortText({
      displayName: "Record ID",
      required: !0,
      description: "The ID of the record to delete."
    })
  },
  async run({ propsValue, auth }) {
    let { object: objectKey, recordId } = propsValue;
    try {
      return await knackApiCall({
        method: import_pieces_common5.HttpMethod.DELETE,
        auth,
        resourceUri: `/objects/${objectKey}/records/${recordId}`
      });
    } catch (error) {
      throw error.message.includes("404") ? new Error(
        "Not Found: The record ID was not found in the specified object. Please verify the ID is correct."
      ) : error.message.includes("400") ? new Error(
        "Bad Request: The request was invalid. Please ensure the Record ID is formatted correctly."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication Failed: Please check your API Key, Application ID, and user permissions."
      ) : error.message.includes("429") ? new Error(
        "Rate Limit Exceeded: Too many requests. Please wait a moment before trying again."
      ) : new Error(`Failed to delete Knack record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/knack/src/lib/actions/find-record.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var findRecordAction = (0, import_pieces_framework6.createAction)({
  auth: knackAuth,
  name: "find_record",
  displayName: "Find Record",
  description: "Finds a single record using field value.",
  audience: "both",
  aiMetadata: {
    description: 'Looks up a single record in a chosen Knack object (table) by matching one field against a value (exact "is" match), returning the first match or a not-found result. Use to find a record by a known field value rather than its record ID. Read-only and idempotent.',
    idempotent: !0
  },
  props: {
    object: objectDropdown,
    fieldId: fieldIdDropdown,
    fieldValue: import_pieces_framework6.Property.ShortText({
      displayName: "Field Value",
      required: !0,
      description: "The value to search for in the specified field."
    })
  },
  async run({ propsValue, auth }) {
    let { object: objectKey, fieldId, fieldValue } = propsValue;
    try {
      let response = await knackApiCall({
        method: import_pieces_common6.HttpMethod.GET,
        auth,
        resourceUri: `/objects/${objectKey}/records`,
        query: {
          filters: JSON.stringify([
            {
              field: fieldId,
              operator: "is",
              value: fieldValue
            }
          ]),
          rows_per_page: "1"
        }
      });
      if (response.records && response.records.length > 0) {
        let objectDetails = await knackApiCall({
          method: import_pieces_common6.HttpMethod.GET,
          auth,
          resourceUri: `/objects/${objectKey}`
        });
        return {
          found: !0,
          record: knackTransformFields(
            objectDetails,
            response.records[0]
          )
        };
      }
      return {
        found: !1,
        record: null,
        message: "No record found matching the provided filters."
      };
    } catch (error) {
      throw error.message.includes("400") ? new Error(
        "Bad Request: The filter rules are invalid. Please check the JSON format and field/operator values."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication Failed: Please check your API Key, Application ID, and user permissions."
      ) : error.message.includes("429") ? new Error(
        "Rate Limit Exceeded: Too many requests. Please wait a moment before trying again."
      ) : new Error(`Failed to find Knack record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/knack/src/lib/actions/update-record.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var updateRecordAction = (0, import_pieces_framework7.createAction)({
  auth: knackAuth,
  name: "update_record",
  displayName: "Update Record",
  description: "Updates an existing record.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing record in a chosen Knack object (table), identified by its record ID, applying the supplied field values keyed by Knack field IDs. Use when modifying a known row. Requires a valid record ID; a missing ID returns a not-found error. Idempotent: re-applying the same field values to the same record yields the same result.",
    idempotent: !0
  },
  props: {
    object: objectDropdown,
    recordId: import_pieces_framework7.Property.ShortText({
      displayName: "Record ID",
      required: !0,
      description: "The ID of the record to update."
    }),
    recordFields
  },
  async run({ propsValue, auth }) {
    let { object: objectKey, recordId, recordFields: recordFields2 } = propsValue;
    try {
      let response = await knackApiCall({
        method: import_pieces_common7.HttpMethod.PUT,
        auth,
        resourceUri: `/objects/${objectKey}/records/${recordId}`,
        body: recordFields2
      }), objectDetails = await knackApiCall({
        method: import_pieces_common7.HttpMethod.GET,
        auth,
        resourceUri: `/objects/${objectKey}`
      });
      return knackTransformFields(objectDetails, response);
    } catch (error) {
      throw error.message.includes("404") ? new Error(
        "Not Found: The record ID was not found in the specified object. Please verify the ID is correct."
      ) : error.message.includes("409") ? new Error(
        "Conflict: The record could not be updated due to a conflict, such as a duplicate unique value."
      ) : error.message.includes("400") ? new Error(
        "Bad Request: Invalid request parameters. Please check your Data to Update JSON and field values."
      ) : error.message.includes("401") || error.message.includes("403") ? new Error(
        "Authentication Failed: Please check your API Key, Application ID, and user permissions."
      ) : error.message.includes("429") ? new Error(
        "Rate Limit Exceeded: Too many requests. Please wait a moment before trying again."
      ) : new Error(`Failed to update Knack record: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/knack/src/index.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), knack = (0, import_pieces_framework8.createPiece)({
  displayName: "Knack",
  auth: knackAuth,
  minimumSupportedRelease: "0.20.0",
  logoUrl: "https://cdn.activepieces.com/pieces/knack.png",
  categories: [import_pieces_framework9.PieceCategory.CONTENT_AND_FILES],
  authors: ["aryel780"],
  actions: [
    createRecordAction,
    deleteRecordAction,
    findRecordAction,
    updateRecordAction,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: knackAuth,
      baseUrl: () => "https://api.knack.com/v1",
      authMapping: async (auth) => {
        let { apiKey, applicationId } = auth.props;
        return {
          "X-Knack-Application-ID": applicationId,
          "X-Knack-REST-API-Key": apiKey
        };
      }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  knack
});
