"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/tapfiliate/src/index.ts
var index_exports = {};
__export(index_exports, {
  tapfiliate: () => tapfiliate
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tapfiliate/src/lib/actions/create-affiliate.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tapfiliate/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/tapfiliate/src/lib/common/tapfiliate.client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var TAPFILIATE_BASE_URL = "https://api.tapfiliate.com/1.6";
function shouldKeepValue(value) {
  return value == null ? !1 : typeof value == "string" ? value.trim().length > 0 : !0;
}
function buildTapfiliateQuery(values) {
  let query = {};
  if (!values)
    return query;
  for (let [key, value] of Object.entries(values))
    shouldKeepValue(value) && (query[key] = String(value));
  return query;
}
function buildTapfiliateRequest({
  method,
  path,
  apiKey,
  apiBaseUrl = TAPFILIATE_BASE_URL,
  query,
  body
}) {
  return {
    method,
    url: `${apiBaseUrl}${path}`,
    headers: {
      "Content-Type": "application/json",
      "X-Api-Key": apiKey
    },
    queryParams: query,
    body
  };
}
function parseTapfiliateNextPage(linkHeader) {
  var _a, _b, _c;
  let normalizedHeader = Array.isArray(linkHeader) ? linkHeader.join(",") : linkHeader;
  if (!normalizedHeader || !normalizedHeader.includes('rel="next"'))
    return;
  let nextLink = (_b = (_a = normalizedHeader.split(",").find((segment) => segment.includes('rel="next"'))) == null ? void 0 : _a.match(/<(.*?)>/)) == null ? void 0 : _b[1];
  if (nextLink)
    return (_c = new URL(nextLink).searchParams.get("page")) != null ? _c : void 0;
}
async function tapfiliateApiCall({
  method,
  path,
  apiKey,
  apiBaseUrl = TAPFILIATE_BASE_URL,
  query,
  body
}) {
  return (await import_pieces_common.httpClient.sendRequest(
    buildTapfiliateRequest({
      method,
      path,
      apiKey,
      apiBaseUrl,
      query,
      body
    })
  )).body;
}
async function tapfiliatePaginatedApiCall({
  method,
  path,
  apiKey,
  apiBaseUrl = TAPFILIATE_BASE_URL,
  query,
  body
}) {
  var _a, _b, _c;
  let results = [], currentQuery = query, pageCount = 0;
  for (; pageCount < TAPFILIATE_MAX_PAGES; ) {
    let response = await import_pieces_common.httpClient.sendRequest(
      buildTapfiliateRequest({
        method,
        path,
        apiKey,
        apiBaseUrl,
        query: currentQuery,
        body
      })
    );
    if (!Array.isArray(response.body) || response.body.length === 0)
      return results;
    results.push(...response.body), pageCount += 1;
    let nextPage = parseTapfiliateNextPage(
      (_c = (_a = response.headers) == null ? void 0 : _a.link) != null ? _c : (_b = response.headers) == null ? void 0 : _b.Link
    );
    if (!nextPage)
      return results;
    currentQuery = __spreadProps(__spreadValues({}, query != null ? query : {}), {
      page: nextPage
    });
  }
  return results;
}
var TAPFILIATE_MAX_PAGES = 500;

// ../ap/packages/pieces/community/tapfiliate/src/lib/common/auth.ts
var tapfiliateAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To obtain your API key:

1. Log in to your Tapfiliate account.
2. Open your account settings.
3. Find the API key section.
4. Copy the API key and paste it here.`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await tapfiliateApiCall({
        method: import_pieces_common2.HttpMethod.GET,
        path: "/affiliates/",
        apiKey: auth
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API Key." };
    }
  }
});

// ../ap/packages/pieces/community/tapfiliate/src/lib/actions/create-affiliate.ts
function isRecord(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}
var createAffiliateAction = (0, import_pieces_framework2.createAction)({
  auth: tapfiliateAuth,
  name: "create_affiliate",
  displayName: "Create Affiliate",
  description: "Creates a new affiliate in Tapfiliate.",
  audience: "both",
  aiMetadata: { description: "Registers a new affiliate account in Tapfiliate from a name, email, and password, with optional company and address details. Use it to onboard a partner into the program. Not idempotent: each call creates a new affiliate, and a duplicate email will be rejected by Tapfiliate.", idempotent: !1 },
  props: {
    firstname: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      required: !0
    }),
    lastname: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      required: !0
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      required: !0
    }),
    password: import_pieces_framework2.Property.ShortText({
      displayName: "Password",
      description: "Password for the new affiliate account.",
      required: !0
    }),
    companyName: import_pieces_framework2.Property.ShortText({
      displayName: "Company Name",
      required: !1
    }),
    companyDescription: import_pieces_framework2.Property.LongText({
      displayName: "Company Description",
      required: !1
    }),
    addressLine1: import_pieces_framework2.Property.ShortText({
      displayName: "Address Line 1",
      required: !1
    }),
    addressLine2: import_pieces_framework2.Property.ShortText({
      displayName: "Address Line 2",
      required: !1
    }),
    postalCode: import_pieces_framework2.Property.ShortText({
      displayName: "Postal Code",
      required: !1
    }),
    city: import_pieces_framework2.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    state: import_pieces_framework2.Property.ShortText({
      displayName: "State",
      required: !1
    }),
    countryCode: import_pieces_framework2.Property.ShortText({
      displayName: "Country Code",
      description: "ISO 3166-1 alpha-2 code, e.g. US or NL.",
      required: !1
    }),
    customFields: import_pieces_framework2.Property.Json({
      displayName: "Custom Fields",
      required: !1
    })
  },
  async run(context) {
    let company = context.propsValue.companyName || context.propsValue.companyDescription ? __spreadValues(__spreadValues({}, context.propsValue.companyName ? { name: context.propsValue.companyName } : {}), context.propsValue.companyDescription ? { description: context.propsValue.companyDescription } : {}) : void 0, address = context.propsValue.addressLine1 || context.propsValue.addressLine2 || context.propsValue.postalCode || context.propsValue.city || context.propsValue.state || context.propsValue.countryCode ? __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, context.propsValue.addressLine1 ? { address: context.propsValue.addressLine1 } : {}), context.propsValue.addressLine2 ? { address_two: context.propsValue.addressLine2 } : {}), context.propsValue.postalCode ? { postal_code: context.propsValue.postalCode } : {}), context.propsValue.city ? { city: context.propsValue.city } : {}), context.propsValue.state ? { state: context.propsValue.state } : {}), context.propsValue.countryCode ? { country: { code: context.propsValue.countryCode } } : {}) : void 0, customFields = isRecord(context.propsValue.customFields) ? context.propsValue.customFields : void 0;
    return await tapfiliateApiCall({
      method: import_pieces_common3.HttpMethod.POST,
      path: "/affiliates/",
      apiKey: context.auth.secret_text,
      body: __spreadValues(__spreadValues(__spreadValues({
        firstname: context.propsValue.firstname,
        lastname: context.propsValue.lastname,
        email: context.propsValue.email,
        password: context.propsValue.password
      }, company ? { company } : {}), address ? { address } : {}), customFields ? { custom_fields: customFields } : {})
    });
  }
});

// ../ap/packages/pieces/community/tapfiliate/src/lib/actions/create-conversion.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
function isRecord2(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}
var createConversionAction = (0, import_pieces_framework3.createAction)({
  auth: tapfiliateAuth,
  name: "create_conversion",
  displayName: "Create Conversion",
  description: "Creates a conversion in Tapfiliate. At least one attribution field is required: Customer ID, Referral Code, Tracking ID, Click ID, Coupon, or Asset ID + Source ID together.",
  audience: "both",
  aiMetadata: { description: "Records a conversion (e.g. a sale or signup) in Tapfiliate so the responsible affiliate earns commission. Requires an amount plus at least one attribution key \u2014 Customer ID, Referral Code, Tracking ID, Click ID, Coupon, or Asset ID together with Source ID \u2014 or the call is rejected. Not idempotent: each call appends a new conversion, so guard against duplicate sends.", idempotent: !1 },
  props: {
    referralCode: import_pieces_framework3.Property.ShortText({
      displayName: "Referral Code",
      description: "The affiliate's referral code used to attribute this conversion.",
      required: !1
    }),
    trackingId: import_pieces_framework3.Property.ShortText({
      displayName: "Tracking ID",
      description: "The Tapfiliate tracking ID stored in the affiliate click cookie.",
      required: !1
    }),
    clickId: import_pieces_framework3.Property.ShortText({
      displayName: "Click ID",
      description: "The Tapfiliate click ID from the affiliate referral link.",
      required: !1
    }),
    coupon: import_pieces_framework3.Property.ShortText({
      displayName: "Coupon",
      description: "A coupon code linked to an affiliate to attribute this conversion.",
      required: !1
    }),
    currency: import_pieces_framework3.Property.ShortText({
      displayName: "Currency",
      description: "Three-letter ISO currency code such as USD or EUR.",
      required: !1
    }),
    assetId: import_pieces_framework3.Property.ShortText({
      displayName: "Asset ID",
      description: "The Tapfiliate asset ID. Must be provided together with Source ID.",
      required: !1
    }),
    sourceId: import_pieces_framework3.Property.ShortText({
      displayName: "Source ID",
      description: "The Tapfiliate source ID. Must be provided together with Asset ID.",
      required: !1
    }),
    externalId: import_pieces_framework3.Property.ShortText({
      displayName: "External ID",
      description: "Your own unique identifier for this conversion, such as an order number.",
      required: !1
    }),
    amount: import_pieces_framework3.Property.Number({
      displayName: "Amount",
      description: "Conversion amount used to calculate commissions.",
      required: !0
    }),
    customerId: import_pieces_framework3.Property.ShortText({
      displayName: "Customer ID",
      description: "Your customer identifier to attribute this conversion to the matching affiliate.",
      required: !1
    }),
    overrideMaxCookieTime: import_pieces_framework3.Property.Checkbox({
      displayName: "Override Max Cookie Time",
      description: "Override the maximum cookie time for this conversion.",
      required: !1,
      defaultValue: !1
    }),
    userAgent: import_pieces_framework3.Property.LongText({
      displayName: "User Agent",
      required: !1
    }),
    ip: import_pieces_framework3.Property.ShortText({
      displayName: "IP Address",
      required: !1
    }),
    metaData: import_pieces_framework3.Property.Json({
      displayName: "Meta Data",
      required: !1
    })
  },
  async run(context) {
    let metaData = isRecord2(context.propsValue.metaData) ? context.propsValue.metaData : void 0;
    if (!!context.propsValue.assetId != !!context.propsValue.sourceId)
      throw new Error("Asset ID and Source ID must be provided together.");
    if (!(!!context.propsValue.customerId || !!context.propsValue.referralCode || !!context.propsValue.trackingId || !!context.propsValue.clickId || !!context.propsValue.coupon || !!context.propsValue.assetId && !!context.propsValue.sourceId))
      throw new Error(
        "Tapfiliate requires at least one attribution key: Customer ID, Referral Code, Tracking ID, Click ID, Coupon, or Asset ID + Source ID."
      );
    return await tapfiliateApiCall({
      method: import_pieces_common4.HttpMethod.POST,
      path: "/conversions/",
      apiKey: context.auth.secret_text,
      query: context.propsValue.overrideMaxCookieTime ? { override_max_cookie_time: "true" } : void 0,
      body: __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadProps(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, context.propsValue.referralCode ? { referral_code: context.propsValue.referralCode } : {}), context.propsValue.trackingId ? { tracking_id: context.propsValue.trackingId } : {}), context.propsValue.clickId ? { click_id: context.propsValue.clickId } : {}), context.propsValue.coupon ? { coupon: context.propsValue.coupon } : {}), context.propsValue.currency ? { currency: context.propsValue.currency } : {}), context.propsValue.assetId ? { asset_id: context.propsValue.assetId } : {}), context.propsValue.sourceId ? { source_id: context.propsValue.sourceId } : {}), context.propsValue.externalId ? { external_id: context.propsValue.externalId } : {}), {
        amount: context.propsValue.amount
      }), context.propsValue.customerId ? { customer_id: context.propsValue.customerId } : {}), context.propsValue.userAgent ? { user_agent: context.propsValue.userAgent } : {}), context.propsValue.ip ? { ip: context.propsValue.ip } : {}), metaData ? { meta_data: metaData } : {})
    });
  }
});

// ../ap/packages/pieces/community/tapfiliate/src/lib/actions/get-affiliate.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var getAffiliateAction = (0, import_pieces_framework4.createAction)({
  auth: tapfiliateAuth,
  name: "get_affiliate",
  displayName: "Get Affiliate",
  description: "Retrieves a Tapfiliate affiliate by ID.",
  audience: "both",
  aiMetadata: { description: "Fetches a single Tapfiliate affiliate by its unique affiliate ID. Use it to look up an affiliate's details when you already have its ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    affiliateId: import_pieces_framework4.Property.ShortText({
      displayName: "Affiliate ID",
      description: "The unique Tapfiliate affiliate ID. Find it on the affiliate detail page in your Tapfiliate dashboard.",
      required: !0
    })
  },
  async run(context) {
    return await tapfiliateApiCall({
      method: import_pieces_common5.HttpMethod.GET,
      path: `/affiliates/${context.propsValue.affiliateId}/`,
      apiKey: context.auth.secret_text
    });
  }
});

// ../ap/packages/pieces/community/tapfiliate/src/lib/actions/list-affiliates.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var listAffiliatesAction = (0, import_pieces_framework5.createAction)({
  auth: tapfiliateAuth,
  name: "list_affiliates",
  displayName: "List Affiliates",
  description: "Lists affiliates with optional Tapfiliate filters.",
  audience: "both",
  aiMetadata: { description: "Lists affiliates in the Tapfiliate account, paginating through all results. With no filters it returns every affiliate; supplying any of email, referral code, click ID, source ID, parent affiliate ID, or affiliate group ID narrows to matching records. Use it to discover affiliates or resolve an affiliate ID from an email. Read-only and idempotent.", idempotent: !0 },
  props: {
    clickId: import_pieces_framework5.Property.ShortText({
      displayName: "Click ID",
      required: !1
    }),
    sourceId: import_pieces_framework5.Property.ShortText({
      displayName: "Source ID",
      required: !1
    }),
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    referralCode: import_pieces_framework5.Property.ShortText({
      displayName: "Referral Code",
      required: !1
    }),
    parentId: import_pieces_framework5.Property.ShortText({
      displayName: "Parent Affiliate ID",
      required: !1
    }),
    affiliateGroupId: import_pieces_framework5.Property.ShortText({
      displayName: "Affiliate Group ID",
      required: !1
    })
  },
  async run(context) {
    return await tapfiliatePaginatedApiCall({
      method: import_pieces_common6.HttpMethod.GET,
      path: "/affiliates/",
      apiKey: context.auth.secret_text,
      query: buildTapfiliateQuery({
        click_id: context.propsValue.clickId,
        source_id: context.propsValue.sourceId,
        email: context.propsValue.email,
        referral_code: context.propsValue.referralCode,
        parent_id: context.propsValue.parentId,
        affiliate_group_id: context.propsValue.affiliateGroupId
      })
    });
  }
});

// ../ap/packages/pieces/community/tapfiliate/src/index.ts
var tapfiliate = (0, import_pieces_framework6.createPiece)({
  displayName: "Tapfiliate",
  description: "Affiliate tracking and conversion management platform.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/tapfiliate.png",
  categories: [import_pieces_framework7.PieceCategory.MARKETING, import_pieces_framework7.PieceCategory.SALES_AND_CRM],
  authors: ["Harmatta", "onyedikachi-david"],
  auth: tapfiliateAuth,
  actions: [
    createAffiliateAction,
    getAffiliateAction,
    listAffiliatesAction,
    createConversionAction,
    (0, import_pieces_common7.createCustomApiCallAction)({
      baseUrl: () => TAPFILIATE_BASE_URL,
      auth: tapfiliateAuth,
      authMapping: async (auth) => ({
        "X-Api-Key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  tapfiliate
});
