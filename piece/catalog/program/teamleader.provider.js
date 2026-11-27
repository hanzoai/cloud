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

// ../ap/packages/pieces/community/teamleader/src/index.ts
var index_exports = {};
__export(index_exports, {
  teamleader: () => teamleader
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common19 = require("@activepieces/pieces-common"), import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_framework20 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/teamleader/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), teamleaderAuth = import_pieces_framework.PieceAuth.OAuth2({
  description: `
    To obtain your OAuth2 credentials:
    
    1. Go to the Teamleader Marketplace (https://marketplace.teamleader.eu)
    2. Log in or create an account
    3. Go to "Build" and click "Create a new Integration"
    4. Fill in the required information
    5. On your integration's settings page, configure:
       - Add \`https://cloud.activepieces.com/redirect\` to the allowed redirect URIs
       - Select all required scopes for your integration
    6. Copy the Client ID and Client Secret
    `,
  authUrl: "https://focus.teamleader.eu/oauth2/authorize",
  tokenUrl: "https://focus.teamleader.eu/oauth2/access_token",
  required: !0,
  scope: []
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/create-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/teamleader/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), TEAMLEADER_API_BASE_URL = "https://api.focus.teamleader.eu", teamleaderCommon = {
  baseUrl: TEAMLEADER_API_BASE_URL,
  async apiCall({
    auth,
    method,
    resourceUri,
    body = void 0,
    queryParams = void 0
  }) {
    return await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${TEAMLEADER_API_BASE_URL}${resourceUri}`,
      body,
      queryParams,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      }
    });
  }
};

// ../ap/packages/pieces/community/teamleader/src/lib/actions/create-contact.ts
var createContact = (0, import_pieces_framework2.createAction)({
  name: "create_contact",
  displayName: "Create Contact",
  description: "Create a new contact record",
  audience: "both",
  aiMetadata: { description: "Create a new person contact in Teamleader Focus CRM with name, emails, phones, addresses, and other profile fields. Use to add an individual; for a business entity use Create Company instead. Requires first and last name. Not idempotent: each call creates a separate contact even with identical input, so guard against duplicates (e.g. search by email first).", idempotent: !1 },
  auth: teamleaderAuth,
  props: {
    first_name: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "First name of the contact",
      required: !0
    }),
    last_name: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the contact",
      required: !0
    }),
    salutation: import_pieces_framework2.Property.ShortText({
      displayName: "Salutation",
      description: "Salutation (e.g., Mr, Mrs, Dr)",
      required: !1
    }),
    emails: import_pieces_framework2.Property.Array({
      displayName: "Emails",
      description: "Email addresses for the contact",
      required: !1,
      properties: {
        type: import_pieces_framework2.Property.StaticDropdown({
          displayName: "Type",
          required: !0,
          options: {
            options: [
              { label: "Primary", value: "primary" }
            ]
          }
        }),
        email: import_pieces_framework2.Property.ShortText({
          displayName: "Email Address",
          required: !0
        })
      }
    }),
    telephones: import_pieces_framework2.Property.Array({
      displayName: "Phone Numbers",
      description: "Phone numbers for the contact",
      required: !1,
      properties: {
        type: import_pieces_framework2.Property.StaticDropdown({
          displayName: "Type",
          required: !0,
          options: {
            options: [
              { label: "Phone", value: "phone" },
              { label: "Mobile", value: "mobile" },
              { label: "Fax", value: "fax" }
            ]
          }
        }),
        number: import_pieces_framework2.Property.ShortText({
          displayName: "Phone Number",
          required: !0
        })
      }
    }),
    website: import_pieces_framework2.Property.ShortText({
      displayName: "Website",
      description: "Website URL",
      required: !1
    }),
    addresses: import_pieces_framework2.Property.Array({
      displayName: "Addresses",
      description: "Contact addresses",
      required: !1,
      properties: {
        type: import_pieces_framework2.Property.StaticDropdown({
          displayName: "Address Type",
          required: !0,
          options: {
            options: [
              { label: "Primary", value: "primary" },
              { label: "Invoicing", value: "invoicing" },
              { label: "Delivery", value: "delivery" },
              { label: "Visiting", value: "visiting" }
            ]
          }
        }),
        line_1: import_pieces_framework2.Property.ShortText({
          displayName: "Address Line 1",
          required: !0
        }),
        postal_code: import_pieces_framework2.Property.ShortText({
          displayName: "Postal Code",
          required: !0
        }),
        city: import_pieces_framework2.Property.ShortText({
          displayName: "City",
          required: !0
        }),
        country: import_pieces_framework2.Property.ShortText({
          displayName: "Country Code",
          description: "Two-letter country code (e.g., BE, US, NL)",
          required: !0
        }),
        area_level_two_id: import_pieces_framework2.Property.ShortText({
          displayName: "Area Level Two ID",
          description: "Optional area identifier",
          required: !1
        }),
        addressee: import_pieces_framework2.Property.ShortText({
          displayName: "Addressee",
          description: "Name/company for this address",
          required: !1
        })
      }
    }),
    language: import_pieces_framework2.Property.ShortText({
      displayName: "Language",
      description: "Language code (e.g., en, nl, fr)",
      required: !1
    }),
    gender: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Gender",
      description: "Gender of the contact",
      required: !1,
      options: {
        options: [
          { label: "Female", value: "female" },
          { label: "Male", value: "male" },
          { label: "Non-binary", value: "non_binary" },
          { label: "Prefers not to say", value: "prefers_not_to_say" },
          { label: "Unknown", value: "unknown" }
        ]
      }
    }),
    birthdate: import_pieces_framework2.Property.ShortText({
      displayName: "Birth Date",
      description: "Birth date (YYYY-MM-DD format)",
      required: !1
    }),
    iban: import_pieces_framework2.Property.ShortText({
      displayName: "IBAN",
      description: "International Bank Account Number",
      required: !1
    }),
    bic: import_pieces_framework2.Property.ShortText({
      displayName: "BIC",
      description: "Bank Identifier Code",
      required: !1
    }),
    national_identification_number: import_pieces_framework2.Property.ShortText({
      displayName: "National ID Number",
      description: "National identification number",
      required: !1
    }),
    remarks: import_pieces_framework2.Property.LongText({
      displayName: "Remarks",
      description: "Additional notes (supports Markdown)",
      required: !1
    }),
    tags: import_pieces_framework2.Property.Array({
      displayName: "Tags",
      description: "Contact tags",
      required: !1,
      properties: {
        tag: import_pieces_framework2.Property.ShortText({
          displayName: "Tag",
          required: !0
        })
      }
    }),
    custom_fields: import_pieces_framework2.Property.Array({
      displayName: "Custom Fields",
      description: "Custom field values",
      required: !1,
      properties: {
        id: import_pieces_framework2.Property.ShortText({
          displayName: "Field ID",
          description: "Custom field identifier",
          required: !0
        }),
        value: import_pieces_framework2.Property.ShortText({
          displayName: "Value",
          description: "Field value (string, number, or boolean)",
          required: !0
        })
      }
    }),
    marketing_mails_consent: import_pieces_framework2.Property.Checkbox({
      displayName: "Marketing Consent",
      description: "Allow marketing emails",
      required: !1
    })
  },
  async run(context) {
    let contact = {
      first_name: context.propsValue.first_name,
      last_name: context.propsValue.last_name
    };
    return context.propsValue.salutation && (contact.salutation = context.propsValue.salutation), context.propsValue.website && (contact.website = context.propsValue.website), context.propsValue.language && (contact.language = context.propsValue.language), context.propsValue.gender && (contact.gender = context.propsValue.gender), context.propsValue.birthdate && (contact.birthdate = context.propsValue.birthdate), context.propsValue.iban && (contact.iban = context.propsValue.iban), context.propsValue.bic && (contact.bic = context.propsValue.bic), context.propsValue.national_identification_number && (contact.national_identification_number = context.propsValue.national_identification_number), context.propsValue.remarks && (contact.remarks = context.propsValue.remarks), context.propsValue.marketing_mails_consent !== void 0 && (contact.marketing_mails_consent = context.propsValue.marketing_mails_consent), context.propsValue.emails && context.propsValue.emails.length > 0 && (contact.emails = context.propsValue.emails.map((emailObj) => ({
      type: emailObj.type,
      email: emailObj.email
    }))), context.propsValue.telephones && context.propsValue.telephones.length > 0 && (contact.telephones = context.propsValue.telephones.map((phoneObj) => ({
      type: phoneObj.type,
      number: phoneObj.number
    }))), context.propsValue.addresses && context.propsValue.addresses.length > 0 && (contact.addresses = context.propsValue.addresses.map((addressObj) => ({
      type: addressObj.type,
      address: __spreadValues(__spreadValues({
        line_1: addressObj.line_1 || null,
        postal_code: addressObj.postal_code || null,
        city: addressObj.city || null,
        country: addressObj.country
      }, addressObj.area_level_two_id && { area_level_two_id: addressObj.area_level_two_id }), addressObj.addressee && { addressee: addressObj.addressee })
    }))), context.propsValue.tags && context.propsValue.tags.length > 0 && (contact.tags = context.propsValue.tags.map((tagObj) => tagObj.tag)), context.propsValue.custom_fields && context.propsValue.custom_fields.length > 0 && (contact.custom_fields = context.propsValue.custom_fields.map((fieldObj) => ({
      id: fieldObj.id,
      value: fieldObj.value
    }))), (await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common2.HttpMethod.POST,
      resourceUri: "/contacts.add",
      body: contact
    })).body;
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/update-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var updateContact = (0, import_pieces_framework3.createAction)({
  name: "update_contact",
  displayName: "Update Contact",
  description: "Modify existing contact data",
  audience: "both",
  aiMetadata: { description: "Update fields on an existing Teamleader contact identified by contact ID. Use when you already know the contact and want to change profile data. Only provided fields are sent; note that array fields (emails, phones, addresses, tags) fully replace the existing values rather than appending. Idempotent: re-sending the same values leaves the contact in the same state.", idempotent: !0 },
  auth: teamleaderAuth,
  props: {
    contact_id: import_pieces_framework3.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Contact",
      description: "Select the contact to update",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common3.HttpMethod.POST,
              resourceUri: "/contacts.list",
              body: {}
            })).body.data.map((contact) => ({
              label: `${contact.first_name} ${contact.last_name || ""}`.trim(),
              value: contact.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading contacts"
          };
        }
      }
    }),
    first_name: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "First name of the contact",
      required: !1
    }),
    last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the contact",
      required: !1
    }),
    salutation: import_pieces_framework3.Property.ShortText({
      displayName: "Salutation",
      description: "Salutation (e.g., Mr, Mrs, Dr)",
      required: !1
    }),
    emails: import_pieces_framework3.Property.Array({
      displayName: "Emails",
      description: "Email addresses (replaces all existing emails)",
      required: !1,
      properties: {
        type: import_pieces_framework3.Property.StaticDropdown({
          displayName: "Type",
          required: !0,
          options: {
            options: [
              { label: "Primary", value: "primary" }
            ]
          }
        }),
        email: import_pieces_framework3.Property.ShortText({
          displayName: "Email Address",
          required: !0
        })
      }
    }),
    telephones: import_pieces_framework3.Property.Array({
      displayName: "Phone Numbers",
      description: "Phone numbers (replaces all existing numbers)",
      required: !1,
      properties: {
        type: import_pieces_framework3.Property.StaticDropdown({
          displayName: "Type",
          required: !0,
          options: {
            options: [
              { label: "Phone", value: "phone" },
              { label: "Mobile", value: "mobile" },
              { label: "Fax", value: "fax" }
            ]
          }
        }),
        number: import_pieces_framework3.Property.ShortText({
          displayName: "Phone Number",
          required: !0
        })
      }
    }),
    website: import_pieces_framework3.Property.ShortText({
      displayName: "Website",
      description: "Website URL",
      required: !1
    }),
    addresses: import_pieces_framework3.Property.Array({
      displayName: "Addresses",
      description: "Contact addresses (replaces all existing addresses)",
      required: !1,
      properties: {
        type: import_pieces_framework3.Property.StaticDropdown({
          displayName: "Address Type",
          required: !0,
          options: {
            options: [
              { label: "Primary", value: "primary" },
              { label: "Invoicing", value: "invoicing" },
              { label: "Delivery", value: "delivery" },
              { label: "Visiting", value: "visiting" }
            ]
          }
        }),
        line_1: import_pieces_framework3.Property.ShortText({
          displayName: "Address Line 1",
          required: !0
        }),
        postal_code: import_pieces_framework3.Property.ShortText({
          displayName: "Postal Code",
          required: !0
        }),
        city: import_pieces_framework3.Property.ShortText({
          displayName: "City",
          required: !0
        }),
        country: import_pieces_framework3.Property.ShortText({
          displayName: "Country Code",
          description: "Two-letter country code (e.g., BE, US, NL)",
          required: !0
        }),
        area_level_two_id: import_pieces_framework3.Property.ShortText({
          displayName: "Area Level Two ID",
          description: "Optional area identifier",
          required: !1
        }),
        addressee: import_pieces_framework3.Property.ShortText({
          displayName: "Addressee",
          description: "Name/company for this address",
          required: !1
        })
      }
    }),
    language: import_pieces_framework3.Property.ShortText({
      displayName: "Language",
      description: "Language code (e.g., en, nl, fr)",
      required: !1
    }),
    gender: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Gender",
      description: "Gender of the contact",
      required: !1,
      options: {
        options: [
          { label: "Female", value: "female" },
          { label: "Male", value: "male" },
          { label: "Non-binary", value: "non_binary" },
          { label: "Prefers not to say", value: "prefers_not_to_say" },
          { label: "Unknown", value: "unknown" }
        ]
      }
    }),
    birthdate: import_pieces_framework3.Property.ShortText({
      displayName: "Birth Date",
      description: "Birth date (YYYY-MM-DD format)",
      required: !1
    }),
    iban: import_pieces_framework3.Property.ShortText({
      displayName: "IBAN",
      description: "International Bank Account Number",
      required: !1
    }),
    bic: import_pieces_framework3.Property.ShortText({
      displayName: "BIC",
      description: "Bank Identifier Code",
      required: !1
    }),
    national_identification_number: import_pieces_framework3.Property.ShortText({
      displayName: "National ID Number",
      description: "National identification number",
      required: !1
    }),
    remarks: import_pieces_framework3.Property.LongText({
      displayName: "Remarks",
      description: "Additional notes (supports Markdown)",
      required: !1
    }),
    tags: import_pieces_framework3.Property.Array({
      displayName: "Tags",
      description: "Contact tags (replaces all existing tags)",
      required: !1,
      properties: {
        tag: import_pieces_framework3.Property.ShortText({
          displayName: "Tag",
          required: !0
        })
      }
    }),
    custom_fields: import_pieces_framework3.Property.Array({
      displayName: "Custom Fields",
      description: "Custom field values",
      required: !1,
      properties: {
        id: import_pieces_framework3.Property.ShortText({
          displayName: "Field ID",
          description: "Custom field identifier",
          required: !0
        }),
        value: import_pieces_framework3.Property.ShortText({
          displayName: "Value",
          description: "Field value (string, number, or boolean)",
          required: !0
        })
      }
    }),
    marketing_mails_consent: import_pieces_framework3.Property.Checkbox({
      displayName: "Marketing Consent",
      description: "Allow marketing emails",
      required: !1
    })
  },
  async run(context) {
    let updateData = {
      id: context.propsValue.contact_id
    };
    return context.propsValue.first_name !== void 0 && (updateData.first_name = context.propsValue.first_name), context.propsValue.last_name !== void 0 && (updateData.last_name = context.propsValue.last_name), context.propsValue.salutation !== void 0 && (updateData.salutation = context.propsValue.salutation), context.propsValue.website !== void 0 && (updateData.website = context.propsValue.website), context.propsValue.language !== void 0 && (updateData.language = context.propsValue.language), context.propsValue.gender !== void 0 && (updateData.gender = context.propsValue.gender), context.propsValue.birthdate !== void 0 && (updateData.birthdate = context.propsValue.birthdate), context.propsValue.iban !== void 0 && (updateData.iban = context.propsValue.iban), context.propsValue.bic !== void 0 && (updateData.bic = context.propsValue.bic), context.propsValue.national_identification_number !== void 0 && (updateData.national_identification_number = context.propsValue.national_identification_number), context.propsValue.remarks !== void 0 && (updateData.remarks = context.propsValue.remarks), context.propsValue.marketing_mails_consent !== void 0 && (updateData.marketing_mails_consent = context.propsValue.marketing_mails_consent), context.propsValue.emails !== void 0 && (updateData.emails = context.propsValue.emails.map((emailObj) => ({
      type: emailObj.type,
      email: emailObj.email
    }))), context.propsValue.telephones !== void 0 && (updateData.telephones = context.propsValue.telephones.map((phoneObj) => ({
      type: phoneObj.type,
      number: phoneObj.number
    }))), context.propsValue.addresses !== void 0 && (updateData.addresses = context.propsValue.addresses.map((addressObj) => ({
      type: addressObj.type,
      address: __spreadValues(__spreadValues({
        line_1: addressObj.line_1 || null,
        postal_code: addressObj.postal_code || null,
        city: addressObj.city || null,
        country: addressObj.country
      }, addressObj.area_level_two_id && { area_level_two_id: addressObj.area_level_two_id }), addressObj.addressee && { addressee: addressObj.addressee })
    }))), context.propsValue.tags !== void 0 && (updateData.tags = context.propsValue.tags.map((tagObj) => tagObj.tag)), context.propsValue.custom_fields !== void 0 && (updateData.custom_fields = context.propsValue.custom_fields.map((fieldObj) => ({
      id: fieldObj.id,
      value: fieldObj.value
    }))), await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common3.HttpMethod.POST,
      resourceUri: "/contacts.update",
      body: updateData
    }), {
      success: !0,
      message: "Contact updated successfully",
      contact_id: context.propsValue.contact_id
    };
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/create-company.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createCompany = (0, import_pieces_framework4.createAction)({
  name: "create_company",
  displayName: "Create Company",
  description: "Add a new company record",
  audience: "both",
  aiMetadata: { description: "Create a new company (business entity) in Teamleader Focus CRM with name, VAT number, emails, phones, addresses, and other fields. Use for organizations; for an individual use Create Contact instead. Only the company name is required. Not idempotent: each call creates a separate company even with identical input, so check for an existing match first to avoid duplicates.", idempotent: !1 },
  auth: teamleaderAuth,
  props: {
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Company Name",
      description: "Name of the company",
      required: !0
    }),
    country_for_business_type: import_pieces_framework4.Property.ShortText({
      displayName: "Country Code",
      description: "Two-letter country code (e.g., BE, NL, US) for business type selection",
      required: !1
    }),
    business_type_id: import_pieces_framework4.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Business Type",
      description: "Legal structure of the company",
      required: !1,
      refreshers: ["country_for_business_type"],
      options: async ({ auth, country_for_business_type }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        if (!country_for_business_type) return {
          disabled: !0,
          options: [],
          placeholder: "Please select a country first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common4.HttpMethod.POST,
              resourceUri: "/businessTypes.list",
              body: {
                country: country_for_business_type
              }
            })).body.data.map((businessType) => ({
              label: businessType.name,
              value: businessType.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading business types"
          };
        }
      }
    }),
    vat_number: import_pieces_framework4.Property.ShortText({
      displayName: "VAT Number",
      description: "VAT/tax identification number",
      required: !1
    }),
    national_identification_number: import_pieces_framework4.Property.ShortText({
      displayName: "National ID Number",
      description: "National identification number",
      required: !1
    }),
    emails: import_pieces_framework4.Property.Array({
      displayName: "Emails",
      description: "Email addresses for the company",
      required: !1,
      properties: {
        type: import_pieces_framework4.Property.StaticDropdown({
          displayName: "Type",
          required: !0,
          options: {
            options: [
              { label: "Primary", value: "primary" },
              { label: "Invoicing", value: "invoicing" }
            ]
          }
        }),
        email: import_pieces_framework4.Property.ShortText({
          displayName: "Email Address",
          required: !0
        })
      }
    }),
    telephones: import_pieces_framework4.Property.Array({
      displayName: "Phone Numbers",
      description: "Phone numbers for the company",
      required: !1,
      properties: {
        type: import_pieces_framework4.Property.StaticDropdown({
          displayName: "Type",
          required: !0,
          options: {
            options: [
              { label: "Phone", value: "phone" },
              { label: "Fax", value: "fax" }
            ]
          }
        }),
        number: import_pieces_framework4.Property.ShortText({
          displayName: "Phone Number",
          required: !0
        })
      }
    }),
    website: import_pieces_framework4.Property.ShortText({
      displayName: "Website",
      description: "Company website URL",
      required: !1
    }),
    addresses: import_pieces_framework4.Property.Array({
      displayName: "Addresses",
      description: "Company addresses",
      required: !1,
      properties: {
        type: import_pieces_framework4.Property.StaticDropdown({
          displayName: "Address Type",
          required: !0,
          options: {
            options: [
              { label: "Primary", value: "primary" },
              { label: "Invoicing", value: "invoicing" },
              { label: "Delivery", value: "delivery" },
              { label: "Visiting", value: "visiting" }
            ]
          }
        }),
        line_1: import_pieces_framework4.Property.ShortText({
          displayName: "Address Line 1",
          required: !0
        }),
        postal_code: import_pieces_framework4.Property.ShortText({
          displayName: "Postal Code",
          required: !0
        }),
        city: import_pieces_framework4.Property.ShortText({
          displayName: "City",
          required: !0
        }),
        country: import_pieces_framework4.Property.ShortText({
          displayName: "Country Code",
          description: "Two-letter country code (e.g., BE, US, NL)",
          required: !0
        }),
        area_level_two_id: import_pieces_framework4.Property.ShortText({
          displayName: "Area Level Two ID",
          description: "Optional area identifier",
          required: !1
        }),
        addressee: import_pieces_framework4.Property.ShortText({
          displayName: "Addressee",
          description: "Name/company for this address",
          required: !1
        })
      }
    }),
    iban: import_pieces_framework4.Property.ShortText({
      displayName: "IBAN",
      description: "International Bank Account Number",
      required: !1
    }),
    bic: import_pieces_framework4.Property.ShortText({
      displayName: "BIC",
      description: "Bank Identifier Code",
      required: !1
    }),
    language: import_pieces_framework4.Property.ShortText({
      displayName: "Language",
      description: "Language code (e.g., en, nl, fr)",
      required: !1
    }),
    responsible_user_id: import_pieces_framework4.Property.ShortText({
      displayName: "Responsible User ID",
      description: "ID of the user responsible for this company",
      required: !1
    }),
    remarks: import_pieces_framework4.Property.LongText({
      displayName: "Remarks",
      description: "Additional notes (supports Markdown)",
      required: !1
    }),
    tags: import_pieces_framework4.Property.Array({
      displayName: "Tags",
      description: "Company tags",
      required: !1,
      properties: {
        tag: import_pieces_framework4.Property.ShortText({
          displayName: "Tag",
          required: !0
        })
      }
    }),
    custom_fields: import_pieces_framework4.Property.Array({
      displayName: "Custom Fields",
      description: "Custom field values",
      required: !1,
      properties: {
        id: import_pieces_framework4.Property.ShortText({
          displayName: "Field ID",
          description: "Custom field identifier",
          required: !0
        }),
        value: import_pieces_framework4.Property.ShortText({
          displayName: "Value",
          description: "Field value (string, number, or boolean)",
          required: !0
        })
      }
    }),
    marketing_mails_consent: import_pieces_framework4.Property.Checkbox({
      displayName: "Marketing Consent",
      description: "Allow marketing emails",
      required: !1
    }),
    preferred_currency: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Preferred Currency",
      description: "Preferred currency for this company",
      required: !1,
      options: {
        options: [
          { label: "Euro (EUR)", value: "EUR" },
          { label: "US Dollar (USD)", value: "USD" },
          { label: "British Pound (GBP)", value: "GBP" },
          { label: "Canadian Dollar (CAD)", value: "CAD" },
          { label: "Swiss Franc (CHF)", value: "CHF" },
          { label: "Danish Krone (DKK)", value: "DKK" },
          { label: "Norwegian Krone (NOK)", value: "NOK" },
          { label: "Swedish Krona (SEK)", value: "SEK" },
          { label: "Japanese Yen (JPY)", value: "JPY" },
          { label: "Chinese Yuan (CNY)", value: "CNY" },
          { label: "Czech Koruna (CZK)", value: "CZK" },
          { label: "Polish Zloty (PLN)", value: "PLN" },
          { label: "Romanian Leu (RON)", value: "RON" },
          { label: "Turkish Lira (TRY)", value: "TRY" },
          { label: "South African Rand (ZAR)", value: "ZAR" },
          { label: "Indian Rupee (INR)", value: "INR" },
          { label: "Mexican Peso (MXN)", value: "MXN" },
          { label: "Chilean Peso (CLP)", value: "CLP" },
          { label: "Colombian Peso (COP)", value: "COP" },
          { label: "Peruvian Sol (PEN)", value: "PEN" },
          { label: "Moroccan Dirham (MAD)", value: "MAD" },
          { label: "Icelandic Krona (ISK)", value: "ISK" },
          { label: "Bosnia and Herzegovina Mark (BAM)", value: "BAM" }
        ]
      }
    })
  },
  async run(context) {
    let company = {
      name: context.propsValue.name
    };
    return context.propsValue.business_type_id && (company.business_type_id = context.propsValue.business_type_id), context.propsValue.vat_number && (company.vat_number = context.propsValue.vat_number), context.propsValue.national_identification_number && (company.national_identification_number = context.propsValue.national_identification_number), context.propsValue.website && (company.website = context.propsValue.website), context.propsValue.iban && (company.iban = context.propsValue.iban), context.propsValue.bic && (company.bic = context.propsValue.bic), context.propsValue.language && (company.language = context.propsValue.language), context.propsValue.responsible_user_id && (company.responsible_user_id = context.propsValue.responsible_user_id), context.propsValue.remarks && (company.remarks = context.propsValue.remarks), context.propsValue.marketing_mails_consent !== void 0 && (company.marketing_mails_consent = context.propsValue.marketing_mails_consent), context.propsValue.preferred_currency && (company.preferred_currency = context.propsValue.preferred_currency), context.propsValue.emails && context.propsValue.emails.length > 0 && (company.emails = context.propsValue.emails.map((emailObj) => ({
      type: emailObj.type,
      email: emailObj.email
    }))), context.propsValue.telephones && context.propsValue.telephones.length > 0 && (company.telephones = context.propsValue.telephones.map((phoneObj) => ({
      type: phoneObj.type,
      number: phoneObj.number
    }))), context.propsValue.addresses && context.propsValue.addresses.length > 0 && (company.addresses = context.propsValue.addresses.map((addressObj) => ({
      type: addressObj.type,
      address: __spreadValues(__spreadValues({
        line_1: addressObj.line_1 || null,
        postal_code: addressObj.postal_code || null,
        city: addressObj.city || null,
        country: addressObj.country
      }, addressObj.area_level_two_id && { area_level_two_id: addressObj.area_level_two_id }), addressObj.addressee && { addressee: addressObj.addressee })
    }))), context.propsValue.tags && context.propsValue.tags.length > 0 && (company.tags = context.propsValue.tags.map((tagObj) => tagObj.tag)), context.propsValue.custom_fields && context.propsValue.custom_fields.length > 0 && (company.custom_fields = context.propsValue.custom_fields.map((fieldObj) => ({
      id: fieldObj.id,
      value: fieldObj.value
    }))), (await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: "/companies.add",
      body: company
    })).body;
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/update-company.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateCompany = (0, import_pieces_framework5.createAction)({
  name: "update_company",
  displayName: "Update Company",
  description: "Modify company information",
  audience: "both",
  aiMetadata: { description: "Update fields on an existing Teamleader company identified by company ID. Use when you already know the company and want to change its data. Only provided fields are sent; array fields (emails, phones, addresses, tags) fully replace the existing values rather than appending. Idempotent: re-sending the same values leaves the company in the same state.", idempotent: !0 },
  auth: teamleaderAuth,
  props: {
    company_id: import_pieces_framework5.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Company",
      description: "Select the company to update",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common5.HttpMethod.POST,
              resourceUri: "/companies.list",
              body: {}
            })).body.data.map((company) => ({
              label: company.name,
              value: company.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading companies"
          };
        }
      }
    }),
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Company Name",
      description: "Name of the company",
      required: !1
    }),
    country_for_business_type: import_pieces_framework5.Property.ShortText({
      displayName: "Country Code",
      description: "Two-letter country code (e.g., BE, NL, US) for business type selection",
      required: !1
    }),
    business_type_id: import_pieces_framework5.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Business Type",
      description: "Legal structure of the company",
      required: !1,
      refreshers: ["country_for_business_type"],
      options: async ({ auth, country_for_business_type }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        if (!country_for_business_type) return {
          disabled: !0,
          options: [],
          placeholder: "Please select a country first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common5.HttpMethod.POST,
              resourceUri: "/businessTypes.list",
              body: {
                country: country_for_business_type
              }
            })).body.data.map((businessType) => ({
              label: businessType.name,
              value: businessType.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading business types"
          };
        }
      }
    }),
    vat_number: import_pieces_framework5.Property.ShortText({
      displayName: "VAT Number",
      description: "VAT/tax identification number",
      required: !1
    }),
    national_identification_number: import_pieces_framework5.Property.ShortText({
      displayName: "National ID Number",
      description: "National identification number",
      required: !1
    }),
    emails: import_pieces_framework5.Property.Array({
      displayName: "Emails",
      description: "Email addresses (replaces all existing emails)",
      required: !1,
      properties: {
        type: import_pieces_framework5.Property.StaticDropdown({
          displayName: "Type",
          required: !0,
          options: {
            options: [
              { label: "Primary", value: "primary" },
              { label: "Invoicing", value: "invoicing" }
            ]
          }
        }),
        email: import_pieces_framework5.Property.ShortText({
          displayName: "Email Address",
          required: !0
        })
      }
    }),
    telephones: import_pieces_framework5.Property.Array({
      displayName: "Phone Numbers",
      description: "Phone numbers (replaces all existing numbers)",
      required: !1,
      properties: {
        type: import_pieces_framework5.Property.StaticDropdown({
          displayName: "Type",
          required: !0,
          options: {
            options: [
              { label: "Phone", value: "phone" },
              { label: "Fax", value: "fax" }
            ]
          }
        }),
        number: import_pieces_framework5.Property.ShortText({
          displayName: "Phone Number",
          required: !0
        })
      }
    }),
    website: import_pieces_framework5.Property.ShortText({
      displayName: "Website",
      description: "Company website URL",
      required: !1
    }),
    addresses: import_pieces_framework5.Property.Array({
      displayName: "Addresses",
      description: "Company addresses (replaces all existing addresses)",
      required: !1,
      properties: {
        type: import_pieces_framework5.Property.StaticDropdown({
          displayName: "Address Type",
          required: !0,
          options: {
            options: [
              { label: "Primary", value: "primary" },
              { label: "Invoicing", value: "invoicing" },
              { label: "Delivery", value: "delivery" },
              { label: "Visiting", value: "visiting" }
            ]
          }
        }),
        line_1: import_pieces_framework5.Property.ShortText({
          displayName: "Address Line 1",
          required: !0
        }),
        postal_code: import_pieces_framework5.Property.ShortText({
          displayName: "Postal Code",
          required: !0
        }),
        city: import_pieces_framework5.Property.ShortText({
          displayName: "City",
          required: !0
        }),
        country: import_pieces_framework5.Property.ShortText({
          displayName: "Country Code",
          description: "Two-letter country code (e.g., BE, US, NL)",
          required: !0
        }),
        area_level_two_id: import_pieces_framework5.Property.ShortText({
          displayName: "Area Level Two ID",
          description: "Optional area identifier",
          required: !1
        }),
        addressee: import_pieces_framework5.Property.ShortText({
          displayName: "Addressee",
          description: "Name/company for this address",
          required: !1
        })
      }
    }),
    iban: import_pieces_framework5.Property.ShortText({
      displayName: "IBAN",
      description: "International Bank Account Number",
      required: !1
    }),
    bic: import_pieces_framework5.Property.ShortText({
      displayName: "BIC",
      description: "Bank Identifier Code",
      required: !1
    }),
    language: import_pieces_framework5.Property.ShortText({
      displayName: "Language",
      description: "Language code (e.g., en, nl, fr)",
      required: !1
    }),
    responsible_user_id: import_pieces_framework5.Property.ShortText({
      displayName: "Responsible User ID",
      description: "ID of the user responsible for this company",
      required: !1
    }),
    remarks: import_pieces_framework5.Property.LongText({
      displayName: "Remarks",
      description: "Additional notes (supports Markdown)",
      required: !1
    }),
    tags: import_pieces_framework5.Property.Array({
      displayName: "Tags",
      description: "Company tags (replaces all existing tags)",
      required: !1,
      properties: {
        tag: import_pieces_framework5.Property.ShortText({
          displayName: "Tag",
          required: !0
        })
      }
    }),
    custom_fields: import_pieces_framework5.Property.Array({
      displayName: "Custom Fields",
      description: "Custom field values",
      required: !1,
      properties: {
        id: import_pieces_framework5.Property.ShortText({
          displayName: "Field ID",
          description: "Custom field identifier",
          required: !0
        }),
        value: import_pieces_framework5.Property.ShortText({
          displayName: "Value",
          description: "Field value (string, number, or boolean)",
          required: !0
        })
      }
    }),
    marketing_mails_consent: import_pieces_framework5.Property.Checkbox({
      displayName: "Marketing Consent",
      description: "Allow marketing emails",
      required: !1
    }),
    preferred_currency: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Preferred Currency",
      description: "Preferred currency for this company",
      required: !1,
      options: {
        options: [
          { label: "Euro (EUR)", value: "EUR" },
          { label: "US Dollar (USD)", value: "USD" },
          { label: "British Pound (GBP)", value: "GBP" },
          { label: "Canadian Dollar (CAD)", value: "CAD" },
          { label: "Swiss Franc (CHF)", value: "CHF" },
          { label: "Danish Krone (DKK)", value: "DKK" },
          { label: "Norwegian Krone (NOK)", value: "NOK" },
          { label: "Swedish Krona (SEK)", value: "SEK" },
          { label: "Japanese Yen (JPY)", value: "JPY" },
          { label: "Chinese Yuan (CNY)", value: "CNY" },
          { label: "Czech Koruna (CZK)", value: "CZK" },
          { label: "Polish Zloty (PLN)", value: "PLN" },
          { label: "Romanian Leu (RON)", value: "RON" },
          { label: "Turkish Lira (TRY)", value: "TRY" },
          { label: "South African Rand (ZAR)", value: "ZAR" },
          { label: "Indian Rupee (INR)", value: "INR" },
          { label: "Mexican Peso (MXN)", value: "MXN" },
          { label: "Chilean Peso (CLP)", value: "CLP" },
          { label: "Colombian Peso (COP)", value: "COP" },
          { label: "Peruvian Sol (PEN)", value: "PEN" },
          { label: "Moroccan Dirham (MAD)", value: "MAD" },
          { label: "Icelandic Krona (ISK)", value: "ISK" },
          { label: "Bosnia and Herzegovina Mark (BAM)", value: "BAM" }
        ]
      }
    })
  },
  async run(context) {
    let updateData = {
      id: context.propsValue.company_id
    };
    return context.propsValue.name !== void 0 && (updateData.name = context.propsValue.name), context.propsValue.business_type_id !== void 0 && (updateData.business_type_id = context.propsValue.business_type_id), context.propsValue.vat_number !== void 0 && (updateData.vat_number = context.propsValue.vat_number), context.propsValue.national_identification_number !== void 0 && (updateData.national_identification_number = context.propsValue.national_identification_number), context.propsValue.website !== void 0 && (updateData.website = context.propsValue.website), context.propsValue.iban !== void 0 && (updateData.iban = context.propsValue.iban), context.propsValue.bic !== void 0 && (updateData.bic = context.propsValue.bic), context.propsValue.language !== void 0 && (updateData.language = context.propsValue.language), context.propsValue.responsible_user_id !== void 0 && (updateData.responsible_user_id = context.propsValue.responsible_user_id), context.propsValue.remarks !== void 0 && (updateData.remarks = context.propsValue.remarks), context.propsValue.marketing_mails_consent !== void 0 && (updateData.marketing_mails_consent = context.propsValue.marketing_mails_consent), context.propsValue.preferred_currency !== void 0 && (updateData.preferred_currency = context.propsValue.preferred_currency), context.propsValue.emails !== void 0 && (updateData.emails = context.propsValue.emails.map((emailObj) => ({
      type: emailObj.type,
      email: emailObj.email
    }))), context.propsValue.telephones !== void 0 && (updateData.telephones = context.propsValue.telephones.map((phoneObj) => ({
      type: phoneObj.type,
      number: phoneObj.number
    }))), context.propsValue.addresses !== void 0 && (updateData.addresses = context.propsValue.addresses.map((addressObj) => ({
      type: addressObj.type,
      address: __spreadValues(__spreadValues({
        line_1: addressObj.line_1 || null,
        postal_code: addressObj.postal_code || null,
        city: addressObj.city || null,
        country: addressObj.country
      }, addressObj.area_level_two_id && { area_level_two_id: addressObj.area_level_two_id }), addressObj.addressee && { addressee: addressObj.addressee })
    }))), context.propsValue.tags !== void 0 && (updateData.tags = context.propsValue.tags.map((tagObj) => tagObj.tag)), context.propsValue.custom_fields !== void 0 && (updateData.custom_fields = context.propsValue.custom_fields.map((fieldObj) => ({
      id: fieldObj.id,
      value: fieldObj.value
    }))), await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common5.HttpMethod.POST,
      resourceUri: "/companies.update",
      body: updateData
    }), {
      success: !0,
      message: "Company updated successfully",
      company_id: context.propsValue.company_id
    };
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/link-contact-to-company.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var linkContactToCompany = (0, import_pieces_framework6.createAction)({
  name: "link_contact_to_company",
  displayName: "Link Contact to Company",
  description: "Associate a contact with a company",
  audience: "both",
  aiMetadata: { description: "Associate an existing contact with an existing company in Teamleader, optionally recording the contact's position and whether they are a decision maker. Use after both records exist to express the employment/relationship link. Requires contact ID and company ID. Idempotent on the link itself: re-running establishes the same association.", idempotent: !0 },
  auth: teamleaderAuth,
  props: {
    contact_id: import_pieces_framework6.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Contact",
      description: "Select the contact to link",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common6.HttpMethod.POST,
              resourceUri: "/contacts.list",
              body: {}
            })).body.data.map((contact) => ({
              label: `${contact.first_name} ${contact.last_name || ""}`.trim(),
              value: contact.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading contacts"
          };
        }
      }
    }),
    company_id: import_pieces_framework6.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Company",
      description: "Select the company to link to",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common6.HttpMethod.POST,
              resourceUri: "/companies.list",
              body: {}
            })).body.data.map((company) => ({
              label: company.name,
              value: company.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading companies"
          };
        }
      }
    }),
    position: import_pieces_framework6.Property.ShortText({
      displayName: "Position",
      description: "Job title or role (e.g., CEO, Manager)",
      required: !1
    }),
    decision_maker: import_pieces_framework6.Property.Checkbox({
      displayName: "Decision Maker",
      description: "Is this contact a decision maker?",
      required: !1
    })
  },
  async run(context) {
    let linkData = {
      id: context.propsValue.contact_id,
      company_id: context.propsValue.company_id
    };
    return context.propsValue.position && (linkData.position = context.propsValue.position), context.propsValue.decision_maker !== void 0 && (linkData.decision_maker = context.propsValue.decision_maker), await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common6.HttpMethod.POST,
      resourceUri: "/contacts.linkToCompany",
      body: linkData
    }), {
      success: !0,
      message: "Contact linked to company successfully",
      contact_id: context.propsValue.contact_id,
      company_id: context.propsValue.company_id
    };
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/unlink-contact-from-company.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var unlinkContactFromCompany = (0, import_pieces_framework7.createAction)({
  name: "unlink_contact_from_company",
  displayName: "Unlink Contact from Company",
  description: "Remove the association between contact and company",
  audience: "both",
  aiMetadata: { description: "Remove the link between a contact and a company in Teamleader without deleting either record. Use to undo a Link Contact to Company association. Requires contact ID and company ID. Idempotent: if the link is already absent, re-running leaves them unlinked.", idempotent: !0 },
  auth: teamleaderAuth,
  props: {
    contact_id: import_pieces_framework7.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Contact",
      description: "Select the contact to unlink",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common7.HttpMethod.POST,
              resourceUri: "/contacts.list",
              body: {}
            })).body.data.map((contact) => ({
              label: `${contact.first_name} ${contact.last_name || ""}`.trim(),
              value: contact.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading contacts"
          };
        }
      }
    }),
    company_id: import_pieces_framework7.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Company",
      description: "Select the company to unlink from",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common7.HttpMethod.POST,
              resourceUri: "/companies.list",
              body: {}
            })).body.data.map((company) => ({
              label: company.name,
              value: company.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading companies"
          };
        }
      }
    })
  },
  async run(context) {
    return await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common7.HttpMethod.POST,
      resourceUri: "/contacts.unlinkFromCompany",
      body: {
        id: context.propsValue.contact_id,
        company_id: context.propsValue.company_id
      }
    }), {
      success: !0,
      message: "Contact unlinked from company successfully",
      contact_id: context.propsValue.contact_id,
      company_id: context.propsValue.company_id
    };
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/create-deal.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var createDeal = (0, import_pieces_framework8.createAction)({
  name: "create_deal",
  displayName: "Create Deal",
  description: "Create a new deal/opportunity",
  audience: "both",
  aiMetadata: { description: "Create a new sales deal/opportunity in Teamleader for a given customer (a company or a contact). Use to open a new pipeline opportunity. Requires a title, a customer type (company or contact), and the matching customer ID; phase, source, department, responsible user, estimated value, and closing date are optional. Not idempotent: each call creates a separate deal.", idempotent: !1 },
  auth: teamleaderAuth,
  props: {
    title: import_pieces_framework8.Property.ShortText({
      displayName: "Deal Title",
      description: "Title of the deal",
      required: !0
    }),
    customer_type: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Customer Type",
      description: "Type of customer for this deal",
      required: !0,
      options: {
        options: [
          { label: "Company", value: "company" },
          { label: "Contact", value: "contact" }
        ]
      }
    }),
    customer_id: import_pieces_framework8.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Customer",
      description: "Select the customer (company or contact)",
      required: !0,
      refreshers: ["customer_type"],
      options: async ({ auth, customer_type }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        if (!customer_type) return {
          disabled: !0,
          options: [],
          placeholder: "Please select customer type first"
        };
        try {
          return customer_type === "company" ? {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common8.HttpMethod.POST,
              resourceUri: "/companies.list",
              body: {}
            })).body.data.map((company) => ({
              label: company.name,
              value: company.id
            }))
          } : {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common8.HttpMethod.POST,
              resourceUri: "/contacts.list",
              body: {}
            })).body.data.map((contact) => ({
              label: `${contact.first_name} ${contact.last_name || ""}`.trim(),
              value: contact.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading customers"
          };
        }
      }
    }),
    contact_person_id: import_pieces_framework8.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Contact Person",
      description: "Contact person for this deal (for company customers)",
      required: !1,
      refreshers: ["customer_type", "customer_id"],
      options: async ({ auth, customer_type, customer_id }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        if (customer_type !== "company" || !customer_id) return {
          disabled: !0,
          options: [],
          placeholder: "Only available for company customers"
        };
        try {
          return {
            disabled: !1,
            options: ((await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common8.HttpMethod.POST,
              resourceUri: "/companies.info",
              body: {
                id: customer_id,
                includes: "related_contacts"
              }
            })).body.data.related_contacts || []).map((contact) => ({
              label: `${contact.first_name} ${contact.last_name || ""}`.trim(),
              value: contact.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading contacts"
          };
        }
      }
    }),
    summary: import_pieces_framework8.Property.LongText({
      displayName: "Summary",
      description: "Additional information about the deal",
      required: !1
    }),
    source_id: import_pieces_framework8.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Source",
      description: "How the lead was acquired",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common8.HttpMethod.POST,
              resourceUri: "/dealSources.list",
              body: {}
            })).body.data.map((source) => ({
              label: source.name,
              value: source.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading sources"
          };
        }
      }
    }),
    department_id: import_pieces_framework8.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Department",
      description: "Department responsible for this deal",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common8.HttpMethod.POST,
              resourceUri: "/departments.list",
              body: {
                filter: {
                  status: ["active"]
                }
              }
            })).body.data.map((department) => ({
              label: department.name,
              value: department.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading departments"
          };
        }
      }
    }),
    responsible_user_id: import_pieces_framework8.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Responsible User",
      description: "User responsible for this deal",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common8.HttpMethod.POST,
              resourceUri: "/users.list",
              body: {
                filter: {
                  status: ["active"]
                }
              }
            })).body.data.map((user) => ({
              label: `${user.first_name} ${user.last_name}`,
              value: user.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading users"
          };
        }
      }
    }),
    phase_id: import_pieces_framework8.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Phase",
      description: "Deal phase or stage",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common8.HttpMethod.POST,
              resourceUri: "/dealPhases.list",
              body: {}
            })).body.data.map((phase) => ({
              label: phase.name,
              value: phase.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading phases"
          };
        }
      }
    }),
    estimated_value_amount: import_pieces_framework8.Property.Number({
      displayName: "Estimated Value",
      description: "Deal value amount",
      required: !1
    }),
    estimated_value_currency: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Value Currency",
      description: "Currency for the estimated value",
      required: !1,
      options: {
        options: [
          { label: "Euro (EUR)", value: "EUR" },
          { label: "US Dollar (USD)", value: "USD" },
          { label: "British Pound (GBP)", value: "GBP" },
          { label: "Canadian Dollar (CAD)", value: "CAD" },
          { label: "Swiss Franc (CHF)", value: "CHF" },
          { label: "Danish Krone (DKK)", value: "DKK" },
          { label: "Norwegian Krone (NOK)", value: "NOK" },
          { label: "Swedish Krona (SEK)", value: "SEK" },
          { label: "Japanese Yen (JPY)", value: "JPY" },
          { label: "Chinese Yuan (CNY)", value: "CNY" }
        ]
      }
    }),
    estimated_probability: import_pieces_framework8.Property.Number({
      displayName: "Estimated Probability",
      description: "Win probability (0-1, e.g., 0.75 for 75%)",
      required: !1
    }),
    estimated_closing_date: import_pieces_framework8.Property.DateTime({
      displayName: "Estimated Closing Date",
      description: "Expected deal closing date",
      required: !1
    }),
    custom_fields: import_pieces_framework8.Property.Array({
      displayName: "Custom Fields",
      description: "Custom field values",
      required: !1,
      properties: {
        id: import_pieces_framework8.Property.ShortText({
          displayName: "Field ID",
          description: "Custom field identifier",
          required: !0
        }),
        value: import_pieces_framework8.Property.ShortText({
          displayName: "Value",
          description: "Field value",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let deal = {
      lead: {
        customer: {
          type: context.propsValue.customer_type,
          id: context.propsValue.customer_id
        }
      },
      title: context.propsValue.title
    };
    if (context.propsValue.contact_person_id && (deal.lead.contact_person_id = context.propsValue.contact_person_id), context.propsValue.summary && (deal.summary = context.propsValue.summary), context.propsValue.source_id && (deal.source_id = context.propsValue.source_id), context.propsValue.department_id && (deal.department_id = context.propsValue.department_id), context.propsValue.responsible_user_id && (deal.responsible_user_id = context.propsValue.responsible_user_id), context.propsValue.phase_id && (deal.phase_id = context.propsValue.phase_id), context.propsValue.estimated_value_amount && context.propsValue.estimated_value_currency && (deal.estimated_value = {
      amount: context.propsValue.estimated_value_amount,
      currency: context.propsValue.estimated_value_currency
    }), context.propsValue.estimated_probability !== void 0 && (deal.estimated_probability = context.propsValue.estimated_probability), context.propsValue.estimated_closing_date) {
      let closingDate = new Date(context.propsValue.estimated_closing_date);
      deal.estimated_closing_date = closingDate.toISOString().split("T")[0];
    }
    return context.propsValue.custom_fields && context.propsValue.custom_fields.length > 0 && (deal.custom_fields = context.propsValue.custom_fields.map((fieldObj) => ({
      id: fieldObj.id,
      value: fieldObj.value
    }))), (await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common8.HttpMethod.POST,
      resourceUri: "/deals.create",
      body: deal
    })).body;
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/update-deal.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var updateDeal = (0, import_pieces_framework9.createAction)({
  name: "update_deal",
  displayName: "Update Deal",
  description: "Modify deal properties",
  audience: "both",
  aiMetadata: { description: "Update properties of an existing Teamleader deal identified by deal ID, such as title, customer, summary, source, department, responsible user, estimated value, probability, or closing date. Use when you already know the deal and want to change its data; only provided fields are sent. Idempotent: re-sending the same values leaves the deal unchanged.", idempotent: !0 },
  auth: teamleaderAuth,
  props: {
    deal_id: import_pieces_framework9.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Deal",
      description: "Select the deal to update",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common9.HttpMethod.POST,
              resourceUri: "/deals.list",
              body: {}
            })).body.data.map((deal) => ({
              label: deal.title,
              value: deal.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading deals"
          };
        }
      }
    }),
    title: import_pieces_framework9.Property.ShortText({
      displayName: "Deal Title",
      description: "Title of the deal",
      required: !1
    }),
    customer_type: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Customer Type",
      description: "Type of customer for this deal",
      required: !1,
      options: {
        options: [
          { label: "Company", value: "company" },
          { label: "Contact", value: "contact" }
        ]
      }
    }),
    customer_id: import_pieces_framework9.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Customer",
      description: "Select the customer (company or contact)",
      required: !1,
      refreshers: ["customer_type"],
      options: async ({ auth, customer_type }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        if (!customer_type) return {
          disabled: !0,
          options: [],
          placeholder: "Please select customer type first"
        };
        try {
          return customer_type === "company" ? {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common9.HttpMethod.POST,
              resourceUri: "/companies.list",
              body: {}
            })).body.data.map((company) => ({
              label: company.name,
              value: company.id
            }))
          } : {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common9.HttpMethod.POST,
              resourceUri: "/contacts.list",
              body: {}
            })).body.data.map((contact) => ({
              label: `${contact.first_name} ${contact.last_name || ""}`.trim(),
              value: contact.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading customers"
          };
        }
      }
    }),
    contact_person_id: import_pieces_framework9.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Contact Person",
      description: "Contact person for this deal (for company customers)",
      required: !1,
      refreshers: ["customer_type", "customer_id"],
      options: async ({ auth, customer_type, customer_id }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        if (customer_type !== "company" || !customer_id) return {
          disabled: !0,
          options: [],
          placeholder: "Only available for company customers"
        };
        try {
          return {
            disabled: !1,
            options: ((await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common9.HttpMethod.POST,
              resourceUri: "/companies.info",
              body: {
                id: customer_id,
                includes: "related_contacts"
              }
            })).body.data.related_contacts || []).map((contact) => ({
              label: `${contact.first_name} ${contact.last_name || ""}`.trim(),
              value: contact.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading contacts"
          };
        }
      }
    }),
    summary: import_pieces_framework9.Property.LongText({
      displayName: "Summary",
      description: "Additional information about the deal",
      required: !1
    }),
    source_id: import_pieces_framework9.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Source",
      description: "How the lead was acquired",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common9.HttpMethod.POST,
              resourceUri: "/dealSources.list",
              body: {}
            })).body.data.map((source) => ({
              label: source.name,
              value: source.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading sources"
          };
        }
      }
    }),
    department_id: import_pieces_framework9.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Department",
      description: "Department responsible for this deal",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common9.HttpMethod.POST,
              resourceUri: "/departments.list",
              body: {
                filter: {
                  status: ["active"]
                }
              }
            })).body.data.map((department) => ({
              label: department.name,
              value: department.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading departments"
          };
        }
      }
    }),
    responsible_user_id: import_pieces_framework9.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Responsible User",
      description: "User responsible for this deal",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common9.HttpMethod.POST,
              resourceUri: "/users.list",
              body: {
                filter: {
                  status: ["active"]
                }
              }
            })).body.data.map((user) => ({
              label: `${user.first_name} ${user.last_name}`,
              value: user.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading users"
          };
        }
      }
    }),
    estimated_value_amount: import_pieces_framework9.Property.Number({
      displayName: "Estimated Value",
      description: "Deal value amount",
      required: !1
    }),
    estimated_value_currency: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Value Currency",
      description: "Currency for the estimated value",
      required: !1,
      options: {
        options: [
          { label: "Euro (EUR)", value: "EUR" },
          { label: "US Dollar (USD)", value: "USD" },
          { label: "British Pound (GBP)", value: "GBP" },
          { label: "Canadian Dollar (CAD)", value: "CAD" },
          { label: "Swiss Franc (CHF)", value: "CHF" },
          { label: "Danish Krone (DKK)", value: "DKK" },
          { label: "Norwegian Krone (NOK)", value: "NOK" },
          { label: "Swedish Krona (SEK)", value: "SEK" },
          { label: "Japanese Yen (JPY)", value: "JPY" },
          { label: "Chinese Yuan (CNY)", value: "CNY" }
        ]
      }
    }),
    estimated_probability: import_pieces_framework9.Property.Number({
      displayName: "Estimated Probability",
      description: "Win probability (0-1, e.g., 0.75 for 75%)",
      required: !1
    }),
    estimated_closing_date: import_pieces_framework9.Property.DateTime({
      displayName: "Estimated Closing Date",
      description: "Expected deal closing date",
      required: !1
    }),
    custom_fields: import_pieces_framework9.Property.Array({
      displayName: "Custom Fields",
      description: "Custom field values",
      required: !1,
      properties: {
        id: import_pieces_framework9.Property.ShortText({
          displayName: "Field ID",
          description: "Custom field identifier",
          required: !0
        }),
        value: import_pieces_framework9.Property.ShortText({
          displayName: "Value",
          description: "Field value",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let dealUpdate = {
      id: context.propsValue.deal_id
    };
    if (context.propsValue.customer_type && context.propsValue.customer_id && (dealUpdate.lead = {
      customer: {
        type: context.propsValue.customer_type,
        id: context.propsValue.customer_id
      }
    }, context.propsValue.contact_person_id && (dealUpdate.lead.contact_person_id = context.propsValue.contact_person_id)), context.propsValue.title !== void 0 && (dealUpdate.title = context.propsValue.title), context.propsValue.summary !== void 0 && (dealUpdate.summary = context.propsValue.summary), context.propsValue.source_id !== void 0 && (dealUpdate.source_id = context.propsValue.source_id), context.propsValue.department_id !== void 0 && (dealUpdate.department_id = context.propsValue.department_id), context.propsValue.responsible_user_id !== void 0 && (dealUpdate.responsible_user_id = context.propsValue.responsible_user_id), context.propsValue.estimated_value_amount !== void 0 && context.propsValue.estimated_value_currency !== void 0 && (dealUpdate.estimated_value = {
      amount: context.propsValue.estimated_value_amount,
      currency: context.propsValue.estimated_value_currency
    }), context.propsValue.estimated_probability !== void 0 && (dealUpdate.estimated_probability = context.propsValue.estimated_probability), context.propsValue.estimated_closing_date !== void 0) {
      let closingDate = new Date(context.propsValue.estimated_closing_date);
      dealUpdate.estimated_closing_date = closingDate.toISOString().split("T")[0];
    }
    return context.propsValue.custom_fields && context.propsValue.custom_fields.length > 0 && (dealUpdate.custom_fields = context.propsValue.custom_fields.map((fieldObj) => ({
      id: fieldObj.id,
      value: fieldObj.value
    }))), await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common9.HttpMethod.POST,
      resourceUri: "/deals.update",
      body: dealUpdate
    }), {
      success: !0,
      message: "Deal updated successfully",
      deal_id: context.propsValue.deal_id
    };
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/search-companies.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var searchCompanies = (0, import_pieces_framework10.createAction)({
  name: "search_companies",
  displayName: "Search Companies",
  description: "List or filter companies",
  audience: "both",
  aiMetadata: { description: "Look up companies in Teamleader, optionally narrowing by a free-text term, email, VAT number, status, tags, or updated-since date, with paging and sorting. With no filters it lists all companies; supply filters to find specific matches (e.g. resolve a company ID before creating a deal). Read-only and idempotent.", idempotent: !0 },
  auth: teamleaderAuth,
  props: {
    term: import_pieces_framework10.Property.ShortText({
      displayName: "Search Term",
      description: "Search by name, VAT number, emails or telephones",
      required: !1
    }),
    email: import_pieces_framework10.Property.ShortText({
      displayName: "Email",
      description: "Filter by email address",
      required: !1
    }),
    vat_number: import_pieces_framework10.Property.ShortText({
      displayName: "VAT Number",
      description: "Filter by VAT number",
      required: !1
    }),
    status: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter by company status",
      required: !1,
      options: {
        options: [
          { label: "Active", value: "active" },
          { label: "Deactivated", value: "deactivated" }
        ]
      }
    }),
    tags: import_pieces_framework10.Property.ShortText({
      displayName: "Tags",
      description: "Filter by tag names (comma-separated)",
      required: !1
    }),
    updated_since: import_pieces_framework10.Property.DateTime({
      displayName: "Updated Since",
      description: "Only companies updated after this date",
      required: !1
    }),
    sort_field: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Sort Field",
      description: "Field to sort by",
      required: !1,
      options: {
        options: [
          { label: "Name", value: "name" },
          { label: "Date Added", value: "added_at" },
          { label: "Date Updated", value: "updated_at" }
        ]
      }
    }),
    sort_order: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Sort Order",
      description: "Sort direction",
      required: !1,
      options: {
        options: [
          { label: "Ascending (A-Z, Oldest First)", value: "asc" },
          { label: "Descending (Z-A, Newest First)", value: "desc" }
        ]
      }
    }),
    page_size: import_pieces_framework10.Property.Number({
      displayName: "Results Per Page",
      description: "Number of results per page (default: 20)",
      required: !1
    }),
    page_number: import_pieces_framework10.Property.Number({
      displayName: "Page Number",
      description: "Page number to retrieve (default: 1)",
      required: !1
    }),
    include_custom_fields: import_pieces_framework10.Property.Checkbox({
      displayName: "Include Custom Fields",
      description: "Include custom field data in results",
      required: !1
    })
  },
  async run(context) {
    let requestBody = {}, filter = {};
    if (context.propsValue.term && (filter.term = context.propsValue.term), context.propsValue.email && (filter.email = {
      type: "primary",
      email: context.propsValue.email
    }), context.propsValue.vat_number && (filter.vat_number = context.propsValue.vat_number), context.propsValue.status && (filter.status = context.propsValue.status), context.propsValue.tags) {
      let tagList = context.propsValue.tags.split(",").map((tag) => tag.trim()).filter((tag) => tag);
      tagList.length > 0 && (filter.tags = tagList);
    }
    if (context.propsValue.updated_since) {
      let updatedSince = new Date(context.propsValue.updated_since);
      filter.updated_since = updatedSince.toISOString();
    }
    Object.keys(filter).length > 0 && (requestBody.filter = filter);
    let page = {};
    return context.propsValue.page_size && (page.size = context.propsValue.page_size), context.propsValue.page_number && (page.number = context.propsValue.page_number), Object.keys(page).length > 0 && (requestBody.page = page), context.propsValue.sort_field && context.propsValue.sort_order && (requestBody.sort = [{
      field: context.propsValue.sort_field,
      order: context.propsValue.sort_order
    }]), context.propsValue.include_custom_fields && (requestBody.includes = "custom_fields"), (await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common10.HttpMethod.POST,
      resourceUri: "/companies.list",
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/search-contacts.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var searchContacts = (0, import_pieces_framework11.createAction)({
  name: "search_contacts",
  displayName: "Search Contacts",
  description: "List or filter contacts",
  audience: "both",
  aiMetadata: { description: "Look up contacts in Teamleader, optionally narrowing by a free-text term, email, company, status, tags, or updated-since date, with paging and sorting. With no filters it lists all contacts; supply filters to find specific matches (e.g. resolve a contact ID by email before updating or linking). Read-only and idempotent.", idempotent: !0 },
  auth: teamleaderAuth,
  props: {
    term: import_pieces_framework11.Property.ShortText({
      displayName: "Search Term",
      description: "Search by first name, last name, email or telephone",
      required: !1
    }),
    email: import_pieces_framework11.Property.ShortText({
      displayName: "Email",
      description: "Filter by email address",
      required: !1
    }),
    company_id: import_pieces_framework11.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Company",
      description: "Filter contacts by company",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common11.HttpMethod.POST,
              resourceUri: "/companies.list",
              body: {}
            })).body.data.map((company) => ({
              label: company.name,
              value: company.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading companies"
          };
        }
      }
    }),
    status: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter by contact status",
      required: !1,
      options: {
        options: [
          { label: "Active", value: "active" },
          { label: "Deactivated", value: "deactivated" }
        ]
      }
    }),
    tags: import_pieces_framework11.Property.ShortText({
      displayName: "Tags",
      description: "Filter by tag names (comma-separated)",
      required: !1
    }),
    updated_since: import_pieces_framework11.Property.DateTime({
      displayName: "Updated Since",
      description: "Only contacts updated after this date",
      required: !1
    }),
    sort_field: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Sort Field",
      description: "Field to sort by",
      required: !1,
      options: {
        options: [
          { label: "Name", value: "name" },
          { label: "Date Added", value: "added_at" },
          { label: "Date Updated", value: "updated_at" }
        ]
      }
    }),
    sort_order: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Sort Order",
      description: "Sort direction",
      required: !1,
      options: {
        options: [
          { label: "Ascending (A-Z, Oldest First)", value: "asc" },
          { label: "Descending (Z-A, Newest First)", value: "desc" }
        ]
      }
    }),
    page_size: import_pieces_framework11.Property.Number({
      displayName: "Results Per Page",
      description: "Number of results per page (default: 20)",
      required: !1
    }),
    page_number: import_pieces_framework11.Property.Number({
      displayName: "Page Number",
      description: "Page number to retrieve (default: 1)",
      required: !1
    }),
    include_custom_fields: import_pieces_framework11.Property.Checkbox({
      displayName: "Include Custom Fields",
      description: "Include custom field data in results",
      required: !1
    })
  },
  async run(context) {
    let requestBody = {}, filter = {};
    if (context.propsValue.term && (filter.term = context.propsValue.term), context.propsValue.email && (filter.email = {
      type: "primary",
      email: context.propsValue.email
    }), context.propsValue.company_id && (filter.company_id = context.propsValue.company_id), context.propsValue.status && (filter.status = context.propsValue.status), context.propsValue.tags) {
      let tagList = context.propsValue.tags.split(",").map((tag) => tag.trim()).filter((tag) => tag);
      tagList.length > 0 && (filter.tags = tagList);
    }
    if (context.propsValue.updated_since) {
      let updatedSince = new Date(context.propsValue.updated_since);
      filter.updated_since = updatedSince.toISOString();
    }
    Object.keys(filter).length > 0 && (requestBody.filter = filter);
    let page = {};
    return context.propsValue.page_size && (page.size = context.propsValue.page_size), context.propsValue.page_number && (page.number = context.propsValue.page_number), Object.keys(page).length > 0 && (requestBody.page = page), context.propsValue.sort_field && context.propsValue.sort_order && (requestBody.sort = [{
      field: context.propsValue.sort_field,
      order: context.propsValue.sort_order
    }]), context.propsValue.include_custom_fields && (requestBody.includes = "custom_fields"), (await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common11.HttpMethod.POST,
      resourceUri: "/contacts.list",
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/search-deals.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var searchDeals = (0, import_pieces_framework12.createAction)({
  name: "search_deals",
  displayName: "Search Deals",
  description: "List or filter deals",
  audience: "both",
  aiMetadata: { description: "Look up sales deals in Teamleader, optionally narrowing by a free-text term, customer, phase, status (open/won/lost), responsible user, or closing/updated/created date ranges, with paging and sorting. With no filters it lists all deals; supply filters to find specific matches (e.g. resolve a deal ID before updating). Read-only and idempotent.", idempotent: !0 },
  auth: teamleaderAuth,
  props: {
    term: import_pieces_framework12.Property.ShortText({
      displayName: "Search Term",
      description: "Search by title, reference or customer name",
      required: !1
    }),
    customer_type: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Customer Type",
      description: "Type of customer to filter by",
      required: !1,
      options: {
        options: [
          { label: "Company", value: "company" },
          { label: "Contact", value: "contact" }
        ]
      }
    }),
    customer_id: import_pieces_framework12.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Customer",
      description: "Filter deals by specific customer",
      required: !1,
      refreshers: ["customer_type"],
      options: async ({ auth, customer_type }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        if (!customer_type) return {
          disabled: !0,
          options: [],
          placeholder: "Please select customer type first"
        };
        try {
          return customer_type === "company" ? {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common12.HttpMethod.POST,
              resourceUri: "/companies.list",
              body: {}
            })).body.data.map((company) => ({
              label: company.name,
              value: company.id
            }))
          } : {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common12.HttpMethod.POST,
              resourceUri: "/contacts.list",
              body: {}
            })).body.data.map((contact) => ({
              label: `${contact.first_name} ${contact.last_name || ""}`.trim(),
              value: contact.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading customers"
          };
        }
      }
    }),
    phase_id: import_pieces_framework12.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Deal Phase",
      description: "Filter by deal phase or stage",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common12.HttpMethod.POST,
              resourceUri: "/dealPhases.list",
              body: {}
            })).body.data.map((phase) => ({
              label: phase.name,
              value: phase.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading phases"
          };
        }
      }
    }),
    status: import_pieces_framework12.Property.StaticMultiSelectDropdown({
      displayName: "Status",
      description: "Filter by deal status (multiple selection)",
      required: !1,
      options: {
        options: [
          { label: "Open", value: "open" },
          { label: "Won", value: "won" },
          { label: "Lost", value: "lost" }
        ]
      }
    }),
    responsible_user_id: import_pieces_framework12.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Responsible User",
      description: "Filter by user responsible for deals",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common12.HttpMethod.POST,
              resourceUri: "/users.list",
              body: {
                filter: {
                  status: ["active"]
                }
              }
            })).body.data.map((user) => ({
              label: `${user.first_name} ${user.last_name}`,
              value: user.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading users"
          };
        }
      }
    }),
    estimated_closing_date: import_pieces_framework12.Property.DateTime({
      displayName: "Exact Closing Date",
      description: "Filter by specific estimated closing date",
      required: !1
    }),
    estimated_closing_date_from: import_pieces_framework12.Property.DateTime({
      displayName: "Closing Date From",
      description: "Filter deals closing after this date (inclusive)",
      required: !1
    }),
    estimated_closing_date_until: import_pieces_framework12.Property.DateTime({
      displayName: "Closing Date Until",
      description: "Filter deals closing before this date (inclusive)",
      required: !1
    }),
    updated_since: import_pieces_framework12.Property.DateTime({
      displayName: "Updated Since",
      description: "Only deals updated after this date (inclusive)",
      required: !1
    }),
    created_before: import_pieces_framework12.Property.DateTime({
      displayName: "Created Before",
      description: "Only deals created before this date (inclusive)",
      required: !1
    }),
    sort_field: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Sort Field",
      description: "Field to sort by",
      required: !1,
      options: {
        options: [
          { label: "Created Date", value: "created_at" },
          { label: "Weighted Value", value: "weighted_value" }
        ]
      }
    }),
    sort_order: import_pieces_framework12.Property.StaticDropdown({
      displayName: "Sort Order",
      description: "Sort direction",
      required: !1,
      options: {
        options: [
          { label: "Ascending (Oldest First, Lowest Value)", value: "asc" },
          { label: "Descending (Newest First, Highest Value)", value: "desc" }
        ]
      }
    }),
    page_size: import_pieces_framework12.Property.Number({
      displayName: "Results Per Page",
      description: "Number of results per page (default: 20)",
      required: !1
    }),
    page_number: import_pieces_framework12.Property.Number({
      displayName: "Page Number",
      description: "Page number to retrieve (default: 1)",
      required: !1
    }),
    include_custom_fields: import_pieces_framework12.Property.Checkbox({
      displayName: "Include Custom Fields",
      description: "Include custom field data in results",
      required: !1
    })
  },
  async run(context) {
    let requestBody = {}, filter = {};
    if (context.propsValue.term && (filter.term = context.propsValue.term), context.propsValue.customer_type && context.propsValue.customer_id && (filter.customer = {
      type: context.propsValue.customer_type,
      id: context.propsValue.customer_id
    }), context.propsValue.phase_id && (filter.phase_id = context.propsValue.phase_id), context.propsValue.status && context.propsValue.status.length > 0 && (filter.status = context.propsValue.status), context.propsValue.responsible_user_id && (filter.responsible_user_id = context.propsValue.responsible_user_id), context.propsValue.estimated_closing_date) {
      let closingDate = new Date(context.propsValue.estimated_closing_date);
      filter.estimated_closing_date = closingDate.toISOString().split("T")[0];
    }
    if (context.propsValue.estimated_closing_date_from) {
      let fromDate = new Date(context.propsValue.estimated_closing_date_from);
      filter.estimated_closing_date_from = fromDate.toISOString().split("T")[0];
    }
    if (context.propsValue.estimated_closing_date_until) {
      let untilDate = new Date(context.propsValue.estimated_closing_date_until);
      filter.estimated_closing_date_until = untilDate.toISOString().split("T")[0];
    }
    if (context.propsValue.updated_since) {
      let updatedSince = new Date(context.propsValue.updated_since);
      filter.updated_since = updatedSince.toISOString();
    }
    if (context.propsValue.created_before) {
      let createdBefore = new Date(context.propsValue.created_before);
      filter.created_before = createdBefore.toISOString();
    }
    Object.keys(filter).length > 0 && (requestBody.filter = filter);
    let page = {};
    return context.propsValue.page_size && (page.size = context.propsValue.page_size), context.propsValue.page_number && (page.number = context.propsValue.page_number), Object.keys(page).length > 0 && (requestBody.page = page), context.propsValue.sort_field && context.propsValue.sort_order && (requestBody.sort = [{
      field: context.propsValue.sort_field,
      order: context.propsValue.sort_order
    }]), context.propsValue.include_custom_fields && (requestBody.includes = "custom_fields"), (await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common12.HttpMethod.POST,
      resourceUri: "/deals.list",
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/actions/search-invoices.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var searchInvoices = (0, import_pieces_framework13.createAction)({
  name: "search_invoices",
  displayName: "Search Invoices",
  description: "List or filter invoices",
  audience: "both",
  aiMetadata: { description: "Look up invoices in Teamleader, optionally narrowing by a free-text term, invoice number, customer, department, associated deal/project/subscription, status (draft/outstanding/matched), purchase order or payment reference, or invoice-date/updated date ranges, with paging and sorting. With no filters it lists all invoices; supply filters to find specific matches. Read-only and idempotent.", idempotent: !0 },
  auth: teamleaderAuth,
  props: {
    term: import_pieces_framework13.Property.ShortText({
      displayName: "Search Term",
      description: "Search by invoice number, purchase order number, payment reference or customer name",
      required: !1
    }),
    invoice_number: import_pieces_framework13.Property.ShortText({
      displayName: "Invoice Number",
      description: 'Filter by full invoice number (e.g., "2017 / 5")',
      required: !1
    }),
    customer_type: import_pieces_framework13.Property.StaticDropdown({
      displayName: "Customer Type",
      description: "Type of customer to filter by",
      required: !1,
      options: {
        options: [
          { label: "Company", value: "company" },
          { label: "Contact", value: "contact" }
        ]
      }
    }),
    customer_id: import_pieces_framework13.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Customer",
      description: "Filter invoices by specific customer",
      required: !1,
      refreshers: ["customer_type"],
      options: async ({ auth, customer_type }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        if (!customer_type) return {
          disabled: !0,
          options: [],
          placeholder: "Please select customer type first"
        };
        try {
          return customer_type === "company" ? {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common13.HttpMethod.POST,
              resourceUri: "/companies.list",
              body: {}
            })).body.data.map((company) => ({
              label: company.name,
              value: company.id
            }))
          } : {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common13.HttpMethod.POST,
              resourceUri: "/contacts.list",
              body: {}
            })).body.data.map((contact) => ({
              label: `${contact.first_name} ${contact.last_name || ""}`.trim(),
              value: contact.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading customers"
          };
        }
      }
    }),
    department_id: import_pieces_framework13.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Department",
      description: "Filter by department (company entity)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common13.HttpMethod.POST,
              resourceUri: "/departments.list",
              body: {
                filter: {
                  status: ["active"]
                }
              }
            })).body.data.map((department) => ({
              label: department.name,
              value: department.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading departments"
          };
        }
      }
    }),
    deal_id: import_pieces_framework13.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Deal",
      description: "Filter by associated deal",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common13.HttpMethod.POST,
              resourceUri: "/deals.list",
              body: {}
            })).body.data.map((deal) => ({
              label: deal.title,
              value: deal.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading deals"
          };
        }
      }
    }),
    project_id: import_pieces_framework13.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Project",
      description: "Filter by associated project",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common13.HttpMethod.POST,
              resourceUri: "/projects.list",
              body: {}
            })).body.data.map((project) => ({
              label: project.title,
              value: project.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading projects"
          };
        }
      }
    }),
    subscription_id: import_pieces_framework13.Property.Dropdown({
      auth: teamleaderAuth,
      displayName: "Subscription",
      description: "Filter by associated subscription",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return {
          disabled: !0,
          options: [],
          placeholder: "Please authenticate first"
        };
        try {
          return {
            disabled: !1,
            options: (await teamleaderCommon.apiCall({
              auth,
              method: import_pieces_common13.HttpMethod.POST,
              resourceUri: "/subscriptions.list",
              body: {}
            })).body.data.map((subscription) => ({
              label: subscription.title || subscription.reference,
              value: subscription.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading subscriptions"
          };
        }
      }
    }),
    status: import_pieces_framework13.Property.StaticMultiSelectDropdown({
      displayName: "Status",
      description: "Filter by invoice status (multiple selection)",
      required: !1,
      options: {
        options: [
          { label: "Draft", value: "draft" },
          { label: "Outstanding", value: "outstanding" },
          { label: "Matched", value: "matched" }
        ]
      }
    }),
    purchase_order_number: import_pieces_framework13.Property.ShortText({
      displayName: "Purchase Order Number",
      description: "Filter by purchase order number",
      required: !1
    }),
    payment_reference: import_pieces_framework13.Property.ShortText({
      displayName: "Payment Reference",
      description: "Filter by payment reference",
      required: !1
    }),
    invoice_date_after: import_pieces_framework13.Property.DateTime({
      displayName: "Invoice Date After",
      description: "Filter invoices with date after this date (inclusive)",
      required: !1
    }),
    invoice_date_before: import_pieces_framework13.Property.DateTime({
      displayName: "Invoice Date Before",
      description: "Filter invoices with date before this date (inclusive)",
      required: !1
    }),
    updated_since: import_pieces_framework13.Property.DateTime({
      displayName: "Updated Since",
      description: "Only invoices updated after this date",
      required: !1
    }),
    sort_field: import_pieces_framework13.Property.StaticDropdown({
      displayName: "Sort Field",
      description: "Field to sort by",
      required: !1,
      options: {
        options: [
          { label: "Invoice Number", value: "invoice_number" },
          { label: "Invoice Date", value: "invoice_date" }
        ]
      }
    }),
    sort_order: import_pieces_framework13.Property.StaticDropdown({
      displayName: "Sort Order",
      description: "Sort direction",
      required: !1,
      options: {
        options: [
          { label: "Ascending", value: "asc" },
          { label: "Descending", value: "desc" }
        ]
      }
    }),
    page_size: import_pieces_framework13.Property.Number({
      displayName: "Results Per Page",
      description: "Number of results per page (default: 20)",
      required: !1
    }),
    page_number: import_pieces_framework13.Property.Number({
      displayName: "Page Number",
      description: "Page number to retrieve (default: 1)",
      required: !1
    }),
    include_late_fees: import_pieces_framework13.Property.Checkbox({
      displayName: "Include Late Fee Information",
      description: "Include late fee and interest calculations in response",
      required: !1
    })
  },
  async run(context) {
    let requestBody = {}, filter = {};
    if (context.propsValue.term && (filter.term = context.propsValue.term), context.propsValue.invoice_number && (filter.invoice_number = context.propsValue.invoice_number), context.propsValue.customer_type && context.propsValue.customer_id && (filter.customer = {
      type: context.propsValue.customer_type,
      id: context.propsValue.customer_id
    }), context.propsValue.department_id && (filter.department_id = context.propsValue.department_id), context.propsValue.deal_id && (filter.deal_id = context.propsValue.deal_id), context.propsValue.project_id && (filter.project_id = context.propsValue.project_id), context.propsValue.subscription_id && (filter.subscription_id = context.propsValue.subscription_id), context.propsValue.status && context.propsValue.status.length > 0 && (filter.status = context.propsValue.status), context.propsValue.purchase_order_number && (filter.purchase_order_number = context.propsValue.purchase_order_number), context.propsValue.payment_reference && (filter.payment_reference = context.propsValue.payment_reference), context.propsValue.invoice_date_after) {
      let afterDate = new Date(context.propsValue.invoice_date_after);
      filter.invoice_date_after = afterDate.toISOString().split("T")[0];
    }
    if (context.propsValue.invoice_date_before) {
      let beforeDate = new Date(context.propsValue.invoice_date_before);
      filter.invoice_date_before = beforeDate.toISOString().split("T")[0];
    }
    if (context.propsValue.updated_since) {
      let updatedSince = new Date(context.propsValue.updated_since);
      filter.updated_since = updatedSince.toISOString();
    }
    Object.keys(filter).length > 0 && (requestBody.filter = filter);
    let page = {};
    return context.propsValue.page_size && (page.size = context.propsValue.page_size), context.propsValue.page_number && (page.number = context.propsValue.page_number), Object.keys(page).length > 0 && (requestBody.page = page), context.propsValue.sort_field && context.propsValue.sort_order && (requestBody.sort = [{
      field: context.propsValue.sort_field,
      order: context.propsValue.sort_order
    }]), context.propsValue.include_late_fees && (requestBody.includes = "late_fees"), (await teamleaderCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common13.HttpMethod.POST,
      resourceUri: "/invoices.list",
      body: requestBody
    })).body;
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/triggers/new-contact.ts
var import_pieces_common14 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework");
var newContact = (0, import_pieces_framework14.createTrigger)({
  name: "new_contact",
  displayName: "New Contact",
  description: "Triggers when a new contact is created",
  aiMetadata: {
    description: "Fires when a new contact (an individual person) is added in Teamleader Focus CRM, via the contact.added webhook. Emits the full contact record. Use to react to newly created people, such as onboarding or enrichment flows."
  },
  auth: teamleaderAuth,
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    id: "12345678-abcd-1234-5678-1234567890ab",
    first_name: "John",
    last_name: "Doe",
    emails: [
      {
        type: "primary",
        email: "john.doe@example.com"
      }
    ],
    telephones: [
      {
        type: "mobile",
        number: "+1234567890"
      }
    ],
    website: "https://www.example.com",
    addresses: [
      {
        type: "primary",
        address: {
          line_1: "123 Main St",
          postal_code: "12345",
          city: "New York",
          country: "US"
        }
      }
    ],
    language: "en",
    gender: "male",
    birthdate: "1980-01-01",
    tags: ["customer", "new"],
    created_at: "2023-07-27T10:00:00+00:00",
    updated_at: "2023-07-27T10:00:00+00:00"
  },
  onEnable: async (context) => {
    let webhookUrl = context.webhookUrl, request = {
      method: import_pieces_common14.HttpMethod.POST,
      url: `${teamleaderCommon.baseUrl}/webhooks.register`,
      body: {
        url: webhookUrl,
        types: ["contact.added"]
      },
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    }, { status } = await import_pieces_common14.httpClient.sendRequest(request);
    if (status !== 204)
      throw new Error(`Failed to register webhook. Status: ${status}`);
  },
  onDisable: async (context) => {
    let webhookUrl = context.webhookUrl, request = {
      method: import_pieces_common14.HttpMethod.POST,
      url: `${teamleaderCommon.baseUrl}/webhooks.unregister`,
      body: {
        url: webhookUrl,
        types: ["contact.added"]
      },
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    };
    try {
      await import_pieces_common14.httpClient.sendRequest(request);
    } catch (error) {
      console.warn("Failed to unregister webhook:", error);
    }
  },
  run: async (context) => {
    let payload = context.payload.body;
    if (payload != null && payload.id)
      try {
        return [(await teamleaderCommon.apiCall({
          auth: context.auth,
          method: import_pieces_common14.HttpMethod.POST,
          resourceUri: "/contacts.info",
          body: { id: payload.id }
        })).body.data];
      } catch (error) {
        return [payload];
      }
    return [payload];
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/triggers/new-company.ts
var import_pieces_common15 = require("@activepieces/pieces-common"), import_pieces_framework15 = require("@activepieces/pieces-framework");
var newCompany = (0, import_pieces_framework15.createTrigger)({
  name: "new_company",
  displayName: "New Company",
  description: "Triggers when a new company is created",
  aiMetadata: {
    description: "Fires when a new company (business entity) is added in Teamleader Focus CRM, via the company.added webhook. Emits the full company record. Use to react to newly created organizations, such as onboarding or enrichment flows."
  },
  auth: teamleaderAuth,
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    id: "12345678-abcd-1234-5678-1234567890ef",
    name: "Acme Corporation",
    business_type: {
      id: "be123456-1234-1234-1234-123456789012",
      name: "Limited Company"
    },
    vat_number: "BE0123456789",
    national_identification_number: "123456789",
    emails: [
      {
        type: "invoicing",
        email: "billing@acme.com"
      }
    ],
    telephones: [
      {
        type: "phone",
        number: "+32 2 123 45 67"
      }
    ],
    website: "https://www.acme.com",
    addresses: [
      {
        type: "invoicing",
        address: {
          line_1: "123 Business Street",
          postal_code: "1000",
          city: "Brussels",
          country: "BE"
        }
      }
    ],
    iban: "BE71 0961 2345 6769",
    bic: "GKCCBEBB",
    language: "en",
    responsible_user: {
      type: "user",
      id: "12345678-1234-1234-1234-123456789012"
    },
    tags: ["prospect", "enterprise"],
    remarks: "Important enterprise client",
    created_at: "2023-07-27T10:00:00+00:00",
    updated_at: "2023-07-27T10:00:00+00:00"
  },
  onEnable: async (context) => {
    let webhookUrl = context.webhookUrl, request = {
      method: import_pieces_common15.HttpMethod.POST,
      url: `${teamleaderCommon.baseUrl}/webhooks.register`,
      body: {
        url: webhookUrl,
        types: ["company.added"]
      },
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    }, { status } = await import_pieces_common15.httpClient.sendRequest(request);
    if (status !== 204)
      throw new Error(`Failed to register webhook. Status: ${status}`);
  },
  onDisable: async (context) => {
    let webhookUrl = context.webhookUrl, request = {
      method: import_pieces_common15.HttpMethod.POST,
      url: `${teamleaderCommon.baseUrl}/webhooks.unregister`,
      body: {
        url: webhookUrl,
        types: ["company.added"]
      },
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    };
    try {
      await import_pieces_common15.httpClient.sendRequest(request);
    } catch (error) {
      console.warn("Failed to unregister webhook:", error);
    }
  },
  run: async (context) => {
    let payload = context.payload.body;
    if (payload != null && payload.id)
      try {
        return [(await teamleaderCommon.apiCall({
          auth: context.auth,
          method: import_pieces_common15.HttpMethod.POST,
          resourceUri: "/companies.info",
          body: { id: payload.id }
        })).body.data];
      } catch (error) {
        return [payload];
      }
    return [payload];
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/triggers/new-deal.ts
var import_pieces_common16 = require("@activepieces/pieces-common"), import_pieces_framework16 = require("@activepieces/pieces-framework");
var newDeal = (0, import_pieces_framework16.createTrigger)({
  name: "new_deal",
  displayName: "New Deal",
  description: "Triggers when a new deal is created",
  aiMetadata: {
    description: "Fires when a new sales deal/opportunity is created in Teamleader, via the deal.created webhook. Emits the full deal record. Use to react to newly opened pipeline opportunities; this fires on creation regardless of phase or outcome (use Deal Won for the won event)."
  },
  auth: teamleaderAuth,
  type: import_pieces_framework16.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    id: "12345678-abcd-1234-5678-1234567890cd",
    title: "New Software Implementation",
    reference: "DEA-2025-001",
    status: {
      id: "open",
      name: "Open"
    },
    lead: {
      type: "contact",
      id: "43a6abc5-7fe1-0fa7-942f-85cc4ae5367d"
    },
    department: {
      type: "department",
      id: "45985439-58ce-02df-2542-9dfe87ee1a39"
    },
    responsible_user: {
      type: "user",
      id: "12345678-1234-1234-1234-123456789012"
    },
    phase: {
      type: "dealPhase",
      id: "5296a95e-1870-01af-9f3f-fb3ef990093e"
    },
    estimated_closing_date: "2025-09-30",
    estimated_value: {
      amount: 25e3,
      currency: "EUR"
    },
    created_at: "2025-07-25T10:15:30+00:00",
    updated_at: "2025-07-25T10:15:30+00:00",
    source: {
      type: "dealSource",
      id: "12345678-abcd-1234-5678-1234567890ab"
    },
    weighted_value: {
      amount: 12500,
      currency: "EUR"
    },
    probability: 50
  },
  onEnable: async (context) => {
    let webhookUrl = context.webhookUrl, request = {
      method: import_pieces_common16.HttpMethod.POST,
      url: `${teamleaderCommon.baseUrl}/webhooks.register`,
      body: {
        url: webhookUrl,
        types: ["deal.created"]
      },
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    }, { status } = await import_pieces_common16.httpClient.sendRequest(request);
    if (status !== 204)
      throw new Error(`Failed to register webhook. Status: ${status}`);
  },
  onDisable: async (context) => {
    let webhookUrl = context.webhookUrl, request = {
      method: import_pieces_common16.HttpMethod.POST,
      url: `${teamleaderCommon.baseUrl}/webhooks.unregister`,
      body: {
        url: webhookUrl,
        types: ["deal.created"]
      },
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    };
    try {
      await import_pieces_common16.httpClient.sendRequest(request);
    } catch (error) {
      console.warn("Failed to unregister webhook:", error);
    }
  },
  run: async (context) => {
    let payload = context.payload.body;
    if (payload != null && payload.id)
      try {
        return [(await teamleaderCommon.apiCall({
          auth: context.auth,
          method: import_pieces_common16.HttpMethod.POST,
          resourceUri: "/deals.info",
          body: { id: payload.id }
        })).body.data];
      } catch (error) {
        return [payload];
      }
    return [payload];
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/triggers/deal-accepted.ts
var import_pieces_common17 = require("@activepieces/pieces-common"), import_pieces_framework17 = require("@activepieces/pieces-framework");
var dealAccepted = (0, import_pieces_framework17.createTrigger)({
  name: "deal_accepted",
  displayName: "Deal Won",
  description: "Triggers when a deal is won/accepted",
  aiMetadata: {
    description: "Fires when an existing deal is marked won/accepted in Teamleader, via the deal.won webhook. Emits the full deal record with won status. Use to react to closed-won opportunities, such as kicking off fulfillment or invoicing; for any new deal regardless of outcome use New Deal instead."
  },
  auth: teamleaderAuth,
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    id: "12345678-abcd-1234-5678-1234567890cd",
    title: "Enterprise Software Deal",
    reference: "DEA-2025-002",
    status: {
      id: "won",
      name: "Won"
    },
    lead: {
      type: "contact",
      id: "43a6abc5-7fe1-0fa7-942f-85cc4ae5367d"
    },
    department: {
      type: "department",
      id: "45985439-58ce-02df-2542-9dfe87ee1a39"
    },
    responsible_user: {
      type: "user",
      id: "12345678-1234-1234-1234-123456789012"
    },
    phase: {
      type: "dealPhase",
      id: "5296a95e-1870-01af-9f3f-fb3ef990093e"
    },
    estimated_closing_date: "2025-09-30",
    estimated_value: {
      amount: 25e3,
      currency: "EUR"
    },
    created_at: "2025-07-25T10:15:30+00:00",
    updated_at: "2025-07-25T15:45:20+00:00",
    closed_at: "2025-07-25T15:45:20+00:00",
    source: {
      type: "dealSource",
      id: "12345678-abcd-1234-5678-1234567890ab"
    },
    weighted_value: {
      amount: 25e3,
      currency: "EUR"
    },
    probability: 100
  },
  onEnable: async (context) => {
    let webhookUrl = context.webhookUrl, request = {
      method: import_pieces_common17.HttpMethod.POST,
      url: `${teamleaderCommon.baseUrl}/webhooks.register`,
      body: {
        url: webhookUrl,
        types: ["deal.won"]
      },
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    }, { status } = await import_pieces_common17.httpClient.sendRequest(request);
    if (status !== 204)
      throw new Error(`Failed to register webhook. Status: ${status}`);
  },
  onDisable: async (context) => {
    let webhookUrl = context.webhookUrl, request = {
      method: import_pieces_common17.HttpMethod.POST,
      url: `${teamleaderCommon.baseUrl}/webhooks.unregister`,
      body: {
        url: webhookUrl,
        types: ["deal.won"]
      },
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    };
    try {
      await import_pieces_common17.httpClient.sendRequest(request);
    } catch (error) {
      console.warn("Failed to unregister webhook:", error);
    }
  },
  run: async (context) => {
    let payload = context.payload.body;
    if (payload != null && payload.id)
      try {
        return [(await teamleaderCommon.apiCall({
          auth: context.auth,
          method: import_pieces_common17.HttpMethod.POST,
          resourceUri: "/deals.info",
          body: { id: payload.id }
        })).body.data];
      } catch (error) {
        return [payload];
      }
    return [payload];
  }
});

// ../ap/packages/pieces/community/teamleader/src/lib/triggers/new-invoice.ts
var import_pieces_common18 = require("@activepieces/pieces-common"), import_pieces_framework18 = require("@activepieces/pieces-framework");
var newInvoice = (0, import_pieces_framework18.createTrigger)({
  name: "new_invoice",
  displayName: "New Invoice",
  description: "Triggers when a new invoice is drafted",
  aiMetadata: {
    description: "Fires when a new invoice is drafted in Teamleader, via the invoice.drafted webhook. Emits the full invoice record (in draft status). Use to react to newly created invoices, such as syncing to accounting or notifying finance."
  },
  auth: teamleaderAuth,
  type: import_pieces_framework18.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData: {
    id: "12345678-abcd-1234-5678-1234567890gh",
    invoice_number: "2025 / 001",
    invoice_date: "2025-01-15",
    status: "draft",
    due_on: "2025-02-14",
    paid: !1,
    paid_at: null,
    sent: !1,
    purchase_order_number: "PO-2025-001",
    payment_reference: "+++084/2613/66074+++",
    invoicee: {
      type: "company",
      id: "12345678-abcd-1234-5678-1234567890ef"
    },
    department: {
      type: "department",
      id: "45985439-58ce-02df-2542-9dfe87ee1a39"
    },
    total: {
      amount: 2500,
      currency: "EUR"
    },
    currency_exchange_rate: {
      from: "EUR",
      to: "EUR",
      rate: 1
    },
    deal: {
      type: "deal",
      id: "12345678-abcd-1234-5678-1234567890cd"
    },
    project: null,
    created_at: "2025-01-15T10:30:00+00:00",
    updated_at: "2025-01-15T10:30:00+00:00",
    web_url: "https://focus.teamleader.eu/invoice_detail.php?id=12345678-abcd-1234-5678-1234567890gh"
  },
  onEnable: async (context) => {
    let webhookUrl = context.webhookUrl, request = {
      method: import_pieces_common18.HttpMethod.POST,
      url: `${teamleaderCommon.baseUrl}/webhooks.register`,
      body: {
        url: webhookUrl,
        types: ["invoice.drafted"]
      },
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    }, { status } = await import_pieces_common18.httpClient.sendRequest(request);
    if (status !== 204)
      throw new Error(`Failed to register webhook. Status: ${status}`);
  },
  onDisable: async (context) => {
    let webhookUrl = context.webhookUrl, request = {
      method: import_pieces_common18.HttpMethod.POST,
      url: `${teamleaderCommon.baseUrl}/webhooks.unregister`,
      body: {
        url: webhookUrl,
        types: ["invoice.drafted"]
      },
      headers: {
        Authorization: `Bearer ${context.auth.access_token}`,
        "Content-Type": "application/json"
      }
    };
    try {
      await import_pieces_common18.httpClient.sendRequest(request);
    } catch (error) {
      console.warn("Failed to unregister webhook:", error);
    }
  },
  run: async (context) => {
    let payload = context.payload.body;
    if (payload != null && payload.id)
      try {
        return [(await teamleaderCommon.apiCall({
          auth: context.auth,
          method: import_pieces_common18.HttpMethod.POST,
          resourceUri: "/invoices.info",
          body: { id: payload.id }
        })).body.data];
      } catch (error) {
        return [payload];
      }
    return [payload];
  }
});

// ../ap/packages/pieces/community/teamleader/src/index.ts
var teamleader = (0, import_pieces_framework19.createPiece)({
  displayName: "Teamleader",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/teamleader.png",
  categories: [import_pieces_framework20.PieceCategory.SALES_AND_CRM, import_pieces_framework20.PieceCategory.BUSINESS_INTELLIGENCE],
  authors: ["owuzo", "onyedikachi-david"],
  auth: teamleaderAuth,
  actions: [
    createContact,
    updateContact,
    createCompany,
    updateCompany,
    linkContactToCompany,
    unlinkContactFromCompany,
    createDeal,
    updateDeal,
    searchCompanies,
    searchContacts,
    searchDeals,
    searchInvoices,
    (0, import_pieces_common19.createCustomApiCallAction)({
      baseUrl: () => "https://api.focus.teamleader.eu",
      auth: teamleaderAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [newContact, newCompany, newDeal, dealAccepted, newInvoice]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  teamleader
});
