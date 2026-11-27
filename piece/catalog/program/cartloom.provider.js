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

// ../ap/packages/pieces/community/cartloom/src/index.ts
var index_exports = {};
__export(index_exports, {
  cartloom: () => cartloom
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_framework10 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/cartloom/src/lib/actions/create-discount.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/cartloom/src/lib/api.ts
var import_pieces_common = require("@activepieces/pieces-common"), cartloomAPI = async (api, auth, body = {}) => {
  let request = {
    method: import_pieces_common.HttpMethod.POST,
    url: `https://${auth.domain}.cartloom.com/api${api}`,
    headers: {
      "Content-Type": "application/x-www-form-urlencoded",
      "X-API-KEY": auth.apiKey
    },
    body
  }, response = await import_pieces_common.httpClient.sendRequest(request);
  if (response.status !== 200)
    throw new Error(`Cartloom API Error: ${response.status} ${response.body}`);
  let data = Object.keys(response.body).map((key) => response.body[key]);
  return typeof response.body[0] == "undefined" && (data = [response.body]), {
    success: !0,
    data
  };
};
async function getProducts(auth) {
  return cartloomAPI("/products/list", auth);
}
async function getAllDiscounts(auth) {
  return cartloomAPI("/discounts/list", auth);
}
async function getDiscount(auth, discountId) {
  return cartloomAPI("/discounts/get", auth, { id: discountId });
}
async function getOrder(auth, invoice) {
  return cartloomAPI("/orders/get", auth, { invoice });
}
async function getOrdersByDate(auth, data) {
  return cartloomAPI("/orders/list", auth, data);
}
async function createDiscount(auth, data) {
  return cartloomAPI("/discounts/add", auth, data);
}

// ../ap/packages/pieces/community/cartloom/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var cartloomAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: "Cartloom Authentication",
  props: {
    domain: import_pieces_framework.Property.ShortText({
      displayName: "Cartloom Domain",
      description: "Your cartloom domain will be the subdomain part of your Cartloom URL. Example: https://<strong>mycompany</strong>.cartloom.com",
      required: !0
    }),
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "The API key for your Cartloom account",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await validateAuth(auth), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: e == null ? void 0 : e.message
      };
    }
  },
  required: !0
}), validateAuth = async (auth) => {
  if ((await getProducts(auth)).success !== !0)
    throw new Error(
      "Authentication failed. Please check your domain and API key and try again."
    );
};

// ../ap/packages/pieces/community/cartloom/src/lib/props.ts
async function buildProductsDropdown(auth) {
  return auth ? {
    options: (await getProducts(auth)).data.map((product) => ({ label: product.name, value: product.pid }))
  } : {
    options: [],
    disabled: !0,
    placeholder: "Please authenticate first"
  };
}

// ../ap/packages/pieces/community/cartloom/src/lib/actions/create-discount.ts
var createDiscountAction = (0, import_pieces_framework2.createAction)({
  name: "create_discount",
  classification: "WRITE",
  auth: cartloomAuth,
  displayName: "Create Discount",
  description: "Create a discount in Cartloom",
  audience: "both",
  aiMetadata: { description: "Creates a new discount in the connected Cartloom store, as either a fixed-amount or percentage discount applied to all products, selected products, or the order total. Use to set up a promotion or coupon; supply a code for a redeemable coupon or leave it blank for an automatic discount, and provide start/stop dates. Not idempotent \u2014 each call creates a separate discount.", idempotent: !1 },
  props: {
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Title",
      description: "Enter the title of the discount",
      required: !0
    }),
    enabled: import_pieces_framework2.Property.Checkbox({
      displayName: "Enabled",
      description: "Is this discount enabled?",
      required: !0,
      defaultValue: !1
    }),
    auto: import_pieces_framework2.Property.Checkbox({
      displayName: "Auto",
      description: "Is this discount automatically applied?",
      required: !0,
      defaultValue: !1
    }),
    unlimited: import_pieces_framework2.Property.Checkbox({
      displayName: "Unlimited",
      description: "Is this discount unlimited?",
      required: !0,
      defaultValue: !1
    }),
    selfDestruct: import_pieces_framework2.Property.Checkbox({
      displayName: "Self Destruct",
      description: "Remove the discount after use",
      required: !0,
      defaultValue: !1
    }),
    applyOnce: import_pieces_framework2.Property.Checkbox({
      displayName: "Apply Once",
      description: "Apply the discount once per order",
      required: !0,
      defaultValue: !1
    }),
    type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Type of Discount",
      description: "Select the type of discount",
      defaultValue: "fixed",
      required: !0,
      options: {
        options: [
          { label: "Fixed Amount", value: "fixed" },
          { label: "Percentage", value: "percent" }
        ]
      }
    }),
    amount: import_pieces_framework2.Property.Number({
      displayName: "Amount",
      description: "Enter the amount of the discount",
      required: !0,
      defaultValue: 0
    }),
    target: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Discount Target",
      description: "Select the target of the discount",
      defaultValue: "all",
      required: !0,
      options: {
        options: [
          { label: "Selected Products", value: "product" },
          { label: "Total", value: "total" },
          { label: "All Products", value: "all" }
        ]
      }
    }),
    startDate: import_pieces_framework2.Property.DateTime({
      displayName: "Start Date",
      description: "The start date of the discount. YYYY-MM-DD HH:MM:SS",
      required: !0
    }),
    stopDate: import_pieces_framework2.Property.DateTime({
      displayName: "Stop Date",
      description: "The stop date of the discount. YYYY-MM-DD HH:MM:SS",
      required: !0
    }),
    optional: import_pieces_framework2.Property.MarkDown({
      value: "## Optional Settings based on above settings"
    }),
    code: import_pieces_framework2.Property.ShortText({
      displayName: "Discount Code",
      description: "Enter the discount code. Leave blank for no code.",
      required: !1
    }),
    targetPids: import_pieces_framework2.Property.MultiSelectDropdown({
      auth: cartloomAuth,
      displayName: "Target Products",
      description: "Select the products to apply the discount to",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => auth ? await buildProductsDropdown(auth.props) : {
        options: [],
        disabled: !0,
        placeholder: "please authenticate"
      }
    }),
    targetAmount: import_pieces_framework2.Property.Number({
      displayName: "Target Amount",
      description: "The target amount for the discount when the target is set to Total",
      required: !1
    }),
    targetQuantity: import_pieces_framework2.Property.Number({
      displayName: "Target Quantity",
      description: "The target quantity for the discount when the target is set to All or Products",
      required: !1,
      defaultValue: 0
    }),
    allowance: import_pieces_framework2.Property.Number({
      displayName: "Allowance",
      description: "The number of times the discount can be used",
      required: !1
    })
  },
  async run(context) {
    var _a;
    return await createDiscount(context.auth.props, {
      enabled: context.propsValue.enabled ? 1 : 0,
      auto: context.propsValue.auto ? 1 : 0,
      unlimited: context.propsValue.unlimited ? 1 : 0,
      self_destruct: context.propsValue.selfDestruct ? 1 : 0,
      apply_once: context.propsValue.applyOnce ? 1 : 0,
      title: context.propsValue.title,
      type: context.propsValue.type,
      amount: context.propsValue.amount,
      target: context.propsValue.target,
      start_date: context.propsValue.startDate.split("T")[0],
      stop_date: context.propsValue.stopDate.split("T")[0],
      code: context.propsValue.code,
      target_pid: (_a = context.propsValue.targetPids) == null ? void 0 : _a.join(","),
      target_amount: context.propsValue.targetAmount,
      target_quantity: context.propsValue.targetQuantity,
      allowance: context.propsValue.allowance
    });
  }
});

// ../ap/packages/pieces/community/cartloom/src/lib/actions/get-discount.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var getDiscountAction = (0, import_pieces_framework3.createAction)({
  name: "get_discount",
  classification: "READ",
  auth: cartloomAuth,
  displayName: "Get Discount",
  description: "Get discount info from Cartloom",
  audience: "both",
  aiMetadata: { description: "Retrieves the details of a single Cartloom discount by its discount ID. Use when you have a specific discount ID; to list discounts without an ID, use Get All Discounts. Read-only and idempotent.", idempotent: !0 },
  props: {
    discountId: import_pieces_framework3.Property.ShortText({
      displayName: "Discount ID",
      description: "Enter the ID of the discount you want to retrieve",
      required: !0
    })
  },
  async run(context) {
    return await getDiscount(context.auth.props, context.propsValue.discountId);
  }
});

// ../ap/packages/pieces/community/cartloom/src/lib/actions/get-discounts.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var getAllDiscountsAction = (0, import_pieces_framework4.createAction)({
  name: "get_all_discounts",
  classification: "SEARCH",
  auth: cartloomAuth,
  displayName: "Get All Discounts",
  description: "Get a list of discounts from Cartloom",
  audience: "both",
  aiMetadata: { description: "Retrieves the full list of discounts configured in the connected Cartloom store. Use to browse existing discounts or find a discount ID before fetching one specific discount. Takes no input and returns all discounts; read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    return await getAllDiscounts(context.auth.props);
  }
});

// ../ap/packages/pieces/community/cartloom/src/lib/actions/get-order.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var getOrderAction = (0, import_pieces_framework5.createAction)({
  name: "get_order",
  classification: "READ",
  auth: cartloomAuth,
  displayName: "Get Order",
  description: "Get an order from Cartloom",
  audience: "both",
  aiMetadata: { description: "Retrieves a single Cartloom order by its invoice ID. Use when you already know the exact invoice ID and want that one order; to find orders without an invoice ID, use the date- or email-based order search instead. Read-only and idempotent.", idempotent: !0 },
  props: {
    invoice: import_pieces_framework5.Property.ShortText({
      displayName: "Invoice ID",
      description: "The invoice ID for the order you want to retrieve",
      required: !0
    })
  },
  async run(context) {
    return await getOrder(context.auth.props, context.propsValue.invoice);
  }
});

// ../ap/packages/pieces/community/cartloom/src/lib/actions/get-orders-date.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getOrderDateAction = (0, import_pieces_framework6.createAction)({
  name: "get_orders_by_date",
  classification: "SEARCH",
  auth: cartloomAuth,
  displayName: "Get Order by Date",
  description: "Get a list of orders from Cartloom within a date range",
  audience: "both",
  aiMetadata: { description: "Lists all Cartloom orders placed within a date range. Use to retrieve orders for a period when you do not have specific invoice IDs; to narrow results to one customer, use Get Order by Email instead. Requires a start date; the end date defaults to today. Read-only and idempotent.", idempotent: !0 },
  props: {
    start: import_pieces_framework6.Property.DateTime({
      displayName: "Start Date",
      description: "Select a date to start the search",
      required: !0
    }),
    end: import_pieces_framework6.Property.DateTime({
      displayName: "End Date",
      description: "Select a date to end the search. Defaults to today.",
      required: !1
    })
  },
  async run(context) {
    return await getOrdersByDate(context.auth.props, {
      start_date: context.propsValue.start.split("T")[0],
      end_date: context.propsValue.end || (/* @__PURE__ */ new Date()).toISOString().split("T")[0]
    });
  }
});

// ../ap/packages/pieces/community/cartloom/src/lib/actions/get-orders-date-email.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var getOrderEmailAction = (0, import_pieces_framework7.createAction)({
  name: "get_orders_by_email",
  classification: "SEARCH",
  auth: cartloomAuth,
  displayName: "Get Order by Email",
  description: "Get a list of orders for an email within a date range",
  audience: "both",
  aiMetadata: { description: "Lists Cartloom orders placed by a specific customer email address within a date range. Use to find a customer's order history; use Get Order by Date to list all orders regardless of customer. Requires the email and a start date; the end date defaults to today. Read-only and idempotent.", idempotent: !0 },
  props: {
    start: import_pieces_framework7.Property.DateTime({
      displayName: "Start Date",
      description: "Select a date to start the search",
      required: !0
    }),
    end: import_pieces_framework7.Property.DateTime({
      displayName: "End Date",
      description: "Select a date to end the search. Defaults to today.",
      required: !1
    }),
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Email",
      description: "Email address to search for.",
      required: !0
    })
  },
  async run(context) {
    return await getOrdersByDate(context.auth.props, {
      search_type: "email",
      keyword: context.propsValue.email,
      start_date: context.propsValue.start.split("T")[0],
      end_date: context.propsValue.end || (/* @__PURE__ */ new Date()).toISOString().split("T")[0]
    });
  }
});

// ../ap/packages/pieces/community/cartloom/src/lib/actions/get-products.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var getProductsAction = (0, import_pieces_framework8.createAction)({
  name: "get_products",
  classification: "SEARCH",
  auth: cartloomAuth,
  displayName: "Get Products",
  description: "Get a list of products from Cartloom",
  audience: "both",
  aiMetadata: { description: "Retrieves the full list of products from the connected Cartloom store. Use to discover available products and their IDs (for example before creating a discount targeting specific products). Takes no input and returns all products; read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    return await getProducts(context.auth.props);
  }
});

// ../ap/packages/pieces/community/cartloom/src/index.ts
var cartloom = (0, import_pieces_framework9.createPiece)({
  displayName: "Cartloom",
  description: "Sell products beautifully",
  auth: cartloomAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/cartloom.png",
  categories: [import_pieces_framework10.PieceCategory.COMMERCE],
  authors: ["joeworkman", "kishanprmr", "MoShizzle", "abuaboud"],
  actions: [
    getProductsAction,
    getOrderAction,
    createDiscountAction,
    getDiscountAction,
    getAllDiscountsAction,
    getOrderDateAction,
    getOrderEmailAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: (auth) => `https://${auth == null ? void 0 : auth.props.domain}.cartloom.com/api`,
      // Replace with the actual base URL
      auth: cartloomAuth,
      authMapping: async (auth) => ({
        "X-API-KEY": auth.props.apiKey
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  cartloom
});
