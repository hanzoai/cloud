"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/sardis/src/index.ts
var index_exports = {};
__export(index_exports, {
  sardis: () => sardis
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sardis/src/lib/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/sardis/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), BASE_URL = "https://api.sardis.sh";
async function sardisApiCall(apiKey, method, path, body, query) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      "X-API-Key": apiKey,
      "Content-Type": "application/json"
    },
    body,
    queryParams: query
  })).body;
}
var sardisCommon = {
  walletId: import_pieces_framework.Property.ShortText({
    displayName: "Wallet ID",
    description: "Your Sardis wallet ID (starts with wal_)",
    required: !0
  }),
  token: import_pieces_framework.Property.StaticDropdown({
    displayName: "Token",
    description: "Stablecoin to use for the transaction",
    required: !1,
    defaultValue: "USDC",
    options: {
      options: [
        { label: "USDC", value: "USDC" },
        { label: "USDT", value: "USDT" },
        { label: "PYUSD", value: "PYUSD" },
        { label: "EURC", value: "EURC" }
      ]
    }
  }),
  chain: import_pieces_framework.Property.StaticDropdown({
    displayName: "Chain",
    description: "Blockchain network",
    required: !1,
    defaultValue: "base",
    options: {
      options: [
        { label: "Base", value: "base" },
        { label: "Ethereum", value: "ethereum" },
        { label: "Polygon", value: "polygon" },
        { label: "Arbitrum", value: "arbitrum" },
        { label: "Optimism", value: "optimism" }
      ]
    }
  })
};

// ../ap/packages/pieces/community/sardis/src/lib/auth.ts
var sardisAuth = import_pieces_framework2.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your Sardis API key:
1. Log in to your [Sardis Dashboard](https://sardis.sh/dashboard).
2. Navigate to **Settings** -> **API Keys**.
3. Copy your API key (starts with \`sk_\`).`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await sardisApiCall(auth, import_pieces_common2.HttpMethod.GET, "/api/v2/wallets"), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key"
      };
    }
  }
});

// ../ap/packages/pieces/community/sardis/src/lib/actions/send-payment.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var sendPaymentAction = (0, import_pieces_framework3.createAction)({
  name: "send_payment",
  auth: sardisAuth,
  displayName: "Send Payment",
  description: "Execute a policy-controlled payment from a Sardis wallet.",
  audience: "both",
  aiMetadata: {
    description: "Sends a stablecoin payment from a Sardis wallet to a recipient address or merchant identifier, subject to the wallet's spending policies. Choose this to actually move funds; to test a payment against policy without moving money, use Check Policy instead. Not idempotent \u2014 each call submits a new transfer, so do not retry blindly on an unclear failure. Requires the source wallet ID, recipient, and amount; token defaults to USDC and chain to Base.",
    idempotent: !1
  },
  props: {
    walletId: sardisCommon.walletId,
    to: import_pieces_framework3.Property.ShortText({
      displayName: "Recipient",
      description: 'Recipient address or merchant identifier (e.g. "0xabc...", "openai.com")',
      required: !0
    }),
    amount: import_pieces_framework3.Property.Number({
      displayName: "Amount",
      description: "Payment amount in token units (e.g. 25.00)",
      required: !0
    }),
    token: sardisCommon.token,
    chain: sardisCommon.chain,
    memo: import_pieces_framework3.Property.ShortText({
      displayName: "Memo",
      description: 'Reason for payment (e.g. "Monthly API subscription")',
      required: !1
    })
  },
  async run(context) {
    let { walletId, to, amount, token, chain, memo } = context.propsValue;
    return sardisApiCall(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      `/api/v2/wallets/${walletId}/transfer`,
      __spreadValues({
        destination: to,
        amount: amount.toString(),
        token: token != null ? token : "USDC",
        chain: chain != null ? chain : "base"
      }, memo && { memo })
    );
  }
});

// ../ap/packages/pieces/community/sardis/src/lib/actions/check-balance.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var checkBalanceAction = (0, import_pieces_framework4.createAction)({
  name: "check_balance",
  auth: sardisAuth,
  displayName: "Check Balance",
  description: "Check wallet balance and spending limits.",
  audience: "both",
  aiMetadata: {
    description: "Reads the current balance and remaining spending limits for a Sardis wallet on a given token and chain. Use it before sending a payment to confirm sufficient funds and policy headroom. Read-only and idempotent. Requires the wallet ID; token defaults to USDC and chain to Base.",
    idempotent: !0
  },
  props: {
    walletId: sardisCommon.walletId,
    token: sardisCommon.token,
    chain: sardisCommon.chain
  },
  async run(context) {
    let { walletId, token, chain } = context.propsValue;
    return sardisApiCall(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.GET,
      `/api/v2/wallets/${walletId}/balance`,
      void 0,
      { chain: chain != null ? chain : "base", token: token != null ? token : "USDC" }
    );
  }
});

// ../ap/packages/pieces/community/sardis/src/lib/actions/check-policy.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var checkPolicyAction = (0, import_pieces_framework5.createAction)({
  name: "check_policy",
  auth: sardisAuth,
  displayName: "Check Policy",
  description: "Simulate a transaction against spending policies without executing it.",
  audience: "both",
  aiMetadata: {
    description: "Dry-runs a proposed payment against a Sardis wallet's spending policies and reports whether it would be allowed, without moving any funds. Use it to validate an amount and recipient before calling Send Payment. Idempotent and side-effect-free \u2014 it only simulates. Requires the wallet ID, amount, and recipient/merchant; token defaults to USDC and chain to Base.",
    idempotent: !0
  },
  props: {
    walletId: sardisCommon.walletId,
    amount: import_pieces_framework5.Property.Number({
      displayName: "Amount",
      description: "Transaction amount to validate",
      required: !0
    }),
    merchant: import_pieces_framework5.Property.ShortText({
      displayName: "Merchant",
      description: "Recipient address or merchant identifier",
      required: !0
    }),
    token: sardisCommon.token,
    chain: sardisCommon.chain
  },
  async run(context) {
    let { walletId, amount, merchant, token, chain } = context.propsValue;
    return sardisApiCall(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      "/api/v2/simulate",
      {
        wallet_id: walletId,
        amount: amount.toString(),
        destination: merchant,
        token: token != null ? token : "USDC",
        chain: chain != null ? chain : "base"
      }
    );
  }
});

// ../ap/packages/pieces/community/sardis/src/lib/actions/set-policy.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var setSpendingPolicyAction = (0, import_pieces_framework6.createAction)({
  name: "set_policy",
  auth: sardisAuth,
  displayName: "Set Spending Policy",
  description: "Set or update spending policies on a Sardis agent using natural language.",
  audience: "both",
  aiMetadata: {
    description: "Sets or replaces the spending policy on a Sardis agent from a plain-English description of the rules (e.g. per-transaction caps, daily limits, blocked merchant categories). Use it to configure how an agent is allowed to spend before it sends payments. Idempotent in effect \u2014 re-applying the same policy text to the same agent yields the same resulting policy. Requires the agent ID and the natural-language policy text.",
    idempotent: !0
  },
  props: {
    agentId: import_pieces_framework6.Property.ShortText({
      displayName: "Agent ID",
      description: "Your Sardis agent ID",
      required: !0
    }),
    policyText: import_pieces_framework6.Property.LongText({
      displayName: "Policy (Natural Language)",
      description: 'Describe the spending rules in plain English. e.g. "Max $50 per transaction, daily limit $500, block gambling merchants"',
      required: !0
    })
  },
  async run(context) {
    let { agentId, policyText } = context.propsValue;
    return sardisApiCall(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.POST,
      "/api/v2/policies/apply",
      { natural_language: policyText, agent_id: agentId, confirm: !0 }
    );
  }
});

// ../ap/packages/pieces/community/sardis/src/lib/actions/list-transactions.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var listTransactionsAction = (0, import_pieces_framework7.createAction)({
  name: "list_transactions",
  auth: sardisAuth,
  displayName: "List Transactions",
  description: "Retrieve transaction history from a Sardis wallet ledger.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves recent ledger entries (transaction history) for a Sardis wallet. Use it to audit past payments or confirm whether a transfer was recorded. Read-only and idempotent. Requires the wallet ID; limit is optional and defaults to 50 (capped at 500).",
    idempotent: !0
  },
  props: {
    walletId: sardisCommon.walletId,
    limit: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      description: "Maximum number of transactions to return (default: 50)",
      required: !1,
      defaultValue: 50
    })
  },
  async run(context) {
    let { walletId, limit } = context.propsValue;
    return sardisApiCall(
      context.auth.secret_text,
      import_pieces_common7.HttpMethod.GET,
      "/api/v2/ledger/entries",
      void 0,
      {
        wallet_id: walletId,
        limit: String(Math.min(limit != null ? limit : 50, 500))
      }
    );
  }
});

// ../ap/packages/pieces/community/sardis/src/index.ts
var sardis = (0, import_pieces_framework8.createPiece)({
  displayName: "Sardis",
  description: "Policy-controlled payments for AI agents.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/sardis.png",
  authors: ["EfeDurmaz16", "onyedikachi-david"],
  categories: [import_pieces_framework9.PieceCategory.PAYMENT_PROCESSING],
  auth: sardisAuth,
  actions: [
    sendPaymentAction,
    checkBalanceAction,
    checkPolicyAction,
    setSpendingPolicyAction,
    listTransactionsAction,
    (0, import_pieces_common8.createCustomApiCallAction)({
      baseUrl: () => "https://api.sardis.sh/api/v2",
      auth: sardisAuth,
      authMapping: async (auth) => ({
        "X-API-Key": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  sardis
});
