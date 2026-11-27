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

// ../ap/packages/pieces/community/editionguard/src/index.ts
var index_exports = {};
__export(index_exports, {
  editionguard: () => editionguard
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/editionguard/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://app.editionguard.com/api/v2", editionguardAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: `Your EditionGuard API token.

**How to get your API token:**
1. Log in to your [EditionGuard dashboard](https://app.editionguard.com)
2. Go to **Settings** (top-bar menu)
3. Find the **API** section and copy your **Rest API Token**`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${BASE_URL}/book`,
        headers: { Authorization: `token ${auth}` },
        queryParams: { page_size: "1" }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API token. Please check your credentials." };
    }
  }
});

// ../ap/packages/pieces/community/editionguard/src/lib/actions/send-ebook-download-links.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/editionguard/src/lib/common/api.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), BASE_URL2 = "https://app.editionguard.com/api/v2";
async function listBooks({ token }) {
  return (await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.GET,
    url: `${BASE_URL2}/book`,
    headers: { Authorization: `token ${token}` },
    queryParams: { page_size: "100000" }
  })).body.results;
}
async function createTransaction({
  token,
  resourceId,
  customerEmail,
  orderNumber,
  quantity,
  watermarkName,
  watermarkEmail,
  watermarkPhone
}) {
  let body = {
    resource_id: resourceId,
    customer_email: customerEmail,
    order_number: orderNumber,
    quantity
  };
  return watermarkName && (body.watermark_name = watermarkName), watermarkEmail && (body.watermark_email = watermarkEmail), watermarkPhone && (body.watermark_phone = watermarkPhone), (await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.POST,
    url: `${BASE_URL2}/transaction`,
    headers: {
      Authorization: `token ${token}`,
      "Content-Type": "application/json"
    },
    body
  })).body;
}
var editionguardApi = { listBooks, createTransaction };

// ../ap/packages/pieces/community/editionguard/src/lib/actions/send-ebook-download-links.ts
var sendEbookDownloadLinks = (0, import_pieces_framework2.createAction)({
  auth: editionguardAuth,
  name: "send_ebook_download_links",
  displayName: "Send eBook Download Links",
  description: "Creates a transaction for an eBook and sends the download link(s) to your customer. Returns the download link you can deliver by email or any other channel.",
  audience: "both",
  aiMetadata: {
    description: "Creates an EditionGuard fulfillment transaction for a selected DRM-protected eBook (referenced by its resource_id) and generates protected download link(s) for a customer email. Use to deliver a purchased eBook after an order. The watermark name/email/phone inputs apply only to Social DRM (EditionMark) titles and are ignored for Adobe DRM or Readium LCP. Not idempotent: each call creates a new transaction and issues fresh links.",
    idempotent: !1
  },
  props: {
    resource_id: import_pieces_framework2.Property.Dropdown({
      displayName: "eBook",
      description: "Select the eBook to send to your customer.",
      auth: editionguardAuth,
      refreshers: [],
      required: !0,
      options: async ({ auth }) => {
        if (!auth)
          return { disabled: !0, options: [], placeholder: "Please connect your account first." };
        try {
          return {
            disabled: !1,
            options: (await editionguardApi.listBooks({ token: auth.secret_text })).map((book) => {
              var _a;
              return {
                label: `${book.title} (${(_a = book.drm_type) != null ? _a : "Unknown DRM"})`,
                value: book.resource_id
              };
            })
          };
        } catch (e) {
          return { disabled: !0, options: [], placeholder: "Failed to load eBooks. Check your connection." };
        }
      }
    }),
    customer_email: import_pieces_framework2.Property.ShortText({
      displayName: "Customer Email",
      description: "The customer's email address where the download instructions will be sent.",
      required: !0
    }),
    order_number: import_pieces_framework2.Property.ShortText({
      displayName: "Order Number",
      description: "Your order or transaction reference number (e.g. from your eCommerce platform). Used for tracking and reporting.",
      required: !0
    }),
    quantity: import_pieces_framework2.Property.Number({
      displayName: "Quantity",
      description: "Number of download links to generate. Use 1 for a single copy. Increase for multi-seat or multi-device licenses.",
      required: !1,
      defaultValue: 1
    }),
    watermark_name: import_pieces_framework2.Property.ShortText({
      displayName: "Customer's Full Name (Social DRM)",
      description: "Only applies when the eBook uses **Social DRM (EditionMark)**. The customer's name will be embedded as a visible watermark in the eBook. Leave empty for Adobe DRM or Readium LCP books.",
      required: !1
    }),
    watermark_email: import_pieces_framework2.Property.ShortText({
      displayName: "Customer's Email for Watermark (Social DRM)",
      description: "Only applies when the eBook uses **Social DRM (EditionMark)**. This email will be embedded as a watermark. Leave empty for Adobe DRM or Readium LCP books.",
      required: !1
    }),
    watermark_phone: import_pieces_framework2.Property.ShortText({
      displayName: "Customer's Phone for Watermark (Social DRM)",
      description: "Only applies when the eBook uses **Social DRM (EditionMark)**. This phone number will be embedded as a watermark. Leave empty for Adobe DRM or Readium LCP books.",
      required: !1
    })
  },
  async run(context) {
    let { resource_id, customer_email, order_number, quantity, watermark_name, watermark_email, watermark_phone } = context.propsValue;
    return await editionguardApi.createTransaction({
      token: context.auth.secret_text,
      resourceId: resource_id,
      customerEmail: customer_email,
      orderNumber: order_number,
      quantity: quantity != null ? quantity : 1,
      watermarkName: watermark_name != null ? watermark_name : void 0,
      watermarkEmail: watermark_email != null ? watermark_email : void 0,
      watermarkPhone: watermark_phone != null ? watermark_phone : void 0
    });
  }
});

// ../ap/packages/pieces/community/editionguard/src/index.ts
var editionguard = (0, import_pieces_framework3.createPiece)({
  displayName: "EditionGuard",
  description: "Secure eBook DRM and fulfillment \u2014 protect and deliver EPUB, MOBI, and PDF files with Adobe DRM, Readium LCP, or Social DRM.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/editionguard.png",
  categories: [import_pieces_framework4.PieceCategory.CONTENT_AND_FILES],
  auth: editionguardAuth,
  authors: ["sanket-a11y"],
  actions: [
    sendEbookDownloadLinks,
    (0, import_pieces_common3.createCustomApiCallAction)({
      baseUrl: () => "https://app.editionguard.com/api/v2",
      auth: editionguardAuth,
      authMapping: async (auth) => ({
        Authorization: `token ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  editionguard
});
