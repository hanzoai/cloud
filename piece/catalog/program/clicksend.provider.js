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

// ../ap/packages/pieces/community/clicksend/src/index.ts
var index_exports = {};
__export(index_exports, {
  clicksend: () => clicksend,
  clicksendAuth: () => clicksendAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_framework14 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/clicksend/src/lib/action/send-sms.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/clicksend/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var clicksendCommon = {
  phone_number: import_pieces_framework.Property.ShortText({
    description: "The phone number (with country code, e.g., +1234567890)",
    displayName: "Phone Number",
    required: !0
  }),
  email: import_pieces_framework.Property.ShortText({
    description: "The email address",
    displayName: "Email Address",
    required: !0
  }),
  contact_list_id: import_pieces_framework.Property.Dropdown({
    auth: clicksendAuth,
    displayName: "Contact List ID",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      var _a, _b, _c;
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account first."
        };
      let authValue = auth, options = [], currentPage = 1, hasNext = !0;
      do {
        let response = await callClickSendApi({
          method: import_pieces_common.HttpMethod.GET,
          username: authValue.username,
          password: authValue.password,
          path: "/lists",
          query: { page: currentPage.toString(), limit: "100" }
        }), items = (_b = (_a = response.body.data) == null ? void 0 : _a.data) != null ? _b : [];
        for (let list of items)
          options.push({ label: list.list_name, value: list.list_id });
        currentPage++, hasNext = !!((_c = response.body.data) != null && _c.next_page_url);
      } while (hasNext);
      return {
        disabled: !1,
        options
      };
    }
  }),
  contact_id: import_pieces_framework.Property.Dropdown({
    auth: clicksendAuth,
    displayName: "Contact ID",
    required: !0,
    refreshers: ["contact_list_id"],
    options: async ({ auth, contact_list_id }) => {
      var _a, _b, _c;
      if (!auth || !contact_list_id)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account first."
        };
      let authValue = auth, options = [], currentPage = 1, hasNext = !0;
      do {
        let response = await callClickSendApi({
          method: import_pieces_common.HttpMethod.GET,
          username: authValue.username,
          password: authValue.password,
          path: `/lists/${contact_list_id}/contacts`,
          query: { page: currentPage.toString(), limit: "100" }
        }), items = (_b = (_a = response.body.data) == null ? void 0 : _a.data) != null ? _b : [];
        for (let contact of items)
          options.push({ label: contact.email, value: contact.contact_id });
        currentPage++, hasNext = !!((_c = response.body.data) != null && _c.next_page_url);
      } while (hasNext);
      return {
        disabled: !1,
        options
      };
    }
  }),
  sender_id: import_pieces_framework.Property.Dropdown({
    auth: clicksendAuth,
    displayName: "From",
    refreshers: [],
    required: !0,
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          options: [],
          placeholder: "Please connect your account first."
        };
      let authValue = auth, response = await callClickSendApi({
        method: import_pieces_common.HttpMethod.GET,
        path: "/account",
        username: authValue.username,
        password: authValue.password
      });
      return {
        disabled: !1,
        options: [
          {
            label: response.body.data.username,
            value: response.body.data.user_id
          }
        ]
      };
    }
  })
};
async function callClickSendApi(params) {
  return await import_pieces_common.httpClient.sendRequest({
    method: params.method,
    url: `https://rest.clicksend.com/v3${params.path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BASIC,
      username: params.username,
      password: params.password
    },
    headers: {
      "Content-Type": "application/json"
    },
    body: params.body,
    queryParams: params.query
  });
}

// ../ap/packages/pieces/community/clicksend/src/lib/action/send-sms.ts
var clicksendSendSmsAction = (0, import_pieces_framework2.createAction)({
  auth: clicksendAuth,
  name: "send_sms",
  classification: "WRITE",
  description: "Send one or more SMS messages.",
  audience: "both",
  aiMetadata: {
    description: "Sends one or more SMS text messages via ClickSend, each with its own recipient phone number (in international format) and body. Choose this to deliver text notifications, alerts, or one-off messages to phone numbers. Not idempotent: each call dispatches the messages again, so repeating it sends duplicates.",
    idempotent: !1
  },
  displayName: "Send SMS",
  props: {
    messages: import_pieces_framework2.Property.Array({
      displayName: "Messages",
      required: !0,
      properties: {
        to: import_pieces_framework2.Property.ShortText({
          description: "The phone number (with country code, e.g., +1234567890)",
          displayName: "To",
          required: !0
        }),
        body: import_pieces_framework2.Property.ShortText({
          description: "The body of the message to send",
          displayName: "Message Body",
          required: !0
        }),
        from: import_pieces_framework2.Property.ShortText({
          description: "The sender name or number (must be approved in ClickSend).",
          displayName: "From",
          required: !1
        }),
        custom_string: import_pieces_framework2.Property.ShortText({
          description: "A custom string for tracking the message",
          displayName: "Custom String",
          required: !1
        }),
        country: import_pieces_framework2.Property.ShortText({
          description: "Country code (for compliance)",
          displayName: "Country",
          required: !1
        }),
        message_expiry: import_pieces_framework2.Property.Number({
          description: "How long (in minutes) the message is valid for.",
          displayName: "Message Expiry (minutes)",
          required: !1
        }),
        priority: import_pieces_framework2.Property.Checkbox({
          description: "Send as high priority",
          displayName: "Priority",
          required: !1
        })
      }
    })
  },
  async run(context) {
    var _a;
    let messages = (_a = context.propsValue.messages) != null ? _a : [];
    if (!Array.isArray(messages) || messages.length === 0)
      throw new Error("At least one message must be provided.");
    let username = context.auth.username, password = context.auth.password, messageData = {
      messages: messages.map((msg) => {
        let {
          to,
          body,
          from,
          custom_string,
          country,
          message_expiry,
          priority
        } = msg;
        return __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
          to,
          body
        }, from && { from }), custom_string && { custom_string }), country && { country }), message_expiry && { message_expiry }), priority !== void 0 ? { priority } : {});
      })
    };
    return (await callClickSendApi({
      method: import_pieces_common2.HttpMethod.POST,
      path: "/sms/send",
      username,
      password,
      body: messageData
    })).body;
  }
});

// ../ap/packages/pieces/community/clicksend/src/lib/action/send-mms.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var clicksendSendMms = (0, import_pieces_framework3.createAction)({
  auth: clicksendAuth,
  name: "send_mms",
  classification: "WRITE",
  description: "Send one or more MMS messages.",
  audience: "both",
  aiMetadata: {
    description: "Sends an MMS multimedia message via ClickSend to a recipient phone number, attaching a media file referenced by URL along with a subject and body. Choose this over Send SMS when the message must include an image, video, or other media. Requires a publicly reachable media URL; not idempotent, as each call dispatches the message again.",
    idempotent: !1
  },
  displayName: "Send MMS",
  props: {
    to: import_pieces_framework3.Property.ShortText({
      description: "The phone number (with country code, e.g., +1234567890)",
      displayName: "To",
      required: !0
    }),
    body: import_pieces_framework3.Property.ShortText({
      description: "The body of the message to send",
      displayName: "Message Body",
      required: !0
    }),
    subject: import_pieces_framework3.Property.ShortText({
      displayName: "Subject",
      required: !0
    }),
    from: clicksendCommon.sender_id,
    media_url: import_pieces_framework3.Property.ShortText({
      description: "The URL of the media file to send (image, video, etc.)",
      displayName: "Media URL",
      required: !0
    })
  },
  async run(context) {
    var _a, _b;
    let {
      to,
      body,
      from,
      media_url,
      subject
    } = context.propsValue, username = context.auth.username, password = context.auth.password;
    if (!to || !body || !media_url)
      throw new Error(
        "Each message must have a recipient (to), body, and media_url."
      );
    try {
      return (await callClickSendApi({
        method: import_pieces_common3.HttpMethod.POST,
        path: "/mms/send",
        username,
        password,
        body: {
          media_file: media_url,
          messages: [
            {
              subject,
              from,
              body,
              to
            }
          ]
        }
      })).body;
    } catch (error) {
      throw (_b = (_a = error == null ? void 0 : error.response) == null ? void 0 : _a.body) != null && _b.response_msg ? new Error(
        `ClickSend API error: ${error.response.body.response_msg}`
      ) : error;
    }
  }
});

// ../ap/packages/pieces/community/clicksend/src/lib/action/create-contact.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
function isValidPhone(phone) {
  return /^\+?[1-9]\d{1,14}$/.test(phone);
}
function isValidEmail(email) {
  return /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email);
}
var clicksendCreateContactAction = (0, import_pieces_framework4.createAction)({
  auth: clicksendAuth,
  name: "create_contact",
  classification: "WRITE",
  description: "Creates a new contact in a contact list.",
  audience: "both",
  aiMetadata: {
    description: "Adds a new contact (phone number plus optional email, name, company, and address fields) to a specific ClickSend contact list identified by its list id. Choose this to populate a list before messaging. Requires a valid phone number; not idempotent, as repeating the call attempts to create another contact and the API rejects duplicates.",
    idempotent: !1
  },
  displayName: "Create Contact",
  props: {
    contact_list_id: clicksendCommon.contact_list_id,
    phone_number: import_pieces_framework4.Property.ShortText({
      description: "The phone number of the contact",
      displayName: "Phone Number",
      required: !0
    }),
    email: import_pieces_framework4.Property.ShortText({
      description: "The email address of the contact",
      displayName: "Email Address",
      required: !1
    }),
    first_name: import_pieces_framework4.Property.ShortText({
      description: "The first name of the contact",
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework4.Property.ShortText({
      description: "The last name of the contact",
      displayName: "Last Name",
      required: !1
    }),
    company_name: import_pieces_framework4.Property.ShortText({
      description: "The company name of the contact",
      displayName: "Company Name",
      required: !1
    }),
    address_line_1: import_pieces_framework4.Property.ShortText({
      description: "The first line of the address",
      displayName: "Address Line 1",
      required: !1
    }),
    address_line_2: import_pieces_framework4.Property.ShortText({
      description: "The second line of the address",
      displayName: "Address Line 2",
      required: !1
    }),
    city: import_pieces_framework4.Property.ShortText({
      description: "The city of the contact",
      displayName: "City",
      required: !1
    }),
    state: import_pieces_framework4.Property.ShortText({
      description: "The state/province of the contact",
      displayName: "State/Province",
      required: !1
    }),
    postal_code: import_pieces_framework4.Property.ShortText({
      description: "The postal code of the contact",
      displayName: "Postal Code",
      required: !1
    }),
    country: import_pieces_framework4.Property.ShortText({
      description: "The country of the contact",
      displayName: "Country",
      required: !1
    })
  },
  async run(context) {
    var _a, _b;
    let {
      contact_list_id,
      phone_number,
      email,
      first_name,
      last_name,
      company_name,
      address_line_1,
      address_line_2,
      city,
      state,
      postal_code,
      country
    } = context.propsValue;
    if (!phone_number || !isValidPhone(phone_number))
      throw new Error("A valid phone number is required.");
    if (email && !isValidEmail(email))
      throw new Error("Invalid email address.");
    let username = context.auth.username, password = context.auth.password, contactData = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({
      phone_number
    }, email && { email }), first_name && { first_name }), last_name && { last_name }), company_name && { company_name }), address_line_1 && { address_line_1 }), address_line_2 && { address_line_2 }), city && { city }), state && { state }), postal_code && { postal_code }), country && { country });
    try {
      return (await callClickSendApi({
        method: import_pieces_common4.HttpMethod.POST,
        path: `/lists/${contact_list_id}/contacts`,
        username,
        password,
        body: contactData
      })).body;
    } catch (error) {
      throw ((_b = (_a = error == null ? void 0 : error.response) == null ? void 0 : _a.body) == null ? void 0 : _b.response_code) === "ALREADY_EXISTS" ? new Error("Contact already exists in this list.") : error;
    }
  }
});

// ../ap/packages/pieces/community/clicksend/src/lib/action/update-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
function isValidPhone2(phone) {
  return /^\+?[1-9]\d{1,14}$/.test(phone);
}
function isValidEmail2(email) {
  return /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email);
}
var clicksendUpdateContactAction = (0, import_pieces_framework5.createAction)({
  auth: clicksendAuth,
  name: "update_contact",
  classification: "WRITE",
  description: "Updates an existing contact in a contact list.",
  audience: "both",
  aiMetadata: {
    description: "Updates the details of an existing ClickSend contact, identified by its contact id within a given contact list, applying only the fields you supply (phone, email, name, company, address). Choose this to edit a known contact rather than create one. Idempotent: re-sending the same values leaves the contact in the same state.",
    idempotent: !0
  },
  displayName: "Update Contact",
  props: {
    contact_list_id: clicksendCommon.contact_list_id,
    contact_id: clicksendCommon.contact_id,
    phone_number: import_pieces_framework5.Property.ShortText({
      description: "The phone number of the contact",
      displayName: "Phone Number",
      required: !1
    }),
    email: import_pieces_framework5.Property.ShortText({
      description: "The email address of the contact",
      displayName: "Email Address",
      required: !1
    }),
    first_name: import_pieces_framework5.Property.ShortText({
      description: "The first name of the contact",
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework5.Property.ShortText({
      description: "The last name of the contact",
      displayName: "Last Name",
      required: !1
    }),
    company_name: import_pieces_framework5.Property.ShortText({
      description: "The company name of the contact",
      displayName: "Company Name",
      required: !1
    }),
    address_line_1: import_pieces_framework5.Property.ShortText({
      description: "The first line of the address",
      displayName: "Address Line 1",
      required: !1
    }),
    address_line_2: import_pieces_framework5.Property.ShortText({
      description: "The second line of the address",
      displayName: "Address Line 2",
      required: !1
    }),
    city: import_pieces_framework5.Property.ShortText({
      description: "The city of the contact",
      displayName: "City",
      required: !1
    }),
    state: import_pieces_framework5.Property.ShortText({
      description: "The state/province of the contact",
      displayName: "State/Province",
      required: !1
    }),
    postal_code: import_pieces_framework5.Property.ShortText({
      description: "The postal code of the contact",
      displayName: "Postal Code",
      required: !1
    }),
    country: import_pieces_framework5.Property.ShortText({
      description: "The country of the contact",
      displayName: "Country",
      required: !1
    })
  },
  async run(context) {
    var _a, _b;
    let {
      contact_id,
      phone_number,
      email,
      first_name,
      last_name,
      company_name,
      address_line_1,
      address_line_2,
      city,
      state,
      postal_code,
      contact_list_id,
      country
    } = context.propsValue;
    if (phone_number && !isValidPhone2(phone_number))
      throw new Error("Invalid phone number.");
    if (email && !isValidEmail2(email))
      throw new Error("Invalid email address.");
    let username = context.auth.username, password = context.auth.password, contactData = __spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues(__spreadValues({}, phone_number && { phone_number }), email && { email }), first_name && { first_name }), last_name && { last_name }), company_name && { company_name }), address_line_1 && { address_line_1 }), address_line_2 && { address_line_2 }), city && { city }), state && { state }), postal_code && { postal_code }), country && { country });
    try {
      return (await callClickSendApi({
        method: import_pieces_common5.HttpMethod.PUT,
        path: `/lists/${contact_list_id}/contacts/${contact_id}`,
        username,
        password,
        body: contactData
      })).body;
    } catch (error) {
      throw ((_a = error == null ? void 0 : error.response) == null ? void 0 : _a.status) === 404 ? new Error("Contact not found.") : ((_b = error == null ? void 0 : error.response) == null ? void 0 : _b.status) === 403 ? new Error("Permission denied.") : error;
    }
  }
});

// ../ap/packages/pieces/community/clicksend/src/lib/action/delete-contact.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var clicksendDeleteContactAction = (0, import_pieces_framework6.createAction)({
  auth: clicksendAuth,
  name: "delete_contact",
  classification: "DESTRUCTIVE",
  description: "Deletes a contact from a contact list.",
  audience: "both",
  aiMetadata: {
    description: "Removes a contact, identified by its contact id, from a specific ClickSend contact list. Choose this to permanently delete a known contact from a list. Effectively idempotent: once removed, repeating the call has no further effect (a missing contact returns a not-found error rather than altering state).",
    idempotent: !0
  },
  displayName: "Delete Contact",
  props: {
    contact_list_id: clicksendCommon.contact_list_id,
    contact_id: clicksendCommon.contact_id
  },
  async run(context) {
    var _a, _b;
    let { contact_id, contact_list_id } = context.propsValue, username = context.auth.username, password = context.auth.password;
    try {
      return await callClickSendApi({
        method: import_pieces_common6.HttpMethod.DELETE,
        path: `/lists/${contact_list_id}/contacts/${contact_id}`,
        username,
        password
      }), { success: !0, message: "Contact deleted." };
    } catch (error) {
      throw ((_a = error == null ? void 0 : error.response) == null ? void 0 : _a.status) === 404 ? new Error("Contact not found.") : ((_b = error == null ? void 0 : error.response) == null ? void 0 : _b.status) === 403 ? new Error("Permission denied.") : error;
    }
  }
});

// ../ap/packages/pieces/community/clicksend/src/lib/action/create-contact-list.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var clicksendCreateContactListAction = (0, import_pieces_framework7.createAction)({
  auth: clicksendAuth,
  name: "create_contact_list",
  classification: "WRITE",
  description: "Creates a new contact list.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new ClickSend contact list with the given name, returning a list id that other contact actions reference. Choose this to set up a list before adding contacts. Requires a non-empty name; not idempotent, as repeating the call attempts to create another list and the API rejects a duplicate name.",
    idempotent: !1
  },
  displayName: "Create Contact List",
  props: {
    list_name: import_pieces_framework7.Property.ShortText({
      displayName: "List Name",
      required: !0
    })
  },
  async run(context) {
    var _a, _b;
    let { list_name } = context.propsValue;
    if (!list_name || list_name.trim().length === 0)
      throw new Error("List name must not be empty.");
    let username = context.auth.username, password = context.auth.password, listData = {
      list_name
    };
    try {
      return (await callClickSendApi({
        method: import_pieces_common7.HttpMethod.POST,
        path: "/lists",
        username,
        password,
        body: listData
      })).body;
    } catch (error) {
      throw ((_b = (_a = error == null ? void 0 : error.response) == null ? void 0 : _a.body) == null ? void 0 : _b.response_code) === "ALREADY_EXISTS" ? new Error("A contact list with this name already exists.") : error;
    }
  }
});

// ../ap/packages/pieces/community/clicksend/src/lib/action/search-contact-by-email.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
function isValidEmail3(email) {
  return /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email);
}
var clicksendFindContactByEmailAction = (0, import_pieces_framework8.createAction)({
  auth: clicksendAuth,
  name: "find_contact_by_email",
  classification: "READ",
  description: "Finds contact by email address.",
  audience: "both",
  aiMetadata: {
    description: "Searches a specific ClickSend contact list for a contact whose email exactly matches the given address, paging through the whole list until a match is found. Choose this to look up or verify a contact by email before updating or messaging them. Requires a valid email and the target list id; read-only and idempotent.",
    idempotent: !0
  },
  displayName: "Find Contact by Email",
  props: {
    contact_list_id: clicksendCommon.contact_list_id,
    email: clicksendCommon.email
  },
  async run(context) {
    var _a, _b, _c;
    let { contact_list_id, email } = context.propsValue;
    if (!isValidEmail3(email))
      throw new Error("Invalid email address.");
    let username = context.auth.username, password = context.auth.password, currentPage = 1, hasNext = !0;
    do {
      let response = await callClickSendApi({
        method: import_pieces_common8.HttpMethod.GET,
        username,
        password,
        path: `/lists/${contact_list_id}/contacts`,
        query: { page: currentPage.toString(), limit: "100" }
      }), matched = ((_b = (_a = response.body.data) == null ? void 0 : _a.data) != null ? _b : []).find((item) => item.email === email);
      if (matched)
        return {
          found: !0,
          data: matched
        };
      currentPage++, hasNext = !!((_c = response.body.data) != null && _c.next_page_url);
    } while (hasNext);
    return {
      found: !1,
      data: {}
    };
  }
});

// ../ap/packages/pieces/community/clicksend/src/lib/action/search-contact-by-phone.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
function isValidPhone3(phone) {
  return /^\+?[1-9]\d{1,14}$/.test(phone);
}
var clicksendFindContactByPhoneAction = (0, import_pieces_framework9.createAction)({
  auth: clicksendAuth,
  name: "find_contact_by_phone",
  classification: "READ",
  description: "Finds contact by phone number.",
  audience: "both",
  aiMetadata: {
    description: "Searches a specific ClickSend contact list for a contact whose phone number exactly matches the given value, paging through the whole list until a match is found. Choose this to look up or verify a contact by phone before updating or messaging them. Requires a valid phone number and the target list id; read-only and idempotent.",
    idempotent: !0
  },
  displayName: "Find Contact by Phone",
  props: {
    contact_list_id: clicksendCommon.contact_list_id,
    phone_number: clicksendCommon.phone_number
  },
  async run(context) {
    var _a, _b, _c;
    let { contact_list_id, phone_number } = context.propsValue;
    if (!isValidPhone3(phone_number))
      throw new Error("Invalid phone number.");
    let username = context.auth.username, password = context.auth.password, currentPage = 1, hasNext = !0;
    do {
      let response = await callClickSendApi({
        method: import_pieces_common9.HttpMethod.GET,
        username,
        password,
        path: `/lists/${contact_list_id}/contacts`,
        query: { page: currentPage.toString(), limit: "100" }
      }), matched = ((_b = (_a = response.body.data) == null ? void 0 : _a.data) != null ? _b : []).find((item) => item.phone_number === phone_number);
      if (matched)
        return {
          found: !0,
          data: matched
        };
      currentPage++, hasNext = !!((_c = response.body.data) != null && _c.next_page_url);
    } while (hasNext);
    return {
      found: !1,
      data: {}
    };
  }
});

// ../ap/packages/pieces/community/clicksend/src/lib/action/search-contact-lists.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var clicksendFindContactListAction = (0, import_pieces_framework10.createAction)({
  auth: clicksendAuth,
  name: "find_contact_lists",
  classification: "READ",
  description: "Finds for contact list based on name.",
  audience: "both",
  aiMetadata: {
    description: "Searches all ClickSend contact lists for one whose name exactly matches the given search term, paging through every list until a match is found. Choose this to resolve a list name to its list id for use by the contact actions. Matching is exact (not partial); read-only and idempotent.",
    idempotent: !0
  },
  displayName: "Find Contact List",
  props: {
    search: import_pieces_framework10.Property.ShortText({
      displayName: "Search",
      description: "Search by list name.",
      required: !0
    })
  },
  async run(context) {
    var _a, _b, _c;
    let { search } = context.propsValue, username = context.auth.username, password = context.auth.password, currentPage = 1, hasNext = !0;
    do {
      let response = await callClickSendApi({
        method: import_pieces_common10.HttpMethod.GET,
        username,
        password,
        path: "/lists",
        query: { page: currentPage.toString(), limit: "100" }
      }), matched = ((_b = (_a = response.body.data) == null ? void 0 : _a.data) != null ? _b : []).find((item) => item.list_name === search);
      if (matched)
        return {
          found: !0,
          data: matched
        };
      currentPage++, hasNext = !!((_c = response.body.data) != null && _c.next_page_url);
    } while (hasNext);
    return {
      found: !1,
      data: {}
    };
  }
});

// ../ap/packages/pieces/community/clicksend/src/lib/trigger/new-incoming-sms.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var import_pieces_framework12 = require("@activepieces/pieces-framework"), TRIGGER_KEY = "new_incoming_sms_trigger", clicksendNewIncomingSms = (0, import_pieces_framework11.createTrigger)({
  auth: clicksendAuth,
  name: "new_incoming_sms",
  classification: "READ",
  displayName: "New Incoming SMS",
  description: "Triggers when a new SMS message is received.",
  aiMetadata: {
    description: "Fires when ClickSend receives an inbound SMS to any of the account dedicated numbers, delivering the incoming message and its sender, recipient, and content details. Represents a new reply or inbound text arriving at the account."
  },
  props: {},
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    let username = context.auth.username, password = context.auth.password, response = await callClickSendApi({
      method: import_pieces_common11.HttpMethod.POST,
      username,
      password,
      path: "/automations/sms/inbound",
      body: {
        dedicated_number: "*",
        rule_name: "AP Incoming SMS",
        message_search_type: 0,
        message_search_term: null,
        action: "URL",
        action_address: context.webhookUrl,
        enabled: 1,
        webhook_type: "json"
      }
    });
    await context.store.put(
      TRIGGER_KEY,
      response.body.data.inbound_rule_id
    );
  },
  async onDisable(context) {
    let username = context.auth.username, password = context.auth.password, webhookId = await context.store.get(TRIGGER_KEY);
    (0, import_pieces_framework12.isNil)(webhookId) || await callClickSendApi({
      method: import_pieces_common11.HttpMethod.DELETE,
      username,
      password,
      path: `/automations/sms/inbound/${webhookId}`
    });
  },
  async run(context) {
    return [context.payload.body];
  },
  sampleData: {
    message_id: "12345678",
    status: "RECEIVED",
    message_timestamp: 1644321600,
    message_time: "2022-02-08 01:00:00",
    message_to: "+1234567890",
    message_from: "+0987654321",
    message_body: "Hello from ClickSend!",
    message_direction: "in",
    message_type: "sms",
    message_parts: 1,
    message_cost: "0.0250",
    from_email: null,
    list_id: null,
    custom_string: null,
    contact_id: null,
    user_id: 12345,
    subaccount_id: null,
    country: "US",
    carrier: "Verizon",
    first_name: "John",
    last_name: "Doe",
    email: "john.doe@example.com"
  }
});

// ../ap/packages/pieces/community/clicksend/src/index.ts
var clicksendAuth = import_pieces_framework13.PieceAuth.BasicAuth({
  description: "You can get your API credentials by clicking 'API Credentials' on the top right of the dashboard.",
  required: !0,
  username: {
    displayName: "Username",
    description: "Your ClickSend username"
  },
  password: {
    displayName: "API Key",
    description: "Your ClickSend API key"
  },
  validate: async ({ auth }) => {
    try {
      return await callClickSendApi({
        method: import_pieces_common12.HttpMethod.GET,
        path: "/account",
        username: auth.username,
        password: auth.password
      }), { valid: !0 };
    } catch (e) {
      return { valid: !1, error: "Invalid Credentials." };
    }
  }
}), clicksend = (0, import_pieces_framework13.createPiece)({
  displayName: "ClickSend SMS",
  description: "Cloud-based messaging platform for sending SMS, MMS, voice, email, and more.",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/clicksend.png",
  auth: clicksendAuth,
  categories: [import_pieces_framework14.PieceCategory.COMMUNICATION],
  actions: [
    clicksendSendSmsAction,
    clicksendSendMms,
    clicksendCreateContactAction,
    clicksendUpdateContactAction,
    clicksendDeleteContactAction,
    clicksendCreateContactListAction,
    clicksendFindContactByEmailAction,
    clicksendFindContactByPhoneAction,
    clicksendFindContactListAction,
    (0, import_pieces_common12.createCustomApiCallAction)({
      baseUrl: () => "https://rest.clicksend.com/v3",
      auth: clicksendAuth,
      authMapping: async (auth) => ({
        Authorization: `Basic ${Buffer.from(
          `${auth.username}:${auth.password}`
        ).toString("base64")}`
      })
    })
  ],
  authors: ["sparkybug"],
  triggers: [clicksendNewIncomingSms]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  clicksend,
  clicksendAuth
});
