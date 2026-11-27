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

// ../ap/packages/pieces/community/lusha/src/index.ts
var index_exports = {};
__export(index_exports, {
  lusha: () => lusha,
  lushaAuth: () => lushaAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/lusha/src/lib/actions/companies/search.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var searchCompanies = (0, import_pieces_framework.createAction)({
  name: "search_companies",
  auth: lushaAuth,
  displayName: "Search Companies",
  description: "Search for companies with filters and pagination",
  audience: "both",
  aiMetadata: { description: "Search the Lusha prospecting database for companies matching a JSON filter (industry, size, location, contact countries, etc.), auto-paginating up to a result cap (max 10000). Use to discover company candidates before enriching them; the returned requestId and company IDs feed the Enrich Companies action. Read-only and idempotent for the same filter.", idempotent: !0 },
  props: {
    resultLimit: import_pieces_framework.Property.Number({
      displayName: "Maximum Results",
      description: "Maximum number of companies to return. The maximum is 10000 results.",
      required: !0,
      defaultValue: 1e4
    }),
    requestBody: import_pieces_framework.Property.Json({
      displayName: "Request Body",
      description: "The JSON body for the search request. Do not include pagination (pages), it will be added automatically.",
      required: !0,
      defaultValue: {
        filters: {
          contacts: {
            include: {
              countries: ["ES"]
            }
          },
          companies: {
            include: {
              sizes: [
                {
                  max: 50,
                  min: 11
                }
              ],
              locations: [
                {
                  country: "Spain"
                }
              ],
              mainIndustriesIds: [17, 14]
            }
          }
        }
      }
    })
  },
  async run(context) {
    let { resultLimit, requestBody } = context.propsValue;
    if (resultLimit > 1e4)
      throw new Error("The maximum of result is 10000");
    let allResults = [], currentPage = 0, hasMorePages = !0;
    for (; hasMorePages && allResults.length < resultLimit; ) {
      let payload = __spreadProps(__spreadValues({}, requestBody), {
        pages: {
          page: currentPage,
          size: 40
          // the maximum for the Lusha API
        }
      }), response = await fetch("https://api.lusha.com/prospecting/company/search", {
        method: "POST",
        headers: {
          "x-app": "activepieces",
          "x-api-key": context.auth.secret_text,
          "Content-Type": "application/json",
          Accept: "application/json"
        },
        body: JSON.stringify(payload)
      });
      if (!response.ok) {
        let errorText = await response.text();
        throw new Error(`HTTP error! status: ${response.status}, body: ${errorText}`);
      }
      let responseData = await response.json();
      if (!responseData.data || responseData.data.length === 0)
        hasMorePages = !1;
      else {
        let remainingNeeded = resultLimit - allResults.length;
        remainingNeeded < 40 ? allResults = allResults.concat(responseData.data.slice(0, remainingNeeded)) : allResults = allResults.concat(responseData.data);
      }
      currentPage++;
    }
    return allResults;
  }
});

// ../ap/packages/pieces/community/lusha/src/lib/actions/companies/enrich.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");
var enrichCompanies = (0, import_pieces_framework2.createAction)({
  name: "enrich_companies",
  auth: lushaAuth,
  displayName: "Enrich Companies",
  description: "Enrich companies details using requestId and company IDs from search results",
  audience: "both",
  aiMetadata: { description: "Fetch full company profiles from Lusha for the companies returned by a prior Search Companies call, using that search step's requestId and the company IDs in its data. Use after Search Companies when you need detailed company records rather than just match candidates; it requires the raw search-results object and cannot enrich arbitrary company IDs. Read-only lookup and idempotent.", idempotent: !0 },
  props: {
    searchResults: import_pieces_framework2.Property.Json({
      displayName: "Search Results",
      description: "The results from the Search Companies step",
      required: !0
    })
  },
  async run(context) {
    let searchResults = context.propsValue.searchResults, requestId = searchResults.requestId, companyIds = searchResults.data.map((item) => item.id), payload = {
      requestId,
      companiesIds: companyIds
    }, response = await fetch("https://api.lusha.com/prospecting/company/enrich", {
      method: "POST",
      headers: {
        "x-app": "activepieces",
        "x-api-key": context.auth.secret_text,
        "Content-Type": "application/json",
        Accept: "application/json"
      },
      body: JSON.stringify(payload)
    });
    if (!response.ok) {
      let errorText = await response.text();
      throw new Error(`HTTP error! status: ${response.status}, body: ${errorText}`);
    }
    return await response.json();
  }
});

// ../ap/packages/pieces/community/lusha/src/index.ts
var lushaAuth = import_pieces_framework3.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Please enter the API Key obtained from Lusha."
}), lusha = (0, import_pieces_framework3.createPiece)({
  displayName: "Lusha",
  auth: lushaAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/lusha.png",
  authors: ["Kevinyu-alan"],
  actions: [
    searchCompanies,
    enrichCompanies,
    (0, import_pieces_common.createCustomApiCallAction)({
      baseUrl: () => "https://api.lusha.com",
      auth: lushaAuth,
      authMapping: async (auth) => ({
        "x-app": "activepieces",
        api_key: auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  lusha,
  lushaAuth
});
