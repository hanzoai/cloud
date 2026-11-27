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

// ../ap/packages/pieces/community/strale/src/index.ts
var index_exports = {};
__export(index_exports, {
  strale: () => strale
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/strale/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), straleAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your Strale API key (starts with sk_live_). Get one free at https://strale.dev/signup \u2014 includes 2.00 EUR trial credits. Some actions (Search, Trust Profile) work without a key.",
  required: !1,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        url: "https://api.strale.io/v1/wallet/balance",
        method: import_pieces_common.HttpMethod.GET,
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API key." };
    }
  }
});

// ../ap/packages/pieces/community/strale/src/lib/actions/search.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
var searchCapabilities = (0, import_pieces_framework2.createAction)({
  name: "search_capabilities",
  auth: straleAuth,
  displayName: "Search Capabilities",
  description: "Search 271 API capabilities by keyword, category, or natural language query. Returns matching capabilities with slug, description, price, and quality score. No API key required.",
  audience: "both",
  aiMetadata: {
    description: "Discover Strale API capabilities (company verification, sanctions screening, IBAN/VAT validation, web intelligence, etc.) by keyword or natural-language query, optionally narrowed to a single category. Use this first to find the slug to feed into Execute Capability. Read-only and idempotent; requires a search query.",
    idempotent: !0
  },
  props: {
    query: import_pieces_framework2.Property.ShortText({
      displayName: "Search Query",
      description: 'Keyword or natural language description (e.g. "validate IBAN", "company data Sweden", "sanctions check")',
      required: !0
    }),
    category: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Category",
      description: "Filter results by category",
      required: !1,
      options: {
        options: [
          { label: "All Categories", value: "" },
          { label: "Compliance", value: "compliance" },
          { label: "Company Data", value: "company-data" },
          { label: "Financial", value: "financial" },
          { label: "Validation", value: "validation" },
          { label: "Web Intelligence", value: "web-intelligence" },
          { label: "Developer Tools", value: "developer-tools" },
          { label: "Security", value: "security" },
          { label: "Web3", value: "web3" },
          { label: "Data Processing", value: "data-processing" }
        ]
      }
    })
  },
  async run(context) {
    let { query, category } = context.propsValue, params = new URLSearchParams({ q: query, limit: "10" });
    return category && params.set("category", category), (await import_pieces_common2.httpClient.sendRequest({
      url: `https://api.strale.io/v1/suggest/typeahead?${params}`,
      method: import_pieces_common2.HttpMethod.GET,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/strale/src/lib/actions/execute.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var executeCapability = (0, import_pieces_framework3.createAction)({
  name: "execute_capability",
  auth: straleAuth,
  displayName: "Execute Capability",
  description: "Run any Strale capability by slug. Performs verifications, validations, lookups, and data extraction. Returns structured output with quality score and data provenance. Five free capabilities work without an API key.",
  audience: "both",
  aiMetadata: {
    description: "Run any Strale capability by slug, passing its required input fields (e.g. an IBAN to validate or a company to verify) and an optional spend cap. Choose this to actually perform a verification, validation, lookup, or data extraction after finding the slug via Search Capabilities. Not idempotent: each call executes the capability and may consume wallet balance; some capabilities are free while paid ones require an API key with sufficient funds.",
    idempotent: !1
  },
  props: {
    slug: import_pieces_framework3.Property.ShortText({
      displayName: "Capability Slug",
      description: 'The slug of the capability to run (e.g. "iban-validate", "sanctions-check"). Use Search to find slugs.',
      required: !0
    }),
    inputs: import_pieces_framework3.Property.Json({
      displayName: "Input Parameters",
      description: 'Input object matching the capability required fields (e.g. {"iban": "DE89370400440532013000"})',
      required: !0,
      defaultValue: {}
    }),
    max_price_cents: import_pieces_framework3.Property.Number({
      displayName: "Max Price (EUR cents)",
      description: "Maximum price in EUR cents. Default: 200 (2.00 EUR).",
      required: !1,
      defaultValue: 200
    })
  },
  async run(context) {
    let { slug, inputs, max_price_cents } = context.propsValue;
    return (await import_pieces_common3.httpClient.sendRequest({
      url: "https://api.strale.io/v1/do",
      method: import_pieces_common3.HttpMethod.POST,
      headers: { "Content-Type": "application/json" },
      body: {
        capability_slug: slug,
        inputs: typeof inputs == "string" ? JSON.parse(inputs) : inputs,
        max_price_cents: max_price_cents != null ? max_price_cents : 200
      },
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/strale/src/lib/actions/balance.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var checkBalance = (0, import_pieces_framework4.createAction)({
  name: "check_balance",
  auth: straleAuth,
  displayName: "Check Balance",
  description: "Returns the current wallet balance in EUR cents and formatted EUR. Use this before executing paid capabilities to verify sufficient funds. Requires an API key.",
  audience: "both",
  aiMetadata: {
    description: "Look up the current Strale wallet balance (EUR cents plus a formatted EUR string). Use this to check available funds before running paid capabilities via Execute Capability. Read-only and idempotent; takes no input but requires an API key.",
    idempotent: !0
  },
  props: {},
  async run(context) {
    let data = (await import_pieces_common4.httpClient.sendRequest({
      url: "https://api.strale.io/v1/wallet/balance",
      method: import_pieces_common4.HttpMethod.GET,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body;
    return {
      balance_cents: data.balance_cents,
      balance_eur: `EUR ${(data.balance_cents / 100).toFixed(2)}`,
      currency: data.currency
    };
  }
});

// ../ap/packages/pieces/community/strale/src/lib/actions/trust-profile.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var trustProfile = (0, import_pieces_framework5.createAction)({
  name: "trust_profile",
  auth: straleAuth,
  displayName: "Trust Profile",
  description: "Returns the trust profile for a capability: SQS quality score (0-100), Quality grade (A-F), Reliability grade (A-F), and execution guidance. Use this before relying on a capability for high-stakes decisions. No API key required.",
  audience: "both",
  aiMetadata: {
    description: "Fetch the trust profile for a Strale slug \u2014 quality score (0-100), quality and reliability grades (A-F), and execution guidance. Set the type to look up either an individual capability or a bundled solution. Use this to gauge reliability before depending on a capability for high-stakes decisions. Read-only and idempotent; requires the target slug.",
    idempotent: !0
  },
  props: {
    slug: import_pieces_framework5.Property.ShortText({
      displayName: "Capability Slug",
      description: 'The slug of the capability to check (e.g. "sanctions-check", "iban-validate")',
      required: !0
    }),
    type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Type",
      description: "Whether this is a capability or a bundled solution",
      required: !1,
      defaultValue: "capability",
      options: {
        options: [
          { label: "Capability", value: "capability" },
          { label: "Solution", value: "solution" }
        ]
      }
    })
  },
  async run(context) {
    let { slug, type } = context.propsValue, endpoint = type === "solution" ? `/v1/internal/trust/solutions/${encodeURIComponent(slug)}` : `/v1/internal/trust/capabilities/${encodeURIComponent(slug)}`;
    return (await import_pieces_common5.httpClient.sendRequest({
      url: `https://api.strale.io${endpoint}`,
      method: import_pieces_common5.HttpMethod.GET,
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/strale/src/index.ts
var strale = (0, import_pieces_framework6.createPiece)({
  displayName: "Strale",
  description: "Trust layer for AI agents. 271 quality-scored API capabilities: company verification across 27 countries, sanctions screening, IBAN/VAT validation, web intelligence, and more. Every result includes a quality score and audit trail.",
  auth: straleAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/strale.png",
  authors: ["petterlindstrom79", "sanket-a11y"],
  categories: [import_pieces_framework7.PieceCategory.ARTIFICIAL_INTELLIGENCE],
  actions: [
    searchCapabilities,
    executeCapability,
    checkBalance,
    trustProfile,
    (0, import_pieces_common6.createCustomApiCallAction)({
      baseUrl: () => "https://api.strale.io",
      auth: straleAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  strale
});
