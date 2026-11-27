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

// ../ap/packages/pieces/community/baremetrics/src/index.ts
var index_exports = {};
__export(index_exports, {
  baremetrics: () => baremetrics
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/baremetrics/src/lib/common/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/baremetrics/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
function getAuthentication(auth) {
  switch (auth.type) {
    case import_pieces_framework.AppConnectionType.OAUTH2:
    case import_pieces_framework.AppConnectionType.CLOUD_OAUTH2:
    case import_pieces_framework.AppConnectionType.PLATFORM_OAUTH2:
      return {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.access_token
      };
    case import_pieces_framework.AppConnectionType.SECRET_TEXT:
      return {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      };
  }
}
async function baremetricsApiCall({
  method,
  path,
  auth,
  body
}) {
  let request = {
    method,
    url: `https://api.baremetrics.com/v1${path}`,
    authentication: getAuthentication(auth),
    headers: {
      "Content-Type": "application/json"
    },
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}

// ../ap/packages/pieces/community/baremetrics/src/lib/common/auth.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), BAREMETRICS_SCOPES = [
  "read",
  "write"
], baremetricsAuth = [
  import_pieces_framework2.PieceAuth.OAuth2({
    description: "Connect your Baremetrics account using OAuth2. Requires approval from Baremetrics support to get Client ID and Client Secret.",
    authUrl: "https://app.baremetrics.com/oauth/authorize",
    tokenUrl: "https://app.baremetrics.com/oauth/token",
    required: !0,
    scope: [...BAREMETRICS_SCOPES],
    validate: async ({ auth }) => {
      try {
        return await baremetricsApiCall({
          method: import_pieces_common2.HttpMethod.GET,
          path: "/account",
          auth: {
            type: import_pieces_framework3.AppConnectionType.OAUTH2,
            access_token: auth.access_token
          }
        }), { valid: !0 };
      } catch (e) {
        return {
          valid: !1,
          error: e.message
        };
      }
    }
  }),
  import_pieces_framework2.PieceAuth.SecretText({
    displayName: "API Key",
    required: !0,
    description: "API key found in Baremetrics Settings area",
    validate: async ({ auth }) => {
      try {
        return await baremetricsApiCall({
          method: import_pieces_common2.HttpMethod.GET,
          path: "/account",
          auth: {
            type: import_pieces_framework3.AppConnectionType.SECRET_TEXT,
            secret_text: auth
          }
        }), { valid: !0 };
      } catch (e) {
        return {
          valid: !1,
          error: e.message
        };
      }
    }
  })
];

// ../ap/packages/pieces/community/baremetrics/src/lib/actions/create-customer.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), createCustomer = (0, import_pieces_framework4.createAction)({
  name: "create_customer",
  classification: "WRITE",
  displayName: "Create Customer",
  description: "Creates a new customer record",
  audience: "both",
  aiMetadata: { description: "Creates a customer under a Baremetrics data source, keyed on your own unique customer ID (oid); pick the source from the connected providers. Use to register a customer before attaching subscriptions. Idempotent: the source treats oid as the unique key, so re-running with the same oid updates that customer rather than creating a duplicate.", idempotent: !0 },
  auth: baremetricsAuth,
  props: {
    source_id: import_pieces_framework4.Property.Dropdown({
      auth: baremetricsAuth,
      displayName: "Source",
      description: "Select the data source (Payment Provider or API)",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          return {
            disabled: !1,
            options: (await baremetricsApiCall({
              method: import_pieces_common3.HttpMethod.GET,
              path: "/sources",
              auth
            })).sources.map((source) => ({
              label: `${source.provider} (${source.id})`,
              value: source.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load sources"
          };
        }
      }
    }),
    oid: import_pieces_framework4.Property.ShortText({
      displayName: "Customer ID",
      description: "Your unique ID for this customer",
      required: !0
    }),
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Name",
      description: "Customer name",
      required: !1
    }),
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email",
      description: "Email address for profile information lookup",
      required: !1
    }),
    notes: import_pieces_framework4.Property.LongText({
      displayName: "Notes",
      description: "Your own notes for this customer",
      required: !1
    }),
    created: import_pieces_framework4.Property.DateTime({
      displayName: "Created Date",
      description: "When this customer was created (defaults to now)",
      required: !1
    })
  },
  async run(context) {
    let { source_id, oid, name, email, notes, created } = context.propsValue, body = { oid };
    return name && (body.name = name), email && (body.email = email), notes && (body.notes = notes), created && (body.created = created), await baremetricsApiCall({
      method: import_pieces_common3.HttpMethod.POST,
      path: `/${source_id}/customers`,
      auth: context.auth,
      body
    });
  }
});

// ../ap/packages/pieces/community/baremetrics/src/lib/actions/create-plan.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), createPlan = (0, import_pieces_framework5.createAction)({
  name: "create_plan",
  classification: "WRITE",
  displayName: "Create Plan",
  description: "Creates a new plan for use when creating or updating subscriptions",
  audience: "both",
  aiMetadata: { description: "Creates a billing plan under a Baremetrics data source, keyed on your own unique plan ID (oid), with currency, amount (in cents), and billing interval. Use to define a plan before creating subscriptions against it. Idempotent: the source treats oid as the unique key, so re-running with the same oid updates that plan rather than creating a duplicate.", idempotent: !0 },
  auth: baremetricsAuth,
  props: {
    source_id: import_pieces_framework5.Property.Dropdown({
      auth: baremetricsAuth,
      displayName: "Source",
      description: "Select the data source (Payment Provider or API)",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          return {
            disabled: !1,
            options: (await baremetricsApiCall({
              method: import_pieces_common4.HttpMethod.GET,
              path: "/sources",
              auth
            })).sources.map((source) => ({
              label: `${source.provider} (${source.id})`,
              value: source.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load sources"
          };
        }
      }
    }),
    oid: import_pieces_framework5.Property.ShortText({
      displayName: "Plan ID",
      description: "Your unique ID for this plan",
      required: !0
    }),
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Plan Name",
      description: "Your internal name for this plan. This will be displayed in the Plan Breakout section",
      required: !0
    }),
    currency: import_pieces_framework5.Property.ShortText({
      displayName: "Currency",
      description: "The ISO code of the currency of this plan (e.g., USD)",
      required: !0
    }),
    amount: import_pieces_framework5.Property.Number({
      displayName: "Amount",
      description: "How much is this plan? (In cents)",
      required: !0
    }),
    interval: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Interval",
      description: "Billing interval",
      required: !0,
      options: {
        options: [
          { label: "Day", value: "day" },
          { label: "Month", value: "month" },
          { label: "Year", value: "year" }
        ]
      }
    }),
    interval_count: import_pieces_framework5.Property.Number({
      displayName: "Interval Count",
      description: "Number of intervals between billings (e.g., 1 for every month, 3 for every 3 months)",
      required: !0
    }),
    trial_duration: import_pieces_framework5.Property.Number({
      displayName: "Trial Duration",
      description: "The duration of this trial",
      required: !1
    }),
    trial_duration_unit: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Trial Duration Unit",
      description: "Unit for trial duration",
      required: !1,
      options: {
        options: [
          { label: "Day", value: "day" },
          { label: "Month", value: "month" },
          { label: "Year", value: "year" }
        ]
      }
    })
  },
  async run(context) {
    let { source_id, oid, name, currency, amount, interval, interval_count, trial_duration, trial_duration_unit } = context.propsValue, body = {
      oid,
      name,
      currency,
      amount,
      interval,
      interval_count
    };
    return trial_duration !== void 0 && (body.trial_duration = trial_duration), trial_duration_unit && (body.trial_duration_unit = trial_duration_unit), await baremetricsApiCall({
      method: import_pieces_common4.HttpMethod.POST,
      path: `/${source_id}/plans`,
      auth: context.auth,
      body
    });
  }
});

// ../ap/packages/pieces/community/baremetrics/src/lib/actions/create-subscription.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), createSubscription = (0, import_pieces_framework6.createAction)({
  name: "create_subscription",
  classification: "WRITE",
  displayName: "Create Subscription",
  description: "Creates a new subscription for a customer on a plan",
  audience: "both",
  aiMetadata: { description: "Creates a subscription linking an existing customer to an existing plan under a Baremetrics data source, keyed on your own unique subscription ID (oid). Requires the customer and plan to already exist in that source. Idempotent: the source treats oid as the unique key, so re-running with the same oid updates that subscription rather than creating a duplicate.", idempotent: !0 },
  auth: baremetricsAuth,
  props: {
    source_id: import_pieces_framework6.Property.Dropdown({
      auth: baremetricsAuth,
      displayName: "Source",
      description: "Select the data source (Payment Provider or API)",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          return {
            disabled: !1,
            options: (await baremetricsApiCall({
              method: import_pieces_common5.HttpMethod.GET,
              path: "/sources",
              auth
            })).sources.map((source) => ({
              label: `${source.provider} (${source.id})`,
              value: source.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load sources"
          };
        }
      }
    }),
    oid: import_pieces_framework6.Property.ShortText({
      displayName: "Subscription ID",
      description: "Your unique ID for this subscription",
      required: !0
    }),
    started_at: import_pieces_framework6.Property.DateTime({
      displayName: "Start Date",
      description: "When this subscription started",
      required: !0
    }),
    canceled_at: import_pieces_framework6.Property.DateTime({
      displayName: "Cancellation Date",
      description: "When this subscription was or should be canceled (optional)",
      required: !1
    }),
    customer_oid: import_pieces_framework6.Property.Dropdown({
      displayName: "Customer",
      auth: baremetricsAuth,
      description: "Select the customer for this subscription",
      required: !0,
      refreshers: ["source_id"],
      options: async (propsValue) => {
        let auth = propsValue.auth, sourceId = propsValue.source_id;
        if (!auth || !sourceId)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please select a source first"
          };
        try {
          return {
            disabled: !1,
            options: (await baremetricsApiCall({
              method: import_pieces_common5.HttpMethod.GET,
              path: `/${sourceId}/customers`,
              auth
            })).customers.map((customer) => ({
              label: customer.name || customer.email || customer.oid,
              value: customer.oid
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load customers"
          };
        }
      }
    }),
    plan_oid: import_pieces_framework6.Property.Dropdown({
      displayName: "Plan",
      auth: baremetricsAuth,
      description: "Select the plan for this subscription",
      required: !0,
      refreshers: ["source_id"],
      options: async (propsValue) => {
        let auth = propsValue.auth, sourceId = propsValue.source_id;
        if (!auth || !sourceId)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please select a source first"
          };
        try {
          return {
            disabled: !1,
            options: (await baremetricsApiCall({
              method: import_pieces_common5.HttpMethod.GET,
              path: `/${sourceId}/plans`,
              auth
            })).plans.map((plan) => ({
              label: `${plan.name} (${plan.currency} ${plan.amount / 100})`,
              value: plan.oid
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load plans"
          };
        }
      }
    }),
    quantity: import_pieces_framework6.Property.Number({
      displayName: "Quantity",
      description: "Number of subscriptions",
      required: !1,
      defaultValue: 1
    }),
    discount: import_pieces_framework6.Property.Number({
      displayName: "Discount",
      description: "Discount amount (in the same currency as the plan)",
      required: !1
    }),
    addons: import_pieces_framework6.Property.Array({
      displayName: "Addons",
      description: "Additional charges to add to the base plan",
      required: !1,
      properties: {
        oid: import_pieces_framework6.Property.ShortText({
          displayName: "Addon ID",
          description: "Your unique ID for this addon",
          required: !0
        }),
        amount: import_pieces_framework6.Property.Number({
          displayName: "Amount",
          description: "Addon amount (in cents)",
          required: !0
        }),
        quantity: import_pieces_framework6.Property.Number({
          displayName: "Quantity",
          description: "Addon quantity",
          required: !0,
          defaultValue: 1
        })
      }
    })
  },
  async run(context) {
    let { source_id, oid, started_at, canceled_at, customer_oid, plan_oid, quantity, discount, addons } = context.propsValue, body = {
      oid,
      started_at,
      customer_oid,
      plan_oid
    };
    return canceled_at && (body.canceled_at = canceled_at), quantity !== void 0 && quantity !== 1 && (body.quantity = quantity), discount !== void 0 && (body.discount = discount), addons && addons.length > 0 && (body.addons = addons), await baremetricsApiCall({
      method: import_pieces_common5.HttpMethod.POST,
      path: `/${source_id}/subscriptions`,
      auth: context.auth,
      body
    });
  }
});

// ../ap/packages/pieces/community/baremetrics/src/lib/actions/update-customer.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), updateCustomer = (0, import_pieces_framework7.createAction)({
  name: "update_customer",
  classification: "WRITE",
  displayName: "Update Customer",
  description: "Updates the basic information stored on a customer",
  audience: "both",
  aiMetadata: { description: "Updates an existing customer (name, email, notes, created date) identified by its customer_oid within a Baremetrics data source; only provided fields are changed. Use to amend customer details rather than register a new one. Idempotent: repeating with the same input leaves the customer in the same state.", idempotent: !0 },
  auth: baremetricsAuth,
  props: {
    source_id: import_pieces_framework7.Property.Dropdown({
      auth: baremetricsAuth,
      displayName: "Source",
      description: "Select the data source (Payment Provider or API)",
      required: !0,
      refreshers: [],
      options: async (propsValue) => {
        if (!propsValue.auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your account first"
          };
        try {
          return {
            disabled: !1,
            options: (await baremetricsApiCall({
              method: import_pieces_common6.HttpMethod.GET,
              path: "/sources",
              auth: propsValue.auth
            })).sources.map((source) => ({
              label: `${source.provider} (${source.id})`,
              value: source.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load sources"
          };
        }
      }
    }),
    customer_oid: import_pieces_framework7.Property.Dropdown({
      displayName: "Customer",
      auth: baremetricsAuth,
      description: "Select the customer to update",
      required: !0,
      refreshers: ["source_id"],
      options: async (propsValue) => {
        let auth = propsValue.auth, sourceId = propsValue.source_id;
        if (!auth || !sourceId)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please select a source first"
          };
        try {
          return {
            disabled: !1,
            options: (await baremetricsApiCall({
              method: import_pieces_common6.HttpMethod.GET,
              path: `/${sourceId}/customers`,
              auth
            })).customers.map((customer) => ({
              label: customer.name || customer.email || customer.oid,
              value: customer.oid
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load customers"
          };
        }
      }
    }),
    name: import_pieces_framework7.Property.ShortText({
      displayName: "Name",
      description: "Customer name",
      required: !1
    }),
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Email",
      description: "Email address for this customer",
      required: !1
    }),
    notes: import_pieces_framework7.Property.LongText({
      displayName: "Notes",
      description: "Your own notes for this customer",
      required: !1
    }),
    created: import_pieces_framework7.Property.DateTime({
      displayName: "Created Date",
      description: "Unix timestamp of when this customer was created",
      required: !1
    })
  },
  async run(context) {
    let { source_id, customer_oid, name, email, notes, created } = context.propsValue, body = {};
    return name && (body.name = name), email && (body.email = email), notes && (body.notes = notes), created && (body.created = created), await baremetricsApiCall({
      method: import_pieces_common6.HttpMethod.PUT,
      path: `/${source_id}/customers/${customer_oid}`,
      auth: context.auth,
      body
    });
  }
});

// ../ap/packages/pieces/community/baremetrics/src/index.ts
var baremetrics = (0, import_pieces_framework8.createPiece)({
  displayName: "Baremetrics",
  description: "Analytics and metrics platform for subscription businesses. Create customers, plans, and subscriptions to track your revenue and customer data.",
  categories: [import_pieces_framework9.PieceCategory.COMMERCE],
  auth: baremetricsAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/baremetrics.png",
  authors: ["onyedikachi-david"],
  actions: [createCustomer, createPlan, createSubscription, updateCustomer],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  baremetrics
});
