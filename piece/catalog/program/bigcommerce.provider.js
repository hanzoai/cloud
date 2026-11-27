"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __objRest = (source, exclude) => {
  var target = {};
  for (var prop in source)
    __hasOwnProp.call(source, prop) && exclude.indexOf(prop) < 0 && (target[prop] = source[prop]);
  if (source != null && __getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(source))
      exclude.indexOf(prop) < 0 && __propIsEnum.call(source, prop) && (target[prop] = source[prop]);
  return target;
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

// ../ap/packages/pieces/community/bigcommerce/src/index.ts
var index_exports = {};
__export(index_exports, {
  bigcommerce: () => bigcommerce
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework29 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/abandoned-cart.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bigcommerce/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bigcommerce/src/lib/common/requests.ts
var import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bigcommerce/src/lib/common/constants.ts
var GET_BASE_URL = (storeHash) => `https://api.bigcommerce.com/stores/${storeHash}`;
function handleDropdownError(msg) {
  return {
    disabled: !0,
    options: [],
    placeholder: msg
  };
}

// ../ap/packages/pieces/community/bigcommerce/src/lib/common/requests.ts
async function fireHttpRequest({
  method,
  path,
  body,
  auth,
  filter = !1
}) {
  let BASE_URL = GET_BASE_URL(auth.storeHash), cleanBody = filter && body ? Array.isArray(body) ? body.map((item) => filterNulls(item)) : filterNulls(body) : body;
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      "X-Auth-Token": auth.accessToken,
      "Content-Type": "application/json",
      Accept: "application/json"
    },
    body: cleanBody
  }).then((res) => res.body).catch((err) => {
    throw new Error(
      `Error in request to ${path}: ${err.message || JSON.stringify(err)}`
    );
  });
}
var bigCommerceApiService = {
  async createWebhook({
    auth,
    payload
  }) {
    return await fireHttpRequest({
      auth,
      path: "/v3/hooks",
      method: import_pieces_common.HttpMethod.POST,
      body: payload
    });
  },
  async createCustomer({
    auth,
    payload
  }) {
    return await fireHttpRequest({
      auth,
      path: "/v3/customers",
      method: import_pieces_common.HttpMethod.POST,
      body: payload
    });
  },
  async fetchCustomers({
    auth,
    queryString
  }) {
    return await fireHttpRequest({
      auth,
      path: `/v3/customers${queryString ? `?${queryString}` : ""}`,
      method: import_pieces_common.HttpMethod.GET
    });
  },
  async fetchCustomerAddresses({
    auth,
    queryString
  }) {
    return await fireHttpRequest({
      auth,
      path: `/v3/customers/addresses${queryString ? `?${queryString}` : ""}`,
      method: import_pieces_common.HttpMethod.GET
    });
  },
  async fetchProducts({
    auth,
    queryString
  }) {
    return await fireHttpRequest({
      auth,
      path: `/v3/catalog/products${queryString ? `?${queryString}` : ""}`,
      method: import_pieces_common.HttpMethod.GET
    });
  },
  async createProduct({
    auth,
    payload
  }) {
    return await fireHttpRequest({
      auth,
      path: "/v3/catalog/products",
      method: import_pieces_common.HttpMethod.POST,
      body: payload,
      filter: !0
    });
  },
  async updateProduct({
    auth,
    productId,
    payload
  }) {
    return await fireHttpRequest({
      auth,
      path: `/v3/catalog/products/${productId}`,
      method: import_pieces_common.HttpMethod.PUT,
      body: payload,
      filter: !0
    });
  },
  async deleteProduct({
    auth,
    productId
  }) {
    return await fireHttpRequest({
      auth,
      path: `/v3/catalog/products/${productId}`,
      method: import_pieces_common.HttpMethod.DELETE
    });
  },
  async fetchOrders({
    auth,
    queryString
  }) {
    return await fireHttpRequest({
      auth,
      path: `/v2/orders${queryString ? `?${queryString}` : ""}`,
      method: import_pieces_common.HttpMethod.GET
    });
  },
  async fetchOrder({
    auth,
    orderId
  }) {
    return await fireHttpRequest({
      auth,
      path: `/v2/orders/${orderId}`,
      method: import_pieces_common.HttpMethod.GET
    });
  },
  async fetchCategories({
    auth,
    queryString
  }) {
    return await fireHttpRequest({
      auth,
      path: `/v3/catalog/categories${queryString ? `?${queryString}` : ""}`,
      method: import_pieces_common.HttpMethod.GET
    });
  },
  async createBlogPost({
    auth,
    payload
  }) {
    return await fireHttpRequest({
      auth,
      path: "/v2/blog/posts",
      method: import_pieces_common.HttpMethod.POST,
      body: payload
    });
  },
  async createCustomerAddress({
    auth,
    payload
  }) {
    return await fireHttpRequest({
      auth,
      path: "/v3/customers/addresses",
      method: import_pieces_common.HttpMethod.POST,
      body: payload
    });
  },
  async deleteWebhook({
    auth,
    webhookId
  }) {
    return await fireHttpRequest({
      auth,
      path: `/v3/hooks/${webhookId}`,
      method: import_pieces_common.HttpMethod.DELETE
    });
  }
};
function filterNulls(obj) {
  if (obj != null)
    return typeof obj != "object" ? obj : Array.isArray(obj) ? obj.map(filterNulls) : Object.fromEntries(
      Object.entries(obj).filter(([_, v]) => v != null).map(([k, v]) => [k, filterNulls(v)])
    );
}

// ../ap/packages/pieces/community/bigcommerce/src/lib/common/auth.ts
var bigcommerceAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Enter your BigCommerce API credentials. ",
  props: {
    instruction: import_pieces_framework.Property.MarkDown({
      value: `1. Login to https://www.bigcommerce.com/essentials/
2. Go to Settings
3. Scroll down to API
4. Click on "Store Level API Accounts"
5. Click "Create API Account" button
6. Enter a name and add required scopes
7. Save - you will get a downloaded file containing your access token and store hash`
    }),
    storeHash: import_pieces_framework.Property.ShortText({
      displayName: "Store Hash",
      description: "Your BigCommerce store hash (e.g., abcd1234)",
      required: !0
    }),
    accessToken: import_pieces_framework.Property.ShortText({
      displayName: "Access Token",
      description: "Your BigCommerce API access token",
      required: !0
    })
  },
  required: !0,
  async validate(context) {
    try {
      return await bigCommerceApiService.fetchProducts({
        auth: context.auth
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Could not validate credentials. please check your credentials are correct."
      };
    }
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/abandoned-cart.ts
var TRIGGER_NAME = "abandoned cart", CACHE_KEY = `${TRIGGER_NAME}-trigger-store-key`, TRIGGER_SCOPE = "store/cart/abandoned", abandonedCart = (0, import_pieces_framework2.createTrigger)({
  auth: bigcommerceAuth,
  name: "abandonedCart",
  classification: "READ",
  displayName: "Abandoned Cart",
  description: "Triggers when a cart is abandoned",
  aiMetadata: {
    description: "Fires when a shopper abandons a cart in the BigCommerce store (the cart sits inactive without checkout completing). The event identifies the affected cart; use it to drive abandoned-cart recovery flows."
  },
  props: {},
  sampleData: {
    producer: "stores/dxqdddcaklwsso",
    hash: "3610addd127e330084ddddd7ddd09b78abfd1a85",
    created_at: 1761809622,
    store_id: "1003425529",
    scope: "store/cart/abandoned",
    data: {
      type: "cart",
      id: "ss-598f-4c95-813b-0ess950562770d"
    }
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhook = await bigCommerceApiService.createWebhook({
      auth: context.auth.props,
      payload: {
        scope: TRIGGER_SCOPE,
        destination: context.webhookUrl,
        is_active: !0
      }
    });
    await context.store.put(CACHE_KEY, webhook.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(CACHE_KEY);
    webhookId && await bigCommerceApiService.deleteWebhook({
      auth: context.auth.props,
      webhookId
    }).then(async () => {
      await context.store.delete(CACHE_KEY);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/cart-created.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var TRIGGER_NAME2 = "cart created", CACHE_KEY2 = `${TRIGGER_NAME2}-trigger-store-key`, TRIGGER_SCOPE2 = "store/cart/created", cartCreated = (0, import_pieces_framework3.createTrigger)({
  auth: bigcommerceAuth,
  name: "cartCreated",
  classification: "READ",
  displayName: "Cart Created",
  description: "Triggers when a new cart is created",
  aiMetadata: {
    description: "Fires when a new shopping cart is created in the BigCommerce store (a shopper adds the first item to a fresh cart). The event identifies the new cart."
  },
  props: {},
  sampleData: {
    producer: "stores/dxqdddcaklwsso",
    hash: "3610addd127e330084ddddd7ddd09b78abfd1a85",
    created_at: 1761809622,
    store_id: "1003425529",
    scope: "store/cart/created",
    data: {
      type: "cart",
      id: "ss-598f-4c95-813b-0ess950562770d"
    }
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhook = await bigCommerceApiService.createWebhook({
      auth: context.auth.props,
      payload: {
        scope: TRIGGER_SCOPE2,
        destination: context.webhookUrl,
        is_active: !0
      }
    });
    await context.store.put(CACHE_KEY2, webhook.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(CACHE_KEY2);
    webhookId && await bigCommerceApiService.deleteWebhook({
      auth: context.auth.props,
      webhookId
    }).then(async () => {
      await context.store.delete(CACHE_KEY2);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/customer-address-created.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var TRIGGER_NAME3 = "customer address created", CACHE_KEY3 = `${TRIGGER_NAME3}-trigger-store-key`, TRIGGER_SCOPE3 = "store/customer/address/created", customerAddressCreated = (0, import_pieces_framework4.createTrigger)({
  auth: bigcommerceAuth,
  name: "customerAddressCreated",
  classification: "READ",
  displayName: "Customer Address Created",
  description: "Triggers when a new customer address is created",
  aiMetadata: {
    description: "Fires when a new address is added to a customer in the BigCommerce store. The event identifies the affected customer."
  },
  props: {},
  sampleData: {
    producer: "stores/xqcaklwsso",
    hash: "d7b899890f5e0e98a3e074f018b14b07d5d89360",
    created_at: 1761808595,
    store_id: "1003425529",
    scope: "store/customer/address/created",
    data: {
      type: "customer",
      id: 2
    }
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhook = await bigCommerceApiService.createWebhook({
      auth: context.auth.props,
      payload: {
        scope: TRIGGER_SCOPE3,
        destination: context.webhookUrl,
        is_active: !0
      }
    });
    await context.store.put(CACHE_KEY3, webhook.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(CACHE_KEY3);
    webhookId && await bigCommerceApiService.deleteWebhook({
      auth: context.auth.props,
      webhookId
    }).then(async () => {
      await context.store.delete(CACHE_KEY3);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/customer-address-updated.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var TRIGGER_NAME4 = "customer address updated", CACHE_KEY4 = `${TRIGGER_NAME4}-trigger-store-key`, TRIGGER_SCOPE4 = "store/customer/address/updated", customerAddressUpdated = (0, import_pieces_framework5.createTrigger)({
  auth: bigcommerceAuth,
  name: "customerAddressUpdated",
  classification: "READ",
  displayName: "Customer Address Updated",
  description: "Triggers when a new customer address is updated",
  aiMetadata: {
    description: "Fires when an existing customer address is updated in the BigCommerce store. The event identifies the affected customer."
  },
  props: {},
  sampleData: {
    producer: "stores/xqcaklwsso",
    hash: "d7b899890f5e0e98a3e074f018b14b07d5d89360",
    created_at: 1761808595,
    store_id: "1003425529",
    scope: "store/customer/address/updated",
    data: {
      type: "customer",
      id: 2
    }
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhook = await bigCommerceApiService.createWebhook({
      auth: context.auth.props,
      payload: {
        scope: TRIGGER_SCOPE4,
        destination: context.webhookUrl,
        is_active: !0
      }
    });
    await context.store.put(CACHE_KEY4, webhook.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(CACHE_KEY4);
    webhookId && await bigCommerceApiService.deleteWebhook({
      auth: context.auth.props,
      webhookId
    }).then(async () => {
      await context.store.delete(CACHE_KEY4);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/customer-created.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var TRIGGER_NAME5 = "customer created", CACHE_KEY5 = `${TRIGGER_NAME5}-trigger-store-key`, TRIGGER_SCOPE5 = "store/customer/created", customerCreated = (0, import_pieces_framework6.createTrigger)({
  auth: bigcommerceAuth,
  name: "customerCreated",
  classification: "READ",
  displayName: "Customer Created",
  description: "Triggers when a new customer is created",
  aiMetadata: {
    description: "Fires when a new customer is created in the BigCommerce store. The event identifies the new customer."
  },
  props: {},
  sampleData: {
    producer: "stores/xqcaklwsso",
    hash: "d7b899890f5e0e98a3e074f018b14b07d5d89360",
    created_at: 1761808595,
    store_id: "1003425529",
    scope: "store/customer/created",
    data: {
      type: "customer",
      id: 2,
      origin_channel_id: 1,
      channel_ids: [1]
    }
  },
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhook = await bigCommerceApiService.createWebhook({
      auth: context.auth.props,
      payload: {
        scope: TRIGGER_SCOPE5,
        destination: context.webhookUrl,
        is_active: !0
      }
    });
    await context.store.put(CACHE_KEY5, webhook.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(CACHE_KEY5);
    webhookId && await bigCommerceApiService.deleteWebhook({
      auth: context.auth.props,
      webhookId
    }).then(async () => {
      await context.store.delete(CACHE_KEY5);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/customer-updated.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var TRIGGER_NAME6 = "customer updated", CACHE_KEY6 = `${TRIGGER_NAME6}-trigger-store-key`, TRIGGER_SCOPE6 = "store/customer/updated", customerUpdated = (0, import_pieces_framework7.createTrigger)({
  auth: bigcommerceAuth,
  name: "customerUpdated",
  classification: "READ",
  displayName: "Customer Updated",
  description: "Triggers when a customer is updated",
  aiMetadata: {
    description: "Fires when an existing customer record is updated in the BigCommerce store. The event identifies the affected customer."
  },
  props: {},
  sampleData: {
    producer: "stores/xqcaklwsso",
    hash: "d7b899890f5e0e98a3e074f018b14b07d5d89360",
    created_at: 1761808595,
    store_id: "1003425529",
    scope: "store/customer/updated",
    data: {
      type: "customer",
      id: 2
    }
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhook = await bigCommerceApiService.createWebhook({
      auth: context.auth.props,
      payload: {
        scope: TRIGGER_SCOPE6,
        destination: context.webhookUrl,
        is_active: !0
      }
    });
    await context.store.put(CACHE_KEY6, webhook.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(CACHE_KEY6);
    webhookId && await bigCommerceApiService.deleteWebhook({
      auth: context.auth.props,
      webhookId
    }).then(async () => {
      await context.store.delete(CACHE_KEY6);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/order-created.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var TRIGGER_NAME7 = "order created", CACHE_KEY7 = `${TRIGGER_NAME7}-trigger-store-key`, TRIGGER_SCOPE7 = "store/order/created", orderCreated = (0, import_pieces_framework8.createTrigger)({
  auth: bigcommerceAuth,
  name: "orderCreated",
  classification: "READ",
  displayName: "Order Created",
  description: "Triggers when a new order is created",
  aiMetadata: {
    description: "Fires when a new order is created in the BigCommerce store. The event identifies the new order; pair with Get Order to fetch its full details."
  },
  props: {},
  sampleData: {
    producer: "stores/xqcaklwsso",
    hash: "d7b899890f5e0e98a3e074f018b14b07d5d89360",
    created_at: 1761808595,
    store_id: "1003425529",
    scope: "store/order/created",
    data: {
      type: "order",
      id: 2
    }
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhook = await bigCommerceApiService.createWebhook({
      auth: context.auth.props,
      payload: {
        scope: TRIGGER_SCOPE7,
        destination: context.webhookUrl,
        is_active: !0
      }
    });
    await context.store.put(CACHE_KEY7, webhook.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(CACHE_KEY7);
    webhookId && await bigCommerceApiService.deleteWebhook({
      auth: context.auth.props,
      webhookId
    }).then(async () => {
      await context.store.delete(CACHE_KEY7);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/order-updated.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var TRIGGER_NAME8 = "order updated", CACHE_KEY8 = `${TRIGGER_NAME8}-trigger-store-key`, TRIGGER_SCOPE8 = "store/order/updated", orderUpdated = (0, import_pieces_framework9.createTrigger)({
  auth: bigcommerceAuth,
  name: "orderUpdated",
  classification: "READ",
  displayName: "Order Updated",
  description: "Triggers when an order is updated",
  aiMetadata: {
    description: "Fires when an existing order is updated in the BigCommerce store (any change to the order, not just its status). The event identifies the affected order; pair with Get Order for full details."
  },
  props: {},
  sampleData: {
    producer: "stores/xqcaklwsso",
    hash: "d7b899890f5e0e98a3e074f018b14b07d5d89360",
    created_at: 1761808595,
    store_id: "1003425529",
    scope: "store/order/updated",
    data: {
      type: "order",
      id: 2
    }
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhook = await bigCommerceApiService.createWebhook({
      auth: context.auth.props,
      payload: {
        scope: TRIGGER_SCOPE8,
        destination: context.webhookUrl,
        is_active: !0
      }
    });
    await context.store.put(CACHE_KEY8, webhook.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(CACHE_KEY8);
    webhookId && await bigCommerceApiService.deleteWebhook({
      auth: context.auth.props,
      webhookId
    }).then(async () => {
      await context.store.delete(CACHE_KEY8);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/order-status-updated.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var TRIGGER_NAME9 = "order status updated", CACHE_KEY9 = `${TRIGGER_NAME9}-trigger-store-key`, TRIGGER_SCOPE9 = "store/order/statusUpdated", orderStatusUpdated = (0, import_pieces_framework10.createTrigger)({
  auth: bigcommerceAuth,
  name: "orderStatusUpdated",
  classification: "READ",
  displayName: "Order Status Updated",
  description: "Triggers when an order status has changed",
  aiMetadata: {
    description: "Fires specifically when an order\u2019s status changes in the BigCommerce store (e.g. moves to shipped or completed). Use this instead of Order Updated when you only care about status transitions. The event identifies the affected order."
  },
  props: {},
  sampleData: {
    producer: "stores/xqcaklwsso",
    hash: "d7b899890f5e0e98a3e074f018b14b07d5d89360",
    created_at: 1761808595,
    store_id: "1003425529",
    scope: "store/order/statusUpdated",
    data: {
      type: "order",
      id: 2
    }
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhook = await bigCommerceApiService.createWebhook({
      auth: context.auth.props,
      payload: {
        scope: TRIGGER_SCOPE9,
        destination: context.webhookUrl,
        is_active: !0
      }
    });
    await context.store.put(CACHE_KEY9, webhook.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(CACHE_KEY9);
    webhookId && await bigCommerceApiService.deleteWebhook({
      auth: context.auth.props,
      webhookId
    }).then(async () => {
      await context.store.delete(CACHE_KEY9);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/product-created.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var TRIGGER_NAME10 = "product created", CACHE_KEY10 = `${TRIGGER_NAME10}-trigger-store-key`, TRIGGER_SCOPE10 = "store/product/created", productCreated = (0, import_pieces_framework11.createTrigger)({
  auth: bigcommerceAuth,
  name: "productCreated",
  classification: "READ",
  displayName: "Product Created",
  description: "Triggers when a new product is created",
  aiMetadata: {
    description: "Fires when a new product is added to the BigCommerce store catalog. The event identifies the new product."
  },
  props: {},
  sampleData: {
    producer: "stores/xqcaklwsso",
    hash: "c6db17090e98d2c58c0cb1988b5e9ace48215e22",
    created_at: 1761808831,
    store_id: "1003425529",
    scope: "store/product/created",
    data: {
      type: "product",
      id: 113
    }
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhook = await bigCommerceApiService.createWebhook({
      auth: context.auth.props,
      payload: {
        scope: TRIGGER_SCOPE10,
        destination: context.webhookUrl,
        is_active: !0
      }
    });
    await context.store.put(CACHE_KEY10, webhook.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(CACHE_KEY10);
    webhookId && await bigCommerceApiService.deleteWebhook({
      auth: context.auth.props,
      webhookId
    }).then(async () => {
      await context.store.delete(CACHE_KEY10);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/triggers/shipment-created.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var TRIGGER_NAME11 = "shipment created", CACHE_KEY11 = `${TRIGGER_NAME11}-trigger-store-key`, TRIGGER_SCOPE11 = "store/shipment/created", shipmentCreated = (0, import_pieces_framework12.createTrigger)({
  auth: bigcommerceAuth,
  name: "shipmentCreated",
  classification: "READ",
  displayName: "Shipment Created",
  description: "Triggers when a new shipment is created",
  aiMetadata: {
    description: "Fires when a new shipment is created for an order in the BigCommerce store (items are shipped/fulfilled). The event identifies the new shipment."
  },
  props: {},
  sampleData: {
    producer: "stores/xqcaklwsso",
    hash: "d7b899890f5e0e98a3e074f018b14b07d5d89360",
    created_at: 1761808595,
    store_id: "1003425529",
    scope: "store/shipment/created",
    data: {
      type: "shipment",
      id: 2
    }
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let webhook = await bigCommerceApiService.createWebhook({
      auth: context.auth.props,
      payload: {
        scope: TRIGGER_SCOPE11,
        destination: context.webhookUrl,
        is_active: !0
      }
    });
    await context.store.put(CACHE_KEY11, webhook.data.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(CACHE_KEY11);
    webhookId && await bigCommerceApiService.deleteWebhook({
      auth: context.auth.props,
      webhookId
    }).then(async () => {
      await context.store.delete(CACHE_KEY11);
    });
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/create-customer.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var createCustomer = (0, import_pieces_framework13.createAction)({
  auth: bigcommerceAuth,
  name: "createCustomer",
  classification: "WRITE",
  displayName: "Create Customer",
  description: "Creates a customer",
  audience: "both",
  aiMetadata: {
    description: "Creates a new customer record in a BigCommerce store from an email plus first/last name (company, phone, and notes optional). Use to register a shopper or contact. Not idempotent: each call adds a new customer, and BigCommerce rejects a duplicate email rather than returning the existing one, so call Search Customer or Find or Create Customer first if the customer may already exist.",
    idempotent: !1
  },
  props: {
    email: import_pieces_framework13.Property.ShortText({
      displayName: "Email",
      description: "Customer email address",
      required: !0
    }),
    first_name: import_pieces_framework13.Property.ShortText({
      displayName: "First Name",
      description: "Customer first name",
      required: !0
    }),
    last_name: import_pieces_framework13.Property.ShortText({
      displayName: "Last Name",
      description: "Customer last name",
      required: !0
    }),
    company: import_pieces_framework13.Property.ShortText({
      displayName: "Company",
      description: "Customer company name",
      required: !1
    }),
    phone: import_pieces_framework13.Property.ShortText({
      displayName: "Phone",
      description: "Customer phone number",
      required: !1
    }),
    notes: import_pieces_framework13.Property.LongText({
      displayName: "Notes",
      description: "Customer notes",
      required: !1
    })
  },
  async run(context) {
    return await bigCommerceApiService.createCustomer({
      auth: context.auth.props,
      payload: [
        {
          email: context.propsValue.email,
          first_name: context.propsValue.first_name,
          last_name: context.propsValue.last_name,
          company: context.propsValue.company,
          phone: context.propsValue.phone,
          notes: context.propsValue.notes
        }
      ]
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/create-a-product.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var createAProduct = (0, import_pieces_framework14.createAction)({
  auth: bigcommerceAuth,
  name: "createAProduct",
  classification: "WRITE",
  displayName: "Create a Product",
  description: "Creates a Product",
  audience: "both",
  aiMetadata: {
    description: "Creates a new catalog product in a BigCommerce store. Use to add an item to the store. Requires name, type (physical or digital), weight, and price; SKU, description, and brand_id are optional. Not idempotent: each call adds a new product, so search the catalog first if it may already exist.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework14.Property.ShortText({
      displayName: "Product Name",
      description: "Name of the product",
      required: !0
    }),
    type: import_pieces_framework14.Property.StaticDropdown({
      displayName: "Product Type",
      description: "Type of product",
      required: !0,
      options: {
        options: [
          { label: "Physical", value: "physical" },
          { label: "Digital", value: "digital" }
        ]
      }
    }),
    sku: import_pieces_framework14.Property.ShortText({
      displayName: "SKU",
      description: "Product SKU",
      required: !1
    }),
    description: import_pieces_framework14.Property.LongText({
      displayName: "Description",
      description: "Product description",
      required: !1
    }),
    weight: import_pieces_framework14.Property.Number({
      displayName: "Weight",
      description: "Product weight",
      required: !0
    }),
    price: import_pieces_framework14.Property.Number({
      displayName: "Price",
      description: "Product price",
      required: !0
    }),
    brand_id: import_pieces_framework14.Property.Number({
      displayName: "Brand ID",
      description: "Brand ID",
      required: !1
    })
  },
  async run(context) {
    return await bigCommerceApiService.createProduct({
      auth: context.auth.props,
      payload: {
        name: context.propsValue.name,
        type: context.propsValue.type,
        sku: context.propsValue.sku,
        description: context.propsValue.description,
        weight: context.propsValue.weight,
        price: context.propsValue.price,
        brand_id: context.propsValue.brand_id
      }
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/update-a-product.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var updateAProduct = (0, import_pieces_framework15.createAction)({
  auth: bigcommerceAuth,
  name: "updateAProduct",
  classification: "WRITE",
  displayName: "Update a Product",
  description: "Updates an existing Product",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing catalog product in a BigCommerce store, identified by its productId. Only the fields you supply are sent (null/undefined values are dropped so existing data is preserved), so use it for partial edits to name, type, SKU, description, weight, price, or brand. Idempotent: re-applying the same field values leaves the product in the same state.",
    idempotent: !0
  },
  props: {
    productId: import_pieces_framework15.Property.ShortText({
      displayName: "Product ID",
      description: "The ID of the product to update",
      required: !0
    }),
    name: import_pieces_framework15.Property.ShortText({
      displayName: "Product Name",
      description: "Name of the product",
      required: !1
    }),
    type: import_pieces_framework15.Property.StaticDropdown({
      displayName: "Product Type",
      description: "Type of product",
      required: !1,
      options: {
        options: [
          { label: "Physical", value: "physical" },
          { label: "Digital", value: "digital" }
        ]
      }
    }),
    sku: import_pieces_framework15.Property.ShortText({
      displayName: "SKU",
      description: "Product SKU",
      required: !1
    }),
    description: import_pieces_framework15.Property.LongText({
      displayName: "Description",
      description: "Product description",
      required: !1
    }),
    weight: import_pieces_framework15.Property.Number({
      displayName: "Weight",
      description: "Product weight",
      required: !1
    }),
    price: import_pieces_framework15.Property.Number({
      displayName: "Price",
      description: "Product price",
      required: !1
    }),
    brand_id: import_pieces_framework15.Property.Number({
      displayName: "Brand ID",
      description: "Brand ID",
      required: !1
    })
  },
  async run(context) {
    let _a = context.propsValue, { productId } = _a, payload = __objRest(_a, ["productId"]), filteredPayload = Object.fromEntries(
      Object.entries(payload).filter(([_, v]) => v != null)
    );
    return await bigCommerceApiService.updateProduct({
      auth: context.auth.props,
      productId,
      payload: filteredPayload
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/delete-a-product.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var deleteAProduct = (0, import_pieces_framework16.createAction)({
  auth: bigcommerceAuth,
  name: "deleteAProduct",
  classification: "DESTRUCTIVE",
  displayName: "Delete a Product",
  description: "Deletes an existing Product",
  audience: "both",
  aiMetadata: {
    description: "Permanently deletes a catalog product from a BigCommerce store by its productId. Use to remove an item from the store. This is destructive and cannot be undone. Effectively idempotent: deleting an already-removed product has no further effect, though the call may error if the id no longer exists.",
    idempotent: !0
  },
  props: {
    productId: import_pieces_framework16.Property.ShortText({
      displayName: "Product ID",
      description: "The ID of the product to delete",
      required: !0
    })
  },
  async run(context) {
    return await bigCommerceApiService.deleteProduct({
      auth: context.auth.props,
      productId: context.propsValue.productId
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/get-order.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var getOrder = (0, import_pieces_framework17.createAction)({
  auth: bigcommerceAuth,
  name: "getOrder",
  classification: "READ",
  displayName: "Get Order",
  description: "Gets details of an order",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the full details of a single BigCommerce order by its orderId. Use when you already have an order id (e.g. from an order trigger or List Orders) and need its data. Idempotent read-only lookup with no side effects.",
    idempotent: !0
  },
  props: {
    orderId: import_pieces_framework17.Property.ShortText({
      displayName: "Order ID",
      description: "The ID of the order to retrieve",
      required: !0
    })
  },
  async run(context) {
    return await bigCommerceApiService.fetchOrder({
      auth: context.auth.props,
      orderId: context.propsValue.orderId
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/list-orders.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var listOrders = (0, import_pieces_framework18.createAction)({
  auth: bigcommerceAuth,
  name: "listOrders",
  classification: "SEARCH",
  displayName: "List Orders",
  description: "Lists all orders",
  audience: "both",
  aiMetadata: {
    description: "Lists orders from a BigCommerce store. With no filters it returns all orders; optionally narrow by customer_id and/or status_id to a subset. Use to browse or find orders when you do not have a specific order id. Idempotent read-only query with no side effects.",
    idempotent: !0
  },
  props: {
    customer_id: import_pieces_framework18.Property.Number({
      displayName: "Customer ID",
      description: "Filter by customer ID",
      required: !1
    }),
    status_id: import_pieces_framework18.Property.Number({
      displayName: "Status ID",
      description: "Filter by status ID",
      required: !1
    })
  },
  async run(context) {
    let qParams = new URLSearchParams();
    return context.propsValue.customer_id !== void 0 && context.propsValue.customer_id !== null && qParams.append("customer_id", context.propsValue.customer_id.toString()), context.propsValue.status_id !== void 0 && context.propsValue.status_id !== null && qParams.append("status_id", context.propsValue.status_id.toString()), await bigCommerceApiService.fetchOrders({
      auth: context.auth.props,
      queryString: qParams.toString()
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/list-categories.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var listCategories = (0, import_pieces_framework19.createAction)({
  auth: bigcommerceAuth,
  name: "listCategories",
  classification: "SEARCH",
  displayName: "List Categories",
  description: "Lists all categories",
  audience: "both",
  aiMetadata: {
    description: "Lists product categories from a BigCommerce store. With no name filter it returns all categories; provide a name to filter to matching categories. Use to discover category names or ids before assigning products. Idempotent read-only query with no side effects.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework19.Property.ShortText({
      displayName: "Category Name",
      description: "Filter by category name",
      required: !1
    })
  },
  async run(context) {
    let qParams = new URLSearchParams();
    return context.propsValue.name && qParams.append("name", context.propsValue.name), await bigCommerceApiService.fetchCategories({
      auth: context.auth.props,
      queryString: qParams.toString()
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/create-blog-post.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var createBlogPost = (0, import_pieces_framework20.createAction)({
  auth: bigcommerceAuth,
  name: "createBlogPost",
  classification: "WRITE",
  displayName: "Create Blog Post",
  description: "Creates a blog post",
  audience: "both",
  aiMetadata: {
    description: "Creates a blog post in a BigCommerce store's blog. Requires a title and body (HTML supported); author, URL, published date, meta description/keywords, tags, and a published flag are optional. Use to publish store blog content. Not idempotent: each call creates a new post.",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework20.Property.ShortText({
      displayName: "Title",
      description: "Blog post title",
      required: !0
    }),
    body: import_pieces_framework20.Property.LongText({
      displayName: "Body",
      description: "Blog post content (HTML supported)",
      required: !0
    }),
    author: import_pieces_framework20.Property.ShortText({
      displayName: "Author",
      description: "Author name",
      required: !1
    }),
    url: import_pieces_framework20.Property.ShortText({
      displayName: "URL",
      description: "URL for the public blog post. Example - /blog/welcome-bigcommerce/",
      required: !1
    }),
    published_date: import_pieces_framework20.Property.DateTime({
      displayName: "Published Date",
      description: "Published Date for the blog post. Example - Wed, 10 Aug 2022 15:39:15 -0500",
      required: !1
    }),
    meta_description: import_pieces_framework20.Property.LongText({
      displayName: "Meta Description",
      description: "Description text for this blog post\u2019s <meta/> element.",
      required: !1
    }),
    meta_keywords: import_pieces_framework20.Property.LongText({
      displayName: "Meta Keywords",
      description: "Comma seperated (,) Keywords for this blog post\u2019s <meta/> element. Eg welcome, bigcommerce",
      required: !1
    }),
    tags: import_pieces_framework20.Property.Array({
      displayName: "Tags",
      description: "Array of tags",
      required: !1
    }),
    is_published: import_pieces_framework20.Property.Checkbox({
      displayName: "Is Published",
      description: "Whether the post is published",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    return await bigCommerceApiService.createBlogPost({
      auth: context.auth.props,
      payload: {
        title: context.propsValue.title,
        body: context.propsValue.body,
        author: context.propsValue.author,
        tags: context.propsValue.tags,
        is_published: context.propsValue.is_published,
        meta_description: context.propsValue.meta_description,
        meta_keywords: context.propsValue.meta_keywords,
        published_date: context.propsValue.published_date,
        url: context.propsValue.url
      }
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/create-customer-address.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/bigcommerce/src/lib/common/props.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var customerDropdown = ({ required }) => import_pieces_framework21.Property.Dropdown({
  auth: bigcommerceAuth,
  displayName: "Customer",
  description: "Select the customer",
  required,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return handleDropdownError("Please connect your account first");
    try {
      let response = await bigCommerceApiService.fetchCustomers({ auth: auth.props }), data = Array.isArray(response == null ? void 0 : response.data) ? response.data : [];
      return data.length === 0 ? handleDropdownError("No customers found") : {
        disabled: !1,
        options: data.map((user) => ({
          label: [user.first_name, user.last_name].filter((p) => typeof p == "string" && p.trim() !== "").join(" ") || user.email || user.id,
          value: user.id
        }))
      };
    } catch (error) {
      return handleDropdownError("Failed to load customers");
    }
  }
}), multiCustomerDropdown = ({ required }) => import_pieces_framework21.Property.MultiSelectDropdown({
  auth: bigcommerceAuth,
  displayName: "Customers",
  description: "Select the customers",
  required,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return handleDropdownError("Please connect your account first");
    try {
      let response = await bigCommerceApiService.fetchCustomers({ auth: auth.props }), data = Array.isArray(response == null ? void 0 : response.data) ? response.data : [];
      return data.length === 0 ? handleDropdownError("No customers found") : {
        disabled: !1,
        options: data.map((user) => ({
          label: [user.first_name, user.last_name].filter((p) => typeof p == "string" && p.trim() !== "").join(" ") || user.email || user.id,
          value: user.id
        }))
      };
    } catch (error) {
      return handleDropdownError("Failed to load customers");
    }
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/create-customer-address.ts
var createCustomerAddress = (0, import_pieces_framework22.createAction)({
  auth: bigcommerceAuth,
  name: "createCustomerAddress",
  classification: "WRITE",
  displayName: "Create Customer Address",
  description: "Creates a customer's address",
  audience: "both",
  aiMetadata: {
    description: "Adds a new address to an existing BigCommerce customer, identified by customer_id (selected from a dropdown of store customers). Requires name, address line 1, city, state/province, postal code, and an ISO 3166-1 alpha-2 country code. Use to attach a shipping or billing address to a customer. Not idempotent: each call appends a new address even if an identical one exists, so use Find or Create Customer\u2019s Address to avoid duplicates.",
    idempotent: !1
  },
  props: {
    customer_id: customerDropdown({
      required: !0
    }),
    first_name: import_pieces_framework22.Property.ShortText({
      displayName: "First Name",
      required: !0
    }),
    last_name: import_pieces_framework22.Property.ShortText({
      displayName: "Last Name",
      required: !0
    }),
    address1: import_pieces_framework22.Property.ShortText({
      displayName: "Address Line 1",
      required: !0
    }),
    address2: import_pieces_framework22.Property.ShortText({
      displayName: "Address Line 2",
      required: !1
    }),
    city: import_pieces_framework22.Property.ShortText({
      displayName: "City",
      required: !0
    }),
    state_or_province: import_pieces_framework22.Property.ShortText({
      displayName: "State/Province",
      required: !0
    }),
    postal_code: import_pieces_framework22.Property.ShortText({
      displayName: "Postal Code",
      required: !0
    }),
    country_code: import_pieces_framework22.Property.ShortText({
      displayName: "Country Code",
      description: "ISO 3166-1 alpha-2 country code",
      required: !0
    }),
    phone: import_pieces_framework22.Property.ShortText({
      displayName: "Phone",
      required: !1
    })
  },
  async run(context) {
    return await bigCommerceApiService.createCustomerAddress({
      auth: context.auth.props,
      payload: [
        {
          customer_id: context.propsValue.customer_id,
          first_name: context.propsValue.first_name,
          last_name: context.propsValue.last_name,
          address1: context.propsValue.address1,
          address2: context.propsValue.address2,
          city: context.propsValue.city,
          state_or_province: context.propsValue.state_or_province,
          postal_code: context.propsValue.postal_code,
          country_code: context.propsValue.country_code,
          phone: context.propsValue.phone
        }
      ]
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/search-customer.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var searchCustomer = (0, import_pieces_framework23.createAction)({
  auth: bigcommerceAuth,
  name: "searchCustomer",
  classification: "SEARCH",
  displayName: "Search Customer",
  description: "Searches for a registered customer",
  audience: "both",
  aiMetadata: {
    description: "Searches registered customers in a BigCommerce store by email, phone, and/or name. With no filters it returns all customers; supplying any filter narrows to matches. Use to look up a customer or check whether one exists before creating it. Idempotent read-only query with no side effects.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework23.Property.ShortText({
      displayName: "Email",
      description: "Customer email to search for",
      required: !1
    }),
    phone: import_pieces_framework23.Property.ShortText({
      displayName: "Phone Number",
      description: "Phone Number to search for",
      required: !1
    }),
    name: import_pieces_framework23.Property.ShortText({
      displayName: "Name",
      description: "Customer name to search for",
      required: !1
    })
  },
  async run(context) {
    let params = new URLSearchParams();
    return context.propsValue.email && params.append("email:in", context.propsValue.email), context.propsValue.name && params.append("name:in", `${context.propsValue.name}`), context.propsValue.phone && params.append("phone:in", context.propsValue.phone), await bigCommerceApiService.fetchCustomers({
      auth: context.auth.props,
      queryString: params.toString()
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/search-product.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework");
var searchProduct = (0, import_pieces_framework24.createAction)({
  auth: bigcommerceAuth,
  name: "searchProduct",
  classification: "SEARCH",
  displayName: "Search Product",
  description: "Searches for a product in the catalog",
  audience: "both",
  aiMetadata: {
    description: "Searches the BigCommerce product catalog by name, SKU, type, brand, weight, and/or price. With no filters it returns all products; supplying any filter narrows to matches. Use to find a product or its id, or to check whether one exists before creating it. Idempotent read-only query with no side effects.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework24.Property.ShortText({
      displayName: "Product Name",
      description: "Product name to search for",
      required: !1
    }),
    sku: import_pieces_framework24.Property.ShortText({
      displayName: "SKU",
      description: "Product SKU to search for",
      required: !1
    }),
    type: import_pieces_framework24.Property.StaticDropdown({
      displayName: "Product Type",
      required: !1,
      options: {
        options: [
          { label: "Physical", value: "physical" },
          { label: "Digital", value: "digital" }
        ]
      }
    }),
    brand_id: import_pieces_framework24.Property.ShortText({
      displayName: "Brand ID",
      description: "Brand ID to search for",
      required: !1
    }),
    weight: import_pieces_framework24.Property.Number({
      displayName: "Weight",
      required: !1
    }),
    price: import_pieces_framework24.Property.Number({
      displayName: "Price",
      required: !1
    })
  },
  async run(context) {
    let params = new URLSearchParams();
    return context.propsValue.name && params.append("name", context.propsValue.name), context.propsValue.sku && params.append("sku", context.propsValue.sku), context.propsValue.brand_id && params.append("brand_id", context.propsValue.brand_id), context.propsValue.type && params.append("type", context.propsValue.type), context.propsValue.price && params.append("price", String(context.propsValue.price)), context.propsValue.weight && params.append("weight", String(context.propsValue.weight)), await bigCommerceApiService.fetchProducts({
      auth: context.auth.props,
      queryString: params.toString()
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/search-customer-address.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework");
var searchCustomerAddress = (0, import_pieces_framework25.createAction)({
  auth: bigcommerceAuth,
  name: "searchCustomerAddress",
  classification: "SEARCH",
  displayName: "Search Customer Address",
  description: "Searches for a customer\u2019s address",
  audience: "both",
  aiMetadata: {
    description: "Searches customer addresses in a BigCommerce store, optionally filtered by one or more customer ids and/or a name. With no filters it returns all addresses; supplying filters narrows to matches. Use to look up addresses belonging to specific customers. Idempotent read-only query with no side effects.",
    idempotent: !0
  },
  props: {
    customer_ids: multiCustomerDropdown({
      required: !1
    }),
    name: import_pieces_framework25.Property.ShortText({
      displayName: "Name",
      description: "Customer name to search for",
      required: !1
    })
  },
  async run(context) {
    let params = new URLSearchParams();
    return context.propsValue.customer_ids && params.append(
      "customer_ids:in",
      context.propsValue.customer_ids.join(",")
    ), context.propsValue.name && params.append("name:in", `${context.propsValue.name}`), await bigCommerceApiService.fetchCustomerAddresses({
      auth: context.auth.props,
      queryString: params.toString()
    });
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/find-or-create-customer.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework");
var findOrCreateCustomer = (0, import_pieces_framework26.createAction)({
  auth: bigcommerceAuth,
  name: "findOrCreateCustomer",
  classification: "WRITE",
  displayName: "Find or Create Customer",
  description: "Finds or creates a customer",
  audience: "both",
  aiMetadata: {
    description: "Looks up a BigCommerce customer by email (and phone if given) and returns the match if found, otherwise creates a new customer from the supplied details. Use this instead of Create Customer when the customer may already exist, to avoid duplicate-email errors. Idempotent: repeated calls match the same existing record rather than creating duplicates.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework26.Property.ShortText({
      displayName: "Email",
      description: "Customer email address",
      required: !0
    }),
    first_name: import_pieces_framework26.Property.ShortText({
      displayName: "First Name",
      description: "Customer first name",
      required: !0
    }),
    last_name: import_pieces_framework26.Property.ShortText({
      displayName: "Last Name",
      description: "Customer last name",
      required: !0
    }),
    company: import_pieces_framework26.Property.ShortText({
      displayName: "Company",
      description: "Customer company name",
      required: !1
    }),
    phone: import_pieces_framework26.Property.ShortText({
      displayName: "Phone",
      description: "Customer phone number",
      required: !1
    }),
    notes: import_pieces_framework26.Property.LongText({
      displayName: "Notes",
      description: "Customer notes",
      required: !1
    })
  },
  async run(context) {
    let params = new URLSearchParams();
    params.append("email:in", context.propsValue.email), context.propsValue.phone && params.append("phone:in", context.propsValue.phone);
    let response = await bigCommerceApiService.fetchCustomers({
      auth: context.auth.props,
      queryString: params.toString()
    });
    return response.data && response.data.length > 0 ? { found: !0, data: response.data } : { found: !1, data: (await bigCommerceApiService.createCustomer({
      auth: context.auth.props,
      payload: [
        {
          email: context.propsValue.email,
          first_name: context.propsValue.first_name,
          last_name: context.propsValue.last_name,
          company: context.propsValue.company,
          phone: context.propsValue.phone,
          notes: context.propsValue.notes
        }
      ]
    })).data };
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/find-or-create-product.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework");
var findOrCreateProduct = (0, import_pieces_framework27.createAction)({
  auth: bigcommerceAuth,
  name: "findOrCreateProduct",
  classification: "WRITE",
  displayName: "Find or Create Product",
  description: "Finds or creates a product",
  audience: "both",
  aiMetadata: {
    description: "Looks up a catalog product by exact name and returns the match if found, otherwise creates a new product from the supplied details (type, weight, and price required). Use instead of Create a Product when the item may already exist, to avoid duplicates. Idempotent: repeated calls match the same existing product by name rather than creating duplicates.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework27.Property.ShortText({
      displayName: "Product Name",
      required: !0
    }),
    sku: import_pieces_framework27.Property.ShortText({
      displayName: "SKU",
      required: !1
    }),
    type: import_pieces_framework27.Property.StaticDropdown({
      displayName: "Product Type",
      required: !0,
      options: {
        options: [
          { label: "Physical", value: "physical" },
          { label: "Digital", value: "digital" }
        ]
      }
    }),
    weight: import_pieces_framework27.Property.Number({
      displayName: "Weight",
      required: !0
    }),
    price: import_pieces_framework27.Property.Number({
      displayName: "Price",
      required: !0
    }),
    description: import_pieces_framework27.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    brand_id: import_pieces_framework27.Property.Number({
      displayName: "Brand ID",
      description: "Brand ID",
      required: !1
    })
  },
  async run(context) {
    let params = new URLSearchParams();
    params.append("name", context.propsValue.name);
    let response = await bigCommerceApiService.fetchProducts({
      auth: context.auth.props,
      queryString: params.toString()
    });
    return response.data && response.data.length > 0 ? { found: !0, data: response.data } : { found: !1, data: (await bigCommerceApiService.createProduct({
      auth: context.auth.props,
      payload: {
        name: context.propsValue.name,
        type: context.propsValue.type,
        sku: context.propsValue.sku,
        description: context.propsValue.description,
        weight: context.propsValue.weight,
        price: context.propsValue.price,
        brand_id: context.propsValue.brand_id
      }
    })).data };
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/lib/actions/find-or-create-customers-address.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework");
var findOrCreateCustomersAddress = (0, import_pieces_framework28.createAction)({
  auth: bigcommerceAuth,
  name: "findOrCreateCustomersAddress",
  classification: "WRITE",
  displayName: "Find or Create Customer\u2019s Address",
  description: "Finds or creates a customer\u2019s address",
  audience: "both",
  aiMetadata: {
    description: "For a given customer_id, looks up that customer\u2019s addresses and returns the one matching the supplied address line 1, city, and postal code if it exists, otherwise creates a new address from the supplied details. Use instead of Create Customer Address when the address may already be on file, to avoid duplicates. Idempotent: repeated calls reuse the matching address rather than appending duplicates.",
    idempotent: !0
  },
  props: {
    customer_id: customerDropdown({
      required: !0
    }),
    first_name: import_pieces_framework28.Property.ShortText({
      displayName: "First Name",
      required: !0
    }),
    last_name: import_pieces_framework28.Property.ShortText({
      displayName: "Last Name",
      required: !0
    }),
    address1: import_pieces_framework28.Property.ShortText({
      displayName: "Address Line 1",
      required: !0
    }),
    address2: import_pieces_framework28.Property.ShortText({
      displayName: "Address Line 2",
      required: !1
    }),
    city: import_pieces_framework28.Property.ShortText({
      displayName: "City",
      required: !0
    }),
    state_or_province: import_pieces_framework28.Property.ShortText({
      displayName: "State/Province",
      required: !0
    }),
    postal_code: import_pieces_framework28.Property.ShortText({
      displayName: "Postal Code",
      required: !0
    }),
    country_code: import_pieces_framework28.Property.ShortText({
      displayName: "Country Code",
      description: "ISO 3166-1 alpha-2 country code",
      required: !0
    }),
    phone: import_pieces_framework28.Property.ShortText({
      displayName: "Phone",
      required: !1
    })
  },
  async run(context) {
    let params = new URLSearchParams();
    params.append("customer_id:in", context.propsValue.customer_id);
    let response = await bigCommerceApiService.fetchCustomerAddresses({
      auth: context.auth.props,
      queryString: params.toString()
    });
    if (response.data && response.data.length > 0) {
      let existingAddress = response.data.find(
        (addr) => addr.address1 === context.propsValue.address1 && addr.city === context.propsValue.city && addr.postal_code === context.propsValue.postal_code
      );
      if (existingAddress)
        return { found: !0, data: existingAddress };
    }
    return { found: !1, data: (await bigCommerceApiService.createCustomerAddress({
      auth: context.auth.props,
      payload: [
        {
          customer_id: context.propsValue.customer_id,
          first_name: context.propsValue.first_name,
          last_name: context.propsValue.last_name,
          address1: context.propsValue.address1,
          address2: context.propsValue.address2,
          city: context.propsValue.city,
          state_or_province: context.propsValue.state_or_province,
          postal_code: context.propsValue.postal_code,
          country_code: context.propsValue.country_code,
          phone: context.propsValue.phone
        }
      ]
    })).data };
  }
});

// ../ap/packages/pieces/community/bigcommerce/src/index.ts
var bigcommerce = (0, import_pieces_framework29.createPiece)({
  displayName: "BigCommerce",
  description: "BigCommerce is a leading e-commerce platform that enables businesses to create and manage online stores.",
  auth: bigcommerceAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/bigcommerce.png",
  authors: ["gs03-dev", "sanket-a11y", "Angelebeats"],
  actions: [
    createCustomer,
    createAProduct,
    updateAProduct,
    deleteAProduct,
    getOrder,
    listOrders,
    listCategories,
    createBlogPost,
    createCustomerAddress,
    searchCustomer,
    searchProduct,
    searchCustomerAddress,
    findOrCreateCustomer,
    findOrCreateProduct,
    findOrCreateCustomersAddress,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: bigcommerceAuth,
      baseUrl: (auth) => auth ? GET_BASE_URL(auth.props.storeHash) : "",
      authMapping: async (auth) => ({
        "X-Auth-Token": auth.props.accessToken,
        "Content-Type": "application/json",
        Accept: "application/json"
      })
    })
  ],
  triggers: [
    abandonedCart,
    cartCreated,
    customerAddressCreated,
    customerAddressUpdated,
    customerCreated,
    customerUpdated,
    orderCreated,
    orderUpdated,
    orderStatusUpdated,
    productCreated,
    shipmentCreated
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  bigcommerce
});
