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

// ../ap/packages/pieces/community/deftform/src/index.ts
var index_exports = {};
__export(index_exports, {
  deftform: () => deftform,
  deftformAuth: () => deftformAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/deftform/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), deftformAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `To get your Deftform API key:
1. Log in to your Deftform workspace at https://deftform.com
2. Go to **Settings > API** (https://deftform.com/settings/api)
3. Click **Create API Key**
4. Copy the key and paste it here

Need help? See https://deftform.com/docs/api`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://deftform.com/api/v1/workspace",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json"
        },
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API Key. Make sure you copied the full key from Deftform." };
    }
  }
});

// ../ap/packages/pieces/community/deftform/src/lib/actions/get-workspace.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/deftform/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var BASE_URL = "https://deftform.com/api/v1";
async function deftformApiCall({
  token,
  method,
  path,
  body,
  queryParams
}) {
  return await import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json"
    },
    authentication: {
      type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
      token
    },
    queryParams,
    body
  });
}
function flattenObject(obj, prefix = "") {
  let result = {};
  for (let [key, value] of Object.entries(obj)) {
    let flatKey = prefix ? `${prefix}_${key}` : key;
    value == null ? result[flatKey] = null : Array.isArray(value) ? result[flatKey] = value.map((v) => typeof v == "object" ? JSON.stringify(v) : String(v)).join(", ") : typeof value == "object" ? Object.assign(result, flattenObject(value, flatKey)) : result[flatKey] = value;
  }
  return result;
}
var DeftformCommon = {
  formDropdown: import_pieces_framework2.Property.Dropdown({
    displayName: "Form",
    description: "Select the form to use",
    refreshers: [],
    required: !0,
    auth: deftformAuth,
    options: async ({ auth }) => {
      if (!auth)
        return { disabled: !0, options: [], placeholder: "Please connect your account first" };
      try {
        return {
          disabled: !1,
          options: (await deftformApiCall({
            token: auth.secret_text,
            method: import_pieces_common2.HttpMethod.GET,
            path: "/forms"
          })).body.data.map((f) => ({
            label: f.name,
            value: f.id
          }))
        };
      } catch (error) {
        return { disabled: !0, options: [], placeholder: "Failed to load forms. Check your connection." };
      }
    }
  })
};

// ../ap/packages/pieces/community/deftform/src/lib/actions/get-workspace.ts
var getWorkspaceDetails = (0, import_pieces_framework3.createAction)({
  auth: deftformAuth,
  name: "get_workspace_details",
  classification: "READ",
  displayName: "Get Workspace Details",
  description: "Retrieves all information about your Deftform workspace. Useful as a connection test.",
  audience: "both",
  aiMetadata: { description: "Fetch the Deftform workspace details (plan, limits, account-level settings). Use it to confirm the connection works or to read workspace-level context before working with forms. Read-only and idempotent; takes no input.", idempotent: !0 },
  props: {},
  async run(context) {
    let response = await deftformApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common3.HttpMethod.GET,
      path: "/workspace"
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/deftform/src/lib/actions/get-all-forms.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var getAllForms = (0, import_pieces_framework4.createAction)({
  auth: deftformAuth,
  name: "get_all_forms",
  classification: "SEARCH",
  displayName: "Get All Forms and Fields",
  description: "Lists all forms in your workspace, including their fields.",
  audience: "both",
  aiMetadata: { description: "List every form in the workspace along with each form fields and metadata (id, name, slug, closed status, response limit). Use it to discover available forms and their ids before targeting a specific form. Read-only and idempotent; takes no input.", idempotent: !0 },
  props: {},
  async run(context) {
    return (await deftformApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      path: "/forms"
    })).body.data.map((form) => {
      var _a, _b, _c, _d, _e, _f, _g, _h, _i;
      let f = form, fieldsRaw = Array.isArray(f.fields) ? f.fields : [];
      return {
        id: (_a = f.id) != null ? _a : null,
        name: (_b = f.name) != null ? _b : null,
        description: (_c = f.description) != null ? _c : null,
        is_closed: (_d = f.is_closed) != null ? _d : null,
        slug: (_e = f.slug) != null ? _e : null,
        created_at: (_f = f.created_at) != null ? _f : null,
        updated_at: (_g = f.updated_at) != null ? _g : null,
        fields_count: fieldsRaw.length,
        fields: fieldsRaw.map((field) => {
          var _a2, _b2, _c2, _d2;
          return {
            id: (_a2 = field.id) != null ? _a2 : null,
            label: (_c2 = (_b2 = field.label) != null ? _b2 : field.name) != null ? _c2 : null,
            type: (_d2 = field.type) != null ? _d2 : null
          };
        }),
        responses_limit: (_h = f.responses_limit) != null ? _h : null,
        admin_email_subject: (_i = f.admin_email_subject) != null ? _i : null
      };
    });
  }
});

// ../ap/packages/pieces/community/deftform/src/lib/actions/get-form-fields.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getFormFields = (0, import_pieces_framework5.createAction)({
  auth: deftformAuth,
  name: "get_form_fields",
  classification: "READ",
  displayName: "Get Form Fields",
  description: "Retrieves only the fields of a specific form. Great for understanding the structure before building automations.",
  audience: "both",
  aiMetadata: { description: "Fetch the field definitions of one form (label, type, required flag, options, validation). Use it to learn a form schema before submitting a response with Add Form Response, since that action keys values by field UUID. Requires the form id. Read-only and idempotent.", idempotent: !0 },
  props: {
    formId: DeftformCommon.formDropdown
  },
  async run(context) {
    return (await deftformApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.GET,
      path: `/forms/${context.propsValue.formId}/fields`
    })).body.data.map((field) => {
      var _a, _b, _c, _d, _e, _f, _g, _h;
      let f = field;
      return {
        id: (_a = f.id) != null ? _a : null,
        name: (_b = f.name) != null ? _b : null,
        label: (_c = f.label) != null ? _c : null,
        type: (_d = f.type) != null ? _d : null,
        required: (_e = f.required) != null ? _e : null,
        options: Array.isArray(f.options) ? f.options : null,
        default_value: (_f = f.default_value) != null ? _f : null,
        placeholder: (_g = f.placeholder) != null ? _g : null,
        validation: (_h = f.validation) != null ? _h : null
      };
    });
  }
});

// ../ap/packages/pieces/community/deftform/src/lib/actions/get-form-responses.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getFormResponses = (0, import_pieces_framework6.createAction)({
  auth: deftformAuth,
  name: "get_form_responses",
  classification: "SEARCH",
  displayName: "Get Form Responses",
  description: "Retrieves all submissions (responses) for a specific form. Useful for reporting and data exports.",
  audience: "both",
  aiMetadata: { description: "Retrieve all submitted responses for one form, with each response answers flattened by field key plus submission metadata (id, uuid, number, timestamps). Use it for reporting, exports, or to look up a response UUID needed by Get Submission PDF. Requires the form id. Read-only and idempotent.", idempotent: !0 },
  props: {
    formId: DeftformCommon.formDropdown
  },
  async run(context) {
    return (await deftformApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.GET,
      path: `/responses/${context.propsValue.formId}`
    })).body.data.map((item) => {
      var _a, _b, _c, _d, _e, _f, _g;
      let i = item, responseFields = {}, responses = i.responses;
      if (responses)
        for (let r of responses) {
          let value = r.value;
          responseFields[r.key] = Array.isArray(value) ? value.map((v) => typeof v == "object" && v !== null ? JSON.stringify(v) : String(v)).join(", ") : value != null ? value : null;
        }
      return __spreadProps(__spreadValues({}, responseFields), {
        id: (_a = i.id) != null ? _a : null,
        uuid: (_b = i.uuid) != null ? _b : null,
        number: (_c = i.number) != null ? _c : null,
        number_formatted: (_d = i.number_formatted) != null ? _d : null,
        form_id: (_e = i.form_id) != null ? _e : null,
        created_at: (_f = i.created_at) != null ? _f : null,
        updated_at: (_g = i.updated_at) != null ? _g : null
      });
    });
  }
});

// ../ap/packages/pieces/community/deftform/src/lib/actions/get-submission-pdf.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var getSubmissionPdf = (0, import_pieces_framework7.createAction)({
  auth: deftformAuth,
  name: "get_submission_pdf",
  classification: "READ",
  displayName: "Get Submission PDF",
  description: "Generates a PDF download link for a specific form submission by its unique response UUID.",
  audience: "both",
  aiMetadata: { description: "Get a downloadable PDF link for a single form submission, identified by its response UUID (obtainable from Get Form Responses). Use it when you need a printable or shareable copy of one response. Read-only and idempotent.", idempotent: !0 },
  props: {
    responseUuid: import_pieces_framework7.Property.ShortText({
      displayName: "Response UUID",
      description: "The unique identifier of the submission. You can find this in the form responses list.",
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let response = await deftformApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      path: `/response/${context.propsValue.responseUuid}/pdf`
    });
    return {
      pdf_url: (_d = (_c = (_a = response.body.data) == null ? void 0 : _a.pdf_url) != null ? _c : (_b = response.body.data) == null ? void 0 : _b.url) != null ? _d : null,
      response_uuid: context.propsValue.responseUuid
    };
  }
});

// ../ap/packages/pieces/community/deftform/src/lib/actions/add-form-response.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var addFormResponse = (0, import_pieces_framework8.createAction)({
  auth: deftformAuth,
  name: "add_form_response",
  classification: "WRITE",
  displayName: "Add Form Response",
  description: `Submits a new response programmatically to a form. 
**Note:** This does **not** trigger email notifications to the form admin, unlike regular public submissions.`,
  audience: "both",
  aiMetadata: { description: "Programmatically submit a new response to a form. Use it to record form data on behalf of a respondent without going through the public form page; note it does not send the usual admin email notification. Requires the form id and a response-data object keyed by field UUID (use Get Form Fields to resolve UUIDs). Not idempotent: each call creates a separate submission.", idempotent: !1 },
  props: {
    formId: DeftformCommon.formDropdown,
    responseData: import_pieces_framework8.Property.Object({
      displayName: "Response Data",
      description: "Key-value pairs where each key is the field UUID and the value is the user input.",
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c, _d;
    let response = await deftformApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.POST,
      path: `/forms/${context.propsValue.formId}/response`,
      body: { data: context.propsValue.responseData }
    });
    return {
      id: (_b = (_a = response.body.data) == null ? void 0 : _a.id) != null ? _b : null,
      uuid: (_d = (_c = response.body.data) == null ? void 0 : _c.uuid) != null ? _d : null,
      message: "Response submitted successfully."
    };
  }
});

// ../ap/packages/pieces/community/deftform/src/lib/actions/update-form-settings.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var updateFormSettings = (0, import_pieces_framework9.createAction)({
  auth: deftformAuth,
  name: "update_form_settings",
  classification: "WRITE",
  displayName: "Update Form Settings",
  description: "Updates any combination of form settings via a single call \u2014 from title and status to integrations, SEO, and security.",
  audience: "both",
  aiMetadata: { description: "Update one form configuration in a single call, covering any subset of general info, after-submission behaviour, admin/respondent email, SEO, integration toggles (Slack, Discord, Google Sheets, HubSpot), security/CAPTCHA, layout, and submission numbering. Only the fields you provide are changed; omitted fields are left untouched. Requires the form id. Idempotent: re-sending the same values leaves the form in the same state.", idempotent: !0 },
  props: {
    formId: DeftformCommon.formDropdown,
    generalHeader: import_pieces_framework9.Property.MarkDown({
      value: "## General Settings"
    }),
    name: import_pieces_framework9.Property.ShortText({
      displayName: "Form Name",
      description: "New display name of the form. Leave empty to keep the current value.",
      required: !1
    }),
    description: import_pieces_framework9.Property.LongText({
      displayName: "Form Description",
      description: "New description shown on the public form page.",
      required: !1
    }),
    isClosed: import_pieces_framework9.Property.Checkbox({
      displayName: "Close Form",
      description: "When checked, the form stops accepting new submissions.",
      required: !1
    }),
    responsesLimit: import_pieces_framework9.Property.Number({
      displayName: "Responses Limit",
      description: "Maximum number of submissions allowed. Leave empty for unlimited responses.",
      required: !1
    }),
    afterSubmissionHeader: import_pieces_framework9.Property.MarkDown({
      value: "## After-Submission Behaviour"
    }),
    afterMessage: import_pieces_framework9.Property.LongText({
      displayName: "After-Submission Message",
      description: "Message shown on screen after the user submits the form.",
      required: !1
    }),
    afterRedirectUrl: import_pieces_framework9.Property.ShortText({
      displayName: "After-Submission Redirect URL",
      description: "URL to redirect the respondent to after submission (instead of showing a message).",
      required: !1
    }),
    afterRedirectDelay: import_pieces_framework9.Property.Number({
      displayName: "Redirect Delay (seconds)",
      description: "Number of seconds to wait before redirecting. Leave empty for instant redirect.",
      required: !1
    }),
    ctaLabel: import_pieces_framework9.Property.ShortText({
      displayName: "CTA Label",
      description: 'Text on the primary button shown after submission (e.g. "Download").',
      required: !1
    }),
    ctaLabelContinue: import_pieces_framework9.Property.ShortText({
      displayName: "CTA Label Continue",
      description: 'Text on the secondary button shown after submission (e.g. "Next").',
      required: !1
    }),
    closedMessage: import_pieces_framework9.Property.LongText({
      displayName: "Closed Form Message",
      description: "Message shown when a user tries to access a closed form.",
      required: !1
    }),
    adminEmailHeader: import_pieces_framework9.Property.MarkDown({
      value: "## Admin Notification Email"
    }),
    adminEmailSubject: import_pieces_framework9.Property.ShortText({
      displayName: "Notification Email Subject",
      description: "Subject line of the email sent to admins on each new submission.",
      required: !1
    }),
    adminEmailNote: import_pieces_framework9.Property.LongText({
      displayName: "Notification Email Note",
      description: "Custom note added to the top of the admin notification email.",
      required: !1
    }),
    adminEmailAttachPdf: import_pieces_framework9.Property.Checkbox({
      displayName: "Attach Submission PDF",
      description: "Attach a PDF version of the submission to the admin email.",
      required: !1
    }),
    includeResponseInEmail: import_pieces_framework9.Property.Checkbox({
      displayName: "Include Response in Email",
      description: "Include the full response data in the admin notification body.",
      required: !1
    }),
    afterMessageEmail: import_pieces_framework9.Property.LongText({
      displayName: "Respondent Confirmation Email Message",
      description: "Message sent to the respondent in their confirmation email (if enabled).",
      required: !1
    }),
    afterMessageEmailSubject: import_pieces_framework9.Property.ShortText({
      displayName: "Respondent Confirmation Email Subject",
      description: "Subject line of the confirmation email sent to the respondent.",
      required: !1
    }),
    seoHeader: import_pieces_framework9.Property.MarkDown({
      value: "## SEO Settings"
    }),
    seoTitle: import_pieces_framework9.Property.ShortText({
      displayName: "SEO Title",
      description: "Custom HTML title tag for the public form page.",
      required: !1
    }),
    seoDescription: import_pieces_framework9.Property.ShortText({
      displayName: "SEO Description",
      description: "Meta description for the public form page.",
      required: !1
    }),
    seoAllowBots: import_pieces_framework9.Property.Checkbox({
      displayName: "Allow Search-Engine Bots",
      description: "When checked, search engines may index the public form page.",
      required: !1
    }),
    integrationsHeader: import_pieces_framework9.Property.MarkDown({
      value: "## Integrations"
    }),
    discordEnabled: import_pieces_framework9.Property.Checkbox({
      displayName: "Discord Notifications",
      description: "When checked, a notification is sent to the configured Discord channel on each submission.",
      required: !1
    }),
    slackEnabled: import_pieces_framework9.Property.Checkbox({
      displayName: "Slack Notifications",
      description: "When checked, a notification is sent to the configured Slack channel on each submission.",
      required: !1
    }),
    googleSheetsEnabled: import_pieces_framework9.Property.Checkbox({
      displayName: "Google Sheets Integration",
      description: "When checked, submissions are automatically synced to Google Sheets.",
      required: !1
    }),
    hubspotEnabled: import_pieces_framework9.Property.Checkbox({
      displayName: "HubSpot Integration",
      description: "When checked, submissions are pushed to HubSpot as contacts or deals.",
      required: !1
    }),
    securityHeader: import_pieces_framework9.Property.MarkDown({
      value: "## Security"
    }),
    captcha: import_pieces_framework9.Property.StaticDropdown({
      displayName: "CAPTCHA",
      description: "Protection against spam bots.",
      required: !1,
      options: {
        options: [
          { label: "ALTCHA", value: "altcha" },
          { label: "Turnstile (Cloudflare)", value: "turnstile" },
          { label: "reCAPTCHA", value: "recaptcha" },
          { label: "None", value: "none" }
        ]
      }
    }),
    captureLocation: import_pieces_framework9.Property.Checkbox({
      displayName: "Capture Location",
      description: "Record the respondent location (IP-based geolocation).",
      required: !1
    }),
    layoutHeader: import_pieces_framework9.Property.MarkDown({
      value: "## Layout & Display"
    }),
    showFormTitle: import_pieces_framework9.Property.Checkbox({
      displayName: "Show Form Title",
      description: "Display the form title at the top of the public page.",
      required: !1
    }),
    showMultipageProgress: import_pieces_framework9.Property.Checkbox({
      displayName: "Show Multi-Page Progress",
      description: "Show a progress bar when the form has multiple pages.",
      required: !1
    }),
    aiSummaryEnabled: import_pieces_framework9.Property.Checkbox({
      displayName: "AI Summary",
      description: "Enable the AI-powered summary on the form dashboard.",
      required: !1
    }),
    sendPdfToRespondent: import_pieces_framework9.Property.Checkbox({
      displayName: "Send PDF to Respondent",
      description: "Attach a PDF version of the submission to the respondent confirmation email.",
      required: !1
    }),
    numberHeader: import_pieces_framework9.Property.MarkDown({
      value: "## Submission Numbering"
    }),
    numberPrefix: import_pieces_framework9.Property.ShortText({
      displayName: "Number Prefix",
      description: 'Text added before the submission number (e.g. "INV-").',
      required: !1
    }),
    numberSuffix: import_pieces_framework9.Property.ShortText({
      displayName: "Number Suffix",
      description: 'Text added after the submission number (e.g. "-A").',
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let body = {};
    context.propsValue.name !== void 0 && (body.name = context.propsValue.name), context.propsValue.description !== void 0 && (body.description = context.propsValue.description), context.propsValue.isClosed !== void 0 && (body.is_closed = context.propsValue.isClosed), context.propsValue.responsesLimit !== void 0 && (body.responses_limit = context.propsValue.responsesLimit), context.propsValue.afterMessage !== void 0 && (body.after_message = context.propsValue.afterMessage), context.propsValue.afterRedirectUrl !== void 0 && (body.after_redirect_url = context.propsValue.afterRedirectUrl), context.propsValue.afterRedirectDelay !== void 0 && (body.after_redirect_delay = context.propsValue.afterRedirectDelay), context.propsValue.ctaLabel !== void 0 && (body.cta_label = context.propsValue.ctaLabel), context.propsValue.ctaLabelContinue !== void 0 && (body.cta_label_continue = context.propsValue.ctaLabelContinue), context.propsValue.closedMessage !== void 0 && (body.closed_message = context.propsValue.closedMessage), context.propsValue.adminEmailSubject !== void 0 && (body.admin_email_subject = context.propsValue.adminEmailSubject), context.propsValue.adminEmailNote !== void 0 && (body.admin_email_note = context.propsValue.adminEmailNote), context.propsValue.adminEmailAttachPdf !== void 0 && (body.admin_email_attach_pdf = context.propsValue.adminEmailAttachPdf), context.propsValue.includeResponseInEmail !== void 0 && (body.include_response_in_email = context.propsValue.includeResponseInEmail), context.propsValue.afterMessageEmail !== void 0 && (body.after_message_email = context.propsValue.afterMessageEmail), context.propsValue.afterMessageEmailSubject !== void 0 && (body.after_message_email_subject = context.propsValue.afterMessageEmailSubject), context.propsValue.seoTitle !== void 0 && (body.seo_title = context.propsValue.seoTitle), context.propsValue.seoDescription !== void 0 && (body.seo_description = context.propsValue.seoDescription), context.propsValue.seoAllowBots !== void 0 && (body.seo_allow_bots = context.propsValue.seoAllowBots), context.propsValue.discordEnabled !== void 0 && (body.discord_enabled = context.propsValue.discordEnabled), context.propsValue.slackEnabled !== void 0 && (body.slack_enabled = context.propsValue.slackEnabled), context.propsValue.googleSheetsEnabled !== void 0 && (body.google_sheets_enabled = context.propsValue.googleSheetsEnabled), context.propsValue.hubspotEnabled !== void 0 && (body.hubspot_enabled = context.propsValue.hubspotEnabled), context.propsValue.captcha !== void 0 && (body.captcha = context.propsValue.captcha), context.propsValue.captureLocation !== void 0 && (body.capture_location = context.propsValue.captureLocation), context.propsValue.showFormTitle !== void 0 && (body.show_formtitle = context.propsValue.showFormTitle), context.propsValue.showMultipageProgress !== void 0 && (body.show_multipage_progress = context.propsValue.showMultipageProgress), context.propsValue.aiSummaryEnabled !== void 0 && (body.ai_summary_enabled = context.propsValue.aiSummaryEnabled), context.propsValue.sendPdfToRespondent !== void 0 && (body.send_pdf_to_respondent = context.propsValue.sendPdfToRespondent), context.propsValue.numberPrefix !== void 0 && (body.number_prefix = context.propsValue.numberPrefix), context.propsValue.numberSuffix !== void 0 && (body.number_suffix = context.propsValue.numberSuffix);
    let response = await deftformApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.POST,
      path: `/forms/${context.propsValue.formId}/settings`,
      body
    });
    return {
      id: (_b = (_a = response.body.data) == null ? void 0 : _a.id) != null ? _b : null,
      name: (_d = (_c = response.body.data) == null ? void 0 : _c.name) != null ? _d : null,
      updated_at: (_f = (_e = response.body.data) == null ? void 0 : _e.updated_at) != null ? _f : null
    };
  }
});

// ../ap/packages/pieces/community/deftform/src/lib/triggers/new-form-response.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var setupMarkdown = `
**Setup instructions:**
1. In your Deftform workspace go to **Settings \u2192 Notifications / Connections**.
2. Under **Webhook**, click **ADD ENDPOINT** and paste the URL below:
\`\`\`
{{webhookUrl}}
\`\`\`
3. Save the endpoint, then open the form you want to watch, go to its **Notifications / Connections** tab, and enable the webhook you just created.
`, newFormResponseTrigger = (0, import_pieces_framework10.createTrigger)({
  auth: deftformAuth,
  name: "new_form_response",
  classification: "READ",
  displayName: "New Form Response",
  description: "Triggers instantly when a new response is submitted to a Deftform form.",
  aiMetadata: {
    description: "Fires when a new response is submitted to a watched Deftform form, delivering the submitted answers. Requires a webhook endpoint to be configured manually in the Deftform workspace settings and enabled on the target form."
  },
  props: {
    instructions: import_pieces_framework10.Property.MarkDown({ value: setupMarkdown })
  },
  sampleData: {
    data: [
      [
        { label: "Full name", response: "John Doe", uuid: "6403fc2b-6d52-4231-b63f-db6ea9f651dd", custom_key: "full_name" },
        { label: "Email address", response: "john@example.com", uuid: "6403fc2b-6d52-4231-b63f-db6ea9f651de", custom_key: "email_address" }
      ]
    ]
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(_context) {
  },
  async onDisable(_context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/deftform/src/index.ts
var deftform = (0, import_pieces_framework11.createPiece)({
  displayName: "Deftform",
  description: "Build powerful forms and automate your workflow with Deftform.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/deftform.png",
  categories: [import_pieces_framework12.PieceCategory.FORMS_AND_SURVEYS],
  auth: deftformAuth,
  authors: ["cumonvip1"],
  actions: [
    getWorkspaceDetails,
    getAllForms,
    getFormFields,
    getFormResponses,
    getSubmissionPdf,
    addFormResponse,
    updateFormSettings,
    (0, import_pieces_common10.createCustomApiCallAction)({
      baseUrl: () => "https://deftform.com/api/v1",
      auth: deftformAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth}`
      })
    })
  ],
  triggers: [newFormResponseTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  deftform,
  deftformAuth
});
