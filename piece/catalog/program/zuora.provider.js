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

// ../ap/packages/pieces/community/zuora/src/index.ts
var index_exports = {};
__export(index_exports, {
  zuora: () => zuora
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/zuora/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), zuoraAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `
  - **Establish a Dedicated API User** : Zuora recommends creating a distinct API user specifically for OAuth authentication with write permissions. Refer to the [API User Creation Guide](https://knowledgecenter.zuora.com/Zuora_Central_Platform/Tenant_Management/A_Administrator_Settings/Manage_Users/Create_an_API_User) for detailed instructions on setting up this user.
  - **Create OAuth Client** : After setting up the API user, it's essential to create an OAuth client tailored for this user.Detailed instructions can be found in the [OAuth Client Creation Guide](https://knowledgecenter.zuora.com/Zuora_Central_Platform/Tenant_Management/A_Administrator_Settings/Manage_Users).
  - **Refer to Base API URL** : Utilize the [Base API URL](https://developer.zuora.com/v1-api-reference/introduction/#section/Introduction/Access-to-the-API) provided by Zuora for your environment. This URL serves as the entry point for accessing the Zuora API.`,
  required: !0,
  props: {
    clientId: import_pieces_framework.Property.ShortText({
      displayName: "Client ID",
      required: !0
    }),
    clientSecret: import_pieces_framework.Property.ShortText({
      displayName: "Client Secret",
      required: !0
    }),
    environment: import_pieces_framework.Property.StaticDropdown({
      displayName: "Environment",
      required: !0,
      options: {
        disabled: !1,
        options: [
          {
            label: "US Cloud 1 Production",
            value: "https://rest.na.zuora.com"
          },
          {
            label: "US Cloud 1 API Sandbox",
            value: "https://rest.sandbox.na.zuora.com"
          },
          {
            label: "US Cloud 2 Production",
            value: "https://rest.zuora.com"
          },
          {
            label: "US Cloud 2 API Sandbox",
            value: "https://rest.apisandbox.zuora.com"
          },
          {
            label: "US Central Sandbox",
            value: "https://rest.test.zuora.com"
          },
          {
            label: "EU Production",
            value: "https://rest.eu.zuora.com"
          },
          {
            label: "EU API Sandbox",
            value: "https://rest.sandbox.eu.zuora.com"
          },
          {
            label: "EU Developer & Central Sandbox",
            value: "https://rest.test.eu.zuora.com"
          }
        ]
      }
    })
  }
});

// ../ap/packages/pieces/community/zuora/src/lib/actions/create-invoice.action.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/zuora/src/lib/common/index.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
async function getAccessToken(auth) {
  let request = {
    method: import_pieces_common.HttpMethod.POST,
    url: `${auth.props.environment}/oauth/token`,
    body: new URLSearchParams({
      client_id: auth.props.clientId,
      client_secret: auth.props.clientSecret,
      grant_type: "client_credentials"
    }),
    headers: {
      "Content-Type": "application/x-www-form-urlencoded"
    }
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body.access_token;
}

// ../ap/packages/pieces/community/zuora/src/lib/actions/create-invoice.action.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), createInvoiceAction = (0, import_pieces_framework3.createAction)({
  auth: zuoraAuth,
  name: "create-invoice",
  displayName: "Create Invoice",
  description: "Create a standalone invoice.",
  audience: "both",
  aiMetadata: {
    description: "Creates a standalone invoice in Zuora for a given customer account, with one or more line items billed to product rate plan charges. Use when an agent needs to bill an account outside the normal subscription cycle; the target account is identified by its account number and each invoice item must reference a product rate plan charge ID. Not idempotent: each call posts a new invoice, so repeating it produces duplicates.",
    idempotent: !1
  },
  props: {
    accountNumber: import_pieces_framework3.Property.ShortText({
      displayName: "Customer Account Number",
      description: "The number of the customer account associated with the invoice.",
      required: !0
    }),
    autoPay: import_pieces_framework3.Property.Checkbox({
      displayName: "Auto Pay?",
      description: "Whether invoices are automatically picked up for processing in the corresponding payment run.",
      required: !1
    }),
    comments: import_pieces_framework3.Property.LongText({
      displayName: "Comments",
      required: !1
    }),
    dueDate: import_pieces_framework3.Property.DateTime({
      displayName: "Due Date",
      description: "Provide YYYY-MM-DD format.",
      required: !0
    }),
    invoiceDate: import_pieces_framework3.Property.DateTime({
      displayName: "Invoice Date",
      required: !0,
      description: "Provide YYYY-MM-DD format."
    }),
    invoiceItems: import_pieces_framework3.Property.Array({
      displayName: "Invoice Items",
      required: !1,
      properties: {
        productRatePlanChargeId: import_pieces_framework3.Property.ShortText({
          displayName: "Product Rate Plan Charge ID",
          description: "The ID of the product rate plan charge that the invoice item is created from.You can use `Find Product Rate Plan` action to search associate rate plan charge.",
          required: !0
        }),
        amount: import_pieces_framework3.Property.Number({
          displayName: "Amount",
          required: !0
        }),
        description: import_pieces_framework3.Property.LongText({
          displayName: "Description",
          required: !1
        }),
        purchaseOrderNumber: import_pieces_framework3.Property.ShortText({
          displayName: "Purchase Order Number",
          required: !1
        }),
        quantity: import_pieces_framework3.Property.ShortText({
          displayName: "Quantity",
          required: !1
        }),
        serviceStartDate: import_pieces_framework3.Property.ShortText({
          displayName: "Service Start Date",
          description: "Provide YYYY-MM-DD format.",
          required: !0
        }),
        serviceEndDate: import_pieces_framework3.Property.ShortText({
          displayName: "Service End Date",
          description: "Provide YYYY-MM-DD format.",
          required: !1
        })
      }
    }),
    status: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Status",
      required: !1,
      defaultValue: "Draft",
      options: {
        disabled: !1,
        options: [
          { label: "Draft", value: "Draft" },
          { label: "Posted", value: "Posted" }
        ]
      }
    })
  },
  async run(context) {
    let { accountNumber, autoPay, comments, dueDate, invoiceDate, status } = context.propsValue, invoiceItems = context.propsValue.invoiceItems, token = await getAccessToken(context.auth), body = {
      accountNumber,
      autoPay,
      comments,
      dueDate,
      invoiceDate,
      status,
      invoiceItems
    }, request = {
      method: import_pieces_common2.HttpMethod.POST,
      url: `${context.auth.props.environment}/v1/invoices`,
      authentication: { type: import_pieces_common2.AuthenticationType.BEARER_TOKEN, token },
      body
    };
    return (await import_pieces_common2.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/zuora/src/index.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/zuora/src/lib/actions/find-product-rate-plans.action.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), findProductRatePlanAction = (0, import_pieces_framework4.createAction)({
  auth: zuoraAuth,
  name: "find-product-rate-plan",
  displayName: "Find Product Rate Plan",
  description: "Retrieves product rate plan with charges.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a Zuora product rate plan (and its expanded rate plan charges) by exact rate plan name scoped to a specific product ID. Use to find the rate plan charge IDs needed when creating an invoice or subscription; both the rate plan name and the product ID are required and matched by exact equality. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Product Rate Plan Name",
      description: "i.e. StealthCo Premium",
      required: !0
    }),
    productid: import_pieces_framework4.Property.ShortText({
      displayName: "Product ID",
      required: !0
    })
  },
  async run(context) {
    let name = context.propsValue.name, productid = context.propsValue.productid, token = await getAccessToken(context.auth), request = {
      method: import_pieces_common3.HttpMethod.GET,
      url: `${context.auth.props.environment}/object-query/product-rate-plans?filter[]=name.EQ:${name}&filter[]=productid.EQ:${productid}`,
      authentication: { type: import_pieces_common3.AuthenticationType.BEARER_TOKEN, token },
      queryParams: {
        "expand[]": "productrateplancharges"
      }
    };
    return (await import_pieces_common3.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/zuora/src/lib/actions/find-account.action.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common"), findAccountAction = (0, import_pieces_framework5.createAction)({
  auth: zuoraAuth,
  name: "find-account",
  displayName: "Find Customer Account",
  description: "Retrieves account based on name.",
  audience: "both",
  aiMetadata: {
    description: "Looks up Zuora customer accounts whose name exactly matches the given value, via an object query. Use to resolve an account before billing or further account operations; matching is an exact equality on the account name, not a partial or fuzzy search. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Account Name",
      required: !0
    })
  },
  async run(context) {
    let name = context.propsValue.name, token = await getAccessToken(context.auth), request = {
      method: import_pieces_common4.HttpMethod.GET,
      url: `${context.auth.props.environment}/object-query/accounts`,
      authentication: { type: import_pieces_common4.AuthenticationType.BEARER_TOKEN, token },
      queryParams: {
        "filter[]": `name.EQ:${name}`
      }
    };
    return (await import_pieces_common4.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/zuora/src/lib/actions/find-product.action.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common"), findProductAction = (0, import_pieces_framework6.createAction)({
  auth: zuoraAuth,
  name: "find-product",
  displayName: "Find Product",
  description: "Retrieves product based on sku.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a Zuora product by exact SKU match via an object query. Use to resolve a product (e.g. to obtain its product ID before finding rate plans) when you know the SKU; matching is exact equality on the SKU, not a partial search. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    sku: import_pieces_framework6.Property.ShortText({
      displayName: "Product SKU",
      required: !0
    })
  },
  async run(context) {
    let sku = context.propsValue.sku, token = await getAccessToken(context.auth), request = {
      method: import_pieces_common5.HttpMethod.GET,
      url: `${context.auth.props.environment}/object-query/products`,
      authentication: { type: import_pieces_common5.AuthenticationType.BEARER_TOKEN, token },
      queryParams: {
        "filter[]": `sku.EQ:${sku}`
      }
    };
    return (await import_pieces_common5.httpClient.sendRequest(request)).body;
  }
});

// ../ap/packages/pieces/community/zuora/src/index.ts
var zuora = (0, import_pieces_framework7.createPiece)({
  displayName: "Zuora",
  auth: zuoraAuth,
  minimumSupportedRelease: "0.27.1",
  description: "Cloud-based subscription management platform that enables businesses to launch and monetize subscription services.",
  logoUrl: "https://cdn.activepieces.com/pieces/zuora.png",
  categories: [
    import_pieces_framework8.PieceCategory.SALES_AND_CRM,
    import_pieces_framework8.PieceCategory.PAYMENT_PROCESSING
  ],
  authors: ["kishanprmr"],
  actions: [
    createInvoiceAction,
    findAccountAction,
    findProductRatePlanAction,
    findProductAction
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  zuora
});
