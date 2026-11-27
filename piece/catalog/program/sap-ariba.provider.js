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

// ../ap/packages/pieces/community/sap-ariba/src/index.ts
var index_exports = {};
__export(index_exports, {
  sapAriba: () => sapAriba
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework16 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sap-ariba/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), sapAribaAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `
Authenticate to SAP Ariba APIs.

**Option 1: API Key Only**
- Fill in only Application Key and API Base URL.

**Option 2: OAuth + API Key**
- Fill in all fields including OAuth Client ID, Client Secret, and OAuth Server URL.

**Steps:**
1. Log in to the [SAP Ariba Developer Portal](https://developer.ariba.com/api/).
2. Click **Manage** and select your application.
3. **Application Key**: Copy from the **Application Key** field.
4. **OAuth Client ID** (optional): Copy from the **OAuth Client ID** field.
5. **OAuth Client Secret** (optional): Click **Actions** > **Generate OAuth Secret**. Copy immediately.
6. **OAuth Server URL** (optional): Find in **Environment details** (e.g., \`https://api.ariba.com\`).
7. **API Base URL**: The API runtime URL (e.g., \`https://openapi.ariba.com/api/purchase-orders-buyer/v1/prod\`).
  `,
  props: {
    apiKey: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Application Key (APIKey)",
      description: "The Application Key for your application.",
      required: !0
    }),
    baseUrl: import_pieces_framework.Property.ShortText({
      displayName: "API Base URL",
      description: "The API Runtime URL (e.g., https://openapi.ariba.com/api/purchase-orders-buyer/v1/prod).",
      required: !0
    }),
    clientId: import_pieces_framework.Property.ShortText({
      displayName: "OAuth Client ID",
      description: "The OAuth Client ID (optional, for OAuth authentication).",
      required: !1
    }),
    clientSecret: import_pieces_framework.PieceAuth.SecretText({
      displayName: "OAuth Client Secret",
      description: "The OAuth Client Secret (optional, for OAuth authentication).",
      required: !1
    }),
    oauthServerUrl: import_pieces_framework.Property.ShortText({
      displayName: "OAuth Server URL Prefix",
      description: "The OAuth Server URL (optional, e.g., https://api.ariba.com).",
      required: !1
    })
  },
  validate: async ({ auth }) => {
    try {
      if (auth.clientId && auth.clientSecret && auth.oauthServerUrl) {
        let credentials = Buffer.from(`${auth.clientId}:${auth.clientSecret}`).toString("base64"), params = new URLSearchParams();
        params.append("grant_type", "openapi_2lo");
        let request = {
          method: import_pieces_common.HttpMethod.POST,
          url: `${auth.oauthServerUrl}/v2/oauth/token`,
          headers: {
            Authorization: `Basic ${credentials}`,
            "Content-Type": "application/x-www-form-urlencoded"
          },
          body: params.toString()
        };
        await import_pieces_common.httpClient.sendRequest(request);
      }
      return { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: (e == null ? void 0 : e.message) || "Invalid credentials"
      };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/list-purchase-orders.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/sap-ariba/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var sapAribaCommon = {
  async getAccessToken(auth) {
    if (!auth.props.clientId || !auth.props.clientSecret || !auth.props.oauthServerUrl)
      return null;
    let credentials = Buffer.from(`${auth.props.clientId}:${auth.props.clientSecret}`).toString("base64"), params = new URLSearchParams();
    params.append("grant_type", "openapi_2lo");
    let request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: `${auth.props.oauthServerUrl}/v2/oauth/token`,
      headers: {
        Authorization: `Basic ${credentials}`,
        "Content-Type": "application/x-www-form-urlencoded"
      },
      body: params.toString()
    }, response = await import_pieces_common2.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error(`Failed to authenticate with SAP Ariba: ${JSON.stringify(response.body)}`);
    return response.body.access_token;
  },
  async makeRequest(auth, method, endpoint, queryParams, body, additionalHeaders) {
    let accessToken = await this.getAccessToken(auth), headers = __spreadValues({
      apiKey: auth.props.apiKey,
      Accept: "application/json",
      "Content-Type": "application/json"
    }, additionalHeaders);
    accessToken && (headers.Authorization = `Bearer ${accessToken}`);
    let request = {
      method,
      url: `${auth.props.baseUrl}${endpoint}`,
      headers,
      queryParams,
      body
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body;
  }
};

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/list-purchase-orders.ts
var listPurchaseOrders = (0, import_pieces_framework2.createAction)({
  auth: sapAribaAuth,
  name: "list_purchase_orders",
  displayName: "List Purchase Orders",
  description: "Retrieves purchase order header information for a specified buyer.",
  audience: "both",
  aiMetadata: { description: "List purchase order headers from SAP Ariba Network for a buyer (required buyer ANID). Use to find or browse POs; optional filters (supplier ANID, document number, order/routing status, date range capped at 31 days) narrow the results, and leaving them empty returns all of the buyer's orders paginated. Read-only and idempotent.", idempotent: !0 },
  props: {
    buyerANID: import_pieces_framework2.Property.ShortText({
      displayName: "Buyer ANID",
      description: "The buyer's Ariba Network ID.",
      required: !0
    }),
    supplierANID: import_pieces_framework2.Property.ShortText({
      displayName: "Supplier ANID",
      description: "Filter by supplier's Ariba Network ID.",
      required: !1
    }),
    documentNumber: import_pieces_framework2.Property.ShortText({
      displayName: "Document Number",
      description: "Filter by order ID / purchase order number.",
      required: !1
    }),
    orderStatus: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Order Status",
      description: "Filter by order processing state.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "New", value: "New" },
          { label: "Changed", value: "Changed" },
          { label: "Confirmed", value: "Confirmed" },
          { label: "Shipped", value: "Shipped" },
          { label: "Invoiced", value: "Invoiced" },
          { label: "Canceled", value: "Canceled" }
        ]
      }
    }),
    routingStatus: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Routing Status",
      description: "Filter by routing status.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Queued", value: "Queued" },
          { label: "Sent", value: "Sent" },
          { label: "Acknowledged", value: "Acknowledged" },
          { label: "Failed", value: "Failed" }
        ]
      }
    }),
    startDate: import_pieces_framework2.Property.DateTime({
      displayName: "Start Date",
      description: "Filter orders received on or after this date. Max range: 31 days.",
      required: !1
    }),
    endDate: import_pieces_framework2.Property.DateTime({
      displayName: "End Date",
      description: "Filter orders received on or before this date. Max range: 31 days.",
      required: !1
    }),
    top: import_pieces_framework2.Property.Number({
      displayName: "Page Size",
      description: "Number of items to return.",
      required: !1,
      defaultValue: 50
    }),
    skip: import_pieces_framework2.Property.Number({
      displayName: "Offset",
      description: "Number of items to skip.",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let { buyerANID, supplierANID, documentNumber, orderStatus, routingStatus, startDate, endDate, top, skip } = context.propsValue, filters = [];
    supplierANID && filters.push(`supplierANID eq '${supplierANID}'`), documentNumber && filters.push(`documentNumber eq '${documentNumber}'`), orderStatus && filters.push(`orderStatus eq '${orderStatus}'`), routingStatus && filters.push(`routingStatus eq '${routingStatus}'`), startDate && filters.push(`startDate eq '${startDate}'`), endDate && filters.push(`endDate eq '${endDate}'`);
    let queryParams = {};
    return filters.length > 0 && (queryParams.$filter = filters.join(" and ")), top && (queryParams.$top = top.toString()), skip && (queryParams.$skip = skip.toString()), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common3.HttpMethod.GET,
      "/orders",
      queryParams,
      void 0,
      { "X-ARIBA-NETWORK-ID": buyerANID }
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/list-purchase-order-items.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var listPurchaseOrderItems = (0, import_pieces_framework3.createAction)({
  auth: sapAribaAuth,
  name: "list_purchase_order_items",
  displayName: "List Purchase Order Line Items",
  description: "Retrieves line item information from purchase orders for a specified buyer.",
  audience: "both",
  aiMetadata: { description: "List purchase-order line items from SAP Ariba Network for a buyer (required buyer ANID). Use when you need item-level detail rather than PO headers; optional filters (document number, order/routing status, date range capped at 31 days) narrow results, and leaving them empty returns all line items paginated. Read-only and idempotent.", idempotent: !0 },
  props: {
    buyerANID: import_pieces_framework3.Property.ShortText({
      displayName: "Buyer ANID",
      description: "The buyer's Ariba Network ID.",
      required: !0
    }),
    documentNumber: import_pieces_framework3.Property.ShortText({
      displayName: "Document Number",
      description: "Filter by order ID / purchase order number.",
      required: !1
    }),
    orderStatus: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Order Status",
      description: "Filter by order processing state.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "New", value: "New" },
          { label: "Changed", value: "Changed" },
          { label: "Confirmed", value: "Confirmed" },
          { label: "Shipped", value: "Shipped" },
          { label: "Invoiced", value: "Invoiced" },
          { label: "Canceled", value: "Canceled" }
        ]
      }
    }),
    routingStatus: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Routing Status",
      description: "Filter by routing status.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Queued", value: "Queued" },
          { label: "Sent", value: "Sent" },
          { label: "Acknowledged", value: "Acknowledged" },
          { label: "Failed", value: "Failed" }
        ]
      }
    }),
    startDate: import_pieces_framework3.Property.DateTime({
      displayName: "Start Date",
      description: "Filter orders received on or after this date. Max range: 31 days.",
      required: !1
    }),
    endDate: import_pieces_framework3.Property.DateTime({
      displayName: "End Date",
      description: "Filter orders received on or before this date. Max range: 31 days.",
      required: !1
    }),
    top: import_pieces_framework3.Property.Number({
      displayName: "Page Size",
      description: "Number of items to return.",
      required: !1,
      defaultValue: 50
    }),
    skip: import_pieces_framework3.Property.Number({
      displayName: "Offset",
      description: "Number of items to skip.",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let { buyerANID, documentNumber, orderStatus, routingStatus, startDate, endDate, top, skip } = context.propsValue, filters = [];
    documentNumber && filters.push(`documentNumber eq '${documentNumber}'`), orderStatus && filters.push(`orderStatus eq '${orderStatus}'`), routingStatus && filters.push(`routingStatus eq '${routingStatus}'`), startDate && filters.push(`startDate eq '${startDate}'`), endDate && filters.push(`endDate eq '${endDate}'`);
    let queryParams = {};
    return filters.length > 0 && (queryParams.$filter = filters.join(" and ")), top && (queryParams.$top = top.toString()), skip && (queryParams.$skip = skip.toString()), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common4.HttpMethod.GET,
      "/items",
      queryParams,
      void 0,
      { "X-ARIBA-NETWORK-ID": buyerANID }
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/get-document-changes.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getDocumentChanges = (0, import_pieces_framework4.createAction)({
  auth: sapAribaAuth,
  name: "get_document_changes",
  displayName: "Get Document Changes",
  description: "Returns the list of changes to approvable documents.",
  audience: "both",
  aiMetadata: { description: "List changes to approvable documents in a SAP Ariba realm (required). Use to poll for incremental updates: pass a change sequence ID to return only changes after that point, or leave it empty to fetch all changes. Read-only and idempotent.", idempotent: !0 },
  props: {
    realm: import_pieces_framework4.Property.ShortText({
      displayName: "Realm",
      description: "Unique identifier of the realm.",
      required: !0
    }),
    changeSequenceId: import_pieces_framework4.Property.Number({
      displayName: "Change Sequence ID",
      description: "Only return changes after this sequence ID. Leave empty to get all changes.",
      required: !1
    }),
    count: import_pieces_framework4.Property.Checkbox({
      displayName: "Include Count",
      description: "Include total count in response.",
      required: !1,
      defaultValue: !1
    }),
    top: import_pieces_framework4.Property.Number({
      displayName: "Page Size",
      description: "Number of records to return (max 1000, default 10).",
      required: !1
    }),
    skip: import_pieces_framework4.Property.Number({
      displayName: "Offset",
      description: "Number of records to skip.",
      required: !1
    })
  },
  async run(context) {
    let { realm, changeSequenceId, count, top, skip } = context.propsValue, queryParams = {
      realm
    };
    return changeSequenceId != null && (queryParams.$filter = `changeSequenceId gt ${changeSequenceId}`), count && (queryParams.$count = "true"), top && (queryParams.$top = top.toString()), skip && (queryParams.$skip = skip.toString()), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common5.HttpMethod.GET,
      "/changes",
      queryParams
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/get-pending-approvables.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getPendingApprovables = (0, import_pieces_framework5.createAction)({
  auth: sapAribaAuth,
  name: "get_pending_approvables",
  displayName: "Get Pending Approvables",
  description: "List all documents/tasks that are pending approval.",
  audience: "both",
  aiMetadata: { description: "List documents and tasks awaiting approval in a SAP Ariba realm (required). Use to find pending approvals; optional filters narrow by approvable type (requisitions, invoices, contracts, payment), user, or password adapter, and leaving them empty returns all pending items. Read-only and idempotent.", idempotent: !0 },
  props: {
    realm: import_pieces_framework5.Property.ShortText({
      displayName: "Realm",
      description: "Unique identifier of the realm.",
      required: !0
    }),
    approvableType: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Approvable Type",
      description: "Filter by document type.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Requisitions", value: "requisitions" },
          { label: "Invoices", value: "invoices" },
          { label: "Contracts", value: "contracts" },
          { label: "Payment", value: "payment" }
        ]
      }
    }),
    user: import_pieces_framework5.Property.ShortText({
      displayName: "User",
      description: "Filter by username.",
      required: !1
    }),
    passwordAdapter: import_pieces_framework5.Property.ShortText({
      displayName: "Password Adapter",
      description: "Filter by password adapter.",
      required: !1
    }),
    count: import_pieces_framework5.Property.Checkbox({
      displayName: "Include Count",
      description: "Include total count in response.",
      required: !1,
      defaultValue: !1
    }),
    top: import_pieces_framework5.Property.Number({
      displayName: "Page Size",
      description: "Number of records to return (max 100, default 100).",
      required: !1
    }),
    skip: import_pieces_framework5.Property.Number({
      displayName: "Offset",
      description: "Number of records to skip.",
      required: !1
    })
  },
  async run(context) {
    let { realm, approvableType, user, passwordAdapter, count, top, skip } = context.propsValue, queryParams = {
      realm
    }, filters = [];
    return approvableType && filters.push(`approvableType eq '${approvableType}'`), user && filters.push(`user eq '${user}'`), passwordAdapter && filters.push(`passwordAdapter eq '${passwordAdapter}'`), filters.length > 0 && (queryParams.$filter = filters.join(" and ")), count && (queryParams.$count = "true"), top && (queryParams.$top = top.toString()), skip && (queryParams.$skip = skip.toString()), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common6.HttpMethod.GET,
      "/pendingApprovables",
      queryParams
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/get-catalog-items.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var getCatalogItems = (0, import_pieces_framework6.createAction)({
  auth: sapAribaAuth,
  name: "get_catalog_items",
  displayName: "Get Catalog Items",
  description: "Get list of filtered catalog items for a specific supplier.",
  audience: "both",
  aiMetadata: { description: "Retrieve catalog items for a specific supplier and catalog in a SAP Ariba realm. Use to look up product/part data within a known catalog; requires realm, catalog name, supplier ID, and the comma-separated output fields to return, with an optional RSQL filter expression to narrow matches. Read-only and idempotent.", idempotent: !0 },
  props: {
    realm: import_pieces_framework6.Property.ShortText({
      displayName: "Realm",
      description: "Realm name.",
      required: !0
    }),
    catalogName: import_pieces_framework6.Property.ShortText({
      displayName: "Catalog Name",
      description: "Name of the catalog.",
      required: !0
    }),
    supplierId: import_pieces_framework6.Property.ShortText({
      displayName: "Supplier ID",
      description: "Supplier ID domain value pair (e.g., buyersystemid:Supplier16).",
      required: !0
    }),
    select: import_pieces_framework6.Property.ShortText({
      displayName: "Select Fields",
      description: "Output fields to return, comma-separated (e.g., SupplierPartId,SupplierId,LeadTime).",
      required: !0,
      defaultValue: "SupplierPartId,SupplierId"
    }),
    rsqlfilter: import_pieces_framework6.Property.ShortText({
      displayName: "RSQL Filter",
      description: "Optional RSQL filter expression (e.g., CatalogName==JCNTechnologies).",
      required: !1
    }),
    offset: import_pieces_framework6.Property.Number({
      displayName: "Offset",
      description: "Starting position for results.",
      required: !0,
      defaultValue: 1
    }),
    limit: import_pieces_framework6.Property.Number({
      displayName: "Limit",
      description: "Maximum number of items to return (default 1000).",
      required: !1,
      defaultValue: 1e3
    })
  },
  async run(context) {
    let { realm, catalogName, supplierId, select, rsqlfilter, offset, limit } = context.propsValue, queryParams = {
      select,
      offset: offset.toString()
    };
    return rsqlfilter && (queryParams.rsqlfilter = rsqlfilter), limit && (queryParams.limit = limit.toString()), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common7.HttpMethod.GET,
      `/catalogname/${encodeURIComponent(catalogName)}/supplierid/${encodeURIComponent(supplierId)}/items`,
      queryParams,
      void 0,
      { realm }
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/get-active-catalogs.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var getActiveCatalogs = (0, import_pieces_framework7.createAction)({
  auth: sapAribaAuth,
  name: "get_active_catalogs",
  displayName: "Get Active Catalogs",
  description: "Get list of active catalog data & supplier values from the specified date.",
  audience: "both",
  aiMetadata: { description: "List active catalogs and their supplier values in a SAP Ariba realm, returning those activated on or after a required date. Use to discover available catalogs; optional filters narrow by supplier domain or subscription name. Read-only and idempotent.", idempotent: !0 },
  props: {
    realm: import_pieces_framework7.Property.ShortText({
      displayName: "Realm",
      description: "Realm name.",
      required: !0
    }),
    date: import_pieces_framework7.Property.DateTime({
      displayName: "Activated From Date",
      description: "Get catalogs activated from this date (ISO format).",
      required: !0
    }),
    supplierdomain: import_pieces_framework7.Property.ShortText({
      displayName: "Supplier Domain",
      description: "Filter by Supplier Domain ID (e.g., buyersystemid:Supplier16).",
      required: !1
    }),
    subscriptionName: import_pieces_framework7.Property.ShortText({
      displayName: "Subscription Name",
      description: "Filter by Subscription Name.",
      required: !1
    }),
    showAll: import_pieces_framework7.Property.Checkbox({
      displayName: "Show All",
      description: "Show all records.",
      required: !1,
      defaultValue: !1
    }),
    showAdditionalFields: import_pieces_framework7.Property.Checkbox({
      displayName: "Show Additional Fields",
      description: "Include additional fields in response.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let { realm, date, supplierdomain, subscriptionName, showAll, showAdditionalFields } = context.propsValue, queryParams = {
      date
    };
    return supplierdomain && (queryParams.supplierdomain = supplierdomain), subscriptionName && (queryParams.subscriptionName = subscriptionName), showAll !== void 0 && (queryParams.showAll = showAll.toString()), showAdditionalFields !== void 0 && (queryParams.showAdditionalFields = showAdditionalFields.toString()), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common8.HttpMethod.GET,
      "/catalogs",
      queryParams,
      void 0,
      { realm }
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/get-facet-data.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var getFacetData = (0, import_pieces_framework8.createAction)({
  auth: sapAribaAuth,
  name: "get_facet_data",
  displayName: "Get Facet Data",
  description: "Get list of Facet data against realm.",
  audience: "both",
  aiMetadata: { description: "Retrieve catalog facet data (e.g. distinct classification codes) for a SAP Ariba realm. Use to enumerate facet values for filtering or grouping catalog content; requires realm, the facet field to select, and an RSQL filter expression. Read-only and idempotent.", idempotent: !0 },
  props: {
    realm: import_pieces_framework8.Property.ShortText({
      displayName: "Realm",
      description: "Realm name.",
      required: !0
    }),
    select: import_pieces_framework8.Property.ShortText({
      displayName: "Select Field",
      description: "Facet field to select (e.g., ClassificationCode).",
      required: !0,
      defaultValue: "ClassificationCode"
    }),
    rsqlfilter: import_pieces_framework8.Property.ShortText({
      displayName: "RSQL Filter",
      description: "RSQL Filter expression (e.g., CatalogName==JCNTechnologies).",
      required: !0
    })
  },
  async run(context) {
    let { realm, select, rsqlfilter } = context.propsValue, queryParams = {
      select,
      rsqlfilter
    };
    return await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common9.HttpMethod.GET,
      "/facets",
      queryParams,
      void 0,
      { realm }
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/list-invoices.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var listInvoices = (0, import_pieces_framework9.createAction)({
  auth: sapAribaAuth,
  name: "list_invoices",
  displayName: "List Invoices",
  description: "Retrieves header information of one or more invoices in Ariba Network.",
  audience: "both",
  aiMetadata: { description: "List invoice headers from SAP Ariba Network for a given Ariba Network ID (required). Use to find or browse invoices; optional filters narrow by buyer/supplier ANID, document number, document/routing status, document purpose, and creation or status-update date ranges, and leaving them empty returns all invoices paginated. Read-only and idempotent.", idempotent: !0 },
  props: {
    anId: import_pieces_framework9.Property.ShortText({
      displayName: "Ariba Network ID",
      description: "Buyer/Supplier Ariba Network ID (ANID).",
      required: !0
    }),
    buyerANID: import_pieces_framework9.Property.ShortText({
      displayName: "Filter: Buyer ANID",
      description: "Filter by Buyer's ANID.",
      required: !1
    }),
    supplierANID: import_pieces_framework9.Property.ShortText({
      displayName: "Filter: Supplier ANID",
      description: "Filter by Supplier's ANID.",
      required: !1
    }),
    documentNumber: import_pieces_framework9.Property.ShortText({
      displayName: "Filter: Document Number",
      description: "Filter by Invoice Document Number.",
      required: !1
    }),
    documentStatus: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Filter: Document Status",
      description: "Filter by processing status.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Sent", value: "Sent" },
          { label: "Acknowledged", value: "Acknowledged" },
          { label: "Failed", value: "Failed" },
          { label: "Rejected", value: "Rejected" },
          { label: "Paid", value: "Paid" },
          { label: "Approved", value: "Approved" }
        ]
      }
    }),
    routingStatus: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Filter: Routing Status",
      description: "Filter by routing status.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Sent", value: "Sent" },
          { label: "Acknowledged", value: "Acknowledged" },
          { label: "Failed", value: "Failed" }
        ]
      }
    }),
    startDate: import_pieces_framework9.Property.DateTime({
      displayName: "Filter: Created Start Date",
      description: "Filter by creation date (start).",
      required: !1
    }),
    endDate: import_pieces_framework9.Property.DateTime({
      displayName: "Filter: Created End Date",
      description: "Filter by creation date (end).",
      required: !1
    }),
    documentStatusUpdatedStartDate: import_pieces_framework9.Property.DateTime({
      displayName: "Filter: Updated Start Date",
      description: "Filter by status updated date (start).",
      required: !1
    }),
    documentStatusUpdatedEndDate: import_pieces_framework9.Property.DateTime({
      displayName: "Filter: Updated End Date",
      description: "Filter by status updated date (end).",
      required: !1
    }),
    documentPurpose: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Filter: Document Purpose",
      description: "Filter by document purpose.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Standard", value: "standard" },
          { label: "Credit Memo", value: "creditMemo" },
          { label: "Debit Memo", value: "debitMemo" }
        ]
      }
    }),
    includeInvoiceProfile: import_pieces_framework9.Property.Checkbox({
      displayName: "Include Invoice Profile",
      description: "Display Invoice Profile related fields (e.g., Address, Tax Id).",
      required: !1,
      defaultValue: !1
    }),
    top: import_pieces_framework9.Property.Number({
      displayName: "Page Size",
      description: "Number of items to return (max 100).",
      required: !1,
      defaultValue: 100
    }),
    skip: import_pieces_framework9.Property.Number({
      displayName: "Offset",
      description: "Number of items to skip.",
      required: !1
    })
  },
  async run(context) {
    let {
      anId,
      buyerANID,
      supplierANID,
      documentNumber,
      documentStatus,
      routingStatus,
      startDate,
      endDate,
      documentStatusUpdatedStartDate,
      documentStatusUpdatedEndDate,
      documentPurpose,
      includeInvoiceProfile,
      top,
      skip
    } = context.propsValue, filters = [];
    buyerANID && filters.push(`buyerANID eq '${buyerANID}'`), supplierANID && filters.push(`supplierANID eq '${supplierANID}'`), documentNumber && filters.push(`documentNumber eq '${documentNumber}'`), documentStatus && filters.push(`documentStatus eq '${documentStatus}'`), routingStatus && filters.push(`routingStatus eq '${routingStatus}'`), startDate && filters.push(`startDate eq '${startDate}'`), endDate && filters.push(`endDate eq '${endDate}'`), documentStatusUpdatedStartDate && filters.push(`documentStatusUpdatedStartDate eq '${documentStatusUpdatedStartDate}'`), documentStatusUpdatedEndDate && filters.push(`documentStatusUpdatedEndDate eq '${documentStatusUpdatedEndDate}'`), documentPurpose && filters.push(`documentPurpose eq '${documentPurpose}'`);
    let queryParams = {};
    return filters.length > 0 && (queryParams.$filter = filters.join(" and ")), includeInvoiceProfile && (queryParams.includeInvoiceProfile = "true"), top && (queryParams.$top = top.toString()), skip && (queryParams.$skip = skip.toString()), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common10.HttpMethod.GET,
      "/invoices",
      queryParams,
      void 0,
      { "X-ARIBA-NETWORK-ID": anId }
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/search-contract-workspaces.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var searchContractWorkspaces = (0, import_pieces_framework10.createAction)({
  auth: sapAribaAuth,
  name: "search_contract_workspaces",
  displayName: "Search Contract Workspaces",
  description: "Search contract workspaces based on attributes such as title, contract ID, region, etc.",
  audience: "both",
  aiMetadata: { description: "Search contract workspaces in a SAP Ariba Contracts realm, acting on behalf of a user (requires realm, user, and password adapter). Use to find contracts by attributes when you do not have an exact ID; an optional filter expression narrows matches and leaving it empty returns all accessible workspaces paginated. Read-only and idempotent.", idempotent: !0 },
  props: {
    realm: import_pieces_framework10.Property.ShortText({
      displayName: "Realm",
      description: "Unique identifier of the realm.",
      required: !0
    }),
    user: import_pieces_framework10.Property.ShortText({
      displayName: "User",
      description: "User ID to perform the action on behalf of.",
      required: !0
    }),
    passwordAdapter: import_pieces_framework10.Property.ShortText({
      displayName: "Password Adapter",
      description: "Password adapter to authenticate the user.",
      required: !0
    }),
    select: import_pieces_framework10.Property.ShortText({
      displayName: "Select Fields",
      description: "Comma-separated metadata fields to retrieve (e.g., title,contractid,region).",
      required: !1
    }),
    filter: import_pieces_framework10.Property.ShortText({
      displayName: "Filter",
      description: "Filter criteria (e.g., LastModified ge '2023-01-01T00:00:00Z').",
      required: !1
    }),
    top: import_pieces_framework10.Property.Number({
      displayName: "Page Size",
      description: "Number of records to return (default 500, max 500).",
      required: !1,
      defaultValue: 500
    }),
    skip: import_pieces_framework10.Property.Number({
      displayName: "Offset",
      description: "Number of records to skip.",
      required: !1,
      defaultValue: 0
    }),
    count: import_pieces_framework10.Property.Checkbox({
      displayName: "Include Count",
      description: "Include total number of records in response.",
      required: !1,
      defaultValue: !1
    }),
    expand: import_pieces_framework10.Property.ShortText({
      displayName: "Expand",
      description: "Additional information to fetch (e.g., relatedFactories).",
      required: !1
    })
  },
  async run(context) {
    let { realm, user, passwordAdapter, select, filter, top, skip, count, expand } = context.propsValue, queryParams = {
      realm,
      user,
      passwordAdapter
    };
    return select && (queryParams.$select = select), filter && (queryParams.$filter = filter), top && (queryParams.$top = top.toString()), skip && (queryParams.$skip = skip.toString()), count && (queryParams.$count = "true"), expand && (queryParams.$expand = expand), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common11.HttpMethod.GET,
      "/contractWorkspaces",
      queryParams
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/get-contract-workspace.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var getContractWorkspace = (0, import_pieces_framework11.createAction)({
  auth: sapAribaAuth,
  name: "get_contract_workspace",
  displayName: "Get Contract Workspace",
  description: "Search for a contract workspace using a unique identifier.",
  audience: "both",
  aiMetadata: { description: "Fetch a single contract workspace by its contract ID from a SAP Ariba Contracts realm, acting on behalf of a user (requires realm, user, and password adapter). Use when you already know the exact contract ID; for attribute-based discovery use Search Contract Workspaces instead. Read-only and idempotent.", idempotent: !0 },
  props: {
    realm: import_pieces_framework11.Property.ShortText({
      displayName: "Realm",
      description: "Unique identifier of the realm.",
      required: !0
    }),
    user: import_pieces_framework11.Property.ShortText({
      displayName: "User",
      description: "User ID to perform the action on behalf of.",
      required: !0
    }),
    passwordAdapter: import_pieces_framework11.Property.ShortText({
      displayName: "Password Adapter",
      description: "Password adapter to authenticate the user.",
      required: !0
    }),
    contractId: import_pieces_framework11.Property.ShortText({
      displayName: "Contract ID",
      description: "Contract ID (e.g., CW0001).",
      required: !0
    }),
    expand: import_pieces_framework11.Property.ShortText({
      displayName: "Expand",
      description: "Additional information to fetch (e.g., relatedFactories).",
      required: !1
    })
  },
  async run(context) {
    let { realm, user, passwordAdapter, contractId, expand } = context.propsValue, queryParams = {
      realm,
      user,
      passwordAdapter
    };
    return expand && (queryParams.$expand = expand), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common12.HttpMethod.GET,
      `/contractWorkspaces/${encodeURIComponent(contractId)}`,
      queryParams
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/create-contract-workspace.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var createContractWorkspace = (0, import_pieces_framework12.createAction)({
  auth: sapAribaAuth,
  name: "create_contract_workspace",
  displayName: "Create Contract Workspace",
  description: "Create a new contract workspace in SAP Ariba Contracts.",
  audience: "both",
  aiMetadata: { description: "Create a new contract workspace in a SAP Ariba Contracts realm, acting on behalf of a user (requires realm, user, password adapter, and a title). Use to start a new contract; optional fields set template, dates, amount/currency, owner, and supplier. Not idempotent: each call creates a separate workspace.", idempotent: !1 },
  props: {
    realm: import_pieces_framework12.Property.ShortText({
      displayName: "Realm",
      description: "Unique identifier of the realm.",
      required: !0
    }),
    user: import_pieces_framework12.Property.ShortText({
      displayName: "User",
      description: "User ID to perform the action on behalf of.",
      required: !0
    }),
    passwordAdapter: import_pieces_framework12.Property.ShortText({
      displayName: "Password Adapter",
      description: "Password adapter to authenticate the user.",
      required: !0
    }),
    title: import_pieces_framework12.Property.ShortText({
      displayName: "Title",
      description: "Contract workspace title.",
      required: !0
    }),
    description: import_pieces_framework12.Property.LongText({
      displayName: "Description",
      description: "Contract workspace description.",
      required: !1
    }),
    templateId: import_pieces_framework12.Property.ShortText({
      displayName: "Template ID",
      description: "Template ID for the contract workspace.",
      required: !1
    }),
    effectiveDate: import_pieces_framework12.Property.DateTime({
      displayName: "Effective Date",
      description: "Contract effective date.",
      required: !1
    }),
    expirationDate: import_pieces_framework12.Property.DateTime({
      displayName: "Expiration Date",
      description: "Contract expiration date.",
      required: !1
    }),
    contractAmount: import_pieces_framework12.Property.Number({
      displayName: "Contract Amount",
      description: "Contract amount value.",
      required: !1
    }),
    contractCurrency: import_pieces_framework12.Property.ShortText({
      displayName: "Contract Currency",
      description: "Currency code (e.g., USD).",
      required: !1
    }),
    ownerUniqueName: import_pieces_framework12.Property.ShortText({
      displayName: "Owner Unique Name",
      description: "Owner user unique name.",
      required: !1
    }),
    ownerPasswordAdapter: import_pieces_framework12.Property.ShortText({
      displayName: "Owner Password Adapter",
      description: "Owner password adapter.",
      required: !1
    }),
    supplierName: import_pieces_framework12.Property.ShortText({
      displayName: "Supplier Name",
      description: "Supplier organization name.",
      required: !1
    }),
    supplierSystemId: import_pieces_framework12.Property.ShortText({
      displayName: "Supplier System ID",
      description: "Supplier system ID.",
      required: !1
    }),
    isTestProject: import_pieces_framework12.Property.Checkbox({
      displayName: "Is Test Project",
      description: "Mark as test project.",
      required: !1,
      defaultValue: !1
    }),
    additionalFields: import_pieces_framework12.Property.Json({
      displayName: "Additional Fields",
      description: "Additional fields as JSON object to merge with the request body.",
      required: !1
    })
  },
  async run(context) {
    let {
      realm,
      user,
      passwordAdapter,
      title,
      description,
      templateId,
      effectiveDate,
      expirationDate,
      contractAmount,
      contractCurrency,
      ownerUniqueName,
      ownerPasswordAdapter,
      supplierName,
      supplierSystemId,
      isTestProject,
      additionalFields
    } = context.propsValue, queryParams = {
      realm,
      user,
      passwordAdapter
    }, body = {
      title
    };
    return description && (body.description = description), templateId && (body.templateId = templateId), effectiveDate && (body.effectiveDate = effectiveDate), expirationDate && (body.expirationDate = expirationDate), isTestProject !== void 0 && (body.isTestProject = isTestProject), contractAmount && contractCurrency && (body.contractAmount = {
      amount: contractAmount,
      currency: contractCurrency
    }), ownerUniqueName && (body.owner = {
      uniqueName: ownerUniqueName,
      passwordAdapter: ownerPasswordAdapter || passwordAdapter
    }), (supplierName || supplierSystemId) && (body.supplier = __spreadValues(__spreadValues({}, supplierName && { name: supplierName }), supplierSystemId && { systemID: supplierSystemId })), additionalFields && typeof additionalFields == "object" && Object.assign(body, additionalFields), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common13.HttpMethod.POST,
      "/contractWorkspaces",
      queryParams,
      body
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/update-contract-workspace.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var updateContractWorkspace = (0, import_pieces_framework13.createAction)({
  auth: sapAribaAuth,
  name: "update_contract_workspace",
  displayName: "Update Contract Workspace",
  description: "Update an existing contract workspace in SAP Ariba Contracts.",
  audience: "both",
  aiMetadata: { description: "Update fields (title, description, dates, amount/currency, amendment reason) on an existing contract workspace in a SAP Ariba Contracts realm, identified by contract ID and acting on behalf of a user (requires realm, user, and password adapter). Idempotent: repeating the call with the same field values converges to the same state.", idempotent: !0 },
  props: {
    realm: import_pieces_framework13.Property.ShortText({
      displayName: "Realm",
      description: "Unique identifier of the realm.",
      required: !0
    }),
    user: import_pieces_framework13.Property.ShortText({
      displayName: "User",
      description: "User ID to perform the action on behalf of.",
      required: !0
    }),
    passwordAdapter: import_pieces_framework13.Property.ShortText({
      displayName: "Password Adapter",
      description: "Password adapter to authenticate the user.",
      required: !0
    }),
    contractId: import_pieces_framework13.Property.ShortText({
      displayName: "Contract ID",
      description: "The ID of the contract workspace to update.",
      required: !0
    }),
    silentUpdate: import_pieces_framework13.Property.StaticDropdown({
      displayName: "Silent Update",
      description: "Update without changing the last modified date.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Yes", value: "Yes" },
          { label: "No", value: "No" }
        ]
      }
    }),
    title: import_pieces_framework13.Property.ShortText({
      displayName: "Title",
      description: "New title.",
      required: !1
    }),
    description: import_pieces_framework13.Property.LongText({
      displayName: "Description",
      description: "New description.",
      required: !1
    }),
    effectiveDate: import_pieces_framework13.Property.DateTime({
      displayName: "Effective Date",
      description: "New effective date.",
      required: !1
    }),
    expirationDate: import_pieces_framework13.Property.DateTime({
      displayName: "Expiration Date",
      description: "New expiration date.",
      required: !1
    }),
    contractAmount: import_pieces_framework13.Property.Number({
      displayName: "Contract Amount",
      description: "New contract amount value.",
      required: !1
    }),
    contractCurrency: import_pieces_framework13.Property.ShortText({
      displayName: "Contract Currency",
      description: "Currency code (e.g., USD).",
      required: !1
    }),
    amendmentReason: import_pieces_framework13.Property.LongText({
      displayName: "Amendment Reason",
      description: "Reason for amendment.",
      required: !1
    }),
    additionalFields: import_pieces_framework13.Property.Json({
      displayName: "Additional Fields",
      description: "Additional fields as JSON object to merge with the request body.",
      required: !1
    })
  },
  async run(context) {
    let {
      realm,
      user,
      passwordAdapter,
      contractId,
      silentUpdate,
      title,
      description,
      effectiveDate,
      expirationDate,
      contractAmount,
      contractCurrency,
      amendmentReason,
      additionalFields
    } = context.propsValue, queryParams = {
      realm,
      user,
      passwordAdapter
    };
    silentUpdate && (queryParams.silentUpdate = silentUpdate);
    let body = {
      contractId
    };
    return title && (body.title = title), description && (body.description = description), effectiveDate && (body.effectiveDate = effectiveDate), expirationDate && (body.expirationDate = expirationDate), amendmentReason && (body.amendmentReason = amendmentReason), contractAmount && contractCurrency && (body.contractAmount = {
      amount: contractAmount,
      currency: contractCurrency
    }), additionalFields && typeof additionalFields == "object" && Object.assign(body, additionalFields), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common14.HttpMethod.PATCH,
      "/contractWorkspaces",
      queryParams,
      body
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/update-contract-status.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var updateContractStatus = (0, import_pieces_framework14.createAction)({
  auth: sapAribaAuth,
  name: "update_contract_status",
  displayName: "Update Contract Status",
  description: "Change the status of a contract workspace (e.g., amend, putOnHold, resume, close).",
  audience: "both",
  aiMetadata: { description: "Perform a status-change action on a contract workspace in a SAP Ariba Contracts realm, identified by contract ID and acting on behalf of a user (requires realm, user, and password adapter). The action prop selects the transition: amend, completeAmendment, putOnHold, resume, or close (amend also takes an amendment type and reason). Not idempotent: each call drives a state transition that depends on the contract's current status.", idempotent: !1 },
  props: {
    realm: import_pieces_framework14.Property.ShortText({
      displayName: "Realm",
      description: "Unique identifier of the realm.",
      required: !0
    }),
    user: import_pieces_framework14.Property.ShortText({
      displayName: "User",
      description: "User ID to perform the action on behalf of.",
      required: !0
    }),
    passwordAdapter: import_pieces_framework14.Property.ShortText({
      displayName: "Password Adapter",
      description: "Password adapter to authenticate the user.",
      required: !0
    }),
    contractId: import_pieces_framework14.Property.ShortText({
      displayName: "Contract ID",
      description: "The ID of the contract workspace.",
      required: !0
    }),
    action: import_pieces_framework14.Property.StaticDropdown({
      displayName: "Action",
      description: "The status change action to perform.",
      required: !0,
      options: {
        disabled: !1,
        options: [
          { label: "Amend", value: "amend" },
          { label: "Complete Amendment", value: "completeAmendment" },
          { label: "Put on Hold", value: "putOnHold" },
          { label: "Resume", value: "resume" },
          { label: "Close", value: "close" }
        ]
      }
    }),
    amendmentType: import_pieces_framework14.Property.StaticDropdown({
      displayName: "Amendment Type",
      description: "Required if action is Amend.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Administrative", value: "Administrative" },
          { label: "Renewal", value: "Renewal" },
          { label: "Amendment", value: "Amendment" },
          { label: "Termination", value: "Termination" },
          { label: "Price Update", value: "PriceUpdate" }
        ]
      }
    }),
    amendmentReason: import_pieces_framework14.Property.LongText({
      displayName: "Amendment Reason",
      description: "Reason for the amendment.",
      required: !1
    }),
    isTemplateUpgradeRequired: import_pieces_framework14.Property.Checkbox({
      displayName: "Is Template Upgrade Required",
      description: "Whether template upgrade is required (for Amend action).",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    let {
      realm,
      user,
      passwordAdapter,
      contractId,
      action,
      amendmentType,
      amendmentReason,
      isTemplateUpgradeRequired
    } = context.propsValue, queryParams = {
      realm,
      user,
      passwordAdapter
    }, body = {
      action
    };
    return action === "amend" && (body.amendAttributes = __spreadProps(__spreadValues(__spreadValues({}, amendmentType && { amendmentType }), amendmentReason && { amendmentReason }), {
      isTemplateUpgradeRequired
    })), await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common15.HttpMethod.POST,
      `/contractWorkspaces/${encodeURIComponent(contractId)}/state`,
      queryParams,
      body
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/lib/actions/delete-contract-workspace.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var deleteContractWorkspace = (0, import_pieces_framework15.createAction)({
  auth: sapAribaAuth,
  name: "delete_contract_workspace",
  displayName: "Delete Contract Workspace",
  description: "Delete a contract workspace in SAP Ariba Contracts.",
  audience: "both",
  aiMetadata: { description: "Delete a contract workspace by its contract ID from a SAP Ariba Contracts realm, acting on behalf of a user (requires realm, user, and password adapter). Use to permanently remove a workspace. Idempotent: once deleted, repeating the call leaves the workspace gone.", idempotent: !0 },
  props: {
    realm: import_pieces_framework15.Property.ShortText({
      displayName: "Realm",
      description: "Name of the SAP Ariba Contract site (e.g., testrealm).",
      required: !0
    }),
    user: import_pieces_framework15.Property.ShortText({
      displayName: "User",
      description: "User ID to perform the action on behalf of (e.g., testUser).",
      required: !0
    }),
    passwordAdapter: import_pieces_framework15.Property.ShortText({
      displayName: "Password Adapter",
      description: "Password adapter to authenticate the user (e.g., PasswordAdapter1).",
      required: !0
    }),
    contractId: import_pieces_framework15.Property.ShortText({
      displayName: "Contract ID",
      description: "The ID of the contract workspace to delete (e.g., CW0001).",
      required: !0
    })
  },
  async run(context) {
    let { realm, user, passwordAdapter, contractId } = context.propsValue, queryParams = {
      realm,
      user,
      passwordAdapter
    };
    return await sapAribaCommon.makeRequest(
      context.auth,
      import_pieces_common16.HttpMethod.DELETE,
      `/contractWorkspaces/${encodeURIComponent(contractId)}`,
      queryParams
    );
  }
});

// ../ap/packages/pieces/community/sap-ariba/src/index.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), sapAriba = (0, import_pieces_framework16.createPiece)({
  displayName: "SAP Ariba",
  description: "Automate procurement, contracts, and invoices with SAP Ariba.",
  auth: sapAribaAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/sap-ariba.png",
  categories: [import_pieces_framework17.PieceCategory.BUSINESS_INTELLIGENCE, import_pieces_framework17.PieceCategory.COMMERCE],
  authors: ["onyedikachi-david"],
  actions: [
    listPurchaseOrders,
    listPurchaseOrderItems,
    getDocumentChanges,
    getPendingApprovables,
    getCatalogItems,
    getActiveCatalogs,
    getFacetData,
    listInvoices,
    searchContractWorkspaces,
    getContractWorkspace,
    createContractWorkspace,
    updateContractWorkspace,
    updateContractStatus,
    deleteContractWorkspace
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  sapAriba
});
