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

// ../ap/packages/pieces/community/billplz/src/index.ts
var index_exports = {};
__export(index_exports, {
  billplz: () => billplz
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/billplz/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), billplzAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Secret Key",
  description: "Enter your Billplz API Secret Key. You can find this in your Billplz account settings.",
  required: !0
});

// ../ap/packages/pieces/community/billplz/src/lib/actions/create-bill.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/billplz/src/lib/common/api.ts
var import_pieces_common = require("@activepieces/pieces-common");
var BILLPLZ_API_URL = "https://www.billplz.com/api", BILLPLZ_SANDBOX_API_URL = "https://www.billplz-sandbox.com/api", billplzApi = {
  async makeRequest(auth, request) {
    let baseUrl = auth.includes("sandbox") ? BILLPLZ_SANDBOX_API_URL : BILLPLZ_API_URL;
    return import_pieces_common.httpClient.sendRequest(__spreadProps(__spreadValues({}, request), {
      url: `${baseUrl}${request.url}`,
      authentication: {
        type: import_pieces_common.AuthenticationType.BASIC,
        username: auth,
        password: ""
      },
      headers: {
        "Content-Type": "application/json"
      }
    }));
  },
  async getCollections(auth) {
    return this.makeRequest(auth, {
      method: import_pieces_common.HttpMethod.GET,
      url: "/v3/collections"
    });
  },
  async createBill(auth, billData) {
    return this.makeRequest(auth, {
      method: import_pieces_common.HttpMethod.POST,
      url: "/v3/bills",
      body: billData
    });
  },
  async getBill(auth, billId) {
    return this.makeRequest(auth, {
      method: import_pieces_common.HttpMethod.GET,
      url: `/v3/bills/${billId}`
    });
  }
};

// ../ap/packages/pieces/community/billplz/src/lib/actions/create-bill.ts
var createBill = (0, import_pieces_framework2.createAction)({
  name: "create_bill",
  classification: "WRITE",
  displayName: "Create Bill",
  description: "Create a new bill within a collection",
  audience: "both",
  aiMetadata: { description: "Creates a new Billplz bill (payment request) inside a given collection, charging a recipient identified by email and/or mobile for an amount in Malaysian Ringgit. Either email or mobile is required, along with a recipient name, description, and callback URL; optionally set a due date, redirect URL, and reference fields. Enabling the deliver option sends the recipient an email/SMS notification. Not idempotent: each call creates a distinct bill and may dispatch a notification.", idempotent: !1 },
  auth: billplzAuth,
  props: {
    collection_id: import_pieces_framework2.Property.Dropdown({
      displayName: "Collection",
      description: "Select the collection to create the bill in",
      required: !0,
      refreshers: [],
      auth: billplzAuth,
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            options: [],
            placeholder: "Please authenticate first"
          };
        try {
          return {
            options: (await billplzApi.getCollections(auth.secret_text)).body.map((collection) => ({
              label: collection.title || collection.name || collection.id,
              value: collection.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load collections"
          };
        }
      }
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "Recipient email address (required if mobile not provided)",
      required: !1
    }),
    mobile: import_pieces_framework2.Property.ShortText({
      displayName: "Mobile Number",
      description: "Recipient mobile number with country code (required if email not provided)",
      required: !1
    }),
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Recipient Name",
      description: "Name of the bill recipient",
      required: !0
    }),
    amount: import_pieces_framework2.Property.Number({
      displayName: "Amount (RM)",
      description: "Amount in Malaysian Ringgit (e.g., 2.00 for RM 2.00)",
      required: !0
    }),
    description: import_pieces_framework2.Property.ShortText({
      displayName: "Description",
      description: "Bill description that will be displayed on the bill",
      required: !0
    }),
    callback_url: import_pieces_framework2.Property.ShortText({
      displayName: "Callback URL",
      description: "Webhook URL to receive payment completion notifications",
      required: !0
    }),
    due_at: import_pieces_framework2.Property.ShortText({
      displayName: "Due Date",
      description: "Due date in YYYY-MM-DD format (optional, defaults to today)",
      required: !1
    }),
    redirect_url: import_pieces_framework2.Property.ShortText({
      displayName: "Redirect URL",
      description: "URL to redirect customer after payment completion",
      required: !1
    }),
    deliver: import_pieces_framework2.Property.Checkbox({
      displayName: "Send Email/SMS",
      description: "Automatically send email and SMS notification to recipient",
      required: !1,
      defaultValue: !1
    }),
    reference_1_label: import_pieces_framework2.Property.ShortText({
      displayName: "Reference 1 Label",
      description: "Label for first reference field",
      required: !1,
      defaultValue: "Reference 1"
    }),
    reference_1: import_pieces_framework2.Property.ShortText({
      displayName: "Reference 1 Value",
      description: "Value for first reference field",
      required: !1
    }),
    reference_2_label: import_pieces_framework2.Property.ShortText({
      displayName: "Reference 2 Label",
      description: "Label for second reference field",
      required: !1,
      defaultValue: "Reference 2"
    }),
    reference_2: import_pieces_framework2.Property.ShortText({
      displayName: "Reference 2 Value",
      description: "Value for second reference field",
      required: !1
    })
  },
  async run(context) {
    let { auth, propsValue } = context, amountInCents = Math.round(propsValue.amount * 100);
    if (!propsValue.email && !propsValue.mobile)
      throw new Error("Either email or mobile number must be provided");
    let billData = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      collection_id: propsValue.collection_id,
      email: propsValue.email,
      mobile: propsValue.mobile,
      name: propsValue.name,
      amount: amountInCents,
      description: propsValue.description,
      callback_url: propsValue.callback_url
    }, propsValue.due_at && { due_at: propsValue.due_at }), propsValue.redirect_url && { redirect_url: propsValue.redirect_url }), propsValue.deliver !== void 0 && { deliver: propsValue.deliver }), propsValue.reference_1_label && { reference_1_label: propsValue.reference_1_label }), propsValue.reference_1 && { reference_1: propsValue.reference_1 }), propsValue.reference_2_label && { reference_2_label: propsValue.reference_2_label }), propsValue.reference_2 && { reference_2: propsValue.reference_2 });
    try {
      return (await billplzApi.createBill(auth.secret_text, billData)).body;
    } catch (error) {
      throw new Error(`Failed to create bill: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/billplz/src/lib/actions/get-bill.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var getBill = (0, import_pieces_framework3.createAction)({
  name: "get_bill",
  classification: "READ",
  displayName: "Get Bill",
  description: "Retrieve information about a specific bill",
  audience: "both",
  aiMetadata: { description: "Looks up a single Billplz bill by its bill ID, returning its details and payment status. Use to check whether a bill has been paid or to fetch its current state. Requires a known bill ID. Idempotent: a read-only lookup with no side effects.", idempotent: !0 },
  auth: billplzAuth,
  props: {
    bill_id: import_pieces_framework3.Property.ShortText({
      displayName: "Bill ID",
      description: "The ID of the bill to retrieve",
      required: !0
    })
  },
  async run(context) {
    let { auth, propsValue } = context;
    if (!propsValue.bill_id)
      throw new Error("Bill ID is required");
    try {
      return (await billplzApi.getBill(auth.secret_text, propsValue.bill_id)).body;
    } catch (error) {
      throw new Error(`Failed to get bill: ${error.message}`);
    }
  }
});

// ../ap/packages/pieces/community/billplz/src/lib/triggers/get-collections.trigger.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), polling = {
  strategy: import_pieces_common2.DedupeStrategy.LAST_ITEM,
  items: async ({ auth }) => {
    try {
      return ((await billplzApi.getCollections(auth.secret_text)).body.collections || []).map((collection) => ({
        epochMilliSeconds: Date.now(),
        data: collection
      }));
    } catch (error) {
      return console.error("Failed to fetch collections:", error), [];
    }
  }
}, getCollectionsTrigger = (0, import_pieces_framework4.createTrigger)({
  name: "get_collections",
  classification: "READ",
  displayName: "Get Collections",
  description: "Triggers when there are new collections",
  aiMetadata: {
    description: "Fires when a new Billplz collection appears on the authenticated account. Polls the collections list and emits one event per newly detected collection, representing a container into which bills can be created."
  },
  auth: billplzAuth,
  props: {},
  type: import_pieces_framework4.TriggerStrategy.POLLING,
  sampleData: {
    id: "inbmmepb",
    title: "My First API Collection",
    logo: {
      thumb_url: null,
      avatar_url: null
    },
    split_payment: {
      email: null,
      fixed_cut: null,
      variable_cut: null,
      split_header: !1
    },
    status: "active"
  },
  async onEnable(context) {
    await import_pieces_common2.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common2.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common2.pollingHelper.poll(polling, context);
  },
  async test(context) {
    return await import_pieces_common2.pollingHelper.test(polling, context);
  }
});

// ../ap/packages/pieces/community/billplz/src/index.ts
var billplz = (0, import_pieces_framework5.createPiece)({
  displayName: "Billplz",
  description: "Create and manage bills and collections with Billplz API",
  auth: billplzAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/billplz.png",
  authors: ["onyedikachi-david"],
  actions: [createBill, getBill],
  triggers: [getCollectionsTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  billplz
});
