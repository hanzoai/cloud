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

// ../ap/packages/pieces/community/lemon-squeezy/src/index.ts
var index_exports = {};
__export(index_exports, {
  lemonSqueezy: () => lemonSqueezy
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework25 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/common/api.ts
var import_pieces_common = require("@activepieces/pieces-common"), LEMON_SQUEEZY_API_BASE = "https://api.lemonsqueezy.com/v1";
function getLemonSqueezyHeaders(apiKey) {
  return {
    Authorization: `Bearer ${apiKey}`,
    Accept: "application/vnd.api+json",
    "Content-Type": "application/vnd.api+json"
  };
}
function buildQueryString(params) {
  let parts = [];
  for (let [key, value] of Object.entries(params))
    parts.push(`${key}=${encodeURIComponent(value)}`);
  return parts.length > 0 ? `?${parts.join("&")}` : "";
}
async function fetchStoreOptions(apiKey) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${LEMON_SQUEEZY_API_BASE}/stores?page[size]=100`,
    headers: getLemonSqueezyHeaders(apiKey)
  })).body.data.map((store) => ({
    label: store.attributes.name,
    value: store.id
  }));
}
async function fetchOrderOptions(apiKey, storeId) {
  let params = { "page[size]": "100" };
  return storeId && (params["filter[store_id]"] = storeId), (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${LEMON_SQUEEZY_API_BASE}/orders${buildQueryString(params)}`,
    headers: getLemonSqueezyHeaders(apiKey)
  })).body.data.map((order) => ({
    label: `#${order.attributes.order_number} \u2014 ${order.attributes.user_name} (${order.attributes.status_formatted})`,
    value: order.id
  }));
}
async function fetchProductOptions(apiKey, storeId) {
  let params = { "page[size]": "100" };
  return storeId && (params["filter[store_id]"] = storeId), (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${LEMON_SQUEEZY_API_BASE}/products${buildQueryString(params)}`,
    headers: getLemonSqueezyHeaders(apiKey)
  })).body.data.map((product) => ({
    label: product.attributes.name,
    value: product.id
  }));
}
async function fetchVariantOptions(apiKey, productId) {
  let params = { "page[size]": "100" };
  return productId && (params["filter[product_id]"] = productId), (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${LEMON_SQUEEZY_API_BASE}/variants${buildQueryString(params)}`,
    headers: getLemonSqueezyHeaders(apiKey)
  })).body.data.map((variant) => ({
    label: variant.attributes.name,
    value: variant.id
  }));
}

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/common/auth.ts
var lemonSqueezyAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To get your API Key:

1. Go to your Lemon Squeezy dashboard
2. Navigate to Settings \xBB API
3. Create a new API key
4. Copy the API key and paste it here
`,
  required: !0,
  validate: async ({ auth }) => {
    if (!auth)
      return {
        valid: !1,
        error: "API key is required"
      };
    try {
      return (await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `${LEMON_SQUEEZY_API_BASE}/stores`,
        headers: {
          Authorization: `Bearer ${auth}`,
          Accept: "application/vnd.api+json",
          "Content-Type": "application/vnd.api+json"
        }
      })).status === 200 ? {
        valid: !0
      } : {
        valid: !1,
        error: "Invalid API key"
      };
    } catch (error) {
      return {
        valid: !1,
        error: "Invalid API key. Please check your API key and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/order-created.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/common/webhook.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var subscribeWebhook = async (auth, storeId, events, targetUrl, secret) => (await import_pieces_common3.httpClient.sendRequest({
  method: import_pieces_common3.HttpMethod.POST,
  url: `${LEMON_SQUEEZY_API_BASE}/webhooks`,
  headers: {
    Authorization: `Bearer ${auth}`,
    Accept: "application/vnd.api+json",
    "Content-Type": "application/vnd.api+json"
  },
  body: {
    data: {
      type: "webhooks",
      attributes: {
        url: targetUrl,
        events,
        secret,
        test_mode: !1
      },
      relationships: {
        store: {
          data: {
            type: "stores",
            id: storeId
          }
        }
      }
    }
  }
})).body, unsubscribeWebhook = async (auth, webhookId) => {
  await import_pieces_common3.httpClient.sendRequest({
    method: import_pieces_common3.HttpMethod.DELETE,
    url: `${LEMON_SQUEEZY_API_BASE}/webhooks/${webhookId}`,
    headers: {
      Authorization: `Bearer ${auth}`,
      Accept: "application/vnd.api+json",
      "Content-Type": "application/vnd.api+json"
    }
  });
}, getStores = async (auth) => (await import_pieces_common3.httpClient.sendRequest({
  method: import_pieces_common3.HttpMethod.GET,
  url: `${LEMON_SQUEEZY_API_BASE}/stores`,
  headers: {
    Authorization: `Bearer ${auth}`,
    Accept: "application/vnd.api+json",
    "Content-Type": "application/vnd.api+json"
  }
})).body.data, createStoreDropdownProperty = () => import_pieces_framework2.Property.Dropdown({
  displayName: "Store",
  required: !0,
  refreshers: ["auth"],
  auth: lemonSqueezyAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please authenticate first"
      };
    try {
      return {
        options: (await getStores(auth.secret_text)).map((store) => ({
          label: store.attributes.name,
          value: store.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load stores"
      };
    }
  }
}), generateWebhookSecret = () => `lemonsqueezy_${Date.now()}_${Math.random().toString(36).substring(2)}`, createWebhookTriggerKey = (eventName) => `_${eventName}_trigger`;

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/order-created.ts
var orderCreatedTrigger = (0, import_pieces_framework3.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "order_created",
  classification: "READ",
  displayName: "Order Created",
  description: "Triggers when a new order is successfully placed",
  aiMetadata: {
    description: "Fires when a new order is created in Lemon Squeezy (a customer completes a purchase). Represents the order record at creation, including customer, product/variant, totals, and status."
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["order_created"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("order_created"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("order_created")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "order_created",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "orders",
      id: "1",
      attributes: {
        store_id: 1,
        customer_id: 1,
        product_id: 1,
        variant_id: 1,
        user_name: "John Doe",
        user_email: "johndoe@example.com",
        currency: "USD",
        currency_rate: "1.0000",
        subtotal: 999,
        discount_total: 0,
        tax: 200,
        total: 1199,
        subtotal_usd: 999,
        discount_total_usd: 0,
        tax_usd: 200,
        total_usd: 1199,
        subtotal_formatted: "$9.99",
        discount_total_formatted: "$0.00",
        tax_formatted: "$2.00",
        total_formatted: "$11.99",
        status: "pending",
        status_formatted: "Pending",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T09:45:53.000000Z",
        test_mode: !1,
        order_number: 1,
        first_order_item: {
          id: 1,
          order_id: 1,
          product_id: 1,
          variant_id: 1,
          quantity: 1,
          price: 999,
          total: 999,
          subtotal: 999,
          tax: 0,
          created_at: "2021-08-17T09:45:53.000000Z",
          updated_at: "2021-08-17T09:45:53.000000Z"
        }
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/order-refunded.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var orderRefundedTrigger = (0, import_pieces_framework4.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "order_refunded",
  classification: "READ",
  displayName: "Order Refunded",
  description: "Triggers when a full or partial refund is made on an order",
  aiMetadata: {
    description: "Fires when an order in Lemon Squeezy is refunded, either fully or partially. Represents the updated order with its refunded amount and refunded status."
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["order_refunded"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("order_refunded"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("order_refunded")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "order_refunded",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "orders",
      id: "1",
      attributes: {
        store_id: 1,
        customer_id: 1,
        product_id: 1,
        variant_id: 1,
        user_name: "John Doe",
        user_email: "johndoe@example.com",
        currency: "USD",
        currency_rate: "1.0000",
        subtotal: 999,
        discount_total: 0,
        tax: 200,
        total: 1199,
        subtotal_usd: 999,
        discount_total_usd: 0,
        tax_usd: 200,
        total_usd: 1199,
        subtotal_formatted: "$9.99",
        discount_total_formatted: "$0.00",
        tax_formatted: "$2.00",
        total_formatted: "$11.99",
        status: "refunded",
        status_formatted: "Refunded",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T10:45:53.000000Z",
        test_mode: !1,
        order_number: 1,
        refunded_amount: 1199,
        refunded_amount_formatted: "$11.99"
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/subscription-created.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var subscriptionCreatedTrigger = (0, import_pieces_framework5.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "subscription_created",
  classification: "READ",
  displayName: "Subscription Created",
  description: "Triggers when a new subscription is successfully created",
  aiMetadata: {
    description: "Fires when a new subscription is created in Lemon Squeezy (a customer subscribes to a recurring plan). Represents the new subscription record, including customer, product/variant, status, and renewal dates."
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["subscription_created"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("subscription_created"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("subscription_created")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "subscription_created",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "subscriptions",
      id: "1",
      attributes: {
        store_id: 1,
        customer_id: 1,
        order_id: 1,
        order_item_id: 1,
        product_id: 1,
        variant_id: 1,
        product_name: "Lemonade - 2 years",
        variant_name: "Default",
        user_name: "John Doe",
        user_email: "johndoe@example.com",
        currency: "USD",
        currency_rate: "1.0000",
        status: "active",
        status_formatted: "Active",
        trial_ends_at: null,
        ends_at: "2023-08-17T09:45:53.000000Z",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T09:45:53.000000Z",
        test_mode: !1,
        pause_mode: null,
        cancelled_at: null,
        billing_anchor: 17,
        billing_anchor_type: "day_of_month",
        first_subscription_item: {
          id: 1,
          subscription_id: 1,
          price_id: 1,
          quantity: 1,
          is_usage_based: !1,
          created_at: "2021-08-17T09:45:53.000000Z",
          updated_at: "2021-08-17T09:45:53.000000Z"
        },
        urls: {
          customer_portal: "https://app.lemonsqueezy.com/subscription/1/customer-portal",
          update_payment_method: "https://app.lemonsqueezy.com/subscription/1/update-payment-method"
        },
        renews_at: "2021-09-17T09:45:53.000000Z"
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/subscription-updated.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var subscriptionUpdatedTrigger = (0, import_pieces_framework6.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "subscription_updated",
  classification: "READ",
  displayName: "Subscription Updated",
  description: "Triggers when a subscription's data is changed or updated",
  aiMetadata: {
    description: "Fires whenever a subscription in Lemon Squeezy changes (status change, plan/variant swap, renewal, or other attribute update). Represents the subscription in its updated state; this is a broad event covering many kinds of change."
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["subscription_updated"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("subscription_updated"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("subscription_updated")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "subscription_updated",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "subscriptions",
      id: "1",
      attributes: {
        store_id: 1,
        customer_id: 1,
        order_id: 1,
        order_item_id: 1,
        product_id: 1,
        variant_id: 1,
        product_name: "Lemonade - 2 years",
        variant_name: "Default",
        user_name: "John Doe",
        user_email: "johndoe@example.com",
        currency: "USD",
        currency_rate: "1.0000",
        status: "active",
        status_formatted: "Active",
        trial_ends_at: null,
        ends_at: "2023-08-17T09:45:53.000000Z",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T10:45:53.000000Z",
        test_mode: !1,
        pause_mode: null,
        cancelled_at: null,
        billing_anchor: 17,
        billing_anchor_type: "day_of_month",
        renews_at: "2021-09-17T09:45:53.000000Z"
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/subscription-cancelled.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var subscriptionCancelledTrigger = (0, import_pieces_framework7.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "subscription_cancelled",
  classification: "READ",
  displayName: "Subscription Cancelled",
  description: "Triggers when a subscription is cancelled manually by the customer or store owner",
  aiMetadata: {
    description: "Fires when a subscription in Lemon Squeezy is cancelled by the customer or store owner. The subscription typically remains active until the period end; the record carries the cancelled status and end date."
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["subscription_cancelled"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("subscription_cancelled"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("subscription_cancelled")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "subscription_cancelled",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "subscriptions",
      id: "1",
      attributes: {
        store_id: 1,
        customer_id: 1,
        order_id: 1,
        order_item_id: 1,
        product_id: 1,
        variant_id: 1,
        product_name: "Lemonade - 2 years",
        variant_name: "Default",
        user_name: "John Doe",
        user_email: "johndoe@example.com",
        currency: "USD",
        currency_rate: "1.0000",
        status: "cancelled",
        status_formatted: "Cancelled",
        trial_ends_at: null,
        ends_at: "2021-09-17T09:45:53.000000Z",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T10:45:53.000000Z",
        test_mode: !1,
        pause_mode: null,
        cancelled_at: "2021-08-17T10:45:53.000000Z",
        billing_anchor: 17,
        billing_anchor_type: "day_of_month",
        renews_at: null
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/subscription-resumed.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var subscriptionResumedTrigger = (0, import_pieces_framework8.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "subscription_resumed",
  classification: "READ",
  displayName: "Subscription Resumed",
  description: "Triggers when a subscription is resumed after being previously cancelled",
  aiMetadata: {
    description: "Fires when a previously cancelled subscription in Lemon Squeezy is resumed and returns to active billing. Represents the reactivated subscription with its restored renewal date."
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["subscription_resumed"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("subscription_resumed"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("subscription_resumed")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "subscription_resumed",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "subscriptions",
      id: "1",
      attributes: {
        store_id: 1,
        customer_id: 1,
        order_id: 1,
        order_item_id: 1,
        product_id: 1,
        variant_id: 1,
        product_name: "Lemonade - 2 years",
        variant_name: "Default",
        user_name: "John Doe",
        user_email: "johndoe@example.com",
        currency: "USD",
        currency_rate: "1.0000",
        status: "active",
        status_formatted: "Active",
        trial_ends_at: null,
        ends_at: "2023-08-17T09:45:53.000000Z",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T10:45:53.000000Z",
        test_mode: !1,
        pause_mode: null,
        cancelled_at: null,
        billing_anchor: 17,
        billing_anchor_type: "day_of_month",
        renews_at: "2021-09-17T09:45:53.000000Z"
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/subscription-expired.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var subscriptionExpiredTrigger = (0, import_pieces_framework9.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "subscription_expired",
  classification: "READ",
  displayName: "Subscription Expired",
  description: "Triggers when a subscription has ended after being previously cancelled",
  aiMetadata: {
    description: "Fires when a subscription in Lemon Squeezy reaches the end of its term and expires (typically after being cancelled). Represents the subscription in its expired state, marking the end of access."
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["subscription_expired"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("subscription_expired"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("subscription_expired")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "subscription_expired",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "subscriptions",
      id: "1",
      attributes: {
        store_id: 1,
        customer_id: 1,
        order_id: 1,
        order_item_id: 1,
        product_id: 1,
        variant_id: 1,
        product_name: "Lemonade - 2 years",
        variant_name: "Default",
        user_name: "John Doe",
        user_email: "johndoe@example.com",
        currency: "USD",
        currency_rate: "1.0000",
        status: "expired",
        status_formatted: "Expired",
        trial_ends_at: null,
        ends_at: "2021-09-17T09:45:53.000000Z",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-09-17T09:45:53.000000Z",
        test_mode: !1,
        pause_mode: null,
        cancelled_at: "2021-08-17T10:45:53.000000Z",
        billing_anchor: 17,
        billing_anchor_type: "day_of_month",
        renews_at: null
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/subscription-paused.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var subscriptionPausedTrigger = (0, import_pieces_framework10.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "subscription_paused",
  classification: "READ",
  displayName: "Subscription Paused",
  description: "Triggers when a subscription's payment collection is paused",
  aiMetadata: {
    description: "Fires when a subscription in Lemon Squeezy has its payment collection paused. Represents the subscription with its pause mode set, indicating billing is temporarily suspended."
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["subscription_paused"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("subscription_paused"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("subscription_paused")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "subscription_paused",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "subscriptions",
      id: "1",
      attributes: {
        store_id: 1,
        customer_id: 1,
        order_id: 1,
        order_item_id: 1,
        product_id: 1,
        variant_id: 1,
        product_name: "Lemonade - 2 years",
        variant_name: "Default",
        user_name: "John Doe",
        user_email: "johndoe@example.com",
        currency: "USD",
        currency_rate: "1.0000",
        status: "on_trial",
        status_formatted: "On trial",
        trial_ends_at: "2021-09-17T09:45:53.000000Z",
        ends_at: "2023-08-17T09:45:53.000000Z",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T10:45:53.000000Z",
        test_mode: !1,
        pause_mode: "free",
        cancelled_at: null,
        billing_anchor: 17,
        billing_anchor_type: "day_of_month",
        renews_at: null
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/subscription-unpaused.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var subscriptionUnpausedTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "subscription_unpaused",
  classification: "READ",
  displayName: "Subscription Unpaused",
  description: "Triggers when a subscription's payment collection is resumed after being previously paused",
  aiMetadata: {
    description: "Fires when a paused subscription in Lemon Squeezy has its payment collection resumed. Represents the subscription returning to normal billing after a pause."
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["subscription_unpaused"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("subscription_unpaused"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("subscription_unpaused")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "subscription_unpaused",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "subscriptions",
      id: "1",
      attributes: {
        store_id: 1,
        customer_id: 1,
        order_id: 1,
        order_item_id: 1,
        product_id: 1,
        variant_id: 1,
        product_name: "Lemonade - 2 years",
        variant_name: "Default",
        user_name: "John Doe",
        user_email: "johndoe@example.com",
        currency: "USD",
        currency_rate: "1.0000",
        status: "active",
        status_formatted: "Active",
        trial_ends_at: null,
        ends_at: "2023-08-17T09:45:53.000000Z",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T10:45:53.000000Z",
        test_mode: !1,
        pause_mode: null,
        cancelled_at: null,
        billing_anchor: 17,
        billing_anchor_type: "day_of_month",
        renews_at: "2021-09-17T09:45:53.000000Z"
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/subscription-payment-success.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var subscriptionPaymentSuccessTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "subscription_payment_success",
  classification: "READ",
  displayName: "Subscription Payment Success",
  description: "Triggers when a subscription payment is successful",
  aiMetadata: {
    description: "Fires when a subscription invoice payment in Lemon Squeezy succeeds (an initial or renewal charge is collected). Represents the successful subscription payment/invoice."
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["subscription_payment_success"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("subscription_payment_success"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("subscription_payment_success")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "subscription_payment_success",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "subscription_invoices",
      id: "1",
      attributes: {
        store_id: 1,
        subscription_id: 1,
        customer_id: 1,
        order_id: 1,
        product_id: 1,
        variant_id: 1,
        currency: "USD",
        currency_rate: "1.0000",
        subtotal: 999,
        discount_total: 0,
        tax: 200,
        total: 1199,
        subtotal_usd: 999,
        discount_total_usd: 0,
        tax_usd: 200,
        total_usd: 1199,
        subtotal_formatted: "$9.99",
        discount_total_formatted: "$0.00",
        tax_formatted: "$2.00",
        total_formatted: "$11.99",
        status: "paid",
        status_formatted: "Paid",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T09:45:53.000000Z",
        test_mode: !1,
        billing_reason: "renewal",
        billing_period_start: "2021-09-17T00:00:00.000000Z",
        billing_period_end: "2021-10-17T23:59:59.000000Z",
        payment_method: "card",
        payment_method_details: "Visa ending in 4242",
        due_date: "2021-09-17T09:45:53.000000Z",
        paid_at: "2021-09-17T09:45:53.000000Z"
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/subscription-payment-failed.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var subscriptionPaymentFailedTrigger = (0, import_pieces_framework13.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "subscription_payment_failed",
  classification: "READ",
  displayName: "Subscription Payment Failed",
  description: "Triggers when a subscription renewal payment fails",
  aiMetadata: {
    description: "Fires when a subscription renewal payment in Lemon Squeezy fails. Represents the failed subscription invoice; use to handle dunning, notify the customer, or restrict access."
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["subscription_payment_failed"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("subscription_payment_failed"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("subscription_payment_failed")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "subscription_payment_failed",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "subscription_invoices",
      id: "1",
      attributes: {
        store_id: 1,
        subscription_id: 1,
        customer_id: 1,
        order_id: 1,
        product_id: 1,
        variant_id: 1,
        currency: "USD",
        currency_rate: "1.0000",
        subtotal: 999,
        discount_total: 0,
        tax: 200,
        total: 1199,
        subtotal_usd: 999,
        discount_total_usd: 0,
        tax_usd: 200,
        total_usd: 1199,
        subtotal_formatted: "$9.99",
        discount_total_formatted: "$0.00",
        tax_formatted: "$2.00",
        total_formatted: "$11.99",
        status: "unpaid",
        status_formatted: "Unpaid",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T09:45:53.000000Z",
        test_mode: !1,
        billing_reason: "renewal",
        billing_period_start: "2021-09-17T00:00:00.000000Z",
        billing_period_end: "2021-10-17T23:59:59.000000Z",
        payment_method: "card",
        payment_method_details: "Visa ending in 4242",
        due_date: "2021-09-17T09:45:53.000000Z",
        paid_at: null
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/subscription-payment-recovered.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var subscriptionPaymentRecoveredTrigger = (0, import_pieces_framework14.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "subscription_payment_recovered",
  classification: "READ",
  displayName: "Subscription Payment Recovered",
  description: "Triggers when a subscription has a successful payment after a failed payment",
  aiMetadata: {
    description: "Fires when a subscription payment in Lemon Squeezy succeeds after a prior failed attempt (dunning recovery). Represents the recovered subscription invoice, signaling the account is back in good standing."
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["subscription_payment_recovered"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("subscription_payment_recovered"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("subscription_payment_recovered")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "subscription_payment_recovered",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "subscription_invoices",
      id: "1",
      attributes: {
        store_id: 1,
        subscription_id: 1,
        customer_id: 1,
        order_id: 1,
        product_id: 1,
        variant_id: 1,
        currency: "USD",
        currency_rate: "1.0000",
        subtotal: 999,
        discount_total: 0,
        tax: 200,
        total: 1199,
        subtotal_usd: 999,
        discount_total_usd: 0,
        tax_usd: 200,
        total_usd: 1199,
        subtotal_formatted: "$9.99",
        discount_total_formatted: "$0.00",
        tax_formatted: "$2.00",
        total_formatted: "$11.99",
        status: "paid",
        status_formatted: "Paid",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T10:45:53.000000Z",
        test_mode: !1,
        billing_reason: "renewal",
        billing_period_start: "2021-09-17T00:00:00.000000Z",
        billing_period_end: "2021-10-17T23:59:59.000000Z",
        payment_method: "card",
        payment_method_details: "Visa ending in 4242",
        due_date: "2021-09-17T09:45:53.000000Z",
        paid_at: "2021-09-17T10:45:53.000000Z"
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/subscription-payment-refunded.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var subscriptionPaymentRefundedTrigger = (0, import_pieces_framework15.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "subscription_payment_refunded",
  classification: "READ",
  displayName: "Subscription Payment Refunded",
  description: "Triggers when a subscription payment is refunded",
  aiMetadata: {
    description: "Fires when a subscription payment in Lemon Squeezy is refunded. Represents the refunded subscription invoice."
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["subscription_payment_refunded"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("subscription_payment_refunded"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("subscription_payment_refunded")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "subscription_payment_refunded",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "subscription_invoices",
      id: "1",
      attributes: {
        store_id: 1,
        subscription_id: 1,
        customer_id: 1,
        order_id: 1,
        product_id: 1,
        variant_id: 1,
        currency: "USD",
        currency_rate: "1.0000",
        subtotal: 999,
        discount_total: 0,
        tax: 200,
        total: 1199,
        subtotal_usd: 999,
        discount_total_usd: 0,
        tax_usd: 200,
        total_usd: 1199,
        subtotal_formatted: "$9.99",
        discount_total_formatted: "$0.00",
        tax_formatted: "$2.00",
        total_formatted: "$11.99",
        status: "refunded",
        status_formatted: "Refunded",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T10:45:53.000000Z",
        test_mode: !1,
        billing_reason: "renewal",
        billing_period_start: "2021-09-17T00:00:00.000000Z",
        billing_period_end: "2021-10-17T23:59:59.000000Z",
        payment_method: "card",
        payment_method_details: "Visa ending in 4242",
        due_date: "2021-09-17T09:45:53.000000Z",
        paid_at: "2021-09-17T09:45:53.000000Z",
        refunded_at: "2021-09-17T10:45:53.000000Z"
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/license-key-created.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var licenseKeyCreatedTrigger = (0, import_pieces_framework16.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "license_key_created",
  classification: "READ",
  displayName: "License Key Created",
  description: "Triggers when a license key is created from a new order",
  aiMetadata: {
    description: "Fires when a license key is generated in Lemon Squeezy as a result of a new order for a license-enabled product. Represents the newly issued license key record."
  },
  type: import_pieces_framework16.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["license_key_created"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("license_key_created"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("license_key_created")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "license_key_created",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "license_keys",
      id: "1",
      attributes: {
        store_id: 1,
        order_id: 1,
        order_item_id: 1,
        product_id: 1,
        variant_id: 1,
        user_name: "John Doe",
        user_email: "johndoe@example.com",
        key: "ABCD-EFGH-IJKL-MNOP-QRST-UVWX",
        activation_limit: 1,
        usage: 0,
        status: "active",
        status_formatted: "Active",
        expires_at: null,
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T09:45:53.000000Z",
        test_mode: !1
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/license-key-updated.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var licenseKeyUpdatedTrigger = (0, import_pieces_framework17.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "license_key_updated",
  classification: "READ",
  displayName: "License Key Updated",
  description: "Triggers when a license key is updated",
  aiMetadata: {
    description: "Fires when an existing license key in Lemon Squeezy is updated (for example its status, activation limit, or expiry changes). Represents the license key in its updated state."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["license_key_updated"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("license_key_updated"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("license_key_updated")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "license_key_updated",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "license_keys",
      id: "1",
      attributes: {
        store_id: 1,
        order_id: 1,
        order_item_id: 1,
        product_id: 1,
        variant_id: 1,
        user_name: "John Doe",
        user_email: "johndoe@example.com",
        key: "ABCD-EFGH-IJKL-MNOP-QRST-UVWX",
        activation_limit: 2,
        usage: 1,
        status: "active",
        status_formatted: "Active",
        expires_at: null,
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T10:45:53.000000Z",
        test_mode: !1
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/triggers/affiliate-activated.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var affiliateActivatedTrigger = (0, import_pieces_framework18.createTrigger)({
  auth: lemonSqueezyAuth,
  name: "affiliate_activated",
  classification: "READ",
  displayName: "Affiliate Activated",
  description: "Triggers when an affiliate is activated",
  aiMetadata: {
    description: "Fires when an affiliate is activated in a Lemon Squeezy store. Represents the activated affiliate record, marking them eligible to earn commissions."
  },
  type: import_pieces_framework18.TriggerStrategy.WEBHOOK,
  props: {
    store_id: createStoreDropdownProperty()
  },
  async onEnable(context) {
    var _a;
    let webhookSecret = generateWebhookSecret(), webhookData = await subscribeWebhook(
      context.auth.secret_text,
      context.propsValue.store_id,
      ["affiliate_activated"],
      context.webhookUrl,
      webhookSecret
    );
    await ((_a = context.store) == null ? void 0 : _a.put(createWebhookTriggerKey("affiliate_activated"), {
      webhookId: webhookData.data.id,
      secret: webhookSecret,
      storeId: context.propsValue.store_id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(createWebhookTriggerKey("affiliate_activated")));
    response != null && await unsubscribeWebhook(context.auth.secret_text, response.webhookId);
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    meta: {
      event_name: "affiliate_activated",
      custom_data: {
        user_id: 123
      }
    },
    data: {
      type: "affiliates",
      id: "1",
      attributes: {
        store_id: 1,
        user_id: 1,
        email: "affiliate@example.com",
        name: "John Affiliate",
        status: "active",
        status_formatted: "Active",
        commission_rate: "10.00",
        created_at: "2021-08-17T09:45:53.000000Z",
        updated_at: "2021-08-17T10:45:53.000000Z",
        test_mode: !1
      }
    }
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/actions/list-products.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var listProducts = (0, import_pieces_framework19.createAction)({
  name: "list_products",
  classification: "SEARCH",
  displayName: "List Products",
  description: "Retrieve a paginated list of products from your Lemon Squeezy store.",
  audience: "both",
  aiMetadata: { description: "List products from Lemon Squeezy, optionally scoped to one store (leave the store empty to span all stores) with page-based pagination. Use to enumerate a catalog or look up products before referencing a variant in a checkout. Read-only and idempotent.", idempotent: !0 },
  auth: lemonSqueezyAuth,
  props: {
    storeId: import_pieces_framework19.Property.Dropdown({
      displayName: "Store",
      description: "Filter products by store. Leave empty to return products from all stores.",
      required: !1,
      auth: lemonSqueezyAuth,
      refreshers: ["auth"],
      options: async ({ auth }) => auth ? { options: await fetchStoreOptions(auth.secret_text) } : { disabled: !0, placeholder: "Connect your account first.", options: [] }
    }),
    page: import_pieces_framework19.Property.Number({
      displayName: "Page",
      description: "Page number for pagination (starts at 1).",
      required: !1,
      defaultValue: 1
    }),
    perPage: import_pieces_framework19.Property.Number({
      displayName: "Per Page",
      description: "Number of products to return per page (1\u2013100). Default is 10.",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    let { auth, propsValue } = context, queryParams = {};
    propsValue.storeId && (queryParams["filter[store_id]"] = propsValue.storeId), propsValue.page && (queryParams["page[number]"] = String(propsValue.page)), propsValue.perPage && (queryParams["page[size]"] = String(propsValue.perPage));
    let url = `${LEMON_SQUEEZY_API_BASE}/products${buildQueryString(queryParams)}`;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url,
      headers: getLemonSqueezyHeaders(auth.secret_text)
    })).body;
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/actions/list-orders.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var listOrders = (0, import_pieces_framework20.createAction)({
  name: "list_orders",
  classification: "SEARCH",
  displayName: "List Orders",
  description: "Retrieve a paginated list of orders from your Lemon Squeezy store, with optional filters.",
  audience: "both",
  aiMetadata: { description: "List orders from Lemon Squeezy, optionally narrowed by store (empty store spans all stores), customer email, and order status (pending, paid, refunded, etc.), with page-based pagination. Use to find or audit orders by criteria; pair with Get Order for full detail on a single order. Read-only and idempotent.", idempotent: !0 },
  auth: lemonSqueezyAuth,
  props: {
    storeId: import_pieces_framework20.Property.Dropdown({
      displayName: "Store",
      description: "Filter orders by store. Leave empty to return orders from all stores.",
      required: !1,
      auth: lemonSqueezyAuth,
      refreshers: ["auth"],
      options: async ({ auth }) => auth ? { options: await fetchStoreOptions(auth.secret_text) } : { disabled: !0, placeholder: "Connect your account first.", options: [] }
    }),
    userEmail: import_pieces_framework20.Property.ShortText({
      displayName: "Customer Email",
      description: "Filter orders by customer email address.",
      required: !1
    }),
    status: import_pieces_framework20.Property.StaticDropdown({
      displayName: "Order Status",
      description: "Filter orders by status.",
      required: !1,
      options: {
        options: [
          { label: "All", value: "" },
          { label: "Pending", value: "pending" },
          { label: "Failed", value: "failed" },
          { label: "Paid", value: "paid" },
          { label: "Refunded", value: "refunded" },
          { label: "Partial Refund", value: "partial_refund" },
          { label: "Chargedback", value: "chargedback" }
        ]
      }
    }),
    page: import_pieces_framework20.Property.Number({
      displayName: "Page",
      description: "Page number for pagination (starts at 1).",
      required: !1,
      defaultValue: 1
    }),
    perPage: import_pieces_framework20.Property.Number({
      displayName: "Per Page",
      description: "Number of orders to return per page (1\u2013100). Default is 10.",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    let { auth, propsValue } = context, queryParams = {};
    propsValue.storeId && (queryParams["filter[store_id]"] = propsValue.storeId), propsValue.userEmail && (queryParams["filter[user_email]"] = propsValue.userEmail), propsValue.status && (queryParams["filter[status]"] = propsValue.status), propsValue.page && (queryParams["page[number]"] = String(propsValue.page)), propsValue.perPage && (queryParams["page[size]"] = String(propsValue.perPage));
    let url = `${LEMON_SQUEEZY_API_BASE}/orders${buildQueryString(queryParams)}`;
    return (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url,
      headers: getLemonSqueezyHeaders(auth.secret_text)
    })).body;
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/actions/get-order.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getOrder = (0, import_pieces_framework21.createAction)({
  name: "get_order",
  classification: "READ",
  displayName: "Get Order",
  description: "Retrieve the details of a specific order by its ID.",
  audience: "both",
  aiMetadata: { description: "Fetch the full details of one Lemon Squeezy order by its order ID. Use when you already have an order ID and need its complete record; to discover orders by store, email, or status first, use List Orders. Read-only and idempotent.", idempotent: !0 },
  auth: lemonSqueezyAuth,
  props: {
    orderId: import_pieces_framework21.Property.Dropdown({
      displayName: "Order",
      description: "Select the order to retrieve.",
      required: !0,
      auth: lemonSqueezyAuth,
      refreshers: ["auth"],
      options: async ({ auth }) => auth ? { options: await fetchOrderOptions(auth.secret_text) } : { disabled: !0, placeholder: "Connect your account first.", options: [] }
    })
  },
  async run(context) {
    let { auth, propsValue } = context;
    return (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: `${LEMON_SQUEEZY_API_BASE}/orders/${propsValue.orderId}`,
      headers: getLemonSqueezyHeaders(auth.secret_text)
    })).body;
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/actions/list-subscriptions.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var listSubscriptions = (0, import_pieces_framework22.createAction)({
  name: "list_subscriptions",
  classification: "SEARCH",
  displayName: "List Subscriptions",
  description: "Retrieve a paginated list of subscriptions from your Lemon Squeezy store.",
  audience: "both",
  aiMetadata: { description: "List subscriptions from Lemon Squeezy, optionally filtered by store, order, order item, product, variant, customer email, and status (active, on_trial, cancelled, expired, etc.), with page-based pagination. Use to find subscriptions matching criteria or to audit a customer's subscriptions. Read-only and idempotent.", idempotent: !0 },
  auth: lemonSqueezyAuth,
  props: {
    storeId: import_pieces_framework22.Property.Dropdown({
      displayName: "Store",
      description: "Filter subscriptions by store.",
      required: !1,
      auth: lemonSqueezyAuth,
      refreshers: ["auth"],
      options: async ({ auth }) => auth ? { options: await fetchStoreOptions(auth.secret_text) } : { disabled: !0, placeholder: "Connect your account first.", options: [] }
    }),
    orderId: import_pieces_framework22.Property.Dropdown({
      displayName: "Order",
      description: "Filter subscriptions by order.",
      required: !1,
      auth: lemonSqueezyAuth,
      refreshers: ["auth", "storeId"],
      options: async ({ auth, storeId }) => auth ? { options: await fetchOrderOptions(auth.secret_text, storeId) } : { disabled: !0, placeholder: "Connect your account first.", options: [] }
    }),
    orderItemId: import_pieces_framework22.Property.ShortText({
      displayName: "Order Item ID",
      description: "Filter subscriptions by a specific order item ID.",
      required: !1
    }),
    productId: import_pieces_framework22.Property.Dropdown({
      displayName: "Product",
      description: "Filter subscriptions by product.",
      required: !1,
      auth: lemonSqueezyAuth,
      refreshers: ["auth", "storeId"],
      options: async ({ auth, storeId }) => auth ? { options: await fetchProductOptions(auth.secret_text, storeId) } : { disabled: !0, placeholder: "Connect your account first.", options: [] }
    }),
    variantId: import_pieces_framework22.Property.Dropdown({
      displayName: "Variant",
      description: "Filter subscriptions by variant.",
      required: !1,
      auth: lemonSqueezyAuth,
      refreshers: ["auth", "productId"],
      options: async ({ auth, productId }) => auth ? { options: await fetchVariantOptions(auth.secret_text, productId) } : { disabled: !0, placeholder: "Connect your account first.", options: [] }
    }),
    userEmail: import_pieces_framework22.Property.ShortText({
      displayName: "Customer Email",
      description: "Filter subscriptions by customer email address.",
      required: !1
    }),
    status: import_pieces_framework22.Property.StaticDropdown({
      displayName: "Subscription Status",
      description: "Filter subscriptions by status.",
      required: !1,
      options: {
        options: [
          { label: "All", value: "" },
          { label: "On Trial", value: "on_trial" },
          { label: "Active", value: "active" },
          { label: "Paused", value: "paused" },
          { label: "Past Due", value: "past_due" },
          { label: "Unpaid", value: "unpaid" },
          { label: "Cancelled", value: "cancelled" },
          { label: "Expired", value: "expired" }
        ]
      }
    }),
    page: import_pieces_framework22.Property.Number({
      displayName: "Page",
      description: "Page number for pagination (starts at 1).",
      required: !1,
      defaultValue: 1
    }),
    perPage: import_pieces_framework22.Property.Number({
      displayName: "Per Page",
      description: "Number of subscriptions to return per page (1\u2013100). Default is 10.",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    let { auth, propsValue } = context, queryParams = {};
    propsValue.storeId && (queryParams["filter[store_id]"] = propsValue.storeId), propsValue.orderId && (queryParams["filter[order_id]"] = propsValue.orderId), propsValue.orderItemId && (queryParams["filter[order_item_id]"] = propsValue.orderItemId), propsValue.productId && (queryParams["filter[product_id]"] = propsValue.productId), propsValue.variantId && (queryParams["filter[variant_id]"] = propsValue.variantId), propsValue.userEmail && (queryParams["filter[user_email]"] = propsValue.userEmail), propsValue.status && (queryParams["filter[status]"] = propsValue.status), propsValue.page && (queryParams["page[number]"] = String(propsValue.page)), propsValue.perPage && (queryParams["page[size]"] = String(propsValue.perPage));
    let url = `${LEMON_SQUEEZY_API_BASE}/subscriptions${buildQueryString(queryParams)}`;
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url,
      headers: getLemonSqueezyHeaders(auth.secret_text)
    })).body;
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/actions/create-checkout.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var createCheckout = (0, import_pieces_framework23.createAction)({
  name: "create_checkout",
  classification: "WRITE",
  displayName: "Create Checkout",
  description: "Create a checkout URL for a specific Lemon Squeezy product variant.",
  audience: "both",
  aiMetadata: { description: "Create a hosted Lemon Squeezy checkout URL for a given store and product variant, optionally pre-filling customer email/name, applying a discount code, overriding the price, attaching custom data, or setting a redirect and expiry. Use to generate a payable link to send a customer. Not idempotent: each call creates a new checkout, so repeated calls yield distinct URLs. Requires a store and variant ID.", idempotent: !1 },
  auth: lemonSqueezyAuth,
  props: {
    storeId: import_pieces_framework23.Property.Dropdown({
      displayName: "Store",
      description: "The store the checkout belongs to.",
      required: !0,
      auth: lemonSqueezyAuth,
      refreshers: ["auth"],
      options: async ({ auth }) => auth ? { options: await fetchStoreOptions(auth.secret_text) } : { disabled: !0, placeholder: "Connect your account first.", options: [] }
    }),
    productId: import_pieces_framework23.Property.Dropdown({
      displayName: "Product",
      description: "Select a product to filter the variant list below.",
      required: !1,
      auth: lemonSqueezyAuth,
      refreshers: ["auth", "storeId"],
      options: async ({ auth, storeId }) => auth ? { options: await fetchProductOptions(auth.secret_text, storeId) } : { disabled: !0, placeholder: "Connect your account first.", options: [] }
    }),
    variantId: import_pieces_framework23.Property.Dropdown({
      displayName: "Variant",
      description: "The product variant to create a checkout for.",
      required: !0,
      auth: lemonSqueezyAuth,
      refreshers: ["auth", "productId"],
      options: async ({ auth, productId }) => auth ? { options: await fetchVariantOptions(auth.secret_text, productId) } : { disabled: !0, placeholder: "Connect your account first.", options: [] }
    }),
    customerEmail: import_pieces_framework23.Property.ShortText({
      displayName: "Customer Email",
      description: "Pre-fill the customer email address on the checkout page.",
      required: !1
    }),
    customerName: import_pieces_framework23.Property.ShortText({
      displayName: "Customer Name",
      description: "Pre-fill the customer name on the checkout page.",
      required: !1
    }),
    customPrice: import_pieces_framework23.Property.Number({
      displayName: "Custom Price (cents)",
      description: "Override the product price in the smallest currency unit (e.g. 999 = $9.99). Only works if custom prices are enabled on the variant.",
      required: !1
    }),
    discountCode: import_pieces_framework23.Property.ShortText({
      displayName: "Discount Code",
      description: "Pre-apply a discount code to the checkout.",
      required: !1
    }),
    redirectUrl: import_pieces_framework23.Property.ShortText({
      displayName: "Redirect URL",
      description: "URL to redirect the customer to after a successful purchase.",
      required: !1
    }),
    expiresAt: import_pieces_framework23.Property.ShortText({
      displayName: "Expires At",
      description: "ISO 8601 datetime when this checkout URL expires (e.g. 2024-12-31T23:59:59Z). Leave empty for no expiry.",
      required: !1
    }),
    customData: import_pieces_framework23.Property.Json({
      displayName: "Custom Data",
      description: "Key-value pairs of custom data to attach to the order (accessible via webhooks).",
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue } = context, checkoutData = {};
    propsValue.customerEmail && (checkoutData.email = propsValue.customerEmail), propsValue.customerName && (checkoutData.name = propsValue.customerName), propsValue.discountCode && (checkoutData.discount_code = propsValue.discountCode), propsValue.customData && (checkoutData.custom = propsValue.customData);
    let checkoutOptions = {};
    propsValue.redirectUrl && (checkoutOptions.redirect_url = propsValue.redirectUrl);
    let attributes = {};
    return Object.keys(checkoutData).length > 0 && (attributes.checkout_data = checkoutData), Object.keys(checkoutOptions).length > 0 && (attributes.checkout_options = checkoutOptions), propsValue.customPrice != null && (attributes.custom_price = propsValue.customPrice), propsValue.expiresAt && (attributes.expires_at = propsValue.expiresAt), (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.POST,
      url: `${LEMON_SQUEEZY_API_BASE}/checkouts`,
      headers: getLemonSqueezyHeaders(auth.secret_text),
      body: {
        data: {
          type: "checkouts",
          attributes,
          relationships: {
            store: {
              data: {
                type: "stores",
                id: String(propsValue.storeId)
              }
            },
            variant: {
              data: {
                type: "variants",
                id: String(propsValue.variantId)
              }
            }
          }
        }
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/lib/actions/list-customers.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var listCustomers = (0, import_pieces_framework24.createAction)({
  name: "list_customers",
  classification: "SEARCH",
  displayName: "List Customers",
  description: "Retrieve a paginated list of customers from your Lemon Squeezy store.",
  audience: "both",
  aiMetadata: { description: "List customers from Lemon Squeezy, optionally narrowed by store (empty store spans all stores) and email address, with page-based pagination. Use to look up a customer by email or enumerate the customer base. Read-only and idempotent.", idempotent: !0 },
  auth: lemonSqueezyAuth,
  props: {
    storeId: import_pieces_framework24.Property.Dropdown({
      displayName: "Store",
      description: "Filter customers by store. Leave empty to return customers from all stores.",
      required: !1,
      auth: lemonSqueezyAuth,
      refreshers: ["auth"],
      options: async ({ auth }) => auth ? { options: await fetchStoreOptions(auth.secret_text) } : { disabled: !0, placeholder: "Connect your account first.", options: [] }
    }),
    email: import_pieces_framework24.Property.ShortText({
      displayName: "Email",
      description: "Filter customers by their email address.",
      required: !1
    }),
    page: import_pieces_framework24.Property.Number({
      displayName: "Page",
      description: "Page number for pagination (starts at 1).",
      required: !1,
      defaultValue: 1
    }),
    perPage: import_pieces_framework24.Property.Number({
      displayName: "Per Page",
      description: "Number of customers to return per page (1\u2013100). Default is 10.",
      required: !1,
      defaultValue: 10
    })
  },
  async run(context) {
    let { auth, propsValue } = context, queryParams = {};
    propsValue.storeId && (queryParams["filter[store_id]"] = propsValue.storeId), propsValue.email && (queryParams["filter[email]"] = propsValue.email), propsValue.page && (queryParams["page[number]"] = String(propsValue.page)), propsValue.perPage && (queryParams["page[size]"] = String(propsValue.perPage));
    let url = `${LEMON_SQUEEZY_API_BASE}/customers${buildQueryString(queryParams)}`;
    return (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url,
      headers: getLemonSqueezyHeaders(auth.secret_text)
    })).body;
  }
});

// ../ap/packages/pieces/community/lemon-squeezy/src/index.ts
var lemonSqueezy = (0, import_pieces_framework25.createPiece)({
  displayName: "Lemon Squeezy",
  auth: lemonSqueezyAuth,
  description: "Lemon Squeezy is a payment gateway for e-commerce and subscription-based businesses.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/lemon-squeezy.png",
  authors: ["onyedikachi-david", "Harmatta"],
  actions: [
    listProducts,
    listOrders,
    getOrder,
    listSubscriptions,
    createCheckout,
    listCustomers,
    (0, import_pieces_common10.createCustomApiCallAction)({
      auth: lemonSqueezyAuth,
      baseUrl: () => LEMON_SQUEEZY_API_BASE,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`,
        Accept: "application/vnd.api+json",
        "Content-Type": "application/vnd.api+json"
      })
    })
  ],
  triggers: [
    orderCreatedTrigger,
    orderRefundedTrigger,
    subscriptionCreatedTrigger,
    subscriptionUpdatedTrigger,
    subscriptionCancelledTrigger,
    subscriptionResumedTrigger,
    subscriptionExpiredTrigger,
    subscriptionPausedTrigger,
    subscriptionUnpausedTrigger,
    subscriptionPaymentSuccessTrigger,
    subscriptionPaymentFailedTrigger,
    subscriptionPaymentRecoveredTrigger,
    subscriptionPaymentRefundedTrigger,
    licenseKeyCreatedTrigger,
    licenseKeyUpdatedTrigger,
    affiliateActivatedTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  lemonSqueezy
});
