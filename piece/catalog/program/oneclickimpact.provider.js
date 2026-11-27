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

// ../ap/packages/pieces/community/oneclickimpact/src/index.ts
var index_exports = {};
__export(index_exports, {
  oneclickimpact: () => oneclickimpact
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework6 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/oneclickimpact/src/lib/actions/capture-carbon.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/oneclickimpact/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), oneclickimpactAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "API Key for authenticating with 1ClickImpact",
  required: !0
});

// ../ap/packages/pieces/community/oneclickimpact/src/lib/actions/capture-carbon.ts
var import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/oneclickimpact/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://api.1clickimpact.com/v1";
async function makeRequest(api_key, method, path, body) {
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${path}`,
      headers: {
        "x-api-key": api_key,
        "Content-Type": "application/json"
      },
      body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/oneclickimpact/src/lib/actions/capture-carbon.ts
var captureCarbon = (0, import_pieces_framework2.createAction)({
  auth: oneclickimpactAuth,
  name: "captureCarbon",
  classification: "WRITE",
  displayName: "Capture carbon",
  description: "Capture carbon for yourself or on behalf of the customer",
  audience: "both",
  aiMetadata: { description: "Purchases carbon capture through 1ClickImpact for a given amount (in lbs, 1 to 10,000,000), optionally attributed to a named customer email/name. Use to offset emissions as part of a transaction or fulfillment flow. Not idempotent \u2014 each call records a new capture, so repeating it captures additional carbon.", idempotent: !1 },
  props: {
    amount: import_pieces_framework2.Property.Number({
      displayName: "Amount",
      description: "The amount of carbon to capture in lbs (1 and 10,000,000)",
      required: !0
    }),
    customerEmail: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Email",
      description: "The email of the customer on whose behalf the carbon is being captured",
      required: !1
    }),
    customerName: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Name",
      description: "The name of the customer on whose behalf the carbon is being captured",
      required: !1
    }),
    notify: import_pieces_framework2.Property.Checkbox({
      displayName: "Notify",
      description: "Notification preference",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      amount: propsValue.amount,
      customerEmail: propsValue.customerEmail,
      customerName: propsValue.customerName,
      notify: propsValue.notify
    };
    return await makeRequest(
      auth.secret_text,
      import_pieces_common2.HttpMethod.POST,
      "/capture_carbon",
      body
    );
  }
});

// ../ap/packages/pieces/community/oneclickimpact/src/lib/actions/clean-ocean.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), cleanOcean = (0, import_pieces_framework3.createAction)({
  auth: oneclickimpactAuth,
  name: "cleanOcean",
  classification: "WRITE",
  displayName: "Clean ocean",
  description: "Clean  ocean waste yourself or on behalf of the customer",
  audience: "both",
  aiMetadata: { description: "Purchases ocean-waste cleanup through 1ClickImpact for a given amount (in lbs, 1 to 10,000,000), optionally attributed to a named customer email/name. Use to fund cleanup as part of a transaction or fulfillment flow. Not idempotent \u2014 each call records a new cleanup, so repeating it cleans additional waste.", idempotent: !1 },
  props: {
    amount: import_pieces_framework3.Property.Number({
      displayName: "Amount",
      description: "The amount of ocean waste to clean in lbs (1 and 10,000,000)",
      required: !0
    }),
    customerEmail: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Email",
      description: "The email of the customer on whose behalf the ocean waste is being cleaned",
      required: !1
    }),
    customerName: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Name",
      description: "The name of the customer on whose behalf the ocean waste is being cleaned",
      required: !1
    }),
    notify: import_pieces_framework3.Property.Checkbox({
      displayName: "Notify",
      description: "Notification preference",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      amount: propsValue.amount,
      customerEmail: propsValue.customerEmail,
      customerName: propsValue.customerName,
      notify: propsValue.notify
    };
    return await makeRequest(
      auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      "/clean_ocean",
      body
    );
  }
});

// ../ap/packages/pieces/community/oneclickimpact/src/lib/actions/donate-money.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var donateMoney = (0, import_pieces_framework4.createAction)({
  auth: oneclickimpactAuth,
  name: "donateMoney",
  classification: "WRITE",
  displayName: "Donate Money",
  description: " Donate money to support environmental causes",
  audience: "both",
  aiMetadata: { description: "Makes a monetary donation to environmental causes through 1ClickImpact for an amount in USD (minimum $1.00, converted to cents internally), optionally attributed to a named customer email/name. Use to fund impact as part of a transaction or fulfillment flow. Not idempotent \u2014 each call records a new donation, so repeating it donates again.", idempotent: !1 },
  props: {
    amount: import_pieces_framework4.Property.Number({
      displayName: "Amount (USD)",
      description: "The amount in USD you wish to donate. Minimum $1.00",
      required: !0
    }),
    customerEmail: import_pieces_framework4.Property.ShortText({
      displayName: "Customer Email",
      description: "The email of the customer on whose behalf the donation is being made",
      required: !1
    }),
    customerName: import_pieces_framework4.Property.ShortText({
      displayName: "Customer Name",
      description: "The name of the customer on whose behalf the donation is being made",
      required: !1
    }),
    notify: import_pieces_framework4.Property.Checkbox({
      displayName: "Notify",
      description: "Notification preference",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      customerEmail: propsValue.customerEmail,
      customerName: propsValue.customerName,
      notify: propsValue.notify
    }, amountInCents = Math.round(propsValue.amount * 100);
    return await makeRequest(
      auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/donate_money",
      __spreadProps(__spreadValues({}, body), { amount: amountInCents })
    );
  }
});

// ../ap/packages/pieces/community/oneclickimpact/src/lib/actions/plant-trees.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), plantTrees = (0, import_pieces_framework5.createAction)({
  auth: oneclickimpactAuth,
  name: "plantTrees",
  classification: "WRITE",
  displayName: "Plant Trees",
  description: "Plant trees for yourself or on behalf of the customer",
  audience: "both",
  aiMetadata: { description: "Purchases tree planting through 1ClickImpact for a given number of trees (1 to 10,000,000), optionally attributed to a named customer email/name. Use to fund reforestation as part of a transaction or fulfillment flow. Not idempotent \u2014 each call records a new planting, so repeating it plants additional trees.", idempotent: !1 },
  props: {
    amount: import_pieces_framework5.Property.Number({
      displayName: "Amount",
      description: "The number of trees to plant (1 and 10,000,000)",
      required: !0
    }),
    customerEmail: import_pieces_framework5.Property.ShortText({
      displayName: "Customer Email",
      description: "The email of the customer on whose behalf the trees are being planted",
      required: !1
    }),
    customerName: import_pieces_framework5.Property.ShortText({
      displayName: "Customer Name",
      description: "The name of the customer on whose behalf the trees are being planted",
      required: !1
    }),
    notify: import_pieces_framework5.Property.Checkbox({
      displayName: "Notify",
      description: "Notification preference",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      amount: propsValue.amount,
      customerEmail: propsValue.customerEmail,
      customerName: propsValue.customerName,
      notify: propsValue.notify
    };
    return await makeRequest(
      auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      "/plant_trees",
      body
    );
  }
});

// ../ap/packages/pieces/community/oneclickimpact/src/index.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), oneclickimpact = (0, import_pieces_framework6.createPiece)({
  displayName: "1ClickImpact",
  auth: oneclickimpactAuth,
  minimumSupportedRelease: "0.36.1",
  description: "Make a positive environmental impact with every transaction.",
  logoUrl: "https://cdn.activepieces.com/pieces/oneclickimpact.png",
  authors: ["sanket-a11y"],
  actions: [
    captureCarbon,
    cleanOcean,
    donateMoney,
    plantTrees,
    (0, import_pieces_common6.createCustomApiCallAction)({
      auth: oneclickimpactAuth,
      baseUrl: () => "https://api.1clickimpact.com/v1",
      authMapping: async (auth) => ({
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  oneclickimpact
});
