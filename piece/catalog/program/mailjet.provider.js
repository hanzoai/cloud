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

// ../ap/packages/pieces/community/mailjet/src/index.ts
var index_exports = {};
__export(index_exports, {
  mailjet: () => mailjet
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/mailjet/src/lib/actions/send-email.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/mailjet/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), mailjetAuth = import_pieces_framework.PieceAuth.BasicAuth({
  description: "Enter your api credentials",
  required: !0,
  username: {
    displayName: "API Key",
    description: "Enter your API Key here"
  },
  password: {
    displayName: "API Secret",
    description: "Enter your API Secret here"
  }
});

// ../ap/packages/pieces/community/mailjet/src/lib/actions/send-email.ts
var sendEmail = (0, import_pieces_framework2.createAction)({
  auth: mailjetAuth,
  name: "send_email",
  displayName: "Send Email",
  description: "Send a text, HTML or template email through Mailjet",
  audience: "both",
  aiMetadata: { description: "Sends an email to one or more recipients via the Mailjet transactional send API. Supply the body inline as plain text and/or HTML, or set a Mailjet template ID to render a predefined template with optional variables. Use when delivering a notification, alert, or transactional message; the sender address must be a verified Mailjet sender. Not idempotent \u2014 each call dispatches a new email.", idempotent: !1 },
  props: {
    fromEmail: import_pieces_framework2.Property.ShortText({
      displayName: "From (Email)",
      description: "Sender email, must be verified in Mailjet",
      required: !0
    }),
    fromName: import_pieces_framework2.Property.ShortText({
      displayName: "From (Name)",
      required: !1
    }),
    toEmails: import_pieces_framework2.Property.Array({
      displayName: "Emails of recipients",
      required: !0
    }),
    subject: import_pieces_framework2.Property.ShortText({
      displayName: "Subject",
      description: void 0,
      required: !0
    }),
    textPart: import_pieces_framework2.Property.LongText({
      displayName: "Text part",
      description: void 0,
      required: !1
    }),
    htmlPart: import_pieces_framework2.Property.LongText({
      displayName: "HTML part",
      description: void 0,
      required: !1
    }),
    templateId: import_pieces_framework2.Property.Number({
      displayName: "Template Id",
      description: "Template Id (number) defined in Mailjet",
      required: !1
    }),
    templateVariables: import_pieces_framework2.Property.Object({
      displayName: "Template variables",
      description: void 0,
      required: !1
    })
  },
  async run(configValue) {
    let { propsValue, auth } = configValue, message = {
      From: {
        Email: propsValue.fromEmail,
        Name: propsValue.fromName || propsValue.fromEmail
      },
      To: propsValue.toEmails.map((to) => ({
        Email: to,
        Name: to
      })),
      Subject: propsValue.subject,
      TextPart: propsValue.textPart,
      TemplateID: propsValue.templateId,
      TemplateLanguage: !!propsValue.templateId,
      Variables: propsValue.templateVariables
    }, request = {
      method: import_pieces_common.HttpMethod.POST,
      url: "https://api.mailjet.com/v3.1/send",
      body: JSON.stringify({ messages: [message] }),
      authentication: {
        type: import_pieces_common.AuthenticationType.BASIC,
        username: auth.username,
        password: auth.password
      },
      queryParams: {}
    }, response = await import_pieces_common.httpClient.sendRequest(request);
    if (response.status !== 200)
      throw new Error("Failed to communicate with Mailjet");
    return response.body.Messages[0];
  }
});

// ../ap/packages/pieces/community/mailjet/src/index.ts
var mailjet = (0, import_pieces_framework3.createPiece)({
  displayName: "Mailjet",
  description: "Email delivery service for sending transactional and marketing emails",
  auth: mailjetAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/mailjet.svg",
  categories: [import_pieces_framework4.PieceCategory.COMMUNICATION],
  authors: ["christian-schab"],
  actions: [sendEmail],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  mailjet
});
