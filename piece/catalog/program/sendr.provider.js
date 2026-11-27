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

// ../ap/packages/pieces/community/sendr/src/index.ts
var index_exports = {};
__export(index_exports, {
  sendr: () => sendr,
  sendrAuth: () => sendrAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework25 = require("@activepieces/pieces-framework"), import_pieces_common25 = require("@activepieces/pieces-common"), import_pieces_framework26 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/sendr/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), sendrAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
**How to get your API key:**
1. Log in to your Sendr dashboard at https://app.sendr.io.
2. Go to **Settings > API Keys**.
3. Click **Create New Key**.
4. Copy the key and paste it below.

Need help? Contact Sendr support at https://sendr.io/support`,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.sendr.io/seat/me",
        headers: { Authorization: `Bearer ${auth}` }
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid API Key. Please verify your key and try again." };
    }
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var BASE_URL = "https://api.sendr.io/api/v1";
async function sendrApiCall({
  token,
  method,
  path,
  body,
  queryParams
}) {
  return await import_pieces_common2.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    authentication: {
      type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
      token
    },
    queryParams,
    body
  });
}
function flattenObject(obj, prefix = "") {
  if (obj == null) return {};
  let result = {};
  for (let [key, value] of Object.entries(obj)) {
    let flatKey = prefix ? `${prefix}_${key}` : key;
    value == null ? result[flatKey] = null : Array.isArray(value) ? result[flatKey] = value.map((v) => typeof v == "object" ? JSON.stringify(v) : String(v)).join(", ") : typeof value == "object" ? Object.assign(result, flattenObject(value, flatKey)) : result[flatKey] = value;
  }
  return result;
}
var sheetDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Sheet",
  description: "Select the contact list (sheet) to use.",
  refreshers: [],
  auth: sendrAuth,
  required: !0,
  options: async ({ auth }) => {
    var _a, _b;
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your Sendr account first" };
    try {
      return {
        disabled: !1,
        options: ((_b = (_a = (await sendrApiCall({
          token: auth.secret_text,
          method: import_pieces_common2.HttpMethod.GET,
          path: "/sheet"
        })).body) == null ? void 0 : _a.sheets) != null ? _b : []).map((s) => ({
          label: s.name ? `${s.name} (${s.id})` : s.id,
          value: s.id
        }))
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: "Failed to load sheets. Check your connection." };
    }
  }
}), campaignDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Campaign",
  description: "Select the campaign.",
  refreshers: [],
  required: !0,
  auth: sendrAuth,
  options: async ({ auth }) => {
    var _a, _b;
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your Sendr account first" };
    try {
      return {
        disabled: !1,
        options: ((_b = (_a = (await sendrApiCall({
          token: auth.secret_text,
          method: import_pieces_common2.HttpMethod.GET,
          path: "/campaigns",
          queryParams: { limit: "200" }
        })).body) == null ? void 0 : _a.items) != null ? _b : []).map((c) => {
          var _a2;
          return {
            label: c.name ? `${c.name} (${(_a2 = c.status) != null ? _a2 : "unknown"})` : c.id,
            value: c.id
          };
        })
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: "Failed to load campaigns. Check your connection." };
    }
  }
}), pageTemplateDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Page Template",
  description: "Select a Sendr Page template to generate content from.",
  refreshers: [],
  required: !0,
  auth: sendrAuth,
  options: async ({ auth }) => {
    var _a, _b;
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your Sendr account first" };
    try {
      return {
        disabled: !1,
        options: ((_b = (_a = (await sendrApiCall({
          token: auth.secret_text,
          method: import_pieces_common2.HttpMethod.GET,
          path: "/page-template/list"
        })).body) == null ? void 0 : _a.templates) != null ? _b : []).map((t) => ({
          label: t.name ? `${t.name} (${t.id})` : t.id,
          value: t.id
        }))
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: "Failed to load page templates. Check your connection." };
    }
  }
}), webhookDropdown = import_pieces_framework2.Property.Dropdown({
  displayName: "Webhook",
  description: "Select the webhook to manage.",
  refreshers: [],
  required: !0,
  auth: sendrAuth,
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return { disabled: !0, options: [], placeholder: "Please connect your Sendr account first" };
    try {
      return {
        disabled: !1,
        options: ((_a = (await sendrApiCall({
          token: auth.secret_text,
          method: import_pieces_common2.HttpMethod.GET,
          path: "/webhook"
        })).body) != null ? _a : []).map((w) => ({
          label: w.name ? `${w.name} (${w.url})` : w.url,
          value: w.url
        }))
      };
    } catch (e) {
      return { disabled: !0, options: [], placeholder: "Failed to load webhooks. Check your connection." };
    }
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/get-user-info.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var getUserInfo = (0, import_pieces_framework3.createAction)({
  auth: sendrAuth,
  name: "get_user_info",
  displayName: "Get Account Info",
  description: "Returns information about the currently connected Sendr API user. Useful for verifying that your API key is valid.",
  audience: "both",
  aiMetadata: { description: "Fetches the Sendr account/seat tied to the connected API key. Use it to verify the connection is valid or to read the current user identity before other Sendr calls. Read-only; takes no input.", idempotent: !0 },
  props: {},
  async run(context) {
    let response = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.GET,
      url: "https://api.sendr.io/seat/me",
      headers: { Authorization: `Bearer ${context.auth.secret_text}` }
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/list-sheets.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var listSheets = (0, import_pieces_framework4.createAction)({
  auth: sendrAuth,
  name: "list_sheets",
  displayName: "List Sheets",
  description: "Lists all available contact lists (sheets) in your Sendr workspace.",
  audience: "both",
  aiMetadata: { description: "Lists all contact lists (sheets) in the Sendr workspace, returning each sheet id and name. Use it to discover the sheet id needed by sheet-scoped actions like Get Sheet, Get Sheet Columns, or Add Row to Sheet. Read-only; takes no input.", idempotent: !0 },
  props: {},
  async run(context) {
    var _a, _b;
    return ((_b = (_a = (await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common4.HttpMethod.GET,
      path: "/sheet"
    })).body) == null ? void 0 : _a.items) != null ? _b : []).map((item) => {
      var _a2, _b2, _c, _d;
      return {
        id: item.id,
        name: (_a2 = item.name) != null ? _a2 : null,
        campaign_id: (_b2 = item.campaignId) != null ? _b2 : null,
        created_at: (_c = item.createdAt) != null ? _c : null,
        updated_at: (_d = item.updatedAt) != null ? _d : null
      };
    });
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/get-sheet.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var getSheet = (0, import_pieces_framework5.createAction)({
  auth: sendrAuth,
  name: "get_sheet",
  displayName: "Get Sheet",
  description: "Returns details of a specific contact list (sheet).",
  audience: "both",
  aiMetadata: { description: "Fetches details of a single contact list (sheet) by its id. Use it after List Sheets to inspect a specific sheet. Read-only; requires the sheet id.", idempotent: !0 },
  props: {
    sheet: sheetDropdown
  },
  async run(context) {
    let response = await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common5.HttpMethod.GET,
      path: `/sheet/${context.propsValue.sheet}`
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/get-sheet-columns.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getSheetColumns = (0, import_pieces_framework6.createAction)({
  auth: sendrAuth,
  name: "get_sheet_columns",
  displayName: "Get Sheet Columns",
  description: "Returns all columns (fields) of a contact list, including their enrichment status.",
  audience: "both",
  aiMetadata: { description: "Lists the columns (fields) of a contact list (sheet), including each column type and enrichment status. Use it to discover the valid column names before building row data for Add Row to Sheet. Read-only; requires the sheet id.", idempotent: !0 },
  props: {
    sheet: sheetDropdown
  },
  async run(context) {
    var _a, _b;
    return ((_b = (_a = (await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common6.HttpMethod.GET,
      path: `/sheet/${context.propsValue.sheet}/column`
    })).body) == null ? void 0 : _a.columns) != null ? _b : []).map((col) => {
      var _a2, _b2, _c;
      return {
        id: col.id,
        name: (_a2 = col.name) != null ? _a2 : null,
        type: (_b2 = col.type) != null ? _b2 : null,
        enriched: (_c = col.enriched) != null ? _c : null
      };
    });
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/add-row-to-sheet.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var addRowToSheet = (0, import_pieces_framework7.createAction)({
  auth: sendrAuth,
  name: "add_row_to_sheet",
  displayName: "Add Row to Sheet",
  description: "Adds a new contact or record to a selected contact list (sheet).",
  audience: "both",
  aiMetadata: { description: "Appends a new contact/record row to a sheet. Provide the sheet id and a key-value object where keys match the sheet column names (call Get Sheet Columns first to learn them). Not idempotent: each call inserts another row.", idempotent: !1 },
  props: {
    sheet: sheetDropdown,
    instructions: import_pieces_framework7.Property.MarkDown({
      value: `### How to Add a Row
1. Select the target sheet above.
2. Enter the row data below using column names as keys and the cell values as values.`
    }),
    rowData: import_pieces_framework7.Property.Object({
      displayName: "Row Data",
      description: 'Key-value pairs representing the new row. Keys should match the sheet column names. Example: { "Email": "john@example.com", "First Name": "John" }',
      required: !0
    })
  },
  async run(context) {
    let response = await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common7.HttpMethod.POST,
      path: `/sheet/${context.propsValue.sheet}/row`,
      body: context.propsValue.rowData
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/list-campaigns.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var listCampaigns = (0, import_pieces_framework8.createAction)({
  auth: sendrAuth,
  name: "list_campaigns",
  displayName: "List Campaigns",
  description: "Lists all campaigns in your Sendr workspace, optionally filtered by status.",
  audience: "both",
  aiMetadata: { description: "Lists campaigns in the Sendr workspace (up to 50), optionally narrowed by status (Draft, Active, or Paused); leaving the status filter empty returns all campaigns. Use it to discover the campaign id needed by Get Campaign. Read-only.", idempotent: !0 },
  props: {
    status: import_pieces_framework8.Property.StaticDropdown({
      displayName: "Status Filter",
      description: "Filter campaigns by their current status.",
      required: !1,
      defaultValue: "",
      options: {
        options: [
          { label: "All", value: "" },
          { label: "Draft", value: "DRAFT" },
          { label: "Active", value: "ACTIVE" },
          { label: "Paused", value: "PAUSED" }
        ]
      }
    })
  },
  async run(context) {
    var _a, _b;
    let queryParams = { limit: "50" };
    return context.propsValue.status && (queryParams.status = context.propsValue.status), ((_b = (_a = (await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common8.HttpMethod.GET,
      path: "/campaigns",
      queryParams
    })).body) == null ? void 0 : _a.items) != null ? _b : []).map((item) => {
      var _a2, _b2, _c;
      return {
        id: item.id,
        name: (_a2 = item.name) != null ? _a2 : null,
        status: (_b2 = item.status) != null ? _b2 : null,
        created_at: (_c = item.createdAt) != null ? _c : null
      };
    });
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/get-campaign.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var getCampaign = (0, import_pieces_framework9.createAction)({
  auth: sendrAuth,
  name: "get_campaign",
  displayName: "Get Campaign",
  description: "Returns detailed information about a specific campaign.",
  audience: "both",
  aiMetadata: { description: "Fetches full details of a single campaign by its id. Use it after List Campaigns to inspect one campaign. Read-only; requires the campaign id.", idempotent: !0 },
  props: {
    campaign: campaignDropdown
  },
  async run(context) {
    let response = await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common9.HttpMethod.GET,
      path: `/campaigns/${context.propsValue.campaign}`
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/list-page-templates.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var listPageTemplates = (0, import_pieces_framework10.createAction)({
  auth: sendrAuth,
  name: "list_page_templates",
  displayName: "List Page Templates",
  description: "Lists all available Sendr Page templates you can use to create personalized landing pages.",
  audience: "both",
  aiMetadata: { description: "Lists the Sendr Page templates available for building personalized landing pages, returning each template id and name. Use it to discover the template id needed by Generate Sendr Page or Get Page Template Variables. Read-only; takes no input.", idempotent: !0 },
  props: {},
  async run(context) {
    var _a, _b;
    return ((_b = (_a = (await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common10.HttpMethod.GET,
      path: "/page-template/list"
    })).body) == null ? void 0 : _a.templates) != null ? _b : []).map((t) => {
      var _a2, _b2;
      return {
        id: t.id,
        name: (_a2 = t.name) != null ? _a2 : null,
        created_at: (_b2 = t.createdAt) != null ? _b2 : null
      };
    });
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/get-page-template-variables.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var getPageTemplateVariables = (0, import_pieces_framework11.createAction)({
  auth: sendrAuth,
  name: "get_page_template_variables",
  displayName: "Get Page Template Variables",
  description: "Returns the custom variables (placeholders) defined in a selected page template \uFFFD for example first_name, company, or email.",
  audience: "both",
  aiMetadata: { description: "Lists the variables (placeholders) defined in a page template, such as first_name or company, with each variable name, type, and default value. Use it to learn which variable keys to supply when calling Generate Sendr Page. Read-only; requires the template id.", idempotent: !0 },
  props: {
    template: pageTemplateDropdown
  },
  async run(context) {
    var _a, _b;
    return ((_b = (_a = (await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common11.HttpMethod.GET,
      path: `/page-template/${context.propsValue.template}/variables`
    })).body) == null ? void 0 : _a.variables) != null ? _b : []).map((v) => {
      var _a2, _b2, _c;
      return {
        id: v.id,
        name: (_a2 = v.name) != null ? _a2 : null,
        type: (_b2 = v.type) != null ? _b2 : null,
        default_value: (_c = v.defaultValue) != null ? _c : null
      };
    });
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/generate-sendr-page.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common");
var generateSendrPage = (0, import_pieces_framework12.createAction)({
  auth: sendrAuth,
  name: "generate_sendr_page",
  displayName: "Generate Sendr Page",
  description: "Creates a personalized Sendr landing page from a template with custom variables.",
  audience: "both",
  aiMetadata: { description: "Creates a personalized Sendr landing page by rendering a template with supplied variable values; optionally screenshots a website background, renders an animated GIF, and posts status updates to a callback URL. Requires a template id and a key-value variables object (use Get Page Template Variables to find the keys). Not idempotent: each call generates a new page.", idempotent: !1 },
  props: {
    instructions: import_pieces_framework12.Property.MarkDown({
      value: `### How to Generate a Page
1. Choose a **Page Template** below.
2. Fill in the variable values (e.g. first_name = "Anna", company = "Acme").
3. Optionally provide a website URL to screenshot, or enable GIF/audio rendering.`
    }),
    template: pageTemplateDropdown,
    websiteUrl: import_pieces_framework12.Property.ShortText({
      displayName: "Website to Screenshot URL",
      description: "Optional. Provide a website URL if the template supports background screenshots.",
      required: !1
    }),
    renderGif: import_pieces_framework12.Property.Checkbox({
      displayName: "Render GIF",
      description: "If enabled, Sendr will generate an animated GIF version of the page.",
      required: !1,
      defaultValue: !1
    }),
    attributes: import_pieces_framework12.Property.Object({
      displayName: "Page Attributes",
      description: "Optional metadata attributes for the generated page (key-value pairs).",
      required: !1
    }),
    webhookUrl: import_pieces_framework12.Property.ShortText({
      displayName: "Webhook URL",
      description: "Optional callback URL to receive status updates (e.g. when GIF or audio is ready).",
      required: !1
    }),
    variables: import_pieces_framework12.Property.Object({
      displayName: "Template Variables",
      description: 'Key-value pairs matching the template variables. Example: { "first_name": "Anna", "company": "Acme Inc" }',
      required: !0
    })
  },
  async run(context) {
    let body = {
      templateId: context.propsValue.template,
      variablesValues: context.propsValue.variables
    };
    context.propsValue.websiteUrl && (body.websiteToScreenshotUrl = context.propsValue.websiteUrl), context.propsValue.renderGif && (body.renderGif = !0), context.propsValue.attributes && (body.attributes = context.propsValue.attributes), context.propsValue.webhookUrl && (body.webhookUrl = context.propsValue.webhookUrl);
    let response = await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common12.HttpMethod.POST,
      path: "/enrichment/sendr-page",
      body
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/queue-dynamic-audio.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common");
var queueDynamicAudio = (0, import_pieces_framework13.createAction)({
  auth: sendrAuth,
  name: "queue_dynamic_audio",
  displayName: "Queue Dynamic Audio Generation",
  description: "Queues a job to generate personalized audio by replacing a target word in a base audio file.",
  audience: "both",
  aiMetadata: { description: "Queues an async job that produces personalized audio by replacing a target word in a base audio file with a replacement word, optionally using a specific ElevenLabs voice and language. Requires the base audio URL, target word, and replacement word; results are delivered later (optionally to a callback URL). Not idempotent: each call enqueues a new generation job.", idempotent: !1 },
  props: {
    audioUrl: import_pieces_framework13.Property.ShortText({
      displayName: "Base Audio URL",
      description: "Public URL of the base audio file to personalize.",
      required: !0
    }),
    targetWord: import_pieces_framework13.Property.ShortText({
      displayName: "Target Word",
      description: 'The word to find and replace in the audio (e.g. "first_name" or "[NAME]").',
      required: !0
    }),
    replacementWord: import_pieces_framework13.Property.ShortText({
      displayName: "Replacement Word",
      description: 'The word to insert in place of the target word (e.g. "Anna").',
      required: !0
    }),
    elevenlabsId: import_pieces_framework13.Property.ShortText({
      displayName: "ElevenLabs Voice ID",
      description: "Optional ElevenLabs voice ID for the generated audio.",
      required: !1
    }),
    languageCode: import_pieces_framework13.Property.ShortText({
      displayName: "Language Code",
      description: 'Optional language code (e.g. "en-US", "de-DE").',
      required: !1,
      defaultValue: "en-US"
    }),
    webhookUrl: import_pieces_framework13.Property.ShortText({
      displayName: "Webhook URL",
      description: "Optional callback URL to receive status updates when the audio job is done.",
      required: !1
    })
  },
  async run(context) {
    let body = {
      audioUrl: context.propsValue.audioUrl,
      targetWord: context.propsValue.targetWord,
      replacementWord: context.propsValue.replacementWord
    };
    context.propsValue.elevenlabsId && (body.elevenlabsId = context.propsValue.elevenlabsId), context.propsValue.languageCode && (body.languageCode = context.propsValue.languageCode), context.propsValue.webhookUrl && (body.webhookUrl = context.propsValue.webhookUrl);
    let response = await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common13.HttpMethod.POST,
      path: "/enrichment/dynamic-audio",
      body
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/queue-video-generation.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");
var queueVideoGeneration = (0, import_pieces_framework14.createAction)({
  auth: sendrAuth,
  name: "queue_video_generation",
  displayName: "Queue Video Generation",
  description: "Queues a job to merge audio and video or generate a lipsync video.",
  audience: "both",
  aiMetadata: { description: "Queues an async video-generation job in one of two modes: merge an audio track onto a base video, or produce a lipsync video. Requires the mode and audio URL (plus a video/Mux source as the mode needs); results are delivered later (optionally to a callback URL). Not idempotent: each call enqueues a new job.", idempotent: !1 },
  props: {
    mode: import_pieces_framework14.Property.StaticDropdown({
      displayName: "Mode",
      description: "Choose the video generation mode.",
      required: !0,
      defaultValue: "merge",
      options: {
        options: [
          { label: "Merge Audio + Video", value: "merge" },
          { label: "Lipsync", value: "lipsync" }
        ]
      }
    }),
    audioUrl: import_pieces_framework14.Property.ShortText({
      displayName: "Audio URL",
      description: "Public URL of the audio track to use.",
      required: !0
    }),
    videoUrl: import_pieces_framework14.Property.ShortText({
      displayName: "Video URL",
      description: "Public URL of the base video to use.",
      required: !1
    }),
    muxAssetId: import_pieces_framework14.Property.ShortText({
      displayName: "Mux Asset ID",
      description: "Optional Mux asset ID for the video source.",
      required: !1
    }),
    targetWord: import_pieces_framework14.Property.ShortText({
      displayName: "Target Word",
      description: "Optional target word for personalization.",
      required: !1
    }),
    replacementWord: import_pieces_framework14.Property.ShortText({
      displayName: "Replacement Word",
      description: "Optional replacement word.",
      required: !1
    }),
    pageSlug: import_pieces_framework14.Property.ShortText({
      displayName: "Page Slug",
      description: "Optional Sendr Page slug to associate with this video.",
      required: !1
    }),
    webhookUrl: import_pieces_framework14.Property.ShortText({
      displayName: "Webhook URL",
      description: "Optional callback URL to receive status updates when the video job is done.",
      required: !1
    })
  },
  async run(context) {
    let body = {
      audioUrl: context.propsValue.audioUrl,
      mode: context.propsValue.mode
    };
    context.propsValue.videoUrl && (body.videoUrl = context.propsValue.videoUrl), context.propsValue.muxAssetId && (body.muxAssetId = context.propsValue.muxAssetId), context.propsValue.targetWord && (body.targetWord = context.propsValue.targetWord), context.propsValue.replacementWord && (body.replacementWord = context.propsValue.replacementWord), context.propsValue.pageSlug && (body.pageSlug = context.propsValue.pageSlug), context.propsValue.webhookUrl && (body.webhookUrl = context.propsValue.webhookUrl);
    let response = await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common14.HttpMethod.POST,
      path: "/enrichment/generate-video",
      body
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/list-webhooks.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework"), import_pieces_common15 = require("@activepieces/pieces-common");
var listWebhooks = (0, import_pieces_framework15.createAction)({
  auth: sendrAuth,
  name: "list_webhooks",
  displayName: "List Webhooks",
  description: "Lists all registered webhooks in your Sendr workspace.",
  audience: "both",
  aiMetadata: { description: "Lists all webhooks registered in the Sendr workspace, returning each webhook URL, name, subscribed events, and enabled state. Use it to discover the webhook URL (the identifier) needed by Update/Delete/Toggle Webhook or Reveal Webhook Secret. Read-only; takes no input.", idempotent: !0 },
  props: {},
  async run(context) {
    var _a, _b;
    return ((_b = (_a = (await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common15.HttpMethod.GET,
      path: "/webhook"
    })).body) == null ? void 0 : _a.webhooks) != null ? _b : []).map((w) => {
      var _a2, _b2;
      return {
        url: w.url,
        name: (_a2 = w.name) != null ? _a2 : null,
        events: Array.isArray(w.events) ? w.events.join(", ") : null,
        enabled: (_b2 = w.enabled) != null ? _b2 : null
      };
    });
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/create-webhook.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common16 = require("@activepieces/pieces-common");
var createWebhook = (0, import_pieces_framework16.createAction)({
  auth: sendrAuth,
  name: "create_webhook",
  displayName: "Create Webhook",
  description: "Registers a new webhook in Sendr to receive event notifications.",
  audience: "both",
  aiMetadata: { description: "Registers a new webhook so Sendr posts event notifications to a target URL. Requires a name, the destination URL, and a list of event names to subscribe to (e.g. page_render:created, contact_page_engagement:created). Not idempotent: each call registers another webhook.", idempotent: !1 },
  props: {
    name: import_pieces_framework16.Property.ShortText({
      displayName: "Webhook Name",
      description: "A human-readable name for the webhook.",
      required: !0
    }),
    url: import_pieces_framework16.Property.ShortText({
      displayName: "Webhook URL",
      description: "The endpoint URL that Sendr will call when events occur.",
      required: !0
    }),
    events: import_pieces_framework16.Property.Array({
      displayName: "Events",
      description: "List of event names to subscribe to (e.g. page_render:created, contact_page_engagement:created).",
      required: !0
    }),
    attributes: import_pieces_framework16.Property.Object({
      displayName: "Attributes",
      description: "Optional metadata attributes for the webhook.",
      required: !1
    }),
    resourceType: import_pieces_framework16.Property.ShortText({
      displayName: "Resource Type",
      description: "Optional resource type filter for the webhook.",
      required: !1
    }),
    resourceId: import_pieces_framework16.Property.ShortText({
      displayName: "Resource ID",
      description: "Optional resource ID filter for the webhook.",
      required: !1
    })
  },
  async run(context) {
    let body = {
      name: context.propsValue.name,
      url: context.propsValue.url,
      events: context.propsValue.events
    };
    context.propsValue.attributes && (body.attributes = context.propsValue.attributes), context.propsValue.resourceType && (body.resourceType = context.propsValue.resourceType), context.propsValue.resourceId && (body.resourceId = context.propsValue.resourceId);
    let response = await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common16.HttpMethod.POST,
      path: "/webhook",
      body
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/update-webhook.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), import_pieces_common17 = require("@activepieces/pieces-common");
var updateWebhook = (0, import_pieces_framework17.createAction)({
  auth: sendrAuth,
  name: "update_webhook",
  displayName: "Update Webhook",
  description: "Updates an existing Sendr webhook by its URL.",
  audience: "both",
  aiMetadata: { description: "Updates an existing webhook, identified by its URL, replacing the supplied fields (name, subscribed events, attributes, or resource filters). Use List Webhooks to find the URL. Idempotent: repeating the same update keyed on that URL yields the same final state.", idempotent: !0 },
  props: {
    webhook: webhookDropdown,
    name: import_pieces_framework17.Property.ShortText({
      displayName: "Webhook Name",
      description: "New name for the webhook.",
      required: !1
    }),
    events: import_pieces_framework17.Property.Array({
      displayName: "Events",
      description: "Updated list of event names to subscribe to.",
      required: !1
    }),
    attributes: import_pieces_framework17.Property.Object({
      displayName: "Attributes",
      description: "Updated metadata attributes.",
      required: !1
    }),
    resourceType: import_pieces_framework17.Property.ShortText({
      displayName: "Resource Type",
      description: "Updated resource type filter.",
      required: !1
    }),
    resourceId: import_pieces_framework17.Property.ShortText({
      displayName: "Resource ID",
      description: "Updated resource ID filter.",
      required: !1
    })
  },
  async run(context) {
    let body = {};
    context.propsValue.name && (body.name = context.propsValue.name), context.propsValue.events && (body.events = context.propsValue.events), context.propsValue.attributes && (body.attributes = context.propsValue.attributes), context.propsValue.resourceType && (body.resourceType = context.propsValue.resourceType), context.propsValue.resourceId && (body.resourceId = context.propsValue.resourceId);
    let response = await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common17.HttpMethod.PATCH,
      path: "/webhook",
      body: __spreadProps(__spreadValues({}, body), { url: context.propsValue.webhook })
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/delete-webhook.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework"), import_pieces_common18 = require("@activepieces/pieces-common");
var deleteWebhook = (0, import_pieces_framework18.createAction)({
  auth: sendrAuth,
  name: "delete_webhook",
  displayName: "Delete Webhook",
  description: "Deletes a registered Sendr webhook by its URL.",
  audience: "both",
  aiMetadata: { description: "Deletes a registered webhook identified by its URL. Use List Webhooks to find the URL. Idempotent: once removed, repeating the call leaves the webhook absent.", idempotent: !0 },
  props: {
    webhook: webhookDropdown
  },
  async run(context) {
    let response = await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common18.HttpMethod.DELETE,
      path: "/webhook",
      body: { url: context.propsValue.webhook }
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/reveal-webhook-secret.ts
var import_pieces_framework19 = require("@activepieces/pieces-framework"), import_pieces_common19 = require("@activepieces/pieces-common");
var revealWebhookSecret = (0, import_pieces_framework19.createAction)({
  auth: sendrAuth,
  name: "reveal_webhook_secret",
  displayName: "Reveal Webhook Secret",
  description: "Reveals the secret key for a registered webhook so you can verify incoming signatures.",
  audience: "both",
  aiMetadata: { description: "Returns the signing secret of a registered webhook, identified by its URL, so incoming webhook signatures can be verified. Use List Webhooks to find the URL. Read-only lookup; repeating the call returns the same secret.", idempotent: !0 },
  props: {
    webhook: webhookDropdown
  },
  async run(context) {
    let response = await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common19.HttpMethod.POST,
      path: "/webhook/reveal-secret",
      body: { url: context.propsValue.webhook }
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/actions/toggle-webhook.ts
var import_pieces_framework20 = require("@activepieces/pieces-framework"), import_pieces_common20 = require("@activepieces/pieces-common");
var toggleWebhook = (0, import_pieces_framework20.createAction)({
  auth: sendrAuth,
  name: "toggle_webhook",
  displayName: "Toggle Webhook",
  description: "Enables or disables a registered Sendr webhook.",
  audience: "both",
  aiMetadata: { description: "Enables or disables a registered webhook, identified by its URL, by setting its enabled flag. Use List Webhooks to find the URL. Idempotent: repeating the call with the same enabled value yields the same state.", idempotent: !0 },
  props: {
    webhook: webhookDropdown,
    enabled: import_pieces_framework20.Property.Checkbox({
      displayName: "Enabled",
      description: "Check to enable the webhook, uncheck to disable it.",
      required: !0,
      defaultValue: !0
    })
  },
  async run(context) {
    let response = await sendrApiCall({
      token: context.auth.secret_text,
      method: import_pieces_common20.HttpMethod.POST,
      path: "/webhook/enabling",
      body: {
        url: context.propsValue.webhook,
        enabled: context.propsValue.enabled
      }
    });
    return flattenObject(response.body);
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/triggers/page-render-created.ts
var import_pieces_framework21 = require("@activepieces/pieces-framework"), import_pieces_common21 = require("@activepieces/pieces-common");
var pageRenderCreated = (0, import_pieces_framework21.createTrigger)({
  auth: sendrAuth,
  name: "page_render_created",
  displayName: "New Page Render",
  description: "Triggers when a new Sendr Page is rendered.",
  aiMetadata: {
    description: "Fires when a new personalized Sendr Page is rendered, delivering the page id, URL, slug, and source template. Use it to act each time a page is freshly generated."
  },
  props: {},
  sampleData: {
    event_type: "page_render:created",
    page_id: "cuid_example",
    page_url: "https://pages.sendr.io/example",
    page_slug: "example-slug",
    template_id: "cuid_template",
    event_status: "created"
  },
  type: import_pieces_framework21.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let oldUrl = await context.store.get("webhookUrl");
    if (oldUrl) {
      try {
        await sendrApiCall({
          token: context.auth.secret_text,
          method: import_pieces_common21.HttpMethod.DELETE,
          path: "/webhook",
          body: { url: oldUrl }
        });
      } catch (e) {
      }
      await context.store.delete("webhookUrl");
    }
    try {
      await sendrApiCall({
        token: context.auth.secret_text,
        method: import_pieces_common21.HttpMethod.POST,
        path: "/webhook",
        body: {
          name: "Activepieces - New Page Render",
          url: context.webhookUrl,
          events: ["page_render.created"]
        }
      }), await context.store.put("webhookUrl", context.webhookUrl);
      try {
        let secretResp = await sendrApiCall({
          token: context.auth.secret_text,
          method: import_pieces_common21.HttpMethod.POST,
          path: "/webhook/reveal-secret",
          body: { url: context.webhookUrl }
        });
        (_a = secretResp.body) != null && _a.secret && await context.store.put("webhookSecret", secretResp.body.secret);
      } catch (e) {
      }
    } catch (e) {
      throw new Error("Failed to register Sendr webhook: " + e.message);
    }
  },
  async onDisable(context) {
    try {
      let webhookUrl = await context.store.get("webhookUrl");
      webhookUrl && await sendrApiCall({
        token: context.auth.secret_text,
        method: import_pieces_common21.HttpMethod.DELETE,
        path: "/webhook",
        body: { url: webhookUrl }
      });
    } catch (e) {
      throw new Error("Failed to unregister Sendr webhook: " + e.message);
    } finally {
      await context.store.delete("webhookUrl"), await context.store.delete("webhookSecret");
    }
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let webhookSecret = await context.store.get("webhookSecret");
    if (webhookSecret && (context.payload.headers || {})["x-webhook-secret"] !== webhookSecret)
      return [];
    let body = context.payload.body;
    return [{
      event_type: (_a = body.event) != null ? _a : "page_render.created",
      page_id: (_b = body.pageId) != null ? _b : null,
      page_url: (_c = body.pageUrl) != null ? _c : null,
      page_slug: (_d = body.pageSlug) != null ? _d : null,
      template_id: (_e = body.templateId) != null ? _e : null,
      event_status: (_f = body.eventStatus) != null ? _f : null
    }];
  },
  async test(context) {
    return [
      {
        event_type: "page_render.created",
        page_id: "test_cuid_page",
        page_url: "https://pages.sendr.io/test",
        page_slug: "test-slug",
        template_id: "test_cuid_template",
        event_status: "created"
      }
    ];
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/triggers/page-render-updated.ts
var import_pieces_framework22 = require("@activepieces/pieces-framework"), import_pieces_common22 = require("@activepieces/pieces-common");
var pageRenderUpdated = (0, import_pieces_framework22.createTrigger)({
  auth: sendrAuth,
  name: "page_render_updated",
  displayName: "Page Render Updated",
  description: "Triggers when a Sendr Page render is updated (e.g. GIF or audio is ready).",
  aiMetadata: {
    description: "Fires when an existing Sendr Page render is updated, typically when asynchronously produced assets become ready (GIF, audio, or lipsync video). Use it to react once media for a page has finished generating."
  },
  props: {},
  sampleData: {
    event_type: "page_render:updated",
    page_id: "cuid_example",
    page_url: "https://pages.sendr.io/example",
    page_slug: "example-slug",
    gif_url: "https://cdn.sendr.io/gif/example.gif",
    audio_url: "https://cdn.sendr.io/audio/example.mp3",
    lipsync_video: "https://cdn.sendr.io/video/example.mp4",
    event_status: "updated"
  },
  type: import_pieces_framework22.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let oldUrl = await context.store.get("webhookUrl");
    if (oldUrl) {
      try {
        await sendrApiCall({
          token: context.auth.secret_text,
          method: import_pieces_common22.HttpMethod.DELETE,
          path: "/webhook",
          body: { url: oldUrl }
        });
      } catch (e) {
      }
      await context.store.delete("webhookUrl");
    }
    try {
      await sendrApiCall({
        token: context.auth.secret_text,
        method: import_pieces_common22.HttpMethod.POST,
        path: "/webhook",
        body: {
          name: "Activepieces - Page Render Updated",
          url: context.webhookUrl,
          events: ["page_render.updated"]
        }
      }), await context.store.put("webhookUrl", context.webhookUrl);
      try {
        let secretResp = await sendrApiCall({
          token: context.auth.secret_text,
          method: import_pieces_common22.HttpMethod.POST,
          path: "/webhook/reveal-secret",
          body: { url: context.webhookUrl }
        });
        (_a = secretResp.body) != null && _a.secret && await context.store.put("webhookSecret", secretResp.body.secret);
      } catch (e) {
      }
    } catch (e) {
      throw new Error("Failed to register Sendr webhook: " + e.message);
    }
  },
  async onDisable(context) {
    try {
      let webhookUrl = await context.store.get("webhookUrl");
      webhookUrl && await sendrApiCall({
        token: context.auth.secret_text,
        method: import_pieces_common22.HttpMethod.DELETE,
        path: "/webhook",
        body: { url: webhookUrl }
      });
    } catch (e) {
      throw new Error("Failed to unregister Sendr webhook: " + e.message);
    } finally {
      await context.store.delete("webhookUrl"), await context.store.delete("webhookSecret");
    }
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h;
    let webhookSecret = await context.store.get("webhookSecret");
    if (webhookSecret && (context.payload.headers || {})["x-webhook-secret"] !== webhookSecret)
      return [];
    let body = context.payload.body;
    return [{
      event_type: (_a = body.event) != null ? _a : "page_render.updated",
      page_id: (_b = body.pageId) != null ? _b : null,
      page_url: (_c = body.pageUrl) != null ? _c : null,
      page_slug: (_d = body.pageSlug) != null ? _d : null,
      gif_url: (_e = body.gifUrl) != null ? _e : null,
      audio_url: (_f = body.audioUrl) != null ? _f : null,
      lipsync_video: (_g = body.lipsyncVideo) != null ? _g : null,
      event_status: (_h = body.eventStatus) != null ? _h : null
    }];
  },
  async test(context) {
    return [
      {
        event_type: "page_render.updated",
        page_id: "test_cuid_page",
        page_url: "https://pages.sendr.io/test",
        page_slug: "test-slug",
        gif_url: "https://cdn.sendr.io/gif/test.gif",
        audio_url: "https://cdn.sendr.io/audio/test.mp3",
        lipsync_video: "https://cdn.sendr.io/video/test.mp4",
        event_status: "updated"
      }
    ];
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/triggers/contact-page-engagement-created.ts
var import_pieces_framework23 = require("@activepieces/pieces-framework"), import_pieces_common23 = require("@activepieces/pieces-common");
var contactPageEngagementCreated = (0, import_pieces_framework23.createTrigger)({
  auth: sendrAuth,
  name: "contact_page_engagement_created",
  displayName: "New Contact Page Engagement",
  description: "Triggers when a contact visits a Sendr Page for the first time.",
  aiMetadata: {
    description: "Fires the first time a contact engages with (visits) a Sendr Page, delivering the page id/slug/URL and the contact attributes. Use it to act on a contact first visiting a page."
  },
  props: {},
  sampleData: {
    event_type: "contact_page_engagement:created",
    page_id: "cuid_example",
    page_slug: "example-slug",
    page_url: "https://pages.sendr.io/example",
    attributes: JSON.stringify({ first_name: "John", company: "Acme" }),
    timestamp: "2026-04-24T14:00:00Z"
  },
  type: import_pieces_framework23.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let oldUrl = await context.store.get("webhookUrl");
    if (oldUrl) {
      try {
        await sendrApiCall({
          token: context.auth.secret_text,
          method: import_pieces_common23.HttpMethod.DELETE,
          path: "/webhook",
          body: { url: oldUrl }
        });
      } catch (e) {
      }
      await context.store.delete("webhookUrl");
    }
    try {
      await sendrApiCall({
        token: context.auth.secret_text,
        method: import_pieces_common23.HttpMethod.POST,
        path: "/webhook",
        body: {
          name: "Activepieces - New Contact Page Engagement",
          url: context.webhookUrl,
          events: ["contact_page_engagement.created"]
        }
      }), await context.store.put("webhookUrl", context.webhookUrl);
      try {
        let secretResp = await sendrApiCall({
          token: context.auth.secret_text,
          method: import_pieces_common23.HttpMethod.POST,
          path: "/webhook/reveal-secret",
          body: { url: context.webhookUrl }
        });
        (_a = secretResp.body) != null && _a.secret && await context.store.put("webhookSecret", secretResp.body.secret);
      } catch (e) {
      }
    } catch (e) {
      throw new Error("Failed to register Sendr webhook: " + e.message);
    }
  },
  async onDisable(context) {
    try {
      let webhookUrl = await context.store.get("webhookUrl");
      webhookUrl && await sendrApiCall({
        token: context.auth.secret_text,
        method: import_pieces_common23.HttpMethod.DELETE,
        path: "/webhook",
        body: { url: webhookUrl }
      });
    } catch (e) {
      throw new Error("Failed to unregister Sendr webhook: " + e.message);
    } finally {
      await context.store.delete("webhookUrl"), await context.store.delete("webhookSecret");
    }
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let webhookSecret = await context.store.get("webhookSecret");
    if (webhookSecret && (context.payload.headers || {})["x-webhook-secret"] !== webhookSecret)
      return [];
    let body = context.payload.body;
    return [{
      event_type: (_a = body.event) != null ? _a : "contact_page_engagement.created",
      page_id: (_b = body.pageId) != null ? _b : null,
      page_slug: (_c = body.pageSlug) != null ? _c : null,
      page_url: (_d = body.pageUrl) != null ? _d : null,
      attributes: body.attributes ? JSON.stringify(body.attributes) : null,
      timestamp: (_e = body.timestamp) != null ? _e : null
    }];
  },
  async test(context) {
    return [
      {
        event_type: "contact_page_engagement.created",
        page_id: "test_cuid_page",
        page_slug: "test-slug",
        page_url: "https://pages.sendr.io/test",
        attributes: JSON.stringify({ first_name: "John", company: "Acme" }),
        timestamp: "2026-04-24T14:00:00Z"
      }
    ];
  }
});

// ../ap/packages/pieces/community/sendr/src/lib/triggers/contact-page-engagement-updated.ts
var import_pieces_framework24 = require("@activepieces/pieces-framework"), import_pieces_common24 = require("@activepieces/pieces-common");
var contactPageEngagementUpdated = (0, import_pieces_framework24.createTrigger)({
  auth: sendrAuth,
  name: "contact_page_engagement_updated",
  displayName: "Contact Page Engagement Updated",
  description: "Triggers when a contact interacts with a Sendr Page again (e.g. replays, re-visits).",
  aiMetadata: {
    description: "Fires on subsequent engagement when a contact interacts with a Sendr Page again (re-visit or replay), delivering the page id/slug/URL, contact attributes, and engagement status. Use it to track repeat interaction after the initial visit."
  },
  props: {},
  sampleData: {
    event_type: "contact_page_engagement:updated",
    page_id: "cuid_example",
    page_slug: "example-slug",
    page_url: "https://pages.sendr.io/example",
    attributes: JSON.stringify({ first_name: "John", company: "Acme" }),
    event_status: "engaged",
    timestamp: "2026-04-24T14:05:00Z"
  },
  type: import_pieces_framework24.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let oldUrl = await context.store.get("webhookUrl");
    if (oldUrl) {
      try {
        await sendrApiCall({
          token: context.auth.secret_text,
          method: import_pieces_common24.HttpMethod.DELETE,
          path: "/webhook",
          body: { url: oldUrl }
        });
      } catch (e) {
      }
      await context.store.delete("webhookUrl");
    }
    try {
      await sendrApiCall({
        token: context.auth.secret_text,
        method: import_pieces_common24.HttpMethod.POST,
        path: "/webhook",
        body: {
          name: "Activepieces - Contact Page Engagement Updated",
          url: context.webhookUrl,
          events: ["contact_page_engagement.updated"]
        }
      }), await context.store.put("webhookUrl", context.webhookUrl);
      try {
        let secretResp = await sendrApiCall({
          token: context.auth.secret_text,
          method: import_pieces_common24.HttpMethod.POST,
          path: "/webhook/reveal-secret",
          body: { url: context.webhookUrl }
        });
        (_a = secretResp.body) != null && _a.secret && await context.store.put("webhookSecret", secretResp.body.secret);
      } catch (e) {
      }
    } catch (e) {
      throw new Error("Failed to register Sendr webhook: " + e.message);
    }
  },
  async onDisable(context) {
    try {
      let webhookUrl = await context.store.get("webhookUrl");
      webhookUrl && await sendrApiCall({
        token: context.auth.secret_text,
        method: import_pieces_common24.HttpMethod.DELETE,
        path: "/webhook",
        body: { url: webhookUrl }
      });
    } catch (e) {
      throw new Error("Failed to unregister Sendr webhook: " + e.message);
    } finally {
      await context.store.delete("webhookUrl"), await context.store.delete("webhookSecret");
    }
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f;
    let webhookSecret = await context.store.get("webhookSecret");
    if (webhookSecret && (context.payload.headers || {})["x-webhook-secret"] !== webhookSecret)
      return [];
    let body = context.payload.body;
    return [{
      event_type: (_a = body.event) != null ? _a : "contact_page_engagement.updated",
      page_id: (_b = body.pageId) != null ? _b : null,
      page_slug: (_c = body.pageSlug) != null ? _c : null,
      page_url: (_d = body.pageUrl) != null ? _d : null,
      attributes: body.attributes ? JSON.stringify(body.attributes) : null,
      event_status: (_e = body.eventStatus) != null ? _e : null,
      timestamp: (_f = body.timestamp) != null ? _f : null
    }];
  },
  async test(context) {
    return [
      {
        event_type: "contact_page_engagement.updated",
        page_id: "test_cuid_page",
        page_slug: "test-slug",
        page_url: "https://pages.sendr.io/test",
        attributes: JSON.stringify({ first_name: "John", company: "Acme" }),
        event_status: "engaged",
        timestamp: "2026-04-24T14:05:00Z"
      }
    ];
  }
});

// ../ap/packages/pieces/community/sendr/src/index.ts
var sendr = (0, import_pieces_framework25.createPiece)({
  displayName: "Sendr",
  description: "Create personalized landing pages, dynamic audio, video, and manage campaigns with Sendr.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/sendr.png",
  categories: [import_pieces_framework26.PieceCategory.MARKETING],
  auth: sendrAuth,
  authors: ["cumonvip1"],
  actions: [
    getUserInfo,
    listSheets,
    getSheet,
    getSheetColumns,
    addRowToSheet,
    listCampaigns,
    getCampaign,
    listPageTemplates,
    getPageTemplateVariables,
    generateSendrPage,
    queueDynamicAudio,
    queueVideoGeneration,
    listWebhooks,
    createWebhook,
    updateWebhook,
    deleteWebhook,
    revealWebhookSecret,
    toggleWebhook,
    (0, import_pieces_common25.createCustomApiCallAction)({
      baseUrl: () => BASE_URL,
      auth: sendrAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [
    pageRenderCreated,
    pageRenderUpdated,
    contactPageEngagementCreated,
    contactPageEngagementUpdated
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  sendr,
  sendrAuth
});
