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

// ../ap/packages/pieces/community/promotekit/src/index.ts
var index_exports = {};
__export(index_exports, {
  promotekit: () => promotekit,
  promotekitAuth: () => promotekitAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common18 = require("@activepieces/pieces-common"), import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_framework19 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/promotekit/src/lib/actions/list-affiliates.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/promotekit/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var BASE_URL = "https://www.promotekit.com/api/v1";
async function promotekitApiCall({
  token,
  method,
  path,
  body,
  queryParams
}) {
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token
    },
    queryParams,
    body
  });
}
var MAX_PAGES = 100;
async function promotekitPaginatedApiCall({
  token,
  path,
  queryParams
}) {
  let results = [], page = 1, hasMore = !0;
  for (; hasMore && page <= MAX_PAGES; ) {
    let response = await promotekitApiCall({
      token,
      method: import_pieces_common.HttpMethod.GET,
      path,
      queryParams: __spreadProps(__spreadValues({}, queryParams), {
        page: String(page),
        limit: "100"
      })
    });
    results.push(...response.body.data), hasMore = response.body.has_more, page++;
  }
  return results;
}
function flattenAffiliate(affiliate) {
  var _a, _b, _c, _d, _e, _f, _g, _h;
  let campaign = affiliate.campaign, links = affiliate.links, promoCodes = affiliate.promo_codes;
  return {
    id: affiliate.id,
    email: affiliate.email,
    first_name: affiliate.first_name,
    last_name: affiliate.last_name,
    payout_email: (_a = affiliate.payout_email) != null ? _a : null,
    clicks: (_b = affiliate.clicks) != null ? _b : 0,
    approved: (_c = affiliate.approved) != null ? _c : !1,
    banned: (_d = affiliate.banned) != null ? _d : !1,
    links: links ? links.map((l) => `${l.url} (${l.code})`).join(", ") : null,
    promo_codes: promoCodes ? promoCodes.map((p) => p.code).join(", ") : null,
    campaign_id: (_e = campaign == null ? void 0 : campaign.id) != null ? _e : null,
    campaign_name: (_f = campaign == null ? void 0 : campaign.name) != null ? _f : null,
    campaign_commission_type: (_g = campaign == null ? void 0 : campaign.commission_type) != null ? _g : null,
    campaign_commission_amount: (_h = campaign == null ? void 0 : campaign.commission_amount) != null ? _h : null,
    created_at: affiliate.created_at,
    updated_at: affiliate.updated_at
  };
}
function flattenReferral(referral) {
  var _a, _b, _c, _d, _e, _f, _g;
  let affiliate = referral.affiliate;
  return {
    id: referral.id,
    email: referral.email,
    subscription_status: (_a = referral.subscription_status) != null ? _a : null,
    signup_date: (_b = referral.signup_date) != null ? _b : null,
    stripe_customer_id: (_c = referral.stripe_customer_id) != null ? _c : null,
    affiliate_id: (_d = affiliate == null ? void 0 : affiliate.id) != null ? _d : null,
    affiliate_email: (_e = affiliate == null ? void 0 : affiliate.email) != null ? _e : null,
    affiliate_first_name: (_f = affiliate == null ? void 0 : affiliate.first_name) != null ? _f : null,
    affiliate_last_name: (_g = affiliate == null ? void 0 : affiliate.last_name) != null ? _g : null,
    created_at: referral.created_at
  };
}
function flattenCommission(commission) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
  let affiliate = commission.affiliate, referral = commission.referral, payout = commission.payout;
  return {
    id: commission.id,
    revenue_amount: (_a = commission.revenue_amount) != null ? _a : null,
    currency: (_b = commission.currency) != null ? _b : null,
    commission_amount: (_c = commission.commission_amount) != null ? _c : null,
    payout_status: (_d = commission.payout_status) != null ? _d : null,
    referral_date: (_e = commission.referral_date) != null ? _e : null,
    stripe_payment_id: (_f = commission.stripe_payment_id) != null ? _f : null,
    affiliate_id: (_g = affiliate == null ? void 0 : affiliate.id) != null ? _g : null,
    affiliate_email: (_h = affiliate == null ? void 0 : affiliate.email) != null ? _h : null,
    referral_id: (_i = referral == null ? void 0 : referral.id) != null ? _i : null,
    referral_email: (_j = referral == null ? void 0 : referral.email) != null ? _j : null,
    payout_id: (_k = payout == null ? void 0 : payout.id) != null ? _k : null,
    created_at: commission.created_at,
    updated_at: commission.updated_at
  };
}
function flattenPayout(payout) {
  var _a, _b, _c, _d, _e, _f, _g, _h;
  let affiliate = payout.affiliate;
  return {
    id: payout.id,
    amount: (_a = payout.amount) != null ? _a : null,
    currency: (_b = payout.currency) != null ? _b : null,
    status: (_c = payout.status) != null ? _c : null,
    affiliate_id: (_d = affiliate == null ? void 0 : affiliate.id) != null ? _d : null,
    affiliate_email: (_e = affiliate == null ? void 0 : affiliate.email) != null ? _e : null,
    affiliate_first_name: (_f = affiliate == null ? void 0 : affiliate.first_name) != null ? _f : null,
    affiliate_last_name: (_g = affiliate == null ? void 0 : affiliate.last_name) != null ? _g : null,
    created_at: payout.created_at,
    updated_at: (_h = payout.updated_at) != null ? _h : null
  };
}
var promotekitCommon = {
  flattenAffiliate,
  flattenReferral,
  flattenCommission,
  flattenPayout,
  campaignDropdown: import_pieces_framework.Property.Dropdown({
    auth: promotekitAuth,
    displayName: "Campaign",
    description: "Select the campaign for this affiliate.",
    refreshers: [],
    required: !1,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await promotekitPaginatedApiCall({
        token: auth.secret_text,
        path: "/campaigns"
      })).map((c) => ({
        label: c.name,
        value: c.id
      }))
    } : {
      disabled: !0,
      options: [],
      placeholder: "Please connect your account first"
    }
  }),
  affiliateDropdown: import_pieces_framework.Property.Dropdown({
    auth: promotekitAuth,
    displayName: "Affiliate",
    description: "Select the affiliate.",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await promotekitPaginatedApiCall({
        token: auth.secret_text,
        path: "/affiliates"
      })).map((a) => {
        var _a, _b;
        return {
          label: `${(_a = a.first_name) != null ? _a : ""} ${(_b = a.last_name) != null ? _b : ""} (${a.email})`.trim(),
          value: a.id
        };
      })
    } : {
      disabled: !0,
      options: [],
      placeholder: "Please connect your account first"
    }
  })
};

// ../ap/packages/pieces/community/promotekit/src/lib/actions/list-affiliates.ts
var listAffiliates = (0, import_pieces_framework2.createAction)({
  auth: promotekitAuth,
  name: "list_affiliates",
  displayName: "List Affiliates",
  description: "List all affiliates in your PromoteKit account.",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a paginated list of affiliates in the PromoteKit account. Use to enumerate or browse affiliates rather than look one up by a known ID. Supports page and per-page limit (max 100); read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    page: import_pieces_framework2.Property.Number({
      displayName: "Page",
      description: "Page number to retrieve. Defaults to 1.",
      required: !1,
      defaultValue: 1
    }),
    limit: import_pieces_framework2.Property.Number({
      displayName: "Limit",
      description: "Number of affiliates per page (max 100). Defaults to 10.",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    var _a, _b;
    return (await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common2.HttpMethod.GET,
      path: "/affiliates",
      queryParams: {
        page: String((_a = context.propsValue.page) != null ? _a : 1),
        limit: String((_b = context.propsValue.limit) != null ? _b : 10)
      }
    })).body.data.map(promotekitCommon.flattenAffiliate);
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/find-affiliate.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var findAffiliate = (0, import_pieces_framework3.createAction)({
  auth: promotekitAuth,
  name: "find_affiliate",
  displayName: "Find Affiliate",
  description: "Get details of a specific affiliate by ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single affiliate by its PromoteKit affiliate ID. Use when you already have the exact ID and need that affiliate's details; to discover IDs, list affiliates first. Read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    affiliate_id: import_pieces_framework3.Property.ShortText({
      displayName: "Affiliate ID",
      description: "The ID of the affiliate to retrieve.",
      required: !0
    })
  },
  async run(context) {
    let response = await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      path: `/affiliates/${context.propsValue.affiliate_id}`
    });
    return promotekitCommon.flattenAffiliate(response.body.data);
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/create-affiliate.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createAffiliate = (0, import_pieces_framework4.createAction)({
  auth: promotekitAuth,
  name: "create_affiliate",
  displayName: "Create Affiliate",
  description: "Create a new affiliate in PromoteKit.",
  audience: "both",
  aiMetadata: {
    description: "Create a new affiliate in PromoteKit, optionally assigning them to a campaign. Email is required; first/last name and campaign are optional. Not idempotent: each call adds another affiliate, so guard against duplicates by checking for an existing affiliate first.",
    idempotent: !1
  },
  props: {
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      description: "The email address of the new affiliate.",
      required: !0
    }),
    first_name: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework4.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    campaign_id: promotekitCommon.campaignDropdown
  },
  async run(context) {
    let body = {
      email: context.propsValue.email
    };
    context.propsValue.first_name && (body.first_name = context.propsValue.first_name), context.propsValue.last_name && (body.last_name = context.propsValue.last_name), context.propsValue.campaign_id && (body.campaign_id = context.propsValue.campaign_id);
    let response = await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/affiliates",
      body
    });
    return promotekitCommon.flattenAffiliate(response.body.data);
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/update-affiliate.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateAffiliate = (0, import_pieces_framework5.createAction)({
  auth: promotekitAuth,
  name: "update_affiliate",
  displayName: "Update Affiliate",
  description: "Update an existing affiliate in PromoteKit.",
  audience: "both",
  aiMetadata: {
    description: "Update an existing affiliate identified by affiliate ID, changing only the fields you supply (name, payout email, approved, banned); omitted fields are left untouched. Use to approve, ban, or edit an affiliate. Idempotent: re-sending the same field values leaves the affiliate in the same state.",
    idempotent: !0
  },
  props: {
    affiliate_id: import_pieces_framework5.Property.ShortText({
      displayName: "Affiliate ID",
      description: "The ID of the affiliate to update.",
      required: !0
    }),
    first_name: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    payout_email: import_pieces_framework5.Property.ShortText({
      displayName: "Payout Email",
      description: "The email address where the affiliate receives payouts.",
      required: !1
    }),
    approved: import_pieces_framework5.Property.Checkbox({
      displayName: "Approved",
      description: "Whether the affiliate is approved.",
      required: !1
    }),
    banned: import_pieces_framework5.Property.Checkbox({
      displayName: "Banned",
      description: "Whether the affiliate is banned.",
      required: !1
    })
  },
  async run(context) {
    let body = {};
    context.propsValue.first_name !== void 0 && (body.first_name = context.propsValue.first_name), context.propsValue.last_name !== void 0 && (body.last_name = context.propsValue.last_name), context.propsValue.payout_email !== void 0 && (body.payout_email = context.propsValue.payout_email), context.propsValue.approved !== void 0 && (body.approved = context.propsValue.approved), context.propsValue.banned !== void 0 && (body.banned = context.propsValue.banned);
    let response = await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.PUT,
      path: `/affiliates/${context.propsValue.affiliate_id}`,
      body
    });
    return promotekitCommon.flattenAffiliate(response.body.data);
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/list-referrals.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var listReferrals = (0, import_pieces_framework6.createAction)({
  auth: promotekitAuth,
  name: "list_referrals",
  displayName: "List Referrals",
  description: "List all referrals in your PromoteKit account.",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a paginated list of referrals (referred customers) in the PromoteKit account. Use to enumerate or browse referrals rather than look one up by a known ID. Supports page and per-page limit (max 100); read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    page: import_pieces_framework6.Property.Number({
      displayName: "Page",
      description: "Page number to retrieve. Defaults to 1.",
      required: !1,
      defaultValue: 1
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Number of referrals per page (max 100). Defaults to 10.",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    var _a, _b;
    return (await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.GET,
      path: "/referrals",
      queryParams: {
        page: String((_a = context.propsValue.page) != null ? _a : 1),
        limit: String((_b = context.propsValue.limit) != null ? _b : 10)
      }
    })).body.data.map(promotekitCommon.flattenReferral);
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/find-referral.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var findReferral = (0, import_pieces_framework7.createAction)({
  auth: promotekitAuth,
  name: "find_referral",
  displayName: "Find Referral",
  description: "Get details of a specific referral by ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single referral by its PromoteKit referral ID. Use when you already have the exact ID and need that referral's details; to discover IDs, list referrals first. Read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    referral_id: import_pieces_framework7.Property.ShortText({
      displayName: "Referral ID",
      description: "The ID of the referral to retrieve.",
      required: !0
    })
  },
  async run(context) {
    let response = await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      path: `/referrals/${context.propsValue.referral_id}`
    });
    return promotekitCommon.flattenReferral(response.body.data);
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/create-referral.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var createReferral = (0, import_pieces_framework8.createAction)({
  auth: promotekitAuth,
  name: "create_referral",
  displayName: "Create Referral",
  description: "Create a new referral in PromoteKit.",
  audience: "both",
  aiMetadata: {
    description: "Create a new referral attributing a referred person (by email) to an affiliate. Both the email and the affiliate ID are required. Not idempotent: each call records another referral, so check for an existing one before repeating.",
    idempotent: !1
  },
  props: {
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Email",
      description: "The email address of the referred person.",
      required: !0
    }),
    affiliate_id: promotekitCommon.affiliateDropdown
  },
  async run(context) {
    let response = await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.POST,
      path: "/referrals",
      body: {
        email: context.propsValue.email,
        affiliate_id: context.propsValue.affiliate_id
      }
    });
    return promotekitCommon.flattenReferral(response.body.data);
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/list-commissions.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var listCommissions = (0, import_pieces_framework9.createAction)({
  auth: promotekitAuth,
  name: "list_commissions",
  displayName: "List Commissions",
  description: "List all commissions in your PromoteKit account.",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a paginated list of commissions earned in the PromoteKit account. Use to enumerate or browse commissions rather than look one up by a known ID. Supports page and per-page limit (max 100); read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    page: import_pieces_framework9.Property.Number({
      displayName: "Page",
      description: "Page number to retrieve. Defaults to 1.",
      required: !1,
      defaultValue: 1
    }),
    limit: import_pieces_framework9.Property.Number({
      displayName: "Limit",
      description: "Number of commissions per page (max 100). Defaults to 10.",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    var _a, _b;
    return (await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.GET,
      path: "/commissions",
      queryParams: {
        page: String((_a = context.propsValue.page) != null ? _a : 1),
        limit: String((_b = context.propsValue.limit) != null ? _b : 10)
      }
    })).body.data.map(promotekitCommon.flattenCommission);
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/find-commission.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var findCommission = (0, import_pieces_framework10.createAction)({
  auth: promotekitAuth,
  name: "find_commission",
  displayName: "Find Commission",
  description: "Get details of a specific commission by ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single commission by its PromoteKit commission ID. Use when you already have the exact ID and need that commission's details; to discover IDs, list commissions first. Read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    commission_id: import_pieces_framework10.Property.ShortText({
      displayName: "Commission ID",
      description: "The ID of the commission to retrieve.",
      required: !0
    })
  },
  async run(context) {
    let response = await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.GET,
      path: `/commissions/${context.propsValue.commission_id}`
    });
    return promotekitCommon.flattenCommission(response.body.data);
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/list-campaigns.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var listCampaigns = (0, import_pieces_framework11.createAction)({
  auth: promotekitAuth,
  name: "list_campaigns",
  displayName: "List Campaigns",
  description: "List all campaigns in your PromoteKit account.",
  audience: "both",
  aiMetadata: {
    description: "Retrieve all campaigns (up to 100) in the PromoteKit account, each with its commission type and amount. Use to discover campaign IDs and commission terms, e.g. before assigning an affiliate to a campaign. Read-only and safe to repeat.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    return (await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.GET,
      path: "/campaigns",
      queryParams: { limit: "100" }
    })).body.data.map((campaign) => {
      var _a, _b;
      return {
        id: campaign.id,
        name: campaign.name,
        commission_type: (_a = campaign.commission_type) != null ? _a : null,
        commission_amount: (_b = campaign.commission_amount) != null ? _b : null
      };
    });
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/find-campaign.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var findCampaign = (0, import_pieces_framework12.createAction)({
  auth: promotekitAuth,
  name: "find_campaign",
  displayName: "Find Campaign",
  description: "Get details of a specific campaign by ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single campaign by its PromoteKit campaign ID, returning its name and commission terms. Use when you already have the exact ID; to discover IDs, list campaigns first. Read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    campaign_id: import_pieces_framework12.Property.ShortText({
      displayName: "Campaign ID",
      description: "The ID of the campaign to retrieve.",
      required: !0
    })
  },
  async run(context) {
    var _a, _b;
    let campaign = (await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.GET,
      path: `/campaigns/${context.propsValue.campaign_id}`
    })).body.data;
    return {
      id: campaign.id,
      name: campaign.name,
      commission_type: (_a = campaign.commission_type) != null ? _a : null,
      commission_amount: (_b = campaign.commission_amount) != null ? _b : null
    };
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/list-payouts.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var listPayouts = (0, import_pieces_framework13.createAction)({
  auth: promotekitAuth,
  name: "list_payouts",
  displayName: "List Payouts",
  description: "List all payouts in your PromoteKit account.",
  audience: "both",
  aiMetadata: {
    description: "Retrieve a paginated list of payouts to affiliates in the PromoteKit account. Use to enumerate or browse payouts rather than look one up by a known ID. Supports page and per-page limit (max 100); read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    page: import_pieces_framework13.Property.Number({
      displayName: "Page",
      description: "Page number to retrieve. Defaults to 1.",
      required: !1,
      defaultValue: 1
    }),
    limit: import_pieces_framework13.Property.Number({
      displayName: "Limit",
      description: "Number of payouts per page (max 100). Defaults to 10.",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    var _a, _b;
    return (await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.GET,
      path: "/payouts",
      queryParams: {
        page: String((_a = context.propsValue.page) != null ? _a : 1),
        limit: String((_b = context.propsValue.limit) != null ? _b : 10)
      }
    })).body.data.map(promotekitCommon.flattenPayout);
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/actions/find-payout.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var findPayout = (0, import_pieces_framework14.createAction)({
  auth: promotekitAuth,
  name: "find_payout",
  displayName: "Find Payout",
  description: "Get details of a specific payout by ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single payout by its PromoteKit payout ID. Use when you already have the exact ID and need that payout's details; to discover IDs, list payouts first. Read-only and safe to repeat.",
    idempotent: !0
  },
  props: {
    payout_id: import_pieces_framework14.Property.ShortText({
      displayName: "Payout ID",
      description: "The ID of the payout to retrieve.",
      required: !0
    })
  },
  async run(context) {
    let response = await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common14.HttpMethod.GET,
      path: `/payouts/${context.propsValue.payout_id}`
    });
    return promotekitCommon.flattenPayout(response.body.data);
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/triggers/new-affiliate-event.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var import_pieces_common15 = require("@activepieces/pieces-common");
var AFFILIATE_EVENT_OPTIONS = [
  { label: "Affiliate Created", value: "affiliate.created" },
  { label: "Affiliate Approved", value: "affiliate.approved" }
], newAffiliateEvent = (0, import_pieces_framework15.createTrigger)({
  auth: promotekitAuth,
  name: "new_affiliate_event",
  displayName: "New Affiliate Event",
  description: "Triggers when a PromoteKit affiliate event is received.",
  aiMetadata: {
    description: "Fires on a PromoteKit affiliate webhook event \u2014 when an affiliate is created or approved. Optionally filter to specific event types; leave the selection empty to fire on any affiliate event. Requires the webhook endpoint to be configured in the PromoteKit dashboard."
  },
  props: {
    eventTypes: import_pieces_framework15.Property.StaticMultiSelectDropdown({
      displayName: "Affiliate Event Types",
      description: "Select the affiliate event types to listen for. Leave empty to receive all affiliate events.",
      required: !1,
      options: { options: AFFILIATE_EVENT_OPTIONS }
    }),
    instructions: import_pieces_framework15.Property.MarkDown({
      value: `### Setup Instructions

1. Go to your PromoteKit dashboard.
2. Navigate to **Settings > Webhooks**.
3. Click **Add Endpoint**.
4. Paste the webhook URL shown below :
\`\`\`text
            {{webhookUrl}}
            \`\`\`
5. Enable the affiliate event types you selected above.
6. Save the endpoint.

**Need different flows for different events?** Create a separate flow with its own webhook URL, and add a new endpoint in PromoteKit for that flow with only the matching event enabled.`
    })
  },
  sampleData: {
    type: "affiliate.created",
    id: "aff_sample",
    email: "affiliate@example.com",
    first_name: "John",
    last_name: "Doe",
    approved: !1,
    banned: !1,
    created_at: "2024-01-01T00:00:00Z"
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    var _a;
    let payload = context.payload.body, selected = (_a = context.propsValue.eventTypes) != null ? _a : [];
    return payload.type === "affiliate.created" || payload.type === "affiliate.approved" ? selected.length > 0 && !selected.includes(payload.type) ? [] : [__spreadValues({ type: payload.type }, promotekitCommon.flattenAffiliate(payload.data))] : [];
  },
  async test(context) {
    var _a, _b;
    let response = await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common15.HttpMethod.GET,
      path: "/affiliates",
      queryParams: { limit: "5" }
    }), eventType = (_b = ((_a = context.propsValue.eventTypes) != null ? _a : [])[0]) != null ? _b : "affiliate.created";
    return response.body.data.map((item) => __spreadValues({
      type: eventType
    }, promotekitCommon.flattenAffiliate(item)));
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/triggers/new-referral-event.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var import_pieces_common16 = require("@activepieces/pieces-common");
var REFERRAL_EVENT_OPTIONS = [
  { label: "Referral Created", value: "referral.created" },
  { label: "Referral Converted", value: "referral.converted" }
], newReferralEvent = (0, import_pieces_framework16.createTrigger)({
  auth: promotekitAuth,
  name: "new_referral_event",
  displayName: "New Referral Event",
  description: "Triggers when a PromoteKit referral webhook event is received.",
  aiMetadata: {
    description: "Fires on a PromoteKit referral webhook event \u2014 when a referral is created or converted. Optionally filter to specific event types; leave the selection empty to fire on any referral event. Requires the webhook endpoint to be configured in the PromoteKit dashboard."
  },
  props: {
    eventTypes: import_pieces_framework16.Property.StaticMultiSelectDropdown({
      displayName: "Referral Event Types",
      description: "Select the referral event types to listen for. Leave empty to receive all referral events.",
      required: !1,
      options: { options: REFERRAL_EVENT_OPTIONS }
    }),
    instructions: import_pieces_framework16.Property.MarkDown({
      value: `### Setup Instructions

1. Go to your PromoteKit dashboard.
2. Navigate to **Settings > Webhooks**.
3. Click **Add Endpoint**.
4. Paste the webhook URL shown below.
\`\`\`text
            {{webhookUrl}}
            \`\`\`
5. Enable the referral event types you selected above.
6. Save the endpoint.

**Need different flows for different events?** Create a separate flow with its own webhook URL, and add a new endpoint in PromoteKit for that flow with only the matching event enabled.`
    })
  },
  sampleData: {
    type: "referral.created",
    id: "ref_sample",
    email: "referral@example.com",
    subscription_status: null,
    affiliate_id: "aff_sample",
    affiliate_email: "affiliate@example.com",
    created_at: "2024-01-01T00:00:00Z"
  },
  type: import_pieces_framework16.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    var _a;
    let payload = context.payload.body, selected = (_a = context.propsValue.eventTypes) != null ? _a : [];
    return payload.type === "referral.created" || payload.type === "referral.converted" ? selected.length > 0 && !selected.includes(payload.type) ? [] : [__spreadValues({ type: payload.type }, promotekitCommon.flattenReferral(payload.data))] : [];
  },
  async test(context) {
    var _a, _b;
    let response = await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common16.HttpMethod.GET,
      path: "/referrals",
      queryParams: { limit: "5" }
    }), eventType = (_b = ((_a = context.propsValue.eventTypes) != null ? _a : [])[0]) != null ? _b : "referral.created";
    return response.body.data.map((item) => __spreadValues({
      type: eventType
    }, promotekitCommon.flattenReferral(item)));
  }
});

// ../ap/packages/pieces/community/promotekit/src/lib/triggers/new-commission-event.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var import_pieces_common17 = require("@activepieces/pieces-common");
var newCommissionEvent = (0, import_pieces_framework17.createTrigger)({
  auth: promotekitAuth,
  name: "new_commission_event",
  displayName: "New Commission Event",
  description: "Triggers when a PromoteKit commission webhook event is received.",
  aiMetadata: {
    description: "Fires when a PromoteKit commission is created (a commission.created webhook event), representing a new commission earned by an affiliate. Requires the webhook endpoint to be configured in the PromoteKit dashboard."
  },
  props: {
    instructions: import_pieces_framework17.Property.MarkDown({
      value: `### Setup Instructions

1. Go to your PromoteKit dashboard.
2. Navigate to **Settings > Webhooks**.
3. Click **Add Endpoint**.
4. Paste the webhook URL shown below :
\`\`\`text
            {{webhookUrl}}
            \`\`\`
5. Enable the **Commission Created** event.
6. Save the endpoint.`
    })
  },
  sampleData: {
    type: "commission.created",
    id: "com_sample",
    revenue_amount: 100,
    currency: "usd",
    commission_amount: 20,
    payout_status: "pending",
    affiliate_id: "aff_sample",
    affiliate_email: "affiliate@example.com",
    created_at: "2024-01-01T00:00:00Z"
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  async onEnable() {
  },
  async onDisable() {
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.type !== "commission.created" ? [] : [__spreadValues({ type: payload.type }, promotekitCommon.flattenCommission(payload.data))];
  },
  async test(context) {
    return (await promotekitApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common17.HttpMethod.GET,
      path: "/commissions",
      queryParams: { limit: "5" }
    })).body.data.map((item) => __spreadValues({
      type: "commission.created"
    }, promotekitCommon.flattenCommission(item)));
  }
});

// ../ap/packages/pieces/community/promotekit/src/index.ts
var promotekitAuth = import_pieces_framework18.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your API key:
1. Log in to your PromoteKit dashboard.
2. Go to **Settings > API Keys**.
3. Click **Create API Key**.
4. Copy the key and paste it here.
`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common18.httpClient.sendRequest({
        method: import_pieces_common18.HttpMethod.GET,
        url: "https://www.promotekit.com/api/v1/affiliates",
        headers: { Authorization: `Bearer ${auth}` },
        queryParams: { limit: "1" }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API Key" };
    }
  }
}), promotekit = (0, import_pieces_framework18.createPiece)({
  displayName: "PromoteKit",
  description: "Affiliate marketing platform integrated with Stripe.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/promotekit.png",
  categories: [import_pieces_framework19.PieceCategory.MARKETING],
  auth: promotekitAuth,
  authors: ["BasTech"],
  actions: [
    listAffiliates,
    findAffiliate,
    createAffiliate,
    updateAffiliate,
    listReferrals,
    findReferral,
    createReferral,
    listCommissions,
    findCommission,
    listCampaigns,
    findCampaign,
    listPayouts,
    findPayout,
    (0, import_pieces_common18.createCustomApiCallAction)({
      baseUrl: () => "https://www.promotekit.com/api/v1",
      auth: promotekitAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth}`
      })
    })
  ],
  triggers: [newAffiliateEvent, newReferralEvent, newCommissionEvent]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  promotekit,
  promotekitAuth
});
