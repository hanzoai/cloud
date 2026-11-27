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

// ../ap/packages/pieces/community/barcode-lookup/src/index.ts
var index_exports = {};
__export(index_exports, {
  barcodeLookup: () => barcodeLookup
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/barcode-lookup/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), barcodeLookupAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "API Key for Barcode Lookup",
  required: !0
});

// ../ap/packages/pieces/community/barcode-lookup/src/lib/actions/search-by-barcode.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var searchByBarcode = (0, import_pieces_framework2.createAction)({
  auth: barcodeLookupAuth,
  name: "searchByBarcode",
  classification: "READ",
  displayName: "Search By Barcode",
  description: "Search for product information by barcode",
  audience: "both",
  aiMetadata: {
    description: "Looks up product information from the Barcode Lookup API for a single barcode/UPC/EAN number. Use it to resolve a barcode into product details such as name, brand, and category. Requires the exact barcode number as input; this is a read-only lookup that is safe to repeat.",
    idempotent: !0
  },
  props: {
    barcode: import_pieces_framework2.Property.ShortText({
      displayName: "Barcode",
      description: "The barcode/UPC/EAN number to search for",
      required: !0
    }),
    formatted: import_pieces_framework2.Property.Checkbox({
      displayName: "Formatted Output",
      description: "Return results in a clean, easy to read format",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    let barcode = context.propsValue.barcode, formatted = context.propsValue.formatted ? "y" : "n", url = `https://api.barcodelookup.com/v3/products?barcode=${encodeURIComponent(
      barcode
    )}&formatted=${formatted}&key=${context.auth.secret_text}`;
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url
    })).body;
  }
});

// ../ap/packages/pieces/community/barcode-lookup/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), barcodeLookup = (0, import_pieces_framework3.createPiece)({
  displayName: "Barcode Lookup",
  auth: barcodeLookupAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/barcode-lookup.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework4.PieceCategory.COMMERCE],
  description: "Lookup product information by barcode number",
  actions: [searchByBarcode],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  barcodeLookup
});
