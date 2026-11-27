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

// ../ap/packages/pieces/community/open-phone/src/index.ts
var index_exports = {};
__export(index_exports, {
  openPhone: () => openPhone
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework12 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/open-phone/src/lib/actions/send-message.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/open-phone/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), openPhoneAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  required: !0,
  description: "Enter your OpenPhone API key. You can generate one from the API tab in your workspace settings."
});

// ../ap/packages/pieces/community/open-phone/src/lib/common/index.ts
var import_pieces_common = require("@activepieces/pieces-common"), openPhoneCommon = {
  baseUrl: "https://api.openphone.com",
  async makeRequest(method, endpoint, auth, body) {
    return (await import_pieces_common.httpClient.sendRequest({
      method,
      url: `${this.baseUrl}${endpoint}`,
      headers: {
        Authorization: auth.secret_text
      },
      body
    })).body;
  }
};

// ../ap/packages/pieces/community/open-phone/src/lib/actions/send-message.ts
var sendMessage = (0, import_pieces_framework2.createAction)({
  auth: openPhoneAuth,
  name: "send_message",
  classification: "WRITE",
  displayName: "Send Message",
  description: "Send SMS/MMS from your OpenPhone number to a recipient",
  audience: "both",
  aiMetadata: { description: "Sends an SMS/MMS text message from one of the workspace's OpenPhone numbers to a recipient. Choose this to programmatically text a contact or phone number. Requires the sending OpenPhone number (E.164 like +15555555555 or a phone-number ID) and exactly one recipient number in E.164 format; content must be 1-1600 characters. Not idempotent \u2014 each call sends a new message.", idempotent: !1 },
  props: {
    content: import_pieces_framework2.Property.LongText({
      displayName: "Message Content",
      description: "The text content of the message to be sent (1-1600 characters)",
      required: !0
    }),
    from: import_pieces_framework2.Property.ShortText({
      displayName: "From Phone Number",
      description: "OpenPhone number to send from (E.164 format: +15555555555) or phone number ID (PN123abc)",
      required: !0
    }),
    to: import_pieces_framework2.Property.Array({
      displayName: "To Phone Numbers",
      description: "Recipient phone numbers in E.164 format (e.g., +15555555555)",
      required: !0,
      properties: {
        phoneNumber: import_pieces_framework2.Property.ShortText({
          displayName: "Phone Number",
          description: "Phone number in E.164 format",
          required: !0
        })
      }
    }),
    userId: import_pieces_framework2.Property.ShortText({
      displayName: "User ID",
      description: "The unique identifier of the OpenPhone user sending the message. If not provided, defaults to the phone number owner.",
      required: !1
    }),
    setInboxStatus: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Inbox Status",
      description: "Set the status of the related OpenPhone inbox conversation",
      required: !1,
      options: {
        options: [
          {
            label: "Done",
            value: "done"
          }
        ]
      }
    })
  },
  async run(context) {
    let { content, from, to, userId, setInboxStatus } = context.propsValue, auth = context.auth, phoneRegex = /^\+[1-9]\d{1,14}$/;
    if (!to || !Array.isArray(to) || to.length === 0)
      throw new Error("At least one phone number is required");
    if (to.length > 1)
      throw new Error("Currently only one recipient phone number is supported");
    let toPhoneNumbers = to;
    for (let phone of toPhoneNumbers)
      if (!phoneRegex.test(phone.phoneNumber))
        throw new Error(
          `Invalid phone number format: ${phone.phoneNumber}. Use E.164 format (e.g., +15555555555)`
        );
    if (!content || !content.trim())
      throw new Error(
        "Message content cannot be empty or contain only whitespace"
      );
    if (content.length > 1600)
      throw new Error("Message content cannot exceed 1600 characters");
    let requestBody = {
      content,
      from,
      to: toPhoneNumbers.map((phone) => phone.phoneNumber)
    };
    userId && (requestBody.userId = userId), setInboxStatus && (requestBody.setInboxStatus = setInboxStatus);
    try {
      return await openPhoneCommon.makeRequest(
        import_pieces_common2.HttpMethod.POST,
        "/v1/messages",
        auth,
        requestBody
      );
    } catch (error) {
      throw new Error(`Failed to send message: ${error}`);
    }
  }
});

// ../ap/packages/pieces/community/open-phone/src/lib/actions/create-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var createContact = (0, import_pieces_framework3.createAction)({
  auth: openPhoneAuth,
  name: "create_contact",
  classification: "WRITE",
  displayName: "Create Contact",
  description: "Create a new contact in your OpenPhone workspace",
  audience: "both",
  aiMetadata: { description: "Creates a new contact in the OpenPhone workspace with optional emails, phone numbers, company, role, and custom fields. Choose this when adding a brand-new person; use Update Contact to modify an existing one. First name is required. Not idempotent \u2014 each call creates a separate contact even with identical input.", idempotent: !1 },
  props: {
    firstName: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "The contact's first name",
      required: !0
    }),
    lastName: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "The contact's last name",
      required: !1
    }),
    company: import_pieces_framework3.Property.ShortText({
      displayName: "Company",
      description: "The contact's company name",
      required: !1
    }),
    role: import_pieces_framework3.Property.ShortText({
      displayName: "Role",
      description: "The contact's role",
      required: !1
    }),
    emails: import_pieces_framework3.Property.Array({
      displayName: "Email Addresses",
      description: "List of email addresses for the contact",
      required: !1,
      properties: {
        name: import_pieces_framework3.Property.ShortText({
          displayName: "Email Label",
          description: 'Label for this email address (e.g., "work", "personal")',
          required: !0
        }),
        value: import_pieces_framework3.Property.ShortText({
          displayName: "Email Address",
          description: "The email address",
          required: !0
        })
      }
    }),
    phoneNumbers: import_pieces_framework3.Property.Array({
      displayName: "Phone Numbers",
      description: "List of phone numbers for the contact",
      required: !1,
      properties: {
        name: import_pieces_framework3.Property.ShortText({
          displayName: "Phone Label",
          description: 'Label for this phone number (e.g., "mobile", "work")',
          required: !0
        }),
        value: import_pieces_framework3.Property.ShortText({
          displayName: "Phone Number",
          description: "The phone number (preferably in E.164 format)",
          required: !0
        })
      }
    }),
    customFields: import_pieces_framework3.Property.Array({
      displayName: "Custom Fields",
      description: "Custom fields for the contact",
      required: !1,
      properties: {
        key: import_pieces_framework3.Property.ShortText({
          displayName: "Field Key",
          description: "The identifying key for the custom field",
          required: !0
        }),
        value: import_pieces_framework3.Property.ShortText({
          displayName: "Field Value",
          description: "The value for the custom field",
          required: !0
        })
      }
    }),
    createdByUserId: import_pieces_framework3.Property.ShortText({
      displayName: "Created By User ID",
      description: "The unique identifier of the user who created the contact (US123abc format)",
      required: !1
    }),
    source: import_pieces_framework3.Property.ShortText({
      displayName: "Source",
      description: `The contact's source (defaults to "public-api")`,
      required: !1,
      defaultValue: "public-api"
    }),
    sourceUrl: import_pieces_framework3.Property.ShortText({
      displayName: "Source URL",
      description: "A link to the contact in the source system",
      required: !1
    }),
    externalId: import_pieces_framework3.Property.ShortText({
      displayName: "External ID",
      description: "A unique identifier from an external system",
      required: !1
    })
  },
  async run(context) {
    let {
      firstName,
      lastName,
      company,
      role,
      emails,
      phoneNumbers,
      customFields,
      createdByUserId,
      source,
      sourceUrl,
      externalId
    } = context.propsValue, auth = context.auth;
    if (!firstName || firstName.trim() === "")
      throw new Error("First name is required and cannot be empty");
    if (emails && Array.isArray(emails)) {
      let emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
      for (let email of emails)
        if (email.value && !emailRegex.test(email.value))
          throw new Error(`Invalid email format: ${email.value}`);
    }
    if (source && (source.length < 1 || source.length > 72))
      throw new Error("Source must be between 1 and 72 characters");
    if (sourceUrl) {
      try {
        new URL(sourceUrl);
      } catch (e) {
        throw new Error("Invalid source URL format");
      }
      if (sourceUrl.length < 1 || sourceUrl.length > 200)
        throw new Error("Source URL must be between 1 and 200 characters");
    }
    if (externalId && (externalId.length < 1 || externalId.length > 75))
      throw new Error("External ID must be between 1 and 75 characters");
    if (createdByUserId && !/^US(.*)$/.test(createdByUserId))
      throw new Error(
        'Created by user ID must start with "US" (e.g., US123abc)'
      );
    let requestBody = {
      defaultFields: {
        firstName: firstName.trim(),
        lastName: lastName ? lastName.trim() : null,
        company: company ? company.trim() : null,
        role: role ? role.trim() : null
      }
    };
    emails && Array.isArray(emails) && emails.length > 0 && (requestBody.defaultFields.emails = emails.map(
      (email) => ({
        name: email.name.trim(),
        value: email.value ? email.value.trim() : null
      })
    )), phoneNumbers && Array.isArray(phoneNumbers) && phoneNumbers.length > 0 && (requestBody.defaultFields.phoneNumbers = phoneNumbers.map(
      (phone) => ({
        name: phone.name.trim(),
        value: phone.value ? phone.value.trim() : null
      })
    )), customFields && Array.isArray(customFields) && customFields.length > 0 && (requestBody.customFields = customFields.map(
      (field) => ({
        key: field.key.trim(),
        value: field.value ? field.value.trim() : null
      })
    )), createdByUserId && (requestBody.createdByUserId = createdByUserId.trim()), source && (requestBody.source = source.trim()), sourceUrl && (requestBody.sourceUrl = sourceUrl.trim()), externalId && (requestBody.externalId = externalId.trim());
    try {
      return await openPhoneCommon.makeRequest(
        import_pieces_common3.HttpMethod.POST,
        "/v1/contacts",
        auth,
        requestBody
      );
    } catch (error) {
      throw new Error(`Failed to create contact: ${error}`);
    }
  }
});

// ../ap/packages/pieces/community/open-phone/src/lib/actions/update-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/open-phone/src/lib/common/props.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var contactDropdown = import_pieces_framework4.Property.Dropdown({
  auth: openPhoneAuth,
  displayName: "Contact",
  description: "Select a contact",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your OpenPhone account first"
      };
    try {
      return {
        disabled: !1,
        options: (await openPhoneCommon.makeRequest(
          import_pieces_common4.HttpMethod.GET,
          "/v1/contacts?maxResults=50",
          auth
        )).data.map((contact) => {
          let displayName = [contact.defaultFields.firstName, contact.defaultFields.lastName].filter(Boolean).join(" ") || "Unnamed Contact", company = contact.defaultFields.company;
          return {
            label: company ? `${displayName} (${company})` : displayName,
            value: contact.id
          };
        }).sort((a, b) => a.label.localeCompare(b.label))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: `Error loading contacts: ${error}`
      };
    }
  }
}), phoneNumberDropdown = import_pieces_framework4.Property.Dropdown({
  auth: openPhoneAuth,
  displayName: "Phone Number",
  description: "Select a phone number to get calls from",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your OpenPhone account first"
      };
    try {
      let phoneNumbersResponse = await openPhoneCommon.makeRequest(
        import_pieces_common4.HttpMethod.GET,
        "/v1/phone-numbers",
        auth
      );
      return !phoneNumbersResponse.data || phoneNumbersResponse.data.length === 0 ? {
        disabled: !0,
        options: [],
        placeholder: "No phone numbers found"
      } : {
        disabled: !1,
        options: phoneNumbersResponse.data.map((phoneNumber) => {
          let displayName = phoneNumber.name || phoneNumber.formattedNumber || phoneNumber.number, numberInfo = phoneNumber.formattedNumber || phoneNumber.number;
          return {
            label: phoneNumber.name ? `${displayName} (${numberInfo})` : displayName,
            value: phoneNumber.id
          };
        }).sort((a, b) => a.label.localeCompare(b.label))
      };
    } catch (error) {
      return {
        disabled: !0,
        options: [],
        placeholder: `Error loading phone numbers: ${error}`
      };
    }
  }
}), participantDropdown = import_pieces_framework4.Property.Dropdown({
  auth: openPhoneAuth,
  displayName: "Participant Phone Number",
  description: "Select a participant phone number to filter calls",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your OpenPhone account first"
      };
    try {
      let contactsResponse = await openPhoneCommon.makeRequest(
        import_pieces_common4.HttpMethod.GET,
        "/v1/contacts?maxResults=50",
        auth
      ), phoneNumbers = /* @__PURE__ */ new Set();
      return contactsResponse.data.forEach((contact) => {
        contact.defaultFields.phoneNumbers && contact.defaultFields.phoneNumbers.forEach((phone) => {
          phone.value && phoneNumbers.add(phone.value);
        });
      }), phoneNumbers.size === 0 ? {
        disabled: !1,
        options: [
          {
            label: "Enter phone number manually (+1234567890)",
            value: "manual"
          }
        ],
        placeholder: "No contact phone numbers found"
      } : {
        disabled: !1,
        options: Array.from(phoneNumbers).map((phoneNumber) => ({
          label: phoneNumber,
          value: phoneNumber
        })).sort((a, b) => a.label.localeCompare(b.label))
      };
    } catch (error) {
      return {
        disabled: !1,
        options: [
          {
            label: "Enter phone number manually (+1234567890)",
            value: "manual"
          }
        ],
        placeholder: `Error loading participants: ${error}`
      };
    }
  }
}), callDropdown = import_pieces_framework4.Property.Dropdown({
  auth: openPhoneAuth,
  displayName: "Call",
  description: "Select a call",
  required: !0,
  refreshers: ["phoneNumberId", "participantNumber"],
  options: async ({ auth, phoneNumberId, participantNumber }) => {
    if (!auth)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please connect your OpenPhone account first"
      };
    if (!phoneNumberId)
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a phone number first"
      };
    if (!participantNumber || participantNumber === "manual")
      return {
        disabled: !0,
        options: [],
        placeholder: "Please select a participant phone number first"
      };
    try {
      let encodedParticipant = encodeURIComponent(
        participantNumber
      ), callsResponse = await openPhoneCommon.makeRequest(
        import_pieces_common4.HttpMethod.GET,
        `/v1/calls?phoneNumberId=${phoneNumberId}&participants=${encodedParticipant}&maxResults=50`,
        auth
      );
      return !callsResponse.data || callsResponse.data.length === 0 ? {
        disabled: !1,
        options: [
          {
            label: "No calls found - Enter Call ID manually",
            value: "manual"
          }
        ],
        placeholder: "No calls available for selection"
      } : {
        disabled: !1,
        options: callsResponse.data.map((call) => {
          let statusInfo = call.status ? ` - ${call.status}` : "", durationInfo = call.duration ? ` (${call.duration}s)` : "", dateInfo = new Date(call.createdAt).toLocaleDateString();
          return {
            label: `${call.direction === "incoming" ? "\u2193" : "\u2191"} ${call.id}${statusInfo}${durationInfo} - ${dateInfo}`,
            value: call.id
          };
        }).sort((a, b) => b.label.localeCompare(a.label))
      };
    } catch (error) {
      return {
        disabled: !1,
        options: [
          {
            label: "Enter Call ID manually (AC...)",
            value: "manual"
          }
        ],
        placeholder: `Error loading calls: ${error}`
      };
    }
  }
});

// ../ap/packages/pieces/community/open-phone/src/lib/actions/update-contact.ts
var updateContact = (0, import_pieces_framework5.createAction)({
  auth: openPhoneAuth,
  name: "update_contact",
  classification: "WRITE",
  displayName: "Update Contact",
  description: "Update an existing contact in your OpenPhone workspace",
  audience: "both",
  aiMetadata: { description: "Updates an existing OpenPhone contact identified by its contact ID, changing any of name, company, role, emails, phone numbers, custom fields, or source. Choose this to modify a contact that already exists rather than create one. Requires a valid contact ID and at least one field to change; supplied email/phone arrays replace the existing ones. Idempotent \u2014 re-applying the same field values leaves the contact in the same state.", idempotent: !0 },
  props: {
    contactId: contactDropdown,
    firstName: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      description: "The contact's first name",
      required: !1
    }),
    lastName: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      description: "The contact's last name",
      required: !1
    }),
    company: import_pieces_framework5.Property.ShortText({
      displayName: "Company",
      description: "The contact's company name",
      required: !1
    }),
    role: import_pieces_framework5.Property.ShortText({
      displayName: "Role",
      description: "The contact's role",
      required: !1
    }),
    emails: import_pieces_framework5.Property.Array({
      displayName: "Email Addresses",
      description: "List of email addresses for the contact (replaces existing emails)",
      required: !1,
      properties: {
        name: import_pieces_framework5.Property.ShortText({
          displayName: "Email Label",
          description: 'Label for this email address (e.g., "work", "personal")',
          required: !0
        }),
        value: import_pieces_framework5.Property.ShortText({
          displayName: "Email Address",
          description: "The email address (set to empty to remove)",
          required: !1
        }),
        id: import_pieces_framework5.Property.ShortText({
          displayName: "Email ID",
          description: "The unique identifier for existing email field (required for updates)",
          required: !1
        })
      }
    }),
    phoneNumbers: import_pieces_framework5.Property.Array({
      displayName: "Phone Numbers",
      description: "List of phone numbers for the contact (replaces existing phone numbers)",
      required: !1,
      properties: {
        name: import_pieces_framework5.Property.ShortText({
          displayName: "Phone Label",
          description: 'Label for this phone number (e.g., "mobile", "work")',
          required: !0
        }),
        value: import_pieces_framework5.Property.ShortText({
          displayName: "Phone Number",
          description: "The phone number (set to empty to remove, preferably in E.164 format)",
          required: !1
        }),
        id: import_pieces_framework5.Property.ShortText({
          displayName: "Phone ID",
          description: "The unique identifier for existing phone number field (required for updates)",
          required: !1
        })
      }
    }),
    customFields: import_pieces_framework5.Property.Array({
      displayName: "Custom Fields",
      description: "Custom fields for the contact",
      required: !1,
      properties: {
        key: import_pieces_framework5.Property.ShortText({
          displayName: "Field Key",
          description: "The identifying key for the custom field",
          required: !0
        }),
        value: import_pieces_framework5.Property.ShortText({
          displayName: "Field Value",
          description: "The value for the custom field (set to empty to remove)",
          required: !1
        }),
        id: import_pieces_framework5.Property.ShortText({
          displayName: "Field ID",
          description: "The unique identifier for the custom field (required for updates)",
          required: !1
        })
      }
    }),
    source: import_pieces_framework5.Property.ShortText({
      displayName: "Source",
      description: "Update the contact's source",
      required: !1
    }),
    sourceUrl: import_pieces_framework5.Property.ShortText({
      displayName: "Source URL",
      description: "Update the link to the contact in the source system",
      required: !1
    }),
    externalId: import_pieces_framework5.Property.ShortText({
      displayName: "External ID",
      description: "Update the unique identifier from an external system",
      required: !1
    })
  },
  async run(context) {
    let {
      contactId,
      firstName,
      lastName,
      company,
      role,
      emails,
      phoneNumbers,
      customFields,
      source,
      sourceUrl,
      externalId
    } = context.propsValue, auth = context.auth;
    if (!contactId)
      throw new Error("Contact ID is required");
    if (emails && Array.isArray(emails)) {
      let emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
      for (let email of emails)
        if (email.value && email.value.trim() && !emailRegex.test(email.value))
          throw new Error(`Invalid email format: ${email.value}`);
    }
    if (source && (source.length < 1 || source.length > 72))
      throw new Error("Source must be between 1 and 72 characters");
    if (sourceUrl && sourceUrl.trim()) {
      try {
        new URL(sourceUrl);
      } catch (e) {
        throw new Error("Invalid source URL format");
      }
      if (sourceUrl.length < 1 || sourceUrl.length > 200)
        throw new Error("Source URL must be between 1 and 200 characters");
    }
    if (externalId && externalId.trim() && (externalId.length < 1 || externalId.length > 75))
      throw new Error("External ID must be between 1 and 75 characters");
    let requestBody = {};
    if ((firstName || lastName || company || role || emails || phoneNumbers) && (requestBody.defaultFields = {}, firstName !== void 0 && (requestBody.defaultFields.firstName = firstName ? firstName.trim() : null), lastName !== void 0 && (requestBody.defaultFields.lastName = lastName ? lastName.trim() : null), company !== void 0 && (requestBody.defaultFields.company = company ? company.trim() : null), role !== void 0 && (requestBody.defaultFields.role = role ? role.trim() : null), emails && Array.isArray(emails) && emails.length > 0 && (requestBody.defaultFields.emails = emails.map(
      (email) => __spreadValues({
        name: email.name.trim(),
        value: email.value && email.value.trim() ? email.value.trim() : null
      }, email.id && { id: email.id.trim() })
    )), phoneNumbers && Array.isArray(phoneNumbers) && phoneNumbers.length > 0 && (requestBody.defaultFields.phoneNumbers = phoneNumbers.map(
      (phone) => __spreadValues({
        name: phone.name.trim(),
        value: phone.value && phone.value.trim() ? phone.value.trim() : null
      }, phone.id && { id: phone.id.trim() })
    ))), customFields && Array.isArray(customFields) && customFields.length > 0 && (requestBody.customFields = customFields.map((field) => __spreadValues({
      key: field.key.trim(),
      value: field.value && field.value.trim() ? field.value.trim() : null
    }, field.id && { id: field.id.trim() }))), source !== void 0 && (requestBody.source = source ? source.trim() : null), sourceUrl !== void 0 && (requestBody.sourceUrl = sourceUrl && sourceUrl.trim() ? sourceUrl.trim() : null), externalId !== void 0 && (requestBody.externalId = externalId && externalId.trim() ? externalId.trim() : null), Object.keys(requestBody).length === 0)
      throw new Error("At least one field must be provided to update");
    try {
      return await openPhoneCommon.makeRequest(
        import_pieces_common5.HttpMethod.PATCH,
        `/v1/contacts/${contactId}`,
        auth,
        requestBody
      );
    } catch (error) {
      throw new Error(`Failed to update contact: ${error}`);
    }
  }
});

// ../ap/packages/pieces/community/open-phone/src/lib/actions/get-call-summary.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var getCallSummary = (0, import_pieces_framework6.createAction)({
  auth: openPhoneAuth,
  name: "get_call_summary",
  classification: "READ",
  displayName: "Get Call Summary",
  description: "Retrieve an AI-generated summary of a specific call by ID. Call summaries are only available on OpenPhone Business plan.",
  audience: "both",
  aiMetadata: { description: "Retrieves the AI-generated summary of a single call identified by its call ID (format AC...). Choose this to fetch the post-call summary text for a known call. Requires a valid call ID and an OpenPhone Business plan (summaries are unavailable otherwise). Idempotent read-only lookup.", idempotent: !0 },
  props: {
    phoneNumberId: phoneNumberDropdown,
    participantNumber: participantDropdown,
    callId: callDropdown
  },
  async run(context) {
    let { callId } = context.propsValue, auth = context.auth;
    if (!callId || callId === "manual" || callId === "")
      throw new Error(
        "Please provide a valid Call ID in the format AC... or select a call from the dropdown"
      );
    if (!callId.match(/^AC[a-zA-Z0-9]+$/))
      throw new Error(
        'Invalid call ID format. Call ID should start with "AC" followed by alphanumeric characters.'
      );
    try {
      return await openPhoneCommon.makeRequest(
        import_pieces_common6.HttpMethod.GET,
        `/v1/call-summaries/${callId}`,
        auth
      );
    } catch (error) {
      throw new Error(`Failed to get call summary: ${error}`);
    }
  }
});

// ../ap/packages/pieces/community/open-phone/src/lib/triggers/call-recording-completed.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var callRecordingCompleted = (0, import_pieces_framework7.createTrigger)({
  auth: openPhoneAuth,
  name: "call_recording_completed",
  classification: "READ",
  displayName: "Call Recording Completed",
  description: "Fires when a call recording finishes. Useful for post-transcription or archival workflows.",
  aiMetadata: {
    description: "Fires when a call recording becomes available after a call ends, delivering the call details and a recording URL. Use to drive transcription, archival, or post-call processing. Can be scoped to a specific OpenPhone number or all numbers."
  },
  props: {
    phoneNumbers: phoneNumberDropdown
  },
  sampleData: {
    event: "call.recording.completed",
    data: {
      id: "AC123abc",
      phoneNumberId: "PN123abc",
      userId: "US123abc",
      direction: "incoming",
      status: "completed",
      participants: ["+15555555555"],
      duration: 120,
      recordingUrl: "https://example.com/recording.mp3",
      createdAt: "2022-01-01T00:00:00Z",
      completedAt: "2022-01-01T00:02:00Z"
    }
  },
  type: import_pieces_framework7.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { phoneNumbers } = context.propsValue, auth = context.auth, webhookRequest = {
      url: context.webhookUrl,
      events: ["call.recording.completed"],
      resourceIds: phoneNumbers ? [phoneNumbers] : ["*"],
      label: "Activepieces Call Recording Completed Trigger",
      status: "enabled"
    };
    try {
      let response = await openPhoneCommon.makeRequest(
        import_pieces_common7.HttpMethod.POST,
        "/v1/webhooks/calls",
        auth,
        webhookRequest
      );
      await context.store.put("webhookId", response.data.id);
    } catch (error) {
      throw new Error(`Failed to create webhook: ${error}`);
    }
  },
  async onDisable(context) {
    let auth = context.auth, webhookId = await context.store.get("webhookId");
    if (webhookId)
      try {
        await openPhoneCommon.makeRequest(
          import_pieces_common7.HttpMethod.DELETE,
          `/v1/webhooks/calls/${webhookId}`,
          auth
        );
      } catch (error) {
        throw new Error(`Failed to delete webhook: ${error}`);
      }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/open-phone/src/lib/triggers/outgoing-message-delivered.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var outgoingMessageDelivered = (0, import_pieces_framework8.createTrigger)({
  auth: openPhoneAuth,
  name: "outgoing_message_delivered",
  classification: "READ",
  displayName: "Outgoing Message Delivered",
  description: "Fires when an outbound message is delivered successfully. Useful for message confirmation workflows.",
  aiMetadata: {
    description: "Fires when an outbound SMS/MMS sent from an OpenPhone number is successfully delivered to the recipient, delivering the message details. Use to confirm delivery or trigger follow-up steps. Can be scoped to a specific OpenPhone number or all numbers."
  },
  props: {
    phoneNumbers: phoneNumberDropdown
  },
  sampleData: {
    event: "message.delivered",
    data: {
      id: "AC123abc",
      to: ["+15555555555"],
      from: "+15551234567",
      text: "Hello, this is a test message",
      phoneNumberId: "PN123abc",
      direction: "outgoing",
      userId: "US123abc",
      status: "delivered",
      createdAt: "2022-01-01T00:00:00Z",
      updatedAt: "2022-01-01T00:00:30Z"
    }
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { phoneNumbers } = context.propsValue, auth = context.auth, webhookRequest = {
      url: context.webhookUrl,
      events: ["message.delivered"],
      resourceIds: phoneNumbers ? [phoneNumbers] : ["*"],
      label: "Activepieces Outgoing Message Delivered Trigger",
      status: "enabled"
    };
    try {
      let response = await openPhoneCommon.makeRequest(
        import_pieces_common8.HttpMethod.POST,
        "/v1/webhooks/messages",
        auth,
        webhookRequest
      );
      await context.store.put("webhookId", response.data.id);
    } catch (error) {
      throw new Error(`Failed to create webhook: ${error}`);
    }
  },
  async onDisable(context) {
    let auth = context.auth, webhookId = await context.store.get("webhookId");
    if (webhookId)
      try {
        await openPhoneCommon.makeRequest(
          import_pieces_common8.HttpMethod.DELETE,
          `/v1/webhooks/messages/${webhookId}`,
          auth
        );
      } catch (error) {
        throw new Error(`Failed to delete webhook: ${error}`);
      }
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/open-phone/src/lib/triggers/outgoing-call-completed.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var outgoingCallCompleted = (0, import_pieces_framework9.createTrigger)({
  auth: openPhoneAuth,
  name: "outgoing_call_completed",
  classification: "READ",
  displayName: "Outgoing Call Completed",
  description: "Fires when an outbound call ends. Useful for call logging.",
  aiMetadata: {
    description: "Fires when an outbound (workspace-initiated) call ends, delivering the completed call details. Incoming calls are filtered out, so this only represents calls placed from the OpenPhone number. Use for call logging or post-call automation. Can be scoped to a specific OpenPhone number or all numbers."
  },
  props: {
    phoneNumbers: phoneNumberDropdown
  },
  sampleData: {
    event: "call.completed",
    data: {
      id: "AC123abc",
      phoneNumberId: "PN123abc",
      userId: "US123abc",
      direction: "outgoing",
      status: "completed",
      participants: ["+15555555555"],
      duration: 120,
      initiatedBy: "US123abc",
      answeredAt: "2022-01-01T00:00:30Z",
      completedAt: "2022-01-01T00:02:30Z",
      createdAt: "2022-01-01T00:00:00Z",
      updatedAt: "2022-01-01T00:02:30Z"
    }
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { phoneNumbers } = context.propsValue, auth = context.auth, webhookRequest = {
      url: context.webhookUrl,
      events: ["call.completed"],
      resourceIds: phoneNumbers ? [phoneNumbers] : ["*"],
      label: "Activepieces Outgoing Call Completed Trigger",
      status: "enabled"
    };
    try {
      let response = await openPhoneCommon.makeRequest(
        import_pieces_common9.HttpMethod.POST,
        "/v1/webhooks/calls",
        auth,
        webhookRequest
      );
      await context.store.put("webhookId", response.data.id);
    } catch (error) {
      throw new Error(`Failed to create webhook: ${error}`);
    }
  },
  async onDisable(context) {
    let auth = context.auth, webhookId = await context.store.get("webhookId");
    if (webhookId)
      try {
        await openPhoneCommon.makeRequest(
          import_pieces_common9.HttpMethod.DELETE,
          `/v1/webhooks/calls/${webhookId}`,
          auth
        );
      } catch (error) {
        throw new Error(`Failed to delete webhook: ${error}`);
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return payload != null && payload.data && payload.data.direction === "outgoing" ? [payload] : [];
  }
});

// ../ap/packages/pieces/community/open-phone/src/lib/triggers/incoming-call-completed.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var incomingCallCompleted = (0, import_pieces_framework10.createTrigger)({
  auth: openPhoneAuth,
  name: "incoming_call_completed",
  classification: "READ",
  displayName: "Incoming Call Completed",
  description: "Fires when an incoming call is completed. Includes voicemail data if available.",
  aiMetadata: {
    description: "Fires when an inbound call to the OpenPhone number ends, delivering the completed call details including voicemail data when present. Outbound calls are filtered out, so this only represents received calls. Use for inbound-call logging, voicemail handling, or follow-up. Can be scoped to a specific OpenPhone number or all numbers."
  },
  props: {
    phoneNumbers: phoneNumberDropdown
  },
  sampleData: {
    event: "call.completed",
    data: {
      id: "AC123abc",
      phoneNumberId: "PN123abc",
      userId: "US123abc",
      direction: "incoming",
      status: "completed",
      participants: ["+15555555555"],
      duration: 120,
      answeredBy: "US123abc",
      answeredAt: "2022-01-01T00:00:30Z",
      completedAt: "2022-01-01T00:02:30Z",
      createdAt: "2022-01-01T00:00:00Z",
      updatedAt: "2022-01-01T00:02:30Z",
      voicemailUrl: "https://example.com/voicemail.mp3",
      voicemailDuration: 15
    }
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { phoneNumbers } = context.propsValue, auth = context.auth, webhookRequest = {
      url: context.webhookUrl,
      events: ["call.completed"],
      resourceIds: phoneNumbers ? [phoneNumbers] : ["*"],
      label: "Activepieces Incoming Call Completed Trigger",
      status: "enabled"
    };
    try {
      let response = await openPhoneCommon.makeRequest(
        import_pieces_common10.HttpMethod.POST,
        "/v1/webhooks/calls",
        auth,
        webhookRequest
      );
      await context.store.put("webhookId", response.data.id);
    } catch (error) {
      throw new Error(`Failed to create webhook: ${error}`);
    }
  },
  async onDisable(context) {
    let auth = context.auth, webhookId = await context.store.get("webhookId");
    if (webhookId)
      try {
        await openPhoneCommon.makeRequest(
          import_pieces_common10.HttpMethod.DELETE,
          `/v1/webhooks/calls/${webhookId}`,
          auth
        );
      } catch (error) {
        throw new Error(`Failed to delete webhook: ${error}`);
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return payload != null && payload.data && payload.data.direction === "incoming" ? [payload] : [];
  }
});

// ../ap/packages/pieces/community/open-phone/src/lib/triggers/incoming-message-received.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var incomingMessageReceived = (0, import_pieces_framework11.createTrigger)({
  auth: openPhoneAuth,
  name: "incoming_message_received",
  classification: "READ",
  displayName: "Incoming Message Received",
  description: "Fires when a new SMS/MMS message is received.",
  aiMetadata: {
    description: "Fires when the OpenPhone number receives a new inbound SMS/MMS message, delivering the message text and sender details. Use to react to incoming texts (auto-reply, routing, logging). Can be scoped to a specific OpenPhone number or all numbers."
  },
  props: {
    phoneNumbers: phoneNumberDropdown
  },
  sampleData: {
    event: "message.received",
    data: {
      id: "AC123abc",
      to: ["+15551234567"],
      from: "+15555555555",
      text: "Hello, this is an incoming message",
      phoneNumberId: "PN123abc",
      direction: "incoming",
      userId: "US123abc",
      status: "received",
      createdAt: "2022-01-01T00:00:00Z",
      updatedAt: "2022-01-01T00:00:00Z"
    }
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let { phoneNumbers } = context.propsValue, auth = context.auth, webhookRequest = {
      url: context.webhookUrl,
      events: ["message.received"],
      resourceIds: phoneNumbers ? [phoneNumbers] : ["*"],
      label: "Activepieces Incoming Message Received Trigger",
      status: "enabled"
    };
    try {
      let response = await openPhoneCommon.makeRequest(
        import_pieces_common11.HttpMethod.POST,
        "/v1/webhooks/messages",
        auth,
        webhookRequest
      );
      await context.store.put("webhookId", response.data.id);
    } catch (error) {
      throw new Error(`Failed to create webhook: ${error}`);
    }
  },
  async onDisable(context) {
    let auth = context.auth, webhookId = await context.store.get("webhookId");
    if (webhookId)
      try {
        await openPhoneCommon.makeRequest(
          import_pieces_common11.HttpMethod.DELETE,
          `/v1/webhooks/messages/${webhookId}`,
          auth
        );
      } catch (error) {
        throw new Error(`Failed to delete webhook: ${error}`);
      }
  },
  async run(context) {
    let payload = context.payload.body;
    return payload != null && payload.data && payload.data.direction === "incoming" ? [payload] : [];
  }
});

// ../ap/packages/pieces/community/open-phone/src/index.ts
var openPhone = (0, import_pieces_framework12.createPiece)({
  displayName: "OpenPhone",
  auth: openPhoneAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/open-phone.png",
  authors: ["Ani-4x", "onyedikachi-david"],
  actions: [sendMessage, createContact, updateContact, getCallSummary],
  triggers: [
    callRecordingCompleted,
    outgoingMessageDelivered,
    outgoingCallCompleted,
    incomingCallCompleted,
    incomingMessageReceived
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  openPhone
});
