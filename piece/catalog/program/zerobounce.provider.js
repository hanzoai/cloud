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

// ../ap/packages/pieces/community/zerobounce/src/index.ts
var index_exports = {};
__export(index_exports, {
  zerobounce: () => zerobounce,
  zerobounceAuth: () => zerobounceAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/zerobounce/src/lib/actions/validate-email.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), validateEmail = (0, import_pieces_framework.createAction)({
  name: "validateEmail",
  classification: "READ",
  displayName: "Validate Email",
  description: "",
  audience: "both",
  aiMetadata: { description: "Validates a single email address through ZeroBounce, returning its deliverability status (e.g. valid, invalid, catch-all, abuse) plus related quality signals. Use to verify an address before sending or storing it. Optionally caches the result in the project store so repeat lookups of the same address skip the API call; this is a read-only check that is safe to repeat.", idempotent: !0 },
  props: {
    email: import_pieces_framework.Property.ShortText({
      displayName: "Email",
      required: !0
    }),
    cacheResponse: import_pieces_framework.Property.Checkbox({
      displayName: "Cache Response",
      description: "Store the response in the project store for future use.",
      required: !1,
      defaultValue: !0
    })
  },
  auth: zerobounceAuth,
  async run({ store, propsValue, auth }) {
    let key = `_zerobounce_${propsValue.email}`;
    if (propsValue.cacheResponse) {
      let cachedResponse = await store.get(key, import_pieces_framework.StoreScope.PROJECT);
      if (!(0, import_pieces_framework2.isNil)(cachedResponse))
        return cachedResponse;
    }
    let res = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `https://api.zerobounce.net/v2/validate?email=${propsValue.email}&api_key=${auth.secret_text}`
    });
    return propsValue.cacheResponse && await store.put(key, res.body, import_pieces_framework.StoreScope.PROJECT), res.body;
  }
});

// ../ap/packages/pieces/community/zerobounce/src/index.ts
var zerobounceAuth = import_pieces_framework3.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0
}), zerobounce = (0, import_pieces_framework3.createPiece)({
  displayName: "ZeroBounce",
  auth: zerobounceAuth,
  description: "ZeroBounce is an email validation service that helps you reduce bounces, improve email deliverability and increase email marketing ROI.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/zerobounce.png",
  authors: ["abuaboud"],
  actions: [validateEmail],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  zerobounce,
  zerobounceAuth
});
