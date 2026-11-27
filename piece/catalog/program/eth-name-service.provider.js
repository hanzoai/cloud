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

// ../ap/packages/pieces/community/eth-name-service/src/index.ts
var index_exports = {};
__export(index_exports, {
  ethNameService: () => ethNameService
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/eth-name-service/src/lib/actions/list-ens-domains.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/eth-name-service/src/lib/common/common.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), ensCommon = {
  auth: import_pieces_framework.PieceAuth.SecretText({
    displayName: "API Key",
    description: "*Get your api Key: https://thegraph.com/studio/apikeys",
    required: !0
  }),
  apiCall: async function(url, method, data = void 0, token = void 0) {
    let headers = {
      "Content-Type": "application/json"
    };
    return token && (headers.Authorization = `Bearer ${token}`), (await fetch(url, {
      method,
      headers,
      body: data ? JSON.stringify(data) : void 0
    })).json();
  }
};

// ../ap/packages/pieces/community/eth-name-service/src/lib/actions/list-ens-domains.ts
var listEnsDomains = (0, import_pieces_framework2.createAction)({
  name: "listEnsDomains",
  auth: ensCommon.auth,
  displayName: "List ENS Domains",
  description: "List the ENS domains for a given address.",
  audience: "both",
  aiMetadata: { description: "Look up all Ethereum Name Service (ENS) domains owned by a given Ethereum address by querying the ENS subgraph on The Graph. Use to discover which ENS names an address controls, along with their subdomains, resolver, and metadata. Requires the wallet address; case is normalized automatically. Read-only and idempotent.", idempotent: !0 },
  props: {
    address: import_pieces_framework2.Property.ShortText({
      displayName: "Address",
      description: "Address to list ENS domains for",
      required: !0
    })
  },
  async run(context) {
    let { address } = context.propsValue;
    if (!address)
      throw new Error("Address is required");
    let query = `
    {
      domains(where: { owner: "${address.toLowerCase()}" }) {
          id
          name
          labelName
          labelhash
          parent {
            id
          }
          subdomains {
            id
            name
          }
          subdomainCount
          resolvedAddress {
            id
          }
          resolver {
            id
            address
          }
          ttl
          isMigrated
          createdAt
        }
    }
    `, res = await ensCommon.apiCall(
      `https://gateway.thegraph.com/api/${context.auth.secret_text}/subgraphs/id/5XqPmWe6gjyrJtFn9cLy237i4cWw2j9HcUJEXsP5qGtH`,
      "POST",
      { query }
    );
    if (res.errors)
      throw new Error(res.errors[0].message);
    return res.data.domains;
  }
});

// ../ap/packages/pieces/community/eth-name-service/src/index.ts
var ethNameService = (0, import_pieces_framework3.createPiece)({
  displayName: "Ethereum Name Service (ENS)",
  description: "Ethereum Name Service (ENS) is a decentralized naming system on the Ethereum blockchain.",
  auth: ensCommon.auth,
  minimumSupportedRelease: "0.20.0",
  logoUrl: "https://cdn.activepieces.com/pieces/eth-name-service.png",
  authors: ["reemayoush"],
  actions: [listEnsDomains],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  ethNameService
});
