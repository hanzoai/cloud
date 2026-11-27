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

// ../ap/packages/pieces/community/chain-aware/src/index.ts
var index_exports = {};
__export(index_exports, {
  chainAware: () => chainAware
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chain-aware/src/lib/actions/audit-wallet-address.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/chain-aware/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), chainAwareAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Enter your ChainAware API key."
});

// ../ap/packages/pieces/community/chain-aware/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://enterprise.api.chainaware.ai";
async function makeRequest(request) {
  return (await import_pieces_common.httpClient.sendRequest({
    method: request.method,
    url: `${BASE_URL}${request.path}`,
    headers: {
      "x-api-key": request.apiKey,
      "Content-Type": "application/json"
    },
    body: request.body
  })).body;
}

// ../ap/packages/pieces/community/chain-aware/src/lib/actions/audit-wallet-address.ts
var auditWalletAddress = (0, import_pieces_framework2.createAction)({
  auth: chainAwareAuth,
  name: "auditWalletAddress",
  classification: "READ",
  displayName: "Audit Wallet Address",
  description: "Audit a wallet address for fraud risk",
  audience: "both",
  aiMetadata: {
    description: "Run a ChainAware fraud-risk audit on a single on-chain wallet address for a given network (e.g. ethereum, bsc). Choose this for a broad risk overview of a wallet; use Fraud Check when you specifically need a fraud-probability score. Requires the network name and wallet address. Read-only analysis \u2014 idempotent.",
    idempotent: !0
  },
  props: {
    network: import_pieces_framework2.Property.ShortText({
      displayName: "Network",
      required: !0
    }),
    walletAddress: import_pieces_framework2.Property.ShortText({
      displayName: "Wallet Address",
      required: !0
    })
  },
  async run(context) {
    return makeRequest({
      apiKey: context.auth.secret_text,
      method: import_pieces_common2.HttpMethod.POST,
      path: "/fraud/audit",
      body: {
        network: context.propsValue.network,
        walletAddress: context.propsValue.walletAddress
      }
    });
  }
});

// ../ap/packages/pieces/community/chain-aware/src/lib/actions/credit-score.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var creditScore = (0, import_pieces_framework3.createAction)({
  auth: chainAwareAuth,
  name: "creditScore",
  classification: "READ",
  displayName: "Credit Score",
  description: "Get a user's credit score",
  audience: "both",
  aiMetadata: {
    description: "Look up the ChainAware on-chain credit score for a wallet address on a given network. Choose this to assess a wallet's creditworthiness or borrowing history rather than fraud risk. Requires the network name and wallet address. Read-only lookup \u2014 idempotent.",
    idempotent: !0
  },
  props: {
    network: import_pieces_framework3.Property.ShortText({
      displayName: "Network",
      required: !0
    }),
    walletAddress: import_pieces_framework3.Property.ShortText({
      displayName: "Wallet Address",
      required: !0
    })
  },
  async run(context) {
    return makeRequest({
      apiKey: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/users/credit-score",
      body: {
        network: context.propsValue.network,
        walletAddress: context.propsValue.walletAddress
      }
    });
  }
});

// ../ap/packages/pieces/community/chain-aware/src/lib/actions/fraud-check.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var fraudCheck = (0, import_pieces_framework4.createAction)({
  auth: chainAwareAuth,
  name: "fraudCheck",
  classification: "READ",
  displayName: "Fraud Check",
  description: "Calculate fraud probability for a wallet address",
  audience: "both",
  aiMetadata: {
    description: 'Calculate a ChainAware fraud-probability score for a wallet address on a given network. Choose this when you need a quantified fraud likelihood for a wallet; the optional "Only Fraud" flag narrows the result to fraud-related signals only. Requires the network name and wallet address. Read-only analysis \u2014 idempotent.',
    idempotent: !0
  },
  props: {
    network: import_pieces_framework4.Property.ShortText({
      displayName: "Network",
      required: !0
    }),
    walletAddress: import_pieces_framework4.Property.ShortText({
      displayName: "Wallet Address",
      required: !0
    }),
    onlyFraud: import_pieces_framework4.Property.Checkbox({
      displayName: "Only Fraud",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a;
    return makeRequest({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/fraud/check",
      body: {
        network: context.propsValue.network,
        walletAddress: context.propsValue.walletAddress,
        onlyFraud: (_a = context.propsValue.onlyFraud) != null ? _a : !1
      }
    });
  }
});

// ../ap/packages/pieces/community/chain-aware/src/lib/actions/rug-pull-check.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var rugPullCheck = (0, import_pieces_framework5.createAction)({
  auth: chainAwareAuth,
  name: "rugPullCheck",
  classification: "READ",
  displayName: "Rug Pull Check",
  description: "Calculate fraud probability for a contract address",
  audience: "both",
  aiMetadata: {
    description: "Calculate a ChainAware rug-pull / fraud-probability score for a smart-contract address (e.g. a token contract) on a given network. Choose this when assessing a token or contract rather than a user wallet \u2014 for wallets use Fraud Check instead. Requires the network name and contract address. Read-only analysis \u2014 idempotent.",
    idempotent: !0
  },
  props: {
    network: import_pieces_framework5.Property.ShortText({
      displayName: "Network",
      required: !0
    }),
    contractAddress: import_pieces_framework5.Property.ShortText({
      displayName: "Contract Address",
      required: !0
    })
  },
  async run(context) {
    return makeRequest({
      apiKey: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/rug/pull-check",
      body: {
        network: context.propsValue.network,
        contractAddress: context.propsValue.contractAddress
      }
    });
  }
});

// ../ap/packages/pieces/community/chain-aware/src/lib/actions/wallet-segment.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var walletSegment = (0, import_pieces_framework6.createAction)({
  auth: chainAwareAuth,
  name: "walletSegment",
  classification: "READ",
  displayName: "Wallet Segment",
  description: "Get wallet behaviour information",
  audience: "both",
  aiMetadata: {
    description: "Retrieve ChainAware behavioural segmentation for a wallet address on a given network \u2014 how the wallet is categorized based on its on-chain activity. Choose this to profile or classify a wallet rather than score its fraud risk. Requires the network name and wallet address. Read-only lookup \u2014 idempotent.",
    idempotent: !0
  },
  props: {
    network: import_pieces_framework6.Property.ShortText({
      displayName: "Network",
      required: !0
    }),
    walletAddress: import_pieces_framework6.Property.ShortText({
      displayName: "Wallet Address",
      required: !0
    })
  },
  async run(context) {
    return makeRequest({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      path: "/segmentation/wallet-segment",
      body: {
        walletAddress: context.propsValue.walletAddress,
        network: context.propsValue.network
      }
    });
  }
});

// ../ap/packages/pieces/community/chain-aware/src/index.ts
var chainAware = (0, import_pieces_framework7.createPiece)({
  displayName: "ChainAware.AI",
  description: "Detect risky wallet behavior",
  auth: chainAwareAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/chain-aware.png",
  authors: ["onyedikachi-david"],
  categories: [import_pieces_framework8.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  actions: [
    auditWalletAddress,
    creditScore,
    fraudCheck,
    rugPullCheck,
    walletSegment,
    (0, import_pieces_common7.createCustomApiCallAction)({
      auth: chainAwareAuth,
      baseUrl: () => BASE_URL,
      authMapping: async (auth) => ({
        "x-api-key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  chainAware
});
