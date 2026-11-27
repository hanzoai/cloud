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

// ../ap/packages/pieces/community/klenty/src/index.ts
var index_exports = {};
__export(index_exports, {
  klenty: () => klenty
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/klenty/src/lib/actions/add-prospect-to-campaign.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/klenty/src/lib/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/klenty/src/lib/common/constants.ts
var KLENTY_API_BASE = "https://app.klenty.com/apis/v1";

// ../ap/packages/pieces/community/klenty/src/lib/auth.ts
async function validateHost(baseUrl, username, apiKey) {
  let response = await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${baseUrl}/user/${encodeURIComponent(username)}/lists`,
    headers: {
      "x-API-key": apiKey,
      api_key: apiKey,
      accept: "application/json"
    }
  });
  return response.status >= 200 && response.status < 300;
}
var klentyAuth = import_pieces_framework.PieceAuth.CustomAuth({
  required: !0,
  description: `
**Generating Klenty API Key**

To access the Klenty REST APIs, you need to authenticate your requests using the Klenty API Key.

1. Login to your Klenty account.
2. Go to **Settings \u2192 Integrations \u2192 Klenty API Key**.
3. Click on the key icon to generate an API Key.
  `,
  props: {
    username: import_pieces_framework.Property.ShortText({
      displayName: "Username / Email",
      description: "The Klenty username or email used in the API URL path (for example: you@company.com).",
      required: !0
    }),
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your Klenty API key.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await validateHost(KLENTY_API_BASE, auth.username, auth.apiKey), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid Klenty credentials. Verify the username/email used in the path and the API key."
      };
    }
  }
});

// ../ap/packages/pieces/community/klenty/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
function getKlentyBaseUrl(username) {
  return `${KLENTY_API_BASE}/user/${encodeURIComponent(username)}`;
}
async function klentyRequest({
  auth,
  method,
  path,
  queryParams,
  body
}) {
  return (await import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${getKlentyBaseUrl(auth.props.username)}${path}`,
    headers: __spreadValues({
      "x-API-key": auth.props.apiKey,
      api_key: auth.props.apiKey,
      accept: "application/json"
    }, body ? { "Content-Type": "application/json" } : {}),
    queryParams,
    body
  })).body;
}
async function findKlentyProspectByEmail({
  auth,
  email
}) {
  let prospect = await klentyRequest({
    auth,
    method: import_pieces_common2.HttpMethod.GET,
    path: "/prospects",
    queryParams: { Email: email }
  });
  return prospect != null ? prospect : null;
}

// ../ap/packages/pieces/community/klenty/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var listNameProp = import_pieces_framework2.Property.Dropdown({
  displayName: "List",
  description: "Select a Klenty list.",
  required: !1,
  refreshers: [],
  auth: klentyAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Klenty account first.",
        options: []
      };
    let lists = await klentyRequest({
      auth,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/lists"
    });
    return {
      disabled: !1,
      options: (lists != null ? lists : []).filter((list) => list.name).map((list) => ({
        label: list.name,
        value: list.name
      }))
    };
  }
}), cadenceNameProp = import_pieces_framework2.Property.Dropdown({
  displayName: "Cadence",
  description: "Select a Klenty cadence.",
  required: !0,
  refreshers: [],
  auth: klentyAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Klenty account first.",
        options: []
      };
    let cadences = await klentyRequest({
      auth,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/cadences"
    });
    return {
      disabled: !1,
      options: (cadences != null ? cadences : []).filter((cadence) => cadence.name).map((cadence) => ({
        label: cadence.name,
        value: cadence.name
      }))
    };
  }
}), prospectCommonProps = {
  company: import_pieces_framework2.Property.ShortText({
    displayName: "Company",
    required: !1
  }),
  fullName: import_pieces_framework2.Property.ShortText({
    displayName: "Full Name",
    required: !1
  }),
  lastName: import_pieces_framework2.Property.ShortText({
    displayName: "Last Name",
    required: !1
  }),
  middleName: import_pieces_framework2.Property.ShortText({
    displayName: "Middle Name",
    required: !1
  }),
  account: import_pieces_framework2.Property.ShortText({
    displayName: "Account",
    required: !1
  }),
  department: import_pieces_framework2.Property.ShortText({
    displayName: "Department",
    required: !1
  }),
  companyDomain: import_pieces_framework2.Property.ShortText({
    displayName: "Company Domain",
    required: !1
  }),
  title: import_pieces_framework2.Property.ShortText({
    displayName: "Title",
    required: !1
  }),
  location: import_pieces_framework2.Property.ShortText({
    displayName: "Location",
    required: !1
  }),
  phone: import_pieces_framework2.Property.ShortText({
    displayName: "Phone",
    required: !1
  }),
  twitterId: import_pieces_framework2.Property.ShortText({
    displayName: "Twitter ID",
    required: !1
  }),
  companyPhone: import_pieces_framework2.Property.ShortText({
    displayName: "Company Phone",
    required: !1
  }),
  companyEmail: import_pieces_framework2.Property.ShortText({
    displayName: "Company Email",
    required: !1
  }),
  linkedinURL: import_pieces_framework2.Property.ShortText({
    displayName: "LinkedIn URL",
    required: !1
  }),
  city: import_pieces_framework2.Property.ShortText({
    displayName: "City",
    required: !1
  }),
  country: import_pieces_framework2.Property.ShortText({
    displayName: "Country",
    required: !1
  }),
  listName: listNameProp,
  tags: import_pieces_framework2.Property.ShortText({
    displayName: "Tags",
    description: "Pipe-separated tag names, for example: Tag1|Tag2",
    required: !1
  }),
  outcome: import_pieces_framework2.Property.ShortText({
    displayName: "Outcome",
    description: "Klenty outcome value. Case-sensitive.",
    required: !1
  }),
  owner: import_pieces_framework2.Property.ShortText({
    displayName: "Owner",
    description: "Email address of the Klenty user to assign as prospect owner.",
    required: !1
  }),
  customFields: import_pieces_framework2.Property.Array({
    displayName: "Custom Fields",
    description: "Key/value pairs to send in Klenty CustomFields.",
    required: !1,
    properties: {
      key: import_pieces_framework2.Property.ShortText({
        displayName: "Key",
        required: !0
      }),
      value: import_pieces_framework2.Property.ShortText({
        displayName: "Value",
        required: !1
      })
    }
  })
};
function buildProspectPayload(input) {
  var _a;
  let customFields = ((_a = input.customFields) != null ? _a : []).filter((field) => typeof field == "object" && field !== null && "key" in field).map((field) => ({
    key: String(field.key),
    value: field.value === void 0 || field.value === null ? "" : String(field.value)
  }));
  return Object.fromEntries(
    Object.entries({
      Email: input.email,
      FirstName: input.firstName,
      Company: input.company,
      FullName: input.fullName,
      LastName: input.lastName,
      MiddleName: input.middleName,
      Account: input.account,
      Department: input.department,
      CompanyDomain: input.companyDomain,
      Title: input.title,
      Location: input.location,
      Phone: input.phone,
      TwitterId: input.twitterId,
      CompanyPhone: input.companyPhone,
      CompanyEmail: input.companyEmail,
      LinkedinURL: input.linkedinURL,
      City: input.city,
      Country: input.country,
      List: input.listName,
      Tags: input.tags,
      Outcome: input.outcome,
      Owner: input.owner,
      CustomFields: customFields.length > 0 ? customFields : void 0
    }).filter(([, value]) => value != null && value !== "")
  );
}

// ../ap/packages/pieces/community/klenty/src/lib/actions/add-prospect-to-campaign.ts
var addProspectToCampaignAction = (0, import_pieces_framework3.createAction)({
  name: "add_prospect_to_campaign",
  displayName: "Add Prospect to Campaign",
  description: "Add a prospect to a Klenty campaign/cadence using the startcadence endpoint.",
  audience: "both",
  aiMetadata: {
    description: "Enrolls a prospect (by email) into a named Klenty cadence/campaign, starting the outreach sequence for them. Use to kick off automated emails to an existing prospect; the prospect should already exist in Klenty and the cadence name must match one configured in the account. Not idempotent: it triggers cadence enrollment as an outbound side effect on each call.",
    idempotent: !1
  },
  auth: klentyAuth,
  props: {
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Prospect Email",
      required: !0
    }),
    cadenceName: cadenceNameProp
  },
  async run({ auth, propsValue }) {
    return await klentyRequest({
      auth,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/startcadence",
      body: {
        Email: propsValue.email,
        cadenceName: propsValue.cadenceName
      }
    });
  }
});

// ../ap/packages/pieces/community/klenty/src/lib/actions/create-prospect.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var createProspectAction = (0, import_pieces_framework4.createAction)({
  name: "create_prospect",
  displayName: "Create Prospect",
  description: "Create a new prospect in Klenty.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new prospect (contact) in Klenty from an email and first name plus optional contact fields. Use to add a lead to a Klenty account before enrolling them in outreach. Not idempotent: each call posts a new prospect, so repeating it may create duplicates \u2014 to modify an existing prospect use Update Prospect instead.",
    idempotent: !1
  },
  auth: klentyAuth,
  props: __spreadValues({
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      required: !0
    }),
    firstName: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      required: !0
    })
  }, prospectCommonProps),
  async run({ auth, propsValue }) {
    return await klentyRequest({
      auth,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/prospects",
      body: buildProspectPayload(propsValue)
    });
  }
});

// ../ap/packages/pieces/community/klenty/src/lib/actions/get-prospect.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var getProspectAction = (0, import_pieces_framework5.createAction)({
  name: "get_prospect",
  displayName: "Get Prospect by Email",
  description: "Find a prospect by email address in Klenty.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a single Klenty prospect by exact email address and returns their record, throwing an error if no prospect matches. Use to read or verify a prospect before updating or enrolling them. Read-only and idempotent.",
    idempotent: !0
  },
  auth: klentyAuth,
  props: {
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      description: "Email address of the prospect to find.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let prospect = await findKlentyProspectByEmail({
      auth,
      email: propsValue.email
    });
    if (!prospect)
      throw new Error(`Prospect ${propsValue.email} was not found in Klenty.`);
    return prospect;
  }
});

// ../ap/packages/pieces/community/klenty/src/lib/actions/update-prospect.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var updateProspectAction = (0, import_pieces_framework6.createAction)({
  name: "update_prospect",
  displayName: "Update Prospect",
  description: "Update an existing prospect in Klenty by email address.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Klenty prospect identified by their current email address, overwriting the supplied contact fields (including optionally a new email). Use to edit a known prospect's details rather than create one. Idempotent: the prospect is keyed on the current email, so repeating the call with the same input yields the same record.",
    idempotent: !0
  },
  auth: klentyAuth,
  props: __spreadValues({
    currentEmail: import_pieces_framework6.Property.ShortText({
      displayName: "Current Prospect Email",
      description: "The current email used to identify the prospect in Klenty.",
      required: !0
    }),
    email: import_pieces_framework6.Property.ShortText({
      displayName: "New Email",
      required: !1
    }),
    firstName: import_pieces_framework6.Property.ShortText({
      displayName: "First Name",
      required: !1
    })
  }, prospectCommonProps),
  async run({ auth, propsValue }) {
    return await klentyRequest({
      auth,
      method: import_pieces_common6.HttpMethod.POST,
      path: `/prospects/${encodeURIComponent(String(propsValue.currentEmail))}`,
      body: buildProspectPayload(propsValue)
    });
  }
});

// ../ap/packages/pieces/community/klenty/src/index.ts
var klenty = (0, import_pieces_framework7.createPiece)({
  displayName: "Klenty",
  description: "Sales engagement platform for managing prospects and adding them to outreach cadences.",
  auth: klentyAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/klenty.png",
  authors: ["Harmatta", "sanket-a11y"],
  categories: [import_pieces_framework8.PieceCategory.SALES_AND_CRM, import_pieces_framework8.PieceCategory.MARKETING],
  actions: [
    createProspectAction,
    updateProspectAction,
    getProspectAction,
    addProspectToCampaignAction,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: klentyAuth,
      baseUrl: (auth) => {
        var _a, _b;
        return getKlentyBaseUrl((_b = (_a = auth == null ? void 0 : auth.props) == null ? void 0 : _a.username) != null ? _b : "");
      },
      authMapping: async (auth) => ({
        "x-API-key": auth.props.apiKey,
        api_key: auth.props.apiKey
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  klenty
});
