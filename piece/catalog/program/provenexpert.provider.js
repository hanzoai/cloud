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

// ../ap/packages/pieces/community/provenexpert/src/index.ts
var index_exports = {};
__export(index_exports, {
  provenexpert: () => provenexpert
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/provenexpert/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/provenexpert/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");
var provenExpertAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `Connect your ProvenExpert account to access reviews, surveys, and invitations.

**How to get your API credentials:**
1. Log in to your [ProvenExpert account](https://www.provenexpert.com/).
2. Go to **integrations ** from the left side panel.
3.  Copy your **Username** and **API Key**.
4. Paste them into the fields below.`,
  required: !0,
  props: {
    api_id: import_pieces_framework.Property.ShortText({
      displayName: "Username",
      description: "Your ProvenExpert Username.",
      required: !0
    }),
    api_key: import_pieces_framework.PieceAuth.SecretText({
      displayName: "API Key",
      description: "Your ProvenExpert API Key.",
      required: !0
    })
  },
  validate: async ({ auth }) => {
    try {
      return await provenExpertCommon.apiCall({
        auth,
        path: "/profile/get"
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid Username or API Key. Double-check your credentials in ProvenExpert under My Account \u2192 API Access."
      };
    }
  }
});

// ../ap/packages/pieces/community/provenexpert/src/lib/common/index.ts
var BASE_URL = "https://www.provenexpert.com/api/v1";
async function provenExpertApiCall({
  auth,
  path,
  body
}) {
  return await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BASIC,
      username: auth.api_id,
      password: auth.api_key
    },
    body: body ? { data: body } : {}
  });
}
var surveyDropdown = import_pieces_framework2.Property.Dropdown({
  auth: provenExpertAuth,
  displayName: "Survey",
  description: "Pick the survey to use. Surveys are created in your ProvenExpert account under the Surveys section.",
  refreshers: [],
  required: !0,
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your ProvenExpert account first"
      };
    try {
      let surveys = (_a = (await provenExpertApiCall({
        auth: auth.props,
        path: "/survey/get"
      })).body.surveys) != null ? _a : {}, options = Object.values(surveys).map((s) => ({
        label: s.active === 1 ? s.name : `${s.name} (inactive)`,
        value: s.code
      }));
      return options.length === 0 ? {
        disabled: !1,
        options: [],
        placeholder: "No surveys found. Create one in your ProvenExpert dashboard first."
      } : { disabled: !1, options };
    } catch (e) {
      return {
        disabled: !0,
        options: [],
        placeholder: "Failed to load surveys. Check your connection."
      };
    }
  }
}), provenExpertCommon = {
  apiCall: provenExpertApiCall,
  surveyDropdown,
  baseUrl: BASE_URL
};

// ../ap/packages/pieces/community/provenexpert/src/lib/actions/get-rating-summary.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var getRatingSummaryAction = (0, import_pieces_framework3.createAction)({
  auth: provenExpertAuth,
  name: "get_rating_summary",
  displayName: "Get Rating Summary",
  description: "Retrieves the overall rating value and total review count for your ProvenExpert profile.",
  audience: "both",
  aiMetadata: { description: "Fetches the aggregate rating value and total review count for the authenticated ProvenExpert profile. Use to read a single profile-wide rating score, not individual reviews. Takes no input; read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    var _a, _b;
    let response = await provenExpertCommon.apiCall({
      auth: context.auth.props,
      path: "/rating/summary/get"
    });
    return {
      status: response.body.status,
      rating_value: (_a = response.body.ratingValue) != null ? _a : null,
      review_count: (_b = response.body.reviewCount) != null ? _b : null
    };
  }
});

// ../ap/packages/pieces/community/provenexpert/src/lib/actions/create-survey-invitation-url.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var createSurveyInvitationUrlAction = (0, import_pieces_framework4.createAction)({
  auth: provenExpertAuth,
  name: "create_survey_invitation_url",
  displayName: "Create Survey Invitation URL",
  description: "Generates a personalised, one-time-use survey link that you can send to a customer.",
  audience: "both",
  aiMetadata: { description: "Generates a personalised, one-time-use survey invitation link for a specific survey and recipient email, returning the URL for you to deliver yourself (rather than having ProvenExpert email it). Requires a valid survey code and the recipient email the link is bound to. Not idempotent: each call mints a fresh invitation.", idempotent: !1 },
  props: {
    survey_code: provenExpertCommon.surveyDropdown,
    email: import_pieces_framework4.Property.ShortText({
      displayName: "Recipient Email",
      description: "Email address of the customer who will receive the survey link. The link is tied to this address.",
      required: !0
    }),
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Recipient Name",
      description: "Optional full name of the recipient. Used to personalise the survey experience.",
      required: !1
    })
  },
  async run(context) {
    var _a;
    let { survey_code, email, name } = context.propsValue, response = await provenExpertCommon.apiCall({
      auth: context.auth.props,
      path: "/invite/url/create",
      body: __spreadValues({
        code: survey_code,
        email
      }, name ? { name } : {})
    });
    return {
      status: response.body.status,
      invitation_url: (_a = response.body.url) != null ? _a : null,
      already_existed: response.body.exists === 1,
      survey_code,
      recipient_email: email,
      recipient_name: name != null ? name : null
    };
  }
});

// ../ap/packages/pieces/community/provenexpert/src/lib/actions/send-survey-invitation-email.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var sendSurveyInvitationEmailAction = (0, import_pieces_framework5.createAction)({
  auth: provenExpertAuth,
  name: "send_survey_invitation_email",
  displayName: "Send Survey Invitation Email",
  description: "Sends a survey invitation email through ProvenExpert to a single recipient. A 7-day reminder is sent automatically unless disabled.",
  audience: "both",
  aiMetadata: { description: "Sends a survey invitation email to one recipient via ProvenExpert, optionally with a custom subject, salutation, and body, and an optional automatic 7-day reminder. Choose this over Create Survey Invitation URL when ProvenExpert should deliver the email itself. Requires a valid survey code and recipient email. Not idempotent: each call dispatches a new email.", idempotent: !1 },
  props: {
    survey_code: provenExpertCommon.surveyDropdown,
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Recipient Email",
      description: "Email address that should receive the invitation.",
      required: !0
    }),
    name: import_pieces_framework5.Property.ShortText({
      displayName: "Recipient Name",
      description: "Optional full name of the recipient \u2014 used to address them in the email.",
      required: !1
    }),
    subject: import_pieces_framework5.Property.ShortText({
      displayName: "Email Subject",
      description: "Optional custom subject line for the invitation. Leave empty to use the survey default.",
      required: !1
    }),
    salutation: import_pieces_framework5.Property.ShortText({
      displayName: "Email Salutation",
      description: 'Optional custom greeting line (e.g. "Hi Jane,"). Leave empty to use the survey default.',
      required: !1
    }),
    text: import_pieces_framework5.Property.LongText({
      displayName: "Email Body",
      description: "Optional custom message body. Leave empty to use the survey default body.",
      required: !1
    }),
    send_reminder: import_pieces_framework5.Property.Checkbox({
      displayName: "Send 7-day Reminder",
      description: "When enabled, ProvenExpert sends a reminder email after 7 days if the recipient has not responded.",
      required: !1,
      defaultValue: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
    let {
      survey_code,
      email,
      name,
      subject,
      salutation,
      text,
      send_reminder
    } = context.propsValue, recipient = { email };
    name && (recipient.name = name), subject && (recipient.subject = subject), salutation && (recipient.salutation = salutation), text && (recipient.text = text);
    let response = await provenExpertCommon.apiCall({
      auth: context.auth.props,
      path: "/invite/mail/create",
      body: {
        code: survey_code,
        reminder: send_reminder === !1 ? 0 : 1,
        recipients: [recipient]
      }
    }), mailing = response.body.mailing;
    return {
      status: response.body.status,
      mailing_id: (_a = mailing == null ? void 0 : mailing.id) != null ? _a : null,
      mailing_status: (_b = mailing == null ? void 0 : mailing.status) != null ? _b : null,
      total_recipients: (_d = (_c = mailing == null ? void 0 : mailing.count) == null ? void 0 : _c.all) != null ? _d : null,
      created_count: (_f = (_e = mailing == null ? void 0 : mailing.count) == null ? void 0 : _e.created) != null ? _f : null,
      error_count: (_h = (_g = mailing == null ? void 0 : mailing.count) == null ? void 0 : _g.error) != null ? _h : null,
      created_emails: Array.isArray((_i = mailing == null ? void 0 : mailing.list) == null ? void 0 : _i.created) ? mailing.list.created.join(", ") : null,
      error_emails: Array.isArray((_j = mailing == null ? void 0 : mailing.list) == null ? void 0 : _j.error) ? mailing.list.error.join(", ") : null,
      survey_code,
      recipient_email: email
    };
  }
});

// ../ap/packages/pieces/community/provenexpert/src/lib/actions/list-surveys.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var listSurveysAction = (0, import_pieces_framework6.createAction)({
  auth: provenExpertAuth,
  name: "list_surveys",
  displayName: "List Surveys",
  description: "Returns all surveys on your ProvenExpert profile with their codes, names, and share links.",
  audience: "both",
  aiMetadata: { description: "Lists all surveys on the authenticated ProvenExpert profile, including each survey code, name, active state, and share links. Use to discover available surveys or to resolve a survey code needed by the invitation actions. Takes no input; read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    var _a;
    let surveys = (_a = (await provenExpertCommon.apiCall({
      auth: context.auth.props,
      path: "/survey/get"
    })).body.surveys) != null ? _a : {};
    return Object.values(surveys).map((s) => {
      var _a2, _b, _c, _d, _e;
      return {
        code: s.code,
        name: s.name,
        active: s.active === 1,
        created_at: s.created ? new Date(s.created * 1e3).toISOString() : null,
        position: (_a2 = s.pos) != null ? _a2 : null,
        survey_url: (_b = s.url) != null ? _b : null,
        qr_code_url: (_c = s.qr) != null ? _c : null,
        print_png_url: (_d = s.printPng) != null ? _d : null,
        print_pdf_url: (_e = s.printPdf) != null ? _e : null
      };
    });
  }
});

// ../ap/packages/pieces/community/provenexpert/src/lib/actions/get-profile.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var getProfileAction = (0, import_pieces_framework7.createAction)({
  auth: provenExpertAuth,
  name: "get_profile",
  displayName: "Get Profile",
  description: "Retrieves the company and contact details of your ProvenExpert profile.",
  audience: "both",
  aiMetadata: { description: "Retrieves the company and contact details (company name, email, address, phone, website, public status) of the authenticated ProvenExpert profile. Use to read account/profile information rather than rating or survey data. Takes no input; read-only and idempotent.", idempotent: !0 },
  props: {},
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m;
    let response = await provenExpertCommon.apiCall({
      auth: context.auth.props,
      path: "/profile/get"
    }), profile = (_a = response.body.profile) != null ? _a : {}, contact = (_b = profile.contact) != null ? _b : {};
    return {
      status: response.body.status,
      created_at: profile.created ? new Date(profile.created * 1e3).toISOString() : null,
      email: (_c = profile.email) != null ? _c : null,
      profile_url: (_d = profile.profileUrl) != null ? _d : null,
      is_public: profile.public === 1,
      company: (_e = profile.company) != null ? _e : null,
      description: (_f = profile.description) != null ? _f : null,
      contact_person: (_g = contact.person) != null ? _g : null,
      contact_street: (_h = contact.street) != null ? _h : null,
      contact_city: (_i = contact.city) != null ? _i : null,
      contact_zip: (_j = contact.zip) != null ? _j : null,
      contact_country: (_k = contact.country) != null ? _k : null,
      contact_phone: (_l = contact.phone) != null ? _l : null,
      contact_website: (_m = contact.website) != null ? _m : null
    };
  }
});

// ../ap/packages/pieces/community/provenexpert/src/index.ts
var provenexpert = (0, import_pieces_framework8.createPiece)({
  displayName: "ProvenExpert",
  description: "Collect customer reviews, send survey invitations, and pull rating data from ProvenExpert.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/provenexpert.png",
  categories: [import_pieces_framework9.PieceCategory.MARKETING, import_pieces_framework9.PieceCategory.FORMS_AND_SURVEYS],
  auth: provenExpertAuth,
  authors: ["sanket-a11y"],
  actions: [
    getRatingSummaryAction,
    createSurveyInvitationUrlAction,
    sendSurveyInvitationEmailAction,
    listSurveysAction,
    getProfileAction,
    (0, import_pieces_common2.createCustomApiCallAction)({
      baseUrl: () => provenExpertCommon.baseUrl,
      auth: provenExpertAuth,
      authMapping: async (auth) => {
        let credentials = auth;
        return {
          Authorization: `Basic ${Buffer.from(
            `${credentials.props.api_id}:${credentials.props.api_key}`
          ).toString("base64")}`
        };
      }
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  provenexpert
});
