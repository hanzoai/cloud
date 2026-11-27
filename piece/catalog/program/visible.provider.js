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

// ../ap/packages/pieces/community/visible/src/index.ts
var index_exports = {};
__export(index_exports, {
  visible: () => visible,
  visibleAuth: () => visibleAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/visible/src/lib/actions/create-portfolio-company.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/visible/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), VISIBLE_BASE_URL = "https://api.visible.vc";
async function visibleMakeRequest(params) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: params.method,
    url: `${VISIBLE_BASE_URL}${params.path}`,
    headers: {
      Authorization: `Bearer ${params.accessToken}`,
      "Content-Type": "application/json"
    },
    queryParams: params.queryParams,
    body: params.body
  })).body;
}

// ../ap/packages/pieces/community/visible/src/lib/actions/create-portfolio-company.ts
var createPortfolioCompany = (0, import_pieces_framework.createAction)({
  name: "create_portfolio_company",
  displayName: "Create Portfolio Company",
  description: "Creates a new portfolio company.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new portfolio company profile in Visible. Use to onboard a company before adding its metrics or contacts. Requires a company ID and name; optionally website URL, currency, and a primary contact ID. Not idempotent: this is a create-only operation, so repeating it adds another company profile.",
    idempotent: !1
  },
  auth: visibleAuth,
  props: {
    companyId: import_pieces_framework.Property.ShortText({
      displayName: "Company ID",
      required: !0
    }),
    name: import_pieces_framework.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    websiteUrl: import_pieces_framework.Property.ShortText({
      displayName: "Website URL",
      required: !1
    }),
    currency: import_pieces_framework.Property.ShortText({
      displayName: "Currency",
      required: !1
    }),
    primaryContactId: import_pieces_framework.Property.ShortText({
      displayName: "Primary Contact ID",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let portfolioCompanyProfile = {
      name: propsValue.name,
      company_id: propsValue.companyId
    };
    return propsValue.websiteUrl && (portfolioCompanyProfile.website_url = propsValue.websiteUrl), propsValue.currency && (portfolioCompanyProfile.currency = propsValue.currency), propsValue.primaryContactId && (portfolioCompanyProfile.primary_contact_id = propsValue.primaryContactId), await visibleMakeRequest({
      accessToken: auth.secret_text,
      method: import_pieces_common2.HttpMethod.POST,
      path: "/portfolio_company_profiles",
      body: {
        portfolio_company_profile: portfolioCompanyProfile
      }
    });
  }
});

// ../ap/packages/pieces/community/visible/src/lib/actions/create-or-update-contact.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var createOrUpdateContact = (0, import_pieces_framework2.createAction)({
  name: "create_or_update_contact",
  displayName: "Create or Update Contact",
  description: "Creates a contact (or updates contact based on email address).",
  audience: "both",
  aiMetadata: {
    description: "Upserts a contact in Visible: creates a new contact, or updates the existing one matched on the provided email address. Use to ensure a person exists under a company without first checking. Requires the email and the company ID; optional name, title, and contact list IDs. Idempotent because the email is the stable match key, so repeating the same input converges to the same contact.",
    idempotent: !0
  },
  auth: visibleAuth,
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      required: !0
    }),
    companyId: import_pieces_framework2.Property.ShortText({
      displayName: "Company ID",
      required: !0
    }),
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Title",
      required: !1
    }),
    contactListIds: import_pieces_framework2.Property.Array({
      displayName: "Contact List IDs",
      required: !1,
      defaultValue: []
    })
  },
  async run({ auth, propsValue }) {
    let contact = {
      email: propsValue.email,
      company_id: propsValue.companyId,
      contact_list_ids: Array.isArray(propsValue.contactListIds) ? propsValue.contactListIds : []
    };
    return propsValue.firstName && (contact.first_name = propsValue.firstName), propsValue.lastName && (contact.last_name = propsValue.lastName), propsValue.title && (contact.title = propsValue.title), await visibleMakeRequest({
      accessToken: auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/contacts",
      body: {
        contact
      }
    });
  }
});

// ../ap/packages/pieces/community/visible/src/lib/actions/create-metric.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var createMetric = (0, import_pieces_framework3.createAction)({
  name: "create_metric",
  displayName: "Create Metric",
  description: "Create a new metric for a company.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new metric (a named, recurring data series with a reporting frequency and unit) for a Visible portfolio company. Use to register a KPI before reporting values against it. Requires the target company ID, a name, a frequency (daily, weekly, monthly, or quarterly), and a unit. Not idempotent: each call creates another metric even with identical inputs.",
    idempotent: !1
  },
  auth: visibleAuth,
  props: {
    companyId: import_pieces_framework3.Property.ShortText({
      displayName: "Company ID",
      required: !0
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    frequency: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Frequency",
      required: !0,
      options: {
        options: [
          { label: "Daily", value: "daily" },
          { label: "Weekly", value: "weekly" },
          { label: "Monthly", value: "monthly" },
          { label: "Quarterly", value: "quarterly" }
        ]
      }
    }),
    unit: import_pieces_framework3.Property.ShortText({
      displayName: "Unit",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return await visibleMakeRequest({
      accessToken: auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/metrics",
      body: {
        metric: {
          name: propsValue.name,
          frequency: propsValue.frequency,
          unit: propsValue.unit,
          company_id: propsValue.companyId
        }
      }
    });
  }
});

// ../ap/packages/pieces/community/visible/src/lib/actions/custom-api-call.ts
var import_pieces_common5 = require("@activepieces/pieces-common");
var customApiCall = (0, import_pieces_common5.createCustomApiCallAction)({
  auth: visibleAuth,
  name: "custom_api_call",
  displayName: "Custom API Call",
  description: "Make a custom API call to any Visible endpoint",
  baseUrl: () => "https://api.visible.vc",
  authMapping: async (auth) => ({
    Authorization: `Bearer ${auth.secret_text}`,
    "Content-Type": "application/json"
  })
});

// ../ap/packages/pieces/community/visible/src/lib/actions/index.ts
var visibleActions = [
  createPortfolioCompany,
  createOrUpdateContact,
  createMetric,
  customApiCall
];

// ../ap/packages/pieces/community/visible/src/index.ts
var visibleAuth = import_pieces_framework4.PieceAuth.SecretText({
  displayName: "Access Token",
  required: !0,
  description: "Enter your Visible access token."
}), visible = (0, import_pieces_framework4.createPiece)({
  displayName: "Visible",
  auth: visibleAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/visible.png",
  authors: ["onyedikachi-david"],
  actions: visibleActions,
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  visible,
  visibleAuth
});
