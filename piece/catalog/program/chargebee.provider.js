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

// ../ap/packages/pieces/community/chargebee/src/index.ts
var index_exports = {};
__export(index_exports, {
  chargebee: () => chargebee
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_framework15 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chargebee/src/lib/actions/cancel-subscription.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chargebee/src/lib/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chargebee/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), CHARGEBEE_API_SUFFIX = "/api/v2";
function getChargebeeBaseUrl(site) {
  return `https://${site.trim()}.chargebee.com${CHARGEBEE_API_SUFFIX}`;
}
function getChargebeeAuthHeader(apiKey) {
  return `Basic ${Buffer.from(`${apiKey}:`).toString("base64")}`;
}
function cleanObject(value) {
  return Object.fromEntries(
    Object.entries(value).filter(([, entry]) => entry == null ? !1 : typeof entry == "string" ? entry.trim().length > 0 : !0)
  );
}
function toFormEncoded(body) {
  return Object.entries(body).map(
    ([key, value]) => `${key}=${encodeURIComponent(String(value))}`
  ).join("&");
}
async function chargebeeRequest({
  site,
  apiKey,
  method,
  path,
  body,
  contentType = "application/x-www-form-urlencoded"
}) {
  var _a, _b, _c, _d, _e;
  try {
    let serializedBody = body && contentType === "application/x-www-form-urlencoded" ? toFormEncoded(body) : body;
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${getChargebeeBaseUrl(site)}${path}`,
      headers: {
        Accept: "application/json",
        Authorization: getChargebeeAuthHeader(apiKey),
        "Content-Type": contentType
      },
      body: serializedBody
    })).body;
  } catch (error) {
    let chargebeeError = error, status = (_a = chargebeeError.response) == null ? void 0 : _a.status, message = (_e = (_d = (_c = (_b = chargebeeError.response) == null ? void 0 : _b.body) == null ? void 0 : _c.message) != null ? _d : chargebeeError.message) != null ? _e : "Chargebee API request failed.";
    throw new Error(
      status ? `${message} (status ${status})` : message
    );
  }
}

// ../ap/packages/pieces/community/chargebee/src/lib/auth.ts
var chargebeeAuth = import_pieces_framework.PieceAuth.CustomAuth({
  displayName: "Chargebee",
  description: "Go to Settings > Configure Chargebee > API Keys and Webhooks > API Keys tab. Click Add an API Key, select Full-Access Key, and copy the generated key.",
  required: !0,
  props: {
    site: import_pieces_framework.Property.ShortText({
      displayName: "Site",
      description: "Your Chargebee site subdomain. For acme.chargebee.com, enter acme.",
      required: !0
    }),
    api_key: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Go to Settings > Configure Chargebee > API Keys and Webhooks > API Keys tab. Click Add an API Key, select Full-Access Key, and copy the generated key.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await chargebeeRequest({
        site: auth.site,
        apiKey: auth.api_key,
        method: import_pieces_common2.HttpMethod.GET,
        path: "/customers?limit=1"
      }), {
        valid: !0
      };
    } catch (error) {
      return {
        valid: !1,
        error: error instanceof Error ? error.message : "Failed to validate Chargebee credentials."
      };
    }
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/actions/cancel-subscription.ts
var cancelSubscription = (0, import_pieces_framework2.createAction)({
  name: "cancel_subscription",
  classification: "DESTRUCTIVE",
  auth: chargebeeAuth,
  displayName: "Cancel Subscription",
  description: "Cancel a Chargebee subscription immediately, at end of term, or on a specific date.",
  audience: "both",
  aiMetadata: {
    description: "Cancel a Chargebee subscription by ID, with control over timing (immediately, end of term, or a specific date) and, for subscriptions with a contract term, a separate contract-term cancel option plus optional termination fee. Use when an agent needs to end a subscription rather than pause or change it. Requires the subscription ID; the timing and credit/refund handling depend on whether the subscription has a contract term. Repeating the call does not re-cancel an already-cancelled subscription, but it is treated as non-idempotent since options like decommission are irreversible.",
    idempotent: !1
  },
  props: {
    subscription_id: import_pieces_framework2.Property.ShortText({
      displayName: "Subscription ID",
      required: !0
    }),
    cancel_option: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Cancel Option",
      description: "When to cancel the subscription. Not applicable when the subscription has a contract term \u2014 use Contract Term Cancel Option instead.",
      required: !1,
      options: {
        options: [
          { label: "Immediately", value: "immediately" },
          { label: "End of Term", value: "end_of_term" },
          { label: "Specific Date", value: "specific_date" },
          { label: "End of Billing Term", value: "end_of_billing_term" }
        ]
      }
    }),
    cancel_at: import_pieces_framework2.Property.Number({
      displayName: "Cancel At (Unix Seconds)",
      description: 'UTC timestamp in seconds for when to cancel. Required when Cancel Option is "Specific Date".',
      required: !1
    }),
    cancel_reason_code: import_pieces_framework2.Property.ShortText({
      displayName: "Cancel Reason Code",
      description: "Reason code from Settings > Configure Chargebee > Reason Codes > Subscriptions > Subscription Cancellation. Case-sensitive.",
      required: !1
    }),
    credit_option_for_current_term_charges: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Credit Option for Current Term Charges",
      description: "How to handle credits for current term charges when canceling immediately.",
      required: !1,
      options: {
        options: [
          { label: "None", value: "none" },
          { label: "Prorate", value: "prorate" },
          { label: "Full", value: "full" }
        ]
      }
    }),
    unbilled_charges_option: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Unbilled Charges Option",
      description: "How to handle unbilled charges when canceling immediately.",
      required: !1,
      options: {
        options: [
          { label: "Invoice", value: "invoice" },
          { label: "Delete", value: "delete" }
        ]
      }
    }),
    contract_term_cancel_option: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Contract Term Cancel Option",
      description: "Required when the subscription has a contract term. Determines when to cancel the subscription and contract term.",
      required: !1,
      options: {
        options: [
          { label: "Terminate Immediately", value: "terminate_immediately" },
          { label: "End of Contract Term", value: "end_of_contract_term" },
          { label: "Specific Date", value: "specific_date" },
          {
            label: "End of Subscription Billing Term",
            value: "end_of_subscription_billing_term"
          }
        ]
      }
    }),
    account_receivables_handling: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Account Receivables Handling",
      description: "How to handle past due invoices when canceling immediately.",
      required: !1,
      options: {
        options: [
          { label: "No Action", value: "no_action" },
          {
            label: "Schedule Payment Collection",
            value: "schedule_payment_collection"
          },
          { label: "Write Off", value: "write_off" }
        ]
      }
    }),
    refundable_credits_handling: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Refundable Credits Handling",
      description: "How to handle refundable credits when canceling immediately.",
      required: !1,
      options: {
        options: [
          { label: "No Action", value: "no_action" },
          { label: "Schedule Refund", value: "schedule_refund" }
        ]
      }
    }),
    invoice_date: import_pieces_framework2.Property.Number({
      displayName: "Invoice Date (Unix Seconds)",
      description: "Document date displayed on the invoice PDF. Defaults to the current date. Provide to backdate the invoice.",
      required: !1
    }),
    decommissioned: import_pieces_framework2.Property.Checkbox({
      displayName: "Decommission",
      description: "When true, all subscription operations will be disabled except deletion. This is irreversible.",
      required: !1
    }),
    termination_fee_item_price_id: import_pieces_framework2.Property.ShortText({
      displayName: "Termination Fee Item Price ID",
      description: "The unique ID of the charge item price representing the termination fee. Used only when the subscription has a contract term.",
      required: !1
    }),
    termination_fee_quantity: import_pieces_framework2.Property.Number({
      displayName: "Termination Fee Quantity",
      description: "Quantity for the termination fee. Applicable only for quantity-based item prices.",
      required: !1
    }),
    termination_fee_unit_price: import_pieces_framework2.Property.Number({
      displayName: "Termination Fee Unit Price (cents)",
      description: "The termination fee in cents. For quantity-based items, this is the fee per unit.",
      required: !1
    }),
    termination_fee_service_period_days: import_pieces_framework2.Property.Number({
      displayName: "Termination Fee Service Period (Days)",
      description: "Service period of the termination fee in days, starting from the current date.",
      required: !1
    })
  },
  async run(context) {
    let {
      subscription_id,
      cancel_option,
      cancel_at,
      cancel_reason_code,
      credit_option_for_current_term_charges,
      unbilled_charges_option,
      contract_term_cancel_option,
      account_receivables_handling,
      refundable_credits_handling,
      invoice_date,
      decommissioned,
      termination_fee_item_price_id,
      termination_fee_quantity,
      termination_fee_unit_price,
      termination_fee_service_period_days
    } = context.propsValue, body = cleanObject({
      cancel_option,
      cancel_at,
      cancel_reason_code,
      credit_option_for_current_term_charges,
      unbilled_charges_option,
      contract_term_cancel_option,
      account_receivables_handling,
      refundable_credits_handling,
      invoice_date,
      decommissioned,
      "subscription_items[item_price_id][0]": termination_fee_item_price_id,
      "subscription_items[quantity][0]": termination_fee_quantity,
      "subscription_items[unit_price][0]": termination_fee_unit_price,
      "subscription_items[service_period_days][0]": termination_fee_service_period_days
    });
    return await chargebeeRequest({
      site: context.auth.props.site,
      apiKey: context.auth.props.api_key,
      method: import_pieces_common3.HttpMethod.POST,
      path: `/subscriptions/${encodeURIComponent(subscription_id)}/cancel_for_items`,
      contentType: "application/x-www-form-urlencoded",
      body
    });
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/actions/create-customer.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var createCustomer = (0, import_pieces_framework3.createAction)({
  name: "create_customer",
  classification: "WRITE",
  auth: chargebeeAuth,
  displayName: "Create Customer",
  description: "Create a customer in Chargebee.",
  audience: "both",
  aiMetadata: {
    description: "Create a new customer record in Chargebee, optionally with contact details, billing address, tax/VAT settings, and metadata. Use when an agent needs to register a customer before subscribing or invoicing them. All fields are optional, including the customer ID (auto-generated if omitted). Not idempotent \u2014 each call creates a new customer unless a custom ID is supplied that already exists.",
    idempotent: !1
  },
  props: {
    id: import_pieces_framework3.Property.ShortText({
      displayName: "Customer ID",
      description: "Optional custom customer identifier.",
      required: !1
    }),
    first_name: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    company: import_pieces_framework3.Property.ShortText({
      displayName: "Company",
      required: !1
    }),
    phone: import_pieces_framework3.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    locale: import_pieces_framework3.Property.ShortText({
      displayName: "Locale",
      description: "For example: en-US, fr-CA.",
      required: !1
    }),
    preferred_currency_code: import_pieces_framework3.Property.ShortText({
      displayName: "Preferred Currency Code",
      description: "ISO 4217 currency code (e.g. USD, EUR). Requires Multi-currency pricing to be enabled.",
      required: !1
    }),
    auto_collection: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Auto Collection",
      description: "Whether invoices should be automatically charged when possible.",
      required: !1,
      options: {
        options: [
          { label: "On", value: "on" },
          { label: "Off", value: "off" }
        ]
      }
    }),
    net_term_days: import_pieces_framework3.Property.Number({
      displayName: "Net Term Days",
      description: "Number of days within which the customer must pay their invoices.",
      required: !1
    }),
    allow_direct_debit: import_pieces_framework3.Property.Checkbox({
      displayName: "Allow Direct Debit",
      description: "Whether the customer can pay via Direct Debit.",
      required: !1
    }),
    vat_number: import_pieces_framework3.Property.ShortText({
      displayName: "VAT Number",
      description: "The VAT/tax registration number for the customer.",
      required: !1
    }),
    taxability: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Taxability",
      description: "Specifies if the customer is liable for tax.",
      required: !1,
      options: {
        options: [
          { label: "Taxable", value: "taxable" },
          { label: "Exempt", value: "exempt" }
        ]
      }
    }),
    invoice_notes: import_pieces_framework3.Property.LongText({
      displayName: "Invoice Notes",
      description: "A customer-facing note added to all invoices for this customer.",
      required: !1
    }),
    meta_data: import_pieces_framework3.Property.Object({
      displayName: "Metadata",
      description: "Key-value pairs to store extra information on the customer.",
      required: !1
    }),
    billing_address_first_name: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address: First Name",
      required: !1
    }),
    billing_address_last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address: Last Name",
      required: !1
    }),
    billing_address_line1: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address: Line 1",
      required: !1
    }),
    billing_address_line2: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address: Line 2",
      required: !1
    }),
    billing_address_city: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address: City",
      required: !1
    }),
    billing_address_state: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address: State",
      required: !1
    }),
    billing_address_zip: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address: ZIP / Postal Code",
      required: !1
    }),
    billing_address_country: import_pieces_framework3.Property.ShortText({
      displayName: "Billing Address: Country",
      description: "Two-letter ISO 3166-1 alpha-2 country code (e.g. US, GB).",
      required: !1
    })
  },
  async run(context) {
    let {
      id,
      first_name,
      last_name,
      email,
      company,
      phone,
      locale,
      preferred_currency_code,
      auto_collection,
      net_term_days,
      allow_direct_debit,
      vat_number,
      taxability,
      invoice_notes,
      meta_data,
      billing_address_first_name,
      billing_address_last_name,
      billing_address_line1,
      billing_address_line2,
      billing_address_city,
      billing_address_state,
      billing_address_zip,
      billing_address_country
    } = context.propsValue;
    return await chargebeeRequest({
      site: context.auth.props.site,
      apiKey: context.auth.props.api_key,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/customers",
      contentType: "application/x-www-form-urlencoded",
      body: cleanObject({
        id,
        first_name,
        last_name,
        email,
        company,
        phone,
        locale,
        preferred_currency_code,
        auto_collection,
        net_term_days,
        allow_direct_debit,
        vat_number,
        taxability,
        invoice_notes,
        meta_data: meta_data ? JSON.stringify(meta_data) : void 0,
        "billing_address[first_name]": billing_address_first_name,
        "billing_address[last_name]": billing_address_last_name,
        "billing_address[line1]": billing_address_line1,
        "billing_address[line2]": billing_address_line2,
        "billing_address[city]": billing_address_city,
        "billing_address[state]": billing_address_state,
        "billing_address[zip]": billing_address_zip,
        "billing_address[country]": billing_address_country
      })
    });
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/actions/create-subscription.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var createSubscription = (0, import_pieces_framework4.createAction)({
  name: "create_subscription",
  classification: "WRITE",
  auth: chargebeeAuth,
  displayName: "Create Subscription",
  description: "Create a subscription for an existing customer using a Chargebee item price.",
  audience: "both",
  aiMetadata: {
    description: "Create a new item-price-based subscription for an existing Chargebee customer. Use when an agent needs to start billing a known customer on a specific item price. Requires a valid customer ID and item price ID; optional fields control trial, billing cycles, coupons, and auto-collection. Not idempotent \u2014 each call creates a separate subscription.",
    idempotent: !1
  },
  props: {
    customer_id: import_pieces_framework4.Property.ShortText({
      displayName: "Customer ID",
      required: !0
    }),
    item_price_id: import_pieces_framework4.Property.ShortText({
      displayName: "Item Price ID",
      description: "The Chargebee item price ID to subscribe the customer to.",
      required: !0
    }),
    quantity: import_pieces_framework4.Property.Number({
      displayName: "Quantity",
      required: !1
    }),
    auto_collection: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Auto Collection",
      description: "Whether to automatically charge the customer. Defaults to the customer setting.",
      required: !1,
      options: {
        options: [
          { label: "On", value: "on" },
          { label: "Off", value: "off" }
        ]
      }
    }),
    billing_cycles: import_pieces_framework4.Property.Number({
      displayName: "Billing Cycles",
      description: "Number of billing cycles before the subscription ends. Defaults to the item price setting.",
      required: !1
    }),
    trial_end: import_pieces_framework4.Property.Number({
      displayName: "Trial End (Unix Seconds)",
      description: "UTC timestamp in seconds when the trial period ends. Set to 0 for no trial.",
      required: !1
    }),
    start_date: import_pieces_framework4.Property.Number({
      displayName: "Start Date (Unix Seconds)",
      description: "UTC timestamp in seconds when the subscription starts. Defaults to now. Provide a past value to backdate.",
      required: !1
    }),
    coupon_ids: import_pieces_framework4.Property.ShortText({
      displayName: "Coupon ID",
      description: "A coupon ID or code to apply to this subscription.",
      required: !1
    }),
    po_number: import_pieces_framework4.Property.ShortText({
      displayName: "PO Number",
      description: "Purchase order number to associate with this subscription.",
      required: !1
    }),
    invoice_notes: import_pieces_framework4.Property.LongText({
      displayName: "Invoice Notes",
      description: "Customer-facing note added to all invoices for this subscription.",
      required: !1
    }),
    payment_source_id: import_pieces_framework4.Property.ShortText({
      displayName: "Payment Source ID",
      description: "ID of the payment source to attach to this subscription.",
      required: !1
    }),
    invoice_immediately: import_pieces_framework4.Property.Checkbox({
      displayName: "Invoice Immediately",
      description: "Invoice any charges raised at creation time immediately rather than adding them to unbilled charges.",
      required: !1
    }),
    net_term_days: import_pieces_framework4.Property.Number({
      displayName: "Net Term Days",
      description: "Number of days within which invoices for this subscription must be paid (Net D). Must match a value configured in Chargebee.",
      required: !1
    }),
    meta_data: import_pieces_framework4.Property.Object({
      displayName: "Metadata",
      description: "Key-value pairs to store extra information on the subscription.",
      required: !1
    })
  },
  async run(context) {
    let {
      customer_id,
      item_price_id,
      quantity,
      auto_collection,
      billing_cycles,
      trial_end,
      start_date,
      coupon_ids,
      po_number,
      invoice_notes,
      payment_source_id,
      invoice_immediately,
      net_term_days,
      meta_data
    } = context.propsValue, body = cleanObject({
      auto_collection,
      billing_cycles,
      trial_end,
      start_date,
      "coupon_ids[0]": coupon_ids,
      po_number,
      invoice_notes,
      payment_source_id,
      invoice_immediately,
      net_term_days,
      meta_data: meta_data ? JSON.stringify(meta_data) : void 0,
      "subscription_items[item_price_id][0]": item_price_id,
      "subscription_items[quantity][0]": quantity
    });
    return await chargebeeRequest({
      site: context.auth.props.site,
      apiKey: context.auth.props.api_key,
      method: import_pieces_common5.HttpMethod.POST,
      path: `/customers/${customer_id}/subscription_for_items`,
      contentType: "application/x-www-form-urlencoded",
      body
    });
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/actions/get-customer.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var getCustomer = (0, import_pieces_framework5.createAction)({
  name: "get_customer",
  classification: "READ",
  auth: chargebeeAuth,
  displayName: "Get Customer",
  description: "Retrieve a customer from Chargebee by ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetch a single Chargebee customer record by its customer ID. Use when an agent needs to read a known customer's details before acting on them. Requires the exact customer ID; does not search by email or other fields. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    customer_id: import_pieces_framework5.Property.ShortText({
      displayName: "Customer ID",
      required: !0
    })
  },
  async run(context) {
    let { customer_id } = context.propsValue;
    return await chargebeeRequest({
      site: context.auth.props.site,
      apiKey: context.auth.props.api_key,
      method: import_pieces_common6.HttpMethod.GET,
      path: `/customers/${encodeURIComponent(customer_id)}`
    });
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/triggers/customer-created.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chargebee/src/lib/common/webhook.ts
var import_pieces_common7 = require("@activepieces/pieces-common");
async function subscribeChargebeeWebhook(site, apiKey, webhookUrl, eventType) {
  return (await chargebeeRequest({
    site,
    apiKey,
    method: import_pieces_common7.HttpMethod.POST,
    path: "/webhook_endpoints",
    contentType: "application/x-www-form-urlencoded",
    body: {
      webhook_url: webhookUrl,
      "subscribed_events[0]": eventType,
      is_active: !0
    }
  })).webhook.id;
}
async function unsubscribeChargebeeWebhook(site, apiKey, webhookId) {
  await chargebeeRequest({
    site,
    apiKey,
    method: import_pieces_common7.HttpMethod.POST,
    path: `/webhook_endpoints/${webhookId}/delete`
  });
}
async function fetchRecentEvents(site, apiKey, eventType) {
  var _a;
  return ((_a = (await chargebeeRequest({
    site,
    apiKey,
    method: import_pieces_common7.HttpMethod.GET,
    path: `/events?limit=5&event_type[is]=${eventType}`
  })).list) != null ? _a : []).map((item) => item.event);
}

// ../ap/packages/pieces/community/chargebee/src/lib/triggers/customer-created.ts
var EVENT_TYPE = "customer_created", STORE_KEY = "_chargebee_customer_created_webhook_id", customerCreated = (0, import_pieces_framework6.createTrigger)({
  auth: chargebeeAuth,
  name: "customer_created",
  classification: "READ",
  displayName: "Customer Created",
  description: "Triggers when a new customer is created in Chargebee.",
  aiMetadata: {
    description: "Fires when Chargebee records a customer_created event \u2014 a new customer record has been created. The payload carries the created customer."
  },
  props: {},
  type: import_pieces_framework6.TriggerStrategy.WEBHOOK,
  sampleData: {
    id: "ev_customercwwy6nq",
    occurred_at: 1776067864,
    source: "admin_console",
    object: "event",
    api_version: "v2",
    event_type: "customer_created",
    webhook_status: "not_applicable",
    content: {
      customer: {
        allow_direct_debit: !1,
        auto_collection: "on",
        billing_address: {
          city: "Walnut",
          country: "US",
          first_name: "John",
          last_name: "Doe",
          line1: "PO Box 9999",
          "...": "..."
        },
        card_status: "no_card",
        created_at: 1517505731,
        "...": "..."
      }
    }
  },
  async onEnable(context) {
    let webhookId = await subscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      context.webhookUrl,
      EVENT_TYPE
    );
    await context.store.put(STORE_KEY, webhookId);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(STORE_KEY);
    webhookId && await unsubscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      webhookId
    );
  },
  async test(context) {
    return fetchRecentEvents(
      context.auth.props.site,
      context.auth.props.api_key,
      EVENT_TYPE
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/triggers/invoice-generated.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var EVENT_TYPE2 = "invoice_generated", STORE_KEY2 = "_chargebee_invoice_generated_webhook_id", invoiceGenerated = (0, import_pieces_framework7.createTrigger)({
  auth: chargebeeAuth,
  name: "invoice_generated",
  classification: "READ",
  displayName: "Invoice Generated",
  description: "Triggers when a new invoice is generated for a subscription.",
  aiMetadata: {
    description: "Fires when Chargebee records an invoice_generated event \u2014 a new invoice has been created (for a subscription or one-time charge). The payload carries the generated invoice."
  },
  props: {},
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  sampleData: {
    id: "ev_invoicegewwy4m2",
    occurred_at: 1776067861,
    source: "admin_console",
    object: "event",
    api_version: "v2",
    event_type: "invoice_generated",
    webhook_status: "not_applicable",
    content: {
      invoice: {
        adjustment_credit_notes: {},
        amount_adjusted: 0,
        amount_due: 0,
        amount_paid: 1e3,
        amount_to_collect: 0,
        "...": "..."
      }
    }
  },
  async onEnable(context) {
    let webhookId = await subscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      context.webhookUrl,
      EVENT_TYPE2
    );
    await context.store.put(STORE_KEY2, webhookId);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(STORE_KEY2);
    webhookId && await unsubscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      webhookId
    );
  },
  async test(context) {
    return fetchRecentEvents(
      context.auth.props.site,
      context.auth.props.api_key,
      EVENT_TYPE2
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/triggers/payment-failed.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var EVENT_TYPE3 = "payment_failed", STORE_KEY3 = "_chargebee_payment_failed_webhook_id", paymentFailed = (0, import_pieces_framework8.createTrigger)({
  auth: chargebeeAuth,
  name: "payment_failed",
  classification: "READ",
  displayName: "Payment Failed",
  description: "Triggers when a payment attempt fails. Useful for dunning and retry workflows.",
  aiMetadata: {
    description: "Fires when Chargebee records a payment_failed event \u2014 an attempt to collect a payment failed. Useful for driving dunning and retry workflows. The payload carries the failed transaction with the related invoice, customer, and subscription."
  },
  props: {},
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  sampleData: {
    id: "ev_paymentfawwy6gd",
    occurred_at: 1776067863,
    source: "admin_console",
    object: "event",
    api_version: "v2",
    event_type: "payment_failed",
    webhook_status: "not_applicable",
    content: {
      transaction: {
        amount: 1395,
        amount_unused: 0,
        currency_code: "USD",
        customer_id: "__test__KyVnHhSBWlv242pN",
        date: 1517505921,
        "...": "..."
      },
      invoice: {
        adjustment_credit_notes: {},
        amount_adjusted: 0,
        amount_due: 0,
        amount_paid: 1e3,
        amount_to_collect: 0,
        "...": "..."
      },
      customer: {
        allow_direct_debit: !1,
        auto_collection: "on",
        billing_address: {
          city: "Walnut",
          country: "US",
          first_name: "John",
          last_name: "Doe",
          line1: "PO Box 9999",
          "...": "..."
        },
        card_status: "no_card",
        created_at: 1517505731,
        "...": "..."
      },
      subscription: {
        activated_at: 1612890920,
        billing_period: 1,
        billing_period_unit: "month",
        created_at: 1612890920,
        currency_code: "USD",
        "...": "..."
      }
    }
  },
  async onEnable(context) {
    let webhookId = await subscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      context.webhookUrl,
      EVENT_TYPE3
    );
    await context.store.put(STORE_KEY3, webhookId);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(STORE_KEY3);
    webhookId && await unsubscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      webhookId
    );
  },
  async test(context) {
    return fetchRecentEvents(
      context.auth.props.site,
      context.auth.props.api_key,
      EVENT_TYPE3
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/triggers/payment-succeeded.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var EVENT_TYPE4 = "payment_succeeded", STORE_KEY4 = "_chargebee_payment_succeeded_webhook_id", paymentSucceeded = (0, import_pieces_framework9.createTrigger)({
  auth: chargebeeAuth,
  name: "payment_succeeded",
  classification: "READ",
  displayName: "Payment Succeeded",
  description: "Triggers when a payment is successfully collected.",
  aiMetadata: {
    description: "Fires when Chargebee records a payment_succeeded event \u2014 a payment was successfully collected. The payload carries the transaction along with the related invoice, customer, and subscription."
  },
  props: {},
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  sampleData: {
    id: "ev_paymentsuwwy8bw",
    occurred_at: 1776067866,
    source: "admin_console",
    object: "event",
    api_version: "v2",
    event_type: "payment_succeeded",
    webhook_status: "not_applicable",
    content: {
      transaction: {
        amount: 1395,
        amount_unused: 0,
        currency_code: "USD",
        customer_id: "__test__KyVnHhSBWlv242pN",
        date: 1517505921,
        "...": "..."
      },
      invoice: {
        adjustment_credit_notes: {},
        amount_adjusted: 0,
        amount_due: 0,
        amount_paid: 1e3,
        amount_to_collect: 0,
        "...": "..."
      },
      customer: {
        allow_direct_debit: !1,
        auto_collection: "on",
        billing_address: {
          city: "Walnut",
          country: "US",
          first_name: "John",
          last_name: "Doe",
          line1: "PO Box 9999",
          "...": "..."
        },
        card_status: "no_card",
        created_at: 1517505731,
        "...": "..."
      },
      subscription: {
        activated_at: 1612890920,
        billing_period: 1,
        billing_period_unit: "month",
        created_at: 1612890920,
        currency_code: "USD",
        "...": "..."
      }
    }
  },
  async onEnable(context) {
    let webhookId = await subscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      context.webhookUrl,
      EVENT_TYPE4
    );
    await context.store.put(STORE_KEY4, webhookId);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(STORE_KEY4);
    webhookId && await unsubscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      webhookId
    );
  },
  async test(context) {
    return fetchRecentEvents(
      context.auth.props.site,
      context.auth.props.api_key,
      EVENT_TYPE4
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/triggers/subscription-cancelled.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var EVENT_TYPE5 = "subscription_cancelled", STORE_KEY5 = "_chargebee_subscription_cancelled_webhook_id", subscriptionCancelled = (0, import_pieces_framework10.createTrigger)({
  auth: chargebeeAuth,
  name: "subscription_cancelled",
  classification: "READ",
  displayName: "Subscription Cancelled",
  description: "Triggers when a subscription is cancelled.",
  aiMetadata: {
    description: "Fires when Chargebee records a subscription_cancelled event \u2014 a subscription has been cancelled (immediately or at end of term). The payload carries the cancelled subscription with its customer and invoice."
  },
  props: {},
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  sampleData: {
    id: "ev_subscriptiwwy3vs",
    occurred_at: 1776067860,
    source: "admin_console",
    object: "event",
    api_version: "v2",
    event_type: "subscription_cancelled",
    webhook_status: "not_applicable",
    content: {
      subscription: {
        activated_at: 1612890920,
        billing_period: 1,
        billing_period_unit: "month",
        created_at: 1612890920,
        currency_code: "USD",
        "...": "..."
      },
      customer: {
        allow_direct_debit: !1,
        auto_collection: "on",
        billing_address: {
          city: "Walnut",
          country: "US",
          first_name: "John",
          last_name: "Doe",
          line1: "PO Box 9999",
          "...": "..."
        },
        card_status: "no_card",
        created_at: 1517505731,
        "...": "..."
      },
      invoice: {
        adjustment_credit_notes: {},
        amount_adjusted: 0,
        amount_due: 0,
        amount_paid: 1e3,
        amount_to_collect: 0,
        "...": "..."
      }
    }
  },
  async onEnable(context) {
    let webhookId = await subscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      context.webhookUrl,
      EVENT_TYPE5
    );
    await context.store.put(STORE_KEY5, webhookId);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(STORE_KEY5);
    webhookId && await unsubscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      webhookId
    );
  },
  async test(context) {
    return fetchRecentEvents(
      context.auth.props.site,
      context.auth.props.api_key,
      EVENT_TYPE5
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/triggers/subscription-changed.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var EVENT_TYPE6 = "subscription_changed", STORE_KEY6 = "_chargebee_subscription_changed_webhook_id", subscriptionChanged = (0, import_pieces_framework11.createTrigger)({
  auth: chargebeeAuth,
  name: "subscription_changed",
  classification: "READ",
  displayName: "Subscription Changed",
  description: "Triggers when a subscription is updated \u2014 plan change, quantity, or add-ons.",
  aiMetadata: {
    description: "Fires when Chargebee records a subscription_changed event \u2014 an existing subscription was modified (plan/item price change, quantity, or add-ons). The payload carries the updated subscription with its customer and invoice."
  },
  props: {},
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  sampleData: {
    id: "ev_subscriptiwwy6f7",
    occurred_at: 1776067863,
    source: "admin_console",
    object: "event",
    api_version: "v2",
    event_type: "subscription_changed",
    webhook_status: "not_applicable",
    content: {
      subscription: {
        activated_at: 1612890920,
        billing_period: 1,
        billing_period_unit: "month",
        created_at: 1612890920,
        currency_code: "USD",
        "...": "..."
      },
      customer: {
        allow_direct_debit: !1,
        auto_collection: "on",
        billing_address: {
          city: "Walnut",
          country: "US",
          first_name: "John",
          last_name: "Doe",
          line1: "PO Box 9999",
          "...": "..."
        },
        card_status: "no_card",
        created_at: 1517505731,
        "...": "..."
      },
      invoice: {
        adjustment_credit_notes: {},
        amount_adjusted: 0,
        amount_due: 0,
        amount_paid: 1e3,
        amount_to_collect: 0,
        "...": "..."
      }
    }
  },
  async onEnable(context) {
    let webhookId = await subscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      context.webhookUrl,
      EVENT_TYPE6
    );
    await context.store.put(STORE_KEY6, webhookId);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(STORE_KEY6);
    webhookId && await unsubscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      webhookId
    );
  },
  async test(context) {
    return fetchRecentEvents(
      context.auth.props.site,
      context.auth.props.api_key,
      EVENT_TYPE6
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/triggers/subscription-created.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var EVENT_TYPE7 = "subscription_created", STORE_KEY7 = "_chargebee_subscription_created_webhook_id", subscriptionCreated = (0, import_pieces_framework12.createTrigger)({
  auth: chargebeeAuth,
  name: "subscription_created",
  classification: "READ",
  displayName: "Subscription Created",
  description: "Triggers when a new subscription is created for a customer.",
  aiMetadata: {
    description: "Fires when Chargebee records a subscription_created event \u2014 a new subscription has been created for a customer. The payload carries the subscription along with its customer and (if applicable) invoice."
  },
  props: {},
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  sampleData: {
    id: "ev_subscriptiwwy7vs",
    occurred_at: 1776067865,
    source: "admin_console",
    object: "event",
    api_version: "v2",
    event_type: "subscription_created",
    webhook_status: "not_applicable",
    content: {
      subscription: {
        activated_at: 1612890920,
        billing_period: 1,
        billing_period_unit: "month",
        created_at: 1612890920,
        currency_code: "USD",
        "...": "..."
      },
      customer: {
        allow_direct_debit: !1,
        auto_collection: "on",
        billing_address: {
          city: "Walnut",
          country: "US",
          first_name: "John",
          last_name: "Doe",
          line1: "PO Box 9999",
          "...": "..."
        },
        card_status: "no_card",
        created_at: 1517505731,
        "...": "..."
      },
      invoice: {
        adjustment_credit_notes: {},
        amount_adjusted: 0,
        amount_due: 0,
        amount_paid: 1e3,
        amount_to_collect: 0,
        "...": "..."
      }
    }
  },
  async onEnable(context) {
    let webhookId = await subscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      context.webhookUrl,
      EVENT_TYPE7
    );
    await context.store.put(STORE_KEY7, webhookId);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(STORE_KEY7);
    webhookId && await unsubscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      webhookId
    );
  },
  async test(context) {
    return fetchRecentEvents(
      context.auth.props.site,
      context.auth.props.api_key,
      EVENT_TYPE7
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/chargebee/src/lib/triggers/subscription-renewed.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var EVENT_TYPE8 = "subscription_renewed", STORE_KEY8 = "_chargebee_subscription_renewed_webhook_id", subscriptionRenewed = (0, import_pieces_framework13.createTrigger)({
  auth: chargebeeAuth,
  name: "subscription_renewed",
  classification: "READ",
  displayName: "Subscription Renewed",
  description: "Triggers when a subscription is successfully renewed.",
  aiMetadata: {
    description: "Fires when Chargebee records a subscription_renewed event \u2014 a subscription has advanced into a new billing term. The payload carries the renewed subscription with its customer and invoice."
  },
  props: {},
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  sampleData: {
    id: "ev_subscriptiwwy5ex",
    occurred_at: 1776067862,
    source: "admin_console",
    object: "event",
    api_version: "v2",
    event_type: "subscription_renewed",
    webhook_status: "not_applicable",
    content: {
      subscription: {
        activated_at: 1612890920,
        billing_period: 1,
        billing_period_unit: "month",
        created_at: 1612890920,
        currency_code: "USD",
        "...": "..."
      },
      customer: {
        allow_direct_debit: !1,
        auto_collection: "on",
        billing_address: {
          city: "Walnut",
          country: "US",
          first_name: "John",
          last_name: "Doe",
          line1: "PO Box 9999",
          "...": "..."
        },
        card_status: "no_card",
        created_at: 1517505731,
        "...": "..."
      },
      invoice: {
        adjustment_credit_notes: {},
        amount_adjusted: 0,
        amount_due: 0,
        amount_paid: 1e3,
        amount_to_collect: 0,
        "...": "..."
      }
    }
  },
  async onEnable(context) {
    let webhookId = await subscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      context.webhookUrl,
      EVENT_TYPE8
    );
    await context.store.put(STORE_KEY8, webhookId);
  },
  async onDisable(context) {
    let webhookId = await context.store.get(STORE_KEY8);
    webhookId && await unsubscribeChargebeeWebhook(
      context.auth.props.site,
      context.auth.props.api_key,
      webhookId
    );
  },
  async test(context) {
    return fetchRecentEvents(
      context.auth.props.site,
      context.auth.props.api_key,
      EVENT_TYPE8
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/chargebee/src/index.ts
var chargebee = (0, import_pieces_framework14.createPiece)({
  displayName: "Chargebee",
  description: "Subscription billing and revenue operations platform for managing customers and subscriptions.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/chargebee.png",
  authors: ["Harmatta", "sanket-a11y"],
  categories: [import_pieces_framework15.PieceCategory.ACCOUNTING, import_pieces_framework15.PieceCategory.PAYMENT_PROCESSING],
  auth: chargebeeAuth,
  actions: [
    createSubscription,
    cancelSubscription,
    createCustomer,
    getCustomer,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: chargebeeAuth,
      baseUrl: (auth) => {
        var _a;
        return getChargebeeBaseUrl((_a = auth == null ? void 0 : auth.props.site) != null ? _a : "");
      },
      authMapping: async (auth) => {
        var _a, _b;
        return {
          Authorization: getChargebeeAuthHeader((_b = (_a = auth == null ? void 0 : auth.props) == null ? void 0 : _a.api_key) != null ? _b : "")
        };
      }
    })
  ],
  triggers: [
    subscriptionCreated,
    subscriptionCancelled,
    subscriptionRenewed,
    subscriptionChanged,
    paymentSucceeded,
    paymentFailed,
    invoiceGenerated,
    customerCreated
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  chargebee
});
