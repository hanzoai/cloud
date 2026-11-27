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

// ../ap/packages/pieces/community/pinch-payments/src/index.ts
var index_exports = {};
__export(index_exports, {
  pinchPayments: () => pinchPayments
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework24 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pinch-payments/src/lib/common/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
async function getPinchPaymentsToken(credentials) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: "https://auth.getpinch.com.au/connect/token",
    authentication: {
      type: import_pieces_common.AuthenticationType.BASIC,
      username: credentials.username,
      password: credentials.password
    },
    headers: {
      "Content-Type": "application/x-www-form-urlencoded"
    },
    body: new URLSearchParams({
      grant_type: "client_credentials",
      scope: "api1"
    })
  })).body;
}
var pinchPaymentsAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `Connect your Pinch Payments account

This connector requires Pinch Payments API credentials. (Application ID and Secret Key)

How to generate API keys:
1. Sign in to your Pinch Payments account at https://web.getpinch.com.au
2. Navigate to API Keys: https://web.getpinch.com.au/api-keys
3. Create an Application to generate API credentials
4. Copy your Application ID and Secret Key (either live or test depending on the environment)

Authentication uses Basic Auth where:
- Username: Your Application ID
- Password: Your Secret Key
`,
  props: {
    username: import_pieces_framework.Property.ShortText({
      displayName: "Application ID",
      description: "Your Application ID",
      required: !0
    }),
    password: import_pieces_framework.Property.ShortText({
      displayName: "Secret Key",
      description: "Your Secret Key",
      required: !0
    }),
    environment: import_pieces_framework.Property.StaticDropdown({
      displayName: "Environment",
      description: "Pinch API environment to use.",
      required: !0,
      options: {
        options: [
          {
            label: "Live",
            value: "live"
          },
          {
            label: "Test",
            value: "test"
          }
        ]
      }
    })
  },
  required: !0,
  async validate(context) {
    try {
      return await getPinchPaymentsToken({
        username: context.auth.username,
        password: context.auth.password,
        environment: context.auth.environment
      }), { valid: !0 };
    } catch (error) {
      return {
        valid: !1,
        error: "Authentication failed. Please check your credentials."
      };
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/actions/create-or-update-payer.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pinch-payments/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var BASE_URL = "https://api.getpinch.com.au";
async function pinchPaymentsClient(credentials, method, endpoint, body, queryParams) {
  let tokenResponse = await getPinchPaymentsToken(credentials), url = `${BASE_URL}/${credentials.environment}${endpoint}`;
  if (queryParams) {
    let params = new URLSearchParams(queryParams);
    url = `${url}?${params.toString()}`;
  }
  return (await import_pieces_common2.httpClient.sendRequest({
    method,
    url,
    headers: {
      Authorization: `Bearer ${tokenResponse.access_token}`,
      "Content-Type": "application/json",
      "User-Agent": "activepieces"
    },
    body
  })).body;
}
async function createOrUpdatePayer(credentials, payer) {
  return pinchPaymentsClient(credentials, import_pieces_common2.HttpMethod.POST, "/payers", payer);
}
async function listPayers(credentials, params) {
  let queryParams = {};
  return params != null && params.page && (queryParams.page = params.page.toString()), params != null && params.pageSize && (queryParams.pageSize = params.pageSize.toString()), params != null && params.filter && (queryParams.filter = params.filter), pinchPaymentsClient(credentials, import_pieces_common2.HttpMethod.GET, "/payers", void 0, queryParams);
}
async function listPlans(credentials, params) {
  let queryParams = {};
  return params != null && params.page && (queryParams.page = params.page.toString()), params != null && params.pageSize && (queryParams.pageSize = params.pageSize.toString()), pinchPaymentsClient(credentials, import_pieces_common2.HttpMethod.GET, "/plans", void 0, queryParams);
}
async function createSubscription(credentials, subscription) {
  return pinchPaymentsClient(credentials, import_pieces_common2.HttpMethod.POST, "/subscriptions", subscription);
}
async function createWebhook(credentials, webhook) {
  return pinchPaymentsClient(credentials, import_pieces_common2.HttpMethod.POST, "/webhooks", webhook);
}
async function deleteWebhook(credentials, webhookId) {
  return pinchPaymentsClient(credentials, import_pieces_common2.HttpMethod.DELETE, `/webhooks/${webhookId}`);
}

// ../ap/packages/pieces/community/pinch-payments/src/lib/actions/create-or-update-payer.ts
var createOrUpdatePayerAction = (0, import_pieces_framework2.createAction)({
  auth: pinchPaymentsAuth,
  name: "create_or_update_payer",
  displayName: "Create or Update Payer",
  description: "Create a new payer or update an existing one",
  audience: "both",
  aiMetadata: { description: "Creates or updates a Pinch Payments payer (a customer/contact who can be charged). Operates in two modes: pass an existing payer id to update that record, or omit it to create a new payer. Optionally attaches a payment source (bank account or credit card) via a capture-script token in the same call. First name and email are required. The update-by-id path is idempotent; omitting the id creates a new payer on each call.", idempotent: !0 },
  props: {
    payerId: import_pieces_framework2.Property.Dropdown({
      displayName: "Payer",
      description: "Select an existing payer to update, or leave empty to create a new one",
      required: !1,
      auth: pinchPaymentsAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your account first",
            options: []
          };
        let credentials = auth.props;
        return {
          disabled: !1,
          options: (await listPayers(credentials, { pageSize: 500 })).data.map((payer) => ({
            label: `${payer.firstName} ${payer.lastName || ""} (${payer.emailAddress})`.trim(),
            value: payer.id
          }))
        };
      }
    }),
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "First name of the payer",
      required: !0
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the payer",
      required: !1
    }),
    emailAddress: import_pieces_framework2.Property.ShortText({
      displayName: "Email Address",
      description: "Email address of the payer",
      required: !0
    }),
    mobileNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Mobile Number",
      description: "Mobile phone number",
      required: !1
    }),
    companyName: import_pieces_framework2.Property.ShortText({
      displayName: "Company Name",
      description: "Company name",
      required: !1
    }),
    companyRegistrationNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Company Registration Number",
      description: "Company registration number (ABN, etc.)",
      required: !1
    }),
    streetAddress: import_pieces_framework2.Property.ShortText({
      displayName: "Street Address",
      description: "Street address",
      required: !1
    }),
    suburb: import_pieces_framework2.Property.ShortText({
      displayName: "Suburb",
      description: "Suburb or city",
      required: !1
    }),
    state: import_pieces_framework2.Property.ShortText({
      displayName: "State",
      description: "State or territory",
      required: !1
    }),
    postcode: import_pieces_framework2.Property.ShortText({
      displayName: "Postcode",
      description: "Postal code",
      required: !1
    }),
    country: import_pieces_framework2.Property.ShortText({
      displayName: "Country",
      description: "Country",
      required: !1
    }),
    metadata: import_pieces_framework2.Property.ShortText({
      displayName: "Metadata",
      description: "Custom metadata for the payer",
      required: !1
    }),
    addPaymentSource: import_pieces_framework2.Property.Checkbox({
      displayName: "Add Payment Source",
      description: "Add a payment source (bank account or credit card) to this payer",
      required: !1,
      defaultValue: !1
    }),
    sourceType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Source Type",
      description: "Type of payment source",
      required: !1,
      options: {
        options: [
          { label: "Bank Account", value: "bank-account" },
          { label: "Credit Card", value: "credit-card" }
        ]
      }
    }),
    sourceToken: import_pieces_framework2.Property.ShortText({
      displayName: "Source Token",
      description: "The token created by the Pinch capture script",
      required: !1
    }),
    ipAddress: import_pieces_framework2.Property.ShortText({
      displayName: "IP Address",
      description: "IP address of the payer (for payment source)",
      required: !1
    })
  },
  async run(context) {
    let {
      payerId,
      firstName,
      lastName,
      emailAddress,
      mobileNumber,
      companyName,
      companyRegistrationNumber,
      streetAddress,
      suburb,
      state,
      postcode,
      country,
      metadata,
      addPaymentSource,
      sourceType,
      sourceToken,
      ipAddress
    } = context.propsValue, payerData = {
      firstName,
      emailAddress
    };
    return payerId && (payerData.id = payerId), lastName && (payerData.lastName = lastName), mobileNumber && (payerData.mobileNumber = mobileNumber), companyName && (payerData.companyName = companyName), companyRegistrationNumber && (payerData.companyRegistrationNumber = companyRegistrationNumber), streetAddress && (payerData.streetAddress = streetAddress), suburb && (payerData.suburb = suburb), state && (payerData.state = state), postcode && (payerData.postcode = postcode), country && (payerData.country = country), metadata && (payerData.metadata = metadata), addPaymentSource && sourceType && sourceToken && (payerData.source = __spreadValues({
      sourceType,
      token: sourceToken
    }, ipAddress && { ipAddress })), await createOrUpdatePayer(
      {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      },
      payerData
    );
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/actions/create-realtime-payment.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var createRealtimePaymentAction = (0, import_pieces_framework3.createAction)({
  auth: pinchPaymentsAuth,
  name: "create_realtime_payment",
  displayName: "Create Realtime Payment",
  description: "Create a real-time payment against a credit card or bank account",
  audience: "both",
  aiMetadata: { description: "Charges a one-off real-time payment against a credit card or bank account in Pinch Payments. Bill either an existing payer (set Use Existing Payer and select the payer) or new payer details supplied inline. Amount is required and given in cents. Use when an agent needs to take an immediate payment rather than schedule one. Not idempotent: each call attempts a new charge; supply a nonce to guard against accidental double submissions.", idempotent: !1 },
  props: {
    payerId: import_pieces_framework3.Property.Dropdown({
      displayName: "Payer",
      description: "Select an existing payer for this payment",
      required: !1,
      auth: pinchPaymentsAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your account first",
            options: []
          };
        let credentials = auth.props;
        return {
          disabled: !1,
          options: (await listPayers(credentials, { pageSize: 500 })).data.map((payer) => ({
            label: `${payer.firstName} ${payer.lastName || ""} (${payer.emailAddress})`.trim(),
            value: payer.id
          }))
        };
      }
    }),
    useExistingPayer: import_pieces_framework3.Property.Checkbox({
      displayName: "Use Existing Payer",
      description: "Check if you want to use an existing payer instead of creating a new one",
      required: !1,
      defaultValue: !1
    }),
    fullName: import_pieces_framework3.Property.ShortText({
      displayName: "Full Name",
      description: "Full name of the payer (use this OR First Name + Last Name)",
      required: !1
    }),
    firstName: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "First name of the payer (use this OR Full Name)",
      required: !1
    }),
    lastName: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the payer (use this OR Full Name)",
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "Email address of the payer",
      required: !1
    }),
    mobileNumber: import_pieces_framework3.Property.ShortText({
      displayName: "Mobile Number",
      description: "Australian 10-digit mobile number (non-international)",
      required: !1
    }),
    amount: import_pieces_framework3.Property.Number({
      displayName: "Amount (cents)",
      description: "Payment amount in cents (e.g., $10.50 = 1050)",
      required: !0
    }),
    applicationFee: import_pieces_framework3.Property.Number({
      displayName: "Application Fee (cents)",
      description: "Optional additional fee in cents (Managed Merchants Only)",
      required: !1
    }),
    description: import_pieces_framework3.Property.ShortText({
      displayName: "Description",
      description: "Optional description for the payment (visible to payer, max 1000 characters)",
      required: !1
    }),
    sourceId: import_pieces_framework3.Property.ShortText({
      displayName: "Source ID",
      description: "Existing payment source ID to use (leave empty to use default or create new source)",
      required: !1
    }),
    token: import_pieces_framework3.Property.ShortText({
      displayName: "Payment Token",
      description: "Credit card or bank account token from capture script (for new payment source)",
      required: !1
    }),
    surcharge: import_pieces_framework3.Property.StaticMultiSelectDropdown({
      displayName: "Surcharge Source Types",
      description: "Select source types to surcharge (pass fees to customer)",
      required: !1,
      options: {
        options: [
          { label: "Bank Account", value: "bank-account" },
          { label: "Credit Card", value: "credit-card" }
        ]
      }
    }),
    nonce: import_pieces_framework3.Property.ShortText({
      displayName: "Nonce",
      description: "Optional one-time-use reference to prevent double submissions",
      required: !1
    }),
    metadata: import_pieces_framework3.Property.ShortText({
      displayName: "Metadata",
      description: "Free text field for storing state or accounting integration data",
      required: !1
    })
  },
  async run(context) {
    let {
      payerId,
      useExistingPayer,
      fullName,
      firstName,
      lastName,
      email,
      mobileNumber,
      amount,
      applicationFee,
      description,
      sourceId,
      token,
      surcharge,
      nonce,
      metadata
    } = context.propsValue, paymentData = {
      amount
    };
    useExistingPayer && payerId ? paymentData.payerId = payerId : (fullName && (paymentData.fullName = fullName), firstName && (paymentData.firstName = firstName), lastName && (paymentData.lastName = lastName), email && (paymentData.email = email), mobileNumber && (paymentData.mobileNumber = mobileNumber)), applicationFee && (paymentData.applicationFee = applicationFee), description && (paymentData.description = description), sourceId && (paymentData.sourceId = sourceId), token && (paymentData.token = token), surcharge && surcharge.length > 0 && (paymentData.surcharge = surcharge), nonce && (paymentData.nonce = nonce), metadata && (paymentData.metadata = metadata);
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    };
    return pinchPaymentsClient(credentials, import_pieces_common3.HttpMethod.POST, "/payments/realtime", paymentData);
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/actions/add-source-to-payer.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var addSourceToPayerAction = (0, import_pieces_framework4.createAction)({
  auth: pinchPaymentsAuth,
  name: "add_source_to_payer",
  displayName: "Add Source to Payer",
  description: "Add a payment source (bank account or credit card) to an existing payer",
  audience: "both",
  aiMetadata: { description: "Adds a payment source (bank account or credit card) to an existing Pinch Payments payer. Two input modes: a capture-script token (works for both source types, IP address recommended) or manual bank-account details (account number, BSB, name \u2014 bank accounts only; credit cards must use a token). Requires the target payer id. Not idempotent: each call appends a new source to the payer.", idempotent: !1 },
  props: {
    payerId: import_pieces_framework4.Property.Dropdown({
      displayName: "Payer",
      description: "Select the payer to add a payment source to",
      required: !0,
      auth: pinchPaymentsAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your account first",
            options: []
          };
        let credentials = auth.props;
        return {
          disabled: !1,
          options: (await listPayers(credentials, { pageSize: 500 })).data.map((payer) => ({
            label: `${payer.firstName} ${payer.lastName || ""} (${payer.emailAddress})`.trim(),
            value: payer.id
          }))
        };
      }
    }),
    sourceType: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Source Type",
      description: "Type of payment source to add",
      required: !0,
      options: {
        options: [
          { label: "Bank Account", value: "bank-account" },
          { label: "Credit Card", value: "credit-card" }
        ]
      }
    }),
    useToken: import_pieces_framework4.Property.Checkbox({
      displayName: "Use Token",
      description: "Use a token from the capture script instead of manual bank account details",
      required: !1,
      defaultValue: !0
    }),
    token: import_pieces_framework4.Property.ShortText({
      displayName: "Payment Token",
      description: "The token created by the Pinch capture script (for both bank accounts and credit cards)",
      required: !1
    }),
    ipAddress: import_pieces_framework4.Property.ShortText({
      displayName: "IP Address",
      description: "IP address of the payer (required for token-based sources)",
      required: !1
    }),
    bankAccountNumber: import_pieces_framework4.Property.ShortText({
      displayName: "Bank Account Number",
      description: "The bank account number (only for manual bank account entry)",
      required: !1
    }),
    bankAccountBsb: import_pieces_framework4.Property.ShortText({
      displayName: "BSB",
      description: "The BSB (Bank-State-Branch) number (only for manual bank account entry)",
      required: !1
    }),
    bankAccountName: import_pieces_framework4.Property.ShortText({
      displayName: "Account Name",
      description: "The name on the bank account (only for manual bank account entry)",
      required: !1
    })
  },
  async run(context) {
    let {
      payerId,
      sourceType,
      useToken,
      token,
      ipAddress,
      bankAccountNumber,
      bankAccountBsb,
      bankAccountName
    } = context.propsValue, sourceData = {
      sourceType
    };
    if (useToken) {
      if (!token)
        throw new Error("Token is required when using token-based payment source");
      sourceData.token = token, ipAddress && (sourceData.ipAddress = ipAddress);
    } else if (sourceType === "bank-account") {
      if (!bankAccountNumber || !bankAccountBsb || !bankAccountName)
        throw new Error("Bank account number, BSB, and account name are required for manual bank account entry");
      sourceData.bankAccountNumber = bankAccountNumber, sourceData.bankAccountBsb = bankAccountBsb, sourceData.bankAccountName = bankAccountName;
    } else
      throw new Error("Manual entry is only supported for bank accounts. Credit cards must use tokens.");
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    };
    return pinchPaymentsClient(credentials, import_pieces_common4.HttpMethod.POST, `/payers/${payerId}/sources`, sourceData);
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/actions/create-or-update-scheduled-payment.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var createOrUpdateScheduledPaymentAction = (0, import_pieces_framework5.createAction)({
  auth: pinchPaymentsAuth,
  name: "create_or_update_scheduled_payment",
  displayName: "Create or Update Scheduled Payment",
  description: "Create a new scheduled payment or update an existing one",
  audience: "both",
  aiMetadata: { description: "Schedules a future-dated payment for a payer in Pinch Payments, or updates an existing scheduled payment when a payment id is provided. Use this (not Create Realtime Payment) when the charge should be attempted on a specific transaction date rather than immediately. Requires payer, amount in cents, and a transaction date. The update-by-id path is idempotent; omitting the id creates a new scheduled payment on each call.", idempotent: !0 },
  props: {
    paymentId: import_pieces_framework5.Property.ShortText({
      displayName: "Payment ID",
      description: "Enter an existing payment ID to update, or leave empty to create a new payment",
      required: !1
    }),
    payerId: import_pieces_framework5.Property.Dropdown({
      displayName: "Payer",
      description: "Select the payer for this scheduled payment",
      required: !0,
      auth: pinchPaymentsAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your account first",
            options: []
          };
        let credentials = auth.props;
        return {
          disabled: !1,
          options: (await listPayers(credentials, { pageSize: 500 })).data.map((payer) => ({
            label: `${payer.firstName} ${payer.lastName || ""} (${payer.emailAddress})`.trim(),
            value: payer.id
          }))
        };
      }
    }),
    amount: import_pieces_framework5.Property.Number({
      displayName: "Amount (cents)",
      description: "Payment amount in cents (e.g., $10.50 = 1050)",
      required: !0
    }),
    transactionDate: import_pieces_framework5.Property.DateTime({
      displayName: "Transaction Date",
      description: "The date to attempt the transaction",
      required: !0
    }),
    description: import_pieces_framework5.Property.ShortText({
      displayName: "Description",
      description: "Optional description for the payment (visible to payer, max 1000 characters)",
      required: !1
    }),
    sourceId: import_pieces_framework5.Property.ShortText({
      displayName: "Source ID",
      description: "Optional: ID of the payment source to use. If omitted, the first valid source will be used",
      required: !1
    }),
    surcharge: import_pieces_framework5.Property.StaticMultiSelectDropdown({
      displayName: "Surcharge Source Types",
      description: "Select source types to surcharge (pass fees to customer)",
      required: !1,
      options: {
        options: [
          { label: "Bank Account", value: "bank-account" },
          { label: "Credit Card", value: "credit-card" }
        ]
      }
    }),
    applicationFee: import_pieces_framework5.Property.Number({
      displayName: "Application Fee (cents)",
      description: "Optional additional fee in cents (Managed Merchants Only)",
      required: !1
    }),
    nonce: import_pieces_framework5.Property.ShortText({
      displayName: "Nonce",
      description: "Optional one-time-use reference to prevent double submissions",
      required: !1
    })
  },
  async run(context) {
    let {
      paymentId,
      payerId,
      amount,
      transactionDate,
      description,
      sourceId,
      surcharge,
      applicationFee,
      nonce
    } = context.propsValue, paymentData = {
      payerId,
      amount,
      transactionDate: transactionDate.toString().split("T")[0]
      // Format as YYYY-MM-DD
    };
    paymentId && (paymentData.id = paymentId), description && (paymentData.description = description), sourceId && (paymentData.sourceId = sourceId), surcharge && surcharge.length > 0 && (paymentData.surcharge = surcharge), applicationFee && (paymentData.applicationFee = applicationFee), nonce && (paymentData.nonce = nonce);
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    };
    return pinchPaymentsClient(credentials, import_pieces_common5.HttpMethod.POST, "/payments", paymentData);
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/actions/add-subscription.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var addSubscriptionAction = (0, import_pieces_framework6.createAction)({
  auth: pinchPaymentsAuth,
  name: "add_subscription",
  displayName: "Add Subscription",
  description: "Create a subscription between a payer and a plan",
  audience: "both",
  aiMetadata: { description: "Creates a recurring subscription linking an existing Pinch Payments payer to an existing plan. Use when an agent needs to enroll a customer in ongoing/recurring billing. Requires a plan id and payer id; total amount is required only for percentage-based plans, and leaving the start date blank begins it immediately. Not idempotent: each call creates a new subscription.", idempotent: !1 },
  props: {
    planId: import_pieces_framework6.Property.Dropdown({
      displayName: "Plan",
      description: "Select the plan for this subscription",
      required: !0,
      auth: pinchPaymentsAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your account first",
            options: []
          };
        let credentials = auth.props;
        return {
          disabled: !1,
          options: (await listPlans(credentials, { pageSize: 500 })).data.map((plan) => ({
            label: `${plan.name} (${plan.id})`,
            value: plan.id
          }))
        };
      }
    }),
    payerId: import_pieces_framework6.Property.Dropdown({
      displayName: "Payer",
      description: "Select the payer for this subscription",
      required: !0,
      auth: pinchPaymentsAuth,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please connect your account first",
            options: []
          };
        let credentials = auth.props;
        return {
          disabled: !1,
          options: (await listPayers(credentials, { pageSize: 500 })).data.map((payer) => ({
            label: `${payer.firstName} ${payer.lastName || ""} (${payer.emailAddress})`.trim(),
            value: payer.id
          }))
        };
      }
    }),
    totalAmount: import_pieces_framework6.Property.Number({
      displayName: "Total Amount (cents)",
      description: "Required for percentage-based plans: total amount in cents (e.g., $100.00 = 10000)",
      required: !1
    }),
    startDate: import_pieces_framework6.Property.DateTime({
      displayName: "Start Date",
      description: "When the subscription should start. Leave blank for immediate start",
      required: !1
    }),
    sourceId: import_pieces_framework6.Property.ShortText({
      displayName: "Source ID",
      description: "Optional: Specific payment source ID to use. If omitted, a source will be automatically selected",
      required: !1
    }),
    surcharge: import_pieces_framework6.Property.StaticMultiSelectDropdown({
      displayName: "Surcharge Source Types",
      description: "Select source types to surcharge (pass fees to customer)",
      required: !1,
      options: {
        options: [
          { label: "Bank Account", value: "bank-account" },
          { label: "Credit Card", value: "credit-card" }
        ]
      }
    })
  },
  async run(context) {
    let {
      planId,
      payerId,
      totalAmount,
      startDate,
      sourceId,
      surcharge
    } = context.propsValue, subscriptionData = {
      planId,
      payerId
    };
    return totalAmount && (subscriptionData.totalAmount = totalAmount), startDate && (subscriptionData.startDate = startDate.toString().split("T")[0]), sourceId && (subscriptionData.sourceId = sourceId), surcharge && surcharge.length > 0 && (subscriptionData.surcharge = surcharge), await createSubscription(
      {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      },
      subscriptionData
    );
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/actions/find-event.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var findEventAction = (0, import_pieces_framework7.createAction)({
  auth: pinchPaymentsAuth,
  name: "find_event",
  displayName: "Find Event",
  description: "Find an event using the Event ID",
  audience: "both",
  aiMetadata: { description: "Retrieves a single Pinch Payments event by its event id (evt_ prefix). Use to look up the details of a specific webhook/notification event. Read-only and idempotent; requires a known event id (this does not list or search events).", idempotent: !0 },
  props: {
    eventId: import_pieces_framework7.Property.ShortText({
      displayName: "Event ID",
      description: "The Event ID in evt_XXXXXXXXXXXXXX format",
      required: !0
    })
  },
  async run(context) {
    let { eventId } = context.propsValue, credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    };
    return pinchPaymentsClient(credentials, import_pieces_common6.HttpMethod.GET, `/events/${eventId}`);
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/actions/find-payer.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var findPayerAction = (0, import_pieces_framework8.createAction)({
  auth: pinchPaymentsAuth,
  name: "find_payer",
  displayName: "Find Payer",
  description: "Find a payer by their Payer ID",
  audience: "both",
  aiMetadata: { description: "Retrieves a single Pinch Payments payer by their payer id (pyr_ prefix). Use to fetch a customer record when you already have its id. Read-only and idempotent; requires a known payer id (this does not search by name or email).", idempotent: !0 },
  props: {
    payerId: import_pieces_framework8.Property.ShortText({
      displayName: "Payer ID",
      description: "The Payer ID in pyr_XXXXXXXXXXXXX format",
      required: !0
    })
  },
  async run(context) {
    let { payerId } = context.propsValue, credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    };
    return pinchPaymentsClient(credentials, import_pieces_common7.HttpMethod.GET, `/payers/${payerId}`);
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/actions/find-subscription.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var findSubscriptionAction = (0, import_pieces_framework9.createAction)({
  auth: pinchPaymentsAuth,
  name: "find_subscription",
  displayName: "Find Subscription",
  description: "Find a subscription using the Subscription ID",
  audience: "both",
  aiMetadata: { description: "Retrieves a single Pinch Payments subscription by its subscription id (sub_ prefix). Use to check the status or details of a known recurring subscription. Read-only and idempotent; requires a known subscription id (this does not list or search subscriptions).", idempotent: !0 },
  props: {
    subscriptionId: import_pieces_framework9.Property.ShortText({
      displayName: "Subscription ID",
      description: "The Subscription ID in sub_XXXXXXXXXXXXXX format",
      required: !0
    })
  },
  async run(context) {
    let { subscriptionId } = context.propsValue, credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    };
    return pinchPaymentsClient(credentials, import_pieces_common8.HttpMethod.GET, `/subscriptions/${subscriptionId}`);
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/bank-results.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var bankResultsTrigger = (0, import_pieces_framework10.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "bank_results",
  displayName: "Bank Results Event",
  description: "Triggers when a bank account transaction returns (may result in dishonour status)",
  aiMetadata: {
    description: "Fires when a bank account (direct debit) transaction returns its result from the bank, which may be a successful clearance or a dishonour. Represents the settlement outcome of bank-account payments; use it to react to failed or dishonoured direct debits."
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["bank-results"]
    });
    await context.store.put("pinch_bank_results_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_bank_results_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "bank-results",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      payments: [
        {
          id: "pay_sample123456",
          amount: 5e3,
          status: "dishonoured",
          payer: {
            id: "pyr_sample123456",
            firstName: "John",
            lastName: "Doe",
            emailAddress: "john.doe@example.com"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/payer-created.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var payerCreatedTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "payer_created",
  displayName: "Payer Created",
  description: "Triggers when a new payer record is created",
  aiMetadata: {
    description: "Fires when a new payer (customer/contact) record is created in Pinch Payments. Use to react to newly onboarded payers, such as syncing them to another system."
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["payer-created"]
    });
    await context.store.put("pinch_payer_created_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_payer_created_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "payer-created",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      payer: {
        id: "pyr_sample123456",
        firstName: "John",
        lastName: "Doe",
        emailAddress: "john.doe@example.com",
        mobileNumber: "0412345678",
        streetAddress: "123 Main St",
        suburb: "Sydney",
        postcode: "2000",
        state: "NSW",
        country: "AU"
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/payer-updated.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var payerUpdatedTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "payer_updated",
  displayName: "Payer Updated",
  description: "Triggers when a payer record is updated",
  aiMetadata: {
    description: "Fires when an existing payer (customer/contact) record is modified in Pinch Payments. Use to keep external records in sync when payer details change."
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["payer-updated"]
    });
    await context.store.put("pinch_payer_updated_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_payer_updated_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "payer-updated",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      payer: {
        id: "pyr_sample123456",
        firstName: "John",
        lastName: "Doe",
        emailAddress: "john.doe@example.com",
        mobileNumber: "0412345678",
        streetAddress: "123 Main St",
        suburb: "Sydney",
        postcode: "2000",
        state: "NSW",
        country: "AU"
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/realtime-payment.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var realtimePaymentTrigger = (0, import_pieces_framework13.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "realtime_payment",
  displayName: "Realtime Payment",
  description: "Triggers when a realtime payment is executed",
  aiMetadata: {
    description: "Fires when a real-time (immediate) payment is executed in Pinch Payments, representing the result of a one-off card or bank-account charge. Use to react to immediate payment outcomes such as a successful or failed charge."
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["realtime-payment"]
    });
    await context.store.put("pinch_realtime_payment_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_realtime_payment_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "realtime-payment",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      payment: {
        id: "pay_sample123456",
        amount: 5e3,
        currency: "AUD",
        status: "successful",
        sourceType: "credit-card",
        payer: {
          id: "pyr_sample123456",
          firstName: "John",
          lastName: "Doe",
          emailAddress: "john.doe@example.com"
        }
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/payment-created.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var paymentCreatedTrigger = (0, import_pieces_framework14.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "payment_created",
  displayName: "Payment Created",
  description: "Triggers when a payment is created (via Save Payment endpoint or Subscription)",
  aiMetadata: {
    description: "Fires when a payment record is created in Pinch Payments, whether through the save-payment endpoint or generated by a subscription. Represents a newly scheduled or recorded payment (typically before it is processed); use to react as payments are queued."
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["payment-created"]
    });
    await context.store.put("pinch_payment_created_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_payment_created_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "payment-created",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      payment: {
        id: "pay_sample123456",
        amount: 5e3,
        currency: "AUD",
        status: "scheduled",
        transactionDate: "2024-01-20",
        payer: {
          id: "pyr_sample123456",
          firstName: "John",
          lastName: "Doe",
          emailAddress: "john.doe@example.com"
        }
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/subscription-created.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var subscriptionCreatedTrigger = (0, import_pieces_framework15.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "subscription_created",
  displayName: "Subscription Created",
  description: "Triggers when a subscription is created for a payer",
  aiMetadata: {
    description: "Fires when a recurring subscription is created for a payer in Pinch Payments. Use to react when a customer is enrolled in ongoing billing against a plan."
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["subscription-created"]
    });
    await context.store.put("pinch_subscription_created_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_subscription_created_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "subscription-created",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      subscription: {
        id: "sub_sample123456",
        planId: "pln_sample123456",
        planName: "Monthly Plan",
        status: "active",
        startDate: "2024-01-15",
        payer: {
          id: "pyr_sample123456",
          firstName: "John",
          lastName: "Doe",
          emailAddress: "john.doe@example.com"
        }
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/subscription-cancelled.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var subscriptionCancelledTrigger = (0, import_pieces_framework16.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "subscription_cancelled",
  displayName: "Subscription Cancelled",
  description: "Triggers when a subscription is cancelled",
  aiMetadata: {
    description: "Fires when a recurring subscription is cancelled in Pinch Payments, ending future scheduled billing. Use to react to churn or to clean up access when a customer cancels."
  },
  type: import_pieces_framework16.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["subscription-cancelled"]
    });
    await context.store.put("pinch_subscription_cancelled_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_subscription_cancelled_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "subscription-cancelled",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      subscription: {
        id: "sub_sample123456",
        planId: "pln_sample123456",
        planName: "Monthly Plan",
        status: "cancelled",
        payer: {
          id: "pyr_sample123456",
          firstName: "John",
          lastName: "Doe",
          emailAddress: "john.doe@example.com"
        }
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/subscription-complete.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var subscriptionCompleteTrigger = (0, import_pieces_framework17.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "subscription_complete",
  displayName: "Subscription Complete",
  description: "Triggers when a subscription is run to completion",
  aiMetadata: {
    description: "Fires when a recurring subscription reaches the end of its term and runs to completion in Pinch Payments (all scheduled payments done), as distinct from being cancelled early. Use to react when a finite billing plan finishes."
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["subscription-complete"]
    });
    await context.store.put("pinch_subscription_complete_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_subscription_complete_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "subscription-complete",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      subscription: {
        id: "sub_sample123456",
        planId: "pln_sample123456",
        planName: "Monthly Plan",
        status: "complete",
        payer: {
          id: "pyr_sample123456",
          firstName: "John",
          lastName: "Doe",
          emailAddress: "john.doe@example.com"
        }
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/refund-created.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var refundCreatedTrigger = (0, import_pieces_framework18.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "refund_created",
  displayName: "Refund Created",
  description: "Triggers when a refund is created",
  aiMetadata: {
    description: "Fires when a refund is created against a payment in Pinch Payments. Use to react when money is being returned to a customer (typically before the refund settles)."
  },
  type: import_pieces_framework18.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["refund-created"]
    });
    await context.store.put("pinch_refund_created_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_refund_created_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "refund-created",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      refund: {
        id: "ref_sample123456",
        amount: 2500,
        status: "pending",
        paymentId: "pay_sample123456"
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/refund-updated.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework");
var refundUpdatedTrigger = (0, import_pieces_framework19.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "refund_updated",
  displayName: "Refund Updated",
  description: "Triggers when a refund is updated (e.g., status change)",
  aiMetadata: {
    description: "Fires when an existing refund changes in Pinch Payments, most commonly a status change (e.g., from pending to completed). Use to track a refund through to settlement."
  },
  type: import_pieces_framework19.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["refund-updated"]
    });
    await context.store.put("pinch_refund_updated_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_refund_updated_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "refund-updated",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      refund: {
        id: "ref_sample123456",
        amount: 2500,
        status: "completed",
        paymentId: "pay_sample123456"
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/scheduled-process.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework");
var scheduledProcessTrigger = (0, import_pieces_framework20.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "scheduled_process",
  displayName: "Scheduled Process",
  description: "Triggers when scheduled payments are processed (daily on business days)",
  aiMetadata: {
    description: "Fires when Pinch Payments runs its scheduled-payment process (daily on business days), submitting that day batch of due payments. Represents the start/result of the daily batch run rather than a single payment; use to react to the daily processing cycle."
  },
  type: import_pieces_framework20.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["scheduled-process"]
    });
    await context.store.put("pinch_scheduled_process_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_scheduled_process_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "scheduled-process",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      payments: [
        {
          id: "pay_sample123456",
          amount: 5e3,
          status: "processing",
          transactionDate: "2024-01-15",
          payer: {
            id: "pyr_sample123456",
            firstName: "John",
            lastName: "Doe",
            emailAddress: "john.doe@example.com"
          }
        }
      ]
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/transfer.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework");
var transferTrigger = (0, import_pieces_framework21.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "transfer",
  displayName: "Transfer Created",
  description: "Triggers when a transfer is created to settle funds to a merchant",
  aiMetadata: {
    description: "Fires when a transfer is created to settle collected funds out to a merchant bank account in Pinch Payments. Use to react to payout/settlement events, such as reconciling deposits."
  },
  type: import_pieces_framework21.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["transfer"]
    });
    await context.store.put("pinch_transfer_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_transfer_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "transfer",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      transfer: {
        id: "tfr_sample123456",
        amount: 5e4,
        currency: "AUD",
        status: "completed"
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/dispute-created.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework");
var disputeCreatedTrigger = (0, import_pieces_framework22.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "dispute_created",
  displayName: "Dispute Created",
  description: "Triggers when a dispute is created",
  aiMetadata: {
    description: "Fires when a dispute (chargeback) is opened against a payment in Pinch Payments. Use to react immediately to new disputes, such as alerting staff or gathering evidence."
  },
  type: import_pieces_framework22.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["dispute-created"]
    });
    await context.store.put("pinch_dispute_created_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_dispute_created_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "dispute-created",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      dispute: {
        id: "dsp_sample123456",
        amount: 5e3,
        status: "open",
        paymentId: "pay_sample123456"
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/lib/triggers/dispute-updated.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework");
var disputeUpdatedTrigger = (0, import_pieces_framework23.createTrigger)({
  auth: pinchPaymentsAuth,
  name: "dispute_updated",
  displayName: "Dispute Updated",
  description: "Triggers when a dispute is updated",
  aiMetadata: {
    description: "Fires when an existing dispute (chargeback) changes in Pinch Payments, typically a status change such as resolution. Use to track a dispute through its lifecycle to its outcome."
  },
  type: import_pieces_framework23.TriggerStrategy.WEBHOOK,
  props: {},
  async onEnable(context) {
    let credentials = {
      username: context.auth.props.username,
      password: context.auth.props.password,
      environment: context.auth.props.environment
    }, webhook = await createWebhook(credentials, {
      uri: context.webhookUrl,
      webhookFormat: "camel-case",
      eventTypes: ["dispute-updated"]
    });
    await context.store.put("pinch_dispute_updated_webhook", webhook);
  },
  async onDisable(context) {
    let webhook = await context.store.get("pinch_dispute_updated_webhook");
    if (webhook) {
      let credentials = {
        username: context.auth.props.username,
        password: context.auth.props.password,
        environment: context.auth.props.environment
      };
      await deleteWebhook(credentials, webhook.id);
    }
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    id: "evt_sample123456",
    type: "dispute-updated",
    eventDate: "2024-01-15T10:30:00Z",
    metadata: {},
    data: {
      dispute: {
        id: "dsp_sample123456",
        amount: 5e3,
        status: "resolved",
        paymentId: "pay_sample123456"
      }
    }
  }
});

// ../ap/packages/pieces/community/pinch-payments/src/index.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework"), pinchPayments = (0, import_pieces_framework24.createPiece)({
  displayName: "Pinch Payments",
  description: "Australian payment processing platform for direct debits, credit cards, and recurring payments. Create payers, process payments, manage subscriptions, and handle payment sources with comprehensive webhook support.",
  auth: pinchPaymentsAuth,
  minimumSupportedRelease: "0.36.1",
  categories: [import_pieces_framework25.PieceCategory.PAYMENT_PROCESSING],
  logoUrl: "https://cdn.activepieces.com/pieces/pinch-payments.jpg",
  authors: ["onyedikachi-david", "dkarzon"],
  actions: [
    createOrUpdatePayerAction,
    createRealtimePaymentAction,
    addSourceToPayerAction,
    createOrUpdateScheduledPaymentAction,
    addSubscriptionAction,
    findEventAction,
    findPayerAction,
    findSubscriptionAction
  ],
  triggers: [
    bankResultsTrigger,
    payerCreatedTrigger,
    payerUpdatedTrigger,
    realtimePaymentTrigger,
    paymentCreatedTrigger,
    subscriptionCreatedTrigger,
    subscriptionCancelledTrigger,
    subscriptionCompleteTrigger,
    refundCreatedTrigger,
    refundUpdatedTrigger,
    scheduledProcessTrigger,
    transferTrigger,
    disputeCreatedTrigger,
    disputeUpdatedTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pinchPayments
});
