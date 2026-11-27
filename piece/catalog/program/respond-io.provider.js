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
var __objRest = (source, exclude) => {
  var target = {};
  for (var prop in source)
    __hasOwnProp.call(source, prop) && exclude.indexOf(prop) < 0 && (target[prop] = source[prop]);
  if (source != null && __getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(source))
      exclude.indexOf(prop) < 0 && __propIsEnum.call(source, prop) && (target[prop] = source[prop]);
  return target;
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

// ../ap/packages/pieces/community/respond-io/src/index.ts
var index_exports = {};
__export(index_exports, {
  respondIo: () => respondIo
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework19 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/respond-io/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/respond-io/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
async function respondIoApiCall({
  method,
  url,
  body,
  auth
}) {
  var _a, _b, _c;
  let request = {
    method,
    url: `https://api.respond.io/v2${url}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token: auth.props.token
    },
    headers: {
      "Content-Type": "application/json",
      accept: "application/json"
    },
    body
  };
  try {
    return (await import_pieces_common.httpClient.sendRequest(request)).body;
  } catch (error) {
    let err = error, status = (_a = err.response) == null ? void 0 : _a.status, apiMessage = ((_c = (_b = err.response) == null ? void 0 : _b.body) == null ? void 0 : _c.message) || "No details provided";
    switch (status) {
      case 400:
        throw new Error(
          `Bad Request: The request format is incorrect or missing required data. Please check your input values. Details: ${apiMessage}`
        );
      case 401:
        throw new Error(
          `Authentication Error: Your API token is invalid or expired. Please check your Respond.io API token in the connection settings. Details: ${apiMessage}`
        );
      case 403:
        throw new Error(
          `Access Forbidden: Your API token doesn't have permission for this operation. Please check your Respond.io account permissions. Details: ${apiMessage}`
        );
      case 404:
        throw new Error(
          `Resource Not Found: The requested contact, conversation, or resource doesn't exist in your Respond.io workspace. Details: ${apiMessage}`
        );
      case 409:
        throw new Error(
          `Conflict: The resource already exists or there's a data conflict. This often happens when trying to create a contact with an existing identifier. Details: ${apiMessage}`
        );
      case 429:
        throw new Error(
          `Rate Limit Exceeded: Too many requests to Respond.io API. Please wait a moment before trying again. Details: ${apiMessage}`
        );
      case 449:
        throw new Error(
          `Request Failed: The request could not be completed. This may be a temporary issue with Respond.io services. Please try again later. Details: ${apiMessage}`
        );
      case 500:
        throw new Error(
          `Server Error: Respond.io is experiencing technical difficulties. Please try again later or contact Respond.io support. Details: ${apiMessage}`
        );
      default: {
        let fallbackMessage = err.message || "Unknown error occurred";
        throw new Error(
          `Respond.io API Error (Status ${status || "Unknown"}): ${apiMessage || fallbackMessage}`
        );
      }
    }
  }
}

// ../ap/packages/pieces/community/respond-io/src/lib/common/auth.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework"), RespondIoAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  description: "Your Respond.io API v2 Token.",
  required: !0
}), respondIoAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: `
  Please follow these steps to get your Respond.io API token:
  
  1. Log in to your Respond.io account.
  2. In the left sidebar, navigate to Settings.
  3. Under Workspace Settings, click on Integrations.
  4. Locate and click on the Developer API option.
  5. Within the Developer API section, find your API key or generate a new one if needed.`,
  props: {
    token: RespondIoAuth
  },
  validate: async ({ auth }) => {
    try {
      return await respondIoApiCall({
        method: import_pieces_common2.HttpMethod.GET,
        url: "/space/user",
        auth: {
          type: import_pieces_framework2.AppConnectionType.CUSTOM_AUTH,
          props: auth
        }
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API Token"
      };
    }
  },
  required: !0
});

// ../ap/packages/pieces/community/respond-io/src/lib/actions/add-comment-to-conversation.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/respond-io/src/lib/common/props.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var contactIdentifierDropdown = import_pieces_framework3.Property.Dropdown({
  auth: respondIoAuth,
  displayName: "Contact",
  description: "Select the contact.",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Connect your Respond.io account first"
      };
    try {
      let contacts = (await respondIoApiCall({
        auth,
        method: import_pieces_common3.HttpMethod.POST,
        url: "/contact/list",
        body: {
          search: "",
          filter: {
            $and: []
          },
          timezone: "UTC"
        }
      })).items;
      return contacts.length === 0 ? {
        disabled: !0,
        options: [],
        placeholder: "No contacts found in your workspace."
      } : {
        disabled: !1,
        options: contacts.map((contact) => {
          let contactInfo = contact.email || contact.phone || `ID: ${contact.id}`;
          return {
            label: `${contact.firstName || ""} ${contact.lastName || ""} (${contactInfo})`.trim(),
            value: `id:${contact.id}`
          };
        })
      };
    } catch (error) {
      let errorMessage = error instanceof Error ? error.message : "Unknown error occurred";
      return {
        disabled: !0,
        options: [],
        placeholder: `Error loading contacts: ${errorMessage}`
      };
    }
  }
}), assigneeDropdown = import_pieces_framework3.Property.Dropdown({
  auth: respondIoAuth,
  displayName: "Assignee",
  description: "Select the user to assign the conversation to.",
  required: !0,
  refreshers: ["auth"],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Connect your Respond.io account first"
      };
    try {
      let users = (await respondIoApiCall({
        auth,
        method: import_pieces_common3.HttpMethod.GET,
        url: "/space/user"
      })).items;
      return users.length === 0 ? {
        disabled: !0,
        options: [],
        placeholder: "No users found in your workspace."
      } : {
        disabled: !1,
        options: [
          {
            label: "Unassigned",
            value: "null"
            // MUST be string "null"
          },
          ...users.map((user) => ({
            label: `${user.firstName} ${user.lastName} (${user.email})`,
            value: user.id.toString()
            // always string
          }))
        ]
      };
    } catch (error) {
      let errorMessage = error instanceof Error ? error.message : "Unknown error occurred";
      return {
        disabled: !0,
        options: [],
        placeholder: `Error loading users: ${errorMessage}`
      };
    }
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/actions/add-comment-to-conversation.ts
var addCommentToConversation = (0, import_pieces_framework4.createAction)({
  auth: respondIoAuth,
  name: "add_comment_to_conversation",
  displayName: "Add Comment to Conversation",
  description: "Add an internal comment/note to a conversation in Respond.io.",
  audience: "both",
  aiMetadata: { description: "Posts an internal comment (note visible to team members, not the contact) on a contact's conversation in Respond.io. Use to leave context for agents rather than to message the contact. Requires the contact identifier and comment text (max 1000 chars); supports @-mentioning users via {{@user.ID}}. Not idempotent \u2014 each call appends another comment.", idempotent: !1 },
  props: {
    identifier: contactIdentifierDropdown,
    text: import_pieces_framework4.Property.LongText({
      displayName: "Comment Text",
      description: "The internal comment to add (max 1000 characters). You can mention users with {{@user.ID}} format where ID is the user ID.",
      required: !0
    })
  },
  async run({ propsValue, auth }) {
    let { identifier, text } = propsValue;
    if (!text || text.trim().length === 0)
      throw new Error("Comment text is required and cannot be empty.");
    if (text.length > 1e3)
      throw new Error(`Comment text is too long (${text.length} characters). Maximum allowed is 1000 characters.`);
    return await respondIoApiCall({
      method: import_pieces_common4.HttpMethod.POST,
      url: `/contact/${identifier}/comment`,
      auth,
      body: {
        text: text.trim()
      }
    });
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/actions/add-tag-to-contact.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var addTagToContact = (0, import_pieces_framework5.createAction)({
  auth: respondIoAuth,
  name: "add_tag_to_contact",
  displayName: "Add Tag to Contact",
  description: "Add one or multiple tags to a contact in Respond.io.",
  audience: "both",
  aiMetadata: { description: "Adds one or more tags to a Respond.io contact, identified by the contact identifier. Use to label or segment a contact. Accepts 1-10 tag names per call. Idempotent on the resulting tag set \u2014 re-adding a tag the contact already has leaves it unchanged.", idempotent: !0 },
  props: {
    identifier: contactIdentifierDropdown,
    tags: import_pieces_framework5.Property.Array({
      displayName: "Tags",
      description: "Tags to add to the contact (minimum 1, maximum 10 tags).",
      required: !0,
      properties: {
        tag: import_pieces_framework5.Property.ShortText({
          displayName: "Tag Name",
          description: "Name of the tag to add.",
          required: !0
        })
      }
    })
  },
  async run({ propsValue, auth }) {
    let { identifier, tags } = propsValue;
    if (!tags || !Array.isArray(tags) || tags.length === 0)
      throw new Error("At least one tag is required.");
    if (tags.length > 10)
      throw new Error("Maximum of 10 tags allowed per request.");
    let tagNames = tags.map((tagObj) => tagObj.tag).filter((tag) => tag && tag.trim()).map((tag) => tag.trim());
    if (tagNames.length === 0)
      throw new Error("At least one valid tag name is required.");
    return await respondIoApiCall({
      method: import_pieces_common5.HttpMethod.POST,
      url: `/contact/${identifier}/tag`,
      auth,
      body: tagNames
    });
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/actions/assign-or-unassign-conversation.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var assignOrUnassignConversation = (0, import_pieces_framework6.createAction)({
  auth: respondIoAuth,
  name: "assign_or_unassign_conversation",
  displayName: "Assign or Unassign Conversation",
  description: "Assign or unassign a conversation to/from a team member in Respond.io.",
  audience: "both",
  aiMetadata: { description: "Sets (or clears) the assignee on a contact's conversation in Respond.io. Two modes: provide a user to assign, or leave the assignee empty to unassign. Use to route a conversation to a specific agent or remove ownership. Idempotent \u2014 re-applying the same assignee leaves the conversation in the same state.", idempotent: !0 },
  props: {
    identifier: contactIdentifierDropdown,
    assignee: __spreadProps(__spreadValues({}, assigneeDropdown), {
      // Use the new dropdown
      description: "Select the user to assign the conversation to. Leave empty to unassign.",
      required: !1
      // Make it optional to allow unassigning
    })
  },
  async run({ propsValue, auth }) {
    let { identifier, assignee } = propsValue, assigneeValue = null;
    if (assignee && assignee.trim() !== "" && assignee !== "null") {
      let userId = parseInt(assignee, 10);
      isNaN(userId) ? assigneeValue = assignee : assigneeValue = userId;
    }
    return await respondIoApiCall({
      method: import_pieces_common6.HttpMethod.POST,
      url: `/contact/${identifier}/conversation/assignee`,
      auth,
      body: {
        assignee: assigneeValue
      }
    });
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/actions/create-contact.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var createContact = (0, import_pieces_framework7.createAction)({
  auth: respondIoAuth,
  name: "create_contact",
  displayName: "Create Contact",
  description: "Create a new contact record in Respond.io.",
  audience: "both",
  aiMetadata: { description: "Creates a new contact in Respond.io, keyed by a unique identifier in `email:...` or `phone:...` form. Use only when adding a brand-new contact; if the contact may already exist, prefer Create or Update Contact, which upserts. Requires the identifier and first name; optional email, phone, language, country, profile picture, and custom fields. Not idempotent \u2014 intended to create a fresh record.", idempotent: !1 },
  props: {
    identifier: import_pieces_framework7.Property.ShortText({
      displayName: "Identifier",
      description: "Must be a unique identifier for the new contact. Use format: `email:user@example.com` or `phone:+60123456789`.",
      required: !0
    }),
    firstName: import_pieces_framework7.Property.ShortText({
      displayName: "First Name",
      required: !0
    }),
    lastName: import_pieces_framework7.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    email: import_pieces_framework7.Property.ShortText({
      displayName: "Email",
      description: "The email address of the contact.",
      required: !1
    }),
    phone: import_pieces_framework7.Property.ShortText({
      displayName: "Phone",
      description: "The phone number of the contact (in E.164 format).",
      required: !1
    }),
    language: import_pieces_framework7.Property.ShortText({
      displayName: "Language",
      description: 'Language code (e.g., "en", "es") following ISO 639-1.',
      required: !1
    }),
    profilePic: import_pieces_framework7.Property.ShortText({
      displayName: "Profile Picture URL",
      description: "A direct URL to the contact's profile picture.",
      required: !1
    }),
    countryCode: import_pieces_framework7.Property.ShortText({
      displayName: "Country Code",
      description: 'Country code (e.g., "US", "MY") following ISO 3166-1 alpha-2.',
      required: !1
    }),
    customFields: import_pieces_framework7.Property.Array({
      displayName: "Custom Fields",
      description: "Add custom fields for the contact.",
      required: !1,
      properties: {
        name: import_pieces_framework7.Property.ShortText({
          displayName: "Field Name",
          description: 'Name of the custom field (e.g., "Company Website", "Department").',
          required: !0
        }),
        value: import_pieces_framework7.Property.ShortText({
          displayName: "Field Value",
          description: `Value of the custom field. Format based on field type:
- Text: "string" (use quotation marks)
- Number: 123 (without quotation marks)
- Email: "user@domain.com"
- URL: "https://example.com"
- Date: "yyyy-mm-dd"
- Time: "HH:MM" (24H format)
- Checkbox: "true" or "false"`,
          required: !1
        })
      }
    })
  },
  async run({ propsValue, auth }) {
    let _a = propsValue, { identifier, customFields } = _a, contactData = __objRest(_a, ["identifier", "customFields"]);
    if (contactData.email && contactData.email.trim() && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(contactData.email))
      throw new Error("Invalid email format. Please provide a valid email address.");
    if (contactData.phone && contactData.phone.trim() && !/^\+[1-9]\d{1,14}$/.test(contactData.phone))
      throw new Error("Invalid phone format. Please provide phone number in E.164 format (e.g., +60123456789).");
    if (contactData.profilePic && contactData.profilePic.trim() && !/^https?:\/\/[^\s$.?#].[^\s]*$/.test(contactData.profilePic))
      throw new Error("Invalid profile picture URL format. Please provide a valid HTTP/HTTPS URL.");
    if (contactData.language && contactData.language.trim() && !/^[a-z]{2}$/.test(contactData.language))
      throw new Error('Invalid language code. Please provide a valid ISO 639-1 two-letter language code (e.g., "en", "es").');
    if (contactData.countryCode && contactData.countryCode.trim() && !/^[A-Z]{2}$/.test(contactData.countryCode))
      throw new Error('Invalid country code. Please provide a valid ISO 3166-1 alpha-2 two-letter country code (e.g., "US", "MY").');
    let body = {};
    for (let [key, value] of Object.entries(contactData))
      value != null && (body[key] = value);
    return customFields && Array.isArray(customFields) && customFields.length > 0 && (body.custom_fields = customFields.filter((field) => field.name && field.name.trim()).map((field) => ({
      name: field.name,
      value: field.value || null
    }))), await respondIoApiCall({
      method: import_pieces_common7.HttpMethod.POST,
      url: `/contact/${identifier}`,
      auth,
      body
    });
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/actions/create-or-update-contact.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var createOrUpdateContact = (0, import_pieces_framework8.createAction)({
  auth: respondIoAuth,
  name: "create_or_update_contact",
  displayName: "Create or Update Contact",
  description: "Create a new contact or update an existing one by email, phone, or ID.",
  audience: "both",
  aiMetadata: { description: "Upserts a Respond.io contact keyed by a unique identifier in `id:...`, `email:...`, or `phone:...` form: updates the matching contact if it exists, otherwise creates one. Prefer this over Create Contact when the contact may already exist. Requires the identifier and first name; optional email, phone, language, country, profile picture, and custom fields. Idempotent \u2014 repeating with the same input converges to the same record.", idempotent: !0 },
  props: {
    identifier: import_pieces_framework8.Property.ShortText({
      displayName: "Identifier",
      description: "Identifier for the contact. Use format: `id:123`, `email:user@example.com`, or `phone:+60123456789`. If contact exists, it will be updated; if not, a new one will be created.",
      required: !0
    }),
    firstName: import_pieces_framework8.Property.ShortText({
      displayName: "First Name",
      required: !0
    }),
    lastName: import_pieces_framework8.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    email: import_pieces_framework8.Property.ShortText({
      displayName: "Email",
      description: "The email address of the contact.",
      required: !1
    }),
    phone: import_pieces_framework8.Property.ShortText({
      displayName: "Phone",
      description: "The phone number of the contact (in E.164 format).",
      required: !1
    }),
    language: import_pieces_framework8.Property.ShortText({
      displayName: "Language",
      description: 'Language code (e.g., "en", "es") following ISO 639-1.',
      required: !1
    }),
    profilePic: import_pieces_framework8.Property.ShortText({
      displayName: "Profile Picture URL",
      description: "A direct URL to the contact's profile picture.",
      required: !1
    }),
    countryCode: import_pieces_framework8.Property.ShortText({
      displayName: "Country Code",
      description: 'Country code (e.g., "US", "MY") following ISO 3166-1 alpha-2.',
      required: !1
    }),
    customFields: import_pieces_framework8.Property.Array({
      displayName: "Custom Fields",
      description: "Add custom fields for the contact.",
      required: !1,
      properties: {
        name: import_pieces_framework8.Property.ShortText({
          displayName: "Field Name",
          description: 'Name of the custom field (e.g., "Company Website", "Department").',
          required: !0
        }),
        value: import_pieces_framework8.Property.ShortText({
          displayName: "Field Value",
          description: `Value of the custom field. Format based on field type:
- Text: "string" (use quotation marks)
- Number: 123 (without quotation marks)
- Email: "user@domain.com"
- URL: "https://example.com"
- Date: "yyyy-mm-dd"
- Time: "HH:MM" (24H format)
- Checkbox: "true" or "false"`,
          required: !1
        })
      }
    })
  },
  async run({ propsValue, auth }) {
    let _a = propsValue, { identifier, customFields } = _a, contactData = __objRest(_a, ["identifier", "customFields"]);
    if (contactData.email && contactData.email.trim() && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(contactData.email))
      throw new Error("Invalid email format. Please provide a valid email address.");
    if (contactData.phone && contactData.phone.trim() && !/^\+[1-9]\d{1,14}$/.test(contactData.phone))
      throw new Error("Invalid phone format. Please provide phone number in E.164 format (e.g., +60123456789).");
    if (contactData.profilePic && contactData.profilePic.trim() && !/^https?:\/\/[^\s$.?#].[^\s]*$/.test(contactData.profilePic))
      throw new Error("Invalid profile picture URL format. Please provide a valid HTTP/HTTPS URL.");
    if (contactData.language && contactData.language.trim() && !/^[a-z]{2}$/.test(contactData.language))
      throw new Error('Invalid language code. Please provide a valid ISO 639-1 two-letter language code (e.g., "en", "es").');
    if (contactData.countryCode && contactData.countryCode.trim() && !/^[A-Z]{2}$/.test(contactData.countryCode))
      throw new Error('Invalid country code. Please provide a valid ISO 3166-1 alpha-2 two-letter country code (e.g., "US", "MY").');
    let body = {};
    for (let [key, value] of Object.entries(contactData))
      value != null && (body[key] = value);
    return customFields && Array.isArray(customFields) && customFields.length > 0 && (body.custom_fields = customFields.filter((field) => field.name && field.name.trim()).map((field) => ({
      name: field.name,
      value: field.value || null
    }))), await respondIoApiCall({
      method: import_pieces_common8.HttpMethod.POST,
      url: `/contact/create_or_update/${identifier}`,
      auth,
      body
    });
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/actions/delete-contact.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var deleteContact = (0, import_pieces_framework9.createAction)({
  auth: respondIoAuth,
  name: "delete_contact",
  displayName: "Delete Contact",
  description: "Permanently delete a contact from Respond.io.",
  audience: "both",
  aiMetadata: { description: "Permanently deletes a contact from Respond.io, identified by the contact identifier. Destructive and irreversible \u2014 use only when the contact should be fully removed. Idempotent on end state: once deleted, repeating leaves the contact absent.", idempotent: !0 },
  props: {
    identifier: contactIdentifierDropdown
  },
  async run({ propsValue, auth }) {
    let { identifier } = propsValue;
    return await respondIoApiCall({
      method: import_pieces_common9.HttpMethod.DELETE,
      url: `/contact/${identifier}`,
      auth
    });
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/actions/find-contact.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework");
var findContact = (0, import_pieces_framework10.createAction)({
  auth: respondIoAuth,
  name: "find_contact",
  displayName: "Find Contact",
  description: "Search for contacts by name, email, or phone number in Respond.io.",
  audience: "both",
  aiMetadata: { description: "Searches Respond.io contacts by a free-text term matching name, email, or phone, returning matching contacts. Use to resolve a contact to its ID before acting on it, or to look up contacts. Supports a result limit (1-99, default 10) and cursor-based pagination. Read-only and idempotent.", idempotent: !0 },
  props: {
    search: import_pieces_framework10.Property.ShortText({
      displayName: "Search Term",
      description: "The value to search for (e.g., email address, phone number, or name).",
      required: !0
    }),
    timezone: import_pieces_framework10.Property.ShortText({
      displayName: "Timezone",
      description: 'Timezone for the search (e.g., "Asia/Kuala_Lumpur"). Optional.',
      required: !1
    }),
    limit: import_pieces_framework10.Property.Number({
      displayName: "Result Limit",
      description: "Number of contacts to return (1-99). Default is 10.",
      required: !1,
      defaultValue: 10
    }),
    cursorId: import_pieces_framework10.Property.Number({
      displayName: "Cursor ID",
      description: "Contact ID to start from for pagination. Optional.",
      required: !1
    })
  },
  async run({ propsValue, auth }) {
    let { search, timezone, limit = 10, cursorId } = propsValue;
    if (limit < 1 || limit >= 100)
      throw new Error("Limit must be between 1 and 99.");
    let queryParams = [`limit=${limit}`];
    cursorId && queryParams.push(`cursorId=${cursorId}`);
    let body = {
      search,
      filter: { $and: [] }
    };
    return timezone && (body.timezone = timezone), await respondIoApiCall({
      method: import_pieces_common10.HttpMethod.POST,
      url: `/contact/list?${queryParams.join("&")}`,
      auth,
      body
    });
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/actions/open-conversation.ts
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var openConversation = (0, import_pieces_framework11.createAction)({
  auth: respondIoAuth,
  name: "open_conversation",
  displayName: "Open Conversation",
  description: "Mark a conversation as open in Respond.io.",
  audience: "both",
  aiMetadata: { description: "Sets a contact's conversation status to open in Respond.io, identified by the contact identifier. Use to (re)open a conversation so it appears in active queues. Idempotent \u2014 opening an already-open conversation leaves it open.", idempotent: !0 },
  props: {
    identifier: contactIdentifierDropdown
  },
  async run({ propsValue, auth }) {
    let { identifier } = propsValue;
    return await respondIoApiCall({
      method: import_pieces_common11.HttpMethod.POST,
      url: `/contact/${identifier}/conversation/status`,
      auth,
      body: {
        status: "open"
      }
    });
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/triggers/contact-tag-updated.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var contactTagUpdatedTrigger = (0, import_pieces_framework12.createTrigger)({
  name: "contact_tag_updated",
  displayName: "Contact Tag Updated",
  description: "Triggers when a tag is added or removed from a contact in Respond.io.",
  aiMetadata: {
    description: "Fires when a contact's tags change \u2014 a tag is added to or removed from a contact in Respond.io. Delivers the affected contact and its updated tag set. Requires manually configuring a contact.tag.updated webhook in Respond.io."
  },
  auth: respondIoAuth,
  props: {
    webhookInstructions: import_pieces_framework12.Property.MarkDown({
      value: `
			To use this trigger, you need to manually set up a webhook in your Respond.io account:

			1. Login to your Respond.io account.
			2. Go to Settings > Integrations > Webhooks.
			3. Click on "Add Webhook" or "Create New Webhook".
			4. Add the following URL in the **Webhook URL** field:
			\`\`\`text
			{{webhookUrl}}
			\`\`\`
			5. Select **contact.tag.updated** from the event types.
			6. Click Save to create the webhook.
			`
    })
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  sampleData: void 0,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/triggers/contact-updated.ts
var import_pieces_framework13 = require("@activepieces/pieces-framework");
var contactUpdatedTrigger = (0, import_pieces_framework13.createTrigger)({
  name: "contact_updated",
  displayName: "Contact Updated",
  description: "Triggers when an existing contact is updated in Respond.io.",
  aiMetadata: {
    description: "Fires when an existing Respond.io contact's details change (e.g. name, email, phone, custom fields), delivering the updated contact. Does not fire on contact creation \u2014 use the New Contact trigger for that. Requires manually configuring a contact.updated webhook in Respond.io."
  },
  auth: respondIoAuth,
  props: {
    webhookInstructions: import_pieces_framework13.Property.MarkDown({
      value: `
			To use this trigger, you need to manually set up a webhook in your Respond.io account:

			1. Login to your Respond.io account.
			2. Go to Settings > Integrations > Webhooks.
			3. Click on "Add Webhook" or "Create New Webhook".
			4. Add the following URL in the **Webhook URL** field:
			\`\`\`text
			{{webhookUrl}}
			\`\`\`
			5. Select **contact.updated** from the event types.
			6. Click Save to create the webhook.
			`
    })
  },
  type: import_pieces_framework13.TriggerStrategy.WEBHOOK,
  sampleData: void 0,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/triggers/conversation-closed.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework");
var conversationClosedTrigger = (0, import_pieces_framework14.createTrigger)({
  name: "conversation_closed",
  displayName: "Conversation Closed",
  description: "Triggers when a conversation is closed.",
  aiMetadata: {
    description: "Fires when a conversation is closed in Respond.io, delivering the contact and conversation that was closed. Use to run follow-up actions after a conversation is resolved. Requires manually configuring a conversation.closed webhook in Respond.io."
  },
  auth: respondIoAuth,
  props: {
    webhookInstructions: import_pieces_framework14.Property.MarkDown({
      value: `
			To use this trigger, you need to manually set up a webhook in your Respond.io account:

			1. Login to your Respond.io account.
			2. Go to Settings > Integrations > Webhooks.
			3. Click on "Add Webhook" or "Create New Webhook".
			4. Add the following URL in the **Webhook URL** field:
			\`\`\`text
			{{webhookUrl}}
			\`\`\`
			5. Select **conversation.closed** from the event types.
			6. Click Save to create the webhook.
			`
    })
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  sampleData: void 0,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/triggers/conversation-opened.ts
var import_pieces_framework15 = require("@activepieces/pieces-framework");
var conversationOpenedTrigger = (0, import_pieces_framework15.createTrigger)({
  auth: respondIoAuth,
  name: "conversation_opened",
  displayName: "Conversation Opened",
  description: "Triggers when a new conversation is opened.",
  aiMetadata: {
    description: "Fires when a conversation is opened in Respond.io, delivering the contact and conversation details (including source and first incoming message). Use to start a workflow when a new conversation begins. Requires manually configuring a conversation.opened webhook in Respond.io."
  },
  type: import_pieces_framework15.TriggerStrategy.WEBHOOK,
  props: {
    webhookInstructions: import_pieces_framework15.Property.MarkDown({
      value: `
          To use this trigger, you need to manually set up a webhook in your Respond.io account:
    
          1. Login to your Respond.io account.
          2. Go to Settings > Integrations > Webhooks.
          3. Click on "Add Webhook" or "Create New Webhook".
          4. Add the following URL in the **Webhook URL** field:
          \`\`\`text
          {{webhookUrl}}
          \`\`\`
          5. Select **conversation.opened** from the event types.
          6. Click Save to create the webhook.
          `
    })
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    let payload = context.payload.body;
    return payload.event_type !== "conversation.opened" ? [] : [payload];
  },
  async test() {
    return [
      {
        event_type: "conversation.opened",
        event_id: "8b9c2905-7a38-4aad-a6b7-b9120dc140fe",
        contact: {
          id: 1,
          firstName: "John",
          lastName: "Doe",
          phone: "+60123456789",
          email: "johndoe@sample.com",
          language: "en",
          profilePic: "https://cdn.chatapi.net/johndoe.png",
          countryCode: "MY",
          status: "open",
          assignee: {
            id: 2,
            firstName: "John",
            lastName: "Doe",
            email: "johndoe@sample.com"
          },
          created_at: 1663274081
        },
        conversation: {
          source: "api",
          conversationOpenedAt: 1663274081,
          firstIncomingMessage: "hello",
          firstIncomingMessageChannelId: 1
        }
      }
    ];
  },
  sampleData: {}
});

// ../ap/packages/pieces/community/respond-io/src/lib/triggers/new-contact.ts
var import_pieces_framework16 = require("@activepieces/pieces-framework");
var newContactTrigger = (0, import_pieces_framework16.createTrigger)({
  name: "new_contact",
  displayName: "New Contact",
  description: "Triggers when a new contact is created in Respond.io.",
  aiMetadata: {
    description: "Fires when a new contact is created in Respond.io, delivering the newly created contact. Use to react to contact onboarding; for changes to existing contacts use the Contact Updated trigger instead. Requires manually configuring a contact.created webhook in Respond.io."
  },
  auth: respondIoAuth,
  props: {
    webhookInstructions: import_pieces_framework16.Property.MarkDown({
      value: `
			To use this trigger, you need to manually set up a webhook in your Respond.io account:

			1. Login to your Respond.io account.
			2. Go to Settings > Integrations > Webhooks.
			3. Click on "Add Webhook" or "Create New Webhook".
			4. Add the following URL in the **Webhook URL** field:
			\`\`\`text
			{{webhookUrl}}
			\`\`\`
			5. Select **contact.created** from the event types.
			6. Click Save to create the webhook.
			`
    })
  },
  type: import_pieces_framework16.TriggerStrategy.WEBHOOK,
  sampleData: void 0,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/triggers/new-incoming-message.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework");
var newIncomingMessageTrigger = (0, import_pieces_framework17.createTrigger)({
  name: "new_incoming_message",
  displayName: "New Incoming Message",
  description: "Triggers when a new message is received from a contact.",
  aiMetadata: {
    description: "Fires when a contact sends an inbound message into Respond.io, delivering the message and its contact. Use to react to incoming customer messages; for messages sent out from Respond.io use the New Outgoing Message trigger. Requires manually configuring a message.received webhook in Respond.io."
  },
  auth: respondIoAuth,
  props: {
    webhookInstructions: import_pieces_framework17.Property.MarkDown({
      value: `
			To use this trigger, you need to manually set up a webhook in your Respond.io account:

			1. Login to your Respond.io account.
			2. Go to Settings > Integrations > Webhooks.
			3. Click on "Add Webhook" or "Create New Webhook".
			4. Add the following URL in the **Webhook URL** field:
			\`\`\`text
			{{webhookUrl}}
			\`\`\`
			5. Select **message.received** from the event types.
			6. Click Save to create the webhook.
			`
    })
  },
  type: import_pieces_framework17.TriggerStrategy.WEBHOOK,
  sampleData: void 0,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/respond-io/src/lib/triggers/new-outgoing-message.ts
var import_pieces_framework18 = require("@activepieces/pieces-framework");
var newOutgoingMessageTrigger = (0, import_pieces_framework18.createTrigger)({
  name: "new_outgoing_message",
  displayName: "New Outgoing Message",
  description: "Triggers when a message is sent from Respond.io.",
  aiMetadata: {
    description: "Fires when a message is sent out from Respond.io to a contact (by an agent, automation, or API), delivering the message and its contact. Use to react to outbound messages; for messages received from contacts use the New Incoming Message trigger. Requires manually configuring a message.sent webhook in Respond.io."
  },
  auth: respondIoAuth,
  props: {
    webhookInstructions: import_pieces_framework18.Property.MarkDown({
      value: `
			To use this trigger, you need to manually set up a webhook in your Respond.io account:

			1. Login to your Respond.io account.
			2. Go to Settings > Integrations > Webhooks.
			3. Click on "Add Webhook" or "Create New Webhook".
			4. Add the following URL in the **Webhook URL** field:
			\`\`\`text
			{{webhookUrl}}
			\`\`\`
			5. Select **message.sent** from the event types.
			6. Click Save to create the webhook.
			`
    })
  },
  type: import_pieces_framework18.TriggerStrategy.WEBHOOK,
  sampleData: void 0,
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/respond-io/src/index.ts
var respondIo = (0, import_pieces_framework19.createPiece)({
  displayName: "Respond.io",
  auth: respondIoAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/respond-io.png",
  authors: ["aryel780"],
  actions: [
    addCommentToConversation,
    addTagToContact,
    assignOrUnassignConversation,
    createContact,
    createOrUpdateContact,
    deleteContact,
    findContact,
    openConversation,
    (0, import_pieces_common12.createCustomApiCallAction)({
      auth: respondIoAuth,
      baseUrl: () => "https://api.respond.io/v2",
      authMapping: async (auth) => {
        let { token } = auth.props;
        return {
          Authorization: `Bearer ${token}`
        };
      }
    })
  ],
  triggers: [
    contactTagUpdatedTrigger,
    contactUpdatedTrigger,
    conversationClosedTrigger,
    conversationOpenedTrigger,
    newContactTrigger,
    newIncomingMessageTrigger,
    newOutgoingMessageTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  respondIo
});
