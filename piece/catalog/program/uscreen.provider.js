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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/uscreen/src/index.ts
var index_exports = {};
__export(index_exports, {
  uscreen: () => uscreen
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/uscreen/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/uscreen/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var uscreenPublisherApiUrl = "https://uscreen.io/publisher_api/v1", UscreenClient = class {
  constructor(apiKey) {
    __publicField(this, "apiKey", apiKey);
  }
  /**
   * Makes an authenticated request to the Uscreen PUBLISHER API.
   */
  async makeRequest(method, url, body, query) {
    let request = {
      method,
      url: `${uscreenPublisherApiUrl}${url}`,
      body,
      queryParams: query,
      headers: {
        "X-Store-Token": this.apiKey,
        Accept: "application/json",
        "Content-Type": "application/json"
      }
    }, { body: responseBody } = await import_pieces_common.httpClient.sendRequest(request);
    return responseBody;
  }
  /**
   * Fetches a paginated list of invoices, sorted by payment date.
   */
  async getInvoices(params) {
    let query = {
      sort_by: params.sort_by,
      status: params.status
    };
    params.per_page !== void 0 && (query.per_page = params.per_page.toString());
    try {
      return await this.makeRequest(
        import_pieces_common.HttpMethod.GET,
        "/invoices",
        void 0,
        query
      );
    } catch (e) {
      return console.error("Failed to fetch Uscreen invoices", e), { items: [] };
    }
  }
  /**
   * Fetches a list of offers (subscriptions).
   */
  async getOffers() {
    try {
      return await this.makeRequest(
        import_pieces_common.HttpMethod.GET,
        "/offers"
      );
    } catch (e) {
      return console.error("Failed to fetch Uscreen offers", e), [];
    }
  }
  /**
   * Fetches a list of programs (bundles).
   */
  async getPrograms() {
    try {
      return await this.makeRequest(
        import_pieces_common.HttpMethod.GET,
        "/programs"
      );
    } catch (e) {
      return console.error("Failed to fetch Uscreen programs", e), [];
    }
  }
  async getCustomers() {
    try {
      return await this.makeRequest(
        import_pieces_common.HttpMethod.GET,
        "/customers"
      );
    } catch (e) {
      return console.error("Failed to fetch Uscreen customers", e), [];
    }
  }
};

// ../ap/packages/pieces/community/uscreen/src/lib/common/auth.ts
var uscreenAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
    To get your API key:
    1. Log in to your Uscreen account
    2. Contact your Customer Success Manager to have an API key (X-Store-Token) issued.
    `,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `${uscreenPublisherApiUrl}/offers`,
        headers: {
          "X-Store-Token": auth,
          Accept: "application/json"
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key or insufficient permissions."
      };
    }
  }
});

// ../ap/packages/pieces/community/uscreen/src/lib/actions/assign-user-access.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/uscreen/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var uscreenProps = {
  customerId: (required = !0) => import_pieces_framework2.Property.Dropdown({
    auth: uscreenAuth,
    displayName: "Customer ID or Email",
    description: "The unique ID or email address of the customer.",
    required,
    refreshers: ["productType"],
    options: async (context) => {
      let auth = context.auth;
      return auth ? {
        disabled: !1,
        options: (await new UscreenClient(auth.secret_text).getCustomers()).map((customer) => ({
          label: customer.email,
          value: customer.id
        }))
      } : {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      };
    }
  }),
  productType: (required = !0) => import_pieces_framework2.Property.StaticDropdown({
    displayName: "Product Type",
    description: "The type of product to assign.",
    required,
    options: {
      options: [
        { label: "Program (Bundle)", value: "program" },
        { label: "Offer (Subscription)", value: "offer" }
      ]
    }
  }),
  productId: (required = !0) => import_pieces_framework2.Property.Dropdown({
    auth: uscreenAuth,
    displayName: "Product",
    description: "The bundle (program) or subscription (offer) to assign.",
    required,
    refreshers: ["productType"],
    options: async (context) => {
      let auth = context.auth, productType = context.propsValue.productType;
      if (!auth)
        return {
          disabled: !0,
          placeholder: "Connect your account first",
          options: []
        };
      if (!productType)
        return {
          disabled: !0,
          placeholder: "Select a Product Type first",
          options: []
        };
      let client = new UscreenClient(auth.secret_text), products = [];
      return productType === "program" ? products = await client.getPrograms() : productType === "offer" && (products = await client.getOffers()), {
        disabled: !1,
        options: products.map((product) => ({
          label: product.name,
          value: product.id
        }))
      };
    }
  }),
  webhookInstructions: (required = !0) => import_pieces_framework2.Property.MarkDown({
    value: `## Setup Instructions

### 1. Access Uscreen Webhook Settings
- Log into your **Uscreen Admin Panel**
- Navigate to **Settings** > **Webhooks**

### 2. Create New Webhook
- Click **"New Webhook"**
- **Callback URL**: 
\`\`\`text
{{webhookUrl}}
\`\`\`
- Select the appropriate event 
- Click **Save**`
  })
};

// ../ap/packages/pieces/community/uscreen/src/lib/actions/assign-user-access.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), assignUserAccess = (0, import_pieces_framework3.createAction)({
  auth: uscreenAuth,
  name: "assign_user_access",
  displayName: "Assign User Access",
  description: "Assigns a bundle or subscription to a customer. Creates a new customer if one doesn't exist.",
  audience: "both",
  aiMetadata: { description: "Grants a customer access to a product on Uscreen, where the product type is either a program (bundle) or an offer (subscription); creating the customer if they do not already exist. Use to provision or comp content access. Access can be scheduled for a future ISO 8601 time instead of granted immediately. Not idempotent: each call grants another access entry.", idempotent: !1 },
  props: {
    customer_id: uscreenProps.customerId(),
    productType: uscreenProps.productType(),
    product_id: uscreenProps.productId(),
    perform_action_at: import_pieces_framework3.Property.ShortText({
      displayName: "Schedule Access (Optional)",
      description: "Schedule a time for the action to be performed (ISO 8601 format, e.g., 2024-07-05T13:47:52Z). Leave blank to assign immediately.",
      required: !1
    }),
    with_manual_billing: import_pieces_framework3.Property.Checkbox({
      displayName: "Manual Billing (for Offers)",
      description: 'Only check this if the Product Type is "Offer" and this is a manual billing scenario.',
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { customer_id, product_id, productType, perform_action_at, with_manual_billing } = context.propsValue, client = new UscreenClient(context.auth.secret_text), body = {
      product_id,
      product_type: productType
    };
    return productType === "offer" && with_manual_billing ? body.with_manual_billing = !0 : perform_action_at && (body.perform_action_at = perform_action_at), await client.makeRequest(
      import_pieces_common3.HttpMethod.POST,
      `/customers/${customer_id}/accesses`,
      body
    );
  }
});

// ../ap/packages/pieces/community/uscreen/src/lib/actions/create-user.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createUser = (0, import_pieces_framework4.createAction)({
  auth: uscreenAuth,
  name: "create_user",
  displayName: "Create User",
  description: "Creates a new user and optionally sends them a welcome email to your storefront.",
  audience: "both",
  aiMetadata: { description: "Creates a new customer account on the Uscreen storefront from an email and name, optionally setting a password (omit to send an invite email) and arbitrary storefront-defined custom fields. Use to onboard a viewer or subscriber. Not idempotent: each call creates another account, so guard against duplicate emails.", idempotent: !1 },
  props: {
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email Address",
      description: "The new user's email address.",
      required: !0
    }),
    first_name: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      description: "The new user's first name.",
      required: !0
    }),
    last_name: import_pieces_framework4.Property.ShortText({
      displayName: "Last Name",
      description: "The new user's last name.",
      required: !0
    }),
    password: import_pieces_framework4.Property.ShortText({
      displayName: "Password",
      description: "The new user's password. Leave blank if sending an invite email.",
      required: !1
    }),
    opted_in_for_news_and_updates: import_pieces_framework4.Property.Checkbox({
      displayName: "Opted in for News and Updates",
      description: "If set to true, the new user will receive news and updates.",
      required: !1,
      defaultValue: !0
    }),
    custom_fields: import_pieces_framework4.Property.DynamicProperties({
      auth: uscreenAuth,
      displayName: "Custom Fields",
      description: 'Add custom user fields defined in your storefront (e.g., "favorite_genre").',
      required: !1,
      refreshers: [],
      props: async () => {
        let fields = {};
        return fields.add_field_helper = import_pieces_framework4.Property.ShortText({
          displayName: "Custom Field Instructions",
          description: "Click 'Add Property' to add a custom field. Use the 'Property Name' as the API key (e.g., 'favorite_genre') and enter the value below.",
          required: !1
        }), fields;
      }
    })
  },
  async run(context) {
    let {
      email,
      first_name,
      last_name,
      password,
      opted_in_for_news_and_updates,
      custom_fields
    } = context.propsValue, client = new UscreenClient(context.auth.secret_text), body = __spreadValues({}, custom_fields);
    return delete body.add_field_helper, body.email = email, body.name = first_name + " " + last_name, password && (body.password = password), opted_in_for_news_and_updates && (body.opted_in_for_news_and_updates = opted_in_for_news_and_updates), await client.makeRequest(
      import_pieces_common4.HttpMethod.POST,
      "/customers",
      body
    );
  }
});

// ../ap/packages/pieces/community/uscreen/src/lib/triggers/paid-order.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var sampleData = {
  id: "789012",
  title: "Masterclass Bundle",
  total: "49.99 USD",
  amount: "39.99 USD",
  discount: "10.00 USD",
  offer_id: "98765",
  customer_name: "Jane Smith",
  customer_email: "customer@example.com",
  country_code: "US",
  transaction_id: "tr_1001abcd",
  ip_address: "192.168.1.101",
  origin: "Stripe",
  coupon: "WELCOME10",
  event: "order_paid"
}, paidOrder = (0, import_pieces_framework5.createTrigger)({
  auth: uscreenAuth,
  name: "paid_order",
  displayName: "Paid Order",
  description: "Triggers when a payment is processed for subscriptions, bundles, or content.",
  aiMetadata: {
    description: "Fires when an order is successfully paid on the Uscreen storefront (event order_paid), covering purchases of subscriptions, bundles, or individual content. Represents a completed paid transaction with its amount, customer, and offer details."
  },
  props: {},
  sampleData,
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload;
    return payload.event !== "order_paid" ? [] : [payload];
  },
  async test(context) {
    return [sampleData];
  }
});

// ../ap/packages/pieces/community/uscreen/src/lib/triggers/new-user.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var sampleData2 = {
  event: "user.created",
  data: {
    id: 123456,
    email: "newuser@example.com",
    first_name: "John",
    last_name: "Doe",
    created_at: "2025-10-27T13:00:00Z",
    custom_fields: {
      favorite_genre: "Documentary",
      how_heard_about_us: "Referral"
    }
  }
}, newUser = (0, import_pieces_framework6.createTrigger)({
  auth: uscreenAuth,
  name: "new_user",
  displayName: "New User",
  description: "Triggers when a new user is added to your storefront.",
  aiMetadata: {
    description: "Fires when a new user account is created on the Uscreen storefront. Represents a newly registered viewer or subscriber, including their email, name, and any storefront custom fields."
  },
  props: { setupInstructions: uscreenProps.webhookInstructions() },
  sampleData: sampleData2.data,
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload;
    return payload.event !== "user_created" ? [] : [payload.data];
  },
  async test(context) {
    return [sampleData2.data];
  }
});

// ../ap/packages/pieces/community/uscreen/src/lib/triggers/began-to-play-video.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var sampleData3 = {
  event: "video_play",
  title: "How to Build an API",
  id: 987654,
  chapter_id: 123456,
  name: "John Doe",
  email: "john.doe@example.com"
}, beganToPlayVideo = (0, import_pieces_framework7.createTrigger)({
  auth: uscreenAuth,
  name: "began_to_play_video",
  displayName: "Began to Play Video",
  description: "Triggers when a user plays a video for the first time.",
  aiMetadata: {
    description: "Fires when a user begins playing a video for the first time on the Uscreen storefront (event video_play). Represents an initial video-engagement event, including the video and chapter identifiers and the viewer who started it."
  },
  props: {
    setupInstructions: uscreenProps.webhookInstructions()
  },
  sampleData: sampleData3,
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload;
    return payload.event !== "video_play" ? [] : [payload];
  },
  async test(context) {
    return [sampleData3];
  }
});

// ../ap/packages/pieces/community/uscreen/src/lib/triggers/canceled-subscription.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var sampleData4 = {
  event: "subscription_canceled",
  user_id: 123456,
  user_email: "user@example.com",
  subscription_id: "sub_789xyz",
  subscription_title: "Premium Plan",
  canceled_at: "2025-10-27T13:32:10Z",
  custom_fields: {
    favorite_genre: "Comedy",
    referral_source: "Google Ads"
  }
}, canceledSubscription = (0, import_pieces_framework8.createTrigger)({
  auth: uscreenAuth,
  name: "canceled_subscription",
  displayName: "Canceled Subscription",
  description: "Triggers when a subscription is canceled for a user.",
  aiMetadata: {
    description: "Fires when a user cancels (or has canceled) a subscription on the Uscreen storefront (event subscription_canceled). Represents a churn event, including the affected user, the subscription, and when it was canceled."
  },
  props: { setupInstructions: uscreenProps.webhookInstructions() },
  sampleData: sampleData4,
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload;
    return payload.event !== "subscription_canceled" ? [] : [payload];
  },
  async test(context) {
    return [sampleData4];
  }
});

// ../ap/packages/pieces/community/uscreen/src/lib/triggers/user-updated.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var sampleData5 = {
  id: "   123456",
  email: "john.doe@example.com",
  changes: {
    name: "John Doe",
    email: "john.doe@example.com",
    subscription_status: "active",
    lifetime_spent: "100",
    bounced_email: !1,
    status: "active",
    field_1: "value_1",
    field_2: "value_2",
    field_3: "value_3"
  },
  attributes: {
    name: "John Doe",
    email: "john.doe@example.com",
    subscription_status: "active",
    lifetime_spent: "100",
    bounced_email: !1,
    segments: ["segment_1", "segment_2"],
    status: "active",
    origin: "user.creation_source"
  },
  event: "user_updated"
}, userUpdated = (0, import_pieces_framework9.createTrigger)({
  auth: uscreenAuth,
  name: "user_updated",
  displayName: "User Updated",
  description: "Triggers when a user\u2019s profile or information is updated.",
  aiMetadata: {
    description: "Fires when an existing user\u2019s profile or attributes change on the Uscreen storefront (event user_updated). Represents a customer-record update, exposing both the changed fields and the full current attributes (e.g. subscription status, lifetime spent, segments)."
  },
  props: { setupInstructions: uscreenProps.webhookInstructions() },
  sampleData: sampleData5,
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload;
    return payload.event !== "user_updated" ? [] : [payload];
  },
  async test(context) {
    return [sampleData5];
  }
});

// ../ap/packages/pieces/community/uscreen/src/index.ts
var uscreen = (0, import_pieces_framework10.createPiece)({
  displayName: "Uscreen",
  description: "All-in-one video monetization platform for creating, hosting, and selling online courses, memberships, and video content.",
  auth: uscreenAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/uscreen.png",
  categories: [import_pieces_framework11.PieceCategory.COMMERCE, import_pieces_framework11.PieceCategory.MARKETING],
  authors: ["srimalleswari205", "sanket-a11y"],
  actions: [
    createUser,
    assignUserAccess,
    (0, import_pieces_common5.createCustomApiCallAction)({
      auth: uscreenAuth,
      baseUrl: () => uscreenPublisherApiUrl,
      authMapping: async (auth) => ({
        "X-Store-Token": `${auth.secret_text}`,
        Accept: "application/json"
      })
    })
  ],
  triggers: [
    paidOrder,
    newUser,
    beganToPlayVideo,
    canceledSubscription,
    userUpdated
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  uscreen
});
