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

// ../ap/packages/pieces/community/mempool-space/src/index.ts
var index_exports = {};
__export(index_exports, {
  mempoolSpace: () => mempoolSpace
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework35 = require("@activepieces/pieces-framework"), import_pieces_common35 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/general/get-difficulty-adjustment.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/mempool-space/src/lib/common/index.ts
var MEMPOOL_API_BASE_URL = "https://mempool.space";

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/general/get-difficulty-adjustment.ts
var getDifficultyAdjustment = (0, import_pieces_framework.createAction)({
  auth: import_pieces_framework.PieceAuth.None(),
  name: "get_difficulty_adjustment",
  displayName: "Get Difficulty Adjustment",
  description: "Returns details about Bitcoin difficulty adjustment",
  audience: "both",
  aiMetadata: { description: "Read the current Bitcoin mining difficulty-adjustment status: progress through the current 2016-block epoch, estimated next-retarget difficulty change, and time/blocks remaining. Pick this for network-wide difficulty/retarget questions, not for a specific block or transaction. Read-only and takes no input.", idempotent: !0 },
  // category: 'General',
  props: {},
  async run() {
    return (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/v1/difficulty-adjustment`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/general/get-price.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var getPrice = (0, import_pieces_framework2.createAction)({
  auth: import_pieces_framework2.PieceAuth.None(),
  name: "get_price",
  displayName: "Get Price",
  description: "Returns bitcoin latest price in main currencies",
  audience: "both",
  aiMetadata: { description: "Read the latest spot Bitcoin price across major fiat currencies (USD, EUR, GBP, etc.). Pick this for the current/most-recent price; use Get Historical Price instead when you need the price at a specific past date. Read-only and takes no input.", idempotent: !0 },
  // category: 'General',
  props: {},
  async run() {
    return (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/v1/prices`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/general/get-historical-price.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getHistoricalPrice = (0, import_pieces_framework3.createAction)({
  auth: import_pieces_framework3.PieceAuth.None(),
  name: "get_historical_price",
  displayName: "Get Historical Price",
  description: "Returns bitcoin historical price in main currencies",
  audience: "both",
  aiMetadata: { description: 'Look up the Bitcoin price for a single chosen currency at a specific past date/timestamp. Pick this for "what was BTC worth on date X" questions; use Get Price instead for the current spot price. Requires a currency and a lookup date, and is read-only.', idempotent: !0 },
  // category: 'General',
  props: {
    currency: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Currency",
      description: "Select the currency for historical price",
      required: !0,
      options: {
        options: [
          { label: "US Dollar", value: "USD" },
          { label: "Euro", value: "EUR" },
          { label: "British Pound", value: "GBP" },
          { label: "Canadian Dollar", value: "CAD" },
          { label: "Swiss Franc", value: "CHF" },
          { label: "Australian Dollar", value: "AUD" },
          { label: "Japanese Yen", value: "JPY" }
        ]
      }
    }),
    dateTime: import_pieces_framework3.Property.DateTime({
      displayName: "Price lookup date",
      description: "Provide YYYY-MM-DD format.",
      required: !0
    })
  },
  async run({ propsValue }) {
    let queryParams = {};
    if (propsValue.currency && (queryParams.currency = propsValue.currency), propsValue.dateTime) {
      let date = new Date(propsValue.dateTime);
      queryParams.timestamp = date.getTime().toString();
    }
    return (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/v1/historical-price`,
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/addresses/get-address-details.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getAddressDetails = (0, import_pieces_framework4.createAction)({
  auth: import_pieces_framework4.PieceAuth.None(),
  name: "get_address_details",
  displayName: "Get Address Details",
  description: "Returns address details including chain and mempool stats",
  audience: "both",
  aiMetadata: { description: "Returns aggregate statistics for a Bitcoin address: total funded/spent amounts and transaction counts split across confirmed chain and pending mempool. Read-only and idempotent. Use this to compute an address balance or activity summary in one call; use the transaction-list actions to enumerate individual transactions or Get Address UTXO for spendable outputs.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    address: import_pieces_framework4.Property.ShortText({
      displayName: "Address",
      description: "The Bitcoin address to look up",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/address/${propsValue.address}`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/addresses/get-address-transactions.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getAddressTransactions = (0, import_pieces_framework5.createAction)({
  auth: import_pieces_framework5.PieceAuth.None(),
  name: "get_address_transactions",
  displayName: "Get Address Transactions",
  description: "Returns transaction history (up to 50 mempool + 25 confirmed transactions)",
  audience: "both",
  aiMetadata: { description: "Returns the combined transaction history for a Bitcoin address (up to 50 unconfirmed mempool plus 25 confirmed transactions), with an optional after transaction ID to page through older entries. Read-only and idempotent. Use this for a single mixed view; choose the dedicated Chain or Mempool variants when you need only confirmed or only pending transactions.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    address: import_pieces_framework5.Property.ShortText({
      displayName: "Address",
      description: "The Bitcoin address to look up transactions for",
      required: !0
    }),
    after_txid: import_pieces_framework5.Property.ShortText({
      displayName: "After Transaction ID",
      description: "Optional: Get transactions after this transaction ID (for pagination)",
      required: !1
    })
  },
  async run({ propsValue }) {
    let queryParams = {};
    return propsValue.after_txid && (queryParams.after_txid = propsValue.after_txid), (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/address/${propsValue.address}/txs`,
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/addresses/get-address-transactions-chain.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getAddressTransactionsChain = (0, import_pieces_framework6.createAction)({
  auth: import_pieces_framework6.PieceAuth.None(),
  name: "get_address_transactions_chain",
  displayName: "Get Address Transactions (Chain)",
  description: "Returns confirmed transaction history (25 transactions per page)",
  audience: "both",
  aiMetadata: { description: "Returns only the confirmed (on-chain) transaction history for a Bitcoin address, 25 per page, newest first, with an optional last transaction ID to page through older entries. Read-only and idempotent. Choose this for settled history; use the Mempool variant for unconfirmed transactions, or Get Address Transactions for a combined view.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    address: import_pieces_framework6.Property.ShortText({
      displayName: "Address",
      description: "The Bitcoin address to look up transactions for",
      required: !0
    }),
    last_txid: import_pieces_framework6.Property.ShortText({
      displayName: "Last Transaction ID",
      description: "Optional: Last transaction ID for pagination",
      required: !1
    })
  },
  async run({ propsValue }) {
    let queryParams = {};
    return propsValue.last_txid && (queryParams.last_txid = propsValue.last_txid), (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/address/${propsValue.address}/txs/chain`,
      queryParams
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/addresses/get-address-transactions-mempool.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var getAddressTransactionsMempool = (0, import_pieces_framework7.createAction)({
  auth: import_pieces_framework7.PieceAuth.None(),
  name: "get_address_transactions_mempool",
  displayName: "Get Address Transactions (Mempool)",
  description: "Returns unconfirmed transactions (up to 50)",
  audience: "both",
  aiMetadata: { description: "Returns only the pending, unconfirmed mempool transactions for a Bitcoin address (up to 50). Read-only and idempotent, though entries disappear as they confirm or are dropped. Choose this to detect incoming/outgoing payments not yet mined; use the Chain variant for confirmed history.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    address: import_pieces_framework7.Property.ShortText({
      displayName: "Address",
      description: "The Bitcoin address to look up mempool transactions for",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/address/${propsValue.address}/txs/mempool`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/addresses/get-address-utxo.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var getAddressUtxo = (0, import_pieces_framework8.createAction)({
  auth: import_pieces_framework8.PieceAuth.None(),
  name: "get_address_utxo",
  displayName: "Get Address UTXO",
  description: "Returns unspent transaction outputs for an address",
  audience: "both",
  aiMetadata: { description: "Lists the unspent transaction outputs (UTXOs) currently held by a Bitcoin address, each with its txid, output index, value, and confirmation status. Read-only and idempotent, though the set changes as funds are spent or received. Use this to determine spendable coins or compute available balance for an address.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    address: import_pieces_framework8.Property.ShortText({
      displayName: "Address",
      description: "The Bitcoin address to look up UTXOs for",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/address/${propsValue.address}/utxo`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/addresses/validate-address.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var validateAddress = (0, import_pieces_framework9.createAction)({
  auth: import_pieces_framework9.PieceAuth.None(),
  name: "validate_address",
  displayName: "Validate Address",
  description: "Validates a Bitcoin address",
  audience: "both",
  aiMetadata: { description: "Checks whether a string is a valid Bitcoin address and reports its type and script details without touching chain history. Read-only and idempotent. Use this as a cheap pre-check before any address lookup; it confirms format/validity only, not whether the address has ever been used or holds funds.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    address: import_pieces_framework9.Property.ShortText({
      displayName: "Address",
      description: "The Bitcoin address to validate",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/v1/validate-address/${propsValue.address}`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/fees/get-mempool-blocks-fees.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var getMempoolBlocksFees = (0, import_pieces_framework10.createAction)({
  auth: import_pieces_framework10.PieceAuth.None(),
  name: "get_mempool_blocks_fees",
  displayName: "Get Mempool Blocks Fees",
  description: "Returns current mempool as projected blocks with fee rates and sizes",
  audience: "both",
  aiMetadata: { description: "Returns the current mempool modeled as a series of projected upcoming blocks, each with its fee-rate range, median fee, size, and transaction count. Takes no input and is read-only and idempotent, but reflects a live snapshot that shifts every few seconds. Use this for detailed fee-tier/congestion analysis; use Get Recommended Fees for simple confirmation-target fee picks.", idempotent: !0 },
  // category: 'Fees',
  props: {},
  async run() {
    return (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/v1/fees/mempool-blocks`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/fees/get-recommended-fees.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var getRecommendedFees = (0, import_pieces_framework11.createAction)({
  auth: import_pieces_framework11.PieceAuth.None(),
  name: "get_recommended_fees",
  displayName: "Get Recommended Fees",
  description: "Returns recommended fee rates for different transaction confirmation targets",
  audience: "both",
  aiMetadata: { description: "Returns suggested Bitcoin fee rates (in sat/vB) for several confirmation targets such as fastest, half-hour, hour, economy, and minimum. Takes no input and is read-only and idempotent, but values track live mempool conditions and update continuously. Use this to pick a fee for a transaction; use Get Mempool Blocks Fees when you need the full per-block fee breakdown instead.", idempotent: !0 },
  // category: 'Fees',
  props: {},
  async run() {
    return (await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/v1/fees/recommended`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-block.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var getBlock = (0, import_pieces_framework12.createAction)({
  auth: import_pieces_framework12.PieceAuth.None(),
  name: "get_block",
  displayName: "Get Block",
  description: "Returns detailed block information",
  audience: "both",
  aiMetadata: { description: "Look up full metadata for one Bitcoin block by its block hash (height, timestamp, transaction count, size, weight, merkle root, etc.). Read-only and idempotent. Requires the exact 64-character block hash; if you only have a height, resolve the hash first. Use Get Block Status for confirmation depth, Get Block Header for the raw hex header, or Get Block Raw for the binary serialization.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    hash: import_pieces_framework12.Property.ShortText({
      displayName: "Block Hash",
      description: "The hash of the block to look up",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common12.httpClient.sendRequest({
      method: import_pieces_common12.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/block/${propsValue.hash}`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-block-header.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var getBlockHeader = (0, import_pieces_framework13.createAction)({
  auth: import_pieces_framework13.PieceAuth.None(),
  name: "get_block_header",
  displayName: "Get Block Header",
  description: "Returns the hex-encoded block header",
  audience: "both",
  aiMetadata: { description: "Returns the hex-encoded 80-byte block header for a given block hash (version, previous hash, merkle root, timestamp, bits, nonce). Read-only and idempotent. Pick this when you only need the header rather than the full block; use Get Block for parsed fields or Get Block Raw for the entire serialized block.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    hash: import_pieces_framework13.Property.ShortText({
      displayName: "Block Hash",
      description: "The hash of the block to look up",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common13.httpClient.sendRequest({
      method: import_pieces_common13.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/block/${propsValue.hash}/header`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-block-height.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var getBlockHeight = (0, import_pieces_framework14.createAction)({
  auth: import_pieces_framework14.PieceAuth.None(),
  name: "get_block_height",
  displayName: "Get Block Height",
  description: "Returns the hash of the block currently at specified height",
  audience: "both",
  aiMetadata: { description: "Resolve a block height to its block hash. Pick this to translate a numeric height into the hash other block actions need; use Get Block Timestamp to find a block by time, or Get Block Tip Hash for the latest block. Read-only.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    height: import_pieces_framework14.Property.Number({
      displayName: "Block Height",
      description: "The height of the block to look up",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common14.httpClient.sendRequest({
      method: import_pieces_common14.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/block-height/${propsValue.height}`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-block-timestamp.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var getBlockTimestamp = (0, import_pieces_framework15.createAction)({
  auth: import_pieces_framework15.PieceAuth.None(),
  name: "get_block_timestamp",
  displayName: "Get Block Timestamp",
  description: "Returns the height and hash of the block closest to the given timestamp",
  audience: "both",
  aiMetadata: { description: "Find the block mined closest to a given Unix timestamp, returning its height and hash. Pick this to locate a block by time; use Get Block Height when you already know the numeric height. Read-only.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    timestamp: import_pieces_framework15.Property.Number({
      displayName: "Timestamp",
      description: "Unix timestamp to find the closest block to",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common15.httpClient.sendRequest({
      method: import_pieces_common15.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/v1/mining/blocks/timestamp/${propsValue.timestamp}`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-block-raw.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var getBlockRaw = (0, import_pieces_framework16.createAction)({
  auth: import_pieces_framework16.PieceAuth.None(),
  name: "get_block_raw",
  displayName: "Get Block Raw",
  description: "Returns the raw block representation in binary",
  audience: "both",
  aiMetadata: { description: "Fetches the full raw serialized block (binary representation) for a given block hash. Read-only and idempotent. Choose this only when you need the complete on-the-wire block bytes; for human-readable fields use Get Block, and for just the 80-byte header use Get Block Header.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    hash: import_pieces_framework16.Property.ShortText({
      displayName: "Block Hash",
      description: "The hash of the block to look up",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common16.httpClient.sendRequest({
      method: import_pieces_common16.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/block/${propsValue.hash}/raw`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-block-status.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var getBlockStatus = (0, import_pieces_framework17.createAction)({
  auth: import_pieces_framework17.PieceAuth.None(),
  name: "get_block_status",
  displayName: "Get Block Status",
  description: "Returns the confirmation status of a block",
  audience: "both",
  aiMetadata: { description: "Checks whether a block (by hash) is currently in the best chain and reports its confirmation state. Read-only and idempotent, but the result can change if the block is reorganized out of the chain. Use this to verify a block is still confirmed; use Get Block for full block details.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    hash: import_pieces_framework17.Property.ShortText({
      displayName: "Block Hash",
      description: "The hash of the block to check status",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common17.httpClient.sendRequest({
      method: import_pieces_common17.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/block/${propsValue.hash}/status`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-block-tip-height.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common");
var getBlockTipHeight = (0, import_pieces_framework18.createAction)({
  auth: import_pieces_framework18.PieceAuth.None(),
  name: "get_block_tip_height",
  displayName: "Get Block Tip Height",
  description: "Returns the height of the last block",
  audience: "both",
  aiMetadata: { description: "Returns the current Bitcoin chain tip height (the height of the most recently mined block) as a single number. Takes no input and is read-only and idempotent, though the value advances roughly every ten minutes as new blocks arrive. Use this to learn the latest height or to compute confirmation counts.", idempotent: !0 },
  // category: 'Blocks',
  props: {},
  async run() {
    return (await import_pieces_common18.httpClient.sendRequest({
      method: import_pieces_common18.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/blocks/tip/height`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-block-tip-hash.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common");
var getBlockTipHash = (0, import_pieces_framework19.createAction)({
  auth: import_pieces_framework19.PieceAuth.None(),
  name: "get_block_tip_hash",
  displayName: "Get Block Tip Hash",
  description: "Returns the hash of the last block",
  audience: "both",
  aiMetadata: { description: 'Get the hash of the current chain-tip (latest) block. Pick this as the starting point for "most recent block" queries, then feed the hash into other block actions. Read-only and takes no input; the result changes as new blocks are mined.', idempotent: !0 },
  // category: 'Blocks',
  props: {},
  async run() {
    return (await import_pieces_common19.httpClient.sendRequest({
      method: import_pieces_common19.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/blocks/tip/hash`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-block-transaction-id.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_common20 = require("@activepieces/pieces-common");
var getBlockTransactionId = (0, import_pieces_framework20.createAction)({
  auth: import_pieces_framework20.PieceAuth.None(),
  name: "get_block_transaction_id",
  displayName: "Get Block Transaction ID",
  description: "Returns the transaction at specified index within the block",
  audience: "both",
  aiMetadata: { description: "Resolves the transaction ID (txid) at a specific positional index within a block, given the block hash and a zero-based index (index 0 is the coinbase transaction). Read-only and idempotent. Use this to map a block position to its txid; the index must be within the block transaction count or the lookup fails.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    hash: import_pieces_framework20.Property.ShortText({
      displayName: "Block Hash",
      description: "The hash of the block",
      required: !0
    }),
    index: import_pieces_framework20.Property.Number({
      displayName: "Transaction Index",
      description: "The index of the transaction within the block",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common20.httpClient.sendRequest({
      method: import_pieces_common20.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/block/${propsValue.hash}/txid/${propsValue.index}`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-block-transaction-ids.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common21 = require("@activepieces/pieces-common");
var getBlockTransactionIds = (0, import_pieces_framework21.createAction)({
  auth: import_pieces_framework21.PieceAuth.None(),
  name: "get_block_transaction_ids",
  displayName: "Get Block Transaction IDs",
  description: "Returns a list of all transaction IDs in the block",
  audience: "both",
  aiMetadata: { description: "List the transaction IDs of every transaction in a block, given the block hash. Pick this for the complete set of IDs only; use Get Block Transactions when you need full transaction objects (paginated, 25 at a time). Read-only.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    hash: import_pieces_framework21.Property.ShortText({
      displayName: "Block Hash",
      description: "The hash of the block",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common21.httpClient.sendRequest({
      method: import_pieces_common21.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/block/${propsValue.hash}/txids`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-block-transactions.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common22 = require("@activepieces/pieces-common");
var getBlockTransactions = (0, import_pieces_framework22.createAction)({
  auth: import_pieces_framework22.PieceAuth.None(),
  name: "get_block_transactions",
  displayName: "Get Block Transactions",
  description: "Returns a list of transactions in the block (up to 25 transactions)",
  audience: "both",
  aiMetadata: { description: "List full transaction objects within a block by block hash, returning up to 25 per call with an optional start index for pagination. Pick this for transaction detail; use Get Block Transaction IDs when you only need the IDs. Read-only.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    hash: import_pieces_framework22.Property.ShortText({
      displayName: "Block Hash",
      description: "The hash of the block",
      required: !0
    }),
    startIndex: import_pieces_framework22.Property.Number({
      displayName: "Start Index",
      description: "Optional: Starting index for pagination",
      required: !1
    })
  },
  async run({ propsValue }) {
    let url = `${MEMPOOL_API_BASE_URL}/api/block/${propsValue.hash}/txs`;
    return propsValue.startIndex !== void 0 && (url += `/${propsValue.startIndex}`), (await import_pieces_common22.httpClient.sendRequest({
      method: import_pieces_common22.HttpMethod.GET,
      url
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/blocks/get-blocks-bulk.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_common23 = require("@activepieces/pieces-common");
var getBlocksBulk = (0, import_pieces_framework23.createAction)({
  auth: import_pieces_framework23.PieceAuth.None(),
  name: "get_blocks_bulk",
  displayName: "Get Blocks (Bulk)",
  description: "Returns details on the range of blocks between minHeight and maxHeight, inclusive, up to 10 blocks",
  audience: "both",
  aiMetadata: { description: "Retrieve details for a contiguous range of blocks by height (minimum required, maximum optional and defaulting to the current tip), capped at 10 blocks per call. Pick this for surveying several consecutive blocks at once rather than fetching them one by one. Read-only.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    minHeight: import_pieces_framework23.Property.Number({
      displayName: "Minimum Height",
      description: "The starting block height",
      required: !0
    }),
    maxHeight: import_pieces_framework23.Property.Number({
      displayName: "Maximum Height",
      description: "Optional: The ending block height. If not specified, defaults to the current tip",
      required: !1
    })
  },
  async run({ propsValue }) {
    let url = propsValue.maxHeight !== void 0 ? `${MEMPOOL_API_BASE_URL}/api/v1/blocks-bulk/${propsValue.minHeight}/${propsValue.maxHeight}` : `${MEMPOOL_API_BASE_URL}/api/v1/blocks-bulk/${propsValue.minHeight}`;
    return (await import_pieces_common23.httpClient.sendRequest({
      method: import_pieces_common23.HttpMethod.GET,
      url
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/transactions/get-transaction.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework"), import_pieces_common24 = require("@activepieces/pieces-common");
var getTransaction = (0, import_pieces_framework24.createAction)({
  auth: import_pieces_framework24.PieceAuth.None(),
  name: "get_transaction",
  displayName: "Get Transaction",
  description: "Retrieve transaction details by transaction ID",
  audience: "both",
  aiMetadata: { description: "Retrieve full parsed details of a Bitcoin transaction by its transaction ID: inputs, outputs, fee, size, and confirmation status. Pick this as the general-purpose transaction lookup; use narrower actions (status, times, hex, outspends) when you only need one facet. Read-only.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    txid: import_pieces_framework24.Property.ShortText({
      displayName: "Transaction ID",
      description: "The transaction ID to retrieve",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common24.httpClient.sendRequest({
      method: import_pieces_common24.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/tx/${propsValue.txid}`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/transactions/get-transaction-hex.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework"), import_pieces_common25 = require("@activepieces/pieces-common");
var getTransactionHex = (0, import_pieces_framework25.createAction)({
  auth: import_pieces_framework25.PieceAuth.None(),
  name: "get_transaction_hex",
  displayName: "Get Transaction Hex",
  description: "Get the raw transaction in hex format",
  audience: "both",
  aiMetadata: { description: "Fetch the hex-encoded serialized form of a transaction by its transaction ID from the /hex endpoint. Functionally equivalent to Get Raw Transaction; use Get Transaction instead for parsed/structured detail. Read-only.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    txid: import_pieces_framework25.Property.ShortText({
      displayName: "Transaction ID",
      description: "The transaction ID to get hex data for",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common25.httpClient.sendRequest({
      method: import_pieces_common25.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/tx/${propsValue.txid}/hex`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/transactions/get-transaction-merkleblock-proof.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework"), import_pieces_common26 = require("@activepieces/pieces-common");
var getTransactionMerkleblockProof = (0, import_pieces_framework26.createAction)({
  auth: import_pieces_framework26.PieceAuth.None(),
  name: "get_transaction_merkleblock_proof",
  displayName: "Get Transaction Merkleblock Proof",
  description: "Get the merkle block proof for a transaction",
  audience: "both",
  aiMetadata: { description: "Get the merkleblock-encoded inclusion proof for a confirmed transaction by its ID. Pick this for the Bitcoin merkleblock hex format; use Get Transaction Merkle Proof for the structured merkle-path form. Read-only.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    txid: import_pieces_framework26.Property.ShortText({
      displayName: "Transaction ID",
      description: "The transaction ID to get merkle block proof for",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common26.httpClient.sendRequest({
      method: import_pieces_common26.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/tx/${propsValue.txid}/merkleblock-proof`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/transactions/get-transaction-merkle-proof.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework"), import_pieces_common27 = require("@activepieces/pieces-common");
var getTransactionMerkleProof = (0, import_pieces_framework27.createAction)({
  auth: import_pieces_framework27.PieceAuth.None(),
  name: "get_transaction_merkle_proof",
  displayName: "Get Transaction Merkle Proof",
  description: "Get the merkle proof for a transaction",
  audience: "both",
  aiMetadata: { description: "Get the Merkle inclusion proof for a confirmed transaction (block height, merkle path, position) to verify it belongs to its block. Pick this for the SPV-style merkle path; use Get Transaction Merkleblock Proof for the encoded merkleblock format. Read-only.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    txid: import_pieces_framework27.Property.ShortText({
      displayName: "Transaction ID",
      description: "The transaction ID to get merkle proof for",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common27.httpClient.sendRequest({
      method: import_pieces_common27.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/tx/${propsValue.txid}/merkle-proof`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/transactions/get-transaction-outspend.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework"), import_pieces_common28 = require("@activepieces/pieces-common");
var getTransactionOutspend = (0, import_pieces_framework28.createAction)({
  auth: import_pieces_framework28.PieceAuth.None(),
  name: "get_transaction_outspend",
  displayName: "Get Transaction Output Spend Status",
  description: "Get the spending status of a specific transaction output",
  audience: "both",
  aiMetadata: { description: "Check whether one specific output (by transaction ID plus 0-based output index/vout) has been spent. Pick this when you care about a single output; use Get All Transaction Output Spend Statuses to check every output of a transaction at once. Read-only.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    txid: import_pieces_framework28.Property.ShortText({
      displayName: "Transaction ID",
      description: "The transaction ID to check output spending status",
      required: !0
    }),
    vout: import_pieces_framework28.Property.Number({
      displayName: "Output Index",
      description: "The index of the output to check (0-based)",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common28.httpClient.sendRequest({
      method: import_pieces_common28.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/tx/${propsValue.txid}/outspend/${propsValue.vout}`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/transactions/get-transaction-outspends.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework"), import_pieces_common29 = require("@activepieces/pieces-common");
var getTransactionOutspends = (0, import_pieces_framework29.createAction)({
  auth: import_pieces_framework29.PieceAuth.None(),
  name: "get_transaction_outspends",
  displayName: "Get All Transaction Output Spend Statuses",
  description: "Get the spending status of all outputs in a transaction",
  audience: "both",
  aiMetadata: { description: "Check the spent/unspent status of every output of a transaction at once, by its transaction ID. Pick this to survey all outputs; use Get Transaction Output Spend Status to check a single output by index. Read-only.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    txid: import_pieces_framework29.Property.ShortText({
      displayName: "Transaction ID",
      description: "The transaction ID to check all output spending statuses",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common29.httpClient.sendRequest({
      method: import_pieces_common29.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/tx/${propsValue.txid}/outspends`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/transactions/get-transaction-raw.ts
var import_pieces_framework30 = require("@activepieces/pieces-framework"), import_pieces_common30 = require("@activepieces/pieces-common");
var getTransactionRaw = (0, import_pieces_framework30.createAction)({
  auth: import_pieces_framework30.PieceAuth.None(),
  name: "get_transaction_raw",
  displayName: "Get Raw Transaction",
  description: "Get the raw transaction in hex format",
  audience: "both",
  aiMetadata: { description: "Fetch the raw serialized bytes of a transaction by its transaction ID from the /raw endpoint. Functionally equivalent to Get Transaction Hex; pick this for the binary raw form, and use Get Transaction for the parsed/structured detail. Read-only.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    txid: import_pieces_framework30.Property.ShortText({
      displayName: "Transaction ID",
      description: "The transaction ID to get raw data for",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common30.httpClient.sendRequest({
      method: import_pieces_common30.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/tx/${propsValue.txid}/raw`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/transactions/get-transaction-rbf-timeline.ts
var import_pieces_framework31 = require("@activepieces/pieces-framework"), import_pieces_common31 = require("@activepieces/pieces-common");
var getTransactionRbfTimeline = (0, import_pieces_framework31.createAction)({
  auth: import_pieces_framework31.PieceAuth.None(),
  name: "get_transaction_rbf_timeline",
  displayName: "Get Transaction RBF Timeline",
  description: "Get the Replace-By-Fee (RBF) timeline for a transaction",
  audience: "both",
  aiMetadata: { description: "Retrieve the Replace-By-Fee (RBF) history for a transaction ID, showing the chain of fee-bumping replacements over time. Pick this when investigating whether/how a transaction was replaced or fee-bumped. Read-only.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    txid: import_pieces_framework31.Property.ShortText({
      displayName: "Transaction ID",
      description: "The transaction ID to get RBF timeline for",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common31.httpClient.sendRequest({
      method: import_pieces_common31.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/tx/${propsValue.txid}/rbf-timeline`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/transactions/get-transaction-status.ts
var import_pieces_framework32 = require("@activepieces/pieces-framework"), import_pieces_common32 = require("@activepieces/pieces-common");
var getTransactionStatus = (0, import_pieces_framework32.createAction)({
  auth: import_pieces_framework32.PieceAuth.None(),
  name: "get_transaction_status",
  displayName: "Get Transaction Status",
  description: "Get the confirmation status of a transaction",
  audience: "both",
  aiMetadata: { description: "Check whether a transaction is confirmed and, if so, in which block (height, hash, time) by its transaction ID. Pick this for a quick confirmed/unconfirmed answer; use Get Transaction for the full transaction detail. Read-only.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    txid: import_pieces_framework32.Property.ShortText({
      displayName: "Transaction ID",
      description: "The transaction ID to check status for",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common32.httpClient.sendRequest({
      method: import_pieces_common32.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/tx/${propsValue.txid}/status`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/transactions/get-transaction-times.ts
var import_pieces_framework33 = require("@activepieces/pieces-framework"), import_pieces_common33 = require("@activepieces/pieces-common");
var getTransactionTimes = (0, import_pieces_framework33.createAction)({
  auth: import_pieces_framework33.PieceAuth.None(),
  name: "get_transaction_times",
  displayName: "Get Transaction Times",
  description: "Get timing information for a transaction including first seen and block entry times",
  audience: "both",
  aiMetadata: { description: "Get timing information for a transaction by its ID, such as when it was first seen in the mempool. Pick this for propagation/first-seen timing rather than confirmation state (use Get Transaction Status) or full detail (use Get Transaction). Read-only.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    txid: import_pieces_framework33.Property.ShortText({
      displayName: "Transaction ID",
      description: "The transaction ID to get timing information for",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common33.httpClient.sendRequest({
      method: import_pieces_common33.HttpMethod.GET,
      url: `${MEMPOOL_API_BASE_URL}/api/tx/${propsValue.txid}/times`
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/lib/actions/transactions/post-transaction.ts
var import_pieces_framework34 = require("@activepieces/pieces-framework"), import_pieces_common34 = require("@activepieces/pieces-common");
var postTransaction = (0, import_pieces_framework34.createAction)({
  auth: import_pieces_framework34.PieceAuth.None(),
  name: "post_transaction",
  displayName: "Post Transaction",
  description: "Submit a raw transaction to the network",
  audience: "both",
  aiMetadata: { description: "Broadcast a signed raw Bitcoin transaction (hex) to the network for inclusion in the mempool. This is the only write action here and is not idempotent: re-submitting may error or have real on-chain effects, so call it once with a fully signed transaction. Returns the resulting transaction ID.", idempotent: !1 },
  // category: 'Transactions',
  props: {
    rawTx: import_pieces_framework34.Property.LongText({
      displayName: "Raw Transaction",
      description: "The raw transaction in hex format to broadcast",
      required: !0
    })
  },
  async run({ propsValue }) {
    return (await import_pieces_common34.httpClient.sendRequest({
      method: import_pieces_common34.HttpMethod.POST,
      url: `${MEMPOOL_API_BASE_URL}/api/tx`,
      body: propsValue.rawTx
    })).body;
  }
});

// ../ap/packages/pieces/community/mempool-space/src/index.ts
var mempoolSpace = (0, import_pieces_framework35.createPiece)({
  displayName: "Mempool",
  description: "The mempool.space website invented the concept of visualizing a Bitcoin node's mempool as projected blocks.",
  logoUrl: "https://cdn.activepieces.com/pieces/mempool-space.png",
  minimumSupportedRelease: "0.20.0",
  authors: ["reemayoush"],
  auth: import_pieces_framework35.PieceAuth.None(),
  actions: [
    // General Actions
    getDifficultyAdjustment,
    getPrice,
    getHistoricalPrice,
    // Address Actions
    getAddressDetails,
    getAddressTransactions,
    getAddressTransactionsChain,
    getAddressTransactionsMempool,
    getAddressUtxo,
    validateAddress,
    // Fees Actions
    getMempoolBlocksFees,
    getRecommendedFees,
    // Block Actions
    getBlock,
    getBlockHeader,
    getBlockHeight,
    getBlockTimestamp,
    getBlockRaw,
    getBlockStatus,
    getBlockTipHeight,
    getBlockTipHash,
    getBlockTransactionId,
    getBlockTransactionIds,
    getBlockTransactions,
    getBlocksBulk,
    // Transaction Actions
    getTransaction,
    getTransactionHex,
    getTransactionMerkleblockProof,
    getTransactionMerkleProof,
    getTransactionOutspend,
    getTransactionOutspends,
    getTransactionRaw,
    getTransactionRbfTimeline,
    getTransactionStatus,
    getTransactionTimes,
    postTransaction,
    (0, import_pieces_common35.createCustomApiCallAction)({
      baseUrl: () => "https://mempool.space/api"
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mempoolSpace
});
