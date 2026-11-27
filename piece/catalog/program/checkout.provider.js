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

// ../ap/packages/pieces/community/checkout/src/index.ts
var index_exports = {};
__export(index_exports, {
  checkout: () => checkout
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/checkout/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
function getEnvironmentFromApiKey(apiKey) {
  return apiKey.startsWith("sk_sbox_") ? { environment: "sandbox", baseUrl: "https://api.sandbox.checkout.com" } : apiKey.startsWith("sk_") && !apiKey.startsWith("sk_sbox_") ? { environment: "production", baseUrl: "https://api.checkout.com" } : { environment: "production", baseUrl: "https://api.checkout.com" };
}
var checkoutComAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Secret Key",
  description: "Your Checkout.com secret key. Use sandbox key (sk_sbox_...) for testing or production key (sk_...) for live transactions. You can find it in the Checkout.com dashboard under Developers > API keys.",
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b;
    try {
      let secretKey = auth;
      if (!secretKey.startsWith("sk_"))
        return { valid: !1, error: 'Invalid API key format. Must start with "sk_" for production or "sk_sbox_" for sandbox.' };
      let { environment, baseUrl } = getEnvironmentFromApiKey(secretKey);
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${baseUrl}/workflows`,
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: secretKey
        }
      }), {
        valid: !0,
        environment
      };
    } catch (e) {
      return ((_a = e.response) == null ? void 0 : _a.status) === 401 ? { valid: !1, error: "Invalid Checkout.com secret key. Please check your API key in the Checkout.com dashboard." } : ((_b = e.response) == null ? void 0 : _b.status) === 403 ? { valid: !1, error: "Insufficient permissions. Please ensure your API key has the required permissions." } : { valid: !1, error: "Unable to validate API key. Please check your connection and try again." };
    }
  }
});

// ../ap/packages/pieces/community/checkout/src/lib/actions/create-customer.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), createCustomerAction = (0, import_pieces_framework2.createAction)({
  name: "create_customer",
  classification: "WRITE",
  auth: checkoutComAuth,
  displayName: "Create Customer",
  description: "Store a customer's details in a customer object to reuse in future payments. You can link payment instruments and set a default instrument.",
  audience: "both",
  aiMetadata: { description: "Creates a new Checkout.com customer record from an email (required) plus optional name, phone, metadata, and a default payment instrument, so the customer can be referenced in later payments. Choose it to register a payer before charging them or saving their card. Not idempotent: each call creates a separate customer even with identical input.", idempotent: !1 },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "The customer's email address",
      required: !0
    }),
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      description: "The customer's name",
      required: !1
    }),
    phone_country_code: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Country Code",
      description: "The international country calling code (e.g., +1, +44). Required if phone number is provided.",
      required: !1
    }),
    phone_number: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Number",
      description: "The phone number (6-25 characters). Required if country code is provided.",
      required: !1
    }),
    metadata: import_pieces_framework2.Property.Object({
      displayName: "Metadata",
      description: "Additional information about the customer. Maximum 10 key-value pairs, each up to 100 characters.",
      required: !1
    }),
    default: import_pieces_framework2.Property.ShortText({
      displayName: "Default Instrument ID",
      description: "The ID of the payment instrument to set as this customer's default",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { email, name, phone_country_code, phone_number, metadata, default: defaultInstrument } = context.propsValue, { baseUrl } = getEnvironmentFromApiKey(context.auth.secret_text), body = { email };
    if (name && (body.name = name), phone_country_code || phone_number) {
      if (!phone_country_code || !phone_number)
        throw new Error("Both phone country code and phone number are required when providing phone information");
      if (phone_country_code.length < 1 || phone_country_code.length > 7)
        throw new Error("Country code must be between 1 and 7 characters");
      if (phone_number.length < 6 || phone_number.length > 25)
        throw new Error("Phone number must be between 6 and 25 characters");
      body.phone = {
        country_code: phone_country_code,
        number: phone_number
      };
    }
    if (metadata) {
      if (Object.keys(metadata).length > 10)
        throw new Error("Metadata can have a maximum of 10 key-value pairs");
      for (let [key, value] of Object.entries(metadata)) {
        if (key.length > 100)
          throw new Error(`Metadata key "${key}" exceeds 100 characters`);
        if (typeof value == "string" && value.length > 100)
          throw new Error(`Metadata value for key "${key}" exceeds 100 characters`);
      }
      body.metadata = metadata;
    }
    defaultInstrument && (body.default = defaultInstrument);
    try {
      return (await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.POST,
        url: `${baseUrl}/customers`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body
      })).body;
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 422 ? new Error(`Invalid data: ${((_c = (_b = error.response.body) == null ? void 0 : _b.error_codes) == null ? void 0 : _c.join(", ")) || "Please check your input data"}`) : error;
    }
  }
});

// ../ap/packages/pieces/community/checkout/src/lib/actions/update-customer.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), updateCustomerAction = (0, import_pieces_framework3.createAction)({
  name: "update_customer",
  classification: "WRITE",
  auth: checkoutComAuth,
  displayName: "Update Customer",
  description: "Update existing customer or their metadata.",
  audience: "both",
  aiMetadata: { description: "Updates an existing Checkout.com customer identified by customer ID, changing supplied fields such as email, name, phone, default instrument, or metadata (metadata replaces any existing values rather than merging). Choose it to amend a known customer rather than create one. Idempotent: applying the same field values to the same customer ID yields the same end state.", idempotent: !0 },
  props: {
    customerId: import_pieces_framework3.Property.ShortText({
      displayName: "Customer ID",
      description: "The ID of the customer to update (e.g., cus_y3oqhf46pyzuxjocn2giaqnb44)",
      required: !0
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "The customer's email address",
      required: !1
    }),
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Name",
      description: "The customer's name",
      required: !1
    }),
    phone_country_code: import_pieces_framework3.Property.ShortText({
      displayName: "Phone Country Code",
      description: "The international country calling code (e.g., +1, +44). Required if phone number is provided.",
      required: !1
    }),
    phone_number: import_pieces_framework3.Property.ShortText({
      displayName: "Phone Number",
      description: "The phone number (6-25 characters). Required if country code is provided.",
      required: !1
    }),
    metadata: import_pieces_framework3.Property.Object({
      displayName: "Metadata",
      description: "Additional information about the customer. Will replace any existing metadata.",
      required: !1
    }),
    default: import_pieces_framework3.Property.ShortText({
      displayName: "Default Instrument ID",
      description: "The ID of this customer's default instrument",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { customerId, email, name, phone_country_code, phone_number, metadata, default: defaultInstrument } = context.propsValue, { baseUrl } = getEnvironmentFromApiKey(context.auth.secret_text), body = {};
    if (email && (body.email = email), name && (body.name = name), phone_country_code || phone_number) {
      if (!phone_country_code || !phone_number)
        throw new Error("Both phone country code and phone number are required when providing phone information");
      if (phone_country_code.length < 1 || phone_country_code.length > 7)
        throw new Error("Country code must be between 1 and 7 characters");
      if (phone_number.length < 6 || phone_number.length > 25)
        throw new Error("Phone number must be between 6 and 25 characters");
      body.phone = {
        country_code: phone_country_code,
        number: phone_number
      };
    }
    if (metadata) {
      if (Object.keys(metadata).length > 10)
        throw new Error("Metadata can have a maximum of 10 key-value pairs");
      for (let [key, value] of Object.entries(metadata)) {
        if (key.length > 100)
          throw new Error(`Metadata key "${key}" exceeds 100 characters`);
        if (typeof value == "string" && value.length > 100)
          throw new Error(`Metadata value for key "${key}" exceeds 100 characters`);
      }
      body.metadata = metadata;
    }
    defaultInstrument && (body.default = defaultInstrument);
    try {
      return (await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.PATCH,
        url: `${baseUrl}/customers/${customerId}`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body
      })).body;
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 422 ? new Error(`Invalid data: ${((_c = (_b = error.response.body) == null ? void 0 : _b.error_codes) == null ? void 0 : _c.join(", ")) || "Please check your input data"}`) : error;
    }
  }
});

// ../ap/packages/pieces/community/checkout/src/lib/actions/create-payment-link.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var createPaymentLinkAction = (0, import_pieces_framework4.createAction)({
  name: "create_payment_link",
  classification: "WRITE",
  auth: checkoutComAuth,
  displayName: "Create Payment Link",
  description: "Create a Payment Link to accept and process payment details.",
  audience: "both",
  aiMetadata: { description: "Creates a hosted Checkout.com Payment Link for a given amount, currency, billing country, and processing channel that a customer can open to enter their details and pay, optionally restricting payment methods, attaching products, customer, and shipping info. Choose it when you need a shareable pay-by-link page rather than charging a card directly. Not idempotent: each call generates a new payment link.", idempotent: !1 },
  props: {
    amount: import_pieces_framework4.Property.Number({
      displayName: "Amount",
      description: "The payment amount in minor units (e.g., cents for USD)",
      required: !0
    }),
    currency: import_pieces_framework4.Property.ShortText({
      displayName: "Currency",
      description: "The three-letter ISO currency code (e.g., USD, EUR)",
      required: !0
    }),
    reference: import_pieces_framework4.Property.ShortText({
      displayName: "Reference",
      description: "A reference to identify the payment (e.g., order number)",
      required: !1
    }),
    description: import_pieces_framework4.Property.LongText({
      displayName: "Description",
      description: "A description of the payment",
      required: !1
    }),
    billing_country: import_pieces_framework4.Property.ShortText({
      displayName: "Billing Country",
      description: "The two-letter ISO country code (e.g., US, GB)",
      required: !0
    }),
    billing_address_line1: import_pieces_framework4.Property.ShortText({
      displayName: "Billing Address Line 1",
      description: "The first line of the billing address",
      required: !1
    }),
    billing_address_line2: import_pieces_framework4.Property.ShortText({
      displayName: "Billing Address Line 2",
      description: "The second line of the billing address",
      required: !1
    }),
    billing_city: import_pieces_framework4.Property.ShortText({
      displayName: "Billing City",
      description: "The billing address city",
      required: !1
    }),
    billing_state: import_pieces_framework4.Property.ShortText({
      displayName: "Billing State",
      description: "The state or province (ISO 3166-2 code)",
      required: !1
    }),
    billing_zip: import_pieces_framework4.Property.ShortText({
      displayName: "Billing ZIP",
      description: "The billing address zip or postal code",
      required: !1
    }),
    billing_phone_country_code: import_pieces_framework4.Property.ShortText({
      displayName: "Billing Phone Country Code",
      description: "The international country calling code (e.g., +1)",
      required: !1
    }),
    billing_phone_number: import_pieces_framework4.Property.ShortText({
      displayName: "Billing Phone Number",
      description: "The phone number (6-25 characters)",
      required: !1
    }),
    // Customer Information
    customer_email: import_pieces_framework4.Property.ShortText({
      displayName: "Customer Email",
      description: "The customer's email address",
      required: !1
    }),
    customer_name: import_pieces_framework4.Property.ShortText({
      displayName: "Customer Name",
      description: "The customer's name",
      required: !1
    }),
    customer_phone_country_code: import_pieces_framework4.Property.ShortText({
      displayName: "Customer Phone Country Code",
      description: "The international country calling code (e.g., +1)",
      required: !1
    }),
    customer_phone_number: import_pieces_framework4.Property.ShortText({
      displayName: "Customer Phone Number",
      description: "The customer's phone number",
      required: !1
    }),
    // Payment Configuration
    expires_in: import_pieces_framework4.Property.Number({
      displayName: "Expires In (seconds)",
      description: "Time for which the link remains valid (default: 86400)",
      required: !1
    }),
    display_name: import_pieces_framework4.Property.ShortText({
      displayName: "Display Name",
      description: "The merchant name to display to customers on the checkout page",
      required: !1
    }),
    return_url: import_pieces_framework4.Property.ShortText({
      displayName: "Return URL",
      description: "URL to redirect customer after successful payment",
      required: !1
    }),
    locale: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Locale",
      description: "Language and region for the payment page",
      required: !1,
      options: {
        options: [
          { label: "English (GB)", value: "en-GB" },
          { label: "Arabic", value: "ar" },
          { label: "Danish (Denmark)", value: "da-DK" },
          { label: "German (Germany)", value: "de-DE" },
          { label: "Greek", value: "el" },
          { label: "Spanish (Spain)", value: "es-ES" },
          { label: "Finnish (Finland)", value: "fi-FI" },
          { label: "Filipino (Philippines)", value: "fil-PH" },
          { label: "French (France)", value: "fr-FR" },
          { label: "Hindi (India)", value: "hi-IN" },
          { label: "Indonesian (Indonesia)", value: "id-ID" },
          { label: "Italian (Italy)", value: "it-IT" },
          { label: "Japanese (Japan)", value: "ja-JP" },
          { label: "Malay (Malaysia)", value: "ms-MY" },
          { label: "Norwegian (Norway)", value: "nb-NO" },
          { label: "Dutch (Netherlands)", value: "nl-NL" },
          { label: "Portuguese (Portugal)", value: "pt-PT" },
          { label: "Swedish (Sweden)", value: "sv-SE" },
          { label: "Thai (Thailand)", value: "th-TH" },
          { label: "Vietnamese (Vietnam)", value: "vi-VN" },
          { label: "Chinese (China)", value: "zh-CN" },
          { label: "Chinese (Hong Kong)", value: "zh-HK" },
          { label: "Chinese (Taiwan)", value: "zh-TW" }
        ]
      }
    }),
    // Payment Methods Control
    allow_payment_methods: import_pieces_framework4.Property.StaticMultiSelectDropdown({
      displayName: "Allow Payment Methods",
      description: "Specific payment methods to present to customers",
      required: !1,
      options: {
        options: [
          { label: "Card", value: "card" },
          { label: "PayPal", value: "paypal" },
          { label: "Apple Pay", value: "applepay" },
          { label: "Google Pay", value: "googlepay" },
          { label: "Klarna", value: "klarna" },
          { label: "Alipay CN", value: "alipay_cn" },
          { label: "Bancontact", value: "bancontact" },
          { label: "iDEAL", value: "ideal" },
          { label: "SEPA", value: "sepa" },
          { label: "Benefit", value: "benefit" },
          { label: "KNet", value: "knet" },
          { label: "STC Pay", value: "stcpay" },
          { label: "Tabby", value: "tabby" },
          { label: "Tamara", value: "tamara" }
        ]
      }
    }),
    disabled_payment_methods: import_pieces_framework4.Property.StaticMultiSelectDropdown({
      displayName: "Disabled Payment Methods",
      description: "Payment methods to NOT present to customers",
      required: !1,
      options: {
        options: [
          { label: "Card", value: "card" },
          { label: "PayPal", value: "paypal" },
          { label: "Apple Pay", value: "applepay" },
          { label: "Google Pay", value: "googlepay" },
          { label: "Klarna", value: "klarna" },
          { label: "Alipay CN", value: "alipay_cn" },
          { label: "Bancontact", value: "bancontact" },
          { label: "iDEAL", value: "ideal" },
          { label: "SEPA", value: "sepa" },
          { label: "Benefit", value: "benefit" },
          { label: "KNet", value: "knet" },
          { label: "STC Pay", value: "stcpay" },
          { label: "Tabby", value: "tabby" },
          { label: "Tamara", value: "tamara" }
        ]
      }
    }),
    // Products/Line Items
    products: import_pieces_framework4.Property.Array({
      displayName: "Products",
      description: "Details about the products in the order",
      required: !1,
      properties: {
        name: import_pieces_framework4.Property.ShortText({
          displayName: "Product Name",
          description: "The descriptive name of the product",
          required: !0
        }),
        quantity: import_pieces_framework4.Property.Number({
          displayName: "Quantity",
          description: "The number of items",
          required: !0
        }),
        price: import_pieces_framework4.Property.Number({
          displayName: "Price",
          description: "The price per item in minor units",
          required: !0
        }),
        reference: import_pieces_framework4.Property.ShortText({
          displayName: "Product Reference",
          description: "Product SKU or reference",
          required: !1
        })
      }
    }),
    // Shipping Information
    shipping_country: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping Country",
      description: "The two-letter ISO country code for shipping",
      required: !1
    }),
    shipping_address_line1: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping Address Line 1",
      description: "The first line of the shipping address",
      required: !1
    }),
    shipping_address_line2: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping Address Line 2",
      description: "The second line of the shipping address",
      required: !1
    }),
    shipping_city: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping City",
      description: "The shipping address city",
      required: !1
    }),
    shipping_state: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping State",
      description: "The state or province for shipping",
      required: !1
    }),
    shipping_zip: import_pieces_framework4.Property.ShortText({
      displayName: "Shipping ZIP",
      description: "The shipping address zip or postal code",
      required: !1
    }),
    // Advanced Settings
    payment_type: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Payment Type",
      description: "Type of payment for card transactions",
      required: !1,
      options: {
        options: [
          { label: "Regular", value: "Regular" },
          { label: "Recurring", value: "Recurring" }
        ]
      }
    }),
    processing_channel_id: import_pieces_framework4.Property.ShortText({
      displayName: "Processing Channel ID",
      description: "The processing channel to be used for the payment. Find this in your Checkout.com dashboard under Settings > Processing Channels. Format: pc_xxxxxxxxxxxxxxxxxxxxxxxxxx",
      required: !0
    }),
    capture: import_pieces_framework4.Property.Checkbox({
      displayName: "Capture Payment",
      description: "Whether to capture the payment immediately",
      required: !1
    }),
    enable_3ds: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable 3D Secure",
      description: "Whether to process as 3D Secure payment",
      required: !1
    }),
    challenge_3ds: import_pieces_framework4.Property.StaticDropdown({
      displayName: "3DS Challenge Preference",
      description: "Preference for 3DS challenge",
      required: !1,
      options: {
        options: [
          { label: "No Preference", value: "no_preference" },
          { label: "No Challenge Requested", value: "no_challenge_requested" },
          { label: "Challenge Requested", value: "challenge_requested" }
        ]
      }
    }),
    // Metadata
    metadata: import_pieces_framework4.Property.Object({
      displayName: "Metadata",
      description: "Additional key-value pairs for transaction information",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let {
      amount,
      currency,
      reference,
      description,
      billing_country,
      billing_address_line1,
      billing_address_line2,
      billing_city,
      billing_state,
      billing_zip,
      billing_phone_country_code,
      billing_phone_number,
      customer_email,
      customer_name,
      customer_phone_country_code,
      customer_phone_number,
      expires_in,
      display_name,
      return_url,
      locale,
      allow_payment_methods,
      disabled_payment_methods,
      products,
      shipping_country,
      shipping_address_line1,
      shipping_address_line2,
      shipping_city,
      shipping_state,
      shipping_zip,
      payment_type,
      processing_channel_id,
      capture,
      enable_3ds,
      challenge_3ds,
      metadata
    } = context.propsValue, { baseUrl } = getEnvironmentFromApiKey(context.auth.secret_text), body = {
      amount,
      currency,
      billing: {
        address: {
          country: billing_country
        }
      }
    };
    billing_address_line1 && (body.billing.address.address_line1 = billing_address_line1), billing_address_line2 && (body.billing.address.address_line2 = billing_address_line2), billing_city && (body.billing.address.city = billing_city), billing_state && (body.billing.address.state = billing_state), billing_zip && (body.billing.address.zip = billing_zip), billing_phone_country_code && billing_phone_number && (body.billing.phone = {
      country_code: billing_phone_country_code,
      number: billing_phone_number
    }), reference && (body.reference = reference), description && (body.description = description), display_name && (body.display_name = display_name), return_url && (body.return_url = return_url), locale && (body.locale = locale), expires_in && (body.expires_in = expires_in), payment_type && (body.payment_type = payment_type), processing_channel_id && (body.processing_channel_id = processing_channel_id), typeof capture == "boolean" && (body.capture = capture), (customer_email || customer_name || customer_phone_country_code && customer_phone_number) && (body.customer = {}, customer_email && (body.customer.email = customer_email), customer_name && (body.customer.name = customer_name), customer_phone_country_code && customer_phone_number && (body.customer.phone = {
      country_code: customer_phone_country_code,
      number: customer_phone_number
    })), (shipping_country || shipping_address_line1 || shipping_city || shipping_state || shipping_zip) && (body.shipping = {
      address: {}
    }, shipping_country && (body.shipping.address.country = shipping_country), shipping_address_line1 && (body.shipping.address.address_line1 = shipping_address_line1), shipping_address_line2 && (body.shipping.address.address_line2 = shipping_address_line2), shipping_city && (body.shipping.address.city = shipping_city), shipping_state && (body.shipping.address.state = shipping_state), shipping_zip && (body.shipping.address.zip = shipping_zip)), allow_payment_methods && allow_payment_methods.length > 0 && (body.allow_payment_methods = allow_payment_methods), disabled_payment_methods && disabled_payment_methods.length > 0 && (body.disabled_payment_methods = disabled_payment_methods), products && products.length > 0 && (body.products = products.map((product) => __spreadValues({
      name: product.name,
      quantity: product.quantity,
      price: product.price
    }, product.reference && { reference: product.reference }))), enable_3ds && (body["3ds"] = __spreadValues({
      enabled: !0
    }, challenge_3ds && { challenge_indicator: challenge_3ds })), metadata && Object.keys(metadata).length > 0 && (body.metadata = metadata);
    try {
      return (await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.POST,
        url: `${baseUrl}/payment-links`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body
      })).body;
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 422 ? new Error(`Invalid data: ${((_c = (_b = error.response.body) == null ? void 0 : _b.error_codes) == null ? void 0 : _c.join(", ")) || "Please check your input data"}`) : error;
    }
  }
});

// ../ap/packages/pieces/community/checkout/src/lib/actions/create-payout.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), createPayoutAction = (0, import_pieces_framework5.createAction)({
  name: "create_payment",
  classification: "WRITE",
  auth: checkoutComAuth,
  displayName: "Create Payment",
  description: "Process and manage payments from various sources including card payments and payouts.",
  audience: "both",
  aiMetadata: { description: "Submits a payment to Checkout.com for a given currency and amount, drawing from one of several source types selected by the source type field: raw card details, a saved customer ID, a token, or a stored instrument. Choose it to actually charge a payer (or run a zero-amount card verification), as opposed to generating a shareable link. Supports capture, 3D Secure, and risk options. Not idempotent: each call initiates a new payment and moves money.", idempotent: !1 },
  props: {
    // Core Payment Information
    currency: import_pieces_framework5.Property.ShortText({
      displayName: "Currency",
      description: "The three-letter ISO currency code (e.g., USD, EUR)",
      required: !0
    }),
    amount: import_pieces_framework5.Property.Number({
      displayName: "Amount",
      description: "The payment amount in minor units (e.g., cents for USD). Use 0 for card verification.",
      required: !1
    }),
    reference: import_pieces_framework5.Property.ShortText({
      displayName: "Reference",
      description: "Your reference for the payment (e.g., order number)",
      required: !1
    }),
    description: import_pieces_framework5.Property.LongText({
      displayName: "Description",
      description: "A description of the payment",
      required: !1
    }),
    // Payment Source - Card Details
    source_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Payment Source Type",
      description: "The type of payment source",
      required: !0,
      options: {
        options: [
          { label: "Card Payment", value: "card" },
          { label: "Customer ID", value: "customer" },
          { label: "Token", value: "token" },
          { label: "Instrument", value: "instrument" }
        ]
      }
    }),
    card_number: import_pieces_framework5.Property.ShortText({
      displayName: "Card Number",
      description: "The card number (without separators). Required for card payments.",
      required: !1
    }),
    expiry_month: import_pieces_framework5.Property.Number({
      displayName: "Expiry Month",
      description: "The expiry month of the card (1-12)",
      required: !1
    }),
    expiry_year: import_pieces_framework5.Property.Number({
      displayName: "Expiry Year",
      description: "The expiry year of the card (4 digits)",
      required: !1
    }),
    cvv: import_pieces_framework5.Property.ShortText({
      displayName: "CVV",
      description: "The card verification code (3-4 digits)",
      required: !1
    }),
    cardholder_name: import_pieces_framework5.Property.ShortText({
      displayName: "Cardholder Name",
      description: "The name of the cardholder",
      required: !1
    }),
    // Alternative Source IDs
    customer_id: import_pieces_framework5.Property.ShortText({
      displayName: "Customer ID",
      description: "The customer ID (for customer source type, e.g., cus_y3oqhf46pyzuxjocn2giaqnb44)",
      required: !1
    }),
    token_id: import_pieces_framework5.Property.ShortText({
      displayName: "Token ID",
      description: "The token ID (for token source type)",
      required: !1
    }),
    instrument_id: import_pieces_framework5.Property.ShortText({
      displayName: "Instrument ID",
      description: "The instrument ID (for instrument source type)",
      required: !1
    }),
    // Billing Address
    billing_address_line1: import_pieces_framework5.Property.ShortText({
      displayName: "Billing Address Line 1",
      description: "The first line of the billing address",
      required: !1
    }),
    billing_address_line2: import_pieces_framework5.Property.ShortText({
      displayName: "Billing Address Line 2",
      description: "The second line of the billing address",
      required: !1
    }),
    billing_city: import_pieces_framework5.Property.ShortText({
      displayName: "Billing City",
      description: "The billing city",
      required: !1
    }),
    billing_state: import_pieces_framework5.Property.ShortText({
      displayName: "Billing State",
      description: "The billing state or province",
      required: !1
    }),
    billing_zip: import_pieces_framework5.Property.ShortText({
      displayName: "Billing ZIP",
      description: "The billing ZIP or postal code",
      required: !1
    }),
    billing_country: import_pieces_framework5.Property.ShortText({
      displayName: "Billing Country",
      description: "The two-letter ISO country code",
      required: !1
    }),
    billing_phone_country_code: import_pieces_framework5.Property.ShortText({
      displayName: "Billing Phone Country Code",
      description: "The international country calling code (e.g., +1)",
      required: !1
    }),
    billing_phone_number: import_pieces_framework5.Property.ShortText({
      displayName: "Billing Phone Number",
      description: "The phone number (6-25 characters)",
      required: !1
    }),
    // Account Holder Information
    account_holder_first_name: import_pieces_framework5.Property.ShortText({
      displayName: "Account Holder First Name",
      description: "The first name of the account holder",
      required: !1
    }),
    account_holder_last_name: import_pieces_framework5.Property.ShortText({
      displayName: "Account Holder Last Name",
      description: "The last name of the account holder",
      required: !1
    }),
    account_holder_middle_name: import_pieces_framework5.Property.ShortText({
      displayName: "Account Holder Middle Name",
      description: "The middle name of the account holder",
      required: !1
    }),
    // Customer Information
    customer_email: import_pieces_framework5.Property.ShortText({
      displayName: "Customer Email",
      description: "The customer's email address",
      required: !1
    }),
    customer_name: import_pieces_framework5.Property.ShortText({
      displayName: "Customer Name",
      description: "The customer's name",
      required: !1
    }),
    customer_phone_country_code: import_pieces_framework5.Property.ShortText({
      displayName: "Customer Phone Country Code",
      description: "The international country calling code",
      required: !1
    }),
    customer_phone_number: import_pieces_framework5.Property.ShortText({
      displayName: "Customer Phone Number",
      description: "The customer's phone number",
      required: !1
    }),
    // Shipping Information
    shipping_first_name: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping First Name",
      description: "The first name for shipping",
      required: !1
    }),
    shipping_last_name: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping Last Name",
      description: "The last name for shipping",
      required: !1
    }),
    shipping_email: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping Email",
      description: "The email for shipping notifications",
      required: !1
    }),
    shipping_address_line1: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping Address Line 1",
      description: "The first line of the shipping address",
      required: !1
    }),
    shipping_address_line2: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping Address Line 2",
      description: "The second line of the shipping address",
      required: !1
    }),
    shipping_city: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping City",
      description: "The shipping city",
      required: !1
    }),
    shipping_state: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping State",
      description: "The shipping state or province",
      required: !1
    }),
    shipping_zip: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping ZIP",
      description: "The shipping ZIP or postal code",
      required: !1
    }),
    shipping_country: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping Country",
      description: "The two-letter ISO country code for shipping",
      required: !1
    }),
    // Payment Configuration
    payment_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Payment Type",
      description: "The type of payment",
      required: !1,
      options: {
        options: [
          { label: "Regular", value: "Regular" },
          { label: "Recurring", value: "Recurring" },
          { label: "MOTO (Mail Order/Telephone Order)", value: "MOTO" },
          { label: "Installment", value: "Installment" },
          { label: "Pay Later", value: "PayLater" },
          { label: "Unscheduled", value: "Unscheduled" }
        ]
      }
    }),
    capture: import_pieces_framework5.Property.Checkbox({
      displayName: "Capture Payment",
      description: "Whether to capture the payment immediately",
      required: !1
    }),
    authorization_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Authorization Type",
      description: "The type of authorization",
      required: !1,
      options: {
        options: [
          { label: "Final", value: "Final" },
          { label: "Estimated", value: "Estimated" }
        ]
      }
    }),
    // 3D Secure Configuration
    enable_3ds: import_pieces_framework5.Property.Checkbox({
      displayName: "Enable 3D Secure",
      description: "Whether to process as 3D Secure payment",
      required: !1
    }),
    challenge_3ds: import_pieces_framework5.Property.StaticDropdown({
      displayName: "3DS Challenge Preference",
      description: "Preference for 3DS challenge",
      required: !1,
      options: {
        options: [
          { label: "No Preference", value: "no_preference" },
          { label: "No Challenge Requested", value: "no_challenge_requested" },
          { label: "Challenge Requested", value: "challenge_requested" },
          { label: "Challenge Requested Mandate", value: "challenge_requested_mandate" }
        ]
      }
    }),
    allow_3ds_upgrade: import_pieces_framework5.Property.Checkbox({
      displayName: "Allow 3DS Upgrade",
      description: "Process as 3DS if soft declined due to 3DS authentication required",
      required: !1
    }),
    // Risk Management
    enable_risk_assessment: import_pieces_framework5.Property.Checkbox({
      displayName: "Enable Risk Assessment",
      description: "Whether to perform risk assessment",
      required: !1
    }),
    customer_ip: import_pieces_framework5.Property.ShortText({
      displayName: "Customer IP Address",
      description: "The customer's IP address (IPv4 or IPv6)",
      required: !1
    }),
    // Processing Options
    processing_channel_id: import_pieces_framework5.Property.ShortText({
      displayName: "Processing Channel ID",
      description: "The processing channel to use for the payment",
      required: !1
    }),
    previous_payment_id: import_pieces_framework5.Property.ShortText({
      displayName: "Previous Payment ID",
      description: "Link to existing payment series (for recurring payments)",
      required: !1
    }),
    // Success/Failure URLs
    success_url: import_pieces_framework5.Property.ShortText({
      displayName: "Success URL",
      description: "Success redirect URL for redirect payment methods",
      required: !1
    }),
    failure_url: import_pieces_framework5.Property.ShortText({
      displayName: "Failure URL",
      description: "Failure redirect URL for redirect payment methods",
      required: !1
    }),
    // Metadata
    metadata: import_pieces_framework5.Property.Object({
      displayName: "Metadata",
      description: "Additional key-value pairs for transaction information",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let {
      currency,
      amount,
      reference,
      description,
      source_type,
      card_number,
      expiry_month,
      expiry_year,
      cvv,
      cardholder_name,
      customer_id,
      token_id,
      instrument_id,
      billing_address_line1,
      billing_address_line2,
      billing_city,
      billing_state,
      billing_zip,
      billing_country,
      billing_phone_country_code,
      billing_phone_number,
      account_holder_first_name,
      account_holder_last_name,
      account_holder_middle_name,
      customer_email,
      customer_name,
      customer_phone_country_code,
      customer_phone_number,
      shipping_first_name,
      shipping_last_name,
      shipping_email,
      shipping_address_line1,
      shipping_address_line2,
      shipping_city,
      shipping_state,
      shipping_zip,
      shipping_country,
      payment_type,
      capture,
      authorization_type,
      enable_3ds,
      challenge_3ds,
      allow_3ds_upgrade,
      enable_risk_assessment,
      customer_ip,
      processing_channel_id,
      previous_payment_id,
      success_url,
      failure_url,
      metadata
    } = context.propsValue, { baseUrl } = getEnvironmentFromApiKey(context.auth.secret_text), body = {
      currency
    };
    if (typeof amount == "number" && (body.amount = amount), reference && (body.reference = reference), description && (body.description = description), payment_type && (body.payment_type = payment_type), typeof capture == "boolean" && (body.capture = capture), authorization_type && (body.authorization_type = authorization_type), processing_channel_id && (body.processing_channel_id = processing_channel_id), previous_payment_id && (body.previous_payment_id = previous_payment_id), success_url && (body.success_url = success_url), failure_url && (body.failure_url = failure_url), source_type) {
      if (body.source = { type: source_type }, source_type === "card") {
        if (!card_number || !expiry_month || !expiry_year)
          throw new Error("Card number, expiry month, and expiry year are required for card payments");
        body.source.number = card_number, body.source.expiry_month = expiry_month, body.source.expiry_year = expiry_year, cvv && (body.source.cvv = cvv), cardholder_name && (body.source.name = cardholder_name), (billing_address_line1 || billing_city || billing_country) && (body.source.billing_address = {}, billing_address_line1 && (body.source.billing_address.address_line1 = billing_address_line1), billing_address_line2 && (body.source.billing_address.address_line2 = billing_address_line2), billing_city && (body.source.billing_address.city = billing_city), billing_state && (body.source.billing_address.state = billing_state), billing_zip && (body.source.billing_address.zip = billing_zip), billing_country && (body.source.billing_address.country = billing_country)), billing_phone_country_code && billing_phone_number && (body.source.phone = {
          country_code: billing_phone_country_code,
          number: billing_phone_number
        }), account_holder_first_name && account_holder_last_name && (body.source.account_holder = {
          type: "individual",
          first_name: account_holder_first_name,
          last_name: account_holder_last_name
        }, account_holder_middle_name && (body.source.account_holder.middle_name = account_holder_middle_name));
      } else if (source_type === "customer") {
        if (!customer_id)
          throw new Error("Customer ID is required for customer source type");
        body.source.id = customer_id;
      } else if (source_type === "token") {
        if (!token_id)
          throw new Error("Token ID is required for token source type");
        body.source.token = token_id;
      } else if (source_type === "instrument") {
        if (!instrument_id)
          throw new Error("Instrument ID is required for instrument source type");
        body.source.id = instrument_id;
      }
    }
    (customer_email || customer_name || customer_phone_country_code && customer_phone_number) && (body.customer = {}, customer_email && (body.customer.email = customer_email), customer_name && (body.customer.name = customer_name), customer_phone_country_code && customer_phone_number && (body.customer.phone = {
      country_code: customer_phone_country_code,
      number: customer_phone_number
    })), (shipping_first_name || shipping_last_name || shipping_email || shipping_address_line1) && (body.shipping = {}, shipping_first_name && (body.shipping.first_name = shipping_first_name), shipping_last_name && (body.shipping.last_name = shipping_last_name), shipping_email && (body.shipping.email = shipping_email), (shipping_address_line1 || shipping_city || shipping_country) && (body.shipping.address = {}, shipping_address_line1 && (body.shipping.address.address_line1 = shipping_address_line1), shipping_address_line2 && (body.shipping.address.address_line2 = shipping_address_line2), shipping_city && (body.shipping.address.city = shipping_city), shipping_state && (body.shipping.address.state = shipping_state), shipping_zip && (body.shipping.address.zip = shipping_zip), shipping_country && (body.shipping.address.country = shipping_country))), enable_3ds && (body["3ds"] = {
      enabled: !0
    }, challenge_3ds && (body["3ds"].challenge_indicator = challenge_3ds), typeof allow_3ds_upgrade == "boolean" && (body["3ds"].allow_upgrade = allow_3ds_upgrade)), typeof enable_risk_assessment == "boolean" && (body.risk = {
      enabled: enable_risk_assessment
    }, customer_ip && (body.risk.device = {
      network: {
        [customer_ip.includes(":") ? "ipv6" : "ipv4"]: customer_ip
      }
    })), metadata && Object.keys(metadata).length > 0 && (body.metadata = metadata);
    try {
      return (await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.POST,
        url: `${baseUrl}/payments`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body
      })).body;
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 422 ? new Error(`Invalid data: ${((_c = (_b = error.response.body) == null ? void 0 : _b.error_codes) == null ? void 0 : _c.join(", ")) || "Please check your input data"}`) : ((_d = error.response) == null ? void 0 : _d.status) === 429 ? new Error("Too many requests or duplicate request detected") : error;
    }
  }
});

// ../ap/packages/pieces/community/checkout/src/lib/actions/refund-payment.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), refundPaymentAction = (0, import_pieces_framework6.createAction)({
  name: "refund_payment",
  classification: "DESTRUCTIVE",
  auth: checkoutComAuth,
  displayName: "Refund a Payment",
  description: "Issue a refund (full or partial) for a captured payment. Supports split refunds, line items, and bank account destinations.",
  audience: "both",
  aiMetadata: { description: "Refunds a captured Checkout.com payment, fully (omit amount) or partially (specify amount in minor units), with optional split allocations to sub-entities, line items, and a bank-account destination for methods like giropay/EPS. Requires the payment ID, resolved from an order reference. Choose it to return funds to a payer. Not idempotent: each call issues a separate refund and moves money.", idempotent: !1 },
  props: {
    reference: import_pieces_framework6.Property.ShortText({
      displayName: "Reference",
      description: "A reference, such as an order ID, that can be used to identify the payment",
      required: !0
    }),
    payment_id: import_pieces_framework6.Property.Dropdown({
      auth: checkoutComAuth,
      displayName: "Payment ID",
      description: "Select the payment to refund",
      required: !0,
      refreshers: ["reference"],
      options: async ({ auth, reference }) => {
        if (!reference || !auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please enter a reference first"
          };
        try {
          let { baseUrl } = getEnvironmentFromApiKey(auth.secret_text), refundablePayments = ((await import_pieces_common6.httpClient.sendRequest({
            method: import_pieces_common6.HttpMethod.GET,
            url: `${baseUrl}/payments`,
            queryParams: {
              reference,
              limit: "100"
            },
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          })).body.data || []).filter(
            (payment) => payment.status === "Captured" || payment.status === "Authorized"
          );
          return refundablePayments.length === 0 ? {
            disabled: !0,
            options: [],
            placeholder: "No refundable payments found for this reference"
          } : {
            disabled: !1,
            options: refundablePayments.map((payment) => ({
              label: `${payment.id} - ${payment.amount} ${payment.currency} (${payment.status})`,
              value: payment.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading payments"
          };
        }
      }
    }),
    amount: import_pieces_framework6.Property.Number({
      displayName: "Refund Amount",
      description: "The amount to refund in minor units. If not specified, full payment amount will be refunded.",
      required: !1
    }),
    refund_reference: import_pieces_framework6.Property.ShortText({
      displayName: "Refund Reference",
      description: "Your reference for the refund (max 80 chars, 30 for Amex, 50 for TWINT)",
      required: !1
    }),
    capture_action_id: import_pieces_framework6.Property.ShortText({
      displayName: "Capture Action ID",
      description: "The action ID of the capture to refund (only for PayPal and Riverty)",
      required: !1
    }),
    amount_allocations: import_pieces_framework6.Property.Array({
      displayName: "Amount Allocations",
      description: "Split refund allocations for sub-entities",
      required: !1,
      properties: {
        id: import_pieces_framework6.Property.ShortText({
          displayName: "Sub-entity ID",
          description: "The ID of the sub-entity",
          required: !0
        }),
        amount: import_pieces_framework6.Property.Number({
          displayName: "Split Amount",
          description: "The amount to refund to this sub-entity in minor units",
          required: !0
        }),
        reference: import_pieces_framework6.Property.ShortText({
          displayName: "Split Reference",
          description: "Reference for this split (e.g., order number)",
          required: !1
        }),
        commission_amount: import_pieces_framework6.Property.Number({
          displayName: "Commission Amount",
          description: "Fixed commission amount in minor units",
          required: !1
        }),
        commission_percentage: import_pieces_framework6.Property.Number({
          displayName: "Commission Percentage",
          description: "Commission percentage (0-100, supports up to 8 decimal places)",
          required: !1
        })
      }
    }),
    // Line Items for Detailed Refunds
    items: import_pieces_framework6.Property.Array({
      displayName: "Line Items",
      description: "Order line items for the refund",
      required: !1,
      properties: {
        type: import_pieces_framework6.Property.ShortText({
          displayName: "Item Type",
          description: "The item type (e.g., physical, digital)",
          required: !1
        }),
        name: import_pieces_framework6.Property.ShortText({
          displayName: "Item Name",
          description: "The descriptive name of the line item",
          required: !1
        }),
        quantity: import_pieces_framework6.Property.Number({
          displayName: "Quantity",
          description: "The number of line items",
          required: !1
        }),
        unit_price: import_pieces_framework6.Property.Number({
          displayName: "Unit Price",
          description: "The unit price in minor currency units",
          required: !1
        }),
        reference: import_pieces_framework6.Property.ShortText({
          displayName: "Item Reference",
          description: "The item reference or product SKU",
          required: !1
        }),
        total_amount: import_pieces_framework6.Property.Number({
          displayName: "Total Amount",
          description: "The total cost including tax and discount in minor units",
          required: !1
        }),
        tax_rate: import_pieces_framework6.Property.Number({
          displayName: "Tax Rate",
          description: "Tax rate in minor units (e.g., 2000 = 20%)",
          required: !1
        }),
        tax_amount: import_pieces_framework6.Property.Number({
          displayName: "Tax Amount",
          description: "Total tax amount in minor units",
          required: !1
        }),
        discount_amount: import_pieces_framework6.Property.Number({
          displayName: "Discount Amount",
          description: "Discount applied to the line item",
          required: !1
        }),
        url: import_pieces_framework6.Property.ShortText({
          displayName: "Product URL",
          description: "Link to the product page",
          required: !1
        }),
        image_url: import_pieces_framework6.Property.ShortText({
          displayName: "Product Image URL",
          description: "Link to the product image",
          required: !1
        })
      }
    }),
    // Bank Account Destination (Required for giropay and EPS)
    destination_country: import_pieces_framework6.Property.ShortText({
      displayName: "Destination Country",
      description: "Two-letter ISO country code for bank account (required for giropay/EPS)",
      required: !1
    }),
    destination_account_number: import_pieces_framework6.Property.ShortText({
      displayName: "Account Number",
      description: "The bank account number",
      required: !1
    }),
    destination_bank_code: import_pieces_framework6.Property.ShortText({
      displayName: "Bank Code",
      description: "The code that identifies the bank",
      required: !1
    }),
    destination_iban: import_pieces_framework6.Property.ShortText({
      displayName: "IBAN",
      description: "International Bank Account Number",
      required: !1
    }),
    destination_swift_bic: import_pieces_framework6.Property.ShortText({
      displayName: "SWIFT BIC",
      description: "8 or 11-digit code identifying the bank",
      required: !1
    }),
    // Account Holder Information
    account_holder_first_name: import_pieces_framework6.Property.ShortText({
      displayName: "Account Holder First Name",
      description: "The account holder's first name",
      required: !1
    }),
    account_holder_last_name: import_pieces_framework6.Property.ShortText({
      displayName: "Account Holder Last Name",
      description: "The account holder's last name",
      required: !1
    }),
    account_holder_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Account Holder Type",
      description: "The type of account holder",
      required: !1,
      options: {
        options: [
          { label: "Individual", value: "individual" },
          { label: "Corporate", value: "corporate" },
          { label: "Government", value: "government" }
        ]
      }
    }),
    account_holder_company_name: import_pieces_framework6.Property.ShortText({
      displayName: "Company Name",
      description: "Company name for corporate account holders",
      required: !1
    }),
    account_holder_email: import_pieces_framework6.Property.ShortText({
      displayName: "Account Holder Email",
      description: "The account holder's email address",
      required: !1
    }),
    account_holder_phone_country_code: import_pieces_framework6.Property.ShortText({
      displayName: "Phone Country Code",
      description: "International country calling code",
      required: !1
    }),
    account_holder_phone_number: import_pieces_framework6.Property.ShortText({
      displayName: "Phone Number",
      description: "The account holder's phone number",
      required: !1
    }),
    // Account Holder Address
    account_holder_address_line1: import_pieces_framework6.Property.ShortText({
      displayName: "Address Line 1",
      description: "First line of the account holder's address",
      required: !1
    }),
    account_holder_address_line2: import_pieces_framework6.Property.ShortText({
      displayName: "Address Line 2",
      description: "Second line of the account holder's address",
      required: !1
    }),
    account_holder_city: import_pieces_framework6.Property.ShortText({
      displayName: "City",
      description: "Account holder's city",
      required: !1
    }),
    account_holder_state: import_pieces_framework6.Property.ShortText({
      displayName: "State",
      description: "Account holder's state or province",
      required: !1
    }),
    account_holder_zip: import_pieces_framework6.Property.ShortText({
      displayName: "ZIP Code",
      description: "Account holder's ZIP or postal code",
      required: !1
    }),
    account_holder_country: import_pieces_framework6.Property.ShortText({
      displayName: "Country",
      description: "Two-letter ISO country code",
      required: !1
    }),
    // Bank Details
    bank_name: import_pieces_framework6.Property.ShortText({
      displayName: "Bank Name",
      description: "The name of the bank",
      required: !1
    }),
    bank_branch: import_pieces_framework6.Property.ShortText({
      displayName: "Bank Branch",
      description: "The name of the bank branch",
      required: !1
    }),
    bank_address_line1: import_pieces_framework6.Property.ShortText({
      displayName: "Bank Address Line 1",
      description: "First line of the bank's address",
      required: !1
    }),
    bank_address_line2: import_pieces_framework6.Property.ShortText({
      displayName: "Bank Address Line 2",
      description: "Second line of the bank's address",
      required: !1
    }),
    bank_city: import_pieces_framework6.Property.ShortText({
      displayName: "Bank City",
      description: "The bank's city",
      required: !1
    }),
    bank_country: import_pieces_framework6.Property.ShortText({
      displayName: "Bank Country",
      description: "Two-letter ISO country code for the bank",
      required: !1
    }),
    // Metadata
    metadata: import_pieces_framework6.Property.Object({
      displayName: "Metadata",
      description: "Additional key-value pairs for the refund request",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let {
      reference,
      payment_id,
      amount,
      refund_reference,
      capture_action_id,
      amount_allocations,
      items,
      destination_country,
      destination_account_number,
      destination_bank_code,
      destination_iban,
      destination_swift_bic,
      account_holder_first_name,
      account_holder_last_name,
      account_holder_type,
      account_holder_company_name,
      account_holder_email,
      account_holder_phone_country_code,
      account_holder_phone_number,
      account_holder_address_line1,
      account_holder_address_line2,
      account_holder_city,
      account_holder_state,
      account_holder_zip,
      account_holder_country,
      bank_name,
      bank_branch,
      bank_address_line1,
      bank_address_line2,
      bank_city,
      bank_country,
      metadata
    } = context.propsValue, { baseUrl } = getEnvironmentFromApiKey(context.auth.secret_text), body = {};
    typeof amount == "number" && (body.amount = amount), refund_reference && (body.reference = refund_reference), capture_action_id && (body.capture_action_id = capture_action_id), amount_allocations && amount_allocations.length > 0 && (body.amount_allocations = amount_allocations.map((allocation) => {
      let allocationObj = {
        id: allocation.id,
        amount: allocation.amount
      };
      return allocation.reference && (allocationObj.reference = allocation.reference), (allocation.commission_amount || allocation.commission_percentage) && (allocationObj.commission = {}, allocation.commission_amount && (allocationObj.commission.amount = allocation.commission_amount), allocation.commission_percentage && (allocationObj.commission.percentage = allocation.commission_percentage)), allocationObj;
    })), items && items.length > 0 && (body.items = items.map((item) => {
      let itemObj = {};
      return item.type && (itemObj.type = item.type), item.name && (itemObj.name = item.name), typeof item.quantity == "number" && (itemObj.quantity = item.quantity), typeof item.unit_price == "number" && (itemObj.unit_price = item.unit_price), item.reference && (itemObj.reference = item.reference), typeof item.total_amount == "number" && (itemObj.total_amount = item.total_amount), typeof item.tax_rate == "number" && (itemObj.tax_rate = item.tax_rate), typeof item.tax_amount == "number" && (itemObj.tax_amount = item.tax_amount), typeof item.discount_amount == "number" && (itemObj.discount_amount = item.discount_amount), item.url && (itemObj.url = item.url), item.image_url && (itemObj.image_url = item.image_url), itemObj;
    })), destination_country && destination_account_number && destination_bank_code && (body.destination = {
      country: destination_country,
      account_number: destination_account_number,
      bank_code: destination_bank_code
    }, destination_iban && (body.destination.iban = destination_iban), destination_swift_bic && (body.destination.swift_bic = destination_swift_bic), account_holder_first_name && account_holder_last_name && (body.destination.account_holder = {
      first_name: account_holder_first_name,
      last_name: account_holder_last_name
    }, account_holder_type && (body.destination.account_holder.type = account_holder_type), account_holder_company_name && (body.destination.account_holder.company_name = account_holder_company_name), account_holder_email && (body.destination.account_holder.email = account_holder_email), account_holder_phone_country_code && account_holder_phone_number && (body.destination.account_holder.phone = {
      country_code: account_holder_phone_country_code,
      number: account_holder_phone_number
    }), (account_holder_address_line1 || account_holder_city || account_holder_country) && (body.destination.account_holder.billing_address = {}, account_holder_address_line1 && (body.destination.account_holder.billing_address.address_line1 = account_holder_address_line1), account_holder_address_line2 && (body.destination.account_holder.billing_address.address_line2 = account_holder_address_line2), account_holder_city && (body.destination.account_holder.billing_address.city = account_holder_city), account_holder_state && (body.destination.account_holder.billing_address.state = account_holder_state), account_holder_zip && (body.destination.account_holder.billing_address.zip = account_holder_zip), account_holder_country && (body.destination.account_holder.billing_address.country = account_holder_country))), (bank_name || bank_branch || bank_address_line1) && (body.destination.bank = {}, bank_name && (body.destination.bank.name = bank_name), bank_branch && (body.destination.bank.branch = bank_branch), (bank_address_line1 || bank_city || bank_country) && (body.destination.bank.address = {}, bank_address_line1 && (body.destination.bank.address.address_line1 = bank_address_line1), bank_address_line2 && (body.destination.bank.address.address_line2 = bank_address_line2), bank_city && (body.destination.bank.address.city = bank_city), bank_country && (body.destination.bank.address.country = bank_country)))), metadata && Object.keys(metadata).length > 0 && (body.metadata = metadata);
    try {
      return (await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.POST,
        url: `${baseUrl}/payments/${payment_id}/refunds`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body
      })).body;
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 403 ? new Error("Refund not allowed for this payment") : ((_b = error.response) == null ? void 0 : _b.status) === 404 ? new Error("Payment not found") : ((_c = error.response) == null ? void 0 : _c.status) === 422 ? new Error(`Invalid data: ${((_e = (_d = error.response.body) == null ? void 0 : _d.error_codes) == null ? void 0 : _e.join(", ")) || "Please check your input data"}`) : error;
    }
  }
});

// ../ap/packages/pieces/community/checkout/src/lib/actions/get-payment-details.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common7 = require("@activepieces/pieces-common"), getPaymentDetailsAction = (0, import_pieces_framework7.createAction)({
  name: "get_payment_details",
  classification: "READ",
  auth: checkoutComAuth,
  displayName: "Get Payment Details",
  description: "Check transaction amount and status before refunding.",
  audience: "both",
  aiMetadata: { description: "Retrieves the full details of a single Checkout.com payment by its payment ID (resolved from an order reference), including amount and status. Choose it to inspect a transaction, for example to verify it is captured before issuing a refund. Read-only and idempotent.", idempotent: !0 },
  props: {
    reference: import_pieces_framework7.Property.ShortText({
      displayName: "Reference",
      description: "A reference, such as an order ID, that can be used to identify the payment",
      required: !0
    }),
    paymentId: import_pieces_framework7.Property.Dropdown({
      auth: checkoutComAuth,
      displayName: "Payment ID",
      description: "Select the payment to get details for",
      required: !0,
      refreshers: ["reference"],
      options: async ({ auth, reference }) => {
        if (!reference || !auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please enter a reference first"
          };
        try {
          let { baseUrl } = getEnvironmentFromApiKey(auth.secret_text), payments = (await import_pieces_common7.httpClient.sendRequest({
            method: import_pieces_common7.HttpMethod.GET,
            url: `${baseUrl}/payments`,
            queryParams: {
              reference,
              limit: "100"
            },
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          })).body.data || [];
          return payments.length === 0 ? {
            disabled: !0,
            options: [],
            placeholder: "No payments found for this reference"
          } : {
            disabled: !1,
            options: payments.map((payment) => ({
              label: `${payment.id} - ${payment.amount} ${payment.currency} (${payment.status})`,
              value: payment.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading payments"
          };
        }
      }
    })
  },
  async run(context) {
    var _a, _b;
    let { paymentId } = context.propsValue, { baseUrl } = getEnvironmentFromApiKey(context.auth.secret_text);
    if (!paymentId.match(/^(pay|sid)_[a-zA-Z0-9]{26}$/))
      throw new Error('Invalid payment ID format. Must start with "pay_" or "sid_" followed by 26 alphanumeric characters.');
    try {
      return (await import_pieces_common7.httpClient.sendRequest({
        method: import_pieces_common7.HttpMethod.GET,
        url: `${baseUrl}/payments/${paymentId}`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        }
      })).body;
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error("Unauthorized: Invalid API key or insufficient permissions") : ((_b = error.response) == null ? void 0 : _b.status) === 404 ? new Error("Payment not found: The specified payment ID does not exist or is not accessible") : new Error(`Failed to get payment details: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/checkout/src/lib/actions/get-payment-actions.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var import_pieces_common8 = require("@activepieces/pieces-common"), getPaymentActionsAction = (0, import_pieces_framework8.createAction)({
  name: "get_payment_actions",
  classification: "READ",
  auth: checkoutComAuth,
  displayName: "Get Payment Actions",
  description: "Build full transaction lifecycles for audit logs.",
  audience: "both",
  aiMetadata: { description: "Retrieves the list of actions (authorization, capture, refund, void, etc.) that have occurred on a single Checkout.com payment, identified by its payment ID (resolved from an order reference). Choose it to reconstruct a transaction's lifecycle for audit or status tracking. Read-only and idempotent.", idempotent: !0 },
  props: {
    reference: import_pieces_framework8.Property.ShortText({
      displayName: "Reference",
      description: "A reference, such as an order ID, that can be used to identify the payment",
      required: !0
    }),
    paymentId: import_pieces_framework8.Property.Dropdown({
      auth: checkoutComAuth,
      displayName: "Payment ID",
      description: "Select the payment to get actions for",
      required: !0,
      refreshers: ["reference"],
      options: async ({ auth, reference }) => {
        if (!reference || !auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please enter a reference first"
          };
        try {
          let { baseUrl } = getEnvironmentFromApiKey(auth.secret_text), validPayments = ((await import_pieces_common8.httpClient.sendRequest({
            method: import_pieces_common8.HttpMethod.GET,
            url: `${baseUrl}/payments`,
            queryParams: {
              reference,
              limit: "100"
            },
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          })).body.data || []).filter((payment) => payment.id.startsWith("pay_"));
          return validPayments.length === 0 ? {
            disabled: !0,
            options: [],
            placeholder: "No payment transactions found for this reference"
          } : {
            disabled: !1,
            options: validPayments.map((payment) => ({
              label: `${payment.id} - ${payment.amount} ${payment.currency} (${payment.status})`,
              value: payment.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Error loading payments"
          };
        }
      }
    })
  },
  async run(context) {
    var _a, _b;
    let { paymentId } = context.propsValue, { baseUrl } = getEnvironmentFromApiKey(context.auth.secret_text);
    if (!paymentId.match(/^pay_[a-zA-Z0-9]{26}$/))
      throw new Error('Invalid payment ID format. Must start with "pay_" followed by 26 alphanumeric characters.');
    try {
      return (await import_pieces_common8.httpClient.sendRequest({
        method: import_pieces_common8.HttpMethod.GET,
        url: `${baseUrl}/payments/${paymentId}/actions`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        }
      })).body;
    } catch (error) {
      throw ((_a = error.response) == null ? void 0 : _a.status) === 401 ? new Error("Unauthorized: Invalid API key or insufficient permissions") : ((_b = error.response) == null ? void 0 : _b.status) === 404 ? new Error("Payment not found: The specified payment ID does not exist or is not accessible") : new Error(`Failed to get payment actions: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/checkout/src/lib/triggers/payment-events.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var import_pieces_common9 = require("@activepieces/pieces-common"), paymentEventsTrigger = (0, import_pieces_framework9.createTrigger)({
  name: "payment_events",
  classification: "READ",
  displayName: "Payment Events",
  description: "Trigger order fulfillment when payment is approved.",
  aiMetadata: {
    description: "Fires when a Checkout.com payment-gateway event occurs for one of the event types you subscribe to (such as payment approved, declined, captured, refunded, or voided, and card-verification events). Use it to react to payment lifecycle changes, for example to start order fulfillment once a payment is approved."
  },
  auth: checkoutComAuth,
  props: {
    eventTypes: import_pieces_framework9.Property.MultiSelectDropdown({
      auth: checkoutComAuth,
      displayName: "Event Types",
      description: "Select the payment events you want to listen for",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your Checkout.com account first"
          };
        try {
          let { baseUrl } = getEnvironmentFromApiKey(auth.secret_text), gatewaySource = ((await import_pieces_common9.httpClient.sendRequest({
            method: import_pieces_common9.HttpMethod.GET,
            url: `${baseUrl}/workflows/event-types`,
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          })).body || []).find((source) => source.id === "gateway");
          return !gatewaySource || !gatewaySource.events ? {
            disabled: !1,
            options: [
              { label: "Payment Approved", value: "payment_approved" },
              { label: "Payment Declined", value: "payment_declined" },
              { label: "Payment Captured", value: "payment_captured" },
              { label: "Payment Refunded", value: "payment_refunded" },
              { label: "Payment Voided", value: "payment_voided" },
              { label: "Card Verified", value: "card_verified" },
              { label: "Card Verification Declined", value: "card_verification_declined" },
              { label: "Payment Authorization Incremented", value: "payment_authorization_incremented" },
              { label: "Payment Authorization Increment Declined", value: "payment_authorization_increment_declined" },
              { label: "Payment Capture Declined", value: "payment_capture_declined" },
              { label: "Payment Refund Declined", value: "payment_refund_declined" },
              { label: "Payment Void Declined", value: "payment_void_declined" }
            ]
          } : {
            disabled: !1,
            options: gatewaySource.events.map((event) => ({
              label: event.display_name || event.id,
              value: event.id
            }))
          };
        } catch (error) {
          return {
            disabled: !1,
            options: [
              { label: "Payment Approved", value: "payment_approved" },
              { label: "Payment Declined", value: "payment_declined" },
              { label: "Payment Captured", value: "payment_captured" },
              { label: "Payment Refunded", value: "payment_refunded" },
              { label: "Payment Voided", value: "payment_voided" },
              { label: "Card Verified", value: "card_verified" },
              { label: "Card Verification Declined", value: "card_verification_declined" }
            ]
          };
        }
      }
    })
  },
  sampleData: {
    id: "evt_az5sblvku4ge3dwpztvyizgcau",
    source: "gateway",
    type: "payment_approved",
    timestamp: "2019-08-24T14:15:22Z",
    version: "1.0.0",
    data: {
      id: "pay_mbabizu24mvu3mela5njyhpit4",
      action_id: "act_y3oqhf46pyzuxjbcn2giaqnb44",
      amount: 6540,
      currency: "USD",
      approved: !0,
      status: "Authorized",
      auth_code: "643381",
      payment_type: "Regular",
      response_code: "10000",
      response_summary: "Approved",
      reference: "ORD-5023-4E89",
      processed_on: "2020-02-27T11:26:59Z"
    }
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { eventTypes } = context.propsValue, { baseUrl } = getEnvironmentFromApiKey(context.auth.secret_text);
    try {
      let response = await import_pieces_common9.httpClient.sendRequest({
        method: import_pieces_common9.HttpMethod.POST,
        url: `${baseUrl}/workflows`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body: {
          name: "Activepieces Payment Events Workflow",
          active: !0,
          conditions: [
            {
              type: "event",
              events: {
                gateway: eventTypes
              }
            }
          ],
          actions: [
            {
              type: "webhook",
              url: context.webhookUrl
            }
          ]
        }
      });
      await context.store.put("checkout_payment_workflow", {
        workflowId: response.body.id
      });
    } catch (error) {
      throw new Error(`Failed to create payment events workflow: ${error.message}`);
    }
  },
  async onDisable(context) {
    try {
      let workflowData = await context.store.get("checkout_payment_workflow");
      if (workflowData != null && workflowData.workflowId) {
        let { baseUrl } = getEnvironmentFromApiKey(context.auth.secret_text);
        await import_pieces_common9.httpClient.sendRequest({
          method: import_pieces_common9.HttpMethod.DELETE,
          url: `${baseUrl}/workflows/${workflowData.workflowId}`,
          headers: {
            Authorization: `Bearer ${context.auth.secret_text}`
          }
        });
      }
    } catch (error) {
      console.error("Failed to delete payment events workflow:", error.message);
    }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/checkout/src/lib/triggers/dispute-events.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var import_pieces_common10 = require("@activepieces/pieces-common"), disputeEventsTrigger = (0, import_pieces_framework10.createTrigger)({
  name: "dispute_events",
  classification: "READ",
  displayName: "Dispute Events",
  description: "Notify operations upon dispute opening or resolution.",
  aiMetadata: {
    description: "Fires when a Checkout.com dispute (chargeback) event occurs for one of the event types you subscribe to, such as evidence required, canceled, expired, resolved, lost, or won. Use it to react to dispute lifecycle changes, for example to alert operations when a dispute opens or is resolved."
  },
  auth: checkoutComAuth,
  props: {
    eventTypes: import_pieces_framework10.Property.MultiSelectDropdown({
      auth: checkoutComAuth,
      displayName: "Event Types",
      description: "Select the dispute events you want to listen for",
      required: !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please connect your Checkout.com account first"
          };
        try {
          let { baseUrl } = getEnvironmentFromApiKey(auth.secret_text), disputeSource = ((await import_pieces_common10.httpClient.sendRequest({
            method: import_pieces_common10.HttpMethod.GET,
            url: `${baseUrl}/workflows/event-types`,
            headers: {
              Authorization: `Bearer ${auth.secret_text}`,
              "Content-Type": "application/json"
            }
          })).body || []).find((source) => source.id === "dispute" || source.id === "disputes");
          return !disputeSource || !disputeSource.events ? {
            disabled: !1,
            options: [
              { label: "Dispute Canceled", value: "dispute_canceled" },
              { label: "Dispute Evidence Required", value: "dispute_evidence_required" },
              { label: "Dispute Expired", value: "dispute_expired" },
              { label: "Dispute Lost", value: "dispute_lost" },
              { label: "Dispute Resolved", value: "dispute_resolved" },
              { label: "Dispute Won", value: "dispute_won" }
            ]
          } : {
            disabled: !1,
            options: disputeSource.events.map((event) => ({
              label: event.display_name || event.id,
              value: event.id
            }))
          };
        } catch (error) {
          return {
            disabled: !1,
            options: [
              { label: "Dispute Canceled", value: "dispute_canceled" },
              { label: "Dispute Evidence Required", value: "dispute_evidence_required" },
              { label: "Dispute Expired", value: "dispute_expired" },
              { label: "Dispute Lost", value: "dispute_lost" },
              { label: "Dispute Resolved", value: "dispute_resolved" },
              { label: "Dispute Won", value: "dispute_won" }
            ]
          };
        }
      }
    })
  },
  sampleData: {
    id: "evt_lw4cjxhp4jme3dwpztvyizgcau",
    source: "dispute",
    type: "dispute_evidence_required",
    timestamp: "2019-08-24T14:15:22Z",
    version: "1.0.0",
    data: {
      id: "dsp_rbhwd2qrg13uhr4w2oxna5tav6",
      category: "fraudulent",
      amount: 6540,
      currency: "USD",
      reason_code: "4855",
      status: "evidence_required",
      resolved_reason: "dispute_not_valid",
      payment_id: "pay_mbabizu24mvu3mela5njyhpit4",
      payment_reference: "ORD-5023-4E89",
      payment_arn: "74593210001234567890123",
      payment_method: "VISA",
      received_on: "2019-08-24T14:15:22Z",
      last_update: "2019-08-24T14:15:22Z"
    }
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { eventTypes } = context.propsValue, { baseUrl } = getEnvironmentFromApiKey(context.auth.secret_text);
    try {
      let response = await import_pieces_common10.httpClient.sendRequest({
        method: import_pieces_common10.HttpMethod.POST,
        url: `${baseUrl}/workflows`,
        headers: {
          Authorization: `Bearer ${context.auth.secret_text}`,
          "Content-Type": "application/json"
        },
        body: {
          name: "Activepieces Dispute Events Workflow",
          active: !0,
          conditions: [
            {
              type: "event",
              events: {
                dispute: eventTypes
              }
            }
          ],
          actions: [
            {
              type: "webhook",
              url: context.webhookUrl
            }
          ]
        }
      });
      await context.store.put("checkout_dispute_workflow", {
        workflowId: response.body.id
      });
    } catch (error) {
      throw new Error(`Failed to create dispute events workflow: ${error.message}`);
    }
  },
  async onDisable(context) {
    try {
      let workflowData = await context.store.get("checkout_dispute_workflow");
      if (workflowData != null && workflowData.workflowId) {
        let { baseUrl } = getEnvironmentFromApiKey(context.auth.secret_text);
        await import_pieces_common10.httpClient.sendRequest({
          method: import_pieces_common10.HttpMethod.DELETE,
          url: `${baseUrl}/workflows/${workflowData.workflowId}`,
          headers: {
            Authorization: `Bearer ${context.auth.secret_text}`
          }
        });
      }
    } catch (error) {
      console.error("Failed to delete dispute events workflow:", error.message);
    }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/checkout/src/index.ts
var checkout = (0, import_pieces_framework11.createPiece)({
  displayName: "Checkout.com",
  auth: checkoutComAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/checkout.png",
  authors: ["sparkybug", "onyedikachi-david"],
  actions: [
    createCustomerAction,
    updateCustomerAction,
    createPaymentLinkAction,
    createPayoutAction,
    refundPaymentAction,
    getPaymentDetailsAction,
    getPaymentActionsAction
  ],
  triggers: [paymentEventsTrigger, disputeEventsTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  checkout
});
