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

// ../ap/packages/pieces/community/vouchery-io/src/index.ts
var index_exports = {};
__export(index_exports, {
  voucheryIo: () => voucheryIo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vouchery-io/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), voucheryIoAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Vouchery-io API Key",
  required: !0
});

// ../ap/packages/pieces/community/vouchery-io/src/lib/actions/find-voucher.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/vouchery-io/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://admin.sandbox.vouchery.app/api/v2.1";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${api_key}`
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/vouchery-io/src/lib/actions/find-voucher.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var findVoucher = (0, import_pieces_framework2.createAction)({
  auth: voucheryIoAuth,
  name: "findVoucher",
  displayName: "Find Voucher",
  description: "Find a voucher by campaign metadata. At least one metadata must be provided",
  audience: "both",
  aiMetadata: {
    description: "Looks up an existing Vouchery voucher by campaign metadata (purpose, team, channel, medium); at least one metadata filter must be provided. Optionally, supplying a customer identifier also assigns the found voucher to that customer (a mutation), otherwise it is a pure read. Use to retrieve a voucher matching campaign criteria before redemption or assignment.",
    idempotent: !0
  },
  props: {
    purpose: import_pieces_framework2.Property.ShortText({
      displayName: "Purpose",
      description: "Purpose metadata (multiple values allowed)",
      required: !1
    }),
    team: import_pieces_framework2.Property.ShortText({
      displayName: "Team",
      description: "Team metadata (multiple values allowed)",
      required: !1
    }),
    channel: import_pieces_framework2.Property.ShortText({
      displayName: "Channel",
      description: "Channel metadata (multiple values allowed)",
      required: !1
    }),
    medium: import_pieces_framework2.Property.ShortText({
      displayName: "Medium",
      description: "Medium metadata (multiple values allowed)",
      required: !1
    }),
    customer_identifier: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Identifier",
      description: "Optional: Assign the found voucher to a customer by providing their identifier",
      required: !1
    })
  },
  async run(context) {
    let { purpose, team, channel, medium, customer_identifier } = context.propsValue, params = new URLSearchParams();
    purpose && purpose.length > 0 && params.append("purpose", purpose), team && team.length > 0 && params.append("team", team), channel && channel.length > 0 && params.append("channel", channel), medium && medium.length > 0 && params.append("medium", medium), customer_identifier && params.append("customer_identifier", customer_identifier);
    let queryString = params.toString(), path = `/vouchers/find${queryString ? "?" + queryString : ""}`;
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common2.HttpMethod.GET,
      path
    );
  }
});

// ../ap/packages/pieces/community/vouchery-io/src/lib/actions/create-customer.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var createCustomer = (0, import_pieces_framework3.createAction)({
  auth: voucheryIoAuth,
  name: "createCustomer",
  displayName: "Create Customer",
  description: "Create a new customer",
  audience: "both",
  aiMetadata: {
    description: "Creates a new customer record in Vouchery, keyed by a unique customer identifier from your application (hash, id, email, or any unique value). Use when onboarding a customer before issuing or assigning vouchers. Not idempotent: each call creates a customer, so repeating it with the same identifier may produce a duplicate or error.",
    idempotent: !1
  },
  props: {
    identifier: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Identifier",
      description: "Unique customer identifier in your application. Can be hash, id, email or any other unique value",
      required: !0
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Name",
      description: "Customer full name",
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "Customer email address",
      required: !1
    }),
    birthdate: import_pieces_framework3.Property.Object({
      displayName: "Birthdate",
      description: "Customer birthdate object",
      required: !1
    }),
    categories: import_pieces_framework3.Property.Array({
      displayName: "Categories",
      description: "Array of category objects to determine how customer is related to specific categories",
      required: !1,
      properties: {
        name: import_pieces_framework3.Property.ShortText({
          displayName: "Category Name",
          description: "Name of the category",
          required: !0
        }),
        tag: import_pieces_framework3.Property.ShortText({
          displayName: "tag",
          description: "tag",
          required: !0
        })
      }
    }),
    metadata: import_pieces_framework3.Property.Object({
      displayName: "Metadata",
      description: "Additional metadata for the customer",
      required: !1
    }),
    referrer_code: import_pieces_framework3.Property.ShortText({
      displayName: "Referrer Code",
      description: "A referral code from the recommending user",
      required: !1
    }),
    loyalty_points: import_pieces_framework3.Property.Number({
      displayName: "Loyalty Points",
      description: "[DEPRECATED - use grant-points endpoint instead] Number of loyalty points customer will have",
      required: !1
    })
  },
  async run(context) {
    let {
      identifier,
      name,
      email,
      birthdate,
      categories,
      metadata,
      referrer_code,
      loyalty_points
    } = context.propsValue, body = {
      identifier,
      categories,
      metadata
    };
    return name && (body.name = name), email && (body.email = email), birthdate && (body.birthdate = birthdate), referrer_code && (body.referrer_code = referrer_code), loyalty_points !== void 0 && (body.loyalty_points = loyalty_points), await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/customers",
      body
    );
  }
});

// ../ap/packages/pieces/community/vouchery-io/src/lib/actions/create-a-voucher.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/vouchery-io/src/lib/common/props.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), comapaignIdDropdown = import_pieces_framework4.Property.Dropdown({
  auth: voucheryIoAuth,
  displayName: "Campaign",
  description: "Select the campaign",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await makeRequest(
      auth == null ? void 0 : auth.secret_text,
      import_pieces_common4.HttpMethod.GET,
      "/campaigns"
    )).map((campaign) => ({
      label: campaign.name,
      value: campaign.id
    }))
  } : {
    disabled: !0,
    options: []
  }
});

// ../ap/packages/pieces/community/vouchery-io/src/lib/actions/create-a-voucher.ts
var createAVoucher = (0, import_pieces_framework5.createAction)({
  auth: voucheryIoAuth,
  name: "createAVoucher",
  displayName: "Create a voucher",
  description: "Create a new voucher in a campaign",
  audience: "both",
  aiMetadata: {
    description: "Creates a new voucher with the given code inside a specific Vouchery campaign, optionally setting an expiration date, activation date, gift card value, and assigned customer. Use to issue a redeemable voucher or gift card under a campaign. Not idempotent: each call creates a voucher, so repeating it generates additional vouchers (a duplicate code may error).",
    idempotent: !1
  },
  props: {
    campaignId: comapaignIdDropdown,
    voucherCode: import_pieces_framework5.Property.ShortText({
      displayName: "Voucher Code",
      description: "The code for the voucher",
      required: !0
    }),
    voucher_valid_until: import_pieces_framework5.Property.DateTime({
      displayName: "Expiration Date",
      description: "The date and time when the voucher expires",
      required: !1
    }),
    customer_identifier: import_pieces_framework5.Property.Number({
      displayName: "Customer Identifier",
      description: "The identifier for the customer",
      required: !1
    }),
    gift_card_value: import_pieces_framework5.Property.Number({
      displayName: "Gift Card Value",
      description: "The value of the gift card",
      required: !1
    }),
    activates_at: import_pieces_framework5.Property.DateTime({
      displayName: "Activation Date",
      description: "The date and time when the voucher becomes active",
      required: !1
    })
  },
  async run(context) {
    let {
      campaignId,
      voucherCode,
      voucher_valid_until,
      customer_identifier,
      gift_card_value,
      activates_at
    } = context.propsValue, body = {
      code: voucherCode
    };
    return voucher_valid_until && (body.valid_until = new Date(voucher_valid_until).toISOString()), customer_identifier && (body.customer_identifier = customer_identifier), gift_card_value && (body.gift_card_value = gift_card_value), activates_at && (body.activates_at = new Date(activates_at).toISOString()), await makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      `/campaigns/${campaignId}/vouchers`,
      body
    );
  }
});

// ../ap/packages/pieces/community/vouchery-io/src/index.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), voucheryIo = (0, import_pieces_framework6.createPiece)({
  displayName: "Vouchery",
  auth: voucheryIoAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/vouchery-io.png",
  authors: ["sanket-a11y"],
  description: "Vouchery is a  voucher and gift card management platform.",
  actions: [
    createAVoucher,
    createCustomer,
    findVoucher,
    (0, import_pieces_common6.createCustomApiCallAction)({
      auth: voucheryIoAuth,
      baseUrl: () => "https://admin.sandbox.vouchery.app/api/v2.1",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  voucheryIo
});
