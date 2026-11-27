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

// ../ap/packages/pieces/community/greip/src/index.ts
var index_exports = {};
__export(index_exports, {
  greip: () => greip
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/greip/src/lib/common/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), BASE_URL = "https://greipapi.com", greipAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To get your API Key:

1. Go to your Greip account dashboard
2. Navigate to API settings
3. Copy your API key
4. Paste it here
`,
  required: !0,
  validate: async ({ auth }) => {
    if (!auth)
      return {
        valid: !1,
        error: "API key is required"
      };
    try {
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${BASE_URL}/geoip`,
        headers: {
          Authorization: `Bearer ${auth}`,
          "Content-Type": "application/json"
        }
      })).status === 200 ? {
        valid: !0
      } : {
        valid: !1,
        error: "Invalid API key"
      };
    } catch (error) {
      return {
        valid: !1,
        error: "Invalid API key. Please check your API key and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/greip/src/lib/actions/asn-lookup.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/greip/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), BASE_URL2 = "https://greipapi.com";
async function greipApiCall({
  method,
  path,
  queryParams,
  body,
  auth
}) {
  var _a, _b, _c;
  let url = `${BASE_URL2}${path}`, headers = {
    Authorization: `Bearer ${auth.secret_text}`,
    "Content-Type": "application/json"
  }, qs = {};
  if (queryParams)
    for (let [key, value] of Object.entries(queryParams))
      value != null && (qs[key] = String(value));
  try {
    let response = await import_pieces_common2.httpClient.sendRequest({
      method,
      url,
      headers,
      queryParams: qs,
      body
    });
    if (response.body && typeof response.body == "object" && "status" in response.body && response.body.status === "error") {
      let errorBody = response.body;
      throw new Error(errorBody.description || `Greip API error (code: ${errorBody.code || "unknown"})`);
    }
    return response.body;
  } catch (error) {
    let errorBody = ((_a = error.response) == null ? void 0 : _a.body) || error.body;
    if (typeof errorBody == "string")
      try {
        errorBody = JSON.parse(errorBody);
      } catch (e) {
      }
    if (errorBody && typeof errorBody == "object") {
      if ((_b = errorBody.response) != null && _b.body && (errorBody = errorBody.response.body), "status" in errorBody && errorBody.status === "error") {
        let greipError = errorBody, description = greipError.description || getErrorMessageByCode(greipError.code);
        throw new Error(description || `Greip API error (code: ${greipError.code || "unknown"})`);
      }
      if ("description" in errorBody && errorBody.description)
        throw new Error(String(errorBody.description));
    }
    let statusCode = ((_c = error.response) == null ? void 0 : _c.status) || error.status;
    if (statusCode === 401 || statusCode === 403)
      throw new Error("Authentication failed. Please check your API key.");
    if (statusCode === 429)
      throw new Error("Rate limit exceeded. Please wait a moment and try again.");
    if (statusCode === 404)
      throw new Error("Endpoint not found. Please check the API endpoint path.");
    if (statusCode >= 400 && statusCode < 500) {
      let errorMessage = (errorBody == null ? void 0 : errorBody.description) || (errorBody == null ? void 0 : errorBody.message) || (errorBody == null ? void 0 : errorBody.error) || error.message || "Request failed";
      throw new Error(`Request failed: ${errorMessage}`);
    }
    let originalMessage = error.message || String(error);
    throw new Error(`Greip API error: ${originalMessage}`);
  }
}
function getErrorMessageByCode(code) {
  return {
    101: "The API Key is missing or invalid.",
    102: "The API Key owner account is inactive. Please contact support.",
    103: "You reached the usage limit of your account. Please upgrade your subscription.",
    104: "Invalid parameters. Please check the parameter values.",
    105: "Your plan has expired. Renew the subscription to enable using the API.",
    106: "Too many requests detected. Please slow down your request rate.",
    107: "The callback parameter value cannot be a function name.",
    108: "Invalid format. Use JSON, XML, CSV, or Newline.",
    109: "Callback feature can only be used with JSON format.",
    110: "Invalid language. Use EN, AR, FR, DE, ES, JA, ZH, or RU.",
    111: "Invalid mode. Use test or live.",
    112: "The IP Address is not valid or empty.",
    113: "Request sent from a domain that is not whitelisted in your API settings.",
    114: "Security module is not available in the free plan. Please upgrade.",
    115: "An error occurred while processing your request. Please try again later.",
    116: "The Country Code is invalid or not found.",
    117: "This feature is not available for your plan. Please upgrade.",
    118: "The Phone Number is invalid or missing.",
    119: "The Email Address is invalid or missing.",
    120: "The BIN number is invalid or missing.",
    121: "The AS Number is empty or invalid.",
    122: "The IBAN is invalid or missing.",
    123: "The user identifier is invalid or too long.",
    124: "The user type is invalid or missing. Use email, phone, or user_id.",
    125: "The user value is invalid or missing.",
    126: "You have reached the limit of deletions for this day. Please wait until the next day."
  }[code || 0] || "An unknown error occurred.";
}

// ../ap/packages/pieces/community/greip/src/lib/actions/asn-lookup.ts
var asnLookup = (0, import_pieces_framework2.createAction)({
  auth: greipAuth,
  name: "asn_lookup",
  displayName: "ASN Lookup",
  description: "Look up details of an Autonomous System Number (ASN)",
  audience: "both",
  aiMetadata: { description: 'Look up registration and network details for an Autonomous System Number (ASN), accepting the number with or without the "AS" prefix (e.g. AS6167 or 6167). Use to identify the organization, allocation, or routing info behind an ASN. Read-only lookup; safe to repeat.', idempotent: !0 },
  props: {
    asn: import_pieces_framework2.Property.ShortText({
      displayName: "AS Number",
      description: "The AS Number to lookup (e.g., AS6167 or 6167)",
      required: !0
    }),
    format: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Format of the response",
      required: !1,
      defaultValue: "JSON",
      options: {
        options: [
          { label: "JSON", value: "JSON" },
          { label: "XML", value: "XML" },
          { label: "CSV", value: "CSV" },
          { label: "Newline", value: "Newline" }
        ]
      }
    }),
    mode: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Environment",
      description: "Environment mode for testing or production",
      required: !1,
      defaultValue: "live",
      options: {
        options: [
          { label: "Live", value: "live" },
          { label: "Test", value: "test" }
        ]
      }
    }),
    callback: import_pieces_framework2.Property.ShortText({
      displayName: "JSONP Callback",
      description: "Function name for JSONP response format",
      required: !1
    })
  },
  async run(context) {
    let { asn, format, mode, callback } = context.propsValue, queryParams = {
      asn
    };
    return format && (queryParams.format = format), mode && (queryParams.mode = mode), callback && (queryParams.callback = callback), await greipApiCall({
      method: import_pieces_common3.HttpMethod.GET,
      path: "/lookup/asn",
      queryParams,
      auth: context.auth
    });
  }
});

// ../ap/packages/pieces/community/greip/src/lib/actions/bin-lookup.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var binLookup = (0, import_pieces_framework3.createAction)({
  auth: greipAuth,
  name: "bin_lookup",
  displayName: "BIN Lookup",
  description: "Look up details of a Bank Identification Number (BIN) or Issuer Identification Number (IIN)",
  audience: "both",
  aiMetadata: { description: "Look up the issuing bank, card scheme, type, and country behind a payment card from its BIN/IIN (the leading digits). Accepts either the first 6+ digits or a full card number (only the leading digits are used). Use for card validation or fraud-screening enrichment. Read-only lookup; safe to repeat.", idempotent: !0 },
  props: {
    bin: import_pieces_framework3.Property.ShortText({
      displayName: "BIN/IIN",
      description: 'The BIN/IIN of the card (minimum 6 digits). Can be partial like "456789" or full card number',
      required: !0
    }),
    format: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Format of the response",
      required: !1,
      defaultValue: "JSON",
      options: {
        options: [
          { label: "JSON", value: "JSON" },
          { label: "XML", value: "XML" },
          { label: "CSV", value: "CSV" },
          { label: "Newline", value: "Newline" }
        ]
      }
    }),
    mode: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Environment",
      description: "Environment mode for testing or production",
      required: !1,
      defaultValue: "live",
      options: {
        options: [
          { label: "Live", value: "live" },
          { label: "Test", value: "test" }
        ]
      }
    }),
    userID: import_pieces_framework3.Property.ShortText({
      displayName: "User Identifier",
      description: "Identify requests from specific users for monitoring (e.g., email, phone, user ID)",
      required: !1
    }),
    callback: import_pieces_framework3.Property.ShortText({
      displayName: "JSONP Callback",
      description: "Function name for JSONP response format",
      required: !1
    })
  },
  async run(context) {
    let { bin, format, mode, userID, callback } = context.propsValue, queryParams = {
      bin
    };
    return format && (queryParams.format = format), mode && (queryParams.mode = mode), userID && (queryParams.userID = userID), callback && (queryParams.callback = callback), await greipApiCall({
      method: import_pieces_common4.HttpMethod.GET,
      path: "/lookup/bin",
      queryParams,
      auth: context.auth
    });
  }
});

// ../ap/packages/pieces/community/greip/src/lib/actions/ip-lookup.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var ipLookup = (0, import_pieces_framework4.createAction)({
  auth: greipAuth,
  name: "ip_lookup",
  displayName: "IP Lookup",
  description: "Look up comprehensive information about an IP address including location, ISP, security, and risk factors",
  audience: "both",
  aiMetadata: { description: "Look up geolocation, ISP, timezone, currency, and security/risk signals for an IPv4 or IPv6 address. Optionally narrow the response to specific modules (security, currency, timezone, location) via a comma-separated list. Use to geolocate or fraud-screen an IP. Read-only lookup; safe to repeat.", idempotent: !0 },
  props: {
    ip: import_pieces_framework4.Property.ShortText({
      displayName: "IP Address",
      description: "The IP address to lookup (IPv4 or IPv6)",
      required: !0
    }),
    params: import_pieces_framework4.Property.ShortText({
      displayName: "Modules",
      description: 'Comma-separated list of modules to include: security, currency, timezone, location (e.g., "security,timezone,currency")',
      required: !1
    }),
    format: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Format of the response",
      required: !1,
      defaultValue: "JSON",
      options: {
        options: [
          { label: "JSON", value: "JSON" },
          { label: "XML", value: "XML" },
          { label: "CSV", value: "CSV" },
          { label: "Newline", value: "Newline" }
        ]
      }
    }),
    lang: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Response Language",
      description: "Language for the response",
      required: !1,
      defaultValue: "EN",
      options: {
        options: [
          { label: "English", value: "EN" },
          { label: "Arabic", value: "AR" },
          { label: "German", value: "DE" },
          { label: "French", value: "FR" },
          { label: "Spanish", value: "ES" },
          { label: "Japanese", value: "JA" },
          { label: "Chinese", value: "ZH" },
          { label: "Russian", value: "RU" }
        ]
      }
    }),
    mode: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Environment",
      description: "Environment mode for testing or production",
      required: !1,
      defaultValue: "live",
      options: {
        options: [
          { label: "Live", value: "live" },
          { label: "Test", value: "test" }
        ]
      }
    }),
    userID: import_pieces_framework4.Property.ShortText({
      displayName: "User Identifier",
      description: "Identify requests from specific users for monitoring (e.g., email, phone, user ID)",
      required: !1
    }),
    callback: import_pieces_framework4.Property.ShortText({
      displayName: "JSONP Callback",
      description: "Function name for JSONP response format",
      required: !1
    })
  },
  async run(context) {
    let { ip, params, format, lang, mode, userID, callback } = context.propsValue, queryParams = {
      ip
    };
    return params && (queryParams.params = params), format && (queryParams.format = format), lang && (queryParams.lang = lang), mode && (queryParams.mode = mode), userID && (queryParams.userID = userID), callback && (queryParams.callback = callback), await greipApiCall({
      method: import_pieces_common5.HttpMethod.GET,
      path: "/lookup/ip",
      queryParams,
      auth: context.auth
    });
  }
});

// ../ap/packages/pieces/community/greip/src/lib/actions/email-validation.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var emailValidation = (0, import_pieces_framework5.createAction)({
  auth: greipAuth,
  name: "email_validation",
  displayName: "Email Validation",
  description: "Validate email addresses by checking domain validity, detecting disposable emails, and assessing risk factors",
  audience: "both",
  aiMetadata: { description: "Validate and risk-score a single email address, checking domain/MX validity, disposable-email detection, and fraud indicators. Use to verify an email before signup, contact, or fraud screening. Read-only scoring call; safe to repeat.", idempotent: !0 },
  props: {
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email Address",
      description: "The email address to validate",
      required: !0
    }),
    format: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Format of the response",
      required: !1,
      defaultValue: "JSON",
      options: {
        options: [
          { label: "JSON", value: "JSON" },
          { label: "XML", value: "XML" },
          { label: "CSV", value: "CSV" },
          { label: "Newline", value: "Newline" }
        ]
      }
    }),
    mode: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Environment",
      description: "Environment mode for testing or production",
      required: !1,
      defaultValue: "live",
      options: {
        options: [
          { label: "Live", value: "live" },
          { label: "Test", value: "test" }
        ]
      }
    }),
    userID: import_pieces_framework5.Property.ShortText({
      displayName: "User Identifier",
      description: "Identify requests from specific users for monitoring (e.g., email, phone, user ID)",
      required: !1
    }),
    callback: import_pieces_framework5.Property.ShortText({
      displayName: "JSONP Callback",
      description: "Function name for JSONP response format",
      required: !1
    })
  },
  async run(context) {
    let { email, format, mode, userID, callback } = context.propsValue, queryParams = {
      email
    };
    return format && (queryParams.format = format), mode && (queryParams.mode = mode), userID && (queryParams.userID = userID), callback && (queryParams.callback = callback), await greipApiCall({
      method: import_pieces_common6.HttpMethod.GET,
      path: "/scoring/email",
      queryParams,
      auth: context.auth
    });
  }
});

// ../ap/packages/pieces/community/greip/src/lib/actions/profanity-detection.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var profanityDetection = (0, import_pieces_framework6.createAction)({
  auth: greipAuth,
  name: "profanity_detection",
  displayName: "Detect Profanity",
  description: "Detect offensive or inappropriate language in text using machine learning",
  audience: "both",
  aiMetadata: { description: "Scan a block of text for offensive or inappropriate language and return a risk/safety assessment. Can return just the score and safety flag, or additionally list the bad words found. Use to moderate user-generated content. Read-only scoring call; safe to repeat.", idempotent: !0 },
  props: {
    text: import_pieces_framework6.Property.LongText({
      displayName: "Text",
      description: "The text to check for profanity",
      required: !0
    }),
    scoreOnly: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Score Only",
      description: "Return only the score and safety status",
      required: !1,
      defaultValue: "no",
      options: {
        options: [
          { label: "No", value: "no" },
          { label: "Yes", value: "yes" }
        ]
      }
    }),
    listBadWords: import_pieces_framework6.Property.StaticDropdown({
      displayName: "List Bad Words",
      description: "Include a list of bad words found in the text",
      required: !1,
      defaultValue: "no",
      options: {
        options: [
          { label: "No", value: "no" },
          { label: "Yes", value: "yes" }
        ]
      }
    }),
    format: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Format of the response",
      required: !1,
      defaultValue: "JSON",
      options: {
        options: [
          { label: "JSON", value: "JSON" },
          { label: "XML", value: "XML" },
          { label: "CSV", value: "CSV" }
        ]
      }
    }),
    mode: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Environment",
      description: "Environment mode for testing or production",
      required: !1,
      defaultValue: "live",
      options: {
        options: [
          { label: "Live", value: "live" },
          { label: "Test", value: "test" }
        ]
      }
    }),
    callback: import_pieces_framework6.Property.ShortText({
      displayName: "JSONP Callback",
      description: "Function name for JSONP response format",
      required: !1
    })
  },
  async run(context) {
    let { text, scoreOnly, listBadWords, format, mode, callback } = context.propsValue, queryParams = {
      text
    };
    return scoreOnly && (queryParams.scoreOnly = scoreOnly), listBadWords && (queryParams.listBadWords = listBadWords), format && (queryParams.format = format), mode && (queryParams.mode = mode), callback && (queryParams.callback = callback), await greipApiCall({
      method: import_pieces_common7.HttpMethod.GET,
      path: "/scoring/profanity",
      queryParams,
      auth: context.auth
    });
  }
});

// ../ap/packages/pieces/community/greip/src/lib/actions/phone-validation.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var phoneValidation = (0, import_pieces_framework7.createAction)({
  auth: greipAuth,
  name: "phone_validation",
  displayName: "Phone Validation",
  description: "Validate phone numbers by checking syntax and assessing validity and operational status",
  audience: "both",
  aiMetadata: { description: "Validate a phone number against its country, checking syntax and reporting validity and operational status. Requires both the phone number (any common format) and the ISO 3166-1 alpha-2 country code (e.g. US, GB, FR). Use to verify a phone before contact or fraud screening. Read-only scoring call; safe to repeat.", idempotent: !0 },
  props: {
    phone: import_pieces_framework7.Property.ShortText({
      displayName: "Phone Number",
      description: "The phone number to validate (e.g., +12121234567, 0012121234567, 12121234567, or 2121234567)",
      required: !0
    }),
    countryCode: import_pieces_framework7.Property.ShortText({
      displayName: "Country Code",
      description: "ISO 3166-1 alpha-2 country code (e.g., US, GB, FR)",
      required: !0
    }),
    format: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Response Format",
      description: "Format of the response",
      required: !1,
      defaultValue: "JSON",
      options: {
        options: [
          { label: "JSON", value: "JSON" },
          { label: "XML", value: "XML" },
          { label: "CSV", value: "CSV" },
          { label: "Newline", value: "Newline" }
        ]
      }
    }),
    mode: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Environment",
      description: "Environment mode for testing or production",
      required: !1,
      defaultValue: "live",
      options: {
        options: [
          { label: "Live", value: "live" },
          { label: "Test", value: "test" }
        ]
      }
    }),
    userID: import_pieces_framework7.Property.ShortText({
      displayName: "User Identifier",
      description: "Identify requests from specific users for monitoring (e.g., email, phone, user ID)",
      required: !1
    }),
    callback: import_pieces_framework7.Property.ShortText({
      displayName: "JSONP Callback",
      description: "Function name for JSONP response format",
      required: !1
    })
  },
  async run(context) {
    let { phone, countryCode, format, mode, userID, callback } = context.propsValue, queryParams = {
      phone,
      countryCode
    };
    return format && (queryParams.format = format), mode && (queryParams.mode = mode), userID && (queryParams.userID = userID), callback && (queryParams.callback = callback), await greipApiCall({
      method: import_pieces_common8.HttpMethod.GET,
      path: "/scoring/phone",
      queryParams,
      auth: context.auth
    });
  }
});

// ../ap/packages/pieces/community/greip/src/lib/triggers/proxy-connection-detected.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var proxyConnectionDetectedTrigger = (0, import_pieces_framework8.createTrigger)({
  name: "proxy_connection_detected",
  displayName: "Proxy Connection Detected",
  description: "Triggers when a new proxy connection is detected",
  aiMetadata: {
    description: "Fires when Greip detects an incoming connection from a proxy, Tor, relay, bot, or hosting IP. Delivered via a Greip webhook (proxy_detected event) that must be configured manually in the Greip dashboard."
  },
  auth: greipAuth,
  props: {
    webhookInstructions: import_pieces_framework8.Property.MarkDown({
      value: `
To use this trigger, you need to manually set up a webhook in your Greip account:

1. Login to your Greip dashboard.
2. Go to Settings > Integrations > Webhooks.
3. Click on "Add Webhook" or "Create New Webhook".
4. Add the following URL in the **Webhook URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
5. Select **proxy_detected** from the event types.
6. Click Save to create the webhook.

**Note:** Webhooks are only available for paid subscriptions.
      `
    })
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  sampleData: {
    event: "proxy_detected",
    ip: "1.1.1.1",
    ipType: "IPv4",
    IPNumber: 16843009,
    countryCode: "US",
    countryGeoNameID: 6252001,
    countryName: "United States",
    security: {
      isProxy: !1,
      proxyType: null,
      isTor: !1,
      isBot: !1,
      isRelay: !1,
      isHosting: !1
    }
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/greip/src/lib/triggers/fraudulent-payment-detected.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var fraudulentPaymentDetectedTrigger = (0, import_pieces_framework9.createTrigger)({
  name: "fraudulent_payment_detected",
  displayName: "Fraudulent Payment Detected",
  description: "Triggers when a new fraudulent payment is detected by Greip",
  aiMetadata: {
    description: "Fires when Greip flags a payment as fraudulent, carrying the customer details, fraud score, and the rules that matched. Delivered via a Greip webhook (fraud_payment event) that must be configured manually in the Greip dashboard."
  },
  auth: greipAuth,
  props: {
    webhookInstructions: import_pieces_framework9.Property.MarkDown({
      value: `
To use this trigger, you need to manually set up a webhook in your Greip account:

1. Login to your Greip dashboard.
2. Go to Settings > Integrations > Webhooks.
3. Click on "Add Webhook" or "Create New Webhook".
4. Add the following URL in the **Webhook URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
5. Select **fraud_payment** from the event types.
6. Click Save to create the webhook.

**Note:** Webhooks are only available for paid subscriptions.
      `
    })
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  sampleData: {
    event: "fraud_payment",
    customer_id: "UID123",
    customer_email: "name@domain.com",
    customer_phone: "0555123456",
    score: 31.666666666666664,
    rules: [
      {
        id: "PF10001",
        description: "High purchase rate, according to `customer_ip`."
      },
      {
        id: "PF10002",
        description: "High purchase rate, according to `customer_id`."
      }
    ],
    rulesChecked: 6,
    rulesDetected: 2
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/greip/src/lib/triggers/profanity-text-detected.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var profanityTextDetectedTrigger = (0, import_pieces_framework10.createTrigger)({
  name: "profanity_text_detected",
  displayName: "Profanity Text Detected",
  description: "Triggers when Greip detects profanity in a specific text",
  aiMetadata: {
    description: "Fires when Greip detects profanity in submitted text, carrying the bad-word count, risk score, and safety flag. Delivered via a Greip webhook (profanity event) that must be configured manually in the Greip dashboard."
  },
  auth: greipAuth,
  props: {
    webhookInstructions: import_pieces_framework10.Property.MarkDown({
      value: `
To use this trigger, you need to manually set up a webhook in your Greip account:

1. Login to your Greip dashboard.
2. Go to Settings > Integrations > Webhooks.
3. Click on "Add Webhook" or "Create New Webhook".
4. Add the following URL in the **Webhook URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
5. Select **profanity** from the event types.
6. Click Save to create the webhook.

**Note:** Webhooks are only available for paid subscriptions.
      `
    })
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  sampleData: {
    event: "profanity",
    text: "**** *****",
    totalBadWords: 2,
    riskScore: 1,
    isSafe: !1
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/greip/src/lib/triggers/spam-email-detected.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var spamEmailDetectedTrigger = (0, import_pieces_framework11.createTrigger)({
  name: "spam_email_detected",
  displayName: "Spam Email Detected",
  description: "Triggers when a new email is marked as SPAM by Greip",
  aiMetadata: {
    description: "Fires when Greip marks an email address as spam/invalid, carrying the score and the reason it failed. Delivered via a Greip webhook (spam_email event) that must be configured manually in the Greip dashboard."
  },
  auth: greipAuth,
  props: {
    webhookInstructions: import_pieces_framework11.Property.MarkDown({
      value: `
To use this trigger, you need to manually set up a webhook in your Greip account:

1. Login to your Greip dashboard.
2. Go to Settings > Integrations > Webhooks.
3. Click on "Add Webhook" or "Create New Webhook".
4. Add the following URL in the **Webhook URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
5. Select **spam_email** from the event types.
6. Click Save to create the webhook.

**Note:** Webhooks are only available for paid subscriptions.
      `
    })
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  sampleData: {
    event: "spam_email",
    email: "name@domain.com",
    score: 3,
    reason: "Email domain's SPF record is not set properly.",
    isValid: !1
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/greip/src/lib/triggers/spam-phone-detected.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var spamPhoneDetectedTrigger = (0, import_pieces_framework12.createTrigger)({
  name: "spam_phone_detected",
  displayName: "Spam Phone Number Detected",
  description: "Triggers when a new phone number is marked as SPAM by Greip",
  aiMetadata: {
    description: "Fires when Greip marks a phone number as spam/invalid, carrying the country code, carrier, and the reason it failed. Delivered via a Greip webhook (spam_phone event) that must be configured manually in the Greip dashboard."
  },
  auth: greipAuth,
  props: {
    webhookInstructions: import_pieces_framework12.Property.MarkDown({
      value: `
To use this trigger, you need to manually set up a webhook in your Greip account:

1. Login to your Greip dashboard.
2. Go to Settings > Integrations > Webhooks.
3. Click on "Add Webhook" or "Create New Webhook".
4. Add the following URL in the **Webhook URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
5. Select **spam_phone** from the event types.
6. Click Save to create the webhook.

**Note:** Webhooks are only available for paid subscriptions.
      `
    })
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  sampleData: {
    event: "spam_phone",
    phone: "0555123456",
    countryCode: "ir",
    carrier: "",
    reason: "Invalid phone number structure.",
    isValid: !1
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/greip/src/index.ts
var greip = (0, import_pieces_framework13.createPiece)({
  displayName: "Greip",
  description: "Detect and prevent fraud in your website or app with Greip's Fraud Prevention API. Protect your business from financial losses and gain better insights into your users.",
  auth: greipAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/greip.png",
  categories: [import_pieces_framework14.PieceCategory.DEVELOPER_TOOLS],
  authors: ["onyedikachi-david"],
  actions: [asnLookup, binLookup, ipLookup, emailValidation, profanityDetection, phoneValidation],
  triggers: [
    proxyConnectionDetectedTrigger,
    fraudulentPaymentDetectedTrigger,
    profanityTextDetectedTrigger,
    spamEmailDetectedTrigger,
    spamPhoneDetectedTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  greip
});
