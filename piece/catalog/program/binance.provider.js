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

// ../ap/packages/pieces/community/binance/src/index.ts
var index_exports = {};
__export(index_exports, {
  binance: () => binance
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/binance/src/lib/actions/fetch-pair-price.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), fetchCryptoPairPrice = (0, import_pieces_framework.createAction)({
  name: "fetch_crypto_pair_price",
  classification: "READ",
  displayName: "Fetch Pair Price",
  description: "Fetch the current price of a pair (e.g. BTC/USDT)",
  audience: "both",
  aiMetadata: { description: "Fetches the current spot price of a trading pair from the public Binance market data API by combining two coin symbols (e.g. first coin BTC + second coin USDT). Use to look up the live exchange rate of one crypto asset against another. No authentication required; the symbol must be a pair that exists on Binance or the request fails. Read-only lookup, idempotent.", idempotent: !0 },
  props: {
    first_coin: import_pieces_framework.Property.ShortText({
      displayName: "First Coin Symbol",
      description: "The currency to fetch the price for (e.g. 'BTC' in 'BTC/USDT')",
      required: !0
    }),
    second_coin: import_pieces_framework.Property.ShortText({
      displayName: "Second Coin Symbol",
      description: "The currency to fetch the price in (e.g. 'USDT' in 'BTC/USDT')",
      required: !0
    })
  },
  async run(context) {
    let { first_coin, second_coin } = context.propsValue;
    if (first_coin && second_coin)
      return await fetchCryptoPairPriceImpl(`${first_coin}${second_coin}`);
    throw Error("Missing parameter(s)");
  }
});
async function fetchCryptoPairPriceImpl(symbol) {
  let url = `https://api.binance.com/api/v3/ticker/price?symbol=${symbol.replace("/", "").replace(" ", "").toUpperCase()}`;
  try {
    let data = await (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url
    })).body;
    return Number(data.price);
  } catch (error) {
    throw console.error(`Error fetching price for symbol ${symbol}:`, error), error;
  }
}

// ../ap/packages/pieces/community/binance/src/index.ts
var binance = (0, import_pieces_framework2.createPiece)({
  displayName: "Binance",
  description: "Fetch the price of a crypto pair from Binance",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/binance.png",
  categories: [],
  auth: import_pieces_framework2.PieceAuth.None(),
  actions: [fetchCryptoPairPrice],
  authors: ["kishanprmr", "khaledmashaly", "abuaboud"],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  binance
});
