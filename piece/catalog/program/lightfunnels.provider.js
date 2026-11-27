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

// ../ap/packages/pieces/community/lightfunnels/src/index.ts
var index_exports = {};
__export(index_exports, {
  lightfunnels: () => lightfunnels
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_framework16 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lightfunnels/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_framework2 = require("@activepieces/pieces-framework"), lightfunnelsAuth = import_pieces_framework.PieceAuth.OAuth2({
  grantType: import_pieces_framework2.OAuth2GrantType.AUTHORIZATION_CODE,
  authUrl: "https://app.lightfunnels.com/admin/oauth",
  tokenUrl: "https://services.lightfunnels.com/oauth/access",
  required: !0,
  scope: ["products,orders,customers,funnels"]
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/triggers/new-order.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lightfunnels/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), lightfunnelsCommon = {
  baseUrl: "https://services.lightfunnels.com/api/v2",
  async makeGraphQLRequest(auth, query, variables) {
    let response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: this.baseUrl,
      headers: {
        Authorization: `Bearer ${auth.access_token}`,
        "Content-Type": "application/json"
      },
      body: {
        query,
        variables
      }
    });
    if (response.body.errors)
      throw new Error(JSON.stringify(response.body.errors));
    return response.body;
  }
};

// ../ap/packages/pieces/community/lightfunnels/src/lib/triggers/new-order.ts
var newOrderTrigger = (0, import_pieces_framework3.createTrigger)({
  auth: lightfunnelsAuth,
  name: "new_order",
  displayName: "New Order",
  description: "Triggers when a new order is created",
  aiMetadata: {
    description: "Fires when an order is confirmed in Lightfunnels (the order/confirmed webhook event), emitting the new order record. Use to react to incoming sales such as fulfillment, notifications, or downstream sync."
  },
  props: {},
  type: import_pieces_framework3.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let query = `
      mutation CreateWebhookMutation($node: WebhookInput!) {
        createWebhook(node: $node) {
          id
          type
          settings
          url
        }
      }
    `, variables = {
      node: {
        type: "order/confirmed",
        url: context.webhookUrl,
        settings: {}
      }
    }, response = await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      query,
      variables
    );
    await context.store.put("webhook_id", response.data.createWebhook.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhook_id");
    webhookId && (await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      `
        mutation webhooksDeleteMutation($id: ID!) {
          deleteWebhook(id: $id)
        }
      `,
      { id: webhookId }
    ), await context.store.delete("webhook_id"));
  },
  sampleData: {
    node: {
      id: "order_123",
      __typename: "Order",
      _id: 123,
      total: 100,
      account_id: "acc_123",
      subtotal: 90,
      discount_value: 10,
      normal_discount_value: 10,
      bundle_discount_value: 0,
      pm_discount_value: 0,
      pm_extra_fees: 0,
      name: "#1001",
      notes: "",
      email: "customer@example.com",
      phone: "+1234567890",
      archived_at: null,
      refunded_amount: 0,
      paid_by_customer: 100,
      net_payment: 100,
      original_total: 100,
      refundable: 100,
      created_at: "2023-01-01T00:00:00Z",
      cancelled_at: null,
      test: !1,
      tags: [],
      shipping: 10,
      shipping_discount: 0,
      funnel_id: "funnel_123",
      store_id: null,
      customer: {
        id: "cust_123",
        full_name: "John Doe",
        avatar: "",
        location: ""
      },
      custom: {},
      items: [],
      payments: [],
      shipping_address: {
        first_name: "John",
        last_name: "Doe",
        email: "john@example.com",
        phone: "+1234567890",
        line1: "123 Main St",
        line2: "",
        country: "US",
        city: "New York",
        area: "",
        zip: "10001",
        state: "NY"
      },
      billing_address: {
        first_name: "John",
        last_name: "Doe",
        email: "john@example.com",
        phone: "+1234567890",
        line1: "123 Main St",
        line2: "",
        country: "US",
        city: "New York",
        area: "",
        zip: "10001",
        state: "NY"
      },
      client_details: {
        ip: "127.0.0.1"
      },
      utm: null,
      currency: "USD"
    }
  },
  async run(context) {
    return [context.payload.body.node];
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/triggers/new-lead.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var newLeadTrigger = (0, import_pieces_framework4.createTrigger)({
  auth: lightfunnelsAuth,
  name: "new_lead",
  displayName: "New Lead",
  description: "Triggers when a new lead subscribes",
  aiMetadata: {
    description: "Fires when a new lead signs up in Lightfunnels (the contact/signup webhook event), emitting the new customer/contact record. Use to react to newly captured leads such as CRM sync, welcome emails, or list enrollment."
  },
  props: {},
  type: import_pieces_framework4.TriggerStrategy.WEBHOOK,
  sampleData: {
    node: {
      id: "cus_dummy_id",
      __typename: "Customer",
      email: "dummy@example.com",
      first_name: "John",
      last_name: "Doe",
      full_name: "John Doe",
      phone: "+1 00000-00000",
      location: "US",
      avatar: "//www.gravatar.com/avatar/00000000000000000000000000000000",
      notes: "Sample customer notes",
      accepts_marketing: !1,
      custom: {},
      tags: [],
      shipping_address: {
        first_name: "John",
        last_name: "Doe",
        email: "dummy@example.com",
        phone: "+1 00000-00000",
        line1: "123 Dummy Street",
        line2: "",
        country: "US",
        city: "Sample City",
        area: "",
        zip: "000000",
        state: "CA"
      },
      billing_address: {
        first_name: "John",
        last_name: "Doe",
        email: "dummy@example.com",
        phone: "+1 00000-00000",
        line1: "123 Dummy Street",
        line2: "",
        country: "US",
        city: "Sample City",
        area: "",
        zip: "000000",
        state: "CA"
      },
      leads: []
    }
  },
  async onEnable(context) {
    let query = `
      mutation CreateWebhookMutation($node: WebhookInput!) {
        createWebhook(node: $node) {
          id
          type
          settings
          url
        }
      }
    `, variables = {
      node: {
        type: "contact/signup",
        url: context.webhookUrl,
        settings: {}
      }
    }, response = await lightfunnelsCommon.makeGraphQLRequest(context.auth, query, variables);
    await context.store.put("webhook_id", response.data.createWebhook.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhook_id");
    webhookId && (await lightfunnelsCommon.makeGraphQLRequest(context.auth, `
        mutation webhooksDeleteMutation($id: ID!) {
          deleteWebhook(id: $id)
        }
      `, {
      id: webhookId
    }), await context.store.delete("webhook_id"));
  },
  async run(context) {
    return [context.payload.body.node];
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/actions/list-products.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var listProducts = (0, import_pieces_framework5.createAction)({
  auth: lightfunnelsAuth,
  name: "list_products",
  displayName: "List Products",
  description: "Retrieve a list of all products",
  audience: "both",
  aiMetadata: { description: 'Lists products in a Lightfunnels store, with optional pagination (limit/cursor) and a Lightfunnels query string to filter, sort, and scope results (e.g. "order_by:id order_dir:desc"); an empty query returns all products in default order. Use to browse the catalog or find products by criteria. Read-only and idempotent.', idempotent: !0 },
  props: {
    first: import_pieces_framework5.Property.Number({
      displayName: "Limit",
      description: "Number of products to retrieve (default: 10)",
      required: !1,
      defaultValue: 10
    }),
    after: import_pieces_framework5.Property.ShortText({
      displayName: "After Cursor",
      description: "Cursor for pagination (optional)",
      required: !1
    }),
    query: import_pieces_framework5.Property.ShortText({
      displayName: "Query",
      description: 'Filter query (e.g., "order_by:id order_dir:desc last_month")',
      required: !1,
      defaultValue: "order_by:id order_dir:desc"
    })
  },
  async run(context) {
    let { first, after, query } = context.propsValue;
    return (await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      `
      query productsQuery($first: Int, $after: String, $query: String!) {
        products(query: $query, after: $after, first: $first) {
          edges {
            node {
              id
              _id
              uid
              slug
              title
              description
              notice_text
              price
              sku
              file_id
              compare_at_price
              product_type
              thumbnail {
                id
                path
              }
              file {
                id
                title
                size
                path
              }
              tags {
                id
                title
              }
              variants {
                id
                price
                compare_at_price
                sku
                enable_inventory_limit
                inventory_quantity
              }
              inventory_quantity
              enable_inventory_limit
              created_at
              updated_at
            }
            cursor
          }
          pageInfo {
            endCursor
            hasNextPage
          }
        }
      }
    `,
      {
        first: first || 10,
        after: after || null,
        query: query || "order_by:id order_dir:desc"
      }
    )).data.products;
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/actions/get-product.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var getProduct = (0, import_pieces_framework6.createAction)({
  auth: lightfunnelsAuth,
  name: "get_product",
  displayName: "Get Product",
  description: "Retrieve a specific product by ID",
  audience: "both",
  aiMetadata: { description: "Fetches the full details of a single Lightfunnels product by its node ID, including variants, options, pricing, and related funnels/stores. Use when you already have a product ID and need its complete record; use List Products first if you only have a name or other criteria. Read-only and idempotent.", idempotent: !0 },
  props: {
    productId: import_pieces_framework6.Property.ShortText({
      displayName: "Product ID",
      description: "The ID of the product to retrieve",
      required: !0
    })
  },
  async run(context) {
    let { productId } = context.propsValue;
    return (await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      `
      query productQuery($id: ID!) {
        product: node(id: $id) {
          __typename
          ... on Product {
            __typename
            id
            _id
            uid
            slug
            title
            description
            notice_text
            updated_at
            price
            sku
            file_id
            compare_at_price
            product_type
            shipping_group_id
            price_bundle_id
            thumbnail {
              id
              path
            }
            file {
              id
              title
              size
              path
            }
            options {
              id
              label
              options
              type
              options_types {
                option_id
                value
              }
            }
            tags {
              id
              title
            }
            images_ids
            variants {
              id
              price
              file_id
              image_id
              compare_at_price
              sku
              options {
                option_id: id
                value
              }
              enable_inventory_limit
              inventory_quantity
            }
            order_bump {
              id
              title
              price
              sku
              enabled
              compare_at_price
              description
              image_id
              file_id
            }
            order_bumps {
              id
              order_bump_product_id
              variant_id
              is_checked
              product {
                id
                uid
                title
                description
                price
                compare_at_price
                sku
                file_id
              }
            }
            features {
              id
              title
              description
              image_id
            }
            testimonials {
              id
              name
              position
              comment
              image_id
            }
            faq {
              id
              question
              answer
            }
            enable_custom_options
            custom_options {
              id
              type
              name
              placeholder
            }
            price_bundle {
              id
              items {
                id
                label
                quantity
                discount_value
                discount_type
              }
            }
            default_variant {
              id
              uid: id
              file_uid: file_id
              _id
              price
              image {
                id
                path
              }
              compare_at_price
              sku
            }
            inventory_quantity
            enable_inventory_limit
            funnels {
              id
              uid: id
              name
              slug
            }
            stores {
              id
              uid: id
              name
              slug
            }
            created_at
          }
        }
      }
    `,
      { id: productId }
    )).data.product;
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/actions/create-product.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var createProduct = (0, import_pieces_framework7.createAction)({
  auth: lightfunnelsAuth,
  name: "create_product",
  displayName: "Create Product",
  description: "Create a new product",
  audience: "both",
  aiMetadata: { description: "Creates a new product in Lightfunnels with a title, price, and required options and variants arrays (each variant maps option values). Use to add a catalog item; not for updating an existing product. Not idempotent \u2014 each call creates a separate product even with identical input.", idempotent: !1 },
  props: {
    title: import_pieces_framework7.Property.ShortText({
      displayName: "Title",
      description: "The title of the product",
      required: !0
    }),
    price: import_pieces_framework7.Property.Number({
      displayName: "Price",
      description: "The price of the product",
      required: !0
    }),
    description: import_pieces_framework7.Property.LongText({
      displayName: "Description",
      description: "The description of the product",
      required: !1
    }),
    compare_at_price: import_pieces_framework7.Property.Number({
      displayName: "Compare at Price",
      description: "The original price before discount",
      required: !1
    }),
    sku: import_pieces_framework7.Property.ShortText({
      displayName: "SKU",
      description: "The SKU of the product",
      required: !1
    }),
    options: import_pieces_framework7.Property.Array({
      displayName: "Options",
      description: "Product options like size, color, etc.",
      required: !0,
      properties: {
        id: import_pieces_framework7.Property.ShortText({
          displayName: "Option ID",
          description: "Unique identifier for the option",
          required: !0
        }),
        label: import_pieces_framework7.Property.ShortText({
          displayName: "Label",
          description: 'The option label (e.g., "Color", "Size")',
          required: !1
        }),
        options: import_pieces_framework7.Property.Json({
          displayName: "Option Values",
          description: 'Available values for this option (e.g., [{"value": "Red"}, {"value": "Blue"}])',
          required: !0
        }),
        type: import_pieces_framework7.Property.StaticDropdown({
          displayName: "Type",
          description: "The type of option",
          required: !1,
          options: {
            disabled: !1,
            options: [
              { label: "Text", value: "text" },
              { label: "Color", value: "color" },
              { label: "Image", value: "image" }
            ]
          }
        })
      }
    }),
    variants: import_pieces_framework7.Property.Array({
      displayName: "Variants",
      description: "Product variants with their details",
      required: !0,
      properties: {
        id: import_pieces_framework7.Property.ShortText({
          displayName: "Variant ID",
          description: "Unique identifier for the variant",
          required: !0
        }),
        sku: import_pieces_framework7.Property.ShortText({
          displayName: "Variant SKU",
          description: "SKU for this specific variant",
          required: !1
        }),
        price: import_pieces_framework7.Property.Number({
          displayName: "Variant Price",
          description: "Price for this variant",
          required: !1
        }),
        compare_at_price: import_pieces_framework7.Property.Number({
          displayName: "Variant Compare at Price",
          description: "Original price for this variant",
          required: !1
        }),
        options: import_pieces_framework7.Property.Json({
          displayName: "Variant Options",
          description: "Option values for this variant (array of objects with id and value pairs)",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let {
      title,
      price,
      description,
      compare_at_price,
      sku,
      options,
      variants
    } = context.propsValue, graphqlQuery = `
      mutation mutationName($node: InputProduct!) {
        createProduct(node: $node) {
          id
          _id
          title
          description
          price
          compare_at_price
          sku
          options {
            id
            label
            
            type
          }
          variants {
            id
            sku
            price
            compare_at_price       
          }
          created_at
          updated_at
        }
      }
    `, inputOptions = (options || []).map((opt) => {
      let optionValues = (opt.options || []).map((o) => o.value);
      return {
        id: opt.id,
        label: opt.label || void 0,
        options: optionValues,
        type: opt.type || "text"
      };
    }), inputVariants = (variants || []).map((variant) => ({
      id: variant.id,
      sku: variant.sku || void 0,
      price: variant.price !== void 0 ? variant.price : price,
      compare_at_price: variant.compare_at_price || compare_at_price,
      options: (variant.options || []).map((opt) => ({
        id: opt.id,
        value: opt.value
      }))
    })), variables = {
      node: {
        title,
        price,
        description: description || void 0,
        compare_at_price: compare_at_price || void 0,
        sku: sku || void 0,
        options: inputOptions,
        variants: inputVariants
      }
    };
    return (await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      graphqlQuery,
      variables
    )).data.createProduct;
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/actions/get-order.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var getOrder = (0, import_pieces_framework8.createAction)({
  auth: lightfunnelsAuth,
  name: "get_order",
  displayName: "Get Order",
  description: "Retrieve a specific order by ID",
  audience: "both",
  aiMetadata: { description: "Fetches the full details of a single Lightfunnels order by its node ID, including line items, payments, refunds, customer, and shipping/billing addresses. Use when you have an order ID and need its complete record; use List Orders first if you only have other criteria. Read-only and idempotent.", idempotent: !0 },
  props: {
    orderId: import_pieces_framework8.Property.ShortText({
      displayName: "Order ID",
      description: "The ID of the order to retrieve",
      required: !0
    })
  },
  async run(context) {
    let { orderId } = context.propsValue;
    return (await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      `
      query viewOrderQuery($id: ID!) {
        order: node(id: $id) {
          __typename
          ... on Order {
            id
            _id
            total
            subtotal
            discount_value
            normal_discount_value
            bundle_discount_value
            pm_discount_value
            pm_extra_fees
            name
            notes
            email
            phone
            archived_at
            refunded_amount
            paid_by_customer
            net_payment
            original_total
            refundable
            created_at
            cancelled_at
            test
            tags
            shipping
            shipping_discount
            customer {
              id
              full_name
              avatar
              location
            }
            custom
            items {
              __typename
              ... on VariantSnapshot {
                product_id
                id
                _id
                image {
                  path
                  id
                }
                title
                price
                variant_id
                fulfillment_status
                carrier
                tracking_number
                tracking_link
                refund_id
                payment_id
                removed_at
                sku
                custom_options {
                  name
                  key
                  value
                  type
                }
                options {
                  id
                  label
                  value
                }
              }
            }
            payments {
              id
              _id
              total
              sub_total
              created_at
              refunded
              refundable
              price_bundle_snapshot {
                id
                value
                discount_result
                label
              }
              discount_snapshot {
                _id
                id
                type
                code
                value
                discount_result
              }
              refunds {
                id
                _id
                amount
                reason
              }
            }
            shipping_address {
              first_name
              last_name
              email
              phone
              line1
              line2
              country
              city
              area
              zip
              state
            }
            billing_address {
              first_name
              last_name
              email
              phone
              line1
              line2
              country
              city
              area
              zip
              state
            }
            client_details {
              ip
            }
            currency
          }
        }
      }
    `,
      { id: orderId }
    )).data.order;
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/actions/list-orders.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var listOrders = (0, import_pieces_framework9.createAction)({
  auth: lightfunnelsAuth,
  name: "list_orders",
  displayName: "List Orders",
  description: "Retrieve a list of orders",
  audience: "both",
  aiMetadata: { description: 'Lists orders in a Lightfunnels store, with optional pagination (limit/cursor) and a Lightfunnels query string to filter, sort, and scope results (e.g. "order_by:created_at order_dir:desc"); an empty query returns all orders in default order. Use to browse orders or find them by criteria. Read-only and idempotent.', idempotent: !0 },
  props: {
    first: import_pieces_framework9.Property.Number({
      displayName: "Limit",
      description: "Number of orders to retrieve (default: 10)",
      required: !1,
      defaultValue: 10
    }),
    after: import_pieces_framework9.Property.ShortText({
      displayName: "After Cursor",
      description: "Cursor for pagination (optional)",
      required: !1
    }),
    query: import_pieces_framework9.Property.ShortText({
      displayName: "Query",
      description: 'Filter query (e.g., "order_by:created_at order_dir:desc")',
      required: !1,
      defaultValue: "order_by:created_at order_dir:desc"
    })
  },
  async run(context) {
    let { first, after, query } = context.propsValue;
    return (await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      `
      query ordersQuery($first: Int, $after: String, $query: String!) {
        orders(first: $first, after: $after, query: $query) {
          edges {
            node {
              id
              _id
              name
              total
              fulfillment_status
              financial_status
              customer {
                full_name
                id
              }
              cancelled_at
              date: created_at
              formattedDate: created_at(format: "LLL")
              test
              currency
              checkout {
                id
                store {
                  id
                  name
                }
                funnel {
                  id
                  name
                }
              }
              tags
            }
            cursor
          }
          pageInfo {
            endCursor
            hasNextPage
            hasPreviousPage
            startCursor
          }
        }
      }
    `,
      {
        first: first || 10,
        after: after || null,
        query: query || "order_by:created_at order_dir:desc"
      }
    )).data.orders;
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/actions/cancel-order.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var cancelOrder = (0, import_pieces_framework10.createAction)({
  auth: lightfunnelsAuth,
  name: "cancel_order",
  displayName: "Cancel Order",
  description: "Cancel an order",
  audience: "both",
  aiMetadata: { description: "Cancels a Lightfunnels order by ID with a required reason, optionally notifying the customer and/or issuing a refund. Use to void an order; the order ID must reference an existing, cancellable order. Mutating and not idempotent \u2014 refund/notification side effects may recur if re-invoked on an already-cancelled order.", idempotent: !1 },
  props: {
    orderId: import_pieces_framework10.Property.ShortText({
      displayName: "Order ID",
      description: "The ID of the order to cancel",
      required: !0
    }),
    reason: import_pieces_framework10.Property.ShortText({
      displayName: "Reason",
      description: "Reason for cancellation",
      required: !0
    }),
    notifyCustomer: import_pieces_framework10.Property.Checkbox({
      displayName: "Notify Customer",
      description: "Whether to notify the customer",
      required: !1,
      defaultValue: !0
    }),
    refund: import_pieces_framework10.Property.Checkbox({
      displayName: "Refund",
      description: "Whether to refund the order",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { orderId, reason, notifyCustomer, refund } = context.propsValue, graphqlQuery = `
      mutation cancelOrderMutation($id: ID!, $reason: String!, $notifyCustomer: Boolean!, $refund: Boolean!) {
        cancelOrder(id: $id, reason: $reason, notifyCustomer: $notifyCustomer, refund: $refund) {
          id
          _id
          name
          cancelled_at
          financial_status
          fulfillment_status
        }
      }
    `, variables = {
      id: orderId,
      reason,
      notifyCustomer: notifyCustomer != null ? notifyCustomer : !0,
      refund: refund != null ? refund : !1
    };
    return (await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      graphqlQuery,
      variables
    )).data.cancelOrder;
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/actions/create-customer.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var createCustomer = (0, import_pieces_framework11.createAction)({
  auth: lightfunnelsAuth,
  name: "create_customer",
  displayName: "Create Customer",
  description: "Create a new customer",
  audience: "both",
  aiMetadata: { description: "Creates a new customer record in Lightfunnels with first/last name, email, phone, marketing-consent flag, and tags. Use to add a contact; not for updating an existing customer. Not idempotent \u2014 each call creates a separate customer even with identical input.", idempotent: !1 },
  props: {
    firstName: import_pieces_framework11.Property.ShortText({
      displayName: "First Name",
      description: "First name of the customer",
      required: !0
    }),
    lastName: import_pieces_framework11.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the customer",
      required: !0
    }),
    email: import_pieces_framework11.Property.ShortText({
      displayName: "Email",
      description: "Email of the customer",
      required: !0
    }),
    phone: import_pieces_framework11.Property.ShortText({
      displayName: "Phone",
      description: "Phone number of the customer eg: +1234567890",
      required: !0
    }),
    acceptsMarketing: import_pieces_framework11.Property.Checkbox({
      displayName: "Accepts Marketing",
      description: "Whether the customer accepts marketing communications",
      required: !0
    }),
    tags: import_pieces_framework11.Property.Array({
      displayName: "Tags",
      description: "Tags to associate with the customer",
      required: !0,
      properties: {
        tag: import_pieces_framework11.Property.ShortText({
          displayName: "Tag",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let { firstName, lastName, email, phone, acceptsMarketing, tags } = context.propsValue, graphqlQuery = `
      mutation createCustomerMutation($node: InputCustomer!) {
        createCustomer(node: $node) {
          id
          _id
          first_name
          last_name
          full_name
          email
          phone
          accepts_marketing
          tags
          created_at
          updated_at
        }
      }
    `, tagArray = (tags || []).map((t) => t.tag), variables = {
      node: {
        first_name: firstName,
        last_name: lastName,
        email,
        phone,
        accepts_marketing: acceptsMarketing,
        tags: tagArray
      }
    };
    return (await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      graphqlQuery,
      variables
    )).data.createCustomer;
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/actions/get-customer.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var getCustomer = (0, import_pieces_framework12.createAction)({
  auth: lightfunnelsAuth,
  name: "get_customer",
  displayName: "Get Customer",
  description: "Retrieve a specific customer by ID",
  audience: "both",
  aiMetadata: { description: "Fetches a single Lightfunnels customer by its node ID, including contact details, location, order count, and expenses. Use when you have a customer ID and need their record; use List Customers first if you only have other criteria. Read-only and idempotent.", idempotent: !0 },
  props: {
    customerId: import_pieces_framework12.Property.ShortText({
      displayName: "Customer ID",
      description: "The ID of the customer to retrieve",
      required: !0
    })
  },
  async run(context) {
    let { customerId } = context.propsValue;
    return (await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      `
      query CustomerQuery($id: ID!) {
        node(id: $id) {
          ... on Customer {
            id
            _id
            first_name
            last_name
            full_name
            email
            phone
            avatar
            location
            expenses
            orders_count
            created_at
            updated_at
          }
        }
      }
    `,
      { id: customerId }
    )).data.node;
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/actions/list-customers.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var listCustomers = (0, import_pieces_framework13.createAction)({
  auth: lightfunnelsAuth,
  name: "list_customers",
  displayName: "List Customers",
  description: "Retrieve a list of customers",
  audience: "both",
  aiMetadata: { description: 'Lists customers in a Lightfunnels store, with optional pagination (limit/cursor) and a Lightfunnels query string to filter, sort, and scope results (e.g. "order_by:created_at order_dir:desc"); an empty query returns all customers in default order. Use to browse contacts or find them by criteria. Read-only and idempotent.', idempotent: !0 },
  props: {
    first: import_pieces_framework13.Property.Number({
      displayName: "Limit",
      description: "Number of customers to retrieve (default: 10)",
      required: !1,
      defaultValue: 10
    }),
    after: import_pieces_framework13.Property.ShortText({
      displayName: "After Cursor",
      description: "Cursor for pagination (optional)",
      required: !1
    }),
    query: import_pieces_framework13.Property.ShortText({
      displayName: "Query",
      description: 'Filter query (e.g., "order_by:created_at order_dir:desc")',
      required: !1,
      defaultValue: "order_by:created_at order_dir:desc"
    })
  },
  async run(context) {
    let { first, after, query } = context.propsValue;
    return (await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      `
      query customersQuery($first: Int, $after: String, $query: String!) {
        customers(first: $first, after: $after, query: $query) {
          edges {
            node {
              id
              _id
              avatar
              email
              full_name
              phone
              updated_at
              created_at
              location
            }
            expenses
            orders_count
            cursor
          }
          pageInfo {
            endCursor
            hasNextPage
          }
        }
      }
    `,
      {
        first: first || 10,
        after: after || null,
        query: query || "order_by:created_at order_dir:desc"
      }
    )).data.customers;
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/lib/actions/get-funnel.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var getFunnel = (0, import_pieces_framework14.createAction)({
  auth: lightfunnelsAuth,
  name: "get_funnel",
  displayName: "Get Funnel",
  description: "Retrieve a specific funnel by ID",
  audience: "both",
  aiMetadata: { description: "Fetches a single Lightfunnels funnel by its node ID, returning its name, slug, and published status. Use when you have a funnel ID and need its record. Read-only and idempotent.", idempotent: !0 },
  props: {
    funnelId: import_pieces_framework14.Property.ShortText({
      displayName: "Funnel ID",
      description: "The ID of the funnel to retrieve",
      required: !0
    })
  },
  async run(context) {
    let { funnelId } = context.propsValue;
    return (await lightfunnelsCommon.makeGraphQLRequest(
      context.auth,
      `
      query FunnelQuery($id: ID!) {
        node(id: $id) {
          ... on Funnel {
            id
            _id
            name
            slug
            published
            created_at
            updated_at
          }
        }
      }
    `,
      { id: funnelId }
    )).data.node;
  }
});

// ../ap/packages/pieces/community/lightfunnels/src/index.ts
var lightfunnels = (0, import_pieces_framework15.createPiece)({
  displayName: "Lightfunnels",
  auth: lightfunnelsAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/lightfunnels.png",
  categories: [import_pieces_framework16.PieceCategory.SALES_AND_CRM, import_pieces_framework16.PieceCategory.COMMERCE],
  authors: ["aminefrira", "sanket-a11y"],
  actions: [
    listProducts,
    getProduct,
    createProduct,
    getOrder,
    listOrders,
    cancelOrder,
    createCustomer,
    getCustomer,
    listCustomers,
    getFunnel
  ],
  triggers: [newOrderTrigger, newLeadTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  lightfunnels
});
