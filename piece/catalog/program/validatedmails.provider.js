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

// ../ap/packages/pieces/community/validatedmails/src/index.ts
var index_exports = {};
__export(index_exports, {
  validatedmails: () => validatedmails
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/validatedmails/src/lib/actions/validate-email.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/validatedmails/src/lib/common/auth.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework"), validatedMailsAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your ValidatedMails API key. You can find it in your ValidatedMails dashboard.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.validatedmails.com/api-keys/me",
        headers: {
          Authorization: `Bearer ${auth}`,
          "Content-Type": "application/json"
        },
        timeout: 5e3
      }), {
        valid: !0
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Unauthorized: Invalid API key"
      };
    }
  }
});

// ../ap/packages/pieces/community/validatedmails/src/lib/common/validate-email-helpers.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
var MIN_DNS_TIMEOUT_MS = 200, MAX_DNS_TIMEOUT_MS = 5e3, DEFAULT_DNS_TIMEOUT_MS = 1500, ERR_UNAUTHORIZED = "Unauthorized: Invalid API key", ERR_INSUFFICIENT_CREDITS = "Insufficient credits", ERR_RATE_LIMITED = "Rate limited", ERR_SERVICE_UNAVAILABLE = "Service unavailable", VALIDATE_URL = "https://api.validatedmails.com/validate", VALIDATE_URL_FALLBACK = "https://api.validatedmails.com/validate/";
function sanitizeEmail(email) {
  return email.trim();
}
function assertEmailInput(email) {
  if (!sanitizeEmail(email).includes("@"))
    throw new Error("Email address must contain @");
}
function normalizeDnsTimeoutMs(dnsTimeoutMs) {
  return dnsTimeoutMs === void 0 || Number.isNaN(dnsTimeoutMs) ? DEFAULT_DNS_TIMEOUT_MS : Math.min(Math.max(Math.round(dnsTimeoutMs), MIN_DNS_TIMEOUT_MS), MAX_DNS_TIMEOUT_MS);
}
function mapHttpStatusToError(status) {
  return status === 401 ? new Error(ERR_UNAUTHORIZED) : status === 402 ? new Error(ERR_INSUFFICIENT_CREDITS) : status === 429 ? new Error(ERR_RATE_LIMITED) : status >= 500 ? new Error(ERR_SERVICE_UNAVAILABLE) : new Error("Request failed");
}
function isTransportOrServerError(error) {
  if (!(error instanceof Error))
    return !1;
  let maybeResponse = error.response;
  return maybeResponse != null && maybeResponse.status && maybeResponse.status >= 500 ? !0 : !maybeResponse;
}
function getErrorStatus(error) {
  var _a;
  if (error instanceof Error)
    return (_a = error.response) == null ? void 0 : _a.status;
}
function shouldRetryWithFallbackUrl(error) {
  let status = getErrorStatus(error);
  return status === 307 || status === 308 || status === 404 || status === 405;
}
function toValidationOutput(body) {
  var _a, _b, _c, _d, _e, _f, _g, _h;
  return {
    is_valid: !!body.is_valid,
    score: Number((_a = body.score) != null ? _a : 0),
    email: String((_b = body.email) != null ? _b : ""),
    normalized: String((_c = body.normalized) != null ? _c : ""),
    state: String((_d = body.state) != null ? _d : ""),
    reason: String((_e = body.reason) != null ? _e : ""),
    domain: String((_f = body.domain) != null ? _f : ""),
    free: !!body.free,
    role: !!body.role,
    disposable: !!body.disposable,
    accept_all: !!body.accept_all,
    tag: !!body.tag,
    smtp_ok: !!body.smtp_ok,
    mx_record: typeof body.mx_record == "string" ? String(body.mx_record) : void 0,
    syntax_ok: !!body.syntax_ok,
    mx_ok: !!body.mx_ok,
    a_ok: !!body.a_ok,
    response_ms: Number((_g = body.response_ms) != null ? _g : 0),
    mx_hosts: Array.isArray(body.mx_hosts) ? body.mx_hosts.map((item) => String(item)) : [],
    status: asStatus(body.status),
    reasons: Array.isArray(body.reasons) ? body.reasons.map((item) => String(item)) : [],
    trace_id: String((_h = body.trace_id) != null ? _h : "")
  };
}
function asStatus(value) {
  return value === "valid" || value === "invalid" || value === "unknown" ? value : "unknown";
}
async function executeValidateEmailRequest(propsValue, apiKey, requestSender = import_pieces_common2.httpClient.sendRequest) {
  let sanitizedEmail = sanitizeEmail(propsValue.email);
  assertEmailInput(sanitizedEmail);
  let dnsTimeoutMs = normalizeDnsTimeoutMs(propsValue.dnsTimeoutMs), request = {
    headers: {
      Authorization: `Bearer ${apiKey}`,
      "Content-Type": "application/json"
    },
    timeout: 1e4
  }, send = async (url) => propsValue.mode === "GET" ? requestSender(__spreadProps(__spreadValues({
    method: import_pieces_common2.HttpMethod.GET,
    url
  }, request), {
    queryParams: {
      email: sanitizedEmail,
      dns_timeout_ms: String(dnsTimeoutMs)
    }
  })) : requestSender(__spreadProps(__spreadValues({
    method: import_pieces_common2.HttpMethod.POST,
    url
  }, request), {
    body: {
      email: sanitizedEmail,
      dns_timeout_ms: dnsTimeoutMs
    }
  })), sendWithFallbackUrl = async () => {
    try {
      return await send(VALIDATE_URL);
    } catch (error) {
      if (shouldRetryWithFallbackUrl(error))
        return send(VALIDATE_URL_FALLBACK);
      throw error;
    }
  };
  try {
    let response = await sendWithFallbackUrl();
    return toValidationOutput(response.body);
  } catch (error) {
    if (isTransportOrServerError(error))
      try {
        let retriedResponse = await sendWithFallbackUrl();
        return toValidationOutput(retriedResponse.body);
      } catch (retryError) {
        let status2 = getErrorStatus(retryError);
        throw status2 ? mapHttpStatusToError(status2) : new Error(ERR_SERVICE_UNAVAILABLE);
      }
    let status = getErrorStatus(error);
    throw status ? mapHttpStatusToError(status) : new Error("Request failed");
  }
}

// ../ap/packages/pieces/community/validatedmails/src/lib/actions/validate-email.ts
var validateEmail = (0, import_pieces_framework2.createAction)({
  auth: validatedMailsAuth,
  name: "validateEmail",
  displayName: "Validate Email",
  description: "Validate a single email address using the ValidatedMails API.",
  audience: "both",
  aiMetadata: { description: "Validates one email address against the ValidatedMails API and returns deliverability signals (syntax, MX, SMTP, disposable/role/free flags, score, and overall status). Use to verify whether an address is real and deliverable before sending to it or persisting it. Requires the email; the Mode option only switches the HTTP method (POST vs GET) used for the same lookup. Read-only and idempotent: re-running with the same address returns the same result with no side effect.", idempotent: !0 },
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email Address",
      description: "The email address to validate.",
      required: !0
    }),
    dnsTimeoutMs: import_pieces_framework2.Property.Number({
      displayName: "DNS Timeout (ms)",
      description: "DNS timeout in milliseconds. Value is clamped between 200 and 5000.",
      required: !1,
      defaultValue: 1500
    }),
    mode: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Mode",
      description: "HTTP method used for validation request.",
      required: !0,
      defaultValue: "POST",
      options: {
        options: [
          { label: "POST", value: "POST" },
          { label: "GET", value: "GET" }
        ]
      }
    })
  },
  async run(context) {
    return executeValidateEmailRequest(__spreadProps(__spreadValues({}, context.propsValue), {
      mode: context.propsValue.mode
    }), context.auth.secret_text);
  }
});

// ../ap/packages/pieces/community/validatedmails/src/index.ts
var validatedmails = (0, import_pieces_framework3.createPiece)({
  displayName: "ValidatedMails",
  auth: validatedMailsAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/validatedmails.png",
  categories: [import_pieces_framework4.PieceCategory.COMMUNICATION],
  description: "ValidatedMails validates email addresses in real time and returns status, score, and domain-level deliverability signals.",
  authors: ["fatiht8a"],
  actions: [
    validateEmail,
    (0, import_pieces_common3.createCustomApiCallAction)({
      auth: validatedMailsAuth,
      baseUrl: () => "https://api.validatedmails.com",
      authLocation: "headers",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  validatedmails
});
