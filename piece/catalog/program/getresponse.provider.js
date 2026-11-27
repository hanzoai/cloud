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

// ../ap/packages/pieces/community/getresponse/src/index.ts
var index_exports = {};
__export(index_exports, {
  getresponse: () => getresponse,
  getresponseAuth: () => getresponseAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/getresponse/src/lib/actions/create-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/getresponse/src/lib/common/auth.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/getresponse/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");
async function validateGetResponseAuth(auth) {
  await sendGetRequest({
    auth,
    path: "/campaigns",
    queryParams: {
      perPage: "1",
      page: "1"
    }
  });
}
async function listGetResponseCampaigns({
  auth,
  limit = 1e3,
  name
}) {
  return sendGetRequest({
    auth,
    path: "/campaigns",
    queryParams: __spreadValues({
      perPage: String(Math.min(limit, 1e3)),
      page: "1"
    }, name ? { "query[name]": name } : {})
  });
}
async function listGetResponseFromFields(auth) {
  return sendGetRequest({
    auth,
    path: "/from-fields",
    queryParams: {
      perPage: "1000",
      page: "1",
      "query[isActive]": "true"
    }
  });
}
async function listGetResponseContacts({
  auth,
  email,
  campaignId
}) {
  return sendGetRequest({
    auth,
    path: "/contacts",
    queryParams: __spreadValues({
      perPage: "1000",
      page: "1",
      "query[email]": email
    }, campaignId ? { "query[campaignId]": campaignId } : {})
  });
}
async function createGetResponseContact({
  auth,
  request
}) {
  return sendPostRequest({
    auth,
    path: "/contacts",
    body: request
  });
}
async function updateGetResponseContact({
  auth,
  contactId,
  request
}) {
  return sendPostRequest({
    auth,
    path: `/contacts/${contactId}`,
    body: request
  });
}
async function createGetResponseNewsletter({
  auth,
  request
}) {
  return sendPostRequest({
    auth,
    path: "/newsletters",
    body: request
  });
}
function flattenGetResponseCampaign(campaign) {
  var _a, _b, _c, _d, _e, _f;
  return {
    campaign_id: campaign.campaignId,
    name: (_a = campaign.name) != null ? _a : null,
    description: (_b = campaign.description) != null ? _b : null,
    language_code: (_c = campaign.languageCode) != null ? _c : null,
    is_default: (_d = campaign.isDefault) != null ? _d : null,
    created_on: (_e = campaign.createdOn) != null ? _e : null,
    href: (_f = campaign.href) != null ? _f : null
  };
}
function flattenGetResponseContact(contact) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n;
  return {
    contact_id: contact.contactId,
    name: (_a = contact.name) != null ? _a : null,
    email: contact.email,
    campaign_id: (_c = (_b = contact.campaign) == null ? void 0 : _b.campaignId) != null ? _c : null,
    campaign_name: (_e = (_d = contact.campaign) == null ? void 0 : _d.name) != null ? _e : null,
    day_of_cycle: (_f = contact.dayOfCycle) != null ? _f : null,
    scoring: (_g = contact.scoring) != null ? _g : null,
    engagement_score: (_h = contact.engagementScore) != null ? _h : null,
    origin: (_i = contact.origin) != null ? _i : null,
    time_zone: (_j = contact.timeZone) != null ? _j : null,
    note: (_k = contact.note) != null ? _k : null,
    tags: formatContactTags(contact.tags),
    custom_field_values: formatContactCustomFieldValues(
      contact.customFieldValues
    ),
    created_on: (_l = contact.createdOn) != null ? _l : null,
    changed_on: (_m = contact.changedOn) != null ? _m : null,
    href: (_n = contact.href) != null ? _n : null
  };
}
function flattenGetResponseNewsletter(newsletter) {
  var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r, _s, _t, _u;
  return {
    newsletter_id: newsletter.newsletterId,
    name: (_a = newsletter.name) != null ? _a : null,
    subject: (_b = newsletter.subject) != null ? _b : null,
    type: (_c = newsletter.type) != null ? _c : null,
    status: (_d = newsletter.status) != null ? _d : null,
    campaign_id: (_f = (_e = newsletter.campaign) == null ? void 0 : _e.campaignId) != null ? _f : null,
    campaign_name: (_h = (_g = newsletter.campaign) == null ? void 0 : _g.name) != null ? _h : null,
    from_field_id: (_j = (_i = newsletter.fromField) == null ? void 0 : _i.fromFieldId) != null ? _j : null,
    reply_to_field_id: (_l = (_k = newsletter.replyTo) == null ? void 0 : _k.fromFieldId) != null ? _l : null,
    selected_campaigns: (_n = (_m = newsletter.sendSettings) == null ? void 0 : _m.selectedCampaigns.join(", ")) != null ? _n : null,
    content_html: (_p = (_o = newsletter.content) == null ? void 0 : _o.html) != null ? _p : null,
    content_plain: (_r = (_q = newsletter.content) == null ? void 0 : _q.plain) != null ? _r : null,
    send_on: (_s = newsletter.sendOn) != null ? _s : null,
    created_on: (_t = newsletter.createdOn) != null ? _t : null,
    href: (_u = newsletter.href) != null ? _u : null
  };
}
function sendGetRequest({
  auth,
  path,
  queryParams
}) {
  return sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.GET,
    path,
    queryParams
  });
}
function sendPostRequest({
  auth,
  path,
  body
}) {
  return sendRequest({
    auth,
    method: import_pieces_common.HttpMethod.POST,
    path,
    body
  });
}
async function sendRequest({
  auth,
  method,
  path,
  body,
  queryParams
}) {
  let request = {
    method,
    url: `${GETRESPONSE_API_URL}${path}`,
    headers: createHeaders(auth),
    queryParams,
    body
  };
  return (await import_pieces_common.httpClient.sendRequest(request)).body;
}
function createHeaders(auth) {
  let headers = {
    Accept: "application/json",
    "Content-Type": "application/json"
  };
  return auth.type === import_pieces_framework.AppConnectionType.OAUTH2 ? headers.Authorization = `Bearer ${auth.access_token}` : headers["X-Auth-Token"] = `api-key ${auth.props.apiKey}`, headers;
}
function formatContactTags(tags) {
  return !tags || tags.length === 0 ? null : tags.map((tag) => {
    var _a;
    return (_a = tag.name) != null ? _a : tag.tagId;
  }).join(", ");
}
function formatContactCustomFieldValues(customFieldValues) {
  return !customFieldValues || customFieldValues.length === 0 ? null : customFieldValues.map((customFieldValue) => {
    let joinedValues = customFieldValue.value.join("|");
    return `${customFieldValue.customFieldId}:${joinedValues}`;
  }).join(", ");
}
var GETRESPONSE_API_URL = "https://api.getresponse.com/v3";

// ../ap/packages/pieces/community/getresponse/src/lib/common/auth.ts
var getresponseAuth = [
  import_pieces_framework2.PieceAuth.OAuth2({
    description: "Connect your GetResponse account using OAuth2.",
    authUrl: "https://app.getresponse.com/oauth2_authorize.html",
    tokenUrl: "https://api.getresponse.com/v3/token",
    required: !0,
    scope: []
  }),
  import_pieces_framework2.PieceAuth.CustomAuth({
    displayName: "API Key",
    description: `Authenticate using a GetResponse API key.

To get your API key:
1. Sign in to your GetResponse account.
2. Go to **Tools > Integrations and API > API**.
3. Create or copy an API key.
4. Paste it below.`,
    required: !0,
    props: {
      apiKey: import_pieces_framework2.PieceAuth.SecretText({
        displayName: "API Key",
        description: "Your GetResponse API key.",
        required: !0
      })
    },
    validate: async ({ auth }) => {
      try {
        return await validateGetResponseAuth({
          type: import_pieces_framework3.AppConnectionType.CUSTOM_AUTH,
          props: { apiKey: auth.apiKey }
        }), { valid: !0 };
      } catch (e) {
        return { valid: !1, error: "Invalid API key." };
      }
    }
  })
];

// ../ap/packages/pieces/community/getresponse/src/lib/common/props.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var getresponseProps = {
  campaign: (required = !0) => import_pieces_framework4.Property.Dropdown({
    auth: getresponseAuth,
    displayName: "Campaign",
    description: "Select the campaign list to use.",
    required,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Connect your account first"
        };
      let campaigns = await listGetResponseCampaigns({
        auth
      });
      return campaigns.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No campaigns found. Create one in GetResponse first."
      } : {
        disabled: !1,
        options: campaigns.map((campaign) => {
          var _a;
          return {
            label: (_a = campaign.name) != null ? _a : campaign.campaignId,
            value: campaign.campaignId
          };
        })
      };
    }
  }),
  fromField: (required = !0, displayName = "From Address") => import_pieces_framework4.Property.Dropdown({
    auth: getresponseAuth,
    displayName,
    description: "Select the verified sender address to use.",
    required,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Connect your account first"
        };
      let fromFields = await listGetResponseFromFields(auth);
      return fromFields.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No active sender addresses found in GetResponse."
      } : {
        disabled: !1,
        options: fromFields.map((fromField) => ({
          label: fromField.name ? `${fromField.name} (${fromField.email})` : fromField.email,
          value: fromField.fromFieldId
        }))
      };
    }
  })
};

// ../ap/packages/pieces/community/getresponse/src/lib/common/utils.ts
function requireString(value, fieldName) {
  if (!value)
    throw new Error(`${fieldName} is required.`);
  return value;
}

// ../ap/packages/pieces/community/getresponse/src/lib/actions/create-contact.ts
var createContactAction = (0, import_pieces_framework5.createAction)({
  auth: getresponseAuth,
  name: "create-contact",
  displayName: "Create Contact",
  description: "Creates a new contact in a GetResponse campaign.",
  audience: "both",
  aiMetadata: {
    description: "Adds a new subscriber to a GetResponse campaign (list) by email, optionally with a name. Use when you intend to create a contact unconditionally; it does not check for an existing contact, so repeated calls with the same email may add duplicates. Requires a campaign ID and an email address. Not idempotent. To avoid duplicates, prefer Create or Update Contact.",
    idempotent: !1
  },
  props: {
    campaignId: getresponseProps.campaign(),
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email Address",
      description: "The email address of the contact to create.",
      required: !0
    }),
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Name",
      description: "The contact name as it should appear in GetResponse.",
      required: !1
    })
  },
  async run(context) {
    let campaignId = requireString(
      context.propsValue.campaignId,
      "Campaign"
    ), email = requireString(context.propsValue.email, "Email Address"), contact = await createGetResponseContact({
      auth: context.auth,
      request: __spreadValues({
        email,
        campaign: {
          campaignId
        }
      }, context.propsValue.name ? { name: context.propsValue.name } : {})
    });
    return flattenGetResponseContact(contact);
  }
});

// ../ap/packages/pieces/community/getresponse/src/lib/actions/create-newsletter.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var createNewsletterAction = (0, import_pieces_framework6.createAction)({
  auth: getresponseAuth,
  name: "create-newsletter",
  displayName: "Create Newsletter",
  description: "Creates a newsletter and queues it to send to the selected campaign.",
  audience: "both",
  aiMetadata: {
    description: "Creates a newsletter (email broadcast) and queues it to send to all subscribers of the selected GetResponse campaign. Use to send a one-off email blast to a list. Requires a campaign ID, a verified from-field address, and a subject, plus HTML and/or plain-text content (at least one is required). Not idempotent: each call creates and dispatches a new newsletter, so repeating it sends another email.",
    idempotent: !1
  },
  props: {
    campaignId: getresponseProps.campaign(),
    fromFieldId: getresponseProps.fromField(),
    replyToFieldId: getresponseProps.fromField(!1, "Reply-to Address"),
    subject: import_pieces_framework6.Property.ShortText({
      displayName: "Subject",
      description: "The subject line for the newsletter.",
      required: !0
    }),
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Newsletter Name",
      description: "An internal name for the newsletter inside GetResponse. Leave empty to use the subject.",
      required: !1
    }),
    htmlContent: import_pieces_framework6.Property.LongText({
      displayName: "HTML Content",
      description: "The newsletter content in HTML format.",
      required: !1
    }),
    plainContent: import_pieces_framework6.Property.LongText({
      displayName: "Plain Text Content",
      description: "The plain text version of the newsletter content.",
      required: !1
    })
  },
  async run(context) {
    let campaignId = requireString(
      context.propsValue.campaignId,
      "Campaign"
    ), fromFieldId = requireString(
      context.propsValue.fromFieldId,
      "From Address"
    ), subject = requireString(context.propsValue.subject, "Subject");
    if (!context.propsValue.htmlContent && !context.propsValue.plainContent)
      throw new Error(
        "Provide HTML Content, Plain Text Content, or both before creating the newsletter."
      );
    let newsletter = await createGetResponseNewsletter({
      auth: context.auth,
      request: __spreadValues(__spreadValues({
        subject,
        fromField: {
          fromFieldId
        },
        campaign: {
          campaignId
        },
        content: __spreadValues(__spreadValues({}, context.propsValue.htmlContent ? { html: context.propsValue.htmlContent } : {}), context.propsValue.plainContent ? { plain: context.propsValue.plainContent } : {}),
        sendSettings: {
          selectedCampaigns: [campaignId]
        }
      }, context.propsValue.name ? { name: context.propsValue.name } : {}), context.propsValue.replyToFieldId ? {
        replyTo: {
          fromFieldId: context.propsValue.replyToFieldId
        }
      } : {})
    });
    return flattenGetResponseNewsletter(newsletter);
  }
});

// ../ap/packages/pieces/community/getresponse/src/lib/actions/create-or-update-contact.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var createOrUpdateContactAction = (0, import_pieces_framework7.createAction)({
  auth: getresponseAuth,
  name: "create-or-update-contact",
  displayName: "Create or Update Contact",
  description: "Creates a contact or updates the existing contact with the same email in the selected campaign.",
  audience: "both",
  aiMetadata: {
    description: "Upserts a GetResponse contact in a campaign keyed on email: looks up the campaign for a contact with the given email, then updates it if found or creates it otherwise (the result reports which operation ran). Use this rather than Create Contact to avoid duplicate subscribers. Requires a campaign ID and an email; effectively idempotent for the same email and campaign.",
    idempotent: !0
  },
  props: {
    campaignId: getresponseProps.campaign(),
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Email Address",
      description: "The email address used to find or create the contact.",
      required: !0
    }),
    name: import_pieces_framework7.Property.ShortText({
      displayName: "Name",
      description: "The contact name as it should appear in GetResponse.",
      required: !1
    })
  },
  async run(context) {
    let campaignId = requireString(
      context.propsValue.campaignId,
      "Campaign"
    ), email = requireString(context.propsValue.email, "Email Address"), existingContact = await findExactContact({
      auth: context.auth,
      email,
      campaignId
    });
    if (!existingContact) {
      let createdContact = await createGetResponseContact({
        auth: context.auth,
        request: __spreadValues({
          email,
          campaign: {
            campaignId
          }
        }, context.propsValue.name ? { name: context.propsValue.name } : {})
      });
      return __spreadValues({
        operation: "created"
      }, flattenGetResponseContact(createdContact));
    }
    let updatedContact = await updateGetResponseContact({
      auth: context.auth,
      contactId: existingContact.contactId,
      request: __spreadValues({
        email,
        campaign: {
          campaignId
        }
      }, context.propsValue.name ? { name: context.propsValue.name } : {})
    });
    return __spreadValues({
      operation: "updated"
    }, flattenGetResponseContact(updatedContact));
  }
});
async function findExactContact({
  auth,
  email,
  campaignId
}) {
  return (await listGetResponseContacts({
    auth,
    email,
    campaignId
  })).find(
    (contact) => contact.email.toLowerCase() === email.toLowerCase()
  );
}

// ../ap/packages/pieces/community/getresponse/src/lib/actions/find-campaign-list.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var findCampaignListAction = (0, import_pieces_framework8.createAction)({
  auth: getresponseAuth,
  name: "find-campaign-list",
  displayName: "Find Campaign List",
  description: "Finds campaign lists by name.",
  audience: "both",
  aiMetadata: {
    description: "Searches GetResponse campaigns (lists) by a partial or full name match, or returns the first lists in the account when the name is left empty. Use to resolve a campaign list ID or to enumerate available lists before creating contacts or newsletters. Read-only and idempotent; a limit caps how many lists are returned.",
    idempotent: !0
  },
  props: {
    name: import_pieces_framework8.Property.ShortText({
      displayName: "Campaign Name",
      description: "Enter part or all of the campaign list name. Leave empty to return the first lists in the account.",
      required: !1
    }),
    limit: import_pieces_framework8.Property.Number({
      displayName: "Limit",
      description: "The maximum number of campaign lists to return.",
      required: !1,
      defaultValue: 100
    })
  },
  async run(context) {
    var _a;
    return (await listGetResponseCampaigns(__spreadValues({
      auth: context.auth,
      limit: (_a = context.propsValue.limit) != null ? _a : 100
    }, context.propsValue.name ? { name: context.propsValue.name } : {}))).map(flattenGetResponseCampaign);
  }
});

// ../ap/packages/pieces/community/getresponse/src/lib/actions/find-contact.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var findContactAction = (0, import_pieces_framework9.createAction)({
  auth: getresponseAuth,
  name: "find-contact",
  displayName: "Find Contact",
  description: "Finds a contact by email address.",
  audience: "both",
  aiMetadata: {
    description: "Looks up a GetResponse contact by exact email address, optionally scoped to a single campaign; without a campaign it searches across the account. Use to check whether a subscriber exists or to retrieve their details before acting. Read-only and idempotent; returns a found flag indicating whether a match was located.",
    idempotent: !0
  },
  props: {
    email: import_pieces_framework9.Property.ShortText({
      displayName: "Email Address",
      description: "The email address of the contact to find.",
      required: !0
    }),
    campaignId: getresponseProps.campaign(!1)
  },
  async run(context) {
    var _a;
    let matchingContact = (await listGetResponseContacts({
      auth: context.auth,
      email: context.propsValue.email,
      campaignId: context.propsValue.campaignId
    })).find(
      (contact) => contact.email.toLowerCase() === context.propsValue.email.toLowerCase()
    );
    return matchingContact ? __spreadValues({
      found: !0
    }, flattenGetResponseContact(matchingContact)) : {
      found: !1,
      email: context.propsValue.email,
      campaign_id: (_a = context.propsValue.campaignId) != null ? _a : null
    };
  }
});

// ../ap/packages/pieces/community/getresponse/src/index.ts
var getresponse = (0, import_pieces_framework10.createPiece)({
  displayName: "GetResponse",
  description: "Email marketing and automation platform for contacts, campaigns, and newsletters.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/getresponse.png",
  categories: [import_pieces_framework11.PieceCategory.MARKETING],
  authors: ["veri5ied"],
  auth: getresponseAuth,
  actions: [
    createContactAction,
    createOrUpdateContactAction,
    findContactAction,
    createNewsletterAction,
    findCampaignListAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      auth: getresponseAuth,
      baseUrl: () => "https://api.getresponse.com/v3",
      authMapping: async (auth) => auth.type === import_pieces_framework11.AppConnectionType.OAUTH2 ? { Authorization: `Bearer ${auth.access_token}` } : { "X-Auth-Token": `api-key ${auth.props.apiKey}` }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  getresponse,
  getresponseAuth
});
