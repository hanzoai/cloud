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

// ../ap/packages/pieces/community/ko-fi/src/index.ts
var index_exports = {};
__export(index_exports, {
  koFi: () => koFi
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/ko-fi/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), koFiAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Verification Token",
  description: "Your Ko-fi webhook verification token. Find it in Ko-fi Dashboard > Settings > API/Webhooks > Advanced Settings > Webhook verification token.",
  required: !0
});

// ../ap/packages/pieces/community/ko-fi/src/lib/triggers/new-commission.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var newCommission = (0, import_pieces_framework2.createTrigger)({
  auth: koFiAuth,
  name: "new_commission",
  displayName: "New Commission",
  description: "Triggers when a new commission is received on Ko-fi.",
  aiMetadata: {
    description: "Fires when a supporter places a paid commission request on the connected Ko-fi creator account. Emits the commission event including the requester name, email, the commission brief or message, amount, currency, and shipping details when delivery of a physical item is involved. Use to intake commission work, create tasks, or notify on new requests."
  },
  props: {
    instructions: import_pieces_framework2.Property.MarkDown({
      value: 'To receive commission events, set up a webhook in your Ko-fi Dashboard: go to Settings > API/Webhooks, paste the URL {{webhookUrl}}, and select the "Commission" event type.'
    })
  },
  type: import_pieces_framework2.TriggerStrategy.WEBHOOK,
  sampleData: {
    message_id: "3a1fac0c-f960-4506-a60e-2e3f3d09e6e0",
    timestamp: "2026-04-24T19:15:00Z",
    type: "Commission",
    is_public: !0,
    from_name: "Supporter Name",
    message: "Please draw my cat in the style of a renaissance portrait.",
    amount: "50.00",
    url: "https://ko-fi.com/Home/CoffeeShop?txid=00000000-1111-2222-3333-444444444444",
    email: "supporter@example.com",
    currency: "USD",
    is_subscription_payment: !1,
    is_first_subscription_payment: !1,
    kofi_transaction_id: "00000000-1111-2222-3333-444444444444",
    shipping: null
  },
  /** No-op: Ko-fi has no API to register the webhook URL — the user pastes it into Ko-fi Dashboard manually. */
  async onEnable() {
  },
  /** No-op: Ko-fi has no API to remove the webhook URL — the user clears it from Ko-fi Dashboard manually. */
  async onDisable() {
  },
  async run(context) {
    let body = context.payload.body, event;
    try {
      event = JSON.parse(body.data);
    } catch (e) {
      return [];
    }
    if (event.verification_token !== context.auth.secret_text)
      return [];
    if (event.type !== "Commission")
      return [];
    let _a = event, { verification_token: _omit } = _a;
    return [__objRest(_a, ["verification_token"])];
  }
});

// ../ap/packages/pieces/community/ko-fi/src/lib/triggers/new-donation.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var newDonation = (0, import_pieces_framework3.createTrigger)({
  auth: koFiAuth,
  name: "new_donation",
  displayName: "New Donation",
  description: "Triggers when a new one-time donation is received on Ko-fi.",
  aiMetadata: {
    description: "Fires when a supporter sends a one-time tip or donation to the connected Ko-fi creator account. Emits the donation event including the supporter name, email, message, amount, currency, and Ko-fi transaction ID. Use to thank supporters, log contributions, or trigger downstream notifications."
  },
  props: {
    instructions: import_pieces_framework3.Property.MarkDown({
      value: 'To receive donation events, set up a webhook in your Ko-fi Dashboard: go to Settings > API/Webhooks, paste the URL {{webhookUrl}}, and select the "Donation" event type.'
    })
  },
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  sampleData: {
    message_id: "3a1fac0c-f960-4506-a60e-2e3f3d09e6e0",
    timestamp: "2026-04-24T19:15:00Z",
    type: "Donation",
    is_public: !0,
    from_name: "Supporter Name",
    message: "Thanks for the work you do!",
    amount: "5.00",
    url: "https://ko-fi.com/Home/CoffeeShop?txid=00000000-1111-2222-3333-444444444444",
    email: "supporter@example.com",
    currency: "USD",
    is_subscription_payment: !1,
    is_first_subscription_payment: !1,
    kofi_transaction_id: "00000000-1111-2222-3333-444444444444"
  },
  /** No-op: Ko-fi has no API to register the webhook URL — the user pastes it into Ko-fi Dashboard manually. */
  async onEnable() {
  },
  /** No-op: Ko-fi has no API to remove the webhook URL — the user clears it from Ko-fi Dashboard manually. */
  async onDisable() {
  },
  async run(context) {
    let body = context.payload.body;
    if (typeof (body == null ? void 0 : body.data) != "string")
      return [];
    let event;
    try {
      event = JSON.parse(body.data);
    } catch (e) {
      return [];
    }
    if (event.verification_token !== context.auth.secret_text)
      return [];
    if (event.type !== "Donation")
      return [];
    let _a = event, { verification_token: _omit } = _a;
    return [__objRest(_a, ["verification_token"])];
  }
});

// ../ap/packages/pieces/community/ko-fi/src/lib/triggers/new-shop-order.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var newShopOrder = (0, import_pieces_framework4.createTrigger)({
  auth: koFiAuth,
  name: "new_shop_order",
  displayName: "New Shop Order",
  description: "Triggers when a new shop order is received on Ko-fi.",
  aiMetadata: {
    description: "Fires when a supporter purchases an item from the Ko-fi shop on the connected creator account. Emits the order event including buyer name, email, amount, currency, the purchased shop items (with variation and quantity), and shipping details when the product requires delivery. Use to fulfill orders, record sales, or notify on new purchases."
  },
  props: {
    instructions: import_pieces_framework4.Property.MarkDown({
      value: 'To receive shop order events, set up a webhook in your Ko-fi Dashboard: go to Settings > API/Webhooks, paste the URL {{webhookUrl}}, and select the "Shop Order" event type.'
    })
  },
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  sampleData: {
    message_id: "3a1fac0c-f960-4506-a60e-2e3f3d09e6e0",
    timestamp: "2026-04-24T19:15:00Z",
    type: "Shop Order",
    is_public: !0,
    from_name: "Supporter Name",
    message: "Excited for this!",
    amount: "15.00",
    url: "https://ko-fi.com/Home/CoffeeShop?txid=00000000-1111-2222-3333-444444444444",
    email: "supporter@example.com",
    currency: "USD",
    is_subscription_payment: !1,
    is_first_subscription_payment: !1,
    kofi_transaction_id: "00000000-1111-2222-3333-444444444444",
    shop_items: [
      {
        direct_link_code: "a1b2c3d4e5",
        variation_name: "Standard",
        quantity: 1
      }
    ],
    shipping: null
  },
  /** No-op: Ko-fi has no API to register the webhook URL — the user pastes it into Ko-fi Dashboard manually. */
  async onEnable() {
  },
  /** No-op: Ko-fi has no API to remove the webhook URL — the user clears it from Ko-fi Dashboard manually. */
  async onDisable() {
  },
  async run(context) {
    let body = context.payload.body;
    if (typeof (body == null ? void 0 : body.data) != "string")
      return [];
    let event;
    try {
      event = JSON.parse(body.data);
    } catch (e) {
      return [];
    }
    if (event.verification_token !== context.auth.secret_text)
      return [];
    if (event.type !== "Shop Order")
      return [];
    let _a = event, { verification_token: _omit } = _a;
    return [__objRest(_a, ["verification_token"])];
  }
});

// ../ap/packages/pieces/community/ko-fi/src/lib/triggers/new-subscription.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var newSubscription = (0, import_pieces_framework5.createTrigger)({
  auth: koFiAuth,
  name: "new_subscription",
  displayName: "New Subscription",
  description: "Triggers when a new subscription is received on Ko-fi.",
  aiMetadata: {
    description: "Fires when a supporter starts a new recurring membership on the connected Ko-fi creator account, only on the first subscription payment (recurring renewal payments do not trigger it). Emits the subscription event including the member name, email, message, amount, currency, and the membership tier name. Use to onboard new members, grant access, or send a welcome message."
  },
  props: {
    instructions: import_pieces_framework5.Property.MarkDown({
      value: 'To receive subscription events, set up a webhook in your Ko-fi Dashboard: go to Settings > API/Webhooks, paste the URL {{webhookUrl}}, and select the "Subscription" event type.'
    })
  },
  type: import_pieces_framework5.TriggerStrategy.WEBHOOK,
  sampleData: {
    message_id: "3a1fac0c-f960-4506-a60e-2e3f3d09e6e0",
    timestamp: "2026-04-24T19:15:00Z",
    type: "Subscription",
    is_public: !0,
    from_name: "Supporter Name",
    message: "Thanks for the work you do!",
    amount: "5.00",
    url: "https://ko-fi.com/Home/CoffeeShop?txid=00000000-1111-2222-3333-444444444444",
    email: "supporter@example.com",
    currency: "USD",
    is_subscription_payment: !0,
    is_first_subscription_payment: !0,
    kofi_transaction_id: "00000000-1111-2222-3333-444444444444",
    tier_name: "Gold Tier"
  },
  /** No-op: Ko-fi has no API to register the webhook URL — the user pastes it into Ko-fi Dashboard manually. */
  async onEnable() {
  },
  /** No-op: Ko-fi has no API to remove the webhook URL — the user clears it from Ko-fi Dashboard manually. */
  async onDisable() {
  },
  async run(context) {
    let body = context.payload.body;
    if (typeof (body == null ? void 0 : body.data) != "string")
      return [];
    let event;
    try {
      event = JSON.parse(body.data);
    } catch (e) {
      return [];
    }
    if (event.verification_token !== context.auth.secret_text)
      return [];
    if (event.type !== "Subscription")
      return [];
    if (event.is_first_subscription_payment !== !0)
      return [];
    let _a = event, { verification_token: _omit } = _a;
    return [__objRest(_a, ["verification_token"])];
  }
});

// ../ap/packages/pieces/community/ko-fi/src/index.ts
var koFi = (0, import_pieces_framework6.createPiece)({
  displayName: "Ko-fi",
  description: "Receive triggers for donations, subscriptions, commissions, and shop orders from Ko-fi.",
  auth: koFiAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/ko-fi.png",
  authors: ["zeiyre", "sanket-a11y"],
  categories: [import_pieces_framework7.PieceCategory.PAYMENT_PROCESSING],
  actions: [],
  triggers: [newDonation, newSubscription, newCommission, newShopOrder]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  koFi
});
