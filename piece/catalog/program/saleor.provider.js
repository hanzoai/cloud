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

// ../ap/packages/pieces/community/saleor/src/index.ts
var index_exports = {};
__export(index_exports, {
  saleor: () => saleor,
  saleorAuth: () => saleorAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/saleor/src/lib/actions/raw-graphql-query.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var import_pieces_common = require("@activepieces/pieces-common"), saleorRawGraphqlQuery = (0, import_pieces_framework.createAction)({
  name: "rawGraphqlQuery",
  displayName: "Raw GraphQL query",
  description: "Perform a raw GraphQL query",
  audience: "both",
  aiMetadata: { description: "Sends an arbitrary GraphQL operation (query or mutation) to the Saleor GraphQL endpoint with optional variables. Use this as the escape hatch when no dedicated action covers the data or operation you need. Not idempotent in general: it can run mutations, so repeating it may create or change data depending on the operation you supply.", idempotent: !1 },
  auth: saleorAuth,
  props: {
    query: import_pieces_framework.Property.LongText({ displayName: "Query", required: !0 }),
    variables: import_pieces_framework.Property.Object({ displayName: "Parameters", required: !1 })
  },
  async run({ auth, propsValue }) {
    let { query, variables } = propsValue, { token, apiUrl } = auth.props;
    return await import_pieces_common.httpClient.sendRequest({
      url: apiUrl,
      method: import_pieces_common.HttpMethod.POST,
      body: JSON.stringify({
        query,
        variables
      }),
      authentication: {
        type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
        token
      }
    });
  }
});

// ../ap/packages/pieces/community/saleor/src/lib/actions/get-order.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common"), getOrder = (0, import_pieces_framework2.createAction)({
  name: "getOrder",
  displayName: "Get an order",
  description: "Get an order according to the ID",
  audience: "both",
  aiMetadata: { description: "Fetches a single Saleor order by its order ID, returning core fields such as number, status, total, customer, and line items. Use it to look up the current details of a known order. Requires the order ID and is read-only and idempotent.", idempotent: !0 },
  auth: saleorAuth,
  props: {
    orderId: import_pieces_framework2.Property.ShortText({
      displayName: "The ID of the order",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let orderId = propsValue.orderId, { token, apiUrl } = auth.props;
    return await import_pieces_common2.httpClient.sendRequest({
      url: apiUrl,
      method: import_pieces_common2.HttpMethod.POST,
      body: JSON.stringify({
        query: `
            query GetOrder($orderId: ID!) {
                order(id: $orderId) {
                    id
                    number
                    created
                    status
                    
                    total {
                        gross {
                            amount
                            currency
                        }
                    }
                    
                    user {
                        email
                        firstName
                        lastName
                    }
                    
                    lines {
                        productName
                        variantName
                        quantity
                        unitPrice {
                            gross {
                                amount
                                currency
                            }
                        }
                    }
                }
            }
        `,
        variables: { orderId }
      }),
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token
      }
    });
  }
});

// ../ap/packages/pieces/community/saleor/src/lib/actions/add-note-to-order.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common"), addOrderNote = (0, import_pieces_framework3.createAction)({
  name: "addOrderNote",
  displayName: "Add Order Note",
  description: "Add a note to a Saleor order",
  audience: "both",
  aiMetadata: { description: "Adds a note to a Saleor order identified by its order ID, appending the given message to that order. Use it to record a comment or annotation on a known order. Requires the order ID and message; not idempotent, since each call appends another note.", idempotent: !1 },
  auth: saleorAuth,
  props: {
    orderId: import_pieces_framework3.Property.ShortText({
      displayName: "Order ID",
      description: "The ID of the order",
      required: !0
    }),
    message: import_pieces_framework3.Property.LongText({
      displayName: "Note Message",
      description: "The content of the note",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let { orderId, message } = propsValue, { token, apiUrl } = auth.props;
    return await import_pieces_common3.httpClient.sendRequest({
      url: apiUrl,
      method: import_pieces_common3.HttpMethod.POST,
      body: JSON.stringify({
        query: `
        mutation AddOrderNote($orderId: ID!, $message: String!) {
            orderNoteAdd(
                order: $orderId,
                input: {
                    message: $message,
                }
            ) {
                order {
                    id
                    number
                }
            }
        }
    `,
        variables: { orderId, message }
      }),
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token
      }
    });
  }
});

// ../ap/packages/pieces/community/saleor/src/index.ts
var saleorAuth = import_pieces_framework4.PieceAuth.CustomAuth({
  description: "Saleor",
  required: !0,
  props: {
    apiUrl: import_pieces_framework4.Property.ShortText({
      displayName: "Saleor API URL",
      required: !0,
      description: "Your Saleor GraphQL API endpoint"
    }),
    token: import_pieces_framework4.Property.ShortText({
      displayName: "Saleor token",
      required: !0,
      description: "Your token"
    })
  }
}), saleor = (0, import_pieces_framework4.createPiece)({
  displayName: "Saleor",
  auth: saleorAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/saleor.png",
  authors: ["Kevinyu-alan"],
  actions: [
    saleorRawGraphqlQuery,
    getOrder,
    addOrderNote
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  saleor,
  saleorAuth
});
