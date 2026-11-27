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

// ../ap/packages/pieces/community/freshsales/src/index.ts
var index_exports = {};
__export(index_exports, {
  freshsales: () => freshsales
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/freshsales/src/lib/actions/create-contact.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/freshsales/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = `
To obtain your API key and bundle alias, follow these steps:

1. Log in to your Freshsales account.
2. Click on your profile icon in the top-right corner of the screen and select **Settings** from the dropdown menu.
3. In the settings menu, select **API Settings** from the left-hand navigation panel.
4. You should now see your API key displayed on the screen. If you don't see an API key.
5. Copy the alias e.g **https://<alias>.myfreshworks.com**
`, freshsalesAuth = import_pieces_framework.PieceAuth.BasicAuth({
  description: markdownDescription,
  username: import_pieces_framework.Property.ShortText({
    displayName: "Bundle alias",
    description: "Your Freshsales bundle alias (e.g. https://<alias>.myfreshworks.com)",
    required: !0
  }),
  password: import_pieces_framework.Property.ShortText({
    displayName: "API Key",
    description: "The API Key supplied by Freshsales",
    required: !0
  }),
  required: !0
});

// ../ap/packages/pieces/community/freshsales/src/lib/actions/create-contact.ts
var freshSalesCreateContact = (0, import_pieces_framework2.createAction)({
  auth: freshsalesAuth,
  name: "freshsales_create_contact",
  displayName: "Create Contact",
  description: "Add new contact in Freshsales CRM",
  audience: "both",
  aiMetadata: {
    description: "Creates a new contact record in Freshsales CRM with the given details (email is required; name, phone, address, social handles, owner/territory/account IDs, and marketing attribution fields are optional). Use when an agent needs to add a person to the CRM. Not idempotent: each call creates a new contact and may produce duplicates if repeated.",
    idempotent: !1
  },
  props: {
    first_name: import_pieces_framework2.Property.ShortText({
      displayName: "First name",
      description: "First name of the contact",
      required: !1
    }),
    last_name: import_pieces_framework2.Property.ShortText({
      displayName: "Last name",
      description: "Last name of the contact",
      required: !1
    }),
    job_title: import_pieces_framework2.Property.ShortText({
      displayName: "Job title",
      description: "Designation of the contact in the account they belong to",
      required: !1
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "Primary email address of the contact",
      required: !0
    }),
    work_number: import_pieces_framework2.Property.ShortText({
      displayName: "Work number",
      description: "Work phone number of the contact",
      required: !1
    }),
    mobile_number: import_pieces_framework2.Property.ShortText({
      displayName: "Mobile number",
      description: "Mobile phone number of the contact",
      required: !1
    }),
    address: import_pieces_framework2.Property.ShortText({
      displayName: "Address",
      description: "Address of the contact",
      required: !1
    }),
    city: import_pieces_framework2.Property.ShortText({
      displayName: "City",
      description: "City that the contact belongs to",
      required: !1
    }),
    state: import_pieces_framework2.Property.ShortText({
      displayName: "State",
      description: "State that the contact belongs to",
      required: !1
    }),
    zipcode: import_pieces_framework2.Property.ShortText({
      displayName: "Zip code",
      description: "Zipcode of the region that the contact belongs to",
      required: !1
    }),
    country: import_pieces_framework2.Property.ShortText({
      displayName: "Country",
      description: "Country that the contact belongs to",
      required: !1
    }),
    territory_id: import_pieces_framework2.Property.ShortText({
      displayName: "Territory id",
      description: "ID of the territory that the contact belongs to",
      required: !1
    }),
    owner_id: import_pieces_framework2.Property.ShortText({
      displayName: "Owner id",
      description: "ID of the user to whom the contact has been assigned",
      required: !1
    }),
    subscription_status: import_pieces_framework2.Property.ShortText({
      displayName: "Subscription status",
      description: "Status of subscription that the contact is in.",
      required: !1
    }),
    medium: import_pieces_framework2.Property.ShortText({
      displayName: "Medium",
      description: "The medium that led your contact to your website/web app",
      required: !1
    }),
    campaign_id: import_pieces_framework2.Property.ShortText({
      displayName: "Campaign id",
      description: "The campaign that led your contact to your web app.",
      required: !1
    }),
    keyword: import_pieces_framework2.Property.ShortText({
      displayName: "Keyword",
      description: "The keywords that the contact used to reach your website/web app",
      required: !1
    }),
    time_zone: import_pieces_framework2.Property.ShortText({
      displayName: "Timezone",
      description: "Timezone that the contact belongs to",
      required: !1
    }),
    facebook: import_pieces_framework2.Property.ShortText({
      displayName: "Facebook",
      description: "Facebook username of the contact",
      required: !1
    }),
    twitter: import_pieces_framework2.Property.ShortText({
      displayName: "Twitter",
      description: "Twitter username of the contact",
      required: !1
    }),
    linkedin: import_pieces_framework2.Property.ShortText({
      displayName: "Linkedin",
      description: "LinkedIn account of the contact",
      required: !1
    }),
    contact_status_id: import_pieces_framework2.Property.ShortText({
      displayName: "Contact status id",
      description: "ID of the contact status that the contact belongs to",
      required: !1
    }),
    sales_account_id: import_pieces_framework2.Property.ShortText({
      displayName: "Sales account id",
      description: "ID of the primary account that the contact belongs to",
      required: !1
    })
  },
  async run(context) {
    let contact = context.propsValue, request = {
      method: import_pieces_common.HttpMethod.POST,
      url: `https://${context.auth.username}.myfreshworks.com/crm/sales/api/contacts`,
      body: {
        contact
      },
      headers: {
        Authorization: `Token token=${context.auth.password}`
      }
    }, result = await import_pieces_common.httpClient.sendRequest(request);
    return result.status == 200 ? result.body : result;
  }
});

// ../ap/packages/pieces/community/freshsales/src/index.ts
var freshsales = (0, import_pieces_framework3.createPiece)({
  displayName: "Freshsales",
  description: "Sales CRM software",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/freshsales.png",
  categories: [import_pieces_framework4.PieceCategory.SALES_AND_CRM],
  authors: ["kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  auth: freshsalesAuth,
  actions: [
    freshSalesCreateContact,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: (auth) => `https://${auth.username}.myfreshworks.com/crm/sales/api`,
      auth: freshsalesAuth,
      authMapping: async (auth) => ({
        Authorization: `Token token=${auth.password}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  freshsales
});
