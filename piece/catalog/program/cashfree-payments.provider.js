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

// ../ap/packages/pieces/community/cashfree-payments/src/index.ts
var index_exports = {};
__export(index_exports, {
  cashfreeTriggers: () => cashfreeTriggers
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/cashfree-payments/src/lib/actions/create-order.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/cashfree-payments/src/lib/auth/cashgram-auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
async function generateCashgramToken(credentials, environment) {
  var _a, _b;
  try {
    let baseUrl = environment === "production" ? "https://payout-api.cashfree.com/payout/v1/authorize" : "https://payout-gamma.cashfree.com/payout/v1/authorize", headers = {
      "x-client-id": credentials.clientId,
      "x-client-secret": credentials.clientSecret,
      "Content-Type": "application/json"
    }, response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: baseUrl,
      headers,
      body: {}
    });
    return response.status === 200 && ((_b = (_a = response.body) == null ? void 0 : _a.data) != null && _b.token) ? {
      success: !0,
      token: response.body.data.token,
      message: "Bearer token generated successfully"
    } : {
      success: !1,
      error: response.body,
      message: `Failed to generate bearer token. Status: ${response.status}. Response: ${JSON.stringify(response.body)}`
    };
  } catch (error) {
    return console.error("Error generating Cashgram token:", error), {
      success: !1,
      error,
      message: "An error occurred while generating the bearer token"
    };
  }
}
function validateAuthCredentials(authType, credentials) {
  if (authType === "client_credentials") {
    if (!credentials.clientId || credentials.clientId.trim().length === 0)
      return {
        isValid: !1,
        error: "Client ID is required for Client Credentials authentication"
      };
    if (!credentials.clientSecret || credentials.clientSecret.trim().length === 0)
      return {
        isValid: !1,
        error: "Client Secret is required for Client Credentials authentication"
      };
  } else if (authType === "client_credentials_with_public_key") {
    if (!credentials.clientId || credentials.clientId.trim().length === 0)
      return {
        isValid: !1,
        error: "Client ID is required for Client Credentials + Public Key authentication"
      };
    if (!credentials.clientSecret || credentials.clientSecret.trim().length === 0)
      return {
        isValid: !1,
        error: "Client Secret is required for Client Credentials + Public Key authentication"
      };
  }
  return {
    isValid: !0
  };
}
var cashfreePaymentsAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `Connect your Cashfree account

This connector requires Cashfree API credentials (Client ID and Client Secret). Important: each Cashfree product is a separate product and requires its own credentials. For example, the Payments API and the Payouts API each need their own Client ID / Client Secret pairs.

Create two connections (recommended)
- For clarity and security we recommend creating two separate Activepieces connections:
  1. **Payments connection** \u2014 use the Payments API Client ID / Client Secret. Use this connection for payments-related actions (create order, payment links, refunds, etc.).
  2. **Payouts connection** \u2014 use the Payouts API Client ID / Client Secret. Use this connection for Cashgram and other payouts-related actions.

Which keys to use
- Payments API: use the credentials generated for the Payments product.
- Payouts API (required by Cashgram actions): use credentials generated from the Payouts dashboard.

How to generate API keys:
1. Sign in to your Cashfree account and open the *Payouts* dashboard.
2. In the navigation panel select **Developers**.
3. Click **API Keys**.
4. Click **Generate API Keys** on the API Keys screen.
5. The **New API Keys** popup displays the Client ID and Client Secret.
6. Click **Download API Keys** to save the keys locally. Keep these secret \u2014 do not share them.

`,
  props: {
    clientId: import_pieces_framework.Property.ShortText({
      displayName: "Cashfree Client ID",
      description: "Your Cashfree Client ID",
      required: !1
    }),
    clientSecret: import_pieces_framework.Property.ShortText({
      displayName: "Cashfree Client Secret",
      description: "Your Cashfree Client Secret",
      required: !1
    })
  },
  required: !0
});

// ../ap/packages/pieces/community/cashfree-payments/src/lib/actions/create-order.ts
var createOrder = (0, import_pieces_framework2.createAction)({
  name: "create-order",
  classification: "WRITE",
  displayName: "Create Order",
  description: "Creates an order in Cashfree Payment Gateway",
  audience: "both",
  aiMetadata: {
    description: "Creates a payment order in the Cashfree Payment Gateway (PG) given an amount, currency, and customer details, returning the order and its session for collecting payment. Use this to start a checkout/payment flow. Targets sandbox or production via the Environment prop; if no Order ID is supplied a unique one is auto-generated, so each call creates a distinct order and is not idempotent.",
    idempotent: !1
  },
  auth: cashfreePaymentsAuth,
  props: {
    environment: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Environment",
      description: "Choose the environment for API calls",
      required: !0,
      defaultValue: "sandbox",
      options: {
        disabled: !1,
        options: [
          {
            label: "Sandbox",
            value: "sandbox"
          },
          {
            label: "Production",
            value: "production"
          }
        ]
      }
    }),
    // Required Fields
    orderAmount: import_pieces_framework2.Property.Number({
      displayName: "Order Amount",
      description: "Bill amount for the order. Provide up to two decimals (e.g., 10.15 means Rs 10 and 15 paisa)",
      required: !0
    }),
    orderCurrency: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Order Currency",
      description: "Currency for the order",
      required: !0,
      defaultValue: "INR",
      options: {
        disabled: !1,
        options: [
          { label: "Indian Rupee (INR)", value: "INR" },
          { label: "US Dollar (USD)", value: "USD" },
          { label: "Euro (EUR)", value: "EUR" },
          { label: "British Pound (GBP)", value: "GBP" }
        ]
      }
    }),
    customerId: import_pieces_framework2.Property.ShortText({
      displayName: "Customer ID",
      description: "A unique identifier for the customer. Use alphanumeric values only (3-50 characters)",
      required: !0
    }),
    customerPhone: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Phone",
      description: "Customer phone number (minimum 10 digits). For international numbers, prefix with +",
      required: !0
    }),
    // Optional Customer Details
    customerEmail: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Email",
      description: "Customer email address (3-100 characters)",
      required: !1
    }),
    customerName: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Name",
      description: "Name of the customer (3-100 characters)",
      required: !1
    }),
    customerBankAccountNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Bank Account Number",
      description: "Customer bank account. Required for TPV (Third Party Verification) (3-20 characters)",
      required: !1
    }),
    customerBankIfsc: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Bank IFSC",
      description: "Customer bank IFSC. Required for TPV (Third Party Verification)",
      required: !1
    }),
    customerBankCode: import_pieces_framework2.Property.Number({
      displayName: "Customer Bank Code",
      description: "Customer bank code. Required for net banking payments with TPV",
      required: !1
    }),
    // Order Details
    orderId: import_pieces_framework2.Property.ShortText({
      displayName: "Order ID",
      description: 'Order identifier in your system. Alphanumeric, "_" and "-" only (3-45 characters). Will be auto-generated if not provided',
      required: !1
    }),
    orderNote: import_pieces_framework2.Property.ShortText({
      displayName: "Order Note",
      description: "Order note for reference (3-200 characters)",
      required: !1
    }),
    orderExpiryTime: import_pieces_framework2.Property.ShortText({
      displayName: "Order Expiry Time",
      description: "ISO 8601 format. Example: 2021-07-02T10:20:12+05:30",
      required: !1
    }),
    // Order Meta
    returnUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Return URL",
      description: "URL to redirect customer after payment completion (max 250 characters)",
      required: !1
    }),
    notifyUrl: import_pieces_framework2.Property.ShortText({
      displayName: "Notify URL",
      description: "HTTPS URL for server-to-server notifications (max 250 characters)",
      required: !1
    }),
    paymentMethods: import_pieces_framework2.Property.ShortText({
      displayName: "Payment Methods",
      description: "Comma-separated values: cc,dc,ccc,ppc,nb,upi,paypal,app,paylater,cardlessemi,dcemi,ccemi,banktransfer",
      required: !1
    }),
    // Cart Details
    cartName: import_pieces_framework2.Property.ShortText({
      displayName: "Cart Name",
      description: "Name of the cart",
      required: !1
    }),
    customerNote: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Note",
      description: "Note from customer",
      required: !1
    }),
    shippingCharge: import_pieces_framework2.Property.Number({
      displayName: "Shipping Charge",
      description: "Shipping charges for the order",
      required: !1
    }),
    // Shipping Address
    shippingFullName: import_pieces_framework2.Property.ShortText({
      displayName: "Shipping - Full Name",
      description: "Full name for shipping address",
      required: !1
    }),
    shippingCountry: import_pieces_framework2.Property.ShortText({
      displayName: "Shipping - Country",
      description: "Country for shipping address",
      required: !1
    }),
    shippingCity: import_pieces_framework2.Property.ShortText({
      displayName: "Shipping - City",
      description: "City for shipping address",
      required: !1
    }),
    shippingState: import_pieces_framework2.Property.ShortText({
      displayName: "Shipping - State",
      description: "State for shipping address",
      required: !1
    }),
    shippingPincode: import_pieces_framework2.Property.ShortText({
      displayName: "Shipping - Pincode",
      description: "Pincode for shipping address",
      required: !1
    }),
    shippingAddress1: import_pieces_framework2.Property.ShortText({
      displayName: "Shipping - Address Line 1",
      description: "Primary address line for shipping",
      required: !1
    }),
    shippingAddress2: import_pieces_framework2.Property.ShortText({
      displayName: "Shipping - Address Line 2",
      description: "Secondary address line for shipping",
      required: !1
    }),
    // Billing Address
    billingFullName: import_pieces_framework2.Property.ShortText({
      displayName: "Billing - Full Name",
      description: "Full name for billing address",
      required: !1
    }),
    billingCountry: import_pieces_framework2.Property.ShortText({
      displayName: "Billing - Country",
      description: "Country for billing address",
      required: !1
    }),
    billingCity: import_pieces_framework2.Property.ShortText({
      displayName: "Billing - City",
      description: "City for billing address",
      required: !1
    }),
    billingState: import_pieces_framework2.Property.ShortText({
      displayName: "Billing - State",
      description: "State for billing address",
      required: !1
    }),
    billingPincode: import_pieces_framework2.Property.ShortText({
      displayName: "Billing - Pincode",
      description: "Pincode for billing address",
      required: !1
    }),
    billingAddress1: import_pieces_framework2.Property.ShortText({
      displayName: "Billing - Address Line 1",
      description: "Primary address line for billing",
      required: !1
    }),
    billingAddress2: import_pieces_framework2.Property.ShortText({
      displayName: "Billing - Address Line 2",
      description: "Secondary address line for billing",
      required: !1
    }),
    // Terminal (for SoftPOS)
    terminalType: import_pieces_framework2.Property.ShortText({
      displayName: "Terminal Type",
      description: "Type of terminal (e.g., SPOS) for SoftPOS orders (4-10 characters)",
      required: !1
    }),
    terminalId: import_pieces_framework2.Property.ShortText({
      displayName: "Terminal ID",
      description: "Terminal ID for merchant reference (3-100 characters)",
      required: !1
    }),
    terminalPhoneNo: import_pieces_framework2.Property.ShortText({
      displayName: "Terminal Phone Number",
      description: "Mobile number of the terminal/agent/storefront",
      required: !1
    }),
    terminalName: import_pieces_framework2.Property.ShortText({
      displayName: "Terminal Name",
      description: "Name of terminal/agent/storefront",
      required: !1
    }),
    terminalAddress: import_pieces_framework2.Property.ShortText({
      displayName: "Terminal Address",
      description: "Location of terminal",
      required: !1
    }),
    terminalNote: import_pieces_framework2.Property.ShortText({
      displayName: "Terminal Note",
      description: "Note given by merchant while creating the terminal",
      required: !1
    }),
    // Products Configuration
    oneClickCheckoutEnabled: import_pieces_framework2.Property.Checkbox({
      displayName: "Enable One Click Checkout",
      description: "Enable One Click Checkout feature",
      required: !1
    }),
    verifyPayEnabled: import_pieces_framework2.Property.Checkbox({
      displayName: "Enable Verify and Pay",
      description: "Enable Verify and Pay feature",
      required: !1
    }),
    // Order Tags (JSON string)
    orderTags: import_pieces_framework2.Property.LongText({
      displayName: "Order Tags",
      description: 'Custom tags as JSON object. Example: {"product":"Laptop","city":"Bangalore"}. Maximum 10 tags',
      required: !1
    })
  },
  async run(context) {
    let { clientId, clientSecret } = context.auth.props;
    if (!clientId || !clientSecret)
      return {
        success: !1,
        error: "Client ID and Client Secret are required",
        message: "Please provide both Client ID and Client Secret for authentication"
      };
    let {
      environment,
      orderAmount,
      orderCurrency,
      customerId,
      customerPhone,
      customerEmail,
      customerName,
      customerBankAccountNumber,
      customerBankIfsc,
      customerBankCode,
      orderId,
      orderNote,
      orderExpiryTime,
      returnUrl,
      notifyUrl,
      paymentMethods,
      cartName,
      customerNote,
      shippingCharge,
      shippingFullName,
      shippingCountry,
      shippingCity,
      shippingState,
      shippingPincode,
      shippingAddress1,
      shippingAddress2,
      billingFullName,
      billingCountry,
      billingCity,
      billingState,
      billingPincode,
      billingAddress1,
      billingAddress2,
      terminalType,
      terminalId,
      terminalPhoneNo,
      terminalName,
      terminalAddress,
      terminalNote,
      oneClickCheckoutEnabled,
      verifyPayEnabled,
      orderTags
    } = context.propsValue, baseUrl = environment === "production" ? "https://api.cashfree.com/pg/orders" : "https://sandbox.cashfree.com/pg/orders", generatedOrderId = orderId || `order_${Date.now()}_${Math.random().toString(36).substr(2, 9)}`, customerDetails = {
      customer_id: customerId,
      customer_phone: customerPhone
    };
    customerEmail && (customerDetails.customer_email = customerEmail), customerName && (customerDetails.customer_name = customerName), customerBankAccountNumber && (customerDetails.customer_bank_account_number = customerBankAccountNumber), customerBankIfsc && (customerDetails.customer_bank_ifsc = customerBankIfsc), customerBankCode && (customerDetails.customer_bank_code = customerBankCode);
    let orderMeta = {};
    returnUrl && (orderMeta.return_url = returnUrl), notifyUrl && (orderMeta.notify_url = notifyUrl), paymentMethods && (orderMeta.payment_methods = paymentMethods);
    let cartDetails = {};
    cartName && (cartDetails.cart_name = cartName), customerNote && (cartDetails.customer_note = customerNote), shippingCharge && (cartDetails.shipping_charge = shippingCharge), (shippingFullName || shippingCountry || shippingCity || shippingState || shippingPincode || shippingAddress1 || shippingAddress2) && (cartDetails.customer_shipping_address = {}, shippingFullName && (cartDetails.customer_shipping_address.full_name = shippingFullName), shippingCountry && (cartDetails.customer_shipping_address.country = shippingCountry), shippingCity && (cartDetails.customer_shipping_address.city = shippingCity), shippingState && (cartDetails.customer_shipping_address.state = shippingState), shippingPincode && (cartDetails.customer_shipping_address.pincode = shippingPincode), shippingAddress1 && (cartDetails.customer_shipping_address.address_1 = shippingAddress1), shippingAddress2 && (cartDetails.customer_shipping_address.address_2 = shippingAddress2)), (billingFullName || billingCountry || billingCity || billingState || billingPincode || billingAddress1 || billingAddress2) && (cartDetails.customer_billing_address = {}, billingFullName && (cartDetails.customer_billing_address.full_name = billingFullName), billingCountry && (cartDetails.customer_billing_address.country = billingCountry), billingCity && (cartDetails.customer_billing_address.city = billingCity), billingState && (cartDetails.customer_billing_address.state = billingState), billingPincode && (cartDetails.customer_billing_address.pincode = billingPincode), billingAddress1 && (cartDetails.customer_billing_address.address_1 = billingAddress1), billingAddress2 && (cartDetails.customer_billing_address.address_2 = billingAddress2));
    let terminal = {};
    terminalType && (terminal.terminal_type = terminalType, terminalId && (terminal.terminal_id = terminalId), terminalPhoneNo && (terminal.terminal_phone_no = terminalPhoneNo), terminalName && (terminal.terminal_name = terminalName), terminalAddress && (terminal.terminal_address = terminalAddress), terminalNote && (terminal.terminal_note = terminalNote));
    let products = {};
    oneClickCheckoutEnabled !== void 0 && (products.one_click_checkout = { enabled: oneClickCheckoutEnabled }), verifyPayEnabled !== void 0 && (products.verify_pay = { enabled: verifyPayEnabled });
    let parsedOrderTags;
    if (orderTags)
      try {
        parsedOrderTags = JSON.parse(orderTags);
      } catch (error) {
        return {
          success: !1,
          error: "Invalid JSON format for order tags",
          message: 'Order tags must be valid JSON format. Example: {"product":"Laptop","city":"Bangalore"}'
        };
      }
    let requestBody = {
      order_amount: orderAmount,
      order_currency: orderCurrency,
      order_id: generatedOrderId,
      customer_details: customerDetails
    };
    Object.keys(orderMeta).length > 0 && (requestBody.order_meta = orderMeta), Object.keys(cartDetails).length > 0 && (requestBody.cart_details = cartDetails), Object.keys(terminal).length > 0 && (requestBody.terminal = terminal), Object.keys(products).length > 0 && (requestBody.products = products), orderNote && (requestBody.order_note = orderNote), orderExpiryTime && (requestBody.order_expiry_time = orderExpiryTime), parsedOrderTags && (requestBody.order_tags = parsedOrderTags);
    let headers = {
      "x-api-version": "2025-01-01",
      "Content-Type": "application/json",
      Accept: "application/json",
      "X-Client-Id": clientId,
      "X-Client-Secret": clientSecret
    };
    try {
      let response = await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.POST,
        url: baseUrl,
        headers,
        body: requestBody
      });
      return response.status === 200 || response.status === 201 ? {
        success: !0,
        data: response.body,
        message: "Order created successfully"
      } : {
        success: !1,
        error: response.body,
        message: "Failed to create order",
        status: response.status
      };
    } catch (error) {
      return console.error("Error creating Cashfree order:", error), {
        success: !1,
        error,
        message: "An error occurred while creating the order"
      };
    }
  }
});

// ../ap/packages/pieces/community/cashfree-payments/src/lib/actions/create-payment-link.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createPaymentLink = (0, import_pieces_framework3.createAction)({
  name: "create-payment-link",
  classification: "WRITE",
  displayName: "Create Payment Link",
  description: "Creates a payment link in Cashfree Payment Gateway",
  audience: "both",
  aiMetadata: {
    description: "Creates a shareable Cashfree payment link for a given amount, currency, and purpose that a customer can pay through, optionally sending it via SMS/email and supporting partial payments. Use this to collect payment without building a full checkout. Targets sandbox or production via the Environment prop; if no Link ID is supplied a unique one is auto-generated, so each call creates a new link and is not idempotent.",
    idempotent: !1
  },
  auth: cashfreePaymentsAuth,
  props: {
    environment: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Environment",
      description: "Choose the environment for API calls",
      required: !0,
      defaultValue: "sandbox",
      options: {
        disabled: !1,
        options: [
          {
            label: "Sandbox",
            value: "sandbox"
          },
          {
            label: "Production",
            value: "production"
          }
        ]
      }
    }),
    // Required Fields
    linkAmount: import_pieces_framework3.Property.Number({
      displayName: "Link Amount",
      description: "Amount to be collected using this link. Provide up to two decimals for paise",
      required: !0
    }),
    linkCurrency: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Link Currency",
      description: "Currency for the payment link",
      required: !0,
      defaultValue: "INR",
      options: {
        disabled: !1,
        options: [
          { label: "Indian Rupee (INR)", value: "INR" },
          { label: "US Dollar (USD)", value: "USD" },
          { label: "Euro (EUR)", value: "EUR" },
          { label: "British Pound (GBP)", value: "GBP" }
        ]
      }
    }),
    linkPurpose: import_pieces_framework3.Property.ShortText({
      displayName: "Link Purpose",
      description: "A brief description for which payment must be collected (max 500 characters)",
      required: !0
    }),
    customerPhone: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Phone",
      description: "Customer phone number (required)",
      required: !0
    }),
    // Optional Customer Details
    customerEmail: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Email",
      description: "Customer email address",
      required: !1
    }),
    customerName: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Name",
      description: "Customer name",
      required: !1
    }),
    customerBankAccountNumber: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Bank Account Number",
      description: "Customer bank account number",
      required: !1
    }),
    customerBankIfsc: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Bank IFSC",
      description: "Customer bank IFSC code",
      required: !1
    }),
    customerBankCode: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Customer Bank Code",
      description: "Customer bank code for net banking",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "State Bank of India (3003)", value: 3003 },
          { label: "HDFC Bank (3005)", value: 3005 },
          { label: "ICICI Bank (3006)", value: 3006 },
          { label: "Axis Bank (3010)", value: 3010 },
          { label: "Punjab National Bank (3012)", value: 3012 },
          { label: "Bank of Baroda (3016)", value: 3016 },
          { label: "Canara Bank (3019)", value: 3019 },
          { label: "Union Bank of India (3020)", value: 3020 },
          { label: "Bank of India (3021)", value: 3021 },
          { label: "Central Bank of India (3022)", value: 3022 },
          { label: "Indian Bank (3023)", value: 3023 },
          { label: "Indian Overseas Bank (3024)", value: 3024 },
          { label: "UCO Bank (3026)", value: 3026 },
          { label: "Bank of Maharashtra (3027)", value: 3027 },
          { label: "Punjab & Sind Bank (3028)", value: 3028 },
          { label: "IDBI Bank (3029)", value: 3029 },
          { label: "Federal Bank (3030)", value: 3030 },
          { label: "South Indian Bank (3031)", value: 3031 },
          { label: "IndusInd Bank (3032)", value: 3032 },
          { label: "YES Bank (3033)", value: 3033 },
          { label: "Kotak Mahindra Bank (7001)", value: 7001 }
        ]
      }
    }),
    // Link Configuration
    linkId: import_pieces_framework3.Property.ShortText({
      displayName: "Link ID",
      description: 'Unique identifier for the link. Alphanumeric, "-" and "_" only (max 50 characters). Auto-generated if not provided',
      required: !1
    }),
    linkPartialPayments: import_pieces_framework3.Property.Checkbox({
      displayName: "Enable Partial Payments",
      description: "Allow customers to make partial payments for the link",
      required: !1
    }),
    linkMinimumPartialAmount: import_pieces_framework3.Property.Number({
      displayName: "Minimum Partial Amount",
      description: "Minimum amount in first installment (required if partial payments enabled)",
      required: !1
    }),
    linkExpiryTime: import_pieces_framework3.Property.ShortText({
      displayName: "Link Expiry Time",
      description: "ISO 8601 format. Example: 2021-07-02T10:20:12+05:30. Default is 30 days",
      required: !1
    }),
    // Notification Settings
    sendSms: import_pieces_framework3.Property.Checkbox({
      displayName: "Send SMS Notification",
      description: "Send SMS notification to customer phone",
      required: !1
    }),
    sendEmail: import_pieces_framework3.Property.Checkbox({
      displayName: "Send Email Notification",
      description: "Send email notification to customer email",
      required: !1
    }),
    linkAutoReminders: import_pieces_framework3.Property.Checkbox({
      displayName: "Auto Reminders",
      description: "Send automatic reminders to customers for payment collection",
      required: !1
    }),
    // Link Meta
    notifyUrl: import_pieces_framework3.Property.ShortText({
      displayName: "Notify URL",
      description: "HTTPS URL for server-to-server notifications",
      required: !1
    }),
    returnUrl: import_pieces_framework3.Property.ShortText({
      displayName: "Return URL",
      description: "URL to redirect user after payment completion (max 250 characters)",
      required: !1
    }),
    upiIntent: import_pieces_framework3.Property.Checkbox({
      displayName: "UPI Intent",
      description: "Directly open UPI Intent flow on mobile devices",
      required: !1
    }),
    paymentMethods: import_pieces_framework3.Property.ShortText({
      displayName: "Payment Methods",
      description: "Comma-separated values: cc,dc,ccc,ppc,nb,upi,paypal,app. Leave blank for all methods",
      required: !1
    }),
    // Additional Data
    linkNotes: import_pieces_framework3.Property.LongText({
      displayName: "Link Notes",
      description: 'Key-value pairs as JSON. Maximum 5 key-value pairs. Example: {"key_1":"value_1","key_2":"value_2"}',
      required: !1
    }),
    // Order Splits
    orderSplits: import_pieces_framework3.Property.LongText({
      displayName: "Order Splits",
      description: 'JSON array for Easy Split. Example: [{"vendor_id":"vendor1","amount":100}]',
      required: !1
    })
  },
  async run(context) {
    let { clientId, clientSecret } = context.auth.props;
    if (!clientId || !clientSecret)
      return {
        success: !1,
        error: "Client ID and Client Secret are required",
        message: "Please provide both Client ID and Client Secret for authentication"
      };
    let {
      environment,
      linkAmount,
      linkCurrency,
      linkPurpose,
      customerPhone,
      customerEmail,
      customerName,
      customerBankAccountNumber,
      customerBankIfsc,
      customerBankCode,
      linkId,
      linkPartialPayments,
      linkMinimumPartialAmount,
      linkExpiryTime,
      sendSms,
      sendEmail,
      linkAutoReminders,
      notifyUrl,
      returnUrl,
      upiIntent,
      paymentMethods,
      linkNotes,
      orderSplits
    } = context.propsValue, baseUrl = environment === "production" ? "https://api.cashfree.com/pg/links" : "https://sandbox.cashfree.com/pg/links", generatedLinkId = linkId || `link_${Date.now()}_${Math.random().toString(36).substr(2, 9)}`, customerDetails = {
      customer_phone: customerPhone
    };
    customerEmail && (customerDetails.customer_email = customerEmail), customerName && (customerDetails.customer_name = customerName), customerBankAccountNumber && (customerDetails.customer_bank_account_number = customerBankAccountNumber), customerBankIfsc && (customerDetails.customer_bank_ifsc = customerBankIfsc), customerBankCode && (customerDetails.customer_bank_code = customerBankCode);
    let linkNotify = {};
    sendSms !== void 0 && (linkNotify.send_sms = sendSms), sendEmail !== void 0 && (linkNotify.send_email = sendEmail);
    let linkMeta = {};
    notifyUrl && (linkMeta.notify_url = notifyUrl), returnUrl && (linkMeta.return_url = returnUrl), upiIntent !== void 0 && (linkMeta.upi_intent = upiIntent), paymentMethods && (linkMeta.payment_methods = paymentMethods);
    let parsedLinkNotes;
    if (linkNotes)
      try {
        parsedLinkNotes = JSON.parse(linkNotes);
      } catch (error) {
        return {
          success: !1,
          error: "Invalid JSON format for link notes",
          message: 'Link notes must be valid JSON format. Example: {"key_1":"value_1","key_2":"value_2"}'
        };
      }
    let parsedOrderSplits;
    if (orderSplits)
      try {
        parsedOrderSplits = JSON.parse(orderSplits);
      } catch (error) {
        return {
          success: !1,
          error: "Invalid JSON format for order splits",
          message: 'Order splits must be valid JSON array. Example: [{"vendor_id":"vendor1","amount":100}]'
        };
      }
    let requestBody = {
      link_amount: linkAmount,
      link_currency: linkCurrency,
      link_purpose: linkPurpose,
      customer_details: customerDetails,
      link_id: generatedLinkId
    };
    linkPartialPayments !== void 0 && (requestBody.link_partial_payments = linkPartialPayments), linkMinimumPartialAmount && (requestBody.link_minimum_partial_amount = linkMinimumPartialAmount), linkExpiryTime && (requestBody.link_expiry_time = linkExpiryTime), Object.keys(linkNotify).length > 0 && (requestBody.link_notify = linkNotify), linkAutoReminders !== void 0 && (requestBody.link_auto_reminders = linkAutoReminders), Object.keys(linkMeta).length > 0 && (requestBody.link_meta = linkMeta), parsedLinkNotes && (requestBody.link_notes = parsedLinkNotes), parsedOrderSplits && (requestBody.order_splits = parsedOrderSplits);
    let headers = {
      "x-api-version": "2025-01-01",
      "Content-Type": "application/json",
      Accept: "application/json",
      "x-client-id": clientId,
      "x-client-secret": clientSecret
    };
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.POST,
        url: baseUrl,
        headers,
        body: requestBody
      });
      return response.status === 200 || response.status === 201 ? {
        success: !0,
        data: response.body,
        message: "Payment link created successfully"
      } : {
        success: !1,
        error: response.body,
        message: "Failed to create payment link",
        status: response.status
      };
    } catch (error) {
      return console.error("Error creating Cashfree payment link:", error), {
        success: !1,
        error,
        message: "An error occurred while creating the payment link"
      };
    }
  }
});

// ../ap/packages/pieces/community/cashfree-payments/src/lib/actions/create-refund.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createRefund = (0, import_pieces_framework4.createAction)({
  auth: cashfreePaymentsAuth,
  name: "create-refund",
  classification: "DESTRUCTIVE",
  displayName: "Create Refund",
  description: "Initiate a refund for a Cashfree order. Refunds can only be initiated within six months of the original transaction.",
  audience: "both",
  aiMetadata: {
    description: "Initiates a refund against an existing Cashfree order, identified by its Order ID, for a specified refund amount (must not exceed the original transaction) and a caller-supplied Refund ID. Use this to return money to a customer for a completed payment; refunds are only allowed within six months of the original transaction. Each call moves money so it is not idempotent, though passing the same Refund ID (or the optional Idempotency Key header) lets Cashfree deduplicate retries.",
    idempotent: !1
  },
  requireAuth: !0,
  props: {
    environment: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Environment",
      description: "Choose the environment for API calls",
      required: !0,
      defaultValue: "sandbox",
      options: {
        disabled: !1,
        options: [
          {
            label: "Sandbox",
            value: "sandbox"
          },
          {
            label: "Production",
            value: "production"
          }
        ]
      }
    }),
    // Required Fields
    orderId: import_pieces_framework4.Property.ShortText({
      displayName: "Order ID",
      description: "The ID which uniquely identifies your order",
      required: !0
    }),
    refundAmount: import_pieces_framework4.Property.Number({
      displayName: "Refund Amount",
      description: "Amount to be refunded. Should be lesser than or equal to the transaction amount. (Decimals allowed)",
      required: !0
    }),
    refundId: import_pieces_framework4.Property.ShortText({
      displayName: "Refund ID",
      description: "An unique ID to associate the refund with. Provide alphanumeric values (3-40 characters)",
      required: !0
    }),
    // Optional Fields
    refundNote: import_pieces_framework4.Property.ShortText({
      displayName: "Refund Note",
      description: "A refund note for your reference (3-100 characters)",
      required: !1
    }),
    refundSpeed: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Refund Speed",
      description: "Speed at which the refund is processed",
      required: !1,
      defaultValue: "STANDARD",
      options: {
        disabled: !1,
        options: [
          {
            label: "Standard",
            value: "STANDARD"
          },
          {
            label: "Instant",
            value: "INSTANT"
          }
        ]
      }
    }),
    // Refund Splits
    refundSplits: import_pieces_framework4.Property.LongText({
      displayName: "Refund Splits",
      description: 'JSON array for vendor splits. Example: [{"vendor_id":"vendor1","amount":100},{"vendor_id":"vendor2","percentage":25}]',
      required: !1
    }),
    // Custom Tags
    tags: import_pieces_framework4.Property.LongText({
      displayName: "Custom Tags",
      description: 'Custom Tags in JSON format {"key":"value"}. Maximum 10 tags allowed. Example: {"refund_reason":"customer_request","processed_by":"admin"}',
      required: !1
    }),
    // Request Headers
    requestId: import_pieces_framework4.Property.ShortText({
      displayName: "Request ID",
      description: "Request ID for the API call. Can be used to resolve tech issues",
      required: !1
    }),
    idempotencyKey: import_pieces_framework4.Property.ShortText({
      displayName: "Idempotency Key",
      description: "UUID format idempotency key to avoid duplicate actions if request fails or times out",
      required: !1
    })
  },
  async run(context) {
    let { clientId, clientSecret } = context.auth.props;
    if (!clientId || !clientSecret)
      return {
        success: !1,
        error: "Client ID and Client Secret are required",
        message: "Please provide both Client ID and Client Secret for authentication"
      };
    let {
      environment,
      orderId,
      refundAmount,
      refundId,
      refundNote,
      refundSpeed,
      refundSplits,
      tags,
      requestId,
      idempotencyKey
    } = context.propsValue;
    if (refundId.length < 3 || refundId.length > 40)
      return {
        success: !1,
        error: "Invalid refund ID length",
        message: "Refund ID must be between 3 and 40 characters"
      };
    if (refundNote && (refundNote.length < 3 || refundNote.length > 100))
      return {
        success: !1,
        error: "Invalid refund note length",
        message: "Refund note must be between 3 and 100 characters"
      };
    if (refundAmount <= 0)
      return {
        success: !1,
        error: "Invalid refund amount",
        message: "Refund amount must be greater than 0"
      };
    let baseUrl = environment === "production" ? `https://api.cashfree.com/pg/orders/${orderId}/refunds` : `https://sandbox.cashfree.com/pg/orders/${orderId}/refunds`, parsedRefundSplits;
    if (refundSplits)
      try {
        if (parsedRefundSplits = JSON.parse(refundSplits), !Array.isArray(parsedRefundSplits))
          return {
            success: !1,
            error: "Invalid refund splits format",
            message: "Refund splits must be a JSON array"
          };
        for (let split of parsedRefundSplits) {
          if (!split.vendor_id)
            return {
              success: !1,
              error: "Invalid refund split entry",
              message: "Each refund split must have a vendor_id"
            };
          if (!split.amount && !split.percentage)
            return {
              success: !1,
              error: "Invalid refund split entry",
              message: "Each refund split must have either amount or percentage"
            };
        }
      } catch (error) {
        return {
          success: !1,
          error: "Invalid JSON format for refund splits",
          message: 'Refund splits must be valid JSON array. Example: [{"vendor_id":"vendor1","amount":100}]'
        };
      }
    let parsedTags;
    if (tags)
      try {
        if (parsedTags = JSON.parse(tags), typeof parsedTags != "object" || Array.isArray(parsedTags))
          return {
            success: !1,
            error: "Invalid tags format",
            message: "Tags must be a JSON object"
          };
        if (Object.keys(parsedTags).length > 10)
          return {
            success: !1,
            error: "Too many tags",
            message: "Maximum 10 tags are allowed"
          };
      } catch (error) {
        return {
          success: !1,
          error: "Invalid JSON format for tags",
          message: 'Tags must be valid JSON format. Example: {"refund_reason":"customer_request","processed_by":"admin"}'
        };
      }
    let requestBody = {
      refund_amount: refundAmount,
      refund_id: refundId
    };
    refundNote && (requestBody.refund_note = refundNote), refundSpeed && (requestBody.refund_speed = refundSpeed), parsedRefundSplits && (requestBody.refund_splits = parsedRefundSplits), parsedTags && (requestBody.tags = parsedTags);
    let headers = {
      "x-api-version": "2025-01-01",
      "Content-Type": "application/json",
      Accept: "application/json",
      "x-client-id": clientId,
      "x-client-secret": clientSecret
    };
    requestId && (headers["x-request-id"] = requestId), idempotencyKey && (headers["x-idempotency-key"] = idempotencyKey);
    try {
      let response = await import_pieces_common4.httpClient.sendRequest({
        method: import_pieces_common4.HttpMethod.POST,
        url: baseUrl,
        headers,
        body: requestBody
      });
      return response.status === 200 || response.status === 201 ? {
        success: !0,
        data: response.body,
        message: "Refund created successfully"
      } : {
        success: !1,
        error: response.body,
        message: "Failed to create refund",
        status: response.status
      };
    } catch (error) {
      return console.error("Error creating Cashfree refund:", error), {
        success: !1,
        error,
        message: "An error occurred while creating the refund"
      };
    }
  }
});

// ../ap/packages/pieces/community/cashfree-payments/src/lib/actions/cancel-payment-link.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var cancelPaymentLink = (0, import_pieces_framework5.createAction)({
  auth: cashfreePaymentsAuth,
  name: "cancel-payment-link",
  classification: "DESTRUCTIVE",
  displayName: "Cancel Payment Link",
  description: "Cancel a payment link in Cashfree Payment Gateway. Only links in ACTIVE status can be cancelled.",
  audience: "both",
  aiMetadata: {
    description: "Cancels an existing Cashfree payment link, identified by its Link ID, so it can no longer be paid. Use this to revoke a link you previously issued. Only links in ACTIVE status can be cancelled (already-cancelled or paid links return a conflict). Cancelling an already-cancelled link has no further effect, but the operation mutates link state and is not treated as idempotent.",
    idempotent: !1
  },
  requireAuth: !0,
  props: {
    environment: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Environment",
      description: "Choose the environment for API calls",
      required: !0,
      defaultValue: "sandbox",
      options: {
        disabled: !1,
        options: [
          {
            label: "Sandbox",
            value: "sandbox"
          },
          {
            label: "Production",
            value: "production"
          }
        ]
      }
    }),
    // Required Fields
    linkId: import_pieces_framework5.Property.ShortText({
      displayName: "Payment Link ID",
      description: "The payment link ID that you want to cancel. Only ACTIVE links can be cancelled.",
      required: !0
    }),
    // Optional Headers
    requestId: import_pieces_framework5.Property.ShortText({
      displayName: "Request ID",
      description: "Request ID for the API call. Can be used to resolve tech issues",
      required: !1
    }),
    idempotencyKey: import_pieces_framework5.Property.ShortText({
      displayName: "Idempotency Key",
      description: "UUID format idempotency key to avoid duplicate actions if request fails or times out",
      required: !1
    })
  },
  async run(context) {
    let { clientId, clientSecret } = context.auth.props;
    if (!clientId || !clientSecret)
      return {
        success: !1,
        error: "Client ID and Client Secret are required",
        message: "Please provide both Client ID and Client Secret for authentication"
      };
    let {
      environment,
      linkId,
      requestId,
      idempotencyKey
    } = context.propsValue;
    if (!linkId || linkId.trim().length === 0)
      return {
        success: !1,
        error: "Invalid link ID",
        message: "Payment Link ID is required and cannot be empty"
      };
    let baseUrl = environment === "production" ? `https://api.cashfree.com/pg/links/${linkId}/cancel` : `https://sandbox.cashfree.com/pg/links/${linkId}/cancel`, headers = {
      "x-api-version": "2025-01-01",
      "Content-Type": "application/json",
      Accept: "application/json",
      "x-client-id": clientId,
      "x-client-secret": clientSecret
    };
    requestId && (headers["x-request-id"] = requestId), idempotencyKey && (headers["x-idempotency-key"] = idempotencyKey);
    try {
      let response = await import_pieces_common5.httpClient.sendRequest({
        method: import_pieces_common5.HttpMethod.POST,
        url: baseUrl,
        headers
      });
      return response.status === 200 || response.status === 201 ? {
        success: !0,
        data: response.body,
        message: "Payment link cancelled successfully"
      } : response.status === 400 ? {
        success: !1,
        error: response.body,
        message: "Bad request - Link may not be in ACTIVE status or invalid link ID",
        status: response.status
      } : response.status === 404 ? {
        success: !1,
        error: response.body,
        message: "Payment link not found with the provided link ID",
        status: response.status
      } : response.status === 409 ? {
        success: !1,
        error: response.body,
        message: "Conflict - Link may already be cancelled or in a non-cancellable state",
        status: response.status
      } : {
        success: !1,
        error: response.body,
        message: "Failed to cancel payment link",
        status: response.status
      };
    } catch (error) {
      return console.error("Error cancelling Cashfree payment link:", error), {
        success: !1,
        error,
        message: "An error occurred while cancelling the payment link"
      };
    }
  }
});

// ../ap/packages/pieces/community/cashfree-payments/src/lib/actions/fetch-payment-link-details.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var fetchPaymentLinkDetails = (0, import_pieces_framework6.createAction)({
  name: "fetch-payment-link-details",
  classification: "READ",
  displayName: "Fetch Payment Link Details",
  description: "View all details and status of a payment link in Cashfree Payment Gateway",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the full details and current status of a single Cashfree payment link identified by its Link ID. Use this to check whether a link is active, expired, or paid, or to read back its configuration. Read-only lookup, so it is idempotent.",
    idempotent: !0
  },
  requireAuth: !0,
  auth: cashfreePaymentsAuth,
  props: {
    environment: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Environment",
      description: "Choose the environment for API calls",
      required: !0,
      defaultValue: "sandbox",
      options: {
        disabled: !1,
        options: [
          {
            label: "Sandbox",
            value: "sandbox"
          },
          {
            label: "Production",
            value: "production"
          }
        ]
      }
    }),
    // Required Fields
    linkId: import_pieces_framework6.Property.ShortText({
      displayName: "Payment Link ID",
      description: "The payment link ID for which you want to view the details",
      required: !0
    }),
    // Optional Headers
    requestId: import_pieces_framework6.Property.ShortText({
      displayName: "Request ID",
      description: "Request ID for the API call. Can be used to resolve tech issues",
      required: !1
    }),
    idempotencyKey: import_pieces_framework6.Property.ShortText({
      displayName: "Idempotency Key",
      description: "UUID format idempotency key for request deduplication",
      required: !1
    })
  },
  async run(context) {
    let { clientId, clientSecret } = context.auth.props;
    if (!clientId || !clientSecret)
      return {
        success: !1,
        error: "Client ID and Client Secret are required",
        message: "Please provide both Client ID and Client Secret for authentication"
      };
    let {
      environment,
      linkId,
      requestId,
      idempotencyKey
    } = context.propsValue;
    if (!linkId || linkId.trim().length === 0)
      return {
        success: !1,
        error: "Invalid link ID",
        message: "Payment Link ID is required and cannot be empty"
      };
    let baseUrl = environment === "production" ? `https://api.cashfree.com/pg/links/${linkId}` : `https://sandbox.cashfree.com/pg/links/${linkId}`, headers = {
      "x-api-version": "2025-01-01",
      Accept: "application/json",
      "x-client-id": clientId,
      "x-client-secret": clientSecret
    };
    requestId && (headers["x-request-id"] = requestId), idempotencyKey && (headers["x-idempotency-key"] = idempotencyKey);
    try {
      let response = await import_pieces_common6.httpClient.sendRequest({
        method: import_pieces_common6.HttpMethod.GET,
        url: baseUrl,
        headers
      });
      return response.status === 200 ? {
        success: !0,
        data: response.body,
        message: "Payment link details fetched successfully"
      } : response.status === 400 ? {
        success: !1,
        error: response.body,
        message: "Bad request - Invalid link ID format or parameters",
        status: response.status
      } : response.status === 404 ? {
        success: !1,
        error: response.body,
        message: "Payment link not found with the provided link ID",
        status: response.status
      } : response.status === 401 ? {
        success: !1,
        error: response.body,
        message: "Unauthorized - Please check your authentication credentials",
        status: response.status
      } : response.status === 403 ? {
        success: !1,
        error: response.body,
        message: "Forbidden - You do not have permission to access this link",
        status: response.status
      } : {
        success: !1,
        error: response.body,
        message: "Failed to fetch payment link details",
        status: response.status
      };
    } catch (error) {
      return console.error("Error fetching Cashfree payment link details:", error), {
        success: !1,
        error,
        message: "An error occurred while fetching the payment link details"
      };
    }
  }
});

// ../ap/packages/pieces/community/cashfree-payments/src/lib/actions/create-cashgram.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var createCashgram = (0, import_pieces_framework7.createAction)({
  auth: cashfreePaymentsAuth,
  name: "create-cashgram",
  classification: "WRITE",
  displayName: "Create Cashgram",
  description: "Create a Cashgram for instant money transfers using Cashfree",
  audience: "both",
  aiMetadata: {
    description: 'Creates a Cashfree Payout "Cashgram" \u2014 a shareable link that lets a contact claim a money transfer for a given amount \u2014 addressed to a name and phone, with an expiry within 30 days. Use this to disburse funds without knowing the recipient bank details. Each call creates a new payout keyed on the caller-supplied Cashgram ID (a duplicate ID returns a conflict), and it moves money, so it is not idempotent.',
    idempotent: !1
  },
  requireAuth: !0,
  props: {
    environment: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Environment",
      description: "Choose the environment for API calls",
      required: !0,
      defaultValue: "sandbox",
      options: {
        disabled: !1,
        options: [
          {
            label: "Sandbox",
            value: "sandbox"
          },
          {
            label: "Production",
            value: "production"
          }
        ]
      }
    }),
    // Required Fields
    cashgramId: import_pieces_framework7.Property.ShortText({
      displayName: "Cashgram ID",
      description: "Unique ID of the Cashgram. Alphanumeric, underscore (_), and hyphen (-) allowed (35 character limit)",
      required: !0
    }),
    amount: import_pieces_framework7.Property.Number({
      displayName: "Amount",
      description: "Amount to be transferred (minimum 1.00)",
      required: !0
    }),
    name: import_pieces_framework7.Property.ShortText({
      displayName: "Contact Name",
      description: "Name of the contact",
      required: !0
    }),
    phone: import_pieces_framework7.Property.ShortText({
      displayName: "Phone Number",
      description: "Phone number of the contact",
      required: !0
    }),
    linkExpiry: import_pieces_framework7.Property.ShortText({
      displayName: "Link Expiry Date",
      description: "Date to expire the cashgram link. Format: YYYY/MM/DD (maximum 30 days from creation)",
      required: !0
    }),
    // Optional Fields
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Email",
      description: "Email of the contact",
      required: !1
    }),
    remarks: import_pieces_framework7.Property.ShortText({
      displayName: "Remarks",
      description: "Specify remarks, if any",
      required: !1
    }),
    notifyCustomer: import_pieces_framework7.Property.Checkbox({
      displayName: "Notify Customer",
      description: "If enabled, a link is sent to customer phone and email",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let {
      clientId,
      clientSecret
    } = context.auth.props, validation = validateAuthCredentials("client_credentials", {
      clientId,
      clientSecret
    });
    if (!validation.isValid)
      return {
        success: !1,
        error: "Invalid authentication credentials",
        message: validation.error
      };
    let { environment } = context.propsValue, tokenResponse = await generateCashgramToken(
      {
        clientId,
        clientSecret
      },
      environment
    );
    if (!tokenResponse.success || !tokenResponse.token)
      return {
        success: !1,
        error: tokenResponse.error,
        message: tokenResponse.message || "Failed to generate bearer token for Cashgram authentication"
      };
    let finalBearerToken = tokenResponse.token, {
      cashgramId,
      amount,
      name,
      phone,
      linkExpiry,
      email,
      remarks,
      notifyCustomer
    } = context.propsValue;
    if (!cashgramId || cashgramId.trim().length === 0)
      return {
        success: !1,
        error: "Invalid Cashgram ID",
        message: "Cashgram ID is required and cannot be empty"
      };
    if (cashgramId.length > 35)
      return {
        success: !1,
        error: "Invalid Cashgram ID length",
        message: "Cashgram ID must be 35 characters or less"
      };
    if (!/^[a-zA-Z0-9_-]+$/.test(cashgramId))
      return {
        success: !1,
        error: "Invalid Cashgram ID format",
        message: "Cashgram ID can only contain alphanumeric characters, underscore (_), and hyphen (-)"
      };
    if (amount < 1)
      return {
        success: !1,
        error: "Invalid amount",
        message: "Amount must be greater than or equal to 1.00"
      };
    if (!/^\d{4}\/\d{2}\/\d{2}$/.test(linkExpiry))
      return {
        success: !1,
        error: "Invalid link expiry date format",
        message: "Link expiry date must be in YYYY/MM/DD format"
      };
    let expiryDate = new Date(linkExpiry.replace(/\//g, "-")), currentDate = /* @__PURE__ */ new Date(), thirtyDaysFromNow = /* @__PURE__ */ new Date();
    if (thirtyDaysFromNow.setDate(currentDate.getDate() + 30), expiryDate > thirtyDaysFromNow)
      return {
        success: !1,
        error: "Invalid link expiry date",
        message: "Link expiry date cannot be more than 30 days from today"
      };
    if (expiryDate <= currentDate)
      return {
        success: !1,
        error: "Invalid link expiry date",
        message: "Link expiry date must be in the future"
      };
    if (!phone || phone.trim().length === 0)
      return {
        success: !1,
        error: "Invalid phone number",
        message: "Phone number is required"
      };
    if (!name || name.trim().length === 0)
      return {
        success: !1,
        error: "Invalid name",
        message: "Contact name is required"
      };
    let baseUrl = environment === "production" ? "https://payout-api.cashfree.com/payout/v1/createCashgram" : "https://payout-gamma.cashfree.com/payout/v1/createCashgram", requestBody = {
      cashgramId,
      amount,
      name,
      phone,
      linkExpiry
    };
    email && (requestBody.email = email), remarks && (requestBody.remarks = remarks), notifyCustomer !== void 0 && (requestBody.notifyCustomer = notifyCustomer ? 1 : 0);
    let headers = {
      Authorization: `Bearer ${finalBearerToken}`,
      "Content-Type": "application/json",
      Accept: "application/json"
    };
    try {
      let response = await import_pieces_common7.httpClient.sendRequest({
        method: import_pieces_common7.HttpMethod.POST,
        url: baseUrl,
        headers,
        body: requestBody
      });
      return response.status === 200 ? {
        success: !0,
        data: response.body
      } : response.status === 400 ? {
        success: !1,
        error: response.body,
        message: "Bad request - Please check your input parameters",
        status: response.status
      } : response.status === 401 ? {
        success: !1,
        error: response.body,
        message: "Unauthorized - Please check your Bearer Token",
        status: response.status
      } : response.status === 403 ? {
        success: !1,
        error: response.body,
        message: "Forbidden - You do not have permission to create Cashgrams",
        status: response.status
      } : response.status === 409 ? {
        success: !1,
        error: response.body,
        message: "Conflict - Cashgram ID already exists",
        status: response.status
      } : {
        success: !1,
        error: response.body,
        message: "Failed to create Cashgram",
        status: response.status
      };
    } catch (error) {
      return console.error("Error creating Cashfree Cashgram:", error), {
        success: !1,
        error,
        message: "An error occurred while creating the Cashgram"
      };
    }
  }
});

// ../ap/packages/pieces/community/cashfree-payments/src/lib/actions/get-orders-for-payment-link.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var getOrdersForPaymentLink = (0, import_pieces_framework8.createAction)({
  auth: cashfreePaymentsAuth,
  name: "get-orders-for-payment-link",
  classification: "SEARCH",
  displayName: "Get Orders for Payment Link",
  description: "View all order details for a payment link in Cashfree Payment Gateway",
  audience: "both",
  aiMetadata: {
    description: "Lists the orders associated with a Cashfree payment link, identified by its Link ID, along with summary totals. Use this to see who paid a link and reconcile collections. The Order Status Filter selects between all orders or paid-only (the default); read-only listing, so it is idempotent.",
    idempotent: !0
  },
  requireAuth: !0,
  props: {
    environment: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Environment",
      description: "Choose the environment for API calls",
      required: !0,
      defaultValue: "sandbox",
      options: {
        disabled: !1,
        options: [
          {
            label: "Sandbox",
            value: "sandbox"
          },
          {
            label: "Production",
            value: "production"
          }
        ]
      }
    }),
    // Required Fields
    linkId: import_pieces_framework8.Property.ShortText({
      displayName: "Payment Link ID",
      description: "The payment link ID for which you want to view the order details",
      required: !0
    }),
    // Optional Query Parameters
    status: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Order Status Filter",
      description: "Filter orders by status (default is PAID)",
      required: !1,
      defaultValue: "PAID",
      options: {
        disabled: !1,
        options: [
          {
            label: "All Orders",
            value: "ALL"
          },
          {
            label: "Paid Orders Only",
            value: "PAID"
          }
        ]
      }
    }),
    // Optional Headers
    requestId: import_pieces_framework8.Property.ShortText({
      displayName: "Request ID",
      description: "Request ID for the API call. Can be used to resolve tech issues",
      required: !1
    }),
    idempotencyKey: import_pieces_framework8.Property.ShortText({
      displayName: "Idempotency Key",
      description: "UUID format idempotency key for request deduplication",
      required: !1
    })
  },
  async run(context) {
    let { clientId, clientSecret } = context.auth.props;
    if (!clientId || !clientSecret)
      return {
        success: !1,
        error: "Client ID and Client Secret are required",
        message: "Please provide both Client ID and Client Secret for authentication"
      };
    let {
      environment,
      linkId,
      status,
      requestId,
      idempotencyKey
    } = context.propsValue;
    if (!linkId || linkId.trim().length === 0)
      return {
        success: !1,
        error: "Invalid link ID",
        message: "Payment Link ID is required and cannot be empty"
      };
    let baseUrl = environment === "production" ? `https://api.cashfree.com/pg/links/${linkId}/orders` : `https://sandbox.cashfree.com/pg/links/${linkId}/orders`, queryParams = new URLSearchParams();
    status && status !== "PAID" && queryParams.append("status", status), queryParams.toString() && (baseUrl += `?${queryParams.toString()}`);
    let headers = {
      "x-api-version": "2025-01-01",
      Accept: "application/json",
      "x-client-id": clientId,
      "x-client-secret": clientSecret
    };
    requestId && (headers["x-request-id"] = requestId), idempotencyKey && (headers["x-idempotency-key"] = idempotencyKey);
    try {
      let response = await import_pieces_common8.httpClient.sendRequest({
        method: import_pieces_common8.HttpMethod.GET,
        url: baseUrl,
        headers
      });
      if (response.status === 200) {
        let ordersData = response.body, orders = Array.isArray(ordersData) ? ordersData : (ordersData == null ? void 0 : ordersData.orders) || [], totalOrders = orders.length, totalAmount = 0, paidAmount = 0, paidOrdersCount = 0, orderStatuses = /* @__PURE__ */ new Set();
        return orders.forEach((order) => {
          order.order_amount && (totalAmount += parseFloat(order.order_amount)), order.order_status && orderStatuses.add(order.order_status), order.order_status === "PAID" && (paidOrdersCount++, order.order_amount && (paidAmount += parseFloat(order.order_amount)));
        }), {
          success: !0,
          data: ordersData,
          message: `Found ${totalOrders} order(s) for payment link`,
          // Summary information
          summary: {
            totalOrders,
            paidOrders: paidOrdersCount,
            totalAmount,
            paidAmount,
            orderStatuses: Array.from(orderStatuses)
          },
          // Individual orders
          orders,
          // Request parameters for reference
          linkId,
          statusFilter: status || "PAID",
          // Quick access to key order information
          orderIds: orders.map((order) => order.order_id).filter(Boolean),
          customerIds: [...new Set(orders.map((order) => {
            var _a;
            return (_a = order.customer_details) == null ? void 0 : _a.customer_id;
          }).filter(Boolean))],
          paymentMethods: [...new Set(orders.map((order) => order.payment_method).filter(Boolean))]
        };
      } else
        return response.status === 400 ? {
          success: !1,
          error: response.body,
          message: "Bad request - Invalid link ID or query parameters",
          status: response.status
        } : response.status === 404 ? {
          success: !1,
          error: response.body,
          message: "Payment link not found with the provided link ID",
          status: response.status
        } : response.status === 401 ? {
          success: !1,
          error: response.body,
          message: "Unauthorized - Please check your authentication credentials",
          status: response.status
        } : response.status === 403 ? {
          success: !1,
          error: response.body,
          message: "Forbidden - You do not have permission to access this link's orders",
          status: response.status
        } : {
          success: !1,
          error: response.body,
          message: "Failed to fetch orders for payment link",
          status: response.status
        };
    } catch (error) {
      return console.error("Error fetching orders for Cashfree payment link:", error), {
        success: !1,
        error,
        message: "An error occurred while fetching orders for the payment link"
      };
    }
  }
});

// ../ap/packages/pieces/community/cashfree-payments/src/lib/actions/get-all-refunds-for-order.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var getAllRefundsForOrder = (0, import_pieces_framework9.createAction)({
  auth: cashfreePaymentsAuth,
  name: "get-all-refunds-for-order",
  classification: "SEARCH",
  displayName: "Get All Refunds for Order",
  description: "Fetch all refunds processed against an order in Cashfree Payment Gateway",
  audience: "both",
  aiMetadata: {
    description: "Lists all refunds processed against a Cashfree order, identified by its Order ID, with summary totals and per-refund status. Use this to check refund progress or reconcile refunds for an order. Read-only listing, so it is idempotent.",
    idempotent: !0
  },
  requireAuth: !0,
  props: {
    environment: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Environment",
      description: "Choose the environment for API calls",
      required: !0,
      defaultValue: "sandbox",
      options: {
        disabled: !1,
        options: [
          {
            label: "Sandbox",
            value: "sandbox"
          },
          {
            label: "Production",
            value: "production"
          }
        ]
      }
    }),
    // Required Fields
    orderId: import_pieces_framework9.Property.ShortText({
      displayName: "Order ID",
      description: "The ID which uniquely identifies your order",
      required: !0
    }),
    // Optional Headers
    requestId: import_pieces_framework9.Property.ShortText({
      displayName: "Request ID",
      description: "Request ID for the API call. Can be used to resolve tech issues",
      required: !1
    }),
    idempotencyKey: import_pieces_framework9.Property.ShortText({
      displayName: "Idempotency Key",
      description: "UUID format idempotency key for request deduplication",
      required: !1
    })
  },
  async run(context) {
    let { clientId, clientSecret } = context.auth.props;
    if (!clientId || !clientSecret)
      return {
        success: !1,
        error: "Client ID and Client Secret are required",
        message: "Please provide both Client ID and Client Secret for authentication"
      };
    let {
      environment,
      orderId,
      requestId,
      idempotencyKey
    } = context.propsValue;
    if (!orderId || orderId.trim().length === 0)
      return {
        success: !1,
        error: "Invalid order ID",
        message: "Order ID is required and cannot be empty"
      };
    let baseUrl = environment === "production" ? `https://api.cashfree.com/pg/orders/${orderId}/refunds` : `https://sandbox.cashfree.com/pg/orders/${orderId}/refunds`, headers = {
      "x-api-version": "2025-01-01",
      Accept: "application/json",
      "x-client-id": clientId,
      "x-client-secret": clientSecret
    };
    requestId && (headers["x-request-id"] = requestId), idempotencyKey && (headers["x-idempotency-key"] = idempotencyKey);
    try {
      let response = await import_pieces_common9.httpClient.sendRequest({
        method: import_pieces_common9.HttpMethod.GET,
        url: baseUrl,
        headers
      });
      if (response.status === 200) {
        let refundsData = response.body, refunds = Array.isArray(refundsData) ? refundsData : (refundsData == null ? void 0 : refundsData.refunds) || [], totalRefunds = refunds.length, totalRefundAmount = 0, successfulRefunds = 0, pendingRefunds = 0, failedRefunds = 0, refundStatuses = /* @__PURE__ */ new Set(), refundModes = /* @__PURE__ */ new Set(), refundSpeeds = /* @__PURE__ */ new Set();
        refunds.forEach((refund) => {
          if (refund.refund_amount && (totalRefundAmount += parseFloat(refund.refund_amount)), refund.refund_status) {
            refundStatuses.add(refund.refund_status);
            let status = refund.refund_status.toUpperCase();
            status === "SUCCESS" || status === "PROCESSED" ? successfulRefunds++ : status === "PENDING" || status === "PROCESSING" ? pendingRefunds++ : (status === "FAILED" || status === "CANCELLED") && failedRefunds++;
          }
          refund.refund_mode && refundModes.add(refund.refund_mode), refund.refund_speed && refundSpeeds.add(refund.refund_speed);
        });
        let refundIds = refunds.map((refund) => refund.refund_id || refund.cf_refund_id).filter(Boolean), refundCharges = refunds.reduce((sum, refund) => sum + (refund.refund_charge ? parseFloat(refund.refund_charge) : 0), 0);
        return {
          success: !0,
          data: refundsData,
          message: `Found ${totalRefunds} refund(s) for order ${orderId}`,
          // Summary information
          summary: {
            totalRefunds,
            successfulRefunds,
            pendingRefunds,
            failedRefunds,
            totalRefundAmount,
            totalRefundCharges: refundCharges,
            refundStatuses: Array.from(refundStatuses),
            refundModes: Array.from(refundModes),
            refundSpeeds: Array.from(refundSpeeds)
          },
          // Individual refunds
          refunds,
          // Request parameters for reference
          orderId,
          // Quick access to key refund information
          refundIds,
          latestRefund: refunds.length > 0 ? refunds[refunds.length - 1] : null,
          oldestRefund: refunds.length > 0 ? refunds[0] : null,
          // Financial summary
          financialSummary: {
            totalRefunded: totalRefundAmount,
            averageRefundAmount: totalRefunds > 0 ? totalRefundAmount / totalRefunds : 0,
            totalCharges: refundCharges,
            netRefundAmount: totalRefundAmount - refundCharges
          }
        };
      } else
        return response.status === 400 ? {
          success: !1,
          error: response.body,
          message: "Bad request - Invalid order ID format",
          status: response.status
        } : response.status === 404 ? {
          success: !1,
          error: response.body,
          message: "Order not found with the provided order ID",
          status: response.status
        } : response.status === 401 ? {
          success: !1,
          error: response.body,
          message: "Unauthorized - Please check your authentication credentials",
          status: response.status
        } : response.status === 403 ? {
          success: !1,
          error: response.body,
          message: "Forbidden - You do not have permission to access this order's refunds",
          status: response.status
        } : {
          success: !1,
          error: response.body,
          message: "Failed to fetch refunds for order",
          status: response.status
        };
    } catch (error) {
      return console.error("Error fetching refunds for Cashfree order:", error), {
        success: !1,
        error,
        message: "An error occurred while fetching refunds for the order"
      };
    }
  }
});

// ../ap/packages/pieces/community/cashfree-payments/src/lib/actions/deactivate-cashgram.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var deactivateCashgram = (0, import_pieces_framework10.createAction)({
  auth: cashfreePaymentsAuth,
  name: "deactivate-cashgram",
  classification: "DESTRUCTIVE",
  displayName: "Deactivate Cashgram",
  description: "Deactivate a Cashgram to prevent further redemptions using Cashfree",
  audience: "both",
  aiMetadata: {
    description: "Deactivates an existing Cashfree Payout Cashgram, identified by its Cashgram ID, so the recipient can no longer redeem it. Use this to revoke a money-transfer link you previously created. Deactivating an already-deactivated Cashgram returns a conflict; the operation mutates payout state and is not treated as idempotent.",
    idempotent: !1
  },
  requireAuth: !0,
  props: {
    environment: import_pieces_framework10.Property.StaticDropdown({
      displayName: "Environment",
      description: "Choose the environment for API calls",
      required: !0,
      defaultValue: "sandbox",
      options: {
        disabled: !1,
        options: [
          {
            label: "Sandbox",
            value: "sandbox"
          },
          {
            label: "Production",
            value: "production"
          }
        ]
      }
    }),
    // Required Fields
    cashgramId: import_pieces_framework10.Property.ShortText({
      displayName: "Cashgram ID",
      description: "ID of the Cashgram to be deactivated. Alphanumeric and underscore (_) allowed (35 character limit)",
      required: !0
    })
  },
  async run(context) {
    let {
      clientId,
      clientSecret
    } = context.auth.props, validation = validateAuthCredentials("client_credentials", {
      clientId,
      clientSecret
    });
    if (!validation.isValid)
      return {
        success: !1,
        error: "Invalid authentication credentials",
        message: validation.error
      };
    let { environment, cashgramId } = context.propsValue, tokenResponse = await generateCashgramToken(
      {
        clientId,
        clientSecret
      },
      environment
    );
    if (!tokenResponse.success || !tokenResponse.token)
      return {
        success: !1,
        error: tokenResponse.error,
        message: tokenResponse.message || "Failed to generate bearer token for Cashgram authentication"
      };
    let finalBearerToken = tokenResponse.token;
    if (!cashgramId || cashgramId.trim().length === 0)
      return {
        success: !1,
        error: "Invalid Cashgram ID",
        message: "Cashgram ID is required and cannot be empty"
      };
    if (cashgramId.length > 35)
      return {
        success: !1,
        error: "Invalid Cashgram ID length",
        message: "Cashgram ID must be 35 characters or less"
      };
    if (!/^[a-zA-Z0-9_]+$/.test(cashgramId))
      return {
        success: !1,
        error: "Invalid Cashgram ID format",
        message: "Cashgram ID can only contain alphanumeric characters and underscore (_)"
      };
    let baseUrl = environment === "production" ? "https://payout-api.cashfree.com/payout/v1/deactivateCashgram" : "https://payout-gamma.cashfree.com/payout/v1/deactivateCashgram", requestBody = {
      cashgramId
    }, headers = {
      Authorization: `Bearer ${finalBearerToken}`,
      "Content-Type": "application/json",
      Accept: "application/json"
    };
    try {
      let response = await import_pieces_common10.httpClient.sendRequest({
        method: import_pieces_common10.HttpMethod.POST,
        url: baseUrl,
        headers,
        body: requestBody
      });
      if (response.status === 200) {
        let responseData = response.body;
        return {
          success: !0,
          data: responseData,
          message: (responseData == null ? void 0 : responseData.message) || "Cashgram deactivated successfully"
        };
      } else
        return response.status === 400 ? {
          success: !1,
          error: response.body,
          message: "Bad request - Please check your Cashgram ID format",
          status: response.status
        } : response.status === 401 ? {
          success: !1,
          error: response.body,
          message: "Unauthorized - Please check your Bearer Token",
          status: response.status
        } : response.status === 403 ? {
          success: !1,
          error: response.body,
          message: "Forbidden - You do not have permission to deactivate Cashgrams",
          status: response.status
        } : response.status === 404 ? {
          success: !1,
          error: response.body,
          message: "Cashgram not found with the provided ID",
          status: response.status
        } : response.status === 409 ? {
          success: !1,
          error: response.body,
          message: "Conflict - Cashgram may already be deactivated or cannot be deactivated",
          status: response.status
        } : {
          success: !1,
          error: response.body,
          message: "Failed to deactivate Cashgram",
          status: response.status
        };
    } catch (error) {
      return console.error("Error deactivating Cashfree Cashgram:", error), {
        success: !1,
        error,
        message: "An error occurred while deactivating the Cashgram"
      };
    }
  }
});

// ../ap/packages/pieces/community/cashfree-payments/src/index.ts
var cashfreeTriggers = (0, import_pieces_framework11.createPiece)({
  displayName: "Cashfree Payments",
  description: "Cashfree Payments integration for processing payments, refunds, and managing payment links and cashgrams.",
  auth: cashfreePaymentsAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/cashfree-payments.png",
  authors: ["kartikvyas", "sanket-a11y"],
  actions: [
    createOrder,
    createPaymentLink,
    createRefund,
    cancelPaymentLink,
    fetchPaymentLinkDetails,
    createCashgram,
    getOrdersForPaymentLink,
    getAllRefundsForOrder,
    deactivateCashgram
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  cashfreeTriggers
});
