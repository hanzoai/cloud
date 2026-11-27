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

// ../ap/packages/pieces/community/katana/src/index.ts
var index_exports = {};
__export(index_exports, {
  katana: () => katana
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/katana/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/katana/src/lib/common/constants.ts
var BASE_URL = "https://api.katanamrp.com/v1";

// ../ap/packages/pieces/community/katana/src/lib/common/auth.ts
var katanaAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: `
To generate your API key:

1. Log in to your Katana account
2. Go to **Settings > API**
3. Select **Add new API key**
4. Copy and paste the key here
`,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${BASE_URL}/products`,
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        }
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key"
      };
    }
  }
});

// ../ap/packages/pieces/community/katana/src/lib/actions/create-customer.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var createCustomer = (0, import_pieces_framework2.createAction)({
  auth: katanaAuth,
  name: "create_customer",
  displayName: "Create Customer",
  description: "Creates a new customer in Katana.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new customer record in the Katana manufacturing ERP, optionally with billing/shipping addresses. Use when registering a new customer; only the display name is required. Not idempotent \u2014 each call creates a separate customer, so look the customer up first if you need to avoid duplicates.",
    idempotent: !1
  },
  props: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      description: "Customer display name.",
      required: !0
    }),
    first_name: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    company: import_pieces_framework2.Property.ShortText({
      displayName: "Company",
      required: !1
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    currency: import_pieces_framework2.Property.ShortText({
      displayName: "Currency",
      description: "Currency code (ISO 4217), e.g. USD, EUR, GBP.",
      required: !1
    }),
    reference_id: import_pieces_framework2.Property.ShortText({
      displayName: "Reference ID",
      description: "External reference ID (max 100 characters).",
      required: !1
    }),
    category: import_pieces_framework2.Property.ShortText({
      displayName: "Category",
      description: "Customer category (max 100 characters).",
      required: !1
    }),
    comment: import_pieces_framework2.Property.LongText({
      displayName: "Comment",
      description: "Additional notes about the customer.",
      required: !1
    }),
    discount_rate: import_pieces_framework2.Property.Number({
      displayName: "Discount Rate",
      description: "Default discount rate for this customer.",
      required: !1
    }),
    addresses: import_pieces_framework2.Property.Array({
      displayName: "Addresses",
      required: !1,
      properties: {
        entity_type: import_pieces_framework2.Property.StaticDropdown({
          displayName: "Address Type",
          required: !0,
          options: {
            options: [
              { label: "Billing", value: "billing" },
              { label: "Shipping", value: "shipping" }
            ]
          }
        }),
        default: import_pieces_framework2.Property.Checkbox({
          displayName: "Set as Default",
          required: !1
        }),
        first_name: import_pieces_framework2.Property.ShortText({
          displayName: "First Name",
          required: !1
        }),
        last_name: import_pieces_framework2.Property.ShortText({
          displayName: "Last Name",
          required: !1
        }),
        company: import_pieces_framework2.Property.ShortText({
          displayName: "Company",
          required: !1
        }),
        phone: import_pieces_framework2.Property.ShortText({
          displayName: "Phone",
          required: !1
        }),
        line_1: import_pieces_framework2.Property.ShortText({
          displayName: "Address Line 1",
          required: !1
        }),
        line_2: import_pieces_framework2.Property.ShortText({
          displayName: "Address Line 2",
          required: !1
        }),
        city: import_pieces_framework2.Property.ShortText({
          displayName: "City",
          required: !1
        }),
        state: import_pieces_framework2.Property.ShortText({
          displayName: "State",
          required: !1
        }),
        zip: import_pieces_framework2.Property.ShortText({
          displayName: "Zip/Postal Code",
          required: !1
        }),
        country: import_pieces_framework2.Property.ShortText({
          displayName: "Country",
          required: !1
        })
      }
    })
  },
  async run(context) {
    let {
      name,
      first_name,
      last_name,
      company,
      email,
      phone,
      currency,
      reference_id,
      category,
      comment,
      discount_rate,
      addresses
    } = context.propsValue, body = {
      name
    };
    return first_name && (body.first_name = first_name), last_name && (body.last_name = last_name), company && (body.company = company), email && (body.email = email), phone && (body.phone = phone), currency && (body.currency = currency), reference_id && (body.reference_id = reference_id), category && (body.category = category), comment && (body.comment = comment), discount_rate != null && (body.discount_rate = discount_rate), addresses && addresses.length > 0 && (body.addresses = addresses.map((addr) => {
      let address = {
        entity_type: addr.entity_type
      };
      return addr.default !== void 0 && (address.default = addr.default), addr.first_name && (address.first_name = addr.first_name), addr.last_name && (address.last_name = addr.last_name), addr.company && (address.company = addr.company), addr.phone && (address.phone = addr.phone), addr.line_1 && (address.line_1 = addr.line_1), addr.line_2 && (address.line_2 = addr.line_2), addr.city && (address.city = addr.city), addr.state && (address.state = addr.state), addr.zip && (address.zip = addr.zip), addr.country && (address.country = addr.country), address;
    })), (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${BASE_URL}/customers`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/katana/src/lib/actions/create-sales-order.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/katana/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var customerDropdown = import_pieces_framework3.Property.Dropdown({
  displayName: "Customer",
  description: "Select the customer.",
  required: !0,
  refreshers: [],
  auth: katanaAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      };
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.GET,
        url: `${BASE_URL}/customers`,
        authentication: {
          type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
          token: auth.secret_text
        }
      });
      return {
        disabled: !1,
        options: (Array.isArray(response.body) ? response.body : []).map((customer) => ({
          label: customer.name,
          value: customer.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        placeholder: "Failed to load customers",
        options: []
      };
    }
  }
}), variantDropdown = import_pieces_framework3.Property.Dropdown({
  displayName: "Product Variant",
  description: "Select the product variant.",
  required: !0,
  refreshers: [],
  auth: katanaAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      };
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.GET,
        url: `${BASE_URL}/variants`,
        authentication: {
          type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
          token: auth.secret_text
        }
      });
      return {
        disabled: !1,
        options: (Array.isArray(response.body) ? response.body : []).map((variant) => ({
          label: variant.sku || variant.name || `Variant ${variant.id}`,
          value: variant.id
        }))
      };
    } catch (e) {
      return {
        disabled: !0,
        placeholder: "Failed to load variants",
        options: []
      };
    }
  }
}), taxRateDropdown = import_pieces_framework3.Property.Dropdown({
  displayName: "Tax Rate",
  required: !1,
  refreshers: [],
  auth: katanaAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      };
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.GET,
        url: `${BASE_URL}/tax_rates`,
        authentication: {
          type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
          token: auth.secret_text
        }
      });
      return {
        disabled: !1,
        options: (Array.isArray(response.body) ? response.body : []).map((rate) => ({
          label: `${rate.name} (${rate.percentage}%)`,
          value: rate.id
        }))
      };
    } catch (e) {
      return {
        disabled: !1,
        placeholder: "No tax rates available",
        options: []
      };
    }
  }
}), locationDropdown = import_pieces_framework3.Property.Dropdown({
  displayName: "Location",
  required: !1,
  refreshers: [],
  auth: katanaAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      };
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.GET,
        url: `${BASE_URL}/locations`,
        authentication: {
          type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
          token: auth.secret_text
        }
      });
      return {
        disabled: !1,
        options: (Array.isArray(response.body) ? response.body : []).map((location) => ({
          label: location.name,
          value: location.id
        }))
      };
    } catch (e) {
      return {
        disabled: !1,
        placeholder: "No locations available",
        options: []
      };
    }
  }
}), locationDropdownRequired = import_pieces_framework3.Property.Dropdown({
  displayName: "Default Location",
  description: "Default location for the order.",
  required: !0,
  refreshers: [],
  auth: katanaAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your account first",
        options: []
      };
    try {
      let response = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.GET,
        url: `${BASE_URL}/locations`,
        authentication: {
          type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
          token: auth.secret_text
        }
      });
      return {
        disabled: !1,
        options: (Array.isArray(response.body) ? response.body : []).map((location) => ({
          label: location.name,
          value: location.id
        }))
      };
    } catch (e) {
      return {
        disabled: !1,
        placeholder: "No locations available",
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/katana/src/lib/actions/create-sales-order.ts
var createSalesOrder = (0, import_pieces_framework4.createAction)({
  auth: katanaAuth,
  name: "create_sales_order",
  displayName: "Create Sales Order",
  description: "Creates a new sales order in Katana.",
  audience: "both",
  aiMetadata: {
    description: "Creates a sales order (or quote, when status is PENDING) in the Katana manufacturing ERP for an existing customer. Use when placing an order; it requires an order number, a customer ID, and at least one line item (the primary product variant plus quantity), with extra line items optionally passed as a JSON array. Not idempotent \u2014 each call creates a new order, so a repeated call yields a duplicate unless the order number is rejected as already used.",
    idempotent: !1
  },
  props: {
    order_no: import_pieces_framework4.Property.ShortText({
      displayName: "Order Number",
      description: "Unique order number.",
      required: !0
    }),
    customer_id: customerDropdown,
    // Simple mode: single product variant
    variant_id: variantDropdown,
    quantity: import_pieces_framework4.Property.Number({
      displayName: "Quantity",
      description: "Quantity for the product variant.",
      required: !0
    }),
    price_per_unit: import_pieces_framework4.Property.Number({
      displayName: "Price Per Unit",
      description: "Override price per unit.",
      required: !1
    }),
    total_discount: import_pieces_framework4.Property.Number({
      displayName: "Total Discount",
      description: "Discount amount for this line.",
      required: !1
    }),
    tax_rate_id: taxRateDropdown,
    additional_order_rows: import_pieces_framework4.Property.Json({
      displayName: "Additional Order Lines (JSON)",
      description: "Add more line items as JSON array. Each object needs: variant_id, quantity. Optional: price_per_unit, total_discount, tax_rate_id, location_id.",
      required: !1
    }),
    status: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Status",
      description: "Order status. Use PENDING for quotes.",
      required: !1,
      options: {
        options: [
          { label: "Not Shipped", value: "NOT_SHIPPED" },
          { label: "Pending (Quote)", value: "PENDING" }
        ]
      }
    }),
    location_id: locationDropdown,
    currency: import_pieces_framework4.Property.ShortText({
      displayName: "Currency",
      description: "Currency code (ISO 4217), e.g. USD, EUR.",
      required: !1
    }),
    order_created_date: import_pieces_framework4.Property.DateTime({
      displayName: "Order Created Date",
      required: !1
    }),
    delivery_date: import_pieces_framework4.Property.DateTime({
      displayName: "Delivery Date",
      required: !1
    }),
    customer_ref: import_pieces_framework4.Property.ShortText({
      displayName: "Customer Reference",
      description: "Customer reference number (max 255 characters).",
      required: !1
    }),
    additional_info: import_pieces_framework4.Property.LongText({
      displayName: "Additional Info",
      description: "Additional notes for this order.",
      required: !1
    }),
    tracking_number: import_pieces_framework4.Property.ShortText({
      displayName: "Tracking Number",
      description: "Shipping tracking number (max 256 characters).",
      required: !1
    }),
    tracking_number_url: import_pieces_framework4.Property.ShortText({
      displayName: "Tracking URL",
      description: "URL to track the shipment.",
      required: !1
    }),
    addresses: import_pieces_framework4.Property.Array({
      displayName: "Addresses",
      required: !1,
      properties: {
        entity_type: import_pieces_framework4.Property.StaticDropdown({
          displayName: "Address Type",
          required: !0,
          options: {
            options: [
              { label: "Billing", value: "billing" },
              { label: "Shipping", value: "shipping" }
            ]
          }
        }),
        first_name: import_pieces_framework4.Property.ShortText({
          displayName: "First Name",
          required: !1
        }),
        last_name: import_pieces_framework4.Property.ShortText({
          displayName: "Last Name",
          required: !1
        }),
        company: import_pieces_framework4.Property.ShortText({
          displayName: "Company",
          required: !1
        }),
        phone: import_pieces_framework4.Property.ShortText({
          displayName: "Phone",
          required: !1
        }),
        line_1: import_pieces_framework4.Property.ShortText({
          displayName: "Address Line 1",
          required: !1
        }),
        line_2: import_pieces_framework4.Property.ShortText({
          displayName: "Address Line 2",
          required: !1
        }),
        city: import_pieces_framework4.Property.ShortText({
          displayName: "City",
          required: !1
        }),
        state: import_pieces_framework4.Property.ShortText({
          displayName: "State",
          required: !1
        }),
        zip: import_pieces_framework4.Property.ShortText({
          displayName: "Zip/Postal Code",
          required: !1
        }),
        country: import_pieces_framework4.Property.ShortText({
          displayName: "Country",
          required: !1
        })
      }
    }),
    ecommerce_order_type: import_pieces_framework4.Property.ShortText({
      displayName: "E-commerce Order Type",
      description: "Source platform (e.g. shopify).",
      required: !1
    }),
    ecommerce_store_name: import_pieces_framework4.Property.ShortText({
      displayName: "E-commerce Store Name",
      description: "Store identifier.",
      required: !1
    }),
    ecommerce_order_id: import_pieces_framework4.Property.ShortText({
      displayName: "E-commerce Order ID",
      description: "External order ID from the e-commerce platform.",
      required: !1
    })
  },
  async run(context) {
    let {
      order_no,
      customer_id,
      variant_id,
      quantity,
      price_per_unit,
      total_discount,
      tax_rate_id,
      additional_order_rows,
      status,
      location_id,
      currency,
      order_created_date,
      delivery_date,
      customer_ref,
      additional_info,
      tracking_number,
      tracking_number_url,
      addresses,
      ecommerce_order_type,
      ecommerce_store_name,
      ecommerce_order_id
    } = context.propsValue, firstRow = {
      variant_id,
      quantity
    };
    price_per_unit != null && (firstRow.price_per_unit = price_per_unit), total_discount != null && (firstRow.total_discount = total_discount), tax_rate_id && (firstRow.tax_rate_id = tax_rate_id), location_id && (firstRow.location_id = location_id);
    let salesOrderRows = [firstRow];
    if (additional_order_rows) {
      let additionalRows = Array.isArray(additional_order_rows) ? additional_order_rows : [];
      for (let row of additionalRows) {
        let orderRow = {
          variant_id: row.variant_id,
          quantity: row.quantity
        };
        row.price_per_unit !== void 0 && (orderRow.price_per_unit = row.price_per_unit), row.total_discount !== void 0 && (orderRow.total_discount = row.total_discount), row.tax_rate_id && (orderRow.tax_rate_id = row.tax_rate_id), row.location_id && (orderRow.location_id = row.location_id), salesOrderRows.push(orderRow);
      }
    }
    let body = {
      order_no,
      customer_id,
      sales_order_rows: salesOrderRows
    };
    return status && (body.status = status), location_id && (body.location_id = location_id), currency && (body.currency = currency), order_created_date && (body.order_created_date = order_created_date), delivery_date && (body.delivery_date = delivery_date), customer_ref && (body.customer_ref = customer_ref), additional_info && (body.additional_info = additional_info), tracking_number && (body.tracking_number = tracking_number), tracking_number_url && (body.tracking_number_url = tracking_number_url), ecommerce_order_type && (body.ecommerce_order_type = ecommerce_order_type), ecommerce_store_name && (body.ecommerce_store_name = ecommerce_store_name), ecommerce_order_id && (body.ecommerce_order_id = ecommerce_order_id), addresses && addresses.length > 0 && (body.addresses = addresses.map((addr) => {
      let address = {
        entity_type: addr.entity_type
      };
      return addr.first_name && (address.first_name = addr.first_name), addr.last_name && (address.last_name = addr.last_name), addr.company && (address.company = addr.company), addr.phone && (address.phone = addr.phone), addr.line_1 && (address.line_1 = addr.line_1), addr.line_2 && (address.line_2 = addr.line_2), addr.city && (address.city = addr.city), addr.state && (address.state = addr.state), addr.zip && (address.zip = addr.zip), addr.country && (address.country = addr.country), address;
    })), (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${BASE_URL}/sales_orders`,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/katana/src/lib/actions/find-customer.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var findCustomer = (0, import_pieces_framework5.createAction)({
  auth: katanaAuth,
  name: "find_customer",
  displayName: "Find Customer",
  description: "Find a customer by name, email, or phone number.",
  audience: "both",
  aiMetadata: {
    description: "Searches customers in the Katana manufacturing ERP, filtering by any combination of name, first/last name, company, email, phone, currency, reference ID, or category; with no filters set it lists all customers (paginated). Use to look up a customer ID before creating an order, to check whether a customer already exists, or to browse the customer list. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Name",
      description: "Filters customers by name.",
      required: !1
    }),
    first_name: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      description: "Filters customers by first name.",
      required: !1
    }),
    last_name: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      description: "Filters customers by last name.",
      required: !1
    }),
    company: import_pieces_framework5.Property.ShortText({
      displayName: "Company",
      description: "Filters customers by company.",
      required: !1
    }),
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      description: "Filters customers by email.",
      required: !1
    }),
    phone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone",
      description: "Filters customers by phone number.",
      required: !1
    }),
    currency: import_pieces_framework5.Property.ShortText({
      displayName: "Currency",
      description: "Filters customers by currency.",
      required: !1
    }),
    reference_id: import_pieces_framework5.Property.ShortText({
      displayName: "Reference ID",
      description: "Filters customers by reference ID.",
      required: !1
    }),
    category: import_pieces_framework5.Property.ShortText({
      displayName: "Category",
      description: "Filters customers by category.",
      required: !1
    }),
    include_deleted: import_pieces_framework5.Property.Checkbox({
      displayName: "Include Deleted",
      description: "Include soft-deleted customers in results.",
      required: !1
    }),
    limit: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "Maximum number of results to return (default is 50).",
      required: !1
    }),
    page: import_pieces_framework5.Property.Number({
      displayName: "Page",
      description: "Page number for pagination (default is 1).",
      required: !1
    })
  },
  async run(context) {
    let {
      name,
      first_name,
      last_name,
      company,
      email,
      phone,
      currency,
      reference_id,
      category,
      include_deleted,
      limit,
      page
    } = context.propsValue, queryParams = {};
    return name && (queryParams.name = name), first_name && (queryParams.first_name = first_name), last_name && (queryParams.last_name = last_name), company && (queryParams.company = company), email && (queryParams.email = email), phone && (queryParams.phone = phone), currency && (queryParams.currency = currency), reference_id && (queryParams.reference_id = reference_id), category && (queryParams.category = category), include_deleted !== void 0 && (queryParams.include_deleted = include_deleted.toString()), limit !== void 0 && (queryParams.limit = limit.toString()), page !== void 0 && (queryParams.page = page.toString()), (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `${BASE_URL}/customers`,
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      },
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/katana/src/index.ts
var katana = (0, import_pieces_framework6.createPiece)({
  displayName: "Katana",
  auth: katanaAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/katana.png",
  description: "Katana is a cloud-based manufacturing ERP software for inventory, production, and order management.",
  categories: [import_pieces_framework7.PieceCategory.PRODUCTIVITY],
  authors: ["onyedikachi-david"],
  actions: [
    createCustomer,
    createSalesOrder,
    findCustomer,
    (0, import_pieces_common6.createCustomApiCallAction)({
      auth: katanaAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  katana
});
