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

// ../ap/packages/pieces/community/cognito-forms/src/index.ts
var index_exports = {};
__export(index_exports, {
  cognitoForms: () => cognitoForms
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/cognito-forms/src/lib/actions/create-entry.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/cognito-forms/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), BASE_URL = "https://www.cognitoforms.com/api";
async function makeRequest({ secret_text }, method, path, body) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${BASE_URL}${path}`,
    headers: {
      Authorization: `Bearer ${secret_text}`,
      "Content-Type": "application/json"
    },
    body
  })).body;
}

// ../ap/packages/pieces/community/cognito-forms/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var cognitoFormsAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: `
  1. Click your organization's name in the top left corner and then click Settings.
  2. Go to the Integrations section and select + New API Key.
  3. Make sure to copy and store your API key, as it cannot be retrieved later.
  `,
  validate: async ({ auth }) => {
    try {
      return await makeRequest({ secret_text: auth, type: import_pieces_framework2.AppConnectionType.SECRET_TEXT }, import_pieces_common2.HttpMethod.GET, "/forms"), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Key."
      };
    }
  }
});

// ../ap/packages/pieces/community/cognito-forms/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var formIdDropdown = import_pieces_framework3.Property.Dropdown({
  auth: cognitoFormsAuth,
  displayName: "Form",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => auth ? {
    disabled: !1,
    options: (await makeRequest(auth, import_pieces_common3.HttpMethod.GET, "/forms")).map((form) => ({
      label: form.Name,
      value: form.Id
    }))
  } : {
    disabled: !0,
    placeholder: "Please connect your Cognito Forms account",
    options: []
  }
}), formFields = import_pieces_framework3.Property.DynamicProperties({
  displayName: "Fields",
  auth: cognitoFormsAuth,
  refreshers: ["formId"],
  required: !0,
  props: async ({ auth, formId }) => {
    if (!auth || !formId) return {};
    let fields = await makeRequest(
      auth,
      import_pieces_common3.HttpMethod.GET,
      `/forms/${formId}/schema`
    ), props = {};
    for (let [key, value] of Object.entries(fields.properties)) {
      let fieldType = value.type, fieldName = key, fieldDes = value.description;
      if (!value.readOnly)
        switch (fieldType) {
          case "string":
            props[fieldName] = import_pieces_framework3.Property.ShortText({
              displayName: fieldName,
              description: fieldDes != null ? fieldDes : "",
              required: !1
            });
            break;
          case "boolean":
            props[fieldName] = import_pieces_framework3.Property.Checkbox({
              displayName: fieldName,
              description: fieldDes != null ? fieldDes : "",
              required: !1
            });
            break;
          case "number":
            props[fieldName] = import_pieces_framework3.Property.Number({
              displayName: fieldName,
              description: fieldDes != null ? fieldDes : "",
              required: !1
            });
            break;
          default:
            break;
        }
    }
    return props;
  }
});

// ../ap/packages/pieces/community/cognito-forms/src/lib/actions/create-entry.ts
var createEntryAction = (0, import_pieces_framework4.createAction)({
  auth: cognitoFormsAuth,
  name: "create_entry",
  displayName: "Create Entry",
  description: "Creates a new entry.",
  audience: "both",
  aiMetadata: { description: "Creates a new entry (form submission) in a specified Cognito Forms form. Choose this to submit data into a form programmatically; the entry field values must match the target form's schema. Not idempotent: each call creates a separate entry.", idempotent: !1 },
  props: {
    formId: formIdDropdown,
    entryData: formFields
  },
  async run(context) {
    let apiKey = context.auth, { formId, entryData } = context.propsValue;
    return await makeRequest(
      apiKey,
      import_pieces_common4.HttpMethod.POST,
      `/forms/${formId}/entries`,
      entryData
    );
  }
});

// ../ap/packages/pieces/community/cognito-forms/src/lib/actions/update-entry.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateEntryAction = (0, import_pieces_framework5.createAction)({
  auth: cognitoFormsAuth,
  name: "update_entry",
  displayName: "Update Entry",
  description: "Update an existing entry.",
  audience: "both",
  aiMetadata: { description: "Updates an existing entry in a Cognito Forms form, identified by its entry ID, applying the provided field values. Choose this when you already have the entry ID and want to modify its data rather than create a new one. Idempotent: repeating with the same field values leaves the entry in the same state.", idempotent: !0 },
  props: {
    formId: formIdDropdown,
    entryId: import_pieces_framework5.Property.ShortText({
      displayName: "Entry ID",
      required: !0,
      description: "Enter the ID of the entry you want to update."
    }),
    entryData: formFields
  },
  async run(context) {
    let apiKey = context.auth, { formId, entryId, entryData } = context.propsValue;
    return await makeRequest(
      apiKey,
      import_pieces_common5.HttpMethod.PATCH,
      `/forms/${formId}/entries/${entryId}`,
      entryData
    );
  }
});

// ../ap/packages/pieces/community/cognito-forms/src/lib/actions/delete-entry.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var deleteEntryAction = (0, import_pieces_framework6.createAction)({
  auth: cognitoFormsAuth,
  name: "delete_entry",
  displayName: "Delete Entry",
  description: "Deletes a specified entry.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a single entry from a Cognito Forms form, identified by its entry ID. Choose this to remove a specific submission you can identify by ID. Idempotent: once the entry is gone, repeating the call converges to the same deleted state.", idempotent: !0 },
  props: {
    formId: formIdDropdown,
    entryId: import_pieces_framework6.Property.ShortText({
      displayName: "Entry ID",
      required: !0,
      description: "Enter the ID of the entry to delete."
    })
  },
  async run(context) {
    let apiKey = context.auth, { formId, entryId } = context.propsValue;
    return await makeRequest(
      apiKey,
      import_pieces_common6.HttpMethod.DELETE,
      `/forms/${formId}/entries/${entryId}`
    );
  }
});

// ../ap/packages/pieces/community/cognito-forms/src/lib/actions/get-entry.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var getEntryAction = (0, import_pieces_framework7.createAction)({
  auth: cognitoFormsAuth,
  name: "get_entry",
  displayName: "Get Entry",
  description: "Gets a specified entry.",
  audience: "both",
  aiMetadata: { description: "Retrieves a single entry from a Cognito Forms form by its entry ID. Choose this to read back the field values of one known submission. Read-only and idempotent; requires both the form ID and the entry ID.", idempotent: !0 },
  props: {
    formId: formIdDropdown,
    entryId: import_pieces_framework7.Property.ShortText({
      displayName: "Entry ID",
      required: !0,
      description: "Enter the ID of the entry to retrieve."
    })
  },
  async run(context) {
    let apiKey = context.auth, { formId, entryId } = context.propsValue;
    return await makeRequest(
      apiKey,
      import_pieces_common7.HttpMethod.GET,
      `/forms/${formId}/entries/${entryId}`
    );
  }
});

// ../ap/packages/pieces/community/cognito-forms/src/lib/triggers/new-entry-submitted.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var newEntryTrigger = (0, import_pieces_framework8.createTrigger)({
  name: "new_entry",
  displayName: "New Entry",
  description: "Triggers when a new form entry is submitted.",
  aiMetadata: {
    description: "Fires when a new entry is submitted to a Cognito Forms form, delivering the submitted entry data. Requires the form to be configured to post JSON to the webhook URL on entry submission."
  },
  auth: cognitoFormsAuth,
  props: {
    webhookInstructions: import_pieces_framework8.Property.MarkDown({
      value: `
			To use this trigger, you need to manually set up a webhook in your Cognito Forms account:

			1. Login to your Cognito Forms account.
			2. Select desired form and go to Form Settings.
			3. Enable **Post JSON Data to Website** and add following URL in **Submit Entry Endpoint** field:
			\`\`\`text
			{{webhookUrl}}
			\`\`\`
			4. Click Save to save the form changes.
			`
    })
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  sampleData: void 0,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/cognito-forms/src/lib/triggers/entry-updated.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var entryUpdatedTrigger = (0, import_pieces_framework9.createTrigger)({
  name: "entry_updated",
  displayName: "Entry Updated",
  description: "Triggers when an existing form entry is updated.",
  aiMetadata: {
    description: "Fires when an existing entry in a Cognito Forms form is updated, delivering the updated entry data. Requires the form to be configured to post JSON to the webhook URL on entry update."
  },
  auth: cognitoFormsAuth,
  props: {
    webhookInstructions: import_pieces_framework9.Property.MarkDown({
      value: `
          To use this trigger, you need to manually set up a webhook in your Cognito Forms account:
    
          1. Login to your Cognito Forms account.
          2. Select desired form and go to Form Settings.
          3. Enable **Post JSON Data to Website** and add following URL in **Update Entry Endpoint** field:
          \`\`\`text
          {{webhookUrl}}
          \`\`\`
          4. Click Save to save the form changes.
          `
    })
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  sampleData: void 0,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/cognito-forms/src/index.ts
var import_pieces_common8 = require("@activepieces/pieces-common");
var cognitoForms = (0, import_pieces_framework10.createPiece)({
  displayName: "Cognito Forms",
  auth: cognitoFormsAuth,
  logoUrl: "https://cdn.activepieces.com/pieces/cognito-forms.png",
  authors: ["krushnarout"],
  categories: [import_pieces_framework11.PieceCategory.PRODUCTIVITY, import_pieces_framework11.PieceCategory.FORMS_AND_SURVEYS],
  actions: [
    createEntryAction,
    updateEntryAction,
    deleteEntryAction,
    getEntryAction,
    (0, import_pieces_common8.createCustomApiCallAction)({
      auth: cognitoFormsAuth,
      baseUrl: () => "https://www.cognitoforms.com/api",
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth}`
      })
    })
  ],
  triggers: [newEntryTrigger, entryUpdatedTrigger]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  cognitoForms
});
