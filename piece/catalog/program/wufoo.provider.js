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

// ../ap/packages/pieces/community/wufoo/src/index.ts
var index_exports = {};
__export(index_exports, {
  wufoo: () => wufoo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/wufoo/src/lib/actions/create-form-entry.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/wufoo/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function wufooApiCall({
  method,
  resourceUri,
  query,
  body,
  auth
}) {
  var _a, _b;
  let { apiKey, subdomain } = auth.props;
  if (!apiKey || !subdomain)
    throw new Error("Wufoo API key and subdomain are required for authentication");
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      value != null && (queryParams[key] = String(value));
  let baseUrl = `https://${subdomain}.wufoo.com/api/v3`, authHeader = `Basic ${Buffer.from(`${apiKey}:footastic`).toString("base64")}`, request = {
    method,
    url: `${baseUrl}${resourceUri}`,
    headers: {
      Authorization: authHeader,
      "Content-Type": "application/x-www-form-urlencoded"
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
          `Bad Request: ${(errorData == null ? void 0 : errorData.Text) || "Invalid request parameters"}. Please check your form data and field values.`
        );
      case 401:
        throw new Error(
          "Authentication Failed: Invalid API key or subdomain. Please verify your Wufoo credentials in the connection settings."
        );
      case 403:
        throw new Error(
          "Access Forbidden: You do not have permission to access this resource. Please check your Wufoo account permissions."
        );
      case 404:
        throw new Error(
          "Resource Not Found: The requested form or resource does not exist. Please verify the form identifier is correct."
        );
      case 429:
        throw new Error(
          "Rate Limit Exceeded: Too many requests in a short time period. Wufoo allows maximum 50 submissions per user in a 5-minute window. Please wait before trying again."
        );
      case 500:
        throw new Error(
          "Internal Server Error: Wufoo is experiencing technical difficulties. Please try again later or contact Wufoo support."
        );
      case 502:
      case 503:
      case 504:
        throw new Error(
          "Service Unavailable: Wufoo service is temporarily unavailable. Please try again in a few minutes."
        );
      default: {
        let errorMessage = "Unknown error occurred";
        throw errorData != null && errorData.Text ? errorMessage = errorData.Text : errorData != null && errorData.ErrorText ? errorMessage = errorData.ErrorText : error.message && (errorMessage = error.message), new Error(
          `Wufoo API Error (${statusCode || "Unknown"}): ${errorMessage}`
        );
      }
    }
  }
}

// ../ap/packages/pieces/community/wufoo/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var wufooAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Enter your Wufoo API Key and Subdomain.",
  props: {
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      required: !0
    }),
    subdomain: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Subdomain",
      description: 'Your Wufoo account subdomain (e.g., for fishbowl.wufoo.com, use "fishbowl")',
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await wufooApiCall({
        method: import_pieces_common2.HttpMethod.GET,
        auth: {
          props: auth,
          type: import_pieces_framework2.AppConnectionType.CUSTOM_AUTH
        },
        resourceUri: "/forms.json"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key or Subdomain"
      };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/wufoo/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var formIdentifier = import_pieces_framework3.Property.Dropdown({
  auth: wufooAuth,
  displayName: "Form Identifier (Name and Hash)",
  description: "Select a Wufoo form to work with.",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your Wufoo account."
      };
    let response;
    try {
      response = await wufooApiCall({
        auth,
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: "/forms.json"
      });
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: `Error fetching forms: ${e.message}`
      };
    }
    let forms = Array.isArray(response.Forms) ? response.Forms : [];
    return forms.length === 0 ? {
      disabled: !0,
      options: [],
      placeholder: "No forms found in your account."
    } : {
      disabled: !1,
      options: forms.map((form) => ({
        label: `${form.Name} (${form.Hash})`,
        value: form.Hash
      }))
    };
  }
}), dynamicFormFields = import_pieces_framework3.Property.DynamicProperties({
  auth: wufooAuth,
  displayName: "Form Fields",
  description: "Fill out the form fields with the data you want to submit. Field types and validation are automatically configured based on your form structure.",
  required: !0,
  refreshers: ["formIdentifier", "format"],
  props: async ({ auth, formIdentifier: formIdentifier2, format }) => {
    if (!auth || !formIdentifier2)
      return {};
    let responseFormat = format || "json";
    try {
      let response = await wufooApiCall({
        auth,
        method: import_pieces_common3.HttpMethod.GET,
        resourceUri: `/forms/${formIdentifier2}/fields.json`
      }), parsedResponse = response;
      if (typeof response == "string" && response.includes("OUTPUT =")) {
        let match = response.match(/OUTPUT = ({.*?});/);
        if (match)
          try {
            parsedResponse = JSON.parse(match[1]);
          } catch (e) {
            console.error("Error parsing Wufoo fields response:", e), parsedResponse = response;
          }
      }
      let fields = [];
      if (parsedResponse && typeof parsedResponse == "object") {
        if (parsedResponse.Fields && Array.isArray(parsedResponse.Fields))
          fields = parsedResponse.Fields;
        else if (responseFormat === "json")
          fields = parsedResponse.Fields || [];
        else if (responseFormat === "xml") {
          let fieldsContainer = parsedResponse.Fields || parsedResponse;
          Array.isArray(fieldsContainer) ? fields = fieldsContainer : fieldsContainer.Field && (Array.isArray(fieldsContainer.Field) ? fields = fieldsContainer.Field : fields = [fieldsContainer.Field]);
        }
      }
      let props = {};
      for (let field of fields) {
        if (field.IsSystem || [
          "EntryId",
          "DateCreated",
          "CreatedBy",
          "LastUpdated",
          "UpdatedBy",
          "Status",
          "PurchaseTotal",
          "Currency",
          "TransactionId",
          "MerchantType",
          "IP",
          "LastPage",
          "CompleteSubmission"
        ].includes(field.ID))
          continue;
        let fieldId = field.ID.startsWith("Field") ? field.ID : `Field${field.ID}`, fieldTitle = field.Title || `Field ${field.ID}`, fieldDescription = field.Instructions || `Enter value for ${fieldTitle}`, isRequired = field.IsRequired === "1";
        switch (field.Type) {
          case "text":
          case "email":
          case "url":
            props[fieldId] = import_pieces_framework3.Property.ShortText({
              displayName: fieldTitle,
              description: fieldDescription,
              required: isRequired,
              defaultValue: field.DefaultVal || void 0
            });
            break;
          case "textarea":
            props[fieldId] = import_pieces_framework3.Property.LongText({
              displayName: fieldTitle,
              description: fieldDescription,
              required: isRequired,
              defaultValue: field.DefaultVal || void 0
            });
            break;
          case "number":
          case "money":
            props[fieldId] = import_pieces_framework3.Property.Number({
              displayName: fieldTitle,
              description: field.Type === "money" ? `${fieldDescription} (Enter monetary amount)` : fieldDescription,
              required: isRequired,
              defaultValue: field.DefaultVal ? parseFloat(field.DefaultVal) : void 0
            });
            break;
          case "date":
            props[fieldId] = import_pieces_framework3.Property.DateTime({
              displayName: fieldTitle,
              description: `${fieldDescription} (Date will be converted to YYYYMMDD format for Wufoo)`,
              required: isRequired
            });
            break;
          case "time":
            props[fieldId] = import_pieces_framework3.Property.ShortText({
              displayName: fieldTitle,
              description: `${fieldDescription} (Enter time in HH:MM format)`,
              required: isRequired,
              defaultValue: field.DefaultVal || void 0
            });
            break;
          case "phone":
            props[fieldId] = import_pieces_framework3.Property.ShortText({
              displayName: fieldTitle,
              description: `${fieldDescription} (Phone number)`,
              required: isRequired,
              defaultValue: field.DefaultVal || void 0
            });
            break;
          case "select":
          case "radio":
            if (field.Choices && field.Choices.length > 0) {
              let options = field.Choices.filter((choice) => choice.Label && choice.Label.trim() !== "").map((choice) => ({
                label: choice.Label,
                value: choice.Label
              }));
              options.length > 0 ? props[fieldId] = import_pieces_framework3.Property.StaticDropdown({
                displayName: fieldTitle,
                description: field.HasOtherField ? `${fieldDescription} (Includes "Other" option)` : fieldDescription,
                required: isRequired,
                options: {
                  disabled: !1,
                  options
                }
              }) : props[fieldId] = import_pieces_framework3.Property.ShortText({
                displayName: fieldTitle,
                description: fieldDescription,
                required: isRequired,
                defaultValue: field.DefaultVal || void 0
              });
            } else
              props[fieldId] = import_pieces_framework3.Property.ShortText({
                displayName: fieldTitle,
                description: fieldDescription,
                required: isRequired,
                defaultValue: field.DefaultVal || void 0
              });
            break;
          case "checkbox":
            if (field.SubFields && field.SubFields.length > 0) {
              let options = field.SubFields.map((subField) => ({
                label: subField.Label,
                value: subField.ID
              }));
              props[fieldId] = import_pieces_framework3.Property.StaticMultiSelectDropdown({
                displayName: fieldTitle,
                description: `${fieldDescription} (Select multiple options)`,
                required: isRequired,
                options: {
                  disabled: !1,
                  options
                }
              });
            } else
              props[fieldId] = import_pieces_framework3.Property.Checkbox({
                displayName: fieldTitle,
                description: fieldDescription,
                required: isRequired,
                defaultValue: field.DefaultVal === "1" || field.DefaultVal === "true"
              });
            break;
          case "address":
            if (field.SubFields && field.SubFields.length > 0)
              for (let subField of field.SubFields)
                props[subField.ID] = import_pieces_framework3.Property.ShortText({
                  displayName: `${fieldTitle} - ${subField.Label}`,
                  description: `Enter ${subField.Label.toLowerCase()}`,
                  required: isRequired && ["Street Address", "City"].includes(subField.Label),
                  defaultValue: subField.DefaultVal || void 0
                });
            else
              props[fieldId] = import_pieces_framework3.Property.LongText({
                displayName: fieldTitle,
                description: `${fieldDescription} (Complete address)`,
                required: isRequired,
                defaultValue: field.DefaultVal || void 0
              });
            break;
          case "shortname":
            if (field.SubFields && field.SubFields.length > 0)
              for (let subField of field.SubFields)
                props[subField.ID] = import_pieces_framework3.Property.ShortText({
                  displayName: `${fieldTitle} - ${subField.Label}`,
                  description: `Enter ${subField.Label.toLowerCase()} name`,
                  required: isRequired,
                  defaultValue: subField.DefaultVal || void 0
                });
            else
              props[fieldId] = import_pieces_framework3.Property.ShortText({
                displayName: fieldTitle,
                description: fieldDescription,
                required: isRequired,
                defaultValue: field.DefaultVal || void 0
              });
            break;
          case "likert":
            if (field.Choices && field.Choices.length > 0) {
              let options = field.Choices.map((choice) => ({
                label: `${choice.Label}${choice.Score ? ` (Score: ${choice.Score})` : ""}`,
                value: choice.Label
              }));
              if (field.SubFields && field.SubFields.length > 0)
                for (let subField of field.SubFields)
                  props[subField.ID] = import_pieces_framework3.Property.StaticDropdown({
                    displayName: `${fieldTitle} - ${subField.Label}`,
                    description: `Rate: ${subField.Label}`,
                    required: isRequired,
                    options: {
                      disabled: !1,
                      options
                    }
                  });
              else
                props[fieldId] = import_pieces_framework3.Property.StaticDropdown({
                  displayName: fieldTitle,
                  description: fieldDescription,
                  required: isRequired,
                  options: {
                    disabled: !1,
                    options
                  }
                });
            } else
              props[fieldId] = import_pieces_framework3.Property.ShortText({
                displayName: fieldTitle,
                description: `${fieldDescription} (Likert scale)`,
                required: isRequired,
                defaultValue: field.DefaultVal || void 0
              });
            break;
          case "rating":
            props[fieldId] = import_pieces_framework3.Property.Number({
              displayName: fieldTitle,
              description: `${fieldDescription} (Rating scale)`,
              required: isRequired,
              defaultValue: field.DefaultVal ? parseFloat(field.DefaultVal) : void 0
            });
            break;
          case "file":
            props[fieldId] = import_pieces_framework3.Property.File({
              displayName: fieldTitle,
              description: `${fieldDescription} (File upload)`,
              required: isRequired
            });
            break;
          default:
            props[fieldId] = import_pieces_framework3.Property.ShortText({
              displayName: fieldTitle,
              description: `${fieldDescription} (Field type: ${field.Type})`,
              required: isRequired,
              defaultValue: field.DefaultVal || void 0
            });
            break;
        }
      }
      return props;
    } catch (error) {
      return console.error(`Error fetching form fields in ${responseFormat} format:`, error), {};
    }
  }
});

// ../ap/packages/pieces/community/wufoo/src/lib/actions/create-form-entry.ts
var createFormEntryAction = (0, import_pieces_framework4.createAction)({
  auth: wufooAuth,
  name: "create-form-entry",
  displayName: "Create Form Entry",
  description: "Submit a new entry to a Wufoo form with dynamic field selection.",
  audience: "both",
  aiMetadata: { description: "Submits a new entry to a specified Wufoo form, with values mapped to the form's dynamic fields. Use to programmatically fill and submit a form (e.g. record a lead or feedback response). Target the form by name or hash; not idempotent \u2014 each call creates a separate entry, and Wufoo rate-limits to 50 submissions per user per 5-minute window.", idempotent: !1 },
  props: {
    formIdentifier,
    format: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Choose the format for the API response. JSON is recommended for most use cases.",
      required: !0,
      defaultValue: "json",
      options: {
        disabled: !1,
        options: [
          { label: "JSON", value: "json" },
          { label: "XML", value: "xml" }
        ]
      }
    }),
    formFields: dynamicFormFields
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let { formIdentifier: formIdentifier2, format, formFields } = context.propsValue;
    try {
      let processedFields = {};
      for (let [fieldId, fieldValue] of Object.entries(formFields))
        if (fieldValue != null)
          if (fieldId.includes("date") || fieldId.includes("Date")) {
            let dateValue = new Date(fieldValue);
            if (isNaN(dateValue.getTime()))
              processedFields[fieldId] = String(fieldValue);
            else {
              let year = dateValue.getFullYear(), month = String(dateValue.getMonth() + 1).padStart(2, "0"), day = String(dateValue.getDate()).padStart(2, "0");
              processedFields[fieldId] = `${year}${month}${day}`;
            }
          } else if (Array.isArray(fieldValue)) {
            if (fieldValue.length > 0)
              for (let selectedId of fieldValue)
                processedFields[selectedId] = "1";
          } else typeof fieldValue == "boolean" ? processedFields[fieldId] = fieldValue ? "1" : "0" : typeof fieldValue == "number" ? processedFields[fieldId] = String(fieldValue) : fieldValue instanceof File ? processedFields[fieldId] = fieldValue.name || "uploaded_file" : processedFields[fieldId] = String(fieldValue);
      let response = await wufooApiCall({
        method: import_pieces_common4.HttpMethod.POST,
        auth: context.auth,
        resourceUri: `/forms/${formIdentifier2}/entries.${format}`,
        body: new URLSearchParams(processedFields).toString()
      });
      if (response.Success === 1)
        return {
          success: !0,
          message: "Form entry submitted successfully!",
          entryId: response.EntryId,
          entryLink: response.EntryLink,
          redirectUrl: response.RedirectUrl,
          submittedFields: processedFields
        };
      {
        let errorDetails = {
          success: !1,
          message: response.ErrorText || "Form submission failed",
          fieldErrors: response.FieldErrors || [],
          redirectUrl: response.RedirectUrl
        }, errorMessage = "Form submission failed";
        if (response.FieldErrors && response.FieldErrors.length > 0) {
          let fieldErrorMessages = response.FieldErrors.map(
            (error) => `${error.ID}: ${error.ErrorText}`
          );
          errorMessage += ` - Field errors: ${fieldErrorMessages.join(", ")}`;
        }
        throw new Error(errorMessage);
      }
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 429 ? new Error(
        "Rate limit exceeded. Wufoo allows maximum 50 submissions per user in a 5-minute window. Please wait before trying again."
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 400 ? new Error(
        `Bad request: ${((_c = error.response.data) == null ? void 0 : _c.Text) || error.message}. Please check your form field values.`
      ) : ((_d = error.response) == null ? void 0 : _d.status) === 401 ? new Error(
        "Authentication failed. Please check your API key and subdomain."
      ) : ((_e = error.response) == null ? void 0 : _e.status) === 404 ? new Error(
        "Form not found. Please verify the form identifier is correct."
      ) : error.message.includes("Field errors:") ? error : new Error(
        `Failed to submit form entry: ${error.message || "Unknown error occurred"}`
      );
    }
  }
});

// ../ap/packages/pieces/community/wufoo/src/lib/actions/find-form.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var findFormAction = (0, import_pieces_framework5.createAction)({
  auth: wufooAuth,
  name: "find-form",
  displayName: "Find Form by Name or Hash",
  description: "Get details about a Wufoo form including settings, entry counts, and metadata.",
  audience: "both",
  aiMetadata: { description: "Retrieves a single Wufoo form's configuration and metadata (name, URL, entry limit, dates, and links to its fields/entries) by form name or hash. Use to look up form settings or resolve a form identifier before working with its entries; optionally include today's entry count. Read-only and idempotent.", idempotent: !0 },
  props: {
    formIdentifier,
    format: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Choose the format for the API response. JSON is recommended for most integrations.",
      required: !0,
      defaultValue: "json",
      options: {
        disabled: !1,
        options: [
          { label: "JSON", value: "json" },
          { label: "XML", value: "xml" }
        ]
      }
    }),
    includeTodayCount: import_pieces_framework5.Property.Checkbox({
      displayName: "Include Today Count",
      description: "Include the number of entries submitted today in the response. Useful for daily analytics and monitoring.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { formIdentifier: formIdentifier2, format, includeTodayCount } = context.propsValue;
    try {
      let response = await wufooApiCall({
        method: import_pieces_common5.HttpMethod.GET,
        auth: context.auth,
        resourceUri: `/forms/${formIdentifier2}.${format}`,
        query: {
          includeTodayCount: includeTodayCount ? "true" : "false",
          pretty: "false"
        }
      }), parsedResponse = response;
      if (typeof response == "string" && response.includes("OUTPUT =")) {
        let match = response.match(/OUTPUT = ({.*?});/);
        if (match)
          try {
            parsedResponse = JSON.parse(match[1]);
          } catch (e) {
            parsedResponse = response;
          }
      }
      if (format === "json" && parsedResponse && typeof parsedResponse == "object") {
        let formData;
        return Array.isArray(parsedResponse) ? formData = parsedResponse[0] : parsedResponse.Forms && Array.isArray(parsedResponse.Forms) ? formData = parsedResponse.Forms[0] : formData = parsedResponse, {
          success: !0,
          message: "Form details retrieved successfully",
          form: {
            name: formData.Name,
            description: formData.Description,
            hash: formData.Hash,
            url: formData.Url,
            isPublic: formData.IsPublic === "1",
            language: formData.Language,
            redirectMessage: formData.RedirectMessage,
            entryLimit: parseInt(formData.EntryLimit || "0"),
            todayCount: formData.EntryCountToday ? parseInt(formData.EntryCountToday) : void 0,
            dateCreated: formData.DateCreated,
            dateUpdated: formData.DateUpdated,
            startDate: formData.StartDate,
            endDate: formData.EndDate,
            fieldsLink: formData.LinkFields,
            entriesLink: formData.LinkEntries,
            entriesCountLink: formData.LinkEntriesCount
          },
          rawResponse: parsedResponse
        };
      } else
        return {
          success: !0,
          message: "Form details retrieved successfully",
          response: parsedResponse
        };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 404 ? new Error(
        `Form not found: The form with identifier "${formIdentifier2}" does not exist or you do not have access to it. Please verify the form identifier and your permissions.`
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error(
        "Access denied: You do not have permission to view this form. Please check your Wufoo account permissions and API key scope."
      ) : ((_c = error.response) == null ? void 0 : _c.status) === 401 ? new Error(
        "Authentication failed: Please verify your API key and subdomain are correct in the connection settings."
      ) : new Error(
        `Failed to retrieve form details: ${error.message || "Unknown error occurred"}. Please check your form identifier and try again.`
      );
    }
  }
});

// ../ap/packages/pieces/community/wufoo/src/lib/actions/find-submission-by-field.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var findSubmissionByFieldAction = (0, import_pieces_framework6.createAction)({
  auth: wufooAuth,
  name: "find-submission-by-field",
  displayName: "Find Submission by Field Value",
  description: "Search for form submissions by field value for deduplication and lookup operations.",
  audience: "both",
  aiMetadata: { description: "Searches a Wufoo form's entries for those whose value in a given field matches a search value, with a selectable match type (exact, contains, starts/ends with, not-equal, not-contains). Use to look up a specific submission or check for duplicates before creating one. Requires the form identifier, the target field ID, and the value; read-only and idempotent.", idempotent: !0 },
  props: {
    formIdentifier,
    format: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Choose the format for the API response. JSON is recommended for most workflows.",
      required: !0,
      defaultValue: "json",
      options: {
        disabled: !1,
        options: [
          { label: "JSON", value: "json" },
          { label: "XML", value: "xml" }
        ]
      }
    }),
    fieldId: import_pieces_framework6.Property.ShortText({
      displayName: "Field ID",
      description: "The form field to search in (e.g., Field1 for first name, Field218 for email). You can find Field IDs in the form builder or via the Form Fields API.",
      required: !0
    }),
    searchValue: import_pieces_framework6.Property.ShortText({
      displayName: "Search Value",
      description: "The value to search for in the specified field. For exact matches, use the complete value.",
      required: !0
    }),
    matchType: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Match Type",
      description: "How to match the search value with field data. Choose based on your use case.",
      required: !0,
      defaultValue: "Is_equal_to",
      options: {
        disabled: !1,
        options: [
          { label: "Exact Match", value: "Is_equal_to" },
          { label: "Contains Text", value: "Contains" },
          { label: "Starts With", value: "Begins_with" },
          { label: "Ends With", value: "Ends_with" },
          { label: "Does Not Contain", value: "Does_not_contain" },
          { label: "Is Not Equal To", value: "Is_not_equal_to" }
        ]
      }
    }),
    maxResults: import_pieces_framework6.Property.Number({
      displayName: "Maximum Results",
      description: "Limit the number of results returned (1-50). Lower numbers are faster for deduplication checks.",
      required: !1,
      defaultValue: 10
    }),
    sortOrder: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Sort Order",
      description: 'Order to return results. "Newest First" is best for finding recent duplicates.',
      required: !1,
      defaultValue: "DESC",
      options: {
        disabled: !1,
        options: [
          { label: "Newest First", value: "DESC" },
          { label: "Oldest First", value: "ASC" }
        ]
      }
    }),
    includeMetadata: import_pieces_framework6.Property.Checkbox({
      displayName: "Include Metadata",
      description: "Include additional information like submission date, IP address, and creation details.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let {
      formIdentifier: formIdentifier2,
      format,
      fieldId,
      searchValue,
      matchType,
      maxResults,
      sortOrder,
      includeMetadata
    } = context.propsValue;
    try {
      let query = {
        pageStart: "0",
        pageSize: String(Math.min(maxResults != null ? maxResults : 10, 50)),
        system: includeMetadata ? "true" : "false",
        pretty: "false",
        Filter1: `${fieldId}+${matchType}+${encodeURIComponent(searchValue)}`,
        sort: "EntryId",
        sortDirection: sortOrder || "DESC"
      }, response = await wufooApiCall({
        method: import_pieces_common6.HttpMethod.GET,
        auth: context.auth,
        resourceUri: `/forms/${formIdentifier2}/entries.${format}`,
        query
      }), parsedResponse = response;
      if (typeof response == "string" && response.includes("OUTPUT =")) {
        let match = response.match(/OUTPUT = ({.*?});/);
        if (match)
          try {
            parsedResponse = JSON.parse(match[1]);
          } catch (e) {
            parsedResponse = response;
          }
      }
      if (format === "json" && parsedResponse && typeof parsedResponse == "object") {
        let entries = parsedResponse.Entries || [];
        return {
          success: !0,
          found: entries.length > 0,
          message: entries.length > 0 ? `Found ${entries.length} submission(s) matching the search criteria` : `No submissions found with ${fieldId} ${matchType.replace("_", " ").toLowerCase()} "${searchValue}"`,
          searchCriteria: {
            fieldId,
            searchValue,
            matchType: matchType.replace("_", " ").toLowerCase(),
            formIdentifier: formIdentifier2
          },
          resultCount: entries.length,
          firstMatch: entries.length > 0 ? {
            entryId: entries[0].EntryId,
            dateCreated: entries[0].DateCreated,
            matchedFieldValue: entries[0][fieldId],
            keyFields: __spreadValues(__spreadValues(__spreadValues(__spreadValues({
              [fieldId]: entries[0][fieldId]
            }, entries[0].Field1 && { name_first: entries[0].Field1 }), entries[0].Field2 && { name_last: entries[0].Field2 }), entries[0].Field218 && { email: entries[0].Field218 }), entries[0].Field220 && { phone: entries[0].Field220 })
          } : null,
          allMatches: entries.map((entry) => ({
            entryId: entry.EntryId,
            dateCreated: entry.DateCreated,
            dateUpdated: entry.DateUpdated,
            matchedFieldValue: entry[fieldId],
            allFields: Object.keys(entry).filter((key) => !["EntryId", "DateCreated", "DateUpdated", "CreatedBy", "UpdatedBy"].includes(key)).reduce((acc, key) => (acc[key] = entry[key], acc), {})
          })),
          rawResponse: parsedResponse
        };
      } else
        return {
          success: !0,
          message: "Search completed successfully",
          response: parsedResponse
        };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 404 ? new Error(
        `Form not found: The form with identifier "${formIdentifier2}" does not exist. Please verify the form identifier is correct.`
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 400 ? new Error(
        `Invalid search parameters: The field ID "${fieldId}" may not exist in this form, or the search value contains invalid characters. Please check the field ID and search value.`
      ) : ((_c = error.response) == null ? void 0 : _c.status) === 403 ? new Error(
        "Access denied: You do not have permission to search entries in this form. Please check your Wufoo account permissions."
      ) : ((_d = error.response) == null ? void 0 : _d.status) === 401 ? new Error(
        "Authentication failed: Please verify your API key and subdomain are correct in the connection settings."
      ) : new Error(
        `Search failed: ${error.message || "Unknown error occurred"}. Please check your search parameters and try again.`
      );
    }
  }
});

// ../ap/packages/pieces/community/wufoo/src/lib/actions/get-entry-details.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var getEntryDetailsAction = (0, import_pieces_framework7.createAction)({
  auth: wufooAuth,
  name: "get-entry-details",
  displayName: "Get Entry Details",
  description: "Get form entries with filtering, sorting, and pagination options.",
  audience: "both",
  aiMetadata: { description: "Retrieves entries from a Wufoo form, either a single entry by its Entry ID or a filtered/sorted/paginated list (up to two field filters combined with AND/OR). Use to fetch one known entry or to query and page through many submissions. Requires the form identifier; read-only and idempotent.", idempotent: !0 },
  props: {
    formIdentifier,
    format: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Choose the format for the API response. JSON is recommended for most integrations.",
      required: !0,
      defaultValue: "json",
      options: {
        disabled: !1,
        options: [
          { label: "JSON", value: "json" },
          { label: "XML", value: "xml" }
        ]
      }
    }),
    entryId: import_pieces_framework7.Property.Number({
      displayName: "Specific Entry ID (Optional)",
      description: "Enter a specific Entry ID to retrieve just that entry. Leave blank to get multiple entries with other filters.",
      required: !1
    }),
    filterField: import_pieces_framework7.Property.ShortText({
      displayName: "Filter Field ID (Optional)",
      description: "The field ID to filter by (e.g., Field1, Field105, EntryId, DateCreated). Leave blank for no filtering.",
      required: !1
    }),
    filterOperator: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Filter Operator",
      description: "How to compare the filter value with the field data.",
      required: !1,
      defaultValue: "Is_equal_to",
      options: {
        disabled: !1,
        options: [
          { label: "Is Equal To", value: "Is_equal_to" },
          { label: "Is Not Equal To", value: "Is_not_equal_to" },
          { label: "Contains", value: "Contains" },
          { label: "Does Not Contain", value: "Does_not_contain" },
          { label: "Begins With", value: "Begins_with" },
          { label: "Ends With", value: "Ends_with" },
          { label: "Is Greater Than", value: "Is_greater_than" },
          { label: "Is Less Than", value: "Is_less_than" },
          { label: "Is On (Date)", value: "Is_on" },
          { label: "Is Before (Date)", value: "Is_before" },
          { label: "Is After (Date)", value: "Is_after" },
          { label: "Is Not Empty", value: "Is_not_NULL" }
        ]
      }
    }),
    filterValue: import_pieces_framework7.Property.ShortText({
      displayName: "Filter Value (Optional)",
      description: "The value to filter by. For dates, use YYYY-MM-DD format or YYYY-MM-DD HH:MM:SS for specific times.",
      required: !1
    }),
    additionalFilterField: import_pieces_framework7.Property.ShortText({
      displayName: "Second Filter Field (Optional)",
      description: "Add a second filter field for more complex queries.",
      required: !1
    }),
    additionalFilterOperator: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Second Filter Operator",
      description: "Operator for the second filter.",
      required: !1,
      defaultValue: "Is_equal_to",
      options: {
        disabled: !1,
        options: [
          { label: "Is Equal To", value: "Is_equal_to" },
          { label: "Is Not Equal To", value: "Is_not_equal_to" },
          { label: "Contains", value: "Contains" },
          { label: "Does Not Contain", value: "Does_not_contain" },
          { label: "Begins With", value: "Begins_with" },
          { label: "Ends With", value: "Ends_with" },
          { label: "Is Greater Than", value: "Is_greater_than" },
          { label: "Is Less Than", value: "Is_less_than" },
          { label: "Is On (Date)", value: "Is_on" },
          { label: "Is Before (Date)", value: "Is_before" },
          { label: "Is After (Date)", value: "Is_after" },
          { label: "Is Not Empty", value: "Is_not_NULL" }
        ]
      }
    }),
    additionalFilterValue: import_pieces_framework7.Property.ShortText({
      displayName: "Second Filter Value (Optional)",
      description: "Value for the second filter.",
      required: !1
    }),
    filterLogic: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Filter Logic",
      description: "How to combine multiple filters. Only applies when using multiple filters.",
      required: !1,
      defaultValue: "AND",
      options: {
        disabled: !1,
        options: [
          { label: "AND (All filters must match)", value: "AND" },
          { label: "OR (Any filter must match)", value: "OR" }
        ]
      }
    }),
    sort: import_pieces_framework7.Property.ShortText({
      displayName: "Sort By Field ID (Optional)",
      description: "Sort results by field ID (e.g., EntryId, DateCreated, Field1). Leave blank for default order.",
      required: !1
    }),
    sortDirection: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Sort Direction",
      description: "Order to sort the results.",
      required: !1,
      defaultValue: "DESC",
      options: {
        disabled: !1,
        options: [
          { label: "Descending (Newest first)", value: "DESC" },
          { label: "Ascending (Oldest first)", value: "ASC" }
        ]
      }
    }),
    pageStart: import_pieces_framework7.Property.Number({
      displayName: "Page Start",
      description: "Starting entry number for pagination (0 = first entry).",
      required: !1,
      defaultValue: 0
    }),
    pageSize: import_pieces_framework7.Property.Number({
      displayName: "Page Size",
      description: "Number of entries to return (maximum 100 per request).",
      required: !1,
      defaultValue: 25
    }),
    includeSystem: import_pieces_framework7.Property.Checkbox({
      displayName: "Include System Fields",
      description: "Include additional metadata like IP address, payment status, and completion status.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let {
      formIdentifier: formIdentifier2,
      format,
      entryId,
      filterField,
      filterOperator,
      filterValue,
      additionalFilterField,
      additionalFilterOperator,
      additionalFilterValue,
      filterLogic,
      sort,
      sortDirection,
      pageStart,
      pageSize,
      includeSystem
    } = context.propsValue;
    try {
      let query = {
        pageStart: String(pageStart != null ? pageStart : 0),
        pageSize: String(Math.min(pageSize != null ? pageSize : 25, 100)),
        system: includeSystem ? "true" : "false",
        pretty: "false"
        // Always false to avoid HTML wrapper
      }, filterCount = 0;
      entryId != null ? (filterCount++, query[`Filter${filterCount}`] = `EntryId+Is_equal_to+${entryId}`) : (filterField && filterValue && (filterCount++, query[`Filter${filterCount}`] = `${filterField}+${filterOperator}+${encodeURIComponent(filterValue)}`), additionalFilterField && additionalFilterValue && (filterCount++, query[`Filter${filterCount}`] = `${additionalFilterField}+${additionalFilterOperator}+${encodeURIComponent(additionalFilterValue)}`), filterCount > 1 && (query.match = filterLogic || "AND")), sort && (query.sort = sort, sortDirection && (query.sortDirection = sortDirection));
      let response = await wufooApiCall({
        method: import_pieces_common7.HttpMethod.GET,
        auth: context.auth,
        resourceUri: `/forms/${formIdentifier2}/entries.${format}`,
        query
      }), parsedResponse = response;
      if (typeof response == "string" && response.includes("OUTPUT =")) {
        let match = response.match(/OUTPUT = ({.*?});/);
        if (match)
          try {
            parsedResponse = JSON.parse(match[1]);
          } catch (e) {
            parsedResponse = response;
          }
      }
      if (format === "json" && parsedResponse && typeof parsedResponse == "object") {
        let entries = parsedResponse.Entries || [];
        return {
          success: !0,
          message: `Retrieved ${entries.length} entries successfully`,
          summary: {
            totalRetrieved: entries.length,
            pageStart: pageStart != null ? pageStart : 0,
            pageSize: pageSize != null ? pageSize : 25,
            hasMoreEntries: entries.length === (pageSize != null ? pageSize : 25),
            filtersApplied: filterCount,
            sortedBy: sort || "default",
            sortDirection: sortDirection || "ASC"
          },
          entries: entries.map((entry) => ({
            entryId: entry.EntryId,
            dateCreated: entry.DateCreated,
            dateUpdated: entry.DateUpdated,
            createdBy: entry.CreatedBy,
            updatedBy: entry.UpdatedBy,
            fieldData: Object.keys(entry).filter((key) => !["EntryId", "DateCreated", "DateUpdated", "CreatedBy", "UpdatedBy"].includes(key)).reduce((acc, key) => (acc[key] = entry[key], acc), {}),
            rawEntry: entry
          })),
          rawResponse: parsedResponse
        };
      } else
        return {
          success: !0,
          message: "Entries retrieved successfully",
          response: parsedResponse
        };
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 404 ? new Error(
        `Form not found: The form with identifier "${formIdentifier2}" does not exist or you do not have access to it. Please verify the form identifier and your permissions.`
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 400 ? new Error(
        `Bad request: Invalid filter parameters or field IDs. Please check your filter fields and values. Error: ${((_d = (_c = error.response) == null ? void 0 : _c.data) == null ? void 0 : _d.Text) || error.message}`
      ) : ((_e = error.response) == null ? void 0 : _e.status) === 403 ? new Error(
        "Access denied: You do not have permission to view entries for this form. Please check your Wufoo account permissions and API key scope."
      ) : ((_f = error.response) == null ? void 0 : _f.status) === 401 ? new Error(
        "Authentication failed: Please verify your API key and subdomain are correct in the connection settings."
      ) : new Error(
        `Failed to retrieve entries: ${error.message || "Unknown error occurred"}. Please check your parameters and try again.`
      );
    }
  }
});

// ../ap/packages/pieces/community/wufoo/src/lib/triggers/new-form-entry.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var TRIGGER_KEY = "wufoo-webhook-hash", newFormEntryTrigger = (0, import_pieces_framework8.createTrigger)({
  auth: wufooAuth,
  name: "new_form_entry",
  displayName: "New Form Entry",
  description: "Triggers when someone submits a new entry to your Wufoo form.",
  aiMetadata: {
    description: "Fires when a new entry is submitted to the specified Wufoo form, delivering the submitted field values for that entry via a Wufoo webhook."
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  props: {
    formIdentifier,
    handshakeKey: import_pieces_framework8.Property.ShortText({
      displayName: "Handshake Key (Optional)",
      description: "A secret key to validate webhook authenticity and prevent unauthorized requests. Recommended for production workflows.",
      required: !1
    }),
    includeMetadata: import_pieces_framework8.Property.Checkbox({
      displayName: "Include Form Structure Metadata",
      description: "Include detailed form and field structure information with each submission. Useful for dynamic processing but increases payload size.",
      required: !1,
      defaultValue: !1
    }),
    responseFormat: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Webhook Response Format",
      description: "Choose the format for webhook data. JSON is recommended for most automation workflows.",
      required: !1,
      defaultValue: "json",
      options: {
        disabled: !1,
        options: [
          { label: "JSON", value: "json" },
          { label: "XML", value: "xml" }
        ]
      }
    })
  },
  async onEnable(context) {
    var _a, _b, _c, _d, _e;
    let { formIdentifier: formIdentifier2, handshakeKey, includeMetadata, responseFormat } = context.propsValue;
    try {
      await wufooApiCall({
        method: import_pieces_common8.HttpMethod.GET,
        auth: context.auth,
        resourceUri: `/forms/${formIdentifier2}.json`
      });
      let webhookBody = {
        url: context.webhookUrl,
        metadata: includeMetadata ? "true" : "false"
      };
      handshakeKey && handshakeKey.trim() && (webhookBody.handshakeKey = handshakeKey.trim());
      let response = await wufooApiCall({
        method: import_pieces_common8.HttpMethod.PUT,
        auth: context.auth,
        resourceUri: `/forms/${formIdentifier2}/webhooks.${responseFormat || "json"}`,
        body: webhookBody
      });
      await context.store.put(TRIGGER_KEY, response.WebHookPutResult.Hash), console.log(`Wufoo webhook successfully configured for form ${formIdentifier2} with hash: ${response.WebHookPutResult.Hash}`);
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 404 ? new Error(
        `Form not found: The form with identifier "${formIdentifier2}" does not exist or you do not have access to it. Please verify the form identifier and your permissions.`
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error(
        "Access denied: You do not have permission to set up webhooks for this form. Please check your Wufoo account permissions and ensure you have webhook access."
      ) : ((_c = error.response) == null ? void 0 : _c.status) === 400 ? new Error(
        `Invalid webhook configuration: ${((_e = (_d = error.response) == null ? void 0 : _d.data) == null ? void 0 : _e.Text) || error.message}. Please check your webhook URL and form identifier.`
      ) : new Error(
        `Failed to set up webhook: ${error.message || "Unknown error occurred"}. Please check your form identifier and try again.`
      );
    }
  },
  async onDisable(context) {
    let webhookHash = await context.store.get(TRIGGER_KEY), { formIdentifier: formIdentifier2, responseFormat } = context.propsValue;
    if (!(0, import_pieces_framework9.isNil)(webhookHash))
      try {
        await wufooApiCall({
          method: import_pieces_common8.HttpMethod.DELETE,
          auth: context.auth,
          resourceUri: `/forms/${formIdentifier2}/webhooks/${webhookHash}.${responseFormat || "json"}`
        }), console.log(`Wufoo webhook successfully removed for form ${formIdentifier2} with hash: ${webhookHash}`);
      } catch (error) {
        console.warn(`Warning: Failed to clean up webhook ${webhookHash} for form ${formIdentifier2}:`, error.message), await context.store.delete(TRIGGER_KEY);
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return payload && typeof payload == "object" ? [{
      entryId: payload.EntryId,
      dateCreated: payload.DateCreated,
      createdBy: payload.CreatedBy || "public",
      formData: Object.keys(payload).filter((key) => !["EntryId", "DateCreated", "CreatedBy", "DateUpdated", "UpdatedBy"].includes(key)).reduce((acc, key) => (acc[key] = payload[key], acc), {}),
      commonFields: __spreadValues(__spreadValues(__spreadValues(__spreadValues({}, payload.Field1 && { firstName: payload.Field1 }), payload.Field2 && { lastName: payload.Field2 }), payload.Field218 && { email: payload.Field218 }), payload.Field220 && { phone: payload.Field220 }),
      rawPayload: payload,
      webhookInfo: {
        receivedAt: (/* @__PURE__ */ new Date()).toISOString(),
        source: "wufoo",
        type: "form_entry"
      }
    }] : [payload];
  },
  async test(context) {
    return [
      {
        entryId: "123",
        dateCreated: "2025-01-15 14:30:22",
        createdBy: "public",
        formData: {
          Field105: "Sample form submission for testing",
          Field106: "456",
          Field107: "This is a test paragraph field entry with multiple lines of content.",
          Field1: "John",
          Field2: "Doe",
          Field218: "john.doe@example.com",
          Field220: "555-123-4567",
          Field217: "2025-01-15"
        },
        commonFields: {
          firstName: "John",
          lastName: "Doe",
          email: "john.doe@example.com",
          phone: "555-123-4567"
        },
        rawPayload: {
          EntryId: "123",
          Field105: "Sample form submission for testing",
          Field106: "456",
          Field107: "This is a test paragraph field entry with multiple lines of content.",
          Field1: "John",
          Field2: "Doe",
          Field218: "john.doe@example.com",
          Field220: "555-123-4567",
          Field217: "2025-01-15",
          DateCreated: "2025-01-15 14:30:22",
          CreatedBy: "public"
        },
        webhookInfo: {
          receivedAt: "2025-01-15T14:30:22.000Z",
          source: "wufoo",
          type: "form_entry"
        }
      }
    ];
  },
  sampleData: {
    entryId: "124",
    dateCreated: "2025-01-15 15:45:10",
    createdBy: "public",
    formData: {
      Field105: "Another sample entry",
      Field106: "789",
      Field1: "Jane",
      Field2: "Smith",
      Field218: "jane.smith@example.com",
      Field220: "555-987-6543"
    },
    commonFields: {
      firstName: "Jane",
      lastName: "Smith",
      email: "jane.smith@example.com",
      phone: "555-987-6543"
    },
    rawPayload: {
      EntryId: "124",
      Field105: "Another sample entry",
      Field106: "789",
      Field1: "Jane",
      Field2: "Smith",
      Field218: "jane.smith@example.com",
      Field220: "555-987-6543",
      DateCreated: "2025-01-15 15:45:10",
      CreatedBy: "public"
    },
    webhookInfo: {
      receivedAt: "2025-01-15T15:45:10.000Z",
      source: "wufoo",
      type: "form_entry"
    }
  }
});

// ../ap/packages/pieces/community/wufoo/src/lib/triggers/new-form.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var LAST_FORM_IDS_KEY = "wufoo-last-form-ids", newFormTrigger = (0, import_pieces_framework10.createTrigger)({
  auth: wufooAuth,
  name: "new_form_created",
  displayName: "New Form Created",
  description: "Triggers when a new form is created in your Wufoo account.",
  aiMetadata: {
    description: "Fires when a new form appears in the connected Wufoo account, detected by polling the account's form list. Can optionally be limited to forms whose name contains given text and to public/active forms only."
  },
  type: import_pieces_framework10.TriggerStrategy.POLLING,
  props: {
    pollingInterval: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Polling Interval",
      description: "How frequently to check for new forms. More frequent checking provides faster detection but uses more API calls.",
      required: !1,
      defaultValue: "5",
      options: {
        disabled: !1,
        options: [
          { label: "Every 1 minute", value: "1" },
          { label: "Every 5 minutes", value: "5" },
          { label: "Every 15 minutes", value: "15" },
          { label: "Every 30 minutes", value: "30" },
          { label: "Every hour", value: "60" }
        ]
      }
    }),
    nameFilter: import_pieces_framework10.Property.ShortText({
      displayName: "Form Name Filter (Optional)",
      description: "Only trigger for forms containing this text in their name. Leave empty to monitor all forms.",
      required: !1
    }),
    includeInactive: import_pieces_framework10.Property.Checkbox({
      displayName: "Include Inactive Forms",
      description: "Include forms that are not currently active/published. Useful for monitoring all form creation activity.",
      required: !1,
      defaultValue: !1
    }),
    responseFormat: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Choose the format for form data. JSON is recommended for most workflows.",
      required: !1,
      defaultValue: "json",
      options: {
        disabled: !1,
        options: [
          { label: "JSON", value: "json" },
          { label: "XML", value: "xml" }
        ]
      }
    })
  },
  async onEnable(context) {
    var _a, _b;
    let { responseFormat } = context.propsValue;
    try {
      let hashes = (await wufooApiCall({
        auth: context.auth,
        method: import_pieces_common9.HttpMethod.GET,
        resourceUri: `/forms.${responseFormat || "json"}`
      })).Forms.map((form) => form.Hash);
      await context.store.put(LAST_FORM_IDS_KEY, hashes), console.log(`Wufoo New Form trigger initialized with ${hashes.length} existing forms`);
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error(
        "Authentication failed: Please check your API key and subdomain. Make sure your API key has permission to access forms."
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 403 ? new Error(
        "Access denied: You do not have permission to list forms. Please check your Wufoo account permissions."
      ) : new Error(
        `Failed to initialize form monitoring: ${error.message || "Unknown error occurred"}. Please check your Wufoo connection.`
      );
    }
  },
  async onDisable() {
    console.log("Wufoo New Form trigger disabled and cleaned up");
  },
  async run(context) {
    var _a, _b, _c;
    let { nameFilter, includeInactive, responseFormat } = context.propsValue;
    try {
      let previousHashes = await context.store.get(LAST_FORM_IDS_KEY) || [], allForms = (await wufooApiCall({
        auth: context.auth,
        method: import_pieces_common9.HttpMethod.GET,
        resourceUri: `/forms.${responseFormat || "json"}`
      })).Forms || [], currentHashes = allForms.map((f) => f.Hash);
      await context.store.put(LAST_FORM_IDS_KEY, currentHashes);
      let newForms = allForms.filter((form) => !previousHashes.includes(form.Hash));
      if (nameFilter && nameFilter.trim()) {
        let filterText = nameFilter.trim().toLowerCase();
        newForms = newForms.filter(
          (form) => form.Name && form.Name.toLowerCase().includes(filterText)
        );
      }
      return includeInactive || (newForms = newForms.filter((form) => form.IsPublic === "1")), newForms.map((form) => ({
        id: form.Hash,
        name: form.Name,
        description: form.Description,
        url: form.Url,
        isPublic: form.IsPublic === "1",
        isActive: form.IsPublic === "1",
        redirectUrl: form.RedirectUrl,
        dateCreated: form.DateCreated,
        dateUpdated: form.DateUpdated,
        entryCount: parseInt(form.EntryCount || "0", 10),
        language: form.Language || "english",
        rawFormData: form,
        triggerInfo: {
          detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
          source: "wufoo",
          type: "new_form"
        }
      }));
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error(
        "Authentication failed: Your API key may have expired. Please check your Wufoo authentication."
      ) : ((_b = error.response) == null ? void 0 : _b.status) === 429 ? new Error(
        "Rate limit exceeded: Wufoo API rate limit reached. Consider increasing your polling interval."
      ) : ((_c = error.response) == null ? void 0 : _c.status) === 403 ? new Error(
        "Access denied: You do not have permission to list forms. Please check your account permissions."
      ) : new Error(
        `Failed to check for new forms: ${error.message || "Unknown error occurred"}. The trigger will retry on the next polling interval.`
      );
    }
  },
  async test(context) {
    let { responseFormat } = context.propsValue;
    try {
      let forms = (await wufooApiCall({
        auth: context.auth,
        method: import_pieces_common9.HttpMethod.GET,
        resourceUri: `/forms.${responseFormat || "json"}`
      })).Forms || [];
      if (forms.length > 0) {
        let testForm = forms[0];
        return [
          {
            id: testForm.Hash,
            name: testForm.Name,
            description: testForm.Description,
            url: testForm.Url,
            isPublic: testForm.IsPublic === "1",
            isActive: testForm.IsPublic === "1",
            redirectUrl: testForm.RedirectUrl,
            dateCreated: testForm.DateCreated,
            dateUpdated: testForm.DateUpdated,
            entryCount: parseInt(testForm.EntryCount || "0", 10),
            language: testForm.Language || "english",
            rawFormData: testForm,
            triggerInfo: {
              detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
              source: "wufoo",
              type: "new_form"
            }
          }
        ];
      } else
        return [
          {
            id: "s1afea8b1vk0jf7",
            name: "Sample Contact Form",
            description: "A sample contact form for testing purposes",
            url: "https://example.wufoo.com/forms/s1afea8b1vk0jf7/",
            isPublic: !0,
            isActive: !0,
            redirectUrl: "https://example.com/thank-you",
            dateCreated: "2025-01-15 10:00:00",
            dateUpdated: "2025-01-15 10:00:00",
            entryCount: 0,
            language: "english",
            rawFormData: {
              Hash: "s1afea8b1vk0jf7",
              Name: "Sample Contact Form",
              Description: "A sample contact form for testing purposes",
              Url: "https://example.wufoo.com/forms/s1afea8b1vk0jf7/",
              IsPublic: "1",
              EntryCount: "0",
              DateCreated: "2025-01-15 10:00:00",
              DateUpdated: "2025-01-15 10:00:00",
              Language: "english"
            },
            triggerInfo: {
              detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
              source: "wufoo",
              type: "new_form"
            }
          }
        ];
    } catch (error) {
      return [
        {
          id: "s1afea8b1vk0jf7",
          name: "Test Contact Form",
          description: "A test form created for workflow testing",
          url: "https://example.wufoo.com/forms/s1afea8b1vk0jf7/",
          isPublic: !0,
          isActive: !0,
          redirectUrl: "https://example.com/thank-you",
          dateCreated: "2025-01-15 10:00:00",
          dateUpdated: "2025-01-15 10:00:00",
          entryCount: 5,
          language: "english",
          rawFormData: {
            Hash: "s1afea8b1vk0jf7",
            Name: "Test Contact Form",
            Description: "A test form created for workflow testing",
            Url: "https://example.wufoo.com/forms/s1afea8b1vk0jf7/",
            IsPublic: "1",
            EntryCount: "5",
            DateCreated: "2025-01-15 10:00:00",
            DateUpdated: "2025-01-15 10:00:00",
            Language: "english"
          },
          triggerInfo: {
            detectedAt: (/* @__PURE__ */ new Date()).toISOString(),
            source: "wufoo",
            type: "new_form"
          }
        }
      ];
    }
  },
  sampleData: {
    id: "s1afea8b1vk0jf7",
    name: "Customer Feedback Form",
    description: "Collect valuable feedback from our customers",
    url: "https://example.wufoo.com/forms/s1afea8b1vk0jf7/",
    isPublic: !0,
    isActive: !0,
    redirectUrl: "https://example.com/thank-you",
    dateCreated: "2025-01-15 09:30:00",
    dateUpdated: "2025-01-15 09:30:00",
    entryCount: 0,
    language: "english",
    rawFormData: {
      Hash: "s1afea8b1vk0jf7",
      Name: "Customer Feedback Form",
      Description: "Collect valuable feedback from our customers",
      Url: "https://example.wufoo.com/forms/s1afea8b1vk0jf7/",
      IsPublic: "1",
      EntryCount: "0",
      DateCreated: "2025-01-15 09:30:00",
      DateUpdated: "2025-01-15 09:30:00",
      Language: "english"
    },
    triggerInfo: {
      detectedAt: "2025-01-15T09:30:00.000Z",
      source: "wufoo",
      type: "new_form"
    }
  }
});

// ../ap/packages/pieces/community/wufoo/src/index.ts
var wufoo = (0, import_pieces_framework11.createPiece)({
  displayName: "Wufoo",
  auth: wufooAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/wufoo.png",
  authors: ["krushnarout", "onyedikachi-david"],
  actions: [
    createFormEntryAction,
    findFormAction,
    findSubmissionByFieldAction,
    getEntryDetailsAction,
    (0, import_pieces_common10.createCustomApiCallAction)({
      auth: wufooAuth,
      baseUrl: (auth) => `https://${auth.subdomain}.wufoo.com/api/v3`,
      authMapping: async (auth) => {
        let { apiKey } = auth.props;
        return {
          Authorization: `Basic ${Buffer.from(`${apiKey}:footastic`).toString("base64")}`
        };
      }
    })
  ],
  triggers: [newFormEntryTrigger, newFormTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  wufoo
});
