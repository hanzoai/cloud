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

// ../ap/packages/pieces/community/useinbox/src/index.ts
var index_exports = {};
__export(index_exports, {
  useinbox: () => useinbox,
  useinboxAuth: () => useinboxAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/useinbox/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/useinbox/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common"), ACCOUNT_BASE_URL = "https://useapi.useinbox.com", INBOX_BASE_URL = "https://useapi.useinbox.com/inbox/v1", NOTIFY_BASE_URL = "https://useapi.useinbox.com/notify/v1";
async function fetchAccessToken({
  email,
  password
}) {
  var _a, _b;
  let token = (_b = (_a = (await import_pieces_common.httpClient.sendRequest({
    method: import_pieces_common.HttpMethod.POST,
    url: `${ACCOUNT_BASE_URL}/token`,
    headers: { "Content-Type": "application/json" },
    body: {
      EmailAddress: email,
      Password: password
    }
  })).body) == null ? void 0 : _a.resultObject) == null ? void 0 : _b.access_token;
  if (!token)
    throw new Error(
      "Failed to retrieve INBOX access token. Check email and password."
    );
  return token;
}
async function inboxApiCall({
  token,
  service,
  method,
  path,
  body,
  queryParams
}) {
  let base = service === "inbox" ? INBOX_BASE_URL : NOTIFY_BASE_URL, cleanedQuery = queryParams ? Object.fromEntries(
    Object.entries(queryParams).filter(([, v]) => v !== void 0)
  ) : void 0;
  return await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${base}${path}`,
    authentication: {
      type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
      token
    },
    headers: { "Content-Type": "application/json" },
    body,
    queryParams: cleanedQuery
  });
}
var useinboxClient = {
  fetchAccessToken,
  inboxApiCall,
  ACCOUNT_BASE_URL,
  INBOX_BASE_URL,
  NOTIFY_BASE_URL
};

// ../ap/packages/pieces/community/useinbox/src/lib/common/auth.ts
var useinboxAuth = import_pieces_framework.PieceAuth.BasicAuth({
  description: `Connect your INBOX account by entering the email and password you use to sign in at https://app.useinbox.com.

Each request exchanges these credentials for a short-lived bearer token, so make sure the credentials belong to a user that has access to the API.`,
  required: !0,
  username: {
    displayName: "Email",
    description: "The email address you sign in to INBOX with."
  },
  password: {
    displayName: "Password",
    description: "Your INBOX account password."
  },
  validate: async ({ auth }) => {
    try {
      return await useinboxClient.fetchAccessToken({
        email: auth.username,
        password: auth.password
      }), { valid: !0 };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid INBOX credentials. Double-check your email and password."
      };
    }
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/actions/add-contact-to-list.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/useinbox/src/lib/common/props.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");
async function withToken({
  auth,
  fn
}) {
  let token = await useinboxClient.fetchAccessToken({
    email: auth.username,
    password: auth.password
  });
  return await fn(token);
}
function disconnectedDropdown(message = "Please connect your INBOX account first.") {
  return { disabled: !0, options: [], placeholder: message };
}
var useinboxProps = {
  contactListDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.Dropdown({
      auth: useinboxAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Contact List",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : "Select the INBOX contact list. Lists are managed under Contacts > Lists in your dashboard.",
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return disconnectedDropdown();
        try {
          let data = await withToken({
            auth,
            fn: async (token) => {
              var _a2, _b2, _c2;
              return (_c2 = (_b2 = (_a2 = (await useinboxClient.inboxApiCall({
                token,
                service: "inbox",
                method: import_pieces_common2.HttpMethod.GET,
                path: "/contactlists"
              })).body) == null ? void 0 : _a2.resultObject) == null ? void 0 : _b2.items) != null ? _c2 : [];
            }
          });
          return data.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No contact lists found. Create one in INBOX first."
          } : {
            disabled: !1,
            options: data.map((list) => ({
              label: list.totalContacts !== void 0 ? `${list.listName} (${list.totalContacts} contacts)` : list.listName,
              value: list.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load contact lists. Check your credentials."
          };
        }
      }
    });
  },
  multiselectContactListDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.MultiSelectDropdown({
      auth: useinboxAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Contact Lists",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : "Select one or more INBOX contact lists. Lists are managed under Contacts > Lists in your dashboard.",
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return disconnectedDropdown();
        try {
          let data = await withToken({
            auth,
            fn: async (token) => {
              var _a2, _b2, _c2;
              return (_c2 = (_b2 = (_a2 = (await useinboxClient.inboxApiCall({
                token,
                service: "inbox",
                method: import_pieces_common2.HttpMethod.GET,
                path: "/contactlists"
              })).body) == null ? void 0 : _a2.resultObject) == null ? void 0 : _b2.items) != null ? _c2 : [];
            }
          });
          return data.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No contact lists found. Create one in INBOX first."
          } : {
            disabled: !1,
            options: data.map((list) => ({
              label: list.totalContacts !== void 0 ? `${list.listName} (${list.totalContacts} contacts)` : list.listName,
              value: list.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load contact lists. Check your credentials."
          };
        }
      }
    });
  },
  newsletterDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.Dropdown({
      auth: useinboxAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Newsletter",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : "Select the newsletter template to send. Newsletters are created under Newsletters in your INBOX dashboard.",
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return disconnectedDropdown();
        try {
          let data = await withToken({
            auth,
            fn: async (token) => {
              var _a2, _b2, _c2;
              return (_c2 = (_b2 = (_a2 = (await useinboxClient.inboxApiCall({
                token,
                service: "inbox",
                method: import_pieces_common2.HttpMethod.GET,
                path: "/newsletters"
              })).body) == null ? void 0 : _a2.resultObject) == null ? void 0 : _b2.items) != null ? _c2 : [];
            }
          });
          return data.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No newsletters found. Create one in INBOX first."
          } : {
            disabled: !1,
            options: data.map((nl) => ({
              label: nl.subject ? `${nl.name} \u2014 ${nl.subject}` : nl.name,
              value: nl.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load newsletters. Check your credentials."
          };
        }
      }
    });
  },
  senderDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.Dropdown({
      auth: useinboxAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Sender",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : "Select the verified sender that emails will be sent from. Senders are managed under Settings > Senders.",
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return disconnectedDropdown();
        try {
          let data = await withToken({
            auth,
            fn: async (token) => {
              var _a2, _b2, _c2, _d;
              return (_d = (_c2 = (_b2 = (await useinboxClient.inboxApiCall({
                token,
                service: (_a2 = params == null ? void 0 : params.service) != null ? _a2 : "inbox",
                method: import_pieces_common2.HttpMethod.GET,
                path: "/senders"
              })).body) == null ? void 0 : _b2.resultObject) == null ? void 0 : _c2.items) != null ? _d : [];
            }
          });
          return data.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No verified senders found. Add one in INBOX first."
          } : {
            disabled: !1,
            options: data.map((s) => ({
              label: `${s.displayName} <${s.email}>${s.isVerified === !1 ? " (unverified)" : ""}`,
              value: s.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load senders. Check your credentials."
          };
        }
      }
    });
  },
  groupDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.Dropdown({
      auth: useinboxAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Group",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : "Optional. Group used to organize contact lists. Pick one to keep your lists tidy.",
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return disconnectedDropdown();
        try {
          let data = await withToken({
            auth,
            fn: async (token) => {
              var _a2, _b2, _c2;
              return (_c2 = (_b2 = (_a2 = (await useinboxClient.inboxApiCall({
                token,
                service: "inbox",
                method: import_pieces_common2.HttpMethod.GET,
                path: "/groups"
              })).body) == null ? void 0 : _a2.resultObject) == null ? void 0 : _b2.items) != null ? _c2 : [];
            }
          });
          return data.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No groups found. Skip this field to leave it ungrouped."
          } : {
            disabled: !1,
            options: data.map((g) => ({
              label: g.groupName,
              value: g.id
            }))
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load groups. Check your credentials."
          };
        }
      }
    });
  },
  contactDropdown: (params) => {
    var _a, _b, _c;
    return import_pieces_framework2.Property.Dropdown({
      auth: useinboxAuth,
      displayName: (_a = params == null ? void 0 : params.displayName) != null ? _a : "Contact",
      description: (_b = params == null ? void 0 : params.description) != null ? _b : "Select the contact to update. Contacts are listed under Contacts in your INBOX dashboard.",
      required: (_c = params == null ? void 0 : params.required) != null ? _c : !0,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth) return disconnectedDropdown();
        try {
          let data = await withToken({
            auth,
            fn: async (token) => {
              var _a2, _b2, _c2;
              return (_c2 = (_b2 = (_a2 = (await useinboxClient.inboxApiCall({
                token,
                service: "inbox",
                method: import_pieces_common2.HttpMethod.GET,
                path: "/contacts"
              })).body) == null ? void 0 : _a2.resultObject) == null ? void 0 : _b2.items) != null ? _c2 : [];
            }
          });
          return data.length === 0 ? {
            disabled: !1,
            options: [],
            placeholder: "No contacts found. Add one in INBOX first."
          } : {
            disabled: !1,
            options: data.map((c) => {
              let name = [c.firstName, c.lastName].filter(Boolean).join(" ").trim();
              return {
                label: name ? `${name} (${c.email})` : c.email,
                value: c.id
              };
            })
          };
        } catch (e) {
          return {
            disabled: !0,
            options: [],
            placeholder: "Failed to load contacts. Check your credentials."
          };
        }
      }
    });
  }
  // customFieldsArray: () =>
  //   Property.Array({
  //     displayName: 'Custom Fields',
  //     description:
  //       'Optional. Map values to INBOX custom fields you have defined. Pick the field and enter the value for this contact.',
  //     required: false,
  //     properties: {
  //       customFieldId: Property.Dropdown({
  //         auth: useinboxAuth,
  //         displayName: 'Field',
  //         description: 'Pick the INBOX custom field to fill in.',
  //         required: true,
  //         refreshers: [],
  //         options: async ({ auth }) => {
  //           if (!auth) return disconnectedDropdown();
  //           try {
  //             const data = await withToken({
  //               auth: auth as BasicAuthValue,
  //               fn: async (token) => {
  //                 const response = await useinboxClient.inboxApiCall<InboxList<CustomField[]>>({
  //                   token,
  //                   service: 'inbox',
  //                   method: HttpMethod.GET,
  //                   path: '/customfields',
  //                 });
  //                 return response.body?.resultObject ?? [];
  //               },
  //             });
  //             if (data.length === 0) {
  //               return {
  //                 disabled: false,
  //                 options: [],
  //                 placeholder: 'No custom fields found. Define them in INBOX first.',
  //               };
  //             }
  //             return {
  //               disabled: false,
  //               options: data.map((f) => ({
  //                 label: f.fieldType ? `${f.fieldName} (${f.fieldType})` : f.fieldName,
  //                 value: f.id,
  //               })),
  //             };
  //           } catch (e) {
  //             return {
  //               disabled: true,
  //               options: [],
  //               placeholder: 'Failed to load custom fields.',
  //             };
  //           }
  //         },
  //       }),
  //       value: Property.ShortText({
  //         displayName: 'Value',
  //         description: 'The value to store for this custom field on the contact.',
  //         required: true,
  //       }),
  //     },
  //   }),
};

// ../ap/packages/pieces/community/useinbox/src/lib/actions/add-contact-to-list.ts
var addContactToListAction = (0, import_pieces_framework3.createAction)({
  auth: useinboxAuth,
  name: "add_contact_to_list",
  displayName: "Add Contact to List",
  description: "Adds a single contact to an INBOX contact list. Creates the contact if the email is new, otherwise updates the existing contact and ensures it is part of the list.",
  audience: "both",
  aiMetadata: {
    description: "Adds a contact (by email) to a specific INBOX contact list, creating the contact if the email is new or updating it if it already exists, and ensuring list membership. Use to subscribe or enroll someone into a list. Requires a target list id and an email address; the upsert is keyed on the email so repeating with the same email and list yields the same membership without creating duplicates.",
    idempotent: !0
  },
  props: {
    listId: useinboxProps.contactListDropdown(),
    email: import_pieces_framework3.Property.ShortText({
      displayName: "Email",
      description: "Email address of the contact to add (e.g. jane@example.com).",
      required: !0
    })
    //customFields: useinboxProps.customFieldsArray(),
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o;
    let { listId, email } = context.propsValue, token = await useinboxClient.fetchAccessToken({
      email: context.auth.username,
      password: context.auth.password
    }), response = await useinboxClient.inboxApiCall({
      token,
      service: "inbox",
      method: import_pieces_common3.HttpMethod.POST,
      path: `/contactlists/${listId}/add`,
      body: {
        email
        // ...(formattedCustomFields.length > 0 ? { customFields: formattedCustomFields } : {}),
      }
    }), contact = (_b = (_a = response.body) == null ? void 0 : _a.resultObject) != null ? _b : {};
    return {
      success: (_d = (_c = response.body) == null ? void 0 : _c.resultStatus) != null ? _d : !1,
      result_code: (_f = (_e = response.body) == null ? void 0 : _e.resultCode) != null ? _f : null,
      result_message: (_h = (_g = response.body) == null ? void 0 : _g.resultMessage) != null ? _h : null,
      contact_id: (_i = contact.id) != null ? _i : null,
      email: (_j = contact.email) != null ? _j : email,
      first_name: (_k = contact.firstName) != null ? _k : null,
      last_name: (_l = contact.lastName) != null ? _l : null,
      status: (_m = contact.status) != null ? _m : null,
      created_at: (_n = contact.createTime) != null ? _n : null,
      updated_at: (_o = contact.updateTime) != null ? _o : null,
      list_id: listId
    };
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/actions/create-contact-list.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var createContactListAction = (0, import_pieces_framework4.createAction)({
  auth: useinboxAuth,
  name: "create_contact_list",
  displayName: "Create Contact List",
  description: "Creates a new contact list in INBOX where you can later add contacts.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new, empty contact list in INBOX that contacts can later be added to. Use when a list does not yet exist and one is needed before enrolling contacts; optionally nest it under a group and tag it with a consent legislation basis (GDPR, CASL, CAN-SPAM, or none). Requires a list name. Not idempotent: each call creates a separate list even if the name matches an existing one.",
    idempotent: !1
  },
  props: {
    listName: import_pieces_framework4.Property.ShortText({
      displayName: "List Name",
      description: 'Name of the new contact list (e.g. "Newsletter Subscribers").',
      required: !0
    }),
    groupId: useinboxProps.groupDropdown({
      description: "Optional. Place the list inside this group to keep things organised. Leave empty to keep it ungrouped."
    }),
    legislation: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Legislation",
      description: "Legal basis for collecting these contacts. Pick the regulation your audience is covered by; this affects what consent text INBOX shows.",
      required: !1,
      defaultValue: 0,
      options: {
        options: [
          { label: "None", value: 0 },
          { label: "GDPR (EU)", value: 1 },
          { label: "CASL (Canada)", value: 2 },
          { label: "CAN-SPAM (US)", value: 3 }
        ]
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q;
    let { listName, groupId, legislation } = context.propsValue, token = await useinboxClient.fetchAccessToken({
      email: context.auth.username,
      password: context.auth.password
    }), response = await useinboxClient.inboxApiCall({
      token,
      service: "inbox",
      method: import_pieces_common4.HttpMethod.POST,
      path: "/contactlists",
      body: __spreadProps(__spreadValues({
        listName
      }, groupId ? { groupId } : {}), {
        legislation: legislation != null ? legislation : 0
      })
    }), list = (_b = (_a = response.body) == null ? void 0 : _a.resultObject) != null ? _b : {};
    return {
      success: (_d = (_c = response.body) == null ? void 0 : _c.resultStatus) != null ? _d : !1,
      result_code: (_f = (_e = response.body) == null ? void 0 : _e.resultCode) != null ? _f : null,
      result_message: (_h = (_g = response.body) == null ? void 0 : _g.resultMessage) != null ? _h : null,
      list_id: (_i = list.id) != null ? _i : null,
      list_name: (_j = list.listName) != null ? _j : listName,
      group_id: (_l = (_k = list.groupId) != null ? _k : groupId) != null ? _l : null,
      legislation: (_n = (_m = list.legislation) != null ? _m : legislation) != null ? _n : 0,
      total_contacts: (_o = list.totalContacts) != null ? _o : 0,
      created_at: (_p = list.createTime) != null ? _p : null,
      updated_at: (_q = list.updateTime) != null ? _q : null
    };
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/actions/update-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateContactAction = (0, import_pieces_framework5.createAction)({
  auth: useinboxAuth,
  name: "update_contact",
  displayName: "Update Contact",
  description: "Updates an existing INBOX contact. You can add or replace tags and set custom field values.",
  audience: "both",
  aiMetadata: {
    description: "Updates an existing INBOX contact identified by its contact id, setting its tags. Use to modify a known contact rather than create one. Requires the contact id; applying the same tags again leaves the contact in the same state.",
    idempotent: !0
  },
  props: {
    contactId: useinboxProps.contactDropdown(),
    tags: import_pieces_framework5.Property.Array({
      displayName: "Tags",
      description: 'Optional. Tags to attach to the contact (e.g. "vip", "newsletter"). Leave empty to keep existing tags.',
      required: !1
    })
    // customFields: useinboxProps.customFieldsArray(),
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o;
    let { contactId, tags } = context.propsValue, token = await useinboxClient.fetchAccessToken({
      email: context.auth.username,
      password: context.auth.password
    }), body = {};
    Array.isArray(tags) && tags.length > 0 && (body.tags = tags);
    let response = await useinboxClient.inboxApiCall({
      token,
      service: "inbox",
      method: import_pieces_common5.HttpMethod.POST,
      path: `/contacts/${contactId}`,
      body
    }), contact = (_b = (_a = response.body) == null ? void 0 : _a.resultObject) != null ? _b : {};
    return {
      success: (_d = (_c = response.body) == null ? void 0 : _c.resultStatus) != null ? _d : !1,
      result_code: (_f = (_e = response.body) == null ? void 0 : _e.resultCode) != null ? _f : null,
      result_message: (_h = (_g = response.body) == null ? void 0 : _g.resultMessage) != null ? _h : null,
      contact_id: (_i = contact.id) != null ? _i : contactId,
      email: (_j = contact.email) != null ? _j : null,
      first_name: (_k = contact.firstName) != null ? _k : null,
      last_name: (_l = contact.lastName) != null ? _l : null,
      status: (_m = contact.status) != null ? _m : null,
      tags: Array.isArray(contact.tags) ? contact.tags.join(", ") : null,
      created_at: (_n = contact.createTime) != null ? _n : null,
      updated_at: (_o = contact.updateTime) != null ? _o : null
    };
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/actions/unsubscribe-contact.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var unsubscribeContactAction = (0, import_pieces_framework6.createAction)({
  auth: useinboxAuth,
  name: "unsubscribe_contact",
  displayName: "Mark Contact as Unsubscribed",
  description: "Changes a contact status to Unsubscribed so they stop receiving campaigns from INBOX. You can also mark them as Hard Bounce or Spam Reported.",
  audience: "both",
  aiMetadata: {
    description: "Sets an existing INBOX contact's deliverability status, defaulting to Unsubscribed so they stop receiving campaigns; the status can instead be set to Hard Bounce or Spam Reported. Use to suppress a contact or record a deliverability outcome. Requires the contact id; setting the same status again is a no-op.",
    idempotent: !0
  },
  props: {
    contactId: useinboxProps.contactDropdown(),
    status: import_pieces_framework6.Property.StaticDropdown({
      displayName: "New Status",
      description: "The status to apply to the contact.",
      required: !0,
      defaultValue: 3,
      options: {
        options: [
          { label: "Unsubscribed", value: 3 },
          { label: "Hard Bounce", value: 2 },
          { label: "Spam Reported", value: 4 }
        ]
      }
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l;
    let { contactId, status } = context.propsValue, token = await useinboxClient.fetchAccessToken({
      email: context.auth.username,
      password: context.auth.password
    }), response = await useinboxClient.inboxApiCall({
      token,
      service: "inbox",
      method: import_pieces_common6.HttpMethod.PATCH,
      path: `/contacts/${contactId}/status`,
      body: { status }
    }), contact = (_b = (_a = response.body) == null ? void 0 : _a.resultObject) != null ? _b : {};
    return {
      success: (_d = (_c = response.body) == null ? void 0 : _c.resultStatus) != null ? _d : !1,
      result_code: (_f = (_e = response.body) == null ? void 0 : _e.resultCode) != null ? _f : null,
      result_message: (_h = (_g = response.body) == null ? void 0 : _g.resultMessage) != null ? _h : null,
      contact_id: (_i = contact.id) != null ? _i : contactId,
      email: (_j = contact.email) != null ? _j : null,
      status: (_k = contact.status) != null ? _k : status,
      updated_at: (_l = contact.updateTime) != null ? _l : null
    };
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/actions/create-campaign.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var createCampaignAction = (0, import_pieces_framework7.createAction)({
  auth: useinboxAuth,
  name: "create_campaign",
  displayName: "Create Campaign",
  description: "Schedules an INBOX campaign that sends a saved newsletter to one or more contact lists.",
  audience: "both",
  aiMetadata: {
    description: "Creates and schedules an INBOX campaign that sends an existing saved newsletter from a verified sender to one or more contact lists, optionally at a future planned time. Use to broadcast a newsletter to lists. Requires a sender, a newsletter id, and at least one target list; the newsletter must already exist. Not idempotent: each call creates a new campaign.",
    idempotent: !1
  },
  props: {
    senderAccountId: useinboxProps.senderDropdown({
      displayName: "Sender",
      description: "The verified sender the campaign emails are sent from. Add senders under Settings > Senders in INBOX."
    }),
    newsletterId: import_pieces_framework7.Property.ShortText({
      displayName: "Newsletter ID",
      description: 'The ID of the saved newsletter to send in the campaign. You can find newsletter IDs by using the "List Newsletters" action or by looking at the URL when editing a newsletter in INBOX (e.g. https://app.inboxroad.com/newsletters/12345/edit, where 12345 is the newsletter ID).',
      required: !0
    }),
    lists: useinboxProps.multiselectContactListDropdown({
      displayName: "Contact Lists",
      description: "Select one or more INBOX contact lists. Lists are managed under Contacts > Lists in your dashboard."
    }),
    plannedTime: import_pieces_framework7.Property.DateTime({
      displayName: "Scheduled Send Time",
      description: "Optional. When to send the campaign. Leave empty to use INBOX defaults. Use a future date/time.",
      required: !1
    }),
    notifyWhenStart: import_pieces_framework7.Property.Checkbox({
      displayName: "Notify when campaign starts",
      description: "If enabled, INBOX emails you when the campaign starts sending.",
      required: !1,
      defaultValue: !1
    }),
    notifyWhenEnd: import_pieces_framework7.Property.Checkbox({
      displayName: "Notify when campaign ends",
      description: "If enabled, INBOX emails you when the campaign finishes sending.",
      required: !1,
      defaultValue: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j, _k, _l, _m, _n, _o, _p, _q, _r;
    let {
      senderAccountId,
      newsletterId,
      lists,
      plannedTime,
      notifyWhenStart,
      notifyWhenEnd
    } = context.propsValue, token = await useinboxClient.fetchAccessToken({
      email: context.auth.username,
      password: context.auth.password
    }), response = await useinboxClient.inboxApiCall({
      token,
      service: "inbox",
      method: import_pieces_common7.HttpMethod.POST,
      path: "/campaigns",
      body: __spreadProps(__spreadValues({
        senderAccountId,
        newsletterId,
        listType: 2,
        lists
      }, plannedTime ? { plannedTime } : {}), {
        notifyWhenStart: notifyWhenStart != null ? notifyWhenStart : !1,
        notifyWhenEnd: notifyWhenEnd != null ? notifyWhenEnd : !1
      })
    }), campaign = (_b = (_a = response.body) == null ? void 0 : _a.resultObject) != null ? _b : {};
    return {
      success: (_d = (_c = response.body) == null ? void 0 : _c.resultStatus) != null ? _d : !1,
      result_code: (_f = (_e = response.body) == null ? void 0 : _e.resultCode) != null ? _f : null,
      result_message: (_h = (_g = response.body) == null ? void 0 : _g.resultMessage) != null ? _h : null,
      campaign_id: (_i = campaign.id) != null ? _i : null,
      name: (_j = campaign.name) != null ? _j : null,
      subject: (_k = campaign.subject) != null ? _k : null,
      sender_id: (_l = campaign.senderAccountId) != null ? _l : senderAccountId,
      newsletter_id: (_m = campaign.newsletterId) != null ? _m : newsletterId,
      status: (_n = campaign.status) != null ? _n : null,
      planned_time: (_p = (_o = campaign.plannedTime) != null ? _o : plannedTime) != null ? _p : null,
      created_at: (_q = campaign.createTime) != null ? _q : null,
      updated_at: (_r = campaign.updateTime) != null ? _r : null
    };
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/actions/send-transactional-email.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var sendTransactionalEmailAction = (0, import_pieces_framework8.createAction)({
  auth: useinboxAuth,
  name: "send_transactional_email",
  displayName: "Send Transactional Email",
  description: "Sends a one-off transactional email through INBOX Notify (e.g. password reset, receipt, welcome).",
  audience: "both",
  aiMetadata: {
    description: "Sends a single one-off transactional HTML email to one recipient through INBOX Notify, for messages like password resets, receipts, or welcome emails. Use for direct triggered email rather than list campaigns. Requires from name, from email, recipient, subject, and HTML body; the from-email domain must already be authenticated in INBOX Notify. Optional merge-field values replace #key# placeholders in the HTML. Not idempotent: each call sends another email.",
    idempotent: !1
  },
  props: {
    fromName: import_pieces_framework8.Property.ShortText({
      displayName: "From Name",
      description: 'The display name shown to recipients (e.g. "Acme Support").',
      required: !0
    }),
    fromEmail: import_pieces_framework8.Property.ShortText({
      displayName: "From Email",
      description: "Verified sender email. The domain must already be authenticated in INBOX Notify, otherwise the email will be rejected.",
      required: !0
    }),
    toEmail: import_pieces_framework8.Property.ShortText({
      displayName: "To Email",
      description: "Email address of the recipient.",
      required: !0
    }),
    subject: import_pieces_framework8.Property.ShortText({
      displayName: "Subject",
      description: "Email subject line.",
      required: !0
    }),
    htmlContent: import_pieces_framework8.Property.LongText({
      displayName: "HTML Content",
      description: "The HTML body of the email. You can reference INBOX merge fields with `#fieldName#` and provide values in Custom Fields below.",
      required: !0
    }),
    customFields: import_pieces_framework8.Property.Object({
      displayName: "Merge Field Values",
      description: 'Optional. Key-value pairs that replace `#key#` placeholders in your HTML (e.g. `{ "name": "Jane", "code": "12345" }`).',
      required: !1
    })
  },
  async run(context) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _i, _j;
    let { fromName, fromEmail, toEmail, subject, htmlContent, customFields } = context.propsValue, token = await useinboxClient.fetchAccessToken({
      email: context.auth.username,
      password: context.auth.password
    }), response = await useinboxClient.inboxApiCall({
      token,
      service: "notify",
      method: import_pieces_common8.HttpMethod.POST,
      path: "/send",
      body: __spreadValues({
        from: { name: fromName, email: fromEmail },
        to: [{ email: toEmail }],
        subject,
        htmlContent
      }, customFields && Object.keys(customFields).length > 0 ? { customFields } : {})
    }), result = (_b = (_a = response.body) == null ? void 0 : _a.resultObject) != null ? _b : {};
    return {
      success: (_d = (_c = response.body) == null ? void 0 : _c.resultStatus) != null ? _d : !1,
      result_code: (_f = (_e = response.body) == null ? void 0 : _e.resultCode) != null ? _f : null,
      result_message: (_h = (_g = response.body) == null ? void 0 : _g.resultMessage) != null ? _h : null,
      message_id: (_i = result.messageId) != null ? _i : null,
      queued_at: (_j = result.queuedAt) != null ? _j : null,
      accepted: Array.isArray(result.accepted) ? result.accepted.join(", ") : null,
      rejected: Array.isArray(result.rejected) ? result.rejected.join(", ") : null,
      to_email: toEmail,
      from_email: fromEmail,
      subject
    };
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/triggers/new-contact.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common9.DedupeStrategy.TIMEBASED,
  items: async ({ auth }) => {
    var _a, _b, _c;
    let token = await useinboxClient.fetchAccessToken({
      email: auth.username,
      password: auth.password
    });
    return ((_c = (_b = (_a = (await useinboxClient.inboxApiCall({
      token,
      service: "inbox",
      method: import_pieces_common9.HttpMethod.GET,
      path: "/contacts"
    })).body) == null ? void 0 : _a.resultObject) == null ? void 0 : _b.items) != null ? _c : []).filter((c) => !!c.createTime).map((c) => {
      var _a2, _b2, _c2, _d, _e;
      return {
        epochMilliSeconds: new Date(c.createTime).getTime(),
        data: {
          contact_id: c.id,
          email: c.email,
          first_name: (_a2 = c.firstName) != null ? _a2 : null,
          last_name: (_b2 = c.lastName) != null ? _b2 : null,
          status: (_c2 = c.status) != null ? _c2 : null,
          tags: Array.isArray(c.tags) ? c.tags.join(", ") : null,
          created_at: (_d = c.createTime) != null ? _d : null,
          updated_at: (_e = c.updateTime) != null ? _e : null
        }
      };
    });
  }
}, newContactTrigger = (0, import_pieces_framework9.createTrigger)({
  auth: useinboxAuth,
  name: "new_contact",
  displayName: "New Contact",
  description: "Triggers when a new contact is added to your INBOX account (through a form, manual entry, import, or API).",
  aiMetadata: {
    description: "Fires when a new contact appears in the INBOX account, regardless of how it was added (signup form, manual entry, import, or API). Represents a newly created contact record and emits its details."
  },
  props: {},
  sampleData: {
    contact_id: "5df9d4b691183c000106cb90",
    email: "jane@example.com",
    first_name: "Jane",
    last_name: "Doe",
    status: 1,
    tags: "newsletter, vip",
    created_at: "2024-01-15T10:30:00.000Z",
    updated_at: "2024-01-15T10:30:00.000Z"
  },
  type: import_pieces_framework9.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common9.pollingHelper.test(polling, context);
  },
  async onEnable(context) {
    await import_pieces_common9.pollingHelper.onEnable(polling, context);
  },
  async onDisable(context) {
    await import_pieces_common9.pollingHelper.onDisable(polling, context);
  },
  async run(context) {
    return await import_pieces_common9.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/triggers/new-campaign.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common10.DedupeStrategy.TIMEBASED,
  items: async ({ auth }) => {
    var _a, _b, _c;
    let token = await useinboxClient.fetchAccessToken({
      email: auth.username,
      password: auth.password
    });
    return ((_c = (_b = (_a = (await useinboxClient.inboxApiCall({
      token,
      service: "inbox",
      method: import_pieces_common10.HttpMethod.GET,
      path: "/campaigns"
    })).body) == null ? void 0 : _a.resultObject) == null ? void 0 : _b.items) != null ? _c : []).filter((c) => !!c.createTime).map((c) => {
      var _a2, _b2, _c2, _d, _e, _f, _g, _h;
      return {
        epochMilliSeconds: new Date(c.createTime).getTime(),
        data: {
          campaign_id: c.id,
          name: (_a2 = c.name) != null ? _a2 : null,
          subject: (_b2 = c.subject) != null ? _b2 : null,
          sender_id: (_c2 = c.senderAccountId) != null ? _c2 : null,
          newsletter_id: (_d = c.newsletterId) != null ? _d : null,
          status: (_e = c.status) != null ? _e : null,
          planned_time: (_f = c.plannedTime) != null ? _f : null,
          created_at: (_g = c.createTime) != null ? _g : null,
          updated_at: (_h = c.updateTime) != null ? _h : null
        }
      };
    });
  }
}, newCampaignTrigger = (0, import_pieces_framework10.createTrigger)({
  auth: useinboxAuth,
  name: "new_campaign",
  displayName: "New Campaign",
  description: "Triggers when a new campaign is created in your INBOX account.",
  aiMetadata: {
    description: "Fires when a new campaign is created in the INBOX account. Represents a newly created campaign record and emits its details."
  },
  props: {},
  sampleData: {
    campaign_id: "5df9d4b691183c000106cb90",
    name: "Spring Newsletter",
    subject: "Our biggest spring sale yet",
    sender_id: "5dd582ff648e4e0001db6171",
    newsletter_id: "5def48547bef2800014e94f1",
    status: 1,
    planned_time: "2024-04-01T09:00:00.000Z",
    created_at: "2024-03-25T10:30:00.000Z",
    updated_at: "2024-03-25T10:30:00.000Z"
  },
  type: import_pieces_framework10.TriggerStrategy.POLLING,
  async test(context) {
    return await import_pieces_common10.pollingHelper.test(polling2, context);
  },
  async onEnable(context) {
    await import_pieces_common10.pollingHelper.onEnable(polling2, context);
  },
  async onDisable(context) {
    await import_pieces_common10.pollingHelper.onDisable(polling2, context);
  },
  async run(context) {
    return await import_pieces_common10.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/common/webhook-factory.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common");
var STORE_KEY_PREFIX = "useinbox_notify_webhook_";
function createNotifyWebhookTrigger({
  event,
  name,
  displayName,
  description,
  aiMetadata,
  sampleData
}) {
  return (0, import_pieces_framework11.createTrigger)({
    auth: useinboxAuth,
    name,
    displayName,
    description,
    aiMetadata,
    props: {},
    sampleData,
    type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
    async onEnable(context) {
      var _a, _b;
      let token = await useinboxClient.fetchAccessToken({
        email: context.auth.username,
        password: context.auth.password
      }), webhookId = (_b = (_a = (await useinboxClient.inboxApiCall({
        token,
        service: "notify",
        method: import_pieces_common11.HttpMethod.POST,
        path: "/webhooks",
        body: {
          url: context.webhookUrl,
          description: `Activepieces ${displayName}`,
          events: [event.id]
        }
      })).body) == null ? void 0 : _a.resultObject) == null ? void 0 : _b.id;
      webhookId && await context.store.put(`${STORE_KEY_PREFIX}${name}`, webhookId);
    },
    async onDisable(context) {
      let webhookId = await context.store.get(`${STORE_KEY_PREFIX}${name}`);
      if (webhookId)
        try {
          let token = await useinboxClient.fetchAccessToken({
            email: context.auth.username,
            password: context.auth.password
          });
          await useinboxClient.inboxApiCall({
            token,
            service: "notify",
            method: import_pieces_common11.HttpMethod.DELETE,
            path: `/webhooks/${webhookId}`
          });
        } catch (e) {
        }
    },
    async run(context) {
      return [context.payload.body];
    }
  });
}

// ../ap/packages/pieces/community/useinbox/src/lib/triggers/email-bounced.ts
var emailBouncedTrigger = createNotifyWebhookTrigger({
  event: { id: 0, key: "hard_bounce" },
  name: "email_bounced",
  displayName: "Email Bounced",
  description: "Triggers when a transactional email permanently bounces (hard bounce) \u2014 the recipient address does not exist or is blocked.",
  aiMetadata: {
    description: "Fires when a transactional email sent through INBOX Notify hard-bounces, meaning the recipient address is permanently undeliverable (does not exist or is blocked). Represents a permanent delivery failure for a specific message and recipient."
  },
  sampleData: {
    event: "HardBounce",
    eventId: 0,
    messageId: "5df9d4b691183c000106cb90",
    email: "jane@example.com",
    occurredAt: "2024-01-15T10:30:00.000Z",
    reason: "550 5.1.1 User unknown"
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/triggers/email-spam-reported.ts
var emailSpamReportedTrigger = createNotifyWebhookTrigger({
  event: { id: 2, key: "spam_reported" },
  name: "email_spam_reported",
  displayName: "Email Spam Reported",
  description: "Triggers when a recipient marks a transactional email as spam in their inbox.",
  aiMetadata: {
    description: "Fires when a recipient marks a transactional email sent through INBOX Notify as spam. Represents a spam complaint for a specific message and recipient."
  },
  sampleData: {
    event: "SpamReported",
    eventId: 2,
    messageId: "5df9d4b691183c000106cb90",
    email: "jane@example.com",
    occurredAt: "2024-01-15T10:30:00.000Z"
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/triggers/email-opened.ts
var emailOpenedTrigger = createNotifyWebhookTrigger({
  event: { id: 5, key: "opened" },
  name: "email_opened",
  displayName: "Email Opened",
  description: "Triggers each time a transactional email is opened by the recipient. Fires on every open (not unique).",
  aiMetadata: {
    description: "Fires each time a recipient opens a transactional email sent through INBOX Notify. Fires on every open, not only the first, so the same message and recipient can trigger it repeatedly."
  },
  sampleData: {
    event: "Opened",
    eventId: 5,
    messageId: "5df9d4b691183c000106cb90",
    email: "jane@example.com",
    occurredAt: "2024-01-15T10:30:00.000Z",
    userAgent: "Mozilla/5.0",
    ip: "203.0.113.42"
  }
});

// ../ap/packages/pieces/community/useinbox/src/lib/triggers/email-clicked.ts
var emailClickedTrigger = createNotifyWebhookTrigger({
  event: { id: 7, key: "clicked" },
  name: "email_clicked",
  displayName: "Email Link Clicked",
  description: "Triggers when a recipient clicks a tracked link inside a transactional email.",
  aiMetadata: {
    description: "Fires when a recipient clicks a tracked link inside a transactional email sent through INBOX Notify. Represents a link-click engagement event and emits the clicked URL; can fire multiple times for the same message and recipient."
  },
  sampleData: {
    event: "Clicked",
    eventId: 7,
    messageId: "5df9d4b691183c000106cb90",
    email: "jane@example.com",
    occurredAt: "2024-01-15T10:30:00.000Z",
    url: "https://example.com/promo",
    userAgent: "Mozilla/5.0",
    ip: "203.0.113.42"
  }
});

// ../ap/packages/pieces/community/useinbox/src/index.ts
var useinbox = (0, import_pieces_framework12.createPiece)({
  displayName: "Inbox",
  description: "Email marketing and transactional email automation with INBOX (useinbox.com). Manage contacts, lists, campaigns, and react to delivery events.",
  auth: useinboxAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/useinbox.png",
  categories: [import_pieces_framework13.PieceCategory.MARKETING, import_pieces_framework13.PieceCategory.COMMUNICATION],
  authors: ["sanket-a11y"],
  actions: [
    addContactToListAction,
    createContactListAction,
    updateContactAction,
    unsubscribeContactAction,
    createCampaignAction,
    sendTransactionalEmailAction,
    (0, import_pieces_common12.createCustomApiCallAction)({
      baseUrl: () => useinboxClient.ACCOUNT_BASE_URL,
      auth: useinboxAuth,
      authMapping: async (auth) => {
        let credentials = auth;
        return {
          Authorization: `Bearer ${await useinboxClient.fetchAccessToken({
            email: credentials.username,
            password: credentials.password
          })}`
        };
      }
    })
  ],
  triggers: [
    newContactTrigger,
    newCampaignTrigger,
    emailBouncedTrigger,
    emailSpamReportedTrigger,
    emailOpenedTrigger,
    emailClickedTrigger
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  useinbox,
  useinboxAuth
});
