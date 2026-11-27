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

// ../ap/packages/pieces/community/moonclerk/src/index.ts
var index_exports = {};
__export(index_exports, {
  moonclerk: () => moonclerk
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/moonclerk/src/lib/common/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), moonclerkAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your Moonclerk API Key. You can find it in your Moonclerk account settings. (https://app.moonclerk.com/settings/api-key)",
  required: !0,
  validate: async ({ auth }) => {
    if (auth) {
      console.log(auth);
      try {
        return await import_pieces_common.httpClient.sendRequest({
          method: import_pieces_common.HttpMethod.GET,
          url: "https://api.moonclerk.com/forms",
          headers: {
            Authorization: `Token token=${auth}`,
            Accept: "application/vnd.moonclerk+json;version=1"
          }
        }), {
          valid: !0
        };
      } catch (error) {
        return {
          valid: !1,
          error: "Invalid API Key or authentication failed."
        };
      }
    }
    return {
      valid: !1,
      error: "API Key is required."
    };
  }
});

// ../ap/packages/pieces/community/moonclerk/src/lib/actions/retrive-plan.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), retrivePlan = (0, import_pieces_framework2.createAction)({
  auth: moonclerkAuth,
  name: "retrivePlan",
  displayName: "Retrieve Plan",
  description: "Retrieve a customer plan from MoonClerk using the customer ID",
  audience: "both",
  aiMetadata: { description: "Look up a MoonClerk customer by their customer ID and return the subscription plan attached to that customer. Use to fetch plan/pricing details (amount, interval, currency) for a known customer. Requires a valid customer ID; returns nothing if the customer has no subscription. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    customerId: import_pieces_framework2.Property.ShortText({
      displayName: "Customer ID",
      description: "The ID of the customer to retrieve the plan for",
      required: !0
    })
  },
  async run(context) {
    var _a, _b;
    let { customerId } = context.propsValue, apiKey = context.auth.secret_text;
    return (_b = (_a = (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `https://api.moonclerk.com/customers/${customerId}`,
      headers: {
        Authorization: `Token token=${apiKey}`,
        Accept: "application/vnd.moonclerk+json;version=1"
      }
    })).body.customer) == null ? void 0 : _a.subscription) == null ? void 0 : _b.plan;
  }
});

// ../ap/packages/pieces/community/moonclerk/src/lib/triggers/new-plan.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var newPlan = (0, import_pieces_framework3.createTrigger)({
  auth: moonclerkAuth,
  name: "newPlan",
  displayName: "New Plan",
  description: "Triggers when a new plan is created in MoonClerk",
  aiMetadata: {
    description: "Fires when a new recurring plan (subscription) is created in MoonClerk, typically when a customer completes a recurring checkout. The event payload carries the customer record along with the newly created subscription and its plan details. Requires a MoonClerk webhook configured for the Plan Created event."
  },
  props: {
    markdown: import_pieces_framework3.Property.MarkDown({
      value: `## MoonClerk Webhook Setup
                        To use this trigger, you need to manually set up a webhook in your MoonClerk account:
            
                        1. Login to your MoonClerk account.
                        2. Navigate to **Account** -> **Settings** -> **Webhooks**.
                        3. **Create webhook Endpoint**. keep state as active.
                        4. Specify the following URL in the **Webhook URL** field:
                        \`\`\`text
                        {{webhookUrl}}
                        \`\`\`
                        5. Select the **Plan Created** event.
                        5. Click Save Agent.
                        `
    })
  },
  sampleData: {
    event: "plan_created",
    object: "customer",
    data: {
      id: 523425,
      account_balance: 0,
      name: "Jim Customer",
      email: "customer@example.com",
      payment_method: {
        type: "card",
        last4: "4242",
        exp_month: 12,
        exp_year: 2018,
        brand: "Visa"
      },
      custom_id: "GHS430",
      customer_reference: "cus_4SOZuEc4cxP5L7",
      discount: {
        coupon: {
          code: "10off",
          duration: "once",
          amount_off: 1e3,
          currency: "USD",
          percent_off: null,
          duration_in_months: null,
          max_redemptions: null,
          redeem_by: null
        },
        starts_at: "2022-04-12T20:05:37Z",
        ends_at: "2022-05-12T20:05:37Z"
      },
      delinquent: !1,
      management_url: "https://app.moonclerk.com/manage/xyz1234567",
      custom_fields: {
        shirt_size: {
          id: 23452,
          type: "string",
          response: "XL"
        },
        shipping_address: {
          id: 23453,
          type: "address",
          response: {
            line1: "123 Main St.",
            line2: "Ste. 153",
            city: "Greenville",
            state: "SC",
            postal_code: "29651",
            country: "United States"
          }
        }
      },
      form_id: 101,
      checkout: {
        amount_due: 1700,
        coupon_amount: 1e3,
        coupon_code: "10off",
        date: "2014-07-23T13:44:12Z",
        fee: 200,
        subtotal: 1e3,
        token: "",
        total: 1700,
        trial_period_days: null,
        upfront_amount: 500
      },
      subscription: {
        id: 98,
        subscription_reference: "sub_3oLgqlp4MgTZC3",
        status: "active",
        start: "2022-07-23T13:44:16Z",
        first_payment_attempt: "2022-07-23T13:44:16Z",
        next_payment_attempt: "2022-08-23T13:44:16Z",
        current_period_start: "2022-07-23T13:44:16Z",
        current_period_end: "2022-08-23T13:44:16Z",
        trial_start: null,
        trial_end: null,
        trial_period_days: null,
        expires_at: null,
        canceled_at: null,
        ended_at: null,
        plan: {
          id: 131,
          plan_reference: "131",
          amount: 1200,
          amount_description: "Option A",
          currency: "USD",
          interval: "month",
          interval_count: 1
        }
      }
    }
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/moonclerk/src/lib/triggers/payment-succeeds.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var paymentSucceeds = (0, import_pieces_framework4.createTrigger)({
  auth: moonclerkAuth,
  name: "paymentSucceeds",
  displayName: "Payment Succeeds",
  description: "",
  aiMetadata: {
    description: "Fires when a payment in MoonClerk succeeds (is collected successfully). Use to react only to successful charges, distinct from the New Payment trigger which fires on payments in any state. Requires a MoonClerk webhook configured for the Payment Succeeds event."
  },
  props: {
    markdown: import_pieces_framework4.Property.MarkDown({
      value: `## MoonClerk Webhook Setup
                                To use this trigger, you need to manually set up a webhook in your MoonClerk account:
                    
                                1. Login to your MoonClerk account.
                                2. Navigate to **Account** -> **Settings** -> **Webhooks**.
                                3. **Create webhook Endpoint**. keep state as active.
                                4. Specify the following URL in the **Webhook URL** field:
                                \`\`\`text
                                {{webhookUrl}}
                                \`\`\`
                                5. Select the **Payment Succeeds** event.
                                5. Click Save Agent.
                                `
    })
  },
  sampleData: {},
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/moonclerk/src/lib/triggers/new-payment.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var newPayment = (0, import_pieces_framework5.createTrigger)({
  auth: moonclerkAuth,
  name: "newPayment",
  displayName: "New Payment",
  description: "Triggers when a payment is created in any state (failed, succeeded, etc).",
  aiMetadata: {
    description: "Fires whenever a payment is created in MoonClerk regardless of outcome \u2014 successful, failed, or otherwise \u2014 so the handler must inspect the payment status field to branch. Use to capture every payment attempt; choose Payment Succeeds instead to react only to successful charges. Requires a MoonClerk webhook configured for the Payment Created event."
  },
  props: {
    markdown: import_pieces_framework5.Property.MarkDown({
      value: `## MoonClerk Webhook Setup
                To use this trigger, you need to manually set up a webhook in your MoonClerk account:
    
                1. Login to your MoonClerk account.
                2. Navigate to **Account** -> **Settings** -> **Webhooks**.
                3. **Create webhook Endpoint**. keep state as active.
                4. Specify the following URL in the **Webhook URL** field:
                \`\`\`text
                {{webhookUrl}}
                \`\`\`
                5. Select the **Payment Created** event.
                5. Click Save Agent.
                `
    })
  },
  sampleData: {
    event: "payment_created",
    object: "payment",
    data: {
      id: 1348394,
      date: "2022-04-08T18:57:26Z",
      status: "successful",
      currency: "USD",
      amount: 1e3,
      fee: 59,
      amount_refunded: 0,
      amount_description: "Option A",
      name: "Jim Customer",
      email: "customer@example.com",
      payment_method: {
        type: "card",
        last4: "4242",
        brand: "Visa"
      },
      charge_reference: "ch_3ohpsF8ra5rqjj",
      // customer_id and customer_reference are available only if the payment was from a recurring checkout
      customer_id: 53453,
      customer_reference: "cus_4SOZuEc4cxP5L7",
      invoice_reference: "in_1La8pLqS2UnhPZ",
      custom_fields: {
        shirt_size: {
          id: 23452,
          type: "string",
          response: "XL"
        },
        shipping_address: {
          id: 23453,
          type: "address",
          response: {
            line1: "123 Main St.",
            line2: "Ste. 153",
            city: "Greenville",
            state: "SC",
            postal_code: "29651",
            country: "United States"
          }
        }
      },
      form_id: 112,
      coupon: {
        code: "10off",
        duration: "once",
        amount_off: 1e3,
        currency: "USD",
        percent_off: null,
        duration_in_months: null,
        max_redemptions: null,
        redeem_by: null
      },
      custom_id: "GHS430",
      // Checkout data is available only if the payment was from a one-time checkout
      checkout: {
        amount_due: 1e3,
        coupon_amount: 0,
        coupon_code: null,
        date: "2022-04-08T18:57:26Z",
        fee: 0,
        subtotal: 1e3,
        token: "",
        total: 1e3,
        trial_period_days: null,
        upfront_amount: 0
      }
    }
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/moonclerk/src/lib/triggers/plan-ended.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var planEnded = (0, import_pieces_framework6.createTrigger)({
  auth: moonclerkAuth,
  name: "planEnded",
  displayName: "Plan Ended",
  description: "Triggers when a plan is canceled or when the set number periods have been exhausted.",
  aiMetadata: {
    description: "Fires when a MoonClerk recurring plan ends \u2014 either because it was canceled or because its configured number of billing periods has been exhausted. Use to handle subscription termination (offboarding, access revocation, churn tracking). Requires a MoonClerk webhook configured for the corresponding plan-ended event."
  },
  props: {
    markdown: import_pieces_framework6.Property.MarkDown({
      value: `## MoonClerk Webhook Setup
                                To use this trigger, you need to manually set up a webhook in your MoonClerk account:
                    
                                1. Login to your MoonClerk account.
                                2. Navigate to **Account** -> **Settings** -> **Webhooks**.
                                3. **Create webhook Endpoint**. keep state as active.
                                4. Specify the following URL in the **Webhook URL** field:
                                \`\`\`text
                                {{webhookUrl}}
                                \`\`\`
                                5. Select the **Plan Created** event.
                                5. Click Save Agent.
                                `
    })
  },
  sampleData: {},
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/moonclerk/src/lib/triggers/plan-payment-failed.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var planPaymentFailed = (0, import_pieces_framework7.createTrigger)({
  auth: moonclerkAuth,
  name: "planPaymentFailed",
  displayName: "Plan Payment Failed",
  description: "Triggers when a payment related to a plan has failed.",
  aiMetadata: {
    description: "Fires when a recurring payment tied to a MoonClerk plan (subscription) fails to collect. Use to handle dunning, failed-renewal alerts, or retry workflows for active subscriptions. Requires a MoonClerk webhook configured for the Plan Payment Failed event."
  },
  props: {
    markdown: import_pieces_framework7.Property.MarkDown({
      value: `## MoonClerk Webhook Setup
                                    To use this trigger, you need to manually set up a webhook in your MoonClerk account:
                        
                                    1. Login to your MoonClerk account.
                                    2. Navigate to **Account** -> **Settings** -> **Webhooks**.
                                    3. **Create webhook Endpoint**. keep state as active.
                                    4. Specify the following URL in the **Webhook URL** field:
                                    \`\`\`text
                                    {{webhookUrl}}
                                    \`\`\`
                                    5. Select the **Plan Payment Failed** event.
                                    5. Click Save Agent.
                                    `
    })
  },
  sampleData: {},
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/moonclerk/src/index.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework"), moonclerk = (0, import_pieces_framework8.createPiece)({
  displayName: "Moonclerk",
  auth: moonclerkAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/moonclerk.png",
  authors: ["sanket-a11y"],
  description: "MoonClerk is an easy-to-use payment form builder for Stripe.",
  categories: [import_pieces_framework9.PieceCategory.COMMERCE],
  actions: [
    retrivePlan,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: moonclerkAuth,
      baseUrl: () => "https://api.moonclerk.com",
      authMapping: async (auth) => ({
        Authorization: `Token token=${auth.secret_text}`,
        Accept: "application/vnd.moonclerk+json;version=1"
      })
    })
  ],
  triggers: [
    newPayment,
    newPlan,
    paymentSucceeds,
    planEnded,
    planPaymentFailed
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  moonclerk
});
