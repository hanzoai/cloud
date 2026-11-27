"use strict";
var __defProp = Object.defineProperty, __defProps = Object.defineProperties, __getOwnPropDesc = Object.getOwnPropertyDescriptor, __getOwnPropDescs = Object.getOwnPropertyDescriptors, __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
}, __spreadProps = (a, b) => __defProps(a, __getOwnPropDescs(b));
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

// ../ap/packages/pieces/community/paddle/src/index.ts
var index_exports = {};
__export(index_exports, {
  paddle: () => paddle
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_framework16 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/paddle/src/lib/actions/cancel-subscription.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/paddle/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/paddle/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function validateAuth({
  apiKey
}) {
  await sendRequestWithApiKey({
    apiKey,
    method: import_pieces_common.HttpMethod.GET,
    path: "/customers",
    queryParams: {
      per_page: "1"
    }
  });
}
async function listCustomers({
  auth,
  limit = DEFAULT_PAGE_SIZE,
  email,
  status
}) {
  return collectPaginatedItems({
    auth,
    path: "/customers",
    limit,
    queryParams: __spreadValues(__spreadValues({}, email ? { email } : {}), status ? { status } : {})
  });
}
async function listSubscriptions({
  auth,
  limit = DEFAULT_PAGE_SIZE,
  customerId,
  status
}) {
  return collectPaginatedItems({
    auth,
    path: "/subscriptions",
    limit,
    queryParams: __spreadValues(__spreadValues({}, customerId ? { customer_id: customerId } : {}), status ? { status } : {})
  });
}
async function listPrices({
  auth,
  limit = DEFAULT_PAGE_SIZE,
  recurring = !0
}) {
  return collectPaginatedItems({
    auth,
    path: "/prices",
    limit,
    queryParams: __spreadValues({
      status: "active"
    }, recurring ? { recurring: "true" } : {})
  });
}
async function getSubscription({
  auth,
  subscriptionId
}) {
  return (await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.GET,
    path: `/subscriptions/${encodeURIComponent(subscriptionId)}`
  })).data;
}
async function updateSubscription({
  auth,
  subscriptionId,
  request
}) {
  return (await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.PATCH,
    path: `/subscriptions/${encodeURIComponent(subscriptionId)}`,
    body: request
  })).data;
}
async function listAddresses({
  auth,
  customerId
}) {
  return (await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.GET,
    path: `/customers/${encodeURIComponent(customerId)}/addresses`
  })).data;
}
async function createNotificationSetting({
  auth,
  url,
  subscribedEvents
}) {
  return (await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.POST,
    path: "/notification-settings",
    body: {
      type: "url",
      destination: url,
      subscribed_events: subscribedEvents,
      active: !0,
      description: "Activepieces"
    }
  })).data;
}
async function deleteNotificationSetting({
  auth,
  notificationSettingId
}) {
  await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.DELETE,
    path: `/notification-settings/${encodeURIComponent(notificationSettingId)}`
  });
}
async function cancelSubscription({
  auth,
  subscriptionId,
  request
}) {
  return (await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.POST,
    path: `/subscriptions/${encodeURIComponent(subscriptionId)}/cancel`,
    body: request
  })).data;
}
async function createTransaction({
  auth,
  request
}) {
  return (await sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.POST,
    path: "/transactions",
    body: request
  })).data;
}
function getBaseUrl({
  apiKey
}) {
  return apiKey.includes("_sdbx_") ? PADDLE_SANDBOX_API_BASE_URL : PADDLE_LIVE_API_BASE_URL;
}
async function collectPaginatedItems({
  auth,
  path,
  limit,
  queryParams
}) {
  var _a, _b, _c;
  let items = [], after;
  for (; items.length < limit; ) {
    let response = await sendRequest({
      auth,
      method: import_pieces_common.HttpMethod.GET,
      path,
      queryParams: __spreadValues(__spreadProps(__spreadValues({}, queryParams), {
        per_page: String(Math.min(limit - items.length, PADDLE_MAX_PAGE_SIZE))
      }), after ? { after } : {})
    });
    items.push(...response.data);
    let nextAfter = getNextAfter({
      nextUrl: (_c = (_b = (_a = response.meta) == null ? void 0 : _a.pagination) == null ? void 0 : _b.next) != null ? _c : null
    });
    if (!nextAfter || response.data.length === 0)
      break;
    after = nextAfter;
  }
  return items;
}
async function sendRequest({
  auth,
  method,
  path,
  body,
  queryParams
}) {
  return sendRequestWithApiKey({
    apiKey: auth.secret_text,
    method,
    path,
    body,
    queryParams
  });
}
async function sendRequestWithApiKey({
  apiKey,
  method,
  path,
  body,
  queryParams
}) {
  let request = __spreadValues(__spreadValues({
    method,
    url: `${getBaseUrl({ apiKey })}${path}`,
    headers: buildHeaders({
      apiKey
    })
  }, body ? { body } : {}), queryParams ? { queryParams } : {});
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}
function buildHeaders({
  apiKey
}) {
  return {
    Accept: "application/json",
    Authorization: `Bearer ${apiKey}`,
    "Content-Type": "application/json",
    "Paddle-Version": "1"
  };
}
function getNextAfter({
  nextUrl
}) {
  var _a;
  if (nextUrl)
    return (_a = new URL(nextUrl).searchParams.get("after")) != null ? _a : void 0;
}
var DEFAULT_PAGE_SIZE = 100, PADDLE_MAX_PAGE_SIZE = 200, PADDLE_LIVE_API_BASE_URL = "https://api.paddle.com", PADDLE_SANDBOX_API_BASE_URL = "https://sandbox-api.paddle.com", paddleClient = {
  cancelSubscription,
  createNotificationSetting,
  createTransaction,
  deleteNotificationSetting,
  getBaseUrl,
  getSubscription,
  listAddresses,
  listCustomers,
  listPrices,
  listSubscriptions,
  updateSubscription,
  validateAuth
};

// ../ap/packages/pieces/community/paddle/src/lib/auth.ts
var paddleAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `Authenticate with a Paddle API key.

Live and sandbox keys are supported automatically.

You can create API keys from **Developer Tools > Authentication** in the Paddle dashboard.`,
  required: !0,
  validate: async ({ auth }) => {
    let { error } = await (0, import_pieces_framework2.tryCatch)(
      () => paddleClient.validateAuth({
        apiKey: auth
      })
    );
    return error ? {
      valid: !1,
      error: "Invalid API key. Check the key and try again."
    } : {
      valid: !0
    };
  }
});

// ../ap/packages/pieces/community/paddle/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");
function customer(required = !0) {
  return import_pieces_framework3.Property.Dropdown({
    displayName: "Customer",
    description: "Select the Paddle customer to use.",
    required,
    refreshers: ["auth"],
    auth: paddleAuth,
    options: async ({ auth, searchValue }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Connect your Paddle account first."
        };
      let normalizedSearchValue = normalizeSearchValue({
        searchValue
      }), { data: customers, error } = await (0, import_pieces_framework4.tryCatch)(
        () => paddleClient.listCustomers(__spreadValues({
          auth,
          limit: DEFAULT_DROPDOWN_LIMIT
        }, normalizedSearchValue != null && normalizedSearchValue.includes("@") ? { email: normalizedSearchValue } : {}))
      );
      if (error)
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load customers. Check your connection."
        };
      let filteredCustomers = filterCustomers({
        customers,
        searchValue: normalizedSearchValue
      });
      return filteredCustomers.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No customers found in Paddle."
      } : {
        disabled: !1,
        options: filteredCustomers.map((customerItem) => ({
          label: buildCustomerLabel({
            customer: customerItem
          }),
          value: customerItem.id
        }))
      };
    }
  });
}
function address(required = !0) {
  return import_pieces_framework3.Property.Dropdown({
    displayName: "Address",
    description: "Select the Paddle address to bill against.",
    required,
    refreshers: ["auth", "customerId"],
    auth: paddleAuth,
    options: async ({ auth, customerId, searchValue }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Connect your Paddle account first."
        };
      if (!customerId || typeof customerId != "string")
        return {
          disabled: !0,
          options: [],
          placeholder: "Select a customer first."
        };
      let normalizedSearchValue = normalizeSearchValue({
        searchValue
      }), { data: addresses, error } = await (0, import_pieces_framework4.tryCatch)(
        () => paddleClient.listAddresses({
          auth,
          customerId
        })
      );
      if (error)
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load addresses. Check your connection."
        };
      let filteredAddresses = filterAddresses({
        addresses,
        searchValue: normalizedSearchValue
      });
      return filteredAddresses.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No addresses found in Paddle."
      } : {
        disabled: !1,
        options: filteredAddresses.map((addressItem) => ({
          label: buildAddressLabel({
            address: addressItem
          }),
          value: addressItem.id
        }))
      };
    }
  });
}
function subscription(required = !0) {
  return import_pieces_framework3.Property.Dropdown({
    displayName: "Subscription",
    description: "Select the Paddle subscription to use.",
    required,
    refreshers: ["auth"],
    auth: paddleAuth,
    options: async ({ auth, searchValue }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Connect your Paddle account first."
        };
      let { data: subscriptions, error } = await (0, import_pieces_framework4.tryCatch)(
        () => paddleClient.listSubscriptions({
          auth,
          limit: DEFAULT_DROPDOWN_LIMIT
        })
      );
      if (error)
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load subscriptions. Check your connection."
        };
      let filteredSubscriptions = filterSubscriptions({
        subscriptions,
        searchValue: normalizeSearchValue({
          searchValue
        })
      });
      return filteredSubscriptions.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No subscriptions found in Paddle."
      } : {
        disabled: !1,
        options: filteredSubscriptions.map((subscriptionItem) => ({
          label: buildSubscriptionLabel({
            subscription: subscriptionItem
          }),
          value: subscriptionItem.id
        }))
      };
    }
  });
}
function recurringPrice(required = !0) {
  return import_pieces_framework3.Property.Dropdown({
    displayName: "Recurring Price",
    description: "Select an active recurring Paddle price.",
    required,
    refreshers: ["auth"],
    auth: paddleAuth,
    options: async ({ auth, searchValue }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Connect your Paddle account first."
        };
      let { data: prices, error } = await (0, import_pieces_framework4.tryCatch)(
        () => paddleClient.listPrices({
          auth,
          limit: DEFAULT_DROPDOWN_LIMIT,
          recurring: !0
        })
      );
      if (error)
        return {
          disabled: !0,
          options: [],
          placeholder: "Failed to load prices. Check your connection."
        };
      let filteredPrices = filterPrices({
        prices,
        searchValue: normalizeSearchValue({
          searchValue
        })
      });
      return filteredPrices.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No active recurring prices found in Paddle."
      } : {
        disabled: !1,
        options: filteredPrices.map((price) => ({
          label: buildPriceLabel({
            price
          }),
          value: price.id
        }))
      };
    }
  });
}
function filterAddresses({
  addresses,
  searchValue
}) {
  return searchValue ? addresses.filter(
    (addressItem) => buildAddressLabel({
      address: addressItem
    }).toLowerCase().includes(searchValue)
  ) : addresses;
}
function buildAddressLabel({
  address: address2
}) {
  let parts = [
    address2.country_code,
    address2.region,
    address2.postal_code
  ].filter((part) => part != null && part.trim().length > 0);
  return parts.length > 0 ? parts.join(", ") : address2.id;
}
function normalizeSearchValue({
  searchValue
}) {
  if (typeof searchValue != "string")
    return;
  let normalizedValue = searchValue.trim().toLowerCase();
  return normalizedValue.length > 0 ? normalizedValue : void 0;
}
function filterCustomers({
  customers,
  searchValue
}) {
  return searchValue ? customers.filter(
    (customer2) => buildCustomerLabel({
      customer: customer2
    }).toLowerCase().includes(searchValue)
  ) : customers;
}
function filterSubscriptions({
  subscriptions,
  searchValue
}) {
  return searchValue ? subscriptions.filter(
    (subscriptionItem) => buildSubscriptionLabel({
      subscription: subscriptionItem
    }).toLowerCase().includes(searchValue)
  ) : subscriptions;
}
function filterPrices({
  prices,
  searchValue
}) {
  return searchValue ? prices.filter(
    (price) => buildPriceLabel({
      price
    }).toLowerCase().includes(searchValue)
  ) : prices;
}
function buildCustomerLabel({
  customer: customer2
}) {
  var _a, _b, _c;
  let name = (_a = customer2.name) == null ? void 0 : _a.trim(), email = (_b = customer2.email) == null ? void 0 : _b.trim(), status = (_c = customer2.status) != null ? _c : "unknown";
  return name && email ? `${name} (${email}) - ${status}` : email ? `${email} - ${status}` : `${customer2.id} - ${status}`;
}
function buildSubscriptionLabel({
  subscription: subscription2
}) {
  var _a, _b, _c, _d, _e, _f;
  let firstItemName = (_c = (_b = (_a = subscription2.items) == null ? void 0 : _a[0]) == null ? void 0 : _b.price) == null ? void 0 : _c.name;
  return firstItemName ? `${subscription2.id} (${(_d = subscription2.status) != null ? _d : "unknown"}, ${firstItemName})` : subscription2.customer_id ? `${subscription2.id} (${(_e = subscription2.status) != null ? _e : "unknown"}, ${subscription2.customer_id})` : `${subscription2.id} (${(_f = subscription2.status) != null ? _f : "unknown"})`;
}
function buildPriceLabel({
  price
}) {
  var _a, _b, _c, _d;
  let name = ((_a = price.name) == null ? void 0 : _a.trim()) || ((_b = price.description) == null ? void 0 : _b.trim()) || price.id, amount = (_c = price.unit_price) == null ? void 0 : _c.amount, currencyCode = (_d = price.unit_price) == null ? void 0 : _d.currency_code;
  return amount && currencyCode ? `${name} (${amount} ${currencyCode})` : name;
}
var DEFAULT_DROPDOWN_LIMIT = 100, paddleProps = {
  customer,
  recurringPrice,
  subscription,
  address
};

// ../ap/packages/pieces/community/paddle/src/lib/common/utils.ts
function getRequiredString({
  value,
  fieldName
}) {
  if (typeof value != "string" || value.trim().length === 0)
    throw new Error(`${fieldName} is required.`);
  return value.trim();
}
function getOptionalString({
  value
}) {
  if (typeof value != "string")
    return;
  let trimmedValue = value.trim();
  return trimmedValue.length > 0 ? trimmedValue : void 0;
}
function getOptionalPositiveInteger({
  value,
  fieldName
}) {
  if (value != null) {
    if (typeof value != "number" || !Number.isInteger(value) || value <= 0)
      throw new Error(`${fieldName} must be a positive integer.`);
    return value;
  }
}
function getOptionalObject({
  value,
  fieldName
}) {
  if (value != null) {
    if (!isRecord(value))
      throw new Error(`${fieldName} must be a JSON object.`);
    return value;
  }
}
function getLineItems({
  value,
  fieldName
}) {
  if (!Array.isArray(value) || value.length === 0)
    throw new Error(`${fieldName} must be a non-empty array.`);
  return value.map((lineItem, index) => {
    if (!isRecord(lineItem))
      throw new Error(`${fieldName} item ${index + 1} must be a JSON object.`);
    let priceId = getRequiredString({
      value: lineItem.price_id,
      fieldName: `${fieldName} item ${index + 1} price_id`
    }), quantity = getOptionalPositiveInteger({
      value: lineItem.quantity,
      fieldName: `${fieldName} item ${index + 1} quantity`
    });
    return quantity === void 0 ? {
      price_id: priceId
    } : {
      price_id: priceId,
      quantity
    };
  });
}
function compactRecord(value) {
  return Object.fromEntries(
    Object.entries(value).filter(([, entryValue]) => entryValue !== void 0)
  );
}
function isRecord(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}
var paddleUtils = {
  compactRecord,
  getLineItems,
  getOptionalObject,
  getOptionalPositiveInteger,
  getOptionalString,
  getRequiredString
};

// ../ap/packages/pieces/community/paddle/src/lib/actions/cancel-subscription.ts
var cancelSubscriptionAction = (0, import_pieces_framework5.createAction)({
  auth: paddleAuth,
  name: "cancel-subscription",
  displayName: "Cancel Subscription",
  description: "Cancels a Paddle subscription immediately or at the next billing period.",
  audience: "both",
  aiMetadata: {
    description: "Cancels a Paddle subscription by ID, applying the cancellation either immediately or at the end of the current billing period via the effective-from option. Use to end a subscription; this changes billing state and is not idempotent (repeating it on an already-canceled subscription will error or re-apply).",
    idempotent: !1
  },
  props: {
    subscriptionId: paddleProps.subscription(),
    effectiveFrom: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Effective From",
      description: "Choose when Paddle should apply the cancellation.",
      required: !1,
      options: {
        options: [
          {
            label: "Immediately",
            value: "immediately"
          },
          {
            label: "Next Billing Period",
            value: "next_billing_period"
          }
        ]
      }
    })
  },
  async run(context) {
    let subscriptionId = paddleUtils.getRequiredString({
      value: context.propsValue.subscriptionId,
      fieldName: "Subscription"
    }), effectiveFrom = paddleUtils.getOptionalString({
      value: context.propsValue.effectiveFrom
    });
    return paddleClient.cancelSubscription({
      auth: context.auth,
      subscriptionId,
      request: paddleUtils.compactRecord({
        effective_from: effectiveFrom
      })
    });
  }
});

// ../ap/packages/pieces/community/paddle/src/lib/actions/create-transaction.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var createTransactionAction = (0, import_pieces_framework6.createAction)({
  auth: paddleAuth,
  name: "create-transaction",
  displayName: "Create Transaction",
  description: "Creates a Paddle transaction. When you use recurring prices, completing the transaction starts the subscription.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new Paddle transaction for a customer against a recurring price (with optional quantity and custom data); since the price is recurring, completing the transaction begins the associated subscription. Requires a valid customer ID, billing address ID, and recurring price ID. Each call creates a separate transaction, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    customerId: paddleProps.customer(),
    addressId: paddleProps.address(),
    priceId: paddleProps.recurringPrice(),
    quantity: import_pieces_framework6.Property.Number({
      displayName: "Quantity",
      description: "Optional quantity for the recurring price.",
      required: !1
    }),
    customData: import_pieces_framework6.Property.Json({
      displayName: "Custom Data",
      description: "Optional JSON object to store on the transaction.",
      required: !1
    })
  },
  async run(context) {
    let customerId = paddleUtils.getRequiredString({
      value: context.propsValue.customerId,
      fieldName: "Customer"
    }), addressId = paddleUtils.getRequiredString({
      value: context.propsValue.addressId,
      fieldName: "Address ID"
    }), priceId = paddleUtils.getRequiredString({
      value: context.propsValue.priceId,
      fieldName: "Recurring Price"
    }), quantity = paddleUtils.getOptionalPositiveInteger({
      value: context.propsValue.quantity,
      fieldName: "Quantity"
    }), customData = paddleUtils.getOptionalObject({
      value: context.propsValue.customData,
      fieldName: "Custom Data"
    });
    return paddleClient.createTransaction({
      auth: context.auth,
      request: paddleUtils.compactRecord({
        address_id: addressId,
        customer_id: customerId,
        custom_data: customData,
        items: [
          paddleUtils.compactRecord({
            price_id: priceId,
            quantity
          })
        ]
      })
    });
  }
});

// ../ap/packages/pieces/community/paddle/src/lib/actions/get-subscription.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var getSubscriptionAction = (0, import_pieces_framework7.createAction)({
  auth: paddleAuth,
  name: "get-subscription",
  displayName: "Get Subscription",
  description: "Retrieves a Paddle subscription by ID.",
  audience: "both",
  aiMetadata: {
    description: "Fetches a single Paddle subscription by its subscription ID (sub_...) to inspect its status, items, and customer. Use when you already have the subscription ID and need its current details. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    subscriptionId: paddleProps.subscription()
  },
  async run(context) {
    let subscriptionId = paddleUtils.getRequiredString({
      value: context.propsValue.subscriptionId,
      fieldName: "Subscription"
    });
    return paddleClient.getSubscription({
      auth: context.auth,
      subscriptionId
    });
  }
});

// ../ap/packages/pieces/community/paddle/src/lib/actions/list-customers.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var listCustomersAction = (0, import_pieces_framework8.createAction)({
  auth: paddleAuth,
  name: "list-customers",
  displayName: "List Customers",
  description: "Lists customers from your Paddle account.",
  audience: "both",
  aiMetadata: {
    description: "Lists customers in a Paddle Billing account, returning all customers by default or filtering when an exact email and/or status (active/archived) is supplied. Use to look up a customer or enumerate the customer base; the email filter matches exactly, not partially. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Email",
      description: "Optionally filter customers by exact email address.",
      required: !1
    }),
    status: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Status",
      description: "Optionally filter customers by status.",
      required: !1,
      options: {
        options: [
          {
            label: "Active",
            value: "active"
          },
          {
            label: "Archived",
            value: "archived"
          }
        ]
      }
    }),
    limit: import_pieces_framework8.Property.Number({
      displayName: "Limit",
      description: "Maximum number of customers to return.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let email = paddleUtils.getOptionalString({
      value: context.propsValue.email
    }), statusValue = paddleUtils.getOptionalString({
      value: context.propsValue.status
    }), limit = (_a = paddleUtils.getOptionalPositiveInteger({
      value: context.propsValue.limit,
      fieldName: "Limit"
    })) != null ? _a : DEFAULT_LIMIT, status = getCustomerStatus({
      statusValue
    });
    return paddleClient.listCustomers(__spreadValues({
      auth: context.auth,
      email,
      limit
    }, status ? { status } : {}));
  }
});
function getCustomerStatus({
  statusValue
}) {
  if (statusValue) {
    if (statusValue === "active" || statusValue === "archived")
      return statusValue;
    throw new Error("Status must be active or archived.");
  }
}
var DEFAULT_LIMIT = 100;

// ../ap/packages/pieces/community/paddle/src/lib/actions/update-subscription.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var updateSubscriptionAction = (0, import_pieces_framework9.createAction)({
  auth: paddleAuth,
  name: "update-subscription",
  displayName: "Update Subscription",
  description: "Updates a Paddle subscription. Send the full recurring item list you want the subscription to keep.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Paddle subscription by ID, replacing its recurring item (price and quantity) and optionally setting custom data; the proration billing mode controls how the change is charged (immediately vs. next billing period, prorated or full, or not billed). Use to change a plan, quantity, or metadata on a live subscription. The items you send replace the subscription's recurring items rather than merge, and the call mutates the subscription on each run, so it is not idempotent.",
    idempotent: !1
  },
  props: {
    subscriptionId: paddleProps.subscription(),
    priceId: paddleProps.recurringPrice(),
    quantity: import_pieces_framework9.Property.Number({
      displayName: "Quantity",
      description: "Optional quantity for the recurring price.",
      required: !1
    }),
    prorationBillingMode: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Proration Billing Mode",
      description: "How Paddle should bill for the subscription change.",
      required: !1,
      options: {
        options: [
          {
            label: "Prorated Immediately",
            value: "prorated_immediately"
          },
          {
            label: "Full Immediately",
            value: "full_immediately"
          },
          {
            label: "Prorated Next Billing Period",
            value: "prorated_next_billing_period"
          },
          {
            label: "Full Next Billing Period",
            value: "full_next_billing_period"
          },
          {
            label: "Do Not Bill",
            value: "do_not_bill"
          }
        ]
      }
    }),
    customData: import_pieces_framework9.Property.Json({
      displayName: "Custom Data",
      description: "Optional JSON object to store on the subscription.",
      required: !1
    })
  },
  async run(context) {
    let subscriptionId = paddleUtils.getRequiredString({
      value: context.propsValue.subscriptionId,
      fieldName: "Subscription"
    }), items = {
      price_id: context.propsValue.priceId,
      quantity: context.propsValue.quantity
    }, prorationBillingMode = paddleUtils.getOptionalString({
      value: context.propsValue.prorationBillingMode
    }), customData = paddleUtils.getOptionalObject({
      value: context.propsValue.customData,
      fieldName: "Custom Data"
    });
    return paddleClient.updateSubscription({
      auth: context.auth,
      subscriptionId,
      request: paddleUtils.compactRecord({
        custom_data: customData,
        items,
        proration_billing_mode: prorationBillingMode
      })
    });
  }
});

// ../ap/packages/pieces/community/paddle/src/lib/triggers/new-active-subscription.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var newActiveSubscription = (0, import_pieces_framework10.createTrigger)({
  auth: paddleAuth,
  name: "newActiveSubscription",
  displayName: "New Active Subscription",
  description: "Triggers when a subscription activates \u2014 a trial converts or the first payment succeeds.",
  aiMetadata: {
    description: "Fires on the Paddle subscription.activated event, when a subscription becomes active \u2014 for example a trial converts to paid or the first payment succeeds. Represents a newly live recurring subscription for a customer."
  },
  props: {},
  sampleData: {
    event_id: "evt_01abc123",
    event_type: "subscription.activated",
    occurred_at: "2024-01-01T00:00:00.000Z",
    data: {
      id: "sub_01abc123",
      status: "active",
      customer_id: "ctm_01abc123"
    }
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let setting = await paddleClient.createNotificationSetting({
      auth: context.auth,
      url: context.webhookUrl,
      subscribedEvents: ["subscription.activated"]
    });
    await context.store.put("notificationSettingId", setting.id);
  },
  async onDisable(context) {
    let notificationSettingId = await context.store.get("notificationSettingId");
    notificationSettingId && await paddleClient.deleteNotificationSetting({
      auth: context.auth,
      notificationSettingId
    });
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event_type !== "subscription.activated" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/paddle/src/lib/triggers/payment-failed.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var paymentFailed = (0, import_pieces_framework11.createTrigger)({
  auth: paddleAuth,
  name: "paymentFailed",
  displayName: "Payment Failed",
  description: "Triggers when a transaction payment fails \u2014 trigger retry logic or alert the customer.",
  aiMetadata: {
    description: "Fires on the Paddle transaction.payment_failed event, when a payment attempt on a transaction is declined or otherwise fails. Represents a failed collection for a customer, useful for retry/dunning logic or customer alerts."
  },
  props: {},
  sampleData: {
    event_id: "evt_01abc127",
    event_type: "transaction.payment_failed",
    occurred_at: "2024-01-01T00:00:00.000Z",
    data: {
      id: "txn_01abc123",
      status: "past_due",
      customer_id: "ctm_01abc123",
      payments: [
        {
          amount: "1000",
          status: "failed",
          error_code: "declined"
        }
      ]
    }
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let setting = await paddleClient.createNotificationSetting({
      auth: context.auth,
      url: context.webhookUrl,
      subscribedEvents: ["transaction.payment_failed"]
    });
    await context.store.put("notificationSettingId", setting.id);
  },
  async onDisable(context) {
    let notificationSettingId = await context.store.get("notificationSettingId");
    notificationSettingId && await paddleClient.deleteNotificationSetting({
      auth: context.auth,
      notificationSettingId
    });
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event_type !== "transaction.payment_failed" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/paddle/src/lib/triggers/subscription-canceled.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var subscriptionCanceled = (0, import_pieces_framework12.createTrigger)({
  auth: paddleAuth,
  name: "subscriptionCanceled",
  displayName: "Subscription Canceled",
  description: "Triggers when a subscription is canceled \u2014 revoke access or notify support.",
  aiMetadata: {
    description: "Fires on the Paddle subscription.canceled event, when a subscription is canceled and its cancellation takes effect. Represents the end of a recurring subscription, useful for revoking access or notifying support."
  },
  props: {},
  sampleData: {
    event_id: "evt_01abc124",
    event_type: "subscription.canceled",
    occurred_at: "2024-01-01T00:00:00.000Z",
    data: {
      id: "sub_01abc123",
      status: "canceled",
      customer_id: "ctm_01abc123",
      canceled_at: "2024-01-01T00:00:00.000Z"
    }
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let setting = await paddleClient.createNotificationSetting({
      auth: context.auth,
      url: context.webhookUrl,
      subscribedEvents: ["subscription.canceled"]
    });
    await context.store.put("notificationSettingId", setting.id);
  },
  async onDisable(context) {
    let notificationSettingId = await context.store.get("notificationSettingId");
    notificationSettingId && await paddleClient.deleteNotificationSetting({
      auth: context.auth,
      notificationSettingId
    });
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event_type !== "subscription.canceled" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/paddle/src/lib/triggers/subscription-past-due.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var subscriptionPastDue = (0, import_pieces_framework13.createTrigger)({
  auth: paddleAuth,
  name: "subscriptionPastDue",
  displayName: "Subscription Past Due",
  description: "Triggers when a subscription goes past due \u2014 start dunning flows or restrict features.",
  aiMetadata: {
    description: "Fires on the Paddle subscription.past_due event, when a subscription enters past-due status because a renewal payment was not collected. Represents a subscription at risk of cancellation, useful for dunning flows or restricting features."
  },
  props: {},
  sampleData: {
    event_id: "evt_01abc125",
    event_type: "subscription.past_due",
    occurred_at: "2024-01-01T00:00:00.000Z",
    data: {
      id: "sub_01abc123",
      status: "past_due",
      customer_id: "ctm_01abc123"
    }
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let setting = await paddleClient.createNotificationSetting({
      auth: context.auth,
      url: context.webhookUrl,
      subscribedEvents: ["subscription.past_due"]
    });
    await context.store.put("notificationSettingId", setting.id);
  },
  async onDisable(context) {
    let notificationSettingId = await context.store.get("notificationSettingId");
    notificationSettingId && await paddleClient.deleteNotificationSetting({
      auth: context.auth,
      notificationSettingId
    });
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event_type !== "subscription.past_due" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/paddle/src/lib/triggers/transaction-completed.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var transactionCompleted = (0, import_pieces_framework14.createTrigger)({
  auth: paddleAuth,
  name: "transactionCompleted",
  displayName: "Transaction Completed",
  description: "Triggers when a transaction completes successfully \u2014 send a receipt or update records.",
  aiMetadata: {
    description: "Fires on the Paddle transaction.completed event, when a transaction is fully paid and finalized. Represents a successful payment/collection for a customer, useful for sending receipts or updating records."
  },
  props: {},
  sampleData: {
    event_id: "evt_01abc126",
    event_type: "transaction.completed",
    occurred_at: "2024-01-01T00:00:00.000Z",
    data: {
      id: "txn_01abc123",
      status: "completed",
      customer_id: "ctm_01abc123",
      details: {
        totals: {
          total: "1000",
          tax: "100",
          subtotal: "900",
          currency_code: "USD"
        }
      }
    }
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let setting = await paddleClient.createNotificationSetting({
      auth: context.auth,
      url: context.webhookUrl,
      subscribedEvents: ["transaction.completed"]
    });
    await context.store.put("notificationSettingId", setting.id);
  },
  async onDisable(context) {
    let notificationSettingId = await context.store.get("notificationSettingId");
    notificationSettingId && await paddleClient.deleteNotificationSetting({
      auth: context.auth,
      notificationSettingId
    });
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event_type !== "transaction.completed" ? [] : [payload];
  }
});

// ../ap/packages/pieces/community/paddle/src/index.ts
var paddle = (0, import_pieces_framework15.createPiece)({
  displayName: "Paddle",
  description: "Manage customers, subscriptions, and recurring billing with Paddle Billing.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/paddle.png",
  categories: [import_pieces_framework16.PieceCategory.COMMERCE, import_pieces_framework16.PieceCategory.PAYMENT_PROCESSING],
  authors: ["veri5ied"],
  auth: paddleAuth,
  actions: [
    listCustomersAction,
    getSubscriptionAction,
    updateSubscriptionAction,
    cancelSubscriptionAction,
    createTransactionAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: paddleAuth,
      baseUrl: (auth) => auth ? paddleClient.getBaseUrl({ apiKey: auth.secret_text }) : paddleClient.getBaseUrl({ apiKey: "" }),
      authMapping: async (auth) => ({
        Accept: "application/json",
        Authorization: `Bearer ${auth.secret_text}`,
        "Content-Type": "application/json",
        "Paddle-Version": "1"
      })
    })
  ],
  triggers: [
    newActiveSubscription,
    subscriptionCanceled,
    subscriptionPastDue,
    transactionCompleted,
    paymentFailed
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  paddle
});
