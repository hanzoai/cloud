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

// ../ap/packages/pieces/community/emailit/src/index.ts
var index_exports = {};
__export(index_exports, {
  emailit: () => emailit
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/emailit/src/lib/actions/send-email.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/emailit/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), emailitAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Get your API key from your [Emailit Workspace Settings](https://app.emailit.com/settings/api-keys).",
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.emailit.com/v2/domains",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key. Please check your Emailit Workspace Settings."
      };
    }
  }
});

// ../ap/packages/pieces/community/emailit/src/lib/actions/send-email.ts
var sendEmailAction = (0, import_pieces_framework2.createAction)({
  auth: emailitAuth,
  name: "send_email",
  displayName: "Send Email",
  description: "Send an email to one or more recipients via Emailit.",
  audience: "both",
  aiMetadata: { description: "Sends a transactional email through Emailit to one or more To recipients (with optional CC/BCC, reply-to, and custom headers). Choose this to deliver a message via Emailit; the body is sent as either plain text or HTML depending on the content type. The sender email must be verified in the Emailit account, and the combined To/CC/BCC list is capped at 50 addresses. Not idempotent: each call dispatches a new email.", idempotent: !1 },
  props: {
    to: import_pieces_framework2.Property.Array({
      displayName: "To",
      description: "Recipient email addresses. Up to 50 total across To, CC, and BCC.",
      required: !0
    }),
    from_name: import_pieces_framework2.Property.ShortText({
      displayName: "Sender Name",
      description: 'Name recipients will see in their inbox, e.g. "Acme Support". If left blank, only the email address is shown.',
      required: !1
    }),
    from_email: import_pieces_framework2.Property.ShortText({
      displayName: "Sender Email",
      description: "Email address to send from. Must be verified in your Emailit account.",
      required: !0
    }),
    cc: import_pieces_framework2.Property.Array({
      displayName: "CC",
      description: "Send a visible copy to these addresses.",
      required: !1
    }),
    bcc: import_pieces_framework2.Property.Array({
      displayName: "BCC",
      description: "Send a hidden copy to these addresses. Other recipients won't see them.",
      required: !1
    }),
    reply_to: import_pieces_framework2.Property.ShortText({
      displayName: "Reply-To",
      description: "Where replies go. Defaults to the sender email if left blank.",
      required: !1
    }),
    subject: import_pieces_framework2.Property.ShortText({
      displayName: "Subject",
      description: "The subject line shown in the recipient's inbox.",
      required: !0
    }),
    content_type: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Content Type",
      required: !0,
      defaultValue: "html",
      options: {
        disabled: !1,
        options: [
          { label: "Plain Text", value: "text" },
          { label: "HTML", value: "html" }
        ]
      }
    }),
    content: import_pieces_framework2.Property.LongText({
      displayName: "Email Body",
      description: "The message body. Use Plain Text for simple messages or HTML for rich formatting.",
      required: !0
    }),
    headers: import_pieces_framework2.Property.Object({
      displayName: "Custom Headers",
      description: "Add custom email headers as key-value pairs, e.g. X-Campaign-ID: welcome.",
      required: !1
    })
  },
  async run(context) {
    let {
      to,
      from_name,
      from_email,
      cc,
      bcc,
      reply_to,
      subject,
      content_type,
      content,
      headers
    } = context.propsValue, requestBody = {
      from: from_name ? `${from_name} <${from_email}>` : from_email,
      to,
      subject
    };
    return content_type === "text" ? requestBody.text = content : requestBody.html = content, cc && cc.length > 0 && (requestBody.cc = cc), bcc && bcc.length > 0 && (requestBody.bcc = bcc), reply_to && (requestBody.reply_to = reply_to), headers && Object.keys(headers).length > 0 && (requestBody.headers = headers), await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: "https://api.emailit.com/v2/emails",
      body: requestBody,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: context.auth.secret_text
      }
    });
  }
});

// ../ap/packages/pieces/community/emailit/src/index.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var emailit = (0, import_pieces_framework3.createPiece)({
  displayName: "Emailit",
  description: "Send transactional emails with Emailit",
  logoUrl: "https://cdn.activepieces.com/pieces/emailit.svg",
  categories: [import_pieces_framework4.PieceCategory.COMMUNICATION, import_pieces_framework4.PieceCategory.PRODUCTIVITY],
  authors: ["dennisklappe", "onyedikachi-david"],
  auth: emailitAuth,
  actions: [
    sendEmailAction,
    (0, import_pieces_common3.createCustomApiCallAction)({
      baseUrl: () => "https://api.emailit.com/v2",
      auth: emailitAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  emailit
});
