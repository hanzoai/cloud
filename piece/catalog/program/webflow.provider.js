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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/webflow/src/index.ts
var index_exports = {};
__export(index_exports, {
  webflow: () => webflow,
  webflowAuth: () => webflowAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_framework16 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/webflow/src/lib/actions/create-collection-item.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/webflow/src/lib/common/props.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/webflow/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var WebflowApiClient = class {
  constructor(accessToken) {
    __publicField(this, "accessToken", accessToken);
  }
  async makeRequest(method, resourceUri, query, body = void 0) {
    let apiUrl = "https://api.webflow.com", params = {};
    if (query)
      for (let [key, value] of Object.entries(query))
        value != null && (params[key] = String(value));
    let request = {
      method,
      url: apiUrl + resourceUri,
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token: this.accessToken
      },
      queryParams: params,
      body
    };
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  }
  async listSites() {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, "/sites");
  }
  async listCollections(siteId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/sites/${siteId}/collections`);
  }
  async getCollection(collectionId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/collections/${collectionId}`);
  }
  async createCollectionItem(collectionId, request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      `/collections/${collectionId}/items`,
      void 0,
      request
    );
  }
  async updateCollectionItem(collectionId, itemId, request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.PUT,
      `/collections/${collectionId}/items/${itemId}`,
      void 0,
      request
    );
  }
  async getCollectionItem(collectionId, itemId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/collections/${collectionId}/items/${itemId}`);
  }
  async deleteCollectionItem(collectionId, itemId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.DELETE,
      `/collections/${collectionId}/items/${itemId}`
    );
  }
  async publishCollectionItem(collectionId, itemId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      `/collections/${collectionId}/items/publish`,
      void 0,
      { itemIds: [itemId] }
    );
  }
  async listCollectionItems(collectionId, page, limit) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/collections/${collectionId}/items`, {
      offset: page,
      limit
    });
  }
  async getOrder(siteId, orderId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/sites/${siteId}/orders/${orderId}`);
  }
  async fulfillOrder(siteId, orderId, request) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      `/sites/${siteId}/orders/${orderId}/fulfill`,
      void 0,
      request
    );
  }
  async unfulfillOrder(siteId, orderId) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      `/sites/${siteId}/orders/${orderId}/unfulfill`,
      void 0
    );
  }
  async refundOrder(siteId, orderId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.POST, `/sites/${siteId}/orders/${orderId}/refund`);
  }
  async listOrders(siteId, page, limit) {
    return await this.makeRequest(import_pieces_common.HttpMethod.GET, `/sites/${siteId}/orders`, {
      offset: page,
      limit
    });
  }
  async createWebhook(siteId, triggerType, webhookUrl) {
    return await this.makeRequest(
      import_pieces_common.HttpMethod.POST,
      `'/sites/${siteId}/webhooks`,
      {},
      {
        triggerType,
        url: webhookUrl
      }
    );
  }
  async deleteWebhook(webhookId) {
    return await this.makeRequest(import_pieces_common.HttpMethod.DELETE, `/webhooks/${webhookId}`);
  }
};

// ../ap/packages/pieces/community/webflow/src/lib/common/props.ts
var webflowProps = {
  site_id: import_pieces_framework.Property.Dropdown({
    auth: webflowAuth,
    displayName: "Site",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect account first."
        };
      let authValue = auth, sites = await new WebflowApiClient(authValue.access_token).listSites(), options = [];
      for (let site of sites)
        options.push({ label: site.name, value: site._id });
      return {
        disabled: !1,
        options
      };
    }
  }),
  collection_id: import_pieces_framework.Property.Dropdown({
    auth: webflowAuth,
    displayName: "Collection",
    required: !0,
    refreshers: ["site_id"],
    options: async ({ auth, site_id }) => {
      if (!auth || !site_id)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect account first."
        };
      let authValue = auth, collections = await new WebflowApiClient(authValue.access_token).listCollections(site_id), options = [];
      for (let collection of collections)
        options.push({ label: collection.name, value: collection._id });
      return {
        disabled: !1,
        options
      };
    }
  }),
  collection_fields: import_pieces_framework.Property.DynamicProperties({
    auth: webflowAuth,
    displayName: "Collection Fields",
    required: !0,
    refreshers: ["collection_id"],
    props: async ({ auth, collection_id }) => {
      if (!auth) return {};
      if (!collection_id) return {};
      let collectionFields = {}, authValue = auth, client = new WebflowApiClient(authValue.access_token), { fields } = await client.getCollection(collection_id);
      for (let field of fields)
        if (field.editable && field.slug !== "_archived" && field.slug !== "_draft")
          switch (field.type) {
            case "Option":
              collectionFields[field.slug] = import_pieces_framework.Property.StaticDropdown({
                displayName: field.name,
                required: field.required,
                options: {
                  disabled: !1,
                  options: field.validations.options.map((option) => ({
                    label: option.name,
                    value: option.name
                  }))
                }
              });
              break;
            case "RichText":
            case "Email":
            case "PlainText":
            case "Phone":
            case "Link":
            case "Video":
            case "Color":
            case "ItemRef":
            case "FileRef":
              collectionFields[field.slug] = import_pieces_framework.Property.ShortText({
                displayName: field.name,
                required: field.required
              });
              break;
            case "ImageRef":
              collectionFields[field.slug] = import_pieces_framework.Property.ShortText({
                displayName: field.name,
                required: field.required,
                description: "Images must be hosted on a publicly accessible URL to be uploaded via the API.The maximum file size for images is 4MB."
              });
              break;
            case "Set":
              collectionFields[field.slug] = import_pieces_framework.Property.Array({
                displayName: field.name,
                required: field.required,
                description: " Images must be hosted on a publicly accessible URL to be uploaded via the API.The maximum file size for images is 4MB."
              });
              break;
            case "ItemRefSet":
              collectionFields[field.slug] = import_pieces_framework.Property.Array({
                displayName: field.name,
                required: field.required
              });
              break;
            case "Number":
              collectionFields[field.slug] = import_pieces_framework.Property.Number({
                displayName: field.name,
                required: field.required
              });
              break;
            case "Date":
              collectionFields[field.slug] = import_pieces_framework.Property.DateTime({
                displayName: field.name,
                required: field.required
              });
              break;
            case "Bool":
              collectionFields[field.slug] = import_pieces_framework.Property.Checkbox({
                displayName: field.name,
                required: !1
              });
              break;
          }
      return collectionFields;
    }
  }),
  collection_item_id: import_pieces_framework.Property.Dropdown({
    auth: webflowAuth,
    displayName: "Collection Item",
    required: !0,
    refreshers: ["collection_id"],
    options: async ({ auth, collection_id }) => {
      if (!auth || !collection_id)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect account first."
        };
      let authValue = auth, client = new WebflowApiClient(authValue.access_token), options = [], page = 0, response;
      do {
        response = await client.listCollectionItems(collection_id, page, 100), page += 100;
        for (let item of response.items)
          options.push({ label: item.name, value: item._id });
      } while (response.items.length > 0);
      return {
        disabled: !1,
        options
      };
    }
  }),
  order_id: import_pieces_framework.Property.Dropdown({
    auth: webflowAuth,
    displayName: "Order",
    required: !0,
    refreshers: ["site_id"],
    options: async ({ auth, site_id }) => {
      if (!auth || !site_id)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect account first."
        };
      let authValue = auth, client = new WebflowApiClient(authValue.access_token), options = [], page = 0, response;
      do {
        response = await client.listOrders(site_id, page, 100), page += 100;
        for (let order of response)
          options.push({ label: order.orderId, value: order.orderId });
      } while (response.length > 0);
      return {
        disabled: !1,
        options
      };
    }
  })
};

// ../ap/packages/pieces/community/webflow/src/lib/actions/create-collection-item.ts
var webflowCreateCollectionItemAction = (0, import_pieces_framework2.createAction)({
  auth: webflowAuth,
  name: "create_collection_item",
  classification: "WRITE",
  displayName: "Create Collection Item",
  description: "Creates new collection item.",
  audience: "both",
  aiMetadata: { description: "Creates a new item in a Webflow CMS collection. Provide the collection ID and the field values (keyed by field slug); optionally mark the item as archived or draft. Use to add CMS content like blog posts or products. Each call creates a distinct item, so it is not idempotent.", idempotent: !1 },
  props: {
    site_id: webflowProps.site_id,
    collection_id: webflowProps.collection_id,
    collection_fields: webflowProps.collection_fields,
    is_archived: import_pieces_framework2.Property.Checkbox({
      displayName: "Is Archived",
      description: "Whether the item is archived or not",
      required: !1
    }),
    is_draft: import_pieces_framework2.Property.Checkbox({
      displayName: "Is Draft",
      description: "Whether the item is a draft or not",
      required: !1
    })
  },
  async run(context) {
    let collectionId = context.propsValue.collection_id, isArchived = context.propsValue.is_archived, isDraft = context.propsValue.is_draft, collectionInputFields = context.propsValue.collection_fields, client = new WebflowApiClient(context.auth.access_token), { fields: CollectionFields } = await client.getCollection(collectionId), formattedCollectionFields = {};
    for (let field of CollectionFields) {
      let fieldValue = collectionInputFields[field.slug];
      if (fieldValue !== void 0 && fieldValue !== "")
        switch (field.type) {
          case "ImageRef":
          case "FileRef":
            formattedCollectionFields[field.slug] = { url: fieldValue };
            break;
          case "Set":
            formattedCollectionFields[field.slug] = fieldValue.map((url) => ({ url }));
            break;
          case "Number":
            formattedCollectionFields[field.slug] = Number(fieldValue);
            break;
          default:
            formattedCollectionFields[field.slug] = fieldValue;
        }
    }
    return await client.createCollectionItem(collectionId, {
      fields: __spreadProps(__spreadValues({}, formattedCollectionFields), { _archived: isArchived, _draft: isDraft })
    });
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/actions/delete-collection-item.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var webflowDeleteCollectionItem = (0, import_pieces_framework3.createAction)({
  auth: webflowAuth,
  name: "delete_collection_item",
  classification: "DESTRUCTIVE",
  description: "Delete collection item",
  displayName: "Delete an item in a collection",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a single item from a Webflow CMS collection, identified by collection ID and item ID. Use when an agent needs to remove existing CMS content. Repeating the call after the item is gone has no further effect, so it is effectively idempotent on the same item ID.", idempotent: !0 },
  props: {
    site_id: webflowProps.site_id,
    collection_id: webflowProps.collection_id,
    collection_item_id: webflowProps.collection_item_id
  },
  async run(context) {
    let collectionId = context.propsValue.collection_id, collectionItemId = context.propsValue.collection_item_id;
    return await new WebflowApiClient(context.auth.access_token).deleteCollectionItem(collectionId, collectionItemId);
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/actions/find-collection-item.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var webflowFindCollectionItem = (0, import_pieces_framework4.createAction)({
  auth: webflowAuth,
  name: "find_collection_item",
  classification: "SEARCH",
  description: "Find collection item in a collection by field",
  displayName: "Find a Collection Item by Field",
  audience: "both",
  aiMetadata: { description: "Searches a Webflow CMS collection for items whose given field name exactly matches the supplied value, optionally capped by a max-results limit. Use to look up an item by a known field value (e.g. slug or name) before reading or updating it. Read-only and idempotent. Note: matching is an exact equality filter applied client-side over the fetched items, not a fuzzy or paginated server search.", idempotent: !0 },
  props: {
    site_id: webflowProps.site_id,
    collection_id: webflowProps.collection_id,
    field_name: import_pieces_framework4.Property.ShortText({
      displayName: "Field Name",
      description: "The name of the field to search by",
      required: !0
    }),
    field_value: import_pieces_framework4.Property.ShortText({
      displayName: "Field Value",
      description: "The value of the field to search for",
      required: !0
    }),
    max_results: import_pieces_framework4.Property.Number({
      displayName: "Max Results",
      description: "The maximum number of results to return",
      required: !1
    })
  },
  async run(configValue) {
    let accessToken = configValue.auth.access_token, collectionId = configValue.propsValue.collection_id, fieldName = configValue.propsValue.field_name, fieldValue = configValue.propsValue.field_value, maxResults = configValue.propsValue.max_results, request = {
      method: import_pieces_common2.HttpMethod.GET,
      url: `https://api.webflow.com/collections/${collectionId}/items`,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      }
    };
    try {
      let res = await import_pieces_common2.httpClient.sendRequest(request);
      if (res.status !== 200)
        throw new Error("Failed to fetch collection items");
      return { success: !0, result: res.body.items.filter((item) => item.fields[fieldName] === fieldValue).slice(0, maxResults) };
    } catch (err) {
      return { success: !1, message: err };
    }
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/actions/find-order.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var webflowFindOrder = (0, import_pieces_framework5.createAction)({
  auth: webflowAuth,
  name: "find_order",
  classification: "READ",
  description: "Find order",
  displayName: "Find an order",
  audience: "both",
  aiMetadata: { description: "Retrieves a single Webflow Ecommerce order by its site ID and order ID. Use to look up order details before fulfilling, refunding, or unfulfilling it. Read-only and idempotent.", idempotent: !0 },
  props: {
    site_id: webflowProps.site_id,
    order_id: webflowProps.order_id
  },
  async run(context) {
    let orderId = context.propsValue.order_id, siteId = context.propsValue.site_id;
    return await new WebflowApiClient(context.auth.access_token).getOrder(siteId, orderId);
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/actions/fulfill-order.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var webflowFulfillOrder = (0, import_pieces_framework6.createAction)({
  auth: webflowAuth,
  name: "fulfill_order",
  classification: "WRITE",
  description: "Fulfill order",
  displayName: "Fulfill an order",
  audience: "both",
  aiMetadata: { description: "Marks an existing Webflow Ecommerce order as fulfilled, identified by site ID and order ID, optionally sending the customer a fulfillment email. Use after shipping or completing an order. Setting fulfilled state is idempotent, but enabling the email may resend a notification, so prefer leaving it off when retrying.", idempotent: !1 },
  props: {
    site_id: webflowProps.site_id,
    order_id: webflowProps.order_id,
    send_order_fulfilled_email: import_pieces_framework6.Property.Checkbox({
      displayName: "Send Order Fulfilled Email",
      description: "Send an email to the customer that their order has been fulfilled",
      required: !1
    })
  },
  async run(context) {
    let orderId = context.propsValue.order_id, siteId = context.propsValue.site_id, sendOrderFulfilledEmail = context.propsValue.send_order_fulfilled_email;
    return await new WebflowApiClient(context.auth.access_token).fulfillOrder(siteId, orderId, { sendOrderFulfilledEmail });
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/actions/get-collection-item.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var webflowGetCollectionItem = (0, import_pieces_framework7.createAction)({
  auth: webflowAuth,
  name: "get_collection_item",
  classification: "READ",
  description: "Get collection item in a collection by ID",
  displayName: "Get a Collection Item by ID",
  audience: "both",
  aiMetadata: { description: "Retrieves a single Webflow CMS collection item by its collection ID and item ID. Use when the agent already knows the item ID and needs its full field data; if the ID is unknown, find it first with Find a Collection Item by Field. Read-only and idempotent.", idempotent: !0 },
  props: {
    site_id: webflowProps.site_id,
    collection_id: webflowProps.collection_id,
    collection_item_id: webflowProps.collection_item_id
  },
  async run(context) {
    let collectionId = context.propsValue.collection_id, collectionItemId = context.propsValue.collection_item_id;
    return await new WebflowApiClient(context.auth.access_token).getCollectionItem(collectionId, collectionItemId);
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/actions/refund-order.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var webflowRefundOrder = (0, import_pieces_framework8.createAction)({
  auth: webflowAuth,
  name: "refund_order",
  classification: "DESTRUCTIVE",
  description: "Refund order",
  displayName: "Refund an order",
  audience: "both",
  aiMetadata: { description: "Issues a refund for an existing Webflow Ecommerce order, identified by site ID and order ID. Use to reverse a customer payment. This moves money and is not idempotent \u2014 repeating it risks duplicate refund attempts, so call it once per order.", idempotent: !1 },
  props: {
    site_id: webflowProps.site_id,
    order_id: webflowProps.order_id
    // reason: Property.StaticDropdown({
    // 	displayName: 'Reason',
    // 	description: 'The reason for the refund',
    // 	required: false,
    // 	options: {
    // 		disabled: false,
    // 		options: [
    // 			{
    // 				label: 'Duplicate',
    // 				value: 'duplicate',
    // 			},
    // 			{
    // 				label: 'Fraudulent',
    // 				value: 'fraudulent',
    // 			},
    // 			{
    // 				label: 'Requested',
    // 				value: 'requested',
    // 			},
    // 		],
    // 	},
    // }),
  },
  async run(context) {
    let orderId = context.propsValue.order_id, siteId = context.propsValue.site_id;
    return await new WebflowApiClient(context.auth.access_token).refundOrder(siteId, orderId);
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/actions/unfulfill-order.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var webflowUnfulfillOrder = (0, import_pieces_framework9.createAction)({
  auth: webflowAuth,
  name: "unfulfill_order",
  classification: "WRITE",
  description: "Unfulfill order",
  displayName: "Unfulfill an order",
  audience: "both",
  aiMetadata: { description: "Reverts an existing Webflow Ecommerce order back to an unfulfilled state, identified by site ID and order ID. Use to undo a fulfillment marking. Idempotent: re-running on an already-unfulfilled order leaves it unfulfilled.", idempotent: !0 },
  props: {
    site_id: webflowProps.site_id,
    order_id: webflowProps.order_id
  },
  async run(context) {
    let orderId = context.propsValue.order_id, siteId = context.propsValue.site_id;
    return await new WebflowApiClient(context.auth.access_token).unfulfillOrder(siteId, orderId);
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/actions/update-collection-item.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var webflowUpdateCollectionItem = (0, import_pieces_framework10.createAction)({
  auth: webflowAuth,
  name: "update_collection_item",
  classification: "WRITE",
  description: "Update collection item",
  displayName: "Update an item in a collection",
  audience: "both",
  aiMetadata: { description: "Updates an existing Webflow CMS collection item, identified by collection ID and item ID, overwriting the supplied field values (keyed by field slug) and the archived/draft flags. Use to edit existing CMS content. Idempotent: applying the same field values repeatedly yields the same item state.", idempotent: !0 },
  props: {
    site_id: webflowProps.site_id,
    collection_id: webflowProps.collection_id,
    collection_item_id: webflowProps.collection_item_id,
    collection_fields: webflowProps.collection_fields,
    is_archived: import_pieces_framework10.Property.Checkbox({
      displayName: "Is Archived",
      description: "Whether the item is archived or not",
      required: !1
    }),
    is_draft: import_pieces_framework10.Property.Checkbox({
      displayName: "Is Draft",
      description: "Whether the item is a draft or not",
      required: !1
    })
  },
  async run(context) {
    let collectionId = context.propsValue.collection_id, collectionItemId = context.propsValue.collection_item_id, isArchived = context.propsValue.is_archived, isDraft = context.propsValue.is_draft, collectionInputFields = context.propsValue.collection_fields, client = new WebflowApiClient(context.auth.access_token), { fields: CollectionFields } = await client.getCollection(collectionId), formattedCollectionFields = {};
    for (let field of CollectionFields) {
      let fieldValue = collectionInputFields[field.slug];
      if (fieldValue !== void 0 && fieldValue !== "")
        switch (field.type) {
          case "ImageRef":
          case "FileRef":
            formattedCollectionFields[field.slug] = { url: fieldValue };
            break;
          case "Set":
            fieldValue.length > 0 && (formattedCollectionFields[field.slug] = fieldValue.map((url) => ({
              url
            })));
            break;
          case "ItemRefSet":
            fieldValue.length > 0 && (formattedCollectionFields[field.slug] = fieldValue);
            break;
          case "Number":
            formattedCollectionFields[field.slug] = Number(fieldValue);
            break;
          default:
            formattedCollectionFields[field.slug] = fieldValue;
        }
    }
    return await client.updateCollectionItem(collectionId, collectionItemId, {
      fields: __spreadProps(__spreadValues({}, formattedCollectionFields), { _archived: isArchived, _draft: isDraft })
    });
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/actions/publish-collection-item.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var webflowPublishCollectionItem = (0, import_pieces_framework11.createAction)({
  auth: webflowAuth,
  name: "publish_collection_item",
  classification: "WRITE",
  description: "Publish collection item",
  displayName: "Publish a Collection Item",
  audience: "both",
  aiMetadata: { description: "Publishes a Webflow CMS collection item live, identified by collection ID and item ID, so its current content goes to the live site. Use after creating or updating a draft item to make it public. Idempotent: re-publishing the same item again leaves it published.", idempotent: !0 },
  props: {
    site_id: webflowProps.site_id,
    collection_id: webflowProps.collection_id,
    collection_item_id: webflowProps.collection_item_id
  },
  async run(context) {
    let collectionId = context.propsValue.collection_id, collectionItemId = context.propsValue.collection_item_id;
    return await new WebflowApiClient(context.auth.access_token).publishCollectionItem(collectionId, collectionItemId);
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/actions/list-sites.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var webflowListSites = (0, import_pieces_framework12.createAction)({
  auth: webflowAuth,
  name: "list_sites",
  classification: "SEARCH",
  description: "List all sites",
  displayName: "List Sites",
  audience: "both",
  aiMetadata: { description: "Lists all Webflow sites accessible to the authenticated account, returning their IDs and metadata. Use to discover a site ID needed by other actions (collections, orders, form-submission trigger). Read-only and idempotent; takes no input.", idempotent: !0 },
  props: {},
  async run(context) {
    return await new WebflowApiClient(context.auth.access_token).listSites();
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/actions/list-collections.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var webflowListCollections = (0, import_pieces_framework13.createAction)({
  auth: webflowAuth,
  name: "list_collections",
  classification: "SEARCH",
  description: "List all collections in a site",
  displayName: "List Collections",
  audience: "both",
  aiMetadata: { description: "Lists all CMS collections within a given Webflow site, identified by site ID, returning their IDs and metadata. Use to discover a collection ID before reading, creating, or updating collection items. Read-only and idempotent.", idempotent: !0 },
  props: {
    site_id: webflowProps.site_id
  },
  async run(context) {
    return await new WebflowApiClient(context.auth.access_token).listCollections(context.propsValue.site_id);
  }
});

// ../ap/packages/pieces/community/webflow/src/lib/common/common.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), webflowCommon = {
  baseUrl: "https://api.webflow.com/",
  subscribeWebhook: async (siteId, tag, webhookUrl, accessToken) => {
    let request = {
      method: import_pieces_common3.HttpMethod.POST,
      url: `https://api.webflow.com/sites/${siteId}/webhooks`,
      headers: {
        "Content-Type": "application/json"
      },
      body: {
        triggerType: tag,
        url: webhookUrl
      },
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      },
      queryParams: {}
    };
    return await import_pieces_common3.httpClient.sendRequest(request);
  },
  unsubscribeWebhook: async (siteId, webhookId, accessToken) => {
    let request = {
      method: import_pieces_common3.HttpMethod.DELETE,
      url: `https://api.webflow.com/sites/${siteId}/webhooks/${webhookId}`,
      headers: {
        "Content-Type": "application/json"
      },
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: accessToken
      }
    };
    return await import_pieces_common3.httpClient.sendRequest(request);
  }
};

// ../ap/packages/pieces/community/webflow/src/lib/triggers/new-form-submitted.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var triggerNameInStore = "webflow_created_form_submissions_trigger", webflowNewSubmission = (0, import_pieces_framework14.createTrigger)({
  auth: webflowAuth,
  name: "new_submission",
  classification: "READ",
  displayName: "New Submission",
  description: "Triggers when Webflow Site receives a new submission",
  aiMetadata: {
    description: "Fires when a form on the selected Webflow site receives a new submission, delivering the submitted form data. Optionally scoped to a single form by name; if no form name is given, it fires for submissions from any form on the site."
  },
  props: {
    site_id: webflowProps.site_id,
    formName: import_pieces_framework14.Property.ShortText({
      displayName: "Form Name",
      required: !1,
      description: "Copy from the form settings, or from one of the responses"
    })
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  // TODO remove and force testing as the data can be custom.
  sampleData: {
    name: "Sample Form",
    site: "62749158efef318abc8d5a0f",
    data: {
      field_one: "mock valued"
    },
    d: "2022-09-14T12:35:16.117Z",
    _id: "6321ca84df3949bfc6752327"
  },
  async onEnable(context) {
    var _a;
    let res = await webflowCommon.subscribeWebhook(
      context.propsValue.site_id,
      "form_submission",
      context.webhookUrl,
      (0, import_pieces_common4.getAccessTokenOrThrow)(context.auth)
    );
    await ((_a = context.store) == null ? void 0 : _a.put(triggerNameInStore, {
      webhookId: res.body._id
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(triggerNameInStore));
    response != null && await webflowCommon.unsubscribeWebhook(
      context.propsValue.site_id,
      response.webhookId,
      (0, import_pieces_common4.getAccessTokenOrThrow)(context.auth)
    );
  },
  async run(context) {
    let body = context.payload.body, { formName } = context.propsValue;
    return formName ? body.name == formName ? [body] : [] : [body];
  }
});

// ../ap/packages/pieces/community/webflow/src/index.ts
var webflowAuth = import_pieces_framework15.PieceAuth.OAuth2({
  description: "",
  authUrl: "https://webflow.com/oauth/authorize",
  tokenUrl: "https://api.webflow.com/oauth/access_token",
  required: !0,
  scope: ["webhooks:write", "forms:read"]
}), webflow = (0, import_pieces_framework15.createPiece)({
  displayName: "Webflow",
  description: "Design, build, and launch responsive websites visually",
  minimumSupportedRelease: "0.5.0",
  logoUrl: "https://cdn.activepieces.com/pieces/webflow.png",
  categories: [import_pieces_framework16.PieceCategory.MARKETING],
  authors: [
    "Angelebeats",
    "Ahmad-AbuOsbeh",
    "TaskMagicKyle",
    "kishanprmr",
    "MoShizzle",
    "khaledmashaly",
    "abuaboud"
  ],
  auth: webflowAuth,
  actions: [
    webflowCreateCollectionItemAction,
    webflowDeleteCollectionItem,
    webflowUpdateCollectionItem,
    webflowFindCollectionItem,
    webflowGetCollectionItem,
    webflowFulfillOrder,
    webflowUnfulfillOrder,
    webflowRefundOrder,
    webflowFindOrder,
    webflowPublishCollectionItem,
    webflowListSites,
    webflowListCollections,
    (0, import_pieces_common5.createCustomApiCallAction)({
      baseUrl: () => "https://api.webflow.com",
      auth: webflowAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: [webflowNewSubmission]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  webflow,
  webflowAuth
});
