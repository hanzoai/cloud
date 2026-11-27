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

// ../ap/packages/pieces/community/lobstermail/src/index.ts
var index_exports = {};
__export(index_exports, {
  lobstermail: () => lobstermail,
  lobstermailAuth: () => lobstermailAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common16 = require("@activepieces/pieces-common"), import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_framework17 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/lobstermail/src/lib/actions/create-inbox.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/lobstermail/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
var baseUrl = "https://api.lobstermail.ai";
async function fetchInboxes(auth) {
  var _a;
  return (_a = (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.GET,
    url: `${baseUrl}/v1/inboxes`,
    authentication: { type: import_pieces_common.AuthenticationType.BEARER_TOKEN, token: auth }
  })).body.data) != null ? _a : [];
}
var inboxIdDropdown = import_pieces_framework.Property.Dropdown({
  auth: lobstermailAuth,
  displayName: "Inbox",
  description: "Select the inbox to use.",
  refreshers: [],
  required: !0,
  options: async ({ auth }) => {
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your account first" };
    try {
      return {
        disabled: !1,
        options: (await fetchInboxes(auth.secret_text)).map((inbox) => ({
          label: inbox.displayName ? `${inbox.displayName} <${inbox.address}>` : inbox.address,
          value: inbox.id
        }))
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: "Failed to load inboxes. Check your API key." };
    }
  }
}), inboxIdDropdownOptional = import_pieces_framework.Property.Dropdown({
  auth: lobstermailAuth,
  displayName: "Inbox",
  description: "Limit results to a specific inbox. Leave empty to search all inboxes.",
  refreshers: [],
  required: !1,
  options: async ({ auth }) => {
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your account first" };
    try {
      return {
        disabled: !1,
        options: (await fetchInboxes(auth.secret_text)).map((inbox) => ({
          label: inbox.displayName ? `${inbox.displayName} <${inbox.address}>` : inbox.address,
          value: inbox.id
        }))
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: "Failed to load inboxes. Check your API key." };
    }
  }
}), fromAddressDropdown = import_pieces_framework.Property.Dropdown({
  auth: lobstermailAuth,
  displayName: "From",
  description: "Select the inbox to send from. Must be an active inbox on your account.",
  refreshers: [],
  required: !0,
  options: async ({ auth }) => {
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your account first" };
    try {
      return {
        disabled: !1,
        options: (await fetchInboxes(auth.secret_text)).map((inbox) => ({
          label: inbox.displayName ? `${inbox.displayName} <${inbox.address}>` : inbox.address,
          value: inbox.address
        }))
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: "Failed to load inboxes. Check your API key." };
    }
  }
}), lobstermailCommon = {
  baseUrl
};

// ../ap/packages/pieces/community/lobstermail/src/lib/actions/create-inbox.ts
var createInbox = (0, import_pieces_framework2.createAction)({
  auth: lobstermailAuth,
  name: "create_inbox",
  displayName: "Create Inbox",
  description: "Create a new email inbox on LobsterMail.",
  audience: "both",
  aiMetadata: { description: "Provisions a new LobsterMail email inbox and returns its id and address. Use when an agent needs a fresh address to send or receive mail. Optionally set a username/local-part and custom domain (custom domains need a Tier 2+ account); leave both empty to auto-generate a random address. Not idempotent \u2014 each call provisions a separate inbox.", idempotent: !1 },
  props: {
    displayName: import_pieces_framework2.Property.ShortText({
      displayName: "Display Name",
      description: 'A friendly label for the inbox (e.g. "Support Inbox"). Only visible inside LobsterMail.',
      required: !1
    }),
    localPart: import_pieces_framework2.Property.ShortText({
      displayName: "Email Username",
      description: 'The part before the @ in the email address (e.g. "support" \u2192 support@lobstermail.ai). Use only lowercase letters, numbers, dots, hyphens, or underscores. Leave empty to auto-generate a random address.',
      required: !1
    }),
    domain: import_pieces_framework2.Property.ShortText({
      displayName: "Domain",
      description: "The domain for the inbox address. Leave empty to use the default lobstermail.ai. Custom domains require a Tier 2+ account and prior DNS configuration.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let body = {};
    context.propsValue.displayName && (body.displayName = context.propsValue.displayName), context.propsValue.localPart && (body.localPart = context.propsValue.localPart), context.propsValue.domain && (body.domain = context.propsValue.domain);
    let inbox = (await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${lobstermailCommon.baseUrl}/v1/inboxes`,
      body,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body;
    return {
      id: inbox.id,
      address: inbox.address,
      local_part: inbox.localPart,
      domain: inbox.domain,
      display_name: (_a = inbox.displayName) != null ? _a : null,
      active: inbox.active,
      created_at: inbox.createdAt
    };
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/actions/get-inbox.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getInbox = (0, import_pieces_framework3.createAction)({
  auth: lobstermailAuth,
  name: "get_inbox",
  displayName: "Get Inbox",
  description: "Get details for a single inbox including its address, status, and settings.",
  audience: "both",
  aiMetadata: { description: "Looks up one LobsterMail inbox by its id and returns its address, active status, and settings. Use to resolve or verify an inbox before sending or reading mail. Requires a known inbox id (obtain it via List Inboxes). Read-only and idempotent.", idempotent: !0 },
  props: {
    inbox_id: inboxIdDropdown
  },
  async run(context) {
    var _a, _b;
    let inbox = (await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: `${lobstermailCommon.baseUrl}/v1/inboxes/${context.propsValue.inbox_id}`,
      authentication: {
        type: import_pieces_common3.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body;
    return {
      id: inbox.id,
      address: inbox.address,
      local_part: inbox.localPart,
      domain: inbox.domain,
      display_name: (_a = inbox.displayName) != null ? _a : null,
      active: inbox.active,
      auto_extract: (_b = inbox.autoExtract) != null ? _b : null,
      created_at: inbox.createdAt
    };
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/actions/delete-inbox.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var deleteInbox = (0, import_pieces_framework4.createAction)({
  auth: lobstermailAuth,
  name: "delete_inbox",
  displayName: "Delete Inbox",
  description: "Permanently delete an inbox and release its address. This action cannot be undone.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a LobsterMail inbox by its id and frees its address; this is destructive and cannot be undone. Use only when an agent is certain the inbox and its mail are no longer needed. Requires a known inbox id. Not idempotent \u2014 a repeat call against an already-deleted inbox will fail rather than succeed silently.", idempotent: !1 },
  props: {
    inbox_id: inboxIdDropdown
  },
  async run(context) {
    return await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.DELETE,
      url: `${lobstermailCommon.baseUrl}/v1/inboxes/${context.propsValue.inbox_id}`,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    }), { success: !0, deleted_inbox_id: context.propsValue.inbox_id };
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/actions/list-inboxes.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var listInboxes = (0, import_pieces_framework5.createAction)({
  auth: lobstermailAuth,
  name: "list_inboxes",
  displayName: "List Inboxes",
  description: "List all email inboxes on the account.",
  audience: "both",
  aiMetadata: { description: "Returns every email inbox on the authenticated LobsterMail account, each with its id and address. Use to discover which inboxes exist or to find an inbox id needed by other actions. Takes no input. Read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    var _a;
    return ((_a = (await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.GET,
      url: `${lobstermailCommon.baseUrl}/v1/inboxes`,
      authentication: {
        type: import_pieces_common5.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body.data) != null ? _a : []).map((inbox) => {
      var _a2;
      return {
        id: inbox.id,
        address: inbox.address,
        local_part: inbox.localPart,
        domain: inbox.domain,
        display_name: (_a2 = inbox.displayName) != null ? _a2 : null,
        active: inbox.active,
        created_at: inbox.createdAt
      };
    });
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/actions/send-email.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var sendEmail = (0, import_pieces_framework6.createAction)({
  auth: lobstermailAuth,
  name: "send_email",
  displayName: "Send Email",
  description: "Send an email from one of your LobsterMail inboxes. Requires a verified account (Tier 1+).",
  audience: "both",
  aiMetadata: { description: "Sends an email from one of the account inboxes to up to 50 recipients. Can start a fresh message or continue an existing conversation by passing a thread id and/or an In-Reply-To message id. Requires a verified account (Tier 1+), a sending inbox, recipients, a subject, and at least one of a plain-text or HTML body. Not idempotent \u2014 each call dispatches a new message.", idempotent: !1 },
  props: {
    from: fromAddressDropdown,
    to: import_pieces_framework6.Property.Array({
      displayName: "To",
      description: "Recipient email addresses (up to 50).",
      required: !0
    }),
    subject: import_pieces_framework6.Property.ShortText({
      displayName: "Subject",
      description: "Email subject line.",
      required: !0
    }),
    body_text: import_pieces_framework6.Property.LongText({
      displayName: "Body (Plain Text)",
      description: "Plain text version of the email. At least one of Plain Text or HTML body is required.",
      required: !1
    }),
    body_html: import_pieces_framework6.Property.LongText({
      displayName: "Body (HTML)",
      description: "HTML version of the email. If only HTML is provided, a plain-text version is auto-generated.",
      required: !1
    }),
    cc: import_pieces_framework6.Property.Array({
      displayName: "CC",
      description: "Additional recipients to CC on the email.",
      required: !1
    }),
    in_reply_to: import_pieces_framework6.Property.ShortText({
      displayName: "In-Reply-To Message ID",
      description: 'Message-ID of the email you are replying to (for threading). You can get this from the "Get Email" action output.',
      required: !1
    }),
    thread_id: import_pieces_framework6.Property.ShortText({
      displayName: "Thread ID",
      description: "ID of an existing thread to add this email to. Leave empty to start a new thread.",
      required: !1
    })
  },
  async run(context) {
    let { from, to, subject, body_text, body_html, cc, in_reply_to, thread_id } = context.propsValue;
    if (!body_text && !body_html)
      throw new Error(
        'At least one of "Body (Plain Text)" or "Body (HTML)" must be provided.'
      );
    let payload = { from, to, subject }, bodyObj = {};
    return body_text && (bodyObj.text = body_text), body_html && (bodyObj.html = body_html), payload.body = bodyObj, cc && cc.length > 0 && (payload.cc = cc), in_reply_to && (payload.inReplyTo = in_reply_to), thread_id && (payload.threadId = thread_id), (await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.POST,
      url: `${lobstermailCommon.baseUrl}/v1/emails/send`,
      body: payload,
      authentication: {
        type: import_pieces_common6.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/actions/list-emails.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var listEmails = (0, import_pieces_framework7.createAction)({
  auth: lobstermailAuth,
  name: "list_emails",
  displayName: "List Emails",
  description: "List emails in a specific inbox.",
  audience: "both",
  aiMetadata: { description: "Lists emails in a single inbox, newest first, returning metadata and previews but not full bodies. Optionally narrow by direction (inbound received vs. outbound sent), restrict to unread only, and cap the count (1-50). Requires a known inbox id; use Get Email to fetch a full message body. Read-only and idempotent.", idempotent: !0 },
  props: {
    inbox_id: inboxIdDropdown,
    direction: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Direction",
      description: "Filter by inbound (received) or outbound (sent) emails.",
      required: !1,
      options: {
        options: [
          { label: "Inbound (received)", value: "inbound" },
          { label: "Outbound (sent)", value: "outbound" }
        ]
      }
    }),
    unread_only: import_pieces_framework7.Property.Checkbox({
      displayName: "Unread Only",
      description: "Return only unread emails.",
      required: !1,
      defaultValue: !1
    }),
    limit: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      description: "Maximum number of emails to return (1\u201350, default 20).",
      required: !1,
      defaultValue: 20
    })
  },
  async run(context) {
    var _a;
    let { inbox_id, direction, unread_only, limit } = context.propsValue, params = new URLSearchParams();
    return limit && params.set("limit", String(limit)), direction && params.set("direction", direction), unread_only && params.set("unread", "true"), ((_a = (await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `${lobstermailCommon.baseUrl}/v1/inboxes/${inbox_id}/emails?${params.toString()}`,
      authentication: {
        type: import_pieces_common7.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body.data) != null ? _a : []).map((email) => {
      var _a2, _b, _c, _d;
      return {
        id: email.id,
        inbox_id: email.inboxId,
        from: email.from,
        to: Array.isArray(email.to) ? email.to.join(", ") : email.to,
        subject: email.subject,
        preview: (_a2 = email.preview) != null ? _a2 : null,
        thread_id: (_b = email.threadId) != null ? _b : null,
        direction: email.direction,
        created_at: email.createdAt,
        is_injection_risk: (_c = email.isInjectionRisk) != null ? _c : null,
        injection_risk_score: (_d = email.injectionRiskScore) != null ? _d : null
      };
    });
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/actions/get-email.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var getEmail = (0, import_pieces_framework8.createAction)({
  auth: lobstermailAuth,
  name: "get_email",
  displayName: "Get Email",
  description: "Get a single email with full body content.",
  audience: "both",
  aiMetadata: { description: "Retrieves one email by its id within an inbox, including the full plain-text and HTML body, authentication results (SPF/DKIM/DMARC), and prompt-injection risk signals. Use when an agent needs the complete message content rather than the preview returned by List Emails or Search Emails. Requires both the inbox id and the email id. Read-only and idempotent.", idempotent: !0 },
  props: {
    inbox_id: inboxIdDropdown,
    email_id: import_pieces_framework8.Property.ShortText({
      displayName: "Email ID",
      description: 'The ID of the email to retrieve (starts with eml_). You can get this from the "List Emails" action or the "New Email Received" trigger output.',
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l;
    let { inbox_id, email_id } = context.propsValue, email = (await import_pieces_common8.httpClient.sendRequest({
      method: import_pieces_common8.HttpMethod.GET,
      url: `${lobstermailCommon.baseUrl}/v1/inboxes/${inbox_id}/emails/${email_id}`,
      authentication: {
        type: import_pieces_common8.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body;
    return {
      id: email.id,
      inbox_id: email.inboxId,
      from: email.from,
      to: Array.isArray(email.to) ? email.to.join(", ") : email.to,
      cc: Array.isArray(email.cc) ? email.cc.join(", ") : (_a = email.cc) != null ? _a : null,
      subject: email.subject,
      body_text: (_c = (_b = email.body) == null ? void 0 : _b.text) != null ? _c : null,
      body_html: (_e = (_d = email.body) == null ? void 0 : _d.html) != null ? _e : null,
      preview: (_f = email.preview) != null ? _f : null,
      thread_id: (_g = email.threadId) != null ? _g : null,
      direction: email.direction,
      created_at: email.createdAt,
      is_injection_risk: (_h = email.isInjectionRisk) != null ? _h : null,
      injection_risk_score: (_i = email.injectionRiskScore) != null ? _i : null,
      spf: (_j = email.spf) != null ? _j : null,
      dkim: (_k = email.dkim) != null ? _k : null,
      dmarc: (_l = email.dmarc) != null ? _l : null
    };
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/actions/search-emails.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var searchEmails = (0, import_pieces_framework9.createAction)({
  auth: lobstermailAuth,
  name: "search_emails",
  displayName: "Search Emails",
  description: "Search emails across inboxes using full-text search and optional filters. Rate limited to 30 requests/minute.",
  audience: "both",
  aiMetadata: { description: "Full-text searches emails across all inboxes (or a single inbox if one is given), matching against subjects, senders, and body previews. Use to find messages by content when the email id is unknown; optionally filter by direction, sender, or attachment presence and cap results (1-50). Requires a non-empty query string and is rate limited to 30 requests/minute. Read-only and idempotent.", idempotent: !0 },
  props: {
    query: import_pieces_framework9.Property.ShortText({
      displayName: "Search Query",
      description: "Text to search for. Searches email subjects (highest priority), senders, and body previews.",
      required: !0
    }),
    inbox_id: inboxIdDropdownOptional,
    direction: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Direction",
      description: "Filter by inbound (received) or outbound (sent) emails.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          { label: "Inbound (received)", value: "inbound" },
          { label: "Outbound (sent)", value: "outbound" }
        ]
      }
    }),
    from: import_pieces_framework9.Property.ShortText({
      displayName: "Sender Filter",
      description: "Filter results to emails from a specific sender address (partial match supported).",
      required: !1
    }),
    has_attachments: import_pieces_framework9.Property.Checkbox({
      displayName: "Has Attachments",
      description: "Return only emails that have attachments.",
      required: !1,
      defaultValue: !1
    }),
    limit: import_pieces_framework9.Property.Number({
      displayName: "Limit",
      description: "Maximum number of results to return (1\u201350, default 20).",
      required: !1,
      defaultValue: 20
    })
  },
  async run(context) {
    var _a;
    let { query, inbox_id, direction, from, has_attachments, limit } = context.propsValue, params = new URLSearchParams();
    return params.set("q", query), inbox_id && params.set("inboxId", inbox_id), direction && params.set("direction", direction), from && params.set("from", from), has_attachments && params.set("hasAttachments", "true"), limit && params.set("limit", String(limit)), ((_a = (await import_pieces_common9.httpClient.sendRequest({
      method: import_pieces_common9.HttpMethod.GET,
      url: `${lobstermailCommon.baseUrl}/v1/emails/search?${params.toString()}`,
      authentication: {
        type: import_pieces_common9.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body.data) != null ? _a : []).map((email) => {
      var _a2, _b;
      return {
        id: email.id,
        inbox_id: email.inboxId,
        from: email.from,
        to: Array.isArray(email.to) ? email.to.join(", ") : email.to,
        subject: email.subject,
        preview: (_a2 = email.preview) != null ? _a2 : null,
        thread_id: (_b = email.threadId) != null ? _b : null,
        direction: email.direction,
        created_at: email.createdAt
      };
    });
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/actions/get-account.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var getAccount = (0, import_pieces_framework10.createAction)({
  auth: lobstermailAuth,
  name: "get_account",
  displayName: "Get Account",
  description: "Get account details including subscription tier, usage, and sending limits.",
  audience: "both",
  aiMetadata: { description: "Returns the authenticated LobsterMail account profile: subscription tier, verification status, current usage, and sending/inbox limits. Use to check capabilities or remaining quota before creating inboxes or sending mail. Takes no input. Read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
    let account = (await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: `${lobstermailCommon.baseUrl}/v1/account`,
      authentication: {
        type: import_pieces_common10.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    })).body;
    return {
      tier: account.tier,
      tier_name: (_a = account.tierName) != null ? _a : null,
      verified: account.verified,
      usage_emails_this_month: (_c = (_b = account.usage) == null ? void 0 : _b.emailsThisMonth) != null ? _c : null,
      usage_sends_today: (_e = (_d = account.usage) == null ? void 0 : _d.sendsToday) != null ? _e : null,
      limit_max_inboxes: (_g = (_f = account.limits) == null ? void 0 : _f.maxInboxes) != null ? _g : null,
      limit_sends_per_day: (_i = (_h = account.limits) == null ? void 0 : _h.sendsPerDay) != null ? _i : null,
      limit_emails_per_month: (_k = (_j = account.limits) == null ? void 0 : _j.emailsPerMonth) != null ? _k : null
    };
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/triggers/new-email.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var newEmailTrigger = (0, import_pieces_framework11.createTrigger)({
  auth: lobstermailAuth,
  name: "new_email",
  displayName: "New Email Received",
  description: "Triggers instantly when a new email is received in an inbox.",
  aiMetadata: {
    description: "Fires via webhook the moment an inbound email arrives, optionally scoped to a single inbox. Each event represents one received message with sender, recipients, subject, preview, thread id, and an injection-risk score; fetch the full body with Get Email."
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  props: {
    inbox_id: inboxIdDropdownOptional
  },
  sampleData: {
    event: "email.received",
    email_id: "eml_sample123",
    inbox_id: "ibx_sample456",
    from: "sender@example.com",
    to: "you@lobstermail.ai",
    subject: "Hello from LobsterMail",
    preview: "This is a sample email body.",
    thread_id: null,
    injection_risk_score: 0,
    received_at: "2026-01-01T00:00:00.000Z"
  },
  async onEnable(context) {
    let body = {
      url: context.webhookUrl,
      events: ["email.received"]
    };
    context.propsValue.inbox_id && (body.inboxId = context.propsValue.inbox_id);
    let response = await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.POST,
      url: `${lobstermailCommon.baseUrl}/v1/webhooks`,
      body,
      authentication: {
        type: import_pieces_common11.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
    await context.store.put("webhookId", response.body.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.DELETE,
      url: `${lobstermailCommon.baseUrl}/v1/webhooks/${webhookId}`,
      authentication: {
        type: import_pieces_common11.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
    let payload = context.payload.body, data = (_a = payload.data) != null ? _a : {};
    return payload.event !== "email.received" ? [] : [
      {
        event: payload.event,
        email_id: (_b = data.id) != null ? _b : null,
        inbox_id: (_c = data.inboxId) != null ? _c : null,
        from: (_d = data.sender) != null ? _d : null,
        to: Array.isArray(data.recipients) ? data.recipients.join(", ") : (_e = data.recipients) != null ? _e : null,
        subject: (_f = data.subject) != null ? _f : null,
        preview: (_g = data.preview) != null ? _g : null,
        thread_id: (_h = data.threadId) != null ? _h : null,
        injection_risk_score: (_i = data.injectionRiskScore) != null ? _i : null,
        received_at: (_k = (_j = data.receivedAt) != null ? _j : payload.timestamp) != null ? _k : null
      }
    ];
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/triggers/new-thread.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var newThreadTrigger = (0, import_pieces_framework12.createTrigger)({
  auth: lobstermailAuth,
  name: "new_thread",
  displayName: "New Email Thread",
  description: "Triggers when a brand-new conversation (thread) is started in an inbox.",
  aiMetadata: {
    description: "Fires via webhook when an inbound email starts a brand-new conversation thread (not a reply to an existing one), optionally scoped to a single inbox. Each event represents the opening message of a new thread with its thread id, sender, recipients, subject, and preview. Use to react only to first-contact messages rather than every email."
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  props: {
    inbox_id: inboxIdDropdownOptional
  },
  sampleData: {
    event: "email.thread.new",
    thread_id: "thr_sample123",
    inbox_id: "ibx_sample456",
    subject: "Question about your product",
    from: "customer@example.com",
    to: "support@lobstermail.ai",
    preview: "Hi, I wanted to ask about...",
    injection_risk_score: 0,
    received_at: "2026-01-01T00:00:00.000Z"
  },
  async onEnable(context) {
    let body = {
      url: context.webhookUrl,
      events: ["email.thread.new"]
    };
    context.propsValue.inbox_id && (body.inboxId = context.propsValue.inbox_id);
    let response = await import_pieces_common12.httpClient.sendRequest({
      method: import_pieces_common12.HttpMethod.POST,
      url: `${lobstermailCommon.baseUrl}/v1/webhooks`,
      body,
      authentication: {
        type: import_pieces_common12.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
    await context.store.put("webhookId", response.body.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await import_pieces_common12.httpClient.sendRequest({
      method: import_pieces_common12.HttpMethod.DELETE,
      url: `${lobstermailCommon.baseUrl}/v1/webhooks/${webhookId}`,
      authentication: {
        type: import_pieces_common12.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
    let payload = context.payload.body, data = (_a = payload.data) != null ? _a : {};
    return payload.event !== "email.thread.new" ? [] : [
      {
        event: payload.event,
        thread_id: (_b = data.threadId) != null ? _b : null,
        inbox_id: (_c = data.inboxId) != null ? _c : null,
        subject: (_d = data.subject) != null ? _d : null,
        from: (_e = data.sender) != null ? _e : null,
        to: Array.isArray(data.recipients) ? data.recipients.join(", ") : (_f = data.recipients) != null ? _f : null,
        preview: (_g = data.preview) != null ? _g : null,
        injection_risk_score: (_h = data.injectionRiskScore) != null ? _h : null,
        received_at: (_j = (_i = data.receivedAt) != null ? _i : payload.timestamp) != null ? _j : null
      }
    ];
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/triggers/thread-reply.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var threadReplyTrigger = (0, import_pieces_framework13.createTrigger)({
  auth: lobstermailAuth,
  name: "thread_reply",
  displayName: "Thread Reply Received",
  description: "Triggers when someone replies to an existing email thread. Useful for follow-up and support automation.",
  aiMetadata: {
    description: "Fires via webhook when an inbound reply lands on an existing email thread, optionally scoped to a single inbox. Each event represents one follow-up message carrying both its email id and the parent thread id. Use to drive follow-up or support automation that should run only on replies, not on new conversations."
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  props: {
    inbox_id: inboxIdDropdownOptional
  },
  sampleData: {
    event: "email.thread.reply",
    email_id: "eml_sample789",
    thread_id: "thr_sample123",
    inbox_id: "ibx_sample456",
    subject: "Re: Question about your product",
    from: "customer@example.com",
    to: "support@lobstermail.ai",
    preview: "Thanks for getting back to me...",
    injection_risk_score: 0,
    received_at: "2026-01-01T01:00:00.000Z"
  },
  async onEnable(context) {
    let body = {
      url: context.webhookUrl,
      events: ["email.thread.reply"]
    };
    context.propsValue.inbox_id && (body.inboxId = context.propsValue.inbox_id);
    let response = await import_pieces_common13.httpClient.sendRequest({
      method: import_pieces_common13.HttpMethod.POST,
      url: `${lobstermailCommon.baseUrl}/v1/webhooks`,
      body,
      authentication: {
        type: import_pieces_common13.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
    await context.store.put("webhookId", response.body.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await import_pieces_common13.httpClient.sendRequest({
      method: import_pieces_common13.HttpMethod.DELETE,
      url: `${lobstermailCommon.baseUrl}/v1/webhooks/${webhookId}`,
      authentication: {
        type: import_pieces_common13.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
    let payload = context.payload.body, data = (_a = payload.data) != null ? _a : {};
    return payload.event !== "email.thread.reply" ? [] : [
      {
        event: payload.event,
        email_id: (_b = data.id) != null ? _b : null,
        thread_id: (_c = data.threadId) != null ? _c : null,
        inbox_id: (_d = data.inboxId) != null ? _d : null,
        subject: (_e = data.subject) != null ? _e : null,
        from: (_f = data.sender) != null ? _f : null,
        to: Array.isArray(data.recipients) ? data.recipients.join(", ") : (_g = data.recipients) != null ? _g : null,
        preview: (_h = data.preview) != null ? _h : null,
        injection_risk_score: (_i = data.injectionRiskScore) != null ? _i : null,
        received_at: (_k = (_j = data.receivedAt) != null ? _j : payload.timestamp) != null ? _k : null
      }
    ];
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/triggers/email-bounced.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var emailBouncedTrigger = (0, import_pieces_framework14.createTrigger)({
  auth: lobstermailAuth,
  name: "email_bounced",
  displayName: "Email Bounced",
  description: "Triggers when an outbound email fails to deliver. Use this to update CRM records, retry with a different address, or alert your team.",
  aiMetadata: {
    description: "Fires via webhook when a previously sent outbound email fails to deliver, optionally scoped to a single inbox. Each event represents one bounce with the failed message id, recipient, and a bounce type (e.g. hard vs. soft) and reason. Use to flag bad addresses, retry delivery, or alert on delivery failures."
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  props: {
    inbox_id: inboxIdDropdownOptional
  },
  sampleData: {
    event: "email.bounced",
    email_id: "eml_sample123",
    inbox_id: "ibx_sample456",
    from: "support@lobstermail.ai",
    to: "invalid@example.com",
    subject: "Your order confirmation",
    bounce_type: "hard",
    bounce_reason: "User unknown",
    bounced_at: "2026-01-01T00:00:00.000Z"
  },
  async onEnable(context) {
    let body = {
      url: context.webhookUrl,
      events: ["email.bounced"]
    };
    context.propsValue.inbox_id && (body.inboxId = context.propsValue.inbox_id);
    let response = await import_pieces_common14.httpClient.sendRequest({
      method: import_pieces_common14.HttpMethod.POST,
      url: `${lobstermailCommon.baseUrl}/v1/webhooks`,
      body,
      authentication: {
        type: import_pieces_common14.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
    await context.store.put("webhookId", response.body.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await import_pieces_common14.httpClient.sendRequest({
      method: import_pieces_common14.HttpMethod.DELETE,
      url: `${lobstermailCommon.baseUrl}/v1/webhooks/${webhookId}`,
      authentication: {
        type: import_pieces_common14.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
    let payload = context.payload.body, data = (_a = payload.data) != null ? _a : {};
    return payload.event !== "email.bounced" ? [] : [
      {
        event: payload.event,
        email_id: (_b = data.id) != null ? _b : null,
        inbox_id: (_c = data.inboxId) != null ? _c : null,
        from: (_d = data.sender) != null ? _d : null,
        to: Array.isArray(data.recipients) ? data.recipients.join(", ") : (_e = data.recipients) != null ? _e : null,
        subject: (_f = data.subject) != null ? _f : null,
        bounce_type: (_g = data.bounceType) != null ? _g : null,
        bounce_reason: (_h = data.bounceReason) != null ? _h : null,
        bounced_at: (_j = (_i = data.bouncedAt) != null ? _i : payload.timestamp) != null ? _j : null
      }
    ];
  }
});

// ../ap/packages/pieces/community/lobstermail/src/lib/triggers/email-sent.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var emailSentTrigger = (0, import_pieces_framework15.createTrigger)({
  auth: lobstermailAuth,
  name: "email_sent",
  displayName: "Email Sent",
  description: "Triggers when an outbound email is successfully delivered. Use for audit logs, CRM updates, or confirming delivery.",
  aiMetadata: {
    description: "Fires via webhook when an outbound email is successfully delivered, optionally scoped to a single inbox. Each event represents one confirmed delivery with the message id, recipients, subject, and thread id. Use for delivery confirmation, audit logging, or downstream CRM updates."
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  props: {
    inbox_id: inboxIdDropdownOptional
  },
  sampleData: {
    event: "email.sent",
    email_id: "eml_sample123",
    inbox_id: "ibx_sample456",
    from: "support@lobstermail.ai",
    to: "customer@example.com",
    subject: "Your order confirmation",
    thread_id: null,
    sent_at: "2026-01-01T00:00:00.000Z"
  },
  async onEnable(context) {
    let body = {
      url: context.webhookUrl,
      events: ["email.sent"]
    };
    context.propsValue.inbox_id && (body.inboxId = context.propsValue.inbox_id);
    let response = await import_pieces_common15.httpClient.sendRequest({
      method: import_pieces_common15.HttpMethod.POST,
      url: `${lobstermailCommon.baseUrl}/v1/webhooks`,
      body,
      authentication: {
        type: import_pieces_common15.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
    await context.store.put("webhookId", response.body.id);
  },
  async onDisable(context) {
    let webhookId = await context.store.get("webhookId");
    webhookId && await import_pieces_common15.httpClient.sendRequest({
      method: import_pieces_common15.HttpMethod.DELETE,
      url: `${lobstermailCommon.baseUrl}/v1/webhooks/${webhookId}`,
      authentication: {
        type: import_pieces_common15.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i;
    let payload = context.payload.body, data = (_a = payload.data) != null ? _a : {};
    return payload.event !== "email.sent" ? [] : [
      {
        event: payload.event,
        email_id: (_b = data.id) != null ? _b : null,
        inbox_id: (_c = data.inboxId) != null ? _c : null,
        from: (_d = data.sender) != null ? _d : null,
        to: Array.isArray(data.recipients) ? data.recipients.join(", ") : (_e = data.recipients) != null ? _e : null,
        subject: (_f = data.subject) != null ? _f : null,
        thread_id: (_g = data.threadId) != null ? _g : null,
        sent_at: (_i = (_h = data.sentAt) != null ? _h : payload.timestamp) != null ? _i : null
      }
    ];
  }
});

// ../ap/packages/pieces/community/lobstermail/src/index.ts
var lobstermailAuth = import_pieces_framework16.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: `To get your LobsterMail API key:
1. Sign up or log in at **https://lobstermail.ai**
2. Go to **Settings \u2192 API Keys**
3. Click **Create New Key**
4. Copy the key \u2014 it starts with \`lm_sk_test_\` (sandbox) or \`lm_sk_live_\` (production)

Need help? See https://lobstermail.ai/docs`
}), lobstermail = (0, import_pieces_framework16.createPiece)({
  displayName: "LobsterMail",
  description: "Email infrastructure for AI agents \u2014 create inboxes, send and receive email, search, threads, and webhooks.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/lobstermail.png",
  authors: ["ibussieres"],
  categories: [import_pieces_framework17.PieceCategory.COMMUNICATION],
  auth: lobstermailAuth,
  actions: [
    createInbox,
    getInbox,
    deleteInbox,
    listInboxes,
    sendEmail,
    listEmails,
    getEmail,
    searchEmails,
    getAccount,
    (0, import_pieces_common16.createCustomApiCallAction)({
      baseUrl: () => lobstermailCommon.baseUrl,
      auth: lobstermailAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [
    newEmailTrigger,
    newThreadTrigger,
    threadReplyTrigger,
    emailBouncedTrigger,
    emailSentTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  lobstermail,
  lobstermailAuth
});
