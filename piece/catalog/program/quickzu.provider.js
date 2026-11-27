"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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

// ../ap/packages/pieces/community/quickzu/src/index.ts
var index_exports = {};
__export(index_exports, {
  quickzu: () => quickzu
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework20 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickzu/src/lib/actions/business-settings/update-business-time.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickzu/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), authHelpDescription = `
1. Login to your Quickzu Dashboard.
2. Go to **https://app.quickzu.com/dash/settings/api-webhooks**.
3. Copy **API Token** to the clipboard and paste it.`, quickzuAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: authHelpDescription,
  required: !0
});

// ../ap/packages/pieces/community/quickzu/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickzu/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
function emptyValueFilter(accessor) {
  return (key) => {
    let val = accessor(key);
    return val != null && (typeof val != "string" || val.length > 0);
  };
}
function prepareQuery(request) {
  let params = {};
  return request && Object.keys(request).filter(emptyValueFilter((k) => request[k])).forEach((k) => {
    params[k] = request[k].toString();
  }), params;
}
var QuickzuAPIClient = class {
  constructor(apiToken) {
    __publicField(this, "apiToken", apiToken);
  }
  async makeRequest(method, resourceUri, query, body = void 0) {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: "https://app.quickzu.com/api" + resourceUri,
      headers: {
        Authorization: this.apiToken
      },
      queryParams: query,
      body
    })).body;
  }
  async createCategory(categoryInput) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/seller/categories/",
      void 0,
      categoryInput
    );
  }
  async updateCategory(categoryId, categoryInput) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      `/seller/categories/${categoryId}`,
      void 0,
      categoryInput
    );
  }
  async deleteCategory(categoryId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.DELETE,
      `/seller/categories/${categoryId}`
    );
  }
  async listCategories(term) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/seller/categories",
      prepareQuery({
        term
      })
    );
  }
  async createProduct(productInput) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/seller/products",
      void 0,
      productInput
    );
  }
  async updateProduct(productId, productInput) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      `/seller/products/${productId}`,
      void 0,
      productInput
    );
  }
  async deleteProduct(productId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.DELETE,
      `/seller/products/${productId}`
    );
  }
  async listProducts(term, categoryId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/seller/products",
      prepareQuery({
        term,
        category: categoryId
      })
    );
  }
  async getOrderDetails(orderId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/seller/orders/${orderId}`);
  }
  async updateOrderStatus(orderId, status) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      `/seller/orders/${orderId}`,
      void 0,
      {
        status
      }
    );
  }
  async listOrders(page, limit) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/seller/orders/",
      prepareQuery({ page, limit })
    );
  }
  async listLiveOrders(limit) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.GET,
      "/seller/orders/live",
      prepareQuery({ limit })
    );
  }
  async createProductDiscount(discountInput) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/seller/discounts",
      void 0,
      discountInput
    );
  }
  async createPromoCode(promocodeInput) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      "/seller/discounts",
      void 0,
      promocodeInput
    );
  }
  async updateBusinessTime(timingInput) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      "/seller/settings",
      void 0,
      timingInput
    );
  }
};

// ../ap/packages/pieces/community/quickzu/src/lib/common/index.ts
function makeClient(auth) {
  return new QuickzuAPIClient(auth.secret_text);
}
var quickzuCommon = {
  categoryId: (required = !1) => import_pieces_framework2.Property.Dropdown({
    auth: quickzuAuth,
    displayName: "Category",
    refreshers: [],
    required,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient(auth).listCategories()).data.map((category) => ({
        label: category.name,
        value: category._id
      }))
    } : {
      disabled: !0,
      options: [],
      placeholder: "Please connect your account first."
    }
  }),
  productId: (required = !1) => import_pieces_framework2.Property.Dropdown({
    auth: quickzuAuth,
    displayName: "Product",
    refreshers: [],
    required,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient(auth).listProducts()).data.map((product) => ({
        label: product.name,
        value: product._id
      }))
    } : {
      disabled: !0,
      options: [],
      placeholder: "Please connect your account first."
    }
  }),
  orderId: (required = !1) => import_pieces_framework2.Property.Dropdown({
    auth: quickzuAuth,
    displayName: "Order",
    refreshers: [],
    required,
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await makeClient(auth).listOrders(1, 20)).data.map(
        (order) => ({
          label: order.order_id.toString(),
          value: order._id
        })
      )
    } : {
      disabled: !0,
      options: [],
      placeholder: "Please connect your account first."
    }
  })
};

// ../ap/packages/pieces/community/quickzu/src/lib/actions/business-settings/update-business-time.ts
var updateBusinessTimeAction = (0, import_pieces_framework3.createAction)({
  auth: quickzuAuth,
  name: "quickzu_update_business_time",
  displayName: "Update Business Time",
  description: "Updates business hours.",
  audience: "both",
  aiMetadata: {
    description: "Sets the weekly operating hours for a Quickzu store, supplying start/end times and an open/closed status per weekday. Use to configure when the store accepts orders. Idempotent: submitting the same schedule repeatedly leaves the store settings in the same state.",
    idempotent: !0
  },
  props: {
    items: import_pieces_framework3.Property.Array({
      displayName: "Business Hours",
      required: !0,
      properties: {
        weekday: import_pieces_framework3.Property.StaticDropdown({
          displayName: "Day",
          required: !0,
          options: {
            disabled: !1,
            options: [
              { label: "Sunday", value: "0" },
              { label: "Monday", value: "1" },
              { label: "Tuesday", value: "2" },
              { label: "Wednesday", value: "3" },
              { label: "Thursday", value: "4" },
              { label: "Friday", value: "5" },
              { label: "Saturday", value: "6" }
            ]
          }
        }),
        start: import_pieces_framework3.Property.ShortText({
          displayName: "Start Time",
          description: "Please use 24:00 hour format",
          required: !0
        }),
        end: import_pieces_framework3.Property.ShortText({
          displayName: "Start Time",
          description: "Please use 24:00 hour format",
          required: !0
        }),
        status: import_pieces_framework3.Property.Checkbox({
          displayName: "Status",
          required: !0,
          defaultValue: !0
        })
      }
    })
  },
  async run(context) {
    let items = context.propsValue.items, input = {
      timing: {}
    };
    for (let day of items)
      input.timing[day.weekday] = {
        start: day.start,
        end: day.end,
        status: day.status
      };
    return await makeClient(context.auth).updateBusinessTime(input);
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/categories/create-category.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var createCategoryAction = (0, import_pieces_framework4.createAction)({
  auth: quickzuAuth,
  name: "quickzu_create_category",
  displayName: "Create Category",
  description: "Creates a new category in store.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new product category in a Quickzu store from a name and active status. Use to add a category before assigning products to it. Not idempotent: each call creates a separate category even with the same name.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Category Name",
      required: !0
    }),
    status: import_pieces_framework4.Property.Checkbox({
      displayName: "Category Status",
      required: !0,
      defaultValue: !0
    })
  },
  async run(context) {
    let { name, status } = context.propsValue;
    return await makeClient(context.auth).createCategory({ name, status });
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/categories/delete-category.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var deleteCategoryAction = (0, import_pieces_framework5.createAction)({
  auth: quickzuAuth,
  name: "quickzu_delete_category",
  displayName: "Delete Category",
  description: "Deletes an existing category from store.",
  audience: "both",
  aiMetadata: {
    description: "Permanently removes a category from a Quickzu store by its category ID. Use to delete a catalog category. Idempotent: once the category is gone, repeating the call leaves the store in the same state.",
    idempotent: !0
  },
  props: {
    categoryId: quickzuCommon.categoryId(!0)
  },
  async run(context) {
    let { categoryId } = context.propsValue;
    return await makeClient(context.auth).deleteCategory(categoryId);
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/categories/list-categories.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var listCategoriesAction = (0, import_pieces_framework6.createAction)({
  auth: quickzuAuth,
  name: "quickzu_list_categories",
  displayName: "List Categories",
  description: "Retrieves all categories from store.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves categories from a Quickzu store. With no search term it returns all categories; supplying a name term filters to matches. Use to look up categories or resolve a category ID before another action. Idempotent read-only lookup.",
    idempotent: !0
  },
  props: {
    term: import_pieces_framework6.Property.ShortText({
      displayName: "Search Term",
      description: "Category name that need to be search.",
      required: !1
    })
  },
  async run(context) {
    let { term } = context.propsValue;
    return makeClient(context.auth).listCategories(term);
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/categories/update-category.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var updateCategoryAction = (0, import_pieces_framework7.createAction)({
  auth: quickzuAuth,
  name: "quickzu_update_category",
  displayName: "Update Category",
  description: "Updates an existing category in store.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Quickzu category identified by its category ID, changing its name and/or active status. Use to rename a category or toggle its visibility. Idempotent: repeating with the same input leaves the category in the same state.",
    idempotent: !0
  },
  props: {
    categoryId: quickzuCommon.categoryId(!0),
    name: import_pieces_framework7.Property.ShortText({
      displayName: "Category Name",
      required: !1
    }),
    status: import_pieces_framework7.Property.Checkbox({
      displayName: "Category Status",
      required: !0,
      defaultValue: !1
    })
  },
  async run(context) {
    let { categoryId, name, status } = context.propsValue;
    return await makeClient(context.auth).updateCategory(categoryId, { name, status });
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/discounts/create-product-discount.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/quickzu/src/lib/common/constants.ts
var OrderStatus = /* @__PURE__ */ ((OrderStatus2) => (OrderStatus2.PENDING = "pending", OrderStatus2.PAID = "paid", OrderStatus2.ACCEPTED = "accepted", OrderStatus2.CANCELLED = "cancelled", OrderStatus2.DONE = "done", OrderStatus2))(OrderStatus || {}), DiscountFilterType = /* @__PURE__ */ ((DiscountFilterType2) => (DiscountFilterType2.PRODUCTS = "PRODUCTS", DiscountFilterType2.CATEGORIES = "CATEGORIES", DiscountFilterType2.ALL_PRODUCTS = "ALL_PRODUCTS", DiscountFilterType2))(DiscountFilterType || {}), DiscountValueType = /* @__PURE__ */ ((DiscountValueType2) => (DiscountValueType2.PERCENTAGE = "PERCENTAGE", DiscountValueType2.FLAT = "FLAT", DiscountValueType2.FIXED_PRICE = "FIXED_PRICE", DiscountValueType2))(DiscountValueType || {});
var ProductUnit = /* @__PURE__ */ ((ProductUnit2) => (ProductUnit2.PIECE = "piece", ProductUnit2.KILOGRAM = "kilogram", ProductUnit2.GRAM = "Gram", ProductUnit2.POUND = "pound", ProductUnit2.LITRE = "litre", ProductUnit2.MILI_LITRE = "mili litre", ProductUnit2.DOZEN = "dozen", ProductUnit2.FEET = "feet", ProductUnit2.METER = "meter", ProductUnit2.SQUARE_FEET = "square feet", ProductUnit2.SQUARE_METER = "square meter", ProductUnit2.SET = "set", ProductUnit2.HOUR = "hour", ProductUnit2.DAY = "day", ProductUnit2.SERVICE = "service", ProductUnit2.COMBO = "combo", ProductUnit2.BOX = "box", ProductUnit2.PACK = "pack", ProductUnit2.BOTTLE = "bottle", ProductUnit2.WHOLE = "whole", ProductUnit2.SLICE = "slice", ProductUnit2.BULK = "bulk", ProductUnit2))(ProductUnit || {});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/discounts/create-product-discount.ts
var createProductDiscountAction = (0, import_pieces_framework8.createAction)({
  auth: quickzuAuth,
  name: "quickzu_create_product_discount",
  displayName: "Create Product Discount",
  description: "Creates a new discount for category or product level.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new item-level discount in a Quickzu store over a date range. The filter type selects scope: specific categories, specific products, or all products. Use to set up an automatic price reduction (not a coupon code). Not idempotent: each call creates a separate discount.",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework8.Property.ShortText({
      displayName: "Promotion / Discount Title",
      required: !0
    }),
    start_date: import_pieces_framework8.Property.DateTime({
      displayName: "Valid From",
      description: "Please use YYYY-MM-DD format.",
      required: !0
    }),
    end_date: import_pieces_framework8.Property.DateTime({
      displayName: "Valid To",
      description: "Please use YYYY-MM-DD format.",
      required: !0
    }),
    filter_type: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Filter",
      description: "Choose what gets discount (products/categories).",
      required: !0,
      options: {
        disabled: !1,
        options: Object.values(DiscountFilterType).map((value) => ({
          label: value.toLowerCase(),
          value
        }))
      }
    }),
    selectedFilterValues: import_pieces_framework8.Property.DynamicProperties({
      auth: quickzuAuth,
      displayName: "Select Option",
      refreshers: ["filter_type"],
      required: !0,
      props: async ({ auth, filter_type }) => {
        if (!auth) return {};
        if (!filter_type) return {};
        let fields = {}, discountFilterType = filter_type, client = makeClient(auth);
        switch (discountFilterType) {
          case "CATEGORIES" /* CATEGORIES */: {
            let res = await client.listCategories();
            fields.values = import_pieces_framework8.Property.StaticMultiSelectDropdown({
              displayName: "Categories",
              required: !0,
              description: "Categories eligible for a discount.",
              options: {
                disabled: !1,
                options: res.data.map((category) => ({
                  label: category.name,
                  value: category._id
                }))
              }
            });
            break;
          }
          case "PRODUCTS" /* PRODUCTS */: {
            let res = await client.listProducts();
            fields.values = import_pieces_framework8.Property.StaticMultiSelectDropdown({
              displayName: "Products",
              required: !0,
              description: "Products eligible for a discount.",
              options: {
                disabled: !1,
                options: res.data.map((product) => ({
                  label: product.name,
                  value: product._id
                }))
              }
            });
            break;
          }
          case "ALL_PRODUCTS" /* ALL_PRODUCTS */: {
            fields.values = [];
            break;
          }
        }
        return fields;
      }
    }),
    type: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Discount",
      required: !0,
      options: {
        disabled: !1,
        options: Object.values(DiscountValueType).map((value) => ({
          label: value.toLowerCase(),
          value
        }))
      }
    }),
    value: import_pieces_framework8.Property.ShortText({
      displayName: "Discount Value",
      required: !0
    }),
    is_enabled: import_pieces_framework8.Property.Checkbox({
      displayName: "Enabled ?",
      required: !0,
      defaultValue: !0
    }),
    is_visible: import_pieces_framework8.Property.Checkbox({
      displayName: "Visibility",
      required: !0,
      defaultValue: !0
    })
  },
  async run(context) {
    let {
      title,
      start_date,
      end_date,
      filter_type,
      selectedFilterValues,
      type,
      value,
      is_enabled,
      is_visible
    } = context.propsValue, input = {
      discount_method: "ITEM_LEVEL" /* ITEM_LEVEL */,
      title,
      start_date,
      end_date,
      filter_type,
      type,
      value,
      is_enabled,
      is_visible
    };
    return filter_type === "CATEGORIES" /* CATEGORIES */ ? input.selectedCategories = selectedFilterValues.values : filter_type === "PRODUCTS" /* PRODUCTS */ && (input.selectedProducts = selectedFilterValues.values), await makeClient(context.auth).createProductDiscount(input);
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/discounts/create-promo-code.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var createPromoCodeAction = (0, import_pieces_framework9.createAction)({
  auth: quickzuAuth,
  name: "quickzu_create_promo_code",
  displayName: "Create Promo/Coupon Code",
  description: "Creates a new promo code for category or product level.",
  audience: "both",
  aiMetadata: {
    description: "Creates a customer-redeemable promo/coupon code in a Quickzu store over a date range, with a minimum cart value and a maximum usage limit. The filter type selects scope: specific categories, specific products, or all products. Use to set up a code shoppers enter at checkout (unlike Create Product Discount, which applies automatically). Not idempotent: each call creates a separate promo.",
    idempotent: !1
  },
  props: {
    title: import_pieces_framework9.Property.ShortText({
      displayName: "Promotion / Discount Title",
      required: !0
    }),
    start_date: import_pieces_framework9.Property.DateTime({
      displayName: "Valid From",
      description: "Please use YYYY-MM-DD format.",
      required: !0
    }),
    end_date: import_pieces_framework9.Property.DateTime({
      displayName: "Valid To",
      description: "Please use YYYY-MM-DD format.",
      required: !0
    }),
    filter_type: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Filter",
      description: "Choose what gets discount (products/categories).",
      required: !0,
      options: {
        disabled: !1,
        options: Object.values(DiscountFilterType).map((value) => ({
          label: value.toLowerCase(),
          value
        }))
      }
    }),
    selectedFilterValues: import_pieces_framework9.Property.DynamicProperties({
      auth: quickzuAuth,
      displayName: "Select Option",
      refreshers: ["filter_type"],
      required: !0,
      props: async ({ auth, filter_type }) => {
        if (!auth) return {};
        if (!filter_type) return {};
        let fields = {}, discountFilterType = filter_type, client = makeClient(auth);
        switch (discountFilterType) {
          case "CATEGORIES" /* CATEGORIES */: {
            let res = await client.listCategories();
            fields.values = import_pieces_framework9.Property.StaticMultiSelectDropdown({
              displayName: "Categories",
              required: !0,
              description: "Categories eligible for a discount.",
              options: {
                disabled: !1,
                options: res.data.map((category) => ({
                  label: category.name,
                  value: category._id
                }))
              }
            });
            break;
          }
          case "PRODUCTS" /* PRODUCTS */: {
            let res = await client.listProducts();
            fields.values = import_pieces_framework9.Property.StaticMultiSelectDropdown({
              displayName: "Products",
              required: !0,
              description: "Products eligible for a discount.",
              options: {
                disabled: !1,
                options: res.data.map((product) => ({
                  label: product.name,
                  value: product._id
                }))
              }
            });
            break;
          }
          case "ALL_PRODUCTS" /* ALL_PRODUCTS */: {
            fields.values = [];
            break;
          }
        }
        return fields;
      }
    }),
    promo_code: import_pieces_framework9.Property.ShortText({
      displayName: "Promo Code",
      required: !0
    }),
    minimum_cart_value: import_pieces_framework9.Property.Number({
      displayName: "Minimum Order Amount",
      required: !0
    }),
    max_users_limit: import_pieces_framework9.Property.Number({
      displayName: "Maximum Promo Code Limit",
      required: !0
    }),
    type: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Discount",
      required: !0,
      options: {
        disabled: !1,
        options: Object.values(DiscountValueType).filter((value) => value !== "FIXED_PRICE" /* FIXED_PRICE */).map((value) => ({
          label: value.toLowerCase(),
          value
        }))
      }
    }),
    value: import_pieces_framework9.Property.ShortText({
      displayName: "Discount Value",
      required: !0
    }),
    is_enabled: import_pieces_framework9.Property.Checkbox({
      displayName: "Enabled ?",
      required: !0,
      defaultValue: !0
    }),
    is_visible: import_pieces_framework9.Property.Checkbox({
      displayName: "Visibility",
      required: !0,
      defaultValue: !0
    })
  },
  async run(context) {
    let {
      title,
      start_date,
      end_date,
      filter_type,
      selectedFilterValues,
      type,
      promo_code,
      max_users_limit,
      minimum_cart_value,
      value,
      is_enabled,
      is_visible
    } = context.propsValue, input = {
      discount_method: "SUB_TOTAL" /* SUB_TOTAL */,
      title,
      start_date,
      end_date,
      filter_type,
      max_users_limit,
      minimum_cart_value,
      promo_code,
      type,
      value,
      is_enabled,
      is_visible
    };
    return filter_type === "CATEGORIES" /* CATEGORIES */ ? input.selectedCategories = selectedFilterValues.values : filter_type === "PRODUCTS" /* PRODUCTS */ && (input.selectedProducts = selectedFilterValues.values), await makeClient(context.auth).createProductDiscount(input);
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/orders/get-order-details.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var getOrderDetailsAction = (0, import_pieces_framework10.createAction)({
  auth: quickzuAuth,
  name: "quickzu_get_order_details",
  displayName: "Get Order Details",
  description: "Retrieves order details from store.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves the full details of a single Quickzu order by its order ID. Use to inspect one specific order (customer, items, totals, status). Requires a known order ID. Idempotent read-only lookup.",
    idempotent: !0
  },
  props: {
    orderId: quickzuCommon.orderId(!0)
  },
  async run(context) {
    let { orderId } = context.propsValue;
    return await makeClient(context.auth).getOrderDetails(orderId);
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/orders/list-live-orders.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var listLiveOrdersAction = (0, import_pieces_framework11.createAction)({
  auth: quickzuAuth,
  name: "quickzu_list_live_orders",
  displayName: "List Live Orders",
  description: "Retrieves live orders of store.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves currently active (live) orders for a Quickzu store, grouped by order status, with a per-status limit on how many are returned. Use to monitor in-progress orders rather than full history. Idempotent read-only lookup.",
    idempotent: !0
  },
  props: {
    limit: import_pieces_framework11.Property.Number({
      displayName: "Number of live orders that need to be fetched per order status",
      required: !0,
      defaultValue: 15
    })
  },
  async run(context) {
    let { limit } = context.propsValue;
    return await makeClient(context.auth).listLiveOrders(limit);
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/orders/list-orders.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var listOrdersAction = (0, import_pieces_framework12.createAction)({
  auth: quickzuAuth,
  name: "quickzu_list_orders",
  displayName: "List Orders",
  description: "Retrieves orders of store.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a paginated list of all orders for a Quickzu store, controlled by page number and page size. Use to browse order history or find recent orders. Idempotent read-only lookup.",
    idempotent: !0
  },
  props: {
    page: import_pieces_framework12.Property.Number({
      displayName: "Current page number",
      required: !0,
      defaultValue: 1
    }),
    limit: import_pieces_framework12.Property.Number({
      displayName: "Number of orders per page",
      required: !0,
      defaultValue: 20
    })
  },
  async run(context) {
    let { page, limit } = context.propsValue;
    return await makeClient(context.auth).listOrders(page, limit);
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/orders/update-order-status.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var updateOrderStatusAction = (0, import_pieces_framework13.createAction)({
  auth: quickzuAuth,
  name: "quickzu_update_order_status",
  displayName: "Update Order Status",
  description: "Updates status of order in store.",
  audience: "both",
  aiMetadata: {
    description: "Sets the status of an existing Quickzu order (identified by order ID) to one of the predefined status values. Use to advance or change an order in its fulfillment lifecycle. Idempotent: setting the same status repeatedly leaves the order in the same state.",
    idempotent: !0
  },
  props: {
    orderId: quickzuCommon.orderId(!0),
    status: import_pieces_framework13.Property.StaticDropdown({
      displayName: "Order Status",
      required: !0,
      options: {
        disabled: !1,
        options: Object.values(OrderStatus).map((value) => ({
          label: value,
          value
        }))
      }
    })
  },
  async run(context) {
    let { orderId, status } = context.propsValue;
    return await makeClient(context.auth).updateOrderStatus(orderId, status);
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/products/create-product.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var addProductAction = (0, import_pieces_framework14.createAction)({
  auth: quickzuAuth,
  name: "quickzu_add_product",
  displayName: "Add Product",
  description: "Adds new product to store.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new product in a Quickzu store, requiring a name, category, MRP price, unit, and unit value. Use to add an item to the catalog. Not idempotent: each call creates a separate product even with identical input.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework14.Property.ShortText({
      displayName: "Name",
      required: !0
    }),
    desc: import_pieces_framework14.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    categoryId: quickzuCommon.categoryId(!0),
    mrp: import_pieces_framework14.Property.Number({
      displayName: "MRP Price",
      required: !0
    }),
    price: import_pieces_framework14.Property.Number({
      displayName: "Selling Price",
      required: !1,
      description: "Selling price should be equal or less than MRP."
    }),
    unit: import_pieces_framework14.Property.StaticDropdown({
      displayName: "Unit",
      required: !0,
      defaultValue: "kilogram",
      options: {
        disabled: !1,
        options: Object.values(ProductUnit).map((val) => ({
          label: val,
          value: val
        }))
      }
    }),
    value_per_unit: import_pieces_framework14.Property.Number({
      displayName: "Unit Value",
      required: !0,
      defaultValue: 1
    }),
    availability: import_pieces_framework14.Property.Checkbox({
      displayName: "Availability",
      required: !0,
      defaultValue: !0
    }),
    exclude_tax: import_pieces_framework14.Property.Checkbox({
      displayName: "Exclude Tax",
      required: !0,
      defaultValue: !1
    }),
    enable_variants: import_pieces_framework14.Property.Checkbox({
      displayName: "Enable Variants",
      required: !0,
      defaultValue: !1
    }),
    status: import_pieces_framework14.Property.Checkbox({
      displayName: "Status",
      required: !0,
      defaultValue: !0
    })
  },
  async run(context) {
    let {
      name,
      desc,
      categoryId,
      mrp,
      price,
      unit,
      value_per_unit,
      availability,
      exclude_tax,
      enable_variants,
      status
    } = context.propsValue;
    return await makeClient(context.auth).createProduct({
      name,
      desc,
      category: categoryId,
      mrp,
      price,
      unit,
      value_per_unit,
      availability,
      exclude_tax,
      enable_variants,
      status,
      meta: {
        nonveg: !1
      }
    });
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/products/delete-product.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var deleteProductAction = (0, import_pieces_framework15.createAction)({
  auth: quickzuAuth,
  name: "quickzu_delete_product",
  displayName: "Delete Product",
  description: "Deletes an existing product from store.",
  audience: "both",
  aiMetadata: {
    description: "Permanently removes a product from a Quickzu store by its product ID. Use to delete a catalog item. Idempotent: once the product is gone, repeating the call leaves the store in the same state.",
    idempotent: !0
  },
  props: {
    productId: quickzuCommon.productId(!0)
  },
  async run(context) {
    let { productId } = context.propsValue;
    return await makeClient(context.auth).deleteProduct(productId);
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/products/list-products.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var listProductsAction = (0, import_pieces_framework16.createAction)({
  auth: quickzuAuth,
  name: "quickzu_list_products",
  displayName: "List Products",
  description: "Retrieves all or single product details from store.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves products from a Quickzu store. With no search term or category it returns all products; supplying a name term and/or a category filters the result to matches. Use to look up products or resolve a product ID before another action. Idempotent read-only lookup.",
    idempotent: !0
  },
  props: {
    term: import_pieces_framework16.Property.ShortText({
      displayName: "Product name that need to be search.",
      required: !1
    }),
    categoryId: quickzuCommon.categoryId(!1)
  },
  async run(context) {
    let { term, categoryId } = context.propsValue;
    return await makeClient(context.auth).listProducts(term, categoryId);
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/actions/products/update-product.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var updateProductAction = (0, import_pieces_framework17.createAction)({
  auth: quickzuAuth,
  name: "quickzu_update_product",
  displayName: "Update Product",
  description: "Updates an existing product in store.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing Quickzu product identified by its product ID; only the provided fields (name, price, category, availability, etc.) are changed. Use to edit a product already in the catalog. Idempotent: repeating with the same input leaves the product in the same state.",
    idempotent: !0
  },
  props: {
    productId: quickzuCommon.productId(!0),
    name: import_pieces_framework17.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    desc: import_pieces_framework17.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    categoryId: quickzuCommon.categoryId(!1),
    mrp: import_pieces_framework17.Property.Number({
      displayName: "MRP Price",
      required: !1
    }),
    price: import_pieces_framework17.Property.Number({
      displayName: "Selling Price",
      required: !1,
      description: "Selling price should be equal or less than MRP."
    }),
    unit: import_pieces_framework17.Property.StaticDropdown({
      displayName: "Unit",
      required: !1,
      options: {
        disabled: !1,
        options: Object.values(ProductUnit).map((val) => ({
          label: val,
          value: val
        }))
      }
    }),
    value_per_unit: import_pieces_framework17.Property.Number({
      displayName: "Unit Value",
      required: !1
    }),
    availability: import_pieces_framework17.Property.Checkbox({
      displayName: "Availability",
      required: !1
    }),
    exclude_tax: import_pieces_framework17.Property.Checkbox({
      displayName: "Exclude Tax",
      required: !1
    }),
    enable_variants: import_pieces_framework17.Property.Checkbox({
      displayName: "Enable Variants",
      required: !1
    }),
    status: import_pieces_framework17.Property.Checkbox({
      displayName: "Status",
      required: !1
    })
  },
  async run(context) {
    let {
      name,
      productId,
      desc,
      categoryId,
      mrp,
      price,
      unit,
      value_per_unit,
      availability,
      exclude_tax,
      enable_variants,
      status
    } = context.propsValue;
    return await makeClient(context.auth).updateProduct(productId, {
      name,
      desc,
      category: categoryId,
      mrp,
      price,
      unit,
      value_per_unit,
      availability,
      exclude_tax,
      enable_variants,
      status,
      meta: {
        nonveg: !1
      }
    });
  }
});

// ../ap/packages/pieces/community/quickzu/src/lib/triggers/order-created.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var import_pieces_framework19 = require("@activepieces/pieces-framework"), markdown = `
- Go to the **Settings->API and Webhooks** section.
- In the webhook settings, paste this URL: 
  \`{{webhookUrl}}\`
- Click on **Save**.
`, sampleData = {
  payload: {
    id: "65cc96cfcf7028f638e20b0c",
    data: {
      id: "65cc96cfcf7028f638e20b0c",
      __v: 0,
      _id: "65cc96cfcf7028f638e20b0c",
      area: "63862620938a5552a9e6b",
      shop: "5fbe833cef26b83b8b7c3",
      status: "pending",
      currency: "INR",
      customer: {
        phone: "9039101337",
        address: "okay mumbai",
        full_name: "Mohit"
      },
      order_id: 233312,
      products: [
        {
          id: "65cc96cfcf70280fbde20b0d",
          _id: "65cc96cfcf70280fbde20b0d",
          qty: 1,
          addon: [],
          amount: 10,
          product: {
            id: "5fbe8677ef26b83b8f53b7cf",
            __v: 0,
            _id: "5fbe8677ef26b83b8f53b7cf",
            mrp: 10,
            sku: "",
            desc: "",
            meta: { nonveg: !1 },
            name: "Shrewsbury Cookies",
            shop: "5fbe833cef26b8f53b7c3",
            unit: "gram",
            index: 0,
            price: 10,
            addons: [],
            status: !0,
            options: [],
            category: "5fbe83acef26bb8f53b7c7",
            variants: [],
            exclude_tax: !1,
            availability: !0,
            stock_enabled: !1,
            value_per_unit: 200,
            available_stock: -1,
            enable_variants: !0
          },
          variant: [],
          createdAt: "2024-02-14T10:32:47.953Z",
          updatedAt: "2024-02-14T10:32:47.953Z"
        }
      ],
      coupon_id: "",
      createdAt: "2024-02-14T10:32:47.953Z",
      sub_total: 29,
      updatedAt: "2024-02-14T10:32:47.953Z",
      order_type: "delivery",
      coupon_code: "",
      instruction: "spicy food",
      payment_mode: "paylater",
      taxes_amount: 2.9,
      total_amount: "31.90",
      currency_data: {
        code: "INR",
        name: "Indian Rupee",
        symbol: "Rs",
        rounding: 0,
        name_plural: "Indian rupees",
        symbol_native: "\u20B9",
        decimal_digits: 2
      },
      payment_status: "unpaid",
      transaction_id: "",
      delivery_charges: 0,
      stripe_session_id: "",
      subtotal_discount: 0,
      item_level_discount: 0,
      razorpay_session_id: ""
    },
    shop: {
      id: "5fbe833c6b83b8f53b7c3",
      __v: 0,
      _id: "5fbe833c6b83b8f53b7c3",
      css: "",
      apps: { analytics: "" },
      desc: "pastries, sandwiches, cake",
      name: "Classic Cakes",
      alias: "ccake",
      header: "",
      timing: {
        0: { end: "23:59", start: "00:00", status: !0 },
        1: { end: "23:59", start: "00:00", status: !1 },
        2: { end: "23:59", start: "00:00", status: !0 },
        3: { end: "23:59", start: "00:00", status: !0 },
        4: { end: "23:59", start: "00:00", status: !0 },
        5: { end: "23:59", start: "00:00", status: !0 },
        6: { end: "23:59", start: "00:00", status: !0 },
        createdAt: "2024-01-13T17:54:46.861Z",
        updatedAt: "2024-01-13T17:54:46.861Z"
      },
      address: "78 Washington St, Hoboken",
      country: "IN",
      dine_in: !0,
      domains: [],
      message: {},
      pick_up: !0,
      category: "restaurant",
      currency: "INR",
      delivery: {
        cost: 0,
        free: 0,
        status: !0,
        min_order: 0,
        is_free_delivery: !0
      },
      language: "en",
      template: "five",
      createdAt: "2020-11-25T16:15:56.049Z",
      is_closed: !1,
      seller_id: {
        id: "5fbe8101ef26b83b8f53b7c1",
        __v: 0,
        _id: "5fbe8101ef26b83b8f53b7c1",
        email: "focix@getnada.com",
        language: "en-us",
        createdAt: "2020-11-25T16:06:25.546Z",
        full_name: "focix",
        updatedAt: "2023-07-01T14:02:27.589Z",
        account_type: "seller",
        is_email_verified: !0
      },
      updatedAt: "2024-01-29T10:22:46.775Z",
      is_blocked: !1,
      receive_on: "whatsapp",
      appointment: !1,
      is_onlymenu: !1,
      payment_inst: "Pay Offline via UPI/CASH etc",
      currency_data: {
        code: "INR",
        name: "Indian Rupee",
        symbol: "Rs",
        rounding: 0,
        name_plural: "Indian rupees",
        symbol_native: "\u20B9",
        decimal_digits: 2
      },
      payment_modes: {
        stripe: {
          logo: "/public/assets/imgs/stripe.svg",
          name: "stripe",
          enabled: !0,
          test_mode: !0,
          description: "",
          display_name: "Stripe"
        },
        razorpay: {
          logo: "/public/assets/imgs/razorpay.svg",
          name: "razorpay",
          key_id: "as",
          enabled: !1,
          test_mode: !1,
          secret_key: "da",
          description: "",
          test_key_id: "",
          display_name: "Razorpay",
          test_secret_key: ""
        }
      },
      use_area_list: !0,
      is_maintenance: !1,
      manual_payments: !0,
      payment_inst_title: "Pay Later",
      term_condition_text: "Bhgghhhbsbsbbsbnzja bhbb",
      condition_option_enabled: !0,
      invoice_footer_thankyou_msg: ""
    },
    text: "Hi, I'd like to place an order",
    overiew: {
      TAXES: "INR 2.90",
      TOTAL: "INR 31.90",
      DELIVERY: "INR 0.00",
      SUB_TOTAL: "INR 29.00",
      ORDER_TYPE: "Delivery",
      STORE_LINK: "ccake.quickzu.com",
      STORE_NAME: "Classic Cakes",
      PICKUP_TIME: "",
      ORDER_NUMBER: 233312,
      TABLE_NUMBER: "",
      CUSTOMER_NAME: "Mohit",
      CUSTOMER_PHONE: "9039101337",
      PAYMENT_METHOD: "PAYLATER",
      APPOINTMENT_TIME: "",
      CUSTOMER_ADDRESS: "okay mumbai",
      ORDER_INSTRUCTION: "spicy food",
      SUBTOTAL_DISCOUNT: "INR 0.00",
      ITEM_LEVEL_DISCOUNT: "INR 0.00",
      PAYMENT_INSTRUCTION: `
Pay Offline via UPI/CASH etc`
    },
    hyperlink: "",
    total_amount: "31.90"
  },
  resource: "order",
  operation: "create"
}, orderCreatedTrigger = (0, import_pieces_framework18.createTrigger)({
  auth: quickzuAuth,
  name: "quickzu_order_created_trigger",
  displayName: "Order Created/Updated",
  description: "Triggers when a new order is created or a order status is changed in store.",
  aiMetadata: {
    description: "Fires when a Quickzu store order is created or its status changes, delivering the order details, the originating shop, and a human-readable overview. Use to react to new orders or order-lifecycle updates in real time."
  },
  type: import_pieces_framework18.TriggerStrategy.WEBHOOK,
  sampleData,
  props: {
    md: import_pieces_framework18.Property.MarkDown({
      value: markdown
    })
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload];
  },
  handshakeConfiguration: {
    strategy: import_pieces_framework19.WebhookHandshakeStrategy.BODY_PARAM_PRESENT,
    paramName: "test"
  },
  async onHandshake(context) {
    return {
      status: 200,
      body: {}
    };
  }
});

// ../ap/packages/pieces/community/quickzu/src/index.ts
var quickzu = (0, import_pieces_framework20.createPiece)({
  displayName: "Quickzu",
  description: "Streamline ordering from whatsapp",
  auth: quickzuAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/quickzu.png",
  authors: ["kishanprmr", "abuaboud"],
  actions: [
    addProductAction,
    updateProductAction,
    deleteProductAction,
    listProductsAction,
    createCategoryAction,
    updateCategoryAction,
    deleteCategoryAction,
    listCategoriesAction,
    getOrderDetailsAction,
    listOrdersAction,
    listLiveOrdersAction,
    updateOrderStatusAction,
    createProductDiscountAction,
    createPromoCodeAction,
    updateBusinessTimeAction
  ],
  triggers: [orderCreatedTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  quickzu
});
