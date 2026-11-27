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

// ../ap/packages/pieces/community/sendinblue/src/index.ts
var index_exports = {};
__export(index_exports, {
  sendinblue: () => sendinblue,
  sendinblueAuth: () => sendinblueAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sendinblue/src/lib/actions/create-event.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sendinblue/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), sendinblueAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Project API key",
  description: "Your project API key",
  required: !0
});

// ../ap/packages/pieces/community/sendinblue/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
async function apiCall({
  apiKey,
  method,
  resourceUri,
  query,
  body
}) {
  let queryParams = {};
  if (query)
    for (let [key, value] of Object.entries(query))
      (0, import_pieces_framework2.isNil)(value) || (queryParams[key] = String(value));
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BREVO_API_URL}${resourceUri}`,
    headers: {
      "api-key": apiKey,
      accept: "application/json"
    },
    queryParams,
    body: compactBody(body)
  })).body;
}
function compactBody(body) {
  return isPlainObject(body) ? Object.fromEntries(
    Object.entries(body).filter(([, value]) => !(0, import_pieces_framework2.isNil)(value))
  ) : body;
}
function isPlainObject(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}
function isEmptyObject(value) {
  return (0, import_pieces_framework2.isNil)(value) || Object.keys(value).length === 0;
}
var brevoCommon = { apiCall, isEmptyObject }, BREVO_API_URL = "https://api.brevo.com/v3";

// ../ap/packages/pieces/community/sendinblue/src/lib/actions/create-event.ts
var createEvent = (0, import_pieces_framework3.createAction)({
  auth: sendinblueAuth,
  name: "create_event",
  classification: "WRITE",
  displayName: "Create Event",
  description: "Track a custom event for a contact.",
  audience: "both",
  aiMetadata: {
    description: "Records a custom event against a Brevo contact so automations and segments can react to it. Identify the contact by email, contact id or external id, and attach arbitrary event properties. Brevo answers with an empty body, so this returns a success flag rather than the stored event. Not idempotent \u2014 each call records another occurrence.",
    idempotent: !1
  },
  props: {
    event_name: import_pieces_framework3.Property.ShortText({
      displayName: "Event Name",
      description: "How the event is identified in Brevo, for example order_completed.",
      required: !0
    }),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Contact Email",
      description: "Email of the contact the event belongs to.",
      required: !0
    }),
    event_date: import_pieces_framework3.Property.DateTime({
      displayName: "Event Date",
      description: "When the event occurred. Defaults to now when left empty.",
      required: !1
    }),
    event_properties: import_pieces_framework3.Property.Object({
      displayName: "Event Properties",
      description: 'Details of the event, for example {"order_id": 42, "total": 99.5}.',
      required: !1
    }),
    contact_properties: import_pieces_framework3.Property.Object({
      displayName: "Contact Properties",
      description: 'Contact attributes to update alongside the event, for example {"FIRSTNAME": "Elly"}.',
      required: !1
    })
  },
  async run(context) {
    let { event_name, email, event_date, event_properties, contact_properties } = context.propsValue, body = {
      event_name,
      identifiers: { email_id: email },
      event_date: event_date != null ? event_date : void 0,
      event_properties: brevoCommon.isEmptyObject(event_properties) ? void 0 : event_properties,
      contact_properties: brevoCommon.isEmptyObject(contact_properties) ? void 0 : contact_properties
    };
    return await brevoCommon.apiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common2.HttpMethod.POST,
      resourceUri: "/events",
      body
    }), { success: !0 };
  }
});

// ../ap/packages/pieces/community/sendinblue/src/lib/actions/create-or-update-contact.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sendinblue/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var PAGE_SIZE = 50;
async function fetchAllLists(apiKey) {
  var _a;
  let lists = [], offset = 0, hasMore = !0;
  for (; hasMore; ) {
    let page = (_a = (await brevoCommon.apiCall({
      apiKey,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: "/contacts/lists",
      query: { limit: PAGE_SIZE, offset }
    })).lists) != null ? _a : [];
    lists.push(...page), offset += PAGE_SIZE, hasMore = page.length === PAGE_SIZE;
  }
  return lists;
}
async function fetchAllTemplates(apiKey) {
  var _a;
  let templates = [], offset = 0, hasMore = !0;
  for (; hasMore; ) {
    let page = (_a = (await brevoCommon.apiCall({
      apiKey,
      method: import_pieces_common3.HttpMethod.GET,
      resourceUri: "/smtp/templates",
      query: { limit: PAGE_SIZE, offset }
    })).templates) != null ? _a : [];
    templates.push(...page), offset += PAGE_SIZE, hasMore = page.length === PAGE_SIZE;
  }
  return templates;
}
function connectFirst() {
  return {
    disabled: !0,
    placeholder: "Connect your Brevo account first.",
    options: []
  };
}
var brevoProps = {
  listIds: ({ displayName, description }) => import_pieces_framework4.Property.MultiSelectDropdown({
    displayName,
    description,
    required: !1,
    auth: sendinblueAuth,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await fetchAllLists(auth.secret_text)).map((list) => ({
        label: list.name,
        value: String(list.id)
      }))
    } : connectFirst()
  }),
  senderEmail: import_pieces_framework4.Property.Dropdown({
    displayName: "Sender",
    description: "The verified sender the email is sent from.",
    required: !1,
    auth: sendinblueAuth,
    refreshers: [],
    options: async ({ auth }) => {
      var _a;
      return auth ? {
        disabled: !1,
        options: ((_a = (await brevoCommon.apiCall({
          apiKey: auth.secret_text,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: "/senders"
        })).senders) != null ? _a : []).map((sender) => ({
          label: sender.name ? `${sender.name} <${sender.email}>` : sender.email,
          value: sender.email
        }))
      } : connectFirst();
    }
  }),
  emailTemplateId: import_pieces_framework4.Property.Dropdown({
    displayName: "Template",
    description: "Send a saved Brevo template instead of supplying subject and content.",
    required: !1,
    auth: sendinblueAuth,
    refreshers: [],
    options: async ({ auth }) => auth ? {
      disabled: !1,
      options: (await fetchAllTemplates(auth.secret_text)).map((template) => ({
        label: template.name,
        value: template.id
      }))
    } : connectFirst()
  })
};

// ../ap/packages/pieces/community/sendinblue/src/lib/output-schemas.ts
var contactFields = [
  { key: "id", label: "Contact ID", format: "number" },
  { key: "email", label: "Email", format: "email" },
  {
    key: "attributes",
    label: "Attributes",
    description: "Account specific contact attributes such as FIRSTNAME or SMS.",
    dynamicKey: !0
  },
  { key: "listIds", label: "List IDs", description: "Lists the contact belongs to." },
  { key: "emailBlacklisted", label: "Email Blacklisted", format: "boolean" },
  { key: "smsBlacklisted", label: "SMS Blacklisted", format: "boolean" },
  { key: "whatsappBlacklisted", label: "WhatsApp Blacklisted", format: "boolean" },
  { key: "createdAt", label: "Created At", format: "datetime" },
  { key: "modifiedAt", label: "Modified At", format: "datetime" },
  {
    key: "statistics",
    label: "Statistics",
    description: "Engagement counters, populated once the contact has campaign activity."
  }
], marketingEventFields = [
  { key: "event", label: "Event" },
  { key: "email", label: "Email", format: "email" },
  {
    key: "id",
    label: "Webhook ID",
    description: "The Brevo webhook that delivered this event, not the contact id.",
    format: "number"
  },
  { key: "date", label: "Date", format: "datetime" },
  { key: "ts", label: "Timestamp (seconds)", format: "number" }
], transactionalEventFields = [
  { key: "event", label: "Event" },
  { key: "email", label: "Recipient", format: "email" },
  { key: "subject", label: "Subject" },
  { key: "message-id", label: "Message ID" },
  { key: "uuid", label: "Event UUID" },
  { key: "sender_email", label: "Sender", format: "email" },
  { key: "tags", label: "Tags" },
  { key: "sending_ip", label: "Sending IP" },
  { key: "date", label: "Date", format: "datetime" },
  { key: "ts_event", label: "Event Timestamp (seconds)", format: "number" },
  { key: "ts_epoch", label: "Event Timestamp", format: "datetime" },
  {
    key: "id",
    label: "Webhook ID",
    description: "The Brevo webhook that delivered this event, not the message id.",
    format: "number"
  }
], deliveryEventFields = [
  ...transactionalEventFields,
  {
    key: "reason",
    label: "Reason",
    description: "Why the message reached this state, for example sent or an MX lookup failure."
  }
], engagementEventFields = [
  ...transactionalEventFields,
  { key: "user_agent", label: "User Agent" },
  { key: "device_used", label: "Device Used" },
  {
    key: "contact_id",
    label: "Contact ID",
    description: "The Brevo contact that engaged. Unlike the top level id, this is a real contact id.",
    format: "number"
  }
], createOrUpdateContactActionOutputSchema = {
  fields: contactFields
}, findContactActionOutputSchema = {
  fields: [
    { key: "found", label: "Found", format: "boolean" },
    { key: "data", label: "Contact", children: contactFields }
  ]
}, sendTransactionalEmailActionOutputSchema = {
  fields: [
    {
      key: "messageId",
      label: "Message ID",
      description: "Angle bracketed SMTP message id, matching the message-id on email events."
    }
  ]
}, sendTransactionalSmsActionOutputSchema = {
  fields: [
    {
      key: "messageId",
      label: "Message ID",
      description: "Brevo accepts the SMS asynchronously, so a message id here does not confirm delivery.",
      format: "number"
    }
  ]
}, contactAddedToListTriggerOutputSchema = {
  fields: [
    ...marketingEventFields,
    { key: "list_id", label: "List IDs", description: "Lists the contact was added to." }
  ]
}, contactUpdatedTriggerOutputSchema = {
  fields: [
    ...marketingEventFields,
    {
      key: "content",
      label: "Changed Fields",
      labelKey: "email",
      listItems: [
        { key: "email", label: "Email", format: "email" },
        { key: "attributes", label: "Attributes", dynamicKey: !0 }
      ]
    }
  ]
}, contactDeletedTriggerOutputSchema = {
  fields: [
    { key: "event", label: "Event" },
    {
      key: "email",
      label: "Emails",
      description: "Array of deleted addresses, unlike the single address other contact events send."
    },
    {
      key: "id",
      label: "Webhook ID",
      description: "The Brevo webhook that delivered this event, not the contact id.",
      format: "number"
    },
    { key: "date", label: "Date", format: "datetime" },
    { key: "ts", label: "Timestamp (seconds)", format: "number" }
  ]
}, contactUnsubscribedTriggerOutputSchema = {
  fields: [
    ...marketingEventFields,
    { key: "camp_id", label: "Campaign ID", format: "number" },
    { key: "campaign_name", label: "Campaign Name" },
    { key: "list_id", label: "List IDs" }
  ]
}, emailDeliveredTriggerOutputSchema = {
  fields: deliveryEventFields
}, emailBouncedTriggerOutputSchema = {
  fields: deliveryEventFields
}, emailOpenedTriggerOutputSchema = {
  fields: engagementEventFields
}, emailClickedTriggerOutputSchema = {
  fields: [
    ...engagementEventFields,
    { key: "link", label: "Clicked Link", format: "url" }
  ]
};

// ../ap/packages/pieces/community/sendinblue/src/lib/actions/create-or-update-contact.ts
var createOrUpdateContact = (0, import_pieces_framework5.createAction)({
  auth: sendinblueAuth,
  name: "create_or_update_contact",
  outputSchema: createOrUpdateContactActionOutputSchema,
  classification: "WRITE",
  displayName: "Create or Update Contact",
  description: "Create or update an existing contact",
  audience: "both",
  aiMetadata: {
    description: "Upserts a Brevo (formerly Sendinblue) contact keyed on its email address, setting attributes, list membership, and email/SMS blacklist flags. Use to add a new subscriber or sync changes to an existing one; because it is keyed on email with update enabled, re-running with the same input is idempotent and does not create duplicates. Email is required, and any attributes referenced must already exist in the Brevo account. Blacklist flags are only sent when explicitly set, so leaving them untouched preserves the contact current subscription state.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      description: 'Email address of the user. Mandatory if "SMS" field is not passed in "attributes" parameter. Mobile Number in SMS field should be passed with proper country code. For example: {"SMS":"+91xxxxxxxxxx"} or {"SMS":"0091xxxxxxxxxx"}',
      required: !0
    }),
    ext_id: import_pieces_framework5.Property.ShortText({
      displayName: "External ID",
      description: "Pass your own Id to create a contact.",
      required: !1
    }),
    attributes: import_pieces_framework5.Property.Object({
      displayName: "Attributes",
      description: `Pass the set of attributes and their values. The attribute's parameter should be passed in capital letter while creating a contact. These attributes must be present in your Brevo account. For eg:
        {"FNAME":"Elly", "LNAME":"Roger"}. Only the attributes you list are changed; the rest are left as they are.`,
      required: !1
    }),
    email_blacklisted: import_pieces_framework5.Property.Checkbox({
      displayName: "Email Blacklisted?",
      description: "Set this field to blacklist the contact for emails (emailBlacklisted = true). Leave untouched to keep the contact's current setting.",
      required: !1
    }),
    sms_blacklisted: import_pieces_framework5.Property.Checkbox({
      displayName: "SMS Blacklisted?",
      description: "Set this field to blacklist the contact for SMS (smsBlacklisted = true). Leave untouched to keep the contact's current setting.",
      required: !1
    }),
    list_ids: brevoProps.listIds({
      displayName: "Lists",
      description: "Lists to add the contact to."
    }),
    blocked_sender_addresses: import_pieces_framework5.Property.Array({
      displayName: "Blocked Sender Addresses",
      description: "Sender email addresses this contact must not receive transactional email from. Leave empty to change nothing.",
      required: !1
    })
  },
  async run(context) {
    let {
      email,
      ext_id,
      attributes,
      email_blacklisted,
      sms_blacklisted,
      list_ids,
      blocked_sender_addresses
    } = context.propsValue, listIds = (list_ids != null ? list_ids : []).map((listId) => Number(listId)).filter((listId) => Number.isFinite(listId)), blockedSenders = (Array.isArray(blocked_sender_addresses) ? blocked_sender_addresses : []).map((sender) => String(sender).trim()).filter((sender) => sender.length > 0), contact = {
      email,
      ext_id,
      attributes: brevoCommon.isEmptyObject(attributes) ? void 0 : attributes,
      emailBlacklisted: email_blacklisted,
      smsBlacklisted: sms_blacklisted,
      listIds: listIds.length > 0 ? listIds : void 0,
      smtpBlacklistSender: blockedSenders.length > 0 ? blockedSenders : void 0,
      updateEnabled: !0
    };
    return await brevoCommon.apiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: "/contacts",
      body: contact
    }), await brevoCommon.apiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      resourceUri: `/contacts/${encodeURIComponent(email)}`
    });
  }
});

// ../ap/packages/pieces/community/sendinblue/src/lib/actions/find-contact.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var findContact = (0, import_pieces_framework6.createAction)({
  auth: sendinblueAuth,
  name: "find_contact",
  outputSchema: findContactActionOutputSchema,
  classification: "READ",
  displayName: "Find Contact",
  description: "Check whether a contact exists in Brevo and fetch its details.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a single Brevo contact by email, phone number, contact id, external id, WhatsApp id or landline number, and returns its attributes, list membership and blacklist flags. Returns found:false instead of failing when no contact matches, so it is safe to branch on. Read-only and idempotent.",
    idempotent: !0
  },
  props: {
    identifier: import_pieces_framework6.Property.ShortText({
      displayName: "Identifier",
      description: "The value to look the contact up by, for example an email address.",
      required: !0
    }),
    identifier_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Identifier Type",
      description: "How the identifier above should be interpreted.",
      required: !1,
      defaultValue: "email_id",
      options: {
        options: [
          { label: "Email", value: "email_id" },
          { label: "Phone (SMS)", value: "phone_id" },
          { label: "Contact ID", value: "contact_id" },
          { label: "External ID", value: "ext_id" },
          { label: "WhatsApp", value: "whatsapp_id" },
          { label: "Landline Number", value: "landline_number_id" }
        ]
      }
    })
  },
  async run(context) {
    let { identifier, identifier_type } = context.propsValue;
    try {
      return { found: !0, data: await brevoCommon.apiCall({
        apiKey: context.auth.secret_text,
        method: import_pieces_common5.HttpMethod.GET,
        resourceUri: `/contacts/${encodeURIComponent(identifier)}`,
        query: { identifierType: identifier_type }
      }) };
    } catch (error) {
      if (error instanceof import_pieces_common5.HttpError && error.response.status === 404)
        return { found: !1, data: {} };
      throw error;
    }
  }
});

// ../ap/packages/pieces/community/sendinblue/src/lib/actions/send-transactional-email.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var sendTransactionalEmail = (0, import_pieces_framework7.createAction)({
  auth: sendinblueAuth,
  name: "send_transactional_email",
  outputSchema: sendTransactionalEmailActionOutputSchema,
  classification: "WRITE",
  displayName: "Send Transactional Email",
  description: "Send an email from your Brevo account with HTML or plain text content.",
  audience: "both",
  aiMetadata: {
    description: "Sends a one-off transactional email through Brevo, either by supplying subject and HTML content directly or by selecting a saved template and passing its variables. Use for receipts, password resets, notifications and other per-recipient mail; not for bulk marketing campaigns. Requires a verified sender unless a template with its own sender is used. Not idempotent \u2014 each call sends a new message.",
    idempotent: !1
  },
  props: {
    to: import_pieces_framework7.Property.Array({
      displayName: "To",
      description: "Recipients of the email.",
      required: !0,
      properties: {
        email: import_pieces_framework7.Property.ShortText({ displayName: "Email", required: !0 }),
        name: import_pieces_framework7.Property.ShortText({ displayName: "Name", required: !1 })
      }
    }),
    sender_email: brevoProps.senderEmail,
    sender_name: import_pieces_framework7.Property.ShortText({
      displayName: "Sender Name",
      description: "Overrides the display name of the selected sender.",
      required: !1
    }),
    template_id: brevoProps.emailTemplateId,
    subject: import_pieces_framework7.Property.ShortText({
      displayName: "Subject",
      description: "Required unless a template is selected.",
      required: !1
    }),
    html_content: import_pieces_framework7.Property.LongText({
      displayName: "HTML Content",
      description: "Required unless a template is selected.",
      required: !1
    }),
    text_content: import_pieces_framework7.Property.LongText({
      displayName: "Text Content",
      description: "Plain text alternative shown when HTML cannot be rendered.",
      required: !1
    }),
    params: import_pieces_framework7.Property.Object({
      displayName: "Template Parameters",
      description: "Values substituted into the template placeholders.",
      required: !1
    }),
    cc: import_pieces_framework7.Property.Array({
      displayName: "CC",
      required: !1,
      properties: {
        email: import_pieces_framework7.Property.ShortText({ displayName: "Email", required: !0 }),
        name: import_pieces_framework7.Property.ShortText({ displayName: "Name", required: !1 })
      }
    }),
    bcc: import_pieces_framework7.Property.Array({
      displayName: "BCC",
      required: !1,
      properties: {
        email: import_pieces_framework7.Property.ShortText({ displayName: "Email", required: !0 }),
        name: import_pieces_framework7.Property.ShortText({ displayName: "Name", required: !1 })
      }
    }),
    reply_to_email: import_pieces_framework7.Property.ShortText({
      displayName: "Reply To",
      required: !1
    }),
    attachments: import_pieces_framework7.Property.Array({
      displayName: "Attachments",
      description: "Files attached by public URL. Brevo downloads each URL when sending.",
      required: !1,
      properties: {
        url: import_pieces_framework7.Property.ShortText({ displayName: "URL", required: !0 }),
        name: import_pieces_framework7.Property.ShortText({ displayName: "File Name", required: !0 })
      }
    }),
    tags: import_pieces_framework7.Property.Array({
      displayName: "Tags",
      description: "Labels used to filter this message in Brevo reporting.",
      required: !1
    }),
    scheduled_at: import_pieces_framework7.Property.DateTime({
      displayName: "Scheduled At",
      description: "Send the email at this UTC time instead of immediately.",
      required: !1
    }),
    sandbox: import_pieces_framework7.Property.Checkbox({
      displayName: "Sandbox Mode",
      description: "Validate the request without delivering anything. Brevo checks the payload, sender and credentials, returns a message id, and drops the message: nothing reaches the recipient and no event is logged.",
      required: !1
    })
  },
  async run(context) {
    let {
      to,
      sender_email,
      sender_name,
      template_id,
      subject,
      html_content,
      text_content,
      params,
      cc,
      bcc,
      reply_to_email,
      attachments,
      tags,
      scheduled_at,
      sandbox
    } = context.propsValue, recipients = toRecipients(to);
    if (recipients.length === 0)
      throw new Error('At least one valid recipient email is required in "To".');
    if ((0, import_pieces_framework7.isNil)(template_id) && ((0, import_pieces_framework7.isNil)(subject) || (0, import_pieces_framework7.isNil)(html_content)))
      throw new Error(
        "Provide a Template, or supply both Subject and HTML Content."
      );
    if ((0, import_pieces_framework7.isNil)(template_id) && (0, import_pieces_framework7.isNil)(sender_email))
      throw new Error("A Sender is required when no Template is selected.");
    let body = {
      to: recipients,
      sender: (0, import_pieces_framework7.isNil)(sender_email) ? void 0 : { email: sender_email, name: sender_name != null ? sender_name : void 0 },
      templateId: template_id != null ? template_id : void 0,
      subject: subject != null ? subject : void 0,
      htmlContent: html_content != null ? html_content : void 0,
      textContent: text_content != null ? text_content : void 0,
      params: brevoCommon.isEmptyObject(params) ? void 0 : params,
      cc: emptyToUndefined(toRecipients(cc)),
      bcc: emptyToUndefined(toRecipients(bcc)),
      replyTo: (0, import_pieces_framework7.isNil)(reply_to_email) ? void 0 : { email: reply_to_email },
      attachment: emptyToUndefined(toAttachments(attachments)),
      tags: emptyToUndefined(toStrings(tags)),
      scheduledAt: scheduled_at != null ? scheduled_at : void 0,
      headers: sandbox ? { "X-Sib-Sandbox": "drop" } : void 0
    };
    return await brevoCommon.apiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      resourceUri: "/smtp/email",
      body
    });
  }
});
function toRecipients(value) {
  return Array.isArray(value) ? value.flatMap((entry) => {
    if (!isRecord(entry))
      return [];
    let email = entry.email;
    if (typeof email != "string" || email.length === 0)
      return [];
    let name = entry.name;
    return [typeof name == "string" && name.length > 0 ? { email, name } : { email }];
  }) : [];
}
function toAttachments(value) {
  return Array.isArray(value) ? value.flatMap((entry) => {
    if (!isRecord(entry))
      return [];
    let url = entry.url, name = entry.name;
    return typeof url != "string" || typeof name != "string" ? [] : [{ url, name }];
  }) : [];
}
function toStrings(value) {
  return Array.isArray(value) ? value.filter((entry) => typeof entry == "string") : [];
}
function emptyToUndefined(value) {
  return value.length > 0 ? value : void 0;
}
function isRecord(value) {
  return typeof value == "object" && value !== null && !Array.isArray(value);
}

// ../ap/packages/pieces/community/sendinblue/src/lib/actions/send-transactional-sms.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var sendTransactionalSms = (0, import_pieces_framework8.createAction)({
  auth: sendinblueAuth,
  name: "send_transactional_sms",
  outputSchema: sendTransactionalSmsActionOutputSchema,
  classification: "WRITE",
  displayName: "Send Transactional SMS",
  description: "Send a transactional SMS from your Brevo account.",
  audience: "both",
  aiMetadata: {
    description: "Sends a one-off transactional SMS through Brevo to a single mobile number. The recipient must include the country code without a leading plus or zeros, and the sender is limited to 11 alphanumeric or 15 numeric characters. Content longer than 160 characters is split into multiple messages and billed accordingly. Not idempotent \u2014 each call sends a new message.",
    idempotent: !1
  },
  props: {
    sender: import_pieces_framework8.Property.ShortText({
      displayName: "Sender",
      description: "Name or number shown as the sender. Limited to 11 alphanumeric characters or 15 numeric characters.",
      required: !0
    }),
    recipient: import_pieces_framework8.Property.ShortText({
      displayName: "Recipient",
      description: "Mobile number with the country code and no leading plus or zeros, for example 33680005003.",
      required: !0
    }),
    content: import_pieces_framework8.Property.LongText({
      displayName: "Content",
      description: "Message body. Longer than 160 characters is sent as multiple messages.",
      required: !0
    }),
    type: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Type",
      required: !1,
      defaultValue: "transactional",
      options: {
        options: [
          { label: "Transactional", value: "transactional" },
          { label: "Marketing", value: "marketing" }
        ]
      }
    }),
    tag: import_pieces_framework8.Property.ShortText({
      displayName: "Tag",
      description: "Label used to filter this message in Brevo reporting.",
      required: !1
    }),
    web_url: import_pieces_framework8.Property.ShortText({
      displayName: "Webhook URL",
      description: "URL Brevo posts delivery reports for this message to.",
      required: !1
    }),
    unicode_enabled: import_pieces_framework8.Property.Checkbox({
      displayName: "Unicode Enabled",
      description: "Send the content as unicode. Unicode messages are limited to 70 characters per part.",
      required: !1
    }),
    organisation_prefix: import_pieces_framework8.Property.ShortText({
      displayName: "Organisation Prefix",
      description: "Brand name prepended to the message content.",
      required: !1
    })
  },
  async run(context) {
    let {
      sender,
      recipient,
      content,
      type,
      tag,
      web_url,
      unicode_enabled,
      organisation_prefix
    } = context.propsValue, body = {
      sender,
      recipient,
      content,
      type,
      tag,
      webUrl: web_url,
      unicodeEnabled: unicode_enabled,
      organisationPrefix: organisation_prefix
    };
    return await brevoCommon.apiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.POST,
      resourceUri: "/transactionalSMS/send",
      body
    });
  }
});

// ../ap/packages/pieces/community/sendinblue/src/lib/actions/unsubscribe-contact.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var unsubscribeContact = (0, import_pieces_framework9.createAction)({
  auth: sendinblueAuth,
  name: "unsubscribe_contact",
  classification: "WRITE",
  displayName: "Unsubscribe Contact",
  description: "Blacklist a contact so it stops receiving email or SMS.",
  audience: "both",
  aiMetadata: {
    description: "Opts a Brevo contact out by setting its email and optionally SMS blacklist flags, and can additionally remove it from specific lists. Use this to honour an unsubscribe request received elsewhere. Brevo answers with an empty body, so this returns a success flag. Idempotent \u2014 unsubscribing an already unsubscribed contact changes nothing.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework9.Property.ShortText({
      displayName: "Contact Email",
      required: !0
    }),
    unsubscribe_email: import_pieces_framework9.Property.Checkbox({
      displayName: "Unsubscribe From Email",
      required: !1,
      defaultValue: !0
    }),
    unsubscribe_sms: import_pieces_framework9.Property.Checkbox({
      displayName: "Unsubscribe From SMS",
      required: !1
    }),
    unlink_list_ids: brevoProps.listIds({
      displayName: "Remove From Lists",
      description: "Lists to remove the contact from as part of unsubscribing."
    })
  },
  async run(context) {
    let { email, unsubscribe_email, unsubscribe_sms, unlink_list_ids } = context.propsValue, unlinkListIds = (unlink_list_ids != null ? unlink_list_ids : []).map((listId) => Number(listId)).filter((listId) => Number.isFinite(listId)), body = {
      emailBlacklisted: unsubscribe_email,
      smsBlacklisted: unsubscribe_sms,
      unlinkListIds: unlinkListIds.length > 0 ? unlinkListIds : void 0
    };
    return await brevoCommon.apiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.PUT,
      resourceUri: `/contacts/${encodeURIComponent(email)}`,
      body
    }), { success: !0 };
  }
});

// ../ap/packages/pieces/community/sendinblue/src/lib/triggers/register-webhook.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var brevoRegisterTrigger = ({
  name,
  displayName,
  description,
  aiDescription,
  type,
  events,
  sampleData,
  outputSchema
}) => (0, import_pieces_framework10.createTrigger)({
  auth: sendinblueAuth,
  name,
  displayName,
  description,
  classification: "READ",
  aiMetadata: { description: aiDescription },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  props: {},
  sampleData,
  outputSchema,
  async onEnable(context) {
    let webhook = await brevoCommon.apiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.POST,
      resourceUri: "/webhooks",
      body: {
        url: context.webhookUrl,
        description: `Activepieces - ${displayName}`,
        events,
        type
      }
    });
    await context.store.put(storeKey(name), {
      webhookId: webhook.id
    });
  },
  async onDisable(context) {
    let information = await context.store.get(
      storeKey(name)
    );
    (0, import_pieces_framework10.isNil)(information) || (await brevoCommon.apiCall({
      apiKey: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.DELETE,
      resourceUri: `/webhooks/${information.webhookId}`
    }), await context.store.delete(storeKey(name)));
  },
  async run(context) {
    return [context.payload.body];
  }
});
function storeKey(name) {
  return `brevo_webhook_${name}`;
}

// ../ap/packages/pieces/community/sendinblue/src/lib/triggers/samples.ts
var transactionalEmail = {
  id: 2152078,
  email: "contact@example.com",
  "message-id": "<202608251105.84807994032@smtp-relay.mailin.fr>",
  date: "2026-08-25 14:05:36",
  tags: ["welcome_series"],
  tag: '["welcome_series"]',
  subject: "Your receipt",
  sending_ip: "77.32.148.25",
  ts_event: 1787655936,
  ts: 1787655936,
  ts_epoch: 1787655936e3,
  sender_email: "sender@example.com",
  uuid: "daf7778b-13f8-4db8-9f05-a65c1795ae21"
}, engagement = __spreadProps(__spreadValues({}, transactionalEmail), {
  user_agent: "Mozilla/5.0 (Windows NT 5.1; rv:11.0) Gecko Firefox/11.0",
  device_used: "DESKTOP"
}), brevoSamples = { transactionalEmail, engagement }, WEBHOOK_ID_NOTE = "Note that the payload id field is the Brevo webhook id, not the contact id \u2014 identify the contact by email.";

// ../ap/packages/pieces/community/sendinblue/src/lib/triggers/contact-added-to-list.ts
var contactAddedToList = brevoRegisterTrigger({
  name: "contact_added_to_list",
  displayName: "Contact Added to List",
  description: "Triggers when a contact is added to one of your lists.",
  aiDescription: `Fires when a contact is added to any Brevo list. The payload carries the contact email and a list_id array of the lists it was added to. Brevo subscribes at account level, so this fires for every list; filter on list_id downstream when only one list matters. ${WEBHOOK_ID_NOTE}`,
  type: "marketing",
  events: ["listAddition"],
  sampleData: {
    id: 2152070,
    event: "list_addition",
    email: "contact@example.com",
    list_id: [2],
    date: "2026-08-25 11:02:27",
    ts: 1787655747
  },
  outputSchema: contactAddedToListTriggerOutputSchema
});

// ../ap/packages/pieces/community/sendinblue/src/lib/triggers/contact-deleted.ts
var contactDeleted = brevoRegisterTrigger({
  name: "contact_deleted",
  displayName: "Contact Deleted",
  description: "Triggers when a contact is deleted.",
  aiDescription: `Fires when a Brevo contact is deleted, so downstream systems can drop the record. Unlike the other contact events the email field is an ARRAY of addresses, and date is an ISO 8601 timestamp rather than the space separated format the other events use. ${WEBHOOK_ID_NOTE}`,
  type: "marketing",
  events: ["contactDeleted"],
  sampleData: {
    id: 2152073,
    event: "contact_deleted",
    email: ["contact@example.com"],
    date: "2026-08-25T11:04:17.82511Z",
    ts: 1787655857
  },
  outputSchema: contactDeletedTriggerOutputSchema
});

// ../ap/packages/pieces/community/sendinblue/src/lib/triggers/contact-unsubscribed.ts
var contactUnsubscribed = brevoRegisterTrigger({
  name: "contact_unsubscribed",
  displayName: "Contact Unsubscribed",
  description: "Triggers when a contact unsubscribes from a campaign.",
  aiDescription: `Fires when a contact unsubscribes, so opt-outs can be honoured in other systems. The payload carries the contact email and the campaign and list identifiers behind the unsubscribe. ${WEBHOOK_ID_NOTE}`,
  type: "marketing",
  events: ["unsubscribed"],
  sampleData: {
    id: 2152074,
    event: "unsubscribe",
    email: "contact@example.com",
    camp_id: 12,
    campaign_name: "My First Campaign",
    list_id: [3, 42],
    date: "2026-08-25 11:05:00",
    ts: 1787655900
  },
  outputSchema: contactUnsubscribedTriggerOutputSchema
});

// ../ap/packages/pieces/community/sendinblue/src/lib/triggers/contact-updated.ts
var contactUpdated = brevoRegisterTrigger({
  name: "contact_updated",
  displayName: "Contact Updated",
  description: "Triggers when the attributes of a contact are updated.",
  aiDescription: `Fires when an existing Brevo contact is modified. The payload carries the contact email plus a content array whose single entry holds the contact email and an attributes object of the fields that changed. ${WEBHOOK_ID_NOTE}`,
  type: "marketing",
  events: ["contactUpdated"],
  sampleData: {
    id: 2152072,
    event: "contact_updated",
    email: "contact@example.com",
    date: "2026-08-25 11:04:09",
    ts: 1787655849,
    content: [
      {
        email: "contact@example.com",
        attributes: { FIRSTNAME: "Elly" }
      }
    ]
  },
  outputSchema: contactUpdatedTriggerOutputSchema
});

// ../ap/packages/pieces/community/sendinblue/src/lib/triggers/email-bounced.ts
var emailBounced = brevoRegisterTrigger({
  name: "email_bounced",
  displayName: "Transactional Email Bounced",
  description: "Triggers when a transactional email hard or soft bounces.",
  aiDescription: `Fires when a transactional email sent from this Brevo account bounces. Covers both hard and soft bounces: read the event field to tell them apart, where hard_bounce means permanently undeliverable and soft_bounce a temporary failure, and read reason for the mail server explanation. ${WEBHOOK_ID_NOTE}`,
  type: "transactional",
  events: ["hardBounce", "softBounce"],
  sampleData: __spreadProps(__spreadValues({}, brevoSamples.transactionalEmail), {
    email: "invalid@example.invalid",
    event: "soft_bounce",
    reason: "Unable to find MX of domain example.invalid"
  }),
  outputSchema: emailBouncedTriggerOutputSchema
});

// ../ap/packages/pieces/community/sendinblue/src/lib/triggers/email-clicked.ts
var emailClicked = brevoRegisterTrigger({
  name: "email_clicked",
  displayName: "Transactional Email Link Clicked",
  description: "Triggers when a recipient clicks a link in a transactional email.",
  aiDescription: `Fires when a recipient clicks a link inside a transactional email sent from this Brevo account. The payload adds the clicked link URL to the usual message and device fields, so use it to score intent or route follow ups. ${WEBHOOK_ID_NOTE}`,
  type: "transactional",
  events: ["click"],
  sampleData: __spreadProps(__spreadValues({}, brevoSamples.engagement), {
    event: "click",
    link: "https://example.com/product"
  }),
  outputSchema: emailClickedTriggerOutputSchema
});

// ../ap/packages/pieces/community/sendinblue/src/lib/triggers/email-delivered.ts
var emailDelivered = brevoRegisterTrigger({
  name: "email_delivered",
  displayName: "Transactional Email Delivered",
  description: "Triggers when a transactional email is delivered to the recipient.",
  aiDescription: `Fires when a transactional email sent from this Brevo account reaches the recipient mail server. The payload carries the recipient email, message-id, subject, sender_email and uuid. Fires for every transactional email on the account, so filter on tags or subject to narrow it. Note tags is an array while tag is that same array JSON encoded as a string. ${WEBHOOK_ID_NOTE}`,
  type: "transactional",
  events: ["delivered"],
  sampleData: __spreadProps(__spreadValues({}, brevoSamples.transactionalEmail), {
    event: "delivered",
    reason: "sent"
  }),
  outputSchema: emailDeliveredTriggerOutputSchema
});

// ../ap/packages/pieces/community/sendinblue/src/lib/triggers/email-opened.ts
var emailOpened = brevoRegisterTrigger({
  name: "email_opened",
  displayName: "Transactional Email Opened",
  description: "Triggers when a recipient opens a transactional email.",
  aiDescription: `Fires when a recipient opens a transactional email sent from this Brevo account. Alongside the message-id and subject the payload carries user_agent and device_used, so use it to react to engagement. ${WEBHOOK_ID_NOTE}`,
  type: "transactional",
  events: ["opened"],
  sampleData: __spreadProps(__spreadValues({}, brevoSamples.engagement), { event: "opened" }),
  outputSchema: emailOpenedTriggerOutputSchema
});

// ../ap/packages/pieces/community/sendinblue/src/index.ts
var sendinblue = (0, import_pieces_framework11.createPiece)({
  displayName: "Brevo",
  description: "Formerly Sendinblue, is a SaaS solution for relationship marketing",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/brevo.png",
  authors: ["kanarelo", "BLaidzX", "Salem-Alaa", "kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  categories: [import_pieces_framework12.PieceCategory.MARKETING],
  auth: sendinblueAuth,
  actions: [
    createOrUpdateContact,
    findContact,
    unsubscribeContact,
    sendTransactionalEmail,
    sendTransactionalSms,
    createEvent,
    (0, import_pieces_common10.createCustomApiCallAction)({
      baseUrl: () => BREVO_API_URL,
      auth: sendinblueAuth,
      authMapping: async (auth) => ({
        "api-key": auth.secret_text
      })
    })
  ],
  triggers: [
    contactAddedToList,
    contactUpdated,
    contactDeleted,
    contactUnsubscribed,
    emailDelivered,
    emailOpened,
    emailClicked,
    emailBounced
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  sendinblue,
  sendinblueAuth
});
