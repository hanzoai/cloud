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

// ../ap/packages/pieces/community/truelayer/src/index.ts
var index_exports = {};
__export(index_exports, {
  paymentsApiV3Payments: () => paymentsApiV3Payments
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework40 = require("@activepieces/pieces-framework"), import_pieces_common39 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/truelayer/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), trueLayerCommon = {
  baseUrl: "https://api.truelayer.com",
  auth: import_pieces_framework.PieceAuth.OAuth2({
    description: "Authentication for TrueLayer API",
    authUrl: "https://auth.truelayer.com",
    tokenUrl: "https://auth.truelayer.com/connect/token",
    required: !0,
    scope: [
      "info",
      "accounts",
      "balance",
      "cards",
      "transactions",
      "direct_debits",
      "standing_orders",
      "offline_access",
      "signupplus",
      "verification"
    ]
  })
};

// ../ap/packages/pieces/community/truelayer/src/lib/action/payouts/create-payout.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var createPayout = (0, import_pieces_framework2.createAction)({
  auth: trueLayerCommon.auth,
  name: "create-payout",
  displayName: "Create payout",
  description: "Pay out from one of your merchant accounts. ",
  audience: "both",
  aiMetadata: { description: "Initiate a payout (transfer of funds) from one of your TrueLayer merchant accounts to a beneficiary. This moves money and is not idempotent on its own, so supply the required idempotency key so retries do not duplicate the payout. To check a payout afterward use Get Payout, and to authorize it use Start authorization flow.", idempotent: !1 },
  props: {
    IdempotencyKeyHeader: import_pieces_framework2.Property.ShortText({
      displayName: "Used to ensure idempotent requests",
      required: !0
    }),
    SignatureHeader: import_pieces_framework2.Property.ShortText({
      displayName: "Used for request signature verification",
      required: !0
    })
  },
  run: async (ctx) => (await await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payouts`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`,
      "Idempotency-Key": ctx.propsValue.IdempotencyKeyHeader,
      Signature: ctx.propsValue.SignatureHeader
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payouts/get-payout.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var getPayout = (0, import_pieces_framework3.createAction)({
  auth: trueLayerCommon.auth,
  name: "get-payout",
  displayName: "Get payout",
  description: "Returns payout details. ",
  audience: "both",
  aiMetadata: { description: "Retrieve the details and current status of a single payout by its ID. Use to check the outcome or state of a payout you previously created; read-only and safe to repeat.", idempotent: !0 },
  props: {
    id: import_pieces_framework3.Property.ShortText({
      displayName: "ID of the payout",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common2.httpClient.sendRequest({
    method: import_pieces_common2.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/payouts/${ctx.propsValue.id}`,
    headers: {
      Authorization: `${ctx.auth}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payouts/start-payout-authorization-flow.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var startPayoutAuthorizationFlow = (0, import_pieces_framework4.createAction)({
  auth: trueLayerCommon.auth,
  name: "start-payout-authorization-flow",
  displayName: "Start authorization flow",
  description: "Start the authorization flow for a payout. This API can be called using the `resource_token` associated with the payout you are trying to fetch.",
  audience: "both",
  aiMetadata: { description: "Begin the authorization flow for an existing payout identified by its ID, transitioning it toward execution. Use after creating a payout that requires authorization; this advances payout state and is not idempotent. To create the payout first, use Create Payout.", idempotent: !1 },
  props: {
    id: import_pieces_framework4.Property.ShortText({
      displayName: "ID of the payout",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common3.httpClient.sendRequest({
    method: import_pieces_common3.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payouts/${ctx.propsValue.id}/authorization-flow`,
    headers: {
      Authorization: `${ctx.auth}`
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/general/submit-payments-provider-return-parameters.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var submitPaymentsProviderReturnParameters = (0, import_pieces_framework5.createAction)({
  auth: trueLayerCommon.auth,
  name: "submit-payments-provider-return-parameters",
  displayName: "Submit payments return parameters",
  description: "Submit direct return query and fragment parameters returned from the provider.",
  audience: "both",
  aiMetadata: { description: "Forward the query and fragment parameters TrueLayer returned to your direct redirect URI back to TrueLayer to continue the payment/payout authorization flow. Use only when handling a provider redirect return; pass an idempotency key to make retries safe. Not a read or status-check operation.", idempotent: !1 },
  props: {
    IdempotencyKeyHeader: import_pieces_framework5.Property.ShortText({
      displayName: "Idempotency Key Header",
      description: "Used to ensure idempotent requests",
      required: !1
    })
  },
  run: async (ctx) => (await import_pieces_common4.httpClient.sendRequest({
    method: import_pieces_common4.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments-provider-return`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`,
      "Idempotency-Key": ctx.propsValue.IdempotencyKeyHeader
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/mandates/create-mandate.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var createMandate = (0, import_pieces_framework6.createAction)({
  auth: trueLayerCommon.auth,
  name: "create-mandate",
  displayName: "Create Mandate",
  description: "Create a new mandate. This API must be called using a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Create a new TrueLayer payment mandate (a recurring-payment authorization) using a backend bearer token. Use when setting up variable recurring payments before any individual payment is taken. This writes a new resource each call; supply the optional idempotency key to prevent duplicate mandates on retry.", idempotent: !1 },
  props: {
    IdempotencyKeyHeader: import_pieces_framework6.Property.ShortText({
      displayName: "Idempotency Key Header",
      description: "Used to ensure idempotent requests",
      required: !1
    }),
    SignatureHeader: import_pieces_framework6.Property.ShortText({
      displayName: "Signature Header",
      description: "Used for request signature verification",
      required: !1
    })
  },
  run: async (ctx) => (await import_pieces_common5.httpClient.sendRequest({
    method: import_pieces_common5.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/mandates`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`,
      "Idempotency-Key": ctx.propsValue.IdempotencyKeyHeader,
      Signature: ctx.propsValue.SignatureHeader
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/mandates/list-mandate.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var listMandate = (0, import_pieces_framework7.createAction)({
  auth: trueLayerCommon.auth,
  name: "list-mandate",
  displayName: "List Mandates",
  description: "List all the mandates associated with the client. This API must be called using a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "List the variable recurring payment (VRP) mandates belonging to your client, optionally filtered by user ID and paged via cursor. Use to discover mandate IDs or audit a user's mandates; read-only and safe to repeat.", idempotent: !0 },
  props: {
    user_id: import_pieces_framework7.Property.ShortText({
      displayName: "User ID",
      description: "Optional ID of the user whose mandates you want to list.",
      required: !1
    }),
    cursor: import_pieces_framework7.Property.ShortText({
      displayName: "Cursor",
      description: "Optional cursor for pagination.",
      required: !1
    }),
    limit: import_pieces_framework7.Property.ShortText({
      displayName: "Limit",
      description: "Optional limit on the number of mandates to return.",
      required: !1
    })
  },
  run: async (ctx) => (await import_pieces_common6.httpClient.sendRequest({
    method: import_pieces_common6.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/mandates`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    queryParams: {
      user_id: ctx.propsValue.user_id || "",
      cursor: ctx.propsValue.cursor || "",
      limit: ctx.propsValue.limit || ""
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/mandates/get-mandate.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var getMandate = (0, import_pieces_framework8.createAction)({
  auth: trueLayerCommon.auth,
  name: "get-mandate",
  displayName: "Get Mandate",
  description: "Returns a mandate with the stated ID. This endpoint can be called either by the regular `backend token` or the `mandate token` for that mandate.",
  audience: "both",
  aiMetadata: { description: "Retrieve a single TrueLayer mandate by its ID, including its current status and details. Use when you have a known mandate ID and need to inspect or poll its state. Read-only and safe to repeat; callable with either a backend bearer token or that mandate's mandate token.", idempotent: !0 },
  props: {
    id: import_pieces_framework8.Property.ShortText({
      displayName: "Mandate ID",
      description: "The ID of the mandate to retrieve.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common7.httpClient.sendRequest({
    method: import_pieces_common7.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/mandates/${ctx.propsValue.id}`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/mandates/start-mandate-authorization-flow.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var startMandateAuthorizationFlow = (0, import_pieces_framework9.createAction)({
  auth: trueLayerCommon.auth,
  name: "start-mandate-authorization-flow",
  displayName: "Start Authorization Flow",
  description: "Start the authorization flow for a mandate. This API can be called using either the mandate_token associated with the mandate or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Begin the authorization flow for an existing mandate identified by its ID so the user can authorize the recurring payment arrangement. Use after the mandate is created; this advances mandate state and is not idempotent. Follow with Submit consent if the flow requires it.", idempotent: !1 },
  props: {
    id: import_pieces_framework9.Property.ShortText({
      displayName: "Mandate ID",
      description: "The ID of the mandate to start the authorization flow for.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common8.httpClient.sendRequest({
    method: import_pieces_common8.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/mandates/${ctx.propsValue.id}/authorization-flow`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/mandates/submit-consent-mandate.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var submitConsentMandate = (0, import_pieces_framework10.createAction)({
  auth: trueLayerCommon.auth,
  name: "submit-consent-mandate",
  displayName: "Submit consent",
  description: "Submit the consent given by the user. This API can be called using either the mandate_token associated with the mandate or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Submit the user's consent action within a mandate authorization flow, identified by the mandate ID. Use after Start Authorization Flow when the flow requires a consent step; this advances mandate state and is not idempotent.", idempotent: !1 },
  props: {
    id: import_pieces_framework10.Property.ShortText({
      displayName: "Mandate ID",
      description: "The ID of the mandate for which consent is being submitted.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common9.httpClient.sendRequest({
    method: import_pieces_common9.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/mandates/${ctx.propsValue.id}/authorization-flow/actions/consent`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/mandates/submit-mandate-provider-selection.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var submitMandateProviderSelection = (0, import_pieces_framework11.createAction)({
  auth: trueLayerCommon.auth,
  name: "submit-mandate-provider-selection",
  displayName: "Submit provider selection",
  description: "Submit the provider details selected by the PSU. This API can be called using either the mandate_token associated with the mandate or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Advance a mandate's authorization flow by submitting the bank provider the user selected. Use during an interactive mandate setup when the flow requires a provider-selection step. This mutates the flow state and is not safe to repeat; it is the mandate counterpart to the payment provider-selection action.", idempotent: !1 },
  props: {
    id: import_pieces_framework11.Property.ShortText({
      displayName: "Mandate ID",
      description: "The ID of the mandate for which provider selection is being submitted.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common10.httpClient.sendRequest({
    method: import_pieces_common10.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/mandates/${ctx.propsValue.id}/authorization-flow/actions/provider-selection`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/mandates/revoke-mandate.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework");
var revokeMandate = (0, import_pieces_framework12.createAction)({
  auth: trueLayerCommon.auth,
  name: "revoke-mandate",
  displayName: "Revoke Mandate",
  description: "Revoke a mandate. This API must be called using a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Permanently revoke a mandate so no further payments can be taken under it, using a backend bearer token. Use to cancel a recurring-payment authorization. This is a destructive state change on the mandate; re-running after success has no further effect but should be treated as one-time.", idempotent: !1 },
  props: {
    id: import_pieces_framework12.Property.ShortText({
      displayName: "Mandate ID",
      description: "The ID of the mandate to revoke.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common11.httpClient.sendRequest({
    method: import_pieces_common11.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/mandates/${ctx.propsValue.id}/revoke`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/mandates/confirm-mandate-funds.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework");
var confirmMandateFunds = (0, import_pieces_framework13.createAction)({
  auth: trueLayerCommon.auth,
  name: "confirm-mandate-funds",
  displayName: "Confirm Mandate Funds",
  description: "Confirm that the PSU has the given funds. This API can be called using the mandate_token associated with the mandate or using a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Check whether the payment service user behind a mandate has sufficient funds for a given amount and currency, identified by the mandate ID. This is a read-only funds availability check that does not move money and is safe to repeat.", idempotent: !0 },
  props: {
    id: import_pieces_framework13.Property.ShortText({
      displayName: "Mandate ID",
      description: "ID of the Mandate to be confirmed.",
      required: !0
    }),
    amount_in_minor: import_pieces_framework13.Property.ShortText({
      displayName: "Amount in Minor Units",
      description: 'A "cent" value representing the amount. For example, 100 == 1 GBP.',
      required: !0
    }),
    currency: import_pieces_framework13.Property.ShortText({
      displayName: "Currency",
      description: "Currency code (e.g., GBP, EUR).",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common12.httpClient.sendRequest({
    method: import_pieces_common12.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/mandates/${ctx.propsValue.id}/funds`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: {
      amount_in_minor: ctx.propsValue.amount_in_minor,
      currency: ctx.propsValue.currency
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/mandates/get-constraints.ts
var import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework14 = require("@activepieces/pieces-framework");
var getConstraints = (0, import_pieces_framework14.createAction)({
  auth: trueLayerCommon.auth,
  name: "get-constraints",
  displayName: "Get Mandate Constraints",
  description: "Retrieve the constraints defined on the mandate, as well as the current utilization of those constraints within the periods.",
  audience: "both",
  aiMetadata: { description: "Look up the spending constraints set on a mandate and how much has already been used within each period. Use to check remaining headroom or limits before initiating a payment under the mandate. Read-only and safe to repeat.", idempotent: !0 },
  props: {
    id: import_pieces_framework14.Property.ShortText({
      displayName: "Mandate ID",
      description: "The ID of the mandate to retrieve the constraints for.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common13.httpClient.sendRequest({
    method: import_pieces_common13.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/mandates/${ctx.propsValue.id}/constraints`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/merchants/list-operating-accounts.ts
var import_pieces_common14 = require("@activepieces/pieces-common"), import_pieces_framework15 = require("@activepieces/pieces-framework");
var listOperatingAccounts = (0, import_pieces_framework15.createAction)({
  auth: trueLayerCommon.auth,
  name: "list-operating-accounts",
  displayName: "List Merchant Accounts",
  description: "List all your TrueLayer merchant accounts. There might be more than one account per currency.",
  audience: "both",
  aiMetadata: { description: "List all merchant accounts belonging to your TrueLayer client, including their currencies and balances. Use to discover available merchant account IDs before creating payouts or configuring sweeping; read-only and safe to repeat. To fetch one account by ID instead, use Get Merchant Account.", idempotent: !0 },
  props: {},
  run: async (ctx) => (await import_pieces_common14.httpClient.sendRequest({
    method: import_pieces_common14.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/merchant-accounts`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/merchants/get-operating-account.ts
var import_pieces_common15 = require("@activepieces/pieces-common"), import_pieces_framework16 = require("@activepieces/pieces-framework");
var getOperatingAccount = (0, import_pieces_framework16.createAction)({
  auth: trueLayerCommon.auth,
  name: "get-operating-account",
  displayName: "Get Merchant Account",
  description: "Get the details of a single merchant account.",
  audience: "both",
  aiMetadata: { description: "Retrieve the details of a single merchant account by its ID, including currency, balance, and account identifiers. Use when you already have the account ID; read-only and safe to repeat. To discover account IDs first, use List Merchant Accounts.", idempotent: !0 },
  props: {
    id: import_pieces_framework16.Property.ShortText({
      displayName: "Merchant Account ID",
      description: "The ID of the merchant account to be retrieved.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common15.httpClient.sendRequest({
    method: import_pieces_common15.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/merchant-accounts/${ctx.propsValue.id}`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/merchants/merchant-account-get-transactions.ts
var import_pieces_common16 = require("@activepieces/pieces-common"), import_pieces_framework17 = require("@activepieces/pieces-framework");
var merchantAccountGetTransactions = (0, import_pieces_framework17.createAction)({
  auth: trueLayerCommon.auth,
  name: "merchant-account-get-transactions",
  displayName: "Get Transactions",
  description: "Get the transactions of a single merchant account. If pagination is missing, add a header `tl-enable-pagination: true` to enable pagination.",
  audience: "both",
  aiMetadata: { description: "Retrieve the transaction history for one merchant account over a required ISO-8601 from/to date range, optionally filtered by type (payments or payouts) and paged via cursor. Use to reconcile activity on a specific account; read-only and safe to repeat. To get account metadata instead, use Get Merchant Account.", idempotent: !0 },
  props: {
    id: import_pieces_framework17.Property.ShortText({
      displayName: "Merchant Account ID",
      description: "The ID of the merchant account to return the transactions for.",
      required: !0
    }),
    from: import_pieces_framework17.Property.ShortText({
      displayName: "Start Timestamp",
      description: "Timestamp for the start of the range to query (inclusive). Uses the ISO-8601 format of YYYY-MM-DDTHH:MM:SS\xB1HHMM.",
      required: !0
    }),
    to: import_pieces_framework17.Property.ShortText({
      displayName: "End Timestamp",
      description: "Timestamp for the end of the range to query (inclusive). Uses the ISO-8601 format of YYYY-MM-DDTHH:MM:SS\xB1HHMM.",
      required: !0
    }),
    cursor: import_pieces_framework17.Property.ShortText({
      displayName: "Cursor",
      description: "Cursor used for pagination purposes, returned as `next_cursor` in the response payload of the initial request. Not required for the first page.",
      required: !1
    }),
    type: import_pieces_framework17.Property.ShortText({
      displayName: "Transaction Type",
      description: "Filters transactions by payments or payouts. If omitted, both are returned.",
      required: !1
    })
  },
  run: async (ctx) => (await import_pieces_common16.httpClient.sendRequest({
    method: import_pieces_common16.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/merchant-accounts/${ctx.propsValue.id}/transactions`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`,
      "tl-enable-pagination": "true"
    },
    queryParams: {
      from: ctx.propsValue.from,
      to: ctx.propsValue.to,
      cursor: ctx.propsValue.cursor || "",
      type: ctx.propsValue.type || ""
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/merchants/merchant-account-setup-sweeping.ts
var import_pieces_common17 = require("@activepieces/pieces-common"), import_pieces_framework18 = require("@activepieces/pieces-framework");
var merchantAccountSetupSweeping = (0, import_pieces_framework18.createAction)({
  auth: trueLayerCommon.auth,
  name: "merchant-account-setup-sweeping",
  displayName: "Set Up or Update Sweeping",
  description: "Set the automatic sweeping settings for a merchant account. At regular intervals, any available balance in excess of the configured `max_amount_in_minor` is withdrawn to a pre-configured IBAN.",
  audience: "both",
  aiMetadata: { description: "Configure or update automatic sweeping for a merchant account so balance above a threshold is periodically withdrawn to a fixed IBAN at the chosen frequency. Idempotent: it sets the full sweeping configuration for the account, so repeating with the same values leaves the same settings. To stop sweeping use Disable Sweeping; to read current settings use Get Sweeping Settings.", idempotent: !0 },
  props: {
    id: import_pieces_framework18.Property.ShortText({
      displayName: "Merchant Account ID",
      description: "The ID of the merchant account to set or update sweeping settings for.",
      required: !0
    }),
    max_amount_in_minor: import_pieces_framework18.Property.ShortText({
      displayName: "Max Amount in Minor Units",
      description: "The amount above which sweeping will occur, expressed in minor units (e.g., 100 means 1 GBP).",
      required: !0
    }),
    frequency: import_pieces_framework18.Property.ShortText({
      displayName: "Sweeping Frequency",
      description: "The frequency of the sweeping operation (e.g., daily, weekly).",
      required: !0
    }),
    iban: import_pieces_framework18.Property.ShortText({
      displayName: "IBAN",
      description: "The IBAN to which sweeping funds will be transferred.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common17.httpClient.sendRequest({
    method: import_pieces_common17.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/merchant-accounts/${ctx.propsValue.id}/sweeping`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: {
      max_amount_in_minor: ctx.propsValue.max_amount_in_minor,
      frequency: ctx.propsValue.frequency,
      iban: ctx.propsValue.iban
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/merchants/merchant-account-disable-sweeping.ts
var import_pieces_common18 = require("@activepieces/pieces-common"), import_pieces_framework19 = require("@activepieces/pieces-framework");
var merchantAccountDisableSweeping = (0, import_pieces_framework19.createAction)({
  auth: trueLayerCommon.auth,
  name: "merchant-account-disable-sweeping",
  displayName: "Disable Sweeping",
  description: "Disable automatic sweeping for a merchant account.",
  audience: "both",
  aiMetadata: { description: "Turn off automatic sweeping for a merchant account by its ID, deleting the current sweeping configuration. Idempotent in effect: once sweeping is off, repeating leaves it off. To configure or re-enable sweeping use Set Up or Update Sweeping.", idempotent: !0 },
  props: {
    id: import_pieces_framework19.Property.ShortText({
      displayName: "Merchant Account ID",
      description: "The ID of the merchant account to disable sweeping for.",
      required: !0
    })
  },
  run: async (ctx) => (await await import_pieces_common18.httpClient.sendRequest({
    method: import_pieces_common18.HttpMethod.DELETE,
    url: `${trueLayerCommon.baseUrl}/v3/merchant-accounts/${ctx.propsValue.id}/sweeping`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/merchants/merchant-account-get-sweeping.ts
var import_pieces_common19 = require("@activepieces/pieces-common"), import_pieces_framework20 = require("@activepieces/pieces-framework");
var merchantAccountGetSweeping = (0, import_pieces_framework20.createAction)({
  auth: trueLayerCommon.auth,
  name: "merchant-account-get-sweeping",
  displayName: "Get Sweeping Settings",
  description: "Get the automatic sweeping settings for a merchant account.",
  audience: "both",
  aiMetadata: { description: "Retrieve the current automatic sweeping configuration (threshold, frequency, destination IBAN) for a merchant account by its ID. Use to check whether and how sweeping is set up; read-only and safe to repeat. To change it use Set Up or Update Sweeping, or Disable Sweeping.", idempotent: !0 },
  props: {
    id: import_pieces_framework20.Property.ShortText({
      displayName: "Merchant Account ID",
      description: "The ID of the merchant account to fetch the sweeping settings for.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common19.httpClient.sendRequest({
    method: import_pieces_common19.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/merchant-accounts/${ctx.propsValue.id}/sweeping`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/merchants/get-merchant-account-payment-sources.ts
var import_pieces_common20 = require("@activepieces/pieces-common"), import_pieces_framework21 = require("@activepieces/pieces-framework");
var getMerchantAccountPaymentSources = (0, import_pieces_framework21.createAction)({
  auth: trueLayerCommon.auth,
  name: "get-merchant-account-payment-sources",
  displayName: "Get Payment Sources",
  description: "Get the payment sources from which the merchant account has received payments.",
  audience: "both",
  aiMetadata: { description: "List the verified payment sources (payer bank details) from which a given user has previously paid into a merchant account. Use to obtain a payer source for refunds or returning payouts; read-only and safe to repeat. Requires both the merchant account ID and the user ID.", idempotent: !0 },
  props: {
    id: import_pieces_framework21.Property.ShortText({
      displayName: "Merchant Account ID",
      description: "The ID of the merchant account into which payments were made.",
      required: !0
    }),
    user_id: import_pieces_framework21.Property.ShortText({
      displayName: "User ID",
      description: "The ID of the user whose payment sources are being retrieved.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common20.httpClient.sendRequest({
    method: import_pieces_common20.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/merchant-accounts/${ctx.propsValue.id}/payment-sources`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    queryParams: {
      user_id: ctx.propsValue.user_id
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payment-links/create-payment-link.ts
var import_pieces_common21 = require("@activepieces/pieces-common"), import_pieces_framework22 = require("@activepieces/pieces-framework");
var createPaymentLink = (0, import_pieces_framework22.createAction)({
  auth: trueLayerCommon.auth,
  name: "create-payment-link",
  displayName: "Create Payment Link",
  description: "Create a new payment link. This API must be called using a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Create a new hosted payment link that a payer can use to make a payment. This provisions a new resource and is not idempotent on its own, so supply the optional idempotency key so retries do not create duplicate links. To read a link or its payments afterward, use Get Payment Link or Get Payments.", idempotent: !1 },
  props: {
    IdempotencyKeyHeader: import_pieces_framework22.Property.ShortText({
      displayName: "Idempotency Key Header",
      description: "Used to ensure idempotent requests.",
      required: !1
    }),
    SignatureHeader: import_pieces_framework22.Property.ShortText({
      displayName: "Signature Header",
      description: "Used for request signature verification.",
      required: !1
    })
  },
  run: async (ctx) => (await import_pieces_common21.httpClient.sendRequest({
    method: import_pieces_common21.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payment-links`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`,
      "Idempotency-Key": ctx.propsValue.IdempotencyKeyHeader,
      Signature: ctx.propsValue.SignatureHeader
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payment-links/get-payment-link.ts
var import_pieces_common22 = require("@activepieces/pieces-common"), import_pieces_framework23 = require("@activepieces/pieces-framework");
var getPaymentLink = (0, import_pieces_framework23.createAction)({
  auth: trueLayerCommon.auth,
  name: "get-payment-link",
  displayName: "Get Payment Link",
  description: "Retrieves payment link details. This API must be called using a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Retrieve the configuration and status of a single payment link by its ID. Use to inspect a link you created; read-only and safe to repeat. To list the payments collected through it instead, use Get Payments.", idempotent: !0 },
  props: {
    id: import_pieces_framework23.Property.ShortText({
      displayName: "Payment Link ID",
      description: "The ID of the payment link to retrieve.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common22.httpClient.sendRequest({
    method: import_pieces_common22.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/payment-links/${ctx.propsValue.id}`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payment-links/get-payment-link-payments.ts
var import_pieces_common23 = require("@activepieces/pieces-common"), import_pieces_framework24 = require("@activepieces/pieces-framework");
var getPaymentLinkPayments = (0, import_pieces_framework24.createAction)({
  auth: trueLayerCommon.auth,
  name: "get-payment-link-payments",
  displayName: "Get Payments",
  description: "List all the payments associated with the payment link. This API must be called using a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "List the payments made against a specific payment link, identified by the link ID, with cursor-based pagination. Use to reconcile or review payments collected through a payment link; read-only and safe to repeat. To fetch the link itself use Get Payment Link.", idempotent: !0 },
  props: {
    id: import_pieces_framework24.Property.ShortText({
      displayName: "Payment Link ID",
      description: "The ID of the payment link for which payments are being retrieved.",
      required: !0
    }),
    cursor: import_pieces_framework24.Property.ShortText({
      displayName: "Cursor",
      description: "Cursor used for pagination purposes, returned as `next_cursor` in the response payload of the initial request. Not required for the first page of items.",
      required: !1
    }),
    limit: import_pieces_framework24.Property.ShortText({
      displayName: "Limit",
      description: "Optional limit on the number of payments to return.",
      required: !1
    })
  },
  run: async (ctx) => (await import_pieces_common23.httpClient.sendRequest({
    method: import_pieces_common23.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/payment-links/${ctx.propsValue.id}/payments`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    queryParams: {
      cursor: ctx.propsValue.cursor || "",
      limit: ctx.propsValue.limit || ""
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/create-payment.ts
var import_pieces_common24 = require("@activepieces/pieces-common"), import_pieces_framework25 = require("@activepieces/pieces-framework");
var createPayment = (0, import_pieces_framework25.createAction)({
  auth: trueLayerCommon.auth,
  name: "create-payment",
  displayName: "Create Payment",
  description: "Create a new payment. This API must be called using a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Initiate a new TrueLayer single payment using a backend bearer token, returning the payment resource and token used to drive its authorization. Use as the first step of taking a payment from a user. This creates a new payment each call; the required idempotency key prevents duplicate charges when retrying.", idempotent: !1 },
  props: {
    IdempotencyKeyHeader: import_pieces_framework25.Property.ShortText({
      displayName: "Idempotency Key",
      description: "A key that uniquely identifies the request. If the same key is sent in another request, the operation will have the same result as the first request.",
      required: !0
    }),
    SignatureHeader: import_pieces_framework25.Property.ShortText({
      displayName: "Signature Header",
      description: "Header containing the signature of the request payload for authentication purposes.",
      required: !0
    }),
    PsuIpAddressHeader: import_pieces_framework25.Property.ShortText({
      displayName: "PSU IP Address",
      description: "Used to collect and record the end-user's IP address. Only considered if the authorization_flow object in the request body is specified.",
      required: !1
    }),
    DeviceUserAgentHeader: import_pieces_framework25.Property.ShortText({
      displayName: "Device User Agent",
      description: "Used to improve the end-user's authentication experience based on the device type. If omitted, the `User-Agent` header will be used instead. Only considered if the authorization_flow object in the request body is specified.",
      required: !1
    })
  },
  run: async (ctx) => (await import_pieces_common24.httpClient.sendRequest({
    method: import_pieces_common24.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`,
      "Idempotency-Key": ctx.propsValue.IdempotencyKeyHeader,
      Signature: ctx.propsValue.SignatureHeader,
      "PSU-IP-Address": ctx.propsValue.PsuIpAddressHeader,
      "Device-User-Agent": ctx.propsValue.DeviceUserAgentHeader
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/start-payment-authorization-flow.ts
var import_pieces_common25 = require("@activepieces/pieces-common"), import_pieces_framework26 = require("@activepieces/pieces-framework");
var startPaymentAuthorizationFlow = (0, import_pieces_framework26.createAction)({
  auth: trueLayerCommon.auth,
  name: "start-payment-authorization-flow",
  displayName: "Start Payment Authorization Flow",
  description: "Start the authorization flow for a payment. This API can be called using the `resource_token` associated with the payment or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Begin the authorization flow for an existing payment, returning the first action the user must take (e.g. provider selection or redirect). Use after create-payment to start driving the user through authorization. This mutates the payment's flow state and should be called once per payment, not repeatedly.", idempotent: !1 },
  props: {
    id: import_pieces_framework26.Property.ShortText({
      displayName: "Payment ID",
      description: "The ID of the payment for which to start the authorization flow.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common25.httpClient.sendRequest({
    method: import_pieces_common25.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.id}/authorization-flow`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/submit-provider-selection.ts
var import_pieces_common26 = require("@activepieces/pieces-common"), import_pieces_framework27 = require("@activepieces/pieces-framework");
var submitProviderSelection = (0, import_pieces_framework27.createAction)({
  auth: trueLayerCommon.auth,
  name: "submit-provider-selection",
  displayName: "Submit Provider Selection",
  description: "Submit the provider details selected by the PSU. This API can be called using the `resource_token` associated with the payment or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Advance a payment's authorization flow by submitting the bank provider the user selected. Use only when the flow has reached a provider-selection step. This mutates flow state and is not safe to repeat; it is the payment counterpart to the mandate provider-selection action.", idempotent: !1 },
  props: {
    id: import_pieces_framework27.Property.ShortText({
      displayName: "Payment ID",
      description: "The ID of the payment for which provider selection is being submitted.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common26.httpClient.sendRequest({
    method: import_pieces_common26.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.id}/authorization-flow/actions/provider-selection`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/submit-scheme-selection.ts
var import_pieces_common27 = require("@activepieces/pieces-common"), import_pieces_framework28 = require("@activepieces/pieces-framework");
var submitSchemeSelection = (0, import_pieces_framework28.createAction)({
  auth: trueLayerCommon.auth,
  name: "submit-scheme-selection",
  displayName: "Submit Scheme Selection",
  description: "Submit the scheme details selected by the PSU. This API can be called using the `resource_token` associated with the payment or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Advance a payment's authorization flow by submitting the payment scheme the user chose. Use only when the flow has reached a scheme-selection step. This mutates flow state and is not safe to repeat; distinct from the provider-selection, consent, and form submission steps of the same flow.", idempotent: !1 },
  props: {
    id: import_pieces_framework28.Property.ShortText({
      displayName: "Payment ID",
      description: "The ID of the payment for which the scheme details are being submitted.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common27.httpClient.sendRequest({
    method: import_pieces_common27.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.id}/authorization-flow/actions/scheme-selection`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/submit-form.ts
var import_pieces_common28 = require("@activepieces/pieces-common"), import_pieces_framework29 = require("@activepieces/pieces-framework");
var submitForm = (0, import_pieces_framework29.createAction)({
  auth: trueLayerCommon.auth,
  name: "submit-form",
  displayName: "Submit Form",
  description: "Submit form details filled by the PSU. This API can be called using the `resource_token` associated with the payment or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Advance a payment's authorization flow by submitting form input the user filled in (e.g. additional bank-required fields). Use only when the flow has reached a form step. This mutates flow state and is not safe to repeat; distinct from provider, scheme, consent, and account-selection steps.", idempotent: !1 },
  props: {
    id: import_pieces_framework29.Property.ShortText({
      displayName: "Payment ID",
      description: "The ID of the payment for which the form is being submitted.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common28.httpClient.sendRequest({
    method: import_pieces_common28.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.id}/authorization-flow/actions/form`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/submit-consent.ts
var import_pieces_common29 = require("@activepieces/pieces-common"), import_pieces_framework30 = require("@activepieces/pieces-framework");
var submitConsent = (0, import_pieces_framework30.createAction)({
  auth: trueLayerCommon.auth,
  name: "submit-consent",
  displayName: "Submit Consent",
  description: "Submit the consent given by the user. This API can be called using the `resource_token` associated with the payment or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Advance a payment's authorization flow by recording the user's consent. Use only when the flow has reached a consent step. This mutates flow state and is not safe to repeat; it is one of several authorization-flow steps (provider, scheme, form, account selection).", idempotent: !1 },
  props: {
    id: import_pieces_framework30.Property.ShortText({
      displayName: "Payment ID",
      description: "The ID of the payment for which consent is being submitted.",
      required: !0
    })
  },
  run: async (ctx) => (await await import_pieces_common29.httpClient.sendRequest({
    method: import_pieces_common29.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.id}/authorization-flow/actions/consent`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/submit-user-account-selection.ts
var import_pieces_common30 = require("@activepieces/pieces-common"), import_pieces_framework31 = require("@activepieces/pieces-framework");
var submitUserAccountSelection = (0, import_pieces_framework31.createAction)({
  auth: trueLayerCommon.auth,
  name: "submit-user-account-selection",
  displayName: "Submit User Account Selection",
  description: "Submit the user account selection option given by the user. This API can be called using the `resource_token` associated with the payment or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Advance a payment's authorization flow by submitting which of the user's accounts to pay from. Use only when the flow has reached a user-account-selection step. This mutates flow state and is not safe to repeat; distinct from provider, scheme, consent, and form steps.", idempotent: !1 },
  props: {
    id: import_pieces_framework31.Property.ShortText({
      displayName: "Payment ID",
      description: "The ID of the payment for which the user account selection is being submitted.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common30.httpClient.sendRequest({
    method: import_pieces_common30.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.id}/authorization-flow/actions/user-account-selection`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/cancel-payment.ts
var import_pieces_common31 = require("@activepieces/pieces-common"), import_pieces_framework32 = require("@activepieces/pieces-framework");
var cancelPayment = (0, import_pieces_framework32.createAction)({
  auth: trueLayerCommon.auth,
  name: "cancel-payment",
  displayName: "Cancel Payment",
  description: "Cancel a payment. This API can be called using the `resource_token` associated with the payment or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Cancel an in-progress payment so it can no longer be authorized or executed. Use to abandon a payment before it completes. This is a state change on the payment and should be treated as one-time; callable with the payment resource token or a backend bearer token.", idempotent: !1 },
  props: {
    id: import_pieces_framework32.Property.ShortText({
      displayName: "Payment ID",
      description: "The ID of the payment to cancel.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common31.httpClient.sendRequest({
    method: import_pieces_common31.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.id}/actions/cancel`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/save-user-account-payment.ts
var import_pieces_common32 = require("@activepieces/pieces-common"), import_pieces_framework33 = require("@activepieces/pieces-framework");
var saveUserAccountPayment = (0, import_pieces_framework33.createAction)({
  auth: trueLayerCommon.auth,
  name: "save-user-account-payment",
  displayName: "Save Payment Account",
  description: "Save the account details associated with a payment for subsequent re-use. This API can be called using the `resource_token` associated with the payment or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Save the payer's account details from a payment so they can be reused for future payments. Use after a successful payment to store the account for repeat use. This writes new stored-account state and is not a read; callable with the payment resource token or a backend bearer token.", idempotent: !1 },
  props: {
    id: import_pieces_framework33.Property.ShortText({
      displayName: "Payment ID",
      description: "The ID of the payment to save the account details for.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common32.httpClient.sendRequest({
    method: import_pieces_common32.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.id}/actions/save-user-account`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/get-payment.ts
var import_pieces_common33 = require("@activepieces/pieces-common"), import_pieces_framework34 = require("@activepieces/pieces-framework");
var getPayment = (0, import_pieces_framework34.createAction)({
  auth: trueLayerCommon.auth,
  name: "get-payment",
  displayName: "Get Payment",
  description: "Returns payment details. This API can be called using either the `resource_token` associated with the payment or a backend bearer token.",
  audience: "both",
  aiMetadata: { description: "Retrieve a single payment by its ID, including its current status. Use when you have a known payment ID and need to inspect or poll the payment's state (e.g. waiting for it to settle). Read-only and safe to repeat; callable with either the payment resource token or a backend bearer token.", idempotent: !0 },
  props: {
    id: import_pieces_framework34.Property.ShortText({
      displayName: "Payment ID",
      description: "The ID of the payment to retrieve.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common33.httpClient.sendRequest({
    method: import_pieces_common33.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.id}`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/create-payment-refund.ts
var import_pieces_common34 = require("@activepieces/pieces-common"), import_pieces_framework35 = require("@activepieces/pieces-framework");
var createPaymentRefund = (0, import_pieces_framework35.createAction)({
  auth: trueLayerCommon.auth,
  name: "create-payment-refund",
  displayName: "Create Payment Refund",
  description: "Refund a merchant account payment, either fully or partially.",
  audience: "both",
  aiMetadata: { description: "Issue a refund against an existing merchant-account payment, given its payment ID; omit the amount to refund in full or set an amount in minor units for a partial refund. Use to return funds to a payer. This creates a new refund each call and is not safe to repeat without external deduplication.", idempotent: !1 },
  props: {
    id: import_pieces_framework35.Property.ShortText({
      displayName: "Payment ID",
      description: "The payment ID for the payment to be fully or partially refunded.",
      required: !0
    }),
    amount_in_minor: import_pieces_framework35.Property.ShortText({
      displayName: "Amount in Minor Units",
      description: "The amount to refund, expressed in minor units (e.g., 100 means 1 GBP).",
      required: !1
    })
  },
  run: async (ctx) => (await import_pieces_common34.httpClient.sendRequest({
    method: import_pieces_common34.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.id}/refunds`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    },
    body: {
      amount_in_minor: ctx.propsValue.amount_in_minor
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/get-payment-refunds.ts
var import_pieces_common35 = require("@activepieces/pieces-common"), import_pieces_framework36 = require("@activepieces/pieces-framework");
var getPaymentRefunds = (0, import_pieces_framework36.createAction)({
  auth: trueLayerCommon.auth,
  name: "get-payment-refunds",
  displayName: "Get Payment Refunds",
  description: "Returns all refunds of a payment.",
  audience: "both",
  aiMetadata: { description: "List all refunds issued against a given merchant-account payment. Use to review a payment's refund history or find a refund ID. Read-only and safe to repeat; to fetch one refund by its ID, use the single-refund lookup action instead.", idempotent: !0 },
  props: {
    id: import_pieces_framework36.Property.ShortText({
      displayName: "Payment ID",
      description: "The ID of the merchant account payment to retrieve all refunds for.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common35.httpClient.sendRequest({
    method: import_pieces_common35.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.id}/refunds`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments/get-payment-refund.ts
var import_pieces_common36 = require("@activepieces/pieces-common"), import_pieces_framework37 = require("@activepieces/pieces-framework");
var getPaymentRefund = (0, import_pieces_framework37.createAction)({
  auth: trueLayerCommon.auth,
  name: "get-payment-refund",
  displayName: "Get Payment Refund",
  description: "Returns refund details for a specific payment.",
  audience: "both",
  aiMetadata: { description: "Retrieve details and status of a single refund, identified by both its payment ID and refund ID. Use when you know the specific refund you want to inspect. Read-only and safe to repeat; to list every refund on a payment instead, use the get-all-refunds action.", idempotent: !0 },
  props: {
    payment_id: import_pieces_framework37.Property.ShortText({
      displayName: "Payment ID",
      description: "The ID of the payment for which the refund was made.",
      required: !0
    }),
    refund_id: import_pieces_framework37.Property.ShortText({
      displayName: "Refund ID",
      description: "The ID of the refund to retrieve details for.",
      required: !0
    })
  },
  run: async (ctx) => (await import_pieces_common36.httpClient.sendRequest({
    method: import_pieces_common36.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/payments/${ctx.propsValue.payment_id}/refunds/${ctx.propsValue.refund_id}`,
    headers: {
      Authorization: `Bearer ${ctx.auth.access_token}`
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments-providers/search-payment-providers.ts
var import_pieces_common37 = require("@activepieces/pieces-common"), import_pieces_framework38 = require("@activepieces/pieces-framework");
var searchPaymentProviders = (0, import_pieces_framework38.createAction)({
  auth: trueLayerCommon.auth,
  name: "search-payment-providers",
  displayName: "Search Payment Providers",
  description: "Returns a list of payment providers.",
  audience: "both",
  aiMetadata: { description: "Search and list available TrueLayer payment providers (banks) that can be offered to a payer. Use to discover provider options before initiating a payment; read-only and safe to repeat. To fetch one provider by its known ID instead, use Get Payment Provider.", idempotent: !0 },
  props: {},
  run: async (ctx) => (await import_pieces_common37.httpClient.sendRequest({
    method: import_pieces_common37.HttpMethod.POST,
    url: `${trueLayerCommon.baseUrl}/v3/payments-providers/search`,
    body: ctx.propsValue
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/lib/action/payments-providers/get-payment-provider.ts
var import_pieces_common38 = require("@activepieces/pieces-common"), import_pieces_framework39 = require("@activepieces/pieces-framework");
var getPaymentProvider = (0, import_pieces_framework39.createAction)({
  auth: trueLayerCommon.auth,
  name: "get-payment-provider",
  displayName: "Get Payment Provider",
  description: "Returns payment provider details. This API can be called without the need for authentication.",
  audience: "both",
  aiMetadata: { description: "Retrieve full details for a single TrueLayer payment provider (bank) by its known provider ID, optionally selecting an icon variant. Use when you already have the provider ID; read-only and safe to repeat. To discover providers by criteria instead, use Search Payment Providers.", idempotent: !0 },
  props: {
    id: import_pieces_framework39.Property.ShortText({
      displayName: "Payment Provider ID",
      description: "The ID of the payment provider to retrieve details for.",
      required: !0
    }),
    client_id: import_pieces_framework39.Property.ShortText({
      displayName: "Client ID",
      description: "Optional client ID to retrieve specific provider details.",
      required: !1
    }),
    icon_type: import_pieces_framework39.Property.ShortText({
      displayName: "Icon Type",
      description: "Optional configuration for the type of icon:\n      - `default`: Default icon with no background (SVG).\n      - `extended`: Extended to a square with an appropriate background color (SVG).\n      - `extended_small`: Extended icon with 192x192 px size (JPEG).\n      - `extended_medium`: Extended icon with 432x432 px size (JPEG).\n      - `extended_large`: Extended icon jpeg with 864x864 px size (JPEG).",
      required: !1
    })
  },
  run: async (ctx) => (await import_pieces_common38.httpClient.sendRequest({
    method: import_pieces_common38.HttpMethod.GET,
    url: `${trueLayerCommon.baseUrl}/v3/payments-providers/${ctx.propsValue.id}`,
    queryParams: {
      client_id: ctx.propsValue.client_id || "",
      icon_type: ctx.propsValue.icon_type || ""
    }
  })).body
});

// ../ap/packages/pieces/community/truelayer/src/index.ts
var import_pieces_framework41 = require("@activepieces/pieces-framework"), paymentsApiV3Payments = (0, import_pieces_framework40.createPiece)({
  displayName: "TrueLayer",
  description: "Connect with TrueLayer to leverage secure open banking services. This integration allows seamless interaction with TrueLayer's API to manage various financial processes.",
  auth: trueLayerCommon.auth,
  minimumSupportedRelease: "0.20.0",
  categories: [import_pieces_framework41.PieceCategory.PAYMENT_PROCESSING],
  logoUrl: "https://cdn.activepieces.com/pieces/truelayer.png",
  authors: ["ahmad-swanblocks"],
  actions: [
    createPayout,
    getPayout,
    startPayoutAuthorizationFlow,
    submitPaymentsProviderReturnParameters,
    createMandate,
    listMandate,
    getMandate,
    startMandateAuthorizationFlow,
    submitConsentMandate,
    submitMandateProviderSelection,
    revokeMandate,
    confirmMandateFunds,
    getConstraints,
    listOperatingAccounts,
    getOperatingAccount,
    merchantAccountGetTransactions,
    merchantAccountSetupSweeping,
    merchantAccountDisableSweeping,
    merchantAccountGetSweeping,
    getMerchantAccountPaymentSources,
    createPaymentLink,
    getPaymentLink,
    getPaymentLinkPayments,
    createPayment,
    startPaymentAuthorizationFlow,
    submitProviderSelection,
    submitSchemeSelection,
    submitForm,
    submitConsent,
    submitUserAccountSelection,
    cancelPayment,
    saveUserAccountPayment,
    getPayment,
    createPaymentRefund,
    getPaymentRefunds,
    getPaymentRefund,
    searchPaymentProviders,
    getPaymentProvider,
    (0, import_pieces_common39.createCustomApiCallAction)({
      baseUrl: () => trueLayerCommon.baseUrl,
      auth: trueLayerCommon.auth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.access_token}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  paymentsApiV3Payments
});
