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

// ../ap/packages/pieces/community/blockscout/src/index.ts
var index_exports = {};
__export(index_exports, {
  blockscout: () => blockscout
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework36 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/blockscout/src/lib/actions/search/search.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), search = (0, import_pieces_framework.createAction)({
  name: "search",
  classification: "SEARCH",
  displayName: "Search",
  description: "Search for addresses, transactions, blocks, or tokens",
  audience: "both",
  aiMetadata: { description: "Run a free-text search across the chain for addresses, transactions, blocks, and tokens, returning ranked matches. Pick this when you have a name, symbol, or partial identifier and need to resolve it to an entity; use the dedicated by-hash lookups when you already hold an exact hash. Read-only lookup on eth.blockscout.com.", idempotent: !0 },
  // category: 'Search',
  props: {
    query: import_pieces_framework.Property.ShortText({
      displayName: "Search Query",
      description: "Search query for addresses, transactions, blocks, or tokens",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: "https://eth.blockscout.com/api/v2/search",
      queryParams: {
        q: context.propsValue.query
      }
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/search-redirect/check-redirect.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), checkRedirect = (0, import_pieces_framework2.createAction)({
  name: "check_redirect",
  classification: "READ",
  displayName: "Check Search Redirect",
  description: "Check if a search query should redirect to a specific resource",
  audience: "both",
  aiMetadata: { description: "Check whether a search query maps unambiguously to a single resource (address, transaction, block, or token) and should redirect straight to it. Pick this to disambiguate an exact identifier into one entity rather than browsing ranked results from Search. Read-only lookup on eth.blockscout.com.", idempotent: !0 },
  // category: 'Search',
  props: {
    query: import_pieces_framework2.Property.ShortText({
      displayName: "Search Query",
      description: "Search query to check for redirect",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.GET,
      url: "https://eth.blockscout.com/api/v2/search/check-redirect",
      queryParams: {
        q: context.propsValue.query
      }
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/blocks/blocks.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common"), getBlocks = (0, import_pieces_framework3.createAction)({
  name: "get_blocks",
  classification: "SEARCH",
  displayName: "Get Blocks",
  description: "Get list of blocks",
  audience: "both",
  aiMetadata: { description: "List recent Ethereum blocks across the chain (newest first), takes no inputs. Read-only. Use this for chain-wide block browsing; to fetch one specific block use Get Block by Hash or Number.", idempotent: !0 },
  // category: 'Blocks',
  props: {},
  async run(context) {
    let response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: "https://eth.blockscout.com/api/v2/blocks"
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/blocks/main-page-blocks.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common"), getMainPageBlocks = (0, import_pieces_framework4.createAction)({
  name: "get_main_page_blocks",
  classification: "SEARCH",
  displayName: "Get Main Page Blocks",
  description: "Get blocks for main page display",
  audience: "both",
  aiMetadata: { description: "Get the short list of latest blocks shown on the Blockscout explorer home page, takes no inputs. Read-only. Use this for a quick at-a-glance feed of the newest blocks; for a fuller paginated block list use Get Blocks.", idempotent: !0 },
  // category: 'Blocks',
  props: {},
  async run(context) {
    let response = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: "https://eth.blockscout.com/api/v2/main-page/blocks"
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/blocks/block-by-hash.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common"), getBlockByHash = (0, import_pieces_framework5.createAction)({
  name: "get_block_by_hash",
  classification: "READ",
  displayName: "Get Block by Hash or Number",
  description: "Get block info by its hash or block number",
  audience: "both",
  aiMetadata: { description: "Look up a single Ethereum block by either its hash or its block number and return its details (miner, timestamp, gas, transaction count). Read-only; accepts both identifier forms. Use this when you know which block you want; to browse recent blocks without an identifier use Get Blocks.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    blockIdentifier: import_pieces_framework5.Property.ShortText({
      displayName: "Block Hash or Number",
      description: "Block hash or block number to fetch",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/blocks/${context.propsValue.blockIdentifier}`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/blocks/block-transactions.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common"), getBlockTransactions = (0, import_pieces_framework6.createAction)({
  name: "get_block_transactions",
  classification: "SEARCH",
  displayName: "Get Block Transactions",
  description: "Get list of transactions for a specific block",
  audience: "both",
  aiMetadata: { description: "List the transactions contained in one Ethereum block, identified by its hash or number. Read-only. Use this to scope transactions to a single block; for chain-wide recent transactions use the Transactions Get Transactions action, and for that block's staking withdrawals use Get Block Withdrawals.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    blockIdentifier: import_pieces_framework6.Property.ShortText({
      displayName: "Block Hash or Number",
      description: "Block hash or block number to fetch transactions for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/blocks/${context.propsValue.blockIdentifier}/transactions`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/blocks/block-withdrawals.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common"), getBlockWithdrawals = (0, import_pieces_framework7.createAction)({
  name: "get_block_withdrawals",
  classification: "SEARCH",
  displayName: "Get Block Withdrawals",
  description: "Get list of withdrawals for a specific block",
  audience: "both",
  aiMetadata: { description: "List the validator (beacon-chain) staking withdrawals included in one Ethereum block, identified by its hash or number. Read-only. Use this for post-Merge withdrawal data of a specific block; for the regular transactions in that block use Get Block Transactions instead.", idempotent: !0 },
  // category: 'Blocks',
  props: {
    blockIdentifier: import_pieces_framework7.Property.ShortText({
      displayName: "Block Hash or Number",
      description: "Block hash or block number to fetch withdrawals for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/blocks/${context.propsValue.blockIdentifier}/withdrawals`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/transactions/transactions.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common"), getTransactions = (0, import_pieces_framework8.createAction)({
  name: "get_transactions",
  classification: "SEARCH",
  displayName: "Get Transactions",
  description: "Get list of transactions",
  audience: "both",
  aiMetadata: { description: "List recent Ethereum transactions across the whole chain (validated mainnet transactions, newest first). Read-only and takes no inputs. Use this for chain-wide browsing; to fetch one known transaction use Get Transaction by Hash, and to scope to a single block use Get Block Transactions.", idempotent: !0 },
  // category: 'Transactions',
  props: {},
  async run(context) {
    let response = await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.GET,
      url: "https://eth.blockscout.com/api/v2/transactions"
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/transactions/main-page-transactions.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common"), getMainPageTransactions = (0, import_pieces_framework9.createAction)({
  name: "get_main_page_transactions",
  classification: "SEARCH",
  displayName: "Get Main Page Transactions",
  description: "Get transactions for main page display",
  audience: "both",
  aiMetadata: { description: "Get the short list of latest Ethereum transactions shown on the Blockscout explorer home page. Read-only and takes no inputs. Use this for a quick at-a-glance feed of the most recent activity; for a fuller paginated transaction list use Get Transactions instead.", idempotent: !0 },
  // category: 'Transactions',
  props: {},
  async run(context) {
    let response = await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: "https://eth.blockscout.com/api/v2/main-page/transactions"
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/transactions/transaction-by-hash.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common"), getTransactionByHash = (0, import_pieces_framework10.createAction)({
  name: "get_transaction_by_hash",
  classification: "READ",
  displayName: "Get Transaction by Hash",
  description: "Get transaction details by its hash",
  audience: "both",
  aiMetadata: { description: "Look up a single Ethereum transaction by its full hash and return its core details (status, value, gas, from/to, block). Read-only. Use this when you have a specific transaction hash; for the related transfers, logs, internal calls, or state changes of that same transaction use the dedicated per-transaction actions instead.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    transactionHash: import_pieces_framework10.Property.ShortText({
      displayName: "Transaction Hash",
      description: "Hash of the transaction to fetch details for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/transactions/${context.propsValue.transactionHash}`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/transactions/transaction-token-transfers.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common"), getTransactionTokenTransfers = (0, import_pieces_framework11.createAction)({
  name: "get_transaction_token_transfers",
  classification: "SEARCH",
  displayName: "Get Transaction Token Transfers",
  description: "Get list of token transfers in a transaction",
  audience: "both",
  aiMetadata: { description: "List the ERC-20/721/1155 token transfers that occurred within one Ethereum transaction, identified by its hash. Read-only. Use this when you have a transaction hash and want the tokens it moved; for token transfers across a token contract over time use the Tokens Get Token Transfers action instead.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    transactionHash: import_pieces_framework11.Property.ShortText({
      displayName: "Transaction Hash",
      description: "Hash of the transaction to fetch token transfers for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/transactions/${context.propsValue.transactionHash}/token-transfers`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/transactions/transaction-internal-transactions.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common"), getTransactionInternalTransactions = (0, import_pieces_framework12.createAction)({
  name: "get_transaction_internal_transactions",
  classification: "SEARCH",
  displayName: "Get Transaction Internal Transactions",
  description: "Get list of internal transactions in a transaction",
  audience: "both",
  aiMetadata: { description: "List the internal transactions (value-transferring calls made by contracts, not separate top-level transactions) triggered within one Ethereum transaction, identified by its hash. Read-only. Use this to trace contract-to-contract ETH movement inside a single transaction; for ERC token movements in that transaction use Get Transaction Token Transfers instead.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    transactionHash: import_pieces_framework12.Property.ShortText({
      displayName: "Transaction Hash",
      description: "Hash of the transaction to fetch internal transactions for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common12.httpClient.sendRequest({
      method: import_pieces_common12.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/transactions/${context.propsValue.transactionHash}/internal-transactions`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/transactions/transaction-logs.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common"), getTransactionLogs = (0, import_pieces_framework13.createAction)({
  name: "get_transaction_logs",
  classification: "SEARCH",
  displayName: "Get Transaction Logs",
  description: "Get list of logs generated by a transaction",
  audience: "both",
  aiMetadata: { description: "List the event logs emitted by one Ethereum transaction, identified by its hash (decoded contract events with topics and data). Read-only. Use this to inspect what events a transaction fired; for the resulting account/storage changes use Get Transaction State Changes instead.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    transactionHash: import_pieces_framework13.Property.ShortText({
      displayName: "Transaction Hash",
      description: "Hash of the transaction to fetch logs for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common13.httpClient.sendRequest({
      method: import_pieces_common13.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/transactions/${context.propsValue.transactionHash}/logs`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/transactions/transaction-raw-trace.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common"), getTransactionRawTrace = (0, import_pieces_framework14.createAction)({
  name: "get_transaction_raw_trace",
  classification: "READ",
  displayName: "Get Transaction Raw Trace",
  description: "Get raw trace data for a transaction",
  audience: "both",
  aiMetadata: { description: "Get the low-level raw execution trace (opcode-level call frames) for one Ethereum transaction by hash. Read-only and intended for deep debugging of contract execution. Prefer the higher-level Get Transaction Internal Transactions or Get Transaction by Hash actions for ordinary lookups; reach for this only when you need the full unprocessed trace.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    transactionHash: import_pieces_framework14.Property.ShortText({
      displayName: "Transaction Hash",
      description: "Hash of the transaction to fetch raw trace for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common14.httpClient.sendRequest({
      method: import_pieces_common14.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/transactions/${context.propsValue.transactionHash}/raw-trace`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/transactions/transaction-state-changes.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common"), getTransactionStateChanges = (0, import_pieces_framework15.createAction)({
  name: "get_transaction_state_changes",
  classification: "SEARCH",
  displayName: "Get Transaction State Changes",
  description: "Get list of state changes in a transaction",
  audience: "both",
  aiMetadata: { description: "List the account balance and storage state changes (before/after values) caused by one Ethereum transaction, identified by its hash. Read-only. Use this to see how a transaction altered account or contract state; for the events it emitted use Get Transaction Logs instead.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    transactionHash: import_pieces_framework15.Property.ShortText({
      displayName: "Transaction Hash",
      description: "Hash of the transaction to fetch state changes for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common15.httpClient.sendRequest({
      method: import_pieces_common15.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/transactions/${context.propsValue.transactionHash}/state-changes`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/transactions/transaction-summary.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common"), getTransactionSummary = (0, import_pieces_framework16.createAction)({
  name: "get_transaction_summary",
  classification: "READ",
  displayName: "Get Transaction Summary",
  description: "Get a human-readable summary of a transaction",
  audience: "both",
  aiMetadata: { description: "Get Blockscout's interpreted, human-readable summary of one Ethereum transaction by hash (e.g. a plain-language description of what the transaction did, such as a swap or transfer). Read-only. Use this for a quick natural-language explanation; for the structured raw fields use Get Transaction by Hash instead.", idempotent: !0 },
  // category: 'Transactions',
  props: {
    transactionHash: import_pieces_framework16.Property.ShortText({
      displayName: "Transaction Hash",
      description: "Hash of the transaction to fetch summary for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common16.httpClient.sendRequest({
      method: import_pieces_common16.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/transactions/${context.propsValue.transactionHash}/summary`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/addresses.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common"), getAddresses = (0, import_pieces_framework17.createAction)({
  name: "get_addresses",
  classification: "SEARCH",
  displayName: "Get Addresses",
  description: "Get list of native coin holders",
  audience: "both",
  aiMetadata: { description: "List the top native-coin (ETH) holding addresses on the chain, ordered by balance. Pick this to discover or rank wealthy accounts; it takes no input and does not target a specific address (use Get Address by Hash for a single account). Read-only lookup on eth.blockscout.com.", idempotent: !0 },
  // category: 'Addresses',
  props: {},
  async run(context) {
    let response = await import_pieces_common17.httpClient.sendRequest({
      method: import_pieces_common17.HttpMethod.GET,
      url: "https://eth.blockscout.com/api/v2/addresses"
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-by-hash.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common"), getAddressByHash = (0, import_pieces_framework18.createAction)({
  name: "get_address_by_hash",
  classification: "READ",
  displayName: "Get Address by Hash",
  description: "Get address info by its hash",
  audience: "both",
  aiMetadata: { description: "Look up the core profile of a single address: current balance, contract/EOA status, name tags, and implementation details. Pick this as the first lookup for one known address before drilling into its transactions, tokens, or logs. Read-only lookup on eth.blockscout.com; requires a 0x address hash.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework18.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch info for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common18.httpClient.sendRequest({
      method: import_pieces_common18.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-counters.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common"), getAddressCounters = (0, import_pieces_framework19.createAction)({
  name: "get_address_counters",
  classification: "READ",
  displayName: "Get Address Counters",
  description: "Get counters (transactions count, token transfers count, etc.) for an address",
  audience: "both",
  aiMetadata: { description: "Fetch aggregate activity counts for an Ethereum address (total transactions, token transfers, gas usage). Pick this for a quick summary of how active an address is rather than retrieving the underlying records. Read-only lookup on eth.blockscout.com; requires a 0x address hash.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework19.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch counters for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common19.httpClient.sendRequest({
      method: import_pieces_common19.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}/counters`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-transactions.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_common20 = require("@activepieces/pieces-common"), getAddressTransactions = (0, import_pieces_framework20.createAction)({
  name: "get_address_transactions",
  classification: "SEARCH",
  displayName: "Get Address Transactions",
  description: "Get list of transactions for an address",
  audience: "both",
  aiMetadata: { description: "List the top-level transactions in which an address was the sender or receiver. Pick this for an account\u2019s normal transaction history; use Get Address Internal Transactions for contract-internal value moves or Get Address Token Transfers for token movements. Read-only lookup on eth.blockscout.com; requires a 0x address hash.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework20.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch transactions for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common20.httpClient.sendRequest({
      method: import_pieces_common20.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}/transactions`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-token-transfers.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common21 = require("@activepieces/pieces-common"), getAddressTokenTransfers = (0, import_pieces_framework21.createAction)({
  name: "get_address_token_transfers",
  classification: "SEARCH",
  displayName: "Get Address Token Transfers",
  description: "Get list of token transfers for an address",
  audience: "both",
  aiMetadata: { description: "List the token transfer events (ERC-20/721/1155) in which an address sent or received tokens. Pick this for token movement history; use Get Address Token Balances for current holdings or Get Address Transactions for native-coin transfers. Read-only lookup on eth.blockscout.com; requires a 0x address hash.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework21.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch token transfers for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common21.httpClient.sendRequest({
      method: import_pieces_common21.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}/token-transfers`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-internal-transactions.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common22 = require("@activepieces/pieces-common"), getAddressInternalTransactions = (0, import_pieces_framework22.createAction)({
  name: "get_address_internal_transactions",
  classification: "SEARCH",
  displayName: "Get Address Internal Transactions",
  description: "Get list of internal transactions for an address",
  audience: "both",
  aiMetadata: { description: "List the internal (contract-to-contract) transactions touching an Ethereum address. Choose this for value movements triggered within smart-contract calls rather than the top-level transactions an account submitted (use Get Address Transactions for those). Read-only lookup on eth.blockscout.com; requires a 0x address hash.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework22.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch internal transactions for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common22.httpClient.sendRequest({
      method: import_pieces_common22.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}/internal-transactions`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-logs.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_common23 = require("@activepieces/pieces-common"), getAddressLogs = (0, import_pieces_framework23.createAction)({
  name: "get_address_logs",
  classification: "SEARCH",
  displayName: "Get Address Logs",
  description: "Get list of logs generated by an address",
  audience: "both",
  aiMetadata: { description: "List the event logs emitted by a contract address (decoded where an ABI is known). Pick this to read smart-contract events such as Transfer or Approval for a given contract rather than its transactions or token balances. Read-only lookup on eth.blockscout.com; requires a 0x address hash.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework23.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch logs for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common23.httpClient.sendRequest({
      method: import_pieces_common23.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}/logs`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-blocks-validated.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework"), import_pieces_common24 = require("@activepieces/pieces-common"), getAddressBlocksValidated = (0, import_pieces_framework24.createAction)({
  name: "get_address_blocks_validated",
  classification: "SEARCH",
  displayName: "Get Address Blocks Validated",
  description: "Get list of blocks validated by an address",
  audience: "both",
  aiMetadata: { description: "List the blocks an address validated or produced (relevant for validator/miner addresses). Choose this to inspect an account\u2019s block-production history rather than its transactions or balances. Read-only lookup on eth.blockscout.com; requires a 0x address hash and returns empty for non-validator addresses.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework24.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch validated blocks for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common24.httpClient.sendRequest({
      method: import_pieces_common24.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}/blocks-validated`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-token-balances.ts
var import_pieces_framework25 = require("@activepieces/pieces-framework"), import_pieces_common25 = require("@activepieces/pieces-common"), getAddressTokenBalances = (0, import_pieces_framework25.createAction)({
  name: "get_address_token_balances",
  classification: "SEARCH",
  displayName: "Get Address Token Balances",
  description: "Get list of token balances for an address",
  audience: "both",
  aiMetadata: { description: "Get the full current token holdings of an address as a flat list of balances. Choose this for a snapshot of what tokens an account holds and how much; use Get Address Token Transfers for movement history or Get Address Tokens for the paginated/filterable variant. Read-only lookup on eth.blockscout.com; requires a 0x address hash.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework25.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch token balances for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common25.httpClient.sendRequest({
      method: import_pieces_common25.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}/token-balances`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-tokens.ts
var import_pieces_framework26 = require("@activepieces/pieces-framework"), import_pieces_common26 = require("@activepieces/pieces-common"), getAddressTokens = (0, import_pieces_framework26.createAction)({
  name: "get_address_tokens",
  classification: "SEARCH",
  displayName: "Get Address Tokens",
  description: "Get list of tokens owned by an address with filtering options",
  audience: "both",
  aiMetadata: { description: "List the tokens owned by an address, the paginated/filterable view of its holdings (e.g. by token type). Choose this when iterating through many holdings; use Get Address Token Balances for a simple full snapshot. Read-only lookup on eth.blockscout.com; requires a 0x address hash.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework26.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch tokens for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common26.httpClient.sendRequest({
      method: import_pieces_common26.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}/tokens`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-withdrawals.ts
var import_pieces_framework27 = require("@activepieces/pieces-framework"), import_pieces_common27 = require("@activepieces/pieces-common"), getAddressWithdrawals = (0, import_pieces_framework27.createAction)({
  name: "get_address_withdrawals",
  classification: "SEARCH",
  displayName: "Get Address Withdrawals",
  description: "Get list of withdrawals for an address",
  audience: "both",
  aiMetadata: { description: "List beacon-chain (staking) withdrawals credited to an address. Choose this to inspect validator-staking payout history for an account; returns empty for addresses that received no withdrawals. Read-only lookup on eth.blockscout.com; requires a 0x address hash.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework27.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch withdrawals for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common27.httpClient.sendRequest({
      method: import_pieces_common27.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}/withdrawals`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-coin-balance-history.ts
var import_pieces_framework28 = require("@activepieces/pieces-framework"), import_pieces_common28 = require("@activepieces/pieces-common"), getAddressCoinBalanceHistory = (0, import_pieces_framework28.createAction)({
  name: "get_address_coin_balance_history",
  classification: "SEARCH",
  displayName: "Get Address Coin Balance History",
  description: "Get list of coin balance changes for an address",
  audience: "both",
  aiMetadata: { description: "List per-event native-coin (ETH) balance changes for an address, one entry per balance-altering block. Pick this for fine-grained balance history; use Get Address Coin Balance History By Day for a daily-aggregated series instead. Read-only lookup on eth.blockscout.com; requires a 0x address hash.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework28.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch coin balance history for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common28.httpClient.sendRequest({
      method: import_pieces_common28.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}/coin-balance-history`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/addresses/address-coin-balance-history-by-day.ts
var import_pieces_framework29 = require("@activepieces/pieces-framework"), import_pieces_common29 = require("@activepieces/pieces-common"), getAddressCoinBalanceHistoryByDay = (0, import_pieces_framework29.createAction)({
  name: "get_address_coin_balance_history_by_day",
  classification: "SEARCH",
  displayName: "Get Address Coin Balance History By Day",
  description: "Get list of coin balance changes for an address grouped by day",
  audience: "both",
  aiMetadata: { description: "Get an address\u2019s native-coin (ETH) balance aggregated into one value per day, suited for charting or trend analysis. Pick this over Get Address Coin Balance History when you want a daily time series rather than every individual balance-changing event. Read-only lookup on eth.blockscout.com; requires a 0x address hash.", idempotent: !0 },
  // category: 'Addresses',
  props: {
    addressHash: import_pieces_framework29.Property.ShortText({
      displayName: "Address Hash",
      description: "Hash of the address to fetch daily coin balance history for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common29.httpClient.sendRequest({
      method: import_pieces_common29.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/addresses/${context.propsValue.addressHash}/coin-balance-history-by-day`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/tokens/tokens.ts
var import_pieces_framework30 = require("@activepieces/pieces-framework"), import_pieces_common30 = require("@activepieces/pieces-common"), getTokens = (0, import_pieces_framework30.createAction)({
  name: "get_tokens",
  classification: "SEARCH",
  displayName: "Get Tokens",
  description: "Get list of tokens",
  audience: "both",
  aiMetadata: { description: "List tokens tracked on the chain (ERC-20/721/1155), takes no inputs. Read-only. Use this to browse or discover tokens when you do not have a specific contract address; once you have an address use Get Token by Address for its details.", idempotent: !0 },
  // category: 'Tokens',
  props: {},
  async run(context) {
    let response = await import_pieces_common30.httpClient.sendRequest({
      method: import_pieces_common30.HttpMethod.GET,
      url: "https://eth.blockscout.com/api/v2/tokens"
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/tokens/token-by-address.ts
var import_pieces_framework31 = require("@activepieces/pieces-framework"), import_pieces_common31 = require("@activepieces/pieces-common"), getTokenByAddress = (0, import_pieces_framework31.createAction)({
  name: "get_token_by_address",
  classification: "READ",
  displayName: "Get Token by Address",
  description: "Get token info by its contract address",
  audience: "both",
  aiMetadata: { description: "Look up the metadata for a single token by its contract address (name, symbol, decimals, type, total supply). Read-only. Use this when you have a specific token contract address; to discover tokens without an address use Get Tokens, and for holder or transfer detail use the dedicated token actions.", idempotent: !0 },
  // category: 'Tokens',
  props: {
    addressHash: import_pieces_framework31.Property.ShortText({
      displayName: "Token Address",
      description: "Contract address of the token to fetch info for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common31.httpClient.sendRequest({
      method: import_pieces_common31.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/tokens/${context.propsValue.addressHash}`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/tokens/token-transfers.ts
var import_pieces_framework32 = require("@activepieces/pieces-framework"), import_pieces_common32 = require("@activepieces/pieces-common"), getTokenTransfers = (0, import_pieces_framework32.createAction)({
  name: "get_token_transfers",
  classification: "SEARCH",
  displayName: "Get Token Transfers",
  description: "Get list of token transfers",
  audience: "both",
  aiMetadata: { description: "List recent transfers of one token, identified by its contract address, across all holders (newest first). Read-only. Use this to track movement of a specific token over time; for the token transfers inside a single transaction use the Transactions Get Transaction Token Transfers action instead.", idempotent: !0 },
  // category: 'Tokens',
  props: {
    addressHash: import_pieces_framework32.Property.ShortText({
      displayName: "Token Address",
      description: "Contract address of the token to fetch transfers for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common32.httpClient.sendRequest({
      method: import_pieces_common32.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/tokens/${context.propsValue.addressHash}/transfers`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/tokens/token-holders.ts
var import_pieces_framework33 = require("@activepieces/pieces-framework"), import_pieces_common33 = require("@activepieces/pieces-common"), getTokenHolders = (0, import_pieces_framework33.createAction)({
  name: "get_token_holders",
  classification: "SEARCH",
  displayName: "Get Token Holders",
  description: "Get list of token holders",
  audience: "both",
  aiMetadata: { description: "List the addresses holding one token, identified by its contract address, with their balances (largest holders first). Read-only. Use this to see who owns a token; for just the total holder count use Get Token Counters, and for NFT item IDs use Get Token Instances.", idempotent: !0 },
  // category: 'Tokens',
  props: {
    addressHash: import_pieces_framework33.Property.ShortText({
      displayName: "Token Address",
      description: "Contract address of the token to fetch holders for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common33.httpClient.sendRequest({
      method: import_pieces_common33.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/tokens/${context.propsValue.addressHash}/holders`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/tokens/token-counters.ts
var import_pieces_framework34 = require("@activepieces/pieces-framework"), import_pieces_common34 = require("@activepieces/pieces-common"), getTokenCounters = (0, import_pieces_framework34.createAction)({
  name: "get_token_counters",
  classification: "READ",
  displayName: "Get Token Counters",
  description: "Get token counters (holders count, transfers count, etc.)",
  audience: "both",
  aiMetadata: { description: "Get aggregate counts for one token by its contract address, such as total number of holders and total transfers. Read-only. Use this when you just need the summary numbers; to enumerate the actual holders or transfers use Get Token Holders or Get Token Transfers instead.", idempotent: !0 },
  // category: 'Tokens',
  props: {
    addressHash: import_pieces_framework34.Property.ShortText({
      displayName: "Token Address",
      description: "Contract address of the token to fetch counters for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common34.httpClient.sendRequest({
      method: import_pieces_common34.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/tokens/${context.propsValue.addressHash}/counters`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/lib/actions/tokens/token-instances.ts
var import_pieces_framework35 = require("@activepieces/pieces-framework"), import_pieces_common35 = require("@activepieces/pieces-common"), getTokenInstances = (0, import_pieces_framework35.createAction)({
  name: "get_token_instances",
  classification: "SEARCH",
  displayName: "Get Token Instances",
  description: "Get list of token instances (NFTs)",
  audience: "both",
  aiMetadata: { description: "List the individual NFT instances (token IDs) minted under one NFT contract, identified by its address. Read-only and only meaningful for ERC-721/1155 collections. Use this to enumerate items in a collection; for fungible token holders use Get Token Holders and for general token info use Get Token by Address.", idempotent: !0 },
  // category: 'Tokens',
  props: {
    addressHash: import_pieces_framework35.Property.ShortText({
      displayName: "Token Address",
      description: "Contract address of the token to fetch instances for",
      required: !0
    })
  },
  async run(context) {
    let response = await import_pieces_common35.httpClient.sendRequest({
      method: import_pieces_common35.HttpMethod.GET,
      url: `https://eth.blockscout.com/api/v2/tokens/${context.propsValue.addressHash}/instances`
    });
    if (response.status !== 200)
      throw new Error(`Blockscout API request failed with status ${response.status}`);
    return response.body;
  }
});

// ../ap/packages/pieces/community/blockscout/src/index.ts
var blockscout = (0, import_pieces_framework36.createPiece)({
  displayName: "Blockscout",
  description: "Blockscout is a tool for inspecting and analyzing EVM chains.",
  auth: import_pieces_framework36.PieceAuth.None(),
  logoUrl: "https://cdn.activepieces.com/pieces/blockscout.png",
  authors: ["reemayoush"],
  actions: [
    search,
    checkRedirect,
    getBlocks,
    getMainPageBlocks,
    getBlockByHash,
    getBlockTransactions,
    getBlockWithdrawals,
    getTransactions,
    getMainPageTransactions,
    getTransactionByHash,
    getTransactionTokenTransfers,
    getTransactionInternalTransactions,
    getTransactionLogs,
    getTransactionRawTrace,
    getTransactionStateChanges,
    getTransactionSummary,
    getAddresses,
    getAddressByHash,
    getAddressCounters,
    getAddressTransactions,
    getAddressTokenTransfers,
    getAddressLogs,
    getAddressBlocksValidated,
    getAddressTokenBalances,
    getAddressTokens,
    getAddressWithdrawals,
    getAddressCoinBalanceHistory,
    getAddressCoinBalanceHistoryByDay,
    getTokens,
    getTokenByAddress,
    getTokenTransfers,
    getTokenHolders,
    getTokenCounters,
    getTokenInstances
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  blockscout
});
