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

// ../ap/packages/pieces/community/giftbit/src/index.ts
var index_exports = {};
__export(index_exports, {
  giftbit: () => giftbit,
  giftbitAuth: () => giftbitAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/giftbit/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/giftbit/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), TESTBED_URL = "https://api-testbed.giftbit.com/papi/v1", PRODUCTION_URL = "https://api.giftbit.com/papi/v1", giftbitApiCall = async ({
  auth,
  method,
  resourceUri,
  body,
  useTestbed = !1
}) => {
  let baseUrl = useTestbed ? TESTBED_URL : PRODUCTION_URL;
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${baseUrl}${resourceUri}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: auth
    },
    headers: {
      "Content-Type": "application/json",
      "Accept-Encoding": "identity"
    },
    body
  })).body;
};

// ../ap/packages/pieces/community/giftbit/src/lib/common/auth.ts
async function testEndpoint(auth, baseUrl) {
  try {
    return await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `${baseUrl}/ping`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth
      }
    }), !0;
  } catch (e) {
    return !1;
  }
}
var giftbitAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: "Get your API token from Account \u2192 API keys in your Giftbit dashboard.",
  required: !0,
  validate: async ({ auth }) => await testEndpoint(auth, TESTBED_URL) ? { valid: !0 } : await testEndpoint(auth, PRODUCTION_URL) ? { valid: !0 } : {
    valid: !1,
    error: "Invalid API token"
  }
});

// ../ap/packages/pieces/community/giftbit/src/lib/actions/send-reward.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var sendReward = (0, import_pieces_framework2.createAction)({
  name: "send_reward",
  displayName: "Send Reward",
  description: "Sends a gift card reward by email",
  audience: "both",
  aiMetadata: { description: "Sends a digital gift card / reward campaign to one or more email recipients via Giftbit. Choose this to disburse incentives, rebates, or rewards. The reward branding can be set in two ways \u2014 either let recipients pick from the full regional catalog (set a region) or restrict them to specific brand codes; the email body comes either from a saved gift template ID or from an explicit subject and message. Not idempotent: each call creates and sends a new campaign, so retrying re-sends rewards.", idempotent: !1 },
  auth: giftbitAuth,
  props: {
    use_testbed: import_pieces_framework2.Property.Checkbox({
      displayName: "Use Testbed Environment",
      description: "Enable for testing. Disable for production.",
      required: !1,
      defaultValue: !1
    }),
    gift_template: import_pieces_framework2.Property.ShortText({
      displayName: "Gift Template ID",
      description: "Template ID from your Giftbit account (optional if subject/message provided)",
      required: !1
    }),
    order_id: import_pieces_framework2.Property.ShortText({
      displayName: "Order ID",
      description: "Unique identifier for this order",
      required: !0
    }),
    price_in_cents: import_pieces_framework2.Property.Number({
      displayName: "Reward Value (cents)",
      description: "Value in cents (e.g., 2500 = $25.00)",
      required: !0
    }),
    use_full_catalog: import_pieces_framework2.Property.Checkbox({
      displayName: "Use Full Catalog",
      description: "Let recipients choose from all brands in their region",
      required: !1,
      defaultValue: !1
    }),
    brand_codes: import_pieces_framework2.Property.MultiSelectDropdown({
      displayName: "Brand Codes",
      description: "Select one or more brands for the reward",
      required: !1,
      auth: giftbitAuth,
      refreshers: ["use_testbed"],
      options: async (context) => {
        var _a;
        let { auth, use_testbed } = context;
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: ((_a = (await giftbitApiCall({
              auth: auth.secret_text,
              method: import_pieces_common3.HttpMethod.GET,
              resourceUri: "/brands",
              useTestbed: use_testbed
            })).brands) != null ? _a : []).map((brand) => ({
              label: `${brand.name} (${brand.code})`,
              value: brand.code
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load brands. Check your API key.",
            options: []
          };
        }
      }
    }),
    region: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Region",
      description: "Required for Full Catalog rewards",
      required: !1,
      defaultValue: "United States",
      options: {
        options: [
          { label: "United States", value: "United States" },
          { label: "Canada", value: "Canada" },
          { label: "United Kingdom", value: "United Kingdom" },
          { label: "Australia", value: "Australia" },
          { label: "Germany", value: "Germany" },
          { label: "France", value: "France" },
          { label: "Italy", value: "Italy" },
          { label: "Spain", value: "Spain" },
          { label: "Netherlands", value: "Netherlands" },
          { label: "Belgium", value: "Belgium" }
        ]
      }
    }),
    expiry_date: import_pieces_framework2.Property.DateTime({
      displayName: "Expiry Date",
      description: "Claim deadline (defaults to 1 year if not set)",
      required: !1
    }),
    subject: import_pieces_framework2.Property.ShortText({
      displayName: "Email Subject",
      description: "Required if no template is used",
      required: !1
    }),
    message: import_pieces_framework2.Property.LongText({
      displayName: "Email Message",
      description: "Required if no template is used",
      required: !1
    }),
    contacts: import_pieces_framework2.Property.Array({
      displayName: "Recipients",
      description: "List of recipients",
      required: !0,
      properties: {
        email: import_pieces_framework2.Property.ShortText({
          displayName: "Email",
          required: !0
        }),
        firstname: import_pieces_framework2.Property.ShortText({
          displayName: "First Name",
          required: !1
        }),
        lastname: import_pieces_framework2.Property.ShortText({
          displayName: "Last Name",
          required: !1
        })
      }
    })
  },
  async run({ auth, propsValue }) {
    let {
      use_testbed,
      gift_template,
      order_id,
      price_in_cents,
      use_full_catalog,
      brand_codes,
      region,
      expiry_date,
      subject,
      message,
      contacts
    } = propsValue;
    if (!gift_template && (!subject || !message))
      throw new Error("Either gift_template or both subject and message must be provided");
    if (!use_full_catalog && (!brand_codes || brand_codes.length === 0))
      throw new Error("Either select brands or enable Full Catalog");
    if (use_full_catalog && !region)
      throw new Error("Region is required when using Full Catalog");
    let requestBody = {
      id: order_id,
      price_in_cents,
      contacts
    };
    return gift_template ? requestBody.gift_template = gift_template : (requestBody.subject = subject, requestBody.message = message), use_full_catalog ? requestBody.region = region : requestBody.brand_codes = brand_codes, expiry_date && (requestBody.expiry = expiry_date.split("T")[0]), await giftbitApiCall({
      auth: auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      resourceUri: "/campaign",
      body: requestBody,
      useTestbed: use_testbed
    });
  }
});

// ../ap/packages/pieces/community/giftbit/src/index.ts
var giftbit = (0, import_pieces_framework3.createPiece)({
  displayName: "Giftbit",
  description: "Send digital gift cards and rewards to recipients via email.",
  auth: giftbitAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/giftbit.png",
  categories: [import_pieces_framework4.PieceCategory.MARKETING],
  authors: ["onyedikachi-david"],
  actions: [sendReward],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  giftbit,
  giftbitAuth
});
