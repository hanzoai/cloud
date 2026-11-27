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

// ../ap/packages/pieces/community/cryptolens/src/index.ts
var index_exports = {};
__export(index_exports, {
  cryptolens: () => cryptolens
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/cryptolens/src/lib/common/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), CRYPTOLENS_API_BASE_URL = "https://api.cryptolens.io/api";
async function validateAccessToken(token) {
  try {
    return await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: `${CRYPTOLENS_API_BASE_URL}/auth/GetToken?token=${encodeURIComponent(
        token
      )}`
    }), !0;
  } catch (e) {
    return !1;
  }
}
var cryptolensAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Access Token",
  description: "Your Cryptolens access token. Get it from https://app.cryptolens.io/User/AccessToken",
  required: !0,
  validate: async ({ auth }) => await validateAccessToken(auth) ? { valid: !0 } : {
    valid: !1,
    error: "Invalid access token"
  }
});

// ../ap/packages/pieces/community/cryptolens/src/lib/actions/add-customer.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/cryptolens/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), BASE_URL = "https://api.cryptolens.io/api";
async function makeRequest(access_token, method, path, body) {
  try {
    let [pathname, search] = path.split("?"), authenticatedUrl = `${BASE_URL}${pathname}?token=${encodeURIComponent(
      access_token
    )}`, sendsFormBody = method !== import_pieces_common2.HttpMethod.GET && !!search;
    return (await import_pieces_common2.httpClient.sendRequest({
      method,
      url: sendsFormBody || !search ? authenticatedUrl : `${authenticatedUrl}&${search}`,
      headers: {
        "Content-Type": sendsFormBody ? "application/x-www-form-urlencoded" : "application/json"
      },
      body: sendsFormBody ? search : body
    })).body;
  } catch (error) {
    throw new Error(`Unexpected error: ${error.message || String(error)}`);
  }
}

// ../ap/packages/pieces/community/cryptolens/src/lib/actions/add-customer.ts
var import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/cryptolens/src/lib/output-schemas.ts
var envelopeFields = [
  { key: "result", label: "Result Code", format: "number" },
  { key: "message", label: "Message" }
], addCustomerActionOutputSchema = {
  fields: [
    { key: "customerId", label: "Customer ID", format: "number" },
    { key: "secret", label: "Customer Secret" },
    { key: "portalLink", label: "Customer Portal Link", format: "url" },
    ...envelopeFields
  ]
}, createKeyActionOutputSchema = {
  fields: [
    { key: "key", label: "License Key" },
    {
      key: "keys",
      label: "License Keys",
      labelKey: "key",
      listItems: [
        { key: "key", label: "License Key" },
        { key: "result", label: "Result Code", format: "number" },
        { key: "message", label: "Message" }
      ]
    },
    ...envelopeFields
  ]
}, blockKeyActionOutputSchema = {
  fields: envelopeFields
}, extendLicenseActionOutputSchema = {
  fields: envelopeFields
}, newApiEventTriggerOutputSchema = {
  fields: [
    { key: "id", label: "Event ID", format: "number" },
    { key: "productId", label: "Product ID", format: "number" },
    { key: "key", label: "License Key" },
    { key: "ip", label: "Caller IP" },
    { key: "time", label: "Time (Unix Seconds)", format: "number" },
    { key: "state", label: "Event State", format: "number" },
    { key: "machineCode", label: "Machine Code" },
    { key: "friendlyName", label: "Friendly Name" },
    { key: "floatingExpires", label: "Floating Expires", format: "number" },
    { key: "doIntValue", label: "Data Object Int Value", format: "number" },
    { key: "doId", label: "Data Object ID", format: "number" }
  ]
};

// ../ap/packages/pieces/community/cryptolens/src/lib/actions/add-customer.ts
var addCustomer = (0, import_pieces_framework2.createAction)({
  auth: cryptolensAuth,
  name: "addCustomer",
  displayName: "Add Customer",
  description: "Add a new customer to your Cryptolens account",
  audience: "both",
  aiMetadata: {
    description: "Creates a new customer record in a Cryptolens account, optionally enabling a customer portal so the customer can self-manage their licenses and device activations. Use to onboard a license holder before associating keys with them. Requires name, email, and company name; not idempotent \u2014 each call creates a separate customer.",
    idempotent: !1
  },
  outputSchema: addCustomerActionOutputSchema,
  props: {
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      description: "The name of the customer (max 100 characters)",
      required: !0
    }),
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email",
      description: "The email of the customer (max 100 characters)",
      required: !0
    }),
    companyName: import_pieces_framework2.Property.ShortText({
      displayName: "Company Name",
      description: "The company name the customer belongs to (max 100 characters)",
      required: !0
    }),
    notes: import_pieces_framework2.Property.LongText({
      displayName: "Notes",
      description: "Optional notes field (max 1000 characters)",
      required: !1
    }),
    enableCustomerAssociation: import_pieces_framework2.Property.Checkbox({
      displayName: "Enable Customer Portal",
      description: "If enabled, a portal link will be returned where the customer can view their licenses",
      required: !1
    }),
    allowActivationManagement: import_pieces_framework2.Property.Checkbox({
      displayName: "Allow Activation Management",
      description: "If enabled, the customer can activate and deactivate devices through the customer portal",
      required: !1
    }),
    allowMultipleUserAssociation: import_pieces_framework2.Property.Checkbox({
      displayName: "Allow Multiple User Association",
      description: "If enabled, multiple user accounts can be associated with this customer",
      required: !1
    })
  },
  async run(context) {
    let params = new URLSearchParams({
      Name: context.propsValue.name,
      Email: context.propsValue.email,
      CompanyName: context.propsValue.companyName
    });
    return context.propsValue.notes && params.append("Notes", context.propsValue.notes), context.propsValue.enableCustomerAssociation && params.append("EnableCustomerAssociation", "true"), context.propsValue.allowActivationManagement && params.append("AllowActivationManagement", "true"), context.propsValue.allowMultipleUserAssociation && params.append("AllowMultipleUserAssociation", "true"), await makeRequest(
      context.auth.secret_text,
      import_pieces_common3.HttpMethod.POST,
      `/customer/AddCustomer?${params.toString()}`
    );
  }
});

// ../ap/packages/pieces/community/cryptolens/src/lib/actions/block-key.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var blockKey = (0, import_pieces_framework3.createAction)({
  auth: cryptolensAuth,
  name: "blockKey",
  displayName: "Block Key",
  description: "Block a license key to prevent it from being accessible by most Web API methods",
  audience: "both",
  aiMetadata: {
    description: "Blocks a specific license key for a product so it is rejected by most Cryptolens Web API methods (e.g. activation/validation). Use to revoke or suspend a license. Requires the product ID and the serial key string. Blocking is a state change, but re-blocking an already-blocked key has no additional effect.",
    idempotent: !0
  },
  outputSchema: blockKeyActionOutputSchema,
  props: {
    productId: import_pieces_framework3.Property.Number({
      displayName: "Product ID",
      description: "The product ID",
      required: !0
    }),
    key: import_pieces_framework3.Property.ShortText({
      displayName: "License Key",
      description: "The serial key string to block",
      required: !0
    })
  },
  async run(context) {
    let params = new URLSearchParams({
      ProductId: String(context.propsValue.productId),
      Key: context.propsValue.key
    });
    return await makeRequest(
      context.auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      `/key/BlockKey?${params.toString()}`
    );
  }
});

// ../ap/packages/pieces/community/cryptolens/src/lib/actions/create-key.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");
var createKey = (0, import_pieces_framework4.createAction)({
  auth: cryptolensAuth,
  name: "createKey",
  displayName: "Create Key",
  description: "Create a new license key for a product",
  audience: "both",
  aiMetadata: {
    description: "Generates one or more new license keys for a Cryptolens product, with configurable validity period, feature flags (F1-F8), machine activation limits, trial activation, and optional association to a customer or reseller. Use to issue licenses for a product. Requires the product ID; not idempotent \u2014 each call mints new key(s), and Number of Keys can generate up to 1000 at once.",
    idempotent: !1
  },
  outputSchema: createKeyActionOutputSchema,
  props: {
    productId: import_pieces_framework4.Property.Number({
      displayName: "Product ID",
      description: "The product ID",
      required: !0
    }),
    period: import_pieces_framework4.Property.Number({
      displayName: "Validity Period (days)",
      description: "The number of days from today the license key should be valid. Default is 0 (no expiration).",
      required: !1
    }),
    f1: import_pieces_framework4.Property.Checkbox({
      displayName: "Feature 1",
      description: "Enable Feature 1",
      required: !1
    }),
    f2: import_pieces_framework4.Property.Checkbox({
      displayName: "Feature 2",
      description: "Enable Feature 2",
      required: !1
    }),
    f3: import_pieces_framework4.Property.Checkbox({
      displayName: "Feature 3",
      description: "Enable Feature 3",
      required: !1
    }),
    f4: import_pieces_framework4.Property.Checkbox({
      displayName: "Feature 4",
      description: "Enable Feature 4",
      required: !1
    }),
    f5: import_pieces_framework4.Property.Checkbox({
      displayName: "Feature 5",
      description: "Enable Feature 5",
      required: !1
    }),
    f6: import_pieces_framework4.Property.Checkbox({
      displayName: "Feature 6",
      description: "Enable Feature 6",
      required: !1
    }),
    f7: import_pieces_framework4.Property.Checkbox({
      displayName: "Feature 7",
      description: "Enable Feature 7",
      required: !1
    }),
    f8: import_pieces_framework4.Property.Checkbox({
      displayName: "Feature 8",
      description: "Enable Feature 8",
      required: !1
    }),
    notes: import_pieces_framework4.Property.LongText({
      displayName: "Notes",
      description: "Additional notes about the license key (max 500 characters)",
      required: !1
    }),
    block: import_pieces_framework4.Property.Checkbox({
      displayName: "Block Key",
      description: "Block the key from being accessed by the Activation method",
      required: !1
    }),
    customerId: import_pieces_framework4.Property.Number({
      displayName: "Customer ID",
      description: "The ID of the customer to associate with this license key",
      required: !1
    }),
    trialActivation: import_pieces_framework4.Property.Checkbox({
      displayName: "Enable Trial Activation",
      description: "Enable or disable trial activation",
      required: !1
    }),
    maxNoOfMachines: import_pieces_framework4.Property.Number({
      displayName: "Max Number of Machines",
      description: "The maximum number of computers that can activate the key. Default is 0 (unlimited).",
      required: !1
    }),
    allowedMachines: import_pieces_framework4.Property.LongText({
      displayName: "Allowed Machines",
      description: "A list of machine codes (separated by new lines) that will be prioritized during activation",
      required: !1
    }),
    resellerId: import_pieces_framework4.Property.Number({
      displayName: "Reseller ID",
      description: "The ID of the reseller to associate with this license key",
      required: !1
    }),
    noOfKeys: import_pieces_framework4.Property.Number({
      displayName: "Number of Keys",
      description: "The number of keys to generate. Default is 1. Maximum 1000 per request.",
      required: !1
    })
  },
  async run(context) {
    let params = new URLSearchParams({
      ProductId: String(context.propsValue.productId)
    });
    return context.propsValue.period !== void 0 && context.propsValue.period !== null && params.append("Period", String(context.propsValue.period)), context.propsValue.f1 && params.append("F1", "true"), context.propsValue.f2 && params.append("F2", "true"), context.propsValue.f3 && params.append("F3", "true"), context.propsValue.f4 && params.append("F4", "true"), context.propsValue.f5 && params.append("F5", "true"), context.propsValue.f6 && params.append("F6", "true"), context.propsValue.f7 && params.append("F7", "true"), context.propsValue.f8 && params.append("F8", "true"), context.propsValue.notes && params.append("Notes", context.propsValue.notes), context.propsValue.block && params.append("Block", "true"), context.propsValue.customerId !== void 0 && context.propsValue.customerId !== null && params.append("CustomerId", String(context.propsValue.customerId)), context.propsValue.trialActivation && params.append("TrialActivation", "true"), context.propsValue.maxNoOfMachines !== void 0 && context.propsValue.maxNoOfMachines !== null && params.append(
      "MaxNoOfMachines",
      String(context.propsValue.maxNoOfMachines)
    ), context.propsValue.allowedMachines && params.append("AllowedMachines", context.propsValue.allowedMachines), context.propsValue.resellerId !== void 0 && context.propsValue.resellerId !== null && params.append("ResellerId", String(context.propsValue.resellerId)), context.propsValue.noOfKeys !== void 0 && context.propsValue.noOfKeys !== null && params.append("NoOfKeys", String(context.propsValue.noOfKeys)), await makeRequest(
      context.auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      `/key/CreateKey?${params.toString()}`
    );
  }
});

// ../ap/packages/pieces/community/cryptolens/src/lib/actions/extend-license.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common");
var extendLicense = (0, import_pieces_framework5.createAction)({
  auth: cryptolensAuth,
  name: "extendLicense",
  displayName: "Extend License",
  description: "Extend a license key by a specified number of days",
  audience: "both",
  aiMetadata: {
    description: "Extends the expiration date of an existing Cryptolens license key by a number of days, or shortens it with a negative number. Use to renew or adjust a license without issuing a new key. Requires the product ID and the serial key string; not idempotent \u2014 each call shifts the expiration date again.",
    idempotent: !1
  },
  outputSchema: extendLicenseActionOutputSchema,
  props: {
    productId: import_pieces_framework5.Property.Number({
      displayName: "Product ID",
      description: "The product ID",
      required: !0
    }),
    key: import_pieces_framework5.Property.ShortText({
      displayName: "License Key",
      description: "The serial key string to extend",
      required: !0
    }),
    noOfDays: import_pieces_framework5.Property.Number({
      displayName: "Number of Days",
      description: "The number of days to extend the license. Use negative numbers to decrease the expiration date.",
      required: !1
    })
  },
  async run(context) {
    let params = new URLSearchParams({
      ProductId: String(context.propsValue.productId),
      Key: context.propsValue.key
    });
    return context.propsValue.noOfDays !== void 0 && context.propsValue.noOfDays !== null && params.append("NoOfDays", String(context.propsValue.noOfDays)), await makeRequest(
      context.auth.secret_text,
      import_pieces_common6.HttpMethod.POST,
      `/key/ExtendLicense?${params.toString()}`
    );
  }
});

// ../ap/packages/pieces/community/cryptolens/src/lib/triggers/new-api-event.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var import_pieces_common8 = require("@activepieces/pieces-common"), props = {
  productId: import_pieces_framework6.Property.Number({
    displayName: "Product ID",
    description: "Filter events for a specific product (optional)",
    required: !1
  }),
  key: import_pieces_framework6.Property.ShortText({
    displayName: "License Key",
    description: "Filter events for a specific license key (requires Product ID)",
    required: !1
  }),
  states: import_pieces_framework6.Property.LongText({
    displayName: "Event States",
    description: "Filter by event states (comma-separated numbers). E.g., 1010 for validation, 2010 for activation, 3010 for key creation, 6010 for deactivation",
    required: !1
  }),
  limit: import_pieces_framework6.Property.Number({
    displayName: "Limit",
    description: "Maximum number of events to fetch per poll (default 100, max 1000)",
    required: !1
  })
}, polling = {
  strategy: import_pieces_common7.DedupeStrategy.TIMEBASED,
  items: async ({ propsValue, auth, lastFetchEpochMS }) => {
    let params = new URLSearchParams();
    propsValue.productId && params.append("ProductId", String(propsValue.productId)), propsValue.key && propsValue.productId && params.append("Key", propsValue.key), propsValue.states && params.append("States", propsValue.states);
    let limit = propsValue.limit || 100;
    if (params.append("Limit", String(limit)), lastFetchEpochMS) {
      let timeFilter = JSON.stringify({
        gte: Math.floor(lastFetchEpochMS / 1e3)
      });
      params.append("Time", timeFilter);
    } else {
      let oneHourAgo = Math.floor((Date.now() - 36e5) / 1e3), timeFilter = JSON.stringify({ gte: oneHourAgo });
      params.append("Time", timeFilter);
    }
    let responseBody = await makeRequest(
      auth.secret_text,
      import_pieces_common8.HttpMethod.GET,
      `/ai/GetWebAPILog?${params.toString()}`
    );
    return responseBody.result !== 0 || !responseBody.logs ? [] : responseBody.logs.map((log) => ({
      epochMilliSeconds: log.time * 1e3,
      data: log
    }));
  }
}, newApiEvent = (0, import_pieces_framework6.createTrigger)({
  auth: cryptolensAuth,
  name: "newApiEvent",
  displayName: "New API Event",
  description: "Trigger when a new API event is logged (activation, deactivation, key creation, etc.)",
  aiMetadata: {
    description: "Fires when a new Web API event is logged in Cryptolens, such as a license activation, deactivation, validation, or key creation. Polls the audit log on an interval and can optionally be scoped to a specific product, a specific key, or particular event-state codes."
  },
  outputSchema: newApiEventTriggerOutputSchema,
  props,
  sampleData: {
    id: 2280434406,
    productId: 12345,
    key: "AAAAA-BBBBB-CCCCC-DDDDD",
    ip: "203.0.113.10",
    time: 1788259071,
    state: 3010,
    machineCode: null,
    friendlyName: "",
    floatingExpires: 0,
    doIntValue: 0,
    doId: 0
  },
  type: import_pieces_framework6.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common7.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common7.pollingHelper.onEnable(polling, { store, auth, propsValue });
  },
  async onDisable(context) {
    let { store, auth, propsValue } = context;
    await import_pieces_common7.pollingHelper.onDisable(polling, { store, auth, propsValue });
  },
  async run(context) {
    return await import_pieces_common7.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/cryptolens/src/index.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework"), cryptolens = (0, import_pieces_framework7.createPiece)({
  displayName: "Cryptolens",
  auth: cryptolensAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/cryptolens.png",
  authors: ["sanket-a11y"],
  categories: [import_pieces_framework8.PieceCategory.DEVELOPER_TOOLS],
  description: "Cryptolens is a Software Licensing as a Service (SLaaS) platform that provides developers with tools to manage software licenses, protect against piracy, and analyze usage data.",
  actions: [
    addCustomer,
    blockKey,
    createKey,
    extendLicense,
    (0, import_pieces_common9.createCustomApiCallAction)({
      auth: cryptolensAuth,
      baseUrl: () => "https://api.cryptolens.io/api",
      authLocation: "queryParams",
      authMapping: async (auth) => ({
        token: `${auth.secret_text}`
      })
    })
  ],
  triggers: [newApiEvent]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  cryptolens
});
