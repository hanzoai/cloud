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

// ../ap/packages/pieces/community/paywhirl/src/index.ts
var index_exports = {};
__export(index_exports, {
  paywhirl: () => paywhirl
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/paywhirl/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/paywhirl/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.paywhirl.com";
async function makeRequest(apikey, apisecret, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "api-key": apikey,
        "api-secret": apisecret,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/paywhirl/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), paywhirlAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "API Key",
  description: "Authenticate with your Paywhirl API Key",
  required: !0,
  props: {
    api_key: import_pieces_framework.Property.ShortText({
      displayName: "API Key",
      description: "Your Paywhirl API Key",
      required: !0
    }),
    api_secret: import_pieces_framework.Property.ShortText({
      displayName: "API Secret",
      description: "Your Paywhirl API Secret",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    if (auth)
      try {
        return await makeRequest(
          auth.api_key,
          auth.api_secret,
          import_pieces_common2.HttpMethod.GET,
          "/test"
        ), {
          valid: !0
        };
      } catch (error) {
        return {
          valid: !1,
          error: "Invalid Api Key"
        };
      }
    return {
      valid: !1,
      error: "Invalid Api Key"
    };
  }
});

// ../ap/packages/pieces/community/paywhirl/src/lib/actions/cancel-subscription.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), cancelSubscription = (0, import_pieces_framework2.createAction)({
  auth: paywhirlAuth,
  name: "cancelSubscription",
  displayName: "Cancel Subscription",
  description: "Cancel a customer's existing subscription. This will prevent the subscription from making any additional charges and unbind the customer from the plan.",
  audience: "both",
  aiMetadata: {
    description: "Cancels a PayWhirl subscription so it stops billing the customer. Target the subscription either by its subscription ID or by customer ID (one of the two is required). Use when ending recurring billing for a customer. Not idempotent: this mutates billing state, so guard against repeated calls.",
    idempotent: !1
  },
  props: {
    subscription_id: import_pieces_framework2.Property.Number({
      displayName: "Subscription ID",
      description: "Subscription ID",
      required: !1
    }),
    customer_id: import_pieces_framework2.Property.ShortText({
      displayName: "Customer ID",
      description: "Customer ID (can be used instead of subscription_id)",
      required: !1
    })
  },
  async run(context) {
    let { subscription_id, customer_id } = context.propsValue;
    if (!subscription_id && !customer_id)
      throw new Error("Either subscription_id or customer_id must be provided");
    let body = {};
    return subscription_id && (body.subscription_id = subscription_id), customer_id && (body.customer_id = customer_id), await makeRequest(
      context.auth.props.api_key,
      context.auth.props.api_secret,
      import_pieces_common3.HttpMethod.POST,
      "/unsubscribe/customer",
      body
    );
  }
});

// ../ap/packages/pieces/community/paywhirl/src/lib/actions/create-customer.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), createCustomer = (0, import_pieces_framework3.createAction)({
  auth: paywhirlAuth,
  name: "createCustomer",
  displayName: "Create Customer",
  description: "Create a new customer. This is required before binding a customer to a plan via a subscription.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new PayWhirl customer record. Required first name, last name, email, and currency code; optional address, payment gateway, and UTM attribution fields. Use this before subscribing a customer to a plan. Not idempotent: each call creates a separate customer, so re-running duplicates the record.",
    idempotent: !1
  },
  props: {
    first_name: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "First name",
      required: !0
    }),
    last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "Last name",
      required: !0
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "Email address",
      required: !0
    }),
    currency: import_pieces_framework3.Property.ShortText({
      displayName: "Currency",
      description: "Currency code (ex. USD, GBP, AUD, CAD, EUR, etc)",
      required: !0
    }),
    password: import_pieces_framework3.Property.ShortText({
      displayName: "Password",
      description: "Password (leave blank to let customer assign via secure link in welcome email)",
      required: !1
    }),
    phone: import_pieces_framework3.Property.Number({
      displayName: "Phone Number",
      description: "Phone number",
      required: !1
    }),
    address: import_pieces_framework3.Property.LongText({
      displayName: "Street Address",
      description: "Street address",
      required: !1
    }),
    city: import_pieces_framework3.Property.ShortText({
      displayName: "City",
      description: "City",
      required: !1
    }),
    state: import_pieces_framework3.Property.ShortText({
      displayName: "State / Region",
      description: "State / Region",
      required: !1
    }),
    zip: import_pieces_framework3.Property.ShortText({
      displayName: "Zip / Postal Code",
      description: "Zip / Postal Code",
      required: !1
    }),
    country: import_pieces_framework3.Property.ShortText({
      displayName: "Country Code",
      description: "Country Code (ex. US, GB, CA, AU, etc)",
      required: !1
    }),
    gateway_id: import_pieces_framework3.Property.ShortText({
      displayName: "Payment Gateway ID",
      description: "PayWhirl ID of payment gateway to attach to customer. If left blank, first loaded gateway will be used.",
      required: !1
    }),
    utm_source: import_pieces_framework3.Property.ShortText({
      displayName: "UTM Source",
      description: "Acquisition source (ex. google, bing, email, fall_campaign, etc)",
      required: !1
    }),
    utm_medium: import_pieces_framework3.Property.ShortText({
      displayName: "UTM Medium",
      description: "Acquisition medium (ex. CPC, banner, text_ad, etc)",
      required: !1
    }),
    utm_term: import_pieces_framework3.Property.ShortText({
      displayName: "UTM Term",
      description: "Acquisition keyword",
      required: !1
    }),
    utm_content: import_pieces_framework3.Property.ShortText({
      displayName: "UTM Content",
      description: "Acquisition Ad Content",
      required: !1
    }),
    utm_campaign: import_pieces_framework3.Property.ShortText({
      displayName: "UTM Campaign",
      description: "Acquisition Ad Campaign",
      required: !1
    }),
    utm_group: import_pieces_framework3.Property.ShortText({
      displayName: "UTM Group",
      description: "Acquisition Ad Group",
      required: !1
    })
  },
  async run(context) {
    let {
      first_name,
      last_name,
      email,
      currency,
      password,
      phone,
      address,
      city,
      state,
      zip,
      country,
      gateway_id,
      utm_source,
      utm_medium,
      utm_term,
      utm_content,
      utm_campaign,
      utm_group
    } = context.propsValue, body = {
      first_name,
      last_name,
      email,
      currency
    };
    return password && (body.password = password), phone && (body.phone = phone), address && (body.address = address), city && (body.city = city), state && (body.state = state), zip && (body.zip = zip), country && (body.country = country), gateway_id && (body.gateway_id = gateway_id), utm_source && (body.utm_source = utm_source), utm_medium && (body.utm_medium = utm_medium), utm_term && (body.utm_term = utm_term), utm_content && (body.utm_content = utm_content), utm_campaign && (body.utm_campaign = utm_campaign), utm_group && (body.utm_group = utm_group), await makeRequest(
      context.auth.props.api_key,
      context.auth.props.api_secret,
      import_pieces_common4.HttpMethod.POST,
      "/create/customer",
      body
    );
  }
});

// ../ap/packages/pieces/community/paywhirl/src/lib/actions/get-customer.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), getCustomer = (0, import_pieces_framework4.createAction)({
  auth: paywhirlAuth,
  name: "getCustomer",
  displayName: "Get Customers",
  description: "Retrieve a selection of customers. Useful for keeping track of customers in a local database.",
  audience: "both",
  aiMetadata: {
    description: "Lists PayWhirl customers, returning all customers by default or narrowing the set with an optional keyword filter plus pagination (limit, before/after ID) and sort controls. Use to look up or enumerate customers. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework4.Property.Number({
      displayName: "Limit",
      description: "Number of entries to return. Limit 100.",
      required: !1
    }),
    order_key: import_pieces_framework4.Property.ShortText({
      displayName: "Order Key",
      description: "Field to sort by. Default: id",
      required: !1
    }),
    order_direction: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Order Direction",
      description: "Direction to order results",
      options: {
        disabled: !1,
        options: [
          { label: "Ascending", value: "asc" },
          { label: "Descending", value: "desc" }
        ]
      },
      required: !1
    }),
    before_id: import_pieces_framework4.Property.Number({
      displayName: "Before ID",
      description: "All customers returned will have an ID less than this value",
      required: !1
    }),
    after_id: import_pieces_framework4.Property.Number({
      displayName: "After ID",
      description: "All customers returned will have an ID greater than this value",
      required: !1
    }),
    keyword: import_pieces_framework4.Property.ShortText({
      displayName: "Keyword",
      description: "Filter by keyword",
      required: !1
    })
  },
  async run(context) {
    let { limit, order_key, order_direction, before_id, after_id, keyword } = context.propsValue, queryParams = new URLSearchParams();
    limit && queryParams.append("limit", limit.toString()), order_key && queryParams.append("order_key", order_key), order_direction && queryParams.append("order_direction", order_direction), before_id && queryParams.append("before_id", before_id.toString()), after_id && queryParams.append("after_id", after_id.toString()), keyword && queryParams.append("keyword", keyword);
    let path = `/customers${queryParams.toString() ? `?${queryParams.toString()}` : ""}`;
    return await makeRequest(
      context.auth.props.api_key,
      context.auth.props.api_secret,
      import_pieces_common5.HttpMethod.GET,
      path
    );
  }
});

// ../ap/packages/pieces/community/paywhirl/src/lib/actions/subscribe-customer.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), subscribeCustomer = (0, import_pieces_framework5.createAction)({
  auth: paywhirlAuth,
  name: "subscribeCustomer",
  displayName: "Subscribe Customer",
  description: "Enroll a customer in an existing plan. The customer must have a valid payment method on file.",
  audience: "both",
  aiMetadata: {
    description: "Enrolls an existing customer into an existing plan, creating a recurring subscription. Requires customer ID and plan ID; optionally set a trial end timestamp, promo code, or quantity. The customer must already have a valid payment method on file. Use to start recurring billing. Not idempotent: each call creates another subscription.",
    idempotent: !1
  },
  props: {
    customer_id: import_pieces_framework5.Property.Number({
      displayName: "Customer ID",
      description: "Customer ID",
      required: !0
    }),
    plan_id: import_pieces_framework5.Property.Number({
      displayName: "Plan ID",
      description: "Plan ID",
      required: !0
    }),
    trial_end: import_pieces_framework5.Property.Number({
      displayName: "Trial End",
      description: "UNIX timestamp in the future (UTC timezone)",
      required: !1
    }),
    promo_id: import_pieces_framework5.Property.Number({
      displayName: "Promo ID",
      description: "ID of promo code to apply",
      required: !1
    }),
    quantity: import_pieces_framework5.Property.Number({
      displayName: "Quantity",
      description: "Quantity (Default: 1)",
      required: !1
    })
  },
  async run(context) {
    let { customer_id, plan_id, trial_end, promo_id, quantity } = context.propsValue, body = {
      customer_id,
      plan_id
    };
    return trial_end && (body.trial_end = trial_end), promo_id && (body.promo_id = promo_id), quantity && (body.quantity = quantity), await makeRequest(
      context.auth.props.api_key,
      context.auth.props.api_secret,
      import_pieces_common6.HttpMethod.POST,
      "/subscribe/customer",
      body
    );
  }
});

// ../ap/packages/pieces/community/paywhirl/src/index.ts
var import_pieces_common8 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/paywhirl/src/lib/actions/search-customers-subscription.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), searchCustomersSubscription = (0, import_pieces_framework6.createAction)({
  auth: paywhirlAuth,
  name: "searchCustomersSubscription",
  displayName: "Get Customer Subscriptions",
  description: "Get a list of subscriptions associated with a given customer. Useful for determining which services a customer is currently expecting.",
  audience: "both",
  aiMetadata: {
    description: "Lists a specific customer's subscriptions, filterable by status (active only by default, canceled only, or all). Requires the customer ID. Use to inspect which plans a customer is enrolled in. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    customer_id: import_pieces_framework6.Property.Number({
      displayName: "Customer ID",
      description: "Customer ID",
      required: !0
    }),
    status: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter by subscription status. Active (default) excludes canceled; Canceled shows only canceled; All shows all subscriptions.",
      options: {
        disabled: !1,
        options: [
          { label: "Active", value: "active" },
          { label: "Canceled", value: "canceled" },
          { label: "All", value: "all" }
        ]
      },
      required: !1
    })
  },
  async run(context) {
    let { customer_id, status } = context.propsValue, queryParams = new URLSearchParams();
    status && queryParams.append("status", status);
    let path = `/subscriptions/${customer_id}${queryParams.toString() ? `?${queryParams.toString()}` : ""}`;
    return await makeRequest(
      context.auth.props.api_key,
      context.auth.props.api_secret,
      import_pieces_common7.HttpMethod.GET,
      path
    );
  }
});

// ../ap/packages/pieces/community/paywhirl/src/index.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), paywhirl = (0, import_pieces_framework7.createPiece)({
  displayName: "Paywhirl",
  auth: paywhirlAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/paywhirl.png",
  description: "PayWhirls intuitive tools make recurring billing easy. Sell subscriptions, pre-orders, payment plans, or whatever billing arrangements you can dream up.",
  categories: [import_pieces_framework8.PieceCategory.ACCOUNTING, import_pieces_framework8.PieceCategory.PAYMENT_PROCESSING],
  authors: ["sanket-a11y"],
  actions: [
    cancelSubscription,
    createCustomer,
    getCustomer,
    searchCustomersSubscription,
    subscribeCustomer,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: paywhirlAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        "api-key": auth.props.api_key,
        "api-secret": auth.props.api_secret
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  paywhirl
});
