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

// ../ap/packages/pieces/community/postmark/src/index.ts
var index_exports = {};
__export(index_exports, {
  postmark: () => postmark
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_framework7 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/postmark/src/lib/actions/get-delivery-stats.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/postmark/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/postmark/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), POSTMARK_API_BASE_URL = "https://api.postmarkapp.com";
async function postmarkRequest({
  method,
  path,
  apiToken,
  body,
  queryParams
}) {
  var _a, _b, _c, _d;
  try {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${POSTMARK_API_BASE_URL}${path}`,
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
        "X-Postmark-Server-Token": apiToken
      },
      body,
      queryParams
    })).body;
  } catch (error) {
    let postmarkError = error, message = (_d = (_b = (_a = postmarkError.response) == null ? void 0 : _a.body) == null ? void 0 : _b.Message) != null ? _d : `Postmark API request failed${(_c = postmarkError.response) != null && _c.status ? ` (${postmarkError.response.status})` : ""}`;
    throw new Error(message);
  }
}
var postmarkClient = {
  get: (apiToken, path, queryParams) => postmarkRequest({
    method: import_pieces_common.HttpMethod.GET,
    path,
    apiToken,
    queryParams
  }),
  post: (apiToken, path, body) => postmarkRequest({
    method: import_pieces_common.HttpMethod.POST,
    path,
    apiToken,
    body
  })
};

// ../ap/packages/pieces/community/postmark/src/lib/auth.ts
var postmarkAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Server API Token",
  description: "Create or copy a Postmark Server API Token from Servers \u2192 API Tokens in Postmark.",
  required: !0,
  validate: async ({ auth }) => {
    try {
      let response = await postmarkClient.get(
        auth,
        "/message-streams"
      );
      return !response || !Array.isArray(response.MessageStreams) ? {
        valid: !1,
        error: "Unexpected response while validating Postmark token."
      } : { valid: !0 };
    } catch (error) {
      return {
        valid: !1,
        error: error instanceof Error ? error.message : "Failed to validate Postmark Server API Token."
      };
    }
  }
});

// ../ap/packages/pieces/community/postmark/src/lib/actions/get-delivery-stats.ts
var getDeliveryStats = (0, import_pieces_framework2.createAction)({
  name: "get_delivery_stats",
  displayName: "Get Delivery Stats",
  description: "Retrieve delivery statistics for the current Postmark server.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves aggregate delivery statistics for the Postmark server, including bounce-rate breakdowns by type. Use to report on overall email health for the server; it takes no inputs and is scoped to the authenticated server token. Read-only and idempotent.",
    idempotent: !0
  },
  auth: postmarkAuth,
  props: {},
  async run(context) {
    return await postmarkClient.get(
      context.auth.secret_text,
      "/deliverystats"
    );
  }
});

// ../ap/packages/pieces/community/postmark/src/lib/actions/get-bounces.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var getEmailBounces = (0, import_pieces_framework3.createAction)({
  name: "get_email_bounces",
  displayName: "Get Email Bounces",
  description: "List bounces from your Postmark server.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a paginated list of email bounces recorded on the Postmark server. Use to inspect delivery failures or check whether a specific recipient bounced; optional filters narrow by bounce type, inactive-only, recipient email, or message ID, and leaving them empty returns all bounces. Read-only and idempotent.",
    idempotent: !0
  },
  auth: postmarkAuth,
  props: {
    count: import_pieces_framework3.Property.Number({
      displayName: "Count",
      description: "Maximum number of bounces to return (max 500).",
      required: !0,
      defaultValue: 25
    }),
    offset: import_pieces_framework3.Property.Number({
      displayName: "Offset",
      description: "Number of bounces to skip before returning results.",
      required: !0,
      defaultValue: 0
    }),
    type: import_pieces_framework3.Property.ShortText({
      displayName: "Type",
      description: "Optional bounce type filter, for example HardBounce.",
      required: !1
    }),
    inactive: import_pieces_framework3.Property.Checkbox({
      displayName: "Inactive Only",
      description: "Filter by inactive bounces only. When not set, both active and inactive bounces are returned.",
      required: !1
    }),
    emailFilter: import_pieces_framework3.Property.ShortText({
      displayName: "Email Filter",
      description: "Filter results by bounced recipient email address.",
      required: !1
    }),
    messageId: import_pieces_framework3.Property.ShortText({
      displayName: "Message ID",
      description: "Filter results by Postmark message ID.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let props = context.propsValue, count = (_a = props.count) != null ? _a : 25, offset = (_b = props.offset) != null ? _b : 0, queryParams = {
      count: count.toString(),
      offset: offset.toString()
    };
    return (_c = props.type) != null && _c.trim() && (queryParams.type = props.type.trim()), props.inactive === !0 && (queryParams.inactive = "true"), (_d = props.emailFilter) != null && _d.trim() && (queryParams.emailFilter = props.emailFilter.trim()), (_e = props.messageId) != null && _e.trim() && (queryParams.messageID = props.messageId.trim()), await postmarkClient.get(
      context.auth.secret_text,
      "/bounces",
      queryParams
    );
  }
});

// ../ap/packages/pieces/community/postmark/src/lib/actions/send-email.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/postmark/src/lib/common/utils.ts
function normalizeEmails(emails) {
  if (!(!emails || emails.length === 0))
    return emails.map((e) => e.trim()).filter((e) => e.length > 0).join(", ");
}

// ../ap/packages/pieces/community/postmark/src/lib/actions/send-email.ts
function assertBodyProvided(textBody, htmlBody) {
  if (!(textBody != null && textBody.trim()) && !(htmlBody != null && htmlBody.trim()))
    throw new Error("Please provide a text body, an HTML body, or both.");
}
var sendEmail = (0, import_pieces_framework4.createAction)({
  name: "send_email",
  displayName: "Send Email",
  description: "Send a single transactional email using Postmark.",
  audience: "both",
  aiMetadata: {
    description: "Sends one transactional email through Postmark with an inline subject and body (text, HTML, or both). Use to deliver a one-off ad-hoc message; for content driven by a saved Postmark template use Send Email With Template instead. Requires a From address verified on the Postmark server and at least one of text or HTML body. Not idempotent: each call dispatches a new email.",
    idempotent: !1
  },
  auth: postmarkAuth,
  props: {
    from: import_pieces_framework4.Property.ShortText({
      displayName: "From",
      description: "Sender email address configured in Postmark.",
      required: !0
    }),
    to: import_pieces_framework4.Property.Array({
      displayName: "To",
      description: "Recipient email addresses.",
      required: !0
    }),
    cc: import_pieces_framework4.Property.Array({
      displayName: "CC",
      description: "Optional CC recipients.",
      required: !1
    }),
    bcc: import_pieces_framework4.Property.Array({
      displayName: "BCC",
      description: "Optional BCC recipients.",
      required: !1
    }),
    replyTo: import_pieces_framework4.Property.ShortText({
      displayName: "Reply To",
      description: "Optional reply-to email address.",
      required: !1
    }),
    subject: import_pieces_framework4.Property.ShortText({
      displayName: "Subject",
      required: !0
    }),
    textBody: import_pieces_framework4.Property.LongText({
      displayName: "Text Body",
      required: !1
    }),
    htmlBody: import_pieces_framework4.Property.LongText({
      displayName: "HTML Body",
      required: !1
    }),
    tag: import_pieces_framework4.Property.ShortText({
      displayName: "Tag",
      required: !1
    }),
    trackOpens: import_pieces_framework4.Property.Checkbox({
      displayName: "Track Opens",
      required: !1,
      defaultValue: !0
    }),
    messageStream: import_pieces_framework4.Property.ShortText({
      displayName: "Message Stream",
      description: "Defaults to outbound if omitted.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let props = context.propsValue;
    assertBodyProvided(props.textBody, props.htmlBody);
    let payload = {
      From: props.from,
      To: normalizeEmails(props.to),
      Cc: normalizeEmails(props.cc),
      Bcc: normalizeEmails(props.bcc),
      ReplyTo: ((_a = props.replyTo) == null ? void 0 : _a.trim()) || void 0,
      Subject: props.subject,
      TextBody: ((_b = props.textBody) == null ? void 0 : _b.trim()) || void 0,
      HtmlBody: ((_c = props.htmlBody) == null ? void 0 : _c.trim()) || void 0,
      Tag: ((_d = props.tag) == null ? void 0 : _d.trim()) || void 0,
      TrackOpens: props.trackOpens,
      MessageStream: ((_e = props.messageStream) == null ? void 0 : _e.trim()) || "outbound"
    }, response = await postmarkClient.post(
      context.auth.secret_text,
      "/email",
      payload
    );
    return {
      success: response.ErrorCode === 0,
      response
    };
  }
});

// ../ap/packages/pieces/community/postmark/src/lib/actions/send-email-with-template.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var sendEmailWithTemplate = (0, import_pieces_framework5.createAction)({
  name: "send_email_with_template",
  displayName: "Send Email With Template",
  description: "Send an email using a Postmark template ID and template model.",
  audience: "both",
  aiMetadata: {
    description: "Sends a transactional email rendered from a saved Postmark template, populating it with a template model (the JSON of merge variables). Use when the message content lives in a Postmark template rather than being composed inline; for free-form subject and body use Send Email instead. Requires the numeric template ID and a verified From address. Not idempotent: each call dispatches a new email.",
    idempotent: !1
  },
  auth: postmarkAuth,
  props: {
    from: import_pieces_framework5.Property.ShortText({
      displayName: "From",
      description: "Sender email address configured in Postmark.",
      required: !0
    }),
    to: import_pieces_framework5.Property.Array({
      displayName: "To",
      description: "Recipient email addresses.",
      required: !0
    }),
    cc: import_pieces_framework5.Property.Array({
      displayName: "CC",
      required: !1
    }),
    bcc: import_pieces_framework5.Property.Array({
      displayName: "BCC",
      required: !1
    }),
    replyTo: import_pieces_framework5.Property.ShortText({
      displayName: "Reply To",
      required: !1
    }),
    templateId: import_pieces_framework5.Property.Number({
      displayName: "Template ID",
      description: "Numeric Postmark template ID.",
      required: !0
    }),
    templateModel: import_pieces_framework5.Property.Json({
      displayName: "Template Model",
      description: "JSON object used to populate the Postmark template.",
      required: !0
    }),
    tag: import_pieces_framework5.Property.ShortText({
      displayName: "Tag",
      required: !1
    }),
    trackOpens: import_pieces_framework5.Property.Checkbox({
      displayName: "Track Opens",
      required: !1,
      defaultValue: !0
    }),
    messageStream: import_pieces_framework5.Property.ShortText({
      displayName: "Message Stream",
      description: "Defaults to outbound if omitted.",
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c;
    let props = context.propsValue, payload = {
      From: props.from,
      To: normalizeEmails(props.to),
      Cc: normalizeEmails(props.cc),
      Bcc: normalizeEmails(props.bcc),
      ReplyTo: ((_a = props.replyTo) == null ? void 0 : _a.trim()) || void 0,
      TemplateId: props.templateId,
      TemplateModel: props.templateModel,
      Tag: ((_b = props.tag) == null ? void 0 : _b.trim()) || void 0,
      TrackOpens: props.trackOpens,
      MessageStream: ((_c = props.messageStream) == null ? void 0 : _c.trim()) || "outbound"
    }, response = await postmarkClient.post(
      context.auth.secret_text,
      "/email/withTemplate",
      payload
    );
    return {
      success: response.ErrorCode === 0,
      response
    };
  }
});

// ../ap/packages/pieces/community/postmark/src/index.ts
var postmark = (0, import_pieces_framework6.createPiece)({
  displayName: "Postmark",
  description: "Transactional email platform for sending email, templates, and retrieving delivery and bounce insights.",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/postmark.png",
  authors: ["Harmatta", "Angelebeats"],
  categories: [import_pieces_framework7.PieceCategory.COMMUNICATION, import_pieces_framework7.PieceCategory.MARKETING],
  auth: postmarkAuth,
  actions: [
    sendEmail,
    sendEmailWithTemplate,
    getEmailBounces,
    getDeliveryStats,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => "https://api.postmarkapp.com",
      auth: postmarkAuth,
      authMapping: async (auth) => ({
        "X-Postmark-Server-Token": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  postmark
});
