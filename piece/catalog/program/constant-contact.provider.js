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

// ../ap/packages/pieces/community/constant-contact/src/index.ts
var index_exports = {};
__export(index_exports, {
  constantContact: () => constantContact
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/constant-contact/src/lib/actions/create-or-update-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/constant-contact/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), constantContactAuth = import_pieces_framework.PieceAuth.OAuth2({
  required: !0,
  tokenUrl: "https://authz.constantcontact.com/oauth2/default/v1/token",
  authUrl: "https://authz.constantcontact.com/oauth2/default/v1/authorize",
  scope: ["contact_data"]
});

// ../ap/packages/pieces/community/constant-contact/src/lib/actions/create-or-update-contact.ts
var createOrUpdateContact = (0, import_pieces_framework2.createAction)({
  auth: constantContactAuth,
  name: "create_or_update_contact",
  displayName: "Create or Update Contact",
  description: "Create or Update a contact in Constant Contact",
  audience: "both",
  aiMetadata: { description: "Creates a contact in Constant Contact or updates the existing one matching the email address, and adds it to one or more contact lists. Use this to add subscribers or keep contact details and list memberships in sync. The email address is required and is the key the upsert matches on, so repeating the call with the same email updates rather than duplicates.", idempotent: !0 },
  props: {
    list: import_pieces_framework2.Property.MultiSelectDropdown({
      auth: constantContactAuth,
      displayName: "List",
      description: "The list of the contact",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => auth ? {
        placeholder: "Select a list",
        disabled: !1,
        options: (await import_pieces_common.httpClient.sendRequest({
          url: "https://api.cc.email/v3/contact_lists",
          method: import_pieces_common.HttpMethod.GET,
          authentication: {
            type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
            token: auth.access_token
          }
        })).body.lists.map((list) => ({
          value: list.list_id,
          label: list.name
        }))
      } : {
        options: [],
        placeholder: "Connect to Constant Contact to see the lists",
        disabled: !0
      }
    }),
    email_address: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "The email of the contact",
      required: !0
    }),
    first_name: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "The first name of the contact",
      required: !1
    }),
    last_name: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "The last name of the contact",
      required: !1
    }),
    job_title: import_pieces_framework2.Property.ShortText({
      displayName: "Job Title",
      description: "The job title of the contact",
      required: !1
    }),
    company_name: import_pieces_framework2.Property.ShortText({
      displayName: "Company Name",
      description: "The company name of the contact",
      required: !1
    }),
    phoner_number: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Number",
      description: "The phone number of the contact",
      required: !1
    })
  },
  run: async ({ auth, propsValue }) => (await import_pieces_common.httpClient.sendRequest({
    url: "https://api.cc.email/v3/contacts/sign_up_form",
    method: import_pieces_common.HttpMethod.POST,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: auth.access_token
    },
    body: {
      email_address: propsValue.email_address,
      first_name: propsValue.first_name,
      last_name: propsValue.last_name,
      job_title: propsValue.job_title,
      company_name: propsValue.company_name,
      phoner_number: propsValue.phoner_number,
      list_memberships: propsValue.list
    }
  })).body
});

// ../ap/packages/pieces/community/constant-contact/src/index.ts
var constantContact = (0, import_pieces_framework3.createPiece)({
  displayName: "Constant Contact",
  description: "Email marketing for small businesses",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/constant-contact.png",
  categories: [import_pieces_framework4.PieceCategory.MARKETING],
  authors: ["kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  auth: constantContactAuth,
  actions: [
    createOrUpdateContact,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://api.cc.email/v3",
      // Replace with the actual base URL
      auth: constantContactAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  constantContact
});
