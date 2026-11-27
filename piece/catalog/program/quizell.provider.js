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

// ../ap/packages/pieces/community/quizell/src/index.ts
var index_exports = {};
__export(index_exports, {
  quizell: () => quizell,
  quizellAuth: () => quizellAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/quizell/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.quizell.com/api/v1";
async function quizellApiCall({
  token,
  method,
  path,
  body,
  queryParams
}) {
  return import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token
    },
    queryParams,
    body
  });
}

// ../ap/packages/pieces/community/quizell/src/lib/actions/create-customer.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var createCustomer = (0, import_pieces_framework.createAction)({
  auth: quizellAuth,
  name: "create_customer",
  displayName: "Create Customer",
  description: "Creates a new customer (quiz lead) in Quizell.",
  audience: "both",
  aiMetadata: { description: "Creates a new customer (quiz lead) in Quizell, attached to a specific quiz. Use when capturing a new lead or contact from a quiz response. Requires a quiz_id and email; not idempotent \u2014 each call creates another customer record even with identical input.", idempotent: !1 },
  props: {
    quiz_id: import_pieces_framework.Property.ShortText({
      displayName: "Quiz ID",
      description: "The ID of the quiz this customer responded to. Find it in your Quizell dashboard under the quiz settings URL (e.g. the number at the end of quizell.com/quiz/12345).",
      required: !0
    }),
    email: import_pieces_framework.Property.ShortText({
      displayName: "Email Address",
      description: "The customer's email address.",
      required: !0
    }),
    first_name: import_pieces_framework.Property.ShortText({
      displayName: "First Name",
      description: "The customer's first name.",
      required: !1
    }),
    last_name: import_pieces_framework.Property.ShortText({
      displayName: "Last Name",
      description: "The customer's last name.",
      required: !1
    }),
    phone: import_pieces_framework.Property.ShortText({
      displayName: "Phone Number",
      description: "The customer's phone number (e.g. +1-555-123-4567).",
      required: !1
    }),
    country: import_pieces_framework.Property.ShortText({
      displayName: "Country",
      description: "The customer's country (e.g. United States).",
      required: !1
    }),
    city: import_pieces_framework.Property.ShortText({
      displayName: "City",
      description: "The customer's city.",
      required: !1
    }),
    address: import_pieces_framework.Property.ShortText({
      displayName: "Address",
      description: "The customer's street address.",
      required: !1
    }),
    note: import_pieces_framework.Property.LongText({
      displayName: "Note",
      description: "An internal note about this customer.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l;
    let { quiz_id, email, first_name, last_name, phone, country, city, address, note } = context.propsValue, customerData = {
      quiz_id,
      email
    };
    first_name && (customerData.first_name = first_name), last_name && (customerData.last_name = last_name), phone && (customerData.phone = phone), country && (customerData.country = country), city && (customerData.city = city), address && (customerData.address = address), note && (customerData.note = note);
    let response = await quizellApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common2.HttpMethod.POST,
      path: "/customers/store",
      body: { customer_data: customerData }
    }), customer = response.body.data;
    return {
      success: response.body.status,
      message: response.body.message,
      id: (_a = customer.id) != null ? _a : null,
      quiz_id: (_b = customer.quiz_id) != null ? _b : null,
      email: (_c = customer.email) != null ? _c : null,
      first_name: (_d = customer.first_name) != null ? _d : null,
      last_name: (_e = customer.last_name) != null ? _e : null,
      phone: (_f = customer.phone) != null ? _f : null,
      country: (_g = customer.country) != null ? _g : null,
      city: (_h = customer.city) != null ? _h : null,
      address: (_i = customer.address) != null ? _i : null,
      note: (_j = customer.note) != null ? _j : null,
      created_at: (_k = customer.created_at) != null ? _k : null,
      updated_at: (_l = customer.updated_at) != null ? _l : null
    };
  }
});

// ../ap/packages/pieces/community/quizell/src/lib/actions/update-customer.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var updateCustomer = (0, import_pieces_framework2.createAction)({
  auth: quizellAuth,
  name: "update_customer",
  displayName: "Update Customer",
  description: "Updates an existing customer record in Quizell.",
  audience: "both",
  aiMetadata: { description: "Updates an existing Quizell customer (quiz lead), identified by customer_id. Use when correcting or enriching a known customer record; requires customer_id, quiz_id, and email. Idempotent \u2014 re-sending the same fields leaves the record in the same state.", idempotent: !0 },
  props: {
    customer_id: import_pieces_framework2.Property.ShortText({
      displayName: "Customer ID",
      description: 'The ID of the customer to update. Use the "List Customers" or "Get Customer" actions to find this ID.',
      required: !0
    }),
    quiz_id: import_pieces_framework2.Property.ShortText({
      displayName: "Quiz ID",
      description: "The ID of the quiz associated with this customer.",
      required: !0
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email Address",
      description: "The customer's email address.",
      required: !0
    }),
    first_name: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "The customer's first name.",
      required: !1
    }),
    last_name: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "The customer's last name.",
      required: !1
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Number",
      description: "The customer's phone number (e.g. +1-555-123-4567).",
      required: !1
    }),
    country: import_pieces_framework2.Property.ShortText({
      displayName: "Country",
      description: "The customer's country.",
      required: !1
    }),
    city: import_pieces_framework2.Property.ShortText({
      displayName: "City",
      description: "The customer's city.",
      required: !1
    }),
    address: import_pieces_framework2.Property.ShortText({
      displayName: "Address",
      description: "The customer's street address.",
      required: !1
    }),
    note: import_pieces_framework2.Property.LongText({
      displayName: "Note",
      description: "An internal note about this customer.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
    let { customer_id, quiz_id, email, first_name, last_name, phone, country, city, address, note } = context.propsValue, customerData = {
      quiz_id,
      email
    };
    first_name && (customerData.first_name = first_name), last_name && (customerData.last_name = last_name), phone && (customerData.phone = phone), country && (customerData.country = country), city && (customerData.city = city), address && (customerData.address = address), note && (customerData.note = note);
    let response = await quizellApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.PUT,
      path: `/customers/update/${customer_id}`,
      body: { customer_data: customerData }
    }), customer = response.body.data;
    return {
      success: response.body.status,
      message: response.body.message,
      id: (_a = customer.id) != null ? _a : null,
      quiz_id: (_b = customer.quiz_id) != null ? _b : null,
      email: (_c = customer.email) != null ? _c : null,
      first_name: (_d = customer.first_name) != null ? _d : null,
      last_name: (_e = customer.last_name) != null ? _e : null,
      phone: (_f = customer.phone) != null ? _f : null,
      country: (_g = customer.country) != null ? _g : null,
      city: (_h = customer.city) != null ? _h : null,
      address: (_i = customer.address) != null ? _i : null,
      note: (_j = customer.note) != null ? _j : null,
      updated_at: (_k = customer.updated_at) != null ? _k : null
    };
  }
});

// ../ap/packages/pieces/community/quizell/src/lib/actions/list-customers.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var listCustomers = (0, import_pieces_framework3.createAction)({
  auth: quizellAuth,
  name: "list_customers",
  displayName: "List Customers",
  description: "Retrieves a list of customers (quiz leads) from Quizell.",
  audience: "both",
  aiMetadata: { description: "Retrieves a paginated list of Quizell customers (quiz leads); an empty search returns all customers, while a search term filters by name or email. Use to browse leads or resolve a customer by name/email when you do not have an ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    search: import_pieces_framework3.Property.ShortText({
      displayName: "Search",
      description: "Filter customers by name or email. Leave empty to return all customers.",
      required: !1
    }),
    page: import_pieces_framework3.Property.Number({
      displayName: "Page",
      description: "Page number for results. Starts at 1.",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework3.Property.Number({
      displayName: "Results Per Page",
      description: "How many customers to return per page. Maximum is 100.",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { search, page, per_page } = context.propsValue, queryParams = {
      page: String(page != null ? page : 1),
      per_page: String(per_page != null ? per_page : 25)
    };
    search && (queryParams.search = search);
    let pagination = (await quizellApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      path: "/customers/list",
      queryParams
    })).body.data, customers = (_a = pagination.data) != null ? _a : [];
    return {
      total: (_b = pagination.total) != null ? _b : null,
      current_page: (_c = pagination.current_page) != null ? _c : null,
      per_page: (_d = pagination.per_page) != null ? _d : null,
      customers: customers.map((c) => {
        var _a2, _b2, _c2, _d2, _e, _f, _g, _h, _i, _j, _k, _l;
        return {
          id: (_a2 = c.id) != null ? _a2 : null,
          quiz_id: (_b2 = c.quiz_id) != null ? _b2 : null,
          email: (_c2 = c.email) != null ? _c2 : null,
          first_name: (_d2 = c.first_name) != null ? _d2 : null,
          last_name: (_e = c.last_name) != null ? _e : null,
          phone: (_f = c.phone) != null ? _f : null,
          country: (_g = c.country) != null ? _g : null,
          city: (_h = c.city) != null ? _h : null,
          address: (_i = c.address) != null ? _i : null,
          note: (_j = c.note) != null ? _j : null,
          created_at: (_k = c.created_at) != null ? _k : null,
          updated_at: (_l = c.updated_at) != null ? _l : null
        };
      })
    };
  }
});

// ../ap/packages/pieces/community/quizell/src/lib/actions/get-customer.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getCustomer = (0, import_pieces_framework4.createAction)({
  auth: quizellAuth,
  name: "get_customer",
  displayName: "Get Customer",
  description: "Retrieves details of a specific customer by their ID.",
  audience: "both",
  aiMetadata: { description: "Fetches the full details of a single Quizell customer by customer_id, including custom fields. Use when you already have the customer ID (e.g. from List Customers) and need their complete record. Read-only and idempotent.", idempotent: !0 },
  props: {
    customer_id: import_pieces_framework4.Property.ShortText({
      displayName: "Customer ID",
      description: 'The ID of the customer to retrieve. Use the "List Customers" action to find customer IDs.',
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l;
    let customer = (await quizellApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.GET,
      path: `/customers/detail/${context.propsValue.customer_id}`
    })).body.data, customFields = customer.custom_fields;
    return {
      id: (_a = customer.id) != null ? _a : null,
      quiz_id: (_b = customer.quiz_id) != null ? _b : null,
      email: (_c = customer.email) != null ? _c : null,
      first_name: (_d = customer.first_name) != null ? _d : null,
      last_name: (_e = customer.last_name) != null ? _e : null,
      phone: (_f = customer.phone) != null ? _f : null,
      country: (_g = customer.country) != null ? _g : null,
      city: (_h = customer.city) != null ? _h : null,
      address: (_i = customer.address) != null ? _i : null,
      note: (_j = customer.note) != null ? _j : null,
      custom_fields: customFields && typeof customFields == "object" ? JSON.stringify(customFields) : null,
      created_at: (_k = customer.created_at) != null ? _k : null,
      updated_at: (_l = customer.updated_at) != null ? _l : null
    };
  }
});

// ../ap/packages/pieces/community/quizell/src/lib/actions/search-products.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var searchProducts = (0, import_pieces_framework5.createAction)({
  auth: quizellAuth,
  name: "search_products",
  displayName: "Search Products",
  description: "Searches for products in your Quizell catalog by title or SKU.",
  audience: "both",
  aiMetadata: { description: "Searches the Quizell product catalog with paginated results; leaving title and SKU empty returns all products, while supplying either filters the catalog, and an optional active/inactive status filter narrows further. Use to browse the catalog or resolve a product by title/SKU when you lack its ID. Read-only and idempotent.", idempotent: !0 },
  props: {
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Product Title",
      description: "Search by product name. Leave empty to return all products.",
      required: !1
    }),
    sku: import_pieces_framework5.Property.ShortText({
      displayName: "SKU",
      description: "Search by product SKU (stock keeping unit). Leave empty to skip this filter.",
      required: !1
    }),
    status: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter by product status.",
      required: !1,
      defaultValue: "",
      options: {
        options: [
          { label: "All", value: "" },
          { label: "Active", value: "1" },
          { label: "Inactive", value: "0" }
        ]
      }
    }),
    page: import_pieces_framework5.Property.Number({
      displayName: "Page",
      description: "Page number for results. Starts at 1.",
      required: !1,
      defaultValue: 1
    }),
    per_page: import_pieces_framework5.Property.Number({
      displayName: "Results Per Page",
      description: "How many products to return per page.",
      required: !1,
      defaultValue: 25
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let { title, sku, status, page, per_page } = context.propsValue, queryParams = {
      page: String(page != null ? page : 1),
      per_page: String(per_page != null ? per_page : 25)
    };
    title && (queryParams.title = title), sku && (queryParams.sku = sku), status && (queryParams.status = status);
    let pagination = (await quizellApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.GET,
      path: "/products/search",
      queryParams
    })).body.data, products = (_a = pagination.data) != null ? _a : [];
    return {
      total: (_b = pagination.total) != null ? _b : null,
      current_page: (_c = pagination.current_page) != null ? _c : null,
      per_page: (_d = pagination.per_page) != null ? _d : null,
      products: products.map((p) => {
        var _a2, _b2, _c2, _d2, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n;
        return {
          id: (_a2 = p.id) != null ? _a2 : null,
          title: (_b2 = p.title) != null ? _b2 : null,
          sku: (_c2 = p.sku) != null ? _c2 : null,
          price: (_d2 = p.price) != null ? _d2 : null,
          compare_at_price: (_e = p.compare_at_price) != null ? _e : null,
          status: p.status === 1 ? "active" : "inactive",
          description: (_f = p.description) != null ? _f : null,
          image: (_g = p.image) != null ? _g : null,
          vendor: (_h = p.vendor) != null ? _h : null,
          quantity: (_i = p.quantity) != null ? _i : null,
          tags: Array.isArray(p.tags) ? p.tags.join(", ") : (_j = p.tags) != null ? _j : null,
          detail_link: (_k = p.detail_link) != null ? _k : null,
          external_id: (_l = p.external_id) != null ? _l : null,
          created_at: (_m = p.created_at) != null ? _m : null,
          updated_at: (_n = p.updated_at) != null ? _n : null
        };
      })
    };
  }
});

// ../ap/packages/pieces/community/quizell/src/lib/actions/create-product.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var createProduct = (0, import_pieces_framework6.createAction)({
  auth: quizellAuth,
  name: "create_product",
  displayName: "Create Product",
  description: "Adds a new product to your Quizell product catalog.",
  audience: "both",
  aiMetadata: { description: "Adds a new product to the Quizell catalog so it can appear in quiz recommendations. Use when introducing a product not yet in Quizell; requires title, price, and active/inactive status. Not idempotent \u2014 each call creates another product, so guard against duplicates if syncing from an external store.", idempotent: !1 },
  props: {
    title: import_pieces_framework6.Property.ShortText({
      displayName: "Product Title",
      description: "The name of the product as it will appear in quiz recommendations.",
      required: !0
    }),
    price: import_pieces_framework6.Property.Number({
      displayName: "Price",
      description: "The selling price of the product (e.g. 29.99).",
      required: !0
    }),
    status: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Status",
      description: "Whether this product is active and visible in quizzes.",
      required: !0,
      defaultValue: 1,
      options: {
        options: [
          { label: "Active", value: 1 },
          { label: "Inactive", value: 0 }
        ]
      }
    }),
    sku: import_pieces_framework6.Property.ShortText({
      displayName: "SKU",
      description: "Your internal stock keeping unit code for this product.",
      required: !1
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      description: "A description of the product.",
      required: !1
    }),
    image: import_pieces_framework6.Property.ShortText({
      displayName: "Image URL",
      description: "A publicly accessible URL to the product image (e.g. https://example.com/product.jpg).",
      required: !1
    }),
    vendor: import_pieces_framework6.Property.ShortText({
      displayName: "Vendor / Brand",
      description: "The brand or vendor name for this product.",
      required: !1
    }),
    quantity: import_pieces_framework6.Property.Number({
      displayName: "Quantity in Stock",
      description: "The available quantity for this product.",
      required: !1
    }),
    compare_at_price: import_pieces_framework6.Property.Number({
      displayName: "Compare-At Price",
      description: "The original price to show a discount (e.g. 49.99). Leave empty if there is no sale.",
      required: !1
    }),
    tags: import_pieces_framework6.Property.ShortText({
      displayName: "Tags",
      description: 'Comma-separated tags to categorize this product (e.g. "skincare, moisturizer, sensitive-skin").',
      required: !1
    }),
    detail_link: import_pieces_framework6.Property.ShortText({
      displayName: "Product Page URL",
      description: "The URL to the product page in your online store.",
      required: !1
    }),
    external_id: import_pieces_framework6.Property.ShortText({
      displayName: "External ID",
      description: "The product ID from your external system (e.g. Shopify product ID) for syncing.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m;
    let { title, price, status, sku, description, image, vendor, quantity, compare_at_price, tags, detail_link, external_id } = context.propsValue, body = {
      title,
      price,
      status
    };
    sku && (body.sku = sku), description && (body.description = description), image && (body.image = image), vendor && (body.vendor = vendor), quantity != null && (body.quantity = quantity), compare_at_price != null && (body.compare_at_price = compare_at_price), tags && (body.tags = tags.split(",").map((t) => t.trim())), detail_link && (body.detail_link = detail_link), external_id && (body.external_id = external_id);
    let response = await quizellApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.POST,
      path: "/products/store",
      body
    }), product = response.body.data;
    return {
      success: response.body.status,
      message: response.body.message,
      id: (_a = product.id) != null ? _a : null,
      title: (_b = product.title) != null ? _b : null,
      sku: (_c = product.sku) != null ? _c : null,
      price: (_d = product.price) != null ? _d : null,
      compare_at_price: (_e = product.compare_at_price) != null ? _e : null,
      status: product.status === 1 ? "active" : "inactive",
      description: (_f = product.description) != null ? _f : null,
      image: (_g = product.image) != null ? _g : null,
      vendor: (_h = product.vendor) != null ? _h : null,
      quantity: (_i = product.quantity) != null ? _i : null,
      tags: Array.isArray(product.tags) ? product.tags.join(", ") : (_j = product.tags) != null ? _j : null,
      detail_link: (_k = product.detail_link) != null ? _k : null,
      external_id: (_l = product.external_id) != null ? _l : null,
      created_at: (_m = product.created_at) != null ? _m : null
    };
  }
});

// ../ap/packages/pieces/community/quizell/src/lib/actions/update-product.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var updateProduct = (0, import_pieces_framework7.createAction)({
  auth: quizellAuth,
  name: "update_product",
  displayName: "Update Product",
  description: "Updates an existing product in your Quizell product catalog.",
  audience: "both",
  aiMetadata: { description: "Updates an existing Quizell catalog product, identified by product_id. Use to change price, stock, status, or other product fields on a known product; requires product_id, title, price, and status. Idempotent \u2014 re-sending the same fields leaves the product in the same state.", idempotent: !0 },
  props: {
    product_id: import_pieces_framework7.Property.ShortText({
      displayName: "Product ID",
      description: 'The ID of the product to update. Use the "Search Products" action to find product IDs.',
      required: !0
    }),
    title: import_pieces_framework7.Property.ShortText({
      displayName: "Product Title",
      description: "The name of the product.",
      required: !0
    }),
    price: import_pieces_framework7.Property.Number({
      displayName: "Price",
      description: "The selling price of the product (e.g. 29.99).",
      required: !0
    }),
    status: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Status",
      description: "Whether this product is active and visible in quizzes.",
      required: !0,
      defaultValue: 1,
      options: {
        options: [
          { label: "Active", value: 1 },
          { label: "Inactive", value: 0 }
        ]
      }
    }),
    sku: import_pieces_framework7.Property.ShortText({
      displayName: "SKU",
      description: "Your internal stock keeping unit code for this product.",
      required: !1
    }),
    description: import_pieces_framework7.Property.LongText({
      displayName: "Description",
      description: "A description of the product.",
      required: !1
    }),
    image: import_pieces_framework7.Property.ShortText({
      displayName: "Image URL",
      description: "A publicly accessible URL to the product image.",
      required: !1
    }),
    vendor: import_pieces_framework7.Property.ShortText({
      displayName: "Vendor / Brand",
      description: "The brand or vendor name for this product.",
      required: !1
    }),
    quantity: import_pieces_framework7.Property.Number({
      displayName: "Quantity in Stock",
      description: "The available quantity for this product.",
      required: !1
    }),
    compare_at_price: import_pieces_framework7.Property.Number({
      displayName: "Compare-At Price",
      description: "The original price to show a discount. Leave empty if there is no sale.",
      required: !1
    }),
    tags: import_pieces_framework7.Property.ShortText({
      displayName: "Tags",
      description: 'Comma-separated tags to categorize this product (e.g. "skincare, moisturizer").',
      required: !1
    }),
    detail_link: import_pieces_framework7.Property.ShortText({
      displayName: "Product Page URL",
      description: "The URL to the product page in your online store.",
      required: !1
    }),
    external_id: import_pieces_framework7.Property.ShortText({
      displayName: "External ID",
      description: "The product ID from your external system for syncing.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m;
    let { product_id, title, price, status, sku, description, image, vendor, quantity, compare_at_price, tags, detail_link, external_id } = context.propsValue, body = {
      title,
      price,
      status
    };
    sku && (body.sku = sku), description && (body.description = description), image && (body.image = image), vendor && (body.vendor = vendor), quantity != null && (body.quantity = quantity), compare_at_price != null && (body.compare_at_price = compare_at_price), tags && (body.tags = tags.split(",").map((t) => t.trim())), detail_link && (body.detail_link = detail_link), external_id && (body.external_id = external_id);
    let response = await quizellApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.PUT,
      path: `/products/update/${product_id}`,
      body
    }), product = response.body.data;
    return {
      success: response.body.status,
      message: response.body.message,
      id: (_a = product.id) != null ? _a : null,
      title: (_b = product.title) != null ? _b : null,
      sku: (_c = product.sku) != null ? _c : null,
      price: (_d = product.price) != null ? _d : null,
      compare_at_price: (_e = product.compare_at_price) != null ? _e : null,
      status: product.status === 1 ? "active" : "inactive",
      description: (_f = product.description) != null ? _f : null,
      image: (_g = product.image) != null ? _g : null,
      vendor: (_h = product.vendor) != null ? _h : null,
      quantity: (_i = product.quantity) != null ? _i : null,
      tags: Array.isArray(product.tags) ? product.tags.join(", ") : (_j = product.tags) != null ? _j : null,
      detail_link: (_k = product.detail_link) != null ? _k : null,
      external_id: (_l = product.external_id) != null ? _l : null,
      updated_at: (_m = product.updated_at) != null ? _m : null
    };
  }
});

// ../ap/packages/pieces/community/quizell/src/index.ts
var quizellAuth = import_pieces_framework8.PieceAuth.SecretText({
  displayName: "API Token",
  description: `To get your Quizell API token:
1. Log in to your [Quizell](https://quizell.com) account.
2. Go to **Settings** in the left sidebar.
3. Navigate to **API & Integrations**.
4. Copy your API token from there.

Need help? See [Quizell API Docs](https://docs.quizell.com).`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await quizellApiCall({
        token: auth,
        method: import_pieces_common10.HttpMethod.GET,
        path: "/customers/list",
        queryParams: { per_page: "1", page: "1" }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API token. Please check your credentials and try again." };
    }
  }
}), quizell = (0, import_pieces_framework8.createPiece)({
  displayName: "Quizell",
  description: "Quiz-based product recommendation and lead capture platform.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/quizell.png",
  categories: [import_pieces_framework9.PieceCategory.MARKETING, import_pieces_framework9.PieceCategory.COMMERCE],
  auth: quizellAuth,
  authors: ["sanket-a11y"],
  actions: [
    createCustomer,
    updateCustomer,
    listCustomers,
    getCustomer,
    searchProducts,
    createProduct,
    updateProduct,
    (0, import_pieces_common9.createCustomApiCallAction)({
      baseUrl: () => "https://api.quizell.com/api/v1",
      auth: quizellAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  quizell,
  quizellAuth
});
