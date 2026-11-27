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

// ../ap/packages/pieces/community/mycase-piece/src/index.ts
var index_exports = {};
__export(index_exports, {
  mycasePiece: () => mycasePiece
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework44 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-case.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mycase-piece/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), mycaseAuthDescription = `
## MyCase OAuth 2.0 Setup

### Obtaining Client Credentials
1. Contact MyCase support at [https://www.mycase.com/support/](https://www.mycase.com/support/) to request OAuth 2.0 client credentials
2. Provide your application details and request a client ID and client secret
3. MyCase support will provide you with:
   - **Client ID**: Your OAuth Client ID
   - **Client Secret**: Your OAuth Client secret
   - **Redirect URI**: A pre-configured redirect URI for your application

### Required Permissions
The authorizing user must have the **"Manage your firm's preferences, billing, and payment options"** permission set to **Yes** in MyCase.

### Authorization Flow
MyCase uses OAuth 2.0 Authorization Code Grant flow with the following endpoints:

**Authorization URL**: \`https://auth.mycase.com/login_sessions/new\`
**Token URL**: \`https://auth.mycase.com/tokens\`

### Rate Limits
- 25 requests per second per client
- Access tokens are valid for 24 hours
- Refresh tokens are valid for 2 weeks

For detailed API documentation, visit: [https://mycaseapi.stoplight.io/docs/mycase-api-documentation/k5xpc4jyhkom7-getting-started](https://mycaseapi.stoplight.io/docs/mycase-api-documentation/k5xpc4jyhkom7-getting-started)
`, mycaseAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: mycaseAuthDescription,
  authUrl: "https://auth.mycase.com/login_sessions/new",
  tokenUrl: "https://auth.mycase.com/tokens",
  required: !0,
  scope: [],
  pkce: !0
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/common/mycase-api.ts
var import_pieces_common = require("@activepieces/pieces-common");
var MyCaseApi = class {
  constructor(auth) {
    __publicField(this, "auth", auth);
    __publicField(this, "baseUrl", "https://external-integrations.mycase.com/v1");
  }
  async makeRequest(method, endpoint, body, queryParams) {
    try {
      let url = `${this.baseUrl}${endpoint}`, response = await import_pieces_common.httpClient.sendRequest({
        method,
        url,
        headers: {
          Authorization: `Bearer ${this.auth.access_token}`,
          "Content-Type": "application/json"
        },
        body: body ? JSON.stringify(body) : void 0,
        queryParams
      });
      return response.status >= 200 && response.status < 300 ? {
        success: !0,
        data: response.body
      } : response.status === 401 ? {
        success: !1,
        error: "Unauthorized - Access token may be expired",
        details: response.body
      } : {
        success: !1,
        error: `API request failed with status: ${response.status}`,
        details: response.body
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to make API request",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
  async get(endpoint, queryParams) {
    return this.makeRequest(import_pieces_common.HttpMethod.GET, endpoint, void 0, queryParams);
  }
  async post(endpoint, body) {
    return this.makeRequest(import_pieces_common.HttpMethod.POST, endpoint, body);
  }
  async put(endpoint, body) {
    return this.makeRequest(import_pieces_common.HttpMethod.PUT, endpoint, body);
  }
  async delete(endpoint) {
    return this.makeRequest(import_pieces_common.HttpMethod.DELETE, endpoint);
  }
};
function createMyCaseApi(auth) {
  return new MyCaseApi(auth);
}

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-case.ts
var createCase = (0, import_pieces_framework2.createAction)({
  auth: mycaseAuth,
  name: "create_case",
  displayName: "Create Case",
  description: "Creates a new case in MyCase",
  audience: "both",
  aiMetadata: { description: "Always inserts a new MyCase case with the given name plus optional stage, practice area, client/company/staff associations, and billing details. Case name must be unique and stage/practice-area values must already exist in MyCase. Not idempotent: calling twice creates two cases, so use Find or Create Case when you want to avoid duplicates, or Update Case to modify an existing one.", idempotent: !1 },
  props: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Case Name",
      description: "The name of the case (must be unique)",
      required: !0
    }),
    case_number: import_pieces_framework2.Property.ShortText({
      displayName: "Case Number",
      description: "An identifier for this case (recommended to be unique)",
      required: !1
    }),
    opened_date: import_pieces_framework2.Property.ShortText({
      displayName: "Opened Date",
      description: "The date the case was created/opened (ISO-8601 format: YYYY-MM-DD)",
      required: !1
    }),
    case_stage: import_pieces_framework2.Property.ShortText({
      displayName: "Case Stage",
      description: "The stage the case is currently in (must match MyCase system)",
      required: !1
    }),
    practice_area: import_pieces_framework2.Property.ShortText({
      displayName: "Practice Area",
      description: "The practice area for this case (must match MyCase system)",
      required: !1
    }),
    description: import_pieces_framework2.Property.LongText({
      displayName: "Description",
      description: "A description for the case",
      required: !1
    }),
    sol_date: import_pieces_framework2.Property.ShortText({
      displayName: "Statute of Limitations Date",
      description: "The statute of limitations date (ISO-8601 date format: YYYY-MM-DD)",
      required: !1
    }),
    status: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Status",
      description: "The status of the case",
      required: !1,
      options: {
        options: [
          { label: "Open", value: "open" },
          { label: "Closed", value: "closed" }
        ]
      },
      defaultValue: "open"
    }),
    outstanding_balance: import_pieces_framework2.Property.Number({
      displayName: "Outstanding Balance",
      description: "The outstanding balance of this case",
      required: !1
    }),
    billing_contact_id: import_pieces_framework2.Property.Number({
      displayName: "Billing Contact ID",
      description: "The ID of the client or company to assign as the billing contact",
      required: !1
    }),
    client_ids: import_pieces_framework2.Property.ShortText({
      displayName: "Client IDs",
      description: "Comma-separated list of client IDs to associate with the case",
      required: !1
    }),
    company_ids: import_pieces_framework2.Property.ShortText({
      displayName: "Company IDs",
      description: "Comma-separated list of company IDs to associate with the case",
      required: !1
    }),
    staff_id: import_pieces_framework2.Property.Number({
      displayName: "Staff ID",
      description: "ID of staff member to associate with the case",
      required: !1
    }),
    lead_lawyer: import_pieces_framework2.Property.Checkbox({
      displayName: "Lead Lawyer",
      description: "Is the staff member the lead lawyer for this case?",
      required: !1,
      defaultValue: !1
    }),
    originating_lawyer: import_pieces_framework2.Property.Checkbox({
      displayName: "Originating Lawyer",
      description: "Is the staff member the originating lawyer for this case?",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      name: context.propsValue.name
    };
    if (context.propsValue.case_number && (requestBody.case_number = context.propsValue.case_number), context.propsValue.opened_date && (requestBody.opened_date = context.propsValue.opened_date), context.propsValue.case_stage && (requestBody.case_stage = context.propsValue.case_stage), context.propsValue.practice_area && (requestBody.practice_area = context.propsValue.practice_area), context.propsValue.description && (requestBody.description = context.propsValue.description), context.propsValue.sol_date && (requestBody.sol_date = context.propsValue.sol_date), context.propsValue.status && (requestBody.status = context.propsValue.status), context.propsValue.outstanding_balance !== void 0 && (requestBody.outstanding_balance = context.propsValue.outstanding_balance), context.propsValue.billing_contact_id && (requestBody.billing_contact = {
      id: context.propsValue.billing_contact_id
    }), context.propsValue.client_ids) {
      let clientIds = context.propsValue.client_ids.split(",").map((id) => parseInt(id.trim())).filter((id) => !isNaN(id));
      clientIds.length > 0 && (requestBody.clients = clientIds.map((id) => ({ id })));
    }
    if (context.propsValue.company_ids) {
      let companyIds = context.propsValue.company_ids.split(",").map((id) => parseInt(id.trim())).filter((id) => !isNaN(id));
      companyIds.length > 0 && (requestBody.companies = companyIds.map((id) => ({ id })));
    }
    context.propsValue.staff_id && (requestBody.staff = [{
      id: context.propsValue.staff_id,
      lead_lawyer: context.propsValue.lead_lawyer || !1,
      originating_lawyer: context.propsValue.originating_lawyer || !1
    }]);
    try {
      let response = await api.post("/cases", requestBody);
      return response.success ? {
        success: !0,
        case: response.data,
        message: `Case "${context.propsValue.name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create case",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-case-stage.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var createCaseStage = (0, import_pieces_framework3.createAction)({
  auth: mycaseAuth,
  name: "create_case_stage",
  displayName: "Create Case Stage",
  description: "Creates a new case stage in MyCase",
  audience: "both",
  aiMetadata: { description: "Always inserts a new case stage with the given name, which can then be assigned to cases. Not idempotent: calling twice with the same name creates two stages, so verify the stage does not already exist before creating.", idempotent: !1 },
  props: {
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Case Stage Name",
      description: "The name of the case stage",
      required: !0
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      name: context.propsValue.name
    };
    try {
      let response = await api.post("/case_stages", requestBody);
      return response.success ? {
        success: !0,
        case_stage: response.data,
        message: `Case stage "${context.propsValue.name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create case stage",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-company.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var createCompany = (0, import_pieces_framework4.createAction)({
  auth: mycaseAuth,
  name: "create_company",
  displayName: "Create Company",
  description: "Creates a new company in MyCase",
  audience: "both",
  aiMetadata: { description: "Create a new company (organization) record in MyCase, optionally setting contact details, an address, and linking it to existing cases and clients. Use when adding a firm or business contact; not idempotent, so calling it repeatedly creates duplicate companies. To revise an existing company instead, use Update Company.", idempotent: !1 },
  props: {
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Company Name",
      description: "The name of the company",
      required: !0
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      description: "Company email address",
      required: !1
    }),
    website: import_pieces_framework4.Property.ShortText({
      displayName: "Website",
      description: "Company website URL",
      required: !1
    }),
    main_phone_number: import_pieces_framework4.Property.ShortText({
      displayName: "Main Phone Number",
      description: "Main phone number for the company",
      required: !1
    }),
    fax_phone_number: import_pieces_framework4.Property.ShortText({
      displayName: "Fax Phone Number",
      description: "Fax phone number for the company",
      required: !1
    }),
    address1: import_pieces_framework4.Property.ShortText({
      displayName: "Address Line 1",
      description: "Street address line 1",
      required: !1
    }),
    address2: import_pieces_framework4.Property.ShortText({
      displayName: "Address Line 2",
      description: "Street address line 2",
      required: !1
    }),
    city: import_pieces_framework4.Property.ShortText({
      displayName: "City",
      description: "City",
      required: !1
    }),
    state: import_pieces_framework4.Property.ShortText({
      displayName: "State",
      description: "State",
      required: !1
    }),
    zip_code: import_pieces_framework4.Property.ShortText({
      displayName: "ZIP Code",
      description: "ZIP/Postal code",
      required: !1
    }),
    country: import_pieces_framework4.Property.ShortText({
      displayName: "Country",
      description: "Country",
      required: !1
    }),
    notes: import_pieces_framework4.Property.LongText({
      displayName: "Notes",
      description: "Additional information about the company (visible to firm members only)",
      required: !1
    }),
    cases: import_pieces_framework4.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Cases",
      description: "Cases to associate with the company",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""}`,
            value: caseItem.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    }),
    clients: import_pieces_framework4.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Clients",
      description: "Clients to associate with the company",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/clients", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((client) => ({
            label: `${client.first_name} ${client.last_name}${client.email ? ` (${client.email})` : ""}`,
            value: client.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load clients"
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      name: context.propsValue.name
    };
    context.propsValue.email && (requestBody.email = context.propsValue.email), context.propsValue.website && (requestBody.website = context.propsValue.website), context.propsValue.main_phone_number && (requestBody.main_phone_number = context.propsValue.main_phone_number), context.propsValue.fax_phone_number && (requestBody.fax_phone_number = context.propsValue.fax_phone_number), context.propsValue.notes && (requestBody.notes = context.propsValue.notes), (context.propsValue.address1 || context.propsValue.city || context.propsValue.state || context.propsValue.zip_code || context.propsValue.country) && (requestBody.address = {
      address1: context.propsValue.address1 || "",
      address2: context.propsValue.address2 || "",
      city: context.propsValue.city || "",
      state: context.propsValue.state || "",
      zip_code: context.propsValue.zip_code || "",
      country: context.propsValue.country || ""
    }), context.propsValue.cases && Array.isArray(context.propsValue.cases) && (requestBody.cases = context.propsValue.cases.map((id) => ({ id: parseInt(id) }))), context.propsValue.clients && Array.isArray(context.propsValue.clients) && (requestBody.clients = context.propsValue.clients.map((id) => ({ id: parseInt(id) })));
    try {
      let response = await api.post("/companies", requestBody);
      return response.success ? {
        success: !0,
        company: response.data,
        message: `Company "${context.propsValue.name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create company",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-custom-field.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var createCustomField = (0, import_pieces_framework5.createAction)({
  auth: mycaseAuth,
  name: "create_custom_field",
  displayName: "Create Custom Field",
  description: "Creates a new custom field in MyCase",
  audience: "both",
  aiMetadata: { description: 'Always defines a new custom field on a MyCase resource type (case, client, company, expense, time, or time-and-expense), with a chosen field type. When the field type is "list" you must supply at least one list option. This is a schema/configuration change, not data entry. Not idempotent: each call adds another custom field.', idempotent: !1 },
  props: {
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Field Name",
      description: "The name of the custom field",
      required: !0
    }),
    parent_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Parent Type",
      description: "The parent resource type of the custom field",
      required: !0,
      options: {
        options: [
          { label: "Case", value: "case" },
          { label: "Client", value: "client" },
          { label: "Company", value: "company" },
          { label: "Expense", value: "expense" },
          { label: "Time", value: "time" },
          { label: "Time and Expense", value: "time_and_expense" }
        ]
      }
    }),
    field_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Field Type",
      description: "The field type of the custom field",
      required: !0,
      options: {
        options: [
          { label: "Short Text", value: "short_text" },
          { label: "Long Text", value: "long_text" },
          { label: "Numeric", value: "numeric" },
          { label: "Boolean", value: "boolean" },
          { label: "Date", value: "date" },
          { label: "List", value: "list" },
          { label: "Currency", value: "currency" }
        ]
      }
    }),
    list_options: import_pieces_framework5.Property.Array({
      displayName: "List Options",
      description: 'List of options for the custom field (required if field type is "list")',
      required: !1,
      properties: {
        option: import_pieces_framework5.Property.ShortText({
          displayName: "Option Value",
          description: "The name of the list option",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      name: context.propsValue.name,
      parent_type: context.propsValue.parent_type,
      field_type: context.propsValue.field_type
    };
    if (context.propsValue.field_type === "list") {
      if (!context.propsValue.list_options || !Array.isArray(context.propsValue.list_options))
        return {
          success: !1,
          error: 'List options are required when field type is "list"'
        };
      if (context.propsValue.list_options.length === 0)
        return {
          success: !1,
          error: "At least one list option must be provided"
        };
      requestBody.list_options = context.propsValue.list_options.map((item) => ({
        option_value: item.option
      }));
    }
    try {
      let response = await api.post("/custom_fields", requestBody);
      return response.success ? {
        success: !0,
        custom_field: response.data,
        message: `Custom field "${context.propsValue.name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create custom field",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-document.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var createDocument = (0, import_pieces_framework6.createAction)({
  auth: mycaseAuth,
  name: "create_document",
  displayName: "Create Document",
  description: "Creates a new document in MyCase",
  audience: "both",
  aiMetadata: { description: "Always creates a new document metadata record in MyCase, either as a firm document or attached to a specific case (firm vs case mode is selected by Document Type, and a case must be chosen in case mode). This registers the document and returns a pre-signed S3 upload URL; it does not upload file bytes itself. Not idempotent: each call creates another document record.", idempotent: !1 },
  props: {
    document_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Document Type",
      description: "Choose whether to create a firm document or associate it with a specific case",
      required: !0,
      options: {
        options: [
          { label: "Firm Document", value: "firm" },
          { label: "Case Document", value: "case" }
        ]
      },
      defaultValue: "firm"
    }),
    case: import_pieces_framework6.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Case",
      description: "Select the case to associate this document with",
      required: !1,
      refreshers: ["document_type"],
      options: async ({ auth, document_type }) => {
        if (!auth || document_type !== "case")
          return {
            disabled: !0,
            options: [],
            placeholder: 'Select "Case Document" above to choose a case'
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""}`,
            value: caseItem.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    }),
    path: import_pieces_framework6.Property.ShortText({
      displayName: "Document Path",
      description: "The relative path including document name (e.g., folder1/folder2/document_name). Folders will be created automatically.",
      required: !0
    }),
    filename: import_pieces_framework6.Property.ShortText({
      displayName: "Filename",
      description: "The filename of the physical file including extension (e.g., file.pdf)",
      required: !0
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      description: "The description of this document",
      required: !1
    }),
    assigned_date: import_pieces_framework6.Property.DateTime({
      displayName: "Assigned Date",
      description: "The assigned date of this document",
      required: !1
    }),
    staff: import_pieces_framework6.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Staff",
      description: "Staff members to share this document with (only for firm documents)",
      required: !1,
      refreshers: ["document_type"],
      options: async ({ auth, document_type }) => {
        if (!auth || document_type === "case")
          return {
            disabled: !0,
            options: [],
            placeholder: "Only available for firm documents"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((staff) => ({
            label: `${staff.first_name} ${staff.last_name}`,
            value: staff.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    })
  },
  async run(context) {
    var _a, _b;
    let api = createMyCaseApi(context.auth);
    if (context.propsValue.document_type === "case" && !context.propsValue.case)
      return {
        success: !1,
        error: "Case must be selected when creating a case document"
      };
    let requestBody = {
      path: context.propsValue.path,
      filename: context.propsValue.filename
    };
    if (context.propsValue.description && (requestBody.description = context.propsValue.description), context.propsValue.assigned_date) {
      let date = new Date(context.propsValue.assigned_date);
      requestBody.assigned_date = date.toISOString().split("T")[0];
    }
    context.propsValue.document_type === "firm" && context.propsValue.staff && Array.isArray(context.propsValue.staff) && (requestBody.staff = context.propsValue.staff.map((id) => ({ id: parseInt(id) })));
    try {
      let endpoint, successMessage;
      context.propsValue.document_type === "case" ? (endpoint = `/cases/${context.propsValue.case}/documents`, successMessage = `Case document "${context.propsValue.path}" created successfully`) : (endpoint = "/documents", successMessage = `Firm document "${context.propsValue.path}" created successfully`);
      let response = await api.post(endpoint, requestBody);
      return response.success ? {
        success: !0,
        document: response.data,
        message: successMessage,
        upload_info: {
          put_url: (_a = response.data) == null ? void 0 : _a.put_url,
          put_headers: (_b = response.data) == null ? void 0 : _b.put_headers,
          note: "Use the put_url and put_headers to upload your file to Amazon S3. Set Content-Type: application/octet-stream"
        }
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create document",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-event.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var createEvent = (0, import_pieces_framework7.createAction)({
  auth: mycaseAuth,
  name: "create_event",
  displayName: "Create Event",
  description: "Creates a new event in MyCase",
  audience: "both",
  aiMetadata: { description: "Create a new calendar event in MyCase with a name, start/end times, and at least one assigned staff member, optionally flagging required attendees, marking all-day or private, and linking a location and case. Use to schedule meetings, hearings, or appointments; not idempotent, so repeated calls create duplicate events.", idempotent: !1 },
  props: {
    name: import_pieces_framework7.Property.ShortText({
      displayName: "Event Name",
      description: "The name of this event",
      required: !0
    }),
    start: import_pieces_framework7.Property.DateTime({
      displayName: "Start Date/Time",
      description: "The start date and time of this event",
      required: !0
    }),
    end: import_pieces_framework7.Property.DateTime({
      displayName: "End Date/Time",
      description: "The end date and time of this event",
      required: !0
    }),
    staff: import_pieces_framework7.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Staff",
      description: "Staff members to associate with this event",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((staff) => ({
            label: `${staff.first_name} ${staff.last_name}`,
            value: staff.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    }),
    required_staff: import_pieces_framework7.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Required Staff",
      description: "Staff members who are required to attend (all others are optional)",
      required: !1,
      refreshers: ["staff"],
      options: async ({ auth, staff }) => {
        if (!auth || !staff || !Array.isArray(staff) || staff.length === 0)
          return {
            disabled: !0,
            options: [],
            placeholder: "Select staff members first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.filter((s) => staff.includes(s.id.toString())).map((s) => ({
            label: `${s.first_name} ${s.last_name}`,
            value: s.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    }),
    description: import_pieces_framework7.Property.LongText({
      displayName: "Description",
      description: "Event description",
      required: !1
    }),
    all_day: import_pieces_framework7.Property.Checkbox({
      displayName: "All Day Event",
      description: "Is this an all day event?",
      required: !1,
      defaultValue: !1
    }),
    private: import_pieces_framework7.Property.Checkbox({
      displayName: "Private Event",
      description: "Is this a private event?",
      required: !1,
      defaultValue: !1
    }),
    location: import_pieces_framework7.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Location",
      description: "The location associated with this event",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/locations", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((location) => ({
            label: location.name,
            value: location.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load locations"
        };
      }
    }),
    case: import_pieces_framework7.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Case",
      description: "The case associated with this event",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""}`,
            value: caseItem.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth);
    if (!context.propsValue.staff || !Array.isArray(context.propsValue.staff) || context.propsValue.staff.length === 0)
      return {
        success: !1,
        error: "At least one staff member must be selected"
      };
    let requestBody = {
      name: context.propsValue.name,
      start: new Date(context.propsValue.start).toISOString(),
      end: new Date(context.propsValue.end).toISOString(),
      staff: context.propsValue.staff.map((staffId) => {
        var _a;
        let isRequired = ((_a = context.propsValue.required_staff) == null ? void 0 : _a.includes(staffId)) || !1;
        return {
          id: parseInt(staffId),
          required: isRequired
        };
      })
    };
    context.propsValue.description && (requestBody.description = context.propsValue.description), context.propsValue.all_day !== void 0 && (requestBody.all_day = context.propsValue.all_day), context.propsValue.private !== void 0 && (requestBody.private = context.propsValue.private), context.propsValue.location && (requestBody.location = { id: parseInt(context.propsValue.location) }), context.propsValue.case && (requestBody.case = { id: parseInt(context.propsValue.case) });
    try {
      let response = await api.post("/events", requestBody);
      return response.success ? {
        success: !0,
        event: response.data,
        message: `Event "${context.propsValue.name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create event",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-expense.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var createExpense = (0, import_pieces_framework8.createAction)({
  auth: mycaseAuth,
  name: "create_expense",
  displayName: "Create Expense",
  description: "Creates a new expense in MyCase",
  audience: "both",
  aiMetadata: { description: "Always inserts a new expense entry tied to a specific case, with activity name, entry date, cost, units, billable flag, and an optional staff member. The activity name is created automatically if it does not already exist. Not idempotent: each call records another expense, so guard against re-runs when retrying.", idempotent: !1 },
  props: {
    activity_name: import_pieces_framework8.Property.ShortText({
      displayName: "Activity Name",
      description: "The activity name for this expense (will be created if it doesn't exist)",
      required: !0
    }),
    case: import_pieces_framework8.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Case",
      description: "The case to associate with this expense",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""}`,
            value: caseItem.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    }),
    entry_date: import_pieces_framework8.Property.DateTime({
      displayName: "Entry Date",
      description: "The entry date of this expense",
      required: !0
    }),
    cost: import_pieces_framework8.Property.Number({
      displayName: "Cost",
      description: "Cost of the expense",
      required: !0
    }),
    units: import_pieces_framework8.Property.Number({
      displayName: "Units",
      description: "Number of units",
      required: !0
    }),
    billable: import_pieces_framework8.Property.Checkbox({
      displayName: "Billable",
      description: "Is this expense billable?",
      required: !0,
      defaultValue: !0
    }),
    description: import_pieces_framework8.Property.LongText({
      displayName: "Description",
      description: "Description of the expense",
      required: !1
    }),
    staff: import_pieces_framework8.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Staff",
      description: "The staff member associated with this expense",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((staff) => ({
            label: `${staff.first_name} ${staff.last_name}`,
            value: staff.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      activity_name: context.propsValue.activity_name,
      case: { id: parseInt(context.propsValue.case) },
      entry_date: new Date(context.propsValue.entry_date).toISOString().split("T")[0],
      cost: context.propsValue.cost,
      units: context.propsValue.units,
      billable: context.propsValue.billable
    };
    context.propsValue.description && (requestBody.description = context.propsValue.description), context.propsValue.staff && (requestBody.staff = { id: parseInt(context.propsValue.staff) });
    try {
      let response = await api.post("/expenses", requestBody);
      return response.success ? {
        success: !0,
        expense: response.data,
        message: `Expense "${context.propsValue.activity_name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create expense",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-lead.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var createLead = (0, import_pieces_framework9.createAction)({
  auth: mycaseAuth,
  name: "create_lead",
  displayName: "Create Lead",
  description: "Creates a new lead in MyCase",
  audience: "both",
  aiMetadata: { description: "Always inserts a new lead (prospective client) with first and last name plus optional contact, address, drivers-license, referral-source, and referred-by details. Distinct from Create Person, which creates an established client rather than a lead. Not idempotent: each call creates another lead.", idempotent: !1 },
  props: {
    first_name: import_pieces_framework9.Property.ShortText({
      displayName: "First Name",
      description: "The first name of the lead",
      required: !0
    }),
    last_name: import_pieces_framework9.Property.ShortText({
      displayName: "Last Name",
      description: "The last name of the lead",
      required: !0
    }),
    email: import_pieces_framework9.Property.ShortText({
      displayName: "Email",
      description: "The email of the lead",
      required: !1
    }),
    middle_initial: import_pieces_framework9.Property.ShortText({
      displayName: "Middle Initial",
      description: "The middle name or initial of the lead",
      required: !1
    }),
    cell_phone_number: import_pieces_framework9.Property.ShortText({
      displayName: "Cell Phone Number",
      description: "Cell phone number",
      required: !1
    }),
    work_phone_number: import_pieces_framework9.Property.ShortText({
      displayName: "Work Phone Number",
      description: "Work phone number",
      required: !1
    }),
    home_phone_number: import_pieces_framework9.Property.ShortText({
      displayName: "Home Phone Number",
      description: "Home phone number",
      required: !1
    }),
    birthdate: import_pieces_framework9.Property.DateTime({
      displayName: "Birthdate",
      description: "Date of birth",
      required: !1
    }),
    drivers_license_number: import_pieces_framework9.Property.ShortText({
      displayName: "Drivers License Number",
      description: "Drivers license number",
      required: !1
    }),
    drivers_license_state: import_pieces_framework9.Property.ShortText({
      displayName: "Drivers License State",
      description: "Drivers license state",
      required: !1
    }),
    lead_details: import_pieces_framework9.Property.LongText({
      displayName: "Lead Details",
      description: "Additional information (visible to firm members only)",
      required: !1
    }),
    address1: import_pieces_framework9.Property.ShortText({
      displayName: "Address Line 1",
      description: "Street address line 1",
      required: !1
    }),
    address2: import_pieces_framework9.Property.ShortText({
      displayName: "Address Line 2",
      description: "Street address line 2",
      required: !1
    }),
    city: import_pieces_framework9.Property.ShortText({
      displayName: "City",
      description: "City",
      required: !1
    }),
    state: import_pieces_framework9.Property.ShortText({
      displayName: "State",
      description: "State",
      required: !1
    }),
    zip_code: import_pieces_framework9.Property.ShortText({
      displayName: "ZIP Code",
      description: "ZIP/Postal code",
      required: !1
    }),
    country: import_pieces_framework9.Property.ShortText({
      displayName: "Country",
      description: "Country",
      required: !1
    }),
    referral_source: import_pieces_framework9.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Referral Source",
      description: "The referral source to associate with this lead",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/referral_sources", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((source) => ({
            label: source.name,
            value: source.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load referral sources"
        };
      }
    }),
    referred_by: import_pieces_framework9.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Referred By",
      description: "Existing client or company who referred this lead",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let api = createMyCaseApi(auth), clientsResponse = await api.get("/clients", { page_size: "50" }), clientOptions = clientsResponse.success && Array.isArray(clientsResponse.data) ? clientsResponse.data.map((client) => ({
          label: `Client: ${client.first_name} ${client.last_name}${client.email ? ` (${client.email})` : ""}`,
          value: `client_${client.id}`
        })) : [], companiesResponse = await api.get("/companies", { page_size: "50" }), companyOptions = companiesResponse.success && Array.isArray(companiesResponse.data) ? companiesResponse.data.map((company) => ({
          label: `Company: ${company.name}`,
          value: `company_${company.id}`
        })) : [];
        return {
          disabled: !1,
          options: [...clientOptions, ...companyOptions]
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      first_name: context.propsValue.first_name,
      last_name: context.propsValue.last_name
    };
    if (context.propsValue.email && (requestBody.email = context.propsValue.email), context.propsValue.middle_initial && (requestBody.middle_initial = context.propsValue.middle_initial), context.propsValue.cell_phone_number && (requestBody.cell_phone_number = context.propsValue.cell_phone_number), context.propsValue.work_phone_number && (requestBody.work_phone_number = context.propsValue.work_phone_number), context.propsValue.home_phone_number && (requestBody.home_phone_number = context.propsValue.home_phone_number), context.propsValue.birthdate) {
      let date = new Date(context.propsValue.birthdate);
      requestBody.birthdate = date.toISOString().split("T")[0];
    }
    if (context.propsValue.drivers_license_number && (requestBody.drivers_license_number = context.propsValue.drivers_license_number), context.propsValue.drivers_license_state && (requestBody.drivers_license_state = context.propsValue.drivers_license_state), context.propsValue.lead_details && (requestBody.lead_details = context.propsValue.lead_details), (context.propsValue.address1 || context.propsValue.city || context.propsValue.state || context.propsValue.zip_code || context.propsValue.country) && (requestBody.address = {
      address1: context.propsValue.address1 || "",
      address2: context.propsValue.address2 || "",
      city: context.propsValue.city || "",
      state: context.propsValue.state || "",
      zip_code: context.propsValue.zip_code || "",
      country: context.propsValue.country || ""
    }), context.propsValue.referral_source && (requestBody.referral_source_reference = {
      id: parseInt(context.propsValue.referral_source)
    }), context.propsValue.referred_by) {
      let [type, idStr] = context.propsValue.referred_by.split("_"), id = parseInt(idStr);
      isNaN(id) || (requestBody.referred_by = { id });
    }
    try {
      let response = await api.post("/leads", requestBody);
      return response.success ? {
        success: !0,
        lead: response.data,
        message: `Lead "${context.propsValue.first_name} ${context.propsValue.last_name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create lead",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-location.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var createLocation = (0, import_pieces_framework10.createAction)({
  auth: mycaseAuth,
  name: "create_location",
  displayName: "Create Location",
  description: "Creates a new location in MyCase",
  audience: "both",
  aiMetadata: { description: "Always inserts a new location with the given name and optional address fields. Not idempotent: calling twice with the same name creates two locations, so use Find or Create Location when you want to avoid duplicates.", idempotent: !1 },
  props: {
    name: import_pieces_framework10.Property.ShortText({
      displayName: "Location Name",
      description: "The name of the location",
      required: !0
    }),
    address1: import_pieces_framework10.Property.ShortText({
      displayName: "Address Line 1",
      description: "Street address line 1",
      required: !1
    }),
    address2: import_pieces_framework10.Property.ShortText({
      displayName: "Address Line 2",
      description: "Street address line 2",
      required: !1
    }),
    city: import_pieces_framework10.Property.ShortText({
      displayName: "City",
      description: "City",
      required: !1
    }),
    state: import_pieces_framework10.Property.ShortText({
      displayName: "State",
      description: "State",
      required: !1
    }),
    zip_code: import_pieces_framework10.Property.ShortText({
      displayName: "ZIP Code",
      description: "ZIP/Postal code",
      required: !1
    }),
    country: import_pieces_framework10.Property.ShortText({
      displayName: "Country",
      description: "Country",
      required: !1
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      name: context.propsValue.name
    };
    (context.propsValue.address1 || context.propsValue.city || context.propsValue.state || context.propsValue.zip_code || context.propsValue.country) && (requestBody.address = {
      address1: context.propsValue.address1 || "",
      address2: context.propsValue.address2 || "",
      city: context.propsValue.city || "",
      state: context.propsValue.state || "",
      zip_code: context.propsValue.zip_code || "",
      country: context.propsValue.country || ""
    });
    try {
      let response = await api.post("/locations", requestBody);
      return response.success ? {
        success: !0,
        location: response.data,
        message: `Location "${context.propsValue.name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create location",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-note.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var createNote = (0, import_pieces_framework11.createAction)({
  auth: mycaseAuth,
  name: "create_note",
  displayName: "Create Note",
  description: "Creates a new note for a case, client, or company in MyCase",
  audience: "both",
  aiMetadata: { description: "Add a note (subject, body, and date) to a MyCase case, client, or company, selected via the entity type and entity ID. Use to attach written notes to a record; not idempotent, so repeated calls create duplicate notes.", idempotent: !1 },
  props: {
    entity_type: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Entity Type",
      description: "Choose the type of entity to create the note for",
      required: !0,
      options: {
        options: [
          { label: "Case", value: "case" },
          { label: "Client", value: "client" },
          { label: "Company", value: "company" }
        ]
      },
      defaultValue: "case"
    }),
    entity: import_pieces_framework11.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Entity",
      description: "Select the entity to create the note for",
      required: !0,
      refreshers: ["entity_type"],
      options: async ({ auth, entity_type }) => {
        if (!auth || !entity_type)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please select entity type first"
          };
        let api = createMyCaseApi(auth), endpoint = "", mapFunction = null;
        switch (entity_type) {
          case "case":
            endpoint = "/cases", mapFunction = (item) => ({
              label: `${item.name}${item.case_number ? ` (${item.case_number})` : ""}`,
              value: item.id.toString()
            });
            break;
          case "client":
            endpoint = "/clients", mapFunction = (item) => ({
              label: `${item.first_name} ${item.last_name}${item.email ? ` (${item.email})` : ""}`,
              value: item.id.toString()
            });
            break;
          case "company":
            endpoint = "/companies", mapFunction = (item) => ({
              label: item.name,
              value: item.id.toString()
            });
            break;
          default:
            return {
              disabled: !0,
              options: [],
              placeholder: "Invalid entity type"
            };
        }
        let response = await api.get(endpoint, { page_size: "100" });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map(mapFunction)
        } : {
          disabled: !0,
          options: [],
          placeholder: `Failed to load ${entity_type}s`
        };
      }
    }),
    subject: import_pieces_framework11.Property.ShortText({
      displayName: "Subject",
      description: "Subject of the note",
      required: !0
    }),
    note: import_pieces_framework11.Property.LongText({
      displayName: "Note",
      description: "The body text of the note",
      required: !0
    }),
    date: import_pieces_framework11.Property.DateTime({
      displayName: "Date",
      description: "The date and time of the note",
      required: !0
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      subject: context.propsValue.subject,
      note: context.propsValue.note,
      date: new Date(context.propsValue.date).toISOString()
    };
    try {
      let endpoint, entityName;
      switch (context.propsValue.entity_type) {
        case "case":
          endpoint = `/cases/${context.propsValue.entity}/notes`, entityName = "case";
          break;
        case "client":
          endpoint = `/clients/${context.propsValue.entity}/notes`, entityName = "client";
          break;
        case "company":
          endpoint = `/companies/${context.propsValue.entity}/notes`, entityName = "company";
          break;
        default:
          return {
            success: !1,
            error: "Invalid entity type"
          };
      }
      let response = await api.post(endpoint, requestBody);
      return response.success ? {
        success: !0,
        note: response.data,
        message: `Note "${context.propsValue.subject}" created successfully for ${entityName} ${context.propsValue.entity}`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create note",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-person.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var createPerson = (0, import_pieces_framework12.createAction)({
  auth: mycaseAuth,
  name: "create_person",
  displayName: "Create Person (Client)",
  description: "Creates a new individual client (person) in MyCase",
  audience: "both",
  aiMetadata: { description: "Always inserts a new individual client (person) with first and last name plus optional contact, address, people-group, and case associations. Not idempotent: calling twice creates two clients, so use Find or Create Person to avoid duplicates, or Update Person to edit an existing one.", idempotent: !1 },
  props: {
    first_name: import_pieces_framework12.Property.ShortText({
      displayName: "First Name",
      description: "The first name of the client",
      required: !0
    }),
    last_name: import_pieces_framework12.Property.ShortText({
      displayName: "Last Name",
      description: "The last name of the client",
      required: !0
    }),
    email: import_pieces_framework12.Property.ShortText({
      displayName: "Email",
      description: "The email of the client",
      required: !1
    }),
    middle_name: import_pieces_framework12.Property.ShortText({
      displayName: "Middle Name",
      description: "The middle name of the client",
      required: !1
    }),
    cell_phone_number: import_pieces_framework12.Property.ShortText({
      displayName: "Cell Phone Number",
      description: "Cell phone number",
      required: !1
    }),
    work_phone_number: import_pieces_framework12.Property.ShortText({
      displayName: "Work Phone Number",
      description: "Work phone number",
      required: !1
    }),
    home_phone_number: import_pieces_framework12.Property.ShortText({
      displayName: "Home Phone Number",
      description: "Home phone number",
      required: !1
    }),
    fax_phone_number: import_pieces_framework12.Property.ShortText({
      displayName: "Fax Phone Number",
      description: "Fax phone number",
      required: !1
    }),
    birthdate: import_pieces_framework12.Property.DateTime({
      displayName: "Birthdate",
      description: "Date of birth",
      required: !1
    }),
    notes: import_pieces_framework12.Property.LongText({
      displayName: "Notes",
      description: "Additional information (visible to firm members only)",
      required: !1
    }),
    address1: import_pieces_framework12.Property.ShortText({
      displayName: "Address Line 1",
      description: "Street address line 1",
      required: !1
    }),
    address2: import_pieces_framework12.Property.ShortText({
      displayName: "Address Line 2",
      description: "Street address line 2",
      required: !1
    }),
    city: import_pieces_framework12.Property.ShortText({
      displayName: "City",
      description: "City",
      required: !1
    }),
    state: import_pieces_framework12.Property.ShortText({
      displayName: "State",
      description: "State",
      required: !1
    }),
    zip_code: import_pieces_framework12.Property.ShortText({
      displayName: "ZIP Code",
      description: "ZIP/Postal code",
      required: !1
    }),
    country: import_pieces_framework12.Property.ShortText({
      displayName: "Country",
      description: "Country",
      required: !1
    }),
    people_group: import_pieces_framework12.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "People Group",
      description: "The people group to associate with this client",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/people_groups", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((group) => ({
            label: group.name,
            value: group.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load people groups"
        };
      }
    }),
    cases: import_pieces_framework12.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Cases",
      description: "Cases to associate with this client",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""}`,
            value: caseItem.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      first_name: context.propsValue.first_name,
      last_name: context.propsValue.last_name
    };
    if (context.propsValue.email && (requestBody.email = context.propsValue.email), context.propsValue.middle_name && (requestBody.middle_name = context.propsValue.middle_name), context.propsValue.cell_phone_number && (requestBody.cell_phone_number = context.propsValue.cell_phone_number), context.propsValue.work_phone_number && (requestBody.work_phone_number = context.propsValue.work_phone_number), context.propsValue.home_phone_number && (requestBody.home_phone_number = context.propsValue.home_phone_number), context.propsValue.fax_phone_number && (requestBody.fax_phone_number = context.propsValue.fax_phone_number), context.propsValue.birthdate) {
      let date = new Date(context.propsValue.birthdate);
      requestBody.birthdate = date.toISOString().split("T")[0];
    }
    context.propsValue.notes && (requestBody.notes = context.propsValue.notes), (context.propsValue.address1 || context.propsValue.city || context.propsValue.state || context.propsValue.zip_code || context.propsValue.country) && (requestBody.address = {
      address1: context.propsValue.address1 || "",
      address2: context.propsValue.address2 || "",
      city: context.propsValue.city || "",
      state: context.propsValue.state || "",
      zip_code: context.propsValue.zip_code || "",
      country: context.propsValue.country || ""
    }), context.propsValue.people_group && (requestBody.people_group = {
      id: parseInt(context.propsValue.people_group)
    }), context.propsValue.cases && Array.isArray(context.propsValue.cases) && (requestBody.cases = context.propsValue.cases.map((id) => ({ id: parseInt(id) })));
    try {
      let response = await api.post("/clients", requestBody);
      return response.success ? {
        success: !0,
        client: response.data,
        message: `Client "${context.propsValue.first_name} ${context.propsValue.last_name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create client",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-practice-area.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var createPracticeArea = (0, import_pieces_framework13.createAction)({
  auth: mycaseAuth,
  name: "create_practice_area",
  displayName: "Create Practice Area",
  description: "Creates a new practice area in MyCase",
  audience: "both",
  aiMetadata: { description: "Create a new practice area in MyCase by name. Use to add a new area of legal practice; not idempotent and performs no duplicate check, so repeated calls may create multiple practice areas with the same name. To look up existing practice areas first, use Find Practice Area.", idempotent: !1 },
  props: {
    name: import_pieces_framework13.Property.ShortText({
      displayName: "Practice Area Name",
      description: "The name of the practice area",
      required: !0
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      name: context.propsValue.name
    };
    try {
      let response = await api.post("/practice_areas", requestBody);
      return response.success ? {
        success: !0,
        practice_area: response.data,
        message: `Practice area "${context.propsValue.name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create practice area",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-referral-source.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var createReferralSource = (0, import_pieces_framework14.createAction)({
  auth: mycaseAuth,
  name: "create_referral_source",
  displayName: "Create Referral Source",
  description: "Creates a new referral source in MyCase",
  audience: "both",
  aiMetadata: { description: "Always inserts a new MyCase referral source with the given name. Not idempotent: calling twice with the same name creates two sources, so check Find Referral Source first if you need to avoid duplicates.", idempotent: !1 },
  props: {
    name: import_pieces_framework14.Property.ShortText({
      displayName: "Referral Source Name",
      description: "The name of the referral source",
      required: !0
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      name: context.propsValue.name
    };
    try {
      let response = await api.post("/referral_sources", requestBody);
      return response.success ? {
        success: !0,
        referral_source: response.data,
        message: `Referral source "${context.propsValue.name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create referral source",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-task.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var createTask = (0, import_pieces_framework15.createAction)({
  auth: mycaseAuth,
  name: "create_task",
  displayName: "Create Task",
  description: "Creates a new task in MyCase",
  audience: "both",
  aiMetadata: { description: "Create a new task in MyCase with a name, priority, due date, and at least one assigned staff member, optionally linking it to a case. Use to add to-do items for the firm; not idempotent, so repeated calls create duplicate tasks.", idempotent: !1 },
  props: {
    name: import_pieces_framework15.Property.ShortText({
      displayName: "Task Name",
      description: "The name of this task",
      required: !0
    }),
    priority: import_pieces_framework15.Property.StaticDropdown({
      displayName: "Priority",
      description: "The priority of this task",
      required: !0,
      options: {
        options: [
          { label: "Low", value: "Low" },
          { label: "Medium", value: "Medium" },
          { label: "High", value: "High" }
        ]
      },
      defaultValue: "Medium"
    }),
    due_date: import_pieces_framework15.Property.DateTime({
      displayName: "Due Date",
      description: "The due date of this task",
      required: !0
    }),
    staff: import_pieces_framework15.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Staff",
      description: "Staff members to assign this task to",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((staff) => ({
            label: `${staff.first_name} ${staff.last_name}`,
            value: staff.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    }),
    description: import_pieces_framework15.Property.LongText({
      displayName: "Description",
      description: "Task description",
      required: !1
    }),
    completed: import_pieces_framework15.Property.Checkbox({
      displayName: "Completed",
      description: "Is this task completed?",
      required: !1,
      defaultValue: !1
    }),
    case: import_pieces_framework15.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Case",
      description: "The case to associate with this task",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""}`,
            value: caseItem.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth);
    if (!context.propsValue.staff || !Array.isArray(context.propsValue.staff) || context.propsValue.staff.length === 0)
      return {
        success: !1,
        error: "At least one staff member must be selected"
      };
    let requestBody = {
      name: context.propsValue.name,
      priority: context.propsValue.priority,
      due_date: new Date(context.propsValue.due_date).toISOString().split("T")[0],
      staff: context.propsValue.staff.map((staffId) => ({
        id: parseInt(staffId)
      }))
    };
    context.propsValue.description && (requestBody.description = context.propsValue.description), context.propsValue.completed !== void 0 && (requestBody.completed = context.propsValue.completed), context.propsValue.case && (requestBody.case = { id: parseInt(context.propsValue.case) });
    try {
      let response = await api.post("/tasks", requestBody);
      return response.success ? {
        success: !0,
        task: response.data,
        message: `Task "${context.propsValue.name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create task",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-time-entry.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var createTimeEntry = (0, import_pieces_framework16.createAction)({
  auth: mycaseAuth,
  name: "create_time_entry",
  displayName: "Create Time Entry",
  description: "Creates a new time entry in MyCase",
  audience: "both",
  aiMetadata: { description: "Create a billable or non-billable time entry in MyCase against a specific case and staff member, recording an activity name, entry date, rate, and hours. Use to log work time for billing; not idempotent, so repeated calls create duplicate entries.", idempotent: !1 },
  props: {
    activity_name: import_pieces_framework16.Property.ShortText({
      displayName: "Activity Name",
      description: "The activity name for this time entry",
      required: !0
    }),
    case: import_pieces_framework16.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Case",
      description: "The case to associate with this time entry",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""}`,
            value: caseItem.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    }),
    staff: import_pieces_framework16.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Staff",
      description: "The staff member associated with this time entry",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((staff) => ({
            label: `${staff.first_name} ${staff.last_name}`,
            value: staff.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    }),
    entry_date: import_pieces_framework16.Property.DateTime({
      displayName: "Entry Date",
      description: "The entry date of this time entry",
      required: !0
    }),
    rate: import_pieces_framework16.Property.Number({
      displayName: "Rate",
      description: "The rate in dollars",
      required: !0
    }),
    hours: import_pieces_framework16.Property.Number({
      displayName: "Hours",
      description: "The duration in hours",
      required: !0
    }),
    description: import_pieces_framework16.Property.LongText({
      displayName: "Description",
      description: "Description of the time entry",
      required: !1
    }),
    billable: import_pieces_framework16.Property.Checkbox({
      displayName: "Billable",
      description: "Is this time entry billable?",
      required: !1,
      defaultValue: !0
    }),
    flat_fee: import_pieces_framework16.Property.Checkbox({
      displayName: "Flat Fee",
      description: "Is this time entry a flat fee?",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      activity_name: context.propsValue.activity_name,
      case: { id: parseInt(context.propsValue.case) },
      staff: { id: parseInt(context.propsValue.staff) },
      entry_date: new Date(context.propsValue.entry_date).toISOString().split("T")[0],
      rate: context.propsValue.rate,
      hours: context.propsValue.hours
    };
    context.propsValue.description && (requestBody.description = context.propsValue.description), context.propsValue.billable !== void 0 && (requestBody.billable = context.propsValue.billable), context.propsValue.flat_fee !== void 0 && (requestBody.flat_fee = context.propsValue.flat_fee);
    try {
      let response = await api.post("/time_entries", requestBody);
      return response.success ? {
        success: !0,
        time_entry: response.data,
        message: `Time entry "${context.propsValue.activity_name}" created successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create time entry",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/create-call.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var createCall = (0, import_pieces_framework17.createAction)({
  auth: mycaseAuth,
  name: "create_call",
  displayName: "Create Call",
  description: "Creates a new call in the call log in MyCase",
  audience: "both",
  aiMetadata: { description: "Log a phone call in MyCase, recording when it happened, the caller phone number, a message, and the staff member it is for. Identify the caller by name (unknown), an existing client, or an existing lead via the caller type. Use to record inbound/outbound calls; not idempotent, so repeated calls create duplicate log entries. To review logged calls, use Find Call.", idempotent: !1 },
  props: {
    called_at: import_pieces_framework17.Property.DateTime({
      displayName: "Called At",
      description: "When the call happened",
      required: !0
    }),
    caller_phone_number: import_pieces_framework17.Property.ShortText({
      displayName: "Caller Phone Number",
      description: "The caller's phone number",
      required: !0
    }),
    call_for: import_pieces_framework17.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Call For",
      description: "The staff member this call is associated with",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((staff) => ({
            label: `${staff.first_name} ${staff.last_name}`,
            value: staff.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    }),
    message: import_pieces_framework17.Property.LongText({
      displayName: "Message",
      description: "A description of the call",
      required: !0
    }),
    caller_type: import_pieces_framework17.Property.StaticDropdown({
      displayName: "Caller Type",
      description: "Choose how to identify the caller",
      required: !0,
      options: {
        options: [
          { label: "Unknown (provide name)", value: "unknown" },
          { label: "Existing Client", value: "client" },
          { label: "Existing Lead", value: "lead" }
        ]
      },
      defaultValue: "unknown"
    }),
    caller_name: import_pieces_framework17.Property.ShortText({
      displayName: "Caller Name",
      description: "The caller's name",
      required: !1
    }),
    client: import_pieces_framework17.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Client",
      description: "The existing client",
      required: !1,
      refreshers: ["caller_type"],
      options: async ({ auth, caller_type }) => {
        if (!auth || caller_type !== "client")
          return {
            disabled: !0,
            options: [],
            placeholder: 'Select "Existing Client" above to choose a client'
          };
        let response = await createMyCaseApi(auth).get("/clients", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((client) => ({
            label: `${client.first_name} ${client.last_name}${client.email ? ` (${client.email})` : ""}`,
            value: client.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load clients"
        };
      }
    }),
    lead: import_pieces_framework17.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Lead",
      description: "The existing lead",
      required: !1,
      refreshers: ["caller_type"],
      options: async ({ auth, caller_type }) => {
        if (!auth || caller_type !== "lead")
          return {
            disabled: !0,
            options: [],
            placeholder: 'Select "Existing Lead" above to choose a lead'
          };
        let response = await createMyCaseApi(auth).get("/leads", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((lead) => ({
            label: `${lead.first_name} ${lead.last_name}${lead.email ? ` (${lead.email})` : ""}`,
            value: lead.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load leads"
        };
      }
    }),
    call_type: import_pieces_framework17.Property.StaticDropdown({
      displayName: "Call Type",
      description: "Type of call",
      required: !1,
      options: {
        options: [
          { label: "Incoming", value: "incoming" },
          { label: "Outgoing", value: "outgoing" }
        ]
      },
      defaultValue: "incoming"
    }),
    resolved: import_pieces_framework17.Property.Checkbox({
      displayName: "Resolved",
      description: "Should this call show as resolved?",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth);
    if (context.propsValue.caller_type === "unknown" && !context.propsValue.caller_name)
      return {
        success: !1,
        error: 'Caller name is required when caller type is "Unknown"'
      };
    if (context.propsValue.caller_type === "client" && !context.propsValue.client)
      return {
        success: !1,
        error: 'Client selection is required when caller type is "Existing Client"'
      };
    if (context.propsValue.caller_type === "lead" && !context.propsValue.lead)
      return {
        success: !1,
        error: 'Lead selection is required when caller type is "Existing Lead"'
      };
    let requestBody = {
      called_at: new Date(context.propsValue.called_at).toISOString(),
      caller_phone_number: context.propsValue.caller_phone_number,
      call_for: { id: parseInt(context.propsValue.call_for) },
      message: context.propsValue.message
    };
    context.propsValue.caller_type === "unknown" ? requestBody.caller_name = context.propsValue.caller_name : context.propsValue.caller_type === "client" ? requestBody.client = { id: parseInt(context.propsValue.client) } : context.propsValue.caller_type === "lead" && (requestBody.lead = { id: parseInt(context.propsValue.lead) }), context.propsValue.call_type && (requestBody.call_type = context.propsValue.call_type), context.propsValue.resolved !== void 0 && (requestBody.resolved = context.propsValue.resolved);
    try {
      let response = await api.post("/calls", requestBody);
      return response.success ? {
        success: !0,
        call: response.data,
        message: "Call created successfully in call log"
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to create call",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/update-case.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var updateCase = (0, import_pieces_framework18.createAction)({
  auth: mycaseAuth,
  name: "update_case",
  displayName: "Update Case",
  description: "Updates an existing case in MyCase",
  audience: "both",
  aiMetadata: { description: "Overwrites an existing case (selected by ID) with the supplied name, stage, practice area, parties, staff, and billing fields. Use to modify a known case rather than create one. The update replaces the named fields wholesale, so pass complete values; lawyer roles are derived from which selected staff member matches the chosen lead/originating lawyer. Re-running with the same input lands the case in the same state.", idempotent: !0 },
  props: {
    case_id: import_pieces_framework18.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Case",
      description: "Select the case to update",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""} - ID: ${caseItem.id}`,
            value: caseItem.id
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    }),
    name: import_pieces_framework18.Property.ShortText({
      displayName: "Case Name",
      description: "The name of the case (must be unique)",
      required: !0
    }),
    case_number: import_pieces_framework18.Property.ShortText({
      displayName: "Case Number",
      description: "An identifier for this case",
      required: !1
    }),
    opened_date: import_pieces_framework18.Property.DateTime({
      displayName: "Opened Date",
      description: "The date the case was created/opened",
      required: !1
    }),
    case_stage: import_pieces_framework18.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Case Stage",
      description: "The stage the case is currently in",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/case_stages", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((stage) => ({
            label: stage.name,
            value: stage.name
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load case stages"
        };
      }
    }),
    practice_area: import_pieces_framework18.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Practice Area",
      description: "The practice area for this case",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/practice_areas", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((area) => ({
            label: area.name,
            value: area.name
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load practice areas"
        };
      }
    }),
    description: import_pieces_framework18.Property.LongText({
      displayName: "Description",
      description: "A description for the case",
      required: !1
    }),
    sol_date: import_pieces_framework18.Property.DateTime({
      displayName: "Statute of Limitations Date",
      description: "The statute of limitations date",
      required: !1
    }),
    status: import_pieces_framework18.Property.StaticDropdown({
      displayName: "Status",
      description: "The status of the case",
      required: !1,
      options: {
        options: [
          { label: "Open", value: "open" },
          { label: "Closed", value: "closed" }
        ]
      }
    }),
    outstanding_balance: import_pieces_framework18.Property.Number({
      displayName: "Outstanding Balance",
      description: "The outstanding balance of this case",
      required: !1
    }),
    billing_contact: import_pieces_framework18.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Billing Contact",
      description: "The client or company to assign as the billing contact",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let api = createMyCaseApi(auth), clientsResponse = await api.get("/clients", { page_size: "50" }), clientOptions = clientsResponse.success && Array.isArray(clientsResponse.data) ? clientsResponse.data.map((client) => ({
          label: `Client: ${client.first_name} ${client.last_name}${client.email ? ` (${client.email})` : ""}`,
          value: `client_${client.id}`
        })) : [], companiesResponse = await api.get("/companies", { page_size: "50" }), companyOptions = companiesResponse.success && Array.isArray(companiesResponse.data) ? companiesResponse.data.map((company) => ({
          label: `Company: ${company.name}`,
          value: `company_${company.id}`
        })) : [];
        return {
          disabled: !1,
          options: [...clientOptions, ...companyOptions]
        };
      }
    }),
    clients: import_pieces_framework18.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Clients",
      description: "Clients to associate with the case",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/clients", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((client) => ({
            label: `${client.first_name} ${client.last_name}${client.email ? ` (${client.email})` : ""}`,
            value: client.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load clients"
        };
      }
    }),
    companies: import_pieces_framework18.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Companies",
      description: "Companies to associate with the case",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/companies", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((company) => ({
            label: company.name,
            value: company.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load companies"
        };
      }
    }),
    staff: import_pieces_framework18.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Staff",
      description: "Staff members to associate with the case",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((staff) => ({
            label: `${staff.first_name} ${staff.last_name}`,
            value: staff.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    }),
    lead_lawyer: import_pieces_framework18.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Lead Lawyer",
      description: "Select the lead lawyer for this case",
      required: !1,
      refreshers: ["staff"],
      options: async ({ auth, staff }) => {
        if (!auth || !staff || !Array.isArray(staff) || staff.length === 0)
          return {
            disabled: !0,
            options: [],
            placeholder: "Select staff members first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.filter(
            (s) => staff.includes(s.id.toString())
          ).map((s) => ({
            label: `${s.first_name} ${s.last_name}`,
            value: s.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    }),
    originating_lawyer: import_pieces_framework18.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Originating Lawyer",
      description: "Select the originating lawyer for this case",
      required: !1,
      refreshers: ["staff"],
      options: async ({ auth, staff }) => {
        if (!auth || !staff || !Array.isArray(staff) || staff.length === 0)
          return {
            disabled: !0,
            options: [],
            placeholder: "Select staff members first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.filter(
            (s) => staff.includes(s.id.toString())
          ).map((s) => ({
            label: `${s.first_name} ${s.last_name}`,
            value: s.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      name: context.propsValue.name
    };
    if (context.propsValue.case_number && (requestBody.case_number = context.propsValue.case_number), context.propsValue.opened_date) {
      let date = new Date(context.propsValue.opened_date);
      requestBody.opened_date = date.toISOString().split("T")[0];
    }
    if (context.propsValue.case_stage && (requestBody.case_stage = context.propsValue.case_stage), context.propsValue.practice_area && (requestBody.practice_area = context.propsValue.practice_area), context.propsValue.description && (requestBody.description = context.propsValue.description), context.propsValue.sol_date) {
      let date = new Date(context.propsValue.sol_date);
      requestBody.sol_date = date.toISOString().split("T")[0];
    }
    if (context.propsValue.status && (requestBody.status = context.propsValue.status), context.propsValue.outstanding_balance !== void 0 && (requestBody.outstanding_balance = context.propsValue.outstanding_balance), context.propsValue.billing_contact) {
      let [type, idStr] = context.propsValue.billing_contact.split("_"), id = parseInt(idStr);
      isNaN(id) || (requestBody.billing_contact = { id });
    }
    if (context.propsValue.clients && Array.isArray(context.propsValue.clients) && (requestBody.clients = context.propsValue.clients.map((id) => ({ id: parseInt(id) }))), context.propsValue.companies && Array.isArray(context.propsValue.companies) && (requestBody.companies = context.propsValue.companies.map((id) => ({ id: parseInt(id) }))), context.propsValue.staff && Array.isArray(context.propsValue.staff)) {
      let leadLawyerId = context.propsValue.lead_lawyer ? parseInt(context.propsValue.lead_lawyer) : null, originatingLawyerId = context.propsValue.originating_lawyer ? parseInt(context.propsValue.originating_lawyer) : null;
      requestBody.staff = context.propsValue.staff.map((staffId) => ({
        id: parseInt(staffId),
        lead_lawyer: leadLawyerId === parseInt(staffId),
        originating_lawyer: originatingLawyerId === parseInt(staffId)
      }));
    }
    try {
      let response = await api.put(`/cases/${context.propsValue.case_id}`, requestBody);
      return response.success ? {
        success: !0,
        message: `Case ${context.propsValue.case_id} updated successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to update case",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/update-company.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var updateCompany = (0, import_pieces_framework19.createAction)({
  auth: mycaseAuth,
  name: "update_company",
  displayName: "Update Company",
  description: "Updates an existing company in MyCase",
  audience: "both",
  aiMetadata: { description: "Update an existing MyCase company identified by its company ID, replacing contact details, address, and linked cases/clients with the supplied values. Use to modify a company you have already resolved (e.g. via Find Case/clients lookups); requires a valid company_id. Idempotent for a given set of inputs. To add a brand-new company, use Create Company.", idempotent: !0 },
  props: {
    company_id: import_pieces_framework19.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Company",
      description: "Select the company to update",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/companies", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((company) => ({
            label: `${company.name}${company.email ? ` (${company.email})` : ""} - ID: ${company.id}`,
            value: company.id
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load companies"
        };
      }
    }),
    name: import_pieces_framework19.Property.ShortText({
      displayName: "Company Name",
      description: "The name of the company",
      required: !0
    }),
    email: import_pieces_framework19.Property.ShortText({
      displayName: "Email",
      description: "Company email address",
      required: !1
    }),
    website: import_pieces_framework19.Property.ShortText({
      displayName: "Website",
      description: "Company website URL",
      required: !1
    }),
    main_phone_number: import_pieces_framework19.Property.ShortText({
      displayName: "Main Phone Number",
      description: "Main phone number for the company",
      required: !1
    }),
    fax_phone_number: import_pieces_framework19.Property.ShortText({
      displayName: "Fax Phone Number",
      description: "Fax phone number for the company",
      required: !1
    }),
    address1: import_pieces_framework19.Property.ShortText({
      displayName: "Address Line 1",
      description: "Street address line 1",
      required: !1
    }),
    address2: import_pieces_framework19.Property.ShortText({
      displayName: "Address Line 2",
      description: "Street address line 2",
      required: !1
    }),
    city: import_pieces_framework19.Property.ShortText({
      displayName: "City",
      description: "City",
      required: !1
    }),
    state: import_pieces_framework19.Property.ShortText({
      displayName: "State",
      description: "State",
      required: !1
    }),
    zip_code: import_pieces_framework19.Property.ShortText({
      displayName: "ZIP Code",
      description: "ZIP/Postal code",
      required: !1
    }),
    country: import_pieces_framework19.Property.ShortText({
      displayName: "Country",
      description: "Country",
      required: !1
    }),
    notes: import_pieces_framework19.Property.LongText({
      displayName: "Notes",
      description: "Additional information about the company (visible to firm members only)",
      required: !1
    }),
    cases: import_pieces_framework19.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Cases",
      description: "Cases to associate with the company",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""}`,
            value: caseItem.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    }),
    clients: import_pieces_framework19.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Clients",
      description: "Clients to associate with the company",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/clients", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((client) => ({
            label: `${client.first_name} ${client.last_name}${client.email ? ` (${client.email})` : ""}`,
            value: client.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load clients"
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      name: context.propsValue.name
    };
    context.propsValue.email && (requestBody.email = context.propsValue.email), context.propsValue.website && (requestBody.website = context.propsValue.website), context.propsValue.main_phone_number && (requestBody.main_phone_number = context.propsValue.main_phone_number), context.propsValue.fax_phone_number && (requestBody.fax_phone_number = context.propsValue.fax_phone_number), context.propsValue.notes && (requestBody.notes = context.propsValue.notes), (context.propsValue.address1 || context.propsValue.city || context.propsValue.state || context.propsValue.zip_code || context.propsValue.country) && (requestBody.address = {
      address1: context.propsValue.address1 || "",
      address2: context.propsValue.address2 || "",
      city: context.propsValue.city || "",
      state: context.propsValue.state || "",
      zip_code: context.propsValue.zip_code || "",
      country: context.propsValue.country || ""
    }), context.propsValue.cases && Array.isArray(context.propsValue.cases) && (requestBody.cases = context.propsValue.cases.map((id) => ({ id: parseInt(id) }))), context.propsValue.clients && Array.isArray(context.propsValue.clients) && (requestBody.clients = context.propsValue.clients.map((id) => ({ id: parseInt(id) })));
    try {
      let response = await api.put(
        `/companies/${context.propsValue.company_id}`,
        requestBody
      );
      return response.success ? {
        success: !0,
        message: `Company ${context.propsValue.company_id} updated successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to update company",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/update-person.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var updatePerson = (0, import_pieces_framework20.createAction)({
  auth: mycaseAuth,
  name: "update_person",
  displayName: "Update Person (Client)",
  description: "Updates an existing individual client (person) in MyCase",
  audience: "both",
  aiMetadata: { description: "Overwrites an existing client (selected by ID) with the supplied name, contact, address, people-group, and case-association fields. Use to modify a known client rather than create one; first and last name are always sent, so provide complete values. Re-running with the same input lands the client in the same state.", idempotent: !0 },
  props: {
    client_id: import_pieces_framework20.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Client",
      description: "Select the client to update",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/clients", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((client) => ({
            label: `${client.first_name} ${client.last_name}${client.email ? ` (${client.email})` : ""} - ID: ${client.id}`,
            value: client.id
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load clients"
        };
      }
    }),
    first_name: import_pieces_framework20.Property.ShortText({
      displayName: "First Name",
      description: "The first name of the client",
      required: !0
    }),
    last_name: import_pieces_framework20.Property.ShortText({
      displayName: "Last Name",
      description: "The last name of the client",
      required: !0
    }),
    email: import_pieces_framework20.Property.ShortText({
      displayName: "Email",
      description: "The email of the client",
      required: !1
    }),
    middle_name: import_pieces_framework20.Property.ShortText({
      displayName: "Middle Name",
      description: "The middle name of the client",
      required: !1
    }),
    cell_phone_number: import_pieces_framework20.Property.ShortText({
      displayName: "Cell Phone Number",
      description: "Cell phone number",
      required: !1
    }),
    work_phone_number: import_pieces_framework20.Property.ShortText({
      displayName: "Work Phone Number",
      description: "Work phone number",
      required: !1
    }),
    home_phone_number: import_pieces_framework20.Property.ShortText({
      displayName: "Home Phone Number",
      description: "Home phone number",
      required: !1
    }),
    fax_phone_number: import_pieces_framework20.Property.ShortText({
      displayName: "Fax Phone Number",
      description: "Fax phone number",
      required: !1
    }),
    birthdate: import_pieces_framework20.Property.DateTime({
      displayName: "Birthdate",
      description: "Date of birth",
      required: !1
    }),
    notes: import_pieces_framework20.Property.LongText({
      displayName: "Notes",
      description: "Additional information (visible to firm members only)",
      required: !1
    }),
    address1: import_pieces_framework20.Property.ShortText({
      displayName: "Address Line 1",
      description: "Street address line 1",
      required: !1
    }),
    address2: import_pieces_framework20.Property.ShortText({
      displayName: "Address Line 2",
      description: "Street address line 2",
      required: !1
    }),
    city: import_pieces_framework20.Property.ShortText({
      displayName: "City",
      description: "City",
      required: !1
    }),
    state: import_pieces_framework20.Property.ShortText({
      displayName: "State",
      description: "State",
      required: !1
    }),
    zip_code: import_pieces_framework20.Property.ShortText({
      displayName: "ZIP Code",
      description: "ZIP/Postal code",
      required: !1
    }),
    country: import_pieces_framework20.Property.ShortText({
      displayName: "Country",
      description: "Country",
      required: !1
    }),
    people_group: import_pieces_framework20.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "People Group",
      description: "The people group to associate with this client",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/people_groups", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((group) => ({
            label: group.name,
            value: group.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load people groups"
        };
      }
    }),
    cases: import_pieces_framework20.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Cases",
      description: "Cases to associate with this client",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""}`,
            value: caseItem.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), requestBody = {
      first_name: context.propsValue.first_name,
      last_name: context.propsValue.last_name
    };
    if (context.propsValue.email && (requestBody.email = context.propsValue.email), context.propsValue.middle_name && (requestBody.middle_name = context.propsValue.middle_name), context.propsValue.cell_phone_number && (requestBody.cell_phone_number = context.propsValue.cell_phone_number), context.propsValue.work_phone_number && (requestBody.work_phone_number = context.propsValue.work_phone_number), context.propsValue.home_phone_number && (requestBody.home_phone_number = context.propsValue.home_phone_number), context.propsValue.fax_phone_number && (requestBody.fax_phone_number = context.propsValue.fax_phone_number), context.propsValue.birthdate) {
      let date = new Date(context.propsValue.birthdate);
      requestBody.birthdate = date.toISOString().split("T")[0];
    }
    context.propsValue.notes && (requestBody.notes = context.propsValue.notes), (context.propsValue.address1 || context.propsValue.city || context.propsValue.state || context.propsValue.zip_code || context.propsValue.country) && (requestBody.address = {
      address1: context.propsValue.address1 || "",
      address2: context.propsValue.address2 || "",
      city: context.propsValue.city || "",
      state: context.propsValue.state || "",
      zip_code: context.propsValue.zip_code || "",
      country: context.propsValue.country || ""
    }), context.propsValue.people_group && (requestBody.people_group = {
      id: parseInt(context.propsValue.people_group)
    }), context.propsValue.cases && Array.isArray(context.propsValue.cases) && (requestBody.cases = context.propsValue.cases.map((id) => ({ id: parseInt(id) })));
    try {
      let response = await api.put(`/clients/${context.propsValue.client_id}`, requestBody);
      return response.success ? {
        success: !0,
        message: `Client ${context.propsValue.client_id} updated successfully`
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to update client",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/make-request.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var makeRequest = (0, import_pieces_framework21.createAction)({
  auth: mycaseAuth,
  name: "make_request",
  displayName: "API Request",
  description: "Makes a raw HTTP request to the MyCase API",
  audience: "both",
  aiMetadata: { description: "Escape hatch that sends an arbitrary authenticated HTTP request (GET/POST/PUT/PATCH/DELETE) to any MyCase API path with custom query params, body, and headers. Use only for endpoints not covered by a dedicated action; prefer the specific actions when one fits. Idempotency depends entirely on the method and path you supply, so treat write methods as unsafe to retry.", idempotent: !1 },
  props: {
    method: import_pieces_framework21.Property.StaticDropdown({
      displayName: "Method",
      description: "HTTP method to use",
      required: !0,
      options: {
        options: [
          { label: "GET", value: "GET" },
          { label: "POST", value: "POST" },
          { label: "PUT", value: "PUT" },
          { label: "PATCH", value: "PATCH" },
          { label: "DELETE", value: "DELETE" }
        ]
      },
      defaultValue: "GET"
    }),
    path: import_pieces_framework21.Property.ShortText({
      displayName: "Path",
      description: "API endpoint path (e.g., /cases or /clients/123)",
      required: !0
    }),
    query_params: import_pieces_framework21.Property.Object({
      displayName: "Query Parameters",
      description: "Query parameters as JSON object",
      required: !1
    }),
    body: import_pieces_framework21.Property.Json({
      displayName: "Request Body",
      description: "Request body as JSON (for POST, PUT, PATCH)",
      required: !1
    }),
    headers: import_pieces_framework21.Property.Object({
      displayName: "Additional Headers",
      description: "Additional headers as JSON object",
      required: !1
    })
  },
  async run(context) {
    var _a, _b;
    let baseUrl = "https://external-integrations.mycase.com/v1", path = context.propsValue.path.startsWith("/") ? context.propsValue.path : `/${context.propsValue.path}`, url = `${baseUrl}${path}`, headers = __spreadValues({
      "Content-Type": "application/json",
      Authorization: `Bearer ${context.auth.access_token}`
    }, context.propsValue.headers || {});
    try {
      let response = await import_pieces_common2.httpClient.sendRequest({
        method: context.propsValue.method,
        url,
        headers,
        queryParams: context.propsValue.query_params,
        body: context.propsValue.body
      });
      return {
        success: !0,
        status: response.status,
        headers: response.headers,
        body: response.body
      };
    } catch (error) {
      return {
        success: !1,
        status: (_a = error.response) == null ? void 0 : _a.status,
        error: error.message || "Request failed",
        details: ((_b = error.response) == null ? void 0 : _b.body) || error
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-case.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var findCase = (0, import_pieces_framework22.createAction)({
  auth: mycaseAuth,
  name: "find_case",
  displayName: "Find Case",
  description: "Searches for cases with optional filters",
  audience: "both",
  aiMetadata: { description: "List or search MyCase legal cases, optionally filtering by open/closed status and last-updated date, and selecting which client and custom-field columns to include. Use to look up cases or resolve a case to its ID before linking it to other records. Read-only and idempotent.", idempotent: !0 },
  props: {
    status: import_pieces_framework22.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter by case status",
      required: !1,
      options: {
        options: [
          { label: "Open", value: "open" },
          { label: "Closed", value: "closed" }
        ]
      }
    }),
    updated_after: import_pieces_framework22.Property.DateTime({
      displayName: "Updated After",
      description: "Filter cases updated after this date and time",
      required: !1
    }),
    page_size: import_pieces_framework22.Property.Number({
      displayName: "Page Size",
      description: "Number of results per page (1-1000)",
      required: !1,
      defaultValue: 25
    }),
    client_fields: import_pieces_framework22.Property.ShortText({
      displayName: "Client Fields",
      description: "Comma-separated list of client fields to include (e.g., id,first_name,last_name)",
      required: !1
    }),
    custom_field_fields: import_pieces_framework22.Property.ShortText({
      displayName: "Custom Field Fields",
      description: "Comma-separated list of custom field fields to include (e.g., id,field_type)",
      required: !1
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), queryParams = {};
    context.propsValue.status && (queryParams["filter[status]"] = context.propsValue.status), context.propsValue.updated_after && (queryParams["filter[updated_after]"] = new Date(context.propsValue.updated_after).toISOString()), context.propsValue.page_size && (queryParams.page_size = context.propsValue.page_size.toString()), context.propsValue.client_fields && (queryParams["field[client]"] = context.propsValue.client_fields), context.propsValue.custom_field_fields && (queryParams["field[custom_field]"] = context.propsValue.custom_field_fields);
    try {
      let response = await api.get("/cases", queryParams);
      return response.success ? {
        success: !0,
        cases: response.data,
        count: Array.isArray(response.data) ? response.data.length : 0
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find cases",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-call.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var findCall = (0, import_pieces_framework23.createAction)({
  auth: mycaseAuth,
  name: "find_call",
  displayName: "Find Call",
  description: "Searches for calls with optional filters",
  audience: "both",
  aiMetadata: { description: "List or search MyCase call-log entries, optionally filtering by last-updated date and limiting page size. Use to review logged phone calls or look one up. Read-only and idempotent. To log a new call, use Create Call.", idempotent: !0 },
  props: {
    updated_after: import_pieces_framework23.Property.DateTime({
      displayName: "Updated After",
      description: "Filter calls updated after this date and time",
      required: !1
    }),
    page_size: import_pieces_framework23.Property.Number({
      displayName: "Page Size",
      description: "Number of results per page (1-1000)",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), queryParams = {};
    context.propsValue.updated_after && (queryParams["filter[updated_after]"] = new Date(context.propsValue.updated_after).toISOString()), context.propsValue.page_size && (queryParams.page_size = context.propsValue.page_size.toString());
    try {
      let response = await api.get("/calls", queryParams);
      return response.success ? {
        success: !0,
        calls: response.data,
        count: Array.isArray(response.data) ? response.data.length : 0
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find calls",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-case-stage.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework");
var findCaseStage = (0, import_pieces_framework24.createAction)({
  auth: mycaseAuth,
  name: "find_case_stage",
  displayName: "Find Case Stage",
  description: "Searches for case stages with optional filters",
  audience: "both",
  aiMetadata: { description: "List or search MyCase case stages, optionally filtering by last-updated date and limiting page size. Use to resolve a stage name to its ID or browse configured stages. Read-only and idempotent. To create a stage when none matches, use Find or Create Case Stage.", idempotent: !0 },
  props: {
    updated_after: import_pieces_framework24.Property.DateTime({
      displayName: "Updated After",
      description: "Filter case stages updated after this date and time",
      required: !1
    }),
    page_size: import_pieces_framework24.Property.Number({
      displayName: "Page Size",
      description: "Number of results per page (1-1000)",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), queryParams = {};
    context.propsValue.updated_after && (queryParams["filter[updated_after]"] = new Date(context.propsValue.updated_after).toISOString()), context.propsValue.page_size && (queryParams.page_size = context.propsValue.page_size.toString());
    try {
      let response = await api.get("/case_stages", queryParams);
      return response.success ? {
        success: !0,
        case_stages: response.data,
        count: Array.isArray(response.data) ? response.data.length : 0
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find case stages",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-company.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework");
var findCompany = (0, import_pieces_framework25.createAction)({
  auth: mycaseAuth,
  name: "find_company",
  displayName: "Find Company",
  description: "Searches for companies with optional filters",
  audience: "both",
  aiMetadata: { description: "Read-only search of MyCase companies by name, email, phone, fax, or an updated-after timestamp. Use to resolve a company ID before associating it with a case or a billing contact. Returns matches without modifying anything; if you instead want to guarantee a company exists, use Find or Create Company.", idempotent: !0 },
  props: {
    name: import_pieces_framework25.Property.ShortText({
      displayName: "Name",
      description: "Filter by company name",
      required: !1
    }),
    email: import_pieces_framework25.Property.ShortText({
      displayName: "Email",
      description: "Filter by email address",
      required: !1
    }),
    main_phone_number: import_pieces_framework25.Property.ShortText({
      displayName: "Main Phone Number",
      description: "Filter by main phone number",
      required: !1
    }),
    fax_phone_number: import_pieces_framework25.Property.ShortText({
      displayName: "Fax Phone Number",
      description: "Filter by fax phone number",
      required: !1
    }),
    updated_after: import_pieces_framework25.Property.DateTime({
      displayName: "Updated After",
      description: "Filter companies updated after this date and time",
      required: !1
    }),
    page_size: import_pieces_framework25.Property.Number({
      displayName: "Page Size",
      description: "Number of results per page (1-1000)",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), queryParams = {};
    context.propsValue.name && (queryParams["filter[name]"] = context.propsValue.name), context.propsValue.email && (queryParams["filter[email]"] = context.propsValue.email), context.propsValue.main_phone_number && (queryParams["filter[main_phone_number]"] = context.propsValue.main_phone_number), context.propsValue.fax_phone_number && (queryParams["filter[fax_phone_number]"] = context.propsValue.fax_phone_number), context.propsValue.updated_after && (queryParams["filter[updated_after]"] = new Date(context.propsValue.updated_after).toISOString()), context.propsValue.page_size && (queryParams.page_size = context.propsValue.page_size.toString());
    try {
      let response = await api.get("/companies", queryParams);
      return response.success ? {
        success: !0,
        companies: response.data,
        count: Array.isArray(response.data) ? response.data.length : 0
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find companies",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-location.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework");
var findLocation = (0, import_pieces_framework26.createAction)({
  auth: mycaseAuth,
  name: "find_location",
  displayName: "Find Location",
  description: "Searches for locations with optional filters",
  audience: "both",
  aiMetadata: { description: "List or search MyCase office/event locations, optionally filtering by last-updated date and limiting page size. Use to resolve a location name to its ID before referencing it elsewhere (e.g. when creating an event). Read-only and idempotent.", idempotent: !0 },
  props: {
    updated_after: import_pieces_framework26.Property.DateTime({
      displayName: "Updated After",
      description: "Filter locations updated after this date and time",
      required: !1
    }),
    page_size: import_pieces_framework26.Property.Number({
      displayName: "Page Size",
      description: "Number of results per page (1-1000)",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), queryParams = {};
    context.propsValue.updated_after && (queryParams["filter[updated_after]"] = context.propsValue.updated_after), context.propsValue.page_size && (queryParams.page_size = context.propsValue.page_size.toString());
    try {
      let response = await api.get("/locations", queryParams);
      return response.success ? {
        success: !0,
        locations: response.data,
        count: Array.isArray(response.data) ? response.data.length : 0
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find locations",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-people-group.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework");
var findPeopleGroup = (0, import_pieces_framework27.createAction)({
  auth: mycaseAuth,
  name: "find_people_group",
  displayName: "Find People Group",
  description: "Searches for people groups with optional filters",
  audience: "both",
  aiMetadata: { description: "List or search MyCase people groups, optionally filtering by last-updated date and limiting page size. Use to browse contact groupings or resolve a group to its ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    updated_after: import_pieces_framework27.Property.DateTime({
      displayName: "Updated After",
      description: "Filter people groups updated after this date and time",
      required: !1
    }),
    page_size: import_pieces_framework27.Property.Number({
      displayName: "Page Size",
      description: "Number of results per page (1-1000)",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), queryParams = {};
    context.propsValue.updated_after && (queryParams["filter[updated_after]"] = new Date(context.propsValue.updated_after).toISOString()), context.propsValue.page_size && (queryParams.page_size = context.propsValue.page_size.toString());
    try {
      let response = await api.get("/people_groups", queryParams);
      return response.success ? {
        success: !0,
        people_groups: response.data,
        count: Array.isArray(response.data) ? response.data.length : 0
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find people groups",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-person.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework");
var findPerson = (0, import_pieces_framework28.createAction)({
  auth: mycaseAuth,
  name: "find_person",
  displayName: "Find Person (Client)",
  description: "Searches for clients (people) with optional filters",
  audience: "both",
  aiMetadata: { description: "List or search MyCase clients (individual people), filtering by name, email, phone numbers, or last-updated date. Use to find a person or resolve them to a client ID before linking elsewhere. Read-only and idempotent. Note this returns clients only, not leads or staff.", idempotent: !0 },
  props: {
    first_name: import_pieces_framework28.Property.ShortText({
      displayName: "First Name",
      description: "Filter by first name",
      required: !1
    }),
    last_name: import_pieces_framework28.Property.ShortText({
      displayName: "Last Name",
      description: "Filter by last name",
      required: !1
    }),
    email: import_pieces_framework28.Property.ShortText({
      displayName: "Email",
      description: "Filter by email address",
      required: !1
    }),
    cell_phone_number: import_pieces_framework28.Property.ShortText({
      displayName: "Cell Phone Number",
      description: "Filter by cell phone number",
      required: !1
    }),
    work_phone_number: import_pieces_framework28.Property.ShortText({
      displayName: "Work Phone Number",
      description: "Filter by work phone number",
      required: !1
    }),
    home_phone_number: import_pieces_framework28.Property.ShortText({
      displayName: "Home Phone Number",
      description: "Filter by home phone number",
      required: !1
    }),
    updated_after: import_pieces_framework28.Property.DateTime({
      displayName: "Updated After",
      description: "Filter clients updated after this date and time",
      required: !1
    }),
    page_size: import_pieces_framework28.Property.Number({
      displayName: "Page Size",
      description: "Number of results per page (1-1000)",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), queryParams = {};
    context.propsValue.first_name && (queryParams["filter[first_name]"] = context.propsValue.first_name), context.propsValue.last_name && (queryParams["filter[last_name]"] = context.propsValue.last_name), context.propsValue.email && (queryParams["filter[email]"] = context.propsValue.email), context.propsValue.cell_phone_number && (queryParams["filter[cell_phone_number]"] = context.propsValue.cell_phone_number), context.propsValue.work_phone_number && (queryParams["filter[work_phone_number]"] = context.propsValue.work_phone_number), context.propsValue.home_phone_number && (queryParams["filter[home_phone_number]"] = context.propsValue.home_phone_number), context.propsValue.updated_after && (queryParams["filter[updated_after]"] = new Date(context.propsValue.updated_after).toISOString()), context.propsValue.page_size && (queryParams.page_size = context.propsValue.page_size.toString());
    try {
      let response = await api.get("/clients", queryParams);
      return response.success ? {
        success: !0,
        clients: response.data,
        count: Array.isArray(response.data) ? response.data.length : 0
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find clients",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-practice-area.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework");
var findPracticeArea = (0, import_pieces_framework29.createAction)({
  auth: mycaseAuth,
  name: "find_practice_area",
  displayName: "Find Practice Area",
  description: "Searches for practice areas with optional filters",
  audience: "both",
  aiMetadata: { description: "List or search MyCase practice areas, optionally filtering by last-updated date and limiting page size. Use to resolve a practice-area name to its ID or browse available practice areas. Read-only and idempotent. To add a missing practice area, use Create Practice Area.", idempotent: !0 },
  props: {
    updated_after: import_pieces_framework29.Property.DateTime({
      displayName: "Updated After",
      description: "Filter practice areas updated after this date and time",
      required: !1
    }),
    page_size: import_pieces_framework29.Property.Number({
      displayName: "Page Size",
      description: "Number of results per page (1-1000)",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), queryParams = {};
    context.propsValue.updated_after && (queryParams["filter[updated_after]"] = new Date(context.propsValue.updated_after).toISOString()), context.propsValue.page_size && (queryParams.page_size = context.propsValue.page_size.toString());
    try {
      let response = await api.get("/practice_areas", queryParams);
      return response.success ? {
        success: !0,
        practice_areas: response.data,
        count: Array.isArray(response.data) ? response.data.length : 0
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find practice areas",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-referral-source.ts
var import_pieces_framework30 = require("@activepieces/pieces-framework");
var findReferralSource = (0, import_pieces_framework30.createAction)({
  auth: mycaseAuth,
  name: "find_referral_source",
  displayName: "Find Referral Source",
  description: "Searches for referral sources with optional filters",
  audience: "both",
  aiMetadata: { description: "Read-only list of MyCase referral sources, optionally filtered by an updated-after timestamp and page size. Use to find an existing referral source ID before attaching it to a lead. Pure lookup with no side effects, safe to call repeatedly.", idempotent: !0 },
  props: {
    updated_after: import_pieces_framework30.Property.DateTime({
      displayName: "Updated After",
      description: "Filter referral sources updated after this date and time",
      required: !1
    }),
    page_size: import_pieces_framework30.Property.Number({
      displayName: "Page Size",
      description: "Number of results per page (1-1000)",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), queryParams = {};
    context.propsValue.updated_after && (queryParams["filter[updated_after]"] = new Date(context.propsValue.updated_after).toISOString()), context.propsValue.page_size && (queryParams.page_size = context.propsValue.page_size.toString());
    try {
      let response = await api.get("/referral_sources", queryParams);
      return response.success ? {
        success: !0,
        referral_sources: response.data,
        count: Array.isArray(response.data) ? response.data.length : 0
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find referral sources",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-staff.ts
var import_pieces_framework31 = require("@activepieces/pieces-framework");
var findStaff = (0, import_pieces_framework31.createAction)({
  auth: mycaseAuth,
  name: "find_staff",
  displayName: "Find Staff",
  description: "Searches for staff members with optional filters",
  audience: "both",
  aiMetadata: { description: "Read-only list of MyCase staff (firm users), optionally narrowed by active/inactive status, an updated-after timestamp, and page size. Use to look up staff IDs needed when assigning lawyers or sharing documents. Returns whatever matches the filters and does not modify anything, so it is safe to call repeatedly.", idempotent: !0 },
  props: {
    status: import_pieces_framework31.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter by staff status",
      required: !1,
      options: {
        options: [
          { label: "Active", value: "active" },
          { label: "Inactive", value: "inactive" }
        ]
      }
    }),
    updated_after: import_pieces_framework31.Property.DateTime({
      displayName: "Updated After",
      description: "Filter staff updated after this date and time",
      required: !1
    }),
    page_size: import_pieces_framework31.Property.Number({
      displayName: "Page Size",
      description: "Number of results per page (1-1000)",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), queryParams = {};
    context.propsValue.status && (queryParams["filter[status]"] = context.propsValue.status), context.propsValue.updated_after && (queryParams["filter[updated_after]"] = new Date(context.propsValue.updated_after).toISOString()), context.propsValue.page_size && (queryParams.page_size = context.propsValue.page_size.toString());
    try {
      let response = await api.get("/staff", queryParams);
      return response.success ? {
        success: !0,
        staff: response.data,
        count: Array.isArray(response.data) ? response.data.length : 0
      } : {
        success: !1,
        error: response.error,
        details: response.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find staff",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-or-create-case.ts
var import_pieces_framework32 = require("@activepieces/pieces-framework");
var findOrCreateCase = (0, import_pieces_framework32.createAction)({
  auth: mycaseAuth,
  name: "find_or_create_case",
  displayName: "Find or Create Case",
  description: "Finds a case by name or creates a new one if it does not exist",
  audience: "both",
  aiMetadata: { description: "Returns the MyCase case matching the given name (case-insensitive), creating it with the optional stage, practice area, parties, staff, and other fields only if no match exists. Use to guarantee a case exists without creating duplicates; repeat calls with the same name converge to one record and the extra fields apply only on creation. Use Create Case to always insert or Update Case to modify an existing one.", idempotent: !0 },
  props: {
    name: import_pieces_framework32.Property.ShortText({
      displayName: "Case Name",
      description: "The name of the case to find or create",
      required: !0
    }),
    case_number: import_pieces_framework32.Property.ShortText({
      displayName: "Case Number",
      description: "An identifier for this case (used only when creating)",
      required: !1
    }),
    opened_date: import_pieces_framework32.Property.DateTime({
      displayName: "Opened Date",
      description: "The date the case was created/opened (used only when creating)",
      required: !1
    }),
    case_stage: import_pieces_framework32.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Case Stage",
      description: "The stage the case is currently in (used only when creating)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/case_stages", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((stage) => ({
            label: stage.name,
            value: stage.name
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load case stages"
        };
      }
    }),
    practice_area: import_pieces_framework32.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Practice Area",
      description: "The practice area for this case (used only when creating)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/practice_areas", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((area) => ({
            label: area.name,
            value: area.name
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load practice areas"
        };
      }
    }),
    description: import_pieces_framework32.Property.LongText({
      displayName: "Description",
      description: "A description for the case (used only when creating)",
      required: !1
    }),
    sol_date: import_pieces_framework32.Property.DateTime({
      displayName: "Statute of Limitations Date",
      description: "The statute of limitations date (used only when creating)",
      required: !1
    }),
    status: import_pieces_framework32.Property.StaticDropdown({
      displayName: "Status",
      description: "The status of the case (used only when creating)",
      required: !1,
      options: {
        options: [
          { label: "Open", value: "open" },
          { label: "Closed", value: "closed" }
        ]
      },
      defaultValue: "open"
    }),
    outstanding_balance: import_pieces_framework32.Property.Number({
      displayName: "Outstanding Balance",
      description: "The outstanding balance of this case (used only when creating)",
      required: !1
    }),
    billing_contact: import_pieces_framework32.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Billing Contact",
      description: "The client or company to assign as the billing contact (used only when creating)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let api = createMyCaseApi(auth), clientsResponse = await api.get("/clients", { page_size: "50" }), clientOptions = clientsResponse.success && Array.isArray(clientsResponse.data) ? clientsResponse.data.map((client) => ({
          label: `Client: ${client.first_name} ${client.last_name}${client.email ? ` (${client.email})` : ""}`,
          value: `client_${client.id}`
        })) : [], companiesResponse = await api.get("/companies", { page_size: "50" }), companyOptions = companiesResponse.success && Array.isArray(companiesResponse.data) ? companiesResponse.data.map((company) => ({
          label: `Company: ${company.name}`,
          value: `company_${company.id}`
        })) : [];
        return {
          disabled: !1,
          options: [...clientOptions, ...companyOptions]
        };
      }
    }),
    clients: import_pieces_framework32.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Clients",
      description: "Clients to associate with the case (used only when creating)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/clients", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((client) => ({
            label: `${client.first_name} ${client.last_name}${client.email ? ` (${client.email})` : ""}`,
            value: client.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load clients"
        };
      }
    }),
    companies: import_pieces_framework32.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Companies",
      description: "Companies to associate with the case (used only when creating)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/companies", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((company) => ({
            label: company.name,
            value: company.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load companies"
        };
      }
    }),
    staff: import_pieces_framework32.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Staff",
      description: "Staff members to associate with the case (used only when creating)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((staff) => ({
            label: `${staff.first_name} ${staff.last_name}`,
            value: staff.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    }),
    lead_lawyer: import_pieces_framework32.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Lead Lawyer",
      description: "Select the lead lawyer for this case (used only when creating)",
      required: !1,
      refreshers: ["staff"],
      options: async ({ auth, staff }) => {
        if (!auth || !staff || !Array.isArray(staff) || staff.length === 0)
          return {
            disabled: !0,
            options: [],
            placeholder: "Select staff members first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.filter(
            (s) => staff.includes(s.id.toString())
          ).map((s) => ({
            label: `${s.first_name} ${s.last_name}`,
            value: s.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    }),
    originating_lawyer: import_pieces_framework32.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "Originating Lawyer",
      description: "Select the originating lawyer for this case (used only when creating)",
      required: !1,
      refreshers: ["staff"],
      options: async ({ auth, staff }) => {
        if (!auth || !staff || !Array.isArray(staff) || staff.length === 0)
          return {
            disabled: !0,
            options: [],
            placeholder: "Select staff members first"
          };
        let response = await createMyCaseApi(auth).get("/staff", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.filter(
            (s) => staff.includes(s.id.toString())
          ).map((s) => ({
            label: `${s.first_name} ${s.last_name}`,
            value: s.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load staff"
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), caseName = context.propsValue.name;
    try {
      let findResponse = await api.get("/cases", {
        page_size: "1000"
      });
      if (findResponse.success && Array.isArray(findResponse.data)) {
        let existingCase = findResponse.data.find(
          (c) => c.name && c.name.toLowerCase() === caseName.toLowerCase()
        );
        if (existingCase)
          return {
            success: !0,
            case: existingCase,
            created: !1,
            message: `Case "${caseName}" found`
          };
      }
      let requestBody = {
        name: caseName
      };
      if (context.propsValue.case_number && (requestBody.case_number = context.propsValue.case_number), context.propsValue.opened_date) {
        let date = new Date(context.propsValue.opened_date);
        requestBody.opened_date = date.toISOString().split("T")[0];
      }
      if (context.propsValue.case_stage && (requestBody.case_stage = context.propsValue.case_stage), context.propsValue.practice_area && (requestBody.practice_area = context.propsValue.practice_area), context.propsValue.description && (requestBody.description = context.propsValue.description), context.propsValue.sol_date) {
        let date = new Date(context.propsValue.sol_date);
        requestBody.sol_date = date.toISOString().split("T")[0];
      }
      if (context.propsValue.status && (requestBody.status = context.propsValue.status), context.propsValue.outstanding_balance !== void 0 && (requestBody.outstanding_balance = context.propsValue.outstanding_balance), context.propsValue.billing_contact) {
        let [type, idStr] = context.propsValue.billing_contact.split("_"), id = parseInt(idStr);
        isNaN(id) || (requestBody.billing_contact = { id });
      }
      if (context.propsValue.clients && Array.isArray(context.propsValue.clients) && (requestBody.clients = context.propsValue.clients.map((id) => ({ id: parseInt(id) }))), context.propsValue.companies && Array.isArray(context.propsValue.companies) && (requestBody.companies = context.propsValue.companies.map((id) => ({ id: parseInt(id) }))), context.propsValue.staff && Array.isArray(context.propsValue.staff)) {
        let leadLawyerId = context.propsValue.lead_lawyer ? parseInt(context.propsValue.lead_lawyer) : null, originatingLawyerId = context.propsValue.originating_lawyer ? parseInt(context.propsValue.originating_lawyer) : null;
        requestBody.staff = context.propsValue.staff.map((staffId) => ({
          id: parseInt(staffId),
          lead_lawyer: leadLawyerId === parseInt(staffId),
          originating_lawyer: originatingLawyerId === parseInt(staffId)
        }));
      }
      let createResponse = await api.post("/cases", requestBody);
      return createResponse.success ? {
        success: !0,
        case: createResponse.data,
        created: !0,
        message: `Case "${caseName}" created successfully`
      } : {
        success: !1,
        error: createResponse.error,
        details: createResponse.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find or create case",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-or-create-case-stage.ts
var import_pieces_framework33 = require("@activepieces/pieces-framework");
var findOrCreateCaseStage = (0, import_pieces_framework33.createAction)({
  auth: mycaseAuth,
  name: "find_or_create_case_stage",
  displayName: "Find or Create Case Stage",
  description: "Finds a case stage by name or creates a new one if it does not exist",
  audience: "both",
  aiMetadata: { description: "Resolve a MyCase case stage by exact (case-insensitive) name, creating it if no match exists, and return the stage. Use to guarantee a named stage is present without risking duplicates. Idempotent: repeated calls with the same name return the existing stage rather than creating another. Use Find Case Stage instead for a pure read with no creation.", idempotent: !0 },
  props: {
    name: import_pieces_framework33.Property.ShortText({
      displayName: "Case Stage Name",
      description: "The name of the case stage to find or create",
      required: !0
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), stageName = context.propsValue.name;
    try {
      let findResponse = await api.get("/case_stages", {
        page_size: "1000"
      });
      if (findResponse.success && Array.isArray(findResponse.data)) {
        let existingStage = findResponse.data.find(
          (stage) => stage.name && stage.name.toLowerCase() === stageName.toLowerCase()
        );
        if (existingStage)
          return {
            success: !0,
            case_stage: existingStage,
            created: !1,
            message: `Case stage "${stageName}" found`
          };
      }
      let requestBody = {
        name: stageName
      }, createResponse = await api.post("/case_stages", requestBody);
      return createResponse.success ? {
        success: !0,
        case_stage: createResponse.data,
        created: !0,
        message: `Case stage "${stageName}" created successfully`
      } : {
        success: !1,
        error: createResponse.error,
        details: createResponse.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find or create case stage",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-or-create-company.ts
var import_pieces_framework34 = require("@activepieces/pieces-framework");
var findOrCreateCompany = (0, import_pieces_framework34.createAction)({
  auth: mycaseAuth,
  name: "find_or_create_company",
  displayName: "Find or Create Company",
  description: "Finds a company by name or creates a new one if it does not exist",
  audience: "both",
  aiMetadata: { description: "Returns the MyCase company matching the given name (case-insensitive), creating it with the optional contact, address, and association fields only if no match exists. Use to guarantee a company exists without creating duplicates; repeat calls with the same name converge to one record. The extra fields apply only on creation. Use Find Company for a pure lookup or Create Company to always insert.", idempotent: !0 },
  props: {
    name: import_pieces_framework34.Property.ShortText({
      displayName: "Company Name",
      description: "The name of the company to find or create",
      required: !0
    }),
    email: import_pieces_framework34.Property.ShortText({
      displayName: "Email",
      description: "Company email address (used only when creating)",
      required: !1
    }),
    website: import_pieces_framework34.Property.ShortText({
      displayName: "Website",
      description: "Company website URL (used only when creating)",
      required: !1
    }),
    main_phone_number: import_pieces_framework34.Property.ShortText({
      displayName: "Main Phone Number",
      description: "Main phone number for the company (used only when creating)",
      required: !1
    }),
    fax_phone_number: import_pieces_framework34.Property.ShortText({
      displayName: "Fax Phone Number",
      description: "Fax phone number for the company (used only when creating)",
      required: !1
    }),
    address1: import_pieces_framework34.Property.ShortText({
      displayName: "Address Line 1",
      description: "Street address line 1 (used only when creating)",
      required: !1
    }),
    address2: import_pieces_framework34.Property.ShortText({
      displayName: "Address Line 2",
      description: "Street address line 2 (used only when creating)",
      required: !1
    }),
    city: import_pieces_framework34.Property.ShortText({
      displayName: "City",
      description: "City (used only when creating)",
      required: !1
    }),
    state: import_pieces_framework34.Property.ShortText({
      displayName: "State",
      description: "State (used only when creating)",
      required: !1
    }),
    zip_code: import_pieces_framework34.Property.ShortText({
      displayName: "ZIP Code",
      description: "ZIP/Postal code (used only when creating)",
      required: !1
    }),
    country: import_pieces_framework34.Property.ShortText({
      displayName: "Country",
      description: "Country (used only when creating)",
      required: !1
    }),
    notes: import_pieces_framework34.Property.LongText({
      displayName: "Notes",
      description: "Additional information about the company (used only when creating)",
      required: !1
    }),
    cases: import_pieces_framework34.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Cases",
      description: "Cases to associate with the company (used only when creating)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""}`,
            value: caseItem.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    }),
    clients: import_pieces_framework34.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Clients",
      description: "Clients to associate with the company (used only when creating)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/clients", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((client) => ({
            label: `${client.first_name} ${client.last_name}${client.email ? ` (${client.email})` : ""}`,
            value: client.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load clients"
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), companyName = context.propsValue.name;
    try {
      let findResponse = await api.get("/companies", {
        page_size: "1000"
      });
      if (findResponse.success && Array.isArray(findResponse.data)) {
        let existingCompany = findResponse.data.find(
          (c) => c.name && c.name.toLowerCase() === companyName.toLowerCase()
        );
        if (existingCompany)
          return {
            success: !0,
            company: existingCompany,
            created: !1,
            message: `Company "${companyName}" found`
          };
      }
      let requestBody = {
        name: companyName
      };
      context.propsValue.email && (requestBody.email = context.propsValue.email), context.propsValue.website && (requestBody.website = context.propsValue.website), context.propsValue.main_phone_number && (requestBody.main_phone_number = context.propsValue.main_phone_number), context.propsValue.fax_phone_number && (requestBody.fax_phone_number = context.propsValue.fax_phone_number), context.propsValue.notes && (requestBody.notes = context.propsValue.notes), (context.propsValue.address1 || context.propsValue.city || context.propsValue.state || context.propsValue.zip_code || context.propsValue.country) && (requestBody.address = {
        address1: context.propsValue.address1 || "",
        address2: context.propsValue.address2 || "",
        city: context.propsValue.city || "",
        state: context.propsValue.state || "",
        zip_code: context.propsValue.zip_code || "",
        country: context.propsValue.country || ""
      }), context.propsValue.cases && Array.isArray(context.propsValue.cases) && (requestBody.cases = context.propsValue.cases.map((id) => ({ id: parseInt(id) }))), context.propsValue.clients && Array.isArray(context.propsValue.clients) && (requestBody.clients = context.propsValue.clients.map((id) => ({ id: parseInt(id) })));
      let createResponse = await api.post("/companies", requestBody);
      return createResponse.success ? {
        success: !0,
        company: createResponse.data,
        created: !0,
        message: `Company "${companyName}" created successfully`
      } : {
        success: !1,
        error: createResponse.error,
        details: createResponse.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find or create company",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-or-create-location.ts
var import_pieces_framework35 = require("@activepieces/pieces-framework");
var findOrCreateLocation = (0, import_pieces_framework35.createAction)({
  auth: mycaseAuth,
  name: "find_or_create_location",
  displayName: "Find or Create Location",
  description: "Finds a location by name or creates a new one if it does not exist",
  audience: "both",
  aiMetadata: { description: "Returns the MyCase location matching the given name (case-insensitive), creating it with the optional address fields only if no match exists. Use to guarantee a location exists without risking duplicates; repeat calls with the same name converge to one record. The address fields are applied only on creation, never to an existing match.", idempotent: !0 },
  props: {
    name: import_pieces_framework35.Property.ShortText({
      displayName: "Location Name",
      description: "The name of the location to find or create",
      required: !0
    }),
    address1: import_pieces_framework35.Property.ShortText({
      displayName: "Address Line 1",
      description: "Street address line 1 (used only when creating)",
      required: !1
    }),
    address2: import_pieces_framework35.Property.ShortText({
      displayName: "Address Line 2",
      description: "Street address line 2 (used only when creating)",
      required: !1
    }),
    city: import_pieces_framework35.Property.ShortText({
      displayName: "City",
      description: "City (used only when creating)",
      required: !1
    }),
    state: import_pieces_framework35.Property.ShortText({
      displayName: "State",
      description: "State (used only when creating)",
      required: !1
    }),
    zip_code: import_pieces_framework35.Property.ShortText({
      displayName: "ZIP Code",
      description: "ZIP/Postal code (used only when creating)",
      required: !1
    }),
    country: import_pieces_framework35.Property.ShortText({
      displayName: "Country",
      description: "Country (used only when creating)",
      required: !1
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), locationName = context.propsValue.name;
    try {
      let findResponse = await api.get("/locations", {
        page_size: "1000"
      });
      if (findResponse.success && Array.isArray(findResponse.data)) {
        let existingLocation = findResponse.data.find(
          (loc) => loc.name && loc.name.toLowerCase() === locationName.toLowerCase()
        );
        if (existingLocation)
          return {
            success: !0,
            location: existingLocation,
            created: !1,
            message: `Location "${locationName}" found`
          };
      }
      let requestBody = {
        name: locationName
      };
      (context.propsValue.address1 || context.propsValue.city || context.propsValue.state || context.propsValue.zip_code || context.propsValue.country) && (requestBody.address = {
        address1: context.propsValue.address1 || "",
        address2: context.propsValue.address2 || "",
        city: context.propsValue.city || "",
        state: context.propsValue.state || "",
        zip_code: context.propsValue.zip_code || "",
        country: context.propsValue.country || ""
      });
      let createResponse = await api.post("/locations", requestBody);
      return createResponse.success ? {
        success: !0,
        location: createResponse.data,
        created: !0,
        message: `Location "${locationName}" created successfully`
      } : {
        success: !1,
        error: createResponse.error,
        details: createResponse.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find or create location",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-or-create-person.ts
var import_pieces_framework36 = require("@activepieces/pieces-framework");
var findOrCreatePerson = (0, import_pieces_framework36.createAction)({
  auth: mycaseAuth,
  name: "find_or_create_person",
  displayName: "Find or Create Person (Client)",
  description: "Finds a person by first and last name or creates a new one if not found",
  audience: "both",
  aiMetadata: { description: "Returns the MyCase client (individual person) matching the given first and last name (case-insensitive), creating it with the optional contact, address, people-group, and case fields only if no match exists. Use to guarantee a client exists without creating duplicates; matching is name-only, so distinct people sharing a name collapse to the first match. The extra fields apply only on creation. Use Create Person to always insert.", idempotent: !0 },
  props: {
    first_name: import_pieces_framework36.Property.ShortText({
      displayName: "First Name",
      description: "The first name of the client",
      required: !0
    }),
    last_name: import_pieces_framework36.Property.ShortText({
      displayName: "Last Name",
      description: "The last name of the client",
      required: !0
    }),
    email: import_pieces_framework36.Property.ShortText({
      displayName: "Email",
      description: "The email of the client (used only when creating)",
      required: !1
    }),
    middle_name: import_pieces_framework36.Property.ShortText({
      displayName: "Middle Name",
      description: "The middle name of the client (used only when creating)",
      required: !1
    }),
    cell_phone_number: import_pieces_framework36.Property.ShortText({
      displayName: "Cell Phone Number",
      description: "Cell phone number (used only when creating)",
      required: !1
    }),
    work_phone_number: import_pieces_framework36.Property.ShortText({
      displayName: "Work Phone Number",
      description: "Work phone number (used only when creating)",
      required: !1
    }),
    home_phone_number: import_pieces_framework36.Property.ShortText({
      displayName: "Home Phone Number",
      description: "Home phone number (used only when creating)",
      required: !1
    }),
    fax_phone_number: import_pieces_framework36.Property.ShortText({
      displayName: "Fax Phone Number",
      description: "Fax phone number (used only when creating)",
      required: !1
    }),
    birthdate: import_pieces_framework36.Property.ShortText({
      displayName: "Birthdate",
      description: "Date of birth in ISO-8601 format: YYYY-MM-DD (used only when creating)",
      required: !1
    }),
    notes: import_pieces_framework36.Property.LongText({
      displayName: "Notes",
      description: "Additional information (used only when creating)",
      required: !1
    }),
    address1: import_pieces_framework36.Property.ShortText({
      displayName: "Address Line 1",
      description: "Street address line 1 (used only when creating)",
      required: !1
    }),
    address2: import_pieces_framework36.Property.ShortText({
      displayName: "Address Line 2",
      description: "Street address line 2 (used only when creating)",
      required: !1
    }),
    city: import_pieces_framework36.Property.ShortText({
      displayName: "City",
      description: "City (used only when creating)",
      required: !1
    }),
    state: import_pieces_framework36.Property.ShortText({
      displayName: "State",
      description: "State (used only when creating)",
      required: !1
    }),
    zip_code: import_pieces_framework36.Property.ShortText({
      displayName: "ZIP Code",
      description: "ZIP/Postal code (used only when creating)",
      required: !1
    }),
    country: import_pieces_framework36.Property.ShortText({
      displayName: "Country",
      description: "Country (used only when creating)",
      required: !1
    }),
    people_group: import_pieces_framework36.Property.Dropdown({
      auth: mycaseAuth,
      displayName: "People Group",
      description: "The people group to associate (used only when creating)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/people_groups", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((group) => ({
            label: group.name,
            value: group.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load people groups"
        };
      }
    }),
    cases: import_pieces_framework36.Property.MultiSelectDropdown({
      auth: mycaseAuth,
      displayName: "Cases",
      description: "Cases to associate with the client (used only when creating)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        let response = await createMyCaseApi(auth).get("/cases", {
          page_size: "100"
        });
        return response.success && Array.isArray(response.data) ? {
          disabled: !1,
          options: response.data.map((caseItem) => ({
            label: `${caseItem.name}${caseItem.case_number ? ` (${caseItem.case_number})` : ""}`,
            value: caseItem.id.toString()
          }))
        } : {
          disabled: !0,
          options: [],
          placeholder: "Failed to load cases"
        };
      }
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), firstName = context.propsValue.first_name, lastName = context.propsValue.last_name;
    try {
      let findResponse = await api.get("/clients", {
        page_size: "1000"
      });
      if (findResponse.success && Array.isArray(findResponse.data)) {
        let existingPerson = findResponse.data.find(
          (p) => p.first_name && p.last_name && p.first_name.toLowerCase() === firstName.toLowerCase() && p.last_name.toLowerCase() === lastName.toLowerCase()
        );
        if (existingPerson)
          return {
            success: !0,
            client: existingPerson,
            created: !1,
            message: `Client "${firstName} ${lastName}" found`
          };
      }
      let requestBody = {
        first_name: firstName,
        last_name: lastName
      };
      context.propsValue.email && (requestBody.email = context.propsValue.email), context.propsValue.middle_name && (requestBody.middle_name = context.propsValue.middle_name), context.propsValue.cell_phone_number && (requestBody.cell_phone_number = context.propsValue.cell_phone_number), context.propsValue.work_phone_number && (requestBody.work_phone_number = context.propsValue.work_phone_number), context.propsValue.home_phone_number && (requestBody.home_phone_number = context.propsValue.home_phone_number), context.propsValue.fax_phone_number && (requestBody.fax_phone_number = context.propsValue.fax_phone_number), context.propsValue.birthdate && (requestBody.birthdate = context.propsValue.birthdate), context.propsValue.notes && (requestBody.notes = context.propsValue.notes), (context.propsValue.address1 || context.propsValue.city || context.propsValue.state || context.propsValue.zip_code || context.propsValue.country) && (requestBody.address = {
        address1: context.propsValue.address1 || "",
        address2: context.propsValue.address2 || "",
        city: context.propsValue.city || "",
        state: context.propsValue.state || "",
        zip_code: context.propsValue.zip_code || "",
        country: context.propsValue.country || ""
      }), context.propsValue.people_group && (requestBody.people_group = {
        id: parseInt(context.propsValue.people_group)
      }), context.propsValue.cases && Array.isArray(context.propsValue.cases) && (requestBody.cases = context.propsValue.cases.map((id) => ({ id: parseInt(id) })));
      let createResponse = await api.post("/clients", requestBody);
      return createResponse.success ? {
        success: !0,
        client: createResponse.data,
        created: !0,
        message: `Client "${firstName} ${lastName}" created successfully`
      } : {
        success: !1,
        error: createResponse.error,
        details: createResponse.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find or create client",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-or-create-practice-area.ts
var import_pieces_framework37 = require("@activepieces/pieces-framework");
var findOrCreatePracticeArea = (0, import_pieces_framework37.createAction)({
  auth: mycaseAuth,
  name: "find_or_create_practice_area",
  displayName: "Find or Create Practice Area",
  description: "Finds a practice area by name or creates a new one if it does not exist",
  audience: "both",
  aiMetadata: { description: "Returns the MyCase practice area matching the given name (case-insensitive), creating it only if no match exists. Use to guarantee a practice area exists before assigning it to a case; repeat calls with the same name converge to one record rather than duplicating.", idempotent: !0 },
  props: {
    name: import_pieces_framework37.Property.ShortText({
      displayName: "Practice Area Name",
      description: "The name of the practice area to find or create",
      required: !0
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), practiceAreaName = context.propsValue.name;
    try {
      let findResponse = await api.get("/practice_areas", {
        page_size: "1000"
      });
      if (findResponse.success && Array.isArray(findResponse.data)) {
        let existingPracticeArea = findResponse.data.find(
          (pa) => pa.name && pa.name.toLowerCase() === practiceAreaName.toLowerCase()
        );
        if (existingPracticeArea)
          return {
            success: !0,
            practice_area: existingPracticeArea,
            created: !1,
            message: `Practice area "${practiceAreaName}" found`
          };
      }
      let requestBody = {
        name: practiceAreaName
      }, createResponse = await api.post("/practice_areas", requestBody);
      return createResponse.success ? {
        success: !0,
        practice_area: createResponse.data,
        created: !0,
        message: `Practice area "${practiceAreaName}" created successfully`
      } : {
        success: !1,
        error: createResponse.error,
        details: createResponse.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find or create practice area",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/actions/find-or-create-referral-source.ts
var import_pieces_framework38 = require("@activepieces/pieces-framework");
var findOrCreateReferralSource = (0, import_pieces_framework38.createAction)({
  auth: mycaseAuth,
  name: "find_or_create_referral_source",
  displayName: "Find or Create Referral Source",
  description: "Finds a referral source by name or creates a new one if it does not exist",
  audience: "both",
  aiMetadata: { description: "Resolve a MyCase referral source by exact (case-insensitive) name, creating it if no match exists, and return the source. Use to ensure a named referral source is present without risking duplicates. Idempotent: repeated calls with the same name return the existing source rather than creating another.", idempotent: !0 },
  props: {
    name: import_pieces_framework38.Property.ShortText({
      displayName: "Referral Source Name",
      description: "The name of the referral source to find or create",
      required: !0
    })
  },
  async run(context) {
    let api = createMyCaseApi(context.auth), referralSourceName = context.propsValue.name;
    try {
      let findResponse = await api.get("/referral_sources", {
        page_size: "1000"
      });
      if (findResponse.success && Array.isArray(findResponse.data)) {
        let existingReferralSource = findResponse.data.find(
          (rs) => rs.name && rs.name.toLowerCase() === referralSourceName.toLowerCase()
        );
        if (existingReferralSource)
          return {
            success: !0,
            referral_source: existingReferralSource,
            created: !1,
            message: `Referral source "${referralSourceName}" found`
          };
      }
      let requestBody = {
        name: referralSourceName
      }, createResponse = await api.post("/referral_sources", requestBody);
      return createResponse.success ? {
        success: !0,
        referral_source: createResponse.data,
        created: !0,
        message: `Referral source "${referralSourceName}" created successfully`
      } : {
        success: !1,
        error: createResponse.error,
        details: createResponse.details
      };
    } catch (error) {
      return {
        success: !1,
        error: "Failed to find or create referral source",
        details: error instanceof Error ? error.message : String(error)
      };
    }
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/triggers/case-added-or-updated.ts
var import_pieces_framework39 = require("@activepieces/pieces-framework");
var caseAddedOrUpdated = (0, import_pieces_framework39.createTrigger)({
  auth: mycaseAuth,
  name: "case_added_or_updated",
  displayName: "Case Added or Updated",
  description: "Triggers when a case has been added or updated",
  aiMetadata: {
    description: "Fires whenever a case (matter) is created or modified in MyCase, delivering the full case payload including case number, name, description, practice area, stage, status, key dates, and outstanding balance. Use to react to new matters or changes in an existing case."
  },
  props: {},
  sampleData: {
    id: 12345,
    name: "Sample Case",
    case_number: "CASE-001",
    description: "Sample case description",
    opened_date: "2024-01-01",
    closed_date: null,
    sol_date: null,
    practice_area: "Personal Injury",
    case_stage: "Discovery",
    status: "open",
    outstanding_balance: 5e3,
    created_at: "2024-01-01T10:00:00Z",
    updated_at: "2024-01-15T14:30:00Z"
  },
  type: import_pieces_framework39.TriggerStrategy.WEBHOOK,
  async onEnable(ctx) {
    let api = createMyCaseApi(ctx.auth), requestBody = {
      model: "case",
      url: ctx.webhookUrl,
      actions: ["created", "updated"]
    }, response = await api.post("/webhooks/subscriptions", requestBody);
    if (response.success && response.data)
      await ctx.store.put("_webhook_subscription", {
        id: response.data.id,
        hmac_key: response.data.hmac_key
      });
    else
      throw new Error(
        `Failed to create webhook subscription: ${response.error}`
      );
  },
  async onDisable(ctx) {
    let subscription = await ctx.store.get(
      "_webhook_subscription"
    );
    subscription && (await createMyCaseApi(ctx.auth).delete(`/webhooks/subscriptions/${subscription.id}`), await ctx.store.delete("_webhook_subscription"));
  },
  async run(ctx) {
    return [ctx.payload.body];
  },
  async test(ctx) {
    return [ctx.propsValue];
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/triggers/event-added-or-updated.ts
var import_pieces_framework40 = require("@activepieces/pieces-framework");
var eventAddedOrUpdated = (0, import_pieces_framework40.createTrigger)({
  auth: mycaseAuth,
  name: "event_added_or_updated",
  displayName: "Event Added or Updated",
  description: "Triggers when an event has been added or updated",
  aiMetadata: {
    description: "Fires whenever a calendar event (such as a meeting, appointment, or deadline) is created or modified in MyCase, delivering the full event payload including name, description, start and end times, all-day and private flags, event type, location, associated case, and assigned staff. Use to sync calendars or react to scheduling changes."
  },
  props: {},
  sampleData: {
    id: 12345,
    name: "Client Meeting",
    description: "Initial consultation with client",
    start: "2024-01-15T10:00:00Z",
    end: "2024-01-15T11:00:00Z",
    all_day: !1,
    private: !1,
    event_type: "Meeting",
    location: {
      id: 1
    },
    case: {
      id: 100
    },
    staff: [
      {
        id: 5
      }
    ],
    created_at: "2024-01-01T10:00:00Z",
    updated_at: "2024-01-15T09:30:00Z"
  },
  type: import_pieces_framework40.TriggerStrategy.WEBHOOK,
  async onEnable(ctx) {
    let api = createMyCaseApi(ctx.auth), requestBody = {
      model: "event",
      url: ctx.webhookUrl,
      actions: ["created", "updated"]
    }, response = await api.post("/webhooks/subscriptions", requestBody);
    if (response.success && response.data)
      await ctx.store.put("_webhook_subscription", {
        id: response.data.id,
        hmac_key: response.data.hmac_key
      });
    else
      throw new Error(
        `Failed to create webhook subscription: ${response.error}`
      );
  },
  async onDisable(ctx) {
    let subscription = await ctx.store.get("_webhook_subscription");
    subscription && (await createMyCaseApi(ctx.auth).delete(`/webhooks/subscriptions/${subscription.id}`), await ctx.store.delete("_webhook_subscription"));
  },
  async run(ctx) {
    return [ctx.payload.body];
  },
  async test(ctx) {
    return [ctx.propsValue];
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/triggers/person-added-or-updated.ts
var import_pieces_framework41 = require("@activepieces/pieces-framework");
var personAddedOrUpdated = (0, import_pieces_framework41.createTrigger)({
  auth: mycaseAuth,
  name: "person_added_or_updated",
  displayName: "Person Added or Updated",
  description: "Triggers when a person has been added or updated",
  aiMetadata: {
    description: "Fires whenever a person (client or contact) is created or modified in MyCase, delivering the full person payload including name, contact details, address, contact group, people group, notes, birthdate, associated cases, and custom fields. Use to keep contact and client records in sync as they change."
  },
  props: {},
  sampleData: {
    id: 12345,
    email: "john.doe@example.com",
    first_name: "John",
    middle_name: "Michael",
    last_name: "Doe",
    address: {
      address1: "123 Main St",
      address2: "Suite 100",
      city: "Los Angeles",
      state: "CA",
      zip_code: "90001",
      country: "USA"
    },
    cell_phone_number: "555-123-4567",
    work_phone_number: "555-987-6543",
    home_phone_number: "555-111-2222",
    fax_phone_number: "555-333-4444",
    contact_group: "Client",
    people_group: {
      id: 1
    },
    notes: "Important client notes",
    birthdate: "1980-05-15",
    archived: !1,
    cases: [
      {
        id: 100
      }
    ],
    custom_field_values: [],
    created_at: "2024-01-01T10:00:00Z",
    updated_at: "2024-01-15T14:30:00Z"
  },
  type: import_pieces_framework41.TriggerStrategy.WEBHOOK,
  async onEnable(ctx) {
    let api = createMyCaseApi(ctx.auth), requestBody = {
      model: "client",
      url: ctx.webhookUrl,
      actions: ["created", "updated"]
    }, response = await api.post("/webhooks/subscriptions", requestBody);
    if (response.success && response.data)
      await ctx.store.put("_webhook_subscription", {
        id: response.data.id,
        hmac_key: response.data.hmac_key
      });
    else
      throw new Error(
        `Failed to create webhook subscription: ${response.error}`
      );
  },
  async onDisable(ctx) {
    let subscription = await ctx.store.get("_webhook_subscription");
    subscription && (await createMyCaseApi(ctx.auth).delete(`/webhooks/subscriptions/${subscription.id}`), await ctx.store.delete("_webhook_subscription"));
  },
  async run(ctx) {
    return [ctx.payload.body];
  },
  async test(ctx) {
    return [ctx.propsValue];
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/triggers/company-added-or-updated.ts
var import_pieces_framework42 = require("@activepieces/pieces-framework");
var companyAddedOrUpdated = (0, import_pieces_framework42.createTrigger)({
  auth: mycaseAuth,
  name: "company_added_or_updated",
  displayName: "Company Added or Updated",
  description: "Triggers when a company has been added or updated",
  aiMetadata: {
    description: "Fires whenever a company (business) record is created or modified in MyCase, delivering the full company payload including name, contact details, address, associated cases and clients, custom fields, and timestamps. Use to keep external systems in sync as company records change."
  },
  props: {},
  sampleData: {
    id: 12345,
    name: "Acme Corporation",
    email: "contact@acmecorp.com",
    website: "https://www.acmecorp.com",
    main_phone_number: "555-123-4567",
    fax_phone_number: "555-987-6543",
    address: {
      address1: "456 Business Ave",
      address2: "Floor 5",
      city: "New York",
      state: "NY",
      zip_code: "10001",
      country: "USA"
    },
    notes: "Important company notes",
    cases: [
      {
        id: 100
      }
    ],
    clients: [
      {
        id: 200
      }
    ],
    custom_field_values: [],
    archived: !1,
    created_at: "2024-01-01T10:00:00Z",
    updated_at: "2024-01-15T14:30:00Z"
  },
  type: import_pieces_framework42.TriggerStrategy.WEBHOOK,
  async onEnable(ctx) {
    let api = createMyCaseApi(ctx.auth), requestBody = {
      model: "company",
      url: ctx.webhookUrl,
      actions: ["created", "updated"]
    }, response = await api.post("/webhooks/subscriptions", requestBody);
    if (response.success && response.data)
      await ctx.store.put("_webhook_subscription", {
        id: response.data.id,
        hmac_key: response.data.hmac_key
      });
    else
      throw new Error(
        `Failed to create webhook subscription: ${response.error}`
      );
  },
  async onDisable(ctx) {
    let subscription = await ctx.store.get("_webhook_subscription");
    subscription && (await createMyCaseApi(ctx.auth).delete(`/webhooks/subscriptions/${subscription.id}`), await ctx.store.delete("_webhook_subscription"));
  },
  async run(ctx) {
    return [ctx.payload.body];
  },
  async test(ctx) {
    return [ctx.propsValue];
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/lib/triggers/lead-added-or-updated.ts
var import_pieces_framework43 = require("@activepieces/pieces-framework");
var leadAddedOrUpdated = (0, import_pieces_framework43.createTrigger)({
  auth: mycaseAuth,
  name: "lead_added_or_updated",
  displayName: "Lead Added or Updated",
  description: "Triggers when a lead has been added or updated",
  aiMetadata: {
    description: "Fires whenever a lead (prospective client) is created or modified in MyCase, delivering the full lead payload including name, contact details, address, lead details, status, approval flag, referral source, and custom fields. Use to capture new prospects or react to changes in a lead during intake."
  },
  props: {},
  sampleData: {
    id: 12345,
    email: "lead@example.com",
    first_name: "Jane",
    middle_initial: "M",
    last_name: "Smith",
    address: {
      address1: "789 Lead St",
      address2: "Apt 3",
      city: "Chicago",
      state: "IL",
      zip_code: "60601",
      country: "USA"
    },
    cell_phone_number: "555-123-4567",
    work_phone_number: "555-987-6543",
    home_phone_number: "555-111-2222",
    lead_details: "Potential client for personal injury case",
    birthdate: "1985-03-20",
    drivers_license_number: "D1234567",
    drivers_license_state: "IL",
    status: "New",
    approved: !1,
    referral_source_reference: {
      id: 1
    },
    referred_by: {
      id: 100
    },
    custom_field_values: [],
    created_at: "2024-01-01T10:00:00Z",
    updated_at: "2024-01-15T14:30:00Z"
  },
  type: import_pieces_framework43.TriggerStrategy.WEBHOOK,
  async onEnable(ctx) {
    let api = createMyCaseApi(ctx.auth), requestBody = {
      model: "lead",
      url: ctx.webhookUrl,
      actions: ["created", "updated"]
    }, response = await api.post("/webhooks/subscriptions", requestBody);
    if (response.success && response.data)
      await ctx.store.put("_webhook_subscription", {
        id: response.data.id,
        hmac_key: response.data.hmac_key
      });
    else
      throw new Error(
        `Failed to create webhook subscription: ${response.error}`
      );
  },
  async onDisable(ctx) {
    let subscription = await ctx.store.get("_webhook_subscription");
    subscription && (await createMyCaseApi(ctx.auth).delete(`/webhooks/subscriptions/${subscription.id}`), await ctx.store.delete("_webhook_subscription"));
  },
  async run(ctx) {
    return [ctx.payload.body];
  },
  async test(ctx) {
    return [ctx.propsValue];
  }
});

// ../ap/packages/pieces/community/mycase-piece/src/index.ts
var mycasePiece = (0, import_pieces_framework44.createPiece)({
  displayName: "MyCase",
  description: "Automate legal case management workflows with MyCase. Create and manage cases, clients, companies, events, tasks, time entries, documents, and more. Get notified when cases, events, people, companies, or leads are added or updated.",
  auth: mycaseAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/mycase-piece.png",
  authors: ["Fortunamide"],
  actions: [createCase, createCaseStage, createCompany, createCustomField, createDocument, createEvent, createExpense, createLead, createLocation, createNote, createPerson, createPracticeArea, createReferralSource, createTask, createTimeEntry, createCall, updateCase, updateCompany, updatePerson, makeRequest, findCase, findCall, findCaseStage, findCompany, findLocation, findPeopleGroup, findPerson, findPracticeArea, findReferralSource, findStaff, findOrCreateCase, findOrCreateCaseStage, findOrCreateCompany, findOrCreateLocation, findOrCreatePerson, findOrCreatePracticeArea, findOrCreateReferralSource],
  triggers: [caseAddedOrUpdated, eventAddedOrUpdated, personAddedOrUpdated, companyAddedOrUpdated, leadAddedOrUpdated]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mycasePiece
});
