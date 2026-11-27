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

// ../ap/packages/pieces/community/reply-io/src/index.ts
var index_exports = {};
__export(index_exports, {
  replyIo: () => replyIo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/reply-io/src/lib/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/reply-io/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var REPLY_IO_API_ROOT = "https://api.reply.io";
function buildReplyIoHeaders(apiKey, includeJsonContentType = !1) {
  return __spreadValues({
    "Api-Key": apiKey,
    "X-Api-Key": apiKey
  }, includeJsonContentType ? { "Content-Type": "application/json" } : {});
}
function removeUndefined(value) {
  return Object.fromEntries(
    Object.entries(value).filter(
      ([, current]) => current != null && current !== ""
    )
  );
}
async function replyIoRequest({
  apiKey,
  method,
  path,
  queryParams,
  body
}) {
  return import_pieces_common.httpClient.sendRequest({
    method,
    url: `${REPLY_IO_API_ROOT}${path}`,
    headers: buildReplyIoHeaders(apiKey, body !== void 0),
    queryParams,
    body
  });
}
function cleanPayload(payload) {
  return removeUndefined(payload);
}
function extractCollection(body) {
  if (Array.isArray(body))
    return body;
  if (!body || typeof body != "object")
    return [];
  let record = body, wrapperKeys = ["results", "items", "campaigns", "people"];
  for (let key of wrapperKeys) {
    let candidate = record[key];
    if (Array.isArray(candidate))
      return candidate;
  }
  return [];
}

// ../ap/packages/pieces/community/reply-io/src/lib/auth.ts
var replyIoAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "Reply.io",
  required: !0,
  description: "Your Reply.io API key. Find it in Reply.io under **Settings \u2192 API**.",
  validate: async ({ auth }) => {
    var _a;
    try {
      return await replyIoRequest({
        apiKey: auth,
        method: import_pieces_common2.HttpMethod.GET,
        path: "/v1/campaigns"
      }), { valid: !0 };
    } catch (error) {
      let status = (_a = error == null ? void 0 : error.response) == null ? void 0 : _a.status;
      return status === 401 || status === 403 ? { valid: !1, error: "Invalid API key." } : {
        valid: !1,
        error: "Could not reach the Reply.io API. Check your API key and network access."
      };
    }
  }
});

// ../ap/packages/pieces/community/reply-io/src/lib/actions/create-or-update-contact.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var createOrUpdateContactAction = (0, import_pieces_framework2.createAction)({
  name: "create_or_update_contact",
  displayName: "Create or Update Contact",
  description: "Add a new contact to Reply.io, or update their details if a contact with that email already exists.",
  audience: "both",
  aiMetadata: { description: "Upsert a contact in Reply.io keyed on email: creates the contact if the email is new, otherwise updates the existing record with the supplied fields. Use to store or refresh contact details without enrolling them in any campaign. Email and first name are required; repeating with the same input converges to the same stored record.", idempotent: !0 },
  auth: replyIoAuth,
  props: {
    email: import_pieces_framework2.Property.ShortText({
      displayName: "Email Address",
      description: "The contact's email address. Used to identify and deduplicate contacts.",
      required: !0
    }),
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "Contact's first name.",
      required: !0
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "Contact's last name.",
      required: !1
    }),
    company: import_pieces_framework2.Property.ShortText({
      displayName: "Company",
      description: "The company or organisation the contact works for.",
      required: !1
    }),
    title: import_pieces_framework2.Property.ShortText({
      displayName: "Job Title",
      description: `Contact's job title, e.g. "VP of Sales" or "Marketing Manager".`,
      required: !1
    }),
    phone: import_pieces_framework2.Property.ShortText({
      displayName: "Phone Number",
      description: "Contact's phone number.",
      required: !1
    }),
    linkedInProfile: import_pieces_framework2.Property.ShortText({
      displayName: "LinkedIn Profile URL",
      description: "Full URL to the contact's LinkedIn profile, e.g. https://linkedin.com/in/janedoe",
      required: !1
    }),
    city: import_pieces_framework2.Property.ShortText({
      displayName: "City",
      description: "City the contact is based in.",
      required: !1
    }),
    state: import_pieces_framework2.Property.ShortText({
      displayName: "State / Province",
      description: "State or province the contact is based in.",
      required: !1
    }),
    country: import_pieces_framework2.Property.ShortText({
      displayName: "Country",
      description: "Country the contact is based in.",
      required: !1
    }),
    timeZoneId: import_pieces_framework2.Property.ShortText({
      displayName: "Time Zone",
      description: 'IANA time zone name used to schedule emails at the right local time, e.g. "America/New_York" or "Europe/London".',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await replyIoRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common3.HttpMethod.POST,
      path: "/v1/people",
      body: cleanPayload({
        email: propsValue.email,
        firstName: propsValue.firstName,
        lastName: propsValue.lastName,
        company: propsValue.company,
        title: propsValue.title,
        phone: propsValue.phone,
        linkedInProfile: propsValue.linkedInProfile,
        city: propsValue.city,
        state: propsValue.state,
        country: propsValue.country,
        timeZoneId: propsValue.timeZoneId
      })
    })).body;
  }
});

// ../ap/packages/pieces/community/reply-io/src/lib/actions/create-and-push-to-campaign.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/reply-io/src/lib/common/props.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");
var campaignIdProp = import_pieces_framework3.Property.Dropdown({
  displayName: "Campaign",
  description: "The Reply.io campaign to use.",
  required: !0,
  refreshers: [],
  auth: replyIoAuth,
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Connect your Reply.io account first.",
        options: []
      };
    let response = await replyIoRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      path: "/v1/campaigns"
    });
    return {
      disabled: !1,
      options: extractCollection(response.body).filter((campaign) => campaign.id !== void 0 && campaign.id !== null).map((campaign) => {
        var _a;
        return {
          label: (_a = campaign.name) != null ? _a : String(campaign.id),
          value: String(campaign.id)
        };
      })
    };
  }
});

// ../ap/packages/pieces/community/reply-io/src/lib/actions/create-and-push-to-campaign.ts
var createAndPushToCampaignAction = (0, import_pieces_framework4.createAction)({
  name: "create_and_push_to_campaign",
  displayName: "Add Contact to Campaign",
  description: "Create a new contact (or update an existing one) and immediately enrol them in a campaign so they start receiving outreach emails.",
  audience: "both",
  aiMetadata: { description: "Upsert a contact (by email) and in the same call enrol them into the given campaign so outbound outreach begins. Use when adding a brand-new lead to a sequence in one step; if the contact already exists in Reply.io, prefer Push Contact to Campaign instead. Requires a target campaign id plus email and first name. Not idempotent: it enrolls the contact and starts sending outreach on each call.", idempotent: !1 },
  auth: replyIoAuth,
  props: {
    campaignId: campaignIdProp,
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Email Address",
      description: "The contact's email address. Used to identify and deduplicate contacts.",
      required: !0
    }),
    firstName: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      description: "Contact's first name.",
      required: !0
    }),
    lastName: import_pieces_framework4.Property.ShortText({
      displayName: "Last Name",
      description: "Contact's last name.",
      required: !1
    }),
    company: import_pieces_framework4.Property.ShortText({
      displayName: "Company",
      description: "The company or organisation the contact works for.",
      required: !1
    }),
    title: import_pieces_framework4.Property.ShortText({
      displayName: "Job Title",
      description: `Contact's job title, e.g. "VP of Sales" or "Marketing Manager".`,
      required: !1
    }),
    phone: import_pieces_framework4.Property.ShortText({
      displayName: "Phone Number",
      description: "Contact's phone number.",
      required: !1
    }),
    linkedInProfile: import_pieces_framework4.Property.ShortText({
      displayName: "LinkedIn Profile URL",
      description: "Full URL to the contact's LinkedIn profile, e.g. https://linkedin.com/in/janedoe",
      required: !1
    }),
    notes: import_pieces_framework4.Property.LongText({
      displayName: "Notes",
      description: "Internal notes about this contact. Not visible to the contact.",
      required: !1
    }),
    city: import_pieces_framework4.Property.ShortText({
      displayName: "City",
      description: "City the contact is based in.",
      required: !1
    }),
    state: import_pieces_framework4.Property.ShortText({
      displayName: "State / Province",
      description: "State or province the contact is based in.",
      required: !1
    }),
    country: import_pieces_framework4.Property.ShortText({
      displayName: "Country",
      description: "Country the contact is based in.",
      required: !1
    }),
    timeZoneId: import_pieces_framework4.Property.ShortText({
      displayName: "Time Zone",
      description: 'IANA time zone name used to schedule emails at the right local time, e.g. "America/New_York" or "Europe/London".',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    return (await replyIoRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/v1/actions/addandpushtocampaign",
      body: cleanPayload({
        campaignId: Number(propsValue.campaignId),
        email: propsValue.email,
        firstName: propsValue.firstName,
        lastName: propsValue.lastName,
        company: propsValue.company,
        title: propsValue.title,
        phone: propsValue.phone,
        linkedInProfile: propsValue.linkedInProfile,
        notes: propsValue.notes,
        city: propsValue.city,
        state: propsValue.state,
        country: propsValue.country,
        timeZoneId: propsValue.timeZoneId
      })
    })).body;
  }
});

// ../ap/packages/pieces/community/reply-io/src/lib/actions/push-to-campaign.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var pushToCampaignAction = (0, import_pieces_framework5.createAction)({
  name: "push_to_campaign",
  displayName: "Push Contact to Campaign",
  description: 'Enrol an existing contact in a campaign so they start receiving outreach emails. Enable "Force Push" to move the contact even if they are already active in another campaign.',
  audience: "both",
  aiMetadata: { description: "Enrol an already-existing Reply.io contact (by email) into a campaign so outbound outreach begins; the optional Force Push flag moves them in even if they are already active in another campaign (otherwise such contacts are skipped). Use when the contact already exists and only needs to be added to a sequence; use Add Contact to Campaign instead if you also need to create the contact. Requires a target campaign id and the contact email. Not idempotent: it enrolls and starts sending outreach on each call.", idempotent: !1 },
  auth: replyIoAuth,
  props: {
    campaignId: campaignIdProp,
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Contact Email Address",
      description: "Email address of the contact to add to the campaign.",
      required: !0
    }),
    forcePush: import_pieces_framework5.Property.Checkbox({
      displayName: "Force Push",
      description: "Move the contact into this campaign even if they are already active in another campaign. Leave off to skip contacts already in an active campaign.",
      required: !1,
      defaultValue: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      campaignId: Number(propsValue.campaignId),
      email: propsValue.email
    };
    return propsValue.forcePush && (body.forcePush = !0), (await replyIoRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      path: "/v1/actions/pushtocampaign",
      body
    })).body;
  }
});

// ../ap/packages/pieces/community/reply-io/src/lib/actions/mark-replied.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var markRepliedAction = (0, import_pieces_framework6.createAction)({
  name: "mark_replied",
  displayName: "Mark Contact as Replied",
  description: "Record that a contact has replied to your outreach. This stops further follow-up emails to that contact across all active campaigns.",
  audience: "both",
  aiMetadata: { description: "Mark a contact (by email) as having replied, which stops further follow-up emails to them across all active campaigns. Use when a reply has been detected and outreach should pause for that reason specifically (vs. Mark Contact as Finished, which closes them out as fully completed). Requires only the contact email. Idempotent: repeating leaves the contact in the replied state.", idempotent: !0 },
  auth: replyIoAuth,
  props: {
    email: import_pieces_framework6.Property.ShortText({
      displayName: "Contact Email Address",
      description: "Email address of the contact to mark as replied.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await replyIoRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common7.HttpMethod.POST,
      path: "/v1/actions/markasreplied",
      body: {
        email: propsValue.email
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/reply-io/src/lib/actions/mark-finished.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var markFinishedAction = (0, import_pieces_framework7.createAction)({
  name: "mark_finished",
  displayName: "Mark Contact as Finished",
  description: "Mark a contact as finished so they no longer receive emails in any campaign. Use this when you have completed all outreach to that contact.",
  audience: "both",
  aiMetadata: { description: "Mark a contact (by email) as finished so they no longer receive emails in any campaign, signalling that all outreach to them is complete. Use to close out a contact whose sequence is done (vs. Mark Contact as Replied, which pauses specifically because they responded). Requires only the contact email. Idempotent: repeating leaves the contact in the finished state.", idempotent: !0 },
  auth: replyIoAuth,
  props: {
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Contact Email Address",
      description: "Email address of the contact to mark as finished.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await replyIoRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common8.HttpMethod.POST,
      path: "/v1/actions/markasfinished",
      body: {
        email: propsValue.email
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/reply-io/src/lib/actions/remove-from-campaign.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var removeFromCampaignAction = (0, import_pieces_framework8.createAction)({
  name: "remove_from_campaign",
  displayName: "Remove Contact from Campaign",
  description: "Stop a contact from receiving further emails in a specific campaign. The contact's record is kept in Reply.io.",
  audience: "both",
  aiMetadata: { description: "Remove a contact (by email) from one specific campaign so they stop receiving its outreach, while keeping their contact record and any other campaign enrolments intact. Use to pull a contact out of a single sequence; use Remove Contact from All Campaigns to halt every sequence at once. Requires the target campaign id and the contact email. Idempotent: repeating leaves the contact removed from that campaign.", idempotent: !0 },
  auth: replyIoAuth,
  props: {
    campaignId: campaignIdProp,
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Contact Email Address",
      description: "Email address of the contact to remove from the campaign.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await replyIoRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common9.HttpMethod.POST,
      path: "/v1/actions/removepersonfromcampaignbyid",
      body: {
        campaignId: Number(propsValue.campaignId),
        email: propsValue.email
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/reply-io/src/lib/actions/remove-from-all-campaigns.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var removeFromAllCampaignsAction = (0, import_pieces_framework9.createAction)({
  name: "remove_from_all_campaigns",
  displayName: "Remove Contact from All Campaigns",
  description: "Remove a contact from every campaign they are enrolled in. No further emails will be sent to them. Their contact record is kept in Reply.io.",
  audience: "both",
  aiMetadata: { description: "Remove a contact (by email) from every campaign they are enrolled in so no further outreach is sent, while keeping their contact record in Reply.io. Use to fully stop outreach to someone without deleting them; use Remove Contact from Campaign to pull them from just one sequence. Requires only the contact email. Idempotent: repeating leaves the contact out of all campaigns.", idempotent: !0 },
  auth: replyIoAuth,
  props: {
    email: import_pieces_framework9.Property.ShortText({
      displayName: "Contact Email Address",
      description: "Email address of the contact to remove from all campaigns.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await replyIoRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common10.HttpMethod.POST,
      path: "/v1/actions/removepersonfromallcampaigns",
      body: {
        email: propsValue.email
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/reply-io/src/lib/actions/get-contact.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var getContactAction = (0, import_pieces_framework10.createAction)({
  name: "get_contact",
  displayName: "Get Contact",
  description: "Look up a contact in Reply.io by their email address and retrieve their details.",
  audience: "both",
  aiMetadata: { description: "Fetch a single Reply.io contact and their profile/stats by exact email address. Use to check whether a contact exists or to read their current details before acting on them. Email is required; this is a read-only lookup that is safe to retry.", idempotent: !0 },
  auth: replyIoAuth,
  props: {
    email: import_pieces_framework10.Property.ShortText({
      displayName: "Contact Email Address",
      description: "Email address of the contact to look up.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await replyIoRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common11.HttpMethod.GET,
      path: "/v1/people",
      queryParams: {
        email: propsValue.email
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/reply-io/src/lib/actions/delete-contact.ts
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var deleteContactAction = (0, import_pieces_framework11.createAction)({
  name: "delete_contact",
  displayName: "Delete Contact",
  description: "Permanently delete a contact from Reply.io by their email address. This cannot be undone.",
  audience: "both",
  aiMetadata: { description: "Permanently delete a contact from Reply.io by exact email address; this also removes them from any campaigns and cannot be undone. Use only when the contact record should be erased entirely rather than just stopped (prefer the remove-from-campaign or mark actions to merely halt outreach). Repeating the call leaves the contact deleted, so it is safe to retry.", idempotent: !0 },
  auth: replyIoAuth,
  props: {
    email: import_pieces_framework11.Property.ShortText({
      displayName: "Contact Email Address",
      description: "Email address of the contact to permanently delete.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    return (await replyIoRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common12.HttpMethod.DELETE,
      path: "/v1/people",
      queryParams: {
        email: propsValue.email
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/reply-io/src/index.ts
var replyIo = (0, import_pieces_framework12.createPiece)({
  displayName: "Reply.io",
  description: "Sales engagement platform for contacts, campaigns, and outbound outreach workflows.",
  auth: replyIoAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/reply-io.png",
  authors: ["Harmatta", "sanket-a11y"],
  categories: [import_pieces_framework13.PieceCategory.SALES_AND_CRM, import_pieces_framework13.PieceCategory.MARKETING],
  actions: [
    getContactAction,
    createOrUpdateContactAction,
    deleteContactAction,
    createAndPushToCampaignAction,
    pushToCampaignAction,
    removeFromCampaignAction,
    removeFromAllCampaignsAction,
    markRepliedAction,
    markFinishedAction,
    (0, import_pieces_common13.createCustomApiCallAction)({
      baseUrl: () => REPLY_IO_API_ROOT,
      auth: replyIoAuth,
      authMapping: async (auth) => buildReplyIoHeaders(auth.secret_text)
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  replyIo
});
