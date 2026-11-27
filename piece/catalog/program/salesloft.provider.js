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

// ../ap/packages/pieces/community/salesloft/src/index.ts
var index_exports = {};
__export(index_exports, {
  salesloft: () => salesloft
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/salesloft/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/salesloft/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var SALESLOFT_API_BASE = "https://api.salesloft.com/v2";
async function salesloftRequest({
  apiKey,
  method,
  path,
  queryParams,
  body
}) {
  return (await import_pieces_common.httpClient.sendRequest({
    method,
    url: `${SALESLOFT_API_BASE}${path}`,
    headers: __spreadValues({
      Authorization: `Bearer ${apiKey}`
    }, body ? { "Content-Type": "application/json" } : {}),
    queryParams,
    body
  })).body;
}
function cleanPayload(payload) {
  return Object.fromEntries(
    Object.entries(payload).filter(
      ([, v]) => v != null && v !== ""
    )
  );
}

// ../ap/packages/pieces/community/salesloft/src/lib/auth.ts
var salesloftAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: `
To obtain your Salesloft API key:
1. Log in to your Salesloft account.
2. Navigate to **Settings \u2192 Integrations \u2192 API**.
3. Click **Generate** to create a new API key.
4. Copy the key and paste it here.
  `,
  required: !0,
  validate: async ({ auth }) => {
    try {
      return (await import_pieces_common2.httpClient.sendRequest({
        method: import_pieces_common2.HttpMethod.GET,
        url: `${SALESLOFT_API_BASE}/me`,
        headers: {
          Authorization: `Bearer ${auth}`
        }
      })).status === 200 ? { valid: !0 } : {
        valid: !1,
        error: "Invalid API key \u2014 could not authenticate with Salesloft."
      };
    } catch (e) {
      return {
        valid: !1,
        error: "Invalid API key \u2014 could not authenticate with Salesloft."
      };
    }
  }
});

// ../ap/packages/pieces/community/salesloft/src/lib/actions/create-cadence-membership.ts
var import_pieces_common4 = require("@activepieces/pieces-common"), import_pieces_framework3 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/salesloft/src/lib/common/props.ts
var import_pieces_common3 = require("@activepieces/pieces-common"), import_pieces_framework2 = require("@activepieces/pieces-framework");
var personIdProp = import_pieces_framework2.Property.Dropdown({
  displayName: "Person",
  required: !0,
  refreshers: [],
  auth: salesloftAuth,
  options: async ({ auth }) => {
    var _a;
    return auth ? {
      disabled: !1,
      options: ((_a = (await salesloftRequest({
        apiKey: auth.secret_text,
        method: import_pieces_common3.HttpMethod.GET,
        path: "/people",
        queryParams: { per_page: "100" }
      })).data) != null ? _a : []).filter((p) => p.id).map((p) => {
        var _a2, _b, _c, _d;
        return {
          label: (_d = (_a2 = p.email_address) != null ? _a2 : p.display_name) != null ? _d : `${(_b = p.first_name) != null ? _b : ""} ${(_c = p.last_name) != null ? _c : ""}`.trim() || String(p.id),
          value: String(p.id)
        };
      })
    } : {
      disabled: !0,
      placeholder: "Connect your Salesloft account first.",
      options: []
    };
  }
}), cadenceIdProp = import_pieces_framework2.Property.Dropdown({
  displayName: "Cadence",
  required: !0,
  refreshers: [],
  auth: salesloftAuth,
  options: async ({ auth }) => {
    var _a;
    return auth ? {
      disabled: !1,
      options: ((_a = (await salesloftRequest({
        apiKey: auth.secret_text,
        method: import_pieces_common3.HttpMethod.GET,
        path: "/cadences",
        queryParams: { per_page: "100" }
      })).data) != null ? _a : []).filter((c) => c.id).map((c) => {
        var _a2;
        return {
          label: (_a2 = c.name) != null ? _a2 : String(c.id),
          value: String(c.id)
        };
      })
    } : {
      disabled: !0,
      placeholder: "Connect your Salesloft account first.",
      options: []
    };
  }
}), userIdProp = import_pieces_framework2.Property.Dropdown({
  displayName: "User (Owner)",
  description: "Salesloft user who will own this resource. Defaults to the authenticated user if not provided.",
  required: !1,
  refreshers: [],
  auth: salesloftAuth,
  options: async ({ auth }) => {
    var _a;
    return auth ? {
      disabled: !1,
      options: ((_a = (await salesloftRequest({
        apiKey: auth.secret_text,
        method: import_pieces_common3.HttpMethod.GET,
        path: "/users",
        queryParams: { per_page: "100" }
      })).data) != null ? _a : []).filter((u) => u.id).map((u) => {
        var _a2, _b;
        return {
          label: (_b = (_a2 = u.name) != null ? _a2 : u.email) != null ? _b : String(u.id),
          value: String(u.id)
        };
      })
    } : {
      disabled: !0,
      placeholder: "Connect your Salesloft account first.",
      options: []
    };
  }
}), accountIdProp = import_pieces_framework2.Property.Dropdown({
  displayName: "Account",
  description: "Salesloft account to associate with this person.",
  required: !1,
  refreshers: [],
  auth: salesloftAuth,
  options: async ({ auth }) => {
    var _a;
    return auth ? {
      disabled: !1,
      options: ((_a = (await salesloftRequest({
        apiKey: auth.secret_text,
        method: import_pieces_common3.HttpMethod.GET,
        path: "/accounts",
        queryParams: { per_page: "100" }
      })).data) != null ? _a : []).filter((a) => a.id).map((a) => {
        var _a2, _b;
        return {
          label: (_b = (_a2 = a.name) != null ? _a2 : a.domain) != null ? _b : String(a.id),
          value: String(a.id)
        };
      })
    } : {
      disabled: !0,
      placeholder: "Connect your Salesloft account first.",
      options: []
    };
  }
});

// ../ap/packages/pieces/community/salesloft/src/lib/actions/create-cadence-membership.ts
var createCadenceMembershipAction = (0, import_pieces_framework3.createAction)({
  name: "create_cadence_membership",
  displayName: "Add Person to Cadence",
  description: "Add a person to a cadence. The person and cadence must be visible to the authenticated user.",
  audience: "both",
  aiMetadata: {
    description: "Enrolls a Salesloft person into a cadence (sequence) so they begin receiving its steps. Use to start outreach for a contact. Requires a person ID and cadence ID, both of which must be visible to the authenticated user; an optional user ID assigns the membership owner. Not idempotent \u2014 each call creates a new membership.",
    idempotent: !1
  },
  auth: salesloftAuth,
  props: {
    person_id: personIdProp,
    cadence_id: cadenceIdProp,
    user_id: userIdProp
  },
  async run({ auth, propsValue }) {
    let body = cleanPayload({
      person_id: Number(propsValue.person_id),
      cadence_id: Number(propsValue.cadence_id),
      user_id: propsValue.user_id ? Number(propsValue.user_id) : void 0
    });
    return salesloftRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common4.HttpMethod.POST,
      path: "/cadence_memberships",
      body
    });
  }
});

// ../ap/packages/pieces/community/salesloft/src/lib/actions/create-note.ts
var import_pieces_common5 = require("@activepieces/pieces-common"), import_pieces_framework4 = require("@activepieces/pieces-framework");
var createNoteAction = (0, import_pieces_framework4.createAction)({
  name: "create_note",
  displayName: "Create Note",
  description: "Create a note associated with a person or account in Salesloft.",
  audience: "both",
  aiMetadata: {
    description: "Creates a note attached to a Salesloft person or account, depending on the chosen associated record type. Use to log context against a contact or company. Requires note content plus the record type and the numeric ID of the target record. Not idempotent \u2014 each call appends a new note.",
    idempotent: !1
  },
  auth: salesloftAuth,
  props: {
    content: import_pieces_framework4.Property.LongText({
      displayName: "Content",
      description: "The body content of the note.",
      required: !0
    }),
    associated_type: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Associated Record Type",
      description: "Whether this note is linked to a person or an account.",
      required: !0,
      options: {
        options: [
          { label: "Person", value: "person" },
          { label: "Account", value: "account" }
        ]
      }
    }),
    associated_id: import_pieces_framework4.Property.Number({
      displayName: "Associated Record ID",
      description: "The numeric ID of the person or account to attach this note to.",
      required: !0
    })
  },
  async run({ auth, propsValue }) {
    let body = cleanPayload({
      content: propsValue.content,
      associated_with_type: propsValue.associated_type,
      associated_with_id: propsValue.associated_id
    });
    return salesloftRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common5.HttpMethod.POST,
      path: "/notes",
      body
    });
  }
});

// ../ap/packages/pieces/community/salesloft/src/lib/actions/create-person.ts
var import_pieces_common6 = require("@activepieces/pieces-common"), import_pieces_framework5 = require("@activepieces/pieces-framework");
var createPersonAction = (0, import_pieces_framework5.createAction)({
  name: "create_person",
  displayName: "Create Person",
  description: "Create a new person in Salesloft.",
  audience: "both",
  aiMetadata: {
    description: "Creates a new person (contact/lead) record in Salesloft. Use when adding a prospect not yet tracked. Requires either an email address, or both a phone number and last name; the call fails if neither identifier set is supplied. Not idempotent \u2014 each call creates a separate person, so repeating risks duplicates.",
    idempotent: !1
  },
  auth: salesloftAuth,
  props: {
    email_address: import_pieces_framework5.Property.ShortText({
      displayName: "Email Address",
      description: "Primary email address. Either this or Phone + Last Name is required.",
      required: !1
    }),
    first_name: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    phone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone",
      description: "Phone number. Required along with Last Name when Email Address is not provided.",
      required: !1
    }),
    title: import_pieces_framework5.Property.ShortText({
      displayName: "Job Title",
      required: !1
    }),
    company_name: import_pieces_framework5.Property.ShortText({
      displayName: "Company Name",
      required: !1
    }),
    city: import_pieces_framework5.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    state: import_pieces_framework5.Property.ShortText({
      displayName: "State",
      required: !1
    }),
    country: import_pieces_framework5.Property.ShortText({
      displayName: "Country",
      required: !1
    }),
    linkedin_url: import_pieces_framework5.Property.ShortText({
      displayName: "LinkedIn URL",
      required: !1
    }),
    account_id: accountIdProp,
    owner_id: userIdProp
  },
  async run({ auth, propsValue }) {
    var _a, _b, _c;
    let hasEmail = (_a = propsValue.email_address) == null ? void 0 : _a.trim(), hasPhoneAndLastName = ((_b = propsValue.phone) == null ? void 0 : _b.trim()) && ((_c = propsValue.last_name) == null ? void 0 : _c.trim());
    if (!hasEmail && !hasPhoneAndLastName)
      throw new Error(
        "Either email_address or both phone and last_name must be provided to create a person."
      );
    let body = cleanPayload({
      email_address: propsValue.email_address,
      first_name: propsValue.first_name,
      last_name: propsValue.last_name,
      phone: propsValue.phone,
      title: propsValue.title,
      company_name: propsValue.company_name,
      city: propsValue.city,
      state: propsValue.state,
      country: propsValue.country,
      linkedin_url: propsValue.linkedin_url,
      account_id: propsValue.account_id ? Number(propsValue.account_id) : void 0,
      owner_id: propsValue.owner_id ? Number(propsValue.owner_id) : void 0
    });
    return salesloftRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common6.HttpMethod.POST,
      path: "/people",
      body
    });
  }
});

// ../ap/packages/pieces/community/salesloft/src/lib/actions/get-person.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), import_pieces_framework6 = require("@activepieces/pieces-framework");
var getPersonAction = (0, import_pieces_framework6.createAction)({
  name: "get_person",
  displayName: "Get Person",
  description: "Fetch a single person by ID from Salesloft.",
  audience: "both",
  aiMetadata: {
    description: "Retrieves a single Salesloft person record by its numeric person ID. Use to look up a known contact's current details. Read-only and idempotent.",
    idempotent: !0
  },
  auth: salesloftAuth,
  props: {
    person_id: personIdProp
  },
  async run({ auth, propsValue }) {
    return salesloftRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common7.HttpMethod.GET,
      path: `/people/${encodeURIComponent(String(propsValue.person_id))}`
    });
  }
});

// ../ap/packages/pieces/community/salesloft/src/lib/actions/list-cadences.ts
var import_pieces_common8 = require("@activepieces/pieces-common"), import_pieces_framework7 = require("@activepieces/pieces-framework");
var listCadencesAction = (0, import_pieces_framework7.createAction)({
  name: "list_cadences",
  displayName: "List Cadences",
  description: "Fetch a paginated list of cadences from Salesloft.",
  audience: "both",
  aiMetadata: {
    description: "Lists the available cadences (outreach sequences) in Salesloft with pagination. Use to browse cadences or resolve a cadence ID before adding a person to one. Read-only and idempotent.",
    idempotent: !0
  },
  auth: salesloftAuth,
  props: {
    per_page: import_pieces_framework7.Property.Number({
      displayName: "Results Per Page",
      description: "Number of results per page (max 100).",
      required: !1,
      defaultValue: 25
    }),
    page: import_pieces_framework7.Property.Number({
      displayName: "Page",
      description: "Page number (1-indexed).",
      required: !1,
      defaultValue: 1
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = {};
    return propsValue.per_page && (queryParams.per_page = String(propsValue.per_page)), propsValue.page && (queryParams.page = String(propsValue.page)), salesloftRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common8.HttpMethod.GET,
      path: "/cadences",
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/salesloft/src/lib/actions/list-people.ts
var import_pieces_common9 = require("@activepieces/pieces-common"), import_pieces_framework8 = require("@activepieces/pieces-framework");
var listPeopleAction = (0, import_pieces_framework8.createAction)({
  name: "list_people",
  displayName: "List People",
  description: "Fetch a paginated list of people from Salesloft.",
  audience: "both",
  aiMetadata: {
    description: "Lists people from Salesloft with pagination; leaving the email filter empty returns all people, while supplying an email address narrows results to matching contacts. Use to browse contacts or to resolve a person ID from an email before another action. Read-only and idempotent.",
    idempotent: !0
  },
  auth: salesloftAuth,
  props: {
    email_addresses: import_pieces_framework8.Property.ShortText({
      displayName: "Filter by Email Address",
      description: "An email address to filter results by. Returns only people matching this email.",
      required: !1
    }),
    per_page: import_pieces_framework8.Property.Number({
      displayName: "Results Per Page",
      description: "Number of results per page (max 100).",
      required: !1,
      defaultValue: 25
    }),
    page: import_pieces_framework8.Property.Number({
      displayName: "Page",
      description: "Page number (1-indexed).",
      required: !1,
      defaultValue: 1
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = {};
    return propsValue.per_page && (queryParams.per_page = String(propsValue.per_page)), propsValue.page && (queryParams.page = String(propsValue.page)), propsValue.email_addresses && (queryParams["email_addresses[]"] = propsValue.email_addresses.trim()), salesloftRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common9.HttpMethod.GET,
      path: "/people",
      queryParams
    });
  }
});

// ../ap/packages/pieces/community/salesloft/src/lib/actions/update-person.ts
var import_pieces_common10 = require("@activepieces/pieces-common"), import_pieces_framework9 = require("@activepieces/pieces-framework");
var updatePersonAction = (0, import_pieces_framework9.createAction)({
  name: "update_person",
  displayName: "Update Person",
  description: "Update an existing person in Salesloft.",
  audience: "both",
  aiMetadata: {
    description: "Updates fields on an existing Salesloft person identified by person ID; only the fields you supply are changed. Use when editing a known contact rather than creating one. Idempotent \u2014 applying the same field values repeatedly leaves the person in the same state.",
    idempotent: !0
  },
  auth: salesloftAuth,
  props: {
    person_id: personIdProp,
    email_address: import_pieces_framework9.Property.ShortText({
      displayName: "Email Address",
      required: !1
    }),
    first_name: import_pieces_framework9.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    last_name: import_pieces_framework9.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    phone: import_pieces_framework9.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    title: import_pieces_framework9.Property.ShortText({
      displayName: "Job Title",
      required: !1
    }),
    company_name: import_pieces_framework9.Property.ShortText({
      displayName: "Company Name",
      required: !1
    }),
    city: import_pieces_framework9.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    state: import_pieces_framework9.Property.ShortText({
      displayName: "State",
      required: !1
    }),
    country: import_pieces_framework9.Property.ShortText({
      displayName: "Country",
      required: !1
    }),
    linkedin_url: import_pieces_framework9.Property.ShortText({
      displayName: "LinkedIn URL",
      required: !1
    }),
    account_id: accountIdProp,
    owner_id: userIdProp
  },
  async run({ auth, propsValue }) {
    let body = cleanPayload({
      email_address: propsValue.email_address,
      first_name: propsValue.first_name,
      last_name: propsValue.last_name,
      phone: propsValue.phone,
      title: propsValue.title,
      company_name: propsValue.company_name,
      city: propsValue.city,
      state: propsValue.state,
      country: propsValue.country,
      linkedin_url: propsValue.linkedin_url,
      account_id: propsValue.account_id ? Number(propsValue.account_id) : void 0,
      owner_id: propsValue.owner_id ? Number(propsValue.owner_id) : void 0
    });
    return salesloftRequest({
      apiKey: auth.secret_text,
      method: import_pieces_common10.HttpMethod.PUT,
      path: `/people/${encodeURIComponent(String(propsValue.person_id))}`,
      body
    });
  }
});

// ../ap/packages/pieces/community/salesloft/src/index.ts
var salesloft = (0, import_pieces_framework10.createPiece)({
  displayName: "Salesloft",
  description: "Sales engagement platform for cadences, people, and revenue workflows.",
  auth: salesloftAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/salesloft.png",
  authors: ["Harmatta"],
  categories: [import_pieces_framework11.PieceCategory.SALES_AND_CRM],
  actions: [
    createPersonAction,
    updatePersonAction,
    getPersonAction,
    listPeopleAction,
    createCadenceMembershipAction,
    listCadencesAction,
    createNoteAction,
    (0, import_pieces_common11.createCustomApiCallAction)({
      baseUrl: () => SALESLOFT_API_BASE,
      auth: salesloftAuth,
      authMapping: async (auth) => ({
        Authorization: `Bearer ${auth.secret_text}`
      })
    })
  ],
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  salesloft
});
