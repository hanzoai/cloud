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

// ../ap/packages/pieces/community/missive/src/index.ts
var index_exports = {};
__export(index_exports, {
  missive: () => missive
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework13 = require("@activepieces/pieces-framework"), import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/missive/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), missiveAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Token",
  required: !0,
  description: `
    To get your API token:
    
    1. Go to your Missive preferences
    2. Click the API tab  
    3. Click "Create a new token"
    4. Copy the generated token (starts with 'missive_pat-')
    
    Note: You need to be part of an organization subscribed to the Productive plan to generate API tokens.
    `,
  validate: async ({ auth }) => {
    try {
      return (await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://public.missiveapp.com/v1/organizations",
        authentication: {
          type: import_pieces_common.AuthenticationType.BEARER_TOKEN,
          token: auth
        }
      })).status === 200 ? {
        valid: !0
      } : {
        valid: !1,
        error: "Invalid API token. Please check your token and try again."
      };
    } catch (error) {
      return {
        valid: !1,
        error: "Failed to validate API token. Please check your token and try again."
      };
    }
  }
});

// ../ap/packages/pieces/community/missive/src/lib/actions/create-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/missive/src/lib/common/client.ts
var import_pieces_common2 = require("@activepieces/pieces-common"), MISSIVE_API_BASE_URL = "https://public.missiveapp.com/v1", missiveCommon = {
  baseUrl: MISSIVE_API_BASE_URL,
  async apiCall({
    auth,
    method,
    resourceUri,
    body = void 0,
    queryParams = void 0
  }) {
    return await import_pieces_common2.httpClient.sendRequest({
      method,
      url: `${MISSIVE_API_BASE_URL}${resourceUri}`,
      body,
      queryParams,
      authentication: {
        type: import_pieces_common2.AuthenticationType.BEARER_TOKEN,
        token: auth.secret_text
      }
    });
  }
};

// ../ap/packages/pieces/community/missive/src/lib/common/dynamic-dropdowns.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var contactBookDropdown = import_pieces_framework2.Property.Dropdown({
  auth: missiveAuth,
  displayName: "Contact Book",
  description: "Select the contact book where the contact will be created",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (((_a = (await missiveCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: "/contact_books"
        })).body) == null ? void 0 : _a.contact_books) || []).map((book) => ({
          label: book.name,
          value: book.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load contact books",
        options: []
      };
    }
  }
}), organizationDropdown = import_pieces_framework2.Property.Dropdown({
  auth: missiveAuth,
  displayName: "Organization",
  description: "Select an organization",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (((_a = (await missiveCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: "/organizations"
        })).body) == null ? void 0 : _a.organizations) || []).map((org) => ({
          label: org.name,
          value: org.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load organizations",
        options: []
      };
    }
  }
}), groupDropdown = import_pieces_framework2.Property.Dropdown({
  auth: missiveAuth,
  displayName: "Group",
  description: "Select a group",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (((_a = (await missiveCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: "/groups"
        })).body) == null ? void 0 : _a.groups) || []).map((group) => ({
          label: group.name,
          value: group.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load groups",
        options: []
      };
    }
  }
}), contactGroupDropdown = import_pieces_framework2.Property.Dropdown({
  auth: missiveAuth,
  displayName: "Contact Group",
  description: "Select a contact group or organization",
  required: !1,
  refreshers: ["contact_book", "group_kind"],
  options: async ({ auth, contact_book, group_kind }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    if (!contact_book)
      return {
        disabled: !0,
        placeholder: "Please select a contact book first",
        options: []
      };
    if (!group_kind)
      return {
        disabled: !0,
        placeholder: "Please select a group type first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (((_a = (await missiveCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: `/contact_groups?contact_book=${contact_book}&kind=${group_kind}`
        })).body) == null ? void 0 : _a.contact_groups) || []).map((group) => ({
          label: group.name,
          value: group.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load contact groups",
        options: []
      };
    }
  }
}), optionalContactBookDropdown = import_pieces_framework2.Property.Dropdown({
  auth: missiveAuth,
  displayName: "Contact Book",
  description: "Contact book to search within (optional)",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (((_a = (await missiveCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: "/contact_books"
        })).body) == null ? void 0 : _a.contact_books) || []).map((book) => ({
          label: book.name,
          value: book.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load contact books",
        options: []
      };
    }
  }
}), contactDropdown = import_pieces_framework2.Property.Dropdown({
  auth: missiveAuth,
  displayName: "Contact",
  description: "Select a contact",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (((_a = (await missiveCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: "/contacts?limit=200"
        })).body) == null ? void 0 : _a.contacts) || []).map((contact) => {
          var _a2, _b;
          let name = [contact.first_name, contact.last_name].filter(Boolean).join(" ") || "Unnamed Contact", email = (_b = (_a2 = contact.infos) == null ? void 0 : _a2.find((info) => info.kind === "email")) == null ? void 0 : _b.value;
          return {
            label: email ? `${name} (${email})` : name,
            value: contact.id
          };
        })
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load contacts",
        options: []
      };
    }
  }
}), teamDropdown = import_pieces_framework2.Property.Dropdown({
  auth: missiveAuth,
  displayName: "Team",
  description: "Select a team",
  required: !1,
  refreshers: ["organization"],
  options: async ({ auth, organization }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    if (!organization)
      return {
        disabled: !0,
        placeholder: "Please select an organization first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (((_a = (await missiveCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: `/teams?organization=${organization}`
        })).body) == null ? void 0 : _a.teams) || []).map((team) => ({
          label: team.name,
          value: team.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load teams",
        options: []
      };
    }
  }
}), sharedLabelDropdown = import_pieces_framework2.Property.Dropdown({
  auth: missiveAuth,
  displayName: "Shared Label",
  description: "Select a shared label",
  required: !1,
  refreshers: ["organization"],
  options: async ({ auth, organization }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    if (!organization)
      return {
        disabled: !0,
        placeholder: "Please select an organization first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (((_a = (await missiveCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: `/shared_labels?organization=${organization}`
        })).body) == null ? void 0 : _a.shared_labels) || []).map((label) => ({
          label: label.name,
          value: label.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load shared labels",
        options: []
      };
    }
  }
}), userDropdown = import_pieces_framework2.Property.Dropdown({
  auth: missiveAuth,
  displayName: "User",
  description: "Select a user",
  required: !1,
  refreshers: ["organization"],
  options: async ({ auth, organization }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    if (!organization)
      return {
        disabled: !0,
        placeholder: "Please select an organization first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (((_a = (await missiveCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: `/users?organization=${organization}`
        })).body) == null ? void 0 : _a.users) || []).map((user) => ({
          label: `${user.name} (${user.email})`,
          value: user.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load users",
        options: []
      };
    }
  }
}), membershipOrganizationDropdown = import_pieces_framework2.Property.Dropdown({
  auth: missiveAuth,
  displayName: "Organization",
  description: "Select an organization for membership",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (((_a = (await missiveCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: "/organizations"
        })).body) == null ? void 0 : _a.organizations) || []).map((org) => ({
          label: org.name,
          value: org.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load organizations",
        options: []
      };
    }
  }
}), membershipContactGroupDropdown = import_pieces_framework2.Property.Dropdown({
  auth: missiveAuth,
  displayName: "Contact Group",
  description: "Select a contact group",
  required: !1,
  refreshers: ["contact_book", "group_kind"],
  options: async ({ auth, contact_book, group_kind }) => {
    var _a;
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    if (!contact_book)
      return {
        disabled: !0,
        placeholder: "Please select a contact book first",
        options: []
      };
    if (!group_kind)
      return {
        disabled: !0,
        placeholder: "Please select a group type first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (((_a = (await missiveCommon.apiCall({
          auth,
          method: import_pieces_common3.HttpMethod.GET,
          resourceUri: `/contact_groups?contact_book=${contact_book}&kind=${group_kind}`
        })).body) == null ? void 0 : _a.contact_groups) || []).map((group) => ({
          label: group.name,
          value: group.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load contact groups",
        options: []
      };
    }
  }
});

// ../ap/packages/pieces/community/missive/src/lib/actions/create-contact.ts
var createContact = (0, import_pieces_framework3.createAction)({
  name: "create_contact",
  displayName: "Create Contact",
  description: "Add a new contact within a specified contact book",
  audience: "both",
  aiMetadata: { description: "Create a new contact in a given Missive contact book, optionally with names, phone/email/address infos, and organization/group memberships. Use to add a person to Missive contacts. Not idempotent: each call creates a separate contact even with identical input; to change an existing one use Update Contact.", idempotent: !1 },
  auth: missiveAuth,
  props: {
    contact_book: contactBookDropdown,
    first_name: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "First name of the contact",
      required: !1
    }),
    last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the contact",
      required: !1
    }),
    middle_name: import_pieces_framework3.Property.ShortText({
      displayName: "Middle Name",
      description: "Middle name of the contact",
      required: !1
    }),
    phonetic_first_name: import_pieces_framework3.Property.ShortText({
      displayName: "Phonetic First Name",
      description: "Phonetic spelling of the first name",
      required: !1
    }),
    phonetic_last_name: import_pieces_framework3.Property.ShortText({
      displayName: "Phonetic Last Name",
      description: "Phonetic spelling of the last name",
      required: !1
    }),
    phonetic_middle_name: import_pieces_framework3.Property.ShortText({
      displayName: "Phonetic Middle Name",
      description: "Phonetic spelling of the middle name",
      required: !1
    }),
    prefix: import_pieces_framework3.Property.ShortText({
      displayName: "Prefix",
      description: "Name prefix (e.g., Mr., Mrs., Dr.)",
      required: !1
    }),
    suffix: import_pieces_framework3.Property.ShortText({
      displayName: "Suffix",
      description: "Name suffix (e.g., Jr., Sr., III)",
      required: !1
    }),
    nickname: import_pieces_framework3.Property.ShortText({
      displayName: "Nickname",
      description: "Nickname for the contact",
      required: !1
    }),
    file_as: import_pieces_framework3.Property.ShortText({
      displayName: "File As",
      description: "How the contact should be filed/sorted",
      required: !1
    }),
    notes: import_pieces_framework3.Property.LongText({
      displayName: "Notes",
      description: "Additional notes about the contact",
      required: !1
    }),
    starred: import_pieces_framework3.Property.Checkbox({
      displayName: "Starred",
      description: "Whether the contact should be starred",
      required: !1,
      defaultValue: !1
    }),
    gender: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Gender",
      description: "Gender of the contact",
      required: !1,
      options: {
        options: [
          { label: "Male", value: "Male" },
          { label: "Female", value: "Female" },
          { label: "Other", value: "Other" }
        ]
      }
    }),
    infos: import_pieces_framework3.Property.Array({
      displayName: "Contact Information",
      description: "Email addresses, phone numbers, and other contact information",
      required: !1,
      properties: {
        kind: import_pieces_framework3.Property.StaticDropdown({
          displayName: "Type",
          description: "Type of contact information",
          required: !0,
          options: {
            options: [
              { label: "Email", value: "email" },
              { label: "Phone Number", value: "phone_number" },
              { label: "Twitter", value: "twitter" },
              { label: "Facebook", value: "facebook" },
              { label: "Physical Address", value: "physical_address" },
              { label: "URL", value: "url" },
              { label: "Custom", value: "custom" }
            ]
          }
        }),
        label: import_pieces_framework3.Property.StaticDropdown({
          displayName: "Label",
          description: "Label for contact info. Choose based on Type: Email(home,work,personal,other) | Phone(main,mobile,home,work,fax,pager,other) | Address(work,home,other) | Social(work,personal,other) | URL(homepage,profile,blog,work,personal,other)",
          required: !0,
          options: {
            options: [
              { label: "Home", value: "home" },
              { label: "Work", value: "work" },
              { label: "Personal", value: "personal" },
              { label: "Main", value: "main" },
              { label: "Mobile", value: "mobile" },
              { label: "Home Fax", value: "home_fax" },
              { label: "Work Fax", value: "work_fax" },
              { label: "Other Fax", value: "other_fax" },
              { label: "Pager", value: "pager" },
              { label: "Homepage", value: "homepage" },
              { label: "Profile", value: "profile" },
              { label: "Blog", value: "blog" },
              { label: "Other", value: "other" }
            ]
          }
        }),
        value: import_pieces_framework3.Property.ShortText({
          displayName: "Value",
          description: "The actual contact information value (email, phone, URL, etc.)",
          required: !1
        }),
        custom_label: import_pieces_framework3.Property.ShortText({
          displayName: "Custom Label",
          description: 'Custom label value (only used when label is "other")',
          required: !1
        }),
        name: import_pieces_framework3.Property.ShortText({
          displayName: "Facebook Name",
          description: "Facebook user name (only for facebook type)",
          required: !1
        }),
        street: import_pieces_framework3.Property.ShortText({
          displayName: "Street",
          description: "Street address (only for physical address type)",
          required: !1
        }),
        extended_address: import_pieces_framework3.Property.ShortText({
          displayName: "Extended Address",
          description: "Extended address like apartment, suite (only for physical address type)",
          required: !1
        }),
        city: import_pieces_framework3.Property.ShortText({
          displayName: "City",
          description: "City (only for physical address type)",
          required: !1
        }),
        region: import_pieces_framework3.Property.ShortText({
          displayName: "Region/State",
          description: "Region or state (only for physical address type)",
          required: !1
        }),
        postal_code: import_pieces_framework3.Property.ShortText({
          displayName: "Postal Code",
          description: "Postal code (only for physical address type)",
          required: !1
        }),
        po_box: import_pieces_framework3.Property.ShortText({
          displayName: "PO Box",
          description: "PO Box (only for physical address type)",
          required: !1
        }),
        country: import_pieces_framework3.Property.ShortText({
          displayName: "Country",
          description: "Country (only for physical address type)",
          required: !1
        })
      }
    }),
    memberships: import_pieces_framework3.Property.DynamicProperties({
      auth: missiveAuth,
      displayName: "Memberships",
      description: "Organizations and groups the contact belongs to",
      required: !1,
      refreshers: ["contact_book"],
      props: async ({ auth, contact_book }) => {
        var _a, _b;
        if (!auth)
          return {
            memberships_array: import_pieces_framework3.Property.Array({
              displayName: "Memberships",
              description: "Please authenticate first",
              required: !1,
              properties: {
                placeholder: import_pieces_framework3.Property.ShortText({
                  displayName: "Authenticate First",
                  description: "Please authenticate to access memberships",
                  required: !1
                })
              }
            })
          };
        let organizationOptions = [], contactGroupOptions = [];
        try {
          organizationOptions = (((_a = (await missiveCommon.apiCall({
            auth,
            method: import_pieces_common4.HttpMethod.GET,
            resourceUri: "/organizations"
          })).body) == null ? void 0 : _a.organizations) || []).map((org) => ({
            label: org.name,
            value: org.id
          }));
        } catch (error) {
          console.error("Failed to fetch organizations:", error);
        }
        if (contact_book)
          try {
            contactGroupOptions = (((_b = (await missiveCommon.apiCall({
              auth,
              method: import_pieces_common4.HttpMethod.GET,
              resourceUri: `/contact_groups?contact_book=${contact_book}&kind=group`
            })).body) == null ? void 0 : _b.contact_groups) || []).map((group) => ({
              label: group.name,
              value: group.id
            }));
          } catch (error) {
            console.error("Failed to fetch contact groups:", error);
          }
        return {
          memberships_array: import_pieces_framework3.Property.Array({
            displayName: "Memberships",
            description: "Add, remove, and manage contact memberships",
            required: !1,
            properties: {
              type: import_pieces_framework3.Property.StaticDropdown({
                displayName: "Type",
                description: "Type of membership",
                required: !0,
                options: {
                  options: [
                    { label: "Organization", value: "organization" },
                    { label: "Group", value: "group" }
                  ]
                }
              }),
              organization: import_pieces_framework3.Property.StaticDropdown({
                displayName: "Organization",
                description: "Select organization (only for Organization type)",
                required: !1,
                options: {
                  options: organizationOptions.length > 0 ? organizationOptions : [
                    { label: "No organizations found", value: "" }
                  ]
                }
              }),
              contact_group: import_pieces_framework3.Property.StaticDropdown({
                displayName: "Contact Group",
                description: contact_book ? "Select contact group (only for Group type)" : "Select contact book first to see groups",
                required: !1,
                options: {
                  options: contactGroupOptions.length > 0 ? contactGroupOptions : [
                    { label: contact_book ? "No groups found" : "Select contact book first", value: "" }
                  ]
                }
              }),
              title: import_pieces_framework3.Property.ShortText({
                displayName: "Title",
                description: "Job title or role",
                required: !1
              }),
              location: import_pieces_framework3.Property.ShortText({
                displayName: "Location",
                description: "Location or office",
                required: !1
              }),
              department: import_pieces_framework3.Property.ShortText({
                displayName: "Department",
                description: "Department",
                required: !1
              }),
              description: import_pieces_framework3.Property.ShortText({
                displayName: "Description",
                description: "Description of role or membership",
                required: !1
              })
            }
          })
        };
      }
    })
  },
  async run(context) {
    let propsValue = context.propsValue, {
      contact_book,
      first_name,
      last_name,
      middle_name,
      phonetic_first_name,
      phonetic_last_name,
      phonetic_middle_name,
      prefix,
      suffix,
      nickname,
      file_as,
      notes,
      starred,
      gender,
      infos
    } = propsValue, contactData = {
      contact_book
    };
    first_name && (contactData.first_name = first_name), last_name && (contactData.last_name = last_name), middle_name && (contactData.middle_name = middle_name), phonetic_first_name && (contactData.phonetic_first_name = phonetic_first_name), phonetic_last_name && (contactData.phonetic_last_name = phonetic_last_name), phonetic_middle_name && (contactData.phonetic_middle_name = phonetic_middle_name), prefix && (contactData.prefix = prefix), suffix && (contactData.suffix = suffix), nickname && (contactData.nickname = nickname), file_as && (contactData.file_as = file_as), notes && (contactData.notes = notes), starred !== void 0 && (contactData.starred = starred), gender && (contactData.gender = gender), infos && Array.isArray(infos) && infos.length > 0 && (contactData.infos = infos.map((info) => {
      let infoObj = {
        kind: info.kind,
        label: info.label
      };
      return info.custom_label && info.label === "other" && (infoObj.custom_label = info.custom_label), info.kind === "email" || info.kind === "phone_number" || info.kind === "twitter" || info.kind === "url" || info.kind === "custom" ? infoObj.value = info.value : info.kind === "facebook" ? infoObj.name = info.name : info.kind === "physical_address" && (info.street && (infoObj.street = info.street), info.extended_address && (infoObj.extended_address = info.extended_address), info.city && (infoObj.city = info.city), info.region && (infoObj.region = info.region), info.postal_code && (infoObj.postal_code = info.postal_code), info.po_box && (infoObj.po_box = info.po_box), info.country && (infoObj.country = info.country)), infoObj;
    }));
    let membershipsArray = propsValue.memberships_array;
    if (membershipsArray && Array.isArray(membershipsArray) && membershipsArray.length > 0) {
      let memberships = [];
      for (let membership of membershipsArray) {
        if (!membership.type) continue;
        let membershipObj = {};
        membership.title && (membershipObj.title = membership.title), membership.location && (membershipObj.location = membership.location), membership.department && (membershipObj.department = membership.department), membership.description && (membershipObj.description = membership.description), membership.type === "organization" && membership.organization ? membershipObj.group = {
          kind: "organization",
          id: membership.organization
        } : membership.type === "group" && membership.contact_group && (membershipObj.group = {
          kind: "group",
          id: membership.contact_group
        }), membershipObj.group && memberships.push(membershipObj);
      }
      memberships.length > 0 && (contactData.memberships = memberships);
    }
    return (await missiveCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common4.HttpMethod.POST,
      resourceUri: "/contacts",
      body: {
        contacts: [contactData]
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/missive/src/lib/actions/update-contact.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var updateContact = (0, import_pieces_framework4.createAction)({
  name: "update_contact",
  displayName: "Update Contact",
  description: "Modify fields for an existing contact",
  audience: "both",
  aiMetadata: { description: "Update an existing Missive contact identified by its contact ID, changing names, notes, infos, memberships, or moving it to another contact book. Use when you already have the target contact and want to edit it rather than create one. Note that any provided infos/memberships replace the existing sets rather than merging. Idempotent in effect for a fixed set of fields, but each call mutates the contact.", idempotent: !1 },
  auth: missiveAuth,
  props: {
    contact_book: contactBookDropdown,
    contact_id: import_pieces_framework4.Property.Dropdown({
      auth: missiveAuth,
      displayName: "Contact",
      description: "Select the contact to update",
      required: !0,
      refreshers: ["contact_book"],
      options: async ({ auth, contact_book }) => {
        var _a;
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate first",
            options: []
          };
        if (!contact_book)
          return {
            disabled: !0,
            placeholder: "Please select a contact book first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (((_a = (await missiveCommon.apiCall({
              auth,
              method: import_pieces_common5.HttpMethod.GET,
              resourceUri: `/contacts?contact_book=${contact_book}`
            })).body) == null ? void 0 : _a.contacts) || []).map((contact) => {
              var _a2, _b, _c, _d;
              let label = "";
              contact.first_name || contact.last_name ? label = `${contact.first_name || ""} ${contact.last_name || ""}`.trim() : label = "Unnamed Contact";
              let primaryEmail = (_b = (_a2 = contact.infos) == null ? void 0 : _a2.find((info) => info.kind === "email")) == null ? void 0 : _b.value, primaryPhone = (_d = (_c = contact.infos) == null ? void 0 : _c.find((info) => info.kind === "phone_number")) == null ? void 0 : _d.value;
              return primaryEmail ? label += ` (${primaryEmail})` : primaryPhone && (label += ` (${primaryPhone})`), {
                label,
                value: contact.id
              };
            })
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load contacts",
            options: []
          };
        }
      }
    }),
    move_to_contact_book: import_pieces_framework4.Property.Dropdown({
      displayName: "Move to Contact Book",
      description: "Move contact to a different contact book (optional)",
      required: !1,
      auth: missiveAuth,
      refreshers: [],
      options: async ({ auth }) => {
        var _a;
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (((_a = (await missiveCommon.apiCall({
              auth,
              method: import_pieces_common5.HttpMethod.GET,
              resourceUri: "/contact_books"
            })).body) == null ? void 0 : _a.contact_books) || []).map((book) => ({
              label: book.name,
              value: book.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load contact books",
            options: []
          };
        }
      }
    }),
    first_name: import_pieces_framework4.Property.ShortText({
      displayName: "First Name",
      description: "First name of the contact",
      required: !1
    }),
    last_name: import_pieces_framework4.Property.ShortText({
      displayName: "Last Name",
      description: "Last name of the contact",
      required: !1
    }),
    middle_name: import_pieces_framework4.Property.ShortText({
      displayName: "Middle Name",
      description: "Middle name of the contact",
      required: !1
    }),
    phonetic_first_name: import_pieces_framework4.Property.ShortText({
      displayName: "Phonetic First Name",
      description: "Phonetic spelling of the first name",
      required: !1
    }),
    phonetic_last_name: import_pieces_framework4.Property.ShortText({
      displayName: "Phonetic Last Name",
      description: "Phonetic spelling of the last name",
      required: !1
    }),
    phonetic_middle_name: import_pieces_framework4.Property.ShortText({
      displayName: "Phonetic Middle Name",
      description: "Phonetic spelling of the middle name",
      required: !1
    }),
    prefix: import_pieces_framework4.Property.ShortText({
      displayName: "Prefix",
      description: "Name prefix (e.g., Mr., Mrs., Dr.)",
      required: !1
    }),
    suffix: import_pieces_framework4.Property.ShortText({
      displayName: "Suffix",
      description: "Name suffix (e.g., Jr., Sr., III)",
      required: !1
    }),
    nickname: import_pieces_framework4.Property.ShortText({
      displayName: "Nickname",
      description: "Nickname for the contact",
      required: !1
    }),
    file_as: import_pieces_framework4.Property.ShortText({
      displayName: "File As",
      description: "How the contact should be filed/sorted",
      required: !1
    }),
    notes: import_pieces_framework4.Property.LongText({
      displayName: "Notes",
      description: "Additional notes about the contact",
      required: !1
    }),
    starred: import_pieces_framework4.Property.Checkbox({
      displayName: "Starred",
      description: "Whether the contact should be starred",
      required: !1
    }),
    gender: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Gender",
      description: "Gender of the contact",
      required: !1,
      options: {
        options: [
          { label: "Male", value: "Male" },
          { label: "Female", value: "Female" },
          { label: "Other", value: "Other" }
        ]
      }
    }),
    infos: import_pieces_framework4.Property.Array({
      displayName: "Contact Information",
      description: "Email addresses, phone numbers, and other contact information. Note: When updating infos, all existing infos will be replaced with the ones provided here.",
      required: !1,
      properties: {
        kind: import_pieces_framework4.Property.StaticDropdown({
          displayName: "Type",
          description: "Type of contact information",
          required: !0,
          options: {
            options: [
              { label: "Email", value: "email" },
              { label: "Phone Number", value: "phone_number" },
              { label: "Twitter", value: "twitter" },
              { label: "Facebook", value: "facebook" },
              { label: "Physical Address", value: "physical_address" },
              { label: "URL", value: "url" },
              { label: "Custom", value: "custom" }
            ]
          }
        }),
        label: import_pieces_framework4.Property.StaticDropdown({
          displayName: "Label",
          description: "Label for this contact information",
          required: !0,
          options: {
            options: [
              { label: "Home", value: "home" },
              { label: "Work", value: "work" },
              { label: "Personal", value: "personal" },
              { label: "Mobile", value: "mobile" },
              { label: "Main", value: "main" },
              { label: "Home Fax", value: "home_fax" },
              { label: "Work Fax", value: "work_fax" },
              { label: "Other Fax", value: "other_fax" },
              { label: "Pager", value: "pager" },
              { label: "Homepage", value: "homepage" },
              { label: "Profile", value: "profile" },
              { label: "Blog", value: "blog" },
              { label: "Other", value: "other" }
            ]
          }
        }),
        value: import_pieces_framework4.Property.ShortText({
          displayName: "Value",
          description: "The actual contact information value",
          required: !0
        }),
        custom_label: import_pieces_framework4.Property.ShortText({
          displayName: "Custom Label",
          description: 'Custom label value (use only if label is "other")',
          required: !1
        }),
        street: import_pieces_framework4.Property.ShortText({
          displayName: "Street",
          description: "Street address (for physical address)",
          required: !1
        }),
        extended_address: import_pieces_framework4.Property.ShortText({
          displayName: "Extended Address",
          description: "Extended address (e.g., apartment, suite)",
          required: !1
        }),
        city: import_pieces_framework4.Property.ShortText({
          displayName: "City",
          description: "City (for physical address)",
          required: !1
        }),
        region: import_pieces_framework4.Property.ShortText({
          displayName: "Region/State",
          description: "Region or state (for physical address)",
          required: !1
        }),
        postal_code: import_pieces_framework4.Property.ShortText({
          displayName: "Postal Code",
          description: "Postal code (for physical address)",
          required: !1
        }),
        po_box: import_pieces_framework4.Property.ShortText({
          displayName: "PO Box",
          description: "PO Box (for physical address)",
          required: !1
        }),
        country: import_pieces_framework4.Property.ShortText({
          displayName: "Country",
          description: "Country (for physical address)",
          required: !1
        }),
        name: import_pieces_framework4.Property.ShortText({
          displayName: "Facebook Name",
          description: "Facebook user name (for facebook type)",
          required: !1
        })
      }
    }),
    memberships: import_pieces_framework4.Property.DynamicProperties({
      auth: missiveAuth,
      displayName: "Memberships",
      description: "Organizations and groups the contact belongs to. Note: When updating memberships, all existing memberships will be replaced with the ones provided here.",
      required: !1,
      refreshers: ["contact_book"],
      props: async ({ auth, contact_book }) => {
        var _a, _b, _c, _d;
        if (!auth)
          return {
            memberships_array: import_pieces_framework4.Property.Array({
              displayName: "Memberships",
              description: "Please authenticate first",
              required: !1,
              properties: {
                placeholder: import_pieces_framework4.Property.ShortText({
                  displayName: "Authenticate First",
                  description: "Please authenticate to access memberships",
                  required: !1
                })
              }
            })
          };
        let organizationOptions = [], contactGroupOptions = [];
        try {
          organizationOptions = ((_b = (_a = (await missiveCommon.apiCall({
            auth,
            method: import_pieces_common5.HttpMethod.GET,
            resourceUri: "/organizations"
          })).body) == null ? void 0 : _a.organizations) == null ? void 0 : _b.map((org) => ({
            label: org.name,
            value: org.id
          }))) || [];
        } catch (error) {
          console.error("Failed to fetch organizations:", error);
        }
        if (contact_book)
          try {
            contactGroupOptions = ((_d = (_c = (await missiveCommon.apiCall({
              auth,
              method: import_pieces_common5.HttpMethod.GET,
              resourceUri: `/contact_groups?contact_book=${contact_book}&kind=group`
            })).body) == null ? void 0 : _c.contact_groups) == null ? void 0 : _d.map((group) => ({
              label: group.name,
              value: group.id
            }))) || [];
          } catch (error) {
            console.error("Failed to fetch contact groups:", error);
          }
        return {
          memberships_array: import_pieces_framework4.Property.Array({
            displayName: "Memberships",
            description: "Add, remove, and manage contact memberships. All existing memberships will be replaced.",
            required: !1,
            properties: {
              type: import_pieces_framework4.Property.StaticDropdown({
                displayName: "Type",
                description: "Type of membership",
                required: !0,
                options: {
                  options: [
                    { label: "Organization", value: "organization" },
                    { label: "Group", value: "group" }
                  ]
                }
              }),
              organization: import_pieces_framework4.Property.StaticDropdown({
                displayName: "Organization",
                description: "Select organization (only for Organization type)",
                required: !1,
                options: {
                  options: organizationOptions.length > 0 ? organizationOptions : [{ label: "No organizations found", value: "" }]
                }
              }),
              contact_group: import_pieces_framework4.Property.StaticDropdown({
                displayName: "Contact Group",
                description: contact_book ? "Select contact group (only for Group type)" : "Select contact book first to see groups",
                required: !1,
                options: {
                  options: contactGroupOptions.length > 0 ? contactGroupOptions : [{ label: contact_book ? "No groups found" : "Select contact book first", value: "" }]
                }
              }),
              title: import_pieces_framework4.Property.ShortText({
                displayName: "Title",
                description: "Job title or role",
                required: !1
              }),
              location: import_pieces_framework4.Property.ShortText({
                displayName: "Location",
                description: "Location or office",
                required: !1
              }),
              department: import_pieces_framework4.Property.ShortText({
                displayName: "Department",
                description: "Department",
                required: !1
              }),
              description: import_pieces_framework4.Property.ShortText({
                displayName: "Description",
                description: "Description of role or membership",
                required: !1
              })
            }
          })
        };
      }
    })
  },
  async run(context) {
    let propsValue = context.propsValue, {
      contact_id,
      contact_book,
      move_to_contact_book,
      first_name,
      last_name,
      middle_name,
      phonetic_first_name,
      phonetic_last_name,
      phonetic_middle_name,
      prefix,
      suffix,
      nickname,
      file_as,
      notes,
      starred,
      gender,
      infos
    } = propsValue, contactData = {
      id: contact_id
    };
    move_to_contact_book && (contactData.contact_book = move_to_contact_book), first_name && (contactData.first_name = first_name), last_name && (contactData.last_name = last_name), middle_name && (contactData.middle_name = middle_name), phonetic_first_name && (contactData.phonetic_first_name = phonetic_first_name), phonetic_last_name && (contactData.phonetic_last_name = phonetic_last_name), phonetic_middle_name && (contactData.phonetic_middle_name = phonetic_middle_name), prefix && (contactData.prefix = prefix), suffix && (contactData.suffix = suffix), nickname && (contactData.nickname = nickname), file_as && (contactData.file_as = file_as), notes && (contactData.notes = notes), starred !== void 0 && (contactData.starred = starred), gender && (contactData.gender = gender), infos && infos.length > 0 && (contactData.infos = infos.map((info) => {
      let infoObj = {
        kind: info.kind,
        label: info.label
      };
      return info.kind === "email" || info.kind === "phone_number" || info.kind === "twitter" || info.kind === "url" || info.kind === "custom" ? infoObj.value = info.value : info.kind === "facebook" ? infoObj.name = info.name : info.kind === "physical_address" && (info.street && (infoObj.street = info.street), info.extended_address && (infoObj.extended_address = info.extended_address), info.city && (infoObj.city = info.city), info.region && (infoObj.region = info.region), info.postal_code && (infoObj.postal_code = info.postal_code), info.po_box && (infoObj.po_box = info.po_box), info.country && (infoObj.country = info.country)), info.custom_label && info.label === "other" && (infoObj.custom_label = info.custom_label), infoObj;
    }));
    let membershipsArray = propsValue.memberships_array;
    if (membershipsArray && Array.isArray(membershipsArray) && membershipsArray.length > 0) {
      let memberships = [];
      for (let membership of membershipsArray) {
        if (!membership.type) continue;
        let membershipObj = {};
        membership.title && (membershipObj.title = membership.title), membership.location && (membershipObj.location = membership.location), membership.department && (membershipObj.department = membership.department), membership.description && (membershipObj.description = membership.description), membership.type === "organization" && membership.organization ? membershipObj.group = {
          kind: "organization",
          id: membership.organization
        } : membership.type === "group" && membership.contact_group && (membershipObj.group = {
          kind: "group",
          id: membership.contact_group
        }), membershipObj.group && memberships.push(membershipObj);
      }
      memberships.length > 0 && (contactData.memberships = memberships);
    }
    return (await missiveCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common5.HttpMethod.PATCH,
      resourceUri: `/contacts/${contact_id}`,
      body: {
        contacts: [contactData]
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/missive/src/lib/actions/create-draft-post.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createDraftPost = (0, import_pieces_framework5.createAction)({
  name: "create_draft_post",
  displayName: "Create Draft/Post",
  description: "Create a draft message or post in Missive, with option to send",
  audience: "both",
  aiMetadata: { description: "Compose a message in Missive across a chosen channel (email, SMS, WhatsApp, live chat, custom channel, Messenger, or Instagram), either saving it as a draft or sending it immediately (optionally scheduled for a future timestamp). Use to send or queue an outbound message; sender/recipient fields and any account IDs depend on the selected message type. Not idempotent: each call creates a new draft and, if send is enabled, dispatches a message.", idempotent: !1 },
  auth: missiveAuth,
  props: {
    message_type: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Message Type",
      description: "Type of message to create",
      required: !0,
      options: {
        options: [
          { label: "Email", value: "email" },
          { label: "SMS", value: "sms" },
          { label: "WhatsApp", value: "whatsapp" },
          { label: "Missive Live Chat", value: "live_chat" },
          { label: "Custom Channel", value: "custom_channel" },
          { label: "Messenger", value: "messenger" },
          { label: "Instagram", value: "instagram" }
        ]
      }
    }),
    subject: import_pieces_framework5.Property.ShortText({
      displayName: "Subject",
      description: "Subject line (primarily for email)",
      required: !1
    }),
    body: import_pieces_framework5.Property.LongText({
      displayName: "Message Body",
      description: "The content of your message (supports HTML for email)",
      required: !0
    }),
    message_fields: import_pieces_framework5.Property.DynamicProperties({
      auth: missiveAuth,
      displayName: "Message Configuration",
      description: "Configure sender, recipients, and message-specific options",
      required: !1,
      refreshers: ["message_type"],
      props: async ({ auth, message_type }) => {
        if (!auth || !message_type)
          return {
            placeholder: import_pieces_framework5.Property.ShortText({
              displayName: "Select Message Type",
              description: "Please select a message type first",
              required: !1
            })
          };
        let props = {};
        switch (props.quote_previous_message = import_pieces_framework5.Property.Checkbox({
          displayName: "Quote Previous Message",
          description: "\u26A0\uFE0F WARNING: Only use if you know the conversation content. May leak sensitive information.",
          required: !1,
          defaultValue: !1
        }), message_type) {
          case "email":
            props.from_email = import_pieces_framework5.Property.ShortText({
              displayName: "From Email",
              description: "Sender email address (must match your Missive email aliases)",
              required: !0
            }), props.from_name = import_pieces_framework5.Property.ShortText({
              displayName: "From Name",
              description: "Sender display name",
              required: !1
            }), props.to_emails = import_pieces_framework5.Property.Array({
              displayName: "To Recipients",
              description: "Email recipients",
              required: !0,
              properties: {
                address: import_pieces_framework5.Property.ShortText({
                  displayName: "Email Address",
                  required: !0
                }),
                name: import_pieces_framework5.Property.ShortText({
                  displayName: "Display Name",
                  required: !1
                })
              }
            }), props.cc_emails = import_pieces_framework5.Property.Array({
              displayName: "CC Recipients",
              description: "Carbon copy recipients",
              required: !1,
              properties: {
                address: import_pieces_framework5.Property.ShortText({
                  displayName: "Email Address",
                  required: !0
                }),
                name: import_pieces_framework5.Property.ShortText({
                  displayName: "Display Name",
                  required: !1
                })
              }
            }), props.bcc_emails = import_pieces_framework5.Property.Array({
              displayName: "BCC Recipients",
              description: "Blind carbon copy recipients",
              required: !1,
              properties: {
                address: import_pieces_framework5.Property.ShortText({
                  displayName: "Email Address",
                  required: !0
                }),
                name: import_pieces_framework5.Property.ShortText({
                  displayName: "Display Name",
                  required: !1
                })
              }
            });
            break;
          case "sms":
          case "whatsapp":
            props.from_phone = import_pieces_framework5.Property.ShortText({
              displayName: "From Phone Number",
              description: "Your phone number (must match your Twilio/SignalWire accounts). Format: +1234567890",
              required: !0
            }), message_type === "whatsapp" && (props.from_type = import_pieces_framework5.Property.StaticDropdown({
              displayName: "From Account Type",
              description: "Type of WhatsApp account",
              required: !1,
              options: {
                options: [
                  { label: "WhatsApp", value: "whatsapp" },
                  { label: "Twilio WhatsApp", value: "twilio_whatsapp" },
                  { label: "Twilio", value: "twilio" },
                  { label: "SignalWire", value: "signalwire" }
                ]
              }
            })), props.to_phone = import_pieces_framework5.Property.ShortText({
              displayName: "To Phone Number",
              description: "Recipient phone number. Format: +1234567890",
              required: !0
            }), message_type === "whatsapp" && (props.whatsapp_template_id = import_pieces_framework5.Property.ShortText({
              displayName: "WhatsApp Template ID",
              description: "Template ID for new conversations (required for users not contacted in 24h)",
              required: !1
            }), props.whatsapp_template_variables = import_pieces_framework5.Property.Object({
              displayName: "Template Variables",
              description: 'Variables for WhatsApp template (e.g., {"1": "John", "2": "Project"})',
              required: !1
            }));
            break;
          case "live_chat":
          case "custom_channel":
          case "messenger":
          case "instagram":
            (message_type === "live_chat" || message_type === "custom_channel") && (props.account_id = import_pieces_framework5.Property.ShortText({
              displayName: "Account ID",
              description: "Account ID from Missive settings > API > Resource IDs",
              required: !0
            })), props.from_id = import_pieces_framework5.Property.ShortText({
              displayName: "From ID",
              description: "Sender ID",
              required: !1
            }), props.from_username = import_pieces_framework5.Property.ShortText({
              displayName: "From Username",
              description: "Sender username (e.g., @missiveapp)",
              required: !1
            }), props.from_name = import_pieces_framework5.Property.ShortText({
              displayName: "From Name",
              description: "Sender display name",
              required: !1
            }), message_type === "messenger" || message_type === "instagram" ? props.to_id = import_pieces_framework5.Property.ShortText({
              displayName: "To ID",
              description: "Recipient ID",
              required: !0
            }) : props.to_recipients = import_pieces_framework5.Property.Array({
              displayName: "Recipients",
              description: "Message recipients",
              required: !0,
              properties: {
                id: import_pieces_framework5.Property.ShortText({
                  displayName: "Recipient ID",
                  required: !0
                }),
                username: import_pieces_framework5.Property.ShortText({
                  displayName: "Username",
                  required: !1
                }),
                name: import_pieces_framework5.Property.ShortText({
                  displayName: "Display Name",
                  required: !1
                })
              }
            });
            break;
        }
        return props;
      }
    }),
    conversation_options: import_pieces_framework5.Property.DynamicProperties({
      auth: missiveAuth,
      displayName: "Conversation Options",
      description: "Advanced conversation management options",
      required: !1,
      refreshers: [],
      props: async ({ auth }) => {
        var _a, _b, _c, _d, _e, _f;
        if (!auth)
          return {
            existing_conversation_id: import_pieces_framework5.Property.ShortText({
              displayName: "Authenticate First",
              description: "Please authenticate to access conversation options",
              required: !1
            }),
            references: import_pieces_framework5.Property.Array({
              displayName: "Message References",
              description: "Please authenticate to access conversation options",
              required: !1,
              properties: {
                reference: import_pieces_framework5.Property.ShortText({
                  displayName: "Reference ID",
                  required: !1
                })
              }
            }),
            organization: import_pieces_framework5.Property.StaticDropdown({
              displayName: "Organization",
              description: "Please authenticate first",
              required: !1,
              options: {
                disabled: !0,
                options: [{ label: "Please authenticate first", value: "" }]
              }
            }),
            team: import_pieces_framework5.Property.StaticDropdown({
              displayName: "Team",
              description: "Please authenticate first",
              required: !1,
              options: {
                disabled: !0,
                options: [{ label: "Please authenticate first", value: "" }]
              }
            }),
            force_team: import_pieces_framework5.Property.Checkbox({
              displayName: "Force Team Assignment",
              description: "Please authenticate first",
              required: !1,
              defaultValue: !1
            }),
            add_shared_labels: import_pieces_framework5.Property.StaticMultiSelectDropdown({
              displayName: "Add Shared Labels",
              description: "Please authenticate first",
              required: !1,
              options: {
                disabled: !0,
                options: [{ label: "Please authenticate first", value: "" }]
              }
            }),
            remove_shared_labels: import_pieces_framework5.Property.StaticMultiSelectDropdown({
              displayName: "Remove Shared Labels",
              description: "Please authenticate first",
              required: !1,
              options: {
                disabled: !0,
                options: [{ label: "Please authenticate first", value: "" }]
              }
            }),
            conversation_subject: import_pieces_framework5.Property.ShortText({
              displayName: "Conversation Subject",
              description: "Please authenticate first",
              required: !1
            }),
            conversation_color: import_pieces_framework5.Property.StaticDropdown({
              displayName: "Conversation Color",
              description: "Please authenticate first",
              required: !1,
              options: {
                disabled: !0,
                options: [{ label: "Please authenticate first", value: "" }]
              }
            }),
            add_to_inbox: import_pieces_framework5.Property.Checkbox({
              displayName: "Add to Inbox",
              description: "Please authenticate first",
              required: !1,
              defaultValue: !1
            }),
            add_to_team_inbox: import_pieces_framework5.Property.Checkbox({
              displayName: "Add to Team Inbox",
              description: "Please authenticate first",
              required: !1,
              defaultValue: !1
            }),
            close_conversation: import_pieces_framework5.Property.Checkbox({
              displayName: "Close Conversation",
              description: "Please authenticate first",
              required: !1,
              defaultValue: !1
            })
          };
        let organizationOptions = [], teamOptions = [], sharedLabelOptions = [];
        try {
          organizationOptions = ((_b = (_a = (await missiveCommon.apiCall({
            auth,
            method: import_pieces_common6.HttpMethod.GET,
            resourceUri: "/organizations"
          })).body) == null ? void 0 : _a.organizations) == null ? void 0 : _b.map((org) => ({
            label: org.name,
            value: org.id
          }))) || [], teamOptions = ((_d = (_c = (await missiveCommon.apiCall({
            auth,
            method: import_pieces_common6.HttpMethod.GET,
            resourceUri: "/teams"
          })).body) == null ? void 0 : _c.teams) == null ? void 0 : _d.map((team) => ({
            label: team.name,
            value: team.id
          }))) || [], sharedLabelOptions = ((_f = (_e = (await missiveCommon.apiCall({
            auth,
            method: import_pieces_common6.HttpMethod.GET,
            resourceUri: "/shared_labels"
          })).body) == null ? void 0 : _e.shared_labels) == null ? void 0 : _f.map((label) => ({
            label: label.name,
            value: label.id
          }))) || [];
        } catch (error) {
          console.error("Failed to fetch conversation options:", error);
        }
        return {
          existing_conversation_id: import_pieces_framework5.Property.ShortText({
            displayName: "Existing Conversation ID",
            description: "Add to existing conversation (optional)",
            required: !1
          }),
          references: import_pieces_framework5.Property.Array({
            displayName: "Message References",
            description: "Reference IDs to append to existing conversation",
            required: !1,
            properties: {
              reference: import_pieces_framework5.Property.ShortText({
                displayName: "Reference ID",
                description: "Message reference (e.g., <message-id@domain.com>)",
                required: !0
              })
            }
          }),
          organization: import_pieces_framework5.Property.StaticDropdown({
            displayName: "Organization",
            description: "Link conversation to organization",
            required: !1,
            options: {
              options: organizationOptions.length > 0 ? organizationOptions : [{ label: "No organizations found", value: "" }]
            }
          }),
          team: import_pieces_framework5.Property.StaticDropdown({
            displayName: "Team",
            description: "Link conversation to team",
            required: !1,
            options: {
              options: teamOptions.length > 0 ? teamOptions : [{ label: "No teams found", value: "" }]
            }
          }),
          force_team: import_pieces_framework5.Property.Checkbox({
            displayName: "Force Team Assignment",
            description: "Force new team even if conversation is in another team",
            required: !1,
            defaultValue: !1
          }),
          add_shared_labels: import_pieces_framework5.Property.StaticMultiSelectDropdown({
            displayName: "Add Shared Labels",
            description: "Shared labels to add to conversation",
            required: !1,
            options: {
              disabled: !1,
              options: sharedLabelOptions.length > 0 ? sharedLabelOptions : [{ label: "No shared labels found", value: "" }]
            }
          }),
          remove_shared_labels: import_pieces_framework5.Property.StaticMultiSelectDropdown({
            displayName: "Remove Shared Labels",
            description: "Shared labels to remove from conversation",
            required: !1,
            options: {
              disabled: !1,
              options: sharedLabelOptions.length > 0 ? sharedLabelOptions : [{ label: "No shared labels found", value: "" }]
            }
          }),
          conversation_subject: import_pieces_framework5.Property.ShortText({
            displayName: "Conversation Subject",
            description: "Subject for the conversation (if creating new)",
            required: !1
          }),
          conversation_color: import_pieces_framework5.Property.StaticDropdown({
            displayName: "Conversation Color",
            description: "Color for the conversation",
            required: !1,
            options: {
              options: [
                { label: "Good (Green)", value: "good" },
                { label: "Warning (Yellow)", value: "warning" },
                { label: "Danger (Red)", value: "danger" },
                { label: "Black", value: "#000000" },
                { label: "Blue", value: "#0066cc" },
                { label: "Purple", value: "#6600cc" }
              ]
            }
          }),
          add_to_inbox: import_pieces_framework5.Property.Checkbox({
            displayName: "Add to Inbox",
            description: "Move conversation to Inbox for everyone",
            required: !1,
            defaultValue: !1
          }),
          add_to_team_inbox: import_pieces_framework5.Property.Checkbox({
            displayName: "Add to Team Inbox",
            description: "Move conversation to team inbox (requires team selection)",
            required: !1,
            defaultValue: !1
          }),
          close_conversation: import_pieces_framework5.Property.Checkbox({
            displayName: "Close Conversation",
            description: "Close the conversation for everyone",
            required: !1,
            defaultValue: !1
          })
        };
      }
    }),
    send_immediately: import_pieces_framework5.Property.Checkbox({
      displayName: "Send Immediately",
      description: "Send the draft immediately instead of saving as draft",
      required: !1,
      defaultValue: !1
    }),
    schedule_send: import_pieces_framework5.Property.Checkbox({
      displayName: "Schedule Send",
      description: "Schedule the message to be sent later",
      required: !1,
      defaultValue: !1
    }),
    send_at_timestamp: import_pieces_framework5.Property.Number({
      displayName: "Send At (Unix Timestamp)",
      description: "Unix timestamp when to send the message (required if scheduling)",
      required: !1
    }),
    auto_followup: import_pieces_framework5.Property.Checkbox({
      displayName: "Auto Follow-up",
      description: "Cancel scheduled send if there is a reply (requires scheduling)",
      required: !1,
      defaultValue: !1
    }),
    attachments: import_pieces_framework5.Property.Array({
      displayName: "Attachments",
      description: "File attachments (up to 25 files, max 10MB total)",
      required: !1,
      properties: {
        filename: import_pieces_framework5.Property.ShortText({
          displayName: "Filename",
          description: "Name of the file (e.g., document.pdf)",
          required: !0
        }),
        base64_data: import_pieces_framework5.Property.LongText({
          displayName: "Base64 Data",
          description: "Base64-encoded file content",
          required: !0
        })
      }
    })
  },
  async run(context) {
    let propsValue = context.propsValue, {
      message_type,
      subject,
      body,
      send_immediately,
      schedule_send,
      send_at_timestamp,
      auto_followup,
      attachments
    } = propsValue, draftData = {
      body
    };
    subject && (draftData.subject = subject);
    let messageFields = propsValue.message_fields || {};
    switch (messageFields.quote_previous_message && (draftData.quote_previous_message = !0), message_type) {
      case "email":
        messageFields.from_email && (draftData.from_field = __spreadValues({
          address: messageFields.from_email
        }, messageFields.from_name && { name: messageFields.from_name })), messageFields.to_emails && Array.isArray(messageFields.to_emails) && (draftData.to_fields = messageFields.to_emails.map(
          (recipient) => __spreadValues({
            address: recipient.address
          }, recipient.name && { name: recipient.name })
        )), messageFields.cc_emails && Array.isArray(messageFields.cc_emails) && messageFields.cc_emails.length > 0 && (draftData.cc_fields = messageFields.cc_emails.map(
          (recipient) => __spreadValues({
            address: recipient.address
          }, recipient.name && { name: recipient.name })
        )), messageFields.bcc_emails && Array.isArray(messageFields.bcc_emails) && messageFields.bcc_emails.length > 0 && (draftData.bcc_fields = messageFields.bcc_emails.map(
          (recipient) => __spreadValues({
            address: recipient.address
          }, recipient.name && { name: recipient.name })
        ));
        break;
      case "sms":
      case "whatsapp":
        messageFields.from_phone && (draftData.from_field = __spreadValues({
          phone_number: messageFields.from_phone
        }, messageFields.from_type && { type: messageFields.from_type })), messageFields.to_phone && (draftData.to_fields = [
          {
            phone_number: messageFields.to_phone
          }
        ]), message_type === "whatsapp" && (messageFields.whatsapp_template_id && (draftData.external_response_id = messageFields.whatsapp_template_id), messageFields.whatsapp_template_variables && (draftData.external_response_variables = messageFields.whatsapp_template_variables));
        break;
      case "live_chat":
      case "custom_channel":
        messageFields.account_id && (draftData.account = messageFields.account_id), (messageFields.from_id || messageFields.from_username || messageFields.from_name) && (draftData.from_field = __spreadValues(__spreadValues(__spreadValues({}, messageFields.from_id && { id: messageFields.from_id }), messageFields.from_username && {
          username: messageFields.from_username
        }), messageFields.from_name && { name: messageFields.from_name })), messageFields.to_recipients && Array.isArray(messageFields.to_recipients) && (draftData.to_fields = messageFields.to_recipients.map(
          (recipient) => __spreadValues(__spreadValues(__spreadValues({}, recipient.id && { id: recipient.id }), recipient.username && { username: recipient.username }), recipient.name && { name: recipient.name })
        ));
        break;
      case "messenger":
      case "instagram":
        messageFields.to_id && (draftData.to_fields = [
          {
            id: messageFields.to_id
          }
        ]);
        break;
    }
    let conversationOptions = propsValue.conversation_options || {};
    return conversationOptions.existing_conversation_id && (draftData.conversation = conversationOptions.existing_conversation_id), conversationOptions.references && Array.isArray(conversationOptions.references) && conversationOptions.references.length > 0 && (draftData.references = conversationOptions.references.map(
      (ref) => ref.reference
    )), conversationOptions.organization && (draftData.organization = conversationOptions.organization), conversationOptions.team && (draftData.team = conversationOptions.team), conversationOptions.force_team && (draftData.force_team = !0), conversationOptions.add_shared_labels && Array.isArray(conversationOptions.add_shared_labels) && conversationOptions.add_shared_labels.length > 0 && (draftData.add_shared_labels = conversationOptions.add_shared_labels), conversationOptions.remove_shared_labels && Array.isArray(conversationOptions.remove_shared_labels) && conversationOptions.remove_shared_labels.length > 0 && (draftData.remove_shared_labels = conversationOptions.remove_shared_labels), conversationOptions.conversation_subject && (draftData.conversation_subject = conversationOptions.conversation_subject), conversationOptions.conversation_color && (draftData.conversation_color = conversationOptions.conversation_color), conversationOptions.add_to_inbox && (draftData.add_to_inbox = !0), conversationOptions.add_to_team_inbox && (draftData.add_to_team_inbox = !0), conversationOptions.close_conversation && (draftData.close = !0), send_immediately && (draftData.send = !0), schedule_send && send_at_timestamp && (draftData.send_at = send_at_timestamp, auto_followup && (draftData.auto_followup = !0)), attachments && Array.isArray(attachments) && attachments.length > 0 && (draftData.attachments = attachments.map((attachment) => ({
      filename: attachment.filename,
      base64_data: attachment.base64_data
    }))), (await missiveCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common6.HttpMethod.POST,
      resourceUri: "/drafts",
      body: {
        drafts: draftData
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/missive/src/lib/actions/create-task.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var createTask = (0, import_pieces_framework6.createAction)({
  name: "create_task",
  displayName: "Create Task",
  description: "Create a task that can be standalone or associated with a conversation",
  audience: "both",
  aiMetadata: { description: "Create a Missive task in one of two modes: a standalone task assigned to a team or specific users, or a subtask attached to a conversation (selected by ID or found/created via message references). Use to add a to-do item; standalone tasks need a team or assignees, subtasks need a parent conversation. Not idempotent: each call creates a new task.", idempotent: !1 },
  auth: missiveAuth,
  props: {
    task_type: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Task Type",
      description: "Type of task to create",
      required: !0,
      options: {
        options: [
          { label: "Standalone Task", value: "standalone" },
          { label: "Conversation Subtask", value: "subtask" }
        ]
      }
    }),
    title: import_pieces_framework6.Property.ShortText({
      displayName: "Task Title",
      description: "Title of the task (max 1000 characters)",
      required: !0
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      description: "Description of the task (max 10000 characters)",
      required: !1
    }),
    state: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Task State",
      description: "Current state of the task",
      required: !1,
      defaultValue: "todo",
      options: {
        options: [
          { label: "Todo", value: "todo" },
          { label: "In Progress", value: "in_progress" },
          { label: "Closed", value: "closed" }
        ]
      }
    }),
    due_at: import_pieces_framework6.Property.DateTime({
      displayName: "Due Date",
      description: "Due date for the task",
      required: !1
    }),
    task_configuration: import_pieces_framework6.Property.DynamicProperties({
      auth: missiveAuth,
      displayName: "Task Configuration",
      description: "Configure task assignment and organization based on task type",
      required: !1,
      refreshers: ["task_type"],
      props: async ({ auth, task_type }) => {
        var _a, _b, _c, _d, _e, _f, _g, _h;
        if (!auth || !task_type)
          return {
            placeholder: import_pieces_framework6.Property.ShortText({
              displayName: "Select Task Type",
              description: "Please select a task type first",
              required: !1
            })
          };
        let organizationOptions = [], teamOptions = [], userOptions = [], conversationOptions = [];
        try {
          if (organizationOptions = ((_b = (_a = (await missiveCommon.apiCall({
            auth,
            method: import_pieces_common7.HttpMethod.GET,
            resourceUri: "/organizations"
          })).body) == null ? void 0 : _a.organizations) == null ? void 0 : _b.map((org) => ({
            label: org.name,
            value: org.id
          }))) || [], organizationOptions.length > 0) {
            try {
              teamOptions = ((_d = (_c = (await missiveCommon.apiCall({
                auth,
                method: import_pieces_common7.HttpMethod.GET,
                resourceUri: `/teams?organization=${organizationOptions[0].value}`
              })).body) == null ? void 0 : _c.teams) == null ? void 0 : _d.map((team) => ({
                label: team.name,
                value: team.id
              }))) || [];
            } catch (error) {
              console.error("Failed to fetch teams:", error);
            }
            try {
              userOptions = ((_f = (_e = (await missiveCommon.apiCall({
                auth,
                method: import_pieces_common7.HttpMethod.GET,
                resourceUri: `/users?organization=${organizationOptions[0].value}`
              })).body) == null ? void 0 : _e.users) == null ? void 0 : _f.map((user) => ({
                label: `${user.name} (${user.email})`,
                value: user.id
              }))) || [];
            } catch (error) {
              console.error("Failed to fetch users:", error);
            }
          }
          if (task_type === "subtask")
            try {
              conversationOptions = ((_h = (_g = (await missiveCommon.apiCall({
                auth,
                method: import_pieces_common7.HttpMethod.GET,
                resourceUri: "/conversations?limit=50"
              })).body) == null ? void 0 : _g.conversations) == null ? void 0 : _h.map((conv) => {
                let subject = conv.subject || conv.latest_message_subject || "No Subject";
                return { label: subject.length > 50 ? `${subject.substring(0, 50)}...` : subject, value: conv.id };
              })) || [];
            } catch (error) {
              console.error("Failed to fetch conversations:", error);
            }
        } catch (error) {
          console.error("Failed to fetch task options:", error);
        }
        let props = {};
        return props.organization = import_pieces_framework6.Property.StaticDropdown({
          displayName: "Organization",
          description: task_type === "standalone" ? "Organization (required when using team or assignees)" : "Organization (required when adding users to conversation)",
          required: !1,
          options: {
            options: organizationOptions.length > 0 ? organizationOptions : [{ label: "No organizations found", value: "" }]
          }
        }), task_type === "standalone" ? (props.assignment_type = import_pieces_framework6.Property.StaticDropdown({
          displayName: "Assignment Type",
          description: "How to assign this standalone task (either team or specific users required)",
          required: !0,
          options: {
            options: [
              { label: "Assign to Team", value: "team" },
              { label: "Assign to Specific Users", value: "users" }
            ]
          }
        }), props.team = import_pieces_framework6.Property.StaticDropdown({
          displayName: "Team",
          description: "Team to assign the task to (only for team assignment)",
          required: !1,
          options: {
            options: teamOptions.length > 0 ? teamOptions : [{ label: "Select organization first or no teams found", value: "" }]
          }
        }), props.assignees_array = import_pieces_framework6.Property.Array({
          displayName: "Assignees",
          description: "Specific users to assign the task to (only for user assignment)",
          required: !1,
          properties: {
            user: import_pieces_framework6.Property.StaticDropdown({
              displayName: "User",
              description: "Select user to assign",
              required: !0,
              options: {
                options: userOptions.length > 0 ? userOptions : [{ label: "Select organization first or no users found", value: "" }]
              }
            })
          }
        })) : (props.conversation_selection = import_pieces_framework6.Property.StaticDropdown({
          displayName: "Conversation Selection Method",
          description: "How to select the parent conversation",
          required: !0,
          options: {
            options: [
              { label: "Select Existing Conversation", value: "existing" },
              { label: "Find/Create by References", value: "references" }
            ]
          }
        }), props.conversation = import_pieces_framework6.Property.StaticDropdown({
          displayName: "Parent Conversation",
          description: "Conversation to create subtask in (only for existing conversation)",
          required: !1,
          options: {
            options: conversationOptions.length > 0 ? conversationOptions : [{ label: "No conversations found", value: "" }]
          }
        }), props.references = import_pieces_framework6.Property.Array({
          displayName: "Message References",
          description: "References to find/create parent conversation (only for reference method)",
          required: !1,
          properties: {
            reference: import_pieces_framework6.Property.ShortText({
              displayName: "Reference ID",
              description: "Message reference (e.g., <message-id@domain.com>)",
              required: !0
            })
          }
        }), props.conversation_subject = import_pieces_framework6.Property.ShortText({
          displayName: "New Conversation Subject",
          description: "Subject for new conversation if created via references",
          required: !1
        }), props.assignees_array = import_pieces_framework6.Property.Array({
          displayName: "Task Assignees",
          description: "Users to assign this subtask to",
          required: !1,
          properties: {
            user: import_pieces_framework6.Property.StaticDropdown({
              displayName: "User",
              description: "Select user to assign",
              required: !0,
              options: {
                options: userOptions.length > 0 ? userOptions : [{ label: "Select organization first or no users found", value: "" }]
              }
            })
          }
        }), props.add_users_array = import_pieces_framework6.Property.Array({
          displayName: "Add Users to Conversation",
          description: "Additional users to add to the parent conversation",
          required: !1,
          properties: {
            user: import_pieces_framework6.Property.StaticDropdown({
              displayName: "User",
              description: "Select user to add to conversation",
              required: !0,
              options: {
                options: userOptions.length > 0 ? userOptions : [{ label: "Select organization first or no users found", value: "" }]
              }
            })
          }
        })), props;
      }
    })
  },
  async run(context) {
    let propsValue = context.propsValue, {
      task_type,
      title,
      description,
      state,
      due_at
    } = propsValue, taskData = {
      title
    };
    description && (taskData.description = description), state && (taskData.state = state), due_at && (taskData.due_at = Math.floor(new Date(due_at).getTime() / 1e3));
    let taskConfig = propsValue.task_configuration || {};
    return taskConfig.organization && (taskData.organization = taskConfig.organization), task_type === "standalone" ? taskConfig.assignment_type === "team" && taskConfig.team ? taskData.team = taskConfig.team : taskConfig.assignment_type === "users" && taskConfig.assignees_array && Array.isArray(taskConfig.assignees_array) && (taskData.assignees = taskConfig.assignees_array.map((assignee) => assignee.user)) : task_type === "subtask" && (taskData.subtask = !0, taskConfig.conversation_selection === "existing" && taskConfig.conversation ? taskData.conversation = taskConfig.conversation : taskConfig.conversation_selection === "references" && taskConfig.references && Array.isArray(taskConfig.references) && (taskData.references = taskConfig.references.map((ref) => ref.reference), taskConfig.conversation_subject && (taskData.conversation_subject = taskConfig.conversation_subject)), taskConfig.assignees_array && Array.isArray(taskConfig.assignees_array) && taskConfig.assignees_array.length > 0 && (taskData.assignees = taskConfig.assignees_array.map((assignee) => assignee.user)), taskConfig.add_users_array && Array.isArray(taskConfig.add_users_array) && taskConfig.add_users_array.length > 0 && (taskData.add_users = taskConfig.add_users_array.map((user) => user.user))), (await missiveCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common7.HttpMethod.POST,
      resourceUri: "/tasks",
      body: {
        tasks: taskData
        // Fixed: Single object, not array
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/missive/src/lib/actions/find-contact.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var findContact = (0, import_pieces_framework7.createAction)({
  name: "find_contact",
  displayName: "Find Contact",
  description: "Search for contacts by text, email, name, or any contact information",
  audience: "both",
  aiMetadata: { description: "Search a Missive contact book for contacts matching a free-text term across name, email, phone, organization, and other fields; leave the search term empty to list all contacts in the book. Use to look up or enumerate contacts before acting on them. A contact book is required; supports paging, sort order, and a count/summary/full result format. Read-only and idempotent.", idempotent: !0 },
  auth: missiveAuth,
  props: {
    contact_book: contactBookDropdown,
    search: import_pieces_framework7.Property.ShortText({
      displayName: "Search Term",
      description: "Search across all contact information including name, email, phone, organization, custom fields, notes, etc. Leave empty to get all contacts.",
      required: !1
    }),
    search_options: import_pieces_framework7.Property.DynamicProperties({
      auth: missiveAuth,
      displayName: "Search & Filter Options",
      description: "Configure how to search and filter contacts",
      required: !1,
      refreshers: [],
      props: async ({ auth }) => auth ? {
        order: import_pieces_framework7.Property.StaticDropdown({
          displayName: "Sort Order",
          description: "How to order the contact results",
          required: !1,
          defaultValue: "last_name",
          options: {
            options: [
              { label: "Last Name (A-Z)", value: "last_name" },
              { label: "Last Modified (Newest First)", value: "last_modified" }
            ]
          }
        }),
        limit: import_pieces_framework7.Property.Number({
          displayName: "Results Limit",
          description: "Maximum number of contacts to return (1-200)",
          required: !1,
          defaultValue: 50
        }),
        offset: import_pieces_framework7.Property.Number({
          displayName: "Offset",
          description: "Number of contacts to skip (for pagination)",
          required: !1,
          defaultValue: 0
        }),
        modified_since: import_pieces_framework7.Property.DateTime({
          displayName: "Modified Since",
          description: "Only return contacts modified or created after this date/time",
          required: !1
        }),
        include_deleted: import_pieces_framework7.Property.Checkbox({
          displayName: "Include Deleted Contacts",
          description: 'Include deleted contacts when using "Modified Since" filter (only shows ID and deleted status)',
          required: !1,
          defaultValue: !1
        })
      } : {
        order: import_pieces_framework7.Property.StaticDropdown({
          displayName: "Please Authenticate",
          description: "Please authenticate to access search options",
          required: !1,
          options: {
            disabled: !0,
            options: [{ label: "Please authenticate first", value: "" }]
          }
        }),
        limit: import_pieces_framework7.Property.Number({
          displayName: "Results Limit",
          description: "Please authenticate first",
          required: !1,
          defaultValue: 50
        }),
        offset: import_pieces_framework7.Property.Number({
          displayName: "Offset",
          description: "Please authenticate first",
          required: !1,
          defaultValue: 0
        }),
        modified_since: import_pieces_framework7.Property.DateTime({
          displayName: "Modified Since",
          description: "Please authenticate first",
          required: !1
        }),
        include_deleted: import_pieces_framework7.Property.Checkbox({
          displayName: "Include Deleted Contacts",
          description: "Please authenticate first",
          required: !1,
          defaultValue: !1
        })
      }
    }),
    result_format: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Result Format",
      description: "How to format the returned contact data",
      required: !1,
      defaultValue: "full",
      options: {
        options: [
          { label: "Full Contact Data", value: "full" },
          { label: "Summary Only", value: "summary" },
          { label: "Count Only", value: "count" }
        ]
      }
    })
  },
  async run(context) {
    var _a;
    let propsValue = context.propsValue, {
      contact_book,
      search,
      result_format
    } = propsValue;
    if (!contact_book)
      throw new Error("Contact book is required to search for contacts");
    let queryParams = {
      contact_book
    };
    search && search.trim() && (queryParams.search = search.trim());
    let searchOptions = propsValue.search_options || {};
    if (searchOptions.order && (queryParams.order = searchOptions.order), searchOptions.limit && searchOptions.limit > 0) {
      let limit = Math.min(Math.max(1, parseInt(searchOptions.limit)), 200);
      queryParams.limit = limit.toString();
    }
    if (searchOptions.offset && searchOptions.offset >= 0 && (queryParams.offset = parseInt(searchOptions.offset).toString()), searchOptions.modified_since) {
      let date = new Date(searchOptions.modified_since), timestamp = Math.floor(date.getTime() / 1e3);
      queryParams.modified_since = timestamp.toString();
    }
    searchOptions.include_deleted && (queryParams.include_deleted = "true");
    let contacts = ((_a = (await missiveCommon.apiCall({
      auth: context.auth,
      method: import_pieces_common8.HttpMethod.GET,
      resourceUri: "/contacts",
      queryParams
    })).body) == null ? void 0 : _a.contacts) || [];
    return result_format === "count" ? {
      total_found: contacts.length,
      search_term: search || "all contacts",
      contact_book_id: contact_book,
      filters_applied: {
        order: searchOptions.order || "last_name",
        limit: queryParams.limit || "50",
        modified_since: searchOptions.modified_since || null,
        include_deleted: searchOptions.include_deleted || !1
      }
    } : result_format === "summary" ? {
      total_found: contacts.length,
      contacts: contacts.map((contact) => {
        var _a2, _b, _c, _d, _e, _f, _g;
        let primaryEmail = (_b = (_a2 = contact.infos) == null ? void 0 : _a2.find((info) => info.kind === "email")) == null ? void 0 : _b.value, primaryPhone = (_d = (_c = contact.infos) == null ? void 0 : _c.find((info) => info.kind === "phone_number")) == null ? void 0 : _d.value, primaryOrg = (_g = (_f = (_e = contact.memberships) == null ? void 0 : _e.find(
          (membership) => {
            var _a3;
            return ((_a3 = membership.group) == null ? void 0 : _a3.kind) === "organization";
          }
        )) == null ? void 0 : _f.group) == null ? void 0 : _g.name;
        return {
          id: contact.id,
          name: [contact.first_name, contact.last_name].filter(Boolean).join(" ") || "No Name",
          first_name: contact.first_name || null,
          last_name: contact.last_name || null,
          primary_email: primaryEmail || null,
          primary_phone: primaryPhone || null,
          primary_organization: primaryOrg || null,
          starred: contact.starred || !1,
          deleted: contact.deleted || !1,
          modified_at: contact.modified_at,
          contact_book: contact.contact_book
        };
      }),
      search_info: {
        search_term: search || "all contacts",
        contact_book_id: contact_book,
        order: searchOptions.order || "last_name",
        limit: parseInt(queryParams.limit || "50"),
        offset: parseInt(queryParams.offset || "0")
      }
    } : {
      total_found: contacts.length,
      contacts,
      search_info: {
        search_term: search || "all contacts",
        contact_book_id: contact_book,
        api_endpoint: "/v1/contacts",
        filters_applied: {
          order: searchOptions.order || "last_name",
          limit: parseInt(queryParams.limit || "50"),
          offset: parseInt(queryParams.offset || "0"),
          modified_since: searchOptions.modified_since || null,
          include_deleted: searchOptions.include_deleted || !1
        }
      }
    };
  }
});

// ../ap/packages/pieces/community/missive/src/lib/triggers/new-message.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");
var newMessage = (0, import_pieces_framework8.createTrigger)({
  name: "new_message",
  displayName: "New Message",
  description: "Triggers when new messages are received (email, SMS, WhatsApp, Facebook, etc.)",
  aiMetadata: {
    description: "Fires when a message arrives on a Missive conversation across any configured channel (email, SMS, WhatsApp, Messenger, Instagram, Twitter, live chat). Delivered via a Missive webhook rule the user sets up manually; each event carries the conversation and the latest message. Use to react to inbound communications."
  },
  auth: missiveAuth,
  props: {
    setupInstructions: import_pieces_framework8.Property.MarkDown({
      value: `
## Setup Instructions

To use this trigger, you need to manually create a webhook rule in your Missive account:

### 1. Access Missive Rules
- Open Missive and go to **Settings > Rules**
- Click **"New incoming rule"** or **"New outgoing rule"**

### 2. Configure the Trigger
Choose from these message types:
- **Email** - Incoming email messages
- **SMS** - Text messages  
- **WhatsApp** - WhatsApp messages
- **Facebook Messenger** - Facebook messages
- **Instagram** - Instagram messages
- **Twitter** - Twitter messages
- **Missive Live Chat** - Live chat messages

### 3. Add Filters (Optional)
Configure filters like:
- Specific sender email addresses
- Subject line contains text
- Message content contains keywords
- Organization or team scope

### 4. Set Webhook Action
1. In the **Actions** section, select **"Webhook"**
2. Paste this URL in the **Webhook URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
3. Set **HTTP Method** to **POST**
4. Leave **Content Type** as **application/json**

### 5. Save the Rule
- Click **Save** to activate the webhook
- Missive will validate your endpoint automatically

---

**Note:** You need admin/owner permissions and a Productive plan to create rules.

**Example Use Cases:**
- Trigger when emails from specific customers arrive
- Process all WhatsApp messages for customer support
- Route urgent emails (subject contains "URGENT") to special workflows
- Log all social media interactions to your CRM
      `
    })
  },
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  sampleData: {
    rule: {
      id: "45408b30-aa3a-45n1-bh67-0a0cb8da9080",
      description: "New message webhook",
      type: "incoming_email"
    },
    conversation: {
      id: "47a57b76-df42-4d8k-927x-80dbe5d87191",
      subject: "Important customer inquiry",
      latest_message_subject: "Re: Important customer inquiry",
      organization: {
        id: "93e5e5d5-11a2-4c9b-80b8-94f3c08068cf",
        name: "Your Organization"
      },
      team: {
        id: "2f618f9e-d3d4-4a01-b7d5-57124ab366b8",
        name: "Support Team",
        organization: "93e5e5d5-11a2-4c9b-80b8-94f3c08068cf"
      },
      assignees: [
        {
          id: "6b52b6b9-9b51-46ad-a4e3-82ef3c45512c",
          name: "John Doe",
          email: "john@company.com"
        }
      ],
      shared_labels: [
        {
          id: "146ff5c4-d5la-3b63-b994-76711fn790lq",
          name: "Customer Support"
        }
      ],
      web_url: "https://mail.missiveapp.com/#inbox/conversations/47a57b76-df42-4d8k-927x-80dbe5d87191"
    },
    latest_message: {
      id: "86ef8bb8-269c-4959-a4f0-213db4e67844",
      subject: "Important customer inquiry",
      preview: "Hi there, I need help with my recent order...",
      type: "email",
      delivered_at: 1548415828,
      from_field: {
        name: "Customer Name",
        address: "customer@example.com"
      },
      to_fields: [
        {
          name: "Support Team",
          address: "support@company.com"
        }
      ]
    }
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/missive/src/lib/triggers/new-comment.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var newComment = (0, import_pieces_framework9.createTrigger)({
  name: "new_comment",
  displayName: "New Comment",
  description: "Triggers when new comments are added to conversations",
  aiMetadata: {
    description: "Fires when a team member posts an internal comment on a Missive conversation or task. Delivered via a manually configured Missive webhook rule; each event carries the conversation and the comment (author, content, mentions). Use to react to internal collaboration rather than customer-facing messages."
  },
  auth: missiveAuth,
  props: {
    setupInstructions: import_pieces_framework9.Property.MarkDown({
      value: `
## Setup Instructions

To use this trigger, you need to manually create a webhook rule in your Missive account:

### 1. Access Missive Rules
- Open Missive and go to **Settings > Rules**
- Click **"New incoming rule"** or **"New outgoing rule"**

### 2. Configure the Trigger
In the **When should rule trigger?** section:
- Select **"New comment"** from the User actions

### 3. Add Filters (Optional)
Configure filters like:
- **Specific author** - Only comments from certain team members
- **Task comments only** - Only comments on tasks
- **Content contains** - Comments with specific keywords
- **Mentions** - When specific users are mentioned
- **Organization scope** - Organization vs personal conversations

### 4. Set Webhook Action
1. In the **Actions** section, select **"Webhook"**
2. Paste this URL in the **Webhook URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
3. Set **HTTP Method** to **POST**
4. Leave **Content Type** as **application/json**

### 5. Save the Rule
- Click **Save** to activate the webhook
- Missive will validate your endpoint automatically

---

**Note:** You need admin/owner permissions and a Productive plan to create rules.

**Example Use Cases:**
- Alert managers when team members comment on critical conversations
- Track task progress through comment notifications
- Route comments with specific keywords to specialized workflows
- Log team collaboration activity to project management tools
      `
    })
  },
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  sampleData: {
    rule: {
      id: "45408b30-aa3a-45n1-bh67-0a0cb8da9080",
      description: "New comment webhook",
      type: "new_comment"
    },
    conversation: {
      id: "47a57b76-df42-4d8k-927x-80dbe5d87191",
      subject: "Project discussion",
      latest_message_subject: "Re: Project discussion",
      organization: {
        id: "93e5e5d5-11a2-4c9b-80b8-94f3c08068cf",
        name: "Your Organization"
      },
      team: {
        id: "2f618f9e-d3d4-4a01-b7d5-57124ab366b8",
        name: "Project Team",
        organization: "93e5e5d5-11a2-4c9b-80b8-94f3c08068cf"
      },
      assignees: [
        {
          id: "6b52b6b9-9b51-46ad-a4e3-82ef3c45512c",
          name: "Project Manager",
          email: "pm@company.com"
        }
      ],
      shared_labels: [
        {
          id: "146ff5c4-d5la-3b63-b994-76711fn790lq",
          name: "Active Projects"
        }
      ],
      web_url: "https://mail.missiveapp.com/#inbox/conversations/47a57b76-df42-4d8k-927x-80dbe5d87191"
    },
    comment: {
      id: "comment_12345678-abcd-1234-5678-1234567890ab",
      content: "I've reviewed the proposal and have some feedback...",
      author: {
        id: "user_12345678-abcd-1234-5678-1234567890ab",
        name: "Team Member",
        email: "member@company.com"
      },
      created_at: "2023-07-27T10:00:00+00:00",
      is_task: !1,
      mentions: ["user_87654321-dcba-4321-8765-ba0987654321"]
    }
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/missive/src/lib/triggers/new-contact.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var newContact = (0, import_pieces_framework10.createTrigger)({
  name: "new_contact",
  displayName: "New Contact",
  description: "Triggers when new contacts are added to contact books",
  aiMetadata: {
    description: "Fires when a contact is created in a Missive contact book. Delivered via a manually configured Missive webhook rule; each event carries the new contact record. Use to sync newly added people into downstream systems."
  },
  auth: missiveAuth,
  props: {
    setupInstructions: import_pieces_framework10.Property.MarkDown({
      value: `
## Setup Instructions

To use this trigger, you need to manually create a webhook rule in your Missive account:

### 1. Access Missive Rules
- Open Missive and go to **Settings > Rules**
- Click **"New incoming rule"** or **"New outgoing rule"**

### 2. Configure the Trigger
In the **When should rule trigger?** section:
- Look for **"New contact"** or **"Contact created"** in the available trigger types
- Select this option to trigger on contact creation

### 3. Add Filters (Optional)
Configure filters like:
- **Contact book** - Only contacts added to specific contact books
- **Contact owner** - Only contacts assigned to specific users
- **Has email** - Only contacts with email addresses
- **Has phone** - Only contacts with phone numbers
- **Organization membership** - Contacts belonging to specific organizations
- **Starred contacts** - Only starred contacts

### 4. Set Webhook Action
1. In the **Actions** section, select **"Webhook"**
2. Paste this URL in the **Webhook URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
3. Set **HTTP Method** to **POST**
4. Leave **Content Type** as **application/json**

### 5. Save the Rule
- Click **Save** to activate the webhook
- Missive will validate your endpoint automatically

---

**Note:** You need admin/owner permissions and a Productive plan to create rules.

**Example Use Cases:**
- Sync new contacts to your CRM automatically
- Send welcome emails to new business contacts
- Add new contacts to marketing automation workflows
- Alert sales team when new prospects are added
- Backup contact data to external systems
      `
    })
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  sampleData: {
    rule: {
      id: "45408b30-aa3a-45n1-bh67-0a0cb8da9080",
      description: "New contact webhook",
      type: "new_contact"
    },
    contact: {
      id: "contact_12345678-abcd-1234-5678-1234567890ab",
      first_name: "John",
      last_name: "Doe",
      middle_name: "Michael",
      starred: !1,
      contact_book: "487bc080-6631-4edc-830e-1d114eef4ab0",
      created_at: "2023-07-27T10:00:00+00:00",
      modified_at: 1556200645,
      infos: [
        {
          kind: "email",
          label: "work",
          value: "john.doe@company.com"
        },
        {
          kind: "phone_number",
          label: "mobile",
          value: "+1-555-123-4567"
        }
      ],
      memberships: [
        {
          title: "Sales Manager",
          location: "New York",
          group: {
            kind: "organization",
            name: "ABC Corporation"
          }
        }
      ],
      organization: {
        id: "org_12345678-abcd-1234-5678-1234567890ab",
        name: "Your Organization"
      },
      owner: {
        id: "user_12345678-abcd-1234-5678-1234567890ab",
        name: "Contact Owner",
        email: "owner@company.com"
      }
    }
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/missive/src/lib/triggers/new-contact-book.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var newContactBook = (0, import_pieces_framework11.createTrigger)({
  name: "new_contact_book",
  displayName: "New Contact Book",
  description: "Triggers when new contact books are created",
  aiMetadata: {
    description: "Fires when a new contact book is created in Missive. Delivered via a manually configured Missive webhook rule; each event carries the contact book details (name, owner, organization, permissions). Use to react to the creation of a new contact database."
  },
  auth: missiveAuth,
  props: {
    setupInstructions: import_pieces_framework11.Property.MarkDown({
      value: `
## Setup Instructions

To use this trigger, you need to manually create a webhook rule in your Missive account:

### 1. Access Missive Rules
- Open Missive and go to **Settings > Rules**
- Click **"New incoming rule"** or **"New outgoing rule"**

### 2. Configure the Trigger
In the **When should rule trigger?** section:
- Look for **"New contact book"** or **"Contact book created"** in the available trigger types
- Select this option to trigger on contact book creation

### 3. Add Filters (Optional)
Configure filters like:
- **Book owner** - Only contact books created by specific users
- **Organization scope** - Organization vs personal contact books
- **Book name contains** - Contact books with specific naming patterns

### 4. Set Webhook Action
1. In the **Actions** section, select **"Webhook"**
2. Paste this URL in the **Webhook URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
3. Set **HTTP Method** to **POST**
4. Leave **Content Type** as **application/json**

### 5. Save the Rule
- Click **Save** to activate the webhook
- Missive will validate your endpoint automatically

---

**Note:** You need admin/owner permissions and a Productive plan to create rules.

**Example Use Cases:**
- Set up automatic contact book backups when new books are created
- Initialize contact book permissions and sharing settings
- Notify administrators when teams create new contact databases
- Create corresponding contact lists in external CRM systems
- Track contact book creation for audit and compliance
      `
    })
  },
  type: import_pieces_framework11.TriggerStrategy.WEBHOOK,
  sampleData: {
    rule: {
      id: "45408b30-aa3a-45n1-bh67-0a0cb8da9080",
      description: "New contact book webhook",
      type: "new_contact_book"
    },
    contact_book: {
      id: "book_12345678-abcd-1234-5678-1234567890ab",
      name: "Sales Prospects 2024",
      description: "Potential customers for Q1 2024 campaign",
      created_at: "2023-07-27T10:00:00+00:00",
      modified_at: 1556200645,
      organization: {
        id: "org_12345678-abcd-1234-5678-1234567890ab",
        name: "Your Organization"
      },
      owner: {
        id: "user_12345678-abcd-1234-5678-1234567890ab",
        name: "Book Creator",
        email: "creator@company.com"
      },
      permissions: {
        public: !1,
        team_access: !0,
        organization_access: !1
      },
      contact_count: 0
    }
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/missive/src/lib/triggers/new-contact-group.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var newContactGroup = (0, import_pieces_framework12.createTrigger)({
  name: "new_contact_group",
  displayName: "New Contact Group",
  description: "Triggers when new contact groups are created",
  aiMetadata: {
    description: "Fires when a new contact group or organization is created within a Missive contact book. Delivered via a manually configured Missive webhook rule; each event carries the group details (name, kind, contact book, creator). Use to react to the creation of contact groupings."
  },
  auth: missiveAuth,
  props: {
    setupInstructions: import_pieces_framework12.Property.MarkDown({
      value: `
## Setup Instructions

To use this trigger, you need to manually create a webhook rule in your Missive account:

### 1. Access Missive Rules
- Open Missive and go to **Settings > Rules**
- Click **"New incoming rule"** or **"New outgoing rule"**

### 2. Configure the Trigger
In the **When should rule trigger?** section:
- Look for **"New contact group"** or **"Contact group created"** in the available trigger types
- Select this option to trigger on contact group creation

### 3. Add Filters (Optional)
Configure filters like:
- **Contact book** - Only groups created in specific contact books
- **Group type** - Organizations vs regular groups
- **Group creator** - Only groups created by specific users
- **Group name contains** - Groups with specific naming patterns

### 4. Set Webhook Action
1. In the **Actions** section, select **"Webhook"**
2. Paste this URL in the **Webhook URL** field:
\`\`\`text
{{webhookUrl}}
\`\`\`
3. Set **HTTP Method** to **POST**
4. Leave **Content Type** as **application/json**

### 5. Save the Rule
- Click **Save** to activate the webhook
- Missive will validate your endpoint automatically

---

**Note:** You need admin/owner permissions and a Productive plan to create rules.

**Example Use Cases:**
- Sync new contact groups to external CRM systems
- Set up automatic group permissions and access controls
- Create corresponding lists in marketing automation platforms
- Track contact organization for sales pipeline management
- Initialize group-specific workflows and templates
      `
    })
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  sampleData: {
    rule: {
      id: "45408b30-aa3a-45n1-bh67-0a0cb8da9080",
      description: "New contact group webhook",
      type: "new_contact_group"
    },
    contact_group: {
      id: "group_12345678-abcd-1234-5678-1234567890ab",
      name: "Enterprise Customers",
      kind: "organization",
      description: "High-value enterprise customer accounts",
      contact_book: "book_12345678-abcd-1234-5678-1234567890ab",
      created_at: "2023-07-27T10:00:00+00:00",
      modified_at: 1556200645,
      organization: {
        id: "org_12345678-abcd-1234-5678-1234567890ab",
        name: "Your Organization"
      },
      creator: {
        id: "user_12345678-abcd-1234-5678-1234567890ab",
        name: "Group Creator",
        email: "creator@company.com"
      },
      member_count: 0,
      tags: ["enterprise", "high-value", "priority"]
    }
  },
  async onEnable(context) {
  },
  async onDisable(context) {
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/missive/src/index.ts
var missive = (0, import_pieces_framework13.createPiece)({
  displayName: "Missive",
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/missive.png",
  categories: [import_pieces_framework14.PieceCategory.COMMUNICATION, import_pieces_framework14.PieceCategory.SALES_AND_CRM],
  authors: ["owuzo", "onyedikachi-david"],
  auth: missiveAuth,
  actions: [
    createContact,
    updateContact,
    createDraftPost,
    createTask,
    findContact,
    (0, import_pieces_common9.createCustomApiCallAction)({
      baseUrl: () => "https://public.missiveapp.com/v1",
      auth: missiveAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: [
    newMessage,
    newComment,
    newContact,
    newContactBook,
    newContactGroup
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  missive
});
