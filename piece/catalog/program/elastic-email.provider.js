"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/elastic-email/src/index.ts
var index_exports = {};
__export(index_exports, {
  elasticEmail: () => elasticEmail
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common14 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/elastic-email/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/elastic-email/src/lib/common/constants.ts
var ELASTIC_EMAIL_API_BASE = "https://api.elasticemail.com/v4", CONTACT_STATUS_OPTIONS = [
  "Transactional",
  "Engaged",
  "Active",
  "Bounced",
  "Unsubscribed",
  "Abuse",
  "Inactive",
  "Stale",
  "NotConfirmed"
], CAMPAIGN_STATUS_OPTIONS = [
  "Deleted",
  "Active",
  "Processing",
  "Sending",
  "Completed",
  "Paused",
  "Cancelled",
  "Draft"
], BODY_CONTENT_TYPE_OPTIONS = [
  "HTML",
  "PlainText",
  "AMP",
  "CSS"
], ENCODING_OPTIONS = [
  "UserProvided",
  "None",
  "Raw7bit",
  "Raw8bit",
  "QuotedPrintable",
  "Base64",
  "Uue"
], CONSENT_TRACKING_OPTIONS = [
  "Unknown",
  "Allow",
  "Deny"
];

// ../ap/packages/pieces/community/elastic-email/src/lib/auth.ts
var elasticEmailAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To obtain your Elastic Email API key:
1. Log in to your Elastic Email account
2. Navigate to **Settings \u2192 Manage API Keys**
3. Click **Create API Key**
4. Ensure the key has the required permissions (Contacts, Campaigns, Emails, Segments)
5. Copy the key and paste it here
  `,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: `${ELASTIC_EMAIL_API_BASE}/contacts`,
        headers: {
          "X-ElasticEmail-ApiKey": auth
        },
        queryParams: { limit: "1" }
      })).status === 200 ? { valid: !0 } : {
        valid: !1,
        error: "Invalid API key \u2014 could not authenticate with Elastic Email."
      };
    } catch (error) {
      let statusCode = error != null && typeof error == "object" && "response" in error && error.response != null && typeof error.response == "object" && "status" in error.response ? error.response.status : void 0;
      return statusCode === 401 || statusCode === 403 ? {
        valid: !1,
        error: "Invalid API key \u2014 could not authenticate with Elastic Email."
      } : {
        valid: !1,
        error: "Could not reach the Elastic Email API. Check your network connection and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/elastic-email/src/lib/actions/add-contact.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/elastic-email/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common");
async function elasticEmailRequest({
  apiKey,
  method,
  path,
  queryParams,
  body
}) {
  return (await import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${ELASTIC_EMAIL_API_BASE}${path}`,
    headers: __spreadValues({
      "X-ElasticEmail-ApiKey": apiKey
    }, body ? { "Content-Type": "application/json" } : {}),
    queryParams,
    body
  })).body;
}

// ../ap/packages/pieces/community/elastic-email/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var contactEmailProp = import_pieces_framework2.Property.Dropdown({
  displayName: "Contact Email",
  description: "Select an existing contact by email address.",
  required: !0,
  refreshers: [],
  auth: elasticEmailAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Elastic Email account first.",
        options: []
      };
    let contacts = await elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/contacts",
      queryParams: { limit: String(100) }
    });
    return {
      disabled: !1,
      options: (contacts != null ? contacts : []).filter((c) => c.Email).map((c) => ({
        label: c.Email,
        value: c.Email
      }))
    };
  }
}), campaignNameProp = import_pieces_framework2.Property.Dropdown({
  displayName: "Campaign",
  description: "Select an existing campaign by name.",
  required: !0,
  refreshers: [],
  auth: elasticEmailAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Elastic Email account first.",
        options: []
      };
    let campaigns = await elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/campaigns",
      queryParams: { limit: String(100) }
    });
    return {
      disabled: !1,
      options: (campaigns != null ? campaigns : []).filter((c) => c.Name).map((c) => ({
        label: c.Name,
        value: c.Name
      }))
    };
  }
}), listNamesProp = import_pieces_framework2.Property.MultiSelectDropdown({
  displayName: "Mailing Lists",
  description: "Select mailing lists.",
  required: !1,
  refreshers: [],
  auth: elasticEmailAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Elastic Email account first.",
        options: []
      };
    let lists = await elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/lists",
      queryParams: { limit: String(100) }
    });
    return {
      disabled: !1,
      options: (lists != null ? lists : []).filter((l) => l.ListName).map((l) => ({
        label: l.ListName,
        value: l.ListName
      }))
    };
  }
}), segmentNamesProp = import_pieces_framework2.Property.MultiSelectDropdown({
  displayName: "Segments",
  description: "Select segments.",
  required: !1,
  refreshers: [],
  auth: elasticEmailAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Elastic Email account first.",
        options: []
      };
    let segments = await elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/segments",
      queryParams: { limit: String(100) }
    });
    return {
      disabled: !1,
      options: (segments != null ? segments : []).filter((s) => s.Name).map((s) => ({
        label: s.Name,
        value: s.Name
      }))
    };
  }
}), templateNameProp = import_pieces_framework2.Property.Dropdown({
  displayName: "Template",
  description: "Select an email template.",
  required: !1,
  refreshers: [],
  auth: elasticEmailAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Elastic Email account first.",
        options: []
      };
    let templates = await elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/templates",
      queryParams: { limit: String(100), scopeType: "Personal" }
    });
    return {
      disabled: !1,
      options: (templates != null ? templates : []).filter((t) => t.Name).map((t) => ({
        label: t.Name,
        value: t.Name
      }))
    };
  }
});

// ../ap/packages/pieces/community/elastic-email/src/lib/actions/add-contact.ts
var addContactAction = (0, import_pieces_framework3.createAction)({
  name: "add_contact",
  displayName: "Add Contact to Mailing List",
  description: "Add a new contact to one or more mailing lists in Elastic Email.",
  audience: "both",
  aiMetadata: {
    description: "Adds a contact (identified by email) to one or more named mailing lists in Elastic Email, with optional name, status, custom fields, and consent details. Use when subscribing someone to specific lists rather than just creating a bare contact. Each call appends the contact, so it is not idempotent.",
    idempotent: !1
  },
  auth: elasticEmailAuth,
  props: {
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "The email address of the contact.",
      required: !0
    }),
    listNames: listNamesProp,
    status: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Status",
      description: "Initial status of the contact.",
      required: !1,
      options: {
        options: CONTACT_STATUS_OPTIONS.map((s) => ({ label: s, value: s }))
      }
    }),
    firstName: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    customFields: import_pieces_framework3.Property.Object({
      displayName: "Custom Fields",
      description: "Key-value pairs of custom contact fields. Only existing custom fields will be saved.",
      required: !1
    }),
    consentIP: import_pieces_framework3.Property.ShortText({
      displayName: "Consent IP",
      description: "IP address of consent. If not provided, your current public IP is used.",
      required: !1
    }),
    consentDate: import_pieces_framework3.Property.ShortText({
      displayName: "Consent Date",
      description: "ISO 8601 date of consent. If not provided, current date is used.",
      required: !1
    }),
    consentTracking: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Consent Tracking",
      description: 'Consent tracking level. Defaults to "Unknown".',
      required: !1,
      options: {
        options: CONSENT_TRACKING_OPTIONS.map((s) => ({ label: s, value: s }))
      }
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b, _c, _d, _e, _f, _g;
    let queryParams = {};
    return propsValue.listNames && (queryParams.listnames = Array.isArray(propsValue.listNames) ? propsValue.listNames.join(",") : String(propsValue.listNames)), elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/contacts",
      queryParams: Object.keys(queryParams).length > 0 ? queryParams : void 0,
      body: [
        {
          Email: propsValue.email,
          Status: (_a = propsValue.status) != null ? _a : void 0,
          FirstName: (_b = propsValue.firstName) != null ? _b : void 0,
          LastName: (_c = propsValue.lastName) != null ? _c : void 0,
          CustomFields: (_d = propsValue.customFields) != null ? _d : void 0,
          Consent: {
            ConsentIP: (_e = propsValue.consentIP) != null ? _e : void 0,
            ConsentDate: (_f = propsValue.consentDate) != null ? _f : void 0,
            ConsentTracking: (_g = propsValue.consentTracking) != null ? _g : void 0
          }
        }
      ]
    });
  }
});

// ../ap/packages/pieces/community/elastic-email/src/lib/actions/create-campaign.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var createCampaignAction = (0, import_pieces_framework4.createAction)({
  name: "create_campaign",
  displayName: "Create Campaign",
  description: "Create a new email campaign in Elastic Email.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new email campaign in Elastic Email targeting the given mailing lists and/or segments, with sender, subject, template, and an initial status (e.g. draft or active). Use to set up a bulk campaign send; for one-off transactional messages use Send Email instead. Each call creates a separate campaign, so it is not idempotent.",
    idempotent: !1
  },
  auth: elasticEmailAuth,
  props: {
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Campaign Name",
      required: !0
    }),
    from: import_pieces_framework4.Property.ShortText({
      displayName: "From",
      description: "Sender email with optional name (e.g. `email@domain.com` or `John Doe <email@domain.com>`).",
      required: !0
    }),
    recipientListNames: listNamesProp,
    recipientSegmentNames: segmentNamesProp,
    replyTo: import_pieces_framework4.Property.ShortText({
      displayName: "Reply To",
      description: "Reply-to email address.",
      required: !0
    }),
    status: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Status",
      description: "Initial campaign status.",
      required: !0,
      options: {
        options: CAMPAIGN_STATUS_OPTIONS.map((s) => ({ label: s, value: s }))
      }
    }),
    subject: import_pieces_framework4.Property.ShortText({
      displayName: "Subject",
      description: "Default email subject line.",
      required: !1
    }),
    templateName: templateNameProp
  },
  async run({ auth, propsValue }) {
    var _a, _b;
    return elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/campaigns",
      body: {
        Name: propsValue.name,
        Recipients: {
          ListNames: propsValue.recipientListNames && propsValue.recipientListNames.length > 0 ? propsValue.recipientListNames : void 0,
          SegmentNames: propsValue.recipientSegmentNames && propsValue.recipientSegmentNames.length > 0 ? propsValue.recipientSegmentNames : void 0
        },
        Content: [
          {
            From: propsValue.from,
            ReplyTo: propsValue.replyTo,
            Subject: (_a = propsValue.subject) != null ? _a : void 0,
            TemplateName: (_b = propsValue.templateName) != null ? _b : void 0
          }
        ],
        Status: propsValue.status
      }
    });
  }
});

// ../ap/packages/pieces/community/elastic-email/src/lib/actions/create-contact.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var createContactAction = (0, import_pieces_framework5.createAction)({
  name: "create_contact",
  displayName: "Create Contact",
  description: "Create a new contact in Elastic Email.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new contact in Elastic Email from an email address, with optional name, status, and custom fields, without attaching it to any mailing list. Use when you only need the contact to exist; use Add Contact to Mailing List when it must also join specific lists. Each call appends the contact, so it is not idempotent.",
    idempotent: !1
  },
  auth: elasticEmailAuth,
  props: {
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      description: "The email address of the contact.",
      required: !0
    }),
    status: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Status",
      description: "Initial status of the contact.",
      required: !1,
      options: {
        options: CONTACT_STATUS_OPTIONS.map((s) => ({ label: s, value: s }))
      }
    }),
    firstName: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    customFields: import_pieces_framework5.Property.Object({
      displayName: "Custom Fields",
      description: "Key-value pairs of custom contact fields. Only existing custom fields will be saved.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b, _c, _d;
    return elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      path: "/contacts",
      body: [
        {
          Email: propsValue.email,
          Status: (_a = propsValue.status) != null ? _a : void 0,
          FirstName: (_b = propsValue.firstName) != null ? _b : void 0,
          LastName: (_c = propsValue.lastName) != null ? _c : void 0,
          CustomFields: (_d = propsValue.customFields) != null ? _d : void 0
        }
      ]
    });
  }
});

// ../ap/packages/pieces/community/elastic-email/src/lib/actions/create-segment.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var createSegmentAction = (0, import_pieces_framework6.createAction)({
  name: "create_segment",
  displayName: "Create Segment",
  description: "Create a new contact segment in Elastic Email using a SQL-like rule.",
  audience: "both",
  aiMetadata: {
    description: "Creates a named contact segment in Elastic Email defined by a SQL-like rule that selects which contacts belong to it. Use to build a reusable audience for campaigns. The rule must follow Elastic Email segment-rule syntax. Each call creates a separate segment, so it is not idempotent.",
    idempotent: !1
  },
  auth: elasticEmailAuth,
  props: {
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Segment Name",
      required: !0
    }),
    rule: import_pieces_framework6.Property.ShortText({
      displayName: "Rule",
      description: "SQL-like rule to determine which contacts belong to this segment. See https://help.elasticemail.com/en/articles/5162182-segment-rules",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common7.HttpMethod.POST,
      path: "/segments",
      body: {
        Name: propsValue.name,
        Rule: propsValue.rule
      }
    });
  }
});

// ../ap/packages/pieces/community/elastic-email/src/lib/actions/list-campaigns.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var listCampaignsAction = (0, import_pieces_framework7.createAction)({
  name: "list_campaigns",
  displayName: "List Campaigns",
  description: "Fetch a paginated list of campaigns from Elastic Email.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a paginated list of campaigns from Elastic Email; leave the search field empty to return all campaigns, or set it to filter by campaign name. Use to look up campaigns or find a campaign name before updating it. Read-only and idempotent.",
    idempotent: !0
  },
  auth: elasticEmailAuth,
  props: {
    search: import_pieces_framework7.Property.ShortText({
      displayName: "Search",
      description: "Filter campaigns by name.",
      required: !1
    }),
    limit: import_pieces_framework7.Property.Number({
      displayName: "Limit",
      description: "Maximum number of campaigns to return (max 100).",
      required: !1,
      defaultValue: 100
    }),
    offset: import_pieces_framework7.Property.Number({
      displayName: "Offset",
      description: "Number of campaigns to skip.",
      required: !1,
      defaultValue: 0
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = {};
    return propsValue.search && (queryParams.search = propsValue.search), propsValue.limit !== void 0 && (queryParams.limit = String(propsValue.limit)), propsValue.offset !== void 0 && (queryParams.offset = String(propsValue.offset)), elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common8.HttpMethod.GET,
      path: "/campaigns",
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/elastic-email/src/lib/actions/list-contacts.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var listContactsAction = (0, import_pieces_framework8.createAction)({
  name: "list_contacts",
  displayName: "List Contacts",
  description: "Fetch a paginated list of contacts from Elastic Email.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a paginated list of contacts from the Elastic Email account using limit and offset. Use to browse or page through contacts. Read-only and idempotent.",
    idempotent: !0
  },
  auth: elasticEmailAuth,
  props: {
    limit: import_pieces_framework8.Property.Number({
      displayName: "Limit",
      description: "Maximum number of contacts to return (max 100).",
      required: !1,
      defaultValue: 100
    }),
    offset: import_pieces_framework8.Property.Number({
      displayName: "Offset",
      description: "Number of contacts to skip.",
      required: !1,
      defaultValue: 0
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = {};
    return propsValue.limit !== void 0 && (queryParams.limit = String(propsValue.limit)), propsValue.offset !== void 0 && (queryParams.offset = String(propsValue.offset)), elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common9.HttpMethod.GET,
      path: "/contacts",
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/elastic-email/src/lib/actions/send-email.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var sendEmailAction = (0, import_pieces_framework9.createAction)({
  name: "send_email",
  displayName: "Send Email",
  description: "Send an email to one or more recipients via Elastic Email.",
  audience: "both",
  aiMetadata: {
    description: "Sends a transactional email to one or more recipients via Elastic Email, with either inline body content or a saved template, plus optional merge fields, tracking, and a scheduled time offset. Use for direct one-off sends; use Create Campaign for bulk list/segment marketing. Each call dispatches a new email, so it is not idempotent.",
    idempotent: !1
  },
  auth: elasticEmailAuth,
  props: {
    recipients: import_pieces_framework9.Property.Array({
      displayName: "Recipients",
      description: "List of recipient email addresses.",
      required: !0
    }),
    from: import_pieces_framework9.Property.ShortText({
      displayName: "From",
      description: "Sender email with optional name (e.g. `email@domain.com` or `John Doe <email@domain.com>`).",
      required: !0
    }),
    subject: import_pieces_framework9.Property.ShortText({
      displayName: "Subject",
      description: "Email subject line.",
      required: !1
    }),
    bodyContentType: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Body Content Type",
      description: "Type of body content.",
      required: !1,
      options: {
        options: BODY_CONTENT_TYPE_OPTIONS.map((s) => ({ label: s, value: s }))
      }
    }),
    bodyContent: import_pieces_framework9.Property.LongText({
      displayName: "Body Content",
      description: "The actual body content of the email.",
      required: !1
    }),
    replyTo: import_pieces_framework9.Property.ShortText({
      displayName: "Reply To",
      description: "Reply-to email address.",
      required: !1
    }),
    templateName: templateNameProp,
    merge: import_pieces_framework9.Property.Object({
      displayName: "Merge Fields",
      description: "Key-value merge fields shared between recipients. Use in email body like {firstname}, {lastname}.",
      required: !1
    }),
    timeOffset: import_pieces_framework9.Property.Number({
      displayName: "Time Offset (minutes)",
      description: "Delay sending by this many minutes. Maximum is 35 days (50400 minutes).",
      required: !1
    }),
    poolName: import_pieces_framework9.Property.ShortText({
      displayName: "Pool Name",
      description: "Name of custom IP pool for sending.",
      required: !1
    }),
    channelName: import_pieces_framework9.Property.ShortText({
      displayName: "Channel Name",
      description: "Name of the sending channel.",
      required: !1
    }),
    encoding: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Encoding",
      description: "Encoding type for email headers.",
      required: !1,
      options: {
        options: ENCODING_OPTIONS.map((s) => ({ label: s, value: s }))
      }
    }),
    trackOpens: import_pieces_framework9.Property.Checkbox({
      displayName: "Track Opens",
      description: "Track email opens. If not set, uses account default.",
      required: !1
    }),
    trackClicks: import_pieces_framework9.Property.Checkbox({
      displayName: "Track Clicks",
      description: "Track link clicks. If not set, uses account default.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k;
    let recipientEmails = ((_a = propsValue.recipients) != null ? _a : []).map(String);
    return elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common10.HttpMethod.POST,
      path: "/emails",
      body: {
        Recipients: recipientEmails.map((email) => ({ Email: email })),
        Content: {
          From: propsValue.from,
          Subject: (_b = propsValue.subject) != null ? _b : void 0,
          Body: propsValue.bodyContentType || propsValue.bodyContent ? [
            {
              ContentType: propsValue.bodyContentType || "HTML",
              Body: propsValue.bodyContent
            }
          ] : void 0,
          ReplyTo: (_c = propsValue.replyTo) != null ? _c : void 0,
          TemplateName: (_d = propsValue.templateName) != null ? _d : void 0,
          Merge: (_e = propsValue.merge) != null ? _e : void 0
        },
        Options: {
          TimeOffset: (_f = propsValue.timeOffset) != null ? _f : void 0,
          PoolName: (_g = propsValue.poolName) != null ? _g : void 0,
          ChannelName: (_h = propsValue.channelName) != null ? _h : void 0,
          Encoding: (_i = propsValue.encoding) != null ? _i : void 0,
          TrackOpens: (_j = propsValue.trackOpens) != null ? _j : void 0,
          TrackClicks: (_k = propsValue.trackClicks) != null ? _k : void 0
        }
      }
    });
  }
});

// ../ap/packages/pieces/community/elastic-email/src/lib/actions/unsubscribe-contact.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var unsubscribeContactAction = (0, import_pieces_framework10.createAction)({
  name: "unsubscribe_contact",
  displayName: "Unsubscribe Contact",
  description: "Unsubscribe one or more email addresses from future emails in Elastic Email.",
  audience: "both",
  aiMetadata: {
    description: "Marks one or more email addresses as unsubscribed in Elastic Email so they no longer receive future emails. Use to honor opt-out or suppression requests. Idempotent: re-running with the same addresses leaves them unsubscribed with no additional effect.",
    idempotent: !0
  },
  auth: elasticEmailAuth,
  props: {
    emails: import_pieces_framework10.Property.Array({
      displayName: "Email Addresses",
      description: "List of email addresses to unsubscribe.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let emails = ((_a = propsValue.emails) != null ? _a : []).map(String);
    return elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common11.HttpMethod.POST,
      path: "/suppressions/unsubscribes",
      body: emails
    });
  }
});

// ../ap/packages/pieces/community/elastic-email/src/lib/actions/update-campaign.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var updateCampaignAction = (0, import_pieces_framework11.createAction)({
  name: "update_campaign",
  displayName: "Update Campaign",
  description: "Update an existing campaign in Elastic Email.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing campaign in Elastic Email, identified by its name, merging any provided fields (name, sender, recipients, subject, template, status) with the campaign's current values so omitted fields are preserved. Use to revise a campaign before it sends. Idempotent: applying the same values yields the same campaign state.",
    idempotent: !0
  },
  auth: elasticEmailAuth,
  props: {
    campaign: campaignNameProp,
    name: import_pieces_framework11.Property.ShortText({
      displayName: "New Campaign Name",
      description: "New name for the campaign. Leave empty to keep existing.",
      required: !1
    }),
    from: import_pieces_framework11.Property.ShortText({
      displayName: "From",
      description: "Sender email with optional name. Leave empty to keep existing.",
      required: !1
    }),
    recipientListNames: listNamesProp,
    recipientSegmentNames: segmentNamesProp,
    replyTo: import_pieces_framework11.Property.ShortText({
      displayName: "Reply To",
      description: "Reply-to email address. Leave empty to keep existing.",
      required: !1
    }),
    subject: import_pieces_framework11.Property.ShortText({
      displayName: "Subject",
      description: "Email subject. Leave empty to keep existing.",
      required: !1
    }),
    templateName: templateNameProp,
    status: import_pieces_framework11.Property.StaticDropdown({
      displayName: "Status",
      description: "Campaign status. Leave empty to keep existing.",
      required: !1,
      options: {
        options: CAMPAIGN_STATUS_OPTIONS.map((s) => ({ label: s, value: s }))
      }
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b, _c, _d, _e, _f;
    let campaignName = String(propsValue.campaign), current = await elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common12.HttpMethod.GET,
      path: `/campaigns/${encodeURIComponent(campaignName)}`
    }), currentContent = (_b = (_a = current.Content) == null ? void 0 : _a[0]) != null ? _b : {};
    return elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common12.HttpMethod.PUT,
      path: `/campaigns/${encodeURIComponent(campaignName)}`,
      body: {
        Name: propsValue.name || current.Name,
        Recipients: {
          ListNames: propsValue.recipientListNames && propsValue.recipientListNames.length > 0 ? propsValue.recipientListNames : (_d = (_c = current.Recipients) == null ? void 0 : _c.ListNames) != null ? _d : void 0,
          SegmentNames: propsValue.recipientSegmentNames && propsValue.recipientSegmentNames.length > 0 ? propsValue.recipientSegmentNames : (_f = (_e = current.Recipients) == null ? void 0 : _e.SegmentNames) != null ? _f : void 0
        },
        Content: [
          {
            From: propsValue.from || currentContent.From,
            ReplyTo: propsValue.replyTo || currentContent.ReplyTo,
            Subject: propsValue.subject || currentContent.Subject,
            TemplateName: propsValue.templateName || currentContent.TemplateName
          }
        ],
        Status: propsValue.status || current.Status
      }
    });
  }
});

// ../ap/packages/pieces/community/elastic-email/src/lib/actions/update-contact.ts
var import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework");
var updateContactAction = (0, import_pieces_framework12.createAction)({
  name: "update_contact",
  displayName: "Update Contact",
  description: "Update an existing contact in Elastic Email.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing contact in Elastic Email, identified by email address, setting first name, last name, and/or custom fields. Use to edit contact details after creation. Idempotent: applying the same values yields the same contact state.",
    idempotent: !0
  },
  auth: elasticEmailAuth,
  props: {
    contact: contactEmailProp,
    firstName: import_pieces_framework12.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    lastName: import_pieces_framework12.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    customFields: import_pieces_framework12.Property.Object({
      displayName: "Custom Fields",
      description: "Key-value pairs of custom contact fields. Only existing custom fields will be saved.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    var _a, _b, _c;
    return elasticEmailRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common13.HttpMethod.PUT,
      path: `/contacts/${encodeURIComponent(String(propsValue.contact))}`,
      body: {
        FirstName: (_a = propsValue.firstName) != null ? _a : void 0,
        LastName: (_b = propsValue.lastName) != null ? _b : void 0,
        CustomFields: (_c = propsValue.customFields) != null ? _c : void 0
      }
    });
  }
});

// ../ap/packages/pieces/community/elastic-email/src/index.ts
var elasticEmail = (0, import_pieces_framework13.createPiece)({
  displayName: "Elastic Email",
  description: "Email delivery and marketing platform for sending emails, managing contacts, campaigns, and segments.",
  auth: elasticEmailAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/elastic-email.png",
  authors: ["Harmatta"],
  categories: [import_pieces_framework14.PieceCategory.MARKETING],
  actions: [
    addContactAction,
    createCampaignAction,
    createContactAction,
    createSegmentAction,
    listCampaignsAction,
    listContactsAction,
    sendEmailAction,
    unsubscribeContactAction,
    updateCampaignAction,
    updateContactAction,
    (0, import_pieces_common14.createCustomApiCallAction)({
      baseUrl: () => ELASTIC_EMAIL_API_BASE,
      auth: elasticEmailAuth,
      authMapping: async (auth) => ({
        "X-ElasticEmail-ApiKey": auth.secret_text
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  elasticEmail
});
